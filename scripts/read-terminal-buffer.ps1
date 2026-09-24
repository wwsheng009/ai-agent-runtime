<#
.SYNOPSIS
  读取物理终端缓冲区（UIA 文本文档），用于"应然帧 vs 物理帧"取证。

.DESCRIPTION
  复用 aicli-e2e-harness.ps1 的 C4 实现（Get-AicliUiaScreenText），把一个已经
  打开的 Windows Terminal 窗口的文本缓冲区完整落盘，并给出摘要与模式计数。

  只读：本脚本不向目标窗口发送任何输入（不写 console input、不 PostMessage）。
  适用场景：aicli 进程已退出但终端窗口仍在，需要判定"答案/历史行到底有没有
  真正写进终端"（物理帧），与 HTTP /debug/chat/screen 的"应然帧"对比。

.PARAMETER WindowTitle
  窗口标题匹配子串（Windows Terminal 的标签标题）。默认 'ai-agent-runtime'。

.PARAMETER List
  只列出当前所有 Windows Terminal 顶层窗口（PID + 标题）后退出，不做读取。
  同一台机器上开着多个终端窗口时，先用它确认要取证的是哪一个。

.PARAMETER OutPath
  完整文档落盘路径；默认 artifacts/terminal-buffer-<yyyyMMdd-HHmmss>.txt。

.PARAMETER Pattern
  需要在摘要里统计出现次数的正则（可多个）。

.PARAMETER TailLines
  摘要中打印的尾部行数，默认 15。

.EXAMPLE
  pwsh -File scripts/read-terminal-buffer.ps1 -WindowTitle 'ai-agent-runtime' -Pattern 'AICLI-E2E-HISTORY-\d{3}'
#>
[CmdletBinding()]
param(
    [string]$WindowTitle = 'ai-agent-runtime',

    [switch]$List,

    [string]$OutPath,

    [string[]]$Pattern = @(),

    [ValidateRange(0, 500)]
    [int]$TailLines = 15
)

$ErrorActionPreference = "Stop"

$repoRoot = Split-Path -Parent $PSScriptRoot
. (Join-Path $PSScriptRoot 'aicli-e2e-harness.ps1')

if ($List) {
    # Windows Terminal 是单进程多窗口：Get-Process 的 MainWindowTitle 只反映其中一个
    # 窗口，必须用 UI Automation 根节点枚举，否则会漏掉其它窗口（取证时容易读错对象）。
    # harness 的 Add-Type 只把 UIAutomation 作为 C# 引用加载，PowerShell 侧的类型解析
    # 仍需显式 -AssemblyName 一次。
    Add-Type -AssemblyName UIAutomationClient, UIAutomationTypes -ErrorAction SilentlyContinue
    $condition = New-Object System.Windows.Automation.PropertyCondition(
        [System.Windows.Automation.AutomationElement]::ClassNameProperty,
        'CASCADIA_HOSTING_WINDOW_CLASS')
    $windows = [System.Windows.Automation.AutomationElement]::RootElement.FindAll(
        [System.Windows.Automation.TreeScope]::Children, $condition)
    Write-Host ("terminal windows: {0}" -f $windows.Count)
    foreach ($window in $windows) {
        Write-Host ("  pid={0}  title={1}" -f $window.Current.ProcessId, $window.Current.Name)
    }
    exit 0
}

if ([string]::IsNullOrWhiteSpace($OutPath)) {
    $stamp = Get-Date -Format "yyyyMMdd-HHmmss"
    $OutPath = Join-Path $repoRoot "artifacts/terminal-buffer-$stamp.txt"
} elseif (-not [System.IO.Path]::IsPathRooted($OutPath)) {
    $OutPath = Join-Path $repoRoot $OutPath
}
$outDir = Split-Path -Parent $OutPath
if (-not (Test-Path -LiteralPath $outDir)) {
    New-Item -ItemType Directory -Path $outDir -Force | Out-Null
}

$doc = Get-AicliUiaScreenText -WindowTitle $WindowTitle
if ($null -eq $doc) {
    Write-Host "[FAIL] no terminal window matched title pattern '$WindowTitle'"
    exit 1
}

$all = [string]$doc.all
[IO.File]::WriteAllText($OutPath, $all, [Text.UTF8Encoding]::new($false))
$lines = @($all -split "`r?`n")

Write-Host "[OK] matched title : $($doc.title)"
Write-Host "[OK] window pid    : $($doc.pid)"
Write-Host "[OK] buffer lines  : $($lines.Count)"
Write-Host "[OK] full document : $OutPath"

foreach ($item in $Pattern) {
    $matches = @($lines | Where-Object { $_ -match $item })
    Write-Host ("[PATTERN] {0} :: count={1}" -f $item, $matches.Count)
}

if ($TailLines -gt 0) {
    Write-Host "--- tail (last $TailLines lines) ---"
    $start = [Math]::Max(0, $lines.Count - $TailLines)
    for ($index = $start; $index -lt $lines.Count; $index++) {
        Write-Host ("{0,6}: {1}" -f ($index + 1), $lines[$index])
    }
}
