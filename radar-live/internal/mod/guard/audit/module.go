// Package audit is the guard module that makes security events visible: a JSON-lines security log
// (one line per event kind, address and reason per minute, with a count; rotated, mode 0600, never
// holding keys, headers, query strings or bodies), Prometheus-format counters and gauges at
// GET /internal/metrics on the internal (loopback) listener only, and an optional alert command
// (RADAR_ALERT_CMD) run when thresholds trip. Other modules report through core's Audit and Metrics
// sockets and never import this package, so deleting its line from modules.go removes all of it
// and nothing else notices. Put it after the module that declares the internal listener.
package audit

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"radarlive/internal/core"
	"radarlive/internal/netx"
)

// ModuleName is the module's registered name.
const ModuleName = "audit"

// Setting keys (environment: RADAR_AUDIT_LOG, RADAR_ALERT_CMD and so on).
const (
	SettingLog          = "audit-log"
	SettingMaxMB        = "audit-max-mb"
	SettingKeep         = "audit-keep"
	SettingSample       = "audit-sample"
	SettingAlertCmd     = "alert-cmd"
	SettingAlertBans    = "alert-bans"
	SettingAlertWindow  = "alert-window"
	SettingAlertDrops   = "alert-relay-drops"
	SettingAlertCool    = "alert-cooldown"
	settingInternal     = "internal-listen" // declared first by whoever owns the internal listener
	abuseTrusted        = "abuse-trusted-proxies"
	ratelimitTrusted    = "ratelimit-trusted-proxies"
	metricsPath         = "/internal/metrics"
	relayDropsMetric    = "radar_relay_dropped_total"
	pollEvery           = 5 * time.Second
	maxOpenWindows      = 4096
	maxTracked          = 4096
	maxLinesPerSecond   = 200
	defaultMaxMB        = 10
	defaultKeep         = 5
	defaultBanThreshold = 5
	defaultBanWindow    = 10 * time.Minute
	defaultDropsAlert   = 100
)

// knownEvents are the event names counted individually; anything else counts as "other".
var knownEvents = []string{"auth_failure", "ban", "unban", "rate_limited", "conn_refused", "overloaded",
	"validation_reject", "integrity_degraded", "disk_full", "stream_refused", "internal_denied", "config_warning"}

// Option customises New.
type Option func(*Module)

// WithClock replaces the time source (tests).
func WithClock(now func() time.Time) Option { return func(m *Module) { m.now = now } }

// WithRunner replaces the alert command runner (tests).
func WithRunner(r Runner) Option { return func(m *Module) { m.runner = r } }

// WithWriter sends the log to w instead of the configured file or stderr (tests).
func WithWriter(w io.Writer) Option { return func(m *Module) { m.out = w } }

// Module is the audit module.
type Module struct {
	host   *core.Host
	now    func() time.Time
	runner Runner
	out    io.Writer

	file  *rotatingFile
	log   *slog.Logger
	sam   *sampler
	alert *alerter

	events map[string]*atomic.Int64 // read-only after Start
	n429   atomic.Int64

	mu       sync.Mutex
	reqs     map[reqKey]*atomic.Int64
	bans     []time.Time
	banLimit int
	banWin   time.Duration
	dropLim  int64
	drops    []dropSample

	cancel context.CancelFunc
	wg     sync.WaitGroup
}

type reqKey struct{ class, family string }

type dropSample struct {
	at time.Time
	v  float64
}

// New returns the audit module.
func New(opts ...Option) *Module {
	m := &Module{reqs: map[reqKey]*atomic.Int64{}}
	for _, opt := range opts {
		opt(m)
	}
	return m
}

// Name implements core.Module.
func (*Module) Name() string { return ModuleName }

// Init declares the settings and the metrics route on the internal listener.
func (m *Module) Init(host *core.Host) error {
	m.host = host
	for _, s := range []core.Setting{
		{Key: SettingLog, Kind: core.KindString, Usage: "security log file, JSON lines, mode 0600, rotated (empty = stderr/journald)"},
		{Key: SettingMaxMB, Kind: core.KindInt, Default: strconv.Itoa(defaultMaxMB), Usage: "rotate the security log at this size in MiB"},
		{Key: SettingKeep, Kind: core.KindInt, Default: strconv.Itoa(defaultKeep), Usage: "rotated security log files to keep"},
		{Key: SettingSample, Kind: core.KindDuration, Default: time.Minute.String(), Usage: "at most one security log line per event, address and reason in this window (with a count)"},
		{Key: SettingAlertCmd, Kind: core.KindString, Secret: true, Usage: "command run (no shell, 5 s limit) on alerts; {kind} {text} {count} are substituted, RADAR_ALERT_* are set"},
		{Key: SettingAlertBans, Kind: core.KindInt, Default: strconv.Itoa(defaultBanThreshold), Usage: "bans within the alert window that raise ban_surge (0 = off)"},
		{Key: SettingAlertWindow, Kind: core.KindDuration, Default: defaultBanWindow.String(), Usage: "window of the ban_surge and relay_drops thresholds"},
		{Key: SettingAlertDrops, Kind: core.KindInt, Default: strconv.Itoa(defaultDropsAlert), Usage: "relay buffer drops within the alert window that raise relay_drops (0 = off)"},
		{Key: SettingAlertCool, Kind: core.KindDuration, Default: defaultCooldown.String(), Usage: "minimum time between two alerts of the same kind"},
	} {
		if err := host.Config.Declare(s); err != nil {
			return fmt.Errorf("audit: %w", err)
		}
	}
	// Disabled unless the binary's owner of the internal listener (radar-db) sets a default or the operator sets RADAR_INTERNAL_LISTEN.
	if err := host.EnsureListener(core.ClassInternal, settingInternal, "", "internal listen address (loopback; empty disables it)"); err != nil {
		return fmt.Errorf("audit: %w", err)
	}
	if err := host.Routes.HandleFuncOn(core.ClassInternal, http.MethodGet, metricsPath, m.serveMetrics); err != nil {
		return fmt.Errorf("audit: %w", err)
	}
	return nil
}

func (m *Module) clock() time.Time {
	if m.now != nil {
		return m.now()
	}
	return time.Now()
}

// Start opens the log, registers the observers and starts the flush/poll loop. A bad alert command
// or log path stops the host from starting.
func (m *Module) Start(ctx context.Context) error {
	cfg := m.host.Config
	argv, err := splitArgs(cfg.String(SettingAlertCmd))
	if err != nil {
		return fmt.Errorf("audit: %s is not a valid command line: %w", SettingAlertCmd, err)
	}
	w := m.out
	if w == nil {
		w, err = m.openLog()
		if err != nil {
			return err
		}
	}
	m.log = newLogger(w)
	m.sam = newSampler(m.clock, cfg.Duration(SettingSample), maxOpenWindows, maxLinesPerSecond, func(r record) { writeRecord(m.log, r) })
	if len(argv) > 0 {
		m.alert = newAlerter(argv, m.runner, m.clock, cfg.Duration(SettingAlertCool))
		m.host.Log.Info("alert command configured", "command", describe(argv))
	}
	m.banLimit, m.banWin, m.dropLim = cfg.Int(SettingAlertBans), cfg.Duration(SettingAlertWindow), int64(cfg.Int(SettingAlertDrops))
	m.registerMetrics()

	m.host.Audit.SetClientResolver(m.resolver())
	m.host.Audit.ObserveRequests(m.request)
	m.host.Audit.Observe(m.handle)

	loopCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	m.cancel = cancel
	m.wg.Add(1)
	go m.loop(loopCtx)
	m.sam.add(sanitize(core.AuditEvent{Event: "audit_started", Reason: "log_open"}))
	return nil
}

func (m *Module) openLog() (io.Writer, error) {
	path := m.host.Config.String(SettingLog)
	if path == "" {
		return os.Stderr, nil
	}
	mb := m.host.Config.Int(SettingMaxMB)
	if mb < 1 {
		return nil, fmt.Errorf("audit: %s must be at least 1", SettingMaxMB)
	}
	f, err := openRotating(path, int64(mb)<<20, m.host.Config.Int(SettingKeep))
	if err != nil {
		return nil, fmt.Errorf("audit: %w", err)
	}
	m.file = f
	return f, nil
}

// resolver finds the client address like the guards do: forwarded headers only from trusted proxies.
func (m *Module) resolver() func(*http.Request) string {
	spec := m.host.Config.String(abuseTrusted)
	if spec == "" {
		spec = m.host.Config.String(ratelimitTrusted)
	}
	trusted, _ := netx.ParseTrusted(spec) // the guards refuse to start on a bad list
	return func(r *http.Request) string {
		if ip := netx.ClientIP(r, trusted); ip.IsValid() {
			return ip.String()
		}
		return ""
	}
}

func (m *Module) loop(ctx context.Context) {
	defer m.wg.Done()
	t := time.NewTicker(pollEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			m.sam.flush()
			if v, ok := m.host.Metrics.Value(relayDropsMetric); ok {
				m.checkDrops(m.clock(), v)
			}
		}
	}
}

// Stop writes the pending summaries and closes the log file.
func (m *Module) Stop(context.Context) error {
	if m.cancel == nil {
		return nil
	}
	m.cancel()
	m.wg.Wait()
	m.sam.flushAll()
	if m.file != nil {
		return m.file.Close()
	}
	return nil
}

// handle counts an event, passes it through the sampler and checks the alert thresholds.
func (m *Module) handle(ev core.AuditEvent) {
	r := sanitize(ev)
	name := r.event
	c, ok := m.events[name]
	if !ok {
		c = m.events["other"]
	}
	c.Add(int64(r.count))
	m.sam.add(r)
	switch name {
	case "ban":
		m.noteBans(m.clock(), r.count)
	case "integrity_degraded":
		m.fire(AlertDBDegraded, "database integrity check failed; writes are refused", 1)
	case "disk_full":
		m.fire(AlertDiskLow, "database writes paused: disk or size cap reached", 1)
	}
}

func (m *Module) fire(kind, text string, count int) {
	if m.alert != nil {
		m.alert.Fire(kind, text, count)
	}
}

// noteBans records n bans at now and fires ban_surge when the window holds the threshold.
func (m *Module) noteBans(now time.Time, n int) {
	if m.banLimit <= 0 {
		return
	}
	m.mu.Lock()
	for i := 0; i < n && len(m.bans) < maxTracked; i++ {
		m.bans = append(m.bans, now)
	}
	cut := 0
	for cut < len(m.bans) && now.Sub(m.bans[cut]) > m.banWin {
		cut++
	}
	m.bans = m.bans[cut:]
	count := len(m.bans)
	m.mu.Unlock()
	if count >= m.banLimit {
		m.fire(AlertBanSurge, fmt.Sprintf("%d address bans within %s", count, m.banWin), count)
	}
}

// checkDrops compares the relay's drop counter with the oldest sample in the window.
func (m *Module) checkDrops(now time.Time, v float64) {
	if m.dropLim <= 0 {
		return
	}
	m.mu.Lock()
	m.drops = append(m.drops, dropSample{at: now, v: v})
	cut := 0
	for cut < len(m.drops)-1 && now.Sub(m.drops[cut].at) > m.banWin {
		cut++
	}
	m.drops = m.drops[cut:]
	delta := int64(v - m.drops[0].v)
	m.mu.Unlock()
	if delta >= m.dropLim {
		m.fire(AlertRelayDrops, fmt.Sprintf("relay buffer dropped %d pings within %s", delta, m.banWin), int(delta))
	}
}

// request counts a finished request by listener class and status family.
func (m *Module) request(class string, status int) {
	if status == http.StatusTooManyRequests {
		m.n429.Add(1)
	}
	family := strconv.Itoa(status/100) + "xx"
	k := reqKey{class: clean(class, maxClassLen), family: family}
	m.mu.Lock()
	c, ok := m.reqs[k]
	if !ok {
		c = new(atomic.Int64)
		m.reqs[k] = c
		m.host.Metrics.MustAdd(core.Metric{Name: "radar_http_requests_total", Help: "HTTP requests by listener class and status family.",
			Kind: core.Counter, Labels: map[string]string{"class": k.class, "code": k.family}, Value: func() float64 { return float64(c.Load()) }})
	}
	m.mu.Unlock()
	c.Add(1)
}

func (m *Module) registerMetrics() {
	mt := m.host.Metrics
	m.events = map[string]*atomic.Int64{}
	for _, e := range append(append([]string(nil), knownEvents...), "other") {
		c := new(atomic.Int64)
		m.events[e] = c
		mt.MustAdd(core.Metric{Name: "radar_security_events_total", Help: "Security events by kind (every occurrence, not just logged lines).",
			Kind: core.Counter, Labels: map[string]string{"event": e}, Value: func() float64 { return float64(c.Load()) }})
	}
	alias := func(name, help, event string) {
		c := m.events[event]
		mt.MustAdd(core.Metric{Name: name, Help: help, Kind: core.Counter, Value: func() float64 { return float64(c.Load()) }})
	}
	alias("radar_auth_failures_total", "Rejected API key attempts.", "auth_failure")
	alias("radar_bans_total", "Addresses banned for repeated authentication failures.", "ban")
	alias("radar_rate_limited_total", "Requests refused by the rate limiter.", "rate_limited")
	mt.MustAdd(core.Metric{Name: "radar_http_429_total", Help: "Responses with status 429 (rate limited or banned).", Kind: core.Counter,
		Value: func() float64 { return float64(m.n429.Load()) }})
	mt.MustAdd(core.Metric{Name: "radar_audit_log_lines_total", Help: "Security log lines written.", Kind: core.Counter,
		Value: func() float64 { l, _ := m.sam.stats(); return float64(l) }})
	mt.MustAdd(core.Metric{Name: "radar_audit_log_lines_dropped_total", Help: "Security log lines refused by the per-second ceiling.", Kind: core.Counter,
		Value: func() float64 { _, d := m.sam.stats(); return float64(d) }})
	for _, kind := range []string{AlertBanSurge, AlertDBDegraded, AlertDiskLow, AlertRelayDrops} {
		mt.MustAdd(core.Metric{Name: "radar_alerts_fired_total", Help: "Alert commands started, by kind.", Kind: core.Counter,
			Labels: map[string]string{"kind": kind}, Value: func() float64 {
				if m.alert == nil {
					return 0
				}
				return float64(m.alert.counts()[kind])
			}})
	}
}

// serveMetrics renders every registered metric in the Prometheus text format (version 0.0.4).
func (m *Module) serveMetrics(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	_, _ = io.WriteString(w, renderMetrics(m.host.Metrics.All()))
}

// renderMetrics formats series grouped by name (sorted), each group with HELP and TYPE.
func renderMetrics(all []core.Metric) string {
	groups := map[string][]core.Metric{}
	for _, s := range all {
		groups[s.Name] = append(groups[s.Name], s)
	}
	names := make([]string, 0, len(groups))
	for n := range groups {
		names = append(names, n)
	}
	sort.Strings(names)
	var b strings.Builder
	for _, n := range names {
		g := groups[n]
		fmt.Fprintf(&b, "# HELP %s %s\n# TYPE %s %s\n", n, escapeHelp(g[0].Help), n, g[0].Kind)
		for _, s := range g {
			b.WriteString(n)
			b.WriteString(formatLabels(s.Labels))
			b.WriteByte(' ')
			b.WriteString(strconv.FormatFloat(s.Value(), 'g', -1, 64))
			b.WriteByte('\n')
		}
	}
	return b.String()
}

func formatLabels(l map[string]string) string {
	if len(l) == 0 {
		return ""
	}
	keys := make([]string, 0, len(l))
	for k := range l {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, len(keys))
	for i, k := range keys {
		parts[i] = k + `="` + escapeLabel(l[k]) + `"`
	}
	return "{" + strings.Join(parts, ",") + "}"
}

func escapeLabel(s string) string {
	return strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`).Replace(s)
}

func escapeHelp(s string) string { return strings.NewReplacer(`\`, `\\`, "\n", `\n`).Replace(s) }
