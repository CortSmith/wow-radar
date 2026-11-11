# Stage 3: SQLite store (`internal/mod/sqlstore`)

Read `../../PLAN.md` and `stage-1-core.md` (Results: `core.Store` signatures, `storetest`). Work only in `internal/mod/sqlstore`
(plus `go.mod`/`go.sum` for the one dependency). Do not touch other dirs.

## Goal
A durable `core.Store` backed by one SQLite file that passes the exact same behavioural suite as memstore
(`storetest.Run`) and is fast and small enough for a weak Xubuntu laptop.

## Deliverables
1. Dependency: `modernc.org/sqlite` only (pure Go; verify `CGO_ENABLED=0 go build` and `GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build ./...`).
2. Schema + versioned migrations (`PRAGMA user_version`), created idempotently. Tables (all rows carry `rs`): `contacts`
   (PK rs+guid; all Contact fields; indexes on (rs, mapId, reaction, lastSeen), (rs, lastSeen), name-lowercase), `trail_fixes`
   (rs, guid, t, mapId, x, y, kind; index rs+guid+t), `charts`, `batches` (rs, batchId PK, appliedAt; pruned to the newest 20000),
   `deaths` (rs, id PK, ...), `bounty` derived from contact kb/assists (or a view), `meta`. Document the schema in `schema.md`.
3. Pragmas: WAL, `synchronous=NORMAL`, `busy_timeout`, `foreign_keys`, cache sized small (default ~16 MB, configurable); single writer
   connection + small reader pool; `ApplyPing` is one transaction (batch id check -> merge contacts with additive deltas -> trail append
   with dedupe/cap -> charts -> deaths -> bounty).
4. Search: case-insensitive substring over name, raw and pastNames (store a lowercase search column; do not use LIKE on JSON blobs
   without an index plan), `limit/offset`, `grp` and `mapId` filters; sweep implements player/zone/all scopes with `since`, `activeWithin`.
5. Retention knobs: per-contact trail cap 5000 and global fix cap (default 250000) enforced by pruning oldest fixes of least-recently-seen
   contacts; `Backup(path)` via `VACUUM INTO`; `Close` checkpoints WAL.
6. Module `sqlstore` (`New()`), registers itself in the `Stores` socket as `sqlite`, declares config `--db-path`, `--cache-mb`, `--max-fixes`.
7. Extra tests: persistence across reopen, concurrent readers during a write (race-free), corrupted/new-version file gives a clear error,
   migration from an empty file, large ping performance sanity (5000 fixes < 1 s on a laptop), benchmarks for sweep and ApplyPing.

## TDD order
open/migrate -> storetest suite red -> ApplyPing -> Sweep -> Search -> Trail/Bounty/Charts/Status -> pruning -> Backup -> module wiring.

## Done when
gofmt/vet/test green (`go test ./... -cover`, coverage >= 85%), linux cross-compile OK, Results section appended here, STATUS.md line added.
