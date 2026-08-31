<#
.SYNOPSIS
    Build the Windows 7 compatible aicli binaries.

.DESCRIPTION
    Builds the CLI and its native-console launcher with the isolated Go 1.20
    dependency graph.  The script is deliberately independent from the normal
    release build: it always selects go.win7.mod, the win7compat build tag,
    windows/amd64, and CGO_ENABLED=0.

    The script can be started from any working directory.  Relative output
    paths are resolved against the repository root, while Go is run from
    backend/ so that the alternate module file is unambiguous.

.EXAMPLE
    pwsh -File ./scripts/build-aicli-win7.ps1

.EXAMPLE
    pwsh -File ./scripts/build-aicli-win7.ps1 -Version win7-v1.2.3 `
        -OutputDir 'dist/win7' -SkipTests

.NOTES
    The produced files are aicli-win7.exe and aicli-console-win7.exe.  The
    checksum sidecars use the same two-column format as sha256sum.
#>
[CmdletBinding()]
param(
    [string]$Version = "",
    [string]$OutputDir = "",
    [string]$GoProxy = "",
    [switch]$SkipTests,
    [switch]$SkipDependencyCheck
)

Set-StrictMode -Version Latest
$ErrorActionPreference = "Stop"

# PowerShell 7 can turn native stderr into a terminating error when this
# preference is enabled.  We inspect the native exit code ourselves so this
# script behaves the same under Windows PowerShell 5.1 and pwsh.
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
            # SetEnvironmentVariable(null) removes the variable, but Remove-Item
            # also clears PowerShell's environment drive immediately on older PS.
            Remove-Item -LiteralPath ("Env:{0}" -f $name) -ErrorAction SilentlyContinue
        }
    }
}

# Resolve paths and defaults before changing the process environment.
$backendDir = Join-Path $script:repoRoot "backend"
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
    $modFile = Join-Path $backendDir "go.win7.mod"
    $sumFile = Join-Path $backendDir "go.win7.sum"
    Assert-File -Path $modFile -Description "Win7 module file"
    Assert-File -Path $sumFile -Description "Win7 module checksum file"

    $goInfo = Get-Command $script:goCommand -ErrorAction SilentlyContinue
    if ($null -eq $goInfo) {
        throw "The Go command was not found in PATH. Install Go 1.21+ (for automatic toolchain download) or Go 1.20.14."
    }
    if (-not [string]::IsNullOrWhiteSpace([string]$goInfo.Source)) {
        $script:goCommand = [string]$goInfo.Source
    }

    if ([string]::IsNullOrWhiteSpace($Version)) {
        foreach ($candidate in @($env:AICLI_VERSION, $env:WIN7_VERSION, $env:GITHUB_REF_NAME)) {
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
    Write-Host "Building Windows 7 compatible aicli (version $Version)"
    Write-Host "  repository: $script:repoRoot"
    Write-Host "  output:    $outputRoot"
    Write-Host "  toolchain: Go 1.20.14 / windows-amd64 / CGO=0"

    # Keep the alternate module file and all target settings scoped to this
    # process.  In particular, do not make a caller's normal GOOS/GOFLAGS leak
    # into subsequent commands after this script exits.
    [System.Environment]::SetEnvironmentVariable("GOTOOLCHAIN", "go1.20.14", "Process")
    [System.Environment]::SetEnvironmentVariable("CGO_ENABLED", "0", "Process")
    [System.Environment]::SetEnvironmentVariable("GOFLAGS", "-modfile=go.win7.mod", "Process")
    # Ordinary tests must execute on the host (the script is also useful from
    # Linux/macOS under pwsh).  Remove inherited target values for this phase;
    # Go then selects GOHOSTOS/GOHOSTARCH instead of trying to execute a PE.
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
        # readonly makes an accidental module-file rewrite a hard failure.  A
        # dependency update belongs in an explicit go mod tidy -modfile=...
        # maintenance operation, never in an ordinary build.
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
                "test", "-tags", "win7compat", "-mod=readonly", "./internal/agent",
                "-run", "TestAgentWithoutCancel|TestAgentWithTimeoutCause|TestComputeAvailableToolsDoesNotExposePolicyDeniedSpawnSubagents",
                "-count=1"
            ) -Description "Run Go 1.20 agent compatibility tests"
            Invoke-Go -Arguments @(
                "test", "-tags", "win7compat", "-mod=readonly",
                "./internal/aiclipaths", "./internal/agentconfig", "./internal/config",
                "./internal/chat", "./cmd/runtime-server", "-count=1"
            ) -Description "Run Win7 configuration and runtime tests"
            Invoke-Go -Arguments @(
                "test", "-tags", "win7compat", "-mod=readonly", "./cmd/aicli/commands",
                "-run", "Test(GetMCPConfigPath|ResolveGlobalRuntimeConfigPath|RunInitCommand|InitCommandHelp)",
                "-count=1"
            ) -Description "Run aicli configuration tests"
            Invoke-Go -Arguments @(
                "test", "-tags", "win7compat", "-mod=readonly", "./cmd/aicli-console", "-count=1"
            ) -Description "Run native console launcher tests"

            # Windows-only console tests are compiled, never executed on the
            # build host.  This catches Go 1.20/build-tag regressions while
            # remaining safe when the script is run on Linux/macOS.
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
        finally {
            Pop-Location
        }
    }

    $targets = @(
        [pscustomobject]@{
            Label = "aicli"
            FileName = "aicli-win7.exe"
            Package = "./cmd/aicli"
            LinkerFlags = "-s -w -X main.version=$Version -X main.buildTime=$buildTime"
        },
        [pscustomobject]@{
            Label = "aicli-console"
            FileName = "aicli-console-win7.exe"
            Package = "./cmd/aicli-console"
            LinkerFlags = "-s -w"
        }
    )

    Push-Location -LiteralPath $backendDir
    try {
        # Only the actual artifact builds use the Windows target.  Keeping this
        # assignment here (after host-side tests) avoids exec-format failures
        # when the script is run under pwsh on a non-Windows host.
        [System.Environment]::SetEnvironmentVariable("GOOS", "windows", "Process")
        [System.Environment]::SetEnvironmentVariable("GOARCH", "amd64", "Process")
        foreach ($target in $targets) {
            $targetPath = Join-Path $outputRoot $target.FileName
            if (Test-Path -LiteralPath $targetPath) {
                Remove-Item -LiteralPath $targetPath -Force
            }
            $checksumPath = $targetPath + ".sha256"
            if (Test-Path -LiteralPath $checksumPath) {
                Remove-Item -LiteralPath $checksumPath -Force
            }

            $buildArguments = @(
                "build", "-tags", "win7compat", "-mod=readonly", "-trimpath",
                "-ldflags", $target.LinkerFlags, "-o", $targetPath, $target.Package
            )
            Invoke-Go -Arguments $buildArguments -Description "Build $($target.Label) for Windows 7"
        }
    }
    finally {
        Pop-Location
    }

    foreach ($target in $targets) {
        Verify-Binary -Path (Join-Path $outputRoot $target.FileName) -Label $target.Label
    }
    Write-Host "Win7 aicli build completed successfully."
}
catch {
    $failureMessage = $_.Exception.ToString()
}
finally {
    Restore-Environment -Snapshot $environmentSnapshot
}

if (-not [string]::IsNullOrWhiteSpace($failureMessage)) {
    [Console]::Error.WriteLine("build-aicli-win7.ps1 failed:")
    [Console]::Error.WriteLine($failureMessage)
    exit 1
}
