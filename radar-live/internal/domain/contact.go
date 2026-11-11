package domain

import (
	"math"
	"reflect"
	"slices"
	"strings"
)

// PastNamesKeptMax is how many former names a contact remembers.
const PastNamesKeptMax = 10

// Contact is one observed player. Counters are running totals built from additive Deltas.
// Times are UTC epoch milliseconds. A contact has a known position only when PosTime > 0.
type Contact struct {
	GUID      string   `json:"guid"`
	Name      string   `json:"name"`
	Raw       string   `json:"raw"`
	PastNames []string `json:"pastNames"`
	Reaction  Reaction `json:"reaction"`
	Level     int      `json:"level"`
	MaxHP     float64  `json:"maxHp"`
	X         float64  `json:"x"`
	Y         float64  `json:"y"`
	MapID     int      `json:"mapId"`
	Zone      string   `json:"zone"`
	PosTime   int64    `json:"posTime"`
	FirstSeen int64    `json:"firstSeen"`
	LastSeen  int64    `json:"lastSeen"`
	Events    int64    `json:"events"`
	SrcEvents int64    `json:"srcEvents"`
	DmgTotal  float64  `json:"dmgTotal"`
	DmgHits   int64    `json:"dmgHits"`
	DmgEvents int64    `json:"dmgEvents"`
	HPSum     float64  `json:"hpSum"`
	HPSamples int64    `json:"hpSamples"`
	KB        int64    `json:"kb"`
	Assists   int64    `json:"assists"`
}

// Round1 rounds to one decimal place, the precision used for yards, damage and hit points.
func Round1(v float64) float64 { return math.Round(v*10) / 10 }

// HasPosition reports whether X, Y, MapID and Zone are meaningful.
func (c Contact) HasPosition() bool { return c.PosTime > 0 }

// MatchesQuery reports whether q (case-insensitive substring) occurs in the name, raw name or any past name.
// An empty query matches everything.
func (c Contact) MatchesQuery(q string) bool {
	q = strings.ToLower(strings.TrimSpace(q))
	if q == "" {
		return true
	}
	if strings.Contains(strings.ToLower(c.Name), q) || strings.Contains(strings.ToLower(c.Raw), q) {
		return true
	}
	return slices.ContainsFunc(c.PastNames, func(n string) bool { return strings.Contains(strings.ToLower(n), q) })
}

// Clone returns a copy that shares no slices with c.
func (c Contact) Clone() Contact {
	c.PastNames = slices.Clone(c.PastNames)
	return c
}

// Equal reports whether two contacts hold the same data.
func (c Contact) Equal(o Contact) bool {
	names, otherNames := c.PastNames, o.PastNames
	c.PastNames, o.PastNames = nil, nil
	return reflect.DeepEqual(c, o) && slices.Equal(names, otherNames)
}
