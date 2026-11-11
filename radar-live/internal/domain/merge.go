package domain

import (
	"slices"
	"strings"
)

// Field limits applied while merging.
const (
	NameMaxChars     = 48
	ZoneNameMaxChars = 64
	LevelMax         = 200
)

// MergeContact folds one ping observation into the stored contact (nil when unseen) and returns the
// result without modifying its inputs. Counters are added, so the caller must apply each batch once.
//
// Rules: firstSeen is the minimum; lastSeen and maxHp are the maximum; the newest sighting decides
// name, raw name and reaction (a known reaction is never replaced by "unknown") and the loser is
// remembered in PastNames; the position only moves forward in game time.
func MergeContact(stored *Contact, in PingContact) Contact {
	var merged Contact
	if stored != nil {
		merged = stored.Clone()
	}
	merged.GUID = in.GUID
	mergeTimes(&merged, stored, in)
	merged.Level = min(max(merged.Level, in.Level), LevelMax)
	merged.MaxHP = Round1(max(merged.MaxHP, in.MaxHP))
	mergeIdentity(&merged, stored, in)
	mergePosition(&merged, stored, in)
	addDeltas(&merged, in.D)
	return merged
}

func mergeTimes(merged *Contact, stored *Contact, in PingContact) {
	first := in.FirstSeen
	if first <= 0 {
		first = in.LastSeen
	}
	if stored != nil && stored.FirstSeen > 0 {
		first = min(first, stored.FirstSeen)
	}
	merged.FirstSeen = first
	merged.LastSeen = max(merged.LastSeen, in.LastSeen)
}

// mergeIdentity applies the newest-sighting-wins rule for name, raw name and reaction.
func mergeIdentity(merged *Contact, stored *Contact, in PingContact) {
	names := newNameList(merged.PastNames)
	inName := truncateRunes(in.Name, NameMaxChars)
	if stored == nil || in.LastSeen >= stored.LastSeen {
		names.remember(merged.Name)
		merged.Name = firstNonEmpty(inName, merged.Name)
		merged.Raw = firstNonEmpty(truncateRunes(in.Raw, NameMaxChars), merged.Raw)
		if in.Reaction != ReactionUnknown && in.Reaction != "" || merged.Reaction == "" {
			merged.Reaction = ParseReaction(string(in.Reaction))
		}
	} else {
		names.remember(inName)
	}
	for _, past := range in.PastNames {
		names.remember(truncateRunes(past, NameMaxChars))
	}
	merged.PastNames = names.without(merged.Name, PastNamesKeptMax)
}

// nameList is an insertion-ordered set of non-empty names.
type nameList []string

func newNameList(existing []string) *nameList {
	list := nameList(slices.Clone(existing))
	return &list
}

func (l *nameList) remember(name string) {
	if name != "" && !slices.Contains(*l, name) {
		*l = append(*l, name)
	}
}

// without returns the newest keep names excluding the current name, or nil when none remain.
func (l *nameList) without(current string, keep int) []string {
	kept := slices.DeleteFunc(slices.Clone(*l), func(n string) bool { return n == current })
	if len(kept) > keep {
		kept = kept[len(kept)-keep:]
	}
	if len(kept) == 0 {
		return nil
	}
	return kept
}

func mergePosition(merged *Contact, stored *Contact, in PingContact) {
	if !in.HasPosition() {
		return
	}
	if stored != nil && stored.HasPosition() && in.PosTime < stored.PosTime {
		return
	}
	merged.X, merged.Y, merged.MapID, merged.PosTime = Round1(in.X), Round1(in.Y), in.MapID, in.PosTime
	merged.Zone = firstNonEmpty(truncateRunes(in.Zone, ZoneNameMaxChars), merged.Zone)
}

func addDeltas(c *Contact, d Deltas) {
	c.Events += d.Events
	c.SrcEvents += d.SrcEvents
	c.DmgHits += d.DmgHits
	c.DmgEvents += d.DmgEvents
	c.HPSamples += d.HPSamples
	c.KB += d.KB
	c.Assists += d.Assists
	c.DmgTotal = Round1(c.DmgTotal + d.DmgTotal)
	c.HPSum = Round1(c.HPSum + d.HPSum)
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

func truncateRunes(s string, limit int) string {
	s = strings.TrimSpace(s)
	if r := []rune(s); len(r) > limit {
		return string(r[:limit])
	}
	return s
}
