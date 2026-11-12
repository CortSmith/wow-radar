package storetest

import (
	"errors"

	"radarlive/internal/domain"
)

func bountyAndStatusCases() []testCase {
	return []testCase{
		{"bounty ranks by killing blows and scores 10 each", func(e *env) {
			now := e.nowMs()
			e.apply(newPing("b1",
				withDelta(seen(guid(1), "Alpha", now), domain.Deltas{KB: 2}),
				withDelta(seen(guid(2), "Bravo", now), domain.Deltas{KB: 5, Assists: 1}),
				withDelta(seen(guid(3), "Charlie", now), domain.Deltas{Assists: 4}),
				seen(guid(4), "Delta", now),
			))
			rows, err := e.s.Bounty(e.ctx, domain.RulesetPvP, 0)
			if err != nil || len(rows) != 3 {
				e.t.Fatalf("rows %+v err %v", rows, err)
			}
			if rows[0].GUID != guid(2) || rows[0].Rank != 1 || rows[0].Score != 50 || rows[2].GUID != guid(3) || rows[2].Score != 0 {
				e.t.Fatalf("ranking %+v", rows)
			}
			limited, _ := e.s.Bounty(e.ctx, domain.RulesetPvP, 1)
			if len(limited) != 1 || limited[0].GUID != guid(2) {
				e.t.Fatalf("limit: %+v", limited)
			}
		}},
		{"bounty player returns row with rank or not found", func(e *env) {
			now := e.nowMs()
			e.apply(newPing("b1",
				withDelta(seen(guid(1), "Alpha", now), domain.Deltas{KB: 2}),
				withDelta(seen(guid(2), "Bravo", now), domain.Deltas{KB: 5}),
				seen(guid(3), "Charlie", now),
			))
			row, err := e.s.BountyPlayer(e.ctx, domain.RulesetPvP, guid(1))
			if err != nil || row.Rank != 2 || row.KB != 2 || row.Name != "Alpha" {
				e.t.Fatalf("row %+v err %v", row, err)
			}
			if _, err := e.s.BountyPlayer(e.ctx, domain.RulesetPvP, guid(3)); !errors.Is(err, domain.ErrNotFound) {
				e.t.Fatalf("no kills must be ErrNotFound, got %v", err)
			}
			if _, err := e.s.BountyPlayer(e.ctx, domain.RulesetPvP, guid(77)); !errors.Is(err, domain.ErrNotFound) {
				e.t.Fatalf("unknown must be ErrNotFound, got %v", err)
			}
		}},
		{"bounty is per ruleset", func(e *env) {
			p := newPing("b1", withDelta(seen(guid(1), "Alpha", e.nowMs()), domain.Deltas{KB: 1}))
			p.RS = domain.RulesetRPPvP
			e.apply(p)
			if rows, _ := e.s.Bounty(e.ctx, domain.RulesetPvP, 0); len(rows) != 0 {
				e.t.Fatalf("leak: %+v", rows)
			}
			if rows, _ := e.s.Bounty(e.ctx, domain.RulesetRPPvP, 0); len(rows) != 1 {
				e.t.Fatalf("missing: %+v", rows)
			}
		}},
		{"deaths are deduplicated by id", func(e *env) {
			p := newPing("b1")
			p.Deaths = []domain.Death{{ID: "d1", T: 1, Victim: guid(1)}, {ID: "d1", T: 1, Victim: guid(1)}, {ID: "d2", T: 2, Victim: guid(2)}}
			e.apply(p)
			again := newPing("b2")
			again.Deaths = []domain.Death{{ID: "d2", T: 2, Victim: guid(2)}, {ID: "d3", T: 3, Victim: guid(3)}}
			e.apply(again)
			status, err := e.s.Status(e.ctx)
			if err != nil || len(status) != 1 || status[0].Deaths != 3 {
				e.t.Fatalf("status %+v err %v", status, err)
			}
		}},
		{"status lists rulesets with data and last write", func(e *env) {
			if status, _ := e.s.Status(e.ctx); len(status) != 0 {
				e.t.Fatalf("empty store status %+v", status)
			}
			e.apply(newPing("b1", seen(guid(1), "Alpha", e.nowMs()), seen(guid(2), "Bravo", e.nowMs())))
			wrote := e.nowMs()
			e.clock.Advance(60_000)
			e.apply(newPing("b2", seen(guid(1), "Alpha", e.nowMs()-60_000))) // no change: must not move lastWrite
			p := newPing("b3", seen(guid(9), "Zed", e.nowMs()))
			p.RS = domain.RulesetNormal
			e.apply(p)
			status, err := e.s.Status(e.ctx)
			if err != nil || len(status) != 2 {
				e.t.Fatalf("status %+v err %v", status, err)
			}
			byID := map[domain.Ruleset]domain.RulesetStatus{}
			for _, s := range status {
				byID[s.ID] = s
			}
			if pvp := byID[domain.RulesetPvP]; pvp.Contacts != 2 || pvp.LastWrite != wrote {
				e.t.Fatalf("pvp %+v, want 2 contacts lastWrite %d", pvp, wrote)
			}
			if normal := byID[domain.RulesetNormal]; normal.Contacts != 1 || normal.LastWrite != e.nowMs() {
				e.t.Fatalf("normal %+v", normal)
			}
		}},
	}
}
