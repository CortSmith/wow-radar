package memstore

import (
	"context"
	"fmt"
	"slices"
	"sort"

	"radarlive/internal/domain"
)

// Sweep returns the contacts of a sector, newest first. A sweep with Since == 0 is a full load and
// always reports Changed; later sweeps report Changed only when rows are returned.
func (s *Store) Sweep(ctx context.Context, sector domain.Sector) (domain.SweepResult, error) {
	if err := prepare(ctx, sector.RS); err != nil {
		return domain.SweepResult{}, err
	}
	if err := sector.Validate(); err != nil {
		return domain.SweepResult{}, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	now := s.now()
	result := domain.SweepResult{Now: now.UnixMilli(), Sector: sector, Contacts: []domain.Contact{}}
	part := s.partitionFor(sector.RS, false)
	if part != nil {
		for _, rec := range part.contacts {
			if sector.Includes(rec.contact, now) {
				result.Contacts = append(result.Contacts, rec.contact.Clone())
			}
		}
		domain.SortNewestFirst(result.Contacts)
		if sector.Scope == domain.ScopePlayer && sector.Trail {
			result.Trail = part.trailAfter(sector.GUID, sector.Since)
		}
	}
	result.Changed = sector.Since == 0 || len(result.Contacts) > 0
	return result, nil
}

// trailAfter returns a copy of a contact's fixes newer than since (never nil).
func (p *partition) trailAfter(guid string, since int64) []domain.Fix {
	rec := p.contacts[guid]
	if rec == nil {
		return []domain.Fix{}
	}
	return nonNil(domain.FixesAfter(rec.trail, since))
}

// Search finds contacts matching the query, newest first, with paging.
func (s *Store) Search(ctx context.Context, query domain.SearchQuery) (domain.SearchResult, error) {
	if err := prepare(ctx, query.RS); err != nil {
		return domain.SearchResult{}, err
	}
	if err := query.Validate(); err != nil {
		return domain.SearchResult{}, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	var hits []domain.Contact
	if part := s.partitionFor(query.RS, false); part != nil {
		for _, rec := range part.contacts {
			if query.Includes(rec.contact) {
				hits = append(hits, rec.contact)
			}
		}
	}
	domain.SortNewestFirst(hits)
	return pageOf(hits, query), nil
}

// pageOf cuts one page out of hits and clones its contacts.
func pageOf(hits []domain.Contact, query domain.SearchQuery) domain.SearchResult {
	limit := domain.ClampLimit(query.Limit, domain.SearchDefaultLimit, domain.SearchMaxLimit)
	start := min(query.Offset, len(hits))
	end := min(start+limit, len(hits))
	page := make([]domain.Contact, 0, end-start)
	for _, c := range hits[start:end] {
		page = append(page, c.Clone())
	}
	return domain.SearchResult{Total: len(hits), Contacts: page}
}

// Trail returns one contact's fixes, optionally restricted to a chart.
func (s *Store) Trail(ctx context.Context, rs domain.Ruleset, guid string, mapID *int) ([]domain.Fix, error) {
	if err := prepare(ctx, rs); err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	part := s.partitionFor(rs, false)
	if part == nil || part.contacts[guid] == nil {
		return []domain.Fix{}, nil
	}
	return nonNil(domain.FixesForMap(part.contacts[guid].trail, mapID)), nil
}

// Bounty returns the ranking of a ruleset, at most limit rows.
func (s *Store) Bounty(ctx context.Context, rs domain.Ruleset, limit int) ([]domain.BountyRow, error) {
	rows, err := s.ranking(ctx, rs)
	if err != nil {
		return nil, err
	}
	limit = domain.ClampLimit(limit, domain.BountyDefaultLimit, domain.BountyMaxLimit)
	return rows[:min(limit, len(rows))], nil
}

// BountyPlayer returns one contact's ranking row, or domain.ErrNotFound.
func (s *Store) BountyPlayer(ctx context.Context, rs domain.Ruleset, guid string) (domain.BountyRow, error) {
	rows, err := s.ranking(ctx, rs)
	if err != nil {
		return domain.BountyRow{}, err
	}
	for _, row := range rows {
		if row.GUID == guid {
			return row, nil
		}
	}
	return domain.BountyRow{}, fmt.Errorf("bounty row for %q: %w", guid, domain.ErrNotFound)
}

// ranking ranks every contact of a ruleset.
func (s *Store) ranking(ctx context.Context, rs domain.Ruleset) ([]domain.BountyRow, error) {
	if err := prepare(ctx, rs); err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	var contacts []domain.Contact
	if part := s.partitionFor(rs, false); part != nil {
		for _, rec := range part.contacts {
			contacts = append(contacts, rec.contact)
		}
	}
	return domain.RankBounty(contacts), nil
}

// Charts lists the charts of a ruleset ordered by map id.
func (s *Store) Charts(ctx context.Context, rs domain.Ruleset) ([]domain.Chart, error) {
	if err := prepare(ctx, rs); err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	charts := []domain.Chart{}
	if part := s.partitionFor(rs, false); part != nil {
		for _, c := range part.charts {
			charts = append(charts, c)
		}
	}
	slices.SortFunc(charts, func(a, b domain.Chart) int { return a.MapID - b.MapID })
	return charts, nil
}

// Status summarises every ruleset that holds data, ordered by ruleset id.
func (s *Store) Status(ctx context.Context) ([]domain.RulesetStatus, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("memstore: %w", err)
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	status := []domain.RulesetStatus{}
	for rs, part := range s.partitions {
		if part.hasData() {
			status = append(status, domain.RulesetStatus{
				ID: rs, Contacts: len(part.contacts), Deaths: part.deaths.len(), LastWrite: part.lastWrite,
			})
		}
	}
	sort.Slice(status, func(i, j int) bool { return status[i].ID < status[j].ID })
	return status, nil
}

// nonNil turns a nil slice into an empty one so JSON encodes [] rather than null.
func nonNil(fixes []domain.Fix) []domain.Fix {
	if fixes == nil {
		return []domain.Fix{}
	}
	return fixes
}
