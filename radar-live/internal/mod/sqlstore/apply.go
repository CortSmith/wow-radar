package sqlstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"radarlive/internal/domain"
)

// statements are the prepared statements one ApplyPing transaction reuses for every contact.
type statements struct {
	getContact *sql.Stmt
	lastFix    *sql.Stmt
	insertFix  *sql.Stmt
}

// ApplyPing merges one batch into its ruleset in a single transaction. A batch id seen before is a
// no-op reported as Duplicate.
func (s *Store) ApplyPing(ctx context.Context, ping domain.Ping) (domain.ApplyResult, error) {
	if err := prepare(ctx, ping.RS); err != nil {
		return domain.ApplyResult{}, err
	}
	if err := ping.Validate(); err != nil {
		return domain.ApplyResult{}, err
	}
	if err := s.writeGate(ctx); err != nil {
		return domain.ApplyResult{}, err
	}
	tx, err := s.writer.BeginTx(ctx, nil)
	if err != nil {
		return domain.ApplyResult{}, fmt.Errorf("sqlstore: begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	result, fixDelta, err := s.applyTx(ctx, tx, ping)
	if err != nil {
		return domain.ApplyResult{}, fmt.Errorf("sqlstore: apply %s: %w", ping.BatchID, err)
	}
	if err := tx.Commit(); err != nil {
		return domain.ApplyResult{}, fmt.Errorf("sqlstore: commit: %w", err)
	}
	s.fixCount.Add(fixDelta)
	s.lastWrite.Store(s.nowMs())
	if !result.Duplicate {
		s.afterCommit(ctx, len(ping.Deaths)+1)
	}
	return result, nil
}

// applyTx does the work of ApplyPing inside tx and returns the change in stored fixes.
func (s *Store) applyTx(ctx context.Context, tx *sql.Tx, ping domain.Ping) (domain.ApplyResult, int64, error) {
	result := domain.ApplyResult{Received: len(ping.Contacts)}
	fresh, err := s.recordBatch(ctx, tx, ping)
	if err != nil || !fresh {
		result.Duplicate = err == nil
		return result, 0, err
	}
	st, err := prepareStatements(ctx, tx)
	if err != nil {
		return result, 0, err
	}
	defer st.close()
	var fixDelta int64
	for _, in := range ping.Contacts {
		changed, delta, err := applyContact(ctx, tx, st, ping.RS, in)
		if err != nil {
			return result, 0, fmt.Errorf("contact %s: %w", in.GUID, err)
		}
		if changed {
			result.Changed++
		}
		fixDelta += delta
	}
	if err := s.applyExtras(ctx, tx, ping, &result); err != nil {
		return result, 0, err
	}
	return result, fixDelta, nil
}

// applyExtras stores charts and deaths and stamps the ruleset's last write time.
func (s *Store) applyExtras(ctx context.Context, tx *sql.Tx, ping domain.Ping, result *domain.ApplyResult) error {
	charts, err := applyCharts(ctx, tx, ping.RS, ping.Charts)
	if err != nil {
		return err
	}
	result.Changed += charts
	if err := recordDeaths(ctx, tx, ping); err != nil {
		return err
	}
	if result.Changed > 0 {
		return setLastWrite(ctx, tx, ping.RS, s.nowMs())
	}
	return nil
}

// recordBatch stores the batch id; it reports false when the id was already applied.
func (s *Store) recordBatch(ctx context.Context, tx *sql.Tx, ping domain.Ping) (bool, error) {
	res, err := tx.ExecContext(ctx,
		`INSERT OR IGNORE INTO batches (rs, batch_id, applied_at) VALUES (?, ?, ?)`,
		string(ping.RS), ping.BatchID, s.nowMs())
	if err != nil {
		return false, fmt.Errorf("record batch: %w", err)
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

func prepareStatements(ctx context.Context, tx *sql.Tx) (*statements, error) {
	st := &statements{}
	var err1, err2, err3 error
	st.getContact, err1 = tx.PrepareContext(ctx, selectContacts+` WHERE rs = ? AND guid = ?`)
	st.lastFix, err2 = tx.PrepareContext(ctx,
		`SELECT t, map_id, x, y, kind FROM trail_fixes WHERE rs = ? AND guid = ? ORDER BY t DESC LIMIT 1`)
	st.insertFix, err3 = tx.PrepareContext(ctx,
		`INSERT INTO trail_fixes (rs, guid, t, map_id, x, y, kind) VALUES (?, ?, ?, ?, ?, ?, ?)`)
	if err := errors.Join(err1, err2, err3); err != nil {
		st.close()
		return nil, fmt.Errorf("prepare: %w", err)
	}
	return st, nil
}

func (st *statements) close() {
	for _, s := range []*sql.Stmt{st.getContact, st.lastFix, st.insertFix} {
		if s != nil {
			_ = s.Close()
		}
	}
}

// applyContact merges one observation and its fixes; it reports whether anything changed and how
// many fixes were added net of the per-contact cap.
func applyContact(ctx context.Context, tx *sql.Tx, st *statements, rs domain.Ruleset,
	in domain.PingContact) (bool, int64, error) {
	stored, err := loadContact(ctx, st, rs, in.GUID)
	if err != nil {
		return false, 0, err
	}
	merged := domain.MergeContact(stored, in)
	changed := stored == nil || !merged.Equal(*stored)
	if changed {
		if _, err := tx.ExecContext(ctx, upsertContact, upsertArgs(rs, merged)...); err != nil {
			return false, 0, fmt.Errorf("upsert: %w", err)
		}
	}
	added, dropped, err := appendTrail(ctx, tx, st, rs, in)
	return changed || added > 0, int64(added - dropped), err
}

func loadContact(ctx context.Context, st *statements, rs domain.Ruleset, guid string) (*domain.Contact, error) {
	c, err := scanContact(st.getContact.QueryRowContext(ctx, string(rs), guid))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("load contact: %w", err)
	}
	return &c, nil
}

// appendTrail stores the fixes of in that domain.AppendFixes accepts after the last stored fix and
// enforces the per-contact cap. It returns how many fixes were added and how many old ones dropped.
func appendTrail(ctx context.Context, tx *sql.Tx, st *statements, rs domain.Ruleset,
	in domain.PingContact) (added, dropped int, err error) {
	if len(in.Fixes) == 0 {
		return 0, 0, nil
	}
	var base []domain.Fix
	last, err := scanFix(st.lastFix.QueryRowContext(ctx, string(rs), in.GUID))
	switch {
	case err == nil:
		base = []domain.Fix{last}
	case !errors.Is(err, sql.ErrNoRows):
		return 0, 0, fmt.Errorf("last fix: %w", err)
	}
	out := domain.AppendFixes(base, in.Fixes)
	if len(base) == 1 && len(out) > 0 && out[0] == last {
		out = out[1:]
	}
	for _, f := range out {
		_, err := st.insertFix.ExecContext(ctx, string(rs), in.GUID, f.T, f.MapID, f.X, f.Y, kindCode(f.Kind))
		if err != nil {
			return 0, 0, fmt.Errorf("insert fix: %w", err)
		}
	}
	if len(out) == 0 {
		return 0, 0, nil
	}
	dropped, err = capTrail(ctx, tx, rs, in.GUID)
	return len(out), dropped, err
}

// capTrail deletes the oldest fixes of a contact beyond domain.TrailCapFixes and returns how many.
func capTrail(ctx context.Context, tx *sql.Tx, rs domain.Ruleset, guid string) (int, error) {
	res, err := tx.ExecContext(ctx, `DELETE FROM trail_fixes WHERE rs = ? AND guid = ? AND t < (
		SELECT min(t) FROM (SELECT t FROM trail_fixes WHERE rs = ? AND guid = ? ORDER BY t DESC LIMIT ?))`,
		string(rs), guid, string(rs), guid, domain.TrailCapFixes)
	if err != nil {
		return 0, fmt.Errorf("cap trail: %w", err)
	}
	n, err := res.RowsAffected()
	return int(n), err
}

// applyCharts stores new or recalibrated charts and returns how many changed.
func applyCharts(ctx context.Context, tx *sql.Tx, rs domain.Ruleset, charts []domain.Chart) (int, error) {
	changed := 0
	for _, c := range charts {
		res, err := tx.ExecContext(ctx, `INSERT INTO charts (rs, map_id, name, x0, x1, y0, y1) VALUES (?,?,?,?,?,?,?)
			ON CONFLICT(rs, map_id) DO UPDATE SET name=excluded.name, x0=excluded.x0, x1=excluded.x1,
			y0=excluded.y0, y1=excluded.y1
			WHERE name IS NOT excluded.name OR x0 IS NOT excluded.x0 OR x1 IS NOT excluded.x1
			OR y0 IS NOT excluded.y0 OR y1 IS NOT excluded.y1`,
			string(rs), c.MapID, c.Name, c.X0, c.X1, c.Y0, c.Y1)
		if err != nil {
			return 0, fmt.Errorf("chart %d: %w", c.MapID, err)
		}
		n, _ := res.RowsAffected()
		changed += int(n)
	}
	return changed, nil
}

// recordDeaths remembers death ids so the same death is never counted twice.
func recordDeaths(ctx context.Context, tx *sql.Tx, ping domain.Ping) error {
	for _, d := range ping.Deaths {
		_, err := tx.ExecContext(ctx,
			`INSERT OR IGNORE INTO deaths (rs, id, t, victim, killer, assists) VALUES (?,?,?,?,?,?)`,
			string(ping.RS), d.ID, d.T, d.Victim, d.Killer, encodeNames(d.Assists))
		if err != nil {
			return fmt.Errorf("death %s: %w", d.ID, err)
		}
	}
	return nil
}

func setLastWrite(ctx context.Context, tx *sql.Tx, rs domain.Ruleset, ms int64) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO meta (key, value) VALUES (?, ?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value`, lastWriteKey(rs), ms)
	if err != nil {
		return fmt.Errorf("last write: %w", err)
	}
	return nil
}

func lastWriteKey(rs domain.Ruleset) string { return "last_write:" + string(rs) }
