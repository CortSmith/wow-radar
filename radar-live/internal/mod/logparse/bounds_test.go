package logparse

import (
	"context"
	"errors"
	"math"
	"strings"
	"testing"
	"time"

	"radarlive/internal/core"
	"radarlive/internal/domain"
)

var boundsNow = time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)

func evAt(t time.Time) domain.Event {
	return domain.Event{Name: "SPELL_DAMAGE", TimeMs: t.UnixMilli(), Src: domain.EventUnit{GUID: "Player-1-A", Raw: "Alice"}}
}

func TestCheckEventTimeWindow(t *testing.T) {
	b := Bounds{Window: 24 * time.Hour}
	cases := map[string]struct {
		at time.Time
		ok bool
	}{
		"now":            {boundsNow, true},
		"23h ago":        {boundsNow.Add(-23 * time.Hour), true},
		"23h ahead":      {boundsNow.Add(23 * time.Hour), true},
		"25h ago":        {boundsNow.Add(-25 * time.Hour), false},
		"25h ahead":      {boundsNow.Add(25 * time.Hour), false},
		"year 2004":      {time.Date(2004, 6, 1, 0, 0, 0, 0, time.UTC), false},
		"year 9999":      {time.Date(9999, 1, 1, 0, 0, 0, 0, time.UTC), false},
		"before the era": {time.Date(1990, 1, 1, 0, 0, 0, 0, time.UTC), false},
	}
	for name, c := range cases {
		err := CheckEvent(evAt(c.at), boundsNow, b)
		if (err == nil) != c.ok {
			t.Errorf("%s: err=%v want ok=%v", name, err, c.ok)
		}
		if err != nil && (!errors.Is(err, ErrOutOfBounds) || !errors.Is(err, ErrBadLine)) {
			t.Errorf("%s: %v does not wrap ErrOutOfBounds and ErrBadLine", name, err)
		}
	}
}

func TestCheckEventReplayOption(t *testing.T) {
	b := Bounds{Window: 24 * time.Hour, AllowReplay: true}
	if err := CheckEvent(evAt(boundsNow.Add(-400*24*time.Hour)), boundsNow, b); err != nil {
		t.Fatalf("old log refused with allow-replay: %v", err)
	}
	if err := CheckEvent(evAt(boundsNow.Add(48*time.Hour)), boundsNow, b); err == nil {
		t.Fatal("future event accepted with allow-replay")
	}
	if err := CheckEvent(evAt(time.Date(1999, 1, 1, 0, 0, 0, 0, time.UTC)), boundsNow, b); err == nil {
		t.Fatal("pre-2004 event accepted with allow-replay")
	}
}

func TestCheckEventValueBounds(t *testing.T) {
	b := Bounds{Window: 24 * time.Hour}
	withInfo := func(f func(in *domain.EventInfo)) domain.Event {
		ev := evAt(boundsNow)
		ev.Info = &domain.EventInfo{GUID: "Player-1-A", HP: 100, MaxHP: 200, HasHP: true, X: 10, Y: -20, HasPos: true, MapID: 530, Level: 70}
		f(ev.Info)
		return ev
	}
	bad := map[string]domain.Event{
		"hp negative":      withInfo(func(in *domain.EventInfo) { in.HP = -1 }),
		"hp huge":          withInfo(func(in *domain.EventInfo) { in.HP = 1e15 }),
		"maxhp huge":       withInfo(func(in *domain.EventInfo) { in.MaxHP = math.MaxFloat64 }),
		"hp NaN":           withInfo(func(in *domain.EventInfo) { in.HP = math.NaN() }),
		"hp inf":           withInfo(func(in *domain.EventInfo) { in.MaxHP = math.Inf(1) }),
		"x huge":           withInfo(func(in *domain.EventInfo) { in.X = 1e9 }),
		"y -inf":           withInfo(func(in *domain.EventInfo) { in.Y = math.Inf(-1) }),
		"x NaN":            withInfo(func(in *domain.EventInfo) { in.X = math.NaN() }),
		"map negative":     withInfo(func(in *domain.EventInfo) { in.MapID = -1 }),
		"map huge":         withInfo(func(in *domain.EventInfo) { in.MapID = 1 << 40 }),
		"level 255":        withInfo(func(in *domain.EventInfo) { in.Level = 255 }),
		"level negative":   withInfo(func(in *domain.EventInfo) { in.Level = -3 }),
		"guid too long":    withInfo(func(in *domain.EventInfo) { in.GUID = "Player-" + strings.Repeat("A", 500) }),
		"owner too long":   withInfo(func(in *domain.EventInfo) { in.Owner = strings.Repeat("A", 500) }),
		"amount negative":  {Name: "SPELL_DAMAGE", TimeMs: boundsNow.UnixMilli(), HasAmount: true, Amount: -5},
		"amount huge":      {Name: "SPELL_DAMAGE", TimeMs: boundsNow.UnixMilli(), HasAmount: true, Amount: 1e300},
		"amount NaN":       {Name: "SPELL_DAMAGE", TimeMs: boundsNow.UnixMilli(), HasAmount: true, Amount: math.NaN()},
		"record name huge": {Name: strings.Repeat("X", 5000), TimeMs: boundsNow.UnixMilli()},
		"src guid huge":    {Name: "A", TimeMs: boundsNow.UnixMilli(), Src: domain.EventUnit{GUID: strings.Repeat("a", 65)}},
	}
	for name, ev := range bad {
		if err := CheckEvent(ev, boundsNow, b); !errors.Is(err, ErrOutOfBounds) {
			t.Errorf("%s: %v", name, err)
		}
	}
	good := withInfo(func(*domain.EventInfo) {})
	good.HasAmount, good.Amount = true, 12345
	if err := CheckEvent(good, boundsNow, b); err != nil {
		t.Fatalf("sane event refused: %v", err)
	}
	edge := withInfo(func(in *domain.EventInfo) { in.X, in.Y, in.MapID, in.Level, in.HP = 100000, -100000, 65535, 120, 0 })
	if err := CheckEvent(edge, boundsNow, b); err != nil {
		t.Fatalf("edge values refused: %v", err)
	}
}

func TestSanitizeEventCleansNames(t *testing.T) {
	ev := domain.Event{
		Name: "SPELL_DAMAGE", Zone: "Zone‮evil\nline",
		Src:   domain.EventUnit{GUID: "Player-1-A", Raw: "Ali\x00ce\r\n‮-Realm"},
		Dst:   domain.EventUnit{GUID: "Player-1-B", Raw: strings.Repeat("N", 5000)},
		Chart: &domain.Chart{MapID: 1, Name: "Map\x07​Name"},
	}
	SanitizeEvent(&ev)
	if ev.Src.Raw != "Alice  -Realm" {
		t.Fatalf("src %q", ev.Src.Raw)
	}
	if len([]rune(ev.Dst.Raw)) != domain.ZoneMaxRunes {
		t.Fatalf("dst len %d", len([]rune(ev.Dst.Raw)))
	}
	if ev.Zone != "Zoneevil line" || ev.Chart.Name != "MapName" {
		t.Fatalf("%q %q", ev.Zone, ev.Chart.Name)
	}
}

type clock struct{ t time.Time }

func (c clock) now() time.Time { return c.t }

func newBoundsHost(t *testing.T, args ...string) (*core.Host, *recorder) {
	t.Helper()
	host := core.NewHost()
	m := New()
	m.now = clock{boundsNow}.now
	if err := host.Register(m); err != nil {
		t.Fatal(err)
	}
	rec := &recorder{}
	host.Parsers.Add(rec)
	if err := host.Config.Load(args, func(string) (string, bool) { return "", false }); err != nil {
		t.Fatal(err)
	}
	return host, rec
}

func line(ts string) domain.RawLine {
	return domain.RawLine{Source: "s", Text: ts + "  SPELL_DAMAGE,Player-1-A,\"Ali‮ce\",0x511,0x0,Creature-0-1,\"Boar\",0x10a48,0x0,1,\"Hit\",0x1,50,0,1,0,0,0,nil,nil,nil"}
}

func TestModuleRejectsOutOfWindowEvents(t *testing.T) {
	host, rec := newBoundsHost(t)
	ctx := context.Background()
	if err := host.Parsers.Feed(ctx, line("11/11/2026 11:59:59.000")); err != nil {
		t.Fatalf("fresh line: %v", err)
	}
	if err := host.Parsers.Feed(ctx, line("11/9/2026 11:59:59.000")); !errors.Is(err, ErrOutOfBounds) {
		t.Fatalf("two-day-old line: %v", err)
	}
	if err := host.Parsers.Feed(ctx, line("11/13/2026 11:59:59.000")); !errors.Is(err, ErrOutOfBounds) {
		t.Fatalf("two-days-ahead line: %v", err)
	}
	if len(rec.events) != 1 {
		t.Fatalf("%d events reached the parsers, want 1", len(rec.events))
	}
	if got := rec.events[0].Src.Raw; got != "Alice" {
		t.Fatalf("name not sanitised: %q", got)
	}
}

func TestModuleReplayOptionAndWindowSetting(t *testing.T) {
	host, rec := newBoundsHost(t, "--logparse-allow-replay=true")
	if err := host.Parsers.Feed(context.Background(), line("1/2/2025 10:00:00.000")); err != nil || len(rec.events) != 1 {
		t.Fatalf("replay refused: %v %d", err, len(rec.events))
	}
	host, rec = newBoundsHost(t, "--logparse-window=1h")
	if err := host.Parsers.Feed(context.Background(), line("11/11/2026 09:00:00.000")); err == nil || len(rec.events) != 0 {
		t.Fatalf("3 h old line accepted with a 1 h window: %v", err)
	}
	if got := host.Config.EnvName(SettingAllowReplay); got != "RADAR_LOGPARSE_ALLOW_REPLAY" {
		t.Fatal(got)
	}
}
