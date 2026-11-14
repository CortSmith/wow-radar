# radar-keys: one ingest key per player

`radar-keys` creates, lists, edits, blocks, unblocks, regenerates and deletes the keys players use to send their combat log to
radar-ingest (`X-Ingest-Key`, `RADAR_FEED_KEY`, the "Ingest key" field of radar-client). Changes reach radar-ingest within about 2
seconds and **no service is restarted**. It runs on the laptop that runs radar-ingest, as root (`sudo`). Design and the reasons for
each choice: `docs/plans/stage-16-key-manager.md`.

## Quick start

```sh
sudo radar-keys                                   # the web page (see below)
sudo radar-keys add --email player@example.com --player Arthas-Faerlina --tag guild:ashes
sudo radar-keys list
sudo radar-keys block 9f2c4a --reason "left the guild"     # an id, or a unique prefix of 6+ characters
sudo radar-keys unblock 9f2c4a
sudo radar-keys regen 9f2c4a                      # replaces the key; the old one stops working
sudo radar-keys delete 9f2c4a
sudo radar-keys reveal 9f2c4a                     # prints the key again (only if a vault copy exists)
```

`add` and `regen` print the **new key once on stdout** (and nothing else on stdout), so a script can capture it. Nothing is ever
passed as a flag value that is a secret: there is no `--key` flag.

## The web page

`sudo radar-keys` (or `radar-keys serve --no-browser`) listens on a random port of `127.0.0.1` only and prints an address that contains
this run's token. Open it in the browser (on the laptop, or through an SSH tunnel `ssh -L PORT:127.0.0.1:PORT laptop`). The token changes at
every start; foreign Host or Origin headers are refused; there is no CORS.

- **Table** of all keys, newest first, with search and a tag filter. Columns: enabled checkbox, masked key, email, player, tags,
  created, last seen, lines today, rate limit (default or custom), status (active, blocked, expired).
- **Enabled checkbox** is the block switch. Unchecking asks for an optional reason. It takes effect within about 2 seconds, also for
  streams that are already open.
- **New key** / **Edit**: email (required), player name, tags (comma or Enter), notes, expiry date, and the rate limits (see below).
  After **Create** the new key is shown once with a Copy button.
- **Regenerate** makes a new key for the same entry and shows it; **Delete** removes the entry (and its vault copy).

### The eye button (reveal)

A hash cannot be turned back into a key, so to show a key later the manager keeps a **sealed copy** (AES-256-GCM) in a vault. The eye
button on a row shows the full key for **10 seconds**, then hides it again; it has a Copy button. The key travels in one
`GET /api/keys/{id}/reveal` reply with `Cache-Control: no-store`, never in the list reply, never in logs or in radar-ingest. Each reveal
writes an audit line (key id and time, no key) to the manager's log.

Keys that have **no** vault copy (created with `--no-vault` or by unchecking "Keep a recoverable copy so I can view it later") have a
greyed eye: use **Regenerate**.

**Honest limits.** The vault protects the files at rest: a backup, a stolen disk image, or a process running as the radar service user
cannot read the keys without `keyvault.key`. It does **not** protect against anyone who can act as root on the laptop: root can read the vault
and the sealing key. If you do not want any recoverable copy, use `--no-vault` and hand the key over when it is created.

## Per-key limits, expiry, tags

Each key can override the server defaults (blank = default; 0 is refused, use blank):

| Limit | CLI flag | Server default setting (default value) | Maximum |
|---|---|---|---|
| lines per second | `--line-rate` | `stream-key-line-rate` (20000) | 1,000,000 |
| line burst | `--line-burst` | `stream-key-line-burst` (40000) | 10,000,000 |
| bytes per second | `--byte-rate` | `stream-key-byte-rate` (16 MiB) | 1 GiB |
| byte burst | `--byte-burst` | `stream-key-byte-burst` (32 MiB) | 4 GiB |
| simultaneous streams | `--max-streams` | `stream-max-per-key` (4) | 1000 |

A changed limit applies to the key's next charge. **Expiry** (`--expires YYYY-MM-DD`, end of that day UTC): after it the key answers like a
blocked key. **Tags** (up to 16, e.g. `guild:ashes`, `tester`) and **notes** are only labels for you. The email is personal data: it exists
only in `keyring.json`, never in logs, metrics, the audit log or anything radar-ingest sends.

## How a key reaches a player

Send the key through a private channel you trust. The player puts it in:

- radar-client: the **Ingest key** field (`docs/client.md`), or the environment variable `RADAR_CLIENT_INGEST_KEY`;
- radar-feed: `RADAR_FEED_KEY` (or `RADAR_FEED_KEY_FILE`, a file holding the key).

## What radar-ingest does

- **Unknown key**: `401`, as always (counts toward the key-guessing lockout).
- **Blocked or expired key**: `403 {"error":"key_disabled"}`. radar-client and radar-feed treat 401 and 403 as final and stop, showing that
  the key was refused.
- **Stream already open when the key is blocked**: ended at the next reload tick (a few seconds); its summary reply says `"ended":"blocked"`.
- **Unblocking** makes the key work again at the next reload; nothing is restarted.
- A broken or half-written `keyring.json` is logged and ignored; the previous set of keys stays active.
- `radar-ingest` counts per key id (never per email) the last seen time, lines today and bytes today, and writes `keyusage.json` every 60 s
  and on a clean stop. The manager shows them.

## Files, owners and modes (what install.sh creates)

radar-ingest runs as the service user `radar`; `radar-keys` runs as root. The vault must not be readable by the service user, and the
service writes only its own usage file.

| Path | Owner, mode | Written by | Read by | Holds |
|---|---|---|---|---|
| `/var/lib/radar-keys/` | root:root 0755 | install.sh | all | only a parent directory |
| `/var/lib/radar-keys/keyring/` | root:radar 0750 | install.sh | radar-ingest, radar-keys | the keyring and its lock file |
| `/var/lib/radar-keys/keyring/keyring.json` | root:radar 0640 | radar-keys | radar-ingest, radar-keys | key ids, SHA-256 hashes, emails, limits (no keys) |
| `/var/lib/radar-keys/usage/` | radar:radar 0700 | install.sh | radar-ingest | the usage file |
| `/var/lib/radar-keys/usage/keyusage.json` | radar:radar 0600 | radar-ingest | radar-keys (root) | per key id: last seen, lines, bytes today |
| `/var/lib/radar-keys/vault/` | root:root 0700 | install.sh | radar-keys | the vault and the sealing key |
| `/var/lib/radar-keys/vault/keyvault.bin` | root:root 0600 | radar-keys | radar-keys | the keys, sealed with AES-256-GCM |
| `/var/lib/radar-keys/vault/keyvault.key` | root:root 0600 | radar-keys (first use) | radar-keys | the 32-byte sealing key |

`radar-keys` writes `keyring.json` atomically (temp file, fsync, rename) under a lock file and keeps the owner and mode of the file it
replaces, so the 0640 root:radar mode survives every edit. `radar-ingest.service` has exactly one writable path,
`/var/lib/radar-keys/usage`. `radar.env` gets `RADAR_KEYRING_FILE` and `RADAR_KEYUSAGE_FILE` (set on a fresh install; added to an
existing file only when absent). The vault settings (`RADAR_KEYVAULT_FILE`, `RADAR_KEYVAULT_KEY_FILE`) are **not** put there: the service
user can read that file. The `radar-keys` default paths match this layout; `--keyring`, `--vault`, `--vault-key`, `--usage` (or the same
environment variables) override them, and a custom `--keyring` makes the other files default to its directory.
`sudo sh verify-hardening.sh` checks all of the above.

## Backup and restore

`radar-keys-backup` (installed to `/usr/local/sbin`, run by root from `/etc/cron.d/radar-live-backup` at 03:27) copies `keyring.json` and
`keyvault.bin` to `/var/backups/radar-live/keys` (root only, 0700) with a timestamp and keeps the newest 14 of each. **It never copies
`keyvault.key`.** A backup of the vault alone cannot reveal any key, and that is the point.

**Restore needs both**: the backup `keyvault.bin` and the original `/var/lib/radar-keys/vault/keyvault.key`. Keep your own offline copy of
`keyvault.key` (a password manager, a USB stick in a drawer), and treat the backup of `keyring.json` like `radar.env`: it lists
players' emails. Without the sealing key, restore only the keyring: every player still works (their keys are checked by hash), but the
eye button will not work, and you can regenerate keys that need to be viewed.

```sh
sudo cp /var/backups/radar-live/keys/keyring-<timestamp>.json /var/lib/radar-keys/keyring/keyring.json
sudo chown root:radar /var/lib/radar-keys/keyring/keyring.json && sudo chmod 0640 /var/lib/radar-keys/keyring/keyring.json
sudo cp /var/backups/radar-live/keys/keyvault-<timestamp>.bin /var/lib/radar-keys/vault/keyvault.bin   # needs the original keyvault.key
sudo chmod 0600 /var/lib/radar-keys/vault/keyvault.bin
```

radar-ingest picks the restored keyring up within 2 seconds.

## Legacy keys from the environment

`RADAR_INGEST_KEYS` / `RADAR_INGEST_KEYS_FILE` still work exactly as before, next to the keyring, and `RADAR_REQUIRE_KEYS` counts both. They have
no email and no limits of their own (the server defaults apply), cannot be blocked from `radar-keys` and are **not listed** in it. The
shared key in `radar.env` is also the key radar-db and radar-armory use between the servers, so keep it. To migrate a player, create a
keyring key for them, give it to them, and later rotate the shared key (`docs/security/hardening.md`, key rotation).

## Troubleshooting

- **403 `key_disabled`**: the key is blocked or expired; `sudo radar-keys list` shows the status.
- **A new key still gets 401**: wait 2 seconds; check `journalctl -u radar-ingest` for "keyring reloaded" (or a message that the file was ignored)
  and that `RADAR_KEYRING_FILE` in `/etc/radar-live/radar.env` is the file radar-keys edits (`radar-keys` uses the same variable).
- **"permission denied"**: run it with `sudo`.
- **Last seen / lines today stay empty**: `RADAR_KEYUSAGE_FILE` must be set and `/var/lib/radar-keys/usage` must be writable by `radar`
  (the unit lists it as `ReadWritePaths`); the file is written every 60 seconds.
