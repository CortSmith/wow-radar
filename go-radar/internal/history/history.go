// Package history keeps long-lived per-GUID statistics (damage, health, last position)
// and persists them as JSON. It is intentionally independent of the live tracker: entries
// are never dropped when a pip expires, and a returning GUID merges into its old entry.
package history

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

// Sample is one observation of a unit, produced for every log event that names it.
type Sample struct {
	GUID     string
	Name     string // raw log name, may be empty
	Display  string // display name (realm suffix stripped), may be empty
	Reaction string // friendly|hostile|neutral|unknown, may be empty
	Time     time.Time

	AsSource bool // the unit was the source of the event

	HasHP     bool
	HP, MaxHP float64

	HasDamage bool // the unit was the source of a damage event
	Damage    float64

	HasPos bool
	X, Y   float64
	MapID  int
	Zone   string
	Approx bool // position is an approximate sighting: recorded in the track only
}

// Entry is the persisted history of one GUID.
type Entry struct {
	GUID      string    `json:"guid"`
	Name      string    `json:"name"`
	Display   string    `json:"display,omitempty"`
	PastNames []string  `json:"pastNames,omitempty"`
	Reaction  string    `json:"reaction,omitempty"`
	FirstSeen time.Time `json:"firstSeen"`
	LastSeen  time.Time `json:"lastSeen"`

	Events    int `json:"events"`    // all events naming the unit
	SrcEvents int `json:"srcEvents"` // events where it was the source

	DamageHits        int     `json:"damageHits"`
	DamageTotal       float64 `json:"damageTotal"`
	AvgDamagePerHit   float64 `json:"avgDamagePerHit"`
	AvgDamagePerEvent float64 `json:"avgDamagePerEvent"` // total damage / source events

	HPSamples     int     `json:"hpSamples"`
	HPSum         float64 `json:"hpSum"`
	MaxHPSum      float64 `json:"maxHpSum"`
	AvgHP         float64 `json:"avgHp"`
	AvgMaxHP      float64 `json:"avgMaxHp"`
	MaxHP         float64 `json:"maxHp"`         // largest max-health seen
	MaxObservedHP float64 `json:"maxObservedHp"` // largest current health seen

	HasPos   bool      `json:"hasPos"`
	LastX    float64   `json:"lastX"`
	LastY    float64   `json:"lastY"`
	LastMap  int       `json:"lastMap"`
	LastZone string    `json:"lastZone"`
	PosTime  time.Time `json:"posTime"`

	Fixes int   `json:"fixes"`           // length of the track (always set)
	Track []Fix `json:"track,omitempty"` // chronological; stripped from Get/Snapshot
}

type file struct {
	Version int               `json:"version"`
	Entries map[string]*Entry `json:"entries"`
}

// Store is a concurrency-safe in-memory history with optional JSON persistence.
type Store struct {
	path string

	mu      sync.Mutex
	entries map[string]*Entry
	dirty   bool
	total   int // fixes over all tracks
}

// New returns an empty store persisting to path ("" = memory only).
func New(path string) *Store {
	return &Store{path: path, entries: map[string]*Entry{}}
}

// Open loads path. A missing file yields an empty store. A corrupt file is moved to
// path+".corrupt" and an empty, usable store is returned together with the error.
func Open(path string) (*Store, error) {
	s := New(path)
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return s, nil
	}
	if err != nil {
		return s, err
	}
	var f file
	if err := json.Unmarshal(b, &f); err != nil {
		_ = os.Rename(path, path+".corrupt")
		return s, fmt.Errorf("history file corrupt (kept as .corrupt): %w", err)
	}
	for g, e := range f.Entries {
		if e != nil {
			e.GUID = g
			s.entries[g] = e
		}
	}
	return s, nil
}

// Record merges one sample into the unit's entry.
func (s *Store) Record(sm Sample) {
	if sm.GUID == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	e := s.entries[sm.GUID]
	if e == nil {
		e = &Entry{GUID: sm.GUID, FirstSeen: sm.Time, LastSeen: sm.Time}
		s.entries[sm.GUID] = e
	}
	s.dirty = true
	s.recordNames(e, sm)
	if sm.Time.Before(e.FirstSeen) {
		e.FirstSeen = sm.Time
	}
	if sm.Time.After(e.LastSeen) {
		e.LastSeen = sm.Time
	}
	e.Events++
	if sm.AsSource {
		e.SrcEvents++
	}
	if sm.HasDamage {
		e.DamageHits++
		e.DamageTotal += sm.Damage
	}
	if e.DamageHits > 0 {
		e.AvgDamagePerHit = e.DamageTotal / float64(e.DamageHits)
	}
	if e.SrcEvents > 0 {
		e.AvgDamagePerEvent = e.DamageTotal / float64(e.SrcEvents)
	}
	if sm.HasHP {
		e.HPSamples++
		e.HPSum += sm.HP
		e.MaxHPSum += sm.MaxHP
		e.AvgHP = e.HPSum / float64(e.HPSamples)
		e.AvgMaxHP = e.MaxHPSum / float64(e.HPSamples)
		if sm.MaxHP > e.MaxHP {
			e.MaxHP = sm.MaxHP
		}
		if sm.HP > e.MaxObservedHP {
			e.MaxObservedHP = sm.HP
		}
	}
	if sm.HasPos && !sm.Approx && !sm.Time.Before(e.PosTime) {
		e.HasPos, e.LastX, e.LastY, e.LastMap, e.PosTime = true, sm.X, sm.Y, sm.MapID, sm.Time
		if sm.Zone != "" {
			e.LastZone = sm.Zone
		}
	}
	if sm.HasPos {
		kind := KindExact
		if sm.Approx {
			kind = KindApprox
		}
		s.addFix(e, Fix{T: sm.Time.UnixMilli(), MapID: sm.MapID, Zone: sm.Zone, X: roundCoord(sm.X), Y: roundCoord(sm.Y), Kind: kind})
	}
}

// Get returns a copy of the entry for guid.
func (s *Store) Get(guid string) (Entry, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.entries[guid]
	if !ok {
		return Entry{}, false
	}
	c := *e
	c.Track = nil
	return c, true
}

// Snapshot returns copies of all entries, most recently seen first.
func (s *Store) Snapshot() []Entry {
	s.mu.Lock()
	out := make([]Entry, 0, len(s.entries))
	for _, e := range s.entries {
		c := *e
		c.Track = nil
		out = append(out, c)
	}
	s.mu.Unlock()
	sort.Slice(out, func(i, j int) bool {
		if !out[i].LastSeen.Equal(out[j].LastSeen) {
			return out[i].LastSeen.After(out[j].LastSeen)
		}
		return out[i].GUID < out[j].GUID
	})
	return out
}

// Save writes the store atomically (temp file + rename). No-op without a path.
func (s *Store) Save() error {
	if s.path == "" {
		return nil
	}
	s.mu.Lock()
	b, err := json.Marshal(file{Version: 2, Entries: s.entries})
	s.dirty = false
	s.mu.Unlock()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		s.markDirty()
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		s.markDirty()
		return err
	}
	if err := os.Rename(tmp, s.path); err != nil {
		s.markDirty()
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

func (s *Store) markDirty() {
	s.mu.Lock()
	s.dirty = true
	s.mu.Unlock()
}

// Flush saves only if something changed since the last save.
func (s *Store) Flush() error {
	s.mu.Lock()
	d := s.dirty
	s.mu.Unlock()
	if !d {
		return nil
	}
	return s.Save()
}

// Run flushes every interval until ctx is cancelled, then flushes once more.
func (s *Store) Run(ctx context.Context, interval time.Duration) {
	tk := time.NewTicker(interval)
	defer tk.Stop()
	for {
		select {
		case <-ctx.Done():
			_ = s.Flush()
			return
		case <-tk.C:
			_ = s.Flush()
		}
	}
}

// Len is the number of units (GUIDs) in the store; it is cheap (no copying).
func (s *Store) Len() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.entries)
}
