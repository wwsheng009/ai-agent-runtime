<#
.SYNOPSIS
  aicli 工具链 E2E（E2E-TOOLS-01）：单进程 3 轮真实 provider 会话，验证
  read 去重 / write→edit 账本 / 外部改动后的 stale 拒绝。

.DESCRIPTION
  验收目标（全部通过才退出码 0；断言名固定，便于机器判读）：

    1. 单进程 3 轮：同一个 `aicli chat --yolo --web-port <port>` 进程内依次
       用 3 个不同的 client_request_id 调 `POST /web/api/invoke`，保证 read
       账本在轮次间共享（读去重依赖同一 session 的 ledger）。
    2. 读去重（round 1）：prompt 要求模型用 view 以 offset 10/limit 10 对
       `backend/internal/docread/source_limit_test.go` 连读两次：
         - tools/read-dedup-first-read：日志里第一次读返回真实内容
           （包含第 11 行开头 `// overrideMaxDocSourceBytes`）；
         - tools/read-dedup-stub：日志第二次读出现 `unchanged:` 且含
           `offset 10 limit 10`。
    3. write→edit 账本（round 2）：write 新建 → 立刻 edit（不先 view）→ view
       → edit → view：
         - tools/write-edit-recovery：日志出现两次成功 Edited 且无 Failed；
         - tools/write-edit-final：磁盘内容为 ledger-ok。
    4. stale 拒绝（round 3）：view → bash 外部改写 → edit 被拒绝 → 不重试：
         - tools/stale-refusal：日志出现 WRITE_PRECONDITION_FAILED；
         - tools/stale-untouched：磁盘仍为 external-change（拒绝是真的）；
         - tools/turn-tool-errors：该轮 turn 记录自解释（status=failed 且
           tool_error_count>=1，且 error 文本或 recovered 计数能说明发生了什么）。
    5. 每轮通用：
         - tools/turn-terminal：invoke 返回的 turn_id 能在 /web/api/turn
           按 id 找到且 status ∈ {completed, failed}（≤5s 有界重试）；
         - tools/invoke-status：三轮 invoke 的 status 均为 completed。
    6. 收尾：tools/exit-graceful —— POST /web/api/input {"prompt":"/exit"}
       → 进程退出码 0、端口可重新绑定。

  模型可能不按 prompt 走（例如改用 shell）——此时按证据如实判 FAIL 并打印
  日志尾部，绝不伪造通过。

  时间预算：多步 prompt 在慢 provider 下单轮可能到分钟级，故 -InvokeTimeoutMs
  缺省 300000；轮间先等上一轮终态（-DrainTimeoutSec，尽力而为），避免下一轮 prompt
  被排队到同一 turn 造成判据互相污染。

  摘要 schema：pass / fail / skip + results[] + skipped[]（与 E2E-DEBUG-02/03
  一致）；证据落盘 run.log / summary.json / aicli.stdout.log / aicli.stderr.log
  / go-build.log（+ timeline.jsonl / diag/ / roundN-*.json）。

.PARAMETER ExePath
  aicli 可执行文件路径；缺省 <repo>/backend/.tmp/aicli-debug-e2e.exe。
.PARAMETER Port
  监听端口；缺省 0 = 自动挑选空闲端口。
.PARAMETER SkipBuild
  跳过 go build，直接使用 ExePath（要求文件已存在，便于复跑）。
.PARAMETER ArtifactDir
  证据目录；缺省 artifacts/aicli-tools-e2e/<yyyyMMdd-HHmmss>。
.PARAMETER InvokeTimeoutMs
  单轮 invoke 等待 turn 结束的超时（毫秒），缺省 300000。
  本场景的 prompt 是多步工具链（4~5 步），慢 provider/长推理下单轮可能到分钟级；
  实测聚合串跑时 180000 会假红（invoke 返回 timeout 而 turn 仍在推进）。
.PARAMETER DrainTimeoutSec
  轮间排空超时（秒），缺省 120：注入下一轮前先等上一轮 turn 终态，避免新 prompt
  被排队到同一 turn 造成取证/判据互相污染（实跑踩到：R2 未完成导致 R3 复用同一 turn_id）。
  超时未终态也会继续（后续断言如实反映），只做尽力排空。
.PARAMETER StartupTimeoutSec
  等待 /debug/endpoints 就绪的超时（秒），缺省 120。
.PARAMETER ExitTimeoutSec
  等待 /exit 生效的超时（秒），缺省 90。
.PARAMETER NoTimeline
  关闭后台时序采样（缺省开启：timeline.jsonl 每 500ms 一帧）。
.PARAMETER TimelineIntervalMs
  时序采样间隔（毫秒），缺省 500。

.EXAMPLE
  pwsh -NoProfile -File scripts/test-aicli-tools-e2e.ps1

.EXAMPLE
  pwsh -NoProfile -File scripts/test-aicli-tools-e2e.ps1 -SkipBuild
#>
[CmdletBinding()]
param(
    [string]$ExePath,
    [ValidateRange(0, 65535)][int]$Port = 0,
    [switch]$SkipBuild,
    [string]$ArtifactDir,
    [ValidateRange(1000, 3600000)][int]$InvokeTimeoutMs = 300000,
    [ValidateRange(10, 900)][int]$DrainTimeoutSec = 120,
    [ValidateRange(5, 600)][int]$StartupTimeoutSec = 120,
    [ValidateRange(5, 600)][int]$ExitTimeoutSec = 90,
    [switch]$NoTimeline,
    [ValidateRange(100, 60000)][int]$TimelineIntervalMs = 500
)

$ErrorActionPreference = 'Stop'

$scenarioId = 'E2E-TOOLS-01'
$repoRoot = Split-Path -Parent $PSScriptRoot
$backend = Join-Path $repoRoot 'backend'
$tmpDir = Join-Path $backend '.tmp'
# 被测文件（轮次前预处理保证可重复；harness 只动 backend/.tmp 下的数据文件）。
$staleTarget = Join-Path $tmpDir 'e2e-tools-stale.txt'
$ledgerTarget = Join-Path $tmpDir 'e2e-tools-ledger.txt'

$stamp = Get-Date -Format 'yyyyMMdd-HHmmss'
if ([string]::IsNullOrWhiteSpace($ArtifactDir)) {
    $ArtifactDir = Join-Path $repoRoot "artifacts/aicli-tools-e2e/$stamp"
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
# 网格根隔离：本场景只驱动一个进程，把 AICLI_MESH_DIR 指到 artifacts 子目录，
# 不污染真实 ~/.aicli/mesh（与 01/02 harness 同一习惯）。
$meshDir = Join-Path $ArtifactDir 'mesh'
New-Item -ItemType Directory -Path $meshDir -Force | Out-Null

# E2E 观测工具集（A1 时序采样 / A2 诊断包 / A3 稳态判据 / C4 双通道取证）。
. (Join-Path $PSScriptRoot 'aicli-e2e-harness.ps1')

$script:baseUrl = $null
$script:timelineJob = $null
$script:diagCaptured = $false
$script:results = New-Object System.Collections.Generic.List[object]
$script:skips = New-Object System.Collections.Generic.List[object]

function Write-Log {
    param([string]$Message)
    $line = '[{0}] {1}' -f (Get-Date -Format 'HH:mm:ss'), $Message
    Write-Host $line
    Add-Content -LiteralPath $runLogPath -Value $line -Encoding UTF8
}

function Read-LogText {
    param([string]$Path)
    if ([string]::IsNullOrWhiteSpace($Path) -or -not (Test-Path -LiteralPath $Path)) { return '' }
    # 关键：日志文件此刻仍被子进程以写方式持有。File.ReadAllText 默认
    # FileShare.Read，其共享模式不允许"已存在的写句柄"，会直接
    # ERROR_SHARING_VIOLATION；这里显式用 ReadWrite|Delete 共享模式打开，
    # 才能边写边读（Get-Content 内部同样允许 ReadWrite，所以诊断尾部能看到
    # 内容、而轮询读取拿到空串——首次实跑四个日志类断言就是栽在这里）。
    $stream = $null
    $reader = $null
    try {
        $share = [System.IO.FileShare]::ReadWrite -bor [System.IO.FileShare]::Delete
        $stream = [System.IO.File]::Open($Path, [System.IO.FileMode]::Open, [System.IO.FileAccess]::Read, $share)
        $reader = [System.IO.StreamReader]::new($stream, [System.Text.Encoding]::UTF8, $true)
        return $reader.ReadToEnd()
    } catch {
        return ''
    } finally {
        if ($null -ne $reader) { $reader.Dispose() }
        elseif ($null -ne $stream) { $stream.Dispose() }
    }
}

# Get-LogTail：失败时打印证据尾部（stdout 优先，stderr 兜底）。
function Get-LogTail {
    param([string]$Path, [int]$Lines = 20)
    if ([string]::IsNullOrWhiteSpace($Path) -or -not (Test-Path -LiteralPath $Path)) { return "(no log: $Path)" }
    try {
        return (@(Get-Content -LiteralPath $Path -Tail $Lines -Encoding UTF8 -ErrorAction Stop) -join "`n")
    } catch {
        return "(read failed: $($_.Exception.Message))"
    }
}

# Get-FlattenedText：把 TUI 日志按行 trim 后拼成单行，供"跨行 wrap 的短语"做
# 子串/正则判定（TUI 在 ~80 列处换行，直接对原文 grep 会漏）。
function Get-FlattenedText {
    param([string]$Text)
    if ([string]::IsNullOrEmpty($Text)) { return '' }
    $normalized = $Text -replace "`r`n", "`n" -replace "`r", "`n"
    $lines = @($normalized -split "`n" | ForEach-Object { $_.Trim() } | Where-Object { $_ -ne '' })
    return ($lines -join ' ')
}

# Invoke-FailureForensics：A2/C4 取证，幂等（只抓一次）；首个 FAIL 就地抓取，
# 避免 /exit 之后进程已退出只能拿到 connection refused。
function Invoke-FailureForensics {
    param([string]$Reason)
    if ($script:diagCaptured) { return }
    if ([string]::IsNullOrWhiteSpace($script:baseUrl)) { return }
    $script:diagCaptured = $true
    try {
        Save-AicliDiagnostics -BaseUrl $script:baseUrl -DiagDir $diagDir `
            -Reason $Reason -TimelinePath $timelinePath | Out-Null
        Save-AicliDualChannelForensics -BaseUrl $script:baseUrl -DiagDir $diagDir `
            -WindowTitle '' -Label 'screen-fail' | Out-Null
        Write-Log "diag: 首个 FAIL 就地取证已写入 $diagDir"
    } catch {
        Write-Log ("diag: 采集失败: " + $_.Exception.Message)
    }
}

function Add-Result {
    param([string]$Name, [bool]$Passed, [string]$Detail)
    $script:results.Add([pscustomobject]@{ name = $Name; passed = $Passed; detail = $Detail })
    $tag = 'FAIL'
    if ($Passed) { $tag = 'PASS' }
    Write-Log ("[{0}] {1} :: {2}" -f $tag, $Name, $Detail)
    if (-not $Passed) {
        Write-Log ("诊断尾部 (aicli.stdout.log 末 20 行):`n" + (Get-LogTail -Path $stdoutPath -Lines 20))
        if ((Test-Path -LiteralPath $stderrPath) -and ((Get-Item -LiteralPath $stderrPath).Length -gt 0)) {
            Write-Log ("诊断尾部 (aicli.stderr.log 末 10 行):`n" + (Get-LogTail -Path $stderrPath -Lines 10))
        }
        Invoke-FailureForensics -Reason "$Name failed: $Detail"
    }
}

# Add-Skip：环境不具备时使用；不计入 PASS/FAIL，但必须显式记录（summary.skipped）。
function Add-Skip {
    param([string]$Name, [string]$Reason)
    $script:skips.Add([pscustomobject]@{ name = $Name; reason = $Reason })
    Write-Log ("[SKIP] {0} :: {1}" -f $Name, $Reason)
}

# Invoke-JsonHttp：统一走 UTF-8 字节收发，避免 Windows 控制台代码页把中文解坏。
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

# Get-DiscoveredEndpointUrl：URL 只从 /debug/endpoints 清单取，不在脚本内另拼语义。
function Get-DiscoveredEndpointUrl {
    param($Snapshot, [string]$Method, [string]$Path)
    $ep = $Snapshot.endpoints | Where-Object { $_.method -eq $Method -and $_.path -eq $Path } | Select-Object -First 1
    if ($null -ne $ep -and -not [string]::IsNullOrWhiteSpace($ep.url)) { return [string]$ep.url }
    return $null
}

# Read-FileTextNormalized：按 BOM/零字节探测解码（pwsh 7 的 `>` 写 UTF-8，
# Windows PowerShell 5.1 写 UTF-16LE），返回 @{ exists; text; bytes }。
function Read-FileTextNormalized {
    param([string]$Path)
    $result = [pscustomobject]@{ exists = $false; text = $null; bytes = 0 }
    if (-not (Test-Path -LiteralPath $Path)) { return $result }
    $result.exists = $true
    $bytes = [System.IO.File]::ReadAllBytes($Path)
    $result.bytes = $bytes.Length
    $text = $null
    if ($bytes.Length -ge 3 -and $bytes[0] -eq 0xEF -and $bytes[1] -eq 0xBB -and $bytes[2] -eq 0xBF) {
        $text = [System.Text.Encoding]::UTF8.GetString($bytes, 3, $bytes.Length - 3)
    } elseif ($bytes.Length -ge 2 -and $bytes[0] -eq 0xFF -and $bytes[1] -eq 0xFE) {
        $text = [System.Text.Encoding]::Unicode.GetString($bytes, 2, $bytes.Length - 2)
    } elseif ($bytes.Length -ge 2 -and $bytes[0] -eq 0xFE -and $bytes[1] -eq 0xFF) {
        $text = [System.Text.Encoding]::BigEndianUnicode.GetString($bytes, 2, $bytes.Length - 2)
    } else {
        $text = [System.Text.Encoding]::UTF8.GetString($bytes)
        if ($text.Contains([char]0)) {
            $text = [System.Text.Encoding]::Unicode.GetString($bytes)
        }
    }
    $result.text = $text
    return $result
}

# 顶层 trap：任何未处理终止错误都写进 run.log（含类型/位置/调用栈），
# 保证"最后一段流程静默崩溃"也有证据可查，而不是只留一行控制台错误。
trap {
    $errorRecord = $_
    try {
        $line = '[{0}] FATAL {1}: {2}' -f (Get-Date -Format 'HH:mm:ss'), $errorRecord.Exception.GetType().FullName, $errorRecord.Exception.Message
        Write-Host $line
        Add-Content -LiteralPath $runLogPath -Value $line -Encoding UTF8
        Add-Content -LiteralPath $runLogPath -Value ('FATAL at: ' + $errorRecord.InvocationInfo.PositionMessage) -Encoding UTF8
        Add-Content -LiteralPath $runLogPath -Value ('FATAL stack: ' + $errorRecord.ScriptStackTrace) -Encoding UTF8
    } catch { }
    exit 1
}

# ---------------------------------------------------------------------------
# 轮次 prompt（强约束：明确必须调用的工具与步骤；判据不做任何放宽）
# ---------------------------------------------------------------------------

$promptReadDedup = @'
只做两件事，必须调用 view 工具（禁止使用 shell/bash，禁止修改任何文件，不要做其它事）：
1) 用 view 工具以 offset 10、limit 10 读取 backend/internal/docread/source_limit_test.go；
2) 用完全相同的 offset 10、limit 10 再读一次同一文件（必须真的再调用一次 view 工具）。
然后用一句话说明第二次返回是否包含 unchanged 字样（把该字样原样贴出来）。
'@

$promptWriteLedger = @'
依次执行下面 5 步，只允许使用 write/view/edit 工具（禁止使用 shell/bash，不要做其它事）：
1) 用 write 新建 backend/.tmp/e2e-tools-ledger.txt，内容为 hello；
2) 立刻用 edit 把 hello 改成 world（必须直接 edit，不要先 view）；
3) 用 view 读取该文件；
4) 用 edit 把 world 改成 ledger-ok；
5) 再用 view 读取确认。
然后逐条汇报：第 2 步是否被拒绝、拒绝理由一句话是什么；第 4 步是否成功。
'@

$promptStaleGuard = @'
严格按顺序执行 4 步，不要做其它事（第 2 步必须用 bash 工具执行给定 shell 命令，禁止用 write/edit 代替；只允许改这一个文件）：
1) 用 view 读取 backend/.tmp/e2e-tools-stale.txt；
2) 用 bash 执行：echo external-change > backend/.tmp/e2e-tools-stale.txt
3) 用 edit 把文件内容 original 改成 agent-write；
4) 如果第 3 步被拒绝，不要重试、不要再次 view，直接把拒绝理由原文（含 STALE 或"已被修改"等字样）贴出来。
'@

$rounds = @(
    [pscustomobject]@{ id = 1; key = 'read-dedup';   prompt = $promptReadDedup },
    [pscustomobject]@{ id = 2; key = 'write-ledger'; prompt = $promptWriteLedger },
    [pscustomobject]@{ id = 3; key = 'stale-guard';  prompt = $promptStaleGuard }
)

$promptsEvidencePath = Join-Path $ArtifactDir 'prompts.txt'
$promptDump = foreach ($r in $rounds) { "===== round $($r.id) [$($r.key)] =====`n$($r.prompt)`n" }
[System.IO.File]::WriteAllText($promptsEvidencePath, ($promptDump -join "`n"), (New-Object System.Text.UTF8Encoding($false)))

$proc = $null
$port = $Port
$ready = $false
$invokeUrl = $null
$turnUrl = $null
$inputUrl = $null
$invokeRecords = New-Object System.Collections.Generic.List[object]
$turnRecords = New-Object System.Collections.Generic.List[object]
$exitEvidence = $null
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
        # 预创建：go build 静默成功时管道不产生对象，Tee-Object 不会创建文件。
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
    # 1. 轮次前预处理（保证可重复）：stale 写回 original；删除 ledger
    # ------------------------------------------------------------------
    New-Item -ItemType Directory -Path $tmpDir -Force | Out-Null
    [System.IO.File]::WriteAllText($staleTarget, 'original', (New-Object System.Text.UTF8Encoding($false)))
    if (Test-Path -LiteralPath $ledgerTarget) { Remove-Item -LiteralPath $ledgerTarget -Force }
    $preStale = Read-FileTextNormalized -Path $staleTarget
    Write-Log ("preflight: {0} = '{1}'；ledger exists={2}" -f $staleTarget, $preStale.text.Trim(), (Test-Path -LiteralPath $ledgerTarget))

    # ------------------------------------------------------------------
    # 2. 独立进程启动（单进程 3 轮共享 session）
    # ------------------------------------------------------------------
    if ($port -eq 0) { $port = Get-FreeTcpPort }
    $launchArgs = @('chat', '--yolo', '--web-port', "$port")
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
        $tail = Get-LogTail -Path $stderrPath -Lines 20
        Add-Result 'startup/endpoints-ready' $false "在 ${StartupTimeoutSec}s 内未就绪；exe_exited=$($proc.HasExited)；stderr tail:`n$tail"
        throw '启动失败：/debug/endpoints 未就绪'
    }
    $ready = $true
    Add-Result 'startup/endpoints-ready' $true "available=true port=$port uptime_sec=$($snapshot.Json.uptime_sec) version=$($snapshot.Json.version)"

    $script:baseUrl = "http://127.0.0.1:$port"
    if (-not $NoTimeline) {
        $script:timelineJob = Start-AicliTimeline -BaseUrl $script:baseUrl -TimelinePath $timelinePath `
            -IntervalMs $TimelineIntervalMs -Tag 'tools-01' `
            -HarnessPath (Join-Path $PSScriptRoot 'aicli-e2e-harness.ps1')
        Write-Log "timeline: 后台采样已启动 interval=${TimelineIntervalMs}ms -> $timelinePath"
    }

    # ------------------------------------------------------------------
    # 3. 入口发现（URL 只从清单取）
    # ------------------------------------------------------------------
    $invokeUrl = Get-DiscoveredEndpointUrl -Snapshot $snapshot.Json -Method 'POST' -Path '/web/api/invoke'
    $turnUrl = Get-DiscoveredEndpointUrl -Snapshot $snapshot.Json -Method 'GET' -Path '/web/api/turn'
    $inputUrl = Get-DiscoveredEndpointUrl -Snapshot $snapshot.Json -Method 'POST' -Path '/web/api/input'
    Add-Result 'discovery/urls-from-catalog' `
        ((-not [string]::IsNullOrWhiteSpace($invokeUrl)) -and (-not [string]::IsNullOrWhiteSpace($turnUrl)) -and `
            (-not [string]::IsNullOrWhiteSpace($inputUrl))) `
        "invoke=$invokeUrl turn=$turnUrl input=$inputUrl"
    if ([string]::IsNullOrWhiteSpace($invokeUrl) -or [string]::IsNullOrWhiteSpace($turnUrl) -or [string]::IsNullOrWhiteSpace($inputUrl)) {
        throw '清单未暴露 invoke/turn/input 端点'
    }

    # ------------------------------------------------------------------
    # 4. 三个轮次（同一进程、同一 session、不同 client_request_id）
    # ------------------------------------------------------------------
    $prevTurnId = ''
    foreach ($round in $rounds) {
        $r = [int]$round.id
        $key = [string]$round.key
        $requestId = "e2e-tools-01-$stamp-r$r"
        $invokeJson = $null
        $invokeError = $null

        # 轮间排空：上一轮未终态时先等（有界），避免把下一轮 prompt 排队到同一 turn
        # ——实跑踩到过：R2 慢导致 R3 复用同一 turn_id、判据互相污染。
        if ($r -gt 1 -and -not [string]::IsNullOrWhiteSpace($prevTurnId)) {
            $drainDeadline = (Get-Date).AddSeconds($DrainTimeoutSec)
            $drained = $false
            while ((Get-Date) -lt $drainDeadline) {
                try {
                    $drainProbe = Invoke-JsonHttp -Method GET -Url "${turnUrl}?id=$([uri]::EscapeDataString($prevTurnId))" -TimeoutSec 15
                    if ($null -ne $drainProbe -and $null -ne $drainProbe.Json -and [bool]$drainProbe.Json.found -and `
                        $null -ne $drainProbe.Json.turn -and (@('completed', 'failed') -contains [string]$drainProbe.Json.turn.status)) {
                        $drained = $true
                        Write-Log ("round {0}: 上一轮 {1} 已终态（status={2}）" -f $r, $prevTurnId, $drainProbe.Json.turn.status)
                        break
                    }
                } catch { }
                Start-Sleep -Milliseconds 500
            }
            if (-not $drained) {
                Write-Log ("round {0}: 上一轮 {1} 在 {2}s 内未终态，仍继续（后续断言会如实反映）" -f $r, $prevTurnId, $DrainTimeoutSec)
            }
        }

        Write-Log "round ${r}($key): invoke POST $invokeUrl client_request_id=$requestId"
        try {
            $invoke = Invoke-JsonHttp -Method POST -Url $invokeUrl -Body @{
                prompt = [string]$round.prompt; timeout_ms = $InvokeTimeoutMs; client_request_id = $requestId
            } -TimeoutSec ([Math]::Ceiling($InvokeTimeoutMs / 1000) + 60)
            $invokeJson = $invoke.Json
            $invokeJson | ConvertTo-Json -Depth 12 | Set-Content -LiteralPath (Join-Path $ArtifactDir "round$r-invoke.json") -Encoding UTF8
        } catch {
            $invokeError = $_.Exception.Message
            Write-Log "round ${r}: invoke 异常: $invokeError"
        }

        $status = $null
        $turnId = ''
        $elapsedMs = $null
        $assistantChars = 0
        $totalTokens = 0
        if ($null -ne $invokeJson) {
            $status = [string]$invokeJson.status
            $turnId = [string]$invokeJson.turn_id
            $elapsedMs = $invokeJson.elapsed_ms
            if ($null -ne $invokeJson.assistant) { $assistantChars = ([string]$invokeJson.assistant.content).Length }
            if ($null -ne $invokeJson.usage) { $totalTokens = [int]$invokeJson.usage.total_tokens }
        }
        if (-not [string]::IsNullOrWhiteSpace($turnId)) { $prevTurnId = $turnId }
        $invokeRecords.Add([pscustomobject]@{
            round = $r; key = $key; client_request_id = $requestId
            status = $status; turn_id = $turnId; elapsed_ms = $elapsedMs
            assistant_chars = $assistantChars; total_tokens = $totalTokens; error = $invokeError
        })
        Write-Log "round ${r}: status=$status turn_id=$turnId elapsed_ms=$elapsedMs assistant_chars=$assistantChars error=$invokeError"

        # ---- tools/turn-terminal（每轮）：按 id 后验，≤5s 有界重试 ----
        $turn = $null
        $turnLastStatus = ''
        $turnLastFound = $false
        if ([string]::IsNullOrWhiteSpace($turnId)) {
            Write-Log "round ${r}: invoke 未返回 turn_id，无法走 /web/api/turn 后验"
        } else {
            $turnDeadline = (Get-Date).AddSeconds(5)
            while ($true) {
                $probe = $null
                try {
                    $probe = Invoke-JsonHttp -Method GET -Url "${turnUrl}?id=$([uri]::EscapeDataString($turnId))" -TimeoutSec 15
                } catch {
                    $probe = $null
                }
                if ($null -ne $probe -and $null -ne $probe.Json -and [bool]$probe.Json.found -and $null -ne $probe.Json.turn) {
                    $turnLastFound = $true
                    $turnLastStatus = [string]$probe.Json.turn.status
                    if (@('completed', 'failed') -contains $turnLastStatus) { $turn = $probe.Json.turn; break }
                }
                if ((Get-Date) -ge $turnDeadline) { break }
                Start-Sleep -Milliseconds 250
            }
        }
        Add-Result 'tools/turn-terminal' ($null -ne $turn) `
            ("round={0} turn_id={1} found={2} status={3} invoke_error={4}" -f $r, $turnId, $turnLastFound, $turnLastStatus, $invokeError)
        if ($null -ne $turn) {
            $turn | ConvertTo-Json -Depth 8 | Set-Content -LiteralPath (Join-Path $ArtifactDir "round$r-turn.json") -Encoding UTF8
            $turnRecords.Add([pscustomobject]@{
                round = $r; key = $key; turn_id = [string]$turn.turn_id; status = [string]$turn.status
                duration_ms = $turn.duration_ms; steps = $turn.steps; assistant_preview = [string]$turn.assistant_preview
            })
        }

        # P2 契约（stale 轮）：工具失败必须让轮次自解释——status=failed 且
        # tool_error_count>=1，并且错误文本或「已恢复」计数至少有一个能说明发生了什么
        # （实跑证据：error 带 [WRITE_PRECONDITION_FAILED]，tool_error_count=1）。
        if ($null -ne $turn -and $key -eq 'stale-guard') {
            $toolErrCount = [int]$turn.tool_error_count
            $recoveredCount = [int]$turn.recovered_tool_error_count
            $errText = [string]$turn.error
            $explained = (-not [string]::IsNullOrWhiteSpace($errText)) -or ($recoveredCount -ge 1)
            $toolErrOk = (([string]$turn.status -eq 'failed') -and ($toolErrCount -ge 1) -and $explained)
            Add-Result 'tools/turn-tool-errors' $toolErrOk `
                ("round=3 status={0} tool_error_count={1} recovered={2} error='{3}'" -f $turn.status, $toolErrCount, $recoveredCount, $errText)
        }

        # ---- 轮次场景断言（证据在 stdout/磁盘；有界等待 TUI flush，不放宽判据）----
        $waitDeadline = (Get-Date).AddSeconds(10)
        if ($key -eq 'read-dedup') {
            $firstRe = '11:[\s]{0,3}//[\s]{0,3}overrideMaxDocSourceBytes'
            $stubRe = 'unchanged:.{0,300}?offset 10 limit 10'
            $raw = ''; $flat = ''; $firstOk = $false; $stubOk = $false; $stubSnippet = ''
            while ($true) {
                $raw = Read-LogText -Path $stdoutPath
                $flat = Get-FlattenedText -Text $raw
                $firstOk = [regex]::IsMatch($flat, $firstRe)
                $stubMatch = [regex]::Match($flat, $stubRe)
                $stubOk = $stubMatch.Success
                if ($stubOk) {
                    $stubSnippet = $stubMatch.Value
                    if ($stubSnippet.Length -gt 180) { $stubSnippet = $stubSnippet.Substring(0, 180) + '...' }
                }
                if (($firstOk -and $stubOk) -or (Get-Date) -ge $waitDeadline) { break }
                Start-Sleep -Milliseconds 250
            }
            $viewCount = [regex]::Matches($raw, 'Completed view file_path=backend/internal/docread/source_limit_test\.go').Count
            Add-Result 'tools/read-dedup-first-read' $firstOk `
                ("round=1 needle='11: // overrideMaxDocSourceBytes' found={0} view_count={1} log={2}" -f $firstOk, $viewCount, $stdoutPath)
            Add-Result 'tools/read-dedup-stub' $stubOk `
                ("round=1 needle='unchanged: ... offset 10 limit 10' found={0} match='{1}'" -f $stubOk, $stubSnippet)
        } elseif ($key -eq 'write-ledger') {
            $editedRe = 'Edited[\s\S]{0,200}?e2e-tools-ledger\.txt'
            $failedRe = 'Failed[\s\S]{0,200}?e2e-tools-ledger\.txt'
            $raw = ''; $editedCount = 0; $failedCount = 0; $fileInfo = $null; $fileOk = $false
            while ($true) {
                $raw = Read-LogText -Path $stdoutPath
                $editedCount = [regex]::Matches($raw, $editedRe).Count
                $failedCount = [regex]::Matches($raw, $failedRe).Count
                $fileInfo = Read-FileTextNormalized -Path $ledgerTarget
                $fileOk = ($fileInfo.exists -and ($fileInfo.text.Trim() -eq 'ledger-ok'))
                if ((($editedCount -ge 2) -and ($failedCount -eq 0) -and $fileOk) -or (Get-Date) -ge $waitDeadline) { break }
                Start-Sleep -Milliseconds 250
            }
            Add-Result 'tools/write-edit-recovery' (($editedCount -ge 2) -and ($failedCount -eq 0)) `
                ("round=2 edited_count={0} (need>=2) failed_count={1} (need=0) log={2}" -f $editedCount, $failedCount, $stdoutPath)
            $diskText = ''
            if ($fileInfo.exists) { $diskText = $fileInfo.text.Trim(); if ($diskText.Length -gt 80) { $diskText = $diskText.Substring(0, 80) + '...' } }
            Add-Result 'tools/write-edit-final' $fileOk `
                ("round=2 file={0} exists={1} bytes={2} content='{3}' expected='ledger-ok'" -f $ledgerTarget, $fileInfo.exists, $fileInfo.bytes, $diskText)
        } elseif ($key -eq 'stale-guard') {
            $refusalNeedle = 'WRITE_PRECONDITION_FAILED'
            $raw = ''; $stderrRaw = ''; $refusalStdout = $false; $refusalStderr = $false; $fileInfo = $null; $fileOk = $false
            while ($true) {
                $raw = Read-LogText -Path $stdoutPath
                $stderrRaw = Read-LogText -Path $stderrPath
                $refusalStdout = $raw.Contains($refusalNeedle)
                $refusalStderr = $stderrRaw.Contains($refusalNeedle)
                $fileInfo = Read-FileTextNormalized -Path $staleTarget
                $fileOk = ($fileInfo.exists -and ($fileInfo.text.Trim() -eq 'external-change'))
                if ((($refusalStdout -or $refusalStderr) -and $fileOk) -or (Get-Date) -ge $waitDeadline) { break }
                Start-Sleep -Milliseconds 250
            }
            Add-Result 'tools/stale-refusal' ($refusalStdout -or $refusalStderr) `
                ("round=3 needle='{0}' stdout_hit={1} stderr_hit={2}" -f $refusalNeedle, $refusalStdout, $refusalStderr)
            $diskText = ''
            if ($fileInfo.exists) { $diskText = $fileInfo.text.Trim(); if ($diskText.Length -gt 80) { $diskText = $diskText.Substring(0, 80) + '...' } }
            Add-Result 'tools/stale-untouched' $fileOk `
                ("round=3 file={0} exists={1} bytes={2} content='{3}' expected='external-change'" -f $staleTarget, $fileInfo.exists, $fileInfo.bytes, $diskText)
        }
    }

    # ---- tools/invoke-status：三轮 invoke 的 status 均为 completed ----
    $statusList = @($invokeRecords | ForEach-Object { "r$($_.round)=$($_.status)" }) -join ' '
    $allCompleted = ($invokeRecords.Count -eq $rounds.Count) -and (@($invokeRecords | Where-Object { $_.status -ne 'completed' }).Count -eq 0)
    Add-Result 'tools/invoke-status' $allCompleted ("rounds={0} statuses: {1}" -f $invokeRecords.Count, $statusList)

    # ------------------------------------------------------------------
    # 5. /exit 收尾（优雅退出 + 端口释放）
    # ------------------------------------------------------------------
    if ($null -eq $proc -or $proc.HasExited) {
        Add-Result 'tools/exit-graceful' $false "进程已在收尾前退出（exe_exited=$($null -ne $proc -and $proc.HasExited)）"
    } else {
        $exitResp = $null
        try { $exitResp = Invoke-JsonHttp -Method POST -Url $inputUrl -Body @{ prompt = '/exit' } -TimeoutSec 15 } catch { $exitResp = $null }
        $exitHttp = 0; $exitStatus = ''
        if ($null -ne $exitResp) { $exitHttp = $exitResp.StatusCode; $exitStatus = [string]$exitResp.Json.status }
        $exited = $proc.WaitForExit($ExitTimeoutSec * 1000)
        $exitCode = $null
        if ($exited) { $proc.Refresh(); $exitCode = $proc.ExitCode }
        $portFree = Test-TcpPortFree -TargetPort $port
        $exitOk = ($exitHttp -eq 200) -and ($exitStatus -eq 'queued') -and $exited -and ($exitCode -eq 0) -and $portFree
        Add-Result 'tools/exit-graceful' $exitOk `
            ("POST /web/api/input HTTP={0} status={1}; exited={2} exit_code={3}; port={4} free={5}" -f $exitHttp, $exitStatus, $exited, $exitCode, $port, $portFree)
        $exitEvidence = [pscustomobject]@{ http = $exitHttp; queued_status = $exitStatus; exited = $exited; exit_code = $exitCode; port_free = $portFree }
    }
} catch {
    Add-Result 'harness/aborted' $false ($_.Exception.Message + "`n" + $_.ScriptStackTrace)
} finally {
    if ($null -eq $prevMeshDir) { Remove-Item Env:AICLI_MESH_DIR -ErrorAction SilentlyContinue } else { $env:AICLI_MESH_DIR = $prevMeshDir }
    Stop-AicliTimeline -Job $script:timelineJob
    $failedNow = @($script:results | Where-Object { -not $_.passed }).Count -gt 0
    if ($failedNow) { Invoke-FailureForensics -Reason 'tools-e2e failed' }
    if ($null -ne $proc -and -not $proc.HasExited) {
        Write-Log "cleanup: 强制结束 pid=$($proc.Id)"
        Stop-Process -Id $proc.Id -Force -ErrorAction SilentlyContinue
        Start-Sleep -Milliseconds 500
    }
}

# ----------------------------------------------------------------------
# 6. 汇总与退出码（schema：pass/fail/skip + results[] + skipped[]）
# ----------------------------------------------------------------------
$passCount = 0
$failCount = 0
$skipCount = 0
$summaryWriteError = $null

try {
    $passCount = @($script:results | Where-Object { $_.passed }).Count
    $failCount = @($script:results | Where-Object { -not $_.passed }).Count
    $skipCount = $script:skips.Count
    $timelineSamples = 0
    if (Test-Path -LiteralPath $timelinePath) { $timelineSamples = @(Get-Content -LiteralPath $timelinePath).Count }

    $finalStale = Read-FileTextNormalized -Path $staleTarget
    $finalLedger = Read-FileTextNormalized -Path $ledgerTarget

    # 摘要一律用索引器逐条赋值，不调用 OrderedDictionary.Add、也不对 List[object]
    # 做 @() 包装：实跑曾在 `@($invokeRecords)` 处抛
    # System.ArgumentException: Argument types do not match（PSToObjectArrayBinder
    # 的 Expression.Condition 路径），而索引器 + 直接传 List[object] 均不经过该绑定。
    $summary = New-Object System.Collections.Specialized.OrderedDictionary
    $summary['scenario'] = "$scenarioId (tools: read-dedup / write-ledger / stale-guard)"
    $summary['finished_at'] = (Get-Date).ToUniversalTime().ToString('o')
    $summary['repo_root'] = $repoRoot
    $summary['exe'] = $ExePath
    $summary['port'] = $port
    $summary['artifacts'] = $ArtifactDir
    $summary['pass'] = $passCount
    $summary['fail'] = $failCount
    $summary['skip'] = $skipCount
    $summary['results'] = $script:results
    $summary['skipped'] = $script:skips

    $fileEntries = New-Object System.Collections.Specialized.OrderedDictionary
    $fileEntries['stale'] = [pscustomobject]@{
        path = $staleTarget; exists = $finalStale.exists
        content = $(if ($finalStale.exists) { $finalStale.text.Trim() } else { $null })
    }
    $fileEntries['ledger'] = [pscustomobject]@{
        path = $ledgerTarget; exists = $finalLedger.exists
        content = $(if ($finalLedger.exists) { $finalLedger.text.Trim() } else { $null })
    }

    $logEntries = New-Object System.Collections.Specialized.OrderedDictionary
    $logEntries['run'] = $runLogPath
    $logEntries['stdout'] = $stdoutPath
    $logEntries['stderr'] = $stderrPath
    $logEntries['prompts'] = $promptsEvidencePath
    $logEntries['timeline'] = [pscustomobject]@{ path = $timelinePath; samples = $timelineSamples }

    $evidence = New-Object System.Collections.Specialized.OrderedDictionary
    $evidence['rounds'] = $invokeRecords
    $evidence['turns'] = $turnRecords
    $evidence['exit'] = $exitEvidence
    $evidence['files'] = $fileEntries
    $evidence['logs'] = $logEntries
    $evidence['diagnostics'] = [pscustomobject]@{ dir = $diagDir; captured = (Test-Path -LiteralPath $diagDir) }
    $evidence['mesh'] = [pscustomobject]@{ dir = $meshDir; isolated = $true }
    $summary['evidence'] = $evidence

    [System.IO.File]::WriteAllText($summaryPath, [string]($summary | ConvertTo-Json -Depth 12), (New-Object System.Text.UTF8Encoding($false)))
} catch {
    $summaryWriteError = $_.Exception.Message
    Write-Log ("summary: 汇总/写盘失败 {0}: {1}" -f $_.Exception.GetType().FullName, $_.Exception.Message)
    Write-Log ("summary: at " + $_.InvocationInfo.PositionMessage)
    Write-Log ("summary: stack " + $_.ScriptStackTrace)
    Write-Log ("summary: fqid=" + $_.FullyQualifiedErrorId + " category=" + $_.CategoryInfo.Category + " activity=" + $_.CategoryInfo.Activity)
    Write-Log ("summary: target=" + $(if ($null -ne $_.TargetObject) { $_.TargetObject.GetType().FullName + ' :: ' + [string]$_.TargetObject } else { 'null' }))
    Write-Log ("summary: exception stack " + $_.Exception.StackTrace)
    Write-Log ("summary: inner=" + $(if ($null -ne $_.Exception.InnerException) { $_.Exception.InnerException.GetType().FullName + ': ' + $_.Exception.InnerException.Message } else { 'none' }))
    # 兜底：至少写出聚合需要的 pass/fail/skip + results（并把写盘失败显式记账）。
    # 退出码仍为 1（见文末），不把"摘要写盘失败"伪装成通过。
    try {
        $fallback = New-Object System.Collections.Specialized.OrderedDictionary
        $fallback['scenario'] = "$scenarioId (summary-write-fallback)"
        $fallback['summary_error'] = $summaryWriteError
        $fallback['finished_at'] = (Get-Date).ToUniversalTime().ToString('o')
        $fallback['pass'] = $passCount
        $fallback['fail'] = $failCount
        $fallback['skip'] = $skipCount
        $fallback['results'] = $script:results
        $fallback['skipped'] = $script:skips
        [System.IO.File]::WriteAllText($summaryPath, [string]($fallback | ConvertTo-Json -Depth 8), (New-Object System.Text.UTF8Encoding($false)))
        Write-Log ("summary: 已写兜底摘要（{0} 条结果 + summary_error）" -f $script:results.Count)
    } catch {
        Write-Log ("summary: 兜底摘要同样写盘失败 {0}: {1}" -f $_.Exception.GetType().FullName, $_.Exception.Message)
    }
}

Write-Host ''
Write-Log ("总结: PASS={0} FAIL={1} SKIP={2}；证据目录 {3}" -f $passCount, $failCount, $skipCount, $ArtifactDir)
$script:results | Format-Table -Property name, passed -AutoSize | Out-String | Write-Host
foreach ($r in $script:results) {
    if (-not $r.passed) { Write-Log ("FAIL 明细: {0} :: {1}" -f $r.name, $r.detail) }
}
foreach ($s in $script:skips) { Write-Log ("SKIP 明细: {0} :: {1}" -f $s.name, $s.reason) }

if (($failCount -gt 0) -or (-not [string]::IsNullOrWhiteSpace($summaryWriteError))) { exit 1 }
exit 0
