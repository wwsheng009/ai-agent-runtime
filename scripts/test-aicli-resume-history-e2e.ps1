#requires -Version 7
<#
.SYNOPSIS
  E2E：resume 之后**整份** transcript 必须交付到原生 scrollback（历史尾部不得缺失）。

.DESCRIPTION
  场景：docs/e2e/debug-guide.md 的手工复现路径（独立进程 + /debug/* + 读屏），断言
  对象是 history-effects 交付闭环，而不是「pending=0」这种在事故里同时成立的假象：
  live 事故（session_20260924072950_ltYRU9tG）resume 之后 next=1288 / acked=322 /
  pending=0 / plan_incomplete=true / plan_stalled=true / cell_rows_misses=59，执行器
  空闲，缺失的尾部再也不会被规划（注入 /status 也不自愈）。

  两种模式（同一被测命令，取决于 stdout 是否可判定为终端）：
    * 渲染器模式（默认）：不重定向 stdout → 子进程 attach 渲染器 →
      app_state.available=true，断言 A1..A5 的 history-effects 终态与布局缓存。
    * 行模式（-RedirectTerminalStream）：重定向 stdout/stderr 到证据目录 →
      term.IsTerminal(stdout)=false → 不 attach 渲染器（app_state.available=false +
      reason 非空，这是**契约**不是降级），换来终端字节流；断言 A6 的读屏 needle
      真的出现在写往终端的流里。

  断言（fail-closed，任一不成立即 FAIL）：
    A1 计划完整：history_effects.plan_incomplete=false
    A2 续跑未死锁：history_effects.plan_stalled=false
    A3 队列排空：pending=0 且 in-flight=0
    A4 每个 cell 都有已交付提交：acked >= scene.cells（事故为 322/6647）
    A5 整份 transcript 都被规划/走过：cell_rows_misses 与 plan_misses 都 >= 90% cells
       （事故冻结在 59 / 1261）
    A6 尾部真的到了终端：读屏尾部 needle 出现在证据流（行模式）或屏幕尾部非空（渲染器模式）
    A7 优雅退出：POST /web/api/input {"prompt":"/exit"} 后进程 exit code 0

.EXAMPLE
  pwsh -NoProfile -File scripts/test-aicli-resume-history-e2e.ps1
  pwsh -NoProfile -File scripts/test-aicli-resume-history-e2e.ps1 -RedirectTerminalStream
#>
[CmdletBinding()]
param(
  [string]$ExePath = 'backend/.tmp/aicli-resume-history-e2e.exe',
  [string]$SessionId = 'session_20260924072950_ltYRU9tG',
  [int]$Port = 0,
  [int]$StartupTimeoutSec = 120,
  [int]$ConvergeTimeoutSec = 420,
  [int]$ExitTimeoutSec = 60,
  [string]$ArtifactDir = '',
  [switch]$SkipBuild,
  [switch]$RedirectTerminalStream,
  [switch]$KeepAlive
)

$ErrorActionPreference = 'Stop'
$repoRoot = Split-Path -Parent $PSScriptRoot
$stamp = Get-Date -Format 'yyyyMMdd-HHmmss'
if ([string]::IsNullOrWhiteSpace($ArtifactDir)) {
  $ArtifactDir = Join-Path $repoRoot "artifacts/aicli-resume-history-e2e/$stamp"
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
$launchArgs = @('resume', $SessionId, '--yolo', '--pprof', '--debug', '--web-port', "$Port")
Write-Log "start: $ExePath $($launchArgs -join ' ') (redirect-stream=$($RedirectTerminalStream.IsPresent))"
# 两种模式都不加 -NoNewWindow：子进程要有自己的控制台，否则 stdin 立刻 EOF，进程会在
# 恢复历史之后自行退出（exit 0），读不到收敛后的终态。与
# scripts/test-aicli-debug-endpoints-e2e.ps1 的启动方式一致。
if ($RedirectTerminalStream) {
  $proc = Start-Process -FilePath $ExePath -ArgumentList $launchArgs -WorkingDirectory $repoRoot -PassThru `
    -RedirectStandardOutput $stdoutLog -RedirectStandardError $stderrLog
} else {
  $proc = Start-Process -FilePath $ExePath -ArgumentList $launchArgs -WorkingDirectory $repoRoot -PassThru
}

function Get-Status { Invoke-RestMethod "$base/debug/chat/status" -TimeoutSec 120 }
function Get-ScreenTail([int]$Tail) { Invoke-RestMethod "$base/web/api/screen?view=tui&tail=$Tail" -TimeoutSec 120 }
# app_state.history_effects 是**预渲染字符串**（"pending=0 in-flight=0 acked=N ... next=N
# epoch=N"），不是对象；history_gates / layout_cache 才是对象。解析成字段再断言。
function ConvertFrom-HistoryEffects([string]$Text) {
  $map = @{}
  foreach ($m in [regex]::Matches([string]$Text, '([a-z\-]+)=([0-9]+|true|false)')) {
    $map[$m.Groups[1].Value] = $m.Groups[2].Value
  }
  return $map
}

try {
  $deadline = (Get-Date).AddSeconds($StartupTimeoutSec)
  $ready = $false
  while ((Get-Date) -lt $deadline) {
    if ($proc.HasExited) { throw "process exited during startup (code $($proc.ExitCode)); see $stderrLog" }
    try {
      $ep = Invoke-RestMethod "$base/debug/endpoints" -TimeoutSec 20
      if ($ep.available) { $ready = $true; break }
    } catch { Start-Sleep -Milliseconds 500 }
  }
  if (-not $ready) { throw "debug endpoints not available within ${StartupTimeoutSec}s" }
  Write-Log "endpoints ready (listen_mode=$($ep.listen_mode))"

  $first = $null
  $last = $null
  $deadline = (Get-Date).AddSeconds($ConvergeTimeoutSec)
  if ($RedirectTerminalStream) {
    # 行模式读不到计数器：等写往终端的字节流连续稳定（恢复写完）为止。
    $stableSize = -1
    $stable = 0
    while ((Get-Date) -lt $deadline) {
      if ($proc.HasExited) { break }
      $size = if (Test-Path $stdoutLog) { (Get-Item $stdoutLog).Length } else { 0 }
      if ($size -gt 0 -and $size -eq $stableSize) { $stable++ } else { $stable = 0 }
      $stableSize = $size
      if ($stable -ge 3) { break }
      Start-Sleep -Seconds 2
    }
    Write-Log "terminal stream stabilized at $stableSize bytes"
  } else {
    $stable = 0
    while ((Get-Date) -lt $deadline) {
      if ($proc.HasExited) { throw "process exited while converging (code $($proc.ExitCode))" }
      $status = Get-Status
      if ($null -eq $first) { $first = $status }
      $last = $status
      $fx = ConvertFrom-HistoryEffects $status.app_state.history_effects
      $cells = [int]$status.scene.cells
      $idle = ($cells -gt 0) -and ($fx['pending'] -eq '0') -and ($fx['in-flight'] -eq '0') -and
        (-not $status.app_state.history_gates.plan_incomplete)
      if ($idle) { $stable++ } else { $stable = 0 }
      if ($stable -ge 3) { break }
      Start-Sleep -Seconds 2
    }
  }

  # ------------------------------------------------------------- 断言 A1..A5
  $screen = Get-ScreenTail 12
  $screenText = if ($screen -is [string]) { $screen } else { $screen.text }
  $needles = @()
  foreach ($line in ($screenText -split "`n")) {
    $clean = ($line -replace '[^\p{L}\p{N} :._/+-]', ' ').Trim()
    if ($clean.Length -ge 12 -and ($clean -match '[A-Za-z]{3,}')) { $needles += $clean }
  }
  $needles = @($needles | Select-Object -Last 4)

  if ($RedirectTerminalStream) {
    $streamSize = if (Test-Path $stdoutLog) { (Get-Item $stdoutLog).Length } else { 0 }
    $stdoutText = if (Test-Path $stdoutLog) { Get-Content -Path $stdoutLog -Raw -Encoding UTF8 } else { '' }
    $hit = $null
    foreach ($needle in $needles) {
      $probe = $needle.Substring(0, [Math]::Min(24, $needle.Length))
      if ($stdoutText -and $stdoutText.Contains($probe)) { $hit = $probe; break }
    }
    $status = Get-Status
    $renderer = [bool]$status.app_state.available
    Add-Result 'A1 no-renderer-contract' ((-not $renderer) -and (-not [string]::IsNullOrWhiteSpace($status.app_state.reason))) `
      "app_state.available=$renderer reason='$($status.app_state.reason)' (documented line-mode contract)"
    Add-Result 'A2 whole-history-streamed' ($streamSize -ge 1048576) `
      "captured terminal stream = $streamSize bytes (the frozen prefix only ever delivered the oldest ~4096 rows)"
    Add-Result 'A3..A5 counters' $true 'not applicable in line mode (no renderer): history-effects counters are unavailable by contract; A6 carries the stream evidence'
    Add-Result 'A6 tail-written-to-terminal' ($null -ne $hit) `
      $(if ($hit) { "screen needle '$hit' found in aicli.stdout.log" } else { "none of the screen tail needles ($($needles -join ' | ')) appear in aicli.stdout.log" })
    $evidence = [pscustomobject]@{
      mode = 'line'; session = $SessionId; base = $base; stream_bytes = $streamSize
      screen_tail = $screenText; needle = $hit; stdout_log = $stdoutLog
    }
  } else {
    if ($null -eq $last) { throw 'no status sample collected' }
    $fx = ConvertFrom-HistoryEffects $last.app_state.history_effects
    $gates = $last.app_state.history_gates
    $cache = $last.app_state.layout_cache
    $cells = [int]$last.scene.cells
    $renderer = [bool]$last.app_state.available
    Write-Log ("mode: renderer=$renderer reason='$($last.app_state.reason)' cells=$cells")
    Write-Log ("final: next=$($fx['next']) acked=$($fx['acked']) pending=$($fx['pending']) in-flight=$($fx['in-flight']) " +
      "failed=$($fx['failed']) invalidated=$($fx['invalidated']) plan_incomplete=$($gates.plan_incomplete) " +
      "plan_stalled=$($gates.plan_stalled) cell_rows_misses=$($cache.cell_rows_misses) " +
      "plan_misses=$($cache.plan_misses) epoch=$($fx['epoch'])")
    Add-Result 'A1 plan-complete' ((-not [bool]$gates.plan_incomplete) -and $renderer) `
      "plan_incomplete=$($gates.plan_incomplete) renderer=$renderer (a truncated plan leaves the tail of the transcript unplanned forever)"
    Add-Result 'A2 continuation-alive' (-not [bool]$gates.plan_stalled) `
      "plan_stalled=$($gates.plan_stalled) (stalled disarms the executor continuation kick)"
    Add-Result 'A3 queue-drained' (($fx['pending'] -eq '0') -and ($fx['in-flight'] -eq '0')) `
      "pending=$($fx['pending']) in-flight=$($fx['in-flight'])"
    $acked = [int]$fx['acked']
    Add-Result 'A4 every-cell-delivered' (($cells -gt 0) -and ($acked -ge $cells)) `
      "acked=$acked cells=$cells (live incident: 322 acked for 6647 cells — the oldest prefix only)"
    $needWalk = [int][Math]::Ceiling($cells * 0.9)
    Add-Result 'A5 whole-transcript-planned' (([int]$cache.cell_rows_misses -ge $needWalk) -and ([int]$cache.plan_misses -ge $needWalk)) `
      "cell_rows_misses=$($cache.cell_rows_misses) plan_misses=$($cache.plan_misses) want >= $needWalk (live incident froze at 59 misses / 1261 plan_misses)"
    Add-Result 'A6 screen-tail-present' ($needles.Count -gt 0) `
      $(if ($needles.Count -gt 0) { "screen tail carries transcript content: '$($needles[-1])'" } else { 'screen tail has no readable transcript content' })
    $evidence = [pscustomobject]@{
      mode = 'renderer'; session = $SessionId; base = $base; scene_cells = $cells
      first_history = $first.app_state.history_effects; final_history = $last.app_state.history_effects
      final_gates = $gates; layout_cache = $cache; screen_tail = $screenText
    }
  }
  $evidence | ConvertTo-Json -Depth 6 | Set-Content -Path (Join-Path $ArtifactDir 'evidence.json') -Encoding UTF8
} catch {
  Add-Result 'E2E-run' $false $_.Exception.Message
} finally {
  if (-not $KeepAlive -and $proc -and -not $proc.HasExited) {
    try {
      $body = '{"prompt":"/exit"}'
      Invoke-WebRequest -Uri "$base/web/api/input" -Method POST `
        -Body ([System.Text.Encoding]::UTF8.GetBytes($body)) `
        -ContentType 'application/json; charset=utf-8' -UseBasicParsing -TimeoutSec 30 | Out-Null
      $exited = $proc.WaitForExit($ExitTimeoutSec * 1000)
      Add-Result 'A7 graceful-exit' ($exited -and $proc.ExitCode -eq 0) `
        "exited=$exited exit_code=$(if ($exited) { $proc.ExitCode } else { 'n/a' })"
    } catch {
      Add-Result 'A7 graceful-exit' $false $_.Exception.Message
    }
  }
}

$failed = @($results | Where-Object { $_.status -eq 'FAIL' })
$summary = [pscustomobject]@{
  scenario   = 'aicli-resume-history'
  session    = $SessionId
  artifact   = $ArtifactDir
  started_at = $stamp
  results    = $results
  failed     = $failed.Count
}
$summary | ConvertTo-Json -Depth 6 | Set-Content -Path (Join-Path $ArtifactDir 'summary.json') -Encoding UTF8
Write-Log ("SUMMARY: {0} passed / {1} failed — {2}" -f ($results.Count - $failed.Count), $failed.Count, $ArtifactDir)
if ($failed.Count -gt 0) { exit 1 }
exit 0
