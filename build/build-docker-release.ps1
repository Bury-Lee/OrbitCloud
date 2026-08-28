# ============================================================
# OrbitCloud Docker 版本包一键打包(Windows / PowerShell 5.1+)
#
# 用法:
#   powershell -ExecutionPolicy Bypass -File build/build-docker-release.ps1
#   powershell ... -Version 1.4.6          # 指定版本(缺省读 config/default.yaml)
#
# 流程:构建镜像 → tag latest → docker save → 复制 compose →
#       生成使用说明(以 dist 中最近版本为模板,替换版本号)→ zip 打包
#
# 产物:
#   dist/OrbitCloud-v<Version>/
#     ├── orbitcloud.tar            docker save 镜像(部署用)
#     ├── docker-compose.yml        单机开箱即用
#     ├── docker-compose.rustfs.yml RustFS+PostgreSQL 生产版
#     └── 使用说明.md
#   dist/OrbitCloud-v<Version>.zip   上述目录的压缩包(带目录前缀)
# ============================================================
param(
    [string]$Version = ""
)

$ErrorActionPreference = "Stop"
$Root = Split-Path -Parent $PSScriptRoot
$DistRoot = Join-Path $Root "dist"

# ---------- 1. 版本号:参数优先,缺省读 config/default.yaml ----------
if (-not $Version) {
    $m = Select-String -Path (Join-Path $Root "config\default.yaml") -Pattern "^\s*version:\s*([\d.]+)"
    if (-not $m) { throw "无法从 config/default.yaml 读取版本号" }
    $Version = $m.Matches[0].Groups[1].Value
}
if ($Version -notmatch "^\d+\.\d+\.\d+$") { throw "版本号格式非法: $Version" }

$PkgDir = Join-Path $DistRoot "OrbitCloud-v$Version"
$TarPath = Join-Path $PkgDir "orbitcloud.tar"
$ZipPath = Join-Path $DistRoot "OrbitCloud-v$Version.zip"

Write-Host "==> 打包版本: $Version" -ForegroundColor Cyan

# ---------- 2. 构建镜像 + tag latest ----------
Write-Host "==> docker build orbitcloud:$Version" -ForegroundColor Cyan
docker build -t "orbitcloud:$Version" $Root
if ($LASTEXITCODE -ne 0) { throw "docker build 失败" }
docker tag "orbitcloud:$Version" "orbitcloud:latest"

# ---------- 3. 目录 + docker save ----------
New-Item -ItemType Directory -Path $PkgDir -Force | Out-Null
Write-Host "==> docker save -> orbitcloud.tar" -ForegroundColor Cyan
docker save "orbitcloud:$Version" -o $TarPath
if ($LASTEXITCODE -ne 0) { throw "docker save 失败" }

# ---------- 4. 复制 compose ----------
Copy-Item (Join-Path $Root "docker-compose.yml") $PkgDir
Copy-Item (Join-Path $Root "docker-compose.rustfs.yml") $PkgDir

# ---------- 5. 使用说明:以 dist 中最近版本为模板,替换版本号 ----------
$tpl = Get-ChildItem $DistRoot -Directory -Filter "OrbitCloud-v*" |
    Where-Object { $_.Name -ne "OrbitCloud-v$Version" -and (Test-Path (Join-Path $_.FullName "使用说明.md")) } |
    Sort-Object Name -Descending | Select-Object -First 1
if ($tpl) {
    $oldVer = $tpl.Name.Substring("OrbitCloud-v".Length) # 模板版本号
    # 纯版本号子串替换:覆盖 "v1.4.5"、"orbitcloud:1.4.5" 等全部出现位置
    (Get-Content (Join-Path $tpl.FullName "使用说明.md") -Encoding UTF8 -Raw) `
        -replace [regex]::Escape($oldVer), $Version |
        Set-Content (Join-Path $PkgDir "使用说明.md") -Encoding UTF8
    Write-Host "  使用说明: 以 $($tpl.Name) 为模板生成" -ForegroundColor DarkGray
} else {
    Write-Host "  [WARN] 未找到使用说明模板,请手动补充" -ForegroundColor Yellow
}

# ---------- 6. zip(带目录前缀,与历史包一致) ----------
Add-Type -AssemblyName System.IO.Compression.FileSystem
Remove-Item $ZipPath -ErrorAction SilentlyContinue
[System.IO.Compression.ZipFile]::CreateFromDirectory($PkgDir, $ZipPath)

# ---------- 7. 汇总 ----------
Write-Host "`n==> 打包完成:" -ForegroundColor Green
Get-ChildItem $PkgDir | ForEach-Object { Write-Host ("    {0,-32} {1,12}" -f $_.Name, $_.Length) }
Write-Host ("    {0,-32} {1,12}" -f "OrbitCloud-v$Version.zip", (Get-Item $ZipPath).Length)
