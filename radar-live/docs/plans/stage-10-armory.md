# Stage 10: armory module (import character data from the WoW Forever armory)

STATUS: PLAN ONLY. Nothing here has been executed. Read `../../PLAN.md`, `docs/security/secrets-policy.md` (binding),
`docs/security/threat-model.md`, `docs/plans/STATUS.md` first. Subagents: Sonnet, medium effort, one task each, lead reviews every task
(gofmt, vet, `go test ./...`, node tests, `scripts/scan-secrets.ps1`) before the next starts. Agents never commit or push.

## 1. Goal
Combat logs do not contain class, race, guild or gear (see HANDOFF.md). The armory does. Add an optional, removable module that looks up
each new player contact in the WoW Forever armory API, stores a small **profile** (class, race, level, guild, faction, a few extras), and
lets the page show it (class-coloured dots, a profile panel, search by class/guild). The radar must keep working with the module removed.

## 2. Open question that gates everything (Task A0)
The user holds an OAuth-style `clientId` / `clientSecret` for "WoW Forever". We do **not** yet know the API: base URL(s), token URL, whether
it is the client-credentials flow, endpoint paths, namespaces, per-realm lookups, rate limits, terms of use, response shapes. Do NOT assume
it is Blizzard's public API. Task A0 must pin this down from the official documentation (the user supplies the docs URL) before any code.

## 3. Where the secrets live (user instruction: never show them to Claude)
- Per the secrets policy they are application-specific environment variables, never in the repo, flags, logs or chat:
  `RADAR_ARMORY_CLIENT_ID`, `RADAR_ARMORY_CLIENT_SECRET`, or file-path variants `RADAR_ARMORY_CLIENT_ID_FILE` /
  `RADAR_ARMORY_CLIENT_SECRET_FILE` (preferred: the value is not in `radar.env` or `systemctl show`).
- Recommended on the laptop: two files outside the repo, owned by root, readable only by the service group, loaded with systemd
  `LoadCredential=` (or `EnvironmentFile=` pointing at a 0600 file). The user types the values locally, hidden, so no assistant sees them:
  ```sh
  sudo install -d -m 0750 -o root -g radar /etc/radar-live/secrets
  read -rs ARMORY_ID;     printf '%s' "$ARMORY_ID"     | sudo install -m 0640 -o root -g radar /dev/stdin /etc/radar-live/secrets/armory-client-id;     unset ARMORY_ID
  read -rs ARMORY_SECRET; printf '%s' "$ARMORY_SECRET" | sudo install -m 0640 -o root -g radar /dev/stdin /etc/radar-live/secrets/armory-client-secret; unset ARMORY_SECRET
  ```
  then in `/etc/radar-live/radar.env`: `RADAR_ARMORY_CLIENT_ID_FILE=/etc/radar-live/secrets/armory-client-id` and
  `RADAR_ARMORY_CLIENT_SECRET_FILE=/etc/radar-live/secrets/armory-client-secret` (paths only, no secrets).
- Treat the client ID as a secret too. If either value was ever pasted anywhere, rotate it at the provider.
- The armory module runs in **radar-ingest** (the process that already talks outward); radar-db never sees the credentials. The access
  token is held in memory only and never logged or stored.

## 4. Design (modular, removable)
```
radar-ingest: contactx (new player seen) -> bus event -> armory module -> queue (dedupe, TTL cache, budget)
              -> armory client (OAuth token, HTTPS to the fixed API host) -> domain.Profile -> Sinks -> relay -> radar-db
radar-db:     writeapi (profiles in a ping) -> store (optional ProfileStore) -> readapi /api/v1/profile(s) -> page profile feature
```
- New plain type `domain.Profile {guid, rs, name, realm, class, race, level, guild, faction, fetchedAt, status ok|notfound|error}`.
- Store extension is an **optional interface** (`core.ProfileStore`) implemented by memstore and sqlstore; hosts without it skip profile
  routes. Removing the module (one line in `cmd/radar-ingest/modules.go`) leaves everything else untouched; removing the page feature file
  does the same on the front end. An architecture test keeps modules independent as usual.
- Outbound calls only ever go to the configured API host (HTTPS, no redirects, fixed path templates). **Character names come from combat
  logs and are attacker-controlled**: they are validated (charset/length), URL-path-escaped, and never used to build a host or a full URL.

## 5. Tasks (each = one subagent run; dependencies in brackets)
**A0 Discovery (lead + user, no code).** User provides the official API docs URL. Produce `docs/plans/armory-api-notes.md`: base URLs, token flow,
endpoints and parameters, response samples (synthetic copies only), rate limits, caching rules, terms of use, error codes, how a realm and a
character are addressed (our logs give `Name-Realm`, see the realm-suffix logic), what fields exist. Decide the exact Profile fields. GATE for all.

**A1 Domain + store (after A0).** `domain.Profile` + validation/sanitising, `Ping.Profiles` (caps: <=200 per ping), optional `core.ProfileStore`,
memstore + sqlstore (`profiles` table, migration to schema v2, indexes on rs+guid and rs+lower(name)), `storetest` cases for upsert, newest wins,
status handling, pruning/caps, persistence across reopen. TDD. Files: `internal/domain`, `internal/core`, `internal/mod/memstore`,
`internal/mod/sqlstore`, `internal/mod/storetest`.

**A2 Armory client (after A0).** `internal/mod/armory/client` (pure package, no host): token fetch + proactive refresh (client credentials), single
flight, HTTP client with timeouts, no redirects, TLS verification, response size cap, retries with backoff + Retry-After, outbound rate limiter, mapping
to `domain.Profile`. Credentials read from env/`*_FILE` only; a `Secret` type that prints `<redacted>`. Tests with an `httptest` fake armory (success,
404, 401 then refresh, 429, 5xx, garbage JSON, huge body, redirect attempt, token expiry), plus a test that no log line or error contains the secret.

**A3 Enrichment module (after A2, A1 types).** `internal/mod/armory` (module `armory`, listed only in `cmd/radar-ingest/modules.go`): watches new
player contacts (via the core bus/Sinks, no import of contactx), bounded dedupe queue, TTL cache (default re-check after 7 days, shorter for errors,
negative cache for not found), concurrency cap, hourly request budget, circuit breaker when the API is down, graceful stop. Emits profile pings through
the sink socket so relay forwards them. Settings `RADAR_ARMORY_*` (enabled flag default **off**, base URL, ttl, budget). Audit events and metrics via the
existing sockets. Removal test: ingest without the module works.

**A4 DB API (after A1).** `writeapi` accepts and validates profiles (ingest key), `readapi` adds `GET /api/v1/profile?rs=&guid=` and
`GET /api/v1/profiles?rs=&guids=` (<=100, view key, rate-limited), optional `class`/`guild` filters on `/api/v1/contacts`. Frozen wire names stay;
new fields only. Strict validation through `internal/validate`. TDD with httptest and both stores.

**A5 Page (after A4 contract).** New removable feature `site/js/features/profile-panel.js` (details of the selected contact), class-coloured dots
(toggle, falls back to blue/red), class/guild search facets, `Profile` data fetched lazily for the selection and for visible dots in batches, textContent
only, CSP-safe. Node tests with the fake DOM; one real-Edge check against a local stack (see `scratchpad/cdp-*.mjs` pattern).

**A6 Security and secrets (after A2, A3).** Threat-model and checklist rows (new SEC items): secrets only via env/`*_FILE` (`core.Setting.Secret`),
secret-scan rules for client id/secret shapes, SSRF tests (fixed host, no redirects, hostile names such as `../`, `@`, `%2f`, very long, unicode),
outbound request budget as an abuse limit, token never in logs/metrics/status/`--help`, dependency check (no new modules unless essential), egress
restriction in the systemd unit (`IPAddressAllow=` for the API host only, if the host has stable ranges) and a `verify-hardening.sh` check.

**A7 Deploy and docs (after A6).** `radar.env.example` placeholders only, `install.sh` creates `/etc/radar-live/secrets` (0750 root:radar) and never
prompts with echo, optional `LoadCredential=` in the ingest unit, README and `README-xubuntu.md` section (the typing recipe above), `docs/armory.md`
user guide (enable, verify, rotate, troubleshoot 401/429, what is stored), site/ARCHITECTURE.md update.

**A8 End-to-end and attack tests (after A3-A5).** e2e in `test/e2e`: fake armory server + ingest + db + page API, asserting enrichment lands and is
cached; attack tests in `test/attack` style for hostile names and a slow/lying armory. Then the lead's review pass and a final full run
(`go test ./...`, node tests, `scripts/security-scan.ps1`, `scripts/scan-secrets.ps1`).

## 6. Order and parallelism
A0 (gate) -> A1 and A2 in parallel (disjoint dirs) -> A3 and A4 in parallel -> A5 -> A6 -> A7 -> A8. One agent at a time edits `go.mod` (avoid
needing it) and `cmd/*/modules.go` (one-line adds, re-read before editing).

## 7. Review checklist for the lead after each task
gofmt/vet/tests green; TDD evidence (tests exist for every function, failing first where practical); no secrets or personal data anywhere
(scan clean, `.gitignore` respected); modules removable (architecture test); functions <= 40 lines and readable; docs and checklist updated;
nothing committed by the agent; real-service behaviour is **not** assumed until A0 notes exist.

## 8. Risks
Unknown API shape and terms (A0); rate limits on a large player count (budget + cache); stale data (TTL); character-name spoofing in logs (validation);
credentials leaking through logs or process lists (env/file only, redaction tests); legal/terms of using armory data on a public page (check in A0).
