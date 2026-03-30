# Team2 设计方案实施情况分析报告

> 基于 `docs/multi-agents/teams/design/team2.md`，按当前代码重新核对。

**分析日期**: 2026-03-15  
**设计文档**: `docs/multi-agents/teams/design/team2.md`

---

## 结论摘要

旧版报告把不少“设计目标”直接写成了“已实现”，与当前代码不符。更准确的判断是：

- `Session Actor`、`Permission Engine`、`AskUserQuestion/BackgroundTask/TaskOutput`、`Hook Engine` 已经落地主体。
- `Tool Broker` 的 Team 工具链已进一步补齐，`SendTeamMessage`、`ReadMailboxDigest`、`ReadTaskSpec`、`ReadTaskContext`、`BlockCurrentTask` 已接入运行时，且已限制为 Team run 可用；`ReadMailboxDigest` 也已支持消费后自动 ack。
- `Agent Teams` 不是未实现，目前已经补齐了 `receipts`、`BlockTask`、`WithImmediateTx`，并把 mailbox 投递、active team orchestrator loop、blocked 自动分支接上；同时 teammate 输出协议已收紧成共享结构化 contract + validator，不满足约定会直接落成 `protocol error`，HTTP mailbox 读取也已支持 auto-ack，但仍属于“核心骨架可运行，细节闭环未完成”。
- `Context Manager L4/L5`、`Subagents Profile`、`数据库迁移统一化` 仍是部分实现。

---

## 模块实施情况

| 模块 | 当前状态 | 完成度 | 说明 |
|------|----------|--------|------|
| Session Actor + Event Bus | ✅ 已实现 | 85% | actor、命令、事件、hub、runtime state/store 已存在 |
| Permission Engine | ✅ 已实现 | 90% | hook/rules/mode/callback/ask 流程已接通 |
| Tool Broker | ✅ 已实现 | 97% | `ask_user_question`、`background_task`、`task_output` 以及 Team 四个 broker tool 已实现，digest 读取后可自动 ack |
| Hook Engine | ⚠️ 部分实现 | 80% | shell/http + 生命周期事件已实现，但 notify/enrich 未接入 |
| Checkpoint + Rewind | ⚠️ 部分实现 | 70% | code rewind 有；conversation/both 仍不完整 |
| Background Task | ✅ 已实现 | 80% | job/log/event 持久化已做，优先级调度与运行中恢复未做 |
| Agent Teams | ⚠️ 部分实现 | 89% | store/orchestrator/mailbox/path claims/planner/runner 已有，mailbox/blocked/active loop 已补一批闭环 |
| Context Manager L4/L5 | ⚠️ 部分实现 | 75% | artifact recall + workspace/team prompt context 已做，但不是完整知识层/工具层 |
| Subagents Profile | ⚠️ 部分实现 | 70% | 顶层 profile 系统已做；subagent 仍用 role-default |
| 数据库迁移 | ⚠️ 部分实现 | 65% | team/chat 有 migration，team mailbox receipts 也已纳入 migration；background 仍是手写 schema |

---

## 本次更新

本轮补齐了分析文档里此前明确列出的 Team 高优先级缺口：

- Team broker tools 已接入：`send_team_message`、`read_mailbox_digest`、`read_task_spec`、`read_task_context`、`report_task_outcome`、`block_current_task`：`internal/runtime/toolbroker/broker.go:19`, `internal/runtime/toolbroker/broker.go:20`, `internal/runtime/toolbroker/broker.go:21`, `internal/runtime/toolbroker/broker.go:22`, `internal/runtime/toolbroker/broker.go:23`, `internal/runtime/toolbroker/broker.go:24`
- `report_task_outcome` / `block_current_task` 已接入 broker；前者支持 done/failed/blocked/handoff 通用上报，后者保留 blocked 兼容入口：`internal/runtime/toolbroker/broker.go:22`, `internal/runtime/toolbroker/broker.go:23`, `internal/runtime/toolbroker/broker.go:196`, `internal/runtime/toolbroker/broker.go:648`
- Team tool 参数/返回模型已补：`internal/runtime/toolbroker/types.go:46`, `internal/runtime/toolbroker/types.go:67`, `internal/runtime/toolbroker/types.go:83`
- Team tool 已纳入 capability 归类：`internal/runtime/policy/capability.go:42`
- 运行时装配已把 `TeamStore` 注入 broker：`internal/api/skills/handler.go:2928`
- mailbox receipts 已落地到 store/migration：`internal/team/store.go:51`, `internal/team/sqlite_store.go:985`, `internal/team/sqlite_store.go:1410`
- `BlockTask` 已落地到 store：`internal/team/store.go:42`, `internal/team/sqlite_store.go:758`
- `WithImmediateTx` 已落地到 `SQLiteStore`，并用于 event/path claim/receipt 写入：`internal/team/sqlite_store.go:56`, `internal/team/sqlite_store.go:1002`, `internal/team/sqlite_store.go:1098`, `internal/team/sqlite_store.go:1191`
- mailbox 写入已接入 session actor 投递：`internal/api/skills/team_handlers.go:277`, `internal/api/skills/team_handlers.go:2368`, `internal/runtime/toolbroker/broker.go:397`
- active team orchestrator loop 已自动同步启动/停止：`internal/api/skills/team_handlers.go:164`, `internal/api/skills/team_handlers.go:222`, `internal/team/orchestrator.go:38`
- `read_mailbox_digest` 已支持默认自动 ack：`internal/runtime/toolbroker/broker.go:376`
- runner/orchestrator 已支持结构化 blocked/handoff 分支，并把缺失或非法 outcome 统一收敛为 `protocol error`：`internal/team/teammate_runner.go:22`, `internal/team/teammate_runner.go:88`, `internal/team/orchestrator.go:299`, `internal/team/orchestrator.go:423`
- teammate prompt 已增加结构化 outcome 协议约定，支持 JSON block 与前缀行兜底，并明确声明 contract 规则：`internal/team/teammate_runner.go:109`, `internal/team/teammate_runner.go:203`
- teammate outcome contract 与 apply helper 已抽成共享 team 入口：`internal/team/task_outcome_contract.go:10`, `internal/team/task_outcome_apply.go:18`
- HTTP `ListMailbox` 已支持 `mark_read` + `agent_id` 自动 receipt：`internal/api/skills/team_handlers.go:2815`
- `TaskRunResult` 已记录 structured outcome 信息：`internal/team/teammate_runner.go:33`

---

## 逐项说明

### 1. Session Actor + Event Bus

**已实现**

- `SessionActor` 主体：`internal/runtime/chat/actor.go:123`
- 运行时状态：`internal/runtime/chat/runtime_state.go:28`
- 运行时状态/事件存储：`internal/runtime/chat/session_runtime_store.go:17`
- actor hub：`internal/runtime/chat/hub.go:13`
- 事件订阅：`internal/runtime/chat/actor.go:335`

**仍有缺口**

- `CurrentRunMeta` 没有持久化到 SQLite runtime state；重启恢复仍不完整：`internal/runtime/chat/runtime_state.go:36` 对比 `internal/runtime/chat/session_runtime_store.go:206`, `internal/runtime/chat/session_runtime_store.go:302`

### 2. Permission Engine

**已实现**

- 统一评估入口：`internal/runtime/policy/engine.go:58`
- mode：`internal/runtime/policy/modes.go:8`
- capability：`internal/runtime/policy/capability.go:9`
- 交互式 ask：`internal/runtime/policy/engine.go:158`

### 3. Tool Broker

**已实现**

- `ask_user_question`、`background_task`、`task_output` 已作为 broker tool 暴露：`internal/runtime/toolbroker/broker.go:17`, `internal/runtime/toolbroker/broker.go:41`
- Team 工具链已接入 broker：`send_team_message`、`read_mailbox_digest`、`read_task_spec`、`read_task_context`、`report_task_outcome`、`block_current_task`：`internal/runtime/toolbroker/broker.go:19`, `internal/runtime/toolbroker/broker.go:20`, `internal/runtime/toolbroker/broker.go:21`, `internal/runtime/toolbroker/broker.go:22`, `internal/runtime/toolbroker/broker.go:23`, `internal/runtime/toolbroker/broker.go:24`
- Team tool schema 已定义：`internal/runtime/toolbroker/types.go:46`, `internal/runtime/toolbroker/types.go:67`, `internal/runtime/toolbroker/types.go:83`
- Team tool 运行时装配已接通：`internal/api/skills/handler.go:2928`
- Team tool 已收紧到 Team run 场景：`internal/runtime/toolbroker/broker.go:695`
- `read_mailbox_digest` 读取后已可自动 ack，且保留 `mark_read` 开关：`internal/runtime/toolbroker/types.go:67`, `internal/runtime/toolbroker/broker.go:409`
- HTTP `ListMailbox` 读取后也已可按 agent 自动 ack：`internal/api/skills/team_handlers.go:2815`

**仍有缺口**

- 目前没有单独的 broker tool 用于 mailbox receipt ack，receipt 主要通过 API / service 层完成。
- Team tool 读取依赖 `RunMeta + TeamStore`，但还没有更细粒度的 team runtime capability/profile 分层。

### 4. Hook Engine

**已实现**

- Hook manager：`internal/runtime/hooks/manager.go:10`
- shell/http executor：`internal/runtime/hooks/executor_shell.go:16`, `internal/runtime/hooks/executor_http.go:20`
- 生命周期事件：`internal/runtime/hooks/types.go:9`

**仍有缺口**

- `DecisionNotify` / `DecisionEnrich` 只有类型，没有真正进入上下文或通知链路：`internal/runtime/hooks/types.go:28`, `internal/runtime/hooks/manager.go:32`

### 5. Checkpoint + Rewind

**已实现**

- 自动 checkpoint：`internal/runtime/checkpoint/manager.go:34`, `internal/runtime/checkpoint/manager.go:83`
- code restore / preview：`internal/runtime/checkpoint/manager.go:249`
- actor conversation rewind：`internal/runtime/chat/actor.go:716`

**仍有缺口**

- `checkpoint.Manager` 自身仍拒绝 `conversation` / `both`：`internal/runtime/checkpoint/manager.go:260`
- conversation rewind 仍是 `head_offset` 回退，不是完整会话快照恢复：`internal/runtime/chat/actor.go:749`

### 6. Background Task

**已实现**

- manager：`internal/runtime/background/manager.go:41`
- 提交/读取输出/列事件：`internal/runtime/background/manager.go:92`, `internal/runtime/background/manager.go:145`, `internal/runtime/background/manager.go:297`
- SQLite store：`internal/runtime/background/store.go:90`, `internal/runtime/background/store.go:384`

**仍有缺口**

- `Priority` 只是字段，没有真实队列调度：`internal/runtime/background/manager.go:119`
- 运行中的任务无法跨进程恢复。

### 7. Agent Teams

**已实现**

- `Store` / `SQLiteStore`：`internal/team/store.go:15`, `internal/team/sqlite_store.go:15`
- `Orchestrator`：`internal/team/orchestrator.go:11`
- `MailboxService`：`internal/team/mailbox.go:10`
- `LeaseManager` / `PathClaimManager`：`internal/team/lease.go:10`, `internal/team/path_claims.go:12`
- `LeadPlanner` / `TeammateRunner`：`internal/team/lead_planner.go:11`, `internal/team/teammate_runner.go:28`
- mailbox receipts：`internal/team/store.go:51`, `internal/team/sqlite_store.go:985`, `internal/team/sqlite_store.go:1029`
- `BlockTask`：`internal/team/store.go:42`, `internal/team/sqlite_store.go:758`
- `WithImmediateTx`：`internal/team/sqlite_store.go:56`
- mailbox digest 已支持广播消息和按 agent receipt 判未读：`internal/team/mailbox.go:53`, `internal/team/mailbox.go:108`, `internal/team/sqlite_store.go:886`
- mailbox 写入已支持 session actor 投递：`internal/api/skills/team_handlers.go:277`, `internal/runtime/chat/actor.go:316`
- active team orchestrator loop 已自动同步：`internal/api/skills/team_handlers.go:164`, `internal/api/skills/team_handlers.go:222`, `internal/team/orchestrator.go:38`
- outcome 运行时闭环已统一：`/outcome` 与 `report_task_outcome` 走共享 apply helper，旧 `/complete`、`/fail`、`/block` 与 `block_current_task` 只保留兼容入口；旧 HTTP 路由已返回 compatibility warning/canonical header：`internal/api/skills/handler.go:62`, `internal/api/skills/handler.go:400`, `internal/api/skills/team_handlers.go:2165`, `internal/api/skills/team_handlers.go:2452`, `internal/runtime/toolbroker/broker.go:738`, `internal/runtime/toolbroker/broker.go:882`, `internal/team/task_outcome_apply.go:66`, `internal/team/task_outcome_apply.go:137`
- runner 已支持结构化 outcome schema 校验，并把缺失/非法 contract 标成 `protocol error`：`internal/team/teammate_runner.go:88`, `internal/team/teammate_runner.go:203`
- shared `TaskOutcomeContractSchema/ParseTaskOutcomeContract/ValidateTaskOutcomeContract` 已抽到独立文件，`ApplyTerminalTaskOutcome/ApplyBlockedTaskOutcome` 也已统一 team 侧副作用：`internal/team/task_outcome_contract.go:25`, `internal/team/task_outcome_contract.go:56`, `internal/team/task_outcome_contract.go:75`, `internal/team/task_outcome_apply.go:66`, `internal/team/task_outcome_apply.go:137`

**仍有缺口**

- mailbox receipt 自动 ack 目前已覆盖 broker digest 和 HTTP mailbox list，但其他读取路径仍需逐步统一。
- 这套 shared contract 目前仍只在 team package 内部消费，尚未推广为跨模块统一 artifact。
- `WithImmediateTx` 目前是 `SQLiteStore` helper，不是抽象在 `Store` 上的通用事务模型。

### 8. Context Manager L4/L5

**已实现**

- recall + ledger：`internal/runtime/contextmgr/manager.go:376`, `internal/runtime/contextmgr/manager.go:533`
- workspace context：`internal/runtime/contextmgr/manager.go:440`
- team digest 注入：`internal/runtime/contextmgr/manager.go:470`

**仍有缺口**

- 还不是完整知识库/向量检索层。
- 团队共享层目前是 prompt digest，不是 Team 专用读写工具。

### 9. Subagents Profile

**已实现**

- 顶层 profile 系统：`internal/profile/resolver.go:10`
- API 自动 profile 路由：`internal/api/skills/handler.go:8635`

**仍有缺口**

- subagent scheduler 仍然依赖 `role -> DefaultToolsForRole`，没有统一 `AgentProfile` 模型：`internal/runtime/agent/role_defaults.go:4`

### 10. 数据库迁移

**当前情况**

- team/chat 走 migration，team mailbox receipts 也已纳入 migration：`internal/team/sqlite_store.go:1410`, `internal/runtime/chat/session_runtime_store.go:449`
- background 仍是手写 schema init：`internal/runtime/background/store.go:485`

---

## 优先级建议

1. 先补 mailbox receipt 消费闭环：把剩余读取路径也纳入统一 auto-ack 策略
2. 再评估是否在文档/API 层显式标记 `/complete`、`/fail`、`/block` 为兼容入口，推动调用方逐步迁移到统一 `/outcome`
3. 最后统一 rewind 语义与 runtime state 持久化

---

*报告生成时间: 2026-03-15*
