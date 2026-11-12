// Package readapi is the module that serves the read API under /api/v1: the 60-second sweep,
// contact search, trails, bounty, charts and status. Every route needs the view role. It reads
// through the host's default store and owns no data.
package readapi

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"radarlive/internal/core"
	"radarlive/internal/domain"
)

// Module name, configuration keys and defaults.
const (
	ModuleName         = "readapi"
	KeyQueryTimeout    = "query-timeout"
	KeyDefaultRuleset  = "default-ruleset"
	KeyCORSOrigins     = "cors-origins"
	defaultQueryBudget = 5 * time.Second
	corsHeaders        = "X-View-Key, Authorization, Content-Type"
	corsMaxAgeSec      = "600"
)

// Version is reported by /api/v1/status; the binary sets it at startup.
var Version = "dev"

// Option customises a Module.
type Option func(*Module)

// WithClock injects the clock used for uptime and as the fallback sweep time.
func WithClock(now func() time.Time) Option { return func(m *Module) { m.now = now } }

// Module registers the read routes.
type Module struct {
	host    *core.Host
	now     func() time.Time
	started time.Time
}

// NewModule returns the readapi module.
func NewModule(opts ...Option) *Module {
	m := &Module{now: time.Now}
	for _, opt := range opts {
		opt(m)
	}
	return m
}

// Name implements core.Module.
func (*Module) Name() string { return ModuleName }

// call is one read handler: it parses q, reads from the store and returns the JSON value.
type call func(ctx context.Context, st core.Store, q url.Values) (any, error)

// Init declares settings and registers every route.
func (m *Module) Init(host *core.Host) error {
	m.host = host
	m.started = m.now()
	for _, s := range []core.Setting{
		{Key: KeyQueryTimeout, Kind: core.KindDuration, Default: defaultQueryBudget.String(), Usage: "per-request store query timeout"},
		{Key: KeyDefaultRuleset, Kind: core.KindString, Default: string(domain.DefaultRuleset()), Usage: "ruleset used when a request has no rs"},
		{Key: KeyCORSOrigins, Kind: core.KindString, Usage: "comma-separated exact origins (scheme://host[:port]) allowed to call the API from a browser (empty = none; * is never honoured)"},
	} {
		if err := host.Config.Declare(s); err != nil {
			return fmt.Errorf("readapi: %w", err)
		}
	}
	routes := map[string]call{
		"/api/v1/sweep":         m.sweep,
		"/api/v1/contacts":      m.contacts,
		"/api/v1/trail":         m.trail,
		"/api/v1/bounty":        m.bounty,
		"/api/v1/bounty/player": m.bountyPlayer,
		"/api/v1/charts":        m.charts,
		"/api/v1/status":        m.status,
	}
	for path, fn := range routes {
		guarded := host.Auth.Require(core.RoleView, http.HandlerFunc(m.handler(fn)))
		if err := host.Routes.HandleFunc(http.MethodGet, path, guarded.ServeHTTP); err != nil {
			return fmt.Errorf("readapi: %w", err)
		}
		if err := host.Routes.HandleFunc(http.MethodOptions, path, m.preflight); err != nil {
			return fmt.Errorf("readapi: %w", err)
		}
	}
	return nil
}

// handler wraps fn with store lookup, timeout, headers and error mapping.
func (m *Module) handler(fn call) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		m.setCommonHeaders(w, r)
		st, err := m.host.Stores.Default()
		if err != nil {
			m.fail(w, r, fmt.Errorf("default store: %w", err))
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), m.host.Config.Duration(KeyQueryTimeout))
		defer cancel()
		out, err := fn(ctx, st, r.URL.Query())
		if err != nil {
			m.fail(w, r, err)
			return
		}
		core.WriteJSON(w, http.StatusOK, out)
	}
}

// allowedOrigin returns origin when it exactly matches one configured origin. "*", "null" and
// anything else never match, so the response never reflects an unlisted origin.
func (m *Module) allowedOrigin(origin string) (string, bool) {
	if origin == "" || origin == "*" || origin == "null" {
		return "", false
	}
	for _, o := range strings.Split(m.host.Config.String(KeyCORSOrigins), ",") {
		if o = strings.TrimSpace(o); o != "" && o != "*" && o == origin {
			return o, true
		}
	}
	return "", false
}

func (m *Module) corsConfigured() bool {
	return strings.TrimSpace(m.host.Config.String(KeyCORSOrigins)) != ""
}

// setCommonHeaders sets the API headers. CORS is exact-match only, never combined with credentials,
// and Vary: Origin is sent whenever an allowlist exists so caches cannot mix origins.
func (m *Module) setCommonHeaders(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if !m.corsConfigured() {
		return
	}
	w.Header().Add("Vary", "Origin")
	if origin, ok := m.allowedOrigin(r.Header.Get("Origin")); ok {
		w.Header().Set("Access-Control-Allow-Origin", origin)
	}
}

func (m *Module) preflight(w http.ResponseWriter, r *http.Request) {
	m.setCommonHeaders(w, r)
	origin := r.Header.Get("Origin")
	if origin == "" {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if _, ok := m.allowedOrigin(origin); !ok {
		core.WriteError(w, http.StatusForbidden, "origin_not_allowed", "origin not allowed")
		return
	}
	w.Header().Set("Access-Control-Allow-Methods", "GET, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", corsHeaders)
	w.Header().Set("Access-Control-Max-Age", corsMaxAgeSec)
	w.WriteHeader(http.StatusNoContent)
}

// fail maps an error to the standard JSON error; internal details are logged, never sent.
func (m *Module) fail(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, domain.ErrInvalid):
		m.host.Audit.EmitRequest(r, core.AuditEvent{Event: "validation_reject", Class: core.ClassPublic, Reason: "query_invalid"})
		core.WriteError(w, http.StatusBadRequest, "invalid_request", err.Error())
	case errors.Is(err, domain.ErrNotFound):
		core.WriteError(w, http.StatusNotFound, "not_found", err.Error())
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled):
		core.WriteError(w, http.StatusServiceUnavailable, "timeout", "the query took too long")
	default:
		m.host.Log.Error("read api", slog.String("path", r.URL.Path), slog.Any("err", err))
		core.WriteError(w, http.StatusInternalServerError, "internal", "internal error")
	}
}
