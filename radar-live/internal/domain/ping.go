package domain

import (
	"math"
	"regexp"
)

// Size caps for one ping; they bound memory on a small host.
const (
	PingMaxContacts        = 500
	PingMaxCharts          = 200
	PingMaxDeaths          = 1000
	PingMaxFixesPerContact = TrailCapFixes
	sourceMaxChars         = 64
)

var (
	batchIDPattern = regexp.MustCompile(`^[\w.:-]{1,64}$`)
	// guidPattern accepts player GUIDs only; pets and creatures are never contacts.
	guidPattern = regexp.MustCompile(`^Player-[\w-]{1,40}$`)
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
		if n < 0 {
			return false
		}
	}
	for _, v := range []float64{d.DmgTotal, d.HPSum} {
		if v < 0 || math.IsNaN(v) || math.IsInf(v, 0) {
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

// Validate checks identifiers, size caps and delta sanity. It never modifies the ping.
func (p Ping) Validate() error {
	if !p.RS.Valid() {
		return invalidf("unknown ruleset %q", p.RS)
	}
	if !batchIDPattern.MatchString(p.BatchID) {
		return invalidf("batchId must match %s", batchIDPattern)
	}
	if len(p.Source) > sourceMaxChars {
		return invalidf("source longer than %d characters", sourceMaxChars)
	}
	if err := p.validateSizes(); err != nil {
		return err
	}
	return p.validateEntries()
}

func (p Ping) validateSizes() error {
	switch {
	case len(p.Contacts) > PingMaxContacts:
		return invalidf("more than %d contacts", PingMaxContacts)
	case len(p.Charts) > PingMaxCharts:
		return invalidf("more than %d charts", PingMaxCharts)
	case len(p.Deaths) > PingMaxDeaths:
		return invalidf("more than %d deaths", PingMaxDeaths)
	}
	return nil
}

func (p Ping) validateEntries() error {
	for i, c := range p.Contacts {
		if !guidPattern.MatchString(c.GUID) {
			return invalidf("contact %d: bad guid %q", i, c.GUID)
		}
		if len(c.Fixes) > PingMaxFixesPerContact {
			return invalidf("contact %d: more than %d fixes", i, PingMaxFixesPerContact)
		}
		if !c.D.valid() {
			return invalidf("contact %d: deltas must be finite and non-negative", i)
		}
	}
	for i, ch := range p.Charts {
		if !ch.Valid() {
			return invalidf("chart %d is invalid", i)
		}
	}
	for i, d := range p.Deaths {
		if d.ID == "" {
			return invalidf("death %d has no id", i)
		}
	}
	return nil
}
