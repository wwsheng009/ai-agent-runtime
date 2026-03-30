# Agent Runtime 功能实现情况分析

> 基于 `docs/multi-agents/teams/design/team1.md`，按当前代码实际状态重新核对。

**分析日期**: 2026-03-15  
**设计文档**: `docs/multi-agents/teams/design/team1.md`  
**分析范围**: `internal/`、`internal/api/skills/`

---

## 总体结论

旧版分析里最大的偏差有两类：

1. 把已经落地的模块写成了“未实现”，典型是 `Agent Teams`、`RunMeta`、`Hook Engine`、`Background Task`。
2. 把仍然缺关键闭环的能力写成了“基本完成”，典型是 `mailbox receipts`、`ReadTaskSpec/ReadMailboxDigest/SendTeamMessage`、`WithImmediateTx`、完整 `conversation/both rewind`。

当前代码更准确的判断是：**Runtime 主干已经成型，Teams 也已有可运行骨架；真正的缺口集中在一致性细节、Team 专用工具链，以及 rewind/恢复语义的最后一段。**

---

## 模块总览

| 模块 | 当前状态 | 完成度 | 依据 |
|------|----------|--------|------|
| Session Actor | ✅ 已实现 | 85% | `internal/runtime/chat/actor.go:123`, `internal/runtime/chat/runtime_state.go:28`, `internal/runtime/chat/hub.go:13` |
| Agent Loop | ✅ 已实现 | 90% | `internal/runtime/agent/loop.go:122`, `internal/runtime/agent/loop.go:552`, `internal/runtime/agent/loop.go:956` |
| Context Manager | ✅ 已实现 | 85% | `internal/runtime/contextmgr/manager.go:295`, `internal/runtime/contextmgr/manager.go:427`, `internal/runtime/contextmgr/manager.go:470` |
| Tool Broker | ✅ 已实现 | 90% | `internal/runtime/toolbroker/broker.go:15`, `internal/runtime/toolbroker/broker.go:112` |
| Permission Engine | ✅ 已实现 | 90% | `internal/runtime/policy/engine.go:58`, `internal/runtime/policy/engine.go:158`, `internal/runtime/policy/modes.go:24` |
| Hook Engine | ⚠️ 部分实现 | 80% | `internal/runtime/hooks/manager.go:32`, `internal/runtime/hooks/types.go:9`, `internal/runtime/hooks/executor_http.go:20` |
| Checkpoint / Rewind | ⚠️ 部分实现 | 70% | `internal/runtime/checkpoint/manager.go:34`, `internal/runtime/checkpoint/manager.go:249`, `internal/runtime/chat/actor.go:716` |
| Background Task | ✅ 已实现 | 80% | `internal/runtime/background/manager.go:92`, `internal/runtime/background/manager.go:145`, `internal/runtime/background/store.go:487` |
| Subagents | ✅ 已实现 | 85% | `internal/runtime/agent/scheduler.go:74`, `internal/runtime/agent/subagent_plan.go:9`, `internal/runtime/agent/subagent_plan.go:112` |
| Agent Teams | ⚠️ 部分实现 | 75% | `internal/team/orchestrator.go:11`, `internal/team/sqlite_store.go:670`, `internal/team/mailbox.go:21` |
| 本地存储 | ✅ 已实现 | 80% | `internal/runtime/chat/session_runtime_store.go:151`, `internal/team/sqlite_store.go:1141`, `internal/runtime/background/store.go:487` |
| MCP | ✅ 已实现 | 90% | `internal/mcp/manager/manager.go:35`, `internal/mcp/client/client.go:137`, `internal/mcp/catalog/catalog.go:15` |

---

## 详细分析

### 1. Session Actor

**已实现**

- `SessionActor` 已经是 goroutine + command channel 模型，支持 `SubmitPrompt`、`ApproveToolWithArgs`、`AnswerQuestion`、`Interrupt`、`RewindTo`、`DeliverMailboxMessage`、`SubscribeEvents`：`internal/runtime/chat/actor.go:123`, `internal/runtime/chat/actor.go:152`, `internal/runtime/chat/actor.go:182`, `internal/runtime/chat/actor.go:225`, `internal/runtime/chat/actor.go:316`, `internal/runtime/chat/actor.go:335`
- 运行时状态包含 `PendingApproval`、`PendingQuestion`、`CurrentRunMeta`、`HeadOffset`：`internal/runtime/chat/runtime_state.go:28`
- 有独立的 runtime state/event store，以及 session actor hub：`internal/runtime/chat/session_runtime_store.go:17`, `internal/runtime/chat/hub.go:13`

**未完成 / 偏差**

- `CurrentRunMeta` 只存在于内存态结构，SQLite runtime state 读写没有落库该字段：`internal/runtime/chat/runtime_state.go:36` 对比 `internal/runtime/chat/session_runtime_store.go:206`, `internal/runtime/chat/session_runtime_store.go:302`
- mailbox 通知能力只到 actor 事件层，Team mailbox 还没有形成 agent 可主动消费的 tool 闭环：`internal/runtime/chat/actor.go:316`, `internal/runtime/chat/actor.go:761`

### 2. Agent Loop

**已实现**

- ReAct loop 主循环已落地：`internal/runtime/agent/loop.go:122`
- 执行过程中接入 permission engine、hook、checkpoint auto-capture、subagent batch：`internal/runtime/agent/loop.go:422`, `internal/runtime/agent/loop.go:552`, `internal/runtime/agent/loop.go:665`, `internal/runtime/agent/loop.go:956`

**仍有边界**

- Team 级协作仍在 `internal/team` 中独立实现，不是由 loop 统一调度。

### 3. Context Manager

**已实现**

- 已有 hot/warm/cold 三层上下文治理、ledger compaction、artifact recall：`internal/runtime/contextmgr/manager.go:295`, `internal/runtime/contextmgr/manager.go:376`, `internal/runtime/contextmgr/manager.go:533`, `internal/runtime/contextmgr/manager.go:571`
- 已支持 workspace recall 和 team context 注入：`internal/runtime/contextmgr/manager.go:440`, `internal/runtime/contextmgr/manager.go:470`
- API 层会把 `team.ContextBuilder` 接入 context manager：`internal/api/skills/handler.go:2415`

**未完成 / 偏差**

- 没有独立向量库或专门的文档索引层，L4 主要依赖 artifact recall + workspace scan。
- L5 是“team digest 注入”，还不是可交互的 Team 专用工具集。

### 4. Tool Broker

**已实现**

- runtime broker 已提供 `ask_user_question`、`background_task`、`task_output` 三个合成工具：`internal/runtime/toolbroker/broker.go:15`, `internal/runtime/toolbroker/broker.go:112`

**未完成 / 偏差**

- 设计文档后续提出的 Team 专用工具 `SendTeamMessage`、`ReadMailboxDigest`、`ReadTaskSpec`、`ReadTaskContext` 当前代码中仍不存在。

### 5. Permission Engine

**已实现**

- 评估顺序已经是 hook -> policy -> rules/mode -> callback -> ask：`internal/runtime/policy/engine.go:58`
- 支持 `allow/deny/ask`、`default/accept_edits/plan/bypass_permissions`：`internal/runtime/policy/engine.go:17`, `internal/runtime/policy/modes.go:8`
- 支持 capability 解析与交互式审批：`internal/runtime/policy/capability.go:9`, `internal/runtime/policy/engine.go:158`

### 6. Hook Engine

**已实现**

- 已独立成包，支持 shell/http executor：`internal/runtime/hooks/manager.go:10`, `internal/runtime/hooks/executor_shell.go:16`, `internal/runtime/hooks/executor_http.go:20`
- 已定义事件：`session_start/end`、`user_prompt_submit`、`pre_tool_use`、`permission_request`、`post_tool_use`、`subagent_start/stop`、`checkpoint_created`、`rewind_completed`：`internal/runtime/hooks/types.go:9`

**未完成 / 偏差**

- `DecisionNotify` / `DecisionEnrich`、`ExtraContext` 目前只有类型定义，主流程没有真正消费：`internal/runtime/hooks/types.go:28`, `internal/runtime/hooks/types.go:37`, `internal/runtime/hooks/manager.go:32`
- Hook manager 现在真正生效的决策主要还是 `block` / `modify`。

### 7. Checkpoint / Rewind

**已实现**

- mutating tool 前后自动捕获 checkpoint：`internal/runtime/checkpoint/manager.go:34`, `internal/runtime/checkpoint/manager.go:83`
- code restore / preview 已实现：`internal/runtime/checkpoint/manager.go:249`
- actor 支持 `code`、`conversation`、`both` 三种入口，其中 conversation 通过 `HeadOffset` 回退：`internal/runtime/chat/actor.go:225`, `internal/runtime/chat/actor.go:664`, `internal/runtime/chat/actor.go:716`

**未完成 / 偏差**

- `checkpoint.Manager.Restore()` 仍明确拒绝 `conversation` / `both`：`internal/runtime/checkpoint/manager.go:260`
- `conversation rewind` 依赖 `MessageCount` + `head_offset`，不是完整的对话快照恢复：`internal/runtime/chat/actor.go:734`, `internal/runtime/chat/actor.go:754`
- shell 命令导致的文件修改仍不纳入 checkpoint 管理。

### 8. Background Task

**已实现**

- 后台任务提交、读取输出、列出任务、取消任务、列事件：`internal/runtime/background/manager.go:92`, `internal/runtime/background/manager.go:145`, `internal/runtime/background/manager.go:244`, `internal/runtime/background/manager.go:297`
- SQLite 持久化 job 与 event，输出落日志文件：`internal/runtime/background/store.go:90`, `internal/runtime/background/store.go:384`, `internal/runtime/background/store.go:487`

**未完成 / 偏差**

- `priority` 只是记录字段，没有真正的调度队列语义：`internal/runtime/background/manager.go:119`
- 进程重启后只能读取已持久化 job/log，不能恢复“正在运行”的任务。

### 9. Subagents

**已实现**

- `SubagentScheduler`、依赖图、单 writer 限制、patch 回执、hook/event 集成：`internal/runtime/agent/scheduler.go:74`, `internal/runtime/agent/scheduler.go:97`, `internal/runtime/agent/scheduler.go:172`
- planner 结果可自动映射为 subagent task 图，并带执行治理校验：`internal/runtime/agent/subagent_plan.go:9`, `internal/runtime/agent/subagent_plan.go:112`

**未完成 / 偏差**

- 子代理仍是 `role + 默认工具白名单`，不是设计文档里那种完整 `AgentProfile` 结构：`internal/runtime/agent/role_defaults.go:4`
- 顶层 API 有 profile 系统和 `explore/planner/executor` 自动选择：`internal/profile/resolver.go:10`, `internal/api/skills/handler.go:8635`；但这套机制还没有下沉成 subagent scheduler 的统一 profile 模型。

### 10. Agent Teams

**已实现**

- Team 核心模型、Store、SQLite 实现、orchestrator、mailbox、lease、path claims、team context、lead planner、teammate runner 已存在：`internal/team/types.go:5`, `internal/team/store.go:15`, `internal/team/sqlite_store.go:15`, `internal/team/orchestrator.go:11`, `internal/team/mailbox.go:10`, `internal/team/lease.go:10`, `internal/team/path_claims.go:12`, `internal/team/context.go:19`, `internal/team/lead_planner.go:11`, `internal/team/teammate_runner.go:28`
- API 层也已经接入 Team handler：`internal/api/skills/team_handlers.go:162`, `internal/api/skills/team_handlers.go:285`, `internal/api/skills/team_handlers.go:468`

**未完成 / 偏差**

- mailbox 仍是 `acked_at` 单表模型，不是 receipts 分表：`internal/team/types.go:107`, `internal/team/sqlite_store.go:791`, `internal/team/sqlite_store.go:817`, `internal/team/sqlite_store.go:898`, `internal/team/sqlite_store.go:1220`
- 缺少 agent 可直接调用的 Team 工具：`SendTeamMessage`、`ReadMailboxDigest`、`ReadTaskSpec`、`ReadTaskContext`
- Store 没有 `BlockTask`、`WithImmediateTx` 这类后续设计要求的一致性接口：`internal/team/store.go:15`
- `CompleteTask` / `FailTask` 已有，但 blocked 语义没有独立闭环：`internal/team/orchestrator.go:395`, `internal/team/orchestrator.go:410`

### 11. 本地存储

**已实现**

- chat runtime、team、background、artifact、MCP catalog 都有本地持久化方案。

**未完成 / 偏差**

- 迁移方式不统一：team/chat 走 `migrate.Apply`，background 仍是手写 `CREATE TABLE IF NOT EXISTS`：`internal/team/sqlite_store.go:1141`, `internal/runtime/chat/session_runtime_store.go:449`, `internal/runtime/background/store.go:485`

### 12. MCP

**已实现**

- MCP manager、client、catalog、health/lifecycle 基本齐全：`internal/mcp/manager/manager.go:35`, `internal/mcp/client/client.go:137`, `internal/mcp/catalog/catalog.go:15`

---

## 当前优先级建议

### P0

1. 给 Team 补齐一致性闭环：`mailbox receipts`、`WithImmediateTx`、`BlockTask`
2. 落地 Team 专用工具：`SendTeamMessage`、`ReadMailboxDigest`、`ReadTaskSpec`

### P1

1. 统一 rewind 语义，让 `checkpoint.Manager` 真正支持 `conversation` / `both`
2. 持久化 `CurrentRunMeta`，补齐 actor 重启后的 team/task 身份恢复

### P2

1. 把 subagent role-default 升级为统一 profile 模型
2. 统一 SQLite migration 与运行时恢复策略

---

*本报告生成于 2026-03-15，已按当前代码重新校正。*
