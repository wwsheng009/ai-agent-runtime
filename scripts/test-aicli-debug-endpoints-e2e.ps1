<#
.SYNOPSIS
  aicli 调试端点 E2E：独立进程启动 → /debug/endpoints 发现 → 读屏 → 同步 invoke → 幂等回放 → turn 后验 → /exit 收尾。

.DESCRIPTION
  验收目标（全部通过才退出码 0）：

    1. 以**独立进程**启动 `aicli chat --yolo --web-port <port>`，仅凭
       `GET /debug/endpoints` 的清单发现后续入口（URL 一律取自清单字段，
       不在脚本里另拼语义）；
    2. 清单自描述：available / listen_mode / web_base_url / write_auth_header 正确，
       且 JSON 与 ?format=text **都不得**出现写令牌原文；
    3. 读屏：`/debug/chat/screen` 与 `/web/api/screen?view=tui` 均可用；
    4. 同步调用：`POST /web/api/invoke` 注入 prompt → status=completed、
       assistant 非空、llm_observed=true、usage.total_tokens>0、screen 可用；
    5. 幂等：同 client_request_id 重放 → duplicate=true、assistant 与 elapsed_ms
       与首次一致、turn 记录数不增加（不重复注入）；
    6. 后验：`/web/api/turn?id=<turn_id>` found=true，且该记录 status=completed、
       duration_ms>0、steps>=1、assistant_preview 非空；
    7. 收尾：`POST /web/api/input {"prompt":"/exit"}` → 进程优雅退出、退出码 0、端口释放。
    8. 网格只读控制面（S5）：清单的 `mesh` 分组含 `/web/api/health`、
       `/web/api/mesh/self`、`/web/api/mesh/peers`；self 自描述网格根（本脚本把
       `AICLI_MESH_DIR` 隔离到 artifacts 子目录，不碰真实 `~/.aicli/mesh`）；
       peers 默认全量口径（counts 不被过滤影响）且令牌默认脱敏（M7），只有
       回环 + `?reveal_token=1` 才回原文。多进程语义见 E2E-DEBUG-03。

   不覆盖（由其他 harness 承担）：真实终端渲染与键盘输入路径，见
  `scripts/test-aicli-windows-terminal-e2e.ps1` 与
  `scripts/test-aicli-opencode-windows-terminal-e2e.ps1`。本脚本只走 HTTP 控制面。

  前置条件：本机已配置可用 provider/model（否则第 4 步会如实失败，不会伪造通过）；
  无人值守环境建议追加 `-Headless`（等价 `--headless`：跳过交互选择器，
  无可用 provider 时直接报错退出，TUI 与调试端点照常）。

.PARAMETER ExePath
  aicli 可执行文件路径；缺省 <repo>/backend/.tmp/aicli-debug-e2e.exe。
.PARAMETER Port
  监听端口；缺省 0 = 自动挑选空闲端口（建议 CI 用 0，避免与本机实例冲突）。
.PARAMETER Prompt
  invoke 注入的 prompt；缺省「只回复两个字：收到」。
.PARAMETER InvokeTimeoutMs
  invoke 等待 turn 结束的超时（毫秒），缺省 120000。
.PARAMETER StartupTimeoutSec
  等待 /debug/endpoints 就绪的超时（秒），缺省 90。
.PARAMETER ExitTimeoutSec
  等待 /exit 生效的超时（秒），缺省 60。
.PARAMETER SkipBuild
  跳过 go build，直接使用 ExePath（要求文件已存在）。
.PARAMETER Headless
  启动参数追加 --headless（无人值守；不改变端点与调用契约）。
.PARAMETER KeepAlive
  不发送 /exit（人工排查用）；脚本收尾强制结束进程并记为 FAIL，避免误当通过。
.PARAMETER KeepAliveOnFailSec
  失败时保留进程的秒数（缺省 0 = 立即清理）。>0 时失败现场保持存活，便于
  人工连上端口复现；同时自动抓取诊断包（diag/）与双通道屏幕取证。
.PARAMETER NoTimeline
  关闭后台时序采样（缺省开启：timeline.jsonl 每 500ms 一帧）。
.PARAMETER TimelineIntervalMs
  时序采样间隔（毫秒），缺省 500。
.PARAMETER LatencyBudgetMs
  `/debug/chat/status` 全量响应的延迟预算（毫秒），缺省 5000。
  饱和主机上排队延迟会整体抬高（实测空载 3~7ms、满载 2.9~11.9s），
  阈值用于区分「端点回归」与「机器正在跑别的重负载」。
.PARAMETER ArtifactDir
  证据目录；缺省 artifacts/aicli-debug-endpoints-e2e/<yyyyMMdd-HHmmss>。

.EXAMPLE
  pwsh -NoProfile -File scripts/test-aicli-debug-endpoints-e2e.ps1

.EXAMPLE
  pwsh -NoProfile -File scripts/test-aicli-debug-endpoints-e2e.ps1 -Port 9999 -SkipBuild -Headless
#>
[CmdletBinding()]
param(
    [string]$ExePath,
    [ValidateRange(0, 65535)][int]$Port = 0,
    [string]$Prompt = '只回复两个字：收到',
    [ValidateRange(1000, 3600000)][int]$InvokeTimeoutMs = 120000,
    [ValidateRange(5, 600)][int]$StartupTimeoutSec = 90,
    [ValidateRange(5, 600)][int]$ExitTimeoutSec = 60,
    [switch]$SkipBuild,
    [switch]$Headless,
    [switch]$KeepAlive,
    [ValidateRange(0, 3600)][int]$KeepAliveOnFailSec = 0,
    [switch]$NoTimeline,
    [ValidateRange(100, 60000)][int]$TimelineIntervalMs = 500,
    [ValidateRange(50, 600000)][int]$LatencyBudgetMs = 5000,
    [string]$ArtifactDir
)

$ErrorActionPreference = 'Stop'

$repoRoot = Split-Path -Parent $PSScriptRoot
$backend = Join-Path $repoRoot 'backend'

if ([string]::IsNullOrWhiteSpace($ArtifactDir)) {
    $stamp = Get-Date -Format 'yyyyMMdd-HHmmss'
    $ArtifactDir = Join-Path $repoRoot "artifacts/aicli-debug-endpoints-e2e/$stamp"
} elseif (-not [System.IO.Path]::IsPathRooted($ArtifactDir)) {
    $ArtifactDir = Join-Path $repoRoot $ArtifactDir
}
New-Item -ItemType Directory -Path $ArtifactDir -Force | Out-Null

$runLogPath = Join-Path $ArtifactDir 'run.log'
$stdoutPath = Join-Path $ArtifactDir 'aicli.stdout.log'
$stderrPath = Join-Path $ArtifactDir 'aicli.stderr.log'
$summaryPath = Join-Path $ArtifactDir 'summary.json'
$timelinePath = Join-Path $ArtifactDir 'timeline.jsonl'
$diagDir = Join-Path $ArtifactDir 'diag'
# 网格根隔离（施工纪律 A6）：本场景只驱动一个进程，把 AICLI_MESH_DIR 指到
# artifacts 子目录——断言口径确定（counts.live 恒为 1），也不污染真实 ~/.aicli/mesh。
$meshDir = Join-Path $ArtifactDir 'mesh'
New-Item -ItemType Directory -Path $meshDir -Force | Out-Null

# E2E 观测工具集（A1 时序采样 / A2 诊断包 / A3 稳态判据 / B4 清单覆盖 / C4 UIA）。
. (Join-Path $PSScriptRoot 'aicli-e2e-harness.ps1')

$script:baseUrl = $null
$script:timelineJob = $null
$script:consoleTitle = ''
$script:diagCaptured = $false

function Write-Log {
    param([string]$Message)
    $line = '[{0}] {1}' -f (Get-Date -Format 'HH:mm:ss'), $Message
    Write-Host $line
    Add-Content -LiteralPath $runLogPath -Value $line -Encoding UTF8
}

$script:results = New-Object System.Collections.Generic.List[object]

function Add-Result {
    param([string]$Name, [bool]$Passed, [string]$Detail)
    $script:results.Add([pscustomobject]@{ name = $Name; passed = $Passed; detail = $Detail })
    $tag = 'FAIL'
    if ($Passed) { $tag = 'PASS' }
    Write-Log ("[{0}] {1} :: {2}" -f $tag, $Name, $Detail)
    if (-not $Passed) { Invoke-FailureForensics -Reason "$Name failed: $Detail" }
}

# Invoke-FailureForensics：A2/C4 取证，幂等（只抓一次）。
# 首个 FAIL 就**就地**抓取，而不是等到 finally：失败若发生在 /exit 之后，
# 进程已经退出，迟到的取证只会得到一串 connection refused
# （20260924-074517 实测：diag/ 全量采集失败，只剩 diag/reason.txt 有信息）。
function Invoke-FailureForensics {
    param([string]$Reason)
    if ($script:diagCaptured) { return }
    if ([string]::IsNullOrWhiteSpace($script:baseUrl)) { return }
    $script:diagCaptured = $true
    try {
        Save-AicliDiagnostics -BaseUrl $script:baseUrl -DiagDir $diagDir `
            -Reason $Reason -TimelinePath $timelinePath | Out-Null
        # C4：双通道取证——HTTP 的 screen 只是"应然帧"，UIA 读到的才是
        # 用户实际看到的物理帧。两者同时落盘才能定位"谁没显示"。
        Save-AicliDualChannelForensics -BaseUrl $script:baseUrl -DiagDir $diagDir `
            -WindowTitle $script:consoleTitle -Label 'screen-fail' | Out-Null
        Write-Log "diag: 首个 FAIL 就地取证已写入 $diagDir"
    } catch {
        Write-Log ("diag: 采集失败: " + $_.Exception.Message)
    }
}

# Invoke-JsonHttp：统一走 UTF-8 字节收发，避免 Windows PowerShell 控制台代码页
# 把中文响应解码坏（docs/aicli/web-remote-api.md 已提示过该坑）。
function Invoke-JsonHttp {
    param(
        [Parameter(Mandatory)][string]$Method,
        [Parameter(Mandatory)][string]$Url,
        $Body,
        [int]$TimeoutSec = 30
    )
    $params = @{ Method = $Method; Uri = $Url; TimeoutSec = $TimeoutSec; UseBasicParsing = $true }
    if ($null -ne $Body) {
        $json = $Body
        if ($Body -isnot [string]) { $json = $Body | ConvertTo-Json -Depth 8 -Compress }
        $params.Body = [System.Text.Encoding]::UTF8.GetBytes($json)
        $params.ContentType = 'application/json; charset=utf-8'
    }
    $resp = Invoke-WebRequest @params
    $text = $null
    try {
        $stream = $resp.RawContentStream
        if ($null -ne $stream) { $text = [System.Text.Encoding]::UTF8.GetString($stream.ToArray()) }
    } catch {
        $text = $null
    }
    if ([string]::IsNullOrEmpty($text)) { $text = [string]$resp.Content }
    # ?format=text 之类的纯文本端点不是 JSON：解析失败时 Json 置空，Text 照常保留。
    $json = $null
    try { $json = $text | ConvertFrom-Json } catch { $json = $null }
    return [pscustomobject]@{
        StatusCode = [int]$resp.StatusCode
        Text       = $text
        Json       = $json
    }
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

# Get-DiscoveredEndpointUrl：URL 只从 /debug/endpoints 清单取，不在脚本内另拼。
function Get-DiscoveredEndpointUrl {
    param($Snapshot, [string]$Method, [string]$Path)
    $ep = $Snapshot.endpoints | Where-Object { $_.method -eq $Method -and $_.path -eq $Path } | Select-Object -First 1
    if ($null -ne $ep -and -not [string]::IsNullOrWhiteSpace($ep.url)) { return [string]$ep.url }
    return $null
}

function Get-EndpointEnabled {
    param($Snapshot, [string]$Method, [string]$Path)
    $ep = $Snapshot.endpoints | Where-Object { $_.method -eq $Method -and $_.path -eq $Path } | Select-Object -First 1
    if ($null -eq $ep) { return $false }
    return [bool]$ep.enabled
}

$proc = $null
$port = $Port
$ready = $false
$invokeResult = $null
$replayResult = $null
$turnBeforeCount = -1
$turnAfterCount = -1
$exitedGracefully = $false
$prevMeshDir = $env:AICLI_MESH_DIR

try {
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
        # 预创建：go build 静默成功（无 stdout/stderr）时管道不产生对象，
        # Tee-Object 不会创建文件，证据目录会缺 go-build.log（历史运行均如此）。
        New-Item -ItemType File -Path $buildLogPath -Force | Out-Null
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
    # 1. 独立进程启动（--web-port 显式端口）
    # ------------------------------------------------------------------
    if ($port -eq 0) { $port = Get-FreeTcpPort }
    # --pprof：显式开启 pprof 端点族，使 /debug/pprof/executor 进入断言范围
    # （B4：清单里的端点要么被断言，要么被显式豁免）。
    $launchArgs = @('chat', '--yolo', '--pprof', '--web-port', "$port")
    if ($Headless) { $launchArgs += '--headless' }
    # 子进程继承环境变量：网格根隔离（见 $meshDir 注释），脚本收尾恢复原值。
    $env:AICLI_MESH_DIR = $meshDir
    Write-Log "launch: $ExePath $($launchArgs -join ' ') (cwd=$repoRoot)"

    $proc = Start-Process -FilePath $ExePath -ArgumentList $launchArgs -WorkingDirectory $repoRoot -PassThru `
        -RedirectStandardOutput $stdoutPath -RedirectStandardError $stderrPath
    Write-Log "pid=$($proc.Id)"

    $endpointsUrl = "http://127.0.0.1:$port/debug/endpoints"
    $deadline = (Get-Date).AddSeconds($StartupTimeoutSec)
    $snapshot = $null
    while ((Get-Date) -lt $deadline) {
        if ($proc.HasExited) { break }
        try {
            $candidate = Invoke-JsonHttp -Method GET -Url $endpointsUrl -TimeoutSec 5
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
    $ready = $true
    $jsonText = $snapshot.Text
    Add-Result 'startup/endpoints-ready' $true "available=true port=$port uptime_sec=$($snapshot.Json.uptime_sec) version=$($snapshot.Json.version)"

    $script:baseUrl = "http://127.0.0.1:$port"
    if (-not $NoTimeline) {
        # A1：后台时序采样（只读 ?fast=1 与屏幕文本，不参与会话调度）。
        # 时间线是"空屏/卡死"类缺陷的现场：固定 sleep 断言失败时，它给出
        # 失败前后的连续帧，而不是一个孤立的终态。
        $script:timelineJob = Start-AicliTimeline -BaseUrl $script:baseUrl -TimelinePath $timelinePath `
            -IntervalMs $TimelineIntervalMs -Tag 'debug-endpoints-01' `
            -HarnessPath (Join-Path $PSScriptRoot 'aicli-e2e-harness.ps1')
        Write-Log "timeline: 后台采样已启动 interval=${TimelineIntervalMs}ms -> $timelinePath"
    }

    # ------------------------------------------------------------------
    # 2. 清单自描述 + 令牌不外泄
    # ------------------------------------------------------------------
    $webBase = [string]$snapshot.Json.web_base_url
    $loopbackBase = [string]$snapshot.Json.loopback_base_url
    Add-Result 'discovery/listen-mode' ($snapshot.Json.listen_mode -eq 'loopback') "listen_mode=$($snapshot.Json.listen_mode)"
    Add-Result 'discovery/web-base-url' ($webBase -eq "http://127.0.0.1:$port/web") "web_base_url=$webBase"
    Add-Result 'discovery/write-auth-header' ($snapshot.Json.write_auth_header -eq 'X-AICLI-Token') "write_auth_header=$($snapshot.Json.write_auth_header)"

    $required = @(
        @{ Method = 'GET';  Path = '/debug/chat/screen'; Name = 'debug-screen' },
        @{ Method = 'GET';  Path = '/web/api/screen';    Name = 'web-screen' },
        @{ Method = 'POST'; Path = '/web/api/invoke';    Name = 'web-invoke' },
        @{ Method = 'GET';  Path = '/web/api/turn';      Name = 'web-turn' },
        @{ Method = 'POST'; Path = '/web/api/input';     Name = 'web-input' }
    )
    foreach ($req in $required) {
        Add-Result "discovery/enabled:$($req.Name)" (Get-EndpointEnabled -Snapshot $snapshot.Json -Method $req.Method -Path $req.Path) `
            "$($req.Method) $($req.Path) enabled=$((Get-EndpointEnabled -Snapshot $snapshot.Json -Method $req.Method -Path $req.Path))"
    }

    # 注意：PowerShell 变量名允许 '?'，必须写 "${var}?query" 才能正确拼查询串
    $textCatalog = Invoke-JsonHttp -Method GET -Url "${endpointsUrl}?format=text" -TimeoutSec 10
    Add-Result 'discovery/text-guide-block' `
        (($textCatalog.Text -like '*Debug 使用说明*') -and ($textCatalog.Text -like '*POST /web/api/invoke*')) `
        'text 清单含「Debug 使用说明」与 invoke 驱动行'

    $tokenPlain = $null
    try { $tokenPlain = [string](Invoke-JsonHttp -Method GET -Url "$webBase/api/token" -TimeoutSec 10).Json.token } catch { $tokenPlain = $null }
    if ([string]::IsNullOrWhiteSpace($tokenPlain) -and (Test-Path -LiteralPath $stderrPath)) {
        $m = Select-String -LiteralPath $stderrPath -Pattern 'X-AICLI-Token\):\s*([A-Za-z0-9\-\._~]+)' | Select-Object -First 1
        if ($null -ne $m) { $tokenPlain = $m.Matches[0].Groups[1].Value }
    }
    if ([string]::IsNullOrWhiteSpace($tokenPlain)) {
        Add-Result 'discovery/token-not-leaked' $false '未能取得写令牌原文（GET /web/api/token 与启动行均失败），无法完成反证'
    } else {
        $leaked = ($jsonText -like "*$tokenPlain*") -or ($textCatalog.Text -like "*$tokenPlain*")
        Add-Result 'discovery/token-not-leaked' (-not $leaked) 'JSON 与 ?format=text 均不含令牌原文'
    }

    # ------------------------------------------------------------------
    # 3. 读屏（URL 取自清单）
    # ------------------------------------------------------------------
    # ------------------------------------------------------------------
    # 2b. 状态端点契约（B1 显式 app_state / B3 有界降级 / B4 清单覆盖）
    # ------------------------------------------------------------------
    $statusUrl = Get-DiscoveredEndpointUrl -Snapshot $snapshot.Json -Method 'GET' -Path '/debug/chat/status'
    if ([string]::IsNullOrWhiteSpace($statusUrl)) { $statusUrl = "$script:baseUrl/debug/chat/status" }
    $statusProbe = Invoke-HarnessRequest -Url $statusUrl -TimeoutSec 30 -AsJson
    $statusDoc = $statusProbe.json
    Add-Result 'debug/status-available' ([bool]$statusDoc.available) `
        ("HTTP {0} ms={1} available={2}" -f $statusProbe.status_code, $statusProbe.ms, $statusDoc.available)

    # B1：app_state 必须显式存在（headless 下 available=false + 非空 reason），
    # 不允许整个区块消失——否则读屏断言无法区分「没有渲染器」与「正常」。
    $appStateExplicit = ($null -ne $statusDoc.app_state) -and ($null -ne $statusDoc.app_state.available)
    $appStateReasonOk = $true
    if ($appStateExplicit -and (-not [bool]$statusDoc.app_state.available)) {
        $appStateReasonOk = -not [string]::IsNullOrWhiteSpace([string]$statusDoc.app_state.reason)
    }
    Add-Result 'debug/status-app-state-explicit' ($appStateExplicit -and $appStateReasonOk) `
        ("app_state.available={0} reason='{1}'" -f $statusDoc.app_state.available, $statusDoc.app_state.reason)

    # B3：?fast=1 必须登记被跳过的重区块，否则「降级」不可观测；默认路径
    # 也要在预算内返回（风暴期实测曾 >3s 拖住轮询方）。agents 与 files/storage
    # 同属重区块：registry 一致性审计要走会话库单连接，实测饱和时单请求阻塞
    # 数秒（2026-09-23 goroutine 现场：请求卡在 buildChatDebugDisplayAgentsInfo）。
    $fastProbe = Invoke-HarnessRequest -Url "${statusUrl}?fast=1" -TimeoutSec 30 -AsJson
    $fastSkipped = @()
    if ($null -ne $fastProbe.json.skipped_sections) { $fastSkipped = @($fastProbe.json.skipped_sections) }
    $fastOk = ([bool]$fastProbe.json.fast) -and ($fastSkipped -contains 'files') -and `
        ($fastSkipped -contains 'storage') -and ($fastSkipped -contains 'agents')
    $planLayoutExpectation = 'n/a(no-renderer)'
    if ([bool]$statusDoc.app_state.available) {
        # plan_layout 只在存在 AppState 时才可能被跳过：没有渲染器时该区块
        # 根本不存在，理由由 app_state.reason 承担，不重复登记。
        $planLayoutExpectation = 'required'
        $fastOk = $fastOk -and ($fastSkipped -contains 'plan_layout')
    }
    Add-Result 'debug/status-fast-bounded' $fastOk `
        ("fast={0} skipped=[{1}] plan_layout={2} ms={3}" -f $fastProbe.json.fast, ($fastSkipped -join ','), $planLayoutExpectation, $fastProbe.ms)
    # 预算可调：饱和主机上排队延迟会整体抬高（实测空载 3~7ms / 满载 2.9~11.9s），
    # 阈值要能区分「端点回归」与「机器在跑别的东西」。
    Add-Result 'debug/status-latency' ([int]$statusProbe.ms -lt $LatencyBudgetMs) `
        ("full={0}ms fast={1}ms" -f $statusProbe.ms, $fastProbe.ms)

    # B4：/debug/pprof/executor 入断言（此前清单里有、断言里没有）。
    $execUrl = Get-DiscoveredEndpointUrl -Snapshot $snapshot.Json -Method 'GET' -Path '/debug/pprof/executor'
    if ([string]::IsNullOrWhiteSpace($execUrl)) { $execUrl = "$script:baseUrl/debug/pprof/executor" }
    $execProbe = Invoke-HarnessRequest -Url $execUrl -TimeoutSec 20 -AsJson
    Add-Result 'debug/pprof-executor' (($execProbe.status_code -eq 200) -and ($null -ne $execProbe.json)) `
        ("HTTP {0} diagnosis='{1}' total_recoveries={2}" -f $execProbe.status_code, $execProbe.json.diagnosis, $execProbe.json.total_recoveries)

    # S5：网格只读控制面入断言（health / self / peers）。三端点都单进程可达，
    # 因此走断言而不是豁免；多进程语义（发现/调用/GC）留给 E2E-DEBUG-03。
    $healthUrl = Get-DiscoveredEndpointUrl -Snapshot $snapshot.Json -Method 'GET' -Path '/web/api/health'
    $meshSelfUrl = Get-DiscoveredEndpointUrl -Snapshot $snapshot.Json -Method 'GET' -Path '/web/api/mesh/self'
    $meshPeersUrl = Get-DiscoveredEndpointUrl -Snapshot $snapshot.Json -Method 'GET' -Path '/web/api/mesh/peers'
    $meshGroup = @($snapshot.Json.endpoints | Where-Object { [string]$_.scheme -eq 'mesh' } | ForEach-Object { [string]$_.path })
    $meshGroupOk = ($meshGroup -contains '/web/api/health') -and ($meshGroup -contains '/web/api/mesh/self') -and `
        ($meshGroup -contains '/web/api/mesh/peers') -and (-not [string]::IsNullOrWhiteSpace($healthUrl)) -and `
        (-not [string]::IsNullOrWhiteSpace($meshSelfUrl)) -and (-not [string]::IsNullOrWhiteSpace($meshPeersUrl))
    Add-Result 'mesh/manifest-group' $meshGroupOk ("scheme=mesh paths=[{0}]" -f ($meshGroup -join ','))

    $health = Invoke-HarnessRequest -Url $healthUrl -TimeoutSec 15 -AsJson
    $healthOk = ($health.status_code -eq 200) -and ([bool]$health.json.available) -and `
        (-not [string]::IsNullOrWhiteSpace([string]$health.json.node_id)) -and ([int]$health.json.pid -gt 0) -and `
        ([bool]$health.json.mesh_ready)
    Add-Result 'mesh/health-shape' $healthOk `
        ("HTTP {0} available={1} pid={2} session_active={3} mesh_ready={4}" -f $health.status_code, $health.json.available, $health.json.pid, $health.json.session_active, $health.json.mesh_ready)

    $meshSelf = Invoke-HarnessRequest -Url $meshSelfUrl -TimeoutSec 15 -AsJson
    $selfRoot = [string]$meshSelf.json.mesh.root
    $selfToken = [string]$meshSelf.json.auth.token
    $selfOk = ($meshSelf.status_code -eq 200) -and ([bool]$meshSelf.json.available) -and `
        (-not [string]::IsNullOrWhiteSpace([string]$meshSelf.json.node_id)) -and ([int]$meshSelf.json.pid -gt 0) -and `
        ([bool]$meshSelf.json.mesh.enabled) -and ($selfRoot.TrimEnd('\', '/') -ieq $meshDir.TrimEnd('\', '/')) -and `
        (-not [string]::IsNullOrWhiteSpace([string]$meshSelf.json.mesh.journal)) -and ($null -ne $meshSelf.json.derived) -and `
        ([string]$meshSelf.json.liveness.state -eq 'live') -and ($selfToken -like '*…')
    Add-Result 'mesh/self-shape' $selfOk `
        ("HTTP {0} available={1} node_id={2} root={3} liveness={4} lease={5} token={6}" -f $meshSelf.status_code, $meshSelf.json.available, $meshSelf.json.node_id, $selfRoot, $meshSelf.json.liveness.state, $meshSelf.json.derived.lease, $selfToken)

    $meshPeers = Invoke-HarnessRequest -Url $meshPeersUrl -TimeoutSec 20 -AsJson
    $peerNodes = @($meshPeers.json.nodes)
    $selfNode = $peerNodes | Where-Object { [string]$_.node_id -eq [string]$meshSelf.json.node_id } | Select-Object -First 1
    $peersOk = ($meshPeers.status_code -eq 200) -and ([int]$meshPeers.json.counts.live -eq 1) -and ($peerNodes.Count -eq 1) -and `
        ($null -ne $selfNode) -and ([string]$selfNode.state -eq 'live') -and ([string]$selfNode.ownership -eq 'owner') -and `
        ([string]$selfNode.reachability -eq 'skipped') -and ([string]$meshPeers.json.filter.scope -eq 'all') -and `
        ([string]$meshPeers.json.filter.state -eq 'all') -and ([string]$meshPeers.json.self.node_id -eq [string]$meshSelf.json.node_id)
    Add-Result 'mesh/peers-shape' $peersOk `
        ("HTTP {0} live={1} nodes={2} state={3} ownership={4} reachability={5} filter={6}/{7}" -f $meshPeers.status_code, $meshPeers.json.counts.live, $peerNodes.Count, $selfNode.state, $selfNode.ownership, $selfNode.reachability, $meshPeers.json.filter.scope, $meshPeers.json.filter.state)

    # M7 反证：默认输出不得出现写令牌原文——self 的 auth.token 只能是 `0f3a…` 提示，
    # peers 的节点 auth 只有 token_hint（没有 token 键）。原文只在显式 reveal 时出现。
    $peerAuth = $null
    if ($null -ne $selfNode) { $peerAuth = $selfNode.auth }
    $peerTokenHint = ''
    if ($null -ne $peerAuth) { $peerTokenHint = [string]$peerAuth.token_hint }
    $tokenKnown = -not [string]::IsNullOrWhiteSpace($tokenPlain)
    $selfLeak = $tokenKnown -and ($meshSelf.text -like "*$tokenPlain*")
    $peersLeak = $tokenKnown -and ($meshPeers.text -like "*$tokenPlain*")
    $redactedOk = $tokenKnown -and (-not $selfLeak) -and (-not $peersLeak) -and ($peerTokenHint -like '*…') -and `
        (($null -eq $peerAuth) -or ($null -eq $peerAuth.token))
    Add-Result 'mesh/token-redacted' $redactedOk `
        ("self_token={0} peer_token_hint={1} leaked={2}" -f $selfToken, $peerTokenHint, ($selfLeak -or $peersLeak))

    $meshReveal = Invoke-HarnessRequest -Url "${meshPeersUrl}?reveal_token=1" -TimeoutSec 20 -AsJson
    $revealNode = @($meshReveal.json.nodes) | Where-Object { [string]$_.node_id -eq [string]$meshSelf.json.node_id } | Select-Object -First 1
    $revealToken = ''
    if ($null -ne $revealNode -and $null -ne $revealNode.auth) { $revealToken = [string]$revealNode.auth.token }
    $revealOk = $tokenKnown -and ($null -ne $revealNode) -and ($revealToken -eq $tokenPlain)
    Add-Result 'mesh/token-reveal-loopback' $revealOk ("reveal_token=1 → token_matches={0}" -f ($revealToken -eq $tokenPlain))

    # B4 门禁：清单里的每个端点都必须被断言覆盖，或被显式豁免（附理由）。
    # 新端点悄悄进入清单而无人断言，就是这道门禁要拦的情况。
    $assertedPaths = @(
        '/debug/endpoints', '/debug/chat/status', '/debug/chat/screen',
        '/web/api/screen', '/web/api/invoke', '/web/api/turn', '/web/api/input',
        '/debug/pprof/executor',
        # S5 网格只读控制面：走断言而不是豁免——三个端点都单进程可达。
        '/web/api/health', '/web/api/mesh/self', '/web/api/mesh/peers'
    )
    $exemptPrefixes = @(
        @{ prefix = '/web/api/config';   reason = '配置面：由 Web 客户端 UI 承担，非本脚本范围' },
        @{ prefix = '/web/api/sessions'; reason = '会话管理面：由 resume 屏幕 E2E（03）承担' },
        @{ prefix = '/web/api/mcps';     reason = 'MCP 管理面：需要外部 MCP 进程' },
        @{ prefix = '/web/api/skills';   reason = '技能目录：静态读取' },
        @{ prefix = '/web/api/analysis'; reason = '用量分析：静态读取' },
        @{ prefix = '/web/api/cache';    reason = 'LLM 缓存分析：静态读取' },
        @{ prefix = '/web/api/events';   reason = 'SSE 事件流：需要持续消费方' },
        @{ prefix = '/web/api/statusbar'; reason = '状态栏快照：非本脚本范围' },
        @{ prefix = '/web/api/runtime';  reason = '运行时元数据：非本脚本范围' },
        @{ prefix = '/web/api/status';   reason = '状态快照：与 /debug/chat/status 同源，已由 debug/status-* 断言覆盖' },
        @{ prefix = '/web/api/token';    reason = '写令牌读取：本脚本以启动行反证令牌不外泄' },
        @{ prefix = '/web/';             reason = 'Web 客户端页面：非本脚本范围' },
        @{ prefix = '/debug/pprof/';     reason = 'pprof 家族：仅 /debug/pprof/executor 入断言，其余按需人工使用' },
        @{ prefix = '/api/runtime/observe'; reason = '观察平面：默认关闭（Observe.Enabled=false）' }
    )
    $exempt = @{}
    $allEndpoints = @()
    if ($null -ne $snapshot.Json.endpoints) { $allEndpoints = @($snapshot.Json.endpoints) }
    foreach ($e in $allEndpoints) {
        $p = [string]$e.path
        if ([string]::IsNullOrWhiteSpace($p)) { continue }
        foreach ($f in $exemptPrefixes) {
            if ($p.StartsWith($f.prefix)) {
                if (-not $exempt.ContainsKey($p)) { $exempt[$p] = $f.reason }
                break
            }
        }
    }
    # executor 走断言而不是豁免（上面 $assertedPaths 必须同时收录，否则
    # 它两边都不占，门禁会把「已断言」误报成「无人断言」——20260924-074517 实测）。
    if ($exempt.ContainsKey('/debug/pprof/executor')) { $exempt.Remove('/debug/pprof/executor') }
    $coverage = Test-AicliEndpointCoverage -EndpointsJson $snapshot.Json -Asserted $assertedPaths -Exempt $exempt
    Add-Result 'coverage/endpoints-asserted' $coverage.ok `
        ("checked={0} asserted={1} exempt={2} uncovered=[{3}]" -f $coverage.checked, $assertedPaths.Count, @($coverage.exempt).Count, (@($coverage.missing) -join ','))

    # ------------------------------------------------------------------
    # 3. 读屏（URL 取自清单）
    # ------------------------------------------------------------------
    $debugScreenUrl = Get-DiscoveredEndpointUrl -Snapshot $snapshot.Json -Method 'GET' -Path '/debug/chat/screen'
    $screenUrl = Get-DiscoveredEndpointUrl -Snapshot $snapshot.Json -Method 'GET' -Path '/web/api/screen'
    $invokeUrl = Get-DiscoveredEndpointUrl -Snapshot $snapshot.Json -Method 'POST' -Path '/web/api/invoke'
    $turnUrl = Get-DiscoveredEndpointUrl -Snapshot $snapshot.Json -Method 'GET' -Path '/web/api/turn'
    $inputUrl = Get-DiscoveredEndpointUrl -Snapshot $snapshot.Json -Method 'POST' -Path '/web/api/input'
    Add-Result 'discovery/urls-from-catalog' `
        ((-not [string]::IsNullOrWhiteSpace($debugScreenUrl)) -and (-not [string]::IsNullOrWhiteSpace($screenUrl)) -and `
            (-not [string]::IsNullOrWhiteSpace($invokeUrl)) -and (-not [string]::IsNullOrWhiteSpace($turnUrl)) -and `
            (-not [string]::IsNullOrWhiteSpace($inputUrl))) `
        "debugScreen=$debugScreenUrl screen=$screenUrl invoke=$invokeUrl turn=$turnUrl input=$inputUrl"

    $debugScreen = Invoke-JsonHttp -Method GET -Url $debugScreenUrl -TimeoutSec 15
    Add-Result 'screen/debug-chat-screen' ([bool]$debugScreen.Json.available) `
        "available=$($debugScreen.Json.available) lines=$(@($debugScreen.Json.lines).Count)"

    # ?view=tui 缺省返回纯文本合成帧；带 format=json 才是结构化快照（available/lines/text）。
    $tui = Invoke-JsonHttp -Method GET -Url "${screenUrl}?view=tui&format=json" -TimeoutSec 15
    $tuiAvailable = $false
    $tuiText = ''
    if ($null -ne $tui.Json) {
        $tuiAvailable = [bool]$tui.Json.available
        $tuiText = [string]$tui.Json.text
        if ([string]::IsNullOrWhiteSpace($tuiText) -and $null -ne $tui.Json.lines) { $tuiText = (@($tui.Json.lines) -join "`n") }
    } else {
        $tuiText = [string]$tui.Text
        $tuiAvailable = -not [string]::IsNullOrWhiteSpace($tuiText)
    }
    Add-Result 'screen/web-screen-tui' ($tuiAvailable -and (-not [string]::IsNullOrWhiteSpace($tuiText))) `
        "available=$tuiAvailable chars=$($tuiText.Length)"

    # ------------------------------------------------------------------
    # 4. 同步 invoke
    # ------------------------------------------------------------------
    $requestId = 'e2e-debug-' + (Get-Date -Format 'yyyyMMdd-HHmmss') + '-1'
    $invokeBody = @{ prompt = $Prompt; timeout_ms = $InvokeTimeoutMs; client_request_id = $requestId }
    Write-Log "invoke: POST $invokeUrl client_request_id=$requestId"
    $invoke = Invoke-JsonHttp -Method POST -Url $invokeUrl -Body $invokeBody -TimeoutSec ([Math]::Ceiling($InvokeTimeoutMs / 1000) + 60)
    $invokeResult = $invoke.Json

    $assistantContent = ''
    if ($null -ne $invoke.Json.assistant) { $assistantContent = [string]$invoke.Json.assistant.content }
    $totalTokens = 0
    if ($null -ne $invoke.Json.usage) { $totalTokens = [int]$invoke.Json.usage.total_tokens }

    Add-Result 'invoke/status-completed' ($invoke.Json.status -eq 'completed') `
        "status=$($invoke.Json.status) reason=$($invoke.Json.reason) elapsed_ms=$($invoke.Json.elapsed_ms)"
    Add-Result 'invoke/assistant-content' (-not [string]::IsNullOrWhiteSpace($assistantContent)) `
        "role=$($invoke.Json.assistant.role) chars=$($assistantContent.Length)"
    Add-Result 'invoke/llm-observed' ([bool]$invoke.Json.llm_observed -and (-not [bool]$invoke.Json.busy) -and ([int]$invoke.Json.pending_inputs -eq 0)) `
        "llm_observed=$($invoke.Json.llm_observed) busy=$($invoke.Json.busy) pending_inputs=$($invoke.Json.pending_inputs)"
    Add-Result 'invoke/usage-tokens' ($totalTokens -gt 0) "total_tokens=$totalTokens"
    Add-Result 'invoke/screen-inline' ([bool]$invoke.Json.screen.available) `
        "screen.available=$($invoke.Json.screen.available) screen.lines=$(@($invoke.Json.screen.lines).Count)"
    # P0 改进后：invoke 终态响应的 turn_id 由观察器从 session_start/session_end
    # 事件回填（actor 收尾后会清空 state.CurrentTurnID），应与 /web/api/turn 的
    # 最近一条 completed 记录一致（见 docs/e2e/debug-guide.md §7.2）。
    $turnProbe = Invoke-JsonHttp -Method GET -Url $turnUrl -TimeoutSec 15
    $latest = @($turnProbe.Json.recent) | Where-Object { $_.status -eq 'completed' } | Select-Object -Last 1
    $preview = ''
    if ($null -ne $latest) { $preview = [string]$latest.assistant_preview }
    $assistantTrimmed = $assistantContent.Trim()
    $previewMatches = ($preview.Trim().Length -gt 0) -and `
        (($assistantTrimmed -eq $preview.Trim()) -or $assistantTrimmed.StartsWith($preview.Trim()) -or $preview.Trim().StartsWith($assistantTrimmed))
    Add-Result 'invoke/turn-resolved' (($null -ne $latest) -and $previewMatches) `
        "turn_id=$($latest.turn_id) status=$($latest.status) preview='$preview'"
    $inlineTurnId = [string]$invoke.Json.turn_id
    $latestTurnId = ''
    if ($null -ne $latest) { $latestTurnId = [string]$latest.turn_id }
    Add-Result 'invoke/turn-id-inline' `
        ((-not [string]::IsNullOrWhiteSpace($inlineTurnId)) -and ($inlineTurnId -eq $latestTurnId)) `
        "invoke.turn_id=$inlineTurnId posthoc.turn_id=$latestTurnId"

    # 屏幕回读必须能看到本轮回复：needle 取回复首个非空行前 40 字符，
    # 容忍 TUI 合成帧的缩进/换行（长回复被换行截断时只看首行前缀）。
    $needle = ''
    $firstLine = $assistantContent -split "`r?`n" | Where-Object { -not [string]::IsNullOrWhiteSpace($_) } | Select-Object -First 1
    if ($null -ne $firstLine) { $needle = $firstLine.Trim() }
    if ($needle.Length -gt 40) { $needle = $needle.Substring(0, 40) }
    $tuiAfter = Invoke-JsonHttp -Method GET -Url "${screenUrl}?view=tui&format=json" -TimeoutSec 15
    $tuiAfterText = ''
    if ($null -ne $tuiAfter.Json) {
        $tuiAfterText = [string]$tuiAfter.Json.text
        if ([string]::IsNullOrWhiteSpace($tuiAfterText) -and $null -ne $tuiAfter.Json.lines) { $tuiAfterText = (@($tuiAfter.Json.lines) -join "`n") }
    } else {
        $tuiAfterText = [string]$tuiAfter.Text
    }
    $needleFound = ($needle.Length -gt 0) -and $tuiAfterText.Contains($needle)
    Add-Result 'screen/assistant-visible' $needleFound "needle='$needle' found=$needleFound"

    # ------------------------------------------------------------------
    # 5. 幂等回放（同 client_request_id 不得重复注入）
    # ------------------------------------------------------------------
    $turnBefore = Invoke-JsonHttp -Method GET -Url $turnUrl -TimeoutSec 15
    $turnBeforeCount = @($turnBefore.Json.recent).Count
    $replay = Invoke-JsonHttp -Method POST -Url $invokeUrl -Body $invokeBody -TimeoutSec ([Math]::Ceiling($InvokeTimeoutMs / 1000) + 60)
    $replayResult = $replay.Json
    $replayContent = ''
    if ($null -ne $replay.Json.assistant) { $replayContent = [string]$replay.Json.assistant.content }
    Add-Result 'invoke/idempotent-replay' `
        (([bool]$replay.Json.duplicate) -and ($replayContent -eq $assistantContent) -and ([int64]$replay.Json.elapsed_ms -eq [int64]$invoke.Json.elapsed_ms)) `
        "duplicate=$($replay.Json.duplicate) elapsed_ms=$($replay.Json.elapsed_ms) same_content=$($replayContent -eq $assistantContent)"
    $turnAfter = Invoke-JsonHttp -Method GET -Url $turnUrl -TimeoutSec 15
    $turnAfterCount = @($turnAfter.Json.recent).Count
    Add-Result 'invoke/replay-no-new-turn' ($turnAfterCount -eq $turnBeforeCount) `
        "recent_before=$turnBeforeCount recent_after=$turnAfterCount"

    # ------------------------------------------------------------------
    # 6. turn 后验
    # ------------------------------------------------------------------
    $turnId = [string]$latest.turn_id
    $turnById = Invoke-JsonHttp -Method GET -Url "${turnUrl}?id=$([uri]::EscapeDataString($turnId))" -TimeoutSec 15
    $rec = $turnById.Json.turn
    $turnOk = ([bool]$turnById.Json.found) -and ($null -ne $rec) -and ([string]$rec.turn_id -eq $turnId) -and `
        ([string]$rec.status -eq 'completed') -and ([int64]$rec.duration_ms -gt 0) -and ([int]$rec.steps -ge 1) -and `
        (-not [string]::IsNullOrWhiteSpace([string]$rec.assistant_preview))
    Add-Result 'turn/found-by-id' $turnOk `
        "found=$($turnById.Json.found) status=$($rec.status) duration_ms=$($rec.duration_ms) steps=$($rec.steps) usage_scope=$($rec.usage_scope)"
    $current = $turnById.Json.current
    Add-Result 'turn/current-idle' (($null -ne $current) -and (-not [bool]$current.busy) -and ([int]$current.pending_inputs -eq 0)) `
        "busy=$($current.busy) pending_inputs=$($current.pending_inputs)"

    # ------------------------------------------------------------------
    # 7. /exit 收尾（优雅退出 + 端口释放）
    # ------------------------------------------------------------------
    if ($KeepAlive) {
        Add-Result 'exit/keep-alive' $false 'KeepAlive 指定：未执行 /exit 收尾（人工排查模式，不作为通过）'
    } else {
        $exitResp = Invoke-JsonHttp -Method POST -Url $inputUrl -Body @{ prompt = '/exit' } -TimeoutSec 15
        Add-Result 'exit/input-queued' (($exitResp.StatusCode -eq 200) -and ($exitResp.Json.status -eq 'queued')) `
            "HTTP $($exitResp.StatusCode) status=$($exitResp.Json.status)"
        $exited = $proc.WaitForExit($ExitTimeoutSec * 1000)
        if ($exited) {
            $proc.Refresh()
            $exitedGracefully = ($proc.ExitCode -eq 0)
            Add-Result 'exit/graceful-code0' $exitedGracefully "exit_code=$($proc.ExitCode)"
        } else {
            Add-Result 'exit/graceful-code0' $false "在 ${ExitTimeoutSec}s 内未退出"
        }
        $released = Test-TcpPortFree -TargetPort $port
        Add-Result 'exit/port-released' $released "port=$port free=$released"
    }
} catch {
    Add-Result 'harness/aborted' $false $_.Exception.Message
} finally {
    if ($null -eq $prevMeshDir) { Remove-Item Env:AICLI_MESH_DIR -ErrorAction SilentlyContinue } else { $env:AICLI_MESH_DIR = $prevMeshDir }
    Stop-AicliTimeline -Job $script:timelineJob
    $failedNow = @($script:results | Where-Object { -not $_.passed }).Count -gt 0
    # A2/C4：兜底取证（若首个 FAIL 已经抓过则直接返回）。中断/超时类失败
    # 不会经过 Add-Result，需要在这里补一次——此时进程通常仍然存活。
    if ($failedNow) { Invoke-FailureForensics -Reason 'debug-endpoints-01 failed' }
    if ($null -ne $proc -and -not $proc.HasExited) {
        if ($failedNow -and $KeepAliveOnFailSec -gt 0) {
            # C3：失败现场保留，便于人工连上端口复现（期间端口与端点仍可用）。
            Write-Log "keepalive: 失败保留进程 pid=$($proc.Id) ${KeepAliveOnFailSec}s（base=$($script:baseUrl)）"
            Start-Sleep -Seconds $KeepAliveOnFailSec
        }
        Write-Log "cleanup: 强制结束 pid=$($proc.Id)"
        Stop-Process -Id $proc.Id -Force -ErrorAction SilentlyContinue
        Start-Sleep -Milliseconds 500
    }
}

# ----------------------------------------------------------------------
# 汇总与退出码
# ----------------------------------------------------------------------
$failed = @($script:results | Where-Object { -not $_.passed })
$passedCount = @($script:results | Where-Object { $_.passed }).Count
$timelineSamples = 0
if (Test-Path -LiteralPath $timelinePath) { $timelineSamples = @(Get-Content -LiteralPath $timelinePath).Count }
$summary = [pscustomobject]@{
    finished_at = (Get-Date).ToUniversalTime().ToString('o')
    repo_root   = $repoRoot
    exe         = $ExePath
    port        = $port
    prompt      = $Prompt
    artifacts   = $ArtifactDir
    passed      = $passedCount
    failed      = $failed.Count
    results     = $script:results
    evidence    = [pscustomobject]@{
        invoke = $invokeResult
        replay = $replayResult
        turns  = [pscustomobject]@{ before = $turnBeforeCount; after = $turnAfterCount }
        timeline = [pscustomobject]@{ path = $timelinePath; samples = $timelineSamples }
        diagnostics = [pscustomobject]@{ dir = $diagDir; captured = (Test-Path -LiteralPath $diagDir) }
        mesh        = [pscustomobject]@{ dir = $meshDir; isolated = $true }
    }
}
[System.IO.File]::WriteAllText($summaryPath, ($summary | ConvertTo-Json -Depth 12), (New-Object System.Text.UTF8Encoding($false)))

Write-Host ''
Write-Log ("总结: PASS={0} FAIL={1}" -f $passedCount, $failed.Count)
$script:results | Format-Table -Property name, passed -AutoSize | Out-String | Write-Host
Write-Host "证据目录: $ArtifactDir"

if ($failed.Count -gt 0) { exit 1 }
exit 0
