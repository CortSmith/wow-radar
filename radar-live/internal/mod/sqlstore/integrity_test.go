package sqlstore

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"radarlive/internal/domain"
)

// logBuf is a goroutine-safe log sink.
type logBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *logBuf) Write(p []byte) (int, error) { l.mu.Lock(); defer l.mu.Unlock(); return l.b.Write(p) }
func (l *logBuf) String() string              { l.mu.Lock(); defer l.mu.Unlock(); return l.b.String() }
func (l *logBuf) logger() *slog.Logger        { return slog.New(slog.NewTextHandler(l, nil)) }

// fakeClock is a settable store clock.
type fakeClock struct{ ms atomic.Int64 }

func newClock() *fakeClock { c := &fakeClock{}; c.ms.Store(baseMs); return c }

func (c *fakeClock) now() time.Time          { return time.UnixMilli(c.ms.Load()) }
func (c *fakeClock) advance(d time.Duration) { c.ms.Add(d.Milliseconds()) }

func dirEntries(t *testing.T, dir string) string {
	t.Helper()
	es, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range es {
		names = append(names, e.Name())
	}
	return strings.Join(names, ",")
}

// seedDB creates a database with data and closes it cleanly, returning its path.
func seedDB(t *testing.T, contacts, fixesEach int) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "radar.db")
	s := openAt(t, path, Options{MaxFixes: 10_000_000})
	for i := 1; i <= contacts; i++ {
		var fixes []domain.Fix
		for f := range fixesEach {
			fixes = append(fixes, domain.Fix{T: baseMs + int64(f)*1000, MapID: 1, X: float64(f), Y: float64(i)})
		}
		if _, err := s.ApplyPing(context.Background(), contactPing(fmt.Sprint("seed", i), i, "Name"+fmt.Sprint(i), fixes...)); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}

func roomy() func(string) (uint64, error) {
	return func(string) (uint64, error) { return 1 << 40, nil }
}

// ---- (2) refuse to start on bad files, touch nothing ----

func TestOpenRefusesNonSQLiteFileAndTouchesNothing(t *testing.T) {
	path := filepath.Join(t.TempDir(), "radar.db")
	junk := []byte(strings.Repeat("this is not a sqlite database ", 400))
	if err := os.WriteFile(path, junk, 0o600); err != nil {
		t.Fatal(err)
	}
	before := dirEntries(t, filepath.Dir(path))
	_, err := Open(path, Options{})
	if !errors.Is(err, ErrNotDatabase) {
		t.Fatalf("err = %v, want ErrNotDatabase", err)
	}
	for _, want := range []string{"radar-db restore", "left untouched"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error lacks %q: %v", want, err)
		}
	}
	got, _ := os.ReadFile(path)
	if !bytes.Equal(got, junk) || dirEntries(t, filepath.Dir(path)) != before {
		t.Fatalf("the refused file was modified or other files appeared: before %s after %s same=%v", before, dirEntries(t, filepath.Dir(path)), bytes.Equal(got, junk))
	}
}

func TestOpenRefusesCorruptPagesAndTouchesNothing(t *testing.T) {
	path := seedDB(t, 30, 50)
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	const page = 4096
	if len(raw) < 8*page {
		t.Fatalf("seed too small: %d bytes", len(raw))
	}
	for i := 2 * page; i < 6*page; i++ { // wreck table and index pages, keep the header valid
		raw[i] = 0xA5
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	before := dirEntries(t, filepath.Dir(path))
	_, err = Open(path, Options{})
	if !errors.Is(err, ErrCorrupt) {
		t.Fatalf("err = %v, want ErrCorrupt", err)
	}
	if !strings.Contains(err.Error(), "radar-db restore") {
		t.Fatalf("error has no restore instructions: %v", err)
	}
	after, _ := os.ReadFile(path)
	if !bytes.Equal(after, raw) || dirEntries(t, filepath.Dir(path)) != before {
		t.Fatalf("the refused file was modified or other files appeared: before %s after %s same=%v", before, dirEntries(t, filepath.Dir(path)), bytes.Equal(after, raw))
	}
}

func TestOpenRefusesNewerSchemaAndTouchesNothing(t *testing.T) {
	path := seedDB(t, 2, 2)
	s := openAt(t, path, Options{})
	if _, err := s.writer.Exec(fmt.Sprintf(`PRAGMA user_version = %d`, len(migrations)+1)); err != nil {
		t.Fatal(err)
	}
	_ = s.Close()
	raw, _ := os.ReadFile(path)
	before := dirEntries(t, filepath.Dir(path))
	_, err := Open(path, Options{})
	if !errors.Is(err, ErrNewerSchema) {
		t.Fatalf("err = %v, want ErrNewerSchema", err)
	}
	after, _ := os.ReadFile(path)
	if !bytes.Equal(after, raw) || dirEntries(t, filepath.Dir(path)) != before {
		t.Fatalf("the refused file was modified or other files appeared: before %s after %s same=%v", before, dirEntries(t, filepath.Dir(path)), bytes.Equal(after, raw))
	}
}

func TestVerify(t *testing.T) {
	good := seedDB(t, 3, 3)
	if err := Verify(good); err != nil {
		t.Fatalf("Verify(good) = %v", err)
	}
	bad := filepath.Join(t.TempDir(), "bad.db")
	_ = os.WriteFile(bad, []byte("garbage garbage garbage"), 0o600)
	if err := Verify(bad); !errors.Is(err, ErrNotDatabase) {
		t.Fatalf("Verify(bad) = %v", err)
	}
	if err := Verify(filepath.Join(t.TempDir(), "missing.db")); err == nil {
		t.Fatal("Verify(missing) must fail: a restore source has to exist")
	}
}

// ---- (1) runtime integrity ----

func TestStartupRecordsOkHealth(t *testing.T) {
	clk := newClock()
	s, _ := tempStore(t, Options{Now: clk.now})
	h := s.Health()
	if h.Integrity != domain.IntegrityOK || h.LastCheck != baseMs || h.Full {
		t.Fatalf("health = %+v", h)
	}
}

func TestNightlyCheckRunsOnTimerAndUsesFullCheck(t *testing.T) {
	var calls atomic.Int32
	var modes sync.Map
	s, _ := tempStore(t, Options{IntegrityEvery: 10 * time.Millisecond,
		checkFn: func(_ context.Context, mode string) (string, error) {
			calls.Add(1)
			modes.Store(mode, true)
			return "ok", nil
		}})
	deadline := time.Now().Add(5 * time.Second)
	for calls.Load() < 3 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if calls.Load() < 3 {
		t.Fatalf("timer ran the check %d times", calls.Load())
	}
	if _, ok := modes.Load("integrity_check"); !ok {
		t.Fatal("the timer must run the full integrity_check")
	}
	if s.Health().Integrity != domain.IntegrityOK {
		t.Fatal("health should stay ok")
	}
}

func TestIntegrityFailureDegradesWritesButNotReads(t *testing.T) {
	logs := &logBuf{}
	clk := newClock()
	s, _ := tempStore(t, Options{Now: clk.now, Logger: logs.logger()})
	ctx := context.Background()
	if _, err := s.ApplyPing(ctx, contactPing("ok1", 1, "Alice")); err != nil {
		t.Fatal(err)
	}
	s.runCheck = func(context.Context, string) (string, error) {
		return "*** in database main ***\nPage 7: btree page corrupt", nil
	}
	clk.advance(time.Hour)
	if err := s.CheckIntegrity(ctx, true); err == nil || !errors.Is(err, ErrCorrupt) {
		t.Fatalf("CheckIntegrity = %v, want ErrCorrupt", err)
	}
	h := s.Health()
	if h.Integrity != domain.IntegrityDegraded || h.LastCheck != clk.ms.Load() {
		t.Fatalf("health = %+v", h)
	}
	if _, err := s.ApplyPing(ctx, contactPing("ok2", 2, "Bob")); !errors.Is(err, domain.ErrDegraded) {
		t.Fatalf("write on degraded store = %v, want ErrDegraded", err)
	}
	if got, err := s.Search(ctx, domain.SearchQuery{RS: domain.RulesetPvP}); err != nil || got.Total != 1 {
		t.Fatalf("reads must continue: %+v %v", got, err)
	}
	for _, want := range []string{"level=ERROR", "integrity", "radar-db restore"} {
		if !strings.Contains(logs.String(), want) {
			t.Fatalf("log lacks %q:\n%s", want, logs.String())
		}
	}
	// A later good result does not silently clear a degraded store.
	s.runCheck = func(context.Context, string) (string, error) { return "ok", nil }
	_ = s.CheckIntegrity(ctx, true)
	if s.Health().Integrity != domain.IntegrityDegraded {
		t.Fatal("degraded must stay until restart")
	}
}

func TestIntegrityCheckErrorDegrades(t *testing.T) {
	s, _ := tempStore(t, Options{})
	s.runCheck = func(context.Context, string) (string, error) { return "", errors.New("disk I/O error") }
	if err := s.CheckIntegrity(context.Background(), false); err == nil {
		t.Fatal("a failing check must report an error")
	}
	if s.Health().Integrity != domain.IntegrityDegraded {
		t.Fatal("unreadable database must degrade")
	}
}

func TestRealIntegrityCheckPassesOnHealthyDB(t *testing.T) {
	s, _ := tempStore(t, Options{})
	if err := s.CheckIntegrity(context.Background(), true); err != nil {
		t.Fatalf("integrity_check on a healthy db: %v", err)
	}
	if err := s.CheckIntegrity(context.Background(), false); err != nil {
		t.Fatalf("quick_check on a healthy db: %v", err)
	}
}

// ---- (3) disk full and size caps ----

func TestLowFreeDiskReturnsStorageFullThenRecovers(t *testing.T) {
	var freeMB atomic.Int64
	freeMB.Store(500)
	clk := newClock()
	logs := &logBuf{}
	s, _ := tempStore(t, Options{Now: clk.now, Logger: logs.logger(), MinFreeMB: 200,
		DiskFree: func(string) (uint64, error) { return uint64(freeMB.Load()) << 20, nil }})
	ctx := context.Background()
	if _, err := s.ApplyPing(ctx, contactPing("a", 1, "Alice")); err != nil {
		t.Fatal(err)
	}
	freeMB.Store(100)
	clk.advance(2 * time.Second)
	if _, err := s.ApplyPing(ctx, contactPing("b", 2, "Bob")); !errors.Is(err, domain.ErrStorageFull) {
		t.Fatalf("err = %v, want ErrStorageFull", err)
	}
	if !s.Health().Full || !strings.Contains(logs.String(), "free disk") {
		t.Fatalf("health %+v logs %s", s.Health(), logs.String())
	}
	if got, err := s.Search(ctx, domain.SearchQuery{RS: domain.RulesetPvP}); err != nil || got.Total != 1 {
		t.Fatalf("reads must continue while full: %+v %v", got, err)
	}
	// The refused batch left no trace, so retrying it later is a real apply.
	freeMB.Store(900)
	clk.advance(2 * time.Second)
	res, err := s.ApplyPing(ctx, contactPing("b", 2, "Bob"))
	if err != nil || res.Duplicate {
		t.Fatalf("after recovery = %+v, %v", res, err)
	}
	if s.Health().Full || !strings.Contains(logs.String(), "recovered") {
		t.Fatalf("health %+v logs %s", s.Health(), logs.String())
	}
}

func TestDiskFreeErrorDoesNotBlockWrites(t *testing.T) {
	s, _ := tempStore(t, Options{DiskFree: func(string) (uint64, error) { return 0, errors.New("unsupported") }})
	if _, err := s.ApplyPing(context.Background(), contactPing("a", 1, "Alice")); err != nil {
		t.Fatalf("an unavailable free-space probe must not stop pings: %v", err)
	}
}

func TestSpaceCheckIsThrottled(t *testing.T) {
	var calls atomic.Int32
	clk := newClock()
	s, _ := tempStore(t, Options{Now: clk.now,
		DiskFree: func(string) (uint64, error) { calls.Add(1); return 1 << 40, nil }})
	for i := range 50 {
		_, _ = s.ApplyPing(context.Background(), contactPing(fmt.Sprint("t", i), 1, "Alice"))
	}
	if calls.Load() > 2 {
		t.Fatalf("free-space probe ran %d times for 50 pings in one second", calls.Load())
	}
}

func TestSizeCapRefusesWritesAndRecoversWhenSpaceReturns(t *testing.T) {
	path := seedDB(t, 10, 10)
	clk := newClock()
	s := openAt(t, path, Options{Now: clk.now, MaxDBBytes: 1, DiskFree: roomy()})
	defer s.Close()
	ctx := context.Background()
	if _, err := s.ApplyPing(ctx, contactPing("new1", 99, "Zed")); !errors.Is(err, domain.ErrStorageFull) {
		t.Fatalf("err = %v, want ErrStorageFull", err)
	}
	if !s.Health().Full {
		t.Fatal("Health.Full should be set")
	}
	if got, err := s.Search(ctx, domain.SearchQuery{RS: domain.RulesetPvP}); err != nil || got.Total != 10 {
		t.Fatalf("reads: %+v %v", got, err)
	}
	s.maxDBBytes.Store(1 << 40) // the operator raised the cap
	clk.advance(2 * time.Second)
	if res, err := s.ApplyPing(ctx, contactPing("new1", 99, "Zed")); err != nil || res.Duplicate {
		t.Fatalf("after recovery = %+v, %v", res, err)
	}
}

func TestSizeCapReclaimsFixesBeforeIDs(t *testing.T) {
	path := seedDB(t, 20, 200)
	clk := newClock()
	probe := openAt(t, path, Options{Now: clk.now})
	size, err := probe.logicalSize(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want, _ := probe.Trail(context.Background(), domain.RulesetPvP, guidOf(20), nil)
	_ = probe.Close()

	s := openAt(t, path, Options{Now: clk.now, MaxFixes: 10_000_000, MaxDBBytes: size - 30_000, DiskFree: roomy()})
	defer s.Close()
	ctx := context.Background()
	before := count(t, s, "batches")
	if _, err := s.ApplyPing(ctx, contactPing("fresh", 99, "Zed")); err != nil {
		t.Fatalf("reclaiming fixes should have made room: %v", err)
	}
	if got := count(t, s, "batches"); got != before+1 {
		t.Fatalf("batch ids touched while fixes could be pruned: %d -> %d", before, got)
	}
	oldest, _ := s.Trail(ctx, domain.RulesetPvP, guidOf(1), nil)  // least recently seen
	newest, _ := s.Trail(ctx, domain.RulesetPvP, guidOf(20), nil) // most recently seen
	if len(want) == 0 || len(oldest) >= len(want) || len(newest) != len(want) {
		t.Fatalf("least-recently-seen should lose fixes first: oldest %d newest %d (was %d)", len(oldest), len(newest), len(want))
	}
}

func TestSizeCapReclaimsIDsWhenFixesAreNotEnough(t *testing.T) {
	path := filepath.Join(t.TempDir(), "radar.db")
	clk := newClock()
	s := openAt(t, path, Options{Now: clk.now, DiskFree: roomy()})
	defer s.Close()
	ctx := context.Background()
	for i := range 400 { // many batch ids, no fixes at all
		if _, err := s.ApplyPing(ctx, contactPing(fmt.Sprintf("id-%04d", i), i%5+1, "Alice")); err != nil {
			t.Fatal(err)
		}
	}
	before := count(t, s, "batches")
	s.maxDBBytes.Store(1)
	clk.advance(2 * time.Second)
	_, _ = s.ApplyPing(ctx, contactPing("trigger", 1, "Alice"))
	if got := count(t, s, "batches"); got >= before {
		t.Fatalf("batch ids were not pruned: %d -> %d", before, got)
	}
}

func count(t *testing.T, s *Store, table string) int {
	t.Helper()
	var n int
	// table is a constant in every caller.
	if err := s.writer.QueryRow(`SELECT count(*) FROM ` + table).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestRealFreeSpaceProbe(t *testing.T) {
	free, err := diskFree(t.TempDir())
	if err != nil || free == 0 {
		t.Fatalf("diskFree = %d, %v", free, err)
	}
}

// ---- (4) read path is read-only ----

func TestReadPathIsReadOnly(t *testing.T) {
	s, _ := tempStore(t, Options{})
	ctx := context.Background()
	_, _ = s.ApplyPing(ctx, contactPing("a", 1, "Alice"))
	for _, stmt := range []string{
		`INSERT INTO meta (key, value) VALUES ('x', 'y')`,
		`DELETE FROM contacts`,
		`UPDATE contacts SET name = 'pwned'`,
		`CREATE TABLE evil (a)`,
		`DROP TABLE contacts`,
		`PRAGMA user_version = 99`,
	} {
		if _, err := s.readers.ExecContext(ctx, stmt); err == nil {
			t.Fatalf("reader pool accepted a write: %s", stmt)
		}
	}
	if got, _ := s.Search(ctx, domain.SearchQuery{RS: domain.RulesetPvP}); got.Total != 1 || got.Contacts[0].Name != "Alice" {
		t.Fatalf("data changed through the reader pool: %+v", got)
	}
}

func TestReadHandlersNeverTouchTheWriterPool(t *testing.T) {
	for _, f := range []string{"read.go", "bounty.go", "filter.go"} {
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(src), "s.writer") {
			t.Errorf("%s uses the writer pool; read paths must use s.readers only", f)
		}
	}
}

// ---- (6) permissions ----

func TestPermProblemTable(t *testing.T) {
	tests := []struct {
		name  string
		goos  string
		isDir bool
		mode  os.FileMode
		bad   bool
	}{
		{"dir 0700", "linux", true, 0o700, false},
		{"dir 0750", "linux", true, 0o750, true},
		{"dir 0755", "linux", true, 0o755, true},
		{"dir 0770", "linux", true, 0o770, true},
		{"file 0600", "linux", false, 0o600, false},
		{"file 0400", "linux", false, 0o400, false},
		{"file 0640", "linux", false, 0o640, true},
		{"file 0644", "linux", false, 0o644, true},
		{"file 0666", "linux", false, 0o666, true},
		{"windows ignores dir", "windows", true, 0o777, false},
		{"windows ignores file", "windows", false, 0o666, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			msg := permProblem(tc.goos, tc.isDir, tc.mode)
			if (msg != "") != tc.bad {
				t.Fatalf("permProblem(%s, dir=%v, %v) = %q, want problem=%v", tc.goos, tc.isDir, tc.mode, msg, tc.bad)
			}
		})
	}
}

func TestOpenCreatesPrivateDirAndFilesOnUnix(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("mode bits are not meaningful on Windows")
	}
	dir := filepath.Join(t.TempDir(), "data")
	s := openAt(t, filepath.Join(dir, "radar.db"), Options{})
	_, _ = s.ApplyPing(context.Background(), contactPing("a", 1, "Alice"))
	defer s.Close()
	for p, want := range map[string]os.FileMode{dir: 0o700, filepath.Join(dir, "radar.db"): 0o600} {
		st, err := os.Stat(p)
		if err != nil || st.Mode().Perm() != want {
			t.Fatalf("%s mode = %v err %v, want %v", p, st.Mode().Perm(), err, want)
		}
	}
}

func TestOpenWarnsOnLoosePermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("mode bits are not meaningful on Windows")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "radar.db")
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	_ = os.Chmod(path, 0o644)
	_ = os.Chmod(dir, 0o755)
	logs := &logBuf{}
	s := openAt(t, path, Options{Logger: logs.logger()})
	defer s.Close()
	out := logs.String()
	if strings.Count(out, "level=WARN") < 2 || !strings.Contains(out, "permissions") {
		t.Fatalf("expected loud warnings for dir and file:\n%s", out)
	}
}

// ---- lock ----

func TestAcquireLockIsExclusiveAndReleasable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "radar.db")
	release, err := AcquireLock(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := AcquireLock(path); !errors.Is(err, ErrLocked) {
		t.Fatalf("second lock = %v, want ErrLocked", err)
	}
	if err := release(); err != nil {
		t.Fatal(err)
	}
	release2, err := AcquireLock(path)
	if err != nil {
		t.Fatalf("lock after release: %v", err)
	}
	_ = release2()
}

// ---- (7) retry flood ----

func TestReplayFloodLeavesCountersUnchangedAndIsCheap(t *testing.T) {
	s, _ := tempStore(t, Options{})
	ctx := context.Background()
	ping := contactPing("flood", 1, "Alice", domain.Fix{T: baseMs, MapID: 1, X: 1, Y: 2})
	if _, err := s.ApplyPing(ctx, ping); err != nil {
		t.Fatal(err)
	}
	snap := func() string {
		got, _ := s.Search(ctx, domain.SearchQuery{RS: domain.RulesetPvP})
		st, _ := s.Status(ctx)
		c := got.Contacts[0]
		return fmt.Sprintf("%d %d %d %d | %+v | fixes %d batches %d",
			got.Total, c.Events, c.SrcEvents, c.LastSeen, st, s.fixCount.Load(), count(t, s, "batches"))
	}
	before := snap()
	start := time.Now()
	for i := range 10_000 {
		res, err := s.ApplyPing(ctx, ping)
		if err != nil || !res.Duplicate {
			t.Fatalf("replay %d = %+v, %v", i, res, err)
		}
	}
	elapsed := time.Since(start)
	if after := snap(); after != before {
		t.Fatalf("counters moved under replay:\n%s\n%s", before, after)
	}
	if elapsed > 5*time.Second {
		t.Fatalf("10k replays took %v", elapsed)
	}
	t.Logf("10k duplicate replays: %v (%v each)", elapsed, elapsed/10_000)
}

func BenchmarkDuplicateReplay(b *testing.B) {
	path := filepath.Join(b.TempDir(), "radar.db")
	s, err := Open(path, Options{})
	if err != nil {
		b.Fatal(err)
	}
	defer s.Close()
	ping := contactPing("bench", 1, "Alice")
	_, _ = s.ApplyPing(context.Background(), ping)
	b.ResetTimer()
	for range b.N {
		_, _ = s.ApplyPing(context.Background(), ping)
	}
}

type recorded struct {
	mu  sync.Mutex
	got []string
}

func (r *recorded) add(event, reason string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.got = append(r.got, event+"/"+reason)
}

func (r *recorded) list() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.got...)
}

func TestStateChangesAreReportedOncePerTransition(t *testing.T) {
	var freeMB atomic.Int64
	freeMB.Store(100)
	clk := newClock()
	rec := &recorded{}
	s, _ := tempStore(t, Options{Now: clk.now, MinFreeMB: 200, OnEvent: rec.add,
		DiskFree: func(string) (uint64, error) { return uint64(freeMB.Load()) << 20, nil }})
	ctx := context.Background()
	for i := 0; i < 3; i++ {
		clk.advance(2 * time.Second)
		if _, err := s.ApplyPing(ctx, contactPing(fmt.Sprint("b", i), 1, "Alice")); !errors.Is(err, domain.ErrStorageFull) {
			t.Fatalf("err = %v", err)
		}
	}
	if got := rec.list(); len(got) != 1 || got[0] != "disk_full/disk_low" {
		t.Fatalf("events = %v, want one disk_full/disk_low", got)
	}
	s.runCheck = func(context.Context, string) (string, error) { return "Page 7: corrupt", nil }
	_ = s.CheckIntegrity(ctx, false)
	_ = s.CheckIntegrity(ctx, true)
	if got := rec.list(); len(got) != 2 || got[1] != "integrity_degraded/quick_check" {
		t.Fatalf("events = %v", got)
	}
}

func TestSizeCapIsReportedAsSizeCap(t *testing.T) {
	path := seedDB(t, 10, 10)
	rec := &recorded{}
	s := openAt(t, path, Options{Now: newClock().now, MaxDBBytes: 1, DiskFree: roomy(), OnEvent: rec.add})
	defer s.Close()
	_, _ = s.ApplyPing(context.Background(), contactPing("new1", 99, "Zed"))
	if got := rec.list(); len(got) != 1 || got[0] != "disk_full/size_cap" {
		t.Fatalf("events = %v", got)
	}
}
