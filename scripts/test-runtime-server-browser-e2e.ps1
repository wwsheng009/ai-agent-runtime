[CmdletBinding()]
param(
    [Parameter(Mandatory = $true)][string]$BinaryPath,
    [string]$OutputDir = "output/playwright/runtime-server-e2e",
    [string]$ExpectedVersion = "",
    [string]$ExpectedGitCommit = "",
    [string]$ExpectedGitDirty = "",
    [string]$ExpectedFrontendManifestHash = "",
    [string]$FrontendBuildInfoPath = "backend/internal/webui/dist/build-info.json",
    [string]$PlaywrightVersion = "0.1.17",
    [int]$StartupTimeoutSeconds = 60,
    [switch]$SkipBrowserInstall
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

function Add-Check {
    param(
        [Parameter(Mandatory = $true)][string]$Name,
        [Parameter(Mandatory = $true)][bool]$Passed,
        [Parameter(Mandatory = $true)][string]$Detail
    )
    $script:checks.Add([pscustomobject]@{
        name = $Name
        passed = $Passed
        detail = $Detail
    })
    if (-not $Passed) {
        throw "$Name failed: $Detail"
    }
}

function Invoke-PlaywrightCli {
    param(
        [Parameter(Mandatory = $true)][string[]]$Arguments,
        [switch]$WithoutSession
    )
    $commandArgs = @("--yes", "--package", "@playwright/cli@$PlaywrightVersion", "playwright-cli")
    if (-not $WithoutSession) {
        $commandArgs += "-s=$script:sessionName"
    }
    $commandArgs += $Arguments
    Push-Location $script:runDir
    try {
        $lines = @(& npx @commandArgs 2>&1 | ForEach-Object { [string]$_ })
        $exitCode = $LASTEXITCODE
    }
    finally {
        Pop-Location
    }
    $script:playwrightTranscript.Add("playwright-cli " + [string]::Join(" ", $Arguments))
    foreach ($line in $lines) {
        $script:playwrightTranscript.Add($line)
    }
    if ($exitCode -ne 0) {
        throw "playwright-cli $($Arguments[0]) failed with exit code $exitCode."
    }
    return [string]::Join("`n", [string[]]$lines)
}

function Convert-RawJson {
    param(
        [Parameter(Mandatory = $true)][string]$Raw,
        [Parameter(Mandatory = $true)][string]$Step
    )
    try {
        return $Raw | ConvertFrom-Json -ErrorAction Stop
    }
    catch {
        throw "$Step returned invalid JSON: $Raw"
    }
}

$repoRoot = [System.IO.Path]::GetFullPath((Join-Path $PSScriptRoot ".."))
$resolvedBinary = Resolve-RepoPath -Root $repoRoot -Path $BinaryPath
$outputRoot = Resolve-RepoPath -Root $repoRoot -Path $OutputDir
$runID = [DateTime]::UtcNow.ToString("yyyyMMdd-HHmmss") + "-" + [Guid]::NewGuid().ToString("N").Substring(0, 8)
$runDir = Join-Path $outputRoot $runID
$reportPath = Join-Path $outputRoot "report.json"
$sessionName = "runtime-e2e-" + [Guid]::NewGuid().ToString("N").Substring(0, 10)
$requiredCheckNames = @(
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
$checks = New-Object System.Collections.Generic.List[object]
$playwrightTranscript = New-Object System.Collections.Generic.List[string]
$process = $null
$traceStarted = $false
$browserOpened = $false
$failure = ""
$health = $null
$pageState = $null
$reloadState = $null
$resourceProbes = $null
$expectedGitDirtyValue = $null
$startedAt = [DateTime]::UtcNow

New-Item -ItemType Directory -Path $runDir -Force | Out-Null
$runtimeHome = Join-Path $runDir "runtime-home"
New-Item -ItemType Directory -Path $runtimeHome -Force | Out-Null
$runtimeSkillDir = Join-Path $runtimeHome "skills"
New-Item -ItemType Directory -Path $runtimeSkillDir -Force | Out-Null
$runtimeSessionDir = Join-Path $runtimeHome ".aicli/sessions"
New-Item -ItemType Directory -Path $runtimeSessionDir -Force | Out-Null
$systemTempRoot = [System.IO.Path]::GetFullPath([System.IO.Path]::GetTempPath())
$runtimeIsolationDir = Join-Path $systemTempRoot ("aicli-runtime-e2e-" + $runID)
New-Item -ItemType Directory -Path $runtimeIsolationDir -Force | Out-Null
$runtimeConfigPath = Join-Path $runtimeIsolationDir "runtime-config.json"
$runtimeConfigEvidencePath = Join-Path $runDir "runtime-config.json"
$agentConfigPath = Join-Path $runDir "aicli-config.json"
$runtimeLogPath = Join-Path $runDir "runtime-server.log"
$runtimeConfigDocument = [ordered]@{
    version = "v1"
    sessions = [ordered]@{ dir = $runtimeSessionDir }
}
Write-JsonFile -Path $runtimeConfigPath -Value $runtimeConfigDocument
Write-JsonFile -Path $runtimeConfigEvidencePath -Value $runtimeConfigDocument
Write-JsonFile -Path $agentConfigPath -Value ([ordered]@{
    Log = [ordered]@{ Enabled = $true; FilePath = $runtimeLogPath }
    SkillsRuntime = [ordered]@{
        Enabled = $false
        ConfigFile = $runtimeConfigPath
        SkillDir = $runtimeSkillDir
        SkillDirs = @()
        ExtraSkillDirs = @()
    }
})
$stdoutPath = Join-Path $runDir "server-stdout.log"
$stderrPath = Join-Path $runDir "server-stderr.log"
$pidPath = Join-Path $runDir "runtime-server.pid"
$consolePath = Join-Path $runDir "console.txt"
$networkPath = Join-Path $runDir "network.txt"
$snapshotTextPath = Join-Path $runDir "snapshot.txt"
$screenshotPath = Join-Path $runDir "page.png"

$pageStateScript = @'
async () => {
  const sleep = ms => new Promise(resolve => setTimeout(resolve, ms));
  let root = null;
  let text = '';
  for (let attempt = 0; attempt < 120; attempt++) {
    root = document.querySelector('#root');
    text = (root?.innerText || '').trim();
    if (root && root.childElementCount > 0 && text.length > 20 && !text.includes('正在加载页面')) break;
    await sleep(250);
  }
  root = document.querySelector('#root');
  text = (root?.innerText || '').trim();
  const heading = root?.querySelector('h1') || null;
  const composer = root?.querySelector('textarea.app-chat-input') || null;
  const composerRoot = composer?.parentElement?.parentElement || null;
  const submitButton = composerRoot?.querySelector('button[aria-label][title]') || null;
  let highlightTokenCount = 0;
  let highlightKeywordCount = 0;
  let highlightNumberCount = 0;
  let highlightError = '';
  try {
    const prism = globalThis.Prism;
    const probe = document.createElement('pre');
    const code = document.createElement('code');
    probe.hidden = true;
    code.className = 'language-javascript';
    code.innerHTML = prism.highlight('const answer = 42;', prism.languages.javascript, 'javascript');
    probe.appendChild(code);
    document.body.appendChild(probe);
    highlightTokenCount = code.querySelectorAll('span.token').length;
    highlightKeywordCount = code.querySelectorAll('span.token.keyword').length;
    highlightNumberCount = code.querySelectorAll('span.token.number').length;
    probe.remove();
  } catch (error) {
    highlightError = String(error);
  }
  return {
    url: location.href,
    path: location.pathname,
    title: document.title,
    root_child_count: root?.childElementCount || 0,
    visible_text_length: text.length,
    loading_placeholder_visible: text.includes('正在加载页面'),
    interactive_count: document.querySelectorAll('button, a, input, textarea, select').length,
    prism_available: !!globalThis.Prism,
    prism_javascript_available: !!globalThis.Prism?.languages?.javascript,
    workspace_heading_count: heading ? 1 : 0,
    workspace_heading_length: (heading?.textContent || '').trim().length,
    composer_count: composer ? 1 : 0,
    composer_submit_count: submitButton ? 1 : 0,
    logs_link_count: root?.querySelectorAll('a[href="/logs"]').length || 0,
    runtime_link_count: root?.querySelectorAll('a[href="/runtime/config"]').length || 0,
    highlight_token_count: highlightTokenCount,
    highlight_keyword_count: highlightKeywordCount,
    highlight_number_count: highlightNumberCount,
    highlight_error: highlightError
  };
}
'@

$resourceProbeScript = @'
async () => {
  const entries = performance.getEntriesByType('resource').map(entry => entry.name);
  const sameOrigin = [...new Set(entries)].filter(value => {
    try { return new URL(value, location.href).origin === location.origin; } catch { return false; }
  });
  const apiUrls = sameOrigin.filter(value => new URL(value).pathname.startsWith('/api/'));
  const assetUrls = sameOrigin.filter(value => {
    const path = new URL(value).pathname;
    return !path.startsWith('/api/') && path !== '/healthz';
  });
  const probe = async url => {
    try {
      const response = await fetch(url, { cache: 'no-store', credentials: 'same-origin' });
      return { url, status: response.status, ok: response.ok };
    } catch (error) {
      return { url, status: 0, ok: false, error: String(error) };
    }
  };
  const assets = await Promise.all(assetUrls.map(probe));
  const apis = await Promise.all(apiUrls.map(probe));
  return { assets, apis };
}
'@

$oldNativePreference = $null
$hasNativePreference = Test-Path variable:PSNativeCommandUseErrorActionPreference
if ($hasNativePreference) {
    $oldNativePreference = $PSNativeCommandUseErrorActionPreference
    $PSNativeCommandUseErrorActionPreference = $false
}

try {
    if (-not (Test-Path -LiteralPath $resolvedBinary -PathType Leaf)) {
        throw "runtime-server binary was not found: $resolvedBinary"
    }
    if (-not (Get-Command npx -ErrorAction SilentlyContinue)) {
        throw "Required command 'npx' was not found in PATH."
    }
    if (-not [string]::IsNullOrWhiteSpace($ExpectedGitDirty)) {
        $parsedGitDirty = $false
        if (-not [bool]::TryParse($ExpectedGitDirty, [ref]$parsedGitDirty)) {
            throw "ExpectedGitDirty must be true, false, or empty."
        }
        $expectedGitDirtyValue = $parsedGitDirty
    }

    $buildInfo = $null
    $resolvedBuildInfo = Resolve-RepoPath -Root $repoRoot -Path $FrontendBuildInfoPath
    if (Test-Path -LiteralPath $resolvedBuildInfo -PathType Leaf) {
        $buildInfo = Get-Content -LiteralPath $resolvedBuildInfo -Raw | ConvertFrom-Json
        if ([string]::IsNullOrWhiteSpace($ExpectedFrontendManifestHash)) {
            $ExpectedFrontendManifestHash = [string]$buildInfo.asset_manifest_hash
        }
    }

    if (-not $SkipBrowserInstall) {
        [void](Invoke-PlaywrightCli -WithoutSession -Arguments @("install-browser", "chrome-for-testing", "--with-deps"))
    }

    $probe = New-Object System.Net.Sockets.TcpListener([System.Net.IPAddress]::Loopback, 0)
    try {
        $probe.Start()
        $port = $probe.LocalEndpoint.Port
    }
    finally {
        $probe.Stop()
    }
    $listenAddress = "127.0.0.1:$port"
    $baseUrl = "http://127.0.0.1:$port"
    $routeUrl = "$baseUrl/workspace/chats/new"

    $startParameters = @{
        FilePath = $resolvedBinary
        ArgumentList = @(
            "serve", "--config", $agentConfigPath, "--listen", $listenAddress,
            "--pid-file", "runtime-server.pid"
        )
        WorkingDirectory = $runDir
        Environment = @{
            HOME = $runtimeHome
            USERPROFILE = $runtimeHome
            XDG_CONFIG_HOME = Join-Path $runtimeHome ".config"
            XDG_DATA_HOME = Join-Path $runtimeHome ".local/share"
            AICLI_SESSION_USER = "release-gate-e2e"
            AICLI_LOG_FILE_PATH = $runtimeLogPath
            SKILLS_RUNTIME_ENABLED = "false"
            SKILLS_RUNTIME_SKILL_DIR = $runtimeSkillDir
        }
        RedirectStandardOutput = $stdoutPath
        RedirectStandardError = $stderrPath
        PassThru = $true
    }
    if ([System.Environment]::OSVersion.Platform -eq [System.PlatformID]::Win32NT) {
        $startParameters.WindowStyle = "Hidden"
    }
    $process = Start-Process @startParameters

    $deadline = [DateTime]::UtcNow.AddSeconds($StartupTimeoutSeconds)
    while ([DateTime]::UtcNow -lt $deadline) {
        if ($process.HasExited) {
            $stderr = if (Test-Path -LiteralPath $stderrPath) { Get-Content -LiteralPath $stderrPath -Raw } else { "" }
            throw "runtime-server exited before health check (code $($process.ExitCode)): $stderr"
        }
        try {
            $health = Invoke-RestMethod -Uri "$baseUrl/healthz" -Method Get -TimeoutSec 2
            break
        }
        catch {
            Start-Sleep -Milliseconds 250
        }
    }
    Add-Check -Name "server.health" -Passed ($null -ne $health) -Detail "health endpoint became ready"
    Write-JsonFile -Path (Join-Path $runDir "health.json") -Value $health

    $provenanceErrors = New-Object System.Collections.Generic.List[string]
    if (-not [string]::IsNullOrWhiteSpace($ExpectedVersion) -and [string]$health.backend.version -ne $ExpectedVersion) {
        $provenanceErrors.Add("backend.version expected '$ExpectedVersion', got '$($health.backend.version)'")
    }
    if (-not [string]::IsNullOrWhiteSpace($ExpectedGitCommit) -and [string]$health.backend.git_commit -ne $ExpectedGitCommit) {
        $provenanceErrors.Add("backend.git_commit mismatch")
    }
    if ($null -ne $expectedGitDirtyValue -and [bool]$health.backend.git_dirty -ne $expectedGitDirtyValue) {
        $provenanceErrors.Add("backend.git_dirty expected '$expectedGitDirtyValue', got '$($health.backend.git_dirty)'")
    }
    if (-not [string]::IsNullOrWhiteSpace($ExpectedFrontendManifestHash) -and
        [string]$health.frontend.asset_manifest_hash -ne $ExpectedFrontendManifestHash) {
        $provenanceErrors.Add("frontend.asset_manifest_hash mismatch")
    }
    if ([string]$health.execution_core.name -ne "session_actor") {
        $provenanceErrors.Add("execution_core.name expected 'session_actor', got '$($health.execution_core.name)'")
    }
    if ([int]$health.execution_core.contract_version -ne 1) {
        $provenanceErrors.Add("execution_core.contract_version expected '1', got '$($health.execution_core.contract_version)'")
    }
    if ([string]$health.execution_core.lifecycle -ne "durable_session_actor" -or
        [string]$health.execution_core.state_authority -ne "runtime_state_store" -or
        [string]$health.execution_core.event_protocol -ne "session_runtime_events" -or
        [string]$health.execution_core.approval_protocol -ne "runtime_command_relay" -or
        -not [bool]$health.execution_core.background_durable) {
        $provenanceErrors.Add("health execution_core does not satisfy the unified SessionActor contract")
    }
    foreach ($requiredValue in @(
        [string]$health.backend.version,
        [string]$health.backend.git_commit,
        [string]$health.backend.build_time,
        [string]$health.frontend.asset_manifest_hash,
        [string]$health.frontend.build_time,
        [string]$health.frontend.entry_asset
    )) {
        if ([string]::IsNullOrWhiteSpace($requiredValue)) {
            $provenanceErrors.Add("health provenance contains an empty required value")
            break
        }
    }
    if ($null -ne $buildInfo) {
        if ([string]$health.frontend.asset_manifest_hash -ne [string]$buildInfo.asset_manifest_hash) {
            $provenanceErrors.Add("health frontend hash differs from staged build-info.json")
        }
        if ([string]$health.frontend.entry_asset -ne [string]$buildInfo.entry_asset) {
            $provenanceErrors.Add("health frontend entry differs from staged build-info.json")
        }
        $healthBuildTime = ([DateTimeOffset]$health.frontend.build_time).ToUniversalTime().ToString("o")
        $stagedBuildTime = ([DateTimeOffset]$buildInfo.build_time).ToUniversalTime().ToString("o")
        if ($healthBuildTime -ne $stagedBuildTime) {
            $provenanceErrors.Add("health frontend build time differs from staged build-info.json")
        }
    }
    $provenanceDetail = if ($provenanceErrors.Count -eq 0) {
        "health provenance and SessionActor runtime contract match the packaged server"
    } else {
        [string]::Join("; ", $provenanceErrors.ToArray())
    }
    Add-Check -Name "build.provenance" -Passed ($provenanceErrors.Count -eq 0) -Detail $provenanceDetail

    [void](Invoke-PlaywrightCli -Arguments @("open", $routeUrl))
    $browserOpened = $true
    [void](Invoke-PlaywrightCli -Arguments @("tracing-start"))
    $traceStarted = $true
    [void](Invoke-PlaywrightCli -Arguments @("reload"))

    $snapshotText = Invoke-PlaywrightCli -Arguments @("snapshot")
    [System.IO.File]::WriteAllText($snapshotTextPath, $snapshotText + "`n")
    $pageStateEval = $pageStateScript -replace "`r?`n", " "
    $stateRaw = Invoke-PlaywrightCli -Arguments @("--raw", "eval", $pageStateEval)
    $pageState = Convert-RawJson -Raw $stateRaw -Step "page state"
    Write-JsonFile -Path (Join-Path $runDir "browser-state.json") -Value $pageState

    $pageVisible = [string]$pageState.path -eq "/workspace/chats/new" -and
        [string]$pageState.title -eq "AI Agent Runtime Console" -and
        [int]$pageState.root_child_count -gt 0 -and
        [int]$pageState.visible_text_length -gt 20 -and
        [int]$pageState.interactive_count -gt 0 -and
        [int]$pageState.workspace_heading_count -eq 1 -and
        [int]$pageState.workspace_heading_length -gt 20 -and
        [int]$pageState.composer_count -eq 1 -and
        [int]$pageState.composer_submit_count -eq 1 -and
        [int]$pageState.logs_link_count -eq 1 -and
        [int]$pageState.runtime_link_count -ge 1 -and
        -not [bool]$pageState.loading_placeholder_visible
    Add-Check -Name "page.visible" -Passed $pageVisible `
        -Detail "path=$($pageState.path), heading=$($pageState.workspace_heading_count), composer=$($pageState.composer_count), text=$($pageState.visible_text_length)"
    $codeHighlightingPassed = [bool]$pageState.prism_available -and
        [bool]$pageState.prism_javascript_available -and
        [int]$pageState.highlight_token_count -ge 3 -and
        [int]$pageState.highlight_keyword_count -ge 1 -and
        [int]$pageState.highlight_number_count -ge 1 -and
        [string]::IsNullOrWhiteSpace([string]$pageState.highlight_error)
    Add-Check -Name "code.highlighting" -Passed $codeHighlightingPassed `
        -Detail "tokens=$($pageState.highlight_token_count), keywords=$($pageState.highlight_keyword_count), numbers=$($pageState.highlight_number_count)"
    Add-Check -Name "route.reload" -Passed ([string]$pageState.path -eq "/workspace/chats/new") `
        -Detail "SPA route remained stable after a browser reload"

    $consoleText = Invoke-PlaywrightCli -Arguments @("--raw", "console", "error")
    [System.IO.File]::WriteAllText($consolePath, $consoleText + "`n")
    $consoleClean = $consoleText -match 'Errors:\s*0' -and $consoleText -notmatch 'ReferenceError'
    Add-Check -Name "console.errors" -Passed $consoleClean -Detail $consoleText.Trim()

    $networkText = Invoke-PlaywrightCli -Arguments @("--raw", "requests", "--static")
    [System.IO.File]::WriteAllText($networkPath, $networkText + "`n")
    $networkFailures = @([regex]::Matches($networkText, '=> \[(?<status>\d{3})\]') |
        Where-Object { [int]$_.Groups['status'].Value -ge 400 })
    $networkClean = $networkText -notmatch '(?i)failed|net::ERR_' -and $networkFailures.Count -eq 0
    Add-Check -Name "network.requests" -Passed $networkClean `
        -Detail "captured $([regex]::Matches($networkText, '=> \[\d{3}\]').Count) requests"

    $resourceProbeEval = $resourceProbeScript -replace "`r?`n", " "
    $resourceRaw = Invoke-PlaywrightCli -Arguments @("--raw", "eval", $resourceProbeEval)
    $resourceProbes = Convert-RawJson -Raw $resourceRaw -Step "resource probes"
    Write-JsonFile -Path (Join-Path $runDir "resource-probes.json") -Value $resourceProbes
    $assetResults = @($resourceProbes.assets)
    $apiResults = @($resourceProbes.apis)
    $failedAssets = @($assetResults | Where-Object { -not [bool]$_.ok -or [int]$_.status -ne 200 })
    $failedApis = @($apiResults | Where-Object { -not [bool]$_.ok -or [int]$_.status -ne 200 })
    Add-Check -Name "assets.status" -Passed ($assetResults.Count -gt 0 -and $failedAssets.Count -eq 0) `
        -Detail "verified $($assetResults.Count) same-origin assets and dynamic chunks"
    Add-Check -Name "api.status" -Passed ($apiResults.Count -gt 0 -and $failedApis.Count -eq 0) `
        -Detail "verified $($apiResults.Count) page API requests"

    [void](Invoke-PlaywrightCli -Arguments @("screenshot", "--filename", "page.png", "--full-page"))
    Add-Check -Name "artifacts.screenshot" -Passed (Test-Path -LiteralPath $screenshotPath -PathType Leaf) `
        -Detail "full-page screenshot was captured"

    [void](Invoke-PlaywrightCli -Arguments @("tracing-stop"))
    $traceStarted = $false
    $traceSource = Join-Path $runDir ".playwright-cli/traces"
    $traceArchive = Join-Path $runDir "trace.zip"
    if (Test-Path -LiteralPath $traceSource -PathType Container) {
        Compress-Archive -Path (Join-Path $traceSource "*") -DestinationPath $traceArchive -CompressionLevel Optimal -Force
    }
    Add-Check -Name "artifacts.trace" -Passed (Test-Path -LiteralPath $traceArchive -PathType Leaf) `
        -Detail "Playwright action, network, and resource trace was archived"

    $snapshot = Get-ChildItem -LiteralPath (Join-Path $runDir ".playwright-cli") -Filter "page-*.yml" -File -ErrorAction SilentlyContinue |
        Sort-Object LastWriteTimeUtc -Descending | Select-Object -First 1
    if ($null -ne $snapshot) {
        Copy-Item -LiteralPath $snapshot.FullName -Destination (Join-Path $runDir "snapshot.yml") -Force
    }
    Add-Check -Name "artifacts.snapshot" -Passed ($null -ne $snapshot) `
        -Detail "fresh accessibility snapshot was retained"
}
catch {
    $failure = $_.Exception.Message
}
finally {
    if ($traceStarted -and $browserOpened) {
        try { [void](Invoke-PlaywrightCli -Arguments @("tracing-stop")) } catch { }
    }
    if ($browserOpened) {
        try { [void](Invoke-PlaywrightCli -Arguments @("close")) } catch { }
    }
    if ($null -ne $process -and -not $process.HasExited) {
        Stop-Process -Id $process.Id -Force -ErrorAction SilentlyContinue
        $null = $process.WaitForExit(5000)
    }
    $resolvedIsolationDir = [System.IO.Path]::GetFullPath($runtimeIsolationDir)
    $tempPrefix = $systemTempRoot.TrimEnd([char[]]@(
        [System.IO.Path]::DirectorySeparatorChar,
        [System.IO.Path]::AltDirectorySeparatorChar
    )) + [System.IO.Path]::DirectorySeparatorChar
    if ($resolvedIsolationDir.StartsWith($tempPrefix, [System.StringComparison]::OrdinalIgnoreCase) -and
        (Test-Path -LiteralPath $resolvedIsolationDir -PathType Container)) {
        Remove-Item -LiteralPath $resolvedIsolationDir -Recurse -Force
    }
    if ($hasNativePreference) {
        $PSNativeCommandUseErrorActionPreference = $oldNativePreference
    }
    $utf8 = New-Object System.Text.UTF8Encoding($false)
    [System.IO.File]::WriteAllLines(
        (Join-Path $runDir "playwright.log"),
        $playwrightTranscript.ToArray(),
        $utf8
    )
}

$finishedAt = [DateTime]::UtcNow
$expectedCheckCount = $requiredCheckNames.Count
$passedChecks = @($checks | Where-Object { $_.passed }).Count
$checkContractErrors = New-Object System.Collections.Generic.List[string]
foreach ($requiredCheckName in $requiredCheckNames) {
    $matchingChecks = @($checks | Where-Object { [string]$_.name -ceq $requiredCheckName })
    if ($matchingChecks.Count -ne 1) {
        $checkContractErrors.Add(
            "required check '$requiredCheckName' occurred $($matchingChecks.Count) times"
        )
    }
    elseif (-not [bool]$matchingChecks[0].passed) {
        $checkContractErrors.Add("required check '$requiredCheckName' did not pass")
    }
}
$unexpectedChecks = @($checks | Where-Object {
    $actualName = [string]$_.name
    @($requiredCheckNames | Where-Object { $_ -ceq $actualName }).Count -eq 0
})
foreach ($unexpectedCheck in $unexpectedChecks) {
    $checkContractErrors.Add("unexpected check '$([string]$unexpectedCheck.name)'")
}
if ($checkContractErrors.Count -gt 0) {
    $contractFailure = "Browser check contract failed: $([string]::Join('; ', $checkContractErrors.ToArray()))"
    $failure = if ([string]::IsNullOrWhiteSpace($failure)) {
        $contractFailure
    }
    else {
        "$failure | $contractFailure"
    }
}
$allPassed = [string]::IsNullOrWhiteSpace($failure) -and
    $checkContractErrors.Count -eq 0 -and $checks.Count -eq $expectedCheckCount -and
    $passedChecks -eq $expectedCheckCount
$binaryHash = ""
$binarySize = 0
if (Test-Path -LiteralPath $resolvedBinary -PathType Leaf) {
    $binaryHash = (Get-FileHash -LiteralPath $resolvedBinary -Algorithm SHA256).Hash.ToLowerInvariant()
    $binarySize = (Get-Item -LiteralPath $resolvedBinary).Length
}
$report = [ordered]@{
    schema_version = 1
    generated_at = $finishedAt.ToString("o")
    passed = $allPassed
    failure = $failure
    duration_ms = [int64]($finishedAt - $startedAt).TotalMilliseconds
    playwright_cli_version = $PlaywrightVersion
    session = $sessionName
    base_url = if ($null -ne $health) { $baseUrl } else { "" }
    binary = [ordered]@{
        path = $resolvedBinary
        size = $binarySize
        sha256 = $binaryHash
    }
    expected = [ordered]@{
        version = $ExpectedVersion
        git_commit = $ExpectedGitCommit
        git_dirty = $expectedGitDirtyValue
        frontend_manifest_hash = $ExpectedFrontendManifestHash
    }
    health = $health
    browser_state = $pageState
    resource_probes = $resourceProbes
    summary = [ordered]@{
        expected_checks = $expectedCheckCount
        executed_checks = $checks.Count
        passed_checks = $passedChecks
        failed_checks = $checks.Count - $passedChecks
    }
    checks = $checks.ToArray()
    artifacts = [ordered]@{
        run_directory = $runDir
        screenshot = Join-Path $runDir "page.png"
        snapshot = Join-Path $runDir "snapshot.yml"
        console = $consolePath
        network = $networkPath
        trace = Join-Path $runDir "trace.zip"
        playwright_log = Join-Path $runDir "playwright.log"
        server_stdout = $stdoutPath
        server_stderr = $stderrPath
        runtime_home = $runtimeHome
        agent_config = $agentConfigPath
        runtime_config = $runtimeConfigEvidencePath
    }
}
Write-JsonFile -Path (Join-Path $runDir "report.json") -Value $report
Write-JsonFile -Path $reportPath -Value $report

Write-Host ""
Write-Host "Runtime browser E2E report: $reportPath"
Write-Host "Artifacts: $runDir"
Write-Host "Checks passed: $passedChecks / $expectedCheckCount"
if (-not $allPassed) {
    Write-Error $(if ([string]::IsNullOrWhiteSpace($failure)) {
        "Browser E2E did not execute every required check."
    } else {
        $failure
    })
    exit 1
}
