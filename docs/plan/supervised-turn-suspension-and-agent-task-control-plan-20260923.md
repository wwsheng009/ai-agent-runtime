# 托管 Turn 挂起与主 Agent 任务巡检/管理实施方案

更新时间：2026-09-23
状态：proposed（仅方案，未修改任何代码）
实施进度：见执行方案 `docs/plan/supervised-turn-suspension-and-agent-task-control-implementation-plan-20260923.md` §13 实施注记（P0-前置–P3 已全部实施并验收闭环；本文件为冻结的设计稿 v5，不随实施过程更新）
适用仓库：`E:\projects\ai\ai-agent-runtime`
证据基线：2026-09-23 工作树只读走查（grep/view）+ 一次真实托管会话的 supervision 事件观察 + 参照实现走查（`E:\projects\ai\codex`，见 §13）
审查方法：只读代码走查（按符号名定位）+ 与 `docs/plan/` 既有 128 份方案做覆盖比对，凡已有方案覆盖的条目只做差异标注，不重开方案

修订记录：

- 2026-09-23：首版。建立"turn 挂起（suspension）+ obligation 账本 + escalate-first 决策上报 + 主 Agent 巡检/管理原语"设计；给出 8 条不变量、P0–P3 分期与 40 条边界场景分析。文中标注"建议新增"的字段/接口/动作/配置均属目标设计，不代表已实现。
- 2026-09-23（v2）：增补 §13 参照实现对照（Codex 子 Agent 管理面）、§14 方案完整性审查（A1–A6 阻断级 / B1–B6 建议级缺口 + 修订清单）、§15 追加边界场景 EC-H 12 条（总数 43 → 55）。**审查结论：主体成立，但未达可直接实施——A1–A6 为实施准入条件，见 §14.4。**
- 2026-09-23（v3）：固化控制流模型（§16）——等待为 turn 级状态而非 actor 阻塞；父 Agent 在等待期可自行干活/巡检/有界等待；收尾门 = 账本全终态（quorum=all，含异常终态）；`wait_agent` 定稿为**保留并改造**（Q11 拍板）；同步修订 §2 / §5.1 / §6.1 / §11 / §14.3。
- 2026-09-23（v4）：**准入条件全部落入正文**——§3.5/§3.6 基线修正（A3）与缺口 G9–G13；§4.2 新增 I9（耐久性前提）/I10（join 可判定性）；§6.1 步骤 5（resume 容量门控，A6）；§6.4 返回契约与 doom-loop 豁免（B1/B5）；§6.6 幂等键（B4）；§6.9 wait 工具族口径（A2）；新增 §6.10 结果取回（A4）/§6.11 所有权（A5）/§6.12 保留与 GC（B3）/§6.13 耐久性降级（A1）；§7 改动 #11–#16；§8 新增 P0-前置；§15 新增 EC-I 5 条（合计 60 条）。
- 2026-09-23（v5）：**建议级缺口收口**——新增 §6.14 审批路由与权限继承（B2）/ §6.15 steer 与 resume 交互（B6）；§7 改动 #17–#18；§8 P2 纳入并补验收；§15 新增 EC-J 4 条（合计 64 条）；§11 Q12 标注已落；§14.2/§14.3/§16.5 状态同步。**B1–B6 至此全部落正文。**

---

## 1. 文档定位

本文件回答一个问题：**主 Agent 派发子 Agent / Team 之后，如何在不阻塞进程的前提下，把"turn 覆盖子任务生命周期"这件事做实，并让主 Agent 有权巡检与判断（查状态、看输出、终止卡死任务、决定延长超时）？**

它针对的是当前执行语义里的三处结构性缺口：

1. `wait` 模式用**阻塞**换取"turn 覆盖子任务"，父 actor / goroutine / ctx 被全程占用，因此必须有硬编码 30m 熔断；
2. `background` 模式用**新 turn** 事后唤醒，turn 边界与子任务生命周期脱钩，"完成后自动总结"依赖只对 critical 生效的唤醒判据；
3. 超时与 stall 判定当前是 **runtime 强制执行**（`progress_stalled` → 直接 cancel + interrupt），主 Agent 只有事后知情权，没有**决策窗口**。

本文与既有方案的关系：

| 文档 | 关系 |
| --- | --- |
| `docs/plan/spawn-subagents-async-supervisor-plan.md` | background 批次原始设计（数据模型、生命周期、超时恢复）；本文件是其**执行语义层**的收敛 |
| `docs/plan/spawn-agent-team-supervision-timeout-recovery-plan.md` | P0–P6 已实施；提供 `execution/timeout.go` 的 `TimeoutSource`/`TimeoutBudget` 与 R0/R1/R2 风险分级，本文件沿用其风险分级 |
| `docs/plan/multi-agent-durable-lifecycle-hardening-plan-20260920.md` | H6–H10 持久记录完整性；本文件的账本设计直接复用其结论，不重复取证 |
| `docs/plan/multi-agent-execution-optimization-plan.md` | 主计划（N1–N11 / P0–P2）；本文件聚焦"turn 边界 + 决策权"，属增量 |
| `docs/plan/supervision-parent-child-control-optimization-plan-20260917.md` | `read_agent_result` 工具定义来源；本文件巡检原语在其之上做字段扩展 |
| `docs/plan/supervision-business-supervision-implementation-plan.md` | 业务监督（doc 6.x）语义来源；本文件不改其判据，只增加 decision-first 通道 |

---

## 2. 结论摘要（TL;DR）

1. **不废弃异步，废弃"阻塞实现"。** `execution_mode=wait` 的语义（turn 覆盖子任务）保留，实现改为**挂起**：派发后 turn **继续运行**（可巡检 / 自行干活 / 有界等待），当父无事可做或试图收尾且账本非空时才进入 `awaiting_obligations`（零 goroutine、零 token），由事件驱动恢复。
2. **turn 结束的判据从"模型说完了"改为"账本为空"。** 新增不变量 I1：账本非空 ⇒ 禁止进入终态。这是全部设计的地基。
3. **resume 复用同一个 `turn_id`**，不是新开 turn。当前的 wake consumer 起新 turn 正是"任务没完成就结束"的机制来源。
4. **超时从"runtime 强制"改为"escalate-first"**：软阈值（`ProgressDeadlineAt`）到期不再直接 cancel，而是投影 critical + action_required → 唤醒主 Agent 决策；只有**决策宽限期**过后才由 runtime 兜底执行。
5. **新增 `extend_deadline` 控制动作**（当前 `ActionKind` 8 个动作中没有），带次数/总量上限与审计；同时把 per-run 声明式 deadline 暴露到派发接口（底座已存在：`execution_supervisor.go` 的 `resolveDeadline(spec.ExecutionTimeout, …, cfg.AllowUnbounded, now)`）。
6. **巡检证据必须二维**：心跳（进程活着）与进度（任务在推进）分开呈现，否则无法区分"长工具调用"与"死循环/进程死"，会导致误杀。
7. **子任务生命周期必须与父 turn 的 ctx 解耦**，否则"延长超时"物理上不可能（ctx deadline 一经设定不可延）。
8. 43 条边界场景见 §9，其中 EC-B 组（超时/延长竞态）与 EC-E 组（turn 生命周期）是本方案的主要风险面。
9. **等待是 turn 级状态，不是 actor 阻塞**：派发后父 Agent 仍可自己干活（执行工具）、巡检（`subagent_status` / `subagent_inspect_task`）、有界等事件（改造后的 `wait_agent`）；受限的只是**收尾**——账本全终态（quorum=all）才允许进入终局综合。正常与异常（failed / canceled / timed_out / orphaned）一律计为终态，且每个 obligation 的终态由 deadline + watchdog 保证**可达**，join 不会永久挂起。定稿见 §16。

---

## 3. 现状基线（取证）

### 3.1 执行模式与阻塞语义

| 事实 | 锚点 |
| --- | --- |
| 工具 schema 暴露 `execution_mode: {"wait","background"}`，**wait 为默认**，"preserves the legacy synchronous semantics and returns full reports"；background "persists the batch durably and returns a batch handle immediately while a supervisor delivers lifecycle updates" | `backend/internal/agent/loop.go:6948-6952` |
| `wait_timeout_sec`：background 批次的**可选批次 deadline** | `backend/internal/agent/loop.go:6951-6952` |
| wait 路径在工具调用内联执行 `scheduler.RunChildren(ctx, SubagentRunOptions{...})`，父 turn 阻塞至子任务全部终态 | `backend/internal/agent/loop.go:2865-2896` |
| 唤醒轮投递使用 `context.WithTimeout(baseCtx, 30*time.Minute)`，**硬编码**；注释说明该上限用于防止后台 turn 把父 UI 卡成不可中断状态；失败时 `requeueSupervisionWake` | `backend/cmd/aicli/commands/chat_actor_host.go:806-842`（超时在 826 行） |
| 可运行判定：`Runnable` = `LoadState` 后 `!state.Summary().Busy()` | `backend/cmd/aicli/commands/chat_actor_host.go:794-805` |
| 交互式会话在宿主初始化期间**不得** drain（否则首个提示渲染前就起唤醒轮 → actor Busy → 输入框被抑制） | `backend/cmd/aicli/commands/chat_actor_host.go:850-857` |
| 子 Agent 派发深度上限校验（超限提示 `complete_locally_or_use_spawn_team`） | `backend/internal/agent/scheduler.go:430-436` |

结论：**"turn 覆盖子任务"目前只能靠阻塞实现**，而阻塞的代价被 30m 熔断兜住——这正是待改造点。

### 3.2 唤醒判据与 turn 边界

| 事实 | 锚点 |
| --- | --- |
| 自动唤醒是固定提示常量：`AutoWakePrompt = "[supervision] 存在待处理的子 Agent / Team 关键生命周期事件，请检查生命周期摘要（lifecycle digest）并继续。"` | `backend/internal/supervision/wake_consumer.go:12` |
| 无常驻轮询 goroutine；consumer 只在可运行状态跃迁点被调用 | `backend/internal/supervision/wake_consumer.go:14-19` |
| **只有 `SeverityCritical && ActionRequired()` 才排 wake** | `backend/internal/supervision/projection.go:87-103` |
| 成功完成"never demand a supervision decision"，仅保留为 resolved 记录 | `backend/internal/supervision/projection.go:107-110` |
| `ParentRunnable` 门控：父会话 running / waiting approval / compacting 时不得并发第二轮；`DrainRunnable` 是自动 turn 的唯一来源，且它自身从不启动 turn | `backend/internal/supervision/wake_scheduler.go:284-293` |
| 本地批次投影：干净完成 → `SeverityInfo` / `SupervisionTerminated` / `ResolutionClosed` / 推荐 `ActionClose`（**不排 wake**） | `backend/cmd/aicli/commands/chat_actor_host.go:512-521` |
| 终态投影后的 drain 只送走**已存在**的 wake，"without creating a second wake row" | `backend/cmd/aicli/commands/chat_actor_host.go:551-553` |
| 生命周期行的 `AllowedActions` 当前为 `{inspect, cancel, close}` | `backend/cmd/aicli/commands/chat_actor_host.go:539` |
| 非交互宿主在终态投影后额外 drain 一次，让 info/warning 与既有 pending wake 立刻起一轮汇报 | `backend/internal/api/skills/supervision_batch_projector.go:50-110` |
| 父 turn 启动前的 preflight 注入未决生命周期 digest（标记 delivered+seen，**不等于 acknowledged**） | `backend/internal/api/skills/supervision_handlers.go:314-317` |

结论：**唤醒判据过窄 + turn 边界与子任务脱钩**。成功完成不排 wake；失败反而必排 wake；且唤醒总是"新 turn"。

### 3.3 已有的 obligation 底座（关键：不必新建账本）

`supervision` 包已存在持久化的 **execution run 账本**，字段与本文设想的 obligation 高度重合：

| 字段 | 锚点 |
| --- | --- |
| `LastHeartbeatAt` / `LastProgressAt` / `ProgressSeq` | `backend/internal/supervision/execution_run.go:92-93` |
| `ExecutionDeadlineAt` / `ProgressDeadlineAt` / `ApprovalDeadlineAt` / `CancelDeadlineAt` | `backend/internal/supervision/execution_run.go:94-98` |
| `CancelSource` / `FinishedAt` / `Attempt` / `MaxAttempts` | `backend/internal/supervision/execution_run.go:99-101` |
| 上述字段全部落 SQLite（`execution_runs`），含 `OwnerLeaseUntil`、`FencingToken`、`Version` | `backend/internal/supervision/execution_store.go:113-119`、`152-158` |
| per-run deadline 由 spec 声明并回落到配置默认：`resolveDeadline(spec.ExecutionTimeout, cfg.DefaultExecutionTimeout, cfg.AllowUnbounded, now)` | `backend/internal/supervision/execution_supervisor.go:206-211` |
| 快照已把 deadline / heartbeat / progress / attempt 暴露给上层（含 toolbroker 类型） | `backend/internal/supervision/snapshot.go:105-113`、`451-459`；`backend/internal/toolbroker/types.go:492-499` |
| 已有 stall 告警：按 `ProgressDeadlineAt` 统计停滞 run 并挑选最久者 | `backend/internal/supervision/alerts.go:158-166` |

结论：**"obligation 账本"已存在**，本方案是给它补 `turn_id` 关联与 decision-first 语义，而不是新造一层。

### 3.4 已有的决策阶梯与"强制执行"（本方案的主要改动点）

`execution_supervisor.go` 已有完整决策阶梯，但**判定后直接强制执行**：

| 决策 | 触发 | 当前行为 |
| --- | --- | --- |
| `approval_timeout` | `ApprovalDeadlineAt` 到期 | 进入强制分支 |
| `execution_timed_out` | `ExecutionDeadlineAt` 到期 | 进入强制分支 |
| `progress_stalled` | `ProgressDeadlineAt` 到期（"no meaningful progress since progress deadline"） | 进入强制分支 |
| `orphan_suspected` | `OwnerLeaseUntil` 过期 | **仅观察**（P3：`ActionTaken = "none_observe"`，只投影） |
| 强制分支 | `if s.enforce()` | `RequestExecutionCancel` → `InterruptRun`，`ActionTaken = "cancel_requested" / "interrupted"` |
| 非强制分支 | `else` | `ActionTaken = "none_observe"`，仅投影 |

锚点：`backend/internal/supervision/execution_supervisor.go:496-524`（阶梯）、`526-553`（强制分支）、`590+`（`projectDecision` 写生命周期 inbox，`progress_stalled`/`execution_timed_out` 为 `SeverityCritical`）。
取消宽限到期后由 `fenceOrphaned` 提升 fencing token 并置 `orphaned`，阻断晚到写入（`execution_supervisor.go:559-588`；`473-479` 为 cancel grace 判定）。

结论：**软阈值（progress_stalled）当前是"杀掉再说"，不是"问一句再杀"。** 这正是"主 Agent 判断与管理"缺位的根因。

补充取证（2026-09-23 复核）：`projectDecision` 的推荐动作文案是 `"inspect run and decide cancel/retry"`（`execution_supervisor.go:619`）——**投影文案面向"决策"，但强制分支已经先把 run 取消/中断了**，语义自相矛盾。且该投影未显式传 `AllowedActions` / `ResolutionState`，由 `ProjectLifecycle` 补齐默认（`blocked` / `unresolved`，`projection.go:56-61`），因此按 `projection.go:87-103` 会排 wake：**主 Agent 收到的是"已经杀完了"的事后通知**，而不是决策请求。这是本方案改动 #1 的直接依据。

### 3.5 控制动作集与巡检能力

| 能力 | 现状 | 锚点 |
| --- | --- | --- |
| 控制动作枚举 | `inspect / acknowledge / defer / cancel / close / cancel_subtree / retry / reassign`（**无 extend**） | `backend/internal/supervision/types.go:197-202` |
| 变更类动作判定 | `isMutationAction` = `cancel/close/cancel_subtree/retry/reassign` | `backend/internal/supervision/action_service.go:513-519` |
| 动作校验 | 变更类动作必须带 `reason`；未知动作返回 `ErrActionInvalid` | `backend/internal/supervision/action_service.go:533-541` |
| 本地控制入口 | 同样白名单校验（无 extend） | `backend/internal/supervision/local_control.go:244-252` |
| 巡检数据面 | `snapshot` 已含 deadline/heartbeat/progress/attempt；`chat_debug` 已渲染 `exec_deadline/progress_deadline/cancel_deadline/heartbeat` | `snapshot.go:105-113`；`backend/cmd/aicli/commands/chat_debug.go:2116-2124`、`2341-2350` |
| 周期性进度巡查（P2-D） | 宿主级、opt-in、有界（`apiSupervisionProgressCheckParentLimit=16`、`BatchLimit=64`）；**不创建 session actor** | `backend/internal/api/skills/supervision_progress_check.go` |
| 超时来源语义 | `TimeoutSource`：`tool_argument / tool_default / chat_turn_deadline / agent_run_deadline / parent_context_deadline / sandbox_policy / runtime_ceiling`；`TimeoutBudget{Requested, Effective, Source}`；`WithTimeoutSource` 在父 deadline 更短时保留父来源 | `backend/internal/execution/timeout.go:13-25`、`37-41`、`59-67` |
| 派发接口的 timeout 声明面（A3 基线修正） | `spawn_agent` **已有** 4 个 per-agent 超时参数：`timeout_sec` / `progress_timeout_sec` / `approval_timeout_sec` / `cancel_grace_sec`；`spawn_subagents`（batch/team）**仅有批次级** `wait_timeout_sec`，无 per-task deadline | `backend/internal/toolbroker/broker.go:1617-1640`、白名单 `:3235-3243`；`backend/internal/agent/loop.go:6948-6960` |
| 等待/巡检工具族 | `wait_agent`（`ToolWaitAgent`）参数 `{after_seq,id,ids,session_id,session_ids,timeout_ms}`；`wait_team` / `read_agent_events` / `list_agents` / `task_output` 同属"重复调用敏感"集合 | `backend/internal/toolbroker/broker.go:45`；`backend/internal/agent/doom_loop.go:178-184` |

结论：巡检**数据面齐备**、控制**动作面缺 extend**、决策**入口缺"先问再做"**；派发面的缺口不在"没有超时参数"，而在**粒度只到 agent、未到 task/obligation，且运行中不可改**（A3）。

### 3.6 缺口清单

| ID | 缺口 | 影响 |
| --- | --- | --- |
| G1 | turn 终态判据与子任务生命周期脱钩（wait 靠阻塞、background 靠新 turn） | "任务未完成就结束"，或 30m 熔断截断 |
| G2 | 唤醒判据仅 `critical && action_required` | 成功完成静默；"完成后自动总结"不兑现 |
| G3 | 软阈值到期直接 cancel + interrupt，无决策窗口 | 长任务/长工具调用被误杀；主 Agent 只有事后知情 |
| G4 | 无 `extend_deadline` 动作，运行中不可延长 | "确实需要很长时间"无法表达，只能改配置重启 |
| G5 | 主 Agent 无结构化巡检原语（状态+输出尾部+stall 证据） | 只能靠读 transcript，成本高、判据弱 |
| G6 | run 无 `turn_id` 关联 | resume 无法复用同一 turn；挂起恢复无锚点 |
| G7 | 子任务 deadline 与父 ctx 耦合（wait 模式显式内联） | ctx deadline 不可延；重启即失 |
| G8 | 进度埋点可关闭（`TaskProgressInterval=0` 不写） | stall 判定失真，可能误判健康任务 |
| G9 | 结果取回链路缺失（H1–H5）：恢复后拿不到子任务交付物 | 终局报告降级为"凭记忆总结"，托管价值打折（对应 §14.2 A4） |
| G10 | 巡检/延长/取消/resume 的**授权边界**未定义（`OwnerID` / `OwnerLeaseUntil` 已存在但未定义谁有权） | 多宿主/多进程双写冲突；任意方均可变更他人 run（A5） |
| G11 | 挂起态依赖 durable store，但默认 coordinator store 是**进程内**、重启失忆（nil 时禁用 background 并回退同步） | 挂起态/账本重启即丢，I1 无法自愈（A1，实施准入项） |
| G12 | resume 未过容量门控（`MaxConcurrent=4` / `MaxDepth=1(+1)` / 可见性门控） | 恢复即超限，或永久排队变相死锁（A6，实施准入项） |
| G13 | 巡检/等待工具属 doom-loop 敏感集合（`wait_agent` / `read_agent_events` / `list_agents` / `task_output` / `background_task`） | 正常巡检被误判死循环 → 巡检能力实际不可用（B1） |

### 3.7 现场案例（2026-09-23，工作树观察）

一次真实托管会话中观察到如下事件序列，构成本方案的直接动因：

1. 唤醒轮投递并持续运行，直至触达 **30m 硬编码上限被截断**，**截断时子批次仍在运行**；
2. 子批次随后失败，投影出 critical 生命周期通知 → 排入 wake；
3. wake 到期边界投递失败（`context deadline exceeded`）→ `requeueSupervisionWake`；
4. 结果是：**父会话 idle、子任务未完成、pending wake 挂在账本中**，三件事同时发生。

该序列分别对应缺口 G1（turn 边界）、G3（无决策窗口导致长任务被截断）与 G2（成功路径静默，失败路径才唤醒）。

---

## 4. 目标语义与不变量

### 4.1 目标语义

> **turn 的生命周期覆盖其派生的全部子任务；turn 的执行不占用进程；子任务的判断权归主 Agent，兜底权归 runtime。**

三条推论：

- **保留异步**：子任务独立于父 turn 的 ctx 与 goroutine 运行，可持久、可恢复；
- **废弃阻塞**：`wait` 不再是"阻塞调用"，其语义由"挂起 + 同一 turn resume"承接；
- **决策前置**：软阈值触发"问"，硬阈值触发"杀"，两者之间有明确的决策宽限期。

### 4.2 不变量

| ID | 不变量 | 违反时的行为 |
| --- | --- | --- |
| I1 | **存在非终态 obligation ⇒ 父 turn 不得进入终态**（收尾被拦截，自动转入挂起；受限的是收尾，不是执行） | 拦截收尾，自动转入挂起 |
| I2 | 每个 obligation 必有 `deadline_at`（声明或默认） | 派发即拒绝；账本因此必然可清空 |
| I3 | resume 复用同一 `turn_id`，不新开 turn | 断言失败即降级为告警并新开 turn（保底不丢事件） |
| I4 | 变更类动作必须带 `reason` 且写审计 | 复用 `action_service` 现有校验 |
| I5 | 延长有界：单次 ≤ 1× 原始预算，单 obligation ≤ 3 次，总量 ≤ 4× 原始（可配） | 超限拒绝并回执 |
| I6 | 不可逆点之后禁止延长：已 `cancel_requested` / `canceling` / terminal 的 run 不可延长 | 返回 `ErrActionInvalid` 类错误 |
| I7 | 巡检输出字节有界，超限走 artifact 归档 | digest 只带预览 + `artifact_id` |
| I8 | 决策宽限期以**可运行时钟**计量（父会话可被唤醒的时间），非纯墙钟 | 见 EC-A3 |
| I9 | **耐久性前提**：无 durable store（进程内 store / nil）⇒ **禁止进入挂起** | 派发时降级为 legacy 同步路径 + 投影 `SeverityWarning`；不得静默挂起（A1） |
| I10 | **join 可判定性**：每个 obligation 的终态必须在有限时间内可达（`deadline_at` + watchdog 兜底） | 缺 `deadline_at` 的派发被拒绝（I2）；watchdog 扫描到"无终态且 deadline 已过/丢失"的 run ⇒ 强制判终态 + 投影 critical，不得静默（§16.4） |

---

## 5. 设计总览

### 5.1 状态机

```
turn.running ──派发子任务──▶ turn.awaiting_obligations  (挂起：零 goroutine / 零 token)
      ▲                              │
      │        ┌─────────────────────┼─────────────────────┐
      │   进度事件(合并限流)     终态事件(必投递)        用户中断/ESC
      │        ▼                     ▼                     ▼
      └── turn.running ◀── resume ──┴──────────▶ turn.canceled → 级联取消账本
                  │
        账本为空 ─┴─▶ turn.running(终局综合) ─▶ turn.completed
```

> 补充（定稿见 §16）：`turn.running` 期间父 Agent **不是干等**——它可以继续执行自己的工具调用、巡检子 Agent、或以有界 `wait_agent` 等事件；挂起是"父无事可做或试图收尾且账本非空"时才进入的**资源态**（零 goroutine / 零 token），不是派发后的强制步骤。收尾门统一为 I1：账本全终态。

### 5.2 组件职责

| 组件 | 职责 | 现有对应 |
| --- | --- | --- |
| Obligation Ledger | 记录 turn 派生的全部子任务及其状态 | `execution_runs`（补 `turn_id` 关联） |
| Suspension Controller | 拦截收尾、进入挂起、持久化挂起态 | 建议新增（`supervision` 或 host 层） |
| Resume Dispatcher | 按 terminal / progress / deadline 三类触发器恢复**同一 turn** | `wake_scheduler` + `wake_consumer`（升级） |
| Progress Coalescer | 合并进度事件，抑制 resume 风暴 | 参考 `supervision_progress_check.go`（宿主级 → obligation 级） |
| Escalation Policy | 软/硬阈值 + 决策宽限期 + 兜底执行 | `execution_supervisor` 决策阶梯（改为 escalate-first） |
| Inspection API | 状态 / 证据 / 输出尾部（只读、有界） | `snapshot` 数据面（补工具层） |
| Control API | inspect / extend / cancel / steer / retry / reassign | `action_service`（补 `extend`） |

---

## 6. 详细设计

### 6.1 turn 挂起与同一 turn resume

**挂起动作（派发后立即执行）**：

1. 落账本：为每个子任务/批次写 obligation（含 `turn_id`、`deadline_at`、`check_in_after`）；
2. 发出可观测事件（复用现有 timeline 事件族，如 `subagent.batch.started`）；
3. 父 turn **继续运行**（可自行干活 / 巡检 / 有界等待，见 §16.2）；当它无事可做或试图收尾时返回"挂起信号"：**不写终态**、**不发 `turn.finished`**；
4. 宿主持久化挂起态：`{turn_id, session_id, obligation_ids, parked_at, decision_window_until}`。

**resume 动作**：

1. Resume Dispatcher 从 pending resume 队列认领一条（复用 `DrainRunnable` 唯一来源语义，`wake_scheduler.go:284-293`）；
2. `ParentRunnable` 门控照旧（running / waiting approval / compacting 时不并发第二轮）；
3. 以**同一 `turn_id`** 注入 resume 上下文（rollup digest + 可用动作清单），父 turn 继续；
4. 若父 turn 再次派发 → 回到挂起；若账本为空 → 进入终局综合 → `turn.completed`。
5. **resume 前置容量门控（A6，实施准入项）**：resume 视为"起 turn"，必须先过并发/深度/可见性门控（`MaxConcurrent=4` / `MaxDepth=1(+1 hard/expert)` / `shouldExposeSpawnSubagents`）；超限时有界 FIFO 排队并让 digest 显示排队位次，排队超时走 escalate。**挂起态本身不占额度**，避免"恢复即超限"或变相死锁（见 EC-H1）。

**关键差异（相对现状）**：

- 现状 wake 是"起一轮新 turn"，语义上等于"上一轮已经结束"；本设计里 turn 从未结束，唤醒只是**同轮续跑**；
- 因此 30m 硬编码上限（`chat_actor_host.go:826`）降级为**单次 resume episode 的熔断**，不再约束整个托管 turn——挂起态不吃墙钟预算。

### 6.2 Obligation Ledger 与 turn 关联

复用 `execution_runs`，新增/明确以下语义（建议新增字段标注）：

| 字段 | 说明 | 现状 |
| --- | --- | --- |
| `TurnID`（建议新增） | 派发该 run 的父 turn | 无（G6） |
| `ParentSessionID` / `ParentRunID` | 已有 | `execution_store.go:113` |
| `ExecutionDeadlineAt` | 硬阈值 | 已有 |
| `ProgressDeadlineAt` | **软阈值**（stall 判定） | 已有，但语义改为"决策触发点" |
| `ApprovalDeadlineAt` | 等待审批/输入的超时 | 已有 |
| `LastHeartbeatAt` / `LastProgressAt` / `ProgressSeq` | 巡检证据 | 已有 |
| `ExtensionCount` / `ExtendedTotal`（建议新增） | 延长审计与上限 | 无（G4） |
| `DeclaredBudget`（建议新增或复用 spec） | 派发时声明的预算 | 部分（`resolveDeadline(spec.*)`） |

账本同时是 I1 的判据来源：`count(obligations where turn_id=? and status not terminal) == 0` 才允许收尾。

### 6.3 escalate-first：软/硬双阈值 + 决策宽限期

把 `execution_supervisor` 的阶梯从"判定 → 执行"改为"判定 → 上报 → 决策 → 兜底"：

| 阶段 | 触发 | 行为 |
| --- | --- | --- |
| 静默 | `progress_age < 软阈值` | 不打扰（仅在合并 digest 中体现） |
| 搭车 | `软阈值 ≤ progress_age < 2×软阈值` | 搭下一次 progress resume 顺带告知 |
| **上报（新）** | `progress_age ≥ 2×软阈值` 或 heartbeat 失效 | 投影 `SeverityCritical + action_required` → **唤醒主 Agent 决策**；`ActionTaken = "escalated"`；**不 cancel** |
| 决策 | 主 Agent 收到 digest | 可选 `inspect / extend_deadline / cancel / steer / retry / reassign` |
| 兜底 | 决策宽限期到期且无决策 | 执行原强制分支（`RequestExecutionCancel` → `InterruptRun`），`CancelSource = "decision_window_expired"` |
| 硬阈值 | `ExecutionDeadlineAt` 到期 | 直接执行强制分支（硬阈值不接受决策延迟），但仍投影通知 |

要点：

- **软阈值不再直接杀**：这是 G3 的修复；`progress_stalled` 的 `ActionTaken` 由 `cancel_requested` 改为 `escalated`；
- **硬阈值语义不变**：`ExecutionDeadlineAt` 是"承诺上限"，到期即执行，避免"等模型决策"变成永久挂起；
- 上报路径复用现有通道（`SeverityCritical && ActionRequired()` → 排 wake，`projection.go:87-103`），**无需新机制**；
- `orphan_suspected` 维持"仅观察"（`execution_supervisor.go:512-521`），待 P4 回收工作落地后再接入决策。

### 6.4 巡检原语（主 Agent 主动查）

建议新增两个只读工具（复用 `snapshot` 数据面与 artifact 归档，不新建存储）：

```
subagent_status         # 账本总览：每个 obligation 一行，含 stall 证据与可用动作
subagent_inspect_task   # 深看单个：最近事件、输出尾部、当前工具调用、产物引用（字节有界）
```

`subagent_status` 每行至少返回：

```
obligation_id / subject_kind(batch|agent_run|team_task) / subject_id
state / attempt / max_attempts
started_at / deadline_at / declared_budget / extension_count
last_heartbeat_at / heartbeat_age / last_progress_at / progress_age / progress_seq
current_tool / completed_steps / artifact_count
stall_verdict(healthy|slow|suspect_stuck|suspect_dead) / allowed_actions[]
```

**二维 stall 判据（核心）**：

| 心跳 | 进度 | 判定 | 建议动作 |
| --- | --- | --- | --- |
| 新鲜 | 在推进 | `healthy` | 继续等 |
| 新鲜 | 停滞 | `suspect_stuck`（长工具调用 / 死循环） | inspect → steer 或 cancel |
| 失效 | 停滞 | `suspect_dead`（进程死 / orphan） | cancel 或 reassign |
| — | 有产物产出 | `slow`（长任务正常） | extend |

`subagent_inspect_task` 的输出尾部走 artifact：digest 只带预览 + `artifact_id`，需要更多时由主 Agent 再次读取（对应 I7；仓库已有工具结果归档机制）。

**返回契约（B5 统一）**：巡检与等待工具一律返回"成功观测 + `next_action`"，而不是裸错误：

```
{ obligations|obligation, terminal_delta[], pending_count, terminal_count,
  stall_verdict, allowed_actions[], next_action, digest_ref|artifact_id }
```

- `next_action ∈ {continue_wait, inspect, extend_deadline, cancel, finalize, suspend}`；
- 空账本调用巡检/等待 ⇒ 立即返回 `next_action=finalize`（不空等）；
- 观测类失败（工具超时、字节超限）不得表现为"任务失败"，只降级为带 `next_action` 的观测结果。

**doom-loop 豁免（B1，必需项）**：巡检/等待工具在 `doom_loop.go:178-184` 的敏感集合内，必须改为按"**无进展的重复**"判罚（参数与读数均未变化）而非"多次调用"：

- `wait_agent` / `subagent_status` / `read_agent_events` 在账本状态或 `progress_seq` 发生变化时**一律不计数**；
- 连续 N 次（建议 5）观测到**完全相同的读数**才计入空转，且只触发 `next_action=suspend` 建议，不直接判罚；
- 否则 active wait 会高频调用 `wait_agent`，巡检能力上线即被误判（对应 §16.2 的 active wait）。

### 6.5 管理原语（主 Agent 主动管）

| 动作 | 现状 | 本方案 |
| --- | --- | --- |
| `inspect` | 已有（`types.go:197`） | 复用，配合 6.4 的字段扩展 |
| `cancel` / `cancel_subtree` / `close` | 已有 | 复用；级联语义见 EC-C2 |
| `retry` / `reassign` | 已有 | 复用；用于 `suspect_dead` 场景 |
| **`extend_deadline`** | **无** | **建议新增** |
| `steer`（向子会话发指令） | 工具层已有等价能力（send_input 族） | 复用，纳入允许动作清单 |

**`extend_deadline` 规格（建议）**：

```
ActionKind: "extend_deadline"
Request: { target_kind, target_id, extend_by | new_deadline, extend_which: execution|progress|both, reason }
```

- 变更类动作 → 必须带 `reason`（复用 `action_service.go:513-541` 校验与审计路径）；
- 更新 `ExecutionDeadlineAt` / `ProgressDeadlineAt` 必须走 **CAS + Version**（`UpdateExecutionRunCAS`），避免与扫描器竞态（见 EC-B1）；
- 每次延长写生命周期事件（digest 与 UI 可见"已延长 ×N, +时长"），并递增 `ExtensionCount`；
- 上限：I5；超限返回明确错误与 `next_action` 提示；
- 不可逆点：I6（`cancel_requested` / `canceling` / terminal 一律拒绝）。

**事前声明优于事后延长**：把 `wait_timeout_sec`（`loop.go:6951-6952`）泛化为 per-obligation 声明式 `deadline`，让主 Agent 在派发时按任务性质声明预算（快任务 5m、长任务 2h）。延长是例外路径。

**表述修正（A3）**：§6.5 的缺口不是"派发接口没有超时参数"——`spawn_agent` 已有 4 个（§3.5 基线）。真正的缺口是**粒度与时机**：

- 粒度：`spawn_subagents`（batch/team）只有批次级 `wait_timeout_sec`，**无 per-task deadline** → 本方案把声明式 `deadline` 扩展到 batch/team 的每个 task；
- 时机：已有参数只在**派发时**生效，运行中不可改 → 本方案新增 `extend_deadline` 覆盖"运行中延长"；
- 口径统一：`timeout_sec`（agent 级）/ `wait_timeout_sec`（batch 级）/ 新 `deadline`（obligation 级）三者归一为同一字段族，避免三套语义并存。

### 6.6 进度合并与 resume 预算

- 进度事件先由 runtime 消费（零 token）：更新 `LastProgressAt` / `ProgressSeq` / 账本计数；
- 满足 `min_interval`（建议 30s–2min）或状态跃迁（完成数变化、失败数增加）才触发一次 resume；
- 复用既有预算族：`WakeMaxAutoWake`（默认 5）、`WakeMaxProgressWake`（默认 6）、`WakeRateWindow`（1h）；**terminal 事件不受 progress 预算限制**（否则账本永远挂着，见 EC-A4）；
- digest 上限沿用 `DigestMaxItems=20` / `DigestMaxChars=4000`（`supervision/config.go:106-107`）。

**通知载体的幂等键（B4）**：resume 通知 / digest 必须携带稳定幂等键，防止重复投递造成重复 resume 或重复汇报：

```
notify_key = hash(turn_id, obligation_id, event_kind, terminal_epoch|progress_seq)
```

- terminal 类：`terminal_epoch` 取该 obligation 最后一次终态跃迁的序号（重试/重派后递增），同一终态只投递一次；
- progress 类：以 `progress_seq` 为幂等键，同一 seq 不重复唤醒；
- 投递失败重试（`requeueSupervisionWake`）**复用同一 notify_key**，消费方按 key 去重（当前载体是提示常量 `AutoWakePrompt`，`wake_consumer.go:12`，需升级为携带 key 的结构化载体，见 §13.3 对 Codex `<subagent_notification>` 载体的对照）。

### 6.7 子任务生命周期与父 ctx 解耦（可延长的前提）

现状 `wait` 在工具调用内联执行（`loop.go:2865-2896`），子任务 ctx 派生自父 turn；ctx deadline **一经设定不可延长**，且挂起态父 turn 没有存活 ctx。因此：

1. obligation 的 deadline 由 runtime 独立持有并持久化（`execution_runs` 已持久）；
2. 父 turn 的 ctx 仅在单次 resume episode 内有效，与子任务生死无关；
3. 子任务内部工具超时（`TimeoutSource` 族，`execution/timeout.go:13-25`）与 run 级 deadline **语义分离**：延长只作用于 run 级；已被父 ctx 固定的内部超时不可延（见 EC-B6）。

### 6.8 可观测与 UI

| 事件 | 内容 |
| --- | --- |
| `turn.suspended` | `turn_id`、obligation 数、`parked_at`、预计下一次巡检时间 |
| `obligation.escalated` | 触发阈值、stall 证据、允许动作清单 |
| `obligation.deadline.extended` | 延长前后 deadline、`reason`、`extension_count` |
| `obligation.canceled` | 发起方（`agent` / `runtime_fallback` / `user`）、`cancel_source` |
| `turn.resumed` | resume 触发类型（terminal / progress / check-in）、digest 摘要 |

UI 要求：挂起期间明确显示"托管中：N 个任务运行中（M 完成 / K 异常）"，且主 Agent 的每次管理动作（尤其 cancel 与 extend）必须可见可审计。

### 6.9 `wait` 语义迁移与 wait 工具族口径

| 阶段 | 措施 |
| --- | --- |
| 兼容期 | 保留 `execution_mode=wait` 取值，**语义映射为 async + park**（调用方仍得到"turn 覆盖子任务"的行为，但不再阻塞） |
| 收敛期 | 工具描述标注 deprecated；新调用默认单一异步语义 |
| 移除期 | 删除阻塞分支（`loop.go:2865-2896`）与 `wait` 取值；`wait_timeout_sec` 泛化为 `deadline` |

**破坏性变更提示**：依赖"wait 在工具返回里内联给出完整报告"的调用方（脚本/API 客户端）必须改造为"挂起 + resume 后取报告"；过渡期建议双写（既返回 batch handle，也在 resume 后回传聚合报告）。

**工具族口径（Q11 已拍板，定稿见 §16.3）**：`wait_agent` / `wait_team` **保留并改造**，不作 deprecated。上表三阶段只针对 `execution_mode=wait`（内联阻塞实现）；工具族的改造口径是四条约束（区间钳制 / 活动驱动 / 可被打断 / 超时即观测）+ 统一返回契约（§6.4），二者不可混淆：

- 废弃的是**无界阻塞的实现**（`loop.go:2865-2896` 内联路径）；
- 保留的是**"等待 + 巡检"这一语义**（主 Agent 边等边干的入口，§16.2 active wait）；
- 旧参数 `{after_seq,id,ids,session_id,session_ids,timeout_ms}` 保留读取，`timeout_ms` 语义按 §16.3 改造（min 10s / default 30s / max 1h）。

### 6.10 结果取回与终局报告（A4 / H1–H5）

**问题**：挂起/resume 解决了"turn 不结束"，但没解决"恢复后拿什么"。若交付物只存在于子任务的 transcript 里，终局报告会退化为"凭记忆总结"。

**契约（对齐 §13.5 的 Codex `agent_jobs` 形状）**：

```
{
  job_id, status,                      // completed | completed_with_failures | failed | canceled | ...
  output_ref | artifact_id,            // 大产物落文件/artifact，回执只带引用
  total, completed, failed,
  failed_items: [{ id, error_class, retryable, last_output_tail_ref }],
  result_digest                        // 有界摘要（受 DigestMaxChars 约束）
}
```

**五项要求**：

| # | 要求 | 落地要点 |
| --- | --- | --- |
| H1 | 交付物必须**落 artifact**，不依赖会话上下文存活 | 复用仓库已有工具结果归档机制；`execution_runs` 建议新增 `ArtifactRefs` / `ResultSummary` 字段 |
| H2 | 回执**只带计数 + 失败摘要 + 引用** | 禁止把全文塞进 digest（与 I7 一致） |
| H3 | 失败项带**错误分类 + 可重试标记** | 供主 Agent 直接决策 `retry` / `reassign` / `abandon` |
| H4 | 终局报告由 **runtime 组装 rollup**，模型只做"综合与决策" | resume digest 自带 rollup；不要求模型回溯历史（与 EC-E4 一致） |
| H5 | 取回**幂等** | 同一 turn 多次 resume 结果一致；按 `terminal_epoch` 去重（§6.6 幂等键） |

**终局综合的输入**：`pending_count == 0` 时，runtime 组装"全终态 rollup"（含 `completed_with_failures` 的失败项清单）注入父 turn，父 Agent 据此产出终局报告——这是"只有所有 Agent 都结束才能继续执行"的实际落点（§16.4）。

### 6.11 所有权与授权边界（A5）

**已有底座**：`OwnerID` / `OwnerLeaseUntil`（`execution_run.go:88-89`，落库 `execution_store.go:113`、`152`、`517`）；`orphan_suspected` 由 `OwnerLeaseUntil` 过期判定，当前仅观察（`execution_supervisor.go:512-518`）；`fenceOrphaned` 提升 fencing token 阻断晚到写入（`:559-588`）。

**规则（本方案补定义，不改底座）**：

| 能力 | 授权边界 |
| --- | --- |
| 只读巡检（`subagent_status` / `inspect` / `wait_agent`） | 同一 `session_id` 内的可运行 turn 均可；不要求 owner 匹配 |
| 变更（`extend_deadline` / `cancel` / `close` / `retry` / `reassign`） | **必须** `OwnerID == 当前会话` 且 lease 有效；否则需显式 `takeover`（新增动作，带 `reason`，写审计） |
| resume（恢复挂起 turn） | 仅账本所属会话的 resume dispatcher；跨层只唤醒直接父（Q9/EC-E3） |
| lease 续租 | 父 turn `running` 或每次 resume 时续租；**挂起态不续租但保留 ownership 记录**，重启后由"恢复 or orphaned"决策处理（P3） |
| 审计 | 所有 mutation 记 `ActorID`（会话 / 模型 / 用户 / `runtime_fallback`）与 `reason`（I4） |

**与 fencing 的关系**：任何写入走 CAS + Version（`UpdateExecutionRunCAS`），`orphan_suspected` 提升 token 后晚到写入被拒——避免"两个宿主同时延长/取消"的双写冲突（EC-B9）。

### 6.12 保留、GC 与"驱逐后可重建"（B3）

| 策略 | 口径 |
| --- | --- |
| 保留窗口 | 终态 obligation 保留至 **turn 终局 + N 天**（默认 7，可配）；窗口内 `subagent_status` 可见（含异常终态原因） |
| GC 条件 | 仅清"已 resolved **且** 无 artifact 引用 **且** 无 pending resume"的行；批量删除，单次有界 |
| 不得影响 | 当前 turn 的账本视图、决策窗口内的行、`orphan_suspected` 待处置行 |
| 驱逐后可重建 | 内存态（常驻 actor / 巡检缓存）可被驱逐；**账本是持久真源**，驱逐后由 store 重建（对齐 §13.4 的 residency 语义：驱逐只影响内存，不影响事实） |
| 挂起态持久化 | `{turn_id, session_id, obligation_ids, parked_at, decision_window_until, resume_queue}` **必须落盘**，否则重启即失忆（EC-B5；与 I9 联动） |

### 6.13 耐久性前提与降级路径（A1，实施准入项）

**事实（取证）**：默认 coordinator store 是**进程内**实现；store 为 `nil` 时**禁用 background 执行并回退同步 wait**（`backend/internal/agent/agent.go:325-336` 注释与实现）。即：**挂起能力天然依赖 durable store，而默认路径不保证它**。

**规则（I9）**：

1. 派发前探测 `store != nil && store.IsDurable()`（探测点与 `shouldExposeSpawnSubagents`（`agent.go:551-584`）同层，建议新增 `supportsSuspension()`）；
2. 不满足 ⇒ **禁止进入挂起**：不返回 batch handle 语义、不写 `awaiting_obligations`，按 legacy 同步路径执行，并投影一次 `SeverityWarning`（不刷屏）+ 工具描述明确告知"当前会话不支持托管挂起"；
3. 满足 ⇒ 正常挂起；挂起态与 resume 队列一并落盘（§6.12）。

**验收**：单测覆盖 `store=nil` 与"进程内 store"两条降级路径；集成测试断言降级时**不出现** `turn.suspended` 事件。

### 6.14 审批路由与权限继承（B2）

**已有底座（无需新造）**：

| 能力 | 现状 | 锚点 |
| --- | --- | --- |
| 审批/提问投影 | 子会话进入 `waiting_approval` / `waiting_input` 时，宿主投影进**父生命周期 inbox**（`ApprovalNotice`，host-neutral、best-effort、不改子结局）；请求与解决**共用同一事件类型**（原地更新，不产生 stale 行） | `backend/internal/supervision/approval_projection.go:10-17`、`:40-60`；事件 `agent_approval_requested` / `agent_question_asked`；运行事件 `approval_requested` / `approval_resolved` / `question_asked` / `question_answered` |
| 审批不误杀 | `waiting_approval` / `waiting_input` 使用独立 deadline，**永不被普通 progress 超时杀掉**（"blocked but healthy"） | `execution_supervisor.go:494-501` |
| 审批超时决策 | `approval_timeout`（`ApprovalDeadlineAt` 到期）→ 当前进强制分支，状态 `SupervisionTimedOut` | `execution_supervisor.go:498-500`、`:603-605` |
| 父侧指引 | `read_agent_events` / `wait_agent` 回执检测到 pending approval 时给出 `next_action="resolve_pending_approval: call resolve_agent_approval with allow=true\|false; do not re-poll read_agent_events for the same approval"` | `backend/internal/toolbroker/types.go:600-712` |
| 控制面调用 | `resolve_agent_approval` 参数 `{allow, id, patched_args, request_id, session_id}`；过期 token 返回 `ErrAgentRunSuperseded`；跨会话写入前丢弃外部 run token（`detachForeignSessionRunControl`） | `broker_arg_audit.go:32`；`internal/errors/codes.go:26-28`；`internal/chat/actor.go:3992-3995` |

**本方案补的规则**：

1. **审批请求是一类 resume 触发器**：与 terminal / progress / deadline 并列（§6.1），且**不受 progress / auto-wake 预算裁剪**（同 EC-A4 的独立通道）——否则"父挂起 + 子等审批"双双卡死，这正是 B2 的风险原形；
2. **投影严重度写死**：`approval_requested` / `question_asked` 必须投影为 `SeverityCritical + action_required`（现由 `projection.go:87-103` 决定是否排 wake），否则挂起 turn 不会被唤醒处理审批；
3. **escalate-first 同样适用**：`approval_timeout` 不再直接进强制分支，而是先上报父决策（allow / deny / extend / cancel），决策宽限期后才兜底（默认 deny + 记 `CancelSource`）；
4. **父挂起不失去决策权**：审批请求与决策窗口一并落盘（§6.12），resume 后父仍可 `resolve_agent_approval`（含 `patched_args` 改写参数）；
5. **权限继承**：子 Agent 的权限模式 / 工具白名单继承自派发方（`spawn_agent` 的 `permission_mode` 等），**跨层不可提权**；父在审批时可通过 `patched_args` **收紧**参数，但不能放宽子会话既有策略边界（`internal/policy` 不变）；
6. **回执契约**：审批相关的过期/失败一律走"成功观测 + `next_action`"（§6.4 / B5）——`ErrAgentRunSuperseded` 映射为 `next_action=inspect|finalize`，**不得**表现为工具失败；
7. **UI 可见性**：挂起期间 UI 必须显示"子任务等待审批（N）"，用户可直接在 UI 批准/拒绝（多会话审批 UI 已存在：`cmd/aicli/commands/web/js/approvals.js`）。

### 6.15 steer 与 resume 的交互（B6）

| 场景 | 规则 |
| --- | --- |
| 父**挂起**（passive wait）收到用户输入 | 作为 **steer 触发器**起一段新 resume episode（同 `turn_id`），用户输入置顶；账本不变（EC-I5） |
| 父 running 且正在 **active wait** | 立即结束等待段返回，用户输入优先（§16.3 约束 3） |
| 父 running 且 **resume episode 执行中** | 不硬截断 episode：用户输入在**当前工具调用边界**后注入；若用户显式要求中断，复用 `send_input` 的 `interrupt`（**唯一**接受 `interrupt` 的工具） | 
| steer 与**审批**同时到达 | **审批优先**：必须先 `resolve_agent_approval`（工具层已用 `next_action` 强制），steer 不得绕过审批闸门 |
| steer 投递失败（目标已终态） | 回执带 `next_action=inspect\|finalize`；投递审计沿用 mailbox 状态（`Queued` / `Delivered` / `Failed`） |
| 计数 | steer **不计入** stall / 延长次数，不改变 `progress_seq` 语义 |

锚点：`send_input` 参数 `{id, interrupt, message, session_id}`（`broker_arg_audit.go:31`，`:95` 注明"send_input is the only tool that accepts interrupt"）；投递审计 `MailboxDeliveryStatus{Queued,Delivered,Failed}`（`internal/api/skills/session_runtime_support.go:1912-2009`）；对照参照实现的 `Steered` 结局（§13.2）。

---

## 7. 代码改动清单

| # | 位置 | 改动 | 对应缺口/审查项 |
| --- | --- | --- | --- |
| 1 | `backend/internal/supervision/execution_supervisor.go:503-553` | 软阈值分支改为 escalate-first：`progress_stalled` → 投影 critical + `ActionTaken="escalated"`；新增决策宽限期字段与兜底执行 | G3 |
| 2 | `backend/internal/supervision/types.go:197-202`、`action_service.go:513-541`、`local_control.go:244-252` | 新增 `ActionKind = "extend_deadline"`，纳入白名单与变更类校验 | G4 |
| 3 | `backend/internal/supervision/execution_run.go`、`execution_store.go` | 新增 `TurnID` / `ExtensionCount` / `ExtendedTotal` / `DecisionWindowUntil` 字段与迁移 | G6/G4 |
| 4 | `backend/internal/supervision/wake_scheduler.go:284-293`、`wake_consumer.go` | wake 升级为 resume：携带 `turn_id`，恢复同一 turn；`AutoWakePrompt` 改为携带 rollup digest 的 resume 上下文 | G1 / G12 |
| 5 | `backend/internal/agent/loop.go:2865-2896`、`6948-6952` | 删除内联阻塞路径；派发后返回挂起信号；`execution_mode` 收敛、`wait_timeout_sec` → `deadline` | G1/G7 |
| 6 | `backend/cmd/aicli/commands/chat_actor_host.go:794-805`、`806-842` | `Busy()` 纳入 `awaiting_obligations`；30m 硬编码降级为单次 resume episode 熔断；`Runnable` 增加"有 pending 用户输入不 drain" | G1 |
| 7 | 工具层（`internal/agent` 工具注册 + `internal/toolbroker`） | 新增 `subagent_status` / `subagent_inspect_task`；`extend_deadline` 暴露为控制动作 | G5 |
| 8 | `backend/internal/supervision/config.go` | 新增软阈值倍率、决策宽限期、延长上限、巡检间隔等配置项（`WithDefaults` 保持零值兼容） | G3/G4 |
| 9 | `backend/internal/api/skills/supervision_progress_check.go` | 从宿主级固定间隔升级为按 obligation 的 `check_in_after` 巡检预约 | G5 |
| 10 | 进度埋点（`TaskProgressInterval`） | 托管 turn 下强制开启进度写入，避免 stall 判定失真 | G8 |
| 11 | `backend/internal/agent/agent.go:325-336`、`:551-584` | 新增 `supportsSuspension()` 探测；无 durable store ⇒ 禁止挂起并走降级路径（I9 / §6.13） | G11 |
| 12 | `backend/internal/supervision/execution_supervisor.go`（扫描器/watchdog） | 新增"无终态且 deadline 已过/丢失 ⇒ 强制判终态 + 投影 critical"（I10 / §16.4） | I10 |
| 13 | `backend/internal/toolbroker/broker.go:45`、`:1617-1640`、`:3235-3243`；`loop.go:6948-6960` | `wait_agent` / `wait_team` 按 §16.3 改造（四条约束 + 统一返回契约）；派发接口补 per-task `deadline` | A2 / A3 |
| 14 | `backend/internal/agent/doom_loop.go:178-184` | 巡检/等待工具改为"无进展重复"判罚，按 `progress_seq` 豁免（B1 / §6.4） | G13 |
| 15 | `backend/internal/supervision/wake_consumer.go:12` + digest 载体 | 通知载体结构化 + `notify_key` 幂等键（B4 / §6.6） | G2 / B4 |
| 16 | `execution_run.go` / `execution_store.go`（GC）/ `action_service.go` | 结果取回字段（`ArtifactRefs` / `ResultSummary`）、保留/GC 策略、`takeover` 授权动作（§6.10–§6.12） | G9 / G10 |
| 17 | `execution_supervisor.go:494-501`、`approval_projection.go`、`projection.go:87-103` | 审批请求纳入 resume 触发器（独立通道）；`approval_timeout` 改 escalate-first；审批投影 severity 写死 critical + action_required（§6.14） | B2 |
| 18 | `internal/chat/actor.go`（steer 注入路径）、`toolbroker`（`send_input`） | steer 作为 resume 触发器；episode 边界注入；steer 优先于等待段、不得绕过审批（§6.15） | B6 |

**兼容性约束**：所有新增字段在 `WithDefaults` 中保持零值向后兼容；`extend_deadline` 属于**新增**动作，未知动作的既有校验路径（`ErrActionInvalid`）不受影响；`wait_agent` / `wait_team` **不删除**（Q11），仅改语义与返回契约，旧参数保留读取。

> **锚点校正（2026-09-23 实测）**：本表 #2（应为 `types.go:193-202`）、#3（DDL/迁移在 `sqlite_store.go:287-327`）、#5（`wait_timeout_sec` 在 `loop.go:6953-6956`）、#7（supervision 工具注册在 `toolbroker/supervision_tools.go:120-122`）、#11（`:551-584` 是工具面门控，非耐久性探测点）、#13（`wait_agent` 主体 `:1996-2060`、`wait_team` `:2757-2800`）、#16（`ArtifactRefs`/`ResultSummary` 现居结果投影侧；GC 仅 `PruneWakeClaims`）、#18（"steer" 为待建概念）的锚点已核验并修正，明细见 `docs/plan/supervised-turn-suspension-and-agent-task-control-implementation-plan-20260923.md` §12。

---

## 8. 实施分期与验收

### P0-前置：准入条件（实施前必须确认）

- A1（耐久性探测 + 降级，§6.13 / I9）、A4（结果取回契约，§6.10）、A5（授权边界，§6.11）、A6（resume 容量门控，§6.1 步骤 5）**已落文档**；
- 实施顺序：**I9 探测与 I10 watchdog 必须先于 P1 落地**（否则挂起会静默失忆或永久悬挂）；
- B1（doom-loop 豁免，§6.4）提前到 **P2 首批**，否则巡检/active wait 上线即被误判。

### P0：决策权回归（最小闭环）

- 范围：改动 #1 + #2 + #3（部分）+ #8
- 交付：软阈值不再自动 cancel；主 Agent 在决策宽限期内可 `extend_deadline` 或 `cancel`；宽限期后 runtime 兜底
- 验收：
  1. 构造 `progress_stalled` 场景，断言**不出现** `cancel_requested`，而出现 `escalated` + critical 通知；
  2. 决策宽限期内调用 `extend_deadline` → 断言 `ProgressDeadlineAt` 前移、`ExtensionCount+1`、无 cancel；
  3. 宽限期结束无决策 → 断言兜底执行且 `CancelSource="decision_window_expired"`；
  4. 延长上限/不可逆点拒绝路径（I5/I6）各有单测。

### P1：turn 挂起与同一 turn resume

- 范围：改动 #4 + #5 + #6
- 交付：`wait` 语义由挂起承接；resume 复用同一 `turn_id`；I1 生效
- 验收：
  1. 派发 3 个 background 子任务，断言 `turn.suspended` 后父 actor **非 Busy 占用**（可接受用户输入与 ESC）；
  2. 子任务全部终态后断言 resume 使用**同一 `turn_id`**，且终局报告在同一 turn 内产出；
  3. 人为构造"账本非空时模型请求收尾" → 断言被拦截并转入挂起（I1）；
  4. 长任务（>30m）不再被父 turn 熔断截断；
  5. resume 撞容量门控（并发/深度）时进入有界 FIFO，digest 显示排队位次，超时走 escalate（A6）。

### P2：巡检与管理闭环

- 范围：改动 #7 + #9 + #10 + #14（doom-loop 豁免，首批）+ #17 + #18（审批路由 / steer 联动）
- 交付：`subagent_status` / `subagent_inspect_task` / 改造后的 `wait_agent` 可用；巡检预约可配置；进度埋点强制开启；巡检不被误判
- 验收：
  0. 连续 active wait / 巡检且读数发生变化时**不**触发 doom loop；读数不变连续 5 次才计入空转（B1）；
  1. 二维 stall 矩阵四象限各有 fixture 测试（长工具调用不得被判 `suspect_dead`）；
  2. 巡检输出超限时走 artifact，digest 体积受 `DigestMaxChars` 约束；
  3. 主 Agent 依据巡检结果 cancel 卡死任务 → 级联取消子会话 + 账本更新 + timeline 可见；
  4. 子任务等审批且父挂起 ⇒ 审批请求能唤醒父（不被预算裁剪），`approval_timeout` 走 escalate-first（B2）；
  5. 挂起态收到用户 steer ⇒ 起新 episode、账本不变；steer 与审批同时到达时**审批优先**（B6）。

### P3：收敛与清理

- 范围：改动 #5（移除）+ 迁移
- 交付：删除阻塞分支与 `wait` 取值；重启恢复补齐（交互式宿主的"恢复 or orphaned"决策）
- 验收：
  1. 宿主重启后挂起 turn 的恢复/围栏路径各有测试；
  2. `execution_mode` 参数标记 deprecated 后旧值仍可用（兼容期）；
  3. 全量回归 + 一次真实长任务端到端（≥2h 声明预算 + 中途一次延长）。

### 度量指标（用于验证收益）

| 指标 | 期望方向 |
| --- | --- |
| 被 runtime 强制取消的 run 占比 | 下降（决策前置） |
| `decision_window_expired` 兜底占比 | 低且稳定 |
| 成功完成到父 Agent 汇报的延迟 | 收敛到一次 resume 周期内 |
| 单托管 turn 的 resume 次数 / token 成本 | 受预算约束，无风暴 |
| 误杀率（取消后 5 分钟内子任务本可完成） | 趋近 0 |

---

## 9. 边界场景分析

共 43 条，按 A–G 分组。每条给出：场景 → 风险 → 期望行为 / 落地要点。（另见 §15 追加的 EC-H 12 条 + EC-I 5 条 + EC-J 4 条，合计 64 条。）

### A. 唤醒与决策时序

| ID | 场景 | 风险 | 期望行为 / 落地要点 |
| --- | --- | --- | --- |
| EC-A1 | 挂起期间用户发来新消息 | 用户 turn 与 resume 争抢 actor；若直接另起 turn，则挂起 turn 与用户 turn 并发，破坏 I1 的单一 turn 假设 | 用户输入优先且不丢：作为 steering 注入挂起 turn 的下一段 resume，或排队并明确提示"任务运行中，将在下一段处理"；`Runnable` 增加"有 pending 用户输入不 drain"（改动 #6） |
| EC-A2 | resume episode 运行中又到达终态事件 | 并发第二轮 resume（现由 `ParentRunnable` 门控挡住）；事件积压 | 事件入 pending resume 队列，合并进下一段；**terminal 事件永不丢弃、不被预算裁剪** |
| EC-A3 | 决策宽限期内父会话不可运行（用户输入中 / 等待审批 / compacting） | 纯墙钟计宽限期 → "没人看就兜底杀掉"，恰好违背决策前置初衷 | 宽限期按**可运行时钟**计量（I8）：不可运行区间不计入；同时设墙钟上限（如 2× W）防止无限顺延 |
| EC-A4 | 唤醒预算耗尽（`WakeMaxAutoWake=5` 等） | terminal 事件被预算挡住 → 账本永不空 → turn 永久挂起 | terminal 类 resume 走**独立通道**，不受 progress/auto-wake 预算限制；仅 progress 类受约束 |
| EC-A5 | 同一 obligation 反复 stall（延长后又 stall） | "延长 → stall → 延长"无限循环 | `ExtensionCount` 达上限（I5）后强制走硬阈值；第二次 stall 起升级 critical 并建议 cancel/reassign |
| EC-A6 | 主 Agent 决策"继续等"但未给新 deadline | 扫描器下一轮立即再 stall → 空转 | "继续等"必须落具体续期值（默认 = 1× 软阈值），禁止空续期 |

### B. 超时与延长

| ID | 场景 | 风险 | 期望行为 / 落地要点 |
| --- | --- | --- | --- |
| EC-B1 | 延长与扫描器判定 `execution_timed_out` 竞态 | 扫描器刚发起 cancel，延长随后写入 → 状态错乱 | 走 CAS + Version（`UpdateExecutionRunCAS`）；escalate 前先 CAS 声明"决策窗口开启"避免重复上报；run 已 `cancel_requested` 时拒绝延长（I6） |
| EC-B2 | 延长超出上限 / 系统天花板 | 绕过 30m 熔断的设计初衷 | 拒绝 + 明确 `next_action`；同时建议改配置或重派 |
| EC-B3 | 延长已进入 cancel grace 的 run | 半取消状态 | I6 拒绝；确需继续则走 `retry` / `reassign` 重派 |
| EC-B4 | 延长后子任务立刻完成（终态事件在途） | 延长"看似无用"；重复终态投影 | 延长只改 deadline 字段（无副作用）；终态投影保持幂等（沿用 `projectTerminal` 收敛语义） |
| EC-B5 | 宿主重启 | 内存中的挂起态 / 决策窗口丢失 | `execution_runs` 已持久 deadline/heartbeat/progress；挂起态与决策窗口需一并持久化，重启后重建扫描与 pending resume |
| EC-B6 | 延长了 run deadline，但子任务内部超时仍触发失败 | 困惑"延长了为什么还失败" | 语义分离并在回执中显式说明生效来源（`TimeoutSource`，`execution/timeout.go:13-25`、`59-67`）：延长只覆盖 `agent_run_deadline` 类来源，不覆盖 `parent_context_deadline` / `tool_default` / `runtime_ceiling` |
| EC-B7 | 延长后的 deadline 跨越 resume episode 熔断（原 30m） | resume 被截断被误判为任务失败 | episode 截断只结束"这一段"；digest 非空则自动续跑（排队下一段），obligation 状态不变 |
| EC-B8 | 声明式预算被滥用（所有任务都声明 2h） | 熔断失效 | 声明预算受 ceiling 约束；超限需显式理由并计入审计与度量 |
| EC-B9 | 多宿主/多进程同时延长或取消同一 run | 双写冲突 | 统一走 `action_service`（不直接改库）+ CAS + fencing token（`execution_supervisor.go:559-588`） |

### C. 终止与级联

| ID | 场景 | 风险 | 期望行为 / 落地要点 |
| --- | --- | --- | --- |
| EC-C1 | cancel 一个已 terminal 的 run | 报错噪音 | 幂等成功（返回当前状态 + `already_terminal` 回执） |
| EC-C2 | 级联取消部分成功（子会话成功、孙会话失败） | 账本状态不一致 | 定义 `partial_cancel` 语义：父 turn 收到失败明细；账本置 `canceling` 并继续收敛，cancel grace 到期走 orphaned 围栏 |
| EC-C3 | cancel 后子任务仍写入（晚到结果） | 结果污染 | fencing token 阻断（已实现）；账本与 UI 标注"晚到写入被围栏" |
| EC-C4 | cancel 正在等待审批的子任务 | 审批态与执行态路径不同 | 先撤销审批请求再取消 run；`ApprovalDeadlineAt` 路径独立（`execution_supervisor.go:496-502`） |
| EC-C5 | 用户 ESC / 关闭会话 | 遗留运行中的子任务 | 级联取消账本内全部 obligation；turn 终态 `canceled_by_user`；持久化终态，重启不得复活 |
| EC-C6 | cancel 无效（interrupt 不响应） | 卡死任务清理不掉 | cancel grace 到期 → orphaned 围栏（已有）；补"围栏后是否 retry/reassign"的决策上报 |
| EC-C7 | 终止卡住任务后父 turn 的账本更新 | 终局综合被阻塞 | canceled 子任务计入终态（`BatchCanceled` 已是 warning/closed，`chat_actor_host.go:508-511`），不阻塞账本清空 |

### D. 巡检与证据

| ID | 场景 | 风险 | 期望行为 / 落地要点 |
| --- | --- | --- | --- |
| EC-D1 | 巡检输出过大 | 父 turn 上下文爆炸 | artifact 化 + 字节上限（I7）；digest 只带预览 + `artifact_id` |
| EC-D2 | 进度埋点关闭（`TaskProgressInterval=0`）导致 `LastProgressAt` 缺失 | stall 判定失真，误判健康任务 | 托管 turn 下强制开启埋点（改动 #10）；确无埋点时退化为 heartbeat-only 并标注 `evidence=weak`，默认不自动取消 |
| EC-D3 | 心跳新鲜 + 进度停滞（如 20 分钟构建） | 误杀长工具调用 | 判 `suspect_stuck` 而非 `suspect_dead`；默认动作是 inspect 而非 cancel；提供长任务/长工具阈值配置 |
| EC-D4 | 子任务处于 `waiting_approval` / `waiting_input` | 被误计为进度停滞 | 不计入 stall；由 `ApprovalDeadlineAt` 单独管 |
| EC-D5 | 巡检读数与终态事件竞态（查到 running，下一秒完成） | 管理动作打空 | 允许陈旧读数；动作幂等，失败回执 `already_terminal` |
| EC-D6 | 子 Agent 输出含敏感内容 | 巡检把完整 transcript 注入父上下文 | 沿用既有输出边界与脱敏策略，只给预览 + artifact 引用；巡检不得绕过授权边界 |

### E. turn 生命周期

| ID | 场景 | 风险 | 期望行为 / 落地要点 |
| --- | --- | --- | --- |
| EC-E1 | 账本条目因 bug 永不终结 | turn 无限挂起（"永不结束"的极端形态） | turn 级 hard cap（建议 24h，可配）+ 强制 orphaned + 告警 |
| EC-E2 | 宿主退出/重启（Ctrl+C、服务器重启） | 挂起 turn 无主 | 交互式宿主需明确"恢复 or 标记 orphaned"；非交互已有 recovery projector（`chat_actor_host.go:451-467` 的 drainWake 差异），交互式需补齐 |
| EC-E3 | 嵌套托管（子 Agent 自己也是父） | 孙任务终态唤醒子、子挂起又唤醒父 → 跨层唤醒风暴 | **只在直接父处 resume**；父的 digest 只聚合直接子，更深层由子自行收敛 |
| EC-E4 | 挂起期间父上下文被 compaction | resume 缺少决策依据 | resume 必须自带 rollup digest，不依赖历史细节（沿用 `DigestMaxItems/Chars`） |
| EC-E5 | 同一会话出现多个挂起 turn | 并发写账本 | 断言禁止（I3）；违反时降级告警并合并到一个 turn |
| EC-E6 | turn 已结束但账本非空（I1 违反自愈） | 静默丢失义务 | 重新进入挂起；无法恢复则强制 orphaned + 告警，绝不静默成功 |
| EC-E7 | 挂起期间用户 ESC | 同 EC-C5 | 取消挂起 turn + 级联取消账本 |
| EC-E8 | resume 注入与自然 turn preflight 重复注入同一批未决项 | 重复打扰、token 浪费 | resume 注入后标记 delivered/seen；自然 turn preflight 不重复注入已 delivered 且未变更项（沿用 `supervision_handlers.go:314-317`，seen ≠ acknowledged） |

### F. 成本与预算

| ID | 场景 | 风险 | 期望行为 / 落地要点 |
| --- | --- | --- | --- |
| EC-F1 | resume 风暴（100 个子任务同时终态） | 唤醒 100 次 | terminal 事件合并为一次 resume（批量 digest）；账本完整但只唤醒一次 |
| EC-F2 | 巡检轮 token 成本 | 成本失控 | digest 上限 + 巡检预算（次数/间隔）；巡检结果按需拉取，不全量推送 |
| EC-F3 | 模型滥用 extend（每次都延长） | 熔断形同虚设 | I5 上限 + 延长率度量 + 每次延长在 digest/UI 可见；连续延长需更强理由 |

### G. 兼容与迁移

| ID | 场景 | 风险 | 期望行为 / 落地要点 |
| --- | --- | --- | --- |
| EC-G1 | 依赖 `wait` 内联报告的调用方 | 破坏性变更 | 兼容期双写（batch handle + resume 后聚合报告）；显式标注 breaking |
| EC-G2 | 非交互宿主无"用户"参与 | 挂起 turn 无人恢复 | resume 由 pending resume 队列驱动（progress check 通道升级为 obligation 级），不依赖人工触发 |
| EC-G3 | 审批等待与挂起叠加（子任务等审批、父挂起） | 父 turn 被审批无限拖住 | 审批在子会话 UI 呈现；`ApprovalDeadlineAt` 兜底；父挂起不因审批失去决策权 |
| EC-G4 | 历史生命周期行无 `turn_id` | 迁移期误判 I1 | 允许 `turn_id` 为空，此类 run 走原有"独立 run"语义，不参与账本清空判定 |

---

## 10. 风险分级与回滚

沿用 `docs/plan/spawn-agent-team-supervision-timeout-recovery-plan.md` 的 R0/R1/R2 分级（避免与实施阶段 P0–P3 编号混淆）。

| 风险 | 等级 | 说明 | 回滚 |
| --- | --- | --- | --- |
| escalate-first 改动后，卡死任务不再被及时清理 | R1 | 软阈值不再直接 cancel，若决策链路失效则任务滞留（有硬阈值兜底） | 配置开关 `escalate_first=false` 恢复原强制分支；保留 `execution_timed_out` 硬路径不受影响 |
| turn 挂起导致"turn 永不结束" | R1 | 极端形态是会话被长期占用 | turn 级 hard cap（EC-E1）+ 用户 ESC 级联取消 + 配置开关退回"结束再唤醒"模式 |
| `extend_deadline` 被滥用 | R2 | 有上限与审计，影响面可控 | 关闭动作白名单即可回滚 |
| 同 turn resume 的上下文管理复杂度 | R1 | 涉及 turn 身份、digest 注入、preflight 去重 | 保留"新 turn 唤醒"作为降级路径（I3 断言失败时的保底行为） |
| `wait` 移除的兼容破坏 | R1 | 影响脚本/API 客户端 | 兼容期双写；`wait` 取值保留映射，不做硬删除直到 P3 |
| 进度埋点强制开启的写放大 | R2 | `TaskProgressInterval` 原为 0=不写 | 仅在存在挂起 turn/obligation 时开启，空闲期不写 |
| 挂起态依赖 durable store，降级路径失效 | R1 | 进程内 store 下若误挂起 → 重启失忆，I1 无法自愈 | I9 探测（§6.13）+ 配置开关 `suspension_enabled`；探测失败自动回退同步路径 |
| 巡检/等待工具被 doom-loop 误判 | R2 | 影响面限于巡检可用性（不丢数据） | 按"无进展重复"判罚（§6.4）；必要时关闭巡检工具暴露或提高阈值 |

**总体判断**：改动集中在 supervision 控制面与 agent 循环的**语义层**，不涉及存储结构破坏性迁移（新增字段走 `WithDefaults` 零值兼容），风险可控。

---

## 11. 开放问题（需拍板）

| # | 问题 | 建议默认 | 影响 |
| --- | --- | --- | --- |
| Q1 | 挂起期间用户发新消息：steering 注入 / 排队 / 另起 turn？ | 排队 + 明确提示，resume 时合并 | 决定 EC-A1 的 UX 与 actor 竞争规则 |
| Q2 | 决策宽限期 W 的默认值 | W = 2× `HeartbeatTimeout`（10m），墙钟上限 2W | 决定"决策前置"的实际效果与兜底延迟 |
| Q3 | stall 升级阈值 | `2× ProgressDeadlineAt` | 决定误报率与打扰频率 |
| Q4 | 延长上限 | 3 次 / 单次 ≤1× 原始 / 总量 ≤4× | 决定熔断强度（I5） |
| Q5 | turn 级 hard cap | 24h（可配） | 决定"永不结束"的上界 |
| Q6 | `wait` 兼容期长度与双写范围 | 一个发布周期；双写 batch handle + resume 聚合报告 | 决定 EC-G1 的破坏面 |
| Q7 | 巡检工具的可见范围 | 仅父 Agent（子 Agent 不可巡检兄弟/父） | 权限边界与信息隔离 |
| Q8 | `orphan_suspected` 是否纳入决策上报 | 暂维持 observe-only，P4 回收落地后接入 | 与既有 P3/P4 计划对齐 |
| Q9 | 跨层 resume 策略 | 只唤醒直接父（EC-E3） | 决定嵌套托管的事件风暴规模 |
| Q10 | 托管 turn 期间是否允许并发新 turn | 不允许（`Busy()` 纳入 `awaiting_obligations`），但允许中断与插话 | 决定 EC-A1/EC-E5 |
| Q11 | `wait_agent` / `wait_team` 工具族处置 | **已拍板（用户 2026-09-23）：保留并改造**为有界、活动驱动、可被打断、超时即观测的等待原语（§16.3），不作 deprecated | 决定 A2 的修复口径与挂起入口的关系 |
| Q12 | 终态 obligation 的保留窗口与 GC 策略 | **已落 §6.12**：保留至 turn 终局 + N 天（默认 7，可配）；GC 只清已 resolved 且无 artifact 引用者 | 决定账本增长与 `subagent_status` 的可见范围（B3） |

---

## 12. 附录：锚点索引

**代码锚点（按符号名引用，行号会随代码漂移）**

| 主题 | 锚点 |
| --- | --- |
| 执行模式与批次 deadline | `backend/internal/agent/loop.go:6948-6952` |
| wait 内联阻塞 | `backend/internal/agent/loop.go:2865-2896` |
| 深度上限 | `backend/internal/agent/scheduler.go:430-436` |
| 唤醒提示常量 | `backend/internal/supervision/wake_consumer.go:12`、`14-19` |
| 唤醒判据 | `backend/internal/supervision/projection.go:87-103`、`107-110` |
| 唤醒门控 | `backend/internal/supervision/wake_scheduler.go:284-293` |
| 配置项 | `backend/internal/supervision/config.go:18-24`、`101-121` |
| run 账本字段 | `backend/internal/supervision/execution_run.go:92-101` |
| run 持久化 | `backend/internal/supervision/execution_store.go:113-119`、`152-158` |
| per-run deadline 解析 | `backend/internal/supervision/execution_supervisor.go:206-211` |
| 决策阶梯 | `backend/internal/supervision/execution_supervisor.go:496-524` |
| 强制执行分支 | `backend/internal/supervision/execution_supervisor.go:526-553` |
| orphan 围栏 | `backend/internal/supervision/execution_supervisor.go:559-588`、`473-479` |
| 决策投影 | `backend/internal/supervision/execution_supervisor.go:590+` |
| 控制动作枚举 | `backend/internal/supervision/types.go:197-202` |
| 动作校验 | `backend/internal/supervision/action_service.go:513-541`、`local_control.go:244-252` |
| 快照证据字段 | `backend/internal/supervision/snapshot.go:105-113`、`451-459` |
| stall 告警 | `backend/internal/supervision/alerts.go:158-166` |
| 超时来源语义 | `backend/internal/execution/timeout.go:13-25`、`37-41`、`59-67` |
| 宿主投递与 30m 熔断 | `backend/cmd/aicli/commands/chat_actor_host.go:806-842`（826 行为超时） |
| 宿主可运行判定 | `backend/cmd/aicli/commands/chat_actor_host.go:794-805` |
| 交互式 init 不 drain | `backend/cmd/aicli/commands/chat_actor_host.go:850-857` |
| 本地批次投影（干净完成不排 wake） | `backend/cmd/aicli/commands/chat_actor_host.go:512-521`、`551-553` |
| 生命周期行允许动作 | `backend/cmd/aicli/commands/chat_actor_host.go:539` |
| API 批次投影与 drain | `backend/internal/api/skills/supervision_batch_projector.go:50-110` |
| preflight 注入 | `backend/internal/api/skills/supervision_handlers.go:314-317` |
| 周期进度巡查（P2-D） | `backend/internal/api/skills/supervision_progress_check.go` |
| 调试渲染（deadline/heartbeat） | `backend/cmd/aicli/commands/chat_debug.go:2116-2124`、`2341-2350` |

**相关方案文档**

- `docs/plan/spawn-subagents-async-supervisor-plan.md`
- `docs/plan/spawn-agent-team-supervision-timeout-recovery-plan.md`
- `docs/plan/multi-agent-durable-lifecycle-hardening-plan-20260920.md`
- `docs/plan/multi-agent-execution-optimization-plan.md`
- `docs/plan/supervision-parent-child-control-optimization-plan-20260917.md`
- `docs/plan/supervision-business-supervision-implementation-plan.md`
- `docs/plan/supervision-manual-audit-plan-20260922.md`
- `docs/plan/tool-output-artifact-cascade-audit-and-optimization-plan-20260919.md`

---

## 13. 参照实现对照：Codex 如何管理子 Agent

> 取证方式：对 `E:\projects\ai\codex`（Codex Rust 工作树）只读走查（grep/view，2026-09-23）。下文路径相对该仓库根；行号随代码漂移，按符号名定位。
> 取证范围限定：子 Agent 的**工具族、等待语义、完成通知、容量/常驻/关闭、批次结果契约**；不涉及 prompt/模型层。

### 13.1 工具族与状态机

| 维度 | Codex 事实 | 锚点 |
| --- | --- | --- |
| 工具枚举 | `CollabAgentTool{SpawnAgent, SendInput, ResumeAgent, Wait, CloseAgent}` | `codex-rs/protocol/src/items.rs:248-256`；`app-server-protocol/src/protocol/v2/item.rs:1028-1033` |
| V1 工具集 | `spawn_agent` / `wait_agent` / `send_input` / `resume_agent` / `close_agent` | `codex-rs/core/src/tools/handlers/multi_agents/{spawn,wait,send_input,resume_agent,close_agent}.rs` |
| V2 工具集 | 追加 `list_agents`（巡检）/ `send_message` / `followup_task` / `interrupt_agent` | `codex-rs/core/src/tools/handlers/multi_agents_v2/{list_agents,wait,spawn,send_message,message_tool,followup_task,interrupt_agent}.rs` |
| 状态机 | `AgentStatus` 由事件推导：TurnStarted→`Running`、TurnComplete→`Completed(last_message)`、TurnAborted→`Interrupted`/`Errored`、Error→`Errored`、ShutdownComplete→`Shutdown`；`is_final` = 非 `PendingInit`/`Running`/`Interrupted` | `codex-rs/core/src/agent/status.rs:4-28` |
| 状态订阅 | 父侧 `subscribe_status(child_thread_id)` 用 `watch` 通道等待 `is_final`，**事件驱动而非轮询** | `codex-rs/core/src/agent/control.rs:450-463` |

要点：Codex 的"管理面"就是**五个动作 + 一个巡检**（spawn / wait / list / send|interrupt / close|resume），没有独立的"延长超时"动作——延长超时在 Codex 里等价于**再等一次**（见 13.2）。

### 13.2 等待语义：有界、活动驱动、可被打断、超时即观测

`wait_agent`（V2）的实现是本方案最直接的参照物（`codex-rs/core/src/tools/handlers/multi_agents_v2/wait.rs`）：

| 特征 | 事实 | 锚点 |
| --- | --- | --- |
| 参数面 | 只有一个 `timeout_ms`，无 unbounded 形态 | `wait.rs:127-131` |
| 区间钳制 | 越界不静默截断，返回**模型可见**错误（`RespondToModel`："timeout_ms must be at least {min}" / "at most {max}"） | `wait.rs:50-66` |
| 默认值 | min = 10s、default = 30s、max = 1h；硬上限 `HARD_MAX_MULTI_AGENT_V2_TIMEOUT_MS = 1h` | `codex-rs/core/src/config/mod.rs:206-210`、`:265-267` |
| 等待机制 | 订阅 input-queue 活动通道，`timeout_at(deadline, activity_rx.changed())`——**活动驱动**，不做 250ms 级轮询 | `wait.rs:178-196` |
| 结局枚举 | `MailboxActivity`（子任务来信）/"Wait completed."、`Steered`（被新用户输入打断）/"Wait interrupted by new input."、`TimedOut`/"Wait timed out." | `wait.rs:140-150`、`:171-176` |
| 超时语义 | 超时是**成功观测**：返回 `{message, timed_out: bool}` 且 `success_for_logging = true`，不是错误、不抛异常 | `wait.rs:133-137`、`:158-160` |
| 可打断性 | 用户 steer（新输入）会立即结束等待并让用户输入优先 | `wait.rs:143-144`、`:190-193` |

**这是对"wait 会堵死进程"这一判断最有力的外部证据**：Codex 从未让 wait 无界——它被三重约束（上下限钳制 / 活动驱动 / 可被 steer 打断），且把"等超时了"降级成一条普通观测结果。也就是说，**需要废弃的不是"等待"这个动作，而是"无界阻塞"这种实现**。

### 13.3 完成通知：注入上下文，但不唤醒父 turn

| 特征 | 事实 | 锚点 |
| --- | --- | --- |
| 通知触发 | 子线程进入 `is_final` 后，父侧异步任务生成通知 | `codex-rs/core/src/agent/control.rs:441-517` |
| V1 路径 | `format_subagent_notification_message` → `parent_thread.inject_user_message_without_turn(message)` | `control.rs:510-516` |
| V2 路径 | `send_inter_agent_communication(..., trigger_turn=false)`；**只有 `trigger_turn=true` 才** `maybe_start_turn_for_pending_work_with_sub_id` | `control.rs:496-507`；`codex-rs/core/src/session/handlers.rs:289-301` |
| 注入语义 | 注释原文："Records a user-role session-prefix message **without creating a new user turn boundary**" | `codex-rs/core/src/codex_thread.rs:445-451` |
| 载体格式 | 结构化上下文片段：`<subagent_notification>{"agent_path","status"}</subagent_notification>`（user 角色、稳定标记） | `codex-rs/core/src/context/subagent_notification.rs:20-41`；`core/src/session_prefix.rs:16-25` |
| 已知欠账 | 该载体自注 TODO："unify with structured schema" | `codex-rs/core/src/session_prefix.rs:19` |

**结论**：Codex 的选择是"**通知不唤醒**"——父 turn 若已结束，完成消息只是躺进上下文，等下一次有人起 turn 时才被读到。这恰恰是本方案 §1 缺口 2 描述的那个洞（"完成后自动总结"不可靠）；Codex 用"父 Agent 自己负责 wait/poll"来规避，代价是把正确性责任推给模型。本方案的 I1（账本非空禁止终态）是对这一洞的正面修复，但因此我们承担了 Codex 不承担的复杂度（挂起态持久化、turn 身份、resume 预算）——必须保留降级路径（见 §14.3 A1）。

### 13.4 容量、常驻与关闭

| 机制 | 事实 | 锚点 |
| --- | --- | --- |
| 线程总量 | `DEFAULT_AGENT_MAX_THREADS = Some(6)` | `codex-rs/core/src/config/mod.rs:206` |
| 单会话并发 | `DEFAULT_MULTI_AGENT_V2_MAX_CONCURRENT_THREADS_PER_SESSION = 4` | 同上 `:207` |
| 深度 | `DEFAULT_AGENT_MAX_DEPTH = 1` | 同上 `:268` |
| 起 turn 前门控 | `ensure_execution_capacity_for_turn_start(...)`：容量不足时不启动该 turn | `codex-rs/core/src/agent/control.rs:174-179` |
| 执行限制器 | `AgentExecutionLimiter{has_capacity, guard}`；`op_starts_turn` = `UserInput` **或** `trigger_turn=true` 的 agent 通信；`is_execution_limited` 只对 V2 子 Agent 生效 | `codex-rs/core/src/agent/control/execution.rs:81-115` |
| 常驻/驱逐 | `V2Residency`：容量 = `effective_agent_max_threads`；满则 `try_unload_one_resident`（LRU 卸载）腾位，腾不出 → `CodexErr::AgentLimitReached`；`protected_thread_id` 保护当前线程不被卸载 | `codex-rs/core/src/agent/control/residency.rs:16-120` |
| 关闭 | `close_agent`："Mark `agent_id` as **explicitly closed in persisted spawn-edge state**, then shut down the agent and **any live descendants** reached from the in-memory tree." | `codex-rs/core/src/agent/control/legacy.rs:27-31` |
| 巡检 | `list_agents(path_prefix)` → `Vec<ListedAgent>` | `multi_agents_v2/list_agents.rs:22-47`；`agent/control.rs:359` |
| **缺失** | 全仓非测试代码 **零命中** `progress_timeout`；无 stall / heartbeat / 无进展检测概念 | `rg progress_timeout codex-rs`（-g '!*_test*'）= 空 |

两点关键差异：

1. **Codex 没有"卡住"检测**。它对"任务是不是卡死了"完全交给模型判断（`list_agents` 看状态 + `interrupt_agent`/`close_agent` 处置），因此不存在误杀问题，也不存在"runtime 抢跑杀掉本可完成的任务"问题。本方案的二维证据（heartbeat vs progress）是**超出参照实现的能力**——这是我们的差异化价值，但必须自己承担误判风险（EC-D2/D3 已覆盖，§14.3 B5 要求把结论表达为观测而非判决）。
2. **容量门控发生在"起 turn"这一层**，且常驻线程有 LRU 驱逐与 `AgentLimitReached` 兜底。本方案的"resume"本质上就是一次"起 turn"，因此必须过同一道门（§14.2 A6）。

### 13.5 批次 fan-out 的结果契约（最接近本方案 H1–H5）

`spawn_agents_on_csv` / `report_agent_job_result`（`codex-rs/core/src/tools/handlers/agent_jobs.rs`）：

| 维度 | 事实 | 锚点 |
| --- | --- | --- |
| 并发 | `DEFAULT_AGENT_JOB_CONCURRENCY = 16`，`MAX = 64` | `agent_jobs.rs:39-40` |
| 单项超时 | `DEFAULT_AGENT_JOB_ITEM_TIMEOUT = 30min`（**per-item**，非批次级） | `agent_jobs.rs:42` |
| 状态轮询 | `STATUS_POLL_INTERVAL = 250ms`（批内轮询，模型不可见） | `agent_jobs.rs:41` |
| 返回契约 | `{job_id, status, output_csv_path, total_items, completed_items, failed_items, job_error, failed_item_errors[]}` | `agent_jobs.rs:64-81` |
| 大产物 | 结果落 **CSV 文件**（`output_csv_path`），回执只带计数与失败摘要 | `agent_jobs.rs:45-54` |
| 子任务回传 | 子 Agent 用 `report_agent_job_result{job_id,item_id,result,stop}` 主动上报 | `agent_jobs.rs:56-62`、`:83-86` |

这是本方案 §6.2（账本）与 H1–H5（结果取回）**可以直接照抄的形状**：`job_id` ↔ obligation/批次 id；`output_csv_path` ↔ artifact；`total/completed/failed` ↔ 账本计数；`failed_item_errors[]` ↔ 失败摘要（不全量倾倒 transcript）；`report_agent_job_result` ↔ 子任务主动上报通道。§14.3 A4 据此给出修订要求。

### 13.6 逐条对照

| # | 本方案设计点 | Codex 对应事实 | 判定 |
| --- | --- | --- | --- |
| 1 | 废弃 `wait` 阻塞实现（§2 TL;DR-1） | Codex 的 wait 有界（10s–1h）+ 活动驱动 + 可被 steer 打断 + 超时即观测 | **同向，但需修正口径**：废弃对象是"无界阻塞"，不是"等待动作"；若保留 wait 工具必须补齐这三条约束（§14.2 A2） |
| 2 | turn 挂起 + 同 turn resume（I1/I2） | Codex 不做挂起，通知 `trigger_turn=false` 不唤醒父 turn | **本方案更强**；Codex 的"通知不唤醒"正是 I1 要修的洞，但本方案必须自带降级路径 |
| 3 | 二维证据（heartbeat × progress）判 stall（§6.4） | Codex 无 stall 检测 | **本方案超出参照**；风险自负，须"观测优先、判决兜底" |
| 4 | escalate-first：决策权交主 Agent（§6.3） | Codex 无运行时判决，处置动作只有 interrupt/close/resume | 同向；Codex 证明"把判决留给模型"可行，但缺审计与决策窗口 |
| 5 | `extend_deadline` 管理原语（§6.5） | Codex 无延长动作；等价物 = 再 wait 一次（新 deadline） | **同向但更规范**；Codex 的"再等一次"天然有界（每次 ≤1h），本方案的延长上限（I5）与之等价 |
| 6 | 巡检原语 `subagent_status` / `subagent_inspect_task`（§6.4） | `list_agents(path_prefix)` + 状态订阅 | 同向；建议补 `path_prefix` 式过滤与稳定 JSON 载体（§14.3 B4） |
| 7 | resume 前过并发/深度门控（§14.2 A6） | `ensure_execution_capacity_for_turn_start` + `AgentLimitReached` + residency LRU | **缺口**：本方案未写，必须补 |
| 8 | 级联取消与关闭标记（EC-C 系列） | `close_agent` = 持久化关闭标记 + 级联**活体**后代 | **缺口**：本方案未写"关闭标记持久化 + resume 尊重关闭标记" |
| 9 | 结果取回 H1–H5 | agent_jobs 的返回契约（产物路径 + 计数 + 失败摘要） | **缺口**：本方案未写，照抄形状即可 |
| 10 | 通知/digest 注入的幂等与去重（EC-E8） | `<subagent_notification>` 稳定标记 + JSON body；已知 TODO 待结构化 | 同向；建议显式定义 digest 标记与幂等键 |

### 13.7 结论

1. **可借鉴（已纳入本方案）**：有界等待的区间钳制与"超时即观测"返回契约；活动驱动（非轮询）的唤醒；巡检工具 + 稳定结构化载体；容量门控 + 常驻驱逐 + 关闭标记级联；批次结果契约（产物落盘 + 计数 + 失败摘要）。
2. **不可借鉴（本方案的差异化）**：Codex 的"通知不唤醒"会把正确性责任完全推给模型，本方案用 I1 + 挂起 + 事件驱动 resume 正面接管；Codex 的"无 stall 检测"意味着任务卡住只能靠人/模型发现，本方案用二维证据补齐，但必须付出误判防护成本。
3. **必须补的作业**：§14.2 的 6 条阻断级缺口（A1–A6）全部来自"本方案承诺了 Codex 没有承诺的东西"，因此这些作业没有外部实现可以抄，只能自己定义清楚。

---

## 14. 方案完整性审查（自查）

### 14.1 审查方法与判定标准

审查在三个维度上进行，每条缺口必须能落到"证据 + 影响 + 修订动作"三要素：

1. **不变量可执行性**：§4 的每条不变量（I1–I8）是否存在唯一、可实现、可测试的落点；
2. **与既有实现基线的一致性**：§3 的现状基线是否漏掉了**已实现但方案未引用**的能力（漏引用会导致重复造轮子或与既有行为冲突）；
3. **与参照实现的完备性对照**：本方案承诺的能力是否都有明确契约（§13 提供对照面）。

判定级别：

- **A（阻断级）**：不补齐则方案在实施时会撞上"物理不可能"或"语义自相矛盾"，必须先补后实施；
- **B（建议级）**：不补齐不阻断实施，但会在真实运行中产生噪音、成本或可观测性缺口。

### 14.2 缺口清单

#### A 类：阻断级（6 条，实施前必须补齐）

| ID | 缺口 | 证据 | 影响 | 判定 |
| --- | --- | --- | --- | --- |
| A1 | **耐久性前提缺失**：方案把"挂起态 + 账本 + 决策窗口"当作可持久化事实，但未写"持久层从哪来"。默认 store 是 **process-local、重启失忆**；host 未注入 file-backed coordinator 时 `nil` 会**禁用 background 并回退同步 wait** | `backend/internal/agent/agent.go:325-336`（注释明确默认 store 为进程内、需 host 注入 file-backed；nil 时降级） | 挂起态/账本在重启后丢失 → I1 无法自愈 → "turn 永不结束"或"义务静默丢失" | 必须新增不变量（建议 **I9**：无 durable store ⇒ 禁止挂起，回退"结束 + 新 turn 唤醒"降级路径，并显式告警） |
| A2 | **`wait` 废弃范围不完整**：方案只覆盖 `execution_mode=wait`，未覆盖 `wait_agent` / `wait_team` 工具族 | `backend/internal/toolbroker/broker.go:45`（`ToolWaitAgent`）；参数 `{after_seq,id,ids,session_id,session_ids,timeout_ms}` | 迁移不彻底：阻塞实现换了入口继续存在；或反过来误删工具造成兼容破坏 | 必须明确口径（§13.2 对照结论）：**废弃对象 = 无界阻塞**；若保留 wait 工具，须补齐"区间钳制 + 活动驱动 + 可被 steer 打断 + 超时即观测"四条约束，否则标 deprecated 由巡检替代。建议新增开放问题 Q11 拍板（**v4：Q11 已拍板为"保留并改造"，口径见 §6.9 / §16.3**） |
| A3 | **基线修正（漏引已实现能力）**：`spawn_agent` **已支持** `timeout_sec` / `progress_timeout_sec` / `approval_timeout_sec` / `cancel_grace_sec`；而 `spawn_subagents` 只有**批次级** `wait_timeout_sec` + `batch_idempotency_key`，**无 per-task deadline** | `broker.go:1617-1640`（参数）；`:3235-3243`（allowlist）；`backend/internal/agent/loop.go:6948-6960`（批次级参数） | §6.5 把"暴露 per-task deadline"写成新工作，实际是**已有能力**；真正缺口在 batch/team 与"运行中延长" | 修订 §6.5 表述：从"暴露到派发接口"改为"**扩展到 batch/team + 增加运行中延长**" |
| A4 | **结果取回链路缺失（H1–H5）**：方案只解决"唤醒与决策"，未定义终局综合如何**可靠取回交付物**（失败但有产出、大输出分页、聚合去重） | 与 `docs/plan/multi-agent-durable-lifecycle-hardening-plan-20260920.md` 的 H1–H5 直接相关；参照实现契约见 §13.5（`agent_jobs.rs:64-81`） | 挂起 turn 恢复了、决策也做了，但拿不到子任务的产物 → 终局报告降级为"凭记忆总结" | 新增一节（建议 §6.10 结果取回）：产物落 artifact + 回执带 `total/completed/failed + failed_item_errors[]`，与 H1–H5 对齐 |
| A5 | **所有权/多宿主语义缺失**：字段已存在，但方案未定义"谁有权巡检 / 延长 / resume / 取消" | `backend/internal/supervision/execution_run.go:88-89`（`OwnerID` / `OwnerLeaseUntil`）；`execution_store.go:113`、`:152`、`:517`；`execution_supervisor.go:512-518`（orphan 观测） | 多宿主/多进程下重复 resume、重复延长、双写冲突（EC-B9 已提风险但未给规则） | 在 §6.7 增补所有权规则：巡检=任意有权读；延长/取消/resume=**持有有效 lease 的 owner**；lease 失效走 EC-B9 的 fencing 路径 |
| A6 | **配额与门控未落到既有旋钮**：resume 未定义为"起 turn"的一种，因此未过并发/深度门控 | `backend/internal/agent/scheduler.go:104-217`、`:419-441`（`MaxConcurrent=4`、`MaxDepth=1`（hard/expert +1）、`EnforceSingleWriter`、`GlobalLimiter`）；`agent.go:551-584`（`shouldExposeSpawnSubagents` 的可见性门控）；参照实现 `ensure_execution_capacity_for_turn_start`（§13.4） | 挂起 turn 恢复时可能突破并发上限（子任务在挂起期间继续占用额度），或越过深度/可见性门控 | 在 §6.1/§6.6 增补：**resume 前必须重新获取容量许可**；挂起态不占额度、恢复需重新获取；超限则排队（有界），不得静默突破 |

#### B 类：建议级（6 条）

| ID | 缺口 | 证据 | 影响 | 判定 |
| --- | --- | --- | --- | --- |
| B1 | **doom-loop 交互未定义**：巡检工具属"重复调用敏感"集合，巡检轮可能被判 doom loop | `backend/internal/agent/doom_loop.go:178-184`（`wait_agent` / `read_agent_events` / `list_agents` / `task_output` / `background_task`） | 主 Agent 正常巡检被误判为死循环 → 巡检能力实际不可用 | 判罚应针对"无进展的重复调用"（参数与读数均未变化）而非"多次巡检"；巡检工具纳入豁免或提高阈值（**v4：已落 §6.4 doom-loop 豁免规则，升为必需项**） |
| B2 | **审批路由与权限继承未定义**：父挂起时子任务的审批在哪呈现、超时怎么算 | `resolve_agent_approval` 已存在；EC-C4/EC-G3 只提到风险 | 子任务等审批、父挂起，用户看不到请求 → 双双卡死 | 明确：审批在子会话呈现 + `ApprovalDeadlineAt` 兜底 + 父挂起不因审批失去决策权（已在 EC-G3 半覆盖；**v5：已落 §6.14**） |
| B3 | **保留/GC 未定义**：终态 obligation 保留多久、被驱逐后如何巡检 | 参照实现 `V2Residency`（LRU 卸载 + `AgentLimitReached`，§13.4） | 长挂起 turn 的账本无界增长；或过早 GC 导致终局综合取不到证据 | 定义保留窗口 + 冷存储降级 + 驱逐后仍可从持久层重建状态（见 EC-H5）（**v4：已落 §6.12**） |
| B4 | **通知载体契约未定义**：digest/通知的标记、幂等键未写死 | 参照实现 `<subagent_notification>{...}`（§13.3）；本仓 EC-E8 已提"delivered/seen" | 重复注入、模型误解析、去重无依据 | 定义稳定标记 + JSON body + 幂等键（`obligation_id + terminal_state`），显式写进 §6.6（**v4：已落 §6.6 幂等键**） |
| B5 | **观测结果的返回契约未统一**：超时/stall/cancel 的返回形态未规定"成功观测 + 明确 next_action" | 参照实现 `WaitAgentResult{message, timed_out}` 且 `success_for_logging=true`（§13.2） | 模型把正常观测当失败 → 触发无意义重试 | 统一契约：观测类返回一律成功 + 携带 `next_action`；错误只用于调用非法（参数越界等）（**v4：已落 §6.4**） |
| B6 | **用户 steer 与 resume 的交互未定义**：EC-A1/Q1 有方向但未与"resume episode 熔断"联动 | 参照实现 `Steered` 结局（§13.2） | 用户输入到达时，正在跑的 resume episode 被硬截断或与之竞争 | 明确：steer 立即结束当前等待/episode 并把用户输入置顶；未完成 obligation 保留，账本不因 steer 变化（**v5：已落 §6.15**） |

### 14.3 修订清单（落到章节，最小改动）

> 状态（v5）：下列 12 项**已全部落入正文**（章节见右列）；B2/B6 于 v5 补落 §6.14 / §6.15，**B1–B6 至此全部落正文**。本表保留为变更索引；实施准入条件见 §8 的 P0-前置。

| # | 修订动作 | 目标章节 |
| --- | --- | --- |
| 1 | 新增不变量 **I9**（无 durable store ⇒ 禁止挂起，回退降级路径 + 告警） | §4 |
| 2 | 现状基线补写：`spawn_agent` 已有 4 个 timeout 参数；batch 仅批次级 | §3.5 / §3.6 |
| 3 | §6.5 表述修正为"扩展到 batch/team + 运行中延长" | §6.5 |
| 4 | 新增 §6.10「结果取回」：artifact + 计数 + 失败摘要（对齐 H1–H5 与 §13.5 契约） | §6.10（新增） |
| 5 | resume 前容量门控（过 limiter / 深度 / 可见性），挂起态不占额度 | §6.1 步骤 5 |
| 6 | 所有权与 lease 规则（巡检/延长/取消/resume 的授权边界） | §6.11 |
| 7 | `wait` 废弃口径修正 + wait 工具族处置（**Q11 已拍板：保留并改造**，按 §16.3 四条约束） | §6.9 / §16.3 |
| 8 | 观测返回契约统一（成功观测 + `next_action`） | §6.4 / §6.5 |
| 9 | 通知/digest 载体与幂等键写死 | §6.6 |
| 10 | doom-loop 豁免规则 | §6.4 / §9 EC-D 组 |
| 11 | 保留/GC 与"驱逐后可重建" | §6.12 |
| 12 | 新增开放问题 Q11（wait 工具族处置）、Q12（保留窗口与 GC 策略） | §11 |

### 14.4 复审结论

- **主体成立**：§4 的目标语义与不变量、§6 的"runtime 感知 / 主 Agent 判断"分工、§6.3 的 escalate-first、§6.4 的二维证据巡检，在逻辑上是自洽的，且与参照实现（§13）同向——Codex 用"有界等待 + 巡检 + 关闭/中断"证明"把判决权留给模型"可行，本方案在此之上补齐了审计、决策窗口与"turn 覆盖子任务"的强不变量。
- **但未达"可直接实施"**：A1–A6 六条阻断级缺口全部属于"本方案承诺了参照实现没有承诺的东西，因此没有现成实现可抄"——尤其是 A1（耐久性前提）与 A6（resume 门控），不补齐会在实施第一天就撞墙。
- **实施准入条件（DoD）**：A1–A6 全部落到文档章节（§14.3 的 1–7 项）后，方可进入 P0；B1–B6 允许随 P2/P3 落地，但 B1（doom-loop）建议提前到 P2，否则巡检能力上线即被误判。
- **对用户原始判断的校准**：用户指出"`wait` 会堵塞进程、应废弃"——审查支持这一判断的**实质**（无界阻塞必须废弃），但对照 §13.2 需要精确化：**废弃的是无界阻塞的实现，而不是"等待/巡检"这一语义**。Codex 的 wait 之所以不堵死进程，是因为它同时满足"区间钳制 + 活动驱动 + 可被打断 + 超时即观测"。本方案的"挂起 + 事件驱动 resume"是更彻底的解（连一次 30s 占用都不要），代价是必须自带 A1/A6 两项作业。
- **v4 更新（准入条件已落文档）**：A1–A6 与 B1–B6 的修订已全部落入正文（§3.5/§3.6 基线修正与 G9–G13、§4.2 I9/I10、§6.1 步骤 5、§6.4 返回契约与 doom-loop 豁免、§6.6 幂等键、§6.9 wait 口径、§6.10–§6.13、§7 改动 #11–#16、§8 P0-前置与验收、§15 EC-I）；文档侧准入条件已满足，剩余是**实现**。

---

## 15. 追加边界场景（EC-H / EC-I / EC-J，21 条）

> 来源：§13 参照实现对照 + §14 完整性审查衍生 + §16 控制流定稿 + §6.14/§6.15（v5）。与 §9 的 A–G 组同格式；总数由 43 条增至 64 条。

### 15.1 场景表

| ID | 场景 | 风险 | 期望行为 / 落地要点 |
| --- | --- | --- | --- |
| EC-H1 | resume 时超过并发/深度门控（`MaxConcurrent=4`，`MaxDepth=1`） | 挂起态绕过 limiter → 恢复即超限；或永久排队变相死锁 | resume 视为"起 turn"，**先获取容量许可再恢复**；超限进入有界 FIFO 等待并让 digest 显示排队位次；等待超时走 escalate（§14.2 A6） |
| EC-H2 | 完成通知到达时父 turn 已进入终态（I1 被违反后的自愈入口） | 义务静默丢失 | 与 EC-E6 合并处理：重新进入挂起；无法恢复则强制 orphaned + 告警，绝不静默成功（§13.3 的"通知不唤醒"是反例，不得照抄） |
| EC-H3 | 等待/巡检期间用户 steer（新输入到达） | 用户输入被挂起 turn 挡住或与之竞争 | 立即结束当前等待段并把用户输入置顶（对照 `Steered` 结局）；未完成 obligation 保留、账本不变；steer 不计入 stall/延长计数 |
| EC-H4 | `wait`/巡检的 `timeout_ms` 越界（< min 或 > max） | 静默钳制 → 模型以为等了 2h 实际 30s，决策依据失真 | 返回**模型可见**的非法参数错误并附合法区间（对照 `RespondToModel`）；禁止静默截断（§14.3 #7/#8） |
| EC-H5 | 巡检时子线程已被常驻驱逐（LRU / 进程重启） | 巡检报"未知任务"，主 Agent 失去判断依据 | 巡检必须能从持久层重建最小证据集（state / heartbeat / progress / 当前工具 / deadline）；驱逐只影响 transcript 级细节（走 artifact），不得影响状态判定 |
| EC-H6 | 已持久化"关闭标记"的 agent 被 resume | 复活已关闭子任务，产生幽灵运行 | 以关闭标记为准，返回 `already_closed`（幂等）；关闭标记随 spawn-edge 持久化、级联只作用于**活体**后代（对照 `close_agent` 语义） |
| EC-H7 | 深度上限触发（非 hard/expert 的嵌套派发被拒） | 主 Agent 收到静默失败，误以为任务已派发 | 派发被拒必须返回可行动回执（降级为同层子任务 / 自己执行 / 提升难度档），并计入审计（`shouldExposeSpawnSubagents` 门控失败同理） |
| EC-H8 | 批次产物缺失（artifact/输出文件未生成或写入失败） | 终局综合被"取不到产物"卡住 → 账本无法清空 | 降级契约：以"计数 + 失败摘要"完成综合，产物缺失记为 `partial_result`，不阻塞账本清空（对齐 §13.5 的 `job_error` / `failed_item_errors[]`） |
| EC-H9 | 嵌套托管 × 容量归属（子 Agent 自己也是父，孙任务占额度） | 子挂起时额度是否释放无定义 → 死锁或超限 | 定义额度归属：**按活跃 turn 计**，挂起不占额度、恢复需重新获取；孙任务额度记在直接父的配额账下（与 EC-E3"只唤醒直接父"一致） |
| EC-H10 | 巡检轮被 doom-loop 判定（连续多次巡检调用） | 巡检能力上线即不可用 | 判罚针对"无进展的重复调用"（参数与读数均未变化）而非调用次数；巡检工具纳入豁免名单或提高阈值（§14.2 B1） |
| EC-H11 | 同一终态事件重复投递（事件重放 / 宿主重启后重放） | digest 重复计入、重复唤醒、账本计数漂移 | 幂等键 = `obligation_id + terminal_state`；重复投递只更新 `last_seen_at`，不改变计数、不重复排 resume |
| EC-H12 | turn 级 hard cap（建议 24h）到期，仍有未完成 obligation | 静默截断 → 用户以为还在跑 | 到期前一个决策窗口必须产生 **critical 决策上报**（延长 / cancel / orphaned 三选一）；无响应则强制 orphaned + 告警；禁止静默结束（EC-E1 的补充） |
| EC-I1 | active wait 期间父 turn 达到墙钟/步数上限 | 截断时账本未清 → 静默丢账本 | 先转挂起再收尾（不丢账本）；episode 截断只结束"这一段"，digest 非空则自动续跑（§16.2） |
| EC-I2 | 父 Agent 连续 active wait 无进展（"忙等"空转） | 烧 token；或与 EC-H10 同源被误判 doom loop | 按"无进展重复"计数（参数与读数均未变）；连续 2 次无进展 ⇒ `next_action=suspend` 强制建议，**不直接判罚**（§6.4 / §16.2） |
| EC-I3 | `wait_agent` 返回 `pending_count>0` 而模型直接收尾 | I1 被违反，义务静默丢失 | I1 拦截 + 自动挂起 + 告警（自愈路径，同 EC-E6） |
| EC-I4 | 等待窗口内同一 obligation 多次转终态（retry / 重派） | `terminal_delta` 重复计数、重复汇报 | 与 EC-H11 同源：以最后一次为准，按 `terminal_epoch` 去重（§6.6 幂等键） |
| EC-I5 | active wait 期间用户 steer | 等待段与用户输入竞争 | 立即结束等待段并置顶用户输入；账本不变、不计 stall（同 EC-H3） |
| EC-J1 | 子任务 `waiting_approval` 且父已挂起 | 审批请求被预算裁剪 → 父不醒、子卡死（双双卡死） | 审批请求走**独立 resume 通道**（不受 progress / auto-wake 预算），且投影为 `SeverityCritical + action_required`（§6.14 规则 1–2） |
| EC-J2 | 审批 token 过期（子 run 被中断/替换，`ErrAgentRunSuperseded`） | 父以为审批已生效，实际未生效 | 回执走"成功观测 + `next_action`"（inspect / finalize）；请求与解决共用同一事件类型、原地更新不留 stale 行（§6.14 规则 6） |
| EC-J3 | 父挂起态收到用户 steer（含 `interrupt=true`） | 语义不明：挂起态没有可中断的 episode | 起一段新 resume episode（同 `turn_id`），用户输入置顶；账本不变、不计 stall（§6.15） |
| EC-J4 | steer 与审批同时到达 | 用户指令绕过审批闸门 | **审批优先**：先 `resolve_agent_approval`，steer 在审批解决后注入（§6.15） |

### 15.2 参照实现锚点速查（Codex）

| 主题 | 锚点（相对 `E:\projects\ai\codex`） |
| --- | --- |
| 工具枚举 | `codex-rs/protocol/src/items.rs:248-256`；`app-server-protocol/src/protocol/v2/item.rs:1028-1033` |
| V1 工具实现 | `codex-rs/core/src/tools/handlers/multi_agents/{spawn,wait,send_input,resume_agent,close_agent}.rs` |
| V2 工具实现（含巡检） | `codex-rs/core/src/tools/handlers/multi_agents_v2/{spawn,wait,list_agents,send_message,message_tool,followup_task,interrupt_agent}.rs` |
| wait 语义（有界/活动驱动/可打断/超时即观测） | `multi_agents_v2/wait.rs:50-66`、`:127-131`、`:133-160`、`:171-196` |
| wait 区间常量与硬上限 | `codex-rs/core/src/config/mod.rs:206-210`、`:265-267`、`:1186-1198` |
| 状态机与 `is_final` | `codex-rs/core/src/agent/status.rs:4-28` |
| 完成通知（不唤醒父 turn） | `codex-rs/core/src/agent/control.rs:441-517`；`codex-rs/core/src/codex_thread.rs:445-451`；`core/src/session/handlers.rs:289-301` |
| 通知载体格式 | `codex-rs/core/src/context/subagent_notification.rs:20-41`；`core/src/session_prefix.rs:16-25` |
| 容量门控 | `codex-rs/core/src/agent/control/execution.rs:81-115`；`core/src/agent/control.rs:174-179` |
| 常驻/驱逐/上限错误 | `codex-rs/core/src/agent/control/residency.rs:16-120` |
| 关闭（持久化标记 + 级联活体后代） | `codex-rs/core/src/agent/control/legacy.rs:27-31` |
| 巡检 `list_agents` | `multi_agents_v2/list_agents.rs:22-47`；`core/src/agent/control.rs:359` |
| 批次结果契约（最接近 H1–H5） | `codex-rs/core/src/tools/handlers/agent_jobs.rs:39-86`、`:45-54`、`:64-81` |
| 默认配额 | `codex-rs/core/src/config/mod.rs:206-210`、`:268` |

---

## 16. 控制流定稿：主 Agent 边等边干 + 全终态 join + `wait_agent` 改造

> 来源：2026-09-23 用户对整体控制流的确认请求——"创建子 Agent 后进入等待状态，期间主 Agent 除了等子 Agent 也可以自己执行任务、巡检子 Agent 或等待子 Agent 的事件；只有所有 Agent 都结束才能继续执行；扩展/优化原来的 `wait_agent`；不管是异常还是正常结束，覆盖所有 Agent。"
>
> 本节把该模型固化为实施口径。**结论：整体逻辑成立，但需三处精确化**（16.1），并据此定稿 `wait_agent` 的保留与改造（16.3，对应 Q11 拍板）。

### 16.1 模型确认与三处精确化

**成立的部分**：

- ✅ 派发子 Agent 后，父 Agent 处于"等待期"，但**等待期不等于不能干活**——父可以继续执行自己的工具调用（读文件、跑命令、写代码）、巡检子 Agent、或以有界调用等子 Agent 的事件；
- ✅ 子任务全部结束后才能"继续执行"（收尾、汇总、进入下一阶段）；
- ✅ `wait_agent` 应**扩展/优化**而不是删除；
- ✅ 终止判定必须覆盖**异常与正常两种结局**，不能只等"成功"。

**精确化 1：等待是 turn 级状态，不是 actor 级阻塞。**

"等待"在运行时里体现为两件事，而不是"父进程卡住"：

1. **收尾门（强制）**：账本存在非终态 obligation ⇒ 父 turn **不得收尾**（I1）。这是唯一被强制的部分。
2. **资源态（可选）**：父**主动选择**不再干活时，可把 turn 置为挂起（`awaiting_obligations`，零 goroutine / 零 token），由事件驱动 resume。父也可以选择**不挂起**，继续占用 actor 边干活边等。

即：**受限的只是"收尾"，不是"执行"**。当前实现的错误不是"有等待"，而是把等待实现成了"父 turn 被内联阻塞"（`loop.go:2865-2896`）——占着 goroutine、占着墙钟、且期间父无法做任何自己的事。

**精确化 2："所有 Agent 结束才能继续执行" → 精确为"账本全终态，turn 才允许收尾（终局综合 → completed）"。**

- 父**自己的**执行不受限：等待期照常干活、巡检、等待事件；
- "继续执行"的出口不止一个：父可以**取消/放弃**某个子任务——但放弃不是绕过门禁，而是必须先把该 obligation **推到终态**（`canceled` / `abandoned`）再收尾；
- 用户中断（ESC）走独立路径：级联取消账本 → 全部进入 `canceled` → turn 收尾。

**精确化 3："结束"必须是可判定的，不能只是"等它们自己结束"。**

如果某个子任务永远不结束（进程死、宿主重启、僵死），join 就会永久挂起。因此必须满足：

- 每个 obligation 必有 `deadline_at`（I2：声明或默认，派发即拒绝缺失）；
- 到期由 supervisor 执行强制分支 → **强制判终态**（`timed_out` / `orphaned`）；
- watchdog 周期扫描"无终态且 deadline 已过/丢失"的 run，兜底判终态并记 `CancelSource`；
- 强制判终态**必须投影** `SeverityCritical + action_required`（不得静默），否则父无法区分"子任务死了"和"还在跑"。

→ 这三条构成**新增不变量 I10（join 可判定性）**：`∀ obligation，其终态在有限时间内可达`。与 §14.3 修订项 1 的 I9（耐久性前提）并列，待 A1/A2 修复时一并落入 §4.2。

### 16.2 两种等待形态（active wait / passive wait）

| 形态 | 触发 | 资源占用 | 载体 | 适用 |
| --- | --- | --- | --- | --- |
| **active wait**（有界主动等待） | 父显式调用改造后的 `wait_agent(timeout_ms)` | 父 turn 保持 `running`（占会话并发位、烧 token） | 工具调用内联，活动驱动，可被 steer 打断 | 父还有自己的活要干、想尽快拿到首批事件 |
| **passive wait**（挂起） | 父无事可做，或试图收尾且账本非空 | 零 goroutine / 零 token，不占墙钟预算 | 持久化挂起态 + 事件驱动 resume（同 `turn_id`） | 父已无自己的活，纯等 |

**切换规则**：

- 两者可自由往返：`running` --（无事可做/试图收尾）--> `awaiting_obligations` --（terminal/progress/deadline 事件）--> `running`；
- runtime **不强制**父立刻挂起（派发后父可以先干自己的活）；
- 但 runtime **强制**"账本非空不得收尾"——模型想结束也结束不了，只能挂起或继续；
- active wait 期间父会话仍 `Busy()` ⇒ 用户消息按 Q1 排队；passive wait 不 `Busy()` ⇒ 用户可插话（Q10）。

**成本口径**：active wait 是"用 token/并发位换低延迟"；passive wait 是"用一次挂起/恢复换零成本长等待"。默认策略建议：`wait_agent` 单次 ≤ 60s，连续 active wait 超过 2 次且无进展 ⇒ 提示模型改用挂起（`next_action=suspend`）。

### 16.3 `wait_agent` 改造定稿（Q11 拍板：保留并改造）

**保留工具，语义重写**（对齐 §13.2 参照实现的四条约束，缺一不可）：

| # | 约束 | 具体口径 | 反例（现状/错误实现） |
| --- | --- | --- | --- |
| 1 | **区间钳制** | min 10s / default 30s / max 1h；越界返回**模型可见错误**，不静默截断 | 无界或静默 clamp |
| 2 | **活动驱动** | 挂在"账本事件 + 输入队列活动"上，事件到达立即返回；**禁止轮询** | 定时 sleep 轮询 |
| 3 | **可被打断** | 用户 steer / ESC / 新输入立即结束等待段并返回 | 等待期间用户输入被吞 |
| 4 | **超时即观测** | `timed_out=true` 是**成功**返回（附账本摘要 + `next_action`），不是错误、不是任务失败 | 超时抛错或超时即取消子任务 |

**返回契约**（与 B5 统一：成功观测 + `next_action`）：

```
{
  waited_ms, timed_out,
  obligations: [{ id, subject, state, terminal, progress_seq, progress_age, stall_verdict }],
  terminal_delta: [...],        // 本次等待期间新转终态的（含异常终态与原因）
  pending_count, terminal_count,
  next_action: continue_wait | inspect | finalize | suspend,
  digest_ref / artifact_id      // 超字节上限时走归档（I7）
}
```

**语义边界（必须写进工具描述）**：

- `timed_out` **不等于** turn 结束，也不等于子任务失败——它只是"观测窗口到期"；
- 返回后若 `pending_count == 0` ⇒ 允许收尾；否则父**不得**收尾（I1 拦截并自动转入挂起）；
- `wait_agent` 自身**不触发挂起**；挂起由"无事可做 / 试图收尾"触发（16.2）；
- 空账本调用 ⇒ 立即返回 `next_action=finalize`，**不空等**；
- `wait_team` 同口径（batch/team 视角），两者共享实现与返回契约。

**兼容**：保留读取旧参数 `{after_seq,id,ids,session_id,session_ids,timeout_ms}`，但 `timeout_ms` 语义按上表改造（新增钳制与活动驱动）；旧的"无界/默认长超时 + 内联阻塞"行为取消。迁移窗口见 Q6。

### 16.4 全终态 join：终态枚举与"异常也必须判终态"

**终态枚举（必须穷尽，否则 join 会挂）**：

| 类别 | 终态 | 来源 |
| --- | --- | --- |
| 正常 | `completed` | 子任务正常结束 |
| 正常（部分） | `completed_with_failures` | 批次部分失败（对齐 §13.5 的 `agent_jobs` 契约：`status` + 计数 + 失败摘要） |
| 异常 | `failed` | 运行错误 / 不可恢复异常 |
| 异常 | `canceled` | 父/用户取消（含 ESC 级联） |
| 异常 | `timed_out` | `ExecutionDeadlineAt` 到期强制取消 |
| 异常 | `orphaned` | 宿主重启/失联且不可恢复（P4 回收落地前先强制判终态 + 告警） |
| 异常 | `rejected` / `superseded` | 未真正启动 / 被重派替代 |
| 异常 | `abandoned`（建议新增） | 父主动放弃（`close` / `reassign` 后） |

**三条保障（对应 I10）**：

1. **可判定**：`deadline_at` 必填（I2）⇒ 到期必有强制分支；
2. **可兜底**：watchdog 扫描无终态 run ⇒ 强制判终态 + 记 `CancelSource`；
3. **可观测**：强制判终态必投影 `SeverityCritical + action_required` ⇒ 父收到 digest 后决策（这正是 §6.3 escalate-first 的复用，无需新机制）。

**join 判定**：

```
pending = count(obligations where turn_id = ? and state not terminal)
pending == 0  ⇒  允许收尾（终局综合 → turn.completed）
```

**quorum=all 是默认且不可配置放宽**；唯一合法"提前收尾"路径是先把未完成项**变成**终态（cancel / abandon），而不是忽略它们。这条正是"不管是异常还是正常结束，所有 Agent 都被覆盖"的落地形式。

### 16.5 与既有章节的修订关系

| 章节 | 修订内容 | 状态 |
| --- | --- | --- |
| §2 结论摘要 | 新增第 9 条（等待是 turn 级状态 / quorum=all / 终态可达） | 已改 |
| §4.2 不变量 | I1 精确化（非终态 obligation ⇒ 禁止收尾）+ **新增 I9（耐久性前提）/ I10（join 可判定性）** | 已改（v4） |
| §5.1 状态机 | 补充"`turn.running` 期间父可巡检/干活/有界等待；挂起是资源态而非强制步骤" | 已改 |
| §6.1 步骤 3 | "派发后立即挂起" → "派发后父继续运行；无事可做或试图收尾时挂起" | 已改 |
| §6.9 | `wait` 口径：废弃无界阻塞实现，**保留工具与语义** | 已改（v4） |
| §11 Q11 | 拍板：保留并改造 | 已改 |
| §14.2 A2 / §14.3 项 7 | 修复口径同步为"保留 + 四条约束" | 已改 |
| §14.2 B1 | doom-loop 豁免升为**必需项**（active wait 会高频调用 `wait_agent`） | 建议提前到 P2 |

**EC-I 5 条已全部落入 §15**（active wait 撞上限 / 忙等空转 / 无视 pending 直接收尾 / 终态去重 / 等待中被 steer）；v5 另增 EC-J 4 条（审批与 steer 交互，见 §15）。
