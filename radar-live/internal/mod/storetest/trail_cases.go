package storetest

import (
	"errors"

	"radarlive/internal/domain"
)

func trailAndChartCases() []testCase {
	return []testCase{
		{"trail appends fixes in time order", func(e *env) {
			c := seen(guid(1), "Alpha", e.nowMs())
			c.Fixes = []domain.Fix{fixAt(30000, 1, 200, 0), fixAt(10000, 1, 0, 0), fixAt(20000, 1, 100, 0)}
			e.apply(newPing("b1", c))
			trail := e.trail(guid(1), nil)
			if len(trail) != 3 || trail[0].T != 10000 || trail[2].T != 30000 {
				e.t.Fatalf("trail %+v", trail)
			}
		}},
		{"trail ignores replayed fixes in a new batch", func(e *env) {
			c := seen(guid(1), "Alpha", e.nowMs())
			c.Fixes = []domain.Fix{fixAt(10000, 1, 0, 0), fixAt(20000, 1, 100, 0)}
			e.apply(newPing("b1", c))
			e.apply(newPing("b2", c))
			if n := len(e.trail(guid(1), nil)); n != 2 {
				e.t.Fatalf("fixes = %d, want 2", n)
			}
		}},
		{"trail drops near and simultaneous fixes", func(e *env) {
			c := seen(guid(1), "Alpha", e.nowMs())
			c.Fixes = []domain.Fix{fixAt(10000, 1, 0, 0), fixAt(11000, 1, 1, 1), fixAt(16000, 1, 1, 1)}
			e.apply(newPing("b1", c))
			if n := len(e.trail(guid(1), nil)); n != 2 {
				e.t.Fatalf("fixes = %d, want 2 (middle one redundant)", n)
			}
		}},
		{"trail rounds coordinates and keeps kind", func(e *env) {
			c := seen(guid(1), "Alpha", e.nowMs())
			c.Fixes = []domain.Fix{{T: 10000, MapID: 1, X: 1.26, Y: 2.04, Kind: domain.FixApprox}}
			e.apply(newPing("b1", c))
			f := e.trail(guid(1), nil)[0]
			if f.X != 1.3 || f.Y != 2 || f.Kind != domain.FixApprox {
				e.t.Fatalf("fix %+v", f)
			}
		}},
		{"trail filters by map", func(e *env) {
			c := seen(guid(1), "Alpha", e.nowMs())
			c.Fixes = []domain.Fix{fixAt(10000, 1, 0, 0), fixAt(20000, 2, 0, 0), fixAt(30000, 1, 500, 0)}
			e.apply(newPing("b1", c))
			if n := len(e.trail(guid(1), intPtr(1))); n != 2 {
				e.t.Fatalf("map 1 fixes = %d", n)
			}
			if n := len(e.trail(guid(1), intPtr(9))); n != 0 {
				e.t.Fatalf("map 9 fixes = %d", n)
			}
		}},
		{"trail of unknown contact is empty", func(e *env) {
			if trail := e.trail(guid(99), nil); len(trail) != 0 {
				e.t.Fatalf("trail %v", trail)
			}
		}},
		{"trail is capped keeping the newest", func(e *env) {
			total := domain.TrailCapFixes + 100
			for start := 0; start < total; start += 1000 {
				c := seen(guid(1), "Alpha", e.nowMs())
				for i := start; i < start+1000 && i < total; i++ {
					c.Fixes = append(c.Fixes, fixAt(int64(i+1)*10000, 1, float64(i)*10, 0))
				}
				e.apply(newPing("cap-"+string(rune('a'+start/1000)), c))
			}
			trail := e.trail(guid(1), nil)
			if len(trail) != domain.TrailCapFixes {
				e.t.Fatalf("len = %d, want %d", len(trail), domain.TrailCapFixes)
			}
			if last := trail[len(trail)-1]; last.T != int64(total)*10000 {
				e.t.Fatalf("newest fix lost: %+v", last)
			}
		}},
		{"charts are stored per ruleset and newest wins", func(e *env) {
			p := newPing("b1")
			p.Charts = []domain.Chart{{MapID: 2, Name: "Two", X1: 1, Y1: 1}, {MapID: 1, Name: "One", X1: 5, Y1: 5}}
			if res := e.apply(p); res.Changed != 2 {
				e.t.Fatalf("changed = %d, want 2 (charts count)", res.Changed)
			}
			p2 := newPing("b2")
			p2.Charts = []domain.Chart{{MapID: 1, Name: "One", X1: 5, Y1: 5}, {MapID: 2, Name: "Two", X1: 9, Y1: 9}}
			if res := e.apply(p2); res.Changed != 1 {
				e.t.Fatalf("unchanged chart must not count: %d", res.Changed)
			}
			charts, err := e.s.Charts(e.ctx, domain.RulesetPvP)
			if err != nil || len(charts) != 2 || charts[0].MapID != 1 || charts[1].X1 != 9 {
				e.t.Fatalf("charts %+v err %v", charts, err)
			}
			other, _ := e.s.Charts(e.ctx, domain.RulesetRP)
			if len(other) != 0 {
				e.t.Fatalf("rp charts %v", other)
			}
		}},
		{"bad ruleset is rejected by read calls", func(e *env) {
			if _, err := e.s.Trail(e.ctx, "x", guid(1), nil); !errors.Is(err, domain.ErrInvalid) {
				e.t.Fatalf("trail err %v", err)
			}
			if _, err := e.s.Charts(e.ctx, "x"); !errors.Is(err, domain.ErrInvalid) {
				e.t.Fatalf("charts err %v", err)
			}
			if _, err := e.s.Bounty(e.ctx, "x", 0); !errors.Is(err, domain.ErrInvalid) {
				e.t.Fatalf("bounty err %v", err)
			}
			if _, err := e.s.BountyPlayer(e.ctx, "x", guid(1)); !errors.Is(err, domain.ErrInvalid) {
				e.t.Fatalf("bounty player err %v", err)
			}
			if _, err := e.s.Search(e.ctx, domain.SearchQuery{RS: "x"}); !errors.Is(err, domain.ErrInvalid) {
				e.t.Fatalf("search err %v", err)
			}
			if _, err := e.s.Sweep(e.ctx, domain.Sector{RS: "x", Scope: domain.ScopeAll}); !errors.Is(err, domain.ErrInvalid) {
				e.t.Fatalf("sweep err %v", err)
			}
		}},
	}
}

// trail reads a pvp trail and fails the test on error.
func (e *env) trail(g string, mapID *int) []domain.Fix {
	e.t.Helper()
	fixes, err := e.s.Trail(e.ctx, domain.RulesetPvP, g, mapID)
	if err != nil {
		e.t.Fatalf("Trail: %v", err)
	}
	return fixes
}
