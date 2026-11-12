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

## Results (Stage 1 complete)

**Built.** `go.mod` (module `radarlive`, go 1.27, no dependencies); `internal/domain`, `internal/core`,
`internal/mod/memstore`, `internal/mod/storetest`, `internal/core/architecture_test.go`.
`gofmt -l .` empty, `go vet ./...` clean, `go test ./... -cover` green (run with `-count=3` too).

| Package | Test functions | Coverage |
|---|---|---|
| domain | 39 | 99.1% |
| core | 38 (incl. architecture + removal tests) | 96.0% |
| memstore | 5 + 36-case `storetest.Run` contract suite | 97.6% |
| storetest | exercised through memstore | - |

(`-race` could not be run: it needs cgo, which this Windows setup lacks. A concurrent-writers case is in the suite.)

**Deviations / decisions later stages must know**
- Merge, trail and ranking rules live in `domain` as pure functions (`MergeContact`, `AppendFixes`, `CleanFixes`,
  `RankBounty`, `Sector.Includes`, `SearchQuery.Includes`, `SortNewestFirst`, `ClampLimit`). memstore is a thin shell around
  them; **sqlstore should call the same functions** inside a transaction instead of re-implementing the rules.
  `AppendFixes` may reuse the input slice's storage (callers replace their copy).
- Position known <=> `Contact.PosTime > 0` (`HasPosition()`); there is no separate flag. Map 0 is valid, so
  `Sector.MapID`, `SearchQuery.MapID` and the `Trail` map argument are `*int`.
- `Fix` JSON is an object `{t,mapId,x,y,kind}` (kind `exact|approx`), not a tuple, in pings and responses.
- `PingContact` embeds `Contact` (flattened JSON) plus `d` (`Deltas`) and `fixes`. The counter fields of the embedded
  Contact are ignored on input; only `d` adds. `maxHp`, `level`, `firstSeen`, `lastSeen` come from the contact fields.
- Killing blows and assists are credited through `d.kb` / `d.assists`. `deaths` are a deduplicated ledger (by id, per
  ruleset, bounded to 50 000 ids) and only feed `RulesetStatus.Deaths`; they do not change counters.
- Idempotency is per ruleset by `batchId` (memstore remembers the last 10 000; a replay older than that would re-apply,
  sqlstore should persist ids). A duplicate returns `{Duplicate:true, Changed:0}`. If one guid appears twice in a ping,
  both observations are merged in order (deltas add).
- `Changed` counts contacts whose data or trail changed plus charts added or recalibrated. `RulesetStatus.LastWrite` moves
  only when `Changed > 0`. `Status` lists only rulesets that hold data.
- Active window: zone and all sweeps drop contacts with `now - lastSeen > activeWithin` (default 15 min,
  `domain.DefaultActiveWindow`; `domain.StaleAfter` = 5 min is for the page). **Player scope ignores the window.**
  `Since` filters on `lastSeen > since`. `SweepResult.Changed` is true for `since == 0` (full load) or when rows return.
  `Sector.Trail` (player scope) returns fixes with `t > since`.
- Read calls validate the ruleset (errors wrap `domain.ErrInvalid`; missing bounty row wraps `domain.ErrNotFound`).
  Slice results are never nil (JSON `[]`). Store returns copies.
- Ping size caps: 500 contacts, 200 charts, 1000 deaths, 5000 fixes per contact; `Validate` requires `Player-` guids,
  finite non-negative deltas, batchId `^[\w.:-]{1,64}$`. Contacts with a bad guid reject the whole ping.
- The parser socket passes events as `any` (`core.Parser.Parse(ctx, event any)`); stage 2 must put the shared event type
  in `domain` (e.g. `domain/event.go`) so logparse, contactx and bountyx never import each other.
- Config: flag > env (`RADAR_<KEY>`) > env file (`--env-file` / `RADAR_ENV_FILE`) > default. The host declares `listen`
  (`:8787`), `max-body` (8 MiB) and `shutdown-timeout` (10 s). Call `host.Config.Load(os.Args[1:], os.LookupEnv)` after
  `Register` (modules declare settings in `Init`) and before `Run`; read values lazily (in handlers/`Start`).
- `Host` serves `GET /healthz` itself (outside auth). Auth roles: `core.RoleView`, `core.RoleIngest`; wrap handlers with
  `host.Auth.Require(role, handler)`; no authenticator for a role = open + one warning log. Helpers `core.WriteJSON`,
  `core.WriteError` produce the standard `{error,message}` body. Middleware: request id (`X-Request-Id`), panic recovery,
  body cap, access log without query string.
- `memstore.NewModule()` registers a wall-clock store named `memstore` in `host.Stores` (for dev); the module's `Name()` is `memstore`.
- Architecture test also enforces: only `cmd/**/modules.go` may import `internal/mod/*`, and `domain` imports nothing of ours.

**Exact interfaces**
```go
type Module interface{ Name() string; Init(*Host) error }   // optional Starter{Start(ctx) error}, Stopper{Stop(ctx) error}
type Store interface {
	ApplyPing(ctx, domain.Ping) (domain.ApplyResult, error)
	Sweep(ctx, domain.Sector) (domain.SweepResult, error)
	Search(ctx, domain.SearchQuery) (domain.SearchResult, error)
	Trail(ctx, rs domain.Ruleset, guid string, mapID *int) ([]domain.Fix, error)
	Bounty(ctx, rs domain.Ruleset, limit int) ([]domain.BountyRow, error)
	BountyPlayer(ctx, rs domain.Ruleset, guid string) (domain.BountyRow, error)
	Charts(ctx, rs domain.Ruleset) ([]domain.Chart, error)
	Status(ctx) ([]domain.RulesetStatus, error)
	Close() error
}
```
Host sockets: `Routes.HandleFunc(method, pattern string, http.HandlerFunc) error`; `Parsers.Add(Parser)/Feed(ctx, any)`;
`Stores.Add(name, Store) error/Get/Default/Names/CloseAll`; `Queries.Add(name, QueryFunc) error/Get/Names`;
`Sinks.Add(Sink)/Publish(ctx, domain.Ping) error`; `Hooks.OnStart/OnStop(core.Hook)`; `Config.Declare(Setting) error`
+ `String/Int/Duration/Bool(key)`; `Auth.Add(Role, Authenticator)/Require(Role, http.Handler)`;
`core.Subscribe[T](bus, topic, func(T)) cancel` / `core.Publish[T](bus, topic, T)`; `Host.Register/Run/Ready/Addr/ModuleNames`.
Stage 3 reuse: `storetest.Run(t, func(now func() time.Time) core.Store { ... })`.
