package contactx

import (
	"fmt"
	"testing"
	"time"

	"radarlive/internal/domain"
)

const (
	meGUID  = "Player-1-0A"
	foeGUID = "Player-1-0B"
	npcGUID = "Creature-0-1"
	baseMs  = int64(1_800_000_000_000)
)

func at(sec int) int64 { return baseMs + int64(sec)*1000 }

func eu(guid, raw string, flags uint32) domain.EventUnit {
	return domain.EventUnit{GUID: guid, Raw: raw, Flags: flags}
}

func infoAt(guid string, x, y float64, mapID, level int, hp, maxHP float64) *domain.EventInfo {
	return &domain.EventInfo{GUID: guid, X: x, Y: y, MapID: mapID, HasPos: true, Level: level, HP: hp, MaxHP: maxHP, HasHP: true}
}

// meEvent is the log owner doing something at a known position.
func meEvent(sec int, x, y float64) domain.Event {
	return domain.Event{TimeMs: at(sec), Name: "SWING_DAMAGE", Src: eu(meGUID, "Ann-PvP-US", 0x511),
		Dst: eu(npcGUID, "Boar", 0x148), Info: infoAt(meGUID, x, y, 10, 60, 90, 100)}
}

func newX(t *testing.T, o Options) *Extractor {
	t.Helper()
	return NewExtractor(o)
}

func find(t *testing.T, p Part, guid string) domain.PingContact {
	t.Helper()
	for _, c := range p.Contacts {
		if c.GUID == guid {
			return c
		}
	}
	t.Fatalf("contact %s not in part (%d contacts)", guid, len(p.Contacts))
	return domain.PingContact{}
}

func TestExactFixAndIdentity(t *testing.T) {
	x := newX(t, Options{})
	x.Feed(meEvent(0, 10.04, 20.06))
	c := find(t, x.Flush(time.Time{}), meGUID)
	if c.Name != "Ann PvP US" || c.Raw != "Ann-PvP-US" {
		t.Fatalf("names: %q %q", c.Name, c.Raw)
	}
	if c.Reaction != domain.ReactionFriendly || c.Level != 60 || c.MaxHP != 100 {
		t.Fatalf("identity: %+v", c.Contact)
	}
	if c.X != 10.0 || c.Y != 20.1 || c.MapID != 10 || c.PosTime != at(0) || !c.HasPosition() {
		t.Fatalf("position: %+v", c.Contact)
	}
	if len(c.Fixes) != 1 || c.Fixes[0].Kind != domain.FixExact || c.Fixes[0].X != 10.0 || c.Fixes[0].Y != 20.1 {
		t.Fatalf("fixes: %+v", c.Fixes)
	}
	if c.FirstSeen != at(0) || c.LastSeen != at(0) {
		t.Fatalf("times: %d %d", c.FirstSeen, c.LastSeen)
	}
}

func TestCountersAreDeltasAndReflushIsEmpty(t *testing.T) {
	x := newX(t, Options{})
	x.Feed(meEvent(0, 1, 1))
	x.Feed(meEvent(1, 1, 1))
	first := find(t, x.Flush(time.Time{}), meGUID)
	if first.D.Events != 2 || first.D.SrcEvents != 2 || first.D.HPSamples != 2 || first.D.HPSum != 180 {
		t.Fatalf("first deltas: %+v", first.D)
	}
	if again := x.Flush(time.Time{}); len(again.Contacts) != 0 || len(again.Charts) != 0 {
		t.Fatalf("re-flush must be empty: %+v", again)
	}
	x.Feed(meEvent(2, 1, 1))
	second := find(t, x.Flush(time.Time{}), meGUID)
	if second.D.Events != 1 || second.D.HPSamples != 1 || second.D.HPSum != 90 {
		t.Fatalf("second deltas must only hold new work: %+v", second.D)
	}
	if second.FirstSeen != at(0) {
		t.Fatalf("firstSeen should stay at the first sighting: %d", second.FirstSeen)
	}
}

func TestDestinationEventsAreNotSourceEvents(t *testing.T) {
	x := newX(t, Options{})
	x.Feed(domain.Event{TimeMs: at(0), Name: "SWING_DAMAGE", Src: eu(foeGUID, "Bob-PvP-US", 0x548),
		Dst: eu(meGUID, "Ann-PvP-US", 0x511)})
	part := x.Flush(time.Time{})
	if d := find(t, part, meGUID).D; d.Events != 1 || d.SrcEvents != 0 {
		t.Fatalf("dst deltas: %+v", d)
	}
	if d := find(t, part, foeGUID).D; d.Events != 1 || d.SrcEvents != 1 {
		t.Fatalf("src deltas: %+v", d)
	}
}

func TestInfoUnitOnlyCountsOnceWhenAlsoSource(t *testing.T) {
	x := newX(t, Options{})
	x.Feed(meEvent(0, 1, 1)) // info unit is the source
	if d := find(t, x.Flush(time.Time{}), meGUID).D; d.Events != 1 {
		t.Fatalf("events=%d", d.Events)
	}
}

func TestApproximateFixesNearMe(t *testing.T) {
	x := newX(t, Options{})
	x.Feed(meEvent(0, 100, 200))
	x.Feed(domain.Event{TimeMs: at(60), Name: "SPELL_AURA_APPLIED", Src: eu(foeGUID, "Bob-PvP-US", 0x548),
		Dst: eu(meGUID, "Ann-PvP-US", 0x511)})
	part := x.Flush(time.Time{})
	foe := find(t, part, foeGUID)
	if len(foe.Fixes) != 1 || foe.Fixes[0].Kind != domain.FixApprox || foe.Fixes[0].X != 100 || foe.Fixes[0].Y != 200 {
		t.Fatalf("approx fix: %+v", foe.Fixes)
	}
	if foe.X != 100 || foe.MapID != 10 || foe.PosTime != at(60) {
		t.Fatalf("approx position: %+v", foe.Contact)
	}
	for _, f := range find(t, part, meGUID).Fixes {
		if f.Kind == domain.FixApprox {
			t.Fatal("me must not get approx fixes")
		}
	}
}

func TestApproximateFixNeedsFreshMeFix(t *testing.T) {
	x := newX(t, Options{})
	x.Feed(meEvent(0, 1, 2))
	x.Feed(domain.Event{TimeMs: at(120), Name: "SPELL_AURA_APPLIED", Src: eu(foeGUID, "Bob", 0x548), Dst: eu(npcGUID, "Boar", 0x148)})
	if foe := find(t, x.Flush(time.Time{}), foeGUID); len(foe.Fixes) != 0 || foe.HasPosition() {
		t.Fatalf("120 s old me fix must not be used: %+v", foe)
	}
}

func TestApproximateFixCanBeDisabled(t *testing.T) {
	x := newX(t, Options{DisableApprox: true})
	x.Feed(meEvent(0, 1, 2))
	x.Feed(domain.Event{TimeMs: at(1), Name: "X", Src: eu(foeGUID, "Bob", 0x548), Dst: eu(npcGUID, "Boar", 0x148)})
	if foe := find(t, x.Flush(time.Time{}), foeGUID); foe.HasPosition() {
		t.Fatalf("approx disabled: %+v", foe)
	}
}

func TestExactPositionBeatsLaterApprox(t *testing.T) {
	x := newX(t, Options{})
	x.Feed(meEvent(0, 100, 100))
	x.Feed(domain.Event{TimeMs: at(1), Name: "SPELL_DAMAGE", Src: eu(foeGUID, "Bob", 0x548), Dst: eu(npcGUID, "Boar", 0x148),
		Info: infoAt(foeGUID, 500, 600, 10, 50, 5, 10)})
	x.Feed(domain.Event{TimeMs: at(10), Name: "X", Src: eu(foeGUID, "Bob", 0x548), Dst: eu(npcGUID, "Boar", 0x148)})
	foe := find(t, x.Flush(time.Time{}), foeGUID)
	if foe.X != 500 || foe.Y != 600 {
		t.Fatalf("exact fix should hold the position for 30 s: %+v", foe.Contact)
	}
	x.Feed(meEvent(50, 100, 100))
	x.Feed(domain.Event{TimeMs: at(51), Name: "X", Src: eu(foeGUID, "Bob", 0x548), Dst: eu(npcGUID, "Boar", 0x148)})
	foe = find(t, x.Flush(time.Time{}), foeGUID)
	if foe.X != 100 || foe.PosTime != at(51) {
		t.Fatalf("a much newer approx fix takes over: %+v", foe.Contact)
	}
}

func TestOnlyPlayersByDefault(t *testing.T) {
	ev := domain.Event{TimeMs: at(0), Name: "SWING_DAMAGE", Src: eu(npcGUID, "Boar", 0x148),
		Dst: eu("Pet-0-9", "Wolf", 0x1112), Info: infoAt(npcGUID, 1, 2, 3, 4, 5, 6)}
	x := newX(t, Options{})
	x.Feed(ev)
	if got := x.Flush(time.Time{}); len(got.Contacts) != 0 {
		t.Fatalf("npcs tracked by default: %+v", got.Contacts)
	}
	x = newX(t, Options{TrackNPCs: true})
	x.Feed(ev)
	if got := x.Flush(time.Time{}); len(got.Contacts) != 2 {
		t.Fatalf("want npc and pet, got %d", len(got.Contacts))
	}
}

func TestDamageCredit(t *testing.T) {
	x := newX(t, Options{})
	dmg := func(sec int, amount float64, has, isDamage bool) domain.Event {
		return domain.Event{TimeMs: at(sec), Name: "SPELL_DAMAGE", Src: eu(foeGUID, "Bob", 0x548), Dst: eu(meGUID, "Ann", 0x511),
			Amount: amount, HasAmount: has, IsDamage: isDamage}
	}
	x.Feed(dmg(0, 100.04, true, true))
	x.Feed(dmg(1, 50, true, true))
	x.Feed(dmg(2, 0, true, true))    // fully absorbed: an event, not a hit
	x.Feed(dmg(3, 999, true, false)) // a *_LANDED duplicate is not damage
	x.Feed(dmg(4, 0, false, true))   // no amount parsed: an event only
	part := x.Flush(time.Time{})
	foe, me := find(t, part, foeGUID), find(t, part, meGUID)
	if foe.D.DmgTotal != 150.0 || foe.D.DmgHits != 2 || foe.D.DmgEvents != 4 {
		t.Fatalf("foe damage: %+v", foe.D)
	}
	if !me.D.IsZero() && (me.D.DmgTotal != 0 || me.D.DmgEvents != 0) {
		t.Fatalf("victim must not be credited: %+v", me.D)
	}
}

func TestMeDetectionNeedsPlayerGUIDAndFlag(t *testing.T) {
	x := newX(t, Options{})
	x.Feed(domain.Event{TimeMs: at(0), Name: "X", Src: eu(npcGUID, "Pet", 0x1), Dst: eu(foeGUID, "Bob", 0x548)})
	if x.Me() != "" {
		t.Fatalf("npc flagged mine is not me: %q", x.Me())
	}
	x.Feed(meEvent(1, 1, 1))
	if x.Me() != meGUID {
		t.Fatalf("me=%q", x.Me())
	}
}

func TestPastNames(t *testing.T) {
	x := newX(t, Options{})
	for i := range 14 {
		x.Feed(domain.Event{TimeMs: at(i), Name: "X", Src: eu(foeGUID, fmt.Sprintf("Name%d", i), 0x548), Dst: eu(meGUID, "Ann", 0x511)})
	}
	foe := find(t, x.Flush(time.Time{}), foeGUID)
	if foe.Name != "Name13" || len(foe.PastNames) != domain.PastNamesKeptMax || foe.PastNames[len(foe.PastNames)-1] != "Name12" {
		t.Fatalf("name=%q past=%v", foe.Name, foe.PastNames)
	}
	x.Feed(domain.Event{TimeMs: at(20), Name: "X", Src: eu(foeGUID, "Name13", 0x548), Dst: eu(meGUID, "Ann", 0x511)})
	foe = find(t, x.Flush(time.Time{}), foeGUID)
	for _, p := range foe.PastNames {
		if p == foe.Name {
			t.Fatalf("current name listed as past: %v", foe.PastNames)
		}
	}
}

func TestRealmSuffixDetectedAndStripped(t *testing.T) {
	x := newX(t, Options{})
	for i := range 7 {
		x.Feed(domain.Event{TimeMs: at(i), Name: "X", Src: eu(fmt.Sprintf("Player-1-%02d", i), fmt.Sprintf("N%d-PvP-US", i), 0x548),
			Dst: eu(npcGUID, "Boar", 0x148)})
	}
	if c := find(t, x.Flush(time.Time{}), "Player-1-03"); c.Name != "N3" || c.Raw != "N3-PvP-US" {
		t.Fatalf("name=%q raw=%q", c.Name, c.Raw)
	}
}

func TestZoneAndChartNames(t *testing.T) {
	x := newX(t, Options{})
	x.Feed(domain.Event{TimeMs: at(0), Name: "ZONE_CHANGE", Zone: "Testville"})
	x.Feed(meEvent(1, 1, 1))
	if c := find(t, x.Flush(time.Time{}), meGUID); c.Zone != "Testville" {
		t.Fatalf("zone=%q", c.Zone)
	}
	x.Feed(domain.Event{TimeMs: at(2), Name: "MAP_CHANGE", Chart: &domain.Chart{MapID: 10, Name: "Chart Ten", X1: 1, Y1: 1}})
	x.Feed(meEvent(3, 50, 50))
	if c := find(t, x.Flush(time.Time{}), meGUID); c.Zone != "Chart Ten" {
		t.Fatalf("chart name should win for its map: %q", c.Zone)
	}
}

func TestChartsFlushedOncePerChange(t *testing.T) {
	x := newX(t, Options{})
	ch := domain.Chart{MapID: 7, Name: "Seven", X0: 0, X1: 10, Y0: 0, Y1: 20}
	x.Feed(domain.Event{TimeMs: at(0), Name: "MAP_CHANGE", Chart: &ch})
	if p := x.Flush(time.Time{}); len(p.Charts) != 1 || p.Charts[0] != ch {
		t.Fatalf("charts: %+v", p.Charts)
	}
	x.Feed(domain.Event{TimeMs: at(1), Name: "MAP_CHANGE", Chart: &ch})
	if p := x.Flush(time.Time{}); len(p.Charts) != 0 {
		t.Fatalf("unchanged chart re-sent: %+v", p.Charts)
	}
	ch.X1 = 11
	x.Feed(domain.Event{TimeMs: at(2), Name: "MAP_CHANGE", Chart: &ch})
	if p := x.Flush(time.Time{}); len(p.Charts) != 1 || p.Charts[0].X1 != 11 {
		t.Fatalf("recalibrated chart not sent: %+v", p.Charts)
	}
}

func TestRulesetReported(t *testing.T) {
	x := newX(t, Options{})
	e := meEvent(0, 1, 1)
	e.Ruleset = domain.RulesetRP
	x.Feed(e)
	if p := x.Flush(time.Time{}); p.Ruleset != domain.RulesetRP {
		t.Fatalf("ruleset=%q", p.Ruleset)
	}
	x.Feed(meEvent(1, 1, 1)) // later events without a token keep the detected ruleset
	if p := x.Flush(time.Time{}); p.Ruleset != domain.RulesetRP {
		t.Fatalf("ruleset=%q", p.Ruleset)
	}
}

func TestTrailDedupe(t *testing.T) {
	cases := []struct {
		name   string
		second func() domain.Event
		keep   bool
	}{
		{"1 yd in 2 s", func() domain.Event { return meEvent(2, 101, 100) }, false},
		{"3 yd in 2 s", func() domain.Event { return meEvent(2, 103, 100) }, true},
		{"same spot after 6 s", func() domain.Event { return meEvent(6, 100, 100) }, true},
		{"older than the last fix", func() domain.Event { return meEvent(-1, 150, 100) }, false},
		{"other chart", func() domain.Event {
			e := meEvent(1, 100, 100)
			e.Info.MapID = 11
			return e
		}, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			x := newX(t, Options{})
			x.Feed(meEvent(0, 100, 100))
			x.Feed(c.second())
			fixes := find(t, x.Flush(time.Time{}), meGUID).Fixes
			if got := len(fixes) == 2; got != c.keep {
				t.Fatalf("kept=%v want %v: %+v", got, c.keep, fixes)
			}
		})
	}
}

func TestTrailDedupeSpansFlushes(t *testing.T) {
	x := newX(t, Options{})
	x.Feed(meEvent(0, 100, 100))
	x.Flush(time.Time{})
	x.Feed(meEvent(1, 100.5, 100))
	if got := x.Flush(time.Time{}); len(find(t, got, meGUID).Fixes) != 0 {
		t.Fatalf("duplicate of a flushed fix re-sent: %+v", got.Contacts)
	}
}

func TestPendingFixesAreBounded(t *testing.T) {
	x := newX(t, Options{})
	for i := range domain.TrailCapFixes + 50 {
		x.Feed(meEvent(i*10, float64(i)*5, 0))
	}
	fixes := find(t, x.Flush(time.Time{}), meGUID).Fixes
	if len(fixes) != domain.TrailCapFixes || fixes[len(fixes)-1].T != at((domain.TrailCapFixes+49)*10) {
		t.Fatalf("len=%d", len(fixes))
	}
}

func TestMemoryIsBounded(t *testing.T) {
	x := newX(t, Options{MaxContacts: 40})
	x.Feed(meEvent(0, 1, 1))
	for i := range 500 {
		x.Feed(domain.Event{TimeMs: at(i + 1), Name: "X", Src: eu(fmt.Sprintf("Player-1-%03d", i), "N", 0x548), Dst: eu(npcGUID, "B", 0x148)})
		if x.Tracked() > 40 {
			t.Fatalf("tracked=%d exceeds cap", x.Tracked())
		}
	}
	if x.Me() != meGUID {
		t.Fatal("me lost")
	}
	if c := find(t, x.Flush(time.Time{}), "Player-1-499"); c.D.Events != 1 {
		t.Fatalf("newest contact was evicted: %+v", c.D)
	}
}

func TestFlushPrunesIdleContacts(t *testing.T) {
	x := newX(t, Options{})
	x.Feed(meEvent(0, 1, 1))
	x.Feed(domain.Event{TimeMs: at(0), Name: "X", Src: eu(foeGUID, "Bob", 0x548), Dst: eu(meGUID, "Ann", 0x511)})
	x.Flush(time.UnixMilli(at(60)))
	if x.Tracked() != 2 {
		t.Fatalf("tracked=%d", x.Tracked())
	}
	x.Flush(time.UnixMilli(at(0)).Add(ExpireAfter + time.Second))
	if x.Tracked() != 1 || x.Me() != meGUID {
		t.Fatalf("idle contact kept or me dropped: tracked=%d", x.Tracked())
	}
}

func TestDirtyContactsAreNotPruned(t *testing.T) {
	x := newX(t, Options{})
	x.Feed(domain.Event{TimeMs: at(0), Name: "X", Src: eu(foeGUID, "Bob", 0x548), Dst: eu(npcGUID, "Boar", 0x148)})
	part := x.Flush(time.UnixMilli(at(0)).Add(2 * ExpireAfter))
	find(t, part, foeGUID)
}

func TestUnknownReactionDoesNotOverwriteKnown(t *testing.T) {
	x := newX(t, Options{})
	x.Feed(domain.Event{TimeMs: at(0), Name: "X", Src: eu(foeGUID, "Bob", 0x548), Dst: eu(npcGUID, "B", 0x148)})
	x.Feed(domain.Event{TimeMs: at(1), Name: "X", Src: eu(foeGUID, "Bob", 0), Dst: eu(npcGUID, "B", 0x148)})
	if c := find(t, x.Flush(time.Time{}), foeGUID); c.Reaction != domain.ReactionHostile {
		t.Fatalf("reaction=%q", c.Reaction)
	}
}

func TestOutputPassesPingValidation(t *testing.T) {
	x := newX(t, Options{TrackNPCs: false})
	x.Feed(meEvent(0, 1, 1))
	x.Feed(domain.Event{TimeMs: at(1), Name: "MAP_CHANGE", Chart: &domain.Chart{MapID: 1, Name: "C", X1: 1, Y1: 1}})
	p := x.Flush(time.Time{})
	ping := domain.Ping{RS: domain.RulesetPvP, BatchID: "b1", Source: "s", Contacts: p.Contacts, Charts: p.Charts}
	if err := ping.Validate(); err != nil {
		t.Fatal(err)
	}
}

func realmEvent(sec int, i int, raw string) domain.Event {
	return domain.Event{TimeMs: at(sec), Name: "SWING_DAMAGE",
		Src: eu(fmt.Sprintf("Player-1-%03d", i), raw, 0x548), Dst: eu(npcGUID, "Boar", 0x148)}
}

func TestRealmSuffixBelowThresholdNotMangled(t *testing.T) {
	x := newX(t, Options{})
	x.Feed(realmEvent(0, 1, "Mary-Ann"))
	x.Feed(realmEvent(0, 2, "Bob-PvP-US"))
	c := find(t, x.Flush(time.Time{}), "Player-1-001")
	if c.Name != "Mary Ann" || c.Raw != "Mary-Ann" {
		t.Fatalf("%q %q", c.Name, c.Raw)
	}
}

func TestRealmSuffixAutoDetectedWithMinority(t *testing.T) {
	x := newX(t, Options{})
	x.Feed(realmEvent(0, 1, "Aaa-PvP-US"))
	x.Feed(realmEvent(0, 2, "Zed-PvE-EU"))
	first := x.Flush(time.Time{})
	if c := find(t, first, "Player-1-001"); c.Name != "Aaa PvP US" {
		t.Fatalf("before detection: %q", c.Name)
	}
	for i, n := range []string{"Bbb", "Ccc", "Ddd", "Eee", "Fff"} {
		x.Feed(realmEvent(1, 3+i, n+"-PvP-US"))
	}
	x.Feed(realmEvent(2, 1, "Aaa-PvP-US"))
	x.Feed(realmEvent(2, 2, "Zed-PvE-EU"))
	p := x.Flush(time.Time{})
	a := find(t, p, "Player-1-001")
	if a.Name != "Aaa" || a.Raw != "Aaa-PvP-US" {
		t.Fatalf("majority: %q %q", a.Name, a.Raw)
	}
	if len(a.PastNames) != 1 || a.PastNames[0] != "Aaa PvP US" {
		t.Fatalf("past=%v", a.PastNames)
	}
	z := find(t, p, "Player-1-002")
	if z.Name != "Zed PvE EU" || len(z.PastNames) != 0 {
		t.Fatalf("minority: %q %v", z.Name, z.PastNames)
	}
	x.Feed(realmEvent(3, 1, "Aaa-PvP-US"))
	if a2 := find(t, x.Flush(time.Time{}), "Player-1-001"); len(a2.PastNames) != 1 {
		t.Fatalf("past must stay unique: %v", a2.PastNames)
	}
}
