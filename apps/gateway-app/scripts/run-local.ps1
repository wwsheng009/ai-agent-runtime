# Launch the locally built desktop shell from a normal user directory.
#
# Sandboxed build environments can mark files under target/ as Low integrity.
# Windows blocks such processes from registering a notification-area icon
# (Shell_NotifyIcon returns ERROR_ACCESS_DENIED), so the tray menu never appears.
# Copying the exe to the user profile removes that label and lets Explorer see it.

$ErrorActionPreference = 'Stop'

$source = Join-Path $PSScriptRoot '..\src-tauri\target\release\aicli-gateway-app.exe'
if (-not (Test-Path $source)) {
    throw "missing release build: $source`nRun: npm run build -- --no-bundle"
}

$targetDir = Join-Path $env:USERPROFILE 'aicli-gateway'
New-Item -ItemType Directory -Force -Path $targetDir | Out-Null
$target = Join-Path $targetDir 'aicli-gateway-app.exe'
Copy-Item $source $target -Force

$label = (icacls $target | Select-String -Pattern 'Mandatory Label').Line
if ($label -match 'Low Mandatory Level') {
    Write-Warning "copied exe still has a low integrity label: $label"
} elseif ($label) {
    Write-Host "integrity label: $label"
} else {
    Write-Host 'integrity label: default (medium)'
}

Write-Host "launching $target"
Start-Process -FilePath $target