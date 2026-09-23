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
    $launchArgs = @('chat', '--yolo', '--web-port', "$port")
    if ($Headless) { $launchArgs += '--headless' }
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
    if ($null -ne $proc -and -not $proc.HasExited) {
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
    }
}
[System.IO.File]::WriteAllText($summaryPath, ($summary | ConvertTo-Json -Depth 12), (New-Object System.Text.UTF8Encoding($false)))

Write-Host ''
Write-Log ("总结: PASS={0} FAIL={1}" -f $passedCount, $failed.Count)
$script:results | Format-Table -Property name, passed -AutoSize | Out-String | Write-Host
Write-Host "证据目录: $ArtifactDir"

if ($failed.Count -gt 0) { exit 1 }
exit 0
