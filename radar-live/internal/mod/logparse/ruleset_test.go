package logparse

import (
	"testing"

	"radarlive/internal/domain"
)

func TestDetectRealmToken(t *testing.T) {
	cases := []struct {
		raw  string
		want domain.Ruleset
		ok   bool
	}{
		{"First-Last-PvP-US", domain.RulesetPvP, true},
		{"First-Normal-US", domain.RulesetNormal, true},
		{"First-RP-US", domain.RulesetRP, true},
		{"First-RP-PvP-US", domain.RulesetRPPvP, true},
		{"First-rppvp-EU", domain.RulesetRPPvP, true},
		{"Pvp", "", false},    // a character called PvP is not a realm
		{"Rp-Bob", "", false}, // the first token is always the name
		{"Name", "", false},
		{"A-B-C-D-E-PvP", domain.RulesetPvP, true},
		{"A-PvP-B-C-D-E", "", false}, // too far from the end
		{"", "", false},
	}
	for _, c := range cases {
		got, ok := DetectRealmToken(c.raw)
		if got != c.want || ok != c.ok {
			t.Errorf("%q: got (%q,%v) want (%q,%v)", c.raw, got, ok, c.want, c.ok)
		}
	}
}
