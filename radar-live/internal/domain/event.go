package domain

import "strings"

// RawLine is one unparsed combat-log line tagged with the source (log owner connection) it came from.
// A streaming consumer feeds RawLines to the parser socket; the logparse module turns them into Events.
type RawLine struct {
	Source string
	Text   string
	// Ruleset, when set, overrides the ruleset detected from the log for the resulting Events.
	Ruleset Ruleset
}

// GUIDKind classifies a unit GUID by its prefix.
type GUIDKind int

// GUID kinds.
const (
	KindOther GUIDKind = iota
	KindPlayer
	KindNPC // Creature and Vehicle
	KindPet
)

// KindOf classifies guid.
func KindOf(guid string) GUIDKind {
	switch {
	case strings.HasPrefix(guid, "Player-"):
		return KindPlayer
	case strings.HasPrefix(guid, "Creature-"), strings.HasPrefix(guid, "Vehicle-"):
		return KindNPC
	case strings.HasPrefix(guid, "Pet-"):
		return KindPet
	}
	return KindOther
}

// Unit-flag bits of the combat log.
const (
	flagMine     = 0x1
	flagFriendly = 0x10
	flagNeutral  = 0x20
	flagHostile  = 0x40
)

// ReactionFromFlags maps unit flags to a Reaction: mine or friendly, else hostile, else neutral.
func ReactionFromFlags(flags uint32) Reaction {
	switch {
	case flags&(flagMine|flagFriendly) != 0:
		return ReactionFriendly
	case flags&flagHostile != 0:
		return ReactionHostile
	case flags&flagNeutral != 0:
		return ReactionNeutral
	}
	return ReactionUnknown
}

// EventUnit is a source or destination unit named on a log line. GUID and Raw are empty for "no unit".
type EventUnit struct {
	GUID      string
	Raw       string
	Flags     uint32
	RaidFlags uint32
}

// Kind classifies the unit's GUID.
func (u EventUnit) Kind() GUIDKind { return KindOf(u.GUID) }

// IsPlayer reports whether the unit is a player.
func (u EventUnit) IsPlayer() bool { return u.Kind() == KindPlayer }

// IsMine reports whether the unit is the log owner (flag 0x1 on a player GUID).
func (u EventUnit) IsMine() bool { return u.IsPlayer() && u.Flags&flagMine != 0 }

// Reaction maps the unit's flags to a Reaction.
func (u EventUnit) Reaction() Reaction { return ReactionFromFlags(u.Flags) }

// EventInfo is the advanced-logging block: the state of one "info unit" at the time of the event.
// X is north and Y is west in the client's orientation.
type EventInfo struct {
	GUID   string
	Owner  string // owner GUID of a pet or totem, "" for none
	HP     float64
	MaxHP  float64
	HasHP  bool
	X, Y   float64
	MapID  int
	Facing float64
	Level  int
	HasPos bool
}

// Event is one parsed combat-log line, produced by logparse and consumed by contactx and bountyx.
// TimeMs is UTC epoch milliseconds.
type Event struct {
	Source          string
	TimeMs          int64
	Name            string // record name, e.g. SPELL_DAMAGE
	Src, Dst        EventUnit
	Info            *EventInfo
	Amount          float64
	HasAmount       bool
	IsDamage        bool // a damage event, excluding *_LANDED duplicates
	IsHeal          bool
	Unconscious     bool // UNIT_DIED with the unconscious flag set (feign death), not a real death
	Zone            string
	Chart           *Chart // MAP_CHANGE
	AdvancedEnabled *bool  // COMBAT_LOG_VERSION
	Ruleset         Ruleset
}

// minNamesForSuffix is how many distinct player names must share a suffix before it is trusted.
const minNamesForSuffix = 6

// maxNamerNames bounds the memory spent on detection; further names are ignored until it locks.
const maxNamerNames = 5000

// Namer turns raw log names ("First-Last-PvP-US") into display names ("First Last"). The shared
// realm suffix is either configured or detected once enough distinct names share trailing tokens;
// names with another suffix do not prevent detection and are shown with dashes as spaces.
// A Namer is not safe for concurrent use.
type Namer struct {
	suffix string
	locked bool
	seen   map[string]struct{}
	tails  map[string]int // trailing-token run ("-PvP-US") to distinct names ending in it
}

// NewNamer returns a Namer; a non-empty realmSuffix disables detection.
func NewNamer(realmSuffix string) *Namer {
	n := &Namer{seen: map[string]struct{}{}, tails: map[string]int{}}
	if realmSuffix != "" {
		if !strings.HasPrefix(realmSuffix, "-") {
			realmSuffix = "-" + realmSuffix
		}
		n.suffix, n.locked = realmSuffix, true
	}
	return n
}

// Observe records a raw player name for suffix detection.
func (n *Namer) Observe(raw string) {
	if n.locked || raw == "" || len(n.seen) >= maxNamerNames {
		return
	}
	if _, ok := n.seen[raw]; ok {
		return
	}
	n.seen[raw] = struct{}{}
	best := ""
	for _, tail := range tailsOf(raw) {
		n.tails[tail]++
		if n.tails[tail] >= minNamesForSuffix && len(tail) > len(best) {
			best = tail
		}
	}
	if best != "" {
		n.suffix, n.locked = best, true
		n.seen, n.tails = nil, nil
	}
}

// Suffix returns the realm suffix in use ("" if none yet).
func (n *Namer) Suffix() string { return n.suffix }

// Display returns the display name for raw: suffix removed, dashes turned into spaces.
func (n *Namer) Display(raw string) string {
	if n.suffix != "" {
		raw = strings.TrimSuffix(raw, n.suffix)
	}
	return strings.ReplaceAll(raw, "-", " ")
}

// tailsOf returns every run of trailing '-' separated tokens of raw ("-US", "-PvP-US"), always
// leaving at least one token as the name.
func tailsOf(raw string) []string {
	tokens := strings.Split(raw, "-")
	out := make([]string, 0, len(tokens)-1)
	for k := 1; k < len(tokens); k++ {
		out = append(out, "-"+strings.Join(tokens[len(tokens)-k:], "-"))
	}
	return out
}
