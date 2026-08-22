[CmdletBinding()]
param(
    [string]$OutputDir = "dist",
    [string]$Version = "dev",
    [string]$Goos = "",
    [string]$Goarch = "",
    [string]$ApiBaseUrl = "",
    [switch]$SkipFrontendInstall,
    [switch]$SkipFrontendBuild,
    [switch]$SkipTests
)

Set-StrictMode -Version Latest
$ErrorActionPreference = "Stop"

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

Assert-Command "go"
# 复用预构建 frontend dist 时（CI 中由 build-frontend job 提供）不再要求 node/pnpm。
if (-not $SkipFrontendBuild) {
    Assert-Command "node"
    Assert-Command "pnpm"
}

$repoRoot = [System.IO.Path]::GetFullPath((Join-Path $PSScriptRoot ".."))
$frontendDir = Join-Path $repoRoot "frontend"
$frontendDist = Join-Path $frontendDir "dist"
$backendDir = Join-Path $repoRoot "backend"
$embeddedDist = Join-Path $backendDir "internal/webui/dist"
$outputRoot = Resolve-RepoPath -Root $repoRoot -Path $OutputDir

if ([string]::IsNullOrWhiteSpace($Goos)) {
    $Goos = (& go env GOOS).Trim()
    Assert-LastExitCode "Resolve GOOS"
}
if ([string]::IsNullOrWhiteSpace($Goarch)) {
    $Goarch = (& go env GOARCH).Trim()
    Assert-LastExitCode "Resolve GOARCH"
}

$Version = $Version.Trim()
if ([string]::IsNullOrWhiteSpace($Version)) {
    $Version = "dev"
}
if ($Version -notmatch '^[0-9A-Za-z][0-9A-Za-z._+\-]*$') {
    throw "Version '$Version' contains characters that cannot be embedded in Go linker metadata."
}
$safeVersion = $Version -replace '[^0-9A-Za-z._-]', '-'
$artifactBaseName = "runtime-server-$safeVersion-$Goos-$Goarch"
$executableName = if ($Goos -eq "windows") { "runtime-server.exe" } else { "runtime-server" }
$packageDir = Join-Path $outputRoot $artifactBaseName
$binaryPath = Join-Path $packageDir $executableName
$archivePath = Join-Path $outputRoot "$artifactBaseName.zip"
$buildTime = [DateTime]::UtcNow.ToString("o")
$gitCommit = "unknown"
$gitDirty = $true
if (Get-Command git -ErrorAction SilentlyContinue) {
    $candidateCommit = (& git -C $repoRoot rev-parse HEAD 2>$null)
    if ($LASTEXITCODE -eq 0 -and -not [string]::IsNullOrWhiteSpace(($candidateCommit -join ""))) {
        $gitCommit = ($candidateCommit -join "").Trim()
    }
    $candidateStatus = @(& git -C $repoRoot status --porcelain --untracked-files=normal 2>$null)
    if ($LASTEXITCODE -eq 0) {
        $gitDirty = $candidateStatus.Count -gt 0
    }
}

if (-not $SkipFrontendBuild) {
    Write-Host "==> Building frontend"
    $oldApiBaseUrl = $env:VITE_API_BASE_URL
    Push-Location $frontendDir
    try {
        # An embedded frontend should use same-origin /api routes by default.
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
else {
    Write-Host "==> Reusing prebuilt frontend dist: $frontendDist"
}

$frontendIndex = Join-Path $frontendDist "index.html"
if (-not (Test-Path -LiteralPath $frontendIndex -PathType Leaf)) {
    throw "Frontend dist is missing '$frontendIndex' (run pnpm build or pass -SkipFrontendBuild with a prebuilt dist)."
}

Write-Host "==> Staging frontend for Go embed"
New-Item -ItemType Directory -Path $embeddedDist -Force | Out-Null
Get-ChildItem -LiteralPath $embeddedDist -Force |
    Where-Object { $_.Name -ne "placeholder.txt" } |
    Remove-Item -Recurse -Force
Copy-Item -Path (Join-Path $frontendDist "*") -Destination $embeddedDist -Recurse -Force

$embeddedIndex = Join-Path $embeddedDist "index.html"
if (-not (Test-Path -LiteralPath $embeddedIndex -PathType Leaf)) {
    throw "Failed to stage the frontend in '$embeddedDist'."
}

$frontendManifestHash = Get-AssetManifestHash -Root $embeddedDist
$frontendEntryAsset = Get-FrontendEntryAsset -IndexPath $embeddedIndex
$frontendBuildInfo = [ordered]@{
    asset_manifest_hash = $frontendManifestHash
    build_time = $buildTime
    entry_asset = $frontendEntryAsset
}
$frontendBuildInfoPath = Join-Path $embeddedDist "build-info.json"
$frontendBuildInfoJson = $frontendBuildInfo | ConvertTo-Json -Depth 3
$utf8 = New-Object System.Text.UTF8Encoding($false)
[System.IO.File]::WriteAllText($frontendBuildInfoPath, $frontendBuildInfoJson + "`n", $utf8)

$verifiedManifestHash = Get-AssetManifestHash -Root $embeddedDist
if ($verifiedManifestHash -ne $frontendManifestHash) {
    throw "Frontend manifest hash changed after writing build-info.json."
}
Write-Host "  Frontend manifest: $frontendManifestHash"
Write-Host "  Frontend entry:    $frontendEntryAsset"

if (-not $SkipTests) {
    Write-Host "==> Testing embedded web UI and runtime-server"
    Push-Location $backendDir
    try {
        & go test ./internal/webui ./cmd/runtime-server
        Assert-LastExitCode "Go tests"
    }
    finally {
        Pop-Location
    }
}

Write-Host "==> Building runtime-server for $Goos/$Goarch"
New-Item -ItemType Directory -Path $outputRoot -Force | Out-Null
if (Test-Path -LiteralPath $packageDir) {
    Remove-Item -LiteralPath $packageDir -Recurse -Force
}
New-Item -ItemType Directory -Path $packageDir -Force | Out-Null

$oldGoos = $env:GOOS
$oldGoarch = $env:GOARCH
$oldCgoEnabled = $env:CGO_ENABLED
try {
    $env:GOOS = $Goos
    $env:GOARCH = $Goarch
    $env:CGO_ENABLED = "0"

    Push-Location $backendDir
    try {
        $gitDirtyValue = ([string]$gitDirty).ToLowerInvariant()
        $ldflags = "-s -w -X github.com/wwsheng009/ai-agent-runtime/internal/buildinfo.version=$Version -X github.com/wwsheng009/ai-agent-runtime/internal/buildinfo.gitCommit=$gitCommit -X github.com/wwsheng009/ai-agent-runtime/internal/buildinfo.gitDirty=$gitDirtyValue -X github.com/wwsheng009/ai-agent-runtime/internal/buildinfo.buildTime=$buildTime"
        & go build -trimpath -ldflags $ldflags -o $binaryPath ./cmd/runtime-server
        Assert-LastExitCode "Build runtime-server"
    }
    finally {
        Pop-Location
    }
}
finally {
    $env:GOOS = $oldGoos
    $env:GOARCH = $oldGoarch
    $env:CGO_ENABLED = $oldCgoEnabled
}

if (-not (Test-Path -LiteralPath $binaryPath -PathType Leaf)) {
    throw "Go build did not produce '$binaryPath'."
}

if (-not $SkipTests) {
    $hostGoos = (& go env GOHOSTOS).Trim()
    Assert-LastExitCode "Resolve host GOOS"
    $hostGoarch = (& go env GOHOSTARCH).Trim()
    Assert-LastExitCode "Resolve host GOARCH"
    if ($Goos -eq $hostGoos -and $Goarch -eq $hostGoarch) {
        Write-Host "==> Running embedded runtime-server smoke test"
        $e2eRoot = [System.IO.Path]::GetFullPath((Join-Path ([System.IO.Path]::GetTempPath()) "ai-agent-runtime-package-e2e"))
        $e2eDir = Join-Path $e2eRoot ([Guid]::NewGuid().ToString("N"))
        New-Item -ItemType Directory -Path $e2eDir -Force | Out-Null
        $stdoutPath = Join-Path $e2eDir "stdout.log"
        $stderrPath = Join-Path $e2eDir "stderr.log"
        $probe = New-Object System.Net.Sockets.TcpListener([System.Net.IPAddress]::Loopback, 0)
        $probe.Start()
        $port = $probe.LocalEndpoint.Port
        $probe.Stop()
        $listenAddress = "127.0.0.1:$port"
        $baseUrl = "http://127.0.0.1:$port"
        $process = $null
        try {
            $startParams = @{
                FilePath               = $binaryPath
                ArgumentList           = @("serve", "--listen", $listenAddress, "--pid-file", "runtime-server.pid")
                WorkingDirectory       = $e2eDir
                RedirectStandardOutput = $stdoutPath
                RedirectStandardError  = $stderrPath
                PassThru               = $true
            }
            if ($IsWindows) {
                # -WindowStyle 仅 Windows 支持；Linux/macOS runner 上不传该参数。
                $startParams["WindowStyle"] = "Hidden"
            }
            $process = Start-Process @startParams

            $health = $null
            for ($attempt = 0; $attempt -lt 60; $attempt++) {
                if ($process.HasExited) {
                    $stderr = if (Test-Path -LiteralPath $stderrPath) { Get-Content -LiteralPath $stderrPath -Raw } else { "" }
                    throw "Embedded runtime-server exited before health check (code $($process.ExitCode)): $stderr"
                }
                try {
                    $health = Invoke-RestMethod -Uri "$baseUrl/healthz" -Method Get -TimeoutSec 2
                    break
                }
                catch {
                    Start-Sleep -Milliseconds 500
                }
            }
            if ($null -eq $health) {
                throw "Timed out waiting for embedded runtime-server health endpoint."
            }
            if ($health.backend.version -ne $Version) {
                throw "Backend version mismatch: expected '$Version', got '$($health.backend.version)'."
            }
            if ($health.backend.git_commit -ne $gitCommit) {
                throw "Backend commit mismatch: expected '$gitCommit', got '$($health.backend.git_commit)'."
            }
            if ([bool]$health.backend.git_dirty -ne $gitDirty) {
                throw "Backend dirty flag mismatch: expected '$gitDirty', got '$($health.backend.git_dirty)'."
            }
            if ($health.frontend.asset_manifest_hash -ne $frontendManifestHash) {
                throw "Frontend manifest mismatch: expected '$frontendManifestHash', got '$($health.frontend.asset_manifest_hash)'."
            }
            $healthBackendBuildTime = if ($health.backend.build_time -is [DateTime]) {
                $health.backend.build_time.ToUniversalTime().ToString("o")
            } else {
                [string]$health.backend.build_time
            }
            $healthFrontendBuildTime = if ($health.frontend.build_time -is [DateTime]) {
                $health.frontend.build_time.ToUniversalTime().ToString("o")
            } else {
                [string]$health.frontend.build_time
            }
            if ($healthBackendBuildTime -ne $buildTime -or
                $healthFrontendBuildTime -ne $buildTime -or
                $health.frontend.entry_asset -ne $frontendEntryAsset) {
                throw "Embedded build metadata mismatch."
            }

            $routeResponse = Invoke-WebRequest -Uri "$baseUrl/workspace/chats/new" -Method Get -TimeoutSec 5 -UseBasicParsing
            if ($routeResponse.StatusCode -ne 200 -or $routeResponse.Content -notmatch 'id="root"') {
                throw "Embedded SPA route did not return the expected shell."
            }
            $entryResponse = Invoke-WebRequest -Uri "$baseUrl$frontendEntryAsset" -Method Get -TimeoutSec 5 -UseBasicParsing
            if ($entryResponse.StatusCode -ne 200 -or [string]::IsNullOrWhiteSpace($entryResponse.Content)) {
                throw "Embedded frontend entry asset did not return content."
            }
        }
        finally {
            if ($null -ne $process -and -not $process.HasExited) {
                Stop-Process -Id $process.Id -Force -ErrorAction SilentlyContinue
                $null = $process.WaitForExit(5000)
            }
            if (Test-Path -LiteralPath $e2eDir) {
                Remove-Item -LiteralPath $e2eDir -Recurse -Force -ErrorAction SilentlyContinue
            }
        }
    }
    else {
        Write-Host "  Skipping embedded smoke test for cross-compiled target $Goos/$Goarch (host $hostGoos/$hostGoarch)."
    }
}

$packageReadme = @"
runtime-server $Version ($Goos/$Goarch)

Backend commit: $gitCommit
Backend dirty:  $gitDirty
Build time:     $buildTime
Frontend hash:  $frontendManifestHash
Frontend entry: $frontendEntryAsset

The production frontend is embedded in $executableName.
Run:
  ./$executableName serve --listen 0.0.0.0:8101
Then open:
  http://127.0.0.1:8101/
Health check:
  http://127.0.0.1:8101/healthz
"@
[System.IO.File]::WriteAllText((Join-Path $packageDir "README.txt"), $packageReadme)

Write-Host "==> Creating archive"
if (Test-Path -LiteralPath $archivePath) {
    Remove-Item -LiteralPath $archivePath -Force
}
Compress-Archive -Path (Join-Path $packageDir "*") -DestinationPath $archivePath -CompressionLevel Optimal

Write-Host "==> Writing checksum"
$archiveName = Split-Path -Leaf $archivePath
$archiveHash = (Get-FileHash -LiteralPath $archivePath -Algorithm SHA256).Hash.ToLowerInvariant()
$checksumPath = "$archivePath.sha256"
$utf8NoBom = New-Object System.Text.UTF8Encoding($false)
[System.IO.File]::WriteAllText($checksumPath, "$archiveHash  $archiveName`n", $utf8NoBom)

$binarySize = (Get-Item -LiteralPath $binaryPath).Length
$archiveSize = (Get-Item -LiteralPath $archivePath).Length
Write-Host ""
Write-Host "Package completed successfully."
Write-Host "  Embedded frontend: $embeddedDist"
Write-Host "  Frontend manifest: $frontendManifestHash"
Write-Host "  Backend commit:    $gitCommit (dirty=$gitDirty)"
Write-Host "  Build time:        $buildTime"
Write-Host "  Binary:            $binaryPath ($binarySize bytes)"
Write-Host "  Archive:           $archivePath ($archiveSize bytes)"
Write-Host "  Checksum:          $checksumPath ($archiveHash)"
