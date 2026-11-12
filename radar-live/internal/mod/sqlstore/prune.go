package sqlstore

import (
	"context"
	"database/sql"
	"fmt"

	"radarlive/internal/domain"
)

// afterCommit runs housekeeping that must not fail the ping that triggered it: errors are dropped
// because the next ping retries the same pruning.
func (s *Store) afterCommit(ctx context.Context, inserted int) {
	if s.fixCount.Load() > s.maxFixes {
		_ = s.pruneFixes(ctx)
	}
	if s.inserts.Add(int64(inserted)) >= pruneEveryInserts {
		s.inserts.Store(0)
		_ = s.pruneIDs(ctx)
	}
}

// pruneIDs keeps only the newest batch and death ids of every ruleset.
func (s *Store) pruneIDs(ctx context.Context) error { return s.pruneIDsKeep(ctx, false) }

// pruneIDsKeep prunes the id tables; halve keeps only the newer half of what each ruleset holds (size-cap reclaim).
func (s *Store) pruneIDsKeep(ctx context.Context, halve bool) error {
	for _, table := range []struct {
		name string
		keep int
	}{{"batches", batchesKeptMax}, {"deaths", deathsKeptMax}} {
		for _, rs := range domain.AllRulesets() {
			keep := table.keep
			if halve {
				var have int
				if err := s.writer.QueryRowContext(ctx, `SELECT count(*) FROM `+table.name+` WHERE rs = ?`, string(rs)).Scan(&have); err != nil {
					return fmt.Errorf("sqlstore: count %s: %w", table.name, err)
				}
				if have < 2 {
					continue
				}
				keep = min(keep, have/2)
			}
			// table.name is one of the two constants above, never user input.
			q := `DELETE FROM ` + table.name + ` WHERE rs = ? AND seq <= (
				SELECT seq FROM ` + table.name + ` WHERE rs = ? ORDER BY seq DESC LIMIT 1 OFFSET ?)`
			if _, err := s.writer.ExecContext(ctx, q, string(rs), string(rs), keep); err != nil {
				return fmt.Errorf("sqlstore: prune %s: %w", table.name, err)
			}
		}
	}
	return nil
}

// trailSize is one contact's stored fix count.
type trailSize struct {
	rs    string
	guid  string
	count int64
}

// pruneFixes deletes fixes, least recently seen contacts first, until the table is back at
// fixPruneTargetPercent of the cap. Whole trails go first; the last victim loses only its oldest fixes.
func (s *Store) pruneFixes(ctx context.Context) error {
	return s.pruneFixesBy(ctx, s.fixCount.Load()-s.maxFixes*fixPruneTargetPercent/100)
}

// pruneFixesBy deletes at least excess fixes (least recently seen contacts first).
func (s *Store) pruneFixesBy(ctx context.Context, excess int64) error {
	tx, err := s.writer.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("sqlstore: prune fixes: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	sizes, err := trailSizesByAge(ctx, tx)
	if err != nil {
		return err
	}
	var removed int64
	for _, sz := range sizes {
		if removed >= excess {
			break
		}
		n, err := deleteOldestFixes(ctx, tx, sz, excess-removed)
		if err != nil {
			return err
		}
		removed += n
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("sqlstore: prune fixes commit: %w", err)
	}
	s.fixCount.Add(-removed)
	return nil
}

func trailSizesByAge(ctx context.Context, tx *sql.Tx) ([]trailSize, error) {
	rows, err := tx.QueryContext(ctx, `SELECT f.rs, f.guid, count(*) FROM trail_fixes f
		JOIN contacts c ON c.rs = f.rs AND c.guid = f.guid
		GROUP BY f.rs, f.guid ORDER BY c.last_seen ASC, f.guid ASC`)
	if err != nil {
		return nil, fmt.Errorf("sqlstore: trail sizes: %w", err)
	}
	defer rows.Close()
	var sizes []trailSize
	for rows.Next() {
		var sz trailSize
		if err := rows.Scan(&sz.rs, &sz.guid, &sz.count); err != nil {
			return nil, fmt.Errorf("sqlstore: trail sizes: %w", err)
		}
		sizes = append(sizes, sz)
	}
	return sizes, rows.Err()
}

// deleteOldestFixes removes up to limit of a contact's oldest fixes and returns how many went.
func deleteOldestFixes(ctx context.Context, tx *sql.Tx, sz trailSize, limit int64) (int64, error) {
	take := min(sz.count, limit)
	res, err := tx.ExecContext(ctx, `DELETE FROM trail_fixes WHERE rs = ? AND guid = ? AND t IN (
		SELECT t FROM trail_fixes WHERE rs = ? AND guid = ? ORDER BY t ASC LIMIT ?)`,
		sz.rs, sz.guid, sz.rs, sz.guid, take)
	if err != nil {
		return 0, fmt.Errorf("sqlstore: prune trail: %w", err)
	}
	return res.RowsAffected()
}
