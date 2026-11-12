package logparse

import (
	"bufio"
	"context"
	"errors"
	"os"
	"sync"
	"testing"
	"time"

	"radarlive/internal/core"
	"radarlive/internal/domain"
)

type recorder struct {
	mu     sync.Mutex
	events []domain.Event
}

func (*recorder) Name() string { return "recorder" }

func (r *recorder) Parse(_ context.Context, event any) error {
	if ev, ok := event.(domain.Event); ok {
		r.mu.Lock()
		r.events = append(r.events, ev)
		r.mu.Unlock()
	}
	return nil
}

func newHost(t *testing.T) (*core.Host, *recorder) {
	t.Helper()
	host := core.NewHost()
	m := New()
	m.now = func() time.Time { return time.Date(2026, 11, 5, 20, 0, 0, 0, time.UTC) } // the fixtures' day
	if err := host.Register(m); err != nil {
		t.Fatal(err)
	}
	rec := &recorder{}
	host.Parsers.Add(rec)
	return host, rec
}

func TestModuleParsesRawLinesAndFeedsEvents(t *testing.T) {
	host, rec := newHost(t)
	line := domain.RawLine{Source: "src1", Text: "11/5/2026 20:00:00.100  ZONE_CHANGE,1500,\"Testville\",0"}
	if err := host.Parsers.Feed(context.Background(), line); err != nil {
		t.Fatal(err)
	}
	if len(rec.events) != 1 {
		t.Fatalf("events=%d (the module must not re-parse its own events)", len(rec.events))
	}
	ev := rec.events[0]
	if ev.Source != "src1" || ev.Zone != "Testville" || ev.TimeMs != 1_793_908_800_100 {
		t.Fatalf("%+v", ev)
	}
}

func TestModuleRulesetOverride(t *testing.T) {
	host, rec := newHost(t)
	line := domain.RawLine{Source: "s", Ruleset: domain.DefaultRuleset(), Text: "11/5/2026 20:00:00.100  ZONE_CHANGE,1500,\"Testville\",0"}
	if err := host.Parsers.Feed(context.Background(), line); err != nil {
		t.Fatal(err)
	}
	if rec.events[0].Ruleset != domain.DefaultRuleset() {
		t.Fatalf("ruleset %q", rec.events[0].Ruleset)
	}
}

func TestModuleReportsBadLines(t *testing.T) {
	host, rec := newHost(t)
	err := host.Parsers.Feed(context.Background(), domain.RawLine{Text: "garbage"})
	if !errors.Is(err, ErrBadLine) || len(rec.events) != 0 {
		t.Fatalf("err=%v events=%d", err, len(rec.events))
	}
}

func TestModuleIgnoresOtherValues(t *testing.T) {
	host, rec := newHost(t)
	if err := host.Parsers.Feed(context.Background(), 3.14); err != nil || len(rec.events) != 0 {
		t.Fatalf("err=%v events=%d", err, len(rec.events))
	}
	if names := host.Parsers.Names(); names[0] != ModuleName {
		t.Fatalf("names=%v", names)
	}
}

func TestModuleRejectsUnknownZone(t *testing.T) {
	host, _ := newHost(t)
	load := func(string) (string, bool) { return "", false }
	if err := host.Config.Load([]string{"--" + SettingZone + "=Not/AZone"}, load); err != nil {
		t.Fatal(err)
	}
	err := host.Parsers.Feed(context.Background(), domain.RawLine{Text: "11/5/2026 20:00:00.100  ZONE_CHANGE,1,\"Z\",0"})
	if err == nil {
		t.Fatal("bad zone accepted")
	}
}

// TestSessionFixture parses the synthetic session log end to end.
func TestSessionFixture(t *testing.T) {
	f, err := os.Open("testdata/session.log")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	bad := 0
	var events []domain.Event
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		ev, err := ParseLine(sc.Text(), Options{Source: "fixture"})
		if err != nil {
			bad++
			continue
		}
		events = append(events, ev)
	}
	if bad != 3 || len(events) != 13 {
		t.Fatalf("bad=%d good=%d", bad, len(events))
	}
	byName := func(name string) []domain.Event {
		var out []domain.Event
		for _, ev := range events {
			if ev.Name == name {
				out = append(out, ev)
			}
		}
		return out
	}
	if charts := byName("MAP_CHANGE"); charts[0].Chart == nil || charts[0].Chart.X1 != 1000.5 || charts[0].Chart.Y1 != 2000 {
		t.Fatalf("chart: %+v", charts[0].Chart)
	}
	if walk := byName("SWING_DAMAGE"); len(walk) != 3 || walk[1].Info.X != 40 || walk[2].Info.Owner != "Player-1-0C" {
		t.Fatalf("walk/pet: %+v", walk)
	}
	deaths := byName("UNIT_DIED")
	if len(deaths) != 2 || !deaths[0].Unconscious || deaths[1].Unconscious {
		t.Fatalf("feign/real death: %+v", deaths)
	}
	if zones := byName("ZONE_CHANGE"); len(zones) != 2 || zones[1].TimeMs != 1_793_908_810_000 {
		t.Fatalf("offset-less line uses the UTC fallback: %+v", zones)
	}
	if got := byName("SPELL_DAMAGE")[0]; got.Ruleset != domain.RulesetPvP || !got.IsDamage || got.Amount != 120 {
		t.Fatalf("damage line: %+v", got)
	}
}
