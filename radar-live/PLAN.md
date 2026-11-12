# radar-live: master plan

A new section of NearbyTracker: a hosted web page that re-pulls data **every 60 seconds** for the user's chosen
parameters (one player, or everyone in a zone), a **database server** it reads from, and a second Go application, the
**ingest API**, that accepts live combat-log connections, parses out the player details we care about and updates the
database in near real time. It must look like the Go app (`../go-radar/web`) and be trivially hostable.

Sources read: `../HANDOFF.md`, `../go-radar/README.md` (log format, rules, API), `../radar-cloud/{PROJECT,README,NAMING,PLAN}.md`,
`../radar-cloud/site/ARCHITECTURE.md`. This project is **separate** from radar-cloud (AWS) and does not modify it.

## 1. Hosting target (hard constraint)

"Easy to host from a shitty laptop running Xubuntu."

- Two **static Linux binaries** (`CGO_ENABLED=0 GOOS=linux GOARCH=amd64`, optional arm64), cross-compiled from Windows
  by `build.ps1` / `Makefile`. No Docker, Node, Python or database package needed on the laptop.
- Database = **SQLite via `modernc.org/sqlite` (pure Go, no cgo)**, WAL mode, one file. Idle RAM budget < 60 MB per binary.
- The web page is **embedded** in the db-server binary (`go:embed`) and also works from any static host (the API base URL is
  configurable in `site/js/config.js`).
- Install = copy two files + `deploy/install.sh` (creates a service user, `/var/lib/radar-live`, two systemd units, an env file).
  HTTPS / remote access is optional: docs cover a Cloudflare Tunnel or Caddy; the default is plain HTTP on the LAN.
- Config = **flags + environment + a simple `KEY=value` env file** (no third-party parser).
- Backups: `radar-db backup <file>` (SQLite `VACUUM INTO`) and a documented cron line.

## 2. Architecture

```
 WoW log --(radar-feed or any client)--> radar-ingest :8788 --POST /internal/v1/ping--> radar-db :8787 --> SQLite
                                          parse + extract                                  |  read API + embedded page
 Browser page (polls every 60 s) ---------------------GET /api/v1/*-----------------------+
```

- **radar-db** (database server): owns the SQLite file; read API for the page; internal write API for ingest; serves the page.
- **radar-ingest** (the API application): live log connections, parsing, per-source state, batching, forwarding to radar-db.
- **radar-feed** (optional tiny client, last stage): tails the newest `WoWCombatLog*.txt` and streams new lines to ingest.
- Separate processes so either can restart alone; ingest buffers (bounded) while the db is down.

### Core + sockets (modularity rule)

One Go module `radarlive`. `internal/core` is a small host that knows nothing about players or logs. Every feature is a
**module** that plugs into a **socket** of the core. **Deleting a feature's package and its one line in `cmd/*/modules.go`
must still build and pass tests.** An architecture test enforces it (Stage 1).

```go
type Module interface { Name() string; Init(*core.Host) error }  // optional Start(ctx) / Stop(ctx) interfaces
// Sockets on the Host (registries modules add to; the core only iterates them):
//   Routes  (HTTP handlers by method+path)     Parsers (extractors fed each parsed Event)
//   Stores  (named Store implementations)      Queries (named read handlers used by the sweep)
//   Sinks   (receive extracted Pings)          Hooks   (OnStart / OnStop)
//   Config  (typed settings each module declares)   Auth (request authenticators; none registered = open, loud warning)
```

Rules: modules never import each other, only `core` and `domain` (shared plain types). Cross-module talk goes through core
sockets or the event bus. `cmd/*/modules.go` is the only file that lists modules.

### Package layout

```
radar-live/
  go.mod                      module radarlive (go 1.27)
  cmd/radar-db/ {main.go, modules.go}
  cmd/radar-ingest/ {main.go, modules.go}
  cmd/radar-feed/ {main.go}                 (stage 7)
  internal/core/              Host, Module, sockets, config, lifecycle, bus           (stage 1)
  internal/domain/            Contact, Fix, Chart, Ping, Ruleset, Reaction, Sector    (stage 1)
  internal/mod/
    memstore/                 in-memory Store (tests, dev)                            (stage 1)
    logparse/                 line -> Event (CSV, timestamps -> UTC ms, advanced block) (stage 2)
    contactx/                 Event -> per-player Contact deltas, fixes, approx fixes (stage 2)
    bountyx/                  friendly deaths -> killing blow / assist deltas         (stage 2)
    sqlstore/                 SQLite Store, schema, migrations, merge rules           (stage 3)
    readapi/                  /api/v1/* read handlers (the page's data)               (stage 4)
    writeapi/                 /internal/v1/ping (idempotent, additive)                (stage 4)
    keys/                     view / ingest API keys, constant-time compare           (stage 4)
    streamapi/                /v1/stream, /v1/lines live connections (ingest)         (stage 5)
    relay/                    batching, retry, bounded buffer, forward to radar-db    (stage 5)
    static/                   embedded page server                                    (stage 6)
  site/                       the web page (index.html, style.css, js/ ES modules)    (stage 6)
  deploy/                     install.sh, *.service, example env, Caddyfile snippet   (stage 7)
  docs/plans/                 one plan file per stage (updated with results) + STATUS.md
```

## 3. Data model and conventions

Vocabulary follows `../radar-cloud/NAMING.md`: **contact** (player), **blip**, **fix**, **trail**, **ping** (one batch from
ingest to db), **sweep** (one page poll), **sector** (what a sweep looks at), **chart** (map), **ledger**, **bounty**,
**ruleset** (`normal|pvp|rp|rppvp`). JSON field names match radar-cloud (`lastSeen`, `mapId`, `posTime`, ...).

- Times: UTC epoch milliseconds everywhere. Local time only in page display code.
- Contact fields: `guid, name, raw, pastNames[<=10], reaction (friendly|hostile|neutral|unknown), level, maxHp, x, y, mapId,
  zone, posTime, firstSeen, lastSeen, events, srcEvents, dmgTotal, dmgHits, dmgEvents, hpSum, hpSamples, kb (killing blows), assists`.
- Trail: fixes `[t, mapId, x, y, kind exact|approx]`; dedupe <=2 yd and <5 s on the same chart; round 0.1 yd; cap 5000 per contact.
- Chart: `mapId, name, x0, x1, y0, y1` (from `MAP_CHANGE,id,"Name",a,b,c,d`: x-range=(a,b), y-range=(c,d), verified in game per HANDOFF).
- Each ruleset is its own partition (contacts, trails, bounty, ledger). Keys may be bound to an account namespace; a
  single-account setup works with zero config.
- Counters change only by **additive deltas tied to a `batchId`**; a replayed ping is a no-op. firstSeen=min,
  lastSeen/position newest-wins, maxHp=max.
- Active window: stale after 5 min, dropped from live views after 15 min (`activeWithin` overrides). History is kept.
- Bounty: `kb * 10`; victims are friendly players; killing blow = PARTY_KILL source or last damage <=5 s before the death;
  assists (worth 0) = hostile players that hit the victim in the prior 15 s; pets/totems credit their owner; 3 s dedupe window.
- Log format facts: see `../HANDOFF.md` section 4 and `../go-radar/README.md` "Verified log layout" (advanced block sits between
  prefix params and the amount; it describes the *info unit*; GUID prefixes Player-/Creature-/Pet-/Vehicle-; flags 0x1 me,
  0x10 friendly, 0x20 neutral, 0x40 hostile; strip realm suffix; approximate fix = "me" last fix < 120 s old).
- Real combat logs hold real names: **tests use synthetic logs only.**

## 4. API contract (all stages build against exactly this)

Auth: `X-View-Key` (read) / `X-Ingest-Key` (write). If no keys are configured the API is open and a warning is logged.
JSON everywhere; errors `{ "error": "<code>", "message": "..." }`; CORS allowed origin configurable.

**radar-db read API**

| Request | Meaning |
|---|---|
| `GET /api/v1/sweep?rs=&scope=player\|zone\|all&guid=&mapId=&grp=friendly\|hostile\|other\|any&q=&since=&trail=0\|1&activeWithin=` | **The 60-second poll.** `since=0` = full load of the sector; later calls return only rows with `lastSeen > since` (client sends `now - 5000` overlap). Response `{now, changed, sector, contacts:[Contact], trail?:[Fix]}` |
| `GET /api/v1/contacts?rs=&q=&mapId=&grp=&limit=&offset=` | search (case-insensitive substring over name, raw, past names; default 50, cap 500) |
| `GET /api/v1/trail?rs=&guid=&mapId=` | one contact's fixes |
| `GET /api/v1/bounty?rs=&limit=` | ranking (default 50, max 500) |
| `GET /api/v1/bounty/player?rs=&guid=` | one row + rank |
| `GET /api/v1/charts?rs=` | map rectangles seen |
| `GET /api/v1/status` | `{version, rulesets:[{id,contacts,lastWrite}], uptimeSec}` |
| `GET /healthz` | `ok`, no auth |

**Common search parameters** (from the existing docs): ruleset `rs`, player name `q`, player `guid`, zone `mapId`, faction group
`grp` (Blue = friendly, Red = hostile/neutral), `since`, `activeWithin`, `limit`; bounty `limit`. Default `rs` = server default
(flag `--default-ruleset`, else `pvp`).

**radar-db write API (called only by ingest):** `POST /internal/v1/ping` body
`{rs, batchId, source, contacts:[Contact + d{deltas} + fixes], charts:[...], deaths:[{id,...}]}` -> `{received, changed, duplicate}`.

**radar-ingest API:** `POST /v1/stream?source=<id>&rs=<optional>` (chunked body of raw log lines, held open, summary on close),
`POST /v1/lines?source=&rs=` (one batch: `text/plain` lines or `{"lines":[...]}`), `GET /v1/sources` (live connections, lines/s,
last event), `GET /healthz`. Ruleset auto-detected from the realm token (as in go-radar) when `rs` is omitted.

## 5. Quality bar (every stage)

1. **TDD**: tests first, seen failing, then code. Every exported function and handler has a test. Table-driven, fake clock,
   `t.TempDir()`, `httptest`. Frontend logic tested with `node --test` (Node: `C:\Program Files\nodejs\node.exe`).
2. `gofmt -l .` empty, `go vet ./...` clean, `go test ./... -cover` green; coverage >= 85% outside `cmd/`.
3. Readable: functions <= 40 lines, nesting <= 3, package comment + doc comment on every exported name, a single responsibility
   per file, named constants with unit suffixes, no magic numbers, errors wrapped with context.
4. Modularity test passes; no module imports another module.
5. Stage end: update the stage plan file (done / test counts / deviations), lead review pass (run everything, read the diff, fix
   findings), entry in `docs/plans/STATUS.md`.
6. No secrets in logs; constant-time key compare; request body and line size caps; bounded buffers everywhere (weak laptop).
7. **No secrets, tokens or personal information in the repository** (user rule). Secret settings come from application-specific
   `RADAR_*` environment variables only; see `docs/security/secrets-policy.md`. Example files hold placeholders; tests use fake values.

## 6. Stages

| # | Stage | Plan file | Depends on | State |
|---|---|---|---|---|
| 1 | Core host, sockets, domain types, memstore, architecture test | `docs/plans/stage-1-core.md` | - | done |
| 2 | Parsing + extraction modules (logparse, contactx, bountyx) | `docs/plans/stage-2-parsing.md` | 1 | done |
| 3 | SQLite store (sqlstore): merge, idempotency, queries | `docs/plans/stage-3-sqlstore.md` | 1 | done |
| 4 | radar-db binary: readapi, writeapi, keys | `docs/plans/stage-4-db-server.md` | 1, 3 | done |
| 5 | radar-ingest binary: streamapi, relay | `docs/plans/stage-5-ingest.md` | 1, 2, API contract | done |
| 6 | Web page (60 s sweep, looks like go-radar) + static module | `docs/plans/stage-6-web.md` | API contract | done |
| 7 | Feeder, Linux packaging, e2e smoke, docs | `docs/plans/stage-7-hosting.md` | all | done |
| 8 | Security: DDoS protection, rate limiting, hardening (tasks 8.1 to 8.12) | `docs/plans/security-plan.md` | 7 | 8.1 done, rest open |
| 9 | radar-client: windowed desktop app, choose the log file, click Start, live upload on every change | `docs/plans/stage-9-client.md` | 5, 7 | done |

Live per-stage detail: `docs/plans/STATUS.md`.

Stages 2, 3, 6 run in parallel after 1 (disjoint directories); 4 and 5 run in parallel after those. Subagents: Sonnet, medium effort.
