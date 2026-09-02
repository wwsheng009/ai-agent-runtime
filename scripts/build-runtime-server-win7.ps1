<#
.SYNOPSIS
    Build the Windows 7 compatible runtime-server binary.

.DESCRIPTION
    Builds ./cmd/runtime-server with Go 1.20.14, the isolated go.win7.mod
    dependency graph, the win7compat build tag, windows/amd64, and
    CGO_ENABLED=0.  These settings are intentionally kept in this dedicated
    script instead of relying on the caller's normal release environment.

    The script resolves the repository from its own location, so it is safe to
    invoke from any current directory.  By default it temporarily replaces a
    generated frontend under backend/internal/webui/dist with the tracked
    placeholder while compiling.  This keeps the Win7 server/API build from
    accidentally embedding a modern frontend left by a previous local build;
    the original directory is restored even when compilation fails.

.EXAMPLE
    pwsh -File ./scripts/build-runtime-server-win7.ps1

.EXAMPLE
    pwsh -File ./scripts/build-runtime-server-win7.ps1 -Version win7-v1.2.3 `
        -OutputDir 'dist/win7' -SkipTests

.EXAMPLE
    pwsh -File ./scripts/build-runtime-server-win7.ps1 -SkipTests `
        -KeepEmbeddedWebUI

.NOTES
    The produced file is runtime-server-win7.exe.  A SHA-256 sidecar is
    written beside it in the same format used by the release workflow.
#>
[CmdletBinding()]
param(
    [string]$Version = "",
    [string]$OutputDir = "",
    [string]$GoProxy = "",
    [string]$ApiBaseUrl = "",
    [switch]$SkipTests,
    [switch]$SkipDependencyCheck,
    [switch]$BuildFrontend,
    [switch]$SkipFrontendInstall,
    [switch]$EmbedPlaceholder,
    [switch]$KeepEmbeddedWebUI
)

Set-StrictMode -Version Latest
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
    if ($null -eq $value) {
        return 0
    }
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
        if ($stream.Length -lt 2) {
            return $false
        }
        return ($stream.ReadByte() -eq 0x4d -and $stream.ReadByte() -eq 0x5a)
    }
    finally {
        if ($null -ne $stream) {
            $stream.Dispose()
        }
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
        if ($null -ne $sha256) {
            $sha256.Dispose()
        }
        if ($null -ne $stream) {
            $stream.Dispose()
        }
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
        [Parameter(Mandatory = $true)][string]$Label
    )

    Assert-File -Path $Path -Description $Label
    $length = (Get-Item -LiteralPath $Path).Length
    if ($length -le 0) {
        throw "$Label is empty: $Path"
    }
    if (-not (Test-PEExecutable -Path $Path)) {
        throw "$Label is not a Windows PE executable (missing MZ header): $Path"
    }

    $metadata = @(Invoke-GoCapture -Arguments @("version", "-m", $Path) -Description "Inspect $Label build metadata")
    $versionLine = ($metadata | ForEach-Object { [string]$_ } |
        Where-Object { $_ -match "(^|:\s)go1\.20\.14(?:\s|$)" } |
        Select-Object -First 1)
    if ([string]::IsNullOrWhiteSpace($versionLine)) {
        $firstLine = ($metadata | ForEach-Object { [string]$_ } |
            Where-Object { -not [string]::IsNullOrWhiteSpace($_) } |
            Select-Object -First 1)
        throw "$Label was not built by Go 1.20.14. First metadata line: '$firstLine'"
    }
    Write-Host "  ${Label}: $Path ($length bytes; Go 1.20.14; PE=MZ)"
    [void](Write-Checksum -Path $Path)
}

function Restore-Environment {
    param([Parameter(Mandatory = $true)][hashtable]$Snapshot)

    foreach ($name in $Snapshot.Keys) {
        [System.Environment]::SetEnvironmentVariable(
            $name,
            [string]$Snapshot[$name],
            [System.EnvironmentVariableTarget]::Process
        )
        if ($null -eq $Snapshot[$name]) {
            Remove-Item -LiteralPath ("Env:{0}" -f $name) -ErrorAction SilentlyContinue
        }
    }
}

function Test-BinaryContainsText {
    param(
        [Parameter(Mandatory = $true)][string]$Path,
        [Parameter(Mandatory = $true)][string]$Text
    )
    if ([string]::IsNullOrEmpty($Text)) {
        return $false
    }
    $utf8 = New-Object System.Text.UTF8Encoding($false)
    $content = $utf8.GetString([System.IO.File]::ReadAllBytes($Path))
    return $content.IndexOf($Text, [System.StringComparison]::Ordinal) -ge 0
}

function Assert-Command {
    param([Parameter(Mandatory = $true)][string]$Name)
    if (-not (Get-Command $Name -ErrorAction SilentlyContinue)) {
        throw "Required command '$Name' was not found in PATH."
    }
}

function Assert-LastExitCode {
    param([Parameter(Mandatory = $true)][string]$Step)
    if ($LASTEXITCODE -ne 0) {
        throw "$Step failed with exit code $LASTEXITCODE."
    }
}

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
        if ($relativePath -eq "build-info.json") {
            continue
        }
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
    } else {
        ""
    }
    $utf8 = New-Object System.Text.UTF8Encoding($false)
    $sha256 = [System.Security.Cryptography.SHA256]::Create()
    try {
        $digest = $sha256.ComputeHash($utf8.GetBytes($manifestText))
    }
    finally {
        $sha256.Dispose()
    }
    return [System.BitConverter]::ToString($digest).Replace("-", "").ToLowerInvariant()
}

function Get-FrontendEntryAsset {
    param([Parameter(Mandatory = $true)][string]$IndexPath)
    $html = [System.IO.File]::ReadAllText($IndexPath)
    $scriptTags = [regex]::Matches(
        $html,
        '<script\b[^>]*>',
        [System.Text.RegularExpressions.RegexOptions]::IgnoreCase
    )
    foreach ($tag in $scriptTags) {
        if (-not [regex]::IsMatch($tag.Value, '\btype\s*=\s*["'']module["'']', 'IgnoreCase')) {
            continue
        }
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

function Invoke-FrontendBuild {
    param(
        [Parameter(Mandatory = $true)][string]$FrontendDir,
        [Parameter(Mandatory = $true)][string]$ApiBaseUrl
    )
    Assert-Command "node"
    Assert-Command "pnpm"
    $oldApiBaseUrl = $env:VITE_API_BASE_URL
    Push-Location -LiteralPath $FrontendDir
    try {
        $env:VITE_API_BASE_URL = $ApiBaseUrl.Trim()
        if (-not $SkipFrontendInstall) {
            & pnpm install --frozen-lockfile
            Assert-LastExitCode "Install frontend dependencies"
        }
        & pnpm build
        Assert-LastExitCode "Build frontend"
    }
    finally {
        $env:VITE_API_BASE_URL = $oldApiBaseUrl
        Pop-Location
    }
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
        # Default: package the production frontend.
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
        # Replace with placeholder-only content.
        Get-ChildItem -LiteralPath $WebUIDir -Force | Remove-Item -Recurse -Force
        $placeholder = Join-Path $WebUIDir "placeholder.txt"
        $utf8 = New-Object System.Text.UTF8Encoding($false)
        [System.IO.File]::WriteAllText($placeholder, "Win7 build: frontend disabled.`n", $utf8)
    }
    else {
        # Stage the production frontend.
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
    if ($null -eq $script:webUIBackup) {
        return
    }

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
            # Restore placeholder-only state, preserving the tracked placeholder.txt.
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

$backendDir = Join-Path $script:repoRoot "backend"
$webUIDir = Join-Path $backendDir "internal/webui/dist"
$frontendDist = Join-Path $script:repoRoot "frontend/dist"
$outputRoot = $null
$failureMessage = $null
$environmentSnapshot = @{}
$environmentNames = @("GOTOOLCHAIN", "GOOS", "GOARCH", "CGO_ENABLED", "GOFLAGS", "GOPROXY")
foreach ($name in $environmentNames) {
    $environmentSnapshot[$name] = [System.Environment]::GetEnvironmentVariable(
        $name,
        [System.EnvironmentVariableTarget]::Process
    )
}

try {
    if (-not (Test-Path -LiteralPath $backendDir -PathType Container)) {
        throw "backend directory was not found: $backendDir"
    }
    Assert-File -Path (Join-Path $backendDir "go.win7.mod") -Description "Win7 module file"
    Assert-File -Path (Join-Path $backendDir "go.win7.sum") -Description "Win7 module checksum file"
    Assert-File -Path (Join-Path $backendDir "configs/runtime.win7.yaml") -Description "Win7 runtime configuration"

    $goInfo = Get-Command $script:goCommand -ErrorAction SilentlyContinue
    if ($null -eq $goInfo) {
        throw "The Go command was not found in PATH. Install Go 1.21+ (for automatic toolchain download) or Go 1.20.14."
    }
    if (-not [string]::IsNullOrWhiteSpace([string]$goInfo.Source)) {
        $script:goCommand = [string]$goInfo.Source
    }

    if ([string]::IsNullOrWhiteSpace($Version)) {
        foreach ($candidate in @($env:RUNTIME_SERVER_VERSION, $env:AICLI_VERSION, $env:WIN7_VERSION, $env:GITHUB_REF_NAME)) {
            if (-not [string]::IsNullOrWhiteSpace($candidate)) {
                $Version = [string]$candidate
                break
            }
        }
    }
    if ([string]::IsNullOrWhiteSpace($Version)) {
        $Version = "win7-dev"
    }
    $Version = $Version.Trim()
    if ($Version -notmatch "^[0-9A-Za-z][0-9A-Za-z._+\-]*$") {
        throw "Version '$Version' contains characters that cannot be passed to Go linker metadata."
    }

    if ([string]::IsNullOrWhiteSpace($OutputDir)) {
        $outputRoot = [System.IO.Path]::GetFullPath((Join-Path $backendDir "dist"))
    }
    else {
        $outputRoot = Resolve-RepoPath -Path $OutputDir
    }
    New-Item -ItemType Directory -Path $outputRoot -Force | Out-Null

    $buildTime = [DateTime]::UtcNow.ToString(
        "yyyy-MM-dd'T'HH:mm:ss'Z'",
        [Globalization.CultureInfo]::InvariantCulture
    )
    Write-Host "Building Windows 7 compatible runtime-server (version $Version)"
    Write-Host "  repository: $script:repoRoot"
    Write-Host "  output:    $outputRoot"
    Write-Host "  toolchain: Go 1.20.14 / windows-amd64 / CGO=0"

    [System.Environment]::SetEnvironmentVariable("GOTOOLCHAIN", "go1.20.14", "Process")
    [System.Environment]::SetEnvironmentVariable("CGO_ENABLED", "0", "Process")
    [System.Environment]::SetEnvironmentVariable("GOFLAGS", "-modfile=go.win7.mod", "Process")
    # Keep host-side tests executable when this script is invoked under pwsh on
    # Linux/macOS.  The Windows target is applied only immediately before go
    # build below; otherwise go test would create a PE and try to run it.
    [System.Environment]::SetEnvironmentVariable("GOOS", $null, "Process")
    [System.Environment]::SetEnvironmentVariable("GOARCH", $null, "Process")
    if (-not [string]::IsNullOrWhiteSpace($GoProxy)) {
        [System.Environment]::SetEnvironmentVariable("GOPROXY", $GoProxy.Trim(), "Process")
    }

    $goVersion = @(Invoke-GoCapture -Arguments @("version") -Description "Verify Go toolchain")
    $goVersionText = ($goVersion | ForEach-Object { [string]$_ }) -join " "
    $goVersionLine = ($goVersion | ForEach-Object { [string]$_ } |
        Where-Object { $_ -match "^go version\s+" } |
        Select-Object -First 1)
    if ([string]::IsNullOrWhiteSpace($goVersionLine) -or
        $goVersionLine -notmatch "^go version\s+go1\.20\.14(?:\s|$)") {
        throw "Go 1.20.14 is required, but the selected toolchain reported: $goVersionText"
    }
    Write-Host "  $goVersionText"

    if (-not $SkipDependencyCheck) {
        Push-Location -LiteralPath $backendDir
        try {
            Invoke-Go -Arguments @("list", "-m", "-mod=readonly", "all") -Description "Resolve Win7 dependency graph"
            Invoke-Go -Arguments @("mod", "verify") -Description "Verify Win7 module checksums"
        }
        finally {
            Pop-Location
        }
    }

    if (-not $SkipTests) {
        Push-Location -LiteralPath $backendDir
        try {
            Invoke-Go -Arguments @(
                "test", "-tags", "win7compat", "-mod=readonly",
                "./internal/aiclipaths", "./internal/agentconfig", "./internal/config",
                "./internal/chat", "./cmd/runtime-server", "-count=1"
            ) -Description "Run Win7 runtime and configuration tests"
        }
        finally {
            Pop-Location
        }
    }

    # Build the frontend if requested (default: reuse existing frontend/dist).
    if ($BuildFrontend) {
        Write-Host "==> Building production frontend for Win7 embedding"
        Invoke-FrontendBuild -FrontendDir (Join-Path $script:repoRoot "frontend") -ApiBaseUrl $ApiBaseUrl
    }
    else {
        Write-Host "  Reusing existing frontend/dist ($(if(Test-Path (Join-Path $frontendDist 'index.html')){'found'}else{'MISSING!'}))"
    }

    Prepare-EmbeddedWebUI -WebUIDir $webUIDir -FrontendDist $frontendDist -BuildTime $buildTime
    try {
        $binaryPath = Join-Path $outputRoot "runtime-server-win7.exe"
        $checksumPath = $binaryPath + ".sha256"
        if (Test-Path -LiteralPath $binaryPath) {
            Remove-Item -LiteralPath $binaryPath -Force
        }
        if (Test-Path -LiteralPath $checksumPath) {
            Remove-Item -LiteralPath $checksumPath -Force
        }

        Push-Location -LiteralPath $backendDir
        try {
            [System.Environment]::SetEnvironmentVariable("GOOS", "windows", "Process")
            [System.Environment]::SetEnvironmentVariable("GOARCH", "amd64", "Process")
            $ldflags = "-s -w -X github.com/wwsheng009/ai-agent-runtime/internal/buildinfo.version=$Version -X github.com/wwsheng009/ai-agent-runtime/internal/buildinfo.buildTime=$buildTime"
            Invoke-Go -Arguments @(
                "build", "-tags", "win7compat", "-mod=readonly", "-trimpath",
                "-ldflags", $ldflags, "-o", $binaryPath, "./cmd/runtime-server"
            ) -Description "Build runtime-server for Windows 7"
        }
        finally {
            Pop-Location
        }

        Verify-Binary -Path $binaryPath -Label "runtime-server"
        # Smoke-verify that the binary embeds frontend assets and build-info.json.
        # The build strips the symbol table (-s -w), so verify by content search.
        if ([string]::IsNullOrWhiteSpace($script:webUIEntryAsset)) {
            Write-Host "  INFO: No frontend entry recorded (placeholder or kept build)."
        }
        else {
            $embeddedEntryFile = [System.IO.Path]::GetFileName($script:webUIEntryAsset)
            $frontendFound = Test-BinaryContainsText -Path $binaryPath -Text $embeddedEntryFile
            $buildInfoFound = Test-BinaryContainsText -Path $binaryPath -Text "asset_manifest_hash"
            if ($frontendFound -and $buildInfoFound) {
                Write-Host "  Embedded frontend verified: entry=$embeddedEntryFile manifest=present"
            }
            elseif (-not $frontendFound -and -not $buildInfoFound) {
                Write-Host "  WARNING: Binary does not embed frontend assets (placeholder build)."
            }
            else {
                Write-Host "  WARNING: Partial frontend embedding (entry=$frontendFound manifest=$buildInfoFound)."
            }
        }
        Write-Host "Win7 runtime-server build completed successfully."
    }
    finally {
        Restore-EmbeddedWebUI
    }
}
catch {
    $failureMessage = $_.Exception.ToString()
}
finally {
    # This runs after both successful and failed builds.  It also protects the
    # caller from inheriting the Win7-specific Go environment.
    Restore-Environment -Snapshot $environmentSnapshot
}

if (-not [string]::IsNullOrWhiteSpace($failureMessage)) {
    [Console]::Error.WriteLine("build-runtime-server-win7.ps1 failed:")
    [Console]::Error.WriteLine($failureMessage)
    exit 1
}
