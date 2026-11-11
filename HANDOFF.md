# HANDOFF: WoW Forever Enemy Radar (Go rewrite)

Written 2026-11-10 for a future Claude session that picks this up after a compaction or token cap. Check the actual state of `go-radar\` before trusting anything below; a build subagent was still running when this was written.

## 1. Original request (user's words, condensed)
A World of Warcraft Forever companion app that works as an **enemy radar**:
- Parses the **advanced combat log** with realtime updates.
- **Stores but hides** historical info per target, including estimated average damage dealt and health.
- Takes **periodic screenshots**, filters out everything except the **minimap coordinates and location**, and uses them to update the map.
- **Any** advanced combat log info updates the app.
- Timer per character, from that character's last logged event: **5 min -> pip dark grey, 15 min -> pip removed**.
- Best practices and **strict TDD** (tests before implementation).
- **Compiles and runs from a single executable.**
- Uses the **latest combat log file** and is a **windowed app**.
- Make **multi-log simultaneous parsing** possible, expressed as **comments** (extension points).

## 2. Decisions made with the user
| Topic | Decision |
|---|---|
| Stack | **Go** (only toolchain available: go1.27.1; no .NET SDK, no Python, no node, no Rust, no tesseract) |
| Location | New project in `NearbyTracker\go-radar\`. The existing Python app (`nearby_tracker.py`, `extract_maps.py`, `start.bat`, `README.md`, `maps\`) is **left untouched**. Reuse `maps\`. |
| Window | go-webview2 (pure Go, Edge WebView2) showing an embedded UI served from a local 127.0.0.1 server. Fall back to the default browser if WebView2 fails. |
| Screenshots | **Do NOT implement capture/OCR.** Build only the connector: `internal/screenshot` with an `Observation` struct, a `Reader` interface, a `NoopReader` that returns ErrNotImplemented, and a `Poller` that feeds `tracker.ApplyObservation`. The user chose to implement the reader later. |
| Tesseract | Rejected for now. A pure-Go glyph matcher could be added later behind the `Reader` interface. Zone names would be matched against `maps\index.json`, with a one-time calibration. |
| Multi-log | One active log now. Leave `// MULTI-LOG:` comments at each extension point. |
| Model policy | Use **haiku** subagents for exploration, **sonnet on medium effort** for most work, and **opus only** for tasks needing higher reasoning. |

## 3. Environment facts
- Windows 11, PowerShell. The working directory is `C:\Users\corts\Desktop\NearbyTracker`. It is not a git repo.
- WoW install: `C:\Program Files (x86)\World of Warcraft\` with `_forever_\Logs` (1 combat log file at survey time) and `_retail_\Logs`.
- Default log dir: `C:\Program Files (x86)\World of Warcraft\_forever_\Logs`.
- Real logs contain real player names. Never copy them into tests or code, and use synthetic data.

## 4. Log format notes (from the Python parser; verify against a real log)
- Line = `M/D/YYYY HH:MM:SS.mmm[+-H]  RECORD` (two spaces), RECORD is CSV with `"` quoting. The timezone offset is optional.
- f[0] event; f[1..4] source GUID, name, flags, raid flags; f[5..8] destination, same layout.
- Special events: `COMBAT_LOG_VERSION` (has `ADVANCED_LOG_ENABLED,1`), `ZONE_CHANGE,id,"Name",x`, `MAP_CHANGE,uiMapID,"Name",a,b,c,d` (a=yMin, b=xMin, c=yMax, d=xMax).
- Advanced block, 19 fields: infoGUID, ownerGUID, curHP, maxHP, AP, SP, armor, absorb, powerType, curPower, maxPower, cost, 2 unknown, **X, Y, uiMapID, facing, level**.
- **Likely layout discrepancy:** in real logs the advanced block most likely sits *between* the prefix params (spellId, name, school) and the suffix params (damage amount, overkill, ...). The haiku-extracted synthetic examples put the amount *before* the advanced block. The build agent was told to verify from the real log tail. Confirm that this was done.
- GUID prefixes are `Player-`, `Creature-`, `Pet-`, `Vehicle-`. The empty GUID is `0000000000000000`.
- Flags: bit 0x1 means "me", 0x10 friendly, 0x40 hostile, 0x20 neutral. The newest flags win.
- Names: strip the realm suffix (for example `-PvP-US`, auto-detected when at least 6 players share it) and replace `-` with a space.
- World axes: X is north, Y is west.
- Other players named on a line with no position of their own get "me"'s last fix as an *approximate* position, valid if the fix is under 120 s old.
- **CORRECTION (verified in game):** `MAP_CHANGE,id,"Name",a,b,c,d` is x-range = (a,b), y-range = (c,d), NOT (b,d)/(a,c) as the Python parser assumed. Confirmed on Thousand Needles: a fix at X=-6399, Y=-3853 reads 77.7 across in game. The Go parser is fixed (with tests). The UI has a fixed 0-100 map frame (top-left 0,0; bottom-right 100,100; Y from the top) with Shift+arrow per-map calibration.
- `maps\index.json`: `{"zones": {"<lowercase a-z0-9 key>": {"name","file","w","h","revealed"}}}`.

## 5. Intended architecture (go-radar)
```
cmd/radar/main.go          entry, flags, wiring, windowed (-H=windowsgui)
internal/parser            line -> Event (CSV, timestamp, advanced block, damage amounts)
internal/tailer            newest WoWCombatLog*.txt follower: rotation, truncation, partial lines, 8MB tail
internal/tracker           store, "me", reaction, expiry (5 min stale / 15 min gone), ApplyObservation
internal/history           persistent JSON (atomic write): avg damage, avg/max HP, counts, last pos; hidden in UI
internal/screenshot        connector ONLY: Observation, Reader, NoopReader, Poller (tested with a fake)
internal/server            http + SSE, /api/state, /api/history, embedded web/
internal/maps              index.json loader
internal/app               config and wiring
web/                       embedded HTML/JS/CSS radar canvas, list panel, hidden history panel
```
Pip colours: friendly blue, hostile red, neutral amber, stale dark grey. The history panel is hidden by default.

## 6. Work status
- [x] Survey of toolchain and project; user decisions collected.
- [x] Log-format spec extracted from the Python app (section 4).
- [x] **Build finished** (sonnet, medium effort). All packages from section 5 exist in `go-radar\` with a README and `build.ps1`. Verified by me: `go test ./...` passes, `go vet` and `gofmt` are clean, and `dist\radar.exe` (7.6MB) and `dist\radar-debug.exe` exist. The agent reported coverage of 87-100% on non-UI packages. `cmd/radar` and `web` have no tests. `-race` was not run (no gcc).
  - Real-log check, per the agent: layout verified, so the advanced block comes after the prefix and before the amount, and it describes the *info unit* (which can be the destination). A console run against the real log gave 4110 pips.
  - **Player search + tracks (added later):** `history` stores per-GUID tracks (dedupe 2 yd/5 s, cap 5000/player, 250000 total, compact v2 file, v1 loads), `tracker` passes exact and approx fixes with the log time, `server` has `/api/players` and `/api/track`, the UI has a search box in the side panel and draws the track dotted on the current map. Tested in Go; the JS was only syntax-checked and smoke-run in a stubbed DOM (goja), never rendered. See README "Player search and tracks".
  - **Bounty system (added later):** new `internal/bounty` (Analyzer: friendly-player deaths from UNIT_DIED/PARTY_KILL/SPELL_INSTAKILL, killing blow + 15 s assists, pet/totem owner credit, feign UNIT_DIED ignored; Store: JSON ledger `<history>-bounty.json`, idempotent by death id = hash(victim+ms+log file); Runner: streamed reverse analysis of old logs, one job at a time). Parser gained `Event.Unconscious`; the tailer publishes `Current()` as soon as a file is opened. Flags `--analyze-logs`, `--analyze-file`; API `/api/bounty`, `/api/bounty/player`, `/api/bounty/analyze` (POST needs `X-Radar` header or JSON content type). UI: Bounty panel + gold marks on the radar + Analyze logs button; JS smoke-run in goja only, never rendered. See README "Bounty system".
  - Build: `powershell -ExecutionPolicy Bypass -File build.ps1` (`-Debug` for the console build).
  - **Map picker (added later):** header combobox to view any map with a known rect + image (`view.available` / `view.noImage` in `/api/state`, built in `server.fillAvailable`; tested in `available_test.go`). UI-only `viewMapId` (null = auto/follow `S.mapId`, not persisted) now drives `curMap`, `fitActive`, `onMap`, `trackFixes`, calibration key, zoom reset, header; JS smoke-run in goja only, never rendered.
  - **Server rule-sets (added later):** new `internal/ruleset` (normal/pvp/rp/rppvp ids, parsing, realm-token detection from the log owner's raw name) and `internal/dataset` (Registry = history + bounty per rule-set, routes all reads/writes to the active one via an atomic pointer; lazy open; flush-all loop; settings `radar-settings.json`; one-time COPY migration of legacy `radar-history*.json`). Files `radar-history-<id>.json` / `-bounty.json`. Flag `--ruleset`, API `GET/POST /api/ruleset`, `view.ruleset` in `/api/state`, header segmented toggle + bounty-panel tag. UI JS smoke-run in goja only, never rendered. See README "Server rule-sets".
  - Also added: `--maps` flag, the `estimate` heuristic, and a loopback-only Host check on the server.
- [ ] **Remaining: the UI JS and the window contents are untested and have never been seen.** Launch `dist\radar.exe` against a real log and look at it. Known gaps: the WebView2-failure fallback to the browser was not exercised, the list panel shows at most 400 rows, multi-log "me" picks the most recently active source, and `orientation alt` is unverified.
- [x] (done, kept for reference) Review checklist:
  - `go test ./... -cover` passes (aim for at least 85% on non-UI packages).
  - `go vet ./...` and `gofmt -l .` are clean.
  - Tests were actually written first (the agent was told to).
  - The expiry boundaries are exactly 5 min and 15 min, and use the injectable clock.
  - The history is persisted and is not shown in the default UI.
  - No screenshot capture or OCR code exists. Only the connector.
  - `// MULTI-LOG:` comments are present.
  - `dist\radar.exe` builds, launches, and `/api/state` returns pips from a synthetic log.
- [ ] Verify the real WebView2 window renders. This has not been seen by anyone yet.
- [ ] Try a run against the real `_forever_\Logs` log.
- [ ] Fix any gaps, then update `go-radar\README.md`.

## 7. Future work (explicitly deferred by the user)
1. Screenshot reader behind `screenshot.Reader`: capture the WoW window (Win32 GDI, pure Go), blank everything except the minimap coordinate text and zone text, read digits with a calibrated glyph matcher, and return an `Observation`.
2. True multi-log simultaneous parsing: several tailers feeding one tracker, with a SourceID and per-source "me".
3. Optional NPC tracking and the weighted position estimate/heatmap from the Python app.
