package relay

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"radarlive/internal/core"
	"radarlive/internal/domain"
)

// Module name and configuration keys.
const (
	ModuleName        = "relay"
	SettingURL        = "db-url"
	SettingKey        = "db-key"
	SettingBufferPing = "relay-buffer-pings"
	SettingBufferByte = "relay-buffer-bytes"
	SettingBatchPings = "relay-batch-pings"
	SettingTimeout    = "relay-timeout"
	SettingBackoffMin = "relay-backoff-min"
	SettingBackoffMax = "relay-backoff-max"

	defaultBufferPings = 5000
	defaultBufferBytes = 64 << 20
	defaultBatchPings  = 50
	defaultTimeout     = 10 * time.Second
	defaultBackoffMin  = 500 * time.Millisecond
	defaultBackoffMax  = 30 * time.Second
)

// Module plugs a Relay into the Sinks socket. Settings are read in Start, after the host loaded them.
type Module struct {
	host *core.Host

	mu     sync.Mutex
	relay  *Relay
	cancel context.CancelFunc
	done   chan struct{}
}

// NewModule returns the relay module.
func NewModule() *Module { return &Module{} }

// Name implements core.Module.
func (*Module) Name() string { return ModuleName }

// Init declares the settings and registers the sink.
func (m *Module) Init(host *core.Host) error {
	m.host = host
	for _, s := range []core.Setting{
		{Key: SettingURL, Kind: core.KindString, Usage: "base URL of the radar-db INTERNAL listener, e.g. http://127.0.0.1:8789"},
		{Key: SettingKey, Kind: core.KindString, Secret: true, Usage: "ingest key sent to radar-db as X-Ingest-Key"},
		{Key: SettingBufferPing, Kind: core.KindInt, Default: fmt.Sprint(defaultBufferPings), Usage: "max pings buffered while radar-db is unreachable (oldest dropped)"},
		{Key: SettingBufferByte, Kind: core.KindInt, Default: fmt.Sprint(defaultBufferBytes), Usage: "max encoded bytes buffered"},
		{Key: SettingBatchPings, Kind: core.KindInt, Default: fmt.Sprint(defaultBatchPings), Usage: "max pings sent per pass"},
		{Key: SettingTimeout, Kind: core.KindDuration, Default: defaultTimeout.String(), Usage: "per-request timeout to radar-db"},
		{Key: SettingBackoffMin, Kind: core.KindDuration, Default: defaultBackoffMin.String(), Usage: "first retry delay"},
		{Key: SettingBackoffMax, Kind: core.KindDuration, Default: defaultBackoffMax.String(), Usage: "longest retry delay"},
	} {
		if err := host.Config.Declare(s); err != nil {
			return fmt.Errorf("relay settings: %w", err)
		}
	}
	host.Sinks.Add(moduleSink{m})
	for _, s := range []struct {
		name, help string
		kind       core.MetricKind
		value      func(Stats) float64
	}{
		{"radar_relay_dropped_total", "Pings pushed out of the full relay buffer (oldest first).", core.Counter, func(s Stats) float64 { return float64(s.Dropped) }},
		{"radar_relay_pending", "Pings waiting in the relay buffer.", core.Gauge, func(s Stats) float64 { return float64(s.Pending) }},
		{"radar_relay_buffer_bytes", "Bytes waiting in the relay buffer.", core.Gauge, func(s Stats) float64 { return float64(s.Bytes) }},
		{"radar_relay_sent_total", "Pings delivered to radar-db.", core.Counter, func(s Stats) float64 { return float64(s.Sent) }},
		{"radar_relay_retries_total", "Delivery retries.", core.Counter, func(s Stats) float64 { return float64(s.Retries) }},
	} {
		host.Metrics.MustAdd(core.Metric{Name: s.name, Help: s.help, Kind: s.kind, Value: func() float64 { return s.value(m.Stats()) }})
	}
	return nil
}

// Start builds the relay from the loaded configuration and launches its sender.
func (m *Module) Start(ctx context.Context) error {
	c := m.host.Config
	r, err := New(Config{
		URL: c.String(SettingURL), Key: c.String(SettingKey),
		BufferPings: c.Int(SettingBufferPing), BufferBytes: c.Int(SettingBufferByte), BatchPings: c.Int(SettingBatchPings),
		Timeout: c.Duration(SettingTimeout), BackoffMin: c.Duration(SettingBackoffMin), BackoffMax: c.Duration(SettingBackoffMax),
	}, m.host.Log, Options{})
	if err != nil {
		return err
	}
	loopCtx, cancel := context.WithCancel(ctx)
	m.mu.Lock()
	m.relay, m.cancel, m.done = r, cancel, make(chan struct{})
	m.mu.Unlock()
	go func() { defer close(m.done); r.Run(loopCtx) }()
	return nil
}

// Stop ends the sender and drains the buffer until ctx (the shutdown allowance) ends.
func (m *Module) Stop(ctx context.Context) error {
	m.mu.Lock()
	r, cancel, done := m.relay, m.cancel, m.done
	m.mu.Unlock()
	if r == nil {
		return nil
	}
	cancel()
	<-done
	return r.Drain(ctx)
}

// Stats returns the counters, or zero before Start.
func (m *Module) Stats() Stats {
	m.mu.Lock()
	r := m.relay
	m.mu.Unlock()
	if r == nil {
		return Stats{}
	}
	return r.Stats()
}

var errNotStarted = errors.New("relay: not started")

// moduleSink forwards to the relay once it exists.
type moduleSink struct{ m *Module }

func (moduleSink) Name() string { return sinkName }

func (s moduleSink) Accept(ctx context.Context, ping domain.Ping) error {
	s.m.mu.Lock()
	r := s.m.relay
	s.m.mu.Unlock()
	if r == nil {
		return errNotStarted
	}
	return r.Accept(ctx, ping)
}
