param(
    [string]$Provider = "mimo_anthropic",
    [string]$AicliPath = "",
    [string]$OutputDir = "docs\working",
    [int]$RequestTimeoutSeconds = 60,
    [int]$SpawnAgentTimeoutSeconds = 240,
    [int]$SpawnTeamTimeoutSeconds = 300,
    [switch]$SkipBuild,
    [switch]$SkipProviderSmoke,
    [switch]$SkipSpawnAgentProbe,
    [switch]$SkipSpawnTeamProbe
)

$ErrorActionPreference = "Stop"
if (Get-Variable -Name PSNativeCommandUseErrorActionPreference -ErrorAction SilentlyContinue) {
    $PSNativeCommandUseErrorActionPreference = $false
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

function Add-LatestArtifactSummary {
    param(
        [Parameter(Mandatory = $true)][string]$ReportPath
    )
    $sessionRoot = Join-Path $env:USERPROFILE ".aicli\sessions"
    $chatLogRoot = Join-Path $env:USERPROFILE ".aicli\chat-logs"
    $latestSession = Get-LatestPath -Path $sessionRoot -Filter "session_*.json"
    $latestChatLog = Get-LatestPath -Path $chatLogRoot -Filter "*"
    Add-ReportLine -Path $ReportPath -Text ""
    Add-ReportLine -Path $ReportPath -Text "最新 artifact:"
    if ($null -ne $latestSession) {
        Add-ReportLine -Path $ReportPath -Text "- Session File: $($latestSession.FullName)"
    } else {
        Add-ReportLine -Path $ReportPath -Text "- Session File: <not found>"
    }
    if ($null -ne $latestChatLog) {
        Add-ReportLine -Path $ReportPath -Text "- Chat Log Dir: $($latestChatLog.FullName)"
        $chatFile = Get-LatestPath -Path $latestChatLog.FullName -Filter "chat_*.json"
        if ($null -ne $chatFile) {
            Add-ReportLine -Path $ReportPath -Text "- Chat Log File: $($chatFile.FullName)"
        }
        $debugFile = Join-Path $latestChatLog.FullName "debug.log"
        if (Test-Path $debugFile) {
            Add-ReportLine -Path $ReportPath -Text "- Debug Log File: $debugFile"
        }
        $httpDir = Join-Path $latestChatLog.FullName "runtime-http"
        if (Test-Path $httpDir) {
            Add-ReportLine -Path $ReportPath -Text "- HTTP Artifact Dir: $httpDir"
        }
        $shellDir = Join-Path $latestChatLog.FullName "local-shell"
        if (Test-Path $shellDir) {
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

Set-Content -Path $reportPath -Encoding UTF8 -Value "# Multi-Agent 真实终端验证记录"
Add-ReportLine -Path $reportPath -Text ""
Add-ReportLine -Path $reportPath -Text "生成时间: $(Get-Date -Format o)"
Add-ReportLine -Path $reportPath -Text "Repo: $repoRoot"
Add-ReportLine -Path $reportPath -Text "Provider: $Provider"
Add-ReportLine -Path $reportPath -Text "AICLI: $resolvedAicliPath"
Add-ReportLine -Path $reportPath -Text "WorkDir: $aicliWorkDir"
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
    $smokeCommand = "cd $aicliWorkDir`n.\$aicliFileName chat --provider $Provider --no-interactive --request-timeout $timeoutValue --message `"请只回复 OK。`""
    Add-ReportBlock -Path $reportPath -Language "powershell" -Text $smokeCommand
    Push-Location $aicliWorkDir
    try {
        $smokeOutput = & $resolvedAicliPath chat --provider $Provider --no-interactive --request-timeout $timeoutValue --message "请只回复 OK。" 2>&1
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
    $spawnPrompt = "请使用 spawn_agent 并行启动 2 个子 agent，不要读取文件。agent A 只基于内联短句 '验证目标是确认多 agent 输出隔离、reasoning 隔离、等待语义和中断语义' 总结一句话；agent B 只基于内联短句 '剩余门禁是真实 Windows Terminal Ctrl+C 与 TUI 面板人工验证' 总结一句话。spawn_agent 后请调用 wait_agent 等待，并调用 read_agent_events 读取两个子 agent 事件，最后 parent 用不超过 120 字中文汇总。"
    $spawnCommand = "cd $aicliWorkDir`n.\$aicliFileName chat --provider $Provider --no-interactive --request-timeout $spawnTimeoutValue --message `"$spawnPrompt`""
    Add-ReportBlock -Path $reportPath -Language "powershell" -Text $spawnCommand
    Push-Location $aicliWorkDir
    try {
        $spawnOutput = & $resolvedAicliPath chat --provider $Provider --no-interactive --request-timeout $spawnTimeoutValue --message $spawnPrompt 2>&1
        $spawnExit = $LASTEXITCODE
    } finally {
        Pop-Location
    }
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
    $requiredFound = Test-FileContainsAny -Path $sessionPath -Patterns $requiredPatterns
    $spawnOutputText = ($spawnOutput | Out-String)
    $forbiddenFound = Test-TextContainsAny -Text $spawnOutputText -Patterns $forbiddenPatterns
    Add-ReportLine -Path $reportPath -Text ""
    Add-ReportLine -Path $reportPath -Text "Session pattern check:"
    Add-ReportLine -Path $reportPath -Text "- Required found: $($requiredFound -join ', ')"
    Add-ReportLine -Path $reportPath -Text "- Forbidden scan source: command output"
    if ($forbiddenFound.Count -gt 0) {
        Add-ReportLine -Path $reportPath -Text "- Forbidden found: $($forbiddenFound -join ', ')"
    } else {
        Add-ReportLine -Path $reportPath -Text "- Forbidden found: <none>"
    }
    if ($spawnExit -eq 0 -and $requiredFound.Count -eq $requiredPatterns.Count -and $forbiddenFound.Count -eq 0) {
        Add-ReportLine -Path $reportPath -Text ""
        Add-ReportLine -Path $reportPath -Text "结论: 真实 provider 非交互 spawn_agent 验证通过。"
    } else {
        Add-ReportLine -Path $reportPath -Text ""
        Add-ReportLine -Path $reportPath -Text "结论: 真实 provider 非交互 spawn_agent 验证未通过或证据不足。请检查 session/chat log。"
    }
} else {
    Add-ReportLine -Path $reportPath -Text "跳过 spawn_agent probe: -SkipSpawnAgentProbe"
}

Add-ReportLine -Path $reportPath -Text ""
Add-ReportLine -Path $reportPath -Text "## 4. 真实 Provider 非交互 spawn_team + wait_team 验证"
Add-ReportLine -Path $reportPath -Text ""

if (-not $SkipSpawnTeamProbe) {
    $teamTimeoutValue = "{0}s" -f $SpawnTeamTimeoutSeconds
    $teamPrompt = "请使用 spawn_team auto_start=true 创建 3 个 team 成员和 3 个 task，不要读取文件，也不要写文件。三个 task 分别基于这些内联短句各用一句中文总结：task-1 'AgentControl task graph 已成为 team task 的主写入路径'；task-2 'spawn_team 完成等待应使用 wait_team 而不是 wait_agent/read_agent_events'；task-3 '真实终端仍需验证 Ctrl+C 与 TUI 面板表现'。spawn_team 返回后，必须使用工具结果里的 team_id 调用 wait_team 等待团队完成和 team.summary，然后 parent 用不超过 120 字中文汇总。不要对 teammate id 调用 wait_agent 或 read_agent_events。"
    $teamCommand = "cd $aicliWorkDir`n.\$aicliFileName chat --provider $Provider --no-interactive --request-timeout $teamTimeoutValue --message `"$teamPrompt`""
    Add-ReportBlock -Path $reportPath -Language "powershell" -Text $teamCommand
    Push-Location $aicliWorkDir
    try {
        $teamOutput = & $resolvedAicliPath chat --provider $Provider --no-interactive --request-timeout $teamTimeoutValue --message $teamPrompt 2>&1
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
    $teamRequiredFound = Test-FileContainsAny -Path $teamSessionPath -Patterns $teamRequiredPatterns
    $teamOutputText = ($teamOutput | Out-String)
    $teamForbiddenFound = Test-TextContainsAny -Text $teamOutputText -Patterns $teamForbiddenPatterns
    Add-ReportLine -Path $reportPath -Text ""
    Add-ReportLine -Path $reportPath -Text "Session pattern check:"
    Add-ReportLine -Path $reportPath -Text "- Required found: $($teamRequiredFound -join ', ')"
    Add-ReportLine -Path $reportPath -Text "- Forbidden scan source: command output"
    if ($teamForbiddenFound.Count -gt 0) {
        Add-ReportLine -Path $reportPath -Text "- Forbidden found: $($teamForbiddenFound -join ', ')"
    } else {
        Add-ReportLine -Path $reportPath -Text "- Forbidden found: <none>"
    }
    if ($teamExit -eq 0 -and $teamRequiredFound.Count -eq $teamRequiredPatterns.Count -and $teamForbiddenFound.Count -eq 0) {
        Add-ReportLine -Path $reportPath -Text ""
        Add-ReportLine -Path $reportPath -Text "结论: 真实 provider 非交互 spawn_team + wait_team 验证通过。"
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
