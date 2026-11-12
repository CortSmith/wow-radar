package contactx

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
	if err := host.Parsers.Feed(context.Background(), "just a string"); err != nil {
		t.Fatal(err)
	}
	if err := m.Flush(context.Background()); err != nil || len(sink.all()) != 0 {
		t.Fatalf("err=%v pings=%d", err, len(sink.all()))
	}
}

func TestModuleFlushPublishesValidPing(t *testing.T) {
	host, m, sink := newHost(t)
	ev := meEvent(0, 5, 6)
	ev.Source = "my src/1"
	ev.Ruleset = domain.RulesetRP
	if err := host.Parsers.Feed(context.Background(), ev); err != nil {
		t.Fatal(err)
	}
	if err := host.Parsers.Feed(context.Background(), domain.Event{TimeMs: at(1), Name: "MAP_CHANGE", Source: "my src/1",
		Chart: &domain.Chart{MapID: 10, Name: "Ten", X1: 1, Y1: 1}}); err != nil {
		t.Fatal(err)
	}
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
	if p.RS != domain.RulesetRP || p.Source != "my src/1" || len(p.Contacts) != 1 || len(p.Charts) != 1 {
		t.Fatalf("%+v", p)
	}
	if err := m.Flush(context.Background()); err != nil || len(sink.all()) != 1 {
		t.Fatal("an idle flush must publish nothing")
	}
}

func TestModuleDefaultRulesetAndNPCsDropped(t *testing.T) {
	host, m, sink := newHost(t)
	m.sources["s"] = NewExtractor(Options{TrackNPCs: true})
	ev := meEvent(0, 1, 1)
	ev.Source = "s"
	_ = host.Parsers.Feed(context.Background(), ev)
	_ = m.Flush(context.Background())
	p := sink.all()[0]
	if p.RS != domain.DefaultRuleset() {
		t.Fatalf("rs=%q", p.RS)
	}
	for _, c := range p.Contacts {
		if domain.KindOf(c.GUID) != domain.KindPlayer {
			t.Fatalf("npc published: %s", c.GUID)
		}
	}
}

func TestModuleBatchIDsAreUniqueAndValid(t *testing.T) {
	host, m, sink := newHost(t)
	for i := range 3 {
		ev := meEvent(i*10, float64(i)*10, 0)
		ev.Source = "a"
		_ = host.Parsers.Feed(context.Background(), ev)
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
	host, m, sink := newHost(t)
	for i := range domain.PingMaxContacts + 5 {
		_ = host.Parsers.Feed(context.Background(), domain.Event{TimeMs: at(i), Name: "X", Source: "big",
			Src: eu(fmt.Sprintf("Player-1-%05d", i), "N", 0x548), Dst: eu(npcGUID, "B", 0x148)})
	}
	_ = m.Flush(context.Background())
	pings := sink.all()
	if len(pings) != 2 || len(pings[0].Contacts) != domain.PingMaxContacts || len(pings[1].Contacts) != 5 {
		t.Fatalf("split wrong: %d pings", len(pings))
	}
}

func TestModuleSourceLimit(t *testing.T) {
	host, _, _ := newHost(t)
	var last error
	for i := range maxSources + 1 {
		last = host.Parsers.Feed(context.Background(), domain.Event{Source: fmt.Sprint("s", i), Name: "ZONE_CHANGE", Zone: "Z"})
	}
	if last == nil {
		t.Fatal("source cap not enforced")
	}
}

func TestModuleReturnsSinkErrors(t *testing.T) {
	host, m, sink := newHost(t)
	sink.err = errors.New("down")
	ev := meEvent(0, 1, 1)
	_ = host.Parsers.Feed(context.Background(), ev)
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
	_ = host.Parsers.Feed(ctx, meEvent(0, 1, 1))
	deadline := time.Now().Add(3 * time.Second)
	for len(sink.all()) == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if len(sink.all()) == 0 {
		t.Fatal("no periodic flush")
	}
	_ = host.Parsers.Feed(ctx, meEvent(100, 90, 90))
	if err := m.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	if got := sink.all(); got[len(got)-1].Contacts[0].X != 90 {
		t.Fatalf("final flush on Stop missing: %+v", got[len(got)-1].Contacts)
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
	_ = host.Parsers.Feed(context.Background(), meEvent(0, 1, 1))
	_ = m.Flush(context.Background())
	if c := sink.all()[0].Contacts[0]; c.Name != "Ann" || c.Raw != "Ann-PvP-US" {
		t.Fatalf("%q %q", c.Name, c.Raw)
	}
}
