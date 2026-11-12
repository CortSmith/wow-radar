package sqlstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"radarlive/internal/core"
	"radarlive/internal/domain"
)

var (
	_ core.ArmoryStore = (*Store)(nil)
	_ core.ArmoryStore = (*lazyStore)(nil)
)

func armTestChar(name string, ms int64) domain.ArmoryCharacter {
	return domain.ArmoryCharacter{
		Realm: "Testrealm", Name: name, Class: "Mage", Race: "Human", Faction: "Alliance", Level: 60,
		Guild: "Synthetic Guild", Health: 4000, PowerType: "mana", Power: 5000,
		Gear: json.RawMessage(`{"head":{"id":1}}`), FetchedAt: ms, Status: domain.ArmoryStatusOK,
	}
}

func armTestBatch(id string, chars ...domain.ArmoryCharacter) domain.ArmoryBatch {
	return domain.ArmoryBatch{BatchID: id, RunID: "run-1", Characters: chars}
}

func tableNames(t *testing.T, db *sql.DB) map[string]bool {
	t.Helper()
	rows, err := db.Query(`SELECT name FROM sqlite_master WHERE type IN ('table','index')`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			t.Fatal(err)
		}
		out[n] = true
	}
	return out
}

func TestMigrationV2CreatesArmoryTablesAndIndexes(t *testing.T) {
	s, _ := tempStore(t, Options{})
	if len(migrations) != 2 {
		t.Fatalf("migrations = %d, want 2", len(migrations))
	}
	names := tableNames(t, s.writer)
	for _, n := range []string{"armory_characters", "armory_snapshots", "armory_batches", "armory_runs",
		"armory_realm_class", "armory_guild", "armory_fetched"} {
		if !names[n] {
			t.Errorf("missing %s", n)
		}
	}
}

func TestMigrationFromV1KeepsData(t *testing.T) {
	path := filepath.Join(t.TempDir(), "radar.db")
	// Build a v1 database: current schema minus the v2 tables, user_version 1.
	s := openAt(t, path, Options{})
	if _, err := s.ApplyPing(context.Background(), contactPing("b1", 1, "Alice", domain.Fix{T: baseMs, MapID: 1, X: 1, Y: 2})); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{`DROP TABLE armory_characters`, `DROP TABLE armory_snapshots`, `DROP TABLE armory_batches`,
		`DROP TABLE armory_runs`, `PRAGMA user_version = 1`} {
		if _, err := s.writer.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	_ = s.Close()

	if err := Verify(path); err != nil {
		t.Fatalf("a v1 file must pass preflight: %v", err)
	}
	s = openAt(t, path, Options{})
	defer s.Close()
	var v int
	if err := s.writer.QueryRow(`PRAGMA user_version`).Scan(&v); err != nil || v != 2 {
		t.Fatalf("user_version = %d, %v", v, err)
	}
	if !tableNames(t, s.writer)["armory_characters"] {
		t.Fatal("armory tables not created")
	}
	got, _ := s.Search(context.Background(), domain.SearchQuery{RS: domain.RulesetPvP})
	if got.Total != 1 || got.Contacts[0].Name != "Alice" {
		t.Fatalf("contacts after migration: %+v", got)
	}
	if fixes, _ := s.Trail(context.Background(), domain.RulesetPvP, guidOf(1), nil); len(fixes) != 1 {
		t.Fatalf("trail %v", fixes)
	}
	if res, err := s.ApplyPing(context.Background(), contactPing("b1", 1, "Alice")); err != nil || !res.Duplicate {
		t.Fatalf("batch ids must survive: %+v %v", res, err)
	}
	if _, err := s.ApplyArmoryBatch(context.Background(), armTestBatch("a1", armTestChar("Aelwyn", baseMs))); err != nil {
		t.Fatal(err)
	}
}

func TestPreflightAcceptsV2AndRefusesV3(t *testing.T) {
	path := seedDB(t, 1, 1)
	if err := Verify(path); err != nil {
		t.Fatalf("v2: %v", err)
	}
	s := openAt(t, path, Options{})
	_, _ = s.writer.Exec(`PRAGMA user_version = 3`)
	_ = s.Close()
	if err := Verify(path); !errors.Is(err, ErrNewerSchema) {
		t.Fatalf("v3: %v", err)
	}
}

func TestArmoryDataSurvivesReopenAndReplayStaysNoop(t *testing.T) {
	path := filepath.Join(t.TempDir(), "radar.db")
	s := openAt(t, path, Options{})
	b := armTestBatch("a1", armTestChar("Aelwyn", baseMs))
	if _, err := s.ApplyArmoryBatch(context.Background(), b); err != nil {
		t.Fatal(err)
	}
	_ = s.Close()
	s = openAt(t, path, Options{})
	defer s.Close()
	res, err := s.ApplyArmoryBatch(context.Background(), b)
	if err != nil || !res.Duplicate {
		t.Fatalf("replay after reopen: %+v %v", res, err)
	}
	if c, err := s.GetArmoryCharacter(context.Background(), "testrealm", "AELWYN"); err != nil || c.Level != 60 {
		t.Fatalf("%+v %v", c, err)
	}
}

func TestArmoryWritesRespectDegraded(t *testing.T) {
	s, _ := tempStore(t, Options{checkFn: func(context.Context, string) (string, error) { return "bad", nil }})
	_ = s.CheckIntegrity(context.Background(), false)
	_, err := s.ApplyArmoryBatch(context.Background(), armTestBatch("a1", armTestChar("Aelwyn", baseMs)))
	if !errors.Is(err, domain.ErrDegraded) {
		t.Fatalf("err = %v, want ErrDegraded", err)
	}
	if _, err := s.GetArmoryCharacter(context.Background(), "Testrealm", "Aelwyn"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("reads continue: %v", err)
	}
}

func TestArmoryCanceledContext(t *testing.T) {
	s, _ := tempStore(t, Options{})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := s.ApplyArmoryBatch(ctx, armTestBatch("a1")); err == nil {
		t.Fatal("apply: want error")
	}
	if _, err := s.GetArmoryCharacter(ctx, "r", "n"); err == nil {
		t.Fatal("get: want error")
	}
	if _, err := s.ListArmoryCharacters(ctx, "r", nil); err == nil {
		t.Fatal("list: want error")
	}
	if _, err := s.ArmorySnapshots(ctx, "r", "n"); err == nil {
		t.Fatal("snapshots: want error")
	}
	if _, err := s.PruneArmorySnapshots(ctx, 1); err == nil {
		t.Fatal("prune: want error")
	}
	if err := s.RecordArmoryRun(ctx, domain.ArmoryRun{}); err == nil {
		t.Fatal("record: want error")
	}
	if _, err := s.LastArmoryRun(ctx); err == nil {
		t.Fatal("last: want error")
	}
}

func TestArmoryBatchIdsPrunedAndSnapshotsAgedOut(t *testing.T) {
	clk := newClock()
	s, _ := tempStore(t, Options{Now: clk.now})
	ctx := context.Background()
	old := baseMs - int64(armorySnapshotKeepDays+5)*86_400_000
	if _, err := s.ApplyArmoryBatch(ctx, armTestBatch("a1", armTestChar("Aelwyn", old))); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ApplyArmoryBatch(ctx, armTestBatch("a2", armTestChar("Aelwyn", baseMs))); err != nil {
		t.Fatal(err)
	}
	tx, _ := s.writer.Begin()
	for i := range armoryBatchesKeptMax + 50 {
		_, _ = tx.Exec(`INSERT INTO armory_batches (batch_id, applied_at) VALUES (?, ?)`, fmt.Sprint("x", i), baseMs)
	}
	_ = tx.Commit()
	if err := s.pruneIDs(ctx); err != nil {
		t.Fatal(err)
	}
	var n int
	_ = s.writer.QueryRow(`SELECT count(*) FROM armory_batches`).Scan(&n)
	if n != armoryBatchesKeptMax {
		t.Fatalf("batches kept = %d, want %d", n, armoryBatchesKeptMax)
	}
	snaps, _ := s.ArmorySnapshots(ctx, "Testrealm", "Aelwyn")
	if len(snaps) != 1 || snaps[0].Day != domain.ArmoryDay(baseMs) {
		t.Fatalf("old snapshot must be pruned: %+v", snaps)
	}
	if c, err := s.GetArmoryCharacter(ctx, "Testrealm", "Aelwyn"); err != nil || c.FetchedAt != baseMs {
		t.Fatalf("current row untouched: %+v %v", c, err)
	}
}

func TestArmoryRunsKeepNewest(t *testing.T) {
	s, _ := tempStore(t, Options{})
	ctx := context.Background()
	for i := range armoryRunsKeptMax + 5 {
		r := domain.ArmoryRun{ID: fmt.Sprint("run-", i), StartedAt: baseMs + int64(i), Status: domain.ArmoryRunOK}
		if err := s.RecordArmoryRun(ctx, r); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.pruneIDs(ctx); err != nil {
		t.Fatal(err)
	}
	var n int
	_ = s.writer.QueryRow(`SELECT count(*) FROM armory_runs`).Scan(&n)
	if n != armoryRunsKeptMax {
		t.Fatalf("runs = %d", n)
	}
	if last, _ := s.LastArmoryRun(ctx); last.ID != fmt.Sprint("run-", armoryRunsKeptMax+4) {
		t.Fatalf("last = %+v", last)
	}
}

func TestArmoryReadPathIsReadOnly(t *testing.T) {
	s, _ := tempStore(t, Options{})
	for _, q := range []string{`DELETE FROM armory_characters`, `INSERT INTO armory_runs (id) VALUES ('x')`, `DROP TABLE armory_snapshots`} {
		if _, err := s.readers.Exec(q); err == nil {
			t.Fatalf("reader accepted %s", q)
		}
	}
}

func TestArmoryReadsNeverTouchTheWriterPool(t *testing.T) {
	src, err := os.ReadFile("armory_read.go")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(src), "s.writer") {
		t.Error("armory_read.go uses the writer pool")
	}
}

func TestArmorySQLiCorpus(t *testing.T) {
	s, _ := tempStore(t, Options{})
	ctx := context.Background()
	if _, err := s.ApplyArmoryBatch(ctx, armTestBatch("seed", armTestChar("Aelwyn", baseMs), armTestChar("Borin", baseMs))); err != nil {
		t.Fatal(err)
	}
	count := func(table string) int {
		var n int
		if err := s.writer.QueryRow(`SELECT count(*) FROM ` + table).Scan(&n); err != nil {
			t.Fatalf("%s damaged: %v", table, err)
		}
		return n
	}
	before := count("armory_characters")
	for i, p := range injectionPayloads {
		if c, err := s.GetArmoryCharacter(ctx, p, p); err == nil {
			t.Errorf("payload %q matched %+v", p, c)
		}
		if got, err := s.ListArmoryCharacters(ctx, p, []string{p, "Aelwyn"}); err != nil || len(got) != 0 {
			t.Errorf("list %q: %v %v", p, got, err)
		}
		if got, err := s.ListArmoryCharacters(ctx, "Testrealm", []string{p}); err != nil || len(got) != 0 {
			t.Errorf("list names %q: %v %v", p, got, err)
		}
		if _, err := s.ArmorySnapshots(ctx, p, p); err != nil {
			t.Errorf("snapshots %q: %v", p, err)
		}
		// Hostile text that passes domain validation is stored literally, never executed.
		c := armTestChar("Aelwyn", baseMs+int64(i)+1)
		c.Guild = p
		if c.Validate() == nil {
			if _, err := s.ApplyArmoryBatch(ctx, armTestBatch(fmt.Sprint("inj-", i), c)); err != nil {
				t.Errorf("apply guild %q: %v", p, err)
			} else if got, err := s.GetArmoryCharacter(ctx, "Testrealm", "Aelwyn"); err != nil || got.Guild != p {
				t.Errorf("guild %q stored as %q (%v)", p, got.Guild, err)
			}
		}
		if err := s.RecordArmoryRun(ctx, domain.ArmoryRun{ID: "run-1", StartedAt: baseMs, Status: domain.ArmoryRunOK, Cursor: p}); err != nil && !errors.Is(err, domain.ErrInvalid) {
			t.Fatal(err)
		}
	}
	if got := count("armory_characters"); got != before {
		t.Fatalf("characters %d -> %d", before, got)
	}
	for _, tb := range []string{"contacts", "armory_snapshots", "armory_batches", "armory_runs", "meta"} {
		count(tb)
	}
}

var (
	_ core.ArmorySeedStore = (*Store)(nil)
	_ core.ArmorySeedStore = (*lazyStore)(nil)
)

func TestArmorySeedsSQLiCorpus(t *testing.T) {
	s, _ := tempStore(t, Options{})
	ctx := context.Background()
	if _, err := s.ApplyArmoryBatch(ctx, armTestBatch("seedinj", armTestChar("Ann", 1_900_000_000_000))); err != nil {
		t.Fatal(err)
	}
	var before int
	if err := s.writer.QueryRow(`SELECT count(*) FROM armory_characters`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	for _, p := range injectionPayloads {
		for _, c := range []string{p, "c|" + p + "|x", "c|ann|" + p, "g|" + p + "|" + p, "g|testrealm|" + p} {
			page, err := s.ArmorySeeds(ctx, c, 10)
			if err != nil && !errors.Is(err, domain.ErrInvalid) {
				t.Errorf("cursor %q: unexpected error %v", c, err)
			}
			if err == nil && page.Items == nil {
				t.Errorf("cursor %q: nil items", c)
			}
		}
	}
	var after int
	if err := s.writer.QueryRow(`SELECT count(*) FROM armory_characters`).Scan(&after); err != nil || after != before {
		t.Fatalf("armory_characters changed: %d -> %d (%v)", before, after, err)
	}
}
