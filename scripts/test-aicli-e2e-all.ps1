<#
.SYNOPSIS
  aicli E2E 全量入口（聚合）：E2E-DEBUG-01 → 02 → 03 顺序执行 + 断言基线校验。

.DESCRIPTION
  为什么需要它：
    - 三个场景共用同一构建产物（01 构建，02/03 -SkipBuild 复用），分开跑容易漂移；
    - "断言只增不减"必须机器校验：基线文件 scripts/e2e-assertion-baseline.json
      记录各 harness 源码里的 Add-Result / Add-Skip 调用点，任何断言被删除或
      改名都会让本脚本 FAIL（防止"悄悄删断言换绿"）。

  本脚本做三件事：
    1. 基线校验：静态抽取各 harness 源码的断言名，与基线对比——
       缺失 = FAIL（断言被删/改名），新增 = 提示（用 -UpdateBaseline 固化）；
    2. 顺序执行 01 → 02 → 03：02/03 始终带 -SkipBuild 复用 01 的二进制（-SkipBuild 时三者
      都复用已有产物）；各自写自己的证据目录，stdout/stderr 分别落盘；
    3. 聚合结论：读各场景 summary.json（字段名不同：01 = passed/failed，02/03 = pass/fail/skip，
       本脚本统一归一化；两套字段都没有 = schema 漂移，直接 FAIL 而不是记 0），
       并把 results 条数与 PASS+FAIL 交叉校验，产出 artifacts/aicli-e2e-all/<stamp>/summary.json
       与 run.log；任一场景 FAIL、缺 summary.json、计数对不上或基线缺失断言 → 退出码 1。

  退出码：0 = 无 FAIL；1 = 有 FAIL（明细同时打印并写入 summary.json）。

.PARAMETER ExePath
  被测二进制；缺省 <repo>/backend/.tmp/aicli-debug-e2e.exe（两个场景共用）。
.PARAMETER Port01 / Port02
  两个场景各自的监听端口；缺省 0 = 自动挑空闲端口。
.PARAMETER LanIp
  传给 02 的非回环请求源 IP；缺省由 02 自动探测。
.PARAMETER WebToken
  传给 02 的显式写令牌（>=16 位）；缺省用 02 的固定 e2e 令牌。
.PARAMETER Headless
  仅传给 01：启动参数追加 --headless（无人值守；02 不需要 provider，无此参数）。
.PARAMETER SkipBuild
  传给 01：跳过 go build 复用 ExePath（02 恒为 -SkipBuild）。
.PARAMETER ArtifactDir
  聚合证据目录；缺省 artifacts/aicli-e2e-all/<yyyyMMdd-HHmmss>。
  两个场景分别落在其下的 01-debug-endpoints/ 与 02-nonloopback-auth/。
.PARAMETER BaselinePath
  断言基线文件；缺省 scripts/e2e-assertion-baseline.json（相对仓库根）。
.PARAMETER UpdateBaseline
  用本次抽取结果重写基线（新增断言固化；基线文件缺失时也用它初始化）。
.PARAMETER BaselineOnly
  只做基线校验，不跑 E2E（CI 里的廉价门禁；断言被删时几秒内就能红）。

.EXAMPLE
  pwsh -NoProfile -File scripts/test-aicli-e2e-all.ps1

.EXAMPLE
  pwsh -NoProfile -File scripts/test-aicli-e2e-all.ps1 -SkipBuild -UpdateBaseline

.EXAMPLE
  pwsh -NoProfile -File scripts/test-aicli-e2e-all.ps1 -BaselineOnly
#>
[CmdletBinding()]
param(
    [string]$ExePath,
    [ValidateRange(0, 65535)][int]$Port01 = 0,
    [ValidateRange(0, 65535)][int]$Port02 = 0,
    [string]$LanIp = '',
    [string]$WebToken = 'e2e-nonloopback-token-0123456789',
    [switch]$Headless,
    [switch]$SkipBuild,
    [string]$ArtifactDir,
    [string]$BaselinePath = 'scripts/e2e-assertion-baseline.json',
    [switch]$UpdateBaseline,
    [switch]$BaselineOnly
)

$ErrorActionPreference = 'Stop'

$repoRoot = Split-Path -Parent $PSScriptRoot
$backend = Join-Path $repoRoot 'backend'

if ([string]::IsNullOrWhiteSpace($ArtifactDir)) {
    $stamp = Get-Date -Format 'yyyyMMdd-HHmmss'
    $ArtifactDir = Join-Path $repoRoot "artifacts/aicli-e2e-all/$stamp"
} elseif (-not [System.IO.Path]::IsPathRooted($ArtifactDir)) {
    $ArtifactDir = Join-Path $repoRoot $ArtifactDir
}
New-Item -ItemType Directory -Path $ArtifactDir -Force | Out-Null

if (-not [System.IO.Path]::IsPathRooted($BaselinePath)) {
    $BaselinePath = Join-Path $repoRoot $BaselinePath
}

$runLogPath = Join-Path $ArtifactDir 'run.log'
$summaryPath = Join-Path $ArtifactDir 'summary.json'

function Write-Log {
    param([string]$Message)
    $line = '[{0}] {1}' -f (Get-Date -Format 'HH:mm:ss'), $Message
    Write-Host $line
    Add-Content -LiteralPath $runLogPath -Value $line -Encoding UTF8
}

$script:results = New-Object System.Collections.Generic.List[object]

function Add-Result {
    param([string]$Name, [bool]$Passed, [string]$Detail)
    $script:results.Add([pscustomobject]@{ name = $Name; passed = $Passed; detail = $Detail })
    $tag = 'FAIL'
    if ($Passed) { $tag = 'PASS' }
    Write-Log ("[{0}] {1} :: {2}" -f $tag, $Name, $Detail)
}

# 场景清单：id / 脚本相对路径 / 证据子目录。
$script:scenarios = @(
    [pscustomobject]@{
        id       = 'E2E-DEBUG-01'
        script   = 'scripts/test-aicli-debug-endpoints-e2e.ps1'
        dirName  = '01-debug-endpoints'
        artifact = $null
        exitCode = $null
        pass     = 0
        fail     = 0
        skip     = 0
        summary  = $null
    },
    [pscustomobject]@{
        id       = 'E2E-DEBUG-02'
        script   = 'scripts/test-aicli-debug-endpoints-e2e-nonloopback.ps1'
        dirName  = '02-nonloopback-auth'
        artifact = $null
        exitCode = $null
        pass     = 0
        fail     = 0
        skip     = 0
        summary  = $null
    },
    [pscustomobject]@{
        id       = 'E2E-DEBUG-03'
        script   = 'scripts/test-aicli-debug-endpoints-e2e-mesh.ps1'
        dirName  = '03-mesh'
        artifact = $null
        exitCode = $null
        pass     = 0
        fail     = 0
        skip     = 0
        summary  = $null
    }
)

# 抽取 harness 源码里的断言调用点（Add-Result / Add-Skip 的字面量或插值模板）。
# 只做静态抽取：与运行环境无关（无 LAN 网卡导致的 SKIP 不会误报"断言被删"）。
function Get-AssertionCallSites {
    param([string]$Path)
    $text = Get-Content -Raw -LiteralPath $Path
    $rx = [regex]'Add-(?:Result|Skip)\s+(?:"([^"]+)"|''([^'']+)'')'
    $names = New-Object System.Collections.Generic.List[string]
    foreach ($m in $rx.Matches($text)) {
        if ($m.Groups[1].Success) { $names.Add($m.Groups[1].Value) } else { $names.Add($m.Groups[2].Value) }
    }
    return @($names | Sort-Object -Unique)
}

# 解析 pwsh 可执行文件：优先当前宿主（pwsh 7），回退 Windows PowerShell。
function Resolve-PwshExe {
    $cmd = Get-Command pwsh -ErrorAction SilentlyContinue
    if ($null -ne $cmd -and -not [string]::IsNullOrWhiteSpace($cmd.Source)) { return $cmd.Source }
    $fallback = Join-Path $PSHOME 'powershell.exe'
    if (Test-Path -LiteralPath $fallback) { return $fallback }
    throw '未找到 pwsh / powershell 可执行文件'
}

function Quote-Arg {
    param([string]$Value)
    if ($Value -match '\s') { return '"' + $Value + '"' }
    return $Value
}

# ------------------------------------------------------------------
# 0. 断言基线校验（静态抽取，与运行环境无关）
# ------------------------------------------------------------------
$pwshExe = Resolve-PwshExe
$baselineExists = Test-Path -LiteralPath $BaselinePath
$baselineExpected = @{}
if ($baselineExists) {
    $baseline = Get-Content -Raw -LiteralPath $BaselinePath | ConvertFrom-Json
    foreach ($s in $baseline.scenarios) { $baselineExpected[[string]$s.id] = @($s.assertions) }
}

$extracted = @{}
foreach ($sc in $script:scenarios) {
    $scriptPath = Join-Path $repoRoot $sc.script
    if (-not (Test-Path -LiteralPath $scriptPath)) { throw "harness 不存在：$scriptPath" }
    $extracted[$sc.id] = Get-AssertionCallSites -Path $scriptPath
}

foreach ($sc in $script:scenarios) {
    $actual = @($extracted[$sc.id])
    if (-not $baselineExists) {
        Add-Result ("baseline/{0}" -f $sc.id) $true `
            ("基线文件不存在：{0}（本次只抽取 {1} 个断言调用点；用 -UpdateBaseline 初始化）" -f $BaselinePath, $actual.Count)
        continue
    }
    if (-not $baselineExpected.ContainsKey($sc.id)) {
        Add-Result ("baseline/{0}" -f $sc.id) $false "基线缺少该场景条目（用 -UpdateBaseline 补齐）"
        continue
    }
    $expected = @($baselineExpected[$sc.id])
    $missing = @($expected | Where-Object { $_ -notin $actual })
    $added = @($actual | Where-Object { $_ -notin $expected })
    if ($missing.Count -gt 0) {
        Add-Result ("baseline/{0}" -f $sc.id) $false `
            ("断言被删除/改名：缺失 {0} 条 -> {1}" -f $missing.Count, ($missing -join ', '))
    } else {
        $detail = "expected={0} actual={1}" -f $expected.Count, $actual.Count
        if ($added.Count -gt 0) {
            $detail += ("；新增 {0} 条（用 -UpdateBaseline 固化）：{1}" -f $added.Count, ($added -join ', '))
        }
        Add-Result ("baseline/{0}" -f $sc.id) $true $detail
    }
}

if ($UpdateBaseline) {
    $baselineDoc = [ordered]@{
        version      = 1
        generated_at = (Get-Date).ToUniversalTime().ToString('yyyy-MM-ddTHH:mm:ssZ')
        note         = '断言只增不减：删除或改名断言会让 scripts/test-aicli-e2e-all.ps1 失败；新增用 -UpdateBaseline 固化。'
        scenarios    = @(
            foreach ($sc in $script:scenarios) {
                [ordered]@{
                    id         = $sc.id
                    script     = $sc.script
                    assertions = @($extracted[$sc.id])
                }
            }
        )
    }
    $baselineJson = $baselineDoc | ConvertTo-Json -Depth 6
    [System.IO.File]::WriteAllText($BaselinePath, $baselineJson, (New-Object System.Text.UTF8Encoding($false)))
    Write-Log "baseline: 已写入 $BaselinePath"
}

# ------------------------------------------------------------------
# 1. 顺序执行 01 → 02 → 03（02/03 恒为 -SkipBuild，复用 01 的二进制）
# ------------------------------------------------------------------
if (-not $BaselineOnly) {
    foreach ($sc in $script:scenarios) {
        $scriptPath = Join-Path $repoRoot $sc.script
        $sc.artifact = Join-Path $ArtifactDir $sc.dirName
        $childArgs = @('-NoProfile', '-File', (Quote-Arg $scriptPath), '-ArtifactDir', (Quote-Arg $sc.artifact))
        if (-not [string]::IsNullOrWhiteSpace($ExePath)) {
            $childArgs += @('-ExePath', (Quote-Arg $ExePath))
        }
        if ($sc.id -eq 'E2E-DEBUG-01') {
            if ($Port01 -gt 0) { $childArgs += @('-Port', "$Port01") }
            if ($SkipBuild) { $childArgs += '-SkipBuild' }
            if ($Headless) { $childArgs += '-Headless' }
        } elseif ($sc.id -eq 'E2E-DEBUG-02') {
            if ($Port02 -gt 0) { $childArgs += @('-Port', "$Port02") }
            if (-not [string]::IsNullOrWhiteSpace($LanIp)) { $childArgs += @('-LanIp', (Quote-Arg $LanIp)) }
            if (-not [string]::IsNullOrWhiteSpace($WebToken)) { $childArgs += @('-WebToken', (Quote-Arg $WebToken)) }
            $childArgs += '-SkipBuild'
        } else {
            # 03（多进程网格）：不占固定端口（--pprof 随机端口，端口从节点档案读），
            # 复用 01 的 aicli 二进制；aicli-mesh 缺失时由 03 自行构建。
            $childArgs += '-SkipBuild'
        }

        $childOut = Join-Path $ArtifactDir ("{0}.stdout.log" -f $sc.dirName)
        $childErr = Join-Path $ArtifactDir ("{0}.stderr.log" -f $sc.dirName)
        Write-Log ("run {0}: {1} {2}" -f $sc.id, $pwshExe, ($childArgs -join ' '))
        $proc = Start-Process -FilePath $pwshExe -ArgumentList $childArgs -WorkingDirectory $repoRoot -PassThru -Wait `
            -RedirectStandardOutput $childOut -RedirectStandardError $childErr
        $sc.exitCode = $proc.ExitCode

        $childSummaryPath = Join-Path $sc.artifact 'summary.json'
        if (Test-Path -LiteralPath $childSummaryPath) {
            $sc.summary = Get-Content -Raw -LiteralPath $childSummaryPath | ConvertFrom-Json
            # 两个 harness 的 summary 字段名不同（历史原因，未强行统一，避免破坏既有消费方）：
            #   01: passed / failed，无 skip 字段
            #   02: pass / fail / skip
            # 这里统一归一化。两套字段都不存在 = schema 漂移 → 直接 FAIL；
            # 绝不能"读不到就记 0"，否则字段改名会把 FAIL 伪装成 PASS（E2E-DEBUG-01 曾实测踩到）。
            $summaryProps = @($sc.summary.PSObject.Properties.Name)
            $hasPassField = ($summaryProps -contains 'pass') -or ($summaryProps -contains 'passed')
            if ($hasPassField) {
                if ($summaryProps -contains 'pass') { $sc.pass = [int]$sc.summary.pass } else { $sc.pass = [int]$sc.summary.passed }
                if ($summaryProps -contains 'fail') { $sc.fail = [int]$sc.summary.fail } else { $sc.fail = [int]$sc.summary.failed }
                if ($summaryProps -contains 'skip') { $sc.skip = [int]$sc.summary.skip } else { $sc.skip = 0 }
                # 交叉校验：results 条数必须等于 PASS+FAIL（SKIP 单独记在 skipped 数组里）。
                # 这条能抓住"计数被改小/结果被吞"的静默失败。
                $resultCount = 0
                if ($summaryProps -contains 'results') { $resultCount = @($sc.summary.results).Count }
                $countOk = ($resultCount -eq ($sc.pass + $sc.fail))
                Add-Result ("scenario/{0}" -f $sc.id) (($sc.exitCode -eq 0) -and ($sc.fail -eq 0) -and $countOk) `
                    ("exit={0} PASS={1} FAIL={2} SKIP={3} results={4} dir={5}" -f $sc.exitCode, $sc.pass, $sc.fail, $sc.skip, $resultCount, $sc.artifact)
            } else {
                Add-Result ("scenario/{0}" -f $sc.id) $false `
                    ("exit={0}；summary.json 缺少 pass/passed 字段（schema 漂移？见 {1}）" -f $sc.exitCode, $childSummaryPath)
            }
        } else {
            Add-Result ("scenario/{0}" -f $sc.id) $false `
                ("exit={0}；未生成 summary.json（见 {1}）" -f $sc.exitCode, $childErr)
        }
    }
}

# ------------------------------------------------------------------
# 2. 聚合结论
# ------------------------------------------------------------------
$passCount = @($script:results | Where-Object { $_.passed }).Count
$failCount = @($script:results | Where-Object { -not $_.passed }).Count

$summary = [ordered]@{
    scenario         = 'E2E-ALL (debug endpoints + non-loopback auth)'
    started_at       = (Get-Date).ToUniversalTime().ToString('yyyy-MM-ddTHH:mm:ssZ')
    exe_path         = $ExePath
    artifact_dir     = $ArtifactDir
    baseline_path    = $BaselinePath
    baseline_updated = [bool]$UpdateBaseline
    baseline_only    = [bool]$BaselineOnly
    pass             = $passCount
    fail             = $failCount
    scenarios        = @(
        foreach ($sc in $script:scenarios) {
            $childResults = @()
            $childSkips = @()
            if ($null -ne $sc.summary) {
                if ($null -ne $sc.summary.results) { $childResults = @($sc.summary.results) }
                if ($null -ne $sc.summary.skipped) { $childSkips = @($sc.summary.skipped) }
            }
            [ordered]@{
                id           = $sc.id
                script       = $sc.script
                artifact_dir = $sc.artifact
                exit_code    = $sc.exitCode
                pass         = $sc.pass
                fail         = $sc.fail
                skip         = $sc.skip
                results      = $childResults
                skipped      = $childSkips
            }
        }
    )
    results          = $script:results
}
$summary | ConvertTo-Json -Depth 12 | Set-Content -LiteralPath $summaryPath -Encoding UTF8

Write-Log ("总结: PASS={0} FAIL={1}；证据目录 {2}" -f $passCount, $failCount, $ArtifactDir)
foreach ($r in $script:results) {
    if (-not $r.passed) { Write-Log ("FAIL 明细: {0} :: {1}" -f $r.name, $r.detail) }
}

if ($failCount -gt 0) { exit 1 }
exit 0
