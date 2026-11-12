package contactx

import (
	"math"
	"slices"

	"radarlive/internal/domain"
)

// approxPreferMs: an exact position is kept unless an approximate one is newer by more than this.
const approxPreferMs = 30_000

// fix is a position sample inside the extractor.
type fix struct {
	t     int64
	x, y  float64
	mapID int
	zone  string
	kind  domain.FixKind
}

// unit is the state kept per tracked contact between flushes.
type unit struct {
	guid      string
	raw       string
	reaction  domain.Reaction
	level     int
	maxHP     float64
	firstSeen int64
	lastSeen  int64
	pastRaw   []string
	shown     string // display name published last
	pos       fix
	hasPos    bool
	lastFix   domain.Fix // newest trail fix seen, flushed or not (dedupe reference)
	hasLast   bool
	pending   []domain.Fix
	d         domain.Deltas
	dirty     bool
	touched   uint64 // recency counter for eviction
}

func newUnit(guid string, t int64) *unit {
	return &unit{guid: guid, firstSeen: t, lastSeen: t, reaction: domain.ReactionUnknown}
}

// observe updates identity and times for one event mention.
func (u *unit) observe(t int64, raw string, flags uint32) {
	u.firstSeen = min(u.firstSeen, t)
	u.lastSeen = max(u.lastSeen, t)
	if reaction := domain.ReactionFromFlags(flags); reaction != domain.ReactionUnknown {
		u.reaction = reaction
	}
	u.setRaw(raw)
	u.dirty = true
}

// setRaw records a (possibly changed) raw name, remembering the previous raw names.
func (u *unit) setRaw(raw string) {
	if raw == "" || raw == u.raw {
		return
	}
	if u.raw != "" && !slices.Contains(u.pastRaw, u.raw) {
		u.pastRaw = append(u.pastRaw, u.raw)
		if len(u.pastRaw) > domain.PastNamesKeptMax+1 {
			u.pastRaw = u.pastRaw[1:]
		}
	}
	u.raw = raw
}

// addFix applies a position sample: it may move the contact's current position and extend the trail.
func (u *unit) addFix(f fix) {
	if u.wantsPosition(f) {
		u.pos, u.hasPos = f, true
	}
	t := domain.Fix{T: f.t, MapID: f.mapID, X: domain.Round1(f.x), Y: domain.Round1(f.y), Kind: f.kind}
	if u.hasLast && !isNewTrailPoint(u.lastFix, t) {
		return
	}
	u.lastFix, u.hasLast = t, true
	u.pending = append(u.pending, t)
	if over := len(u.pending) - domain.TrailCapFixes; over > 0 {
		u.pending = slices.Delete(u.pending, 0, over)
	}
}

// wantsPosition applies the exact-beats-approximate rule for the current position.
func (u *unit) wantsPosition(f fix) bool {
	switch {
	case !u.hasPos:
		return true
	case f.t < u.pos.t:
		return false
	case f.kind == domain.FixApprox && u.pos.kind == domain.FixExact:
		return f.t-u.pos.t > approxPreferMs
	}
	return true
}

// isNewTrailPoint reports whether next adds information after last: strictly newer, and on another
// chart, far enough away, or late enough.
func isNewTrailPoint(last, next domain.Fix) bool {
	if next.T <= last.T {
		return false
	}
	if next.MapID != last.MapID || next.T-last.T >= domain.FixDedupeWindowMs {
		return true
	}
	return math.Hypot(next.X-last.X, next.Y-last.Y) >= domain.FixDedupeDistanceYd
}

// names returns the display name and past display names using display to strip realm suffixes.
func (u *unit) names(display func(string) string) (string, []string) {
	current := display(u.raw)
	var past []string
	for _, raw := range u.pastRaw {
		if name := display(raw); name != current && !slices.Contains(past, name) {
			past = append(past, name)
		}
	}
	if len(past) > domain.PastNamesKeptMax {
		past = past[len(past)-domain.PastNamesKeptMax:]
	}
	return current, past
}

// take builds the unit's ping entry and resets everything that is a delta.
func (u *unit) take(display func(string) string) domain.PingContact {
	name, past := u.names(display)
	if u.shown != "" && u.shown != name && !slices.Contains(u.pastRaw, u.shown) {
		// The display name changed with no rename (the realm suffix was detected): keep the old one.
		u.pastRaw = append(u.pastRaw, u.shown)
		if len(u.pastRaw) > domain.PastNamesKeptMax+1 {
			u.pastRaw = u.pastRaw[1:]
		}
		name, past = u.names(display)
	}
	u.shown = name
	c := domain.PingContact{Contact: domain.Contact{
		GUID: u.guid, Name: name, Raw: u.raw, PastNames: past, Reaction: u.reaction,
		Level: u.level, MaxHP: domain.Round1(u.maxHP), FirstSeen: u.firstSeen, LastSeen: u.lastSeen,
	}}
	if u.hasPos {
		c.X, c.Y, c.MapID = domain.Round1(u.pos.x), domain.Round1(u.pos.y), u.pos.mapID
		c.Zone, c.PosTime = u.pos.zone, u.pos.t
	}
	u.d.DmgTotal = domain.Round1(u.d.DmgTotal)
	u.d.HPSum = domain.Round1(u.d.HPSum)
	c.D, c.Fixes = u.d, u.pending
	u.d, u.pending, u.dirty = domain.Deltas{}, nil, false
	return c
}
