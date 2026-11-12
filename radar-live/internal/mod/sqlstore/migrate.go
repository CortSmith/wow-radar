package sqlstore

import (
	"context"
	"database/sql"
	"fmt"
)

// migrations[i] upgrades a database from user_version i to i+1. Append only; never edit a shipped step.
var migrations = []string{schemaV1, schemaV2}

// schemaV2 adds the armory tables (stage 15). Keys are case-folded (realm_lc, name_lc); the display
// forms and the JSON blobs (gear, talents, stats, pvp; ” = none) are plain TEXT, size-capped in domain.
const schemaV2 = `
CREATE TABLE armory_characters (
  realm_lc TEXT NOT NULL, name_lc TEXT NOT NULL, realm TEXT NOT NULL, name TEXT NOT NULL,
  guid_link TEXT,
  class TEXT NOT NULL, race TEXT NOT NULL, faction TEXT NOT NULL, level INTEGER NOT NULL, guild TEXT NOT NULL,
  health INTEGER NOT NULL, power_type TEXT NOT NULL, power INTEGER NOT NULL,
  gear TEXT NOT NULL, talents TEXT NOT NULL, stats TEXT NOT NULL, pvp TEXT NOT NULL,
  api_updated_at INTEGER, fetched_at INTEGER NOT NULL, status TEXT NOT NULL,
  PRIMARY KEY (realm_lc, name_lc)
) WITHOUT ROWID;
CREATE INDEX armory_realm_class ON armory_characters (realm_lc, class);
CREATE INDEX armory_guild ON armory_characters (guild);
CREATE INDEX armory_fetched ON armory_characters (fetched_at);
CREATE TABLE armory_snapshots (
  realm_lc TEXT NOT NULL, name_lc TEXT NOT NULL, day INTEGER NOT NULL,
  level INTEGER NOT NULL, gear_hash TEXT NOT NULL, pvp TEXT NOT NULL,
  PRIMARY KEY (realm_lc, name_lc, day)
) WITHOUT ROWID;
CREATE INDEX armory_snapshots_day ON armory_snapshots (day);
CREATE TABLE armory_batches (
  seq INTEGER PRIMARY KEY AUTOINCREMENT, batch_id TEXT NOT NULL UNIQUE, applied_at INTEGER NOT NULL
);
CREATE TABLE armory_runs (
  id TEXT PRIMARY KEY, started_at INTEGER NOT NULL, finished_at INTEGER NOT NULL,
  enumerated INTEGER NOT NULL DEFAULT 0, fetched INTEGER NOT NULL DEFAULT 0, stored INTEGER NOT NULL DEFAULT 0,
  skipped INTEGER NOT NULL DEFAULT 0, failed INTEGER NOT NULL DEFAULT 0,
  status TEXT NOT NULL DEFAULT '', cursor TEXT NOT NULL DEFAULT ''
) WITHOUT ROWID;
CREATE INDEX armory_runs_started ON armory_runs (started_at);
`

const schemaV1 = `
CREATE TABLE contacts (
  rs TEXT NOT NULL, guid TEXT NOT NULL,
  name TEXT NOT NULL, name_lc TEXT NOT NULL, raw TEXT NOT NULL,
  past_names TEXT NOT NULL, search TEXT NOT NULL,
  reaction TEXT NOT NULL, level INTEGER NOT NULL, max_hp REAL NOT NULL,
  x REAL NOT NULL, y REAL NOT NULL, map_id INTEGER NOT NULL, zone TEXT NOT NULL, pos_time INTEGER NOT NULL,
  first_seen INTEGER NOT NULL, last_seen INTEGER NOT NULL,
  events INTEGER NOT NULL, src_events INTEGER NOT NULL,
  dmg_total REAL NOT NULL, dmg_hits INTEGER NOT NULL, dmg_events INTEGER NOT NULL,
  hp_sum REAL NOT NULL, hp_samples INTEGER NOT NULL,
  kb INTEGER NOT NULL, assists INTEGER NOT NULL,
  PRIMARY KEY (rs, guid)
) WITHOUT ROWID;
CREATE INDEX contacts_zone ON contacts (rs, map_id, reaction, last_seen);
CREATE INDEX contacts_seen ON contacts (rs, last_seen);
CREATE INDEX contacts_name ON contacts (rs, name_lc);
CREATE INDEX contacts_bounty ON contacts (rs, kb DESC, assists DESC) WHERE kb > 0 OR assists > 0;
CREATE TABLE trail_fixes (
  rs TEXT NOT NULL, guid TEXT NOT NULL, t INTEGER NOT NULL,
  map_id INTEGER NOT NULL, x REAL NOT NULL, y REAL NOT NULL, kind INTEGER NOT NULL,
  PRIMARY KEY (rs, guid, t)
) WITHOUT ROWID;
CREATE TABLE charts (
  rs TEXT NOT NULL, map_id INTEGER NOT NULL, name TEXT NOT NULL,
  x0 REAL NOT NULL, x1 REAL NOT NULL, y0 REAL NOT NULL, y1 REAL NOT NULL,
  PRIMARY KEY (rs, map_id)
) WITHOUT ROWID;
CREATE TABLE batches (
  seq INTEGER PRIMARY KEY AUTOINCREMENT, rs TEXT NOT NULL, batch_id TEXT NOT NULL, applied_at INTEGER NOT NULL,
  UNIQUE (rs, batch_id)
);
CREATE TABLE deaths (
  seq INTEGER PRIMARY KEY AUTOINCREMENT, rs TEXT NOT NULL, id TEXT NOT NULL,
  t INTEGER NOT NULL, victim TEXT NOT NULL, killer TEXT NOT NULL, assists TEXT NOT NULL,
  UNIQUE (rs, id)
);
CREATE TABLE meta (key TEXT PRIMARY KEY, value TEXT NOT NULL) WITHOUT ROWID;
`

// migrate brings the database to the newest schema, one transaction per step. A database written by
// a newer build is refused rather than damaged.
func migrate(ctx context.Context, db *sql.DB) error {
	var version int
	if err := db.QueryRowContext(ctx, `PRAGMA user_version`).Scan(&version); err != nil {
		return fmt.Errorf("read schema version (is this a SQLite database?): %w", err)
	}
	if version > len(migrations) {
		return fmt.Errorf("database schema version %d is newer than this build supports (%d); upgrade radar-live",
			version, len(migrations))
	}
	for v := version; v < len(migrations); v++ {
		if err := applyMigration(ctx, db, v); err != nil {
			return fmt.Errorf("migrate to version %d: %w", v+1, err)
		}
	}
	return nil
}

func applyMigration(ctx context.Context, db *sql.DB, from int) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, migrations[from]); err != nil {
		return err
	}
	// PRAGMA does not accept bound parameters; the value is an int from our own counter.
	if _, err := tx.ExecContext(ctx, fmt.Sprintf(`PRAGMA user_version = %d`, from+1)); err != nil {
		return err
	}
	return tx.Commit()
}
