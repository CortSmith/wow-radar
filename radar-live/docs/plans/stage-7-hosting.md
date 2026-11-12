# Stage 7: feeder, Xubuntu packaging, e2e smoke, docs

Read `../../PLAN.md` section 1 (hosting constraint), `STATUS.md`. Work in `cmd/radar-feed`, `deploy/`, `scripts/`, `README.md`, `test/e2e`.

## Deliverables
1. `cmd/radar-feed` (runs on the gaming PC, Windows or Linux, static): finds the newest `WoWCombatLog*.txt` in `--logs`
   (default `C:\Program Files (x86)\World of Warcraft\_forever_\Logs`), tails it (rotation, truncation, partial lines, start at end
   unless `--from-start`), streams new lines to `--ingest-url` `/v1/stream` with `X-Ingest-Key`, reconnects with backoff, sends no
   more than `--max-bytes-per-sec`. Reuse ideas from `../go-radar/internal/tailer` (rewrite + tests). Tests with temp files.
2. `scripts/build.ps1` and `scripts/build.sh`: cross-compile `radar-db`, `radar-ingest` (linux/amd64 + arm64) and `radar-feed`
   (windows/amd64, linux/amd64), `CGO_ENABLED=0 -trimpath -ldflags "-s -w"`, write `dist/` + `SHA256SUMS`.
3. `deploy/`: `install.sh` (idempotent; creates user `radar`, `/var/lib/radar-live`, `/etc/radar-live/radar.env` mode 0640, installs
   binaries to `/usr/local/bin`, units, enables them, prints generated keys once), `radar-db.service`, `radar-ingest.service`
   (basic sandboxing; Stage 8 hardens further), `radar.env.example`, `uninstall.sh`, `backup.cron` example, `README-xubuntu.md`
   (copy the dist files over scp, run install.sh, open the page on the LAN, optional Cloudflare Tunnel / Caddy for remote access).
   Tuned for a weak laptop: `GOMAXPROCS=2`, `GOMEMLIMIT=96MiB`, SQLite cache 16 MB.
4. `test/e2e`: Go test that starts radar-db and radar-ingest binaries (built in test) on random ports with temp dirs, streams a
   synthetic log via the feeder code path, and asserts the page's sweep API returns the contacts; a measure of RSS after load.
5. Top-level `README.md` for radar-live (what it is, 5-minute quickstart for the laptop, configuration table of every flag/env, API
   summary, architecture diagram, module list and how to remove one) and update `PLAN.md` status table.

## Done when
All tests green, linux binaries build, scripts shellcheck-clean if shellcheck exists (else reviewed by eye), Results appended,
STATUS.md line added.

## Results (Stage 7 complete)

**Built.** `internal/feed/tailer` (rewrite of go-radar's tailer: newest file, rotation, truncation, partial lines, CRLF, start at end
with replay of the last `ZONE_CHANGE`/`MAP_CHANGE`), `internal/feed` (bounded drop-oldest `Queue`, byte-rate `Limiter`, `Streamer`),
`cmd/radar-feed`, `scripts/build.ps1` + `build.sh`, `deploy/` (`install.sh`, `uninstall.sh`, `radar-db.service`, `radar-ingest.service`,
`radar.env.example`, `backup.cron`, `README-xubuntu.md`), `test/e2e`, top-level `README.md`, `.gitignore`, `.gitattributes`, PLAN.md state column.
gofmt/vet clean; all tests green. Coverage: feed 94.9%, tailer 91.3%, cmd/radar-feed 81.9%. `build.ps1` run here produced
linux amd64/arm64 (db, ingest), windows+linux amd64 (feed), all ELF/PE static, ~7-12 MB each, plus `SHA256SUMS`.

**Key wiring.** ingest binary lists `keys`, so one shared ingest key works: `RADAR_INGEST_KEYS` (checked by both servers), `RADAR_DB_KEY`
(relay -> db), feeder sends `X-Ingest-Key`; the view key is separate. Both units read one `/etc/radar-live/radar.env`; listen addresses come from
`RADAR_DB_LISTEN` (`:8787`) and `RADAR_INGEST_LISTEN` (`127.0.0.1:8788`) via `ExecStart=... --listen ${...}`. Ingest -> db is `http://127.0.0.1:8787`
(public listener); the units carry TODO comments for the Stage 8.2 internal listener.

**Decisions / deviations**
- Feeder opens the connection lazily (on the first line) with `Expect: 100-continue`, so a rejection (401, 409 source_busy) arrives before any line
  is sent and nothing is lost; lines already written to a connection that breaks are not resent (at most once; ingest deltas are additive).
- A log file that appears after the feeder started is read from its top; only a file present at the first poll is entered at its end.
- Feeder defaults: `--max-bytes-per-sec` 262144, `--queue-lines` 200000, key via `--key-file` / `RADAR_FEED_KEY` (a `--key` flag exists but is documented as less safe).
- e2e: real feeder process, log timestamps rewritten to "now" with an explicit `+0` offset; garbage fixture lines are kept. Names are not asserted
  (realm-suffix fix owned by another agent); GUIDs of the three players are. RSS is read from `/proc` and only logged (Linux; "not measured" on Windows).
  `-short` skips the e2e (it compiles three binaries, ~8 s warm).
- Root `.gitignore` ignores `*.log`, which silently excluded `internal/mod/logparse/testdata/session.log`; `radar-live/.gitignore` un-ignores it.
- shellcheck is not installed here; `install.sh`, `uninstall.sh`, `build.sh` pass `dash -n` and were read twice. `install.sh` was not executed (needs a Linux root).
- Not done: Linux packaging was not run on a real Xubuntu machine; Windows service wrapper for the feeder (plain exe only).
