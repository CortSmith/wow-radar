package contactx

import (
	"cmp"
	"math"
	"slices"
	"time"

	"radarlive/internal/domain"
)

// Defaults and bounds.
const (
	// ExpireAfter is how long a flushed contact may stay silent before Flush forgets its in-memory state.
	ExpireAfter = 15 * time.Minute
	// DefaultApproxMaxAge is the oldest "me" fix an approximate sighting may borrow.
	DefaultApproxMaxAge = 120 * time.Second
	// DefaultMaxContacts bounds the contacts tracked per source.
	DefaultMaxContacts = 4000
	// evictFraction: when full, this share of the least recently seen contacts is dropped at once.
	evictFraction = 4
)

// Options configure an Extractor.
type Options struct {
	// TrackNPCs also tracks Creature, Vehicle and Pet GUIDs (default: players only). Pings accept only
	// players, so the module drops these before publishing; the option exists for other consumers.
	TrackNPCs bool
	// DisableApprox turns off approximate sightings near the log owner.
	DisableApprox bool
	// ApproxMaxAge is the oldest "me" fix an approximate sighting may use (default 120 s).
	ApproxMaxAge time.Duration
	// MaxContacts caps tracked contacts; the least recently seen are evicted (default 4000).
	MaxContacts int
	// RealmSuffix fixes the realm suffix stripped from names ("" = detect from the stream).
	RealmSuffix string
}

// Part is what one Flush yields: the pieces of a domain.Ping that this extractor knows. The caller
// adds RS (use Ruleset when set), BatchID and Source.
type Part struct {
	Ruleset  domain.Ruleset // last ruleset seen in the stream, "" if none
	Contacts []domain.PingContact
	Charts   []domain.Chart
}

// Extractor turns the events of ONE log source into contact deltas. It is not safe for concurrent use.
type Extractor struct {
	opts    Options
	namer   *domain.Namer
	units   map[string]*unit
	tick    uint64
	meGUID  string
	meFix   fix
	hasMe   bool
	zone    string
	mapID   int
	charts  map[int]domain.Chart
	chartsQ map[int]domain.Chart // charts waiting for the next flush
	ruleset domain.Ruleset
}

// NewExtractor creates an Extractor.
func NewExtractor(o Options) *Extractor {
	if o.ApproxMaxAge <= 0 {
		o.ApproxMaxAge = DefaultApproxMaxAge
	}
	if o.MaxContacts <= 0 {
		o.MaxContacts = DefaultMaxContacts
	}
	return &Extractor{opts: o, namer: domain.NewNamer(o.RealmSuffix), units: map[string]*unit{},
		charts: map[int]domain.Chart{}, chartsQ: map[int]domain.Chart{}}
}

// Me returns the log owner's GUID once seen ("" before).
func (e *Extractor) Me() string { return e.meGUID }

// Tracked returns how many contacts are held in memory.
func (e *Extractor) Tracked() int { return len(e.units) }

func (e *Extractor) tracked(guid string) bool {
	switch domain.KindOf(guid) {
	case domain.KindPlayer:
		return true
	case domain.KindNPC, domain.KindPet:
		return e.opts.TrackNPCs
	}
	return false
}

// Feed consumes one event from this extractor's source, in log order.
func (e *Extractor) Feed(ev domain.Event) {
	if ev.Ruleset != "" {
		e.ruleset = ev.Ruleset
	}
	switch ev.Name {
	case "ZONE_CHANGE":
		e.zone = firstNonEmpty(ev.Zone, e.zone)
		return
	case "MAP_CHANGE":
		e.feedChart(ev.Chart)
		return
	case "COMBAT_LOG_VERSION":
		return
	}
	e.feedCombat(ev)
}

func (e *Extractor) feedChart(c *domain.Chart) {
	if c == nil || !c.Valid() {
		return
	}
	e.mapID = c.MapID
	if e.zone == "" {
		e.zone = c.Name
	}
	if old, ok := e.charts[c.MapID]; ok && old == *c {
		return
	}
	e.charts[c.MapID] = *c
	e.chartsQ[c.MapID] = *c
}

func (e *Extractor) feedCombat(ev domain.Event) {
	parts := e.participants(ev)
	for _, p := range parts {
		u := e.touch(p.guid, ev.TimeMs)
		u.observe(ev.TimeMs, p.raw, p.flags)
		u.d.Events++
		if p.guid == ev.Src.GUID {
			u.d.SrcEvents++
		}
		if domain.KindOf(p.guid) == domain.KindPlayer {
			e.namer.Observe(p.raw)
		}
		if p.mine {
			e.meGUID = p.guid
		}
	}
	infoGUID := e.feedInfo(ev)
	e.feedApprox(ev, parts, infoGUID)
	e.feedDamage(ev)
}

// part is one tracked unit mentioned by an event.
type part struct {
	guid, raw string
	flags     uint32
	mine      bool
}

// participants lists the distinct tracked units of an event: source, destination, info unit.
func (e *Extractor) participants(ev domain.Event) []part {
	var out []part
	add := func(p part) {
		if p.guid != "" && e.tracked(p.guid) && !slices.ContainsFunc(out, func(q part) bool { return q.guid == p.guid }) {
			out = append(out, p)
		}
	}
	for _, u := range []domain.EventUnit{ev.Src, ev.Dst} {
		add(part{guid: u.GUID, raw: u.Raw, flags: u.Flags, mine: u.IsMine()})
	}
	if ev.Info != nil {
		add(part{guid: ev.Info.GUID})
	}
	return out
}

// feedInfo applies the advanced block and returns the GUID whose exact position it supplied.
func (e *Extractor) feedInfo(ev domain.Event) string {
	in := ev.Info
	if in == nil || !e.tracked(in.GUID) {
		return ""
	}
	u := e.units[in.GUID]
	if u == nil {
		return ""
	}
	if in.HasHP && saneHP(in.HP) && saneHP(in.MaxHP) {
		u.maxHP = max(u.maxHP, in.MaxHP)
		u.d.HPSum += in.HP
		u.d.HPSamples++
	}
	if in.Level > 0 {
		u.level = in.Level
	}
	if !in.HasPos {
		return ""
	}
	f := fix{t: ev.TimeMs, x: in.X, y: in.Y, mapID: in.MapID, zone: e.zoneFor(in.MapID), kind: domain.FixExact}
	u.addFix(f)
	if in.GUID == e.meGUID {
		e.meFix, e.hasMe, e.mapID = f, true, in.MapID
	}
	return in.GUID
}

// feedApprox gives other players mentioned near a fresh "me" fix an approximate position.
func (e *Extractor) feedApprox(ev domain.Event, parts []part, infoGUID string) {
	if e.opts.DisableApprox || !e.hasMe {
		return
	}
	age := time.Duration(ev.TimeMs-e.meFix.t) * time.Millisecond
	if age < 0 {
		age = -age
	}
	if age >= e.opts.ApproxMaxAge {
		return
	}
	for _, p := range parts {
		if domain.KindOf(p.guid) != domain.KindPlayer || p.guid == e.meGUID || p.guid == infoGUID {
			continue
		}
		f := e.meFix
		f.t, f.kind = ev.TimeMs, domain.FixApprox
		if u := e.units[p.guid]; u != nil {
			u.addFix(f)
		}
	}
}

// feedDamage credits the source of a damage event (never the *_LANDED duplicates; the parser leaves
// IsDamage false for them).
func (e *Extractor) feedDamage(ev domain.Event) {
	if !ev.IsDamage || !e.tracked(ev.Src.GUID) {
		return
	}
	u := e.units[ev.Src.GUID]
	if u == nil {
		return
	}
	u.d.DmgEvents++
	if ev.HasAmount && ev.Amount > 0 && !math.IsInf(ev.Amount, 0) {
		u.d.DmgHits++
		u.d.DmgTotal += ev.Amount
	}
}

func (e *Extractor) zoneFor(mapID int) string {
	if c, ok := e.charts[mapID]; ok && c.Name != "" {
		return c.Name
	}
	return e.zone
}

// touch returns the unit for guid, creating it (and evicting old ones when full).
func (e *Extractor) touch(guid string, t int64) *unit {
	u := e.units[guid]
	if u == nil {
		if len(e.units) >= e.opts.MaxContacts {
			e.evict()
		}
		u = newUnit(guid, t)
		e.units[guid] = u
	}
	e.tick++
	u.touched = e.tick
	return u
}

// evict drops the least recently seen quarter of the contacts, never the log owner.
func (e *Extractor) evict() {
	all := make([]*unit, 0, len(e.units))
	for _, u := range e.units {
		if u.guid != e.meGUID {
			all = append(all, u)
		}
	}
	slices.SortFunc(all, func(a, b *unit) int { return cmp.Compare(a.touched, b.touched) })
	drop := max(len(e.units)/evictFraction, 1)
	for _, u := range all[:min(drop, len(all))] {
		delete(e.units, u.guid)
	}
}

// Flush returns everything changed since the previous Flush and resets it: counters are deltas, fixes
// are the new trail points, charts are new or recalibrated. A second Flush with no new events returns
// an empty Part. A non-zero now also forgets clean contacts idle for ExpireAfter.
func (e *Extractor) Flush(now time.Time) Part {
	part := Part{Ruleset: e.ruleset}
	guids := make([]string, 0, len(e.units))
	for g, u := range e.units {
		if u.dirty {
			guids = append(guids, g)
		}
	}
	slices.Sort(guids)
	for _, g := range guids {
		part.Contacts = append(part.Contacts, e.units[g].take(e.namer.Display))
	}
	for _, id := range sortedKeys(e.chartsQ) {
		part.Charts = append(part.Charts, e.chartsQ[id])
	}
	clear(e.chartsQ)
	e.prune(now)
	return part
}

// prune forgets clean contacts that have been silent for ExpireAfter (never the log owner).
func (e *Extractor) prune(now time.Time) {
	if now.IsZero() {
		return
	}
	for g, u := range e.units {
		if g != e.meGUID && !u.dirty && now.UnixMilli()-u.lastSeen > ExpireAfter.Milliseconds() {
			delete(e.units, g)
		}
	}
}

// saneHP accepts a finite, non-negative hit point value; anything else would corrupt the sums.
func saneHP(v float64) bool { return !math.IsNaN(v) && !math.IsInf(v, 0) && v >= 0 }

func sortedKeys(m map[int]domain.Chart) []int {
	keys := make([]int, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}
