# Stage 4: radar-db binary (readapi, writeapi, keys)

Read `../../PLAN.md` (API contract, section 4 is binding), `STATUS.md`, and the Results of stages 1 and 3. Work only in
`internal/mod/readapi`, `internal/mod/writeapi`, `internal/mod/keys`, `cmd/radar-db`, `deploy/` is NOT yours.

## Deliverables
1. `keys` module: loads view/ingest keys from config (`--view-keys`, `--ingest-keys`, comma lists or `*_FILE` paths; keys >= 16 chars,
   hashed with SHA-256 in memory, constant-time compare), registers `core.Authenticator`s for `RoleView` / `RoleIngest` reading
   `X-View-Key` / `X-Ingest-Key` (also `Authorization: Bearer`). No keys configured = open + the core's warning. Never logs keys.
   Provide `radar-db keygen` subcommand helper `GenerateKey()` (32 random bytes, base64url).
2. `readapi` module: every GET route in PLAN.md section 4 (`sweep`, `contacts`, `trail`, `bounty`, `bounty/player`, `charts`, `status`)
   wrapped in `Auth.Require(RoleView, ...)`. Strict query parsing into `domain.Sector` / `SearchQuery` (invalid -> 400 `{error,message}`,
   unknown `rs` -> 400, `limit` clamped with `domain.ClampLimit`). `sweep` returns `{now, changed, sector, contacts, trail?}` with
   `now` from the injected clock. `Cache-Control: no-store`. Uses `host.Stores.Default()`; query timeout via `context` (config
   `--query-timeout`, default 5 s).
3. `writeapi` module: `POST /internal/v1/ping` (`RoleIngest`, JSON, body cap, `Ping.Validate()`, `ApplyPing`, `{received, changed, duplicate}`);
   also publishes accepted pings to `host.Sinks` (so other modules can observe). Rejects unknown fields.
4. `cmd/radar-db`: `main.go` (flags, `keygen`, `backup <file>` subcommands hooks, signal handling) and `modules.go` (the ONLY list of
   modules: sqlstore, keys, readapi, writeapi, static). Build tag or flag `--store memstore|sqlite` (sqlite default, `--db-path`).
5. Tests: httptest per route, table-driven parameter validation, auth matrix (no key/wrong/right/role mix-up), end-to-end test
   (real Host, memstore and sqlstore): ping in -> sweep out for player/zone/all scopes with `since` deltas, duplicate ping no-op.
   Removal test: building the host without readapi still serves /healthz and writeapi works (and vice versa).

## Done when
gofmt/vet/test green, >= 85% coverage per package, `GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build ./cmd/radar-db` works,
Results appended here, STATUS.md line added.

## Results (Stage 4 complete)

**Built.** `internal/mod/keys` (keys.go), `internal/mod/readapi` (module.go, params.go, handlers.go), `internal/mod/writeapi`
(writeapi.go), `cmd/radar-db` (main.go, modules.go, backup.go). gofmt/vet clean, `go test -cover` green (also `-count=2`),
`GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build ./cmd/radar-db` OK. Architecture test still green.

| Package | Test functions | Coverage |
|---|---|---|
| keys | 7 | 96.2% |
| readapi | 15 | 95.8% |
| writeapi | 9 | 97.9% |
| cmd/radar-db | 11 (e2e memstore + sqlite, key matrix, removal both ways, keygen, backup, flags) | 87.4% |

**Config keys / flags.** keys: `view-keys`, `view-keys-file`, `ingest-keys`, `ingest-keys-file` (comma/newline lists, `#` comments,
>= 16 chars; env `RADAR_VIEW_KEYS` etc.). readapi: `query-timeout` (5s), `default-ruleset` (pvp), `cors-origin` (empty = off).
radar-db: `--store sqlite|memstore` (default sqlite; flag or `RADAR_STORE` only), plus the host/sqlstore/static keys.
Subcommands: `radar-db keygen`, `radar-db backup [--db-path f] <target>` (also `RADAR_DB_PATH`), `radar-db version`.

**Decisions / deviations**
- Authenticators are registered in a start hook (settings are not loaded at Init), so "no keys" stays open with the core warning.
  Tests that skip `host.Run` must call `host.Hooks.RunStart`. View and ingest keys are separate roles (no cross-use).
- `modules.go` is the only cmd file allowed to import modules (architecture test), so it also holds `generateKey`, `setVersion`,
  `backupDB` and `modules(store, omit...)` (omit is used by the removal tests). The store module is listed first = default store.
- Response shapes follow the site's demo API: `trail`, `bounty`, `charts` return bare arrays, `contacts` returns `{total, contacts}`,
  `bounty/player` a bare row. `scope` defaults to `all`; unknown query params are ignored; `q`/`guid` capped at 64 chars.
- `sweep.now` is the store's value (the store filters with its own clock); the injected clock is used only when the store returns 0
  and for `status.uptimeSec`. `readapi.Version` is set by main.
- Error codes: `invalid_request` 400, `not_found` 404, `too_large` 413, `timeout` 503, `internal` 500 (details only in logs).
- CORS: `cors-origin` adds Allow-Origin on read routes and answers OPTIONS preflight with 204.
- writeapi: strict JSON (unknown fields, trailing data rejected), body cap = host `max-body`; sinks get only non-duplicate pings;
  a sink failure is logged, not returned. `changed` counts charts too (store rule), so a ping with 2 contacts + 1 new chart reports 3.
- static module is listed (it exists and builds).
