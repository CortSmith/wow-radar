<#
.SYNOPSIS
  Quick secret scan for pre-commit use (task 8.13, docs/security/secrets-policy.md).
.DESCRIPTION
  Same rules as the Go test internal/mod/guard/secrets_test.go (the test is the authority): private-key
  blocks, key-like values assigned to key/secret/token/password names, Bearer tokens, AWS keys, personal
  paths, e-mail addresses, public IPv4 addresses, and env/key/pem/db files. It scans the files git would
  commit (tracked + untracked, not ignored). Known fakes: docs/security/secrets-allowlist.txt.
  Exit code 0 = clean, 1 = findings, 2 = cannot run.
.EXAMPLE
  powershell -File scripts\scan-secrets.ps1
#>
$ErrorActionPreference = 'Stop'
Set-Location (Join-Path $PSScriptRoot '..')
$allowFile = 'docs/security/secrets-allowlist.txt'
if (-not (Test-Path $allowFile)) { Write-Error "missing $allowFile"; exit 2 }
if (-not (Get-Command git -ErrorAction SilentlyContinue)) { Write-Error 'git is required'; exit 2 }

$placeholder = 'change_me|changeme|test|fake|example|dummy|placeholder|sample|xxxx|0000|abcdef|1234|your|<|\$|\{\{|todo|none|redacted'
$generic = 'you|name|user|username|<you>|public|default|%username%|\$env:username|radar|me|example|foo|yourname|your-name|someone'
$allow = @(Get-Content $allowFile | Where-Object { $_ -and -not $_.StartsWith('#') } | ForEach-Object {
        $f = $_ -split '\s+', 3
        [pscustomobject]@{ Rule = $f[0]; Glob = $f[1] }
    })

function Test-Allowed($rule, $path) {
    foreach ($a in $allow) {
        if ($a.Rule -ne '*' -and $a.Rule -ne $rule) { continue }
        $g = $a.Glob
        if ($g.EndsWith('/**')) { if ($path.StartsWith($g.Substring(0, $g.Length - 2))) { return $true } }
        elseif ($path -like $g) { return $true }
    }
    return $false
}

function Get-Entropy([string]$s) {
    $h = 0.0
    foreach ($g in ($s.ToCharArray() | Group-Object)) { $p = $g.Count / $s.Length; $h -= $p * [math]::Log($p, 2) }
    $h
}

function Test-LooksSecret([string]$v) {
    if ($v.Length -lt 20 -or $v -match $placeholder) { return $false }
    if ($v -notmatch '[0-9]' -or $v -notmatch '[A-Za-z]') { return $false }
    if (($v.Split('/').Count - 1) -gt 2 -or ($v.Split('.').Count - 1) -gt 2) { return $false }
    (Get-Entropy $v) -ge 3.5
}

function Test-PublicIPv4([string]$ip) {
    $o = $ip.Split('.') | ForEach-Object { [int]$_ }
    if ($o.Count -ne 4 -or ($o | Where-Object { $_ -gt 255 })) { return $false }
    if ($o[0] -in 0, 10, 127 -or $o[0] -ge 224) { return $false }
    if ($o[0] -eq 172 -and $o[1] -ge 16 -and $o[1] -le 31) { return $false }
    if (($o[0] -eq 192 -and $o[1] -eq 168) -or ($o[0] -eq 169 -and $o[1] -eq 254)) { return $false }
    if (($o[0] -eq 192 -and $o[1] -eq 0 -and $o[2] -eq 2) -or ($o[0] -eq 198 -and $o[1] -eq 51 -and $o[2] -eq 100) -or ($o[0] -eq 203 -and $o[1] -eq 0 -and $o[2] -eq 113)) { return $false }
    if ($o[0] -eq 100 -and $o[1] -ge 64 -and $o[1] -le 127) { return $false }
    $true
}

$reAssign = [regex]::new('(?i)(?:key|secret|token|passw(?:or)?d|credential)[\w.-]*["'']?\s*(?::=|[:=])\s*["''`]?([A-Za-z0-9_+/=.-]{20,})')
$reBearer = [regex]::new('(?i)\bBearer\s+([A-Za-z0-9._~+/=-]{20,})')
$rePem = [regex]::new('-----BEGIN [A-Z0-9 ]*PRIVATE KEY( BLOCK)?-----')
$reAws = [regex]::new('\b(AKIA|ASIA|AGPA|AIDA)[0-9A-Z]{16}\b|(?i)aws_secret_access_key["'']?\s*[:=]\s*["'']?[A-Za-z0-9/+=]{40}')
$reEmail = [regex]::new('[A-Za-z0-9._%+-]+@([A-Za-z0-9-]+(?:\.[A-Za-z0-9-]+)*\.[A-Za-z]{2,})')
$reIp = [regex]::new('(^|[^0-9A-Za-z.])(\d{1,3}(?:\.\d{1,3}){3})(?:[^0-9.]|\.$|\.\s|$)')
$reWin = [regex]::new('(?i)\b[A-Z]:(?:\\\\?|/)Users(?:\\\\?|/)([^\\/"''<>*?|\s]+)')
$reNix = [regex]::new('(?:^|[\s"''=(])/(?:Users|home)/([A-Za-z0-9._-]+)/')

$hits = New-Object System.Collections.Generic.List[string]
function Add-Hit($rule, $path, $line, $text) {
    if (-not (Test-Allowed $rule $path)) {
        $t = $text.Trim(); if ($t.Length -gt 12) { $t = $t.Substring(0, 6) + '...' }
        $hits.Add("${rule}: ${path}:${line}: $t")
    }
}

$files = git ls-files -c -o --exclude-standard | Where-Object { $_ -and $_ -notmatch '(^|/)(\.git|node_modules|dist|maps)/' }
foreach ($rel in $files) {
    if (-not (Test-Path -LiteralPath $rel -PathType Leaf)) { continue }
    $base = [IO.Path]::GetFileName($rel).ToLower()
    if ($base -match '\.(env|key|pem|p12|db|db-wal|db-shm|sqlite)$' -or $base -eq '.env' -or ($base -like '.env.*' -and $base -notlike '*.example')) {
        Add-Hit 'file' $rel 0 $base; continue
    }
    $info = Get-Item -LiteralPath $rel
    if ($info.Length -gt 2MB) { continue }
    $bytes = [IO.File]::ReadAllBytes($info.FullName)
    if ([Array]::IndexOf($bytes[0..([Math]::Min($bytes.Length, 8000) - 1)], [byte]0) -ge 0) { continue }
    $lines = [Text.Encoding]::UTF8.GetString($bytes) -split "`n"
    for ($i = 0; $i -lt $lines.Count; $i++) {
        $l = $lines[$i]; $n = $i + 1
        foreach ($m in $rePem.Matches($l)) { Add-Hit 'pem' $rel $n $m.Value }
        foreach ($m in $reAws.Matches($l)) { Add-Hit 'aws' $rel $n $m.Value }
        foreach ($m in $reBearer.Matches($l)) { if (Test-LooksSecret $m.Groups[1].Value) { Add-Hit 'bearer' $rel $n $m.Groups[1].Value } }
        foreach ($m in $reAssign.Matches($l)) { if (Test-LooksSecret $m.Groups[1].Value) { Add-Hit 'token' $rel $n $m.Groups[1].Value } }
        foreach ($m in $reEmail.Matches($l)) {
            $d = $m.Groups[1].Value.ToLower()
            if ($d -notmatch '^(example\.(com|org|net)|localhost)$' -and $d -notmatch '\.(invalid|example|test)$') { Add-Hit 'email' $rel $n $m.Value }
        }
        foreach ($m in $reIp.Matches($l)) { if (Test-PublicIPv4 $m.Groups[2].Value) { Add-Hit 'ipv4' $rel $n $m.Groups[2].Value } }
        foreach ($m in $reWin.Matches($l)) { if ($m.Groups[1].Value -notmatch "^($generic)$") { Add-Hit 'path' $rel $n $m.Value } }
        foreach ($m in $reNix.Matches($l)) { if ($m.Groups[1].Value -notmatch "^($generic)$") { Add-Hit 'path' $rel $n $m.Value } }
    }
}

if ($hits.Count -gt 0) {
    $hits | Sort-Object -Unique | ForEach-Object { Write-Host $_ }
    Write-Host "scan-secrets: findings above. Fix them, or allowlist a known fake with a reason in $allowFile" -ForegroundColor Red
    exit 1
}
Write-Host 'scan-secrets: clean'
exit 0
