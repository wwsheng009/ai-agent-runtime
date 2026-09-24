<#
.SYNOPSIS
  E2E-DEBUG-02：非回环鉴权（--web-host 0.0.0.0 + X-AICLI-Token）验收。

.DESCRIPTION
  E2E-DEBUG-01（scripts/test-aicli-debug-endpoints-e2e.ps1）覆盖回环模式的
  Host/Origin + 写令牌（GET 免令牌、POST 需令牌）。本脚本覆盖**非回环模式**的
  鉴权契约（docs/aicli/web-remote-api.md §1 鉴权、backend/cmd/aicli/commands/web_auth.go）：

    1. 启动：chat --yolo --web-host 0.0.0.0 --web-port <p> --web-token <固定令牌>；
    2. /debug/endpoints 报 listen_mode=non-loopback、write_auth_hint 提示
       ALL 请求（含 GET/SSE）需令牌，且 JSON 与 ?format=text 均不泄露令牌原文；
    3. 从**非回环本机 IP**（局域网地址；RemoteAddr 不是 127.0.0.1）发起：
       - 无令牌 GET /web/api/screen → 403 forbidden（reason 含 non-loopback）；
       - 携带 X-AICLI-Token 头 → 200；
       - 用 ?token= 查询参数 → 200；
       - 错误令牌 → 403；
       - 无令牌 POST /web/api/input → 403（拒绝发生在执行之前）；
       - 带令牌 POST /web/api/input → 200；
       - 无令牌 GET /debug/pprof/ → 403，带令牌 → 200（/debug/* 与 /web/api/* 同权）；
       - 无令牌 GET /web/api/token → 403；带令牌 → 200 且回显 header/token/query_param
         （token 与 --web-token 入参一致、source=--web-token）；
       - 无令牌 GET /web/api/events（SSE）→ 403（SSE 不豁免）；
       - 无令牌 GET /web/ → 403（index.html 不算静态资产），带 ?token= → 200
         且页面注入令牌 meta（浏览器自动附加，无需手工操作）；
    4. 两个豁免（实现契约，回归红线）：
       - 回环 IP（127.0.0.1）发起的请求始终免令牌（读与写都免）；
       - /web/** 静态资产（非 /web/api/*）GET 免令牌（浏览器 <link>/<script> 带不了令牌）；
    5. 清单自描述：JSON 与 ?format=text 不含令牌原文，且 LAN 访问地址用 token=<token>
       占位符（正向锁定：只测"不含令牌"会放过"整段 LAN 区块被删"的退化实现）；
    6. 收尾：带令牌 POST /web/api/input {"prompt":"/exit"} → 优雅退出、退出码 0、端口释放。
       注意 interrupt 探针会让会话短暂进入清理态，此刻命令门以 status=rejected 拒绝
       /exit（不是鉴权问题）；脚本在 ExitTimeoutSec 内做有界重试，证据里记录 attempts。

  环境前提：本机存在非回环 IPv4 地址（自动探测，可用 -LanIp 指定）。探测不到时
  依赖 LAN 的断言记为 SKIP（不计入 PASS/FAIL，但写入 run.log 与 summary.json），
  避免在无网卡/受限环境产生假红；SKIP 原因会在总结里显式打印。

.PARAMETER ExePath
  被测二进制；缺省 <repo>/backend/.tmp/aicli-debug-e2e.exe（与 E2E-DEBUG-01 共用构建产物）。

.PARAMETER Port
  监听端口；0（缺省）= 自动挑空闲端口。

.PARAMETER ListenHost
  监听地址；缺省 0.0.0.0（非回环模式）。

.PARAMETER WebToken
  显式写令牌（>=16 位、字符集 A-Za-z0-9-._~）；缺省用固定 e2e 令牌，便于断言启动行回显。

.PARAMETER LanIp
  指定用于非回环请求的本机 IP；缺省自动探测（Dns.GetHostAddresses + Get-NetIPAddress）。

.PARAMETER SkipBuild
  跳过 go build，复用 -ExePath。

.PARAMETER KeepAlive
  人工排查：不执行 /exit 收尾，脚本强制结束并判 FAIL（避免误当通过）。

.PARAMETER ArtifactDir
  证据目录；缺省 artifacts/aicli-debug-auth-e2e/<yyyyMMdd-HHmmss>。

.EXAMPLE
  pwsh -NoProfile -File scripts/test-aicli-debug-endpoints-e2e-nonloopback.ps1

.EXAMPLE
  pwsh -NoProfile -File scripts/test-aicli-debug-endpoints-e2e-nonloopback.ps1 -SkipBuild -LanIp 192.168.1.10
#>
[CmdletBinding()]
param(
    [string]$ExePath,
    [ValidateRange(0, 65535)][int]$Port = 0,
    [string]$ListenHost = '0.0.0.0',
    [string]$WebToken = 'e2e-nonloopback-token-0123456789',
    [string]$LanIp = '',
    [ValidateRange(5, 600)][int]$StartupTimeoutSec = 90,
    [ValidateRange(5, 600)][int]$ExitTimeoutSec = 60,
    [switch]$SkipBuild,
    [switch]$KeepAlive,
    [string]$ArtifactDir
)

$ErrorActionPreference = 'Stop'

$repoRoot = Split-Path -Parent $PSScriptRoot
$backend = Join-Path $repoRoot 'backend'

if ([string]::IsNullOrWhiteSpace($ArtifactDir)) {
    $stamp = Get-Date -Format 'yyyyMMdd-HHmmss'
    $ArtifactDir = Join-Path $repoRoot "artifacts/aicli-debug-auth-e2e/$stamp"
} elseif (-not [System.IO.Path]::IsPathRooted($ArtifactDir)) {
    $ArtifactDir = Join-Path $repoRoot $ArtifactDir
}
New-Item -ItemType Directory -Path $ArtifactDir -Force | Out-Null
# 网格根隔离（同 01）：本场景不测网格，但进程默认会加入网格并写真实
# ~/.aicli/mesh；指到证据目录既避免污染，也让本场景不受本机其它节点影响。
$meshDir = Join-Path $ArtifactDir 'mesh'
New-Item -ItemType Directory -Path $meshDir -Force | Out-Null

$runLogPath = Join-Path $ArtifactDir 'run.log'
$stdoutPath = Join-Path $ArtifactDir 'aicli.stdout.log'
$stderrPath = Join-Path $ArtifactDir 'aicli.stderr.log'
$summaryPath = Join-Path $ArtifactDir 'summary.json'

function Write-Log {
    param([string]$Message)
    $line = '[{0}] {1}' -f (Get-Date -Format 'HH:mm:ss'), $Message
    Write-Host $line
    Add-Content -LiteralPath $runLogPath -Value $line -Encoding UTF8
}

$script:results = New-Object System.Collections.Generic.List[object]
$script:skips = New-Object System.Collections.Generic.List[object]
# 探针复用的 HttpClient（见 Invoke-AuthProbe）：跨请求复用连接，且不依赖
# Invoke-WebRequest 的异常语义。
$script:httpClient = $null

function Add-Result {
    param([string]$Name, [bool]$Passed, [string]$Detail)
    $script:results.Add([pscustomobject]@{ name = $Name; passed = $Passed; detail = $Detail })
    $tag = 'FAIL'
    if ($Passed) { $tag = 'PASS' }
    Write-Log ("[{0}] {1} :: {2}" -f $tag, $Name, $Detail)
}

# Add-Skip：环境不具备（如无非回环 IPv4）时使用；不计入 PASS/FAIL，但必须显式记录，
# 避免"静默通过"。summary.json 与最终总结都会打印 SKIP 数。
function Add-Skip {
    param([string]$Name, [string]$Reason)
    $script:skips.Add([pscustomobject]@{ name = $Name; reason = $Reason })
    Write-Log ("[SKIP] {0} :: {1}" -f $Name, $Reason)
}

# Invoke-AuthProbe：直接用 System.Net.Http.HttpClient 收发（UTF-8），
# **不把非 2xx 当异常抛出**：403 是本脚本的预期结果，必须能拿到状态码与响应体。
#
# 为什么不用 Invoke-WebRequest：E2E-DEBUG-02 首次实跑时，PowerShell 7 的
# Invoke-WebRequest 在 403 上抛出的 HttpResponseException 既没有 ErrorDetails.Message，
# 也没能给出可读的 Response 流，导致"403 + JSON 拒绝体"被误判为"403 + 空体"。
# HttpClient 直接返回状态码与 body，行为与 PowerShell 版本无关，探针更可靠。
function Invoke-AuthProbe {
    param(
        [Parameter(Mandatory)][string]$Method,
        [Parameter(Mandatory)][string]$Url,
        $Body,
        [hashtable]$Headers,
        [int]$TimeoutSec = 20,
        # 只读到响应头即返回：用于可能长时间流式（SSE）的 URL——若护栏失效导致
        # 请求被放行，读正文会挂到超时并抛异常，把"断言失败"变成"脚本崩溃"。
        [switch]$HeadersOnly
    )
    if ($null -eq $script:httpClient) {
        $handler = [System.Net.Http.HttpClientHandler]::new()
        # 本机/LAN 直连，绕开系统代理，避免代理返回的 403/空体污染断言。
        $handler.UseProxy = $false
        $script:httpClient = [System.Net.Http.HttpClient]::new($handler)
    }
    $request = [System.Net.Http.HttpRequestMessage]::new(
        [System.Net.Http.HttpMethod]::new($Method.ToUpperInvariant()), $Url)
    $status = 0
    $text = ''
    try {
        if ($null -ne $Headers) {
            foreach ($key in $Headers.Keys) {
                $request.Headers.TryAddWithoutValidation($key, [string]$Headers[$key]) | Out-Null
            }
        }
        if ($null -ne $Body) {
            $json = $Body
            if ($Body -isnot [string]) { $json = $Body | ConvertTo-Json -Depth 8 -Compress }
            # 第 3 个参数是 mediaType（不含参数）：charset 由编码自动补成 charset=utf-8。
            $request.Content = [System.Net.Http.StringContent]::new(
                $json, [System.Text.Encoding]::UTF8, 'application/json')
        }
        $cts = [System.Threading.CancellationTokenSource]::new([TimeSpan]::FromSeconds($TimeoutSec))
        $completion = [System.Net.Http.HttpCompletionOption]::ResponseContentRead
        if ($HeadersOnly) { $completion = [System.Net.Http.HttpCompletionOption]::ResponseHeadersRead }
        try {
            $resp = $script:httpClient.SendAsync($request, $completion, $cts.Token).GetAwaiter().GetResult()
            try {
                $status = [int]$resp.StatusCode
                if (-not $HeadersOnly) { $text = $resp.Content.ReadAsStringAsync().GetAwaiter().GetResult() }
            } finally { $resp.Dispose() }
        } finally { $cts.Dispose() }
    } finally { $request.Dispose() }
    $json = $null
    if (-not [string]::IsNullOrWhiteSpace($text)) {
        try { $json = $text | ConvertFrom-Json } catch { $json = $null }
    }
    return [pscustomobject]@{ StatusCode = $status; Text = $text; Json = $json }
}

function Get-FreeTcpPort {
    $listener = [System.Net.Sockets.TcpListener]::new([System.Net.IPAddress]::Loopback, 0)
    $listener.Start()
    try { return ([System.Net.IPEndPoint]$listener.LocalEndpoint).Port }
    finally { $listener.Stop() }
}

function Test-TcpPortFree {
    param([int]$TargetPort)
    try {
        $listener = [System.Net.Sockets.TcpListener]::new([System.Net.IPAddress]::Loopback, $TargetPort)
        $listener.Start()
        $listener.Stop()
        return $true
    } catch {
        return $false
    }
}

# Get-LanIPv4：挑一个可用于"非回环请求"的本机 IPv4（过滤回环/链路本地/APIPA）。
function Get-LanIPv4 {
    $found = New-Object System.Collections.Generic.List[string]
    try {
        $hostName = [System.Net.Dns]::GetHostName()
        foreach ($addr in [System.Net.Dns]::GetHostAddresses($hostName)) {
            if ($addr.AddressFamily -ne [System.Net.Sockets.AddressFamily]::InterNetwork) { continue }
            $ip = $addr.ToString()
            if ([System.Net.IPAddress]::IsLoopback($addr)) { continue }
            if ($ip.StartsWith('169.254.')) { continue }
            $found.Add($ip)
        }
    } catch { }
    if ($found.Count -eq 0) {
        try {
            $addrs = Get-NetIPAddress -AddressFamily IPv4 -ErrorAction SilentlyContinue |
                Where-Object { $_.IPAddress -ne '127.0.0.1' -and -not $_.IPAddress.StartsWith('169.254.') -and $_.AddressState -eq 'Preferred' }
            foreach ($a in $addrs) { $found.Add([string]$a.IPAddress) }
        } catch { }
    }
    if ($found.Count -eq 0) { return $null }
    return $found[0]
}

$proc = $null
$port = $Port
$lanIp = $LanIp
$exitedGracefully = $false
$prevMeshDir = $env:AICLI_MESH_DIR
$evidence = [ordered]@{}

try {
    # ------------------------------------------------------------------
    # 前置约束：本场景依赖回环可达
    # ------------------------------------------------------------------
    # 回环豁免断言与 /exit 收尾都走 127.0.0.1，因此要求服务器绑定**通配地址**
    # （0.0.0.0 / ::）。startPprofServer 用 net.Listen 精确绑定给定 host
    # （backend/cmd/aicli/pprof.go），只绑单个网卡 IP 时回环不可达——该形态不在
    # 本场景覆盖范围内（见 docs/e2e/nonloopback-auth-e2e.md §1.1）。这里显式
    # 拒绝而不是让回环断言无谓变红。
    if ($ListenHost.Trim() -notin @('0.0.0.0', '::', '[::]', '*')) {
        throw ("-ListenHost 必须是通配地址（0.0.0.0 / ::）：本场景依赖回环可达" +
               "（回环豁免断言与 /exit 收尾）；只绑单个网卡 IP（如 192.168.x.x）时" +
               "回环不可达，属未覆盖形态。")
    }

    # ------------------------------------------------------------------
    # 0. 构建 / 校验可执行文件
    # ------------------------------------------------------------------
    if ([string]::IsNullOrWhiteSpace($ExePath)) {
        $ExePath = Join-Path $backend '.tmp/aicli-debug-e2e.exe'
    } elseif (-not [System.IO.Path]::IsPathRooted($ExePath)) {
        $ExePath = Join-Path $repoRoot $ExePath
    }

    if (-not $SkipBuild) {
        $buildLogPath = Join-Path $ArtifactDir 'go-build.log'
        $buildTime = (Get-Date).ToUniversalTime().ToString('yyyy-MM-ddTHH:mm:ssZ')
        Write-Log "build: go build -trimpath -ldflags ... -o $ExePath ./cmd/aicli (workdir=backend)"
        Push-Location $backend
        try {
            & go build -trimpath -ldflags "-X main.version=e2e -X main.buildTime=$buildTime" -o $ExePath ./cmd/aicli 2>&1 |
                Tee-Object -FilePath $buildLogPath | Out-Null
            $buildExit = $LASTEXITCODE
        } finally {
            Pop-Location
        }
        Add-Result 'build/go-build' ($buildExit -eq 0) "exit=$buildExit log=$buildLogPath"
        if ($buildExit -ne 0) { throw "go build 失败（exit=$buildExit）" }
    } elseif (-not (Test-Path -LiteralPath $ExePath)) {
        throw "SkipBuild 指定但可执行文件不存在：$ExePath"
    }

    if (-not (Test-Path -LiteralPath $ExePath)) { throw "可执行文件不存在：$ExePath" }
    Write-Log "exe: $ExePath ($((Get-Item -LiteralPath $ExePath).Length) bytes)"

    # ------------------------------------------------------------------
    # 1. 独立进程启动（非回环监听 + 显式固定令牌）
    # ------------------------------------------------------------------
    if ($port -eq 0) { $port = Get-FreeTcpPort }
    if ([string]::IsNullOrWhiteSpace($lanIp)) { $lanIp = Get-LanIPv4 }

    $launchArgs = @('chat', '--yolo', '--web-host', $ListenHost, '--web-port', "$port", '--web-token', $WebToken)
    # 子进程继承环境变量：网格根隔离（见 $meshDir 注释），脚本收尾恢复原值。
    $env:AICLI_MESH_DIR = $meshDir
    Write-Log "launch: $ExePath $($launchArgs -join ' ') (cwd=$repoRoot)"
    $proc = Start-Process -FilePath $ExePath -ArgumentList $launchArgs -WorkingDirectory $repoRoot -PassThru `
        -RedirectStandardOutput $stdoutPath -RedirectStandardError $stderrPath
    Write-Log "pid=$($proc.Id) lan_ip=$lanIp"

    $loopbackBase = "http://127.0.0.1:$port"
    $endpointsUrl = "$loopbackBase/debug/endpoints"
    $deadline = (Get-Date).AddSeconds($StartupTimeoutSec)
    $snapshot = $null
    while ((Get-Date) -lt $deadline) {
        if ($proc.HasExited) { break }
        try {
            $candidate = Invoke-AuthProbe -Method GET -Url $endpointsUrl -TimeoutSec 5
            if ($candidate.Json.available) { $snapshot = $candidate; break }
        } catch {
            $snapshot = $null
        }
        Start-Sleep -Milliseconds 500
    }

    if ($null -eq $snapshot) {
        $tail = ''
        if (Test-Path -LiteralPath $stderrPath) {
            $tail = (Get-Content -LiteralPath $stderrPath -Tail 20 -ErrorAction SilentlyContinue) -join "`n"
        }
        Add-Result 'startup/endpoints-ready' $false "在 ${StartupTimeoutSec}s 内未就绪；exe_exited=$($proc.HasExited)；stderr tail:`n$tail"
        throw '启动失败：/debug/endpoints 未就绪'
    }
    Add-Result 'startup/endpoints-ready' $true "available=true port=$port uptime_sec=$($snapshot.Json.uptime_sec) version=$($snapshot.Json.version)"
    $evidence['endpoints'] = $snapshot.Json

    # ------------------------------------------------------------------
    # 2. 清单自描述：非回环模式 + 令牌不外泄 + 启动行回显
    # ------------------------------------------------------------------
    Add-Result 'discovery/listen-mode' ($snapshot.Json.listen_mode -eq 'non-loopback') "listen_mode=$($snapshot.Json.listen_mode)"
    $authHint = [string]$snapshot.Json.write_auth_hint
    Add-Result 'discovery/write-auth-hint-all' ($authHint -like '*ALL 请求*') "write_auth_hint=$authHint"
    $webBase = [string]$snapshot.Json.web_base_url
    Add-Result 'discovery/web-base-url' ((-not [string]::IsNullOrWhiteSpace($webBase)) -and ($webBase -like "*:$port*")) "web_base_url=$webBase"

    $textCatalog = Invoke-AuthProbe -Method GET -Url "${endpointsUrl}?format=text" -TimeoutSec 10
    $leaked = ($snapshot.Text -like "*$WebToken*") -or ($textCatalog.Text -like "*$WebToken*")
    Add-Result 'discovery/token-not-leaked' (-not $leaked) 'JSON 与 ?format=text 均不含令牌原文'

    # 非回环模式下 ?format=text 会列出局域网访问地址模板：必须是 <token> 占位符
    # （真实令牌只走启动行 / 回环 GET /web/api/token），这里做正向锁定——
    # 只测"不含令牌"会放过"整段 LAN 区块被删掉"的退化实现。
    if ($snapshot.Json.listen_mode -ne 'non-loopback') {
        Add-Skip 'discovery/lan-url-placeholder' '回环模式无 LAN 访问区块'
    } elseif (-not $textCatalog.Text.Contains('LAN access')) {
        Add-Skip 'discovery/lan-url-placeholder' '本机无非回环地址（服务端 LAN 列表为空）'
    } else {
        $placeholderOk = $textCatalog.Text.Contains('token=<token>')
        Add-Result 'discovery/lan-url-placeholder' $placeholderOk `
            "text 含 token=<token> 占位符=$placeholderOk"
        $placeholderLine = [string](($textCatalog.Text -split "`n" |
            Where-Object { $_ -like '*token=<token>*' } | Select-Object -First 1))
        $evidence['lanUrlPlaceholder'] = [ordered]@{
            ok   = $placeholderOk
            line = $placeholderLine.Trim()
        }
    }

    $tokenFromLine = $null
    if (Test-Path -LiteralPath $stderrPath) {
        $m = Select-String -LiteralPath $stderrPath -Pattern 'X-AICLI-Token' | Select-Object -First 1
        if ($null -ne $m) {
            $line = [string]$m.Line
            $idx = $line.IndexOf('):')
            if ($idx -ge 0) {
                $rest = $line.Substring($idx + 2).Trim()
                $parts = $rest -split ' ' | Where-Object { -not [string]::IsNullOrWhiteSpace($_) }
                if ($parts.Count -gt 0) { $tokenFromLine = [string]$parts[0] }
            }
        }
    }
    Add-Result 'discovery/startup-line-token' ($tokenFromLine -eq $WebToken) `
        "启动行令牌与 --web-token 一致=$($tokenFromLine -eq $WebToken)（line_token_len=$($tokenFromLine.Length)）"

    # ------------------------------------------------------------------
    # 3. 非回环请求：令牌是唯一凭据（路径取自清单，Host 换成局域网 IP）
    # ------------------------------------------------------------------
    $screenPath = $null
    $ep = $snapshot.Json.endpoints | Where-Object { $_.method -eq 'GET' -and $_.path -eq '/web/api/screen' } | Select-Object -First 1
    if ($null -ne $ep) { $screenPath = [string]$ep.path }
    $inputPath = $null
    $epIn = $snapshot.Json.endpoints | Where-Object { $_.method -eq 'POST' -and $_.path -eq '/web/api/input' } | Select-Object -First 1
    if ($null -ne $epIn) { $inputPath = [string]$epIn.path }
    $tokenPath = $null
    $epToken = $snapshot.Json.endpoints | Where-Object { $_.method -eq 'GET' -and $_.path -eq '/web/api/token' } | Select-Object -First 1
    if ($null -ne $epToken) { $tokenPath = [string]$epToken.path }
    Add-Result 'discovery/paths-from-catalog' `
        ((-not [string]::IsNullOrWhiteSpace($screenPath)) -and (-not [string]::IsNullOrWhiteSpace($inputPath)) -and (-not [string]::IsNullOrWhiteSpace($tokenPath))) `
        "screen=$screenPath input=$inputPath token=$tokenPath"

    if ([string]::IsNullOrWhiteSpace($lanIp)) {
        Add-Skip 'auth/lan-*' '本机未探测到非回环 IPv4（可用 -LanIp 指定）；LAN 鉴权断言跳过'
    } else {
        $lanBase = "http://${lanIp}:$port"
        $authHeaders = @{ 'X-AICLI-Token' = $WebToken }

        $lanNoToken = Invoke-AuthProbe -Method GET -Url "$lanBase$screenPath" -TimeoutSec 20
        $reasonNoToken = ''
        if ($null -ne $lanNoToken.Json) { $reasonNoToken = [string]$lanNoToken.Json.reason }
        Add-Result 'auth/lan-get-no-token' `
            (($lanNoToken.StatusCode -eq 403) -and ($reasonNoToken -like '*non-loopback*')) `
            "HTTP $($lanNoToken.StatusCode) status=$($lanNoToken.Json.status) reason=$reasonNoToken"
        $evidence['lanGetNoToken'] = [ordered]@{ status = $lanNoToken.StatusCode; body = $lanNoToken.Text }

        $lanHeader = Invoke-AuthProbe -Method GET -Url "$lanBase$screenPath" -Headers $authHeaders -TimeoutSec 20
        # 注意：/web/api/screen 缺省返回 TUI 合成帧（纯文本），不是 JSON；
        # 这里断言"鉴权放行 + 正文非空"，不要按 JSON 字段判读。
        Add-Result 'auth/lan-get-token-header' (($lanHeader.StatusCode -eq 200) -and ($lanHeader.Text.Length -gt 0)) `
            "HTTP $($lanHeader.StatusCode) bytes=$($lanHeader.Text.Length)"

        $lanQuery = Invoke-AuthProbe -Method GET -Url "$lanBase${screenPath}?token=$WebToken" -TimeoutSec 20
        Add-Result 'auth/lan-get-token-query' (($lanQuery.StatusCode -eq 200) -and ($lanQuery.Text.Length -gt 0)) `
            "HTTP $($lanQuery.StatusCode) bytes=$($lanQuery.Text.Length)"

        $lanWrong = Invoke-AuthProbe -Method GET -Url "$lanBase$screenPath" -Headers @{ 'X-AICLI-Token' = 'wrong-token-0123456789' } -TimeoutSec 20
        Add-Result 'auth/lan-get-wrong-token' ($lanWrong.StatusCode -eq 403) "HTTP $($lanWrong.StatusCode)"

        # 页面加载同样鉴权：index.html 不算静态资产（其内容要注入令牌 meta），
        # 非回环模式下必须 ?token= 才能打开；带令牌时页面自注入 meta，浏览器无需手工操作。
        $lanPageNoToken = Invoke-AuthProbe -Method GET -Url "$lanBase/web/" -TimeoutSec 20
        Add-Result 'auth/lan-page-no-token' ($lanPageNoToken.StatusCode -eq 403) `
            "HTTP $($lanPageNoToken.StatusCode) status=$($lanPageNoToken.Json.status)"

        $lanPageToken = Invoke-AuthProbe -Method GET -Url "$lanBase/web/?token=$WebToken" -TimeoutSec 20
        $metaInjected = $lanPageToken.Text.Contains('aicli-web-token')
        Add-Result 'auth/lan-page-token-meta' (($lanPageToken.StatusCode -eq 200) -and $metaInjected) `
            "HTTP $($lanPageToken.StatusCode) bytes=$($lanPageToken.Text.Length) meta=$metaInjected"
        # 证据只留片段，且把真实令牌替换成 <token>：证据文件本身不能成为泄露源。
        $metaSnippet = ''
        if ($metaInjected) {
            $i = $lanPageToken.Text.IndexOf('aicli-web-token')
            $start = [Math]::Max(0, $i - 60)
            $len = [Math]::Min(160, $lanPageToken.Text.Length - $start)
            $metaSnippet = $lanPageToken.Text.Substring($start, $len).Replace($WebToken, '<token>')
        }
        $evidence['pageTokenMeta'] = [ordered]@{ present = $metaInjected; snippet = $metaSnippet }

        # /debug/* 与 /web/api/* 同权：非回环模式下同样要令牌（清单里的 enabled
        # 只表示"处理器已注册"，不代表免鉴权）。
        $lanPprofNoToken = Invoke-AuthProbe -Method GET -Url "$lanBase/debug/pprof/" -TimeoutSec 20
        Add-Result 'auth/lan-debug-no-token' ($lanPprofNoToken.StatusCode -eq 403) `
            "HTTP $($lanPprofNoToken.StatusCode) status=$($lanPprofNoToken.Json.status)"

        $lanPprofToken = Invoke-AuthProbe -Method GET -Url "$lanBase/debug/pprof/" -Headers $authHeaders -TimeoutSec 20
        Add-Result 'auth/lan-debug-token' ($lanPprofToken.StatusCode -eq 200) `
            "HTTP $($lanPprofToken.StatusCode) bytes=$($lanPprofToken.Text.Length)"

        # SSE 不豁免：EventSource 带不了请求头，浏览器页面用 ?token= 拼接。
        # 只读响应头（HeadersOnly）即可判 403；万一护栏失效也不会把长连接读成超时异常。
        $lanSseNoToken = Invoke-AuthProbe -Method GET -Url "$lanBase/web/api/events" -HeadersOnly -TimeoutSec 20
        Add-Result 'auth/lan-sse-no-token' ($lanSseNoToken.StatusCode -eq 403) `
            "HTTP $($lanSseNoToken.StatusCode)（预期 403：SSE 同样需要令牌）"

        # 无令牌 POST 必须被拒（用 interrupt：即便护栏失效也不会注入 prompt，只做无害动作）。
        $lanPostNoToken = Invoke-AuthProbe -Method POST -Url "$lanBase$inputPath" -Body @{ type = 'interrupt' } -TimeoutSec 20
        Add-Result 'auth/lan-post-no-token' ($lanPostNoToken.StatusCode -eq 403) `
            "HTTP $($lanPostNoToken.StatusCode) status=$($lanPostNoToken.Json.status)"

        $lanPostToken = Invoke-AuthProbe -Method POST -Url "$lanBase$inputPath" -Body @{ type = 'interrupt' } -Headers $authHeaders -TimeoutSec 20
        Add-Result 'auth/lan-post-token' ($lanPostToken.StatusCode -eq 200) `
            "HTTP $($lanPostToken.StatusCode) status=$($lanPostToken.Json.status)"

        # /web/api/token 是令牌读取面，同样属于 /web/api/*：非回环模式下必须带令牌，
        # 带令牌时回显 header/query_param，且 token 与 --web-token 入参一致。
        # 证据/明细里不写令牌原文，只留匹配布尔值。
        $lanTokenNoToken = Invoke-AuthProbe -Method GET -Url "$lanBase$tokenPath" -TimeoutSec 20
        $lanTokenReason = ''
        if ($null -ne $lanTokenNoToken.Json) { $lanTokenReason = [string]$lanTokenNoToken.Json.reason }
        Add-Result 'auth/lan-token-no-token' `
            (($lanTokenNoToken.StatusCode -eq 403) -and ($lanTokenReason -like '*non-loopback*')) `
            "HTTP $($lanTokenNoToken.StatusCode) status=$($lanTokenNoToken.Json.status) reason=$lanTokenReason"

        $lanTokenProbe = Invoke-AuthProbe -Method GET -Url "$lanBase$tokenPath" -Headers $authHeaders -TimeoutSec 20
        $tokenEchoOk = $false
        if ($null -ne $lanTokenProbe.Json) {
            $tokenEchoOk = ([string]$lanTokenProbe.Json.token -eq $WebToken)
            $tokenEchoOk = $tokenEchoOk -and ([string]$lanTokenProbe.Json.header -eq 'X-AICLI-Token')
            $tokenEchoOk = $tokenEchoOk -and ([string]$lanTokenProbe.Json.query_param -eq 'token')
            $tokenEchoOk = $tokenEchoOk -and ([string]$lanTokenProbe.Json.source -eq '--web-token')
        }
        Add-Result 'auth/lan-token-echo' (($lanTokenProbe.StatusCode -eq 200) -and $tokenEchoOk) `
            "HTTP $($lanTokenProbe.StatusCode) header=$($lanTokenProbe.Json.header) query_param=$($lanTokenProbe.Json.query_param) source=$($lanTokenProbe.Json.source) token_match=$tokenEchoOk"
        # 证据只留比对结论（含 token_match 布尔值），不写令牌原文。
        $evidence['lanTokenEcho'] = [ordered]@{
            status      = $lanTokenProbe.StatusCode
            header      = [string]$lanTokenProbe.Json.header
            query_param = [string]$lanTokenProbe.Json.query_param
            source      = [string]$lanTokenProbe.Json.source
            token_match = $tokenEchoOk
        }
    }

    # 豁免 1：回环 IP 发起的请求始终免令牌（本地浏览器访问场景）。
    $loopbackExempt = Invoke-AuthProbe -Method GET -Url "$loopbackBase$screenPath" -TimeoutSec 20
    Add-Result 'auth/loopback-exempt' (($loopbackExempt.StatusCode -eq 200) -and ($loopbackExempt.Text.Length -gt 0)) `
        "HTTP $($loopbackExempt.StatusCode) bytes=$($loopbackExempt.Text.Length)"

    # 回环豁免覆盖写操作：不带令牌 POST /web/api/input 也应被接受（interrupt 幂等无害）。
    # 注意副作用：interrupt 会让会话短暂进入清理态，收尾 /exit 因此需要重试（见 §4 收尾）。
    $loopbackWriteExempt = Invoke-AuthProbe -Method POST -Url "$loopbackBase$inputPath" -Body @{ type = 'interrupt' } -TimeoutSec 20
    Add-Result 'auth/loopback-write-exempt' ($loopbackWriteExempt.StatusCode -eq 200) `
        "HTTP $($loopbackWriteExempt.StatusCode) status=$($loopbackWriteExempt.Json.status)"

    # 豁免 1 的读取面补口：/web/api/token 是令牌自举入口，回环（不带令牌）也应 200；
    # 与之相对，非回环必须带令牌（auth/lan-token-no-token）。
    $loopbackTokenExempt = Invoke-AuthProbe -Method GET -Url "$loopbackBase$tokenPath" -TimeoutSec 20
    Add-Result 'auth/loopback-token-exempt' `
        (($loopbackTokenExempt.StatusCode -eq 200) -and ([string]$loopbackTokenExempt.Json.status -eq 'ok')) `
        "HTTP $($loopbackTokenExempt.StatusCode) status=$($loopbackTokenExempt.Json.status) source=$($loopbackTokenExempt.Json.source)"

    # 豁免 2：/web/** 静态资产（非 /web/api/*）GET 免令牌（浏览器 <link>/<script> 带不了令牌）。
    if ([string]::IsNullOrWhiteSpace($lanIp)) {
        Add-Skip 'auth/static-asset-exempt' '无非回环 IPv4，无法区分"静态豁免"与"回环豁免"'
    } else {
        $assetProbe = Invoke-AuthProbe -Method GET -Url "http://${lanIp}:$port/web/style.css" -TimeoutSec 20
        Add-Result 'auth/static-asset-exempt' ($assetProbe.StatusCode -eq 200) `
            "HTTP $($assetProbe.StatusCode) bytes=$($assetProbe.Text.Length)"
    }

    # ------------------------------------------------------------------
    # 4. 收尾：带令牌 /exit → 优雅退出 + 端口释放
    # ------------------------------------------------------------------
    if ($KeepAlive) {
        Add-Result 'exit/keep-alive' $false 'KeepAlive 指定：未执行 /exit 收尾（人工排查模式，不作为通过）'
    } else {
        # 收尾走回环 + 令牌：两种模式下都成立，避免因 LAN 不可达卡住收尾。
        # 有界重试：前面的 interrupt 探针会让会话进入"清理中"状态，此刻命令门会以
        # {"status":"rejected"} 拒绝 /exit（不是鉴权问题，也不代表退出失败）。
        # 重试到 queued 为止，并把尝试次数与最后一次响应写进证据，避免把
        # "会话尚在收尾"误判成"退出接口不可用"。
        $exitProbe = $null
        $exitAttempts = 0
        $exitDeadline = (Get-Date).AddSeconds($ExitTimeoutSec)
        while ((Get-Date) -lt $exitDeadline) {
            if ($proc.HasExited) { break }
            $exitAttempts++
            $exitProbe = Invoke-AuthProbe -Method POST -Url "$loopbackBase$inputPath" -Body @{ prompt = '/exit' } `
                -Headers @{ 'X-AICLI-Token' = $WebToken } -TimeoutSec 30
            if ($exitProbe.StatusCode -eq 200 -and $exitProbe.Json.status -eq 'queued') { break }
            Write-Log ("exit: 第 {0} 次 /exit 未入队（HTTP {1} status={2} reason={3}），等待会话就绪后重试" -f `
                $exitAttempts, $exitProbe.StatusCode, $exitProbe.Json.status, $exitProbe.Json.reason)
            Start-Sleep -Milliseconds 500
        }
        $evidence['exit'] = [ordered]@{
            attempts = $exitAttempts
            status   = $exitProbe.StatusCode
            body     = $exitProbe.Text
        }
        Add-Result 'exit/queued' (($exitProbe.StatusCode -eq 200) -and ($exitProbe.Json.status -eq 'queued')) `
            "HTTP $($exitProbe.StatusCode) status=$($exitProbe.Json.status) attempts=$exitAttempts"

        $exitStart = Get-Date
        $deadline = $exitStart.AddSeconds($ExitTimeoutSec)
        while ((Get-Date) -lt $deadline -and -not $proc.HasExited) { Start-Sleep -Milliseconds 500 }
        $proc.Refresh()
        if ($proc.HasExited) {
            $exitedGracefully = ($proc.ExitCode -eq 0)
            $waited = [int]((Get-Date) - $exitStart).TotalSeconds
            Add-Result 'exit/graceful' $exitedGracefully "exit_code=$($proc.ExitCode) waited=${waited}s"
        } else {
            Add-Result 'exit/graceful' $false "在 ${ExitTimeoutSec}s 内未退出"
        }
        Start-Sleep -Milliseconds 300
        $portFree = Test-TcpPortFree -TargetPort $port
        Add-Result 'exit/port-released' $portFree "port=$port free=$portFree"
    }
} finally {
    if ($null -eq $prevMeshDir) { Remove-Item Env:AICLI_MESH_DIR -ErrorAction SilentlyContinue } else { $env:AICLI_MESH_DIR = $prevMeshDir }
    # 收尾兜底：异常/KeepAlive 路径下不留下孤儿进程。
    if ($null -ne $proc) {
        try { $proc.Refresh() } catch { }
        if (-not $proc.HasExited) {
            Write-Log "cleanup: 强制结束 pid=$($proc.Id)（未能优雅退出）"
            try { Stop-Process -Id $proc.Id -Force -ErrorAction SilentlyContinue } catch { }
        }
    }
}

# ------------------------------------------------------------------
# 5. 总结
# ------------------------------------------------------------------
$passCount = @($script:results | Where-Object { $_.passed }).Count
$failCount = @($script:results | Where-Object { -not $_.passed }).Count
$skipCount = $script:skips.Count

$summary = [ordered]@{
    scenario     = 'E2E-DEBUG-02 (non-loopback auth)'
    started_at   = (Get-Date).ToUniversalTime().ToString('yyyy-MM-ddTHH:mm:ssZ')
    port         = $port
    lan_ip       = $lanIp
    listen_host  = $ListenHost
    exe_path     = $ExePath
    pass         = $passCount
    fail         = $failCount
    skip         = $skipCount
    results      = $script:results
    skipped      = $script:skips
    evidence     = $evidence
}
$summary | ConvertTo-Json -Depth 8 | Set-Content -LiteralPath $summaryPath -Encoding UTF8

Write-Log ("总结: PASS={0} FAIL={1} SKIP={2}；证据目录 {3}" -f $passCount, $failCount, $skipCount, $ArtifactDir)
foreach ($r in $script:results) {
    if (-not $r.passed) { Write-Log ("FAIL 明细: {0} :: {1}" -f $r.name, $r.detail) }
}
foreach ($s in $script:skips) {
    Write-Log ("SKIP 明细: {0} :: {1}" -f $s.name, $s.reason)
}

if ($failCount -gt 0) { exit 1 }
exit 0
