#requires -Version 5.1
<#
.SYNOPSIS
    OrbitCloud 一键构建脚本(Windows / PowerShell)。

.DESCRIPTION
    在 Windows 上交叉编译 OrbitCloud 后端,默认同时产出三个操作系统
    (Windows / Linux / macOS)的二进制,并可选择同步构建前端并打包。

    产物统一输出到 ./build/dist/,每个平台一个目录,内含:
      orbitcloud(.exe)              后端二进制(CGO_ENABLED=0,纯静态)
      web/                          前端构建产物(frontend/dist,可选)
      校验值文件                    sha256 校验和

.EXAMPLE
    # 默认:构建 windows+linux+darwin 的 amd64/arm64 后端,不构建前端,不打包
    .\build\build.ps1

    # 完整发布:三个系统后端 + 前端构建 + 打 zip/tar.gz 包
    .\build\build.ps1 -Frontend -Package -Version 1.0.0

    # 仅构建 Linux amd64,跳过 arm64
    .\build\build.ps1 -OSes linux -Archs amd64

    # 使用 batch 入口(避免 PowerShell 执行策略问题)
    build\build.bat -Frontend -Package
#>
[CmdletBinding()]
param(
    # 目标操作系统列表(默认:windows / linux / darwin)
    [string[]]$OSes = @("windows", "linux", "darwin"),

    # 目标 CPU 架构列表(默认:amd64 / arm64)
    [string[]]$Archs = @("amd64", "arm64"),

    # 版本号(仅用于产物/包名后缀;缺省取 "dev")
    [string]$Version = "",

    # 同时构建前端(frontend/,需 Node.js ≥ 18 且已 npm install)
    [switch]$Frontend,

    # 跳过前端构建(与 -Frontend 互斥,默认即跳过)
    [switch]$SkipFrontend,

    # 打包为 zip / tar.gz(Windows→zip,Linux/macOS→tar.gz)
    [switch]$Package,

    # 跳过打包(与 -Package 互斥,默认即跳过)
    [switch]$SkipPackage,

    # 仅校验环境(go / node 可用性)后退出
    [switch]$CheckOnly
)

Set-StrictMode -Version Latest
$ErrorActionPreference = "Stop"

# 统一控制台编码为 UTF-8,避免中文输出乱码(配合 chcp 65001)
try { [Console]::OutputEncoding = [System.Text.Encoding]::UTF8 } catch { }

# ---------------------------------------------------------------- 常量与路径
$ScriptRoot   = Split-Path -Parent $MyInvocation.MyCommand.Path
$RepoRoot     = Split-Path -Parent $ScriptRoot
$DistRoot     = Join-Path $ScriptRoot "dist"
$FrontendDir  = Join-Path $RepoRoot "frontend"
$LogFile      = Join-Path $DistRoot "build.log"

# ---------------------------------------------------------------- 小工具函数
function Write-Step([string]$msg) {
    Write-Host ""
    Write-Host "==> $msg" -ForegroundColor Cyan
}

function Write-Info([string]$msg) {
    Write-Host "    $msg" -ForegroundColor Gray
}

function Write-Ok([string]$msg) {
    Write-Host "    [OK] $msg" -ForegroundColor Green
}

function Write-Fail([string]$msg) {
    Write-Host "    [FAIL] $msg" -ForegroundColor Red
}

function Add-Log([string]$msg) {
    if (-not (Test-Path -LiteralPath $DistRoot)) {
        New-Item -ItemType Directory -Path $DistRoot -Force | Out-Null
    }
    Add-Content -LiteralPath $LogFile -Value ("[{0}] {1}" -f (Get-Date -Format "HH:mm:ss"), $msg)
}

# Invoke-Native 执行原生命令并把 stdout/stderr 统一作为字符串行返回。
# 背景:PS 5.1 下 2>&1 会把原生 stderr 转成 ErrorRecord,在 ErrorActionPreference=Stop
# 下会被误判为终止错误(如 rollup/编译器写 stderr 的警告);这里临时放开再恢复。
function Invoke-Native {
    param([scriptblock]$Script)
    $oldEAP = $ErrorActionPreference
    $ErrorActionPreference = "Continue"
    try {
        $lines = @(& $Script 2>&1 | ForEach-Object { "$_" })
    }
    finally {
        $ErrorActionPreference = $oldEAP
    }
    return ,$lines
}

# ---------------------------------------------------------------- 参数归一
# 兼容 "-OSes windows,linux" 与 "-OSes windows -OSes linux" 两种写法
$OSes  = @($OSes | ForEach-Object { $_ -split "," } | Where-Object { $_ })
$Archs = @($Archs | ForEach-Object { $_ -split "," } | Where-Object { $_ })

if ($Frontend -and $SkipFrontend) {
    throw "参数冲突:-Frontend 与 -SkipFrontend 不能同时使用"
}
if ($Package -and $SkipPackage) {
    throw "参数冲突:-Package 与 -SkipPackage 不能同时使用"
}
if ($SkipFrontend) { $Frontend = $false }

if ([string]::IsNullOrWhiteSpace($Version)) {
    # 优先取 git 最新 tag,否则取 dev。
    # 注意:git 无 tag 时 stderr 输出 fatal 行,须用 $LASTEXITCODE + 格式校验过滤。
    $tag = ""
    if (Get-Command git -ErrorAction SilentlyContinue) {
        $tagOut = @(Invoke-Native { & git describe --tags --abbrev=0 })
        if ($LASTEXITCODE -eq 0) {
            foreach ($line in $tagOut) {
                $t = $line.Trim()
                if ($t -match '^v?[0-9]+(\.[0-9]+)*') { $tag = $t; break }
            }
        }
    }
    $Version = if ($tag) { $tag } else { "dev" }
}
$Version = $Version.TrimStart("v")

# ---------------------------------------------------------------- 环境检查
Write-Step "环境检查"
$needNode = $false
if ($Frontend) { $needNode = $true }

$goBin = Get-Command go -ErrorAction SilentlyContinue
if (-not $goBin) { throw "未找到 Go:请先安装 Go 1.25+ 并加入 PATH(https://go.dev/dl/)" }
$goVer = & go version
Write-Ok $goVer
Add-Log $goVer

if ($needNode) {
    $nodeBin = Get-Command node -ErrorAction SilentlyContinue
    $npmBin  = Get-Command npm.cmd -ErrorAction SilentlyContinue
    if (-not $nodeBin) { throw "未找到 Node.js:构建前端需 Node.js ≥ 18(https://nodejs.org/)" }
    if (-not $npmBin)  { throw "未找到 npm:请随 Node.js 一起安装" }
    Write-Ok (& node --version)
    Write-Ok "npm $(& npm.cmd --version)"
}

# 输出目标组合
Write-Info "构建目标:"
foreach ($os in $OSes) {
    foreach ($arch in $Archs) {
        Write-Info "  - $os/$arch"
    }
}
Write-Info "前端构建:$(if ($Frontend) { '开启' } else { '跳过' }) | 打包:$(if ($Package) { '开启' } else { '跳过' }) | 版本:$Version"

if ($CheckOnly) {
    Write-Ok "环境检查通过(go + node 可用)"
    exit 0
}

# ---------------------------------------------------------------- 准备目录
Write-Step "准备输出目录"
New-Item -ItemType Directory -Path $DistRoot -Force | Out-Null
Remove-Item -LiteralPath $LogFile -Force -ErrorAction SilentlyContinue
Add-Log "orbitcloud build start (version=$Version, frontend=$Frontend, package=$Package)"

# ---------------------------------------------------------------- 1. 构建前端
if ($Frontend) {
    Write-Step "构建前端(frontend/)"
    if (-not (Test-Path -LiteralPath (Join-Path $FrontendDir "node_modules"))) {
        Write-Info "未发现 node_modules,先执行 npm install ..."
        Push-Location $FrontendDir
        try {
            npm.cmd install | Out-Null
            if ($LASTEXITCODE -ne 0) { throw "npm install 失败(退出码 $LASTEXITCODE)" }
        }
        finally { Pop-Location }
    }
    Push-Location $FrontendDir
    try {
        Write-Info "执行 npm run build(含 vue-tsc 类型检查)..."
        $out = Invoke-Native { & npm.cmd run build }
        $out | ForEach-Object { Add-Log $_ }
        if ($LASTEXITCODE -ne 0) { throw "npm run build 失败(退出码 $LASTEXITCODE),详见 $LogFile" }
    }
    finally { Pop-Location }
    Write-Ok "前端产物:frontend/dist"
}

# ---------------------------------------------------------------- 2. 交叉编译后端
$binaries = @()
foreach ($os in $OSes) {
    foreach ($arch in $Archs) {
        Write-Step "编译 $os/$arch"

        $exe = if ($os -eq "windows") { "orbitcloud.exe" } else { "orbitcloud" }
        $pkgDir = "orbitcloud-$os-$arch-$Version"
        $outDir = Join-Path $DistRoot $pkgDir
        $binPath = Join-Path $outDir $exe

        New-Item -ItemType Directory -Path $outDir -Force | Out-Null

        $env:GOOS   = $os
        $env:GOARCH = $arch
        $env:CGO_ENABLED = "0"   # glebarez/sqlite 为纯 Go 实现,可安全静态交叉编译

        Push-Location $RepoRoot
        try {
            $out = Invoke-Native { & go build -trimpath -ldflags "-s -w" -o $binPath . }
            $out | ForEach-Object { Add-Log $_ }
            if ($LASTEXITCODE -ne 0) { throw "go build $os/$arch 失败(退出码 $LASTEXITCODE)" }
        }
        finally { Pop-Location }

        # 拷贝前端产物到包内 web/ 目录(便于 Nginx 直接托管)
        if ($Frontend -and (Test-Path -LiteralPath (Join-Path $FrontendDir "dist"))) {
            Copy-Item -Path (Join-Path $FrontendDir "dist") -Destination (Join-Path $outDir "web") -Recurse -Force
            Write-Info "已附带前端产物 → $pkgDir/web/"
        }

        # 计算 sha256
        $hash = (Get-FileHash -LiteralPath $binPath -Algorithm SHA256).Hash.ToLowerInvariant()
        Add-Content -LiteralPath (Join-Path $outDir "sha256sums.txt") -Value ("{0}  {1}" -f $hash, $exe)
        Write-Ok "$binPath ($([math]::Round((Get-Item -LiteralPath $binPath).Length / 1MB, 1)) MB)"

        $binaries += [pscustomobject]@{ OS = $os; Arch = $arch; Dir = $outDir; Exe = $exe; Hash = $hash }
        Add-Log "built $os/$arch -> $binPath ($hash)"
    }
}

# 还原环境变量(避免影响后续命令)
Remove-Item Env:GOOS, Env:GOARCH, Env:CGO_ENABLED -ErrorAction SilentlyContinue

# ---------------------------------------------------------------- 3. 打包
if ($Package) {
    Write-Step "打包"
    foreach ($b in $binaries) {
        $pkgDir = "orbitcloud-$($b.OS)-$($b.Arch)-$Version"
        if ($b.OS -eq "windows") {
            $pkgFile = Join-Path $DistRoot ($pkgDir + ".zip")
            Compress-Archive -Path (Join-Path $DistRoot $pkgDir) -DestinationPath $pkgFile -Force
        }
        else {
            $pkgFile = Join-Path $DistRoot ($pkgDir + ".tar.gz")
            # Windows 10 1803+ 自带 bsdtar;老系统缺失时回退 zip
            $tar = Get-Command tar -ErrorAction SilentlyContinue
            if ($tar) {
                Push-Location $DistRoot
                try {
                    $out = Invoke-Native { & tar -czf $pkgFile $pkgDir }
                    $out | ForEach-Object { Add-Log $_ }
                    if ($LASTEXITCODE -ne 0) { throw "tar 打包失败(退出码 $LASTEXITCODE)" }
                }
                finally { Pop-Location }
            }
            else {
                Write-Info "未找到 tar,Linux/macOS 包改用 zip"
                $pkgFile = Join-Path $DistRoot ($pkgDir + ".zip")
                Compress-Archive -Path (Join-Path $DistRoot $pkgDir) -DestinationPath $pkgFile -Force
            }
        }
        Write-Ok $pkgFile
        Add-Log "packed $pkgFile"
    }
}

# ---------------------------------------------------------------- 4. 汇总
Write-Step "构建完成"
$binaries | Format-Table -AutoSize OS, Arch, Hash, Dir | Out-String | Write-Host
Write-Info "产物目录:$DistRoot"
Write-Info "构建日志:$LogFile"
Write-Info "提示:各平台运行前先执行 <binary> -initConfig 生成配置,再 <binary> --add-superadmin <user> <pass> 创建管理员"
