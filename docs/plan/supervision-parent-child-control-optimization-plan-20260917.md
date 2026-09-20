# 主 agent 对子 agent 的进度事件 / 定时巡检 / 主动控制能力优化实施方案

- 版本：v1.1（2026-09-17 工程实践修订：补齐决策记录、可观测性、容量预算、灰度回滚与里程碑 DoD）
- 状态：待评审。§9 决策记录为默认执行基线：评审中未否决的条目即按该决策实施；否决时在 §9 原地更新状态与替代决策（保留历史行）。
- 日期：2026-09-17
- 输入：`docs/plan/supervision-parent-child-control-analysis-20260917.md`（现状分析，§2–§5 缺口清单）
- 关联既有方案：`docs/plan/supervision-business-supervision-implementation-plan.md`（P0–P2 已落地：`supervision_descendants`、progress rollup、opt-in 巡查、自动收敛）、`docs/plan/spawn-agent-team-supervision-timeout-recovery-plan.md`、`docs/plan/supervision-operator-runbook.md`
- 实施注记模板：本方案落地后须在本文档追加"实施核验"章节（与 2026-09-16 方案同一惯例）

---

## 0. 目标与非目标

### 0.1 目标（对齐需求 R1–R4）

| 需求 | 目标状态 |
| --- | --- |
| R1 子→父进度事件 | 子代理运行期间产生**真实可持久化**的进度事实（`LastProgressAt` 有生产者）；高频进度走 live 通道、里程碑进父侧可读面；CLI/API 双宿主对等 |
| R2 定时巡检 | opt-in 巡查可安全开启：进度预算独立、存在 critical 未决时不抢跑；覆盖 batch 与单子代理；进度停滞可升级为告警 |
| R3 主动控制 | busy 子代理的指令有**消费保证**（`trigger_turn` 闭环）；`send_message/followup_task/send_input` 三态语义显式且双宿主一致；指令投递有审计 |
| R4 状态与产物 | `supervision_descendants` 可选携带有界结果/artifact 引用；新增按会话/任务读取结果契约的只读工具；关闭与产物保留的冲突有守卫或显式提示 |

### 0.2 非目标

- 不重做 supervision 架构：沿用通知模型、wake 预算、evaluator、audit、snapshot 读模型与既有 wake 通路。
- 不改变"成功不唤醒"默认策略：进度本身默认不产生唤醒；仅 opt-in 巡查与停滞告警例外（且受独立预算）。
- 不引入"每 N 秒扫描所有会话"的常驻全量轮询；所有周期性行为按需启停、有界。
- 不改 `wait` 模式语义、`spawn_subagents` 幂等键语义与 polling guard 既有契约。

### 0.3 成功度量（上线后 30 天内可验证）

| 指标 | 目标 | 观测方式 |
| --- | --- | --- |
| 进度新鲜度：running task 的 `LastProgressAt` 年龄 P95 | ≤10s（interval=5s 启用时） | debug 计数 + 抽查 SQLite 行 |
| 进度写入健康度：CAS 冲突丢弃率 | <5%（超出说明终态竞态或写入风暴） | debug 计数 |
| busy 指令送达率：busy 子会话的 `followup_task` 产生新 turn 的比例 | ≥95%（排除终态/关闭） | `agent.trigger_turn.consumed/dropped` 计数 |
| 自动触发风暴：同一 child 连续自动触发 ≥3 次 | 0 | 审计事件 |
| 巡查成本：progress wake 占父会话唤醒比例 | 可解释且不超独立额度 | `FormatWakeBudgetLine` |
| 结果可读性：终态任务可经 `read_agent_result` 取到 summary 的比例 | ≥95% | `source != none` 计数 |
| 回归：未接线宿主注入文本字节级不变 | 100% | golden 对比测试 |

---

## 1. 设计原则

1. **生产者优先**：先让进度有真实来源（写既有列/既有行），再做展示与消费；避免继续在"心跳/更新时间"上做语义再解释。
2. **不落新表优先**：能写既有的 `SubagentTaskRecord.LastProgressAt`、`BatchSummary`、mailbox、事件注册表的，不新表；确需新列时按 SQLite `ALTER TABLE ADD COLUMN` 兼容迁移。
3. **默认安全、显式开启**：所有新行为与字段默认关闭/缺省不变，未接线宿主逐字节兼容（沿用 P0-B 的三条硬约束）。
4. **预算先分账**：任何新增唤醒类别先分配独立预算，禁止与 critical（approval/failure）共用有界额度。
5. **双宿主对等**：CLI 与 runtime-server 必须同契约落地，测试按宿主分别门控（沿用 2026-09-16 的 API 控制器补齐经验）。
6. **模型可预测**：工具描述必须与真实语义一致；不一致时改实现或改描述，二者只留一个真相。

---

## 2. 方案总览

| 阶段 | 编号 | 内容 | 覆盖需求 | 关键文件 |
| --- | --- | --- | --- | --- |
| P0 | P0-1 | 进度事实生产者 + 事件通道登记 + CLI 进度镜像对等 | R1 | `agent/subagent_batch_coordinator.go`、`subagentbatch`、`events/contract.go`、`cmd/aicli/commands/chat_actor_*` |
| P0 | P0-2 | 巡查预算分账 + 启用路径（配置/提示词/runbook） | R2 | `supervision/wake_budget.go`、`supervision/config.go`、两个宿主的 progress check |
| P0 | P0-3 | 指令投递语义收敛 + `trigger_turn` 消费闭环 + 投递审计 | R3 | `toolbroker/broker.go`、`chat/actor.go`、`chat_actor_registry.go`、`session_runtime_support.go` |
| P0 | P0-4 | 结果/产物读出口（snapshot 可选结果 + `read_agent_result`） | R4 | `supervision/snapshot.go`、`toolbroker/supervision_tools.go`、`agentresult` |
| P1 | P1-1 | 进度里程碑持久化（进度 note）+ 子代理 `report_progress`（可选） | R1 | `subagentbatch`（列迁移）、`toolbroker` |
| P1 | P1-2 | 巡查覆盖单子代理 + 停滞升级为告警 | R2 | `chat_actor_progress_check.go`、`supervision_progress_check.go`、`supervision/run_alerts.go` |
| P1 | P1-3 | `close_agent` 产物守卫（worktree 未处理时 warn + `force`，见 ADR-6） | R3/R4 | `broker.go`、`chat_actor_registry.go`、`session_runtime_support.go` |
| P2 | P2-1 | 订阅式进度推送 / 进度触发策略钩子（不列入本期，触发条件见 ADR-8） | R1/R3 | 待定（见 §10 遗留开放问题） |

---

## 3. 详细设计

### 3.1 P0-1：进度事实生产者 + 事件通道登记 + CLI 镜像对等（R1）

**问题回顾**：`SubagentTaskRecord.LastProgressAt` 无生产写入者（`subagentbatch/sqlite_store.go:361-366/:713-736` 仅序列化）；`subagent.batch.progress / subagent.task.*` 未在 `events/contract.go:74-135` 登记（`ChannelsFor` 返回空）；live 镜像仅 API 宿主接线（`api/skills/session_runtime_support.go:971/:988-991`）。

**改动 1：task 进度写回（无 schema 变更）**

- 在 `backend/internal/agent/subagent_batch_coordinator.go` 的 task 级进度发射点（`:1386/:1426/:1435/:1444`；`subagent.task.started :1184`、`subagent.task.completed :1399`）追加一次有界写回：
  ```go
  // 伪代码：best-effort，节流窗口内合并
  if task.LastProgressAt == nil || now.Sub(*task.LastProgressAt) >= progressWriteInterval {
      store.UpdateTask(ctx, batchID, taskID, task.Version, func(t *subagentbatch.SubagentTaskRecord) {
          t.LastProgressAt = &now
      })
  }
  ```
- 约束：
  - **节流**：新增 `supervision.task_progress_interval`（struct 字段 `TaskProgressInterval`；目标默认 5s，M1 灰度期出厂 0=不写，M8 验证后转 5s）。窗口内合并、状态转换（started/completed/failed）强制写；
  - **CAS 安全**：写回走 `UpdateTask` 的版本校验；批次/任务进入终态后写入被拒绝（既有保护 `subagentbatch/sqlite_store_test.go:331` `TestTaskWritesRejectLateProgressAfterBatchOrphaned`）；
  - **best-effort**：写失败只记 debug 事件，不影响子任务执行；`VersionConflictError` 静默丢弃（迟到的进度）；
  - 不新增列、不改 JSON 结构。
- 落地效果：`batch_progress.go:205-219` 的 `taskProgressTime` 首选分支（`LastProgressAt`）第一次真正生效，progress age 语义从"记录更新时间"升级为"子代理真实进度"。

**改动 2：事件通道登记（消除"无通道"）**

- `backend/internal/events/contract.go` 为以下类型补登记（当前完全缺失）：
  - `subagent.batch.progress` → `ChannelLiveOnly`（高频，明确不落盘；沿用 `subagent.progress` 的定位 `contract.go:97-99`）；
  - `subagent.task.started` / `subagent.task.completed` → `ChannelTailOnly`（与 `subagent.batch.started/completed` 同档 `contract.go:102-103`）。
- 说明：登记不改变事件行为，只是消除"未注册 ⇒ ChannelsFor 返回 0 ⇒ 交付层无法判定"的隐式状态，为后续（P1）按需升格 durable 里程碑留出契约位置。
- 测试：`events/contract_test.go`（或既有 contractgen 测试）断言三类通道。

**改动 3：CLI 宿主进度镜像对等**

- 把 API 宿主已有的 `supervision.NewSubagentProgressMirror` 接线复制到 CLI（`cmd/aicli/commands/chat_actor_*` 的子会话事件订阅处）：订阅子会话 `toolprotocol.EventTypeProgress` → `mirror.Observe(target, event, now)` → 发布父侧 live 事件（不落库）→ 同时供 `BatchProgressSource.Messages`（`batch_progress.go:221-243`）富化。
- 要求：仅 live 转发、2s 合并窗口（`DefaultSubagentProgressWindow`）、随父 host `Close()` 关闭；CLI 测试断言"重复帧合并、状态变化保留、不写 event store"。
- 交付后 `supervision_descendants`/digest 的 `last_message` 富化在两个宿主一致。

**验收（P0-1）**

- 单测：task 进度写回节流 + CAS 拒绝迟到写入；contract 通道断言；CLI mirror 合并/不落库。
- 宿主级 e2e（复用 `supervision_e2e_three_background_test.go` 骨架）：3 个 background 子代理运行 ≥10s 后，`LastProgressAt` 与实际进度时间差 ≤ 节流窗口；父 turn preflight 的 `last progress Xs ago` 不再是 60s 心跳粒度。

### 3.2 P0-2：巡查预算分账与启用路径（R2）

**问题回顾**：progress wake 归入 `other` 类，与 critical lifecycle 共用 1h/5 次有界预算（`supervision_progress_check.go:31-36`；`wake_budget.go:29-31/:67-83`）；预算耗尽静默跳过（`:245-250`）。配置默认 0（`supervision/config.go:48-53/:110-113`），且 `backend/configs` 中不存在 `supervision` 段（本轮 grep 确认）→ 需求 R2 目前"代码可用、默认不可见"。

**改动 1：新增独立预算类别 `progress`**

- `backend/internal/supervision/wake_budget.go`：
  - 常量区新增 `WakeBudgetClassProgress WakeBudgetClass = "progress"`（`:20-32` 一带）；
  - `WakeBudgetClassOf`（`:67-83`）**最先**判定 `WakeReasonIsProgressCheck(reason)`（`:59-61` 已有判定函数）→ 返回 progress 类；
  - `Bounded()` 保持 `true`（progress 仍受自己的有界额度约束）。
- `WakeSchedulerConfig` 新增 `MaxProgressWakePerWindow int`；`Config`（`supervision/config.go`）新增 `WakeMaxProgressWake`（yaml `wake_max_progress_wake`，默认 6/窗口，0 表示默认值，负数可按现有约定表示不设限但**不推荐**）。
- 语义保证：
  - progress wake 不再消耗 failure/other 预算；critical 告警密集时进度汇报名义额度仍在，反之亦然；
  - durable 预算账本（`WakeBudgetModeDurable`）按类别写新 key，旧数据不受影响；
  - `FormatWakeBudgetLine`（`wake_budget.go:126+`）自动多渲染一行 `progress=x/6`，父 turn 可见分账状态。

**改动 2：critical 未决时的让位门（防抢跑）**

- 在两个宿主的巡查单次执行（`chat_actor_progress_check.go:93-151`、`supervision_progress_check.go` 对应逻辑）中，`pending wake` 门之后追加：
  - 若 scope 内存在 `SeverityCritical && Unresolved` 的通知，则本轮跳过 progress wake（critical 的投递会带来同一个 digest，其中已含 progress 区块）。
- 复用现有查询（`Store.ListNotifications`，与 digest 一致的口径），不新增存储访问模式；测试断言"存在 critical 未决 ⇒ 巡查不起 turn；critical 消除后恢复"。

**改动 3：启用路径（保持代码默认 0）**

- 在 agent 配置（`internal/agentconfig/config.go:37` 的 `supervision` 段）对应的默认/示例配置文件中提供**注释示例**：
  ```yaml
  supervision:
    # 周期巡查（默认关闭）：存在 running background batch 且父会话空闲时，
    # 按该间隔经既有 wake 通路注入一次进度摘要。
    # progress_check_interval: 90s
    # wake_max_progress_wake: 6
  ```
- `ProgressCheckInterval` 加载时做下限钳制（建议 `< 30s` 视为 30s 并记 warning），避免用户配置 1s 造成巡检风暴。
- `docs/plan/supervision-operator-runbook.md` 更新 §3：默认关闭的语义、开启后的成本、与 critical 告警的关系、如何用 `/debug supervision` 验证（wake 预算行）。

**验收（P0-2）**

- 单测：`WakeBudgetClassOf("progress_check") == progress`；progress 与 other 预算互不占用；critical 未决门；`FormatWakeBudgetLine` 渲染。
- 宿主级（复用 `chat_actor_progress_check_test.go` / `supervision_progress_check_api_test.go` 六条契约）：开 interval 后按间隔注入；critical 未决时跳过；关 interval 零 ticker。

---

### 3.3 P0-3：指令投递语义收敛与 `trigger_turn` 消费闭环（R3）

**问题回顾**：busy 子会话的指令只落 mailbox + `mailbox_received` 事件，`trigger_turn` 无消费方（`agent_mailbox.go:48-50` 全仓仅写入+测试）；`send_message` 与 `followup_task` 共用宿主分支（`broker.go:1714-1766`），idle 子会话都会起 turn（CLI `~:2213-2219`）；API `send_input(interrupt=false)` busy 时直接报错（`session_runtime_support.go:1755-1759`）与 CLI 静默投递不一致。

**改动 1：三态语义矩阵（先定契约，再改实现）**

| 工具 | 子会话 idle | 子会话 busy | 子会话终态 |
| --- | --- | --- | --- |
| `send_message` | **仅投递**，不启动 turn | 投递 mailbox；等子会话下一 turn 自然读取 | 返回明确错误（session closed），不静默丢弃 |
| `followup_task` | 投递 + **启动新 turn** | 投递 + `trigger_turn=true`；**run 结束后自动起新 turn**（改动 2） | 返回明确错误 |
| `send_input(interrupt=false)` | 投递 + 启动新 turn | 投递 mailbox + `trigger_turn=true`（与 followup 同语义，返回 `queued=true`） | 返回明确错误 |
| `send_input(interrupt=true)` | 投递 + 启动新 turn | **Stop → 等待退出（有超时）→ SubmitPrompt**（现状，双宿主统一超时上限） | 返回明确错误 |

- 返回体统一字段：`delivered`（是否已写入 mailbox）、`queued`（是否等待子会话消费）、`triggered`（是否提交了 turn）、`duplicate`（幂等命中）；工具描述同步改写（`broker.go:365-405`）。
- `send_message` 的 idle 行为按矩阵修正：CLI `deliverAgentMessage` 需按 kind 分派，`agent_message` 不调用 `SubmitPromptAsync`；API 侧同样。
- API `send_input(interrupt=false)` 的 busy 行为改为"排队投递"（`queued=true`），与 CLI 对齐；不再直接报错（保留 `interrupt=true` 的 Stop/Wait 语义，`:1760-1772`）。
- 终态目标：所有分支返回可读错误（`ErrAgentSessionClosed` 等），并写入投递审计（改动 4）。

**改动 2：`trigger_turn` 消费闭环（核心）**

- 在子会话 run 结束路径（`internal/chat/actor.go` 的 session-end 处理，与 `mailbox_received` 事件同一宿主链路）追加一次 drain：
  1. 读取该子会话 mailbox 中 `trigger_turn=true` 且未消费的消息（复用 `read_mailbox_digest`/agentcontrol mailbox read model 的读取与标记能力）；
  2. 按投递顺序合并为一次 prompt（多条时拼接并标注来源），标记为已消费（幂等；崩溃后重启不会重复触发）；
  3. 经既有 `SubmitPromptAsync` 提交一次新 turn；
  4. 失败保留未消费标记，下次 run 结束或 resume 时重试。
- 限流与防环：
  - 每个子会话同时最多 1 条待消费 trigger；新 trigger 覆盖旧 trigger 时合并文本而非排队多条；
  - 自动触发频率下限（建议 30s，同 child）与最大连续自动触发次数（建议 3，防止父子互相触发成环）；超限时降级为"仅 mailbox 留痕 + 父侧 digest 提示"。
- 该路径不消耗父侧 wake 预算（是子会话自己的 turn），但必须写入协作事件供父侧 `read_agent_events` 观察。

**改动 3：dispatch 拆分与双宿主统一**

- `toolbroker/broker.go`：`case ToolSendMessage` 与 `case ToolFollowupTask` 拆分为两个分支（当前合并 `:1714-1766`），分别映射到控制器的 `SendMessage` / `FollowupTask`；错误信息与返回字段统一。
- CLI `chat_actor_registry.go` 与 API `session_runtime_support.go` 的 `deliverAgentMessage` 按同一矩阵实现，禁止宿主各自解释语义。

**改动 4：投递审计（对齐批量路径）**

- 单发 `send_message/followup_task/send_input` 的投递结果写入与批量路径同级的 `mailbox_delivery_status / mailbox_delivery_error` 字段（`subagent_batch_coordinator.go:344/:761-763` 已有先例）；失败可被 `read_mailbox_digest` 与 `/debug` 观察到。
- 指令投递不进入 supervision control 动作管线（它是消息不是动作），但需要在父会话事件中留下一条可读记录（`mailbox_delivery` 事件或既有 mailbox 事件即可）。

**验收（P0-3）**

- 单测（两宿主）：矩阵 12 格（4 工具 × idle/busy/terminal 抽样）语义断言；`trigger_turn` drain 幂等（重复调用零副作用）；限流上限。
- 宿主级 e2e：busy 子代理收到 `followup_task` → 当前 turn 结束后自动起新 turn 并消费消息；`send_message` 不触发；`send_input(interrupt=true)` 打断后可观察旧 run 取消事件。

### 3.4 P0-4：结果与产物读出口（R4）

**问题回顾**：结果数据在 durable 侧齐全（`SubagentTaskRecord.ResultSummary/ArtifactRef` `types.go:226-228`、`TaskResult` `:254-268`、`BatchSummary` `:272-286`、`agentresult.Result` `agentresult/contract.go:11-110`），但 `SnapshotItem`（`snapshot.go:72-104`）不携带结果/产物，也没有按任务的查询工具；终态行只允许 `inspect`（`evaluator.go:36-41`），父 agent 取产物路径零散。

**改动 1：`supervision_descendants` 可选携带结果摘要（有界）**

- `supervision/snapshot.go`：
  - `SnapshotItem` 新增字段（仅在 `IncludeResults=true` 时填充）：`result_status`、`result_summary`（≤512 字符，按 rune 截断并标注 `result_truncated`）、`artifact_refs`（≤3 条，每条 ≤256 字符）、`error_class`、`finished_at`；
  - `SnapshotRequest` 新增 `IncludeResults bool`；`DescendantState` 增加对应透传字段（provider 负责从 batch/agentcontrol/run 读取）。
- `toolbroker/supervision_tools.go`：`supervision_descendants` 增加参数 `include_results`（默认 false）；工具描述说明"会显著增加输出，仅在需要收敛结果时使用"。
- 数据源优先级（provider 实现，`runtimeserver/supervision.go`）：
  1. batch task：`SubagentTaskRecord.ResultSummary`（`TaskResult` JSON）与 `ArtifactRef`；
  2. 单子会话：agentcontrol record / execution run 的结果引用（若存在）；
  3. 终态 mailbox completion payload（`BuildSubagentCompletionMailboxMessage :64-149` 的 status/success/error/usage），作为兜底摘要。
- 默认 `include_results=false` 保证既有输出逐字节兼容。

**改动 2：新增只读工具 `read_agent_result`（P0）**

- 契约（`toolbroker/supervision_tools.go`）：
  ```json
  {
    "id": "child session id or path (required)",
    "task_id": "optional batch task id",
    "sections": ["summary", "findings", "changes", "artifacts", "errors", "usage"],
    "max_chars": 4000
  }
  ```
- 行为：解析 scope（模型不可指定 root scope，沿用 `chat_supervision_tools.go:42-58` / `supervision_tool_controller.go:44-56`）→ 优先读取 batch task 的 `TaskResult`（含 `agentresult.Result` 投影）→ 回退 mailbox completion payload → 均无记录时返回 `no_result_recorded` 与可执行建议（`read_agent_events`/`wait_agent`）。
- 输出为**有界结构化结果**：`status/summary/findings(≤3)/changes(≤8)/artifacts(≤8)/errors(≤3)/usage`，超限置 `truncated=true`（沿用 `subagent_parent_summary.go:12-15` 的预算常量风格）。
- 工具注册门控：与其它 supervision 工具一致（控制器为 nil 时不注册）；两宿主同步落地并补差异测试。

**改动 3：结果写入保证（实施时核对落点）**

- 核对子会话完成路径是否已把有界结果写入 batch task `ResultSummary`/`ArtifactRef`（`subagentbatch/sqlite_store.go:713-736` 已支持写入 `result` JSON）；未写入则补接线，并使用既有 `ensureSubagentResultContract`（`agent/result_contract.go:73-116`）生成契约，避免父侧读到空结果。
- 契约一致性：确保 `TaskResult` 与 `agentresult.Result` 字段映射稳定（summary/findings/changes(status/artifact_refs)/errors/usage），由单测钉住。

**验收（P0-4）**

- 单测：`include_results` 开关兼容（默认输出不变）；结果截断预算；`read_agent_result` 的 scope/回退/空结果语义。
- 宿主级 e2e：3 个 background 子代理（1 成功 / 1 失败 / 1 超时）完成后，单次 `supervision_descendants(include_terminal=true, include_results=true)` 可见每行 status/summary/artifact_refs；`read_agent_result` 可取到成功任务的 findings 与失败任务的 error_class。

---

## 4. P1 增强项（评审后按序推进）

### 4.1 P1-1：进度里程碑持久化与子代理主动上报（R1）

- `subagentbatch` 增加 `progress_note`（≤256B，`ALTER TABLE ADD COLUMN` 兼容迁移）与写入方：live 镜像的 `LastMessage` 在节流窗口内同步落库（best-effort），使父侧在无 live 通道时也能看到"最近在做什么"。
- 新增子代理工具 `report_progress`（仅 spawn_agent 子会话工具面可见）：
  ```json
  { "phase": "running|blocked|milestone", "note": "…", "percent": 0 }
  ```
  写入同一 durable 行（`LastProgressAt` + `progress_note`），`blocked` 时额外投递一条父侧 mailbox 里程碑消息（复用 completion mailbox 的去重键风格 `agent_mailbox.go:194-216`）。
- 默认开启（仅写既有行，成本有界）；工具描述强调"只在阶段性节点调用，不要每步调用"。

### 4.2 P1-2：巡查覆盖扩展与停滞升级（R2）

- 巡查 active 判定从 batch-only（`batch_progress.go:59-63/:87-106`）扩展到"batch 或 agentcontrol/execution run 中 running 的子会话"，覆盖单 `spawn_agent` 场景；
- 进度停滞升级：`progress_age_ms` 超过 `HeartbeatTimeout`（默认 5m，`config.go:15-17`）时产生一条 warning 级 `progress_stalled` 通知（复用既有告警类型），由 failure 预算唤醒父 agent；run 终态时按既有 `run_alerts.go` 收敛语义关闭（2026-09-16 已修复）；
- 去重：同一 subject 在窗口内只产生一条，避免"每次巡检都刷一条"。

### 4.3 P1-3：`close_agent` 产物守卫（R3/R4）

- 关闭前检查 worktree 隔离是否存在未处理变更：
  - 存在且未被 apply/discard → 返回可读错误 `worktree_pending`（附 apply/discard 指引），除非显式 `force=true`；
  - 关闭成功时把"清理了哪些 worktree/路径"写入关闭回执事件，保证审计可查（当前 `close_agent` 绕过 supervision 审计管线，至少要有宿主侧事件）。
- 默认行为变更需评审：按 ADR-6 先以 warning + `force` 参数灰度（返回提示但默认继续清理），再依据误删数据/用户反馈评估是否改为拒绝。

---

## 5. 变更清单（接口 / 配置 / 数据）

| 类别 | 变更 | 兼容策略 |
| --- | --- | --- |
| 工具 | `supervision_descendants` 增 `include_results`（默认 false） | 默认输出逐字节不变 |
| 工具 | 新增 `read_agent_result` | 控制器 nil 时不注册 |
| 工具 | `send_message/followup_task/send_input` 描述与返回字段（`queued/triggered/duplicate`） | 新增字段向后兼容；描述同步 |
| 工具（P1） | 新增 `report_progress`（子会话） | 暂不引入（ADR-5）；若引入则仅子会话工具面、默认低频 |
| 配置 | `supervision.wake_max_progress_wake`（默认 6/窗口） | 新字段；0=默认值 |
| 配置 | `supervision.task_progress_interval`（灰度期默认 0，M8 后目标默认 5s） | 新字段；0=不写（现状） |
| 配置 | `supervision.progress_check_interval` 下限钳制（≥30s） | 已存在字段的行为加固 |
| 事件契约 | 注册 `subagent.batch.progress`（LiveOnly）、`subagent.task.started/completed`（TailOnly） | 仅补登记，不改行为 |
| 预算 | 新增 `WakeBudgetClassProgress` | 新 durable ledger key；旧行不影响 |
| 数据（P1） | `subagent_task_records.progress_note` 列 | `ALTER TABLE ADD COLUMN`，旧行 NULL |
| 读模型 | `SnapshotItem` 结果字段（仅在 include_results 时填充） | 默认省略（omitempty） |
| 返回契约 | 指令类工具返回 `delivered/queued/triggered/duplicate`；新增错误码 `ErrAgentSessionClosed`、`no_result_recorded`、`worktree_pending` | 新字段向后兼容；错误码写入工具描述与 runbook |
| 开关 | `supervision.message_semantics_v2`（bool，默认 false，灰度通过后转默认 true） | 关闭时行为 = 2026-09-17 现状 |
| 开关 | `supervision.trigger_turn_auto`（bool，默认 true；仅在 v2 语义下生效） | 关闭时 busy 指令仅投递、不自动起 turn |
| 开关（P1） | `supervision.close_agent_worktree_guard`（`off|warn`，默认 `warn`） | 非破坏性；`force=true` 始终可用 |

---

## 6. 测试与验收

### 6.1 需求 → 场景 → 测试映射

| 需求 | 验收场景 | 测试落点（新增/扩展） |
| --- | --- | --- |
| R1 | spawn 3 background 运行 ≥10s：`LastProgressAt` 与真实进度差 ≤ 节流窗口；父 turn preflight 出现真实 progress age；CLI/API 均有 live 镜像 | `subagentbatch` 单测 + `supervision_e2e_three_background_test.go` + CLI mirror 测试 |
| R2 | 开 interval：父空闲期按间隔收到 progress 汇报；存在 critical 未决时跳过；progress 预算不占 failure/other；关闭时零 ticker | `chat_actor_progress_check_test.go`、`supervision_progress_check_api_test.go`、`wake_budget_test.go` 扩展 |
| R3 | busy 子代理收到 `followup_task` → run 结束后自动起新 turn（幂等、限流）；`send_message` 不触发；API/CLI 对 `send_input` 行为一致 | 两宿主 `deliverAgentMessage` 测试 + `trigger_turn` drain 集成测试 |
| R4 | `supervision_descendants(include_results=true)` 一行可见结果；`read_agent_result` 取回有界契约；默认输出不变 | `snapshot_test.go`、`supervision_tools_test.go`、宿主级 e2e |

### 6.2 回归守护（不得破坏的既有契约）

- `wait` 模式语义、`spawn_subagents` 幂等键、`agents.autoCloseCompleted` 收敛语义；
- polling guard / doom-loop 既有契约（巡查工具豁免、写动作不豁免）；
- 未接线宿主逐字节兼容（progress source nil、supervision 控制器 nil）；
- ack 后不重复注入（N9）、defer 到期回归、wake 限流不丢消息。

### 6.3 建议执行命令（实施时）

```powershell
go build ./...
go test ./internal/supervision/... ./internal/subagentbatch/... ./internal/events/... -count=1
go test ./internal/toolbroker/... ./internal/agent/... -count=1
go test ./cmd/aicli/commands/ -run "Supervision|ProgressCheck|Agent" -count=1
go test ./internal/api/skills/ -run "Supervision|Agent" -count=1
```

### 6.4 测试层次与门禁（每个里程碑都要满足）

| 层次 | 内容 | 门禁 |
| --- | --- | --- |
| 单元 | 状态机、节流、CAS 冲突、预算分类、参数解析 | 目标包 `-count=1` 全绿；新增分支有对应用例 |
| 契约 | 工具 schema/返回字段、事件通道、JSON `omitempty` 兼容 | 契约测试钉住；默认输出 golden 对比 |
| 宿主门控 | CLI 与 runtime-server 分别断言"未接线不注册/不开启" | 双宿主各自包测试 |
| 集成 | 三子代理 e2e（成功/失败/超时）、busy 指令 drain、critical 让位 | `supervision_e2e_*` 扩展用例 |
| 故障注入 | CAS 冲突、mailbox 重放/重复、预算耗尽、ticker 抖动 | 显式构造并断言降级语义（不吞错、不重复触发） |
| QA | `go build ./...`、目标包测试、`git diff --check`、并发用例 `-race` | CI 或本地等价命令 |

每个里程碑的 Definition of Done 见 §8 表内"退出标准"列。

---

## 7. 风险、容量与可观测性

### 7.1 风险登记与缓解

| # | 风险 | 触发条件 | 缓解 | 回滚 |
| --- | --- | --- | --- | --- |
| 1 | 进度写回造成 SQLite 写入放大 | 大量并发 running 子任务、interval 过小 | 节流窗口（默认 5s）+ 仅 background batch + best-effort + CAS 静默丢弃；写回频率可配 | `supervision.task_progress_interval=0` 回到现状（无写入） |
| 2 | `trigger_turn` 自动起 turn 形成父子互触发环 | 父子互相 followup、消息风暴 | 每 child 同时最多 1 条待消费 trigger；同 child 30s 下限；连续自动触发 ≤3 次 | 宿主级开关（默认开）或把 `followup_task` busy 行为退回"仅投递" |
| 3 | 结果字段撑大输出 | `include_results=true` + 大 summary/多 artifact | rune 截断 + 条数上限 + `truncated=true`；默认 false | 不传参数即现状 |
| 4 | `send_message` idle 语义变更破坏既有调用方 | 依赖"消息即起 turn"的外部用法 | 变更写入 release note；双宿主测试；灰度期保留 `followup_task` 作为显式触发路径 | 恢复 idle SubmitPrompt 行为（改描述而非实现） |
| 5 | 新预算类别改变唤醒总量 | progress 汇报频率高于预期 | 独立额度默认 6/窗口 + critical 未决让位门 + `/debug` 预算行观测 | `wake_max_progress_wake=0`+ 关 interval 回到现状 |
| 6 | 事件契约登记牵动前端生成物 | contractgen 单一真源 | 登记后运行 contractgen 并提交生成物；只加类型不改既有 | 移除登记项即可 |
| 7 | `close_agent` 守卫改变默认行为 | 既有"直接关闭"用法 | 先 warning+`force` 灰度，再评估拒绝 | `force=true` 或回退守卫 |
| 8 | 进度写回与 SQLite 单写者锁争用 | 高并发 running 任务 + 5s 写回 | 单行短事务、冲突即丢弃（不重试）、仅 background batch；压测门槛见 §7.2 | `task_progress_interval=0` |
| 9 | 自动触发 turn 的 token/成本上升 | busy 指令密集、父子互相触发 | 每 child 1 条待消费 + 30s 下限 + 连续 ≤3 次环保护 | `trigger_turn_auto=false` |

### 7.2 性能与容量预算

| 维度 | 预算 | 依据 / 验证 |
| --- | --- | --- |
| 进度写回 | 每 running task ≤1 次/`task_progress_interval`（默认 5s）；50 并发 running ≤10 写/s；单行短事务、冲突不重试 | 写放大可算；`subagentbatch` 增加 benchmark 与冲突用例；若 P95 写延迟 >50ms 或锁等待显著，调大 interval |
| 巡查唤醒 | ≤`wake_max_progress_wake`（默认 6）/窗口/root scope；critical 未决时为 0 | 复用 durable claim 账本；`/debug supervision` 可核对 |
| 自动触发 | 每 child ≤1 条待消费 trigger；同 child ≥30s；连续自动触发 ≤3 次 | 环保护与幂等用例 |
| 结果读取 | snapshot 行 ≤512 rune + 3 refs；`read_agent_result` ≤4000 字符 | rune 截断 + `truncated` 标记；不改变 digest 既有 4000 字符预算 |
| 内存 | 复用 live mirror 上限（512 条目 + 10×窗口剪枝，`subagent_progress.go:29-35`） | 不新增无界结构 |

### 7.3 可观测性

| 信号 | 载体 | 用途 / 验收 |
| --- | --- | --- |
| 进度写回结果 | 宿主 debug 计数（成功/窗口跳过/冲突丢弃），经 `/debug supervision` 输出一行 | 上线后确认"生产者真的在写"；能区分节流与故障 |
| wake 预算分账 | `FormatWakeBudgetLine` 自动新增 `progress=x/6` 行 | 父 turn 与 `/debug` 直接可见 progress 是否被限流 |
| 指令投递 | mailbox `mailbox_delivery_status/error`（单发与批量统一）+ 工具返回字段 | 排障"指令是否送达" |
| trigger drain | 子会话事件流写 `agent.trigger_turn.consumed/dropped`（父侧 `read_agent_events` 可见） | 验证 busy 指令确实产生新 turn；dropped 可解释 |
| 结果读取 | `read_agent_result` 返回 `source`（task_result/completion_payload/none）与 `truncated` | 判断结果质量与回退路径 |
| 运维路径 | runbook 增加"进度生产者是否工作""trigger drain 是否生效"两条排查 | 不依赖实现者即可复现 |

实现约定：不新增指标后端，全部复用既有 `/debug supervision`、事件总线与日志，避免为观测引入第二套基础设施。

### 7.4 灰度发布与回滚开关

| 能力 | 开关 | 默认 | 回滚动作 | 生效范围 |
| --- | --- | --- | --- | --- |
| 进度写回 | `supervision.task_progress_interval` | 0（灰度期）→ 5s（M8 后） | 置 `0` | 两宿主 |
| 周期巡查 | `supervision.progress_check_interval` | 0（关） | 置 `0` | 两宿主 |
| progress 预算 | `supervision.wake_max_progress_wake` | 6 | 置 `0` 并关巡查 | supervision 控制面 |
| 指令语义 v2 | `supervision.message_semantics_v2` | false（灰度后转 true） | 置 false | 两宿主 |
| 自动 trigger turn | `supervision.trigger_turn_auto` | true（仅 v2 语义下生效） | 置 false | 两宿主 |
| 结果读出口 | 工具参数 `include_results` / `read_agent_result` | false / 只读 | 不传参即现状 | 工具面 |
| close 守卫（P1） | `supervision.close_agent_worktree_guard` | `warn` | 置 `off` | 两宿主 |

**灰度顺序**：M1 进度写回以"interval 出厂 0、单会话显式开启"验证 1 天；M5 指令语义 v2 先开 `message_semantics_v2` 灰度，观察 trigger 计数与 turn 成本后转默认；任一步回滚只动开关、不改数据。

**总回滚原则**：所有 P0 行为都可通过"配置关 + 工具参数默认值"回到 2026-09-17 现状；不引入需要数据回滚的变更（列迁移只做加列）。

---

## 8. 实施里程碑与 Definition of Done

| 里程碑 | 内容 | 依赖 | 退出标准（需同时满足） | 回滚点 |
| --- | --- | --- | --- | --- |
| M0 基线冻结 | 复核分析行号（工作区存在并行改动）、重跑目标包基线测试、评审 §9 决策 | 无 | `go build ./...` + 目标包测试全绿；行号复核写入"实施核验" | 不适用 |
| M1 进度写回（P0-1a） | task 进度写回（节流 + CAS；出厂 interval=0） | M0 | 节流/冲突/终态拒绝单测；benchmark 满足 §7.2；`/debug` 计数可见 | `task_progress_interval=0` |
| M2 预算与让位门（P0-2a） | `WakeBudgetClassProgress` + critical 让位 + 配置钳制 | M0 | 分类/互不占用/让位/钳制用例；`FormatWakeBudgetLine` 快照 | `wake_max_progress_wake=0` + interval 0 |
| M3 结果读出口（P0-4） | snapshot 结果字段（gate）+ `read_agent_result` | M0 | 默认输出 golden 不变；截断/回退/scope 用例；e2e 可见三态结果 | 不传参即现状 |
| M4 事件契约（P0-1b） | 登记 `subagent.*` 通道 + contractgen 生成物 | M1（可并行） | 契约测试 + 前端类型 diff 评审通过 | 移除登记项 |
| M5 指令语义 v2（P0-3a） | dispatch 拆分、返回字段、双宿主统一；`message_semantics_v2` 灰度 | M0 | 两宿主矩阵用例；关闭开关时与现状逐字节一致 | `message_semantics_v2=false` |
| M6 trigger drain（P0-3b） | 消费闭环 + 幂等 + 限流/环保护 + 审计事件 | M5 | 幂等/限流/环保护/重启重试用例；观测信号可查 | `trigger_turn_auto=false` |
| M7 CLI 镜像对等（P0-1c） | CLI mirror + 双宿主输出对齐 | M1 | mirror 合并/不落库用例；两宿主 digest 对齐快照 | 移除接线（回到无富化 live-only） |
| M8 联调与文档（P0 收口） | R1–R4 e2e、runbook/提示词更新、默认值评审 | M1–M7 | §6 全部门禁通过；runbook 两条排查路径演练通过；默认值评审留痕 | 各开关回默认 |
| M9 P1 增强 | P1-1/P1-2/P1-3（评审后启动） | M8 | 各专项测试 + 灰度开关就位 | 见 §7.4 |

> 依赖与并行：M1/M2/M3/M5 可并行开发；M4 依赖 M1 的事件语义冻结；M6 依赖 M5；M7 依赖 M1。每个里程碑完成后在本文档追加"实施核验"条目（状态、命令、残余事项）。

---

## 9. 决策记录（ADR，默认执行基线）

> 状态：Accepted（2026-09-17 建议基线）。评审未否决即按此实施；变更需修改本表并记录原因与影响范围。

| ADR | 决策 | 依据 | 备选与否决理由 | 反转成本 |
| --- | --- | --- | --- | --- |
| ADR-1 进度写回范围 | 仅 background batch；`wait` 前台任务不写（P2 再评估） | 前台父 turn 同步等待，preflight 消费不到；写回纯成本 | 全部写：写放大无收益，否决 | 低（一处条件 + 用例） |
| ADR-2 progress 预算额度 | 独立类，默认 6/窗口，不做空闲加权（v1） | 先有界再调参；claim 账本可观测；空闲加权增加语义复杂度 | 与 failure 共用：会饿死 critical，否决；无限：turn 风暴，否决 | 低（改数值） |
| ADR-3 `send_input(interrupt=false)` busy 语义 | 排队投递 + `queued=true`；终态返回明确错误；打断只由 `interrupt=true` 表达 | 模型可预测；双宿主一致；与 `followup_task` 对齐 | 双宿主统一报错：迫使模型只能打断，否决 | 中（行为 + 测试 + 描述） |
| ADR-4 进度 note 列迁移时机 | P0 不迁移，只写 `LastProgressAt`；note 列随 P1-1 一次迁移 | P0 零 schema 变更 = 可回滚；R1 验收不依赖 note | P0 加列：扩大 P0 变更面，否决 | 低（提前 P1-1） |
| ADR-5 `report_progress` 工具 | v1 不引入；触发条件：P1 数据显示"停滞但 note 为空">20%，或产品明确需要里程碑语义 | 自动进度 + note 已覆盖多数场景；新工具带 token/提示词成本 | 直接引入：成本先于证据，否决 | 低（子会话工具面新增） |
| ADR-6 close 守卫档位 | P1 先 `warn` + `force`（默认仍清理）并写审计；升级为拒绝需数据支撑 | 非破坏性灰度；不破坏既有流程 | 直接拒绝：破坏面大，否决 | 低（改默认档位） |
| ADR-7 巡查默认开启 | 代码与 shipped 配置双默认关闭；提供配置片段 + runbook + 提示词引导 | token 成本未实测；与"无常驻轮询"原则一致 | 出厂默认 90s：成本不可控，否决（评审可覆盖） | 低（改默认值） |
| ADR-8 P2 订阅推送 | 不列入本期；触发条件：出现"无 turn 时实时感知进度"的真实产品场景 | 现有 SSE 镜像 + 巡查 + digest 已覆盖模型决策所需 | 本期立项：需求未验证，否决 | 高（新能力，需重新立项） |

## 10. 遗留开放问题（需要产品输入，不阻塞 M1–M8）

1. 是否在 shipped 配置中默认启用巡查（ADR-7 默认关闭，产品可覆盖）；建议先看 M8 后 progress wake 的成本遥测再定。
2. P2 订阅推送是否纳入下一期路线图（即 ADR-8 的触发条件是否成立）。

---

## 附：证据边界

- 本文引用的行号来自 2026-09-17 工作区（HEAD `d107a885`）只读取证与本会话三个只读调查子代理的交叉验证；实施前若目标文件已被并行改动，需先复核行号。
- 标记为"实施时核对落点"的两处（结果写入 batch task 的完成路径、`trigger_turn` drain 的宿主挂点）在现状分析中未能定位到唯一确定的代码位置，属于实施第一步必须确认的事项。

---

## 实施核验（2026-09-17）

> 按 §8 惯例追加。实施基线：工作区 HEAD `5a87260f` + 并行未提交改动（分析文档基于 `d107a885`，行号已按当前代码复核）。
> P0（M1–M8）全部落地；P1（M9：P1-1/P1-2/P1-3）按 §8「评审后启动」未实施。

### 1. 里程碑结果

| 里程碑 | 状态 | 落地内容与证据 | 残余事项 |
| --- | --- | --- | --- |
| M0 基线冻结 | ✅ | `go build ./...` 全绿；目标包基线测试全绿；§3.1/§3.3 行号复核（本文 §3 偏差清单） | 无 |
| M1 进度写回 | ✅ | `SubagentBatchCoordinatorConfig.TaskProgressInterval` + background-only 刷新器（每任务 ≤1 次/窗口，CAS 冲突静默丢弃，终态 fence 拒绝）、started/settle 强制写、`TaskProgressWriteCounts()`；`internal/agent/subagent_task_progress_test.go`（7 用例 + benchmark，`-count=3 -race` 通过）。**额外修复** `subagentbatch.scanTaskRow` 读投影缺口（见 §3 偏差 1） | 灰度期出厂 `0`（不写）；M8 后按 30 天遥测再评估 5s |
| M2 预算与让位门 | ✅ | `WakeBudgetClassProgress` 独立额度（`wake_max_progress_wake` 默认 6、负数不设限）、`WakeBudgetClassOf` 优先判定 progress、durable 账本按类分账；双宿主 critical 未决让位门；`ProgressCheckInterval < 30s → 30s` + warning；`/debug` 与 preflight `wake_budget:` 自动多渲染 `progress=used/limit`；`wake_progress_budget_test.go`/`config_progress_test.go` + 双宿主 ProgressCheck 用例 | 巡查默认关（ADR-7）；让位门对 defer 未到期的 critical 同样生效（按 §3.2 原文） |
| M3 结果读出口 | ✅ | `supervision_descendants(include_results=true)`（summary ≤512 rune、refs ≤3×256、`result_truncated`）；新只读工具 `read_agent_result`（双宿主、有界 sections、`source=task_result/completion_payload/none`、`no_result_recorded` + next_action）；provider 优先级 batch `TaskResult` → execution run 引用 → 终态 mailbox completion；结果写入补齐 `ChildSessionID` 列与 `TaskResult.UsageTotal`（CAS best-effort，不影响结果写） | `duplicate` 与 P1 项无关；include_results 每次有界扫描（batch ≤32 / mailbox ≤64 / run ≤32） |
| M4 事件契约 | ✅ | `subagent.batch.progress → ChannelLiveOnly`；`subagent.task.started/completed → ChannelTailOnly`；`runtimeobserve` 已知目录同步；contractgen 生成物（`frontend/src/types/runtime/event-contract.ts`）已更新且 `-check` 通过 | 无 |
| M5 指令语义 v2 | ✅ | 三态矩阵（send_message 仅投递 / followup_task 投递+按需起 turn / send_input 排队或打断 / 终态 `ErrAgentSessionClosed`）双宿主一致；返回字段 `delivered/queued/triggered/duplicate`；工具描述同步；投递审计 `mailbox_delivery`；开关 `message_semantics_v2`（默认 false=逐字段兼容） | `duplicate` 字段已贯通但恒 false（单发指令无调用方幂等键，契约未新增） |
| M6 trigger drain | ✅ | run 结束 drain：合并 trigger_turn 消息为一次 `SubmitPromptAsync`、事件账本幂等（`agent.trigger_turn.consumed`，重启不重复）、失败保留未消费重试；限流/环保护（同 child 30s、连续 ≤3，超限写 `agent.trigger_turn.dropped` 且 mailbox 留痕）；`trigger_turn_auto`（默认 true，仅 v2 生效） | 限流计数为进程内状态（重启清零）；账本按最近 1000 事件窗口恢复 |
| M7 CLI 镜像对等 | ✅ | CLI per-host `SubagentProgressMirror`（2s 窗口、live-only、session_end/Close 收敛）+ `BatchProgressSource.Messages` 富化；API 侧同款 per-host 镜像与 Messages 接线；双宿主富化用例 | 无 |
| M8 联调与文档 | ✅ | 本核验章节；runbook 新增 §3.4（巡查）、§5.1（进度写回读法）、§5.5（指令 v2/trigger drain 排查）、§5.4 两条排障行、§6 索引；`backend/configs/config.yaml` supervision 注释样例（含 `message_semantics_v2`/`trigger_turn_auto`）；门禁命令见下 | 默认值维持灰度（见 §4），转默认需 30 天遥测评审 |

### 2. 测试与门禁（2026-09-17 执行）

```powershell
cd backend
go build ./...                                                                     # ✅ exit 0
go test ./internal/supervision/... ./internal/subagentbatch/... ./internal/events/... ./internal/runtimeserver/... -count=1   # ✅ 全绿
go test ./internal/toolbroker/... ./internal/chat/... ./internal/api/skills/... ./cmd/aicli/commands/... -count=1             # ✅ 全绿
go test ./internal/agent/... -count=1                                              # ✅ 全绿（含 M1 用例）
go test ./internal/chat/ -run "TriggerTurn" -count=1 -race                         # ✅（drain 幂等/限流/环保护）
go test ./internal/agent/ -run "TestSubagentTaskProgress" -count=3 -race            # ✅ 无竞态
git diff --check                                                                   # ✅ 无空白错误
```

需求 → 场景对照（§6.1）：

- R1：`TestSubagentTaskProgress*`（写回节流/CAS/终态拒绝/仅 background）+ `TestSupervisionE2E_TaskProgressAgeFromLastProgressAt`（durable 行 → rollup `last progress 7s ago`，不再退回 60s 心跳）+ 双宿主 mirror 用例。
- R2：`TestWakeProgressBudget*`（分类/独立额度/渲染）+ 双宿主 ProgressCheck 用例（按间隔注入、critical 让位与恢复、关 interval 零 ticker、1s→30s 钳制）。
- R3：`broker_message_semantics_test.go` + 双宿主矩阵抽样 + `trigger_turn_drain_test.go`（busy followup → run 结束自动起 turn、幂等、限流、环保护）。
- R4：`snapshot_results_test.go`/`agent_result_test.go` + `chat_supervision_result_tools_test.go`/`internal/api/skills` 的 read 工具用例 + `supervision_results_test.go`（优先级/scope 隔离/两种 JSON 形状）。

### 3. 与方案的偏差（以当前代码为准）

1. **`subagentbatch.scanTaskRow` 读投影缺口（实施中发现，必须修复）**：`last_progress_at`/`started_at`/`finished_at`/`task_deadline` 四列写入正常但读回时被丢弃，导致 `LastProgressAt` 永远读不到、M1 节流恒不命中、`batch_progress` 首选分支失效。已按写路径列序对称回填并有往返测试；`getTaskTx` 共用同一扫描因此 `UpdateTask` 回调也能看到真实 `StartedAt`。
   **更正（2026-09-20 复核）**：「四列写入正常」只对 `started_at`/`finished_at` 成立。取证批次 `batch_42d0a52d2a24e401` 的 `subagent_tasks` 行显示 `task_deadline` / `last_progress_at` 在**写侧**从未被填充（全为 NULL），并非仅读投影丢弃；该写侧缺口已由 `multi-agent-durable-lifecycle-hardening-plan-20260920.md` P1-1（H8）修复。
2. **broker dispatch 已是分离分支**：现状 `case ToolSendMessage, ToolFollowupTask` 内部已分别调用 `SendMessage`/`FollowupTask`，「拆分」在实施时已无必要；真正的工作在三态语义、返回字段与双宿主一致（已完成）。
3. **`trigger_turn` 消费标记**：agentcontrol mailbox 无 consumed 字段，方案所述「复用 read model 标记」不可行；实现改为 session events 持久账本（`agent.trigger_turn.consumed` + 高水位/ID 集合），语义等价且父侧可读。
4. **结果兜底通道**：completion payload 落在父会话 **AgentControl session mailbox**（非 `team.Store.InsertMail`，后者要求 TeamID）；读取器按真实通道实现并做 parent/root scope 二次校验。
5. **`SubagentTaskRecord.ChildSessionID` 原无生产写入者**：终态落结果时补一次 best-effort CAS（不覆盖已有值、不影响结果写）；`TaskResult.UsageTotal` 同步打通（复用同包 `usageTotal()`）。
6. **事件登记连带**：`runtimeobserve` 已知目录与 contractgen 生成物必须同步，否则既有双向一致门禁失败（已包含在 M4 产物内）。
7. **`duplicate` 字段恒 false**：单发指令没有调用方幂等键；字段保留以稳定契约，真正去重需后续在调用方引入 idempotency key（未改契约）。

### 4. 默认值与灰度（M8 评审留痕）

维持 §7.4 的灰度基线，不在本轮转默认：

| 开关 | 出厂值 | 说明 |
| --- | --- | --- |
| `supervision.task_progress_interval` | `0` | 关闭=不写（现状）；灰度先单会话显式开启验证 1 天，再按遥测决定是否转 5s |
| `supervision.progress_check_interval` | `0` | 巡查默认关；开启需 `wake_max_progress_wake` 与 `/debug` 预算行共同观察 |
| `supervision.wake_max_progress_wake` | `6`（默认语义） | 0=默认 6；负数不设限（不推荐） |
| `supervision.message_semantics_v2` | `false` | 先灰度观察 trigger 计数与 turn 成本，再转默认 true |
| `supervision.trigger_turn_auto` | `true`（仅 v2 生效） | 环保护命中时降级为 mailbox 留痕 + dropped 事件 |

### 5. 残余风险与后续

1. `duplicate` 恒 false（见 §3 偏差 7）；如需真正幂等需扩展调用方契约。
2. trigger drain 的限流计数为进程内、账本按最近 1000 条事件恢复；事件保留窗口远大于该窗口，理论重复触发风险低，但裁剪极端场景下需人工核对 `agent.trigger_turn.consumed`。
3. `include_results` 为每次调用的有界扫描（batch ≤32、mailbox ≤64、run 查询 ≤32）；行数增长后需调参。
4. P1-1（`progress_note` + `report_progress`）、P1-2（单子代理巡查 + `progress_stalled` 告警）、P1-3（`close_agent` worktree 守卫）未实施，触发条件见 §4 与 ADR-5/ADR-6。
5. CLI 的结果源装饰在控制器层（`chat_supervision_tools.go`），API 在 `cmd/runtime-server/main.go` hooks；功能等价，后续可统一到一处 hooks 以减少装配差异。
