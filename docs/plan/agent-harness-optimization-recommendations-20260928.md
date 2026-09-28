# Agent Harness 优化建议（基于 L3 子会话超时失败排查）

更新时间：2026-09-28

状态：**建议稿（未实施）**。本文只给出优化建议、优先级依据与代码/日志证据索引，不代表相关代码已实现或已排期；文中标注"建议"的均为目标设计。

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

- `ExecutionSupervisor.RecordProgress`（`backend/internal/supervision/execution_supervisor.go:307`）→ `Store.RecordExecutionProgress`（`backend/internal/supervision/execution_store.go:370`）在**生产代码中没有任何调用方**；全仓库仅测试引用（`backend/internal/api/runtimeapi/session_agent_controller_test.go:1338`、`backend/internal/supervision/execution_supervisor_test.go:148`）。
- `last_progress_at` 只在建行（`execution_store.go:179`）与 `RecordExecutionProgress` 内部更新（`execution_store.go:383-403`）；supervisor 自身的 CAS 状态转换（`execution_supervisor.go:814 / 916 / 959 / 1007 / 1252`，均 `UpdateExecutionRunCAS`）**不更新进度时间戳**。
- 因此对 `spawn_agent` 产生的 ExecutionRun，进度时间戳自建行起冻结；默认 5 分钟进度阈值（`execution_supervisor.go:96`）与 escalate-first 决策窗口（`execution_supervisor.go:102-105`）全部运行在冻结时间上。

**后果**：任何真实活跃超过 `ProgressTimeout` 的子任务都会被判"假停滞"→ 升级 → 决策窗口超时 → 到期强杀。任务越难（越需要长时间连续执行），越容易中招——本次 L3 案例正是这条路径。

**建议**：

1. 按 `spawn-agent-team-supervision-timeout-recovery-plan.md` §5.4（第 406-431 行）的落点清单实施埋点：`agent/loop.go`（LLM 帧完成、tool start/end、每步迭代）、`chat/actor.go`（interrupt、审批/输入状态迁移、session_end）、`session_runtime_support.go`（spawn/followup 基线）、`toolbroker/broker.go`（仅在执行路径真实产生消息时计入）。
2. 统一走 `ProgressRecorder`（`Record(ctx, runID, kind, event)`），`progress_seq` 单调分配、乱序去重（store 已支持：`execution_store.go:367-399`）。
3. 明确"非进度事件"（supervisor 扫描、无条件 heartbeat、UI 查询）不写 progress（同计划 §5.4 第 399-404 行）。
4. **低成本先行项**：batch 路径（`spawn_subagents`）已有节流回写实现（`backend/internal/agent/subagent_batch_coordinator.go:1143 / 1246 / 1286`），且宿主已接线（`cmd/aicli/commands/chat_actor_host.go:2196`、`backend/internal/api/runtimeapi/supervision_batch_store.go:84`），但 `TaskProgressInterval` 灰度期默认 `0` = 完全关闭（`backend/internal/supervision/config.go:70-77`）。**先把该开关打开**，即可让 batch 任务的父侧 progress rollup 反映真实进度年龄，成本最低。

### 2.2 超时策略：从"到期强杀"到"到期评估"

现状默认：执行 30m / 进度 5m / 审批 1h / 取消宽限 15s / 扫描 5s（`execution_supervisor.go:87-109`）。

建议：

1. spawn 时按 difficulty / 历史 P50 设置 `ExecutionTimeout`（`RunSpec` 已有字段：`execution_supervisor.go:114-135`；注册入口 `backend/internal/toolbroker/execution_run_hook.go:25`，调用点 `backend/internal/toolbroker/broker.go:1970`）。
2. 到期先做"健康度评估"（最近 progress 年龄、token 消耗速率、工具错误率），健康 → **自动延长一次**。extend 能力已存在：`backend/internal/supervision/action_service.go:753-800`（改写 `ExecutionDeadlineAt` / `ProgressDeadlineAt`，CAS 落库在 `:787`）。
3. 真停滞 → 优雅取消（interrupt → cancel grace → fail），保留 partial 产物。
4. 可续跑任务 `MaxAttempts` 默认大于 1，并让失败结果携带"可续跑基线"。

### 2.3 升级送达与"无人应答"的安全默认（P1）

现象（本次取证）：stall 升级在父侧长期处于 delivery pending，`resume_queue` 积压最久约 1 小时 46 分；父会话长 turn 期间无人应答，子会话最终仍被强杀。

建议：落地 `spawn-agent-team-supervision-timeout-recovery-plan.md` §6.5（第 711-730 行）的分级 wake——critical（timeout / stalled / orphaned / invalid）在父会话 idle 时调度 turn；busy 时写 durable `wake_pending`，turn 结束 drain；同 root debounce / batch，限制单位时间 auto turn 数。

**关键补充**：当"无人应答"超过一个上界时，默认动作必须是安全动作（健康 → 延长；不健康 → 保产物取消），不能等价于"放行到 deadline 强杀"。

### 2.4 失败收尾与产物保全制度化（P1）

正面样本（本次）：子会话被杀，但产物已落库（commit `0f29201a`）+ `do_not_retry` 生效，避免了盲目重派。

建议制度化：

1. 被杀 / 取消路径强制 checkpoint / flush（会话级产物清单 + 工作区未提交变更提示）。
2. 失败结果显式附"已完成 / 未完成清单 + 产物位置"。
3. 区分"有产物失败（不重派）"与"无产物失败（可重派）"，把 `do_not_retry` 的判定规则写成文档化策略。

### 2.5 账本收敛与队列卫生（P2）

- 现状问题：同一 run 产生多个通知（stalled → timed_out）、delivery pending 滞留、终态后 ack 受限（只允许 inspect）。
- 建议：终态通知去重 / 自动收敛；对 `wake_pending` / delivery 滞留时长做告警（既有计划 §16 观测项，见第 1630-1634 行）。

## 3. 工具执行与失败语义（P1）

1. **工具错误级联可视化**：本次会话出现 4 个工具错误、1 个未恢复；当前"停滞 → 超时"的归因链不可视。建议在 supervision snapshot / 诊断面板中呈现"最后一次成功工具调用 → 连续失败次数 → 是否已恢复"。
2. **大工具输出强制 artifact 化**：输出超阈值时只回传摘要 + artifact 引用，防止上下文与 token 失控（参见 `tool-output-artifact-cascade-audit-and-optimization-plan-20260919.md`）。
3. shell 内容失败语义、patch / edit 上下文失效的持续收敛（`aicli-session-optimization-plan.md` P0-2 / P1-1 在跟踪）。

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
