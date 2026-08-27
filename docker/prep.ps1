# prep.ps1 —— 构建前准备: 复制 dist 二进制到各服务上下文 + 生成 .env(含随机密钥)
# 用法: powershell -ExecutionPolicy Bypass -File prep.ps1
$ErrorActionPreference = "Stop"
$root = Split-Path -Parent $PSScriptRoot
$dist = Join-Path $root "dist"
$docker = $PSScriptRoot

$bins = @{
    "orbitcloud-linux-amd64" = "orbitcloud/bin/orbitcloud"
    "smb-gateway-linux-amd64" = "smb-gateway/bin/smb-gateway"
}
foreach ($src in $bins.Keys) {
    $from = Join-Path $dist $src
    $to = Join-Path $docker $bins[$src]
    if (-not (Test-Path $from)) {
        Write-Error "缺少二进制: $from(请先编译 dist,Linux amd64 版)"
    }
    New-Item -ItemType Directory -Path (Split-Path $to) -Force | Out-Null
    Copy-Item $from $to -Force
    Write-Output "复制: $from -> $to"
}

# RustFS 二进制来自 other_software(仓库内)
$rustfsFrom = Join-Path $root "other_software\rustfs"
$rustfsTo = Join-Path $docker "rustfs\bin\rustfs"
if (-not (Test-Path $rustfsFrom)) { Write-Error "缺少 RustFS 二进制: $rustfsFrom" }
New-Item -ItemType Directory -Path (Split-Path $rustfsTo) -Force | Out-Null
Copy-Item $rustfsFrom $rustfsTo -Force
Write-Output "复制: $rustfsFrom -> $rustfsTo"

$webSrc = Join-Path $dist "web"
$webDst = Join-Path $docker "web\web"
if (-not (Test-Path $webSrc)) { Write-Error "缺少前端产物: $webSrc" }
New-Item -ItemType Directory -Path $webDst -Force | Out-Null
Copy-Item "$webSrc\*" $webDst -Recurse -Force
Write-Output "复制: $webSrc -> $webDst"

# 生成 .env(不存在时,密钥随机)
$envFile = Join-Path $docker ".env"
if (-not (Test-Path $envFile)) {
    $jwt = -join ((1..64) | ForEach-Object { "{0:x}" -f (Get-Random -Max 16) })
    $key = -join ((1..32) | ForEach-Object { "{0:x}" -f (Get-Random -Max 16) })
    $pgPass = -join ((1..16) | ForEach-Object { "{0:x}" -f (Get-Random -Max 16) })
    @"
# OrbitCloud Docker 部署配置(由 prep.ps1 自动生成)
JWT_SECRET=$jwt
SHARED_KEY=$key
S3_ACCESS_KEY=rustfsadmin
S3_SECRET_KEY=rustfsadmin
PG_USER=orbitcloud
PG_PASSWORD=$pgPass
PG_DB=orbitcloud
PORT_WEB=18080
PORT_SMB=1445
PORT_API=8080
PORT_S3=9000
"@ | Set-Content $envFile -Encoding ascii
    Write-Output "已生成 .env(含随机密钥)"
} else {
    Write-Output ".env 已存在,跳过"
}

# 模板替换: 把密钥写入两侧挂载配置(确定性)
$env = @{}
Get-Content $envFile | ForEach-Object {
    if ($_ -match '^\s*([^#=]+)=(.*)$') { $env[$matches[1].Trim()] = $matches[2].Trim() }
}
function Resolve-Template($tplPath, $outPath) {
    $text = [System.IO.File]::ReadAllText($tplPath, [System.Text.Encoding]::UTF8)
    $text = $text.Replace('{{JWT_SECRET}}', $env['JWT_SECRET']).Replace('{{SHARED_KEY}}', $env['SHARED_KEY'])
    $text = $text.Replace('{{S3_ACCESS_KEY}}', $env['S3_ACCESS_KEY']).Replace('{{S3_SECRET_KEY}}', $env['S3_SECRET_KEY'])
    $text = $text.Replace('{{PG_USER}}', $env['PG_USER']).Replace('{{PG_PASSWORD}}', $env['PG_PASSWORD']).Replace('{{PG_DB}}', $env['PG_DB'])
    [System.IO.File]::WriteAllText($outPath, $text, [System.Text.UTF8Encoding]::new($false))
    Write-Output "生成配置: $outPath"
}
Resolve-Template (Join-Path $docker "orbitcloud\OrbitCloud.yaml.tpl") (Join-Path $docker "orbitcloud\OrbitCloud.yaml")
Resolve-Template (Join-Path $docker "smb-gateway\config.yaml.tpl") (Join-Path $docker "smb-gateway\config.yaml")
Write-Output "prep 完成: cd $docker && docker compose up -d --build"
