package readapi

import (
	"errors"
	"fmt"
	"net/url"

	"radarlive/internal/domain"
	"radarlive/internal/validate"
)

// Parameter bounds; values outside are rejected (400), values inside are clamped by the handler.
const (
	maxQueryChars  = 64
	maxMapID       = domain.MaxMapID
	maxLimitParam  = 100_000
	maxOffsetParam = 1_000_000
	maxActiveMs    = 30 * 24 * 3600 * 1000 // 30 days
)

// Allowed parameter names per route; anything else is a 400.
var (
	sweepParams      = []string{"rs", "scope", "guid", "mapId", "grp", "q", "since", "activeWithin", "trail"}
	contactsParams   = []string{"rs", "q", "mapId", "grp", "limit", "offset"}
	trailParams      = []string{"rs", "guid", "mapId"}
	bountyParams     = []string{"rs", "limit"}
	playerParams     = []string{"rs", "guid"}
	chartsParams     = []string{"rs"}
	armoryOneParams  = []string{"realm", "name"}
	armoryManyParams = []string{"realm", "names"}
)

// params parses the query through the central validate layer; the first error sticks.
type params struct {
	q         *validate.Query
	defaultRS domain.Ruleset
}

func newQuery(v url.Values, allowed []string, defaultRS domain.Ruleset) *params {
	return &params{q: validate.NewQuery(v, allowed...), defaultRS: defaultRS}
}

// err returns the first validation problem, wrapped so it maps to 400.
func (p *params) err() error {
	if err := p.q.Err(); err != nil {
		var ve *validate.Error
		if errors.As(err, &ve) {
			return fmt.Errorf("%w: %s", domain.ErrInvalid, ve.Error())
		}
		return err
	}
	return nil
}

func (p *params) ruleset() domain.Ruleset {
	all := domain.AllRulesets()
	names := make([]string, len(all))
	for i, rs := range all {
		names[i] = string(rs)
	}
	return domain.Ruleset(p.q.Enum("rs", string(p.defaultRS), true, names...))
}

func (p *params) group() domain.Group {
	return domain.Group(p.q.Enum("grp", string(domain.GroupAny), true,
		string(domain.GroupAny), string(domain.GroupFriendly), string(domain.GroupHostile), string(domain.GroupOther)))
}

func (p *params) scope() domain.Scope {
	return domain.Scope(p.q.Enum("scope", string(domain.ScopeAll), false,
		string(domain.ScopeAll), string(domain.ScopeZone), string(domain.ScopePlayer)))
}

func (p *params) text(key string) string { return p.q.Text(key, maxQueryChars) }

func (p *params) guid() string { return p.q.GUID("guid", false) }

func (p *params) requireGUID() string { return p.q.GUID("guid", true) }

// number parses an optional integer in [0, hi]; absent is 0.
func (p *params) number(key string, hi int64) int64 {
	n, _ := p.q.Int(key, 0, hi)
	return n
}

// mapID parses the optional chart id; 0 is a valid chart.
func (p *params) mapID() *int {
	n, ok := p.q.Int("mapId", 0, maxMapID)
	if !ok || p.q.Err() != nil {
		return nil
	}
	id := int(n)
	return &id
}

func (p *params) flag(key string) bool { return p.q.Flag(key) }
