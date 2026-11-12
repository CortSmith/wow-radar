package sqlstore

import (
	"context"
	"fmt"

	"radarlive/internal/domain"
)

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

// ranking ranks every contact that has a killing blow or an assist, using domain.RankBounty; the
// partial index contacts_bounty keeps this to the few rows that score.
func (s *Store) ranking(ctx context.Context, rs domain.Ruleset) ([]domain.BountyRow, error) {
	if err := prepare(ctx, rs); err != nil {
		return nil, err
	}
	f := newFilter(rs)
	f.add("(kb > 0 OR assists > 0)")
	contacts, err := s.queryContacts(ctx, f, ``, 0, 0)
	if err != nil {
		return nil, err
	}
	return domain.RankBounty(contacts), nil
}
