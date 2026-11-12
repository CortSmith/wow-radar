# sqlstore schema (user_version 1)

One SQLite file, WAL, `synchronous=NORMAL`, `busy_timeout=5000`, `foreign_keys=ON`, `cache_size` = `--cache-mb` per connection.
One writer connection, 4 read-only connections (`query_only`). Every table carries `rs` (ruleset) and is a separate partition.
Migrations live in `migrate.go` (`migrations[i]` takes version i to i+1, one transaction each). A file with a newer
`user_version` is refused; a non-SQLite file fails `Open` with a wrapped error.

| Table | Key | Purpose |
|---|---|---|
| `contacts` | (rs, guid), WITHOUT ROWID | all Contact fields; `past_names` JSON array; `name_lc` lowercase name; `search` = lowercase `name US raw US pastNames` (US = 0x1f) |
| `trail_fixes` | (rs, guid, t), WITHOUT ROWID | fixes; `kind` 0 exact, 1 approx; per-contact cap 5000, global cap `--max-fixes` |
| `charts` | (rs, map_id) | calibration rectangles |
| `batches` | seq autoinc, UNIQUE(rs, batch_id) | applied batch ids (idempotency); newest 20000 per ruleset kept |
| `deaths` | seq autoinc, UNIQUE(rs, id) | deduplicated death ledger; newest 50000 per ruleset kept |
| `meta` | key | `last_write:<rs>` epoch ms, moved only when a ping changed something |

Indexes on `contacts`: `(rs, map_id, reaction, last_seen)` zone sweeps; `(rs, last_seen)` all sweeps and ordering;
`(rs, name_lc)`; partial `(rs, kb DESC, assists DESC) WHERE kb>0 OR assists>0` for the bounty. There is no separate
bounty table: the ranking is derived from `kb`/`assists` with `domain.RankBounty`.

Search uses `instr(search, ?)` on the lowercased term, so `%` and `_` are literal and no LIKE escaping is needed.
Pruning: per-contact cap in the ping transaction; global fix cap prunes the least recently seen contacts' oldest
fixes down to 90% of the cap; id tables are pruned at open and every 1000 inserts.

## Safety (task 8.8)
- **Start:** `preflight` opens an existing file read-only (immutable when no WAL is pending, so nothing is created), reads `user_version`
  and runs `quick_check`. Not SQLite (`ErrNotDatabase`), newer `user_version` (`ErrNewerSchema`) or failed check (`ErrCorrupt`) refuse the
  start with restore instructions; the file is never moved, rewritten or deleted. `sqlstore.Verify(path)` is the same check for `radar-db backup/restore`.
- **Runtime:** `--db-integrity-every` (`RADAR_DB_INTEGRITY_EVERY`, 24h) runs `integrity_check` on a reader connection. A failure sets the store
  degraded (sticky until restart): `ApplyPing` returns `domain.ErrDegraded` (writeapi 503), reads continue. `Store.Health()` feeds `/api/v1/status`.
- **Space:** before each write transaction (probes throttled to 1/s on the store clock) writes pause with `domain.ErrStorageFull` (writeapi 507) when
  free disk space of the data dir < `--db-min-free-mb` (200) or the size > `--db-max-mb` (2048). Size = live pages (`page_count - freelist_count`) times
  page size, and the WAL file; freed pages are reused, so pruning lowers it. Recovery is automatic on the next probe.
- **Pruning order** when the size cap is hit (once per transition into full): 1. trail fixes: half of all fixes, whole trails of the least recently seen
  contacts first, oldest fixes first within a trail (the same routine as the `--max-fixes` cap); 2. only if still over, the older half of the batch ids
  and death ids per ruleset (this shortens the replay-protection window). Contacts, charts and counters are never pruned.
- **Permissions:** the data dir (if missing, one level) is created 0700 and an empty db file 0600 before SQLite opens, so `-wal`/`-shm` inherit it; wider
  existing modes only log a WARN (`permProblem`, skipped on Windows).
- **Lock:** `AcquireLock(db)` holds `<db>.lock` (flock / exclusive open) while the sqlstore module serves; `radar-db restore` needs it. `backup` does not.
- **Read path:** the 4 reader connections are `query_only`; read methods use `s.readers` only (source-guard test).
