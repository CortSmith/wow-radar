# radar-live threat model (task 8.1)

Grounded in the code as built by stages 1 to 6 (stage 5 ingest was still being written). Companion: `checklist.md`
(every control, its owning task and proof). Plan: `docs/plans/security-plan.md`.

## 1. Assets

| Asset | Why it matters |
|---|---|
| SQLite file (`radar.db`, WAL) | Integrity of contacts, trails, bounty ledger; availability of the page. Single file on a laptop disk. |
| View keys and ingest keys | Gate the read API and the write API. Compared as SHA-256 hashes (`internal/mod/keys/keys.go`). |
| Host machine (Xubuntu laptop, home IP) | Runs both binaries; home IP must not be exposed (security-plan section 0). |
| Visitors' browsers | The page renders attacker-controlled strings (player names); the view key sits in `localStorage`. |
| Availability | A weak laptop: CPU, RAM (<60 MB budget), disk, file descriptors, uplink. |
| Env file / key files | Hold the keys in clear text on disk. |

## 2. Actors

| Actor | Capability assumed |
|---|---|
| Anonymous internet client | Can reach the public listener: floods, slowloris, scraping, key guessing, injection payloads. |
| Key guesser | Brute forces `X-View-Key` / `X-Ingest-Key`. |
| Malicious or buggy ingest client | Holds (or steals) an ingest key, or reaches an open ingest port; sends garbage lines, huge lines, many streams. |
| Log author (in-game player) | Never touches the system directly but controls bytes inside the combat log: character names, realm tokens, text fields. Their data flows to the DB and into every visitor's browser. |
| Malicious visitor | XSS/CSRF attempts, cross-origin calls, key theft from `localStorage`. |
| Compromised dependency / build | Supply chain (only `modernc.org/sqlite` plus its transitive set). |
| Local user on the laptop | Can read the env file, DB file, process list. |

## 3. Trust boundaries and data flow

```
 [WoW client log] --(feed/any client)--> B1 --> radar-ingest :8788 --B2--> POST /internal/v1/ping --> radar-db :8787 --B3--> SQLite
   (attacker-controlled bytes)                  parse (logparse) + extract                          writeapi -> sqlstore       file on disk
                                                (contactx, bountyx) + relay (bounded buffer)
 [Browser] --B4--> GET /  (static, embedded site)   GET /api/v1/* (readapi, X-View-Key)  <-- same radar-db listener
```

- B1 (untrusted network to ingest): lines are untrusted bytes; only the ingest key (planned `streamapi`) authenticates.
- B2 (ingest to db): fixed configured URL, `X-Ingest-Key`, redirects not followed (`relay`).
- B3 (process to disk): sqlstore with parameterised SQL; DB file permissions are whatever the umask gives.
- B4 (public internet to radar-db): the page, the read API and the internal write API are all on ONE listener today.
- B5 (browser DOM): names/zone strings from the DB cross into the DOM; must only be written as text.

Sequence: log line -> `logparse.ParseLine` (1 MiB cap) -> `contactx`/`bountyx` aggregate in bounded memory -> `domain.Ping` ->
`relay` buffer -> HTTP POST -> `writeapi` (`Ping.Validate`, `DisallowUnknownFields`, body cap) -> `sqlstore.ApplyPing`
(idempotent by `batchId`) -> `readapi` sweep -> browser every 60 s.

## 4. Entry points and current protections

| Entry point | Reached by | Current protection (file) | Gaps |
|---|---|---|---|
| `GET /` and assets | anyone | path clean + embed FS (`mod/static/static.go`); `nosniff`, ETag | No CSP/HSTS/Referrer/Permissions/COOP/CORP headers; `--site-dir` uses `os.DirFS` (follows symlinks) |
| `GET /api/v1/*` | anyone with view key | `Auth.Require(RoleView)`, constant-time hashed compare (`core/auth.go`, `mod/keys/keys.go`); param parsing with enums, ints, length 64 (`mod/readapi/params.go`); `limit` clamp; 5 s query timeout; `no-store`; exact-origin CORS (`readapi/module.go`) | No rate limit, no lockout; duplicate params and charset not checked; sweep result size uncapped; open when no key configured |
| `POST /internal/v1/ping` | ingest key | `RoleIngest` auth, 8 MiB body cap, unknown fields and trailing data rejected, `Ping.Validate` caps (500 contacts, 200 charts, 1000 deaths, guid and batchId patterns) (`mod/writeapi`, `domain/ping.go`) | Served on the public listener; no timestamp/coordinate/name bounds; no rate limit; no disk-full handling |
| `GET /healthz`, `/api/v1/status` | anyone (healthz) | trivial handlers | Status needs view role; no integrity or metrics endpoint yet |
| Listener (`core/run.go`) | network | `ReadHeaderTimeout` 10 s, global body cap via `Middleware`, panic recovery, request id | Default `:8787` (all interfaces); no Read/Write/Idle timeouts; no `MaxHeaderBytes`; no connection or in-flight limits |
| Radar-ingest (`/v1/stream`, `/v1/lines`) | feeder | In progress (stage 5). `logparse.MaxLineBytes` 1 MiB; `relay` bounded buffer, drop-oldest | Not yet reviewed: line reader bound, per-source rates, stream limits, key check |
| Relay dial | config | Fixed URL, fixed path, redirects not followed (`mod/relay`) | Nothing yet stops a non-http scheme or unsafe URL in config at startup (verify) |
| Log content (names, realms, text) | in-game players | `ParseLine` never panics on garbage; `Ping.Validate` guid pattern; CSV parsing is linear | No name length/charset/control-char sanitising; no coordinate/level/time sanity bounds |
| SQLite file | local disk | WAL, parameterised SQL (only `Sprintf` on ints for PRAGMA), LIKE avoided via `instr`, rejects corrupt/newer schema, fix cap and batch-id pruning (`mod/sqlstore`) | No `integrity_check` schedule, no max DB size, no permission check, no read-only connection for the read path, backup has no rotation |
| Browser page | visitors | No inline script/style/handlers, no unsafe HTML sinks, no external requests (tested in `site/test/safety.test.js`) | Key kept in `localStorage`; no CSP header to enforce the above |
| Env/key files | local | Keys never in error messages or logs; min length 16 (`keys`); keygen 256 bit | No file mode check; no rotation procedure |
| Deploy/install | operator | Stage 7 not built | systemd sandbox, firewall, fail2ban, tunnel all pending (8.10) |

## 5. Top risks, in priority order

1. Public exposure of the write API: `/internal/v1/ping` shares the public listener (A01).
2. No request rate limiting or connection caps; only `ReadHeaderTimeout` exists (API4, DoS).
3. Missing browser hardening headers (CSP etc.): the page is CSP-ready but nothing enforces it (A05/A03).
4. Attacker-controlled names and values pass validation unbounded (A03/A04): length, charset, coordinates, timestamps.
5. Open-by-default auth with only a log warning, on a default all-interface bind (A05/A07).
6. No lockout on key guessing (A07). 7. DB integrity/disk-full/permissions (A08). 8. No security log or metrics (A09).

## 6. Out of scope / accepted

Volumetric L3/L4 DDoS (mitigated only by a tunnel/CDN, 8.10), a compromised host or root user, and a malicious logged-in
operator. TLS is terminated by the tunnel or Caddy, not by the Go binaries.
