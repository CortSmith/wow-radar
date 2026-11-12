# sqlstore schema (user_version 2)

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

### Armory tables (migration v2, stage 15, `core.ArmoryStore`; not partitioned by `rs`)
| Table | Key | Purpose |
|---|---|---|
| `armory_characters` | (realm_lc, name_lc), WITHOUT ROWID | latest state per character; `realm`/`name` display case; `gear`/`talents`/`stats`/`pvp` JSON object text ('' = none, each <= 16 KiB); `api_updated_at` nullable; `guid_link` reserved (NULL); `status` ok/hidden/notfound/error |
| `armory_snapshots` | (realm_lc, name_lc, day), WITHOUT ROWID | one row per UTC day from ok fetches (`level`, `gear_hash`, `pvp`); same-day newer fetch replaces; pruned after 30 days |
| `armory_batches` | seq autoinc, UNIQUE(batch_id) | applied batch ids (replay is a no-op); newest 20000 kept |
| `armory_runs` | id, WITHOUT ROWID | crawl run summaries and cursor; newest 400 kept |

Indexes: `armory_realm_class (realm_lc, class)`, `armory_guild (guild)`, `armory_fetched (fetched_at)`, `armory_snapshots_day (day)`,
`armory_runs_started (started_at)`. Rules: an older `fetched_at` is skipped (equal accepted); a non-ok status on a known character
updates only `status` and `fetched_at`; a rejected batch stores nothing and its id is not remembered. All access uses bound parameters; reads use
the reader pool. Armory pruning runs in the same hook as the id tables (`pruneIDsKeep` -> `pruneArmory`); character rows are never pruned.
Seed feed (`ArmorySeeds`, reader pool, bound parameters only): characters are read from `contacts` (guid prefix `Player-`, all rulesets, ordered by `name_lc`; the realm exists only in the `raw` suffix `Name-Realm`, so de-duplication by (realm, name) lowercase and the realm tie-break happen in Go, invalid names skipped) and guilds from `armory_characters` (`guild != `, grouped by `realm_lc, guild`, uses `armory_guild`/full scan). No new tables or migration.

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
