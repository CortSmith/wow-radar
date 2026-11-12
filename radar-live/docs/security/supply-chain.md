# Supply chain, static analysis and reproducible builds (task 8.11)

Run everything with `scripts\security-scan.ps1` (Windows) or `scripts/security-scan.sh` (Linux/macOS). No CI, no GitHub: the
scan is a local gate before a release. A missing tool is printed as `SKIPPED` in red and in the summary; `-Strict` /
`RADAR_SCAN_STRICT=1` turns skips into a failure. Tools are installed outside the repo (see the header of the scripts) and found
through `PATH`, `-ToolsDir` / `RADAR_SCAN_BIN` or `GOPATH/bin`.

## 1. Dependencies

Direct modules (allowlist, enforced by `internal/mod/guard/supplychain_test.go::TestDirectDepsAllowlist`, which parses `go.mod`):

| Module | Why |
|---|---|
| `modernc.org/sqlite` | pure-Go SQLite (no cgo), the store |
| `github.com/jchv/go-webview2` | desktop client window on Windows only (pure Go, loads the system WebView2 runtime) |
| `golang.org/x/sys` | Windows/Unix syscalls (file locks, disk space) |

Indirect closure (reviewed, also enforced): `dustin/go-humanize`, `google/uuid`, `jchv/go-winloader`, `mattn/go-isatty`,
`ncruces/go-strftime`, `remyoudompheng/bigfft`, `modernc.org/libc`, `modernc.org/mathutil`, `modernc.org/memory`.
Adding any module (direct or indirect) fails the test until the allowlist in that file and this document change in the same
commit. `replace` directives are refused. (The original plan said "only modernc.org/sqlite"; the client stage added go-webview2
and x/sys, both used on the Windows side; radar-db and radar-ingest link only sqlite and its closure.)

`go.sum` is committed and verified three ways: `go mod verify` (module cache matches go.sum), `TestGoSumPinsEveryRequirement`
(every requirement has a go.mod hash, every direct one a content hash, no malformed lines) and the Go checksum database
(`GONOSUMDB` / `GOFLAGS=-insecure` are not set anywhere). The build scripts never run `go get`.

### Vendor decision: not vendoring
`vendor/` is not used (`TestNoVendorDirectory` keeps it that way unless this section changes). Reasons: the module graph is tiny
and every module is hash-pinned by `go.sum` and checked against sum.golang.org on first download; the module cache is verifiable
with `go mod verify`; vendoring modernc.org/libc and sqlite would add a very large body of generated C-to-Go source to the repo and
hide upgrades from review; a vendored copy is not checked against the checksum database, so it is weaker, not stronger, unless
re-verified. Offline builds remain possible from the module cache (`GOPROXY=off`) on the build machine.

## 2. govulncheck

Installed outside the repo: `GOBIN=%TEMP%\radar-sec-bin go install golang.org/x/vuln/cmd/govulncheck@latest` (worked, proxy
reachable, govulncheck v1.9.0).

| Toolchain | Result |
|---|---|
| go1.27.1 (installed on this machine) | 13 reachable vulnerabilities, all in the **Go standard library**; 2 more in imported packages that are not called; 0 in required modules (x/sys v0.50.0 and modernc.org/sqlite v1.62.0 are clean) |
| go1.27.4 (`GOTOOLCHAIN=go1.27.4`, auto-downloaded, used for all later runs) | **No vulnerabilities found** |

The 13 findings (GO-2026-6859, 6809, 6617, 6613, 6612, 6611, 6610, 6608, 6607, 6605, 6603, 6600, 6599): net/http HTTP/1 and HTTP/2
server crashes and memory exhaustion (MaxBytesReader header race fixed in 1.27.4; HPACK encoder race, double flow-control refund,
initial-window CPU, trailer memory, CONNECT desync fixed in 1.27.2), net/textproto MIME header memory limit (1.27.2), crypto/x509
distrusted intermediates (1.27.3), crypto/tls ECH (1.27.2), html/template context tracking (1.27.2, used by the client page).
All are reachable (the servers and the relay use net/http), so they matter for the public listeners.

**Action (decision for the operator):** release builds must use Go 1.27.4 or newer. Nothing in the repo pins a toolchain;
proposal: add `toolchain go1.27.4` to `go.mod` (not edited by this task) or build with `GOTOOLCHAIN=go1.27.4`. The deployed
laptop needs no Go at all. No dependency upgrade is required.

## 3. Static analysis triage

Tools (same temp-GOBIN install): `go vet`, `staticcheck`, `gosec`.

**go vet:** clean on Go 1.27.4 (one transient failure in `internal/core` during the run was another task's in-progress test).

**staticcheck** (run with `-checks inherit,-ST1018`):
- ST1018 (zero-width / bidi characters as raw literals), 25 hits, all in test files that deliberately inject U+202E, U+2066 and
  U+200B to attack the sanitisers. False positive for intent; suppressed in the scan.
- U1000 `contactx/unit.go` field `pastNames`, U1000 `readapi/params.go` func `invalidf`: dead code. **Fixed** (section 6).
- SA4006 `internal/mod/sqlstore/sqlinject_test.go:183` value never used: a test, **open**, for that test's author (the scan
  fails until it is fixed).

**gosec:** 36 findings in 29 rule/file groups, all recorded in `scripts/gosec-accepted.txt`; the scan fails on any new group.

| Rule | Where | Verdict |
|---|---|---|
| G304 file inclusion via variable (11) | radar-db backup/restore, radar-feed key file, radar-ingest defaults, client settings, core envfile, tailer, abuse ban file, keys file, sqlstore preflight | Accepted: paths come from operator flags/env or the user's own file picker, never from network input. Serving of request-derived paths is the static module, which uses EvalSymlinks and a prefix check (8.5). |
| G703 path traversal taint (1) | radar-ingest/defaults.go | Accepted: the path is an operator flag. |
| G103 unsafe (7) | Windows syscalls (GetDiskFreeSpaceEx, GetOpenFileName, MessageBoxW) | Accepted: required by those APIs, pointers to locals and buffers that outlive the call. |
| G104 unhandled errors (5) | `tmp.Close()` on an already failing path, `pr.CloseWithError`, `os.Stderr.WriteString` | Accepted: the original error is the one returned. |
| G115 int conversion (2) | picker buffer length (fixed size), test helper rune | Accepted: bounded constants. |
| G202 SQL concatenation (2) | sqlstore read.go and prune.go | Accepted: only constant fragments and table names from a two-element constant list are concatenated; all values are bound parameters; the AST scan in `sqlinject_test.go` enforces this. |
| G204 subprocess (1) | `exec.Command("rundll32", ..., url)` in radar-client | Accepted: no shell; the URL is the app's own 127.0.0.1 address. |
| G401 / G505 sha1 (2) | bountyx death id | Accepted: a truncated dedupe identifier, not a security control. |
| G404 math/rand (1) | relay retry jitter | Accepted: jitter, not a secret. |
| G101 hardcoded credentials (1) | `tokenHeader = "X-Radar-Token"` | False positive: a header name; the token is generated per run. |
| G602 slice index (1) | storetest equalStrings | False positive: lengths are compared just above. |
| G705 XSS taint (2) | static asset writer, client asset writer | Accepted: embedded or validated files with fixed Content-Type, nosniff and CSP (8.7). |

**New since the baseline:** `G204 internal/mod/guard/audit/alert.go` (alert hook exec, added by another task while this one ran).
The scan reports it as not in the baseline. It runs an operator-configured command with argv and no shell (its own test asserts
the argv); the module owner should confirm the command comes only from operator config and add it to
`scripts/gosec-accepted.txt`.

## 4. Reproducible, hardened builds

`scripts/build.ps1` and `build.sh` build with `CGO_ENABLED=0`, `-trimpath`, `-buildvcs=false` and
`-ldflags "-s -w -buildid= -X main.version=<v>"` (static, no VCS stamping, no build id, no absolute paths). The only variable
input is the version string; pass the same `[version]` argument to compare builds of different checkouts, and use the same Go
version (`go version -m <binary>`). The Windows GUI client adds `-H=windowsgui`.

Check (a step of the scan, also by hand): radar-db and radar-ingest linux/amd64 are built twice into a temp dir and must have equal
SHA256. Recorded run (Go 1.27.4, windows/amd64 host, version string `repro`, run through `security-scan.sh`):

```
radar-db      7e8c4da9bf132076a90390f8c204e028efb1243a37dbcfe8458a218cd25b9414   (twice, identical)
radar-ingest  420a80a1c153d19b82fb4c40dcbff228b3711193ce5f6738523cb59269473db6   (twice, identical)
```
Hashes change whenever the source changes. One earlier run showed differing radar-db hashes only because another task edited
radar-db sources between the two builds; three consecutive builds afterwards were identical.

## 5. Verifying a download on the laptop

`dist/SHA256SUMS` is written next to the binaries (`hash  name`, LF line ends, deploy files included). After copying `dist/`:

```
cd dist
sha256sum -c SHA256SUMS                    # every line must say OK
sha256sum -c --ignore-missing SHA256SUMS   # when only some files were copied
sha256sum /usr/local/bin/radar-db          # the installed binary against its line in SHA256SUMS
```
Read the expected hashes from the build output on your own machine (a second channel), not from the same copy that travelled:
SHA256SUMS next to the binaries only detects corruption and accidental change. There is no signing key and no auto-update. To
confirm a binary comes from this source, rebuild with the same Go version and version string and compare hashes.

## 6. Code changes made by this task
- `internal/mod/contactx/unit.go`: removed unused field `pastNames`.
- `internal/mod/readapi/params.go`: removed unused func `invalidf`.
- `scripts/build.ps1`, `scripts/build.sh`: added `-buildvcs=false` and `-buildid=`.
- New: `scripts/security-scan.{ps1,sh}`, `scripts/gosec-accepted.txt`, `internal/mod/guard/supplychain_test.go`.

- G204 `internal/mod/guard/audit/alert.go` (added by lead after 8.9): accepted. The argv comes only from the operator-set secret setting `RADAR_ALERT_CMD`, is split without a shell, runs with a 5 s timeout, and alert text is passed as data. No request input reaches it.
