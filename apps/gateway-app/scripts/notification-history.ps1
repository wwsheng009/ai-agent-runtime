# Read the Windows toast history for the aicli-gateway AUMID.
#
# Requires Windows PowerShell 5.1 (pwsh 7 cannot activate the WinRT
# ToastNotificationManager type directly):
#
#   powershell.exe -NoProfile -ExecutionPolicy Bypass -File scripts/notification-history.ps1

$ErrorActionPreference = 'Stop'
$Aumid = 'online.ttai.aicli.gateway'

try {
    $history = [Windows.UI.Notifications.ToastNotificationManager, Windows.UI.Notifications, ContentType = WindowsRuntime]::History.GetHistory($Aumid)
    Write-Output ("count=" + $history.Count)
    foreach ($toast in $history) {
        $parts = @()
        foreach ($text in $toast.Content.GetElementsByTagName('text')) { $parts += $text.InnerText }
        Write-Output ($toast.Tag + ' | ' + ($parts -join ' / '))
    }
} catch {
    Write-Output ('ERR: ' + $_.Exception.Message)
}