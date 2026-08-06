[CmdletBinding()]
param(
    [ValidateRange(30, 900)]
    [int]$TimeoutSeconds = 300,

    [ValidateRange(10, 180)]
    [int]$StartupTimeoutSeconds = 60,

    [switch]$KeepWindow
)

$ErrorActionPreference = "Stop"
Set-StrictMode -Version 3.0

function Get-OpenCodeApiKey {
    $environmentKey = [string][Environment]::GetEnvironmentVariable("OPENCODE_API_KEY", "Process")
    if (-not [string]::IsNullOrWhiteSpace($environmentKey)) {
        return [pscustomobject]@{ Key = $environmentKey.Trim(); Source = "OPENCODE_API_KEY" }
    }
    $authPath = Join-Path $HOME ".local\share\opencode\auth.json"
    if (-not (Test-Path -LiteralPath $authPath -PathType Leaf)) {
        throw "OpenCode credential unavailable: set OPENCODE_API_KEY or configure opencode auth.json."
    }
    try {
        $auth = Get-Content -LiteralPath $authPath -Raw -Encoding utf8 | ConvertFrom-Json
    } catch {
        throw "OpenCode credential unavailable: auth.json could not be parsed."
    }
    foreach ($entryName in @("opencode-go", "deepseek")) {
        $entry = $auth.PSObject.Properties[$entryName]
        if ($null -eq $entry -or $null -eq $entry.Value) { continue }
        $keyProperty = $entry.Value.PSObject.Properties["key"]
        if ($null -eq $keyProperty) { continue }
        $key = [string]$keyProperty.Value
        if (-not [string]::IsNullOrWhiteSpace($key)) {
            return [pscustomobject]@{ Key = $key.Trim(); Source = "auth.json:$entryName" }
        }
    }
    throw "OpenCode credential unavailable: auth.json has no opencode-go or deepseek key."
}

function Protect-SensitiveText {
    param([AllowNull()][string]$Text, [AllowNull()][string]$Secret)
    if ($null -eq $Text) { return "" }
    if (-not [string]::IsNullOrEmpty($Secret)) {
        return $Text.Replace($Secret, "[REDACTED]")
    }
    return $Text
}

function Write-Utf8NoBom {
    param([Parameter(Mandatory)][string]$Path, [AllowNull()][string]$Value)
    [IO.File]::WriteAllText($Path, [string]$Value, [Text.UTF8Encoding]::new($false))
}

function Remove-SecretArtifact {
    param([AllowNull()][string]$Path)
    if (-not [string]::IsNullOrWhiteSpace($Path) -and (Test-Path -LiteralPath $Path)) {
        Remove-Item -LiteralPath $Path -Force -ErrorAction SilentlyContinue
    }
}

function Scrub-TextArtifacts {
    param([string]$Root, [string]$Secret)
    if ([string]::IsNullOrEmpty($Secret) -or -not (Test-Path -LiteralPath $Root)) { return }
    $extensions = @(".txt", ".log", ".json", ".jsonl", ".yaml", ".yml", ".ps1", ".cmd")
    foreach ($file in Get-ChildItem -LiteralPath $Root -Recurse -File -ErrorAction SilentlyContinue) {
        if ($extensions -notcontains $file.Extension.ToLowerInvariant()) { continue }
        try {
            $content = [IO.File]::ReadAllText($file.FullName)
            if ($content.Contains($Secret)) {
                Write-Utf8NoBom -Path $file.FullName -Value $content.Replace($Secret, "[REDACTED]")
            }
        } catch { }
    }
}

function ConvertTo-PowerShellLiteral {
    param([Parameter(Mandatory)][string]$Value)
    return "'" + $Value.Replace("'", "''") + "'"
}

if ($env:OS -ne "Windows_NT") {
    throw "This E2E requires an interactive Windows desktop."
}

$repoRoot = [IO.Path]::GetFullPath((Join-Path $PSScriptRoot ".."))
$backendDir = Join-Path $repoRoot "backend"
$outputDir = Join-Path $repoRoot "output\aicli-tool-rendering-e2e"
$executable = Join-Path $outputDir "aicli-live-e2e.exe"
$consoleInputWriterPath = Join-Path $PSScriptRoot "write-console-input.ps1"
$runID = [Guid]::NewGuid().ToString("N")
$runRoot = Join-Path $outputDir ("tool-render-" + $runID)
$configPath = Join-Path $runRoot "config.yaml"
$runnerPath = Join-Path $runRoot "run-chat.ps1"
$secretPath = Join-Path $runRoot "credential.bridge"
$sessionDir = Join-Path $runRoot "sessions"
$chatLogDir = Join-Path $runRoot "chat-logs"
$processLog = Join-Path $runRoot "aicli.log"
$exitCodePath = Join-Path $runRoot "runner-exit-code.txt"
$exitInputPath = Join-Path $runRoot "exit-command.txt"
$fullDump = Join-Path $runRoot "uia-document-full.txt"
$visibleDump = Join-Path $runRoot "uia-visible-ranges.txt"
$runningDump = Join-Path $runRoot "uia-running-midflight.txt"
$manifestPath = Join-Path $runRoot "manifest.json"
$startupSentinel = "AICLI-TOOL-RENDER-READY-" + $runID
$runnerExitSentinel = "AICLI-TOOL-RENDER-EXIT-" + $runID + "-"
$windowTitle = "aicli-tool-render-" + $runID.Substring(0, 12)
$expectedProvider = "opencode.ai"
$expectedModel = "deepseek-v4-flash"
$expectedReasoningEffort = "high"

# 触发一次真实 bash 工具调用后即结束；禁止继续调用其他工具。
# 用快命令（git status，亚秒级）复现用户手动会话的观察：工具执行极短，
# Running 状态可见窗口小；若 ActiveBand 行仍被抓拍到，说明渲染链路正常，
# 缺口在于 transcript 里的 Running 标记。
$testPrompt = "调用 bash 工具执行一次命令：git status。执行完成后只回复两个字：完成。不要调用任何其他工具，不要输出任何其他内容。"
Write-Host "Prompt: $testPrompt"

New-Item -ItemType Directory -Path $runRoot, $sessionDir, $chatLogDir -Force | Out-Null
if (-not (Test-Path -LiteralPath $consoleInputWriterPath -PathType Leaf)) {
    throw "Console input writer helper is missing: $consoleInputWriterPath"
}
Write-Utf8NoBom -Path $fullDump -Value ""
Write-Utf8NoBom -Path $visibleDump -Value ""

$credential = Get-OpenCodeApiKey
$apiKey = [string]$credential.Key
$credentialSource = [string]$credential.Source
Write-Utf8NoBom -Path $secretPath -Value $apiKey

$configYaml = @'
aicli:
  chat:
    stream: true
    terminal_title:
      enabled: false
  mcp:
    auto_connect: false
providers:
  default_provider: opencode.ai
  max_retries: 0
  headers: {}
  items:
    opencode.ai:
      enabled: true
      protocol: openai
      base_url: https://opencode.ai/zen/go
      api_path: /v1/chat/completions
      api_key: ${AICLI_TERMINAL_E2E_API_KEY}
      compatibility:
        profile: opencode-console-go-2026-07
      default_model: deepseek-v4-flash
      supported_models:
        - deepseek-v4-flash
      model_capabilities:
        deepseek-v4-flash:
          reasoning_model: true
          reasoning_efforts:
            - high
            - max
          max_tokens: 4096
      max_tokens_limit: 4096
      timeout: 300s
'@
Write-Utf8NoBom -Path $configPath -Value ($configYaml.Trim() + [Environment]::NewLine)

$secretLiteral = ConvertTo-PowerShellLiteral $secretPath
$exeLiteral = ConvertTo-PowerShellLiteral $executable
$configLiteral = ConvertTo-PowerShellLiteral $configPath
$processLogLiteral = ConvertTo-PowerShellLiteral $processLog
$chatLogLiteral = ConvertTo-PowerShellLiteral $chatLogDir
$sessionLiteral = ConvertTo-PowerShellLiteral $sessionDir
$exitCodeLiteral = ConvertTo-PowerShellLiteral $exitCodePath
$startupLiteral = ConvertTo-PowerShellLiteral $startupSentinel
$runnerExitLiteral = ConvertTo-PowerShellLiteral $runnerExitSentinel
$runIDLiteral = ConvertTo-PowerShellLiteral $runID
$requestTimeoutLiteral = ConvertTo-PowerShellLiteral ($TimeoutSeconds.ToString() + "s")
$promptLiteral = ConvertTo-PowerShellLiteral $testPrompt

# 注意：这里不传 --disable-tools —— 本测试的目的就是验证工具执行过程渲染。
$runner = @"
`$ErrorActionPreference = 'Stop'
[Console]::OutputEncoding = [Text.UTF8Encoding]::new(`$false)
try { [Console]::InputEncoding = [Text.UTF8Encoding]::new(`$false) } catch {}
`$exitCode = 1
`$secret = ''
try {
    `$secret = [IO.File]::ReadAllText($secretLiteral).Trim()
    Remove-Item -LiteralPath $secretLiteral -Force -ErrorAction SilentlyContinue
    if ([string]::IsNullOrWhiteSpace(`$secret)) { throw 'credential bridge was empty' }
    `$env:AICLI_TERMINAL_E2E_API_KEY = `$secret
    Write-Output $startupLiteral
    `$chatArguments = @(
        '--config', $configLiteral,
        '--logfile', $processLogLiteral,
        'chat',
        '--provider', 'opencode.ai',
        '--model', 'deepseek-v4-flash',
        '--reasoning-effort', 'high',
        '--stream',
        '--yolo',
        '--runtime-mode', 'local',
        '--request-timeout', $requestTimeoutLiteral,
        '--session-dir', $sessionLiteral,
        '--log-dir', $chatLogLiteral,
        '--title', $runIDLiteral,
        '--prompt', $promptLiteral
    )
    & $exeLiteral @chatArguments
    `$exitCode = `$LASTEXITCODE
} catch {
    Write-Error 'aicli tool-rendering E2E runner failed.'
} finally {
    `$env:AICLI_TERMINAL_E2E_API_KEY = `$null
    `$secret = `$null
    [IO.File]::WriteAllText($exitCodeLiteral, [string]`$exitCode, [Text.UTF8Encoding]::new(`$false))
    Write-Output ($runnerExitLiteral + [string]`$exitCode)
}
exit `$exitCode
"@
Write-Utf8NoBom -Path $runnerPath -Value $runner

if (-not ("AicliLiveTerminalAutomation" -as [type])) {
    Add-Type -ReferencedAssemblies UIAutomationClient, UIAutomationTypes -TypeDefinition @'
using System;
using System.Runtime.InteropServices;
using System.Text;
using System.Windows.Automation;

public sealed class AicliLiveTerminalAutomationSnapshot
{
    public long WindowHandle;
    public int ProcessId;
    public string WindowTitle;
    public string AllText;
    public string VisibleText;
}

public static class AicliLiveTerminalAutomation
{
    private delegate bool EnumWindowsProc(IntPtr hWnd, IntPtr lParam);

    [DllImport("user32.dll")]
    private static extern bool EnumWindows(EnumWindowsProc callback, IntPtr lParam);

    [DllImport("user32.dll")]
    private static extern bool IsWindowVisible(IntPtr hWnd);

    [DllImport("user32.dll")]
    private static extern uint GetWindowThreadProcessId(IntPtr hWnd, out uint processId);

    [DllImport("user32.dll", CharSet = CharSet.Unicode)]
    private static extern int GetClassName(IntPtr hWnd, StringBuilder className, int maxCount);

    private static bool IsWindowsTerminalWindow(IntPtr hWnd, out int processId)
    {
        processId = 0;
        if (hWnd == IntPtr.Zero || !IsWindowVisible(hWnd)) return false;
        uint pid;
        GetWindowThreadProcessId(hWnd, out pid);
        if (pid == 0) return false;
        StringBuilder className = new StringBuilder(256);
        if (GetClassName(hWnd, className, className.Capacity) == 0) return false;
        if (className.ToString().IndexOf("CASCADIA", StringComparison.OrdinalIgnoreCase) < 0) return false;
        processId = (int)pid;
        return true;
    }

    private static AutomationElement FindTextElement(AutomationElement root, string needle, out string allText)
    {
        allText = null;
        AutomationElement bestElement = null;
        int bestLength = -1;
        AutomationElementCollection elements = root.FindAll(TreeScope.Subtree, Condition.TrueCondition);
        foreach (AutomationElement element in elements)
        {
            try
            {
                object candidate;
                if (!element.TryGetCurrentPattern(TextPattern.Pattern, out candidate)) continue;
                TextPattern pattern = (TextPattern)candidate;
                string text = pattern.DocumentRange.GetText(-1) ?? string.Empty;
                if (text.IndexOf(needle, StringComparison.Ordinal) < 0 || text.Length <= bestLength) continue;
                bestElement = element;
                bestLength = text.Length;
                allText = text;
            }
            catch (ElementNotAvailableException) { }
            catch (InvalidOperationException) { }
            catch (COMException) { }
        }
        return bestElement;
    }

    private static AicliLiveTerminalAutomationSnapshot CaptureCore(IntPtr hWnd, int processId, string needle)
    {
        try
        {
            AutomationElement root = AutomationElement.FromHandle(hWnd);
            if (root == null) return null;
            string allText;
            AutomationElement element = FindTextElement(root, needle, out allText);
            if (element == null) return null;

            object candidate;
            if (!element.TryGetCurrentPattern(TextPattern.Pattern, out candidate)) return null;
            TextPattern pattern = (TextPattern)candidate;
            StringBuilder visible = new StringBuilder();
            foreach (var range in pattern.GetVisibleRanges())
            {
                visible.Append(range.GetText(-1));
            }

            string title = string.Empty;
            try { title = root.Current.Name ?? string.Empty; } catch { }
            return new AicliLiveTerminalAutomationSnapshot
            {
                WindowHandle = hWnd.ToInt64(),
                ProcessId = processId,
                WindowTitle = title,
                AllText = allText ?? string.Empty,
                VisibleText = visible.ToString()
            };
        }
        catch (ElementNotAvailableException) { return null; }
        catch (InvalidOperationException) { return null; }
        catch (COMException) { return null; }
    }

    public static AicliLiveTerminalAutomationSnapshot Find(string needle)
    {
        if (string.IsNullOrEmpty(needle)) throw new ArgumentException("needle is required", "needle");
        AicliLiveTerminalAutomationSnapshot best = null;
        EnumWindows(delegate(IntPtr hWnd, IntPtr ignored)
        {
            int processId;
            if (!IsWindowsTerminalWindow(hWnd, out processId)) return true;
            AicliLiveTerminalAutomationSnapshot snapshot = CaptureCore(hWnd, processId, needle);
            if (snapshot != null && (best == null || snapshot.AllText.Length > best.AllText.Length)) best = snapshot;
            return true;
        }, IntPtr.Zero);
        return best;
    }

    public static AicliLiveTerminalAutomationSnapshot Capture(long windowHandle, string needle)
    {
        IntPtr hWnd = new IntPtr(windowHandle);
        int processId;
        if (!IsWindowsTerminalWindow(hWnd, out processId)) return null;
        return CaptureCore(hWnd, processId, needle);
    }
}
'@
}

function Save-TerminalSnapshot {
    param($Snapshot, [string]$FullPath, [string]$VisiblePath, [AllowNull()][string]$Secret)
    if ($null -eq $Snapshot) { return }
    Write-Utf8NoBom -Path $FullPath -Value (Protect-SensitiveText ([string]$Snapshot.AllText) $Secret)
    Write-Utf8NoBom -Path $VisiblePath -Value (Protect-SensitiveText ([string]$Snapshot.VisibleText) $Secret)
}

$assertionFailures = [Collections.Generic.List[string]]::new()
$testError = $null
$snapshot = $null
$snapshotEvidence = $null
$exitWasSent = $false
$requestCompleted = $false
$completedAt = $null
$runningCaptured = $false
$durationRendered = $false
$activeBandRunningRendered = $false

try {
    $goCommand = Get-Command go -ErrorAction Stop
    $wtCommand = Get-Command wt.exe -ErrorAction Stop
    $shellCommand = Get-Command pwsh.exe -ErrorAction SilentlyContinue
    if ($null -eq $shellCommand) {
        $shellCommand = Get-Command powershell.exe -ErrorAction Stop
    }

    Push-Location $backendDir
    try {
        & $goCommand.Source build -trimpath -o $executable ./cmd/aicli
        if ($LASTEXITCODE -ne 0) {
            throw "go build failed with exit code $LASTEXITCODE."
        }
    } finally {
        Pop-Location
    }

    $wtArguments = @(
        "-w", "new",
        "--size", "100,24",
        "new-tab",
        "--title", $windowTitle,
        "--suppressApplicationTitle",
        "-d", $backendDir,
        $shellCommand.Source,
        "-NoLogo",
        "-NoProfile",
        "-ExecutionPolicy", "Bypass",
        "-File", $runnerPath
    )
    & $wtCommand.Source @wtArguments
    if ($LASTEXITCODE -ne 0) {
        throw "wt.exe failed to launch the isolated terminal window (exit code $LASTEXITCODE)."
    }

    $startupDeadline = (Get-Date).AddSeconds($StartupTimeoutSeconds)
    while ((Get-Date) -lt $startupDeadline) {
        $snapshot = [AicliLiveTerminalAutomation]::Find($startupSentinel)
        if ($null -ne $snapshot) { break }
        if (Test-Path -LiteralPath $exitCodePath) { break }
        Start-Sleep -Milliseconds 250
    }
    if ($null -eq $snapshot) {
        throw "Windows Terminal UIA TextPattern did not expose the startup sentinel within $StartupTimeoutSeconds seconds."
    }

    $terminalHandle = [long]$snapshot.WindowHandle
    $terminalPID = [int]$snapshot.ProcessId
    $terminalTitle = [string]$snapshot.WindowTitle
    $terminalProcessName = [string](Get-Process -Id $terminalPID -ErrorAction Stop).ProcessName
    if (-not [string]::Equals($terminalProcessName, "WindowsTerminal", [StringComparison]::OrdinalIgnoreCase)) {
        throw "UI Automation window belongs to process '$terminalProcessName', expected WindowsTerminal.exe."
    }

    # 轮询直到会话完成（assistant 回复"完成"）或 runner 退出。
    $responseDeadline = (Get-Date).AddSeconds($TimeoutSeconds)
    $stableFingerprint = ""
    $stableSamples = 0
    $runningSnapshot = $null
    $runningShotsDir = Join-Path $runRoot "running-shots"
    New-Item -ItemType Directory -Force -Path $runningShotsDir | Out-Null
    $runningShotIndex = 0
    while ((Get-Date) -lt $responseDeadline) {
        $candidate = [AicliLiveTerminalAutomation]::Capture($terminalHandle, $startupSentinel)
        if ($null -ne $candidate) {
            $snapshot = $candidate
            $document = [string]$candidate.AllText
            # 执行中抓拍：工具执行阶段（出现 "Running" 字样）的每个轮询
            # 快照都保存到 running-shots/，用于验证 ActiveBand 的
            # "• Running <命令>" 行真实画到了 Windows Terminal。工具执行
            # 时间很短（秒级），多张快照能覆盖 begin/end 两侧。
            if ($document.Contains("Running")) {
                if ($null -eq $runningSnapshot) {
                    $runningSnapshot = $candidate
                    Write-Utf8NoBom -Path $runningDump -Value ("captured_at=" + (Get-Date).ToString("o") + [Environment]::NewLine + [string]$candidate.AllText)
                }
                $runningShotIndex++
                Write-Utf8NoBom -Path (Join-Path $runningShotsDir ("shot-{0:D3}.txt" -f $runningShotIndex)) -Value ("captured_at=" + (Get-Date).ToString("o") + [Environment]::NewLine + [string]$candidate.AllText)
            }
            # 会话完成的可靠信号：事件日志里出现 assistant_delta（assistant
            # 正文开始流式）。reasoning summary 里可能复述指令（含"完成"），
            # 直接用屏幕文本判断会提前结束，快照全部落在工具执行之前。
            $assistantDeltaLogged = $false
            if (-not $assistantDeltaLogged -and (Test-Path -LiteralPath $chatLogDir)) {
                $evtLog = Get-ChildItem -Recurse -Filter runtime-events.jsonl -LiteralPath $chatLogDir -ErrorAction SilentlyContinue | Select-Object -First 1
                if ($null -ne $evtLog -and (Select-String -LiteralPath $evtLog.FullName -Pattern '"assistant_delta"' -SimpleMatch -Quiet -ErrorAction SilentlyContinue)) {
                    $assistantDeltaLogged = $true
                }
            }
            $sessionDone = $assistantDeltaLogged
            if ($sessionDone) {
                $fingerprint = [string]$candidate.AllText + [char]0 + [string]$candidate.VisibleText
                if ($fingerprint -ceq $stableFingerprint) {
                    $stableSamples++
                } else {
                    $stableFingerprint = $fingerprint
                    $stableSamples = 1
                }
                if ($stableSamples -ge 3) {
                    $completedAt = Get-Date
                    $requestCompleted = $true
                    break
                }
            } else {
                $stableFingerprint = ""
                $stableSamples = 0
            }
        }
        if (Test-Path -LiteralPath $exitCodePath) { break }
        Start-Sleep -Milliseconds 100
    }

    $final = [AicliLiveTerminalAutomation]::Capture($terminalHandle, $startupSentinel)
    if ($null -ne $final) { $snapshot = $final }
    Save-TerminalSnapshot $snapshot $fullDump $visibleDump $apiKey

    $document = [string]$snapshot.AllText
    $visible = [string]$snapshot.VisibleText
    $failures = [Collections.Generic.List[string]]::new()

    if (-not $requestCompleted) {
        $failures.Add("Timed out before the tool call completed and the assistant replied (稳定快照未出现)。")
    }

    # 断言 1：渲染中不得出现字面 "tool.reduced"（本修复的核心回归点）。
    $reducedLeak = $document.IndexOf("tool.reduced", [StringComparison]::Ordinal) -ge 0
    if ($reducedLeak) {
        $failures.Add("可见 transcript 泄漏了内部遥测事件字面 'tool.reduced'。")
    }

    # 断言 2：工具执行过程应渲染 —— 工具名与执行结果应出现在 transcript。
    $toolRendered = $document.Contains("bash")
    $outputRendered = $document.Contains("Exit code: 0")
    if (-not $toolRendered) {
        $failures.Add("工具执行过程未渲染：transcript 中找不到工具名 'bash'。")
    }
    if (-not $outputRendered) {
        $failures.Add("工具输出未渲染：transcript 中找不到 'Exit code: 0'。")
    }

    # 断言 2b：用户输入 prompt 本身应渲染为 user cell（首次提交时 bridge
    # 尚未创建，需由 RenderSubmittedUserInput 补齐；否则第一条消息丢失）。
    $userPromptRendered = $document.Contains("调用 bash 工具执行一次命令")
    if (-not $userPromptRendered) {
        $failures.Add("用户输入 prompt 未渲染为 user cell：transcript 中找不到 prompt 原文。")
    }

    # 断言 3：调用后标题应带时长后缀（对齐旧 compactToolCompletionTitle 的
    # "• Completed ... in 5ms" 细节）。enrichRuntimeToolDuration 会在 Scene
    # 数据面编码前注入 duration_ms，真实会话里应渲染出 " in "。
    $durationRendered = $document.Contains("• Completed") -and $document.Contains(" in ")
    if (-not $durationRendered) {
        $failures.Add("调用后标题未渲染时长后缀（期望 '• Completed ... in ...'）。")
    }

    # 断言 4（观察项，不强制）：执行中 "Running" 行抓拍。慢命令（ping -n 4
    # 约 3 秒）为 ActiveBand 的 "• Running ..." 提供了可见窗口；若一次都
    # 没抓到，说明执行中状态没有画到终端（ActiveBand 渲染问题）。
    $runningCaptured = $null -ne $runningSnapshot
    if (-not $runningCaptured) {
        Write-Host "WARN: 未抓拍到执行中 'Running' 快照（工具可能执行过快或 ActiveBand 未渲染）。"
    }

    # 断言 5：至少一张执行中快照应包含 ActiveBand 的 "• Running <命令>" 行
    # （命令摘要画到了终端），而不只是 status 行的 "◦ Running shell"。
    $activeBandRunningRendered = $false
    if (Test-Path -LiteralPath $runningShotsDir) {
        $runningShots = Get-ChildItem -LiteralPath $runningShotsDir -Filter "shot-*.txt" -ErrorAction SilentlyContinue
        foreach ($shotFile in $runningShots) {
            $shotText = [IO.File]::ReadAllText($shotFile.FullName)
            if ($shotText.Contains("• Running")) {
                $activeBandRunningRendered = $true
                break
            }
        }
    }
    if (-not $activeBandRunningRendered) {
        $failures.Add("ActiveBand 的 '• Running <命令>' 行未出现在任何执行中快照（status 行 '◦ Running shell' 不等于 ActiveBand 命令摘要行）。")
    }

    Write-Host "=== tool.reduced leak: $reducedLeak | bash rendered: $toolRendered | hello rendered: $outputRendered | duration rendered: $durationRendered | running mid-flight: $runningCaptured | activeband running row: $activeBandRunningRendered ==="
    foreach ($failure in $failures) { $assertionFailures.Add($failure) }
    if ($failures.Count -gt 0) {
        throw (($failures -join "; ") + ".")
    }
} catch {
    $caughtError = Protect-SensitiveText ([string]$_.Exception.Message) $apiKey
    if ($terminalHandle -ne 0) {
        try {
            $final = [AicliLiveTerminalAutomation]::Capture($terminalHandle, $startupSentinel)
            if ($null -ne $final) { $snapshot = $final }
        } catch {
            $snapshotEvidenceError = Protect-SensitiveText ([string]$_.Exception.Message) $apiKey
        }
    }
    Save-TerminalSnapshot $snapshot $fullDump $visibleDump $apiKey
    $testError = $caughtError
} finally {
    Remove-SecretArtifact $secretPath

    if (-not $exitWasSent) {
        try {
            $expectedExecutable = [IO.Path]::GetFullPath($executable)
            if (Test-Path -LiteralPath $exitCodePath) {
                $exitWasSent = $true
            } else {
                $testProcesses = @(Get-CimInstance Win32_Process -ErrorAction Stop | Where-Object {
                    -not [string]::IsNullOrWhiteSpace([string]$_.ExecutablePath) -and
                    [string]::Equals([IO.Path]::GetFullPath([string]$_.ExecutablePath), $expectedExecutable, [StringComparison]::OrdinalIgnoreCase) -and
                    ([string]$_.CommandLine).Contains($runID)
                })
                if ($testProcesses.Count -ge 1) {
                    $exitTargetProcessID = [int]$testProcesses[0].ProcessId
                    Write-Utf8NoBom -Path $exitInputPath -Value "/exit"
                    $inputShell = Get-Command pwsh.exe -ErrorAction SilentlyContinue
                    if ($null -eq $inputShell) {
                        $inputShell = Get-Command powershell.exe -ErrorAction Stop
                    }
                    $helperOutput = @(& $inputShell.Source -NoLogo -NoProfile -NonInteractive -ExecutionPolicy Bypass `
                        -File $consoleInputWriterPath -TargetProcessId $exitTargetProcessID -TextPath $exitInputPath 2>&1)
                    $helperExitCode = [int]$LASTEXITCODE
                    if ($helperExitCode -eq 0) { $exitWasSent = $true }
                }
            }
        } catch {
            $exitSendError = Protect-SensitiveText ([string]$_.Exception.Message) $apiKey
        }
    }

    $runnerExitCode = -1
    $runnerExited = $false
    if (Test-Path -LiteralPath $exitCodePath) {
        try {
            $runnerExitCode = [int]([IO.File]::ReadAllText($exitCodePath).Trim())
            $runnerExited = $true
        } catch { }
    }
    if (-not $runnerExited) {
        $deadline = (Get-Date).AddSeconds(30)
        while ((Get-Date) -lt $deadline -and -not $runnerExited) {
            Start-Sleep -Milliseconds 500
            if (Test-Path -LiteralPath $exitCodePath) {
                try {
                    $runnerExitCode = [int]([IO.File]::ReadAllText($exitCodePath).Trim())
                    $runnerExited = $true
                } catch { }
            }
        }
    }

    Scrub-TextArtifacts -Root $runRoot -Secret $apiKey
    $credential = $null

    $manifest = [ordered]@{
        schema_version = 1
        test = "aicli-tool-rendering-e2e"
        run_id = $runID
        started_at = $null
        completed_at = $(if ($null -ne $completedAt) { $completedAt.ToString("o") } else { $null })
        passed = ($null -eq $testError -and $assertionFailures.Count -eq 0)
        request_completed = $requestCompleted
        tool_reduced_leak = $reducedLeak
        tool_rendered = $toolRendered
        tool_output_rendered = $outputRendered
        completed_duration_rendered = $durationRendered
        running_midflight_captured = $runningCaptured
        activeband_running_row_rendered = $activeBandRunningRendered
        runner_exit_code = $runnerExitCode
        runner_exited = $runnerExited
        assertions_failed = @($assertionFailures)
        credential_source = $credentialSource
        error = $testError
        artifacts = @{
            full_dump = $fullDump
            visible_dump = $visibleDump
            running_dump = $runningDump
            running_shots = $runningShotsDir
            chat_logs = $chatLogDir
            process_log = $processLog
        }
    }
    Write-Utf8NoBom -Path $manifestPath -Value ($manifest | ConvertTo-Json -Depth 8)

    Write-Host "Manifest: $manifestPath"
    Write-Host "PASS: $($manifest.passed) | tool.reduced leak: $reducedLeak | tool rendered: $toolRendered | output rendered: $outputRendered | runner exit: $runnerExitCode"
}

if (-not $KeepWindow -and $exitWasSent) {
    Start-Sleep -Seconds 2
    try {
        $windowProcess = Get-Process -Id $terminalPID -ErrorAction SilentlyContinue
        if ($null -ne $windowProcess) {
            $windowProcess.CloseMainWindow() | Out-Null
        }
    } catch { }
}

if ($null -ne $testError -or $assertionFailures.Count -gt 0) {
    $errorText = "Tool-rendering E2E FAILED: " + $testError
    if ($assertionFailures.Count -gt 0) {
        $errorText += " [assertions: " + (($assertionFailures | ForEach-Object { [string]$_ }) -join "; ") + "]"
    }
    throw $errorText
}
Write-Host "Tool-rendering E2E PASS."
