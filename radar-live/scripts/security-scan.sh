#!/bin/sh
# Supply-chain and static-analysis gate (task 8.11). Local only, no CI.
#   gofmt, go vet, go test ./..., go mod verify, dependency allowlist (Go test),
#   govulncheck, staticcheck, gosec (baseline scripts/gosec-accepted.txt), reproducible-build check.
# Missing tools are reported as SKIPPED loudly (never silently passed). Install them outside the repo:
#   GOBIN=/some/dir go install golang.org/x/vuln/cmd/govulncheck@latest
#   GOBIN=/some/dir go install honnef.co/go/tools/cmd/staticcheck@latest
#   GOBIN=/some/dir go install github.com/securego/gosec/v2/cmd/gosec@latest
# and put that dir in PATH or in RADAR_SCAN_BIN. Use `scripts/security-scan.sh --no-repro` to skip the double build.
# Exit status: 1 when any step FAILED; skipped tools exit 0 unless RADAR_SCAN_STRICT=1.
set -u
root=$(cd "$(dirname "$0")/.." && pwd)
cd "$root" || exit 2
[ -n "${RADAR_SCAN_BIN:-}" ] && PATH="$RADAR_SCAN_BIN:$PATH"
gopath_bin="$(go env GOPATH 2>/dev/null)/bin"
PATH="$PATH:$gopath_bin"
repro=1
[ "${1:-}" = "--no-repro" ] && repro=0

fail=0; skipped=0; summary=""
ok()   { summary="$summary
PASS     $1"; }
bad()  { summary="$summary
FAIL     $1"; fail=$((fail+1)); }
skip() { summary="$summary
SKIPPED  $1 (tool not installed)"; skipped=$((skipped+1))
         echo "!!!!!! SKIPPED: $1 is not installed; this check did NOT run !!!!!!"; }
step() { echo; echo "== $1"; }

step gofmt
out=$(gofmt -l cmd internal 2>&1)
if [ -z "$out" ]; then ok gofmt; else echo "$out"; bad "gofmt (files above need formatting)"; fi

step "go vet"
if go vet ./...; then ok "go vet"; else bad "go vet"; fi

step "go test ./..."
if go test ./...; then ok "go test"; else bad "go test"; fi

step "go mod verify"
if go mod verify; then ok "go mod verify"; else bad "go mod verify"; fi

step "dependency allowlist (internal/mod/guard/supplychain_test.go)"
if go test -count=1 -run 'TestDirectDepsAllowlist|TestGoSumPinsEveryRequirement|TestNoVendorDirectory|TestBuildScriptsReproducibleFlags|TestSecurityScanScriptsPresent' ./internal/mod/guard/; then
    ok "dependency allowlist"; else bad "dependency allowlist"; fi

step govulncheck
if command -v govulncheck >/dev/null 2>&1; then
    if govulncheck ./...; then ok govulncheck; else bad "govulncheck (update the Go toolchain / modules, see docs/security/supply-chain.md)"; fi
else skip govulncheck; fi

step staticcheck
if command -v staticcheck >/dev/null 2>&1; then
    if staticcheck -checks 'inherit,-ST1018' ./...; then ok staticcheck; else bad staticcheck; fi
else skip staticcheck; fi

step gosec
if command -v gosec >/dev/null 2>&1; then
    found=$(gosec -quiet -no-fail -fmt=golint ./... 2>&1 | tr '\' '/' \
        | sed -E 's|^[A-Za-z]:/.*/radar-live/||; s|^'"$root"'/||; s|^([^:]+):[0-9]+:[0-9]+:.*Rule:(G[0-9]+).*|\2 \1|' \
        | grep -E '^G[0-9]+ ' | sort -u)
    accepted=$(grep -Ev '^(#|$)' scripts/gosec-accepted.txt | sort -u)
    new=$(printf '%s\n' "$found" | grep -vxFf scripts/gosec-accepted.txt)
    if [ -z "$new" ]; then ok "gosec (all findings in the accepted baseline)"
    else echo "gosec findings not in scripts/gosec-accepted.txt:"; echo "$new"; bad gosec; fi
else skip gosec; fi

if [ "$repro" = 1 ]; then
    step "reproducible build (radar-db, radar-ingest linux/amd64 built twice)"
    tmp=$(mktemp -d) || exit 2
    rc=0
    for n in radar-db radar-ingest; do
        for i in 1 2; do
            CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -buildvcs=false \
                -ldflags "-s -w -buildid= -X main.version=repro" -o "$tmp/$n.$i" "./cmd/$n" || rc=1
        done
        if [ -f "$tmp/$n.1" ] && [ -f "$tmp/$n.2" ]; then
            a=$(sha256sum "$tmp/$n.1" 2>/dev/null | cut -d' ' -f1); b=$(sha256sum "$tmp/$n.2" 2>/dev/null | cut -d' ' -f1)
            echo "$n  $a"; echo "$n  $b"
            [ -n "$a" ] && [ "$a" = "$b" ] || rc=1
        else rc=1; fi
    done
    rm -rf "$tmp"
    if [ $rc = 0 ]; then ok "reproducible build"; else bad "reproducible build (hashes differ)"; fi
fi

echo; echo "================ security-scan summary ================"
printf '%s\n' "$summary"
echo "failed: $fail   skipped: $skipped"
[ "$skipped" -gt 0 ] && echo "WARNING: $skipped check(s) were SKIPPED; the scan is incomplete."
[ "$fail" -gt 0 ] && exit 1
[ "$skipped" -gt 0 ] && [ "${RADAR_SCAN_STRICT:-0}" = 1 ] && exit 1
exit 0
