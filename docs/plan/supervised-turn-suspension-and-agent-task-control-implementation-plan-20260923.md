# 监督式 turn 挂起与 Agent 任务控制 · 执行方案（2026-09-23）

> **上游设计稿**：`docs/plan/supervised-turn-suspension-and-agent-task-control-plan-20260923.md`（v5，下称"设计稿"）
> **本文件定位**：施工单。只回答四件事——**改哪里 / 改成什么 / 怎么验收 / 按什么顺序与怎么退**。设计论证、64 条边界场景（§9/§15）、参照实现对照（§13）见设计稿，不在此重复。
> **状态**：待执行（当前未改动任何代码）。与设计稿冲突时**以设计稿为准**；本文件新增的分期归属（#13/#15/#16 的落位）与验收编号（`AC-*`）为施工约定。
>
> **修订记录**：
> - 2026-09-23（v1）：初稿。按设计稿 §7/§8 拆出 P0-前置 / P0 / P1 / P2 / P3 施工单；给出 18 项改动 + A6 / 重启恢复 / 兼容补位项的"需要调整的内容"与 `AC-*` 验收编号；#13/#15/#16 的分期归属由本文件补位；§7 给出**实测存在**的测试落点；§12 记录锚点核验结果（8 处修正）。

## 0. 执行总览

### 0.1 交付目标

把"父 Agent 派发子任务后**只能阻塞等待**"改造为"**turn 级挂起 + 同轮 resume + 全终态 join**"，并让主 Agent 在等待期间保留巡检、管理与决策权：

1. **决策权回归**：软阈值 stall 不再自动 cancel，改为 escalate-first（判定 → 上报 → 决策 → 兜底）；
2. **turn 不结束**：挂起是资源态（零 goroutine / 零 token），resume 复用**同一 `turn_id`**（I3）；
3. **全终态 join**：`pending_count == 0` 才允许收尾，quorum=all，异常终态同样计入（I1 + §16.4）；
4. **可判定**：`deadline_at` 必填（I2）+ watchdog 兜底（I10），异常也必须能判终态；
5. **可观测**：巡检 / 等待工具统一"成功观测 + `next_action`"契约（B5），观测类失败不表现为任务失败。

### 0.2 阶段总表

| 阶段 | 目标 | 改动项（设计稿 §7） | 进入条件 | 退出条件（DoD） | 回滚点 |
| --- | --- | --- | --- | --- | --- |
| **P0-前置** | 地基与准入 | #11、#12 | 设计稿 §14.4 复审通过；准入检查（§1.1）全绿 | I9 探测与 I10 watchdog 有单测；降级路径不产生挂起事件 | 关 `suspension_enabled` 即回现状 |
| **P0** | 决策权回归 | #1、#2、#3（字段部分）、#8 | P0-前置 DoD | 软阈值不再自动 cancel；`extend_deadline` 可用且有上限/审计 | 关 `escalate_first=false` 恢复原强制分支 |
| **P1** | 挂起与同轮 resume | #4、#5、#6、#15、#16（取回字段部分） | P0 DoD + O1/O2 已落地 | 3 个 background 子任务全终态后**同 `turn_id`** resume；账本非空时收尾被拦截 | `execution_mode=legacy` 走旧同步路径 |
| **P2** | 巡检与管理闭环 | #7、#9、#10、#13、#14、#17、#18 | P1 DoD | 巡检/等待工具可用且不被误判；审批与 steer 能唤醒父 | 下架巡检工具 / 保留旧 `wait` 语义 |
| **P3** | 收敛与清理 | #5（移除）、#16（GC/保留）、重启恢复 | P2 DoD + 兼容期结束 | 阻塞分支删除；重启恢复/围栏有测试；全量回归通过 | 保留 deprecated 参数读取，不做硬删除 |

> **分期补充说明（本执行方案对设计稿 §8 的补位）**：设计稿 §8 未给 #13/#15/#16 指定阶段，按其依赖关系落位——#15（幂等键）与 #4 同批进 P1（否则重复投递造成 resume 风暴）；#16 拆为"结果取回字段（P1，A4/H1–H5）"与"GC/保留（P3）"；#13（`wait_agent` 改造）进 P2，与 §8 P2 交付物"改造后的 `wait_agent` 可用"一致。

### 0.3 三条硬顺序约束（不可交换）

| # | 约束 | 依据 | 违反后果 |
| --- | --- | --- | --- |
| **O1** | **I9 探测（#11）必须先于 P1** | §6.13 / I9 | 无 durable store 的会话进入挂起 ⇒ **静默失忆**：重启后 turn 永不 resume |
| **O2** | **I10 watchdog（#12）必须先于 P1** | §16.4 / I10 | 丢失终态的子任务 ⇒ join 永久挂起，父 turn **永不收尾** |
| **O3** | **doom-loop 豁免（#14）必须在 active wait 上线前**（P2 首批） | §6.4 / B1 | 巡检与 `wait_agent` 高频调用被误判为死循环，能力上线即被禁用 |

> 软顺序：`#3 字段迁移`（至少 `TurnID`）必须早于 `#4 resume`；`#15 幂等键`必须与 `#4` 同批；`#8 配置项`先于 `#1` 的宽限期实现。

### 0.4 施工纪律

- 每个改动项**独立提交**，提交信息带改动项编号（如 `feat(supervision): #1 escalate-first`），便于单点回滚；
- 新增字段一律在 `WithDefaults` 保持**零值向后兼容**（设计稿 §7 兼容性约束）；
- 任何**变更类**动作必须带 `reason` 并写审计（I4）；账本写入一律走 **CAS + Version**（`UpdateExecutionRunCAS`），禁止裸更新；
- 新增的"观测类"工具一律遵守"成功观测 + `next_action`"，**不得**把观测失败表现为工具失败（B5）；
- 每阶段结束跑一次阶段验收（§6.3）并留痕，不通过不进下一阶段。

---

## 1. P0-前置：准入与地基（必须先于 P1）

### 1.1 准入检查清单（施工前一次性确认）

| ID | 检查内容 | 方法 | 通过标准 |
| --- | --- | --- | --- |
| C0-A | 设计稿缺口 A1–A6 / B1–B6 是否已全部落正文 | 读设计稿 §14.2 | 全部"已落"（v5 状态） |
| C0-B | 基线可构建 | `go build ./...`（`backend/`） | 零错误 |
| C0-C | 基线测试为绿 | `go test ./internal/supervision/... ./internal/toolbroker/...` | 全绿，存档为回归基线 |
| C0-D | 现有开关盘点 | grep `execution_mode` / `AutoWake` / `wake` / `suspension` | 记录现有开关名，供回滚使用 |
| C0-E | 度量基线采集 | 设计稿 §8 五项指标取当前值 | 有基线数字，否则收益不可验证 |

> C0-E 落地与首次采样见 §13.14（2026-09-23）：五项指标的口径已提升为生产代码单一来源，采样入口 `GET /supervision/metrics` 与 `go run ./cmd/supervision-metrics -store <supervision.db>` 同源。

### 1.2 施工项

#### C0-1（= 改动 #11）耐久性探测与降级路径（I9 / §6.13）

**需要调整的内容**

| 位置 | 调整 |
| --- | --- |
| `backend/internal/agent/agent.go:325-336`（`GetSubagentBatchCoordinator` 的 doc，明确“进程内 store 不跨重启、须降级同步路径”）、`:551-584`（**工具面门控** `shouldExposeSpawnSubagents`，**不是**耐久性探测点） | 新增 `supportsSuspension()`：探测 `store != nil && store.IsDurable()`；与工具面门控**同层放置但用途不同**（可见性 ≠ 耐久性） |
| 派发路径（工具注册 + 工具描述） | 探测不满足 ⇒ **不返回 batch handle 语义**、**不写 `awaiting_obligations`**，按 legacy 同步路径执行 |
| 投影 | 降级时投影**一次** `SeverityWarning`（不刷屏）；工具描述明确"当前会话不支持托管挂起" |
| 挂起态持久化前置 | 探测满足 ⇒ 挂起态 `{turn_id, session_id, obligation_ids, parked_at, decision_window_until, resume_queue}` 一并落盘（§6.12） |

**不改什么**：legacy 同步路径的既有行为；`shouldExposeSpawnSubagents` 的可见性口径。

**验收标准**

| AC | 断言 | 层级 |
| --- | --- | --- |
| AC-C0-1a | `store = nil` ⇒ 不进入挂起，走 legacy 路径 | L1 单测 |
| AC-C0-1b | 进程内 store（非 durable）⇒ 同上，且投影**恰好 1 条** `SeverityWarning` | L1 单测 |
| AC-C0-1c | durable store ⇒ 允许挂起，挂起态记录**确实落盘**（重开 store 后可读回） | L2 集成 |
| AC-C0-1d | 两条降级路径下均**不出现** `turn.suspended` 事件 | L2 集成 |

#### C0-2（= 改动 #12）join 可判定性 watchdog（I10 / §16.4）

**需要调整的内容**

| 位置 | 调整 |
| --- | --- |
| `backend/internal/supervision/execution_supervisor.go`（真实入口：`ScanOnce:287`、`RunLoop:317`、`evaluateRun:465`、`projectTerminal:624`） | 新增扫描：**无终态 且 `deadline_at` 已过或丢失** ⇒ 强制判终态 + 记 `CancelSource` |
| 投影 | 强制判终态**必投影** `SeverityCritical + action_required`（复用 `projection.go:87-103` 的排 wake 判定，无需新机制） |
| 终态枚举 | 覆盖 §16.4 全表：`completed` / `completed_with_failures` / `failed` / `canceled` / `timed_out` / `orphaned` / `rejected` / `superseded` / `abandoned` |
| 硬阈值 | `ExecutionDeadlineAt` 到期 ⇒ 直接执行强制分支（不接受决策延迟），但仍投影通知 |

**验收标准**

| AC | 断言 | 层级 |
| --- | --- | --- |
| AC-C0-2a | 构造"deadline 已过且无终态"的 run ⇒ watchdog 在 ≤1 个扫描周期内判终态 | L1 单测（假时钟） |
| AC-C0-2b | 强制判终态必产生 1 条 critical + action_required 投影，且 `CancelSource` 被写入 | L1 单测 |
| AC-C0-2c | §16.4 终态枚举**每一条**都能被 join 判为终态（表驱动，无遗漏） | L1 单测 |
| AC-C0-2d | 硬阈值到期与 watchdog 兜底**不重复**判终态（幂等） | L1 单测 |
| AC-C0-2e | 缺 `deadline_at` 的派发被拒绝（I2 前置校验） | L1 单测 |

---

## 2. P0：决策权回归（最小闭环）

> 目标：软阈值 stall 不再自动 cancel；主 Agent 在决策宽限期内可 `extend_deadline` 或 `cancel`；宽限期后 runtime 兜底。

#### C1-1（= 改动 #1）escalate-first：软/硬双阈值 + 决策宽限期

**需要调整的内容**

| 位置 | 调整 |
| --- | --- |
| `backend/internal/supervision/execution_supervisor.go:503-553`（`evaluateRun` 起于 465；503 起为 `else` 分支；`progress_stalled` 判定 508-510；强制取消路径 526-553，`ActionTaken="cancel_requested"` 在 538/542） | 软阈值分支由"判定 → 执行"改为"判定 → 上报 → 决策 → 兜底"：`progress_stalled` 的 `ActionTaken` 由 `cancel_requested` 改为 **`escalated`**，投影 `SeverityCritical + action_required`，**不 cancel** |
| 同上 | 新增**决策宽限期**（`DecisionWindowUntil`）：到期且无决策 ⇒ 执行原强制分支（`RequestExecutionCancel` → `InterruptRun`），`CancelSource = "decision_window_expired"` |
| 阶梯口径 | 静默（`progress_age < 软阈值`）/ 搭车（`软阈值 ≤ age < 2×软阈值`）/ **上报（`age ≥ 2×软阈值` 或 heartbeat 失效）** / 决策 / 兜底 / 硬阈值 |
| `orphan_suspected` | **维持 observe-only**（`execution_supervisor.go:512-521`），本阶段不接入决策 |

**不改什么**：`ExecutionDeadlineAt`（硬阈值）语义——到期即执行，不接受决策延迟；`SupervisionTimedOut` 的既有路径。

**验收标准**

| AC | 断言 | 层级 | 对应设计稿 |
| --- | --- | --- | --- |
| AC-P0-1a | 构造 `progress_stalled` ⇒ **不出现** `cancel_requested`，出现 `escalated` + critical 通知 | L1/L2 | §8 P0-1 |
| AC-P0-1b | 宽限期内调用 `extend_deadline` ⇒ `ProgressDeadlineAt` 前移、`ExtensionCount+1`、无 cancel | L2 | §8 P0-2 |
| AC-P0-1c | 宽限期结束无决策 ⇒ 兜底执行且 `CancelSource="decision_window_expired"` | L1 | §8 P0-3 |
| AC-P0-1d | 硬阈值到期 ⇒ 立即执行强制分支（不等待决策），且仍投影通知 | L1 | §6.3 |
| AC-P0-1e | 宽限期以**可运行时钟**计量（父会话可被唤醒的时间），非纯墙钟（I8） | L1 | EC-A3 |

#### C1-2（= 改动 #2）新增 `extend_deadline` 控制动作

**需要调整的内容**

| 位置 | 调整 |
| --- | --- |
| `backend/internal/supervision/types.go:193-202`（ActionKind 常量块完整范围；197-202 仅尾部） | 新增 `ActionKind = "extend_deadline"` |
| `action_service.go:513-541` | 纳入**变更类**校验：必须带 `reason`，写审计（I4） |
| `local_control.go:244-252` | 纳入动作白名单与执行路径 |
| 请求体 | `{target_kind, target_id, extend_by \| new_deadline, extend_which: execution\|progress\|both, reason}` |
| 写入路径 | 更新 `ExecutionDeadlineAt` / `ProgressDeadlineAt` 必须走 **CAS + Version**（`UpdateExecutionRunCAS`），避免与扫描器竞态（EC-B1） |
| 审计与可见性 | 每次延长写生命周期事件（digest 与 UI 可见"已延长 ×N, +时长"），递增 `ExtensionCount` / `ExtendedTotal` |
| 边界 | 上限 = I5（单次 ≤1× 原始、单 obligation ≤3 次、总量 ≤4×）；不可逆点 = I6（`cancel_requested` / `canceling` / terminal 一律拒绝） |

**验收标准**

| AC | 断言 | 层级 |
| --- | --- | --- |
| AC-P0-2a | 合法调用 ⇒ `ExecutionDeadlineAt`/`ProgressDeadlineAt` 按 `extend_which` 前移，事件可见"已延长 ×N, +时长" | L1 |
| AC-P0-2b | 缺 `reason` ⇒ 拒绝（变更类校验路径） | L1 |
| AC-P0-2c | 超 I5 上限 ⇒ 明确错误 + `next_action` 提示 | L1 |
| AC-P0-2d | I6 不可逆点 ⇒ 一律拒绝（`ErrActionInvalid` 类） | L1 |
| AC-P0-2e | 并发延长 ⇒ CAS 生效，仅一次成功、无丢更新（EC-B1） | L1 |
| AC-P0-2f | 未知动作仍走既有 `ErrActionInvalid` 路径（兼容性约束不受影响） | L1 |

#### C1-3（= 改动 #3，字段部分）账本字段与迁移

**需要调整的内容**

| 位置 | 调整 |
| --- | --- |
| `backend/internal/supervision/execution_run.go`（`ExecutionRun` 结构 77-108） | 新增 `TurnID` / `ExtensionCount` / `ExtendedTotal` / `DecisionWindowUntil`（`DeclaredBudget` 复用或新增，见 §6.2） |
| `backend/internal/supervision/execution_store.go`（CRUD 接口 48-81、清单 86-92） | 读写路径；零值向后兼容 |
| **`backend/internal/supervision/sqlite_store.go:287-327`**（**DDL / 迁移实际所在**，设计稿未点名） | 新增列 + **幂等**迁移 |

> 说明：`ArtifactRefs` / `ResultSummary`（A4 取回字段）在 **P1** 落地（C2-4）；GC/保留策略在 **P3** 落地（C4-2）。

**验收标准**

| AC | 断言 | 层级 |
| --- | --- | --- |
| AC-P0-3a | 迁移**幂等**（重复执行不报错），旧行读出为零值且不 panic | L1 |
| AC-P0-3b | 新字段读写往返一致（store round-trip） | L1 |
| AC-P0-3c | `TurnID` 在派发时被写入，且等于派发方 turn（供 P1 的 join 判据使用） | L2 |

#### C1-4（= 改动 #8）配置项

**需要调整的内容**

| 位置 | 调整 |
| --- | --- |
| `backend/internal/supervision/config.go`（字段区 60-99、`WithDefaults:117`） | 新增：软阈值倍率（默认 `2× ProgressDeadlineAt`，Q3）、决策宽限期（默认 `2× HeartbeatTimeout`=10m、墙钟上限 2W，Q2）、延长上限（I5 参数，Q4）、turn 级 hard cap（默认 24h，Q5）、巡检间隔；`WithDefaults` 零值兼容 |
| 开关 | `escalate_first`（默认 true）、`suspension_enabled`（默认 true）——供 §10 回滚使用 |

**验收标准**

| AC | 断言 | 层级 |
| --- | --- | --- |
| AC-P0-4a | 零值 ⇒ 取默认值（表驱动逐项断言） | L1 |
| AC-P0-4b | 显式配置 ⇒ 生效且被 §6.3 阶梯实际使用（构造边界值验证） | L1 |
| AC-P0-4c | `escalate_first=false` ⇒ 恢复原强制分支（回滚路径可用） | L1 |

---

## 3. P1：turn 挂起与同一 turn resume

> 目标：`wait` 语义由挂起承接；resume 复用同一 `turn_id`；I1 收尾门生效。**进入前必须确认 O1/O2 已完成。**

#### C2-1（= 改动 #4）wake 升级为 resume

**需要调整的内容**

| 位置 | 调整 |
| --- | --- |
| `backend/internal/supervision/wake_scheduler.go:284-293`（现为 `ParentRunnable:287` + `DrainRunnable:289-293` 的门控与认领口；wake 载体 `WakeRequest` / `ScheduleWake` 在文件其他位置） | wake 升级为 **resume**：通知携带 `turn_id`，恢复**同一 turn**；保留 `DrainRunnable` 唯一来源语义 |
| `backend/internal/supervision/wake_consumer.go` | `AutoWakePrompt`（提示常量）改为**携带 rollup digest 的 resume 上下文**；`ParentRunnable` 门控照旧（running / waiting approval / compacting 时不并发第二轮） |
| 终局综合 | `pending_count == 0` ⇒ runtime 组装**全终态 rollup**（含 `completed_with_failures` 的失败项清单）注入父 turn，父据产出终局报告（§6.10 / §16.4） |
| 保底 | 若同一 `turn_id` 注入失败 ⇒ 按 I3 降级：断言失败**降级为告警并新开 turn**（保底不丢事件） |

**验收标准**

| AC | 断言 | 层级 | 对应设计稿 |
| --- | --- | --- | --- |
| AC-P1-1a | 派发 3 个 background 子任务，全终态后 resume 使用**同一 `turn_id`**，终局报告在同一 turn 内产出 | L2 | §8 P1-2 |
| AC-P1-1b | `turn.suspended` 后父 actor **非 Busy 占用**（可接受用户输入与 ESC） | L2 | §8 P1-1 |
| AC-P1-1c | 门控生效：父 running / waiting approval / compacting 时**不并发**第二轮 resume | L1 | §6.1 步骤 2 |
| AC-P1-1d | resume 上下文自带 rollup digest（模型无需回溯历史，H4）；digest 受 `DigestMaxItems=20` / `DigestMaxChars=4000` 约束 | L1 | §6.6 / §6.10 |

#### C2-2（= 改动 #5）删除内联阻塞路径

**需要调整的内容**

| 位置 | 调整 |
| --- | --- |
| `backend/internal/agent/loop.go:2865-2896`（正是 sync("wait") 内联阻塞路径，`RunChildren` 2874）、`:6948-6952`（`execution_mode` 枚举）+ **`:6953-6956`（`wait_timeout_sec`，设计稿所引范围未覆盖）** | 删除内联阻塞；派发后返回**挂起信号**（**不写终态**、**不发 `turn.finished`**） |
| 参数收敛 | `execution_mode` 收敛（旧值兼容期保留）；`wait_timeout_sec` → `deadline`（泛化为 per-obligation 声明式预算） |
| 派发时校验 | 每个 obligation 必须带 `deadline_at`（声明或默认），否则**派发即拒绝**（I2） |

**验收标准**

| AC | 断言 | 层级 |
| --- | --- | --- |
| AC-P1-2a | 派发后父**继续运行**；仅在"无事可做 / 试图收尾"时才挂起（§16.2） | L2 |
| AC-P1-2b | 挂起路径**不写终态、不发 `turn.finished`**（事件断言） | L1 |
| AC-P1-2c | `execution_mode` 旧值仍可读（兼容期不破坏） | L1 |
| AC-P1-2d | 声明式 `deadline` 落到 obligation 字段；缺 `deadline_at` 的派发被拒绝（I2） | L1 |

#### C2-3（= 改动 #6）宿主 Busy / Runnable 与熔断

**需要调整的内容**

| 位置 | 调整 |
| --- | --- |
| `backend/cmd/aicli/commands/chat_actor_host.go:794-805` | `Busy()` 纳入 `awaiting_obligations`（Q10：托管 turn 期间不允许并发新 turn，但允许中断与插话） |
| `:806-842` | 30m 硬编码（`chat_actor_host.go:826`）降级为**单次 resume episode 的熔断**，不再约束整个托管 turn |
| 同上 | `Runnable` 增加"**有 pending 用户输入不 drain**" |
| turn 级上界 | turn 级 hard cap（默认 24h，Q5）+ 用户 ESC 级联取消，避免"turn 永不结束"（EC-E1） |

**验收标准**

| AC | 断言 | 层级 | 对应设计稿 |
| --- | --- | --- | --- |
| AC-P1-3a | 长任务（>30m）不再被父 turn 熔断截断 | L2 | §8 P1-4 |
| AC-P1-3b | 挂起态**不占** actor 忙碌额度（用户可输入） | L1 | §8 P1-1 |
| AC-P1-3c | 有 pending 用户输入时不被自动 drain 覆盖 | L1 | §6.1 |
| AC-P1-3d | 单次 resume episode 超熔断 ⇒ 结束该 episode，turn 仍为挂起（不回退成整轮超时） | L1 | §6.1 |

#### C2-4（= 改动 #15 + #16 取回部分）通知幂等键与结果取回契约

**需要调整的内容**

| 位置 | 调整 |
| --- | --- |
| `backend/internal/supervision/wake_consumer.go:12` + digest 载体 | 通知载体由提示常量升级为**结构化载体**，携带稳定幂等键 `notify_key = hash(turn_id, obligation_id, event_kind, terminal_epoch\|progress_seq)` |
| 去重规则 | terminal 类按 `terminal_epoch` 去重（同一终态只投递一次）；progress 类按 `progress_seq` 去重（同一 seq 不重复唤醒） |
| 重试 | `requeueSupervisionWake` 复用**同一** `notify_key`，消费方按 key 去重 |
| `execution_run.go`（`ExecutionRun` 77-108，现仅有 `ResultRef:104`）/ `execution_store.go` / `sqlite_store.go` | 新增 `ArtifactRefs` / `ResultSummary` 字段（A4 / H1）——同名概念现居**结果投影侧**（`agent_result.go:58/70/130-131`、`snapshot.go:43-45/127-129`），需**对齐而非重复造**；回执形状对齐 §13.5：`job_id,status,output_ref\|artifact_id,total,completed,failed,failed_items[{id,error_class,retryable,last_output_tail_ref}],result_digest` |
| 字节约束 | 回执只带计数 + 失败摘要 + 引用（H2）；大产物落 artifact（I7 / H1） |

**验收标准**

| AC | 断言 | 层级 |
| --- | --- | --- |
| AC-P1-4a | 同一终态重复投递 ⇒ **只 resume 一次**（去重命中，无重复汇报） | L1 |
| AC-P1-4b | 投递失败重试 ⇒ 复用同一 `notify_key`，不产生第二次 resume | L1 |
| AC-P1-4c | 大产物走 artifact，digest 体积受 `DigestMaxChars` 约束 | L1 |
| AC-P1-4d | 失败项带 `error_class` + `retryable`（H3），供 `retry` / `reassign` / `abandon` 直接决策 | L1 |
| AC-P1-4e | 同一 turn 多次 resume 取回结果一致（H5 幂等，按 `terminal_epoch` 去重） | L2 |

#### C2-5（= A6）resume 前置容量门控

**需要调整的内容**

| 位置 | 调整 |
| --- | --- |
| resume dispatcher（`wake_scheduler.go` 认领路径） | resume 视为"起 turn"，**先过门控**：并发（`MaxConcurrent=4`）/ 深度（`MaxDepth=1(+1 hard/expert)`）/ 可见性（`shouldExposeSpawnSubagents`） |
| 超限处理 | 有界 **FIFO 排队**，digest 显示**排队位次**；排队超时走 escalate（critical + action_required） |
| 额度口径 | **挂起态本身不占额度**，避免"恢复即超限"或变相死锁（EC-H1） |

**验收标准**

| AC | 断言 | 层级 | 对应设计稿 |
| --- | --- | --- | --- |
| AC-P1-5a | 撞并发/深度门控 ⇒ 排队而非丢弃，digest 带位次 | L1 | §8 P1-5 |
| AC-P1-5b | 排队超时 ⇒ escalate（critical + action_required），不静默 | L1 | §6.1 步骤 5 |
| AC-P1-5c | 构造 N = `MaxConcurrent` 个挂起 turn ⇒ 仍可起新 turn（挂起不占额度） | L2 | EC-H1 |
| AC-P1-5d | **I1 收尾门**：账本非空时模型请求收尾 ⇒ 拦截并自动转入挂起 | L2 | §8 P1-3 |
| AC-P1-5e | 提前收尾的唯一合法路径：先把未完成项**变成**终态（cancel / abandon），而非忽略（quorum=all 不可放宽） | L1 | §16.4 |

---

## 4. P2：巡检与管理闭环

> 目标：`subagent_status` / `subagent_inspect_task` / 改造后的 `wait_agent` 可用；巡检预约可配置；进度埋点强制开启；巡检不被误判；审批与 steer 能唤醒父。**#14 必须在本阶段首批落地（O3）。**

#### C3-1（= 改动 #7）巡检原语（只读）

**需要调整的内容**

| 位置 | 调整 |
| --- | --- |
| **`backend/internal/toolbroker/supervision_tools.go:120-122`（supervision 工具注册实际所在，共 5 个）+ `broker.go:59-63`（工具常量）**；`internal/agent` 侧仅名称引用（`doom_loop.go:205`） | 新增 `subagent_status`（账本总览）与 `subagent_inspect_task`（深看单个），复用 `snapshot` 数据面与 artifact 归档，**不新建存储** |
| `subagent_status` 行字段 | `obligation_id` / `subject_kind(batch\|agent_run\|team_task)` / `subject_id` / `state` / `attempt` / `max_attempts` / `started_at` / `deadline_at` / `declared_budget` / `extension_count` / `last_heartbeat_at` / `heartbeat_age` / `last_progress_at` / `progress_age` / `progress_seq` / `current_tool` / `completed_steps` / `artifact_count` / `stall_verdict(healthy\|slow\|suspect_stuck\|suspect_dead)` / `allowed_actions[]` |
| `subagent_inspect_task` | 最近事件 / 输出尾部 / 当前工具调用 / 产物引用，**字节有界**；超出走 artifact，digest 只带预览 + `artifact_id`（I7） |
| 暴露范围 | 仅**父 Agent** 可见（Q7：子 Agent 不可巡检兄弟/父） |
| `extend_deadline` | 暴露为控制动作（P0 已实现内核，此处仅暴露） |

**验收标准**

| AC | 断言 | 层级 | 对应设计稿 |
| --- | --- | --- | --- |
| AC-P2-1a | 二维 stall 矩阵**四象限**各有 fixture：心跳新鲜×进度停滞 ⇒ `suspect_stuck`；心跳失效×停滞 ⇒ `suspect_dead`；有产物产出 ⇒ `slow`；**长工具调用不得被判 `suspect_dead`** | L1 | §8 P2-1 |
| AC-P2-1b | 巡检输出超限 ⇒ 走 artifact，digest 体积受 `DigestMaxChars` 约束 | L1 | §8 P2-2 |
| AC-P2-1c | 空账本调用 ⇒ **立即**返回 `next_action=finalize`（不空等） | L1 | §6.4 |
| AC-P2-1d | 观测类失败（工具超时 / 字节超限）⇒ 表现为观测结果 + `next_action`，**不是**工具失败 | L1 | B5 |
| AC-P2-1e | 返回契约含 `terminal_delta[] / pending_count / terminal_count / allowed_actions[] / digest_ref` | L1 | §6.4 |

#### C3-2（= 改动 #9）巡检预约

**需要调整的内容**

| 位置 | 调整 |
| --- | --- |
| `backend/internal/api/skills/supervision_progress_check.go` | 从宿主级固定间隔升级为**按 obligation 的 `check_in_after` 巡检预约** |
| 触发语义 | 到点触发一次巡检或"搭车"下一次 progress resume（§6.3 搭车档） |

**验收标准**

| AC | 断言 | 层级 |
| --- | --- | --- |
| AC-P2-2a | 每个 obligation 有独立 `check_in_after`，到点触发一次（不重复、不遗漏） | L1 |
| AC-P2-2b | 关掉全局固定间隔后仍能按预约触发（不再依赖宿主级间隔） | L1 |

#### C3-3（= 改动 #10）托管 turn 下强制进度埋点

**需要调整的内容**

| 位置 | 调整 |
| --- | --- |
| 进度埋点（`TaskProgressInterval`；定义 `supervision/config.go:70-77`，默认处理 `159-161`，**默认 0 = 显式 opt-in**） | 托管 turn 下**强制开启**进度写入，避免 stall 判定失真（G8） |
| 写放大控制 | **仅在存在挂起 turn / obligation 时开启**，空闲期不写（§10 风险 R2 的缓解） |

**验收标准**

| AC | 断言 | 层级 |
| --- | --- | --- |
| AC-P2-3a | 托管 turn 中 `LastProgressAt` / `ProgressSeq` 持续更新（长工具调用期间也有心跳） | L1 |
| AC-P2-3b | 非托管 turn 行为不变（空闲期不写，无写放大） | L1 |

#### C3-4（= 改动 #13）`wait_agent` / `wait_team` 改造（§16.3，Q11 拍板：保留并改造）

**需要调整的内容**

| 位置 | 调整 |
| --- | --- |
| `backend/internal/toolbroker/broker.go:45`（`ToolWaitAgent` 常量，精确命中）、**`:1996-2060`（`wait_agent` 主体）、`:2757-2800`（`wait_team` 主体）**、`:1617-1640` / `:3235-3243`（实为 `spawn_agent` 超时参数与参数白名单，**非 wait 主体**）；`loop.go:6961+`（`agents` 数组项的 per-task `deadline` 落点） | 按 §16.3 **四条约束**改造：①**区间钳制** min 10s / default 30s / max 1h，越界返回**模型可见错误**（不静默截断）；②**活动驱动**（挂账本事件 + 输入队列活动，**禁止轮询**）；③**可被打断**（用户 steer / ESC / 新输入立即结束等待段）；④**超时即观测**（`timed_out=true` 是**成功**返回） |
| 统一返回契约 | `{waited_ms, timed_out, obligations[], terminal_delta[], pending_count, terminal_count, next_action, digest_ref\|artifact_id}`；`next_action ∈ {continue_wait, inspect, extend_deadline, cancel, finalize, suspend}` |
| 派发接口 | 补 per-task `deadline`（batch/team 的每个 task 可声明，A3） |
| 语义边界（写进工具描述） | `timed_out` ≠ turn 结束 ≠ 子任务失败；返回后 `pending_count>0` ⇒ 父**不得**收尾（I1 拦截并自动转挂起）；`wait_agent` **自身不触发挂起**；空账本 ⇒ 立即 `finalize`；`wait_team` 同口径（共享实现） |
| 兼容 | 保留读取旧参数 `{after_seq,id,ids,session_id,session_ids,timeout_ms}`，但 `timeout_ms` 语义按新口径；旧的"无界 / 默认长超时 + 内联阻塞"行为**取消**（迁移窗口见 Q6） |

**验收标准**

| AC | 断言 | 层级 |
| --- | --- | --- |
| AC-P2-4a | 越界 `timeout_ms`（<10s 或 >1h）⇒ **模型可见错误**，不静默 clamp | L1 |
| AC-P2-4b | 事件到达 ⇒ **立即返回**（无 sleep 轮询），`waited_ms` 与事件时间相符 | L1 |
| AC-P2-4c | 等待期间用户输入 / ESC ⇒ 立即结束等待段返回（§16.3 约束 3） | L2 |
| AC-P2-4d | `timed_out=true` ⇒ 成功返回（非错误、**不取消**子任务），附账本摘要 + `next_action` | L1 |
| AC-P2-4e | 空账本 ⇒ 立即返回 `next_action=finalize` | L1 |
| AC-P2-4f | `pending_count>0` 时父请求收尾 ⇒ 被 I1 拦截并自动转挂起 | L2 |
| AC-P2-4g | `wait_team` 与 `wait_agent` 共享实现、返回契约一致（batch/team 视角） | L1 |
| AC-P2-4h | 旧参数仍可读（兼容期），但不再产生无界阻塞 | L1 |

#### C3-5（= 改动 #14）doom-loop 豁免（**P2 首批**）

**需要调整的内容**

| 位置 | 调整 |
| --- | --- |
| `backend/internal/agent/doom_loop.go:178-184`（重复调用敏感集合） | 巡检 / 等待工具改为按"**无进展的重复**"判罚，而非"多次调用" |
| 豁免规则 | `wait_agent` / `subagent_status` / `read_agent_events` 在账本状态或 `progress_seq` **发生变化时一律不计数** |
| 判罚降级 | 连续 **5 次**观测到**完全相同读数**才计入空转，且只触发 `next_action=suspend` **建议**，不直接判罚 |

**验收标准**

| AC | 断言 | 层级 | 对应设计稿 |
| --- | --- | --- | --- |
| AC-P2-5a | 连续 active wait / 巡检且读数变化 ⇒ **不触发** doom loop | L1 | §8 P2-0 |
| AC-P2-5b | 读数完全不变连续 5 次 ⇒ 计一次空转 + 建议 `suspend`（不直接判罚） | L1 | §6.4 |
| AC-P2-5c | 参数变化但读数不变（换 id 轮询）⇒ 仍计入空转（防绕过） | L1 | §6.4 |

#### C3-6（= 改动 #17）审批路由与权限继承（§6.14）

**需要调整的内容**

| 位置 | 调整 |
| --- | --- |
| `backend/internal/supervision/execution_supervisor.go:494-501` | 审批请求纳入 **resume 触发器独立通道**（与 terminal / progress / deadline 并列），**不受 progress / auto-wake 预算裁剪** |
| `:498-500`、`:603-605` | `approval_timeout` 不再直接进强制分支，改 **escalate-first**：先上报父决策（allow / deny / extend / cancel），宽限期后兜底（默认 deny + 记 `CancelSource`） |
| `approval_projection.go:10-17`、`:40-60`；`projection.go:87-103` | 审批投影 severity **写死** `SeverityCritical + action_required`（否则挂起 turn 不会被唤醒处理审批） |
| 持久化 | 审批请求与决策窗口一并落盘（§6.12），resume 后父仍可 `resolve_agent_approval`（含 `patched_args`） |
| 回执 | 过期 token（`ErrAgentRunSuperseded`）映射为 `next_action=inspect\|finalize`，**不得**表现为工具失败 |
| 权限 | 子 Agent 权限继承自派发方，**跨层不可提权**；`patched_args` 只能**收紧**（`internal/policy` 不变） |
| UI | 挂起期间显示"子任务等待审批（N）"，用户可直接批准/拒绝（多会话审批 UI 已存在：`cmd/aicli/commands/web/js/approvals.js`） |

**验收标准**

| AC | 断言 | 层级 | 对应设计稿 |
| --- | --- | --- | --- |
| AC-P2-6a | 子任务等审批 + 父挂起 ⇒ 审批请求**能唤醒父**（不被预算裁剪），无"双双卡死" | L2 | §8 P2-4 |
| AC-P2-6b | `approval_timeout` 走 escalate-first：先上报、宽限后兜底 deny 且记 `CancelSource` | L1 | §6.14 规则 3 |
| AC-P2-6c | 审批投影恒为 critical + action_required（请求与解决两类事件都断言） | L1 | §6.14 规则 2 |
| AC-P2-6d | 过期 token ⇒ 成功观测 + `next_action`，**非**工具失败 | L1 | §6.14 规则 6 |
| AC-P2-6e | 请求与解决**原地更新**，不产生 stale 行（父 inbox 中同一审批只有一行） | L2 | §6.14 规则 1 |
| AC-P2-6f | `patched_args` 收紧生效、放宽被拒（跨层不可提权） | L1 | §6.14 规则 5 |

#### C3-7（= 改动 #18）steer 与 resume 的交互（§6.15）

**需要调整的内容**

| 位置 | 调整 |
| --- | --- |
| `backend/internal/chat/actor.go`（**“steer” 目前全仓不存在，属待建概念**；现有载体 `turnInjection`（`:409-411`）与 `startSessionRun`（`:2587`，注入点 `:2615-2617`）） | steer 作为 **resume 触发器**：父**挂起**时起一段新 resume episode（同 `turn_id`），用户输入置顶；账本不变（EC-I5） |
| 同上 | 父 running 且 **active wait** ⇒ 立即结束等待段返回；**episode 执行中** ⇒ 不硬截断，在**当前工具调用边界**后注入 |
| `toolbroker`（`send_input`） | 显式中断复用 `send_input` 的 `interrupt`（**唯一**接受 `interrupt` 的工具，`broker_arg_audit.go:31`、`:95`） |
| 优先级 | steer 与**审批**同时到达 ⇒ **审批优先**（必须先 `resolve_agent_approval`），steer 不得绕过审批闸门 |
| 投递审计 | 沿用 mailbox 状态 `MailboxDeliveryStatus{Queued, Delivered, Failed}`（`session_runtime_support.go:1912-2009`）；目标已终态 ⇒ 回执带 `next_action=inspect\|finalize` |
| 计数 | steer **不计入** stall / 延长次数，不改变 `progress_seq` 语义 |

**验收标准**

| AC | 断言 | 层级 | 对应设计稿 |
| --- | --- | --- | --- |
| AC-P2-7a | 挂起态收到 steer ⇒ 起新 episode（同 `turn_id`）、账本不变 | L2 | §8 P2-5 |
| AC-P2-7b | steer 与审批同时到达 ⇒ **审批优先**（steer 在审批解决后注入） | L2 | §8 P2-5 / EC-J4 |
| AC-P2-7c | steer 不改变 `progress_seq`、不计入 stall / 延长计数 | L1 | §6.15 |
| AC-P2-7d | 目标已终态 ⇒ 回执带 `next_action`；投递审计状态正确（`Queued→Delivered` / `Failed`） | L1 | §6.15 |
| AC-P2-7e | active wait 中 steer ⇒ 等待段立即结束并返回（同 AC-P2-4c） | L2 | §16.3 约束 3 |

---

## 5. P3：收敛与清理

> 目标：删除阻塞分支与 `wait` 取值；重启恢复补齐；兼容期收口。**进入条件：P2 DoD + 兼容期结束（Q6：一个发布周期）。**

#### C4-1（= 改动 #5 移除部分）删除阻塞分支与 `wait` 取值

**需要调整的内容**

| 位置 | 调整 |
| --- | --- |
| `backend/internal/agent/loop.go` | 删除内联阻塞分支残留；`execution_mode` 标记 **deprecated**（旧值仍可用） |
| 兼容映射 | `wait` 取值保留映射，不做硬删除直到本阶段（§10 风险 R1 的缓解） |

**验收标准**

| AC | 断言 | 层级 |
| --- | --- | --- |
| AC-P3-1a | 阻塞分支删除后无残留调用（编译期 + grep 断言） | L1 |
| AC-P3-1b | `execution_mode` 旧值可用且有 deprecation 提示 | L1 |
| AC-P3-1c | 兼容期双写（batch handle + resume 聚合报告）在移除后仍不破坏旧客户端 | L2 |

#### C4-2（= 改动 #16 GC/保留部分）保留、GC 与"驱逐后可重建"

**需要调整的内容**

| 位置 | 调整 |
| --- | --- |
| `execution_run.go` / `execution_store.go`（GC）/ `action_service.go`；**现状：`execution_runs` 无 GC（仅 `PruneWakeClaims`，`sqlite_store.go:1279`）；`takeover` 在 supervision 内不存在（仅 `team/sqlite_owner_lease.go:14/96`）** | 保留窗口：终态 obligation 保留至 **turn 终局 + N 天**（默认 7，可配）；窗口内 `subagent_status` 可见（含异常终态原因） |
| GC 条件 | 仅清"已 resolved **且** 无 artifact 引用 **且** 无 pending resume"的行；批量删除、单次有界 |
| **不得影响** | 当前 turn 的账本视图、决策窗口内的行、`orphan_suspected` 待处置行 |
| 驱逐后可重建 | 内存态（常驻 actor / 巡检缓存）可被驱逐；**账本是持久真源**，驱逐后由 store 重建 |
| 授权动作 | `takeover`（新增动作，带 `reason`，写审计）——变更类动作需 `OwnerID == 当前会话` 且 lease 有效（§6.11） |

**验收标准**

| AC | 断言 | 层级 |
| --- | --- | --- |
| AC-P3-2a | GC 三个条件各有正/反例测试；"不得影响"三类行**必须保留** | L1 |
| AC-P3-2b | 驱逐内存态后账本由 store 重建，`subagent_status` 读数一致 | L2 |
| AC-P3-2c | 非 owner 变更 ⇒ 需 `takeover` 且写审计（`ActorID` + `reason`，I4） | L1 |

#### C4-3 重启恢复与围栏

**需要调整的内容**

| 位置 | 调整 |
| --- | --- |
| 宿主重启路径 | 交互式宿主的"**恢复 or orphaned**"决策；不可恢复 ⇒ `orphaned`（终态）+ 告警 |
| `execution_supervisor.go:559-588`（`fenceOrphaned`） | 提升 fencing token，阻断晚到写入 |

**验收标准**

| AC | 断言 | 层级 | 对应设计稿 |
| --- | --- | --- | --- |
| AC-P3-3a | 重启后挂起 turn 的**恢复**路径有测试（账本 + resume 队列可读回） | L2 | §8 P3-1 |
| AC-P3-3b | 不可恢复 ⇒ 判 `orphaned` + fencing token 提升，晚到写入被拒 | L1 | §8 P3-1 |
| AC-P3-3c | 重启后 `awaiting_obligations` 与 `Busy()` 语义一致（不出现"假空闲"） | L1 | §6.7 |

#### C4-4 端到端验收

| AC | 断言 | 层级 | 对应设计稿 |
| --- | --- | --- | --- |
| AC-P3-4a | 全量回归绿（backend 全包 + 前端相关单测） | L2 | §8 P3-3 |
| AC-P3-4b | 一次真实长任务端到端：**≥2h 声明预算 + 中途一次延长**，全程 turn 不中断、终局报告完整 | L4 | §8 P3-3 |
| AC-P3-4c | 五项度量指标相对基线改善（§7.3），且 `decision_window_expired` 兜底占比低且稳定 | L4 | §8 度量 |

---

## 6. 验收标准（总表）

### 6.1 验收层级定义

| 层级 | 含义 | 形态 | 执行时机 |
| --- | --- | --- | --- |
| **L1** | 单元 / 表驱动测试 | 包内 `_test.go`，可用假时钟与 fake store | 每次提交（CI） |
| **L2** | 集成测试 | 跨包（supervisor + store + actor + toolbroker），真实 store 落盘 | 每阶段门禁 |
| **L3** | 场景回放 | 按设计稿 §9/§15 的场景表逐条构造（见 §6.4） | 每阶段门禁 |
| **L4** | 观测与度量 | 事件流 / supervision snapshot / 指标看板 | 阶段末 + 上线后 |

**统一断言口径**：事件名与字段（`turn.suspended` / `turn.finished` / `escalated` / `cancel_requested` / `CancelSource` / `progress_seq` / `ExtensionCount`）与设计稿保持一致；**观测类失败不得断言为工具失败**（B5）。

### 6.2 不变量验收（I1–I10）

| ID | 不变量 | 可执行断言 | 层级 | 落地 AC |
| --- | --- | --- | --- | --- |
| **I1** | 非终态 obligation ⇒ 父 turn 不得进入终态 | 账本非空时请求收尾 ⇒ 被拦截并转挂起；`pending_count>0` 时 `wait_agent` 返回后父仍不得收尾 | L1/L2 | AC-P1-5d、AC-P1-5e、AC-P2-4f |
| **I2** | 每个 obligation 必有 `deadline_at` | 缺 `deadline_at` 的派发被拒绝 | L1 | AC-C0-2e、AC-P1-2d |
| **I3** | resume 复用同一 `turn_id` | 全终态后 resume 的 `turn_id` 与派发 turn 相同；注入失败 ⇒ 告警 + 新开 turn（保底） | L2 | AC-P1-1a |
| **I4** | 变更类动作必须带 `reason` 且写审计 | 缺 `reason` 拒绝；审计含 `ActorID`（会话/模型/用户/`runtime_fallback`）+ `reason` | L1 | AC-P0-2b、AC-P3-2c |
| **I5** | 延长有界 | 单次 ≤1× 原始、单 obligation ≤3 次、总量 ≤4×，超限拒绝并回执 | L1 | AC-P0-2c |
| **I6** | 不可逆点之后禁止延长 | `cancel_requested` / `canceling` / terminal ⇒ `ErrActionInvalid` 类错误 | L1 | AC-P0-2d |
| **I7** | 巡检输出字节有界，超限走 artifact | digest 只带预览 + `artifact_id`，体积受 `DigestMaxChars` 约束 | L1 | AC-P1-4c、AC-P2-1b |
| **I8** | 决策宽限期按**可运行时钟**计量 | 父不可运行时宽限期不计时（构造睡眠窗口验证） | L1 | AC-P0-1e |
| **I9** | 耐久性前提：无 durable store ⇒ 禁止挂起 | 两条降级路径均不产生 `turn.suspended`；降级投影恰好 1 条 warning | L1/L2 | AC-C0-1a–d |
| **I10** | join 可判定性：终态有限时间可达 | watchdog 在 ≤1 扫描周期判终态；终态枚举全表可达；判终态幂等 | L1 | AC-C0-2a–d |

### 6.3 阶段门禁（不通过不进下一阶段）

| 阶段 | 必过 AC | 必过场景（§6.4） | 证据留痕 |
| --- | --- | --- | --- |
| P0-前置 | AC-C0-1a–d、AC-C0-2a–e | — | 降级路径的 warning 投影截图 / 日志 |
| P0 | AC-P0-1a–e、AC-P0-2a–f、AC-P0-3a–c、AC-P0-4a–c | EC-A 组 6 条 + EC-B1/EC-B5 | `escalated` 事件、延长审计行 |
| P1 | AC-P1-1a–d、AC-P1-2a–d、AC-P1-3a–d、AC-P1-4a–e、AC-P1-5a–e | EC-E 组 8 条 + EC-C 组 7 条 | `turn.suspended` / 同 `turn_id` resume 的 turn 轨迹 |
| P2 | AC-P2-1a–e、AC-P2-2a–b、AC-P2-3a–b、AC-P2-4a–h、AC-P2-5a–c、AC-P2-6a–f、AC-P2-7a–e | EC-D 组 6 条 + EC-H/EC-I/EC-J 21 条 | 巡检读数、审批唤醒、steer 注入审计 |
| P3 | AC-P3-1a–c、AC-P3-2a–c、AC-P3-3a–c、AC-P3-4a–c | EC-F 组 3 条 + EC-G 组 4 条 + 全量回归 | 端到端 runbook 记录 + 度量对比 |

### 6.4 场景回放（64 条 → 抽样规则与必测清单）

设计稿共 **64 条**边界场景（§9 的 A–G 组 43 条 + §15 的 EC-H 12 条 + EC-I 5 条 + EC-J 4 条）。

| 分组 | 条数 | 回放要求 |
| --- | --- | --- |
| A. 唤醒与决策时序 | 6 | **全部必测**（P0 门禁） |
| B. 超时与延长 | 9 | 必测 B1（CAS 竞态）、B5（挂起态落盘）；其余抽 2 |
| C. 终止与级联 | 7 | **全部必测**（P1 门禁，join 正确性核心） |
| D. 巡检与证据 | 6 | **全部必测**（P2 门禁） |
| E. turn 生命周期 | 8 | **全部必测**（P1 门禁） |
| F. 成本与预算 | 3 | 全部必测（P3） |
| G. 兼容与迁移 | 4 | **全部必测**（P3 + 兼容期） |
| H. 追加（挂起/resume 语义） | 12 | 抽 6，必含 H1（resume 容量门控） |
| I. 追加（active wait） | 5 | **全部必测**（P2 门禁） |
| J. 追加（审批/steer 交互） | 4 | **全部必测**（P2 门禁） |

**必测总计**：6+2+7+6+8+3+4+1+5+4 = 46 条；其余 18 条在 P3 全量回归中覆盖（至少各执行一次）。

### 6.5 兼容与回归验收

| AC | 断言 | 层级 |
| --- | --- | --- |
| AC-X-1 | 新增字段零值向后兼容：旧数据读出零值不 panic、行为不退化 | L1 |
| AC-X-2 | 未知动作仍走 `ErrActionInvalid` 既有校验路径 | L1 |
| AC-X-3 | `wait_agent` / `wait_team` **不删除**（Q11），旧参数保留读取，仅语义与返回契约变更 | L1 |
| AC-X-4 | `execution_mode` 旧值在兼容期内可用（P3 才标记 deprecated） | L1 |
| AC-X-5 | EC-G 组 4 条全绿（迁移/兼容面） | L3 |
| AC-X-6 | 全量回归：backend 全包测试绿，且无新增 flaky | L2 |

---

## 7. 测试落点、证据与度量

### 7.1 测试落点（已实测存在，**优先扩展而非新建**）

| 改动项 | 目录 | 建议落点（现有文件） |
| --- | --- | --- |
| #1 / #2 / #3 / #8 / #12 | `backend/internal/supervision/` | `execution_supervisor_test.go`（主路径）、`action_service_test.go`（动作校验/白名单）、`execution_store_test.go` + `sqlite_store_test.go`（字段与迁移）、`config_test.go`（默认值）、`projection_test.go`（critical→`ScheduleWake`）、`approval_projection_test.go` |
| #11 | `backend/internal/agent/` | `subagent_batch_coordinator_test.go`、`spawn_subagents_policy_test.go`、`tool_surface_binding_test.go`（工具面门控）、`subagent_batch_fencing_token_test.go` |
| #4 / #15 | `backend/internal/supervision/` | `wake_consumer_test.go`、`wake_budget_test.go`、`wake_progress_budget_test.go`、`wake_progress_wake_test.go`、`wake_self_check_test.go`、`digest_test.go` |
| #5 / #6 | `backend/cmd/aicli/commands/` | `chat_actor_host_test.go`、`chat_actor_batch_converge_test.go`、`chat_actor_execution_supervisor_test.go`、`chat_actor_registry_wake_test.go`、`chat_actor_run_stall_env_test.go`、`chat_actor_progress_check_test.go` |
| #7 / #13 | `backend/internal/toolbroker/` | `supervision_tools_test.go`、`supervision_tool_registry_test.go`、`broker_wait_schema_guard_test.go`、`broker_team_wait_policy_test.go`、`broker_arg_kinds_test.go`、`broker_arg_audit_test.go`；wiring 守卫见 `backend/internal/api/skills/broker_wait_policy_wiring_guard_test.go` |
| #9 / #10 | `backend/internal/api/skills/` | `supervision_progress_check_test.go`、`supervision_progress_check_api_test.go`、`supervision_progress_mirror_source_test.go`、`chat_actor_progress_mirror_test.go` |
| #14 | `backend/internal/agent/` + `backend/internal/toolbroker/` | `doom_loop_test.go`、`polling_guard_test.go`、`agent_events_repeat_read_test.go` |
| #17 | 跨包 | `supervision/approval_projection_test.go`、`chat/actor_approval_terminal_test.go` + `chat/actor_test.go`、`api/skills/session_agent_approval_test.go`、`cmd/aicli/commands/chat_actor_approval_test.go`、`agent_stdio_approval_recovery_test.go` |
| #18 | `backend/internal/chat/` + `backend/cmd/aicli/commands/` | `chat/actor_test.go`（注入载体）、`chat/mailbox_idempotency_test.go`、`chat/mailbox_event_test.go`、`chat/trigger_turn_drain_test.go`；`cmd/aicli/commands/chat_escape_interrupt_test.go`、`chat_input_arbitration_test.go` |
| P3 重启恢复 | 跨包 | `supervision/sqlite_store_test.go`、`cmd/aicli/commands/chat_actor_registry_reclaim_test.go`、`chat_agent_reclaim_events_test.go`、`chat/session_runtime_store_test.go` |
| 端到端 | `backend/internal/chat/` | `integration_test.go` + 运维 runbook 记录 |

> 约定：**能扩展既有文件就不新建**（保持测试与实现同包、同构）；确有必要新建时使用 `<被测对象>_<主题>_test.go` 命名。

### 7.2 证据留痕（每个门禁必须产出）

| 证据类型 | 形态 | 用途 |
| --- | --- | --- |
| 事件流 | `turn.suspended`（出现）/ `turn.finished`（挂起期不出现）/ `escalated`（出现）/ `cancel_requested`（软阈值下不出现） | 证明 I1 与 escalate-first |
| 审批事件 | `approval_requested` / `approval_resolved`（原地更新） | 证明 §6.14 规则 1/2/6 |
| 账本快照 | `subagent_status` 输出（含 `stall_verdict` / `allowed_actions[]`） | 巡检正确性与 UI 可见性 |
| turn 轨迹 | 同一 `turn_id` 的 resume 序列 | 证明 I3 |
| 审计 | 变更类动作的 `reason` + `ActorID`（含 `runtime_fallback`） | 证明 I4 |
| 汇总计数 | supervision snapshot 的 `critical_unresolved` / `action_required` | 证明"异常也必须上报" |
| 度量 | §7.3 五项与基线对比 | 证明收益 |

### 7.3 度量指标与阈值（基线见 §1.1 C0-E）

| 指标 | 期望方向 | 验收口径（建议值，需与基线一并确认） |
| --- | --- | --- |
| 被 runtime 强制取消的 run 占比 | 下降 | 低于基线，且下降**不来自漏判**（用误杀率交叉验证） |
| `decision_window_expired` 兜底占比 | 低且稳定 | < 10% 且两周内波动收敛 |
| 成功完成到父 Agent 汇报的延迟 | 收敛到一次 resume 周期内 | P95 ≤ 1 个 resume 周期（含 rollup digest 组装） |
| 单托管 turn 的 resume 次数 / token 成本 | 受预算约束、无风暴 | 不超过 `WakeMaxAutoWake`=5 / `WakeMaxProgressWake`=6 / 1h 窗口 |
| 误杀率（取消后 5 分钟内子任务本可完成） | 趋近 0 | 抽样人工复核 + 端到端 runbook |

> **基线**（2026-09-23 首次采样，读数与采样命令见 §13.14）：指标 1 = 26/144（18.06%）；指标 2 = 0/144（0%）；指标 3 在 dev 库无样本（0 条出件行）；指标 4 = 宿主账本读数；指标 5 = 26 条候选（其中 25 条为重启清扫批次）。生产基线在部署实例上按同一命令一次采样即为基线。

---

## 8. 施工顺序、依赖与回滚

### 8.1 依赖图

```
C0-1(#11 I9 探测) ─┐
C0-2(#12 watchdog) ─┴─► C1-3(#3 字段) ─► C2-1(#4 resume) ─┐
C1-4(#8 配置) ─► C1-1(#1 escalate) ─► C1-2(#2 extend)     ├─► C2-5(A6 容量门控)
                                                          │
C2-2(#5 去阻塞) ─► C2-3(#6 宿主 Busy/熔断) ───────────────┘
C2-4(#15 幂等键 + #16 取回字段) ── 与 C2-1 同批

P2: C3-5(#14 doom-loop 豁免，首批) ─► C3-1(#7 巡检) ─► C3-2(#9 预约) ─► C3-4(#13 wait 改造)
                                     C3-3(#10 埋点) ─┘
    C3-6(#17 审批) / C3-7(#18 steer) ── 依赖 C2-1(resume) + C3-1(巡检读数)

P3: C4-1(#5 移除) ─► C4-2(#16 GC/保留) ─► C4-3(重启恢复) ─► C4-4(端到端)
```

**关键路径**：`C0-1 → C0-2 → C1-3 → C2-1 → C2-5 → C3-5 → C3-4 → C4-1 → C4-4`。

### 8.2 回滚操作手册（逐阶段）

| 阶段 | 触发信号 | 回滚动作 | 影响面 |
| --- | --- | --- | --- |
| P0 | 卡死任务不再被及时清理 | `escalate_first=false` 恢复原强制分支；`execution_timed_out` 硬路径不受影响 | 仅软阈值行为 |
| P0 | `extend_deadline` 被滥用 | 关闭动作白名单条目 | 单动作失效 |
| P1 | turn 挂起导致"turn 永不结束" | 开启 turn 级 hard cap（默认 24h）+ 用户 ESC 级联取消；必要时 `suspension_enabled=false` 退回"结束再唤醒"模式 | 回到 legacy 同步路径 |
| P1 | 同 turn resume 上下文异常 | 走 I3 保底：断言失败 ⇒ 告警 + **新开 turn**（不丢事件） | 语义降级，不丢数据 |
| P1 | 挂起态依赖 durable store 失效 | `suspension_enabled=false`；I9 探测失败自动回退同步路径 | 会话级降级 |
| P2 | 巡检 / 等待工具被误判 | 按"无进展重复"判罚（#14）；必要时关闭巡检工具暴露或提高阈值 | 仅巡检可用性 |
| P2 | 进度埋点写放大 | 仅在存在挂起 turn/obligation 时开启，空闲期不写 | 写放大收敛 |
| P3 | `wait` 移除造成兼容破坏 | 兼容期双写；`wait` 取值保留映射，不做硬删除 | 旧客户端 |

### 8.3 提交与分支约定

- 一改动项一提交（编号入 commit message），一阶段一 PR/合并点；
- 每次合并前必须通过该阶段门禁（§6.3）；
- 破坏性语义变更（P1 的 `wait` 语义、P3 的移除）单独标注 **BREAKING** 并在变更说明中给出迁移窗口（Q6：一个发布周期）。

---

## 9. 交付物与完成定义（DoD 清单）

**代码**

- [ ] `supportsSuspension()` 探测 + 两条降级路径（#11）
- [ ] watchdog 强制判终态 + 全终态枚举（#12）
- [ ] escalate-first + 决策宽限期 + 兜底（#1）
- [ ] `extend_deadline` 动作（含上限/不可逆点/CAS）（#2）
- [ ] 账本字段 `TurnID` / `ExtensionCount` / `ExtendedTotal` / `DecisionWindowUntil`（#3）
- [ ] 配置项 + 回滚开关 `escalate_first` / `suspension_enabled`（#8）
- [ ] wake → resume（同 `turn_id`）+ rollup digest（#4）
- [ ] 内联阻塞移除 + 声明式 `deadline`（#5）
- [ ] 宿主 `Busy()` / `Runnable` / episode 熔断（#6）
- [ ] `notify_key` 幂等键 + 结果取回字段（#15 / #16 取回）
- [ ] resume 容量门控 + 排队位次（A6）
- [ ] `subagent_status` / `subagent_inspect_task`（#7）
- [ ] 巡检预约（#9）、进度埋点强制（#10）
- [ ] `wait_agent` / `wait_team` 四约束 + 统一返回契约（#13）
- [ ] doom-loop 豁免（#14）
- [ ] 审批独立 resume 通道 + escalate-first（#17）
- [ ] steer 联动 + 审批优先（#18）
- [ ] GC/保留 + `takeover`（#16）、重启恢复与围栏（P3）

**测试**

- [ ] §7.1 列出的测试文件全部落地，且 §6.3 阶段门禁 AC 全绿
- [ ] §6.4 的 46 条必测场景全部回放通过，其余 18 条在全量回归中覆盖
- [ ] 兼容验收 AC-X-1…6 全绿

**文档与运维**

- [ ] 更新工具描述（`wait_agent` 语义边界、巡检工具可见范围、降级提示）
- [ ] 更新 supervision runbook（挂起/恢复/围栏的操作步骤）
- [ ] 度量基线 + 上线后对比记录（§7.3）

---

## 10. 风险与应对（施工期）

> 风险分级沿用设计稿 §10 的 **R0/R1/R2**（与实施阶段 P0–P3 编号区分）。

| 风险 | 等级 | 施工期表现 | 应对 |
| --- | --- | --- | --- |
| escalate-first 后卡死任务滞留 | R1 | 决策链路未接通时软阈值不再清理 | 硬阈值兜底不受影响；`escalate_first=false` 可回滚 |
| turn 永不结束 | R1 | 会话被长期占用 | turn 级 hard cap + ESC 级联 + `suspension_enabled` 开关 |
| resume 风暴 | R1 | 重复投递造成多次 resume / 重复汇报 | `notify_key` 幂等键（#15）与 #4 同批落地 |
| 无 durable store 误挂起 | R1 | 重启后失忆、I1 无法自愈 | O1 强制顺序 + I9 探测 + 自动回退同步路径 |
| join 永久挂起 | R1 | 子任务终态丢失 | O2 强制顺序 + watchdog（#12）+ 全终态枚举 |
| 巡检被误判 | R2 | 能力上线即被 doom-loop 禁用 | O3：豁免（#14）进 P2 首批 |
| 进度埋点写放大 | R2 | 空闲期也写进度 | 仅在存在挂起 turn/obligation 时开启 |
| `wait` 兼容破坏 | R1 | 脚本/API 客户端受影响 | 兼容期双写 + 保留取值映射，P3 才移除 |
| 长工具调用被误判 `suspect_dead` | R2 | 误杀正常运行的长任务 | 二维 stall 判据（心跳×进度）+ 有产物产出判 `slow` |

---

## 11. 索引：改动项 ↔ 设计章节 ↔ 验收编号

| 改动项 | 设计稿章节 | 阶段 | 验收编号 |
| --- | --- | --- | --- |
| #1 escalate-first | §6.3 / §8 P0 | P0 | AC-P0-1a–e |
| #2 `extend_deadline` | §6.5 / §8 P0 | P0 | AC-P0-2a–f |
| #3 账本字段 | §6.2 | P0（取回字段在 P1） | AC-P0-3a–c |
| #4 wake → resume | §6.1 / §6.6 | P1 | AC-P1-1a–d |
| #5 去阻塞 / 移除 | §6.1 / §16.2 | P1（移除在 P3） | AC-P1-2a–d、AC-P3-1a–c |
| #6 宿主 Busy/熔断 | §6.1 | P1 | AC-P1-3a–d |
| #7 巡检原语 | §6.4 | P2 | AC-P2-1a–e |
| #8 配置项 | §6.3 / §6.5 | P0 | AC-P0-4a–c |
| #9 巡检预约 | §6.4 | P2 | AC-P2-2a–b |
| #10 进度埋点 | §6.6 | P2 | AC-P2-3a–b |
| #11 I9 探测 | §6.13 | **P0-前置** | AC-C0-1a–d |
| #12 I10 watchdog | §16.4 | **P0-前置** | AC-C0-2a–e |
| #13 `wait` 工具族 | §6.9 / §16.3 | P2 | AC-P2-4a–h |
| #14 doom-loop 豁免 | §6.4 | P2（首批） | AC-P2-5a–c |
| #15 幂等键 | §6.6 | P1 | AC-P1-4a–b |
| #16 取回 / GC / takeover | §6.10–§6.12 | P1（取回）+ P3（GC） | AC-P1-4c–e、AC-P3-2a–c |
| #17 审批路由 | §6.14 | P2 | AC-P2-6a–f |
| #18 steer 联动 | §6.15 | P2 | AC-P2-7a–e |
| A6 resume 容量门控 | §6.1 步骤 5 | P1 | AC-P1-5a–c |
| 重启恢复 / 围栏 | §6.11 / §6.12 | P3 | AC-P3-3a–c |
| 兼容与回归 | §7 兼容性约束 / §9 G 组 | P3 | AC-X-1–6 |

---

## 12. 锚点核验记录（施工前实测，2026-09-23）

> 方法：对设计稿 §7 的 #1–#18 逐条核验"文件是否存在 / 引用行号是否确实包含所称构造"（只读核验，未改任何代码）。
> **结论：18 行引用的文件全部存在；所有标注为"新增"的符号经全仓 grep 确认尚不存在**，与改动清单的定位一致。

### 12.1 可直接施工的锚点（精确命中）

| # | 锚点 | 核验结果 |
| --- | --- | --- |
| #1 | `execution_supervisor.go:503-553` | ✅ `else` 分支自 503；`progress_stalled` 508-510；强制取消 526-553（`cancel_requested` 538/542） |
| #4 | `wake_consumer.go:12` | ✅ 第 12 行即 `AutoWakePrompt` 常量（digest 载体在 `digest.go`） |
| #5 | `loop.go:2865-2896` | ✅ 即 sync("wait") 内联阻塞路径（`RunChildren` 2874） |
| #6 | `chat_actor_host.go:794-805`、`806-842` | ✅ `Runnable`（`Busy()` 在 804）/ `Deliver`；30m 硬编码在 826 |
| #8 | `supervision/config.go` | ✅ 字段区 60-99、`WithDefaults:117` |
| #9 | `api/skills/supervision_progress_check.go` | ✅ 宿主级周期巡查（常量 39-47）；注意 `supervision/` 下**无**同名文件 |
| #10 | `TaskProgressInterval` | ✅ `config.go:70-77` + 默认处理 159-161（默认 0 = 显式 opt-in） |
| #12 | `execution_supervisor.go` | ✅ 入口 `ScanOnce:287` / `RunLoop:317` / `evaluateRun:465` / `projectTerminal:624` |
| #13 | `broker.go:45` | ✅ 精确命中 `ToolWaitAgent` |
| #14 | `doom_loop.go:178-184` | ✅ 按工具名整体豁免的 switch（`wait_agent` 181、`wait_team` 186）；`semanticToolCallRepeatExempt:166`、`supervisionInspectTool:203`；**尚无 `progress_seq` 维度**（新增） |
| #17 | `execution_supervisor.go:494-501`、`approval_projection.go`、`projection.go:87-103` | ✅ 494-501 为 waiting_approval/waiting_input 分支（`approval_timeout` 498-499）；`projection.go` 92-103 正是 critical + ActionRequired → `ScheduleWake` |

### 12.2 已按核验结果修正的锚点（正文 §1–§5 已采用修正值）

| # | 设计稿原锚点 | 修正为 | 原因 |
| --- | --- | --- | --- |
| #2 | `types.go:197-202` | **`types.go:193-202`** | 197-202 仅为 ActionKind 常量块尾部 |
| #3 | `execution_run.go` / `execution_store.go` | 补 **`sqlite_store.go:287-327`** | DDL / 迁移实际在 sqlite_store |
| #5 | `loop.go:6948-6952` | 补 **`:6953-6956`** | `wait_timeout_sec` 落在所引范围之外 |
| #7 | `internal/agent` 注册 + `internal/toolbroker` | 收敛为 **`toolbroker/supervision_tools.go:120-122`** + `broker.go:59-63` | supervision 工具注册全在 toolbroker；agent 侧仅名称引用（`doom_loop.go:205`） |
| #11 | `agent.go:551-584`（表述为探测点） | 重述：该处是**工具面门控**；`supportsSuspension()` 为新增、与其**同层放置但用途不同** | 避免把可见性门控误当耐久性探测 |
| #13 | `:1617-1640`、`:3235-3243` | 补 **`:1996-2060`（`wait_agent` 主体）、`:2757-2800`（`wait_team` 主体）**、`loop.go:6961+`（per-task `deadline`） | 原范围实为 `spawn_agent` 参数解析与参数白名单 |
| #16 | `ArtifactRefs` / `ResultSummary` 置于 `execution_run.go` | 补注：同名概念现居 `agent_result.go:58/70/130-131`、`snapshot.go:43-45/127-129`；`ExecutionRun` 现仅有 `ResultRef:104`；GC 仅 `PruneWakeClaims`（`sqlite_store.go:1279`）；`takeover` 仅见于 `team/sqlite_owner_lease.go:14/96` | 避免重复造字段、重复实现 GC |
| #18 | "steer 注入路径" | 补注：**"steer" 为待建概念**，现有载体 `turnInjection`（`actor.go:409-411`）、`startSessionRun`（`:2587`，注入点 `:2615-2617`） | 明确这是**新建**而非改造既有概念 |

### 12.3 核验确认"尚不存在"的新增符号（施工即新增，无冲突）

`extend_deadline`、`supportsSuspension`、`DecisionWindowUntil`、`ExtensionCount`、`ExtendedTotal`、`check_in_after`、`notify_key`、`subagent_status`、`subagent_inspect_task` —— 全仓 grep 无命中，均可安全新增。

### 12.4 测试落点核验

§7.1 列出的测试文件均在仓库中**实际存在**（按目录枚举确认），因此施工以"**扩展既有测试文件**"为默认策略，避免新增孤立测试。

---

## 13. 实施注记（A6 口径与偏差登记，2026-09-23）

> 本节只登记**已落地事实**与**显式偏差**，不改写 §3 的 AC 文本；C2-5 第 295 行与 AC-P1-5a 的口径以本节为准。

### 13.1 A6 已落地事实（含锚点）

| 事实 | 锚点 |
| --- | --- |
| 门控位置＝resume dispatcher 的认领路径（claim → 门控 → deliver），未走旁路 | `backend/internal/supervision/wake_consumer.go:79`（`MaybeWakeParent`）；生产调用点 5 处：CLI `chat_actor_host.go:1062`、`chat_supervision.go:381`；API `session_runtime_support.go:1263`、`supervision_batch_projector.go:137`、`supervision_handlers.go:844`。`DrainRunnable` 直调仅存在于测试 |
| 并发门控（`MaxConcurrent`）＝宿主注入的非阻塞 probe；读数取**子代理 in-flight 槽位**（挂起态不占额度，AC-P1-5c） | `backend/internal/supervision/resume_capacity.go:126`（`NewSubagentCapacityProbe`）；宿主侧 `agent.SubagentConcurrencyLimiter.InFlight()` |
| 探测错误 ⇒ **fail-open**（放行），限流器不可读不得卡死 supervision | 同上（`NewSubagentCapacityProbe` / `CombineResumeProbes`：探测 error ⇒ 跳过/放行） |
| 超限 ⇒ 有界 FIFO 排队：**不丢 wake**、保留 FIFO 位次（只归还 claim，绝不 Resolve 未投递行） | `wake_consumer.go:196`（`deferResume`）；`store.go:58-63` / `sqlite_store.go:1319`（`ReleaseWakePending`，仅 claim owner 可归还） |
| digest 显示**排队位次**（AC-P1-5a 后半句） | `backend/internal/supervision/digest.go`：`Digest.ResumeQueue` + `resume_queue: 排队中 N 条，位次 1/N，最早已等待 …`；实时读未认领行（drain 后自动消失，跨重启一致） |
| 排队超时 / 队列越界 ⇒ escalate（critical + action_required，幂等单行，不静默） | `resume_capacity.go`（`ResumeQueuePolicy` / `ResumeQueueEscalation` / `resumeQueueNotification`） |

### 13.2 深度 / 可见性门控（补丁前口径：显式偏差）

> **2026-09-23 更新**：下列条目记录的是**补丁前**的实施口径。深度门控现已实现（受限放行 + digest `resume_gate` 行），可见性仍为显式偏差。**当前口径以 §13.5 为准。**

- **文档字面**（设计稿 §6.1 步骤 5、G12、A6；本文件 C2-5 第 295 行、AC-P1-5a）：resume 视为"起 turn"，先过并发 / 深度（`MaxDepth=1(+1 hard/expert)`）/ 可见性（`shouldExposeSpawnSubagents`）门控。
- **实施口径**：准入面只对**并发**拒绝；深度 / 可见性**不在 resume 投递前拒绝**。
- **理由**：`delegationPolicy` / `shouldExposeSpawnSubagents` 是**静态策略、不可恢复**；据此拒绝会让**纯汇报型 resume**（子任务已终态、父只需收尾汇报）永久排队并持续升级 —— 即 G12 自述的第二种失败模式（"永久排队变相死锁"）。
- **实际保护未缺失**：resume 回合若要继续派发，仍受同一套门控 —— 深度在**派发面**校验（`backend/internal/agent/scheduler.go:434-450`，hard/expert +1），可见性在**工具面**校验（`backend/internal/agent/agent.go:564-594`），超限返回 `complete_locally_or_use_spawn_team` 语义。
- **代码注释**：`backend/internal/supervision/resume_capacity.go:113-115` 已写明该边界。
- **待拍板（不阻塞）**：若要严格对齐字面，可选等效实现"resume 回合内禁止派发 + digest 显式告知"，需 per-turn 派发禁令注入点（当前 spawn 工具面在 actor 构建期计算）。

### 13.3 口径外边界（非 A6 范围）

| 边界 | 事实 | 判定 |
| --- | --- | --- |
| 父 turn 结束自检（`MaybeSelfCheckParent`） | 只过 `Runnable`（busy）门禁，**不过并发门控**；digest-only、不 claim wake（`wake_self_check.go:149-189`；生产调用点 CLI `chat_actor_host.go:1074`、API `session_runtime_support.go:1293`） | 按 A6 字面（门控点＝认领路径）**不属 A6**；按"起 turn 先过并发门控"的意图属灰区，需拍板 |

### 13.4 验证证据（2026-09-23 实测）

- `go build ./...` exit 0；`go vet ./internal/supervision/` exit 0。
- `go test ./internal/supervision/ -count=1` ok（含 `resume_capacity_test.go`、`resume_queue_digest_test.go` 三个 A6 用例）。
- `go test ./internal/api/skills/ -count=1`、`go test ./cmd/aicli/commands/ -count=1` 均 ok（两宿主接线未回归）。

### 13.5 补丁登记：静态策略门控（受限放行）与验证证据，2026-09-23

> 本节为**当前口径**；§13.2 的“显式偏差”条目保留为补丁前历史。

**口径：三类门控都进 resume 准入面，结果按“可恢复性”分档**

| 门控 | 可恢复性 | 准入面结果 | wake 去向 |
| --- | --- | --- | --- |
| 瞬时容量 `MaxConcurrent`（子代理槽位占满） | 可恢复（等额度释放） | **defer** | 保持 pending + FIFO 位次，额度释放后同一条 wake 重新投递，绝不丢 |
| 静态策略 `MaxDepth`（`depth >= ceiling` ⇒ 该会话重建时 spawn_* 被禁） | 不可恢复 | **受限放行（restricted）** | 照常投递并消费；digest 带 `resume_gate:` 行告知“本回合不得再派发子任务” |
| 静态策略 可见性（`shouldExposeSpawnSubagents` / `delegationPolicy`） | 不可恢复 | **仍未进准入面（显式偏差）** | 仍由工具面硬拒绝（`backend/internal/agent/agent.go:564-594`） |

- 优先级：`defer > restrict > allow`；探测报错一律 fail-open（`CombineResumeProbes`）；`nil` 探测 = 未接线 = 旧行为（回滚开关不变）。
- 静态策略**不得转成 defer**：静态策略等不到，排队即 G12 的“永久排队变相死锁”；受限放行不产生升级通知、不占 FIFO 位次。

**变更清单**

| 文件 | 变更 |
| --- | --- |
| `backend/internal/supervision/resume_capacity.go` | `ResumeCapacityVerdict.Restricted`；`ResumePolicyView` + `NewResumePolicyGate`（永不 defer）；`CombineResumeProbes`（defer > restrict > allow，error fail-open） |
| `backend/internal/supervision/digest.go` | `Digest.ResumeGate` / `ResumeGateNotice` / `SetResumeGate`（重渲染 `Text`）；渲染 `resume_gate: <reason>（<detail>）：本回合不得再派发子任务，请就地收尾或直接汇报` |
| `backend/internal/supervision/wake_consumer.go` | 投递路径：`!Allowed ⇒ deferResume`；`Restricted ⇒ digest.SetResumeGate(...)` 后照常投递 |
| `backend/internal/supervision/resume_gate_test.go`（新增） | 4 用例：永不 defer / 合并优先级与 fail-open / digest 渲染与未接线字节不变 / 端到端（受限 resume 仍投递、wake 被消费、不排队） |
| `backend/cmd/aicli/commands/chat_actor_host.go` | `ResumeCapacity: CombineResumeProbes(容量探测, 深度策略)`；新增 `resumePolicyView`（`depth >= localAgentDepthCeiling` ⇒ restricted；查询失败 fail-open） |
| `backend/internal/api/skills/supervision_batch_projector.go` | 同上（API 唯一 consumer 构造点，batch 终态桥与 `sessionAgentController` 共用）；新增 `resumePolicyView`（`depth >= apiAgentDepthCeiling` ⇒ restricted） |

**验证证据（2026-09-23 实测）**

- `gofmt -l`（4 个改动文件）无输出。
- `go build ./...` exit 0。
- `go test ./internal/supervision/ -count=1` ok（含新增 `resume_gate_test.go`）。
- `go test ./internal/api/skills/ -count=1` ok（38.3s）；`go test ./cmd/aicli/commands/ -count=1` ok（136.1s）。
- 宿主侧**深度半支**已覆盖（2026-09-23 二轮）：`TestLocalHostWiresResumePolicyDepthGate`（`cmd/aicli/commands`）/ `TestAPIHostWiresResumePolicyDepthGate`（`internal/api/skills`）各断言三组——`depth < ceiling` 不受限、`depth == ceiling` 受限放行 + `reason/detail`、会话不可读 fail-open；实测 ok（18.6s / 36.5s 含整包编译）。

**可见性维度为什么不接准入面（2026-09-23 结论：不实施，维持显式偏差）**

- `shouldExposeSpawnSubagents`（`backend/internal/agent/agent.go:564-594`）是 **actor 运行时谓词**：依赖 `scheduler.AllowsDelegation()` + 本轮 callWhitelist + `ToolExecutionPolicy`（`BlockDelegation` / `DeniedTools` / `AllowlistEnabled`），只有 actor 构建后才可知。宿主侧只能近似复刻（如仅看 agentdef 的 delegationPolicy），而**假受限比不告知更糟**：digest 会让本可派发的回合自我禁足。
- 实质判定点就在工具面（`loop.go:3668` / `tool_list.go:193` / `tool_surface_binding.go:129`）：resume 会重建 actor，工具面按当时策略计算；不允许派发时 `spawn_subagents` 根本不在模型可见工具面，wake 照常投递并消费——无丢 wake / 排队 / 升级风险。
- 验收面也不要求：AC-P1-5a（§5 表）只列并发 / 深度。
- 若将来出现「resumed 回合误试派发」的实证，唯一健全形态是**单侧蕴含**：仅当宿主零成本确证 `delegationPolicy == disabled`（该条件下工具面必然隐藏，见 `internal/agent/spawn_subagents_policy_test.go:63-68`）才置 `Restricted=true` + `Reason=ResumeGateVisibility`（词表已预置：`resume_capacity.go:29-31`）。

### 13.6 补丁登记：P2 收尾（C3-1 命名映射 / C3-7 steer 状态），2026-09-23

> 本节为**当时口径**：登记 P2 剩余两项的字面偏差与落地状态（P3 判定见 §13.8：7a 闭环后已可进入）。

**C3-1（改动 #7）工具命名映射（能力等价，字面偏差）**

| 方案字面 | 实际落地 | 能力 |
| --- | --- | --- |
| `subagent_status` | `supervision_snapshot` | 账本总览（summary / items / next_action / next_seq） |
| `subagent_inspect_task` | `supervision_descendants` + `read_agent_result` | 深看（N 行状态矩阵）与有界结果读取（artifact_refs / error_class / truncated） |

- 影响面：AC-P2-1a..1e 与 C3-5 的豁免集合（`wait_agent` / `read_agent_events` / 巡检工具）按**实际工具名**复述；`subagent_status` / `subagent_inspect_task` 全仓零命中（仅存在于本方案文档）。
- 判定：**能力等价，不补建别名**（别名会引入第二套命名与额外的工具面面积）。

**C3-7（改动 #18）steer 状态：L1 / L2 已闭环（7a 见 §13.8，7b / 7e 见 §13.7）**

| AC | 状态 | 证据 |
| --- | --- | --- |
| AC-P2-7c（steer 不改 `progress_seq`、不计入 stall / 延长计数） | **已落地（L1）** | 新增 `backend/internal/supervision/steer_resume_no_count_test.go`：wake 被投递并消费后，目标 obligation 的 `ProgressSeq` / `ExtensionCount` / `ExtendedTotal` / `LastProgressAt` / `DecisionWindowUntil` 逐字段不变 |
| AC-P2-7d（目标已终态 ⇒ 回执带 `next_action`；投递审计 `Queued→Delivered` / `Failed`） | **已落地（L1）** | 新增 `toolbroker.AgentSessionClosedError`（`backend/internal/toolbroker/types.go`）统一两个宿主 4 个调用点的终态回执：保留 `errors.Is(ErrAgentSessionClosed)`（P0-3a 不静默丢弃）并追加 `next_action=inspect\|finalize`；审计状态沿用既有三态矩阵断言（`chat_actor_message_semantics_test.go` / `session_agent_controller_test.go`） |
| AC-P2-7a（挂起态收到 steer ⇒ 起新 episode、同 `turn_id`、账本不变） | **已落地（L2，见 §13.8）** | 事实来源＝durable batch 控制面的 §6.12 挂起记录：提交入口 `nextTurnID` 命中即复用挂起 `turn_id`（同 turn 新 episode、steer 天然置顶），run 收尾 `syncSuspendedTurn` 写回缓存，记录被清则缓存自愈。原登记依据（当时未建）：方案自述 "steer 全仓不存在，属待建概念"；现有载体只有恢复回合（`WakeConsumer` → resume）与 mailbox 投递，尚无"挂起 turn 内联注入"语义 |
| AC-P2-7b（steer 与审批同时到达 ⇒ 审批优先） | **已落地（L2，见 §13.7）** | 两宿主 `send_input` 排队分支在目标 `PendingApproval` 时回执 `next_action=approval_first…`（`toolbroker.AgentSteerApprovalFirstNextAction`）：消息仍排队、不越过审批闸门；`AgentStatusResult.NextAction` 已透出到 `send_input` 摘要 |
| AC-P2-7e（active wait 中 steer ⇒ 等待段立即结束并返回） | **已落地（L2，见 §13.7）** | 两宿主等待段：调用方 ctx 被 steer/ESC/interrupt 打断，或 CLI 侧有新输入排队 ⇒ 立即返回 `interrupted=true` + `next_action=steer_pending…`，不再谎报 `timed_out`（`toolbroker.AgentWaitSteerInterruptNextAction`） |

- 结论（**2026-09-23 修订**）：C3-7 仅剩 **7a（挂起态内联注入）** 未闭环；兼容期（Q6：一个发布周期）约束**已由用户解除**（"插话功能是必要的…不用管旧客户端"）⇒ P3 进入条件收敛为 **P2 DoD = 7a 闭环**。**（2026-09-23 三修：7a 已闭环，见 §13.8 ⇒ P2 DoD 达成、P3 进入条件满足。）**
- 下一步顺序：① ~~C3-7 7a~~（已闭环，见 §13.8）→ ② **P3（C4-1..C4-4）**。

**验证证据（2026-09-23 实测）**

- `gofmt -l`（6 个改动 / 新增文件）无输出（含顺带修正的 `chat_actor_registry.go:937` 对齐）。
- `go build ./...` exit 0。
- `go test ./internal/toolbroker/ -count=1` ok（18.2s）。
- `go test ./internal/supervision/ -count=1` ok（含新增 `steer_resume_no_count_test.go`）。
- `go test ./cmd/aicli/commands/ -run 'TestLocalActorRegistry' -count=1` ok（14.7s）。
- `go test ./internal/api/skills/ -run 'TestSessionAgentControllerV2TerminalTargetReturnsSessionClosed' -count=1` ok（1.1s）。

---

### 13.7 补丁登记：C3-7 steer 收尾（7b 审批优先 / 7e 等待段可打断），2026-09-23

> 用户口径（本轮）：**插话（steer）是必做能力** —— 主 agent 要能对子代理"打断 / 插话 / 继续执行"，且**不再受旧客户端兼容期约束**（"不用管旧客户端"）。据此 §13.6 中"兼容期未走完"一条作废，P2 DoD 收敛为 **C3-7 仅剩 7a**。

**AC-P2-7b（审批优先）—— 已落地（L2）**

| 位置 | 改动 |
| --- | --- |
| `backend/internal/toolbroker/types.go` | 新增 `AgentSteerApprovalFirstNextAction(approvalID, reason)`：`approval_first: the target is blocked on a pending approval (<id> <reason>); call resolve_agent_approval with allow=true\|false before steering — this message stays queued and is injected only after the approval resolves; do not bypass the approval gate`；`AgentStatusResult` 新增 `NextAction`（`json:"next_action,omitempty"`） |
| `backend/internal/toolbroker/broker.go` | `send_input` 摘要透出 `next_action`（模型可见） |
| `backend/internal/api/skills/session_runtime_support.go`、`backend/cmd/aicli/commands/chat_actor_registry.go` | busy + `interrupt=false` 排队分支：目标 `PendingApproval` ⇒ 回执带审批优先引导 |

- 语义边界（重要）：**审批优先靠"排队序"实现，而不是靠丢弃**。busy 态 steer 仍 `Queued→Delivered`（`trigger_turn=true`），而 busy 态消息只在当前 run 结束后注入 ⇒ 注入天然晚于审批解决。若将来引入"busy 态即时内联注入"，必须同时加真正的排队门槛，否则该 AC 失效。

**AC-P2-7e（active wait 中 steer ⇒ 等待段立即结束并返回）—— 已落地（L2）**

| 位置 | 改动 |
| --- | --- |
| `backend/internal/toolbroker/types.go` | `AgentWaitResult` 新增 `Interrupted`（`json:"interrupted,omitempty"`）+ `AgentWaitSteerInterruptNextAction()`；`FinalizeAgentWaitResult` 首分支：`Interrupted` ⇒ 强制 `timed_out=false`、`execution_continues=true`、`next_action=steer_pending…` |
| 两宿主 `Wait` 等待段 | `waitCtx.Done()` 时区分"调用方被打断"（`ctx.Err() != nil`）与"观测窗口超时"：前者 `interrupted=true` 立即返回，后者维持 `timed_out=true` |
| CLI 额外 | `localActorRegistry.localAgentSteerPendingInput()`：等待段内 `host.BaseSession.InputQueue` 出现排队新输入（steer / 新输入）⇒ 立即结束等待段返回 |

- 触发链：CLI ESC/`cancel` → `interruptChatTurnFromBusyInputCancel` → 回合 ctx 取消；API `interrupt` 命令 → `activeTurnRegistry` 取消在途回合 ctx ⇒ 两者都沿 ctx 传播到等待段，因此"立即返回"不需要新线程，也不引入轮询。
- 语义边界：等待段结束**不取消子代理**（`execution_continues=true`）、不改变账本；`interrupted` 与 `timed_out` 互斥（被打断的等待不再谎报超时）。
- 未覆盖（留待 7a 或后续）：API 侧"新输入排队但未 interrupt"的 steer 形态（API 目前只有 `interrupt` 命令，无排队输入通道）；CLI 侧排队输入的识别挂在根会话 `BaseSession`，子代理自身发起的等待以 ctx 打断为准。

**验证证据（2026-09-23 实测）**

- `gofmt -l`（6 个改动文件）无输出。
- `go build ./...` exit 0。
- `go test ./internal/toolbroker/ -run TestFinalizeAgentWaitResultProvidesSchedulingGuidance -count=1` ok（含新增 `interrupted` 断言）。
- `go test ./cmd/aicli/commands/ -run 'TestLocalActorRegistryV2SendInputApprovalFirstNextAction|TestLocalActorRegistryWaitEndsOnCallerSteerInterrupt|TestLocalActorRegistryWaitEndsOnQueuedUserInput' -count=1` ok。
- `go test ./internal/api/skills/ -run 'TestSessionAgentControllerV2SendInputApprovalFirstNextAction|TestSessionAgentControllerWaitEndsOnCallerSteerInterrupt' -count=1` ok。

---

### 13.8 补丁登记：C3-7 7a 挂起态内联注入（同一 turn 的新 episode），2026-09-23

> 用户口径（本轮）：**插话（steer）是必做能力**（主 agent 可打断 / 插话 / 继续执行），且**不再受旧客户端兼容期约束**（"不用管旧客户端"）。7a 是 P2 最后一项；本节登记其落地口径与验证证据，并据此宣告 **P2 DoD 达成、P3 进入条件满足**（§5 进入条件＝P2 DoD + 兼容期结束；兼容期由用户解除）。

**落地形态：不做"窄注入"，做"同一 turn 的新 episode"**

7a 的字面是"挂起 turn 收到 steer ⇒ 起新 episode、同 `turn_id`、账本不变"。落地上把它做成**同一 `turn_id` 的第二次 run**：

| 语义 | 落地 |
| --- | --- |
| 同一 turn | 新 episode 复用挂起 turn 的 `turn_id`（`SessionActor.nextTurnID`） |
| 输入置顶 | steer 文本就是本 episode 的 prompt ⇒ 天然位于请求历史末尾，无需改写历史（设计 §6.15） |
| 账本不变 | episode 的投递不经过 supervision 账本写路径 ⇒ `progress_seq` / 延长计数 / stall 判定逐字段不变（与 7c 同口径） |
| 事实来源 | durable batch 控制面的 §6.12 挂起记录（`subagentbatch.TurnSuspension`，键 `(session_id, turn_id)`）；`RuntimeState.SuspendedTurnID` **只是派生缓存**，每次使用前回查记录，记录被清即停止复用 |

**改动清单**

| 位置 | 改动 |
| --- | --- |
| `backend/internal/chat/runtime_state.go` | `RuntimeState` 新增 `SuspendedTurnID`（`json:"suspended_turn_id,omitempty"`）：记录"本会话当前挂起 turn"，供重启后的冷启动复用 |
| `backend/internal/chat/actor_resume_episode.go`（新增） | `nextTurnID`（挂起 ⇒ 复用，否则 `turn_<uuid>`）；`suspendedTurnID`（内存缓存 → 冷启动 durable 回查 → durable 记录校验，失效则收敛清理）；`parkedTurnRecord` / `turnSuspended`（要求 `BatchStore.IsDurable()`，未接线 / 非 durable 一律按"未挂起"）；`syncSuspendedTurn`（run 收尾写回）；`adoptSuspendedTurnID` / `clearSuspendedTurnID`（收敛写） |
| `backend/internal/chat/actor.go` | ① `handleSubmitPrompt` / `handleContinueSession` 改用 `nextTurnID`；② 审批 / 问答恢复入口（`state.CurrentTurnID` 为空时）同样改走 `nextTurnID`——resume 也是一次续跑；③ run 收尾 `publishTerminal` 之后用 `syncSuspendedTurn` 写回 `SuspendedTurnID` |
| `backend/internal/chat/actor_resume_episode_test.go`（新增） | 三个用例（见下） |

**测试落点（L1 / L2）**

| 用例 | 断言 |
| --- | --- |
| `TestSubmitPromptOnSuspendedTurnResumesSameTurnID` | 真 durable SQLite batch store + 已挂起记录 ⇒ `SubmitPrompt` 后 `PrepareRun` 观测到的 run `turn_id` 等于挂起 `turn_id`；steer 文本进入会话历史；episode 结束后 `CurrentTurnID==""` 且 `SuspendedTurnID` 仍是挂起 turn；挂起记录的 `ObligationIDs` / `ResumeQueue` / `ParkedAt` 逐字段不变（账本不变） |
| `TestSubmitPromptClearsStaleSuspendedTurnID` | 只有缓存、没有挂起记录（记录已被清 / 放弃）⇒ 提交不得复用旧 `turn_id`，且陈旧缓存被清掉（EC-E1"turn 永不结束"防线） |
| `TestRunEndStampsSuspendedTurnIDForParkedTurn` | 回合内落盘 §6.12 挂起记录（模拟 loop 的 background 派发）⇒ 收尾后 `SuspendedTurnID` 等于本回合 `turn_id`，下一次 steer 才能落回同一 turn |

**语义边界 / 留白**

- 判读失败一律 fail-open 回旧行为：未接线 / 非 durable store / 读错误 ⇒ 按"未挂起"处理，新开 turn；**绝不丢输入、绝不静默改账本**。
- 宿主 store 的 durability 决定 7a 是否生效：CLI 宿主按会话落文件（`resolveLocalChatSubagentBatchStorePath`，ephemeral 会话除外）⇒ durable；API 宿主需 runtime-server 调 `EnableDurableSubagentBatches(dir)` 注入文件型 store（`supervision_batch_recovery.go:289`），否则默认是进程内内存库（`resolveBatchDSN` 无 Path/DSN 时生成 uuid 内存 DSN）⇒ 按"未挂起"fail-open：不误伤，但也不复用 `turn_id`。
- `startSessionRun` 内的兜底默认（`turnID == ""` ⇒ 新 `turn_<uuid>`）保留：它只服务直调（测试 / 内部），三个真实入口（submit / continue / 审批恢复）都走 `nextTurnID`。
- 宿主（`internal/api/skills/handler.go`）在 agent ctx 上携带的 `turn_id` 是**事件渲染身份**，与 actor 的 run `turn_id` 无关：actor 在 `startSessionRun` 里用 run `turn_id` 覆盖 ctx 值（该行为本次改动前后一致）。
- busy 态 steer 仍走排队（`Queued→Delivered`）：7b 的"审批优先"依赖排队序；7a 覆盖的是**挂起态（无在途 run）**的 steer。
- API 侧"新输入排队但未 interrupt"的 steer 形态仍缺（同 §13.7 留白）：API 目前只有 `interrupt` 命令，无排队输入通道。

**验证证据（2026-09-23 实测）**

- `gofmt -l`（4 个改动 / 新增文件）无输出。
- `go build ./...` exit 0；`go vet ./internal/chat/` exit 0。
- `go test ./internal/chat/ -count=1` ok（37.5s，含上述三个新用例）。
- `go test ./internal/api/skills/ -count=1` ok（39.1s）；`go test ./cmd/aicli/commands/ -count=1` ok（137.0s）。

**P3 进入判定**

- §5 进入条件＝**P2 DoD + 兼容期结束**：P2 登记项由 §13.6 / §13.7 / 本节共同闭环（C3-1 能力等价不补别名；C3-7 七项全闭环）；兼容期约束已由用户解除。
- ⇒ **P3 可进入**，首项 C4-1（删除阻塞分支与 `wait` 取值，`execution_mode` 标记 deprecated）。

### 13.9 补丁登记：P3 首项 C4-1（删除阻塞分支与 `wait` 取值），2026-09-23

> 进入条件（§5：P2 DoD + 兼容期结束）已由 §13.6 / §13.7 / §13.8 满足，本轮实施 **C4-1：删除内联阻塞分支与 `wait` 取值残留**，`execution_mode` 标记 **deprecated（旧值仍可用）**。

**落地形态：派发只有异步语义，旧值只读不选路**

| 语义 | 落地 |
| --- | --- |
| 内联阻塞分支 | 删除。`spawn_subagents` 一律走 `startBackgroundSubagentBatch` ⇒ 回执是 batch 句柄（`batch_id` / `execution_mode=background` / `parent_action=continue_parent_turn…` / `task_count`），父 turn 不再等待子任务 |
| 旧 `execution_mode` 取值 | 空值、`wait`、`sync` 仍解析为 `ExecutionModeWait`（旧持久化行与旧调用方继续可读），但**不再选择执行路径**；显式传 `wait` / `sync` 时回执追加 `execution_mode_deprecated` 提示（AC-P3-1b） |
| 宿主不支持异步 | 不再静默退回阻塞：返回模型可见错误 `spawn_subagents requires asynchronous background dispatch, but this host has background subagent batches disabled or no batch coordinator` |
| 账本口径 | 即便调用方传 `wait`，durable 账本仍落 `background`（监督查询按该值过滤，语义已无阻塞） |
| 父上下文预算 | 父 turn 只拿到句柄；子任务输出不再进入父上下文（原"截断 + artifact 引用"路径随阻塞分支一并删除） |

**改动清单**

| 位置 | 改动 |
| --- | --- |
| `backend/internal/agent/loop.go` | 删除 `spawn_subagents` 内联阻塞分支及其等待 / 聚合路径；派发统一异步；句柄额外以 `subagent_batch_id` 观测指标暴露（供宿主摘要统计"已派发"） |
| `backend/internal/agent/agent.go` | `SetSubagentBackgroundEnabled` 注释更新：禁用 ⇒ 显式失败，不再回退同步路径 |
| `backend/internal/agent/suspension_gate.go` | I9 降级语义改为"派发仍异步成功、但 parked turn 无法跨重启恢复"；`SupportsSuspension` 注释同步 |
| `backend/internal/subagentbatch/types.go` | `ExecutionModeWait` 标 deprecated（常量保留供旧行解析）；`ParseExecutionMode` 兼容映射不变；新增 `LegacyExecutionModeNotice`（一次性提示，`background` / 空值不提示） |
| `backend/internal/api/skills/handler.go` | 观测摘要新增 `subagent_dispatched`（按 `subagent_batch_id` + `subagent_count` 统计已派发批次与任务数）；`summarizeSubagents` 在"只有派发、没有报告"时不再返回 nil |

**测试落点（L1）**

| 用例 | 断言 |
| --- | --- |
| `TestReActLoop_Run_SpawnSubagentsDispatchesAsyncBatchHandle`（`internal/agent/loop_test.go`） | 父 turn 只发 2 次模型调用（派发 + 收尾）；回执含 `batch_id=batch_` / `execution_mode` / `parent_action` / `task_count`；lifecycle 投影 `ExecutionModeBackground` + `BatchCompleted`；子任务请求由 detached worker 单独消费 |
| `TestSpawnSubagentsLargeResultStaysOutOfParentContext`（`internal/agent/subagent_parent_summary_test.go`） | 父工具消息 ≤ 12KiB、含 `batch_id` / `continue_parent_turn`、不含子输出；父 metadata 无 `subagent_reports` |
| `TestReActLoop_Run_EmitsRuntimeEvents` / `TestSpawnSubagentsInjectsPromptBuilderIntoChild` | 事件与子提示词注入在异步语义下仍成立（trace 断言收窄到父 turn 自己的里程碑，detached worker 事件不纳入） |
| `TestExecutionModeLegacyValuesStayReadableAndWarn` / `TestSpawnSubagentsLedgerAlwaysRecordsBackground` / spawn_subagents 工具定义用例（`internal/agent/suspension_gate_test.go`） | 旧值可解析 + deprecation 提示 + 错拼仍拒绝；账本恒为 `background`；`execution_mode` 参数描述标 Deprecated 且不再提供 enum（AC-P3-1a/b） |
| `TestAgentChat_EnableReAct_ExposesSubagentSummary`（`internal/api/skills/handler_test.go`） | 异步口径：`subagent_summary.batches=1` / `dispatched=1` / `count=0` / `successful=0` / `roles=[]`；`testSequenceLLMProvider` 加锁（detached 子任务与父 turn 可能并发调用同一 provider 实例） |

**语义边界 / 留白**

- 父 turn 结束时的摘要只反映"**已派发**"；`successful` / `roles` / patch 计数等终态字段由批次终态投递（resume / 监督通道）在**后续 turn** 的观测里补齐 —— AC-P3-1c（兼容期双写：batch handle + resume 聚合报告）的端到端取证仍是 L2 留白。
- `subagent_reports` 指标未删除：orchestrator / planner 路径（`agent_planned_subagents`）与 resume 侧聚合继续使用；`summarizeSubagentReportsForParent`（有界父投影）保留给 resume 聚合，其内联调用方已随阻塞分支删除。
- 旧客户端兼容：句柄字段（`batch_id` / `status` / `task_count` / `parent_action`）不变；新增 `subagent_batch_id` 观测指标与 `subagent_summary.dispatched` 属附加字段。
- 不再有"宿主不支持异步 ⇒ 退回阻塞"的降级：I9 降级现在只影响"能否跨重启恢复挂起 turn"。

**验证证据（2026-09-23 实测）**

- `gofmt -l internal/agent internal/subagentbatch` 无输出；`internal/api/skills` 的 `gofmt -l` 输出均为改动前既有偏差（本次改动的 `handler.go` / `handler_test.go` 不在其中）。
- `go test ./internal/agent/ -count=1` ok（14.8s）；`go test ./internal/api/skills/ -run TestAgentChat_EnableReAct_ExposesSubagentSummary -count=1` PASS（0.8s）。
- `go test ./... -count=1`（backend 全量，285s）：除 `internal/toolbroker` 的 `TestReliabilityEvalBrokerTimeoutRetryUsesNewInvocationWithoutDuplicateSideEffect` 在全量并发负载下超时外全绿；该用例单包复跑（`-run` 单用例，26s）通过 ⇒ 负载相关既有 flake，与本次改动无调用关系（改动不涉及 `internal/toolbroker`）。
- 残留断言（AC-P3-1a）：`grep -E 'runSubagentsInline|executeSubagentsSync|awaitSubagentBatch|waitForBatch|blockingDispatch|syncDispatch'`（`internal/agent`，`*.go`）无匹配；`go build ./...` 通过。

**AC 判定**

| AC | 判定 | 证据 |
| --- | --- | --- |
| AC-P3-1a（无残留调用） | ✅ L1 | 编译通过 + 上述 grep 空结果 + 阻塞路径测试改为异步句柄断言 |
| AC-P3-1b（旧值可用 + 提示） | ✅ L1 | `TestExecutionModeLegacyValuesStayReadableAndWarn` / `TestSpawnSubagentsLedgerAlwaysRecordsBackground` / spawn_subagents 工具定义用例 |
| AC-P3-1c（双写不破坏旧客户端） | ⏳ L2 | 句柄侧 L1 已覆盖；resume 聚合报告的端到端取证待真实运行（留白如上） |

---

### 13.10 补丁登记：P3 第二项 C4-2（保留窗口 / GC / 驱逐后重建 / takeover），2026-09-23

> 进入条件（§5 顺序：C4-1 → **C4-2** → C4-3 → C4-4）已由 §13.9 满足。本轮实施 C4-2 四块：**保留窗口**、**GC 三条件**、**驱逐后由 store 重建**、**`takeover` 授权动作**；并顺带闭环挂起 turn 生命周期上两个既有缺口（**EC-E1「turn 永不结束」**、**EC-E7 / EC-C5「中断不放弃挂起记录」**）——GC 的保留锚点是"turn 终局 + N 天"，turn 不终局则终态行永不到期、GC 永不触发，故属同一交付。

**落地形态（对照 §5 C4-2 表）**

| 需求字面 | 落地 |
| --- | --- |
| 保留窗口：终态 obligation 保留至 **turn 终局 + N 天**（默认 7，可配）；窗口内 `subagent_status` 可见 | `ExecutionRunPrunePolicy{Now, Retention, Limit}`；默认 `DefaultExecutionRunRetention = 7d`、`DefaultExecutionRunPruneLimit = 200`、硬上限 `MaxExecutionRunPruneLimit = 1000`（`backend/internal/supervision/execution_store.go:105-129`）。可配面：`Config.ExecutionRunRetention` / `ExecutionRunPruneLimit`（`config.go:137-143`，**0 取默认，不是"关 GC"**；`config.go:284-290` 透传）→ `ExecutionSupervisorConfig`（`execution_supervisor.go:76-82`，零值兜底 `:1253-1259`）。窗口内行照常可读（读路径与 GC 同源同 store） |
| GC 条件：仅清"已 resolved **且** 无 artifact 引用 **且** 无 pending resume" | 三条**内联在同一条 DELETE 的子查询**里（不是 read-then-delete，两步之间的状态变化不会误删）：① `status IN (终态) AND finished_at` 非空且 ≤ cutoff；② `result_ref = ''`（无 artifact 引用）；③ `NOT EXISTS` 未投递 wake（`supervision_wake_pending`）**且** `NOT EXISTS` 未投递 outbox（`supervision_completion_outbox.delivered_at IS NULL`）——`execution_store.go:568`（SQL 主体 `:601-634`） |
| 批量删除、单次有界 | `ORDER BY finished_at ASC LIMIT ?`：**最旧优先**，单次 ≤ `Limit`（超 1000 收敛到硬上限）；巡检每次最多一批（`execution_supervisor.go:563`），由 `executionRunPruneInterval = 1h` 节流（`:549`）；GC 失败只记 `Stats.LastPruneError`，**不替换扫描结果**（`:395-398`） |
| **不得影响**：当前 turn 的账本视图 / 决策窗口内的行 / `orphan_suspected` 待处置行 | 同一语句内排除：决策窗口未过期（`decision_window_until` 空或 ≤ now）；`turn_id` 仍有"未终态**或**未过 cutoff"的同行 ⇒ **整条 turn 一起留**（`:626-633`）；`orphan_suspected` 属 supervision 状态、不在 `runTerminalStatuses` 中 ⇒ 被状态过滤天然排除 |
| 驱逐后可重建：账本是持久真源 | 运行时**不持有 run 的内存副本**：读路径全部经 `ExecutionRunStore` 能力断言直读 store（`alerts.go:81`、`snapshot.go:179`、`projection.go:200`、`api/skills/handler.go:5290`、`api/skills/supervision_handlers.go:258`、CLI `chat_actor_host.go:2900`）。行被 GC 后读回 `ErrRunNotFound`（`execution_store_test.go:92`），调用方按"缺失"重建视图；未实现该能力的宿主自动跳过 run 视图（降级不报错，`projection_run_finalize_test.go:11` 的 `notificationOnlyStore` 即该形态） |
| 授权动作：`takeover`（新增动作，带 `reason`，写审计） | `ActionTakeover`（`types.go:207-212`）；`Evaluator` 对 `SubjectAgentRun` announce（宿主中立，`evaluator.go:64-69`）；`isMutationAction` 收录（`action_service.go:541-543`）；`validateActionRequest` 收录且 `reason` 必填（`:564-571`，I4）；**控制面自执行**、不走宿主 executor（`:331-335`） |

**`takeover` 语义（§6.11）**

| 面 | 落地 |
| --- | --- |
| 前置拒绝 | 目标非 agent run ⇒ `HintTakeoverTargetMissing`；终态 run ⇒ `HintTakeoverTerminal`；**已是 owner** ⇒ `HintTakeoverNotNeeded`（`action_service.go:855-866`，判定 `:914` / `:919`） |
| 非 owner 变更 | 有 live owner lease 时 mutation 被拒，next_action = `owner_lease_held_use_takeover`（`HintOwnerLeaseHeld`，`:897`）⇒ 显式 takeover 是唯一授权路径（不得静默越权写） |
| 持久化 | `TakeoverExecutionRun`（`execution_store.go:646`）：`UPDATE … WHERE run_id = ? AND fencing_token = ? AND status NOT IN (终态)` ⇒ **fencing token 上的 CAS**（两个并发 takeover 只有一个能赢，EC-B9）；成功后 `owner_id` 改写、`fencing_token + 1`、`version + 1`、发新 lease |
| 审计 | 生命周期事件 `obligation.ownership.taken_over`（SeverityWarning，`reason` 记 previous → new owner，`ActorID` = 发起会话），投影到父 digest（`:945-977`） |
| 边界 | 空 owner / 空 run_id ⇒ 报错（非静默 no-op，`:654`）；行在 takeover 期间消失 ⇒ `ErrActionConflict`（`:939`） |

**改动清单**

| 位置 | 改动 |
| --- | --- |
| `backend/internal/supervision/execution_store.go` | `ExecutionRunStore` 新增 `PruneExecutionRuns` / `TakeoverExecutionRun`（`:84` / `:88`）；`ExecutionRunPrunePolicy` 与默认/上限常量；两个 SQL 实现（GC 单语句三条件 + 最旧优先有界；takeover CAS） |
| `backend/internal/supervision/config.go` | `ExecutionRunRetention` / `ExecutionRunPruneLimit` 配置项（0 取默认，显式值透传） |
| `backend/internal/supervision/execution_supervisor.go` | `ScanOnce` 搭车 GC（`pruneRetention`，1h 节流，失败只记 Stats）；`Stats` 新增 `Retention` / `PrunedTotal` / `LastPruneAt` / `LastPruneRemoved` / `LastPruneError`（`:188-196`） |
| `backend/internal/supervision/sqlite_store.go` | 迁移 **v7**：`idx_supervision_runs_terminal(status, finished_at)`（否则每次巡检全表扫描，`:418-427`） |
| `backend/internal/supervision/types.go` / `evaluator.go` / `action_service.go` | `ActionTakeover` + announce + `executeTakeover` + 四个稳定 next_action 提示 + 审计事件 |
| `backend/internal/subagentbatch/turn_suspension.go` | `TurnObligationsSettled`（`:96`）+ `ObligationBatchIDs`（`:128`）——settle / abandon 共用同一份 obligation 解析规则 |
| `backend/internal/agent/loop.go` | 回合边界收尾清账 `settleParkedTurnOnRunEnd`（`:6619`，`defer` 注册于 `:625`） |
| `backend/internal/chat/actor_abandon.go`（新）/ `chat/actor.go` | `AbandonSuspendedTurn`（`:39`）+ interrupt 钩子（`actor.go:1236`）与事件载荷 `abandoned_turn_id` / `abandon_error` |

**配套闭环：挂起 turn 的"结束"与"放弃"**

| 缺口 | 落地 |
| --- | --- |
| EC-E1「turn 永不结束」 | 派发方（loop）在回合边界清账：`subagentbatch.TurnObligationsSettled` 判定账本内 obligation **是否全部终态**，是则清挂起记录（否则 `nextTurnID` 永久复用挂起 `turn_id`）。**行缺失不算完成**（无证据 ⇒ 保持挂起，绝不静默丢 obligation）；run ctx 常已取消 ⇒ 用 detached ctx + 5s 超时；清账失败只报 I9 降级、绝不失败本回合 |
| EC-E7 / EC-C5「中断不放弃挂起记录」 | 用户 ESC / interrupt ⇒ 放弃挂起 turn：**先级联取消账本内全部 obligation（`canceled`），全部成功之后才清记录**；任一失败即中止并保留记录（可重试）。解析顺序＝内存缓存 → durable 读回（冷缓存 / 重启）→ 被中断 run 的 `turn_id`——挂起态本身没有 run，`run` 为 nil 时仍要能找到记录 |
| 共用解析规则 | `ObligationBatchIDs`：`ResumeQueue` 优先、`ObligationIDs[0]` 兜底 ⇒ settle 读的与 abandon 取消的是**同一份** obligation，不存在"清记录时漏取消"的窗口 |

**测试落点（L1）**

| 用例 | 断言 |
| --- | --- |
| `TestPruneExecutionRuns_RemovesOnlyEligibleRows`（`execution_gc_test.go:57`） | 六类行中**只有**全条件满足的一行被删：窗口内 / artifact 引用 / pending wake / pending outbox / 决策窗口内 / `orphan_suspected` **全部保留**（AC-P3-2a 正反例） |
| `TestPruneExecutionRuns_AnchorsRetentionOnTurnFinalization`（`:113`） | 锚点是 **turn 终局**：同 turn 一行过期一行未过期 ⇒ 都不删；两行都过期 ⇒ 才删 |
| `TestPruneExecutionRuns_BoundedBatchesDrainOldestFirst`（`:147`） | `Limit=2` 时单次只删**最旧**两行，多轮 drain 至空 |
| `TestPruneExecutionRuns_ZeroPolicyUsesDefaults`（`:176`） | 零值策略取默认窗口（读回 `DefaultExecutionRunRetention`）与默认批量 |
| `TestExecutionSupervisor_ScanOncePrunesExpiredRuns`（`:194`） | GC 搭车 `ScanOnce`，`Stats.PrunedTotal` / `LastPruneAt` / `Retention` 有读数 |
| `TestTakeover_ClaimsOwnershipWithAuditTrail`（`takeover_test.go:50`） | takeover 改写 owner + 审计事件落库（含 reason）；**不需要宿主 executor** |
| `TestTakeover_UnblocksCrossOwnerMutation`（`:106`） | 非 owner + live lease ⇒ mutation 被拒（`owner_lease_held_use_takeover`）；显式 takeover 后**同一 mutation 放行** |
| `TestTakeover_Rejections`（`:168`） | 终态 ⇒ `takeover_requires_live_run`；已是 owner ⇒ `already_owner_issue_action_directly`；目标缺失 ⇒ `takeover_requires_agent_run_subject` |
| `TestTakeoverExecutionRun_CASAndTerminalGuard`（`:237`） | store 侧 CAS：token 不匹配 / 终态行都不生效（AC-P3-2c 的 durable half） |
| `TestTurnObligationsSettled`（`subagentbatch/turn_suspension_settled_test.go:14`） | 全终态 ⇒ settled；含非终态 ⇒ 不 settled；**行缺失 ⇒ 不算完成** |
| `TestSettleParkedTurnOnRunEnd`（`agent/turn_settlement_test.go:19`） | 回合边界清账：obligation 全终态 ⇒ 记录被清；未终态 ⇒ 记录保留 |
| `TestAbandonSuspendedTurnCancelsObligationsAndClearsRecord` / `…SkipsTerminalAndMissingObligations` / `TestInterruptAbandonsSuspendedTurn`（`chat/actor_abandon_test.go:18/61/97`） | 放弃＝级联取消 + 清记录；终态行与缺失行分别归类；interrupt 钩子生效（EC-E7） |
| `capabilities_test.go` 四处 allowed 集合断言 | `takeover` 进入 announce 集合；`extend_deadline` 与 `takeover` 都**不需要**宿主 executor（快照注释同步） |

**语义边界 / 留白**

- **AC-P3-2b 仍是 L2 留白**：本轮取证到 **store 侧**（删除条件、缺失行读回 `ErrRunNotFound`、能力缺失宿主降级），未跑"真实宿主 + 真实驱逐 + `subagent_status` 读数比对"的端到端。
- 保留窗口按"**turn 终局**"锚定（同 turn 全部行终态、且最旧一行过 cutoff 才整条删），比字面"每行 `finished_at` + N 天"更保守 —— 为满足"当前 turn 的账本视图不得受影响"。
- GC 是**机会性**的（搭车巡检 + 1h 节流 + 单次有界）：不承诺"到点即删"，只承诺有界、最旧优先、不误删；驱逐后读数由 store 决定。
- `takeover` 目前只对 `SubjectAgentRun` announce；团队 / 子代理主体仍走既有 cancel / close 路径。
- 决策行消耗与再动作的既有语义不变：每个 mutation 终态会折叠出 `action_<action>_resolution` 行并把 subject 收敛为 `inspect` ⇒ **连续两次 mutation 之间必须等下一轮决策行**（这是生产语义，不是测试特例）。

**验证证据（2026-09-23 实测）**

- `go build ./...` exit 0；`gofmt -l internal/subagentbatch` 无输出（其余包为改动前既有偏差）。
- `go test ./internal/supervision/ -count=1` **ok**（5.997s，含 `execution_gc_test.go` 五个 GC 用例与 `takeover_test.go` 四个 takeover 用例）。
- `go test ./internal/subagentbatch/ ./internal/agent/ -count=1` 均 ok；`go test ./internal/chat/ ./internal/agent/ ./internal/api/skills/ -count=1` 均 ok（39.6s / 15.8s / 40.9s，三宿主接线未回归）。
- `go test ./... -count=1`（backend 全量，约 5 分钟）：除 `internal/toolbroker` 的 `TestReliabilityEvalBrokerTimeoutRetryUsesNewInvocationWithoutDuplicateSideEffect` 在全量并发负载下超时外**全绿**；该用例即 §13.9 记录的同一负载相关既有 flake（本次改动不涉及 `internal/toolbroker`，单包复跑通过）。
- `grep -E 'extend_deadline'`（`backend`，排除 `internal/supervision`）无匹配 ⇒ 无其他包硬编码 announce 集合需要同步。

**AC 判定**

| AC | 判定 | 证据 |
| --- | --- | --- |
| AC-P3-2a（GC 三条件正反例；"不得影响"三类必须保留） | ✅ L1 | `TestPruneExecutionRuns_RemovesOnlyEligibleRows`（六类保留行全在场）+ `…_AnchorsRetentionOnTurnFinalization` + `…_BoundedBatchesDrainOldestFirst` |
| AC-P3-2b（驱逐内存态后账本由 store 重建，`subagent_status` 读数一致） | ⏳ L2 | store 侧已覆盖（缺失行 ⇒ `ErrRunNotFound`；读路径无内存副本，全经 `ExecutionRunStore` 能力断言）；端到端读数比对留白如上 |
| AC-P3-2c（非 owner 变更 ⇒ 需 `takeover` 且写审计，I4） | ✅ L1 | `TestTakeover_UnblocksCrossOwnerMutation`（拒绝 → takeover → 放行）+ `TestTakeover_ClaimsOwnershipWithAuditTrail`（ActorID + reason）+ `TestTakeoverExecutionRun_CASAndTerminalGuard` |
| EC-E1 / EC-E7 / EC-C5（挂起 turn 的结束与放弃） | ✅ L1 | `TestSettleParkedTurnOnRunEnd` / `TestTurnObligationsSettled` / `TestAbandonSuspendedTurn*` / `TestInterruptAbandonsSuspendedTurn` |

**下一步**：P3 第三项 **C4-3（重启恢复与围栏，§6.11 / §6.12）** —— AC-P3-3a–c。

---

### 13.11 补丁登记：P3 第三项 C4-3（重启恢复与围栏），2026-09-23

> 进入条件（§5 顺序：C4-1 → C4-2 → **C4-3** → C4-4）已由 §13.10 满足。本轮实施 C4-3 两块：**宿主重启时的"恢复 or orphaned"决策（含围栏）** 与 **挂起 turn 的重启读回**；并顺带闭环一处语义缺口：挂起 turn（§6.7 `awaiting_obligations`）在状态投影里被读成"空闲"（**假空闲**），会让重启后的第二个 turn 与 resume 抢跑（AC-P3-3c）。

**落地形态（对照 §5 C4-3 表）**

| 需求字面 | 落地 |
| --- | --- |
| 宿主重启路径：交互式宿主的"**恢复 or orphaned**"决策；不可恢复 ⇒ `orphaned`（终态）+ 告警 | `ExecutionSupervisor.ReconcileRestart(ctx, RestartReconcilePolicy)`（`execution_supervisor.go:1075`）：启动时一次对账，读 `ListActiveExecutionRuns`（非终态、最旧优先、`Limit` 有界）后逐条判定，不可恢复 ⇒ `fenceRun` 置 `orphaned` + critical 投影。宿主接线在启动恢复批次内（`chat_actor_host.go:1227-1235`），与 `RecoverStaleBatches` / `ReplayTerminalDeliveries` 同批、共用宽限期 `localSubagentBatchRestartGrace = 5m`（`:50`）；构造器 `newLocalExecutionSupervisor`（`chat_actor_execution_supervisor.go:90`）复用同一份 durable 接线与配置，**不安装、不起 loop**（一次性对账不引入第二个 watchdog） |
| `fenceOrphaned`（`execution_supervisor.go`）：提升 fencing token，阻断晚到写入 | `fenceOrphaned` 收敛为薄封装（`:981`），共享围栏原语 `fenceRun(ctx, run, source, keepExistingSource, now)`（`:990`）：`Get` 最新行 → 已终态即成功（**幂等**）→ `FencingToken + 1` / `Status = orphaned` / `FinishedAt` / `CancelSource` → `UpdateExecutionRunCAS(fenced, current.Version)`；CAS 输掉时回查终态再定性。晚到写入因 version 已前进被拒（`ErrRunConflict`，EC-B9） |

**语义决策**

| 面 | 决策 |
| --- | --- |
| 判据 = **worker 存活**，不是"进程里有没有内存态" | `last = max(last_heartbeat_at, updated_at)`；`last + StaleAfter > now` ⇒ 宽限期内 ⇒ **保留**（另一个宿主可能还活着，或本进程刚接手 —— 这就是恢复分支）；否则不可恢复 ⇒ 围栏。`StaleAfter` 零值取 `DefaultRestartReconcileGrace = 5m`，`Limit` 零值取 `512`，两者与宿主既有 batch 恢复同口径 |
| 宿主挂钩（`Recoverable` 谓词） | `RestartReconcilePolicy.Recoverable func(*ExecutionRun) bool`：宿主已知这条 run 可恢复（例如该 run 的挂起 turn 仍有 durable §6.12 记录）⇒ **一律保留**，不看心跳。这让"重启恢复"由账本事实决定，而不是靠时间猜 |
| 非 enforce 只观察 | `Mode != "enforce"` 时记 `Report.Observed` 并**不写账本**（dry-run 与生产走同一代码路径，便于先观察后放行） |
| 幂等 / 有界 | 已终态行不再出现在 `ListActiveExecutionRuns`；第二次 pass 的 `Scanned` 不含它，`FencingToken` / `Version` 各只 +1（不会因重复启动反复围栏） |
| 告警契约 | `projectRestartOrphan`（`:1141`）投影 `run_orphaned` + `SeverityCritical` + `SupervisionOrphaned` + `next_action = inspect run and decide cancel/retry`：重启判决不只在账本里，父会话的决策面看得到（与其它非健康判定同契约） |
| 挂起 turn 的重启读回（AC-P3-3a） | 账本（`turn_suspensions` 表）与 resume 队列是 durable 真源：新进程用**新句柄**打开同一份文件即可读回 `ObligationIDs` / `ResumeQueue`；actor 的解析顺序＝内存缓存 → durable 状态 → **账本验证记录仍在**（`actor_resume_episode.go:40-64`），因此重启后仍复用挂起 `turn_id`（与 AC-P1-1a 同口径），episode 不触碰账本 |
| `awaiting_obligations` 与 `Busy()`（AC-P3-3c） | `RuntimeStateSummary` 增加 `SuspendedTurnID`（来自 durable 状态，`runtime_state.go:130-133`）；`Busy() = statusBusy() 或 AwaitingObligations()`（`:168`，Q10「托管 turn 期间不允许并发新 turn」），`AcceptsResume() = 非 statusBusy()`（`:176`）—— 挂起 turn **不是空闲**，但同一 turn 的 resume episode（steer / wake resume / 巡检投递）仍放行。三个宿主调用点按语义分流：`chat_actor_host.go:816`、`chat_actor_progress_check.go:262` 用 `AcceptsResume()`，其余 Busy 语义点不变 |

**改动清单**

| 位置 | 改动 |
| --- | --- |
| `backend/internal/supervision/execution_supervisor.go` | `ReconcileRestart`（`:1075`）+ `RestartReconcilePolicy`（`:1045`）/ `RestartReconcileReport`（`:1061`）+ 常量（`:1033-1042`：`DefaultRestartReconcileGrace` / `DefaultRestartReconcileLimit` / `RunCancelSourceRestartUnrecoverable`）；共享围栏原语 `fenceRun`（`:990`，`fenceOrphaned` 收敛为薄封装 `:981`）；`projectRestartOrphan`（`:1141`） |
| `backend/cmd/aicli/commands/chat_actor_execution_supervisor.go` | `newLocalExecutionSupervisor()`（`:90`）：不安装到宿主、不起扫描 loop 的一次性构造器，复用 `localExecutionSupervisorConfig` 的 durable 接线 |
| `backend/cmd/aicli/commands/chat_actor_host.go` | 启动恢复批次内调用 `ReconcileRestart`（`:1227-1235`，宽限期复用 `localSubagentBatchRestartGrace`）；wake consumer 的 `Runnable` 判定改为 `AcceptsResume()`（`:816`） |
| `backend/cmd/aicli/commands/chat_actor_progress_check.go` | 巡检投递的父会话空闲判定改为 `AcceptsResume()`（`:262`） |
| `backend/internal/chat/runtime_state.go` | `RuntimeStateSummary.SuspendedTurnID` + `AwaitingObligations()` + `Busy()` 纳入挂起 turn + `AcceptsResume()`；原 `Busy()` 更名 `statusBusy()`（`:145-178`） |
| `backend/internal/chat/actor_resume_episode_test.go` | 测试夹具暴露 `apiAgent` / `llmRuntime` / `batchesPath`：重启用例需要"新进程用新句柄打开同一份账本" |

**测试落点（L1）**

| 用例 | 断言 |
| --- | --- |
| `TestReconcileRestartFencesUnrecoverableRunsAndBlocksLateWrites`（`supervision/restart_reconcile_test.go:37`） | 两条遗留 run：宽限期外无心跳 ⇒ `orphaned` + `FencingToken + 1` + critical 通知落库（`run_orphaned` / `SeverityCritical` / SubjectID = run_id）；旧持有者拿着围栏前的 version 回写 ⇒ `ErrRunConflict` 且终态判决不被改写；宽限期内的一行逐字段未动（AC-P3-3b 正反例） |
| `TestReconcileRestartKeepsRecoverableRunsAndRepeatsIdempotently`（`:111`） | `Recoverable` 命中 ⇒ 保留且 `Version` / `FencingToken` 不动；去掉谓词后围栏；第二次 pass `Scanned = 0`、token / version 各只 +1 |
| `TestReconcileRestartObserveModeRecordsWithoutWriting`（`:167`） | `Mode = observe` ⇒ `Observed = 1`、账本零写入 |
| `TestRestartedActorResumesParkedTurnFromDurableLedger`（`chat/actor_restart_recovery_test.go:22`） | 重启现场（actor 停掉 + 账本句柄关闭 → 新句柄打开同一文件）：§6.12 记录逐字段读回（`obligation_ids` / `resume_queue` / `parked_at`）；重建 actor 冷缓存解析出挂起 `turn_id`、`Busy()` 与 `AcceptsResume()` 同时成立、提交复用同一 `turn_id`，episode 收尾后账本与挂起标记不变（AC-P3-3a + AC-P3-3c 的宿主面） |
| `TestRuntimeStateSummaryBusyCoversParkedTurn`（`chat/runtime_state_test.go:144`） | 挂起 turn ⇒ `AwaitingObligations()` / `Busy()` 为真且 `AcceptsResume()` 为真；纯空闲、执行中两态语义不变；状态 JSON 往返（durable 读回的等价物）后语义还原（AC-P3-3c） |

**验证证据（2026-09-23 实测）**

- `go build ./...` exit 0（backend 全量编译）。
- `go test ./internal/supervision/ -count=1` **ok**（6.689s，含新增三个重启对账用例）。
- `go test ./internal/chat/ -count=1` **ok**（37.907s）；`go test ./cmd/aicli/commands/ -count=1` **ok**（154.788s）—— 三处宿主接线（Busy 语义分流 + 启动对账）未回归。
- 定向复跑：`go test ./internal/supervision/ -run TestReconcileRestart -v` 3/3 PASS（1.040s）；`go test ./internal/chat/ -run 'TestRestartedActor…|TestSubmitPromptOnSuspendedTurn…|TestSubmitPromptClearsStale…|TestRunEndStamps…' -v` 4/4 PASS（1.264s）。
- `gofmt -l`（本轮 9 个改动文件）无输出。

**AC 判定**

| AC | 判定 | 证据 |
| --- | --- | --- |
| AC-P3-3a（重启后挂起 turn 的恢复路径有测试，账本 + resume 队列可读回） | ✅ L1 | `TestRestartedActorResumesParkedTurnFromDurableLedger`（新句柄读回 §6.12 记录 + 冷缓存复用 `turn_id`）；账本真源（`turn_suspensions`）不变 |
| AC-P3-3b（不可恢复 ⇒ `orphaned` + fencing token 提升，晚到写入被拒） | ✅ L1 | `TestReconcileRestartFencesUnrecoverableRunsAndBlocksLateWrites`（正反例 + `ErrRunConflict` + critical 通知）+ `…KeepsRecoverableRunsAndRepeatsIdempotently` + `…ObserveModeRecordsWithoutWriting` |
| AC-P3-3c（重启后 `awaiting_obligations` 与 `Busy()` 语义一致，不出现"假空闲"） | ✅ L1 | `TestRuntimeStateSummaryBusyCoversParkedTurn`（含 JSON 往返）+ `TestRestartedActorResumesParkedTurnFromDurableLedger` 的状态断言 |

**留白 / 语义边界**

- 重启对账是**启动时一次**（其余时间由巡检的常规 deadline 路径负责）：它不取代 watchdog，也不承诺"另一个宿主还活着时立刻接管"——宽限期内一律按可恢复保留，正是为了不误伤活着的 worker。
- `Recoverable` 谓词目前只有 supervision 包内的契约与测试：CLI 宿主**尚未注入**"该 run 的挂起 turn 记录仍在"的谓词（宿主侧现成判据是 `subagentbatch.GetTurnSuspension`）。当前生产形态下，挂起 turn 的 run 若心跳陈旧仍会被判 `orphaned`；由于挂起态本身**没有在途 run**（§6.7 `awaiting_obligations`），这条组合路径暂无实际触发面——接入点已留出（策略字段），待 C4-4 端到端按真实读数决定是否启用。
- AC-P3-3a 的"真实进程重启"（跨进程文件锁、真实宿主启动）仍是 **L2 留白**：本轮取证到"新句柄 + 冷缓存 actor"这一等价形态。

**下一步**：P3 第四项 **C4-4（端到端验收）** —— AC-P3-4a（全量回归绿）/ AC-P3-4b（≥2h 声明预算 + 一次延长的真实长任务，L4）/ AC-P3-4c（五项度量相对基线改善，L4）。

### 13.12 补丁登记：P3 第四项 C4-4（端到端验收），2026-09-23

> 进入条件（§5 顺序：C4-1 → C4-2 → C4-3 → **C4-4**）已由 §13.11 满足。本轮把 §5 的三条 AC 落到**可复算的证据**上（AC-P3-4b / AC-P3-4c 各新增一条端到端用例），并顺带闭环一处**由端到端用例暴露的生产缺陷**：run 形态的监督动作（延长 / 接管 / inspect run …）在生产宿主上被 root scope 授权器**全量拒绝**（fail-closed）。

**落地形态（对照 §5 C4-4 表）**

| AC | 落地 |
| --- | --- |
| AC-P3-4a 全量回归绿（backend 全包 + 前端相关单测） | backend：`go build ./...` + `go test ./... -count=1`（隔离沙箱，须带 `backend/internal/webui/dist` 嵌入夹具）；前端：`npm test`（`frontend/`）。逐项读数见「验证证据」 |
| AC-P3-4b ≥2h 声明预算 + 一次延长，全程 turn 不中断、终局报告完整 | `TestSupervisionE2E_LongTaskDeclaredBudgetWithOneExtension`（`cmd/aicli/commands/supervision_e2e_long_task_budget_test.go:29`）：把「等两小时」换成**声明值 + durable 状态推进**，其余四段全走生产路径 —— 派发 + 挂起（`SubagentBatches.ParkTurnSuspension`，§6.12）→ 停摆上报（`supervision.ProjectLifecycle`，与生产 `projectDecision` 同一投影入口）→ 中途一次延长（`Actions.RequestAction` → `AcceptAction` → `ExecuteAction` 的 `extend_deadline`）→ 终局 rollup（`injectLocalSupervisionPreflight`，`chat_actor_host.go:2991`） |
| AC-P3-4c 五项度量相对基线改善 + 兜底占比低且稳定 | `TestSupervisionMetricsReadout_RuntimeCancelSources`（`:274`）：用**宿主装配的 watchdog**在 `enforce` 下判出一条真实兜底行（`CancelSource=decision_window_expired`），再补 11 条样本凑出 12 条窗口，按 §7.3 指标 1–2 的口径复算（`readRuntimeCancelMetrics`，`:432`：`ListExecutionRunsBySession` + `CancelSource` 分组，线上可直接复用同一读法） |

**语义决策**

| 面 | 决策 |
| --- | --- |
| 「真实长任务」如何在没有两小时墙钟的情况下取证 | 三处一致声明 ≥2h（`batch.BatchDeadline` / `task.TaskDeadline` / `run.DeclaredBudget` + `ExecutionDeadlineAt`），时间推进只落在 durable 账本上。除「模型选择工具」与「真实墙钟」外没有替身；断言的都是**账本事实**（`ExtensionCount=1` / `ExtendedTotal=30m` / 两个 deadline 各 +30m / `CancelSource` 为空）与**父会话可见面**（`obligation.deadline.extended` 事件、digest 文本、rollup `2/2 completed (terminal …)`） |
| 「全程 turn 不中断」的判据 | 不靠 UI 观测，靠账本：`GetTurnSuspension(parent, turnID)` 全程有记录、`turn_id` 不变、`ObligationIDs` 不变、`ResumeQueue` 只排一次（预算内，无唤醒风暴）—— 与 AC-P3-3c 的「假空闲」修复同一真源 |
| 延长的**逐义务**语义 | 延长只作用于目标 run：同 scope 的另一条 run 的 `ExtensionCount` / `ExecutionDeadlineAt` 逐字段不动（避免「延长一个 = 全批延期」） |
| 兜底占比的分母 | 分母 = 窗口内的 **run 行**（按子会话枚举），不是通知行或 turn 行：这样 operator / parent 的主动取消（`operator_cancel` 等）落在分母、不落分子；「被 runtime 强制取消」只认白名单来源（`decision_window_expired` / `progress_stalled` / `execution_deadline` / `execution_timed_out`，`:402`） |
| 兜底行必须由**生产** watchdog 产生 | 用例不手工写 `CancelSource`：`decision_window_expired` 行由 `host.getLocalExecutionSupervisor()`（宿主同一装配，`chat_actor_execution_supervisor.go:26`）在 `enforce` 下 `ScanOnce` 判定；样本里的 `progress_stalled` / `operator_cancel` 行也按生产两步（`RequestExecutionCancel` → `MarkExecutionRunTerminal`）落终态，因为 `CancelSource` **只有取消请求会写**（`execution_store.go:390` 的 `MarkExecutionRunTerminal` 只写 `error_code`） |

**改动清单**

| 位置 | 改动 |
| --- | --- |
| `backend/internal/runtimeserver/supervision.go` | `rootScopeAuthorizer.Authorize` 的 agent 分支（`:233-255`）：先把 target 解析成**候选身份集** —— `targetID` 本身，加上（当 target 是 run id 时）`ExecutionRunStore.GetExecutionRun` 读回的 `run.AgentID` / `run.SessionID`，再与 `ListAgentControlAgents` 的记录比对；未知 run_id 与跨 root scope 的 run 仍 fail-closed |
| `backend/internal/runtimeserver/supervision_test.go` | 新增 `TestSupervisionControlPlane_GraphAuthorizerResolvesRunSubjects`（`:413`）：run-in-scope 放行 / run-foreign 拒绝 / run-unknown 仍拒绝；既有 `…RejectsForeignAgent`（`:357`）保留为反例护栏 |
| `backend/cmd/aicli/commands/supervision_e2e_long_task_budget_test.go`（新增，453 行） | 两条端到端用例 + 读数口径（`runtimeForcedCancelSources` `:402` / `readRuntimeCancelMetrics` `:432`） |

**缺陷登记：run 形态的监督动作被 root scope 授权器全量拒绝（fail-closed）**

- **现象**：新写的 AC-P3-4b 用例在 `RequestAction(ActionExtendDeadline, TargetID=run-long-1)` 上直接失败：`supervision: action not allowed for current state: supervision: target agent is outside the requested root scope; not authorized`（由 `ActionService.RequestAction` 包裹的 `ErrActionNotAllowed`）。
- **根因**：生产把 run 生命周期行投影为 `SubjectKind = SubjectAgentRun` 且 **`SubjectID = run_id`**（`execution_supervisor.go:1141/1149` 重启 orphan、`action_service.go:959/968` takeover、`action_service.go:1076/1085` 延长），`local_control.go:277` 又把 `record.SubjectID` 原样当 `TargetID` 转发；而 `rootScopeAuthorizer.Authorize` 只用 `targetID` 直接匹配 `AgentRecord.AgentID` / `SessionID` —— 图里从来没有 run_id 这个身份，于是**所有 run 形态的动作**在生产宿主上都被拒。这是 fail-closed 的真缺陷（不是测试问题）：CLI 宿主注入 `AgentRegistry`（`chat_actor_host.go:1160-1167`）走的就是这条分支。
- **修复**：把 run 解析进候选身份集再比对（见上表），**不放宽** scope 规则本身：跨 root scope 的 run、未知 run_id 仍拒绝；`hooks.Authorize` 显式注入时仍优先（`:203-205` 未改）。
- **反例护栏**：`run-foreign` / `run-unknown` 两条断言锁住「解析 ≠ 放行」。

**测试落点（L1 / L2）**

| 用例 | 断言 |
| --- | --- |
| `TestSupervisionE2E_LongTaskDeclaredBudgetWithOneExtension` | 挂起期巡检文本带 `0/2 completed, 2 running`；延长链路 requested → accepted → completed，结果含「已延长 ×1」「+30m0s」；账本 `ExtensionCount=1`、两个 deadline 各 +30m、`DecisionWindowUntil` 被花掉（escalate-first 窗口用尽）、`CancelSource` 为空；同 scope 另一 run 逐字段不动；`obligation.deadline.extended` 事件与 digest 文本对父会话可见；终局 `2/2 completed (terminal …)` 且 `CancelRequestedAt` 为空；全程 `turn_id` / 义务清单不变、`ResumeQueue` 只排一次 |
| `TestSupervisionMetricsReadout_RuntimeCancelSources` | 真实兜底行：宿主 watchdog 在 `enforce` 下把停摆 run 判成 `cancel_requested` + `CancelSource=decision_window_expired`；12 条窗口读数 `Total=12` / `ForcedCancel=2` / `DecisionWindowExpired=1`，`ForcedCancelRatio=2/12`，兜底占比 < 10% |
| `TestSupervisionControlPlane_GraphAuthorizerResolvesRunSubjects` | run-in-scope 放行（经 run → agent 解析）、run-foreign 与 run-unknown 均 `not authorized` |

**验证证据（2026-09-23 实测）**

- **全量回归（隔离沙箱 `%TEMP%\c44-baseline\backend`，job `job_ref_1a6fadf0a51d`，5.4 分钟）**：`go build ./...` **exit 0**；`go test ./... -count=1` **exit 0** —— 133 个包 `ok` + 20 个 `[no test files]`，**零 FAIL**。关键包读数：`cmd/aicli/commands` 266.9s（含本轮两条端到端用例）、`internal/api/skills` 141.9s、`internal/chat` 130.0s、`internal/usageledger` 80.1s、`internal/runtimeserver` 54.0s（含新增授权用例）、`internal/toolbroker` 32.3s、`internal/supervision` 9.1s、`internal/subagentbatch` 6.6s、`internal/webui` 2.2s、`cmd/contractgen` 1.1s。
- **前端**：`npm test --silent`（`frontend/`，job `job_6d50d6e971df40ecb3bf6520b1e636e3`）**exit 0**：**320 个测试文件 / 2664 个用例全通过**（412.5s）。
- **定向复跑**（沙箱，job `job_ref_7c46f1bd8681`，exit 0）：`go test ./internal/runtimeserver ./cmd/aicli/commands -run 'TestSupervisionControlPlane_GraphAuthorizer|TestSupervisionE2E_LongTaskDeclaredBudgetWithOneExtension|TestSupervisionMetricsReadout_RuntimeCancelSources' -v` → **4/4 PASS**（`…RejectsForeignAgent` 0.95s / `…ResolvesRunSubjects` 0.12s / `…LongTaskDeclaredBudgetWithOneExtension` 0.86s / `…RuntimeCancelSources` 0.13s）。
- **gofmt**：本轮 3 个改动文件 `gofmt -l` 无输出。
- **三个非代码性失败已排除**（登记以备复现口径一致；均未改动被测代码、未放宽断言）：
  - `internal/webui [setup failed]`（`assets.go:12` 的 `//go:embed dist` 找不到目录）：沙箱漏拷 `backend/internal/webui/dist`（121 文件 / 2.5 MB）；补拷后 `go test ./internal/webui/` → `ok 1.803s`。
  - `cmd/contractgen` `TestGeneratedFileIsUpToDate`：沙箱的 `frontend/src/types/runtime/event-contract.ts` 与 `backend/internal/events` 注册表版本不一致（沙箱是**局部拷贝**，前端那份停在拷贝时点）；主仓同一用例 `--- PASS`，补拷该文件后沙箱也 `--- PASS`（0.788s）。
  - `internal/background` `TestManagerPersistsTimeoutBudgetAndStructuredTimeoutOutcome`：满负载并行下 20s 预算超时（`last status=failed, message="context deadline exceeded"`），单包复跑 `ok 49.863s`。同类载荷敏感抖动还有 `internal/toolbroker` 的 `TestReliabilityEvalBrokerTimeoutRetryUsesNewInvocationWithoutDuplicateSideEffect`（21:55 那次主仓全量中 16s 超时，其后两轮全量均 `ok`）。

**AC 判定**

| AC | 判定 | 证据 |
| --- | --- | --- |
| AC-P3-4a（全量回归绿：backend 全包 + 前端相关单测） | ✅ L2 | 后端全量 **exit 0**（133 包 ok，零 FAIL）+ 前端 **exit 0**（320 文件 / 2664 用例）；两个夹具问题按「补齐沙箱夹具 / 单包复跑」排除，未放宽任何断言 |
| AC-P3-4b（≥2h 声明预算 + 中途一次延长：turn 不中断、终局报告完整） | ✅ 语义闭环（L2 形态）；⏳ 真实墙钟留白 | `TestSupervisionE2E_LongTaskDeclaredBudgetWithOneExtension`：三处声明 2h、延长 ×1（+30m）、账本与父会话可见面双证据、`turn_id` 与义务清单全程不变、终局 `2/2 completed (terminal …)`、`CancelSource` 空 |
| AC-P3-4c（五项度量相对基线改善；`decision_window_expired` 兜底占比低且稳定） | ⏳ 部分：**口径与读数链路 ✅**；基线与稳定性**不可判定**（C0-E 未采集） | `TestSupervisionMetricsReadout_RuntimeCancelSources`：兜底行由生产 watchdog 产生，12 条窗口复算 `ForcedCancel=2/12`、兜底占比 < 10%；但「相对基线改善」缺基线数字，见留白 |

**P3 门禁的场景表（§6.3 P3 行）初步登记**

§6.3 的 P3 行还要求 **EC-F 组 3 条 + EC-G 组 4 条**必测（此前 §13.9–§13.11 未登记过）。按用例名匹配的初步登记如下（**未逐条复核断言强度**，仅确认落点存在）：

| 场景 | 落点（已存在用例） | 状态 |
| --- | --- | --- |
| EC-F1 resume 风暴（终态合并为一次 resume） | `cmd/aicli/commands/chat_actor_batch_converge_test.go:137/158`、`supervision_autoclose_convergence_test.go:142/176/205`（终态批量收敛 + 策略矩阵 + 未接线惰性） | ✅ 落点存在 |
| EC-F2 巡检轮 token 成本（digest 上限 / 巡检预算） | `internal/supervision/digest_test.go:159`（`…Truncation`）、`wake_budget_test.go`（唤醒预算与审批不被饿死） | ✅ 落点存在 |
| EC-F3 模型滥用 extend（I5 上限 + 延长可见） | `internal/supervision/extend_deadline_test.go`（`ExtensionCount` / 上限断言 9 处）、`config_test.go`（上限配置 4 处） | ✅ 落点存在 |
| EC-G1 依赖 `wait` 内联报告的调用方（兼容期） | `internal/toolbroker/broker_agent_test.go:1075`（`wait_agent` 接 batch id）、`broker_wait_schema_guard_test.go:108`（无 legacy wait 文案）、`broker_team_test.go:279/332/359/414/467` | ✅ 落点存在 |
| EC-G2 非交互宿主无「用户」参与（队列驱动 resume） | `internal/supervision/resume_queue_digest_test.go:14/77/91`、`wake_consumer` 系列 + §13.9 的 durable resume 队列 | ✅ 落点存在 |
| EC-G3 审批等待与挂起叠加 | `internal/supervision/approval_projection_test.go`（7 例）、`execution_supervisor_test.go:181`（等审批不被进度超时杀）、`:829`（`ApprovalDeadlineAt` 缺失时兜底终态） | ✅ 落点存在 |
| EC-G4 历史生命周期行无 `turn_id`（走独立 run 语义） | **未见专用用例**；空 `turn_id` 语义散在 `internal/supervision/resume_context.go`（5 处判定） | ⏳ 缺口 |

**留白 / 语义边界**

- **C0-E 基线仍未采集**：§1.1 C0-E 的通过标准是「有基线数字」，§7.3 三个阈值都写着「建议值，需与基线一并确认」，§7.3 待办项（`:719`）未勾。因此 AC-P3-4c 的「相对基线改善」**当前不可判定**，本轮只把它收敛到「口径可复算 + 一条真实兜底行」。落地方式已备好：`readRuntimeCancelMetrics` 的读法线上可直接复用（按子会话枚举 run 行 + `CancelSource` 分组），采集脚本一落地基线即为一次性采样。
- **§7.3 指标 3–5 缺真实运行期数据**：成功汇报延迟 P95、单 turn resume 次数 / token 成本、误杀率（需人工抽样复核）三项都依赖真实运行期采样，本轮未取证。
- **AC-P3-4b 的「≥2h 墙钟」是 L4 留白**：用例证明语义与宿主接线成立；真实两小时里的观测量（进度心跳抖动、watchdog 判定时点、resume 延迟）仍待真实长任务。
- **§13.11 留白的收口**：C4-3 留出的 `Recoverable` 谓词接入点，按「真实读数」判断的结果是**暂不启用** —— 当前生产形态下挂起 turn 本身没有在途 run（§6.7 `awaiting_obligations`），这条组合路径无实际触发面；接入点（策略字段）保留，待真实长任务出现「挂起 turn + 陈旧心跳」的组合读数再决定。
- **EC-G4 是 P3 门禁里唯一没有专用用例的场景**（见上表）：迁移期「历史行 `turn_id` 为空 ⇒ 独立 run 语义、不参与账本清空判定」目前只有代码内判定、无用例锁住。
- **run 授权修复的边界**：解析只发生在 agent 分支内，且只在 target 能读回 run 行时生效；`hooks.Authorize` 注入优先、跨 scope 拒绝、未知 id 拒绝三条边界都有测试锁住。`cmd/runtime-server/main.go` 未注入 `AgentRegistry`（走 legacy 放行分支），因此该修复对 CLI 宿主是实际生效面，对 runtime-server 宿主是未来接线的前提。

**下一步**：P3 的 AC 面（AC-P3-1…4）已全部收口；P3 门禁还剩两块账 —— ① **C0-E 度量基线采集**（AC-P3-4c 的判定前提，上线后 L4）；② **EC-G4 专用用例**（P3 场景表唯一缺口）。建议优先补 ②（L1 即可闭环），基线采集按上线节奏走。

---

### 13.13 补丁登记：P3 场景表 EC-G4（历史生命周期行无 `turn_id` 走独立 run 语义），2026-09-23

> 进入条件：§13.12 已把 P3 的 AC 面收口，并在场景表与留白里登记「EC-G4 是 P3 门禁唯一没有专用用例的场景」。本轮把这条场景从「只有代码内判定」补成「有专用用例 + 反证用例锁住的判定」。

**场景口径（design §6.2 + EC-G4）**

| 面 | 口径 |
| --- | --- |
| I1 判据是 **turn 作用域** | `count(obligations where turn_id=? and status not terminal) == 0`（design `:271`）——「账本清空」问的是**这个 turn** 还有没有在途义务，不是「整个父会话还有没有在途行」 |
| 迁移期历史行 | 旧版本没有 `turn_id` 列，历史生命周期行读回时 `ParentTurnID == ""`；EC-G4 明确此类行走**原有「独立 run」语义**、**不参与账本清空判定**（design `:699`） |
| 两侧都不许偏差 | ① 计入 pending ⇒ 迁移期的陈旧行把 `can_finalize` 永久压成 false（turn 收不了尾）；② 当作某 turn 的义务 ⇒ 编造 turn 身份、把 resume 拉进一个不存在的 turn（违反 I3） |

**改动清单**

| 位置 | 改动 |
| --- | --- |
| `backend/internal/supervision/resume_context.go` | `applyObligations`（`:255`）：非终态行按 `ParentTurnID` 分流 —— 有归因 → `PendingCount++`（继续压住 I1），无归因 → 新增 `StandaloneCount++`（**只报告、不判据**）；`ResumeContext` 新增 `StandaloneCount` 字段（`:150`）；`formatResumeText` 新增汇总行 `standalone_obligations: N (no turn_id; legacy standalone runs, not gating can_finalize)`（`:405`）与逐行 `standalone=true` 标记（`:449`），使模型能看到「有一个不归因的活体 run」而不会被它误判 I1；三处工具名文案同步为合并后的可见面（`subagent_status(include_digest=true)` / `subagent_inspect_task`） |
| `backend/internal/supervision/resume_context_test.go` | `TestResumeContext_AggregateStatusSeverity` 的样本行补 `ParentTurnID: "turn-1"`：口径变化后只有**归因行**才压住收尾，而该用例测的是终态族优先级（canceled/failed 不被报成 clean completion），必须显式归因 |
| `backend/internal/supervision/resume_context_legacy_turn_test.go`（新增） | EC-G4 专用用例 ×3（见下表） |
| `backend/internal/subagentbatch/turn_suspension_settled_test.go` | 新增 `TestTurnObligationsSettled_IgnoresUnreferencedLegacyRows`：**清空侧**的同一条不变量 —— 挂起记录的清空谓词只读**记录自己引用的**义务 id，同库里未被引用的无归因活体行既不阻塞也不触发清空；反证方向（记录**引用**了该行时仍必须等待）同时锁住 |

**测试落点（L1）**

| 用例 | 断言 |
| --- | --- |
| `TestBatchObligationSource_LegacyRowsWithoutTurnIDDoNotGateFinalize` | 同一父会话里既有本 turn 的终态批次、又有一行迁移期遗留的无归因活体批次：`PendingCount == 0`（I1 可收尾）、`StandaloneCount == 1`（如实报告）、resume 文本含 `standalone_obligations: 1` 且该行带 `standalone=true` |
| `TestBatchObligationSource_LegacyRowsAloneKeepLegacyNewTurnFallback` | 账本里**只剩**历史行：没有可归因的 turn 时不编造 turn 身份，宿主按原有「新开 turn」路径保底（I3 的降级分支） |
| `TestResumeContext_AttributedLiveRowsStillGateFinalize` | **反证**：真正归因于某 turn 的非终态行继续压住收尾（`PendingCount > 0`、`can_finalize=false`），且其 turn 优先于调度提示（I1/I3 未被放宽） |
| `TestTurnObligationsSettled_IgnoresUnreferencedLegacyRows` | 未被记录引用的无归因活体行 ⇒ `settled=true`（可清空）；被记录引用 ⇒ `settled=false`（仍等待）。判据键在**记录**上，不在行的归因上 |

**验证证据（2026-09-23 实测）**

- **全量回归（主仓，日志 `logs/p3-full-20260923-b.log`，命令 `cd backend; go test ./internal/... ./cmd/... -count=1`）**：**EXIT=1**；读数 `ok=131` / `[no test files]=18` / **`FAIL` 包 = 1**。
  - 唯一失败包 `internal/background`（`FAIL … 158.598s`，用例 `TestManagerRecoversPendingAndMarksInterruptedRunningJobsOrphaned` 43.60s）—— §13.12 已登记为**满负载载荷敏感抖动**；本轮反证：单用例复跑 `--- PASS (5.05s)`（包 `ok 5.320s`）、整包复跑 `ok 41.590s`。与本轮改动无因果关系。
  - 关键包读数（全绿）：`internal/api/skills 183.074s`（含本轮 stale 断言修复）/ `internal/chat 163.369s` / `internal/policy 19.265s` / `internal/runtimeserver 66.743s` / `internal/subagentbatch 12.865s` / `internal/supervision 11.757s` / `internal/toolbroker 43.562s` / `cmd/aicli/commands 171.537s` / `cmd/aicli/ui 12.433s`。
- **EC-G4 定向复跑（fresh，`-count=1 -v`）**：`internal/supervision` 三例 **全 PASS**（各 0.00s；包 `ok 0.236s`）；`internal/subagentbatch` 的 `TestTurnObligationsSettled`（7 子例）+ `TestTurnObligationsSettled_IgnoresUnreferencedLegacyRows` **全 PASS**（包 `ok 2.241s`）。
- **顺带闭环（门禁红项）**：`internal/api/skills/supervision_tool_controller_test.go` 的 stale 断言（仍在断言旧 `supervision_*` 名字必须出现在 `Definitions()`）改为**合并后可见面 + 兼容面**两层契约（`:31` 注释说明事实源 + `supervisionToolFamilyNames` 全集守卫，新增/退役名字不会漏掉这处反向断言）；定向 `go test ./internal/api/skills -run 'TestApplyAgentRuntimeServicesGatesSupervisionToolController' -count=1` → **ok 24.09s**。
- **gofmt**：本轮改动文件 `gofmt -l` 无输出。

**AC 判定**

| AC | 判定 | 证据 |
| --- | --- | --- |
| P3 场景表 EC-G4（P3 门禁唯一缺口，§13.12 登记） | ✅ L1 闭环 | 专用用例 ×3（无归因行不压收尾 / 只剩历史行退回「新 turn」保底 / **反证**：归因行继续压住收尾）+ 清空侧 ×1（未被引用不阻塞、被引用仍等待）；`StandaloneCount` 只报告、不判据 |
| 既有 AC 不放宽（I1 / I3） | ✅ | 反证用例锁住「归因行仍压住 `can_finalize`」；`TestResumeContext_AggregateStatusSeverity` 的口径随迁（只有归因行压收尾）；全量回归除已登记抖动外全绿 |

**留白 / 语义边界**

- **「真·迁移库」形态未抽样**：用例构造的是「同一父会话 + 无 `turn_id` 的活体批次」，等价于迁移期读回形态（`ParentTurnID == ""`）。真实旧库的 `NULL` 与空串两种读回尚未在真库快照上抽样复核（代码侧 `strings.TrimSpace` 同时覆盖两者）。
- **`StandaloneCount` 尚无独立观测面**：目前只出现在 resume 文本（`standalone_obligations: N` / 逐行 `standalone=true`）与 `ResumeContext` JSON 里，没有事件与指标行；若要把「迁移期残留行数」做成可观测指标，需在 §7.3 口径表加一行。

**下一步**：提交本轮（生产改动 + 用例 + 本登记）。P3 门禁只剩 **C0-E 度量基线采集**（AC-P3-4c 的判定前提，L4，按上线节奏走）；落地方式已在 §13.12 留白里备好（按子会话枚举 run 行 + `CancelSource` 分组）。

### 13.14 C0-E 度量基线采集（§7.3 五项指标：口径落地 + 首次真实采样）

**进入条件**：§13.13 已闭环 P3 场景表 EC-G4；P3 门禁仅剩 **C0-E**。§1.1 的通过标准是「**有基线数字**」，而 §13.12 的留白把当时的状态记成「口径可复算，但没有采集入口」。本轮把口径提升为生产代码的单一来源，并给出可复算的采集入口与**首次真实采样**。

**口径表（§7.3 五项指标 → 读数来源 → 入口）**

| 指标 | 读数来源 | 入口 |
| --- | --- | --- |
| 1 被 runtime 强制取消的 run 占比 | `supervision.ComputeRuntimeCancelMetricsFromRuns`：分母 = 窗口内 run 行（终态按 `finished_at`、活体按 `created_at` 记账，即 `RunAccountingInstant`）；分子只认 runtime 自己判的取消来源（`decision_window_expired` / `progress_stalled` / `execution_deadline` / `execution_timed_out`），操作者取消只进分母 | `GET /supervision/metrics`、`go run ./cmd/supervision-metrics` |
| 2 `decision_window_expired` 兜底占比 | 同上（`Cancel.DecisionWindowExpired` / `DecisionWindowExpiredRatio`） | 同上 |
| 3 成功完成 → 父 Agent 汇报延迟 P95 | `supervision.ComputeReportLatencyMetrics`：`supervision_completion_outbox.delivered_at` − run `finished_at`，只 join **成功终态**（取消类 run 的出件会被 `run_canceled:` 标失败，天然不进样本）；无样本时报「无样本」而不是 0ms | 同上（`report_latency`） |
| 4 单托管 turn 的 resume 次数 / token | 宿主预算账本（`/supervision/snapshot` 的 `wake_budget`）；memory 预算模式下 durable claim 行不存在，**无法离线复算** | 快照 `notes` 显式指向，不报假数 |
| 5 误杀率 | `MisKillCandidate` 候选清单（窗口内 runtime 强制取消的 run，按完成时刻倒序、限流），**判定仍由人工复核**（取消后 5 分钟内本可完成） | 同上（`mis_kill_candidates`） |

**改动清单**

1. `internal/supervision/metrics_readout.go`（新）：上面五项口径 + `CollectMetricsSnapshot`。窗口过滤在 Go 里按 `RunAccountingInstant` 做 —— 时间列是 RFC3339Nano TEXT，SQL 字符串序在小数秒不同的行上不再是时间序（§13.12 已登记该坑）。`ExecutionRunWindowLister` 不可用时降级为按 session 分页走 `ListExecutionRunsBySession`，并把「页满且最旧行仍在窗口内」的 session 记进 `TruncatedSessions`（报告可能少算，而不是静默少算）。
2. `internal/supervision/execution_store.go`：新增 `ExecutionRunWindowFilter` + `ExecutionRunWindowLister`（`ListExecutionRunsInWindow`：全库或单 root scope、按记账时刻倒序、窗口在 Go 里过滤）与 `ListDeliveredOutboxSince`（指标 3 的样本来源，只取已投递、按投递时刻倒序）。两者都是**可选接口**：老 store 不实现也不破坏既有调用。
3. `internal/api/skills/supervision_metrics.go`（新）+ `handler.go` 一行注册：`GET /supervision/metrics`，参数 `root_session_id` / `since` / `until`（RFC3339 或 `24h` / `7d`）/ `window_limit` / `max_candidates`；参数非法报 400，未接线宿主报 503（不回全 0 载荷）。
4. `cmd/supervision-metrics/main.go`（新）：离线一次性采集，读路径全 `SELECT`；用于「上线后对比记录」对**任意一份** `supervision.db`（含已下线实例）复算同一份数字。
5. `supervision.ParseMetricsWindowValue`：HTTP 端点与离线命令共用一份窗口解析，避免「最近一周」在两个入口含义不同。

**测试落点**

- `internal/supervision/metrics_readout_test.go`（新）：口径（分子/分母、`0/0` 不产 NaN、操作者取消不进分子）、store 读路（窗口 / root scope / 限流 / 只取已投递出件）、纯投影（延迟 join 跳过非成功终态与未投递、候选清单倒序限流）、端到端快照（真实 store 上同时给出指标 1–3 与候选清单）、窗口解析。
- `internal/api/skills/supervision_metrics_test.go`（新）：载荷字段与 scope（`root_session_id` 只读一个 root）、参数错误 400、未接线 503。
- `cmd/supervision-metrics/main_test.go`（新）：命令接线（含 `-root` 缩小分母、`-store` 必填、非法 `since`、多余位置参数）。

**首次采样（2026-09-23，真实库，非合成数据）**

命令与读数（`backend/` 下执行）：

```
go run ./cmd/supervision-metrics -store "C:\Users\vince\.aicli\sessions\runtime\supervision\supervision.db" -since 7d
go run ./cmd/supervision-metrics -store "C:\Users\vince\.aicli\sessions\runtime\supervision\supervision.db"   # 无界
go run ./cmd/supervision-metrics -store "%TEMP%\ai-agent-runtime\supervision\supervision.db" -since 7d
```

| 指标 | 基线读数（`.aicli/sessions/runtime` 库，全库 = 7d 窗口，144 行） |
| --- | --- |
| 1 强制取消占比 | `forced_cancel = 26 / 144 = 18.06%`（`by_source` 只有 `execution_timed_out: 26`） |
| 2 兜底占比 | `decision_window_expired = 0 / 144 = 0%` —— 兜底分支在真实库上**从未触发**，26 条强制取消全部来自硬路径（执行超时） |
| 3 汇报延迟 | **无样本**：该库 `supervision_completion_outbox` 0 行（`outbox_total=0`），故 `report_latency.samples=0` 且带 note |
| 4 resume 次数 | 宿主账本读数（`notes` 已指向 `/supervision/snapshot` 的 `wake_budget`） |
| 5 误杀候选 | 26 条：25 条是 `2026-09-22T01:00:01Z` 的**重启清扫批次**（`orphaned`），1 条 `failed` —— 说明「候选清单 ≠ 误杀」，复核必须人工过一遍 |

- **独立 SQL 反证**（`python -c` + `sqlite3` 直读同一库）：`count(*)=144`、`execution_timed_out = 25 orphaned + 1 failed = 26`、`decision_window_expired` 分组缺失（=0）、`outbox` 0 行、`finished_at` 区间 `2026-09-16T23:49` → `2026-09-23T13:45`（全部落在 7d 窗口内，与「全库 = 7d」一致）—— 与读数**逐项一致**。
- **空库行为**：runtime-server 默认库（`%TEMP%\ai-agent-runtime\supervision\supervision.db`）读数全 0 且 `read_path=window` —— 该实例尚无 run 行，读数不报假数。

**验证证据**

- `go test ./internal/supervision/ -count=1` → **ok**（4.992s，全包）；`go test ./internal/api/skills/ -count=1` → **ok**（35.559s，全包，日志 `logs/c0e_skills_pkg_test.log`）；`go test ./cmd/supervision-metrics/` → **ok**。
- 既有验收用例未放宽：`go test ./cmd/aicli/commands/ -run 'TestSupervisionMetricsReadout_RuntimeCancelSources' -count=1 -v` → **PASS**（该用例仍是「宿主 watchdog 判出的真实兜底行 + 12 条窗口复算」；共享函数与它的局部实现口径一致）。
- `gofmt -l` 对本轮改动文件无输出。
- **全量回归（提交后冻结态，日志 `logs/p3-full-c0e-20260923.log`，命令 `cd backend; go test ./internal/... ./cmd/... -count=1`）**：读数 `ok=132` / `[no test files]=18` / **`FAIL` 包 = 1**（`internal/background`：`TestReliabilityEvalBackgroundTimeoutRetrySucceeds` 20.01s —— 与 §13.13 登记的 `internal/background` 属**同一族满负载时序抖动**，不是本轮改动）；反证：单用例复跑 **PASS 1.53s**、整包复跑 **ok 47.264s**。
  - 关键包读数（全绿）：`internal/supervision 9.490s`（含本轮新增用例）/ `internal/api/skills 67.763s` / `cmd/supervision-metrics 1.110s` / `cmd/aicli/commands 153.013s` / `internal/chat 54.295s` / `internal/toolbroker 22.960s` / `internal/runtimeserver 17.030s` / `internal/policy 2.534s`。

**AC 判定**

| AC | 判定 | 证据 |
| --- | --- | --- |
| C0-E（§1.1「有基线数字」） | ✅ 口径 + 采集入口 + 首次真实采样；生产基线按同一命令一次采样即得 | 上表读数 + SQL 反证；两个入口（HTTP / 离线命令）共用 `CollectMetricsSnapshot` |
| AC-P3-4a（全量回归绿） | ✅ 在 C0-E 提交后的冻结态复验 | 132 包 ok、唯一失败为已登记满负载时序抖动（单例 + 整包复跑均绿）——见「验证证据」 |
| AC-P3-4c（五项度量相对基线改善） | 判定前提已具备（基线 ✅、口径 ✅）；「改善」仍需上线后对比 | 指标 1 基线 18.06%、指标 2 基线 0%；指标 3 在 dev 库无样本；指标 4/5 明确为宿主读数 + 人工复核 |

**留白 / 语义边界**

- **基线取自本机 dev/CLI 库**（行由开发期 e2e / 探针产生），不是生产流量：它足以作为「口径可用 + 量级参考」，但生产基线要在部署实例上重采（一次采样即基线）。
- **指标 3 在 dev 库无样本**：P95 阈值「≤ 1 个 resume 周期」仍需有出件流量的实例确认；读数链路本身已由用例（含未投递/非成功终态跳过）覆盖。
- **指标 5 的复核规则未细化**：候选清单以重启清扫批次为主，runbook 需要写明抽样规则（例如只看「5 分钟窗口内有产物产出」的行）才谈得上「误杀率」。
- **`sessions` 字段在窗口读路上恒为 0**（该计数只在按 session 分页的降级读路上有意义）；字段注释已写明，但跨读路比较快照时不要把它当「参与统计的会话数」。

**下一步**：提交本轮（读数模块 + 端点 + 采集命令 + 用例 + 本登记）。P3 门禁到此**全部闭环**（四项 AC + 场景表 + C0-E）；后续按发布节奏做「上线后对比记录」（§7.3 待办项的后半段）。
