# Stage 9: radar-client (desktop app for live log upload)

Read `../../PLAN.md`, `STATUS.md`, Results of stage 7 (`internal/feed` tailer, `cmd/radar-feed`), stage 5 (`/v1/stream`, auth, summary)
and `../../../go-radar/cmd/radar/{main.go,window_windows.go}` (how the Go app opens a WebView2 window with a browser fallback).
Work in `cmd/radar-client`, `internal/client/` (+ `web/` embedded UI), `scripts/` (build only), docs.

## What the user asked
"A client application that users will use to live upload logs whenever the WoW combat log file is updated with new information:
let users set the file and click a start button."

## Behaviour
- A small windowed app (Windows first; the same code must also build for Linux with the browser fallback). One `.exe`, no installer,
  no cgo, no runtime to install (WebView2 is present on Windows 11; fallback = default browser at a 127.0.0.1 port).
- UI (embedded HTML/JS/CSS, same dark theme tokens as the page): fields **Server address** (ingest URL), **Ingest key** (password
  field), **Rule-set** (Auto / Normal / PvP / RP / RP-PvP), **Log file** (text box + **Browse...** native file dialog; also a
  **Use newest log in folder** option that follows the newest `WoWCombatLog*.txt` in the chosen folder, including rotation),
  **Start** / **Stop** buttons, and a live status panel: state (idle / connecting / uploading / waiting for new data / error with
  reason), lines sent, bytes, lines/s, last event time, last server reply, reconnect countdown, and a scrolling short event log.
- **Start** begins at the end of the file by default (option "Include existing content" to send the whole file first, with a bounded rate).
  Uploads whenever the file grows (tail by polling size/mtime every 500 ms, partial lines held back, truncation/rotation handled),
  streaming to `POST /v1/stream` (reuse `internal/feed`); reconnects with backoff; Stop closes the stream cleanly and shows the
  server's summary.
- **Secrets rule (user requirement, see `docs/security/secrets-policy.md`):** the ingest key is NEVER written to disk or the repo.
  It is read from the env var `RADAR_CLIENT_INGEST_KEY`; if unset the UI asks for it and keeps it in memory for the run only (no
  "remember key"). Non-secret settings (server URL via `RADAR_CLIENT_SERVER` default, log path, rule-set, options) persist in
  `%APPDATA%\RadarClient\settings.json` (outside the repo); a test asserts the saved file never contains the key. Closing the window
  stops the upload.
- Local UI server: 127.0.0.1 on a random port only, Host-header check, per-run random token required on every API call (CSRF/other
  local users), no CORS, strict CSP, textContent only. The key never leaves the process except to the configured server (warn in the UI
  if the server address is plain http to a non-private address).

## Deliverables
1. `internal/client`: `Settings` (load/save/validate), `Session` (state machine around `internal/feed`: Start/Stop/Status with an injected
   clock and injected uploader for tests), `UIServer` (JSON API: GET/PUT settings, POST start/stop, GET status long-poll or SSE,
   POST pick-file calling an injected `FilePicker`), `FilePicker` interface with a Windows implementation (pure Go, COM/`comdlg32`
   `GetOpenFileNameW` via `golang.org/x/sys/windows`) and a Linux/no-op fallback that lets the user type the path.
2. `cmd/radar-client`: main + window_windows.go (WebView2 via `github.com/jchv/go-webview2` as in go-radar, build tag windows; browser
   fallback elsewhere or on failure) + flags `--no-window`, `--port`, `--settings`.
3. Tests (TDD): settings round trip + validation + permissions; session state transitions with fake uploader/clock (start at end,
   include existing, growth triggers upload, partial line held, rotation, truncation, server error -> backoff -> reconnect, stop);
   UIServer httptest (token required, Host check, bad input, start/stop flow, status shape); embedded UI JS logic with `node --test`
   (status rendering, form validation); an integration test streaming a temp log into a real in-process ingest host
   (memstore + relay test db handler as in `cmd/radar-ingest/e2e`).
4. `scripts/build.ps1`: add `radar-client.exe` (windows/amd64, `-H=windowsgui`) next to the others; checksums.
5. Docs: `docs/client.md` user guide (screenshots not required): how to get a key, set server, pick the log, Start; troubleshooting
   (path with spaces, Advanced Combat Logging must be on, firewall, wrong key -> 401 message). Top-level README section.

## Done when
gofmt/vet/tests green, coverage >= 85% on internal/client, `go build` for windows/amd64 and linux/amd64 (CGO off) OK, launching the
exe with `--no-window` serves the UI on 127.0.0.1 and a manual smoke (documented) uploads a synthetic log to a locally running
radar-ingest + radar-db; Results appended, STATUS.md line added.

## Results (Stage 9 complete)

**Built.** `internal/client` (settings, keystore, progress, session, picker, UI server + JSON API, embedded `web/`), `cmd/radar-client`
(`main.go`, `ui.go`, `window_windows.go` WebView2, `window_other.go` browser fallback), `internal/feed` additions, `scripts/build.{ps1,sh}`
(radar-client windows/amd64 with `-H=windowsgui -s -w` and linux/amd64, in `SHA256SUMS`), `docs/client.md`, README section. `go.mod`: added
`github.com/jchv/go-webview2` (same version as go-radar) and `golang.org/x/sys` (now direct); CGO stays off.
gofmt/vet clean; all packages green. Coverage: internal/client 87.0% (the Windows dialog call is the untested part), cmd/radar-client 55.9%
(window/message-box code cannot run headless), internal/feed 95.6%. Web UI: 33 `node --test` tests (`web/test/*.test.mjs`: logic, plus the page
glue against a fake DOM, including hostile text rendered literally); a Go test (`TestWebUILogicWithNode`) runs them when node is installed.
Integration test: real `Session` + `feed.Streamer` into an in-process radar-ingest (same modules as the binary) relaying to a memstore db handler.

**Decisions / deviations**
- `feed.Streamer` got an optional `StreamerConfig.Observe func(Event)` (Connecting, Sent{Lines,Bytes}, Ended{Status,Body,Err}, Backoff{Wait}); nil keeps the old
  behaviour, three new tests. The client builds all status (state, rates, countdown, server summary on Stop) from these events.
- Status is polled by the page every second (`GET /api/status`) instead of long-poll/SSE; the countdown and rates change every second anyway.
- Single files are followed through the tailer with an escaped glob (`[x]` classes) so names with `[`, `*`, `?` work; "follow newest" accepts a folder or any file in it.
  "Browse..." always picks a file (GetOpenFileNameW); there is no folder dialog.
- "Last event time" in the status is the time of the last upload (the client does not parse events).
- 401, 403, 404, 409, 429 and network failures are translated to plain sentences; the client keeps retrying with backoff (a 401 is shown, not fatal).
- Server address with user:password is rejected (net/http would echo it in errors). Plain http to a non-private host only warns, in the page.
- Quit button exists (hidden when the program has no Quit hook); it is how the browser fallback is closed. Windowed exe `--no-window` still prints the UI address to stdout.
- Upload cap 256 KiB/s and 200 000 queued lines (same as radar-feed). Source id = `<computer name>-client`.
- The web page's token travels in the opening URL (`/?t=...`) and as `X-Radar-Token` on API calls; static assets need no token.
- Smoke (documented in docs/client.md) first hit a 405 because radar-db's public listener no longer serves `/internal/*` (task 8.2); the smoke uses `--internal-listen`.
  Result: 16 lines / 1827 bytes uploaded, stop reply "16 lines, 14 events, 3 players seen, 2 lines not understood", 3 contacts in the db, no key in settings or output.
- Not done: screenshots; the native dialog was compiled and the window opened ("Radar Client" title) but the dialog itself was not clicked through; Linux binary built, not run on Linux.
