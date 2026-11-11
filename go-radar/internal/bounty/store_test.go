package bounty

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var t0 = time.Date(2026, 11, 10, 16, 0, 0, 0, time.UTC)

func foe(n int) Participant {
	return Participant{GUID: fmt.Sprintf("Player-1-E%03d", n), Raw: fmt.Sprintf("Foe%d-Realm-PvP-US", n), Display: fmt.Sprintf("Foe%d", n)}
}

func death(id string, at time.Time, victim string, killer *Participant, assists ...Participant) Death {
	return Death{ID: id, Time: at, VictimGUID: "Player-1-" + victim, VictimName: victim, Killer: killer, Assists: assists,
		Zone: "Dun Morogh", MapID: 1426, HasPos: true, X: 10, Y: 20}
}

func readFile(p string) string { b, _ := os.ReadFile(p); return string(b) }

func exists(p string) bool { _, err := os.Stat(p); return err == nil }

func TestValueFormula(t *testing.T) {
	// The bounty only counts kills of friendly players (killing blows); assists carry no value.
	if PointsPerKillingBlow != 10 || PointsPerAssist != 0 {
		t.Fatal("documented constants changed")
	}
	if Value(2, 3) != 20 || Value(0, 5) != 0 || Value(0, 0) != 0 {
		t.Fatalf("%d", Value(2, 3))
	}
}

func TestRecordDeathCreditsAndIdempotency(t *testing.T) {
	s := New("")
	k, a := foe(1), foe(2)
	if !s.RecordDeath(death("d1", t0, "V1", &k, a)) {
		t.Fatal("first record must be new")
	}
	if s.RecordDeath(death("d1", t0, "V1", &k, a)) {
		t.Fatal("same id must not count twice")
	}
	rows := s.Leaderboard(10)
	if len(rows) != 2 || rows[0].GUID != k.GUID || rows[0].KillingBlows != 1 || rows[0].Bounty != 10 || rows[0].Rank != 1 {
		t.Fatalf("%+v", rows)
	}
	if rows[1].GUID != a.GUID || rows[1].Assists != 1 || rows[1].Bounty != 0 || rows[1].Rank != 2 {
		t.Fatalf("%+v", rows[1])
	}
	if rows[0].Name != "Foe1" || rows[0].Raw != "Foe1-Realm-PvP-US" || rows[0].LastZone != "Dun Morogh" || rows[0].LastMap != 1426 || !rows[0].HasPos {
		t.Fatalf("%+v", rows[0])
	}
	st := s.Stats()
	if st.Deaths != 1 || st.Players != 2 {
		t.Fatalf("%+v", st)
	}
}

func TestInvolvedDeathsCountsDistinctVictims(t *testing.T) {
	s := New("")
	k, a := foe(1), foe(2)
	s.RecordDeath(death("d1", t0, "V1", &k))
	s.RecordDeath(death("d2", t0.Add(time.Minute), "V1", &a, k)) // same victim again, k assists
	s.RecordDeath(death("d3", t0.Add(2*time.Minute), "V2", &k))
	d, ok := s.Player(k.GUID)
	if !ok || d.KillingBlows != 2 || d.Assists != 1 || d.InvolvedDeaths != 2 {
		t.Fatalf("%+v", d)
	}
}

func TestLeaderboardOrderAndLimit(t *testing.T) {
	s := New("")
	a, b, c, d := foe(1), foe(2), foe(3), foe(4)
	s.RecordDeath(death("1", t0, "V1", &a))
	s.RecordDeath(death("2", t0.Add(time.Second), "V2", &b))
	s.RecordDeath(death("3", t0.Add(2*time.Second), "V3", &b))
	s.RecordDeath(death("4", t0.Add(3*time.Second), "V4", &c, a, d))
	s.RecordDeath(death("5", t0.Add(4*time.Second), "V5", &d, a))
	// b 2KB; a 1KB 2A; d 1KB 1A; c 1KB 0A
	rows := s.Leaderboard(10)
	order := []string{b.GUID, a.GUID, d.GUID, c.GUID}
	for i, g := range order {
		if rows[i].GUID != g || rows[i].Rank != i+1 {
			t.Fatalf("pos %d: want %s got %+v", i, g, rows)
		}
	}
	if len(s.Leaderboard(2)) != 2 || len(s.Leaderboard(0)) != 4 || len(s.Leaderboard(-5)) != 4 {
		t.Fatal("limit handling")
	}
	// tie on kills and assists: most recent kill first
	s2 := New("")
	s2.RecordDeath(death("1", t0, "V1", &a))
	s2.RecordDeath(death("2", t0.Add(time.Hour), "V2", &b))
	r := s2.Leaderboard(5)
	if r[0].GUID != b.GUID {
		t.Fatalf("most recent kill breaks ties: %+v", r)
	}
}

func TestNPCDeathCountedSeparately(t *testing.T) {
	s := New("")
	if !s.RecordDeath(death("n1", t0, "V1", nil)) {
		t.Fatal("new")
	}
	if s.RecordDeath(death("n1", t0, "V1", nil)) {
		t.Fatal("dup")
	}
	st := s.Stats()
	if st.NPCDeaths != 1 || st.Players != 0 || st.Deaths != 1 || len(s.Leaderboard(10)) != 0 {
		t.Fatalf("%+v", st)
	}
}

func TestRecentRecordsCapAndDetail(t *testing.T) {
	s := New("")
	k := foe(1)
	for i := 0; i < MaxRecentPerPlayer+25; i++ {
		s.RecordDeath(death(fmt.Sprintf("d%d", i), t0.Add(time.Duration(i)*time.Second), fmt.Sprintf("V%d", i), &k))
	}
	d, ok := s.Player(k.GUID)
	if !ok || len(d.Recent) != MaxRecentPerPlayer || d.KillingBlows != MaxRecentPerPlayer+25 {
		t.Fatalf("recent=%d kb=%d", len(d.Recent), d.KillingBlows)
	}
	// newest first, role killer, oldest dropped
	if d.Recent[0].Victim != fmt.Sprintf("V%d", MaxRecentPerPlayer+24) || d.Recent[0].Role != RoleKiller || d.Recent[0].Map != 1426 {
		t.Fatalf("%+v", d.Recent[0])
	}
	if d.Recent[len(d.Recent)-1].Victim != "V25" {
		t.Fatalf("oldest kept: %+v", d.Recent[len(d.Recent)-1])
	}
	if _, ok := s.Player("nope"); ok {
		t.Fatal("unknown guid")
	}
	a := foe(2)
	s.RecordDeath(death("x", t0.Add(time.Hour), "VX", &k, a))
	da, _ := s.Player(a.GUID)
	if len(da.Recent) != 1 || da.Recent[0].Role != RoleAssist {
		t.Fatalf("%+v", da.Recent)
	}
}

func TestFirstLastKillAndLatestPosition(t *testing.T) {
	s := New("")
	k := foe(1)
	late := death("late", t0.Add(time.Hour), "V2", &k)
	late.Zone, late.MapID, late.X, late.Y = "Elwynn", 1429, 5, 6
	s.RecordDeath(late)
	s.RecordDeath(death("early", t0, "V1", &k)) // processed afterwards but older: must not move the position back
	r := s.Leaderboard(1)[0]
	if r.FirstKill != t0.UnixMilli() || r.LastKill != t0.Add(time.Hour).UnixMilli() || r.LastZone != "Elwynn" || r.LastMap != 1429 || r.LastX != 5 {
		t.Fatalf("%+v", r)
	}
}

func TestNameUpdatesToLatestAndFallsBackToRaw(t *testing.T) {
	s := New("")
	k := foe(1)
	s.RecordDeath(death("1", t0, "V1", &k))
	k2 := Participant{GUID: k.GUID, Raw: "Renamed-Realm-PvP-US", Display: "Renamed"}
	s.RecordDeath(death("2", t0.Add(time.Second), "V2", &k2))
	if r := s.Leaderboard(1)[0]; r.Name != "Renamed" || r.Raw != "Renamed-Realm-PvP-US" {
		t.Fatalf("%+v", r)
	}
	k3 := Participant{GUID: k.GUID, Raw: "Third-Realm-PvP-US"} // no display: falls back to raw
	s.RecordDeath(death("3", t0.Add(2*time.Second), "V3", &k3))
	if r := s.Leaderboard(1)[0]; r.Name != "Third-Realm-PvP-US" {
		t.Fatalf("%+v", r)
	}
}

func TestPersistenceRoundTripAndDirtyOnly(t *testing.T) {
	p := filepath.Join(t.TempDir(), "sub", "b.json")
	s, err := Open(p)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Flush(); err != nil || exists(p) {
		t.Fatalf("clean store must not write: %v", err)
	}
	k, a := foe(1), foe(2)
	s.RecordDeath(death("d1", t0, "V1", &k, a))
	s.RecordDeath(death("n1", t0.Add(time.Second), "V2", nil))
	if err := s.Flush(); err != nil || !exists(p) {
		t.Fatalf("flush: %v", err)
	}
	if exists(p + ".tmp") {
		t.Fatal("temp file left behind")
	}
	fi1, _ := os.Stat(p)
	time.Sleep(15 * time.Millisecond)
	if err := s.Flush(); err != nil { // not dirty: untouched
		t.Fatal(err)
	}
	fi2, _ := os.Stat(p)
	if !fi2.ModTime().Equal(fi1.ModTime()) {
		t.Fatal("Flush rewrote an unchanged store")
	}

	s2, err := Open(p)
	if err != nil {
		t.Fatal(err)
	}
	if s2.RecordDeath(death("d1", t0, "V1", &k, a)) || s2.RecordDeath(death("n1", t0, "V2", nil)) {
		t.Fatal("ids must survive a restart (idempotent re-analysis)")
	}
	r := s2.Leaderboard(10)
	if len(r) != 2 || r[0].KillingBlows != 1 || r[1].Assists != 1 || s2.Stats().NPCDeaths != 1 {
		t.Fatalf("%+v %+v", r, s2.Stats())
	}
	d, _ := s2.Player(k.GUID)
	if len(d.Recent) != 1 || d.Recent[0].Victim != "V1" {
		t.Fatalf("%+v", d)
	}
	// the distinct-victim set survives too
	s2.RecordDeath(death("d9", t0.Add(time.Minute), "V1", &k))
	if d, _ := s2.Player(k.GUID); d.InvolvedDeaths != 1 || d.KillingBlows != 2 {
		t.Fatalf("%+v", d)
	}
}

func TestOpenMissingCorruptAndFutureVersion(t *testing.T) {
	dir := t.TempDir()
	if s, err := Open(filepath.Join(dir, "none.json")); err != nil || s == nil {
		t.Fatal(err)
	}
	bad := filepath.Join(dir, "bad.json")
	_ = os.WriteFile(bad, []byte("{nope"), 0o600)
	s, err := Open(bad)
	if err == nil || s == nil || !exists(bad+".corrupt") || exists(bad) {
		t.Fatalf("corrupt handling: %v", err)
	}
	// still usable after the corrupt file was moved away
	s.RecordDeath(death("x", t0, "V", &Participant{GUID: "Player-1-X", Raw: "X"}))
	if err := s.Save(); err != nil {
		t.Fatal(err)
	}
	// a file written by a newer build still loads what it can
	fut := filepath.Join(dir, "fut.json")
	_ = os.WriteFile(fut, []byte(`{"version":99,"players":{"Player-9-Z":{"name":"Z","killingBlows":4}},"seen":["a"],"future":true}`), 0o600)
	s3, err := Open(fut)
	if err != nil {
		t.Fatal(err)
	}
	if r := s3.Leaderboard(5); len(r) != 1 || r[0].KillingBlows != 4 || r[0].GUID != "Player-9-Z" {
		t.Fatalf("%+v", r)
	}
}

func TestPathFor(t *testing.T) {
	cases := map[string]string{
		filepath.Join("a", "radar-history.json"): filepath.Join("a", "radar-history-bounty.json"),
		filepath.Join("a", "h.json"):             filepath.Join("a", "h-bounty.json"),
		filepath.Join("a", "noext"):              filepath.Join("a", "noext-bounty.json"),
	}
	for in, want := range cases {
		if got := PathFor(in); got != want {
			t.Errorf("%q -> %q want %q", in, got, want)
		}
	}
	if PathFor("") != "" {
		t.Fatal("memory-only history gives memory-only bounty store")
	}
}

func TestPlayerCapEvictsWeakest(t *testing.T) {
	old := maxPlayers
	maxPlayers = 10
	defer func() { maxPlayers = old }()
	s := New("")
	star := foe(0)
	for i := 0; i < 3; i++ {
		s.RecordDeath(death(fmt.Sprintf("s%d", i), t0.Add(time.Duration(i)*time.Second), fmt.Sprintf("S%d", i), &star))
	}
	for i := 1; i <= 30; i++ {
		f := foe(i)
		s.RecordDeath(death(fmt.Sprintf("a%d", i), t0.Add(time.Duration(i)*time.Minute), fmt.Sprintf("A%d", i), nil, f))
	}
	if st := s.Stats(); st.Players > 10 {
		t.Fatalf("players %d > cap", st.Players)
	}
	if _, ok := s.Player(star.GUID); !ok {
		t.Fatal("top player must survive eviction")
	}
}

func TestSeenIDCapForgetsOldest(t *testing.T) {
	old := maxSeen
	maxSeen = 5
	defer func() { maxSeen = old }()
	s := New("")
	for i := 0; i < 8; i++ {
		s.RecordDeath(death(fmt.Sprintf("id%d", i), t0, "V", nil))
	}
	if n := s.Stats().SeenIDs; n != 5 {
		t.Fatalf("seen %d", n)
	}
	if s.RecordDeath(death("id7", t0, "V", nil)) {
		t.Fatal("recent id still deduped")
	}
	if !s.RecordDeath(death("id0", t0, "V", nil)) {
		t.Fatal("evicted id is forgotten")
	}
}

func TestVictimSetCap(t *testing.T) {
	old := maxVictimsPerPlayer
	maxVictimsPerPlayer = 3
	defer func() { maxVictimsPerPlayer = old }()
	s := New("")
	k := foe(1)
	for i := 0; i < 6; i++ {
		s.RecordDeath(death(fmt.Sprintf("d%d", i), t0, fmt.Sprintf("V%d", i), &k))
	}
	if d, _ := s.Player(k.GUID); d.KillingBlows != 6 || d.InvolvedDeaths != 6 {
		t.Fatalf("%+v", d)
	}
	s.mu.Lock()
	n := len(s.players[k.GUID].Victims)
	s.mu.Unlock()
	if n > 3 {
		t.Fatalf("victim set grew to %d", n)
	}
}

func TestRunFlushesOnCancel(t *testing.T) {
	p := filepath.Join(t.TempDir(), "b.json")
	s, _ := Open(p)
	s.RecordDeath(death("d", t0, "V", &Participant{GUID: "Player-1-X", Raw: "X-R"}))
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { s.Run(ctx, 10*time.Millisecond); close(done) }()
	time.Sleep(50 * time.Millisecond)
	cancel()
	<-done
	if !strings.Contains(readFile(p), "Player-1-X") {
		t.Fatal("not flushed")
	}
}
