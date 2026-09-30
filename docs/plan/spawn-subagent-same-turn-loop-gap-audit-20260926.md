# spawn 子代理「同一 turn 循环」设计 vs 现状审计（2026-09-26）

> 触发：用户口径——"创建子 agent 后进入一个子循环，子 agent 与主 agent 在同一工作平面上，
> 需要子 agent 都完成后再退出这个循环"。本文件核对该目标与既有设计方案、当前实现，
> 并给出差距清单。方法：三路只读子代理审计（设计文档 / 后端实现 / 前端呈现）+ 主线复核；
> 未修改任何生产代码。

## 0. 结论（TL;DR）

| 目标 | 设计口径 | 现状判定 |
| --- | --- | --- |
| ① spawn 后进入"子循环" | 设计**没有**常驻子循环：等待是 **turn 级状态**，v3 起"派发后父继续运行，无事可做/试图收尾时挂起"（A:1140） | **达成**：异步派发 → 父 run 继续 → run 结束按账本挂起 → durable wake → 同 `turn_id` resume episode；**G2（resume 上下文接线）已于 2026-09-26 修复，见 §7** |
| ② 父子"同一工作平面" | 设计口径是 **turn_id + obligation 账本（生命周期平面）**，显式**不是上下文平面**（子事件不混入父流） | **部分达成**：机制齐备（镜像/下钻/过滤视图/inline 审批），但运行期身份缺失（H7）、镜像 live-only 单槽位、控制面非实时、挂起态无事件/无 UI 表达（G3–G5） |
| ③ 全部子完成才退出 | I1 + `quorum=all`（不可放宽）+ settle 清账（A:189/1131） | **后端达成**：挂起记录只在 `TurnObligationsSettled` 时清除（loop.go:6690-6727）。"退出后自动继续"依赖 resume 投递：**G1（API 宿主 Runnable 挡住挂起 turn）已于 2026-09-26 修复，见 §7** |

一句话：**收尾门（不许提前退出）是硬的；"自动回到父平面继续干"这条腿在 CLI 成立，
在 API 宿主曾经断了（G1，runtime-server 生产形态可达，2026-09-26 已修复）；而
"同一工作平面"实际是"同 turn 的隔离事件面 + 镜像/下钻"，不是单一平面。**

## 1. 设计目标与权威锚点

设计稿 A = `docs/plan/supervised-turn-suspension-and-agent-task-control-plan-20260923.md`；
施工单 B = `docs/plan/supervised-turn-suspension-and-agent-task-control-implementation-plan-20260923.md`。

1. **总目标语义**：A:177 "turn 的生命周期覆盖其派生的全部子任务；turn 的执行不占用进程；
   子任务的判断权归主 Agent，兜底权归 runtime"。
2. **三处精确化**（A:1018-1051）：
   - 等待是 **turn 级状态**，不是 actor 级阻塞；强制的只有"收尾门"（A:1027-1034）；
   - "所有 Agent 结束才能继续" ⇒ **账本全终态才允许收尾**（A:1036-1040）；
   - "结束"必须**可判定**：deadline + watchdog 强制判终态（A:1042-1051）。
3. **两种等待形态**（A:1053-1067）：active wait（有界 `wait_agent`，占并发位/烧 token）与
   passive wait（挂起，零 goroutine/零 token，事件驱动 resume）。
4. **不变量**：I1（非终态 obligation ⇒ 禁止收尾，A:189）；I3（resume 复用同一 `turn_id`，
   A:191）；I9（无 durable store ⇒ 禁止挂起，A:197）；I10（join 可判定，A:198）。
5. **join 判定**（A:1124-1131）：`pending = count(obligations where turn_id=? and not terminal)`，
   `pending==0` 才允许收尾；`quorum=all` 默认且**不可配置放宽**。
6. **明确边界**：全仓无"子循环 / 工作平面 / 同一平面"字面表述；最接近的是 A:1014 引述的
   用户模型，与 v3 修订后的 A:1140（"派发后父继续运行"）。

## 2. 现状机制（代码锚点）

**派发（异步唯一）**
- `spawn_subagents` 只有异步语义（P3/C4-1）：`agent/loop.go:2857-2965`，回执 batch 句柄
  `parent_action=continue_parent_turn`；宿主无异步能力时给模型可见错误，不静默回退。
- `startBackgroundSubagentBatch`（loop.go:6732-6764）→ coordinator 落 durable batch/task 行并
  detach worker（`agent/subagent_batch_coordinator.go:1-11,395+`）→ 立即写挂起记录。

**挂起（被动、可恢复）**
- `parkBackgroundTurn`（loop.go:6641-6677）：`{turn_id, session_id, obligation_ids=batch+tasks,
  resume_queue, parked_at}`；非 durable store 不写（I9，`agent/suspension_gate.go`）。
- run 收尾 `settleParkedTurnOnRunEnd`（loop.go:6690-6727）：`TurnObligationsSettled` 为真才
  `ClearTurnSuspension`；否则记录保留 = turn 未结束。
- actor 侧派生缓存与 episode 语义：`chat/actor_resume_episode.go:30-64`（`nextTurnID` 复用挂起
  turn_id）、`chat/runtime_state.go:160-178`（`AwaitingObligations`/`Busy`/`AcceptsResume`）。

**恢复（事件驱动）**
- 终态投影 → `supervision.ScheduleWake` → `WakeConsumer.MaybeWakeParent`
  （`supervision/wake_consumer.go:79-176`）→ `DrainRunnable` 门控（`wake_scheduler.go:517-535`）→
  host `Deliver`。
- CLI：`Runnable = AcceptsResume()`（`cmd/aicli/commands/chat_actor_host.go:845-850`），
  `Deliver` 异步 `SubmitPrompt(AutoWakePrompt)`（:950-958）；
- API：`Runnable = !state.Busy()`（`internal/api/runtimeapi/supervision_batch_projector.go:167-174`），
  `Deliver` 为 `actor.SubmitPromptAsync(AutoWakePrompt)`（:175-181）；周期巡查同口径 Busy
  （`supervision_progress_check.go:364-393`）。
- resume 上下文构造器存在（`supervision/resume_context.go:195-247`、`ResumePrompt:527-547`），
  但 `DeliverResume` 仅在测试接线（`wake_consumer.go:145-153` + 全仓 `DeliverResume:` 仅测试命中）。

## 3. 差距清单

| # | 级别 | 差距 | 证据 | 影响 |
| --- | --- | --- | --- | --- |
| G1 | 高 | **（已修复 2026-09-26，见 §7）** API 宿主挂起 turn 的自动 resume 被 Runnable 挡住：挂起时 `Busy()==true`、`AcceptsResume()==true`，API 用 `!Busy()` | 原证据：`supervision_batch_projector.go:167-174`、`runtime_state.go:148-178`、`chat_actor_host.go:845-850`；后端审计逐条核实 API 五条入口（batch 终态桥 / controller wake / progress check / self-check / 手动 flush）全被同一门挡住，`DrainRunnable` 返回 `ErrWakeParentBusy`，投递代码走不到 | 原影响：critical 终态 wake 滞留 durable，resume 只能靠下一次自然输入补投；缺陷只在注入 durable batch store 的生产形态可达（`cmd/runtime-server/main.go:1174-1176`），多数单测因未注入 store 而掩盖 |
| G2 | 高/中 | **（已修复 2026-09-26，见 §7）** 生产未接 `DeliverResume`/obligation source：resume 走通用 `AutoWakePrompt` + preflight digest，§6.1 步骤 3 的"rollup digest + 可用动作清单"未按设计注入 | 原证据：`wake_consumer.go:144-163`；`grep DeliverResume:`/`SetObligationSource` 生产零命中；AC-P3-1c 仍 ⏳（B:1079） | 原影响：恢复回合的上下文完整性与 I1 判据明示性弱于设计；长账本时模型要自行巡检 |
| G3 | 中 | **（后端已修复 2026-09-26，见 §7.3；前端表达同批实施；真机 E2E 闭合半边见 §7.8）** 挂起态无可观测事件：`turn.suspended` 在生产无发射点（唯一引用是 `suspension_gate_test.go:127`，且断言"降级宿主不得发"）；挂起期仍无条件发 `agent.turn.finished`（`loop.go:578`）；前端零表达 | 原证据：上述锚点 + `frontend/src` grep 无命中。真机 E2E 补：`turn.suspended` 已在生产真发（model_items seq=14），但"同 run 结清"路径原先静默清记录 ⇒ 无 `turn.resumed` ⇒ 横幅永久卡住（已修） | UI/分析面把托管挂起看成"回合已结束"，用户无法区分"挂起等待"与"卡死"（设计 §6.8 要求可观测） |
| G4 | 中 | **（已修复 2026-09-26，见 §7.6）** 运行期身份缺失（H7 遗留）：background 批次运行中 `wait_agent(task_id)=missing`、`read_agent_events=0`，三套 UI 运行期缺入口 | 原证据：加固稿 `:84,168`（H7）。**生产侧**的早绑定（`scheduler.go:621-626 notifyTaskBound` → coordinator 运行期回写 `ChildSessionID`）已在更早的加固轮落地；缺口在**解析侧**：两宿主的 `resolveTargetSessionID` / `resolveLocalAgentTargetSessionID` 都不查批任务表 | 父平面"只能等"；下钻/巡检在运行期不可达 |
| G5 | 中 | **（已复核+修复 2026-09-26，见 §7.7）** 信息面不一致。复核结论：四项子诉求中**三项已被既有轮次关闭**——①镜像早已按子会话分行（后端 `streamLiveMergeKey` 按 `agent_id/session_id` 分组；前端 P1-1 实体身份，`trajectory-entity-identity.test.ts`）；②弹层早已实时（`useSubagentSession` live SSE + 退避重连 + inline 审批）；③`running_count` 唯一 JSON 键属 mesh 节点数，批次计数读侧走 `DeriveTaskCounts(task 行)`。**真缺口是第④项**：`waiting_approval`/`waiting_input` 在 agent 快照里被折叠成 `running`，父平面看不出"谁在等审批" | 复核锚点见 §7.7；原前端审计结论（`subagent_progress.go`/`subagent-session-dialog.tsx`/`use-subagent-session.ts`）与今日代码不符，已逐条订正 | 原"多子代理互相覆盖"已不成立；剩余影响收窄为"审批入口只在下钻内可见" |
| G6 | 低/口径 | **（已修复 2026-09-26，见 §7.4）** 设计文本自冲突：A:1065 "passive wait 不 Busy()" vs Q10/A:736（实现按 Q10：Busy 但 AcceptsResume）；A:1075 的 wait 上界 1h 与加固稿的 2m 冲突 | 原证据：A:1065 / A:1075 vs A:736、runtime_state.go、`agentcontrol.MaxWaitTimeoutMs` | 易误读门控语义与等待上界 |
| G7 | 低/偏差 | **（已修复 2026-09-26，见 §7.4）** `wait_agent` 上界设计写 1h（A:1075），落地 2m（2026-09-26 加固） | 原证据：`agentcontrol/wait_timeout.go:19-23`；设计稿已回填 2m | 主动等待上限主动收紧；设计稿与实现现已一致 |

已达成项（对照不变量）：I1 ✅（收尾门 + ledger next_action 规则）；I3 ✅（`nextTurnID` 复用 +
actor 重启恢复测试）；I9 ✅（C4-1 后语义＝仍异步 + 一次性 warning，不静默）；I10 ✅
（deadline 默认 + watchdog `watchdogForceTerminal`）。

## 4. 与验收标准的对照

| 项 | 状态 | 说明 |
| --- | --- | --- |
| I1 收尾门 / AC-P2-4f | ✅ | `ApplyAgentWaitLedger` 只填不许 finalize；settle 只在全终态清账 |
| I3 同 turn resume / AC-P1-1a、AC-P2-7a | ✅（后端） | `nextTurnID` 复用挂起 turn；重启恢复有测试（`chat/actor_restart_recovery_test.go`） |
| 主动等待有界 + 无进展挂起 | ✅（2026-09-26） | `wait-budget-and-max-window-hardening-plan-20260926.md`：预算耗尽 `next_action=suspend`、上界 2m |
| I9 耐久性前提 | ✅（按 C4-1 修订后语义） | 无 durable ⇒ 不写挂起 + 一次性 warning |
| I10 join 可判定 | ✅ | 默认 deadline + watchdog 强制终态 |
| **G1 自动 resume（API）** | ✅ 已修复 + 回归 | 缺口经代码级逐入口核实后，2026-09-26 将两处 admission 改为 `AcceptsResume()`；新增 `TestAPIBatchLifecycleProjector_ResumesParkedTurnOnCriticalTerminal`（含反证：改回 `!Busy()` 时 deliveries=0）；`go test ./internal/api/runtimeapi/ -count=1` 全包 ok。**仍未做活体 API 端到端**（见 §5 第 1 条残留） |
| AC-P3-1c resume 聚合报告端到端 | ✅ 已闭合（代码级） | 输入面（obligation/进度投影）+ 投递面（`DeliverResume` →`AutoWakePromptFor`）已接线，两宿主各有回归用例；真机 LLM 端到端未跑 |

## 5. 建议动作（按优先级）

1. ~~**G1（先修，1 行语义 + 测试）**~~ **已实施（2026-09-26，见 §7）**：两处 admission 改判
   `AcceptsResume()`，回归用例覆盖"durable parked turn + failed batch 终态 ⇒ 投递成功"。
   残留：真机 runtime-server 端到端验证（含 `wait_agent` 在同一 `turn_id` 上收口）尚未做。
2. ~~**G2（补设计闭环）**~~ **已实施（2026-09-26，见 §7）**：两宿主接 `DeliverResume` +
   `SetObligationSource`（进度投影随 `wireLocalSupervisionSources`/`wireSupervisionSources`
   同点接线），回归用例覆盖"resume 上下文带 pending_count / turn_id / rollup"与
   "实际投递的 prompt 是 ResumePrompt"。残留：真机 LLM 端到端。
3. ~~**G3（可观测性）**~~ **后端已实施（2026-09-26，见 §7.3）**：`turn.suspended` 在 durable
   首次挂起时发射（边沿、降级不发），`turn.resumed` 在 resume 投递成功后经宿主总线播报
   （触发类型 terminal/progress/approval/other + 有界 digest 摘要）；`agent.turn.finished`
   的"本次 run 结束"语义已在事件常量与契约注释里写死，并由 `turn.suspended/resumed`
   承担托管状态表达。前端表达与后端同一批推进（会话状态面显示"托管中"）。
   残留：真机端到端。
4. ~~**G4（运行期身份）**~~ **已实施（2026-09-26，见 §7.6）**：解析侧把 durable 批任务 id
   映射回子会话（`FindTaskByIDInParentSession` + 两宿主 resolver 接线），运行期
   `wait_agent(task_id)` / `read_agent_events(task_id)` / 下钻可用；未绑定身份时报可执行错误。
5. ~~**G5（信息面收敛）**~~ **已复核+修复（2026-09-26，见 §7.7）**：镜像分行与弹层实时
   经复核**早已成立**（不再改造）；真缺口为审批可见性——后端 agent 快照拆出
   `waiting_approval` / `waiting_input` 两档，前端面板把等待行留在"进行中"并显示
   "等待审批 / 等待输入"（警示色），父子两面都可看出"谁在等审批"。
6. ~~**G6/G7（口径）**~~ **已实施（2026-09-26，见 §7.4）**：设计稿 A:1065 改为"两种等待都
   `Busy()`、都接受插话（`AcceptsResume()`）"，A:1075 上界回填 **2m** 并注明加固稿。

## 6. 证据边界

- 本文结论来自三路只读审计 + 主线代码复核；前端/文档两路结论已回收，后端路已确认
  "异步派发 → run 继续 → 收尾挂起 → 同 turn resume"链路，并逐入口核实 G1（含绕过路径
  排查与测试覆盖缺口）；G1 的活体端到端未做。
- 未运行任何写操作；审计子代理均为只读沙箱（无文件写入）。

## 7. 实施记录：G1 + G2 修复（2026-09-26）

### 7.1 G1（API 宿主 resume 门控）

**改动**

| 文件 | 改动 |
| --- | --- |
| `backend/internal/api/runtimeapi/supervision_batch_projector.go` | wake consumer 的 `Runnable`：`ok && !state.Busy()` → `ok && state.AcceptsResume()`（挂起 turn 接受同一 turn 的 resume，仍拒绝并发新 turn；与 CLI 同口径） |
| `backend/internal/api/runtimeapi/supervision_progress_check.go` | 巡查投递的父会话可运行预检（actor 快照 + durable 回退）改用 `AcceptsResume()`，与 CLI 巡检同口径 |
| `backend/internal/api/runtimeapi/supervision_batch_projector_test.go` | 新增 `TestAPIBatchLifecycleProjector_ResumesParkedTurnOnCriticalTerminal` |

**验证证据**

- 定向：`go test ./internal/api/runtimeapi/ -run "TestAPIBatchLifecycleProjector|TestAPIControllerWake|TestAPISupervisionProgressCheck" -count=1` → ok。
- 全包：`go test ./internal/api/runtimeapi/ -count=1` → **ok（41.254s）**。
- 编译/格式：`go build ./...` exit 0；`gofmt -l`（3 个改动文件）无输出。
- **反证**：把 `Runnable` 临时改回 `!state.Busy()` ⇒ 新用例失败（`expected: 1, actual: 0`，
  "parked 父会话必须收到 critical 终态的同一 turn resume"）；恢复 `AcceptsResume()` 后转绿。

**仍未覆盖**

- 真机 runtime-server 端到端（真实 LLM + 真实子代理失败批次）未跑；建议观察一次
  `wait_agent` 是否在同一 `turn_id` 上收口。
- 同族残留（本次刻意不动）：`followup_task` / `send_input` 对 parked 会话仍按 `Busy()` 走
  排队而非起 resume episode（`session_runtime_support.go:1724,1727,1936`）；`DeliverResume`
  已于 §7.2 接线；挂起态事件/前端表达（G3）。

### 7.2 G2（resume 上下文接线）

**改动**

| 文件 | 改动 |
| --- | --- |
| `backend/internal/runtimeserver/supervision.go` | 控制面装配时，若 `hooks.SubagentBatchStore != nil` 则同时接 `SetObligationSource(BatchObligationSource)` 与 host-neutral 的 `SetProgressSource(BatchProgressSource)`（宿主之后可用自己的 live 镜像富化覆盖） |
| `backend/internal/api/runtimeapi/supervision_progress_check.go` | `wireSupervisionProgressSource` → `wireSupervisionSources`：进度投影 + 账本投影一次接好；巡查前调用点同步改名 |
| `backend/internal/api/runtimeapi/supervision_batch_store.go` / `supervision_handlers.go` | `SetSubagentBatchStore` / `SetSupervisionWakeScheduler` 在控制面可见后补接线（抵消装配顺序差异） |
| `backend/internal/api/runtimeapi/supervision_batch_projector.go` | 新增 `DeliverResume`（`AutoWakePromptFor(resume)`）与共用提交口 `submitSupervisionWakePrompt`（保留 `Deliver` 作 legacy 回落） |
| `backend/internal/api/runtimeapi/handler.go` | 新增测试缝 `supervisionWakeSubmit`（默认 nil，生产路径不变），供用例观察"实际投递的 prompt" |
| `backend/cmd/aicli/commands/chat_actor_host.go` | `Deliver` 的闭包体抽成 `deliverSupervisionWake(..., prompt)`；新增 `DeliverResume`；`beginWakeTurnRun` / `submitParentWakeTurn` 接受 prompt（`PrepareRunPrompt` 与提交使用同一文本） |
| `backend/cmd/aicli/commands/chat_actor_progress_check.go` | `wireLocalSupervisionProgressSource` → `wireLocalSupervisionSources`：加接 `SetObligationSource` |
| `backend/cmd/aicli/commands/chat_actor_host.go`（宿主启动） | `wireLocalSupervisionSources()` 随 ActorRegistry 就绪立即调用（不依赖 opt-in 巡查） |
| `backend/cmd/aicli/commands/chat_actor_resume_delivery_test.go`（新增） | `TestLocalHostWiresResumeDelivery`（DeliverResume/Deliver 双接线）+ `TestLocalSupervisionSourcesBuildResumeContextWithVerdict`（账本投影 ⇒ pending=0 / turn 锚点 / ResumePrompt 文本） |
| `backend/internal/api/runtimeapi/supervision_batch_projector_test.go` | G1 用例升级为端到端 resume 断言：生产 `Runnable` + 生产 `DeliverResume` + 提交缝捕获 prompt，断言 `turn_id=turn_parked`、终局判据与 batch id 都在 prompt 里 |

**验证证据**

- `go test ./internal/api/runtimeapi/ -count=1` → **ok（34.2s，全包）**。
- `go test ./cmd/aicli/commands/ -run "TestLocalSupervision|TestLocalHostWires|TestLocalHostWakeConsumer|TestLocalHostTurnEndCheck|TestLocalSupervisionSources|TestLocalHostWiresResumeDelivery" -count=1` → **ok**。
- `go test ./internal/supervision/ ./internal/runtimeserver/ -count=1` → ok；`go build ./...` exit 0；`gofmt -l`（本轮 10 个改动文件）无输出。
- **反证（两处）**：
  1) 去掉 CLI `wireLocalSupervisionSources` 的账本接线 ⇒ `TestLocalSupervisionSourcesBuildResumeContextWithVerdict` 失败（`pending_count expected 0, actual -1`）；
  2) 让 API `DeliverResume` 提交 legacy `AutoWakePrompt` ⇒ G1 用例失败（prompt 不含 `[supervision] resume`）。
- `cmd/aicli/commands` 全包仍有 3 个**与本次改动无关**的既有失败（`TestResumeProgressDynamicRowSurvivesIncrementalHistoryPublishOnScreen`、`TestChatDebugDisplayShowsStorageSection`、`TestPrintVisibleChatHistory_UnifiedPrimaryViewportRetainsHistoryTailAlongsideActiveReasoning`）：用 `git stash` 移除本轮 CLI 改动后逐一复跑，**同样红**（属未跟踪的在研功能/既有渲染用例）。

**仍未覆盖**

- 真机 LLM 端到端（resume 回合实际带着 ResumePrompt 跑完并产出终局报告）。
- `Deliver`（legacy）路径未删：未接 resume 的宿主/降级上下文继续走它，`AutoWakePromptFor(nil)` 亦逐字节回落。

### 7.3 G3（挂起/恢复可观测性，后端）

**改动**

| 文件 | 改动 |
| --- | --- |
| `backend/internal/events/turn_events.go`（新增） | `EventTurnSuspended` / `EventTurnResumed` 常量与语义边界（`agent.turn.finished` = 本次 run 结束，不等于托管 turn 结束） |
| `backend/internal/events/contract.go` | 两个事件登记为 **A+D 通道 + PersistCritical**（状态跃迁既要事后可查，也要在回合末尾巴帧让晚挂载 UI 看到）；顺带把 `internal/chat/events.go` 的 4 个 plan-* 常量按 **0 通道**登记（显式表态"当前无 chat 侧交付通道"，不改变投递行为） |
| `backend/internal/runtimeobserve/known_types.go` | 两个事件 + 4 个 plan-* 常量进"产品已知类型"目录（三分法不把它们当未知类型） |
| `backend/internal/events/contract_test.go` | 契约门禁补两个事件的 A+D 断言与已知目录校验；**顺带修复** commit `8e3744c2` 把 `internal/api/skills` 改名成 `internal/api/runtimeapi` 后未跟进的取样路径（该门禁此前恒红） |
| `backend/internal/agent/loop.go`（`parkBackgroundTurn`） | durable 首次挂起后发 `turn.suspended`（turn_id 由 emitRuntimeEvent 统一补；载荷 obligation_count / resume_queue_count / batch_id / parked_at）；读失败不猜"首次"、写失败不发——降级路径零信号（AC-C0-1d） |
| `backend/internal/supervision/resume_announce.go`（新增） | `ResumeAnnouncement` + `ResumeTriggerForReasons`（terminal > approval > progress > other，与 `WakeBudgetClassOf` 同源）+ `EventPayload()`（两宿主共用键名契约）+ 512 字符摘要截断 |
| `backend/internal/supervision/wake_consumer.go` | 新增 `Announce` 钩子；**仅投递成功**后播报（失败分支已 return）；钩子 panic 被吞（best-effort 观察者） |
| `backend/internal/api/runtimeapi/supervision_turn_events.go`（新增）/ `supervision_batch_projector.go` | 消费面接 `Announce` → `publishTurnResumed` 发到宿主总线 |
| `backend/cmd/aicli/commands/chat_turn_events.go`（新增）/ `chat_actor_host.go` | 同上（CLI 走 `host.EventBus`） |
| `backend/cmd/aicli/commands/chat_runtime_events.go` | CLI 时间线渲染：`turn.suspended` → "托管挂起：等待 N 个义务（batch …）"；`turn.resumed` → "托管恢复：触发 <trigger>，pending=N"，终态加"（全部终态，可产出终局报告）"且用 success 状态；数字键缺失时不假装 0 |
| `backend/cmd/contractgen`（再生成） | `frontend/src/types/runtime/event-contract.ts` 增补两个新事件（99 个类型） |
| 测试 | `agent/turn_suspension_events_test.go`（边沿一次）、`supervision/resume_announce_test.go`（触发映射 + 仅成功播报 + 降级口径 + panic 容忍）、API G1 用例扩展为同时断言总线上的 `turn.resumed`、CLI `TestLocalTurnResumedEventPayload` |
| 测试（CLI 渲染） | `chat_turn_events_render_test.go`：时间线文案/终态提示/dedup 键/缺数字降级/JSON 回放 float64 五条 |

**总线归属（两个宿主都不需要额外接线）**：`turn.suspended` 由 agent 的
`loop.emitRuntimeEvent` 发出，而两宿主都把 agent 的事件总线接到自己的宿主总线上
（API：`handler.go:4330 apiAgent.SetEventBus(h.getRuntimeEventBus())`；CLI：
`chat_actor_host.go:2047 apiAgent.SetEventBus(host.EventBus)`），因此它天然进入
A+D 交付通道；`turn.resumed` 由宿主自己发布（`publishTurnResumed` /
`publishLocalTurnResumed`）。

**验证证据**

- `go test ./internal/events/ ./internal/runtimeobserve/ ./internal/supervision/ -count=1` → **ok**（events 门禁在修复取样路径与 plan-* 登记后由恒红转绿）。
- `go test ./internal/agent/ -count=1` → **ok（14.0s，全包）**。
- `go test ./internal/api/runtimeapi/ -run "TestAPIBatchLifecycleProjector" -count=1` → ok；`go test ./cmd/aicli/commands/ -run "TestLocalHostWiresResumeDelivery|TestLocalTurnResumedEventPayload|TestLocalSupervisionSourcesBuildResumeContextWithVerdict" -count=1` → ok。
- `go test ./cmd/aicli/commands/ -run "TestRenderTurnSuspensionTimelineEvents" -count=1` → ok（CLI TUI 时间线表达）。
- `go build ./...` exit 0；`go test ./cmd/contractgen/ -count=1` ok（生成物与注册表一致）。
- **反证（三处）**：
  1) 去掉 `parkBackgroundTurn` 的"已挂起"边沿判定 ⇒ 同一 turn 播报 **2** 次（用例期望 1）；
  2) 去掉 `WakeConsumer` 的 announce 调用 ⇒ supervision 用例失败（成功投递后 announcements 为 0）；
  3) 去掉 API consumer 的 `Announce` 接线 ⇒ G1 用例失败（总线上 `turn.resumed` 为 0）。
- `gofmt -l`：新增文件与本次改动的 LF 文件无输出；`internal/runtimeobserve/known_types.go`、`internal/supervision/alerts.go` 等既有 CRLF 文件仍被 gofmt 标记（仓库既有现象，`alerts.go` 本轮未改动，可对照）。

**仍未覆盖**

- 前端表达的**已知边界**（同批已实施，见 §7.5）：M/K 计数来自目录投影而非权威账本；目录只在
  挂起边沿补刷一次；只接入前台（选中会话）事件流，刷新后无快照重建入口；清除仅由
  `turn.resumed` 驱动（`session_end` 不主动收敛）。
- 真机端到端：一次真实 durable 挂起 → UI 出现"托管中" → resume 后消失尚未实跑。
- `next_check_at`（设计表里的"预计下一次巡检时间"）未进载荷：agent 侧不知道宿主巡检间隔，宿主若需要应在自己的播报里补，不在 agent 事件里猜。

### 7.4 G6（设计稿口径漂移）

纯文档修正（`docs/plan/supervised-turn-suspension-and-agent-task-control-plan-20260923.md`），
不改代码：

- A:1065：原句"passive wait 不 `Busy()`"与 Q10（A:736）及实现（`runtime_state.go` 的
  `AwaitingObligations()` 计入 `Busy()`）冲突。改为"两种等待都计入 `Busy()`（挂起 turn 不接受
  并发新 turn），但都接受用户插话/steer（同一 `turn_id` 起新 episode，`AcceptsResume()`）"。
- A:1075：`wait_agent` 区间上界由 **1h 回填为 2m**（`agentcontrol.MaxWaitTimeoutMs = 120000`，
  加固稿 `wait-budget-and-max-window-hardening-plan-20260926.md` 的口径），并保留 min 10s /
  default 30s 不变。

### 7.5 G3 前端表达（"托管中：N 个任务运行中（M 完成 / K 异常）"）

**改动**（前端，`frontend/src`）

| 文件 | 改动 |
| --- | --- |
| `lib/parked-turn/events.ts`（新增） | 纯归约器：`turn.suspended` 按 session 建条（边沿、幂等）、`turn.resumed` 清除同会话同 turn 条目（迟到/串 turn 不误清）、`agent.turn.finished` 显式忽略、缺 `session_id` 不建条 |
| `hooks/workspace/use-parked-turns.ts`（新增） | `useParkedTurns`（归约 + 按会话 select）与 `useParkedTurnView`（快照 + `useSessionAgents` 目录投影计数，挂起边沿补刷一次目录） |
| `hooks/workspace/use-workspace-live.ts` | 挂起状态由该 hook 持有（前台会话事件入口），返回值新增 `parkedTurn` |
| `pages/workspace-page.tsx` / `components/workspace/workspace-shell/*`（types/main-section/dock/shell） | 单个 `ParkedTurnView` prop 逐层透传（不新增取数） |
| `components/workspace/session-mode-banner.tsx` | **落点**：composer 上沿的常驻状态行（会话存在即渲染，不依赖弹层/输入态）新增独立"托管"段（`role=status`、警告色、`data-testid=session-parked-turn`）；组件头注释写明落点理由与"不承载裁决动作"的契约 |
| `components/workspace/session-agents-panel-shared.ts` | `projectParkedTurnTaskCounts`（active→运行中、closed/ended→完成、stale→异常、unknown 不归类） |
| `i18n/resources/{zh-CN,en-US}/workspace/base.ts` | `composer.parkedTurn.waiting` / `.tasks`（跟随仓库 i18n 体系） |
| `types/runtime/event-contract.test.ts` | 生成物新增两事件导致双通道清单 7→9，同步断言 |

**验证证据**

- 定向：`npx vitest run src/lib/parked-turn src/hooks/workspace/use-parked-turns.test.tsx src/components/workspace/session-mode-banner.test.tsx src/components/workspace/session-agents-panel-shared.test.ts src/types/runtime/event-contract.test.ts` → **5 files / 67 tests passed**。
- 扩面（工作区全部面）：`npx vitest run src/components/workspace src/hooks/workspace src/lib/parked-turn src/types/runtime` → **203 files / 1483 tests passed（262s）**。
- `npx tsc -b --pretty false` → exit 0；`node scripts/verify-max-lines.mjs` → 0 个超 500 非空行；
  `node scripts/verify-frontend-i18n.ts` → scanned=927, violations=0。

**降级口径**：目录投影为空或三档全 0 时回落为"托管中：等待 {obligation_count} 个义务"；
`obligation_count` 缺失时不伪造数字（文案不出现"0 个义务"）。

### 7.6 G4（运行期身份：task id → child session，解析侧）

**根因**：H7 的**生产侧**（运行期把 `child_session_id` 早绑定写进 task 行）在更早的加固轮
已落地（`agent/scheduler.go:621-626 notifyTaskBound` → `subagent_batch_coordinator.go:1401-1428`
运行期回写，且 `subagent_batch_acceptance_test.go:161-272` 已钉住"运行中的 task 行必须带
child_session_id"）。剩下的缺口在**解析侧**：两宿主的工具面解析器只认 agent-control 记录
（session id / agent path / agent id），拿到派发回执上的 **task id** 时直接原样透传，最终由
上层报 `missing` / 0 events。

**改动**

| 文件 | 改动 |
| --- | --- |
| `backend/internal/subagentbatch/task_lookup.go`（新增） | `FindTaskByIDInParentSession(ctx, store, parentSessionID, taskID)`：只扫该父会话自己的批次（`BatchFilter{ParentSessionID}`，作用域不可由模型指定）；同名 id 优先**非终态**行（运行期解析目标就是"现在还在跑的那个"），否则回退最新终态行（终态 child_session_id 仍可读结果） |
| `backend/internal/api/runtimeapi/session_runtime_support.go` | `resolveTargetSessionID` 在 agent-control 记录之后、路径解析之前插入 task 解析；新增 `resolveTaskChildSessionID`（父会话取自 `toolctx.SessionID(ctx)`；未绑定身份 ⇒ 可执行错误"…has no child session bound yet…"，不把"身份未就绪"与"未知 id"混为一谈） |
| `backend/cmd/aicli/commands/chat_actor_registry.go` | 同上（父会话取自 `Host.baseRuntimeSessionID()`），新增 `resolveLocalTaskChildSessionID` |
| `backend/internal/toolbroker/broker.go` | `wait_agent` / `read_agent_events` 两套工具定义的 `id` 描述补上"batch task id from a dispatch receipt"，让模型知道运行期可以用 task id 取身份 |
| 测试 | `subagentbatch/task_lookup_test.go`（活行优先 / 作用域 / 回退 / 空输入）、`runtimeapi/session_agent_task_identity_test.go`、`commands/chat_actor_task_identity_test.go`（两宿主各 3 条：映射成功 / 未绑定报错 / 裸 id 与跨会话不越权） |

**验证证据**

- `go test ./internal/subagentbatch/ `→ ok；`go test ./internal/toolbroker/ -count=1` → ok（13.9s）。
- `go test ./internal/api/runtimeapi/ -count=1` → **ok（32.3s 全包）**。
- `go test ./cmd/aicli/commands/ -count=1` → 仅 2 个**既有**渲染失败（与本次无关，见 §7.3 同款）。
- `go build ./...` exit 0；新文件 `gofmt -l` 无输出。
- **反证**：临时让两宿主的 `resolveTaskChildSessionID` 直接返回未命中 ⇒
  API `TestResolveTargetSessionIDMapsTaskToChildSession` / `...ReportsUnboundTask` 与 CLI
  `TestResolveLocalAgentTargetSessionIDMapsTaskToChildSession` / `...ReportsUnboundTask`
  全部失败；恢复后全绿。

**后续（§7.7 已复核）**：原判"父流镜像整批单槽位 + live-only 是缺陷"经复核**不成立**
（live 队列与前端轨迹早已按子会话分行；live-only 是设计分工）。G5 的真实缺口收窄为
"父平面看不出谁在等审批"，已在 §7.7 修复。

### 7.7 G5（信息面复核：三项已被既有轮次关闭，一项真缺口已修）

**复核 1（镜像单槽位 → 不成立）**

- 后端：`session_runtime_stream_live_queue.go:161-185 streamLiveMergeKey` 明确按
  `agent_id`/`session_id`（其次 `AgentName`）分组，注释逐字写着"仅凭 AgentName 会把
  所有子代理的进度折叠成同一 key，latest-wins 下互相覆盖"——即**该问题已在 live 队列
  层修掉**；镜像本体 `supervision/subagent_progress.go` 的节流键也是 `childSessionID + "\x00" + tool identity`，天然按子会话独立。
- 前端：`lib/trajectory/trajectory-entity-identity.test.ts:76`（"修复前的表现是「所有子代理共用
  runtime-0」"）与 `recovery.ts:358-370`（P1-1 批次 20）证明轨迹折叠行早已按子会话建实体。

**复核 2（弹层非实时 → 不成立）**：`hooks/workspace/use-subagent-session.ts` 就是**实时**实现——
先 `GET /runtime/events` 增量回填，再 `GET /runtime/stream?live=1` 持续跟随（游标续传、
3 次退避重连、`reconnect()` 手动重试），并跟随 `approval_requested` / `approval_resolved`
渲染 inline 审批（P1-5 方案 4，`subagent-session-dialog.tsx:205-248` 用例锁定）。

**复核 3（`running_count=0` 矛盾 → 与 UI 无关）**：全仓 `"running_count"` JSON 键只出现在
`cmd/aicli/commands/web_handlers_mesh_sessions.go:84`，语义是"该工作区活节点数"（mesh），
与子代理批次无关；批次计数的读侧一律从 task 行派生（`supervision/batch_progress.go:154
subagentbatch.DeriveTaskCounts`），前端不读批次计数列。

**复核 4（审批可见性 → 真缺口，已修）**：`normalizeAgentRuntimeState` 把
`SessionWaitingApproval` / `SessionWaitingInput` 折叠进 `AgentRuntimeStateRunning`，父平面
（agent 面板）因此无法区分"在跑"与"在等审批"，审批入口只存在于已打开的下钻里。

| 层 | 改动 |
| --- | --- |
| 后端（本批） | `internal/api/runtimeapi/agent_control_runtime_state.go` 新增 `AgentRuntimeStateWaitingApproval` / `AgentRuntimeStateWaitingInput` 两档并拆开映射（`running` 只保留 Running/Rewinding；未知仍返回空串，读不到 ≠ 已结束）；测试改断言 + 新增 waiting_input 用例 |

**验证证据（后端）**

- `go test ./internal/api/runtimeapi/ -run TestListAgentControlAgents -count=1` → ok；
- `go test ./internal/api/runtimeapi/ -count=1` → **ok（36.3s 全包）**；`go build ./...` exit 0。

**前端证据（同批子代理实施，父侧独立复跑）**

改动面：`types/runtime/agents.ts`（`RuntimeAgentRuntimeState` / `RuntimeAgentDisplayStatus`
加两档）→ `api/runtime/agents.ts`（normalize 接受新值，其余仍 `unknown`）→
`session-agents-panel-shared.ts`（`agentDisplayStatus` 原样透出；`isAgentRunning` 归入
"进行中"分区；`agentStatusToneClass` 警示色；`projectParkedTurnTaskCounts` 把等待态计入
"运行中"档，避免等待行三档全不落）→ `panels-agents.ts`（zh/en 词典）。

- 定向：`npx vitest run src/api/runtime/agents.test.ts src/components/workspace/session-agents-panel-shared.test.ts src/components/workspace/session-agents-panel.test.tsx src/components/workspace/session-agents-tree.test.tsx src/hooks/use-session-agents.test.tsx`
  → **5 files / 87 tests passed**（父侧复跑一致）。
- 扩面：`npx vitest run src/components/workspace src/hooks/workspace src/api/runtime src/types/runtime src/lib/parked-turn`
  → **230 files / 1787 tests passed（260s）**。
- `npx tsc -b` exit 0；`verify-max-lines` / `verify-frontend-i18n` OK；改动文件 eslint 干净。
- **反证**：临时把 `agentDisplayStatus` 的等待态透出改为空分支 ⇒
  `session-agents-panel-shared` / `session-agents-panel` / `session-agents-tree` 各 1 条新用例转红；
  恢复后全绿。

**边界（子代理报告 + 父侧确认）**：root 行仍回退身份状态（当前会话自己不当"等待审批"行显示，
已由用例固定）；等待态不新增身份状态，`canStopAgent/canResumeAgent` 仍按身份状态授权；
面板文案结构未改（等待态只是把行留在"进行中"并亮警示色 + 标签"等待审批/等待输入"）。

**口径订正**：原 G5 把"live-only"列为缺陷，但 `subagent.progress` 是**设计上**的 live-only
（`internal/events/contract.go:128` 登记 + 契约注释"never append it to the durable event
store"，避免子代理高频进度污染父 transcript）；重放/刷新后的权威视图由子会话自己的
durable 事件流（下钻）承担——这是分工，不是缺口。

### 7.8 真机 E2E（远程调试 session_20260926184051_wySvqTPT / 127.0.0.1:61409）

按 `docs/e2e/debug-guide.md` 的 `/web/api/invoke` 通道对独立进程做了两轮真实注入
（provider=commandgo / deepseek-v4.1-flash），证据落在
`artifacts/remote-debug-61409/`（两轮响应原文 + `/debug/chat/status` 快照）。

**轮 1（工具名错误，非缺陷）**：`spawn_agent`（单数）**不支持** `execution_mode`
（broker 回 `TOOL_INVALID_ARGS` 并列出支持参数）；后台批量派发属于 `spawn_subagents`
（复数）。turn 记录 `status=failed`（step=2），模型按纪律原样回报、未重试。

**轮 2（真实链路，抓到两个缺口）**：

- **G3 正向验证通过**：`spawn_subagents` 派发后台批次后，渲染项出现
  `turn.suspended`（`/debug/chat/status` 的 model_items seq=14，载荷
  `batch_id`+`obligation_count`）——G3 的生产发射点与事件注册在真机上成立。
- **缺口 1（G4 残留，未修）**：派发回执只有 `{batch_id, status, task_count,
  execution_mode, parent_action}`（`internal/agent/loop.go:2928-2935`），**不含 task id、
  也不含 child session id** ⇒ 父代理拿不到任何可用身份。模型退而用 `batch_id` 调
  `wait_agent`，得到 `agent.status=missing / exists=false` 且
  `next_action=consume_ready_outputs`（**误导**：既无 ready 输出、也不是可消费目标）。
  这正是加固稿 H7 的原始诉求"在派发回执中携带身份映射"尚未落地的那一半。
- **缺口 2（G3 闭合半边，已修）**：批次在父 turn 结束前就全部终态 ⇒
  `settleParkedTurnOnRunEnd` 静默清掉挂起记录（`loop.go:6740-6746`），**不发任何事件**；
  而前端契约是"只有 `turn.resumed` 清除挂起态"（`frontend/src/lib/parked-turn/events.ts:15,168`）
  ⇒ 同 run 结清的场景下"托管中"横幅**永久卡住**（本轮 turn 全程无 `turn.resumed`，
  run 以 `agent.turn.finished` 正常收尾）。

**缺口 2 的修复（本批）**

| 文件 | 改动 |
| --- | --- |
| `internal/events/turn_events.go` | 新增 `TurnResumedTriggerSettled = "settled"`：`turn.resumed` 的非 wake 闭合触发值（结清 ≠ resume episode，但同样是挂起态的闭合边沿） |
| `internal/agent/loop.go` | `settleParkedTurnOnRunEnd` 成功清记录后补发 `turn.resumed`（trigger=settled、turn_id/session_id/obligation_count；清失败仍只报降级、不发假闭合） |
| `internal/agent/turn_suspension_events_test.go` | 新增 `TestSettleParkedTurnAnnouncesResumeClosure`：未结清不播报 / 结清播报一次 / 重复结清不重播 |

**验证**：`go test ./internal/agent/ -run "TestSettleParkedTurn|TestDurableParkAnnounces"` → ok；
`go test ./internal/agent/ -count=1` 全包 → **ok（14.4s）**；`go build ./...` exit 0。
**反证**：临时注掉补发 ⇒ `TestSettleParkedTurnAnnouncesResumeClosure` 在"结清是边沿：
同一 turn 只播报一次"处转红，恢复后全绿。前端无需改动（归约器只看 type + turn 身份，
不依赖 wake 字段）。

**上述①—③已在本轮修复并真机复验，见 §7.9。**

### 7.9 真机闭环（修复后二进制；含 R2 轮发现的三个新缺口）

第二轮真机（新构建 `aicli-verify-fixed*.exe`，独立进程 61591/61592，会话
`session_20260926185913_inV5QHfR`）把 §7.8 的遗留全部收敛，并在过程中又抓到
**一个更深的缺口**（子代理会话不落 SessionStore）。最终一轮（v2 二进制）四条链路全绿：

| # | 缺口（R2 轮发现） | 修复 | 真机证据（verify2） |
| - | - | - | - |
| 1 | 派发回执只有 `batch_id`，无 task/child 身份 | `loop.go` 回执补 `tasks[]`（task_id + 已绑定时的 child_session_id） | 回执含 `"tasks":[{"status":"pending","task_id":"envprobe"}]` |
| 2 | `wait_agent(<batch_id>)` 把不存在的目标计成 ready 并回 "consume_ready_outputs" | `toolbroker.FinalizeAgentWaitResult` 新增 missing-target 分支：`target_not_found: … take a task_id from the receipt tasks[] …` | 负路径返回 `target_not_found`（不再误导） |
| 3 | **子代理会话不落 SessionStore**：`wait_agent(task_id)` 解析出的 `subagent_<task>_<uuid>` 在会话库里恒为 `missing`（账本行才是权威） | 新增 `subagentbatch.FindTaskByChildSessionIDInParentSession`；两宿主 `snapshot` 回退到账本行投影（`Exists=true`、终态→idle/stopped、结果摘要→Output） | `wait_agent(envprobe)` → `exists=true, status=idle, current_task_id=envprobe, current_task_status=succeeded`，`output` 就是子代理两条命令的真实输出（go1.27.1 + `cfa87c71`） |
| 4 | （承接 §7.8 缺口 2）挂起结清无闭合事件 | `settleParkedTurnOnRunEnd` 补发 `turn.resumed(trigger=settled)` | model_items：`turn.suspended`(seq5) → `turn.resumed`(seq23) 成对出现 |

**验证**：`go test ./internal/agent/ ./internal/toolbroker/ ./internal/subagentbatch/ -count=1`
全绿；`go test ./internal/api/runtimeapi/ -count=1` ok(34.8s)；`go test ./cmd/aicli/commands/
-run "TestLocalAgentSnapshot|TestWaitAgent|TestLocalActorRegistry"` ok(32.3s)；`go build ./...` exit 0。
**反证**（各自单独注掉修复后转红、恢复即绿）：回执 tasks（`TestSpawnSubagentsLargeResultStaysOutOfParentContext`）、
missing-target 指引（`TestFinalizeAgentWaitResultMissingTargetGuidesToReceiptTaskID`）、
账本行回退（`TestLocalAgentSnapshotFallsBackToBatchTaskRow`）、结清闭合（`TestSettleParkedTurnAnnouncesResumeClosure`）。

**证据目录**：`artifacts/remote-debug-61409/`（两轮 invoke 原文、`status-round2.json` /
`verify-status.json` / `verify2-status.json`、`verify-invoke.assistant.md`）。

**剩余（诚实记录）**：① 账本行回退的 **API 宿主**（`sessionAgentController.snapshot`）与 CLI 对称实现
~~只做了编译 + 全包回归，未加独立单测~~ **已补（2026-09-28，`0122bdd7`）**：
`session_agent_batch_task_snapshot_test.go` 覆盖终态行投影（成功/失败证据/未绑定不伪装命中）；② `wait_agent(task_id)`
返回的 `obligations[]` 在 "无挂起记录"场景下 ~~仍缺省（模型只能从 agent 投影取状态）——按需要可再补账本直出~~
**已补（2026-09-28）**：两宿主新增 `waitTargetObligations`/`localWaitTargetObligations`——把调用方显式等待的
目标（batch id / task id / 子会话 id）映射到所属批次，走与挂起路径同源的 `BuildWaitLedger` 直出账本
（只覆盖 wait 目标、不扫描全部批次；预算键保持空，fail-open 不武装）。直出行不吃"全终态立即 finalize"
短路，保证 agent 投影（状态/输出/失败原因）仍随本次等待返回。回归：
`TestWaitAgentObligationsDirectFromTargetsWithoutParkedRecord`、
`TestWaitAgentDirectObligationsFinalizeWhenTargetTerminal` 及 CLI 两条镜像用例。

### 7.10 长时运行/监管真机轮（session_20260926191909_od40XJn6，51875）

场景：父 turn 4.6 分钟、子代理批任务长跑，父代理反复 `wait_agent` + `subagent_status` + `send_input`。

**结论：`wait_agent` 不会卡住进程**。父 turn 全程 `/web/api/health` 2–3ms、`/web/api/turn` 1–3ms，
goroutine 稳定 79–80；等待中抓的 62KB goroutine dump：`semacquire` 0、`sync.(*Mutex).Lock` 0、
`time.Sleep` 1、`chan receive` 4（无锁竞争、无阻塞树）。等待窗口到期即返（`execution_continues=true`），
子代理就绪后 4.2s 返回全量 output；`send_input(子会话 id)` 在运行中投递成功（`queued=true`），
子代理被唤醒产出进度报告（message_count 25→39）。

本轮又抓到并修掉 3 处（修复后二进制 `aicli-5x-fix2.exe` 真机复验通过）：

| # | 缺口 | 修复 | 复验证据 |
| - | - | - | - |
| F5 | 等待窗口"静默超调"：`startedAt` 在入口打点，deadline 却在目标解析/订阅之后创建，准备耗时被算进 `waited_ms`（请求 90000 → waited_ms 92617，两轮复现） | 两宿主四处等待段（status/mailbox × CLI/API）改用 `context.WithDeadline(ctx, startedAt.Add(timeout))`，窗口与 `waited_ms` 同锚 | 请求 8000 → 钳制到 minWait=10000（`wait_timeout_clamped=true` 如实回显），`waited_ms=10000`，**与生效窗口零偏差** |
| F6 | 终态失败批次被标成 `supervision_state=blocked`（blocked 语义=等外部裁决），与 reason "finished with status failed" 自相矛盾 | 两宿主投影器 `BatchFailed` → `SupervisionTerminated`（severity=critical、resolution=unresolved 不变，失败仍进父代理必读清单） | 失败行 `"SupervisionState":"terminated"`、`ActionRequired:false`、`Stale:true` |
| F7 | `subagent_status` 的 next_action 用矩阵计数（含"subject 已不在控制面"的陈旧行），与自身 digest 的 `0 action_required` 矛盾，模型被指使去裁决一个 `allowed_actions:null` 的行 | 新增 stale 感知的 `supervisionDescendantsNextActionExcluding` + `staleSubjectKeys`；`include_digest=true` 时以 digest 的 stale 裁决为准，无指引则回落 `digest_next_action` | 同一响应顶层与 `cache_safe_summary` 均为 `next_action=no unresolved supervision items for this session` |

**验证**：`go test ./internal/supervision/ ./internal/toolbroker/` 全绿；`./internal/api/runtimeapi/` ok(25.4s)；
`cmd/aicli/commands -run "Projector|Supervision|Wait|Batch|Subagent"` ok(32.8s)；`go build ./...` exit 0。
**反证**：注掉 F6 → `TestLocalSubagentBatchLifecycleProjectorPersistsAndDeduplicates` 转红；
注掉 F7（退回 `Summary.ActionRequired`）→ `TestBroker_Execute_SupervisionDescendantsStaleRowDoesNotAskForDecision` 转红；恢复即绿。
**证据**：`artifacts/remote-debug-51875/`（probe-longrun.log、goroutine-mid-wait.txt、session-trace.json、
fix-trace.txt、fix/fix2 的 invoke 原文与响应）。

**新遗留（诚实记录）**：① ~~批次在**派发前**失败（如 single-writer 策略拒绝）时，`wait_agent(task_id)`
返回硬错误 `TOOL_BROKER_FAILURE`（"batch task … is failed but has no child session bound yet; retry shortly"），
应改为可读的缺失/失败观测并把父代理指向批次失败原因，而不是让它"稍后重试"~~
**已修（2026-09-28）**：终态未绑定任务不再返回硬错——解析层按可寻址 id 透传，snapshot 以 task id 兜底投影账本行，
`wait_agent(task_id)` 得到 `stopped` + `ErrorClass: ErrorCode` 的失败观测；两宿主对称
（`session_runtime_support.go` / `chat_actor_registry.go`），回归
`TestWaitAgentTaskIDProducesPreDispatchFailureObservation`、
`TestLocalWaitAgentTaskIDProducesPreDispatchFailureObservation`。非终态未绑定任务保留"身份未就绪"可执行错误
（与"未知 id"不混同）；② `wait_agent` 对
read_only 子代理的执行面限制 ~~（管道/命令替换一律拒绝）~~ 属策略设计，但**派发建议**上父代理应默认给
"要跑命令"的子代理 `read_only=false`（本轮 pre-dispatch 失败即因两个任务都成了 writer）。
**已补（2026-09-28）**：派发契约（`policy.ReadOnlyChildOptionDescription`，两 spawn 工具共用）尾部新增
可执行的 shell 面说明——**先修正审计表述**：实际规则是逐段白名单（`cat a | head -5` 允许；重定向 `>`/`<`、
命令替换/动态展开 `$`/反引号/`%`/`!` 一律拒绝，见 `AssessShellReadOnlyCommand`），不是"管道一律拒绝"；
文案据此写为"redirection, command substitution and dynamic expansion are denied, and compound commands
(&&, ||, ;, |) must be read-only in every segment — leave read_only unset for children that need writes or
general shell syntax"，父代理在派发前即可决策，而非等子代理运行中反复被拒后上浮重派。回归：
`TestSpawnAgentReadOnlyDescriptionCarriesDispatchAdvice`（前缀契约由既有
`TestSpawnAgentReadOnlyDescriptionMatchesPolicyConstant` 继续钉住）。

### 7.11 架构问答：长任务截止（10min 估计 / 5min 必须结束）+ 主代理巡检节奏（2026-09-26 真机）

**三套截止机制（可叠加，按确定性排序）**

| 机制 | 参数/入口 | 语义 | 强制者 | 真机证据 |
| - | - | - | - | - |
| 批次墙钟 | `spawn_subagents(wait_timeout_sec=300)` | 整批 deadline，超时任务记 `timed_out`、批次 `BatchTimedOut` | 批次协调器（与宿主模式无关） | schema 原文 "Optional batch deadline (seconds) for background batches"；`loop.go:6793-6809` → `BatchDeadline`；`TestStartBackgroundDeadlineMarksTaskTimedOut` |
| 每任务执行截止 | `agents[i].timeout=300` | 子代理执行上下文 deadline（"Time budget: N seconds." 写进子代理提示词） | 子代理执行器（context deadline） | 实测 `timeout=120`：子代理 ~108s 后以 `failed_with_result` + `context deadline exceeded` 终止，父代理等待提前返回 |
| 显式取消 | `close_agent(child_session_id)` / `subagent_control(cancel)` | 立即停止 | 父代理/宿主 | 实测 `closed_count=1`（child → `status=stopped`） |

**巡检/等待节奏（默认值 = 你本机生效值，`C:\Users\vince\.aicli\config.yaml` 未覆盖任何相关键）**

- `wait_agent` 窗口：默认 **30s**、最小 **10s**、**上限 2min**（`WaitTimeoutMode=clamp`，超限被钳制并回显 `wait_timeout_clamped`）。窗口结束即一次巡检机会；窗口内 turn 被同步工具调用占住，不能做别的事。
- 等待预算：`maxConsecutiveWaitWithoutProgress=6`（2026-09-27 起；原为 2，仅 4 分钟耐心）——连续 6 个无 `terminal_delta` 的等待段后宿主**不再开新窗口**，返回 `next_action=suspend`（`wait_budget_exhausted=true`）；I1 把提前收尾转成 turn 挂起（零 goroutine/零 token）。
- 事件驱动：子代理 ready 时等待立即返回（实测 2786ms / 3691ms，而非等满窗口）；steer/ESC 立即打断；子代理终态经 wake 在 turn 边界 resume（`resume_queue` 可见）。
- 宿主后台（模型不可见）：等待循环 500ms 轮询；执行监督者 **5s** 扫描（deadline/stall/approval）；批次协调器 1min 心跳；周期巡检 `progress_check_interval` **默认关闭**（开启下限 30s），且父会话忙时跳过；每个父 turn 开头必有一次 preflight digest（always-on）。
- 真实限制：监督 wake **不会打断正在进行的 wait**，只在当前等待段返回后投递 ⇒ "截止触发"到"父代理处理"最坏晚一个窗口（默认 ≤2min）。

**结论**：① "5 分钟必须结束"满足（声明式优先，真机验证）；② LLM 可自主处理（声明式 + 每 ≤2min 回归控制权 + cancel 工具），但它没有"定时唤醒"，且子代理内部工具超时（实测 2min）会先于外层截止终止任务 ⇒ 长命令必须显式给子代理 `timeout_ms` 或分片；③ 巡检间隔 = min(等待窗口, 默认 2min 上限)，要更细就调小 `agents.maxWaitTimeoutMs`（如 30s）或让模型用 `timeout_ms: 30000`。
**宿主差异（需注意）**：执行监督者默认模式 CLI=**observe**（`AICLI_EXECUTION_SUPERVISOR_MODE=enforce` 可开）、API=**enforce**；observe 只记录决策与通知，enforce 才发 interrupt/cancel-grace。
**证据**：`artifacts/remote-debug-51875/arch-invoke*、arch3-invoke*、arch4-invoke*`（含 waited_ms 时序与子代理终止原文）。

### 7.12 轻量子代理挂起闭环（2026-09-30，缺口 1–3）

**背景**：2026-09-30 真机复盘（`session_20260929175325_cxHK8mPD`）发现：父 turn 派发
`spawn_agent` 轻量子代理后 `wait_agent` 超时收尾，UI 只显示 "Worked for 2m 35s"，
等待期没有任何"托管中"表达（该会话 `turn.suspended` 计数 0）；而 §6.12 挂起/账本
此前只覆盖 subagent batch。子代理完成后的 wake 仍能续跑父 turn（实测同秒 resume），
但"等待子代理"与"已收工"在 UI 上不可区分。

**改动（CLI 宿主；API 宿主待接）**

| 文件 | 改动 |
| --- | --- |
| `internal/subagentbatch/turn_suspension.go` | `agent_session:` 前缀义务编码（复用 `obligation_ids_json`，无 schema 迁移）+ `ObligationAgentSessionIDs()`；批次 id 解析跳过前缀项 |
| `internal/subagentbatch/obligation_resolver.go`（新增） | `AgentSessionObligationResolver` + `TurnObligationsSettledWith`：批次 + 子会话义务全终态才可清账；缺行跳过、无法判读保持挂起 |
| `internal/subagentbatch/wait_ledger.go` | `BuildWaitLedgerWith`：账本新增 `agent_session` 行（active/closed/missing），wait_agent 的 pending/I1 指引覆盖子代理 |
| `internal/agent/loop.go` | `LoopReActConfig.AgentSessionObligations`；`settleParkedTurnOnRunEnd` 改用 With 版本 |
| `cmd/aicli/commands/chat_actor_agent_obligations.go`（新增） | ①派发即挂起 `parkLocalAgentChildObligation`（仅 durable batch store + 判读面齐备时写）；②宿主侧 `turn.suspended` 边沿播报（session\|turn 去重，批次已播报则不重复）；③判读器读 supervision 通知面（agent_completed/failed/interrupted ⇒ terminated）；④回合结束仍有在途子代理且无挂起记录 → 复用 `subagent.suspension.unavailable` 降级信号 |
| `cmd/aicli/commands/chat_actor_registry.go` | Spawn 成功且 queued ⇒ park；`localWaitLedger` 改用 With + `CurrentTurnID` 兜底（run 内即可见账本） |
| `cmd/aicli/commands/chat_actor_host.go` | 判读器接入 loop config；注册回合末在途信号 binder |
| `cmd/aicli/commands/chat_runtime_events.go` | `turn.suspended` dedup 身份补 `turn_id`（无 batch_id 的宿主侧挂起不再被去重吞掉） |
| `frontend/src/lib/parked-turn/events.ts` 等 11 文件 | 迟到唤醒瞬时通知：与挂起分表存放、TTL 10s 自动消失、同会话新一轮 `turn.suspended` 清除、会话隔离；banner 非阻塞一行（`session-resumed-notice`） |

**验证**

- `go test ./internal/subagentbatch/ ./internal/agent/ -count=1` → ok。
- `go test ./cmd/aicli/commands/ -run "TestLocalAgentSessionObligationResolver|TestLocalSpawnPark|TestLocalParkedChildObligation|TestLocalInFlightSignal|TestLocalWaitLedger|TestWaitAgent|TestRenderTurnSuspension|TestLocalTurnResumed|TestLocalHostWakeConsumer|TestLocalSupervisionSources|TestLocalHostWiresResumeDelivery" -count=1` → ok；
  新增 5 条用例覆盖：判读器（终态投影）、派发挂起 + 边沿一次、终态结算、在途降级信号、真实 `Spawn` 集成（RuntimeState.CurrentTurnID 兜底）。
- `go build ./...`、`go vet`（3 包）、`gofmt -l`（改动文件）干净。
- 前端：`npx vitest run src/lib/parked-turn src/hooks/workspace/use-parked-turns.test.tsx src/components/workspace/session-mode-banner.test.tsx` → 3 files / 47 passed；`npx tsc -b` exit 0。

**边界（诚实记录）**：① API 宿主（runtime-server）的 spawn 路径**已于 2026-09-30 补齐**
（见 §7.14）；② 判读面缺失（无 supervision store）时不写挂起（不写无法结算的记录），
改由回合末降级信号表达；③ 真机 LLM 端到端未跑。

### 7.13 角色词表归一与子代理 shell 能力（2026-09-30，继续优化）

**问题**：`DefaultToolsForRole` 只认精确角色名（researcher/tester/writer/implementer/…），
而模型实际使用的是 spawn_agent `agent_type`（explore/general/plan）与 spawn_subagents
`task_type`（config/explore/generate/implement/…）。未归一的 role 落到 nil（"继承父策略"），
同时 `CapabilitiesForTask(role, …, toolNames=nil, …)` 的能力面只剩 read_only+write_fs：
子代理继承到父工具面，却被能力门禁挡掉 shell（exec_shell）与 network（fetch/web_search）。

**改动**

- `internal/policy/capability_scope.go`：新增 `RoleFamilyForTask`（单一别名表：
  research/test/write 三族）与 `CapabilitiesForTask` 的角色家族能力地板——仅当任务未声明
  工具表（继承父策略）时生效；显式工具表仍以工具为准，未知角色不扩权。
- `internal/agent/role_defaults.go`：默认工具表按同一家族表归一，新增
  explore/understand/research/plan（研究族）、implement/generate/modify/refactor/migrate/
  integration/config/security/general/worker/default（写族）、verify/validate（测试族）等别名。
- `cmd/aicli/commands/chat_actor_agent_obligations.go` + `chat_actor_registry.go`：
  spawn_agent 成功登记挂起义务后，工具结果携带 `next_action=obligation_registered…`，
  把「wait_agent 会报 agent_session 义务行 / 直接收尾也会被挂起并在子代理终态续跑」
  告诉模型（仅在义务真的落账时宣告，避免承诺不会发生的挂起）。

**验证**：`go test ./internal/policy/ -count=1`、`go test ./internal/agent/ -count=1` 全绿；
`go build ./...`、gofmt 干净；新增 `role_defaults_vocabulary_test.go` 与
`capability_scope_role_test.go` 钉住别名命中与「未知角色不扩权」。

**边界**：未知 role（自定义 agentdef 名等）仍保持 nil / 不扩权；`plan` 家族按研究族
（读 + shell + 网络，不写盘）；能力扩权仍受父策略 `intersectCapabilities` 限制，绝不
宽于父会话。

**追加修复（同日，真机证据）**：一个 `agent_type=general` 的轻量子代理在真机上报
`policy:capability not allowed by execution policy: exec_shell`——连 `go test` 都跑不了。
根因不在词表，而在 agentdef 派生链：`applyLocalChildAgentdefToolPolicy`（CLI 与 API 各
一处）用 `DeriveChild(allowlist, readOnly)` 派生，role 传成 `""`；内建 `general` 的
`Tools: nil`（继承全套工具）又让 allowlist 为空，于是
`CapabilitiesForTask("", …, nil, …)` 的能力地板只剩 `read_only+write_fs`，子代理继承到
shell 工具面却被能力门禁拒绝。修复：两处改用
`DeriveChildForTask(allowlist, readOnly, agentType, nil)` 把真实角色传下去（`general`
→ 写族 → 补回 `exec_shell`；仍受父策略交集约束，绝不宽于父会话）。回归测试：
`chat_child_shell_capability_test.go`（general/plan/explore 派生后 `exec_shell` 必须可用）。

### 7.14 API 宿主 spawn_agent 挂起义务 parity（2026-09-30）

**改动（backend/internal/api/runtimeapi）**

- 新增 `session_agent_obligations.go`：`apiAgentSessionObligationResolver`（与 CLI 同口径：
  supervision 通知面 + `SupervisionState==terminated`；无行=found=false，不视为完成）、
  `parkAgentChildObligation`（判读器缺失/非 durable store/无 turn id 均静默跳过，绝不影响
  Spawn）、`apiTurnIDForSession`（ctx turn id → `CurrentTurnID` → `SuspendedTurnID`）。
- `session_runtime_support.go`：`Spawn` 在 queued 成功后登记 `agent_session:` 义务；
  `waitLedger` / `waitTargetObligations` 改用 `BuildWaitLedgerWith`；
  actor 工厂 `loopConfig.AgentSessionObligations` 注入判读器。
- `handler.go`：Web 回合的内联 loop config（流式/非流式两处）同样注入判读器——Web 回合
  不经过 actor，只接 actor 会让该路径的义务永远无法结清。

**验证**：新增 3 条用例（真实 Spawn 挂起 + 去重、终态结算、wait 账本含 agent_session 行）
`go test ./internal/api/runtimeapi/ -run "TestAPISpawnParksQueuedChildObligation|TestAPIAgentSessionObligationSettlesAfterTerminalProjection|TestAPIWaitLedgerIncludesAgentSessionObligation" -count=1` ✅；
`go build ./...`、`go vet ./internal/api/runtimeapi/`、gofmt ✅。

**边界**：① API 宿主暂不播报宿主侧 `turn.suspended` 边沿（挂起状态仍经 actor 收尾回写
`SuspendedTurnID` 且 wait 账本可见；如需 UI 边沿需加 Handler 级去重状态）；② 直连
`/api/agent/chat` 回合不持久化 RuntimeState，`waitLedger` 以 `SuspendedTurnID` 定位记录，
该路径下账本看不到挂起（批次义务同样如此，属既有不对称）；但 park/settle 都用 ctx 的
turn id，语义正确。

### 7.15 账本 ctx 兜底 + 只读派生收窄 + wait_agent 描述（2026-09-30，第三批）

1. **直连路径 wait 账本可见性（修 §7.14 边界②）**：`waitLedger`
   （runtimeapi/session_runtime_support.go）在 `SuspendedTurnID` 为空时，回退到调用 ctx 上
   的 turn 注解（`agent.TurnIDFromContext`）——与 `parkAgentChildObligation` 的 turn id
   解析同源。直连 `/api/agent/chat` 回合不落 RuntimeState，此前该路径的轻量子会话/批次
   义务在 wait_agent 里永远不可见。回归测试：`TestAPIWaitLedgerFallsBackToContextTurnID`。
2. **只读派生只能收窄（边界加固）**：CLI 与 API 的 `applyLocalChildReadOnlyPolicy` 此前
   `SetCapabilityScope(ReadOnlyChildCapabilities())` **直接替换**继承面，父策略没有
   network/agent_management 时会被 read_only 派生静默放宽。新增
   `ToolExecutionPolicy.IntersectAllowedCapabilities`（父面无 scope 时原样返回，有则取交集），
   两处宿主改为交集落座。测试：`TestIntersectAllowedCapabilitiesNeverWidensParentScope`、
   `TestApplyLocalChildReadOnlyPolicyNarrowsToParentCapabilities`。
3. **wait_agent 工具描述**：两处 schema 描述补上 "obligations[] covers both batch tasks and
   lightweight child sessions (agent_session rows): a non-terminal row keeps the result
   pending (never a finalize verdict) and terminal rows join the baseline."，让模型在调用前
   就知道轻量子会话义务与 I1 语义。

**验证**：`go test ./internal/policy/ ./internal/toolbroker/ ./internal/api/runtimeapi/`
（定向）+ `cmd/aicli/commands` 定向全绿；gofmt/vet 干净。

**provider 上下文口径（核查结论，未改代码）**：loop 侧
（`resolvePromptPreflightBudget`：`contextWindow` 优先 `ModelCapabilityMaxContextTokens`）与
CLI 侧（`resolveChatStatusContextWindowTokens` / token 用量：capability → provider limit →
active turn）**同序**，且 CLI 会把 `session.Provider.ModelCapabilities` 传播进 loop 的
runtime provider 配置（chat_actor_host.go:3031、chat_core.go:702），两侧读同一份能力数据，
未发现功能分裂。唯一差异是**能力缺失时的兜底**：CLI 用 `sharedChatDefaultContextWindowTokens`
（256K）而 loop 用 provider caps（如 128K）——只影响展示/active-turn 提示，不影响 loop 的
真实 prompt 预算；若要统一，需先确认哪个面出现过 128K 与 1M 并列的现场。

### 7.16 三端 UI 接线审计 + micro web client 挂起横幅（2026-09-30，第四批）

**审计问题**：subagent 执行完成后，是否会唤醒主 agent 并更新 UI（aicli TUI / frontend /
micro web client）。

**结论（逐端取证）**

- **唤醒（两宿主都有）**：CLI `chat_actor_host.go`（投递成功后 `publishLocalTurnResumed`）
  与 API `supervision_batch_projector.go`（投递成功后 `publishTurnResumed`）都会把 wake 投递成
  一次 resume episode 并播报 `turn.resumed`；测试 `TestLocalTurnResumedEventPayload`、
  `supervision_batch_projector_test.go`。
- **aicli TUI：已接线**。`chat_runtime_events.go` 把 `turn.suspended` / `turn.resumed` 渲染成
  `[subagents]` 时间线行（挂起："等待 N 个义务"；恢复：trigger/pending/终态），测试
  `chat_turn_events_render_test.go`；恢复 run 的流式输出走正常渲染管线——但同 turn 复用的
  退役门禁缺口见 §7.17（已修）。
- **frontend：已接线**。`frontend/src/lib/parked-turn/`（挂起快照与迟到通知分离的归约器）、
  `use-parked-turns`、`session-mode-banner`、`stopResumedTurn`（`use-workspace-live.ts`）齐备，
  事件契约（`event-contract.ts`）含两事件，vitest 覆盖。
- **micro web client：此前零处理**（`backend/cmd/aicli/commands/web/` 下 grep
  `suspended|resumed|parked` 无任何匹配）。SSE 端不过滤（`chatWebSSEEventName` 对未映射事件
  返回 `(rawEvent,false)`，按原名透传），因此事件本就到达客户端，只是没有消费者。

**改动（micro web client，最小可用面）**

- 新增 `web/js/parked.js`：`turn.suspended` → 常驻横幅（义务数/batch 身份）；`turn.resumed`
  → 恢复通知（trigger/pending/terminal，8s 自动隐藏，且不被恢复 run 的 `turn_start` 清掉）；
  `turn_start` 清挂起态；`session_switched` 清空；元素缺失静默降级。
- `web/js/sse.js` 接入分发（与 `handleTodoSSEEvent` 同层）；`web/index.html` 增加
  `#parked-banner`（sticky 在消息区顶部）；`web/style.css` 增加 `.parked-banner` 双主题样式
  （只用两个主题块都已定义的 token，满足 menu verify 的 CSS 不变量）。
- 回归：Go 资产测试 `web_parked_turn_asset_test.go`（go:embed 收录 + sse.js 装配链 +
  index/style 元素与样式）；`scripts/verify-micro-web-parked-turn.mjs`（10 项断言，纯逻辑迷你 DOM）。

**边界**：micro 客户端没有挂起快照端点，横幅是纯事件驱动；页面刷新/断线重连若错过边沿则
不显示（不伪造状态）——如需刷新后仍可见，需要为 `/web/api/*` 增加挂起快照字段（另议）。

### 7.17 aicli TUI 托管恢复输出被退役门禁吞掉（2026-09-30，第五批）

**根因**：§6.12 挂起 turn 的 run 收尾时 `EndRun` 会 `retireTurnLocked(activeTurnID)`；而
wake 恢复是「同一 turn 的新 episode」（复用挂起 turn_id，见
`internal/chat TestSubmitPromptOnSuspendedTurnResumesSameTurnID`）。
`shouldSuppressMismatchedPrimaryTurnEvent` 的 `retiredTurnIDs` 分支先于 `activeTurnID` 比对，
于是恢复 episode 的 `llm.request.started` / 工具行 / `assistant_message` 全被丢弃——模型在跑，
终端只显示一行「托管恢复」，没有任何内容（用户读成"TUI 没有接入"）。此前唯一的解退休入口是
`maybeAdoptResumedPrimaryTurn`，只由提问回答的 `expectResumedTurnAfterAnswer` 武装。

**修复**：`chatRuntimeEventBridge.Handle` 在 `turn.resumed` 边沿调用
`expectResumedTurnAfterWake(turnID, arm)`：撤销退役标记并清除 `retiredTurnOrder`；
`trigger=settled`（账本全终态闭合、不会再有 run）只解退役、不武装复活，避免开出等不到
`session_end` 的幻影 run。

**验证**：`TestChatRuntimeEventBridge_WakeResumeRevivesRetiredTurn`（恢复后同 turn 事件必须
过门禁并重开 run 上下文）、`TestChatRuntimeEventBridge_SettledResumeDoesNotOpenPhantomRun`
（闭合边沿不复活）；`go test ./cmd/aicli/commands/ -run "TestChatRuntimeEvent|TestChatWebPage|
TestHandleChatWebPage|TestLocalTurnResumed|TestApplyLocalChild"` 全绿。

### 7.18 session_20260929175325_cxHK8mPD 真机审计：子代理 10 连败的三个根因（2026-09-30）

**现场**：该会话 12 个子代理、10 个 `failed`（analytics），单子代理 token 消耗 0.4M–8.4M。
错误原文只有三类：`tool not found: commands|shell_commands`、`policy:capability not allowed by
execution policy: exec_shell`、1 个 `stopped`。逐条取证如下（存储位置：批次控制面
`~/.aicli/sessions/runtime/subagent_batches/session_<id>.sqlite`、监督面
`~/.aicli/sessions/runtime/supervision/supervision.db`）。

**根因 A（已修，工作树未提交）**：内建 general/plan/explore 子代理经 agentdef 派生策略时 role
丢失 → 能力地板只剩 read_only+write_fs → 子代理继承到 shell 工具面后被门禁拒绝
（`chat_child_shell_capability_test.go` 即为此写的回归）。修复在
`chat_actor_host.go`（`DeriveChildForTask(..., agentType, ...)`，08:33 改），09:28 构建已含。
真机验证：修复后的两个子代理 `U0ubRwbv`/`zzSmruDS` 进度事件带 `tool_name=shell` 且
`supervision_execution_runs.status=succeeded`。

**根因 B（本次已修）**：失败分类把这两类**确定性工具面错误**归 `unknown`（无 retry_advice，
父模型只能反复重派新子代理）。修复：`internal/llm/failure_category.go` 增加
`TOOL NOT FOUND` / `NOT ALLOWED BY EXECUTION POLICY` 子串 → `tool_error`；回归
`TestFailureCategoryFromErrorCode_ToolSurfaceFailures`。

**根因 C（未修，契约违背）**：spawn_agent 的 next_action 明确承诺 "ending the turn is safe:
the host parks the turn and **auto-resumes it on the child's terminal event**"，但
`internal/supervision/projection.go:98` 只在 `SeverityCritical && ActionRequired()` 时排 wake；
成功终态只写 `severity=info / resolution=closed` 通知。于是**全部义务成功**的挂起回合永不自动
恢复。真机证据：`turn_ea632a84`（义务 `agent_session:U0ubRwbv` + `agent_session:zzSmruDS`）
两子代理 01:41:08 / 01:51:17 succeeded，`supervision_wake_pending`=0、
`supervision_wake_delivered` 无这两行、无 `turn.resumed`，父会话持续 parked（>10min），
模型却在回合末向用户承诺"完成后自动接续"。建议修复方向：在 `projectLocalAgentCompletion`
（CLI）/ API 投影处，对"终态且属于挂起回合义务"的 completion 也排一次 wake
（notify key 沿用 obligation+seq 去重；WakeReason 用 agent_completed），或宿主侧用
`TurnObligationsSettledWith` 结清后投递 resume。

**附带观察**：义务生命周期与会话生命周期分离——`ZO0aFOKs` 义务在 01:00 被判 failed 后，父会话
仍可 `followup_task` 使用该子会话并在 01:28 完成修复；"失败"只描述那一次 attempt。

### 7.19 §7.18-C 修复（结算 wake）+ 状态栏残影修复 + 过期数据清理（2026-09-30）

**A. 结算 wake（契约违背修复，CLI + API 双宿主）**

- 新增 `supervision.ScheduleSettledTurnWake`（`internal/supervision/settled_turn_wake.go`）：
  在子会话终态投影**没有**排生命周期 wake（非 critical）时，读 durable 挂起账本，
  若该 turn 的义务全部已知终态则排一笔 `obligation_settled` wake（notify key 由
  turn id 派生，重复调用只投一次）；CLI `projectLocalAgentCompletion` 与 API
  `projectAgentCompletion` 在投影后调用它，随后走既有 `wakeSupervisedParent` 排空。
- 新增 `subagentbatch.TurnObligationsAllTerminal`（严格判定）：与 settle 谓词不同，
  **没有生命周期行的子会话视为仍在运行并阻塞**——运行中的 spawn_agent 子代理没有行，
  若按 settle 谓词的"未知跳过"语义，第一个孩子完成就会把 turn 判成结清并提前恢复父会话。
- `supervision.LifecycleWakeScheduled` 抽出投影的唤醒条件，投影与完成桥共用，防止漂移。
- 新增 `ListTurnSuspensions(ctx, sessionID)`（BatchStore 接口 + sqlite 实现）：不依赖
  `RuntimeState.SuspendedTurnID` 派生缓存即可找到挂起 turn。
- 回归：CLI `TestProjectLocalAgentCompletionWakesSettledParkedTurn` /
  `…DoesNotDoubleWakeCriticalSettledTurn`；API `TestAPIProjectAgentCompletionWakesSettledParkedTurn`；
  判定表 `TestTurnObligationsAllTerminal`（缺行/运行中/混合批次/nil 判读器全部阻塞）。

**B. 状态栏残影（"Analyzing (58s)" 永不推进）**

- 根因：`currentSurfaceStateLocked` 把"actor Busy 但本会话没有 run"的托管挂起回退成
  `Waiting` → 动态栏复用运行态文案 "Analyzing"，而 `dynamicStatusCompleted` 冻结了上一段
  run 的耗时（58s），秒表永远不动。
- 修复：新增 `chatSurfaceStatusParked` + `interactiveSessionActorAwaitingObligations`
  （`chat_team_drain.go`）；托管挂起渲染 "◦ Waiting for subagents"（不可中断、无秒表），
  且完成冻结的 "Worked for …" 不得覆盖它（turn 尚未结束）。
- 回归：`TestBuildChatDynamicStatusModelParkedTurnHasNoFrozenClock` + 状态矩阵三表新增
  parked 行（isRunning=false / String / action-role-interrupt）。

**C. 过期数据清理（2026-09-30 10:56 CST，均有备份）**

- 备份：`.tmp/backup/supervision-20260930-105622.db`（1.4MB）、
  `.tmp/backup/agent_control-20260930-105636.sqlite`（592MB，SQLite backup API 一致性快照）。
- 监督库（活跃 lease 与 7 天窗口内数据不动）：`wake_pending` 279→5（删 274 条僵尸 wake）、
  `supervision_lifecycle_notifications` 1226→473（删 753 条死会话过期通知，其中 229 条
  unresolved critical）、`wake_delivered` 保持 82（均在 7 天内）；WAL 已 checkpoint。
- Agent 注册表：以**内置** `PruneAgentWakeEvents`（enforce 模式，7 天 closed 窗口 + 每 agent
  64 条活跃上限）删 261,743 条 closed wake 事件（1,210,293→949,902，46s/28 批）；
  `PurgeTerminalAgentRecords` 30 天窗口无命中。宿主默认仍是 observe，本次为一次性操作。
- 残留（未动，记录在案）：`agent_control.sqlite` freelist 31,102 页 ≈127MB 需停机窗口
  VACUUM 才能回收（auto_vacuum=0）；`session_actor_leases` 266 条过期租约保留（`GetLease`
  会读到它们做接管判定，属诊断语义，不做静默清理）。

### 7.20 重启竞态下的账本复位（revival 收敛，CLI + API 双宿主）（2026-09-30）

**现象**：子代理会话实际在运行（lease 未过期、runtime state=running、turn 在流式输出），
`agent_control_agents` 行却是 `stale`/`closed` —— `/agents`、Web 面板与父会话因此把运行中
的子代理报成已结束（真机：`session_20260930105804_4YgwfIX7`、`…105807_4OT9C2lq`）。

**根因**：sweep 与恢复的竞态是单向的。旧进程退出释放执行 lease 后，新进程的
`sweepStaleLocalAgentRegistry` / `sweepStaleAgentControlAgentRegistry` 按"expired lease +
state 仍 claims progress"把行标 stale；随后重启恢复 / 显式 `resume_agent` 让会话重新持有
lease 并继续运行，但**没有任何路径把行复位**（投影 upsert 明确禁止复活终态行，见
`UpsertAgentControlAgent` 的 SQL 守卫）。

**修复**：

- `agentcontrol.AgentReactivator` + `ReactivateAgentControlAgent`（单行、幂等）：只把
  stale/closed 行移回 `active` 并清空 `closed_at`，守卫在 SQL 内（active 行与并发 close
  都不会被覆盖），成功后补发 `active` wake 事件供面板/父流解释状态回摆。
- CLI：`materializeLocalAgentRegistry` 在 stale sweep 之后跑
  `reviveLiveLocalAgentRegistryRows` —— 同一份 listing、同一个 lease 时钟，方向相反；
  条件为"未过期 lease + state=running/rewinding"（无 lease 的空闲会话不会被复活，崩溃遗留
  的 running 状态仍保持终态），并用 24h 窗口把扫描限制在近期终态行。`Resume` 另外做即时
  复位（best-effort，不因账本写入失败回滚已生效的 resume）。
- API：`sweepStaleAgentControlAgentRegistry` 同样先跑 `reviveLiveAgentControlAgentRegistry`
  （`apiAgentRegistryLeaseLive` + running/rewinding），保持两宿主对同一 registry 文件的收敛
  口径一致（G3/N2 同构要求）。

**测试**：`TestReactivateAgentControlAgentRevivesOnlyTerminalRows`（active/未知 id 不动、
stale→active 清 closed_at、`active` 事件、幂等、closed 同样可复位）；
CLI `TestReviveLiveLocalAgentRegistryRowsRevivesRunningSession` /
`…KeepsDeadSessionTerminal` / `TestResumeReactivatesTerminalAgentRow`；
API `TestAPISweepRevivesLiveAgentRegistryRow` / `TestAPISweepKeepsDeadSessionTerminal`。

**现场修复**：03:37 用新 store 调用把仍在运行却被标 stale 的 `session_20260930105807_4OT9C2lq`
复位为 `active`（closed_at 清空）；复查"live lease 会话 vs 终态行"零不一致。运行中的宿主仍是
旧二进制，下一次重建/重启后该收敛自动生效（stale 与 revival 由同一 pass 双向收敛）。

### 7.21 子代理终态唤醒缺口修复（P0-A/P0-B/P0-C，P1 归并）（2026-09-30）

**现象（真机 W6=`session_20260930105807_4OT9C2lq`）**：子代理 03:57:48 以 success/idle 结束，
完整汇报已写进它自己的事件流（seq 15623/15624），但父会话（`…175325_cxHK8mPD`）事件流最后
一条仍是 03:55:01 的自有 session_end，mailbox 停在 seq 16（10:24 的子代理），无 supervision
通知、无 wake —— 主 agent 永久 idle，只能人工介入（"子代理在汇报前停止"）。

**根因（三层，按确定性排序）**：

1. **完成订阅是进程内存态**：`subscribeLocalAgentCompletion`（CLI）与
   `subscribeAgentCompletion`（API）只在 spawn 时登记，宿主重启后没有重建路径。被 resume
   续跑的子代理结束时，新进程里没有任何监听者，完成投影（mailbox + 通知 + 镜像 + wake）
   全部不产生。
2. **turn-end 自动 drain 默认关闭**（`supervision.turn_end_check`）：即使 wake 已排
   （run 级 stall/timeout），交互式会话没有自然 turn 时也永远不投递；现场两条 wake
   （03:08 `progress_stalled`、03:28 `execution_timed_out`）至今 `claimed_at=NULL`。
3. **run 账本不终态**：`ProjectAgentCompletion` 内部本会收敛子会话的 execution run 与 run
   级告警（`finalizeChildExecutionRuns` → `MarkExecutionRunTerminal` + `ConvergeRunAlerts`），
   但链路 1 断掉后无人触发，run 停在 `queued`、过期 deadline 反复产生 critical 通知。

**修复**：

- **P0-A 订阅重建**：CLI `rebindLocalChildCompletionSubscriptions`（宿主启动 +
  `Resume` 即时重建；`trackChildEventSubscription` 覆盖前先释放旧句柄，避免订阅泄漏）；
  API `recoverAgentChildCompletion` 的订阅部分（按子会话"每进程一次"，会话尚未落盘时
  归还名额、下一次物化重试）。
- **P0-B 启动重放**：CLI `replayLocalChildCompletions`（宿主启动：尾部事件取最近
  `session_end`/`session_interrupted`，48h 窗口，终态之后有新动作的子会话跳过，父侧
  `subagent.completed` 镜像作为"已投影"闸门保证幂等；交互式只补 durable 账本、不 drain，
  与 batch 启动重放同款语义）；API 对等实现挂在物化路径
  （`materializeAgentControlAgentProjections`），幂等判据用 supervision 通知。
- **P0-C turn-end 放行**：CLI `turnEndWakeDrainAllowed` / API `apiTurnEndWakeDrainAllowed`
  —— `turn_end_check` 关闭时，只要该 scope 存在**非 progress 类** pending wake 就允许
  drain（子代理终态/审批/失败/obligation 结算都属于此类）；开关仍只约束 progress 自检，
  wake 预算（MaxAutoWakePerWindow）限流不变。
- **P1 run 终态收敛**：并入 P0-B 路径（补投影即调用
  `ProjectAgentCompletion → finalizeChildExecutionRuns`），无需新增巡检器；缺失的
  completion 一旦被重放，run 与 run 级告警同时收敛。

**测试**：CLI `TestReplayLocalChildCompletionsProjectsMissedTerminal` /
`…SkipsChildThatMovedOn` / `TestRebindLocalChildCompletionSubscriptionsCatchesNextTerminal` /
`TestLocalHostTurnEndCheck_TerminalWakeDrainsWithoutOptIn` /
`…ProgressOnlyWakeStaysPendingWithoutOptIn`；API
`TestRecoverAgentChildCompletionsReplaysAndRebinds` / `TestSupervisionTurnEndDrainOnAPISide`
（原 CLI `TestLocalHostTurnEndCheck_DefaultOffKeepsWakePending` 与 API
`TestSupervisionTurnEndDrainSwitchOnAPISide` 按新契约改写）。

**残留（设计边界，未动）**：成功终态（info/closed）不排 lifecycle wake —— 父会话靠 I1
挂起 turn 的 `obligation_settled` 或下一次自然 turn 的 preflight digest 收取结果；
"父 idle 且未挂起时被成功终态自动唤醒"不在本契约内（如需覆盖，应改 I1 挂起判定，而不是
放宽 wake 排程）。API 侧重放不追加父侧 `subagent.completed` 镜像（mailbox 事件 + digest
承担同一事实）。运行中的宿主仍需重建/重启才能带上本修复。

### 7.22 「宿主重启后永久静默」的第二个根因：render ownership 死锁（2026-09-30 现场复核）

**现场（`aicli-2x.exe resume session_20260929175325_cxHK8mPD`，PID 17756，15:05:52 启动）**：
15:06 / 15:09 / 15:15 三轮**都真实跑完**（LLM 请求、工具回执、`session_end success=true`），
15:15 那轮还产出了完整的 `assistant_message`（"## 进展 …"）；但每一轮的**全部主事件**被
`shouldSuppressMismatchedPrimaryTurnEvent` 丢弃：`debug.log` 满屏
`render suppressed reason="event turn does not match active run"`，连该轮自己的
`session_start` / `session_end` 也在其中。新轮的 `session_end` 同样被丢 → 陈旧 run 上下文
永不闭合，死锁自我延续：模型照常产出回答，页面从此全程无反应（用户读成"回答没有提交到
服务器/无法回复"）。诊断计数：`runtime.render_fence_dropped {"active_mismatch":…,"turn_id":…}`。

**根因**：`observePrimaryRunTurn` 只在 `activeTurnID == ""` 时登记当前轮；宿主重启 /
auto-continuation 遗留的陈旧 `activeTurnID` 会永久霸占所有权，而 `session_start`（actor 侧
权威的"新一轮开始"信号）没有夺回所有权的路径；`maybeAdoptPrimaryRunTurn` 又因
`runActive || adoptedTurnID != ""` 而拒绝重新打开 run。三条守卫叠加 = 无自愈能力。

**修复（chat_runtime_events.go）**：

- `observePrimaryRunTurn`：主会话 `session_start` 携带的 turn id 与当前 `activeTurnID` 不同
  时**移交所有权**（旧轮 `retireTurnLocked` 退役；若旧轮是被收养的 run，收养关系随迁），
  并写 `primary turn ownership transferred` 调试行（锁外写，避免 IO 持锁）。
- `maybeAdoptPrimaryRunTurn`：`!runActive && adoptedTurnID != "" && != turnID` 视为陈旧收养，
  清掉收养记录后由新轮重新打开 run（否则 `runActive=false + adoptedTurnID!=""` 同样永久静默）。

**回归测试**：`TestChatRuntimeEventBridge_StaleActiveTurnYieldsToNewSessionStart` /
`…StaleAdoptedTurnYieldsToNewSessionStart`（钉住"移交后新轮 assistant_message/session_end 必须
进入渲染管线、被顶替轮残余事件仍被挡下、同一轮重复 session_start 幂等"）。

**运维事实（为什么"多次修复"没生效）**：端口 63056 的宿主是 `aicli-2x.exe`，其二进制
mtime=11:02:19 —— **早于当天全部修复**；多次 `resume` 都复用同一个陈旧二进制，源码级修复
从未进入运行进程。修复必须 **重建 + 重启**（`go build -o aicli.exe ./cmd/aicli` 后替换
`aicli-2x.exe` 再 `resume`）。

### 7.23 §7.22 的第二层：wake 恢复 episode 复用被退役 turn_id（2026-09-30 16:33 现场）

**现场（`aicli-5x.exe resume session_20260929175325_cxHK8mPD`，PID 15588，16:26:04 启动）**：
16:33:13 wake_consumer 投递 settled wake（`supervision_wake_delivered` 有记录），16:33:13.6
起跑 `turn_3bf8448a`，16:34:22 产出完整的 `assistant_message`（"🏁 Phase 2 收口完成"）与
`session_end success=true`；但整轮事件（session_start/assistant_message/session_end 及全部
中间事件）再次被 `render suppressed reason="event turn does not match active run"` 丢弃 ——
页面只有 child completion + `[Child lifecycle preflight]`，用户读成"没有拉起主 agent"。

**根因（比 §7.22 更深一层，三条叠加）**：

1. wake 恢复是「同一 turn 的新 episode」：`turn_3bf8448a` 第一 episode 16:26:26→16:27:13
   已被 `EndRun` **退役**，16:33 的 settled wake 复用同一 turn_id。
2. `EndRun`（chat_runtime_events.go:1126-1136）退役 turn、清 `runActive`，但**不清**
   `activeTurnID`/`adoptedTurnID`；于是 `maybeAdoptPrimaryRunTurn` 的等值早退与
   `maybeAdoptResumedPrimaryTurn`（要求 `adoptedTurnID==""`）双双失效。
3. settled 边沿的 `turn.resumed` payload **不含 turn_id**（只有 `pending_count` /
   `trigger=terminal` / `wake_ids`…），`expectResumedTurnAfterWake` 拿到空 id 直接返回，
   退役标记无从撤销。

结果：`retiredTurnIDs` 分支先于 `activeTurnID` 比对执行 → 整段 episode 被丢；新 episode 的
`session_end` 同样被丢 → 陈旧上下文永不闭合（与 §7.22 同一条死锁，只是触发面更窄）。

**修复**：

- 新增 `adoptPrimaryRunTurnLocked`（撤销退役 + 作废陈旧收养记录 + 以该轮开启新 run epoch）
  与 `unretireTurnLocked`；`maybeAdoptPrimaryRunTurn` 在 `!runActive` 时统一走该路径。
- `observePrimaryRunTurn` 在活动 run 的三条路径上撤销陈旧退役（含所有权移交路径）。
- **保持 ambient 语义**：`!runActive` 时 `observePrimaryRunTurn` 仍不接管转录，由
  `updateComposerAgentStageForAmbientPrimaryRunEvent` 只投状态
  （`TestChatRuntimeEvents_ProjectsBackgroundPrimaryRunAsNotReady` 钉住）。

**回归**：`TestChatRuntimeEventBridge_ResumedEpisodeRevivesRetiredTurn`（复刻 16:33 现场：
退役 + 同 id 复用 + 陈旧收养 → 必须重开 run、撤销退役、assistant_message/session_end 可渲染）。

**残留**：settled `turn.resumed` 不带 turn_id（本次修复不再依赖它；建议后续把 turn_id 补进
该 payload，让恢复边沿自身可自证）。
