# 启动 aicli web 服务器并保持 stdin 打开（避免后台无 TTY 时进程退出）。
$binary = "E:\projects\ai\ai-agent-runtime\backend\aicli-2x.exe"
$env:AICLI_PPROF = "127.0.0.1:61228"

$psi = New-Object System.Diagnostics.ProcessStartInfo
$psi.FileName = $binary
$psi.Arguments = "--yolo --pprof --debug"
$psi.UseShellExecute = $false
$psi.RedirectStandardInput = $true
$psi.RedirectStandardOutput = $false
$psi.RedirectStandardError = $false
$psi.CreateNoWindow = $true

try {
    $p = [System.Diagnostics.Process]::Start($psi)
    Write-Output "PID=$($p.Id)"
    $p.WaitForExit()
} catch {
    Write-Output "启动失败: $_"
}