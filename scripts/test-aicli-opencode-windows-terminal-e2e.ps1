[CmdletBinding()]
param(
    [ValidateRange(30, 900)]
    [int]$TimeoutSeconds = 300,

    [ValidateRange(10, 180)]
    [int]$StartupTimeoutSeconds = 60,

    [switch]$KeepWindow
)

$ErrorActionPreference = "Stop"
Set-StrictMode -Version 3.0

function Get-OpenCodeApiKey {
    $environmentKey = [string][Environment]::GetEnvironmentVariable("OPENCODE_API_KEY", "Process")
    if (-not [string]::IsNullOrWhiteSpace($environmentKey)) {
        return [pscustomobject]@{ Key = $environmentKey.Trim(); Source = "OPENCODE_API_KEY" }
    }

    $authPath = Join-Path $HOME ".local\share\opencode\auth.json"
    if (-not (Test-Path -LiteralPath $authPath -PathType Leaf)) {
        throw "OpenCode credential unavailable: set OPENCODE_API_KEY or configure opencode auth.json."
    }

    try {
        $auth = Get-Content -LiteralPath $authPath -Raw -Encoding utf8 | ConvertFrom-Json
    } catch {
        throw "OpenCode credential unavailable: auth.json could not be parsed."
    }

    foreach ($entryName in @("opencode-go", "deepseek")) {
        $entry = $auth.PSObject.Properties[$entryName]
        if ($null -eq $entry -or $null -eq $entry.Value) { continue }
        $keyProperty = $entry.Value.PSObject.Properties["key"]
        if ($null -eq $keyProperty) { continue }
        $key = [string]$keyProperty.Value
        if (-not [string]::IsNullOrWhiteSpace($key)) {
            return [pscustomobject]@{ Key = $key.Trim(); Source = "auth.json:$entryName" }
        }
    }

    throw "OpenCode credential unavailable: auth.json has no opencode-go or deepseek key."
}

function Protect-SensitiveText {
    param([AllowNull()][string]$Text, [AllowNull()][string]$Secret)
    if ($null -eq $Text) { return "" }
    if (-not [string]::IsNullOrEmpty($Secret)) {
        return $Text.Replace($Secret, "[REDACTED]")
    }
    return $Text
}

function Write-Utf8NoBom {
    param([Parameter(Mandatory)][string]$Path, [AllowNull()][string]$Value)
    [IO.File]::WriteAllText($Path, [string]$Value, [Text.UTF8Encoding]::new($false))
}

function Remove-SecretArtifact {
    param([AllowNull()][string]$Path)
    if (-not [string]::IsNullOrWhiteSpace($Path) -and (Test-Path -LiteralPath $Path)) {
        Remove-Item -LiteralPath $Path -Force -ErrorAction SilentlyContinue
    }
}

function Scrub-TextArtifacts {
    param([string]$Root, [string]$Secret)
    if ([string]::IsNullOrEmpty($Secret) -or -not (Test-Path -LiteralPath $Root)) { return }
    $extensions = @(".txt", ".log", ".json", ".jsonl", ".yaml", ".yml", ".ps1", ".cmd")
    foreach ($file in Get-ChildItem -LiteralPath $Root -Recurse -File -ErrorAction SilentlyContinue) {
        if ($extensions -notcontains $file.Extension.ToLowerInvariant()) { continue }
        try {
            $content = [IO.File]::ReadAllText($file.FullName)
            if ($content.Contains($Secret)) {
                Write-Utf8NoBom -Path $file.FullName -Value $content.Replace($Secret, "[REDACTED]")
            }
        } catch {
            # Best-effort hygiene only; the test result is reported separately.
        }
    }
}

if ($env:OS -ne "Windows_NT") {
    throw "This E2E requires an interactive Windows desktop."
}

$repoRoot = [IO.Path]::GetFullPath((Join-Path $PSScriptRoot ".."))
$backendDir = Join-Path $repoRoot "backend"
$outputDir = Join-Path $repoRoot "output\aicli-terminal-e2e"
$executable = Join-Path $outputDir "aicli-live-e2e.exe"
$consoleInputWriterPath = Join-Path $PSScriptRoot "write-console-input.ps1"
$runID = [Guid]::NewGuid().ToString("N")
$runRoot = Join-Path $outputDir ("opencode-wt-" + $runID)
$configPath = Join-Path $runRoot "config.yaml"
$runnerPath = Join-Path $runRoot "run-chat.ps1"
$secretPath = Join-Path $runRoot "credential.bridge"
$sessionDir = Join-Path $runRoot "sessions"
$chatLogDir = Join-Path $runRoot "chat-logs"
$processLog = Join-Path $runRoot "aicli.log"
$exitCodePath = Join-Path $runRoot "runner-exit-code.txt"
$exitInputPath = Join-Path $runRoot "exit-command.txt"
$fullDump = Join-Path $runRoot "uia-document-full.txt"
$visibleDump = Join-Path $runRoot "uia-visible-ranges.txt"
$manifestPath = Join-Path $runRoot "manifest.json"
$runPrefix = "WTLIVE" + $runID.Substring(0, 10).ToUpperInvariant()
$startupSentinel = "AICLI-WT-LIVE-READY-" + $runID
$runnerExitSentinel = "AICLI-WT-LIVE-EXIT-" + $runID + "-"
$windowTitle = "aicli-opencode-e2e-" + $runID.Substring(0, 12)
$reasoningSentinel = "REASON" + $runID.Substring(10, 10).ToUpperInvariant()
$expectedProvider = "opencode.ai"
$expectedModel = "deepseek-v4-flash"
$expectedReasoningEffort = "max"
$expectedCompatibilityProfile = "opencode-console-go-2026-07"
$reasoningSentinelSplit = [int][Math]::Floor($reasoningSentinel.Length / 2)
$reasoningSentinelFirst = $reasoningSentinel.Substring(0, $reasoningSentinelSplit)
$reasoningSentinelSecond = $reasoningSentinel.Substring($reasoningSentinelSplit)
$testPrompt = "Briefly explain how terminal scrollback preserves completed output. After that explanation, add one control line by concatenating the fragments $reasoningSentinelFirst and $reasoningSentinelSecond with no separator. Then emit exactly forty validation rows and stop. Construct each row from the run prefix $runPrefix, one hyphen, its two-digit sequence number from 01 through 40, one ASCII space, and the fixed words terminal history validation. Do not quote, rehearse, or discuss the control fragments, the row construction, or any completed validation row in prose. The concatenated control value must occur once and must not appear in a validation row. Use no brackets, code formatting, markdown, bullets, headings, commentary, or blank lines among the forty validation rows."
if ($testPrompt.Contains($reasoningSentinel) -or $testPrompt -match ([regex]::Escape($runPrefix) + '-\d{2}')) {
    throw 'E2E prompt must not contain a complete reasoning sentinel or validation marker example.'
}

New-Item -ItemType Directory -Path $runRoot, $sessionDir, $chatLogDir -Force | Out-Null
if (-not (Test-Path -LiteralPath $consoleInputWriterPath -PathType Leaf)) {
    throw "Console input writer helper is missing: $consoleInputWriterPath"
}
Write-Utf8NoBom -Path $fullDump -Value ""
Write-Utf8NoBom -Path $visibleDump -Value ""

$credential = Get-OpenCodeApiKey
$apiKey = [string]$credential.Key
$credentialSource = [string]$credential.Source
Write-Utf8NoBom -Path $secretPath -Value $apiKey

$configYaml = @'
aicli:
  chat:
    stream: true
    terminal_title:
      enabled: false
  mcp:
    auto_connect: false
providers:
  default_provider: opencode.ai
  max_retries: 0
  headers: {}
  items:
    opencode.ai:
      enabled: true
      protocol: openai
      base_url: https://opencode.ai/zen/go
      api_path: /v1/chat/completions
      api_key: ${AICLI_TERMINAL_E2E_API_KEY}
      compatibility:
        profile: opencode-console-go-2026-07
      default_model: deepseek-v4-flash
      supported_models:
        - deepseek-v4-flash
      model_capabilities:
        deepseek-v4-flash:
          reasoning_model: true
          reasoning_efforts:
            - high
            - max
          max_tokens: 4096
      max_tokens_limit: 4096
      timeout: 300s
'@
Write-Utf8NoBom -Path $configPath -Value ($configYaml.Trim() + [Environment]::NewLine)

function ConvertTo-PowerShellLiteral {
    param([Parameter(Mandatory)][string]$Value)
    return "'" + $Value.Replace("'", "''") + "'"
}

$secretLiteral = ConvertTo-PowerShellLiteral $secretPath
$exeLiteral = ConvertTo-PowerShellLiteral $executable
$configLiteral = ConvertTo-PowerShellLiteral $configPath
$processLogLiteral = ConvertTo-PowerShellLiteral $processLog
$chatLogLiteral = ConvertTo-PowerShellLiteral $chatLogDir
$sessionLiteral = ConvertTo-PowerShellLiteral $sessionDir
$exitCodeLiteral = ConvertTo-PowerShellLiteral $exitCodePath
$startupLiteral = ConvertTo-PowerShellLiteral $startupSentinel
$runnerExitLiteral = ConvertTo-PowerShellLiteral $runnerExitSentinel
$runIDLiteral = ConvertTo-PowerShellLiteral $runID
$requestTimeoutLiteral = ConvertTo-PowerShellLiteral ($TimeoutSeconds.ToString() + "s")
$promptLiteral = ConvertTo-PowerShellLiteral $testPrompt

$runner = @"
`$ErrorActionPreference = 'Stop'
[Console]::OutputEncoding = [Text.UTF8Encoding]::new(`$false)
try { [Console]::InputEncoding = [Text.UTF8Encoding]::new(`$false) } catch {}
`$exitCode = 1
`$secret = ''
try {
    `$secret = [IO.File]::ReadAllText($secretLiteral).Trim()
    Remove-Item -LiteralPath $secretLiteral -Force -ErrorAction SilentlyContinue
    if ([string]::IsNullOrWhiteSpace(`$secret)) { throw 'credential bridge was empty' }
    `$env:AICLI_TERMINAL_E2E_API_KEY = `$secret
    Write-Output $startupLiteral
    `$chatArguments = @(
        '--config', $configLiteral,
        '--logfile', $processLogLiteral,
        'chat',
        '--provider', 'opencode.ai',
        '--model', 'deepseek-v4-flash',
        '--reasoning-effort', 'max',
        '--stream',
        '--yolo',
        '--disable-tools',
        '--runtime-mode', 'local',
        '--request-timeout', $requestTimeoutLiteral,
        '--session-dir', $sessionLiteral,
        '--log-dir', $chatLogLiteral,
        '--title', $runIDLiteral,
        '--prompt', $promptLiteral
    )
    & $exeLiteral @chatArguments
    `$exitCode = `$LASTEXITCODE
} catch {
    Write-Error 'aicli terminal E2E runner failed.'
} finally {
    `$env:AICLI_TERMINAL_E2E_API_KEY = `$null
    `$secret = `$null
    [IO.File]::WriteAllText($exitCodeLiteral, [string]`$exitCode, [Text.UTF8Encoding]::new(`$false))
    Write-Output ($runnerExitLiteral + [string]`$exitCode)
}
exit `$exitCode
"@
Write-Utf8NoBom -Path $runnerPath -Value $runner

function Get-ReadyPromptEvidence {
    param(
        [AllowNull()][string]$VisibleText,
        [AllowNull()][string]$ExpectedProvider,
        [AllowNull()][string]$ExpectedModel,
        [AllowNull()][string]$ExpectedReasoningEffort
    )
    $result = [ordered]@{
        Ready = $false
        IdentityValid = $false
        PromptLineNumber = -1
        FooterLineNumber = -1
        FooterLine = ""
        VisibleModelText = ""
        ProviderVisible = $false
        ModelVisible = $false
        ReasoningEffortVisible = $false
    }
    if ([string]::IsNullOrWhiteSpace($VisibleText)) { return [pscustomobject]$result }
    $lines = [regex]::Split($VisibleText, "\r?\n")
    $promptLineIndex = -1
    for ($index = 0; $index -lt $lines.Count; $index++) {
        if ($lines[$index] -match '^\s*>\s*$') { $promptLineIndex = $index }
    }
    if ($promptLineIndex -lt 0) { return [pscustomobject]$result }
    $result.PromptLineNumber = $promptLineIndex + 1
    for ($index = $promptLineIndex + 1; $index -lt $lines.Count; $index++) {
        $line = $lines[$index]
        if ($line -match '^\s*Plan\s+(?:ON|OFF)\s*[·|]' -and $line -notmatch '(?i)\b(?:Working|Streaming|Thinking|Waiting|Stopping)\b') {
            $result.Ready = $true
            $result.FooterLineNumber = $index + 1
            $result.FooterLine = $line.Trim()
            $segments = @($line -split '\s*[·|]\s*')
            $modelSegment = if ($segments.Count -gt 1) { $segments[1].Trim() } else { "" }
            $result.VisibleModelText = $modelSegment
            foreach ($segment in $segments) {
                if ([string]::Equals($segment.Trim(), $ExpectedProvider, [StringComparison]::OrdinalIgnoreCase)) {
                    $result.ProviderVisible = $true
                    break
                }
            }
            $effortPattern = '(?i)(?:^|\s)' + [regex]::Escape($ExpectedReasoningEffort) + '(?:\s|$)'
            $result.ReasoningEffortVisible = [regex]::IsMatch($modelSegment, $effortPattern)
            $visibleModel = [regex]::Replace($modelSegment, '(?i)\s+' + [regex]::Escape($ExpectedReasoningEffort) + '\s*$', '').Trim()
            $result.VisibleModelText = $visibleModel
            $result.ModelVisible = [string]::Equals($visibleModel, $ExpectedModel, [StringComparison]::OrdinalIgnoreCase)
            if (-not $result.ModelVisible -and $visibleModel.EndsWith("...", [StringComparison]::Ordinal)) {
                $prefix = $visibleModel.Substring(0, $visibleModel.Length - 3)
                $result.ModelVisible = $ExpectedModel.StartsWith($prefix, [StringComparison]::OrdinalIgnoreCase)
            }
            $result.IdentityValid = $result.ProviderVisible -and $result.ModelVisible -and $result.ReasoningEffortVisible
            return [pscustomobject]$result
        }
    }
    return [pscustomobject]$result
}

function Test-ReadyPromptVisible {
    param([AllowNull()][string]$VisibleText)
    return (Get-ReadyPromptEvidence -VisibleText $VisibleText -ExpectedProvider "" -ExpectedModel "" -ExpectedReasoningEffort "").Ready
}

function Get-ReasoningEvidence {
    param(
        [Parameter(Mandatory)][string]$Document,
        [Parameter(Mandatory)][int]$FirstMarkerIndex,
        [Parameter(Mandatory)][string]$Sentinel
    )
    if ($FirstMarkerIndex -le 0) {
        return [pscustomobject]@{ Found = $false; Unique = $false; Text = ""; Index = -1; LineNumber = -1; MatchCount = 0 }
    }
    $linePattern = '(?m)^[\t ]*' + [regex]::Escape($Sentinel) + '[\t ]*\r?$'
    $matches = [regex]::Matches($Document, $linePattern, [Text.RegularExpressions.RegexOptions]::CultureInvariant)
    if ($matches.Count -gt 0) {
        $match = $matches[0]
        $sentinelOffset = $match.Value.IndexOf($Sentinel, [StringComparison]::Ordinal)
        $sentinelIndex = $match.Index + $sentinelOffset
        $lineNumber = [regex]::Matches($Document.Substring(0, $match.Index), "`n").Count + 1
        return [pscustomobject]@{
            Found = $true
            Unique = $matches.Count -eq 1
            Text  = $match.Value.Trim()
            Index = $sentinelIndex
            LineNumber = $lineNumber
            MatchCount = $matches.Count
        }
    }
    return [pscustomobject]@{ Found = $false; Unique = $false; Text = ""; Index = -1; LineNumber = -1; MatchCount = 0 }
}

function Get-StandaloneMarkerEvidence {
    param(
        [Parameter(Mandatory)][string]$Document,
        [Parameter(Mandatory)][string]$Marker
    )
    $expectedLine = $Marker + ' terminal history validation'
    $linePattern = '(?m)^' + [regex]::Escape($expectedLine) + '[\t ]*\r?$'
    $matches = [regex]::Matches($Document, $linePattern, [Text.RegularExpressions.RegexOptions]::CultureInvariant)
    if ($matches.Count -eq 0) {
        return [pscustomobject]@{ Found = $false; Unique = $false; Index = -1; LineNumber = -1; MatchCount = 0 }
    }
    $match = $matches[0]
    $markerOffset = $match.Value.IndexOf($Marker, [StringComparison]::Ordinal)
    return [pscustomobject]@{
        Found = $true
        Unique = $matches.Count -eq 1
        Index = $match.Index + $markerOffset
        LineNumber = [regex]::Matches($Document.Substring(0, $match.Index), "`n").Count + 1
        MatchCount = $matches.Count
    }
}

function Get-TextSha256 {
    param([AllowNull()][string]$Text)
    $hasher = [Security.Cryptography.SHA256]::Create()
    try {
        $bytes = [Text.Encoding]::UTF8.GetBytes([string]$Text)
        return ([BitConverter]::ToString($hasher.ComputeHash($bytes))).Replace('-', '').ToLowerInvariant()
    } finally {
        $hasher.Dispose()
    }
}

function Get-StableTerminalSnapshotEvidence {
    param(
        [Parameter(Mandatory)][long]$WindowHandle,
        [Parameter(Mandatory)][string]$Needle,
        [ValidateRange(2, 3)][int]$RequiredStableSamples = 3,
        [ValidateRange(3, 12)][int]$MaxAttempts = 9,
        [ValidateRange(50, 2000)][int]$DelayMilliseconds = 250
    )
    $observations = [Collections.Generic.List[object]]::new()
    $snapshot = $null
    $previousFingerprint = ''
    $consecutiveStableSamples = 0
    for ($attempt = 1; $attempt -le $MaxAttempts; $attempt++) {
        $candidate = [AicliLiveTerminalAutomation]::Capture($WindowHandle, $Needle)
        if ($null -eq $candidate) {
            $previousFingerprint = ''
            $consecutiveStableSamples = 0
            $observations.Add([ordered]@{
                attempt = $attempt
                captured_at_utc = (Get-Date).ToUniversalTime().ToString('o')
                captured = $false
            })
        } else {
            $allText = [string]$candidate.AllText
            $visibleText = [string]$candidate.VisibleText
            $documentHash = Get-TextSha256 $allText
            $visibleHash = Get-TextSha256 $visibleText
            $fingerprint = $documentHash + ':' + $visibleHash
            if ($fingerprint -ceq $previousFingerprint) {
                $consecutiveStableSamples++
            } else {
                $consecutiveStableSamples = 1
                $previousFingerprint = $fingerprint
            }
            $snapshot = $candidate
            $observations.Add([ordered]@{
                attempt = $attempt
                captured_at_utc = (Get-Date).ToUniversalTime().ToString('o')
                captured = $true
                document_characters = $allText.Length
                visible_characters = $visibleText.Length
                document_sha256 = $documentHash
                visible_sha256 = $visibleHash
                consecutive_stable_samples = $consecutiveStableSamples
            })
            if ($consecutiveStableSamples -ge $RequiredStableSamples) { break }
        }
        if ($attempt -lt $MaxAttempts) { Start-Sleep -Milliseconds $DelayMilliseconds }
    }
    $finalAllText = if ($null -eq $snapshot) { '' } else { [string]$snapshot.AllText }
    $finalVisibleText = if ($null -eq $snapshot) { '' } else { [string]$snapshot.VisibleText }
    return [pscustomobject]@{
        Snapshot = $snapshot
        CaptureAttempts = $observations.Count
        RequiredStableSamples = $RequiredStableSamples
        ConsecutiveStableSamples = $consecutiveStableSamples
        Stable = $null -ne $snapshot -and $consecutiveStableSamples -ge $RequiredStableSamples
        DocumentCharacters = $finalAllText.Length
        VisibleCharacters = $finalVisibleText.Length
        DocumentSHA256 = if ($null -eq $snapshot) { '' } else { Get-TextSha256 $finalAllText }
        VisibleSHA256 = if ($null -eq $snapshot) { '' } else { Get-TextSha256 $finalVisibleText }
        Observations = @($observations)
    }
}

function Get-JsonPathString {
    param(
        [AllowNull()]$Value,
        [Parameter(Mandatory)][string[]]$Path
    )
    $current = $Value
    foreach ($segment in $Path) {
        if ($null -eq $current) { return "" }
        $property = $current.PSObject.Properties[$segment]
        if ($null -eq $property) { return "" }
        $current = $property.Value
    }
    if ($null -eq $current) { return "" }
    return [string]$current
}

function Get-ProviderRouteEvidence {
    param(
        [Parameter(Mandatory)][string]$LogRoot,
        [Parameter(Mandatory)][string]$ExpectedProvider,
        [Parameter(Mandatory)][string]$ExpectedModel,
        [Parameter(Mandatory)][string]$ExpectedReasoningEffort,
        [Parameter(Mandatory)][string]$ExpectedCompatibilityProfile
    )
    $records = [Collections.Generic.List[object]]::new()
    $parseErrors = [Collections.Generic.List[string]]::new()
    $files = @(Get-ChildItem -LiteralPath $LogRoot -Recurse -File -Filter '*_request_provider_wrapper.json' -ErrorAction SilentlyContinue | Sort-Object FullName)
    foreach ($file in $files) {
        try {
            $payload = Get-Content -LiteralPath $file.FullName -Raw -Encoding utf8 | ConvertFrom-Json
            $record = [ordered]@{
                path = $file.FullName
                protocol = Get-JsonPathString $payload @('protocol')
                compatibility_profile = Get-JsonPathString $payload @('request_metadata', '_request_debug', 'compatibility_profile')
                requested_provider = Get-JsonPathString $payload @('requested_provider')
                effective_provider = Get-JsonPathString $payload @('effective_provider')
                requested_model = Get-JsonPathString $payload @('requested_model')
                effective_model = Get-JsonPathString $payload @('effective_model')
                requested_reasoning_effort = Get-JsonPathString $payload @('requested_reasoning_effort')
                effective_reasoning_effort = Get-JsonPathString $payload @('effective_reasoning_effort')
                valid = $false
            }
            $record.valid =
                [string]::Equals($record.requested_provider, $ExpectedProvider, [StringComparison]::OrdinalIgnoreCase) -and
                [string]::Equals($record.effective_provider, $ExpectedProvider, [StringComparison]::OrdinalIgnoreCase) -and
                [string]::Equals($record.requested_model, $ExpectedModel, [StringComparison]::OrdinalIgnoreCase) -and
                [string]::Equals($record.effective_model, $ExpectedModel, [StringComparison]::OrdinalIgnoreCase) -and
                [string]::Equals($record.requested_reasoning_effort, $ExpectedReasoningEffort, [StringComparison]::OrdinalIgnoreCase) -and
                [string]::Equals($record.effective_reasoning_effort, $ExpectedReasoningEffort, [StringComparison]::OrdinalIgnoreCase) -and
                [string]::Equals($record.compatibility_profile, $ExpectedCompatibilityProfile, [StringComparison]::OrdinalIgnoreCase)
            $records.Add($record)
        } catch {
            $parseErrors.Add("$($file.FullName): $($_.Exception.Message)")
        }
    }
    $invalidCount = @($records | Where-Object { -not $_.valid }).Count
    return [pscustomobject]@{
        Found = $records.Count -gt 0
        Valid = $records.Count -gt 0 -and $invalidCount -eq 0 -and $parseErrors.Count -eq 0
        RecordCount = $records.Count
        Records = @($records)
        ParseErrors = @($parseErrors)
    }
}

function Get-MarkerBlankLineViolations {
    param(
        [Parameter(Mandatory)][string]$Document,
        [Parameter(Mandatory)][string]$Prefix
    )
    $violations = [Collections.Generic.List[object]]::new()
    $lines = [regex]::Split($Document, "\r?\n")
    $markerPattern = '^' + [regex]::Escape($Prefix) +
        '-(?<number>\d{2}) terminal history validation[\t ]*$'
    $previousNumber = 0
    $previousLine = -1
    for ($lineIndex = 0; $lineIndex -lt $lines.Count; $lineIndex++) {
        $match = [regex]::Match($lines[$lineIndex], $markerPattern)
        if (-not $match.Success) { continue }
        $number = [int]$match.Groups['number'].Value
        if ($previousNumber -gt 0 -and $number -eq ($previousNumber + 1)) {
            $blankLines = [Collections.Generic.List[int]]::new()
            for ($between = $previousLine + 1; $between -lt $lineIndex; $between++) {
                if ([string]::IsNullOrWhiteSpace($lines[$between])) {
                    $blankLines.Add($between + 1)
                }
            }
            if ($blankLines.Count -gt 0) {
                $violations.Add([ordered]@{
                    after_marker = $previousNumber
                    before_marker = $number
                    blank_line_numbers = @($blankLines)
                })
            }
        }
        $previousNumber = $number
        $previousLine = $lineIndex
    }
    return @($violations)
}

function Write-E2EManifest {
    param([Parameter(Mandatory)][Collections.IDictionary]$Data)
    Write-Utf8NoBom -Path $manifestPath -Value ($Data | ConvertTo-Json -Depth 8)
}

function Test-AicliTerminalE2EHelpers {
    $fixturePrefix = "WTFIXTURE"
    $fixtureReasoning = "REASONFIXTURE"
    $fixtureLines = @(
        $fixtureReasoning,
        "$fixturePrefix-01 terminal history validation",
        "$fixturePrefix-02 terminal history validation",
        "$fixturePrefix-03 terminal history validation"
    )
    $fixture = $fixtureLines -join "`r`n"
    $firstIndex = $fixture.IndexOf("$fixturePrefix-01", [StringComparison]::Ordinal)
    $reasoning = Get-ReasoningEvidence -Document $fixture -FirstMarkerIndex $firstIndex -Sentinel $fixtureReasoning
    if (-not $reasoning.Found -or -not $reasoning.Unique -or $reasoning.Index -ge $firstIndex -or $reasoning.LineNumber -ne 1) {
        throw "helper self-test failed: reasoning evidence ordering"
    }
    $embeddedFixture = "prompt contains [$fixtureReasoning]`r`n$fixturePrefix-01 terminal history validation"
    $embeddedFirstIndex = $embeddedFixture.IndexOf("$fixturePrefix-01", [StringComparison]::Ordinal)
    if ((Get-ReasoningEvidence -Document $embeddedFixture -FirstMarkerIndex $embeddedFirstIndex -Sentinel $fixtureReasoning).Found) {
        throw "helper self-test failed: embedded prompt token was accepted as reasoning evidence"
    }
    $duplicateFixture = $fixture + "`r`n" + $fixtureReasoning
    $duplicateReasoning = Get-ReasoningEvidence -Document $duplicateFixture -FirstMarkerIndex $firstIndex -Sentinel $fixtureReasoning
    if (-not $duplicateReasoning.Found -or $duplicateReasoning.Unique -or $duplicateReasoning.MatchCount -ne 2) {
        throw "helper self-test failed: duplicate standalone reasoning evidence was accepted"
    }
    $markerReference = "reasoning cites `"$fixturePrefix-01 terminal history validation`" as an example"
    $markerFixture = $markerReference + "`r`n" + $fixtureLines[1]
    $markerEvidence = Get-StandaloneMarkerEvidence -Document $markerFixture -Marker "$fixturePrefix-01"
    if (-not $markerEvidence.Found -or -not $markerEvidence.Unique -or $markerEvidence.MatchCount -ne 1 -or
        $markerEvidence.Index -ne $markerFixture.LastIndexOf($fixturePrefix, [StringComparison]::Ordinal)) {
        throw "helper self-test failed: prose marker reference was counted as a rendered marker line"
    }
    $duplicateMarker = Get-StandaloneMarkerEvidence -Document ($fixtureLines[1] + "`r`n" + $fixtureLines[1]) -Marker "$fixturePrefix-01"
    if (-not $duplicateMarker.Found -or $duplicateMarker.Unique -or $duplicateMarker.MatchCount -ne 2) {
        throw "helper self-test failed: duplicate standalone marker line was accepted"
    }
    foreach ($malformedMarkerLine in @(
        (' ' + $fixtureLines[1])
        "$fixturePrefix-01  terminal history validation"
        ($fixtureLines[1] + ' trailing prose')
    )) {
        if ((Get-StandaloneMarkerEvidence -Document $malformedMarkerLine -Marker "$fixturePrefix-01").Found) {
            throw "helper self-test failed: malformed marker line was accepted"
        }
    }
    if (@(Get-MarkerBlankLineViolations -Document $fixture -Prefix $fixturePrefix).Count -ne 0) {
        throw "helper self-test failed: contiguous markers reported a blank-line violation"
    }
    $fixtureWithBlank = $fixtureLines[0..1] + "" + $fixtureLines[2..3]
    $blankViolations = @(Get-MarkerBlankLineViolations -Document ($fixtureWithBlank -join "`r`n") -Prefix $fixturePrefix)
    if ($blankViolations.Count -ne 1 -or $blankViolations[0].after_marker -ne 1 -or $blankViolations[0].before_marker -ne 2) {
        throw "helper self-test failed: marker blank-line violation was not detected"
    }
    $readyFixture = ">`r`n`r`nPlan OFF · deepseek-v4-f... max · opencode.ai"
    if (-not (Test-ReadyPromptVisible $readyFixture)) {
        throw "helper self-test failed: restored prompt/idle footer was not recognized"
    }
    $identity = Get-ReadyPromptEvidence -VisibleText $readyFixture -ExpectedProvider "opencode.ai" -ExpectedModel "deepseek-v4-flash" -ExpectedReasoningEffort "max"
    if (-not $identity.Ready -or -not $identity.IdentityValid -or -not $identity.ProviderVisible -or -not $identity.ModelVisible -or -not $identity.ReasoningEffortVisible) {
        throw "helper self-test failed: provider/model/effort footer identity was not recognized"
    }
    $wrongIdentity = Get-ReadyPromptEvidence -VisibleText $readyFixture -ExpectedProvider "wrong-provider" -ExpectedModel "deepseek-v4-flash" -ExpectedReasoningEffort "max"
    if ($wrongIdentity.IdentityValid -or $wrongIdentity.ProviderVisible) {
        throw "helper self-test failed: wrong provider was accepted from the idle footer"
    }
    if (Test-ReadyPromptVisible ">`r`nWorking · model") {
        throw "helper self-test failed: busy footer was recognized as Ready"
    }
}

function Invoke-AicliTerminalE2E {
function Save-TerminalSnapshot {
    param(
        [AllowNull()]$Snapshot,
        [string]$FullPath,
        [string]$VisiblePath,
        [string]$Secret
    )
    $allText = if ($null -eq $Snapshot) { "" } else { [string]$Snapshot.AllText }
    $visibleText = if ($null -eq $Snapshot) { "" } else { [string]$Snapshot.VisibleText }
    Write-Utf8NoBom -Path $FullPath -Value (Protect-SensitiveText $allText $Secret)
    Write-Utf8NoBom -Path $VisiblePath -Value (Protect-SensitiveText $visibleText $Secret)
}

$snapshot = $null
$terminalHandle = 0L
$terminalPID = 0
$terminalTitle = ""
$terminalProcessName = ""
$testError = ""
$exitSendError = ""
$exitWasSent = $false
$exitWasConfirmed = $false
$exitTransport = "attach_console_write_console_input"
$exitTargetProcessID = 0
$exitHelperExitCode = $null
$forcedCleanupCount = 0
$completedAt = $null
$buildInfo = $null
$requestCompleted = $false
$readyPromptRestored = $false
$statusIdentityValidated = $false
$tailObserved = $false
$inputAccepted = $false
$inputAttempts = 0
$stableSamples = 0
$exactlyOnce = 0
$statusEvidence = Get-ReadyPromptEvidence -VisibleText "" -ExpectedProvider $expectedProvider -ExpectedModel $expectedModel -ExpectedReasoningEffort $expectedReasoningEffort
$routeEvidence = [pscustomobject]@{ Found = $false; Valid = $false; RecordCount = 0; Records = @(); ParseErrors = @() }
$markerResults = @()
$markerIndicesStrictlyIncreasing = $false
$reasoningEvidence = [pscustomobject]@{ Found = $false; Unique = $false; Text = ""; Index = -1; LineNumber = -1; MatchCount = 0 }
$blankLineViolations = @()
$rawReasoningLabel = $false
$rawRequestStartedLabel = $false
$rawRequestFinishedLabel = $false
$firstInFull = $false
$firstInVisible = $false
$lastInVisible = $false
$firstMarkerIndex = -1
$runnerExitCode = $null
$postExitRunnerExitCode = $null
$postCleanupRunnerExitCode = $null
$runnerExitObservedAfter = ''
$snapshotEvidenceError = ''
$snapshotEvidence = [pscustomobject]@{
    Snapshot = $null
    CaptureAttempts = 0
    RequiredStableSamples = 3
    ConsecutiveStableSamples = 0
    Stable = $false
    DocumentCharacters = 0
    VisibleCharacters = 0
    DocumentSHA256 = ''
    VisibleSHA256 = ''
    Observations = @()
}
$assertionFailures = [Collections.Generic.List[string]]::new()
$startedAt = Get-Date

try {
    $goCommand = Get-Command go -ErrorAction Stop
    $wtCommand = Get-Command wt.exe -ErrorAction Stop
    $shellCommand = Get-Command pwsh.exe -ErrorAction SilentlyContinue
    if ($null -eq $shellCommand) {
        $shellCommand = Get-Command powershell.exe -ErrorAction Stop
    }

    Push-Location $backendDir
    try {
        & $goCommand.Source build -trimpath -o $executable ./cmd/aicli
        if ($LASTEXITCODE -ne 0) {
            throw "go build failed with exit code $LASTEXITCODE."
        }
    } finally {
        Pop-Location
    }
    $buildInfo = Get-Item -LiteralPath $executable

    $wtArguments = @(
        "-w", "new",
        "--size", "100,24",
        "new-tab",
        "--title", $windowTitle,
        "--suppressApplicationTitle",
        "-d", $backendDir,
        $shellCommand.Source,
        "-NoLogo",
        "-NoProfile",
        "-ExecutionPolicy", "Bypass",
        "-File", $runnerPath
    )
    & $wtCommand.Source @wtArguments
    if ($LASTEXITCODE -ne 0) {
        throw "wt.exe failed to launch the isolated terminal window (exit code $LASTEXITCODE)."
    }

    $startupDeadline = (Get-Date).AddSeconds($StartupTimeoutSeconds)
    while ((Get-Date) -lt $startupDeadline) {
        $snapshot = [AicliLiveTerminalAutomation]::Find($startupSentinel)
        if ($null -ne $snapshot) { break }
        if (Test-Path -LiteralPath $exitCodePath) { break }
        Start-Sleep -Milliseconds 250
    }
    if ($null -eq $snapshot) {
        throw "Windows Terminal UIA TextPattern did not expose the startup sentinel within $StartupTimeoutSeconds seconds."
    }

    $terminalHandle = [long]$snapshot.WindowHandle
    $terminalPID = [int]$snapshot.ProcessId
    $terminalTitle = [string]$snapshot.WindowTitle
    $terminalProcessName = [string](Get-Process -Id $terminalPID -ErrorAction Stop).ProcessName
    if (-not [string]::Equals($terminalProcessName, "WindowsTerminal", [StringComparison]::OrdinalIgnoreCase)) {
        throw "UI Automation window belongs to process '$terminalProcessName', expected WindowsTerminal.exe."
    }
    # The test prompt is supplied by --prompt in the runner command. UIA is
    # observation-only until the final /exit cleanup, so startup cannot pass
    # by accidentally targeting another focused terminal window.
    $inputAccepted = $true
    $inputAttempts = 1

    $lastMarker = $runPrefix + "-40"
    $responseDeadline = (Get-Date).AddSeconds($TimeoutSeconds)
    $stableFingerprint = ""
    while ((Get-Date) -lt $responseDeadline) {
        $candidate = [AicliLiveTerminalAutomation]::Capture($terminalHandle, $startupSentinel)
        if ($null -ne $candidate) {
            $snapshot = $candidate
            $tailPresent = (Get-StandaloneMarkerEvidence -Document ([string]$candidate.AllText) -Marker $lastMarker).Found
            if ($tailPresent) { $tailObserved = $true }
            $statusEvidence = Get-ReadyPromptEvidence -VisibleText ([string]$candidate.VisibleText) -ExpectedProvider $expectedProvider -ExpectedModel $expectedModel -ExpectedReasoningEffort $expectedReasoningEffort
            $readyPromptRestored = $statusEvidence.Ready
            $statusIdentityValidated = $statusEvidence.IdentityValid
            if ($tailPresent -and $readyPromptRestored -and $statusIdentityValidated) {
                $fingerprint = [string]$candidate.AllText + [char]0 + [string]$candidate.VisibleText
                if ($fingerprint -ceq $stableFingerprint) {
                    $stableSamples++
                } else {
                    $stableFingerprint = $fingerprint
                    $stableSamples = 1
                }
                if ($stableSamples -ge 3) {
                    $completedAt = Get-Date
                    $requestCompleted = $true
                    break
                }
            } else {
                $stableFingerprint = ""
                $stableSamples = 0
            }
        }
        if (Test-Path -LiteralPath $exitCodePath) { break }
        Start-Sleep -Milliseconds 750
    }

    $snapshotEvidence = Get-StableTerminalSnapshotEvidence -WindowHandle $terminalHandle -Needle $startupSentinel
    if ($null -ne $snapshotEvidence.Snapshot) { $snapshot = $snapshotEvidence.Snapshot }
    if ((Get-StandaloneMarkerEvidence -Document ([string]$snapshot.AllText) -Marker $lastMarker).Found) {
        $tailObserved = $true
    }
    $statusEvidence = Get-ReadyPromptEvidence -VisibleText ([string]$snapshot.VisibleText) -ExpectedProvider $expectedProvider -ExpectedModel $expectedModel -ExpectedReasoningEffort $expectedReasoningEffort
    $readyPromptRestored = $statusEvidence.Ready
    $statusIdentityValidated = $statusEvidence.IdentityValid
    Save-TerminalSnapshot $snapshot $fullDump $visibleDump $apiKey
    $routeEvidence = Get-ProviderRouteEvidence -LogRoot $chatLogDir -ExpectedProvider $expectedProvider -ExpectedModel $expectedModel -ExpectedReasoningEffort $expectedReasoningEffort -ExpectedCompatibilityProfile $expectedCompatibilityProfile

    $document = [string]$snapshot.AllText
    $visible = [string]$snapshot.VisibleText
    $failures = [Collections.Generic.List[string]]::new()
    if (-not $tailObserved -or -not $readyPromptRestored -or -not $statusIdentityValidated -or -not $requestCompleted -or $null -eq $completedAt) {
        $failures.Add("Timed out before marker 40 was stable and the expected provider/model/effort footer plus interactive prompt returned to Ready")
    }
    if (-not $snapshotEvidence.Stable) {
        $failures.Add("final UIA snapshot did not reach three identical DocumentRange/VisibleRanges samples")
    }
    $previousMarkerIndex = -1
    $indicesIncreasing = $true
    $markerResults = [Collections.Generic.List[object]]::new()
    for ($index = 1; $index -le 40; $index++) {
        $marker = $runPrefix + ("-{0:D2}" -f $index)
        $markerEvidence = Get-StandaloneMarkerEvidence -Document $document -Marker $marker
        $count = $markerEvidence.MatchCount
        $documentIndex = $markerEvidence.Index
        if ($count -eq 1) {
            $exactlyOnce++
        } else {
            $failures.Add("marker $index count=$count; expected exactly one")
        }
        if ($documentIndex -lt 0 -or ($previousMarkerIndex -ge 0 -and $documentIndex -le $previousMarkerIndex)) {
            $indicesIncreasing = $false
            $failures.Add("marker $index document index=$documentIndex is not strictly after $previousMarkerIndex")
        }
        $markerResults.Add([ordered]@{
            number = $index
            marker = $marker
            count = $count
            document_index = $documentIndex
        })
        if ($documentIndex -ge 0) { $previousMarkerIndex = $documentIndex }
    }
    $markerIndicesStrictlyIncreasing = $indicesIncreasing

    $firstMarker = $runPrefix + "-01"
    $firstMarkerEvidence = Get-StandaloneMarkerEvidence -Document $document -Marker $firstMarker
    $firstVisibleEvidence = Get-StandaloneMarkerEvidence -Document $visible -Marker $firstMarker
    $lastVisibleEvidence = Get-StandaloneMarkerEvidence -Document $visible -Marker $lastMarker
    $firstInFull = $firstMarkerEvidence.Found
    $firstInVisible = $firstVisibleEvidence.Found
    $lastInVisible = $lastVisibleEvidence.Found
    $rawReasoningLabel = $document.IndexOf("assistant.reasoning", [StringComparison]::OrdinalIgnoreCase) -ge 0
    $rawRequestStartedLabel = $document.IndexOf("llm.request.started", [StringComparison]::OrdinalIgnoreCase) -ge 0
    $rawRequestFinishedLabel = $document.IndexOf("llm.request.finished", [StringComparison]::OrdinalIgnoreCase) -ge 0
    $firstMarkerIndex = $firstMarkerEvidence.Index
    $reasoningEvidence = Get-ReasoningEvidence -Document $document -FirstMarkerIndex $firstMarkerIndex -Sentinel $reasoningSentinel
    $blankLineViolations = @(Get-MarkerBlankLineViolations -Document $document -Prefix $runPrefix)

    if (-not $firstInFull) {
        $failures.Add("marker 01 is absent from the host DocumentRange")
    }
    if ($firstInVisible) {
        $failures.Add("marker 01 is still visible; it never entered host scrollback")
    }
    if (-not $lastInVisible) {
        $failures.Add("marker 40 is absent from UIA VisibleRanges")
    }
    if (-not $statusEvidence.Ready) {
        $failures.Add("restored prompt and idle status footer are absent from UIA VisibleRanges")
    }
    if (-not $statusEvidence.ProviderVisible) {
        $failures.Add("expected provider '$expectedProvider' is absent from the visible idle status footer")
    }
    if (-not $statusEvidence.ModelVisible) {
        $failures.Add("expected model '$expectedModel' is absent from the visible idle status footer")
    }
    if (-not $statusEvidence.ReasoningEffortVisible) {
        $failures.Add("expected reasoning effort '$expectedReasoningEffort' is absent from the visible idle status footer")
    }
    if (-not $routeEvidence.Valid) {
        $failures.Add("provider request artifacts do not prove the expected provider/model/reasoning-effort route")
    }
    if ($rawReasoningLabel) {
        $failures.Add("raw assistant.reasoning text leaked into terminal output")
    }
    if ($rawRequestStartedLabel) {
        $failures.Add("raw llm.request.started text leaked into terminal output")
    }
    if ($rawRequestFinishedLabel) {
        $failures.Add("raw llm.request.finished text leaked into terminal output")
    }
    if (-not $reasoningEvidence.Found -or -not $reasoningEvidence.Unique -or $reasoningEvidence.Index -lt 0 -or $reasoningEvidence.Index -ge $firstMarkerIndex) {
        $failures.Add("exactly one standalone reasoning sentinel line was not found before marker 01")
    }
    if ($blankLineViolations.Count -gt 0) {
        $failures.Add("found $($blankLineViolations.Count) abnormal blank-line gap(s) between consecutive marker lines")
    }
    foreach ($failure in $failures) { $assertionFailures.Add($failure) }
    if ($failures.Count -gt 0) {
        throw (($failures -join "; ") + ".")
    }
} catch {
    $caughtError = Protect-SensitiveText ([string]$_.Exception.Message) $apiKey
    if ($terminalHandle -ne 0 -and $snapshotEvidence.CaptureAttempts -eq 0) {
        try {
            $snapshotEvidence = Get-StableTerminalSnapshotEvidence -WindowHandle $terminalHandle -Needle $startupSentinel
            if ($null -ne $snapshotEvidence.Snapshot) { $snapshot = $snapshotEvidence.Snapshot }
        } catch {
            $snapshotEvidenceError = Protect-SensitiveText ([string]$_.Exception.Message) $apiKey
        }
    }
    Save-TerminalSnapshot $snapshot $fullDump $visibleDump $apiKey
    $testError = $caughtError
} finally {
    Remove-SecretArtifact $secretPath

    $expectedExecutable = [IO.Path]::GetFullPath($executable)
    if (-not (Test-Path -LiteralPath $exitCodePath)) {
        try {
            $testProcesses = @(Get-CimInstance Win32_Process -ErrorAction Stop | Where-Object {
                -not [string]::IsNullOrWhiteSpace([string]$_.ExecutablePath) -and
                [string]::Equals([IO.Path]::GetFullPath([string]$_.ExecutablePath), $expectedExecutable, [StringComparison]::OrdinalIgnoreCase) -and
                ([string]$_.CommandLine).Contains($runID)
            })
            if ($testProcesses.Count -ne 1) {
                throw "Expected exactly one live test AICLI process for run '$runID'; found $($testProcesses.Count)."
            }
            $exitTargetProcessID = [int]$testProcesses[0].ProcessId
            Write-Utf8NoBom -Path $exitInputPath -Value "/exit"
            $inputShell = Get-Command pwsh.exe -ErrorAction SilentlyContinue
            if ($null -eq $inputShell) {
                $inputShell = Get-Command powershell.exe -ErrorAction Stop
            }
            $helperOutput = @(& $inputShell.Source -NoLogo -NoProfile -NonInteractive -ExecutionPolicy Bypass `
                -File $consoleInputWriterPath -TargetProcessId $exitTargetProcessID -TextPath $exitInputPath 2>&1)
            $exitHelperExitCode = [int]$LASTEXITCODE
            if ($exitHelperExitCode -ne 0) {
                $helperDetail = (($helperOutput | ForEach-Object { [string]$_ }) -join " | ").Trim()
                throw "Console input writer exited with code $exitHelperExitCode. $helperDetail"
            }
            $exitWasSent = $true
        } catch {
            $exitSendError = Protect-SensitiveText ([string]$_.Exception.Message) $apiKey
        } finally {
            Remove-Item -LiteralPath $exitInputPath -Force -ErrorAction SilentlyContinue
        }
    }

    $exitDeadline = (Get-Date).AddSeconds(20)
    while ($terminalHandle -ne 0 -and -not (Test-Path -LiteralPath $exitCodePath) -and (Get-Date) -lt $exitDeadline) {
        Start-Sleep -Milliseconds 250
    }
    $exitWasConfirmed = Test-Path -LiteralPath $exitCodePath
    if ($exitWasConfirmed) {
        $postExitRunnerExitCode = ([IO.File]::ReadAllText($exitCodePath)).Trim()
        $runnerExitCode = $postExitRunnerExitCode
        $runnerExitObservedAfter = 'post_exit_wait'
    }
    if (-not $exitWasConfirmed) {
        $staleProcesses = @(Get-CimInstance Win32_Process -ErrorAction SilentlyContinue | Where-Object {
            -not [string]::IsNullOrWhiteSpace([string]$_.ExecutablePath) -and
            [string]::Equals([IO.Path]::GetFullPath([string]$_.ExecutablePath), $expectedExecutable, [StringComparison]::OrdinalIgnoreCase) -and
            ([string]$_.CommandLine).Contains($runID)
        })
        foreach ($staleProcess in $staleProcesses) {
            Stop-Process -Id ([int]$staleProcess.ProcessId) -Force -ErrorAction SilentlyContinue
            $forcedCleanupCount++
        }
        if ($terminalHandle -ne 0 -or $forcedCleanupCount -gt 0) {
            $cleanupExitDeadline = (Get-Date).AddSeconds(5)
            while (-not (Test-Path -LiteralPath $exitCodePath) -and (Get-Date) -lt $cleanupExitDeadline) {
                Start-Sleep -Milliseconds 100
            }
        }
    }
    # The runner writes its exit file in a finally block. Re-read after cleanup
    # so a child terminated at the deadline is reported as a non-zero exit,
    # rather than as an unconfirmed exit race.
    if (Test-Path -LiteralPath $exitCodePath) {
        $exitWasConfirmed = $true
        $postCleanupRunnerExitCode = ([IO.File]::ReadAllText($exitCodePath)).Trim()
        $runnerExitCode = $postCleanupRunnerExitCode
        if ([string]::IsNullOrWhiteSpace($runnerExitObservedAfter)) {
            $runnerExitObservedAfter = if ($forcedCleanupCount -gt 0) {
                'post_forced_cleanup_recheck'
            } else {
                'post_cleanup_recheck'
            }
        }
    }
    Scrub-TextArtifacts $runRoot $apiKey
    $apiKey = $null
    $credential = $null

    $manifestStatus = if (
        [string]::IsNullOrWhiteSpace($testError) -and
        [string]::IsNullOrWhiteSpace($exitSendError) -and
        $exitWasSent -and $exitWasConfirmed -and $runnerExitCode -eq "0" -and
        $requestCompleted -and $readyPromptRestored -and $statusIdentityValidated -and $routeEvidence.Valid -and
        $assertionFailures.Count -eq 0
    ) { "passed" } else { "failed" }
    $manifestFailures = [Collections.Generic.List[string]]::new()
    foreach ($failure in $assertionFailures) { $manifestFailures.Add([string]$failure) }
    if (-not [string]::IsNullOrWhiteSpace($testError) -and -not $manifestFailures.Contains($testError)) {
        $manifestFailures.Add($testError)
    }
    if (-not [string]::IsNullOrWhiteSpace($exitSendError)) {
        $manifestFailures.Add("exit input failed: $exitSendError")
    }
    if (-not [string]::IsNullOrWhiteSpace($snapshotEvidenceError)) {
        $manifestFailures.Add("stable UIA evidence capture failed: $snapshotEvidenceError")
    }
    if (-not $exitWasSent) {
        $manifestFailures.Add("exit command was not sent through the real terminal")
    }
    if (-not $exitWasConfirmed) {
        $manifestFailures.Add("runner exit was not confirmed within the deadline")
    }
    if ($exitWasConfirmed -and $runnerExitCode -ne "0") {
        $manifestFailures.Add("runner exit code was '$runnerExitCode'; expected 0")
    }
    Write-E2EManifest -Data ([ordered]@{
        schema_version = 1
        status = $manifestStatus
        run_id = $runID
        run_prefix = $runPrefix
        reasoning_sentinel = $reasoningSentinel
        started_at = $startedAt.ToUniversalTime().ToString("o")
        completed_at = if ($null -eq $completedAt) { $null } else { $completedAt.ToUniversalTime().ToString("o") }
        provider = [ordered]@{
            name = $expectedProvider
            base_url = "https://opencode.ai/zen/go"
            compatibility_profile = $expectedCompatibilityProfile
            model = $expectedModel
            reasoning_effort = $expectedReasoningEffort
            stream = $true
            tools_disabled = $true
            route_artifacts_found = $routeEvidence.Found
            route_artifacts_valid = $routeEvidence.Valid
            route_artifact_count = $routeEvidence.RecordCount
            route_records = @($routeEvidence.Records)
            route_parse_errors = @($routeEvidence.ParseErrors)
        }
        terminal = [ordered]@{
            process_id = $terminalPID
            process_name = $terminalProcessName
            window_handle = $terminalHandle
            expected_title = $windowTitle
            captured_title = $terminalTitle
            ready_footer_line = $statusEvidence.FooterLine
            ready_footer_line_number = $statusEvidence.FooterLineNumber
            prompt_line_number = $statusEvidence.PromptLineNumber
        }
        uia_capture = [ordered]@{
            stable = $snapshotEvidence.Stable
            required_stable_samples = $snapshotEvidence.RequiredStableSamples
            consecutive_stable_samples = $snapshotEvidence.ConsecutiveStableSamples
            capture_attempts = $snapshotEvidence.CaptureAttempts
            document_characters = $snapshotEvidence.DocumentCharacters
            visible_characters = $snapshotEvidence.VisibleCharacters
            document_sha256 = $snapshotEvidence.DocumentSHA256
            visible_sha256 = $snapshotEvidence.VisibleSHA256
            observations = @($snapshotEvidence.Observations)
            error = if ([string]::IsNullOrWhiteSpace($snapshotEvidenceError)) { $null } else { $snapshotEvidenceError }
        }
        input = [ordered]@{
            transport = "cli_prompt_argument"
            app_activation = "none_for_prompt"
            prompt_cli_flag_used = $true
            accepted = $inputAccepted
            attempts = $inputAttempts
        }
        completion = [ordered]@{
            marker_40_observed = $tailObserved
            request_completed = $requestCompleted
            ready_prompt_restored = $readyPromptRestored
            status_identity_validated = $statusIdentityValidated
            stable_samples = $stableSamples
        }
        assertions = [ordered]@{
            marker_count_exactly_once = $exactlyOnce
            marker_indices_strictly_increasing = $markerIndicesStrictlyIncreasing
            marker_01_in_document = $firstInFull
            marker_01_visible = $firstInVisible
            marker_40_visible = $lastInVisible
            expected_provider_visible = $statusEvidence.ProviderVisible
            expected_model_visible = $statusEvidence.ModelVisible
            expected_reasoning_effort_visible = $statusEvidence.ReasoningEffortVisible
            provider_route_artifacts_valid = $routeEvidence.Valid
            reasoning_before_marker_01 = [bool]($reasoningEvidence.Found -and $reasoningEvidence.Unique -and $reasoningEvidence.Index -lt $firstMarkerIndex)
            reasoning_document_index = [int]$reasoningEvidence.Index
            reasoning_document_line = [int]$reasoningEvidence.LineNumber
            reasoning_standalone_line_count = [int]$reasoningEvidence.MatchCount
            raw_assistant_reasoning_present = $rawReasoningLabel
            raw_llm_request_started_present = $rawRequestStartedLabel
            raw_llm_request_finished_present = $rawRequestFinishedLabel
            abnormal_blank_line_gap_count = $blankLineViolations.Count
        }
        markers = @($markerResults)
        abnormal_blank_line_gaps = @($blankLineViolations)
        failures = @($manifestFailures)
        error = if ([string]::IsNullOrWhiteSpace($testError)) { $null } else { $testError }
        exit = [ordered]@{
            transport = $exitTransport
            command_sent = $exitWasSent
            target_process_id = $exitTargetProcessID
            helper_exit_code = $exitHelperExitCode
            confirmed = $exitWasConfirmed
            runner_exit_code = $runnerExitCode
            observed_after = $runnerExitObservedAfter
            post_exit_wait_runner_exit_code = $postExitRunnerExitCode
            post_cleanup_recheck_runner_exit_code = $postCleanupRunnerExitCode
            forced_cleanup_count = $forcedCleanupCount
            error = if ([string]::IsNullOrWhiteSpace($exitSendError)) { $null } else { $exitSendError }
        }
        artifacts = [ordered]@{
            document_range = $fullDump
            visible_ranges = $visibleDump
            session_directory = $sessionDir
            chat_log_directory = $chatLogDir
            process_log = $processLog
            manifest = $manifestPath
        }
    })
}

if (-not [string]::IsNullOrWhiteSpace($testError)) {
    $exitDetail = if ([string]::IsNullOrWhiteSpace($exitSendError)) { "" } else { " Exit injection also failed: $exitSendError." }
    throw ("Windows Terminal live E2E failed: $testError$exitDetail Full dump: $fullDump Visible dump: $visibleDump Logs: $chatLogDir")
}
if (-not $exitWasSent) {
    throw "Windows Terminal live E2E assertions passed, but /exit was not delivered to the real terminal. Dumps: $fullDump"
}
if (-not [string]::IsNullOrWhiteSpace($exitSendError)) {
    throw "Windows Terminal live E2E assertions passed, but /exit failed: $exitSendError"
}
if (-not $exitWasConfirmed) {
    throw "Windows Terminal live E2E assertions passed, but /exit was not observed within 20 seconds; forced cleanup count=$forcedCleanupCount. Dumps: $fullDump"
}
if ($runnerExitCode -ne "0") {
    throw "Windows Terminal live E2E assertions passed, but runner exit code was '$runnerExitCode'; expected 0. Manifest: $manifestPath"
}
$safeTitle = Protect-SensitiveText $terminalTitle $null
Write-Host "PASS: real Windows Terminal + UI Automation live E2E completed."
Write-Host "Build: $($buildInfo.FullName) | UTC $($buildInfo.LastWriteTimeUtc.ToString('o'))"
Write-Host "Terminal: process=$terminalProcessName.exe PID=$terminalPID HWND=0x$($terminalHandle.ToString('X')) title=$safeTitle"
Write-Host "Provider: opencode.ai | base_url=https://opencode.ai/zen/go | compatibility=opencode-console-go-2026-07"
Write-Host "Model: deepseek-v4-flash | reasoning_effort=max | stream=true | yolo=true | tools=disabled"
Write-Host "Credential source: $credentialSource (value was never placed on the command line or in dumps)"
Write-Host "Markers: 40/40 present exactly once"
Write-Host "Scrollback: marker01 full=True visible=False | marker40 visible=True"
Write-Host "Completion: request_completed=True | ready_prompt_restored=True"
Write-Host "UIA evidence: stable=$($snapshotEvidence.Stable) samples=$($snapshotEvidence.ConsecutiveStableSamples) document_sha256=$($snapshotEvidence.DocumentSHA256) visible_sha256=$($snapshotEvidence.VisibleSHA256)"
Write-Host "Ordering: reasoning_before_marker01=True | marker_indices_strictly_increasing=True"
Write-Host "Raw protocol labels: assistant.reasoning=False | llm.request.started=False | llm.request.finished=False"
Write-Host "Abnormal marker blank-line gaps: 0"
Write-Host "Exit: /exit sent=True confirmed=True | runner_exit_code=$runnerExitCode | observed_after=$runnerExitObservedAfter"
Write-Host "Manifest: $manifestPath"
Write-Host "Full DocumentRange dump: $fullDump"
Write-Host "VisibleRanges dump: $visibleDump"
Write-Host "Session directory: $sessionDir"
Write-Host "Chat log directory: $chatLogDir"
Write-Host "Process log: $processLog"

if ($KeepWindow) {
    Write-Host "KeepWindow was requested, but the required /exit command still closes a healthy chat session."
}
}

if (-not ("AicliLiveTerminalAutomation" -as [type])) {
    Add-Type -ReferencedAssemblies UIAutomationClient, UIAutomationTypes -TypeDefinition @'
using System;
using System.Runtime.InteropServices;
using System.Text;
using System.Windows.Automation;

public sealed class AicliLiveTerminalAutomationSnapshot
{
    public long WindowHandle;
    public int ProcessId;
    public string WindowTitle;
    public string AllText;
    public string VisibleText;
}

public static class AicliLiveTerminalAutomation
{
    private delegate bool EnumWindowsProc(IntPtr hWnd, IntPtr lParam);

    [DllImport("user32.dll")]
    private static extern bool EnumWindows(EnumWindowsProc callback, IntPtr lParam);

    [DllImport("user32.dll")]
    private static extern bool IsWindowVisible(IntPtr hWnd);

    [DllImport("user32.dll")]
    private static extern uint GetWindowThreadProcessId(IntPtr hWnd, out uint processId);

    [DllImport("user32.dll", CharSet = CharSet.Unicode)]
    private static extern int GetClassName(IntPtr hWnd, StringBuilder className, int maxCount);

    private static bool IsWindowsTerminalWindow(IntPtr hWnd, out int processId)
    {
        processId = 0;
        if (hWnd == IntPtr.Zero || !IsWindowVisible(hWnd)) return false;
        uint pid;
        GetWindowThreadProcessId(hWnd, out pid);
        if (pid == 0) return false;
        StringBuilder className = new StringBuilder(256);
        if (GetClassName(hWnd, className, className.Capacity) == 0) return false;
        if (className.ToString().IndexOf("CASCADIA", StringComparison.OrdinalIgnoreCase) < 0) return false;
        processId = (int)pid;
        return true;
    }

    private static AutomationElement FindTextElement(AutomationElement root, string needle, out string allText)
    {
        allText = null;
        AutomationElement bestElement = null;
        int bestLength = -1;
        AutomationElementCollection elements = root.FindAll(TreeScope.Subtree, Condition.TrueCondition);
        foreach (AutomationElement element in elements)
        {
            try
            {
                object candidate;
                if (!element.TryGetCurrentPattern(TextPattern.Pattern, out candidate)) continue;
                TextPattern pattern = (TextPattern)candidate;
                string text = pattern.DocumentRange.GetText(-1) ?? string.Empty;
                if (text.IndexOf(needle, StringComparison.Ordinal) < 0 || text.Length <= bestLength) continue;
                bestElement = element;
                bestLength = text.Length;
                allText = text;
            }
            catch (ElementNotAvailableException) { }
            catch (InvalidOperationException) { }
            catch (COMException) { }
        }
        return bestElement;
    }

    private static AicliLiveTerminalAutomationSnapshot CaptureCore(IntPtr hWnd, int processId, string needle)
    {
        try
        {
            AutomationElement root = AutomationElement.FromHandle(hWnd);
            if (root == null) return null;
            string allText;
            AutomationElement element = FindTextElement(root, needle, out allText);
            if (element == null) return null;

            object candidate;
            if (!element.TryGetCurrentPattern(TextPattern.Pattern, out candidate)) return null;
            TextPattern pattern = (TextPattern)candidate;
            StringBuilder visible = new StringBuilder();
            foreach (var range in pattern.GetVisibleRanges())
            {
                visible.Append(range.GetText(-1));
            }

            string title = string.Empty;
            try { title = root.Current.Name ?? string.Empty; } catch { }
            return new AicliLiveTerminalAutomationSnapshot
            {
                WindowHandle = hWnd.ToInt64(),
                ProcessId = processId,
                WindowTitle = title,
                AllText = allText ?? string.Empty,
                VisibleText = visible.ToString()
            };
        }
        catch (ElementNotAvailableException) { return null; }
        catch (InvalidOperationException) { return null; }
        catch (COMException) { return null; }
    }

    public static AicliLiveTerminalAutomationSnapshot Find(string needle)
    {
        if (string.IsNullOrEmpty(needle)) throw new ArgumentException("needle is required", "needle");
        AicliLiveTerminalAutomationSnapshot best = null;
        EnumWindows(delegate(IntPtr hWnd, IntPtr ignored)
        {
            int processId;
            if (!IsWindowsTerminalWindow(hWnd, out processId)) return true;
            AicliLiveTerminalAutomationSnapshot snapshot = CaptureCore(hWnd, processId, needle);
            if (snapshot != null && (best == null || snapshot.AllText.Length > best.AllText.Length)) best = snapshot;
            return true;
        }, IntPtr.Zero);
        return best;
    }

    public static AicliLiveTerminalAutomationSnapshot Capture(long windowHandle, string needle)
    {
        IntPtr hWnd = new IntPtr(windowHandle);
        int processId;
        if (!IsWindowsTerminalWindow(hWnd, out processId)) return null;
        return CaptureCore(hWnd, processId, needle);
    }
}
'@
}

Test-AicliTerminalE2EHelpers
Invoke-AicliTerminalE2E
