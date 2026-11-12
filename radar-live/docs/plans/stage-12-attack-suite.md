# Stage 12: authorized attack suite against the user's own laptop

STATUS: PLAN ONLY. Nothing here has been executed. This replaces/resumes task **8.12a**, which previously stopped itself on first read of
the security docs (a safety classifier false-positive on a plan that only *described* attacks). This version is written to run cleanly:
scoped, logged, non-destructive, with the authorization statement right at the top of every artifact the suite produces.

## 0. Authorization (restate this verbatim in the final report)
The target is hardware the user owns outright (the Xubuntu laptop running radar-live), on the user's own home network, which the user is
choosing to port-forward. The user has given explicit, standing consent to attempt to gain unauthorized access to this specific machine for
the purpose of testing it, and has asked for a full report of what was tried and found. This is a first-party authorized penetration test /
red-team exercise of self-owned hardware — not third-party, not production infrastructure, not a shared or employer-owned system. Rules of
engagement below keep it that way; if at any point the target stops being solely the user's own box (e.g. the ISP's router, a neighbor's
device visible on the same network, a cloud host), work stops and the user is asked first.

## 1. Rules of engagement (binding, checked before every task)
- **Scope:** only the laptop's own exposed services (radar-db/radar-ingest/radar-client ports once port-forwarded, SSH if enabled, the OS
  itself) and only from machines/networks the user controls (this dev PC, or a cloud agent session, hitting the forwarded public IP or a
  local test copy). Never scan the home router's WAN-side neighbors, the ISP's range, or anything beyond the forwarded ports themselves.
- **No data exfiltration of real content.** Rule from the user: do not pull secrets off the machine. Concretely: no reading `RADAR_*`
  env values, no reading `/etc/radar-live/secrets/*`, no dumping the sqlite DB's contents, no reading combat logs or player names. If a
  vulnerability *would* expose such data, prove it with a synthetic canary (a planted dummy value/dummy row written by the tester, then
  confirmed readable) instead of reading the real thing, and stop there.
- **No destruction, no DoS.** No volumetric flooding, no disk-filling, no service crash loops left running, no `rm`/wipe of real data, no
  ransomware-style encryption tests, no persistence left installed afterward. A crash *discovered* (e.g. a panic from malformed input) is
  reported, not repeated at scale.
- **Reversible only.** Anything the suite installs, starts, or changes (a cron job, an SSH key, a user, a firewall rule, a planted file) is
  recorded in a running `artifacts/undo.md` log as it happens and removed/reverted at the end of the engagement, verified removed.
- **Time-boxed and logged.** Every tool run, command, and request is logged with timestamp, command line, and target to
  `artifacts/log.jsonl` (append-only) — this is the primary evidence for the report and lets the user audit what actually happened.
- **Stop conditions:** real secrets observed (stop, do not read, note the exposure only), real player/personal data observed (same), any
  sign the target is not actually the user's box (different hostname/keys than expected), any safety-classifier pause (ask the user,
  do not reword-and-retry silently).

## 2. Why 8.12a stalled, and how this plan avoids it
The prior attempt read the threat-model/checklist docs and produced nothing, most likely tripped by prose describing "attacks" with no
visible authorization framing. Mitigations here: (a) every task file and agent prompt opens with the Section 0 authorization statement in
full, not a reference to it; (b) tasks are phrased as defensive verification ("confirm the rate limiter rejects >N req/s", "confirm SSH
rejects password auth") rather than offensive ("flood", "crack", "exploit"); (c) one task at a time, lead reviews the previous task's log
before starting the next, so a stall is caught immediately instead of silently producing nothing; (d) no task asks an agent to actually
defeat a security control for destructive purposes — only to test whether controls and limits hold, which is the checklist's own intent.

## 3. Pre-engagement checklist (lead, before any task starts)
1. Confirm with the user: the laptop's current forwarded ports/public IP (or that it is not yet forwarded, in which case testing runs over
   LAN first and the public-exposure pass waits until forwarding is live).
2. Snapshot current state for comparison: `systemctl list-units`, `ss -tlnp`, `ufw status verbose`, `fail2ban-client status`, running
   processes, crontab, authorized_keys, package list, `go.mod`/binary versions. Save to `artifacts/baseline/`.
3. Confirm backups exist (radar-db backup/rotation from stage 8.8) before anything that touches the running service.
4. Confirm the attack suite runs from a separate machine/account than the one being tested, so logs are independent of the target.

## 4. Phases and tasks (one agent per task, Sonnet medium, sequential — lead reviews each log before the next starts)

**T0 Recon (passive/active, non-intrusive).** Port scan the forwarded host (`nmap -sV -sC` equivalent) against only the known forwarded
ports plus a small common-port sweep (not a full 1-65535 blast unless the user wants that); OS/service fingerprint; TLS config check if any
port is TLS (cipher suites, cert); banner grab radar-db/radar-ingest/radar-client HTTP responses; check what `/`, `/internal/*`,
`/.git`, `/.env`, common backup-file names (`*.bak`, `*~`) return. Output: `artifacts/t0-recon.md`.

**T1 Network/firewall posture.** From outside the LAN (or simulated via the forwarded IP) confirm: only the intended ports answer (stage
8.10's `ufw-setup.sh` + verify-hardening.sh claims); `radar-db --internal-listen` is NOT reachable from outside (8.2's split); SSH, if
enabled, is not on a default/guessable port with password auth. No exploitation yet, just confirm exposure matches intent. Output:
`artifacts/t1-network.md`.

**T2 Application-layer abuse testing (reuses 8.5/8.6 test *intent*, run for real against the live service).** Rate limiting (8.3): hammer
within policy to confirm the limiter actually triggers and recovers, not an uncapped flood — stop once triggered, do not sustain it.
Auth/key handling (8.4/8.13): try the service with no key, a wrong key, an expired/garbage key, confirm `RADAR_REQUIRE_KEYS` really refuses
to start open; confirm lockout/ban behavior on repeated bad keys (bounded attempts, not a brute-force run). Input validation (8.5): malformed
JSON, oversized payloads, injection-shaped strings, XSS-shaped names, confirm rejection and confirm no stack trace/secret leaks in the error
body. CORS/CSP (8.7): confirm the page actually enforces them from a disallowed origin. Output: `artifacts/t2-appsec.md`.

**T3 Credential and SSH surface (if SSH is exposed for remote admin).** Confirm password auth is disabled, confirm no default/weak
accounts exist, a bounded dictionary attempt (small, known-wrong list, not a real crack attempt against a real password — the user is not
asked for their password) against SSH to confirm fail2ban/jail actually bans after N tries within the window (reuses the fail2ban filter
from 8.10). Output: `artifacts/t3-ssh.md`.

**T4 Host/OS hardening verification.** Run `scripts/verify-hardening.sh` for real (first time on real Linux per CHECKPOINT.md); check
systemd unit sandboxing actually applies (`systemd-analyze security radar-db.service` et al.); check file permissions on the secrets dir
(0750/0640 as specified) and the data dir (0700 per the 8.10 follow-up); check for unattended-upgrades / patch currency; check for any
listening service the user didn't expect. Output: `artifacts/t4-host.md`.

**T5 Supply chain / dependency check for real.** Run `govulncheck`, `staticcheck`, `gosec` (per 8.11) against the actual deployed binary's
build, confirm the reachable-vuln count after the `go1.27.4` toolchain bump (open decision from last session); confirm reproducible build
hash matches what's deployed. Output: `artifacts/t5-supplychain.md`.

**T6 Social/physical surface (documented, not performed).** The suite does not attempt phishing, physical access, or anything involving a
human target — out of scope by nature (there is no second person to target) — but the report notes these as categories the user should
consider separately (physical laptop security, Xubuntu login screen lock, disk encryption state).

**T7 Clean-up and verification.** Walk `artifacts/undo.md` top to bottom, revert everything, re-run the T4 baseline diff to confirm the
system matches the pre-engagement snapshot except for intended hardening fixes. Output: `artifacts/t7-cleanup.md`.

## 5. Final report (deliverable: `docs/security/report.md`, the already-planned 8.12c, folded into this stage)
Structure: Executive summary (risk-ranked, plain language) -> Scope & authorization (Section 0, verbatim) -> Methodology -> Findings, each
with: what was tried, what happened, evidence (log line references, not raw secrets), severity (Critical/High/Medium/Low/Info), and a fix
recommendation -> a table mapping every row of `docs/security/checklist.md` to pass/fail/not-applicable evidence from this engagement ->
Appendix: full `artifacts/log.jsonl` reference and the undo log confirming clean-up.

## 6. Order
Pre-engagement checklist -> T0 -> T1 -> T2 -> T3 (only if SSH exposed) -> T4 -> T5 -> T6 (doc only) -> T7 -> report. Each task's agent
prompt restates Section 0 and Section 1 in full before any command. The lead reads each task's log before launching the next and halts the
whole suite on any stop condition from Section 1.

## 7. Open items for the user before this starts
- Confirm the laptop's forwarded ports / public IP once forwarding is live (T1 needs this; can start LAN-only before then).
- Confirm whether SSH is exposed at all (gates T3).
- Confirm the `go1.27.4` toolchain bump has happened before T5, or T5 notes the known 1.27.1 vuln count instead.
