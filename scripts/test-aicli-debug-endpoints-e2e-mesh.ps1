<#
.SYNOPSIS
  aicli 多进程网格 E2E（E2E-DEBUG-03）：发现 → CLI/API 同源 → 会话租约互斥 →
  跨进程调用 → 实时扇入 → 崩溃对账 → 无令牌泄漏 → 旧目录清理 → 自包含 → 跨工作区语义 →
  非回环默认拒绝 → 审计降级。

.DESCRIPTION
  验收目标（全部通过才退出码 0；断言名与 docs/e2e/mesh-e2e.md §5 的 M 表一致）：

    M1  mesh/discovery-both-nodes：两个独立进程（A=本仓库、B=临时工作区）各自写节点
        档案；`aicli-mesh ls --json` 与 `GET /web/api/mesh/peers?probe=1` 都能看到
        两节点 state=live、endpoint.base_url 非空且可达，工作区路径不同。
    M2  mesh/cli-api-parity：CLI 视图与 HTTP 视图同源——节点集合、每节点 session.id、
        endpoint.base_url 三者完全一致。
    M3  mesh/session-lease-exclusive：B 尝试 `sessions.resume` A 的会话后，同一会话
        仍只有一个 owner（A），counts.conflict=0，B 没有抢走会话。
    M4  mesh/cross-call-invoke：A 经网格调用让 B 跑完整一轮（CLI `send --allow-write`，
        走 B 的 POST /web/api/mesh/call）；返回 status=ok、result.status=completed、
        turn_id 非空；该 turn 只出现在 B 的 turn 列表，A 的列表不增加（定向投递）。
    M5  mesh/realtime-fanin：订阅 A 的 GET /web/api/mesh/events 期间，能看到
        source_node_id=B 的 mesh.peer.updated 帧，busy 先 true 后 false（B 的忙碌翻转
        被扇入到 A 的流），且首帧是 mesh.ready、seq 单调。
    M6  mesh/crash-reconcile：强杀 B 后 A 的视图在有限时间内把它翻成 stale；
        `aicli-mesh gc --apply` 只回收 B 的档案与租约，A 的档案不受影响。
    M7  mesh/no-token-leak：写令牌原文不得出现在 peers/ls 文本、journal 文件、
        /debug/endpoints 的 JSON 与 text、以及本脚本落盘的证据副本里（只留 token_hint）。
    M8  mesh/legacy-purge：`gc --purge-legacy --apply` 只删旧目录（<AICLI_HOME>/web-ports），
        mesh/ 的 nodes/bindings 完全不受影响。
    M9  mesh/self-containment：全部进程退出后 `aicli-mesh ls --json` 仍能工作（退出码 0、
        不卡住），并把崩溃节点如实标成 stale。
    M10 mesh/cross-workspace-ops：默认允许跨工作区调用（M4 的写调用即跨工作区成功）；
        目标进程带 --mesh-restrict-workspace 启动后，同一写调用 → refused +
        mesh_cross_workspace_denied，只读调用不受影响。
    M11 mesh/nonloopback-default-deny + mesh/nonloopback-cli-parity：目标进程带
        --web-host 0.0.0.0 启动后，网格写路径**默认整机拒绝**——连回环客户端、带
        正确令牌也拿 403 + refused + mesh_nonloopback_denied（call 与 stop 两个端点
        各探一次；CLI 侧同口径：exit 6 + 同一原因码）；同一节点的非 mesh 端点
        （/web/api/status）照常 200。脚本不传 --mesh-allow-nonloopback（逃生门默认
        关闭是红线，放行路径由 Go 单测覆盖）；B3 的 --mesh-allow-stop=true 只为让
        stop 越过开关检查、走到非回环判定（探针 target 指向 B3 自己，判定若被绕过
        也只会 self_refused，不会真停节点）。
    M12 mesh/journal-disabled：目标进程带 --mesh-journal=false 启动后，self 如实回显
        mesh.journal_enabled=false（A 默认 true 作对照）、该节点不落 journal 文件、
        watch --once 退化（回放里没有它的事件）但不崩，call/gc 照常。

  安全红线（§8.8，脚本不得放宽）：
    - 网格根隔离：AICLI_MESH_DIR 指向 artifacts 子目录，绝不碰真实 ~/.aicli/mesh；
    - 跨机/非回环写路径一律不打开（不传 --mesh-allow-nonloopback）：M11 只用
      --web-host 0.0.0.0 复现「默认拒绝」，逃生门的放行路径不在本脚本里验证；
    - 写 op（invoke/input/cancel/sessions.resume）逐次显式 --allow-write；
    - 证据落盘前脱敏：令牌原文替换为 <REDACTED-TOKEN>，只保留 token_hint；
    - 只杀本脚本自己启动的进程（A/B/B2/B3/B4 与自家 curl 采集器）。

  前置条件：本机已配置可用 provider/model（否则 M4 如实 FAIL，绝不伪造通过）。
  本脚本无人值守：chat 进程都以 `chat --yolo --headless --pprof` 启动（--pprof 展开为
  <webHost>:0，随机空闲端口；--web-port 不接受 0，所以这里不用它）。端口一律从节点档案
  endpoint.port 读取，脚本不预先占用固定端口。

.PARAMETER ExePath
  aicli 可执行文件；缺省 <repo>/backend/.tmp/aicli-debug-e2e.exe。
.PARAMETER MeshExePath
  aicli-mesh 可执行文件；缺省 <repo>/backend/.tmp/aicli-mesh-e2e.exe。
.PARAMETER Prompt
  M4 注入 B 的 prompt；缺省「只回复两个字：收到」。
.PARAMETER StartupTimeoutSec
  等待节点档案 / 清单就绪的超时（秒），缺省 90。
.PARAMETER ExitTimeoutSec
  等待进程退出的超时（秒），缺省 60。
.PARAMETER InvokeTimeoutMs
  M4 invoke 等待 turn 结束的超时（毫秒），缺省 120000。
.PARAMETER SseWindowSec
  M5 SSE 采集窗口上限（秒），缺省 45；采集器会在忙碌回落帧到齐后提前收工。
.PARAMETER LeaseGraceSec
  M3/M6 判定归属与 stale 的观察窗口（秒），缺省 12。
.PARAMETER FaninReadySec
  M5 等 A 的扇入订阅接上 B 的上限（秒），缺省 20；一旦 A 的流里出现 B 的帧
  就提前继续。订阅接通前的窗口里 B 的 busy 翻转不会被扇入（不重放历史）。
.PARAMETER SkipBuild
  跳过 aicli 的 go build（复用已有 ExePath，E2E-DEBUG-01 已构建）；
  aicli-mesh 缺失时仍会构建一次（03 独有的依赖，01 不产出）。
.PARAMETER ArtifactDir
  证据目录；缺省 artifacts/aicli-debug-endpoints-e2e-mesh/<yyyyMMdd-HHmmss>。

.EXAMPLE
  pwsh -NoProfile -File scripts/test-aicli-debug-endpoints-e2e-mesh.ps1

.EXAMPLE
  pwsh -NoProfile -File scripts/test-aicli-debug-endpoints-e2e-mesh.ps1 -SkipBuild -ArtifactDir artifacts/mesh-03
#>
[CmdletBinding()]
param(
    [string]$ExePath,
    [string]$MeshExePath,
    [string]$Prompt = '只回复两个字：收到',
    [ValidateRange(5, 600)][int]$StartupTimeoutSec = 90,
    [ValidateRange(5, 600)][int]$ExitTimeoutSec = 60,
    [ValidateRange(1000, 3600000)][int]$InvokeTimeoutMs = 120000,
    [ValidateRange(5, 600)][int]$SseWindowSec = 45,
    [ValidateRange(1, 300)][int]$LeaseGraceSec = 12,
    [ValidateRange(1, 300)][int]$FaninReadySec = 20,
    [switch]$SkipBuild,
    [string]$ArtifactDir
)

$ErrorActionPreference = 'Stop'

$repoRoot = Split-Path -Parent $PSScriptRoot
$backend = Join-Path $repoRoot 'backend'

if ([string]::IsNullOrWhiteSpace($ExePath)) { $ExePath = Join-Path $backend '.tmp/aicli-debug-e2e.exe' }
if ([string]::IsNullOrWhiteSpace($MeshExePath)) { $MeshExePath = Join-Path $backend '.tmp/aicli-mesh-e2e.exe' }

if ([string]::IsNullOrWhiteSpace($ArtifactDir)) {
    $stamp = Get-Date -Format 'yyyyMMdd-HHmmss'
    $ArtifactDir = Join-Path $repoRoot "artifacts/aicli-debug-endpoints-e2e-mesh/$stamp"
} elseif (-not [System.IO.Path]::IsPathRooted($ArtifactDir)) {
    $ArtifactDir = Join-Path $repoRoot $ArtifactDir
}
New-Item -ItemType Directory -Path $ArtifactDir -Force | Out-Null

$runLogPath = Join-Path $ArtifactDir 'run.log'
$summaryPath = Join-Path $ArtifactDir 'summary.json'
$ssePath = Join-Path $ArtifactDir 'mesh-events.sse'
$sseErrPath = Join-Path $ArtifactDir 'mesh-events.stderr.log'
$evidenceDir = Join-Path $ArtifactDir 'evidence'
# 网格根隔离（施工纪律 A6）：绝不使用真实 ~/.aicli/mesh。
$meshDir = Join-Path $ArtifactDir 'mesh'
# 假 AICLI_HOME：只用于 M8 的 --purge-legacy（旧目录 = <AICLI_HOME>/web-ports）。
# 注意：只在本脚本内做临时作用域，绝不注入 chat 子进程（避免改掉会话存储位置）。
$homeDir = Join-Path $ArtifactDir 'home'
$legacyDir = Join-Path $homeDir 'web-ports'
$workspaceB = Join-Path $ArtifactDir 'workspace-b'
foreach ($dir in @($meshDir, $evidenceDir, $legacyDir, $workspaceB)) {
    New-Item -ItemType Directory -Path $dir -Force | Out-Null
}

# E2E 观测工具集（B4 清单覆盖门禁等；不定义宿主函数，状态只走参数）。
. (Join-Path $PSScriptRoot 'aicli-e2e-harness.ps1')

$prevMeshDir = $env:AICLI_MESH_DIR
$prevHomeEnv = $env:AICLI_HOME
$prevConsoleEncoding = $null
try { $prevConsoleEncoding = [Console]::OutputEncoding } catch { }
try { [Console]::OutputEncoding = New-Object System.Text.UTF8Encoding($false) } catch { }
$env:AICLI_MESH_DIR = $meshDir

$script:results = New-Object System.Collections.Generic.List[object]
$script:skipped = New-Object System.Collections.Generic.List[object]
$script:secrets = @()
$script:procs = New-Object System.Collections.Generic.List[object]

function Write-Log {
    param([string]$Message)
    $line = '[{0}] {1}' -f (Get-Date -Format 'HH:mm:ss'), $Message
    Write-Host $line
    Add-Content -LiteralPath $runLogPath -Value $line -Encoding UTF8
}

function Add-Result {
    param([string]$Name, [bool]$Passed, [string]$Detail)
    $script:results.Add([pscustomobject]@{ name = $Name; passed = $Passed; detail = $Detail })
    $tag = 'FAIL'
    if ($Passed) { $tag = 'PASS' }
    Write-Log ("[{0}] {1} :: {2}" -f $tag, $Name, $Detail)
}

function Add-Skip {
    param([string]$Name, [string]$Detail)
    $script:skipped.Add([pscustomobject]@{ name = $Name; detail = $Detail })
    Write-Log ("[SKIP] {0} :: {1}" -f $Name, $Detail)
}

# ---------------------------------------------------------------------------
# 通用工具（HTTP / 子进程 / 视图取值 / 脱敏）
# ---------------------------------------------------------------------------

function Invoke-JsonHttp {
    param(
        [string]$Method = 'GET',
        [string]$Url,
        [string]$Body = $null,
        [int]$TimeoutSec = 30,
        [hashtable]$Headers = @{}
    )
    $result = [pscustomobject]@{ ok = $false; status = 0; text = ''; json = $null; error = '' }
    $client = $null
    try {
        $handler = New-Object System.Net.Http.HttpClientHandler
        $handler.UseProxy = $false
        $client = New-Object System.Net.Http.HttpClient($handler)
        $client.Timeout = [TimeSpan]::FromSeconds($TimeoutSec)
        $request = New-Object System.Net.Http.HttpRequestMessage([System.Net.Http.HttpMethod]::new($Method), $Url)
        foreach ($key in $Headers.Keys) {
            [void]$request.Headers.TryAddWithoutValidation($key, [string]$Headers[$key])
        }
        if ($null -ne $Body) {
            $request.Content = New-Object System.Net.Http.StringContent($Body, (New-Object System.Text.UTF8Encoding($false)), 'application/json')
        }
        $response = $client.SendAsync($request).GetAwaiter().GetResult()
        $bytes = $response.Content.ReadAsByteArrayAsync().GetAwaiter().GetResult()
        $text = [System.Text.Encoding]::UTF8.GetString($bytes)
        $result.status = [int]$response.StatusCode
        $result.text = $text
        $result.ok = ($result.status -ge 200 -and $result.status -lt 300)
        if (-not [string]::IsNullOrWhiteSpace($text)) {
            try { $result.json = $text | ConvertFrom-Json } catch { }
        }
        $response.Dispose()
    } catch {
        $result.error = $_.Exception.Message
    } finally {
        if ($null -ne $client) { $client.Dispose() }
    }
    return $result
}

# ConvertFrom-Json 的容错版：整段优先，失败则截取首尾花括号之间的子串。
function ConvertFrom-JsonLoose {
    param([string]$Text)
    if ([string]::IsNullOrWhiteSpace($Text)) { return $null }
    try { return ($Text | ConvertFrom-Json) } catch { }
    $start = $Text.IndexOf('{')
    $end = $Text.LastIndexOf('}')
    if ($start -ge 0 -and $end -gt $start) {
        try { return ($Text.Substring($start, $end - $start + 1) | ConvertFrom-Json) } catch { }
    }
    return $null
}

# 调用 aicli-mesh CLI；AICLI_MESH_DIR 由父进程环境继承，HomeOverride 只用于 M8 的临时作用域。
# 注意：参数名**不能**叫 $Home——$HOME 是只读自动变量（Options=ReadOnly,AllScope），
# 绑定该参数会抛「无法覆盖变量 Home，因为它只读变量或常量」，而且报错行不指向调用点，
# 看起来像脚本自身崩溃（实测踩过：run2 因此在 M1 前中断）。
function Invoke-MeshCli {
    param([string[]]$Arguments, [string]$HomeOverride = $null)
    $sw = [System.Diagnostics.Stopwatch]::StartNew()
    $scopedHome = $false
    if (-not [string]::IsNullOrWhiteSpace($HomeOverride)) {
        $env:AICLI_HOME = $HomeOverride
        $scopedHome = $true
    }
    $text = ''
    $code = -1
    try {
        $text = (& $MeshExePath @Arguments 2>&1 | Out-String)
        $code = $LASTEXITCODE
    } catch {
        $text = $_.Exception.Message
        $code = -1
    } finally {
        if ($scopedHome) {
            if ([string]::IsNullOrWhiteSpace($prevHomeEnv)) {
                Remove-Item Env:\AICLI_HOME -ErrorAction SilentlyContinue
            } else {
                $env:AICLI_HOME = $prevHomeEnv
            }
        }
    }
    $sw.Stop()
    return [pscustomobject]@{
        exit_code = $code
        text      = $text
        json      = (ConvertFrom-JsonLoose -Text $text)
        ms        = $sw.ElapsedMilliseconds
    }
}

function Get-Prop {
    param($Object, [string]$Path)
    $current = $Object
    foreach ($segment in $Path.Split('.')) {
        if ($null -eq $current) { return $null }
        $prop = $current.PSObject.Properties[$segment]
        if ($null -eq $prop) { return $null }
        $current = $prop.Value
    }
    return $current
}

# Get-ShortId 截断展示用 id（空值安全：详情字符串在失败路径上也要能拼出来）。
function Get-ShortId {
    param([string]$Id, [int]$Length = 8)
    if ([string]::IsNullOrWhiteSpace($Id)) { return '-' }
    if ($Id.Length -le $Length) { return $Id }
    return $Id.Substring(0, $Length)
}

# Get-LeafSafe 取路径末段（空值安全）。
function Get-LeafSafe {
    param([string]$Path)
    if ([string]::IsNullOrWhiteSpace($Path)) { return '-' }
    try { return (Split-Path -Leaf $Path) } catch { return '-' }
}

# peers / ls 的文档形态在不同子命令下略有差异（裸视图或 { view: ... }），这里统一取 nodes[]。
function Get-ViewNodes {
    param($Document)
    if ($null -eq $Document) { return @() }
    $direct = Get-Prop $Document 'nodes'
    if ($null -ne $direct) { return @($direct) }
    $nested = Get-Prop $Document 'view.nodes'
    if ($null -ne $nested) { return @($nested) }
    return @()
}

function Get-NodeRecords {
    param([string]$MeshDir)
    $nodesDir = Join-Path $MeshDir 'nodes'
    if (-not (Test-Path -LiteralPath $nodesDir)) { return @() }
    $records = New-Object System.Collections.Generic.List[object]
    foreach ($file in (Get-ChildItem -LiteralPath $nodesDir -Filter '*.json' -File -ErrorAction SilentlyContinue)) {
        $record = $null
        try { $record = Get-Content -Raw -LiteralPath $file.FullName | ConvertFrom-Json } catch { continue }
        $records.Add([pscustomobject]@{ path = $file.FullName; record = $record })
    }
# 注意：本机 pwsh 对「@(<List[object]> 变量)」会抛 Argument types do not match
# （已实测：@(List[int]) / @(object[]) / @(管道) 均正常，只有 List[object] 变量会炸）。
# 这里统一用 ToArray()，不要退回 @($records)。
    return $records.ToArray()
}

function Get-RecordByPid {
    param([string]$MeshDir, [int]$ProcessId)
    foreach ($entry in (Get-NodeRecords -MeshDir $MeshDir)) {
        if ([int](Get-Prop $entry.record 'pid') -eq $ProcessId) { return $entry }
    }
    return $null
}

function Wait-RecordByPid {
    param([string]$MeshDir, [int]$ProcessId, [int]$TimeoutSec = 90, [switch]$RequireEndpoint)
    $deadline = (Get-Date).AddSeconds($TimeoutSec)
    $entry = $null
    do {
        $entry = Get-RecordByPid -MeshDir $MeshDir -ProcessId $ProcessId
        if ($null -ne $entry) {
            if (-not $RequireEndpoint) { return $entry }
            # 首版档案可能还没有 endpoint 段（回环地址解析/落盘晚于档案首写；
            # 非回环监听下实测更明显）：等到 endpoint.port 就绪再交还调用方，
            # 端口仍然只从档案读（文件头的前置约定），不另拼地址。
            if ([int](Get-Prop $entry.record 'endpoint.port') -gt 0) { return $entry }
        }
        Start-Sleep -Milliseconds 500
    } while ((Get-Date) -lt $deadline)
    return $entry
}

function Wait-ManifestReady {
    param([string]$BaseUrl, [int]$TimeoutSec = 90, [hashtable]$Headers = @{})
    $deadline = (Get-Date).AddSeconds($TimeoutSec)
    $last = $null
    do {
        $last = Invoke-JsonHttp -Url "$BaseUrl/debug/endpoints" -TimeoutSec 10 -Headers $Headers
        if ($last.ok -and [bool](Get-Prop $last.json 'available')) { return $last }
        Start-Sleep -Milliseconds 700
    } while ((Get-Date) -lt $deadline)
    return $last
}

function Start-AicliChat {
    param(
        [string]$Name,
        [string]$WorkDir,
        [string[]]$ExtraArgs = @(),
        [string]$StdoutPath,
        [string]$StderrPath
    )
    $chatArgs = @('chat', '--yolo', '--headless', '--pprof') + $ExtraArgs
    $proc = Start-Process -FilePath $ExePath -ArgumentList $chatArgs -WorkingDirectory $WorkDir -PassThru `
        -RedirectStandardOutput $StdoutPath -RedirectStandardError $StderrPath
    $script:procs.Add([pscustomobject]@{ name = $Name; process = $proc; stdout = $StdoutPath; stderr = $StderrPath })
    Write-Log ("start {0}: pid={1} cwd={2} args={3}" -f $Name, $proc.Id, $WorkDir, ($chatArgs -join ' '))
    return $proc
}

function Protect-Text {
    param([string]$Text, [string[]]$Secrets)
    $out = [string]$Text
    foreach ($secret in $Secrets) {
        if (-not [string]::IsNullOrWhiteSpace($secret) -and $out.Contains($secret)) {
            $out = $out.Replace($secret, '<REDACTED-TOKEN>')
        }
    }
    return $out
}

function Save-RedactedText {
    param([string]$Path, [string]$Text, [string[]]$Secrets)
    [System.IO.File]::WriteAllText($Path, (Protect-Text -Text $Text -Secrets $Secrets), (New-Object System.Text.UTF8Encoding($false)))
}

function Save-ProcessLogTail {
    param([string]$Path, [string]$Target, [int]$Lines = 40)
    if (-not (Test-Path -LiteralPath $Path)) { return }
    try {
        $tail = Get-Content -LiteralPath $Path -Tail $Lines -Encoding UTF8
        Save-RedactedText -Path $Target -Text ($tail -join "`n") -Secrets @($script:secrets)
    } catch { }
}

# Get-MeshRootInventory 返回网格根的相对文件清单（M8 用它证明 purge-legacy 只动旧目录）。
function Get-MeshRootInventory {
    param([string]$MeshDir)
    $items = New-Object System.Collections.Generic.List[string]
    foreach ($file in (Get-ChildItem -LiteralPath $MeshDir -Recurse -File -ErrorAction SilentlyContinue)) {
        $rel = $file.FullName.Substring($MeshDir.Length).TrimStart('\', '/')
        $items.Add(($rel -replace '\\', '/'))
    }
    return @($items | Sort-Object)
}

function Stop-ScriptProcess {
    param([string]$Name, [bool]$Force = $false)
    foreach ($entry in $script:procs) {
        if ($entry.name -ne $Name) { continue }
        $proc = $entry.process
        try {
            if (-not $proc.HasExited) {
                if ($Force) { Stop-Process -Id $proc.Id -Force -ErrorAction SilentlyContinue }
                else { Stop-Process -Id $proc.Id -ErrorAction SilentlyContinue }
            }
        } catch { }
    }
}

# 全程 try/catch/finally：任何未预期异常都必须 ① 清理自启进程 ② 摘要判 FAIL。
# 否则「异常退出 → A/B 残留 + summary.json 缺席」既会污染后续用例，也会被聚合器当成
# 「没有失败断言」而误判通过（实测踩过：run2 因 $Home 参数绑定异常直接中断且留下孤儿进程）。
$scriptFailure = $null
try {
# ---------------------------------------------------------------------------
# 0. 构建（aicli-mesh 是 03 独有的依赖：E2E-DEBUG-01 只产出 aicli，缺失时必须构建）
# ---------------------------------------------------------------------------
Write-Log ("artifact dir : {0}" -f $ArtifactDir)
Write-Log ("mesh dir     : {0}" -f $meshDir)
Write-Log ("aicli exe    : {0}" -f $ExePath)
Write-Log ("aicli-mesh   : {0}" -f $MeshExePath)

$buildLogPath = Join-Path $ArtifactDir 'build.log'
$script:buildLog = New-Object System.Collections.Generic.List[string]

function Invoke-GoBuild {
    param([string]$Package, [string]$OutPath)
    Push-Location $backend
    try {
        $text = (& go build -o $OutPath $Package 2>&1 | Out-String)
        $code = $LASTEXITCODE
    } finally {
        Pop-Location
    }
    $script:buildLog.Add(("[{0}] go build -o {1} {2} -> exit {3}`n{4}" -f (Get-Date -Format 'HH:mm:ss'), $OutPath, $Package, $code, $text))
    return [pscustomobject]@{ exit_code = $code; text = $text }
}

if (-not $SkipBuild) {
    Write-Log 'build aicli ...'
    $built = Invoke-GoBuild -Package './cmd/aicli' -OutPath $ExePath
    if ($built.exit_code -ne 0) {
        Add-Result 'startup/build-artifacts' -Passed $false -Detail ("go build ./cmd/aicli 失败（exit {0}）：{1}" -f $built.exit_code, ($built.text.Trim() -split "`n" | Select-Object -First 3 | Join-String -Separator ' | '))
    }
} else {
    Write-Log 'skip aicli build (SkipBuild)'
}
# aicli-mesh **恒构建**：它是 03 独有的依赖（01 不产出），而 03 的断言几乎全落在网格
# 代码上——复用旧二进制会让「改了 mesh 代码但没重编」伪装成通过（实测踩过：target
# 解析修好后，harness 仍在跑旧二进制）。go 构建缓存命中时通常只要 1–3 秒。
Write-Log 'build aicli-mesh ...'
$builtMesh = Invoke-GoBuild -Package './cmd/aicli-mesh' -OutPath $MeshExePath
if ($builtMesh.exit_code -ne 0) {
    Add-Result 'startup/build-artifacts' -Passed $false -Detail ("go build ./cmd/aicli-mesh 失败（exit {0}）：{1}" -f $builtMesh.exit_code, ($builtMesh.text.Trim() -split "`n" | Select-Object -First 3 | Join-String -Separator ' | '))
}
Add-Content -LiteralPath $buildLogPath -Value ($script:buildLog -join "`n") -Encoding UTF8

$missing = @()
foreach ($candidate in @($ExePath, $MeshExePath)) {
    if (-not (Test-Path -LiteralPath $candidate)) { $missing += $candidate }
}
if ($missing.Count -gt 0) {
    if (-not ($script:results | Where-Object { $_.name -eq 'startup/build-artifacts' })) {
        Add-Result 'startup/build-artifacts' -Passed $false -Detail ("可执行文件缺失：{0}" -f ($missing -join '; '))
    }
    Write-Log '构建产物缺失，终止。'
    $script:abort = $true
} else {
    Add-Result 'startup/build-artifacts' -Passed $true -Detail 'aicli / aicli-mesh 构建产物就绪'
}

# ---------------------------------------------------------------------------
# 1. 启动两个独立进程：A=本仓库，B=临时工作区（跨工作区调用场景）
# ---------------------------------------------------------------------------
$baseA = ''
$baseB = ''
$nodeIdA = ''
$nodeIdB = ''
$recordA = $null
$recordB = $null
$manifestA = $null
$manifestB = $null

if (-not $script:abort) {
    $procA = Start-AicliChat -Name 'A' -WorkDir $repoRoot `
        -StdoutPath (Join-Path $ArtifactDir 'A.stdout.log') -StderrPath (Join-Path $ArtifactDir 'A.stderr.log')
    $procB = Start-AicliChat -Name 'B' -WorkDir $workspaceB `
        -StdoutPath (Join-Path $ArtifactDir 'B.stdout.log') -StderrPath (Join-Path $ArtifactDir 'B.stderr.log')

    $recordA = Wait-RecordByPid -MeshDir $meshDir -ProcessId $procA.Id -TimeoutSec $StartupTimeoutSec -RequireEndpoint
    $recordB = Wait-RecordByPid -MeshDir $meshDir -ProcessId $procB.Id -TimeoutSec $StartupTimeoutSec -RequireEndpoint

    if ($null -eq $recordA -or $null -eq $recordB) {
        Save-ProcessLogTail -Path (Join-Path $ArtifactDir 'A.stderr.log') -Target (Join-Path $evidenceDir 'A.stderr.tail.txt')
        Save-ProcessLogTail -Path (Join-Path $ArtifactDir 'B.stderr.log') -Target (Join-Path $evidenceDir 'B.stderr.tail.txt')
        Add-Result 'startup/endpoints-ready' -Passed $false -Detail ("等待节点档案超时（{0}s）：A={1} B={2}；已落盘 stderr 尾部" -f $StartupTimeoutSec, ($null -ne $recordA), ($null -ne $recordB))
    } else {
        $nodeIdA = [string](Get-Prop $recordA.record 'node_id')
        $nodeIdB = [string](Get-Prop $recordB.record 'node_id')
        $urlA = [string](Get-Prop $recordA.record 'endpoint.base_url')
        $urlB = [string](Get-Prop $recordB.record 'endpoint.base_url')
        if ([string]::IsNullOrWhiteSpace($urlA)) {
            $urlA = 'http://127.0.0.1:{0}' -f (Get-Prop $recordA.record 'endpoint.port')
        }
        if ([string]::IsNullOrWhiteSpace($urlB)) {
            $urlB = 'http://127.0.0.1:{0}' -f (Get-Prop $recordB.record 'endpoint.port')
        }
        $baseA = $urlA.TrimEnd('/')
        $baseB = $urlB.TrimEnd('/')

        $manifestA = Wait-ManifestReady -BaseUrl $baseA -TimeoutSec $StartupTimeoutSec
        $manifestB = Wait-ManifestReady -BaseUrl $baseB -TimeoutSec $StartupTimeoutSec

        $healthA = Invoke-JsonHttp -Url "$baseA/web/api/health" -TimeoutSec 15
        $meshEnabled = (Get-Prop $healthA.json 'mesh_ready')
        if ($null -eq $meshEnabled) { $meshEnabled = (Get-Prop $healthA.json 'mesh.enabled') }
        if ($null -eq $meshEnabled) { $meshEnabled = (Get-Prop $healthA.json 'mesh_enabled') }

        $ok = ($manifestA.ok -and [bool](Get-Prop $manifestA.json 'available')) -and `
              ($manifestB.ok -and [bool](Get-Prop $manifestB.json 'available')) -and `
              ($nodeIdA -ne $nodeIdB)
        $detail = ("A(pid={0}, node={1}, port={2}) / B(pid={3}, node={4}, port={5})；mesh.enabled={6}" -f `
            $procA.Id, $nodeIdA, (Get-Prop $recordA.record 'endpoint.port'), $procB.Id, $nodeIdB, (Get-Prop $recordB.record 'endpoint.port'), $meshEnabled)
        Add-Result 'startup/endpoints-ready' -Passed $ok -Detail $detail
        if (-not $ok) {
            Save-ProcessLogTail -Path (Join-Path $ArtifactDir 'A.stderr.log') -Target (Join-Path $evidenceDir 'A.stderr.tail.txt')
            Save-ProcessLogTail -Path (Join-Path $ArtifactDir 'B.stderr.log') -Target (Join-Path $evidenceDir 'B.stderr.tail.txt')
        }
    }
}

# ---------------------------------------------------------------------------
# M1 mesh/discovery-both-nodes：两节点互相发现，视图与档案一致
# ---------------------------------------------------------------------------
$selfA = $null
$selfB = $null
$lsAll = $null
$peersProbe = $null

if ($baseA -ne '' -and $baseB -ne '') {
    $selfA = Invoke-JsonHttp -Url "$baseA/web/api/mesh/self" -TimeoutSec 15
    $selfB = Invoke-JsonHttp -Url "$baseB/web/api/mesh/self" -TimeoutSec 15
    $peersProbe = Invoke-JsonHttp -Url "$baseA/web/api/mesh/peers?probe=1" -TimeoutSec 40
    # -a：全量视图（含 stale/stopped），与 peers 的默认全量口径对齐（ls 默认只列在线）
    $lsAll = Invoke-MeshCli -Arguments @('ls', '-a', '--json')

    Save-RedactedText -Path (Join-Path $evidenceDir 'm1-self-A.json') -Text $selfA.text -Secrets @($script:secrets)
    Save-RedactedText -Path (Join-Path $evidenceDir 'm1-peers-A-probe1.json') -Text $peersProbe.text -Secrets @($script:secrets)

    $nodesA = Get-ViewNodes $peersProbe.json
    $viewA = $nodesA | Where-Object { [string](Get-Prop $_ 'node_id') -eq $nodeIdA } | Select-Object -First 1
    $viewB = $nodesA | Where-Object { [string](Get-Prop $_ 'node_id') -eq $nodeIdB } | Select-Object -First 1

    $wsA = [string](Get-Prop $viewA 'workspace.path')
    $wsB = [string](Get-Prop $viewB 'workspace.path')
    $stateA = [string](Get-Prop $viewA 'state')
    $stateB = [string](Get-Prop $viewB 'state')
    $epB = [string](Get-Prop $viewB 'endpoint.base_url')

    # endpoint.base_url 必须真的可达：用视图里的地址（而非档案）再探一次。
    $reachB = Invoke-JsonHttp -Url ("{0}/web/api/mesh/self" -f $epB.TrimEnd('/')) -TimeoutSec 10

    $ok = ($null -ne $viewA) -and ($null -ne $viewB) -and `
          ($stateA -eq 'live') -and ($stateB -eq 'live') -and `
          (-not [string]::IsNullOrWhiteSpace($epB)) -and $reachB.ok -and `
          (-not [string]::IsNullOrWhiteSpace($wsA)) -and (-not [string]::IsNullOrWhiteSpace($wsB)) -and `
          ($wsA -ne $wsB)
    Add-Result 'mesh/discovery-both-nodes' -Passed $ok -Detail (
        "A.state={0} B.state={1} B.endpoint={2} 可达={3} wsA={4} wsB={5}" -f `
            $stateA, $stateB, $epB, $reachB.ok, (Get-LeafSafe $wsA), (Get-LeafSafe $wsB))
} else {
    Add-Result 'mesh/discovery-both-nodes' -Passed $false -Detail '进程未就绪（见 startup/endpoints-ready）'
}

# ---------------------------------------------------------------------------
# M2 mesh/cli-api-parity：CLI 视图与 HTTP 视图同源
# ---------------------------------------------------------------------------
if ($null -ne $peersProbe -and $null -ne $lsAll) {
    $parityOk = $false
    $parityDetail = ''
    for ($attempt = 1; $attempt -le 2 -and -not $parityOk; $attempt++) {
        if ($attempt -gt 1) {
            Start-Sleep -Milliseconds 1500
            $peersProbe = Invoke-JsonHttp -Url "$baseA/web/api/mesh/peers?probe=1" -TimeoutSec 40
            $lsAll = Invoke-MeshCli -Arguments @('ls', '-a', '--json')
        }
        $httpNodes = @(Get-ViewNodes $peersProbe.json)
        $cliNodes = @(Get-ViewNodes $lsAll.json)
        $httpIds = @($httpNodes | ForEach-Object { [string](Get-Prop $_ 'node_id') } | Sort-Object)
        $cliIds = @($cliNodes | ForEach-Object { [string](Get-Prop $_ 'node_id') } | Sort-Object)
        $idsMatch = (($httpIds -join ',') -eq ($cliIds -join ','))
        $fieldDiff = New-Object System.Collections.Generic.List[string]
        foreach ($id in $httpIds) {
            $h = $httpNodes | Where-Object { [string](Get-Prop $_ 'node_id') -eq $id } | Select-Object -First 1
            $c = $cliNodes | Where-Object { [string](Get-Prop $_ 'node_id') -eq $id } | Select-Object -First 1
            if ($null -eq $c) { $fieldDiff.Add(("{0}: CLI 缺失" -f $id)); continue }
            foreach ($key in @('session.id', 'endpoint.base_url', 'state')) {
                $hv = [string](Get-Prop $h $key)
                $cv = [string](Get-Prop $c $key)
                if ($hv -ne $cv) { $fieldDiff.Add(("{0}.{1}: http={2} cli={3}" -f (Get-ShortId $id), $key, $hv, $cv)) }
            }
        }
        $parityOk = $idsMatch -and ($fieldDiff.Count -eq 0)
        $parityDetail = ("节点集合一致={0}（http={1} cli={2}）；字段差异={3}" -f $idsMatch, $httpIds.Count, $cliIds.Count, $(if ($fieldDiff.Count -eq 0) { '无' } else { ($fieldDiff -join '; ') }))
    }
    Add-Result 'mesh/cli-api-parity' -Passed $parityOk -Detail ("exit={0} {1}" -f $lsAll.exit_code, $parityDetail)
    Save-RedactedText -Path (Join-Path $evidenceDir 'm2-ls-A.json') -Text $lsAll.text -Secrets @($script:secrets)
} else {
    Add-Result 'mesh/cli-api-parity' -Passed $false -Detail '缺少 peers / ls 数据'
}

# ---------------------------------------------------------------------------
# B4 清单覆盖门禁：网格端点必须被断言覆盖，或在 $Exempt 中显式豁免
# ---------------------------------------------------------------------------
if ($manifestA -and $manifestA.ok) {
    $entries = @($manifestA.json.endpoints)
    $meshGroup = @($entries | Where-Object {
        $p = [string](Get-Prop $_ 'path')
        $p -like '/web/api/mesh/*' -or $p -eq '/web/api/health' -or $p -eq '/debug/endpoints'
    })
    # M11 已在非回环模式下探过 stop（403 + mesh_nonloopback_denied），
    # 所以它进 $asserted 而不是 $exempt——新端点不许悄悄漏在门禁外。
    $asserted = @(
        '/debug/endpoints',
        '/web/api/health',
        '/web/api/mesh/self',
        '/web/api/mesh/peers',
        '/web/api/mesh/events',
        '/web/api/mesh/call',
        '/web/api/mesh/stop'
    )
    # 豁免必须给出理由：spawn 是「拉起新窗口」的单进程前端场景（S9 覆盖），
    # 03 关注多进程网格语义，不重复拉起第三个浏览器窗口。
    $exempt = @{
        '/web/api/mesh/spawn' = 'S9 已覆盖（CLI open + 前端新窗口）；03 只验证多进程网格语义，不重复拉起'
    }
    $scoped = [pscustomobject]@{ endpoints = $meshGroup }
    $gate = Test-AicliEndpointCoverage -EndpointsJson $scoped -Asserted $asserted -Exempt $exempt
    $detail = ("网格组端点 {0} 个：覆盖 {1}，豁免 {2}，遗漏 {3}" -f `
        $meshGroup.Count, $gate.checked, $(if ($gate.exempt.Count -eq 0) { '无' } else { $gate.exempt -join ',' }), $(if ($gate.missing.Count -eq 0) { '无' } else { $gate.missing -join ',' }))
    Add-Result 'gate/mesh-endpoint-coverage' -Passed ([bool]$gate.ok) -Detail $detail
} else {
    Add-Result 'gate/mesh-endpoint-coverage' -Passed $false -Detail '清单不可用'
}

# ---------------------------------------------------------------------------
# M7 mesh/no-token-leak：写令牌原文不得出现在任何可读面（只留 token_hint）
# ---------------------------------------------------------------------------
function Update-EvidenceRedaction {
    foreach ($file in (Get-ChildItem -LiteralPath $evidenceDir -File -ErrorAction SilentlyContinue)) {
        try {
            $content = Get-Content -Raw -LiteralPath $file.FullName
            $masked = Protect-Text -Text $content -Secrets @($script:secrets)
            if ($masked -ne $content) { Save-RedactedText -Path $file.FullName -Text $masked -Secrets @() }
        } catch { }
    }
    foreach ($file in @($runLogPath, $summaryPath)) {
        if (-not (Test-Path -LiteralPath $file)) { continue }
        try {
            $content = Get-Content -Raw -LiteralPath $file
            $masked = Protect-Text -Text $content -Secrets @($script:secrets)
            if ($masked -ne $content) { [System.IO.File]::WriteAllText($file, $masked, (New-Object System.Text.UTF8Encoding($false))) }
        } catch { }
    }
}

if ($null -ne $recordA) {
    $tokenA = [string](Get-Prop $recordA.record 'auth.token')
    if ([string]::IsNullOrWhiteSpace($tokenA)) {
        $revealProbe = Invoke-JsonHttp -Url "$baseA/web/api/mesh/self?reveal_token=1" -TimeoutSec 15
        $tokenA = [string](Get-Prop $revealProbe.json 'auth.token')
    }
    if ([string]::IsNullOrWhiteSpace($tokenA)) {
        Add-Skip 'mesh/no-token-leak' -Detail '节点档案与 self?reveal_token=1 都没有令牌原文，无法验证脱敏（写令牌可能未启用）'
    } else {
        $script:secrets += $tokenA
        Update-EvidenceRedaction

        $selfPlain = Invoke-JsonHttp -Url "$baseA/web/api/mesh/self" -TimeoutSec 15
        $selfReveal = Invoke-JsonHttp -Url "$baseA/web/api/mesh/self?reveal_token=1" -TimeoutSec 15
        $leaks = New-Object System.Collections.Generic.List[string]

        # 1) 可读面（peers / ls / self / 清单 / screen）都不得出现原文；show 是 CLI 的
        #    **显式披露面**（文本与 --json 直给令牌原文 + /web?token=… 打开地址），
        #    因此单独断言「确实披露」而不是「不得出现」。
        if ($peersProbe.text.Contains($tokenA)) { $leaks.Add('peers') }
        if ($lsAll.text.Contains($tokenA)) { $leaks.Add('ls') }
        if ($selfPlain.text.Contains($tokenA)) { $leaks.Add('self') }
        if ($manifestA.text.Contains($tokenA)) { $leaks.Add('endpoints') }
        $screenA = Invoke-MeshCli -Arguments @('screen', "pid:$($procA.Id)", '--json')
        if ($screenA.text.Contains($tokenA)) { $leaks.Add('screen') }
        $lsText = Invoke-MeshCli -Arguments @('ls', '-a')
        if ($lsText.text.Contains($tokenA)) { $leaks.Add('ls-text') }
        $showA = Invoke-MeshCli -Arguments @('show', "pid:$($procA.Id)", '--json')
        $showToken = [string](Get-Prop $showA.json 'token')
        $showWebUrl = [string](Get-Prop $showA.json 'web_url')
        $showRevealOk = ($showA.exit_code -eq 0) -and ($showToken -eq $tokenA) -and `
            $showWebUrl.Contains($tokenA) -and $showWebUrl.Contains('/web?token=')
        $showText = Invoke-MeshCli -Arguments @('show', "pid:$($procA.Id)")
        $showTextOk = ($showText.exit_code -eq 0) -and $showText.text.Contains($tokenA) -and $showText.text.Contains('/web?token=')

        # 2) 网格根内的静态文件（nodes/ 档案按设计持有令牌，其余不得出现）
        $nodesRoot = (Join-Path $meshDir 'nodes')
        foreach ($file in (Get-ChildItem -LiteralPath $meshDir -Recurse -File -ErrorAction SilentlyContinue)) {
            if ($file.FullName.StartsWith($nodesRoot, [System.StringComparison]::OrdinalIgnoreCase)) { continue }
            try {
                $content = Get-Content -Raw -LiteralPath $file.FullName
                if ($content.Contains($tokenA)) { $leaks.Add(("文件:" + $file.Name)) }
            } catch { }
        }

        # 3) 本脚本落盘的证据副本（脱敏后不得残留原文）
        foreach ($file in (Get-ChildItem -LiteralPath $evidenceDir -Recurse -File -ErrorAction SilentlyContinue)) {
            try {
                $content = Get-Content -Raw -LiteralPath $file.FullName
                if ($content.Contains($tokenA)) { $leaks.Add(("证据:" + $file.Name)) }
            } catch { }
        }

        $revealOk = $selfReveal.ok -and $selfReveal.text.Contains($tokenA)
        $hint = [string](Get-Prop $selfPlain.json 'auth.token_hint')
        if ([string]::IsNullOrWhiteSpace($hint)) { $hint = [string](Get-Prop $selfPlain.json 'auth.hint') }

        $ok = ($leaks.Count -eq 0) -and $showRevealOk -and $showTextOk
        $showDetail = "show 披露（--json 原文+web_url={0}，文本原文+地址={1}）" -f $showRevealOk, $showTextOk
        Add-Result 'mesh/no-token-leak' -Passed $ok -Detail (
            "令牌长度={0} 其余可读面泄漏={1}；{2}；self 默认返回 token_hint={3}（非空={4}），reveal_token=1 回原文={5}" -f `
                $tokenA.Length, $(if ($leaks.Count -eq 0) { '无' } else { $leaks -join ',' }), $showDetail, $hint, (-not [string]::IsNullOrWhiteSpace($hint)), $revealOk)
    }
} else {
    Add-Result 'mesh/no-token-leak' -Passed $false -Detail '缺少 A 的节点档案'
}

# ---------------------------------------------------------------------------
# M4 mesh/cross-call-invoke + M5 mesh/realtime-fanin
#   先在 A 上打开 SSE 采集器（curl -N），再让 A 跨进程调用 B 跑完整一轮，
#   同一个窗口里验证「B 忙碌翻转被扇入到 A 的流」。
# ---------------------------------------------------------------------------
$sseFrames = @()
$sendResult = $null
$turnId = ''
$invokeOk = $false
$invokeDetail = '跨进程调用未执行'

if ($baseA -ne '' -and $baseB -ne '' -and -not $script:abort) {
    $curlExe = $null
    $curlCmd = Get-Command curl.exe -ErrorAction SilentlyContinue
    if ($null -ne $curlCmd) { $curlExe = $curlCmd.Source }
    if ($null -eq $curlExe) {
        Add-Skip 'mesh/realtime-fanin' -Detail '本机没有 curl.exe，无法采集 SSE 流'
    }

    $sseProc = $null
    $sseUrl = "$baseA/web/api/mesh/events"
    if ($null -ne $curlExe) {
        $collectSeconds = [Math]::Ceiling($InvokeTimeoutMs / 1000) + 45
        $sseProc = Start-Process -FilePath $curlExe -ArgumentList @('-N', '-s', '--max-time', "$collectSeconds", $sseUrl) `
            -PassThru -RedirectStandardOutput $ssePath -RedirectStandardError $sseErrPath
        Write-Log ("sse collector started: pid={0} url={1} max-time={2}s" -f $sseProc.Id, $sseUrl, $collectSeconds)
        Start-Sleep -Milliseconds 1200
    }

    # 扇入订阅就绪门（M5 前置）：A 的 Subscriber 按 tick 发现 B 并建立 SSE
    # 订阅，接通那一刻 A 会合成一帧 mesh.peer.joined（source=B）。若在订阅
    # 接通前就发 invoke，B 的 busy=true 帧会落在订阅之前——流不重放历史，
    # 于是 A 永远看不到这次翻转（run4 实测：A 05:05:53 才接上 B，而 invoke
    # 05:05:51.7 就开始了，busy=true 丢失）。先等到 B 的帧出现再发调用。
    $faninReady = $false
    $faninWaitedMs = 0
    if ($null -ne $sseProc -and -not [string]::IsNullOrWhiteSpace($nodeIdB)) {
        $faninStart = Get-Date
        $faninDeadline = $faninStart.AddSeconds($FaninReadySec)
        do {
            Start-Sleep -Milliseconds 250
            $probe = ''
            try { $probe = Get-Content -Raw -LiteralPath $ssePath -ErrorAction SilentlyContinue } catch { }
            if ($probe -match ('"source_node_id"\s*:\s*"' + [regex]::Escape($nodeIdB) + '"')) { $faninReady = $true; break }
        } while ((Get-Date) -lt $faninDeadline)
        $faninWaitedMs = [int]((Get-Date) - $faninStart).TotalMilliseconds
        Write-Log ("fanin subscription to B: ready={0} waited={1}ms" -f $faninReady, $faninWaitedMs)
    } else {
        # 没有采集器或不知道 B 的 node id：无从匹配，不阻塞，让后续断言自然说话。
        $faninReady = $true
    }

    # 调用前基线：A 的会话状态（用于证明「被唤醒的是 B，不是 A」）
    $peersBefore = Invoke-JsonHttp -Url "$baseA/web/api/mesh/peers?probe=0" -TimeoutSec 20
    $nodeABefore = (Get-ViewNodes $peersBefore.json) | Where-Object { [string](Get-Prop $_ 'node_id') -eq $nodeIdA } | Select-Object -First 1
    $sessionIdABefore = [string](Get-Prop $nodeABefore 'session.id')

    $timeoutSec = [Math]::Ceiling($InvokeTimeoutMs / 1000)
    $sendResult = Invoke-MeshCli -Arguments @('send', "pid:$($procB.Id)", $Prompt, '--allow-write', '--timeout', "${timeoutSec}s", '--json')
    Save-RedactedText -Path (Join-Path $evidenceDir 'm4-send-A-to-B.json') -Text $sendResult.text -Secrets @($script:secrets)

    $envStatus = [string](Get-Prop $sendResult.json 'status')
    $code = [string](Get-Prop $sendResult.json 'code')
    $turnId = [string](Get-Prop $sendResult.json 'result.turn_id')
    if ([string]::IsNullOrWhiteSpace($turnId)) { $turnId = [string](Get-Prop $sendResult.json 'result.result.turn_id') }
    if ([string]::IsNullOrWhiteSpace($turnId)) { $turnId = [string](Get-Prop $sendResult.json 'turn_id') }
    $resultStatus = [string](Get-Prop $sendResult.json 'result.status')
    if ([string]::IsNullOrWhiteSpace($resultStatus)) { $resultStatus = [string](Get-Prop $sendResult.json 'result.result.status') }
    $targetNode = [string](Get-Prop $sendResult.json 'target.node_id')
    if ([string]::IsNullOrWhiteSpace($targetNode)) { $targetNode = [string](Get-Prop $sendResult.json 'target_node_id') }
    $message = [string](Get-Prop $sendResult.json 'message')

    $invokeOk = ($sendResult.exit_code -eq 0) -and ($envStatus -eq 'ok') -and ($resultStatus -eq 'completed') -and `
                (-not [string]::IsNullOrWhiteSpace($turnId))
    $targetOk = [string]::IsNullOrWhiteSpace($targetNode) -or ($targetNode -eq $nodeIdB)
    $invokeOk = $invokeOk -and $targetOk
    $invokeDetail = ("exit={0} status={1} code={2} result.status={3} turn_id={4} target={5}；message={6}" -f `
        $sendResult.exit_code, $envStatus, $code, $resultStatus, $turnId, (Get-ShortId $targetNode), ($message.Substring(0, [Math]::Min(160, $message.Length))))

    # 等忙碌回落帧（B 的 turn 结束后 host 会再发一次 peer.updated），再收工。
    if ($null -ne $sseProc) {
        $deadline = (Get-Date).AddSeconds($LeaseGraceSec)
        do {
            Start-Sleep -Milliseconds 500
            $probe = ''
            try { $probe = Get-Content -Raw -LiteralPath $ssePath -ErrorAction SilentlyContinue } catch { }
            if ($probe -match '"busy"\s*:\s*false' -or $probe -match '"state"\s*:\s*"idle"') { break }
        } while ((Get-Date) -lt $deadline)
        try { Stop-Process -Id $sseProc.Id -Force -ErrorAction SilentlyContinue } catch { }
        Write-Log 'sse collector stopped'
    }

    # A 未被唤醒：调用后 A 的会话仍属同一会话，且不处于 busy
    $peersAfter = Invoke-JsonHttp -Url "$baseA/web/api/mesh/peers?probe=0" -TimeoutSec 20
    $nodeAAfter = (Get-ViewNodes $peersAfter.json) | Where-Object { [string](Get-Prop $_ 'node_id') -eq $nodeIdA } | Select-Object -First 1
    $sessionIdAAfter = [string](Get-Prop $nodeAAfter 'session.id')
    $stateAAfter = [string](Get-Prop $nodeAAfter 'session.state')
    $aUntouched = ($sessionIdAAfter -eq $sessionIdABefore) -and ($stateAAfter -ne 'busy')
    Add-Result 'mesh/cross-call-invoke' -Passed ($invokeOk -and $aUntouched) -Detail (
        "{0}；A 未被唤醒={1}（session {2}→{3}, state={4}）" -f $invokeDetail, $aUntouched, `
            (Get-ShortId $sessionIdABefore), (Get-ShortId $sessionIdAAfter), $stateAAfter)
    if (-not $invokeOk) {
        Save-ProcessLogTail -Path (Join-Path $ArtifactDir 'B.stdout.log') -Target (Join-Path $evidenceDir 'B.stdout.tail.txt')
        Save-ProcessLogTail -Path (Join-Path $ArtifactDir 'B.stderr.log') -Target (Join-Path $evidenceDir 'B.stderr.tail.txt')
    }

    # ---- M5：解析 SSE 帧 ----
    if ($null -ne $curlExe) {
        $rawLines = @()
        try { $rawLines = Get-Content -LiteralPath $ssePath -Encoding UTF8 -ErrorAction SilentlyContinue } catch { }
        $frames = New-Object System.Collections.Generic.List[object]
        $payload = $null
        foreach ($line in $rawLines) {
            if ($line -match '^data:\s?(.*)$') {
                $payload = ConvertFrom-JsonLoose -Text $Matches[1]
                if ($null -ne $payload) { $frames.Add($payload) }
            }
        }
        $sseFrames = $frames.ToArray()

        $firstType = ''
        if ($sseFrames.Count -gt 0) { $firstType = [string](Get-Prop $sseFrames[0] 'type') }

        $seqOk = $true
        $lastSeq = -1
        foreach ($frame in $sseFrames) {
            $seq = Get-Prop $frame 'seq'
            if ($null -ne $seq) {
                if ([int]$seq -lt $lastSeq) { $seqOk = $false }
                $lastSeq = [int]$seq
            }
        }

        $busyB = New-Object System.Collections.Generic.List[bool]
        $busyASeen = $false
        $bFrameCount = 0
        foreach ($frame in $sseFrames) {
            $type = [string](Get-Prop $frame 'type')
            $source = [string](Get-Prop $frame 'source_node_id')
            if ([string]::IsNullOrWhiteSpace($source)) { $source = [string](Get-Prop $frame 'source') }
            $dataNode = [string](Get-Prop $frame 'data.node_id')
            $isB = ($source -eq $nodeIdB) -or ($dataNode -eq $nodeIdB)
            $isA = ($source -eq $nodeIdA) -or ($dataNode -eq $nodeIdA)
            if ($type -ne 'mesh.peer.updated') { continue }
            if ($isB) {
                $bFrameCount++
                $busy = Get-Prop $frame 'data.busy'
                if ($null -eq $busy) {
                    $state = [string](Get-Prop $frame 'data.state')
                    if ($state -eq 'busy') { $busy = $true }
                    elseif ($state -eq 'idle') { $busy = $false }
                }
                if ($null -ne $busy) { $busyB.Add([bool]$busy) }
            }
            if ($isA) {
                $busyA = Get-Prop $frame 'data.busy'
                if ($null -eq $busyA) { $busyA = ([string](Get-Prop $frame 'data.state') -eq 'busy') }
                if ([bool]$busyA) { $busyASeen = $true }
            }
        }
        $sawBusyTrue = ($busyB -contains $true)
        $sawBusyFalseAfter = $false
        for ($i = 0; $i -lt $busyB.Count; $i++) {
            if ($busyB[$i]) {
                for ($j = $i + 1; $j -lt $busyB.Count; $j++) {
                    if (-not $busyB[$j]) { $sawBusyFalseAfter = $true; break }
                }
                if ($sawBusyFalseAfter) { break }
            }
        }

        $ok = ($sseFrames.Count -gt 0) -and ($firstType -eq 'mesh.ready') -and $seqOk -and `
              ($bFrameCount -gt 0) -and $sawBusyTrue -and $sawBusyFalseAfter -and (-not $busyASeen)
        Add-Result 'mesh/realtime-fanin' -Passed $ok -Detail (
            "帧数={0} 首帧={1} seq 单调={2}；订阅就绪={7}（等待 {8}ms）；B 的 peer.updated 帧={3} busy true→false={4}/{5}；同窗口 A 出现 busy={6}（定向投递反证）" -f `
                $sseFrames.Count, $firstType, $seqOk, $bFrameCount, $sawBusyTrue, $sawBusyFalseAfter, $busyASeen, $faninReady, $faninWaitedMs)
        Save-RedactedText -Path (Join-Path $evidenceDir 'm5-sse-frames.json') -Text (($sseFrames | ConvertTo-Json -Depth 8)) -Secrets @($script:secrets)
    } else {
        Add-Result 'mesh/realtime-fanin' -Passed $false -Detail '未采集到 SSE（curl.exe 缺失）'
    }
} else {
    Add-Result 'mesh/cross-call-invoke' -Passed $false -Detail $invokeDetail
    Add-Result 'mesh/realtime-fanin' -Passed $false -Detail '进程未就绪'
}

# ---------------------------------------------------------------------------
# M3 mesh/session-lease-exclusive：B 抢不走 A 的会话（租约互斥）
#   B 此刻已有自己的活跃会话（M4 刚跑完一轮），所以这次 resume 是有意义的
#   「另一个活跃节点来抢」，而不是对着空会话打空气。
# ---------------------------------------------------------------------------
$recordAFresh = $recordA
if ($null -ne $recordA -and -not $script:abort) {
    $recordAFresh = Get-RecordByPid -MeshDir $meshDir -ProcessId $procA.Id
    $sessionIdA = [string](Get-Prop $recordAFresh.record 'session.id')
    if ([string]::IsNullOrWhiteSpace($sessionIdA)) {
        $sessionIdA = $sessionIdABefore
    }
    if ([string]::IsNullOrWhiteSpace($sessionIdA)) {
        Add-Skip 'mesh/session-lease-exclusive' -Detail 'A 没有活跃会话（headless 启动未建会话），无法构造租约争抢'
    } else {
        $resumeArgs = '{"session_id":"' + $sessionIdA + '"}'
        $resume = Invoke-MeshCli -Arguments @('call', "pid:$($procB.Id)", 'sessions.resume', '--args', $resumeArgs, '--allow-write', '--json')
        Save-RedactedText -Path (Join-Path $evidenceDir 'm3-resume-A-session-on-B.json') -Text $resume.text -Secrets @($script:secrets)

        $resumeStatus = [string](Get-Prop $resume.json 'result.status')
        $resumeCode = [string](Get-Prop $resume.json 'code')
        $resumeMsg = [string](Get-Prop $resume.json 'message')

        # 观察窗口：给「异步接管」留出暴露时间
        $deadline = (Get-Date).AddSeconds($LeaseGraceSec)
        $owners = @()
        $conflicts = -1
        $ownerOk = $false
        do {
            $peersNow = Invoke-JsonHttp -Url "$baseA/web/api/mesh/peers?probe=0" -TimeoutSec 20
            $nodesNow = @(Get-ViewNodes $peersNow.json)
            $owners = @($nodesNow | Where-Object { [string](Get-Prop $_ 'session.id') -eq $sessionIdA } |
                ForEach-Object { [string](Get-Prop $_ 'node_id') })
            $conflicts = [int](Get-Prop $peersNow.json 'counts.conflict')
            $ownerOk = ($owners.Count -eq 1) -and ($owners[0] -eq $nodeIdA)
            if ($ownerOk -and $conflicts -eq 0) { break }
            Start-Sleep -Milliseconds 700
        } while ((Get-Date) -lt $deadline)

        $tookOver = ($resume.exit_code -eq 0) -and ($resumeStatus -eq 'queued' -or $resumeStatus -eq 'ok' -or $resumeStatus -eq 'accepted')
        $ok = $ownerOk -and ($conflicts -eq 0) -and (-not $tookOver)
        Add-Result 'mesh/session-lease-exclusive' -Passed $ok -Detail (
            "会话 {0} 的 owner 数={1}（owner={2}，期望 A={3}）；counts.conflict={4}；B 的 resume: exit={5} status={6} code={7} msg={8}" -f `
                (Get-ShortId $sessionIdA), $owners.Count, `
                $(if ($owners.Count -gt 0) { (Get-ShortId $owners[0]) } else { '-' }), `
                (Get-ShortId $nodeIdA), $conflicts, $resume.exit_code, $resumeStatus, $resumeCode, `
                ($resumeMsg.Substring(0, [Math]::Min(160, $resumeMsg.Length))))
    }
} else {
    Add-Result 'mesh/session-lease-exclusive' -Passed $false -Detail '缺少 A 的节点档案'
}

# ---------------------------------------------------------------------------
# M10a 默认放行（证据采集）：M4 的写调用本来就是跨工作区（A=仓库，B=临时工作区），
#   这里把「工作区确实不同」与「写调用确实成功」固化下来，M10b 之后一起断言。
# ---------------------------------------------------------------------------
$wsA = ''
$wsB = ''
if ($null -ne $recordA -and $null -ne $recordB) {
    $wsA = [string](Get-Prop $recordA.record 'workspace.path')
    $wsB = [string](Get-Prop $recordB.record 'workspace.path')
}
$crossWorkspaceAllowed = ($wsA -ne '') -and ($wsB -ne '') -and ($wsA -ne $wsB) -and $invokeOk

# ---------------------------------------------------------------------------
# M6a mesh/crash-reconcile：强杀 B → A 的视图在有限时间内翻成 stale
# ---------------------------------------------------------------------------
$crashObserved = $false
$crashDetail = '未执行'
if ($baseA -ne '' -and -not $script:abort) {
    Stop-ScriptProcess -Name 'B' -Force $true
    Write-Log ("killed B (pid={0})" -f $procB.Id)
    $deadline = (Get-Date).AddSeconds(30)
    $stateB = ''
    $liveCount = -1
    do {
        $peersNow = Invoke-JsonHttp -Url "$baseA/web/api/mesh/peers?probe=0" -TimeoutSec 20
        $nodesNow = @(Get-ViewNodes $peersNow.json)
        $viewB = $nodesNow | Where-Object { [string](Get-Prop $_ 'node_id') -eq $nodeIdB } | Select-Object -First 1
        $stateB = [string](Get-Prop $viewB 'state')
        $liveCount = [int](Get-Prop $peersNow.json 'counts.live')
        if ($stateB -ne 'live' -and $stateB -ne '') { break }
        Start-Sleep -Milliseconds 700
    } while ((Get-Date) -lt $deadline)
    $crashObserved = ($stateB -eq 'stale') -and ($liveCount -eq 1)
    $crashDetail = ("B.state={0}（期望 stale）；counts.live={1}（期望 1）；观察窗口 30s" -f $stateB, $liveCount)
} else {
    $crashDetail = '进程未就绪'
}

# ---------------------------------------------------------------------------
# M6b mesh/crash-reconcile：gc 只回收 B 的档案与租约，A 的档案不受影响
#   规则依据（cli.go buildGCPlan）：node-record 需要「pid 已死 且 age >= --stale-ttl」；
#   存活 pid 永不删除（规则 R3），所以 1s 的 TTL 只可能命中已崩的 B。
# ---------------------------------------------------------------------------
$gcDetail = '未执行'
$gcOk = $false
if ($crashObserved) {
    $recordAPath = $recordAFresh.path
    $recordBPath = $recordB.path
    $gcRun = Invoke-MeshCli -Arguments @('gc', '--apply', '--json', '--stale-ttl', '1s')
    Save-RedactedText -Path (Join-Path $evidenceDir 'm6-gc-apply.json') -Text $gcRun.text -Secrets @($script:secrets)

    $actionKinds = @($gcRun.json.actions | ForEach-Object { [string](Get-Prop $_ 'kind') } | Sort-Object -Unique)
    $actionPaths = @($gcRun.json.actions | ForEach-Object { [string](Get-Prop $_ 'path') })
    $errors = @($gcRun.json.errors | Where-Object { $null -ne $_ })
    $bRemoved = -not (Test-Path -LiteralPath $recordBPath)
    $aKept = Test-Path -LiteralPath $recordAPath
    $aInPlan = @($actionPaths | Where-Object { $_ -eq $recordAPath }).Count -gt 0

    $lsAfterGc = Invoke-MeshCli -Arguments @('ls', '-a', '--json')
    $nodesAfterGc = @(Get-ViewNodes $lsAfterGc.json)
    $stateAAfterGc = ''
    $viewAAfterGc = $nodesAfterGc | Where-Object { [string](Get-Prop $_ 'node_id') -eq $nodeIdA } | Select-Object -First 1
    if ($null -ne $viewAAfterGc) { $stateAAfterGc = [string](Get-Prop $viewAAfterGc 'state') }

    $ok = ($gcRun.exit_code -eq 0) -and $bRemoved -and $aKept -and (-not $aInPlan) -and ($errors.Count -eq 0) -and ($stateAAfterGc -eq 'live')
    $gcOk = $ok
    $gcDetail = ("exit={0} actions={1}[{2}] 删除 B 档案={3} 保留 A 档案={4} A 在计划中={5} errors={6} gc 后 A.state={7}" -f `
        $gcRun.exit_code, $actionPaths.Count, ($actionKinds -join ','), $bRemoved, $aKept, $aInPlan, $errors.Count, $stateAAfterGc)
} else {
    $gcDetail = ("崩溃未被观测到（{0}），跳过回收验证" -f $crashDetail)
}
Add-Result 'mesh/crash-reconcile' -Passed $gcOk -Detail ("{0}；{1}" -f $crashDetail, $gcDetail)

# ---------------------------------------------------------------------------
# M8 mesh/legacy-purge：gc --purge-legacy 只删旧目录，网格根不受影响
#   AICLI_HOME 只在本脚本的临时作用域内指向假 home，绝不注入 chat 子进程。
# ---------------------------------------------------------------------------
if (-not $script:abort) {
    $seedLegacy = Join-Path $legacyDir 'stale-port.json'
    [System.IO.File]::WriteAllText($seedLegacy, '{"port":12345,"pid":999999,"session_id":"legacy"}' + "`n", (New-Object System.Text.UTF8Encoding($false)))
    $meshBefore = Get-MeshRootInventory -MeshDir $meshDir
    $nodesBefore = @($meshBefore | Where-Object { $_ -like 'nodes/*' } | Sort-Object)

    $purge = Invoke-MeshCli -Arguments @('gc', '--apply', '--json', '--purge-legacy') -HomeOverride $homeDir
    Save-RedactedText -Path (Join-Path $evidenceDir 'm8-purge-legacy.json') -Text $purge.text -Secrets @($script:secrets)

    $legacyGone = -not (Test-Path -LiteralPath $legacyDir)
    $meshAfter = Get-MeshRootInventory -MeshDir $meshDir
    $nodesAfter = @($meshAfter | Where-Object { $_ -like 'nodes/*' } | Sort-Object)
    $nodesUnchanged = (($nodesBefore -join '|') -eq ($nodesAfter -join '|'))
    $recordAStillThere = Test-Path -LiteralPath $recordAFresh.path
    $legacyAction = @($purge.json.actions | Where-Object { [string](Get-Prop $_ 'kind') -eq 'legacy-dir' })

    $ok = $legacyGone -and $nodesUnchanged -and $recordAStillThere -and ($purge.exit_code -eq 0) -and ($legacyAction.Count -ge 1)
    Add-Result 'mesh/legacy-purge' -Passed $ok -Detail (
        "exit={0} 旧目录已删={1} legacy-dir 动作={2} nodes/ 集合不变={3}（{4} 项）A 档案仍在={5}" -f `
            $purge.exit_code, $legacyGone, $legacyAction.Count, $nodesUnchanged, $nodesAfter.Count, $recordAStillThere)
} else {
    Add-Result 'mesh/legacy-purge' -Passed $false -Detail '进程未就绪'
}

# ---------------------------------------------------------------------------
# M10b mesh/cross-workspace-ops：目标进程开 --mesh-restrict-workspace 后，
#   同一写调用被拒（refused + mesh_cross_workspace_denied），只读调用不受影响。
# ---------------------------------------------------------------------------
$restrictedRefused = $false
$restrictedReadOk = $false
$restrictedDetail = '未执行'
if ($baseA -ne '' -and -not $script:abort) {
    $procB2 = Start-AicliChat -Name 'B2' -WorkDir $workspaceB -ExtraArgs @('--mesh-restrict-workspace') `
        -StdoutPath (Join-Path $ArtifactDir 'B2.stdout.log') -StderrPath (Join-Path $ArtifactDir 'B2.stderr.log')
    $recordB2 = Wait-RecordByPid -MeshDir $meshDir -ProcessId $procB2.Id -TimeoutSec $StartupTimeoutSec -RequireEndpoint
    if ($null -eq $recordB2) {
        $restrictedDetail = ("B2 未登记节点档案（{0}s 超时）" -f $StartupTimeoutSec)
        Save-ProcessLogTail -Path (Join-Path $ArtifactDir 'B2.stderr.log') -Target (Join-Path $evidenceDir 'B2.stderr.tail.txt')
    } else {
        $nodeIdB2 = [string](Get-Prop $recordB2.record 'node_id')
        $urlB2 = [string](Get-Prop $recordB2.record 'endpoint.base_url')
        if ([string]::IsNullOrWhiteSpace($urlB2)) { $urlB2 = 'http://127.0.0.1:{0}' -f (Get-Prop $recordB2.record 'endpoint.port') }
        $baseB2 = $urlB2.TrimEnd('/')
        $manifestB2 = Wait-ManifestReady -BaseUrl $baseB2 -TimeoutSec $StartupTimeoutSec

        $sessionForProbe = $sessionIdA
        if ([string]::IsNullOrWhiteSpace($sessionForProbe)) { $sessionForProbe = 'probe-session-nonexistent' }
        $writeProbe = Invoke-MeshCli -Arguments @('call', "pid:$($procB2.Id)", 'sessions.resume', '--args', ('{"session_id":"' + $sessionForProbe + '"}'), '--allow-write', '--json')
        $readProbe = Invoke-MeshCli -Arguments @('call', "pid:$($procB2.Id)", 'node.info', '--json')
        Save-RedactedText -Path (Join-Path $evidenceDir 'm10-restricted-write.json') -Text $writeProbe.text -Secrets @($script:secrets)
        Save-RedactedText -Path (Join-Path $evidenceDir 'm10-restricted-read.json') -Text $readProbe.text -Secrets @($script:secrets)

        $writeCode = [string](Get-Prop $writeProbe.json 'code')
        $crossWorkspaceMentioned = ($writeCode -eq 'mesh_cross_workspace_denied') -or `
            ($writeProbe.text -match 'mesh_cross_workspace_denied') -or ($writeProbe.text -match '跨工作区')
        $restrictedRefused = ($writeProbe.exit_code -eq 6) -and $crossWorkspaceMentioned
        $restrictedReadOk = ($readProbe.exit_code -eq 0) -and ([string](Get-Prop $readProbe.json 'status') -eq 'ok')
        $restrictedDetail = ("B2(node={0}) manifest={1}；写调用 exit={2} code={3}（期望 6 + mesh_cross_workspace_denied）；只读 node.info exit={4} status={5}" -f `
            (Get-ShortId $nodeIdB2), $manifestB2.ok, $writeProbe.exit_code, $writeCode, $readProbe.exit_code, [string](Get-Prop $readProbe.json 'status'))
        Stop-ScriptProcess -Name 'B2' -Force $true
        Write-Log ("killed B2 (pid={0})" -f $procB2.Id)
    }
} else {
    $restrictedDetail = '进程未就绪'
}

$m10Ok = $crossWorkspaceAllowed -and $restrictedRefused -and $restrictedReadOk
Add-Result 'mesh/cross-workspace-ops' -Passed $m10Ok -Detail (
    "默认放行（跨工作区写调用成功）={0}（wsA={1} wsB={2}）；收敛后写调用被拒={3}；收敛后只读放行={4}；{5}" -f `
        $crossWorkspaceAllowed, (Get-LeafSafe $wsA), (Get-LeafSafe $wsB), $restrictedRefused, $restrictedReadOk, $restrictedDetail)

# ---------------------------------------------------------------------------
# M11 mesh/nonloopback-default-deny：--web-host 0.0.0.0（非回环监听）下，网格写
#   路径默认整机拒绝——拒绝依据是「本进程处于非回环模式」，与客户端来源无关：
#   连回环客户端、带正确令牌也照拒（§5.8/§9.4 默认关闭）。
#   探针带 X-AICLI-Token（从档案读）：测的是「有令牌也拒」，而不是被 401 挡在门外。
#   call 与 stop 两个端点各探一次——stop 的开关检查在非回环判定之前，所以 B3 额外
#   带 --mesh-allow-stop=true 让它走到非回环这一层；探针 target 指向 B3 自己，
#   万一判定被绕过也只会拿到 mesh_stop_self_refused，不会真停任何节点。
#   脚本**不传** --mesh-allow-nonloopback：这里只锁「默认拒绝」这条红线，
#   逃生门的放行路径由 Go 单测（web_handlers_mesh_nonloopback_test.go）覆盖。
# ---------------------------------------------------------------------------
$nonloopbackDenied = $false
$nonloopbackStopDenied = $false
$nonloopbackServed = $false
$nonloopbackDetail = '未执行'
if ($baseA -ne '' -and -not $script:abort) {
    $procB3 = Start-AicliChat -Name 'B3' -WorkDir $workspaceB -ExtraArgs @('--web-host', '0.0.0.0', '--mesh-allow-stop=true') `
        -StdoutPath (Join-Path $ArtifactDir 'B3.stdout.log') -StderrPath (Join-Path $ArtifactDir 'B3.stderr.log')
    $recordB3 = Wait-RecordByPid -MeshDir $meshDir -ProcessId $procB3.Id -TimeoutSec $StartupTimeoutSec -RequireEndpoint
    if ($null -eq $recordB3) {
        $nonloopbackDetail = ("B3 未登记节点档案（{0}s 超时）" -f $StartupTimeoutSec)
        Save-ProcessLogTail -Path (Join-Path $ArtifactDir 'B3.stderr.log') -Target (Join-Path $evidenceDir 'B3.stderr.tail.txt')
    } else {
        $nodeIdB3 = [string](Get-Prop $recordB3.record 'node_id')
        $portB3 = [int](Get-Prop $recordB3.record 'endpoint.port')
        $tokenB3 = [string](Get-Prop $recordB3.record 'auth.token')
        $headersB3 = @{}
        if (-not [string]::IsNullOrWhiteSpace($tokenB3)) { $headersB3['X-AICLI-Token'] = $tokenB3 }
        # 直连回环地址的同一端口：0.0.0.0 模式下整机退出网格写路径，客户端来源
        # 与判定无关，所以这是最严格的一条探针（等价于手工验证 P10a）。
        $loopbackB3 = 'http://127.0.0.1:{0}' -f $portB3
        [void](Wait-ManifestReady -BaseUrl $loopbackB3 -TimeoutSec $StartupTimeoutSec -Headers $headersB3)

        $callB3 = Invoke-JsonHttp -Method 'POST' -Url "$loopbackB3/web/api/mesh/call" -Body '{"op":"node.info"}' -Headers $headersB3 -TimeoutSec 20
        # 注意：PowerShell 的 -f 把 { } 当格式项，JSON 字面量里的花括号要写成 {{ }}。
        $stopB3 = Invoke-JsonHttp -Method 'POST' -Url "$loopbackB3/web/api/mesh/stop" -Body ('{{"target":"pid:{0}","mode":"graceful"}}' -f $procB3.Id) -Headers $headersB3 -TimeoutSec 20
        $statusB3 = Invoke-JsonHttp -Url "$loopbackB3/web/api/status" -Headers $headersB3 -TimeoutSec 15
        Save-RedactedText -Path (Join-Path $evidenceDir 'm11-nonloopback-call.json') -Text $callB3.text -Secrets @($script:secrets)
        Save-RedactedText -Path (Join-Path $evidenceDir 'm11-nonloopback-stop.json') -Text $stopB3.text -Secrets @($script:secrets)

        $callStatus = [string](Get-Prop $callB3.json 'status')
        $callCode = [string](Get-Prop $callB3.json 'code')
        $nonloopbackDenied = ($callB3.status -eq 403) -and ($callStatus -eq 'refused') -and ($callCode -eq 'mesh_nonloopback_denied')
        $stopStatus = [string](Get-Prop $stopB3.json 'status')
        $stopCode = [string](Get-Prop $stopB3.json 'code')
        $nonloopbackStopDenied = ($stopB3.status -eq 403) -and ($stopStatus -eq 'refused') -and ($stopCode -eq 'mesh_nonloopback_denied')
        $nonloopbackServed = ($statusB3.status -eq 200)

        # CLI 侧同口径（手工验证 P10b）：CLI 照档案 advertise 的地址直连，没有
        # 客户端侧回环豁免，因此同样拿到 6 + mesh_nonloopback_denied。
        $cliB3 = Invoke-MeshCli -Arguments @('call', "pid:$($procB3.Id)", 'node.info', '--json')
        $cliCode = [string](Get-Prop $cliB3.json 'code')
        $cliStatus = [string](Get-Prop $cliB3.json 'status')
        if (($cliB3.exit_code -eq 6) -and ($cliCode -eq 'mesh_nonloopback_denied')) {
            Add-Result 'mesh/nonloopback-cli-parity' -Passed $true -Detail (
                "aicli-mesh call exit={0} code={1}（advertise={2}）" -f $cliB3.exit_code, $cliCode, (Get-Prop $recordB3.record 'endpoint.base_url'))
        } elseif ($cliStatus -in @('unreachable', 'timeout')) {
            # 环境限制：档案 advertise 的局域网地址在本机不可达（如接口过滤）。
            # 规则本体已由上面 HTTP 探针锁死，这里如实记 SKIP 而不是伪造绿。
            Add-Skip 'mesh/nonloopback-cli-parity' -Detail (
                "档案 advertise 的地址在本机不可达（status={0}）；HTTP 侧已覆盖规则本体" -f $cliStatus)
        } else {
            Add-Result 'mesh/nonloopback-cli-parity' -Passed $false -Detail (
                "aicli-mesh call exit={0} status={1} code={2}（期望 6 + mesh_nonloopback_denied）" -f $cliB3.exit_code, $cliStatus, $cliCode)
        }

        $nonloopbackDetail = ("B3(node={0}, listen=0.0.0.0, port={1}, advertise={2}, 探针带令牌={3})；HTTP call={4}/{5}/{6}；HTTP stop={7}/{8}/{9}（两者都期望 403+refused+mesh_nonloopback_denied）；非 mesh 端点 /web/api/status={10}（期望 200）" -f `
            (Get-ShortId $nodeIdB3), $portB3, (Get-Prop $recordB3.record 'endpoint.base_url'), ($headersB3.Count -gt 0), $callB3.status, $callStatus, $callCode, $stopB3.status, $stopStatus, $stopCode, $statusB3.status)
        Stop-ScriptProcess -Name 'B3' -Force $true
        Write-Log ("killed B3 (pid={0})" -f $procB3.Id)
    }
} else {
    $nonloopbackDetail = '进程未就绪'
}
Add-Result 'mesh/nonloopback-default-deny' -Passed ($nonloopbackDenied -and $nonloopbackStopDenied -and $nonloopbackServed) -Detail $nonloopbackDetail

# ---------------------------------------------------------------------------
# M12 mesh/journal-disabled：--mesh-journal=false 时审计面退化、网格本体不受影响
#   （§9.5）——self 如实回显 journal_enabled=false；该节点不落任何 journal 行
#   （seq 仍分配）；watch --once 退化为只看别人的日志、不崩；call/gc 照常。
# ---------------------------------------------------------------------------
$journalDisabled = $false
$journalDetail = '未执行'
if ($baseA -ne '' -and -not $script:abort) {
    $procB4 = Start-AicliChat -Name 'B4' -WorkDir $workspaceB -ExtraArgs @('--mesh-journal=false') `
        -StdoutPath (Join-Path $ArtifactDir 'B4.stdout.log') -StderrPath (Join-Path $ArtifactDir 'B4.stderr.log')
    $recordB4 = Wait-RecordByPid -MeshDir $meshDir -ProcessId $procB4.Id -TimeoutSec $StartupTimeoutSec -RequireEndpoint
    if ($null -eq $recordB4) {
        $journalDetail = ("B4 未登记节点档案（{0}s 超时）" -f $StartupTimeoutSec)
        Save-ProcessLogTail -Path (Join-Path $ArtifactDir 'B4.stderr.log') -Target (Join-Path $evidenceDir 'B4.stderr.tail.txt')
    } else {
        $nodeIdB4 = [string](Get-Prop $recordB4.record 'node_id')
        $urlB4 = [string](Get-Prop $recordB4.record 'endpoint.base_url')
        if ([string]::IsNullOrWhiteSpace($urlB4)) { $urlB4 = 'http://127.0.0.1:{0}' -f (Get-Prop $recordB4.record 'endpoint.port') }
        $baseB4 = $urlB4.TrimEnd('/')
        $manifestB4 = Wait-ManifestReady -BaseUrl $baseB4 -TimeoutSec $StartupTimeoutSec

        # 1) 开关回显：B4 必须如实自述 false，且 A（默认）仍是 true——
        #    后者是对照，证明这个字段不是写死的。
        $selfB4 = Invoke-JsonHttp -Url "$baseB4/web/api/mesh/self" -TimeoutSec 15
        $selfAJournal = Invoke-JsonHttp -Url "$baseA/web/api/mesh/self" -TimeoutSec 15
        Save-RedactedText -Path (Join-Path $evidenceDir 'm12-self-B4.json') -Text $selfB4.text -Secrets @($script:secrets)
        $journalFlagB4 = Get-Prop $selfB4.json 'mesh.journal_enabled'
        $journalFlagA = Get-Prop $selfAJournal.json 'mesh.journal_enabled'
        $selfEchoOk = ($journalFlagB4 -eq $false) -and ($journalFlagA -eq $true)

        # 2) 写侧：B4 的 journal 文件不得出现；A 的文件必须已有内容（对照）。
        $journalDir = Join-Path $meshDir 'journal'
        $b4JournalExists = Test-Path -LiteralPath (Join-Path $journalDir ("{0}.ndjson" -f $nodeIdB4))
        $aJournalPath = Join-Path $journalDir ("{0}.ndjson" -f $nodeIdA)
        $aJournalLines = 0
        if (Test-Path -LiteralPath $aJournalPath) {
            $aJournalLines = @(Get-Content -LiteralPath $aJournalPath -ErrorAction SilentlyContinue).Count
        }
        $writeSideOk = (-not $b4JournalExists) -and ($aJournalLines -ge 1)

        # 3) 读侧：watch 退化但不崩——回放里不含 B4 的事件，别人的日志照常可见。
        $watchB4 = Invoke-MeshCli -Arguments @('watch', '--once', '--json')
        $watchEvents = @()
        $rawEvents = Get-Prop $watchB4.json 'events'
        if ($null -ne $rawEvents) { $watchEvents = @($rawEvents) }
        $b4Events = @($watchEvents | Where-Object { [string](Get-Prop $_ 'node_id') -eq $nodeIdB4 }).Count
        $watchOk = ($watchB4.exit_code -eq 0) -and ($b4Events -eq 0) -and ($watchEvents.Count -ge 1)

        $callB4 = Invoke-MeshCli -Arguments @('call', "pid:$($procB4.Id)", 'node.info', '--json')
        $gcB4 = Invoke-MeshCli -Arguments @('gc', '--json')
        $readSideOk = ($callB4.exit_code -eq 0) -and ([string](Get-Prop $callB4.json 'status') -eq 'ok') -and ($gcB4.exit_code -eq 0)

        $journalDisabled = $manifestB4.ok -and $selfEchoOk -and $writeSideOk -and $readSideOk
        $journalDetail = ("B4(node={0}) manifest={1}；self.mesh.journal_enabled B4={2}/A={3}（期望 false/true）；B4 journal 文件存在={4}（期望 False）；A journal 行数={5}（对照 ≥1）；watch --once exit={6} B4 事件={7} 总事件={8}；call node.info exit={9} status={10}；gc exit={11}" -f `
            (Get-ShortId $nodeIdB4), $manifestB4.ok, $journalFlagB4, $journalFlagA, $b4JournalExists, $aJournalLines, `
            $watchB4.exit_code, $b4Events, $watchEvents.Count, $callB4.exit_code, [string](Get-Prop $callB4.json 'status'), $gcB4.exit_code)
        Stop-ScriptProcess -Name 'B4' -Force $true
        Write-Log ("killed B4 (pid={0})" -f $procB4.Id)
    }
} else {
    $journalDetail = '进程未就绪'
}
Add-Result 'mesh/journal-disabled' -Passed $journalDisabled -Detail $journalDetail

# ---------------------------------------------------------------------------
# M9 mesh/self-containment：全部进程退出后，aicli-mesh 仍能独立工作并如实对账
# ---------------------------------------------------------------------------
$m9Ok = $false
$m9Detail = '未执行'
if ($null -ne $procA -and $baseA -ne '') {
    Stop-ScriptProcess -Name 'A' -Force $false
    try { $procA.WaitForExit($ExitTimeoutSec * 1000) | Out-Null } catch { }
    if (-not $procA.HasExited) {
        Stop-ScriptProcess -Name 'A' -Force $true
        Write-Log 'A 未在超时内退出，已强杀'
    }
    Write-Log ("A stopped (pid={0})" -f $procA.Id)

    $sw = [System.Diagnostics.Stopwatch]::StartNew()
    # -a：全部进程已退出，A 必须是 stale——默认（只看在线）会把它藏起来（M9 断言依赖全量视图）
    $lsOffline = Invoke-MeshCli -Arguments @('ls', '-a', '--json')
    $showOffline = Invoke-MeshCli -Arguments @('show', "pid:$($procA.Id)", '--json')
    $sw.Stop()
    Save-RedactedText -Path (Join-Path $evidenceDir 'm9-ls-offline.json') -Text $lsOffline.text -Secrets @($script:secrets)

    $offlineNodes = @(Get-ViewNodes $lsOffline.json)
    $viewAOffline = $offlineNodes | Where-Object { [string](Get-Prop $_ 'node_id') -eq $nodeIdA } | Select-Object -First 1
    $stateAOffline = [string](Get-Prop $viewAOffline 'state')
    $fast = $sw.Elapsed.TotalSeconds -lt 30
    $m9Ok = ($lsOffline.exit_code -eq 0) -and ($stateAOffline -eq 'stale') -and $fast
    $m9Detail = ("ls exit={0} 用时={1:N1}s（不卡住={2}）；A.state={3}（期望 stale）；show exit={4}" -f `
        $lsOffline.exit_code, $sw.Elapsed.TotalSeconds, $fast, $stateAOffline, $showOffline.exit_code)
} else {
    $m9Detail = '进程未就绪'
}
Add-Result 'mesh/self-containment' -Passed $m9Ok -Detail $m9Detail
} catch {
    # 未预期异常：记录原因，仍然走 finally 清理；摘要一律判 FAIL（绝不降级通过）
    $scriptFailure = $_
} finally {
# ---------------------------------------------------------------------------
# 收尾：清理自启进程 / 还原环境（正常结束与异常终止都必须执行）
# ---------------------------------------------------------------------------
foreach ($name in @('A', 'B', 'B2', 'B3', 'B4')) { Stop-ScriptProcess -Name $name -Force $true }
$leftover = New-Object System.Collections.Generic.List[string]
foreach ($entry in $script:procs) {
    try {
        if (-not $entry.process.HasExited) {
            $leftover.Add(("{0}(pid={1})" -f $entry.name, $entry.process.Id))
            Stop-Process -Id $entry.process.Id -Force -ErrorAction SilentlyContinue
        }
    } catch { }
}
if ($leftover.Count -gt 0) { Write-Log ("收尾时仍有存活进程，已强制结束：{0}" -f ($leftover -join ', ')) }

if ($null -eq $prevMeshDir) { Remove-Item Env:\AICLI_MESH_DIR -ErrorAction SilentlyContinue } else { $env:AICLI_MESH_DIR = $prevMeshDir }
if ([string]::IsNullOrWhiteSpace($prevHomeEnv)) { Remove-Item Env:\AICLI_HOME -ErrorAction SilentlyContinue } else { $env:AICLI_HOME = $prevHomeEnv }
if ($null -ne $prevConsoleEncoding) { try { [Console]::OutputEncoding = $prevConsoleEncoding } catch { } }

# 证据副本再脱敏一遍（M7 之后新增的落盘内容）
try { Update-EvidenceRedaction } catch { }
}

$failed = @($script:results | Where-Object { -not $_.passed })
$passedCount = @($script:results | Where-Object { $_.passed }).Count
$verdict = 'pass'
if ($failed.Count -gt 0) { $verdict = 'fail' }
$failureText = ''
if ($null -ne $scriptFailure) {
    $verdict = 'fail'
    $failureText = ("{0} | {1}" -f $scriptFailure.Exception.Message, $scriptFailure.ScriptStackTrace)
    try { Write-Log ("harness 异常终止：{0}" -f $failureText) } catch { }
}

$summary = [ordered]@{
    generated_at   = (Get-Date).ToString('o')
    verdict        = $verdict
    error          = $failureText
    pass           = $passedCount
    fail           = $failed.Count
    skip           = $script:skipped.Count
    artifact_dir   = $ArtifactDir
    mesh_dir       = $meshDir
    aicli_exe      = $ExePath
    mesh_exe       = $MeshExePath
    node_a         = $nodeIdA
    node_b         = $nodeIdB
    results        = $script:results.ToArray()
    skipped        = $script:skipped.ToArray()
    failed_names   = @($failed | ForEach-Object { $_.name })
}
$summaryJson = $summary | ConvertTo-Json -Depth 6
[System.IO.File]::WriteAllText($summaryPath, $summaryJson, (New-Object System.Text.UTF8Encoding($false)))
Update-EvidenceRedaction

$mdLines = New-Object System.Collections.Generic.List[string]
$mdLines.Add(("# E2E-DEBUG-03 多进程网格：{0}" -f $verdict))
$mdLines.Add('')
$mdLines.Add(("- 证据目录：{0}" -f $ArtifactDir))
$mdLines.Add(("- 网格根：{0}（隔离，未触碰真实 ~/.aicli/mesh）" -f $meshDir))
$mdLines.Add(("- 通过 {0} / 失败 {1} / 跳过 {2}" -f $passedCount, $failed.Count, $script:skipped.Count))
$mdLines.Add('')
$mdLines.Add('| 断言 | 结果 | 细节 |')
$mdLines.Add('| --- | --- | --- |')
foreach ($item in $script:results) {
    $tag = 'FAIL'
    if ($item.passed) { $tag = 'PASS' }
    $mdLines.Add(("| {0} | {1} | {2} |" -f $item.name, $tag, ($item.detail -replace '\|', '\|')))
}
foreach ($item in $script:skipped) {
    $mdLines.Add(("| {0} | SKIP | {1} |" -f $item.name, ($item.detail -replace '\|', '\|')))
}
[System.IO.File]::WriteAllText((Join-Path $ArtifactDir 'summary.md'), ($mdLines -join "`n"), (New-Object System.Text.UTF8Encoding($false)))

Write-Log ("verdict={0} pass={1} fail={2} skip={3}" -f $verdict, $passedCount, $failed.Count, $script:skipped.Count)
foreach ($item in $failed) { Write-Log ("  FAIL {0} :: {1}" -f $item.name, $item.detail) }
Write-Log ("summary: {0}" -f $summaryPath)

if ($failed.Count -gt 0 -or $null -ne $scriptFailure) { exit 1 }
exit 0
