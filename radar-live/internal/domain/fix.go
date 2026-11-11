package domain

import (
	"math"
	"slices"
)

// Trail limits and de-duplication thresholds.
const (
	// TrailCapFixes is the most fixes kept per contact; the oldest are dropped first.
	TrailCapFixes = 5000
	// FixDedupeDistanceYd and FixDedupeWindowMs: a fix this near and this soon after the previous one is redundant.
	FixDedupeDistanceYd = 2.0
	FixDedupeWindowMs   = 5000
	// TimestampMaxMs bounds believable timestamps (year ~2096).
	TimestampMaxMs = 4_000_000_000_000
)

// FixKind says how a fix was obtained.
type FixKind string

// Fix kinds.
const (
	// FixExact comes from the contact's own advanced-log position.
	FixExact FixKind = "exact"
	// FixApprox is borrowed from the logging player's nearby position.
	FixApprox FixKind = "approx"
)

// Fix is one point of a contact's trail.
type Fix struct {
	T     int64   `json:"t"`
	MapID int     `json:"mapId"`
	X     float64 `json:"x"`
	Y     float64 `json:"y"`
	Kind  FixKind `json:"kind"`
}

// CleanFixes drops unusable fixes, rounds coordinates to 0.1 yd, normalises the kind and sorts by time.
// The input is not modified.
func CleanFixes(in []Fix) []Fix {
	out := make([]Fix, 0, len(in))
	for _, f := range in {
		if !isUsableFix(f) {
			continue
		}
		f.X, f.Y = Round1(f.X), Round1(f.Y)
		if f.Kind != FixApprox {
			f.Kind = FixExact
		}
		out = append(out, f)
	}
	slices.SortStableFunc(out, func(a, b Fix) int { return compareInt64(a.T, b.T) })
	return out
}

func isUsableFix(f Fix) bool {
	finite := !math.IsNaN(f.X) && !math.IsInf(f.X, 0) && !math.IsNaN(f.Y) && !math.IsInf(f.Y, 0)
	return finite && f.T > 0 && f.T <= TimestampMaxMs && f.MapID >= 0
}

func compareInt64(a, b int64) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

// isRedundantFix reports whether next adds nothing after previous: same chart, nearly in place, nearly simultaneous.
func isRedundantFix(previous, next Fix) bool {
	return previous.MapID == next.MapID &&
		next.T-previous.T < FixDedupeWindowMs &&
		math.Hypot(previous.X-next.X, previous.Y-next.Y) < FixDedupeDistanceYd
}

// AppendFixes adds incoming fixes to a time-ordered trail and returns the new trail.
// Only fixes strictly newer than the stored last fix are considered (so replays never duplicate),
// redundant ones are skipped, and the oldest fixes are dropped beyond TrailCapFixes.
func AppendFixes(trail, incoming []Fix) []Fix {
	out := slices.Clone(trail)
	for _, f := range CleanFixes(incoming) {
		if len(out) > 0 {
			last := out[len(out)-1]
			if f.T <= last.T || isRedundantFix(last, f) {
				continue
			}
		}
		out = append(out, f)
	}
	if over := len(out) - TrailCapFixes; over > 0 {
		out = slices.Clone(out[over:])
	}
	return out
}

// FixesForMap returns the fixes on one chart, or all fixes when mapID is nil.
func FixesForMap(trail []Fix, mapID *int) []Fix {
	if mapID == nil {
		return slices.Clone(trail)
	}
	return slices.DeleteFunc(slices.Clone(trail), func(f Fix) bool { return f.MapID != *mapID })
}

// FixesAfter returns the fixes with T strictly greater than since.
func FixesAfter(trail []Fix, since int64) []Fix {
	return slices.DeleteFunc(slices.Clone(trail), func(f Fix) bool { return f.T <= since })
}
