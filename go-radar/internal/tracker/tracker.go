// Package tracker maintains the live set of tracked units ("pips") from parsed combat log
// events, applies the stale/expiry rules, and feeds the long-term history.
//
// Time model: every unit carries the LOG timestamp of its last event (LastEvent, UTC).
// Expiry compares that against the injectable wall clock: older than StaleAfter (5 min)
// the pip is "stale", older than ExpireAfter (15 min) it leaves the live view (its
// history is kept and merges when the GUID is seen again).
//
// MULTI-LOG: Apply takes a SourceID so several tailers can feed one Tracker. Per-source
// state (who "me" is, current zone/map, my last fix) lives in `source`; units are keyed
// by GUID globally so the same player seen by two logs is one pip.
package tracker

import (
	"math"
	"sort"
	"sync"
	"time"

	"nearbyradar/internal/clock"
	"nearbyradar/internal/history"
	"nearbyradar/internal/parser"
	"nearbyradar/internal/screenshot"
)

// SourceID identifies one log source (one combat log file / one WoW client).
type SourceID string

// Pip states and position kinds.
const (
	StateActive = "active"
	StateStale  = "stale"

	PosNone   = "none"
	PosExact  = "exact"
	PosApprox = "approx"
)

const (
	approxWeight = 0.3
	approxWindow = 30 * time.Second // exact fix preferred unless an approx one is newer by more than this
	fixRing      = 60
	estimateN    = 15
	estimateGap  = 30 * time.Second
)

// Recorder receives per-event samples for the long-term history.
type Recorder interface{ Record(history.Sample) }

// Config tunes the tracker. The zero value is valid (players only, 5/15 minute expiry).
type Config struct {
	TrackNPCs     bool          // also track Creature/Vehicle/Pet GUIDs (default: players only)
	DisableApprox bool          // turn off approximate sightings near "me"
	RealmSuffix   string        // e.g. "-PvP-US"; empty = auto-detect
	StaleAfter    time.Duration // default 5m
	ExpireAfter   time.Duration // default 15m
	ApproxMaxAge  time.Duration // max age of my fix for approximate sightings, default 120s
	History       Recorder      // optional
}

func (c Config) withDefaults() Config {
	if c.StaleAfter <= 0 {
		c.StaleAfter = 5 * time.Minute
	}
	if c.ExpireAfter <= 0 {
		c.ExpireAfter = 15 * time.Minute
	}
	if c.ApproxMaxAge <= 0 {
		c.ApproxMaxAge = 120 * time.Second
	}
	return c
}

type fix struct {
	t     time.Time
	x, y  float64
	kind  string
	mapID int
}

type unit struct {
	guid, raw string
	flags     uint32
	hasFlags  bool
	kind      parser.GUIDKind
	first     time.Time
	last      time.Time
	events    int
	hp, maxHP float64
	hasHP     bool
	level     int
	exact     fix
	hasExact  bool
	approx    fix
	hasApprox bool
	fixes     []fix
	dmgTotal  float64
	dmgHits   int
	source    SourceID
}

type source struct {
	id       SourceID
	meGUID   string
	meFix    fix
	hasMeFix bool
	zone     string
	mapID    int
	maps     map[int]parser.MapBounds
	advanced *bool
	events   int
}

// Tracker is safe for concurrent use.
type Tracker struct {
	cfg   Config
	clk   clock.Clock
	mu    sync.Mutex
	units map[string]*unit
	srcs  map[SourceID]*source
	// MULTI-LOG: primary is the source that most recently delivered an event. It decides which
	// "me" the radar centres on and which zone/map the UI shows. A multi-client setup could
	// instead let the user pick the primary source explicitly.
	primary SourceID
	namer   *parser.Namer
	version uint64
	events  int
	obsZone string
	obsMap  int
	pending *screenshot.Observation
}

// New creates a Tracker. clk may be nil (real clock).
func New(cfg Config, clk clock.Clock) *Tracker {
	if clk == nil {
		clk = clock.Real{}
	}
	cfg = cfg.withDefaults()
	return &Tracker{cfg: cfg, clk: clk, units: map[string]*unit{}, srcs: map[SourceID]*source{},
		namer: parser.NewNamer(cfg.RealmSuffix)}
}

func (t *Tracker) source(id SourceID) *source {
	s := t.srcs[id]
	if s == nil {
		s = &source{id: id, maps: map[int]parser.MapBounds{}}
		t.srcs[id] = s
	}
	return s
}

func (t *Tracker) tracked(guid string) bool {
	switch parser.KindOf(guid) {
	case parser.KindPlayer:
		return true
	case parser.KindNPC, parser.KindPet:
		return t.cfg.TrackNPCs
	}
	return false
}

type part struct {
	guid     string
	name     string
	flags    uint32
	hasFlags bool
}

// Apply feeds one parsed event from the given source.
//
// MULTI-LOG: every tailer calls Apply with its own SourceID; Apply is serialised by a mutex,
// so tailers need no coordination. Events from different logs may interleave arbitrarily.
func (t *Tracker) Apply(id SourceID, ev parser.Event) {
	t.mu.Lock()
	defer t.mu.Unlock()
	s := t.source(id)
	t.primary = id
	t.version++
	t.events++
	s.events++

	switch ev.Type {
	case "COMBAT_LOG_VERSION":
		s.advanced = ev.AdvancedEnabled
		return
	case "ZONE_CHANGE":
		if ev.Zone != "" {
			s.zone = ev.Zone
		}
		return
	case "MAP_CHANGE":
		if ev.Map != nil {
			s.maps[ev.Map.ID] = *ev.Map
			s.mapID = ev.Map.ID
			if s.zone == "" {
				s.zone = ev.Map.Name
			}
		}
		return
	}

	var parts []part
	add := func(p part) {
		if p.guid == "" || !t.tracked(p.guid) {
			return
		}
		for _, q := range parts {
			if q.guid == p.guid {
				return
			}
		}
		parts = append(parts, p)
	}
	add(part{ev.Src.GUID, ev.Src.Name, ev.Src.Flags, true})
	add(part{ev.Dst.GUID, ev.Dst.Name, ev.Dst.Flags, true})
	if ev.Info != nil {
		add(part{guid: ev.Info.GUID})
	}

	for _, p := range parts {
		u := t.touch(s, p, ev.Time)
		_ = u
	}

	infoPos := ""
	if ev.Info != nil && t.tracked(ev.Info.GUID) {
		u := t.units[ev.Info.GUID]
		if ev.Info.HasHP {
			u.hp, u.maxHP, u.hasHP = ev.Info.HP, ev.Info.MaxHP, true
		}
		if ev.Info.Level > 0 {
			u.level = ev.Info.Level
		}
		if ev.Info.HasPos {
			f := fix{t: ev.Time, x: ev.Info.X, y: ev.Info.Y, kind: PosExact, mapID: ev.Info.MapID}
			setExact(u, f)
			infoPos = u.guid
			if u.guid == s.meGUID {
				s.meFix, s.hasMeFix, s.mapID = f, true, f.mapID
			}
		}
	}

	approxFix := map[string]fix{}
	if !t.cfg.DisableApprox && s.hasMeFix && absDur(ev.Time.Sub(s.meFix.t)) < t.cfg.ApproxMaxAge {
		for _, p := range parts {
			u := t.units[p.guid]
			if u.kind != parser.KindPlayer || p.guid == s.meGUID || p.guid == infoPos {
				continue
			}
			f := fix{t: ev.Time, x: s.meFix.x, y: s.meFix.y, kind: PosApprox, mapID: s.meFix.mapID}
			u.approx, u.hasApprox = f, true
			pushFix(u, f)
			approxFix[p.guid] = f
		}
	}

	if ev.IsDamage && ev.HasAmount && t.tracked(ev.Src.GUID) {
		u := t.units[ev.Src.GUID]
		u.dmgTotal += ev.Amount
		u.dmgHits++
	}

	if t.pending != nil && id == t.primary && s.meGUID != "" {
		obs := *t.pending
		t.pending = nil
		t.applyObsToMe(s, obs)
	}

	if t.cfg.History != nil {
		for _, p := range parts {
			sm := history.Sample{GUID: p.guid, Name: p.name, Time: ev.Time, AsSource: p.guid == ev.Src.GUID, Zone: s.zone}
			t.describe(&sm, p.guid, p.name)
			if ev.Info != nil && ev.Info.GUID == p.guid {
				sm.HasHP, sm.HP, sm.MaxHP = ev.Info.HasHP, ev.Info.HP, ev.Info.MaxHP
				if ev.Info.HasPos {
					sm.HasPos, sm.X, sm.Y, sm.MapID = true, ev.Info.X, ev.Info.Y, ev.Info.MapID
				}
			}
			if af, ok := approxFix[p.guid]; ok && !sm.HasPos {
				sm.HasPos, sm.Approx, sm.X, sm.Y, sm.MapID = true, true, af.x, af.y, af.mapID
			}
			if ev.IsDamage && ev.HasAmount && p.guid == ev.Src.GUID {
				sm.HasDamage, sm.Damage = true, ev.Amount
			}
			t.cfg.History.Record(sm)
		}
	}
}

func (t *Tracker) touch(s *source, p part, ts time.Time) *unit {
	u := t.units[p.guid]
	if u == nil {
		u = &unit{guid: p.guid, kind: parser.KindOf(p.guid), first: ts, last: ts, source: s.id}
		t.units[p.guid] = u
	}
	if p.name != "" {
		u.raw = p.name
		if u.kind == parser.KindPlayer {
			t.namer.Observe(p.name)
		}
	}
	if p.hasFlags {
		u.flags, u.hasFlags = p.flags, true
		// MULTI-LOG: "me" is decided per source from the "affiliation mine" flag.
		if u.kind == parser.KindPlayer && parser.IsMine(p.flags) {
			s.meGUID = p.guid
		}
	}
	if ts.After(u.last) {
		u.last = ts
	}
	u.source = s.id
	u.events++
	return u
}

func setExact(u *unit, f fix) {
	u.exact, u.hasExact = f, true
	pushFix(u, f)
}

func pushFix(u *unit, f fix) {
	if n := len(u.fixes); n > 0 {
		l := u.fixes[n-1]
		if l.kind == f.kind && l.x == f.x && l.y == f.y && f.t.Sub(l.t) < 500*time.Millisecond {
			return
		}
	}
	u.fixes = append(u.fixes, f)
	if len(u.fixes) > fixRing {
		u.fixes = append(u.fixes[:0], u.fixes[len(u.fixes)-fixRing:]...)
	}
}

func absDur(d time.Duration) time.Duration {
	if d < 0 {
		return -d
	}
	return d
}

// ApplyObservation feeds a screenshot-reader observation. It updates the zone/map and, when
// the observation has coordinates, the position of "me" of the primary source, and counts
// as an event for "me" (refreshing its expiry timer and history).
//
// MULTI-LOG: observations are applied to the primary source's "me" (the log most recently
// active); a screenshot belongs to the focused game window, which a future multi-client
// reader could map to a specific SourceID.
func (t *Tracker) ApplyObservation(obs screenshot.Observation) {
	if !obs.Valid() {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if obs.Time.IsZero() {
		obs.Time = t.clk.Now().UTC()
	}
	t.version++
	if obs.HasZone {
		t.obsZone = obs.Zone
	}
	if obs.HasMap {
		t.obsMap = obs.MapID
	}
	s := t.srcs[t.primary]
	if s == nil {
		if obs.HasPos {
			o := obs
			t.pending = &o
		}
		return
	}
	if obs.HasZone {
		s.zone = obs.Zone
	}
	if obs.HasMap {
		s.mapID = obs.MapID
	}
	if s.meGUID == "" || t.units[s.meGUID] == nil {
		if obs.HasPos {
			o := obs
			t.pending = &o
		}
		return
	}
	t.applyObsToMe(s, obs)
}

func (t *Tracker) applyObsToMe(s *source, obs screenshot.Observation) {
	u := t.units[s.meGUID]
	if u == nil {
		return
	}
	if obs.Time.After(u.last) {
		u.last = obs.Time
	}
	u.events++
	sm := history.Sample{GUID: u.guid, Time: obs.Time, Zone: s.zone}
	t.describe(&sm, u.guid, "")
	if obs.HasPos {
		mp := s.mapID
		if obs.HasMap {
			mp = obs.MapID
		} else if s.hasMeFix {
			mp = s.meFix.mapID
		}
		f := fix{t: obs.Time, x: obs.X, y: obs.Y, kind: PosExact, mapID: mp}
		setExact(u, f)
		s.meFix, s.hasMeFix = f, true
		s.mapID = mp
		sm.HasPos, sm.X, sm.Y, sm.MapID = true, obs.X, obs.Y, mp
	}
	if t.cfg.History != nil {
		t.cfg.History.Record(sm)
	}
}

// Version increases whenever the tracker's content may have changed.
func (t *Tracker) Version() uint64 {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.version
}

func (t *Tracker) meSet() map[string]bool {
	m := map[string]bool{}
	for _, s := range t.srcs {
		if s.meGUID != "" {
			m[s.meGUID] = true
		}
	}
	return m
}

func (t *Tracker) prune(now time.Time) {
	me := t.meSet()
	for g, u := range t.units {
		if !me[g] && now.Sub(u.last) > t.cfg.ExpireAfter {
			delete(t.units, g)
		}
	}
}

// Prune drops units whose last event is older than ExpireAfter (history is unaffected).
func (t *Tracker) Prune() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.prune(t.clk.Now())
}

// Stats are cheap counters.
type Stats struct {
	Events  int `json:"events"`
	Units   int `json:"units"`
	Sources int `json:"sources"`
}

// Stats returns counters.
func (t *Tracker) Stats() Stats {
	t.mu.Lock()
	defer t.mu.Unlock()
	return Stats{Events: t.events, Units: len(t.units), Sources: len(t.srcs)}
}

// Estimate is a smoothed position guess from recent located events.
type Estimate struct {
	X      float64 `json:"x"`
	Y      float64 `json:"y"`
	Spread float64 `json:"spread"`
	MapID  int     `json:"mapId"`
	N      int     `json:"n"`
	Exact  int     `json:"exact"`
}

// Pip is the JSON view of one tracked unit.
type Pip struct {
	GUID        string    `json:"guid"`
	Name        string    `json:"name"`
	Raw         string    `json:"raw"`
	Kind        string    `json:"kind"`
	Reaction    string    `json:"reaction"`
	State       string    `json:"state"`
	Me          bool      `json:"me"`
	HP          float64   `json:"hp"`
	MaxHP       float64   `json:"maxHp"`
	HasHP       bool      `json:"hasHp"`
	Level       int       `json:"level"`
	X           float64   `json:"x"`
	Y           float64   `json:"y"`
	MapID       int       `json:"mapId"`
	PosKind     string    `json:"posKind"`
	Weight      float64   `json:"weight"`
	LastEvent   int64     `json:"lastEvent"` // unix ms, log time
	AgeSec      float64   `json:"ageSec"`
	Events      int       `json:"events"`
	DamageTotal float64   `json:"damageTotal"`
	DamageHits  int       `json:"damageHits"`
	Source      string    `json:"source"`
	Est         *Estimate `json:"est,omitempty"`
}

// MapInfo is a map's world rectangle (from MAP_CHANGE).
type MapInfo struct {
	Name string  `json:"name"`
	X0   float64 `json:"x0"`
	X1   float64 `json:"x1"`
	Y0   float64 `json:"y0"`
	Y1   float64 `json:"y1"`
}

// Snapshot is the full live view.
type Snapshot struct {
	Now         int64              `json:"now"` // unix ms wall clock
	Zone        string             `json:"zone"`
	MapID       int                `json:"mapId"`
	Me          *Pip               `json:"me"`
	Pips        []Pip              `json:"pips"`
	Maps        map[string]MapInfo `json:"maps"`
	Advanced    *bool              `json:"advanced"`
	RealmSuffix string             `json:"realmSuffix"`
	Sources     []string           `json:"sources"`
	Stats       Stats              `json:"stats"`
	StaleSec    float64            `json:"staleSec"`
	ExpireSec   float64            `json:"expireSec"`
}

// Snapshot returns the current live view, pruning expired units first.
func (t *Tracker) Snapshot() Snapshot {
	t.mu.Lock()
	defer t.mu.Unlock()
	now := t.clk.Now()
	t.prune(now)
	me := t.meSet()

	snap := Snapshot{
		Now: now.UnixMilli(), Pips: make([]Pip, 0, len(t.units)), Maps: map[string]MapInfo{},
		RealmSuffix: t.namer.Suffix(), Sources: []string{},
		Stats:    Stats{Events: t.events, Units: len(t.units), Sources: len(t.srcs)},
		StaleSec: t.cfg.StaleAfter.Seconds(), ExpireSec: t.cfg.ExpireAfter.Seconds(),
		Zone: t.obsZone, MapID: t.obsMap,
	}
	for id, s := range t.srcs {
		snap.Sources = append(snap.Sources, string(id))
		for mid, m := range s.maps {
			snap.Maps[itoa(mid)] = MapInfo{Name: m.Name, X0: m.X0, X1: m.X1, Y0: m.Y0, Y1: m.Y1}
		}
	}
	sort.Strings(snap.Sources)

	units := make([]*unit, 0, len(t.units))
	for _, u := range t.units {
		units = append(units, u)
	}
	sort.Slice(units, func(i, j int) bool {
		if !units[i].last.Equal(units[j].last) {
			return units[i].last.After(units[j].last)
		}
		return units[i].guid < units[j].guid
	})
	for _, u := range units {
		snap.Pips = append(snap.Pips, t.pip(u, now, me[u.guid]))
	}
	if ps := t.srcs[t.primary]; ps != nil {
		if ps.zone != "" {
			snap.Zone = ps.zone
		}
		if ps.mapID != 0 {
			snap.MapID = ps.mapID
		}
		snap.Advanced = ps.advanced
		if u := t.units[ps.meGUID]; u != nil {
			p := t.pip(u, now, true)
			snap.Me = &p
		}
	}
	return snap
}

func itoa(i int) string {
	b, neg := [20]byte{}, i < 0
	if neg {
		i = -i
	}
	p := len(b)
	for {
		p--
		b[p] = byte('0' + i%10)
		i /= 10
		if i == 0 {
			break
		}
	}
	if neg {
		p--
		b[p] = '-'
	}
	return string(b[p:])
}

func (t *Tracker) pip(u *unit, now time.Time, isMe bool) Pip {
	age := now.Sub(u.last)
	p := Pip{
		GUID: u.guid, Raw: u.raw, Name: t.namer.Display(u.raw), State: StateActive, Me: isMe,
		HP: u.hp, MaxHP: u.maxHP, HasHP: u.hasHP, Level: u.level,
		PosKind: PosNone, LastEvent: u.last.UnixMilli(), AgeSec: age.Seconds(),
		Events: u.events, DamageTotal: u.dmgTotal, DamageHits: u.dmgHits, Source: string(u.source),
		Reaction: parser.ReactionUnknown.String(),
	}
	if p.Name == "" {
		p.Name = u.guid
	}
	if age > t.cfg.StaleAfter {
		p.State = StateStale
	}
	switch u.kind {
	case parser.KindPlayer:
		p.Kind = "player"
	case parser.KindPet:
		p.Kind = "pet"
	default:
		p.Kind = "npc"
	}
	if u.hasFlags {
		p.Reaction = parser.ReactionOf(u.flags).String()
	}
	switch {
	case u.hasExact && (!u.hasApprox || u.approx.t.Sub(u.exact.t) <= approxWindow):
		p.X, p.Y, p.MapID, p.PosKind, p.Weight = u.exact.x, u.exact.y, u.exact.mapID, PosExact, 1
	case u.hasApprox:
		p.X, p.Y, p.MapID, p.PosKind, p.Weight = u.approx.x, u.approx.y, u.approx.mapID, PosApprox, approxWeight
	}
	p.Est = estimate(u.fixes, now, estimateN, estimateGap)
	return p
}

// estimate guesses where a unit is from its recent located events (chronological fixes).
// Starting from the newest fix it walks back while the gap between consecutive fixes stays
// <= gap (one "encounter") on the same map, keeping at most n fixes. The result is the
// weighted mean (exact fixes count more than approximate ones, newer more than older); if
// the exact fixes show movement, one extrapolated point is added.
func estimate(fixes []fix, now time.Time, n int, gap time.Duration) *Estimate {
	if len(fixes) == 0 {
		return nil
	}
	mp := fixes[len(fixes)-1].mapID
	var win []fix
	for i := len(fixes) - 1; i >= 0; i-- {
		f := fixes[i]
		if f.mapID != mp || (len(win) > 0 && win[len(win)-1].t.Sub(f.t) > gap) {
			break
		}
		win = append(win, f)
		if len(win) >= n {
			break
		}
	}
	type wp struct{ x, y, w float64 }
	var pts []wp
	decay := 1.0
	exact := 0
	for _, f := range win {
		w := approxWeight
		if f.kind == PosExact {
			w = 1
			exact++
		}
		pts = append(pts, wp{f.x, f.y, w * decay})
		decay *= 0.9
	}
	if exact >= 2 {
		var ex []fix
		for _, f := range win {
			if f.kind == PosExact {
				ex = append(ex, f)
			}
		}
		a, b := ex[0], ex[min(4, len(ex)-1)]
		if dt := a.t.Sub(b.t).Seconds(); dt >= 1.5 {
			vx, vy := (a.x-b.x)/dt, (a.y-b.y)/dt
			sp := math.Hypot(vx, vy)
			const maxSpeed = 30.0
			if sp > maxSpeed {
				vx, vy, sp = vx*maxSpeed/sp, vy*maxSpeed/sp, maxSpeed
			}
			if sp >= 0.5 {
				tp := now.Sub(a.t).Seconds()
				if tp < 0 {
					tp = 0
				}
				if tp > 10 {
					tp = 10
				}
				pts = append(pts, wp{a.x + vx*tp, a.y + vy*tp, 1.5})
			}
		}
	}
	var ws, mx, my float64
	for _, p := range pts {
		ws += p.w
		mx += p.x * p.w
		my += p.y * p.w
	}
	mx /= ws
	my /= ws
	var v float64
	for _, p := range pts {
		v += p.w * ((p.x-mx)*(p.x-mx) + (p.y-my)*(p.y-my))
	}
	spread := math.Sqrt(v / ws / 2)
	if spread < 8 {
		spread = 8
	}
	return &Estimate{X: round1(mx), Y: round1(my), Spread: round1(spread), MapID: mp, N: len(win), Exact: exact}
}

func round1(v float64) float64 { return math.Round(v*10) / 10 }

// describe fills the naming/reaction part of a history sample from the live unit. Caller
// holds t.mu.
func (t *Tracker) describe(sm *history.Sample, guid, raw string) {
	u := t.units[guid]
	if u == nil {
		return
	}
	if raw == "" {
		raw = u.raw
	}
	sm.Name = raw
	if raw != "" {
		sm.Display = t.namer.Display(raw)
	}
	if u.hasFlags {
		sm.Reaction = parser.ReactionOf(u.flags).String()
	}
}

// RealmSuffix is the realm suffix in use for display names (configured or auto-detected; "" if none yet).
func (t *Tracker) RealmSuffix() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.namer.Suffix()
}
