package bountyx

import (
	"context"
	"fmt"
	"math"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"radarlive/internal/core"
	"radarlive/internal/domain"
)

// A hostile stream of one million distinct killers and victims must not grow the analyzer without bound.
func TestMillionDistinctGUIDsStayBounded(t *testing.T) {
	if testing.Short() {
		t.Skip("1M events")
	}
	a := NewAnalyzer(Options{})
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	for i := 0; i < 1_000_000; i++ {
		killer := foe(fmt.Sprintf("Player-1-K%07X", i), "Killer-PvP-US")
		a.Feed(hitOn(float64(i)*20, killer, fmt.Sprintf("Player-1-V%07X", i)))
		a.Feed(partyKill(float64(i)*20+1, killer, fmt.Sprintf("Player-1-V%07X", i)))
		if i%100_000 == 0 {
			checkBounded(t, a)
		}
	}
	checkBounded(t, a)
	part := a.Flush()
	if len(part.Credits) > maxCredits || len(part.Deaths) > domain.PingMaxDeaths {
		t.Fatalf("flush carries %d credits and %d deaths", len(part.Credits), len(part.Deaths))
	}
	runtime.GC()
	runtime.ReadMemStats(&after)
	if grown := int64(after.HeapAlloc) - int64(before.HeapAlloc); grown > 64<<20 {
		t.Fatalf("heap grew %d MiB", grown>>20)
	}
}

func checkBounded(t *testing.T, a *Analyzer) {
	t.Helper()
	if len(a.credits) > maxCredits || len(a.deaths) > domain.PingMaxDeaths || len(a.known) > maxKnownPlayers ||
		len(a.wins) > maxTrackedVictims || len(a.owners) > maxOwners || len(a.last) > maxLastDeaths {
		t.Fatalf("unbounded: credits=%d deaths=%d known=%d wins=%d owners=%d last=%d",
			len(a.credits), len(a.deaths), len(a.known), len(a.wins), len(a.owners), len(a.last))
	}
}

func TestOwnersAndPetsAreBounded(t *testing.T) {
	a := NewAnalyzer(Options{})
	for i := 0; i < 100_000; i++ {
		a.Feed(domain.Event{TimeMs: at(float64(i)), Name: "SPELL_SUMMON", Src: foe(fmt.Sprintf("Player-1-O%06d", i), "O-PvP-US"),
			Dst: eu(fmt.Sprintf("Creature-0-%d", i), "Pet", 0x1148)})
	}
	checkBounded(t, a)
}

func TestForgedCreditsAreClampedToSaneCaps(t *testing.T) {
	m := New()
	m.runID = "r"
	m.host = core.NewHost()
	_ = m.host.Config.Declare(core.Setting{Key: SettingRuleset, Kind: core.KindString})
	credits := map[string]Credit{
		"Player-1-A": {Name: "A", KB: math.MaxInt64, Assists: -7},
		"Player-1-B": {Name: "B", KB: 3, Assists: 1 << 50},
	}
	pings := m.pings("s", Part{Credits: credits})
	if len(pings) != 1 {
		t.Fatalf("%d pings", len(pings))
	}
	if err := pings[0].Validate(); err != nil {
		t.Fatalf("clamped ping is invalid: %v", err)
	}
	for _, c := range pings[0].Contacts {
		if c.D.KB < 0 || c.D.KB > domain.DeltaMaxCount || c.D.Assists < 0 || c.D.Assists > domain.DeltaMaxCount {
			t.Fatalf("%s: %+v", c.GUID, c.D)
		}
	}
}

func TestOneKillerManyDeathsIsOneContactWithSaneCount(t *testing.T) {
	a := NewAnalyzer(Options{})
	killer := foe(foeA, "Aaa-PvP-US")
	for i := 0; i < 2000; i++ {
		a.Feed(partyKill(float64(i)*10, killer, fmt.Sprintf("Player-1-V%04d", i)))
	}
	part := a.Flush()
	if len(part.Credits) != 1 || part.Credits[foeA].KB != 2000 {
		t.Fatalf("%+v", part.Credits)
	}
	if len(part.Deaths) != domain.PingMaxDeaths {
		t.Fatalf("deaths %d", len(part.Deaths))
	}
}

func TestPoisonedNamesAreSanitisedInPings(t *testing.T) {
	host := core.NewHost()
	m := New()
	if err := host.Register(m); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var got []domain.Ping
	host.Sinks.Add(sinkFunc(func(p domain.Ping) { mu.Lock(); got = append(got, p); mu.Unlock() }))
	evil := "Bo\nb‮\x00" + strings.Repeat("y", 400) + "-PvP-US"
	ev := partyKill(0, foe(foeA, evil), victim)
	if err := host.Parsers.Feed(context.Background(), ev); err != nil {
		t.Fatal(err)
	}
	if err := m.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(got) == 0 {
		t.Fatal("nothing published")
	}
	for _, p := range got {
		if err := p.Validate(); err != nil {
			t.Fatalf("poisoned ping invalid: %v", err)
		}
		for _, c := range p.Contacts {
			if strings.ContainsAny(c.Name+c.Raw, "\n\r\x00‮") {
				t.Fatalf("control text survived: %q %q", c.Name, c.Raw)
			}
		}
	}
}

type sinkFunc func(domain.Ping)

func (sinkFunc) Name() string { return "capture" }
func (f sinkFunc) Accept(_ context.Context, p domain.Ping) error {
	f(p)
	return nil
}

func TestIdleSourcesExpire(t *testing.T) {
	host := core.NewHost()
	m := New()
	if err := host.Register(m); err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1_800_000_000, 0)
	m.now = func() time.Time { return now }
	ctx := context.Background()
	for i := 0; i < maxSources; i++ {
		ev := partyKill(0, foe(foeA, "A"), victim)
		ev.Source = fmt.Sprintf("src%d", i)
		if err := host.Parsers.Feed(ctx, ev); err != nil {
			t.Fatal(err)
		}
	}
	extra := partyKill(0, foe(foeA, "A"), victim)
	extra.Source = "one-too-many"
	if err := host.Parsers.Feed(ctx, extra); err == nil {
		t.Fatal("source cap did not hold")
	}
	_ = m.Flush(ctx)
	now = now.Add(idleExpiry + time.Minute)
	_ = m.Flush(ctx)
	if err := host.Parsers.Feed(ctx, extra); err != nil {
		t.Fatalf("idle sources were not expired: %v", err)
	}
}

func TestOnePoisonedDeathDoesNotLoseTheRest(t *testing.T) {
	host := core.NewHost()
	m := New()
	if err := host.Register(m); err != nil {
		t.Fatal(err)
	}
	var got []domain.Ping
	host.Sinks.Add(sinkFunc(func(p domain.Ping) { got = append(got, p) }))
	ctx := context.Background()
	_ = host.Parsers.Feed(ctx, partyKill(0, foe(foeA, "Aaa-PvP-US"), "Player-1-bad!!victim"))
	_ = host.Parsers.Feed(ctx, partyKill(20, foe(foeA, "Aaa-PvP-US"), victim))
	if err := m.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || len(got[0].Deaths) != 1 || got[0].Deaths[0].Victim != victim {
		t.Fatalf("%+v", got)
	}
	if err := got[0].Validate(); err != nil {
		t.Fatal(err)
	}
}
