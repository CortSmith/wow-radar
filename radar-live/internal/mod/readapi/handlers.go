package readapi

import (
	"context"
	"net/url"
	"time"

	"radarlive/internal/core"
	"radarlive/internal/domain"
)

const bountyDefaultLimit, bountyMaxLimit = 50, 500

// newParams starts parsing q with the configured default ruleset and the route's allowed names.
func (m *Module) newParams(q url.Values, allowed []string) *params {
	rs, err := domain.ParseRuleset(m.host.Config.String(KeyDefaultRuleset))
	if err != nil {
		rs = domain.DefaultRuleset()
	}
	return newQuery(q, allowed, rs)
}

func (m *Module) sweep(ctx context.Context, st core.Store, q url.Values) (any, error) {
	p := m.newParams(q, sweepParams)
	sec := domain.Sector{RS: p.ruleset(), Scope: p.scope(), GUID: p.guid(), MapID: p.mapID(), Group: p.group(),
		Query: p.text("q"), Since: p.number("since", domain.TimestampMaxMs),
		ActiveWithinMs: p.number("activeWithin", maxActiveMs), Trail: p.flag("trail")}
	if err := p.err(); err != nil {
		return nil, err
	}
	if err := sec.Validate(); err != nil {
		return nil, err
	}
	res, err := st.Sweep(ctx, sec)
	if err != nil {
		return nil, err
	}
	if res.Now == 0 {
		res.Now = m.now().UnixMilli()
	}
	return res, nil
}

func (m *Module) contacts(ctx context.Context, st core.Store, q url.Values) (any, error) {
	p := m.newParams(q, contactsParams)
	sq := domain.SearchQuery{RS: p.ruleset(), Q: p.text("q"), MapID: p.mapID(), Group: p.group(),
		Limit: int(p.number("limit", maxLimitParam)), Offset: int(p.number("offset", maxOffsetParam))}
	if err := p.err(); err != nil {
		return nil, err
	}
	sq.Limit = domain.ClampLimit(sq.Limit, domain.SearchDefaultLimit, domain.SearchMaxLimit)
	return st.Search(ctx, sq)
}

func (m *Module) trail(ctx context.Context, st core.Store, q url.Values) (any, error) {
	p := m.newParams(q, trailParams)
	rs, guid, mapID := p.ruleset(), p.requireGUID(), p.mapID()
	if err := p.err(); err != nil {
		return nil, err
	}
	return st.Trail(ctx, rs, guid, mapID)
}

func (m *Module) bounty(ctx context.Context, st core.Store, q url.Values) (any, error) {
	p := m.newParams(q, bountyParams)
	rs, limit := p.ruleset(), int(p.number("limit", maxLimitParam))
	if err := p.err(); err != nil {
		return nil, err
	}
	return st.Bounty(ctx, rs, domain.ClampLimit(limit, bountyDefaultLimit, bountyMaxLimit))
}

func (m *Module) bountyPlayer(ctx context.Context, st core.Store, q url.Values) (any, error) {
	p := m.newParams(q, playerParams)
	rs, guid := p.ruleset(), p.requireGUID()
	if err := p.err(); err != nil {
		return nil, err
	}
	return st.BountyPlayer(ctx, rs, guid)
}

func (m *Module) charts(ctx context.Context, st core.Store, q url.Values) (any, error) {
	p := m.newParams(q, chartsParams)
	rs := p.ruleset()
	if err := p.err(); err != nil {
		return nil, err
	}
	return st.Charts(ctx, rs)
}

func (m *Module) status(ctx context.Context, st core.Store, q url.Values) (any, error) {
	if err := newQuery(q, nil, "").err(); err != nil {
		return nil, err
	}
	rulesets, err := st.Status(ctx)
	if err != nil {
		return nil, err
	}
	if rulesets == nil {
		rulesets = []domain.RulesetStatus{}
	}
	out := map[string]any{
		"version":   Version,
		"rulesets":  rulesets,
		"uptimeSec": int64(m.now().Sub(m.started) / time.Second),
	}
	if hs, ok := st.(healthReporter); ok {
		h := hs.Health()
		out["integrity"], out["lastCheck"], out["full"] = h.Integrity, h.LastCheck, h.Full
	}
	return out, nil
}

// healthReporter is implemented by stores that run integrity checks (sqlstore); the status route
// then adds integrity, lastCheck and full. Only the state is shown, never paths or error text.
type healthReporter interface{ Health() domain.Health }
