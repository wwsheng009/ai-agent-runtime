# 主 agent 对子 agent 的进度事件 / 定时巡检 / 主动控制能力现状分析报告

- 日期：2026-09-17
- 范围：`backend/internal/{supervision,subagentbatch,agentcontrol,agentresult,artifact,agent,events,toolbroker,api/skills}`、`backend/cmd/aicli/commands`、`backend/cmd/runtime-server`，以及 `docs/plan` 既有监督方案
- 方法：文档复盘（2026-09-16 既有 gap analysis / implementation plan / runbook）+ 代码只读取证 + 3 个只读调查子代理交叉验证（进度事件链路 / 巡检链路 / 控制与产物链路）
- 证据口径：本文所有 `file.go:line` 均来自本会话实际读取；未能读到实现的位置显式标注「未验证」，不做推断性引用
- 配套方案：`docs/plan/supervision-parent-child-control-optimization-plan-20260917.md`

---

## 0. 需求拆解与验收口径

### 0.1 需求原文拆解

| 编号 | 需求 | 判定要点 |
| --- | --- | --- |
| R1 | 主 agent 分配任务后，子 agent 能**通过事件**向主 agent 发送进度消息 | 事件有类型、有时间戳、有进度语义；父侧有确定的消费通道（不依赖模型恰好调用某工具）；可丢失性与降噪有定义 |
| R2 | 主 agent 能**定时巡检**子 agent 的工作进度与状态 | 存在按间隔触发的主动巡检通道；巡检产生父 turn 可读的摘要；可开关、有预算上限、与"无常驻轮询"约束兼容 |
| R3 | 主 agent 能**主动控制**子 agent 行为：按进度发新指令 / 关闭 agent / 查询状态 | 指令投递语义明确（中断当前 turn vs 排队到下一 turn）；有投递确认；关闭/取消受动作校验约束且可审计 |
| R4 | 主 agent 能查询子 agent 的**产物**（隐含：任务→状态→结果→工件引用可读） | 从父侧工具面能读到任务终态、结果摘要、artifact_ref；且读模型有界、不引入 token 爆炸 |

### 0.2 判定口径（可验收）

- **R1**：子任务运行期间，父侧（事件存储或父会话 mailbox）能看到带游标/序号的进度记录；父 agent 的读取原语（`read_agent_events` 等）能看到它；缺失时有确定的降级行为。
- **R2**：开关打开后，父会话在空闲期能按 interval 收到进度汇报 turn；关闭/无活跃批次时零附加行为；巡检唤醒与 critical 生命周期告警不互相饿死。
- **R3**：每个控制工具都有"运行中 / 空闲 / 已终态"三态下的投递语义说明；`send_input` 类工具能说明是否中断；关闭类动作在目标已终态时返回可读错误而不是静默失败；动作产生审计记录。
- **R4**：snapshot/batch 读模型行内含结果摘要或 artifact 引用（或有明确的二次读取入口），父 agent 无需读取子会话完整事件流即可收敛任务。

---

## 1. 结论摘要（TL;DR）

1. **R1 部分具备、且是结构性缺口**：进度信息存在三个互不相通的形态——(a) 进程内 live-only 的 `subagent.progress` 镜像（仅 API 宿主接线、仅 SSE 消费者可见、不落库）；(b) durable 的 batch/task 控制面行（但 `SubagentTaskRecord.LastProgressAt` 没有生产写入者，P0-B 投影实际退化为 `UpdatedAt/StartedAt`）；(c) 终态才有的 mailbox 完成回执（durable，但只有终态）。子 agent 没有"主动上报进度"的事件契约（对比 team 侧 `report_task_outcome`）；`subagent.batch.progress / subagent.task.*` 未在 `events/contract.go` 注册，交付层判定为"无通道"。
2. **R2 已落地但默认关闭、且预算与关键告警同池**：opt-in 的 tick+wake 巡检（CLI/API 双宿主）、`supervision_descendants` 读模型、preflight digest 进度区块都已实现并有回归测试；但 `supervision.progressCheckInterval` 默认 0，progress wake 与 critical lifecycle 共用 `other` 类预算（默认 1h/5 次），异常密集时进度汇报会被静默跳过。
3. **R3 工具面齐全、语义文档缺口大于实现缺口**：`send_input / followup_task / send_message`（指令）、`close_agent / control_descendant / resume_agent`（生命周期）、`supervision_descendants / supervision_snapshot / read_agent_events / wait_agent`（观察）都存在，且 control 动作有 evaluator 校验与回执通知；缺口集中在"运行中指令的投递边界语义""投递确认（我方指令是否被消费）"与工具描述未向模型表达这些差异。
4. **R4 数据存在但父侧读模型没有出口**：`SubagentTaskRecord.ResultSummary/ArtifactRef`、`BatchSummary`、`agentresult.Result` 契约、`artifact.Store` 都已存在；但 `SnapshotItem`（`supervision/snapshot.go:72-104`）没有 result/artifact 字段，`supervision_descendants` 只回状态与 age，父 agent 若要看产物需要另找通道（mailbox 完成回执 / 直接读子会话事件），没有"任务→状态→结果→引用"的一次性查询面。

一句话：**监督面的"骨架"已经完成（通知、巡检、动作、预算、审计都有），当前主要矛盾是"进度的生产者缺失 + 进度不是 durable 事件 + 结果/产物没有父侧读出口"**，属于补链路而不是重架构。

---

## 2. R1 现状：子 agent → 主 agent 进度事件链路

### 2.1 事件产生侧：谁发什么、落在哪

**（a）`spawn_subagents`（batch 批量子代理）**

- 事件发射点（`backend/internal/agent/subagent_batch_coordinator.go`）：
  - `subagent.batch.started` `:1071`、`subagent.task.started` `:1184`、`subagent.batch.progress` `:1386/:1426/:1435/:1444`、`subagent.task.completed` `:1399`；
  - 契约测试固化：`subagent_batch_coordinator_test.go:159`、`:759`。
- 后台批次启动：`agent/loop.go:2531-2562`（`startBackgroundSubagentBatch`），返回体直接声明 `"parent_action": "continue_parent_turn; lifecycle_updates_will_be_delivered_by_supervision"`（`:2543`），并 emit `subagent.batch.created`（`:2553`）；前台同步路径在 `:2567` emit `subagent.batch.started`（`execution_mode=wait`）。
- **发射只到运行时总线、不落库**：CLI `cmd/aicli/commands/chat_actor_host.go:335-348`（`host.EventBus.Publish(...)`，无 `AppendEvent`）；API `internal/api/skills/supervision_batch_recovery.go:52-70`（`h.getRuntimeEventBus().Publish(...)`）。
- `events/contract.go:102-103` 把 `subagent.batch.started/completed` 标为 **TailOnly**（仅回合末尾巴，实时通道与事件库都没有）；而 `subagent.batch.progress`、`subagent.task.started`、`subagent.task.completed` **未在 contract.go:74-135 注册** → `ChannelsFor` 返回空集合，即连尾巴通道都没有。
- durable 部分不在事件总线，而在 batch 控制面（SQLite）：宿主持有 `SubagentBatches subagentbatch.BatchStore`（`chat_actor_host.go:147`），投影只读 `ListBatches/ListTasks`（`supervision/batch_progress.go:87-106`）。
- 批终态有 durable mailbox 投递：API `supervision_batch_recovery.go:72-121`（先把终态通知写成父会话 mailbox 消息，再通知 runtime bus；幂等键 `ParentSessionID+BatchID+DeliveryKey`）；消息构造 `toolbroker/agent_mailbox.go:154-192`（`BuildSubagentBatchTerminalMailboxMessage`）。

**（b）`spawn_agent`（单个子会话）**

- 生命周期事件 `agent/scheduler.go:417`（`subagent.started`）、`:454/:507`（`subagent.completed`），走 `emitRuntimeEvent` → 运行时总线；同样是 **TailOnly**（`contract.go:104-105`）。
- 父侧完成镜像 **durable**：API `api/skills/session_runtime_support.go:1036-1053` 把子完成镜像成 `subagent.completed` 并 `store.AppendEvent` 落父会话事件库 + `bus.Publish`；CLI `chat_actor_registry.go:1277` 构造 completion mailbox。
- **live-only 进度镜像**：API `session_runtime_support.go:971`（`NewSubagentProgressMirror`）→ `:988-991`，子会话 `toolprotocol.EventTypeProgress` 经 `progressMirror.Observe(...)` 后 `bus.Publish(mirrored)`，**没有 `AppendEvent`**。产出类型 `subagent.progress` 在契约中标记为 live-only（`events/contract.go:97-99`，注释"不落盘，刷新即丢"）。
- 镜像本体为**进程内内存结构**：`supervision/subagent_progress.go`（`Observe` `:86`、`Forget` `:219`、`Latest` `:242`、`Pending` `:266`、窗口剪枝 `:275`），无持久化实现；节流窗口 `DefaultSubagentProgressWindow`（`session_runtime_support.go:971`）。

**（c）`spawn_team`（teammate）**

- team 事件 **durable 落 team event store**：`team/terminal_state.go:332`（`team.completed`）、`:377`（`team.summary`）；task 级事件 `team.task.*` 见 `api/skills/team_handlers.go:2711/:2890/:3475/:3567` 等。
- 但 `team.*` 类型未进 `events/contract.go:74-135` 注册表 → chat 交付通道 `ChannelsFor` 为 0；父侧可见性走另一套通路（CLI 渲染 switch `cmd/aicli/commands/chat_runtime_events.go:7350/:7410/:7470`；mailbox 等待白名单 `session_runtime_support.go:3464-3473` 含 `team.completed/team.summary`）。
- teammate 有主动上报工具 `report_task_outcome` / `block_current_task`（执行在 `toolbroker/broker.go:2937-3000`），契约见 `docs/skill_runtime/team_task_outcome_contract.md`。

### 2.2 父侧读取原语（进度能否被读到）

- `read_agent_events`：路由 `toolbroker/broker.go:1968-2058`（`after_seq/limit/wait_ms/view`，无 id 时降级读自身 mailbox `:1997-2001`）；实现 `api/skills/session_runtime_support.go:2058-2162`（默认 limit 20 `:2085-2088`，`ListEvents(after_seq, limit+1)` 判 `has_more` `:2134-2144`，阻塞等待 wake channel + 500ms 轮询 `:2145-2161`，超时清空 next_action `:2152-2157`）。
- `view=tool_progress` 白名单（`toolbroker/types.go:831-875`）：保留 `tool.*` 前缀 + sticky 集合 `{session_end, agent.completed, agent.failed, agent.cancelled, agent.reclaimed, approval_requested, approval_resolved}`（`:840-848`、`:863-875`），注释明确"不丢终态"（`:836-839`）。
- 重复读抑制：`toolbroker/agent_events_read_memo.go:9-13`（128 条 FIFO）、`:18-24`（key = caller+target+view+after_seq+limit）、`:63-91`（同窗口 `latestSeq` 不变 → `unchanged=true` + `repeat_count`）、`:96-111`（close 后清理）；未读数探测 `session_runtime_support.go:3398-3410`。
- `wait_agent`：`broker.go:1896-1966`（`after_seq/timeout_ms/ids`；无目标时 mailbox-only `:1919-1921`）。
- **关键结论**：`subagent.progress` 是 live-only、不落库，而 `read_agent_events` 读的是 event store → **父 agent 的读取原语在协议上不可能读到进度镜像**；该镜像只面向 SSE/流式 UI 消费者。

### 2.3 进度聚合（P0-B 投影）与其真实数据质量

- 设计约束（`supervision/progress.go:10-29`）：不落新表、不改"成功不唤醒"、未接线时字节级不变；`ProgressTask`（`:42-51`）字段为 `TaskID/ChildSessionID/State/LastProgressAt/LastMessage`；预算 `maxProgressGroups=3`、`maxProgressTasksPerGroup=4`（`:87-94`）。
- durable 投影 `BatchProgressSource`（`batch_progress.go`）：两次有界读（活跃批次 + 10 分钟内终态后台批次，`:87-121`）；`progressGroup`（`:134-187`）逐任务展开非终态行；`MirrorProgressMessages`（`:221-243`）把 live-only 镜像接成 `ProgressMessageProvider`。
- **数据质量退化点**：
  - 批次时间戳 `batchProgressTime`（`:189-196`）优先 `batch.HeartbeatAt`（60s 级心跳）→ `UpdatedAt`；
  - 任务时间戳 `taskProgressTime`（`:205-219`）优先 `task.LastProgressAt`，但 **`SubagentTaskRecord.LastProgressAt` 在生产路径没有赋值者**：`subagentbatch/types.go:207-232` 只有字段定义；`subagentbatch/sqlite_store.go:361-366`（INSERT）与 `:713-736`（UPDATE）只做序列化；全包唯一赋值出现在测试。实现注释本身也承认这是 fallback 设计（"a running child that never reported still shows a sane age"）。
  - 因此父侧看到的 `progress_age_ms` 实际是"记录被更新/心跳"的年龄，不是子代理真实工作进度；粒度受 60s 心跳限制（与既有方案 §3.1 描述一致）。
- execution run 侧同样只在创建时写一次进度：`supervision/execution_supervisor.go:190-192` 初始化 `LastHeartbeatAt/LastProgressAt=now, ProgressSeq=0`；此后无进度推进来源（`batch_progress.go` 不读 run 表）。

### 2.4 R1 缺口清单

| # | 缺口 | 证据 | 影响 |
| --- | --- | --- | --- |
| R1-1 | 进度镜像 **live-only 且仅 API 宿主接线**：CLI 宿主无 mirror；SSE 断开即丢；`read_agent_events` 读不到 | `events/contract.go:97-99`；`session_runtime_support.go:971/:988-991`；全仓 `SubagentProgressMirror` 装配仅此一处 | 父 agent 拿不到"正在跑什么工具"的实时进度；CLI 宿主完全缺失 |
| R1-2 | `subagent.batch.progress / subagent.task.*` 未注册交付通道 | `events/contract.go:74-135` 无这三个类型 | 事件在 chat 交付层"无通道"，只有进程内订阅者可见 |
| R1-3 | 任务级 `LastProgressAt` 无生产写入者；批次级只有 60s 心跳 | `subagentbatch/sqlite_store.go:361-366/:713-736` 仅序列化；`batch_progress.go:189-219` fallback | 巡检/摘要里的"进度年龄"语义不准确，无法支撑"停滞判定" |
| R1-4 | 子 agent 无主动上报进度的工具/契约（team 有 `report_task_outcome`，subagent 无对应物） | `toolbroker/broker.go:33-63` 工具常量表；team 契约文档 | 子代理无法主动声明"里程碑/百分比/当前阻塞"；模型只能靠事件被动推断 |
| R1-5 | `supervision_descendants` 行不含进度消息（无 `last_message` / progress 字段），只有 age | `supervision/snapshot.go:72-104`（`SnapshotItem` 无 message 字段） | 父 agent 即使巡检也只能看到"陈旧度"，看不到"孩子在做什么" |
| R1-6 | 进度汇总只出现在父 turn 的 preflight digest 或 opt-in 巡查里；无"订阅式"推进 | `supervision/progress.go:20-24`；巡检链路见 §3 | 进度时效取决于父 turn 时机或 interval 配置 |

---

## 3. R2 现状：定时巡检子 agent 进度与状态

### 3.1 观察工具面（模型可用）

- `supervision_descendants`（`toolbroker/supervision_tools.go:52-66`）：参数 `mode(children|descendants)`、`health(any|abnormal|action_required)`、`include_terminal`、`limit`、`after_seq`；描述明确 abnormal 排序优先、`truncated=true` 不静默丢行（`:125-149`）；工具描述同时声明 anti-polling 豁免（`:119`）与 scope 边界"Only rows inside this session's scope are returned（模型不能指定 root scope）"（`:121`）。
- 双宿主控制器：
  - CLI `cmd/aicli/commands/chat_supervision_tools.go:28-37`（控制器 nil 时不注册工具）、`:42-58`（scope 解析：普通会话取自身，team lead 取 active team 且校验 lead）、`:103-130`（handler；`DefaultLimit` 用 `SnapshotMaxItems`）；
  - API `internal/api/skills/supervision_tool_controller.go:32-40/:44-56/:124-147`（同契约；team lead 解析 `leadTeamID()` `:62-88`）。
- 越权兜底：`internal/runtimeserver/supervision.go:180-262`（agent/run 必须在 root scope 的 agent graph 内；team/team_task 必须在 ancestors 内）。
- 读模型 `BuildSnapshot`（`supervision/snapshot.go:143+`）：装载 notifications（`:165-174`，按 root scope + target parent 过滤）、pending actions（`:190+`）；行字段 `SnapshotItem`（`:72-104`）含状态/age/run/deadline/auto_action/recommended_action/allowed_actions/notification_id，但**不含结果摘要或产物引用**（见 §5）。
- descendant 数据来源（`runtimeserver/supervision.go:264-300`）：agentcontrol registry（含 closed，跳过 `/root`）+ team edges + 可选 live 投影 hook（`ListAgentDescendants/ListTeamDescendants` `:28-31`）。CLI 装配只传 `AgentRegistry + TeamStore`（`chat_actor_host.go:931-938`）→ CLI 的行主要来自 registry/team store。

### 3.2 被动通道：父 turn 内的 preflight digest 进度区块（P0-B）

- 投影只读 batch 控制面与 live 镜像（`supervision/batch_progress.go:32-47`、`progress.go:10-29`），把"批次进行到哪"渲染进父 turn 的 preflight digest；预算 `maxProgressGroups=3`、`maxProgressTasksPerGroup=4`（`progress.go:87-94`），digest 总预算 `DigestMaxItems=20 / DigestMaxChars=4000`（`config.go:18-27/:67-69`）。
- 设计上明确：**不新增唤醒**——进度只在父 turn 内被动出现（`progress.go:20-24`）。
- CLI 装配：`chat_actor_host.go:2590-2595`（`Progress: supervision.NewBatchProgressSource(host.SubagentBatches)`）；API 装配：`api/skills/supervision_progress_check.go:63-73`（`wireSupervisionProgressSource`，幂等、可重复调用）。

### 3.3 主动通道：opt-in tick + wake 巡查（P2-D）

- 配置 `supervision.progressCheckInterval`（`supervision/config.go:48-53`）：默认 0 = 不注册任何 ticker；`WithDefaults()` 显式保持 0（`:110-113`），`ProgressCheckEnabled()` 只认正值（`:121-126`）。
- CLI 实现（`cmd/aicli/commands/chat_actor_progress_check.go`）：
  - 启动 `startLocalSupervisionProgressCheck`（`:36-58`）、ticker `runLocalSupervisionProgressCheck`（`:72-85`）；
  - 单次 gate 顺序（`:93-151`）：有 active batch → 父会话空闲（无 running turn / 无 pending wake）→ `AllowAutoWake`（预算类别判定）→ `ScheduleWake` → `wakeSupervisedParent`；
  - 接线 `wireLocalSupervisionProgressSource`（`:24-29`，调用点 `:105`），active 判定 `localSupervisionHasActiveProgress`（`:163-167`）。
- API 实现（`internal/api/skills/supervision_progress_check.go`）：`SetSupervisionConfig` 启动（`:76-96`）；多会话宿主按"batch store 中活跃批次所属父会话"为单位、单轮上限 16、按最近活动排序、**不创建 session actor**（`:24-29` 自述、`:76-96`）；每轮巡查前幂等接线进度源（`:183-185`，注释说明未接线会导致 wake 无内容不投递）。
- 预算与降级：progress wake 归入 `other` 类（`supervision_progress_check.go:31-36`），与 critical lifecycle 共用默认窗口 1h/5 次（`config.go:31-34/:71-74`）；预算耗尽时静默跳过、不留下过期 wake（`:245-250`）；`ErrWakeRateLimited` 语义为"wake 保持 durable，下个 runnable transition 重试"（`wake_scheduler.go:12-20`）。
- 运营记录：`docs/plan/supervision-operator-runbook.md`（§2 巡查 / §3 preflight digest / §4 收敛）。

### 3.4 R2 缺口清单

| # | 缺口 | 证据 | 影响 |
| --- | --- | --- | --- |
| R2-1 | 巡检**默认关闭**，主 agent 的常规体验里没有周期巡检 | `config.go:48-53/:110-113/:121-126` | 需求 R2 需要显式配置才成立；未配置时只能等父 turn 的 preflight |
| R2-2 | 巡查 wake 与 critical 生命周期告警**同池竞争** `other` 预算（1h/5 次） | `supervision_progress_check.go:31-36`；`config.go:31-34/:71-74`；`:245-250` | 异常密集时"进度汇报"被静默饿死；反之进度汇报也会占掉告警预算 |
| R2-3 | 进度内容依赖 live 镜像与心跳时间戳，语义不精确 | §2.3；`batch_progress.go:189-219` | 巡检可见的信息是"批次计数 + 陈旧度 + 可选工具名"，缺里程碑/阻塞原因 |
| R2-4 | 双宿主形态差异（API 多会话 ≤16、按最近活动；CLI 单根会话） | `supervision_progress_check.go:24-29` | 跨宿主行为不一致，文档/验收需要分别声明 |
| R2-5 | 巡检单位是"活跃 batch"，纯 `spawn_agent` 子会话（无 batch）不在巡查范围内 | 巡查只读 batch 控制面（`batch_progress.go:59-63/:87-106`） | 单子代理场景仍需父 turn 主动调用 `supervision_descendants` 或 `wait_agent` |
| R2-6 | 无停滞升级（stall escalation）：进度陈旧到阈值不会自行升级为告警/动作 | 巡检仅注入 digest（`:93-151`）；run 级 `progress_stalled` 由 execution 侧产生，二者未打通 | "孩子卡住了"要靠父 agent 每次巡检自行判断 |

---

## 4. R3 现状：主 agent 主动控制子 agent

### 4.1 工具族与模型可见契约

| 类别 | 工具 | 契约要点 | 证据 |
| --- | --- | --- | --- |
| 指令（排队） | `followup_task` | "If the child is busy, the message is delivered without interrupting the active run."；参数无 `interrupt` | `toolbroker/broker.go:380-391`（`:378` 起） |
| 指令（可打断） | `send_input` | 参数含 `interrupt`："Whether to interrupt an active child run before submitting the new prompt"；明确不面向 team teammate | `broker.go:392-405`（`:401`） |
| 指令（纯消息） | `send_message` | 模型侧描述"Queue a plain message … without interrupting or starting a new turn"；**但宿主分派与 `followup_task` 合并**，返回 `delivered` + `triggered` | `broker.go:1714-1766`（`:1764-1765`）；ToolsDefinition 精确行号未验证 |
| 生命周期 | `close_agent` | "Closing a parent path also closes its descendant child sessions"；会清理剩余 worktree | `broker.go:451-461`；`:488` |
| 生命周期 | `resume_agent` | 仅重建 in-memory actor | `broker.go:462-472` |
| 生命周期 | `control_descendant` | action 枚举 `cancel|close|cancel_subtree|retry|reassign`；"validated against the notification's server-computed allowed_actions" | `supervision_tools.go:192-223`（`:204`） |
| 产物 | `apply_agent_worktree` / `discard_agent_worktree` | "completion does not auto-apply"（父必须先 review） | `broker.go:473-496`（`:475`） |

### 4.2 投递路径与"运行中指令"的真实语义

- CLI（`chat_actor_registry.go`）：`SendMessage :2182`、`FollowupTask :2186`、`SendInput :2313`；共用 `deliverAgentMessage`（`:2190-2243`）：
  - 子 actor 不忙 → `SubmitPromptAsync` 提交为新 turn（`~:2213-2219`）→ **`send_message` 在 idle 子会话上也会起新 turn**，与其模型描述不一致；
  - 子 actor 忙 → 不中断，仅投递消息（busy 判定 `:2245-2263`），投递走 `runtimechat.DeliverMailboxEventFirst`（`chat_actor_host.go:588-596`，含 `Duplicate` 结果）。
- API（`api/skills/session_runtime_support.go`）：`SendMessage :1618`、`FollowupTask :1622`、`SendInput :1733`；`SendInput` 在 busy 且 `interrupt=false` 时**直接报错**（`:1755-1759`）；`interrupt=true` → `Stop` + `Wait`（上限约 5s，`:1760-1769`）后 `SubmitPrompt`（`:1772`）→ 语义是"打断并重开 turn"，**不是运行中安全注入（steer）**。
- mailbox envelope（`toolbroker/agent_mailbox.go:31-59`）：`trigger=false` → `agent_message`；`trigger=true` → `followup_task`；metadata 写 `from_session_id/target_session_id/trigger_turn`（`:48-50`）。
- **消费时机（关键缺口）**：`chat/actor.go:677-695`、`:1509-1515` 的 `handleDeliverMailboxMessage` 只发布 `mailbox_received` 会话事件；全仓检索 `trigger_turn` 仅在 `agent_mailbox.go:50` 写入 + 测试断言，**没有任何生产代码消费 `trigger_turn` 来驱动子会话启动新 turn**（本报告 §4 复核）。因此 busy 子会话收到的新指令何时被读取、是否会被读取，没有保证。
- 幂等：完成类消息 id = `subagentCompletionDeliveryKey`（sha256(parent, child, eventType, terminalIdentity)，`agent_mailbox.go:194-216`）；批量终态消息 `ID=deliveryKey`（`:154-192`）；批量路径记录 `mailbox_delivery_status/error`（`agent/subagent_batch_coordinator.go:344/:761-763/:935/:1614-1616`）；**单发 `send_message/followup_task` 的失败审计是否同等持久：未验证**。

### 4.3 关闭 / 取消 / 恢复路径

- `close_agent` CLI：`chat_actor_registry.go:2907-2964`——`StopAsync`（`:2933`）、持久化 `StateClosed`（`:2939`）、清理 spawn 隔离（`:2938` → `cleanupLocalSpawnIsolation :804-834`）、关闭 durable graph（`:2952-2962`）；API：`session_runtime_support.go:2331-2383` → handler `CloseAgentSessionByID`。
- `close_agent` **绕过 supervision 的 CAS/audit/allowed_actions 管线**（直连控制通道）；`control_descendant` 才走 evaluator：终态行（`resolution=closed`）只允许 `inspect`（`supervision/evaluator.go:36-41`），mutation 成功后写入回执通知并把源行 resolve（既有方案 §4.1.1，`action_service.go:441-511/:503-506`）。
- `control_descendant` 的 `retry|reassign` 在工具枚举里，但执行体未启用（`internal/runtimeserver/supervision_actions.go:28-38`）；正常会被 `local_control.go:324-334` 以不允许动作拒绝。
- `resume_agent`：`broker.go:462-472`；CLI `:3211-3228`；API `:2602-2616`。busy 期间 mailbox 中滞留的消息在 resume 后是否补投：**未验证**，与 §4.2 的黑洞叠加风险。
- 关闭与产物保留冲突：`close_agent` 会清理剩余 worktree（`broker.go:488`；CLI `:2938/:804-834`），而 `apply_agent_worktree` 要求父先 review（`broker.go:475`）；**没有守卫阻止"先 close 后想 apply"造成产物不可恢复**。

### 4.4 R3 缺口清单

| # | 缺口 | 证据 | 影响 |
| --- | --- | --- | --- |
| R3-1 | busy 子会话的指令**无消费保证**：`trigger_turn` 无生产消费方，投递只落 mailbox+事件 | `agent_mailbox.go:48-50`（全仓仅写入+测试）；`chat/actor.go:677-695/:1509-1515` | "根据进度发新指令"在子代理运行中可能静默丢失 |
| R3-2 | 无运行中安全注入（steer）：唯一确定路径是 `interrupt=true` 打断重开 | API `:1755-1779`；CLI `:2348` | 代价是丢弃子代理当前进度，投递新指令的决策成本高 |
| R3-3 | `send_message` 与 `followup_task` 共用宿主分支，idle 场景语义与描述不符 | `broker.go:1714-1766`；CLI `~:2213-2219` | 模型难以预测调用效果（"不起 turn" vs 实际起 turn） |
| R3-4 | `close_agent` 绕过监督审计管线，且会清理 worktree | `broker.go:451-461/:488`；CLI `:2938/:804-834` | 关闭动作不可从监督面审计；先关后 apply 导致产物丢失 |
| R3-5 | 无"进度阈值 → 自动动作"钩子；`progress_age_ms` 只是观察字段 | `supervision_tools.go:118-121`；`evaluator.go:66-91`（被动建议） | 需求"根据子 agent 进度主动控制"只能靠父模型每次自己巡检+决策 |
| R3-6 | 单发指令投递失败审计不完整（批量有，单发未验证） | `subagent_batch_coordinator.go:344/:761-763` 等 | 指令丢失后排障困难 |
| R3-7 | `control_descendant` 枚举与宿主能力不一致（retry/reassign 未启用） | `supervision_tools.go:204`；`supervision_actions.go:28-38` | 模型调用必失败，浪费 turn |

---

## 5. R4 现状：状态与产物查询

### 5.1 数据面（durable 侧已具备）

- 任务记录：`SubagentTaskRecord`（`subagentbatch/types.go:207-232`）含 `ChildSessionID/Role/Status/Attempt/FinishedAt/ResultSummary/ArtifactRef/ErrorClass/ErrorCode/Version`；
- 结果胶囊：`TaskResult`（`types.go:254-268`）：`Success/Summary/Findings/Patches/Error/UsageTotal/ArtifactRef`；
- 批次摘要：`BatchSummary`（`types.go:272-286`）：计数 + `CriticalErrors/TaskStatuses`；
- 统一结果契约：`agentresult.Result`（`agentresult/contract.go:11-110`，含 `status/summary/findings/changes/artifacts/evidence/remaining_work/errors/usage/trace_id/execution_event_refs`）+ 校验；agent 侧落点 `agent/result_contract.go:13-116`；
- artifact 存储：`internal/artifact`（Store/测试），子代理 worktree 产物经 `apply_agent_worktree` 落地。

### 5.2 父侧可见面（读出口）

- mailbox 完成回执（durable）：单子完成 `BuildSubagentCompletionMailboxMessage`（`agent_mailbox.go:64-149`，payload 带 status/success/error/event_seq 与 provider/model/usage 等）；批终态 `BuildSubagentBatchTerminalMailboxMessage`（`:154-192`）；父侧读取工具 `read_mailbox_digest` / `read_agent_events` / `wait_agent`。
- 监督读模型：`supervision_descendants` 行含状态/age/run/deadline/推荐动作（`snapshot.go:72-104`），**不含结果摘要、不含 artifact 引用、不含 last_message**。
- batch 结果读取入口分散：diff/文件列表/最终消息/错误分散在多个工具与通道（`broker.go:423/:436-450/:473-496`；`agent_mailbox.go:64-149`），**没有"按任务/会话查结果与产物"的统一读模型**。
- team 侧对照：teammate 有 `report_task_outcome` 主动提交结果（`broker.go:2937-3000` + `docs/skill_runtime/team_task_outcome_contract.md`，含 `result_ref`）；subagent 侧没有对等契约，结果依赖 host 侧从子会话终态自行提取。

### 5.3 R4 缺口清单

| # | 缺口 | 证据 | 影响 |
| --- | --- | --- | --- |
| R4-1 | `SnapshotItem` 无 result/artifact/progress-message 字段 | `snapshot.go:72-104` | 父 agent 巡检看不到"孩子产出了什么"，必须二次查询 |
| R4-2 | 终态行只允许 `inspect`，且没有配套的"终态产物读取"入口 | `evaluator.go:36-41`；`action_service.go:424/:433/:473` | 任务结束后父 agent 取产物的路径零散、不可预期 |
| R4-3 | 无统一"任务→状态→结果→artifact"读模型 | §5.2；C 报告 §4.3 | 父 agent 收敛成本高，容易漏读失败任务的 error_class/error_code |
| R4-4 | `close_agent` 清理 worktree，与"先 review 再 apply"的契约冲突且无守卫 | `broker.go:475/:488`；CLI `:2938/:804-834` | 关闭动作可能销毁未 apply 的产物 |
| R4-5 | subagent 与 team 的结果提交契约不对等（team 有 `report_task_outcome`，subagent 无） | `broker.go:33-63` 工具表；team 契约文档 | 两套协作模型的结果质量/可观测性不一致 |

---

## 6. 交叉问题与根因

### 6.1 交叉问题

1. **双宿主对等**：进度镜像仅 API 宿主有（§2.1b）；descendant live 投影 hook 在 CLI 未接线（`chat_actor_host.go:931-938` 只传 registry/team store）；巡查形态 API（多会话 ≤16）与 CLI（单根会话）不同；`supervision_*` 工具面到 2026-09-16 才在 API 宿主补齐控制器。凡是"事件→父侧"的增强都要同时在两个宿主落地并写门控测试。
2. **预算模型**：`other` 类预算 1h/5 次同时服务 lifecycle 告警与 progress 巡查（§3.3）；任何新增"进度事件唤醒"必须先从预算账本上分账，否则加剧饿死。
3. **终态收敛**：2026-09-16 已确认"run 终态不收敛 `progress_stalled`"为缺口并纳入 P1（既有方案 §4.3）；本轮又发现巡检依赖的 `LastProgressAt` 无生产者（§2.3），说明"进度语义"在 run/task/batch 三层都没有真实来源。
4. **审计与版本**：`close_agent` 直连通道绕过 CAS/audit（§4.3）；mutation 回执走 supervision 通知，但"发送指令"类动作不在该管线内。

### 6.2 根因归纳

- **根因 A（生产者缺失）**：进度的真实生产者不存在——子代理没有上报进度的工具/事件契约；任务级 `LastProgressAt` 无人写入；run 级进度创建即冻结。现有投影只是对"心跳/更新时间"的再解释。
- **根因 B（通道割裂）**：进度有三个互不相通的形态（live-only 镜像 / durable 控制面行 / 终态 mailbox），且 `subagent.batch.progress` 等类型未注册交付通道，父侧读取原语读不到进度镜像。
- **根因 C（读出口缺失）**：结果与产物在 durable 侧齐全，但监督读模型（snapshot/descendants）不携带结果/产物引用，也没有按任务的查询入口；父 agent 的"查询状态/产物"能力落在多个工具的组合上，而非一个读模型。
- **根因 D（控制语义未定义）**：busy 场景的指令投递没有消费保证（`trigger_turn` 无消费方），又没有安全 steer 通道，导致"根据进度发新指令"实际只有"打断重来"一种可靠形态。

---

## 7. 结论

现有监督架构（通知、预算、动作、审计、巡检、读模型）已经具备继续演进的全部基础件；本轮需求 R1–R4 的落地不需要新架构，需要的是**把"进度"变成有生产者的 durable 事实、把"结果/产物"接进监督读模型、把"控制指令"的投递语义显式化并补上消费保证**。具体实施路径、接口与验收标准见配套方案 `docs/plan/supervision-parent-child-control-optimization-plan-20260917.md`。
