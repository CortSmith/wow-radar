// Package bounty tracks hostile players by how many friendly players they killed.
//
// A friendly player's death (UNIT_DIED / PARTY_KILL / SPELL_INSTAKILL) credits the hostile
// player who dealt the killing blow (KillingBlows) and every other hostile player who hit the
// victim shortly before (Assists). The Analyzer detects deaths from parsed log events (live or
// replayed from old log files) and writes them into a Store, which keeps the leaderboard and
// persists it as JSON next to the history file.
//
// Scoring (single place, documented in the README):
//
//	bounty = killingBlows*PointsPerKillingBlow + assists*PointsPerAssist   (assists are worth 0)
//
// Every recorded death has a stable id (see Analyzer); the Store remembers ids so analysing the
// same log again (or live tailing followed by a reverse analysis) never double counts.
package bounty

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// Scoring and sizing constants.
const (
	// PointsPerKillingBlow and PointsPerAssist define the bounty value of a player. The bounty only
	// counts kills of friendly players (killing blows), so assists are worth nothing; they are still
	// recorded and shown, and break ties in the ranking.
	PointsPerKillingBlow = 10
	PointsPerAssist      = 0

	// MaxRecentPerPlayer caps the per-player list of recent death records (oldest dropped).
	MaxRecentPerPlayer = 200
	// MaxPlayers caps the number of hostile players kept; beyond it the lowest scoring
	// (then least recently active) players are dropped down to evictTarget of the cap.
	MaxPlayers = 5000
	// MaxSeenDeaths caps the remembered death ids (idempotency memory); oldest forgotten first.
	MaxSeenDeaths = 100000
	// MaxVictimsPerPlayer caps the per-player distinct victim set used for InvolvedDeaths.
	MaxVictimsPerPlayer = 1000

	evictTarget   = 0.9
	storeVersion  = 1
	maxLeaderRows = 500
)

// Overridable by tests.
var (
	maxPlayers          = MaxPlayers
	maxSeen             = MaxSeenDeaths
	maxVictimsPerPlayer = MaxVictimsPerPlayer
)

// Roles in a death record.
const (
	RoleKiller = "killer"
	RoleAssist = "assist"
)

// Value is the bounty number for a kill/assist tally.
func Value(killingBlows, assists int) int {
	return killingBlows*PointsPerKillingBlow + assists*PointsPerAssist
}

// Participant is a hostile player credited with a death.
type Participant struct {
	GUID    string
	Raw     string // raw log name
	Display string // display name (realm suffix stripped); may be empty
}

// Death is one friendly player death with its credited hostile players.
type Death struct {
	ID         string
	Time       time.Time
	VictimGUID string
	VictimName string
	Killer     *Participant // nil: no hostile player dealt the killing blow (npc, environment, friendly fire)
	Assists    []Participant
	Zone       string
	MapID      int
	HasPos     bool
	X, Y       float64
}

// Sink receives deaths; RecordDeath reports whether the death was new (not a duplicate id).
type Sink interface{ RecordDeath(Death) bool }

// Record is one entry of a player's recent death list.
type Record struct {
	Victim string `json:"victim"`
	T      int64  `json:"t"` // unix ms (log time)
	Role   string `json:"role"`
	Map    int    `json:"map,omitempty"`
	Zone   string `json:"zone,omitempty"`
}

// Player is the persisted tally of one hostile player.
type Player struct {
	GUID           string   `json:"guid,omitempty"`
	Raw            string   `json:"raw,omitempty"`
	Display        string   `json:"name,omitempty"`
	KillingBlows   int      `json:"killingBlows"`
	Assists        int      `json:"assists"`
	InvolvedDeaths int      `json:"involvedDeaths"`
	FirstKill      int64    `json:"firstKill,omitempty"` // unix ms of the earliest credited death
	LastKill       int64    `json:"lastKill,omitempty"`  // unix ms of the latest credited death (kill or assist)
	LastZone       string   `json:"lastZone,omitempty"`
	LastMap        int      `json:"lastMap,omitempty"`
	HasPos         bool     `json:"hasPos,omitempty"`
	LastX          float64  `json:"lastX,omitempty"`
	LastY          float64  `json:"lastY,omitempty"`
	Recent         []Record `json:"recent,omitempty"`  // chronological (oldest first), capped
	Victims        []string `json:"victims,omitempty"` // distinct victim GUIDs, capped
}

func (p *Player) bounty() int { return Value(p.KillingBlows, p.Assists) }

func (p *Player) name() string {
	if p.Display != "" {
		return p.Display
	}
	return p.Raw
}

type fileFormat struct {
	Version   int                `json:"version"`
	Players   map[string]*Player `json:"players"`
	Seen      []string           `json:"seen,omitempty"`
	Deaths    int                `json:"deaths"`
	NPCDeaths int                `json:"npcDeaths"`
}

// Store is the concurrency-safe bounty ledger with optional JSON persistence.
type Store struct {
	path string

	mu        sync.Mutex
	players   map[string]*Player
	seen      map[string]struct{}
	seenOrder []string
	deaths    int
	npcDeaths int
	dirty     bool
}

// New returns an empty store persisting to path ("" = memory only).
func New(path string) *Store {
	return &Store{path: path, players: map[string]*Player{}, seen: map[string]struct{}{}}
}

// PathFor derives the bounty file next to the history file: "<dir>/<name>-bounty<ext>".
func PathFor(historyPath string) string {
	if historyPath == "" {
		return ""
	}
	ext := filepath.Ext(historyPath)
	if ext == "" {
		ext = ".json"
	}
	return strings.TrimSuffix(historyPath, filepath.Ext(historyPath)) + "-bounty" + ext
}

// Open loads path. A missing file gives an empty store; a corrupt file is moved to
// path+".corrupt" and an empty usable store is returned together with the error. A file with
// a newer version still loads the fields this build understands.
func Open(path string) (*Store, error) {
	s := New(path)
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return s, nil
	}
	if err != nil {
		return s, err
	}
	var f fileFormat
	if err := json.Unmarshal(b, &f); err != nil {
		_ = os.Rename(path, path+".corrupt")
		return s, fmt.Errorf("bounty file corrupt (kept as .corrupt): %w", err)
	}
	for g, p := range f.Players {
		if p == nil {
			continue
		}
		p.GUID = g
		s.players[g] = p
	}
	for _, id := range f.Seen {
		s.remember(id)
	}
	s.deaths, s.npcDeaths = f.Deaths, f.NPCDeaths
	return s, nil
}

func (s *Store) remember(id string) {
	if _, ok := s.seen[id]; ok {
		return
	}
	s.seen[id] = struct{}{}
	s.seenOrder = append(s.seenOrder, id)
	for len(s.seenOrder) > maxSeen {
		delete(s.seen, s.seenOrder[0])
		s.seenOrder = s.seenOrder[1:]
	}
	if cap(s.seenOrder) > 2*len(s.seenOrder)+1024 { // release the slack left by the sliding window
		s.seenOrder = append([]string(nil), s.seenOrder...)
	}
}

// RecordDeath credits a death. It returns false (and changes nothing) when the id was already recorded.
func (s *Store) RecordDeath(d Death) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, dup := s.seen[d.ID]; dup {
		return false
	}
	s.remember(d.ID)
	s.dirty = true
	s.deaths++
	if d.Killer == nil && len(d.Assists) == 0 {
		s.npcDeaths++
		return true
	}
	if d.Killer != nil {
		s.credit(*d.Killer, RoleKiller, d)
	}
	for _, a := range d.Assists {
		s.credit(a, RoleAssist, d)
	}
	if len(s.players) > maxPlayers {
		s.evict()
	}
	return true
}

func (s *Store) credit(pt Participant, role string, d Death) {
	if pt.GUID == "" {
		return
	}
	p := s.players[pt.GUID]
	if p == nil {
		p = &Player{GUID: pt.GUID}
		s.players[pt.GUID] = p
	}
	if pt.Raw != "" {
		p.Raw = pt.Raw
	}
	if pt.Display != "" || pt.Raw != "" {
		p.Display = pt.Display
	}
	ms := d.Time.UnixMilli()
	if role == RoleKiller {
		p.KillingBlows++
	} else {
		p.Assists++
	}
	if p.FirstKill == 0 || ms < p.FirstKill {
		p.FirstKill = ms
	}
	if ms >= p.LastKill {
		p.LastKill = ms
		if d.Zone != "" {
			p.LastZone = d.Zone
		}
		if d.MapID != 0 {
			p.LastMap = d.MapID
		}
		if d.HasPos {
			p.HasPos, p.LastX, p.LastY = true, d.X, d.Y
		}
	}
	if !contains(p.Victims, d.VictimGUID) {
		p.InvolvedDeaths++
		if len(p.Victims) < maxVictimsPerPlayer {
			p.Victims = append(p.Victims, d.VictimGUID)
		}
	}
	p.Recent = append(p.Recent, Record{Victim: d.VictimName, T: ms, Role: role, Map: d.MapID, Zone: d.Zone})
	if len(p.Recent) > MaxRecentPerPlayer {
		p.Recent = append([]Record(nil), p.Recent[len(p.Recent)-MaxRecentPerPlayer:]...)
	}
}

func contains(l []string, v string) bool {
	for _, x := range l {
		if x == v {
			return true
		}
	}
	return false
}

// evict drops the weakest players until len <= evictTarget*maxPlayers. Caller holds s.mu.
func (s *Store) evict() {
	all := s.sorted()
	keep := int(float64(maxPlayers) * evictTarget)
	if keep < 1 {
		keep = 1
	}
	for _, p := range all[min(keep, len(all)):] {
		delete(s.players, p.GUID)
	}
}

// sorted returns the players best first: killing blows, assists, most recent kill, guid.
func (s *Store) sorted() []*Player {
	out := make([]*Player, 0, len(s.players))
	for _, p := range s.players {
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		switch {
		case a.KillingBlows != b.KillingBlows:
			return a.KillingBlows > b.KillingBlows
		case a.Assists != b.Assists:
			return a.Assists > b.Assists
		case a.LastKill != b.LastKill:
			return a.LastKill > b.LastKill
		}
		return a.GUID < b.GUID
	})
	return out
}

// Row is one leaderboard line.
type Row struct {
	Rank           int     `json:"rank"`
	GUID           string  `json:"guid"`
	Name           string  `json:"name"`
	Raw            string  `json:"raw"`
	Bounty         int     `json:"bounty"`
	KillingBlows   int     `json:"killingBlows"`
	Assists        int     `json:"assists"`
	InvolvedDeaths int     `json:"involvedDeaths"`
	FirstKill      int64   `json:"firstKill"`
	LastKill       int64   `json:"lastKill"`
	LastZone       string  `json:"lastZone"`
	LastMap        int     `json:"lastMap"`
	HasPos         bool    `json:"hasPos"`
	LastX          float64 `json:"lastX"`
	LastY          float64 `json:"lastY"`
}

// Detail is a Row plus the recent death records (newest first).
type Detail struct {
	Row
	Recent []Record `json:"recent"`
}

func row(rank int, p *Player) Row {
	return Row{Rank: rank, GUID: p.GUID, Name: p.name(), Raw: p.Raw, Bounty: p.bounty(),
		KillingBlows: p.KillingBlows, Assists: p.Assists, InvolvedDeaths: p.InvolvedDeaths,
		FirstKill: p.FirstKill, LastKill: p.LastKill, LastZone: p.LastZone, LastMap: p.LastMap,
		HasPos: p.HasPos, LastX: p.LastX, LastY: p.LastY}
}

// Leaderboard returns the top limit players (limit <= 0 means all, capped at 500 rows).
func (s *Store) Leaderboard(limit int) []Row {
	s.mu.Lock()
	defer s.mu.Unlock()
	all := s.sorted()
	if limit <= 0 || limit > maxLeaderRows {
		limit = maxLeaderRows
	}
	if limit > len(all) {
		limit = len(all)
	}
	out := make([]Row, 0, limit)
	for i := 0; i < limit; i++ {
		out = append(out, row(i+1, all[i]))
	}
	return out
}

// Player returns the detail of one hostile player; Rank is the current leaderboard position.
func (s *Store) Player(guid string) (Detail, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p := s.players[guid]
	if p == nil {
		return Detail{}, false
	}
	rank := 0
	for i, q := range s.sorted() {
		if q == p {
			rank = i + 1
			break
		}
	}
	d := Detail{Row: row(rank, p), Recent: make([]Record, 0, len(p.Recent))}
	for i := len(p.Recent) - 1; i >= 0; i-- {
		d.Recent = append(d.Recent, p.Recent[i])
	}
	return d, true
}

// Stats are cheap counters.
type Stats struct {
	Players   int `json:"players"`
	Deaths    int `json:"deaths"`    // distinct friendly deaths recorded
	NPCDeaths int `json:"npcDeaths"` // of those, deaths without any hostile player credit
	SeenIDs   int `json:"seenIds"`
}

// Stats returns counters.
func (s *Store) Stats() Stats {
	s.mu.Lock()
	defer s.mu.Unlock()
	return Stats{Players: len(s.players), Deaths: s.deaths, NPCDeaths: s.npcDeaths, SeenIDs: len(s.seen)}
}

// Save writes the store atomically (temp file + rename). No-op without a path.
func (s *Store) Save() error {
	if s.path == "" {
		return nil
	}
	s.mu.Lock()
	b, err := json.Marshal(fileFormat{Version: storeVersion, Players: s.players, Seen: s.seenOrder,
		Deaths: s.deaths, NPCDeaths: s.npcDeaths})
	s.dirty = false
	s.mu.Unlock()
	if err != nil {
		s.markDirty()
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
