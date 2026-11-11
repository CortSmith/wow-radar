package server

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"nearbyradar/internal/dataset"
)

// RulesetSource is what the server needs to report and switch the active server rule-set.
// *dataset.Registry implements it, and also the History/Bounty sources, so the very same
// object answers /api/players, /api/history, /api/bounty... for the active rule-set only.
type RulesetSource interface {
	Brief() dataset.Brief
	Status() dataset.Status
	Select(sel string) (dataset.Status, error)
}

const maxRulesetBody = 1 << 10

func (s *Server) registerRuleset(mux *http.ServeMux) {
	if s.opt.Rulesets != nil {
		mux.HandleFunc("/api/ruleset", s.ruleset)
	}
}

// ruleset: GET reports the active rule-set and per-rule-set counts; POST {"ruleset":"rp"|"auto"|...}
// switches it (same CSRF rules as the other POST endpoints).
func (s *Server) ruleset(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet, http.MethodHead:
		writeJSON(w, s.opt.Rulesets.Status())
	case http.MethodPost:
		if !csrfSafe(r) {
			http.Error(w, "forbidden: send the X-Radar: 1 header or application/json", http.StatusForbidden)
			return
		}
		var req struct {
			Ruleset string `json:"ruleset"`
		}
		dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxRulesetBody))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&req); err != nil {
			var mb *http.MaxBytesError
			if errors.As(err, &mb) {
				http.Error(w, "request body too large", http.StatusRequestEntityTooLarge)
				return
			}
			http.Error(w, "invalid JSON body", http.StatusBadRequest)
			return
		}
		if _, err := dec.Token(); !errors.Is(err, io.EOF) {
			http.Error(w, "invalid JSON body", http.StatusBadRequest)
			return
		}
		st, err := s.opt.Rulesets.Select(req.Ruleset)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		writeJSON(w, st)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}
