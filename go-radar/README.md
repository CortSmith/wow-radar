# Nearby Radar (Go)

A World of Warcraft (Forever client) companion radar. It follows the newest `WoWCombatLog*.txt`
in real time, tracks every player named in the advanced combat log and shows them as pips on a
radar in a native WebView2 window. One `.exe`, no cgo.

## Build

```powershell
.\build.ps1            # vet + test + dist\radar.exe   (-ldflags "-H=windowsgui -s -w", no console)
.\build.ps1 -Debug     # dist\radar-debug.exe (console build; use with --debug)
.\build.ps1 -SkipTests
```

Requires Go (go.mod says 1.27) and, to run, the Edge WebView2 runtime (present on Windows 11).
If WebView2 cannot start, the app opens the default browser instead (closing the tab ends the
process after 30 s unless `--keep-alive`).

## Run / flags

`dist\radar.exe [flags]` (flag > environment > default)

| flag | meaning |
|---|---|
| `--logs DIR` / env `WOW_LOGS` | logs folder (default `C:\Program Files (x86)\World of Warcraft\_forever_\Logs`) |
| `--from-start` | read the newest log from its beginning; default reads only the last `--tail-mb` (8) |
| `--realm-suffix -PvP-US` | realm suffix to strip; default auto-detected (>= 6 players share it) |
| `--track-npcs` | also track Creature/Vehicle/Pet GUIDs (default players only) |
| `--no-approx` | disable approximate sightings |
| `--orientation std\|alt` | std: X north/up, Y west/left; alt: X right, Y up |
| `--maps DIR` / env `WOW_MAPS` | maps folder with `index.json` (default `.\maps`, `..\maps`, `..\..\maps` relative to the exe) |
| `--history FILE` | base history path (default `radar-history.json` next to the exe, else `%APPDATA%\NearbyRadar`); the data lives in `<base>-<ruleset>.json` and `<base>-<ruleset>-bounty.json` (see "Server rule-sets") |
| `--ruleset auto\|normal\|pvp\|rp\|rppvp` | server rule-set whose data is used (default `auto`: detect from the log) |
| `--stale 5m` / `--expire 15m` | expiry thresholds |
| `--browser` | use the default browser, no native window |
| `--debug` | log to stderr (console build) and print the UI URL |
| `--log-file FILE` | write diagnostics to a file |
| `--no-ui` | headless: API only (smoke tests) |
| `--keep-alive` | browser mode: do not exit when the tab closes |
| `--analyze-logs` / `--analyze-file PATH` | bounty reverse analysis at startup (see "Bounty system") |

## UI

Radar canvas (wheel = range, drag = pan, "Follow me"), list panel, zone map background if the
maps folder is present. Pip colours: friendly blue, hostile red, neutral amber, stale dark grey;
dashed ring = approximate position. **History is hidden by default**: tick "History" or press `H`;
the data is also at `GET /api/history`. Other endpoints: `/api/state`, `/api/events` (SSE, the UI
falls back to polling `/api/state`), `/api/maps/<file>`.

### Map picker

The box at the left of the header is a combobox. Click it to open the full list, or type to narrow
it (case-insensitive, spaces and punctuation ignored). Arrow keys + Enter pick, Esc or a click
outside closes. "Auto (follow game)" (the default, not persisted) follows the game's current map;
picking a map shows that map instead: its image, pips, search tracks, calibration and zoom all
use the viewed map. Selectable maps are those with a known rectangle (from `MAP_CHANGE`) and an
image in `maps\index.json`; maps with a rectangle but no image are listed greyed with "no image".
The server sends them as `view.available` (`mapId,name,url,w,h,revealed`, sorted by name) and
`view.noImage` (`mapId,name`) in `/api/state`; `view.zone` is still the current map's image.

## Player search and tracks

Every GUID in the history also keeps a chronological *track* of position fixes (`t` unix ms from
the log event time, `mapId`, `zone`, `x`, `y`, `kind` exact|approx; only events that carry a
position are stored). Names are kept (latest display and raw name plus up to 10 past names), so
players can be found after their pip expired from the live view.

UI: the side panel has a search box (250 ms debounce, case-insensitive substring over display,
raw and past names). Picking a result draws that player's fixes on the current map: small dots
(blue friendly, red otherwise, approx = hollow, newest one ringed) joined in time order by a dotted
line whose older segments are fainter. Fixes on other maps are not drawn; the panel lists the count
per map. Hover a dot for time and 0-100 coordinates. `x` button or Esc clears it. The track refreshes every 5 s.

API (loopback Host check applies):

* `GET /api/players?q=<text>` - up to 50 `{guid,name,raw,reaction,fixes,firstSeen,lastSeen}`, newest
  first; empty `q` lists the most recent named players; no match gives `[]`.
* `GET /api/track?guid=<guid>[&map=<id>]` - `{guid,name,reaction,total,maps:[{mapId,zone,count}],fixes:[…]}`.
  `map` filters `fixes` only (`total`/`maps` describe all). 400 for a missing guid or a non-numeric/negative map, 404 for an unknown guid.

Pruning (constants in `internal/history/track.go`): a fix is skipped when it is within 2 yards of the
previous fix on the same map and less than 5 s later (`MinFixDistance`, `MinFixInterval`); at most
5000 fixes per player (`MaxTrackPerPlayer`, oldest dropped); at most 250000 fixes overall
(`MaxTotalFixes`, about 15 MB of JSON): beyond that the least recently seen players lose their whole
track down to 90 %, their aggregates stay. Positions are rounded to 0.1 yard. The file is written
compact (history version 2), only when something changed, atomically; version 1 files load unchanged.
`/api/history` and `Get`/`Snapshot` never include the tracks.

## Bounty system

Tracks **hostile players with the most kills of friendly players**, live and by re-reading old logs.

**What counts.** A *victim* is a friendly player (`Player-` GUID, flags `0x1` or `0x10`) that dies:
`UNIT_DIED` (not when its trailing unconscious flag is `1`, which is a Hunter feigning death),
`PARTY_KILL` (src = killer) or `SPELL_INSTAKILL`. Signals for the same victim within 3 s are one
death (`DuplicateDeathWindow`). The *killer* (`killingBlows`) is the hostile player (`Player-`
GUID, hostile flag `0x40`) named by `PARTY_KILL`, else the source of the last damage event on the
victim at most 5 s before the death (`KillingBlowWindow`). Every other hostile player that damaged
the victim in the 15 s before the death gets an `assist` (`AssistWindow`; the window is a small
bounded per-victim ring: 64 hits, 256 victims, older hits evicted). Pets, guardians and totems
(`Pet-`/`Creature-` GUIDs) credit their hostile player owner when the owner is known (from the
advanced block's ownerGUID or a `SPELL_SUMMON`), otherwise nobody. NPC or environment final
blows give no killer (the death is only counted in `npcDeaths`; hostile players that hit the victim
in the window still get assists). Friendly-on-friendly kills (duels, own AoE, mind control) and
hostile-on-hostile kills give no credit at all.

**Score** (constants in `internal/bounty/store.go`): `bounty = killingBlows * 10` (assists are still recorded and break ties, but are worth 0, so only kills of friendly players count; the UI lists and marks only players with a bounty above 0)
(`PointsPerKillingBlow`, `PointsPerAssist`). Ranking: killing blows desc, assists desc, most
recent credited death. Also kept per player: `involvedDeaths` (distinct victims killed or
assisted), `firstKill`/`lastKill` (first/latest credited death), last zone, map and position, and up
to 200 recent records `{victim, t, role killer|assist, map, zone}`.

**Idempotency.** Every death has a stable id: SHA-1 (first 10 bytes, hex) of victim GUID + death
time in ms + log file name. The store remembers up to 100000 ids (persisted), so re-analysing the
same log, or live tailing and a later analysis of the same file, never counts a death twice.

**Storage.** `<history file name>-bounty.json` next to the history file (default
`radar-history-bounty.json`; now per rule-set, see "Server rule-sets"), JSON version 1, written atomically and only when something changed.
Caps: 5000 players (weakest evicted), 200 records and 1000 victim ids per player. The history
file itself is unchanged, so old history files load as before.

**Reverse analysis.** Scans the logs folder oldest to newest (by modification time), streaming
line by line with the same parser (bounded memory; lines over 1 MiB are skipped; cancellable;
progress in bytes and files). Each file gets a fresh analyzer (sliding windows do not leak between
files) writing into the shared store. One job at a time.

| flag | meaning |
|---|---|
| `--analyze-logs` | at startup, analyze every combat log in `--logs` in the background (default off) |
| `--analyze-file PATH` | also analyze this file (repeatable; may be outside the logs folder) |

API (loopback Host check applies):

* `GET /api/bounty?limit=50` - `{rows:[{rank,guid,name,raw,bounty,killingBlows,assists,involvedDeaths,firstKill,lastKill,lastZone,lastMap,hasPos,lastX,lastY}],stats:{players,deaths,npcDeaths,seenIds}}`; default 50, max 500, 400 for a non-positive/non-numeric limit.
* `GET /api/bounty/player?guid=` - one row plus `recent` records (newest first); 400 without a guid, 404 unknown.
* `POST /api/bounty/analyze` - body optional `{"files":["WoWCombatLog-....txt"]}` (plain names inside the logs folder only: separators, `..`, drive letters, absolute paths, non-combat-log names and symlinks leaving the folder give 400); no body = all logs. Returns 202 `{jobId,status}`, 409 if a job is running. CSRF protection: the request must carry a custom header (`X-Radar: 1`) or `Content-Type: application/json`, and a foreign `Origin` is refused (403).
* `GET /api/bounty/analyze` - `{jobId,running,cancelled,filesDone,filesTotal,bytes,bytesTotal,deaths,newDeaths,errors,currentFile,startedAt,finishedAt}`.

UI: a collapsible **Bounty** panel in the side panel lists the top 50 (rank, name, bounty value,
killing blows / assists, time since last kill). Clicking a row opens that player's recorded track
exactly like a search result. Hostile players on the leaderboard that are visible on the radar get
a gold ring and a small gold diamond (dots stay blue/red, no names; the tooltip shows the bounty).
The **Analyze logs** button POSTs the endpoint above and polls the status every second; it is safe to re-run.

Real-log event shapes: `UNIT_DIED,0000000000000000,nil,0x80000000,0x80000000,<dstGUID>,"<dst>",<flags>,<raid>,<unconsciousFlag>`
(no source; the last field is `1` for a feigning Hunter), `PARTY_KILL,<src(4)>,<dst(4)>,0`.

## Rules implemented

* Any event with an advanced block updates the unit it describes (the block's own GUID, which can
  be the source or the destination): position, HP/max HP, level; src/dst update name, flags,
  reaction and last-event time. Damage dealt is credited to the source of `*_DAMAGE` events
  (`*_LANDED` duplicates are ignored).
* Expiry per target: log timestamp of its last event vs. wall clock (injectable `clock.Clock`):
  > 5 min stale, > 15 min removed from the live view (history is kept and merges when seen again).
* Approximate sightings: players named on a line without their own fix get "my" last fix
  (kind `approx`, weight 0.3) if it is < 120 s old. A weighted `estimate` over the last located
  events within a 30 s encounter gap is attached to each pip.
* "Me" = flag bit `0x1` on a Player GUID. Reaction: `0x1|0x10` friendly, `0x40` hostile, `0x20` neutral.

### Verified log layout

`event, src(4), dst(4), prefix params (SWING 0, SPELL/RANGE/DAMAGE_* 3, ENVIRONMENTAL 1),
[19 field advanced block: guid, owner, hp, maxhp, AP, SP, armor, absorb, 6 power fields, X, Y, mapId, facing, level],
suffix params (damage: amount, base, overkill, school, ...)`. The block sits *before* the amount.
Unknown event types fall back to scanning for a block.

## Server rule-sets (separate data per rule-set)

The radar keeps one dataset per WoW server rule-set: `normal` (Normal), `pvp` (PvP), `rp` (RP) and
`rppvp` (RP-PvP). Each dataset has its OWN history (aggregates, per-player tracks, search) and its
OWN bounty ledger; the header toggle (`Auto | Normal | PvP | RP | RP-PvP`) chooses which one the
app reads AND writes. The live radar view (current pips) is not per rule-set and is never cleared
by switching. Names follow the realm types of the Warcraft wiki / Blizzard (Normal, PvP, RP, RP-PvP).

* **Auto** (default): the rule-set is detected from the log owner's own raw name, e.g.
  `Name-PvP-US` (realm token `PvP`; `RPPvP`, `RP-PvP`, `RP`, `Normal` also work; unknown realms are
  not detected). If no event naming you was read yet (e.g. the 8 MB tail window of an old log does not
  contain you), the realm token shared by the players seen (the tracker's realm suffix) is used as a
  weaker hint. Without any detection it falls back to the last manual choice stored in
  `radar-settings.json`, then to `pvp`. Detection happens before the event is recorded, so the first
  event naming you already lands in the right dataset (events before you appear in the log, if any,
  use the fallback).
* **Manual**: a pick overrides auto until you press Auto again. `{mode, ruleset}` is persisted in
  `radar-settings.json` (atomic write, next to the history file).
* Flag `--ruleset auto|normal|pvp|rp|rppvp` (default `auto`; an invalid value is an error). A
  concrete id is applied like a manual pick (and persisted).
* Files (history path `X.json`, default `radar-history.json`): `X-<id>.json` and
  `X-<id>-bounty.json`, e.g. `radar-history-pvp.json`, `radar-history-pvp-bounty.json`. A dataset is
  opened lazily the first time it is used; all open datasets are flushed every 10 s and on exit.
* **Legacy migration**: if no per-rule-set file exists but the old `radar-history.json` (and/or
  `radar-history-bounty.json`) does, the first dataset that is used gets a COPY of them (the old
  files are never moved or deleted) and `radar-history-migrated.json` is written so it happens once.
  The target is the rule-set active at that moment (auto-detected or last choice, default `pvp`).
* The live tail, the reverse analysis (`Analyze logs`) and every read (search, track, history,
  bounty) use the active dataset only.

API: `GET /api/ruleset` -> `{active,mode:"auto"|"manual",detected:id|null,rulesets:[{id,name,hasData,players}]}`;
`POST /api/ruleset` with `{"ruleset":"rp"}` or `{"ruleset":"auto"}` returns the same payload (400
for an unknown id; same CSRF rules as the bounty POST: `X-Radar` header or JSON content type, a
foreign `Origin` gives 403, loopback Host only). `/api/state` also carries `view.ruleset`
(`{active,mode,detected}`). Code: `internal/ruleset` (ids, names, parsing, detection) and
`internal/dataset` (the registry that routes to the active stores).

## Architecture

```
cmd/radar            main, WebView2 window (windows only), browser fallback
internal/parser      line -> Event (CSV, timestamp->UTC, adv block, reaction, name cleanup)
internal/tailer      LogSource + Follower (newest file, rotation, truncation, partial lines)
internal/tracker     live units, expiry, approx fixes, estimate, snapshot, ApplyObservation
internal/history     per-GUID JSON history, atomic writes
internal/ruleset     rule-set ids/names, parsing and realm-token detection
internal/dataset     per-rule-set history + bounty registry (routes to the active rule-set, migration, settings)
internal/bounty      friendly-death analyzer, bounty ledger (JSON), reverse log analysis runner
internal/screenshot  CONNECTOR ONLY: Observation, Reader, NoopReader, Poller
internal/server      HTTP + SSE + embedded UI on 127.0.0.1:<random> (Host-header guarded)
internal/maps        optional maps index loader
internal/app         config/flags and wiring
web/                 embedded index.html, app.js, style.css
```

### Multi-log extension points (search for `MULTI-LOG:`)

Only one log is followed today. To follow several: create one `tailer.Follower` per folder with a
distinct ID, append to `App.Sources`; `App.Pump` already tags each event with the source ID and
`Tracker.Apply(sourceID, ev)` keeps "me", zone, map and last fix per source while units are
shared by GUID. Open points marked in code: choice of primary source, per-source reaction.

### Screenshot connector (not implemented)

`internal/screenshot` only defines `Observation`, `Reader`, `NoopReader` (returns
`ErrNotImplemented`) and `Poller`, which is disabled while the reader is a Noop and otherwise
calls `Tracker.ApplyObservation`, updating "me" position/zone/map and counting as an event for
"me". A future reader should capture the game window, blank everything except the minimap
coordinate text and zone text, and return an `Observation`. Assign it to `App.ScreenshotReader`.

## Tests

`go test -cover ./...` (table-driven, fake clock, temp dirs). `-race` needs cgo/gcc and was not run.
