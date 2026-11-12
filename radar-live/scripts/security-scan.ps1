<#
.SYNOPSIS
  Supply-chain and static-analysis gate (task 8.11). Local only, no CI.

.DESCRIPTION
  Runs: gofmt, go vet, go test ./..., go mod verify, the dependency allowlist (Go test in
  internal\mod\guard\supplychain_test.go), govulncheck, staticcheck, gosec (baseline in
  scripts\gosec-accepted.txt) and a reproducible-build check (radar-db and radar-ingest linux/amd64
  built twice, SHA256 must match). Missing tools are reported as SKIPPED loudly, never passed silently.
  Install the tools outside the repo, e.g.
    $env:GOBIN="$env:TEMP\radar-sec-bin"; go install golang.org/x/vuln/cmd/govulncheck@latest
    go install honnef.co/go/tools/cmd/staticcheck@latest; go install github.com/securego/gosec/v2/cmd/gosec@latest
  then put that folder in PATH or pass -ToolsDir. Exit code 1 when a step failed (and with -Strict when skipped).

.EXAMPLE
  powershell -ExecutionPolicy Bypass -File scripts\security-scan.ps1 -ToolsDir $env:TEMP\radar-sec-bin
#>
[CmdletBinding()]
param(
    [string]$ToolsDir = "",
    [switch]$NoRepro,
    [switch]$Strict
)

$ErrorActionPreference = "Continue"
$root = Split-Path -Parent $PSScriptRoot
Set-Location $root
if ($ToolsDir) { $env:PATH = "$ToolsDir;$env:PATH" }
$gopathBin = Join-Path (& go env GOPATH) "bin"
$env:PATH = "$env:PATH;$gopathBin"

$script:results = New-Object System.Collections.ArrayList
$script:failed = 0
$script:skipped = 0
function Step($name) { Write-Host ""; Write-Host "== $name" }
function Pass($name) { [void]$script:results.Add("PASS     $name") }
function Fail($name) { [void]$script:results.Add("FAIL     $name"); $script:failed++ }
function Skip($name) {
    [void]$script:results.Add("SKIPPED  $name (tool not installed)"); $script:skipped++
    Write-Host "!!!!!! SKIPPED: $name is not installed; this check did NOT run !!!!!!" -ForegroundColor Red
}
function Have($cmd) { [bool](Get-Command $cmd -ErrorAction SilentlyContinue) }

Step "gofmt"
$unformatted = & gofmt -l cmd internal 2>&1
if (-not $unformatted) { Pass "gofmt" } else { $unformatted | ForEach-Object { Write-Host $_ }; Fail "gofmt (files above need formatting)" }

Step "go vet"
& go vet ./...
if ($LASTEXITCODE -eq 0) { Pass "go vet" } else { Fail "go vet" }

Step "go test ./..."
& go test ./...
if ($LASTEXITCODE -eq 0) { Pass "go test" } else { Fail "go test" }

Step "go mod verify"
& go mod verify
if ($LASTEXITCODE -eq 0) { Pass "go mod verify" } else { Fail "go mod verify" }

Step "dependency allowlist (internal\mod\guard\supplychain_test.go)"
& go test -count=1 -run 'TestDirectDepsAllowlist|TestGoSumPinsEveryRequirement|TestNoVendorDirectory|TestBuildScriptsReproducibleFlags|TestSecurityScanScriptsPresent' ./internal/mod/guard/
if ($LASTEXITCODE -eq 0) { Pass "dependency allowlist" } else { Fail "dependency allowlist" }

Step "govulncheck"
if (Have govulncheck) {
    & govulncheck ./...
    if ($LASTEXITCODE -eq 0) { Pass "govulncheck" } else { Fail "govulncheck (update the Go toolchain / modules, see docs\security\supply-chain.md)" }
} else { Skip "govulncheck" }

Step "staticcheck"
if (Have staticcheck) {
    & staticcheck -checks 'inherit,-ST1018' ./...
    if ($LASTEXITCODE -eq 0) { Pass "staticcheck" } else { Fail "staticcheck" }
} else { Skip "staticcheck" }

Step "gosec"
if (Have gosec) {
    $lines = & gosec -quiet -no-fail -fmt=golint ./... 2>&1 | ForEach-Object { "$_" }
    $found = @{}
    foreach ($l in $lines) {
        $n = $l.Replace('\', '/')
        if ($n -match '^(?:[A-Za-z]:)?.*?/?((?:cmd|internal)/[^:]+):\d+:\d+:.*Rule:(G\d+)') { $found["$($Matches[2]) $($Matches[1])"] = $true }
    }
    $accepted = @{}
    Get-Content (Join-Path $PSScriptRoot "gosec-accepted.txt") | Where-Object { $_ -and -not $_.StartsWith("#") } | ForEach-Object { $accepted[$_.Trim()] = $true }
    $new = $found.Keys | Where-Object { -not $accepted.ContainsKey($_) } | Sort-Object
    if (-not $new) { Pass "gosec (all findings in the accepted baseline)" }
    else { Write-Host "gosec findings not in scripts\gosec-accepted.txt:"; $new | ForEach-Object { Write-Host "  $_" }; Fail "gosec" }
} else { Skip "gosec" }

if (-not $NoRepro) {
    Step "reproducible build (radar-db, radar-ingest linux/amd64 built twice)"
    $tmp = Join-Path ([System.IO.Path]::GetTempPath()) ("radar-repro-" + [guid]::NewGuid().ToString("N"))
    New-Item -ItemType Directory -Path $tmp | Out-Null
    $saved = @{ CGO_ENABLED = $env:CGO_ENABLED; GOOS = $env:GOOS; GOARCH = $env:GOARCH }
    $ok = $true
    try {
        $env:CGO_ENABLED = "0"; $env:GOOS = "linux"; $env:GOARCH = "amd64"
        foreach ($n in @("radar-db", "radar-ingest")) {
            $hashes = @()
            foreach ($i in 1, 2) {
                $out = Join-Path $tmp "$n.$i"
                & go build -trimpath -buildvcs=false -ldflags "-s -w -buildid= -X main.version=repro" -o $out "./cmd/$n"
                if ($LASTEXITCODE -ne 0) { $ok = $false; continue }
                $h = (Get-FileHash -Algorithm SHA256 $out).Hash.ToLowerInvariant()
                Write-Host "$n  $h"
                $hashes += $h
            }
            if ($hashes.Count -ne 2 -or $hashes[0] -ne $hashes[1]) { $ok = $false }
        }
    } finally {
        foreach ($k in $saved.Keys) { if ($null -eq $saved[$k]) { Remove-Item "Env:$k" -ErrorAction SilentlyContinue } else { Set-Item "Env:$k" $saved[$k] } }
        Remove-Item -Recurse -Force $tmp -ErrorAction SilentlyContinue
    }
    if ($ok) { Pass "reproducible build" } else { Fail "reproducible build (hashes differ)" }
}

Write-Host ""
Write-Host "================ security-scan summary ================"
$script:results | ForEach-Object { Write-Host $_ }
Write-Host "failed: $($script:failed)   skipped: $($script:skipped)"
if ($script:skipped -gt 0) { Write-Host "WARNING: $($script:skipped) check(s) were SKIPPED; the scan is incomplete." -ForegroundColor Red }
if ($script:failed -gt 0) { exit 1 }
if ($script:skipped -gt 0 -and $Strict) { exit 1 }
exit 0
