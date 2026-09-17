# usage-analytics-endpoint-bench.ps1
#
# 用量分析页端点基线/回归脚本（方案 §2.8 / §11.3 / Phase 0.1）。
#
# 对 runtime-server 的 /api/runtime/analytics/* 端点做多轮计时，输出每个端点的
# P50/P95/均值（毫秒）与 JSON 摘要，用于改造前后对比与 CI 回归留档。
#
# 用法：
#   pwsh -File scripts/usage-analytics-endpoint-bench.ps1
#   pwsh -File scripts/usage-analytics-endpoint-bench.ps1 -AdminToken <token> -Iterations 10
#   pwsh -File scripts/usage-analytics-endpoint-bench.ps1 -BaseUrl http://127.0.0.1:8101 -Json
#
# 参数：
#   -BaseUrl      runtime-server 地址（默认 http://127.0.0.1:8101）
#   -AdminToken   admin token（写入 Authorization: Bearer，未设置时端点可能 403）
#   -Iterations   每端点轮数（默认 5）
#   -Warmup       预热轮数（默认 1，不计入统计）
#   -Json         只输出 JSON（便于脚本消费/留档）
#
# 提示：首屏 6 个请求对应下列 endpoints（overview 为 Phase 2 合并后的引导端点）。

[CmdletBinding()]
param(
    [string]$BaseUrl = "http://127.0.0.1:8101",
    [string]$AdminToken = "",
    [int]$Iterations = 5,
    [int]$Warmup = 1,
    [switch]$Json
)

$ErrorActionPreference = "Stop"

$endpoints = @(
    @{ Name = "overview";  Path = "/api/runtime/analytics/overview" },
    @{ Name = "sessions";  Path = "/api/runtime/analytics/sessions?limit=50" },
    @{ Name = "summary_day"; Path = "/api/runtime/analytics/summary?group_by=day" },
    @{ Name = "summary_provider"; Path = "/api/runtime/analytics/summary?group_by=provider" },
    @{ Name = "summary_model"; Path = "/api/runtime/analytics/summary?group_by=model" },
    @{ Name = "dimensions"; Path = "/api/runtime/analytics/dimensions" },
    @{ Name = "subagents"; Path = "/api/runtime/analytics/subagents?limit=200" },
    @{ Name = "errors";    Path = "/api/runtime/analytics/errors?top=10" }
)

$headers = @{}
if ($AdminToken -and $AdminToken.Trim().Length -gt 0) {
    $headers["Authorization"] = "Bearer $($AdminToken.Trim())"
}

function Get-Percentile {
    param([double[]]$Values, [double]$Percentile)
    if ($Values.Count -eq 0) { return 0 }
    $sorted = $Values | Sort-Object
    $index = [Math]::Ceiling($Percentile * $sorted.Count) - 1
    if ($index -lt 0) { $index = 0 }
    if ($index -ge $sorted.Count) { $index = $sorted.Count - 1 }
    return $sorted[$index]
}

$results = @()
foreach ($endpoint in $endpoints) {
    $url = "$BaseUrl$($endpoint.Path)"
    for ($i = 0; $i -lt $Warmup; $i++) {
        try { Invoke-WebRequest -Uri $url -Headers $headers -UseBasicParsing -TimeoutSec 120 | Out-Null } catch { }
    }
    $samples = @()
    $status = 0
    for ($i = 0; $i -lt $Iterations; $i++) {
        $sw = [System.Diagnostics.Stopwatch]::StartNew()
        try {
            $response = Invoke-WebRequest -Uri $url -Headers $headers -UseBasicParsing -TimeoutSec 120
            $status = [int]$response.StatusCode
        } catch {
            if ($_.Exception.Response) { $status = [int]$_.Exception.Response.StatusCode }
            else { $status = -1 }
        } finally {
            $sw.Stop()
        }
        $samples += $sw.Elapsed.TotalMilliseconds
    }
    $stats = [ordered]@{
        name       = $endpoint.Name
        path       = $endpoint.Path
        status     = $status
        iterations = $Iterations
        p50_ms     = [Math]::Round((Get-Percentile -Values $samples -Percentile 0.50), 2)
        p95_ms     = [Math]::Round((Get-Percentile -Values $samples -Percentile 0.95), 2)
        mean_ms    = [Math]::Round(($samples | Measure-Object -Average).Average, 2)
        max_ms     = [Math]::Round(($samples | Measure-Object -Maximum).Maximum, 2)
    }
    $results += $stats
}

$total = [ordered]@{
    base_url   = $BaseUrl
    generated  = (Get-Date).ToUniversalTime().ToString("o")
    iterations = $Iterations
    endpoints  = $results
}

if ($Json) {
    $total | ConvertTo-Json -Depth 6
    exit 0
}

Write-Host "usage analytics endpoint baseline ($BaseUrl), iterations=$Iterations" -ForegroundColor Cyan
$results | Format-Table name, status, iterations, p50_ms, p95_ms, mean_ms, max_ms -AutoSize
Write-Host ""
Write-Host "JSON summary:"
$total | ConvertTo-Json -Depth 6
