package domain

import "time"

// DefaultActiveWindow is how long a silent contact stays in live views; StaleAfter is when the page greys it out.
const (
	DefaultActiveWindow = 15 * time.Minute
	StaleAfter          = 5 * time.Minute
)

// Scope selects what a sweep looks at.
type Scope string

// Sweep scopes.
const (
	ScopePlayer Scope = "player"
	ScopeZone   Scope = "zone"
	ScopeAll    Scope = "all"
)

// Sector describes one sweep: which contacts the page wants and how fresh they must be.
// MapID is a pointer because chart 0 is a real map.
type Sector struct {
	RS             Ruleset `json:"rs"`
	Scope          Scope   `json:"scope"`
	GUID           string  `json:"guid,omitempty"`
	MapID          *int    `json:"mapId,omitempty"`
	Group          Group   `json:"grp,omitempty"`
	Query          string  `json:"q,omitempty"`
	Since          int64   `json:"since"`
	ActiveWithinMs int64   `json:"activeWithin,omitempty"`
	Trail          bool    `json:"trail,omitempty"`
}

// Validate checks that the sector is internally consistent.
func (s Sector) Validate() error {
	if !s.RS.Valid() {
		return invalidf("unknown ruleset %q", s.RS)
	}
	if !s.Group.Valid() {
		return invalidf("unknown group %q", s.Group)
	}
	if s.Since < 0 || s.ActiveWithinMs < 0 {
		return invalidf("since and activeWithin must not be negative")
	}
	switch s.Scope {
	case ScopeAll:
	case ScopeZone:
		if s.MapID == nil {
			return invalidf("scope zone needs mapId")
		}
	case ScopePlayer:
		if s.GUID == "" {
			return invalidf("scope player needs guid")
		}
	default:
		return invalidf("unknown scope %q", s.Scope)
	}
	return nil
}

// ActiveWindow returns the effective activity window.
func (s Sector) ActiveWindow() time.Duration {
	if s.ActiveWithinMs > 0 {
		return time.Duration(s.ActiveWithinMs) * time.Millisecond
	}
	return DefaultActiveWindow
}

// Includes reports whether a contact belongs in the sweep at time now. The activity window applies to
// zone and all sweeps only: a player explicitly being tracked stays visible however old.
func (s Sector) Includes(c Contact, now time.Time) bool {
	if c.LastSeen <= s.Since || !s.Group.Contains(c.Reaction) || !c.MatchesQuery(s.Query) {
		return false
	}
	switch s.Scope {
	case ScopePlayer:
		return c.GUID == s.GUID
	case ScopeZone:
		return s.MapID != nil && c.HasPosition() && c.MapID == *s.MapID && s.isActive(c, now)
	}
	return s.isActive(c, now)
}

func (s Sector) isActive(c Contact, now time.Time) bool {
	return now.UnixMilli()-c.LastSeen <= s.ActiveWindow().Milliseconds()
}

// SearchQuery is a contact search; MapID nil means any chart.
type SearchQuery struct {
	RS     Ruleset `json:"rs"`
	Q      string  `json:"q"`
	MapID  *int    `json:"mapId,omitempty"`
	Group  Group   `json:"grp,omitempty"`
	Limit  int     `json:"limit"`
	Offset int     `json:"offset"`
}

// Search paging limits.
const (
	SearchDefaultLimit = 50
	SearchMaxLimit     = 500
)

// Validate checks the ruleset, group and paging values.
func (q SearchQuery) Validate() error {
	switch {
	case !q.RS.Valid():
		return invalidf("unknown ruleset %q", q.RS)
	case !q.Group.Valid():
		return invalidf("unknown group %q", q.Group)
	case q.Limit < 0 || q.Offset < 0:
		return invalidf("limit and offset must not be negative")
	}
	return nil
}

// Includes reports whether a contact matches the text, chart and group filters.
func (q SearchQuery) Includes(c Contact) bool {
	if q.MapID != nil && (!c.HasPosition() || c.MapID != *q.MapID) {
		return false
	}
	return q.Group.Contains(c.Reaction) && c.MatchesQuery(q.Q)
}

// ClampLimit returns def for non-positive limits and caps the rest at maxLimit.
func ClampLimit(limit, def, maxLimit int) int {
	if limit <= 0 {
		return def
	}
	return min(limit, maxLimit)
}

// ApplyResult reports what a ping did.
type ApplyResult struct {
	Received  int  `json:"received"`
	Changed   int  `json:"changed"`
	Duplicate bool `json:"duplicate"`
}

// SweepResult is the answer to one sweep; Now is the store's clock in epoch milliseconds.
type SweepResult struct {
	Now      int64     `json:"now"`
	Changed  bool      `json:"changed"`
	Sector   Sector    `json:"sector"`
	Contacts []Contact `json:"contacts"`
	Trail    []Fix     `json:"trail,omitempty"`
}

// SearchResult is one page of search hits; Total counts all matches before paging.
type SearchResult struct {
	Total    int       `json:"total"`
	Contacts []Contact `json:"contacts"`
}

// RulesetStatus summarises one ruleset partition.
type RulesetStatus struct {
	ID        Ruleset `json:"id"`
	Contacts  int     `json:"contacts"`
	Deaths    int     `json:"deaths"`
	LastWrite int64   `json:"lastWrite"`
}
