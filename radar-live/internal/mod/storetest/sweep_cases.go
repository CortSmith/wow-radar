package storetest

import (
	"errors"
	"time"

	"radarlive/internal/domain"
)

// seedWorld applies a small cast: two hostiles and a friendly on map 1, a hostile on map 2, and one
// contact without a position. Returns the fake time of the seeding.
func seedWorld(e *env) int64 {
	now := e.nowMs()
	e.apply(newPing("seed",
		at(seen(guid(1), "Alpha", now-3000), 1, 10, 10, now-3000),
		at(seen(guid(2), "Bravo", now-2000), 1, 20, 20, now-2000),
		at(with(seen(guid(3), "Charlie", now-1000), domain.ReactionFriendly), 1, 30, 30, now-1000),
		at(seen(guid(4), "Delta", now-4000), 2, 40, 40, now-4000),
		seen(guid(5), "Echo", now-5000),
	))
	return now
}

func sweepCases() []testCase {
	return []testCase{
		{"sweep all returns everything newest first", func(e *env) {
			seedWorld(e)
			res := e.sweep(everything())
			want := []string{guid(3), guid(2), guid(1), guid(4), guid(5)}
			if got := guids(res.Contacts); !equalStrings(got, want) {
				e.t.Fatalf("order %v, want %v", got, want)
			}
			if !res.Changed || res.Now != e.nowMs() || res.Sector.Scope != domain.ScopeAll {
				e.t.Fatalf("envelope %+v", res)
			}
		}},
		{"sweep zone filters by map and needs a position", func(e *env) {
			seedWorld(e)
			if got := guids(e.sweep(zone(1)).Contacts); !equalStrings(got, []string{guid(3), guid(2), guid(1)}) {
				e.t.Fatalf("zone 1: %v", got)
			}
			if got := guids(e.sweep(zone(2)).Contacts); !equalStrings(got, []string{guid(4)}) {
				e.t.Fatalf("zone 2: %v", got)
			}
			if res := e.sweep(zone(7)); res.Contacts == nil || len(res.Contacts) != 0 {
				e.t.Fatalf("empty zone must be an empty slice, got %#v", res.Contacts)
			}
		}},
		{"sweep group, query and player filters", func(e *env) {
			seedWorld(e)
			s := zone(1)
			s.Group = domain.GroupFriendly
			if got := guids(e.sweep(s).Contacts); !equalStrings(got, []string{guid(3)}) {
				e.t.Fatalf("friendly: %v", got)
			}
			s.Group = domain.GroupHostile
			s.Query = "BRAV"
			if got := guids(e.sweep(s).Contacts); !equalStrings(got, []string{guid(2)}) {
				e.t.Fatalf("hostile+query: %v", got)
			}
			p := domain.Sector{RS: domain.RulesetPvP, Scope: domain.ScopePlayer, GUID: guid(4)}
			if got := guids(e.sweep(p).Contacts); !equalStrings(got, []string{guid(4)}) {
				e.t.Fatalf("player: %v", got)
			}
		}},
		{"sweep since returns only newer rows", func(e *env) {
			now := seedWorld(e)
			s := everything()
			s.Since = now - 2500
			res := e.sweep(s)
			if got := guids(res.Contacts); !equalStrings(got, []string{guid(3), guid(2)}) {
				e.t.Fatalf("since: %v", got)
			}
			s.Since = now
			if res := e.sweep(s); res.Changed || len(res.Contacts) != 0 {
				e.t.Fatalf("nothing newer must report changed=false: %+v", res)
			}
		}},
		{"sweep full load is always changed", func(e *env) {
			if res := e.sweep(everything()); !res.Changed {
				e.t.Fatal("since=0 on an empty store is still a (empty) full load")
			}
		}},
		{"sweep drops inactive contacts but player scope keeps them", func(e *env) {
			seedWorld(e)
			e.clock.Advance(domain.DefaultActiveWindow)
			if got := e.sweep(everything()).Contacts; len(got) != 0 {
				e.t.Fatalf("inactive contacts still listed: %v", guids(got))
			}
			s := everything()
			s.ActiveWithinMs = (domain.DefaultActiveWindow + time.Minute).Milliseconds()
			if got := e.sweep(s).Contacts; len(got) != 5 {
				e.t.Fatalf("activeWithin override ignored: %d", len(got))
			}
			p := domain.Sector{RS: domain.RulesetPvP, Scope: domain.ScopePlayer, GUID: guid(1)}
			if got := e.sweep(p).Contacts; len(got) != 1 {
				e.t.Fatalf("player scope must ignore the window: %d", len(got))
			}
		}},
		{"sweep player scope can include the trail", func(e *env) {
			now := e.nowMs()
			c := at(seen(guid(1), "Alpha", now), 1, 0, 0, now)
			c.Fixes = []domain.Fix{fixAt(10000, 1, 0, 0), fixAt(20000, 1, 100, 0), fixAt(30000, 1, 200, 0)}
			e.apply(newPing("b1", c))
			s := domain.Sector{RS: domain.RulesetPvP, Scope: domain.ScopePlayer, GUID: guid(1), Trail: true}
			if res := e.sweep(s); len(res.Trail) != 3 {
				e.t.Fatalf("full trail: %v", res.Trail)
			}
			s.Since = 10000 // contact lastSeen is newer, so it is returned; trail is cut at since
			if res := e.sweep(s); len(res.Trail) != 2 || res.Trail[0].T != 20000 {
				e.t.Fatalf("incremental trail: %v", res.Trail)
			}
			s.Trail = false
			if res := e.sweep(s); res.Trail != nil {
				e.t.Fatalf("trail not requested: %v", res.Trail)
			}
		}},
		{"sweep validates the sector", func(e *env) {
			_, err := e.s.Sweep(e.ctx, domain.Sector{RS: domain.RulesetPvP, Scope: domain.ScopeZone})
			if !errors.Is(err, domain.ErrInvalid) {
				e.t.Fatalf("err = %v", err)
			}
		}},
	}
}
