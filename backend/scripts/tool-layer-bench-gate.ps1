# Tool-layer benchmark regression gate.
#
# Design reference: docs/plan/tool-layer-abstraction-design-20261022.md §15.2
#   "关键 benchmark 设回归阈值; CI 3 次取中位数; 中位数超出基线 10% 失败"
#
# Usage:
#   pwsh -File backend/scripts/tool-layer-bench-gate.ps1
#   pwsh -File backend/scripts/tool-layer-bench-gate.ps1 -TolerancePercent 20
#   pwsh -File backend/scripts/tool-layer-bench-gate.ps1 -SkipShell
#   pwsh -File backend/scripts/tool-layer-bench-gate.ps1 -UpdateBaseline
#
# The committed baseline is a provisional first-run median from a
# memory-constrained Windows laptop (see the baseline report). Re-run
# -UpdateBaseline on the quiet CI machine before making the 10% gate blocking.

param(
    [double]$TolerancePercent = 10,
    [string]$BaselinePath = (Join-Path $PSScriptRoot 'tool-layer-bench-baseline.json'),
    [switch]$SkipSearch,
    [switch]$SkipShell,
    [switch]$UpdateBaseline
)

$ErrorActionPreference = 'Stop'
$backend = Split-Path -Parent $PSScriptRoot
Push-Location $backend
try {
    $env:GOGC = '20'
    $env:GOMAXPROCS = '2'

    function Get-BenchmarkMedians {
        param(
            [string]$Package,
            [string]$Pattern,
            [string[]]$ExtraArgs = @()
        )
        $arguments = @('-run', '^$', '-bench', $Pattern, '-benchmem', '-count=3') + $ExtraArgs + @($Package)
        $output = & go test @arguments 2>&1
        if ($LASTEXITCODE -ne 0) {
            throw "benchmark run failed for $Package (pattern=$Pattern)"
        }
        $rows = @{}
        foreach ($line in $output) {
            if ($line -match '^(Benchmark\S+)\s+\d+\s+([\d.]+)\s+ns/op') {
                # `go test -bench` appends -<GOMAXPROCS>; the baseline stores the
                # logical benchmark name.
                $name = $Matches[1] -replace '-\d+$', ''
                $value = [double]$Matches[2]
                if (-not $rows.ContainsKey($name)) {
                    $rows[$name] = [System.Collections.Generic.List[double]]::new()
                }
                [void]$rows[$name].Add($value)
            }
        }
        $medians = @{}
        foreach ($name in $rows.Keys) {
            $sorted = @($rows[$name] | Sort-Object)
            $count = $sorted.Count
            $middle = [int][Math]::Floor($count / 2)
            if ($count % 2 -eq 1) {
                $medians[$name] = $sorted[$middle]
            }
            else {
                $medians[$name] = ($sorted[$middle - 1] + $sorted[$middle]) / 2.0
            }
        }
        return $medians
    }

    $medians = @{}
    $packages = @(
        @{ Name = 'registry'; Pattern = 'BenchmarkRegistryDefinitions256|BenchmarkRegistryList256'; Args = @(); Package = './internal/toolcore' },
        @{ Name = 'viewcore'; Pattern = 'BenchmarkViewCoreDirect|BenchmarkViewHandlerRuntime'; Args = @(); Package = './internal/toolcore/viewcore' },
        @{ Name = 'mcp'; Pattern = 'BenchmarkInProcess32KiB|BenchmarkMCPConvert32KiB|BenchmarkMCPRoundTrip32KiB'; Args = @(); Package = './internal/toolkit' }
    )
    if (-not $SkipSearch) {
        $packages = ,@{ Name = 'searchcore'; Pattern = 'BenchmarkGlob10kNoMatch|BenchmarkGrep10kNoMatch'; Args = @() } + $packages
    }
    if (-not $SkipShell) {
        $packages += @{ Name = 'shell'; Pattern = 'BenchmarkShellCapture5MBNoArtifact|BenchmarkShellCapture5MBWithArtifact'; Args = @('-benchtime=1x'); Package = './internal/toolcore/shellexec' }
    }
    foreach ($spec in $packages) {
        $package = if ($spec.ContainsKey('Package')) { $spec.Package } else { "./internal/toolcore/$($spec.Name)" }
        Write-Host "== collect medians: $($spec.Name) ($package) =="
        $found = Get-BenchmarkMedians $package $spec.Pattern $spec.Args
        foreach ($name in $found.Keys) {
            $medians[$name] = $found[$name]
        }
    }

    if ($UpdateBaseline) {
        $medians | ConvertTo-Json -Depth 3 | Set-Content -LiteralPath $BaselinePath -Encoding UTF8
        Write-Host "baseline written: $BaselinePath ($($medians.Count) benchmarks)"
        exit 0
    }

    if (-not (Test-Path -LiteralPath $BaselinePath)) {
        throw "baseline missing: $BaselinePath (run -UpdateBaseline once on the CI machine)"
    }
    $baseline = Get-Content -LiteralPath $BaselinePath -Raw | ConvertFrom-Json

    $failed = $false
    foreach ($property in $baseline.PSObject.Properties) {
        $name = $property.Name
        if (-not $medians.ContainsKey($name)) {
            Write-Warning "baseline benchmark $name was not produced by this run"
            continue
        }
        $base = [double]$property.Value
        $limit = $base * (1 + $TolerancePercent / 100.0)
        $current = [double]$medians[$name]
        $delta = if ($base -gt 0) { ($current - $base) / $base * 100.0 } else { 0 }
        if ($current -gt $limit) {
            Write-Host ("FAIL {0}: median={1:N0} baseline={2:N0} delta=+{3:N1}% limit=+{4:N0}%" -f $name, $current, $base, $delta, $TolerancePercent)
            $failed = $true
        }
        else {
            Write-Host ("PASS {0}: median={1:N0} baseline={2:N0} delta={3:+0.0;-0.0;0}%" -f $name, $current, $base, $delta)
        }
    }
    if ($failed) {
        Write-Host "benchmark gate FAILED (tolerance=$TolerancePercent%)"
        exit 1
    }
    Write-Host "benchmark gate PASSED (tolerance=$TolerancePercent%)"
}
finally {
    Pop-Location
}