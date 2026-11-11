# Stage 1: core host, sockets, domain types, memstore

Read `../../PLAN.md` first (architecture, contract, quality bar). Work only inside `radar-live/`.

## Goal
The empty, tested shell everything else plugs into, plus the shared plain types and an in-memory store.

## Deliverables
1. `go.mod` (`module radarlive`, go 1.27). No third-party dependencies in this stage.
2. `internal/domain`: `Ruleset` (+ `ParseRuleset`, `DefaultRuleset`), `Reaction`, `Contact`, `Deltas`, `Fix`, `Chart`, `Death`,
   `Ping`, `Sector` (scope player|zone|all, guid, mapId, group, query, since, activeWithin), `BountyRow`. JSON tags per PLAN.md
   section 3/4. Pure data + small validators (`Ping.Validate()` with size caps: <=500 contacts, <=5000 fixes/contact, <=200 charts).
3. `internal/core`:
   - `Module` interface (+ optional `Starter`, `Stopper`).
   - `Host` with sockets: `Routes` (`HandleFunc(method, pattern, handler)` on a `http.ServeMux`, method-aware, duplicate pattern -> error),
     `Parsers`, `Stores` (`Store` interface lives here, see below), `Queries`, `Sinks`, `Hooks`, `Config` (module declares flags/env keys
     with defaults and types: string/int/duration/bool; resolution order flag > env > env-file > default), `Auth` (chain of authenticators
     keyed by role `view|ingest`; none registered => allowed + one warning).
   - `Register(modules ...Module)`: duplicate names rejected; `Init` in order; `Run(ctx)` starts HTTP server + `Starter`s, graceful shutdown
     calling `Stopper`s in reverse order; a module failing `Init` aborts with its name in the error.
   - A tiny typed event bus (`Publish/Subscribe` by topic string, synchronous, panics in subscribers recovered and logged).
   - Request middleware: body size cap, panic recovery, request id, access log line (no query strings that could hold keys).
   - `Store` interface (consumed by readapi/writeapi, implemented by memstore and sqlstore): `ApplyPing(ctx, domain.Ping) (domain.ApplyResult, error)`,
     `Sweep(ctx, domain.Sector) (domain.SweepResult, error)`, `Search(ctx, SearchQuery)`, `Trail(ctx, rs, guid, mapID)`, `Bounty(ctx, rs, limit)`,
     `BountyPlayer`, `Charts`, `Status`, `Close`. Define the argument/result structs in `domain`.
4. `internal/mod/memstore`: complete in-memory `Store` implementing every rule in PLAN.md section 3 (additive deltas by batchId, merge
   rules, trail dedupe/cap/rounding, grp/zone/player/since filters, bounty ranking, active window with an injectable clock). It is the
   **reference behaviour**; stage 3 must pass the same tests. Put the behavioural tests in a reusable function
   `storetest.Run(t, newStore func(clock) core.Store)` in `internal/mod/storetest` so sqlstore reuses them.
5. `internal/core/architecture_test.go`: parses imports of every package under `internal/mod/*` and fails if one imports another
   module package (storetest excepted for test files), or if `core`/`domain` import any `mod`. Also a "removal" test: a fake host built with
   only the core and zero modules starts, serves `/healthz`, and shuts down.
6. `docs/plans/stage-1-core.md` updated with results; add `docs/plans/STATUS.md` (one line per stage).

## TDD order
domain validators -> core config resolution -> core host/registration/lifecycle -> middleware -> bus -> storetest suite (red against memstore)
-> memstore -> architecture test.

## Done when
`gofmt -l .` empty; `go vet ./...` clean; `go test ./... -cover` green, >= 85% on core/domain/memstore; PLAN.md quality bar met.
