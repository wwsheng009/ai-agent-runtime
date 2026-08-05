[CmdletBinding()]
param(
    [int]$TimeoutSeconds = 45,
    [switch]$KeepWindow
)

$ErrorActionPreference = "Stop"
$repoRoot = [System.IO.Path]::GetFullPath((Join-Path $PSScriptRoot ".."))
$backend = Join-Path $repoRoot "backend"
$outputDir = Join-Path $repoRoot "output\aicli-terminal-e2e"
$fixture = Join-Path $outputDir "aicli-render-fixture.exe"
$runID = [Guid]::NewGuid().ToString("N")
$windowTitle = "aicli-render-fixture-" + $runID.Substring(0, 12)
$lastMarker = "AICLI-E2E-HISTORY-071"
$promptMarker = "AICLI-E2E-PROMPT-VIEWPORT"
$statusMarker = "AICLI-E2E-STATUS-VIEWPORT"
$markdownMarkers = @(
    "AICLI-E2E-MARKDOWN-HEADING",
    "AICLI-E2E-MARKDOWN-BOLD",
    "AICLI-E2E-MARKDOWN-CODE"
)

New-Item -ItemType Directory -Path $outputDir -Force | Out-Null
Push-Location $backend
try {
    & go build -trimpath -o $fixture ./cmd/aicli-render-fixture
    if ($LASTEXITCODE -ne 0) {
        throw "failed to build aicli-render-fixture"
    }
} finally {
    Pop-Location
}

$command = @"
`$env:AICLI_RENDER_FIXTURE_HOLD_MS = '$([Math]::Max(($TimeoutSeconds + 15) * 1000, 30000))'
`$env:AICLI_RENDER_FIXTURE_RUN_ID = '$runID'
& '$fixture'
exit `$LASTEXITCODE
"@
$encoded = [Convert]::ToBase64String([Text.Encoding]::Unicode.GetBytes($command))
& wt.exe -w new --size "100,24" new-tab --title $windowTitle --suppressApplicationTitle -d $backend pwsh.exe -NoLogo -NoProfile -EncodedCommand $encoded
if ($LASTEXITCODE -ne 0) {
    throw "failed to launch Windows Terminal fixture"
}

Add-Type -TypeDefinition @'
using System;
using System.Runtime.InteropServices;
using System.Text;
using System.Windows.Automation;

public sealed class AicliTerminalDocument {
    public long WindowHandle;
    public int ProcessId;
    public string WindowTitle;
    public string All;
    public string Visible;
}

public static class AicliTerminalAutomation {
    private delegate bool EnumWindowsProc(IntPtr hWnd, IntPtr lParam);

    [DllImport("user32.dll")]
    private static extern bool EnumWindows(EnumWindowsProc callback, IntPtr lParam);

    [DllImport("user32.dll")]
    private static extern bool IsWindowVisible(IntPtr hWnd);

    [DllImport("user32.dll")]
    private static extern uint GetWindowThreadProcessId(IntPtr hWnd, out uint processId);

    [DllImport("user32.dll", CharSet = CharSet.Unicode)]
    private static extern int GetClassName(IntPtr hWnd, StringBuilder className, int maxCount);

    private static bool IsWindowsTerminalWindow(IntPtr window, out int processId) {
        processId = 0;
        if (window == IntPtr.Zero || !IsWindowVisible(window)) return false;
        StringBuilder className = new StringBuilder(256);
        if (GetClassName(window, className, className.Capacity) == 0 ||
            className.ToString().IndexOf("CASCADIA", StringComparison.OrdinalIgnoreCase) < 0) return false;
        uint pid;
        GetWindowThreadProcessId(window, out pid);
        if (pid == 0) return false;
        processId = (int)pid;
        return true;
    }

    private static AicliTerminalDocument Capture(IntPtr window, int processId, string expectedTitle) {
        AutomationElement root = AutomationElement.FromHandle(window);
        if (root == null) return null;
        string title = root.Current.Name ?? string.Empty;
        if (!string.Equals(title, expectedTitle, StringComparison.Ordinal)) return null;
        AutomationElementCollection descendants = root.FindAll(TreeScope.Descendants, Condition.TrueCondition);
        AicliTerminalDocument best = null;
        foreach (AutomationElement element in descendants) {
            object pattern;
            if (!element.TryGetCurrentPattern(TextPattern.Pattern, out pattern)) continue;
            TextPattern textPattern = (TextPattern)pattern;
            string all = textPattern.DocumentRange.GetText(-1);
            if (all != null && (best == null || all.Length > best.All.Length)) {
                string visible = "";
                foreach (var range in textPattern.GetVisibleRanges()) {
                    visible += range.GetText(-1);
                }
                best = new AicliTerminalDocument {
                    WindowHandle = window.ToInt64(), ProcessId = processId, WindowTitle = title,
                    All = all, Visible = visible
                };
            }
        }
        return best;
    }

    public static AicliTerminalDocument FindByTitle(string expectedTitle) {
        AicliTerminalDocument best = null;
        EnumWindows(delegate(IntPtr window, IntPtr ignored) {
            int processId;
            if (!IsWindowsTerminalWindow(window, out processId)) return true;
            try {
                AicliTerminalDocument candidate = Capture(window, processId, expectedTitle);
                if (candidate != null && (best == null || candidate.All.Length > best.All.Length)) best = candidate;
            } catch (ElementNotAvailableException) { }
              catch (InvalidOperationException) { }
              catch (COMException) { }
            return true;
        }, IntPtr.Zero);
        return best;
    }
}
'@ -ReferencedAssemblies UIAutomationClient,UIAutomationTypes

$deadline = (Get-Date).AddSeconds($TimeoutSeconds)
$terminalDocument = $null
$lastDocument = $null
while ((Get-Date) -lt $deadline) {
    $candidate = [AicliTerminalAutomation]::FindByTitle($windowTitle)
    if ($candidate) {
        $lastDocument = $candidate
    }
    if ($candidate -and
        $candidate.All.Contains($lastMarker) -and
        $candidate.All.Contains($promptMarker) -and
        $candidate.All.Contains($statusMarker)) {
        $terminalDocument = $candidate
        break
    }
    Start-Sleep -Milliseconds 250
}

if (-not $terminalDocument) {
    if ($lastDocument) {
        Set-Content -LiteralPath (Join-Path $outputDir "windows-terminal-timeout-buffer.txt") -Value $lastDocument.All -Encoding utf8
        Set-Content -LiteralPath (Join-Path $outputDir "windows-terminal-timeout-visible.txt") -Value $lastDocument.Visible -Encoding utf8
    }
    throw "Windows Terminal fixture '$windowTitle' did not become ready within $TimeoutSeconds seconds"
}
$terminalProcess = Get-Process -Id $terminalDocument.ProcessId -ErrorAction Stop
if (-not [string]::Equals($terminalProcess.ProcessName, "WindowsTerminal", [StringComparison]::OrdinalIgnoreCase)) {
    throw "fixture window process is '$($terminalProcess.ProcessName)', expected WindowsTerminal.exe"
}
$document = $terminalDocument.All
$visible = $terminalDocument.Visible

$failures = [System.Collections.Generic.List[string]]::new()
$first = "AICLI-E2E-HISTORY-000"
$last = $lastMarker
$prompt = $promptMarker
$status = $statusMarker

foreach ($marker in @($first, $last, $prompt, $status)) {
    $count = ([regex]::Matches($document, [regex]::Escape($marker))).Count
    if ($count -ne 1) {
        $failures.Add("marker '$marker' count=$count, want exactly 1")
    }
}
foreach ($marker in $markdownMarkers) {
    $count = ([regex]::Matches($document, [regex]::Escape($marker))).Count
    if ($count -ne 1) {
        $failures.Add("rendered Markdown marker '$marker' count=$count, want exactly 1")
    }
}
foreach ($rawMarkdown in @(
    "# AICLI-E2E-MARKDOWN-HEADING",
    "**AICLI-E2E-MARKDOWN-BOLD**",
    '`AICLI-E2E-MARKDOWN-CODE`'
)) {
    if ($document.Contains($rawMarkdown)) {
        $failures.Add("raw Markdown syntax leaked into the Windows Terminal document: '$rawMarkdown'")
    }
}
for ($index = 0; $index -lt 72; $index++) {
    $marker = "AICLI-E2E-HISTORY-{0:D3}" -f $index
    $count = ([regex]::Matches($document, [regex]::Escape($marker))).Count
    if ($count -ne 1) {
        $failures.Add("history marker '$marker' count=$count, want exactly 1")
    }
}
if ($visible.Contains($first)) {
    $failures.Add("oldest history marker remained in the visible viewport instead of scrolling out")
}
if (-not $visible.Contains($last)) {
    $failures.Add("newest history marker is absent from the visible viewport")
}
foreach ($marker in @($prompt, $status)) {
    if (-not $visible.Contains($marker)) {
        $failures.Add("inline viewport marker '$marker' is absent from the visible viewport")
    }
}

if ($failures.Count -gt 0) {
    $dump = Join-Path $outputDir "windows-terminal-buffer.txt"
    $visibleDump = Join-Path $outputDir "windows-terminal-visible.txt"
    Set-Content -LiteralPath $dump -Value $document -Encoding utf8
    Set-Content -LiteralPath $visibleDump -Value $visible -Encoding utf8
    throw (($failures -join [Environment]::NewLine) + [Environment]::NewLine + "buffer dump: $dump" + [Environment]::NewLine + "visible dump: $visibleDump")
}

Write-Host "PASS: Windows Terminal document contains all 72 history rows exactly once."
Write-Host "PASS: oldest and newest history remain reachable through the host text buffer."
Write-Host "PASS: incremental history moved the visible tail while the oldest row entered scrollback."
Write-Host "PASS: prompt and status remain present exactly once."
Write-Host "PASS: committed Markdown is rendered once without raw heading/emphasis/code syntax."
Write-Host "Terminal: process=$($terminalProcess.ProcessName).exe PID=$($terminalDocument.ProcessId) HWND=0x$([Convert]::ToString($terminalDocument.WindowHandle, 16)) title=$($terminalDocument.WindowTitle)"

if ($KeepWindow) {
    Write-Host "Fixture window retained until its hold timeout."
}
