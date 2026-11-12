package armorysync

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"radarlive/internal/domain"
)

// ---- seed-driven crawl: guilds, default realm, cursor ----

func TestCursorEncoding(t *testing.T) {
	for _, tc := range []struct {
		base string
		n    int
		enc  string
	}{{"", 0, ""}, {"", 7, "#7"}, {"c|abc|realm", 0, "c|abc|realm#0"}, {"g|realm|My Guild", 12, "g|realm|My Guild#12"}, {"x#5", 0, "x#5#0"}} {
		if got := formatCursor(tc.base, tc.n); got != tc.enc {
			t.Errorf("format(%q,%d) = %q, want %q", tc.base, tc.n, got, tc.enc)
		}
		if base, n := parseCursor(tc.enc); base != tc.base || n != tc.n {
			t.Errorf("parse(%q) = %q,%d, want %q,%d", tc.enc, base, n, tc.base, tc.n)
		}
	}
	for _, legacy := range []string{"abc", "12", "a#b", "a#", "a#-3", "a#99999999999", "a# 3"} {
		if base, n := parseCursor(legacy); base != legacy || n != 0 {
			t.Errorf("parse(%q) = %q,%d, want it untouched", legacy, base, n)
		}
	}
	if len(formatCursor(strings.Repeat("c", domain.ArmoryCursorMax-8), 99)) > domain.ArmoryCursorMax {
		t.Error("a normal cursor must fit the domain limit")
	}
}

func TestGuildExpansionDedupesAndCrawlsMembers(t *testing.T) {
	r := newRig(0, 0)
	r.seeds.items = []domain.ArmorySeed{charSeed(1), charSeed(2), guildSeed("Testrealm", "Alpha"), guildSeed("Testrealm", "Beta"), guildSeed("Other realm", "Gamma")}
	r.src.rosters = map[string][]CharRef{
		"Testrealm/Alpha": {{"testrealm", nameOf(2)}, {"testrealm", nameOf(10)}, {"testrealm", nameOf(11)}, {"testrealm", nameOf(10)}},
		"Testrealm/Beta":  nil, // 404: no roster
	}
	r.src.rosters["Other realm/Gamma"] = []CharRef{{"other-realm", nameOf(20)}, {"Testrealm", nameOf(1)}}
	sum, err := r.syncer().Run(context.Background())
	if err != nil || sum.Status != StatusOK {
		t.Fatalf("%+v %v", sum, err)
	}
	// 1, 2 (seeds) + 10, 11 (Alpha; 2 and the second 10 are duplicates) + 20 (Gamma; 1 is a duplicate)
	if r.src.detail != 5 || len(r.sink.chars) != 5 || sum.Fetched != 5 || sum.Enumerated != 5 {
		t.Fatalf("details=%d stored=%d %+v", r.src.detail, len(r.sink.chars), sum)
	}
	if strings.Join(r.src.rosterOf, ",") != "Testrealm/Alpha,Testrealm/Beta,Other realm/Gamma" {
		t.Fatalf("roster calls %v", r.src.rosterOf)
	}
	for _, key := range []string{"Testrealm/" + nameOf(1), "testrealm/" + nameOf(10), "other-realm/" + nameOf(20)} {
		if _, ok := r.sink.chars[key]; !ok {
			t.Errorf("%s not stored: %v", key, r.sink.chars)
		}
	}
}

// The same character appearing as "Area 52", "area-52" and "AREA52" is one character in a run.
func TestDedupeIgnoresRealmSpellingAndCase(t *testing.T) {
	r := newRig(0, 0)
	r.seeds.items = []domain.ArmorySeed{
		{Kind: domain.ArmorySeedCharacter, Realm: "Area 52", Name: "Alice"},
		{Kind: domain.ArmorySeedCharacter, Realm: "area-52", Name: "ALICE"},
		{Kind: domain.ArmorySeedCharacter, Realm: "AREA52", Name: "alice"},
		{Kind: domain.ArmorySeedCharacter, Realm: "Area 52", Name: "Bob"},
	}
	if sum, err := r.syncer().Run(context.Background()); err != nil || sum.Fetched != 2 || r.src.detail != 2 {
		t.Fatalf("%+v %v detail=%d", sum, err, r.src.detail)
	}
}

func TestGuildMemberCap(t *testing.T) {
	r := newRig(0, 0)
	r.cfg.MaxGuildMembers = 5
	r.seeds.items = []domain.ArmorySeed{guildSeed("Testrealm", "Big")}
	var members []CharRef
	for i := 0; i < 50; i++ {
		members = append(members, CharRef{"testrealm", nameOf(i)})
	}
	r.src.rosters = map[string][]CharRef{"Testrealm/Big": members}
	sum, _ := r.syncer().Run(context.Background())
	if sum.Status != StatusOK || r.src.detail != 5 || sum.Fetched != 5 {
		t.Fatalf("%+v detail=%d", sum, r.src.detail)
	}
}

func TestDefaultRealm(t *testing.T) {
	r := newRig(0, 0)
	r.cfg.DefaultRealm = "area-52"
	r.seeds.items = []domain.ArmorySeed{
		{Kind: domain.ArmorySeedCharacter, Name: "Alice"},
		{Kind: domain.ArmorySeedCharacter, Realm: "Testrealm", Name: "Bob"},
		{Kind: domain.ArmorySeedGuild, Guild: "Nameless"},
	}
	r.src.rosters = map[string][]CharRef{"area-52/Nameless": {{"area-52", "Carol"}}}
	sum, _ := r.syncer().Run(context.Background())
	if sum.Status != StatusOK || sum.Fetched != 3 || sum.Skipped != 0 {
		t.Fatalf("%+v", sum)
	}
	for _, key := range []string{"area-52/Alice", "Testrealm/Bob", "area-52/Carol"} {
		if _, ok := r.sink.chars[key]; !ok {
			t.Errorf("%s missing: %v", key, r.sink.chars)
		}
	}
}

func TestSeedsWithoutRealmAreSkippedAndCounted(t *testing.T) {
	r := newRig(0, 0) // no DefaultRealm
	r.seeds.items = []domain.ArmorySeed{
		{Kind: domain.ArmorySeedCharacter, Name: "Alice"},
		{Kind: domain.ArmorySeedGuild, Guild: "Nameless"},
		charSeed(3),
	}
	sum, err := r.syncer().Run(context.Background())
	if err != nil || sum.Status != StatusOK || sum.Skipped != 2 || sum.Fetched != 1 || r.src.detail != 1 || len(r.src.rosterOf) != 0 {
		t.Fatalf("%+v %v detail=%d rosters=%v", sum, err, r.src.detail, r.src.rosterOf)
	}
}

func TestUnknownSeedKindsAreSkipped(t *testing.T) {
	r := newRig(0, 0)
	r.seeds.items = []domain.ArmorySeed{{Kind: "dragon", Realm: "Testrealm", Name: "X"}, charSeed(1)}
	sum, err := r.syncer().Run(context.Background())
	if err != nil || sum.Status != StatusOK || sum.Skipped != 1 || sum.Fetched != 1 {
		t.Fatalf("%+v %v", sum, err)
	}
}

func TestRosterFailureIsCountedAndTheCrawlContinues(t *testing.T) {
	r := newRig(0, 0)
	r.seeds.items = []domain.ArmorySeed{guildSeed("Testrealm", "Broken"), guildSeed("Testrealm", "Fine"), charSeed(1)}
	r.src.rosterEr = map[string]error{"Testrealm/Broken": errors.New("boom")}
	r.src.rosters = map[string][]CharRef{"Testrealm/Fine": {{"testrealm", nameOf(7)}}}
	sum, err := r.syncer().Run(context.Background())
	if err != nil || sum.Status != StatusPartial || sum.Failed != 1 || sum.Fetched != 2 || len(r.sink.chars) != 2 {
		t.Fatalf("%+v %v", sum, err)
	}
}

func TestRosterAuthErrorIsFatal(t *testing.T) {
	r := newRig(0, 0)
	r.seeds.items = []domain.ArmorySeed{guildSeed("Testrealm", "G")}
	r.src.rosterEr = map[string]error{"Testrealm/G": errAuthFake}
	sum, err := r.syncer().Run(context.Background())
	if !errors.Is(err, ErrFatal) || sum.Status != StatusFailed || sum.ExitCode() != 2 {
		t.Fatalf("%+v %v", sum, err)
	}
}

func TestRosterCancelIsInterrupted(t *testing.T) {
	r := newRig(0, 0)
	r.seeds.items = []domain.ArmorySeed{guildSeed("Testrealm", "G")}
	r.src.rosterEr = map[string]error{"Testrealm/G": context.Canceled}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if sum, _ := r.syncer().Run(ctx); sum.Status != StatusInterrupted {
		t.Fatalf("%+v", sum)
	}
}

func TestRosterCountsAgainstTheRequestBudget(t *testing.T) {
	r := newRig(0, 0)
	r.cfg.MaxRequests = 3
	r.seeds.items = []domain.ArmorySeed{guildSeed("Testrealm", "A"), guildSeed("Testrealm", "B"), guildSeed("Testrealm", "C"), guildSeed("Testrealm", "D")}
	sum, _ := r.syncer().Run(context.Background())
	if sum.Status != StatusPartial || len(r.src.rosterOf) != 3 || sum.Note == "" {
		t.Fatalf("%+v rosters=%v", sum, r.src.rosterOf)
	}
}

func TestSeedPageSizeIsPassedAndPagesAreWalked(t *testing.T) {
	r := newRig(25, 10)
	r.cfg.SeedPageSize = 10
	sum, _ := r.syncer().Run(context.Background())
	if sum.Status != StatusOK || sum.Fetched != 25 || fmt.Sprint(r.seeds.cursors) != "[ 10 20]" {
		t.Fatalf("%+v %v", sum, r.seeds.cursors)
	}
	for _, l := range r.seeds.limits {
		if l != 10 {
			t.Fatalf("limits %v", r.seeds.limits)
		}
	}
	if (Config{}).withDefaults().SeedPageSize != domain.ArmorySeedLimitDefault || (Config{}).withDefaults().MaxGuildMembers != 1000 {
		t.Fatalf("defaults %+v", (Config{}).withDefaults())
	}
	if (Config{SeedPageSize: 5000}).withDefaults().SeedPageSize != domain.ArmorySeedLimitMax {
		t.Fatal("page size must be capped at the server maximum")
	}
}

// Opaque server cursors, even ones containing '#', round-trip through the checkpoint.
func TestOpaqueSeedCursorsRoundTrip(t *testing.T) {
	r := newRig(30, 10)
	r.seeds.prefix = "c|x#y|"
	sum, _ := r.syncer().Run(context.Background())
	if sum.Status != StatusOK || sum.Fetched != 30 || r.seeds.cursors[1] != "c|x#y|10" {
		t.Fatalf("%+v %v", sum, r.seeds.cursors)
	}
}

// A run killed while it expands the second guild resumes at that guild: the finished guild and the
// finished characters are not fetched again.
func TestResumeMidPageAcrossGuilds(t *testing.T) {
	r := newRig(0, 0)
	r.cfg.Concurrency = 1
	r.seeds.items = []domain.ArmorySeed{charSeed(1), charSeed(2), guildSeed("Testrealm", "A"), guildSeed("Testrealm", "B"), guildSeed("Testrealm", "C")}
	r.src.rosters = map[string][]CharRef{
		"Testrealm/A": {{"testrealm", nameOf(10)}, {"testrealm", nameOf(11)}},
		"Testrealm/B": {{"testrealm", nameOf(20)}, {"testrealm", nameOf(21)}},
		"Testrealm/C": {{"testrealm", nameOf(30)}},
	}
	ctx, cancel := context.WithCancel(context.Background())
	r.src.onDetail = func(_ int, name string) error {
		if name == nameOf(20) {
			cancel()
		}
		return nil
	}
	sum, err := r.syncer().Run(ctx)
	if err != nil || sum.Status != StatusInterrupted {
		t.Fatalf("%+v %v", sum, err)
	}
	if base, n := parseCursor(r.sink.last.Cursor); base != "" || n != 3 {
		t.Fatalf("checkpoint %q: want the start of guild B (item 3)", r.sink.last.Cursor)
	}
	r.clk.Advance(time.Hour)
	src2 := &fakeSource{rosters: r.src.rosters}
	sum2, err := New(src2, r.seeds, r.sink, r.clk, r.cfg).Run(context.Background())
	if err != nil || sum2.Status != StatusOK || sum2.RunID != sum.RunID {
		t.Fatalf("%+v %v", sum2, err)
	}
	if strings.Join(src2.rosterOf, ",") != "Testrealm/B,Testrealm/C" || src2.detail != 3 {
		t.Fatalf("resume redid finished work: rosters=%v details=%d", src2.rosterOf, src2.detail)
	}
	if len(r.sink.chars) != 7 {
		t.Fatalf("stored %d, want 7", len(r.sink.chars))
	}
}

// Batch ids are unique per unit and stable across identical runs.
func TestBatchIDsPerUnitAreStable(t *testing.T) {
	run := func() map[string]bool {
		r := newRig(0, 0)
		r.seeds.items = []domain.ArmorySeed{charSeed(1), guildSeed("Testrealm", "A"), guildSeed("Testrealm", "B")}
		r.src.rosters = map[string][]CharRef{"Testrealm/A": {{"testrealm", nameOf(10)}}, "Testrealm/B": {{"testrealm", nameOf(20)}}}
		if _, err := r.syncer().Run(context.Background()); err != nil {
			t.Fatal(err)
		}
		ids := map[string]bool{}
		for _, b := range r.sink.batches {
			ids[b.BatchID] = true
		}
		if len(ids) != 3 {
			t.Fatalf("ids %v", ids)
		}
		return ids
	}
	a, b := run(), run()
	for id := range a {
		if !b[id] {
			t.Fatalf("batch ids differ between identical runs: %v vs %v", a, b)
		}
	}
}

func TestSeedFailureMidCrawlKeepsCheckpoint(t *testing.T) {
	r := newRig(30, 10)
	r.cfg.Attempts = 1
	r.seeds.onPage = func(n int) error {
		if n == 2 {
			return errors.New("radar-db is down")
		}
		return nil
	}
	sum, err := r.syncer().Run(context.Background())
	if err != nil || sum.Status != StatusPartial || sum.Fetched != 10 || !strings.Contains(sum.Note, "seeds") {
		t.Fatalf("%+v %v", sum, err)
	}
	if base, n := parseCursor(r.sink.last.Cursor); base != "10" || n != 0 {
		t.Fatalf("checkpoint %q", r.sink.last.Cursor)
	}
}

func TestSeedRetryBacksOff(t *testing.T) {
	r := newRig(5, 5)
	r.cfg.Backoff = time.Second
	r.seeds.onPage = func(n int) error {
		if n <= 2 {
			return errors.New("flaky")
		}
		return nil
	}
	sum, _ := r.syncer().Run(context.Background())
	if sum.Status != StatusOK || len(r.slept) != 2 || r.slept[1] <= r.slept[0] {
		t.Fatalf("%+v slept=%v", sum, r.slept)
	}
}

// A feed that never ends (or never advances) cannot keep the run alive past the wall-clock budget, even
// though seed pages do not use the request budget.
func TestEndlessSeedFeedStopsAtTheWallClock(t *testing.T) {
	r := newRig(0, 0)
	r.cfg.MaxDuration = time.Hour
	endless := &endlessSeeds{clk: r.clk}
	sum, err := New(r.src, endless, r.sink, r.clk, r.cfg).Run(context.Background())
	if err != nil || sum.Status != StatusPartial || sum.Note == "" || endless.calls > 100 {
		t.Fatalf("%+v %v calls=%d", sum, err, endless.calls)
	}
}

type endlessSeeds struct {
	clk   *fakeClock
	calls int
}

func (e *endlessSeeds) Seeds(context.Context, string, int) (domain.ArmorySeedPage, error) {
	e.calls++
	e.clk.Advance(time.Minute)
	return domain.ArmorySeedPage{Next: fmt.Sprint("next", e.calls)}, nil
}

var errInvalidFake = errors.New("fake: invalid name")

func TestInvalidSeedsAreSkippedNotFailed(t *testing.T) {
	r := newRig(0, 0)
	r.cfg.InvalidErr = errInvalidFake
	r.seeds.items = []domain.ArmorySeed{guildSeed("Testrealm", "a/b?c=d"), charSeed(1), charSeed(2)}
	r.src.rosterEr = map[string]error{"Testrealm/a/b?c=d": fmt.Errorf("wrapped: %w", errInvalidFake)}
	r.src.onDetail = func(_ int, name string) error {
		if name == nameOf(2) {
			return errInvalidFake
		}
		return nil
	}
	sum, err := r.syncer().Run(context.Background())
	if err != nil || sum.Status != StatusOK || sum.Skipped != 2 || sum.Failed != 0 || sum.Fetched != 1 {
		t.Fatalf("%+v %v", sum, err)
	}
}

func TestRealFetchFailuresStillCountAsFailedWithInvalidErrSet(t *testing.T) {
	r := newRig(0, 0)
	r.cfg.InvalidErr = errInvalidFake
	r.seeds.items = []domain.ArmorySeed{charSeed(1)}
	r.src.onDetail = func(int, string) error { return errors.New("boom") }
	sum, err := r.syncer().Run(context.Background())
	if err != nil || sum.Status != StatusPartial || sum.Failed != 1 || sum.Skipped != 0 {
		t.Fatalf("%+v %v", sum, err)
	}
}
