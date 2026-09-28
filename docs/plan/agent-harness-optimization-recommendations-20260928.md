# Agent Harness 优化建议（基于 L3 子会话超时失败排查）

更新时间：2026-09-28

状态：**实施中**（2026-09-28 起）。§2.1 的 P0-1 进度埋点、§2.2/§2.3 的到期评估与无人应答安全默认、§2.4/§2.5 的失败收尾保全（含被杀/取消路径的 checkpoint/flush 可见化与 worktree 产物保全）与账本收敛、§3.1 工具错误级联可视化均已落地（见各节实施记录，最小起步三项完成）；其余条目仍为建议稿，不代表已排期。本文只给出优化建议、优先级依据与代码/日志证据索引；文中标注"建议"的均为目标设计。

## 0. 文档定位

本文基于一次真实子会话失败（L3 前端传输层帧级去重任务被超时强杀）的取证结论，以及对本仓库监督 / 执行 / 上下文 / 工具链路的代码核对，给出 Agent Harness 的分级优化建议。

本文不替代以下既有方案，而是为它们补充"优先级依据 + 证据索引 + 最小起步"：

- `docs/plan/spawn-agent-team-supervision-timeout-recovery-plan.md`（监督 / 超时 / 恢复主方案；§5.4 progress 埋点清单、§6.5 分级 wake）
- `docs/plan/tool-execution-failure-cascade-root-cause-analysis-and-fix-plan-20260919.md`
- `docs/plan/tool-output-artifact-cascade-audit-and-optimization-plan-20260919.md`
- `docs/plan/context-preflight-budget-and-compaction-convergence-design-20260921.md`
- `docs/plan/frontend-deepseek-harness-streaming-render-performance-plan.md`
- `docs/plan/aicli-session-optimization-plan.md`（工具失败语义 / patch 上下文相关条目）

## 1. 触发案例（摘要）

| 项 | 值 |
| --- | --- |
| 子会话 | `session_20260928183132_77pvSbDN`（hard 难度；任务：L3 前端传输层帧级去重） |
| 生命周期 | 18:31:32 启动 → 19:03:24 终止（存活 ≈31.9 分钟），85 步 |
| 规模 | 6.14M tokens（其中缓存读 5.73M） |
| 终态 | `run_status=failed`、`result=failed`、`failure_kind=canceled`、`error=context canceled` |
| 直接原因 | 执行超时到期强杀（默认 `DefaultExecutionTimeout = 30m`，`backend/internal/supervision/execution_supervisor.go:95`）；真实活跃 32 分钟越界 |
| 结构性原因 | progress 信号缺失导致全程按"停滞候选"评估；升级与决策窗口建立在冻结时间戳上（见 §2.1） |
| 产物保全 | `frame-intake*` 三文件已随 commit `0f29201a` 落库（`frontend/src/hooks/workspace/frame-intake.ts` 等）；runtime 已标记 `do_not_retry` |

结论：本次失败不是"模型能力不足"，而是**监督面缺少真实进度信号** + **到期默认动作是强杀而非评估**。6.14M tokens 的执行过程因此没有留下可复用的蒸馏结论，只有最终产物幸存。

## 2. 监督与生命周期（P0，证据最硬）

### 2.1 接通 progress 埋点（最高优先）

**事实（代码取证，2026-09-28 核对）**：

- `ExecutionSupervisor.RecordProgress`（`backend/internal/supervision/execution_supervisor.go:307`）→ `Store.RecordExecutionProgress`（`backend/internal/supervision/execution_store.go:370`）在**生产代码中没有任何调用方**（2026-09-28 实施前取证；P0-1 接线后已有生产调用方）；全仓库此前仅测试引用（`backend/internal/api/runtimeapi/session_agent_controller_test.go:1338`、`backend/internal/supervision/execution_supervisor_test.go:148`）。
- `last_progress_at` 只在建行（`execution_store.go:179`）与 `RecordExecutionProgress` 内部更新（`execution_store.go:383-403`）；supervisor 自身的 CAS 状态转换（`execution_supervisor.go:814 / 916 / 959 / 1007 / 1252`，均 `UpdateExecutionRunCAS`）**不更新进度时间戳**。
- 因此对 `spawn_agent` 产生的 ExecutionRun，进度时间戳自建行起冻结；默认 5 分钟进度阈值（`execution_supervisor.go:96`）与 escalate-first 决策窗口（`execution_supervisor.go:102-105`）全部运行在冻结时间上。

**后果**：任何真实活跃超过 `ProgressTimeout` 的子任务都会被判"假停滞"→ 升级 → 决策窗口超时 → 到期强杀。任务越难（越需要长时间连续执行），越容易中招——本次 L3 案例正是这条路径。

**建议**：

1. 按 `spawn-agent-team-supervision-timeout-recovery-plan.md` §5.4（第 406-431 行）的落点清单实施埋点：`agent/loop.go`（LLM 帧完成、tool start/end、每步迭代）、`chat/actor.go`（interrupt、审批/输入状态迁移、session_end）、`session_runtime_support.go`（spawn/followup 基线）、`toolbroker/broker.go`（仅在执行路径真实产生消息时计入）。
2. 统一走 `ProgressRecorder`（`Record(ctx, runID, kind, event)`），`progress_seq` 单调分配、乱序去重（store 已支持：`execution_store.go:367-399`）。
3. 明确"非进度事件"（supervisor 扫描、无条件 heartbeat、UI 查询）不写 progress（同计划 §5.4 第 399-404 行）。
4. **低成本先行项**：batch 路径（`spawn_subagents`）已有节流回写实现（`backend/internal/agent/subagent_batch_coordinator.go:1143 / 1246 / 1286`），且宿主已接线（`cmd/aicli/commands/chat_actor_host.go:2196`、`backend/internal/api/runtimeapi/supervision_batch_store.go:84`），但 `TaskProgressInterval` 灰度期默认 `0` = 完全关闭（`backend/internal/supervision/config.go:70-77`）。**先把该开关打开**，即可让 batch 任务的父侧 progress rollup 反映真实进度年龄，成本最低。

**实施记录（2026-09-28）**：

- ✅ `agent/loop.go`：LLM 响应完成、tool start/end、每步迭代结束（commit `21a3bcff`）。
- ✅ `chat/actor.go`：loop tick 透传 + §5.4 状态迁移（审批/输入等待进入与解除、中断）（commits `21a3bcff`、`97ab0258`）。
- ✅ 双宿主接线（session→run 解析 + 10s 缓存；best-effort、2s 超时）：`cmd/aicli/commands/chat_actor_progress.go`（commit `21a3bcff`）与 `internal/api/runtimeapi/session_progress_wiring.go`（commit `a0481e00`）。spawn run 以子会话 ID 挂载（`backend/internal/toolbroker/execution_run_hook.go`），子会话 loop tick 可直接命中；无 supervision store / 无 run 的会话回调为 nil，行为与接线前一致。
- ✅ 验收：两宿主解析/缓存/未知会话/无 store no-op 单测、`internal/chat` 全包回归通过。
- ⏳ batch `TaskProgressInterval`：**不改出厂默认**——按 `supervision-parent-child-control-optimization-plan-20260917.md` §4 灰度流程（先单会话显式开启验证，再按遥测决定是否转 5s）执行；操作键 `supervision.task_progress_interval`（runbook §4.4）。
- ⏳ `toolbroker/broker.go`：现状不从 broker 写 progress（满足 wait 不更新 progress 的负面约束）；真实消息的 tick 已由 loop 覆盖，暂无独立埋点。
- ⏳ `session_end`：终态 run 的 progress 写入被 store 忽略、会话结束已由 CompleteRun 收敛，暂不单独埋点。

### 2.2 超时策略：从"到期强杀"到"到期评估"

现状默认：执行 30m / 进度 5m / 审批 1h / 取消宽限 15s / 扫描 5s（`execution_supervisor.go:87-109`）。

建议：

1. spawn 时按 difficulty / 历史 P50 设置 `ExecutionTimeout`（`RunSpec` 已有字段：`execution_supervisor.go:114-135`；注册入口 `backend/internal/toolbroker/execution_run_hook.go:25`，调用点 `backend/internal/toolbroker/broker.go:1970`）。
2. 到期先做"健康度评估"（最近 progress 年龄、token 消耗速率、工具错误率），健康 → **自动延长一次**。extend 能力已存在：`backend/internal/supervision/action_service.go:753-800`（改写 `ExecutionDeadlineAt` / `ProgressDeadlineAt`，CAS 落库在 `:787`）。
3. 真停滞 → 优雅取消（interrupt → cancel grace → fail），保留 partial 产物。
4. 可续跑任务 `MaxAttempts` 默认大于 1，并让失败结果携带"可续跑基线"。

**实施记录（2026-09-28，commit `f5e68752`）**：

- ✅ 到期评估（本节第 2 条）：执行硬到期先做健康度评估（最近 progress 年龄在一个 soft 窗口内），健康 → 自动延长一次原始预算窗口（与 extend_deadline 同一 I5 预算/计数/事件契约：`obligation.deadline.extended`）；不健康 → 保持既有强制分支。
- ✅ 真实活跃不报假 stall（本节验收第 1 条）：软阈值（progress deadline）到期时，仍在 tick 的 run 不再开新的 stall 上报/决策窗口；窗口已开时仍走既有 decide/fallback 时间线。
- ✅ 无人应答安全默认（§2.3 关键补充）：决策窗口耗尽且无人应答时，健康 → 自动延长（而非放行到强杀）；不健康 → 既有兜底取消（保产物语义不变）。
- ✅ 回退开关：`AutoExtendHealthy`（`supervision.Config` / `ExecutionSupervisorConfig`，nil/true=启用，显式 false 回退；与 `EscalateFirst` 同级）；自动延长共享 I5 上限（`MaxExtensions` / `MaxExtensionPerCall` / `MaxExtensionTotal`，零值取操作员默认）。
- ✅ 验收：新增 8 个定向测试（健康延长 / 停滞仍杀 / 预算耗尽 / 回退开关 / observe 不落库 / 无人应答延长 / 延长不放宽 liveness / 真实活跃不报假 stall）；`internal/supervision` 全包回归通过。
- ⏳ 健康度信号目前仅 progress 年龄；token 速率 / 工具错误率按本节第 2 条留作扩展点（`runHealthy` 单点可扩）。
- ⏳ §2.3 第一段的分级 wake（durable `wake_pending` / debounce）属既有 WakeScheduler 工作流，本项未改动。

### 2.3 升级送达与"无人应答"的安全默认（P1）

现象（本次取证）：stall 升级在父侧长期处于 delivery pending，`resume_queue` 积压最久约 1 小时 46 分；父会话长 turn 期间无人应答，子会话最终仍被强杀。

建议：落地 `spawn-agent-team-supervision-timeout-recovery-plan.md` §6.5（第 711-730 行）的分级 wake——critical（timeout / stalled / orphaned / invalid）在父会话 idle 时调度 turn；busy 时写 durable `wake_pending`，turn 结束 drain；同 root debounce / batch，限制单位时间 auto turn 数。

**关键补充**：当"无人应答"超过一个上界时，默认动作必须是安全动作（健康 → 延长；不健康 → 保产物取消），不能等价于"放行到 deadline 强杀"。

> 实施（2026-09-28，commit `f5e68752`）：该安全默认已落在决策窗口耗尽处——健康 → 自动延长一次（I5 预算内），不健康 → 既有兜底取消；见 §2.2 实施记录。

### 2.4 失败收尾与产物保全制度化（P1）

正面样本（本次）：子会话被杀，但产物已落库（commit `0f29201a`）+ `do_not_retry` 生效，避免了盲目重派。

建议制度化：

1. 被杀 / 取消路径强制 checkpoint / flush（会话级产物清单 + 工作区未提交变更提示）。
2. 失败结果显式附"已完成 / 未完成清单 + 产物位置"。
3. 区分"有产物失败（不重派）"与"无产物失败（可重派）"，把 `do_not_retry` 的判定规则写成文档化策略。

**实施记录（2026-09-28，commit `30c56769`）**：

- ✅ 失败结果显式附"已完成 / 未完成 + 产物位置"（本节第 2 条）：`read_agent_result` 对失败/取消记录附 `wrap_up`（completed / unfinished / artifacts），机械派生自 durable record（`agent_result.go` 的 `applyFailedWrapUp`）——被杀的子会话无法事后被追问，摘要不靠新模型 turn。
- ✅ "有产物失败（不重派）"判定收紧（本节第 3 条）：deliverable = summary / findings / artifacts，或**已落库变更**（`applied`，或带 artifact ref 的未落库变更）；`skipped`-only 失败保持可重派（`hasLandedChange`）。`do_not_retry` / `result_available` 语义不变。
- ✅ 预算口径：`wrap_up` 与 changes/artifacts 重叠，超 `max_chars` 时按 changes → unfinished → completed → wrap-up artifacts 顺序脱落，清空即整段消失。
- ✅ 被杀 / 取消路径的 checkpoint / flush 可见化与产物保全（本节第 1 条，commit `73017d02`）：中断/停摆终态 payload 的 A5 救助快照（`partial_summary` / `partial_steps`）与隔离信息此前在 completion 消息 allowlist 处被丢弃，现贯通到 `read_agent_result`（`partial_product` + `workspace` 含救援提示）；CLI close / 强杀路径不再对脏 worktree 执行 `git worktree remove --force`——有未提交改动的 worktree 保留并记 `disposition=kept_uncommitted`，干净 worktree 照旧移除。
- ⏳ 残余：API 宿主 close 路径未发现同类 worktree 销毁挂点（未改动）；无任何终态记录的会话 read 仍走 `no_result_recorded` 指引。

### 2.5 账本收敛与队列卫生（P2）

- 现状问题：同一 run 产生多个通知（stalled → timed_out）、delivery pending 滞留、终态后 ack 受限（只允许 inspect）。
- 建议：终态通知去重 / 自动收敛；对 `wake_pending` / delivery 滞留时长做告警（既有计划 §16 观测项，见第 1630-1634 行）。

> 实施（2026-09-28，commit `30c56769`）：live condition 收敛已落地——同一 run 升级（stalled → timed_out 等）投影新条件时，旧条件行以 `ResolutionClosed` 收束（`SupersedeRunAlerts`，挂在 `projectDecision` 的 run-alert 分支；info 级 `auto_extended` 投影不触发）；终态收敛沿用既有 `ConvergeRunAlerts`。`wake_pending` / delivery 滞留告警属既有观测工作流，未改动。

## 3. 工具执行与失败语义（P1）

1. **工具错误级联可视化**：本次会话出现 4 个工具错误、1 个未恢复；当前"停滞 → 超时"的归因链不可视。建议在 supervision snapshot / 诊断面板中呈现"最后一次成功工具调用 → 连续失败次数 → 是否已恢复"。
2. **大工具输出强制 artifact 化**：输出超阈值时只回传摘要 + artifact 引用，防止上下文与 token 失控（参见 `tool-output-artifact-cascade-audit-and-optimization-plan-20260919.md`）。
3. shell 内容失败语义、patch / edit 上下文失效的持续收敛（`aicli-session-optimization-plan.md` P0-2 / P1-1 在跟踪）。

> 实施（2026-09-28，commit `cfc9c555`）：第 1 条"工具错误级联可视化"已落地——`usage_turns` 新增回合内最长连续失败（`tool_failure_streak`）与最后一次成功工具名（`last_tool_success`），会话明细与 rollup（`max_tool_failure_streak`）直接呈现，与既有 `recovered/unrecovered_tool_error_count`、`retry_recovered` 诊断共同构成"最后一次成功工具调用 → 连续失败次数 → 是否已恢复"归因链；采集侧成功清零、失败累加，老库幂等迁移（与新库 DDL 同源）。第 2、3 条仍由各自专案跟踪。

## 4. 上下文与成本（P1/P2）

1. **压缩预检准确性 + 分层降级**：本会话出现 `compact skipped because estimated input 249873 > budget 125952` → 退化为本地 fallback 摘要。建议超预算时先裁工具输出 / artifact 化再摘要，并把"压缩失败 / 跳过"做成一等事件（参见 `context-preflight-budget-and-compaction-convergence-design-20260921.md`）。
2. **子会话成本护栏**：本次子会话 6.14M tokens / 32 分钟，事前无预算告警。建议用 usage ledger 做单任务预算、超支告警，并把"成本消耗速率"纳入停滞判定（与 §2.2 的健康度评估共用）。

## 5. 协作与观测面（P2）

1. **子代理 / 批量报告归档**：调研类子任务的大报告（本次 3 份 ≈2.1M tokens）目前只存在于 task_result；建议落 notes / reports 并纳入 rollup 引用，避免重复调研。
2. **`inspect` / `wait` / `list` 语义与分页一致性**：核对 `spawn-subagent-same-turn-loop-gap-audit-20260926.md`、`wait-budget-and-max-window-hardening-plan-20260926.md` 中已列条目的落地状态，防止"语义冻结"只停在文档层。

## 6. 前端 / 渲染（按既有专案推进）

按 `frontend-deepseek-harness-streaming-render-performance-plan.md` 五级闸门推进：P0 排空审计（`sse.ts` 消费循环）、L3 提交节奏、L4 订阅切片。本次 L3 子任务的 `frame-intake` 交付（commit `0f29201a`）可作为该专案的直接输入。

## 7. 最小起步（只做三件事）

| 顺序 | 事项 | 依据 | 主要落点 |
| --- | --- | --- | --- |
| 1 | progress 埋点接线（含打开 batch `TaskProgressInterval`） | §2.1 | `agent/loop.go`、`chat/actor.go`、`toolbroker/broker.go`、`supervision/config.go` |
| 2 | 到期评估 / 自动延长 + 无人应答安全默认 | §2.2 / §2.3 | `execution_supervisor.go`、`action_service.go`、wake scheduler |
| 3 | 失败收尾保全 + 账本收敛 | §2.4 / §2.5 | 失败路径 + supervision store |

> 状态（2026-09-28）：第 1 项 agent_run 埋点已落地（commits `21a3bcff`、`a0481e00`、`97ab0258`）；第 2 项到期评估 / 自动延长 + 无人应答安全默认已落地（commit `f5e68752`）；第 3 项失败收尾保全 + 账本收敛已落地（commits `30c56769`、`73017d02`，见 §2.4/§2.5 实施记录；§2.5 滞留告警为后续条目）。**最小起步三项完成**。batch `TaskProgressInterval` 维持出厂 `0`，按 20260917 计划 §4 灰度流程显式开启验证。

建议验收：

- 一个真实活跃超过 5 分钟的子任务全程不再产生假 stall；父侧可见 progress age 随工具调用推进。
- 一个到期任务在"健康"时被自动延长一次；在"停滞"时被优雅取消且产物可查。
- 失败 run 的结果里能看到"已完成 / 未完成 + 产物位置"。

## 附录 A：关键代码落点索引（2026-09-28 核对）

| 主题 | 落点 | 说明 |
| --- | --- | --- |
| 执行监督默认配置 | `backend/internal/supervision/execution_supervisor.go:87-109` | 30m / 5m / 1h / 15s / 5s；escalate-first 旋钮来自 `DefaultConfig()`（`:102-105`） |
| RunSpec（超时 / 预算字段） | `backend/internal/supervision/execution_supervisor.go:114-135` | `ExecutionTimeout` / `ProgressTimeout` / `ApprovalTimeout` / `CancelGrace` / `MaxAttempts` |
| progress 上报入口 | `backend/internal/supervision/execution_supervisor.go:307` | `RecordProgress`（生产代码零调用方） |
| progress 落库 | `backend/internal/supervision/execution_store.go:370-420` | 单调 seq、乱序去重、终态忽略 |
| CAS 状态转换（不动进度） | `backend/internal/supervision/execution_supervisor.go:814 / 916 / 959 / 1007 / 1252` | 均 `UpdateExecutionRunCAS` |
| spawn 注册执行 run | `backend/internal/toolbroker/execution_run_hook.go:25`；调用点 `backend/internal/toolbroker/broker.go:1970` | policy = `interrupt_then_fail`（`:15`） |
| 延长 deadline | `backend/internal/supervision/action_service.go:753-800` | 改写 `ExecutionDeadlineAt` / `ProgressDeadlineAt`，CAS 在 `:787` |
| batch 进度回写 | `backend/internal/agent/subagent_batch_coordinator.go:1143 / 1246 / 1286` | `TaskProgressInterval > 0` 时启动节流刷新 |
| 回写开关默认 | `backend/internal/supervision/config.go:70-77`；宿主接线 `cmd/aicli/commands/chat_actor_host.go:2196`、`backend/internal/api/runtimeapi/supervision_batch_store.go:84` | 灰度默认 `0` = 关闭 |
| 进度诊断展示 | `backend/internal/api/runtimeapi/session_runtime_support.go:3442`；`cmd/aicli/commands/chat_debug.go:2173 / 2410` | 仅展示 `last_progress_at` |
| progress 埋点清单（既有计划） | `docs/plan/spawn-agent-team-supervision-timeout-recovery-plan.md:406-431` | 落点 + `ProgressRecorder` 设计 |
| 分级 wake（既有计划） | `docs/plan/spawn-agent-team-supervision-timeout-recovery-plan.md:711-730` | critical → 父 idle 调度；busy → durable `wake_pending` |

> 路径注记：既有计划 §5.4 中引用的 `backend/internal/api/skills/session_runtime_support.go` 在当前仓库已迁移为 `backend/internal/api/runtimeapi/session_runtime_support.go`，实施时以现路径为准。

## 附录 B：相关文档索引

- `docs/plan/spawn-agent-team-supervision-timeout-recovery-plan.md`（监督 / 超时 / 恢复主方案）
- `docs/plan/tool-execution-failure-cascade-root-cause-analysis-and-fix-plan-20260919.md`
- `docs/plan/tool-output-artifact-cascade-audit-and-optimization-plan-20260919.md`
- `docs/plan/context-preflight-budget-and-compaction-convergence-design-20260921.md`
- `docs/plan/frontend-deepseek-harness-streaming-render-performance-plan.md`
- `docs/plan/aicli-session-optimization-plan.md`
- `docs/plan/spawn-subagent-same-turn-loop-gap-audit-20260926.md`
- `docs/plan/wait-budget-and-max-window-hardening-plan-20260926.md`
