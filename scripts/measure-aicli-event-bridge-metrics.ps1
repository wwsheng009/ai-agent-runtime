<#
.SYNOPSIS
  §8.3 现场指标复核：对活动会话的 /debug/chat/status 连续采样并判定事件桥目标。

.DESCRIPTION
  方案 docs/plan/ui-event-bridge-drop-hardening.md §8.3 的复核工具。在活动会话上按固定
  间隔采样 JSON 快照，计算窗口内的增量/峰值，并对四条验收目标给出 PASS/FAIL：

    - scene.deferred_queue.dropped 增速 < 1/min
    - scene.deferred_queue.pending 峰值 < 256（上限 512 的一半）
    - runtime.unknown_events_dropped 增速 < 10/min（观察平面端点；不可用时跳过并标注）
    - executor.window_diagnosis 健康期应为 healthy/idle（CURRENT 口径，非 since_start）

  端点不可达 / 无活动会话时以退出码 2 结束，不产生假阴性结论。

.PARAMETER Endpoint
  aicli loopback 端点基址，例如 http://127.0.0.1:64751（不带具体路径）。

.PARAMETER WindowSeconds
  采样窗口秒数，默认 120（§8.3 要求连续两个 30 分钟窗口时请显式传 1800 并采集两次）。

.PARAMETER SampleSeconds
  采样间隔秒数，默认 5。

.PARAMETER StatusPath
  /debug/chat/status 路径，默认 /debug/chat/status。

.PARAMETER ObservePath
  观察平面快照路径，默认 /api/runtime/observe/v1/snapshot。

.EXAMPLE
  pwsh -File scripts/measure-aicli-event-bridge-metrics.ps1 -Endpoint http://127.0.0.1:64751 -WindowSeconds 1800
#>
[CmdletBinding()]
param(
    [Parameter(Mandatory = $true)]
    [string]$Endpoint,

    [ValidateRange(2, 86400)]
    [int]$WindowSeconds = 120,

    [ValidateRange(1, 300)]
    [int]$SampleSeconds = 5,

    [string]$StatusPath = "/debug/chat/status",

    [string]$ObservePath = "/api/runtime/observe/v1/snapshot"
)

$ErrorActionPreference = "Stop"

function Find-Node {
    param(
        [AllowNull()]$Node,
        [Parameter(Mandatory = $true)][string]$Name,
        [int]$Depth = 0
    )
    if ($null -eq $Node -or $Depth -gt 12) { return $null }
    if ($Node -is [string] -or $Node -is [ValueType]) { return $null }
    if ($Node -is [System.Collections.IDictionary]) {
        if ($Node.Contains($Name)) { return $Node[$Name] }
        foreach ($key in $Node.Keys) {
            $found = Find-Node -Node $Node[$key] -Name $Name -Depth ($Depth + 1)
            if ($null -ne $found) { return $found }
        }
        return $null
    }
    if ($Node -is [System.Array]) {
        foreach ($item in $Node) {
            $found = Find-Node -Node $item -Name $Name -Depth ($Depth + 1)
            if ($null -ne $found) { return $found }
        }
        return $null
    }
    if ($Node -is [System.Management.Automation.PSCustomObject]) {
        $prop = $Node.PSObject.Properties[$Name]
        if ($null -ne $prop) { return $prop.Value }
        foreach ($p in $Node.PSObject.Properties) {
            $found = Find-Node -Node $p.Value -Name $Name -Depth ($Depth + 1)
            if ($null -ne $found) { return $found }
        }
        return $null
    }
    return $null
}

function Get-JsonOrNull {
    param([Parameter(Mandatory = $true)][string]$Url)
    try {
        return Invoke-RestMethod -Uri $Url -TimeoutSec 5 -Method Get
    }
    catch {
        return $null
    }
}

function Get-LongValue {
    param([AllowNull()]$Value)
    if ($null -eq $Value) { return $null }
    try { return [int64]$Value } catch { return $null }
}

$base = $Endpoint.TrimEnd("/")
$statusUrl = "$base$StatusPath"
$observeUrl = "$base$ObservePath"

$status = Get-JsonOrNull -Url $statusUrl
if ($null -eq $status) {
    Write-Host "ENDPOINT_UNREACHABLE: $statusUrl（无活动会话或 loopback 调试服务器未开启）" -ForegroundColor Yellow
    exit 2
}
if ($status -is [string] -and $status -match "无活动会话|no active session") {
    Write-Host "NO_ACTIVE_SESSION: $statusUrl" -ForegroundColor Yellow
    exit 2
}

$observeAvailable = $true
$observe = Get-JsonOrNull -Url $observeUrl
if ($null -eq $observe) {
    $observeAvailable = $false
    Write-Host "observe 端点不可用（$observeUrl），跳过 unknown_events_dropped 判定" -ForegroundColor Yellow
}

Write-Host "采样中：$statusUrl（窗口 ${WindowSeconds}s，间隔 ${SampleSeconds}s）" -ForegroundColor Cyan
$startedAt = Get-Date
$deadline = $startedAt.AddSeconds($WindowSeconds)

$firstDropped = $null
$lastDropped = $null
$firstUnknown = $null
$lastUnknown = $null
$pendingPeak = 0
$diagnoses = New-Object System.Collections.Generic.List[string]
$sampleCount = 0

while ((Get-Date) -lt $deadline -or $sampleCount -lt 2) {
    $status = Get-JsonOrNull -Url $statusUrl
    if ($null -eq $status) {
        Write-Host "采样中断：$statusUrl 不可达（已采 $sampleCount 次）" -ForegroundColor Yellow
        exit 2
    }

    $dq = Find-Node -Node $status -Name "deferred_queue"
    if ($null -ne $dq) {
        $dropped = Get-LongValue (Find-Node -Node $dq -Name "dropped")
        $pending = Get-LongValue (Find-Node -Node $dq -Name "pending")
        if ($null -ne $dropped) {
            if ($null -eq $firstDropped) { $firstDropped = $dropped }
            $lastDropped = $dropped
        }
        if ($null -ne $pending -and $pending -gt $pendingPeak) {
            $pendingPeak = [int64]$pending
        }
    }
    else {
        if ($null -eq $firstDropped) { $firstDropped = 0 }
        $lastDropped = 0
    }

    $executor = Find-Node -Node $status -Name "executor"
    if ($null -ne $executor) {
        $diag = Find-Node -Node $executor -Name "window_diagnosis"
        if ($null -ne $diag -and -not [string]::IsNullOrWhiteSpace([string]$diag)) {
            $diagnoses.Add([string]$diag)
        }
    }

    if ($observeAvailable) {
        $observe = Get-JsonOrNull -Url $observeUrl
        if ($null -ne $observe) {
            $unknown = Get-LongValue (Find-Node -Node $observe -Name "unknown_events_dropped")
            if ($null -ne $unknown) {
                if ($null -eq $firstUnknown) { $firstUnknown = $unknown }
                $lastUnknown = $unknown
            }
        }
        else {
            $observeAvailable = $false
            Write-Host "observe 端点在窗口内变为不可用，unknown_events_dropped 判定降级为 SKIP" -ForegroundColor Yellow
        }
    }

    $sampleCount++
    if ((Get-Date) -lt $deadline) {
        Start-Sleep -Seconds $SampleSeconds
    }
}

$elapsedSeconds = [math]::Max(1.0, ((Get-Date) - $startedAt).TotalSeconds)
$elapsedMinutes = $elapsedSeconds / 60.0

$checks = New-Object System.Collections.Generic.List[object]
function Add-Check {
    param([string]$Name, [string]$Target, [string]$Actual, [bool]$Ok, [bool]$Skip = $false)
    $checks.Add([pscustomobject]@{
            Metric = $Name
            Target = $Target
            Actual = $Actual
            Verdict = if ($Skip) { "SKIP" } elseif ($Ok) { "PASS" } else { "FAIL" }
            Ok     = ($Skip -or $Ok)
        })
}

if ($null -eq $firstDropped) { $firstDropped = 0; $lastDropped = 0 }
$droppedRate = ($lastDropped - $firstDropped) / $elapsedMinutes
Add-Check -Name "deferred_queue.dropped" -Target "< 1/min" -Actual ("{0:N2}/min ({1} -> {2})" -f $droppedRate, $firstDropped, $lastDropped) -Ok ($droppedRate -lt 1.0)

Add-Check -Name "deferred_queue.pending 峰值" -Target "< 256" -Actual ("{0}" -f $pendingPeak) -Ok ($pendingPeak -lt 256)

if ($observeAvailable -and $null -ne $firstUnknown -and $null -ne $lastUnknown) {
    $unknownRate = ($lastUnknown - $firstUnknown) / $elapsedMinutes
    Add-Check -Name "runtime.unknown_events_dropped" -Target "< 10/min" -Actual ("{0:N2}/min ({1} -> {2})" -f $unknownRate, $firstUnknown, $lastUnknown) -Ok ($unknownRate -lt 10.0)
}
else {
    Add-Check -Name "runtime.unknown_events_dropped" -Target "< 10/min" -Actual "observe 端点不可用" -Ok $false -Skip $true
}

$unhealthy = @($diagnoses | Where-Object { $_ -ne "healthy" -and $_ -ne "idle" })
if ($diagnoses.Count -eq 0) {
    Add-Check -Name "executor.window_diagnosis" -Target "healthy/idle" -Actual "未采到窗口判决" -Ok $false -Skip $true
}
else {
    $unique = ($diagnoses | Select-Object -Unique) -join ","
    Add-Check -Name "executor.window_diagnosis" -Target "healthy/idle" -Actual ("{0}（{1} 次采样）" -f $unique, $diagnoses.Count) -Ok ($unhealthy.Count -eq 0)
}

Write-Host ""
Write-Host ("===== §8.3 现场指标（窗口 {0:N0}s，采样 {1} 次） =====" -f $elapsedSeconds, $sampleCount) -ForegroundColor Cyan
$checks | Format-Table Metric, Target, Actual, Verdict -AutoSize | Out-String | Write-Host

$failed = @($checks | Where-Object { -not $_.Ok })
if ($failed.Count -gt 0) {
    Write-Host "RESULT: FAIL ($($failed.Count)/$($checks.Count) 项未达标)" -ForegroundColor Red
    exit 1
}

Write-Host "RESULT: PASS ($($checks.Count)/$($checks.Count) 项达标)" -ForegroundColor Green
exit 0
