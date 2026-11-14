package client

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
)

// settingsView is what GET /api/settings returns: the settings plus facts the page needs. The ingest
// key itself is never part of it, only where it came from.
type settingsView struct {
	Settings
	KeySource string `json:"keySource"`
	KeySaved  bool   `json:"keySaved"`   // a key is remembered on this device
	CanSave   bool   `json:"canSaveKey"` // "Remember on this device" is available
	CanBrowse bool   `json:"canBrowse"`
	CanQuit   bool   `json:"canQuit"`
	Version   string `json:"version"`
	Warning   string `json:"warning"`
}

// startRequest is the body of POST /api/start: the settings and, optionally, a key typed this run.
type startRequest struct {
	Settings
	Key      string `json:"key"`
	Remember bool   `json:"remember"` // also store the typed key, sealed, for the next runs
}

type startResponse struct {
	Status
	Warning string `json:"warning"` // e.g. the settings could not be saved; the upload runs anyway
}

type keyRequest struct {
	Key      string `json:"key"`
	Remember bool   `json:"remember"`
}

type pickRequest struct {
	Current string `json:"current"`
}

// CanBrowse reports whether p is a real file dialog (not the typed-path fallback).
func CanBrowse(p FilePicker) bool {
	_, fallback := p.(UnsupportedPicker)
	return p != nil && !fallback
}

func (u *UIServer) routeAPI(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/settings", u.handleGetSettings)
	mux.HandleFunc("PUT /api/settings", u.handlePutSettings)
	mux.HandleFunc("PUT /api/key", u.handlePutKey)
	mux.HandleFunc("DELETE /api/key/saved", u.handleForgetKey)
	mux.HandleFunc("POST /api/start", u.handleStart)
	mux.HandleFunc("POST /api/stop", u.handleStop)
	mux.HandleFunc("GET /api/status", u.handleStatus)
	mux.HandleFunc("POST /api/pick-file", u.handlePickFile)
	mux.HandleFunc("POST /api/quit", u.handleQuit)
}

func (u *UIServer) view() settingsView {
	u.mu.Lock()
	cur := u.current
	u.mu.Unlock()
	return settingsView{
		Settings: cur, KeySource: u.opts.Keys.Source(), KeySaved: u.opts.Keys.HasSaved(), CanSave: u.opts.Keys.CanSave(), CanBrowse: u.opts.CanBrowse,
		CanQuit: u.opts.Quit != nil, Version: u.opts.Version, Warning: u.opts.SettingsWarn,
	}
}

func (u *UIServer) setCurrent(s Settings) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.current = s
}

func (u *UIServer) handleGetSettings(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, u.view())
}

func (u *UIServer) handlePutSettings(w http.ResponseWriter, r *http.Request) {
	var s Settings
	if !readJSON(w, r, &s) {
		return
	}
	s = s.Normalized()
	if err := s.Validate(); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_settings", err.Error())
		return
	}
	u.setCurrent(s)
	if warn := u.save(s); warn != "" {
		writeError(w, http.StatusInternalServerError, "save_failed", warn)
		return
	}
	writeJSON(w, http.StatusOK, u.view())
}

// save writes the settings file and returns a message when that fails ("" = saved or not wanted).
func (u *UIServer) save(s Settings) string {
	if u.opts.SettingsPath == "" {
		return ""
	}
	if err := SaveSettings(u.opts.SettingsPath, s); err != nil {
		return "Your settings could not be saved: " + err.Error()
	}
	return ""
}

func (u *UIServer) handlePutKey(w http.ResponseWriter, r *http.Request) {
	var req keyRequest
	if !readJSON(w, r, &req) {
		return
	}
	u.opts.Keys.Set(req.Key)
	if req.Remember {
		if err := u.opts.Keys.Remember(req.Key); err != nil {
			writeError(w, http.StatusInternalServerError, "save_key_failed", "The key could not be saved: "+err.Error())
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]string{"keySource": u.opts.Keys.Source()})
}

func (u *UIServer) handleForgetKey(w http.ResponseWriter, _ *http.Request) {
	if err := u.opts.Keys.Forget(); err != nil {
		writeError(w, http.StatusInternalServerError, "forget_key_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, u.view())
}

func (u *UIServer) handleStart(w http.ResponseWriter, r *http.Request) {
	var req startRequest
	if !readJSON(w, r, &req) {
		return
	}
	s := req.Settings.Normalized()
	if err := s.ValidateForStart(); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_settings", err.Error())
		return
	}
	keyWarn := ""
	if req.Key != "" {
		u.opts.Keys.Set(req.Key)
		if req.Remember {
			if err := u.opts.Keys.Remember(req.Key); err != nil {
				keyWarn = "The key could not be saved: " + err.Error() + " "
			}
		}
	}
	err := u.opts.Session.Start(StartConfig{Settings: s, Key: u.opts.Keys.Key(), Source: u.opts.Source})
	if errors.Is(err, ErrAlreadyRunning) {
		writeError(w, http.StatusConflict, "already_running", err.Error())
		return
	}
	if err != nil {
		writeError(w, http.StatusBadRequest, "cannot_start", err.Error())
		return
	}
	u.setCurrent(s)
	writeJSON(w, http.StatusAccepted, startResponse{Status: u.opts.Session.Status(), Warning: strings.TrimSpace(keyWarn + u.save(s))})
}

func (u *UIServer) handleStop(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, u.opts.Session.Stop())
}

func (u *UIServer) handleStatus(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, u.opts.Session.Status())
}

func (u *UIServer) handlePickFile(w http.ResponseWriter, r *http.Request) {
	var req pickRequest
	if !readJSON(w, r, &req) {
		return
	}
	path, err := u.opts.Picker.Pick(r.Context(), PickOptions{InitialPath: req.Current})
	switch {
	case errors.Is(err, ErrPickCancelled):
		writeJSON(w, http.StatusOK, map[string]any{"cancelled": true})
	case errors.Is(err, ErrPickerUnsupported):
		writeError(w, http.StatusNotImplemented, "no_dialog", "This system has no file dialog; type the path instead.")
	case err != nil:
		writeError(w, http.StatusInternalServerError, "dialog_failed", err.Error())
	default:
		writeJSON(w, http.StatusOK, map[string]string{"path": path})
	}
}

func (u *UIServer) handleQuit(w http.ResponseWriter, _ *http.Request) {
	if u.opts.Quit == nil {
		writeError(w, http.StatusNotFound, "not_available", "Quit is only offered in browser mode.")
		return
	}
	w.WriteHeader(http.StatusNoContent)
	go u.opts.Quit()
}

// readJSON decodes the request body into v (strictly, with a size cap). On failure it has already
// answered the request and returns false.
func readJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes))
	dec.DisallowUnknownFields()
	err := dec.Decode(v)
	if err == nil {
		err = expectEnd(dec)
	}
	if err == nil {
		return true
	}
	var tooBig *http.MaxBytesError
	if errors.As(err, &tooBig) {
		writeError(w, http.StatusRequestEntityTooLarge, "too_large", "The request is too large.")
		return false
	}
	writeError(w, http.StatusBadRequest, "bad_request", "The request body is not valid: "+err.Error())
	return false
}

// expectEnd fails when anything but whitespace follows the JSON value.
func expectEnd(dec *json.Decoder) error {
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("unexpected data after the JSON value")
		}
		return err
	}
	return nil
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, code int, errCode, message string) {
	writeJSON(w, code, map[string]string{"error": errCode, "message": message})
}
