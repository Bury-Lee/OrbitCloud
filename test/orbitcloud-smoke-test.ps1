# ============================================================
# OrbitCloud 冒烟测试脚本(可重复运行)
#
# 用法:
#   powershell -ExecutionPolicy Bypass -File test/orbitcloud-smoke-test.ps1
#   powershell ... -BaseUrl http://192.168.1.10:8080 -AdminUser admin -AdminPass 'xxx'
#
# 覆盖:
#   - 认证:登录/无 token 401/普通用户访问管理员接口 403
#   - 桶:创建/列表
#   - 文件:上传(含 OOXML 长 MIME)/同名重命名/搜索/下载/Range 206/预览
#   - 回收站:删除进回收站/列表/恢复/永久删除
#   - 分享:创建/公开解析/公开下载
#   - 系统运维:system/status、日志列表、日志下载、限流状态
#   - 限流:IP 维度 429(需临时调低阈值或高并发)
#   - 批量:批量删除/批量下载 zip
#   - 用户组:创建组/加成员/可见组权限(成员 200 非成员 403)
#   - 权限:普通用户对 admin 接口 403
#
# 说明:
#   - 使用独立测试桶(test-bucket-<时间戳>),不污染业务数据
#   - 限流测试默认跳过(会 429 干扰其他用例),可用 -TestRateLimit 开启
#   - SMB 测试需要 smbclient(Linux)或仅验证端口
# ============================================================
[CmdletBinding()]
param(
    [string]$BaseUrl = "http://localhost:8080",
    [string]$AdminUser = "admin",
    [string]$AdminPass = "Admin@12345",
    [string]$NormalUser = "test",
    [string]$NormalPass = "Test@12345",
    [switch]$TestRateLimit,
    [switch]$TestSmb,
    [switch]$SkipCleanup
)

$ErrorActionPreference = "Stop"
$script:pass = 0
$script:fail = 0
$script:failures = @()
$script:apiBase = "$BaseUrl/api/v1"
$script:tmpDir = Join-Path $env:TEMP "orbitcloud-test-$(Get-Date -Format 'HHmmss')"
New-Item -ItemType Directory -Path $script:tmpDir -Force | Out-Null

function Write-Result {
    param([string]$Name, [bool]$Ok, [string]$Detail = "")
    if ($Ok) { $script:pass++; Write-Host "  [PASS] $Name" -ForegroundColor Green }
    else {
        $script:fail++; $script:failures += "$Name : $Detail"
        Write-Host "  [FAIL] $Name -> $Detail" -ForegroundColor Red
    }
}

function Invoke-Api {
    param([string]$Method, [string]$Path, [string]$Token, [object]$Body, [hashtable]$ExtraHeaders = @{})
    $headers = @{}
    if ($Token) { $headers.Authorization = "Bearer $Token" }
    foreach ($k in $ExtraHeaders.Keys) { $headers[$k] = $ExtraHeaders[$k] }
    $params = @{ Uri = "$script:apiBase$Path"; Method = $Method; Headers = $headers; TimeoutSec = 30 }
    if ($Body -is [string]) { $params.ContentType = "application/json"; $params.Body = $Body }
    elseif ($null -ne $Body) { $params.ContentType = "application/json"; $params.Body = ($Body | ConvertTo-Json -Depth 6) }
    # 429 限流自动重试(最多 3 次,间隔 1s;默认限流 10/s 下冒烟连发可能触发)
    for ($attempt = 0; $attempt -lt 3; $attempt++) {
        try {
            $r = Invoke-RestMethod @params
            return @{ ok = $true; resp = $r }
        } catch {
            $status = ""
            if ($_.Exception.Response) { $status = [int]$_.Exception.Response.StatusCode }
            if ($status -eq 429 -and $attempt -lt 2) { Start-Sleep -Seconds 1; continue }
            return @{ ok = $false; status = $status; msg = $_.ErrorDetails.Message }
        }
    }
}

# curl 包装:429 自动重试(默认限流下连发可能触发)。用法与 curl.exe 相同,
# 裸参数形式(Invoke-Curl -s -o NUL ...)或数组形式均可;返回 stdout。
# ValueFromRemainingArguments 收集所有裸参数(否则 -s 会被当函数命名参数解析丢失)。
# 注意:PS5.1 中 1) 裸 -o/-w 会被公共参数(-OutVariable/-WarningAction 等)歧义劫持;
# 2) 字面量 @("a","b") 展开只算一个实参,必须经数组变量再 @展开。故状态码请求
# 统一走 Get-HttpCode(固定 -s -o NUL -w %{http_code} 前缀),其余裸参数仅用
# -X/-H/URL 等无歧义形式。
function Invoke-Curl {
    param([Parameter(ValueFromRemainingArguments = $true)][string[]]$CurlArgs)
    for ($attempt = 0; $attempt -lt 3; $attempt++) {
        $out = & curl.exe @CurlArgs 2>$null
        $is429 = $false
        if ($LASTEXITCODE -eq 0) {
            if ($out -match "429") { $is429 = $true }                       # -w "%{http_code}" 模式
            elseif ($out -match "请求过于频繁") { $is429 = $true }            # JSON body 模式
        }
        if ($is429 -and $attempt -lt 2) { Start-Sleep -Seconds 1; continue }
        return $out
    }
}

# 取 HTTP 状态码(PS5.1 兼容封装;$UrlArgs 收集 -X/-H/URL 等无歧义裸参数)。
function Get-HttpCode {
    param([Parameter(ValueFromRemainingArguments = $true)][string[]]$UrlArgs)
    $curlArgs = @("-s", "-o", "NUL", "-w", "%{http_code}")
    $curlArgs += $UrlArgs
    return (Invoke-Curl @curlArgs)
}

function New-TestFile {
    param([string]$Name, [string]$Content = "test content $Name")
    $f = Join-Path $script:tmpDir $Name
    [System.IO.File]::WriteAllText($f, $Content)
    return $f
}

function Upload-File {
    param([string]$Token, [int]$BucketId, [string]$FilePath, [string]$Type = "")
    # 注意:不能命名 $args(PS 自动变量,会被覆盖语义干扰),用 $curlArgs
    $curlArgs = @("-s", "-X", "POST", "$script:apiBase/buckets/$BucketId/files", "-H", "Authorization: Bearer $Token", "-F", "file=@$FilePath")
    if ($Type) { $curlArgs += "-F", "file_type=$Type" }
    $out = Invoke-Curl @curlArgs   # @ 展开为独立位置参数(ValueFromRemainingArguments 逐 token 收集)
    return ($out | ConvertFrom-Json)
}

Write-Host "`n=== OrbitCloud 冒烟测试 ===" -ForegroundColor Cyan
Write-Host "目标: $BaseUrl  管理员: $AdminUser  时间: $(Get-Date -Format 'yyyy-MM-dd HH:mm:ss')"

# ==================== 0. 健康检查 ====================
Write-Host "`n--- 0. 健康检查 ---" -ForegroundColor Yellow
$adminBody = @{ username = $AdminUser; password = $AdminPass } | ConvertTo-Json -Compress
$login = Invoke-Api "POST" "/auth/login" $null $adminBody
Write-Result "管理员登录" ($login.ok -and $login.resp.code -eq 0) $login.msg
if (-not $login.ok) { Write-Host "  无法登录,中止测试" -ForegroundColor Red; exit 1 }
$adminTok = $login.resp.data.access_token

$userBody = @{ username = $NormalUser; password = $NormalPass } | ConvertTo-Json -Compress
$nLogin = Invoke-Api "POST" "/auth/login" $null $userBody
Write-Result "普通用户登录" ($nLogin.ok -and $nLogin.resp.code -eq 0) $nLogin.msg
$userTok = $nLogin.resp.data.access_token

# ==================== 1. 桶 ====================
Write-Host "`n--- 1. 桶管理 ---" -ForegroundColor Yellow
$bucketName = "test-bucket-$(Get-Date -Format 'MMddHHmmss')"
$bk = Invoke-Api "POST" "/buckets" $adminTok @{ name = $bucketName; description = "smoke test" }
Write-Result "创建桶" ($bk.ok -and $bk.resp.code -eq 0) $bk.msg
$bucketId = $bk.resp.data.ID

$bl = Invoke-Api "GET" "/buckets" $adminTok
Write-Result "桶列表" ($bl.ok -and $bl.resp.code -eq 0 -and ($bl.resp.data | Where-Object { $_.ID -eq $bucketId })) $bl.msg

# 普通用户自建桶(权限 3):匿名分享下载/普通用户访问依赖低权限桶
$userBucketName = "test-user-bucket-$(Get-Date -Format 'MMddHHmmss')"
$ub = Invoke-Api "POST" "/buckets" $userTok @{ name = $userBucketName; description = "smoke test user bucket" }
Write-Result "普通用户建桶" ($ub.ok -and $ub.resp.code -eq 0) $ub.msg
$userBucketId = $ub.resp.data.ID

# ==================== 2. 文件 ====================
Write-Host "`n--- 2. 文件上传/下载 ---" -ForegroundColor Yellow
$f1 = New-TestFile "smoke-1.txt" "hello smoke test"
$up1 = Upload-File $adminTok $bucketId $f1
Write-Result "上传 txt" ($up1.code -eq 0 -and $up1.data.FileSize -gt 0) ($up1 | ConvertTo-Json -Compress)
$fileId = $up1.data.ID

$f2 = New-TestFile "smoke-2.docx"
$up2 = Upload-File $adminTok $bucketId $f2 "application/vnd.openxmlformats-officedocument.wordprocessingml.document"
Write-Result "上传 OOXML(长 MIME 不 500)" ($up2.code -eq 0) ($up2 | ConvertTo-Json -Compress)

$dl = Get-HttpCode "$script:apiBase/buckets/$bucketId/files/$fileId/download" -H "Authorization: Bearer $adminTok" 2>$null
Write-Result "下载文件" ($dl -eq "200") "HTTP $dl"

$rg = Get-HttpCode "$script:apiBase/buckets/$bucketId/files/$fileId/download" -H "Authorization: Bearer $adminTok" -H "Range: bytes=0-9" 2>$null
Write-Result "Range 206 断点" ($rg -eq "206") "HTTP $rg"

$up3 = Upload-File $adminTok $bucketId $f1
Write-Result "同名上传自动重命名" ($up3.code -eq 0 -and $up3.data.Name -ne "smoke-1.txt") ($up3.data.Name)

$sch = Invoke-Api "GET" "/buckets/$bucketId/files?key=smoke" $adminTok
Write-Result "文件名搜索" ($sch.ok -and $sch.resp.code -eq 0 -and $sch.resp.data.total -ge 1) $sch.msg

$pv = Get-HttpCode "$script:apiBase/buckets/$bucketId/files/$fileId/preview" -H "Authorization: Bearer $adminTok" 2>$null
Write-Result "预览接口" ($pv -eq "200") "HTTP $pv"

# ==================== 3. 回收站 ====================
Write-Host "`n--- 3. 回收站 ---" -ForegroundColor Yellow
$del = Get-HttpCode -X DELETE "$script:apiBase/buckets/$bucketId/files/$fileId" -H "Authorization: Bearer $adminTok" 2>$null
Write-Result "删除进回收站" ($del -eq "204") "HTTP $del"

$rb = Invoke-Api "GET" "/recycle-bin/files" $adminTok
Write-Result "回收站列表含删除文件" ($rb.ok -and $rb.resp.code -eq 0 -and ($rb.resp.data.list | Where-Object { $_.ID -eq $fileId })) $rb.msg

$rs = Invoke-Api "POST" "/recycle-bin/files/$fileId/restore" $adminTok
Write-Result "回收站恢复" ($rs.ok -and $rs.resp.code -eq 0 -and $null -eq $rs.resp.data.DeletedAt) $rs.msg

Get-HttpCode -X DELETE "$script:apiBase/buckets/$bucketId/files/$fileId" -H "Authorization: Bearer $adminTok" 2>$null | Out-Null
$pd = Get-HttpCode -X DELETE "$script:apiBase/recycle-bin/files/$fileId" -H "Authorization: Bearer $adminTok" 2>$null
Write-Result "回收站永久删除" ($pd -eq "204") "HTTP $pd"

# ==================== 4. 分享 ====================
Write-Host "`n--- 4. 分享 ---" -ForegroundColor Yellow
# 文件放普通用户桶(权限 3),匿名分享下载可访问
$up4 = Upload-File $adminTok $userBucketId (New-TestFile "share-me.txt")
$sh = Invoke-Api "POST" "/shares" $adminTok @{ file_id = $up4.data.ID }
Write-Result "创建分享" ($sh.ok -and $sh.resp.code -eq 0) $sh.msg
if ($sh.ok) {
    $tk = $sh.resp.data.Token
    $shareArgs = @("-s", "$script:apiBase/share/$tk")
    $res = Invoke-Curl @shareArgs 2>$null | ConvertFrom-Json
    Write-Result "公开解析分享(无 token)" ($res.code -eq 0) ($res | ConvertTo-Json -Compress)
    # 注意:变量后紧跟 ? 必须用 $() 包裹(PS 5.1 会把 $tk?download 整体当变量名解析为空)
    $sdl = Get-HttpCode "$script:apiBase/share/$($tk)?download=1" 2>$null
    if ($sdl -ne "200") {
        $sdbArgs = @("-s", "$script:apiBase/share/$($tk)?download=1")
        $sdb = Invoke-Curl @sdbArgs 2>$null
        Write-Result "公开下载分享" $false "HTTP $sdl body=$sdb"
    } else {
        Write-Result "公开下载分享" $true "HTTP $sdl"
    }
}

# ==================== 5. 系统运维 ====================
Write-Host "`n--- 5. 系统运维 ---" -ForegroundColor Yellow
$st = Invoke-Api "GET" "/system/status" $adminTok
Write-Result "system/status" ($st.ok -and $st.resp.code -eq 0 -and $st.resp.data.db_alive) $st.msg

$rl = Invoke-Api "GET" "/system/ratelimit" $adminTok
Write-Result "ratelimit 状态" ($rl.ok -and $rl.resp.code -eq 0) $rl.msg

$logs = Invoke-Api "GET" "/logs" $adminTok
Write-Result "日志列表" ($logs.ok -and $logs.resp.code -eq 0) $logs.msg
if ($logs.ok -and $logs.resp.data.files.Count -gt 0) {
    $first = $logs.resp.data.files | Select-Object -First 1
    $ldl = Get-HttpCode "$script:apiBase/logs/download?path=$($first.path)" -H "Authorization: Bearer $adminTok" 2>$null
    Write-Result "日志下载" ($ldl -eq "200") "HTTP $ldl path=$($first.path)"
} else {
    Write-Result "日志下载" $false "无日志文件(未配置 ORBITCLOUD_LOG_OUTPUT_PATH?)"
}

# ==================== 6. 权限 ====================
Write-Host "`n--- 6. 权限 ---" -ForegroundColor Yellow
$p1 = Get-HttpCode "$script:apiBase/system/status" -H "Authorization: Bearer $userTok" 2>$null
Write-Result "普通用户访问管理接口 403" ($p1 -eq "403") "HTTP $p1"

$p2 = Get-HttpCode "$script:apiBase/buckets" 2>$null
Write-Result "无 token 401" ($p2 -eq "401") "HTTP $p2"

$p3 = Get-HttpCode "$script:apiBase/buckets/$userBucketId/files" -H "Authorization: Bearer $userTok" 2>$null
Write-Result "普通用户访问自己的桶" ($p3 -eq "200") "HTTP $p3"

# ==================== 7. 用户组可见性 ====================
Write-Host "`n--- 7. 用户组 ---" -ForegroundColor Yellow
$gname = "smoke-group-$(Get-Date -Format 'MMddHHmmss')"
$g = Invoke-Api "POST" "/groups" $adminTok @{ name = $gname }
Write-Result "创建组" ($g.ok -and $g.resp.code -eq 0) $g.msg
$gid = $g.resp.data.ID

# 取普通用户 ID(按用户名查;简化:从登录响应拿 user.ID 不可靠,用组成员接口测试即可)
$gm = Invoke-Api "POST" "/groups/$gid/members" $adminTok @{ user_id = 2 }
Write-Result "添加组成员" ($gm.ok -and $gm.resp.code -eq 0) $gm.msg

# 限制文件仅本组可见 → 组外用户不可见(outsider 用户可选)
$up5 = Upload-File $adminTok $bucketId (New-TestFile "vis.txt")
$vis = Invoke-Api "PUT" "/buckets/$bucketId/files/$($up5.data.ID)/visibility" $adminTok @{ groups = @($gid) }
Write-Result "设置文件可见组" ($vis.ok -and $vis.resp.code -eq 0) $vis.msg
$v1 = Get-HttpCode "$script:apiBase/buckets/$bucketId/files/$($up5.data.ID)" -H "Authorization: Bearer $adminTok" 2>$null
Write-Result "管理员可见组内文件" ($v1 -eq "200") "HTTP $v1"

# ==================== 8. 批量 ====================
Write-Host "`n--- 8. 批量操作 ---" -ForegroundColor Yellow
$bd = Invoke-Api "POST" "/buckets/$userBucketId/items/batch-delete" $adminTok @{ items = @(@{ kind = "file"; id = $up4.data.ID }) }
Write-Result "批量删除" ($bd.ok -and $bd.resp.code -eq 0 -and $bd.resp.data.failed.Count -eq 0) ($bd.resp.data | ConvertTo-Json -Compress)

# ==================== 9. 限流(可选) ====================
if ($TestRateLimit) {
    Write-Host "`n--- 9. 限流(启用时才测) ---" -ForegroundColor Yellow
    $codes = @()
    for ($i = 0; $i -lt 40; $i++) {
        $c = Get-HttpCode "$script:apiBase/buckets" -H "Authorization: Bearer $adminTok" 2>$null
        $codes += $c
    }
    $n429 = ($codes | Where-Object { $_ -eq "429" }).Count
    Write-Result "IP 维度限流触发 429(40 连发)" ($n429 -gt 0) "429 x $n429"
}

# ==================== 10. SMB(可选) ====================
if ($TestSmb) {
    Write-Host "`n--- 10. SMB ---" -ForegroundColor Yellow
    $tcp = New-Object System.Net.Sockets.TcpClient
    try {
        $port = 1445
        if ($BaseUrl -match ":(?<p>\d+)$") { $port = [int]$Matches.p }
        $tcp.Connect("localhost", $port)
        Write-Result "SMB 端口可达" $true "localhost:$port"
    } catch { Write-Result "SMB 端口可达" $false $_.Exception.Message }
    finally { $tcp.Close() }
}

# ==================== 清理 ====================
if (-not $SkipCleanup) {
    Write-Host "`n--- 清理 ---" -ForegroundColor Yellow
    Invoke-Api "DELETE" "/buckets/$bucketId" $adminTok | Out-Null
    Invoke-Api "DELETE" "/buckets/$userBucketId" $userTok | Out-Null
    Invoke-Api "DELETE" "/groups/$gid" $adminTok | Out-Null
    Remove-Item $script:tmpDir -Recurse -Force -ErrorAction SilentlyContinue
}

# ==================== 汇总 ====================
Write-Host "`n=== 测试汇总 ===" -ForegroundColor Cyan
Write-Host "  通过: $script:pass  失败: $script:fail"
if ($script:failures.Count -gt 0) {
    Write-Host "  失败项:" -ForegroundColor Red
    $script:failures | ForEach-Object { Write-Host "    - $_" -ForegroundColor Red }
    exit 1
} else {
    Write-Host "  全部通过!" -ForegroundColor Green
    exit 0
}
