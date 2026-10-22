# measure-aicli-residency.ps1 —— transcript 驻留降级基线与回归测量（任务 T0）
#
# 设计文档：docs/plan/aicli-transcript-residency-and-lazy-history-design-20261020.md §15.3
# 任务清单：docs/plan/aicli-transcript-residency-implementation-tasks-20261020.md §T0
#
# 用途：
#   1. 采集目标 aicli 进程的内存/句柄/CPU 与 /debug/chat/status 的
#      scene/布局缓存/驻留（app_state.residency）读数；
#   2. 输出 report.json + report.md，作为 M0 基线证据与后续 A/B 对照。
#
# 注意：-IncludePprof 会触发一次 heap 文本导出（?gc=1，proto 格式、无 tabwriter），
# 属于重量级操作，仅在需要堆归因时使用；对大会话建议加 -PprofTopN 限制输出。
[CmdletBinding()]
param(
    [int]$ProcessId = 0,
    [string]$ProcessName = 'aicli-5x',
    [string]$StatusUrl = 'http://127.0.0.1:50644/debug/chat/status',
    [int]$DurationMinutes = 0,
    [int]$IntervalSeconds = 15,
    [string]$OutputDir = '',
    [switch]$IncludePprof,
    [int]$PprofTopN = 15
)

Set-StrictMode -Off
$ErrorActionPreference = 'Stop'

function Get-ResidencyTargetProcess {
    if ($ProcessId -gt 0) {
        return Get-Process -Id $ProcessId -ErrorAction Stop
    }
    $procs = @(Get-Process -Name $ProcessName -ErrorAction SilentlyContinue)
    if ($procs.Count -eq 0) {
        throw "找不到进程 '$ProcessName'；请显式传 -ProcessId。"
    }
    # 多个同名进程时取工作集最大的那个（长会话实例通常最大）。
    return ($procs | Sort-Object WorkingSet64 -Descending | Select-Object -First 1)
}

function Get-Sample($proc) {
    $sample = [ordered]@{
        captured_at = (Get-Date).ToString('o')
        pid         = $proc.Id
        ws_mb       = [math]::Round($proc.WorkingSet64 / 1MB, 1)
        private_mb  = [math]::Round($proc.PrivateMemorySize64 / 1MB, 1)
        peak_ws_mb  = [math]::Round($proc.PeakWorkingSet64 / 1MB, 1)
        threads     = $proc.Threads.Count
        handles     = $proc.HandleCount
        cpu_pct     = $null
        status      = $null
        status_error = $null
    }
    try {
        $perf = Get-CimInstance Win32_PerfFormattedData_PerfProc_Process -Filter "IDProcess=$($proc.Id)" -ErrorAction Stop
        if ($perf) { $sample.cpu_pct = [int]$perf.PercentProcessorTime }
    } catch { }

    try {
        $json = (Invoke-WebRequest -UseBasicParsing -Uri $StatusUrl -TimeoutSec 10).Content
        $status = $json | ConvertFrom-Json
        $statusView = [ordered]@{
            scene_cells       = $null
            layout_rows       = $null
            transcript_cells  = $null
            appstate_cells    = $null
            frontier_cells    = $null
            cell_rows_bytes   = $null
            cell_rows_entries = $null
            cell_rows_evictions = $null
            cell_rows_hit_rate  = $null
            plan_bytes        = $null
            residency_enabled = $null
            residency_note    = $null
        }
        if ($status.scene) {
            $statusView.scene_cells = $status.scene.cells
            $statusView.layout_rows = $status.scene.layout_rows
        }
        if ($status.app_state) {
            if ($status.app_state.layout_cache) {
                $lc = $status.app_state.layout_cache
                $statusView.cell_rows_bytes     = $lc.cell_rows_bytes
                $statusView.cell_rows_entries   = $lc.cell_rows_entries
                $statusView.cell_rows_evictions = $lc.cell_rows_evictions
                $statusView.cell_rows_hit_rate  = $lc.cell_rows_hit_rate
                $statusView.plan_bytes          = $lc.plan_bytes
            }
            if ($status.app_state.plan) {
                $statusView.transcript_cells = $status.app_state.plan.transcript_cells
                $statusView.appstate_cells   = $status.app_state.plan.app_state_cells
                $statusView.frontier_cells   = $status.app_state.plan.frontier_cells
            }
            if ($status.app_state.residency) {
                $statusView.residency_enabled = $status.app_state.residency.enabled
                $statusView.residency_note    = $status.app_state.residency.note
            }
        }
        $sample.status = $statusView
    } catch {
        $sample.status_error = $_.Exception.Message
    }
    return $sample
}

function Get-Aggregate($samples, [string]$field) {
    $values = @($samples | ForEach-Object { $_.$field } | Where-Object { $_ -ne $null })
    if ($values.Count -eq 0) { return $null }
    $sorted = @($values | Sort-Object)
    return [ordered]@{
        min  = [math]::Round(($sorted | Select-Object -First 1), 1)
        max  = [math]::Round(($sorted | Select-Object -Last 1), 1)
        mean = [math]::Round((($values | Measure-Object -Average).Average), 1)
        p95  = [math]::Round($sorted[[math]::Min([int][math]::Floor($sorted.Count * 0.95), $sorted.Count - 1)], 1)
    }
}

$proc = Get-ResidencyTargetProcess
Write-Host ("target: PID {0} ({1}), WS {2} MB, Private {3} MB" -f `
    $proc.Id, $proc.ProcessName, [math]::Round($proc.WorkingSet64 / 1MB, 1), [math]::Round($proc.PrivateMemorySize64 / 1MB, 1))

if (-not $OutputDir) {
    $OutputDir = Join-Path (Get-Location) 'output/residency-baseline'
}
New-Item -ItemType Directory -Force -Path $OutputDir | Out-Null
$stamp = (Get-Date).ToString('yyyyMMdd-HHmmss')
$runDir = Join-Path $OutputDir "run-$stamp"
New-Item -ItemType Directory -Force -Path $runDir | Out-Null

$samples = @()
$deadline = if ($DurationMinutes -gt 0) { (Get-Date).AddMinutes($DurationMinutes) } else { $null }
do {
    $samples += Get-Sample $proc
    if ($null -ne $deadline -and (Get-Date) -lt $deadline) {
        Start-Sleep -Seconds ([math]::Max(1, $IntervalSeconds))
    }
} while ($null -ne $deadline -and (Get-Date) -lt $deadline -and $proc.HasExited -eq $false)

$report = [ordered]@{
    tool          = 'measure-aicli-residency.ps1'
    task          = 'T0 baseline'
    generated_at  = (Get-Date).ToString('o')
    status_url    = $StatusUrl
    samples_count = $samples.Count
    aggregates    = [ordered]@{
        ws_mb      = Get-Aggregate $samples 'ws_mb'
        private_mb = Get-Aggregate $samples 'private_mb'
        cpu_pct    = Get-Aggregate $samples 'cpu_pct'
        threads    = Get-Aggregate $samples 'threads'
        handles    = Get-Aggregate $samples 'handles'
    }
    samples       = $samples
}

$reportPath = Join-Path $runDir 'report.json'
$report | ConvertTo-Json -Depth 8 | Set-Content -Encoding UTF8 -Path $reportPath

$last = $samples[-1]
$md = @()
$md += "# aicli residency baseline ($stamp)"
$md += ''
$md += "- PID: $($last.pid)  区间样本: $($samples.Count)"
$md += "- WS(min/mean/max/p95): $($report.aggregates.ws_mb.min) / $($report.aggregates.ws_mb.mean) / $($report.aggregates.ws_mb.max) / $($report.aggregates.ws_mb.p95) MB"
$md += "- Private(min/mean/max/p95): $($report.aggregates.private_mb.min) / $($report.aggregates.private_mb.mean) / $($report.aggregates.private_mb.max) / $($report.aggregates.private_mb.p95) MB"
$md += "- CPU%(mean/max/p95): $($report.aggregates.cpu_pct.mean) / $($report.aggregates.cpu_pct.max) / $($report.aggregates.cpu_pct.p95)"
$md += "- threads/handles(mean/max): $($report.aggregates.threads.mean)/$($report.aggregates.threads.max) · $($report.aggregates.handles.mean)/$($report.aggregates.handles.max)"
$md += ''
$md += '## 最后一次状态读数'
$md += ''
if ($last.status) {
    $md += '| 字段 | 值 |'
    $md += '| --- | --- |'
    foreach ($k in $last.status.Keys) { $md += "| $k | $($last.status[$k]) |" }
} else {
    $md += "status 不可读：$($last.status_error)"
}
$mdPath = Join-Path $runDir 'report.md'
$md | Set-Content -Encoding UTF8 -Path $mdPath

if ($IncludePprof) {
    Write-Host 'collecting pprof heap (proto, gc=1) ...'
    $base = ($StatusUrl -replace '/debug/chat/status.*$', '')
    $pprofUrl = "$base/debug/pprof/heap?gc=1"
    $pprofOut = Join-Path $runDir 'pprof-heap-inuse.txt'
    try {
        & go tool pprof -top -inuse_space -nodecount=$PprofTopN $pprofUrl 2>&1 | Set-Content -Encoding UTF8 -Path $pprofOut
        Write-Host "pprof -> $pprofOut"
    } catch {
        Write-Host "pprof 采集失败：$($_.Exception.Message)"
    }
}

Write-Host "report: $reportPath"
Write-Host "report: $mdPath"
if ($last.status) {
    Write-Host ("scene: cells={0} layout_rows={1} | cache: entries={2} bytes={3} evictions={4} | residency_enabled={5}" -f `
        $last.status.scene_cells, $last.status.layout_rows, $last.status.cell_rows_entries, `
        $last.status.cell_rows_bytes, $last.status.cell_rows_evictions, $last.status.residency_enabled)
}