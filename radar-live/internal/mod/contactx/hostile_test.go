package contactx

import (
	"context"
	"fmt"
	"math"
	"runtime"
	"strings"
	"testing"
	"time"

	"radarlive/internal/domain"
)

// A hostile stream of one million distinct player GUIDs must not grow the extractor past its cap.
func TestMillionDistinctGUIDsStayBounded(t *testing.T) {
	if testing.Short() {
		t.Skip("1M events")
	}
	x := newX(t, Options{})
	x.Feed(meEvent(0, 1, 1))
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	for i := 0; i < 1_000_000; i++ {
		x.Feed(domain.Event{TimeMs: at(1 + i/1000), Name: "SPELL_DAMAGE", IsDamage: true, Amount: 5, HasAmount: true,
			Src: eu(fmt.Sprintf("Player-1-%07X", i), "Hostile-PvP-US", 0x548), Dst: eu(meGUID, "Ann-PvP-US", 0x511)})
		if i%50_000 == 0 && x.Tracked() > DefaultMaxContacts {
			t.Fatalf("tracked %d after %d events", x.Tracked(), i)
		}
	}
	if x.Tracked() > DefaultMaxContacts {
		t.Fatalf("tracked %d", x.Tracked())
	}
	part := x.Flush(time.Time{})
	if len(part.Contacts) > DefaultMaxContacts {
		t.Fatalf("one flush carries %d contacts", len(part.Contacts))
	}
	runtime.GC()
	runtime.ReadMemStats(&after)
	if grown := int64(after.HeapAlloc) - int64(before.HeapAlloc); grown > 64<<20 {
		t.Fatalf("heap grew %d MiB", grown>>20)
	}
}

func TestHostileNumbersNeverMakeNegativeOrHugeDeltas(t *testing.T) {
	x := newX(t, Options{})
	x.Feed(meEvent(0, 1, 1))
	foe := eu(foeGUID, "Bob-PvP-US", 0x548)
	for _, amount := range []float64{-5, math.NaN(), math.Inf(1), math.Inf(-1), 0, 1e300} {
		x.Feed(domain.Event{TimeMs: at(1), Name: "SPELL_DAMAGE", IsDamage: true, Amount: amount, HasAmount: true,
			Src: foe, Dst: eu(meGUID, "Ann-PvP-US", 0x511)})
	}
	for _, hp := range []float64{-100, math.NaN(), math.Inf(1)} {
		x.Feed(domain.Event{TimeMs: at(2), Name: "SPELL_DAMAGE", Src: foe, Dst: eu(meGUID, "Ann-PvP-US", 0x511),
			Info: &domain.EventInfo{GUID: foeGUID, HP: hp, MaxHP: hp, HasHP: true, Level: 70}})
	}
	part := x.Flush(time.Time{})
	_, m, _ := newHost(t)
	for _, ping := range m.pings("s", part) {
		if err := ping.Validate(); err != nil {
			t.Fatalf("hostile numbers made an invalid ping: %v", err)
		}
		for _, c := range ping.Contacts {
			if c.D.DmgTotal < 0 || c.D.HPSum < 0 || c.D.DmgTotal > domain.DeltaMaxValue || c.D.HPSum > domain.DeltaMaxValue {
				t.Fatalf("%s: %+v", c.GUID, c.D)
			}
		}
	}
}

func TestPingDeltasAreClampedToSaneCaps(t *testing.T) {
	_, m, _ := newHost(t)
	part := Part{Contacts: []domain.PingContact{{
		Contact: domain.Contact{GUID: "Player-1-Z"},
		D: domain.Deltas{Events: math.MaxInt64, SrcEvents: -4, DmgTotal: math.Inf(1), DmgHits: 1 << 60, DmgEvents: 1 << 40,
			HPSum: math.NaN(), HPSamples: -1, KB: 1 << 62, Assists: math.MinInt64},
	}}}
	pings := m.pings("s", part)
	if len(pings) != 1 {
		t.Fatalf("%d pings", len(pings))
	}
	if err := pings[0].Validate(); err != nil {
		t.Fatalf("clamped ping still invalid: %v", err)
	}
	d := pings[0].Contacts[0].D
	if d.Events != domain.DeltaMaxCount || d.SrcEvents != 0 || d.HPSamples != 0 || d.Assists != 0 || d.DmgTotal != domain.DeltaMaxValue || d.HPSum != 0 {
		t.Fatalf("%+v", d)
	}
}

func TestDuplicateGUIDsInOneFlushAreMerged(t *testing.T) {
	x := newX(t, Options{})
	for i := 0; i < 500; i++ {
		x.Feed(domain.Event{TimeMs: at(i), Name: "SPELL_DAMAGE", IsDamage: true, Amount: 1, HasAmount: true,
			Src: eu(foeGUID, "Bob-PvP-US", 0x548), Dst: eu(meGUID, "Ann-PvP-US", 0x511)})
	}
	part := x.Flush(time.Time{})
	seen := map[string]int{}
	for _, c := range part.Contacts {
		seen[c.GUID]++
	}
	for g, n := range seen {
		if n != 1 {
			t.Fatalf("%s appears %d times in one flush", g, n)
		}
	}
	if c := find(t, part, foeGUID); c.D.DmgHits != 500 || c.D.SrcEvents != 500 {
		t.Fatalf("%+v", c.D)
	}
}

func TestPoisonedNamesAreSanitisedInPings(t *testing.T) {
	host, m, sink := newHost(t)
	evil := "Al\nice‮\x00" + strings.Repeat("x", 500) + "-PvP-US"
	_ = host.Parsers.Feed(context.Background(), domain.Event{Source: "s", TimeMs: at(0), Name: "SPELL_DAMAGE",
		Src: eu("Player-1-E", evil, 0x548), Dst: eu(meGUID, "Ann-PvP-US", 0x511), Zone: "Z‮\n"})
	if err := m.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	pings := sink.all()
	if len(pings) == 0 {
		t.Fatal("nothing published")
	}
	for _, p := range pings {
		if err := p.Validate(); err != nil {
			t.Fatalf("ping with poisoned names is invalid: %v", err)
		}
		for _, c := range p.Contacts {
			for _, s := range []string{c.Name, c.Raw} {
				if strings.ContainsAny(s, "\n\r\x00‮") {
					t.Fatalf("control or bidi text survived: %q", s)
				}
			}
		}
	}
}

func TestIdleSourcesExpireSoTheSourceCapRecovers(t *testing.T) {
	host, m, _ := newHost(t)
	now := time.Unix(1_800_000_000, 0)
	m.now = func() time.Time { return now }
	ctx := context.Background()
	for i := 0; i < maxSources; i++ {
		ev := meEvent(0, 1, 1)
		ev.Source = fmt.Sprintf("src%d", i)
		if err := host.Parsers.Feed(ctx, ev); err != nil {
			t.Fatal(err)
		}
	}
	ev := meEvent(0, 1, 1)
	ev.Source = "one-too-many"
	if err := host.Parsers.Feed(ctx, ev); err == nil {
		t.Fatal("source cap did not hold")
	}
	_ = m.Flush(ctx)
	now = now.Add(ExpireAfter + time.Minute)
	_ = m.Flush(ctx)
	if err := host.Parsers.Feed(ctx, ev); err != nil {
		t.Fatalf("idle sources were not expired: %v", err)
	}
	if n := len(m.sources); n != 1 {
		t.Fatalf("%d sources kept", n)
	}
}

func TestOnePoisonedContactDoesNotLoseTheRest(t *testing.T) {
	host, m, sink := newHost(t)
	ctx := context.Background()
	_ = host.Parsers.Feed(ctx, domain.Event{Source: "s", TimeMs: at(0), Name: "SPELL_DAMAGE",
		Src: eu("Player-1-bad!!id", "Mallory-PvP-US", 0x548), Dst: eu(meGUID, "Ann-PvP-US", 0x511)})
	if err := m.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	pings := sink.all()
	if len(pings) != 1 {
		t.Fatalf("%d pings", len(pings))
	}
	if err := pings[0].Validate(); err != nil {
		t.Fatal(err)
	}
	if len(pings[0].Contacts) != 1 || pings[0].Contacts[0].GUID != meGUID {
		t.Fatalf("%+v", pings[0].Contacts)
	}
}
