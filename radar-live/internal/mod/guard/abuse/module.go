// Package abuse is the guard module that fights key guessing: after a number of failed key
// attempts from one client address it bans the address with an exponentially growing duration
// (doubling up to a cap), refuses banned addresses with one uniform response before any auth work,
// persists the ban list to a small file (0600, atomic writes, expired entries pruned at load) and
// offers the `radar-db guard list|unban|ban` admin commands. It also keeps the internal listener's
// routes loopback-only. It only uses core's Server socket and shares nothing with other modules
// except the pure helpers in internal/netx, so deleting its line from modules.go removes it cleanly.
package abuse

import (
	"context"
	"fmt"
	"net/http"
	"net/netip"
	"os"
	"sync"
	"time"

	"radarlive/internal/core"
	"radarlive/internal/netx"
)

// ModuleName is the module's registered name.
const ModuleName = "abuse"

// Setting keys (flags; the environment names are RADAR_ABUSE_ENABLED and so on).
const (
	SettingEnabled       = "abuse-enabled"
	SettingMaxFailures   = "abuse-max-failures"
	SettingWindow        = "abuse-window"
	SettingBanBase       = "abuse-ban-base"
	SettingBanMax        = "abuse-ban-max"
	SettingMaxEntries    = "abuse-max-entries"
	SettingBanFile       = "abuse-ban-file"
	SettingTrusted       = "abuse-trusted-proxies"
	SettingBanLoopback   = "abuse-ban-loopback"
	SettingInternalAllow = "abuse-internal-allow"

	// ratelimitTrusted is the rate limiter's setting; read as plain config (no import) so both
	// guards trust the same proxies unless abuse-trusted-proxies says otherwise.
	ratelimitTrusted = "ratelimit-trusted-proxies"
	dbPathSetting    = "db-path"

	defaultFlushEvery = time.Second
	pruneEvery        = time.Minute
)

// Option customises New.
type Option func(*Module)

// WithClock replaces the time source (tests use a fake clock).
func WithClock(now func() time.Time) Option { return func(m *Module) { m.now = now } }

// WithFlushInterval sets how often the persistence loop looks for changes (default 1 s).
func WithFlushInterval(d time.Duration) Option { return func(m *Module) { m.flushEvery = d } }

// Module wires the guard and the ban file into the host.
type Module struct {
	host       *core.Host
	now        func() time.Time
	flushEvery time.Duration

	enabled bool
	guard   *Guard
	table   *Table
	path    string

	cancel context.CancelFunc
	wg     sync.WaitGroup
	sigT   time.Time // signature of the ban file as we last wrote or read it
	sigN   int64
}

// New returns the abuse module.
func New(opts ...Option) *Module {
	m := &Module{flushEvery: defaultFlushEvery}
	for _, opt := range opts {
		opt(m)
	}
	return m
}

// Name implements core.Module.
func (*Module) Name() string { return ModuleName }

// Table exposes the ban table (tests and diagnostics); nil before Start.
func (m *Module) Table() *Table { return m.table }

// Init declares the settings and registers the handler hook.
func (m *Module) Init(host *core.Host) error {
	m.host = host
	d := DefaultConfig()
	for _, s := range []core.Setting{
		{Key: SettingEnabled, Kind: core.KindBool, Default: "true", Usage: "enable failed-key lockout and bans (kill switch)"},
		{Key: SettingMaxFailures, Kind: core.KindInt, Default: fmt.Sprint(d.MaxFailures), Usage: "failed key attempts within the window that ban an address"},
		{Key: SettingWindow, Kind: core.KindDuration, Default: d.Window.String(), Usage: "window in which failed attempts are counted"},
		{Key: SettingBanBase, Kind: core.KindDuration, Default: d.BaseBan.String(), Usage: "first ban duration; doubles with every further ban"},
		{Key: SettingBanMax, Kind: core.KindDuration, Default: d.MaxBan.String(), Usage: "longest ban duration"},
		{Key: SettingMaxEntries, Kind: core.KindInt, Default: fmt.Sprint(d.MaxEntries), Usage: "max tracked client addresses"},
		{Key: SettingBanFile, Kind: core.KindString, Usage: "ban list file (default: beside the database file; empty without a database = memory only)"},
		{Key: SettingTrusted, Kind: core.KindString, Usage: "reverse proxies whose forwarded client address is believed (default: the ratelimit setting)"},
		{Key: SettingBanLoopback, Kind: core.KindBool, Default: "false", Usage: "also ban loopback clients (off: a same-host tunnel would lock everybody out)"},
		{Key: SettingInternalAllow, Kind: core.KindString, Usage: "extra CIDRs allowed to reach the internal listener besides loopback (default none)"},
	} {
		if err := host.Config.Declare(s); err != nil {
			return fmt.Errorf("abuse: %w", err)
		}
	}
	host.Server.WrapHandler(m.wrapHandler)
	return nil
}

func (m *Module) wrapHandler(class string, next http.Handler) http.Handler {
	if m.guard == nil {
		return next
	}
	return m.guard.Wrap(class, next)
}

func (m *Module) load() (Config, []netip.Prefix, []netip.Prefix, error) {
	c := m.host.Config
	cfg := Config{MaxFailures: c.Int(SettingMaxFailures), Window: c.Duration(SettingWindow), BaseBan: c.Duration(SettingBanBase),
		MaxBan: c.Duration(SettingBanMax), MaxEntries: c.Int(SettingMaxEntries), BanLoopback: c.Bool(SettingBanLoopback)}
	if err := cfg.Validate(); err != nil {
		return cfg, nil, nil, err
	}
	spec := c.String(SettingTrusted)
	if spec == "" {
		spec = c.String(ratelimitTrusted)
	}
	trusted, err := netx.ParseTrusted(spec)
	if err != nil {
		return cfg, nil, nil, fmt.Errorf("abuse: %s: %w", SettingTrusted, err)
	}
	allow, err := netx.ParseTrusted(c.String(SettingInternalAllow))
	if err != nil {
		return cfg, nil, nil, fmt.Errorf("abuse: %s: %w", SettingInternalAllow, err)
	}
	return cfg, trusted, allow, nil
}

// Start validates the configuration, loads the ban file and starts the persistence loop. The host
// does not listen when the configuration is unusable.
func (m *Module) Start(ctx context.Context) error {
	m.enabled = m.host.Config.Bool(SettingEnabled)
	if !m.enabled {
		m.host.Log.Warn("abuse protection is disabled", "setting", SettingEnabled)
		m.host.Audit.Emit(core.AuditEvent{Event: "config_warning", Reason: "abuse_disabled"})
		return nil
	}
	cfg, trusted, allow, err := m.load()
	if err != nil {
		return err
	}
	m.table = NewTable(cfg, m.now)
	m.path = ResolveBanFile(m.host.Config.String(SettingBanFile), m.host.Config.String(dbPathSetting))
	if m.path == "" {
		m.host.Log.Info("ban list is kept in memory only", "setting", SettingBanFile)
	} else if err := m.restore(); err != nil {
		m.host.Log.Warn("ban file ignored, starting with no bans", "error", err.Error())
	}
	m.guard = NewGuard(m.table, trusted, allow)
	m.guard.audit = m.host.Audit
	m.host.Metrics.MustAdd(core.Metric{Name: "radar_bans_active", Help: "Addresses banned right now.", Kind: core.Gauge,
		Value: func() float64 { return float64(len(m.table.Bans())) }})
	loopCtx, cancel := context.WithCancel(context.Background())
	m.cancel = cancel
	m.wg.Add(1)
	go m.loop(loopCtx)
	return nil
}

func (m *Module) clock() time.Time {
	if m.now != nil {
		return m.now()
	}
	return time.Now()
}

// restore loads the ban file; a corrupt one is moved aside so it can be inspected and is not overwritten silently.
func (m *Module) restore() error {
	bans, err := LoadBans(m.path, m.clock())
	if err != nil {
		_ = os.Rename(m.path, m.path+".corrupt")
		m.sigT, m.sigN = fileSignature(m.path)
		return err
	}
	m.table.Replace(bans)
	m.sigT, m.sigN = fileSignature(m.path)
	if len(bans) > 0 {
		m.host.Log.Info("bans restored", "count", len(bans))
	}
	return nil
}

// flush writes the ban list when it changed.
func (m *Module) flush() {
	if m.path == "" || !m.table.TakeDirty() {
		return
	}
	if err := SaveBans(m.path, m.table.Bans()); err != nil {
		m.host.Log.Error("cannot save ban file", "error", err.Error())
		return
	}
	m.sigT, m.sigN = fileSignature(m.path)
}

// resync picks up changes an operator made with the admin command while the server runs.
func (m *Module) resync() {
	if m.path == "" {
		return
	}
	if t, n := fileSignature(m.path); t.Equal(m.sigT) && n == m.sigN {
		return
	}
	bans, err := LoadBans(m.path, m.clock())
	if err != nil {
		m.host.Log.Warn("ban file unreadable, keeping current bans", "error", err.Error())
		m.sigT, m.sigN = fileSignature(m.path)
		return
	}
	before := m.table.Bans()
	m.table.Replace(bans)
	m.table.TakeDirty()
	m.auditAdminChanges(before, bans)
	m.sigT, m.sigN = fileSignature(m.path)
}

// auditAdminChanges reports bans an operator added or lifted with the admin command while the server ran.
func (m *Module) auditAdminChanges(before, after []Ban) {
	set := func(bans []Ban) map[netip.Addr]bool {
		out := make(map[netip.Addr]bool, len(bans))
		for _, b := range bans {
			out[b.IP] = true
		}
		return out
	}
	old, cur := set(before), set(after)
	for ip := range cur {
		if !old[ip] {
			m.host.Audit.Emit(core.AuditEvent{Event: "ban", IP: ip.String(), Reason: "admin"})
		}
	}
	for ip := range old {
		if !cur[ip] {
			m.host.Audit.Emit(core.AuditEvent{Event: "unban", IP: ip.String(), Reason: "admin"})
		}
	}
}

func (m *Module) loop(ctx context.Context) {
	defer m.wg.Done()
	flush := time.NewTicker(m.flushEvery)
	defer flush.Stop()
	lastPrune := time.Now()
	for {
		select {
		case <-ctx.Done():
			return
		case <-flush.C:
			m.flush()
			m.resync()
			if time.Since(lastPrune) >= pruneEvery {
				m.table.Prune()
				lastPrune = time.Now()
			}
		}
	}
}

// Stop ends the persistence loop and writes the final ban list.
func (m *Module) Stop(context.Context) error {
	if m.cancel == nil {
		return nil
	}
	m.cancel()
	m.wg.Wait()
	m.flush()
	return nil
}
