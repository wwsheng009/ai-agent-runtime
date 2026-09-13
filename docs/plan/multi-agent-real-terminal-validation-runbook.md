# Multi-Agent 真实终端验证 Runbook

更新时间: 2026-05-09

## 1. 目的

本 runbook 用于验证多 agent 在真实 `aicli chat`、真实终端和真实 provider 下的行为是否与单元测试一致，重点覆盖:

- child agent assistant delta 不直接污染 primary console。
- child agent reasoning 不直接污染 primary console。
- parent 只通过 AgentControl mailbox / collab event 看到结构化协作通知。
- `spawn_team auto_start=true` 不再出现 busy error storm。
- Ctrl+C 第一次取消 active child/team，第二次退出 chat loop。
- provider stream error 或慢响应时，后台 team 能稳定进入 terminal state。

## 2. 前置条件

1. 构建当前工作区的 `aicli`。
2. 配置一个可用 provider/model。
3. 使用支持 Ctrl+C 和 VT 序列的终端优先验证，例如 Windows Terminal。
4. 保留本次运行的 session/debug/chat log 路径。

建议先运行:

```powershell
cd E:\projects\ai\ai-agent-runtime\backend
go test ./internal/agentcontrol ./internal/chat ./internal/team ./internal/api/skills ./cmd/aicli/commands -count=1
```

可选辅助脚本:

```powershell
cd E:\projects\ai\ai-agent-runtime
.\scripts\validate-multi-agent-real-terminal.ps1 -Provider mimo_anthropic
```

该脚本会构建 `backend\aicli.exe`，以 `backend` 作为工作目录执行 provider smoke test，并继续执行两轮真实 provider 非交互 probe:

- `spawn_agent -> wait_agent -> read_agent_events`
- `spawn_team auto_start=true -> wait_team`

脚本会在 `docs/working` 下生成验证记录，自动记录最新 session/chat/debug/http/shell artifact 路径，并检查 session 中是否出现以下回归信号:

- `UNIQUE constraint failed: agent_control_agents.session_id`
- `spawn_team teammate id`
- `session is busy (running)`

若当前 provider 返回 `HTTP 401 Invalid API Key`，脚本会把阻断原因写入报告；需要先确认工作目录、config/env 与凭证均正确，再按后续人工验证步骤补充 Ctrl+C、TUI 和多 agent 并行观察结果。

可选参数:

```powershell
.\scripts\validate-multi-agent-real-terminal.ps1 -Provider mimo_anthropic -SkipBuild
.\scripts\validate-multi-agent-real-terminal.ps1 -Provider mimo_anthropic -SkipProviderSmoke
.\scripts\validate-multi-agent-real-terminal.ps1 -Provider mimo_anthropic -SkipSpawnAgentProbe
.\scripts\validate-multi-agent-real-terminal.ps1 -Provider mimo_anthropic -SkipSpawnTeamProbe
.\scripts\validate-multi-agent-real-terminal.ps1 -Provider opencode.ai -Model deepseek-v4.1-flash -ReasoningEffort max
# 2026-09-13 实测双通过（spawn_agent + spawn_team 两个探针都判通过，且不改用户全局配置）：
$cfg = 'E:\projects\ai\ai-agent-runtime\output\real-test\config.team-probe.yaml'
.\scripts\validate-multi-agent-real-terminal.ps1 -Provider opencode.ai -Model deepseek-v4.1-flash -ReasoningEffort max `
  -ConfigPath $cfg -SpawnAgentTimeoutSeconds 420 -SpawnTeamTimeoutSeconds 600 -SkipBuild -SkipProviderSmoke
```

注意: `aicli` 的 `.env` 与 `config.yaml` 查找依赖当前工作目录。本仓库当前可用配置位于 `backend\configs`，因此真实验证必须先 `cd E:\projects\ai\ai-agent-runtime\backend` 再执行 `.\aicli.exe ...`。如果从仓库根目录执行 `backend\aicli.exe ...`，可能无法加载同一套 provider 凭证并产生误导性的 `HTTP 401 Invalid API Key`。

`-Model` / `-ReasoningEffort` 会以 `--model` / `--reasoning-effort` 透传给三次非交互探针；留空时使用 provider 默认值。`-ConfigPath <config.yaml>` 会以 `--config` 透传（相对路径按调用时的当前目录展开为绝对路径），用于「不改用户全局配置」的局部副本路线，报告头部会额外输出 `Config:` 行。仅设置父会话的 provider/model 不会改变 teammate 的难度路由，见下节。

超时选择：`-SpawnAgentTimeoutSeconds` / `-SpawnTeamTimeoutSeconds` 会分别作为对应探针的 `--request-timeout`。真实 provider 下 `spawn_agent` 探针（2 个子 agent + `wait_agent` + `read_agent_events`）用默认 240s 实测会以 `Error: 操作错误: context deadline exceeded` 结束，建议 ≥600s；team 探针含 teammate 真实往返，建议 ≥420s。

### 2.1 子任务路由目标必须在验证前可达（2026-09-13 补充）

`spawn_team` 的 teammate 调用不继承父会话 provider，而是按难度路由解析：读 `aicli.teams.routing`，缺失时继承 `aicli.subagents.routing`（`backend/internal/agentconfig/config.go:629-642`）。若某个 `levels.*` 指向本机不可达的 provider，teammate 会在传输层快速失败，典型信号为 `provider transport stream failed ... timeout awaiting response headers (response-header guard after 20s)`，team 收敛为 `failed`（详见 `docs/working/multi-agent-real-provider-probe-20260913.md` §3）。

验证前请确认生效的难度路由：

```powershell
Select-String -Path "$env:USERPROFILE\.aicli\config.yaml" -Pattern 'teams:|subagents:|routing:|levels:|easy:|hard:|provider:|model:|reasoning_effort:' | Select-Object -First 40
```

处置方式（任选一种）：

- 把 `aicli.teams.routing.levels.*`（或 `aicli.subagents.routing.levels.*`）指向本机可达的 provider/model；
- 或把该 routing 的 `enabled` 置为 `false`，让子任务继承父会话 provider；
- 或按 2026-09-13 探针的做法：复制一份配置并用 `--config <copy>` 覆盖（复制后务必脱敏内联密钥），不改动用户全局配置——脚本可直接用 `-ConfigPath <copy>` 走这条路线，team 与 spawn_agent 两个探针会同时生效。

脚本行为：`scripts/validate-multi-agent-real-terminal.ps1` 会把上述传输层信号识别为「环境阻断」，在报告中输出 `Route target blocked signals` 与处置建议，而不是记为团队逻辑回归；若改用 `-ConfigPath` 指向把 `levels.easy` 改指可达 provider 的局部副本，则 team 分支应正常跑到 `team.summary` 与 `done`。

判定依据（按顺序回查，任一命中即视为环境阻断）：

1. 探针进程输出（脚本已把子进程输出按 UTF-8 解码，避免 GBK 控制台下中文变成乱码）；
2. 旧的 `session_*.json`（当前版本已改用 `session_history.sqlite` 承载会话正文，命中概率低）；
3. `~/.aicli/sessions/runtime/team_store.sqlite` 的 `agent_control_task_records`（只看探针开始后更新的记录；最权威，含 `route_provider` / `route_model` 与原始失败摘要，需要 `sqlite3` 在 PATH 上，缺失时自动跳过）；
4. 探针开始后更新的 `~/.aicli/chat-logs/**/*.debug.log`。

另注意：模型能力校验可能把显式路由降级为父路由。当 `validate_model_capabilities=true` 且目标 provider 未声明该模型能力时，会出现 `route_source=fallback` + `fallback_reason=model_unsupported_parent` 与 `model_unsupported` 警告（`backend/internal/modelrouting/resolver.go:366-380`），此时实际执行的是父路由模型。

### 2.2 spawn_agent 子 agent 同样按难度路由，「验证通过」必须以子 agent 有产出为准（2026-09-13 补充）

`spawn_agent` 的子 agent 也不继承父会话 provider：只有难度路由未命中时，`inherit_parent_when_missing: true` 才让它回退到父路由。用户配置 `~/.aicli/config.yaml:18-29` 当前只定义了 `subagents.routing.levels.easy` = `vsllm2_codex/gpt-5.4-mini`（本机不可达）：

| 子 agent 难度 | 路由结果 | 实测行为 |
| --- | --- | --- |
| `easy` | `levels.easy` → `vsllm2_codex/gpt-5.4-mini` | 传输层快速失败、子 agent 零产出（`message_count=1`） |
| `normal` 或未命中 | `inherit_parent_when_missing` → 父会话 provider | 正常产出（`output/real-test/sessions` 里的 `a1`：`route=parent_inherit`、`provider=opencode.ai/deepseek-v4.1-flash`） |

难度与来源记录在 `agent_control_agents.difficulty` / `difficulty_source`（`explicit` = 模型在 `spawn_agent` 调用里显式传了 `difficulty`）。跑本脚本时的探针 prompt 是「内联短句汇总」，很容易被判成 `easy`，从而正好命中唯一一条指向不可达 provider 的路由。

**假绿陷阱**：父进程输出里能看到 `spawn_agent` / `wait_agent` / `read_agent_events` 三个工具名，但子 agent 可能零产出（典型信号 `provider transport stream failed ... response-header guard after 20s`），而子 agent 的失败不会出现在父进程输出里。只扫父输出就会把这种运行记成「验证通过」。

脚本现行判定（`scripts/validate-multi-agent-real-terminal.ps1` §3 分支）：

1. `Required found`：`spawn_agent` / `wait_agent` / `read_agent_events` 三源合并取证——探针 stdout、父会话 transcript（`~/.aicli/sessions/session_history.sqlite` 的 `session_messages`，只看 `role=assistant` 的 `tool_calls`）、子 agent 行（`spawn_agent` 的 assistant 行在并行 spawn 时不落库，只能由子 agent 行反证）；
2. `Child agent count > 0`：本次探针时间窗（`created_at` 上下界 + root 行同期创建 + `team_id` 为空）内，在 `~/.aicli/sessions/runtime/agent_control.sqlite` 的 `agent_control_agents` 找到 spawn 子 agent；
3. `Child agent output`：`~/.aicli/sessions/session_history.sqlite` 的 `sessions.message_count >= 2` 的子 agent 数必须等于子 agent 总数（该阈值经实测校准：有产出的子 agent 为 2，零产出为 1）；
4. 全部满足才判「验证通过」；否则结论为「spawn_agent 工具链已连通，但子 agent 无产出，不能记为验证通过」，并在 `Child agent routes` 与父 provider 分叉（或命中传输层信号）时追加「归类: 环境阻断」与处置建议。

报告字段：`Child agent count` / `Child agent routes` / `Child agent row` / `Child agent output` / `Child agent difficulty (requested)` / `Required evidence source` / `Parent session` / `Parent tool calls` / `Route target blocked signals` / `Route target blocked evidence`。

不改用户全局配置的处置方式：脚本参数 `-SpawnChildDifficulty`（默认 `normal`）会让探针 prompt 要求模型在 `spawn_agent` 调用里显式传该难度，从而走 `inherit_parent_when_missing` 继承父 provider（`spawn_agent` 的 `difficulty` 参数定义见 `backend/internal/toolbroker/broker.go:321`）；也可用 `--config <copy>` 关闭 `subagents.routing.enabled`。

离线复算脚本（只读，不改数据）：`output/real-test/verify_spawn_child_evidence.ps1` —— 用 AST 从真实脚本抽取证据函数，对历史探针窗口复算子 agent 数与产出，可用来校验窗口隔离是否仍然正确。

**正向分支已复现（2026-09-13 16:12:50）**：按上述四步判定复跑，报告 `docs/working/multi-agent-real-terminal-validation-20260913-161250.md` 判为「真实 provider 非交互 spawn_agent 验证通过」——`Required evidence source` 明确列出三源（命令输出 + 父会话 transcript + 子 agent 行）、`Parent tool calls: list_agents, spawn_agent, wait_agent, read_agent_events`、`Child agent output: 2/2 个子 agent session 的 message_count >= 2`、子 agent 行 `difficulty=normal` / `route=parent_inherit`。即：真产出 + 证据齐全时判通过，零产出（15:52）判不通过，假绿与假红两个方向都有真实数据覆盖。

### 2.3 非交互探针必须显式禁止模型提问（2026-09-13 补充）

`--no-interactive` 下，若模型在收尾阶段调用提问/确认类工具，本轮会直接以 exit 1 结束：`非交互模式（--no-interactive）无法回答运行时提问`；此时报告里 `Route target blocked signals: <none>`，属于探针与模型行为问题，**不是**环境阻断。脚本已把两个探针的 prompt 都加上「不要向用户提问或请求确认，也不需要其它收尾动作，汇总后直接结束」；自建探针时同样要加这条约束，否则会把这类失败误读成 team 语义回归。

加约束后 team 探针已连续两次判通过：16:19:15（只跑 team，报告 `docs/working/multi-agent-real-terminal-validation-20260913-161915.md`）与 16:26:51（spawn_agent + team 整轮，报告 `docs/working/multi-agent-real-terminal-validation-20260913-162651.md`，两个分支都判「验证通过」）。

## 3. 验证 A: spawn_agent 并行输出隔离

启动:

```powershell
cd E:\projects\ai\ai-agent-runtime\backend
.\aicli.exe chat
```

输入:

```text
启动 2 个 spawn_agent，不要读取文件。agent A 基于内联短句“验证目标是确认多 agent 输出隔离、reasoning 隔离、等待语义和中断语义”总结一句话；agent B 基于内联短句“剩余门禁是真实 Windows Terminal Ctrl+C 与 TUI 面板人工验证”总结一句话。parent 调用 wait_agent 与 read_agent_events 后，用不超过 120 字中文汇总。执行时不要把 child 的原始 reasoning/assistant delta 直接展示到主控制台。
```

观察点:

- 主控制台只应看到 parent 的普通输出、工具调用摘要、结构化 collab/mailbox 通知。
- 不应持续刷出 child 的 assistant delta。
- 不应持续刷出 child 的 reasoning block。
- `/collab all 20` 应能看到 parent + child mailbox 聚合。
- `/agents panel 20` 应能看到 agent graph、parent mailbox 和 selected mailbox。

验证命令:

```text
/agents
/collab all 20
/agents panel 20
/agents panel follow timeout=10s 20
```

通过标准:

- `/collab` 和 `/agents panel` 可直接展示 AgentControl mailbox 事件。
- 没有 `subagent.completed` display mirror 时，completion mailbox 仍可展示。
- primary console 不出现 child 原始 stream 污染。

## 4. 验证 B: spawn_team auto_start 并行与 busy 收敛

输入:

```text
使用 spawn_team auto_start=true 创建 3 个 team 成员和 3 个 task，不要读取文件，也不要写文件。三个 task 分别基于这些内联短句各用一句中文总结: task-1 “AgentControl task graph 已成为 team task 的主写入路径”；task-2 “spawn_team 完成等待应使用 wait_team 而不是 wait_agent/read_agent_events”；task-3 “真实终端仍需验证 Ctrl+C 与 TUI 面板表现”。spawn_team 返回后，必须使用工具结果里的 team_id 调用 wait_team 等待 team.completed/team.summary，再由 lead 汇总每个成员的结论。
```

观察点:

- task assignment 应进入 durable AgentControl mailbox。
- `team_events` timeline 应能看到 dispatch requested/completed 和 task lifecycle。
- parent 模型应使用 `wait_team` 等待 team terminal summary，不应把 team teammate id 传给 `wait_agent` / `read_agent_events`。
- 不应出现大量 `session is busy (running)` 错误风暴。
- team terminal state 应最终为 done/failed/cancelled 中的稳定状态，不应挂死。

验证命令:

```text
/timeline active 50
/collab all 50
/agents panel 30
```

通过标准:

- `/timeline active` 能看到 assignment、completion、blocked/cancelled 等关键事件。
- `/collab all` 能看到 teammate mailbox/lifecycle 消息。
- 控制台输出仍以结构化摘要为主，不混入 teammate 原始 delta/reasoning。

## 5. 验证 C: Ctrl+C 中断收敛

输入一个会运行较久的 team prompt:

```text
spawn_team auto_start=true，创建 3 个成员分别执行耗时检查任务，每个成员先等待较长时间再报告。
```

操作:

1. 在 child/team 仍在运行时按一次 Ctrl+C。
2. 观察 chat loop 是否仍保持可用。
3. 运行 `/timeline active 50` 和 `/collab all 50`。
4. 再按第二次 Ctrl+C 退出。

通过标准:

- 第一次 Ctrl+C 触发 active team/child cleanup，不直接退出 chat loop。
- cancelled task 会写入 task lifecycle mailbox。
- provider context 被取消后，不应继续写 terminal team summary。
- 第二次 Ctrl+C 退出。

## 6. 验证 D: provider stream error 收敛

可使用不稳定 provider/model 或临时断网方式制造 stream error。

观察点:

- provider stream/internal error 不应导致后台 team 永久 running。
- task/team 应收敛为 failed 或 cancelled。
- `/timeline active` 能看到错误摘要。
- `/collab all` 能看到 lifecycle/error mailbox。

通过标准:

- 后台 orchestrator 不挂死。
- 错误进入结构化 event/mailbox，而不是散落在 primary console。

## 7. 需要保存的证据

每次真实验证至少保存:

- Session ID。
- Session File。
- Chat Log File。
- Debug Log File。
- HTTP Artifact Dir。
- Shell Artifact Dir。
- 执行的 prompt。
- `/agents panel`、`/collab all`、`/timeline active` 的关键输出摘要。
- 是否通过，以及失败时的具体异常。

建议将验证摘要写入:

```text
docs/working/multi-agent-real-terminal-validation-YYYYMMDD.md
```

## 8. 当前自动化覆盖

本 runbook 对应的确定性测试已经覆盖以下基础行为:

- `RegistryService` lifecycle、health、shared SQLite mode、idempotent close。
- 多实例打开同一个 registry DB 并发 spawn reservation。
- global-primary mailbox 并发 append。
- display mirror 缺失时 `/collab`、`/agents panel`、`read_agent_events mailbox_only` 仍读取 completion mailbox。
- runtime/team projection mode 可见。
- write-through global 写失败时 local projection 保留，后续 repair 可补齐 global row。
- `/agents panel follow` 可等待 mailbox 更新并刷新 panel 输出。

真实终端验证仍是必要补充，因为 Ctrl+C、provider stream、终端 surface 和真实模型流式输出属于端到端行为。
