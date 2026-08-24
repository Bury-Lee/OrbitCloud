# ============================================================================
# OrbitCloud SMB Gateway source sync script (Windows side)
#
# Pushes smb_server/rust source + the Linux deploy script to the server,
# then runs deploy/dev-deploy-smb-gateway.sh remotely (build/restart/verify).
#
# Requires sshbox.exe (repo tool/sshbox.exe, config tool/sshbox.json).
# Pure ASCII on purpose - PowerShell 5.1 reads .ps1 as ANSI without a BOM,
# which would corrupt any non-ASCII literal.
#
# Usage:
#   .\deploy\deploy-smb-gateway.ps1             # push + remote build + deploy
#   .\deploy\deploy-smb-gateway.ps1 -SkipBuild  # remote deploy only (no cargo)
# ============================================================================
param(
    [switch]$SkipBuild
)

$ErrorActionPreference = "Stop"
$scriptDir = Split-Path -Parent $MyInvocation.MyCommand.Path
$repoRoot = Split-Path -Parent $scriptDir
$sshbox = Join-Path $repoRoot "tool\sshbox.exe"
if (-not (Test-Path $sshbox)) { throw "sshbox.exe not found at $sshbox" }

$rustDir = Join-Path $repoRoot "smb_server\rust"
if (-not (Test-Path (Join-Path $rustDir "Cargo.toml"))) { throw "smb_server/rust not found: $rustDir" }

$sshboxJson = Join-Path $repoRoot "tool\sshbox.json"
if (Test-Path $sshboxJson) { $env:SSHBOX_CONFIG = $sshboxJson }

$tmp = Join-Path $env:TEMP "opencode"
New-Item -ItemType Directory -Force -Path $tmp | Out-Null
$tarPath = Join-Path $tmp "smb-gateway-src.tar.gz"

Write-Host "==> 1. Pack source + deploy script (excludes target/logs)" -ForegroundColor Cyan
Remove-Item -Force $tarPath -ErrorAction SilentlyContinue
Push-Location $repoRoot
try {
    & tar -czf $tarPath `
        deploy/dev-deploy-smb-gateway.sh `
        smb_server/rust/Cargo.toml `
        smb_server/rust/src `
        smb_server/rust/patched-ixr/Cargo.toml `
        smb_server/rust/patched-ixr/src
    if ($LASTEXITCODE -ne 0) { throw "tar pack failed" }
} finally { Pop-Location }
Write-Host "    packed: $tarPath" -ForegroundColor Green

Write-Host "==> 2. Push tarball" -ForegroundColor Cyan
& $sshbox push $tarPath "/tmp/" | ForEach-Object { Write-Host "    $_" }
if ($LASTEXITCODE -ne 0) { throw "push tarball failed" }

Write-Host "==> 3. Remote unpack + run deploy script" -ForegroundColor Cyan
# Locate the repo on the server via the backend process cwd, then unpack
# into the repo root and run deploy/dev-deploy-smb-gateway.sh.
$arg = if ($SkipBuild) { "--skip-build" } else { "" }
$cmd = "GO_PID=`$(pgrep -f 'build/dist/orbitcloud' | head -1); " + `
       "if [ -z `"`$GO_PID`" ]; then echo `"[FAIL] backend process not found`"; exit 1; fi; " + `
       "REPO=`$(readlink /proc/`$GO_PID/cwd); " + `
       "if [ -z `"`$REPO`" ]; then echo `"[FAIL] cannot read backend cwd`"; exit 1; fi; " + `
       "echo `"    repo=`$REPO`"; " + `
       "tar xzf /tmp/smb-gateway-src.tar.gz -C `"`$REPO`"; " + `
       "chmod +x `"`$REPO/deploy/dev-deploy-smb-gateway.sh`"; " + `
       "bash `"`$REPO/deploy/dev-deploy-smb-gateway.sh`" $arg"
& $sshbox run $cmd
if ($LASTEXITCODE -ne 0) { throw "remote deploy failed" }

Write-Host ""
Write-Host "Deploy finished" -ForegroundColor Green
