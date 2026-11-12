// Package ratelimit is the guard module that limits request rates with token buckets: one budget per
// route class per client address (IPv6 aggregated to the /64) and, for the write and ingest classes,
// one per API key (identified by a SHA-256-derived id, never the key itself), plus a budget for
// failed authentications. Memory is fixed: a sharded table with TTL eviction and a hard cap, and a
// shared bucket instead of allocation when it is full. Over-budget requests get 429 with
// Retry-After. It only uses core's Server socket (WrapHandler), so deleting its line from a
// binary's modules.go removes the controls cleanly.
package ratelimit

import (
	"context"
	"net/http"
	"time"

	"radarlive/internal/core"
)

// ModuleName is the module's registered name.
const ModuleName = "ratelimit"

// Option customises New.
type Option func(*Module)

// WithClock replaces the time source (tests use a fake clock).
func WithClock(now func() time.Time) Option { return func(m *Module) { m.now = now } }

// Module wires the limiter into the host's Server socket.
type Module struct {
	host *core.Host
	now  func() time.Time
	lim  *Limiter
}

// New returns the ratelimit module.
func New(opts ...Option) *Module {
	m := &Module{}
	for _, opt := range opts {
		opt(m)
	}
	return m
}

// Name implements core.Module.
func (*Module) Name() string { return ModuleName }

// Init declares the settings and registers the handler hook.
func (m *Module) Init(host *core.Host) error {
	m.host = host
	if err := declare(host.Config); err != nil {
		return err
	}
	host.Server.WrapHandler(m.wrapHandler)
	return nil
}

// Start validates the configuration; the host does not listen when it is unusable.
func (m *Module) Start(context.Context) error {
	cfg, err := load(m.host.Config)
	if err != nil {
		return err
	}
	m.lim = newLimiter(cfg, m.now)
	m.lim.audit = m.host.Audit
	if !cfg.enabled {
		m.host.Log.Warn("rate limiting is disabled", "setting", SettingEnabled)
		m.host.Audit.Emit(core.AuditEvent{Event: "config_warning", Reason: "ratelimit_disabled"})
	}
	if len(cfg.trusted) > 0 {
		m.host.Log.Info("trusting forwarded client addresses from proxies", "count", len(cfg.trusted))
	}
	return nil
}

func (m *Module) wrapHandler(_ string, next http.Handler) http.Handler { return m.lim.Wrap(next) }
