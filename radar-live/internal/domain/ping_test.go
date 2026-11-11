package domain

import (
	"errors"
	"math"
	"strings"
	"testing"
)

func validPing() Ping {
	return Ping{
		RS:      RulesetPvP,
		BatchID: "src1:42",
		Source:  "src1",
		Contacts: []PingContact{{
			Contact: Contact{GUID: "Player-1234-0ABC", Name: "Alpha"},
			D:       Deltas{Events: 2, DmgTotal: 10.5},
			Fixes:   []Fix{{T: 1000, MapID: 1, X: 1, Y: 1, Kind: FixExact}},
		}},
		Charts: []Chart{{MapID: 1, Name: "Zone", X0: 0, X1: 1, Y0: 0, Y1: 1}},
		Deaths: []Death{{ID: "d1", T: 5, Victim: "Player-1234-0ABC"}},
	}
}

func TestPingValidate(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Ping)
		ok     bool
	}{
		{"valid", func(*Ping) {}, true},
		{"empty ping is valid", func(p *Ping) { p.Contacts, p.Charts, p.Deaths = nil, nil, nil }, true},
		{"bad ruleset", func(p *Ping) { p.RS = "hc" }, false},
		{"missing batch id", func(p *Ping) { p.BatchID = "" }, false},
		{"batch id with spaces", func(p *Ping) { p.BatchID = "a b" }, false},
		{"batch id too long", func(p *Ping) { p.BatchID = strings.Repeat("a", 65) }, false},
		{"source too long", func(p *Ping) { p.Source = strings.Repeat("a", 65) }, false},
		{"too many contacts", func(p *Ping) { p.Contacts = make([]PingContact, PingMaxContacts+1) }, false},
		{"too many charts", func(p *Ping) { p.Charts = make([]Chart, PingMaxCharts+1) }, false},
		{"too many deaths", func(p *Ping) { p.Deaths = make([]Death, PingMaxDeaths+1) }, false},
		{"bad guid", func(p *Ping) { p.Contacts[0].GUID = "Creature-1" }, false},
		{"too many fixes", func(p *Ping) { p.Contacts[0].Fixes = make([]Fix, PingMaxFixesPerContact+1) }, false},
		{"negative delta", func(p *Ping) { p.Contacts[0].D.KB = -1 }, false},
		{"nan delta", func(p *Ping) { p.Contacts[0].D.DmgTotal = math.NaN() }, false},
		{"bad chart", func(p *Ping) { p.Charts[0].X1 = math.Inf(1) }, false},
		{"death without id", func(p *Ping) { p.Deaths[0].ID = "" }, false},
	}
	for _, tt := range tests {
		p := validPing()
		tt.mutate(&p)
		err := p.Validate()
		if (err == nil) != tt.ok {
			t.Errorf("%s: Validate() = %v, want ok=%v", tt.name, err, tt.ok)
		}
		if err != nil && !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: error %v does not wrap ErrInvalid", tt.name, err)
		}
	}
}

func TestDeltasIsZero(t *testing.T) {
	if !(Deltas{}).IsZero() || (Deltas{Assists: 1}).IsZero() || (Deltas{HPSum: 0.1}).IsZero() {
		t.Fatal("IsZero wrong")
	}
}

func TestChartValid(t *testing.T) {
	if !(Chart{MapID: 0}).Valid() || (Chart{MapID: -1}).Valid() || (Chart{Y0: math.NaN()}).Valid() {
		t.Fatal("Chart.Valid wrong")
	}
}
