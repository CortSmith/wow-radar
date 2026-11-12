package domain

import (
	"errors"
	"math"
	"strings"
	"testing"
	"time"
)

var hardenNow = time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)

// hardenPing is a valid ping whose timestamps sit inside the window around hardenNow.
func hardenPing() Ping {
	ms := hardenNow.UnixMilli()
	return Ping{
		RS: RulesetPvP, BatchID: "src1:42", Source: "src1",
		Contacts: []PingContact{{
			Contact: Contact{GUID: "Player-1234-0ABC", Name: "Alpha", Raw: "Alpha-Realm", Zone: "Arena", Level: 80,
				MaxHP: 5000, X: 100, Y: -200, MapID: 1, PosTime: ms, FirstSeen: ms - 1000, LastSeen: ms,
				Reaction: ReactionHostile, PastNames: []string{"Old"}},
			D:     Deltas{Events: 2, DmgTotal: 10.5},
			Fixes: []Fix{{T: ms, MapID: 1, X: 1, Y: 1, Kind: FixExact}},
		}},
		Charts: []Chart{{MapID: 1, Name: "Zone", X0: 0, X1: 1, Y0: 0, Y1: 1}},
		Deaths: []Death{{ID: "abc123", T: ms, Victim: "Player-1234-0ABC", Killer: "Player-9", Assists: []string{"Player-8"}}},
	}
}

func TestValidateAtBoundaries(t *testing.T) {
	ms := hardenNow.UnixMilli()
	day := (24 * time.Hour).Milliseconds()
	tests := []struct {
		name   string
		mutate func(*Ping)
		ok     bool
	}{
		{"valid", func(*Ping) {}, true},
		// timestamps
		{"lastSeen exactly 24h ahead", func(p *Ping) { p.Contacts[0].LastSeen = ms + day }, true},
		{"lastSeen 24h+1ms ahead", func(p *Ping) { p.Contacts[0].LastSeen = ms + day + 1 }, false},
		{"firstSeen 24h+1ms ahead", func(p *Ping) { p.Contacts[0].FirstSeen = ms + day + 1 }, false},
		{"lastSeen before 2004", func(p *Ping) { p.Contacts[0].LastSeen = MinTimestampMs - 1 }, false},
		{"lastSeen at 2004", func(p *Ping) { p.Contacts[0].FirstSeen, p.Contacts[0].LastSeen = MinTimestampMs, MinTimestampMs }, true},
		{"lastSeen negative", func(p *Ping) { p.Contacts[0].LastSeen = -5 }, false},
		{"zero times allowed", func(p *Ping) { p.Contacts[0].FirstSeen, p.Contacts[0].LastSeen, p.Contacts[0].PosTime = 0, 0, 0 }, true},
		{"firstSeen after lastSeen", func(p *Ping) { p.Contacts[0].FirstSeen = p.Contacts[0].LastSeen + 1 }, false},
		{"posTime in far future", func(p *Ping) { p.Contacts[0].PosTime = ms + day + 1 }, false},
		{"fix in far future", func(p *Ping) { p.Contacts[0].Fixes[0].T = ms + day + 1 }, false},
		{"fix before 2004", func(p *Ping) { p.Contacts[0].Fixes[0].T = 1000 }, false},
		{"death in far future", func(p *Ping) { p.Deaths[0].T = ms + day + 1 }, false},
		{"death before 2004", func(p *Ping) { p.Deaths[0].T = 5 }, false},
		{"timestamp over max", func(p *Ping) { p.Contacts[0].LastSeen = TimestampMaxMs + 1 }, false},
		// coordinates
		{"x at bound", func(p *Ping) { p.Contacts[0].X = CoordMax }, true},
		{"x over bound", func(p *Ping) { p.Contacts[0].X = CoordMax + 0.1 }, false},
		{"y under bound", func(p *Ping) { p.Contacts[0].Y = -CoordMax - 0.1 }, false},
		{"x nan", func(p *Ping) { p.Contacts[0].X = math.NaN() }, false},
		{"y inf", func(p *Ping) { p.Contacts[0].Y = math.Inf(-1) }, false},
		{"fix x over bound", func(p *Ping) { p.Contacts[0].Fixes[0].X = 1e9 }, false},
		{"fix y nan", func(p *Ping) { p.Contacts[0].Fixes[0].Y = math.NaN() }, false},
		{"fix map negative", func(p *Ping) { p.Contacts[0].Fixes[0].MapID = -1 }, false},
		{"fix map over", func(p *Ping) { p.Contacts[0].Fixes[0].MapID = MaxMapID + 1 }, false},
		{"fix kind unknown", func(p *Ping) { p.Contacts[0].Fixes[0].Kind = "<b>" }, false},
		{"fix kind empty", func(p *Ping) { p.Contacts[0].Fixes[0].Kind = "" }, true},
		// map ids, levels, hp
		{"map at bound", func(p *Ping) { p.Contacts[0].MapID = MaxMapID }, true},
		{"map over bound", func(p *Ping) { p.Contacts[0].MapID = MaxMapID + 1 }, false},
		{"map negative", func(p *Ping) { p.Contacts[0].MapID = -1 }, false},
		{"level 0", func(p *Ping) { p.Contacts[0].Level = 0 }, true},
		{"level max", func(p *Ping) { p.Contacts[0].Level = PingLevelMax }, true},
		{"level over", func(p *Ping) { p.Contacts[0].Level = PingLevelMax + 1 }, false},
		{"level negative", func(p *Ping) { p.Contacts[0].Level = -1 }, false},
		{"maxHp at bound", func(p *Ping) { p.Contacts[0].MaxHP = MaxHPLimit }, true},
		{"maxHp over", func(p *Ping) { p.Contacts[0].MaxHP = MaxHPLimit + 1 }, false},
		{"maxHp negative", func(p *Ping) { p.Contacts[0].MaxHP = -1 }, false},
		{"maxHp nan", func(p *Ping) { p.Contacts[0].MaxHP = math.NaN() }, false},
		{"reaction junk", func(p *Ping) { p.Contacts[0].Reaction = "<img>" }, false},
		{"reaction empty", func(p *Ping) { p.Contacts[0].Reaction = "" }, true},
		// deltas
		{"delta events at cap", func(p *Ping) { p.Contacts[0].D.Events = DeltaMaxCount }, true},
		{"delta events over cap", func(p *Ping) { p.Contacts[0].D.Events = DeltaMaxCount + 1 }, false},
		{"delta kb over cap", func(p *Ping) { p.Contacts[0].D.KB = DeltaMaxCount + 1 }, false},
		{"delta dmg at cap", func(p *Ping) { p.Contacts[0].D.DmgTotal = DeltaMaxValue }, true},
		{"delta dmg over cap", func(p *Ping) { p.Contacts[0].D.DmgTotal = DeltaMaxValue * 2 }, false},
		{"delta hpSum over cap", func(p *Ping) { p.Contacts[0].D.HPSum = DeltaMaxValue * 2 }, false},
		{"delta negative", func(p *Ping) { p.Contacts[0].D.Assists = -1 }, false},
		{"delta int64 max", func(p *Ping) { p.Contacts[0].D.Events = math.MaxInt64 }, false},
		// charts
		{"chart x over bound", func(p *Ping) { p.Charts[0].X1 = CoordMax + 1 }, false},
		{"chart y under bound", func(p *Ping) { p.Charts[0].Y0 = -CoordMax - 1 }, false},
		{"chart at bound", func(p *Ping) { p.Charts[0].X0, p.Charts[0].X1 = -CoordMax, CoordMax }, true},
		{"chart map over", func(p *Ping) { p.Charts[0].MapID = MaxMapID + 1 }, false},
		{"chart name newline", func(p *Ping) { p.Charts[0].Name = "a\nb" }, false},
		{"chart name too long", func(p *Ping) { p.Charts[0].Name = strings.Repeat("a", NameMaxRunes*2+1) }, false},
		// names
		{"name at max", func(p *Ping) { p.Contacts[0].Name = strings.Repeat("é", NameMaxRunes) }, true},
		{"name over max", func(p *Ping) { p.Contacts[0].Name = strings.Repeat("é", NameMaxRunes+1) }, false},
		{"name newline", func(p *Ping) { p.Contacts[0].Name = "a\nb" }, false},
		{"name nul", func(p *Ping) { p.Contacts[0].Name = "a\x00b" }, false},
		{"name bidi override", func(p *Ping) { p.Contacts[0].Name = "a‮b" }, false},
		{"name invalid utf8", func(p *Ping) { p.Contacts[0].Name = "a\xffb" }, false},
		{"raw bidi", func(p *Ping) { p.Contacts[0].Raw = "a⁧b" }, false},
		{"zone control", func(p *Ping) { p.Contacts[0].Zone = "a\rb" }, false},
		{"zone too long", func(p *Ping) { p.Contacts[0].Zone = strings.Repeat("z", ZoneMaxRunes+1) }, false},
		{"past name tab", func(p *Ping) { p.Contacts[0].PastNames = []string{"a\tb"} }, false},
		{"too many past names", func(p *Ping) { p.Contacts[0].PastNames = make([]string, PastNamesKeptMax+1) }, false},
		{"source control", func(p *Ping) { p.Source = "a\nb" }, false},
		{"xss name is plain text", func(p *Ping) { p.Contacts[0].Name = "<script>alert(1)</script>" }, true},
		// deaths
		{"death victim not player", func(p *Ping) { p.Deaths[0].Victim = "Creature-1" }, false},
		{"death killer bad", func(p *Ping) { p.Deaths[0].Killer = "x\ny" }, false},
		{"death assist bad", func(p *Ping) { p.Deaths[0].Assists = []string{"nope"} }, false},
		{"death too many assists", func(p *Ping) { p.Deaths[0].Assists = make([]string, DeathMaxAssists+1) }, false},
		{"death id bad charset", func(p *Ping) { p.Deaths[0].ID = "a b" }, false},
		{"death id too long", func(p *Ping) { p.Deaths[0].ID = strings.Repeat("a", 129) }, false},
		{"death empty killer ok", func(p *Ping) { p.Deaths[0].Killer, p.Deaths[0].Assists = "", nil }, true},
		// fixes per ping
		{"too many fixes in total", func(p *Ping) {
			p.Contacts = make([]PingContact, 10)
			for i := range p.Contacts {
				p.Contacts[i] = hardenPing().Contacts[0]
				p.Contacts[i].Fixes = make([]Fix, PingMaxFixesPerContact)
				for j := range p.Contacts[i].Fixes {
					p.Contacts[i].Fixes[j] = Fix{T: ms, MapID: 1, Kind: FixExact}
				}
			}
		}, false},
	}
	for _, tt := range tests {
		p := hardenPing()
		tt.mutate(&p)
		err := p.ValidateAt(hardenNow)
		if (err == nil) != tt.ok {
			t.Errorf("%s: ValidateAt = %v, want ok=%v", tt.name, err, tt.ok)
		}
		if err != nil && !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: error does not wrap ErrInvalid", tt.name)
		}
	}
}

func TestValidateErrorsDoNotEchoInput(t *testing.T) {
	p := hardenPing()
	p.Contacts[0].GUID = "Player-<script>alert(1)</script>"
	if err := p.Validate(); err == nil || strings.Contains(err.Error(), "script") {
		t.Fatalf("guid error echoes input: %v", err)
	}
	p = hardenPing()
	p.RS = "<img src=x>"
	if err := p.Validate(); err == nil || strings.Contains(err.Error(), "img") {
		t.Fatalf("ruleset error echoes input: %v", err)
	}
	p = hardenPing()
	p.BatchID = "<b>"
	if err := p.Validate(); err == nil || strings.Contains(err.Error(), "<b>") {
		t.Fatalf("batch error echoes input: %v", err)
	}
}

// Validate (no clock) keeps the time window off so synthetic log timestamps in older tests still pass,
// but all other bounds apply.
func TestValidateWithoutClockSkipsWindowOnly(t *testing.T) {
	p := hardenPing()
	p.Contacts[0].LastSeen = 5
	p.Contacts[0].FirstSeen = 1
	p.Contacts[0].Fixes[0].T = 1000
	p.Deaths[0].T = 5
	if err := p.Validate(); err != nil {
		t.Fatalf("synthetic times rejected without clock: %v", err)
	}
	p.Contacts[0].X = CoordMax * 2
	if p.Validate() == nil {
		t.Fatal("coordinate bound skipped")
	}
}

func TestSanitize(t *testing.T) {
	p := hardenPing()
	p.Contacts[0].Name = "Al‮pha\nx\x00"
	p.Contacts[0].Raw = "Raw⁦"
	p.Contacts[0].Zone = "Zone\r\nEvil"
	p.Contacts[0].PastNames = []string{"ok", "bad​", strings.Repeat("n", 300)}
	p.Charts[0].Name = "Chart\x07"
	p.Source = "src\n"
	n := p.Sanitize()
	if n < 6 {
		t.Fatalf("repairs = %d", n)
	}
	c := p.Contacts[0]
	if c.Name != "Alpha x" || c.Raw != "Raw" || c.Zone != "Zone  Evil" || c.PastNames[1] != "bad" || p.Charts[0].Name != "Chart" || p.Source != "src" {
		t.Fatalf("sanitised wrongly: %+v %+v", c, p.Charts[0])
	}
	if len([]rune(c.PastNames[2])) != NameMaxRunes {
		t.Fatalf("past name not truncated: %d", len([]rune(c.PastNames[2])))
	}
	if err := p.ValidateAt(hardenNow); err != nil {
		t.Fatalf("sanitised ping invalid: %v", err)
	}
	clean := hardenPing()
	if n := clean.Sanitize(); n != 0 {
		t.Fatalf("clean ping reported %d repairs", n)
	}
}

func TestSanitizeCapsPastNames(t *testing.T) {
	p := hardenPing()
	for i := 0; i < 50; i++ {
		p.Contacts[0].PastNames = append(p.Contacts[0].PastNames, "n")
	}
	if p.Sanitize() == 0 || len(p.Contacts[0].PastNames) != PastNamesKeptMax {
		t.Fatalf("past names = %d", len(p.Contacts[0].PastNames))
	}
}

func TestChartValidBounds(t *testing.T) {
	if (Chart{MapID: MaxMapID + 1}).Valid() || (Chart{X1: CoordMax + 1}).Valid() || !(Chart{X1: CoordMax}).Valid() {
		t.Fatal("Chart.Valid bounds")
	}
}
