# radar-client user guide

`radar-client` uploads your World of Warcraft combat log to a radar-ingest server while you play. You choose the log file,
click **Start**, and every new line is sent as the game writes it. Nothing is installed; it is one program.

## What you need

1. A radar-ingest server address, for example `http://192.168.1.20:8788`, from whoever runs the server.
2. An **ingest key** from the same person. On the server it is shown once by `install.sh` (or made with `radar-db keygen`).
3. Combat logging switched on in WoW: type `/combatlog` in game, and turn on **Advanced Combat Logging**
   (Options, Network). Without the advanced option the log has no positions and the radar has nothing to show.

## Install and start

Copy `radar-client-windows-amd64.exe` anywhere (the build script puts it in `dist\`) and double-click it. A window opens.
On Windows 11 the window uses WebView2, which is already installed. If it cannot start, your default browser opens the same
page instead; use the **Quit** button there to end the program.

Linux: run `./radar-client-linux-amd64`; it prints the page address and opens the browser. Add `--no-window` to only print it.

### The key

The key is never written to disk. Either:

- set the environment variable before starting the program (PowerShell: `$env:RADAR_CLIENT_INGEST_KEY = "<your key>"; .\radar-client-windows-amd64.exe`), or
- type it into the **Ingest key** field. It stays in the program's memory until you close it; there is no "remember key".
  Next time you start, type it again (or use the variable).

There is no `--key` option (command lines show up in process lists): `radar-client --key ...` stops with a message naming
`RADAR_CLIENT_INGEST_KEY`. Tests: `TestKeyNeverAppearsInAnyReply`, `TestSecretFlagsAreRefusedWithAPointerToTheEnvVar`, `TestPutSettingsValidatesSavesAndNeverStoresTheKey`.

The other settings (server address, log path, rule-set, options) are saved in `%APPDATA%\RadarClient\settings.json`.
`RADAR_CLIENT_SERVER` supplies the default server address when nothing is saved yet.

## Using it

| Field | Meaning |
|---|---|
| Server address | the radar-ingest address, `http://host:8788` or `https://...` |
| Ingest key | password field; empty is fine when the environment variable is set |
| Rule-set | Auto (the server reads it from the realm in the log), Normal, PvP, RP or RP-PvP |
| Log file + Browse... | the `WoWCombatLog*.txt` file; **Browse...** opens the normal Windows "Open" dialog (on Linux type the path) |
| Use newest log in folder | follow the newest `WoWCombatLog*.txt` of the folder; picks up the new file when WoW starts a new log |
| Include existing content | send the whole file first (at a limited speed); by default only new lines are sent |

Click **Start**. By default the client begins at the end of the file, so only what happens from now on is uploaded.
**Stop** (or closing the window) ends the upload cleanly and shows the server's summary.

The status panel shows the state (Stopped, Connecting, Uploading, Waiting for new data, Error with the reason), lines and
bytes sent, lines per second, the time of the last upload, the server's last reply, a countdown to the next reconnect and a
short scrolling event log. If the server is unreachable the client keeps reading the log (up to 200 000 lines, oldest dropped)
and reconnects with a growing delay (0.5 s up to 30 s, with a little random jitter). The wait is longer when the server
answers 429 (at least 30 s, up to 15 min, following its Retry-After header) or 409 (10 s up to 2 min). A refused key (401/403)
stops the upload at once instead of retrying.

Uploads are at most once: lines already written to a connection that then breaks are not sent again, because the server adds
counters and a repeat would count twice.

## Troubleshooting

| Symptom | Fix |
|---|---|
| "The server rejected the ingest key (401). The upload stopped..." | the upload stopped and does not retry. Check that the key has no stray spaces or line breaks (they are trimmed, but a wrong or truncated paste is not), type the right key and click Start again. No restart is needed |
| "The server is limiting this address (429); waiting N s" | the server temporarily blocked this address, usually after repeated wrong keys. Wait; the client retries by itself (30 s or more). On the server run `radar-db guard list` to see the ban and `radar-db guard unban <ip>` to lift it |
| "Cannot reach the server" | check the address and port, that the server is running, and the firewall of the server (port 8788) and of this PC |
| "The server answered 404" | the address points to the web page (port 8787) instead of the ingest server (8788) |
| "Another uploader is already sending with the same source name (409)" | another radar-client or radar-feed uses the same source name (computer name); close it or change the source. The client waits 10 s or more between attempts |
| Status stays "Waiting for the log file" | the folder or file does not exist, or `/combatlog` is off so WoW has not created a log |
| Uploading, but the page shows nothing | Advanced Combat Logging is off, or the rule-set differs from the one the page shows |
| Path with spaces | fine; paste it as is. Quotes from Explorer's "Copy as path" are removed automatically |
| "plain http to a non-private host" warning | the key and log travel unencrypted over the internet; use an https address (Cloudflare Tunnel / Caddy, see deploy/README-xubuntu.md) |

## Safety design

- The UI server listens on `127.0.0.1` only, on a random port (or `--port`).
- Every API call needs a random per-run token; the page address that radar-client opens contains it. The Host and Origin
  headers are checked (against DNS rebinding and other web pages), there is no CORS, the Content-Security-Policy allows only the
  page's own files, and all text from the server is shown with `textContent`. The page makes no request to any other address.
- The key goes only to the configured server (`X-Ingest-Key`). It is not in the settings file, the status, the event log or
  any console output; the program has no command-line option for it, on purpose.

## Command line

| Flag | Default | Meaning |
|---|---|---|
| `--port` | 0 (free port) | port of the local UI |
| `--no-window` | off | do not open a window or a browser; print the address (`radar-client UI: http://127.0.0.1:PORT/?t=TOKEN`) |
| `--settings` | `%APPDATA%\RadarClient\settings.json` | settings file |
| `--version` | | print the version |

## Build

`powershell -ExecutionPolicy Bypass -File scripts\build.ps1`, run from inside the `radar-live` folder (or `sh scripts/build.sh`), writes `radar-client-windows-amd64.exe`
(`-H=windowsgui`, no console window) and `radar-client-linux-amd64` into `dist\` with `SHA256SUMS`. Both are static, CGO off.

## Smoke test (real binaries)

This proves an upload end to end on one machine. Use throwaway keys and a synthetic log (never a real one).

1. Build: `go build -o bin/ ./cmd/radar-db ./cmd/radar-ingest ./cmd/radar-client` (`bin/` is not tracked).
2. Make two random strings for the keys. Start the database with `RADAR_INGEST_KEYS=<ingest key>` and `RADAR_VIEW_KEYS=<view key>`:
   `radar-db --listen 127.0.0.1:18787 --internal-listen 127.0.0.1:18786 --db-path <temp>/r.db`
3. Start ingest with the same `RADAR_INGEST_KEYS`, `RADAR_VIEW_KEYS`, plus `RADAR_DB_KEY=<ingest key>`:
   `radar-ingest --listen 127.0.0.1:18788 --db-url http://127.0.0.1:18786 --contactx-flush 200ms`
   (the flags for the internal listener follow the security work in `deploy/`; check `radar-db --help` if they changed).
4. Create an empty file `<temp>/WoWCombatLog.txt`. Start `radar-client --no-window --port 18790 --settings <temp>/settings.json` with
   `RADAR_CLIENT_INGEST_KEY=<ingest key>` set; it prints `radar-client UI: http://127.0.0.1:18790/?t=<token>`.
5. Start the upload (what the Start button does), using the token from step 4:
   `POST http://127.0.0.1:18790/api/start` with header `X-Radar-Token: <token>` and JSON
   `{"server":"http://127.0.0.1:18788","ruleset":"pvp","logPath":"<temp>/WoWCombatLog.txt","followNewest":false,"includeExisting":false}`.
6. Append the lines of `internal/mod/logparse/testdata/session.log` to the log file with their date/time replaced by the current UTC
   time written as `M/D/YYYY H:MM:SS.mmm+0` (so they count as fresh).
7. `GET /api/status` shows `linesSent` rising. `POST /api/stop` returns the server's summary in `lastReply`.
8. `GET http://127.0.0.1:18787/api/v1/contacts?rs=pvp` with header `X-View-Key: <view key>` lists the three synthetic players.
9. Check that `<temp>/settings.json` and the console output contain neither key.

Result on the development machine (Windows 11, built binaries): 16 lines / 1827 bytes uploaded, stop reply "16 lines, 14 events,
3 players seen, 2 lines not understood", three contacts in the database, no key in the settings file or the output.
