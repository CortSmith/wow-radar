package domain

import "strings"

// Reaction is how the logging player's faction regards a contact.
type Reaction string

// Known reactions, taken from the combat-log unit flags.
const (
	ReactionFriendly Reaction = "friendly"
	ReactionHostile  Reaction = "hostile"
	ReactionNeutral  Reaction = "neutral"
	ReactionUnknown  Reaction = "unknown"
)

// ParseReaction converts text to a Reaction; anything unrecognised becomes ReactionUnknown.
func ParseReaction(text string) Reaction {
	switch r := Reaction(strings.ToLower(strings.TrimSpace(text))); r {
	case ReactionFriendly, ReactionHostile, ReactionNeutral:
		return r
	}
	return ReactionUnknown
}

// Group is a faction filter: the page shows Blue (friendly) and Red (hostile and everything else).
type Group string

// Group filter values.
const (
	GroupAny      Group = "any"
	GroupFriendly Group = "friendly"
	GroupHostile  Group = "hostile"
	GroupOther    Group = "other"
)

// GroupOf maps a reaction to the single group it belongs to.
func GroupOf(r Reaction) Group {
	switch r {
	case ReactionFriendly:
		return GroupFriendly
	case ReactionHostile:
		return GroupHostile
	}
	return GroupOther
}

// Valid reports whether g is a usable filter value; the empty group means "any".
func (g Group) Valid() bool {
	switch g {
	case "", GroupAny, GroupFriendly, GroupHostile, GroupOther:
		return true
	}
	return false
}

// Contains reports whether a contact with reaction r passes the group filter.
func (g Group) Contains(r Reaction) bool {
	return g == "" || g == GroupAny || g == GroupOf(r)
}
