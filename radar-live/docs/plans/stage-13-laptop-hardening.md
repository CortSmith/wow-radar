# Stage 13: laptop hardening session (to be RUN ON THE XUBUNTU LAPTOP ITSELF)

STATUS: PLAN ONLY, written on the dev PC. **This plan is meant to be handed to a Claude Code session running directly on the Xubuntu
laptop** (not the Windows dev machine) once stage 12's findings exist, since hardening needs root on that box and should be verified in
place. If run before stage 12, do sections 1-3 and 6 first (they don't need findings), then revisit 4-5 once the report lands.

## 0. Authorization
Same as stage 12 Section 0: the user owns this laptop outright and is directing its own hardening. Standing consent for a Claude session
with root/sudo on this machine to inspect and change system configuration for security purposes. No step here reads or exports the radar
secrets (`RADAR_ARMORY_CLIENT_*`, view/ingest keys) — it only confirms *where* they live and how they're protected.

## 1. Inputs this session should read first
- `docs/security/report.md` (stage 12 output) — prioritize fixes by the severities there.
- `docs/security/threat-model.md`, `docs/security/checklist.md`, `docs/security/secrets-policy.md`, `docs/security/hardening.md`.
- `CHECKPOINT.md` "Known violations" / "FOLLOW-UP" lines, specifically: install.sh data-dir perms (0750 -> should be 0700), the
  `RADAR_CORS_ORIGIN` -> `RADAR_CORS_ORIGINS` rename, the radar-client token-in-URL issue, `MemoryDenyWriteExecute` SIGSEGV note.
- `go.mod` toolchain decision (bump to go1.27.4) — do this early since it also fixes stdlib CVEs the report will likely flag.

## 2. Ground rules for this session
- Snapshot before changing anything (`systemctl list-units`, `ufw status verbose`, `fail2ban-client status`, crontab, package list,
  `ss -tlnp`) to `~/radar-hardening-baseline/` outside the repo (not committed, may contain host specifics).
- Every change is a small, reversible, logged step: note the before/after and the exact command in a running
  `docs/security/hardening-log.md` (committed) so a future session or the user can audit or revert.
- Never commit secrets, host-specific IPs/hostnames if sensitive, or real player data. Review `git status`/`git diff` before any commit.
- Reboot-test anything that touches boot/network (systemd units, ufw, fail2ban) and confirm the service comes back healthy before moving on.
- One category at a time; run `scripts/verify-hardening.sh` and `scripts/security-scan.ps1`-equivalent (or its bash counterpart on Linux)
  after each category, not just at the end.

## 3. Categories (ordered by what stage 12 is likely to flag + CHECKPOINT follow-ups; re-order once the real report exists)

### 3.1 OS baseline
- Confirm Xubuntu is on a supported/patched release; enable `unattended-upgrades` for security updates at minimum.
- Disable unused services found in the baseline scan; confirm no unexpected listeners (`ss -tlnp` clean except intended ports).
- Full-disk encryption status check (physical theft risk for a laptop) — report state, let the user decide whether to enable (destructive,
  needs their go-ahead, likely a reinstall if not already encrypted).
- Screen lock / auto-lock timeout on the desktop session (physical access risk, cheap to fix).

### 3.2 Network exposure (feeds from stage 12 T1)
- `ufw`: default-deny inbound, allow only the radar-live public port(s) and SSH (if used) from any, or from specific source IPs if the
  user wants to restrict further. Re-run `verify-hardening.sh`.
- Router-side note (not this machine, but document it): confirm port forwarding on the home router forwards **only** the intended
  port(s) to this laptop's LAN IP, nothing else; recommend a static DHCP reservation so forwarding doesn't drift.
- If the public access is meant to be occasional rather than 24/7, consider a Cloudflare Tunnel (already referenced in
  `docs/security/hardening.md`) instead of raw port-forwarding, so the laptop is never directly dialable — raise this as an option, user
  decides.

### 3.3 Service hardening (systemd units)
- Re-verify `systemd-analyze security` score for each unit; address anything the SIGSEGV workaround note left incomplete
  (`MemoryDenyWriteExecute` removed per hardening.md — confirm whether a newer glibc/Go build lets it be re-enabled).
- `IPAddressAllow=` egress restriction for radar-ingest if/when the armory module lands (stage 10/11), scoped to the armory API host only.
- Data directory permission fix: 0750 -> 0700 per the known follow-up; re-run install.sh's check or patch permissions directly and update
  `install.sh` in the repo so future installs get it right (commit that fix).

### 3.4 Secrets and keys
- Confirm `RADAR_ARMORY_CLIENT_ID_FILE`/`_SECRET_FILE` (if stage 10 has landed by then) are 0640 root:radar, outside the repo, loaded via
  `LoadCredential=` or a 0600 env file — never read the values themselves in this session.
- Rotate any key that stage 12's testing touched (even non-destructively) as a precaution — ingest/view keys, not architecture.
- Fix the radar-client per-run UI token appearing in the page address (CHECKPOINT follow-up): move it to a header/cookie or a POST
  handshake instead of a URL query param. TDD per usual project rules; small, disjoint change.

### 3.5 Fail2ban / abuse response
- Confirm the shipped filter+jail actually bans on the attack patterns stage 12 exercised (cross-check `artifacts/t3-ssh.md` and
  `t2-appsec.md` findings); tune ban time / retry count if too permissive or too strict for real traffic.

### 3.6 Config correctness
- Rename `RADAR_CORS_ORIGIN` -> `RADAR_CORS_ORIGINS` in `deploy/radar.env.example` and anywhere else referenced (known follow-up); confirm
  the running `radar.env` on this laptop is updated to match, not just the example.
- Confirm `RADAR_REQUIRE_KEYS` is set true in the real deployed env (8.4 made this possible; verify it's actually on here).

### 3.7 Supply chain
- Apply the `toolchain go1.27.4` bump to `go.mod`, upgrade Go on this laptop, rebuild, re-run `govulncheck`/`staticcheck`/`gosec`, confirm
  the vuln count referenced in CHECKPOINT drops to zero reachable. Reproducible-build check against the new toolchain.

### 3.8 Fix the known flaky/broken items
- `internal/client TestLiveLogReachesTheDatabaseThroughTheRealStack` (fix the test fixture per the existing note, not the policy).
- `gofmt` on `cmd/radar-db/redaction_test.go`, `cmd/radar-ingest/main_test.go`; staticcheck SA4006 in `sqlstore/sqlinject_test.go:183`; gosec
  G204 triage in `guard/audit/alert.go` (accept-list it properly in `scripts/gosec-accepted.txt` if it's a false positive, fix it if not).

## 4. Verification pass (after each category and at the end)
- `gofmt -l .; go vet ./...; go test -count=1 ./... -cover`; node tests; `scripts/security-scan.ps1` (or Linux equivalent) until clean;
  `scripts/verify-hardening.sh`; `scripts/scan-secrets.ps1`.
- Re-run the non-destructive parts of stage 12's T1/T2 (network exposure, rate limiting, auth) to confirm fixes actually closed the gaps,
  without repeating the SSH dictionary test unless something there changed.
- `systemctl status` on every unit, confirm all healthy after a reboot.

## 5. Report update
Append a "Remediation" section to `docs/security/report.md` (or a new `docs/security/remediation.md` linked from it): for every finding
in stage 12's report, what was changed, when, and the verification evidence. This is the artifact the user keeps as their record.

## 6. What this plan deliberately does NOT do
- Does not touch the home router's firmware/config (outside the laptop; document recommendations only, user applies them).
- Does not enable full-disk encryption or reinstall the OS without the user explicitly saying so (destructive/high-friction).
- Does not rotate or display any secret value to the assistant.

## 7. Handoff note for the session that runs this
Start by reading this file, `docs/security/report.md` if it exists yet, and `CHECKPOINT.md`. Work one category (3.1-3.8) at a time, verify
(Section 4), commit each category's changes separately with a clear message, update `docs/security/hardening-log.md` as you go. Ask the
user before anything destructive (disk encryption, OS reinstall, router changes) or before rotating a key they'd need to re-enter.
