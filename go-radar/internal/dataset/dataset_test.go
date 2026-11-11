package dataset

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"nearbyradar/internal/bounty"
	"nearbyradar/internal/history"
	"nearbyradar/internal/ruleset"
)

var t0 = time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)

func newReg(t *testing.T, dir, initial string) (*Registry, *[]string) {
	t.Helper()
	var msgs []string
	var mu sync.Mutex
	r, err := New(Options{HistoryPath: filepath.Join(dir, "h.json"), Initial: initial,
		Logf: func(f string, a ...any) { mu.Lock(); msgs = append(msgs, fmt.Sprintf(f, a...)); mu.Unlock() }})
	if err != nil {
		t.Fatal(err)
	}
	return r, &msgs
}

func rec(r *Registry, guid string) {
	r.Record(history.Sample{GUID: guid, Name: guid, Time: t0})
}

func death(id, killer string) bounty.Death {
	return bounty.Death{ID: id, Time: t0, VictimGUID: "Player-1-V", VictimName: "V",
		Killer: &bounty.Participant{GUID: killer, Raw: killer}}
}

func TestDefaultsToPvP(t *testing.T) {
	r, _ := newReg(t, t.TempDir(), "")
	st := r.Status()
	if st.Active != ruleset.PvP || st.Mode != ModeAuto || st.Detected != nil || len(st.Rulesets) != 4 {
		t.Fatalf("%+v", st)
	}
	if r.Active() != ruleset.PvP || r.Mode() != ModeAuto {
		t.Fatal("accessors")
	}
	if b := r.Brief(); b.Active != ruleset.PvP || b.Mode != ModeAuto {
		t.Fatalf("%+v", b)
	}
}

func TestObserveMeSwitchesInAutoAndDataIsSeparate(t *testing.T) {
	dir := t.TempDir()
	r, _ := newReg(t, dir, "")
	rec(r, "Player-1-A")
	r.RecordDeath(death("d1", "Player-1-K"))
	r.ObserveMe("Me-Realm-RP-US")
	if r.Active() != ruleset.RP {
		t.Fatalf("active %s", r.Active())
	}
	if st := r.Status(); st.Detected == nil || *st.Detected != ruleset.RP {
		t.Fatalf("%+v", st)
	}
	if len(r.Snapshot()) != 0 || len(r.Leaderboard(5)) != 0 || len(r.Search("", 5)) != 0 {
		t.Fatal("rp dataset must start empty")
	}
	if _, ok := r.Track("Player-1-A", 0); ok {
		t.Fatal("track must not leak")
	}
	if _, ok := r.Player("Player-1-K"); ok {
		t.Fatal("bounty player must not leak")
	}
	rec(r, "Player-1-B")
	if s := r.Snapshot(); len(s) != 1 || s[0].GUID != "Player-1-B" {
		t.Fatalf("%+v", s)
	}
	if err := r.Flush(); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"h-pvp.json", "h-pvp-bounty.json", "h-rp.json"} {
		if !exists(filepath.Join(dir, f)) {
			t.Errorf("missing %s", f)
		}
	}
	if exists(filepath.Join(dir, "h-normal.json")) {
		t.Error("normal must not be created")
	}
	if _, err := r.Select("pvp"); err != nil {
		t.Fatal(err)
	}
	if s := r.Snapshot(); len(s) != 1 || s[0].GUID != "Player-1-A" || r.Stats().Deaths != 1 {
		t.Fatalf("%+v %+v", s, r.Stats())
	}
	if _, ok := r.Player("Player-1-K"); !ok {
		t.Fatal("bounty player visible under pvp")
	}
}

func TestObserveMeIgnoredWhenManualOrUnknown(t *testing.T) {
	r, _ := newReg(t, t.TempDir(), "")
	r.ObserveMe("Me-Realm-US")
	if r.Status().Detected != nil || r.Active() != ruleset.PvP {
		t.Fatal("unknown realm must not detect")
	}
	if _, err := r.Select("normal"); err != nil {
		t.Fatal(err)
	}
	r.ObserveMe("Me-Realm-RPPvP-US")
	if r.Active() != ruleset.Normal || r.Mode() != ModeManual {
		t.Fatalf("manual must win: %s %s", r.Active(), r.Mode())
	}
	if st := r.Status(); st.Detected == nil || *st.Detected != ruleset.RPPvP {
		t.Fatal("detection still reported")
	}
	if st, err := r.Select("AUTO"); err != nil || st.Active != ruleset.RPPvP || st.Mode != ModeAuto {
		t.Fatalf("%+v %v", st, err)
	}
	r.ObserveMe("Me-Realm-RPPvP-US")
	if r.Active() != ruleset.RPPvP {
		t.Fatal("still rppvp")
	}
}

func TestSelectErrors(t *testing.T) {
	r, _ := newReg(t, t.TempDir(), "")
	for _, bad := range []string{"", "pve", "x"} {
		if _, err := r.Select(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
	if r.Active() != ruleset.PvP || r.Mode() != ModeAuto {
		t.Fatal("state must not change on error")
	}
	st, err := r.Select("RP-PvP")
	if err != nil || st.Active != ruleset.RPPvP || st.Mode != ModeManual {
		t.Fatalf("%+v %v", st, err)
	}
}

func TestPersistenceAcrossRestart(t *testing.T) {
	dir := t.TempDir()
	r, _ := newReg(t, dir, "")
	if _, err := r.Select("rp"); err != nil {
		t.Fatal(err)
	}
	rec(r, "Player-1-A")
	if err := r.Flush(); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(dir, "radar-settings.json"))
	if err != nil {
		t.Fatal(err)
	}
	var s struct{ Mode, Ruleset string }
	if json.Unmarshal(b, &s) != nil || s.Mode != "manual" || s.Ruleset != "rp" {
		t.Fatalf("%s", b)
	}
	r2, _ := newReg(t, dir, "")
	if r2.Active() != ruleset.RP || r2.Mode() != ModeManual || len(r2.Snapshot()) != 1 {
		t.Fatalf("restart: %s %s", r2.Active(), r2.Mode())
	}
	if _, err := r2.Select("auto"); err != nil {
		t.Fatal(err)
	}
	r3, _ := newReg(t, dir, "")
	if r3.Mode() != ModeAuto || r3.Active() != ruleset.RP {
		t.Fatalf("auto fallback to last manual: %s %s", r3.Mode(), r3.Active())
	}
	r3.ObserveMe("Me-Realm-Normal-EU")
	if r3.Active() != ruleset.Normal {
		t.Fatal("detection beats fallback")
	}
}

func TestInitialFlag(t *testing.T) {
	dir := t.TempDir()
	r, _ := newReg(t, dir, "normal")
	if r.Active() != ruleset.Normal || r.Mode() != ModeManual {
		t.Fatalf("%s %s", r.Active(), r.Mode())
	}
	r2, _ := newReg(t, dir, "auto")
	if r2.Active() != ruleset.Normal {
		t.Fatalf("auto keeps the persisted setting: %s", r2.Active())
	}
	if _, err := New(Options{HistoryPath: filepath.Join(dir, "h.json"), Initial: "bogus"}); err == nil {
		t.Fatal("bad initial ruleset must be an error")
	}
}

func TestCorruptSettingsIgnored(t *testing.T) {
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "radar-settings.json"), []byte("{oops"), 0o600)
	r, msgs := newReg(t, dir, "")
	if r.Active() != ruleset.PvP || len(*msgs) == 0 {
		t.Fatalf("%s %v", r.Active(), *msgs)
	}
	_ = os.WriteFile(filepath.Join(dir, "radar-settings.json"), []byte(`{"mode":"manual","ruleset":"nope"}`), 0o600)
	r, _ = newReg(t, dir, "")
	if r.Active() != ruleset.PvP || r.Mode() != ModeAuto {
		t.Fatalf("invalid ruleset in settings: %s %s", r.Active(), r.Mode())
	}
}

func writeLegacy(t *testing.T, dir string) (hp string) {
	t.Helper()
	hp = filepath.Join(dir, "h.json")
	h := history.New(hp)
	h.Record(history.Sample{GUID: "Player-1-L", Name: "Legacy", Time: t0})
	if err := h.Save(); err != nil {
		t.Fatal(err)
	}
	b := bounty.New(bounty.PathFor(hp))
	b.RecordDeath(death("old", "Player-1-LK"))
	if err := b.Save(); err != nil {
		t.Fatal(err)
	}
	return hp
}

func TestLegacyMigrationCopiesIntoActive(t *testing.T) {
	dir := t.TempDir()
	hp := writeLegacy(t, dir)
	before, _ := os.ReadFile(hp)
	r, _ := newReg(t, dir, "")
	if len(r.Snapshot()) != 1 || r.Stats().Deaths != 1 || r.Active() != ruleset.PvP {
		t.Fatalf("legacy data must appear in the active (pvp) dataset")
	}
	after, err := os.ReadFile(hp)
	if err != nil || string(after) != string(before) {
		t.Fatal("legacy file must stay untouched")
	}
	if !exists(bounty.PathFor(hp)) || !exists(filepath.Join(dir, "h-pvp.json")) || !exists(filepath.Join(dir, "h-pvp-bounty.json")) {
		t.Fatal("copies missing")
	}
	if _, err := r.Select("rp"); err != nil {
		t.Fatal(err)
	}
	if len(r.Snapshot()) != 0 || r.Stats().Deaths != 0 {
		t.Fatal("rp must stay empty")
	}
	_ = os.Remove(filepath.Join(dir, "h-pvp.json"))
	_ = os.Remove(filepath.Join(dir, "h-pvp-bounty.json"))
	r2, _ := newReg(t, dir, "pvp")
	if len(r2.Snapshot()) != 0 {
		t.Fatal("marker must stop re-migration")
	}
}

func TestLegacyMigrationIntoChosenRuleset(t *testing.T) {
	dir := t.TempDir()
	writeLegacy(t, dir)
	r, _ := newReg(t, dir, "rppvp")
	if len(r.Snapshot()) != 1 || r.Active() != ruleset.RPPvP {
		t.Fatal("legacy data goes to the ruleset active at that moment")
	}
	if _, err := r.Select("pvp"); err != nil || len(r.Snapshot()) != 0 {
		t.Fatal("pvp stays empty")
	}
}

func TestNoMigrationWhenRulesetFilesExist(t *testing.T) {
	dir := t.TempDir()
	writeLegacy(t, dir)
	h := history.New(filepath.Join(dir, "h-rp.json"))
	h.Record(history.Sample{GUID: "Player-1-R", Time: t0})
	_ = h.Save()
	r, _ := newReg(t, dir, "")
	if len(r.Snapshot()) != 0 {
		t.Fatal("existing per-ruleset data means the user already migrated")
	}
}

func TestLegacyOnlyBountyFileMigrates(t *testing.T) {
	dir := t.TempDir()
	hp := filepath.Join(dir, "h.json")
	b := bounty.New(bounty.PathFor(hp))
	b.RecordDeath(death("x", "Player-1-K"))
	_ = b.Save()
	r, _ := newReg(t, dir, "")
	if r.Stats().Deaths != 1 {
		t.Fatal("bounty-only legacy")
	}
}

func TestCorruptLegacyIsLoggedNotFatal(t *testing.T) {
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "h.json"), []byte("{oops"), 0o600)
	r, msgs := newReg(t, dir, "")
	if r == nil || len(*msgs) == 0 {
		t.Fatalf("%v", *msgs)
	}
	rec(r, "Player-1-A")
	if len(r.Snapshot()) != 1 {
		t.Fatal("usable after corruption")
	}
}

func TestStatusCounts(t *testing.T) {
	dir := t.TempDir()
	r, _ := newReg(t, dir, "")
	rec(r, "Player-1-A")
	rec(r, "Player-1-B")
	r.RecordDeath(death("d", "Player-1-K"))
	_, _ = r.Select("rp")
	_ = r.Flush()
	st := r.Status()
	by := map[ruleset.ID]RulesetStatus{}
	for _, e := range st.Rulesets {
		by[e.ID] = e
	}
	if by[ruleset.PvP].Players != 2 || !by[ruleset.PvP].HasData || by[ruleset.PvP].Name != "PvP" {
		t.Fatalf("%+v", by[ruleset.PvP])
	}
	if by[ruleset.RP].HasData || by[ruleset.Normal].HasData || by[ruleset.Normal].Players != 0 {
		t.Fatalf("%+v", by)
	}
	r2, _ := newReg(t, dir, "normal")
	for _, e := range r2.Status().Rulesets {
		if e.ID == ruleset.PvP && (e.Players != 2 || !e.HasData) {
			t.Fatalf("files not yet open must still be counted: %+v", e)
		}
	}
	_, _ = r2.Select("rp")
	r2.RecordDeath(death("z", "Player-1-K"))
	for _, e := range r2.Status().Rulesets {
		if e.ID == ruleset.RP && !e.HasData {
			t.Fatal("bounty-only data")
		}
	}
}

func TestRunFlushesPeriodicallyAndOnShutdown(t *testing.T) {
	dir := t.TempDir()
	r, _ := newReg(t, dir, "")
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { r.Run(ctx, 20*time.Millisecond); close(done) }()
	rec(r, "Player-1-A")
	deadline := time.Now().Add(3 * time.Second)
	for !exists(filepath.Join(dir, "h-pvp.json")) {
		if time.Now().After(deadline) {
			t.Fatal("periodic flush never happened")
		}
		time.Sleep(10 * time.Millisecond)
	}
	_, _ = r.Select("rp")
	rec(r, "Player-1-B")
	r.RecordDeath(death("q", "Player-1-K"))
	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("Run did not stop")
	}
	h, err := history.Open(filepath.Join(dir, "h-rp.json"))
	if err != nil || h.Len() != 1 {
		t.Fatalf("final flush of the active set: %v", err)
	}
	b, _ := bounty.Open(filepath.Join(dir, "h-rp-bounty.json"))
	if b.Stats().Deaths != 1 {
		t.Fatal("bounty final flush")
	}
}

func TestInactiveSetStillFlushed(t *testing.T) {
	dir := t.TempDir()
	r, _ := newReg(t, dir, "")
	rec(r, "Player-1-A")
	_, _ = r.Select("rp")
	if err := r.Flush(); err != nil {
		t.Fatal(err)
	}
	h, _ := history.Open(filepath.Join(dir, "h-pvp.json"))
	if h.Len() != 1 {
		t.Fatal("inactive dirty set must be flushed")
	}
}

func TestMemoryOnly(t *testing.T) {
	r, err := New(Options{})
	if err != nil {
		t.Fatal(err)
	}
	rec(r, "Player-1-A")
	_, _ = r.Select("rp")
	if len(r.Snapshot()) != 0 {
		t.Fatal("separate even in memory")
	}
	_, _ = r.Select("pvp")
	if len(r.Snapshot()) != 1 || r.Flush() != nil {
		t.Fatal("memory dataset")
	}
}

func TestConcurrentSwitchAndWrites(t *testing.T) {
	dir := t.TempDir()
	r, _ := newReg(t, dir, "")
	var wg sync.WaitGroup
	stop := make(chan struct{})
	var mu sync.Mutex
	n := 0
	for w := 0; w < 4; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; ; i++ {
				select {
				case <-stop:
					return
				default:
				}
				g := fmt.Sprintf("Player-1-%d-%d", w, i)
				rec(r, g)
				mu.Lock()
				n++
				mu.Unlock()
				r.RecordDeath(death(g, g))
				_ = r.Snapshot()
				_ = r.Leaderboard(3)
				_ = r.Status()
			}
		}(w)
	}
	ids := []string{"pvp", "rp", "normal", "rppvp", "auto"}
	for i := 0; i < 60; i++ {
		if _, err := r.Select(ids[i%len(ids)]); err != nil {
			t.Error(err)
		}
		r.ObserveMe("Me-Realm-RP-US")
		time.Sleep(time.Millisecond)
	}
	close(stop)
	wg.Wait()
	if err := r.Flush(); err != nil {
		t.Fatal(err)
	}
	total := 0
	for _, id := range ruleset.All() {
		h, _ := history.Open(filepath.Join(dir, "h-"+string(id.ID)+".json"))
		total += h.Len()
	}
	if total != n {
		t.Fatalf("data loss: wrote %d, persisted %d", n, total)
	}
}

func TestObserveSuffixIsWeakerThanMe(t *testing.T) {
	r, _ := newReg(t, t.TempDir(), "")
	r.ObserveSuffix("") // nothing
	r.ObserveSuffix("-Stormrage-US")
	if r.Active() != ruleset.PvP || r.Brief().Detected != nil {
		t.Fatal("unknown realm token must not detect")
	}
	r.ObserveSuffix("-RP-EU")
	if r.Active() != ruleset.RP || r.Brief().Detected == nil || *r.Brief().Detected != ruleset.RP {
		t.Fatalf("hint should be used when me is unknown: %s", r.Active())
	}
	r.ObserveSuffix("-RP-EU") // repeat is a no-op
	r.ObserveMe("Me-Realm-Normal-US")
	if r.Active() != ruleset.Normal {
		t.Fatal("me beats the realm hint")
	}
	r.ObserveSuffix("-RP-EU")
	if r.Active() != ruleset.Normal {
		t.Fatal("a later hint must not override me")
	}
	if _, err := r.Select("pvp"); err != nil || r.Active() != ruleset.PvP {
		t.Fatal("manual still wins")
	}
}
