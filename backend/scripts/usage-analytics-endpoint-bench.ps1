<#
.SYNOPSIS
  /usage 首屏端点基准（方案 §11.3 / Phase 0.1）。

.DESCRIPTION
  直连 runtime-server 的 analytics 端点，输出冷启动样本与 P50/P95 耗时。
  改造前后各跑一次即可量化收益（对照 docs/plan/usage-analytics-query-performance-optimization-plan-20260917.md §2.8）。

.PARAMETER BaseUrl
  后端地址，默认 http://127.0.0.1:8101（Vite dev server 5193 的代理目标）。

.PARAMETER Iterations
  每个端点重复次数，默认 5；首次样本作为 cold_s 单独列出。

.PARAMETER AdminToken
  可选 Bearer Token（与前端 admin token 一致）。

.EXAMPLE
  pwsh -File backend/scripts/usage-analytics-endpoint-bench.ps1
  pwsh -File backend/scripts/usage-analytics-endpoint-bench.ps1 -BaseUrl http://127.0.0.1:8101 -Iterations 10
#>
[CmdletBinding()]
param(
    [string]$BaseUrl = "http://127.0.0.1:8101",
    [int]$Iterations = 5,
    [string]$AdminToken = ""
)

$ErrorActionPreference = "Stop"

# 与 /usage 首屏 6 请求 + 2 个延迟 provider/model summary 一一对应。
$endpoints = [ordered]@{
    "overview"          = "/api/runtime/analytics/overview?group_by=day"
    "sessions"          = "/api/runtime/analytics/sessions?limit=50"
    "summary"           = "/api/runtime/analytics/summary?group_by=day"
    "summary_provider"  = "/api/runtime/analytics/summary?group_by=provider"
    "summary_model"     = "/api/runtime/analytics/summary?group_by=model"
    "dimensions"        = "/api/runtime/analytics/dimensions"
    "errors"            = "/api/runtime/analytics/errors?top=10"
    "subagents"         = "/api/runtime/analytics/subagents?limit=200"
    "tools"             = "/api/runtime/analytics/tools"
}

$curlArgs = @("-s", "-o", "NUL", "-w", "%{http_code} %{time_total}")
if ($AdminToken.Trim()) {
    $curlArgs += @("-H", "Authorization: Bearer $AdminToken")
}

function Get-Percentile {
    param([double[]]$Sorted, [double]$P)
    if ($Sorted.Count -eq 0) { return 0 }
    $index = [int][math]::Floor($Sorted.Count * $P)
    if ($index -ge $Sorted.Count) { $index = $Sorted.Count - 1 }
    return $Sorted[$index]
}

Write-Host "usage analytics endpoint bench -> $BaseUrl (iterations=$Iterations)" -ForegroundColor Cyan
Write-Host ""

$rows = @()
foreach ($entry in $endpoints.GetEnumerator()) {
    $url = "$BaseUrl$($entry.Value)"
    $samples = [System.Collections.Generic.List[double]]::new()
    $code = 0
    $reps = [Math]::Max(1, $Iterations)
    for ($i = 0; $i -lt $reps; $i++) {
        $out = & curl.exe @curlArgs $url
        $parts = "$out".Trim().Split(" ")
        if ($parts.Count -ge 2) {
            $code = [int]$parts[0]
            $samples.Add([double]$parts[1])
        }
    }
    $sorted = [double[]]@($samples | Sort-Object)
    $rows += [pscustomobject]@{
        endpoint = $entry.Key
        http     = $code
        cold_s   = if ($sorted.Count -gt 0) { [math]::Round($sorted[0], 4) } else { $null }
        p50_s    = [math]::Round((Get-Percentile -Sorted $sorted -P 0.50), 4)
        p95_s    = [math]::Round((Get-Percentile -Sorted $sorted -P 0.95), 4)
        samples  = $sorted.Count
    }
}

$rows | Format-Table -AutoSize

# 首屏总成本：overview（Phase 2 合并后的主请求）+ 2 个延迟 summary。
$overviewRow = $rows | Where-Object { $_.endpoint -eq "overview" }
$providerRow = $rows | Where-Object { $_.endpoint -eq "summary_provider" }
$modelRow = $rows | Where-Object { $_.endpoint -eq "summary_model" }
$firstScreen = 0.0
foreach ($row in @($overviewRow, $providerRow, $modelRow)) {
    if ($null -ne $row) { $firstScreen += [double]$row.p50_s }
}
Write-Host ("first-screen estimate (overview + provider/model summary, p50 sum): {0:N4}s" -f $firstScreen) -ForegroundColor Green

# 未合并前的等价成本（sessions + summary + dimensions + 2 延迟 summary，用于对照 §2.8 基线）。
$legacy = 0.0
foreach ($name in @("sessions", "summary", "dimensions", "summary_provider", "summary_model")) {
    $row = $rows | Where-Object { $_.endpoint -eq $name }
    if ($null -ne $row) { $legacy += [double]$row.p50_s }
}
Write-Host ("pre-merge equivalent (sessions + summary + dimensions + 2 lazy summary, p50 sum): {0:N4}s" -f $legacy) -ForegroundColor DarkGray
