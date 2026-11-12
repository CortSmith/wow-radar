package storetest

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"radarlive/internal/core"
	"radarlive/internal/domain"
)

// RunArmory executes the ArmoryStore contract against fresh stores from newStore. A store that does
// not implement core.ArmoryStore makes every case skip, so sqlstore can call it unconditionally.
func RunArmory(t *testing.T, newStore NewStore) {
	t.Helper()
	for _, c := range armoryCases() {
		t.Run("armory/"+c.name, func(t *testing.T) {
			clock := NewFakeClock()
			store := newStore(clock.Now)
			t.Cleanup(func() { _ = store.Close() })
			if _, ok := store.(core.ArmoryStore); !ok {
				t.Skip("store does not implement core.ArmoryStore")
			}
			c.run(&env{t: t, s: store, clock: clock, ctx: t.Context()})
		})
	}
}

func armoryCases() []testCase {
	return []testCase{
		{"upsert and get", armoryUpsertGet},
		{"newest fetch wins", armoryNewestWins},
		{"batch replay is a no-op", armoryReplay},
		{"names are case-insensitive", armoryCaseInsensitive},
		{"list keeps request order", armoryList},
		{"list cap", armoryListCap},
		{"size caps and bad input", armorySizeCaps},
		{"status handling", armoryStatus},
		{"snapshots per day", armorySnapshots},
		{"prune snapshots", armoryPrune},
		{"run summary round trip", armoryRuns},
		{"seeds: players only, de-duplicated", armorySeedsPlayersOnly},
		{"seeds: empty store", armorySeedsEmpty},
		{"seeds: pagination and guilds", armorySeedsPagination},
		{"seeds: bad cursor and limit", armorySeedsBadInput},
	}
}

const dayMs = int64(86_400_000)

func (e *env) arm() core.ArmoryStore { return e.s.(core.ArmoryStore) }

// armChar builds a valid synthetic character fetched at ms.
func armChar(name string, ms int64) domain.ArmoryCharacter {
	return domain.ArmoryCharacter{
		Realm: "Testrealm", Name: name, Class: "Mage", Race: "Human", Faction: "Alliance", Level: 60,
		Guild: "Synthetic Guild", Health: 4000, PowerType: "mana", Power: 5000,
		Gear:      json.RawMessage(`{"head":{"id":1}}`),
		Talents:   json.RawMessage(`{"tree":"arcane"}`),
		Stats:     json.RawMessage(`{"crit":5}`),
		PvP:       json.RawMessage(`{"kills":3}`),
		FetchedAt: ms, Status: domain.ArmoryStatusOK,
	}
}

func armBatch(id string, chars ...domain.ArmoryCharacter) domain.ArmoryBatch {
	return domain.ArmoryBatch{BatchID: id, RunID: "run-1", Characters: chars, Cursor: "c1"}
}

func (e *env) applyArm(b domain.ArmoryBatch) domain.ArmoryApplyResult {
	e.t.Helper()
	res, err := e.arm().ApplyArmoryBatch(e.ctx, b)
	if err != nil {
		e.t.Fatalf("ApplyArmoryBatch(%s): %v", b.BatchID, err)
	}
	return res
}

func (e *env) getArm(name string) domain.ArmoryCharacter {
	e.t.Helper()
	c, err := e.arm().GetArmoryCharacter(e.ctx, "Testrealm", name)
	if err != nil {
		e.t.Fatalf("GetArmoryCharacter(%s): %v", name, err)
	}
	return c
}

func armoryUpsertGet(e *env) {
	now := e.nowMs()
	res := e.applyArm(armBatch("b1", armChar("Aelwyn", now), armChar("Borin", now)))
	if res.Duplicate || res.Stored != 2 || res.Skipped != 0 {
		e.t.Fatalf("result %+v", res)
	}
	got := e.getArm("Aelwyn")
	if got.Level != 60 || got.Guild != "Synthetic Guild" || string(got.Gear) != `{"head":{"id":1}}` || got.FetchedAt != now {
		e.t.Fatalf("got %+v", got)
	}
	if _, err := e.arm().GetArmoryCharacter(e.ctx, "Testrealm", "Nobody"); !errors.Is(err, domain.ErrNotFound) {
		e.t.Fatalf("missing: %v", err)
	}
}

func armoryNewestWins(e *env) {
	now := e.nowMs()
	newer := armChar("Aelwyn", now+1000)
	newer.Level = 61
	e.applyArm(armBatch("b1", newer))
	older := armChar("Aelwyn", now)
	older.Level = 59
	res := e.applyArm(armBatch("b2", older))
	if res.Stored != 0 || res.Skipped != 1 || e.getArm("Aelwyn").Level != 61 {
		e.t.Fatalf("older fetch must lose: %+v", res)
	}
	newest := armChar("Aelwyn", now+2000)
	newest.Level = 62
	if res := e.applyArm(armBatch("b3", newest)); res.Stored != 1 || e.getArm("Aelwyn").Level != 62 {
		e.t.Fatalf("newer fetch must win: %+v", res)
	}
}

func armoryReplay(e *env) {
	b := armBatch("b1", armChar("Aelwyn", e.nowMs()))
	e.applyArm(b)
	res := e.applyArm(b)
	if !res.Duplicate || res.Stored != 0 {
		e.t.Fatalf("replay %+v", res)
	}
}

func armoryCaseInsensitive(e *env) {
	e.applyArm(armBatch("b1", armChar("Aelwyn", e.nowMs())))
	c, err := e.arm().GetArmoryCharacter(e.ctx, " testREALM ", "AELWYN")
	if err != nil || c.Name != "Aelwyn" {
		e.t.Fatalf("lookup %+v %v", c, err)
	}
	e.applyArm(armBatch("b2", armChar("AELWYN", e.nowMs()+5)))
	if got, err := e.arm().ListArmoryCharacters(e.ctx, "Testrealm", []string{"aelwyn"}); err != nil || len(got) != 1 {
		e.t.Fatalf("one row expected: %v %v", got, err)
	}
}

func armoryList(e *env) {
	now := e.nowMs()
	e.applyArm(armBatch("b1", armChar("Aelwyn", now), armChar("Borin", now), armChar("Caro", now)))
	got, err := e.arm().ListArmoryCharacters(e.ctx, "Testrealm", []string{"Caro", "Nobody", "aelwyn"})
	if err != nil || len(got) != 2 || got[0].Name != "Caro" || got[1].Name != "Aelwyn" {
		e.t.Fatalf("list %+v %v", got, err)
	}
}

func armoryListCap(e *env) {
	names := make([]string, domain.ArmoryBatchMax+1)
	for i := range names {
		names[i] = "Aelwyn"
	}
	if _, err := e.arm().ListArmoryCharacters(e.ctx, "Testrealm", names); !errors.Is(err, domain.ErrInvalid) {
		e.t.Fatalf("err %v", err)
	}
}

func armorySizeCaps(e *env) {
	now := e.nowMs()
	big := armChar("Aelwyn", now)
	big.Gear = json.RawMessage(`{"x":"` + strings.Repeat("a", domain.ArmoryBlobMaxBytes) + `"}`)
	bad := armChar("Borin", now)
	bad.Status = "weird"
	arr := armChar("Caro", now)
	arr.PvP = json.RawMessage(`[1]`)
	for _, c := range []domain.ArmoryCharacter{big, bad, arr} {
		if _, err := e.arm().ApplyArmoryBatch(e.ctx, armBatch("bx", armChar("Dara", now), c)); !errors.Is(err, domain.ErrInvalid) {
			e.t.Fatalf("%s: err %v", c.Name, err)
		}
	}
	if _, err := e.arm().GetArmoryCharacter(e.ctx, "Testrealm", "Dara"); !errors.Is(err, domain.ErrNotFound) {
		e.t.Fatalf("a rejected batch must store nothing: %v", err)
	}
	// A rejected batch id is not remembered: the corrected batch is applied.
	if res := e.applyArm(armBatch("bx", armChar("Dara", now))); res.Duplicate || res.Stored != 1 {
		e.t.Fatalf("corrected batch %+v", res)
	}
}

func armoryStatus(e *env) {
	now := e.nowMs()
	e.applyArm(armBatch("b1", armChar("Aelwyn", now)))
	failed := armChar("Aelwyn", now+10)
	failed.Status, failed.Level, failed.Gear = domain.ArmoryStatusError, 0, nil
	e.applyArm(armBatch("b2", failed))
	got := e.getArm("Aelwyn")
	if got.Status != domain.ArmoryStatusError || got.FetchedAt != now+10 || got.Level != 60 || len(got.Gear) == 0 {
		e.t.Fatalf("an error keeps the last good details: %+v", got)
	}
	hidden := armChar("Borin", now)
	hidden.Status = domain.ArmoryStatusHidden
	e.applyArm(armBatch("b3", hidden))
	if e.getArm("Borin").Status != domain.ArmoryStatusHidden {
		e.t.Fatal("a new hidden character is stored with its status")
	}
}

func armorySnapshots(e *env) {
	day0 := e.nowMs()
	e.applyArm(armBatch("b1", armChar("Aelwyn", day0)))
	second := armChar("Aelwyn", day0+1000)
	second.Level = 61
	e.applyArm(armBatch("b2", second))
	next := armChar("Aelwyn", day0+dayMs)
	next.Gear = json.RawMessage(`{"head":{"id":2}}`)
	e.applyArm(armBatch("b3", next))
	snaps, err := e.arm().ArmorySnapshots(e.ctx, "Testrealm", "aelwyn")
	if err != nil || len(snaps) != 2 {
		e.t.Fatalf("one snapshot per day expected: %+v %v", snaps, err)
	}
	if snaps[0].Day != domain.ArmoryDay(day0) || snaps[0].Level != 61 || snaps[1].Day != snaps[0].Day+1 {
		e.t.Fatalf("snapshots %+v", snaps)
	}
	if snaps[0].GearHash == "" || snaps[0].GearHash == snaps[1].GearHash || string(snaps[0].PvP) != `{"kills":3}` {
		e.t.Fatalf("snapshot content %+v", snaps)
	}
}

func armoryPrune(e *env) {
	day0 := e.nowMs()
	for i, id := range []string{"b1", "b2", "b3"} {
		e.applyArm(armBatch(id, armChar("Aelwyn", day0+int64(i)*dayMs)))
	}
	cut := domain.ArmoryDay(day0) + 1
	n, err := e.arm().PruneArmorySnapshots(e.ctx, cut)
	snaps, _ := e.arm().ArmorySnapshots(e.ctx, "Testrealm", "Aelwyn")
	if err != nil || n != 1 || len(snaps) != 2 || snaps[0].Day != cut {
		e.t.Fatalf("pruned %d %v, left %+v", n, err, snaps)
	}
	if e.getArm("Aelwyn").Level != 60 {
		e.t.Fatal("pruning must not touch the current row")
	}
}

func armoryRuns(e *env) {
	if _, err := e.arm().LastArmoryRun(e.ctx); !errors.Is(err, domain.ErrNotFound) {
		e.t.Fatalf("empty: %v", err)
	}
	now := e.nowMs()
	old := domain.ArmoryRun{ID: "run-1", StartedAt: now, FinishedAt: now + 5, Enumerated: 10, Fetched: 9, Stored: 8, Skipped: 1, Failed: 1, Status: domain.ArmoryRunPartial, Cursor: "p3"}
	cur := domain.ArmoryRun{ID: "run-2", StartedAt: now + 1000, Status: domain.ArmoryRunRunning, Cursor: "p1"}
	for _, r := range []domain.ArmoryRun{old, cur} {
		if err := e.arm().RecordArmoryRun(e.ctx, r); err != nil {
			e.t.Fatal(err)
		}
	}
	if got, _ := e.arm().LastArmoryRun(e.ctx); got != cur {
		e.t.Fatalf("last %+v", got)
	}
	cur.Status, cur.FinishedAt, cur.Stored = domain.ArmoryRunOK, now+2000, 7
	if err := e.arm().RecordArmoryRun(e.ctx, cur); err != nil {
		e.t.Fatal(err)
	}
	if got, _ := e.arm().LastArmoryRun(e.ctx); got != cur {
		e.t.Fatalf("replaced %+v", got)
	}
	if err := e.arm().RecordArmoryRun(e.ctx, domain.ArmoryRun{ID: "x"}); !errors.Is(err, domain.ErrInvalid) {
		e.t.Fatalf("invalid run: %v", err)
	}
}

// seedPing builds a ping for any ruleset from raw contact observations.
func seedPing(rs domain.Ruleset, batch string, ms int64, cs ...[3]string) domain.Ping {
	p := domain.Ping{RS: rs, BatchID: batch, Source: "test"}
	for _, c := range cs { // guid, name, raw
		p.Contacts = append(p.Contacts, domain.PingContact{Contact: domain.Contact{
			GUID: c[0], Name: c[1], Raw: c[2], Reaction: domain.ReactionHostile, FirstSeen: ms, LastSeen: ms}})
	}
	return p
}

func (e *env) seeds(cursor string, limit int) domain.ArmorySeedPage {
	e.t.Helper()
	ss, ok := e.s.(core.ArmorySeedStore)
	if !ok {
		e.t.Skip("store does not implement core.ArmorySeedStore")
	}
	p, err := ss.ArmorySeeds(e.ctx, cursor, limit)
	if err != nil {
		e.t.Fatalf("ArmorySeeds(%q, %d): %v", cursor, limit, err)
	}
	return p
}

// seedWorldContacts stores players (some duplicated across rulesets; pings only accept Player
// GUIDs, so the store-side player filter is defensive), an invalid name and a
// player without a realm suffix. Expected distinct seeds: ann, bob, cyd.
func seedWorldContacts(e *env) {
	ms := e.nowMs()
	e.apply(seedPing(domain.RulesetPvP, "s1", ms,
		[3]string{"Player-0001-AAAAAA", "Ann", "Ann-Testrealm"},
		[3]string{"Player-0002-AAAAAA", "Bob", "Bob"},
		[3]string{"Player-0004-AAAAAA", "Zed2", "Zed2-Testrealm"}))
	e.apply(seedPing(domain.RulesetNormal, "s2", ms,
		[3]string{"Player-0003-AAAAAA", "ANN", "ANN-testrealm"},
		[3]string{"Player-0005-AAAAAA", "Cyd", "Cyd-Otherrealm"}))
}

func seedKeys(items []domain.ArmorySeed) []string {
	var out []string
	for _, s := range items {
		if s.Kind == domain.ArmorySeedGuild {
			out = append(out, "g:"+s.Realm+"/"+s.Guild)
		} else {
			out = append(out, "c:"+s.Realm+"/"+s.Name)
		}
	}
	return out
}

func armorySeedsPlayersOnly(e *env) {
	seedWorldContacts(e)
	p := e.seeds("", 500)
	got := seedKeys(p.Items)
	want := []string{"c:Testrealm/Ann", "c:/Bob", "c:Otherrealm/Cyd"}
	if !equalStrings(got, want) || p.Next != "" {
		e.t.Fatalf("seeds = %v next %q, want %v", got, p.Next, want)
	}
	for _, s := range p.Items {
		if err := s.Validate(); err != nil {
			e.t.Fatalf("invalid seed %+v: %v", s, err)
		}
	}
}

func armorySeedsEmpty(e *env) {
	p := e.seeds("", 10)
	if p.Items == nil || len(p.Items) != 0 || p.Next != "" {
		e.t.Fatalf("empty store: %+v", p)
	}
}

func armorySeedsPagination(e *env) {
	seedWorldContacts(e)
	if _, err := e.arm().ApplyArmoryBatch(e.ctx, domain.ArmoryBatch{BatchID: "sg1", Characters: []domain.ArmoryCharacter{
		armChar("Ann", e.nowMs()), armChar("Dee", e.nowMs()), func() domain.ArmoryCharacter {
			c := armChar("Eve", e.nowMs())
			c.Guild = ""
			return c
		}()}}); err != nil {
		e.t.Fatal(err)
	}
	full := seedKeys(e.seeds("", 500).Items)
	want := []string{"c:Testrealm/Ann", "c:/Bob", "c:Otherrealm/Cyd", "g:Testrealm/Synthetic Guild"}
	if !equalStrings(full, want) {
		e.t.Fatalf("full feed = %v, want %v (characters first, then guilds)", full, want)
	}
	for limit := 1; limit <= len(want)+1; limit++ {
		var all []string
		cursor := ""
		for pages := 0; ; pages++ {
			if pages > 20 {
				e.t.Fatalf("limit %d: does not terminate", limit)
			}
			p := e.seeds(cursor, limit)
			if len(p.Items) > limit {
				e.t.Fatalf("limit %d: page of %d", limit, len(p.Items))
			}
			all = append(all, seedKeys(p.Items)...)
			if p.Next == "" {
				break
			}
			if len(p.Items) != limit {
				e.t.Fatalf("limit %d: short page with a next cursor", limit)
			}
			cursor = p.Next
		}
		if !equalStrings(all, want) {
			e.t.Fatalf("limit %d: paged %v, want %v", limit, all, want)
		}
	}
}

func armorySeedsBadInput(e *env) {
	ss, ok := e.s.(core.ArmorySeedStore)
	if !ok {
		e.t.Skip("store does not implement core.ArmorySeedStore")
	}
	long := strings.Repeat("a", domain.ArmoryCursorMax+1)
	for _, c := range []string{"x", "c|a", "z|a|b", "c|'; DROP TABLE contacts;--|x", "g|a", "\x00", long} {
		if _, err := ss.ArmorySeeds(e.ctx, c, 10); !errors.Is(err, domain.ErrInvalid) {
			e.t.Errorf("cursor %q: err %v, want ErrInvalid", c, err)
		}
	}
	for _, n := range []int{0, -3, 501} {
		if _, err := ss.ArmorySeeds(e.ctx, "", n); !errors.Is(err, domain.ErrInvalid) {
			e.t.Errorf("limit %d: err %v, want ErrInvalid", n, err)
		}
	}
}
