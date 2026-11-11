# Frontend architecture (site/)

Framework-free, no bundler, no external requests. Every script is a plain `<script>` (or `importScripts` in the Worker)
and registers itself under **one global namespace, `Radar`** (`Radar.config`, `Radar.parser`, `Radar.ui.search`, ...).
Logic modules also `module.exports` so node tests `require` them. Names follow `../NAMING.md`; wire names
(`/api/*` paths, `pips`, `lastSeen`, `mapId`, query params, worker message fields, stored pref keys) are frozen.
Code style: Prettier (`../.prettierrc.json`), single quotes, 2 spaces, width 120.

## Modules by layer

Each file has one responsibility, stated in its header comment together with its public API.

### Config, docs and shell

| File          | Responsibility                                                                                        |
| ------------- | ----------------------------------------------------------------------------------------------------- |
| `config.js`   | every tunable constant (units, intervals, limits, colors, engine thresholds) with units documented    |
| `types.js`    | JSDoc typedefs (Contact, Blip, Snapshot, Fix, Ping, Sector, Settings); documentation only, not loaded |
| `messages.js` | every user-visible text and the one rule that turns an error into text                                |
| `index.html`  | the static page: semantic markup, ARIA, the ordered script list                                       |
| `style.css`   | the single stylesheet (sections, `--radar-*` custom properties)                                       |

### Logic (DOM-free, node-tested)

| File                  | Responsibility                                                                               |
| --------------------- | -------------------------------------------------------------------------------------------- |
| `format.js`           | ages, numbers, percents, names, query strings                                                |
| `time-zone.js`        | log timestamp to UTC instant (offset or fallback zone, DST rule), UTC ISO text, zone display |
| `charts.js`           | chart (map) math: world to frame percent, joining charts with the maps index                 |
| `contacts.js`         | stored rows to cloud snapshot, bounty ranking, history cells, trail summaries                |
| `http.js`             | login/signup and the authenticated JSON api client                                           |
| `ledger.js`           | file fingerprints, ledger watermarks, request chunking, resumable pings, the outbox          |
| `console-settings.js` | default, merge and compaction of per-operator settings                                       |
| `parser.js`           | combat log text to event objects; names and rule-sets                                        |
| `tracker.js`          | live units from events: stale/expiry, approximate fixes, estimates, snapshot                 |
| `analyzer.js`         | bounty: killing blows and assists from deaths of friendly players                            |
| `engine.js`           | per-ruleset accumulation of events into upload deltas, gated by watermarks                   |
| `engine-protocol.js`  | the engine message protocol (request to reply), transport independent                        |
| `engine-client.js`    | promise client over a Worker or in-process handler                                           |
| `ping.js`             | upload of one whole log file (parse, flush, upload)                                          |
| `tail.js`             | live tailing of the newest log and interval uploads                                          |
| `sweep.js`            | the adaptive cloud sweep loop and merging of delta rows and fixes                            |
| `blips.js`            | radar geometry: frame, zoom/pan, projection, calibration keys, visibility                    |
| `state.js`            | the shared UI state object and the event bus                                                 |
| `view-model.js`       | everything the screen shows, as plain data and text                                          |

### Platform adapters

| File          | Responsibility                                                                          |
| ------------- | --------------------------------------------------------------------------------------- |
| `adapters.js` | the only code touching localStorage, fetch, File System Access, crypto, Worker, confirm |
| `dom.js`      | cached element lookup and safe element building (`textContent`, never `innerHTML`)      |

### UI controllers (DOM, one per area)

| File                 | Responsibility                                                           |
| -------------------- | ------------------------------------------------------------------------ |
| `feedback.js`        | footer, statuses, progress bar, connection dot, login error              |
| `session.js`         | the operator's pass: persistence and the authenticated api               |
| `auth-ui.js`         | login / create account overlay                                           |
| `operator-flow.js`   | session lifecycle: enter, log out, expiry                                |
| `settings-ui.js`     | console controls, calibration keys, persisting settings                  |
| `ruleset-ui.js`      | the rule-set segmented control                                           |
| `chart-box.js`       | the chart (map) combobox                                                 |
| `view-controller.js` | adopt a new snapshot, chart index, repaint everything                    |
| `contact-panel.js`   | header readouts and the contact list                                     |
| `search.js`          | name search and the followed trail                                       |
| `bounty-panel.js`    | the bounty leaderboard                                                   |
| `history-panel.js`   | the stored-history table                                                 |
| `canvas.js`          | paints the radar canvas and answers hit tests                            |
| `canvas-input.js`    | pointer input and tooltip on the canvas                                  |
| `tail-ui.js`         | file upload, drop zone, Analyze logs, live tail controls                 |
| `cloud-sync.js`      | full reads and sweeps while not live (DOM-free; browser events injected) |
| `ads-ui.js`          | the sponsor column                                                       |

### Glue and engine host

| File        | Responsibility                                                                 |
| ----------- | ------------------------------------------------------------------------------ |
| `app.js`    | creates state and bus, routes events, supplies browser events, starts the page |
| `worker.js` | the Web Worker around the engine; documents the message protocol               |

Modules talk through the shared `state` object and an event bus; they do not call each other's redraw.
Events: `draw list header trail-info chart-input history sector refresh load-cloud engine-config engine-ruleset snapshot live-started live-stopped ruleset-changed`.

## UI to modules to API calls

| UI element (index.html id)                                     | Module                                     | API / engine                                                                       |
| -------------------------------------------------------------- | ------------------------------------------ | ---------------------------------------------------------------------------------- |
| `#auth-overlay` login / create account                         | `auth-ui.js` + `session.js` + `http.js`    | `POST /api/login`, `POST /api/signup`                                              |
| session start, `#logout`                                       | `operator-flow.js`                         | `GET /api/me` (settings), then `cloud-sync.js` begin                               |
| `#ruleset-box` (`rs-*` buttons)                                | `ruleset-ui.js`                            | none (reloads via `ruleset-changed`); `PUT /api/settings` through `settings-ui.js` |
| `#chart-box` map combobox                                      | `chart-box.js` + `view-model.js`           | none (sets the viewed chart, the sweep follows)                                    |
| range, follow, stale, blue/red, map, fixed map                 | `settings-ui.js`                           | `PUT /api/settings` (debounced), localStorage `nr.*`                               |
| `#timezone-input` (+ `#timezone-choices` datalist)             | `settings-ui.js` + `time-zone.js`          | `PUT /api/settings` (`timezone`), localStorage; repaints every time, no reload     |
| `#upload-summary` (log span after an upload)                   | `tail-ui.js` + `view-model.js`             | none (engine `ack` carries `firstT`/`lastT`)                                       |
| `#radar` canvas                                                | `canvas.js`, `canvas-input.js`, `blips.js` | none                                                                               |
| `#contact-list`, `#contact-summary`, `#zone`, `#status-footer` | `contact-panel.js` + `view-model.js`       | none                                                                               |
| `#search`, `#trail-info`                                       | `search.js`                                | `GET /api/players?q=`, `GET /api/track`                                            |
| `#bounty-panel`                                                | `bounty-panel.js`                          | `GET /api/bounty`, `GET /api/bounty/player`                                        |
| `#history-panel`                                               | `history-panel.js`                         | rows already loaded by `cloud-sync.js`                                             |
| `#log-source-panel` live tail                                  | `tail-ui.js` + `tail.js` + `ping.js`       | `GET /api/ledger`, `POST /api/upload`                                              |
| drop zone / Choose files                                       | `tail-ui.js` + `ping.js`                   | `GET /api/ledger`, `POST /api/upload`                                              |
| Analyze logs                                                   | `tail-ui.js`                               | `POST /api/reset`, then as above                                                   |
| cloud view when not live                                       | `cloud-sync.js` + `sweep.js`               | `GET /api/players` (full read), `GET /api/poll` (deltas)                           |
| `#ads`                                                         | `ads-ui.js`                                | static `ads/ads.json`; charts: static `maps/index.json` (`view-controller.js`)     |

## Concepts worth knowing (the WHY)

- **Ledger and watermark.** Logs get re-uploaded. `ledger.js` fingerprints a file (first 64 KB + name); the server keeps
  one watermark (`lastTs`) per rule-set and fingerprint. The engine ignores events at or before it, so a repeat upload adds nothing.
  A ping keeps its `batchId` across retries so the server can skip contacts it already applied.
- **Sweep overlap.** `GET /api/poll` sends `since = lastNow - OVERLAP_MS`; rows committed while the previous request was in flight
  would otherwise fall between two sweeps. The `v` tripwire version makes an unchanged answer cheap. The cadence is adaptive:
  fast on input or change, slow when quiet, exponential backoff on errors, paused while the tab is hidden.
- **Engine in a Worker.** Parsing a multi-hundred-MB log must not block the page; `engine-client.js` falls back to an in-process
  engine when Workers are unavailable. One engine per manual file keeps memory flat; the log clock stops anything expiring on replay.
- **Rule-sets are partitions.** Contacts, trails and bounty are stored per rule-set (`normal pvp rp rppvp`), so changing it reloads data.

## Time: UTC inside, local time only on screen

- **Stored and wire values are UTC instants.** Every timestamp that is parsed, stored, uploaded or returned by the api is
  unix epoch milliseconds (a number, a UTC instant): `firstSeen`, `lastSeen`, `posTime`, fix `t`, ledger `lastTs`,
  `since`. Nothing in the wire or database format is an ISO string.
- **Normalised at parse time.** `parser.parseLine(line, zone)` calls `time-zone.parseLogTimestamp` in the Worker. A line's
  own offset (`-5`, `+1`, `+5:30`, `-9:30`) always wins. A line without one is read as wall-clock time in the operator's
  zone (`settings.timezone`, default the browser's zone), sent to the engine in the `config` message. DST rule: an
  ambiguous hour (clocks go back) takes the first occurrence; a nonexistent hour (clocks jump forward) is read with the
  offset in force before the change, so it moves forward by the gap.
- **Local time is display-only.** `state.zone` (resolved by `settings-ui.js`, never read from a global inside logic) is the
  zone every view formats with. `view-model.describeTimestamp` gives the text (`2026-11-11 14:00:00 EST`) and the ISO UTC
  string with an explicit `+00:00` (`toUtcIso`) used as the `title`/tooltip beside raw uploaded times; relative ages
  (`2m`) stay relative. Changing the zone emits `zone-changed`, which repaints everything without a reload.

## Tests

`test/site/<module>.test.js` per logic module; `shell.test.js` boots every script from `index.html` in a fake DOM (login,
cloud snapshot, rule-set control) and `architecture.test.js` checks this document against the files on disk.
