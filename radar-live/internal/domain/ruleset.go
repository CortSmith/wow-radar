package domain

import "strings"

// Ruleset names one game ruleset; each ruleset is a separate data partition.
type Ruleset string

// The four supported rulesets.
const (
	RulesetNormal Ruleset = "normal"
	RulesetPvP    Ruleset = "pvp"
	RulesetRP     Ruleset = "rp"
	RulesetRPPvP  Ruleset = "rppvp"
)

// AllRulesets lists every supported ruleset.
func AllRulesets() []Ruleset {
	return []Ruleset{RulesetNormal, RulesetPvP, RulesetRP, RulesetRPPvP}
}

// DefaultRuleset is used when a request names no ruleset and the server has no override.
func DefaultRuleset() Ruleset { return RulesetPvP }

// Valid reports whether r is one of the supported rulesets.
func (r Ruleset) Valid() bool {
	for _, known := range AllRulesets() {
		if r == known {
			return true
		}
	}
	return false
}

// ParseRuleset converts text (case-insensitive, trimmed) into a Ruleset.
func ParseRuleset(text string) (Ruleset, error) {
	r := Ruleset(strings.ToLower(strings.TrimSpace(text)))
	if !r.Valid() {
		return "", invalidf("unknown ruleset %q", text)
	}
	return r, nil
}
