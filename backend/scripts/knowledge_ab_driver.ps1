# A/B 驱动：设置 knowledge.mode → 启动节点 → 等 chat session 就绪 → 顺序跑任务集 → 记录 JSONL → 停节点。
# 用法：ab_driver.ps1 -Arm warm|off|on -Mode on|off
#
# 入库说明（收口轮八）：本脚本原为 .tmp\ab_driver.ps1（不入库），收口轮八随
#   reports/phase5_ab_report.md 一并迁入 backend/scripts\。默认路径全部相对脚本
#   位置推导，路径可用参数覆盖。
#   前置：-Exe 指向的二进制必须含被测知识层改动，先构建：
#     cd backend; go build -o aicli.exe ./cmd/aicli
#   两臂共用同一个 -Ws 工作区副本（知识库按 per-workspace 落在 -Ws\.aicli\knowledge）。
#   典型三段跑法见 docs/knowledge_Layer/reports/phase5_ab_report.md §7。
#
# 收口轮八修订（turn 阻塞教训，本脚本最关键的三处修复）：
#   * 空闲门：/web/api/invoke 返回 ≠ turn 结束。模型在一个 turn 里跑 14 分钟不返回
#     的 shell 时，invoke 225s 就带 status=requires_approval 返回，而 turn 仍 running；
#     后续任务全被排进 pending 队列 300ms 返回 0 token，整臂报废。故每次 invoke 前
#     校验 GET /web/api/turn 的 current.busy，busy 超时即 POST /web/api/input
#     {"type":"interrupt","discard_pending":true} 强杀并等它落地。
#   * 交互禁令（-PromptSuffix）：ask_user_question / enter_plan_mode 在无头环境
#     永久阻塞且不受 timeout_ms 约束（实测 6m17s），评测 prompt 必须显式禁用。
#   * 单任务预算（-TaskBudgetSec）：把"卡死"代价从整臂报废压到单任务失败。
#
# 收口轮八修订（上下文上限教训）：加 -SessionPerTask，每个任务开一个干净会话。
#   单会话连打 20 任务时上下文单调增长，第 10 个任务 in_tok 已达 998000（模型窗口
#   1M），随后 t10/t11 连续 240s 超时——后半程全是超时噪声，且 token 无法按任务
#   比较。每任务新会话后：切换耗时 ~0.8s，每任务 prompt 从干净上下文起算。
#
# 收口轮七修订（409 教训）：
#   1) 端口监听 ≠ 会话就绪。knowledge=on 时端口 ~2s 就监听，但 chat session
#      要到 ~16s 才注册（内部 knowledge 初始化阻塞）；这段窗口内
#      /web/api/invoke 一律 409 {"reason":"no active chat session"}。
#      旧驱动只在端口就绪后立刻连打 20 个 invoke → 全军覆没。故新增就绪门。
#   2) 记录 invoke 响应自带的 usage/turn_id/llm_observed（不再事后靠 usage ledger join），
#      并把 409 响应体一并落盘，便于事后归因。
param(
    [Parameter(Mandatory = $true)][string]$Arm,
    [Parameter(Mandatory = $true)][ValidateSet('on', 'off')][string]$Mode,
    # 留空则按脚本位置推导：<repo>/backend/scripts/knowledge_ab_driver.ps1 -> <repo>
    [string]$RepoRoot = '',
    [string]$Ws = "$env:TEMP\p6_ab\ws",
    [string]$Exe = '',
    [string]$Provider = 'opencode.ai',
    [string]$Model = 'space-bunny-free',
    [string]$SetPath = '',
    [string]$OutDir = '',
    [int]$TimeoutMs = 240000,
    [int]$ReadyTimeoutSec = 300,
    # 收口轮八：交互工具会让无头驱动永久挂死（实测模型调 ask_user_question
    # 阻塞 6m17s，invoke 不返回）。统一追加只读/不交互约束，两臂同口径。
    [string]$PromptSuffix = '',
    # 单任务总预算（跨全部attempt）：卡死任务最多花这么久，超时如实记失败。
    [int]$TaskBudgetSec = 150,
    [switch]$SessionPerTask
)

$ErrorActionPreference = 'Continue'
if (-not $RepoRoot) { $RepoRoot = (Resolve-Path (Join-Path $PSScriptRoot '..\..')).Path }
if (-not $Exe) { $Exe = Join-Path $RepoRoot 'backend\aicli.exe' }
if (-not $SetPath) { $SetPath = Join-Path $PSScriptRoot 'knowledge_ab_tasks.json' }
if (-not $OutDir) { $OutDir = Join-Path $RepoRoot '.tmp' }
New-Item -ItemType Directory -Force -Path $OutDir | Out-Null
New-Item -ItemType Directory -Force -Path "$Ws\.aicli" | Out-Null
Set-Content -Path "$Ws\.aicli\runtime.yaml" -Value "knowledge:`n  mode: $Mode`n  code_tools: on" -Encoding UTF8

$sw = [Diagnostics.Stopwatch]::StartNew()
$proc = Start-Process -FilePath $Exe `
    -ArgumentList '--yolo', '--pprof', '--debug', '--headless', '-p', $Provider, '-m', $Model `
    -WorkingDirectory $Ws -PassThru -WindowStyle Hidden
Write-Output "[$Arm] node pid=$($proc.Id) mode=$Mode provider=$Provider model=$Model"

$port = $null
for ($i = 0; $i -lt 120; $i++) {
    Start-Sleep -Milliseconds 700
    if ($proc.HasExited) { Write-Output "[$Arm] ERROR node exited early"; exit 2 }
    $conn = Get-NetTCPConnection -State Listen -ErrorAction SilentlyContinue |
        Where-Object { $_.OwningProcess -eq $proc.Id } | Select-Object -First 1
    if ($conn) { $port = $conn.LocalPort; break }
}
if (-not $port) { Write-Output "[$Arm] ERROR node did not listen"; exit 2 }
Write-Output "[$Arm] port=$port at $($sw.ElapsedMilliseconds)ms"

$token = (Invoke-RestMethod "http://127.0.0.1:$port/web/api/token").token
$headers = @{ 'X-AICLI-Token' = $token }

function Test-SessionReady {
    try {
        $s = Invoke-RestMethod "http://127.0.0.1:$port/web/api/status" -Headers $headers -TimeoutSec 20
        return ([string]$s.available -eq 'True' -or [string]$s.available -eq 'true')
    } catch { return $false }
}

# 收口轮八修订（turn 阻塞教训）：
#   /web/api/invoke 在 turn 卡住（模型正在跑一个 14 分钟不返回的 shell）时，
#   225s 就带 status=requires_approval 返回，而服务端 turn 仍在 running。
#   驱动若不检查会话是否 idle，后续 invoke 会把任务排进 pending 队列，
#   300ms 内全部记成 requires_approval + 0 token —— 上一轮 warm 臂 t09~t11
#   就是这样变成垃圾数据的（screen 上能看到 "Running shell (13m 55s)"）。
#   修法：每个任务 invoke 前过空闲门；busy 超时则 POST /web/api/input
#   {"type":"interrupt","discard_pending":true} 强杀当前 turn 并等它落地。
function Get-TurnState {
    try {
        $r = Invoke-RestMethod "http://127.0.0.1:$port/web/api/turn" -Headers $headers -TimeoutSec 20
        return [pscustomobject]@{ busy = [bool]$r.current.busy; turn = [string]$r.current.turn_id; pending = $r.current.pending_inputs }
    } catch { return [pscustomobject]@{ busy = $false; turn = ''; pending = 0 } }
}

function Stop-StuckTurn {
    param([int]$MaxWaitSec = 25)
    try {
        $null = Invoke-RestMethod -Method Post -Uri "http://127.0.0.1:$port/web/api/input" -Headers $headers `
            -ContentType 'application/json' -TimeoutSec 20 -Body '{"type":"interrupt","discard_pending":true}'
    } catch { }
    $swi = [Diagnostics.Stopwatch]::StartNew()
    while ($swi.ElapsedMilliseconds -lt $MaxWaitSec * 1000) {
        Start-Sleep -Milliseconds 800
        if (-not (Get-TurnState).busy) { return $true }
    }
    return $false
}

function Wait-TurnIdle {
    param([int]$MaxWaitSec = 20)
    $swi = [Diagnostics.Stopwatch]::StartNew()
    while ($swi.ElapsedMilliseconds -lt $MaxWaitSec * 1000) {
        $st = Get-TurnState
        if (-not $st.busy) { return $true }
        Start-Sleep -Milliseconds 700
    }
    Write-Output "[$Arm] WARN turn busy >${MaxWaitSec}s ($((Get-TurnState).turn)) -> interrupt"
    return (Stop-StuckTurn)
}

# 就绪门：端口可听 ≠ 会话可调；knowledge 初始化期间 invoke 恒 409。
$readyDeadline = $ReadyTimeoutSec * 1000
$ready = $false
while ($sw.ElapsedMilliseconds -lt $readyDeadline) {
    Start-Sleep -Milliseconds 1500
    if (Test-SessionReady) { $ready = $true; break }
}
if (-not $ready) { Write-Output "[$Arm] ERROR session not ready after ${ReadyTimeoutSec}s"; Stop-Process -Id $proc.Id -Force -EA SilentlyContinue; exit 4 }
Write-Output "[$Arm] session ready at $($sw.ElapsedMilliseconds)ms"

# 上游活性预检（收口轮七实测教训）：provider 配额耗尽时，invoke 依然返回
# status=completed 且 llm_observed=true —— 上一轮 60/60 "成功" 实为 60 次
# UPSTREAM_QUOTA_EXHAUSTED，全是假阳性。只有 assistant 正文非空才能证明
# 上游真的答了。故在跑任务集之前先打一发探针，失败即中止整臂。
$preflightOk = $false
for ($k = 1; $k -le 3 -and -not $preflightOk; $k++) {
    try {
        $pf = Invoke-RestMethod -Method Post -Uri "http://127.0.0.1:$port/web/api/invoke" -Headers $headers `
            -ContentType 'application/json' -TimeoutSec 180 `
            -Body (@{ prompt = '只回复 OK 两个字符，不要做任何其它事。'; timeout_ms = 120000 } | ConvertTo-Json -Compress)
        $pfTxt = ''
        if ($pf.assistant) { $pfTxt = [string]$pf.assistant.content }
        Write-Output "[$Arm] preflight#$k status=$($pf.status) assistant_len=$($pfTxt.Length) tot_tok=$($pf.usage.total_tokens)"
        if (([string]$pf.status -eq 'completed' -or [string]$pf.status -eq 'settled' -or [string]$pf.status -eq 'ok') -and $pfTxt.Trim().Length -gt 0) {
            $preflightOk = $true
        }
    } catch {
        $pfBody = ''
        try { $pfBody = [string]$_.ErrorDetails.Message } catch { $pfBody = '' }
        Write-Output "[$Arm] preflight#$k HTTP error body=$pfBody"
    }
}
if (-not $preflightOk) {
    Write-Output "[$Arm] ERROR upstream unusable: no assistant text in 3 preflight attempts (quota/auth?) - aborting arm"
    Stop-Process -Id $proc.Id -Force -EA SilentlyContinue
    exit 5
}
Write-Output "[$Arm] preflight OK at $($sw.ElapsedMilliseconds)ms"

$tasks = Get-Content $SetPath -Raw | ConvertFrom-Json
$out = Join-Path $OutDir "ab_run_$Arm.jsonl"
if (Test-Path $out) { Remove-Item $out }

$i = 0
foreach ($t in $tasks) {
    $i++

    # 每任务新会话（-SessionPerTask）：单会话连打 20 个任务时上下文单调增长，
    # 第 10 个任务就撞上模型 1M 窗口（实测 in_tok 998000，随后连续 240s 超时），
    # 之后的任务全是超时噪声，token 也无法按任务比较。改成每任务一个干净会话后：
    #   * 每任务 prompt token 从小上下文起算，off/on 可直接对比；
    #   * 知识层的跨任务复用走 per-workspace 的 knowledge.db（不依赖会话），
    #     被测机制仍然完整生效。
    if ($SessionPerTask -and $i -gt 1) {
        $prev = ''
        try { $prev = [string](Invoke-RestMethod "http://127.0.0.1:$port/web/api/sessions" -Headers $headers -TimeoutSec 20).current_session_id } catch { }
        try { $null = Invoke-RestMethod -Method Post -Uri "http://127.0.0.1:$port/web/api/sessions/new" -Headers $headers -ContentType 'application/json' -TimeoutSec 30 -Body '{}' } catch { }
        $swNew = [Diagnostics.Stopwatch]::StartNew()
        $switched = $false
        while ($swNew.ElapsedMilliseconds -lt $ReadyTimeoutSec * 1000) {
            Start-Sleep -Milliseconds 700
            try {
                $s2 = Invoke-RestMethod "http://127.0.0.1:$port/web/api/sessions" -Headers $headers -TimeoutSec 20
                $cur = [string]$s2.current_session_id
                if ($cur -and $cur -ne $prev) {
                    # 端口/会话已切换 ≠ 可 invoke：knowledge 初始化期间仍恒 409。
                    if (Test-SessionReady) { $switched = $true; break }
                }
            } catch { }
        }
        Write-Output "[$Arm] $i/$($tasks.Count) new session ready=$switched after $($swNew.ElapsedMilliseconds)ms"
    }

    $rec = $null
    $taskStart = Get-Date
    for ($attempt = 1; $attempt -le 3; $attempt++) {
        if (((Get-Date) - $taskStart).TotalSeconds -gt $TaskBudgetSec) {
            Write-Output "[$Arm] $i/$($tasks.Count) $($t.id) task budget >${TaskBudgetSec}s -> give up"
            $null = Stop-StuckTurn
            $rec = [ordered]@{
                arm = $Arm; mode = $Mode; idx = $i; id = $t.id; status = 'budget_exceeded'; session = ''
                turn = ''; llm_observed = $false
                in_tok = $null; out_tok = $null; tot_tok = $null
                elapsed_ms = 0; wall_ms = [int]((Get-Date) - $taskStart).TotalMilliseconds; assistant = ''
                expect_any = $t.expect_any; attempt = $attempt
                error = "task budget ${TaskBudgetSec}s exceeded (likely interactive tool blocking)"
            }
            break
        }
        # 空闲门：invoke 之前必须确认没有残留 turn（见文件头 turn 阻塞教训）。
        $null = Wait-TurnIdle -MaxWaitSec 20
        $swt = [Diagnostics.Stopwatch]::StartNew()
        try {
            $r = Invoke-RestMethod -Method Post -Uri "http://127.0.0.1:$port/web/api/invoke" -Headers $headers `
                -ContentType 'application/json' -TimeoutSec 140 `
                -Body (@{ prompt = ($t.prompt + $PromptSuffix); timeout_ms = $TimeoutMs } | ConvertTo-Json -Compress)
            $swt.Stop()
            $txt = ''
            if ($r.assistant) { $txt = [string]$r.assistant.content }
            $inTok = $null; $outTok = $null; $totTok = $null
            if ($r.usage) {
                $inTok = $r.usage.input_tokens
                $outTok = $r.usage.output_tokens
                $totTok = $r.usage.total_tokens
            }
            $rec = [ordered]@{
                arm = $Arm; mode = $Mode; idx = $i; id = $t.id; status = [string]$r.status; session = $r.session_id
                turn = $r.turn_id; llm_observed = $r.llm_observed
                in_tok = $inTok; out_tok = $outTok; tot_tok = $totTok
                elapsed_ms = $r.elapsed_ms; wall_ms = $swt.ElapsedMilliseconds
                assistant = $txt; expect_any = $t.expect_any; attempt = $attempt
            }
            # turn 阻塞回收：status 不是终态成功、或助手正文为空（通常是被
            # interrupt 掐断 / 上游无输出），强杀残留 turn + 换干净会话重试。
            $stale = ($r.status -eq 'requires_approval' -or $r.status -eq 'queued' -or [string]::IsNullOrWhiteSpace($txt))
            if ($stale -and $attempt -lt 3) {
                Write-Output "[$Arm] $i/$($tasks.Count) stale status=$($r.status) len=$($txt.Length) -> interrupt+new session+retry(attempt $($attempt+1))"
                $null = Stop-StuckTurn
                try { $null = Invoke-RestMethod -Method Post -Uri "http://127.0.0.1:$port/web/api/sessions/new" -Headers $headers -ContentType 'application/json' -TimeoutSec 30 -Body '{}' } catch { }
                Start-Sleep -Seconds 2
                continue
            }
            break
        } catch {
            $body = ''
            try { $body = [string]$_.ErrorDetails.Message } catch { $body = '' }
            $code = 'n/a'
            try { $code = [int]$_.Exception.Response.StatusCode } catch { }
            $swt.Stop()
            if ($code -eq 409) {
                # 会话未就绪/上一轮仍在跑：回退到就绪门再重试，不盲打。
                Write-Output "[$Arm] $i/$($tasks.Count) $($t.id) HTTP 409 body=$body -> wait+retry (attempt=$attempt)"
                Start-Sleep -Seconds 3
                continue
            }
            if ($attempt -eq 3) {
                $rec = [ordered]@{
                    arm = $Arm; mode = $Mode; idx = $i; id = $t.id; status = 'invoke_error'; session = ''
                    turn = ''; llm_observed = $false
                    in_tok = $null; out_tok = $null; tot_tok = $null
                    elapsed_ms = 0; wall_ms = $swt.ElapsedMilliseconds; assistant = ''
                    expect_any = $t.expect_any; attempt = $attempt
                    error = "http=$code $($_.Exception.Message) $body"
                }
            }
        }
    }
    if (-not $rec) { $rec = [ordered]@{ arm = $Arm; mode = $Mode; idx = $i; id = $t.id; status = 'invoke_error'; session = ''; turn = ''; llm_observed = $false; in_tok = $null; out_tok = $null; tot_tok = $null; elapsed_ms = 0; wall_ms = 0; assistant = ''; expect_any = $t.expect_any; attempt = 3; error = 'exhausted retries (409)' } }
    ($rec | ConvertTo-Json -Compress -Depth 6) | Add-Content -Path $out -Encoding UTF8
    Write-Output "[$Arm] $i/$($tasks.Count) $($t.id) status=$($rec.status) llm=$($rec.llm_observed) in_tok=$($rec.in_tok) wall=$($rec.wall_ms)ms"
}

Stop-Process -Id $proc.Id -Force -ErrorAction SilentlyContinue
Write-Output "[$Arm] done (node stopped)"