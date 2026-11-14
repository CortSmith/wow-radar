// Package streamapi is the ingest-side module that accepts live combat-log lines over HTTP:
// POST /v1/stream (a long-lived chunked body), POST /v1/lines (one batch) and GET /v1/sources.
// It owns connections only: source ids, caps, idle timeouts, counters and summaries. Each line goes
// to host.Parsers as a domain.RawLine; parsing and ping creation belong to the parsing modules,
// whose pings reach host.Sinks. The module never imports them, so any of them can be removed.
package streamapi

import (
	"context"
	"fmt"
	"net/http"
	"regexp"
	"sync"
	"sync/atomic"
	"time"

	"radarlive/internal/core"
	"radarlive/internal/domain"
)

// Module name, configuration keys and limits.
const (
	ModuleName         = "streamapi"
	SettingStreamIdle  = "stream-idle"
	SettingMaxSources  = "max-sources"
	SettingStreamBytes = "stream-max-bytes"
	SettingLinesCount  = "lines-max-lines"
	SettingLinesBytes  = "lines-max-bytes"

	// MaxLineBytes is the longest accepted line; longer lines are counted as rejected.
	MaxLineBytes = 1 << 20

	defaultStreamIdle  = 120 * time.Second
	defaultMaxSources  = 16
	defaultStreamBytes = 4 << 30
	defaultLinesCount  = 20_000
	defaultLinesBytes  = 8 << 20
	defaultSource      = "default"
	defaultKeyTick     = 2 * time.Second
)

var sourcePattern = regexp.MustCompile(`^[\w.:-]{1,48}$`)

// Module serves the ingest endpoints.
type Module struct {
	host *core.Host
	now  func() time.Time
	// sleep waits for the rate limiter; tests replace it.
	sleep func(ctx, run context.Context, d time.Duration) error
	reg   *registry
	// keyTick is how often an open stream re-checks its keyring key; tests shorten it.
	keyTick time.Duration
	lim     limits
	// usage counts per key id into keyusage-file; nil when the setting is empty.
	usage      *usageTracker
	usageEvery time.Duration
	usageStop  context.CancelFunc
	usageDone  chan struct{}

	rejected atomic.Int64 // lines refused by the parsers (metric)

	mu      sync.Mutex
	runCtx  context.Context
	regOnce sync.Once
}

// New returns the module.
func New() *Module {
	return &Module{now: time.Now, sleep: sleepCtx, runCtx: context.Background(), keyTick: defaultKeyTick, usageEvery: defaultUsageFlush}
}

// Name implements core.Module.
func (*Module) Name() string { return ModuleName }

// Init declares the settings, the observer parser and the routes.
func (m *Module) Init(host *core.Host) error {
	m.host = host
	for _, s := range []core.Setting{
		{Key: SettingStreamIdle, Kind: core.KindDuration, Default: defaultStreamIdle.String(), Usage: "close a stream that sends nothing for this long"},
		{Key: SettingMaxSources, Kind: core.KindInt, Default: fmt.Sprint(defaultMaxSources), Usage: "max concurrent sources"},
		{Key: SettingStreamBytes, Kind: core.KindInt, Default: fmt.Sprint(defaultStreamBytes), Usage: "max bytes accepted on one stream before it is closed"},
		{Key: SettingLinesCount, Kind: core.KindInt, Default: fmt.Sprint(defaultLinesCount), Usage: "max lines in one /v1/lines batch"},
		{Key: SettingLinesBytes, Kind: core.KindInt, Default: fmt.Sprint(defaultLinesBytes), Usage: "max body bytes of one /v1/lines batch"},
		{Key: SettingLineRate, Kind: core.KindInt, Default: fmt.Sprint(defaultLineRate), Usage: "lines per second accepted from one source (0 = no limit)"},
		{Key: SettingLineBurst, Kind: core.KindInt, Default: fmt.Sprint(defaultLineBurst), Usage: "burst of the per-source line limit"},
		{Key: SettingByteRate, Kind: core.KindInt, Default: fmt.Sprint(defaultByteRate), Usage: "bytes per second accepted from one source (0 = no limit)"},
		{Key: SettingByteBurst, Kind: core.KindInt, Default: fmt.Sprint(defaultByteBurst), Usage: "burst of the per-source byte limit"},
		{Key: SettingKeyLineRate, Kind: core.KindInt, Default: fmt.Sprint(defaultKeyLineRate), Usage: "lines per second accepted over all sources of one key (0 = no limit)"},
		{Key: SettingKeyLineBurst, Kind: core.KindInt, Default: fmt.Sprint(defaultKeyLineBurst), Usage: "burst of the per-key line limit"},
		{Key: SettingKeyByteRate, Kind: core.KindInt, Default: fmt.Sprint(defaultKeyByteRate), Usage: "bytes per second accepted over all sources of one key (0 = no limit)"},
		{Key: SettingKeyByteBurst, Kind: core.KindInt, Default: fmt.Sprint(defaultKeyByteBurst), Usage: "burst of the per-key byte limit"},
		{Key: SettingPatience, Kind: core.KindDuration, Default: defaultPatience.String(), Usage: "how long a source may be slowed without a break before its stream ends with reason rate"},
		{Key: SettingMaxPerKey, Kind: core.KindInt, Default: fmt.Sprint(defaultMaxPerKey), Usage: "max concurrent streams per ingest key (0 = no limit)"},
		{Key: SettingMaxStreams, Kind: core.KindInt, Default: fmt.Sprint(defaultMaxStreams), Usage: "max concurrent streams over all keys (0 = no limit)"},
		{Key: SettingLineTimeout, Kind: core.KindDuration, Default: defaultLineTimeout.String(), Usage: "max time one line may take to arrive (slow-sender cut, 0 = off)"},
		{Key: SettingKeyUsageFile, Kind: core.KindString, Usage: "write per-key last-seen and daily line/byte counts (ids only) to this JSON file for radar-keys (empty = off)"},
	} {
		if err := host.Config.Declare(s); err != nil {
			return fmt.Errorf("streamapi settings: %w", err)
		}
	}
	host.Parsers.Add(observer{m})
	host.Metrics.MustAdd(core.Metric{Name: "radar_stream_sessions", Help: "Open ingest connections (streams and batches).", Kind: core.Gauge,
		Value: func() float64 { return float64(len(m.registry().list(m.now()))) }})
	host.Metrics.MustAdd(core.Metric{Name: "radar_stream_rejected_lines_total", Help: "Ingest lines the parsers refused.", Kind: core.Counter,
		Value: func() float64 { return float64(m.rejected.Load()) }})
	return m.routes()
}

func (m *Module) routes() error {
	ingest := func(h http.HandlerFunc) http.HandlerFunc {
		guarded := m.host.Auth.Require(core.RoleIngest, h)
		return func(w http.ResponseWriter, r *http.Request) { guarded.ServeHTTP(&closeOnError{ResponseWriter: w}, r) }
	}
	for _, r := range []struct {
		method, path string
		h            http.HandlerFunc
	}{
		{http.MethodPost, "/v1/stream", ingest(m.handleStream)},
		{http.MethodPost, "/v1/lines", ingest(m.handleLines)},
		{http.MethodGet, "/v1/sources", m.handleSources},
	} {
		if err := m.host.Routes.HandleFunc(r.method, r.path, r.h); err != nil {
			return fmt.Errorf("streamapi routes: %w", err)
		}
	}
	return nil
}

// Start checks the limits and remembers the run context: when it ends, open streams are told to finish.
func (m *Module) Start(ctx context.Context) error {
	if _, err := m.limits(); err != nil {
		return err
	}
	m.mu.Lock()
	m.runCtx = ctx
	m.mu.Unlock()
	if path := m.host.Config.String(SettingKeyUsageFile); path != "" {
		m.usage = newUsageTracker(path, m.now)
		ctx, stop := context.WithCancel(context.Background())
		m.usageStop, m.usageDone = stop, make(chan struct{})
		go func() {
			defer close(m.usageDone)
			m.usage.run(ctx, m.usageEvery, func(err error) { m.host.Log.Warn("streamapi: keyusage write failed", "err", err) })
		}()
	}
	return nil
}

// Stop ends the usage flusher after one last write.
func (m *Module) Stop(context.Context) error {
	if m.usageStop != nil {
		m.usageStop()
		<-m.usageDone
	}
	return nil
}

func (m *Module) running() context.Context {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.runCtx
}

// registry builds the session registry on first use, once the configuration is loaded.
func (m *Module) registry() *registry {
	m.regOnce.Do(func() {
		limit := m.host.Config.Int(SettingMaxSources)
		if limit < 1 {
			limit = defaultMaxSources
		}
		m.reg = newRegistry(limit)
		m.lim, _ = m.limits() // validated in Start; invalid values are treated as "off" by the zero checks
		m.reg.lim = m.lim
	})
	return m.reg
}

// observer is the parser that sees the domain.Events logparse feeds back, to count them per source.
type observer struct{ m *Module }

func (observer) Name() string { return ModuleName }

func (o observer) Parse(_ context.Context, event any) error {
	ev, ok := event.(domain.Event)
	if !ok {
		return nil
	}
	if s := o.m.registry().get(ev.Source); s != nil {
		s.observe(ev)
	}
	return nil
}

// params reads and validates ?source= and ?rs=; on failure it has already answered.
func (m *Module) params(w http.ResponseWriter, r *http.Request) (source string, rs domain.Ruleset, ok bool) {
	q := r.URL.Query()
	source = q.Get("source")
	defer func() {
		if !ok {
			refuse(w)
		}
	}()
	if source == "" {
		source = defaultSource
	}
	if !sourcePattern.MatchString(source) {
		core.WriteError(w, http.StatusBadRequest, "bad_request", "source must match "+sourcePattern.String())
		return "", "", false
	}
	if text := q.Get("rs"); text != "" {
		parsed, err := domain.ParseRuleset(text)
		if err != nil {
			core.WriteError(w, http.StatusBadRequest, "bad_request", "unknown ruleset "+text)
			return "", "", false
		}
		rs = parsed
	}
	return source, rs, true
}

// refuse marks an early refusal: the connection closes after the reply, so the server does not wait
// to drain the (possibly endless) request body before it answers.
func refuse(w http.ResponseWriter) { w.Header().Set("Connection", "close") }

// open registers a session or answers 409 / 429.
func (m *Module) open(w http.ResponseWriter, r *http.Request, source string, rs domain.Ruleset) *session {
	info, _ := core.KeyInfoFromContext(r.Context())
	s, err := m.registry().open(source, rs, keyOf(r), m.now(), info)
	if err != nil {
		refuse(w)
	}
	if err != nil {
		m.host.Audit.EmitRequest(r, core.AuditEvent{Event: "stream_refused", Class: "ingest", Reason: refusalReason(err)})
	}
	switch err {
	case nil:
		return s
	case errSourceBusy:
		core.WriteError(w, http.StatusConflict, "source_busy", "a connection for this source is already open")
	case errTooStreams, errKeyStreams:
		w.Header().Set("Retry-After", "5")
		core.WriteError(w, http.StatusTooManyRequests, "too_many_streams", err.Error())
	default:
		core.WriteError(w, http.StatusTooManyRequests, "too_many_sources", err.Error())
	}
	return nil
}

// refusalReason names why a session was refused, for the audit event.
func refusalReason(err error) string {
	switch err {
	case errSourceBusy:
		return "source_busy"
	case errTooStreams:
		return "too_many_streams"
	case errKeyStreams:
		return "too_many_streams_key"
	}
	return "too_many_sources"
}

// feed hands one line to the parsers; blank lines are ignored, failures count as rejected.
func (m *Module) feed(ctx context.Context, s *session, text string) {
	if text == "" {
		return
	}
	s.lines.Add(1)
	line := domain.RawLine{Source: s.source, Text: text, Ruleset: s.rs}
	if err := m.host.Parsers.Feed(ctx, line); err != nil {
		s.rejected.Add(1)
		m.rejected.Add(1)
	}
}

// closeOnError adds "Connection: close" to every error reply, so a refused request with an endless
// body (an unauthenticated stream, say) is answered at once instead of after the server drains it.
type closeOnError struct{ http.ResponseWriter }

func (c *closeOnError) WriteHeader(status int) {
	if status >= http.StatusBadRequest {
		refuse(c.ResponseWriter)
	}
	c.ResponseWriter.WriteHeader(status)
}

// Unwrap lets http.ResponseController reach the real connection.
func (c *closeOnError) Unwrap() http.ResponseWriter { return c.ResponseWriter }
