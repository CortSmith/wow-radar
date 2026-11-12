package sqlstore

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"radarlive/internal/core"
	"radarlive/internal/domain"
)

const baseMs = 1_700_000_000_000

func guidOf(n int) string { return fmt.Sprintf("Player-%04d-ABCDEF", n) }

func openAt(t *testing.T, path string, opts Options) *Store {
	t.Helper()
	s, err := Open(path, opts)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	return s
}

func tempStore(t *testing.T, opts Options) (*Store, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "radar.db")
	s := openAt(t, path, opts)
	t.Cleanup(func() { _ = s.Close() })
	return s, path
}

func contactPing(batch string, n int, name string, fixes ...domain.Fix) domain.Ping {
	c := domain.PingContact{Fixes: fixes, D: domain.Deltas{Events: 1}}
	c.GUID, c.Name, c.Raw, c.Reaction = guidOf(n), name, name+"-Realm", domain.ReactionHostile
	c.FirstSeen, c.LastSeen = baseMs, baseMs+int64(n)
	return domain.Ping{RS: domain.RulesetPvP, BatchID: batch, Contacts: []domain.PingContact{c}}
}

func TestOpenMigratesEmptyFileAndIsIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "radar.db")
	for range 2 {
		s := openAt(t, path, Options{})
		var version int
		if err := s.writer.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil || version != len(migrations) {
			t.Fatalf("user_version = %d, err %v", version, err)
		}
		var mode string
		_ = s.writer.QueryRow(`PRAGMA journal_mode`).Scan(&mode)
		if mode != "wal" {
			t.Fatalf("journal_mode = %q", mode)
		}
		if err := s.Close(); err != nil {
			t.Fatal(err)
		}
	}
}

func TestOpenRejectsNewerSchema(t *testing.T) {
	path := filepath.Join(t.TempDir(), "radar.db")
	s := openAt(t, path, Options{})
	if _, err := s.writer.Exec(fmt.Sprintf(`PRAGMA user_version = %d`, len(migrations)+5)); err != nil {
		t.Fatal(err)
	}
	_ = s.Close()
	_, err := Open(path, Options{})
	if err == nil || !strings.Contains(err.Error(), "newer") {
		t.Fatalf("err = %v, want a 'newer than this build' error", err)
	}
}

func TestOpenRejectsCorruptFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "radar.db")
	if err := os.WriteFile(path, []byte(strings.Repeat("this is not a sqlite database ", 400)), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(path, Options{}); err == nil || !strings.Contains(err.Error(), "sqlstore") {
		t.Fatalf("err = %v, want a wrapped sqlstore error", err)
	}
}

func TestOpenFailsInMissingDirectory(t *testing.T) {
	if _, err := Open(filepath.Join(t.TempDir(), "no", "such", "dir", "x.db"), Options{}); err == nil {
		t.Fatal("expected an error")
	}
}

func TestDataAndBatchIdsSurviveReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "radar.db")
	s := openAt(t, path, Options{})
	ping := contactPing("b1", 1, "Alice", domain.Fix{T: baseMs, MapID: 1, X: 1, Y: 2})
	ping.Charts = []domain.Chart{{MapID: 1, Name: "Vale", X1: 10, Y1: 10}}
	if _, err := s.ApplyPing(context.Background(), ping); err != nil {
		t.Fatal(err)
	}
	_ = s.Close()

	s = openAt(t, path, Options{})
	defer s.Close()
	res, err := s.ApplyPing(context.Background(), ping)
	if err != nil || !res.Duplicate {
		t.Fatalf("replay after reopen = %+v, %v; want Duplicate", res, err)
	}
	got, _ := s.Search(context.Background(), domain.SearchQuery{RS: domain.RulesetPvP})
	if got.Total != 1 || got.Contacts[0].Events != 1 {
		t.Fatalf("contacts after reopen = %+v", got)
	}
	if fixes, _ := s.Trail(context.Background(), domain.RulesetPvP, guidOf(1), nil); len(fixes) != 1 {
		t.Fatalf("trail = %v", fixes)
	}
	if charts, _ := s.Charts(context.Background(), domain.RulesetPvP); len(charts) != 1 {
		t.Fatalf("charts = %v", charts)
	}
}

func TestBatchIdsPrunedToNewest(t *testing.T) {
	path := filepath.Join(t.TempDir(), "radar.db")
	s := openAt(t, path, Options{})
	tx, _ := s.writer.Begin()
	for i := range batchesKeptMax + 50 {
		if _, err := tx.Exec(`INSERT INTO batches (rs, batch_id, applied_at) VALUES ('pvp', ?, 0)`, fmt.Sprint("old", i)); err != nil {
			t.Fatal(err)
		}
	}
	_ = tx.Commit()
	_ = s.Close()

	s = openAt(t, path, Options{})
	defer s.Close()
	var n int
	_ = s.writer.QueryRow(`SELECT count(*) FROM batches`).Scan(&n)
	if n != batchesKeptMax {
		t.Fatalf("batches kept = %d, want %d", n, batchesKeptMax)
	}
	var oldest string
	_ = s.writer.QueryRow(`SELECT batch_id FROM batches ORDER BY seq LIMIT 1`).Scan(&oldest)
	if oldest != "old50" {
		t.Fatalf("oldest kept = %q, want old50", oldest)
	}
}

func TestPeriodicPruneRunsAfterManyInserts(t *testing.T) {
	s, _ := tempStore(t, Options{})
	tx, _ := s.writer.Begin()
	for i := range deathsKeptMax + 10 {
		_, _ = tx.Exec(`INSERT INTO deaths (rs, id, t, victim, killer, assists) VALUES ('pvp', ?, 0, '', '', '')`, i)
	}
	_ = tx.Commit()
	ping := contactPing("trigger", 1, "Alice")
	ping.Deaths = make([]domain.Death, pruneEveryInserts)
	for i := range ping.Deaths {
		ping.Deaths[i] = domain.Death{ID: fmt.Sprint("new", i), Victim: "Player-1"}
	}
	if _, err := s.ApplyPing(context.Background(), ping); err != nil {
		t.Fatal(err)
	}
	var n int
	_ = s.writer.QueryRow(`SELECT count(*) FROM deaths`).Scan(&n)
	if n != deathsKeptMax {
		t.Fatalf("deaths kept = %d, want %d", n, deathsKeptMax)
	}
}

func TestSearchTreatsWildcardsLiterally(t *testing.T) {
	s, _ := tempStore(t, Options{})
	ctx := context.Background()
	for i, name := range []string{"100%Real", "under_score", "plain"} {
		if _, err := s.ApplyPing(ctx, contactPing(fmt.Sprint("b", i), i+1, name)); err != nil {
			t.Fatal(err)
		}
	}
	for q, want := range map[string]int{"%": 1, "_": 1, "100%": 1, "'; DROP TABLE contacts;--": 0, "\x1f": 0, "PLAIN": 1, "  plain ": 1} {
		got, err := s.Search(ctx, domain.SearchQuery{RS: domain.RulesetPvP, Q: q})
		if err != nil || got.Total != want || len(got.Contacts) != want {
			t.Errorf("Search(%q) = %d, %v; want %d", q, got.Total, err, want)
		}
	}
}

func TestSweepAndSearchMatchDomainRules(t *testing.T) {
	s, _ := tempStore(t, Options{Now: func() time.Time { return time.UnixMilli(baseMs + 60_000) }})
	ctx := context.Background()
	for i := 1; i <= 6; i++ {
		p := contactPing(fmt.Sprint("b", i), i, fmt.Sprint("Name", i))
		p.Contacts[0].Reaction = []domain.Reaction{domain.ReactionFriendly, domain.ReactionHostile, domain.ReactionNeutral}[i%3]
		p.Contacts[0].MapID, p.Contacts[0].PosTime = i%2, baseMs
		if _, err := s.ApplyPing(ctx, p); err != nil {
			t.Fatal(err)
		}
	}
	zero := 0
	for _, g := range []domain.Group{"", domain.GroupFriendly, domain.GroupHostile, domain.GroupOther} {
		sec := domain.Sector{RS: domain.RulesetPvP, Scope: domain.ScopeZone, MapID: &zero, Group: g, Query: "name"}
		res, err := s.Sweep(ctx, sec)
		if err != nil {
			t.Fatal(err)
		}
		for _, c := range res.Contacts {
			if !sec.Includes(c, time.UnixMilli(baseMs+60_000)) {
				t.Errorf("group %q returned %s which the sector excludes", g, c.GUID)
			}
		}
	}
}

func TestSweepActiveWindowUsesStoreClock(t *testing.T) {
	now := time.UnixMilli(baseMs + 1000)
	s, _ := tempStore(t, Options{Now: func() time.Time { return now }})
	ctx := context.Background()
	_, _ = s.ApplyPing(ctx, contactPing("b1", 1, "Alice"))
	all := domain.Sector{RS: domain.RulesetPvP, Scope: domain.ScopeAll}
	if res, _ := s.Sweep(ctx, all); len(res.Contacts) != 1 {
		t.Fatalf("fresh contact missing: %+v", res)
	}
	now = now.Add(domain.DefaultActiveWindow + time.Minute)
	if res, _ := s.Sweep(ctx, all); len(res.Contacts) != 0 {
		t.Fatalf("stale contact still listed: %+v", res)
	}
	player := domain.Sector{RS: domain.RulesetPvP, Scope: domain.ScopePlayer, GUID: guidOf(1)}
	if res, _ := s.Sweep(ctx, player); len(res.Contacts) != 1 {
		t.Fatal("player scope must ignore the active window")
	}
}

func TestPerContactTrailCap(t *testing.T) {
	s, _ := tempStore(t, Options{})
	ctx := context.Background()
	for batch := range 2 {
		fixes := make([]domain.Fix, 3000)
		for i := range fixes {
			fixes[i] = domain.Fix{T: baseMs + int64(batch*3000+i)*10_000, MapID: 1, X: float64(i * 5), Y: 0}
		}
		if _, err := s.ApplyPing(ctx, contactPing(fmt.Sprint("b", batch), 1, "Alice", fixes...)); err != nil {
			t.Fatal(err)
		}
	}
	got, _ := s.Trail(ctx, domain.RulesetPvP, guidOf(1), nil)
	if len(got) != domain.TrailCapFixes {
		t.Fatalf("trail length %d, want %d", len(got), domain.TrailCapFixes)
	}
	if want := int64(baseMs + 1000*10_000); got[0].T != want {
		t.Fatalf("oldest kept t = %d, want %d", got[0].T, want)
	}
	if s.fixCount.Load() != domain.TrailCapFixes {
		t.Fatalf("fixCount = %d", s.fixCount.Load())
	}
}

func TestGlobalFixCapPrunesLeastRecentlySeen(t *testing.T) {
	s, _ := tempStore(t, Options{MaxFixes: 100})
	ctx := context.Background()
	for n := 1; n <= 4; n++ { // contact n is seen at baseMs+n, so contact 1 is the least recent
		fixes := make([]domain.Fix, 30)
		for i := range fixes {
			fixes[i] = domain.Fix{T: baseMs + int64(i+1)*10_000, MapID: 1, X: float64(i * 5)}
		}
		if _, err := s.ApplyPing(ctx, contactPing(fmt.Sprint("b", n), n, fmt.Sprint("N", n), fixes...)); err != nil {
			t.Fatal(err)
		}
	}
	total := s.fixCount.Load()
	if total > 100 || total < 80 {
		t.Fatalf("fixCount = %d, want within 80..100", total)
	}
	var actual int64
	_ = s.writer.QueryRow(`SELECT count(*) FROM trail_fixes`).Scan(&actual)
	if actual != total {
		t.Fatalf("counter %d disagrees with table %d", total, actual)
	}
	oldest, _ := s.Trail(ctx, domain.RulesetPvP, guidOf(1), nil)
	newest, _ := s.Trail(ctx, domain.RulesetPvP, guidOf(4), nil)
	if len(oldest) >= 30 || len(newest) != 30 {
		t.Fatalf("oldest contact keeps %d, newest %d fixes", len(oldest), len(newest))
	}
}

func TestBackupProducesOpenableCopy(t *testing.T) {
	s, _ := tempStore(t, Options{})
	ctx := context.Background()
	_, _ = s.ApplyPing(ctx, contactPing("b1", 1, "Alice"))
	dest := filepath.Join(t.TempDir(), "backup.db")
	if err := s.Backup(ctx, dest); err != nil {
		t.Fatal(err)
	}
	copyStore := openAt(t, dest, Options{})
	defer copyStore.Close()
	if got, _ := copyStore.Search(ctx, domain.SearchQuery{RS: domain.RulesetPvP}); got.Total != 1 {
		t.Fatalf("backup holds %d contacts", got.Total)
	}
	if err := s.Backup(ctx, dest); err == nil {
		t.Fatal("backing up over an existing file must fail")
	}
}

func TestReadersRunWhileWriterWrites(t *testing.T) {
	s, _ := tempStore(t, Options{})
	ctx := context.Background()
	var wg sync.WaitGroup
	errs := make(chan error, 64)
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := range 60 {
			if _, err := s.ApplyPing(ctx, contactPing(fmt.Sprint("w", i), i%10+1, "Alice",
				domain.Fix{T: baseMs + int64(i+1)*10_000, MapID: 1, X: float64(i * 9)})); err != nil {
				errs <- err
			}
		}
	}()
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 60 {
				if _, err := s.Sweep(ctx, domain.Sector{RS: domain.RulesetPvP, Scope: domain.ScopeAll}); err != nil {
					errs <- err
				}
				if _, err := s.Status(ctx); err != nil {
					errs <- err
				}
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
}

func TestLargePingPerformance(t *testing.T) {
	s, _ := tempStore(t, Options{})
	fixes := make([]domain.Fix, 5000)
	for i := range fixes {
		fixes[i] = domain.Fix{T: baseMs + int64(i+1)*10_000, MapID: 1, X: float64(i * 5)}
	}
	start := time.Now()
	if _, err := s.ApplyPing(context.Background(), contactPing("big", 1, "Alice", fixes...)); err != nil {
		t.Fatal(err)
	}
	if took := time.Since(start); took > time.Second {
		t.Fatalf("5000-fix ping took %v, want < 1s", took)
	}
}

func TestInvalidInputWrapsErrInvalid(t *testing.T) {
	s, _ := tempStore(t, Options{})
	ctx := context.Background()
	if _, err := s.Search(ctx, domain.SearchQuery{RS: "nope"}); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("err = %v", err)
	}
	if _, err := s.BountyPlayer(ctx, domain.RulesetPvP, "Player-0-X"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("err = %v", err)
	}
}

func TestClosedStoreReportsErrors(t *testing.T) {
	path := filepath.Join(t.TempDir(), "radar.db")
	s := openAt(t, path, Options{})
	_ = s.Close()
	ctx := context.Background()
	if _, err := s.ApplyPing(ctx, contactPing("b", 1, "A")); err == nil {
		t.Error("ApplyPing on closed store should fail")
	}
	if _, err := s.Sweep(ctx, domain.Sector{RS: domain.RulesetPvP, Scope: domain.ScopeAll}); err == nil {
		t.Error("Sweep on closed store should fail")
	}
	if _, err := s.Search(ctx, domain.SearchQuery{RS: domain.RulesetPvP}); err == nil {
		t.Error("Search on closed store should fail")
	}
	if _, err := s.Trail(ctx, domain.RulesetPvP, guidOf(1), nil); err == nil {
		t.Error("Trail on closed store should fail")
	}
	if _, err := s.Charts(ctx, domain.RulesetPvP); err == nil {
		t.Error("Charts on closed store should fail")
	}
	if _, err := s.Status(ctx); err == nil {
		t.Error("Status on closed store should fail")
	}
	if _, err := s.Bounty(ctx, domain.RulesetPvP, 0); err == nil {
		t.Error("Bounty on closed store should fail")
	}
	if err := s.Backup(ctx, filepath.Join(t.TempDir(), "x.db")); err == nil {
		t.Error("Backup on closed store should fail")
	}
}

func TestCorruptRowsAreReportedNotHidden(t *testing.T) {
	s, _ := tempStore(t, Options{})
	ctx := context.Background()
	_, _ = s.ApplyPing(ctx, contactPing("b1", 1, "Alice"))
	if _, err := s.writer.Exec(`UPDATE contacts SET past_names = '{not json'`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Search(ctx, domain.SearchQuery{RS: domain.RulesetPvP}); err == nil {
		t.Fatal("expected a decode error")
	}
}

func TestStatusCountsAndLastWrite(t *testing.T) {
	now := time.UnixMilli(baseMs)
	s, _ := tempStore(t, Options{Now: func() time.Time { return now }})
	ctx := context.Background()
	ping := contactPing("b1", 1, "Alice")
	ping.Deaths = []domain.Death{{ID: "d1", Victim: "Player-1"}, {ID: "d1", Victim: "Player-1"}, {ID: "d2", Victim: "Player-1"}}
	_, _ = s.ApplyPing(ctx, ping)
	st, err := s.Status(ctx)
	if err != nil || len(st) != 1 || st[0].Contacts != 1 || st[0].Deaths != 2 || st[0].LastWrite != baseMs {
		t.Fatalf("status = %+v, %v", st, err)
	}
}

// moduleHost builds a host with the module registered and settings loaded from args.
func moduleHost(t *testing.T, args ...string) (*core.Host, *Module) {
	t.Helper()
	host := core.NewHost()
	m := New()
	if err := host.Register(m); err != nil {
		t.Fatal(err)
	}
	if err := host.Config.Load(args, func(string) (string, bool) { return "", false }); err != nil {
		t.Fatal(err)
	}
	return host, m
}

func TestModuleRegistersStoreAndOpensOnStart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mod.db")
	host, m := moduleHost(t, "--db-path", path, "--cache-mb", "4", "--max-fixes", "1000")
	if m.Name() != ModuleName {
		t.Fatalf("name %q", m.Name())
	}
	store, ok := host.Stores.Get(StoreName)
	if !ok {
		t.Fatal("sqlite store not registered")
	}
	if err := m.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("database file not created: %v", err)
	}
	ctx := context.Background()
	if _, err := store.ApplyPing(ctx, contactPing("b1", 1, "Alice")); err != nil {
		t.Fatal(err)
	}
	checks := []func() error{
		func() error {
			_, err := store.Sweep(ctx, domain.Sector{RS: domain.RulesetPvP, Scope: domain.ScopeAll})
			return err
		},
		func() error { _, err := store.Search(ctx, domain.SearchQuery{RS: domain.RulesetPvP}); return err },
		func() error { _, err := store.Trail(ctx, domain.RulesetPvP, guidOf(1), nil); return err },
		func() error { _, err := store.Bounty(ctx, domain.RulesetPvP, 1); return err },
		func() error { _, err := store.Charts(ctx, domain.RulesetPvP); return err },
		func() error { _, err := store.Status(ctx); return err },
	}
	for i, check := range checks {
		if err := check(); err != nil {
			t.Errorf("delegated call %d: %v", i, err)
		}
	}
	if _, err := store.BountyPlayer(ctx, domain.RulesetPvP, guidOf(1)); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("BountyPlayer err = %v", err)
	}
	if err := host.Stores.CloseAll(); err != nil {
		t.Fatal(err)
	}
}

func TestModuleStartFailsOnUnusablePath(t *testing.T) {
	bad := filepath.Join(t.TempDir(), "missing", "dir", "x.db")
	host, m := moduleHost(t, "--db-path", bad)
	if err := m.Start(context.Background()); err == nil {
		t.Fatal("expected Start to fail")
	}
	store, _ := host.Stores.Get(StoreName)
	if _, err := store.Status(context.Background()); err == nil {
		t.Fatal("store must keep reporting the open error")
	}
	if _, err := store.ApplyPing(context.Background(), contactPing("b", 1, "A")); err == nil {
		t.Fatal("ApplyPing must report the open error")
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close of a never-opened store: %v", err)
	}
}

func TestModuleClosedBeforeUse(t *testing.T) {
	host, _ := moduleHost(t, "--db-path", filepath.Join(t.TempDir(), "x.db"))
	if err := host.Stores.CloseAll(); err != nil {
		t.Fatal(err)
	}
}

func TestModuleDefaultsDeclared(t *testing.T) {
	host, _ := moduleHost(t)
	if host.Config.String(KeyDBPath) == "" || host.Config.Int(KeyCacheMB) != DefaultCacheMB ||
		host.Config.Int(KeyMaxFixes) != DefaultMaxFixes {
		t.Fatal("defaults not declared")
	}
}

func TestStoreImplementsCoreStore(t *testing.T) {
	var _ core.Store = (*Store)(nil)
	var _ core.Store = (*lazyStore)(nil)
}

func benchStore(b *testing.B) *Store {
	b.Helper()
	s, err := Open(filepath.Join(b.TempDir(), "bench.db"), Options{})
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { _ = s.Close() })
	return s
}

func BenchmarkApplyPing(b *testing.B) {
	s := benchStore(b)
	ctx := context.Background()
	for i := 0; b.Loop(); i++ {
		p := domain.Ping{RS: domain.RulesetPvP, BatchID: fmt.Sprint("b", i)}
		for n := range 100 {
			c := contactPing("", n+1, fmt.Sprint("Name", n)).Contacts[0]
			c.LastSeen = baseMs + int64(i)*1000
			c.Fixes = []domain.Fix{{T: baseMs + int64(i+1)*10_000, MapID: 1, X: float64(i * 9)}}
			p.Contacts = append(p.Contacts, c)
		}
		if _, err := s.ApplyPing(ctx, p); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkSweepZone(b *testing.B) {
	s := benchStore(b)
	ctx := context.Background()
	p := domain.Ping{RS: domain.RulesetPvP, BatchID: "seed"}
	for n := range 2000 {
		c := contactPing("", n+1, fmt.Sprint("Name", n)).Contacts[0]
		c.LastSeen, c.MapID, c.PosTime = time.Now().UnixMilli(), n%5, time.Now().UnixMilli()
		p.Contacts = append(p.Contacts, c)
		if len(p.Contacts) == domain.PingMaxContacts {
			if _, err := s.ApplyPing(ctx, p); err != nil {
				b.Fatal(err)
			}
			p = domain.Ping{RS: domain.RulesetPvP, BatchID: fmt.Sprint("seed", n)}
		}
	}
	zone := 1
	sector := domain.Sector{RS: domain.RulesetPvP, Scope: domain.ScopeZone, MapID: &zone}
	for b.Loop() {
		if _, err := s.Sweep(ctx, sector); err != nil {
			b.Fatal(err)
		}
	}
}
