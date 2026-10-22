# Tool-layer benchmark baseline runner.
#
# Design reference: docs/plan/tool-layer-abstraction-design-20261022.md §15.2
# Baseline report: docs/performance/tool-layer-benchmark-baseline-20261022.md
#
# Usage:
#   pwsh -File backend/scripts/tool-layer-bench.ps1
#   $env:AICLI_TOOLCORE_BENCH_LARGE='1'; pwsh -File backend/scripts/tool-layer-bench.ps1
#
# The 100k-file corpus is opt-in because it creates ~100 MB of files and one
# grep pass takes tens of seconds on a laptop.

$ErrorActionPreference = 'Stop'

$backend = Split-Path -Parent $PSScriptRoot
Push-Location $backend
try {
    # Conservative memory settings: the baseline report is produced on a
    # memory-constrained Windows host, so GOGC/GOMAXPROCS are pinned.
    $env:GOGC = '20'
    $env:GOMAXPROCS = '2'

    Write-Host '== medium-repo / 10k files: glob + grep =='
    go test -run '^$' `
        -bench 'BenchmarkGlob10kNoMatch|BenchmarkGrep10kNoMatch' `
        -benchmem -count=3 ./internal/toolcore/searchcore
    if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }

    Write-Host '== 256-tool registry list =='
    go test -run '^$' `
        -bench 'BenchmarkRegistryDefinitions256|BenchmarkRegistryList256' `
        -benchmem -count=3 ./internal/toolcore
    if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }

    if ($env:AICLI_TOOLCORE_BENCH_LARGE -eq '1') {
        Write-Host '== large-repo / 100k files: glob + grep (peak-heap sampled) =='
        go test -run '^$' `
            -bench 'BenchmarkGlob100kNoMatch|BenchmarkGrep100kNoMatch' `
            -benchmem -benchtime=1x -count=1 ./internal/toolcore/searchcore
        if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }
    }
    else {
        Write-Host 'skipped 100k corpus; set AICLI_TOOLCORE_BENCH_LARGE=1 to run it'
    }
}
finally {
    Pop-Location
}