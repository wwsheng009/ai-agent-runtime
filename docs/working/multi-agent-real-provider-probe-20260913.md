# 真实 Provider 多 Agent 探针记录（opencode.ai / deepseek-v4.1-flash / reasoning_effort=max）

生成时间: 2026-09-13
仓库: `E:\projects\ai\ai-agent-runtime`（工作树含未提交改动，基线 commit `b19cf11b`）
Provider: `opencode.ai`（`base_url=https://opencode.ai/zen/go`，`api_path=/v1/chat/completions`，兼容档 `opencode-console-go-2026-07`）
Model: `deepseek-v4.1-flash`
ReasoningEffort: `max`

## 0. 前置校验

| 项 | 结果 | 证据 |
| --- | --- | --- |
| 凭证可用 | 是 | `~/.local/share/opencode/auth.json` 的 `opencode-go` 条目（type=api） |
| 网关模型清单含目标模型 | 是 | `GET https://opencode.ai/zen/go/v1/models` → 200，37 个模型，含 `deepseek-v4.1-flash` |
| 用户配置默认值 | 一致 | `~/.aicli/config.yaml:3-5` = `default_provider: opencode.ai` / `default_model: deepseek-v4.1-flash` / `reasoning_effort: max`；`:7714` `supported_models` 含该模型 |
| 本地产物 | `backend/aicli.exe` 于本轮 `go build -o aicli.exe ./cmd/aicli` 重新构建（exit 0） | — |

## 1. T1 provider smoke（单轮真实往返）

命令（工作目录 `backend`）：

```powershell
.\aicli.exe chat --provider opencode.ai --model deepseek-v4.1-flash --reasoning-effort max `
  --no-interactive --output json --stream=false --request-timeout 240s --message "请只回复 OK。"
```

结果：exit 0，`response="OK"`，`session_id=session_20260913151652_3iqJ63HA`（`total_tokens=13945`）。

线级证据（`%USERPROFILE%\.aicli\chat-logs\2026\09\13\20260913_151652_033_16556dfc.http\001_request_provider_wrapper.json`）：

- `url=https://opencode.ai/zen/go/v1/chat/completions`、`model=deepseek-v4.1-flash`
- 请求体 `body_json.model=deepseek-v4.1-flash`、`body_json.reasoning_effort=max`、`stream=false`
- `requested_reasoning_effort=max` / `effective_reasoning_effort=max`（请求与生效一致）
- 请求元数据记录 `compatibility_profile=opencode-console-go-2026-07`、暴露工具 40 个（多 agent 工具在场）

结论：**通过**。`reasoning_effort=max` 确实落到了线上请求体，而不是只停留在回显字段。

## 2. T2 spawn_agent + wait_agent + 相同 after_seq 重复读（P1-7 真机探针）

命令（工作目录 `backend`，产物落在仓库内便于复读）：

```powershell
.\aicli.exe chat --provider opencode.ai --model deepseek-v4.1-flash --reasoning-effort max `
  --no-interactive --output json --stream=false --yolo --request-timeout 600s `
  --session-dir E:\projects\ai\ai-agent-runtime\output\real-test\sessions `
  --log-dir E:\projects\ai\ai-agent-runtime\output\real-test\logs `
  --title real-multiagent-probe --message "<五步脚本：spawn_agent → wait_agent → read_agent_events ×2（参数完全相同）→ 报告 unchanged/repeat_count>"
```

结果：exit 0，`session_id=session_20260913151710_37ifVcDW`（`total_tokens=78533`），模型自述第二次读 `unchanged=true`、`repeat_count=1`。

模型实际工具序列（从 `output/real-test/logs/2026/09/13/20260913_151710_850_a227736e.http/007_request_provider_wrapper.json` 的 messages 还原，与自述一致）：

| 步骤 | 工具调用 | 结果要点 |
| --- | --- | --- |
| 1 | `spawn_agent({"id":"a1","message":"只回复：子任务完成。"})` | 子会话 `path=/root/a1`、`depth=1`，且继承 `provider=opencode.ai` / `model=deepseek-v4.1-flash` / `reasoning_effort=max` |
| 2 | `wait_agent({"id":"a1","timeout_ms":60000})` | 返回 agent 快照，子 agent 结束后 idle |
| 3 | `read_agent_events({"after_seq":0,"id":"a1","limit":50})` | `count=4`、`latest_seq=4`，**无** `unchanged`/`repeat_count`，`next_action=consume_events...` |
| 4 | `read_agent_events({"after_seq":0,"id":"a1","limit":50})`（完全相同参数） | 同一窗口 + `"unchanged": true`、`"repeat_count": 1`，`next_action=unchanged_window: identical read #1 with after_seq=0 returned the same window (no new events)...` |

关键 A/B（同一请求体内的两条 tool message，参数逐字节相同、有效载荷相同）：

- `message[7]`（第一次读）：`"count": 4, "latest_seq": 4` + `consume_events` 引导
- `message[9]`（第二次读）：`"count": 4, "latest_seq": 4, "unchanged": true, "repeat_count": 1` + `unchanged_window` 引导

结论：**通过**。重复读的显式信号由运行时产生并随工具结果回到模型侧，不是模型臆测；`unchanged` 只在窗口未推进时出现，第一次读干净无标记。

复现校验脚本（本轮临时产物，随 `output/real-test/` 一并保留）：`output/real-test/verify_probe.py`（按 needle 打印请求体中的 tool 结果窗口）、`output/real-test/list_calls.py`（列出 assistant tool_calls 与 tool 结果摘要）。

## 3. T3 spawn_team + wait_team

命令（工作目录 `backend`，后台作业 `job_ref_26dfee42a529`）：

```powershell
.\aicli.exe chat --provider opencode.ai --model deepseek-v4.1-flash --reasoning-effort max `
  --no-interactive --output json --stream=false --yolo --request-timeout 900s `
  --session-dir E:\projects\ai\ai-agent-runtime\output\real-test\sessions `
  --log-dir E:\projects\ai\ai-agent-runtime\output\real-test\logs `
  --title probe-team --message "<spawn_team → wait_team ×2 → 汇总>"
```

结果：父侧 `exit code 1`（`{"ok":false,...,"error":"非交互模式（--no-interactive）无法回答运行时提问..."}`），团队两个 task 均 `failed`。**本条未通过，且未通过的原因不在团队编排逻辑**。

### 3.1 时间线（CST，证据见下）

| 时间 | 事件 | 证据 |
| --- | --- | --- |
| 15:18:10 | 运行态出现 `Running ask_user_question`（模型先追问） | `logs/2026/09/13/20260913_151801_971_fd1158a1.debug.log:11` |
| 15:18:18 | `spawn_team` 创建 team `probe-team`（2 teammate + 2 task），`task.dispatch.requested/started`（team_seq 7-8） | `team_store.sqlite: agent_control_task_graph_events` |
| 15:18:19–15:22:38 | task `lease_renewed` 约 5s 一次（running） | `team_store.sqlite: team_task_signals` seq 108-110 |
| 15:22:42/43 | `task.failed` + `task.released`（team_seq 10-12）；teammate → `idle`，agent/session → `closed`（`reclaimed:session_terminal`） | 同上 + `agent_control.sqlite: agent_control_agent_wake_events` id 54-57 |
| 15:22:43 | `wait_team` 第 3 次调用返回 `status=failed`、`Team is terminal` + summary（最新 seq=15） | `session_messages` seq 11 |
| 15:22:48 | 进程退出：`[goal] error recovery skipped reason=no_interactive`，命令返回 `ok:false`、exit 1 | `debug.log:74`、后台作业输出 |

### 3.2 子任务失败根因（两条独立证据）

1. 任务记录（`output/real-test/sessions/runtime/team_store.sqlite: agent_control_task_records`）：

   - `route_provider=vsllm2_codex`、`route_model=gpt-5.4-mini`、`route_reasoning_effort=low`、`route_source=difficulty_level`、`fallback_used=0`、`attempt=1`
   - `summary=provider call failed after repeated fast-fail retries: provider transport stream failed after retries: failed to send request: timeout awaiting response headers (response-header guard after 20s)`
2. 本机直连探测 `GET https://vsllm.com/v1/models`：**20s 超时**（`20215ms`，`Invoke-WebRequest` 取消），与运行时记录的 20s header guard 完全一致。

路由来源（用户全局配置 `C:\Users\vince\.aicli\config.yaml`）：

- `:19-30` `aicli.subagents.routing.levels.easy = {provider: vsllm2_codex, model: gpt-5.4-mini, reasoning_effort: low}`（`enabled: true`）
- `:3040+` `providers.vsllm2_codex` → `base_url: https://vsllm.com`、`api_path: /v1/responses`、`api_key_ref: vsllm2`
- 该配置未设置 `aicli.teams.routing`，因此团队任务按 `backend/internal/agentconfig/config.go:629-642`（`EffectiveTeamRoutingConfig`）继承 subagents 路由

结论：**子任务按难度路由到 `vsllm2_codex`，而该 provider 的本机可达性为 0**，所以 teammate 的真实调用在 20s 后快速失败；这不是 team/wait_team 语义缺陷。

### 3.3 收敛行为（正向证据）

失败后运行时行为符合设计：任务 `lease` 释放、teammate 回到 `idle`、agent/session 置 `closed`（`reclaimed:session_terminal`）、`wait_team` 返回终态 `failed` + summary，没有遗留 running 任务或泄漏会话。

### 3.4 附带发现：非交互模式下的模型提问

- 模型在同一轮内调用过 `ask_user_question`（`session_messages` seq=3，`arguments.prompt` 含 A/B/C 选项），`--no-interactive` 无法回答，于是整轮以 `ok:false` + exit 1 结束，即使模型随后仍产出了最终答复（HTTP `006_response` = 200、`finish_reason=stop`）。
- 触发原因在本轮探针自身：`session_messages` seq=2 的用户消息**只有 132 字符**，末尾停在 `task-1 基于内联短句`，即内联 `--prompt` 传长中文文本被截断。Windows 内联传长 prompt 不可靠；仓内已有 stdin 传参的稳妥做法（`backend/internal/toolkit/tools/aicli_exec.go:41`）。

## 4. 结论与仍待做项

| 项 | 结果 | 说明 |
| --- | --- | --- |
| T1 provider smoke（单轮真实往返） | 通过 | 线上请求体 `model=deepseek-v4.1-flash`、`reasoning_effort=max` |
| T2 spawn_agent + wait_agent + 相同 `after_seq` 重复读（P1-7） | 通过 | 第二次读由运行时返回 `unchanged=true`、`repeat_count=1`，第一次读无标记 |
| T3 spawn_team + wait_team | 未通过（受阻） | 子任务难度路由指向不可达 provider（`vsllm2_codex` / `https://vsllm.com`），团队链路按设计收敛到 failed |
| T3-b spawn_team + wait_team（路由改指可达 provider） | 通过 | 2 个 teammate 真实多轮往返，2 个 task `done`，team 终态 `done` |

上述三项已在同轮闭环：

1. **路由目标可达性预检** → 写入 `docs/plan/multi-agent-real-terminal-validation-runbook.md` §2.1（含 `aicli.subagents.routing` → `aicli.teams.routing` 继承关系与三种处置方式）；本轮用局部配置副本把 `easy` 改指可达 provider 后跑到 `done`（见 §5）。
2. **环境阻断 vs 逻辑回归分类** → `scripts/validate-multi-agent-real-terminal.ps1:629-669`（team 分支）、`:504-574`（spawn_agent 分支）新增 `Route target blocked signals` 检测（`response-header guard after 20s` / `timeout awaiting response headers` / `provider transport stream failed` / `provider call failed after repeated fast-fail retries`），命中时报告输出环境阻断结论与处置建议，而非判为团队语义回归。
3. **超长 prompt 传递** → 本轮全部改为「先写文件、再 `Get-Content -Raw` 传 `--message`」；后续探针继续沿用。

## 5. T3-b 复跑（路由改指可达 provider）

### 5.1 配置与命令

- 局部配置：`output/real-test/config.team-probe.yaml`（用户全局配置的副本，仅两处差异：新增 `aicli.teams.routing`，`levels.easy → opencode.ai / deepseek-v4.1-flash / max`；内联 `api_key` / `sk-*` 值已脱敏为占位符；**用户原始 `C:\Users\vince\.aicli\config.yaml` 未改动**）
- prompt 落文件后再读取，避免内联传参被截断：`output/real-test/prompt-team.txt`

```powershell
$msg = Get-Content -Raw E:\projects\ai\ai-agent-runtime\output\real-test\prompt-team.txt
.\aicli.exe chat --config E:\projects\ai\ai-agent-runtime\output\real-test\config.team-probe.yaml `
  --provider opencode.ai --model deepseek-v4.1-flash --reasoning-effort max `
  --no-interactive --output json --stream=false --yolo --request-timeout 900s `
  --session-dir E:\projects\ai\ai-agent-runtime\output\real-test\sessions `
  --log-dir E:\projects\ai\ai-agent-runtime\output\real-test\logs `
  --title probe-team2 --message $msg
```

### 5.2 结果（exit 0）

- `session_id=session_20260913152747_FmjIHaLC`，`total_tokens=44372`，`average_response_time_ms=2244`，`effective_provider=opencode.ai` / `effective_model=deepseek-v4.1-flash` / `effective_reasoning_effort=max`
- 模型自述：首次 `wait_team` 即进入终态 `done`（未触发第二次等待），两个 task 都交付了一行中文短句

独立核验（不采信模型自述）：

| 核验项 | 证据 |
| --- | --- |
| task 终态 | `team_store.sqlite: agent_control_task_records` → `task-1_v2` / `task-2_v2` 均 `status=done`，summary 为「子任务一完成。」「子任务二完成」（后者附结构化状态块） |
| teammate 真实往返 | `session_history.sqlite: sessions` → `probe-team2__member_1` message_count=14、`probe-team2__member_2` message_count=16，`state=closed`，会话 summary 即交付短句 |
| teammate 实际出网路由 | `session_messages` seq=1（teammate system prompt）→ Provider `opencode.ai`、Model `deepseek-v4.1-flash`、Reasoning effort `max`、Route source `fallback`、Fallback reason `model_unsupported_parent`、Warnings `model_unsupported; model_fallback_parent; reasoning_effort_capability_unknown` |
| 结构化收尾契约 | member-1 先出现一次 `report_task_outcome` 参数校验失败（`TOOL_INVALID_ARGS`，缺必需字段），随后自我纠正并成功上报 `Task outcome: done` |

### 5.3 过程观察

1. 我显式配置的 `easy → opencode.ai/deepseek-v4.1-flash` 被能力校验判为 `model_unsupported`，按 `inherit_parent_when_missing=true` 回退到父路由（`backend/internal/modelrouting/resolver.go:366-380`），`route_source` 记为 `fallback`。因此本轮验证的是 **team 编排 + teammate 真实往返**；「难度路由到另一个可达 provider」这条路径**未被验证**（需要在配置里为该 provider 声明模型能力）。
2. 任务 ID 跨团队去重：`task-1`/`task-2` 已被 probe-team 占用，probe-team2 得到 `task-1_v2`/`task-2_v2`（`backend/internal/toolbroker/broker.go:2909-2922`，`GetTask` 未按 team 过滤）；teammate prompt 里 `Task ID=task-2_v2`、`Title=task-2`。
3. `reasoning_effort=max` 在能力目录未知时只记 `reasoning_effort_capability_unknown` 警告而不降级（`resolver.go:391-393`），teammate 实际以 `max` 出网。

## 6. 验证脚本加固（同轮完成并复跑验证）

首轮 T3 失败暴露出脚本自身的缺陷，已修复：

| # | 缺陷 | 修复 |
| --- | --- | --- |
| 1 | 捕获子进程输出时按 GBK 解码，中文变乱码，报告不可读、模式匹配失真 | 脚本启动时把 `[Console]::OutputEncoding` 置为 UTF-8，收尾还原（`scripts/validate-multi-agent-real-terminal.ps1:25-30`、`:712-714`） |
| 2 | 环境阻断判定只看 parent 最终输出与 `session_*.json`（后者已不再生成），必然判不出来 | 新增 `Get-RecentBlockedEvidence`（`:88`）：按「session 文件 → `team_store.sqlite: agent_control_task_records`（只看探针开始后更新的行）→ chat debug log」回查，报告输出 `Route target blocked evidence` |
| 3 | required 模式只扫 `session_*.json`，导致「通过」分支实际不可达 | required 模式改为先扫探针输出、再用 session 文件补全（`:463-468`、`:620-624`），报告标签改为 `Pattern check:` |
| 4 | artifact 摘要只能报 `<not found>`，误导排查 | 明确提示会话正文由 `session_history.sqlite` 承载，并打印该库路径与更新时间 |
| 5 | spawn_agent 分支只看父进程输出，父进程看得到三个工具名但子 agent 零产出时被记为「验证通过」（假绿） | 新增 `Get-RecentChildAgents`（`:155`）与 `Get-SessionMessageCounts`（`:220`），报告输出 `Child agent count/routes/row/output`；判定改为必须「子 agent 数 > 0 且全部子 agent 有产出」才判通过（`:474-579`） |
| 6 | 子 agent 的环境阻断（按难度路由到本机不可达 provider）在 spawn_agent 分支不分类 | 与 team 分支同口径：`Child agent routes` 与父 provider 分叉（或命中传输层信号）时输出「归类: 环境阻断」+ 处置建议（`:570-574`） |
| 7 | 真绿场景被判「证据不足」：非交互 stdout 不回显 tool_calls，required 模式又只扫 stdout 与 `session_*.json` | required 证据三源合并：stdout + 父会话 transcript（`Get-SessionToolCallNames`，`:193`，只扫 `role=assistant` 的 `tool_calls`）+ 子 agent 行（`spawn_agent` 的 assistant 行并行 spawn 时不落库），报告输出 `Required evidence source` / `Parent session` / `Parent tool calls` |
| 8 | 探针 prompt 产出的子任务被模型标成 `difficulty=easy`，必然命中唯一一条不可达路由，正向分支永远跑不到 | 新增 `-SpawnChildDifficulty`（默认 `normal`）：prompt 要求模型在 `spawn_agent` 调用里显式传该难度，走 `inherit_parent_when_missing` 继承父 provider（`:12`、`:436`） |
| 9 | team 分支的环境阻断只能靠手工 `--config` 绕开，脚本自身无法在不改用户全局配置的前提下把 team 正向分支跑通 | 新增 `-ConfigPath`（`:7`、`:353-357`）：把 `--config` 透传给三个探针并在报告头部输出 `Config:` 行，可直接指向局部副本 `output/real-test/config.team-probe.yaml` |
| 10 | `--no-interactive` 探针里模型在收尾阶段调用提问/确认类工具，本轮以 exit 1 结束（`非交互模式（--no-interactive）无法回答运行时提问`），team 正向分支被卡住且报告显示 `<none>` 环境阻断信号，易被误读 | 两个探针 prompt（`:436`、`:593`）追加「不要向用户提问或请求确认，也不需要其它收尾动作，汇总后直接结束」 |

复跑证据（`-SkipBuild -SkipProviderSmoke -SkipSpawnAgentProbe`，即只跑 team 分支；用户全局配置未改动，路由仍指向不可达的 `vsllm2_codex`）：

- 报告：`docs/working/multi-agent-real-terminal-validation-20260913-153917.md`
- 输出已可读（UTF-8 修复生效），parent 原文含「团队状态：`failed`（终态），team.summary 已生成……」
- 判定命中（该报告第 53-58 行）：

```text
- Route target blocked signals: response-header guard after 20s, timeout awaiting response headers, provider transport stream failed, provider call failed after repeated fast-fail retries
- Route target blocked evidence: team_store.sqlite -> task: task-2_v2 | inline-summary-team_v2 | failed | vsllm2_codex/gpt-5.4-mini | provider call failed after repeated fast-fail retries: provider transport stream failed after retries: failed to send request: timeout awaiting response headers (response-header guard after 20s)
结论: spawn_team + wait_team 未通过，且属于环境阻断: 子任务路由目标的 provider 在本机不可达（transport 快速失败 / 20s response-header guard）。
```

同样的失败，修复前被记为「未通过或证据不足」，修复后被正确归类为**环境阻断**并给出可执行处置建议。另注意本轮 team_id 为 `inline-summary-team_v2`，是跨轮次的 team id 去重结果（`backend/internal/toolbroker/broker.go:2909-2922` 同源机制）。

### 6.1 spawn_agent 假绿根因（2026-09-13 15:52 那次探针）

15:52 那次 spawn_agent 探针被脚本判为「验证通过」，但两个子 agent 实际零产出。根因是判定口径：只扫父进程输出。父进程确实调用了 `spawn_agent` / `wait_agent` / `read_agent_events`，三个工具名都在输出里，而子 agent 的传输失败**不会**出现在父进程输出中。

子 agent 为何零产出（确定性解释）：子 agent 不继承父会话 provider，而是按难度路由。用户全局配置 `~/.aicli/config.yaml:18-29` 只定义了 `subagents.routing.levels.easy`（`vsllm2_codex/gpt-5.4-mini`，本机不可达），其余难度靠 `inherit_parent_when_missing: true` 回退父路由：

| 探针 | 子 agent | difficulty / source | route_source | effective provider/model | 结果 |
| --- | --- | --- | --- | --- | --- |
| §2 手工探针（`--session-dir output/real-test/sessions`） | `a1` | `normal` | `parent_inherit` | `opencode.ai/deepseek-v4.1-flash` | 子 agent 正常产出（`count=4` 事件） |
| §3 脚本探针（15:52） | `session_20260913155248/49_*` | `easy` / `explicit` | `difficulty_level` | `vsllm2_codex/gpt-5.4-mini` | 零产出、`message_count=1` |

即「内联短句汇总」这种探针 prompt 会被模型在 `spawn_agent` 调用里显式标成 `difficulty=easy`，正好命中唯一一条指向不可达 provider 的路由。证据来源：`~/.aicli/sessions/runtime/agent_control.sqlite` 的 `agent_control_agents`（`difficulty` / `difficulty_source` / `route_source` / `requested_*` / `effective_*` 列）。

离线复算（只读，从真实脚本里提取证据函数后对历史探针窗口复算判定输入，同时验证时间窗隔离）：

```text
extracted: Invoke-SqliteQuery, Get-RecentBlockedEvidence, Get-RecentChildAgents, Get-SessionMessageCounts, Test-FileContainsAny
=== spawn-probe-155243 (UTC 2026-09-13T07:52:40 ~ 07:58:00) ===
  child rows        : 2
  child sessions    : session_20260913155248_MllQ6Yhw, session_20260913155249_NkghZFi6
  child routes      : vsllm2_codex/gpt-5.4-mini
  msgs[...MllQ6Yhw] = 1   msgs[...NkghZFi6] = 1
  withOutput>=2     : 0
  childrenIncomplete: True   routeDiverged: True
=== spawn-probe-154632 (UTC 2026-09-13T07:46:30 ~ 07:52:00) ===
  child rows        : 2  (agent-a, agent-b)
=== team-probe-153918 (UTC 2026-09-13T07:39:20 ~ 07:46:00) ===
  child rows        : 0   <- team 成员被 team_id 过滤排除，不会污染 spawn 分支证据
```

脚本：`output/real-test/verify_spawn_child_evidence.ps1`（用 AST 从真实脚本抽取 `Get-RecentChildAgents` / `Get-SessionMessageCounts` 等函数后复算，避免复制粘贴漂移）。窗口隔离的必要条件有三个：`created_at` 上下界夹住探针时间窗、root 行同期创建、`team_id` 为空（team 成员的 `agent_path` 同样落在 `/root/...` 下，只能靠 `team_id` 区分）。

端到端复跑（16:01:56 启动，`-SkipBuild -SkipProviderSmoke -SkipSpawnTeamProbe`，用户全局配置未改动）：报告 `docs/working/multi-agent-real-terminal-validation-20260913-160156.md` 第 39-45 行

```text
- Child agent count: 2
- Child agent routes: vsllm2_codex/gpt-5.4-mini
- Child agent row: /root/sum-agent-a | vsllm2_codex/gpt-5.4-mini | difficulty=easy | route=difficulty_level | fallback=0 | warnings=[] | closed | session=sum-agent-a
- Child agent row: /root/sum-agent-b | vsllm2_codex/gpt-5.4-mini | difficulty=easy | route=difficulty_level | fallback=0 | warnings=[] | closed | session=sum-agent-b
- Child agent output: 0/2 个子 agent session 的 message_count >= 2
结论: 真实 provider 非交互 spawn_agent 验证未通过或证据不足。请检查 session/chat log。
```

该次运行的父进程自身 `context deadline exceeded`（exit 1），所以落在「未通过或证据不足」分支；关键是**没有再出现假绿**，且子 agent 证据在真实数据上正确落位（`sum-agent-a`/`sum-agent-b` 是模型自取的 id，与之前几轮的 `agent-a`、`session_...` 命名不同，说明证据是按时间窗而不是按 id 形状取的）。

不改用户全局配置的处置方式：脚本新增 `-SpawnChildDifficulty`（默认 `normal`，可选 `easy|normal|hard|expert`），探针 prompt 会让模型在 `spawn_agent` 调用里显式传该难度，从而走 `inherit_parent_when_missing` 继承父 provider，而不是命中不可达的 `levels.easy`；也可用 `--config <copy>` 关闭 `subagents.routing.enabled`。

### 6.2 正向分支复跑（16:08，子 agent 继承父 provider）

先给脚本补 `-SpawnChildDifficulty`（默认 `normal`）：探针 prompt 要求模型在 `spawn_agent` 调用里显式传难度，于是 `inherit_parent_when_missing` 生效，子 agent 用父会话 provider 运行。报告 `docs/working/multi-agent-real-terminal-validation-20260913-160811.md`：

```text
ExitCode: 0
两个子 agent 均以 normal 难度并行运行并成功结束（各 1 步、无工具调用、session_end success）。
- Child agent routes: opencode.ai/deepseek-v4.1-flash
- Child agent row: /root/agentA | opencode.ai/deepseek-v4.1-flash | difficulty=normal | route=parent_inherit | fallback=0 | warnings=["reasoning_effort_capability_unknown"] | closed | session=agentA
- Child agent row: /root/agentB | opencode.ai/deepseek-v4.1-flash | difficulty=normal | route=parent_inherit | fallback=0 | warnings=["reasoning_effort_capability_unknown"] | closed | session=agentB
- Child agent output: 2/2 个子 agent session 的 message_count >= 2
```

由此确定两件事：

1. `message_count >= 2` 是「子 agent 有产出」的正确分界：有产出的 `agentA`/`agentB` 为 2，零产出的子 agent（155243、154632、160156 各轮）全为 1。
2. 该次运行判定仍是「未通过或证据不足」——`Required found:` 为空。原因是**非交互 stdout 不回显 tool_calls**（该次 stdout 只有父进程最终答复），而 required 模式此前只扫 stdout 与 `session_*.json`。回查父会话 transcript（`session_history.sqlite: session_messages`，`role=assistant`）后可见：`wait_agent` / `read_agent_events` 有记录，但 `spawn_agent` 的 assistant 行在并行 spawn 时不落库（155243、160811 两次都如此）。因此 required 证据改为三源合并：

| 需要的证据 | 来源 |
| --- | --- |
| `spawn_agent` | `agent_control.sqlite` 的 `agent_control_agents` 子 agent 行（`depth=1` 且 `team_id` 为空，只能由 `spawn_agent` 产生） |
| `wait_agent` / `read_agent_events` | 父会话 transcript 的 assistant `tool_calls`（stdout 继续作为补充） |

报告新增 `Required evidence source` / `Parent session` / `Parent tool calls` 三行，便于人工复核证据来自哪一路。

### 6.3 正向分支闭环（16:12，三源证据生效）

三源合并落地后用同一命令复跑（`-SkipBuild -SkipProviderSmoke -SkipSpawnTeamProbe`，16:12:50 启动，用户全局配置未改动），报告 `docs/working/multi-agent-real-terminal-validation-20260913-161250.md` 第 45-59 行：

```text
Pattern check:
- Required found: wait_agent, spawn_agent, read_agent_events
- Required evidence source: command output + session_history.sqlite（父会话 tool_calls）+ agent_control.sqlite（子 agent 行）
- Parent session: session_20260913161251_6MJ5uzxa
- Parent tool calls: list_agents, spawn_agent, wait_agent, read_agent_events
- Child agent count: 2
- Child agent routes: opencode.ai/deepseek-v4.1-flash
- Child agent row: /root/session_20260913161306_QDvBzH3o | opencode.ai/deepseek-v4.1-flash | difficulty=normal | route=parent_inherit | fallback=0 | warnings=["reasoning_effort_capability_unknown"] | closed
- Child agent row: /root/session_20260913161306_X4QZhXak | opencode.ai/deepseek-v4.1-flash | difficulty=normal | route=parent_inherit | fallback=0 | warnings=["reasoning_effort_capability_unknown"] | closed
- Child agent output: 2/2 个子 agent session 的 message_count >= 2
结论: 真实 provider 非交互 spawn_agent 验证通过。
```

与 16:08 那次的差别有两点，都能在报告字段里核到：

1. required 证据不再依赖 stdout 回显 tool_calls：`Required evidence source` 显式写出「父会话 transcript + 子 agent 行」两路补充，`Parent tool calls` 列出四条真实调用（`list_agents` 在前，说明这是模型自发的探查顺序）。
2. 该次 `spawn_agent` 的 assistant 行**落库了**（`Parent tool calls` 含 `spawn_agent`），不再需要子 agent 行单独反证；子 agent 行仍作为独立证据核对 `difficulty=normal` / `route=parent_inherit` / `2/2 有产出`。

离线复算（`output/real-test/verify_spawn_child_evidence.ps1` 增加 `spawn-probe-161250` 窗口后实跑，全部只读）：

```text
=== spawn-probe-161250 (UTC 2026-09-13T08:12:50 ~ 08:20:00) ===
  child rows        : 2
  child sessions    : session_20260913161306_QDvBzH3o, session_20260913161306_X4QZhXak
  child routes      : opencode.ai/deepseek-v4.1-flash
  msgs[...QDvBzH3o] = 2   msgs[...X4QZhXak] = 2
  withOutput>=2     : 2
  childrenIncomplete: False
  routeDiverged     : False
=== spawn-probe-161250 parent transcript (UTC 08:12:50 ~ 08:20:00) ===
  root sessions    : session_20260913161251_6MJ5uzxa
  transcript tools : list_agents, spawn_agent, wait_agent, read_agent_events
  required[spawn_agent] = True   required[wait_agent] = True   required[read_agent_events] = True
```

同批复算的对照组：`spawn-probe-160811` 的 transcript tools 为 `wait_agent, read_agent_events`（`spawn_agent` 未落库，与 §6.2 判断一致）；`spawn-probe-155243` 为 `wait_agent, read_agent_events, close_agent`，且两个子 agent `message_count` 均为 1、`routeDiverged=True`（假绿样本）。

顺带补了 team 分支的「不改用户全局配置」路线：脚本新增 `-ConfigPath`（把 `--config` 透传给三个探针，报告头部输出 `Config:` 行），可直接指向局部副本 `output/real-test/config.team-probe.yaml`（该副本把 `levels.easy` 改指 `opencode.ai/deepseek-v4.1-flash`，用户全局配置未改动）。

至此 spawn_agent 分支的「验证通过」结论从不可达变为可达，且假绿（15:52 零产出被记通过）与假红（16:08 真产出被记证据不足）两侧都有真实数据覆盖。

### 6.4 非交互探针必须显式禁止提问（16:15 team 探针暴露的新失败模式）

16:15:46 那次整轮复跑（spawn_agent + team 两个探针，带 `-ConfigPath`）里，spawn_agent 分支再次判「验证通过」（报告 `docs/working/multi-agent-real-terminal-validation-20260913-161546.md` 第 43-50 行：`Child agent count: 2`、`note-a`/`note-b` 两行 `difficulty=normal` / `route=parent_inherit`、`2/2 有产出`、`Required evidence source` 为命令输出 + 父 transcript + 子 agent 行，父 transcript 含 `spawn_agent, list_agents, wait_agent, read_agent_events`）。team 分支则暴露了一个与路由无关的新失败模式：

```text
ExitCode: 1
Error: 操作错误: 非交互模式（--no-interactive）无法回答运行时提问；prompt=（占位确认）是否需要我在总结之外做其他动作？若无需回复，我将直接以结构化状态块结束任务。
```

即模型在收尾阶段调用了提问/确认类工具，`--no-interactive` 无法回答，整个探针以 exit 1 结束。报告把 `Route target blocked signals` 记为 `<none>`（确实不是环境阻断），结论落在「未通过或证据不足」——分类正确，但正向分支跑不出来。

处置：两个探针 prompt（`scripts/validate-multi-agent-real-terminal.ps1:436`、`:593`）追加「不要向用户提问或请求确认，也不需要其它收尾动作，汇总后直接结束」，在 prompt 层把这个行为约束掉。

加约束后只复跑 team 探针（`-SkipSpawnAgentProbe`，16:19:15 启动，仍用 `-ConfigPath` 局部副本），报告 `docs/working/multi-agent-real-terminal-validation-20260913-161915.md`：

```text
ExitCode: 0
团队 team-summary-3 已完成（3/3 tasks done），team.summary 已就绪：...
Pattern check:
- Required found: spawn_team, wait_team, team.summary
- Forbidden found: <none>
- Route target blocked signals: <none>
- Route target blocked evidence: <none>
结论: 真实 provider 非交互 spawn_team + wait_team 验证通过。
```

即 team 分支的失败与路由无关：约束掉提问行为后，同一 provider、同一局部配置下直接跑到 `team.summary` 与 3/3 `done`。

### 6.5 整轮收官：单报告内 spawn_agent 与 spawn_team 双双通过（16:26:51）

最后用一条命令跑完整轮（两个探针都开，`-ConfigPath` 指向局部副本，`-SkipBuild -SkipProviderSmoke`），报告 `docs/working/multi-agent-real-terminal-validation-20260913-162651.md`：

| 分支 | 关键证据行 | 判定 |
| --- | --- | --- |
| spawn_agent | `Required found: spawn_agent, wait_agent, read_agent_events`；`Child agent count: 2`（`iso-a1`/`gate-b1`，`difficulty=normal`、`route=parent_inherit`）；`Child agent output: 2/2` | 验证通过 |
| spawn_team | `Required found: spawn_team, wait_team, team.summary`；`Route target blocked signals: <none>`；team `team-summary-3t`、3/3 tasks done | 验证通过 |

命令（可复用）：

```powershell
& 'E:\projects\ai\ai-agent-runtime\scripts\validate-multi-agent-real-terminal.ps1' `
  -Provider opencode.ai -Model deepseek-v4.1-flash -ReasoningEffort max `
  -ConfigPath 'E:\projects\ai\ai-agent-runtime\output\real-test\config.team-probe.yaml' `
  -SpawnAgentTimeoutSeconds 420 -SpawnTeamTimeoutSeconds 600 -SkipBuild -SkipProviderSmoke
```

至此真实 provider（`opencode.ai` / `deepseek-v4.1-flash` / `reasoning_effort=max`）下两条非交互正向分支已在同一次运行内判「验证通过」；剩余门禁只有 §5 的人工终端项（Ctrl+C、TUI 面板、mailbox/timeline 面板）。

## 7. 一句话总结

T1、T2、T3-b 在真实 provider（`opencode.ai` / `deepseek-v4.1-flash` / `reasoning_effort=max`）下通过；spawn_agent 正向分支（子 agent 继承父 provider、2/2 有产出）于 16:12:50 复跑判为验证通过，spawn_team 正向分支（3/3 tasks done、`team.summary` 就绪）于 16:19:15 复跑判为验证通过，两者在 16:26:51 的同一次整轮运行内双双通过（`docs/working/multi-agent-real-terminal-validation-20260913-162651.md`）。T3 在**用户全局配置**下的失败已定位为「easy 级路由指向本机不可达的 `vsllm.com`」，脚本把这类失败归类为环境阻断（不再混同于团队逻辑回归），并用 `-ConfigPath` 局部副本跑通正向分支而不改动用户全局配置。
