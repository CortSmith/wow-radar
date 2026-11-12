# radar-live

A hosted web page that re-polls **every 60 seconds** for the players you choose (one player, or everyone in a zone),
backed by a small database server and an ingest server that turns live WoW combat-log lines into player positions and
bounty credits. Built to run on a weak Xubuntu laptop: static Go binaries, SQLite, no Docker.

Part of NearbyTracker; independent of `../radar-cloud` (AWS). Design: [PLAN.md](PLAN.md). Progress: [docs/plans/STATUS.md](docs/plans/STATUS.md).

## Architecture

```
 gaming PC                         laptop (Xubuntu)
 +-------------+  POST /v1/stream  +----------------+  POST /internal/v1/ping  +-------------+
 | WoW log     |  (X-Ingest-Key)   | radar-ingest   |  (X-Ingest-Key)          | radar-db    |--> SQLite file
 | radar-feed  |------------------>| :8788 parse,   |------------------------->| :8789 (int) |
 +-------------+                   | extract, batch |   bounded retry buffer   | read API +  |
                                   +----------------+                          | embedded    |
 browser (page polls every 60 s) --- GET /api/v1/* (X-View-Key) -------------->| web page    |
                                                                               +-------------+
```

Everything is a **module** plugged into a small core host (`internal/core`). Removing a feature means deleting its
package and its one line in `cmd/*/modules.go`; an architecture test keeps modules independent.

| Module | Binary | Purpose |
|---|---|---|
| `sqlstore` / `memstore` | radar-db | SQLite store (default) / in-memory store (tests, `--store memstore`) |
| `keys` | both | view and ingest API keys |
| `readapi` | radar-db | `/api/v1/*` for the page |
| `writeapi` | radar-db | `/internal/v1/ping`, called by ingest |
| `static` | radar-db | the embedded web page (`site/`) |
| `logparse`, `contactx`, `bountyx` | radar-ingest | lines -> events -> contacts / bounty credits |
| `streamapi` | radar-ingest | `/v1/stream`, `/v1/lines`, `/v1/sources` |
| `relay` | radar-ingest | batches pings to radar-db, retries, bounded buffer |

To remove one: delete its line (and import) in `cmd/radar-db/modules.go` or `cmd/radar-ingest/modules.go`, optionally delete
`internal/mod/<name>`, and run `go build ./... && go test ./...`. (Keep `relay` first in the ingest list: it must stop last.)

`radar-feed` is not a module host: it is a tiny client (`cmd/radar-feed`, `internal/feed`) that runs next to the game.

## Quickstart (5 minutes)

On the Windows PC, open PowerShell in the `radar-live` folder (the one holding this README; from the repo root
run `cd radar-live` first), then build everything and copy it over:

```powershell
cd radar-live        # skip if you are already inside radar-live
powershell -ExecutionPolicy Bypass -File scripts\build.ps1      # writes dist\ with SHA256SUMS
scp dist\* you@laptop:/tmp/radar-live/
```

On the laptop:

```sh
cd /tmp/radar-live && sudo sh install.sh      # prints the view key and the ingest key once
```

Open `http://<laptop-ip>:8787/`, enter the view key. On the gaming PC, enable `/combatlog` in WoW and run:

```powershell
$env:RADAR_FEED_KEY_FILE = "ingest.key"   # secrets are environment variables only, never flags; keep the file out of the repo
radar-feed-windows-amd64.exe --ingest-url http://<laptop-ip>:8788
```

(`RADAR_INGEST_LISTEN=0.0.0.0:8788` in `/etc/radar-live/radar.env` lets the feeder in from the LAN.) Details, backups and
remote access (Cloudflare Tunnel / Caddy): [deploy/README-xubuntu.md](deploy/README-xubuntu.md). Host hardening (systemd sandbox, firewall,
fail2ban, key rotation, `verify-hardening.sh`): [docs/security/hardening.md](docs/security/hardening.md).

Try it locally without installing: `go run ./cmd/radar-db --store memstore --listen 127.0.0.1:8787` serves the page.

### Key wiring

One ingest key is shared: `RADAR_INGEST_KEYS` (both servers check it), `RADAR_DB_KEY` (ingest sends it to radar-db, same value),
and the feeder sends it as `X-Ingest-Key`. The page uses a separate view key (`RADAR_VIEW_KEYS`). `radar-db keygen` makes keys.
With no keys configured the API is open and a warning is logged.

**Secrets rule** (`docs/security/secrets-policy.md`): keys and tokens are environment variables (or a `*_FILE` variable naming a
file outside the repo). No command-line flag accepts a secret: `--view-keys`, `--ingest-keys`, `--db-key`, `radar-feed --key` and
`radar-client --key` stop with a message naming the variable. `--help` shows secrets as `<set>`/`<unset>` only. Before committing run
`scripts/scan-secrets.ps1` (or `.sh`); `go test ./internal/mod/guard` runs the same scan.

## Configuration

Every setting is a flag `--name`, or the environment variable `RADAR_NAME` (dashes become underscores), or a line in a
`KEY=value` file given by `--env-file` / `RADAR_ENV_FILE`. Precedence: flag, environment, env file, default.
The systemd units read `/etc/radar-live/radar.env` (see `deploy/radar.env.example`).

### Both servers

| Flag / env | Default | Meaning |
|---|---|---|
| `--internal-listen` / `RADAR_INTERNAL_LISTEN` | db `127.0.0.1:8789` | write-API-only listener (units: `RADAR_DB_INTERNAL_LISTEN`); radar-ingest `--db-url` points here |
| `--limits-max-conns`, `--limits-max-inflight` (+ `--limits-*-timeout`) | 512 / 256 per listener | connection and in-flight request caps, server timeouts (guard/limits) |
| `--listen` / `RADAR_LISTEN` | db `:8787`, ingest `127.0.0.1:8788` | HTTP listen address (the units take `RADAR_DB_LISTEN` / `RADAR_INGEST_LISTEN`) |
| `--max-body` | 8 MiB (ingest: 1 TiB, streams are capped by `stream-max-bytes`) | request body cap |
| `--shutdown-timeout` | 10s | graceful stop allowance |
| `--env-file` | none | settings file |
| `RADAR_VIEW_KEYS` (env only), `--view-keys-file` / `RADAR_VIEW_KEYS_FILE` | none | keys that may read the API (comma/newline lists, >= 16 chars). No flag takes a key value |
| `RADAR_INGEST_KEYS` (env only), `--ingest-keys-file` / `RADAR_INGEST_KEYS_FILE` | none | keys that may write |

### radar-db

| Flag / env | Default | Meaning |
|---|---|---|
| `--store` / `RADAR_STORE` | `sqlite` | `sqlite` or `memstore` (flag or environment only) |
| `--db-path` | `radar-live.db` (units: `/var/lib/radar-live/radar-live.db`) | SQLite file |
| `--cache-mb` | 16 | SQLite page cache per connection, MiB |
| `--max-fixes` | 250000 | global cap on stored trail fixes |
| `--query-timeout` | 5s | per-request store timeout |
| `--default-ruleset` | `pvp` | ruleset when a request has no `rs` |
| `--cors-origins` | off | allowed browser origin |
| `--site-dir` | embedded | serve the page from a folder (development) |

Subcommands: `radar-db keygen`, `radar-db backup [--db-path f] <target>`, `radar-db guard list|unban <ip>|ban <ip> <duration>`, `radar-db version`.

### radar-ingest

| Flag / env | Default | Meaning |
|---|---|---|
| `--db-url` | required | base URL of radar-db INTERNAL listener, e.g. `http://127.0.0.1:8789` |
| `RADAR_DB_KEY` (env only, no flag) | none | key sent to radar-db as `X-Ingest-Key` |
| `--relay-buffer-pings` / `--relay-buffer-bytes` | 5000 / 64 MiB | buffer while radar-db is down (oldest dropped) |
| `--relay-batch-pings`, `--relay-timeout` | 50, 10s | pings per pass, request timeout |
| `--relay-backoff-min` / `--relay-backoff-max` | 500ms / 30s | retry delays |
| `--stream-idle` | 120s | close a stream that sends nothing |
| `--max-sources` | 16 | concurrent feeders |
| `--stream-max-bytes` | 4 GiB | per stream |
| `--lines-max-lines` / `--lines-max-bytes` | 20000 / 8 MiB | `/v1/lines` batch caps |
| `--logparse-zone` | `UTC` | zone for timestamps without an offset |
| `--contactx-flush`, `--bountyx-flush` | 2s (ingest default) | how often pings are published |
| `--contactx-ruleset`, `--bountyx-ruleset` | `pvp` | ruleset until a realm token is seen |
| `--contactx-track-npcs` | false | also track non-player units in memory |

### radar-feed

| Flag / env | Default | Meaning |
|---|---|---|
| `--logs` / `RADAR_FEED_LOGS` | `C:\Program Files (x86)\World of Warcraft\_forever_\Logs` | folder with `WoWCombatLog*.txt` |
| `--pattern` | `WoWCombatLog*.txt` | file name pattern |
| `--ingest-url` / `RADAR_FEED_INGEST_URL` | `http://127.0.0.1:8788` | radar-ingest base URL |
| `RADAR_FEED_KEY`, or `--key-file` / `RADAR_FEED_KEY_FILE` (path of a key file) | none | ingest key. There is no `--key` flag: arguments are visible to other users |
| `--source` / `RADAR_FEED_SOURCE` | host name | source id shown by ingest |
| `--rs` / `RADAR_FEED_RS` | auto | force a ruleset |
| `--from-start` | off | send the whole current log instead of starting at its end |
| `--max-bytes-per-sec` | 262144 | upload cap (0 = unlimited) |
| `--queue-lines` | 200000 | lines kept while ingest is unreachable (oldest dropped) |
| `--poll` | 500ms | log check interval |

The feeder starts at the end of the newest log (after replaying its last `ZONE_CHANGE` / `MAP_CHANGE` so the map is known),
follows rotation and truncation, only sends complete lines, and reconnects with backoff. Delivery is at most once: a line
already written to a connection that then breaks is not resent, because ingest adds counters and a replay would double count.

### radar-client (desktop app)

`radar-client-windows-amd64.exe` is a small windowed app for players who prefer clicking to a command line: enter the server
address and ingest key, pick the combat log (or "use newest log in folder"), press **Start**. It uploads whenever WoW writes
new lines and shows live status. One exe, no installer, no cgo; the window is WebView2 (present on Windows 11), with the
default browser as fallback, and a Linux build runs the same UI in the browser. The ingest key is read from
`RADAR_CLIENT_INGEST_KEY` or typed into the window for that run; it is never saved. User guide, troubleshooting and the
smoke test: [docs/client.md](docs/client.md).

## API summary

Auth: `X-View-Key` for reads, `X-Ingest-Key` for writes. Errors: `{"error":"<code>","message":"..."}`. Times are UTC epoch ms.

| Request | Meaning |
|---|---|
| `GET /api/v1/sweep?rs=&scope=player\|zone\|all&guid=&mapId=&grp=&q=&since=&trail=0\|1&activeWithin=` | the 60-second poll |
| `GET /api/v1/contacts?rs=&q=&mapId=&grp=&limit=&offset=` | search |
| `GET /api/v1/trail?rs=&guid=&mapId=` | one contact's fixes |
| `GET /api/v1/bounty?rs=&limit=`, `/bounty/player?rs=&guid=` | bounty ranking |
| `GET /api/v1/charts?rs=` | map rectangles seen |
| `GET /api/v1/status` | version, per-ruleset counts, uptime |
| `POST /internal/v1/ping` | ingest -> db (idempotent per `batchId`) |
| `POST /v1/stream?source=&rs=` | ingest: held-open raw log lines; answers a summary when it ends |
| `POST /v1/lines?source=&rs=` | ingest: one batch of lines |
| `GET /v1/sources` | ingest: live connections |
| `GET /healthz` | `ok`, no auth |

## Development

```sh
go test ./...                       # includes test/e2e (builds the 3 binaries; skip with -short)
node --test site/test               # page logic
```

`test/e2e` builds the binaries into a temp dir, starts them on free ports, appends a synthetic combat log (the
`logparse` fixture with timestamps moved to "now") to a log folder watched by the real `radar-feed`, and checks the
sweep and status APIs, key enforcement and the served page. On Linux it also logs each process's RSS.
Real combat logs contain real names: tests use synthetic logs only.

Security work (threat model, checklist, hardening) is Stage 8: [docs/security](docs/security).
