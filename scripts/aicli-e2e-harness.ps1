# aicli E2E 观测工具集（被 test-aicli-*.ps1 dot-source 复用）
#
# 用法：
#   . (Join-Path $PSScriptRoot 'aicli-e2e-harness.ps1')
#
# 提供四类能力（对应 docs/e2e/harness-observability.md 的"数据采集/接口/E2E 流程"三项优化）：
#   A1  Get-AicliTimelineSample / Start-AicliTimeline   时序采样（timeline.jsonl）
#   A2  Save-AicliDiagnostics                           失败自动诊断包
#   A3  Wait-AicliScreenStable                          稳态判据（连续 N 次不变）
#   B4  Test-AicliEndpointCoverage                      清单↔断言对齐门禁
#   C4  Get-AicliUiaScreenText                          物理终端帧（UIA 读窗口）
#
# 设计约束：本文件不定义 Add-Result / Write-Log 之类的宿主函数，也不读写宿主
# 变量；所有状态通过参数进出，便于任意脚本 dot-source。

Set-StrictMode -Off

# ---------------------------------------------------------------------------
# HTTP 取值（UTF-8 字节收发，避免 Windows PowerShell 代码页把中文解坏）
# ---------------------------------------------------------------------------

# Invoke-HarnessRequest：返回 @{ ok; status_code; text; json; ms; error }。
# 绝不抛异常：采样器必须能在会话繁忙/端点超时时继续产出时间线。
function Invoke-HarnessRequest {
    param(
        [Parameter(Mandatory)][string]$Url,
        [int]$TimeoutSec = 10,
        [switch]$AsJson
    )
    $result = [ordered]@{
        ok = $false; status_code = 0; text = $null; json = $null
        ms = 0; error = $null
    }
    $sw = [System.Diagnostics.Stopwatch]::StartNew()
    try {
        $resp = Invoke-WebRequest -Method GET -Uri $Url -TimeoutSec $TimeoutSec -UseBasicParsing
        $result.status_code = [int]$resp.StatusCode
        $text = $null
        try {
            $stream = $resp.RawContentStream
            if ($null -ne $stream) { $text = [System.Text.Encoding]::UTF8.GetString($stream.ToArray()) }
        } catch {
            $text = $null
        }
        if ([string]::IsNullOrEmpty($text)) { $text = [string]$resp.Content }
        $result.text = $text
        $result.ok = $true
        if ($AsJson) {
            try { $result.json = $text | ConvertFrom-Json } catch { $result.json = $null }
        }
    } catch {
        $result.error = $_.Exception.Message
        $statusCode = 0
        try {
            if ($null -ne $_.Exception.Response) { $statusCode = [int]$_.Exception.Response.StatusCode }
        } catch { $statusCode = 0 }
        $result.status_code = $statusCode
    }
    $sw.Stop()
    $result.ms = $sw.ElapsedMilliseconds
    return [pscustomobject]$result
}

# ---------------------------------------------------------------------------
# A1：时序采样
# ---------------------------------------------------------------------------

# Get-AicliTimelineSample 取一帧「状态 + 屏幕」快照，可选追加到 JSONL。
#
# 采样刻意只用 ?fast=1：时间线是高频采样，必须走有界路径（B3），否则采样器
# 自己会成为会话风暴的一部分。字段选择围绕"空屏/卡死"的判据：
#   - app_state_available 区分「没有渲染器」与「渲染器正常」；
#   - history_next / history_pending 区分「从未规划」与「有规划不可投递」；
#   - screen_lines / screen_hash 是物理投影的规模与稳定性指纹。
function Get-AicliTimelineSample {
    param(
        [Parameter(Mandatory)][string]$BaseUrl,
        [string]$TimelinePath,
        [string]$Tag = '',
        [int]$TimeoutSec = 10
    )
    $t = [DateTimeOffset]::UtcNow.ToUnixTimeMilliseconds()
    $status = Invoke-HarnessRequest -Url "$BaseUrl/debug/chat/status?fast=1" -TimeoutSec $TimeoutSec -AsJson
    $screen = Invoke-HarnessRequest -Url "$BaseUrl/debug/chat/screen?format=text" -TimeoutSec $TimeoutSec

    $screenText = ''
    if ($screen.ok -and $null -ne $screen.text) { $screenText = [string]$screen.text }
    $screenLines = 0
    $screenChars = 0
    $screenHash = ''
    if ($screenText.Length -gt 0) {
        $screenChars = $screenText.Length
        $screenLines = ($screenText -split "`n").Count
        $sha = [System.Security.Cryptography.SHA1]::Create()
        try {
            $bytes = [System.Text.Encoding]::UTF8.GetBytes($screenText)
            $screenHash = ([System.BitConverter]::ToString($sha.ComputeHash($bytes)) -replace '-', '').Substring(0, 16).ToLowerInvariant()
        } finally { $sha.Dispose() }
    }

    $sample = [ordered]@{
        t_ms               = $t
        tag                = $Tag
        status_ok          = [bool]$status.ok
        status_ms          = [int]$status.ms
        status_error       = $status.error
        screen_ok          = [bool]$screen.ok
        screen_ms          = [int]$screen.ms
        screen_error       = $screen.error
        screen_lines       = [int]$screenLines
        screen_chars       = [int]$screenChars
        screen_hash        = $screenHash
        session_available  = $null
        app_state_available = $null
        app_state_reason   = $null
        revision           = $null
        layout_generation  = $null
        scene_cells        = $null
        encode_count       = $null
        append_count       = $null
        output_state       = $null
        primary_committed  = $null
        last_sequence      = $null
        abandoned          = $null
        history_effects    = $null
        history_next       = $null
        history_pending    = $null
        plan_transcript_cells = $null
        plan_app_state_cells  = $null
        plan_frontier_cells   = $null
        plan_layout_probed    = $null
        plan_layout_rows      = $null
        skipped_sections   = $null
    }

    $doc = $status.json
    if ($status.ok -and $null -ne $doc) {
        $sample.session_available = $doc.available
        if ($null -ne $doc.app_state) {
            $sample.app_state_available = $doc.app_state.available
            $sample.app_state_reason = $doc.app_state.reason
            $sample.revision = $doc.app_state.revision
            $sample.layout_generation = $doc.app_state.layout_generation
            $sample.history_effects = $doc.app_state.history_effects
            if ($null -ne $doc.app_state.plan) {
                $sample.plan_transcript_cells = $doc.app_state.plan.transcript_cells
                $sample.plan_app_state_cells = $doc.app_state.plan.app_state_cells
                $sample.plan_frontier_cells = $doc.app_state.plan.frontier_cells
                $sample.plan_layout_probed = $doc.app_state.plan.layout_probed
                $sample.plan_layout_rows = $doc.app_state.plan.layout_rows_budgeted
            }
        }
        if ($null -ne $doc.scene) {
            $sample.scene_cells = $doc.scene.cells
        }
        if ($null -ne $doc.render_encoder) {
            $sample.encode_count = $doc.render_encoder.encode_count
            $sample.append_count = $doc.render_encoder.append_count
        }
        if ($null -ne $doc.render_output) {
            $sample.output_state = $doc.render_output.state
            $sample.primary_committed = $doc.render_output.primary_committed
            $sample.last_sequence = $doc.render_output.last_sequence
            $sample.abandoned = $doc.render_output.abandoned
        }
        if ($null -ne $doc.skipped_sections) {
            $sample.skipped_sections = ($doc.skipped_sections -join ',')
        }
        # history_effects 是 "pending=.. in-flight=.. next=.. epoch=.." 形式的
        # 稳定字符串；解析出 next/pending 供稳定性判据与断言使用。
        if ($null -ne $sample.history_effects) {
            $m = [regex]::Match([string]$sample.history_effects, 'pending=(\d+).*next=(\d+)')
            if ($m.Success) {
                $sample.history_pending = [int]$m.Groups[1].Value
                $sample.history_next = [int]$m.Groups[2].Value
            }
        }
    }

    $record = [pscustomobject]$sample
    if (-not [string]::IsNullOrWhiteSpace($TimelinePath)) {
        $line = ($record | ConvertTo-Json -Depth 6 -Compress)
        Add-Content -LiteralPath $TimelinePath -Value $line -Encoding UTF8
    }
    return $record
}

# Start-AicliTimeline 以固定间隔在后台采样，返回 job；调用方用
# Stop-AicliTimeline 收尾。后台 job 独立进程空间，因此这里内联采样逻辑而
# 不 dot-source 本文件（job 看不到宿主作用域的函数）。
function Start-AicliTimeline {
    param(
        [Parameter(Mandatory)][string]$BaseUrl,
        [Parameter(Mandatory)][string]$TimelinePath,
        [int]$IntervalMs = 500,
        [string]$Tag = 'background',
        [string]$HarnessPath
    )
    $job = Start-Job -ScriptBlock {
        param($BaseUrl, $TimelinePath, $IntervalMs, $Tag, $HarnessPath)
        . $HarnessPath
        while ($true) {
            Get-AicliTimelineSample -BaseUrl $BaseUrl -TimelinePath $TimelinePath -Tag $Tag -TimeoutSec 10 | Out-Null
            Start-Sleep -Milliseconds $IntervalMs
        }
    } -ArgumentList $BaseUrl, $TimelinePath, $IntervalMs, $Tag, $HarnessPath
    return $job
}

function Stop-AicliTimeline {
    param($Job)
    if ($null -ne $Job) {
        Stop-Job -Job $Job -ErrorAction SilentlyContinue
        Remove-Job -Job $Job -Force -ErrorAction SilentlyContinue
    }
}

# ---------------------------------------------------------------------------
# A3：稳态判据
# ---------------------------------------------------------------------------

# Wait-AicliScreenStable 等待屏幕连续 $StableSamples 次采样指纹不变，且行数
# 不低于 $MinLines。返回 @{ stable; samples; last; reason }。
#
# 替代固定 sleep：固定 sleep 在慢机器上假失败、在快机器上白等；稳态判据把
# "渲染完成"变成可观测事实（指纹不变 = 没有新帧写入）。
function Wait-AicliScreenStable {
    param(
        [Parameter(Mandatory)][string]$BaseUrl,
        [int]$StableSamples = 3,
        [int]$MinLines = 1,
        [int]$TimeoutSec = 60,
        [int]$IntervalMs = 400,
        [string]$TimelinePath,
        [string]$Tag = 'stable-wait'
    )
    $deadline = (Get-Date).AddSeconds($TimeoutSec)
    $lastHash = ''
    $stableCount = 0
    $samples = 0
    $last = $null
    $reason = 'timeout'
    while ((Get-Date) -lt $deadline) {
        $last = Get-AicliTimelineSample -BaseUrl $BaseUrl -TimelinePath $TimelinePath -Tag $Tag
        $samples++
        if ($last.screen_hash -ne '' -and $last.screen_hash -eq $lastHash -and $last.screen_lines -ge $MinLines) {
            $stableCount++
            if ($stableCount -ge $StableSamples) { $reason = 'stable'; break }
        } else {
            $stableCount = 0
        }
        $lastHash = $last.screen_hash
        Start-Sleep -Milliseconds $IntervalMs
    }
    return [pscustomobject]@{
        stable  = ($reason -eq 'stable')
        samples = $samples
        last    = $last
        reason  = $reason
    }
}

# ---------------------------------------------------------------------------
# A2：失败诊断包
# ---------------------------------------------------------------------------

# Save-AicliDiagnostics 把"一次失败现场"需要的全部端点落到 $DiagDir：
# goroutine dump / executor / status(JSON+text) / screen / endpoints / 时间线副本。
# 每个文件独立 try/catch，单点失败不影响其余取证。
function Save-AicliDiagnostics {
    param(
        [Parameter(Mandatory)][string]$BaseUrl,
        [Parameter(Mandatory)][string]$DiagDir,
        [string]$Reason = '',
        [string]$TimelinePath,
        [int]$TimeoutSec = 20
    )
    New-Item -ItemType Directory -Path $DiagDir -Force | Out-Null
    $index = New-Object System.Collections.Generic.List[object]

    $captures = @(
        @{ name = 'goroutine.txt'; url = "$BaseUrl/debug/pprof/goroutine?debug=2"; kind = 'text' },
        @{ name = 'executor.json'; url = "$BaseUrl/debug/pprof/executor"; kind = 'text' },
        @{ name = 'status.json'; url = "$BaseUrl/debug/chat/status"; kind = 'text' },
        @{ name = 'status-fast.json'; url = "$BaseUrl/debug/chat/status?fast=1"; kind = 'text' },
        @{ name = 'status.txt'; url = "$BaseUrl/debug/chat/status?format=text"; kind = 'text' },
        @{ name = 'screen.txt'; url = "$BaseUrl/debug/chat/screen?format=text"; kind = 'text' },
        @{ name = 'endpoints.json'; url = "$BaseUrl/debug/endpoints"; kind = 'text' }
    )
    foreach ($c in $captures) {
        $r = Invoke-HarnessRequest -Url $c.url -TimeoutSec $TimeoutSec
        $path = Join-Path $DiagDir $c.name
        if ($r.ok -and $null -ne $r.text) {
            Set-Content -LiteralPath $path -Value $r.text -Encoding UTF8
        } else {
            Set-Content -LiteralPath $path -Value ("capture failed: " + [string]$r.error) -Encoding UTF8
        }
        $index.Add([pscustomobject]@{
            file = $c.name; url = $c.url; ok = [bool]$r.ok
            ms = [int]$r.ms; error = $r.error
        })
    }
    if (-not [string]::IsNullOrWhiteSpace($Reason)) {
        Set-Content -LiteralPath (Join-Path $DiagDir 'reason.txt') -Value $Reason -Encoding UTF8
    }
    if (-not [string]::IsNullOrWhiteSpace($TimelinePath) -and (Test-Path -LiteralPath $TimelinePath)) {
        Copy-Item -LiteralPath $TimelinePath -Destination (Join-Path $DiagDir 'timeline.jsonl') -Force
    }
    $index | ConvertTo-Json -Depth 5 | Set-Content -LiteralPath (Join-Path $DiagDir 'index.json') -Encoding UTF8
    return $DiagDir
}

# ---------------------------------------------------------------------------
# B4：清单↔断言对齐门禁
# ---------------------------------------------------------------------------

# Test-AicliEndpointCoverage 检查 /debug/endpoints 清单里的每个端点都被脚本
# 断言覆盖，或在 $Exempt 中显式豁免（豁免必须给出理由，理由为空视为未豁免）。
# 返回 @{ ok; missing; exempt; checked }。
#
# 动机：清单会随代码增长，断言不会。没有这道门禁，新端点在 E2E 里"悄悄没人管"。
function Test-AicliEndpointCoverage {
    param(
        [Parameter(Mandatory)]$EndpointsJson,
        [Parameter(Mandatory)][string[]]$Asserted,
        [hashtable]$Exempt = @{}
    )
    $checked = 0
    $missing = New-Object System.Collections.Generic.List[string]
    $exempted = New-Object System.Collections.Generic.List[string]
    $entries = @()
    if ($null -ne $EndpointsJson -and $null -ne $EndpointsJson.endpoints) {
        $entries = @($EndpointsJson.endpoints)
    }
    foreach ($e in $entries) {
        $path = [string]$e.path
        if ([string]::IsNullOrWhiteSpace($path)) { continue }
        # enabled=false 的端点（pprof 未开启 / 观察平面未启用）不参与门禁：
        # 它们在本进程里根本不可调用，断言无从谈起。
        if ($null -ne $e.PSObject.Properties['enabled'] -and -not [bool]$e.enabled) { continue }
        $checked++
        $covered = $false
        foreach ($a in $Asserted) {
            # 精确命中，或命中同一端点的查询串变体（/x?format=text 视为 /x 已覆盖）。
            if ($path -eq $a -or $path.StartsWith($a + '?')) { $covered = $true; break }
        }
        if ($covered) { continue }
        $reason = $null
        if ($Exempt.ContainsKey($path)) { $reason = [string]$Exempt[$path] }
        if (-not [string]::IsNullOrWhiteSpace($reason)) {
            $exempted.Add(("{0} :: {1}" -f $path, $reason))
            continue
        }
        $missing.Add($path)
    }
    return [pscustomobject]@{
        ok      = ($missing.Count -eq 0)
        missing = @($missing)
        exempt  = @($exempted)
        checked = $checked
    }
}

# ---------------------------------------------------------------------------
# C4：物理终端帧（UIA）
# ---------------------------------------------------------------------------

# 与 scripts/test-aicli-windows-terminal-e2e.ps1 同源的 UIA 读窗口实现。
# 类型名带 Harness 前缀，避免与宿主脚本已加载的类型冲突。
if (-not ("AicliHarnessTerminalAutomation" -as [type])) {
    Add-Type -ReferencedAssemblies UIAutomationClient, UIAutomationTypes -TypeDefinition @'
using System;
using System.Text;
using System.Runtime.InteropServices;
using System.Windows.Automation;

public class AicliHarnessTerminalDocument {
    public long WindowHandle;
    public int ProcessId;
    public string WindowTitle;
    public string All;
    public string Visible;
}

public static class AicliHarnessTerminalAutomation {
    private delegate bool EnumWindowsProc(IntPtr hWnd, IntPtr lParam);

    [DllImport("user32.dll")]
    private static extern bool EnumWindows(EnumWindowsProc callback, IntPtr lParam);

    [DllImport("user32.dll")]
    private static extern bool IsWindowVisible(IntPtr hWnd);

    [DllImport("user32.dll")]
    private static extern uint GetWindowThreadProcessId(IntPtr hWnd, out uint processId);

    [DllImport("user32.dll", CharSet = CharSet.Unicode)]
    private static extern int GetClassName(IntPtr hWnd, StringBuilder className, int maxCount);

    private static bool IsConsoleWindow(IntPtr window, out int processId) {
        processId = 0;
        if (window == IntPtr.Zero || !IsWindowVisible(window)) return false;
        StringBuilder className = new StringBuilder(256);
        if (GetClassName(window, className, className.Capacity) == 0) return false;
        string name = className.ToString();
        if (name.IndexOf("CASCADIA", StringComparison.OrdinalIgnoreCase) < 0 &&
            name.IndexOf("ConsoleWindowClass", StringComparison.OrdinalIgnoreCase) < 0) return false;
        uint pid;
        GetWindowThreadProcessId(window, out pid);
        if (pid == 0) return false;
        processId = (int)pid;
        return true;
    }

    private static AicliHarnessTerminalDocument Capture(IntPtr window, int processId, string expectedTitle) {
        AutomationElement root = AutomationElement.FromHandle(window);
        if (root == null) return null;
        string title = root.Current.Name ?? string.Empty;
        if (!string.IsNullOrEmpty(expectedTitle) &&
            title.IndexOf(expectedTitle, StringComparison.OrdinalIgnoreCase) < 0) return null;
        AutomationElementCollection descendants = root.FindAll(TreeScope.Descendants, Condition.TrueCondition);
        AicliHarnessTerminalDocument best = null;
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
                best = new AicliHarnessTerminalDocument {
                    WindowHandle = window.ToInt64(), ProcessId = processId, WindowTitle = title,
                    All = all, Visible = visible
                };
            }
        }
        return best;
    }

    public static AicliHarnessTerminalDocument FindByTitle(string expectedTitle) {
        AicliHarnessTerminalDocument best = null;
        EnumWindows(delegate(IntPtr window, IntPtr ignored) {
            int processId;
            if (!IsConsoleWindow(window, out processId)) return true;
            try {
                AicliHarnessTerminalDocument candidate = Capture(window, processId, expectedTitle);
                if (candidate != null && (best == null || candidate.All.Length > best.All.Length)) best = candidate;
            } catch (ElementNotAvailableException) { }
              catch (InvalidOperationException) { }
              catch (COMException) { }
            return true;
        }, IntPtr.Zero);
        return best;
    }
}
'@
}

# Get-AicliUiaScreenText 读取物理终端窗口文本（失败返回 $null，绝不抛）。
# C4：失败取证时，HTTP 的 /debug/chat/screen 只是"应然帧"；UIA 读到的才是
# 用户实际看到的帧。两者同时落盘才能区分"渲染器没产出"与"终端没显示"。
function Get-AicliUiaScreenText {
    param([string]$WindowTitle = '')
    try {
        $doc = [AicliHarnessTerminalAutomation]::FindByTitle($WindowTitle)
        if ($null -eq $doc) { return $null }
        return [pscustomobject]@{
            title   = $doc.WindowTitle
            pid     = $doc.ProcessId
            all     = $doc.All
            visible = $doc.Visible
            lines   = ($doc.All -split "`n").Count
        }
    } catch {
        return $null
    }
}

# Save-AicliDualChannelForensics 把"应然帧 + 物理帧"成对落盘（C4）。
# 返回 @{ http; uia; files }；两路都失败时也如实记录，不伪造证据。
function Save-AicliDualChannelForensics {
    param(
        [Parameter(Mandatory)][string]$BaseUrl,
        [Parameter(Mandatory)][string]$DiagDir,
        [string]$WindowTitle = '',
        [string]$Label = 'screen'
    )
    New-Item -ItemType Directory -Path $DiagDir -Force | Out-Null
    $files = New-Object System.Collections.Generic.List[string]

    $http = Invoke-HarnessRequest -Url "$BaseUrl/debug/chat/screen?format=text" -TimeoutSec 20
    $httpPath = Join-Path $DiagDir ("{0}-http-expected.txt" -f $Label)
    if ($http.ok) {
        Set-Content -LiteralPath $httpPath -Value $http.text -Encoding UTF8
    } else {
        Set-Content -LiteralPath $httpPath -Value ("http capture failed: " + [string]$http.error) -Encoding UTF8
    }
    $files.Add($httpPath)

    $uia = Get-AicliUiaScreenText -WindowTitle $WindowTitle
    $uiaPath = Join-Path $DiagDir ("{0}-uia-physical.txt" -f $Label)
    if ($null -ne $uia) {
        Set-Content -LiteralPath $uiaPath -Value $uia.all -Encoding UTF8
    } else {
        Set-Content -LiteralPath $uiaPath -Value "uia capture unavailable (no matching console window)" -Encoding UTF8
    }
    $files.Add($uiaPath)

    return [pscustomobject]@{ http = $http; uia = $uia; files = @($files) }
}
