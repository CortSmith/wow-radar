package domain

import (
	"errors"
	"testing"
)

func TestParseRuleset(t *testing.T) {
	tests := []struct {
		in      string
		want    Ruleset
		wantErr bool
	}{
		{"normal", RulesetNormal, false},
		{"PVP", RulesetPvP, false},
		{"  rp ", RulesetRP, false},
		{"rppvp", RulesetRPPvP, false},
		{"", "", true},
		{"hardcore", "", true},
	}
	for _, tt := range tests {
		got, err := ParseRuleset(tt.in)
		if (err != nil) != tt.wantErr || got != tt.want {
			t.Errorf("ParseRuleset(%q) = %q, %v; want %q, err=%v", tt.in, got, err, tt.want, tt.wantErr)
		}
		if err != nil && !errors.Is(err, ErrInvalid) {
			t.Errorf("ParseRuleset(%q) error %v does not wrap ErrInvalid", tt.in, err)
		}
	}
}

func TestDefaultRuleset(t *testing.T) {
	if DefaultRuleset() != RulesetPvP {
		t.Fatalf("default ruleset = %q, want pvp", DefaultRuleset())
	}
}

func TestRulesetValid(t *testing.T) {
	if !RulesetRP.Valid() || Ruleset("x").Valid() {
		t.Fatal("Valid() wrong")
	}
}

func TestAllRulesets(t *testing.T) {
	if got := AllRulesets(); len(got) != 4 {
		t.Fatalf("AllRulesets() = %v", got)
	}
}
