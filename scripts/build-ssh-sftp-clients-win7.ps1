<#
.SYNOPSIS
    Build the Windows 7 compatible ssh-client and sftp-client binaries.

.DESCRIPTION
    Builds ./cmd/ssh-client and ./cmd/sftp-client with the isolated Go 1.20
    dependency graph.  The script is deliberately independent from the normal
    release build: it always selects go.win7.mod, the win7compat build tag,
    windows/amd64, and CGO_ENABLED=0.

    The script can be started from any working directory.  Relative output
    paths are resolved against the repository root, while Go is run from
    backend/ so that the alternate module file is unambiguous.

.EXAMPLE
    pwsh -File ./scripts/build-ssh-sftp-clients-win7.ps1

.EXAMPLE
    pwsh -File ./scripts/build-ssh-sftp-clients-win7.ps1 -Version win7-v1.2.3 `
        -OutputDir 'dist/win7' -SkipTests

.NOTES
    The produced files are ssh-client-win7.exe and sftp-client-win7.exe.  The
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
    $environmentSnapshot[$name] = [System.Environment]::GetEnvironmentVariable($name)
}

try {
    # ----- Output directory -------------------------------------------------
    if ([string]::IsNullOrWhiteSpace($OutputDir)) {
        $outputRoot = Join-Path $script:repoRoot "backend\dist"
    }
    else {
        $outputRoot = Resolve-RepoPath -Path $OutputDir
    }
    if (-not (Test-Path -LiteralPath $outputRoot -PathType Container)) {
        New-Item -Path $outputRoot -ItemType Directory -Force | Out-Null
    }
    Write-Host "Output directory: $outputRoot"

    # ----- Environment ------------------------------------------------------
    [System.Environment]::SetEnvironmentVariable("GOTOOLCHAIN", "go1.20.14", "Process")
    [System.Environment]::SetEnvironmentVariable("GOOS", "windows", "Process")
    [System.Environment]::SetEnvironmentVariable("GOARCH", "amd64", "Process")
    [System.Environment]::SetEnvironmentVariable("CGO_ENABLED", "0", "Process")
    [System.Environment]::SetEnvironmentVariable("GOFLAGS", "-modfile=go.win7.mod", "Process")
    if (-not [string]::IsNullOrWhiteSpace($GoProxy)) {
        [System.Environment]::SetEnvironmentVariable("GOPROXY", $GoProxy, "Process")
    }

    # ----- Dependency check (optional) -------------------------------------
    if (-not $SkipDependencyCheck) {
        Write-Host "Checking go.win7.mod and go.win7.sum..."
        Assert-File -Path (Join-Path $backendDir "go.win7.mod") -Description "go.win7.mod"
        Assert-File -Path (Join-Path $backendDir "go.win7.sum") -Description "go.win7.sum"

        $goVersion = Invoke-GoCapture -Arguments @("version") -Description "Go version check"
        Write-Host ($goVersion -join " ")
        $versionLine = ($goVersion | Where-Object { $_ -match "go1\.20\.14" } | Select-Object -First 1)
        if ([string]::IsNullOrWhiteSpace($versionLine)) {
            throw "GOTOOLCHAIN=go1.20.14 did not resolve to Go 1.20.14. Installed: $($goVersion -join ' ')"
        }
    }

    Write-Host "Build environment: GOTOOLCHAIN=go1.20.14 GOOS=windows GOARCH=amd64 CGO_ENABLED=0 GOFLAGS=-modfile=go.win7.mod"

    # Build version string for linker injection.
    $buildTime = (Get-Date -Format "yyyy-MM-ddTHH:mm:ssZ")
    $versionLabel = if ([string]::IsNullOrWhiteSpace($Version)) { "win7-dev" } else { $Version }
    $linkerFlags = "-s -w -X main.version=$versionLabel"

    $targets = @(
        [pscustomobject]@{
            Label = "ssh-client"
            FileName = "ssh-client-win7.exe"
            Package = "./cmd/ssh-client"
            LinkerFlags = $linkerFlags
        },
        [pscustomobject]@{
            Label = "sftp-client"
            FileName = "sftp-client-win7.exe"
            Package = "./cmd/sftp-client"
            LinkerFlags = $linkerFlags
        }
    )

    # ----- Tests (optional) -------------------------------------------------
    if (-not $SkipTests) {
        Push-Location -LiteralPath $backendDir
        try {
            Write-Host "Running host-side tests..."
            # Only run tests that do not require a Windows target binary.
            # For cross-compiled test exe, a future enhancement could emulate
            # the aicli script's -c approach.
            Invoke-Go -Arguments @(
                "test", "-tags", "win7compat", "-mod=readonly", "-count=1",
                "./internal/winconsole/..."
            ) -Description "Run winconsole package tests"
        }
        finally {
            Pop-Location
        }
    }

    # ----- Build targets ----------------------------------------------------
    Push-Location -LiteralPath $backendDir
    try {
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

    # ----- Verify ------------------------------------------------------------
    foreach ($target in $targets) {
        Verify-Binary -Path (Join-Path $outputRoot $target.FileName) -Label $target.Label
    }
    Write-Host "Win7 ssh-client and sftp-client build completed successfully."
}
catch {
    $failureMessage = $_.Exception.ToString()
}
finally {
    Restore-Environment -Snapshot $environmentSnapshot
}

if (-not [string]::IsNullOrWhiteSpace($failureMessage)) {
    [Console]::Error.WriteLine("build-ssh-sftp-clients-win7.ps1 failed:")
    [Console]::Error.WriteLine($failureMessage)
    exit 1
}