package sqlstore

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"radarlive/internal/core"
	"radarlive/internal/domain"
)

// Module and store name, and the configuration keys the module declares.
const (
	ModuleName  = "sqlstore"
	StoreName   = "sqlite"
	KeyDBPath   = "db-path"
	KeyCacheMB  = "cache-mb"
	KeyMaxFixes = "max-fixes"
	// KeyMaxDBMB, KeyMinFreeMB and KeyIntegrityEvery are RADAR_DB_MAX_MB, RADAR_DB_MIN_FREE_MB, RADAR_DB_INTEGRITY_EVERY.
	KeyMaxDBMB            = "db-max-mb"
	KeyMinFreeMB          = "db-min-free-mb"
	KeyIntegrityEvery     = "db-integrity-every"
	defaultIntegrityEvery = 24 * time.Hour
	defaultDBRel          = "radar-live.db"
)

// Module plugs the SQLite store into the host's Stores socket.
type Module struct {
	host *core.Host
	lazy *lazyStore
}

// New returns the sqlstore module.
func New() *Module { return &Module{} }

// Name implements core.Module.
func (*Module) Name() string { return ModuleName }

// Init declares the settings and registers the store. Settings are not loaded yet, so the database
// is opened on Start (or on first use), reading its configuration then.
func (m *Module) Init(host *core.Host) error {
	m.host = host
	for _, s := range []core.Setting{
		{Key: KeyDBPath, Kind: core.KindString, Default: defaultDBRel, Usage: "path of the SQLite database file"},
		{Key: KeyCacheMB, Kind: core.KindInt, Default: fmt.Sprint(DefaultCacheMB), Usage: "SQLite page cache per connection, MiB"},
		{Key: KeyMaxFixes, Kind: core.KindInt, Default: fmt.Sprint(DefaultMaxFixes), Usage: "global cap on stored trail fixes"},
		{Key: KeyMaxDBMB, Kind: core.KindInt, Default: fmt.Sprint(DefaultMaxDBMB), Usage: "maximum database size in MiB; above it pings get 507 (oldest trail fixes are pruned first)"},
		{Key: KeyMinFreeMB, Kind: core.KindInt, Default: fmt.Sprint(DefaultMinFreeMB), Usage: "pause pings (507) when the data directory has less free disk space, MiB; negative disables"},
		{Key: KeyIntegrityEvery, Kind: core.KindDuration, Default: defaultIntegrityEvery.String(), Usage: "interval of the full database integrity_check; 0 disables the timer"},
	} {
		if err := host.Config.Declare(s); err != nil {
			return fmt.Errorf("sqlstore: %w", err)
		}
	}
	m.lazy = &lazyStore{open: m.openConfigured}
	m.registerMetrics()
	return host.Stores.Add(StoreName, m.lazy)
}

// Start opens the database now so a bad path or file fails startup instead of the first request.
func (m *Module) Start(context.Context) error {
	_, err := m.lazy.get()
	return err
}

// audit forwards store state changes to the host's audit socket.
func (m *Module) audit(event, reason string) {
	m.host.Audit.Emit(core.AuditEvent{Event: event, Class: "db", Reason: reason})
}

// registerMetrics exposes database size, integrity state, paused writes and the age of the last write.
func (m *Module) registerMetrics() {
	gauge := func(name, help string, fn func(s *Store) float64) {
		m.host.Metrics.MustAdd(core.Metric{Name: name, Help: help, Kind: core.Gauge, Value: func() float64 {
			if s := m.lazy.peek(); s != nil {
				return fn(s)
			}
			return 0
		}})
	}
	gauge("radar_db_size_bytes", "Database file plus write-ahead log size in bytes.", func(s *Store) float64 {
		return float64(fileSize(s.path) + fileSize(s.path+"-wal"))
	})
	gauge("radar_db_integrity_degraded", "1 when an integrity check failed and writes are refused.", func(s *Store) float64 {
		return b2f(s.Health().Integrity == domain.IntegrityDegraded)
	})
	gauge("radar_db_writes_paused", "1 when pings get 507 for lack of disk space or the size cap.", func(s *Store) float64 {
		return b2f(s.Health().Full)
	})
	gauge("radar_db_last_write_age_seconds", "Seconds since the last committed ping (-1 = none since start).", func(s *Store) float64 {
		last := s.lastWrite.Load()
		if last == 0 {
			return -1
		}
		return float64(s.nowMs()-last) / 1000
	})
}

func b2f(b bool) float64 {
	if b {
		return 1
	}
	return 0
}

func (m *Module) openConfigured() (*Store, error) {
	cfg := m.host.Config
	path := cfg.String(KeyDBPath)
	release, err := AcquireLock(path)
	if err != nil {
		return nil, fmt.Errorf("sqlstore: another radar-db is using %s (or a restore is running): %w", path, err)
	}
	s, err := Open(path, Options{CacheMB: cfg.Int(KeyCacheMB), MaxFixes: cfg.Int(KeyMaxFixes),
		MaxDBMB: cfg.Int(KeyMaxDBMB), MinFreeMB: cfg.Int(KeyMinFreeMB), IntegrityEvery: cfg.Duration(KeyIntegrityEvery),
		Logger: m.host.Log, OnEvent: m.audit})
	if err != nil {
		_ = release()
		return nil, err
	}
	m.lazy.release = release
	return s, nil
}

// lazyStore opens its Store on first use; the first outcome (store or error) is kept.
type lazyStore struct {
	open    func() (*Store, error)
	once    sync.Once
	store   *Store
	err     error
	release func() error // the server lock, held while the store is open
	opened  atomic.Pointer[Store]
}

// peek returns the store if it has been opened, never opening it.
func (l *lazyStore) peek() *Store {
	return l.opened.Load()
}

func (l *lazyStore) get() (*Store, error) {
	l.once.Do(func() {
		l.store, l.err = l.open()
		if l.err == nil {
			l.opened.Store(l.store)
		}
	})
	return l.store, l.err
}

// Close closes the database if it was ever opened.
func (l *lazyStore) Close() error {
	l.once.Do(func() { l.err = fmt.Errorf("sqlstore: closed before use") })
	if l.store == nil {
		return nil
	}
	err := l.store.Close()
	if l.release != nil {
		err = errors.Join(err, l.release())
	}
	return err
}

// Health forwards the wrapped store health; before the store opens it reports ok.
func (l *lazyStore) Health() domain.Health {
	if s, err := l.get(); err == nil {
		return s.Health()
	}
	return domain.Health{Integrity: domain.IntegrityOK}
}

func (l *lazyStore) ApplyPing(ctx context.Context, p domain.Ping) (domain.ApplyResult, error) {
	s, err := l.get()
	if err != nil {
		return domain.ApplyResult{}, err
	}
	return s.ApplyPing(ctx, p)
}

func (l *lazyStore) Sweep(ctx context.Context, sec domain.Sector) (domain.SweepResult, error) {
	s, err := l.get()
	if err != nil {
		return domain.SweepResult{}, err
	}
	return s.Sweep(ctx, sec)
}

func (l *lazyStore) Search(ctx context.Context, q domain.SearchQuery) (domain.SearchResult, error) {
	s, err := l.get()
	if err != nil {
		return domain.SearchResult{}, err
	}
	return s.Search(ctx, q)
}

func (l *lazyStore) Trail(ctx context.Context, rs domain.Ruleset, guid string, mapID *int) ([]domain.Fix, error) {
	s, err := l.get()
	if err != nil {
		return nil, err
	}
	return s.Trail(ctx, rs, guid, mapID)
}

func (l *lazyStore) Bounty(ctx context.Context, rs domain.Ruleset, limit int) ([]domain.BountyRow, error) {
	s, err := l.get()
	if err != nil {
		return nil, err
	}
	return s.Bounty(ctx, rs, limit)
}

func (l *lazyStore) BountyPlayer(ctx context.Context, rs domain.Ruleset, guid string) (domain.BountyRow, error) {
	s, err := l.get()
	if err != nil {
		return domain.BountyRow{}, err
	}
	return s.BountyPlayer(ctx, rs, guid)
}

func (l *lazyStore) Charts(ctx context.Context, rs domain.Ruleset) ([]domain.Chart, error) {
	s, err := l.get()
	if err != nil {
		return nil, err
	}
	return s.Charts(ctx, rs)
}

func (l *lazyStore) Status(ctx context.Context) ([]domain.RulesetStatus, error) {
	s, err := l.get()
	if err != nil {
		return nil, err
	}
	return s.Status(ctx)
}
