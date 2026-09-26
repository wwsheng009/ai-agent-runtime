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

**剩余（诚实记录）**：① 账本行回退的 **API 宿主**（`sessionAgentController.snapshot`）与 CLI 对称实现，
但只做了编译 + 全包回归，未加独立单测；② `wait_agent(task_id)` 返回的 `obligations[]` 在
"无挂起记录"场景下仍缺省（模型只能从 agent 投影取状态）——按需要可再补账本直出。

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

**新遗留（诚实记录）**：① 批次在**派发前**失败（如 single-writer 策略拒绝）时，`wait_agent(task_id)`
返回硬错误 `TOOL_BROKER_FAILURE`（"batch task … is failed but has no child session bound yet; retry shortly"），
应改为可读的缺失/失败观测并把父代理指向批次失败原因，而不是让它"稍后重试"；② `wait_agent` 对
read_only 子代理的执行面限制（管道/命令替换一律拒绝）属策略设计，但**派发建议**上父代理应默认给
"要跑命令"的子代理 `read_only=false`（本轮 pre-dispatch 失败即因两个任务都成了 writer）。

### 7.11 架构问答：长任务截止（10min 估计 / 5min 必须结束）+ 主代理巡检节奏（2026-09-26 真机）

**三套截止机制（可叠加，按确定性排序）**

| 机制 | 参数/入口 | 语义 | 强制者 | 真机证据 |
| - | - | - | - | - |
| 批次墙钟 | `spawn_subagents(wait_timeout_sec=300)` | 整批 deadline，超时任务记 `timed_out`、批次 `BatchTimedOut` | 批次协调器（与宿主模式无关） | schema 原文 "Optional batch deadline (seconds) for background batches"；`loop.go:6793-6809` → `BatchDeadline`；`TestStartBackgroundDeadlineMarksTaskTimedOut` |
| 每任务执行截止 | `agents[i].timeout=300` | 子代理执行上下文 deadline（"Time budget: N seconds." 写进子代理提示词） | 子代理执行器（context deadline） | 实测 `timeout=120`：子代理 ~108s 后以 `failed_with_result` + `context deadline exceeded` 终止，父代理等待提前返回 |
| 显式取消 | `close_agent(child_session_id)` / `subagent_control(cancel)` | 立即停止 | 父代理/宿主 | 实测 `closed_count=1`（child → `status=stopped`） |

**巡检/等待节奏（默认值 = 你本机生效值，`C:\Users\vince\.aicli\config.yaml` 未覆盖任何相关键）**

- `wait_agent` 窗口：默认 **30s**、最小 **10s**、**上限 2min**（`WaitTimeoutMode=clamp`，超限被钳制并回显 `wait_timeout_clamped`）。窗口结束即一次巡检机会；窗口内 turn 被同步工具调用占住，不能做别的事。
- 等待预算：`maxConsecutiveWaitWithoutProgress=2` —— 连续 2 个无 `terminal_delta` 的等待段后宿主**不再开新窗口**，返回 `next_action=suspend`（`wait_budget_exhausted=true`）；I1 把提前收尾转成 turn 挂起（零 goroutine/零 token）。
- 事件驱动：子代理 ready 时等待立即返回（实测 2786ms / 3691ms，而非等满窗口）；steer/ESC 立即打断；子代理终态经 wake 在 turn 边界 resume（`resume_queue` 可见）。
- 宿主后台（模型不可见）：等待循环 500ms 轮询；执行监督者 **5s** 扫描（deadline/stall/approval）；批次协调器 1min 心跳；周期巡检 `progress_check_interval` **默认关闭**（开启下限 30s），且父会话忙时跳过；每个父 turn 开头必有一次 preflight digest（always-on）。
- 真实限制：监督 wake **不会打断正在进行的 wait**，只在当前等待段返回后投递 ⇒ "截止触发"到"父代理处理"最坏晚一个窗口（默认 ≤2min）。

**结论**：① "5 分钟必须结束"满足（声明式优先，真机验证）；② LLM 可自主处理（声明式 + 每 ≤2min 回归控制权 + cancel 工具），但它没有"定时唤醒"，且子代理内部工具超时（实测 2min）会先于外层截止终止任务 ⇒ 长命令必须显式给子代理 `timeout_ms` 或分片；③ 巡检间隔 = min(等待窗口, 默认 2min 上限)，要更细就调小 `agents.maxWaitTimeoutMs`（如 30s）或让模型用 `timeout_ms: 30000`。
**宿主差异（需注意）**：执行监督者默认模式 CLI=**observe**（`AICLI_EXECUTION_SUPERVISOR_MODE=enforce` 可开）、API=**enforce**；observe 只记录决策与通知，enforce 才发 interrupt/cancel-grace。
**证据**：`artifacts/remote-debug-51875/arch-invoke*、arch3-invoke*、arch4-invoke*`（含 waited_ms 时序与子代理终止原文）。
