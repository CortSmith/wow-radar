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
| 8.13 | Secrets hygiene (user rule, `docs/security/secrets-policy.md`) | move every secret input to application-specific `RADAR_*` env vars (remove secret flags such as `--db-key`, `--view-keys` on the command line; keep `*_FILE` path vars only), `internal/mod/guard/secrets_test.go` repo scan (key-like strings, PEM blocks, personal paths, e-mails, real-looking names in testdata), `scripts/scan-secrets.ps1|sh`, placeholder-only `deploy/radar.env.example`, redaction tests proving keys never appear in logs/errors/status | 8.4 |
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

## Results 8.1
- Added `docs/security/threat-model.md` (assets, actors, boundaries, entry-point table with current protections and gaps) and
  `docs/security/checklist.md` (98 controls SEC-001..SEC-138, owners 8.2..8.12): 27 done, 13 partial, 58 todo.
- `internal/mod/guard/{doc.go,checklist_test.go}`: `TestChecklistIsWellFormed` (unique ids, owner 8.2..8.12, non-empty
  control/proof, valid status) always runs; `TestChecklistProofsExist` runs with `RADAR_SECURITY_FINAL=1` and checks every
  `done` proof test name exists in `*_test.go`/`*.test.js` (passes today).
- Top gaps found: `/internal/v1/ping` on the public listener with default bind `:8787`; no rate limit/lockout/conn caps (only
  ReadHeaderTimeout 10 s); no CSP/HSTS/other headers; no name/coordinate/timestamp bounds in `Ping.Validate`; `--site-dir`
  symlink escape and no DB integrity/size/permission checks.
- Proof format for later tasks: flip a row to `done` only with a real `path::TestName`.

## Results 8.2
- Core: `Server` socket (`host.Server.Tune/WrapListener/WrapHandler`, internal/core/server.go) and route classes
  (`Routes.HandleFuncOn`, `ClassPublic`/`ClassInternal`, `Host.EnsureListener`, `Host.ClassAddr`); `Run` serves one
  http.Server per enabled class, each with `/healthz`. core coverage 96%.
- Module `internal/mod/guard/limits` (92.6% coverage): server timeouts 5/15/30/60 s, 16 KiB headers, per-listener connection
  cap (512, `--limits-max-conns` / `RADAR_LIMITS_MAX_CONNS`, over-cap connections closed at accept), in-flight cap (256,
  `--limits-max-inflight`, 503 + Retry-After, streams not counted), per-route body caps (`/internal/` 2 MiB, `/api/` 1 KiB),
  `POST /v1/stream` exempt from Read/Write deadlines with a per-read/write idle deadline (`http.ResponseController`).
  Config is validated at Start (host refuses to listen on bad values). Removal test: `TestHostWorksWithoutLimits`,
  `TestLimitsModuleIsRemovable`. Listed in radar-db and radar-ingest `modules.go`.
- Listener split: writeapi declares `--internal-listen` (default `127.0.0.1:8789`, empty disables) and registers on
  `ClassInternal`; the public listener (`:8787`) never serves `/internal/*` (GET 404; POST 405 because the page catch-all is
  `GET /`). Units, `radar.env.example`, READMEs use `RADAR_DB_INTERNAL_LISTEN` and `RADAR_DB_URL=http://127.0.0.1:8789`; the
  real-binary e2e sends pings to the internal listener and asserts the split. relay `--db-url` stays required (no default).
- Checklist: SEC-001..007 done, new SEC-139 (listener split) done. SEC-027 (peer check) left to 8.4.
- Upgrade note: an existing `/etc/radar-live/radar.env` must change `RADAR_DB_URL` to the internal port (documented).

## Results 8.3
- Module `internal/mod/guard/ratelimit` (95.4% coverage), hooked only through core `Server.WrapHandler`; listed before `limits`
  in radar-db and radar-ingest `modules.go` (so 429s are cheap and outermost). Removal tests: `TestHostWorksWithoutRatelimit`.
- Token buckets (float tokens, fake-clock tests) in a 64-shard table keyed by fixed-size `entryKey{class,kind,id[16]}`: IP id =
  address (IPv6 masked to /64), key id = first 16 bytes of SHA-256 of the key (raw key never stored or logged). Hard cap 100k
  identities, TTL eviction (10 m, sweep at most 1/s per shard); a full shard sends newcomers to one shared bucket per class
  (10x an identity's budget): memory fixed, flood cannot evade (`TestTableFullFailsSafe`, `TestMemoryBound`).
- Classes by path: page (default; `/api/v1/status`), sweep, search (other `/api/*`), write (`/internal/*`), ingest (`/v1/stream`,
  `/v1/lines`, `/v1/sources`), authfail (401/403 responses charge it per IP; an IP with it spent gets 429 until it refills).
  Defaults per minute: page 120/60 burst, sweep 30/15, search 60/30, write 1200/400 per IP + 600/200 per key, ingest same,
  authfail 10/10. Write/ingest charge the IP first, so rotating fake keys cannot dodge it.
- Client IP: RemoteAddr unless the peer is in `ratelimit-trusted-proxies` (default none; loopback entry documented for a tunnel on
  the same host); then CF-Connecting-IP, else X-Forwarded-For read right to left skipping trusted hops. `/healthz` from a
  loopback client is never limited.
- Config: `--ratelimit-*` / `RADAR_RATELIMIT_*` (enabled kill switch, per-class `-rate`/`-burst`, `-key-rate`/`-key-burst`,
  max-entries, ttl, headers, trusted-proxies); invalid values stop the host at start. 429 body via `core.WriteError`
  (`rate_limited`), `Retry-After`, optional `X-RateLimit-*`.
- Benchmarks: ~320 ns per limited request, ~140 ns per bucket take (parallel). Checklist SEC-010..016 done.
- Not done here: lockout/bans (8.4), metrics counters (8.9; `table.overflowHits()` is ready), per-source ingest rates (8.6).


## Results 8.4
- Shared helper `internal/netx` (not a module): `ParseTrusted`, `ClientIP` (trusted-proxy rules), `PeerAddr`, `Aggregate` (IPv6 /64),
  `PresentedKey`; moved out of ratelimit (which now delegates; its tests stay green, the moved tests live in netx).
- Module `internal/mod/guard/abuse` (91% coverage), via `Server.WrapHandler` only, listed after `ratelimit` in radar-db and
  radar-ingest `modules.go`. 5 failed key attempts (401/403 on a request that presented a key) in 10 min ban the address 1 min,
  doubling to 24 h; strikes forgotten after one quiet ban length; table capped (10k, active bans never evicted); IPv6 per /64;
  loopback never banned unless `RADAR_ABUSE_BAN_LOOPBACK=true` (a same-host tunnel would otherwise lock everybody out).
  Banned clients get one uniform 429 (+ Retry-After) before any auth work. Trusted proxies: `RADAR_ABUSE_TRUSTED_PROXIES`, else the
  ratelimit setting (read as config, no import). Internal-class routes also check the TCP peer is loopback or in
  `RADAR_ABUSE_INTERNAL_ALLOW` (SEC-027). Config `--abuse-*` / `RADAR_ABUSE_*`, validated at start; kill switch `RADAR_ABUSE_ENABLED`.
- Ban file: `RADAR_ABUSE_BAN_FILE`, default `abuse-bans.json` beside `db-path` (memory only when neither exists, e.g. radar-ingest);
  JSON, 0600, atomic temp+rename, loaded at start with expired entries dropped, corrupt file moved to `.corrupt` and ignored; saved by a
  1 s loop and on stop; a running server re-reads it when the admin command changes it.
- CLI: `radar-db guard list|unban <ip>|ban <ip> <duration>` (`abuse.Admin`, wired through modules.go); never overwrites a corrupt file.
- Core: `RADAR_REQUIRE_KEYS` (auto/true/false; auto = true unless every listener is loopback). `Auth` records roles used by `Require`;
  after start hooks the host refuses to start when a used role has no authenticator (radar-db, radar-ingest). Existing tests, e2e and
  units already set keys or bind loopback; docs and `deploy/radar.env.example` (placeholders only) updated.
- keys: compare is `subtle.ConstantTimeCompare` over equal-length SHA-256 digests (source-guard test); a missing key now takes the same
  hash-and-compare path as a wrong key; tests prove view/ingest/missing/wrong/huge keys give byte-identical failures.
- Checklist: SEC-020, 023..028 done, new SEC-140..142. Not asserted: wall-clock timing differences (same code path, no early return).

## Results 8.5
- New pure package `internal/validate` (stdlib only): `Query` (per-route allow-list of names, duplicates, 2 KiB total, enums, plain-decimal
  ints with ranges, `Flag`, `GUID`, `Text` rejecting control/bidi/zero-width/invalid UTF-8), `DecodeJSON` (unknown fields, trailing data,
  depth 8, arrays 5000, strings 1024; read errors such as MaxBytesError pass through), `CleanText`. Errors carry a code and name the field
  and rule only, never the input. All reported as HTTP 400 `invalid_request`. `domain` keeps its own small copy of the text rules in
  `domain/text.go` because the architecture test forbids domain importing anything of ours.
- readapi: every route uses `validate.Query` (limit<=100000, offset<=1e6, mapId<=65535, since<=TimestampMaxMs, activeWithin<=30 d, q<=64
  bytes), `nosniff` on all responses. writeapi: `validate.DecodeJSON`, then `Ping.Sanitize()` (repairs names/zone/source/chart names, caps
  past names, counted and logged at debug) then `Ping.ValidateAt(clock)`; `writeapi.WithClock` option added.
- domain: `Ping.Validate()` now bounds names, coordinates (|x|,|y|<=100000), level<=120, maxHp, mapId<=65535, reaction, fix kind, deltas
  (<=1e6 counts, <=1e10 sums), 20000 fixes per ping, deaths (id pattern, Player guids, 50 assists); `ValidateAt(now)` adds the timestamp
  window (2004-01-01 .. now+24h; zero = unset). Plain `Validate()` skips only the window, so memstore/sqlstore/relay and synthetic-time tests
  are unchanged. Error messages no longer echo values (incl. `ParseRuleset`, `Sector`, `SearchQuery`). Renamed my level constant to
  `PingLevelMax` (merge.go already has `LevelMax`=200).
- Fixtures corrected as truly invalid: sqlstore deaths without victim, readapi `guid=a`, writeapi timestamps now around a fake clock.
- sqlstore: `sqlinject_test.go` AST scan (SQL args must be constants/whitelisted fragments, Sprintf only `%d`, filter clauses literal; a
  self-test proves it flags concatenation/Sprintf/param SQL), 40-payload SQLi/LIKE corpora for Search/Sweep/Trail/BountyPlayer. No injection found.
- static: `safeDir` (ValidPath, EvalSymlinks, real-root prefix check) used for `--site-dir`; `\` and NUL in the request path are 404; tests for
  `..`, `%2e%2e`, backslash, NUL, absolute and drive paths, symlink/junction escape (file symlink tests skip without privilege on Windows; the
  directory junction test runs). nosniff on 404s.
- site: new `site/test/xss.test.js` (sink grep over all shipped JS/HTML, scanner self-test, name rendering stays text). Found and fixed one
  issue: `?maps=//evil.host/` or `javascript:` was accepted as map folder (third-party image/index fetch); `isSafeMapsFolder` now allows only
  same-origin relative paths. No innerHTML/eval/handler/javascript: sinks exist.
- Fuzz (25 s each, no crashes, no new corpus failures): FuzzQuery 371k execs, FuzzDecodeJSON 6.7M, FuzzPingJSON 3.9M, sqlstore FuzzSearchQuery 500k.
- Checklist: SEC-030..038 done (SEC-039 sweep size cap, SEC-041 gzip left open; SEC-042 partial), new SEC-143..146.

## Results 8.7
- New guard module `internal/mod/guard/headers` (registered right after the store in `cmd/radar-db/modules.go`, so it is the outermost
  `WrapHandler` and even 429/503 answers carry the headers; applies to all listener classes). Every response gets strict CSP
  (`default-src/script-src/style-src 'self'`, `img-src 'self' data:`, `connect-src 'self' <RADAR_HEADERS_API_ORIGINS>`, `object-src 'none'`,
  `base-uri 'none'`, `form-action 'self'`, `frame-ancestors 'none'`), nosniff, `Referrer-Policy: no-referrer`, `Permissions-Policy` denying
  every feature, COOP/CORP same-origin, `X-Frame-Options: DENY`; `Cache-Control: no-store` on `/api/*`; HSTS (1 year, no preload) only with
  `RADAR_HEADERS_HSTS=true` (use behind HTTPS/tunnel). API origins are validated at start (plain `scheme://host[:port]`, no `*`, `;`, quotes or
  paths) so a setting cannot inject CSP directives. Asset caching (static module) is untouched. Removal test: host without the module serves the page.
- readapi CORS: setting renamed `cors-origin` to `cors-origins` (`RADAR_CORS_ORIGINS`, comma list, exact match only; `*`, `null`, case or port
  or trailing-slash variants never match); `Vary: Origin` whenever a list exists; never `Allow-Credentials`; preflight with an unlisted Origin
  gets 403 without echoing it. `deploy/radar.env.example` still says `RADAR_CORS_ORIGIN` (deploy/ was out of scope): needs the rename.
- static: `/.well-known/security.txt` from `RADAR_SECURITY_CONTACT` (mailto:/https: or bare e-mail; control characters rejected; Expires = now+1y);
  404 when unset. No contact data in the repo.
- site: view key is memory-only by default; "Remember on this device" checkbox opts into localStorage, unticked save erases it. New
  `site/test/csp.test.js` (markup parse: no inline script/style/on*/external URL/blocked elements, imports resolve, no cookies/workers/blobs).
  No inline code existed, nothing had to be removed. Go: `headers_test.go` (all headers on 200/404/405/418 responses, API origins, HSTS flag,
  bad origins refuse start, no Set-Cookie), `cmd/radar-db/headers_test.go` (real page), readapi CORS tests.
- Checklist: SEC-070..072, 077, 078 done. SEC-081 (pprof/debug attack test) left to 8.12.

## Results 8.8
- sqlstore: `preflight` (read-only, immutable without WAL so refused files stay byte-identical, no -wal/-shm created) classifies a bad file as
  `ErrNotDatabase`, `ErrNewerSchema` or `ErrCorrupt`; the error carries `radar-db restore` instructions. `Verify(path)` shares it. Tests use crafted
  garbage, page-smashed and newer-schema files and assert the bytes and directory listing are unchanged.
- Runtime integrity: `Options.IntegrityEvery` timer (module default 24h, `RADAR_DB_INTEGRITY_EVERY`) runs `integrity_check`; failure = sticky degraded
  (`domain.ErrDegraded`, writeapi 503 `store_degraded`, ERROR log with restore steps), reads continue. `/api/v1/status` gains `integrity`, `lastCheck`,
  `full` through an optional `Health()` interface (domain.Health); no paths or error text are exposed.
- Space: `RADAR_DB_MAX_MB` 2048, `RADAR_DB_MIN_FREE_MB` 200; writes get `domain.ErrStorageFull` (writeapi 507 `insufficient_storage`) and recover on the
  next probe (throttled 1/s, injectable `DiskFree` and clock in tests). Real free-space probe in build-tagged files (linux/darwin Statfs, windows
  GetDiskFreeSpaceEx, other = off), CGO off. Pruning order is documented in schema.md and tested (fixes of least recently seen first, then ids).
- Read-only: reader pool write attempts (INSERT/DELETE/UPDATE/CREATE/DROP/PRAGMA) fail; source guard keeps read paths off `s.writer`.
- Backups: `radar-db backup --keep N <dir>` (timestamped, 0600, verify, link-into-place so nothing is overwritten, prune only `radar-*Z.db`);
  `radar-db restore <file>` verifies, takes the new `sqlstore.AcquireLock` (held by the server module), stages a verified copy, moves db and WAL files to
  `.pre-restore-<time>` (nothing deleted). `deploy/backup.cron` uses `--keep 14`; README-xubuntu section 6 documents backup, restore, limits.
- Permissions: dir 0700 / db 0600 created at start; wider existing modes warn (`permProblem` pure, table-tested; fs tests skip on Windows).
- Retry flood: 10k replays of one batchId leave contacts, events, fix count and batch rows unchanged, about 25 microseconds each.
- Checklist SEC-090..098 done, new SEC-150, 151. Domain gained `ErrStorageFull`, `ErrDegraded`, `Health` (domain/health.go).
- Notes: `deploy/install.sh` makes the data dir 0750 so radar-db logs a permission WARN until `chmod 0700` (README says so; install.sh left to 8.10).
  Unix file-mode tests and the flock path compile (GOOS=linux vet) but were run only on Windows here. `internal/client` TestLiveLogReachesTheDatabaseThroughTheRealStack
  failed in the full run (memstore/ingest path, unrelated to sqlstore).

## Results 8.6
- streamapi (94.8% coverage) gained token-bucket limits, all `RADAR_STREAM_*` (0 = off, negatives stop the host at start): per source
  `stream-line-rate` 5000/s (burst 10000) and `stream-byte-rate` 4 MiB/s (burst 8 MiB); per key (SHA-256 prefix, raw key never kept)
  `stream-key-line-rate` 20000/s and `stream-key-byte-rate` 16 MiB/s. Over the limit the reader sleeps (no read = TCP backpressure);
  unbroken throttling longer than `stream-rate-patience` (30 s) ends the stream with `ended: "rate"`; a `/v1/lines` batch whose wait
  would exceed it gets 429 code `rate` + Retry-After. Summary and `GET /v1/sources` show `throttledMs` and `rejected`.
  A bulk import of an old log at full speed is cut after 30 s: raise the rates for imports.
- Concurrency: `stream-max-per-key` 4, `stream-max-streams` 32, `max-sources` 16 (429 `too_many_streams` / `too_many_sources`).
- Slowloris: a line unfinished `stream-line-timeout` (30 s) after its first byte ends the stream (`ended: "slow"`); `stream-idle` as before.
  Found and fixed by the many-connections test: Go drains the unread body before sending a 4xx, so a refused stream with an endless body
  parked its handler. Every early refusal (params, caps, 401/403 from Auth) now carries `Connection: close`.
- logparse: `CheckEvent` (module `logparse-window` 24 h default, `logparse-allow-replay` for old logs, which still caps the future and
  pre-2004) rejects non-finite, negative or huge HP/amount, coordinates beyond +-100000, map id, level over 120, long GUIDs/record names;
  `ErrOutOfBounds` wraps `ErrBadLine`, so streamapi counts it as rejected and nothing is applied. `SanitizeEvent` cleans names and zones.
  Upgrade note: tests and the desktop client importing old logs must set `RADAR_LOGPARSE_ALLOW_REPLAY=true` (e2e, client integration and
  cmd e2e fixtures now pass `--logparse-allow-replay`).
- contactx/bountyx: deltas clamped (counts 0..1e6, sums 0..1e10, NaN to 0); each contact/death validated alone and bad ones dropped with a
  logged count (one odd GUID used to invalidate and lose the whole ping); Ping.Sanitize before publish; extractor ignores negative or
  non-finite HP and amounts; bountyx credits capped at 4096 per flush (`Stats.DroppedCredits`); idle sources expire after 15 min so
  throwaway source names cannot fill the 256-source tables. 1M distinct GUID tests keep heap growth under 64 MiB.
- relay (96.9%): Sanitize before Validate (dirty names repaired and counted, not refused), replay window of 4096 batch ids
  (`Stats.Duplicates`), JSON-tagged `Stats` (dropped, pending, retries, rejected, sent, sanitized), db-url must be a plain http(s) base
  (no credentials, query, fragment, spaces; errors never echo it), backoff minimum 10 ms. SSRF, redirect (301/302/303/307/308), NaN/huge
  delta and no-spin tests added. Not done here: exposing the relay stats as `/internal/metrics` (8.9).
- Checklist: SEC-050..053, 055..061, 063 done, 062 stays partial (fuzz = 8.12); new SEC-147, 148, 149, 160..163.
  Hostile-client tests: slowloris, never-closing client, 200k tiny lines, 4 MiB binary garbage, 256 MiB single line, 120 simultaneous
  connections, full-chain poisoned log (`cmd/radar-ingest/e2e/hostile_test.go`). Not asserted: re-uploading the same log within 24 h counts
  twice (batch ids are new); the limits module caps connections, this module caps streams.

## Results 8.11
- `docs/security/supply-chain.md` documents the dependency allowlist (sqlite, go-webview2, x/sys + reviewed indirect closure), the
  no-vendoring decision, govulncheck/staticcheck/gosec triage, reproducible-build check and laptop verification (`sha256sum -c`).
- govulncheck (installed to a temp GOBIN, network OK): with the installed go1.27.1, 13 reachable vulnerabilities, all Go standard library
  (net/http, http2, textproto, crypto/x509, crypto/tls, html/template), none in modules; with go1.27.4: clean. Decision needed: pin
  `toolchain go1.27.4` in go.mod (not edited here) or always build with `GOTOOLCHAIN=go1.27.4`.
- go vet clean. staticcheck: ST1018 in attack tests (suppressed, intentional), two dead-code items fixed (contactx `pastNames`,
  readapi `invalidf`), one open SA4006 in `sqlstore/sqlinject_test.go`. gosec: 36 findings, 29 rule/file groups all accepted with
  reasons and recorded in `scripts/gosec-accepted.txt`; one new G204 in `guard/audit/alert.go` is untriaged (owner of 8.9/8.10).
- `scripts/security-scan.ps1|sh` run all checks, print SKIPPED loudly for missing tools, exit 1 on failure. gosec baseline compares
  rule + file, so line moves do not break it. The shipped scan currently fails on gofmt (test files in cmd/radar-db and
  cmd/radar-ingest from other tasks), staticcheck SA4006 and gosec G204 audit/alert.go, which are not 8.11 files.
- `internal/mod/guard/supplychain_test.go`: go.mod allowlist (direct + indirect, no replace), go.sum pins, no vendor/, build flags,
  scan scripts present, no network-update strings. Build scripts now use `-trimpath -buildvcs=false -ldflags "-s -w -buildid="`;
  radar-db and radar-ingest linux/amd64 built twice give identical SHA256 (hashes in supply-chain.md).
- Checklist: SEC-120, 121, 122, 124 done (proofs re-pointed to supplychain_test.go), SEC-123 partial until the two open items close.

## Results 8.13
- core: `Setting.Secret` (no flag is registered; `--name` or `--name=v` returns `SecretFlagError` naming `RADAR_NAME`, never the value;
  parse errors of a secret do not echo it), `Config.Usage()` (what `--help` prints: secrets as `env RADAR_X (secret, no flag)`, default
  `<set>`/`<unset>`), `Config.Dump()` (masked, safe to log), `Config.EnvNames()`, `core.RejectSecretFlags` for programs with their own flags.
  Marked secret: `view-keys`, `ingest-keys` (keys), `db-key` (relay), `alert-cmd` (8.9, may carry a webhook token). `*-keys-file` stay flags
  and env (they are paths).
- `radar-feed --key` removed (refused with a pointer to `RADAR_FEED_KEY`); `--key-file` kept and now also `RADAR_FEED_KEY_FILE`; `--help`
  prints usage. `radar-client --key/--ingest-key` refused with a pointer to `RADAR_CLIENT_INGEST_KEY`. The client already kept the key out of
  settings.json, status and console; new `TestKeyNeverAppearsInAnyReply` covers every endpoint plus a bad request echoing the key.
- Tests/e2e that passed keys by flag now pass them by environment (cmd/radar-db, cmd/radar-ingest/e2e, test/e2e, keys, relay).
- Redaction tests: `cmd/radar-db/redaction_test.go` (logs, status, 401/400 bodies, `--help`), feed (`TestRunNeverLogsTheKey`,
  `TestKeyNeverReachesLogsOrEvents`), core masking tests.
- `deploy/radar.env.example`: placeholders only (`CHANGE_ME_*`), the keys module refuses any `CHANGE_ME` key, `RADAR_CORS_ORIGIN` fixed to
  `RADAR_CORS_ORIGINS`, every variable of radar-db and radar-ingest documented (`envdoc_test.go` in both cmd packages fails otherwise, which also
  caught the 8.9 audit variables, now documented). `install.sh`: keys from `radar-db keygen` into `/etc/radar-live/radar.env` 0640 root:radar with
  `umask 077`, printed once; data and backup dirs now 0700 (8.8 finding); `dash -n` clean. `deploy/secrets_deploy_test.go` checks all of it.
- Repo scan: `internal/mod/guard/secrets_test.go` (rules pem, token, bearer, aws, path, email, ipv4, file; scans what git would commit),
  `docs/security/secrets-allowlist.txt` (reasons mandatory, stale entries fail), `scripts/scan-secrets.ps1|sh`. First full run: no real secret,
  e-mail, personal path or public host found; 9 allowlist entries, all synthetic test inputs (public-looking client IPs in limiter tests, one
  fake key constant, the scanner's own samples). Added `*.exe` to .gitignore (an untracked `radar-db.exe` sat in the tree).
- Checklist SEC-170..177 done; checklist owner pattern now allows 8.13.
- Open: the radar-client console prints its per-run local UI token in the page address (needed to open the UI, not the ingest key). The go-radar
  history/settings files outside radar-live (possible real names) are not covered by this scan.

## Results 8.9
- New module `internal/mod/guard/audit` (coverage about 91%) plus two small core sockets: `host.Audit` (Emit/EmitRequest of `AuditEvent{Event, IP, Class, Reason, Count}`,
  no free-form fields; observers; client-IP resolver; request-outcome observers; 128-event backlog replayed to the first observer so startup warnings survive) and
  `host.Metrics` (named counters/gauges evaluated at scrape time). Modules emit/register without importing audit; `TestNothingBreaksWithoutTheAuditModule` and
  `TestAuditModuleIsRemovable` prove removal. In `cmd/radar-db/modules.go` audit comes after writeapi (it only adds a route to the internal listener writeapi declares).
- Security log (`docs/security/logging.md`): slog JSON handler, fixed field order `ts,event,ip,class,reason,count`, `RADAR_AUDIT_LOG` file (0600, size rotation `RADAR_AUDIT_MAX_MB`
  x `RADAR_AUDIT_KEEP`, pure Go) or stderr/journal. Sampler: one line per event/ip/class/reason per `RADAR_AUDIT_SAMPLE` (1 m) with a count, bounded window table (4096),
  200 lines/s ceiling with `audit_overflow`. Fields are reduced to a safe charset; tests send a key in header, query and body through the real modules and grep the log and metrics.
- Emitters (one line each): abuse (`auth_failure`, `ban`, admin `ban`/`unban` seen on file resync, `internal_denied`, disabled warning), ratelimit (`rate_limited`, disabled warning),
  limits (`conn_refused`, `overloaded`), readapi/writeapi (`validation_reject`), streamapi (`stream_refused`), sqlstore (`disk_full`, `integrity_degraded`, once per transition via new `Options.OnEvent`).
- Metrics `GET /internal/metrics` (hand-written Prometheus text) on the internal listener only; 404 on the public one (tested). Series: requests by class and status family, 429s, bans,
  auth failures, rate limited, bans active, in-flight and connections per class, relay dropped/pending/bytes/sent/retries, stream sessions and rejected lines, DB size, integrity degraded,
  writes paused, last write age. The audit module declares the internal listener with an empty default when nobody else did, so radar-ingest serves metrics only when
  `RADAR_INTERNAL_LISTEN` is set (suggest 127.0.0.1:8790; radar-db keeps 8789).
- Alerts: `RADAR_ALERT_CMD` (env only via `Setting.Secret`) split without a shell, `{kind} {text} {count}` substitution, child gets only PATH plus `RADAR_ALERT_*`, 5 s limit, 1 per 5 min per
  kind (`RADAR_ALERT_COOLDOWN`), kinds `ban_surge` (`RADAR_ALERT_BANS`/`RADAR_ALERT_WINDOW`), `db_degraded`, `disk_low`, `relay_drops` (counter delta, `RADAR_ALERT_RELAY_DROPS`). Fake-runner tests.
- fail2ban filter and jail in logging.md; the filter regex is applied to real lines in `cmd/radar-db/audit_test.go`.
- Checklist: SEC-100..102 done (rewritten to the real tests), new SEC-180..183, SEC-057 evidence updated.
- Notes: `deploy/radar.env.example` needs the block documenting the new RADAR_AUDIT_* / RADAR_ALERT_* variables (cmd envdoc tests require it; owner 8.13); deploy units (8.10) should create the log directory
  (`/var/log/radar`, 0700 radar) and set `RADAR_AUDIT_LOG`; the fail2ban jail itself ships with 8.10. Loopback clients behind a tunnel appear as 127.0.0.1 unless the tunnel is a trusted proxy.

## Results 8.10
- Nothing here was executed on a real Linux host (author on Windows): shell scripts are checked with `sh -n` (dash) and the ufw dry run is executed in a Go test; systemd, ufw, fail2ban, `systemd-analyze security` and the real install must be confirmed on the laptop with `deploy/verify-hardening.sh`.
- Units: added ProtectProc=invisible, ProcSubset=pid, SystemCallFilter=@system-service then ~@privileged @resources (+ SystemCallErrorNumber=EPERM so a blocked call is an error, not SIGSYS), RemoveIPC, KeyringMode=private, MemoryHigh=160M (MemoryMax 192M kept), TasksMax=256, CPUQuota=150%, StartLimitIntervalSec/Burst in [Unit], StateDirectory/LogsDirectory/RuntimeDirectory (0700), `RADAR_AUDIT_LOG` per unit under /var/log/radar. radar-ingest has no writable path. MemoryDenyWriteExecute stays (Go runtime and transpiled modernc sqlite need no W+X; documented fallback in hardening.md if the first start shows SIGSEGV). Score target 2.5 or lower, to be measured on the laptop.
- `deploy/ufw-setup.sh`: default deny, SSH `ufw limit` from the LAN CIDR only, 8787 from the CIDR (or `--tunnel`: none, `--public`: anywhere), 8788/8789 never, IPv6 on, `--dry-run`, rejects /0, junk and injection, rollback in the header.
- `deploy/fail2ban/radar-live.conf` + `jail.local` (installed to jail.d by `install.sh --with-fail2ban`); the regex is run in `deploy_test.go` against synthetic lines (IPv4, IPv6, non-matching events, a field-injection line). `logging.md` names the filter `radar`; the shipped one is `radar-live`.
- `deploy/verify-hardening.sh` (read-only; PASS/FAIL/SKIP, exit 1 on FAIL), `install.sh` (root check, flock, mandatory SHA256SUMS verified before anything is installed, log dir 0700, env file mode re-asserted, optional ufw/fail2ban), `uninstall.sh` (removes the jail, purge also logs).
- `docs/security/hardening.md`: limits of defence (volumetric DDoS only via CDN), sandbox table, ufw, Cloudflare Tunnel with the token in a 0600 root-only file and a hardened cloudflared unit (not `service install <token>`), Caddy alternative that strips a forged CF-Connecting-IP, origin-IP checklist, fail2ban, unattended-upgrades, SSH key-only, key rotation, backup drill, log rotation, metrics to watch.
- Tests: `deploy/deploy_test.go` (units, writable paths, install, `sh -n` of all scripts, ufw dry runs and bad input, fail2ban regex, verify script read-only, docs present, placeholder-only addresses). Checklist: SEC-110, 111, 113..119 done, SEC-112 partial (the program default listen is still :8787).
- For the lead: `scripts/build.sh|ps1` copy only top-level files of deploy/, so `deploy/fail2ban/` is not in dist/ and `--with-fail2ban` needs the folder copied by hand (scope forbade editing the build scripts).
