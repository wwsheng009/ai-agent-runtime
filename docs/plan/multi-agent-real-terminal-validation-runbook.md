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
```

注意: `aicli` 的 `.env` 与 `config.yaml` 查找依赖当前工作目录。本仓库当前可用配置位于 `backend\configs`，因此真实验证必须先 `cd E:\projects\ai\ai-agent-runtime\backend` 再执行 `.\aicli.exe ...`。如果从仓库根目录执行 `backend\aicli.exe ...`，可能无法加载同一套 provider 凭证并产生误导性的 `HTTP 401 Invalid API Key`。

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
