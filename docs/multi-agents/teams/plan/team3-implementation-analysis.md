# Team3 设计文档实现状态分析报告

> 基于 `docs/multi-agents/teams/design/team3.md`，按当前代码重新校对。

**分析日期**: 2026-03-15  
**设计文档**: `docs/multi-agents/teams/design/team3.md`

---

## 总体判断

Team3 聚焦的 7 个主题里，已经没有“从零开始”的模块；旧版报告的主要偏差，是把一批已经补齐的 Team 闭环仍写成“缺失”。

1. `Agent Teams` 已有可运行骨架，并且近期补齐了 `mailbox receipts`、`BlockTask`、`WithImmediateTx`、Team broker tools、mailbox dispatch、active team orchestrator loop。
2. `Rewind / Checkpoint` 已统一到 `checkpoint.Manager`：`conversation` / `both` 不再被 manager 直接拒绝；新 checkpoint 还能携带 conversation snapshot，actor 优先按精确 transcript 恢复，旧 checkpoint 再回退到 `HeadOffset` 方案。
3. `Session Actor` 的审批、提问、rewind、事件订阅已实现，但 `CurrentRunMeta` 仍未写入 SQLite runtime state。
4. `Hook Engine` 的 `notify` / `enrich` 已接进主流程：`hooks.Manager` 会聚合 message / extra context，`policy.Engine` 会把这些信息带到 permission decision，agent loop 也会把它们并进 tool metadata。
5. 当前最主要的剩余缺口，已经收敛到 `BackgroundTask` 的“运行中进程真正续跑”语义，以及 Team richer context / outcome contract 继续向剩余入口推广。

---

## 当前进度概览

| 模块 | 当前状态 | 完成度 | 关键依据 |
|------|----------|--------|----------|
| Agent Teams | ⚠️ 部分实现 | 86% | `internal/team/orchestrator.go:11`, `internal/team/sqlite_store.go:758`, `internal/team/sqlite_store.go:979`, `internal/runtime/toolbroker/broker.go:124` |
| Rewind / Checkpoint | ⚠️ 部分实现 | 90% | `internal/runtime/checkpoint/manager.go:227`, `internal/runtime/checkpoint/manager.go:345`, `internal/runtime/chat/actor.go:667` |
| Hook Engine | ⚠️ 部分实现 | 90% | `internal/runtime/hooks/manager.go:69`, `internal/runtime/policy/engine.go:93`, `internal/runtime/agent/loop.go:502` |
| Permission Engine (ask) | ✅ 已实现 | 90% | `internal/runtime/policy/engine.go:58`, `internal/runtime/policy/engine.go:158` |
| AskUserQuestion | ✅ 已实现 | 95% | `internal/runtime/toolbroker/broker.go:17`, `internal/runtime/chat/actor.go:993` |
| BackgroundTask / TaskOutput | ✅ 已实现 | 90% | `internal/runtime/background/manager.go:592`, `internal/runtime/background/manager.go:721`, `internal/runtime/background/manager_test.go:141` |
| Session Actor | ✅ 已实现 | 92% | `internal/runtime/chat/actor.go:123`, `internal/runtime/chat/session_runtime_store.go:206`, `internal/runtime/chat/hub.go:13` |

---

## 本次校正

旧版 `team3` 报告里列为“仍缺”的几项，当前代码已经补上：

- Team broker tools 已接入：`send_team_message`、`read_mailbox_digest`、`read_task_spec`、`read_task_context`、`report_task_outcome`、`block_current_task`：`internal/runtime/toolbroker/broker.go:19`, `internal/runtime/toolbroker/broker.go:20`, `internal/runtime/toolbroker/broker.go:21`, `internal/runtime/toolbroker/broker.go:22`, `internal/runtime/toolbroker/broker.go:23`, `internal/runtime/toolbroker/broker.go:24`
- Team tool 参数/结果模型已定义：`internal/runtime/toolbroker/types.go:52`, `internal/runtime/toolbroker/types.go:72`, `internal/runtime/toolbroker/types.go:90`, `internal/runtime/toolbroker/types.go:113`
- mailbox receipts 已拆成独立 receipt 持久化，而不再只是 `acked_at` 单字段：`internal/team/store.go:51`, `internal/team/sqlite_store.go:906`, `internal/team/sqlite_store.go:979`, `internal/team/sqlite_store.go:1025`, `internal/team/sqlite_store.go:1407`
- `BlockTask` 已进入 `Store` / SQLite / API：`internal/team/store.go:42`, `internal/team/sqlite_store.go:758`, `internal/api/skills/team_handlers.go:2165`
- `WithImmediateTx` 已存在于 `SQLiteStore`，并用于 receipt / path claim / event 写入：`internal/team/sqlite_store.go:56`, `internal/team/sqlite_store.go:995`, `internal/team/sqlite_store.go:1095`, `internal/team/sqlite_store.go:1188`
- mailbox 写入已支持投递到 session actor：`internal/api/skills/team_handlers.go:289`, `internal/api/skills/team_handlers.go:2780`, `internal/runtime/chat/actor.go:316`
- active team orchestrator loop 已由 handler 自动同步启动/停止：`internal/api/skills/team_handlers.go:173`, `internal/api/skills/team_handlers.go:177`, `internal/api/skills/team_handlers.go:226`, `internal/team/orchestrator.go:38`
- rewind 已统一到 `checkpoint.Manager`：`conversation` / `both` restore 会优先返回 exact conversation snapshot，actor 再把它应用到 session；旧 checkpoint 仍回退到 `HeadOffset`：`internal/runtime/checkpoint/manager.go:227`, `internal/runtime/checkpoint/manager.go:345`, `internal/runtime/checkpoint/types.go:59`, `internal/runtime/chat/actor.go:623`, `internal/runtime/chat/actor.go:667`, `internal/runtime/chat/actor.go:675`
- hook `notify` / `enrich` 已进入主流程：manager 聚合 message / extra context，policy decision 带出 hook metadata，agent loop 再并进 tool metadata：`internal/runtime/hooks/manager.go:69`, `internal/runtime/hooks/manager.go:79`, `internal/runtime/policy/engine.go:93`, `internal/runtime/policy/engine.go:108`, `internal/runtime/agent/loop.go:502`, `internal/runtime/agent/loop.go:579`
- background 已有真实优先级队列和恢复策略：`pending` 会恢复排队，`running` 默认失败收敛，也可通过 `restart_policy=rerun` 显式重排队：`internal/runtime/background/manager.go:592`, `internal/runtime/background/manager.go:721`, `internal/runtime/background/manager_test.go:60`, `internal/runtime/background/manager_test.go:141`
- richer team context 已进入执行主链：不仅 broker `read_task_context` 可读，teammate runner、lead planner replan、lead final summary 也会直接注入 `ContextBuilder` 输出：`internal/team/teammate_runner.go:71`, `internal/team/lead_planner.go:63`, `internal/team/lead_planner.go:120`, `internal/api/skills/team_handlers.go:160`

---

## 1. Agent Teams

### 已实现

- Team 核心模型、Store、SQLite、orchestrator、mailbox、lease、path claims、team context、lead planner、teammate runner 已存在：`internal/team/types.go:5`, `internal/team/store.go:15`, `internal/team/sqlite_store.go:15`, `internal/team/orchestrator.go:11`, `internal/team/mailbox.go:10`, `internal/team/lease.go:10`, `internal/team/path_claims.go:12`, `internal/team/context.go:19`, `internal/team/lead_planner.go:11`, `internal/team/teammate_runner.go:28`
- Team broker tools 已落地，并限制为 Team run 才可调用：`internal/runtime/toolbroker/broker.go:124`, `internal/runtime/toolbroker/broker.go:158`, `internal/runtime/toolbroker/broker.go:179`, `internal/runtime/toolbroker/broker.go:196`, `internal/runtime/toolbroker/broker.go:695`
- `read_task_context` 已存在，能够组合 task spec、team context 与 mailbox digest，不再只剩 prompt 注入：`internal/runtime/toolbroker/broker.go:543`
- `read_mailbox_digest` 默认支持按 agent 写 receipt / mark read，不再只是“只读摘要”：`internal/runtime/toolbroker/broker.go:427`, `internal/runtime/toolbroker/broker.go:467`, `internal/team/mailbox.go:88`, `internal/team/sqlite_store.go:979`
- teammate runner 在启动前读取 mailbox digest 时也会自动按 agent 写 receipt，和 team tool / HTTP 读取路径一致：`internal/team/teammate_runner.go:56`, `internal/team/mailbox.go:107`
- `report_task_outcome` 已形成通用 runtime 闭环，`block_current_task` 仅保留 blocked 兼容入口；HTTP `/complete`、`/fail`、`/block` 也已压薄到统一 outcome handler，并返回 compatibility warning/canonical header：done/failed/blocked/handoff 都会走共享 outcome apply 逻辑：`internal/api/skills/handler.go:62`, `internal/api/skills/handler.go:400`, `internal/runtime/toolbroker/broker.go:738`, `internal/runtime/toolbroker/broker.go:882`, `internal/team/task_outcome_apply.go:66`, `internal/team/task_outcome_apply.go:137`, `internal/api/skills/team_handlers.go:2165`, `internal/api/skills/team_handlers.go:2452`
- orchestrator 已在 runner 返回 blocked 时自动走 blocked 分支，并调用 lead planner 持久化 follow-up task：`internal/team/orchestrator.go:323`, `internal/team/orchestrator.go:407`, `internal/team/orchestrator.go:441`
- teammate outcome 已收紧为显式结构化 contract；缺失或非法 status block 会直接转成 `protocol error`，不再靠输出前缀 heuristics 决定 blocked：`internal/team/teammate_runner.go:203`, `internal/team/orchestrator.go:323`
- teammate outcome contract 已抽成共享 schema/validator，team 入口副作用也已收敛到共享 apply helper：`internal/team/task_outcome_contract.go:10`, `internal/team/task_outcome_apply.go:18`
- Team 共享上下文已经通过 `ContextBuilder` 注入 prompt，不是完全空白：`internal/team/context.go:19`, `internal/runtime/contextmgr/manager.go:470`

### 仍缺

- `ReadTaskContext`/`ContextBuilder` 已进入 broker、teammate runner、lead planner prompt 主链；剩余缺口主要是继续推广到其他非 prompt 入口和客户端文档，而不是 runtime 主执行链本身。
- shared contract 目前仍主要由 runner 消费，尚未推广为跨模块统一 artifact。
- `WithImmediateTx` 目前只是 `SQLiteStore` helper，不是 `Store` 接口层的通用事务抽象：`internal/team/sqlite_store.go:56`, `internal/team/store.go:15`

---

## 2. Rewind / Checkpoint

### 已实现

- mutating tool 前后自动 checkpoint capture 已实现；shell-like 工具即使没有显式 mutation hints，也会触发基于 `cwd/workdir` 的 checkpoint fallback：`internal/runtime/checkpoint/manager.go:34`, `internal/runtime/checkpoint/manager.go:95`, `internal/runtime/agent/loop.go:980`, `internal/runtime/agent/loop_test.go:691`
- `checkpoint.Manager.Restore()` 已统一支持 `code`、`conversation`、`both`；新 checkpoint 会携带 exact conversation snapshot，restore 时优先返回 transcript 本体：`internal/runtime/checkpoint/manager.go:227`, `internal/runtime/checkpoint/manager.go:345`, `internal/runtime/checkpoint/manager.go:555`, `internal/runtime/checkpoint/types.go:59`, `internal/runtime/checkpoint/manager_test.go:112`
- code restore / preview 已实现；conversation preview 也会区分 exact restore 与 legacy head rewind：`internal/runtime/checkpoint/manager.go:301`, `internal/runtime/checkpoint/manager.go:348`, `internal/runtime/checkpoint/manager_test.go:38`, `internal/runtime/checkpoint/manager_test.go:41`, `internal/runtime/checkpoint/manager_test.go:82`
- actor 已支持 `code`、`conversation`、`both` 三种 rewind 入口，并会优先把 exact conversation snapshot 直接替换到 session history，再发出 `rewind_completed` hook：`internal/runtime/chat/actor.go:225`, `internal/runtime/chat/actor.go:623`, `internal/runtime/chat/actor.go:667`, `internal/runtime/chat/actor.go:675`, `internal/runtime/chat/actor.go:727`, `internal/runtime/chat/actor_test.go:121`, `internal/runtime/chat/actor_test.go:138`

### 仍缺

- 新 checkpoint 已支持 exact conversation snapshot restore，但旧 checkpoint / 旧历史记录仍会回退到 `MessageCount + HeadOffset` 语义：`internal/runtime/checkpoint/manager.go:345`, `internal/runtime/chat/actor.go:691`
- 当前 checkpoint 仍主要聚焦代码与 transcript，可恢复的是“会话消息 + 工作区快照”；还不是完整 runtime state checkpoint。 

---

## 3. Hook Engine

### 已实现

- hook manager 与 shell/http executor 已独立成包：`internal/runtime/hooks/manager.go:10`, `internal/runtime/hooks/executor_shell.go:16`, `internal/runtime/hooks/executor_http.go:20`
- 生命周期事件已覆盖 Session、Prompt、Permission、Tool、Subagent、Checkpoint、Rewind：`internal/runtime/hooks/types.go:9`
- `pre_tool_use` 和 `permission_request` 已经实际接入主流程：`internal/runtime/agent/loop.go:427`, `internal/runtime/policy/engine.go:91`
- `hooks.Manager` 现在会聚合多个 hook 的 `notify` / `enrich` message 与 `extra_context`，而不是在 manager 层静默丢弃：`internal/runtime/hooks/manager.go:69`, `internal/runtime/hooks/manager.go:79`, `internal/runtime/hooks/manager_test.go:22`, `internal/runtime/hooks/manager_test.go:42`, `internal/runtime/hooks/manager_test.go:44`
- `policy.Engine` 会把 hook message / context 带到 permission decision 与 request metadata：`internal/runtime/policy/engine.go:93`, `internal/runtime/policy/engine.go:104`, `internal/runtime/policy/engine.go:108`, `internal/runtime/policy/engine.go:260`, `internal/runtime/policy/engine_test.go:25`
- agent loop 会把这些 hook metadata 并进 tool metadata，而不是在 tool 执行链路丢弃：`internal/runtime/agent/loop.go:502`, `internal/runtime/agent/loop.go:579`, `internal/runtime/agent/loop.go:695`, `internal/runtime/agent/loop.go:881`, `internal/runtime/agent/loop.go:1353`

### 仍缺

- `notify` / `enrich` 已接通主流程，但它们当前仍是“附加信息”语义，不直接改变 allow/deny/ask 主决策分支；真正改变控制流的动作仍主要是 `block` / `modify`：`internal/runtime/hooks/manager.go:55`, `internal/runtime/hooks/manager.go:58`, `internal/runtime/policy/engine.go:95`, `internal/runtime/agent/loop.go:454`

---

## 4. Permission Engine (ask)

### 已实现

- 统一评估入口已经接通：hook -> policy -> rules/mode -> callback -> ask：`internal/runtime/policy/engine.go:58`
- 支持 `allow` / `deny` / `ask`，以及 `default`、`accept_edits`、`plan`、`bypass_permissions` 等 mode：`internal/runtime/policy/engine.go:17`, `internal/runtime/policy/modes.go:8`
- 交互式 ask handler 已实现：`internal/runtime/policy/engine.go:158`

### 仍缺

- 本模块主链路已达成，当前高优先级缺口不在 ask 决策本身，而在 Team / rewind / runtime state 的后续消费闭环。

---

## 5. AskUserQuestion

### 已实现

- `ask_user_question` 已作为 broker tool 暴露：`internal/runtime/toolbroker/broker.go:17`
- actor 侧已经支持问题挂起与恢复：`internal/runtime/chat/actor.go:993`
- question request / answer 注入模型已打通，属于 runtime 内部挂起点，而不是普通外部工具：`internal/runtime/chat/actor.go:1016`, `internal/runtime/chat/actor.go:1025`, `internal/runtime/chat/actor.go:554`

### 仍缺

- 该能力本身已基本完成；剩余问题主要受 Session runtime state 持久化边界影响，而不是问答链路本身。

---

## 6. BackgroundTask / TaskOutput

### 已实现

- 后台任务提交、读取输出、列任务、取消任务、列事件已实现：`internal/runtime/background/manager.go:92`, `internal/runtime/background/manager.go:145`, `internal/runtime/background/manager.go:244`, `internal/runtime/background/manager.go:297`
- SQLite 持久化 job / event，输出写入日志文件：`internal/runtime/background/store.go:90`, `internal/runtime/background/store.go:384`, `internal/runtime/background/store.go:487`
- 已有真实有限并发优先级调度语义，而不是只记录 `priority`：`internal/runtime/background/manager.go:592`, `internal/runtime/background/manager_test.go:15`
- 重启恢复已接通：`pending` 会恢复排队，`running` 默认失败收敛，也可用 `restart_policy=rerun` 显式重排队：`internal/runtime/background/manager.go:721`, `internal/runtime/background/manager.go:801`, `internal/runtime/background/manager_test.go:60`, `internal/runtime/background/manager_test.go:141`

### 仍缺

- 还没有“运行中进程真正续跑”能力；当前恢复模型仍是 fail 或 rerun/requeue，而不是恢复原 OS 进程。 

---

## 7. Session Actor

### 已实现

- `SessionActor` 已是 goroutine + command channel 模型，支持 `SubmitPrompt`、`ApproveToolWithArgs`、`AnswerQuestion`、`Interrupt`、`RewindTo`、`DeliverMailboxMessage`、`SubscribeEvents`：`internal/runtime/chat/actor.go:123`, `internal/runtime/chat/actor.go:152`, `internal/runtime/chat/actor.go:182`, `internal/runtime/chat/actor.go:225`, `internal/runtime/chat/actor.go:316`, `internal/runtime/chat/actor.go:335`
- runtime state 已包含 `PendingApproval`、`PendingQuestion`、`CurrentRunMeta`、`HeadOffset`：`internal/runtime/chat/runtime_state.go:30`
- runtime state/event store 与 actor hub 已存在：`internal/runtime/chat/session_runtime_store.go:17`, `internal/runtime/chat/hub.go:13`
- `RunMeta` 已在 prompt 提交、恢复路径、Team tool 作用域解析里传播，并且已经落库到 runtime state：`internal/runtime/chat/actor.go:427`, `internal/runtime/chat/actor.go:467`, `internal/runtime/chat/session_runtime_store.go:206`, `internal/runtime/chat/session_runtime_store_test.go:17`

### 仍缺

- legacy checkpoint 仍可能只带 `HeadOffset` 级别的 conversation restore 目标；新的 exact snapshot 语义还没有回填到旧 checkpoint 数据。 

---

## 优先级建议

1. 先决定 `BackgroundTask` 对“运行中进程真正续跑”的模型；当前已经有 fail/rerun 策略，但还没有原进程恢复
2. 再继续把 richer team context / shared outcome contract 推广到剩余非 prompt 入口和客户端文档
3. 然后视需要补 legacy checkpoint 的 conversation snapshot 回填或迁移策略
4. 最后再推动旧 `/complete`、`/fail`、`/block` 调用迁移到统一 `/outcome`

---

*报告生成时间: 2026-03-15*
