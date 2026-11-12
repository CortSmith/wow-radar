package domain

import (
	"math"
	"regexp"
	"time"
	"unicode/utf8"
)

// Size caps for one ping; they bound memory on a small host.
const (
	PingMaxContacts        = 500
	PingMaxCharts          = 200
	PingMaxDeaths          = 1000
	PingMaxFixesPerContact = TrailCapFixes
	sourceMaxChars         = 64
	// PingMaxFixesTotal bounds all fixes of one ping together.
	PingMaxFixesTotal = 20000
	// DeathMaxAssists bounds the assist list of one death.
	DeathMaxAssists = 50
)

// Value bounds for ping contents; they are generous for the game and tight for an attacker.
const (
	// CoordMax bounds |x| and |y| of positions and chart rectangles, in yards.
	CoordMax = 100000
	// MaxMapID bounds chart ids.
	MaxMapID = 65535
	// PingLevelMax is the highest believable character level.
	PingLevelMax = 120
	// MaxHPLimit bounds a contact's maximum hit points.
	MaxHPLimit = 1e9
	// DeltaMaxCount bounds every integer counter delta carried by one ping for one contact.
	DeltaMaxCount = 1_000_000
	// DeltaMaxValue bounds the damage and hit-point sum deltas.
	DeltaMaxValue = 1e10
	// MinTimestampMs is 2004-01-01 UTC, before which no timestamp is believable.
	MinTimestampMs = 1_072_915_200_000
	// PingMaxFuture is how far ahead of the server clock a timestamp may be.
	PingMaxFuture = 24 * time.Hour
	// Display-name caps in runes.
	NameMaxRunes = 48
	ZoneMaxRunes = 96
	deathIDMax   = 128
)

var (
	batchIDPattern = regexp.MustCompile(`^[\w.:-]{1,64}$`)
	// guidPattern accepts player GUIDs only; pets and creatures are never contacts.
	guidPattern    = regexp.MustCompile(`^Player-[A-Za-z0-9_-]{1,40}$`)
	deathIDPattern = regexp.MustCompile(`^[\w.:|-]{1,128}$`)
)

// Deltas are the counter increments carried by one ping for one contact.
type Deltas struct {
	Events    int64   `json:"events"`
	SrcEvents int64   `json:"srcEvents"`
	DmgTotal  float64 `json:"dmgTotal"`
	DmgHits   int64   `json:"dmgHits"`
	DmgEvents int64   `json:"dmgEvents"`
	HPSum     float64 `json:"hpSum"`
	HPSamples int64   `json:"hpSamples"`
	KB        int64   `json:"kb"`
	Assists   int64   `json:"assists"`
}

// IsZero reports whether the deltas change nothing.
func (d Deltas) IsZero() bool { return d == Deltas{} }

func (d Deltas) valid() bool {
	counts := []int64{d.Events, d.SrcEvents, d.DmgHits, d.DmgEvents, d.HPSamples, d.KB, d.Assists}
	for _, n := range counts {
		if n < 0 || n > DeltaMaxCount {
			return false
		}
	}
	for _, v := range []float64{d.DmgTotal, d.HPSum} {
		if v < 0 || v > DeltaMaxValue || math.IsNaN(v) || math.IsInf(v, 0) {
			return false
		}
	}
	return true
}

// PingContact is a contact observation inside a ping: the latest identity and position, the counter
// deltas since the previous ping, and the new trail fixes. The counter fields of the embedded
// Contact are ignored; only D adds to them.
type PingContact struct {
	Contact
	D     Deltas `json:"d"`
	Fixes []Fix  `json:"fixes"`
}

// Death records one friendly player death for the bounty ledger. Counters are credited through
// Deltas.KB and Deltas.Assists; the Death itself is the deduplicated history entry.
type Death struct {
	ID      string   `json:"id"`
	T       int64    `json:"t"`
	Victim  string   `json:"victim"`
	Killer  string   `json:"killer,omitempty"`
	Assists []string `json:"assists,omitempty"`
}

// Ping is one batch sent from ingest to the database. Replaying a BatchID is a no-op.
type Ping struct {
	RS       Ruleset       `json:"rs"`
	BatchID  string        `json:"batchId"`
	Source   string        `json:"source"`
	Contacts []PingContact `json:"contacts"`
	Charts   []Chart       `json:"charts"`
	Deaths   []Death       `json:"deaths"`
}

// Validate checks identifiers, size caps, text, coordinate, level and delta bounds. It never modifies
// the ping and does not check timestamps against a clock (see ValidateAt). Error messages name the
// offending field, never its value.
func (p Ping) Validate() error { return p.validate(nil) }

// ValidateAt is Validate plus the timestamp window: every non-zero time must lie between
// MinTimestampMs and now+PingMaxFuture.
func (p Ping) ValidateAt(now time.Time) error { return p.validate(&now) }

func (p Ping) validate(now *time.Time) error {
	if !p.RS.Valid() {
		return invalidf("unknown ruleset")
	}
	if !batchIDPattern.MatchString(p.BatchID) {
		return invalidf("batchId must match %s", batchIDPattern)
	}
	if !validText(p.Source, sourceMaxChars) {
		return invalidf("source is too long or has unsafe characters")
	}
	if err := p.validateSizes(); err != nil {
		return err
	}
	w := newTimeWindow(now)
	if err := p.validateContacts(w); err != nil {
		return err
	}
	for i, ch := range p.Charts {
		if !ch.Valid() || !validText(ch.Name, ZoneMaxRunes) {
			return invalidf("chart %d is invalid", i)
		}
	}
	return p.validateDeaths(w)
}

func (p Ping) validateSizes() error {
	fixes := 0
	for _, c := range p.Contacts {
		fixes += len(c.Fixes)
	}
	switch {
	case len(p.Contacts) > PingMaxContacts:
		return invalidf("more than %d contacts", PingMaxContacts)
	case len(p.Charts) > PingMaxCharts:
		return invalidf("more than %d charts", PingMaxCharts)
	case len(p.Deaths) > PingMaxDeaths:
		return invalidf("more than %d deaths", PingMaxDeaths)
	case fixes > PingMaxFixesTotal:
		return invalidf("more than %d fixes in one ping", PingMaxFixesTotal)
	}
	return nil
}

func (p Ping) validateContacts(w timeWindow) error {
	for i, c := range p.Contacts {
		if !guidPattern.MatchString(c.GUID) {
			return invalidf("contact %d: bad guid", i)
		}
		if err := validateContactFields(c.Contact); err != nil {
			return invalidf("contact %d: %v", i, err)
		}
		for _, t := range []int64{c.PosTime, c.FirstSeen, c.LastSeen} {
			if !w.ok(t) {
				return invalidf("contact %d: timestamp outside the accepted window", i)
			}
		}
		if c.FirstSeen > 0 && c.LastSeen > 0 && c.FirstSeen > c.LastSeen {
			return invalidf("contact %d: firstSeen is after lastSeen", i)
		}
		if len(c.Fixes) > PingMaxFixesPerContact {
			return invalidf("contact %d: more than %d fixes", i, PingMaxFixesPerContact)
		}
		if !c.D.valid() {
			return invalidf("contact %d: deltas must be finite, non-negative and within bounds", i)
		}
		for j, f := range c.Fixes {
			if !validFix(f) || !w.ok(f.T) {
				return invalidf("contact %d: fix %d is invalid", i, j)
			}
		}
	}
	return nil
}

func validateContactFields(c Contact) error {
	switch {
	case !validText(c.Name, NameMaxRunes), !validText(c.Raw, ZoneMaxRunes), !validText(c.Zone, ZoneMaxRunes):
		return invalidf("name or zone is too long or has unsafe characters")
	case len(c.PastNames) > PastNamesKeptMax:
		return invalidf("too many past names")
	case c.Level < 0 || c.Level > PingLevelMax:
		return invalidf("level out of range")
	case !finiteWithin(c.MaxHP, 0, MaxHPLimit):
		return invalidf("maxHp out of range")
	case !finiteWithin(c.X, -CoordMax, CoordMax) || !finiteWithin(c.Y, -CoordMax, CoordMax):
		return invalidf("position out of range")
	case c.MapID < 0 || c.MapID > MaxMapID:
		return invalidf("mapId out of range")
	case !validReaction(c.Reaction):
		return invalidf("unknown reaction")
	}
	for _, n := range c.PastNames {
		if !validText(n, NameMaxRunes) {
			return invalidf("past name is too long or has unsafe characters")
		}
	}
	return nil
}

func (p Ping) validateDeaths(w timeWindow) error {
	for i, d := range p.Deaths {
		switch {
		case !deathIDPattern.MatchString(d.ID):
			return invalidf("death %d has a bad id", i)
		case !w.ok(d.T):
			return invalidf("death %d: timestamp outside the accepted window", i)
		case !guidPattern.MatchString(d.Victim):
			return invalidf("death %d: bad victim", i)
		case d.Killer != "" && !guidPattern.MatchString(d.Killer):
			return invalidf("death %d: bad killer", i)
		case len(d.Assists) > DeathMaxAssists:
			return invalidf("death %d: more than %d assists", i, DeathMaxAssists)
		}
		for _, a := range d.Assists {
			if !guidPattern.MatchString(a) {
				return invalidf("death %d: bad assist", i)
			}
		}
	}
	return nil
}

// Sanitize repairs display text in place: invalid UTF-8, control characters (newline and tab become a
// space), bidi overrides and other format characters are removed, names are cut to their caps and the
// past-name list is capped. It returns how many repairs it made, so callers can count and log them.
func (p *Ping) Sanitize() int {
	n := 0
	clean := func(s *string, maxRunes int) {
		out, k := cleanText(*s, maxRunes)
		*s = out
		n += k
	}
	clean(&p.Source, sourceMaxChars)
	for i := range p.Contacts {
		c := &p.Contacts[i]
		clean(&c.Name, NameMaxRunes)
		clean(&c.Raw, ZoneMaxRunes)
		clean(&c.Zone, ZoneMaxRunes)
		if len(c.PastNames) > PastNamesKeptMax {
			c.PastNames = c.PastNames[:PastNamesKeptMax]
			n++
		}
		for j := range c.PastNames {
			clean(&c.PastNames[j], NameMaxRunes)
		}
	}
	for i := range p.Charts {
		clean(&p.Charts[i].Name, ZoneMaxRunes)
	}
	return n
}

// timeWindow is the accepted range for non-zero timestamps; a nil clock only checks the absolute bounds.
type timeWindow struct {
	lo, hi int64
	clock  bool
}

func newTimeWindow(now *time.Time) timeWindow {
	if now == nil {
		return timeWindow{hi: TimestampMaxMs}
	}
	return timeWindow{lo: MinTimestampMs, hi: min(TimestampMaxMs, now.Add(PingMaxFuture).UnixMilli()), clock: true}
}

// ok accepts 0 (unset) and values in [lo, hi].
func (w timeWindow) ok(t int64) bool { return t == 0 || (t >= w.lo && t <= w.hi && t > 0) }

func validText(s string, maxRunes int) bool {
	return utf8.RuneCountInString(s) <= maxRunes && isSafeText(s)
}

func finiteWithin(v, lo, hi float64) bool { return !math.IsNaN(v) && v >= lo && v <= hi }

func validReaction(r Reaction) bool {
	switch r {
	case "", ReactionFriendly, ReactionHostile, ReactionNeutral, ReactionUnknown:
		return true
	}
	return false
}

func validFix(f Fix) bool {
	return finiteWithin(f.X, -CoordMax, CoordMax) && finiteWithin(f.Y, -CoordMax, CoordMax) &&
		f.MapID >= 0 && f.MapID <= MaxMapID && (f.Kind == "" || f.Kind == FixExact || f.Kind == FixApprox)
}
