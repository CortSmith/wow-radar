// Package writeapi is the module that accepts pings from the ingest application at
// POST /internal/v1/ping on the internal listener (never the public one). Pings are validated, applied to the default store (idempotent by batch
// id) and, when new, published to the host's Sinks so other modules can observe them.
package writeapi

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"radarlive/internal/core"
	"radarlive/internal/domain"
	"radarlive/internal/validate"
)

// ModuleName is the module's registered name; PingPath is its only route.
const (
	ModuleName = "writeapi"
	PingPath   = "/internal/v1/ping"
	// SettingInternalListen is the loopback listener the write API is served on.
	SettingInternalListen = "internal-listen"
	defaultInternalListen = "127.0.0.1:8789"
)

// Option customises a Module.
type Option func(*Module)

// WithClock injects the clock used for the timestamp window.
func WithClock(now func() time.Time) Option { return func(m *Module) { m.now = now } }

// Module registers the ping route.
type Module struct {
	host *core.Host
	now  func() time.Time
}

// NewModule returns the writeapi module.
func NewModule(opts ...Option) *Module {
	m := &Module{now: time.Now}
	for _, opt := range opts {
		opt(m)
	}
	return m
}

// Name implements core.Module.
func (*Module) Name() string { return ModuleName }

// Init declares the internal listener and registers POST /internal/v1/ping on it, behind the ingest role.
func (m *Module) Init(host *core.Host) error {
	m.host = host
	guarded := host.Auth.Require(core.RoleIngest, http.HandlerFunc(m.ping))
	if err := host.EnsureListener(core.ClassInternal, SettingInternalListen, defaultInternalListen,
		"internal listen address for the write API (empty disables it)"); err != nil {
		return fmt.Errorf("writeapi: %w", err)
	}
	if err := host.Routes.HandleFuncOn(core.ClassInternal, http.MethodPost, PingPath, guarded.ServeHTTP); err != nil {
		return fmt.Errorf("writeapi: %w", err)
	}
	return nil
}

func (m *Module) ping(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("X-Content-Type-Options", "nosniff")
	ping, err := m.decode(w, r)
	if err != nil {
		m.fail(w, r, err)
		return
	}
	if n := ping.Sanitize(); n > 0 {
		m.host.Log.Debug("ping text repaired", slog.String("batch", ping.BatchID), slog.Int("repairs", n))
	}
	if err := ping.ValidateAt(m.now()); err != nil {
		m.fail(w, r, err)
		return
	}
	st, err := m.host.Stores.Default()
	if err != nil {
		m.fail(w, r, fmt.Errorf("default store: %w", err))
		return
	}
	res, err := st.ApplyPing(r.Context(), ping)
	if err != nil {
		m.fail(w, r, err)
		return
	}
	if !res.Duplicate {
		m.publish(r.Context(), ping)
	}
	core.WriteJSON(w, http.StatusOK, res)
}

// decode reads one JSON ping through the central validate layer: unknown fields, trailing data,
// deep nesting, long arrays and long strings are rejected, and oversized bodies map to errTooLarge.
func (m *Module) decode(w http.ResponseWriter, r *http.Request) (domain.Ping, error) {
	body := http.MaxBytesReader(w, r.Body, int64(m.host.Config.Int(core.SettingMaxBody)))
	var ping domain.Ping
	if err := validate.DecodeJSON(body, &ping, validate.JSONLimits{MaxArray: domain.PingMaxFixesPerContact}); err != nil {
		return ping, wrapBody(err)
	}
	return ping, nil
}

// errTooLarge marks a body over the cap.
var errTooLarge = errors.New("request body too large")

func wrapBody(err error) error {
	var tooBig *http.MaxBytesError
	if errors.As(err, &tooBig) {
		return errTooLarge
	}
	var ve *validate.Error
	if errors.As(err, &ve) {
		return fmt.Errorf("%w: bad ping body: %s", domain.ErrInvalid, ve.Msg)
	}
	return fmt.Errorf("%w: bad ping body", domain.ErrInvalid)
}

// publish hands an accepted ping to the sinks; a sink failure is logged but does not fail the write.
func (m *Module) publish(ctx context.Context, ping domain.Ping) {
	if err := m.host.Sinks.Publish(ctx, ping); err != nil {
		m.host.Log.Warn("ping sink failed", slog.String("batch", ping.BatchID), slog.Any("err", err))
	}
}

func (m *Module) fail(w http.ResponseWriter, r *http.Request, err error) {
	w.Header().Set("X-Content-Type-Options", "nosniff")
	switch {
	case errors.Is(err, errTooLarge):
		m.host.Audit.EmitRequest(r, core.AuditEvent{Event: "validation_reject", Class: core.ClassInternal, Reason: "ping_too_large"})
		core.WriteError(w, http.StatusRequestEntityTooLarge, "too_large", "request body too large")
	case errors.Is(err, domain.ErrInvalid):
		m.host.Audit.EmitRequest(r, core.AuditEvent{Event: "validation_reject", Class: core.ClassInternal, Reason: "ping_invalid"})
		core.WriteError(w, http.StatusBadRequest, "invalid_request", err.Error())
	case errors.Is(err, domain.ErrStorageFull):
		m.host.Log.Warn("write refused: storage full")
		core.WriteError(w, http.StatusInsufficientStorage, "insufficient_storage", "the database is full; writes are paused")
	case errors.Is(err, domain.ErrDegraded):
		m.host.Log.Error("write refused: store degraded (integrity check failed); restore a backup, see docs")
		core.WriteError(w, http.StatusServiceUnavailable, "store_degraded", "the database failed an integrity check; writes are paused")
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled):
		core.WriteError(w, http.StatusServiceUnavailable, "timeout", "the write took too long")
	default:
		m.host.Log.Error("write api", slog.Any("err", err))
		core.WriteError(w, http.StatusInternalServerError, "internal", "internal error")
	}
}
