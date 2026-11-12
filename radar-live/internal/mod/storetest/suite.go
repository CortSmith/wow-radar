// Package storetest is the behavioural test suite every core.Store implementation must pass.
// memstore is the reference implementation; sqlstore reuses the same suite via Run.
package storetest

import (
	"context"
	"fmt"
	"testing"
	"time"

	"radarlive/internal/core"
	"radarlive/internal/domain"
)

// NewStore builds a fresh, empty store that reads the current time from now.
type NewStore func(now func() time.Time) core.Store

// FakeClock is a settable clock for tests.
type FakeClock struct{ t time.Time }

// NewFakeClock starts at a fixed instant in 2026.
func NewFakeClock() *FakeClock { return &FakeClock{t: time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)} }

// Now returns the current fake time.
func (c *FakeClock) Now() time.Time { return c.t }

// Advance moves the clock forward by d.
func (c *FakeClock) Advance(d time.Duration) { c.t = c.t.Add(d) }

// env is what each case receives: a fresh store, its clock and a context.
type env struct {
	t     *testing.T
	s     core.Store
	clock *FakeClock
	ctx   context.Context
}

type testCase struct {
	name string
	run  func(e *env)
}

// Run executes every case against a fresh store from newStore.
func Run(t *testing.T, newStore NewStore) {
	t.Helper()
	var cases []testCase
	cases = append(cases, applyCases()...)
	cases = append(cases, trailAndChartCases()...)
	cases = append(cases, sweepCases()...)
	cases = append(cases, searchCases()...)
	cases = append(cases, bountyAndStatusCases()...)
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			clock := NewFakeClock()
			store := newStore(clock.Now)
			t.Cleanup(func() { _ = store.Close() })
			c.run(&env{t: t, s: store, clock: clock, ctx: context.Background()})
		})
	}
}

// nowMs is the fake clock in epoch milliseconds.
func (e *env) nowMs() int64 { return e.clock.Now().UnixMilli() }

// apply sends a ping and fails the test on error.
func (e *env) apply(p domain.Ping) domain.ApplyResult {
	e.t.Helper()
	res, err := e.s.ApplyPing(e.ctx, p)
	if err != nil {
		e.t.Fatalf("ApplyPing(%s): %v", p.BatchID, err)
	}
	return res
}

// all returns every contact of a ruleset through a wide search.
func (e *env) all(rs domain.Ruleset) []domain.Contact {
	e.t.Helper()
	res, err := e.s.Search(e.ctx, domain.SearchQuery{RS: rs, Limit: domain.SearchMaxLimit})
	if err != nil {
		e.t.Fatalf("Search: %v", err)
	}
	return res.Contacts
}

// one returns the single contact with guid, failing if it is absent.
func (e *env) one(rs domain.Ruleset, guid string) domain.Contact {
	e.t.Helper()
	for _, c := range e.all(rs) {
		if c.GUID == guid {
			return c
		}
	}
	e.t.Fatalf("contact %s not found in %s", guid, rs)
	return domain.Contact{}
}

// sweep runs a sweep and fails the test on error.
func (e *env) sweep(s domain.Sector) domain.SweepResult {
	e.t.Helper()
	res, err := e.s.Sweep(e.ctx, s)
	if err != nil {
		e.t.Fatalf("Sweep: %v", err)
	}
	return res
}

func guid(n int) string { return fmt.Sprintf("Player-%04d-ABCDEF", n) }

// newPing builds a pvp ping with the given contacts.
func newPing(batch string, contacts ...domain.PingContact) domain.Ping {
	return domain.Ping{RS: domain.RulesetPvP, BatchID: batch, Source: "test", Contacts: contacts}
}

// seen builds a hostile contact observation last seen at lastSeen.
func seen(g, name string, lastSeen int64) domain.PingContact {
	return domain.PingContact{Contact: domain.Contact{
		GUID: g, Name: name, Raw: name + "-Realm", Reaction: domain.ReactionHostile,
		FirstSeen: lastSeen, LastSeen: lastSeen,
	}}
}

// at gives a contact observation a position.
func at(c domain.PingContact, mapID int, x, y float64, posTime int64) domain.PingContact {
	c.MapID, c.X, c.Y, c.PosTime = mapID, x, y, posTime
	return c
}

// with sets the reaction of a contact observation.
func with(c domain.PingContact, r domain.Reaction) domain.PingContact {
	c.Reaction = r
	return c
}

// fixAt builds an exact fix.
func fixAt(t int64, mapID int, x, y float64) domain.Fix {
	return domain.Fix{T: t, MapID: mapID, X: x, Y: y, Kind: domain.FixExact}
}

func intPtr(v int) *int { return &v }

func zone(mapID int) domain.Sector {
	return domain.Sector{RS: domain.RulesetPvP, Scope: domain.ScopeZone, MapID: intPtr(mapID)}
}

func everything() domain.Sector {
	return domain.Sector{RS: domain.RulesetPvP, Scope: domain.ScopeAll}
}

func guids(contacts []domain.Contact) []string {
	out := make([]string, len(contacts))
	for i, c := range contacts {
		out[i] = c.GUID
	}
	return out
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
