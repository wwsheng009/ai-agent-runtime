#requires -Version 7
<#
Goal 机制端到端验收（ConPTY 多轮夹具的固化版）。

背景：托管运行时下"交互式 PTY 作业单槽且易被回收"，此前多轮验证要靠
"每轮一个进程 + 手工 job_cancel"。`aicli chat --script <file>` 落地后，
单个进程即可按行串行提交多轮输入并在读完后自动退出——本脚本把它变成
一条可重复执行的命令，并对产物做结构断言（goal 命令文档 / 状态机 /
自动续跑 / 进程退出码）。

用法：
  pwsh -NoProfile -File scripts/run-conpty-goal-e2e.ps1
  pwsh -NoProfile -File scripts/run-conpty-goal-e2e.ps1 -ExePath backend/.tmp/aicli-goal-e2e.exe -Keep

参数：
  -ExePath         aicli 可执行文件（默认 <repo>/backend/aicli.exe）
  -WorkDir         运行目录（默认 <repo>）
  -Provider        默认 workbuddy（传空串则用配置默认）
  -Model           默认 cn:deepseek-v4.1-flash（传空串则用配置默认）
  -ReasoningEffort 默认 max（传空串则用配置默认）
  -TimeoutSec      整轮上限，默认 600s
  -Keep            保留 runRoot（默认保留；该参数仅为兼容显式语义）

产物目录：<repo>/output/goal-e2e/goal-e2e-<timestamp>/
  rounds.txt / stdout.log / stderr.log / render.log / chat-logs/**/events/runtime-events.jsonl

退出码：0 = 全部断言通过；1 = 断言失败或进程失败。
#>
[CmdletBinding()]
param(
  [string]$ExePath = '',
  [string]$WorkDir = '',
  [string]$Provider = 'workbuddy',
  [string]$Model = 'cn:deepseek-v4.1-flash',
  [string]$ReasoningEffort = 'max',
  [int]$TimeoutSec = 600,
  [switch]$Keep
)

$ErrorActionPreference = 'Stop'
$repoRoot = [IO.Path]::GetFullPath((Join-Path $PSScriptRoot '..'))
if ([string]::IsNullOrWhiteSpace($ExePath)) { $ExePath = Join-Path $repoRoot 'backend/aicli.exe' }
if ([string]::IsNullOrWhiteSpace($WorkDir)) { $WorkDir = $repoRoot }
if (-not (Test-Path -LiteralPath $ExePath)) {
  throw "aicli 可执行文件不存在: $ExePath（构建：cd backend; go build -o aicli.exe ./cmd/aicli/）"
}

$stamp = Get-Date -Format 'yyyyMMdd-HHmmss'
$runRoot = Join-Path $repoRoot ("output/goal-e2e/goal-e2e-" + $stamp)
New-Item -ItemType Directory -Force -Path $runRoot | Out-Null
$sessionDir = Join-Path $runRoot 'sessions'
$chatLogDir = Join-Path $runRoot 'chat-logs'
$renderLog = Join-Path $runRoot 'render.log'
$transcriptLog = Join-Path $runRoot 'transcript.txt'
$roundsFile = Join-Path $runRoot 'rounds.txt'
$stdoutLog = Join-Path $runRoot 'stdout.log'
$stderrLog = Join-Path $runRoot 'stderr.log'

# 多轮脚本：设置目标（要求模型完成后调用 update_goal）→ 状态 → 终态下 pause
# （应被状态机拒绝并给出精确原因）→ clear → 状态（应为空）。
$rounds = @(
  '/goal 只读核验：读取仓库根目录 VERSION 文件内容；核实后必须调用 update_goal 标记完成，summary 给出内容。',
  '/goal status',
  '/goal pause',
  '/goal clear',
  '/goal status'
)
Set-Content -LiteralPath $roundsFile -Value $rounds -Encoding UTF8

function Quote-Arg([string]$value) {
  if ($value -match '[\s"]') { return '"' + ($value -replace '"', '\"') + '"' }
  return $value
}

$argv = @('chat', '--headless', '--script', $roundsFile, '--render-output-file', $renderLog, '--transcript-file', $transcriptLog,
  '--session-dir', $sessionDir, '--log-dir', $chatLogDir, '--title', 'goal-e2e')
if (-not [string]::IsNullOrWhiteSpace($Provider)) { $argv += @('--provider', $Provider) }
if (-not [string]::IsNullOrWhiteSpace($Model)) { $argv += @('--model', $Model) }
if (-not [string]::IsNullOrWhiteSpace($ReasoningEffort)) { $argv += @('--reasoning-effort', $ReasoningEffort) }

Write-Host "[goal-e2e] exe      : $ExePath"
Write-Host "[goal-e2e] runRoot  : $runRoot"
Write-Host "[goal-e2e] rounds   : $($rounds.Count) 条（--script 驱动）"

$procArgs = ($argv | ForEach-Object { Quote-Arg $_ }) -join ' '
$sw = [Diagnostics.Stopwatch]::StartNew()
$proc = Start-Process -FilePath $ExePath -ArgumentList $procArgs -WorkingDirectory $WorkDir -PassThru -NoNewWindow `
  -RedirectStandardOutput $stdoutLog -RedirectStandardError $stderrLog
$timedOut = -not $proc.WaitForExit($TimeoutSec * 1000)
if ($timedOut) { try { $proc.Kill($true) } catch {} }
$sw.Stop()
$exitCode = if ($timedOut) { -1 } else { $proc.ExitCode }
Write-Host ("[goal-e2e] exit={0} elapsed={1:F1}s" -f $exitCode, $sw.Elapsed.TotalSeconds)

# ---- 收集产物 ----
function Read-Jsonl([string]$path) {
  if (-not (Test-Path -LiteralPath $path)) { return @() }
  $rows = New-Object System.Collections.Generic.List[object]
  foreach ($line in Get-Content -LiteralPath $path -Encoding UTF8) {
    $t = $line.Trim()
    if ($t -eq '') { continue }
    try { $rows.Add(($t | ConvertFrom-Json)) } catch {}
  }
  return $rows
}

$eventsFile = Get-ChildItem -Path $chatLogDir -Recurse -Filter 'runtime-events.jsonl' -ErrorAction SilentlyContinue |
  Sort-Object LastWriteTime -Descending | Select-Object -First 1
$debugFile = Get-ChildItem -Path $chatLogDir -Recurse -Filter 'debug.log' -ErrorAction SilentlyContinue |
  Sort-Object LastWriteTime -Descending | Select-Object -First 1
$events = @()
$commands = New-Object System.Collections.Generic.List[string]
if ($eventsFile) {
  $events = Read-Jsonl $eventsFile.FullName
  foreach ($e in $events) {
    if ($e.PSObject.Properties.Name -contains 'command' -and $e.command) { $commands.Add([string]$e.command) }
  }
}
$debugText = if ($debugFile) { Get-Content -LiteralPath $debugFile.FullName -Raw -Encoding UTF8 } else { '' }
$renderText = if (Test-Path -LiteralPath $renderLog) { Get-Content -LiteralPath $renderLog -Raw -Encoding UTF8 } else { '' }
$transcriptText = if (Test-Path -LiteralPath $transcriptLog) { Get-Content -LiteralPath $transcriptLog -Raw -Encoding UTF8 } else { '' }

function Test-RunContains([string]$needle) {
  if ($renderText -and $renderText.Contains($needle)) { return $true }
  if ($transcriptText -and $transcriptText.Contains($needle)) { return $true }
  foreach ($c in $commands) { if ($c.Contains($needle)) { return $true } }
  return $false
}

# ---- 断言 ----
$results = New-Object System.Collections.Generic.List[object]
function Assert-That([string]$name, [bool]$ok, [string]$detail) {
  $results.Add([pscustomobject]@{ Assertion = $name; Pass = $ok; Detail = $detail })
}

$setCommand = if ($commands.Count -gt 0) { $commands[0] } else { '' }
Assert-That '进程正常退出 (exit=0)' ($exitCode -eq 0) "exit=$exitCode timed_out=$timedOut"
Assert-That 'events 含 goal 命令文档 (>=4 条)' ($commands.Count -ge 4) "commands=$($commands.Count)"
Assert-That '目标已设置且为 active' ($setCommand.Contains('Goal 已设置') -and $setCommand.Contains('Goal: active')) ($setCommand -replace "`n", ' | ')
Assert-That '状态查询确认 complete' ((Test-RunContains 'Goal: complete') -or (Test-RunContains '"completed_by": "model"')) '期望模型在首轮完成并调用 update_goal'
Assert-That 'complete 终态 pause 被拒绝（精确原因）' (Test-RunContains 'goal 已完成') '状态机守卫 + typed error 回归'
Assert-That '目标已清除' (Test-RunContains 'Goal 已清除') ''
Assert-That '清除后状态为空' (Test-RunContains '当前会话未设置 goal') ''
Assert-That '自动续跑有决策日志' ($debugText.Contains('[goal] auto continuation') -or $debugText.Contains('[goal] error recovery')) 'debug.log 的 [goal] 行（成功收尾=auto continuation；EOF 类收尾=error recovery，两者都会带 reason=goal_complete）'
Assert-That 'transcript 已生成且含目标设置' ((Test-Path -LiteralPath $transcriptLog) -and ($transcriptText.Contains('Goal 已设置'))) '--transcript-file 产物'

$failed = @($results | Where-Object { -not $_.Pass })
Write-Host ''
Write-Host '==== goal-e2e 断言结果 ===='
$results | ForEach-Object {
  $mark = if ($_.Pass) { 'PASS' } else { 'FAIL' }
  $line = "[{0}] {1}" -f $mark, $_.Assertion
  if (-not $_.Pass -and $_.Detail) { $line += "  -- $($_.Detail)" }
  if ($_.Pass) { Write-Host $line -ForegroundColor Green } else { Write-Host $line -ForegroundColor Red }
}
Write-Host ("[goal-e2e] artifacts: {0}" -f $runRoot)
if ($failed.Count -gt 0) {
  Write-Host ("[goal-e2e] FAILED ({0}/{1})" -f $failed.Count, $results.Count) -ForegroundColor Red
  exit 1
}
Write-Host ("[goal-e2e] OK ({0} assertions)" -f $results.Count) -ForegroundColor Green
exit 0
