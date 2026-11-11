package server

import (
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strconv"

	"nearbyradar/internal/bounty"
)

// BountySource is what the server needs from the bounty store.
type BountySource interface {
	Leaderboard(limit int) []bounty.Row
	Player(guid string) (bounty.Detail, bool)
	Stats() bounty.Stats
}

// BountyJobs starts and reports the background reverse analysis of old logs.
type BountyJobs interface {
	Start(names []string) (string, error)
	Status() bounty.Status
}

const (
	bountyDefaultLimit = 50
	bountyMaxLimit     = 500
	maxGUIDLen         = 128
	maxAnalyzeBody     = 64 << 10
	csrfHeader         = "X-Radar"
)

func (s *Server) registerBounty(mux *http.ServeMux) {
	if s.opt.Bounty != nil {
		mux.HandleFunc("/api/bounty", s.bountyBoard)
		mux.HandleFunc("/api/bounty/player", s.bountyPlayer)
	}
	if s.opt.Analyzer != nil {
		mux.HandleFunc("/api/bounty/analyze", s.bountyAnalyze)
	}
}

func getOnly(w http.ResponseWriter, r *http.Request) bool {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return false
	}
	return true
}

// bountyBoard serves GET /api/bounty?limit=N: {rows:[...], stats:{...}}.
func (s *Server) bountyBoard(w http.ResponseWriter, r *http.Request) {
	if !getOnly(w, r) {
		return
	}
	limit := bountyDefaultLimit
	if v := r.URL.Query().Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 {
			http.Error(w, "limit must be a positive integer", http.StatusBadRequest)
			return
		}
		limit = min(n, bountyMaxLimit)
	}
	rows := s.opt.Bounty.Leaderboard(limit)
	if rows == nil {
		rows = []bounty.Row{}
	}
	writeJSON(w, struct {
		Rows  []bounty.Row `json:"rows"`
		Stats bounty.Stats `json:"stats"`
	}{rows, s.opt.Bounty.Stats()})
}

// bountyPlayer serves GET /api/bounty/player?guid=...: one hostile player with recent death records.
func (s *Server) bountyPlayer(w http.ResponseWriter, r *http.Request) {
	if !getOnly(w, r) {
		return
	}
	g := r.URL.Query().Get("guid")
	if g == "" || len(g) > maxGUIDLen {
		http.Error(w, "guid is required (max 128 bytes)", http.StatusBadRequest)
		return
	}
	d, ok := s.opt.Bounty.Player(g)
	if !ok {
		http.Error(w, "unknown guid", http.StatusNotFound)
		return
	}
	writeJSON(w, d)
}

// bountyAnalyze: GET reports the job status; POST starts a job (202) unless one is running (409).
func (s *Server) bountyAnalyze(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet, http.MethodHead:
		writeJSON(w, s.opt.Analyzer.Status())
	case http.MethodPost:
		s.startAnalyze(w, r)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// csrfSafe accepts only requests a cross-site page cannot forge without a CORS preflight
// (which this server never grants): a custom header, or a JSON content type. A foreign Origin
// is always refused.
func csrfSafe(r *http.Request) bool {
	if o := r.Header.Get("Origin"); o != "" {
		u, err := url.Parse(o)
		if err != nil {
			return false
		}
		switch u.Hostname() {
		case "127.0.0.1", "localhost", "::1":
		default:
			return false
		}
	}
	if r.Header.Get(csrfHeader) != "" {
		return true
	}
	mt, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	return err == nil && mt == "application/json"
}

func (s *Server) startAnalyze(w http.ResponseWriter, r *http.Request) {
	if !csrfSafe(r) {
		http.Error(w, "forbidden: send the X-Radar: 1 header or application/json", http.StatusForbidden)
		return
	}
	var req struct {
		Files []string `json:"files"`
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxAnalyzeBody))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil && !errors.Is(err, io.EOF) {
		var mb *http.MaxBytesError
		if errors.As(err, &mb) {
			http.Error(w, "request body too large", http.StatusRequestEntityTooLarge)
			return
		}
		http.Error(w, "invalid JSON body", http.StatusBadRequest)
		return
	} else if err == nil {
		if _, err := dec.Token(); !errors.Is(err, io.EOF) {
			http.Error(w, "invalid JSON body", http.StatusBadRequest)
			return
		}
	}
	var names []string // nil = every combat log in the logs folder
	if len(req.Files) > 0 {
		names = req.Files
	}
	id, err := s.opt.Analyzer.Start(names)
	var bad *bounty.BadFileError
	switch {
	case errors.Is(err, bounty.ErrBusy):
		http.Error(w, err.Error(), http.StatusConflict)
	case errors.As(err, &bad):
		http.Error(w, err.Error(), http.StatusBadRequest)
	case err != nil:
		http.Error(w, err.Error(), http.StatusInternalServerError)
	default:
		b, _ := json.Marshal(map[string]any{"jobId": id, "status": s.opt.Analyzer.Status()})
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write(b)
	}
}
