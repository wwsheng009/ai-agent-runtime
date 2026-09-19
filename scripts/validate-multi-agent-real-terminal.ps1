param(
    [string]$Provider = "mimo_anthropic",
    [string]$AicliPath = "",
    [string]$OutputDir = "docs\working",
    [string]$Model = "",
    [string]$ReasoningEffort = "",
    [string]$ConfigPath = "",
    [int]$RequestTimeoutSeconds = 60,
    [int]$SpawnAgentTimeoutSeconds = 240,
    [int]$SpawnTeamTimeoutSeconds = 300,
    [ValidateSet("easy", "normal", "hard", "expert")]
    [string]$SpawnChildDifficulty = "normal",
    [switch]$SkipBuild,
    [switch]$SkipProviderSmoke,
    [switch]$SkipSpawnAgentProbe,
    [switch]$SkipSpawnTeamProbe
)

$ErrorActionPreference = "Stop"
if (Get-Variable -Name PSNativeCommandUseErrorActionPreference -ErrorAction SilentlyContinue) {
    $PSNativeCommandUseErrorActionPreference = $false
}

# 子进程按 UTF-8 输出；若控制台默认是 GBK，捕获到的中文会变成乱码，报告与模式匹配都会失真。
$previousConsoleOutputEncoding = $null
try {
    $previousConsoleOutputEncoding = [Console]::OutputEncoding
    [Console]::OutputEncoding = New-Object System.Text.UTF8Encoding($false)
} catch {
    $previousConsoleOutputEncoding = $null
}

function Add-ReportLine {
    param(
        [Parameter(Mandatory = $true)][string]$Path,
        [AllowEmptyString()][string]$Text = ""
    )
    Add-Content -Path $Path -Value $Text -Encoding UTF8
}

function Add-ReportBlock {
    param(
        [Parameter(Mandatory = $true)][string]$Path,
        [Parameter(Mandatory = $true)][string]$Language,
        [string]$Text = ""
    )
    Add-ReportLine -Path $Path -Text "``````$Language"
    if ($Text -ne "") {
        Add-ReportLine -Path $Path -Text $Text
    }
    Add-ReportLine -Path $Path -Text "``````"
}

function Get-LatestPath {
    param(
        [Parameter(Mandatory = $true)][string]$Path,
        [string]$Filter = "*"
    )
    if (-not (Test-Path $Path)) {
        return $null
    }
    return Get-ChildItem -Path $Path -Filter $Filter -Force |
        Sort-Object LastWriteTime -Descending |
        Select-Object -First 1
}

# Get-ChatLogFiles 返回会话目录下的 chat 日志文件：新布局 <session-id>/chat/chat.json，
# 兼容旧布局 <session-id>/chat_*.json 与日期分区扁平布局 <partition>/chat_*.json。
function Get-ChatLogFiles {
    param([Parameter(Mandatory = $true)][string]$Directory)
    $files = @(Get-ChildItem -LiteralPath $Directory -File -Filter "chat_*.json" -ErrorAction SilentlyContinue)
    $chatDir = Join-Path $Directory "chat"
    $files += @(Get-ChildItem -LiteralPath $chatDir -File -Filter "chat*.json" -ErrorAction SilentlyContinue)
    return $files
}

# Get-LatestChatSessionDir 返回最近写入的会话日志目录：
# 新布局 chat-logs/YYYY/MM/DD/<session-id>/，兼容更早的嵌套/扁平目录。
function Get-LatestChatSessionDir {
    param([Parameter(Mandatory = $true)][string]$Root)
    if (-not (Test-Path $Root)) {
        return $null
    }
    return Get-ChildItem -LiteralPath $Root -Directory -Recurse -ErrorAction SilentlyContinue |
        ForEach-Object {
            $files = @(Get-ChatLogFiles -Directory $_.FullName)
            if ($files.Count -eq 0) { return }
            $lastWrite = ($files | Measure-Object -Property LastWriteTime -Maximum).Maximum
            [pscustomobject]@{ Directory = $_; LastWriteTime = $lastWrite }
        } |
        Sort-Object LastWriteTime -Descending |
        Select-Object -First 1 |
        ForEach-Object { $_.Directory }
}

# Get-FirstExistingPath 返回第一个存在的候选路径。
function Get-FirstExistingPath {
    param([string[]]$Candidates)
    foreach ($candidate in $Candidates) {
        if ($candidate -and (Test-Path -LiteralPath $candidate)) {
            return $candidate
        }
    }
    return $null
}

function Invoke-SqliteQuery {
    param(
        [Parameter(Mandatory = $true)][string]$DatabasePath,
        [Parameter(Mandatory = $true)][string]$Sql,
        [int]$TimeoutMs = 3000
    )
    if (-not (Test-Path $DatabasePath)) {
        return @()
    }
    $sqliteCommand = Get-Command sqlite3 -ErrorAction SilentlyContinue
    if ($null -eq $sqliteCommand) {
        return @()
    }
    $rows = & $sqliteCommand.Source -readonly -cmd ".timeout $TimeoutMs" $DatabasePath $Sql 2>$null
    if ($LASTEXITCODE -ne 0) {
        # WAL 未 checkpoint 时 readonly 打开可能失败；探针的 aicli 进程此时已退出，退化为普通打开。
        $rows = & $sqliteCommand.Source -cmd ".timeout $TimeoutMs" $DatabasePath $Sql 2>$null
    }
    return @($rows | Where-Object { -not [string]::IsNullOrWhiteSpace($_) })
}

function Get-RecentBlockedEvidence {
    param(
        [string]$SessionPath,
        [datetime]$Since,
        [string[]]$Patterns
    )
    $evidence = @{
        Patterns = @()
        Source = "<none>"
        Sample = ""
    }
    if (-not [string]::IsNullOrWhiteSpace($SessionPath) -and (Test-Path $SessionPath)) {
        $found = @(Test-FileContainsAny -Path $SessionPath -Patterns $Patterns)
        if ($found.Count -gt 0) {
            $evidence.Patterns = $found
            $evidence.Source = "session file"
            $evidence.Sample = $SessionPath
            return $evidence
        }
    }
    $storePath = Join-Path (Join-Path $env:USERPROFILE ".aicli\sessions") "runtime\team_store.sqlite"
    if (Test-Path $storePath) {
        # 用去掉 Z 的字符串比较（updated_at 形如 2026-09-13T07:35:12.9241319Z），Since 已回退 30s。
        $sinceText = $Since.ToUniversalTime().ToString("yyyy-MM-ddTHH:mm:ss")
        $sql = "select task_id || ' | ' || team_id || ' | ' || status || ' | ' || route_provider || '/' || route_model || ' | ' || substr(replace(replace(summary, char(10), ' '), char(13), ' '), 1, 240) from agent_control_task_records where updated_at >= '$sinceText' order by updated_at desc limit 20;"
        $rows = @(Invoke-SqliteQuery -DatabasePath $storePath -Sql $sql)
        $hitPatterns = @()
        $hitRow = ""
        foreach ($row in $rows) {
            $rowHit = $false
            foreach ($pattern in $Patterns) {
                if ($row.Contains($pattern)) {
                    $rowHit = $true
                    if ($hitPatterns -notcontains $pattern) {
                        $hitPatterns += $pattern
                    }
                }
            }
            if ($rowHit -and $hitRow -eq "") {
                $hitRow = $row
            }
        }
        if ($hitPatterns.Count -gt 0) {
            $evidence.Patterns = $hitPatterns
            $evidence.Source = "team_store.sqlite"
            $evidence.Sample = "task: $hitRow"
            return $evidence
        }
    }
    $chatLogRoot = Join-Path $env:USERPROFILE ".aicli\chat-logs"
    if (Test-Path $chatLogRoot) {
        # 新布局为 <session-id>/debug/debug.log，旧布局为 <session-id>.debug.log / <session-id>/debug.log。
        $debugLogs = @(Get-ChildItem -Path $chatLogRoot -Recurse -File -ErrorAction SilentlyContinue |
            Where-Object { ($_.Name -eq "debug.log" -or $_.Name -like "*.debug.log") -and $_.LastWriteTime -ge $Since } |
            Sort-Object LastWriteTime -Descending)
        foreach ($log in $debugLogs) {
            $found = @(Test-FileContainsAny -Path $log.FullName -Patterns $Patterns)
            if ($found.Count -gt 0) {
                $evidence.Patterns = $found
                $evidence.Source = "chat debug log"
                $evidence.Sample = $log.FullName
                return $evidence
            }
        }
    }
    return $evidence
}

function Get-RecentChildAgents {
    param(
        [datetime]$Since,
        [datetime]$Until
    )
    $storePath = Join-Path (Join-Path $env:USERPROFILE ".aicli\sessions") "runtime\agent_control.sqlite"
    if (-not (Test-Path $storePath)) {
        return @()
    }
    # 三条限定：上下界夹住探针时间窗、root 行也在窗口内、且只取 spawn_agent 子 agent。
    # team 成员的 agent_path 同样落在 /root/... 下，必须靠 team_id 为空把它们排除。
    $sinceText = $Since.ToUniversalTime().ToString("yyyy-MM-ddTHH:mm:ss")
    if ($null -eq $Until) {
        $Until = (Get-Date).ToUniversalTime().AddMinutes(5)
    }
    $untilText = $Until.ToUniversalTime().ToString("yyyy-MM-ddTHH:mm:ss")
    $sql = "select agent_path || ' | ' || coalesce(provider, '?') || '/' || coalesce(model, '?') || ' | difficulty=' || coalesce(difficulty, '') || ' | route=' || coalesce(route_source, '') || ' | fallback=' || fallback_used || ' | warnings=' || coalesce(route_warnings_json, '[]') || ' | ' || status || ' | session=' || coalesce(session_id, '') from agent_control_agents where depth > 0 and coalesce(team_id, '') = '' and created_at >= '$sinceText' and created_at <= '$untilText' and root_session_id in (select root_session_id from agent_control_agents where depth = 0 and created_at >= '$sinceText' and created_at <= '$untilText') order by created_at limit 20;"
    return @(Invoke-SqliteQuery -DatabasePath $storePath -Sql $sql)
}

function Get-RecentRootSessions {
    param(
        [datetime]$Since,
        [datetime]$Until
    )
    $storePath = Join-Path (Join-Path $env:USERPROFILE ".aicli\sessions") "runtime\agent_control.sqlite"
    if (-not (Test-Path $storePath)) {
        return @()
    }
    $sinceText = $Since.ToUniversalTime().ToString("yyyy-MM-ddTHH:mm:ss")
    if ($null -eq $Until) {
        $Until = (Get-Date).ToUniversalTime().AddMinutes(5)
    }
    $untilText = $Until.ToUniversalTime().ToString("yyyy-MM-ddTHH:mm:ss")
    $sql = "select session_id from agent_control_agents where depth = 0 and session_id is not null and session_id <> '' and created_at >= '$sinceText' and created_at <= '$untilText' order by created_at limit 5;"
    return @(Invoke-SqliteQuery -DatabasePath $storePath -Sql $sql)
}

function Get-TeamTerminalEvidence {
    param(
        [datetime]$Since,
        [datetime]$Until
    )
    $evidence = @{
        Rows                = @()
        SummaryEventCount   = 0
        CompletedEventCount = 0
    }
    $storePath = Join-Path (Join-Path $env:USERPROFILE ".aicli\sessions") "runtime\team_store.sqlite"
    if (-not (Test-Path $storePath)) {
        return $evidence
    }
    # teams/team_events 才是 wait_team 观察到 team.completed / team.summary 的持久证据：
    # parent 最终输出里通常只有中文总结正文，不含 "team.summary" 这种事件名字面量。
    $sinceText = $Since.ToUniversalTime().ToString("yyyy-MM-ddTHH:mm:ss")
    if ($null -eq $Until) {
        $Until = (Get-Date).ToUniversalTime().AddMinutes(5)
    }
    $untilText = $Until.ToUniversalTime().ToString("yyyy-MM-ddTHH:mm:ss")
    $sql = "select t.id || ' | status=' || t.status || ' | team.summary=' || (select count(*) from team_events e where e.team_id = t.id and e.type = 'team.summary') || ' | team.completed=' || (select count(*) from team_events e where e.team_id = t.id and e.type = 'team.completed') || ' | lead=' || coalesce(t.lead_session_id, '') from teams t where t.updated_at >= '$sinceText' and t.updated_at <= '$untilText' order by t.updated_at desc limit 5;"
    $evidence.Rows = @(Invoke-SqliteQuery -DatabasePath $storePath -Sql $sql)
    foreach ($row in $evidence.Rows) {
        if ($row -match "\| team\.summary=(\d+)") {
            $evidence.SummaryEventCount += [int]$Matches[1]
        }
        if ($row -match "\| team\.completed=(\d+)") {
            $evidence.CompletedEventCount += [int]$Matches[1]
        }
    }
    return $evidence
}

function Get-SessionToolCallNames {
    param(
        [string[]]$SessionIds
    )
    $names = @()
    $ids = @($SessionIds | Where-Object { -not [string]::IsNullOrWhiteSpace($_) } | Select-Object -Unique)
    if ($ids.Count -eq 0) {
        return $names
    }
    $dbPath = Join-Path (Join-Path $env:USERPROFILE ".aicli\sessions") "session_history.sqlite"
    if (-not (Test-Path $dbPath)) {
        return $names
    }
    $quotedIds = ($ids | ForEach-Object { "'" + ($_ -replace "'", "''") + "'" }) -join ","
    # 只取 assistant 行：工具名出现在 tool_calls 里，工具结果行（role=tool）里的同名文本不算调用证据。
    $sql = "select cast(substr(payload_json, 1, 200000) as text) from session_messages where role = 'assistant' and session_id in ($quotedIds) order by session_id, seq;"
    foreach ($row in @(Invoke-SqliteQuery -DatabasePath $dbPath -Sql $sql)) {
        foreach ($match in [regex]::Matches($row, '"name"\s*:\s*"([a-z_]+)"')) {
            $toolName = $match.Groups[1].Value
            if ($names -notcontains $toolName) {
                $names += $toolName
            }
        }
    }
    return $names
}

function Get-SessionMessageCounts {
    param(
        [string[]]$SessionIds
    )
    $counts = @{}
    $ids = @($SessionIds | Where-Object { -not [string]::IsNullOrWhiteSpace($_) } | Select-Object -Unique)
    if ($ids.Count -eq 0) {
        return $counts
    }
    $dbPath = Join-Path (Join-Path $env:USERPROFILE ".aicli\sessions") "session_history.sqlite"
    if (-not (Test-Path $dbPath)) {
        return $counts
    }
    $quotedIds = ($ids | ForEach-Object { "'" + ($_ -replace "'", "''") + "'" }) -join ","
    $sql = "select id || '|' || message_count from sessions where id in ($quotedIds);"
    foreach ($row in @(Invoke-SqliteQuery -DatabasePath $dbPath -Sql $sql)) {
        $parts = $row -split '\|', 2
        if ($parts.Count -eq 2) {
            $parsed = 0
            if ([int]::TryParse($parts[1], [ref]$parsed)) {
                $counts[$parts[0]] = $parsed
            }
        }
    }
    return $counts
}

function Add-LatestArtifactSummary {
    param(
        [Parameter(Mandatory = $true)][string]$ReportPath
    )
    $sessionRoot = Join-Path $env:USERPROFILE ".aicli\sessions"
    $chatLogRoot = Join-Path $env:USERPROFILE ".aicli\chat-logs"
    $latestSession = Get-LatestPath -Path $sessionRoot -Filter "session_*.json"
    $sessionHistoryDb = Join-Path $sessionRoot "session_history.sqlite"
    $latestChatLog = Get-LatestChatSessionDir -Root $chatLogRoot
    Add-ReportLine -Path $ReportPath -Text ""
    Add-ReportLine -Path $ReportPath -Text "最新 artifact:"
    if ($null -ne $latestSession) {
        Add-ReportLine -Path $ReportPath -Text "- Session File: $($latestSession.FullName)"
    } else {
        Add-ReportLine -Path $ReportPath -Text "- Session File: <not found: 会话正文现由 session_history.sqlite 承载>"
        if (Test-Path $sessionHistoryDb) {
            $historyItem = Get-Item $sessionHistoryDb
            Add-ReportLine -Path $ReportPath -Text "- Session History DB: $($historyItem.FullName) (updated $($historyItem.LastWriteTime.ToString('yyyy-MM-dd HH:mm:ss')))"
        }
    }
    if ($null -ne $latestChatLog) {
        Add-ReportLine -Path $ReportPath -Text "- Chat Log Dir: $($latestChatLog.FullName)"
        $chatFiles = @(Get-ChatLogFiles -Directory $latestChatLog.FullName | Sort-Object LastWriteTime -Descending)
        if ($chatFiles.Count -gt 0) {
            Add-ReportLine -Path $ReportPath -Text "- Chat Log File: $($chatFiles[0].FullName)"
        }
        $debugFile = Get-FirstExistingPath -Candidates @(
            (Join-Path (Join-Path $latestChatLog.FullName "debug") "debug.log"),
            (Join-Path $latestChatLog.FullName "debug.log")
        )
        if ($null -ne $debugFile) {
            Add-ReportLine -Path $ReportPath -Text "- Debug Log File: $debugFile"
        }
        $httpDir = Get-FirstExistingPath -Candidates @(
            (Join-Path $latestChatLog.FullName "http"),
            (Join-Path $latestChatLog.FullName "runtime-http"),
            "$($latestChatLog.FullName).http"
        )
        if ($null -ne $httpDir) {
            Add-ReportLine -Path $ReportPath -Text "- HTTP Artifact Dir: $httpDir"
        }
        $shellDir = Get-FirstExistingPath -Candidates @(
            (Join-Path $latestChatLog.FullName "shell"),
            (Join-Path $latestChatLog.FullName "local-shell"),
            "$($latestChatLog.FullName).shell"
        )
        if ($null -ne $shellDir) {
            Add-ReportLine -Path $ReportPath -Text "- Shell Artifact Dir: $shellDir"
        }
    } else {
        Add-ReportLine -Path $ReportPath -Text "- Chat Log Dir: <not found>"
    }
    return @{
        Session = $latestSession
        ChatLog = $latestChatLog
    }
}

function Test-FileContainsAny {
    param(
        [string]$Path,
        [string[]]$Patterns
    )
    if ([string]::IsNullOrWhiteSpace($Path) -or -not (Test-Path $Path)) {
        return @()
    }
    $matches = @()
    foreach ($pattern in $Patterns) {
        $found = Select-String -Path $Path -Pattern $pattern -SimpleMatch -ErrorAction SilentlyContinue | Select-Object -First 1
        if ($null -ne $found) {
            $matches += $pattern
        }
    }
    return $matches
}

function Test-TextContainsAny {
    param(
        [AllowEmptyString()][string]$Text,
        [string[]]$Patterns
    )
    $matches = @()
    foreach ($pattern in $Patterns) {
        if (-not [string]::IsNullOrEmpty($pattern) -and $Text.Contains($pattern)) {
            $matches += $pattern
        }
    }
    return $matches
}

$scriptDir = Split-Path -Parent $MyInvocation.MyCommand.Path
$repoRoot = (Resolve-Path (Join-Path $scriptDir "..")).Path
Set-Location $repoRoot

$timestamp = Get-Date -Format "yyyyMMdd-HHmmss"
$outputRoot = Join-Path $repoRoot $OutputDir
New-Item -ItemType Directory -Force -Path $outputRoot | Out-Null
$reportPath = Join-Path $outputRoot "multi-agent-real-terminal-validation-$timestamp.md"

if ([string]::IsNullOrWhiteSpace($AicliPath)) {
    $AicliPath = Join-Path $repoRoot "backend\aicli.exe"
}
$resolvedAicliPath = $AicliPath
if (-not [System.IO.Path]::IsPathRooted($resolvedAicliPath)) {
    $resolvedAicliPath = Join-Path $repoRoot $resolvedAicliPath
}
$resolvedAicliPath = (Resolve-Path $resolvedAicliPath).Path
$aicliWorkDir = Split-Path -Parent $resolvedAicliPath
$aicliFileName = Split-Path -Leaf $resolvedAicliPath

$extraChatArguments = @()
if (-not [string]::IsNullOrWhiteSpace($Model)) {
    $extraChatArguments += @("--model", $Model)
}
if (-not [string]::IsNullOrWhiteSpace($ReasoningEffort)) {
    $extraChatArguments += @("--reasoning-effort", $ReasoningEffort)
}
$configFullPath = ""
if (-not [string]::IsNullOrWhiteSpace($ConfigPath)) {
    $configFullPath = [System.IO.Path]::GetFullPath($ConfigPath)
    $extraChatArguments += @("--config", $configFullPath)
}
$extraChatArgsText = ""
if ($extraChatArguments.Count -gt 0) {
    $extraChatArgsText = " " + ($extraChatArguments -join " ")
}
$modelLabel = if ([string]::IsNullOrWhiteSpace($Model)) { "<provider default>" } else { $Model }
$reasoningLabel = if ([string]::IsNullOrWhiteSpace($ReasoningEffort)) { "<provider default>" } else { $ReasoningEffort }

Set-Content -Path $reportPath -Encoding UTF8 -Value "# Multi-Agent 真实终端验证记录"
Add-ReportLine -Path $reportPath -Text ""
Add-ReportLine -Path $reportPath -Text "生成时间: $(Get-Date -Format o)"
Add-ReportLine -Path $reportPath -Text "Repo: $repoRoot"
Add-ReportLine -Path $reportPath -Text "Provider: $Provider"
Add-ReportLine -Path $reportPath -Text "Model: $modelLabel"
Add-ReportLine -Path $reportPath -Text "ReasoningEffort: $reasoningLabel"
Add-ReportLine -Path $reportPath -Text "AICLI: $resolvedAicliPath"
Add-ReportLine -Path $reportPath -Text "WorkDir: $aicliWorkDir"
if ($configFullPath -ne "") {
    Add-ReportLine -Path $reportPath -Text "Config: $configFullPath（局部配置副本；用户全局配置未改动）"
}
Add-ReportLine -Path $reportPath -Text ""

if (-not $SkipBuild) {
    Add-ReportLine -Path $reportPath -Text "## 1. 构建"
    Add-ReportLine -Path $reportPath -Text ""
    Add-ReportBlock -Path $reportPath -Language "powershell" -Text "cd $repoRoot\backend`ngo build -o aicli.exe ./cmd/aicli"
    Push-Location (Join-Path $repoRoot "backend")
    try {
        $buildOutput = & go build -o aicli.exe ./cmd/aicli 2>&1
        $buildExit = $LASTEXITCODE
    } finally {
        Pop-Location
    }
    Add-ReportLine -Path $reportPath -Text "ExitCode: $buildExit"
    Add-ReportBlock -Path $reportPath -Language "text" -Text (($buildOutput | Out-String).TrimEnd())
    if ($buildExit -ne 0) {
        Write-Host "Build failed. Report: $reportPath"
        exit $buildExit
    }
} else {
    Add-ReportLine -Path $reportPath -Text "## 1. 构建"
    Add-ReportLine -Path $reportPath -Text ""
    Add-ReportLine -Path $reportPath -Text "跳过构建: -SkipBuild"
}

Add-ReportLine -Path $reportPath -Text ""
Add-ReportLine -Path $reportPath -Text "## 2. Provider Smoke Test"
Add-ReportLine -Path $reportPath -Text ""

if (-not $SkipProviderSmoke) {
    $timeoutValue = "{0}s" -f $RequestTimeoutSeconds
    $smokeCommand = "cd $aicliWorkDir`n.\$aicliFileName chat --provider $Provider$extraChatArgsText --no-interactive --request-timeout $timeoutValue --message `"请只回复 OK。`""
    Add-ReportBlock -Path $reportPath -Language "powershell" -Text $smokeCommand
    Push-Location $aicliWorkDir
    try {
        $smokeOutput = & $resolvedAicliPath chat --provider $Provider @extraChatArguments --no-interactive --request-timeout $timeoutValue --message "请只回复 OK。" 2>&1
        $smokeExit = $LASTEXITCODE
    } finally {
        Pop-Location
    }
    Add-ReportLine -Path $reportPath -Text "ExitCode: $smokeExit"
    Add-ReportBlock -Path $reportPath -Language "text" -Text (($smokeOutput | Out-String).TrimEnd())
    if ($smokeExit -ne 0) {
        Add-ReportLine -Path $reportPath -Text ""
        Add-ReportLine -Path $reportPath -Text "结论: provider smoke test 未通过。请先修复 provider 凭证或配置，再执行真实终端验证。"
    } else {
        Add-ReportLine -Path $reportPath -Text ""
        Add-ReportLine -Path $reportPath -Text "结论: provider smoke test 通过，可以继续人工终端验证。"
    }
} else {
    Add-ReportLine -Path $reportPath -Text "跳过 provider smoke test: -SkipProviderSmoke"
}

Add-ReportLine -Path $reportPath -Text ""
Add-ReportLine -Path $reportPath -Text "## 3. 真实 Provider 非交互 spawn_agent 验证"
Add-ReportLine -Path $reportPath -Text ""

if (-not $SkipSpawnAgentProbe) {
    $spawnTimeoutValue = "{0}s" -f $SpawnAgentTimeoutSeconds
    $spawnPrompt = "请使用 spawn_agent 并行启动 2 个子 agent，不要读取文件。agent A 只基于内联短句 '验证目标是确认多 agent 输出隔离、reasoning 隔离、等待语义和中断语义' 总结一句话；agent B 只基于内联短句 '剩余门禁是真实 Windows Terminal Ctrl+C 与 TUI 面板人工验证' 总结一句话。两个子任务都标记为 $SpawnChildDifficulty 难度，请在 spawn_agent 调用里把 difficulty 参数显式设为 $SpawnChildDifficulty。spawn_agent 后请调用 wait_agent 等待，并调用 read_agent_events 读取两个子 agent 事件，最后 parent 用不超过 120 字中文汇总。不要向用户提问或请求确认，也不需要其它收尾动作，汇总后直接结束。"
    $spawnCommand = "cd $aicliWorkDir`n.\$aicliFileName chat --provider $Provider$extraChatArgsText --no-interactive --request-timeout $spawnTimeoutValue --message `"$spawnPrompt`""
    Add-ReportBlock -Path $reportPath -Language "powershell" -Text $spawnCommand
    $spawnProbeStartedAt = (Get-Date).AddSeconds(-30)
    Push-Location $aicliWorkDir
    try {
        $spawnOutput = & $resolvedAicliPath chat --provider $Provider @extraChatArguments --no-interactive --request-timeout $spawnTimeoutValue --message $spawnPrompt 2>&1
        $spawnExit = $LASTEXITCODE
    } finally {
        Pop-Location
    }
    $spawnProbeEndedAt = (Get-Date).AddSeconds(5)
    Add-ReportLine -Path $reportPath -Text "ExitCode: $spawnExit"
    Add-ReportBlock -Path $reportPath -Language "text" -Text (($spawnOutput | Out-String).TrimEnd())
    $artifacts = Add-LatestArtifactSummary -ReportPath $reportPath
    $sessionPath = ""
    if ($null -ne $artifacts.Session) {
        $sessionPath = $artifacts.Session.FullName
    }
    $requiredPatterns = @("spawn_agent", "wait_agent", "read_agent_events")
    $forbiddenPatterns = @(
        "UNIQUE constraint failed: agent_control_agents.session_id",
        "spawn_team teammate id",
        "session is busy (running)"
    )
    $spawnOutputText = ($spawnOutput | Out-String)
    # 同上：优先扫描探针输出的真实工具调用文本，再用 session 文件补全。
    $requiredFound = @(Test-TextContainsAny -Text $spawnOutputText -Patterns $requiredPatterns)
    if ($requiredFound.Count -lt $requiredPatterns.Count -and -not [string]::IsNullOrWhiteSpace($sessionPath)) {
        foreach ($pattern in @(Test-FileContainsAny -Path $sessionPath -Patterns $requiredPatterns)) {
            if ($requiredFound -notcontains $pattern) {
                $requiredFound += $pattern
            }
        }
    }
    $forbiddenFound = Test-TextContainsAny -Text $spawnOutputText -Patterns $forbiddenPatterns
    # 子 agent 会被 aicli.subagents.routing 按 difficulty 单独路由，provider 可能与父会话不同；
    # 只扫父进程输出会把「子 agent 零产出」误判为验证通过，因此必须回查 agent store + session 历史。
    $spawnChildAgents = @(Get-RecentChildAgents -Since $spawnProbeStartedAt -Until $spawnProbeEndedAt)
    $spawnChildSessions = @()
    $spawnChildRoutes = @()
    foreach ($childRow in $spawnChildAgents) {
        $childFields = $childRow -split ' \| '
        if ($childFields.Count -ge 2 -and $spawnChildRoutes -notcontains $childFields[1]) {
            $spawnChildRoutes += $childFields[1]
        }
        $childSessionMatch = [regex]::Match($childRow, 'session=([^\s|]+)')
        if ($childSessionMatch.Success -and $spawnChildSessions -notcontains $childSessionMatch.Groups[1].Value) {
            $spawnChildSessions += $childSessionMatch.Groups[1].Value
        }
    }
    $spawnChildMessageCounts = Get-SessionMessageCounts -SessionIds $spawnChildSessions
    $spawnChildrenWithOutput = @($spawnChildSessions | Where-Object {
            $spawnChildMessageCounts.ContainsKey($_) -and $spawnChildMessageCounts[$_] -ge 2
        }).Count
    $spawnChildrenIncomplete = ($spawnChildSessions.Count -eq 0) -or ($spawnChildrenWithOutput -lt $spawnChildSessions.Count)
    $spawnRouteDiverged = $false
    # '?/?' 表示该子 agent 未落路由信息（不等于「路由到别的 provider」），不能据此判环境阻断。
    $spawnKnownChildRoutes = @($spawnChildRoutes | Where-Object { $_ -notlike '?/*' -and $_ -notlike '*/?' })
    if ($spawnKnownChildRoutes.Count -gt 0) {
        $spawnRouteDiverged = @($spawnKnownChildRoutes | Where-Object { $_ -like "$Provider/*" }).Count -eq 0
    }
    $spawnBlockedEvidence = @{
        Patterns = @()
        Source = "<none>"
        Sample = ""
    }
    if ($spawnChildrenIncomplete) {
        $spawnBlockedEvidence = Get-RecentBlockedEvidence -SessionPath $sessionPath -Since $spawnProbeStartedAt -Patterns @(
            "response-header guard after 20s",
            "timeout awaiting response headers",
            "provider transport stream failed",
            "provider call failed after repeated fast-fail retries"
        )
    }
    # 非交互 stdout 不保证回显 tool_calls（16:08 那次父进程全绿但 stdout 只有最终答复），
    # 因此工具调用证据以父会话 transcript 为准，stdout 只作为补充。
    $spawnRootSessions = @(Get-RecentRootSessions -Since $spawnProbeStartedAt -Until $spawnProbeEndedAt)
    $spawnTranscriptToolNames = @(Get-SessionToolCallNames -SessionIds $spawnRootSessions)
    foreach ($toolName in $spawnTranscriptToolNames) {
        if ($requiredPatterns -contains $toolName -and $requiredFound -notcontains $toolName) {
            $requiredFound += $toolName
        }
    }
    # spawn_agent 的 assistant 行在并行 spawn 时不落库（已有证据：155243 / 160811 的 transcript 里
    # 只有 wait_agent / read_agent_events），但它派生的子 agent 行本身就是调用证据：
    # depth=1 且 team_id 为空的子 agent 只能由 spawn_agent 产生。
    if ($spawnChildAgents.Count -gt 0 -and $requiredFound -notcontains "spawn_agent") {
        $requiredFound += "spawn_agent"
    }
    Add-ReportLine -Path $reportPath -Text ""
    Add-ReportLine -Path $reportPath -Text "Pattern check:"
    Add-ReportLine -Path $reportPath -Text "- Required found: $($requiredFound -join ', ')"
    Add-ReportLine -Path $reportPath -Text "- Forbidden scan source: command output"
    Add-ReportLine -Path $reportPath -Text "- Required evidence source: command output + session_history.sqlite（父会话 tool_calls）+ agent_control.sqlite（子 agent 行）"
    if ($spawnRootSessions.Count -gt 0) {
        Add-ReportLine -Path $reportPath -Text "- Parent session: $($spawnRootSessions -join ', ')"
    }
    Add-ReportLine -Path $reportPath -Text "- Parent tool calls: $(if ($spawnTranscriptToolNames.Count -gt 0) { $spawnTranscriptToolNames -join ', ' } else { '<none>' })"
    if ($forbiddenFound.Count -gt 0) {
        Add-ReportLine -Path $reportPath -Text "- Forbidden found: $($forbiddenFound -join ', ')"
    } else {
        Add-ReportLine -Path $reportPath -Text "- Forbidden found: <none>"
    }
    Add-ReportLine -Path $reportPath -Text "- Child agent count: $($spawnChildAgents.Count)"
    if ($spawnChildRoutes.Count -gt 0) {
        Add-ReportLine -Path $reportPath -Text "- Child agent routes: $($spawnChildRoutes -join ', ')"
    } else {
        Add-ReportLine -Path $reportPath -Text "- Child agent routes: <none>"
    }
    foreach ($childRow in @($spawnChildAgents | Select-Object -First 6)) {
        Add-ReportLine -Path $reportPath -Text "- Child agent row: $childRow"
    }
    Add-ReportLine -Path $reportPath -Text "- Child agent output: $spawnChildrenWithOutput/$($spawnChildSessions.Count) 个子 agent session 的 message_count >= 2"
    Add-ReportLine -Path $reportPath -Text "- Child agent difficulty (requested): $SpawnChildDifficulty（若 levels.easy 指向不可达 provider，探针显式指定其它难度以走 inherit_parent_when_missing 继承父 provider）"
    if ($spawnBlockedEvidence.Patterns.Count -gt 0) {
        Add-ReportLine -Path $reportPath -Text "- Route target blocked signals: $($spawnBlockedEvidence.Patterns -join ', ')"
        $spawnBlockedEvidenceText = $spawnBlockedEvidence.Source
        if (-not [string]::IsNullOrWhiteSpace($spawnBlockedEvidence.Sample)) {
            $spawnBlockedEvidenceText = "$spawnBlockedEvidenceText -> $($spawnBlockedEvidence.Sample)"
        }
        Add-ReportLine -Path $reportPath -Text "- Route target blocked evidence: $spawnBlockedEvidenceText"
    }
    $spawnPatternsOk = ($spawnExit -eq 0 -and $requiredFound.Count -eq $requiredPatterns.Count -and $forbiddenFound.Count -eq 0)
    $spawnChildrenOk = ($spawnChildAgents.Count -gt 0 -and $spawnChildSessions.Count -eq $spawnChildAgents.Count -and -not $spawnChildrenIncomplete)
    if ($spawnPatternsOk -and $spawnChildrenOk) {
        Add-ReportLine -Path $reportPath -Text ""
        Add-ReportLine -Path $reportPath -Text "结论: 真实 provider 非交互 spawn_agent 验证通过。"
    } elseif ($spawnPatternsOk -and $spawnChildAgents.Count -gt 0) {
        Add-ReportLine -Path $reportPath -Text ""
        Add-ReportLine -Path $reportPath -Text "结论: spawn_agent 工具链已连通，但子 agent 无产出，不能记为验证通过。"
        Add-ReportLine -Path $reportPath -Text "- 证据: 子 agent 共 $($spawnChildAgents.Count) 个，其中 $spawnChildrenWithOutput 个 session 有 message_count >= 2 的产出记录。"
        Add-ReportLine -Path $reportPath -Text "- 子 agent 路由: $(if ($spawnChildRoutes.Count -gt 0) { $spawnChildRoutes -join ', ' } else { '<none>' })（父会话 provider: $Provider）"
        if ($spawnRouteDiverged -or $spawnBlockedEvidence.Patterns.Count -gt 0) {
            Add-ReportLine -Path $reportPath -Text "- 归类: 环境阻断——子 agent 被 aicli.subagents.routing 按 difficulty 路由到与父会话不同的 provider，且该路由目标在本机失败。"
            Add-ReportLine -Path $reportPath -Text "- 处置: 把 aicli.subagents.routing.levels.* 指向可达 provider（或把 routing.enabled 置为 false 让子 agent 继承父会话 provider），然后重跑本脚本。"
            Add-ReportLine -Path $reportPath -Text "- 该结论不代表 spawn_agent/wait_agent/read_agent_events 语义回归；需要与真实实现缺陷区分记录。"
        } else {
            Add-ReportLine -Path $reportPath -Text "- 归类: 待区分——子 agent 与父会话使用同一 provider，需人工判断是实现缺陷还是该 provider 的子 agent 请求参数问题。"
        }
    } else {
        Add-ReportLine -Path $reportPath -Text ""
        Add-ReportLine -Path $reportPath -Text "结论: 真实 provider 非交互 spawn_agent 验证未通过或证据不足。请检查 session/chat log。"
        if ($spawnChildAgents.Count -eq 0) {
            Add-ReportLine -Path $reportPath -Text "- 未在 agent_control.sqlite 找到本次探针窗口内新建的子 agent 记录，无法确认 spawn_agent 是否真正派生出子 agent。"
        }
    }
} else {
    Add-ReportLine -Path $reportPath -Text "跳过 spawn_agent probe: -SkipSpawnAgentProbe"
}

Add-ReportLine -Path $reportPath -Text ""
Add-ReportLine -Path $reportPath -Text "## 4. 真实 Provider 非交互 spawn_team + wait_team 验证"
Add-ReportLine -Path $reportPath -Text ""

if (-not $SkipSpawnTeamProbe) {
    $teamTimeoutValue = "{0}s" -f $SpawnTeamTimeoutSeconds
    $teamPrompt = "请使用 spawn_team auto_start=true 创建 3 个 team 成员和 3 个 task，不要读取文件，也不要写文件。三个 task 分别基于这些内联短句各用一句中文总结：task-1 'AgentControl task graph 已成为 team task 的主写入路径'；task-2 'spawn_team 完成等待应使用 wait_team 而不是 wait_agent/read_agent_events'；task-3 '真实终端仍需验证 Ctrl+C 与 TUI 面板表现'。spawn_team 返回后，必须使用工具结果里的 team_id 调用 wait_team 等待团队完成和 team.summary，然后 parent 用不超过 120 字中文汇总。不要对 teammate id 调用 wait_agent 或 read_agent_events。不要向用户提问或请求确认，也不需要其它收尾动作，汇总后直接结束。"
    $teamCommand = "cd $aicliWorkDir`n.\$aicliFileName chat --provider $Provider$extraChatArgsText --no-interactive --request-timeout $teamTimeoutValue --message `"$teamPrompt`""
    Add-ReportBlock -Path $reportPath -Language "powershell" -Text $teamCommand
    $teamProbeStartedAt = (Get-Date).AddSeconds(-30)
    Push-Location $aicliWorkDir
    try {
        $teamOutput = & $resolvedAicliPath chat --provider $Provider @extraChatArguments --no-interactive --request-timeout $teamTimeoutValue --message $teamPrompt 2>&1
        $teamExit = $LASTEXITCODE
    } finally {
        Pop-Location
    }
    Add-ReportLine -Path $reportPath -Text "ExitCode: $teamExit"
    Add-ReportBlock -Path $reportPath -Language "text" -Text (($teamOutput | Out-String).TrimEnd())
    $teamArtifacts = Add-LatestArtifactSummary -ReportPath $reportPath
    $teamSessionPath = ""
    if ($null -ne $teamArtifacts.Session) {
        $teamSessionPath = $teamArtifacts.Session.FullName
    }
    $teamRequiredPatterns = @("spawn_team", "wait_team", "team.summary")
    $teamForbiddenPatterns = @(
        "UNIQUE constraint failed: agent_control_agents.session_id",
        "spawn_team teammate id",
        "session is busy (running)",
        "is a spawn_agent child-session tool"
    )
    $teamOutputText = ($teamOutput | Out-String)
    # 会话正文现在存于 session_history.sqlite，session_*.json 往往不存在，因此先看探针输出，再用 session 文件补全。
    $teamRequiredFound = @(Test-TextContainsAny -Text $teamOutputText -Patterns $teamRequiredPatterns)
    if ($teamRequiredFound.Count -lt $teamRequiredPatterns.Count -and -not [string]::IsNullOrWhiteSpace($teamSessionPath)) {
        foreach ($pattern in @(Test-FileContainsAny -Path $teamSessionPath -Patterns $teamRequiredPatterns)) {
            if ($teamRequiredFound -notcontains $pattern) {
                $teamRequiredFound += $pattern
            }
        }
    }
    # 命令输出只有 parent 总结正文：spawn_team/wait_team 的调用证据在 session_history.sqlite，
    # team.summary 的完成证据在 team_store.sqlite（teams/team_events），补齐后再判定，避免假红。
    $teamProbeEndedAt = (Get-Date).AddSeconds(5)
    $teamRootSessions = @(Get-RecentRootSessions -Since $teamProbeStartedAt -Until $teamProbeEndedAt)
    $teamToolCalls = @(Get-SessionToolCallNames -SessionIds $teamRootSessions)
    foreach ($toolName in @("spawn_team", "wait_team")) {
        if ($teamToolCalls -contains $toolName -and $teamRequiredFound -notcontains $toolName) {
            $teamRequiredFound += $toolName
        }
    }
    $teamTerminalEvidence = Get-TeamTerminalEvidence -Since $teamProbeStartedAt -Until $teamProbeEndedAt
    if ($teamTerminalEvidence.SummaryEventCount -gt 0 -and $teamRequiredFound -notcontains "team.summary") {
        $teamRequiredFound += "team.summary"
    }
    $teamForbiddenFound = Test-TextContainsAny -Text $teamOutputText -Patterns $teamForbiddenPatterns
    $teamEnvironmentBlockedPatterns = @(
        "response-header guard after 20s",
        "timeout awaiting response headers",
        "provider transport stream failed",
        "provider call failed after repeated fast-fail retries"
    )
    $teamEnvironmentBlockedFound = @(Test-TextContainsAny -Text $teamOutputText -Patterns $teamEnvironmentBlockedPatterns)
    $teamBlockedEvidence = @{
        Source = $(if ($teamEnvironmentBlockedFound.Count -gt 0) { "command output" } else { "<none>" })
        Sample = ""
    }
    if ($teamEnvironmentBlockedFound.Count -eq 0) {
        # 子任务的传输错误通常不出现在 parent 最终输出里，需要回查 team store / session / chat log。
        $teamBlockedEvidence = Get-RecentBlockedEvidence -SessionPath $teamSessionPath -Since $teamProbeStartedAt -Patterns $teamEnvironmentBlockedPatterns
        $teamEnvironmentBlockedFound = @($teamBlockedEvidence.Patterns)
    }
    Add-ReportLine -Path $reportPath -Text ""
    Add-ReportLine -Path $reportPath -Text "Pattern check:"
    Add-ReportLine -Path $reportPath -Text "- Required found: $($teamRequiredFound -join ', ')"
    Add-ReportLine -Path $reportPath -Text "- Forbidden scan source: command output"
    Add-ReportLine -Path $reportPath -Text "- Required evidence source: command output + session_history.sqlite（父会话 tool_calls）+ team_store.sqlite（teams/team_events）"
    Add-ReportLine -Path $reportPath -Text "- Parent session: $(if ($teamRootSessions.Count -gt 0) { $teamRootSessions -join ', ' } else { '<none>' })"
    Add-ReportLine -Path $reportPath -Text "- Parent tool calls: $(if ($teamToolCalls.Count -gt 0) { $teamToolCalls -join ', ' } else { '<none>' })"
    if ($teamTerminalEvidence.Rows.Count -gt 0) {
        foreach ($teamRow in $teamTerminalEvidence.Rows) {
            Add-ReportLine -Path $reportPath -Text "- Team row: $teamRow"
        }
    } else {
        Add-ReportLine -Path $reportPath -Text "- Team row: <none in probe window>"
    }
    if ($teamForbiddenFound.Count -gt 0) {
        Add-ReportLine -Path $reportPath -Text "- Forbidden found: $($teamForbiddenFound -join ', ')"
    } else {
        Add-ReportLine -Path $reportPath -Text "- Forbidden found: <none>"
    }
    if ($teamEnvironmentBlockedFound.Count -gt 0) {
        Add-ReportLine -Path $reportPath -Text "- Route target blocked signals: $($teamEnvironmentBlockedFound -join ', ')"
    } else {
        Add-ReportLine -Path $reportPath -Text "- Route target blocked signals: <none>"
    }
    $teamBlockedEvidenceText = $teamBlockedEvidence.Source
    if (-not [string]::IsNullOrWhiteSpace($teamBlockedEvidence.Sample)) {
        $teamBlockedEvidenceText = "$teamBlockedEvidenceText -> $($teamBlockedEvidence.Sample)"
    }
    Add-ReportLine -Path $reportPath -Text "- Route target blocked evidence: $teamBlockedEvidenceText"
    if ($teamExit -eq 0 -and $teamRequiredFound.Count -eq $teamRequiredPatterns.Count -and $teamForbiddenFound.Count -eq 0) {
        Add-ReportLine -Path $reportPath -Text ""
        Add-ReportLine -Path $reportPath -Text "结论: 真实 provider 非交互 spawn_team + wait_team 验证通过。"
    } elseif ($teamEnvironmentBlockedFound.Count -gt 0) {
        Add-ReportLine -Path $reportPath -Text ""
        Add-ReportLine -Path $reportPath -Text "结论: spawn_team + wait_team 未通过，且属于环境阻断: 子任务路由目标的 provider 在本机不可达（transport 快速失败 / 20s response-header guard）。"
        Add-ReportLine -Path $reportPath -Text "- 命中信号: $($teamEnvironmentBlockedFound -join ', ')"
        Add-ReportLine -Path $reportPath -Text "- 证据来源: $teamBlockedEvidenceText"
        Add-ReportLine -Path $reportPath -Text "- 先做可达性预检: 读取 `$env:USERPROFILE\.aicli\config.yaml 中 aicli.teams.routing.levels.* / aicli.subagents.routing.levels.* 的 provider，再探测其 base_url 是否可达。"
        Add-ReportLine -Path $reportPath -Text "- 处置: 把 levels.* 指向可达 provider，或把 routing.enabled 置为 false 让子任务继承父会话 provider，然后重跑本脚本。"
        Add-ReportLine -Path $reportPath -Text "- 该结论不代表 spawn_team/wait_team 语义回归；需要与真实实现缺陷区分记录。"
    } else {
        Add-ReportLine -Path $reportPath -Text ""
        Add-ReportLine -Path $reportPath -Text "结论: 真实 provider 非交互 spawn_team + wait_team 验证未通过或证据不足。请检查 session/chat log。"
    }
} else {
    Add-ReportLine -Path $reportPath -Text "跳过 spawn_team probe: -SkipSpawnTeamProbe"
}

Add-ReportLine -Path $reportPath -Text ""
Add-ReportLine -Path $reportPath -Text "## 5. 真实终端人工验证"
Add-ReportLine -Path $reportPath -Text ""
Add-ReportLine -Path $reportPath -Text "在 Windows Terminal 中执行:"
Add-ReportBlock -Path $reportPath -Language "powershell" -Text "cd $aicliWorkDir`n.\$aicliFileName chat"
Add-ReportLine -Path $reportPath -Text ""
Add-ReportLine -Path $reportPath -Text "按以下清单记录结果:"
Add-ReportLine -Path $reportPath -Text ""
Add-ReportLine -Path $reportPath -Text "- [ ] 验证 A: spawn_agent 并行输出隔离。"
Add-ReportLine -Path $reportPath -Text "- [ ] 验证 B: spawn_team auto_start 并行与 busy 收敛。"
Add-ReportLine -Path $reportPath -Text "- [ ] 验证 C: Ctrl+C 第一次取消 active child/team，第二次退出。"
Add-ReportLine -Path $reportPath -Text "- [ ] 验证 D: provider stream error 收敛。"
Add-ReportLine -Path $reportPath -Text "- [ ] `/agents panel follow timeout=10s 20` 可刷新且不污染 primary console。"
Add-ReportLine -Path $reportPath -Text "- [ ] `/collab all 50` 和 `/timeline active 50` 可看到结构化 mailbox/timeline。"
Add-ReportLine -Path $reportPath -Text ""
Add-ReportLine -Path $reportPath -Text "需要补充的证据:"
Add-ReportLine -Path $reportPath -Text ""
Add-ReportLine -Path $reportPath -Text "- Session ID:"
Add-ReportLine -Path $reportPath -Text "- Session File:"
Add-ReportLine -Path $reportPath -Text "- Chat Log File:"
Add-ReportLine -Path $reportPath -Text "- Debug Log File:"
Add-ReportLine -Path $reportPath -Text "- HTTP Artifact Dir:"
Add-ReportLine -Path $reportPath -Text "- Shell Artifact Dir:"
Add-ReportLine -Path $reportPath -Text "- 通过/失败结论:"
Add-ReportLine -Path $reportPath -Text "- 失败细节:"

Write-Host "Validation report written: $reportPath"
Write-Host "Provider: $Provider | Model: $modelLabel | ReasoningEffort: $reasoningLabel"

if ($null -ne $previousConsoleOutputEncoding) {
    try {
        [Console]::OutputEncoding = $previousConsoleOutputEncoding
    } catch {
        # 还原失败不影响验证结论。
    }
}
