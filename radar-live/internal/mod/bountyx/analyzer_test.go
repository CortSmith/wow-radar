package bountyx

import (
	"fmt"
	"testing"

	"radarlive/internal/domain"
)

const (
	victim  = "Player-1-V1"
	foeA    = "Player-1-A1"
	foeB    = "Player-1-B1"
	friend  = "Player-1-F1"
	npc     = "Creature-0-9"
	baseMs  = int64(1_800_000_000_000)
	hostile = uint32(0x548)
	friendF = uint32(0x514)
)

func at(sec float64) int64 { return baseMs + int64(sec*1000) }

func eu(guid, raw string, flags uint32) domain.EventUnit {
	return domain.EventUnit{GUID: guid, Raw: raw, Flags: flags}
}

func hitOn(sec float64, src domain.EventUnit, dst string) domain.Event {
	return domain.Event{Source: "s1", TimeMs: at(sec), Name: "SPELL_DAMAGE", IsDamage: true, Src: src,
		Dst: eu(dst, "Vic-PvP-US", friendF), Amount: 10, HasAmount: true}
}

func died(sec float64, dst string) domain.Event {
	return domain.Event{Source: "s1", TimeMs: at(sec), Name: "UNIT_DIED", Dst: eu(dst, "Vic-PvP-US", friendF)}
}

func partyKill(sec float64, killer domain.EventUnit, dst string) domain.Event {
	return domain.Event{Source: "s1", TimeMs: at(sec), Name: "PARTY_KILL", Src: killer, Dst: eu(dst, "Vic-PvP-US", friendF)}
}

func run(t *testing.T, events ...domain.Event) (Part, *Analyzer) {
	t.Helper()
	a := NewAnalyzer(Options{})
	for _, ev := range events {
		a.Feed(ev)
	}
	return a.Flush(), a
}

func foe(guid, raw string) domain.EventUnit { return eu(guid, raw, hostile) }

func TestKillingBlowFromLastHit(t *testing.T) {
	p, _ := run(t, hitOn(0, foe(foeA, "Aaa-PvP-US"), victim), hitOn(2, foe(foeB, "Bbb-PvP-US"), victim), died(4, victim))
	if len(p.Deaths) != 1 {
		t.Fatalf("deaths=%d", len(p.Deaths))
	}
	d := p.Deaths[0]
	if d.Killer != foeB || len(d.Assists) != 1 || d.Assists[0] != foeA || d.Victim != victim || d.T != at(4) {
		t.Fatalf("death: %+v", d)
	}
	if p.Credits[foeB].KB != 1 || p.Credits[foeB].Assists != 0 || p.Credits[foeA].Assists != 1 || p.Credits[foeA].KB != 0 {
		t.Fatalf("credits: %+v", p.Credits)
	}
}

func TestKillingBlowWindowIsFiveSeconds(t *testing.T) {
	cases := []struct {
		name   string
		gap    float64
		wantKB bool
	}{{"5.0 s counts", 5, true}, {"5.1 s too old", 5.1, false}, {"1 s", 1, true}}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p, _ := run(t, hitOn(0, foe(foeA, "A"), victim), died(c.gap, victim))
			got := p.Credits[foeA]
			if (got.KB == 1) != c.wantKB || (!c.wantKB && got.Assists != 1) {
				t.Fatalf("credit: %+v", got)
			}
		})
	}
}

func TestAssistWindowIsFifteenSeconds(t *testing.T) {
	p, _ := run(t, hitOn(0, foe(foeA, "A"), victim), hitOn(10, foe(foeB, "B"), victim), died(15, victim))
	if p.Credits[foeA].Assists != 1 || p.Credits[foeB].KB != 1 {
		t.Fatalf("15 s window: %+v", p.Credits)
	}
	p, _ = run(t, hitOn(0, foe(foeA, "A"), victim), hitOn(10, foe(foeB, "B"), victim), died(15.5, victim))
	if _, ok := p.Credits[foeA]; ok {
		t.Fatalf("hit older than 15 s must not assist: %+v", p.Credits)
	}
}

func TestPartyKillNamesTheKiller(t *testing.T) {
	p, _ := run(t, hitOn(0, foe(foeA, "A"), victim), hitOn(9, foe(foeB, "B"), victim), partyKill(10, foe(foeA, "A"), victim))
	d := p.Deaths[0]
	if d.Killer != foeA || len(d.Assists) != 1 || d.Assists[0] != foeB {
		t.Fatalf("death: %+v", d)
	}
}

func TestInstakill(t *testing.T) {
	ev := partyKill(1, foe(foeA, "A"), victim)
	ev.Name = "SPELL_INSTAKILL"
	p, _ := run(t, ev)
	if p.Credits[foeA].KB != 1 {
		t.Fatalf("credits: %+v", p.Credits)
	}
}

func TestDuplicateSignalsWithinThreeSecondsAreOneDeath(t *testing.T) {
	p, a := run(t, hitOn(0, foe(foeA, "A"), victim), died(1, victim), partyKill(3.5, foe(foeA, "A"), victim))
	if len(p.Deaths) != 1 || p.Credits[foeA].KB != 1 {
		t.Fatalf("deaths=%d credits=%+v", len(p.Deaths), p.Credits)
	}
	if a.Stats().Duplicates != 1 {
		t.Fatalf("stats: %+v", a.Stats())
	}
	p, _ = run(t, hitOn(0, foe(foeA, "A"), victim), died(1, victim), died(4.1, victim))
	if len(p.Deaths) != 2 {
		t.Fatalf("deaths 3.1 s apart are two: %d", len(p.Deaths))
	}
}

func TestFeignDeathIgnored(t *testing.T) {
	feign := died(1, victim)
	feign.Unconscious = true
	p, a := run(t, hitOn(0, foe(foeA, "A"), victim), feign)
	if len(p.Deaths) != 0 || len(p.Credits) != 0 || a.Stats().Feigns != 1 {
		t.Fatalf("feign counted: %+v %+v", p, a.Stats())
	}
}

func TestOnlyFriendlyPlayersAreVictims(t *testing.T) {
	hostileDies := domain.Event{TimeMs: at(1), Name: "UNIT_DIED", Dst: eu(foeA, "A", hostile)}
	npcDies := domain.Event{TimeMs: at(1), Name: "UNIT_DIED", Dst: eu(npc, "Boar", friendF)}
	p, _ := run(t, hostileDies, npcDies)
	if len(p.Deaths) != 0 {
		t.Fatalf("deaths=%d", len(p.Deaths))
	}
}

func TestNPCKillCountsAsNPCDeath(t *testing.T) {
	p, a := run(t, hitOn(0, eu(npc, "Boar", 0x148), victim), died(1, victim))
	if len(p.Deaths) != 1 || p.Deaths[0].Killer != "" || len(p.Credits) != 0 {
		t.Fatalf("npc kill: %+v", p)
	}
	if st := a.Stats(); st.Deaths != 1 || st.NPCDeaths != 1 {
		t.Fatalf("stats: %+v", st)
	}
}

func TestUnattributedDeathHasNoCredit(t *testing.T) {
	p, _ := run(t, died(1, victim))
	if len(p.Deaths) != 1 || len(p.Credits) != 0 {
		t.Fatalf("%+v", p)
	}
}

func TestFriendlyFireGivesNoCredit(t *testing.T) {
	p, _ := run(t, hitOn(0, foe(foeA, "A"), victim), hitOn(1, eu(friend, "F", friendF), victim), died(2, victim))
	if len(p.Credits) != 0 || p.Deaths[0].Killer != "" || len(p.Deaths[0].Assists) != 0 {
		t.Fatalf("friendly final blow must void credit: %+v", p)
	}
}

func TestPetCreditsItsOwnerViaSummon(t *testing.T) {
	owner := foe(foeA, "Own-PvP-US")
	summon := domain.Event{TimeMs: at(0), Name: "SPELL_SUMMON", Src: owner, Dst: eu("Pet-0-5", "Wolf", 0x1148)}
	pet := hitOn(1, eu("Pet-0-5", "Wolf", 0x1148), victim)
	p, _ := run(t, summon, pet, died(2, victim))
	if p.Credits[foeA].KB != 1 || p.Credits[foeA].Raw != "Own-PvP-US" {
		t.Fatalf("pet kill: %+v", p.Credits)
	}
}

func TestPetCreditsItsOwnerViaAdvancedBlock(t *testing.T) {
	owner := foe(foeA, "Own")
	know := hitOn(0, owner, "Player-1-OTHER")
	pet := hitOn(1, eu("Creature-0-7", "Totem", 0x1148), victim)
	pet.Info = &domain.EventInfo{GUID: "Creature-0-7", Owner: foeA}
	p, _ := run(t, know, pet, died(2, victim))
	if p.Credits[foeA].KB != 1 {
		t.Fatalf("totem kill: %+v", p.Credits)
	}
}

func TestPetOfUnknownOwnerIsNPC(t *testing.T) {
	pet := hitOn(1, eu("Pet-0-5", "Wolf", 0x1148), victim)
	pet.Info = &domain.EventInfo{GUID: "Pet-0-5", Owner: foeB}
	p, _ := run(t, pet, died(2, victim))
	if len(p.Credits) != 0 {
		t.Fatalf("owner never seen as hostile: %+v", p.Credits)
	}
}

func TestPetOfFriendlyOwnerIsFriendlyFire(t *testing.T) {
	know := hitOn(0, eu(friend, "F", friendF), "Player-1-OTHER")
	pet := hitOn(1, eu("Pet-0-5", "Wolf", 0x1112), victim)
	pet.Info = &domain.EventInfo{GUID: "Pet-0-5", Owner: friend}
	p, _ := run(t, hitOn(0, foe(foeA, "A"), victim), know, pet, died(2, victim))
	if len(p.Credits) != 0 {
		t.Fatalf("friendly pet kill must void credit: %+v", p.Credits)
	}
}

func TestFlushReturnsDeltasOnce(t *testing.T) {
	a := NewAnalyzer(Options{})
	a.Feed(hitOn(0, foe(foeA, "Aaa-PvP-US"), victim))
	a.Feed(died(1, victim))
	first := a.Flush()
	if first.Credits[foeA].KB != 1 || first.Credits[foeA].LastMs != at(1) || len(first.Deaths) != 1 {
		t.Fatalf("first: %+v", first)
	}
	if second := a.Flush(); len(second.Deaths) != 0 || len(second.Credits) != 0 {
		t.Fatalf("second flush must be empty: %+v", second)
	}
	a.Feed(hitOn(20, foe(foeA, "Aaa-PvP-US"), victim))
	a.Feed(died(21, victim))
	if third := a.Flush(); third.Credits[foeA].KB != 1 {
		t.Fatalf("third: %+v", third)
	}
}

func TestCreditCarriesDisplayName(t *testing.T) {
	a := NewAnalyzer(Options{RealmSuffix: "-PvP-US"})
	a.Feed(hitOn(0, foe(foeA, "Aa-Bb-PvP-US"), victim))
	a.Feed(died(1, victim))
	if c := a.Flush().Credits[foeA]; c.Name != "Aa Bb" || c.Raw != "Aa-Bb-PvP-US" {
		t.Fatalf("%+v", c)
	}
}

func TestDeathIDIsStable(t *testing.T) {
	a := deathID(victim, at(4), "s1")
	if len(a) != 20 {
		t.Fatalf("id length %d", len(a))
	}
	if a != deathID(victim, at(4), "s1") {
		t.Fatal("not deterministic")
	}
	for name, other := range map[string]string{"victim": deathID("Player-1-X", at(4), "s1"), "time": deathID(victim, at(5), "s1"), "source": deathID(victim, at(4), "s2")} {
		if other == a {
			t.Errorf("%s change did not change the id", name)
		}
	}
	p, _ := run(t, died(4, victim))
	if p.Deaths[0].ID != a {
		t.Fatalf("analyzer id %s want %s", p.Deaths[0].ID, a)
	}
}

func TestHitRingIsBounded(t *testing.T) {
	a := NewAnalyzer(Options{})
	for i := range maxHitsPerVictim * 3 {
		a.Feed(hitOn(float64(i)*0.01, foe(fmt.Sprintf("Player-1-H%03d", i), "H"), victim))
	}
	if n := len(a.wins[victim].hits); n > maxHitsPerVictim {
		t.Fatalf("hits=%d", n)
	}
}

func TestVictimTableIsBounded(t *testing.T) {
	a := NewAnalyzer(Options{})
	for i := range maxTrackedVictims * 2 {
		a.Feed(hitOn(float64(i)*0.01, foe(foeA, "A"), fmt.Sprintf("Player-1-V%04d", i)))
		if len(a.wins) > maxTrackedVictims {
			t.Fatalf("victims=%d", len(a.wins))
		}
	}
}

func TestIgnoresIrrelevantEvents(t *testing.T) {
	a := NewAnalyzer(Options{})
	a.Feed(domain.Event{Name: "ZONE_CHANGE", Zone: "Z", TimeMs: at(0)})
	a.Feed(domain.Event{Name: "SPELL_HEAL", TimeMs: at(0), Src: foe(foeA, "A"), Dst: eu(victim, "V", friendF)})
	a.Feed(hitOn(0, foe(foeA, "A"), npc)) // npc victim: not tracked
	if len(a.wins) != 0 {
		t.Fatalf("wins=%d", len(a.wins))
	}
}

func TestPendingDeathsAreBounded(t *testing.T) {
	a := NewAnalyzer(Options{})
	for i := range domain.PingMaxDeaths + 20 {
		a.Feed(died(float64(i)*10, fmt.Sprintf("Player-1-V%05d", i)))
	}
	p := a.Flush()
	if len(p.Deaths) != domain.PingMaxDeaths || p.Deaths[len(p.Deaths)-1].T != at(float64(domain.PingMaxDeaths+19)*10) {
		t.Fatalf("deaths=%d", len(p.Deaths))
	}
}

func TestPartReportsRuleset(t *testing.T) {
	ev := died(1, victim)
	ev.Ruleset = domain.RulesetRPPvP
	p, _ := run(t, ev)
	if p.Ruleset != domain.RulesetRPPvP {
		t.Fatalf("rs=%q", p.Ruleset)
	}
}

func TestOutputPassesPingValidation(t *testing.T) {
	p, _ := run(t, hitOn(0, foe(foeA, "A"), victim), died(1, victim))
	ping := domain.Ping{RS: domain.RulesetPvP, BatchID: "b", Deaths: p.Deaths}
	if err := ping.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestCreditNamesFollowSuffixDetection(t *testing.T) {
	a := NewAnalyzer(Options{})
	a.Feed(hitOn(0, foe(foeA, "Aaa-PvP-US"), victim))
	a.Feed(died(1, victim))
	for i, n := range []string{"Bbb", "Ccc", "Ddd", "Eee", "Fff"} {
		a.Feed(hitOn(10, foe(fmt.Sprintf("Player-1-X%d", i), n+"-PvP-US"), victim))
	}
	a.Feed(hitOn(11, foe("Player-1-Z", "Zed-PvE-EU"), victim))
	p := a.Flush()
	if c := p.Credits[foeA]; c.Name != "Aaa" || c.Raw != "Aaa-PvP-US" {
		t.Fatalf("%q %q", c.Name, c.Raw)
	}
}

func TestExplicitRealmSuffixOption(t *testing.T) {
	a := NewAnalyzer(Options{RealmSuffix: "-PvP-US"})
	a.Feed(hitOn(0, foe(foeA, "Aaa-PvP-US"), victim))
	a.Feed(died(1, victim))
	if c := a.Flush().Credits[foeA]; c.Name != "Aaa" {
		t.Fatalf("%q", c.Name)
	}
}
