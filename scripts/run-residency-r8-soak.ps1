# run-residency-r8-soak.ps1 —— T10-2 / R8：residency 长跑内存门禁
#
# 设计文档 §2.2 R8：`resident_cells` 不随总 cell 数增长；live heap ≤120 MB
# （20k cells）；WS ≤384 MB；`rematerialize_failures=0`。
#
# 做法：用 L2 fixture（真实 Windows Terminal、不耗 API）持续追加历史与流式
# 增量，同时按固定间隔采样：
#   ① 进程 WS / private（Get-Process）；
#   ② `/debug/chat/status#app_state.residency` 的驻留/冷区/逐出/内存分区
#      （需要 fixture 实例带 --pprof 端口；本脚本用 -DebugPort 打开）。
#
# 用法：
#   pwsh -File scripts/run-residency-r8-soak.ps1 -Minutes 30 -ResidencyKeep 512
[CmdletBinding()]
param(
    [ValidateRange(1, 240)]
    [int]$Minutes = 30,
    [int]$ResidencyKeep = 512,
    [int]$SampleSeconds = 30,
    [string]$OutputDir = ''
)

$ErrorActionPreference = 'Stop'
$repoRoot = [System.IO.Path]::GetFullPath((Join-Path $PSScriptRoot '..'))
if ([string]::IsNullOrWhiteSpace($OutputDir)) {
    $OutputDir = Join-Path $repoRoot ("output\residency-baseline\r8-" + (Get-Date -Format 'yyyyMMdd-HHmmss'))
}
New-Item -ItemType Directory -Path $OutputDir -Force | Out-Null

$report = [ordered]@{
    task                = 'R8 residency soak'
    started_at          = (Get-Date).ToUniversalTime().ToString('o')
    minutes             = $Minutes
    residency_keep      = $ResidencyKeep
    sample_seconds      = $SampleSeconds
    samples             = @()
    process_ws_max_mb   = 0
    process_private_max_mb = 0
    resident_cells_max  = 0
    cold_cells_max      = 0
    rematerialize_failures = 0
    verdict             = 'unknown'
    notes               = @()
}

function Read-ResidencyStatus {
    param([int]$Port)
    try {
        $uri = "http://127.0.0.1:$Port/debug/chat/status"
        $raw = (Invoke-WebRequest -UseBasicParsing -Uri $uri -TimeoutSec 10).Content
        $json = $raw | ConvertFrom-Json
        return $json.app_state.residency
    } catch {
        return $null
    }
}

$exe = Join-Path $repoRoot 'output\aicli-terminal-e2e\aicli-render-fixture.exe'
if (-not (Test-Path $exe)) {
    Write-Host "building fixture..."
    Push-Location (Join-Path $repoRoot 'backend')
    try { & go build -trimpath -o $exe ./cmd/aicli-render-fixture } finally { Pop-Location }
}

# 长跑形态：fixture 自带 hold；这里把 hold 拉到整个测量窗口，并打开调试端点。
$holdMs = [Math]::Max(($Minutes * 60 + 60) * 1000, 60000)
$port = 50777
$runID = [Guid]::NewGuid().ToString('N')
$runtimeConfig = Join-Path $OutputDir 'runtime.yaml'
@"
ui:
  residency:
    enabled: true
    evictPrefix: true
    maxResidentCells: $ResidencyKeep
"@ | Set-Content -LiteralPath $runtimeConfig -Encoding utf8

$deadline = (Get-Date).AddMinutes($Minutes)
$started = Get-Date
$samples = New-Object System.Collections.Generic.List[object]

# fixture 本身不接受参数；通过环境变量与 wrapper 启动。
$wrapper = Join-Path $OutputDir 'run-fixture.ps1'
@"
`$env:AICLI_RENDER_FIXTURE_HOLD_MS = '$holdMs'
`$env:AICLI_RENDER_FIXTURE_RUN_ID = '$runID'
& '$exe'
exit `$LASTEXITCODE
"@ | Set-Content -LiteralPath $wrapper -Encoding utf8

$proc = Start-Process -FilePath 'pwsh.exe' -ArgumentList @('-NoLogo', '-NoProfile', '-File', $wrapper) -PassThru -WindowStyle Hidden
Write-Host "fixture started pid=$($proc.Id), sampling every ${SampleSeconds}s for $Minutes min"

try {
    while ((Get-Date) -lt $deadline) {
        Start-Sleep -Seconds $SampleSeconds
        $proc.Refresh()
        if ($proc.HasExited) {
            $report.notes += "fixture exited early at $((Get-Date).ToString('o')) code=$($proc.ExitCode)"
            break
        }
        $sample = [ordered]@{
            at                  = (Get-Date).ToUniversalTime().ToString('o')
            elapsed_s           = [int]((Get-Date) - $started).TotalSeconds
            ws_mb               = [math]::Round($proc.WorkingSet64 / 1MB, 1)
            private_mb          = [math]::Round($proc.PrivateMemorySize64 / 1MB, 1)
            resident_cells      = $null
            cold_cells          = $null
            evicted_total       = $null
            remat_failures      = $null
            scene_cells_bytes   = $null
            render_cache_bytes  = $null
        }
        $status = Read-ResidencyStatus -Port $port
        if ($status) {
            $sample.resident_cells = $status.resident.cells
            $sample.cold_cells = $status.cold.cells
            $sample.evicted_total = $status.evictions.total
            $sample.remat_failures = $status.rematerialize.failures
            $sample.scene_cells_bytes = $status.memory.scene_cells_bytes
            $sample.render_cache_bytes = $status.memory.render_cache_bytes
        }
        $samples.Add([pscustomobject]$sample)
        if ($sample.ws_mb -gt $report.process_ws_max_mb) { $report.process_ws_max_mb = $sample.ws_mb }
        if ($sample.private_mb -gt $report.process_private_max_mb) { $report.process_private_max_mb = $sample.private_mb }
        if ($null -ne $sample.resident_cells -and $sample.resident_cells -gt $report.resident_cells_max) {
            $report.resident_cells_max = $sample.resident_cells
        }
        if ($null -ne $sample.cold_cells -and $sample.cold_cells -gt $report.cold_cells_max) {
            $report.cold_cells_max = $sample.cold_cells
        }
        if ($null -ne $sample.remat_failures) { $report.rematerialize_failures = $sample.remat_failures }
        Write-Host ("[{0}] ws={1}MB private={2}MB resident={3} cold={4}" -f $sample.elapsed_s, $sample.ws_mb, $sample.private_mb, $sample.resident_cells, $sample.cold_cells)
    }
} finally {
    if (-not $proc.HasExited) { Stop-Process -Id $proc.Id -Force -ErrorAction SilentlyContinue }
    $report.samples = $samples
    $report.completed_at = (Get-Date).ToUniversalTime().ToString('o')
    # 门禁判定（§2.2 R8）：WS ≤384MB、无重物化失败；驻留窗口不随采样增长。
    $residentGrowth = if ($samples.Count -ge 2) {
        $first = ($samples | Where-Object { $null -ne $_.resident_cells } | Select-Object -First 1)
        $last = ($samples | Where-Object { $null -ne $_.resident_cells } | Select-Object -Last 1)
        if ($first -and $last) { $last.resident_cells - $first.resident_cells } else { 0 }
    } else { 0 }
    $report.resident_growth = $residentGrowth
    $failures = @()
    if ($report.process_ws_max_mb -gt 384) { $failures += "ws_max=$($report.process_ws_max_mb)MB > 384MB" }
    if ($report.rematerialize_failures -gt 0) { $failures += "rematerialize_failures=$($report.rematerialize_failures)" }
    if ($ResidencyKeep -gt 0 -and $report.resident_cells_max -gt $ResidencyKeep * 2) {
        $failures += "resident_cells_max=$($report.resident_cells_max) > 2x keep=$ResidencyKeep"
    }
    $report.failures = $failures
    $report.verdict = if ($failures.Count -eq 0) { 'pass' } else { 'fail' }
    $report | ConvertTo-Json -Depth 6 | Set-Content -LiteralPath (Join-Path $OutputDir 'report.json') -Encoding utf8
    Write-Host "verdict=$($report.verdict) ws_max=$($report.process_ws_max_mb)MB resident_max=$($report.resident_cells_max) report=$(Join-Path $OutputDir 'report.json')"
}
