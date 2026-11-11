package parser

import "strings"

// Reaction is the unit's disposition towards the log owner.
type Reaction int

const (
	ReactionUnknown Reaction = iota
	ReactionFriendly
	ReactionNeutral
	ReactionHostile
)

func (r Reaction) String() string {
	switch r {
	case ReactionFriendly:
		return "friendly"
	case ReactionNeutral:
		return "neutral"
	case ReactionHostile:
		return "hostile"
	}
	return "unknown"
}

// IsMine reports whether the flags carry the "affiliation mine" bit, i.e. the unit is the log owner.
func IsMine(flags uint32) bool { return flags&0x1 != 0 }

// ReactionOf maps combat-log unit flags to a Reaction: 0x1 or 0x10 friendly; else 0x40 hostile; else 0x20 neutral.
func ReactionOf(flags uint32) Reaction {
	switch {
	case flags&0x11 != 0:
		return ReactionFriendly
	case flags&0x40 != 0:
		return ReactionHostile
	case flags&0x20 != 0:
		return ReactionNeutral
	}
	return ReactionUnknown
}

// GUIDKind classifies a GUID by its prefix.
type GUIDKind int

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

// Namer turns raw log names ("First-Last-PvP-US") into display names ("First Last").
// The shared realm suffix is either configured or detected once enough distinct names
// share a trailing run of tokens. Namer is NOT safe for concurrent use.
type Namer struct {
	suffix string
	locked bool
	seen   map[string]struct{}
	names  []string
}

// minNamesForSuffix is how many distinct player names must share a suffix before it is trusted.
const minNamesForSuffix = 6

// NewNamer returns a Namer; a non-empty realmSuffix disables detection.
func NewNamer(realmSuffix string) *Namer {
	n := &Namer{seen: map[string]struct{}{}}
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
	if n.locked || raw == "" {
		return
	}
	if _, ok := n.seen[raw]; ok {
		return
	}
	n.seen[raw] = struct{}{}
	n.names = append(n.names, raw)
	if len(n.names) < minNamesForSuffix {
		return
	}
	if s := commonSuffix(n.names); s != "" {
		n.suffix, n.locked = s, true
		n.names, n.seen = nil, nil
	}
}

// Suffix returns the realm suffix in use ("" if none yet).
func (n *Namer) Suffix() string { return n.suffix }

// Display returns the display name for raw.
func (n *Namer) Display(raw string) string {
	if n.suffix != "" {
		raw = strings.TrimSuffix(raw, n.suffix)
	}
	return strings.ReplaceAll(raw, "-", " ")
}

// commonSuffix returns the longest run of trailing '-' separated tokens shared by all names
// (always leaving at least one token as the name), formatted with a leading '-'.
func commonSuffix(names []string) string {
	if len(names) == 0 {
		return ""
	}
	lists := make([][]string, len(names))
	shortest := -1
	for i, nm := range names {
		lists[i] = strings.Split(nm, "-")
		if shortest < 0 || len(lists[i]) < shortest {
			shortest = len(lists[i])
		}
	}
	var best []string
	for k := 1; k < shortest; k++ {
		tail := lists[0][len(lists[0])-k:]
		ok := true
		for _, l := range lists[1:] {
			t := l[len(l)-k:]
			for j := range t {
				if t[j] != tail[j] {
					ok = false
				}
			}
		}
		if !ok {
			break
		}
		best = tail
	}
	if len(best) == 0 {
		return ""
	}
	return "-" + strings.Join(best, "-")
}
