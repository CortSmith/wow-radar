package bounty

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"nearbyradar/internal/parser"
)

// ---- synthetic log helpers (names are invented) ----

const (
	vic    = `Player-1-0000A001,"Vic-Realm-PvP-US",0x518,0x80000000`
	vic2   = `Player-1-0000A002,"Wen-Realm-PvP-US",0x511,0x80000000`
	pal    = `Player-1-0000A003,"Pal-Realm-PvP-US",0x518,0x80000000`
	foe1   = `Player-1-0000E001,"Fone-Realm-PvP-US",0x548,0x80000000`
	foe2   = `Player-1-0000E002,"Ftwo-Realm-PvP-US",0x10548,0x80000000`
	foe3   = `Player-1-0000E003,"Fthree-Realm-PvP-US",0x548,0x80000000`
	boar   = `Creature-0-1-2-3-4-0000B001,"Boar",0xa48,0x80000000`
	imp    = `Pet-0-1-2-3-4-0000C001,"Imp",0x1148,0x80000000`
	totem  = `Creature-0-1-2-3-5-0000C002,"Totem",0x2148,0x80000000`
	nobody = `0000000000000000,nil,0x80000000,0x80000000`
)

const (
	vicGUID  = "Player-1-0000A001"
	foe1GUID = "Player-1-0000E001"
	foe2GUID = "Player-1-0000E002"
	foe3GUID = "Player-1-0000E003"
)

// at builds a UTC timestamp prefix for second s (may be fractional) of 16:00 and returns the line.
func at(sec float64, rest string) string {
	ms := int(sec*1000 + 0.5)
	return fmt.Sprintf("11/10/2026 16:%02d:%02d.%03d+0  %s", ms/60000, (ms/1000)%60, ms%1000, rest)
}

func dmg(sec float64, src, dst string) string {
	return at(sec, "SPELL_DAMAGE,"+src+","+dst+`,133,"Fireball",0x4,100,0,4,0,0,0,nil,nil,nil`)
}

// dmgAdv is a damage line with an advanced block describing infoGUID at (x,y) on map m.
func dmgAdv(sec float64, src, dst, infoGUID, owner string, x, y float64, m int) string {
	return at(sec, fmt.Sprintf("SPELL_DAMAGE,%s,%s,133,\"Fireball\",0x4,%s,%s,50,100,0,0,0,0,0,0,0,0,0,0,%.2f,%.2f,%d,0,60,100,0,4,0,0,0,nil,nil,nil", src, dst, infoGUID, owner, x, y, m))
}

func died(sec float64, dst string, flag int) string {
	return at(sec, fmt.Sprintf("UNIT_DIED,%s,%s,%d", nobody, dst, flag))
}

func pk(sec float64, src, dst string) string {
	return at(sec, "PARTY_KILL,"+src+","+dst+",0")
}

type fakeSink struct {
	deaths []Death
	seen   map[string]bool
}

func (f *fakeSink) RecordDeath(d Death) bool {
	if f.seen == nil {
		f.seen = map[string]bool{}
	}
	f.deaths = append(f.deaths, d)
	if f.seen[d.ID] {
		return false
	}
	f.seen[d.ID] = true
	return true
}

func run(t *testing.T, a *Analyzer, lines ...string) {
	t.Helper()
	for _, l := range lines {
		ev, err := parser.ParseLine(l, time.UTC)
		if err != nil {
			t.Fatalf("%v: %s", err, l)
		}
		a.Apply(ev)
	}
}

func newA() (*Analyzer, *fakeSink) {
	fs := &fakeSink{}
	a := NewAnalyzer(fs, Options{})
	a.SetFile("WoWCombatLog-test.txt")
	return a, fs
}

func ids(ps []Participant) string {
	var s []string
	for _, p := range ps {
		s = append(s, p.GUID)
	}
	return strings.Join(s, ",")
}

// ---- tests ----

func TestConstantsDocumented(t *testing.T) {
	if AssistWindow != 15*time.Second || KillingBlowWindow != 5*time.Second || DuplicateDeathWindow != 3*time.Second {
		t.Fatal("window constants changed; update README")
	}
}

func TestKillingBlowAndAssistFromDamageWindow(t *testing.T) {
	a, fs := newA()
	run(t, a,
		dmg(1, foe2, vic),  // 12 s before death: assist
		dmg(8, foe3, vic),  // 5 s before death: assist
		dmg(12, foe1, vic), // killing blow
		died(13, vic, 0),
	)
	if len(fs.deaths) != 1 {
		t.Fatalf("%d deaths", len(fs.deaths))
	}
	d := fs.deaths[0]
	if d.Killer == nil || d.Killer.GUID != foe1GUID || d.Killer.Raw != "Fone-Realm-PvP-US" {
		t.Fatalf("killer %+v", d.Killer)
	}
	if got := ids(d.Assists); got != foe2GUID+","+foe3GUID && got != foe3GUID+","+foe2GUID {
		t.Fatalf("assists %q", got)
	}
	if d.VictimGUID != vicGUID || d.VictimName != "Vic Realm PvP US" {
		t.Fatalf("victim %+v", d)
	}
	want := time.Date(2026, 11, 10, 16, 0, 13, 0, time.UTC)
	if !d.Time.Equal(want) {
		t.Fatalf("time %v", d.Time)
	}
	if st := a.Stats(); st.Deaths != 1 || st.Recorded != 1 {
		t.Fatalf("%+v", st)
	}
}

func TestAssistWindowBoundaries(t *testing.T) {
	a, fs := newA()
	run(t, a,
		dmg(0, foe2, vic),   // exactly 15 s before: inside
		dmg(0.5, foe3, vic), // 14.5 s: inside
		dmg(14, foe1, vic),  // killing blow
		died(15, vic, 0),
	)
	if got := len(fs.deaths[0].Assists); got != 2 {
		t.Fatalf("assists %d", got)
	}
	b, fs2 := newA()
	run(t, b,
		dmg(0, foe2, vic), // 15.001 s before: outside
		dmg(14, foe1, vic),
		died(15.001, vic, 0),
	)
	if len(fs2.deaths[0].Assists) != 0 || fs2.deaths[0].Killer == nil {
		t.Fatalf("%+v", fs2.deaths[0])
	}
}

func TestKillingBlowWindow(t *testing.T) {
	// last hit is older than KillingBlowWindow: nobody gets the killing blow, but it is still an assist.
	a, fs := newA()
	run(t, a, dmg(1, foe1, vic), died(7, vic, 0))
	d := fs.deaths[0]
	if d.Killer != nil || len(d.Assists) != 1 || d.Assists[0].GUID != foe1GUID {
		t.Fatalf("%+v", d)
	}
	// exactly at the window edge it still counts
	b, fs2 := newA()
	run(t, b, dmg(1, foe1, vic), died(6, vic, 0))
	if fs2.deaths[0].Killer == nil {
		t.Fatal("5 s is inside the window")
	}
}

func TestPartyKillIsAuthoritativeAndDedupesUnitDied(t *testing.T) {
	a, fs := newA()
	run(t, a,
		dmg(10, foe1, vic),
		dmg(11, foe2, vic), // last damage, but PARTY_KILL names foe1
		pk(11.5, foe1, vic),
		died(11.6, vic, 0),
	)
	if len(fs.deaths) != 1 {
		t.Fatalf("duplicate signals must be one death, got %d", len(fs.deaths))
	}
	d := fs.deaths[0]
	if d.Killer == nil || d.Killer.GUID != foe1GUID || ids(d.Assists) != foe2GUID {
		t.Fatalf("%+v", d)
	}
	// reverse order: UNIT_DIED first then PARTY_KILL
	b, fs2 := newA()
	run(t, b, dmg(10, foe1, vic), died(10.5, vic, 0), pk(10.51, foe1, vic))
	if len(fs2.deaths) != 1 {
		t.Fatalf("got %d", len(fs2.deaths))
	}
}

func TestPartyKillAloneIsADeath(t *testing.T) {
	a, fs := newA()
	run(t, a, pk(5, foe1, vic))
	if len(fs.deaths) != 1 || fs.deaths[0].Killer == nil || fs.deaths[0].Killer.GUID != foe1GUID {
		t.Fatalf("%+v", fs.deaths)
	}
}

func TestInstakillCountsLikePartyKill(t *testing.T) {
	a, fs := newA()
	run(t, a, at(5, "SPELL_INSTAKILL,"+foe1+","+vic+`,18788,"Kill",0x1`))
	if len(fs.deaths) != 1 || fs.deaths[0].Killer == nil || fs.deaths[0].Killer.GUID != foe1GUID {
		t.Fatalf("%+v", fs.deaths)
	}
}

func TestFeignDeathIsNotADeath(t *testing.T) {
	a, fs := newA()
	run(t, a, dmg(1, foe1, vic), died(2, vic, 1))
	if len(fs.deaths) != 0 {
		t.Fatalf("feign counted: %+v", fs.deaths)
	}
	if a.Stats().Feigns != 1 {
		t.Fatalf("%+v", a.Stats())
	}
	// a real death right after the feign is still counted (the feign did not consume the dedupe slot)
	run(t, a, died(3, vic, 0))
	if len(fs.deaths) != 1 {
		t.Fatalf("%d", len(fs.deaths))
	}
}

func TestOnlyFriendlyPlayersAreVictims(t *testing.T) {
	a, fs := newA()
	run(t, a,
		dmg(1, foe1, foe2), died(2, foe2, 0), // hostile player dies
		dmg(3, foe1, boar), died(4, boar, 0), // npc dies
		pk(5, vic, foe1), // we killed an enemy
		died(6, `Player-1-0000Z999,"Ghost-Realm-PvP-US",0x0,0x0`, 0), // unknown reaction
	)
	if len(fs.deaths) != 0 {
		t.Fatalf("%+v", fs.deaths)
	}
}

func TestMeIsAVictimToo(t *testing.T) {
	a, fs := newA()
	run(t, a, dmg(1, foe1, vic2), died(2, vic2, 0))
	if len(fs.deaths) != 1 || fs.deaths[0].Killer == nil {
		t.Fatal("0x511 (me) is friendly")
	}
}

func TestFriendlyKillerDoesNotCount(t *testing.T) {
	a, fs := newA()
	run(t, a,
		dmg(1, foe1, vic),
		dmg(2, pal, vic), // duel / own AoE: the friendly lands the last blow
		died(3, vic, 0),
	)
	d := fs.deaths[0]
	if d.Killer != nil || len(d.Assists) != 0 {
		t.Fatalf("friendly fire gives no bounty credit at all: %+v", d)
	}
	b, fs2 := newA()
	run(t, b, pk(3, pal, vic))
	if fs2.deaths[0].Killer != nil {
		t.Fatal("friendly PARTY_KILL source is not a bounty")
	}
}

func TestNPCKillerNoKillerCreditButAssistsKept(t *testing.T) {
	a, fs := newA()
	run(t, a,
		dmg(1, foe1, vic),
		dmg(2, boar, vic),
		died(3, vic, 0),
	)
	d := fs.deaths[0]
	if d.Killer != nil || ids(d.Assists) != foe1GUID {
		t.Fatalf("%+v", d)
	}
	b, fs2 := newA()
	run(t, b, dmg(1, boar, vic), died(2, vic, 0))
	if len(fs2.deaths) != 1 || fs2.deaths[0].Killer != nil || len(fs2.deaths[0].Assists) != 0 {
		t.Fatalf("pure npc death is reported with no credit: %+v", fs2.deaths)
	}
	// environmental damage has no source at all
	c, fs3 := newA()
	run(t, c, at(1, "ENVIRONMENTAL_DAMAGE,"+nobody+","+vic+`,Falling,100,0,1,0,0,0,nil,nil,nil`), died(1.5, vic, 0))
	if len(fs3.deaths) != 1 || fs3.deaths[0].Killer != nil {
		t.Fatalf("%+v", fs3.deaths)
	}
}

func TestPetCreditsHostileOwnerViaAdvancedBlock(t *testing.T) {
	a, fs := newA()
	run(t, a,
		dmg(0, foe1, boar), // learn that foe1 is a hostile player (flags)
		// pet hits the victim; the info block describes the pet and names its owner
		dmgAdv(5, imp, vic, "Pet-0-1-2-3-4-0000C001", foe1GUID, -100, 200, 1426),
		died(6, vic, 0),
	)
	d := fs.deaths[0]
	if d.Killer == nil || d.Killer.GUID != foe1GUID {
		t.Fatalf("owner must get the credit: %+v", d)
	}
}

func TestPetWithUnknownOrFriendlyOwnerGivesNoCredit(t *testing.T) {
	a, fs := newA()
	run(t, a,
		// owner never seen: not identifiable as hostile
		dmgAdv(5, imp, vic, "Pet-0-1-2-3-4-0000C001", "Player-1-0000E777", 0, 0, 1426),
		died(6, vic, 0),
	)
	if fs.deaths[0].Killer != nil {
		t.Fatalf("%+v", fs.deaths[0])
	}
	b, fs2 := newA()
	run(t, b,
		dmg(0, pal, boar),
		dmgAdv(5, imp, vic, "Pet-0-1-2-3-4-0000C001", "Player-1-0000A003", 0, 0, 1426),
		died(6, vic, 0),
	)
	if fs2.deaths[0].Killer != nil {
		t.Fatalf("friendly owner: %+v", fs2.deaths[0])
	}
}

func TestSummonedTotemCreditsOwner(t *testing.T) {
	a, fs := newA()
	run(t, a,
		at(0, "SPELL_SUMMON,"+foe3+","+totem+`,8190,"Totem",0x1`),
		dmg(5, totem, vic),
		died(6, vic, 0),
	)
	if k := fs.deaths[0].Killer; k == nil || k.GUID != foe3GUID {
		t.Fatalf("%+v", fs.deaths[0])
	}
}

func TestPetOwnerAssist(t *testing.T) {
	a, fs := newA()
	run(t, a,
		dmg(0, foe2, boar),
		at(1, "SPELL_SUMMON,"+foe2+","+imp+`,688,"Imp",0x1`),
		dmg(4, imp, vic),
		dmg(5, foe1, vic),
		died(6, vic, 0),
	)
	d := fs.deaths[0]
	if d.Killer == nil || d.Killer.GUID != foe1GUID || ids(d.Assists) != foe2GUID {
		t.Fatalf("%+v", d)
	}
}

func TestSameAttackerCountedOnceAndNotAssistToSelf(t *testing.T) {
	a, fs := newA()
	run(t, a, dmg(1, foe1, vic), dmg(2, foe1, vic), dmg(3, foe1, vic), died(4, vic, 0))
	d := fs.deaths[0]
	if d.Killer == nil || len(d.Assists) != 0 {
		t.Fatalf("%+v", d)
	}
}

func TestTwoDeathsOfSameVictimOutsideDedupeWindow(t *testing.T) {
	a, fs := newA()
	run(t, a, dmg(1, foe1, vic), died(2, vic, 0), dmg(6, foe2, vic), died(7, vic, 0))
	if len(fs.deaths) != 2 || fs.deaths[0].ID == fs.deaths[1].ID {
		t.Fatalf("%+v", fs.deaths)
	}
	if fs.deaths[1].Killer.GUID != foe2GUID || len(fs.deaths[1].Assists) != 0 {
		t.Fatalf("hits before the first death must not leak into the second: %+v", fs.deaths[1])
	}
}

func TestDeathCarriesZoneMapAndPosition(t *testing.T) {
	a, fs := newA()
	run(t, a,
		at(0, `ZONE_CHANGE,0,"Dun Morogh",0`),
		at(0, `MAP_CHANGE,1426,"Dun Morogh",-3877.08,-7160.41,1802.08,-3122.91`),
		dmg(0.5, foe1, boar),
		dmgAdv(5, foe1, vic, foe1GUID, "0000000000000000", -5300.5, -200.25, 1426),
		died(6, vic, 0),
	)
	d := fs.deaths[0]
	if d.Zone != "Dun Morogh" || d.MapID != 1426 || !d.HasPos || d.X != -5300.5 || d.Y != -200.25 {
		t.Fatalf("%+v", d)
	}
	// no position known: HasPos false, no crash
	b, fs2 := newA()
	run(t, b, dmg(5, foe1, vic), died(6, vic, 0))
	if fs2.deaths[0].HasPos {
		t.Fatal("no position expected")
	}
}

func TestMapNameWinsOverZoneForItsMap(t *testing.T) {
	a, fs := newA()
	run(t, a,
		at(0, `ZONE_CHANGE,0,"Old Zone",0`),
		at(0, `MAP_CHANGE,1453,"Stormwind City",-1,1,-1,1`),
		dmgAdv(5, foe1, vic, vicGUID, "0000000000000000", 1, 2, 1453),
		died(6, vic, 0),
	)
	if d := fs.deaths[0]; d.MapID != 1453 || d.Zone != "Stormwind City" {
		t.Fatalf("%+v", d)
	}
}

func TestDeathIDStableAndFileSpecific(t *testing.T) {
	lines := []string{dmg(1, foe1, vic), died(2, vic, 0)}
	a, fs := newA()
	run(t, a, lines...)
	b, fs2 := newA()
	run(t, b, lines...)
	if fs.deaths[0].ID == "" || fs.deaths[0].ID != fs2.deaths[0].ID {
		t.Fatalf("id must be stable: %q %q", fs.deaths[0].ID, fs2.deaths[0].ID)
	}
	c, fs3 := newA()
	c.SetFile("WoWCombatLog-other.txt")
	run(t, c, lines...)
	if fs3.deaths[0].ID == fs.deaths[0].ID {
		t.Fatal("id must include the log file")
	}
	// a different victim or time gives a different id
	d, fs4 := newA()
	run(t, d, died(3, vic, 0))
	if fs4.deaths[0].ID == fs.deaths[0].ID {
		t.Fatal("id must include the time")
	}
}

func TestReAnalysisThroughStoreNeverDoubleCounts(t *testing.T) {
	st := New("")
	lines := []string{dmg(1, foe2, vic), dmg(2, foe1, vic), died(3, vic, 0)}
	for pass := 0; pass < 3; pass++ {
		a := NewAnalyzer(st, Options{})
		a.SetFile("WoWCombatLog-x.txt") // a fresh analyzer per pass, shared store
		run(t, a, lines...)
		if pass > 0 && a.Stats().Recorded != 0 {
			t.Fatalf("pass %d re-recorded", pass)
		}
	}
	r := st.Leaderboard(5)
	if len(r) != 2 || r[0].KillingBlows != 1 || r[1].Assists != 1 {
		t.Fatalf("%+v", r)
	}
}

func TestSetFileResetsWindowsOnlyOnChange(t *testing.T) {
	a, fs := newA()
	run(t, a, dmg(1, foe1, vic))
	a.SetFile("WoWCombatLog-test.txt") // same name: keep
	run(t, a, died(2, vic, 0))
	if fs.deaths[0].Killer == nil {
		t.Fatal("window was reset by a no-op SetFile")
	}
	b, fs2 := newA()
	run(t, b, dmg(1, foe1, vic))
	b.SetFile("WoWCombatLog-next.txt")
	run(t, b, died(2, vic, 0))
	if fs2.deaths[0].Killer != nil || len(fs2.deaths[0].Assists) != 0 {
		t.Fatal("window must not leak across files")
	}
}

func TestWindowEvictionIsBounded(t *testing.T) {
	a, _ := newA()
	// many hits on one victim: capped
	var lines []string
	for i := 0; i < maxHitsPerVictim*3; i++ {
		lines = append(lines, dmg(float64(i)*0.001, foe1, vic))
	}
	run(t, a, lines...)
	if n := a.windowSize(vicGUID); n > maxHitsPerVictim {
		t.Fatalf("hits %d", n)
	}
	// hits older than the assist window are dropped as time advances
	run(t, a, dmg(60, foe2, vic))
	if n := a.windowSize(vicGUID); n != 1 {
		t.Fatalf("old hits kept: %d", n)
	}
	// many victims: capped, least recently touched dropped
	b, _ := newA()
	for i := 0; i < maxTrackedVictims+50; i++ {
		v := fmt.Sprintf(`Player-1-V%05d,"V%d-Realm-PvP-US",0x518,0x80000000`, i, i)
		run(t, b, dmg(float64(i)*0.01, foe1, v))
	}
	if n := b.victimCount(); n > maxTrackedVictims {
		t.Fatalf("victims %d", n)
	}
	if b.windowSize("Player-1-V00000") != 0 {
		t.Fatal("oldest victim should be evicted")
	}
	if b.windowSize(fmt.Sprintf("Player-1-V%05d", maxTrackedVictims+49)) != 1 {
		t.Fatal("newest victim kept")
	}
}

func TestKnownPlayerAndOwnerMapsAreBounded(t *testing.T) {
	a, _ := newA()
	for i := 0; i < maxKnown*2+10; i++ {
		p := fmt.Sprintf(`Player-1-K%06d,"K%d-Realm-PvP-US",0x548,0x80000000`, i, i)
		run(t, a, dmg(float64(i)*0.0001, p, boar))
	}
	if a.knownCount() > maxKnown {
		t.Fatalf("known %d", a.knownCount())
	}
}

func TestDisplayNamesUseRealmSuffix(t *testing.T) {
	fs := &fakeSink{}
	a := NewAnalyzer(fs, Options{RealmSuffix: "-Realm-PvP-US"})
	a.SetFile("f")
	run(t, a, dmg(1, foe1, vic), died(2, vic, 0))
	d := fs.deaths[0]
	if d.Killer.Display != "Fone" || d.Killer.Raw != "Fone-Realm-PvP-US" || d.VictimName != "Vic" {
		t.Fatalf("%+v", d)
	}
}

func TestAnalyzerIgnoresGarbageAndUnrelatedEvents(t *testing.T) {
	a, fs := newA()
	run(t, a,
		at(1, `COMBAT_LOG_VERSION,22,ADVANCED_LOG_ENABLED,1,BUILD_VERSION,1.60.1,PROJECT_ID,18`),
		at(1, "SPELL_HEAL,"+foe1+","+vic+`,1,"Heal",0x1,5,0,0,nil`),
		at(2, "UNIT_DIED,"+nobody+","+nobody+",0"), // no victim
	)
	if len(fs.deaths) != 0 {
		t.Fatalf("%+v", fs.deaths)
	}
}

func TestStatsFeignAndDuplicates(t *testing.T) {
	st := New("")
	a := NewAnalyzer(st, Options{})
	a.SetFile("f")
	run(t, a, dmg(1, foe1, vic), died(2, vic, 0))
	b := NewAnalyzer(st, Options{})
	b.SetFile("f")
	run(t, b, dmg(1, foe1, vic), died(2, vic, 0))
	if s := b.Stats(); s.Deaths != 1 || s.Recorded != 0 || s.Duplicates != 1 {
		t.Fatalf("%+v", s)
	}
}

// ---- white-box helpers ----

func (a *Analyzer) windowSize(victim string) int {
	a.mu.Lock()
	defer a.mu.Unlock()
	if w := a.wins[victim]; w != nil {
		return len(w.hits)
	}
	return 0
}

func (a *Analyzer) victimCount() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return len(a.wins)
}

func (a *Analyzer) knownCount() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return len(a.known)
}
