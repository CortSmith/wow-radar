package main

import (
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"

	"radarlive/internal/keyring"
	"radarlive/internal/keyvault"
)

// Defaults are the server-wide per-key limits shown as placeholders in the UI.
type Defaults struct {
	LineRate   int64 `json:"lineRate"`
	LineBurst  int64 `json:"lineBurst"`
	ByteRate   int64 `json:"byteRate"`
	ByteBurst  int64 `json:"byteBurst"`
	MaxStreams int64 `json:"maxStreams"`
}

// defaultLimits mirror internal/mod/streamapi (stream-key-* settings).
var defaultLimits = Defaults{LineRate: 20_000, LineBurst: 40_000, ByteRate: 16 << 20, ByteBurst: 32 << 20, MaxStreams: 4}

type app struct {
	keyringPath string
	usagePath   string
	vault       *keyvault.Vault
	defaults    Defaults
	token       string
	log         *log.Logger
	now         func() time.Time
}

type keyView struct {
	ID             string         `json:"id"`
	Masked         string         `json:"masked"`
	Email          string         `json:"email"`
	Player         string         `json:"player"`
	Tags           []string       `json:"tags"`
	Notes          string         `json:"notes"`
	CreatedAt      time.Time      `json:"createdAt"`
	ExpiresAt      *time.Time     `json:"expiresAt"`
	Disabled       bool           `json:"disabled"`
	DisabledReason string         `json:"disabledReason"`
	Status         string         `json:"status"`
	CanReveal      bool           `json:"canReveal"`
	Limits         keyring.Limits `json:"limits"`
	Usage          usageView      `json:"usage"`
}

type keyReq struct {
	Email     string         `json:"email"`
	Player    string         `json:"player"`
	Tags      []string       `json:"tags"`
	Notes     string         `json:"notes"`
	ExpiresAt *string        `json:"expiresAt"`
	Limits    keyring.Limits `json:"limits"`
	KeepCopy  *bool          `json:"keepCopy"`
}

func (a *app) routeAPI(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/keys", a.handleList)
	mux.HandleFunc("POST /api/keys", a.handleCreate)
	mux.HandleFunc("PUT /api/keys/{id}", a.handleUpdate)
	mux.HandleFunc("POST /api/keys/{id}/disable", a.handleDisable)
	mux.HandleFunc("POST /api/keys/{id}/enable", a.handleEnable)
	mux.HandleFunc("POST /api/keys/{id}/regenerate", a.handleRegenerate)
	mux.HandleFunc("GET /api/keys/{id}/reveal", a.handleReveal)
	mux.HandleFunc("DELETE /api/keys/{id}", a.handleDelete)
	mux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) {
		writeError(w, http.StatusNotFound, "not_found", "Unknown API call.")
	})
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, code int, errCode, msg string) {
	writeJSON(w, code, map[string]string{"error": errCode, "message": msg})
}

// decodeBody reads a size-capped JSON body, rejecting unknown fields and trailing data.
func decodeBody(w http.ResponseWriter, r *http.Request, v any) bool {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "The request body is not valid.")
		return false
	}
	if _, err := dec.Token(); err != io.EOF {
		writeError(w, http.StatusBadRequest, "invalid_request", "The request body is not valid.")
		return false
	}
	return true
}

type apiError struct {
	status    int
	code, msg string
}

func (e *apiError) Error() string { return e.code + ": " + e.msg }

func invalid(msg string) error { return &apiError{http.StatusBadRequest, "invalid_request", msg} }
func notFound() error          { return &apiError{http.StatusNotFound, "not_found", "No such key."} }

func (a *app) fail(w http.ResponseWriter, err error) {
	var ae *apiError
	if errors.As(err, &ae) {
		writeError(w, ae.status, ae.code, ae.msg)
		return
	}
	a.log.Printf("error: %v", err)
	writeError(w, http.StatusInternalServerError, "internal", "The operation failed. See the radar-keys log.")
}

func validID(id string) bool {
	if len(id) != 32 {
		return false
	}
	for i := 0; i < len(id); i++ {
		c := id[i]
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

func (a *app) view(e keyring.Entry, usage map[string]usageView) keyView {
	masked := e.ID[:8] + "..."
	canReveal := false
	if k, ok, err := a.vault.Get(e.ID); err == nil && ok {
		canReveal = true
		if len(k) >= 12 {
			masked = k[:4] + "..." + k[len(k)-4:]
		}
	}
	status := "active"
	switch {
	case e.Disabled:
		status = "blocked"
	case e.ExpiresAt != nil && !a.now().Before(*e.ExpiresAt):
		status = "expired"
	}
	tags := e.Tags
	if tags == nil {
		tags = []string{}
	}
	return keyView{
		ID: e.ID, Masked: masked, Email: e.Email, Player: e.Player, Tags: tags, Notes: e.Notes,
		CreatedAt: e.CreatedAt, ExpiresAt: e.ExpiresAt, Disabled: e.Disabled, DisabledReason: e.DisabledReason,
		Status: status, CanReveal: canReveal, Limits: e.Limits, Usage: usage[e.ID],
	}
}

func (a *app) viewOne(e keyring.Entry) keyView { return a.view(e, readUsage(a.usagePath)) }

func (a *app) load() (*keyring.Keyring, error) {
	kr, err := keyring.Load(a.keyringPath)
	if errors.Is(err, os.ErrNotExist) {
		return &keyring.Keyring{Version: keyring.Version, Keys: []keyring.Entry{}}, nil
	}
	return kr, err
}

func (a *app) handleList(w http.ResponseWriter, r *http.Request) {
	kr, err := a.load()
	if err != nil {
		a.fail(w, err)
		return
	}
	keys := append([]keyring.Entry(nil), kr.Keys...)
	sort.SliceStable(keys, func(i, j int) bool { return keys[i].CreatedAt.After(keys[j].CreatedAt) })
	usage := readUsage(a.usagePath)
	out := make([]keyView, 0, len(keys))
	for _, e := range keys {
		out = append(out, a.view(e, usage))
	}
	writeJSON(w, http.StatusOK, map[string]any{"keys": out, "defaults": a.defaults})
}

// applyReq copies the editable fields of req onto e and validates e.
func applyReq(e *keyring.Entry, req *keyReq) error {
	e.Email = req.Email
	e.Player = req.Player
	e.Notes = req.Notes
	e.Tags = req.Tags
	if e.Tags == nil {
		e.Tags = []string{}
	}
	e.Limits = req.Limits
	e.ExpiresAt = nil
	if req.ExpiresAt != nil && *req.ExpiresAt != "" {
		t, err := time.Parse(time.RFC3339, *req.ExpiresAt)
		if err != nil {
			return invalid("expiresAt must be a date and time like 2026-12-31T23:59:59Z.")
		}
		t = t.UTC()
		e.ExpiresAt = &t
	}
	if err := e.Validate(); err != nil {
		return invalid(err.Error())
	}
	return nil
}

func (a *app) handleCreate(w http.ResponseWriter, r *http.Request) {
	var req keyReq
	if !decodeBody(w, r, &req) {
		return
	}
	raw, e, err := a.createKey(&req, req.KeepCopy == nil || *req.KeepCopy)
	if err != nil {
		a.fail(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"key": raw, "entry": a.viewOne(e)})
}

// createKey generates a key, stores its entry (and optionally the vault copy) and returns the raw key.
func (a *app) createKey(req *keyReq, keep bool) (string, keyring.Entry, error) {
	raw, err := keyring.GenerateKey()
	if err != nil {
		return "", keyring.Entry{}, err
	}
	e := keyring.NewEntry(raw, req.Email, a.now())
	if err := applyReq(&e, req); err != nil {
		return "", e, err
	}
	if keep {
		if err := a.vault.Put(e.ID, raw); err != nil {
			return "", e, err
		}
	}
	if err := keyring.Update(a.keyringPath, func(kr *keyring.Keyring) error {
		kr.Keys = append(kr.Keys, e)
		return nil
	}); err != nil {
		if keep {
			_ = a.vault.Delete(e.ID)
		}
		return "", e, err
	}
	return raw, e, nil
}

// mutate applies fn to the entry with the given id and returns the saved entry.
func (a *app) mutate(id string, fn func(*keyring.Entry) error) (keyring.Entry, error) {
	if !validID(id) {
		return keyring.Entry{}, notFound()
	}
	var out keyring.Entry
	err := keyring.Update(a.keyringPath, func(kr *keyring.Keyring) error {
		i := kr.Find(id)
		if i < 0 {
			return notFound()
		}
		c := kr.Keys[i]
		if err := fn(&c); err != nil {
			return err
		}
		kr.Keys[i] = c
		out = c
		return nil
	})
	return out, err
}

func (a *app) handleUpdate(w http.ResponseWriter, r *http.Request) {
	var req keyReq
	if !decodeBody(w, r, &req) {
		return
	}
	e, err := a.mutate(r.PathValue("id"), func(e *keyring.Entry) error { return applyReq(e, &req) })
	if err != nil {
		a.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, a.viewOne(e))
}

func (a *app) handleDisable(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Reason string `json:"reason"`
	}
	if r.ContentLength != 0 && !decodeBody(w, r, &req) {
		return
	}
	e, err := a.disable(r.PathValue("id"), req.Reason)
	if err != nil {
		a.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, a.viewOne(e))
}

func (a *app) disable(id, reason string) (keyring.Entry, error) {
	reason = strings.TrimSpace(reason)
	return a.mutate(id, func(e *keyring.Entry) error {
		now := a.now().UTC().Truncate(time.Second)
		e.Disabled, e.DisabledReason, e.DisabledAt = true, reason, &now
		if err := e.Validate(); err != nil {
			return invalid(err.Error())
		}
		return nil
	})
}

func (a *app) enable(id string) (keyring.Entry, error) {
	return a.mutate(id, func(e *keyring.Entry) error {
		e.Disabled, e.DisabledReason, e.DisabledAt = false, "", nil
		return nil
	})
}

func (a *app) handleEnable(w http.ResponseWriter, r *http.Request) {
	e, err := a.enable(r.PathValue("id"))
	if err != nil {
		a.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, a.viewOne(e))
}

func (a *app) handleRegenerate(w http.ResponseWriter, r *http.Request) {
	var req struct {
		KeepCopy *bool `json:"keepCopy"`
	}
	if r.ContentLength != 0 && !decodeBody(w, r, &req) {
		return
	}
	raw, e, err := a.regenerate(r.PathValue("id"), req.KeepCopy == nil || *req.KeepCopy)
	if err != nil {
		a.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"key": raw, "entry": a.viewOne(e)})
}

// regenerate swaps the key of entry id for a new one (new hash and id) and replaces the vault copy.
func (a *app) regenerate(id string, keep bool) (string, keyring.Entry, error) {
	if !validID(id) {
		return "", keyring.Entry{}, notFound()
	}
	raw, err := keyring.GenerateKey()
	if err != nil {
		return "", keyring.Entry{}, err
	}
	newID := keyring.IDFromKey(raw)
	if keep {
		if err := a.vault.Put(newID, raw); err != nil {
			return "", keyring.Entry{}, err
		}
	}
	e, err := a.mutate(id, func(e *keyring.Entry) error {
		e.ID, e.Hash = newID, keyring.HashKey(raw)
		return nil
	})
	if err != nil {
		if keep {
			_ = a.vault.Delete(newID)
		}
		return "", e, err
	}
	if err := a.vault.Delete(id); err != nil {
		a.log.Printf("warning: could not remove the old vault copy of key %s: %v", id, err)
	}
	return raw, e, nil
}

// reveal returns the stored raw key and writes the audit line (id and time only).
func (a *app) reveal(id string) (string, error) {
	if !validID(id) {
		return "", notFound()
	}
	kr, err := a.load()
	if err != nil {
		return "", err
	}
	if kr.Find(id) < 0 {
		return "", notFound()
	}
	raw, ok, err := a.vault.Get(id)
	if err != nil {
		return "", err
	}
	if !ok {
		return "", &apiError{http.StatusNotFound, "no_stored_copy", "No copy of this key is stored. Use Regenerate."}
	}
	a.log.Printf("audit: reveal key id=%s at=%s", id, a.now().UTC().Format(time.RFC3339))
	return raw, nil
}

func (a *app) handleReveal(w http.ResponseWriter, r *http.Request) {
	raw, err := a.reveal(r.PathValue("id"))
	if err != nil {
		a.fail(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]string{"key": raw})
}

func (a *app) handleDelete(w http.ResponseWriter, r *http.Request) {
	if err := a.deleteKey(r.PathValue("id")); err != nil {
		a.fail(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *app) deleteKey(id string) error {
	if !validID(id) {
		return notFound()
	}
	err := keyring.Update(a.keyringPath, func(kr *keyring.Keyring) error {
		i := kr.Find(id)
		if i < 0 {
			return notFound()
		}
		kr.Keys = append(kr.Keys[:i], kr.Keys[i+1:]...)
		return nil
	})
	if err != nil {
		return err
	}
	return a.vault.Delete(id)
}

// resolveID turns an id or a unique prefix (at least 6 characters) into a full id.
func (a *app) resolveID(prefix string) (string, error) {
	prefix = strings.ToLower(strings.TrimSpace(prefix))
	if len(prefix) < 6 {
		return "", errors.New("key id prefix must be at least 6 characters")
	}
	kr, err := a.load()
	if err != nil {
		return "", err
	}
	match := ""
	for _, e := range kr.Keys {
		if strings.HasPrefix(e.ID, prefix) {
			if match != "" {
				return "", errors.New("key id prefix is ambiguous, use more characters")
			}
			match = e.ID
		}
	}
	if match == "" {
		return "", errors.New("no key with that id")
	}
	return match, nil
}
