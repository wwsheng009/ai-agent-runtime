# Supervision 业务化监督差距分析（主 Agent 巡查 / 进度汇报 / 完成收敛）

- 日期：2026-09-16
- 范围：`backend/internal/supervision`、`backend/internal/agent`（batch 协调与轮询治理）、`backend/cmd/aicli/commands`（CLI 宿主接线）、`backend/internal/api/skills`（runtime-server 接线）、`backend/internal/agentcontrol`（回收/线程闸门）、`backend/internal/toolbroker`（模型工具契约）
- 关联设计：`docs/plan/spawn-subagents-async-supervisor-plan.md`、`docs/plan/multi-agent-agentcontrol-convergence-plan.md`（doc 6.2 快照 / 6.5 唤醒 / 6.6 控制动作）、`docs/plan/supervision-operator-runbook.md`、`docs/plan/runtime-observability-supervision-http-api-plan.md`

**结论（TL;DR）**：当前 supervision 是"**异常驱动的技术监督面**"（通知、看门狗、审批投影、超时/取消、控制动作），不是"**业务驱动的进度监督面**"。用户期望的"定时巡查 → 进度汇报 → 完成收敛"三步中：

1. **定时巡查**：没有调度器（全包唯一 ticker 是 run 健康扫描），且现有轮询手段被运行时主动抑制；更关键的是，最适合做巡查的 6.2 状态矩阵读模型**已实现但只接了 HTTP/debug，没有接给模型**。
2. **进度汇报**：中间进度只存在于 EventBus/UI（live-only，不落 durable、不进模型上下文）；子 agent **成功完成不唤醒父 agent**，因此"3/3 完成"不会主动汇报，除非用户再次发言或存在 critical 事件。
3. **完成收敛**：没有 completion → close 的自动策略；`close_agent` 靠模型自觉，`reclaim` 只按资源压力回收，两者语义不同。

这不是 bug，而是"异常驱动"的设计取舍与"业务进度驱动"的用户预期未对齐。能力大多已经存在，缺的是**通路与策略**（见 §6 方案 A/B/C）。

---

## 1. 需求与现状定位

### 1.1 用户描述的业务场景（目标语义）

主 agent/线程是业务负责人：

1. 创建 N（例：3）个子 agent 并行处理业务；
2. **定时**检查这 N 个子 agent 的状态与返回结果；
3. 向用户**主动汇报进度**（"3 个已完成 2 个"）；
4. 子 agent 完成后**关闭**对应子 agent（收敛生命周期）。

### 1.2 现有设计文档中的 supervision 定位

设计文档把 supervision 定义为**异常驱动的监督/控制面**：

- **6.2 统一快照读模型**（Scope/Mode children|descendants、rollup 计数、heartbeat/progress age、auto_action、allowed_actions）——`backend/internal/supervision/snapshot.go:10-118` 的注释逐条标注 "doc 6.2"。
- **6.5/6.6 唤醒规则与 durable 控制动作**（cancel/close/cancel_subtree/retry/reassign + CAS + audit）。
- 异步子代理设计明确选择"**父 agent 不为等待子 agent 而醒来**"、"普通完成不唤醒父"（`backend/internal/supervision/wake_consumer.go:14-19` 注释：*there is no resident polling goroutine*），只在 runnable 状态转换点（子完成投影、父 turn 结束）drain。

即：设计里"监督" = **出问题时叫醒你 + 给你可执行的修复动作**。"正常态的进度巡查与主动汇报"不在设计范围内。用户诉求本质是要求在 supervision 之上补一层"**业务进度监督**"。

### 1.3 三层语义对照

| 层 | 设计定位 | 当前实现 | 用户期望 |
| --- | --- | --- | --- |
| 技术监督 | run 健康/超时/取消/孤儿 | ✅ 完整（ExecutionSupervisor、reclaim、控制动作） | 背景能力 |
| 异常监督 | critical + action_required 唤醒 + 审批投影 | ✅ 完整（通知 + digest + wake + ack/control） | 背景能力 |
| **业务监督** | **未设计** | ❌ 只有 live-only 进度事件 + 手动 close | 巡查 / 汇报 / 收敛 |

---

## 2. 当前实现全貌（分层 + 证据）

### 2.1 事件产生层

| 事件 | 产生点 | 是否进入 supervision inbox |
| --- | --- | --- |
| 子会话完成 | `projectLocalAgentCompletion`（`backend/cmd/aicli/commands/chat_actor_registry.go:1131-1158`）→ `supervision.ProjectAgentCompletion`（`backend/internal/supervision/projection.go:111`） | 是（成功=info/closed；失败=critical/unresolved；取消/中断=warning） |
| 子会话审批请求 | `projectLocalAgentApproval`（`chat_actor_registry.go:1163-1191`） | 是（critical + action_required） |
| batch 终态（超时/孤儿/取消/完成） | `BatchTerminalLifecycle`（`backend/internal/agent/batch_lifecycle.go:14-46`）+ `localSubagentBatchLifecycleProjector`（`backend/cmd/aicli/commands/chat_actor_host.go:345-444`） | 是（超时→TimedOut、孤儿→Orphaned、取消→warning/closed） |
| **中间进度**（batch.progress / task.started / task.completed） | `backend/internal/agent/subagent_batch_coordinator.go:1157/1359/1372/1399/1408/1417` emit | **否**：只走 EventBus/UI；镜像 `supervision.SubagentProgressMirror`（`backend/internal/supervision/subagent_progress.go:13-21`）为 live-only、2s 节流、不落 durable store |
| run 健康（心跳/进度/deadline/超时/取消） | `ExecutionSupervisor`（`backend/internal/supervision/execution_supervisor.go`，5s ticker 见 `:332`） | 是，但这是 **run 级看门狗**，不是业务进度 |

### 2.2 持久化与投影层

- 通知落 `lifecycle_notifications`；**只有 `SeverityCritical && ActionRequired()` 才 `ScheduleWake`**（`projection.go:92-103`）。
- `BuildDigest`（`backend/internal/supervision/digest.go:85`）产出父侧摘要：`critical_unresolved` / `action_required` / `auto_actions_in_progress` / `resolved_since_last_turn` / `stale_subjects` / `Text`。

### 2.3 注入 / 唤醒层（被动、事件驱动）

- **父 turn 开始前注入 digest**：CLI `injectLocalSupervisionPreflight`（`chat_actor_host.go:2310-2363`，只 MarkDelivered+MarkSeen，绝不 ack）；runtime-server 对应 `backend/internal/api/skills/supervision_handlers.go:303/337`。
- **唤醒**：`bindSupervisionWakeConsumer`（`chat_actor_host.go:611-636`）监听父 turn 结束事件 `EventSessionEnd` → `wakeSupervisedParent`（`:641-645`）→ `MaybeWakeParent` / `DrainRunnable` / `Deliver` / `ResolveWake`（`wake_consumer.go:36-85`）；自动唤醒 turn 使用 `AutoWakePrompt`（`wake_consumer.go:12`："存在待处理的子 Agent / Team 关键生命周期事件…"），经 `submitParentWakeTurn`（`chat_actor_host.go:598-606`）以 `bypass_permissions` 提交。
- **限流退化**：`ErrWakeRateLimited` → `selfCheckSupervisedParent`（`chat_actor_host.go:628-634`）。
- **交付是异步 goroutine**（`chat_actor_host.go:512-542`），立即返回 nil，不等待父 turn admission —— 与设计 6.5 rule 6"必须等 admission 才算成功"存在偏差（次要风险，见 §5 R6）。

### 2.4 读模型层：6.2 Snapshot 已实现，但没有接给模型

- `BuildSnapshot`（`backend/internal/supervision/snapshot.go:120-123`）能产出 **descendants 状态矩阵**：
  - `SnapshotSummary`（`snapshot.go:51-61`）：running / blocked / stalled / timed_out / orphaned / invalid / canceling / terminal_unacknowledged / action_required；
  - `SnapshotItem`（`snapshot.go:64-96`）：heartbeat_age_ms、progress_age_ms、execution_deadline_at、run_id/attempt、auto_action、recommended_action、allowed_actions、notification_id、last_change_seq。
- descendant 聚合已装配：`supervisionDescendantProvider`（`backend/internal/runtimeserver/supervision.go:264-292`，合并 agentcontrol 记录 + team edges）。
- **暴露面**：HTTP API（`api/skills/supervision_handlers.go:393`）与 `/debug`、observe（`chat_observe_http.go:82`、`chat_debug_turn_metrics.go:52`）。
- **模型工具 `supervision_snapshot` 返回的不是它**：CLI 宿主控制器 `SupervisionSnapshot`（`backend/cmd/aicli/commands/chat_supervision_tools.go:63-82`）调用 `LocalControlService.Snapshot` → `BuildDigest`（`backend/internal/supervision/local_control.go:81-99`），返回 `*supervision.Digest`（**通知摘要**），不是 `*supervision.Snapshot`（**状态矩阵**）。runtime-server 侧 `internal/toolbroker/supervision_tools.go` 的控制器接口同为 digest 语义。

> 这是本分析最可操作的一条发现：**巡查所需的能力已经存在，只是没有对模型开放**。

### 2.5 控制层

- 模型工具 `control_descendant`（cancel / close / cancel_subtree / retry / reassign，需 notification_id + CAS + reason）与 `ack_lifecycle`，本地实现 `LocalControlService`（`local_control.go`）；scope 解析见 `chat_supervision_tools.go:42-61`（模型不可指定 root scope）。
- `close_agent` 是独立模型工具（`backend/internal/toolbroker/broker.go:47/450/626/2044`），是否关闭由模型自主决定。
- 资源回收 `agentcontrol.ReclaimPolicy`（`backend/internal/agentcontrol/reclaim.go:15-19/76-81/318`）：session_missing / terminal / stale / idle_timeout，由线程闸门诊断驱动；`agents.reclaimIdleMs`（`config/manager.go:119/1112`）为 opt-in。
- 提示词只有软引导（`backend/internal/prompt/environment_context.go:247-251`：spawn 后先做独立工作、不要立即 wait_agent；线程满时 close idle child）。

### 2.6 轮询治理（与业务巡查直接冲突的既有机制）

- polling/control 工具被排除在 doom-loop 重复检测之外（`backend/internal/agent/polling_guard.go:14-15`），但 P1-7 增加了**非阻断软刹车**：
  - 连续 3 次相同轮询（`PollingBackoffNoticeThreshold`，`:22-26`）或累计阻塞等待 > 5m（`PollingWaitBudgetNoticeThreshold`，`:28-35`）时注入 advisory；
  - 文案立场明确："*waiting longer is not progress … Stop re-issuing wait_agent with a larger timeout_ms; use this turn for independent work or a status update*"（`:305-331`）。
- 工具契约层同样抑制重复等待（`wait_agent` / `read_agent_events` 描述："do not immediately re-call with the same id/after_seq"、"Raising the wait timeout between repeated waits is not progress"）。

---

## 3. 业务诉求逐项差异

### S1 创建 3 个子 agent —— ✅ 已满足

`spawn_subagents`（background 模式）返回 batch handle + 幂等键（`backend/internal/agent/loop.go:6446-6526`），batch 由协调器持久化，生命周期更新由 supervision 投递。

### S2 定时检查 3 个子 agent 的状态 —— ❌ 缺口最大

- **没有常驻调度**：supervision 包内唯一 ticker 是 run 健康扫描（`execution_supervisor.go:332`，5s），只管 deadline/超时/取消；`wake_consumer.go:14-19` 明确声明"没有常驻轮询 goroutine"。
- **模型侧唯一"检查"手段不匹配**：`list_agents` / `wait_agent` / `read_agent_events` 是逐个/逐窗口的等待原语，不能一次拿到 N 个子 agent 的状态矩阵；而且它们被 polling guard 与工具契约明确抑制（§2.6）。
- **最合适的原语被闲置**：6.2 状态矩阵读模型（§2.4）已实现、provider 已装配，但**未接给模型**。
- 结论：**"定时"没有载体；"检查"缺原语。** 父 agent 只能"子完成时被动收到通知"，做不到"按节拍主动看板"。

### S3 向用户汇报进度 —— ⚠️ 只覆盖异常，不覆盖正常进度

- 中间进度（N/M 完成、最近进度时间）只进 EventBus/UI（live-only），不落 durable store、不进 digest、不进父 LLM 上下文。
- **成功完成是 info/closed 且不唤醒**（`projection.go:92-103` 的唤醒门控 + `:111` 的 `ProjectAgentCompletion`）：3/3 全部成功时，父 agent 不会被叫醒汇报"全部完成"；用户只有在下一轮自己说话、或 UI 上看到 batch 事件时才知道。
- 因此"主动汇报"在架构上被**有意排除**（token 成本取舍），与用户预期冲突——这是取舍未对齐，不是缺陷。

### S4 完成后关闭子 agent —— ❌ 无自动收敛

- 没有"batch 终态 → close 子会话"的钩子；`close_agent` 依赖模型自觉，提示词仅有软引导。
- `control_descendant close` 需要 notification_id 且要过 `allowed_actions` 复核；对"成功完成"这类 info/closed 行，模型还得先查快照找到行、再执行动作——链路存在但繁琐。快照里其实已有 `recommended_action` / `allowed_actions` / `notification_id` 字段，但模型拿不到 Snapshot（§2.4），所以这条"推荐动作"通路也是断的。
- `reclaim` 只表达资源压力（idle/terminal/stale/session_missing），不表达"业务已完成、请收敛并回执"；两者语义不同，不可互相替代。

### S5 检查 / 汇总返回结果 —— ⚠️ 部分满足

- 已有：batch 终态通知（含超时/孤儿/取消）、`wait` 模式 full reports、mailbox / `read_agent_events`、`apply_agent_worktree`（worktree 隔离产物落地）。
- 缺：父侧"**任务 → 状态 → 结果摘要**"的统一业务视图——Snapshot 有状态无结果摘要，digest 有通知无进度，"给用户汇报进度"所需的聚合数据没有单一来源。

---

## 4. 差异矩阵（总表）

| # | 业务动作 | 用户期望 | 设计定位 | 实际实现 | 差距 | 关键证据 |
| --- | --- | --- | --- | --- | --- | --- |
| S1 | 创建子 agent | 一次创建 N 个 | 支持 | 已实现（batch + 幂等 + background） | 无 | `agent/loop.go:6446-6526` |
| S2 | 定时检查状态 | 父 agent 定时看 N 个子状态 | **设计未覆盖**（无常驻轮询） | 事件驱动被动通知；轮询被抑制；状态矩阵读模型未接模型 | **高** | `wake_consumer.go:14-19`、`execution_supervisor.go:332`、`polling_guard.go:14-35/305-331`、`chat_supervision_tools.go:63-82`、`snapshot.go:39-118`、`supervision_handlers.go:393` |
| S3 | 进度汇报 | 主动向用户报 N/M | 设计未覆盖（避免 token 成本） | 进度 live-only（UI）；成功完成不唤醒；digest 只有通知 | **高** | `subagent_progress.go:13-21`、`subagent_batch_coordinator.go:1157/1359/1372/1399/1408/1417`、`projection.go:92-103/111` |
| S4 | 完成后关闭 | 自动收敛 + 回执 | 6.6 提供手动控制动作 | `close_agent` 手动；reclaim 仅资源型；无 completion→close 策略 | **中高** | `toolbroker/broker.go:47/450/626/2044`、`agentcontrol/reclaim.go:15-19/76-81/318`、`config/manager.go:119/1112` |
| S5 | 汇总结果 | 单一视图看状态+结果 | 6.2 快照（状态） | 快照仅 HTTP/debug；模型工具拿 digest | **中** | `snapshot.go:120-123`、`runtimeserver/supervision.go:264-292`、`local_control.go:81-99` |
| — | 唤醒交付语义 | 汇报不能丢 | 6.5 rule 6：等 admission | `Deliver` 异步立即返回 | 低（次要风险） | `chat_actor_host.go:512-542` |

---

## 5. 根因分析

| # | 根因 | 说明 | 证据 |
| --- | --- | --- | --- |
| R1 | **定位差异** | supervision 被定义为异常驱动（critical + action_required 才唤醒），"业务正常态进度"被系统性排除在通知模型之外 | `projection.go:92-103` |
| R2 | **通知模型缺"进度"类别** | Notification 表达生命周期事件与处置状态（unresolved→decided），没有"进度里程碑"概念；进度事件只存在于 EventBus/UI 通道（live-only） | `subagent_progress.go:13-21`、`subagent_batch_coordinator.go:1157+` |
| R3 | **读模型与工具面错配** | 6.2 Snapshot（最适合"巡查"的原语）已实现且装配了 provider，却只接 HTTP/debug；模型工具返回的是通知摘要 digest。**能力在，通路不在** | `snapshot.go:120-123`、`runtimeserver/supervision.go:264-292`、`chat_supervision_tools.go:63-82` |
| R4 | **反轮询治理与业务巡查冲突** | P1-7 软刹车与工具契约都明确"重复等待不是进展"；在缺少批量快照工具的前提下，这等于堵住了模型唯一的"定期看"手段 | `polling_guard.go:14-35/305-331` |
| R5 | **生命周期收敛缺策略层** | 完成/终态 → 关闭没有 runtime 策略，只有模型自觉与资源型 reclaim；"业务收敛"与"资源回收"语义不同 | `broker.go:47/450/626/2044`、`reclaim.go:15-19/76-81/318` |
| R6 | **唤醒交付是 fire-and-forget**（次要） | `Deliver` 异步提交、不校验 admission，与设计 6.5 rule 6 不一致，极端情况下"该汇报"的 turn 可能未真正被调度 | `chat_actor_host.go:512-542` |

---

## 6. 改造建议（按 ROI 排序）

### 方案 A（P0）：把 6.2 快照接成模型工具（建议命名 `supervision_descendants`）

- **目标**：父 agent 一次调用拿到 N 个子 agent 的状态矩阵（running/blocked/stalled/timed_out/terminal + heartbeat_age/progress_age + allowed_actions/recommended_action）。
- **改动点**：
  1. `backend/internal/toolbroker/supervision_tools.go`：新增工具 schema（`mode=children|descendants`、`health=any|abnormal|action_required`、`include_terminal`、`limit`）。
  2. CLI 宿主：`chat_supervision_tools.go` 新增控制器方法，调用 `supervision.BuildSnapshot` + `DescendantProvider`（复用 `runtimeserver/supervision.go:264-292` 的聚合逻辑，CLI 侧补一个 agentcontrol/team 聚合实现）。
  3. runtime-server：`api/skills/supervision_handlers.go:393` 已有 BuildSnapshot，补 toolbroker 侧接线。
- **收益**：直接补齐 S2 的"检查原语"，并降低对 wait_agent 轮询的依赖（缓解 R4）。
- **风险**：跨 scope 数据泄露 → 复用现有 scope 解析（`chat_supervision_tools.go:42-61`，模型不可指定 root scope）。
- **验证**：单测 scope/过滤/limit；场景：3 个 running 子 agent 一次调用返回 3 行 + summary。

### 方案 B（P0/P1）：进度汇总通道（progress rollup → digest / 上下文）

- **目标**：让父 agent 在 turn 内看到"batch X: 2/3 完成，running: worker-3（最近进度 12s 前）"，而不是只有异常。
- **改动点（二选一或组合）**：
  1. **轻量**：digest 增加 progress 区块——把 live-only 的 `SubagentProgressMirror` 聚合成 `Digest.ProgressSummary`（读 agentcontrol/batch 状态，不落新表）。
  2. **durable**：progress ledger（batch 里程碑：started / first-progress / task-completed 计数），供 digest 与快照共用。
- **收益**：补齐 S3 的"数据来源"，且不改变"成功不唤醒"的默认策略（进度只在父 turn 内可见）。
- **风险**：token 成本；需限制行数（复用 `DigestMaxItems`）。

### 方案 C（P1）：完成后自动收敛（close + 回执）

- **目标**：batch 终态（或子会话 completion 投影）后按策略自动 close 已完成子会话，并产出回执通知（可 ack、可审计）。
- **改动点**：
  - 新配置 `agents.autoCloseCompleted`（默认 `off` 保持现状；可选 `completed` / `batch_terminal` / `off`）。
  - 在 `localSubagentBatchLifecycleProjector`（`chat_actor_host.go:345-444`）或 batch 终态投影后挂钩：对终态子会话调用控制面 close（复用 `LocalControlService.Control`，写 audit + 回执通知）。
  - **轻量版**（不改默认行为）：batch 终态通知里带 `recommended_action=close` + notification_id（快照已有这两个字段），让模型一次 `control_descendant` 完成收敛。
- **风险**：关闭时机 vs 用户"先看结果再关"的期望；默认关闭策略 + 显式开关。

### 方案 D（P2，opt-in）：周期巡查调度

- **目标**：真正实现"定时检查"——当存在 running background batch 且父会话空闲时，按 `supervision.progressCheckInterval`（默认 0=关闭）注入轻量 progress digest，必要时触发一次汇报 turn。
- **复用**：`WakeConsumer` 的 runnable gate + 限流 + 去抖；**不得**引入"每 5s 扫描所有会话"的常驻开销。
- **权衡**：与"无常驻轮询"的设计原则冲突，因此必须 opt-in + 有 token 预算说明（建议写入 `docs/plan/supervision-operator-runbook.md`）。

### 方案 E（P2）：契约与提示词对齐

- 更新工具描述与 `internal/prompt/environment_context.go:247-251` 的引导：巡查用 `supervision_descendants` 而非重复 `wait_agent`；batch 完成后关闭子 agent 并汇报。
- 把"业务监督"流程（spawn → 巡查 → 汇报 → 收敛）写进 `docs/plan/supervision-operator-runbook.md`。

### 方案 F（P2，可选）：唤醒交付语义对齐 6.5 rule 6

- `Deliver` 改为等待 admission 结果（或至少记录投递失败并重试），消除 fire-and-forget 的静默丢失面（`chat_actor_host.go:512-542`）。

---

## 7. 分期路线与验收标准

| 阶段 | 内容 | 验收 |
| --- | --- | --- |
| **P0** | 方案 A（快照工具）+ 方案 B 轻量版（digest progress 区块） | 场景演练：spawn 3 个 background 子 agent（1 快完成 / 1 慢 / 1 超时），父 agent 单次工具调用可见 3 行状态；父 turn 内可见 2/3 进度 |
| **P1** | 方案 C（自动收敛/推荐动作）+ 方案 E（契约对齐） | batch 终态后子会话被关闭（或通知带 close 推荐并被 ack 收敛）；关闭动作有 audit 记录 |
| **P2** | 方案 D（周期巡查，opt-in）+ 方案 F（admission 语义） | 开启开关后父 agent 按间隔收到 progress 摘要且不触发 polling backoff；关闭时行为与现状完全一致 |

**回归关注**：

- `wait` 模式语义不变；`spawn_subagents` 幂等键行为不变。
- polling guard 行为不变（`polling_guard_test.go` 既有契约：polling/control 工具保持 doom-loop 豁免 + 非阻断 advisory）。
- token 预算：digest 行数上限（`DigestMaxItems`）与 progress 区块行数。
- 通知收敛：ack 后不再重复注入（N9 语义）。

**待复核项**（本次未展开验证，建议实施前确认）：

1. `control_descendant close` 对 info/closed 行的 `allowed_actions` 计算是否包含 close（决定"轻量版自动收敛"是否可行）。
2. runtime-server（非 CLI）宿主是否装配了 `DescendantProvider`（决定方案 A 在两条宿主上的工作量差异）。
3. `Deliver` 异步路径在 UI 关闭 / 无订阅者时的投递结果（决定方案 F 优先级）。

---

## 8. 附录：关键代码索引

| 主题 | 位置 |
| --- | --- |
| 无常驻轮询声明 | `backend/internal/supervision/wake_consumer.go:14-19` |
| 唤醒编排（MaybeWakeParent/DrainRunnable/Deliver/ResolveWake） | `wake_consumer.go:36-85`；`cmd/aicli/commands/chat_actor_host.go:611-645` |
| 自动唤醒 prompt / 提交 | `wake_consumer.go:12`；`chat_actor_host.go:598-606` |
| 唤醒交付（异步，不校验 admission） | `chat_actor_host.go:512-542` |
| preflight digest 注入 | `chat_actor_host.go:2310-2363`；`internal/api/skills/supervision_handlers.go:303/337` |
| 通知投影与唤醒门控 | `internal/supervision/projection.go:35/92-103/111` |
| digest 读模型 | `internal/supervision/digest.go:85` |
| 6.2 快照读模型 | `internal/supervision/snapshot.go:10-123` |
| descendant provider（agentcontrol + team） | `internal/runtimeserver/supervision.go:264-292` |
| 快照 HTTP 暴露 | `internal/api/skills/supervision_handlers.go:393` |
| 模型工具（digest 语义 + scope 解析） | `cmd/aicli/commands/chat_supervision_tools.go:42-82`；`internal/supervision/local_control.go:55-99` |
| batch 终态投影 | `internal/agent/batch_lifecycle.go:14-46`；`chat_actor_host.go:345-444` |
| 子完成 / 审批投影 | `cmd/aicli/commands/chat_actor_registry.go:1131-1191` |
| 进度事件（live-only） | `internal/agent/subagent_batch_coordinator.go:1157/1359/1372/1399/1408/1417`；`internal/supervision/subagent_progress.go:13-21` |
| 轮询软刹车（P1-7） | `internal/agent/polling_guard.go:14-35/305-331`；`polling_guard_test.go:202-262` |
| run 健康看门狗（5s ticker） | `internal/supervision/execution_supervisor.go:332` |
| `close_agent` 工具 | `internal/toolbroker/broker.go:47/450/626/2044` |
| 资源回收（reclaim） | `internal/agentcontrol/reclaim.go:15-19/76-81/318`；`internal/config/manager.go:119/1112` |
| 提示词软引导 | `internal/prompt/environment_context.go:247-251` |
| 控制面装配 | `internal/runtimeserver/supervision.go:75-166` |

---

*本文档基于 2026-09-16 的工作区代码静态分析（行号以当日 HEAD 为准）；未运行端到端场景演练，§7"待复核项"列出实施前需确认的三点。*

---

## 9. 补记（2026-09-16）：API 宿主 batch 终态投递 + 重启恢复对等

本轮复核发现一个**两条宿主不对等**的实现缺口（方案 A–F 之外，属"能力在包内、宿主没装"）：

| # | 缺口 | CLI 宿主 | API 宿主（修复前） | 影响 |
| --- | --- | --- | --- | --- |
| N1 | batch 终态 mailbox 投递（`BatchTerminalSink`） | `chat_actor_host.go` 在 agent 构造时装 sink | agent 只回填 emitter/projector，**从不装 sink** | 终态只进 supervision store + wake，父会话拿不到那条 durable「batch 结束」汇报消息 |
| N2 | 启动恢复（遗留行收敛 + 终态重放） | `runLocalSubagentStartupRecovery`（立即 + 宽限期后两趟） | **无入口**，且 durable store 从未接线（默认值 `resolveBatchDSN` 是 per-process 内存 DSN） | 进程重启后遗留的 `queued/running` batch 永远停在非终态；投递失败的终态永不重试 |

修复（`backend/internal/api/skills/supervision_batch_recovery.go`）：

- `apiSubagentBatchTerminalSink`：终态 → `toolbroker.BuildSubagentBatchTerminalMailboxMessage` → `chat.DeliverMailboxEventFirstResult`（先落 event store 再通知 bus）；幂等边界是 mailbox 消息 id（`ParentSessionID + BatchID + DeliveryKey`），因此重放不会塞重复汇报。
- `apiSubagentBatchEmitter`：展示镜像与 CLI 同口径（事件挂 `parent_session_id`，`tool_name=spawn_subagents`）。
- `StartSubagentBatchRecovery` / `StopSubagentBatchRecovery`：两趟有界恢复（立即 + `restartGrace`=5m 后），恢复 coordinator **不带 Scheduler**（只收敛 durable 行 + 重放投递，不与持有 worker 的 coordinator 抢所有权）；单趟超时 15s、上限 512 行。
- 顺序固定「先 `RecoverStaleBatches`，再 `ReplayTerminalDeliveries`」：收敛产生的新终态同一趟就要投递。
- 未接线宿主保持逐字节不变：`StartSubagentBatchRecovery` 只读**已注入**的 store（不惰性建库）；没有 durable supervision 控制面时恢复直接 no-op。
- 接线：`newAPIAgent` 给共享 coordinator 装 sink；`runtime-server` 通过 `EnableDurableSubagentBatches(<data>/data/subagent-batches)` 落盘 store + 启动恢复，关闭时先 `StopSubagentBatchRecovery` 再 `Close`（建库失败只降级告警，与 usage ledger 同口径）。

验收证据（实测）：

- `go test ./internal/api/skills/ -run TestAPIBatchRecovery -count=1`：收敛 + 投递 + 幂等 + 宽限窗口 + 跨进程恢复。
- `go test ./internal/api/skills/ -run TestNewAPIAgentWiresBatchTerminalSink -count=1`：接线断言（`HasTerminalSink`）。
- `go test ./internal/api/skills/ ./internal/agent/ ./cmd/aicli/commands/ ./cmd/runtime-server/ -count=1` 全绿。

| 主题 | 位置 |
| --- | --- |
| API 终态投递 + 重启恢复 | `backend/internal/api/skills/supervision_batch_recovery.go` |
| sink 接线点 | `backend/internal/api/skills/handler.go`（`newAPIAgent`） |
| durable store 接线 | `backend/cmd/runtime-server/main.go`（`EnableDurableSubagentBatches` + `close()`） |
| recovery 语义（收敛/重放） | `backend/internal/agent/subagent_batch_coordinator.go:928/271` |
| 回归测试 | `backend/internal/api/skills/supervision_batch_recovery_test.go` |
