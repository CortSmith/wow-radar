package sqlstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"radarlive/internal/domain"
)

// ApplyArmoryBatch implements core.ArmoryStore. The whole batch is validated before the transaction
// opens, so a rejected batch stores nothing and its id is not remembered. The rules match memstore:
// an older FetchedAt is skipped, a non-ok status on a known character updates only status and
// fetched_at, and only ok fetches write the one-per-UTC-day snapshot.
func (s *Store) ApplyArmoryBatch(ctx context.Context, batch domain.ArmoryBatch) (domain.ArmoryApplyResult, error) {
	if err := ctx.Err(); err != nil {
		return domain.ArmoryApplyResult{}, fmt.Errorf("sqlstore: %w", err)
	}
	if err := batch.Validate(); err != nil {
		return domain.ArmoryApplyResult{}, err
	}
	if err := s.writeGate(ctx); err != nil {
		return domain.ArmoryApplyResult{}, err
	}
	tx, err := s.writer.BeginTx(ctx, nil)
	if err != nil {
		return domain.ArmoryApplyResult{}, fmt.Errorf("sqlstore: begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	res, err := s.applyArmoryTx(ctx, tx, batch)
	if err != nil {
		return domain.ArmoryApplyResult{}, fmt.Errorf("sqlstore: apply armory %s: %w", batch.BatchID, err)
	}
	if err := tx.Commit(); err != nil {
		return domain.ArmoryApplyResult{}, fmt.Errorf("sqlstore: commit: %w", err)
	}
	if !res.Duplicate {
		s.afterCommit(ctx, len(batch.Characters)+1)
	}
	return res, nil
}

func (s *Store) applyArmoryTx(ctx context.Context, tx *sql.Tx, batch domain.ArmoryBatch) (domain.ArmoryApplyResult, error) {
	var res domain.ArmoryApplyResult
	ins, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO armory_batches (batch_id, applied_at) VALUES (?, ?)`,
		batch.BatchID, s.nowMs())
	if err != nil {
		return res, err
	}
	if n, err := ins.RowsAffected(); err != nil || n == 0 {
		res.Duplicate = err == nil
		return res, err
	}
	for _, c := range batch.Characters {
		stored, err := upsertArmory(ctx, tx, c)
		if err != nil {
			return res, err
		}
		if stored {
			res.Stored++
		} else {
			res.Skipped++
		}
	}
	return res, nil
}

// upsertArmory applies one character and reports whether it was stored (false: an older fetch).
func upsertArmory(ctx context.Context, tx *sql.Tx, c domain.ArmoryCharacter) (bool, error) {
	realmKey, nameKey := domain.ArmoryRealmKey(c.Realm), domain.ArmoryNameKey(c.Name)
	var have int64
	err := tx.QueryRowContext(ctx, `SELECT fetched_at FROM armory_characters WHERE realm_lc = ? AND name_lc = ?`,
		realmKey, nameKey).Scan(&have)
	switch {
	case errors.Is(err, sql.ErrNoRows):
	case err != nil:
		return false, err
	case c.FetchedAt < have:
		return false, nil
	case c.Status != domain.ArmoryStatusOK:
		_, err := tx.ExecContext(ctx, `UPDATE armory_characters SET status = ?, fetched_at = ? WHERE realm_lc = ? AND name_lc = ?`,
			c.Status, c.FetchedAt, realmKey, nameKey)
		return err == nil, err
	}
	var apiUpdated any
	if c.APIUpdatedAt != nil {
		apiUpdated = *c.APIUpdatedAt
	}
	if _, err := tx.ExecContext(ctx, `INSERT OR REPLACE INTO armory_characters
		(realm_lc, name_lc, realm, name, guid_link, class, race, faction, level, guild, health, power_type, power,
		 gear, talents, stats, pvp, api_updated_at, fetched_at, status)
		VALUES (?, ?, ?, ?, NULL, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		realmKey, nameKey, c.Realm, c.Name, c.Class, c.Race, c.Faction, c.Level, c.Guild, c.Health, c.PowerType, c.Power,
		string(c.Gear), string(c.Talents), string(c.Stats), string(c.PvP), apiUpdated, c.FetchedAt, c.Status); err != nil {
		return false, err
	}
	if c.Status == domain.ArmoryStatusOK {
		day := domain.ArmoryDay(c.FetchedAt)
		if _, err := tx.ExecContext(ctx, `INSERT OR REPLACE INTO armory_snapshots (realm_lc, name_lc, day, level, gear_hash, pvp)
			VALUES (?, ?, ?, ?, ?, ?)`, realmKey, nameKey, day, c.Level, domain.GearHash(c.Gear), string(c.PvP)); err != nil {
			return false, err
		}
	}
	return true, nil
}

// PruneArmorySnapshots implements core.ArmoryStore. It is not gated by the write pause: deleting
// is how space comes back.
func (s *Store) PruneArmorySnapshots(ctx context.Context, olderThanDay int64) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, fmt.Errorf("sqlstore: %w", err)
	}
	res, err := s.writer.ExecContext(ctx, `DELETE FROM armory_snapshots WHERE day < ?`, olderThanDay)
	if err != nil {
		return 0, fmt.Errorf("sqlstore: prune armory snapshots: %w", err)
	}
	n, err := res.RowsAffected()
	return int(n), err
}

// RecordArmoryRun implements core.ArmoryStore.
func (s *Store) RecordArmoryRun(ctx context.Context, run domain.ArmoryRun) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("sqlstore: %w", err)
	}
	if err := run.Validate(); err != nil {
		return err
	}
	if err := s.writeGate(ctx); err != nil {
		return err
	}
	if _, err := s.writer.ExecContext(ctx, `INSERT OR REPLACE INTO armory_runs
		(id, started_at, finished_at, enumerated, fetched, stored, skipped, failed, status, cursor)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		run.ID, run.StartedAt, run.FinishedAt, run.Enumerated, run.Fetched, run.Stored, run.Skipped, run.Failed,
		run.Status, run.Cursor); err != nil {
		return fmt.Errorf("sqlstore: record armory run: %w", err)
	}
	return nil
}
