// Package sqlstore is the durable core.Store: one SQLite file (pure Go driver, no cgo) in WAL mode
// with a single writer connection and a small pool of readers. Merge, trail and ranking rules are
// the pure functions of package domain, the same ones memstore uses, so both stores behave alike.
package sqlstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"sync"
	"sync/atomic"
	"time"

	_ "modernc.org/sqlite" // registers the "sqlite" driver

	"radarlive/internal/domain"
)

// Defaults and bounds, all sized for a small laptop.
const (
	DefaultCacheMB    = 16
	DefaultMaxFixes   = 250_000
	readerConnections = 4
	busyTimeoutMs     = 5000
	batchesKeptMax    = 20_000
	deathsKeptMax     = 50_000
	pruneEveryInserts = 1000
	// armory retention: remembered batch ids, run summaries and days of snapshot history.
	armoryBatchesKeptMax   = 20_000
	armoryRunsKeptMax      = 400
	armorySnapshotKeepDays = 30
	// fixPruneTargetPercent is the fill level the fix table is pruned down to once it exceeds the cap.
	fixPruneTargetPercent = 90
)

// Options tune a Store; zero values select the defaults.
type Options struct {
	// Now supplies the clock; nil means time.Now.
	Now func() time.Time
	// CacheMB is the SQLite page cache per connection in MiB.
	CacheMB int
	// MaxFixes is the global trail fix cap across all contacts.
	MaxFixes int
	// MaxDBMB caps the database size in MiB (0 = DefaultMaxDBMB); MaxDBBytes overrides it for tests.
	MaxDBMB    int
	MaxDBBytes int64
	// MinFreeMB pauses writes when the data directory has less free space (0 = DefaultMinFreeMB, negative = off).
	MinFreeMB int
	// DiskFree reports free bytes of the filesystem holding a directory; nil means the real probe.
	DiskFree func(dir string) (uint64, error)
	// IntegrityEvery runs the full integrity_check on a timer; 0 disables the timer.
	IntegrityEvery time.Duration
	// Logger receives warnings and errors; nil means slog.Default.
	Logger *slog.Logger
	// OnEvent, when set, is told about security-relevant state changes: ("disk_full", "disk_low"|"size_cap") and
	// ("integrity_degraded", "quick_check"|"integrity_check"), once per transition. It must be quick.
	OnEvent func(event, reason string)

	checkFn func(ctx context.Context, mode string) (string, error) // test hook replacing the SQLite check
}

// Store implements core.Store on SQLite. It is safe for concurrent use.
type Store struct {
	now      func() time.Time
	writer   *sql.DB
	readers  *sql.DB
	maxFixes int64
	fixCount atomic.Int64 // fixes currently stored, maintained by the writer
	inserts  atomic.Int64 // batch/death inserts since the last id prune

	path         string
	log          *slog.Logger
	diskFree     func(string) (uint64, error)
	minFreeBytes int64
	maxDBBytes   atomic.Int64
	runCheck     func(ctx context.Context, mode string) (string, error)
	degraded     atomic.Bool
	full         atomic.Bool
	lastCheck    atomic.Int64
	lastWrite    atomic.Int64 // epoch ms of the last committed ping (0 = none since start)
	onEvent      func(event, reason string)
	gateMu       sync.Mutex // guards the fields below
	checkedOnce  bool
	lastSpace    time.Time
	fullReason   string
	stopMonitor  context.CancelFunc
	monitorDone  chan struct{}
}

// Open opens (creating and migrating when needed) the database file at path.
func Open(path string, opts Options) (*Store, error) {
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.CacheMB <= 0 {
		opts.CacheMB = DefaultCacheMB
	}
	if opts.MaxFixes <= 0 {
		opts.MaxFixes = DefaultMaxFixes
	}
	if err := preparePaths(path); err != nil {
		return nil, err
	}
	if err := preflight(path); err != nil {
		return nil, err
	}
	writer, err := openPool(path, opts.CacheMB, 1, false)
	if err != nil {
		return nil, err
	}
	s := newStore(path, writer, opts)
	if err := s.init(path, opts.CacheMB); err != nil {
		_ = writer.Close()
		return nil, err
	}
	s.lastCheck.Store(s.nowMs()) // preflight ran quick_check on this file
	s.warnPermissions()
	if opts.IntegrityEvery > 0 {
		ctx, cancel := context.WithCancel(context.Background())
		s.stopMonitor, s.monitorDone = cancel, make(chan struct{})
		go s.monitor(ctx, opts.IntegrityEvery)
	}
	return s, nil
}

// newStore builds the Store around an open writer, applying option defaults.
func newStore(path string, writer *sql.DB, opts Options) *Store {
	s := &Store{now: opts.Now, writer: writer, maxFixes: int64(opts.MaxFixes), path: path,
		log: opts.Logger, diskFree: opts.DiskFree, onEvent: opts.OnEvent}
	if s.log == nil {
		s.log = slog.Default()
	}
	if s.diskFree == nil {
		s.diskFree = diskFree
	}
	switch {
	case opts.MinFreeMB < 0:
		s.diskFree = nil
	case opts.MinFreeMB == 0:
		s.minFreeBytes = DefaultMinFreeMB * mib
	default:
		s.minFreeBytes = int64(opts.MinFreeMB) * mib
	}
	limit := opts.MaxDBBytes
	if limit <= 0 {
		limit = int64(opts.MaxDBMB) * mib
	}
	if limit <= 0 {
		limit = DefaultMaxDBMB * mib
	}
	s.maxDBBytes.Store(limit)
	s.runCheck = s.sqliteCheck
	if opts.checkFn != nil {
		s.runCheck = opts.checkFn
	}
	return s
}

// init migrates the schema, then opens the reader pool and loads counters.
func (s *Store) init(path string, cacheMB int) error {
	ctx := context.Background()
	if err := migrate(ctx, s.writer); err != nil {
		return fmt.Errorf("sqlstore: open %s: %w", path, err)
	}
	readers, err := openPool(path, cacheMB, readerConnections, true)
	if err != nil {
		return err
	}
	s.readers = readers
	var fixes int64
	if err := s.writer.QueryRowContext(ctx, `SELECT count(*) FROM trail_fixes`).Scan(&fixes); err != nil {
		_ = readers.Close()
		return fmt.Errorf("sqlstore: count fixes: %w", err)
	}
	s.fixCount.Store(fixes)
	if err := s.pruneIDs(ctx); err != nil {
		_ = readers.Close()
		return err
	}
	return nil
}

// openPool opens a connection pool with the per-connection pragmas in the DSN.
func openPool(path string, cacheMB, conns int, readOnly bool) (*sql.DB, error) {
	q := url.Values{}
	for _, p := range []string{
		fmt.Sprintf("busy_timeout(%d)", busyTimeoutMs),
		"journal_mode(WAL)",
		"synchronous(NORMAL)",
		"foreign_keys(1)",
		fmt.Sprintf("cache_size(-%d)", cacheMB*1024),
	} {
		q.Add("_pragma", p)
	}
	if readOnly {
		q.Add("_pragma", "query_only(1)")
	}
	db, err := sql.Open("sqlite", path+"?"+q.Encode())
	if err != nil {
		return nil, fmt.Errorf("sqlstore: open %s: %w", path, err)
	}
	db.SetMaxOpenConns(conns)
	db.SetMaxIdleConns(conns)
	db.SetConnMaxLifetime(0)
	return db, nil
}

// Close checkpoints the WAL and releases both pools.
func (s *Store) Close() error {
	if s.stopMonitor != nil {
		s.stopMonitor()
		<-s.monitorDone
	}
	_, cpErr := s.writer.Exec(`PRAGMA wal_checkpoint(TRUNCATE)`)
	err := errors.Join(s.readers.Close(), s.writer.Close())
	if cpErr != nil {
		cpErr = fmt.Errorf("sqlstore: checkpoint: %w", cpErr)
	}
	return errors.Join(cpErr, err)
}

// Backup writes a consistent copy of the database to path (which must not exist) using VACUUM INTO.
func (s *Store) Backup(ctx context.Context, path string) error {
	if _, err := s.writer.ExecContext(ctx, `VACUUM INTO ?`, path); err != nil {
		return fmt.Errorf("sqlstore: backup to %s: %w", path, err)
	}
	return nil
}

// nowMs is the store clock in epoch milliseconds.
func (s *Store) nowMs() int64 { return s.now().UnixMilli() }

// prepare checks the context and ruleset shared by every call.
func prepare(ctx context.Context, rs domain.Ruleset) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("sqlstore: %w", err)
	}
	return rs.Validate()
}
