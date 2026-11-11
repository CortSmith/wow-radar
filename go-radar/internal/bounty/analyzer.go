package bounty

import (
	"crypto/sha1"
	"encoding/hex"
	"strconv"
	"sync"
	"time"

	"nearbyradar/internal/parser"
)

// Attribution windows and memory bounds.
const (
	// AssistWindow: a hostile player that damaged the victim up to this long before the death
	// is credited with an assist (or the killing blow).
	AssistWindow = 15 * time.Second
	// KillingBlowWindow: without a PARTY_KILL the killer is the source of the last damage on
	// the victim, but only when that hit happened at most this long before the death.
	KillingBlowWindow = 5 * time.Second
	// DuplicateDeathWindow: UNIT_DIED, PARTY_KILL and SPELL_INSTAKILL for the same victim within
	// this long are one death (the first signal seen wins).
	DuplicateDeathWindow = 3 * time.Second

	maxHitsPerVictim  = 64  // damage events remembered per victim
	maxTrackedVictims = 256 // victims with a live damage window
	maxKnown          = 4096
	maxOwners         = 4096
	maxLastDeaths     = 512
)

// Options configure an Analyzer.
type Options struct {
	// RealmSuffix is stripped from names for display ("" = auto-detect from the stream).
	RealmSuffix string
}

// AnalyzerStats are counters of one Analyzer.
type AnalyzerStats struct {
	Deaths     int // friendly player deaths detected (feigns and duplicate signals excluded)
	Recorded   int // of those, new to the sink
	Duplicates int // of those, already known to the sink (re-analysis)
	Feigns     int // UNIT_DIED events with the unconscious/feign flag, ignored
	NPC        int // deaths without hostile player credit
}

type hit struct {
	t      time.Time
	credit string // GUID of the hostile player credited (owner for pets); "" = npc, environment or friendly
	friend bool   // the damage came from a friendly player (duel, mind control, own AoE)
	hasPos bool
	posOf  string // GUID the position describes
	x, y   float64
	mapID  int
}

type window struct {
	hits    []hit
	touched time.Time
}

type knownPlayer struct {
	raw      string
	hostile  bool
	friendly bool
}

// Analyzer turns parsed events into Deaths. It keeps small bounded sliding windows of recent
// damage per friendly victim; create one per log file (SetFile resets the windows) and share
// the Sink. Safe for concurrent use, though events must arrive in log order.
type Analyzer struct {
	sink  Sink
	namer *parser.Namer

	mu     sync.Mutex
	file   string
	wins   map[string]*window
	known  map[string]knownPlayer
	owners map[string]string // pet/totem/guardian GUID -> owner GUID
	last   map[string]time.Time
	zone   string
	maps   map[int]string
	now    time.Time
	stats  AnalyzerStats
}

// NewAnalyzer creates an Analyzer writing to sink.
func NewAnalyzer(sink Sink, o Options) *Analyzer {
	a := &Analyzer{sink: sink, namer: parser.NewNamer(o.RealmSuffix)}
	a.reset()
	return a
}

func (a *Analyzer) reset() {
	a.wins = map[string]*window{}
	a.known = map[string]knownPlayer{}
	a.owners = map[string]string{}
	a.last = map[string]time.Time{}
	a.zone = ""
	a.maps = map[int]string{}
}

// SetFile names the log file the following events come from (part of every death id). A
// different name clears the per-file windows; the same name is a no-op.
//
// MULTI-LOG: events carry a SourceID in the tracker; here the analyzer is per source/file and
// the file name keeps ids of different logs apart.
func (a *Analyzer) SetFile(name string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if name == a.file {
		return
	}
	a.file = name
	a.reset()
}

// Stats returns the counters.
func (a *Analyzer) Stats() AnalyzerStats {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.stats
}

func hostileFlags(f uint32) bool { return parser.ReactionOf(f) == parser.ReactionHostile }

// Apply consumes one parsed event.
func (a *Analyzer) Apply(ev parser.Event) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if ev.Time.After(a.now) || a.now.IsZero() {
		a.now = ev.Time
	}
	switch ev.Type {
	case "ZONE_CHANGE":
		if ev.Zone != "" {
			a.zone = ev.Zone
		}
		return
	case "MAP_CHANGE":
		if ev.Map != nil {
			if len(a.maps) > 512 {
				a.maps = map[int]string{}
			}
			a.maps[ev.Map.ID] = ev.Map.Name
		}
		return
	case "COMBAT_LOG_VERSION":
		return
	}
	a.learn(ev)

	switch {
	case ev.Type == "UNIT_DIED":
		if ev.Unconscious {
			if isFriendlyPlayer(ev.Dst) {
				a.stats.Feigns++
			}
			return
		}
		a.death(ev, nil)
	case ev.Type == "PARTY_KILL" || ev.Type == "SPELL_INSTAKILL":
		src := ev.Src
		a.death(ev, &src)
	case ev.IsDamage:
		a.hit(ev)
	}
}

func isFriendlyPlayer(u parser.Unit) bool {
	return parser.KindOf(u.GUID) == parser.KindPlayer && parser.ReactionOf(u.Flags) == parser.ReactionFriendly
}

// learn remembers which players are hostile/friendly and who owns which pet/totem.
func (a *Analyzer) learn(ev parser.Event) {
	for _, u := range []parser.Unit{ev.Src, ev.Dst} {
		if parser.KindOf(u.GUID) != parser.KindPlayer {
			continue
		}
		if u.Name != "" {
			a.namer.Observe(u.Name)
		}
		r := parser.ReactionOf(u.Flags)
		if r == parser.ReactionUnknown && u.Flags == 0 {
			continue
		}
		if len(a.known) >= maxKnown {
			a.dropHalf()
		}
		a.known[u.GUID] = knownPlayer{raw: u.Name, hostile: r == parser.ReactionHostile, friendly: r == parser.ReactionFriendly}
	}
	if ev.Info != nil && ev.Info.Owner != "" && parser.KindOf(ev.Info.GUID) != parser.KindPlayer {
		a.setOwner(ev.Info.GUID, ev.Info.Owner)
	}
	if ev.Type == "SPELL_SUMMON" && parser.KindOf(ev.Src.GUID) == parser.KindPlayer && ev.Dst.GUID != "" {
		a.setOwner(ev.Dst.GUID, ev.Src.GUID)
	}
}

func (a *Analyzer) setOwner(pet, owner string) {
	if len(a.owners) >= maxOwners {
		for k := range a.owners { // drop an arbitrary half
			delete(a.owners, k)
			if len(a.owners) <= maxOwners/2 {
				break
			}
		}
	}
	a.owners[pet] = owner
}

func (a *Analyzer) dropHalf() {
	for k := range a.known {
		delete(a.known, k)
		if len(a.known) <= maxKnown/2 {
			return
		}
	}
}

// creditOf resolves who gets bounty credit for damage dealt by src: a hostile player itself,
// or the hostile player owning a pet/guardian/totem. friend is true for friendly players.
func (a *Analyzer) creditOf(src parser.Unit) (guid string, friend bool) {
	switch parser.KindOf(src.GUID) {
	case parser.KindPlayer:
		if hostileFlags(src.Flags) {
			return src.GUID, false
		}
		return "", parser.ReactionOf(src.Flags) == parser.ReactionFriendly
	case parser.KindPet, parser.KindNPC:
		owner := a.owners[src.GUID]
		if owner == "" {
			return "", false
		}
		if k, ok := a.known[owner]; ok {
			if k.hostile {
				return owner, false
			}
			return "", k.friendly
		}
	}
	return "", false
}

// hit records damage on a friendly player in its sliding window.
func (a *Analyzer) hit(ev parser.Event) {
	if !isFriendlyPlayer(ev.Dst) {
		return
	}
	h := hit{t: ev.Time}
	h.credit, h.friend = a.creditOf(ev.Src)
	if ev.Info != nil && ev.Info.HasPos {
		h.hasPos, h.posOf, h.x, h.y, h.mapID = true, ev.Info.GUID, ev.Info.X, ev.Info.Y, ev.Info.MapID
	}
	w := a.wins[ev.Dst.GUID]
	if w == nil {
		if len(a.wins) >= maxTrackedVictims {
			a.evictVictim()
		}
		w = &window{}
		a.wins[ev.Dst.GUID] = w
	}
	w.touched = ev.Time
	// drop hits that can no longer matter, then keep the newest maxHitsPerVictim
	n := 0
	for _, o := range w.hits {
		if ev.Time.Sub(o.t) <= AssistWindow {
			w.hits[n] = o
			n++
		}
	}
	w.hits = append(w.hits[:n], h)
	if len(w.hits) > maxHitsPerVictim {
		w.hits = append(w.hits[:0], w.hits[len(w.hits)-maxHitsPerVictim:]...)
	}
}

func (a *Analyzer) evictVictim() {
	oldest, oldT := "", time.Time{}
	for g, w := range a.wins {
		if oldest == "" || w.touched.Before(oldT) {
			oldest, oldT = g, w.touched
		}
	}
	delete(a.wins, oldest)
}

// death handles a death signal. pkSrc is non-nil for PARTY_KILL / SPELL_INSTAKILL (the killer is named).
func (a *Analyzer) death(ev parser.Event, pkSrc *parser.Unit) {
	v := ev.Dst
	if !isFriendlyPlayer(v) {
		return
	}
	if lt, ok := a.last[v.GUID]; ok && absDur(ev.Time.Sub(lt)) <= DuplicateDeathWindow {
		return
	}
	if len(a.last) >= maxLastDeaths {
		a.last = map[string]time.Time{}
	}
	a.last[v.GUID] = ev.Time

	var hits []hit
	if w := a.wins[v.GUID]; w != nil {
		for _, h := range w.hits {
			if d := ev.Time.Sub(h.t); d >= 0 && d <= AssistWindow {
				hits = append(hits, h)
			}
		}
		delete(a.wins, v.GUID)
	}

	var killer string
	friendlyKill := false
	if pkSrc != nil {
		killer, friendlyKill = a.creditOf(*pkSrc)
	} else if n := len(hits); n > 0 {
		last := hits[n-1]
		for _, h := range hits { // latest by time (hits are appended in log order; ties keep the later line)
			if !h.t.Before(last.t) {
				last = h
			}
		}
		if ev.Time.Sub(last.t) <= KillingBlowWindow {
			killer, friendlyKill = last.credit, last.friend
		}
	}

	d := Death{Time: ev.Time, VictimGUID: v.GUID, VictimName: a.namer.Display(v.Name), Zone: a.zone}
	d.ID = deathID(v.GUID, ev.Time, a.file)
	if !friendlyKill {
		if killer != "" {
			p := a.participant(killer)
			d.Killer = &p
		}
		seen := map[string]bool{killer: true, "": true}
		for _, h := range hits {
			if !seen[h.credit] {
				seen[h.credit] = true
				d.Assists = append(d.Assists, a.participant(h.credit))
			}
		}
	}
	a.place(&d, killer, hits)

	a.stats.Deaths++
	if d.Killer == nil && len(d.Assists) == 0 {
		a.stats.NPC++
	}
	if a.sink.RecordDeath(d) {
		a.stats.Recorded++
	} else {
		a.stats.Duplicates++
	}
}

func (a *Analyzer) participant(guid string) Participant {
	raw := a.known[guid].raw
	p := Participant{GUID: guid, Raw: raw}
	if raw != "" {
		p.Display = a.namer.Display(raw)
	}
	return p
}

// place fills the death position: the killer's own position if one of its hits carried it,
// else the most recent located hit (the victim's position, i.e. where the fight was).
func (a *Analyzer) place(d *Death, killer string, hits []hit) {
	var best *hit
	for i := range hits {
		h := &hits[i]
		if !h.hasPos {
			continue
		}
		own := killer != "" && h.posOf == killer
		bestOwn := best != nil && killer != "" && best.posOf == killer
		if best == nil || (own && !bestOwn) || (own == bestOwn && !h.t.Before(best.t)) {
			best = h
		}
	}
	if best == nil {
		return
	}
	d.HasPos, d.X, d.Y, d.MapID = true, best.x, best.y, best.mapID
	if n := a.maps[best.mapID]; n != "" {
		d.Zone = n
	}
}

// deathID is stable across runs: victim GUID + death time (ms) + log file name, hashed.
func deathID(victim string, t time.Time, file string) string {
	h := sha1.Sum([]byte(victim + "|" + strconv.FormatInt(t.UnixMilli(), 10) + "|" + file))
	return hex.EncodeToString(h[:10])
}

func absDur(d time.Duration) time.Duration {
	if d < 0 {
		return -d
	}
	return d
}
