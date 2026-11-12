package bountyx

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"radarlive/internal/core"
	"radarlive/internal/domain"
)

type captureSink struct {
	mu    sync.Mutex
	pings []domain.Ping
	err   error
}

func (*captureSink) Name() string { return "capture" }

func (s *captureSink) Accept(_ context.Context, p domain.Ping) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pings = append(s.pings, p)
	return s.err
}

func (s *captureSink) all() []domain.Ping {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]domain.Ping(nil), s.pings...)
}

func newHost(t *testing.T) (*core.Host, *Module, *captureSink) {
	t.Helper()
	host := core.NewHost()
	m := New()
	if err := host.Register(m); err != nil {
		t.Fatal(err)
	}
	sink := &captureSink{}
	host.Sinks.Add(sink)
	return host, m, sink
}

func feedKill(t *testing.T, host *core.Host, source string, startSec float64) {
	t.Helper()
	ctx := context.Background()
	for _, ev := range []domain.Event{hitOn(startSec, foe(foeA, "Aaa-PvP-US"), victim), died(startSec+1, victim)} {
		ev.Source = source
		if err := host.Parsers.Feed(ctx, ev); err != nil {
			t.Fatal(err)
		}
	}
}

func TestModuleRegistersParser(t *testing.T) {
	host, m, _ := newHost(t)
	if m.Name() != ModuleName {
		t.Fatal(m.Name())
	}
	if names := host.Parsers.Names(); len(names) != 1 || names[0] != ModuleName {
		t.Fatalf("parsers=%v", names)
	}
}

func TestModuleIgnoresForeignEvents(t *testing.T) {
	host, m, sink := newHost(t)
	if err := host.Parsers.Feed(context.Background(), 42); err != nil {
		t.Fatal(err)
	}
	if err := m.Flush(context.Background()); err != nil || len(sink.all()) != 0 {
		t.Fatalf("err=%v pings=%d", err, len(sink.all()))
	}
}

func TestModuleFlushPublishesValidPing(t *testing.T) {
	host, m, sink := newHost(t)
	feedKill(t, host, "src/1", 0)
	if err := m.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	pings := sink.all()
	if len(pings) != 1 {
		t.Fatalf("pings=%d", len(pings))
	}
	p := pings[0]
	if err := p.Validate(); err != nil {
		t.Fatal(err)
	}
	if p.RS != domain.DefaultRuleset() || p.Source != "src/1" || len(p.Deaths) != 1 || len(p.Contacts) != 1 {
		t.Fatalf("%+v", p)
	}
	c := p.Contacts[0]
	if c.GUID != foeA || c.D.KB != 1 || c.Name != "Aaa PvP US" || c.Reaction != domain.ReactionHostile || c.LastSeen != at(1) {
		t.Fatalf("contact: %+v", c)
	}
	if err := m.Flush(context.Background()); err != nil || len(sink.all()) != 1 {
		t.Fatal("an idle flush must publish nothing")
	}
}

func TestModuleUsesSourceRuleset(t *testing.T) {
	host, m, sink := newHost(t)
	ev := died(1, victim)
	ev.Ruleset = domain.RulesetRP
	_ = host.Parsers.Feed(context.Background(), ev)
	_ = m.Flush(context.Background())
	if p := sink.all()[0]; p.RS != domain.RulesetRP || len(p.Contacts) != 0 {
		t.Fatalf("%+v", p)
	}
}

func TestModuleBatchIDsAreUniqueAndValid(t *testing.T) {
	host, m, sink := newHost(t)
	for i := range 3 {
		feedKill(t, host, "a", float64(i)*100)
		_ = m.Flush(context.Background())
	}
	seen := map[string]bool{}
	for _, p := range sink.all() {
		if seen[p.BatchID] {
			t.Fatalf("duplicate batch id %s", p.BatchID)
		}
		seen[p.BatchID] = true
		if err := p.Validate(); err != nil {
			t.Fatal(err)
		}
	}
	if len(seen) != 3 {
		t.Fatalf("pings=%d", len(seen))
	}
}

func TestModuleSplitsLargePings(t *testing.T) {
	_, m, _ := newHost(t)
	credits := map[string]Credit{}
	for i := range domain.PingMaxContacts + 3 {
		credits[fmt.Sprintf("Player-1-%05d", i)] = Credit{KB: 1, LastMs: at(1)}
	}
	pings := m.pings("s", Part{Credits: credits, Deaths: []domain.Death{{ID: "x", T: at(1), Victim: "Player-1-V1"}}})
	if len(pings) != 2 || len(pings[0].Contacts) != domain.PingMaxContacts || len(pings[1].Contacts) != 3 {
		t.Fatalf("split wrong: %d pings", len(pings))
	}
	if len(pings[0].Deaths) != 1 || len(pings[1].Deaths) != 0 {
		t.Fatal("deaths belong to the first ping only")
	}
}

func TestModuleSourceLimit(t *testing.T) {
	host, _, _ := newHost(t)
	var last error
	for i := range maxSources + 1 {
		last = host.Parsers.Feed(context.Background(), domain.Event{Source: fmt.Sprint("s", i), Name: "ZONE_CHANGE"})
	}
	if last == nil {
		t.Fatal("source cap not enforced")
	}
}

func TestModuleReturnsSinkErrors(t *testing.T) {
	host, m, sink := newHost(t)
	sink.err = errors.New("down")
	feedKill(t, host, "s", 0)
	if err := m.Flush(context.Background()); err == nil {
		t.Fatal("sink error swallowed")
	}
}

func TestModuleStartStopFlushesPeriodically(t *testing.T) {
	host, m, sink := newHost(t)
	if err := host.Config.Load([]string{"--" + SettingFlushEvery + "=20ms"}, func(string) (string, bool) { return "", false }); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := m.Start(ctx); err != nil {
		t.Fatal(err)
	}
	feedKill(t, host, "s", 0)
	deadline := time.Now().Add(3 * time.Second)
	for len(sink.all()) == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if len(sink.all()) == 0 {
		t.Fatal("no periodic flush")
	}
	feedKill(t, host, "s", 100)
	if err := m.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	total := 0
	for _, p := range sink.all() {
		total += len(p.Deaths)
	}
	if total != 2 {
		t.Fatalf("deaths published=%d want 2", total)
	}
}

func TestStopWithoutStart(t *testing.T) {
	_, m, _ := newHost(t)
	if err := m.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestSanitizeID(t *testing.T) {
	if got := sanitizeID("a b/c.d:e-f_g", 24); got != "a_b_c.d:e-f_g" {
		t.Fatal(got)
	}
	if got := sanitizeID("abcdef", 3); got != "abc" {
		t.Fatal(got)
	}
}

func TestModuleRealmSuffixSetting(t *testing.T) {
	host, m, sink := newHost(t)
	if err := host.Config.Load([]string{"--" + SettingRealmSuffix + "=PvP-US"}, func(string) (string, bool) { return "", false }); err != nil {
		t.Fatal(err)
	}
	feedKill(t, host, "s", 0)
	_ = m.Flush(context.Background())
	if c := sink.all()[0].Contacts[0]; c.Name != "Aaa" || c.Raw != "Aaa-PvP-US" {
		t.Fatalf("%q %q", c.Name, c.Raw)
	}
}
