package logparse

import (
	"strings"

	"radarlive/internal/domain"
)

// realmTokensChecked is how many trailing '-' parts of a raw name can hold the realm token.
const realmTokensChecked = 3

// DetectRealmToken finds the realm type in a raw log name such as "First-Last-PvP-US" (name, realm,
// region). Only the trailing three parts after the first are examined, so a character called "Rp" or
// "Normal" is not mistaken for a realm. "RP" followed by "PvP" is recognised as rppvp.
func DetectRealmToken(raw string) (domain.Ruleset, bool) {
	parts := strings.Split(raw, "-")
	lowest := max(1, len(parts)-realmTokensChecked)
	for i := len(parts) - 1; i >= lowest; i-- {
		if i >= 2 && strings.EqualFold(parts[i], "pvp") && strings.EqualFold(parts[i-1], "rp") {
			return domain.RulesetRPPvP, true
		}
		if rs, ok := tokenRuleset(parts[i]); ok {
			return rs, true
		}
	}
	return "", false
}

// tokenRuleset maps one realm token to a ruleset; rp-pvp spellings are accepted.
func tokenRuleset(token string) (domain.Ruleset, bool) {
	switch strings.ToLower(strings.TrimSpace(token)) {
	case "rp-pvp", "rp_pvp", "rp pvp":
		return domain.RulesetRPPvP, true
	}
	rs, err := domain.ParseRuleset(token)
	return rs, err == nil
}
