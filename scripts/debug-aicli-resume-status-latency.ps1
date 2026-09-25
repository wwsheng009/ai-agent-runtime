#requires -Version 7
<#
诊断工具：量测 resume 期间「UI/debug 端点」的响应时延，把「卡住」变成数字。

背景（2026-09-24 实测）：
  backend/cmd/aicli/ui/controller.go 的 apply 循环里，c.apply() 是**解锁**执行的
  （controller.go:563 Unlock → :566 apply → :569 Lock），但紧接着的
  reduceUIControllerState() 是**持锁**执行的（:578）。而
  reduceUIControllerState → continueTruncatedHistoryPlan → syncHistoryEffectsForTranscriptWithin
  在续跑路径上是**无预算**的（history_effect_planner.go:736-738 明确要求传零值 deadline），
  于是「整份 transcript 的布局 + markdown/chroma 重渲染」会在 c.mu 临界区里跑完。
  c.mu 同时保护 State()（TUI 渲染帧、/debug/chat/status、web 客户端读取），
  所以这段时间里终端看起来就是**卡死**（不渲染、不回显输入）。

本工具用外部观测（curl 硬超时）把这段时延量出来，并在卡顿瞬间抓 goroutine dump，
用于证明「卡住的持有者」是谁（无需改产品代码）。

用法:
  pwsh -NoProfile -File scripts/debug-aicli-resume-status-latency.ps1
  pwsh -NoProfile -File scripts/debug-aicli-resume-status-latency.ps1 -DurationSec 180 -StallSec 1.5
#>
[CmdletBinding()]
param(
  [string]$ExePath = 'backend/.tmp/aicli-resume-startup-perf-e2e.exe',
  [string]$SessionId = 'session_20260924072950_ltYRU9tG',
  [int]$Port = 0,
  [int]$DurationSec = 150,
  [int]$IdleProbeSec = 15,
  [int]$IntervalMs = 200,
  [int]$TimeoutSec = 3,
  [double]$StallSec = 1.0,
  [string]$ArtifactDir = ''
)
$ErrorActionPreference = 'Continue'
$repoRoot = (Resolve-Path (Join-Path $PSScriptRoot '..')).Path
if (-not $ArtifactDir) {
  $ArtifactDir = Join-Path $repoRoot ('artifacts/aicli-resume-status-latency/' + (Get-Date -Format 'yyyyMMdd-HHmmss'))
}
New-Item -ItemType Directory -Force -Path $ArtifactDir | Out-Null
$log = Join-Path $ArtifactDir 'probe.log'
function P([string]$m) {
  $line = "[{0}] {1}" -f (Get-Date -Format 'HH:mm:ss.fff'), $m
  Write-Host $line
  Add-Content -Path $log -Value $line -Encoding UTF8
}
function Curl([string]$url, [int]$maxTimeSec, [string]$outFile = 'NUL') {
  $raw = & curl.exe -s -m $maxTimeSec -o $outFile -w '%{http_code} %{time_total}' $url 2>&1
  [pscustomobject]@{ exit = $LASTEXITCODE; text = ($raw | Out-String).Trim() }
}
function Percentile([double[]]$values, [double]$p) {
  if ($values.Count -eq 0) { return $null }
  $sorted = @($values | Sort-Object)
  $idx = [Math]::Min($sorted.Count - 1, [Math]::Max(0, [int][Math]::Ceiling($p * $sorted.Count) - 1))
  return [Math]::Round($sorted[$idx], 3)
}

if ($Port -le 0) {
  $listener = [System.Net.Sockets.TcpListener]::new([System.Net.IPAddress]::Loopback, 0)
  $listener.Start(); $Port = $listener.LocalEndpoint.Port; $listener.Stop()
}
$exe = Join-Path $repoRoot $ExePath
if (-not (Test-Path $exe)) { throw "exe not found: $exe" }
$stderrLog = Join-Path $ArtifactDir 'aicli.stderr.log'
$timelinePath = Join-Path $ArtifactDir 'timeline.jsonl'
P "artifact: $ArtifactDir"
$env:AICLI_STARTUP_TIMING = '1'
$proc = Start-Process -FilePath $exe -WorkingDirectory $repoRoot -PassThru -RedirectStandardError $stderrLog `
  -ArgumentList @('resume', $SessionId, '--yolo', '--pprof', '--debug', '--web-port', "$Port")
Remove-Item Env:\AICLI_STARTUP_TIMING -ErrorAction SilentlyContinue
$base = "http://127.0.0.1:$Port"
$t0 = Get-Date
P "child pid=$($proc.Id) base=$base interval=${IntervalMs}ms stall_threshold=${StallSec}s"

$lat = [System.Collections.Generic.List[double]]::new()
$stallWindows = [System.Collections.Generic.List[object]]::new()
$openWindow = $null
$timeouts = 0; $dumps = 0; $iter = 0
$lastFullMs = -10000.0
$cells = 0; $acked = 0; $converged = $false; $convergedMs = $null; $firstCellsMs = $null; $firstAckedMs = $null
$stable = 0
$deadline = (Get-Date).AddSeconds($DurationSec)

while ((Get-Date) -lt $deadline) {
  if ($proc.HasExited) { P "child exited code=$($proc.ExitCode)"; break }
  $iter++
  $tMs = [Math]::Round(((Get-Date) - $t0).TotalMilliseconds, 0)
  $r = Curl "$base/debug/chat/status?fast=1" $TimeoutSec
  $sec = if ($r.exit -eq 0) { [double]($r.text -split '\s+')[-1] } else { $null }
  $isStall = ($r.exit -ne 0) -or ($null -ne $sec -and $sec -ge $StallSec)
  if ($r.exit -ne 0) { $timeouts++ }
  if ($null -ne $sec) { $lat.Add($sec) }

  if ($isStall) {
    if ($null -eq $openWindow) {
      $openWindow = [pscustomobject]@{ start_ms = $tMs; end_ms = $tMs; probes = 0; timeouts = 0 }
      $stallWindows.Add($openWindow)
      if ($dumps -lt 3) {
        $dumps++
        $g = Join-Path $ArtifactDir "goroutine-stall-$dumps.txt"
        $null = Curl "$base/debug/pprof/goroutine?debug=2" 20 $g
        $s = Join-Path $ArtifactDir "screen-stall-$dumps.json"
        $null = Curl "$base/debug/chat/screen" 20 $s
        P "stall window opened at ${tMs}ms (exit=$($r.exit) lat=$sec) -> dump #$dumps"
      } else {
        P "stall window opened at ${tMs}ms (exit=$($r.exit) lat=$sec)"
      }
    }
    $openWindow.end_ms = $tMs
    $openWindow.probes++
    if ($r.exit -ne 0) { $openWindow.timeouts++ }
  } elseif ($null -ne $openWindow) {
    P ("stall window closed: {0}ms..{1}ms ({2}ms, probes={3})" -f $openWindow.start_ms, $openWindow.end_ms, ($openWindow.end_ms - $openWindow.start_ms), $openWindow.probes)
    $openWindow = $null
  }

  if (($tMs - $lastFullMs) -ge 1000) {
    $lastFullMs = $tMs
    $body = Join-Path $ArtifactDir 'tmp-full-status.json'
    $null = Curl "$base/debug/chat/status" 30 $body
    $text = if (Test-Path $body) { Get-Content $body -Raw } else { '' }
    $c = 0; $a = 0; $n = 0; $pd = '?'; $fl = '?'
    if ($text -match '"cells"\s*:\s*(\d+)') { $c = [int]$Matches[1] }
    if ($text -match 'acked=(\d+)') { $a = [int]$Matches[1] }
    if ($text -match 'next=(\d+)') { $n = [int]$Matches[1] }
    if ($text -match 'pending=(\d+)') { $pd = $Matches[1] }
    if ($text -match 'in-flight=(\d+)') { $fl = $Matches[1] }
    $cells = $c; $acked = $a
    if ($cells -gt 0 -and $null -eq $firstCellsMs) { $firstCellsMs = $tMs }
    if ($acked -gt 0 -and $null -eq $firstAckedMs) { $firstAckedMs = $tMs }
    Add-Content -Path $timelinePath -Encoding UTF8 -Value ([pscustomobject]@{ t_ms = $tMs; cells = $cells; acked = $acked; next = $n; pending = $pd; in_flight = $fl } | ConvertTo-Json -Compress)
    if ($cells -gt 0 -and $acked -ge $cells -and $pd -eq '0' -and $fl -eq '0') { $stable++ } else { $stable = 0 }
    if ($stable -ge 3 -and -not $converged) {
      $converged = $true; $convergedMs = $tMs
      P "converged at ${tMs}ms (cells=$cells acked=$acked) -> idle probe ${IdleProbeSec}s"
      $deadline = (Get-Date).AddSeconds($IdleProbeSec)
    }
  }
  Start-Sleep -Milliseconds $IntervalMs
}
if ($null -ne $openWindow) {
  P ("stall window still open at end: {0}ms..{1}ms" -f $openWindow.start_ms, $openWindow.end_ms)
}
if (-not $proc.HasExited) { try { $proc.Kill($true) } catch { }; P "child killed" }

$all = $lat.ToArray()
$worst = ($stallWindows | Sort-Object { $_.end_ms - $_.start_ms } -Descending | Select-Object -First 1)
$stallTotal = ($stallWindows | Measure-Object -Property { $_.end_ms - $_.start_ms } -Sum).Sum
if ($null -eq $stallTotal) { $stallTotal = 0 }
$summary = [pscustomobject]@{
  artifact                 = $ArtifactDir
  session                  = $SessionId
  probes                   = $iter
  interval_ms              = $IntervalMs
  stall_threshold_sec      = $StallSec
  request_timeouts         = $timeouts
  stall_windows            = $stallWindows.Count
  longest_stall_ms         = $(if ($worst) { $worst.end_ms - $worst.start_ms } else { 0 })
  longest_stall_at_ms      = $(if ($worst) { $worst.start_ms } else { $null })
  stall_total_ms           = $stallTotal
  fast_status_p50_sec      = Percentile $all 0.50
  fast_status_p90_sec      = Percentile $all 0.90
  fast_status_p99_sec      = Percentile $all 0.99
  fast_status_max_sec      = $(if ($all.Count -gt 0) { [Math]::Round(($all | Measure-Object -Maximum).Maximum, 3) } else { $null })
  converged                = $converged
  converged_ms             = $convergedMs
  first_cells_ms           = $firstCellsMs
  first_acked_ms           = $firstAckedMs
  cells                    = $cells
  acked                    = $acked
}
$summary | ConvertTo-Json -Depth 4 | Set-Content -Path (Join-Path $ArtifactDir 'summary.json') -Encoding UTF8
P ("SUMMARY " + ($summary | ConvertTo-Json -Compress))
$stderrText = if (Test-Path $stderrLog) { Get-Content $stderrLog -Raw -Encoding UTF8 } else { '' }
foreach ($line in ($stderrText -split "`r?`n")) { if ($line -match 'startup timing') { P ("MARK " + $line) } }
