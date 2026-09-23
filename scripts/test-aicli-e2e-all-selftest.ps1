<#
.SYNOPSIS
  scripts/test-aicli-e2e-all.ps1 的自测（负例驱动）：用桩 harness 验证聚合逻辑。

.DESCRIPTION
  聚合脚本是 CI 门禁，它必须"红了就是真的红了"。但它有三个关键分支，在真实 E2E 全绿时
  永远不会被触发，因此最容易悄悄腐坏：
    1. summary 字段归一化：01 用 passed/failed，02 用 pass/fail/skip；两套都读不到时
       必须 FAIL（曾实测踩到：01 显示 PASS=0，被 exit=0 掩盖）；
    2. 计数交叉校验：results 条数必须等于 PASS+FAIL（SKIP 另记），防止"结果被吞"；
    3. 子场景失败/缺 summary.json 时必须拉红。

  本脚本在 %TEMP% 下搭沙箱：桩 harness 按环境变量 E2E_STUB_MODE 伪造 summary.json，
  再运行**真实的**聚合脚本（从 scripts/ 复制过去），断言四种模式的退出码与关键日志。

  沙箱不接触真实 provider、真实终端、真实端口，也不改动仓库内任何文件。

  退出码：0 = 四种模式全部符合预期；1 = 有不符合预期（明细同时打印）。

.EXAMPLE
  pwsh -NoProfile -File scripts/test-aicli-e2e-all-selftest.ps1

.EXAMPLE
  pwsh -NoProfile -File scripts/test-aicli-e2e-all-selftest.ps1 -KeepSandbox
#>
[CmdletBinding()]
param(
    [switch]$KeepSandbox
)

$ErrorActionPreference = 'Stop'

$repoRoot = Split-Path -Parent $PSScriptRoot
$aggSource = Join-Path $repoRoot 'scripts/test-aicli-e2e-all.ps1'
if (-not (Test-Path -LiteralPath $aggSource)) { throw "聚合脚本不存在：$aggSource" }

$pwshExe = (Get-Command pwsh -ErrorAction SilentlyContinue).Source
if ([string]::IsNullOrWhiteSpace($pwshExe)) { $pwshExe = (Get-Command powershell -ErrorAction SilentlyContinue).Source }
if ([string]::IsNullOrWhiteSpace($pwshExe)) { throw '未找到 pwsh / powershell 可执行文件' }

$stamp = Get-Date -Format 'yyyyMMdd-HHmmss'
$sandbox = Join-Path $env:TEMP "aicli-e2e-agg-selftest-$stamp"
$sandboxScripts = Join-Path $sandbox 'scripts'
$null = New-Item -ItemType Directory -Path $sandboxScripts -Force
Copy-Item -LiteralPath $aggSource -Destination (Join-Path $sandboxScripts 'test-aicli-e2e-all.ps1') -Force
$agg = Join-Path $sandboxScripts 'test-aicli-e2e-all.ps1'
# 基线故意指向不存在的文件：基线校验会记为"文件不存在"（PASS），让本自测只聚焦聚合逻辑。
$baseline = Join-Path $sandboxScripts 'no-baseline.json'

# ---- 桩 harness：01（字段 passed/failed）----------------------------------
$stub01 = @'
[CmdletBinding()]
param(
    [string]$ArtifactDir,
    [string]$ExePath,
    [int]$Port = 0,
    [string]$Prompt = '',
    [switch]$Headless,
    [switch]$SkipBuild
)
$ErrorActionPreference = 'Stop'
if ([string]::IsNullOrWhiteSpace($ArtifactDir)) { throw 'stub01: 需要 -ArtifactDir' }
New-Item -ItemType Directory -Path $ArtifactDir -Force | Out-Null

$mode = $env:E2E_STUB_MODE
if ([string]::IsNullOrWhiteSpace($mode)) { $mode = 'ok' }

$results = @(
    [pscustomobject]@{ name = 'stub01/a'; passed = $true; detail = 'ok' },
    [pscustomobject]@{ name = 'stub01/b'; passed = $true; detail = 'ok' }
)

switch ($mode) {
    'ok'     { $doc = [ordered]@{ passed = 2; failed = 0; results = $results } }
    'schema' { $doc = [ordered]@{ passedX = 2; failedX = 0; results = $results } }
    'count'  { $doc = [ordered]@{ passed = 1; failed = 0; results = $results } }
    'fail'   {
        $results = @(
            [pscustomobject]@{ name = 'stub01/a'; passed = $true; detail = 'ok' },
            [pscustomobject]@{ name = 'stub01/c'; passed = $false; detail = 'boom' }
        )
        $doc = [ordered]@{ passed = 1; failed = 1; results = $results }
    }
    default  { throw "stub01: 未知 E2E_STUB_MODE=$mode" }
}

[System.IO.File]::WriteAllText((Join-Path $ArtifactDir 'summary.json'), ($doc | ConvertTo-Json -Depth 6), (New-Object System.Text.UTF8Encoding($false)))
Write-Host "stub01 mode=$mode artifact=$ArtifactDir"
if ($mode -eq 'fail') { exit 1 }
exit 0
'@

# ---- 桩 harness：02（字段 pass/fail/skip）---------------------------------
$stub02 = @'
[CmdletBinding()]
param(
    [string]$ArtifactDir,
    [string]$ExePath,
    [int]$Port = 0,
    [string]$ListenHost = '',
    [string]$WebToken = '',
    [string]$LanIp = '',
    [int]$StartupTimeoutSec = 0,
    [int]$ExitTimeoutSec = 0,
    [switch]$KeepAlive,
    [switch]$SkipBuild
)
$ErrorActionPreference = 'Stop'
if ([string]::IsNullOrWhiteSpace($ArtifactDir)) { throw 'stub02: 需要 -ArtifactDir' }
New-Item -ItemType Directory -Path $ArtifactDir -Force | Out-Null

$results = @(
    [pscustomobject]@{ name = 'stub02/a'; passed = $true; detail = 'ok' }
)
$doc = [ordered]@{ pass = 1; fail = 0; skip = 0; results = $results }
[System.IO.File]::WriteAllText((Join-Path $ArtifactDir 'summary.json'), ($doc | ConvertTo-Json -Depth 6), (New-Object System.Text.UTF8Encoding($false)))
Write-Host "stub02 ok artifact=$ArtifactDir"
exit 0
'@

$utf8 = New-Object System.Text.UTF8Encoding($false)
[System.IO.File]::WriteAllText((Join-Path $sandboxScripts 'test-aicli-debug-endpoints-e2e.ps1'), $stub01, $utf8)
[System.IO.File]::WriteAllText((Join-Path $sandboxScripts 'test-aicli-debug-endpoints-e2e-nonloopback.ps1'), $stub02, $utf8)

# ---- 用例表 ---------------------------------------------------------------
# 每条：stub 模式 / 期望聚合退出码 / 必须出现的日志片段（正则）
$cases = @(
    [pscustomobject]@{
        name    = 'ok：01(passed/failed) + 02(pass/fail/skip) 均正常'
        mode    = 'ok'
        exit    = 0
        expect  = @('scenario/E2E-DEBUG-01 .*PASS=2 FAIL=0 SKIP=0 results=2', '总结: PASS=4 FAIL=0')
    },
    [pscustomobject]@{
        name    = 'schema：01 字段被改名 -> 必须 FAIL（不能记 0 后通过）'
        mode    = 'schema'
        exit    = 1
        expect  = @('scenario/E2E-DEBUG-01 .*schema 漂移', '总结: PASS=3 FAIL=1')
    },
    [pscustomobject]@{
        name    = 'count：results 条数与 PASS+FAIL 对不上 -> 必须 FAIL'
        mode    = 'count'
        exit    = 1
        expect  = @('scenario/E2E-DEBUG-01 .*PASS=1 FAIL=0 SKIP=0 results=2', '总结: PASS=3 FAIL=1')
    },
    [pscustomobject]@{
        name    = 'fail：子场景退出码 1 且 FAIL=1 -> 必须拉红'
        mode    = 'fail'
        exit    = 1
        expect  = @('scenario/E2E-DEBUG-01 .*exit=1 PASS=1 FAIL=1', '总结: PASS=3 FAIL=1')
    }
)

Write-Host "[selftest] 沙箱: $sandbox"
$results = New-Object System.Collections.Generic.List[object]

foreach ($case in $cases) {
    $env:E2E_STUB_MODE = $case.mode
    $art = Join-Path $sandbox ("artifacts/{0}" -f $case.mode)
    $out = & $pwshExe -NoProfile -File $agg -SkipBuild -BaselinePath $baseline -ArtifactDir $art 2>&1 | Out-String
    $code = $LASTEXITCODE

    $problems = New-Object System.Collections.Generic.List[string]
    if ($code -ne $case.exit) { $problems.Add("退出码期望 $($case.exit)，实际 $code") }
    foreach ($pattern in $case.expect) {
        if ($out -notmatch $pattern) { $problems.Add("日志缺少 /$pattern/") }
    }
    $passed = ($problems.Count -eq 0)
    $tag = if ($passed) { 'PASS' } else { 'FAIL' }
    $detail = if ($passed) { "exit=$code" } else { ($problems -join '；') }
    Write-Host ("[{0}] selftest/{1} :: {2}" -f $tag, $case.mode, $detail)
    $results.Add([pscustomobject]@{ name = $case.mode; passed = $passed; detail = $detail })
}
Remove-Item Env:\E2E_STUB_MODE -ErrorAction SilentlyContinue

$passCount = @($results | Where-Object { $_.passed }).Count
$failCount = @($results | Where-Object { -not $_.passed }).Count
Write-Host ''
Write-Host ("总结: PASS={0} FAIL={1}；沙箱 {2}" -f $passCount, $failCount, $sandbox)

if (-not $KeepSandbox) {
    Remove-Item -LiteralPath $sandbox -Recurse -Force -ErrorAction SilentlyContinue
} else {
    Write-Host "[selftest] -KeepSandbox：保留沙箱供排查"
}

if ($failCount -gt 0) { exit 1 }
exit 0
