[CmdletBinding()]
param(
    [string]$OutputDir = "output/reliability-evals",
    [int]$TimeoutSeconds = 300
)

Set-StrictMode -Version Latest
$ErrorActionPreference = "Stop"

function Resolve-RepoPath {
    param(
        [Parameter(Mandatory = $true)][string]$Root,
        [Parameter(Mandatory = $true)][string]$Path
    )
    if ([System.IO.Path]::IsPathRooted($Path)) {
        return [System.IO.Path]::GetFullPath($Path)
    }
    return [System.IO.Path]::GetFullPath((Join-Path $Root $Path))
}

function Write-JsonFile {
    param(
        [Parameter(Mandatory = $true)][string]$Path,
        [Parameter(Mandatory = $true)]$Value
    )
    $json = $Value | ConvertTo-Json -Depth 12
    $utf8 = New-Object System.Text.UTF8Encoding($false)
    [System.IO.File]::WriteAllText($Path, $json + "`n", $utf8)
}

function Convert-GoTestEvents {
    param([string[]]$Lines)
    $events = New-Object System.Collections.Generic.List[object]
    foreach ($line in $Lines) {
        if ([string]::IsNullOrWhiteSpace($line)) {
            continue
        }
        try {
            $event = $line | ConvertFrom-Json -ErrorAction Stop
            if ($null -ne $event.Action) {
                $events.Add($event)
            }
        }
        catch {
            continue
        }
    }
    return $events.ToArray()
}

function Test-GoEvent {
    param(
        [object[]]$Events,
        [string]$Action,
        [string]$TestName
    )
    return @($Events | Where-Object {
        $testProperty = $_.PSObject.Properties["Test"]
        $_.Action -eq $Action -and $null -ne $testProperty -and $testProperty.Value -eq $TestName
    }).Count -gt 0
}

function Get-GoTestEventCount {
    param(
        [object[]]$Events,
        [string]$Action
    )
    return @($Events | Where-Object {
        $_.Action -eq $Action -and $null -ne $_.PSObject.Properties["Test"]
    }).Count
}

$repoRoot = [System.IO.Path]::GetFullPath((Join-Path $PSScriptRoot ".."))
$backendDir = Join-Path $repoRoot "backend"
$outputRoot = Resolve-RepoPath -Root $repoRoot -Path $OutputDir
$reportPath = Join-Path $outputRoot "report.json"
New-Item -ItemType Directory -Path $outputRoot -Force | Out-Null

$scenarios = @()
$results = New-Object System.Collections.Generic.List[object]
$fatalError = ""
$startedAt = [DateTime]::UtcNow

$scenarios = @(
    [pscustomobject]@{
        Id = "tool.parent_deadline"; Category = "tool"; Package = "./internal/toolkit/tools"
        Test = "TestReliabilityEvalParentDeadlineStopsRealToolExecution"
        Description = "A shorter parent deadline stops real tool execution and preserves structured timeout metadata."
        RequiredSubtests = @()
    },
    [pscustomobject]@{
        Id = "tool.timeout_retry"; Category = "tool"; Package = "./internal/toolbroker"
        Test = "TestReliabilityEvalBrokerTimeoutRetryUsesNewInvocationWithoutDuplicateSideEffect"
        Description = "A timed-out broker invocation can be retried without committing duplicate side effects."
        RequiredSubtests = @()
    },
    [pscustomobject]@{
        Id = "tool.large_output_artifact"; Category = "tool"; Package = "./internal/agent"
        Test = "TestReActLoop_Run_UsesOutputGatewayForToolResults"
        Description = "Large tool output is reduced inline while the full artifact remains readable."
        RequiredSubtests = @()
    },
    [pscustomobject]@{
        Id = "background.alias_restart"; Category = "background"; Package = "./internal/toolbroker"
        Test = "TestBrokerBackgroundAliasSurvivesManagerRestart"
        Description = "Background aliases remain queryable after manager restart."
        RequiredSubtests = @()
    },
    [pscustomobject]@{
        Id = "background.orphan_reconcile"; Category = "background"; Package = "./internal/background"
        Test = "TestManagerRecoversPendingAndMarksInterruptedRunningJobsOrphaned"
        Description = "Restart reconciliation requeues pending work and marks interrupted work orphaned."
        RequiredSubtests = @()
    },
    [pscustomobject]@{
        Id = "tool.write_idempotency"; Category = "tool"; Package = "./internal/toolkit/tools"
        Test = "TestReliabilityEvalWriteIdempotencyProtection"
        Description = "Write and append retries use revision or offset preconditions without duplication."
        RequiredSubtests = @()
    },
    [pscustomobject]@{
        Id = "context.long_session_compact"; Category = "context"; Package = "./internal/contextmgr"
        Test = "TestReliabilityEvalLongSessionCompactStateRetention"
        Description = "50, 100, and 200 turn sessions retain active state and facts across compaction."
        RequiredSubtests = @("turns_50", "turns_100", "turns_200")
    },
    [pscustomobject]@{
        Id = "context.goal_scope"; Category = "context"; Package = "./internal/contextmgr"
        Test = "TestReliabilityEvalGoalScopedToolReplayIsolation"
        Description = "Foreign goal messages and matching tool-call halves are removed atomically."
        RequiredSubtests = @()
    },
    [pscustomobject]@{
        Id = "context.no_active_goal"; Category = "context"; Package = "./internal/contextmgr"
        Test = "TestReliabilityEvalNoActiveGoalRejectsGoalOwnedHistory"
        Description = "Goal-owned history is excluded when no goal is active."
        RequiredSubtests = @()
    },
    [pscustomobject]@{
        Id = "context.checkpoint_reuse"; Category = "context"; Package = "./internal/contextmgr"
        Test = "TestManager_BuildReusesCheckpointWithoutDuplicatingLedger"
        Description = "Repeated compaction reuses completed checkpoints instead of duplicating ledger state."
        RequiredSubtests = @()
    }
)

$scenarios += @(
    [pscustomobject]@{
        Id = "team.approval_resume"; Category = "team"; Package = "./cmd/aicli/commands"
        Test = "TestReliabilityEvalActorExecutorApprovalBridgeDelaysAndExecutesToolOnce"
        Description = "A genuinely delayed approval resumes asynchronous execution and invokes the tool once."
        RequiredSubtests = @()
    },
    [pscustomobject]@{
        Id = "team.member_timeout"; Category = "team"; Package = "./internal/team"
        Test = "TestReliabilityEvalTeammateTimeoutPreservesStructuredFailure"
        Description = "A member timeout preserves trace, timeout source, retryability, and summary."
        RequiredSubtests = @()
    },
    [pscustomobject]@{
        Id = "team.lead_fallback"; Category = "team"; Package = "./internal/team"
        Test = "TestReconcileTerminalTeamStatePublishesFallbackSummaryFailureMetadata"
        Description = "An injected Lead summary failure falls back with durable structured failure metadata."
        RequiredSubtests = @()
    },
    [pscustomobject]@{
        Id = "team.runtime_restart"; Category = "team"; Package = "./internal/team"
        Test = "TestReliabilityEvalTeammateRuntimeRestartRecoversStructuredOutcome"
        Description = "Structured teammate outcome is recovered after the SQLite store is closed and reopened."
        RequiredSubtests = @()
    },
    [pscustomobject]@{
        Id = "team.mailbox_idempotency"; Category = "team"; Package = "./internal/agentcontrol"
        Test = "TestSQLiteGlobalMailboxRegistryStoreDoesNotWakeOnIdempotentRefresh"
        Description = "Duplicate mailbox delivery refreshes neither rows nor consumer wake notifications."
        RequiredSubtests = @()
    },
    [pscustomobject]@{
        Id = "team.dependency_failure"; Category = "team"; Package = "./internal/team"
        Test = "TestReliabilityEvalTeamDependencyFailurePropagation"
        Description = "Dependency failure recursively terminates blocked descendants and retains partial results."
        RequiredSubtests = @()
    },
    [pscustomobject]@{
        Id = "team.parent_turn_end"; Category = "team"; Package = "./internal/team"
        Test = "TestOrchestratorExecuteAssignmentCompletesAfterCallerContextCanceled"
        Description = "A child assignment completes after the parent turn context ends."
        RequiredSubtests = @()
    },
    [pscustomobject]@{
        Id = "team.partial_results"; Category = "team"; Package = "./internal/team"
        Test = "TestReconcileTerminalTeamStatePreservesPartialResults"
        Description = "Terminal fallback retains successful results beside failed member outcomes."
        RequiredSubtests = @()
    }
)

$goVersion = ""
$oldGoMaxProcs = $env:GOMAXPROCS
$hasNativePreference = Test-Path variable:PSNativeCommandUseErrorActionPreference
$oldNativePreference = $null
if ($hasNativePreference) {
    $oldNativePreference = $PSNativeCommandUseErrorActionPreference
    $PSNativeCommandUseErrorActionPreference = $false
}

try {
    if (-not (Get-Command go -ErrorAction SilentlyContinue)) {
        throw "Required command 'go' was not found in PATH."
    }
    $goVersion = (& go version).Trim()
    if ($LASTEXITCODE -ne 0) {
        throw "Unable to resolve Go version."
    }
    $env:GOMAXPROCS = "2"

    foreach ($scenario in $scenarios) {
        Write-Host "==> Reliability eval: $($scenario.Id)"
        $watch = [System.Diagnostics.Stopwatch]::StartNew()
        $safeID = $scenario.Id -replace '[^0-9A-Za-z._-]', '-'
        $logPath = Join-Path $outputRoot "$safeID.log"
        $rawLines = @()
        $exitCode = -1
        Push-Location $backendDir
        try {
            $pattern = "^$([regex]::Escape($scenario.Test))$"
            $rawLines = @(& go test -json -p=1 $scenario.Package -run $pattern -count=1 "-timeout=$($TimeoutSeconds)s" 2>&1 |
                ForEach-Object { [string]$_ })
            $exitCode = $LASTEXITCODE
        }
        finally {
            Pop-Location
        }
        $watch.Stop()
        $utf8 = New-Object System.Text.UTF8Encoding($false)
        [System.IO.File]::WriteAllLines($logPath, [string[]]$rawLines, $utf8)

        $events = Convert-GoTestEvents -Lines $rawLines
        $rootExecuted = Test-GoEvent -Events $events -Action "run" -TestName $scenario.Test
        $rootPassed = Test-GoEvent -Events $events -Action "pass" -TestName $scenario.Test
        $missingSubtests = New-Object System.Collections.Generic.List[string]
        foreach ($subtest in $scenario.RequiredSubtests) {
            $fullName = "$($scenario.Test)/$subtest"
            if (-not (Test-GoEvent -Events $events -Action "run" -TestName $fullName) -or
                -not (Test-GoEvent -Events $events -Action "pass" -TestName $fullName)) {
                $missingSubtests.Add($fullName)
            }
        }

        $passed = $exitCode -eq 0 -and $rootExecuted -and $rootPassed -and $missingSubtests.Count -eq 0
        $failureReasons = New-Object System.Collections.Generic.List[string]
        if ($exitCode -ne 0) { $failureReasons.Add("go test exit code $exitCode") }
        if (-not $rootExecuted) { $failureReasons.Add("root test did not execute") }
        if (-not $rootPassed) { $failureReasons.Add("root test did not pass") }
        if ($missingSubtests.Count -gt 0) {
            $failureReasons.Add("missing or failed subtests: $([string]::Join(', ', $missingSubtests.ToArray()))")
        }

        $results.Add([pscustomobject]@{
            id = $scenario.Id
            category = $scenario.Category
            package = $scenario.Package
            test = $scenario.Test
            description = $scenario.Description
            required_subtests = @($scenario.RequiredSubtests)
            required_subtests_passed = $scenario.RequiredSubtests.Count - $missingSubtests.Count
            executed = $rootExecuted
            passed = $passed
            exit_code = $exitCode
            run_event_count = Get-GoTestEventCount -Events $events -Action "run"
            pass_event_count = Get-GoTestEventCount -Events $events -Action "pass"
            duration_ms = $watch.ElapsedMilliseconds
            log_path = [System.IO.Path]::GetRelativePath($repoRoot, $logPath).Replace([char]92, [char]47)
            failure = [string]::Join("; ", $failureReasons.ToArray())
        })
    }
}
catch {
    $fatalError = $_.Exception.Message
}
finally {
    $env:GOMAXPROCS = $oldGoMaxProcs
    if ($hasNativePreference) {
        $PSNativeCommandUseErrorActionPreference = $oldNativePreference
    }
}

$finishedAt = [DateTime]::UtcNow
$passedCount = @($results | Where-Object { $_.passed }).Count
$failedCount = $scenarios.Count - $passedCount
$longSessionResults = @($results | Where-Object { $_.id -eq "context.long_session_compact" })
$constraintSampleCount = 3
$constraintPassedCount = if ($longSessionResults.Count -eq 1) {
    [int]$longSessionResults[0].required_subtests_passed
} else {
    0
}
$constraintRetentionRate = [Math]::Round(100 * $constraintPassedCount / $constraintSampleCount, 2)
$goalScenarioIDs = @("context.long_session_compact", "context.goal_scope")
$goalDriftBlockers = @($results | Where-Object {
    $goalScenarioIDs -contains $_.id -and -not $_.passed
}).Count + ($goalScenarioIDs.Count - @($results | Where-Object { $goalScenarioIDs -contains $_.id }).Count)
$partialResultScenarioIDs = @("team.dependency_failure", "team.partial_results")
$partialResultsPassed = @($results | Where-Object {
    $partialResultScenarioIDs -contains $_.id -and $_.passed
}).Count
$partialResultsRetentionRate = [Math]::Round(
    100 * $partialResultsPassed / $partialResultScenarioIDs.Count,
    2
)
$recoveryScenarioIDs = @(
    "tool.timeout_retry",
    "background.alias_restart",
    "background.orphan_reconcile",
    "team.approval_resume",
    "team.runtime_restart",
    "team.mailbox_idempotency",
    "team.dependency_failure",
    "team.parent_turn_end"
)
$recoveryPassed = @($results | Where-Object {
    $recoveryScenarioIDs -contains $_.id -and $_.passed
}).Count
$automaticRecoveryRate = [Math]::Round(100 * $recoveryPassed / $recoveryScenarioIDs.Count, 2)
$thresholdChecks = [ordered]@{
    user_constraint_retention = $constraintRetentionRate -ge 99
    goal_drift_blockers = $goalDriftBlockers -eq 0
    partial_results_retention = $partialResultsRetentionRate -ge 100
    automatic_recovery = $automaticRecoveryRate -ge 95
}
$thresholdsPassed = @($thresholdChecks.Values | Where-Object { -not $_ }).Count -eq 0
$allPassed = [string]::IsNullOrWhiteSpace($fatalError) -and
    $results.Count -eq $scenarios.Count -and $failedCount -eq 0 -and $thresholdsPassed
$report = [ordered]@{
    schema_version = 1
    generated_at = $finishedAt.ToString("o")
    repo_root = $repoRoot
    go_version = $goVersion
    passed = $allPassed
    fatal_error = $fatalError
    summary = [ordered]@{
        total = $scenarios.Count
        executed = $results.Count
        passed = $passedCount
        failed = $failedCount
        duration_ms = [int64]($finishedAt - $startedAt).TotalMilliseconds
    }
    metrics = [ordered]@{
        user_constraint_retention_pct = $constraintRetentionRate
        user_constraint_samples = $constraintSampleCount
        goal_drift_blockers = $goalDriftBlockers
        partial_results_retention_pct = $partialResultsRetentionRate
        partial_result_scenarios = $partialResultScenarioIDs.Count
        automatic_recovery_pct = $automaticRecoveryRate
        automatic_recovery_scenarios = $recoveryScenarioIDs.Count
    }
    quality_thresholds = [ordered]@{
        passed = $thresholdsPassed
        targets = [ordered]@{
            user_constraint_retention_pct = ">=99"
            goal_drift_blockers = "0"
            partial_results_retention_pct = "100"
            automatic_recovery_pct = ">=95"
        }
        checks = $thresholdChecks
    }
    scenarios = $results.ToArray()
}
Write-JsonFile -Path $reportPath -Value $report

Write-Host ""
Write-Host "Reliability eval report: $reportPath"
Write-Host "Passed: $passedCount / $($scenarios.Count)"
if (-not $allPassed) {
    if (-not [string]::IsNullOrWhiteSpace($fatalError)) {
        Write-Error $fatalError
    }
    foreach ($failed in @($results | Where-Object { -not $_.passed })) {
        Write-Error "$($failed.id): $($failed.failure)"
    }
    if (-not $thresholdsPassed) {
        Write-Error "Reliability quality thresholds were not satisfied."
    }
    exit 1
}
