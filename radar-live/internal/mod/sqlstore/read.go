package sqlstore

import (
	"context"
	"database/sql"
	"fmt"

	"radarlive/internal/domain"
)

const orderNewestFirst = ` ORDER BY last_seen DESC, guid ASC`

// Sweep returns the contacts of a sector, newest first. A sweep with Since == 0 is a full load and
// always reports Changed; later sweeps report Changed only when rows are returned.
func (s *Store) Sweep(ctx context.Context, sector domain.Sector) (domain.SweepResult, error) {
	if err := prepare(ctx, sector.RS); err != nil {
		return domain.SweepResult{}, err
	}
	if err := sector.Validate(); err != nil {
		return domain.SweepResult{}, err
	}
	now := s.now()
	result := domain.SweepResult{Now: now.UnixMilli(), Sector: sector, Contacts: []domain.Contact{}}
	f := newFilter(sector.RS)
	f.add("last_seen > ?", sector.Since)
	f.group(sector.Group)
	f.text(sector.Query)
	switch sector.Scope {
	case domain.ScopePlayer:
		f.add("guid = ?", sector.GUID)
	case domain.ScopeZone:
		f.onMap(sector.MapID)
	}
	if sector.Scope != domain.ScopePlayer {
		f.add("last_seen >= ?", now.UnixMilli()-sector.ActiveWindow().Milliseconds())
	}
	contacts, err := s.queryContacts(ctx, f, orderNewestFirst, 0, 0)
	if err != nil {
		return domain.SweepResult{}, err
	}
	for _, c := range contacts {
		if sector.Includes(c, now) { // the domain rule has the final say
			result.Contacts = append(result.Contacts, c)
		}
	}
	if sector.Scope == domain.ScopePlayer && sector.Trail {
		if result.Trail, err = s.fixes(ctx, sector.RS, sector.GUID, nil, sector.Since); err != nil {
			return domain.SweepResult{}, err
		}
	}
	result.Changed = sector.Since == 0 || len(result.Contacts) > 0
	return result, nil
}

// queryContacts runs the filter; limit 0 means no limit.
func (s *Store) queryContacts(ctx context.Context, f *filter, order string, limit, offset int) ([]domain.Contact, error) {
	if f.never {
		return []domain.Contact{}, nil
	}
	q, args := selectContacts+` WHERE `+f.where()+order, f.args
	if limit > 0 {
		q += ` LIMIT ? OFFSET ?`
		args = append(append([]any(nil), args...), limit, offset)
	}
	rows, err := s.readers.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("sqlstore: query contacts: %w", err)
	}
	defer rows.Close()
	contacts := []domain.Contact{}
	for rows.Next() {
		c, err := scanContact(rows)
		if err != nil {
			return nil, fmt.Errorf("sqlstore: read contact: %w", err)
		}
		contacts = append(contacts, c)
	}
	return contacts, rows.Err()
}

// Search finds contacts matching the query, newest first, with paging.
func (s *Store) Search(ctx context.Context, query domain.SearchQuery) (domain.SearchResult, error) {
	if err := prepare(ctx, query.RS); err != nil {
		return domain.SearchResult{}, err
	}
	if err := query.Validate(); err != nil {
		return domain.SearchResult{}, err
	}
	f := newFilter(query.RS)
	f.onMap(query.MapID)
	f.group(query.Group)
	f.text(query.Q)
	result := domain.SearchResult{Contacts: []domain.Contact{}}
	if f.never {
		return result, nil
	}
	err := s.readers.QueryRowContext(ctx, `SELECT count(*) FROM contacts WHERE `+f.where(), f.args...).Scan(&result.Total)
	if err != nil {
		return domain.SearchResult{}, fmt.Errorf("sqlstore: count contacts: %w", err)
	}
	limit := domain.ClampLimit(query.Limit, domain.SearchDefaultLimit, domain.SearchMaxLimit)
	result.Contacts, err = s.queryContacts(ctx, f, orderNewestFirst, limit, query.Offset)
	return result, err
}

// Trail returns one contact's fixes, optionally restricted to a chart.
func (s *Store) Trail(ctx context.Context, rs domain.Ruleset, guid string, mapID *int) ([]domain.Fix, error) {
	if err := prepare(ctx, rs); err != nil {
		return nil, err
	}
	return s.fixes(ctx, rs, guid, mapID, 0)
}

// fixes lists a contact's fixes with t > since, oldest first, optionally on one chart (never nil).
func (s *Store) fixes(ctx context.Context, rs domain.Ruleset, guid string, mapID *int, since int64) ([]domain.Fix, error) {
	q, args := `SELECT t, map_id, x, y, kind FROM trail_fixes WHERE rs = ? AND guid = ? AND t > ?`,
		[]any{string(rs), guid, since}
	if mapID != nil {
		q, args = q+` AND map_id = ?`, append(args, *mapID)
	}
	rows, err := s.readers.QueryContext(ctx, q+` ORDER BY t ASC`, args...)
	if err != nil {
		return nil, fmt.Errorf("sqlstore: query trail: %w", err)
	}
	defer rows.Close()
	out := []domain.Fix{}
	for rows.Next() {
		f, err := scanFix(rows)
		if err != nil {
			return nil, fmt.Errorf("sqlstore: read fix: %w", err)
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// Charts lists the charts of a ruleset ordered by map id.
func (s *Store) Charts(ctx context.Context, rs domain.Ruleset) ([]domain.Chart, error) {
	if err := prepare(ctx, rs); err != nil {
		return nil, err
	}
	rows, err := s.readers.QueryContext(ctx,
		`SELECT map_id, name, x0, x1, y0, y1 FROM charts WHERE rs = ? ORDER BY map_id`, string(rs))
	if err != nil {
		return nil, fmt.Errorf("sqlstore: query charts: %w", err)
	}
	defer rows.Close()
	charts := []domain.Chart{}
	for rows.Next() {
		var c domain.Chart
		if err := rows.Scan(&c.MapID, &c.Name, &c.X0, &c.X1, &c.Y0, &c.Y1); err != nil {
			return nil, fmt.Errorf("sqlstore: read chart: %w", err)
		}
		charts = append(charts, c)
	}
	return charts, rows.Err()
}

// Status summarises every ruleset that holds data, ordered by ruleset id.
func (s *Store) Status(ctx context.Context) ([]domain.RulesetStatus, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("sqlstore: %w", err)
	}
	status := []domain.RulesetStatus{}
	for _, rs := range domain.AllRulesets() {
		st, err := s.rulesetStatus(ctx, rs)
		if err != nil {
			return nil, err
		}
		if st.Contacts > 0 || st.Deaths > 0 || st.hasCharts {
			status = append(status, st.RulesetStatus)
		}
	}
	return status, nil
}

type statusRow struct {
	domain.RulesetStatus
	hasCharts bool
}

func (s *Store) rulesetStatus(ctx context.Context, rs domain.Ruleset) (statusRow, error) {
	st := statusRow{RulesetStatus: domain.RulesetStatus{ID: rs}}
	var lastWrite sql.NullInt64
	err := s.readers.QueryRowContext(ctx, `SELECT
		(SELECT count(*) FROM contacts WHERE rs = ?1),
		(SELECT count(*) FROM deaths WHERE rs = ?1),
		EXISTS (SELECT 1 FROM charts WHERE rs = ?1),
		(SELECT CAST(value AS INTEGER) FROM meta WHERE key = ?2)`,
		string(rs), lastWriteKey(rs)).Scan(&st.Contacts, &st.Deaths, &st.hasCharts, &lastWrite)
	if err != nil {
		return st, fmt.Errorf("sqlstore: status of %s: %w", rs, err)
	}
	st.LastWrite = lastWrite.Int64
	return st, nil
}
