# Host and deployment hardening (task 8.10)

Target: radar-live on a weak Xubuntu laptop behind a home connection. Everything here uses placeholders
(`radar.example.com`, `192.0.2.0/24`, `<token>`): replace them on the laptop, never in the repository
(`docs/security/secrets-policy.md`). Nothing in this document was executed on a real Linux host by the author;
`deploy/verify-hardening.sh` is how you prove each claim on your own machine.

## 0. What can and cannot be defended

- **Volumetric DDoS (L3/L4 floods, SYN floods, saturating the home uplink) cannot be absorbed by the laptop or by any Go code.**
  A home line of 20 to 100 Mbit is full long before the CPU notices. Only a CDN with huge capacity in front of you absorbs
  it, and only if the attacker cannot reach your home address directly. That is why remote access goes through a
  Cloudflare Tunnel (or similar) and the firewall is closed (sections 2 and 3).
- What the application and this host layer do stop: L7 abuse (request floods, slowloris, oversized bodies, key guessing,
  scraping), crashing or exhausting the box (memory, tasks, file handles, CPU quota), and damage after a compromise
  (sandbox, no capabilities, nothing writable but the data directory).
- "Cannot be hacked" is not provable. Done means every check of `verify-hardening.sh` passes and the checklist rows
  SEC-110..SEC-119 have evidence.

## 1. systemd sandbox (`deploy/radar-db.service`, `deploy/radar-ingest.service`)

| Directive | Why |
|---|---|
| `User=radar`, no login shell (`/usr/sbin/nologin`), no home | a compromised process is nobody |
| `NoNewPrivileges`, `CapabilityBoundingSet=` and `AmbientCapabilities=` empty | no setuid escalation, no capability at all (ports are above 1024) |
| `ProtectSystem=strict`, `ProtectHome`, `PrivateTmp`, `PrivateDevices`, `RemoveIPC`, `KeyringMode=private` | read-only OS, no other user's files, private `/tmp` and devices |
| `ReadWritePaths=/var/lib/radar-live` (radar-db only) + `StateDirectory` 0700 | the one writable data place; radar-ingest has none |
| `LogsDirectory=radar` 0700, `RADAR_AUDIT_LOG=/var/log/radar/security*.log` | the security log (fail2ban reads it as root) |
| `RuntimeDirectory` 0700 | private scratch under `/run`, removed on stop |
| `ProtectKernelTunables/Modules/Logs`, `ProtectControlGroups`, `ProtectClock`, `ProtectHostname` | cannot touch sysctl, load modules, read dmesg, change cgroups, clock or hostname |
| `ProtectProc=invisible`, `ProcSubset=pid` | cannot see other processes or most of `/proc` |
| `RestrictNamespaces`, `RestrictRealtime`, `RestrictSUIDSGID`, `LockPersonality` | no new namespaces, no realtime scheduling, no setuid files, no personality tricks |
| `RestrictAddressFamilies=AF_INET AF_INET6 AF_UNIX` | no raw/packet/netlink sockets |
| `SystemCallArchitectures=native`, `SystemCallFilter=@system-service` then `~@privileged @resources`, `SystemCallErrorNumber=EPERM` | only ordinary service syscalls; a denied one returns an error instead of killing the process |
| `MemoryDenyWriteExecute=yes` | no memory that is writable and executable at once, which blocks most shellcode loaders |
| `UMask=0077` | new files are private |
| `MemoryHigh=160M`, `MemoryMax=192M`, `TasksMax=256`, `LimitNOFILE=4096`, `CPUQuota=150%` | a runaway process slows then dies alone, the laptop stays usable. `GOMEMLIMIT=96MiB` and `GOMAXPROCS=2` stay in `radar.env` as soft limits |
| `Restart=on-failure`, `RestartSec=3`, `StartLimitIntervalSec=120`, `StartLimitBurst=5` | recovers from a crash, stops a crash loop after 5 starts in 2 minutes (`systemctl reset-failed radar-db` to retry) |

**MemoryDenyWriteExecute and Go/modernc sqlite.** The Go runtime allocates heap and stacks with `mmap` without
`PROT_EXEC` and has no JIT; `modernc.org/sqlite` is SQLite transpiled to Go (no cgo, no generated machine code at run
time). So the combination is expected to work, and both binaries are static and CGO-free. This was not run on a real
Linux host while writing this file. First start on the laptop: `systemctl status radar-db` must say `active (running)`; a
death with `SIGSEGV`/`status=11` right after start would mean MDWE is the cause. In that case delete the
`MemoryDenyWriteExecute=yes` line from the unit, `systemctl daemon-reload && systemctl restart radar-db`, and record it
here. `verify-hardening.sh` then FAILs that single check, which is the intended reminder.

**SystemCallFilter fallback.** If the journal shows `status=31/SYS` (killed by a blocked syscall), the `EPERM` setting
should already have turned it into an error; look at `journalctl -u radar-db` and run
`systemd-analyze syscall-filter @resources` to see what was blocked. Loosening means removing `@resources` from the second
`SystemCallFilter=` line, not the whole filter.

**Score.** After installing, run:

```sh
systemd-analyze security radar-db.service radar-ingest.service
```

Target: an overall exposure level of **2.5 or lower (label OK)** for both units. Anything above 5 (MEDIUM) means a
directive got lost (the per-line list shows which). Remaining items that cannot be removed for a network service
(`IPAddressDeny` would block the listeners, `PrivateNetwork` would too) are accepted. The author could not run the tool
(it exists only on the laptop); record the real numbers in the Results block of security-plan.md.

User and file ownership (set by `install.sh`):

```
/etc/radar-live               0750 root:radar
/etc/radar-live/radar.env     0640 root:radar   (the keys; readable by the service, not by other users)
/var/lib/radar-live           0700 radar:radar  (database; the files inside are 0600)
/var/backups/radar-live       0700 radar:radar
/var/log/radar                0700 radar:radar  (security log 0600)
/usr/local/bin/radar-*        0755 root:root    (the service cannot rewrite its own binary)
```

## 2. Firewall (`deploy/ufw-setup.sh`)

```sh
sudo apt install ufw
sh deploy/ufw-setup.sh --dry-run 192.0.2.0/24     # prints the ufw commands, changes nothing
sudo sh deploy/ufw-setup.sh 192.0.2.0/24          # LAN mode: SSH + 8787 from the LAN only
sudo sh deploy/ufw-setup.sh --tunnel 192.0.2.0/24 # tunnel mode: SSH from the LAN, NO web port
sudo sh deploy/ufw-setup.sh --public 192.0.2.0/24 # 8787 open to the world (Caddy-style); avoid
```

Rules: default deny incoming, allow outgoing, SSH rate limited (`ufw limit`, 6 new connections per 30 s per address) and
only from the LAN, web port only from the LAN (or nowhere, or everywhere with the explicit `--public`). 8788 and 8789 are
**never** opened: they are loopback listeners. IPv6 filtering is switched on (`IPV6=yes` in `/etc/default/ufw`), otherwise
v6 stays open. It refuses `/0`, malformed networks and anything with shell characters. Re-running is safe.

A feeder on another LAN PC needs 8788, which the script deliberately does not open. Prefer the tunnel (section 3). If you accept
the risk, add one rule by hand for that PC only (`sudo ufw allow from <pc-address> to any port 8788 proto tcp`) and set
`RADAR_INGEST_LISTEN` to the LAN address; `verify-hardening.sh` will then FAIL the 8788 checks, which is the intended reminder.

Established SSH sessions survive `ufw enable`, but test a fresh SSH login from another device before closing your session.
**Rollback:** `sudo ufw disable` (stop filtering), `sudo ufw status numbered` then `sudo ufw delete N` (one rule), or
`sudo ufw --force reset` (all rules gone). If locked out, use the keyboard on the laptop.

## 3. Remote access with a Cloudflare Tunnel (recommended)

Why: the browser talks to Cloudflare, `cloudflared` on the laptop makes an **outbound** connection to Cloudflare, and the
laptop accepts nothing from the internet. Cloudflare absorbs volumetric attacks and terminates TLS.

`/etc/radar-live/radar.env`:

```
RADAR_DB_LISTEN=127.0.0.1:8787
RADAR_RATELIMIT_TRUSTED_PROXIES=127.0.0.1/32
RADAR_HEADERS_HSTS=true
RADAR_REQUIRE_KEYS=true
```

`RADAR_RATELIMIT_TRUSTED_PROXIES=127.0.0.1/32` trusts `CF-Connecting-IP` **only when the TCP peer is the local tunnel
process**. Because the web port is bound to loopback, nobody else can reach it to forge the header. Never set it while
8787 is also reachable from the LAN or the internet. Add `::1/128` if cloudflared connects over IPv6 loopback.
`RADAR_REQUIRE_KEYS=true` makes both services refuse to start without keys. Then `sudo systemctl restart radar-db radar-ingest`.

Create the tunnel in the Cloudflare dashboard (Zero Trust, Networks, Tunnels, "Cloudflared", remote-managed) with one public
hostname `radar.example.com` pointing at `http://127.0.0.1:8787`. Route **only** that port: never 8788 or 8789 (the internal
write API and metrics must stay unreachable from outside). Copy the token shown there. **Do not run
`cloudflared service install <token>`**: it writes the token into a unit file and a command line. Instead:

```sh
sudo useradd --system --no-create-home --shell /usr/sbin/nologin cloudflared
sudo install -d -m 0700 -o root -g root /etc/cloudflared
sudoedit /etc/cloudflared/tunnel.env        # one line: TUNNEL_TOKEN=<token>
sudo chown root:root /etc/cloudflared/tunnel.env && sudo chmod 0600 /etc/cloudflared/tunnel.env
```

systemd (PID 1, root) reads `EnvironmentFile=` before it drops to `User=cloudflared`, so the file can stay 0600 root:root and
not even the tunnel account can read it. The token lives outside the repo; `TUNNEL_TOKEN` is the variable name cloudflared itself reads
(the policy's `RADAR_TUNNEL_TOKEN` is for tokens kept in radar.env, which a tunnel does not need and which the radar group can read).
`/etc/systemd/system/cloudflared.service`:

```
[Unit]
Description=Cloudflare Tunnel for radar-live
After=network-online.target radar-db.service
Wants=network-online.target
StartLimitIntervalSec=120
StartLimitBurst=5

[Service]
User=cloudflared
EnvironmentFile=/etc/cloudflared/tunnel.env
ExecStart=/usr/bin/cloudflared --no-autoupdate tunnel run
Restart=on-failure
RestartSec=5
NoNewPrivileges=yes
ProtectSystem=strict
ProtectHome=yes
PrivateTmp=yes
PrivateDevices=yes
CapabilityBoundingSet=
AmbientCapabilities=
RestrictAddressFamilies=AF_INET AF_INET6 AF_UNIX
MemoryMax=128M
UMask=0077

[Install]
WantedBy=multi-user.target
```

`sudo systemctl daemon-reload && sudo systemctl enable --now cloudflared`, then `sudo sh deploy/ufw-setup.sh --tunnel 192.0.2.0/24`.
Also in the Cloudflare dashboard: SSL mode Full, "Always use HTTPS", a WAF rate-limiting rule on `/api/*`, and Bot Fight
Mode. A feeder on a remote gaming PC reaches radar-ingest through a second hostname to `http://127.0.0.1:8788` protected by
Cloudflare Access with a service token; the ingest key still applies. Plain HTTP over the open internet to 8788 is not an option.

### Origin IP hiding checklist
- [ ] No DNS A/AAAA record anywhere (also old subdomains, `mail.`, `direct.`) points at the home address; the tunnel hostname is a CNAME managed by Cloudflare.
- [ ] No router port forwarding to the laptop; UPnP off on the router.
- [ ] `ufw` default deny; web port closed (`--tunnel` mode); `verify-hardening.sh` passes.
- [ ] The app never reveals the address: no absolute URLs with the IP, `RADAR_SECURITY_CONTACT` is an e-mail or URL that does not.
- [ ] The only network path is cloudflared's outbound connection (`ss -tnp | grep cloudflared`).
- [ ] The address was never published (forum posts, screenshots, git history, old DNS history). If it leaked, change it: reboot the router or ask the ISP; DNS history cannot be erased.
- [ ] Email or other services on the same address do not run (they reveal it in headers).

## 4. Alternative: Caddy (direct exposure)

Weaker: ports 80/443 are forwarded to the laptop and the home address is public, so a volumetric attack hits the home line.
Use it only for a small trusted audience. Bind radar-db to loopback exactly as above, open 80/443 (`sudo ufw allow 80,443/tcp`
by hand; `ufw-setup.sh --public` is for 8787 and not what you want here), and use Caddy for TLS (automatic, Let's Encrypt).
The rate limit directive needs a Caddy build with the `caddy-ratelimit` module (`xcaddy build --with github.com/mholt/caddy-ratelimit`).

```
radar.example.com {
    request_body {
        max_size 1MB
    }
    rate_limit {
        zone per_ip {
            key    {remote_host}
            events 120
            window 1m
        }
    }
    header Strict-Transport-Security "max-age=31536000"
    reverse_proxy 127.0.0.1:8787 {
        # Caddy sets X-Forwarded-For from the real peer. A client-supplied CF-Connecting-IP would be believed
        # by radar-db (the proxy is trusted), so strip it and let X-Forwarded-For carry the address.
        header_up -CF-Connecting-IP
    }
}
```

The app sends HSTS itself with `RADAR_HEADERS_HSTS=true`; the header line above is a safety net. Never proxy 8788 or 8789.
Keep `RADAR_RATELIMIT_TRUSTED_PROXIES=127.0.0.1/32` here too, for the same reason as with the tunnel.

## 5. fail2ban (`deploy/fail2ban/`)

```sh
sudo apt install fail2ban
sudo sh install.sh --with-fail2ban        # copies radar-live.conf to filter.d and jail.local to jail.d/radar-live.local
sudo fail2ban-regex /var/log/radar/security.log radar-live       # dry run of the filter
sudo fail2ban-client status radar-live
```

The filter reads the JSON security log of `docs/security/logging.md` (events `auth_failure` and `rate_limited`, address
right after `event`). The jail bans for one hour after 20 distinct trouble-minutes in 10 minutes, ignores loopback, and
acts through ufw. Behind the tunnel the visitor never connects directly, so a firewall ban cannot help there: the app-level
bans (`radar-db guard list`) and Cloudflare's WAF do that job. The fail2ban jail matters in LAN or Caddy mode.

## 6. Updates, SSH, accounts

**Automatic security updates**

```sh
sudo apt install unattended-upgrades
sudo dpkg-reconfigure -plow unattended-upgrades      # answer Yes
cat /etc/apt/apt.conf.d/20auto-upgrades              # both lines must say "1"
sudo unattended-upgrade --dry-run --debug | tail
```

`20auto-upgrades` must contain `APT::Periodic::Update-Package-Lists "1";` and `APT::Periodic::Unattended-Upgrade "1";`. Keep
the default origins (security pockets only). Consider `Unattended-Upgrade::Automatic-Reboot "true"` with
`Automatic-Reboot-Time "04:00"` so kernel fixes apply; the services come back by themselves. radar-live itself is never
updated from the network (checksum-verified manual installs only, `docs/security/supply-chain.md`).

**SSH, key only** (`/etc/ssh/sshd_config.d/10-radar.conf`):

```
PasswordAuthentication no
KbdInteractiveAuthentication no
PermitRootLogin no
PubkeyAuthentication yes
MaxAuthTries 3
LoginGraceTime 20
AllowUsers <your-login>
X11Forwarding no
```

Order: copy your public key first (`ssh-copy-id`), open a second session, then `sudo sshd -t && sudo systemctl reload ssh` and
test a new login before closing the old one. `sudo sshd -T | grep -E 'passwordauthentication|permitrootlogin'` shows what is
effective; `verify-hardening.sh` checks it. Use an ed25519 key with a passphrase. Do not expose SSH to the internet (the
firewall allows it from the LAN only).

## 7. Key rotation (no downtime for the page)

Keys are comma lists, so old and new can be valid together.

1. Generate: `radar-db keygen` (prints one key; run it twice for two roles). Do it on the laptop, never paste keys into chat or tickets.
2. Edit `/etc/radar-live/radar.env` (`sudoedit`): `RADAR_INGEST_KEYS=<old>,<new>`, `RADAR_VIEW_KEYS=<old>,<new>`. `RADAR_DB_KEY` must stay one of the ingest keys.
3. `sudo systemctl restart radar-db radar-ingest` (radar-ingest buffers while radar-db restarts).
4. Move clients over: feeders (`RADAR_FEED_KEY` / `RADAR_FEED_KEY_FILE`), the desktop client, and tell viewers to enter the new view key (they tick "remember" only on trusted devices).
5. Check `radar_auth_failures_total` stays flat for a day (`/internal/metrics`, section 9) and `journalctl` shows no `bad_key` from your own addresses.
6. Remove the old keys from the lists, set `RADAR_DB_KEY=<new>` if it used the old one, restart again, and delete any file or note that held the old key.
7. After a suspected leak do steps 1 to 6 at once, skipping the overlap, and review `/var/log/radar/security.log` for the time window.

The same applies to the Cloudflare tunnel token: revoke and recreate the tunnel in the dashboard, write the new token into
`/etc/cloudflared/tunnel.env`, restart `cloudflared`.

## 8. Backup and restore drill (quarterly)

Backups live on the same disk (`/var/backups/radar-live`, nightly via `backup.cron`, newest 14). Copy a recent file to another
device or offsite storage you control; a backup that dies with the disk is not a backup.

1. List: `sudo ls -l /var/backups/radar-live`. The newest file must be less than 25 hours old.
2. Prove it opens, on a scratch copy so the live database is untouched:
   ```sh
   sudo -u radar install -d -m 0700 /var/tmp/radar-drill
   sudo -u radar /usr/local/bin/radar-db restore --db-path /var/tmp/radar-drill/radar-live.db /var/backups/radar-live/radar-<timestamp>.db
   sudo -u radar /usr/local/bin/radar-db backup --db-path /var/tmp/radar-drill/radar-live.db /var/tmp/radar-drill/check.db
   sudo rm -r /var/tmp/radar-drill
   ```
   `restore` verifies the backup (`quick_check`) and refuses damaged files; the second command proves the restored copy is readable.
3. Real restore (only when needed): `sudo systemctl stop radar-db radar-ingest`, `radar-db restore --db-path /var/lib/radar-live/radar-live.db <backup>` as user radar, `sudo systemctl start radar-db radar-ingest`. The old files are kept as `.pre-restore-<time>`.
4. Confirm: `curl -s http://127.0.0.1:8787/api/v1/status` (needs the view key) shows `integrity: ok` and the expected contact count.
5. Write the date of the drill and the backup age into your own notes.

## 9. Logs and monitoring

**Rotation.** The security log rotates itself by size (`RADAR_AUDIT_MAX_MB`, default 10, keeping `RADAR_AUDIT_KEEP`, default 5, named
`security.log.1` ...). Do not add a logrotate rule with `copytruncate` or compression for these files: the application owns the
renaming. To keep 90 days of history, raise `RADAR_AUDIT_KEEP` and delete old rotated files by age, for example a cron line
`find /var/log/radar -name 'security*.log.*' -mtime +90 -delete`. The text logs go to the journal: set `SystemMaxUse=200M` and
`MaxRetentionSec=1month` in `/etc/systemd/journald.conf.d/radar.conf`. The log contains client IP addresses: keep it out of git and
off shared drives.

**What to watch** (`curl -s http://127.0.0.1:8789/internal/metrics | grep -v '^#'`, loopback only, never forwarded):

| Metric | Worry when |
|---|---|
| `radar_auth_failures_total`, `radar_bans_total`, `radar_bans_active` | sudden growth: someone is guessing keys |
| `radar_http_429_total`, `radar_rate_limited_total` | sustained growth: a flood or a client that needs a bigger budget |
| `radar_connections_refused_total`, `radar_inflight_requests` | near the caps (512 connections, 256 in flight) |
| `radar_db_integrity_degraded` = 1, `radar_db_writes_paused` = 1 | restore a backup / free disk |
| `radar_db_size_bytes` | approaching `RADAR_DB_MAX_MB` |
| `radar_db_last_write_age_seconds` | grows while a feeder should be sending |
| `radar_relay_dropped_total`, `radar_relay_pending` (radar-ingest, needs `RADAR_INTERNAL_LISTEN=127.0.0.1:8790`) | pings are being lost |

Set `RADAR_ALERT_CMD` (environment only) to get told about `ban_surge`, `db_degraded`, `disk_low` and `relay_drops`.

## 10. Verify on the laptop

```sh
sudo sh /usr/local/share/doc/radar-live/verify-hardening.sh        # add --public-ok only if you chose --public on purpose
```

It reads state only. Per line `PASS`, `FAIL` or `SKIP` (SKIP = needs root or the tool is absent); it exits 1 if anything FAILs.
Checks: both units active and enabled; every sandbox property as systemd reports it (`systemctl show`); limits set; service
user without login shell; modes and owners of `/etc/radar-live`, `radar.env` (0640 root:radar), data (0700), backup and log
directories, binaries and database files; no `CHANGE_ME` left and keys at least 16 characters; `RADAR_REQUIRE_KEYS` effective;
in proxy mode HSTS on and `127.0.0.1` trusted; 8788 and 8789 bound to loopback only and 8787 bound where `RADAR_DB_LISTEN` says;
`/internal/metrics` absent on the public port; ufw active with default deny, SSH limited and not open to anywhere, no rule for
8788/8789; the `radar-live` fail2ban jail running; unattended-upgrades enabled with the daily apt settings; sshd password login
and root login off.

Fixes for common FAILs: wrong mode on the env file: `sudo chown root:radar /etc/radar-live/radar.env && sudo chmod 0640 /etc/radar-live/radar.env`;
data directory: `sudo chmod 0700 /var/lib/radar-live` (running `install.sh` again does both); a unit property: compare with
`deploy/*.service` and run `sudo systemctl daemon-reload && sudo systemctl restart radar-db radar-ingest`.

The same file list as a table is in `docs/security/checklist.md` (SEC-110..SEC-119).
