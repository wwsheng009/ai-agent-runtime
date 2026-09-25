#requires -Version 7
<#
.SYNOPSIS
  E2E：resume 启动性能 —— 「卡住很久才进入恢复」的可观测判据。

.DESCRIPTION
  场景（docs/e2e/debug-guide.md 方法论）：独立进程 + /debug/* 采样 + 启动阶段打点，
  回答的是**时间维度**的问题，与 E2E-RESUME-01（终态维度：尾部是否交付完整）互补。

  被测抱怨：`aicli resume <大会话>` 启动后长时间静默，很久才「进入恢复」。

  三个互不依赖的观测通道：
    1. 启动阶段打点（AICLI_STARTUP_TIMING=1 → stderr）：
       `aicli chat startup timing: profile=+.. persistence=+.. resume_metadata=+..
        resume_history=+.. capabilities_*=+.. ready=+.. (total ..)`
       给出「进程启动 → composer 就绪」的逐阶段耗时。
    2. 执行器恢复循环（GET /debug/pprof/executor）：每次 recovery 迭代都带
       atUnixMs，**迭代间隔**就是「恢复是否在推进」的直接证据；一条巨大的
       间隔 = 一次卡住。totalRecoveries 单调，可判「是否进入过恢复」。
    3. 交付计数时间线（GET /debug/chat/status?fast=1，每 -SampleIntervalMs 一帧）：
       cells/next/acked/pending/plan_incomplete 的推进轨迹，用于区分
       「增量交付」与「最后一次性倾倒」。

  断言（fail-closed；P4 是唯一的时长上界，其余是结构/次序判据）：
    P1 启动打点可用：stderr 出现 `aicli chat startup timing:`，marks 含 ready，
       且 elapsed 单调不减（阶段次序未乱）
    P2 恢复先于计划完成：首次 recovery 迭代时间戳 < plan_incomplete 翻假的时间
       （证明恢复与规划交错，而不是「整份计划算完才开始写」）
    P3 增量交付：acked 严格递增的采样帧数 >= -MinProgressSamples
    P4 无静默窗口：执行器相邻 recovery 迭代的最大间隔 <= -SilentWindowSec
    P5 无启动挂起 dump：stderr 不含 `chat startup stalled`（内建 90s watchdog）
    P6 收敛：plan_incomplete=false 且 plan_stalled=false 且 pending=0 且
       in-flight=0 且 acked >= cells
    P7 优雅退出：POST /web/api/input {"prompt":"/exit"} 后 exit code 0
    P8 启动打点已开启：stderr 出现 timing 行（证明 AICLI_STARTUP_TIMING 生效）
    P9 首个内容预算：进程启动 -> 第一行历史真正交付到终端 <= FirstContentBudgetMs
    P10 交付单调：acked 从不回退（回退 = 计划被重置、已交付的行被重投一遍）
    P11 重放规模预算：replayed 记录数 <= ReplayRecordBudgetFactor x scene.cells
    P12 UI 端点不冻结：/debug/chat/status（与 TUI 渲染帧共用 UIController 锁）在
        恢复期间的「最长单次冻结」与「冻结累计」都不超过预算 —— 这是「卡住」的直接断言
        （冻结时长取窗口内最大单次探针时延；窗口 span 含采样间隔与取证等待，只列不判定）
    P13 卡顿取证：一旦出现卡顿窗口，artifact 里必须有 goroutine dump（含持锁者）
    P14 重放解析预算：eventlog_parse 阶段耗时 <= ReplayParseBudgetMs
    P15 重放应用预算：eventlog_apply 阶段耗时 <= ReplayApplyBudgetMs
    P16 规划归因：跨帧最大单次 transcript 规划耗时 <= PlanBudgetMs（P12 的归因：
        端点冻结若与一次长规划重合，就该修规划器；未观测到规划计时则判 FAIL，
        避免把「没测到」当成「很快」）

  采样传输用 curl.exe（-m 外部硬超时）。端点被锁卡住时探针会拿到 exit=28 而不是
  自己被挂死；卡顿窗口会被记录进 timeline.jsonl 的 status_ms/status_exit，
  并触发 goroutine-stall-*.txt / screen-stall-*.json 取证。

.EXAMPLE
  pwsh -NoProfile -File scripts/test-aicli-resume-startup-perf-e2e.ps1
  pwsh -NoProfile -File scripts/test-aicli-resume-startup-perf-e2e.ps1 -CpuProfileSeconds 40
#>
[CmdletBinding()]
param(
  [string]$ExePath = 'backend/.tmp/aicli-resume-startup-perf-e2e.exe',
  [string]$SessionId = 'session_20260924072950_ltYRU9tG',
  [int]$Port = 0,
  [int]$StartupTimeoutSec = 180,
  [int]$ConvergeTimeoutSec = 420,
  [int]$ExitTimeoutSec = 60,
  [int]$SampleIntervalMs = 500,
  # 执行器静默窗口：行仍待交付时，相邻 recovery 迭代之间不允许超过这个间隔。
  # 45s 是「挂起」量级，抓不到用户报告的秒级冻结；6s 是「卡住」量级。
  [int]$SilentWindowSec = 6,
  [int]$MinProgressSamples = 3,
  # 「进入恢复」的预算：进程启动 -> composer ready（含事件日志重放/首帧 seed）。
  [int]$ReadyBudgetMs = 3000,
  # 进程启动 -> 第一行历史真正交付到终端（用户第一次看到内容）。
  [int]$FirstContentBudgetMs = 8000,
  # 重放记录数 / Scene cell 数的上限：重放规模必须随转录规模增长，
  # 而不是随 runtime 遥测（reasoning/tool.progress/llm.request）体积增长。
  [int]$ReplayRecordBudgetFactor = 4,
  # UI/debug 端点冻结预算。/debug/chat/status 与 TUI 渲染帧、web 客户端读的是
  # **同一把** UIController 锁（ui.(*UIController).State → c.mu），所以它的时延
  # 就是「用户此刻按键盘有没有回显、屏幕有没有刷新」的代理指标：
  #   单次卡顿窗口 > UiStallWindowBudgetMs，或整轮累计卡顿 > UiStallTotalBudgetMs
  # 就是用户报告的「卡住」。
  [int]$StatusTimeoutSec = 3,
  [double]$UiStallSec = 1.0,
  [int]$UiStallWindowBudgetMs = 2000,
  [int]$UiStallTotalBudgetMs = 4000,
  # P14/P15：恢复重放两段的独立预算（L1.1 便宜解析的收益门禁）。
  # 基线（全量 json.Unmarshal）：parse ~3.0s / apply ~3.4s；裁剪后按比例收紧。
  [int]$ReplayParseBudgetMs = 1500,
  [int]$ReplayApplyBudgetMs = 1800,
  # P16：单次规划预算（规划器 screening + 铸 commit）。它与 P12 配对使用：
  # P12 断言「用户被冻结」，P16 断言「冻结不是规划造成的」。
  [int]$PlanBudgetMs = 250,
  [int]$CpuProfileSeconds = 0,
  [string]$ArtifactDir = '',
  [switch]$SkipBuild,
  [switch]$KeepAlive,
  # 测量前置条件：空闲物理内存下限（MB）。低于此值说明系统已在换页，
  # ready/parse/apply 会全线虚高（2026-09-25 实测 1.8~2.3 倍）。
  [int]$MinFreeMemoryMb = 6000,
  # 只为功能排查、明确接受「数据不可用于性能归因」时跳过内存断言。
  [switch]$AllowLowMemory
)

$ErrorActionPreference = 'Stop'
$repoRoot = Split-Path -Parent $PSScriptRoot
$stamp = Get-Date -Format 'yyyyMMdd-HHmmss'
if ([string]::IsNullOrWhiteSpace($ArtifactDir)) {
  $ArtifactDir = Join-Path $repoRoot "artifacts/aicli-resume-startup-perf-e2e/$stamp"
} elseif (-not [System.IO.Path]::IsPathRooted($ArtifactDir)) {
  $ArtifactDir = Join-Path $repoRoot $ArtifactDir
}
New-Item -ItemType Directory -Force -Path $ArtifactDir | Out-Null
if (-not [System.IO.Path]::IsPathRooted($ExePath)) { $ExePath = Join-Path $repoRoot $ExePath }

$runLog = Join-Path $ArtifactDir 'run.log'
$results = [System.Collections.Generic.List[object]]::new()
function Write-Log([string]$Message) {
  $line = "[{0}] {1}" -f (Get-Date -Format 'HH:mm:ss.fff'), $Message
  Write-Host $line
  Add-Content -Path $runLog -Value $line
}
function Add-Result([string]$Name, [bool]$Pass, [string]$Detail) {
  $results.Add([pscustomobject]@{ name = $Name; status = $(if ($Pass) { 'PASS' } else { 'FAIL' }); detail = $Detail })
  Write-Log ("{0} {1} — {2}" -f $(if ($Pass) { 'PASS' } else { 'FAIL' }), $Name, $Detail)
}

# ------------------------------------------------- 环境前置条件（fail-closed）
# 教训（2026-09-25 M1 A/B，见 docs/e2e/resume-m1-ab-measurement.md §3）：14GB 机器只剩
# 3.1GB 空闲且有其它 agent 并行时，同一份代码的三次运行互相矛盾（resume_metadata 在
# 135ms 与 4035ms 之间摆动；PowerShell 自身报 0x800705AF；aicli 实例中途 exit=2）。
# 这种数据不能用于归因，因此默认在低内存时直接失败，并把环境指纹写进 artifact。
$osInfo = Get-CimInstance Win32_OperatingSystem
$freeMb = [int]($osInfo.FreePhysicalMemory / 1KB)
$totalMb = [int]($osInfo.TotalVisibleMemorySize / 1KB)
$siblingAgents = @(Get-Process -Name 'aicli*' -ErrorAction SilentlyContinue |
  Where-Object { $_.Id -ne $PID -and $_.ProcessName -notlike 'aicli-resume-startup-perf-e2e*' })
$sibDesc = if ($siblingAgents.Count -gt 0) { ($siblingAgents.ProcessName | Sort-Object -Unique) -join ',' } else { 'none' }
[pscustomobject]@{
  preflight_at     = (Get-Date -Format 'o')
  free_mb          = $freeMb
  total_mb         = $totalMb
  min_free_mb      = $MinFreeMemoryMb
  sibling_agents   = $siblingAgents.Count
  sibling_pids     = @($siblingAgents.Id)
  sibling_names    = $sibDesc
  allow_low_memory = [bool]$AllowLowMemory
} | ConvertTo-Json -Depth 4 | Set-Content -Path (Join-Path $ArtifactDir 'preflight.json') -Encoding UTF8
Write-Log ("preflight: free={0}MB/{1}MB sibling-aicli={2} [{3}]" -f $freeMb, $totalMb, $siblingAgents.Count, $sibDesc)
if (-not $AllowLowMemory -and $freeMb -lt $MinFreeMemoryMb) {
  $msg = "preflight: 空闲物理内存 ${freeMb}MB < ${MinFreeMemoryMb}MB —— 系统在换页，本次数据不可用于性能归因；" +
  "清理后重跑，或仅为功能排查时用 -AllowLowMemory 显式接受（见 docs/e2e/resume-m1-ab-measurement.md §3/§4）"
  throw $msg
}
if ($siblingAgents.Count -gt 0) {
  Write-Log ("WARN preflight: 检测到 {0} 个其它 aicli 进程 [{1}]，性能数字可能被污染" -f $siblingAgents.Count, $sibDesc)
}

# ---------------------------------------------------------------- 构建 + 启动
if (-not $SkipBuild) {
  Write-Log "go build -> $ExePath"
  Push-Location (Join-Path $repoRoot 'backend')
  try {
    & go build -o $ExePath ./cmd/aicli 2>&1 | Tee-Object -FilePath (Join-Path $ArtifactDir 'go-build.log')
    if ($LASTEXITCODE -ne 0) { throw "go build failed ($LASTEXITCODE)" }
  } finally { Pop-Location }
}
if (-not (Test-Path $ExePath)) { throw "exe not found: $ExePath" }

if ($Port -le 0) {
  $listener = [System.Net.Sockets.TcpListener]::new([System.Net.IPAddress]::Loopback, 0)
  $listener.Start(); $Port = $listener.LocalEndpoint.Port; $listener.Stop()
}
$base = "http://127.0.0.1:$Port"
$stdoutLog = Join-Path $ArtifactDir 'aicli.stdout.log'
$stderrLog = Join-Path $ArtifactDir 'aicli.stderr.log'
$timelinePath = Join-Path $ArtifactDir 'timeline.jsonl'
$launchArgs = @('resume', $SessionId, '--yolo', '--pprof', '--debug', '--web-port', "$Port")
Write-Log "start: $ExePath $($launchArgs -join ' ') (AICLI_STARTUP_TIMING=1, redirect stderr)"

# 子进程要有自己的控制台（不加 -NoNewWindow），否则 stdin 立刻 EOF；stderr 重定向到
# 证据文件以捕获启动打点与 watchdog dump（stdout 仍是终端 → 渲染器照常 attach）。
$previousTiming = $env:AICLI_STARTUP_TIMING
$env:AICLI_STARTUP_TIMING = '1'
try {
  $proc = Start-Process -FilePath $ExePath -ArgumentList $launchArgs -WorkingDirectory $repoRoot -PassThru `
    -RedirectStandardError $stderrLog
} finally {
  if ($null -eq $previousTiming) { Remove-Item Env:\AICLI_STARTUP_TIMING -ErrorAction SilentlyContinue }
  else { $env:AICLI_STARTUP_TIMING = $previousTiming }
}
$t0 = Get-Date
$startEpochMs = [DateTimeOffset]::new($proc.StartTime.ToUniversalTime()).ToUnixTimeMilliseconds()

# HTTP 传输用 curl.exe：`-m` 是**外部硬超时**（连接 + 读取都算），端点被锁卡住时
# 一定会在预算内返回 exit=28，而不是把探针自己挂死（PowerShell 的 -TimeoutSec
# 在响应体停滞时不总是兑现，2026-09-24 实测 harness 曾因此静默 6 分钟）。
# 时延/退出码通过 $script:lastCall* 暴露给采样循环，用来度量「卡住」。
$script:lastCallMs = 0
$script:lastCallExit = 0
function Invoke-Json([string]$Url, [int]$TimeoutSec = 15) {
  $tmp = Join-Path $ArtifactDir 'tmp-http-body.json'
  $sw = [System.Diagnostics.Stopwatch]::StartNew()
  $null = & curl.exe -s -m $TimeoutSec -o $tmp -w '%{http_code}' $Url 2>&1
  $sw.Stop()
  $script:lastCallMs = [Math]::Round($sw.Elapsed.TotalMilliseconds, 1)
  $script:lastCallExit = $LASTEXITCODE
  if ($LASTEXITCODE -ne 0) { return $null }   # 28=超时(卡住) 7=连接被拒(未就绪)
  $text = if (Test-Path $tmp) { Get-Content -Path $tmp -Raw -Encoding UTF8 } else { '' }
  if ([string]::IsNullOrWhiteSpace($text)) { return $null }
  try { return ($text | ConvertFrom-Json) } catch { return $null }
}
# app_state.history_effects 是**预渲染字符串**（"pending=0 in-flight=0 acked=N ... next=N"），
# 不是对象；history_gates / layout_cache 才是对象。与 E2E-RESUME-01 同一套解析。
function ConvertFrom-HistoryEffects([string]$Text) {
  $map = @{}
  foreach ($m in [regex]::Matches([string]$Text, '([a-z\-]+)=([0-9]+|true|false)')) {
    $map[$m.Groups[1].Value] = $m.Groups[2].Value
  }
  return $map
}
function ConvertFrom-GoDuration([string]$Text) {
  if ([string]::IsNullOrWhiteSpace($Text)) { return $null }
  $total = 0.0
  foreach ($m in [regex]::Matches($Text, '(\d+(?:\.\d+)?)(ns|us|µs|ms|s|m|h)')) {
    $v = [double]$m.Groups[1].Value
    switch ($m.Groups[2].Value) {
      'ns' { $total += $v / 1e6 }
      'us' { $total += $v / 1e3 }
      'µs' { $total += $v / 1e3 }
      'ms' { $total += $v }
      's' { $total += $v * 1000 }
      'm' { $total += $v * 60000 }
      'h' { $total += $v * 3600000 }
    }
  }
  return [Math]::Round($total, 1)
}

# ------------------------------------------------------- 采样（从 T0 开始，含就绪前）
$samples = [System.Collections.Generic.List[object]]::new()
$recoveryBySeq = @{}                       # seq -> atUnixMs（跨帧合并，去重）
$ackedAdvanceTimes = [System.Collections.Generic.List[double]]::new()
$ackedResets = [System.Collections.Generic.List[object]]::new()
$prevAcked = -1
$firstCellsMs = $null
$firstAckedMs = $null
$firstRecoveryMs = $null
$planStartedMs = $null
$planCompleteMs = $null
$convergedMs = $null
$endpointsReadyMs = $null
$profileProc = $null
$stable = 0
$converged = $false
$exitDuringRun = $null
$lastStatus = $null
$lastExec = $null
$statusSource = 'none'                      # fast | full | none（记录 ?fast=1 是否够用）
$uiLatencies = [System.Collections.Generic.List[double]]::new()
$stallWindows = [System.Collections.Generic.List[object]]::new()
$openStallWindow = $null
$stallDumps = 0
$maxPlanMs = $null                          # P16：单次规划耗时的全局最大值（跨帧）
$planCount = $null                          # P16：规划 pass 计数（末帧）

$deadline = (Get-Date).AddSeconds($StartupTimeoutSec + $ConvergeTimeoutSec)
while ((Get-Date) -lt $deadline) {
  if ($proc.HasExited) { $exitDuringRun = $proc.ExitCode; break }
  $tMs = [Math]::Round(((Get-Date) - $t0).TotalMilliseconds, 0)

  $status = Invoke-Json "$base/debug/chat/status?fast=1" $StatusTimeoutSec
  $statusMs = $script:lastCallMs
  $statusExit = $script:lastCallExit
  $uiLatencies.Add([double]$statusMs)
  # 「卡住」的代理指标：fast status 超时或超过阈值 = 同一时刻用户按键盘没有回显、
  # TUI 也不刷新（reduce 的长临界区占着 UIController 锁）。
  if ($statusExit -ne 0 -or $statusMs -ge ($UiStallSec * 1000)) {
    if ($null -eq $openStallWindow) {
      # max_ms = 窗口内最大的单次探针时延 = 用户此刻真正感知到的冻结时长；
      # start/end span 只是采样跨度（含采样间隔与取证等待），不能拿来当冻结时长。
      $openStallWindow = [pscustomobject]@{ start_ms = $tMs; end_ms = $tMs; max_ms = 0.0; probes = 0; timeouts = 0 }
      $stallWindows.Add($openStallWindow)
      if ($stallDumps -lt 3) {
        $stallDumps++
        # 卡顿瞬间的持有者证据：goroutine dump 里能看到谁占着 ui.(*UIController) 锁。
        # dump 走 5s 上限：pprof 由独立 handler 提供，不受 UI 锁影响；screen 会受锁影响，
        # 因此窗口时长里可能包含这段取证等待（日志会标注）。
        $null = & curl.exe -s -m 5 -o (Join-Path $ArtifactDir "goroutine-stall-$stallDumps.txt") "$base/debug/pprof/goroutine?debug=2"
        $null = & curl.exe -s -m 5 -o (Join-Path $ArtifactDir "screen-stall-$stallDumps.json") "$base/debug/chat/screen"
        Write-Log "stall window opened at ${tMs}ms (exit=$statusExit lat=${statusMs}ms) -> goroutine-stall-$stallDumps.txt"
      } else {
        Write-Log "stall window opened at ${tMs}ms (exit=$statusExit lat=${statusMs}ms)"
      }
    }
    $openStallWindow.end_ms = $tMs
    if ($statusMs -gt $openStallWindow.max_ms) { $openStallWindow.max_ms = [double]$statusMs }
    $openStallWindow.probes++
    if ($statusExit -ne 0) { $openStallWindow.timeouts++ }
  } elseif ($null -ne $openStallWindow) {
    # 关闭帧也必须推进 end_ms，否则 span 退化成「窗口只有开帧」= 0ms（曾把 3s 冻结判成 PASS）。
    $openStallWindow.end_ms = $tMs
    Write-Log ("stall window closed: {0}->{1} ms (span={2} ms, freeze={3} ms, probes={4}, timeouts={5})" -f $openStallWindow.start_ms, $openStallWindow.end_ms, ($openStallWindow.end_ms - $openStallWindow.start_ms), [int64][Math]::Round($openStallWindow.max_ms), $openStallWindow.probes, $openStallWindow.timeouts)
    $openStallWindow = $null
  }
  if ($null -ne $status) {
    if ($statusSource -eq 'none') { $statusSource = 'fast' }
    # ?fast=1 跳过的重区块若包含 app_state，则退回默认（有预算的）端点一次。
    if ($null -eq $status.app_state.history_effects -and $statusSource -eq 'fast') {
      $status = Invoke-Json "$base/debug/chat/status" 20
      $uiLatencies.Add([double]$script:lastCallMs)
      if ($null -ne $status -and $null -ne $status.app_state.history_effects) { $statusSource = 'full' }
    }
    if ($null -eq $endpointsReadyMs) { $endpointsReadyMs = $tMs }
    $lastStatus = $status
    if ($CpuProfileSeconds -gt 0 -and $null -eq $profileProc) {
      $profilePath = Join-Path $ArtifactDir 'cpu.pprof'
      try {
        $profileProc = Start-Process -FilePath 'curl.exe' -PassThru -NoNewWindow `
          -ArgumentList @('-sS', '-o', $profilePath, "$base/debug/pprof/profile?seconds=$CpuProfileSeconds")
        Write-Log "cpu profile started (${CpuProfileSeconds}s) -> $profilePath"
      } catch { Write-Log "cpu profile start failed (continuing): $($_.Exception.Message)" }
    }
  }

  $exec = Invoke-Json "$base/debug/pprof/executor" 8
  $execMs = $script:lastCallMs
  if ($null -ne $exec) { $lastExec = $exec }

  $fx = $null; $gates = $null; $cache = $null; $cells = 0; $acked = 0; $next = 0
  if ($null -ne $status -and $null -ne $status.app_state.history_effects) {
    $fx = ConvertFrom-HistoryEffects $status.app_state.history_effects
    $gates = $status.app_state.history_gates
    $cache = $status.app_state.layout_cache
    $cells = [int]$status.scene.cells
    if ($fx.ContainsKey('acked')) { $acked = [int]$fx['acked'] }
    if ($fx.ContainsKey('next')) { $next = [int]$fx['next'] }
  }
  if ($cells -gt 0 -and $null -eq $firstCellsMs) { $firstCellsMs = $tMs }
  if ($acked -gt 0 -and $null -eq $firstAckedMs) { $firstAckedMs = $tMs }
  # P16：规划器耗时归因（plan-max-ms 是自进程启动以来的单次最大值，取跨帧最大）。
  if ($fx -and $fx.ContainsKey('plan-max-ms')) {
    $framePlanMax = [int]$fx['plan-max-ms']
    if ($null -eq $maxPlanMs -or $framePlanMax -gt $maxPlanMs) { $maxPlanMs = $framePlanMax }
  }
  if ($fx -and $fx.ContainsKey('plan-count')) { $planCount = [int]$fx['plan-count'] }
  if ($acked -gt $prevAcked -and $prevAcked -ge 0) { $ackedAdvanceTimes.Add($tMs) }
  # acked 回退 = 计划被重置：已经交付的行要重新投递一遍，用户看到历史被重放
  # 第二遍，且重置前后执行器不再推进（静默窗口）。
  if ($acked -lt $prevAcked -and $prevAcked -gt 0) {
    $ackedResets.Add([pscustomobject]@{ t_ms = $tMs; from = $prevAcked; to = $acked })
  }
  if ($acked -gt $prevAcked) { $prevAcked = $acked }

  $totalRecoveries = 0; $newestEntryAt = $null; $windowAgeMs = $null; $windowDiag = ''
  if ($null -ne $exec) {
    $totalRecoveries = [int]$exec.totalRecoveries
    if ($null -ne $exec.entries) {
      foreach ($entry in $exec.entries) {
        if ($null -ne $entry.seq -and -not $recoveryBySeq.ContainsKey([string]$entry.seq)) {
          $recoveryBySeq[[string]$entry.seq] = [int64]$entry.atUnixMs
        }
      }
    }
    $windowAgeMs = $exec.windowAgeMs
    $windowDiag = $exec.windowDiagnosis
  }
  if ($null -eq $firstRecoveryMs -and $recoveryBySeq.Count -gt 0) {
    $earliest = ($recoveryBySeq.Values | Measure-Object -Minimum).Minimum
    $firstRecoveryMs = [Math]::Round([double]$earliest - $startEpochMs, 0)
  }
  # 空 Scene 上 plan_incomplete 恒为 false，因此「翻假」只有相对「首次为真」
  # 才有意义；并且只有 pending/in-flight 都归零才算计划真正落定。
  if ($null -ne $gates) {
    if ([bool]$gates.plan_incomplete -and $null -eq $planStartedMs) { $planStartedMs = $tMs }
    if ($null -ne $planStartedMs -and $null -eq $planCompleteMs -and
      (-not [bool]$gates.plan_incomplete) -and $null -ne $fx -and
      $fx['pending'] -eq '0' -and $fx['in-flight'] -eq '0') {
      $planCompleteMs = $tMs
    }
  }

  $sample = [pscustomobject]@{
    t_ms = $tMs; http_ok = ($null -ne $status); available = $(if ($null -ne $status) { [bool]$status.app_state.available } else { $null })
    status_ms = $statusMs; status_exit = $statusExit; exec_ms = $execMs
    cells = $cells; next = $next; acked = $acked
    pending = $(if ($fx) { $fx['pending'] } else { $null }); in_flight = $(if ($fx) { $fx['in-flight'] } else { $null })
    failed = $(if ($fx) { $fx['failed'] } else { $null })
    plan_incomplete = $(if ($gates) { [bool]$gates.plan_incomplete } else { $null })
    plan_stalled = $(if ($gates) { [bool]$gates.plan_stalled } else { $null })
    cell_rows_misses = $(if ($cache) { [int]$cache.cell_rows_misses } else { $null })
    plan_misses = $(if ($cache) { [int]$cache.plan_misses } else { $null })
    plan_max_ms = $maxPlanMs; plan_count = $planCount
    total_recoveries = $totalRecoveries; recovery_seqs = $recoveryBySeq.Count
    window_age_ms = $windowAgeMs; window_diagnosis = $windowDiag
  }
  $samples.Add($sample)
  Add-Content -Path $timelinePath -Value ($sample | ConvertTo-Json -Compress) -Encoding UTF8

  # 收敛判据与 E2E-RESUME-01 一致：idle 不是 pending=0，而是计划完整 + 队列排空 + 交付覆盖。
  if ($cells -gt 0 -and $fx -and $gates -and
    (-not [bool]$gates.plan_incomplete) -and (-not [bool]$gates.plan_stalled) -and
    $fx['pending'] -eq '0' -and $fx['in-flight'] -eq '0' -and $acked -ge $cells) {
    $stable++
  } else { $stable = 0 }
  if ($stable -ge 3) { $converged = $true; $convergedMs = $tMs; break }

  Start-Sleep -Milliseconds $SampleIntervalMs
}
Write-Log ("sampling done: frames=$($samples.Count) converged=$converged exit_during_run=$exitDuringRun status_source=$statusSource")

# ------------------------------------------------- 事件日志重放规模（恢复成本源头）
# 「进入恢复」的启动关键路径成本 ≈ 事件日志体积：full status 暴露的
# event_log{recorded,replayed} 是「这次恢复到底解析了多少条记录」的权威读数，
# 文件字节数作为交叉校验。字段位置不固定，递归查找 recorded+replayed 对象。
function Find-EventLogInfo($node) {
  if ($null -eq $node) { return $null }
  if ($node -is [System.Management.Automation.PSCustomObject]) {
    $names = @($node.PSObject.Properties.Name)
    if (($names -contains 'replayed') -and ($names -contains 'recorded')) { return $node }
    foreach ($p in $node.PSObject.Properties) {
      $hit = Find-EventLogInfo $p.Value
      if ($null -ne $hit) { return $hit }
    }
    return $null
  }
  if (($node -is [System.Collections.IEnumerable]) -and -not ($node -is [string])) {
    foreach ($item in $node) {
      $hit = Find-EventLogInfo $item
      if ($null -ne $hit) { return $hit }
    }
  }
  return $null
}
$finalFullStatus = $null
try { $finalFullStatus = Invoke-Json "$base/debug/chat/status" 20 } catch { $finalFullStatus = $null }
$eventLogInfo = Find-EventLogInfo $finalFullStatus
$eventLogPath = $null; $eventLogRecorded = $null; $eventLogReplayed = $null; $eventLogBytes = $null
if ($null -ne $eventLogInfo) {
  $eventLogRecorded = [int64]$eventLogInfo.recorded
  $eventLogReplayed = [int64]$eventLogInfo.replayed
  if ($eventLogInfo.path) {
    $eventLogPath = [string]$eventLogInfo.path
    if (Test-Path -LiteralPath $eventLogPath) {
      $eventLogBytes = (Get-Item -LiteralPath $eventLogPath).Length
    }
  }
}
Write-Log ("event log: recorded={0} replayed={1} bytes={2} path={3}" -f $eventLogRecorded, $eventLogReplayed, $eventLogBytes, $eventLogPath)

# ------------------------------------------------------- 启动打点（stderr 通道）
$stderrText = if (Test-Path $stderrLog) { Get-Content -Path $stderrLog -Raw -Encoding UTF8 } else { '' }
$marks = [System.Collections.Generic.List[object]]::new()
$timingLine = $null
foreach ($line in ($stderrText -split "`r?`n")) {
  if ($line -match 'aicli chat startup timing:') { $timingLine = $line }
}
if ($timingLine) {
  $payload = $timingLine -replace '^.*aicli chat startup timing:\s*', ''
  foreach ($m in [regex]::Matches($payload, '([A-Za-z_][A-Za-z0-9_]*)=\+([^\s(]+) \(total ([^)]+)\)')) {
    $marks.Add([pscustomobject]@{
        name     = $m.Groups[1].Value
        delta_ms = ConvertFrom-GoDuration $m.Groups[2].Value
        total_ms = ConvertFrom-GoDuration $m.Groups[3].Value
      })
  }
}
$marksOrdered = $true
for ($i = 1; $i -lt $marks.Count; $i++) { if ($marks[$i].total_ms -lt $marks[$i - 1].total_ms) { $marksOrdered = $false } }
$hasReadyMark = @($marks | Where-Object { $_.name -eq 'ready' }).Count -gt 0

# ------------------------------------------------------- 执行器迭代间隔（恢复推进）
$execGaps = [System.Collections.Generic.List[object]]::new()
$seqList = @($recoveryBySeq.Keys | Sort-Object { [int]$_ })
for ($i = 1; $i -lt $seqList.Count; $i++) {
  $execGaps.Add([pscustomobject]@{
      from_seq = [int]$seqList[$i - 1]; to_seq = [int]$seqList[$i]
      gap_ms   = [int64]$recoveryBySeq[$seqList[$i]] - [int64]$recoveryBySeq[$seqList[$i - 1]]
    })
}
$maxExecGapMs = if ($execGaps.Count -gt 0) { [int64](($execGaps | Measure-Object -Property gap_ms -Maximum).Maximum) } else { $null }
$worstGap = if ($execGaps.Count -gt 0) { $execGaps | Sort-Object -Property gap_ms -Descending | Select-Object -First 1 } else { $null }

# ------------------------------------------------------- 阶段表（性能读数）
$table = [System.Collections.Generic.List[object]]::new()
foreach ($m in $marks) {
  $table.Add([pscustomobject]@{ stage = $m.name; delta_ms = $m.delta_ms; total_ms = $m.total_ms })
}
Write-Log '--- 启动阶段（AICLI_STARTUP_TIMING，stderr）---'
foreach ($row in $table) { Write-Log ("  {0,-28} +{1,8} ms   (total {2,8} ms)" -f $row.stage, $row.delta_ms, $row.total_ms) }
Write-Log '--- 恢复/交付时间线（相对进程启动）---'
Write-Log ("  首个非空 scene.cells     : {0} ms" -f $(if ($null -ne $firstCellsMs) { $firstCellsMs } else { 'n/a' }))
Write-Log ("  首次 acked>0            : {0} ms" -f $(if ($null -ne $firstAckedMs) { $firstAckedMs } else { 'n/a' }))
Write-Log ("  首次 recovery 迭代       : {0} ms" -f $(if ($null -ne $firstRecoveryMs) { $firstRecoveryMs } else { 'n/a' }))
Write-Log ("  计划首次 incomplete      : {0} ms" -f $(if ($null -ne $planStartedMs) { $planStartedMs } else { 'n/a' }))
Write-Log ("  计划落定(pending=0)      : {0} ms" -f $(if ($null -ne $planCompleteMs) { $planCompleteMs } else { 'n/a' }))
Write-Log ("  收敛                     : {0} ms" -f $(if ($null -ne $convergedMs) { $convergedMs } else { 'n/a' }))
Write-Log ("  recovery 迭代数(观测)    : {0} (totalRecoveries={1})" -f $recoveryBySeq.Count, $(if ($lastExec) { $lastExec.totalRecoveries } else { 'n/a' }))
Write-Log ("  相邻迭代最大间隔         : {0} ms{1}" -f $maxExecGapMs, $(if ($worstGap) { " (seq $($worstGap.from_seq)->$($worstGap.to_seq))" } else { '' }))
Write-Log ("  acked 递增帧数           : {0}" -f $ackedAdvanceTimes.Count)
Write-Log ("  acked 回退(计划重置)     : {0} 次{1}" -f $ackedResets.Count, $(if ($ackedResets.Count -gt 0) { ' -> ' + (($ackedResets | ForEach-Object { "$($_.from)->$($_.to)@$($_.t_ms)ms" }) -join ', ') } else { '' }))

# ------------------------------------------------------- UI 端点时延 = 用户感知的「卡住」
if ($null -ne $openStallWindow) {
  Write-Log ("stall window still open at sampling end: {0}->{1} ms" -f $openStallWindow.start_ms, $openStallWindow.end_ms)
}
$uiArr = $uiLatencies.ToArray()
$worstStall = if ($stallWindows.Count -gt 0) { $stallWindows | Sort-Object { $_.max_ms } -Descending | Select-Object -First 1 } else { $null }
$maxStallWindowMs = if ($worstStall) { [int64][Math]::Round($worstStall.max_ms) } else { 0 }
$stallTotalMs = 0; $stallTimeouts = 0; $stallSpanMs = 0
foreach ($w in $stallWindows) { $stallTotalMs += [int64][Math]::Round($w.max_ms); $stallSpanMs += ($w.end_ms - $w.start_ms); $stallTimeouts += $w.timeouts }
$uiSorted = @($uiArr | Sort-Object)
$uiP95 = if ($uiSorted.Count -gt 0) { $uiSorted[[Math]::Min($uiSorted.Count - 1, [int][Math]::Ceiling(0.95 * $uiSorted.Count) - 1)] } else { $null }
$uiMax = if ($uiSorted.Count -gt 0) { $uiSorted[-1] } else { $null }
Write-Log '--- UI/debug 端点时延（= 用户感知的「卡住」）---'
Write-Log ("  fast status 采样数        : {0} (timeout={1})" -f $uiSorted.Count, $stallTimeouts)
Write-Log ("  fast status p95 / max     : {0} ms / {1} ms" -f $uiP95, $uiMax)
Write-Log ("  卡顿窗口                  : {0} 个；最长单次冻结 {1} ms @ {2} ms；冻结累计 {3} ms；窗口跨度累计 {4} ms" -f $stallWindows.Count, $maxStallWindowMs, $(if ($worstStall) { $worstStall.start_ms } else { 'n/a' }), $stallTotalMs, $stallSpanMs)

# ------------------------------------------------------- 断言 P1..P8
try {
  Add-Result 'P1 startup-marks-parsed' (($marks.Count -ge 5) -and $hasReadyMark -and $marksOrdered) `
    "marks=$($marks.Count) ready=$hasReadyMark monotonic=$marksOrdered"
  $readyMark = $marks | Where-Object { $_.name -eq 'ready' } | Select-Object -First 1
  $readyTotalMs = if ($readyMark) { $readyMark.total_ms } else { $null }
  $stageDetail = ($table | Where-Object { $_.stage -in @('resume_history', 'eventlog_read', 'eventlog_parse', 'eventlog_apply', 'history_seed', 'ready') } | ForEach-Object { "$($_.stage)=+$($_.delta_ms)" }) -join ' '
  # P2 是用户报告的直接形式：进程启动到 composer ready 之间不允许有长时间静默。
  Add-Result 'P2 startup-ready-budget' (($null -ne $readyTotalMs) -and ($readyTotalMs -le $ReadyBudgetMs)) `
    "ready=+${readyTotalMs}ms total (budget ${ReadyBudgetMs}ms): $stageDetail"
  Add-Result 'P2b recovery-before-plan-settled' (($null -ne $firstRecoveryMs) -and ($null -ne $planCompleteMs) -and ($firstRecoveryMs -le $planCompleteMs)) `
    "first_recovery=$firstRecoveryMs ms plan_settled=$planCompleteMs ms (recovery must interleave with delivery, not start after the whole plan)"
  Add-Result 'P3 incremental-delivery' ($ackedAdvanceTimes.Count -ge $MinProgressSamples) `
    "acked advanced in $($ackedAdvanceTimes.Count) distinct samples (want >= $MinProgressSamples; a single final dump means history is batched, not streamed)"
  if ($null -eq $maxExecGapMs -or $recoveryBySeq.Count -lt 2) {
    Add-Result 'P4 no-silent-window' $true "not enough recovery iterations observed ($($recoveryBySeq.Count)) — metric reported only: max_gap=$maxExecGapMs ms"
  } else {
    Add-Result 'P4 no-silent-window' ($maxExecGapMs -le ($SilentWindowSec * 1000)) `
      "max gap between consecutive recovery iterations = $maxExecGapMs ms (bound ${SilentWindowSec}s)$(if ($worstGap) { "; worst seq $($worstGap.from_seq)->$($worstGap.to_seq)" })"
  }
  $stallDump = $stderrText -match 'chat startup stalled'
  Add-Result 'P5 no-startup-stall-dump' (-not $stallDump) `
    $(if ($stallDump) { 'stderr carries the built-in 90s startup-stall watchdog dump' } else { 'no 90s startup-stall watchdog dump in stderr' })
  $lastFx = if ($lastStatus) { ConvertFrom-HistoryEffects $lastStatus.app_state.history_effects } else { @{} }
  $lastGates = if ($lastStatus) { $lastStatus.app_state.history_gates } else { $null }
  $lastCells = if ($lastStatus) { [int]$lastStatus.scene.cells } else { 0 }
  $lastAcked = if ($lastFx.ContainsKey('acked')) { [int]$lastFx['acked'] } else { 0 }
  Add-Result 'P6 converged' ($converged -and $lastGates -and (-not [bool]$lastGates.plan_incomplete) -and (-not [bool]$lastGates.plan_stalled) -and $lastFx['pending'] -eq '0' -and $lastFx['in-flight'] -eq '0' -and $lastAcked -ge $lastCells) `
    "converged=$converged plan_incomplete=$($lastGates.plan_incomplete) plan_stalled=$($lastGates.plan_stalled) pending=$($lastFx['pending']) in-flight=$($lastFx['in-flight']) acked=$lastAcked cells=$lastCells"
  $hasReplayMarks = @($marks | Where-Object { $_.name -eq 'eventlog_parse' }).Count -gt 0
  Add-Result 'P8 startup-timing-enabled' (($null -ne $timingLine) -and $hasReplayMarks) `
    $(if ($null -eq $timingLine) { 'no startup timing line — AICLI_STARTUP_TIMING was not honored' } else { "timing line parsed: marks=$($marks.Count) replay/seed-marks=$hasReplayMarks (eventlog_read/parse/apply + history_seed)" })
  Add-Result 'P9 first-content-budget' (($null -ne $firstAckedMs) -and ($firstAckedMs -le $FirstContentBudgetMs)) `
    "first delivered row at $firstAckedMs ms from process start (budget ${FirstContentBudgetMs}ms) — this is the user-visible 'stuck before recovery' duration"
  Add-Result 'P10 delivery-monotonic' ($ackedResets.Count -eq 0) `
    $(if ($ackedResets.Count -eq 0) { 'acked never regressed: no plan reset / no re-delivery' } else { "acked regressed $($ackedResets.Count)x — the plan was reset mid-recovery and already-delivered rows were re-sent: " + (($ackedResets | ForEach-Object { "$($_.from)->$($_.to)@$($_.t_ms)ms" }) -join ', ') })
  $replayBudget = if ($null -ne $lastCells -and $lastCells -gt 0) { [int64]$lastCells * $ReplayRecordBudgetFactor } else { $null }
  Add-Result 'P11 replay-volume-budget' (($null -ne $eventLogReplayed) -and ($null -ne $replayBudget) -and ($eventLogReplayed -le $replayBudget)) `
    "replayed=$eventLogReplayed records of $eventLogRecorded ($eventLogBytes bytes) vs cells=$lastCells (budget ${ReplayRecordBudgetFactor}x = $replayBudget); replay must scale with the transcript, not with runtime telemetry"
  # P12 是「卡住」的直接断言：/debug/chat/status 与 TUI 渲染帧共用 UIController 锁，
  # 端点被锁住多久，用户就多久看不到刷新/回显。
  Add-Result 'P12 ui-endpoint-no-freeze' (($maxStallWindowMs -le $UiStallWindowBudgetMs) -and ($stallTotalMs -le $UiStallTotalBudgetMs)) `
    "longest UI freeze=${maxStallWindowMs}ms (budget ${UiStallWindowBudgetMs}ms) total freeze=${stallTotalMs}ms (budget ${UiStallTotalBudgetMs}ms) windows=$($stallWindows.Count) span=${stallSpanMs}ms timeouts=$stallTimeouts p95=$uiP95 ms max=$uiMax ms over $($uiSorted.Count) probes"
  Add-Result 'P13 stall-diagnostics-captured' (($stallWindows.Count -eq 0) -or ($stallDumps -gt 0)) `
    $(if ($stallWindows.Count -eq 0) { 'no UI stall observed — nothing to dump' } else { "captured $stallDumps goroutine+screen dump(s) at the stall (goroutine-stall-*.txt shows who holds ui.(*UIController) mutex)" })
  # P14/P15：恢复重放两段的独立预算（L1.1 便宜解析后 parse 应显著低于基线；
  # 这两条把「重放裁剪」的收益固化下来，防止回退到全量 json.Unmarshal）。
  $parseMark = $marks | Where-Object { $_.name -eq 'eventlog_parse' } | Select-Object -First 1
  $applyMark = $marks | Where-Object { $_.name -eq 'eventlog_apply' } | Select-Object -First 1
  Add-Result 'P14 replay-parse-budget' (($null -ne $parseMark) -and ($parseMark.delta_ms -le $ReplayParseBudgetMs)) `
    "eventlog_parse=+$(if ($parseMark) { $parseMark.delta_ms } else { 'n/a' })ms (budget ${ReplayParseBudgetMs}ms)"
  Add-Result 'P15 replay-apply-budget' (($null -ne $applyMark) -and ($applyMark.delta_ms -le $ReplayApplyBudgetMs)) `
    "eventlog_apply=+$(if ($applyMark) { $applyMark.delta_ms } else { 'n/a' })ms (budget ${ReplayApplyBudgetMs}ms)"
  # P16 是 P12 的归因断言：端点冻结（用户被卡住）必须能判到「规划器 screening +
  # 铸 commit」这一段；plan-max-ms 无观测（老二进制/无 uiActor）时记 SKIP 语义的
  # 通过说明，不能把「没测到」当成「规划很快」。
  $planDetail = if ($null -eq $maxPlanMs) {
    'planner timing not observed (old binary or no uiActor) — attribution unverified'
  } elseif ($maxPlanMs -le $PlanBudgetMs) {
    'planning did not dominate the freeze window'
  } else {
    'planning is the dominant lock holder — fix the planner, not the layout cache'
  }
  Add-Result 'P16 planner-attribution' (($null -ne $maxPlanMs) -and ($maxPlanMs -le $PlanBudgetMs)) `
    "max single transcript plan=${maxPlanMs}ms (budget ${PlanBudgetMs}ms), plan_count=$planCount — P12 freeze attribution; $planDetail"
} catch {
  Add-Result 'P1..P16' $false $_.Exception.Message
}

# ------------------------------------------------------- 取证 + 收尾
$evidence = [pscustomobject]@{
  scenario = 'aicli-resume-startup-perf'; session = $SessionId; base = $base
  process_start_epoch_ms = $startEpochMs; sample_interval_ms = $SampleIntervalMs
  status_source = $statusSource; frames = $samples.Count
  first_cells_ms = $firstCellsMs; first_acked_ms = $firstAckedMs; first_recovery_ms = $firstRecoveryMs
  plan_complete_ms = $planCompleteMs; converged_ms = $convergedMs
  max_executor_gap_ms = $maxExecGapMs; worst_gap = $worstGap
  recovery_iterations_observed = $recoveryBySeq.Count
  acked_advance_samples = $ackedAdvanceTimes.Count
  plan_started_ms = $planStartedMs
  acked_resets = $ackedResets
  ui_endpoint = [pscustomobject]@{
    probes = $uiSorted.Count; p95_ms = $uiP95; max_ms = $uiMax
    stall_windows = $stallWindows.Count; longest_stall_ms = $maxStallWindowMs
    longest_stall_at_ms = $(if ($worstStall) { $worstStall.start_ms } else { $null })
    stall_total_ms = $stallTotalMs; stall_span_ms = $stallSpanMs; stall_timeouts = $stallTimeouts; dumps = $stallDumps
    stall_threshold_ms = ($UiStallSec * 1000); windows = $stallWindows
  }
  event_log = [pscustomobject]@{ path = $eventLogPath; recorded = $eventLogRecorded; replayed = $eventLogReplayed; bytes = $eventLogBytes }
  plan_attribution = [pscustomobject]@{ plan_max_ms = $maxPlanMs; plan_count = $planCount }
  budgets = [pscustomobject]@{ ready_ms = $ReadyBudgetMs; first_content_ms = $FirstContentBudgetMs; silent_window_ms = ($SilentWindowSec * 1000); replay_records_per_cell = $ReplayRecordBudgetFactor; replay_parse_ms = $ReplayParseBudgetMs; replay_apply_ms = $ReplayApplyBudgetMs; plan_ms = $PlanBudgetMs; ui_stall_window_ms = $UiStallWindowBudgetMs; ui_stall_total_ms = $UiStallTotalBudgetMs }
  startup_marks = $marks; timing_line = $timingLine
  last_status = [pscustomobject]@{
    cells = $lastCells; history_effects = $(if ($lastStatus) { $lastStatus.app_state.history_effects } else { $null })
    gates = $lastGates; layout_cache = $(if ($lastStatus) { $lastStatus.app_state.layout_cache } else { $null })
  }
  executor = $(if ($lastExec) { $lastExec } else { $null })
}
$evidence | ConvertTo-Json -Depth 8 | Set-Content -Path (Join-Path $ArtifactDir 'evidence.json') -Encoding UTF8

if (-not $KeepAlive -and $proc -and -not $proc.HasExited) {
  try {
    $body = '{"prompt":"/exit"}'
    Invoke-WebRequest -Uri "$base/web/api/input" -Method POST `
      -Body ([System.Text.Encoding]::UTF8.GetBytes($body)) `
      -ContentType 'application/json; charset=utf-8' -UseBasicParsing -TimeoutSec 30 | Out-Null
    $exited = $proc.WaitForExit($ExitTimeoutSec * 1000)
    Add-Result 'P7 graceful-exit' ($exited -and $proc.ExitCode -eq 0) `
      "exited=$exited exit_code=$(if ($exited) { $proc.ExitCode } else { 'n/a' })"
  } catch {
    Add-Result 'P7 graceful-exit' $false $_.Exception.Message
  }
} elseif ($exitDuringRun -ne $null) {
  Add-Result 'P7 graceful-exit' $false "process exited during the run (code $exitDuringRun) before /exit"
}
if ($profileProc -and -not $profileProc.HasExited) { $null = $profileProc.WaitForExit(($CpuProfileSeconds + 30) * 1000) }

$failed = @($results | Where-Object { $_.status -eq 'FAIL' })
$summary = [pscustomobject]@{
  scenario = 'aicli-resume-startup-perf'; session = $SessionId; artifact = $ArtifactDir
  started_at = $stamp; sample_interval_ms = $SampleIntervalMs
  frames = $samples.Count; converged_ms = $convergedMs
  first_recovery_ms = $firstRecoveryMs; max_executor_gap_ms = $maxExecGapMs
  plan_max_ms = $maxPlanMs; plan_count = $planCount
  ui_endpoint = [pscustomobject]@{
    probes = $uiSorted.Count; p95_ms = $uiP95; max_ms = $uiMax
    stall_windows = $stallWindows.Count; longest_stall_ms = $maxStallWindowMs
    stall_total_ms = $stallTotalMs; stall_span_ms = $stallSpanMs
    stall_timeouts = $stallTimeouts; dumps = $stallDumps
  }
  results = $results; failed = $failed.Count
}
$summary | ConvertTo-Json -Depth 6 | Set-Content -Path (Join-Path $ArtifactDir 'summary.json') -Encoding UTF8
Write-Log ("SUMMARY: {0} passed / {1} failed — {2}" -f ($results.Count - $failed.Count), $failed.Count, $ArtifactDir)
if ($failed.Count -gt 0) { exit 1 }
exit 0
