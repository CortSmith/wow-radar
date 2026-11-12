package storetest

import (
	"context"
	"errors"
	"sync"

	"radarlive/internal/domain"
)

func applyCases() []testCase {
	return []testCase{
		{"apply rejects invalid ping", func(e *env) {
			_, err := e.s.ApplyPing(e.ctx, domain.Ping{RS: domain.RulesetPvP})
			if !errors.Is(err, domain.ErrInvalid) {
				e.t.Fatalf("err = %v, want ErrInvalid", err)
			}
		}},
		{"apply stores a new contact", func(e *env) {
			res := e.apply(newPing("b1", seen(guid(1), "Alpha", e.nowMs())))
			if res.Received != 1 || res.Changed != 1 || res.Duplicate {
				e.t.Fatalf("result %+v", res)
			}
			if c := e.one(domain.RulesetPvP, guid(1)); c.Name != "Alpha" || c.Reaction != domain.ReactionHostile {
				e.t.Fatalf("contact %+v", c)
			}
		}},
		{"replayed batch is a no-op", func(e *env) {
			p := newPing("b1", withDelta(seen(guid(1), "Alpha", e.nowMs()), domain.Deltas{Events: 3, KB: 1}))
			e.apply(p)
			res := e.apply(p)
			if !res.Duplicate || res.Changed != 0 {
				e.t.Fatalf("replay result %+v", res)
			}
			if c := e.one(domain.RulesetPvP, guid(1)); c.Events != 3 || c.KB != 1 {
				e.t.Fatalf("counters doubled: %+v", c)
			}
		}},
		{"new batches add counters", func(e *env) {
			now := e.nowMs()
			e.apply(newPing("b1", withDelta(seen(guid(1), "Alpha", now), domain.Deltas{Events: 3, DmgTotal: 1.5})))
			e.apply(newPing("b2", withDelta(seen(guid(1), "Alpha", now+1000), domain.Deltas{Events: 2, DmgTotal: 2.5})))
			if c := e.one(domain.RulesetPvP, guid(1)); c.Events != 5 || c.DmgTotal != 4 || c.LastSeen != now+1000 {
				e.t.Fatalf("contact %+v", c)
			}
		}},
		{"unchanged observation reports zero changes", func(e *env) {
			now := e.nowMs()
			e.apply(newPing("b1", seen(guid(1), "Alpha", now)))
			if res := e.apply(newPing("b2", seen(guid(1), "Alpha", now))); res.Changed != 0 || res.Received != 1 {
				e.t.Fatalf("result %+v", res)
			}
		}},
		{"merge rules hold through the store", func(e *env) {
			now := e.nowMs()
			first := at(seen(guid(1), "Alpha", now), 1, 10, 10, now)
			e.apply(newPing("b1", first))
			stale := at(seen(guid(1), "Beta", now-5000), 2, 99, 99, now-5000)
			stale.FirstSeen = now - 9000
			e.apply(newPing("b2", stale))
			c := e.one(domain.RulesetPvP, guid(1))
			if c.Name != "Alpha" || c.MapID != 1 || c.X != 10 || c.FirstSeen != now-9000 || c.LastSeen != now {
				e.t.Fatalf("late ping overrode newer data: %+v", c)
			}
			if len(c.PastNames) != 1 || c.PastNames[0] != "Beta" {
				e.t.Fatalf("pastNames %v", c.PastNames)
			}
		}},
		{"rulesets are separate partitions", func(e *env) {
			p := newPing("b1", seen(guid(1), "Alpha", e.nowMs()))
			p.RS = domain.RulesetRP
			e.apply(p)
			if got := e.all(domain.RulesetPvP); len(got) != 0 {
				e.t.Fatalf("pvp leaked rp data: %v", got)
			}
			if got := e.all(domain.RulesetRP); len(got) != 1 {
				e.t.Fatalf("rp lost data: %v", got)
			}
			// the same batch id in another ruleset is not a replay
			p.RS = domain.RulesetNormal
			if res := e.apply(p); res.Duplicate {
				e.t.Fatal("batch ids must be scoped per ruleset")
			}
		}},
		{"results are copies", func(e *env) {
			p := seen(guid(1), "Alpha", e.nowMs())
			p.PastNames = []string{"Old"}
			e.apply(newPing("b1", p))
			got := e.all(domain.RulesetPvP)
			got[0].Name = "Mutated"
			got[0].PastNames[0] = "Mutated"
			again := e.one(domain.RulesetPvP, guid(1))
			if again.Name != "Alpha" || again.PastNames[0] != "Old" {
				e.t.Fatalf("store state leaked through results: %+v", again)
			}
		}},
		{"cancelled context is honoured", func(e *env) {
			ctx, cancel := context.WithCancel(e.ctx)
			cancel()
			if _, err := e.s.ApplyPing(ctx, newPing("b1")); !errors.Is(err, context.Canceled) {
				e.t.Fatalf("err = %v", err)
			}
			if _, err := e.s.Sweep(ctx, everything()); !errors.Is(err, context.Canceled) {
				e.t.Fatalf("sweep err = %v", err)
			}
		}},
		{"concurrent pings add up", func(e *env) {
			const writers = 20
			var wg sync.WaitGroup
			for i := 0; i < writers; i++ {
				wg.Add(1)
				go func() {
					defer wg.Done()
					p := newPing(batchName(i), withDelta(seen(guid(1), "Alpha", e.nowMs()), domain.Deltas{Events: 1}))
					if _, err := e.s.ApplyPing(e.ctx, p); err != nil {
						e.t.Error(err)
					}
				}()
			}
			wg.Wait()
			if c := e.one(domain.RulesetPvP, guid(1)); c.Events != writers {
				e.t.Fatalf("events = %d, want %d", c.Events, writers)
			}
		}},
		{"close can be called", func(e *env) {
			if err := e.s.Close(); err != nil {
				e.t.Fatal(err)
			}
		}},
	}
}

func batchName(i int) string { return "conc-" + string(rune('a'+i)) }

func withDelta(c domain.PingContact, d domain.Deltas) domain.PingContact {
	c.D = d
	return c
}
