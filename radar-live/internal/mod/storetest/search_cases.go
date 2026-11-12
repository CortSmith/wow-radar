package storetest

import "radarlive/internal/domain"

func searchCases() []testCase {
	return []testCase{
		{"search matches name, raw name and past names case-insensitively", func(e *env) {
			now := e.nowMs()
			renamed := seen(guid(1), "Newname", now)
			renamed.PastNames = []string{"Formerly"}
			e.apply(newPing("b1", renamed, seen(guid(2), "Bravo", now-1000)))
			for q, want := range map[string]int{"newn": 1, "FORMER": 1, "realm": 2, "bravo": 1, "zzz": 0, "": 2} {
				res, err := e.s.Search(e.ctx, domain.SearchQuery{RS: domain.RulesetPvP, Q: q})
				if err != nil || res.Total != want || len(res.Contacts) != want {
					e.t.Errorf("q=%q total=%d len=%d err=%v, want %d", q, res.Total, len(res.Contacts), err, want)
				}
			}
		}},
		{"search filters by map and group", func(e *env) {
			seedWorld(e)
			q := domain.SearchQuery{RS: domain.RulesetPvP, MapID: intPtr(1), Group: domain.GroupHostile}
			res, _ := e.s.Search(e.ctx, q)
			if !equalStrings(guids(res.Contacts), []string{guid(2), guid(1)}) {
				e.t.Fatalf("hits %v", guids(res.Contacts))
			}
		}},
		{"search pages with total, default limit and cap", func(e *env) {
			now := e.nowMs()
			var cs []domain.PingContact
			for i := 1; i <= 60; i++ {
				cs = append(cs, seen(guid(i), "Player", now-int64(i)))
			}
			e.apply(newPing("b1", cs...))
			res, _ := e.s.Search(e.ctx, domain.SearchQuery{RS: domain.RulesetPvP})
			if res.Total != 60 || len(res.Contacts) != domain.SearchDefaultLimit {
				e.t.Fatalf("default page: total=%d len=%d", res.Total, len(res.Contacts))
			}
			page, _ := e.s.Search(e.ctx, domain.SearchQuery{RS: domain.RulesetPvP, Limit: 10, Offset: 55})
			if len(page.Contacts) != 5 || page.Contacts[0].GUID != guid(56) {
				e.t.Fatalf("offset page: %v", guids(page.Contacts))
			}
			past, _ := e.s.Search(e.ctx, domain.SearchQuery{RS: domain.RulesetPvP, Offset: 500})
			if past.Total != 60 || past.Contacts == nil || len(past.Contacts) != 0 {
				e.t.Fatalf("offset beyond end: %+v", past)
			}
			capped, _ := e.s.Search(e.ctx, domain.SearchQuery{RS: domain.RulesetPvP, Limit: 100000})
			if len(capped.Contacts) != 60 {
				e.t.Fatalf("cap: %d", len(capped.Contacts))
			}
		}},
	}
}
