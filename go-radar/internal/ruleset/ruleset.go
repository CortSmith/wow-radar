// Package ruleset is the single source of truth for the four WoW server rule-sets
// (realm types): Normal, PvP, RP and RP-PvP. Each rule-set owns a separate dataset
// (history + bounty ledger); see package dataset.
package ruleset

import "strings"

// ID is the stable machine name of a rule-set (used in file names and the API).
type ID string

// The four rule-sets.
const (
	Normal ID = "normal"
	PvP    ID = "pvp"
	RP     ID = "rp"
	RPPvP  ID = "rppvp"
)

// Default is used when nothing is detected and nothing was ever chosen.
const Default = PvP

// Info pairs an id with its display name.
type Info struct {
	ID   ID     `json:"id"`
	Name string `json:"name"`
}

var all = []Info{
	{Normal, "Normal"},
	{PvP, "PvP"},
	{RP, "RP"},
	{RPPvP, "RP-PvP"},
}

// All returns the rule-sets in display order (a copy).
func All() []Info { return append([]Info(nil), all...) }

// NameOf returns the display name of id ("" if unknown).
func NameOf(id ID) string {
	for _, i := range all {
		if i.ID == id {
			return i.Name
		}
	}
	return ""
}

// Parse accepts (case-insensitively) normal, pvp, rp, rppvp, rp-pvp, rp_pvp and "rp pvp".
func Parse(s string) (ID, bool) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "normal":
		return Normal, true
	case "pvp":
		return PvP, true
	case "rp":
		return RP, true
	case "rppvp", "rp-pvp", "rp_pvp", "rp pvp":
		return RPPvP, true
	}
	return "", false
}

// Detect maps one realm token (the "PvP" of "Name-PvP-US") to a rule-set.
func Detect(realmToken string) (ID, bool) { return Parse(realmToken) }

// DetectFromRawName finds the realm token in a raw log name such as "First-Last-PvP-US"
// (name, realm, region). Only the trailing three '-' parts after the first are considered, so a
// character called "Rp" or "Normal" is not mistaken for a realm type. "RP-PvP" written as two
// tokens is recognised as RP-PvP.
func DetectFromRawName(raw string) (ID, bool) {
	parts := strings.Split(raw, "-")
	lo := max(1, len(parts)-3)
	for i := len(parts) - 1; i >= lo; i-- {
		if i-1 >= 1 && strings.EqualFold(parts[i], "pvp") && strings.EqualFold(parts[i-1], "rp") {
			return RPPvP, true
		}
		if id, ok := Detect(parts[i]); ok {
			return id, true
		}
	}
	return "", false
}
