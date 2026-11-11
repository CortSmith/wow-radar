# Builds dist\radar.exe (windowed, no console). Use -Debug for dist\radar-debug.exe (console, logs to stderr).
param(
    [switch]$Debug,
    [switch]$SkipTests
)
$ErrorActionPreference = 'Stop'
Set-Location $PSScriptRoot
$env:CGO_ENABLED = '0'
$env:GOOS = 'windows'
$env:GOARCH = 'amd64'

if (-not $SkipTests) {
    go vet ./...
    if ($LASTEXITCODE -ne 0) { throw 'go vet failed' }
    go test ./...
    if ($LASTEXITCODE -ne 0) { throw 'go test failed' }
}

New-Item -ItemType Directory -Force dist | Out-Null
if ($Debug) {
    go build -trimpath -o dist\radar-debug.exe .\cmd\radar
    if ($LASTEXITCODE -ne 0) { throw 'go build failed' }
    Write-Host 'Built dist\radar-debug.exe (console build; run with --debug)'
} else {
    go build -trimpath -ldflags "-H=windowsgui -s -w" -o dist\radar.exe .\cmd\radar
    if ($LASTEXITCODE -ne 0) { throw 'go build failed' }
    Write-Host 'Built dist\radar.exe'
}
