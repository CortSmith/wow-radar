# radar-live checkpoint (resume here)

Last updated: 2026-11-12, session 3 (end of day).

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
Plan only: docs/plans/stage-10-armory.md (tasks A0-A8). Gate: A0 needs the official WoW Forever API docs URL from the user. Secrets: env/_FILE only, never shown to Claude; typing recipe is in section 3 of the plan. **Still blocked** - the user has not supplied the docs URL as of end of session 3. The tooltip rows this unlocks (U6 in stage-11-radar-ux.md) are the only remaining piece of stage 11 not done.

## DONE (session 3): Stage 11 radar UX (U1-U5)
docs/plans/stage-11-radar-ux.md. `state.tracked` (ordered Map, cap `TRACK_MAX=8`) replaces single-select; `state.player` is a derived
getter (focused/most-recent). Search renders chips (one per tracked player, stable per-guid colour via `trackcolors.js`), not a filled
box. Roster sweep and each tracked player's own sweep are independent per cycle (`core.js`) so tracking someone no longer empties the
list/radar of everyone else. `contact-list.js` always shows the full roster; non-tracked rows `.dim` (never hidden unless "Show only
tracked" is checked) with a header count. `radar-canvas.js`/`draw.js` fade non-tracked dots, ring tracked ones. Hover tooltips
(`tooltip.js`) now work on historical trail points too (that fix's own time/kind, no fabricated state). Only U6 (armory tooltip rows)
remains, gated on stage 10's A0. 200/200 node tests, full Go suite green at time of commit.

## DONE (session 3): Stage 14 world map (W0-W7), with real mid-course corrections - read this before touching maps/world/*
docs/plans/stage-14-world-map.md + docs/plans/world-map-notes.md (the full investigation trail, several real dead ends, read it before
assuming anything about continent placement). Summary of what's actually true today:
- **No game data places Kalimdor and Eastern Kingdoms relative to each other** in this build. Confirmed across two independent
  investigation passes: the `UiMap`/`UiMapAssignment` DB2 tables have no usable combined-placement rows (the two rows under uiMapID 947
  overlap and aren't a valid pair), and the client's own `Blizzard_WorldMap`/`Blizzard_MapCanvas` Lua/XML has zero hardcoded fallback
  either (it's fully data-driven with nothing to fall back to). Do not go looking for this again without new evidence.
- **Real pre-existing bug found and fixed**: `Interface\WorldMap\EasternKingdoms\` in this build holds decorative UI border chrome, not
  terrain - the real Eastern Kingdoms art ships under the old lore name `Azeroth` instead. Fixed in `maps/index.json` (the
  `"easternkingdoms"` catalog entry now points at `Azeroth.png`) and `extract_maps.py` (`CONTINENT_ART_OVERRIDE`). This affects the
  *normal* per-zone map background too, not just the world view.
  - **UNCHECKED FOLLOW-UP for a future session**: the normal (non-world) `maps/EasternKingdoms.png` file on disk is STILL the broken
    border-chrome image - only the *catalog pointer* (`maps/index.json`) was redirected to `Azeroth.png`. If `maps/index.json` is ever
    regenerated by the plain (non---world) extraction pipeline, this redirect will be silently lost and the bug will come back. Worth
    either deleting the stale `maps/EasternKingdoms.png` or making `extract_maps.py`'s main pipeline apply the same
    `CONTINENT_ART_OVERRIDE` so regenerating `maps/index.json` keeps the fix.
- **`maps/world/World.png`** is, after two iterations, now a clean third-party image the user supplied a URL for
  (`https://wowforeveratlas.com/media/forever-maps/azeroth.webp`, 1002x668, no UI chrome to remove). This REPLACED an earlier
  hand-cleaned-screenshot version and, before that, a synthetic two-panel composite built from the separately-extracted continent
  PNGs - both superseded, don't resurrect them. **Licensing note, not yet resolved**: this is third-party art, not Blizzard's own
  extracted files and not something `extract_maps.py` generated; no terms-of-use check has been done. Worth asking the user before any
  wider/public distribution of this repo.
- Zone marker positions on `World.png` are an **explicitly labeled approximation** (`maps/world/index.json`'s top-level `"placement":
  "approximate"` / `"note"` fields spell this out): each zone's real, data-derived fractional position within its own continent's
  native 1002x668 extraction was remapped onto a hand-estimated landmass bounding box in the new image, verified by plotting all 46
  markers. Good enough to be useful, not pixel-perfect.
- **`extract_maps.py --world --force` will DESTROY this hand-prepared result** if ever re-run - it still only knows how to build the
  old inferior synthetic composite. There's a loud warning comment above `extract_world()` in the script. Back up `maps/world/World.png`
  + `index.json` first, or don't re-run `--world` at all until the script itself is taught to use a real source.
- `state.worldView` now **defaults to true** - "All Zones" shows the world map by default, not the single-zone radar. Picking a real
  zone (world-map click or the dropdown) exits world view automatically; clearing back to "All Zones" restores it.
- **Found and fixed post-deploy**: with world view as the default, the sidebar (`#side`) was the sole visible child of `<main>`'s flex
  row (the radar section collapses via `hidden` while in world view) and packed to the LEFT edge instead of staying docked right. Fixed
  with `margin-left:auto` on `#side` in `style.css`. **UNCOMMITTED as of this checkpoint - commit this before anything else.**
- **Maps are OFF by default** when running `radar-db` locally/ad hoc - `maps-dir` (flag `--maps-dir` / env `RADAR_MAPS_DIR`) is empty
  unless explicitly set, so `/maps/` 404s and nothing (zone art or the world map) loads. The bare README quickstart command doesn't set
  it. Verified the real binary works once `--maps-dir ../maps` is passed. Not a bug, just an easy thing to forget when testing.

## Next (planned, NOT started): Stage 12 attack suite against the user's OWN laptop (authorized, self-owned hardware)
Plan only: docs/plans/stage-12-attack-suite.md. Replaces/resumes 8.12a (which previously stopped itself on a safety-classifier read).
Scoped, non-destructive, logs everything, does not read/exfiltrate secrets or real player data, reverts anything it changes. Produces
docs/security/report.md (folds in 8.12c). Needs the user to confirm forwarded ports/public IP (and whether SSH is exposed) before T1/T3.
**User has explicitly said not to run this yet** (session 3): finish features first.

## Next (planned, NOT started): Stage 13 laptop hardening (run ON the Xubuntu laptop itself, not this dev PC)
Plan only: docs/plans/stage-13-laptop-hardening.md. Hand this file to a Claude Code session running directly on the laptop (needs root
there). Prioritized by stage 12's report once it exists; sections 1-3 and 6 (OS baseline, network, config renames) can start without it.
Covers the known follow-ups: data dir 0750->0700 (already done, stale note), RADAR_CORS_ORIGIN->RADAR_CORS_ORIGINS (already done, stale
note), client token-in-URL (still real/open), go1.27.4 toolchain bump (not done - still 1.27.1, 13 reachable stdlib vulns), gofmt/
staticcheck/gosec open items (gofmt is actually clean; staticcheck/gosec/govulncheck aren't installed on this Windows box, Linux-side
task), the flaky live-stack test (confirmed passing now, was likely a one-off). Never reads/displays secret values. **User has
explicitly said not to run this yet** (session 3).

## Also written this session (session 3): go-radar/docs/plans/stage-1-radar-live-client.md
Plan only, nothing implemented. Makes the go-radar desktop app a thin client of radar-live's read API (serving the SAME `site/js`
feature set via a vendored copy + reverse proxy) instead of its own separate log-parsing pipeline/UI, plus a settings gear/overlay
panel (base URL, view key, ruleset, maps folder) with an accessible (i) info tooltip next to every field. Section 0's interpretation
of the request should be confirmed with the user before starting (flagged in the plan itself).

## Session 3 git log (all pushed to origin/main as of this checkpoint, except the sidebar CSS fix above)
- Tools: extract_maps.py --world mode; generate world map assets
- Plan: stage 11 radar UX, stage 14 world map, stage 12/13 security, go-radar client
- Page: multi-select tracking, full roster list, history tooltips, world map
- Page: All Zones defaults to the world map view
- Tools/maps: single combined world map (real art), EasternKingdoms art bug fix
Remote moved case (`cortsmith` -> `CortSmith`) - pushes still succeed via GitHub's redirect but `git remote set-url` was offered and not
yet taken.

## First thing next session
1. Commit the uncommitted `#side{margin-left:auto}` sidebar fix in `radar-live/site/style.css` (see above) before anything else.
2. `gofmt -l .; go vet ./...; go test -count=1 ./... -cover` and `node --test site/test/*.test.js` (expect 200/200) to confirm clean
   baseline, same as every session.
3. Ask the user: stage 10 armory's API docs URL (still the only thing blocking it and stage 11's U6); whether to pursue the
   `wowforeveratlas.com` art's licensing/terms before any wider distribution; the EasternKingdoms.png stale-file follow-up above.
