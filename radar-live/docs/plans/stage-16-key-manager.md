# Stage 16: radar-keys, the API key manager (plan)

Status: built (K1 to K7), see "Results" at the end. Plan written 2026-11-13.

## Goal
A small program on the laptop (the radar-ingest host) that creates, lists, edits, blocks and deletes ingest keys, one per player,
with the keys taking effect **without restarting any service**. Each key carries an email, an optional player name, optional tags
and its own rate limits.

## Today (what the code does now)
- Keys are a static list read once at start (`internal/mod/keys`): `RADAR_INGEST_KEYS` / `RADAR_INGEST_KEYS_FILE`, hashed with
  SHA-256 in memory, compared in constant time. A key has no identity: no owner, no label, no off switch except editing the
  env file and restarting radar-ingest.
- Rate limits are global. `streamapi` keeps one shared bucket per key, keyed by the first 16 bytes of the key's SHA-256
  (`keyID`), but every key gets the same numbers (`stream-key-line-rate` 20000/s, `stream-key-byte-rate` 16 MiB/s,
  `stream-max-per-key` 4 streams).
- Only radar-ingest authenticates the players' keys. radar-armory's `RADAR_ARMORY_DB_KEY` is a separate ingest key on
  radar-db's internal listener; it stays in `radar.env` and is out of scope here.

## Design decisions
1. **Source of truth is a keyring file, not a network API.** `/var/lib/radar-live/keyring.json`, mode 0600, owner = the radar
   service user. It holds only the SHA-256 of each key, never the key. This adds no listener and no new attack surface, and it
   fits the "weak laptop, no Docker" setup.
2. **radar-ingest hot-reloads it.** The `keys` module polls the file (size + mtime, every 2 s), parses and validates it, and
   swaps the active set atomically. A broken file is logged and ignored; the previous set stays active. No restart.
3. **Two single-writer files.** The manager writes `keyring.json`. radar-ingest writes `keyusage.json` (last seen, last remote
   IP, lines and bytes today, every 60 s). The manager reads both. No write conflicts, no locks between processes.
4. **Manager writes atomically** (temp file, fsync, rename) under an advisory file lock so two manager instances cannot lose
   each other's edits.
5. **A key's id is the same `keyID` streamapi already uses** (first 16 bytes of the SHA-256, hex). So limits and "blocked" can
   be looked up from a request without storing the raw key anywhere.
6. **The old env keys keep working.** Keys in `RADAR_INGEST_KEYS` show in the list as read-only "legacy (env)" rows with the
   default limits and cannot be blocked from the app. Migration is optional: add a keyring entry, remove the env entry.
7. **UI is a loopback web page, like radar-client.** Xubuntu is the target, so a browser window rather than a native toolkit:
   a Go binary with embedded static files, listening on 127.0.0.1 with a per-run token and the same CSP and Host/Origin
   checks as `internal/client/server.go`. The same binary has CLI subcommands for scripts and SSH.

## Data model (`keyring.json`, version 1)
```json
{ "version": 1, "keys": [ {
  "id": "9f2c...(32 hex)", "hash": "sha256 hex", "email": "player@example.com",
  "player": "Arthas-Faerlina", "tags": ["guild:ashes", "tester"], "notes": "free text",
  "createdAt": "2026-11-13T10:00:00Z", "expiresAt": null,
  "disabled": false, "disabledReason": "", "disabledAt": null,
  "limits": { "lineRate": null, "lineBurst": null, "byteRate": null, "byteBurst": null, "maxStreams": null }
} ] }
```
`null` means "server default". Extra fields I added beyond your list, for day-to-day use: **notes**, **expiresAt** (key stops
working on its own), **disabled reason**, **max concurrent streams**, **burst** values, and the read-only usage columns. Not
in this file: the key itself. It is generated (32 random bytes, base64url, the same as `radar-db keygen`) and shown in the create
window with a Copy button. It can be viewed again with the eye button only if it was kept in the vault (see "Revealing a key
later"). **Regenerate** replaces the hash (and the vault copy) and shows the new key.

## Revealing a key later (eye button)
A hash cannot be turned back into the key, so "reveal" needs the manager to keep a recoverable copy. This is a deliberate step
back from "hashes only", so it is kept apart from what radar-ingest reads:
- `keyring.json` (read by radar-ingest) still holds **only hashes**. A leak of the server process or its files never exposes keys.
- A separate **vault** `/var/lib/radar-live/keyvault.bin` (mode 0600, readable by the manager's user only, **not** by the
  radar-ingest service user) holds each raw key sealed with AES-256-GCM. The sealing key is a random 32-byte file
  `/var/lib/radar-live/keyvault.key` (0600, same owner), never inside the vault file or a backup of it. Backups include the
  vault but not the sealing key (documented: restore needs both).
- Main screen: each row has an **eye button**. Click: the masked key (`abcd...wxyz`) turns into the full key for **10 seconds**
  (then hides again), with a Copy button. The reveal is one `GET /api/keys/{id}/reveal` call with the page token; the key is
  returned only in that reply, with `Cache-Control: no-store`, and is never in the list reply, logs, metrics or the audit log.
  Each reveal writes an audit line (key id, time, no key) to the manager's log.
- Keys created before the vault existed, legacy env keys and keys created with the CLI flag `--no-vault` have **no** recoverable
  copy: the eye is greyed out ("not stored, use Regenerate"). Regenerate makes a new key and shows it.
- The create window gets a checkbox **Keep a recoverable copy so I can view it later** (on by default, it can be turned off per
  key for people who want a one-time key).
- Honest limit: anyone who can act as the manager's user on the laptop can read the vault and the sealing key. The sealing
  protects the files at rest (backups, a stolen disk image without the key file), not a compromised account.

## Screens
**Main screen**: a table of every key, newest first, with search and a tag filter.
Columns: Enabled (checkbox), Key (masked, with the eye button), Email, Player, Tags, Created, Last seen, Lines today, Rate limit (effective, "default" or custom),
Status (active / blocked / expired / legacy), Actions (Edit, Regenerate, Delete). Buttons: **New key**, Refresh, Export CSV
(without keys). The checkbox is the block switch: unchecking asks for an optional reason and takes effect within about 2 s,
including for streams that are already open.

**New key window** (also used by Edit): Email (required, validated), Player name, Tags (comma or Enter), Notes, Expires,
**Rate limits** (lines/s, burst, bytes/s, max simultaneous streams; each blank = default, with the default value shown as
placeholder text and a sanity maximum), a Create button. After creating: a one-time reveal of the key with Copy and a hint on
where the player puts it (radar-client "Ingest key" field, or `RADAR_FEED_KEY`).

## Server changes (small, behind existing interfaces)
1. `keys` module: add the keyring source next to the env source, hot reload, per-key `id` kept with the hash. Authentication
   returns the matched entry (id, limits, disabled/expired) through the request context.
2. A disabled or expired key answers **403** `key_disabled` (the client already stops and shows a different message for 403
   than for 401). Unknown key stays 401.
3. `streamapi`: when it creates the per-key state it asks the keyring for that key's limits and uses them instead of the
   global key defaults. Limits are re-read on reload, so a changed rate applies to the next charge. Open streams of a key that
   became disabled are ended (reason `blocked`) at the next reload tick.
4. A `keyusage.json` writer in `streamapi` (counters per key id; no line content, no emails).
5. Startup policy unchanged: `require-keys` counts keyring keys and env keys together.

## Security notes
- Emails are personal data. They live only in `keyring.json` (0600), never in logs, metrics, the audit log, API replies or
  anything radar-ingest sends over the network. Logs use the key id only.
- radar-ingest authenticates with the SHA-256 in `keyring.json`, like today. 256-bit random keys need no slow hash. The raw
  keys exist only in the manager's vault (see "Revealing a key later").
- The manager UI binds to 127.0.0.1 only, needs the per-run token, refuses foreign Host/Origin, no CORS.
- Deleting a key deletes its entry; a backup of `keyring.json` is included in the nightly backup set and is as sensitive as
  `radar.env`.
- Limits are validated (non-negative, upper bounds) on both the manager and the server, so a hand-edited file cannot disable
  limiting by accident (0 still means "off" in the existing settings, so the manager will refuse 0 for per-key limits and use
  blank for "default").

## Build order
| Step | Work | Done when |
|---|---|---|
| K1 | `internal/keyring`: types, validation, load/save, atomic write + lock, key and id generation; `internal/keyvault`: AES-GCM sealed store, sealing-key file | round-trip, bad-file, concurrent-writer, wrong-sealing-key and tamper tests pass |
| K2 | `keys` module reads the keyring with hot reload; 403 for disabled/expired | tests: add, disable, enable, expire, corrupt file keeps the old set |
| K3 | `streamapi` per-key limits and end-open-stream-on-block | tests: two keys with different rates, a blocked key's open stream ends |
| K4 | `keyusage.json` writer | counters per id, no PII in the file |
| K5 | `cmd/radar-keys`: CLI (`list`, `add`, `block`, `unblock`, `delete`, `regen`) | CLI tests |
| K6 | Web UI (main table, New key/Edit window, eye button with 10 s auto-hide, search/filter) | node tests with the fake DOM as in radar-client; Go tests: reveal needs the token, the list reply never contains a key, no-store header, audit line without the key |
| K7 | install.sh / uninstall / backup / verify-hardening / docs / e2e | e2e: create key in the manager, upload with it, block it, upload is refused, unblock, upload works again, no restart |

## Open questions for you
1. Should a key ever be a **view** key (for the web page) too, or only ingest keys? The plan covers ingest only.
2. Should expired and deleted keys be kept in a history (who and when), or simply removed?
3. Hard default caps for per-key limits (so one entry cannot exceed what the laptop handles)? I suggest the manager refuses
   values above the current global per-key defaults unless you tick an "allow above default" box.

---

# How the ingestion client sends logs (answer to your question)

This is `radar-client` (the desktop app) and `radar-feed` (the command line); both use the same engine, `internal/feed`.
It is **not** a fixed upload interval. It is one long-lived streaming connection that sends lines as they appear.

1. **Reading the log.** The follower checks the combat log (or the newest `WoWCombatLog*.txt` in the folder) every **500 ms**.
   It sends only complete lines (ending in a newline). On start it begins at the end of the newest log, after replaying that
   log's last `ZONE_CHANGE` / `MAP_CHANGE` so the map is known. "Include existing content" / `--from-start` sends the file's
   existing content first. It follows log rotation and truncation.
2. **Queue.** Lines go into a bounded in-memory queue of **200,000 lines** (`--queue-lines`). If the server is unreachable and
   the queue fills, the **oldest** lines are dropped, never memory.
3. **Connection.** The first queued line opens **one HTTP POST** to `/v1/stream?source=<host>&rs=<ruleset>` with the header
   `X-Ingest-Key`, as a single chunked request that stays open. It sends `Expect: 100-continue`, so a bad key or a busy source
   is refused before any log data leaves the computer.
4. **Sending.** The writer takes everything currently in the queue, up to **32 KiB per write** (or a quarter second of the byte
   cap, whichever is smaller), writes it to the open connection and immediately takes the next batch. So lines go out within
   roughly the 500 ms poll of being written to the log, continuously while you play. The upload speed cap is **256 KiB/s** by
   default (`--max-bytes-per-sec` on `radar-feed`, 0 = unlimited; the desktop client uses the same fixed 256 KiB/s).
5. **Quiet periods.** With nothing to send, no connection is opened. The server closes a stream that sends nothing for
   **120 s** (`stream-idle`); the client simply opens a new one at the next line.
6. **Server side limits.** Per source 5,000 lines/s and per key 20,000 lines/s by default. When over the limit the server stops
   reading (the sender slows down through TCP). If the flood lasts longer than the **30 s** patience, the stream is ended with
   `rate`.
7. **Failures and retry.** Reconnect waits are 0.5 s growing to 30 s with ±20% jitter for network errors; 10 s to 2 min after
   409 (source already in use); at least 30 s up to 15 min after 429 (honouring `Retry-After`). A 401 or 403 stops the
   client for good and shows that the key was refused.
8. **Delivery guarantee.** At most once. Lines never written stay queued and are retried. Lines already written to a connection
   that then breaks are **not** resent, because the server adds counters and a replay would double count.
9. **Transport.** Plain HTTP unless the server address starts with `https://`; the client warns when a non-private host is
   used over plain HTTP, because the key and the log would be readable on the way.

---

# Results (K7, 2026-11-13)

**Built.** `internal/keyring`, `internal/keyvault`, the `keys` module keyring source with 2 s hot reload and 403 `key_disabled`, per-key limits and the
`blocked` end reason in `streamapi`, the `keyusage.json` writer, `cmd/radar-keys` (CLI + loopback web UI), and in K7 the end-to-end test, the deploy
changes and the guide `docs/keys.md`.

**Files layout (deviation from the plan, which put everything in `/var/lib/radar-live`).** radar-ingest runs sandboxed as `radar`, radar-keys as root, and the
vault must be unreadable by the service. So one parent `/var/lib/radar-keys` (root 0755) with three directories: `keyring/` (root:radar 0750,
`keyring.json` 0640 root:radar), `usage/` (radar 0700, `keyusage.json` written by radar-ingest, which gets exactly this one `ReadWritePaths`) and
`vault/` (root 0700, `keyvault.bin` + `keyvault.key`). The data directory of radar-db is untouched. radar-keys defaults match; a custom `--keyring` keeps
everything next to it (tests and Windows).

**Other changes and deviations.**
- `keyring.WriteFileAtomic` now keeps the mode and (when privileged) the owner of the file it replaces. Without this, root's rewrite of `keyring.json`
  produced a root:root 0600 file that the service user could not read.
- `radar-ingest.service` gained `ReadWritePaths=/var/lib/radar-keys/usage`; `deploy_test.go` and `verify-hardening.sh` now expect exactly that instead of "no writable path".
- New `deploy/backup-keys.sh` (installed as `/usr/local/sbin/radar-keys-backup`, root line in `backup.cron`): backs up `keyring.json` and `keyvault.bin`, never `keyvault.key`.
- install.sh sets `RADAR_KEYRING_FILE` / `RADAR_KEYUSAGE_FILE` (fresh file: from the example; existing file: appended only when absent) and creates an empty keyring once.
  The vault paths are deliberately not in `radar.env`.
- build.sh / build.ps1 build `radar-keys` for linux amd64 and arm64 and ship `keys.md`; SHA256SUMS covers them (the build copies `deploy/*`, so `backup-keys.sh` too).
- The e2e (`test/e2e/keys_test.go`) turns the per-address rate limiter and the abuse lockout off for radar-ingest because it polls faster than a player would. The usage
  check waits for the 60 s periodic flush on Windows (no way to send an interrupt to a child); on Linux it stops radar-ingest gracefully and reads the final flush.

**Open items.**
- Legacy env keys are not shown as read-only rows in the radar-keys list (the plan's decision 6); they work, and `docs/keys.md` says they are not listed.
- No Export CSV button in the UI (listed on the plan's main screen).
- radar-db reads the same `radar.env`, so it also loads the keyring (a player key would be accepted on its loopback-only internal listener). Harmless as deployed, but worth
  a per-program setting if the internal listener is ever exposed.
- The flush interval of `keyusage.json` (60 s) is not configurable from a binary.
- Not run for real: install.sh, uninstall.sh, backup-keys.sh and verify-hardening.sh (only `sh -n` and pattern tests; they need a Linux host with systemd).
- The plan's three open questions (view keys, history of deleted keys, caps above the defaults) are unchanged: ingest keys only, deletes leave no history, caps are the hard maxima in `internal/keyring`.
