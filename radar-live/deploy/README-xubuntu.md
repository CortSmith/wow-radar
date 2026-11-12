# Running radar-live on an Xubuntu laptop

Two small static programs, no Docker, no database package, no Node or Python. A 2 GB laptop is plenty:
each program idles well under 60 MB, and the units cap memory at 192 MB.

## 1. Build (on the Windows PC) and copy

```powershell
cd radar-live        # the folder that contains scripts\ and deploy\ (skip if you are already in it)
powershell -ExecutionPolicy Bypass -File scripts\build.ps1
scp -r dist/* you@laptop:/tmp/radar-live/     # -r: also copies the maps/ and fail2ban/ sub-folders; or use a USB stick
```

`dist\` holds the binaries for amd64 and arm64, `install.sh`, the two units, `radar.env.example`,
this file and `SHA256SUMS`. (Not on Windows? `sh scripts/build.sh` does the same.)

## 2. Install (on the laptop)

```sh
cd /tmp/radar-live
sudo sh install.sh
```

It refuses to run unless `SHA256SUMS` is present and every file matches, then creates the `radar` user, `/var/lib/radar-live` (database) and
`/etc/radar-live/radar.env` (settings and keys, mode 0640), installs the binaries to
`/usr/local/bin`, installs and starts `radar-db.service` and `radar-ingest.service`.

The first run prints two generated keys **once** (they also live in `radar.env`):

| Key | Who uses it |
|---|---|
| view key | you: type it into the page (it is remembered by the browser) |
| ingest key | `radar-feed` on the gaming PC |

Running `install.sh` again is safe: it keeps `radar.env` and the database and only swaps the binaries.
Options: `--arch amd64|arm64`, `--no-start`, `--with-ufw LAN_CIDR` (firewall, see section 9), `--ufw-tunnel`,
`--with-fail2ban`. Two installers at once are refused; the script must run as root.

### How the keys are wired

One shared ingest key is enough, and `install.sh` does it for you:

```
radar-feed --X-Ingest-Key--> radar-ingest   (RADAR_INGEST_KEYS on radar-ingest checks it)
radar-ingest --X-Ingest-Key--> radar-db     (RADAR_DB_KEY is sent; RADAR_INGEST_KEYS on radar-db checks it)
browser --X-View-Key--> radar-db            (RADAR_VIEW_KEYS)
```

`RADAR_INGEST_KEYS`, `RADAR_DB_KEY` and `RADAR_VIEW_KEYS` are all in `/etc/radar-live/radar.env`, which both
services read. View and ingest keys are separate roles: an ingest key cannot read the API. To rotate, edit the
file and `sudo systemctl restart radar-db radar-ingest`, then update the feeder.

## 3. Open the page

`http://<laptop-ip>:8787/` from any device on the LAN (`hostname -I` shows the address). With the firewall script
(`sudo sh ufw-setup.sh 192.0.2.0/24`, your LAN instead of the placeholder) the port is open to the LAN only; see section 9.

Check it: `curl -s localhost:8787/healthz` prints `ok`; `systemctl status radar-db radar-ingest`;
`journalctl -u radar-db -u radar-ingest -f`.

## 4. Feed it from the gaming PC

Enable advanced combat logging in WoW and `/combatlog`. On the PC (Windows or Linux) the ingest key is given by environment variable only (there is no `--key` flag: command lines show up in process lists), either `RADAR_FEED_KEY` or `RADAR_FEED_KEY_FILE` (path of a file outside any repo holding the key):

```powershell
$env:RADAR_FEED_KEY_FILE = "C:\Users\<you>\radar\ingest.key"   # a file holding the key; never commit it
radar-feed-windows-amd64.exe --ingest-url http://<laptop-ip>:8788 --source my-pc
```

Radar-ingest listens on `127.0.0.1:8788` by default. To accept the feeder from another machine, set
`RADAR_INGEST_LISTEN=0.0.0.0:8788` in `radar.env` and `sudo systemctl restart radar-ingest`
(and allow port 8788 from the PC only). Plain HTTP: keep it on a trusted LAN or use the tunnel below.

## 5. Settings for a weak machine

`radar.env` already sets `GOMAXPROCS=2`, `GOMEMLIMIT=96MiB` and `RADAR_CACHE_MB=16` (SQLite page cache). The
database keeps at most 250 000 trail fixes (`RADAR_MAX_FIXES`). Each unit has `MemoryMax=192M`.

## 6. Backups, restore and data safety

```sh
# one-off copy (verified with quick_check, mode 0600, never overwrites an existing file)
sudo -u radar /usr/local/bin/radar-db backup --db-path /var/lib/radar-live/radar-live.db /var/backups/radar-live/manual.db
# nightly rotation: timestamped files, keeps the newest 14
sudo cp /usr/local/share/doc/radar-live/backup.cron /etc/cron.d/radar-live-backup
```

`backup` writes a consistent copy while the server runs. With `--keep N <dir>` it creates
`radar-YYYYMMDDTHHMMSSZ.db` in the directory, verifies the new file, and only then removes the oldest
`radar-*.db` files beyond N (other files are never touched).

**Restore** (also what the start-up error message tells you to do):

```sh
sudo systemctl stop radar-db radar-ingest
sudo -u radar /usr/local/bin/radar-db restore --db-path /var/lib/radar-live/radar-live.db /var/backups/radar-live/radar-<timestamp>.db
sudo systemctl start radar-db radar-ingest
```

`restore` verifies the backup, refuses while a server holds the database lock, and moves the current database and its
`-wal`/`-shm` files to `radar-live.db.pre-restore-<time>` instead of deleting them. Delete those yourself when you no longer
need them.

**Integrity.** radar-db runs `quick_check` on every start and a full `integrity_check` every `RADAR_DB_INTEGRITY_EVERY`
(default `24h`, `0` disables). A non-SQLite, damaged or newer-schema file stops the start with a message and is left
untouched. If a later check fails, the server stays up read-only: pings get `503 store_degraded`,
`/api/v1/status` shows `"integrity":"degraded"` and the log has an ERROR with these restore steps; restore a backup and restart.

**Disk limits.** `RADAR_DB_MAX_MB` (default 2048) caps the data size; `RADAR_DB_MIN_FREE_MB` (default 200) pauses pings when the data
directory's disk has less free space. Pings then get `507 insufficient_storage` (reads continue) and resume by themselves when space
returns. At the size cap the oldest trail fixes of the least recently seen contacts are pruned first, then the oldest batch and death ids.

**Permissions.** radar-db creates the data directory 0700 and the database files 0600 and logs a WARN when existing ones are wider.
`install.sh` makes the data and backup directories 0700 too (also when it upgrades an existing install).

## 7. Remote access (optional)

**Upgrading from before the listener split:** an existing `/etc/radar-live/radar.env` is never overwritten, so change `RADAR_DB_URL` to `http://127.0.0.1:8789` (and compare with `radar.env.example`) or radar-ingest will get 404/405 from the public port.

The full guide is [docs/security/hardening.md](../docs/security/hardening.md): Cloudflare Tunnel as a hardened
systemd service with the token in a root-only file (never on a command line), a Caddy alternative with TLS and a rate limit,
the origin-IP checklist, key rotation, backup drill and monitoring. Short version for the tunnel in `radar.env`:

```
RADAR_DB_LISTEN=127.0.0.1:8787
RADAR_RATELIMIT_TRUSTED_PROXIES=127.0.0.1/32
RADAR_HEADERS_HSTS=true
RADAR_REQUIRE_KEYS=true
```

Only the public port (page, `/api/*`, `/healthz`) is ever proxied; the write API lives on a separate loopback listener
(`RADAR_DB_INTERNAL_LISTEN=127.0.0.1:8789`, used by radar-ingest) and must not be forwarded. Do not publish the ingest port.
A volumetric DDoS can only be absorbed by the CDN in front of you, which is why the tunnel (no open ports, no DNS record
for your home address) is the recommended way.

**Rate limiting and the real client address.** The `ratelimit` module counts requests per client address, so it must
know who the client is. By default it uses the TCP peer. Behind a Cloudflare Tunnel or Caddy on this same machine every
request arrives from `127.0.0.1`, so all visitors would share one budget; tell the module to trust the local proxy:

```
RADAR_RATELIMIT_TRUSTED_PROXIES=127.0.0.1/32,::1/128
```

Then `CF-Connecting-IP` (or the last untrusted `X-Forwarded-For` hop) is used for peers in that list only; the same
headers from any other peer are ignored. Set it only when the public port is reachable solely through the proxy
(bind `RADAR_DB_LISTEN=127.0.0.1:8787`). Limits are in `radar.env.example` (`RADAR_RATELIMIT_*`); over-budget clients get
`429` with `Retry-After`; `RADAR_RATELIMIT_ENABLED=false` switches the module off.

**Key guessing, bans and the refuse-to-start policy.** The `abuse` module bans a client address after 5 failed key
attempts in 10 minutes (1 minute, doubling each time up to 24 hours); banned clients get `429` before any key check.
It uses the same trusted-proxy list as the rate limiter (or `RADAR_ABUSE_TRUSTED_PROXIES`) and never bans loopback
clients, so a same-host proxy without that setting cannot lock everybody out. The ban list is saved to
`/var/lib/radar-live/abuse-bans.json` (mode 0600, beside the database). Manage it with `radar-db guard list`,
`radar-db guard unban <ip>`, `radar-db guard ban <ip> 24h` (run with the service's environment so it finds the same
file); a running server notices the change within seconds. Both services refuse to start when a role in use has no
key and any listener is not loopback (`RADAR_REQUIRE_KEYS`, default auto; `false` accepts an open API on purpose).
The shipped `radar.env` sets all keys, so the units start as before; a hand-made env file without keys must set them
or bind to loopback.

### Zone maps (included by default)

The radar draws a zone picture (and the world map) behind the contacts. The images come from game data for personal
use, so they are **not** in the binary and **not** in git; the build copies them from the repository's `maps/` folder
into `dist/maps` automatically (`-NoMaps` / `--no-maps` leaves them out), and `SHA256SUMS` covers every file.

Copy `dist` with `scp -r`, then `sudo sh install.sh` verifies `maps/` against `SHA256SUMS` and installs it into
`/var/lib/radar-live/maps` (owned by root, 0755/0644: the service can read it but not change it). `radar-db.service`
defaults `RADAR_MAPS_DIR` to that folder, so no flag and no `radar.env` edit is needed, even on an install whose
`radar.env` predates maps; a `RADAR_MAPS_DIR=` line in `radar.env` still overrides it (empty = off).
After installing, check: `curl -sI localhost:8787/maps/index.json` answers 200 (the page asks for it
without a key). Without maps the page still works: grid and outline, plus a muted "no map image" note.

## 8. Upgrade and uninstall

Copy a newer `dist` and run `sudo sh install.sh` again. `sudo sh uninstall.sh` removes the services and
binaries but keeps data and settings; `sudo sh uninstall.sh --purge` deletes everything including the `radar` user.

## 9. Hardening: firewall, fail2ban, verification

The units are sandboxed (no capabilities, read-only system, only `/var/lib/radar-live` writable, syscall filter, memory,
task and CPU caps; reasons in `docs/security/hardening.md`). Check it on the laptop:

```sh
systemd-analyze security radar-db.service radar-ingest.service     # target: exposure 2.5 or lower
sh ufw-setup.sh --dry-run 192.0.2.0/24                              # preview the firewall; placeholder network
sudo sh ufw-setup.sh 192.0.2.0/24                                   # SSH (rate limited) + web port from the LAN only
sudo sh ufw-setup.sh --tunnel 192.0.2.0/24                          # tunnel mode: no web port at all
sudo sh install.sh --with-fail2ban                                  # filter + jail for /var/log/radar/security*.log
sudo sh /usr/local/share/doc/radar-live/verify-hardening.sh         # PASS/FAIL per check, exit 1 on FAIL
```

Ports 8788 and 8789 are never opened. Roll the firewall back with `sudo ufw disable`. `fail2ban/` must sit next to
`install.sh` for `--with-fail2ban` (copy the `deploy/fail2ban` folder along with `dist/`).
