[CmdletBinding()]
param(
    [string]$OutputDir = "output/release-gate",
    [string]$Version = "",
    [string]$PlaywrightVersion = "0.1.17",
    [switch]$SkipFrontendInstall,
    [switch]$SkipBrowserInstall,
    [ValidateRange(1, 86400)][int]$StepTimeoutSeconds = 1200
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
    $json = $Value | ConvertTo-Json -Depth 16
    $utf8 = New-Object System.Text.UTF8Encoding($false)
    [System.IO.File]::WriteAllText($Path, $json + "`n", $utf8)
}

function Get-ReportRelativePath {
    param([Parameter(Mandatory = $true)][string]$Path)
    return [System.IO.Path]::GetRelativePath($script:repoRoot, $Path).Replace([char]92, [char]47)
}

function Add-EvidenceError {
    param([Parameter(Mandatory = $true)][string]$Message)
    $script:evidenceErrors.Add($Message)
}

function Read-JsonEvidence {
    param(
        [Parameter(Mandatory = $true)][string]$Path,
        [Parameter(Mandatory = $true)][string]$Label,
        [switch]$Required
    )
    if (-not (Test-Path -LiteralPath $Path -PathType Leaf)) {
        if ($Required) {
            Add-EvidenceError "$Label is missing: $Path"
        }
        return $null
    }
    try {
        return Get-Content -LiteralPath $Path -Raw | ConvertFrom-Json -ErrorAction Stop
    }
    catch {
        Add-EvidenceError "$Label could not be parsed: $($_.Exception.Message)"
        return $null
    }
}

function Add-StepResult {
    param(
        [string]$Name,
        [bool]$Passed,
        [int]$ExitCode,
        [int64]$DurationMs,
        [string]$LogPath,
        [string]$Detail,
        [bool]$TimedOut = $false,
        [int]$TimeoutSeconds = 0
    )
    $script:stepResults.Add([pscustomobject]@{
        name = $Name
        passed = $Passed
        exit_code = $ExitCode
        duration_ms = $DurationMs
        log_path = $LogPath
        detail = $Detail
        timed_out = $TimedOut
        timeout_seconds = $TimeoutSeconds
    })
}

function Stop-ProcessTree {
    param([Parameter(Mandatory = $true)][System.Diagnostics.Process]$Process)

    if ($Process.HasExited) {
        return
    }
    try {
        $Process.Kill($true)
        if (-not $Process.WaitForExit(10000)) {
            throw "process tree did not exit within 10 seconds"
        }
    }
    catch {
        $treeError = $_.Exception.Message
        try {
            Stop-Process -Id $Process.Id -Force -ErrorAction Stop
            if (-not $Process.WaitForExit(5000)) {
                throw "process did not exit within 5 seconds"
            }
        }
        catch {
            throw "Unable to terminate process tree for PID $($Process.Id): $treeError; $($_.Exception.Message)"
        }
    }
}

function Write-NativeStepRunner {
    param([Parameter(Mandatory = $true)][string]$Path)

    $source = @'
[CmdletBinding()]
param([Parameter(Mandatory = $true)][string]$InvocationPath)

Set-StrictMode -Version Latest
$ErrorActionPreference = "Stop"
if (Test-Path variable:PSNativeCommandUseErrorActionPreference) {
    $PSNativeCommandUseErrorActionPreference = $false
}

$invocation = Get-Content -LiteralPath $InvocationPath -Raw |
    ConvertFrom-Json -ErrorAction Stop
Set-Location -LiteralPath ([string]$invocation.working_directory)
$command = [string]$invocation.command
$arguments = @($invocation.arguments | ForEach-Object { [string]$_ })
$exitCode = 0
try {
    & $command @arguments
    $nativeExitCode = Get-Variable LASTEXITCODE -ValueOnly -ErrorAction SilentlyContinue
    if ($null -ne $nativeExitCode) {
        $exitCode = [int]$nativeExitCode
    }
}
catch {
    [Console]::Error.WriteLine($_.Exception.ToString())
    $exitCode = 1
}
exit $exitCode
'@
    $utf8 = New-Object System.Text.UTF8Encoding($false)
    [System.IO.File]::WriteAllText($Path, $source + "`n", $utf8)
}

function Resolve-PowerShellExecutable {
    $candidates = New-Object System.Collections.Generic.List[string]
    try {
        $mainModule = [System.Diagnostics.Process]::GetCurrentProcess().MainModule.FileName
        if (-not [string]::IsNullOrWhiteSpace($mainModule)) {
            [void]$candidates.Add($mainModule)
        }
    }
    catch {
        # MainModule can be unavailable under some Linux process-permission contexts.
    }

    foreach ($commandName in @("pwsh", "pwsh.exe", "powershell", "powershell.exe")) {
        $resolved = Get-Command $commandName -ErrorAction SilentlyContinue
        if ($null -ne $resolved -and -not [string]::IsNullOrWhiteSpace([string]$resolved.Source)) {
            [void]$candidates.Add([string]$resolved.Source)
        }
    }

    if (-not [string]::IsNullOrWhiteSpace($PSHOME)) {
        foreach ($leaf in @("pwsh", "pwsh.exe", "powershell", "powershell.exe")) {
            [void]$candidates.Add((Join-Path $PSHOME $leaf))
        }
    }

    foreach ($candidate in $candidates) {
        if ([string]::IsNullOrWhiteSpace($candidate)) {
            continue
        }
        if (Test-Path -LiteralPath $candidate -PathType Leaf) {
            return [System.IO.Path]::GetFullPath($candidate)
        }
    }

    throw "Unable to resolve a PowerShell executable for native step execution."
}

function ConvertTo-ProcessArgumentString {
    param([Parameter(Mandatory = $true)][string[]]$Arguments)
    return ($Arguments | ForEach-Object {
        $value = [string]$_
        if ($value -match '[\s"]') {
            '"' + ($value -replace '"', '\"') + '"'
        } else {
            $value
        }
    }) -join ' '
}

function Invoke-NativeCommandWithTimeout {
    param(
        [Parameter(Mandatory = $true)][string]$RunnerPath,
        [Parameter(Mandatory = $true)][string]$InvocationPath,
        [Parameter(Mandatory = $true)][string]$WorkingDirectory,
        [Parameter(Mandatory = $true)][string]$Command,
        [Parameter(Mandatory = $true)][string[]]$Arguments,
        [Parameter(Mandatory = $true)][int]$TimeoutSeconds
    )

    Write-JsonFile -Path $InvocationPath -Value ([ordered]@{
        working_directory = $WorkingDirectory
        command = $Command
        arguments = @($Arguments)
    })

    $process = $null
    $stdoutTask = $null
    $stderrTask = $null
    $timedOut = $false
    $terminationError = ""
    $exitCode = -1
    $stdout = ""
    $stderr = ""
    try {
        $startInfo = New-Object System.Diagnostics.ProcessStartInfo
        $startInfo.FileName = if (-not [string]::IsNullOrWhiteSpace($script:powerShellExecutable)) {
            $script:powerShellExecutable
        } else {
            Resolve-PowerShellExecutable
        }
        $startInfo.WorkingDirectory = $WorkingDirectory
        $startInfo.UseShellExecute = $false
        $startInfo.CreateNoWindow = $true
        $startInfo.RedirectStandardOutput = $true
        $startInfo.RedirectStandardError = $true
        $runnerArguments = @(
            "-NoLogo", "-NoProfile", "-NonInteractive", "-File", $RunnerPath,
            "-InvocationPath", $InvocationPath
        )
        $argumentList = $null
        try {
            $argumentList = $startInfo.ArgumentList
        }
        catch {
            $argumentList = $null
        }
        if ($null -ne $argumentList) {
            foreach ($argument in $runnerArguments) {
                [void]$argumentList.Add($argument)
            }
        }
        else {
            $startInfo.Arguments = ConvertTo-ProcessArgumentString -Arguments $runnerArguments
        }

        $process = New-Object System.Diagnostics.Process
        $process.StartInfo = $startInfo
        if (-not $process.Start()) {
            throw "Failed to start native step runner."
        }
        $stdoutTask = $process.StandardOutput.ReadToEndAsync()
        $stderrTask = $process.StandardError.ReadToEndAsync()
        $timeoutMilliseconds = [int]([int64]$TimeoutSeconds * 1000)
        if (-not $process.WaitForExit($timeoutMilliseconds)) {
            $timedOut = $true
            $exitCode = 124
            try {
                Stop-ProcessTree -Process $process
            }
            catch {
                $terminationError = $_.Exception.Message
            }
        }
        else {
            $process.WaitForExit()
            $exitCode = $process.ExitCode
        }

        if ($process.HasExited) {
            $stdout = $stdoutTask.GetAwaiter().GetResult()
            $stderr = $stderrTask.GetAwaiter().GetResult()
        }
    }
    finally {
        if ($null -ne $process) {
            $process.Dispose()
        }
        Remove-Item -LiteralPath $InvocationPath -Force -ErrorAction SilentlyContinue
    }

    return [pscustomobject]@{
        exit_code = $exitCode
        timed_out = $timedOut
        termination_error = $terminationError
        stdout = $stdout
        stderr = $stderr
    }
}

function Invoke-NativeStep {
    param(
        [Parameter(Mandatory = $true)][string]$Name,
        [Parameter(Mandatory = $true)][string]$WorkingDirectory,
        [Parameter(Mandatory = $true)][string]$Command,
        [Parameter(Mandatory = $true)][string[]]$Arguments,
        [scriptblock]$PostCondition,
        [ValidateRange(1, 86400)][int]$TimeoutSeconds = $script:StepTimeoutSeconds
    )
    Write-Host "==> $Name"
    $watch = [System.Diagnostics.Stopwatch]::StartNew()
    $safeName = $Name -replace '[^0-9A-Za-z._-]', '-'
    $logPath = Join-Path $script:logsDir "$safeName.txt"
    $invocationPath = Join-Path $script:logsDir "$safeName.invocation.json"
    $lines = @()
    $exitCode = -1
    $timedOut = $false
    $terminationError = ""
    try {
        $result = Invoke-NativeCommandWithTimeout `
            -RunnerPath $script:nativeStepRunnerPath `
            -InvocationPath $invocationPath `
            -WorkingDirectory $WorkingDirectory `
            -Command $Command `
            -Arguments $Arguments `
            -TimeoutSeconds $TimeoutSeconds
        $exitCode = [int]$result.exit_code
        $timedOut = [bool]$result.timed_out
        $terminationError = [string]$result.termination_error
        if (-not [string]::IsNullOrWhiteSpace([string]$result.stdout)) {
            $lines += @([string]$result.stdout -split "`r?`n")
        }
        if (-not [string]::IsNullOrWhiteSpace([string]$result.stderr)) {
            $lines += @([string]$result.stderr -split "`r?`n")
        }
    }
    catch {
        $lines += $_.Exception.ToString()
    }
    if ($timedOut) {
        $lines += "Timed out after $TimeoutSeconds seconds; process tree termination was requested."
        if (-not [string]::IsNullOrWhiteSpace($terminationError)) {
            $lines += "Process tree termination error: $terminationError"
        }
    }
    if ($exitCode -eq 0 -and $null -ne $PostCondition) {
        try {
            $postConditionOutput = [string](& $PostCondition | Out-String)
            if (-not [string]::IsNullOrWhiteSpace($postConditionOutput)) {
                $lines += $postConditionOutput.Trim()
            }
        }
        catch {
            $lines += "Postcondition failed: $($_.Exception.Message)"
            $exitCode = 1
        }
    }
    $watch.Stop()
    $utf8 = New-Object System.Text.UTF8Encoding($false)
    [System.IO.File]::WriteAllLines($logPath, [string[]]$lines, $utf8)
    $relativeLog = Get-ReportRelativePath -Path $logPath
    $passed = $exitCode -eq 0
    $detail = if ($timedOut) {
        "timed out after $TimeoutSeconds seconds; process tree termination requested"
    }
    elseif ($passed) {
        "completed"
    }
    else {
        "command failed with exit code $exitCode"
    }
    if (-not $passed) {
        Write-Host "!! $Name failed (exit=$exitCode timed_out=$timedOut)"
        foreach ($line in @($lines | Select-Object -Last 40)) {
            Write-Host $line
        }
    }
    Add-StepResult -Name $Name -Passed $passed -ExitCode $exitCode `
        -DurationMs $watch.ElapsedMilliseconds -LogPath $relativeLog `
        -Detail $detail -TimedOut $timedOut -TimeoutSeconds $TimeoutSeconds
    if (-not $passed) {
        throw "$Name $detail. See $logPath"
    }
}

function Invoke-InternalStep {
    param(
        [Parameter(Mandatory = $true)][string]$Name,
        [Parameter(Mandatory = $true)][scriptblock]$Action
    )
    Write-Host "==> $Name"
    $watch = [System.Diagnostics.Stopwatch]::StartNew()
    $safeName = $Name -replace '[^0-9A-Za-z._-]', '-'
    $logPath = Join-Path $script:logsDir "$safeName.txt"
    $passed = $false
    $detail = ""
    try {
        $detail = [string](& $Action | Out-String)
        $detail = $detail.Trim()
        $passed = $true
    }
    catch {
        $detail = $_.Exception.ToString()
    }
    finally {
        $watch.Stop()
    }
    $utf8 = New-Object System.Text.UTF8Encoding($false)
    [System.IO.File]::WriteAllText($logPath, $detail + "`n", $utf8)
    $relativeLog = Get-ReportRelativePath -Path $logPath
    if (-not $passed) {
        Write-Host "!! $Name failed"
        foreach ($line in @(($detail -split "`r?`n") | Select-Object -Last 40)) {
            Write-Host $line
        }
    }
    Add-StepResult -Name $Name -Passed $passed -ExitCode $(if ($passed) { 0 } else { 1 }) `
        -DurationMs $watch.ElapsedMilliseconds -LogPath $relativeLog -Detail $detail
    if (-not $passed) {
        throw "$Name failed. See $logPath"
    }
}

$repoRoot = [System.IO.Path]::GetFullPath((Join-Path $PSScriptRoot ".."))
$backendDir = Join-Path $repoRoot "backend"
$frontendDir = Join-Path $repoRoot "frontend"
$outputRoot = Resolve-RepoPath -Root $repoRoot -Path $OutputDir
$logsDir = Join-Path $outputRoot "logs"
$packageOutput = Join-Path $outputRoot "package"
$reliabilityOutput = Join-Path $outputRoot "reliability-evals"
$browserOutput = Join-Path $outputRoot "playwright"
$reportPath = Join-Path $outputRoot "report.json"
$nestedReliabilityPath = Join-Path $reliabilityOutput "report.json"
$nestedBrowserPath = Join-Path $browserOutput "report.json"
$stepResults = New-Object System.Collections.Generic.List[object]
$evidenceErrors = New-Object System.Collections.Generic.List[string]
$failure = ""
$startedAt = [DateTime]::UtcNow
$expectedStepCount = 10
$requiredReliabilityScenarios = @(
    "tool.parent_deadline",
    "tool.timeout_retry",
    "tool.large_output_artifact",
    "background.alias_restart",
    "background.orphan_reconcile",
    "tool.write_idempotency",
    "context.long_session_compact",
    "context.goal_scope",
    "context.no_active_goal",
    "context.checkpoint_reuse",
    "team.approval_resume",
    "team.member_timeout",
    "team.lead_fallback",
    "team.runtime_restart",
    "team.mailbox_idempotency",
    "team.dependency_failure",
    "team.parent_turn_end",
    "team.partial_results"
)
$requiredBrowserChecks = @(
    "server.health",
    "build.provenance",
    "page.visible",
    "code.highlighting",
    "route.reload",
    "console.errors",
    "network.requests",
    "assets.status",
    "api.status",
    "artifacts.screenshot",
    "artifacts.trace",
    "artifacts.snapshot"
)

New-Item -ItemType Directory -Path $logsDir -Force | Out-Null
New-Item -ItemType Directory -Path $packageOutput -Force | Out-Null
$nativeStepRunnerPath = Join-Path $outputRoot ".native-step-runner.ps1"
Write-NativeStepRunner -Path $nativeStepRunnerPath
$script:powerShellExecutable = Resolve-PowerShellExecutable

$oldGoMaxProcs = $env:GOMAXPROCS
$oldCgoEnabled = $env:CGO_ENABLED
$hasNativePreference = Test-Path variable:PSNativeCommandUseErrorActionPreference
$oldNativePreference = $null
if ($hasNativePreference) {
    $oldNativePreference = $PSNativeCommandUseErrorActionPreference
    $PSNativeCommandUseErrorActionPreference = $false
}

$gitCommit = "unknown"
$gitDirty = $true
$goos = ""
$goarch = ""
$binaryPath = ""

try {
    foreach ($staleReport in @($reportPath, $nestedReliabilityPath, $nestedBrowserPath)) {
        if (Test-Path -LiteralPath $staleReport -PathType Leaf) {
            Remove-Item -LiteralPath $staleReport -Force
        }
    }
    foreach ($commandName in @("go", "git", "node", "pnpm", "npx", "pwsh")) {
        if (-not (Get-Command $commandName -ErrorAction SilentlyContinue)) {
            throw "Required command '$commandName' was not found in PATH."
        }
    }
    Write-Host "Native step PowerShell: $script:powerShellExecutable"
    $env:GOMAXPROCS = "2"
    $env:CGO_ENABLED = "0"

    $gitCommit = (& git -C $repoRoot rev-parse HEAD).Trim()
    if ($LASTEXITCODE -ne 0) { throw "Unable to resolve git commit." }
    $gitStatus = @(& git -C $repoRoot status --porcelain --untracked-files=normal)
    if ($LASTEXITCODE -ne 0) { throw "Unable to resolve git dirty state." }
    $gitDirty = $gitStatus.Count -gt 0

    if ([string]::IsNullOrWhiteSpace($Version)) {
        $Version = "gate-" + $gitCommit.Substring(0, [Math]::Min(12, $gitCommit.Length))
    }
    $Version = $Version.Trim()
    if ($Version -notmatch '^[0-9A-Za-z][0-9A-Za-z._+\-]*$') {
        throw "Version '$Version' is not valid for build metadata."
    }
    $safeVersion = $Version -replace '[^0-9A-Za-z._-]', '-'

    $goos = (& go env GOOS).Trim()
    if ($LASTEXITCODE -ne 0) { throw "Unable to resolve GOOS." }
    $goarch = (& go env GOARCH).Trim()
    if ($LASTEXITCODE -ne 0) { throw "Unable to resolve GOARCH." }
    $executableName = if ($goos -eq "windows") { "runtime-server.exe" } else { "runtime-server" }
    $packageDir = Join-Path $packageOutput "runtime-server-$safeVersion-$goos-$goarch"
    $binaryPath = Join-Path $packageDir $executableName

    Invoke-NativeStep -Name "go.test" -WorkingDirectory $backendDir -Command "go" `
        -Arguments @("test", "-p=1", "./...", "-count=1", "-timeout=600s")
    Invoke-NativeStep -Name "go.vet" -WorkingDirectory $backendDir -Command "go" `
        -Arguments @("vet", "-p=1", "./...")

    if ($SkipFrontendInstall) {
        Invoke-InternalStep -Name "frontend.install" -Action {
            if (-not (Test-Path -LiteralPath (Join-Path $frontendDir "node_modules") -PathType Container)) {
                throw "SkipFrontendInstall was set but frontend/node_modules is missing."
            }
            "skipped by request; existing node_modules verified"
        }
    }
    else {
        Invoke-NativeStep -Name "frontend.install" -WorkingDirectory $frontendDir -Command "pnpm" `
            -Arguments @("install", "--frozen-lockfile")
    }
    Invoke-NativeStep -Name "frontend.test" -WorkingDirectory $frontendDir -Command "pnpm" `
        -Arguments @("test", "--maxWorkers=2")
    Invoke-NativeStep -Name "frontend.build" -WorkingDirectory $frontendDir -Command "pnpm" `
        -Arguments @("build")

    Invoke-NativeStep -Name "reliability.evals" -WorkingDirectory $repoRoot -Command "pwsh" `
        -Arguments @(
            "-NoProfile", "-File", (Join-Path $PSScriptRoot "run-reliability-evals.ps1"),
            "-OutputDir", $reliabilityOutput
        ) -PostCondition {
            if (-not (Test-Path -LiteralPath $nestedReliabilityPath -PathType Leaf)) {
                throw "Reliability report is missing: $nestedReliabilityPath"
            }
            $evalReport = Get-Content -LiteralPath $nestedReliabilityPath -Raw |
                ConvertFrom-Json -ErrorAction Stop
            $evalScenarios = @($evalReport.scenarios)
            if (-not [bool]$evalReport.passed -or [int]$evalReport.summary.failed -ne 0 -or
                [int]$evalReport.summary.total -ne $evalScenarios.Count -or
                [int]$evalReport.summary.executed -ne $evalScenarios.Count -or
                [int]$evalReport.summary.passed -ne $evalScenarios.Count) {
                throw "Reliability report summary is incomplete or failed."
            }
            $failedScenarios = @($evalScenarios | Where-Object {
                -not [bool]$_.executed -or -not [bool]$_.passed
            })
            if ($failedScenarios.Count -gt 0) {
                throw "Reliability report contains unexecuted or failed scenarios."
            }
            if (-not [bool]$evalReport.quality_thresholds.passed -or
                [double]$evalReport.metrics.user_constraint_retention_pct -lt 99 -or
                [int]$evalReport.metrics.goal_drift_blockers -ne 0 -or
                [double]$evalReport.metrics.partial_results_retention_pct -lt 100 -or
                [double]$evalReport.metrics.automatic_recovery_pct -lt 95) {
                throw "Reliability report quality thresholds were not satisfied."
            }
            foreach ($requiredScenario in $requiredReliabilityScenarios) {
                if (@($evalScenarios | Where-Object { $_.id -eq $requiredScenario }).Count -ne 1) {
                    throw "Reliability report must contain scenario '$requiredScenario' exactly once."
                }
            }
            "validated $($evalScenarios.Count) reliability scenarios"
        }

    Invoke-NativeStep -Name "package.runtime_server" -WorkingDirectory $repoRoot -Command "pwsh" `
        -Arguments @(
            "-NoProfile", "-File", (Join-Path $PSScriptRoot "package-runtime-server.ps1"),
            "-OutputDir", $packageOutput,
            "-Version", $Version,
            "-Goos", $goos,
            "-Goarch", $goarch,
            "-SkipFrontendInstall",
            "-SkipTests"
        )
    if (-not (Test-Path -LiteralPath $binaryPath -PathType Leaf)) {
        throw "Package step did not produce expected binary: $binaryPath"
    }

    $browserArguments = @(
        "-NoProfile", "-File", (Join-Path $PSScriptRoot "test-runtime-server-browser-e2e.ps1"),
        "-BinaryPath", $binaryPath,
        "-OutputDir", $browserOutput,
        "-ExpectedVersion", $Version,
        "-ExpectedGitCommit", $gitCommit,
        "-ExpectedGitDirty", ([string]$gitDirty),
        "-PlaywrightVersion", $PlaywrightVersion
    )
    if ($SkipBrowserInstall) {
        $browserArguments += "-SkipBrowserInstall"
    }
    Invoke-NativeStep -Name "browser.e2e" -WorkingDirectory $repoRoot -Command "pwsh" `
        -Arguments $browserArguments

    Invoke-InternalStep -Name "provenance.verify" -Action {
        $browserReportPath = Join-Path $browserOutput "report.json"
        if (-not (Test-Path -LiteralPath $browserReportPath -PathType Leaf)) {
            throw "Browser report is missing: $browserReportPath"
        }
        $browserReport = Get-Content -LiteralPath $browserReportPath -Raw |
            ConvertFrom-Json -ErrorAction Stop
        if (-not [bool]$browserReport.passed) {
            throw "Browser report did not pass."
        }
        $browserChecks = @($browserReport.checks)
        if ([int]$browserReport.summary.expected_checks -ne $requiredBrowserChecks.Count -or
            [int]$browserReport.summary.executed_checks -ne $requiredBrowserChecks.Count -or
            [int]$browserReport.summary.passed_checks -ne $requiredBrowserChecks.Count -or
            $browserChecks.Count -ne $requiredBrowserChecks.Count) {
            throw "Browser report summary does not satisfy the required check contract."
        }
        foreach ($requiredBrowserCheck in $requiredBrowserChecks) {
            $matchingChecks = @($browserChecks | Where-Object {
                [string]$_.name -ceq $requiredBrowserCheck
            })
            if ($matchingChecks.Count -ne 1) {
                throw "Browser report must contain check '$requiredBrowserCheck' exactly once."
            }
            if (-not [bool]$matchingChecks[0].passed) {
                throw "Browser report check '$requiredBrowserCheck' did not pass."
            }
        }
        if ([string]$browserReport.health.backend.version -ne $Version -or
            [string]$browserReport.health.backend.git_commit -ne $gitCommit -or
            [bool]$browserReport.health.backend.git_dirty -ne $gitDirty) {
            throw "Runtime health backend provenance differs from the release gate inputs."
        }
        "version=$Version commit=$gitCommit dirty=$gitDirty frontend=$($browserReport.health.frontend.asset_manifest_hash)"
    }

    Invoke-NativeStep -Name "git.diff_check" -WorkingDirectory $repoRoot -Command "git" `
        -Arguments @("diff", "--check")
}
catch {
    $failure = $_.Exception.Message
}
finally {
    $env:GOMAXPROCS = $oldGoMaxProcs
    $env:CGO_ENABLED = $oldCgoEnabled
    if ($hasNativePreference) {
        $PSNativeCommandUseErrorActionPreference = $oldNativePreference
    }
}

$binaryHash = ""
$binarySize = 0
$packageSteps = @($stepResults | Where-Object { $_.name -eq "package.runtime_server" })
$packagePassed = $packageSteps.Count -eq 1 -and [bool]$packageSteps[0].passed
if ($packagePassed) {
    if ([string]::IsNullOrWhiteSpace($binaryPath) -or
        -not (Test-Path -LiteralPath $binaryPath -PathType Leaf)) {
        Add-EvidenceError "Packaged runtime-server binary is missing: $binaryPath"
    }
    else {
        try {
            $binaryHash = (Get-FileHash -LiteralPath $binaryPath -Algorithm SHA256).Hash.ToLowerInvariant()
            $binarySize = (Get-Item -LiteralPath $binaryPath).Length
        }
        catch {
            Add-EvidenceError "Packaged runtime-server metadata could not be read: $($_.Exception.Message)"
        }
    }
}
$reliabilitySteps = @($stepResults | Where-Object { $_.name -eq "reliability.evals" })
$browserSteps = @($stepResults | Where-Object { $_.name -eq "browser.e2e" })
$reliabilityReport = Read-JsonEvidence -Path $nestedReliabilityPath -Label "Reliability report" `
    -Required:($reliabilitySteps.Count -gt 0)
$browserReport = Read-JsonEvidence -Path $nestedBrowserPath -Label "Browser report" `
    -Required:($browserSteps.Count -gt 0)
$reliabilitySummary = $null
if ($null -ne $reliabilityReport) {
    $summaryProperty = $reliabilityReport.PSObject.Properties["summary"]
    if ($null -eq $summaryProperty) {
        Add-EvidenceError "Reliability report does not contain a summary."
    }
    else {
        $reliabilitySummary = $summaryProperty.Value
    }
}
$browserSummary = $null
if ($null -ne $browserReport) {
    $summaryProperty = $browserReport.PSObject.Properties["summary"]
    if ($null -eq $summaryProperty) {
        Add-EvidenceError "Browser report does not contain a summary."
    }
    else {
        $browserSummary = $summaryProperty.Value
    }
}
if ($evidenceErrors.Count -gt 0) {
    $evidenceFailure = "Evidence validation failed: $([string]::Join('; ', $evidenceErrors.ToArray()))"
    $failure = if ([string]::IsNullOrWhiteSpace($failure)) {
        $evidenceFailure
    } else {
        "$failure | $evidenceFailure"
    }
}
$finishedAt = [DateTime]::UtcNow
$passedSteps = @($stepResults | Where-Object { $_.passed }).Count
$failedSteps = @($stepResults | Where-Object { -not $_.passed }).Count
$notExecutedSteps = [Math]::Max(0, $expectedStepCount - $stepResults.Count)
$allPassed = [string]::IsNullOrWhiteSpace($failure) -and $evidenceErrors.Count -eq 0 -and
    $stepResults.Count -eq $expectedStepCount -and $passedSteps -eq $expectedStepCount

$report = [ordered]@{
    schema_version = 1
    generated_at = $finishedAt.ToString("o")
    passed = $allPassed
    failure = $failure
    duration_ms = [int64]($finishedAt - $startedAt).TotalMilliseconds
    step_timeout_seconds = $StepTimeoutSeconds
    release = [ordered]@{
        version = $Version
        git_commit = $gitCommit
        git_dirty = $gitDirty
        goos = $goos
        goarch = $goarch
        cgo_enabled = "0"
    }
    summary = [ordered]@{
        expected_steps = $expectedStepCount
        executed_steps = $stepResults.Count
        passed_steps = $passedSteps
        failed_steps = $failedSteps
        not_executed_steps = $notExecutedSteps
    }
    steps = $stepResults.ToArray()
    evidence_errors = $evidenceErrors.ToArray()
    binary = [ordered]@{
        path = $binaryPath
        size = $binarySize
        sha256 = $binaryHash
    }
    reliability_evals = $reliabilitySummary
    browser_e2e = $browserSummary
    artifacts = [ordered]@{
        output_directory = $outputRoot
        logs = $logsDir
        package = $packageOutput
        reliability_report = $nestedReliabilityPath
        browser_report = $nestedBrowserPath
    }
}
Write-JsonFile -Path $reportPath -Value $report

Write-Host ""
Write-Host "Release gate report: $reportPath"
Write-Host "Steps passed: $passedSteps / $expectedStepCount"
if (-not $allPassed) {
    Write-Error $(if ([string]::IsNullOrWhiteSpace($failure)) {
        "Release gate did not execute every required step."
    } else {
        $failure
    })
    exit 1
}
