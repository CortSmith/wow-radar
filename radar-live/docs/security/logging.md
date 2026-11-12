# Security log, metrics and alerts (task 8.9)

The `audit` module (`internal/mod/guard/audit`) turns security events into a JSON-lines log, Prometheus counters and an
optional alert command. Other modules never import it: they call `host.Audit.Emit(...)` and `host.Metrics.Add(...)` on the
core sockets. Delete the `audit.New()` line from `cmd/*/modules.go` and everything else keeps working (tested).

## Where the log goes
- `RADAR_AUDIT_LOG=/var/log/radar/security.log`: file, mode 0600, directory 0700 if created, appended.
  Rotated by size (`RADAR_AUDIT_MAX_MB`, default 10) keeping `RADAR_AUDIT_KEEP` older files (default 5): `security.log.1` is the newest old file.
- Unset: lines go to stderr, which systemd sends to the journal (`journalctl -u radar-db -o cat | grep '"event"'`).
- The normal text logs (`time=... level=...`) are separate and unchanged; the access log still never holds the query string.

## Line format
One JSON object per line, field order fixed (so regexes stay simple):

```
{"ts":"2026-11-12T00:53:30Z","event":"auth_failure","ip":"203.0.113.9","class":"public","reason":"bad_key","count":1}
```

| field | meaning |
|---|---|
| `ts` | UTC, RFC 3339, seconds |
| `event` | event name, below |
| `ip` | client address as resolved by `internal/netx` (TCP peer; `CF-Connecting-IP`/`X-Forwarded-For` only from configured trusted proxies). Omitted when the event has no client. IPv4-mapped IPv6 is written as IPv4 |
| `class` | route class: `public`, `internal`, `ingest`, `db`, or a rate-limit class (`page`, `sweep`, `search`, `write`, `ingest`, `authfail`) |
| `reason` | short constant token (`bad_key`, `ip_budget`, `conn_cap`, ...) |
| `count` | occurrences this line stands for |

Fields are reduced to `[A-Za-z0-9_.:-]` and short lengths; an `ip` that is not a literal address becomes `invalid`.
There are no other fields: **no key, header, query string, body, user agent, path or environment ever appears** (tests send a key in a header, the
query and the body and grep the log and the metrics for it).

### Flood control
The first occurrence of an `(event, ip, class, reason)` in a window (`RADAR_AUDIT_SAMPLE`, default 1 minute) is logged at once with `count` 1; the
rest of the window is summed into one more line (`count` = how many) when it ends or at shutdown. So at most one line per IP per minute per reason.
At most 4096 windows are open (beyond that addresses share one window without `ip`) and at most 200 lines per second are written; what the ceiling
refuses is summed into an `audit_overflow` line. The counters in `/internal/metrics` always count every occurrence.

## Events
| event | emitted by | reasons | notes |
|---|---|---|---|
| `auth_failure` | abuse | `bad_key` | a key was presented and rejected (401/403) |
| `ban` / `unban` | abuse | `lockout`, `admin` | `ban` on lockout; `admin` when `radar-db guard ban/unban` changed the ban file of a running server |
| `rate_limited` | ratelimit | `ip_budget`, `key_budget`, `authfail_budget` | 429 from a token bucket; `class` is the budget's class |
| `conn_refused` | limits | `conn_cap` | connection closed at the connection cap |
| `overloaded` | limits | `inflight_cap` | 503 at the in-flight cap |
| `validation_reject` | readapi, writeapi | `query_invalid`, `ping_invalid`, `ping_too_large` | a spike of these from one address shows as a large `count` |
| `stream_refused` | streamapi | `source_busy`, `too_many_streams`, `too_many_streams_key`, `too_many_sources`, `rate` | ingest connection refused |
| `integrity_degraded` | sqlstore | `quick_check`, `integrity_check` | writes now get 503; restore a backup |
| `disk_full` | sqlstore | `disk_low`, `size_cap` | writes now get 507; clears by itself |
| `internal_denied` | abuse | `non_loopback` | a non-loopback peer reached the internal listener |
| `config_warning` | ratelimit, abuse | `ratelimit_disabled`, `abuse_disabled` | startup warnings |
| `audit_started` / `audit_overflow` | audit | | the log opened / lines were dropped by the ceiling |

## fail2ban
Shipped as `deploy/fail2ban/radar-live.conf` and `jail.local` (`install.sh --with-fail2ban`, see `docs/security/hardening.md`; the jail name there is `radar-live`). The original sketch:

Filter `/etc/fail2ban/filter.d/radar.conf`:

```
[Definition]
failregex = ^\{"ts":"[^"]+","event":"(?:auth_failure|rate_limited)","ip":"<HOST>"
ignoreregex =
```

Jail (file log; the filter works on the journal too with `backend = systemd` and a `journalmatch` for the unit):

```
[radar]
enabled  = true
filter   = radar
logpath  = /var/log/radar/security.log
maxretry = 20
findtime = 10m
bantime  = 1h
```

Notes: one line stands for `count` occurrences, so with the sampling above `maxretry` counts distinct minutes with trouble; a single noisy
minute yields the first line plus a summary. The application already bans key guessers for itself (`abuse`, 5 failures in 10 minutes, doubling bans);
fail2ban adds a firewall-level ban for repeat offenders. Behind a tunnel the address is the real client only when the tunnel's address is in
`RADAR_RATELIMIT_TRUSTED_PROXIES`; otherwise every line shows the tunnel (loopback) and a firewall ban would be useless, so keep `ignoreip = 127.0.0.1/8 ::1`.
The filter is tested against real lines in `cmd/radar-db/audit_test.go`.

## Metrics
`GET /internal/metrics` on the **internal** listener (`127.0.0.1:8789` for radar-db; peers other than loopback are refused by `abuse`). It does not
exist on the public listener (404, tested). radar-ingest has no internal listener unless `RADAR_INTERNAL_LISTEN` is set (use another port, for example
`127.0.0.1:8790`). Prometheus text format 0.0.4, no dependency, no authentication (loopback; nothing in it is secret).

| metric | type | source |
|---|---|---|
| `radar_http_requests_total{class,code}` | counter | every request by listener class and status family (`2xx`...) |
| `radar_http_429_total` | counter | responses with status 429 |
| `radar_security_events_total{event}` | counter | all events by kind |
| `radar_auth_failures_total`, `radar_bans_total`, `radar_rate_limited_total` | counter | events |
| `radar_bans_active` | gauge | abuse |
| `radar_inflight_requests{class}`, `radar_connections{class}`, `radar_connections_refused_total{class}` | gauge/counter | limits |
| `radar_relay_dropped_total`, `radar_relay_pending`, `radar_relay_buffer_bytes`, `radar_relay_sent_total`, `radar_relay_retries_total` | counter/gauge | relay (radar-ingest) |
| `radar_stream_sessions`, `radar_stream_rejected_lines_total` | gauge/counter | streamapi (radar-ingest) |
| `radar_db_size_bytes`, `radar_db_integrity_degraded`, `radar_db_writes_paused`, `radar_db_last_write_age_seconds` (-1 = none yet) | gauge | sqlstore |
| `radar_audit_log_lines_total`, `radar_audit_log_lines_dropped_total`, `radar_alerts_fired_total{kind}` | counter | audit |

Quick look: `curl -s http://127.0.0.1:8789/internal/metrics | grep -v '^#'`.

## Alert command
`RADAR_ALERT_CMD` (env only, because it may contain a webhook token; it is never logged, only the program name is). Split into arguments like a shell
would split quotes but **without a shell** (no pipes, redirects, variables or globs); `{kind}`, `{text}`, `{count}` in an argument are replaced; the child gets only
`PATH` (and the Windows system variables) plus `RADAR_ALERT_KIND`, `RADAR_ALERT_TEXT`, `RADAR_ALERT_COUNT`, never the server's environment. It is killed after 5 seconds
and runs at most once per `RADAR_ALERT_COOLDOWN` (5 minutes) per kind. Alert texts are fixed wording and numbers, no addresses.

| kind | trips when | tune |
|---|---|---|
| `ban_surge` | `RADAR_ALERT_BANS` (5) bans within `RADAR_ALERT_WINDOW` (10m) | 0 turns it off |
| `db_degraded` | an integrity check failed | |
| `disk_low` | writes paused for low disk space or the size cap | |
| `relay_drops` | the relay dropped `RADAR_ALERT_RELAY_DROPS` (100) pings within the window | 0 turns it off; radar-ingest |

An unparsable command (unterminated quote) stops the host from starting. Example: `RADAR_ALERT_CMD=/usr/local/bin/notify-owner {kind} "{text}"`.

## Retention
- Keep at least 30 days of security log (about 10 MiB x 5 files is weeks of normal traffic; a flood is capped by the sampler). Rotated files are not compressed or
  deleted by age: raise `RADAR_AUDIT_KEEP` or archive `security.log.N` with logrotate's `copytruncate`-free approach (move, do not truncate).
- The journal (when no file is set) follows the system's `SystemMaxUse`/`MaxRetentionSec`.
- The log holds client IP addresses, the only personal data; delete it when no longer needed and do not copy it into the repository (`docs/security/secrets-policy.md`).
