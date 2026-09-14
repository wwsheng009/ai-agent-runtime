<#
.SYNOPSIS
  PR-4 单轮预算 / turn 生命周期 / 第 5 条 prompt cache 熔断的受控注入式验收。

.DESCRIPTION
  方案 docs/plan/ui-event-bridge-drop-hardening.md §6.4 第 6 条 / §8.2 的"注入式长任务"
  验收入口。长任务由 SequenceLLMProvider 脚本化注入（无网络、无真实 provider），
  走真实 ReActLoop / 事件桥 / 状态行 / `/debug` 文档渲染路径，逐项断言：

    1. 80% 软着陆提示出现且只注入一次（internal/agent）；
    2. 100% 硬边界优雅收尾，收尾文案写回可持久化历史、不静默截断（internal/agent）；
    3. TUI 状态行出现 `turn budget: …`（cmd/aicli/commands）；
    4. `/debug/chat/status` 文本区块透出 bridge 通告水位与 observe 终局水位（cmd/aicli/commands）；
    5. turn 生命周期事件与主 chat `--budget-tokens` 接线（internal/agent + commands）；
    6. prompt cache 熔断 / `UPSTREAM_INVALID_RESPONSE` 聚合（internal/agent）。

  真实终端渲染不在本脚本范围：见 §8.2 的两个 Windows Terminal E2E（需要交互桌面，
  分别覆盖统一渲染 marker 与终端渲染基线）。

.PARAMETER Count
  go test -count 值，默认 1。

.PARAMETER LogPath
  原始 go test 输出的日志路径；默认 artifacts/turn-budget-e2e-<yyyyMMdd-HHmmss>.log。

.EXAMPLE
  pwsh -File scripts/test-aicli-turn-budget-e2e.ps1

.EXAMPLE
  pwsh -File scripts/test-aicli-turn-budget-e2e.ps1 -Count 3 -LogPath artifacts/turn-budget-e2e.log
#>
[CmdletBinding()]
param(
    [ValidateRange(1, 10)]
    [int]$Count = 1,

    [string]$LogPath
)

$ErrorActionPreference = "Stop"

$repoRoot = Split-Path -Parent $PSScriptRoot
$backend = Join-Path $repoRoot "backend"

if ([string]::IsNullOrWhiteSpace($LogPath)) {
    $stamp = Get-Date -Format "yyyyMMdd-HHmmss"
    $LogPath = Join-Path $repoRoot "artifacts/turn-budget-e2e-$stamp.log"
}
elseif (-not [System.IO.Path]::IsPathRooted($LogPath)) {
    $LogPath = Join-Path $repoRoot $LogPath
}
$logDir = Split-Path -Parent $LogPath
if (-not (Test-Path -LiteralPath $logDir)) {
    New-Item -ItemType Directory -Path $logDir -Force | Out-Null
}

# 每组用例对应 §6.4 第 6 条的一项或多项验收断言；Pattern 是 go test -run 正则。
$cases = @(
    [pscustomobject]@{
        Name     = "agent/turn-budget"
        Package  = "./internal/agent/"
        Covers   = "80% 软着陆、100% 优雅硬边界、落点 C 生命周期事件、主 chat 预算缺省/优先级、第 5 条熔断与聚合"
        Pattern  = 'TestReActLoop_Run_(InjectsTurnBudgetSoftLandingOnce|TokenBudgetHardStopIsGraceful|TurnBudgetReminderEventCarriesTurnIdentity|EmitsTurnLifecycleEvents|TurnBudgetDefaultsFromLoopConfig|ExplicitBudgetOverridesLoopConfig|StepLimitReportsTurnBudgetReason)|TestPromptCacheBreaker|TestReActLoop_RetryReporter|TestReActLoop_EmitAggregatedRetryReport|TestPromptFingerprintFromRequest|TestEvaluateTurnBudget|TestTokensSpentFromBudget|TestFormatTurnBudgetDuration|TestTurnBudgetSoftLandingMessage|TestTurnBudgetHardStopMessage|TestNewTurnBudgetReminderMessage|TestNormalizeReminderKindKeepsTurnBudgetCanonical'
    },
    [pscustomobject]@{
        Name     = "commands/tui+bridge+debug"
        Package  = "./cmd/aicli/commands/"
        Covers   = "TUI 水位提示、桥接判读契约（turn 归属/跨轮清空）、/debug turn 区块、--budget-tokens 接线"
        Pattern  = 'TestChatInteractionCoordinator_(TurnBudgetHintOnStatusLine|StatusHintsKeepDegradationSuffix)|TestChatRuntimeEventBridge_(MirrorsTurnBudgetSoftLanding|IgnoresNonBudgetReminders|TurnBudgetIgnoresForeignTurn|TurnBudgetRequiresPrimarySession|BeginRunClearsTurnBudget)|TestChatDebugTurnMetricsBlockShowsBridgeWatermarkWithoutObserve|TestFormatChatDebugTurnWatermark|TestParseChatCommandOptions_BudgetTokens|TestBuildLocalChatLoopConfig_CopiesTurnBudgetTokens'
    },
    [pscustomobject]@{
        Name     = "runtimeobserve/turn-metrics"
        Package  = "./internal/runtimeobserve/"
        Covers   = "observe 侧 turn 指标聚合与 JSON 契约（running_turns/last_turn 水位字段）"
        Pattern  = 'TestCollectorTurnMetrics|TestProjectorAgentTurnPayloadKeepsWatermarkFields'
    }
)

$logLines = New-Object System.Collections.Generic.List[string]
$logLines.Add("# aicli turn-budget E2E (count=$Count) $(Get-Date -Format o)")
$results = New-Object System.Collections.Generic.List[object]

Push-Location $backend
try {
    foreach ($case in $cases) {
        $header = "`n===== $($case.Name) :: go test -count=$Count -v -run '$($case.Pattern)' $($case.Package) ====="
        Write-Host $header
        $logLines.Add($header)

        $output = & go test "-count=$Count" -v -run $case.Pattern $case.Package 2>&1
        $exit = $LASTEXITCODE
        $text = ($output | ForEach-Object { "$_" }) -join "`n"
        $logLines.Add($text)

        $pass = ([regex]::Matches($text, "(?m)^\s*--- PASS: ")).Count
        $fail = ([regex]::Matches($text, "(?m)^\s*--- FAIL: ")).Count
        if ($fail -gt 0 -or $exit -ne 0) {
            Write-Host "  -> FAIL (exit=$exit, pass=$pass, fail=$fail)" -ForegroundColor Red
            if ($fail -eq 0) {
                Write-Host "  (no '--- FAIL' parsed; go test exited $exit — build error?)" -ForegroundColor Red
            }
        }
        else {
            Write-Host "  -> PASS (pass=$pass)" -ForegroundColor Green
        }

        $results.Add([pscustomobject]@{
            Name    = $case.Name
            Package = $case.Package
            Covers  = $case.Covers
            Pass    = $pass
            Fail    = $fail
            Exit    = $exit
        })
    }
}
finally {
    Pop-Location
}

Set-Content -LiteralPath $LogPath -Value ($logLines -join "`n") -Encoding utf8

$failed = @($results | Where-Object { $_.Fail -gt 0 -or $_.Exit -ne 0 })
Write-Host ""
Write-Host "===== §6.4 第 6 条验收摘要 =====" -ForegroundColor Cyan
$results | Format-Table Name, Pass, Fail, Exit -AutoSize | Out-String | Write-Host
foreach ($r in $results) {
    Write-Host ("- {0}: {1}" -f $r.Name, $r.Covers)
}
Write-Host "raw log: $LogPath"

if ($failed.Count -gt 0) {
    Write-Host "RESULT: FAIL ($($failed.Count)/$($results.Count) groups failed)" -ForegroundColor Red
    exit 1
}

Write-Host "RESULT: PASS ($($results.Count)/$($results.Count) groups)" -ForegroundColor Green
exit 0
