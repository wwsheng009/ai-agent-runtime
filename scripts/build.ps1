<#
.SYNOPSIS
    Unified build script: build any repository tool for Windows or Windows 7.

.DESCRIPTION
    Builds the Go command tools of this repository for either the mainline
    Windows target (current go.mod + current Go toolchain) or the Windows 7
    compatible target (Go 1.20.14 + the isolated go.win7.mod dependency graph
    + the win7compat build tag + CGO_ENABLED=0 + windows/amd64).

    Tools: aicli, aicli-console, runtime-server, ssh-client, sftp-client,
    ssh-keygen.  Select with -Tools (comma separated, default "all") and the
    target with -Target ("windows", "win7" or "both", default "both").

    Output naming (default OutputDir backend/dist):
      windows  -> <tool>.exe
      win7     -> <tool>-win7.exe
    A SHA-256 sidecar is written beside every binary (sha256sum format).

    runtime-server embeds the web UI: by default the production frontend under
    frontend/dist is staged into backend/internal/webui/dist (which is backed
    up and restored after the build); pass -EmbedPlaceholder to embed a
    placeholder instead, or -KeepEmbeddedWebUI to leave the current assets
    untouched.

.EXAMPLE
    pwsh -File ./scripts/build.ps1 -Target win7 -Tools aicli,aicli-console

.EXAMPLE
    pwsh -File ./scripts/build.ps1 -Target windows -Tools ssh-client,sftp-client,ssh-keygen -SkipTests

.EXAMPLE
    pwsh -File ./scripts/build.ps1 -Target both -Tools all -Version v1.2.3 -OutputDir dist/release

.NOTES
    Backward-compatible wrappers that forward to this script:
      scripts/build-aicli-win7.ps1            (-Tools aicli,aicli-console -Target win7)
      scripts/build-runtime-server-win7.ps1   (-Tools runtime-server     -Target win7)
      scripts/build-ssh-sftp-clients-win7.ps1 (-Tools ssh-client,sftp-client -Target win7)
#>
[CmdletBinding()]
param(
    [string]$Tools = "all",
    [ValidateSet("windows", "win7", "both")]
    [string]$Target = "both",
    [string]$Version = "",
    [string]$OutputDir = "",
    [string]$GoProxy = "",
    [string]$Goos = "",
    [string]$Goarch = "",
    [switch]$SkipTests,
    [switch]$SkipDependencyCheck,
    # runtime-server web UI handling
    [switch]$BuildFrontend,
    [switch]$SkipFrontendInstall,
    [switch]$EmbedPlaceholder,
    [switch]$KeepEmbeddedWebUI,
    [string]$ApiBaseUrl = ""
)Set-StrictMode -Version Latest
$ErrorActionPreference = "Stop"

if (Test-Path -LiteralPath "variable:PSNativeCommandUseErrorActionPreference") {
    $PSNativeCommandUseErrorActionPreference = $false
}

$scriptDirectory = if (Test-Path -LiteralPath "variable:PSScriptRoot") {
    [string]$PSScriptRoot
}
else {
    Split-Path -Parent $MyInvocation.MyCommand.Definition
}
$script:repoRoot = [System.IO.Path]::GetFullPath((Join-Path $scriptDirectory ".."))
$script:goCommand = "go"
$script:webUIBackup = $null
$script:webUIEntryAsset = $null

# Tool registry: name -> Go package, output file names, ldflags kind.
# Ldflags kinds:
#   plain        -> no version injection (-s -w only)
#   main-version -> -X main.version=<v>  (ssh-client, sftp-client, ssh-keygen)
#   main-full    -> -X main.version=<v> -X main.buildTime=<t> (aicli)
#   buildinfo    -> internal/buildinfo.version/buildTime (runtime-server)
$script:toolRegistry = @(
    [pscustomobject]@{ Name = "aicli";            Package = "./cmd/aicli";            WindowsName = "aicli.exe";            Win7Name = "aicli-win7.exe";            LdflagsKind = "main-full" },
    [pscustomobject]@{ Name = "aicli-console";    Package = "./cmd/aicli-console";    WindowsName = "aicli-console.exe";    Win7Name = "aicli-console-win7.exe";    LdflagsKind = "plain" },
    [pscustomobject]@{ Name = "runtime-server";   Package = "./cmd/runtime-server";   WindowsName = "runtime-server.exe";   Win7Name = "runtime-server-win7.exe";   LdflagsKind = "buildinfo" },
    [pscustomobject]@{ Name = "ssh-client";       Package = "./cmd/ssh-client";       WindowsName = "ssh-client.exe";       Win7Name = "ssh-client-win7.exe";       LdflagsKind = "main-version" },
    [pscustomobject]@{ Name = "sftp-client";      Package = "./cmd/sftp-client";      WindowsName = "sftp-client.exe";      Win7Name = "sftp-client-win7.exe";      LdflagsKind = "main-version" },
    [pscustomobject]@{ Name = "ssh-keygen";       Package = "./cmd/ssh-keygen";       WindowsName = "ssh-keygen.exe";       Win7Name = "ssh-keygen-win7.exe";       LdflagsKind = "main-version" }
)

function Resolve-RepoPath {
    param([Parameter(Mandatory = $true)][string]$Path)
    if ([System.IO.Path]::IsPathRooted($Path)) {
        return [System.IO.Path]::GetFullPath($Path)
    }
    return [System.IO.Path]::GetFullPath((Join-Path $script:repoRoot $Path))
}

function Assert-File {
    param(
        [Parameter(Mandatory = $true)][string]$Path,
        [Parameter(Mandatory = $true)][string]$Description
    )
    if (-not (Test-Path -LiteralPath $Path -PathType Leaf)) {
        throw "$Description was not found: $Path"
    }
}

function Get-NativeExitCode {
    $value = Get-Variable -Name LASTEXITCODE -ValueOnly -ErrorAction SilentlyContinue
    if ($null -eq $value) { return 0 }
    return [int]$value
}

function Invoke-Go {
    param(
        [Parameter(Mandatory = $true)][string[]]$Arguments,
        [Parameter(Mandatory = $true)][string]$Description
    )
    Write-Host ("==> go {0}" -f ($Arguments -join " "))
    & $script:goCommand @Arguments
    $exitCode = Get-NativeExitCode
    if ($exitCode -ne 0) {
        throw "$Description failed with exit code $exitCode."
    }
}

function Invoke-GoCapture {
    param(
        [Parameter(Mandatory = $true)][string[]]$Arguments,
        [Parameter(Mandatory = $true)][string]$Description
    )
    $output = @(& $script:goCommand @Arguments 2>&1)
    $exitCode = Get-NativeExitCode
    if ($exitCode -ne 0) {
        $details = ($output | ForEach-Object { [string]$_ }) -join [Environment]::NewLine
        if ([string]::IsNullOrWhiteSpace($details)) {
            $details = "(no output)"
        }
        throw "$Description failed with exit code $exitCode.`n$details"
    }
    return $output
}

function Test-PEExecutable {
    param([Parameter(Mandatory = $true)][string]$Path)
    $stream = $null
    try {
        $stream = [System.IO.File]::OpenRead($Path)
        if ($stream.Length -lt 2) { return $false }
        return ($stream.ReadByte() -eq 0x4d -and $stream.ReadByte() -eq 0x5a)
    }
    finally {
        if ($null -ne $stream) { $stream.Dispose() }
    }
}

function Get-Sha256Hex {
    param([Parameter(Mandatory = $true)][string]$Path)
    $stream = $null
    $sha256 = $null
    try {
        $stream = [System.IO.File]::OpenRead($Path)
        $sha256 = [System.Security.Cryptography.SHA256]::Create()
        $digest = $sha256.ComputeHash($stream)
        return [System.BitConverter]::ToString($digest).Replace("-", "").ToLowerInvariant()
    }
    finally {
        if ($null -ne $sha256) { $sha256.Dispose() }
        if ($null -ne $stream) { $stream.Dispose() }
    }
}

function Write-Checksum {
    param([Parameter(Mandatory = $true)][string]$Path)
    $hash = Get-Sha256Hex -Path $Path
    $checksumPath = $Path + ".sha256"
    $line = "{0}  {1}{2}" -f $hash, [System.IO.Path]::GetFileName($Path), [Environment]::NewLine
    $utf8 = New-Object System.Text.UTF8Encoding($false)
    [System.IO.File]::WriteAllText($checksumPath, $line, $utf8)
    Write-Host "  SHA-256: $hash"
    return $checksumPath
}

function Verify-Binary {
    param(
        [Parameter(Mandatory = $true)][string]$Path,
        [Parameter(Mandatory = $true)][string]$Label,
        [Parameter(Mandatory = $true)][bool]$RequireGo120
    )
    Assert-File -Path $Path -Description $Label
    $length = (Get-Item -LiteralPath $Path).Length
    if ($length -le 0) { throw "$Label is empty: $Path" }
    if (-not (Test-PEExecutable -Path $Path)) {
        throw "$Label is not a Windows PE executable (missing MZ header): $Path"
    }
    if ($RequireGo120) {
        $metadata = @(Invoke-GoCapture -Arguments @("version", "-m", $Path) -Description "Inspect $Label build metadata")
        $versionLine = ($metadata | ForEach-Object { [string]$_ } |
            Where-Object { $_ -match "(^|:\s)go1\.20\.14(?:\s|$)" } | Select-Object -First 1)
        if ([string]::IsNullOrWhiteSpace($versionLine)) {
            $firstLine = ($metadata | ForEach-Object { [string]$_ } |
                Where-Object { -not [string]::IsNullOrWhiteSpace($_) } | Select-Object -First 1)
            throw "$Label was not built by Go 1.20.14. First metadata line: '$firstLine'"
        }
        Write-Host "  ${Label}: $Path ($length bytes; Go 1.20.14; PE=MZ)"
    }
    else {
        Write-Host "  ${Label}: $Path ($length bytes; PE=MZ)"
    }
    [void](Write-Checksum -Path $Path)
}

function Restore-Environment {
    param([Parameter(Mandatory = $true)][hashtable]$Snapshot)
    foreach ($name in $Snapshot.Keys) {
        [System.Environment]::SetEnvironmentVariable($name, [string]$Snapshot[$name], [System.EnvironmentVariableTarget]::Process)
        if ($null -eq $Snapshot[$name]) {
            Remove-Item -LiteralPath ("Env:{0}" -f $name) -ErrorAction SilentlyContinue
        }
    }
}

# ---- Web UI embedding (runtime-server only) ----
function Get-AssetManifestHash {
    param([Parameter(Mandatory = $true)][string]$Root)
    $normalizedRoot = [System.IO.Path]::GetFullPath($Root)
    $rootPrefix = $normalizedRoot
    if (-not $rootPrefix.EndsWith([System.IO.Path]::DirectorySeparatorChar.ToString())) {
        $rootPrefix += [System.IO.Path]::DirectorySeparatorChar
    }
    $fileByPath = @{}
    foreach ($file in Get-ChildItem -LiteralPath $normalizedRoot -Recurse -File) {
        $relativePath = $file.FullName.Substring($rootPrefix.Length).Replace([char]92, [char]47)
        if ($relativePath -eq "build-info.json") { continue }
        $fileByPath[$relativePath] = $file.FullName
    }
    [string[]]$paths = @($fileByPath.Keys)
    [System.Array]::Sort($paths, [System.StringComparer]::Ordinal)
    $records = foreach ($relativePath in $paths) {
        $contentHash = (Get-FileHash -LiteralPath $fileByPath[$relativePath] -Algorithm SHA256).Hash.ToLowerInvariant()
        "$contentHash  $relativePath"
    }
    $manifestText = if ($records.Count -gt 0) {
        [string]::Join("`n", [string[]]$records) + "`n"
    } else { "" }
    $utf8 = New-Object System.Text.UTF8Encoding($false)
    $sha256 = [System.Security.Cryptography.SHA256]::Create()
    try {
        $digest = $sha256.ComputeHash($utf8.GetBytes($manifestText))
        return [System.BitConverter]::ToString($digest).Replace("-", "").ToLowerInvariant()
    }
    finally { $sha256.Dispose() }
}

function Get-FrontendEntryAsset {
    param([Parameter(Mandatory = $true)][string]$IndexPath)
    $html = [System.IO.File]::ReadAllText($IndexPath)
    $scriptTags = [regex]::Matches($html, '<script\b[^>]*>', [System.Text.RegularExpressions.RegexOptions]::IgnoreCase)
    foreach ($tag in $scriptTags) {
        if (-not [regex]::IsMatch($tag.Value, '\btype\s*=\s*["'']module["'']', 'IgnoreCase')) { continue }
        $source = [regex]::Match($tag.Value, '\bsrc\s*=\s*["''](?<src>[^"'']+)["'']', 'IgnoreCase')
        if ($source.Success) {
            $entryAsset = $source.Groups['src'].Value.Trim()
            if (-not $entryAsset.StartsWith('/') -and -not $entryAsset.Contains('://')) {
                $entryAsset = '/' + $entryAsset.TrimStart('.', '/')
            }
            return $entryAsset
        }
    }
    throw "Frontend index '$IndexPath' does not contain a module entry asset."
}

function Prepare-EmbeddedWebUI {
    param(
        [Parameter(Mandatory = $true)][string]$WebUIDir,
        [Parameter(Mandatory = $true)][string]$FrontendDist,
        [Parameter(Mandatory = $true)][string]$BuildTime
    )
    if ($KeepEmbeddedWebUI) {
        Write-Host "  Keeping existing embedded web UI assets (-KeepEmbeddedWebUI)."
        return
    }
    if ($EmbedPlaceholder) {
        Write-Host "  Embedding placeholder web UI only (-EmbedPlaceholder)."
    }
    else {
        $frontendIndex = Join-Path $FrontendDist "index.html"
        if (-not (Test-Path -LiteralPath $frontendIndex -PathType Leaf)) {
            throw "Frontend dist is missing '$frontendIndex' (run 'pnpm build' in frontend/ or pass -EmbedPlaceholder)."
        }
        Write-Host "  Staging production frontend: $FrontendDist"
    }
    $parent = Split-Path -Parent $WebUIDir
    if (-not (Test-Path -LiteralPath $parent -PathType Container)) {
        throw "The web UI parent directory was not found: $parent"
    }
    $hadOriginalDirectory = Test-Path -LiteralPath $WebUIDir -PathType Container
    $backup = $null
    $wasPlaceholderOnly = $false
    if ($hadOriginalDirectory) {
        $entries = @(Get-ChildItem -LiteralPath $WebUIDir -Force)
        $wasPlaceholderOnly = ($entries.Count -eq 1 -and $entries[0].Name -eq "placeholder.txt" -and $entries[0].PSIsContainer -eq $false)
        if ($wasPlaceholderOnly) {
            Write-Host "  Current embedded web UI contains only the placeholder (will be restored after build)."
        }
        else {
            $backup = Join-Path $parent ("dist.win7-backup-{0}-{1}" -f $PID, ([Guid]::NewGuid().ToString("N")))
            Move-Item -LiteralPath $WebUIDir -Destination $backup -Force
            Write-Host "  Backed up existing web UI assets for restore after build."
        }
    }
    else {
        Write-Host "  Creating embedded web UI directory for build."
    }
    New-Item -ItemType Directory -Path $WebUIDir -Force | Out-Null
    if ($EmbedPlaceholder) {
        Get-ChildItem -LiteralPath $WebUIDir -Force | Remove-Item -Recurse -Force
        $placeholder = Join-Path $WebUIDir "placeholder.txt"
        $utf8 = New-Object System.Text.UTF8Encoding($false)
        [System.IO.File]::WriteAllText($placeholder, "Win7 build: frontend disabled.`n", $utf8)
    }
    else {
        Get-ChildItem -LiteralPath $WebUIDir -Force |
            Where-Object { $_.Name -ne "placeholder.txt" } |
            Remove-Item -Recurse -Force
        Copy-Item -Path (Join-Path $FrontendDist "*") -Destination $WebUIDir -Recurse -Force
        $embeddedIndex = Join-Path $WebUIDir "index.html"
        if (-not (Test-Path -LiteralPath $embeddedIndex -PathType Leaf)) {
            throw "Failed to stage the frontend in '$WebUIDir'."
        }
        $frontendManifestHash = Get-AssetManifestHash -Root $WebUIDir
        $frontendEntryAsset = Get-FrontendEntryAsset -IndexPath $embeddedIndex
        $frontendBuildInfo = [ordered]@{
            asset_manifest_hash = $frontendManifestHash
            build_time = $BuildTime
            entry_asset = $frontendEntryAsset
        }
        $frontendBuildInfoPath = Join-Path $WebUIDir "build-info.json"
        $frontendBuildInfoJson = $frontendBuildInfo | ConvertTo-Json -Depth 3
        $utf8 = New-Object System.Text.UTF8Encoding($false)
        [System.IO.File]::WriteAllText($frontendBuildInfoPath, $frontendBuildInfoJson + "`n", $utf8)
        $verifiedManifestHash = Get-AssetManifestHash -Root $WebUIDir
        if ($verifiedManifestHash -ne $frontendManifestHash) {
            throw "Frontend manifest hash changed after writing build-info.json."
        }
        Write-Host "  Frontend manifest: $frontendManifestHash"
        Write-Host "  Frontend entry:    $frontendEntryAsset"
        $script:webUIEntryAsset = $frontendEntryAsset
    }
    $script:webUIBackup = [pscustomobject]@{
        Original = $WebUIDir
        Backup = $backup
        HadOriginalDirectory = $hadOriginalDirectory
        WasPlaceholderOnly = $wasPlaceholderOnly
    }
}

function Restore-EmbeddedWebUI {
    if ($null -eq $script:webUIBackup) { return }
    $state = $script:webUIBackup
    try {
        if (Test-Path -LiteralPath $state.Original -PathType Container) {
            Remove-Item -LiteralPath $state.Original -Recurse -Force
        }
        if ($state.HadOriginalDirectory -and $null -ne $state.Backup) {
            if (-not (Test-Path -LiteralPath $state.Backup -PathType Container)) {
                throw "The temporary web UI backup disappeared: $($state.Backup)"
            }
            Move-Item -LiteralPath $state.Backup -Destination $state.Original -Force
        }
        elseif ($state.HadOriginalDirectory -and $state.WasPlaceholderOnly) {
            New-Item -ItemType Directory -Path $state.Original -Force | Out-Null
            $placeholder = Join-Path $state.Original "placeholder.txt"
            $utf8 = New-Object System.Text.UTF8Encoding($false)
            [System.IO.File]::WriteAllText($placeholder, "Win7 build: frontend disabled.`n", $utf8)
        }
    }
    finally {
        $script:webUIBackup = $null
    }
}

# ---- Frontend build (runtime-server only, optional) ----
function Invoke-FrontendBuild {
    param(
        [Parameter(Mandatory = $true)][string]$FrontendDir,
        [Parameter(Mandatory = $true)][string]$ApiBaseUrl
    )
    foreach ($cmd in @("node", "pnpm")) {
        if (-not (Get-Command $cmd -ErrorAction SilentlyContinue)) {
            throw "Required command '$cmd' was not found in PATH."
        }
    }
    $oldApiBaseUrl = $env:VITE_API_BASE_URL
    Push-Location -LiteralPath $FrontendDir
    try {
        $env:VITE_API_BASE_URL = $ApiBaseUrl.Trim()
        if (-not $SkipFrontendInstall) {
            & pnpm install --frozen-lockfile
            if ($LASTEXITCODE -ne 0) { throw "Install frontend dependencies failed with exit code $LASTEXITCODE." }
        }
        & pnpm build
        if ($LASTEXITCODE -ne 0) { throw "Build frontend failed with exit code $LASTEXITCODE." }
    }
    finally {
        $env:VITE_API_BASE_URL = $oldApiBaseUrl
        Pop-Location
    }
}

# ---- Select tools and targets ----
$selectedTools = @()
if ($Tools -eq "all" -or [string]::IsNullOrWhiteSpace($Tools)) {
    $selectedTools = @($script:toolRegistry)
}
else {
    foreach ($name in ($Tools -split "," | ForEach-Object { $_.Trim() })) {
        if ([string]::IsNullOrWhiteSpace($name)) { continue }
        $tool = $script:toolRegistry | Where-Object { $_.Name -eq $name } | Select-Object -First 1
        if ($null -eq $tool) {
            $known = ($script:toolRegistry | ForEach-Object { $_.Name }) -join ", "
            throw "Unknown tool '$name'. Known tools: $known"
        }
        $selectedTools += $tool
    }
}

$selectedTargets = @()
if ($Target -eq "both") { $selectedTargets = @("windows", "win7") }
else { $selectedTargets = @($Target) }

# ---- Resolve version / build time ----
if ([string]::IsNullOrWhiteSpace($Version)) {
    foreach ($candidate in @($env:RUNTIME_SERVER_VERSION, $env:AICLI_VERSION, $env:WIN7_VERSION, $env:GITHUB_REF_NAME)) {
        if (-not [string]::IsNullOrWhiteSpace($candidate)) {
            $Version = [string]$candidate
            break
        }
    }
}
if ([string]::IsNullOrWhiteSpace($Version)) {
    $Version = if ($selectedTargets -contains "win7" -and $selectedTargets.Count -eq 1) { "win7-dev" } else { "dev" }
}
$Version = $Version.Trim()
if ($Version -notmatch "^[0-9A-Za-z][0-9A-Za-z._+\-]*$") {
    throw "Version '$Version' contains characters that cannot be passed to Go linker metadata."
}
$buildTime = [DateTime]::UtcNow.ToString(
    "yyyy-MM-dd'T'HH:mm:ss'Z'",
    [Globalization.CultureInfo]::InvariantCulture
)

# ---- Resolve output directory ----
$backendDir = Join-Path $script:repoRoot "backend"
if ([string]::IsNullOrWhiteSpace($OutputDir)) {
    $outputRoot = [System.IO.Path]::GetFullPath((Join-Path $backendDir "dist"))
}
else {
    $outputRoot = Resolve-RepoPath -Path $OutputDir
}
New-Item -ItemType Directory -Path $outputRoot -Force | Out-Null

Write-Host "Building tools: $($selectedTools.Name -join ', ')  targets: $($selectedTargets -join ', ')  version: $Version"
Write-Host "  repository: $script:repoRoot"
Write-Host "  output:    $outputRoot"

# ---- Environment snapshot ----
$environmentSnapshot = @{}
foreach ($name in @("GOTOOLCHAIN", "GOOS", "GOARCH", "CGO_ENABLED", "GOFLAGS", "GOPROXY")) {
    $environmentSnapshot[$name] = [System.Environment]::GetEnvironmentVariable($name, [System.EnvironmentVariableTarget]::Process)
}

$webUIDir = Join-Path $backendDir "internal/webui/dist"
$frontendDist = Join-Path $script:repoRoot "frontend/dist"
$failureMessage = $null

try {
    if (-not (Test-Path -LiteralPath $backendDir -PathType Container)) {
        throw "backend directory was not found: $backendDir"
    }
    $goInfo = Get-Command $script:goCommand -ErrorAction SilentlyContinue
    if ($null -eq $goInfo) {
        throw "The Go command was not found in PATH. Install Go 1.21+ (for automatic toolchain download) or Go 1.20.14."
    }
    if (-not [string]::IsNullOrWhiteSpace([string]$goInfo.Source)) {
        $script:goCommand = [string]$goInfo.Source
    }

    # ---- Frontend for runtime-server ----
    if (($selectedTools.Name -contains "runtime-server") -and $BuildFrontend) {
        Invoke-FrontendBuild -FrontendDir (Join-Path $script:repoRoot "frontend") -ApiBaseUrl $ApiBaseUrl
    }

    foreach ($target in $selectedTargets) {
        $isWin7 = ($target -eq "win7")
        Write-Host ""
        Write-Host "==== Target: $target ($(if ($isWin7) { 'Go 1.20.14 / go.win7.mod / win7compat / CGO=0' } else { 'mainline go.mod / current toolchain / CGO=0' })) ===="

        [System.Environment]::SetEnvironmentVariable("CGO_ENABLED", "0", "Process")
        if ($isWin7) {
            Assert-File -Path (Join-Path $backendDir "go.win7.mod") -Description "Win7 module file"
            Assert-File -Path (Join-Path $backendDir "go.win7.sum") -Description "Win7 module checksum file"
            [System.Environment]::SetEnvironmentVariable("GOTOOLCHAIN", "go1.20.14", "Process")
            [System.Environment]::SetEnvironmentVariable("GOFLAGS", "-modfile=go.win7.mod", "Process")
        }
        else {
            [System.Environment]::SetEnvironmentVariable("GOTOOLCHAIN", $null, "Process")
            [System.Environment]::SetEnvironmentVariable("GOFLAGS", $null, "Process")
        }
        if (-not [string]::IsNullOrWhiteSpace($GoProxy)) {
            [System.Environment]::SetEnvironmentVariable("GOPROXY", $GoProxy.Trim(), "Process")
        }
        # Host-side commands (version check, tests) must not inherit target values.
        [System.Environment]::SetEnvironmentVariable("GOOS", $null, "Process")
        [System.Environment]::SetEnvironmentVariable("GOARCH", $null, "Process")

        $goVersion = @(Invoke-GoCapture -Arguments @("version") -Description "Verify Go toolchain")
        $goVersionText = ($goVersion | ForEach-Object { [string]$_ }) -join " "
        $goVersionLine = ($goVersion | ForEach-Object { [string]$_ } |
            Where-Object { $_ -match "^go version\s+" } | Select-Object -First 1)
        if ($isWin7) {
            if ([string]::IsNullOrWhiteSpace($goVersionLine) -or $goVersionLine -notmatch "^go version\s+go1\.20\.14(?:\s|$)") {
                throw "Go 1.20.14 is required, but the selected toolchain reported: $goVersionText"
            }
        }
        Write-Host "  $goVersionText"

        # ---- Dependency check ----
        if (-not $SkipDependencyCheck) {
            Push-Location -LiteralPath $backendDir
            try {
                Invoke-Go -Arguments @("list", "-m", "-mod=readonly", "all") -Description "Resolve $target dependency graph"
                if ($isWin7) {
                    Invoke-Go -Arguments @("mod", "verify") -Description "Verify Win7 module checksums"
                }
            }
            finally { Pop-Location }
        }

        # ---- Tests ----
        if (-not $SkipTests) {
            Push-Location -LiteralPath $backendDir
            try {
                if ($isWin7) {
                    # Common Win7 compatibility suite shared by aicli/runtime-server.
                    $commonSuite = @(
                        "./internal/aiclipaths", "./internal/agentconfig", "./internal/config",
                        "./internal/chat", "./cmd/runtime-server"
                    )
                    $toolNames = @($selectedTools | ForEach-Object { $_.Name })
                    if ($toolNames -contains "aicli" -or $toolNames -contains "runtime-server") {
                        Invoke-Go -Arguments @(
                            "test", "-tags", "win7compat", "-mod=readonly", $commonSuite, "-count=1"
                        ) -Description "Run Win7 configuration and runtime tests"
                    }
                    if ($toolNames -contains "aicli") {
                        Invoke-Go -Arguments @(
                            "test", "-tags", "win7compat", "-mod=readonly", "./internal/agent",
                            "-run", "TestAgentWithoutCancel|TestAgentWithTimeoutCause|TestComputeAvailableToolsDoesNotExposePolicyDeniedSpawnSubagents",
                            "-count=1"
                        ) -Description "Run Go 1.20 agent compatibility tests"
                        Invoke-Go -Arguments @(
                            "test", "-tags", "win7compat", "-mod=readonly", "./cmd/aicli/commands",
                            "-run", "Test(GetMCPConfigPath|ResolveGlobalRuntimeConfigPath|RunInitCommand|InitCommandHelp)",
                            "-count=1"
                        ) -Description "Run aicli configuration tests"
                        $testExe = Join-Path ([System.IO.Path]::GetTempPath()) (
                            "aicli-commands-win7-{0}.test.exe" -f ([Guid]::NewGuid().ToString("N"))
                        )
                        try {
                            [System.Environment]::SetEnvironmentVariable("GOOS", "windows", "Process")
                            [System.Environment]::SetEnvironmentVariable("GOARCH", "amd64", "Process")
                            Invoke-Go -Arguments @(
                                "test", "-tags", "win7compat", "-mod=readonly", "./cmd/aicli/commands",
                                "-run", "Test(ReadChatSessionLine|ParseChatCommandOptionsConsoleInputMode|DecodeSystemConsoleLine|ReadSystemConsoleUTF16Line|LegacyConsoleDispatchAcceptsCommittedIMEProcessCharacter)",
                                "-c", "-o", $testExe
                            ) -Description "Cross-compile Windows console compatibility tests"
                            if (-not (Test-PEExecutable -Path $testExe)) {
                                throw "Cross-compiled command test is not a Windows PE executable: $testExe"
                            }
                        }
                        finally {
                            if (Test-Path -LiteralPath $testExe) {
                                Remove-Item -LiteralPath $testExe -Force -ErrorAction SilentlyContinue
                            }
                        }
                    }
                    if ($toolNames -contains "aicli-console") {
                        Invoke-Go -Arguments @(
                            "test", "-tags", "win7compat", "-mod=readonly", "./cmd/aicli-console", "-count=1"
                        ) -Description "Run native console launcher tests"
                    }
                    if (@($toolNames | Where-Object { $_ -like "ssh-*" -or $_ -eq "sftp-client" }).Count -gt 0) {
                        Invoke-Go -Arguments @(
                            "test", "-tags", "win7compat", "-mod=readonly", "-count=1", "./internal/winconsole/..."
                        ) -Description "Run winconsole package tests"
                    }
                    if ($toolNames -contains "ssh-keygen") {
                        Invoke-Go -Arguments @(
                            "test", "-tags", "win7compat", "-mod=readonly", "-count=1", "./cmd/ssh-keygen"
                        ) -Description "Run ssh-keygen tests"
                    }
                }
                else {
                    # Mainline tests: run each selected tool's own package plus shared deps.
                    $testPackages = New-Object System.Collections.Generic.List[string]
                    foreach ($tool in $selectedTools) {
                        switch ($tool.Name) {
                            "aicli"          { $testPackages.Add("./cmd/aicli/...") }
                            "aicli-console"  { $testPackages.Add("./cmd/aicli-console") }
                            "runtime-server" { $testPackages.Add("./cmd/runtime-server"); $testPackages.Add("./internal/webui") }
                            "ssh-client"     { $testPackages.Add("./cmd/ssh-client"); $testPackages.Add("./internal/winconsole/...") }
                            "sftp-client"    { $testPackages.Add("./cmd/sftp-client"); $testPackages.Add("./internal/winconsole/...") }
                            "ssh-keygen"     { $testPackages.Add("./cmd/ssh-keygen") }
                        }
                    }
                    if ($testPackages.Count -gt 0) {
                        $mainlineTestArgs = @("test", "-mod=readonly", "-count=1") + @($testPackages)
                        Invoke-Go -Arguments $mainlineTestArgs -Description "Run mainline tests"
                    }
                }
            }
            finally { Pop-Location }
        }

        # ---- Build ----
        $goos = if ([string]::IsNullOrWhiteSpace($Goos)) { "windows" } else { $Goos.Trim() }
        $goarch = if ([string]::IsNullOrWhiteSpace($Goarch)) { "amd64" } else { $Goarch.Trim() }
        [System.Environment]::SetEnvironmentVariable("GOOS", $goos, "Process")
        [System.Environment]::SetEnvironmentVariable("GOARCH", $goarch, "Process")

        foreach ($tool in $selectedTools) {
            $fileName = if ($isWin7) { $tool.Win7Name } else { $tool.WindowsName }
            $targetPath = Join-Path $outputRoot $fileName
            $checksumPath = $targetPath + ".sha256"
            if (Test-Path -LiteralPath $targetPath) { Remove-Item -LiteralPath $targetPath -Force }
            if (Test-Path -LiteralPath $checksumPath) { Remove-Item -LiteralPath $checksumPath -Force }

            $ldflags = "-s -w"
            switch ($tool.LdflagsKind) {
                "main-version" { $ldflags += " -X main.version=$Version" }
                "main-full"    { $ldflags += " -X main.version=$Version -X main.buildTime=$buildTime" }
                "buildinfo"    { $ldflags += " -X github.com/wwsheng009/ai-agent-runtime/internal/buildinfo.version=$Version -X github.com/wwsheng009/ai-agent-runtime/internal/buildinfo.buildTime=$buildTime" }
            }

            # runtime-server embeds the web UI: stage it before building, restore after.
            if ($tool.Name -eq "runtime-server") {
                if ($isWin7) {
                    Prepare-EmbeddedWebUI -WebUIDir $webUIDir -FrontendDist $frontendDist -BuildTime $buildTime
                }
                else {
                    $mainlineIndex = Join-Path $frontendDist "index.html"
                    if (-not (Test-Path -LiteralPath $mainlineIndex -PathType Leaf) -and -not $EmbedPlaceholder -and -not $KeepEmbeddedWebUI) {
                        throw "Frontend dist is missing '$mainlineIndex' (run 'pnpm build' in frontend/ or pass -EmbedPlaceholder)."
                    }
                    Prepare-EmbeddedWebUI -WebUIDir $webUIDir -FrontendDist $frontendDist -BuildTime $buildTime
                }
            }

            Push-Location -LiteralPath $backendDir
            try {
                $buildArguments = @(
                    "build", "-mod=readonly", "-trimpath", "-ldflags", $ldflags, "-o", $targetPath, $tool.Package
                )
                if ($isWin7) {
                    $buildArguments = @(
                        "build", "-tags", "win7compat", "-mod=readonly", "-trimpath", "-ldflags", $ldflags, "-o", $targetPath, $tool.Package
                    )
                }
                Invoke-Go -Arguments $buildArguments -Description "Build $($tool.Name) for $target"
            }
            finally { Pop-Location }

            Verify-Binary -Path $targetPath -Label "$($tool.Name) ($target)" -RequireGo120 $isWin7
        }
    }
    Write-Host ""
    Write-Host "Build completed successfully: $($selectedTools.Name -join ', ') for $($selectedTargets -join ', ')"
}
catch {
    $failureMessage = $_.Exception.ToString()
}
finally {
    try { Restore-EmbeddedWebUI } catch { Write-Host "WARNING: failed to restore embedded web UI: $($_.Exception.Message)" }
    Restore-Environment -Snapshot $environmentSnapshot
}

if (-not [string]::IsNullOrWhiteSpace($failureMessage)) {
    [Console]::Error.WriteLine("build.ps1 failed:")
    [Console]::Error.WriteLine($failureMessage)
    exit 1
}