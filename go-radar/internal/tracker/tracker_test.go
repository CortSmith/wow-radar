package tracker

import (
	"encoding/json"
	"testing"
	"time"

	"nearbyradar/internal/clock"
	"nearbyradar/internal/history"
	"nearbyradar/internal/parser"
	"nearbyradar/internal/screenshot"
)

var t0 = time.Date(2026, 11, 10, 20, 0, 0, 0, time.UTC)

const (
	meGUID    = "Player-1-0000ME01"
	otherGUID = "Player-1-0000OT02"
	foeGUID   = "Player-1-0000F003"
	npcGUID   = "Creature-0-1-0-11-1397-0000F38640"
	petGUID   = "Pet-0-1-0-11-416-06000C25E0"
)

const (
	fMe     = 0x511
	fFriend = 0x518
	fNeut   = 0x528
	fHostPl = 0x548
	fNPC    = 0xa28
)

func mkUnit(g, n string, f uint32) parser.Unit { return parser.Unit{GUID: g, Name: n, Flags: f} }

func info(g string, hp, mx, x, y float64, m int) *parser.Info {
	return &parser.Info{GUID: g, HP: hp, MaxHP: mx, HasHP: true, X: x, Y: y, MapID: m, HasPos: true, Level: 10}
}

func evAt(at time.Time, typ string, src, dst parser.Unit, in *parser.Info) parser.Event {
	return parser.Event{Time: at, Type: typ, Src: src, Dst: dst, Info: in}
}

func newT(t *testing.T, cfg Config) (*Tracker, *clock.Fake, *history.Store) {
	t.Helper()
	fc := clock.NewFake(t0)
	h := history.New("")
	cfg.History = h
	return New(cfg, fc), fc, h
}

func findPip(s Snapshot, g string) *Pip {
	for i := range s.Pips {
		if s.Pips[i].GUID == g {
			return &s.Pips[i]
		}
	}
	return nil
}

func TestAnyEventWithUnitInfoUpdatesTracker(t *testing.T) {
	tr, _, _ := newT(t, Config{})
	tr.Apply("a", evAt(t0, "SPELL_CAST_SUCCESS", mkUnit(otherGUID, "Ot-Her-PvP-US", fFriend), parser.Unit{}, info(otherGUID, 50, 100, -100, 200, 1453)))
	p := findPip(tr.Snapshot(), otherGUID)
	if p == nil {
		t.Fatal("no pip")
	}
	if p.X != -100 || p.Y != 200 || p.MapID != 1453 || p.PosKind != PosExact {
		t.Fatalf("pos %+v", p)
	}
	if p.HP != 50 || p.MaxHP != 100 || !p.HasHP {
		t.Fatalf("hp %+v", p)
	}
	if p.Reaction != "friendly" || p.Raw != "Ot-Her-PvP-US" || p.Name != "Ot Her PvP US" {
		t.Fatalf("meta %+v", p)
	}
	if p.State != StateActive || p.Events != 1 || p.LastEvent != t0.UnixMilli() {
		t.Fatalf("state %+v", p)
	}
}

func TestInfoBelongsToInfoGUIDNotSource(t *testing.T) {
	tr, _, _ := newT(t, Config{})
	// the attacker (foe) hits me; the advanced block describes ME (the destination)
	tr.Apply("a", evAt(t0, "SWING_DAMAGE", mkUnit(foeGUID, "Foe-X", fHostPl), mkUnit(meGUID, "Me-X", fMe), info(meGUID, 80, 100, 10, 20, 1453)))
	s := tr.Snapshot()
	foe, me := findPip(s, foeGUID), findPip(s, meGUID)
	if foe == nil || me == nil {
		t.Fatalf("pips %+v", s.Pips)
	}
	if foe.PosKind == PosExact || foe.X == 10 && foe.PosKind == PosExact {
		t.Fatalf("attacker must not get my position as an exact fix: %+v", foe)
	}
	if foe.PosKind != PosApprox { // named in my log range -> approximate sighting at my position
		t.Fatalf("attacker should be an approximate sighting: %+v", foe)
	}
	if me.PosKind != PosExact || me.X != 10 {
		t.Fatalf("me %+v", me)
	}
	if !me.Me || s.Me == nil || s.Me.GUID != meGUID {
		t.Fatal("me flag")
	}
	if foe.Reaction != "hostile" {
		t.Fatalf("reaction %s", foe.Reaction)
	}
}

func TestDamageDealtAttributedToSource(t *testing.T) {
	tr, _, h := newT(t, Config{})
	for i, amt := range []float64{10, 30} {
		e := evAt(t0.Add(time.Duration(i)*time.Second), "SPELL_DAMAGE", mkUnit(foeGUID, "Foe-X", fHostPl), mkUnit(otherGUID, "Ot-X", fFriend), info(otherGUID, 50, 100, 1, 1, 1))
		e.IsDamage, e.HasAmount, e.Amount = true, true, amt
		tr.Apply("a", e)
	}
	s := tr.Snapshot()
	foe, ot := findPip(s, foeGUID), findPip(s, otherGUID)
	if foe.DamageTotal != 40 || foe.DamageHits != 2 {
		t.Fatalf("foe %+v", foe)
	}
	if ot.DamageTotal != 0 {
		t.Fatal("target must not be credited")
	}
	he, _ := h.Get(foeGUID)
	if he.AvgDamagePerHit != 20 || he.SrcEvents != 2 {
		t.Fatalf("history %+v", he)
	}
	ho, _ := h.Get(otherGUID)
	if ho.AvgMaxHP != 100 || ho.HPSamples != 2 {
		t.Fatalf("target history %+v", ho)
	}
}

func TestExpiryTable(t *testing.T) {
	tests := []struct {
		name      string
		age       time.Duration
		wantPip   bool
		wantState string
	}{
		{"fresh", 0, true, StateActive},
		{"just under 5 minutes", 5*time.Minute - time.Second, true, StateActive},
		{"exactly 5 minutes still active", 5 * time.Minute, true, StateActive},
		{"over 5 minutes is stale", 5*time.Minute + time.Second, true, StateStale},
		{"just under 15 minutes stale", 15*time.Minute - time.Second, true, StateStale},
		{"over 15 minutes removed", 15*time.Minute + time.Second, false, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tr, fc, h := newT(t, Config{})
			tr.Apply("a", evAt(t0, "SPELL_CAST_SUCCESS", mkUnit(otherGUID, "O-X", fFriend), parser.Unit{}, info(otherGUID, 1, 2, 3, 4, 5)))
			fc.Advance(tc.age)
			p := findPip(tr.Snapshot(), otherGUID)
			if (p != nil) != tc.wantPip {
				t.Fatalf("pip presence %v", p != nil)
			}
			if p != nil && p.State != tc.wantState {
				t.Fatalf("state %s", p.State)
			}
			if _, ok := h.Get(otherGUID); !ok {
				t.Fatal("history must survive expiry")
			}
		})
	}
}

func TestExpiryIsPerTargetAndUsesLogTimeAgainstWallClock(t *testing.T) {
	tr, fc, _ := newT(t, Config{})
	tr.Apply("a", evAt(t0, "SPELL_CAST_SUCCESS", mkUnit(otherGUID, "O-X", fFriend), parser.Unit{}, info(otherGUID, 1, 2, 3, 4, 5)))
	fc.Advance(10 * time.Minute)
	// B logs an event "now" (log time == wall time); A's own last event is 10 minutes old
	tr.Apply("a", evAt(fc.Now(), "SPELL_CAST_SUCCESS", mkUnit(foeGUID, "F-X", fHostPl), parser.Unit{}, info(foeGUID, 1, 2, 3, 4, 5)))
	s := tr.Snapshot()
	if findPip(s, otherGUID).State != StateStale || findPip(s, foeGUID).State != StateActive {
		t.Fatalf("%+v", s.Pips)
	}
	if s.Pips[0].GUID != foeGUID {
		t.Fatal("most recent first")
	}
}

func TestReturningTargetRevivesAndHistoryMerges(t *testing.T) {
	tr, fc, h := newT(t, Config{})
	e := evAt(t0, "SPELL_DAMAGE", mkUnit(foeGUID, "F-X", fHostPl), mkUnit(otherGUID, "O-X", fFriend), info(otherGUID, 1, 2, 3, 4, 5))
	e.IsDamage, e.HasAmount, e.Amount = true, true, 100
	tr.Apply("a", e)
	fc.Advance(20 * time.Minute)
	if findPip(tr.Snapshot(), foeGUID) != nil {
		t.Fatal("should be gone")
	}
	e2 := evAt(fc.Now(), "SPELL_DAMAGE", mkUnit(foeGUID, "F-X", fHostPl), mkUnit(otherGUID, "O-X", fFriend), info(otherGUID, 1, 2, 3, 4, 5))
	e2.IsDamage, e2.HasAmount, e2.Amount = true, true, 300
	tr.Apply("a", e2)
	p := findPip(tr.Snapshot(), foeGUID)
	if p == nil || p.State != StateActive {
		t.Fatal("should be back")
	}
	he, _ := h.Get(foeGUID)
	if he.DamageHits != 2 || he.AvgDamagePerHit != 200 {
		t.Fatalf("history not merged: %+v", he)
	}
}

func TestNPCTrackingToggle(t *testing.T) {
	line := func() parser.Event {
		return evAt(t0, "SWING_DAMAGE", mkUnit(meGUID, "Me-X", fMe), mkUnit(npcGUID, "Boar", fNPC), info(npcGUID, 5, 10, 1, 2, 3))
	}
	tr, _, _ := newT(t, Config{})
	tr.Apply("a", line())
	if findPip(tr.Snapshot(), npcGUID) != nil {
		t.Fatal("npc tracked by default")
	}
	tr2, _, _ := newT(t, Config{TrackNPCs: true})
	tr2.Apply("a", line())
	p := findPip(tr2.Snapshot(), npcGUID)
	if p == nil || p.Kind != "npc" || p.Reaction != "neutral" || p.PosKind != PosExact {
		t.Fatalf("%+v", p)
	}
	tr2.Apply("a", evAt(t0, "SPELL_DAMAGE", mkUnit(petGUID, "Pet", 0x1118), mkUnit(npcGUID, "Boar", fNPC), nil))
	if p := findPip(tr2.Snapshot(), petGUID); p == nil || p.Kind != "pet" {
		t.Fatalf("pet %+v", p)
	}
}

func TestMeAndApproximateSightings(t *testing.T) {
	tr, _, _ := newT(t, Config{})
	tr.Apply("a", evAt(t0, "SPELL_CAST_SUCCESS", mkUnit(meGUID, "Me-X", fMe), parser.Unit{}, info(meGUID, 9, 9, 100, 200, 1453)))
	// other player named with no position of their own, 10s later
	tr.Apply("a", evAt(t0.Add(10*time.Second), "SPELL_AURA_APPLIED", mkUnit(otherGUID, "O-X", fFriend), mkUnit(foeGUID, "F-X", fHostPl), nil))
	s := tr.Snapshot()
	o := findPip(s, otherGUID)
	if o.PosKind != PosApprox || o.X != 100 || o.Y != 200 || o.MapID != 1453 || o.Weight >= 1 {
		t.Fatalf("approx %+v", o)
	}
	if findPip(s, meGUID).PosKind != PosExact {
		t.Fatal("me stays exact")
	}
	// my fix is now >120s old: no new approximate fix for a newcomer
	late := t0.Add(200 * time.Second)
	tr.Apply("a", evAt(late, "SPELL_AURA_APPLIED", mkUnit("Player-1-0000NEW9", "N-X", fFriend), parser.Unit{}, nil))
	if p := findPip(tr.Snapshot(), "Player-1-0000NEW9"); p == nil || p.PosKind != PosNone {
		t.Fatalf("stale me fix must not approximate: %+v", p)
	}
	// an exact fix replaces the approximation
	tr.Apply("a", evAt(t0.Add(11*time.Second), "SPELL_CAST_SUCCESS", mkUnit(otherGUID, "O-X", fFriend), parser.Unit{}, info(otherGUID, 1, 1, 7, 8, 1453)))
	o = findPip(tr.Snapshot(), otherGUID)
	if o.PosKind != PosExact || o.X != 7 {
		t.Fatalf("exact should win: %+v", o)
	}
}

func TestApproxDisabled(t *testing.T) {
	tr, _, _ := newT(t, Config{DisableApprox: true})
	tr.Apply("a", evAt(t0, "SPELL_CAST_SUCCESS", mkUnit(meGUID, "Me-X", fMe), parser.Unit{}, info(meGUID, 9, 9, 100, 200, 1453)))
	tr.Apply("a", evAt(t0, "SPELL_AURA_APPLIED", mkUnit(otherGUID, "O-X", fFriend), parser.Unit{}, nil))
	if findPip(tr.Snapshot(), otherGUID).PosKind != PosNone {
		t.Fatal("approx should be disabled")
	}
}

func TestZoneAndMapChange(t *testing.T) {
	tr, _, _ := newT(t, Config{})
	tr.Apply("a", parser.Event{Time: t0, Type: "ZONE_CHANGE", Zone: "Dun Morogh"})
	tr.Apply("a", parser.Event{Time: t0, Type: "MAP_CHANGE", Map: &parser.MapBounds{ID: 1426, Name: "Dun Morogh", X0: -1, X1: 1, Y0: -2, Y1: 2}})
	adv := true
	tr.Apply("a", parser.Event{Time: t0, Type: "COMBAT_LOG_VERSION", AdvancedEnabled: &adv})
	s := tr.Snapshot()
	if s.Zone != "Dun Morogh" || s.MapID != 1426 || s.Maps["1426"].Name != "Dun Morogh" {
		t.Fatalf("%+v", s)
	}
	if s.Advanced == nil || !*s.Advanced {
		t.Fatal("advanced")
	}
	// a located event for me moves the current map
	tr.Apply("a", evAt(t0, "SPELL_CAST_SUCCESS", mkUnit(meGUID, "Me-X", fMe), parser.Unit{}, info(meGUID, 1, 1, 0, 0, 1455)))
	if tr.Snapshot().MapID != 1455 {
		t.Fatal("map follows me")
	}
}

func TestObservationUpdatesMeAndCountsAsEvent(t *testing.T) {
	tr, fc, h := newT(t, Config{})
	tr.Apply("a", evAt(t0, "SPELL_CAST_SUCCESS", mkUnit(meGUID, "Me-X", fMe), parser.Unit{}, info(meGUID, 9, 9, 1, 2, 1453)))
	fc.Advance(6 * time.Minute) // me is stale now
	if findPip(tr.Snapshot(), meGUID).State != StateStale {
		t.Fatal("precondition")
	}
	v := tr.Version()
	tr.ApplyObservation(screenshot.Observation{Time: fc.Now(), Zone: "Ironforge", MapID: 1455, X: 33, Y: 44, HasZone: true, HasMap: true, HasPos: true})
	s := tr.Snapshot()
	me := findPip(s, meGUID)
	if me.X != 33 || me.Y != 44 || me.MapID != 1455 || me.State != StateActive || me.Events != 2 {
		t.Fatalf("me %+v", me)
	}
	if s.Zone != "Ironforge" || s.MapID != 1455 {
		t.Fatalf("zone/map %+v", s)
	}
	if tr.Version() == v {
		t.Fatal("version must bump")
	}
	if e, _ := h.Get(meGUID); e.LastZone != "Ironforge" || e.LastX != 33 {
		t.Fatalf("history %+v", e)
	}
	// ignores invalid
	tr.ApplyObservation(screenshot.Observation{})
	if tr.Snapshot().Zone != "Ironforge" {
		t.Fatal("invalid observation changed state")
	}
}

func TestObservationBeforeMeKnownIsApplied(t *testing.T) {
	tr, fc, _ := newT(t, Config{})
	tr.ApplyObservation(screenshot.Observation{Time: fc.Now(), MapID: 1455, X: 5, Y: 6, HasMap: true, HasPos: true, Zone: "Z", HasZone: true})
	if tr.Snapshot().Zone != "Z" {
		t.Fatal("zone applied immediately")
	}
	tr.Apply("a", evAt(fc.Now(), "SPELL_AURA_APPLIED", mkUnit(meGUID, "Me-X", fMe), parser.Unit{}, nil))
	me := findPip(tr.Snapshot(), meGUID)
	if me.X != 5 || me.PosKind != PosExact {
		t.Fatalf("pending observation not applied: %+v", me)
	}
}

func TestObservationOnlyZone(t *testing.T) {
	tr, fc, _ := newT(t, Config{})
	tr.Apply("a", evAt(t0, "SPELL_CAST_SUCCESS", mkUnit(meGUID, "Me-X", fMe), parser.Unit{}, info(meGUID, 9, 9, 1, 2, 1453)))
	tr.ApplyObservation(screenshot.Observation{Time: fc.Now(), Zone: "Z2", HasZone: true})
	me := findPip(tr.Snapshot(), meGUID)
	if me.X != 1 || tr.Snapshot().Zone != "Z2" {
		t.Fatalf("%+v", me)
	}
}

func TestMultipleSourcesKeepSeparateMe(t *testing.T) {
	tr, _, _ := newT(t, Config{})
	other := "Player-1-0000ME99"
	tr.Apply("log1", evAt(t0, "SPELL_CAST_SUCCESS", mkUnit(meGUID, "Me-X", fMe), parser.Unit{}, info(meGUID, 1, 1, 1, 1, 1)))
	tr.Apply("log2", evAt(t0.Add(time.Second), "SPELL_CAST_SUCCESS", mkUnit(other, "Me2-X", fMe), parser.Unit{}, info(other, 1, 1, 2, 2, 1)))
	s := tr.Snapshot()
	if s.Me == nil || s.Me.GUID != other {
		t.Fatalf("primary me is that of the most recently active source: %+v", s.Me)
	}
	if !findPip(s, meGUID).Me || !findPip(s, other).Me {
		t.Fatal("both are 'me' of their own source")
	}
	if len(s.Sources) != 2 {
		t.Fatalf("sources %v", s.Sources)
	}
}

func TestRealmSuffixDetection(t *testing.T) {
	tr, _, _ := newT(t, Config{})
	names := []string{"A-Aa-PvP-US", "B-Bb-PvP-US", "C-Cc-PvP-US", "D-Dd-PvP-US", "E-Ee-PvP-US", "F-Ff-PvP-US"}
	for i, n := range names {
		g := "Player-1-0000000" + string(rune('A'+i))
		tr.Apply("a", evAt(t0, "SPELL_AURA_APPLIED", mkUnit(g, n, fFriend), parser.Unit{}, nil))
	}
	s := tr.Snapshot()
	if s.RealmSuffix != "-PvP-US" {
		t.Fatalf("suffix %q", s.RealmSuffix)
	}
	if p := findPip(s, "Player-1-0000000A"); p.Name != "A Aa" {
		t.Fatalf("name %q", p.Name)
	}
	tr2, _, _ := newT(t, Config{RealmSuffix: "-PvP-US"})
	tr2.Apply("a", evAt(t0, "SPELL_AURA_APPLIED", mkUnit(otherGUID, "A-Aa-PvP-US", fFriend), parser.Unit{}, nil))
	if findPip(tr2.Snapshot(), otherGUID).Name != "A Aa" {
		t.Fatal("configured suffix")
	}
}

func TestApplyFromParsedLines(t *testing.T) {
	tr, _, _ := newT(t, Config{})
	lines := []string{
		`11/10/2026 14:00:00.000-6  SWING_DAMAGE,Player-1-0000ME01,"Me-Realm-PvP-US",0x511,0x80000000,Creature-0-6835-0-11-1397-0000F38640,"Boar",0xa28,0x80000000,Player-1-0000ME01,0000000000000000,56,147,90,0,171,0,0,0,3,35,105,0,-5370.61,-224.12,1426,1.8470,12,6,11,-1,1,0,0,0,nil,1,nil`,
		`not a line`,
	}
	for _, l := range lines {
		ev, err := parser.ParseLine(l, time.UTC)
		if err != nil {
			continue
		}
		tr.Apply("a", ev)
	}
	s := tr.Snapshot()
	me := findPip(s, meGUID)
	if me == nil || me.X != -5370.61 || me.DamageTotal != 6 {
		t.Fatalf("%+v", me)
	}
	if s.Stats.Events != 1 {
		t.Fatalf("stats %+v", s.Stats)
	}
}

func TestSnapshotJSONAndPrune(t *testing.T) {
	tr, fc, _ := newT(t, Config{})
	tr.Apply("a", evAt(t0, "SPELL_CAST_SUCCESS", mkUnit(otherGUID, "O-X", fFriend), parser.Unit{}, info(otherGUID, 1, 2, 3, 4, 5)))
	b, err := json.Marshal(tr.Snapshot())
	if err != nil || len(b) < 50 {
		t.Fatalf("json %v", err)
	}
	fc.Advance(time.Hour)
	tr.Prune()
	if tr.Stats().Units != 0 {
		t.Fatal("prune should drop expired units")
	}
}

func TestEstimate(t *testing.T) {
	now := t0.Add(5 * time.Second)
	fixes := []fix{
		{t: t0, x: 0, y: 0, kind: PosExact, mapID: 1},
		{t: t0.Add(2 * time.Second), x: 10, y: 0, kind: PosExact, mapID: 1},
		{t: t0.Add(4 * time.Second), x: 20, y: 0, kind: PosExact, mapID: 1},
	}
	e := estimate(fixes, now, 15, 30*time.Second)
	if e == nil || e.N != 3 || e.Exact != 3 {
		t.Fatalf("%+v", e)
	}
	if e.X <= 10 { // newest weighs more and movement is projected forward
		t.Fatalf("x %v", e.X)
	}
	if estimate(nil, now, 15, 30*time.Second) != nil {
		t.Fatal("empty")
	}
	// gap splits encounters: only the recent fix counts
	gapped := []fix{{t: t0.Add(-5 * time.Minute), x: 999, y: 999, kind: PosExact, mapID: 1}, {t: t0, x: 1, y: 2, kind: PosApprox, mapID: 1}}
	e = estimate(gapped, now, 15, 30*time.Second)
	if e.N != 1 || e.X != 1 || e.Exact != 0 {
		t.Fatalf("%+v", e)
	}
	// different map breaks the window
	mixed := []fix{{t: t0, x: 5, y: 5, kind: PosExact, mapID: 2}, {t: t0.Add(time.Second), x: 1, y: 2, kind: PosExact, mapID: 1}}
	if e := estimate(mixed, now, 15, 30*time.Second); e.N != 1 || e.MapID != 1 {
		t.Fatalf("%+v", e)
	}
	// n caps window
	if e := estimate(fixes, now, 2, 30*time.Second); e.N != 2 {
		t.Fatalf("%+v", e)
	}
}

func TestPipCarriesEstimate(t *testing.T) {
	tr, _, _ := newT(t, Config{})
	for i := 0; i < 3; i++ {
		tr.Apply("a", evAt(t0.Add(time.Duration(i)*time.Second), "SPELL_CAST_SUCCESS", mkUnit(otherGUID, "O-X", fFriend), parser.Unit{}, info(otherGUID, 1, 2, float64(i), 0, 5)))
	}
	if p := findPip(tr.Snapshot(), otherGUID); p.Est == nil || p.Est.N != 3 {
		t.Fatalf("%+v", p.Est)
	}
}

func TestConfigDefaults(t *testing.T) {
	c := Config{}.withDefaults()
	if c.StaleAfter != 5*time.Minute || c.ExpireAfter != 15*time.Minute || c.ApproxMaxAge != 120*time.Second {
		t.Fatalf("%+v", c)
	}
	tr := New(Config{}, nil)
	if tr.Snapshot().Pips == nil {
		t.Fatal("pips must be non-nil for JSON")
	}
}
