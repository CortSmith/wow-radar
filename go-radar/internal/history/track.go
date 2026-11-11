package history

import (
	"math"
	"sort"
	"strings"
	"time"
)

// Position kinds stored in a Fix.
const (
	KindExact  = "exact"
	KindApprox = "approx"
)

// Track pruning constants.
const (
	// MinFixDistance (yards) and MinFixInterval: a new fix is skipped when it is closer than
	// MinFixDistance to the previous fix on the same map AND less than MinFixInterval later.
	MinFixDistance = 2.0
	MinFixInterval = 5 * time.Second
	// MaxTrackPerPlayer caps one player's track; the oldest fixes are dropped first.
	MaxTrackPerPlayer = 5000
	// MaxTotalFixes caps all tracks together (about 60 bytes each, so roughly 15 MB of JSON).
	// Beyond it the least recently seen players lose their whole track (aggregates are kept)
	// until the total is back under evictTarget of the cap.
	MaxTotalFixes = 250000
	evictTarget   = 0.9
	// MaxPastNames is the number of former names remembered per GUID.
	MaxPastNames = 10
	coordScale   = 10 // positions are stored with one decimal
)

// maxTotalFixes is MaxTotalFixes, overridable by tests.
var maxTotalFixes = MaxTotalFixes

// Fix is one recorded position of a player. T is unix milliseconds (log event time).
type Fix struct {
	T     int64   `json:"t"`
	MapID int     `json:"mapId"`
	Zone  string  `json:"zone,omitempty"`
	X     float64 `json:"x"`
	Y     float64 `json:"y"`
	Kind  string  `json:"kind"`
}

// MapCount is the number of fixes a player has on one map.
type MapCount struct {
	MapID int    `json:"mapId"`
	Zone  string `json:"zone,omitempty"`
	Count int    `json:"count"`
}

// TrackResult is a player's track, optionally restricted to one map.
type TrackResult struct {
	GUID     string     `json:"guid"`
	Name     string     `json:"name"`
	Reaction string     `json:"reaction"`
	Total    int        `json:"total"` // fixes on all maps
	Maps     []MapCount `json:"maps"`  // fixes per map, ascending map id
	Fixes    []Fix      `json:"fixes"` // chronological, restricted to the requested map if any
}

// Match is one Search result.
type Match struct {
	GUID      string    `json:"guid"`
	Name      string    `json:"name"`
	Raw       string    `json:"raw"`
	Reaction  string    `json:"reaction"`
	Fixes     int       `json:"fixes"`
	FirstSeen time.Time `json:"firstSeen"`
	LastSeen  time.Time `json:"lastSeen"`
}

func roundCoord(v float64) float64 { return math.Round(v*coordScale) / coordScale }

func skipFix(prev, f Fix) bool {
	if prev.MapID != f.MapID {
		return false
	}
	dt := f.T - prev.T
	if dt < 0 {
		dt = -dt
	}
	return dt < MinFixInterval.Milliseconds() && math.Hypot(prev.X-f.X, prev.Y-f.Y) < MinFixDistance
}

// addFix inserts f in time order, applying dedupe and the per-player cap. Caller holds mu.
func (s *Store) addFix(e *Entry, f Fix) {
	tr := e.Track
	i := sort.Search(len(tr), func(i int) bool { return tr[i].T > f.T }) // after equal times
	if i > 0 && skipFix(tr[i-1], f) {
		return
	}
	if i == len(tr) {
		tr = append(tr, f)
	} else {
		tr = append(tr, Fix{})
		copy(tr[i+1:], tr[i:])
		tr[i] = f
	}
	s.total++
	if over := len(tr) - MaxTrackPerPlayer; over > 0 {
		tr = append(tr[:0], tr[over:]...)
		s.total -= over
	}
	e.Track, e.Fixes = tr, len(tr)
	if s.total > maxTotalFixes {
		s.evict(e.GUID)
	}
}

// evict drops whole tracks of the least recently seen players (never keep) until the total
// is under the target. Caller holds mu.
func (s *Store) evict(keep string) {
	var list []*Entry
	for _, e := range s.entries {
		if e.GUID != keep && len(e.Track) > 0 {
			list = append(list, e)
		}
	}
	sort.Slice(list, func(i, j int) bool { return list[i].LastSeen.Before(list[j].LastSeen) })
	target := int(float64(maxTotalFixes) * evictTarget)
	for _, e := range list {
		if s.total <= target {
			return
		}
		s.total -= len(e.Track)
		e.Track, e.Fixes = nil, 0
	}
}

func (s *Store) recordNames(e *Entry, sm Sample) {
	if sm.Name != "" && sm.Name != e.Name {
		if e.Name != "" {
			e.PastNames = addPast(e.PastNames, e.Name, e.Display)
		}
		e.Name = sm.Name
	}
	if sm.Display != "" {
		e.Display = sm.Display
	}
	if sm.Reaction != "" {
		e.Reaction = sm.Reaction
	}
}

func addPast(past []string, names ...string) []string {
outer:
	for _, n := range names {
		if n == "" {
			continue
		}
		for _, p := range past {
			if p == n {
				continue outer
			}
		}
		past = append(past, n)
	}
	if len(past) > MaxPastNames {
		past = past[len(past)-MaxPastNames:]
	}
	return past
}

func (e *Entry) shownName() string {
	if e.Display != "" {
		return e.Display
	}
	return strings.ReplaceAll(e.Name, "-", " ")
}

func (e *Entry) matches(q string) bool {
	if strings.Contains(strings.ToLower(e.shownName()), q) || strings.Contains(strings.ToLower(e.Name), q) {
		return true
	}
	for _, p := range e.PastNames {
		if strings.Contains(strings.ToLower(p), q) {
			return true
		}
	}
	return false
}

// Search returns players whose display name, raw name or a past name contains q
// (case-insensitive), most recently seen first, at most limit (<= 0 means 50). An empty q
// lists the most recent named players. Expired live pips are found too: this reads history.
func (s *Store) Search(q string, limit int) []Match {
	if limit <= 0 {
		limit = 50
	}
	q = strings.ToLower(strings.TrimSpace(q))
	s.mu.Lock()
	var hits []*Entry
	for _, e := range s.entries {
		if e.Name == "" && e.Display == "" {
			continue
		}
		if q == "" || e.matches(q) {
			hits = append(hits, e)
		}
	}
	sort.Slice(hits, func(i, j int) bool {
		if !hits[i].LastSeen.Equal(hits[j].LastSeen) {
			return hits[i].LastSeen.After(hits[j].LastSeen)
		}
		return hits[i].GUID < hits[j].GUID
	})
	if len(hits) > limit {
		hits = hits[:limit]
	}
	out := make([]Match, 0, len(hits))
	for _, e := range hits {
		out = append(out, Match{GUID: e.GUID, Name: e.shownName(), Raw: e.Name, Reaction: e.Reaction,
			Fixes: len(e.Track), FirstSeen: e.FirstSeen, LastSeen: e.LastSeen})
	}
	s.mu.Unlock()
	return out
}

// Track returns a copy of guid's track. mapID 0 returns every map, otherwise only that map
// (Maps and Total always describe the whole track). ok is false for an unknown GUID.
func (s *Store) Track(guid string, mapID int) (TrackResult, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.entries[guid]
	if !ok {
		return TrackResult{}, false
	}
	r := TrackResult{GUID: guid, Name: e.shownName(), Reaction: e.Reaction, Total: len(e.Track),
		Maps: []MapCount{}, Fixes: []Fix{}}
	idx := map[int]int{}
	for _, f := range e.Track {
		i, seen := idx[f.MapID]
		if !seen {
			i = len(r.Maps)
			idx[f.MapID] = i
			r.Maps = append(r.Maps, MapCount{MapID: f.MapID})
		}
		r.Maps[i].Count++
		if f.Zone != "" {
			r.Maps[i].Zone = f.Zone
		}
		if mapID == 0 || f.MapID == mapID {
			r.Fixes = append(r.Fixes, f)
		}
	}
	sort.Slice(r.Maps, func(i, j int) bool { return r.Maps[i].MapID < r.Maps[j].MapID })
	return r, true
}
