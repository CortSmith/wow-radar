package sqlstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"radarlive/internal/domain"
)

const selectArmoryChar = `SELECT realm, name, class, race, faction, level, guild, health, power_type, power,
	gear, talents, stats, pvp, api_updated_at, fetched_at, status FROM armory_characters WHERE realm_lc = ? AND name_lc = ?`

// blob turns a stored JSON text into a RawMessage; ” means none.
func blob(s string) json.RawMessage {
	if s == "" {
		return nil
	}
	return json.RawMessage(s)
}

func scanArmoryChar(row *sql.Row) (domain.ArmoryCharacter, error) {
	var c domain.ArmoryCharacter
	var gear, talents, stats, pvp string
	var api sql.NullInt64
	err := row.Scan(&c.Realm, &c.Name, &c.Class, &c.Race, &c.Faction, &c.Level, &c.Guild, &c.Health, &c.PowerType, &c.Power,
		&gear, &talents, &stats, &pvp, &api, &c.FetchedAt, &c.Status)
	if err != nil {
		return domain.ArmoryCharacter{}, err
	}
	c.Gear, c.Talents, c.Stats, c.PvP = blob(gear), blob(talents), blob(stats), blob(pvp)
	if api.Valid {
		c.APIUpdatedAt = &api.Int64
	}
	return c, nil
}

// GetArmoryCharacter implements core.ArmoryStore; realm and name match case-insensitively.
func (s *Store) GetArmoryCharacter(ctx context.Context, realm, name string) (domain.ArmoryCharacter, error) {
	if err := ctx.Err(); err != nil {
		return domain.ArmoryCharacter{}, fmt.Errorf("sqlstore: %w", err)
	}
	c, err := scanArmoryChar(s.readers.QueryRowContext(ctx, selectArmoryChar, domain.ArmoryRealmKey(realm), domain.ArmoryNameKey(name)))
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return domain.ArmoryCharacter{}, fmt.Errorf("%w: armory character", domain.ErrNotFound)
	case err != nil:
		return domain.ArmoryCharacter{}, fmt.Errorf("sqlstore: armory character: %w", err)
	}
	return c, nil
}

// ListArmoryCharacters implements core.ArmoryStore: known characters in request order.
func (s *Store) ListArmoryCharacters(ctx context.Context, realm string, names []string) ([]domain.ArmoryCharacter, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("sqlstore: %w", err)
	}
	if len(names) > domain.ArmoryBatchMax {
		return nil, fmt.Errorf("%w: more than %d names", domain.ErrInvalid, domain.ArmoryBatchMax)
	}
	out := make([]domain.ArmoryCharacter, 0, len(names))
	realmKey := domain.ArmoryRealmKey(realm)
	for _, n := range names {
		c, err := scanArmoryChar(s.readers.QueryRowContext(ctx, selectArmoryChar, realmKey, domain.ArmoryNameKey(n)))
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("sqlstore: armory character: %w", err)
		}
		out = append(out, c)
	}
	return out, nil
}

// ArmorySnapshots implements core.ArmoryStore, oldest day first; an unknown character has none.
func (s *Store) ArmorySnapshots(ctx context.Context, realm, name string) ([]domain.ArmorySnapshot, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("sqlstore: %w", err)
	}
	rows, err := s.readers.QueryContext(ctx, `SELECT day, level, gear_hash, pvp FROM armory_snapshots
		WHERE realm_lc = ? AND name_lc = ? ORDER BY day ASC`, domain.ArmoryRealmKey(realm), domain.ArmoryNameKey(name))
	if err != nil {
		return nil, fmt.Errorf("sqlstore: armory snapshots: %w", err)
	}
	defer rows.Close()
	var out []domain.ArmorySnapshot
	for rows.Next() {
		var sn domain.ArmorySnapshot
		var pvp string
		if err := rows.Scan(&sn.Day, &sn.Level, &sn.GearHash, &pvp); err != nil {
			return nil, fmt.Errorf("sqlstore: armory snapshots: %w", err)
		}
		sn.PvP = blob(pvp)
		out = append(out, sn)
	}
	return out, rows.Err()
}

// LastArmoryRun implements core.ArmoryStore: the run with the latest StartedAt.
func (s *Store) LastArmoryRun(ctx context.Context) (domain.ArmoryRun, error) {
	if err := ctx.Err(); err != nil {
		return domain.ArmoryRun{}, fmt.Errorf("sqlstore: %w", err)
	}
	var r domain.ArmoryRun
	err := s.readers.QueryRowContext(ctx, `SELECT id, started_at, finished_at, enumerated, fetched, stored, skipped, failed, status, cursor
		FROM armory_runs ORDER BY started_at DESC, id DESC LIMIT 1`).
		Scan(&r.ID, &r.StartedAt, &r.FinishedAt, &r.Enumerated, &r.Fetched, &r.Stored, &r.Skipped, &r.Failed, &r.Status, &r.Cursor)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return domain.ArmoryRun{}, fmt.Errorf("%w: armory run", domain.ErrNotFound)
	case err != nil:
		return domain.ArmoryRun{}, fmt.Errorf("sqlstore: armory run: %w", err)
	}
	return r, nil
}
