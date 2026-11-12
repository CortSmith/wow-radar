<#
.SYNOPSIS
  Cross-compiles radar-live into dist\ (static binaries, no cgo) and writes SHA256SUMS.

.DESCRIPTION
  radar-db and radar-ingest: linux/amd64 and linux/arm64.
  radar-feed: windows/amd64 and linux/amd64.
  radar-client: windows/amd64 (windowed exe, no console) and linux/amd64 (console + browser).
  The deploy\ files (install.sh, units, env example, ...) are copied next to the binaries so that
  dist\ is everything you need to scp to the laptop.

.PARAMETER NoMaps
  Leave the maps out. By default ..\maps (zone images, world map, index.json; game data for personal use) is copied
  to dist\maps; SHA256SUMS covers it and install.sh installs it where radar-db looks.

.PARAMETER WithMaps
  Accepted for old command lines; does nothing (maps are included by default).

.PARAMETER Version
  Build version stamped into the binaries (default: git describe, else "dev").

.EXAMPLE
  powershell -ExecutionPolicy Bypass -File scripts\build.ps1

.EXAMPLE
  powershell -ExecutionPolicy Bypass -File scripts\build.ps1 -NoMaps
#>
[CmdletBinding()]
param(
    [string]$Version = "",
    [switch]$WithMaps,
    [switch]$NoMaps
)

$ErrorActionPreference = "Stop"

$root = Split-Path -Parent $PSScriptRoot
Set-Location $root
$dist = Join-Path $root "dist"

if (-not $Version) {
    $Version = "dev"
    if (Get-Command git -ErrorAction SilentlyContinue) {
        $described = $null
        try { $described = & git describe --tags --always --dirty 2>$null } catch { $described = $null }
        if ($LASTEXITCODE -eq 0 -and $described) { $Version = "$described".Trim() }
    }
}

# name, GOOS, GOARCH
$targets = @(
    @("radar-db", "linux", "amd64", ""),
    @("radar-db", "linux", "arm64", ""),
    @("radar-ingest", "linux", "amd64", ""),
    @("radar-ingest", "linux", "arm64", ""),
    @("radar-feed", "windows", "amd64", ""),
    @("radar-feed", "linux", "amd64", ""),
    @("radar-client", "windows", "amd64", "-H=windowsgui"),
    @("radar-client", "linux", "amd64", "")
)

if (Test-Path $dist) { Remove-Item -Recurse -Force $dist }
New-Item -ItemType Directory -Path $dist | Out-Null

$savedEnv = @{ CGO_ENABLED = $env:CGO_ENABLED; GOOS = $env:GOOS; GOARCH = $env:GOARCH }
try {
    $env:CGO_ENABLED = "0"
    foreach ($t in $targets) {
        $name, $os, $arch, $extraLd = $t
        $ext = ""
        if ($os -eq "windows") { $ext = ".exe" }
        $out = Join-Path $dist "$name-$os-$arch$ext"
        $env:GOOS = $os
        $env:GOARCH = $arch
        Write-Host "building $name for $os/$arch"
        & go build -trimpath -buildvcs=false -ldflags "$extraLd -s -w -buildid= -X main.version=$Version" -o $out "./cmd/$name"
        if ($LASTEXITCODE -ne 0) { throw "go build failed for $name $os/$arch" }
    }
}
finally {
    foreach ($k in $savedEnv.Keys) {
        if ($null -eq $savedEnv[$k]) { Remove-Item "Env:$k" -ErrorAction SilentlyContinue }
        else { Set-Item "Env:$k" $savedEnv[$k] }
    }
}

# Deployment files travel with the binaries.
Get-ChildItem -File (Join-Path $root "deploy") | Where-Object { $_.Name -notlike "*_test.go" } | ForEach-Object {
    Copy-Item $_.FullName (Join-Path $dist $_.Name)
}
# The fail2ban filter and jail go in a sub-folder (used by install.sh --with-fail2ban).
$fail2banSource = Join-Path $root "deploy\fail2ban"
if (Test-Path $fail2banSource) {
    $fail2banTarget = Join-Path $dist "fail2ban"
    New-Item -ItemType Directory -Force $fail2banTarget | Out-Null
    Copy-Item (Join-Path $fail2banSource "*") $fail2banTarget
}

# Zone maps are included by default so a fresh install shows them without any flag (-NoMaps skips them).
if (-not $NoMaps) {
    $mapsSource = Join-Path $root "..\maps"
    if (Test-Path $mapsSource) {
        $mapsTarget = Join-Path $dist "maps"
        New-Item -ItemType Directory -Force $mapsTarget | Out-Null
        Copy-Item -Recurse -Force (Join-Path $mapsSource "*") $mapsTarget
        Write-Host "zone maps copied from $mapsSource"
    }
    else {
        Write-Warning "$mapsSource not found; building without maps (the page will show no map images)"
    }
}

# SHA256SUMS in the "hash  name" format that `sha256sum -c` reads; LF line ends, no BOM.
$lines = Get-ChildItem -File -Recurse $dist | Where-Object { $_.Name -ne "SHA256SUMS" } | ForEach-Object {
    $relative = $_.FullName.Substring($dist.Length + 1).Replace("\", "/")
    $hash = (Get-FileHash -Algorithm SHA256 $_.FullName).Hash.ToLowerInvariant()
    "$hash  $relative"
} | Sort-Object
$text = ($lines -join "`n") + "`n"
[System.IO.File]::WriteAllText((Join-Path $dist "SHA256SUMS"), $text, (New-Object System.Text.UTF8Encoding($false)))

Write-Host ""
Write-Host "version $Version -> $dist"
Get-ChildItem -File $dist | Sort-Object Name | ForEach-Object {
    "{0,-34} {1,10:N0} bytes" -f $_.Name, $_.Length
}
