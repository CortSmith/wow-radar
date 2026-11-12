// Package limits is the guard module that hardens the HTTP servers against resource exhaustion:
// server timeouts and header size (a core.ServerTuner), a cap on concurrent connections (a
// listener wrapper), a cap on in-flight requests answered with 503 and Retry-After, per-route body
// caps, and the deadline exemption of long-lived stream routes. It only uses core's Server socket,
// so deleting its line from a binary's modules.go removes every one of these controls cleanly.
package limits

import (
	"context"
	"net"
	"net/http"
	"sync/atomic"

	"radarlive/internal/core"
)

// ModuleName is the module's registered name.
const ModuleName = "limits"

// DefaultStreamPaths are the POST routes that stream for a long time.
var DefaultStreamPaths = []string{"/v1/stream"}

// DefaultBodyCaps maps route prefixes to body caps in bytes (tighter than the global cap).
func DefaultBodyCaps() map[string]int64 {
	return map[string]int64{"/internal/": 2 << 20, "/api/": 1 << 10}
}

// Option customises New.
type Option func(*Module)

// WithStreamPaths replaces the stream routes exempt from read/write deadlines.
func WithStreamPaths(paths ...string) Option { return func(m *Module) { m.routes.streamPaths = paths } }

// WithBodyCaps replaces the per-route body caps (prefix to bytes).
func WithBodyCaps(caps map[string]int64) Option { return func(m *Module) { m.routes.bodyCaps = caps } }

// Module wires the limits into the host's Server socket.
type Module struct {
	host   *core.Host
	cfg    settings
	routes routeLimits
}

// New returns the limits module.
func New(opts ...Option) *Module {
	m := &Module{routes: routeLimits{streamPaths: DefaultStreamPaths, bodyCaps: DefaultBodyCaps()}}
	for _, opt := range opts {
		opt(m)
	}
	return m
}

// Name implements core.Module.
func (*Module) Name() string { return ModuleName }

// Init declares the settings and registers the server hooks.
func (m *Module) Init(host *core.Host) error {
	m.host = host
	if err := declare(host.Config); err != nil {
		return err
	}
	host.Server.Tune(m.tune)
	host.Server.WrapListener(m.wrapListener)
	host.Server.WrapHandler(m.wrapHandler)
	return nil
}

// Start validates the configuration; the host does not listen when it is unusable.
func (m *Module) Start(context.Context) error {
	cfg, err := load(m.host.Config)
	if err != nil {
		return err
	}
	m.cfg = cfg
	m.routes.streamIdle = cfg.streamIdle
	return nil
}

func (m *Module) tune(_ string, srv *http.Server) {
	srv.ReadHeaderTimeout = m.cfg.readHeader
	srv.ReadTimeout = m.cfg.read
	srv.WriteTimeout = m.cfg.write
	srv.IdleTimeout = m.cfg.idle
	srv.MaxHeaderBytes = m.cfg.maxHeader
}

func (m *Module) wrapListener(class string, ln net.Listener) net.Listener {
	cl := newCapListener(ln, m.cfg.maxConns, m.host.Log, class, m.host.Audit)
	labels := map[string]string{"class": class}
	m.host.Metrics.MustAdd(core.Metric{Name: "radar_connections", Help: "Open connections per listener.", Kind: core.Gauge, Labels: labels,
		Value: func() float64 { return float64(len(cl.slots)) }})
	m.host.Metrics.MustAdd(core.Metric{Name: "radar_connections_refused_total", Help: "Connections refused at the connection cap.", Kind: core.Counter, Labels: labels,
		Value: func() float64 { return float64(cl.refused.Load()) }})
	return cl
}

func (m *Module) wrapHandler(class string, next http.Handler) http.Handler {
	current := new(atomic.Int64)
	m.host.Metrics.MustAdd(core.Metric{Name: "radar_inflight_requests", Help: "Requests being served per listener (streams excluded).", Kind: core.Gauge,
		Labels: map[string]string{"class": class}, Value: func() float64 { return float64(current.Load()) }})
	return inflight(m.cfg.maxInflight, m.routes.isStream, current, m.host.Audit, class, m.routes.wrap(next))
}
