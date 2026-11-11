# Security plan (Stage 8): DDoS protection, rate limiting, hardening

Runs **after Stages 1 to 7** are done and reviewed. Same rules as PLAN.md section 5 (TDD, readable, modular). Every control
is a removable **module** plugged into core sockets (a `guard` family), not code scattered through handlers.
Target: a public site on a weak Xubuntu laptop behind a home connection.

## 0. Honest limits (read first)
- A laptop cannot absorb a volumetric (L3/L4) DDoS; nothing in Go can. The only real defence is **not exposing the home IP**:
  use a **Cloudflare Tunnel** (free) or similar so traffic arrives via a CDN, and only the tunnel is allowed through the firewall.
  The app-level work below stops L7 abuse (floods of expensive requests, slowloris, big bodies, scraping, key guessing) and keeps
  the process alive and cheap under load.
- "Cannot be hacked" is not provable. Done = every item in the checklist below is implemented, tested by an attack test, and
  scanned clean by the tools listed in 8.12.

## 1. Threat model (assets, actors, entry points)
Assets: SQLite file (integrity, availability), ingest/view keys, host machine, visitors' browsers.
Actors: anonymous internet scrapers/flooders, key guessers, a malicious or buggy ingest client, a malicious visitor (XSS/CSRF),
a compromised dependency. Entry points: `radar-db` public HTTP (read API, page), `radar-ingest` HTTP (streams/lines), internal
write API, deploy/install scripts, the env file, the SQLite file, the feeder.
**Network layout rule:** radar-db's `/internal/*` and radar-ingest's listener bind to `127.0.0.1` by default; only the page and
read API are meant for the public listener. Ingest from a remote gaming PC goes through the tunnel/VPN with its own key.

## 2. Task breakdown (each task = one subagent run, Sonnet medium; small enough to finish in one context)

Order matters only where "needs" is listed. Each task: write failing attack/abuse tests first, implement, run
`gofmt`/`vet`/`test -race` where cgo allows (else plain test), append Results here, add a STATUS.md line.

| Task | Name | Scope (files) | Needs |
|---|---|---|---|
| 8.1 | Threat model + checklist test | `docs/security/threat-model.md`, `docs/security/checklist.md`, `internal/mod/guard/checklist_test.go` (fails until every checklist item names a test) | - |
| 8.2 | `guard/limits`: server hardening | `http.Server` timeouts (ReadHeader 5 s, Read 15 s, Write 30 s, Idle 60 s), `MaxHeaderBytes` 16 KiB, per-route body caps, max concurrent connections (listener wrapper), max in-flight requests with 503 + `Retry-After`, streaming exemption with idle deadline only for `/v1/stream`, graceful degradation | 1 |
| 8.3 | `guard/ratelimit`: token buckets | per-IP and per-key limiters (cheap, fixed memory: sharded map, LRU/TTL eviction, cap on tracked IPs), separate budgets per route class (page, sweep, search, bounty, write, auth-fail), `429` + `Retry-After`, correct client IP (trust `CF-Connecting-IP`/`X-Forwarded-For` **only** from configured trusted proxies, else RemoteAddr), global fail-safe when the table is full | 8.2 |
| 8.4 | `guard/abuse`: auth abuse + bans | exponential lockout after N failed key attempts per IP, temporary bans with expiry, constant-time compares everywhere, uniform error timing/shape, ban list persisted to a small file, admin unban command (`radar-db guard unban <ip>`) | 8.3 |
| 8.5 | Request validation + injection | every query param/body field: type, length, charset, range, enum (`rs`, `scope`, `grp`, guid format, limits); reject unknown JSON fields, duplicate params, oversized arrays; prove **all SQL is parameterised** (test that scans `sqlstore` for string-built SQL; fuzz search `q` with SQLi/LIKE-wildcard payloads, escape `%` `_`); no path traversal in static/`--site-dir` (clean + prefix check, no symlink escape) | 8.2 |
| 8.6 | Ingest hardening | line length cap 1 MiB, lines/s and bytes/s per source and per key, max concurrent streams per key and globally, max sources, ping size caps already in `Ping.Validate`, sanity bounds on parsed values (coordinates, levels, timestamps within +-24 h of now or configurable for replays), bounded relay buffer with drop-oldest + metric, poisoned-data resistance (names length/charset, no control chars), replay protection by `batchId` window | 8.3, 5 |
| 8.7 | Web hardening (browser side) | response headers on every response: strict `Content-Security-Policy` (`default-src 'self'`, no inline script/style, `frame-ancestors 'none'`, `base-uri 'none'`, `form-action 'self'`), `X-Content-Type-Options`, `Referrer-Policy`, `Permissions-Policy`, `Cross-Origin-Opener/Resource-Policy`, HSTS (only when behind HTTPS, config flag), `Cache-Control: no-store` on API; remove inline handlers/styles from `site/`; XSS audit of every DOM write (`textContent`), key stored in localStorage threat note and option to keep in memory only; CORS allowlist exact-match (never `*` with keys); CSRF: write endpoints require a custom header + key, no cookies used | 6 |
| 8.8 | Data integrity + corruption defence | SQLite: `PRAGMA integrity_check` on start and nightly, `quick_check` endpoint in status, atomic backups with rotation (`radar-db backup`, keep N), refuse to start on a corrupt DB with a clear message and restore instructions, disk-full handling (write API returns 507 and stops accepting, reads continue), size caps (max DB MB, pruning order), file permissions 0600/0700 checks at start, no writes from the read API path (read-only connection for `/api`), idempotent pings so a retry flood cannot inflate counters | 3, 4 |
| 8.9 | Observability + alerting | structured security log (JSON lines, rotated, no secrets/PII beyond IP), counters at `/internal/metrics` (localhost only): 429s, bans, auth failures, in-flight, buffer drops, db size; optional `--alert-cmd` hook on thresholds | 8.3 |
| 8.10 | Host + deployment hardening | systemd sandbox in `deploy/*.service` (`NoNewPrivileges`, `ProtectSystem=strict`, `ProtectHome`, `PrivateTmp`, `PrivateDevices`, `ReadWritePaths` only the data dir, `CapabilityBoundingSet=` empty, `RestrictAddressFamilies`, `SystemCallFilter=@system-service`, `MemoryMax`, `TasksMax`, `LimitNOFILE`), dedicated non-login user, env file mode 0640 root:radar, `ufw` rules script (allow only SSH from LAN + tunnel egress, nothing inbound else), `fail2ban` jail reading the security log, unattended-upgrades note, Cloudflare Tunnel setup guide + Caddy alternative with rate limit and HSTS, key rotation procedure, `deploy/verify-hardening.sh` that checks all of it on the laptop | 7 |
| 8.11 | Supply chain + build | `govulncheck ./...` clean, pinned `go.sum`, vendoring decision, minimal deps list documented (only `modernc.org/sqlite`), reproducible static build flags (`-trimpath -ldflags "-s -w"`), checksums published next to binaries, `go vet`, `staticcheck` or `gosec` run and triaged, GitHub-free: all via scripts in `scripts/security-scan.ps1` and `.sh` | 7 |
| 8.12 | Verification: attack suite + scans | `test/attack/`: Go tests that run real servers and hit them with slowloris, huge headers/bodies, oversized/garbage lines, 10k-connection flood, request flood past limits (assert 429 not crash, memory stays bounded, p99 for a legit client stays low), key brute force, SQLi/XSS/path-traversal payload corpora, malformed ping fuzz (`go test -fuzz` for ParseLine, Ping decode, query parsing: 60 s each), header assertions, CORS tests; manual scan scripts for OWASP ZAP baseline / nuclei if installed (documented, optional); final `docs/security/report.md` with each checklist item -> evidence | all |

## 3. Vulnerability checklist (OWASP Top 10 2021 + API Top 10 mapped to tasks)
A01 broken access control -> 8.4, 8.6, 8.10 (listener split, key roles, internal API loopback only).
A02 crypto failures -> 8.4, 8.7, 8.10 (constant-time keys, HSTS/TLS via tunnel, no secrets in logs/URLs; keys in headers only).
A03 injection -> 8.5 (SQL, header, log, CSV/log-line injection, XSS via player names: names are attacker-controlled data from the combat log).
A04 insecure design -> 8.1, 8.6 (threat model, trust boundaries, untrusted-client data bounds).
A05 misconfiguration -> 8.2, 8.7, 8.10 (timeouts, headers, sandbox, default-deny firewall, no debug endpoints, loud warning when open).
A06 vulnerable components -> 8.11.
A07 auth failures -> 8.4 (lockout, rate limit, rotation).
A08 integrity failures -> 8.8, 8.11 (backups, checksums, no auto-update from the network, idempotent writes).
A09 logging/monitoring -> 8.9.
A10 SSRF -> verify no server-side fetch of user URLs (relay target is fixed config); test asserts the relay only dials the configured address.
API4 resource consumption -> 8.2, 8.3, 8.6. API6 sensitive business flows (scraping) -> 8.3 budgets + pagination caps.
DoS classes: slowloris/slow-POST (8.2), connection exhaustion (8.2), request floods (8.3), amplification via large responses
(8.5 caps + gzip level + `limit` caps), expensive queries (8.5 + query timeouts via `context` and SQLite `interrupt`), memory
exhaustion (8.2/8.6 bounded buffers), disk exhaustion (8.8), CPU exhaustion by regex/parse (8.5/8.6, linear-time parsing only),
hash-flooding of in-memory maps (8.3 bounded tables).

## 4. Keeping each run inside the token budget
- One task per subagent, scoped to the listed files; the prompt points at this file and the task row only, plus
  `PLAN.md` section 5. Agents must not re-read the whole repo: read `docs/plans/STATUS.md` and the packages in scope.
- Each task appends a 10-line Results block and one STATUS.md line, so the next task starts from facts, not from re-exploration.
- Large scans (fuzz, flood tests) write output to files in the scratchpad; agents report only counts and failures.
- 8.12 is split in practice into three runs if needed: (a) network abuse tests, (b) fuzz + injection corpora, (c) scans + report.
- After every task the lead runs the full test suite and reads the diff before the next task starts.

## 5. Done when
All checklist items map to a passing test in `test/attack` or the named module's tests, `govulncheck` and the chosen static
analyser are clean (or triaged in the report), `deploy/verify-hardening.sh` passes on the laptop, and `docs/security/report.md`
lists evidence per item.
