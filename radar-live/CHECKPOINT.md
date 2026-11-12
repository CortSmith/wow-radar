# radar-live checkpoint (resume here)

Last updated: 2026-11-11, session 1.

## What this is
New hosted section of NearbyTracker. `PLAN.md` = master plan, API contract, quality bar. Target host: a weak Xubuntu laptop (static
Linux binaries, pure-Go SQLite, no Docker). User rules: strict TDD, Go API, HTML + JS page polling every 60 s, modular core with
sockets (removing any feature must not break the build), unit tests for everything, readable best-practice code, a plan document per
stage, a review pass after each stage. Subagents: **Sonnet, medium effort**; lead re-runs gofmt/vet/tests after every stage.

## Status (trust docs/plans/STATUS.md over this table if they differ)
| Stage | State |
|---|---|
| 1 core + domain + memstore | DONE, verified (cover 96/99/98%) |
| 2 logparse, contactx, bountyx | DONE, verified (98/96/96%) + realm-suffix fix (Namer locks longest suffix shared by >= 6 players) |
| 3 sqlstore (modernc sqlite) | DONE, verified (89%; linux static build OK; no string-built SQL) |
| 4 radar-db (keys, readapi, writeapi) | DONE, verified (96/96/98, cmd 87%); live probe OK |
| 5 radar-ingest (streamapi, relay) | DONE, verified (98/96, cmd 86%); real run: log -> ingest -> db OK |
| 6 web page + static module | DONE, verified (58 JS tests, static 95%); view: `node site/dev-server.mjs` |
| 7 feeder, packaging, e2e, README | DONE, verified; `install.sh` has NEVER been run on real Linux |
| 8.1 threat model + checklist | DONE: docs/security/{threat-model,checklist}.md (98 controls: 27 done, 13 partial, 58 todo) |
| 9 radar-client (desktop app: pick log, Start, live upload) | DONE, verified (client 87%, feed 96%; win/amd64 exe builds; real smoke upload OK; Browse dialog never clicked by a human, Linux build not run) |
| 8.2 server limits + internal listener split | DONE, verified (limits 92.6%); public listener no longer serves /internal/* |
| 8.3 rate limiting | DONE, verified (ratelimit 95.4%) |
| 8.4 abuse lockout/bans, refuse-to-start-open (RADAR_REQUIRE_KEYS) | DONE, verified |
| 8.5 validation, injection, fuzz, XSS audit | DONE, verified (whole tree: 28 Go pkgs ok, 63 node tests; open: SEC-039 sweep size cap, SEC-041 gzip) |
| 8.7 web headers/CSP/CORS allowlist, key memory-only by default | DONE (agent-reported green; lead full-tree verify pending until 8.6 and 8.8 land). FOLLOW-UP: deploy/radar.env.example still says RADAR_CORS_ORIGIN, must become RADAR_CORS_ORIGINS (do in 8.10/8.13) |
| 8.8 data integrity (integrity checks, 507 disk-full, backups rotate/restore, perms) | DONE (agent-reported). FOLLOW-UPS: install.sh makes data dir 0750, must be 0700 (8.10); internal/client TestLiveLogReachesTheDatabaseThroughTheRealStack fails in full run - probably stricter ping validation (8.5 timestamp window) or key policy (8.4) vs the test fixture: fix the test setup, not the policy |
| 8.6 ingest hardening (stream rate/caps, 24h timestamp window, poisoned-data clamps, relay SSRF tests) | DONE. FULL TREE VERIFIED GREEN after 8.6+8.7+8.8 (gofmt, vet, all packages). NOTE: importing logs older than 24 h needs RADAR_LOGPARSE_ALLOW_REPLAY=true on ingest (client "include existing content" depends on it - document in docs/client.md) |
| 8.11 supply chain (govulncheck/staticcheck/gosec scripts, dep allowlist test, reproducible builds verified identical) | DONE. DECISION NEEDED: Go 1.27.1 has 13 reachable stdlib vulns, 1.27.4 clean -> add `toolchain go1.27.4` to go.mod (not yet done; do after 8.9/8.13 land) and upgrade the local Go. OPEN: gofmt of cmd/radar-db/redaction_test.go and cmd/radar-ingest/main_test.go; staticcheck SA4006 sqlstore/sqlinject_test.go:183; gosec G204 in guard/audit/alert.go needs triage + add to scripts/gosec-accepted.txt; flaky TestAlerterRateLimitsPerKindAndPassesTextWithoutShell (passed on rerun). Then run scriptssecurity-scan.ps1 until it exits 0 |
| 8.13 secrets hygiene (Setting.Secret, secret flags removed -> RADAR_* env only, repo scan test+scripts, install.sh generates keys on host, data dir 0700, CORS var fixed) | DONE (agent-reported green). OPEN: radar-client prints its per-run local UI token in the page address (consider POST handshake); go-radar history files outside radar-live not scanned |
| 8.9 observability (audit log JSON+rotation, /internal/metrics, alert hook, fail2ban format) | DONE. Lead clean-up pass done: gofmt, gosec G204 accepted, alert cooldown doc fixed; FULL TREE VERIFIED GREEN (go test all, 70 node tests, checklist final-mode, security-scan.ps1 PASS with 3 optional scanners skipped as not installed) |
| 8.10 host hardening (hardened systemd units, ufw-setup.sh, fail2ban filter+jail, tunnel guide docs/security/hardening.md, verify-hardening.sh, install.sh checks SHA256SUMS) | DONE, full tree re-verified green (go test all, 70 node, checklist final-mode, build scripts now ship dist/fail2ban). NEVER RUN ON REAL LINUX. If radar-db dies with SIGSEGV at first start, delete MemoryDenyWriteExecute from the unit (see hardening.md). SEC-112 partial: program default listen is still :8787 |
| 8.12a network-abuse attack suite (test/attack) | NOT DONE: the agent was stopped by a safety classifier on its first read of the security docs and did nothing (no files created). Paused on purpose - NOT retried or reworded; waiting for the user to say how to proceed. THEN: 8.12b fuzz+payload corpora summary, 8.12c final docs/security/report.md (each checklist item -> evidence) |
| (remaining) 8.10 host hardening, 8.12 attack suite + report | NOT STARTED; one agent per task, in that order (8.13 right after 8.4); see docs/plans/security-plan.md |

## User rules added mid-session (binding)
- **No secrets, tokens or personal info in the repo; secrets only via application-specific `RADAR_*` env vars** (docs/security/secrets-policy.md,
  `.gitignore`, task 8.13). Known violations to convert in 8.13: `radar-feed --key` flag (keep env `RADAR_FEED_KEY`/key-file path only), any
  `--view-keys/--ingest-keys/--db-key` flags (env only), anything stage 9 persists (client must keep key in memory/env only).
- DDoS protection, rate limiting and hardening are required (security-plan.md). Honest limit: volumetric DDoS needs a CDN/tunnel in front.
- **Open question for the user:** `go-radar/dist/radar-history*.json` and `radar-settings.json` are tracked in git and likely contain
  real player names (personal data). Not touched. Ask before untracking, and note history rewrite would be needed to purge them.

## How to resume
1. `cd radar-live; gofmt -l .; go vet ./...; go test ./... -cover`; node tests: `"C:\Program Files\nodejs\node.exe" --test site/test/*.test.js`.
2. Read `docs/plans/STATUS.md`. A stage whose plan file has no 'Results' section is unfinished or its agent died: inspect the code, relaunch a
   Sonnet/medium agent with the stage plan.
3. Order from here: finish 9 and 8.2 (lead review each) -> 8.3 -> 8.4 -> 8.13 -> 8.5 -> 8.6 -> 8.7 -> 8.8 -> 8.9 -> 8.10 -> 8.11 -> 8.12.
4. Parallel agents need disjoint directories; one agent at a time edits `go.mod` and `cmd/*/modules.go`; STATUS.md is append-only.

## Decisions worth remembering
- radar-cloud (AWS) is untouched; radar-live reuses its vocabulary (`../radar-cloud/NAMING.md`) and JSON field names.
- Merge/trail/bounty rules are pure functions in `internal/domain`; both stores call them.
- Parsers take `any`; shared types in `internal/domain/event.go`. contactx/bountyx flush their own pings to Sinks (2 s on ingest); streamapi only feeds lines.
- Config: flag > env `RADAR_<KEY>` > env file > default. Auth: no keys = open with a warning (8.4/8.10 should make production refuse to start open).
- Only `cmd/**/modules.go` may import `internal/mod/*` (architecture test); e2e tests live in their own dirs with their own modules.go.
- `-race` unavailable (no cgo on this Windows box). Node for site tests: `C:\Program Files\nodejs\node.exe`.
- Real combat logs contain real names: never in tests or docs. Root `.gitignore` ignores `*.log`; radar-live/.gitignore un-ignores testdata logs.

## Open questions for the user
- The tracked go-radar data files above. Map images are extracted game art (personal use) and the page treats charts as optional.
- Domain / Cloudflare account for remote access (needed only at deploy time).

## First steps next session (updated: 8.4 and 8.5 are done; 8.6, 8.7, 8.8 were launched - check their Results blocks)
1. `gofmt -l .; go vet ./...; go test -count=1 ./... -cover` and the node tests. Fix whatever 8.4/8.5 left half-done (inspect `git status`, STATUS.md).
2. Re-launch any task lacking a Results block (Sonnet, medium, one task per agent, disjoint dirs). Remaining: 8.6 ingest hardening, 8.7 web headers/CSP,
   8.8 data integrity, 8.9 observability, 8.10 host hardening (systemd sandbox, ufw, fail2ban, Cloudflare Tunnel), 8.11 supply chain (govulncheck),
   8.13 secrets hygiene (convert `radar-feed --key` and any secret flags to env-only; repo secret-scan test), 8.12 attack suite + docs/security/report.md.
3. Ask the user: the tracked go-radar history/settings files (possible real player names), and have them test `install.sh` on the real Xubuntu laptop
   and click through the client's Browse dialog on Windows (neither has been run by a human).
4. Nothing is committed to git: all radar-live work is uncommitted in the working tree. Commit only when the user asks.

## Final state at end of session 1
- All stages 1-9 and security 8.1-8.11, 8.13 DONE and verified. Left: **8.12** (attack suite a: network abuse, BLOCKED by a safety classifier on the agent's first read; ask the user before retrying anything; b: fuzz/payload summary (8.5 already ran fuzz targets); c: docs/security/report.md mapping every checklist item to evidence).
- User decisions pending: run 8.12a?; add `toolchain go1.27.4` to go.mod and upgrade local Go (13 reachable stdlib vulns on 1.27.1); untrack go-radar/dist history files (possible real names).
- Not yet run by a human: install.sh on the Xubuntu laptop, verify-hardening.sh, client Browse dialog on Windows.
- Nothing committed. Quick local test recipe is in the chat history: run radar-db (RADAR_REQUIRE_KEYS=false, --listen 127.0.0.1:8787 --internal-listen 127.0.0.1:8789), radar-ingest (RADAR_LOGPARSE_ALLOW_REPLAY=true, --db-url http://127.0.0.1:8789), POST a log to :8788/v1/lines.

## Next feature (planned, NOT started): Stage 10 armory module
Plan only: docs/plans/stage-10-armory.md (tasks A0-A8). Gate: A0 needs the official WoW Forever API docs URL from the user. Secrets: env/_FILE only, never shown to Claude; typing recipe is in section 3 of the plan.

## Next feature (planned, NOT started): Stage 11 radar UX
Plan only: docs/plans/stage-11-radar-ux.md (multi-select chips, full greyed list, tooltips on history dots, armory rows in tooltip). U1-U5 need no armory.

## Next (planned, NOT started): Stage 12 attack suite against the user's OWN laptop (authorized, self-owned hardware)
Plan only: docs/plans/stage-12-attack-suite.md. Replaces/resumes 8.12a (which previously stopped itself on a safety-classifier read).
Scoped, non-destructive, logs everything, does not read/exfiltrate secrets or real player data, reverts anything it changes. Produces
docs/security/report.md (folds in 8.12c). Needs the user to confirm forwarded ports/public IP (and whether SSH is exposed) before T1/T3.

## Next (planned, NOT started): Stage 13 laptop hardening (run ON the Xubuntu laptop itself, not this dev PC)
Plan only: docs/plans/stage-13-laptop-hardening.md. Hand this file to a Claude Code session running directly on the laptop (needs root
there). Prioritized by stage 12's report once it exists; sections 1-3 and 6 (OS baseline, network, config renames) can start without it.
Covers the known follow-ups: data dir 0750->0700, RADAR_CORS_ORIGIN->RADAR_CORS_ORIGINS, client token-in-URL, go1.27.4 toolchain bump,
gofmt/staticcheck/gosec open items, the flaky live-stack test. Never reads/displays secret values.

## Verified 2026-11-12 (session 2, before starting stage 11/14 work)
Full tree re-verified green from a clean checkout: `gofmt -l .` empty, `go vet ./...` clean, `go test -count=1 ./... -cover` all green
(including the previously-flaky `internal/client TestLiveLogReachesTheDatabaseThroughTheRealStack`, which passed), 90/90 node tests green.
Several CHECKPOINT follow-ups turned out to already be done (stale notes, not re-done this session): `RADAR_CORS_ORIGIN` is already
`RADAR_CORS_ORIGINS` everywhere (deploy/radar.env.example, dist/, tests, docs all consistent); install.sh already makes data/backup/log dirs
0700. Still open: radar-client's per-run token still appears in the page URL (`cmd/radar-client/main.go` `PageURL(ln, ui.token)`) - real,
unresolved, but it's a stage-13 (laptop-hardening) item and stage 13 is deliberately not being worked yet. `staticcheck`/`gosec`/`govulncheck`
are not installed on this Windows dev box at all (stage 13, Linux-side task) - nothing to check here.

## Next (planned, NOT started): Stage 14 world map (All Zones view: continent map, zone chips, cross-zone player paths, click-to-navigate)
Plan only: docs/plans/stage-14-world-map.md. World map art does not exist yet - W0/W1 extend extract_maps.py to pull continent + per-zone
placement data from the local game install (read-only, same approach as today's zone extraction) into maps/world/*.png + index.json.
Friendly/hostile chip sizing per zone, a pure worldPath() for chronological cross-zone lines per tracked player (trail fixes already carry
mapId, no server change needed), and click-to-navigate that pre-sets the Blue/Red checkboxes from the chip clicked. Pairs well with stage
11's multi-select but the single-player case works without it.
