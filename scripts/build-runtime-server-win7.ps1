<#
.SYNOPSIS
    Backward-compatible wrapper for the Win7 runtime-server build.

.DESCRIPTION
    Forwards to scripts/build.ps1 with -Tools runtime-server -Target win7.
    All parameters are passed through unchanged.

.EXAMPLE
    pwsh -File ./scripts/build-runtime-server-win7.ps1 -Version win7-v1.2.3 -OutputDir dist/win7 -SkipTests
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

$scriptDir = if (Test-Path -LiteralPath "variable:PSScriptRoot") {
    [string]$PSScriptRoot
}
else {
    Split-Path -Parent $MyInvocation.MyCommand.Definition
}
$buildScript = Join-Path $scriptDir "build.ps1"
if (-not (Test-Path -LiteralPath $buildScript -PathType Leaf)) {
    throw "Unified build script not found: $buildScript"
}

# Hashtable splatting: copy caller-bound params, then override Tools/Target
$forwardArgs = @{} + $PSBoundParameters
$forwardArgs['Tools'] = 'runtime-server'
$forwardArgs['Target'] = 'win7'

& $buildScript @forwardArgs
exit $LASTEXITCODE