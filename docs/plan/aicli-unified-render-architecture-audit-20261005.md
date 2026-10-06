# aicli 统一渲染器架构体检报告（2026-10-05）

> 性质：只读架构审计（写端清单 / 状态与队列地图 / 性能热点）+ 结构判定 + 收敛方案（P0–P3）。
> 审计基线：`main` @ `a75d1c89`（含当日两处渲染修复：`b19284db` 实时事件单车道、`a75d1c89` active 归档对齐）。
> 关联文档：`docs/plan/aicli-chat-unified-render-stall-analysis-and-hardening.md`（统一渲染卡住分析与加固）、
> `docs/plan/aicli-tui-owned-render-simplification-plan.md`（owned render 母计划）、
> `docs/plan/aicli-tui-owned-render-simplification-implementation-guide.md`。
> `docs/architecture/aicli-tui-renderer-architecture-design.md`（目标形态 v2；§7.5 差距扫描）与
> `docs/plan/aicli-render-gap-closure-plan-20261006.md`（G1–G12 收敛执行：批次 A–D）。
> 证据口径：全部结论附 `file:line`；行号为审计快照行号。审计由 3 个只读子代理台账（writers / statemap / perf）
> 与主审抽查合并而成；抽查与独立审查结果见 §7。
> 审查状态：已完成两轮独立只读审查（fact-check），修订记录见 §7.2；修订后核心结论不变。

## 0. 结论摘要

**判定：是，统一渲染器已超出「简单 TUI」的复杂度预算。** 这不是"还有 bug 没修完"，而是三条结构性失控叠加：

1. **多写端未归零**：主内容已收敛到 `TerminalSession`（gateway 唯一物理写），但 `inputbox_editor` 的
   bracketed-paste/secret/submit echo、`TerminalTitleWriter`（OSC）、`TerminalBellWriter`、`status.go` 的
   fallback 打印、以及 **stderr** 仍可未被栅栏地直写 `os.Stdout`；ScreenModel 不知道这些字节，模型与物理屏漂移。
2. **同一事实 14 处独立记录**：run/turn 身份、layout generation、几何、历史交付进度、scrollback epoch、lease、
   帧号、行内容镜像等各有 2–5 份副本。任何修复只对齐其中一份，其余镜像漂移 → "修一处、坏一处"。
3. **把不可回读的 native scrollback 当作分布式事务做**：6 态 `HistoryCommit` ledger + ack/fail/defer/inflight +
   replay/settle/reconcile + backoff/守卫。不变量散落在 planner/queue/executor/session/presenter 五个包；
   在 14 处镜像 + 5 包分散不变量下，只修一条路径时其余副本漂移风险很高（当日两次修复即为例证：
   active 归档 vs finalized 溢出）。

**性能判定：每次 FlushTransaction 的代价与"实际变化量"脱钩。** 结构上至少 3 段 goroutine 交接
（bridge worker → UI actor → executor，gateway run 另计）、串行锁区 transactionMu → s.mu → terminalWriteMu、
每次 FlushTransaction ≥2 次全屏级克隆（`:743/:907`）；历史规划 O(cells)（代码注释实测 723ms/op @ 2000-cell
transcript）；active 流式按 30FPS 上限做全量重解析
markdown+chroma（source 变化即 miss，精确 miss 率需实测）；写历史/边界字节即触发全屏强制重绘。

**收敛目标（一句话）**：事件 → 单一有序队列 → 单一 reducer（AppState）→ 单一 frame producer → 单一 writer；
native scrollback 只做 append-only 单向交付，不做事务回执。实施分 P0（写端归一）/P1（状态收敛）/
P2（历史线性化）/P3（性能），各自可独立验收。

## 1. 规模与量化体检

### 1.1 关键文件规模（非空行数，审计快照实测）

| 文件 | 行数 | 说明 |
| --- | ---: | --- |
| `backend/cmd/aicli/commands/chat_runtime_events.go` | 10,418 | 事件桥：分类/backlog/队列/worker/EndRun/编码接入 |
| `backend/cmd/aicli/ui/fixed_bottom_surface.go` | 4,949 | legacy 兼容层（生产已栅栏，仍持大量状态） |
| `backend/cmd/aicli/ui/render/encoding/encoder.go` | 3,131 | EventEncoder → Scene/RenderModel |
| `backend/cmd/aicli/ui/terminal_session.go` | 1,929 | 唯一物理写会话：事务/历史/scrollback/reset |
| `backend/cmd/aicli/ui/history_effect_planner.go` | 1,596 | 历史 screening/铸 commit/续跑游标 |
| `backend/cmd/aicli/ui/controller_state.go` | 1,308 | reducer/AppState 派生 |
| `backend/cmd/aicli/ui/controller.go` | 1,157 | UI actor：mailbox/coalesce/deferred/followup |
| `backend/cmd/aicli/ui/terminal_session_executor.go` | 1,145 | 物理 worker：claim/ack/recovery/backoff |
| `backend/cmd/aicli/ui/history_effect_queue.go` | 829 | 历史效应队列/memo/epoch |
| `backend/cmd/aicli/ui/history_commit.go` | 751 | 6 态 token ledger |
| `backend/cmd/aicli/ui/app_screen_layout.go` | 774 | transcript/band/bottom 合成布局 |

> 口径注：表中为**非空行数**；物理总行数更高（审查复核：`chat_runtime_events.go` 10,946、
> `fixed_bottom_surface.go` 5,229、`render/encoding/encoder.go` 3,308、`terminal_session.go` 2,035、
> `history_effect_planner.go` 1,657、`controller.go` 1,223）。§4 的行号引用以物理总行为准。

### 1.2 全仓量化（`ui` + `commands`，生产文件）

- 生产 `.go` 文件 **553** 个；goroutine spawn 点 **~50**；`sync.Mutex/RWMutex/WaitGroup/Once` 出现 **237** 处
  （主审脚本口径，审查轮未复算）。
- 测试文件 **708** 个、`func Test` **5,296** 个（主审脚本口径）。测试规模已远超"简单 TUI"的量级，且多数是
  对当前行为的固化断言，使架构性变更的成本与回归风险持续上升。

### 1.3 流水线规模（runtime event → 终端字节）

以 `docs/plan/aicli-chat-unified-render-stall-analysis-and-hardening.md` §2 为主干，叠加 statemap 台账：

```text
runtime 事件流
 → chatRuntimeEventBridge.Handle（分类）              P0
 → 单车道 backlog（512/2MiB + 合并/驱逐）             P1
 → 有界 eventQueue（576）→ 消费 worker                P2
 → EndRun 有界排水                                    P3
 → UIController mailbox（256，coalesce/deferred/followup） P4
 → reducer/AppState                                   P5
 → FlushEffect / HistoryCommitWakeEffect              P6
 → TerminalSessionPresenter（几何发布 + Request）      P6.5
 → TerminalSessionExecutor（claim/恢复/backoff）       P7
 → history planner / async plan worker（250ms 预算）   P8
 → TerminalSession.FlushTransaction（prepare→写）      P9
 → Presenter + 全局 terminalWriteMu + DEC2026          P10
 → RenderOutputGateway（serial + journal + mirror）    P11
 → FramePump 调度                                      P12
 → ScreenModel 前后双缓冲                              P13
```

汇总计数（statemap 口径）：

| 项 | 数量 | 备注 |
| --- | ---: | --- |
| 队列/缓冲结构 | **28** | 其中 8 个是带背压/丢弃策略的 transport queue：bridge eventQueue(576)、backlog(512/2MiB)、UI mailbox(256)、planWorker req(1)、FramePump jobs、gateway mirror（**生产队列 64**；兜底值 1024 在 gateway 构造下不可达）、eventHub 订阅缓冲、gateway primary serial |
| 状态机 | **~20**（核心 12） | run 生命周期、事件 epoch、UI action、AppState、ActiveCellPhase(3)、HistoryCommit(6)、history queue 恢复/plan、plan seq 栅栏、executor worker/backoff、TerminalSession 投影/epoch、ScreenModel projection、gateway lifecycle；辅助 8 |
| 锁/条件变量 | **~30** | bridge 14 + controller 2 + TerminalSession 2 + Presenter/terminalWrite 2 + executor 3 + gateway ≥6 + renderengine 4 |
| 常驻 goroutine | **9 类** | bridge run、bridge backlog worker、UI actor Run、plan worker、TerminalSessionExecutor、HistoryCommitExecutor、FramePump、gateway run、每 mirror worker |
| 缓冲 | **22** | front/back 双缓冲、render cache、soft tail、paint trace ring、diag ring、tail rows/cells、prepared history、journal ring、event log 等 |

> 计数口径：statemap 台账 + 主审抽查；审查轮复核了 4 个关键队列常量（见 §7.2）。

### 1.4 稳态单 delta 成本链（perf 台账 §0）

```text
runtime event → bridge.Handle（renderMu 合并 pendingStreams）
  → bridge worker: TryPost + 有界重试轮询            （chat_runtime_events.go:2898-2980）
  → UI mailbox（UpdateActiveCellAction 按 CellID coalesce）（action.go:699）
  → actor Run（批 ≤64 action）→ reduce                （controller.go:169/530-648）
  → 每批一个 FlushEffect → presenter → executor.Request（terminal_session_presenter.go:86-101）
  → waitControllerIdle（1ms 轮询，上限 2s）            （terminal_session_executor.go:795-804/847）
  → terminalSessionSnapshot（锁内克隆 + claim 深拷贝）  （terminal_session_snapshot.go:81-155）
  → composeTerminalViewportTransactionPlan（全屏布局）  （terminal_session.go:126-166）
  → FlushTransaction（plan 再克隆 → 全屏物化 → 全屏 diff → CUP 重写）
  → writeTerminalBytesKindLocked（transactionMu + s.mu + 全局 terminalWriteMu）→ gateway
```

**即使只变 1 行：结构上至少 3 段 goroutine 交接、3 层串行锁（transactionMu → s.mu → terminalWriteMu）、
≥2 次全屏级克隆（`:743/:907`）。**（上述为代码结构推导；跨进程跳转次数与锁段数的墙钟影响未实测。）

## 2. 写端清单与「统一输出管理」判定

### 2.1 生产 interactive 的权威链（判定基准）

- 装配：`commands/chat_setup.go:76-84` 建 surface 后立即 `SetPhysicalWritesEnabled(false)`；
  unified presenter：`chat_setup.go:231` → `chat_ui_actor.go:108/185/195-239` →
  `chat_interaction.go:676-720`（`:715 FencePhysicalWrites()` 单向锁死 + `:716` lease transport + `:718` Request）。
- 结论：`FixedBottomSurface` 的物理写（含内部全部渲染、lease legacy 分支）**在生产已死**；
  唯一合法物理写 = `TerminalSession`（经 RenderOutputPort/gateway，`terminal_session.go:867/1846/1854/1890`）。

### 2.2 未被栅栏、生产仍可达的写端（本次核查重点命中）

| 写端 | 证据 | 生产存活 |
| --- | --- | --- |
| `inputbox_editor.go` bracketed-paste / focus-change 序列 | `:264/:267`，`ReadWithHistoryPromptWithHooksContext` 固定传 `os.Stdout`（`:270`）；链 `chat_input_queue.go:1205→1220 → chat_composer.go:96` | **LIVE：每次交互式读取都执行** |
| `writeEditorText` 未 claim 分支（transient/modal/agent-panel composer 空 hooks） | `:439-451`；`chatTransientLineComposer` `chat_composer.go:865-870` → `chat_input_queue.go:1379`；`chatModalComposerPrompt` `:630-635` → `:1416`；`chatAgentPanelComposer` `:914-921` → `chat_debug.go:1791` | **部分 LIVE**（主 composer/busy/merged 已被吞） |
| `ReadTransientSecretPrompt` 直写 | `inputbox_editor.go:151/157` ← `chat_composer.go:877-889` ← `chat_input_queue.go:1466` | **LIVE** |
| `TerminalTitleWriter`（OSC 0） | `terminal_title.go:52/74`；构造 `chat_notification.go:602-613`（`chat_setup.go:702`）；动画 tick 循环 `chat_notification.go:263-304` 反复 `Set`（`:274`） | **LIVE（运行期频繁）** |
| `TerminalBellWriter`（`\a`） | `terminal_bell.go:27`；构造 `chat_notification_sound.go:160-170`（`chat_setup.go:703`） | **LIVE（偶发）** |
| `status.go` `Print*` 家族 | `:124/127/:180-197`；调用点 `chat.go:1811/1939/1957/2032`、`chat_export_command.go:441/495`、`chat_model_switch.go:423/766/781` 等（多为 legacy/no-popup/异常兜底） | **边缘可达：一旦触发即无栅栏直写** |
| **stderr 通道** | `chat_http_debug.go:25/29`、`chat_bootstrap.go:76/196/204/219`、`chat_actor_host.go:3137/3256/3312/3332/3352`、`chat_cache_local.go:276`、`chat_fast_command.go:132`、`ui/history_trace.go:123` 等 | **LIVE（无级栅栏）**；stderr 与 stdout 同 tty，可覆盖底部保留区 |

### 2.3 无栅栏但调用链已断的 legacy 直写（清理对象）

`progress.go:136/193/300/314/356/362`、`layout.go:317/333/362/409/418/426/434/482`、
`message.go:197/217`、`output.go:251/257`、`info.go:168`、`input.go:134/228`、
`statusbar.go:218/247/270`、`separator.go:105/110`、`theme.go:193/198`、`inputbox.go` layout-disabled 分支。
其调用链证据显示生产不可达（如 `NewProgress` 无调用点；`Layout.Render/PrintMessage` 无非测试调用者），
**但均无栅栏，任何新调用者都会绕开唯一写端**。

### 2.4 判定

**统一输出管理不完整**：主内容通道唯一，但存在 6 类未被模型感知的字节出口（编辑器序列、secret、
transient 面板 echo、标题、铃、stderr），且全仓无"仅允许 render/output 出口"的强制门禁。
这类字节不进 ScreenModel → 模型漂移 → 表现为"中部插入 / 被覆盖 / 顺序异常"的不可解释缺陷。

## 3. 状态与队列地图（「太多状态控制」的实锤）

### 3.1 阶段 → 载体 → 队列/缓冲 → 不变量（紧凑表）

| 阶段 | 载体 | 队列/缓冲 | 核心不变量 |
| --- | --- | --- | --- |
| P0 分类 | `chatRuntimeEventBridge`（`chat_runtime_events.go:42-278,1387`） | 无 | 外来会话内容不进父数据面（`:2997`） |
| P1 单车道 backlog | `backlog []*chatRuntimeQueuedEvent + backlogIndex`（`:62-91,1527,1573,1606`） | 512 条 / 2MiB；同流尾槽合并、latest-wins、ordered 溢出计数 | 跨家族严格 FIFO；有积压不得绕过（`:2380/:2551`） |
| P2 有界 eventQueue | `eventQueue chan`（`:627`） | 576；非流 200ms 预算；critical 保留 64 席 | 单 worker 按序消费；stale epoch 拒绝（`:2777-2783`） |
| P4 UI actor | `UIController`（`controller.go:227-277`） | mailbox 256 + followups + coalesce map | 单 Run goroutine；批 ≤64；followup 先于外部 mailbox（`controller.go:530-648`） |
| P5 reducer | `AppState`（`app_state.go:18-44`） | COW 复用（`app_state.go:120-182`） | reducer 是 AppState 唯一写者 |
| P7 planner | `planEligibleHistoryCommits*`（`history_effect_planner.go:35,956,1423,1532`） | memo 指纹（`history_effect_queue.go:114-182`） | ledger 只持"最老有效前缀" |
| P8 plan worker | `asyncTranscriptPlanWorker`（`controller_plan_worker.go:20-26`） | req chan 容量 1，覆盖语义 | seq / planInputsEpoch / 指纹三重栅栏 |
| P9 ledger | `HistoryCommitLedger`（`history_commit.go:192-231`） | byToken/byRange/bySource/activeTokensByCell/… | token 单调；range/source 身份不得二次铸造；6 态 |
| P10 executor | `TerminalSessionExecutor`（`terminal_session_executor.go:182-271`） | 无队列；schedule 标量快照（`terminal_session_snapshot.go:43-75`） | claim-miss 必须显式释放；有界等待 2s |
| P12 session 状态 | `TerminalSession`（`terminal_session.go:315-384`） | `historyTailRows / historyStreamTailRows / historyTailCells / preparedHistory` 等 | 单一 writer；prepare→write 顺序 |
| P13 presenter | `renderengine.Presenter`（`presenter.go:19-25`） | 一帧一次 Write | 非重入；短写=ErrShortWrite |
| P14 gateway | `RenderOutputGateway`（`output/gateway.go:333-406`） | primary serial + journal ring + 每 mirror chan(1024) | 一次只有一个 primary 提交 |
| P15 帧调度 | `FramePump`（`frame_pump.go:60-76`）/ `FrameClock` | jobs map 按 key 单挂；60FPS / 30FPS | 唯一 scheduler；key 替换即合并 |
| P16 双缓冲 | `ScreenModel`（`screen_model.go:32-38`） | front/back 两个 W×H 网格 | 未确认 front 绝不作差分依据 |
| P17 编码 | `EventEncoder`（`encoder.go:99-121`） | assistant/tool/streamOrder maps；pending 上限 128/1MB | 流序单点编号 |
| P19 lease/replay | `ScreenLease`（`screen_lease.go:27-92`）、replay 授权（`history_effect_queue.go:32`） | lease 等待预算 | replay 仅在有授权时替换 scrollback |

### 3.2 「同一事实被多处独立记录」清单（14 项）

1. **run/turn 身份**：`runEpoch/runState/activeTurnID/lastClosedTurnID/adoptedTurnID/executorTurnID`
   （`chat_runtime_events.go:136-165`）↔ queued `.epoch`（`:283`）↔ `HistoryEffects.TerminalEpoch`
   （`history_effect_queue.go:22`）↔ `TerminalSession.terminalEpoch`（`terminal_session.go:368`）。
2. **layout generation**：`AppState.LayoutGeneration` ↔ `frame.LayoutGeneration` ↔ `TerminalSession.generation`
   ↔ `HistoryCommit.LayoutGeneration` ↔ `executor.lastResetGeneration`（5 处）。
3. **几何**：`AppState.Geometry` ↔ `TerminalSession.geometry` ↔ `TerminalSessionPresenter.lastWidth/lastHeight`。
4. **历史交付进度**：ledger 状态 ↔ claim 拒绝计数 ↔ `historyTailRows/historyTailCells` ↔ executor diag（4 处）。
5. **scrollback epoch**：`HistoryEffects.TerminalEpoch` ↔ `ProvenScrollbackEpoch` ↔
   `TerminalSession.terminalEpoch/scrollbackResetCount`。
6. **副屏租约**：`AppState.Lease` ↔ `TerminalSession.lease` ↔ `alternateLeaseID` ↔ `ScreenLease.ID`（4 处）。
7. **帧号**：`TerminalSession.frame` ↔ `Presenter.flushes` ↔ `PaintTrace.frames` ↔ `gateway.sequence`（4 处）。
8. **流序号/合并区间**：事件 payload sequence/coalesced_from ↔ `encoder streamOrder.nextSeq` ↔ RenderModel `item.Seq`。
9. **assistant 去重**：`renderedAssistantDeltaContent/digest/length` ↔ encoder assistantSnapshotBy ↔
   `ActiveCellState.Stable/Enqueued/Acked`。
10. **行内容镜像**：`historyTailRows` ↔ `historyStreamTailRows` ↔ `ScreenModel.front/back` ↔
    `SoftOutputState.lines` ↔ `PaintTrace` hash（5 处；代码注释（`terminal_session.go:345-349`）称 streamTail
    是已归档行的去重证明）。
11. **plan 完整性**：`PlanIncomplete/PlanStalled/planResume*` ↔ `executor schedule.planIncomplete`。
12. **空闲判定**：controller `inFlight/delivering/queue/followups/planInFlight` ↔ executor `waitControllerIdle` 轮询。
13. **backoff 进度**：`lastResetAt/Epoch/Generation/Failed/SuccessRetries` ↔ `schedule.stateGeneration` ↔ `LayoutGeneration`。
14. **降级/预算遥测**：bridge degradation atomic 与 turnBudget atomic 是同一事实的两份无锁镜像。

**判定**：本轮与上一轮"修 A 漏 B"的两次现场缺陷（resident-tail 双写、active 归档对齐）都直接落在
第 4/10 项镜像上——修一处、别处未知。这是"反复修复出新的 bug"的第一机制性原因。

## 4. 性能热点 TOP（「又卡又慢」的结构原因）

> 复杂度口径：稳态流式（每秒数十 delta、transcript 数千 cells），`terminal_session_executor.go:1046`
> 注释记录了真实会话规模：**6,622 cells / 291,842 rows**。

| # | 热点 | 证据 | 复杂度/影响 |
| --- | --- | --- | --- |
| 1 | 全量深克隆 | `terminal_session.go:743`（`plan.Clone()`；现有契约下 plan 传入后不再变更，属防御性开销）、`:907`（`ScreenModel.Clone` 拷贝 front+back 两网格）、`:196-205/:209/:1972`；payload 到终端前被深拷贝 3~4 次（`terminal_session_snapshot.go:136-151` → `terminal_session.go:144-148` → `:743` → `:1225`） | 每帧 `O(W×H + rows×spans)` 分配；GC 压力与帧延迟同变化量脱钩 |
| 2 | 全屏物化 | `terminal_session.go:1199-1207`（先整屏 `terminalFrameCells` 再切 `rows[area.Top-1:]`）、`:1992-2025`（每行 plain 校验 + style 解析 + `vt.NewScreen(width,2)` 分配） | 每帧 `O(H×(spans+W))`，约 3 次分配/行（估算，无 alloc 实测）；不可见行也编码后丢弃 |
| 3 | 全屏强制重绘 | `terminal_session.go:1043-1045`（写过 transition/history 即 `Invalidate()` → `PrepareFlush` 走 forceRepaint，逐行 `emitForcedRow`）；`screen_model.go:294-339`（无条件整网格 copy） | 历史 handoff 与视口刷新同帧时退化为全屏 ANSI |
| 4 | 握手饿死 | `history_commit_executor.go:133-194`（成功路径 `WaitIdle` ×3；含 defer/fail 分支共 5 处调用点 `:140/:155/:166/:182/:192`；Post 2~4 次）；`controller.go:892-926`（`WaitIdle` 含 plan worker 在飞；`WaitIdleTimeout` 1ms 轮询）；executor `:847/:891/:1012` | plan 请求预算 250ms ⇒ 每 token 可能等一整个 screening；高吞吐下 mailbox 难排空 → `PlanIncomplete` → TOP5 |
| 5 | 规划 O(cells) 且会全量重跑 | `controller_plan_worker.go:84-110`；`history_effect_planner.go:119-146`（每次请求重推 `LayoutRows`，预算不含布局）、`:1348-1383`（`PlanIncomplete` 使 memo 失效）、`:1401-1407` 注释**实测 723ms/op（2000-cell transcript）**，同注释记录 plan-last-ms 4.7–6.9s / ~9 plans/min；`syncHistoryEffectCandidates` 每轮全候选 reconcile | 每个 chunk 可能一次 O(cells) 全量规划 |
| 6 | active 流式全量重解析 | `active_stream.go:509-533`（每帧 `FormatDocumentCached(整体 source hash)`；推导：source 变化即 miss → goldmark+chroma 全量重跑，精确 miss 率需实测；单块预算 80ms/64KB/2000 行）、`:443-477`（`Buffer.Render`+`Diff`+两次 `StyledSnapshot`，第二次为全量重拷贝）、`:479-507`（整段 split 数行）；`cache.go:89-133`（命中也要 O(len(source)) hash）；FPS 30（`render/frame.go:10`） | 30FPS 上限下最坏每秒 30 次全正文重解析（上限推算）；与增量无关 |
| 7 | 每帧 ANSI 后处理扫描 | `terminal_session.go:1048` → `:1391-1440`（逐字节识别 CSI；每个 CUP `strings.Split` 分配） | O(bytes)/帧 + 每 CUP 一次分配 |
| 8 | 锁竞争 | `renderengine/terminal_lock.go:10-14,40-105`（进程级全局 `terminalWriteMu` 覆盖整批写）；`terminal_session.go:752-753`（`transactionMu` 覆盖全屏 prepare+写）；`:756-767/:771-781/:1234-1259` 多段持 `s.mu`，期间有 `candidateScreen.Clone()`（`:907`） | 一次慢 tty 写阻塞所有 batch；`CommitHistory` 与 `FlushTransaction` 无法并发准备 |
| 9 | prepare 缓存必然失效 | `terminal_session.go:831-852`（每次先全量 `PresentationEqual` 比较）、`:1324`（写完 `preparedHistory=nil`）、`:1452/:1513/:1523/:1565`（每 commit 对 resident 尾做 O(capacity)/O(payload) 重排与逐行 `fmt.Fprintf`） | 写完即清，跨帧缓存不可用；每 token 重复渲染（命中率可用 `historyPrepareHits/Misses` 计数核验） |

## 5. 根因判定：为什么「反复修复总是出现新的 bug」

1. **没有单一事实源**（§3.2 的 14 处镜像）。每个修复只对齐其中一个镜像，其余副本在下一次流式/重绘时把旧值写回。
2. **native scrollback 的事务化**：scrollback 不可回读、不可改写、不可删除，但代码把它建模为
   6 态 ledger（pending/in_flight/acked/failed/invalidated/abandoned）+ ack/fail/defer + replay/settle/reconcile
   + reset backoff。不变量跨 5 个包、7 个阶段，任何路径组合都可能出现新的顺序窗口——这是分布式系统级的
   复杂度，而 UI 需求只是"日志式追加"。
3. **多层补偿互相叠加**：backoff、recovery 调度、projection unknown、reconciliation required、lease 冻结、
   scrollback replay、resize rebuild、partial-write fail-closed……每加一个守卫就新增一个状态组合，且守卫之间
   的优先级只存在于代码顺序里，没有可枚举的全局状态模型。
4. **输出边界不闭合**（§2）：模型外字节出口持续存在，任何一次未建模写入都会制造"随机"现象。
5. **成本模型错位**：把"每帧全量（全屏/全 transcript）"当作基线，导致延迟、GC、锁持有时间都与实际变化量无关；
   系统只能靠 FPS 上限、250ms 预算、backoff 睡眠等"限流补丁"维持可用，进一步增加状态。
6. **变更成本**：5,296 个测试（statemap/主审口径）中多数为行为固化断言；改一处需要同时重写多处断言，回归信号噪声大，促使开发者
   选择"局部最小补丁"，于是镜像漂移继续累积。

**结论**：这不是"某个人写得不好"，而是架构形态（多写端 + 多镜像 + 事务化 scrollback + 全量渲染基线）
把每一类修复都变成"在组合爆炸中找一致切面"。继续以局部补丁方式修复，产出新 bug 的概率会保持在高位。

## 6. 收敛方案（P0–P3）

**目标形态**：事件 → 单一有序队列 → 单一 reducer（AppState）→ 单一 frame producer → 单一 writer；
native scrollback 只做 append-only 单向交付（写出去的字节永不回读、永不如图改写）。

### P0 写端归一（建议最先做；约半天–1 天）

- 所有终端字节必须经统一 output 边界（`TerminalSession`/gateway port）。为标题/铃/编辑器序列提供
  session 侧旁路方法（session 内串行、可记录、可被 lease 感知），禁止组件自行持有 `os.Stdout`。
- stderr 收编：交互期统一走 `NotifyChatDiagnostic`（已有）或写日志文件；定位"stderr 与 stdout 同 tty"的缺口。
- CI 门禁：禁止 `render/output` 之外出现 `os.Stdout`/`fmt.Print`/`TerminalOutput()` 直写（白名单 DBG 文档）。
- 验收：单写端断言测试（注入计数 writer，断言交互期只有 1 个物理 writer）；真机 e2e（`scripts/test-aicli-windows-terminal-e2e.ps1`）。

### P1 状态收敛（2–4 天）

- ledger 6 态 → 2 态（pending/acked）或直接废除，改为"已交付行号单调游标"；删除 defer/inflight/rebase 特例
  （单线程序列化天然替代）。删除仅服务 ledger 的 diag/压缩逻辑。
- 删除可推导镜像：`historyStreamTailRows`、`historyTailCells`、`historyTopAligned`、`preparedHistory`、
  `PaintTrace` 内容 hash 计数（或降级为 /debug 专用）；几何只留 probe 一处（其余由广播的 Resize 派生）；
  帧号由 writer 单点分配。
- `WaitIdle` 握手改事件驱动 ack（消除 1ms 轮询与三连 Wait）。
- 验收：状态字段清单缩减（附 before/after 计数）；`go test ./cmd/aicli/ui ./cmd/aicli/commands -race` 全绿。

### P2 历史线性化（专项，需完整验收矩阵）

- finalized-only 进 history；active 只渲染在 band（现状已是方向，但派生出 archive/replay/settle/reconcile 全族特例）。
- 删除"active 溢出归档 / sticky top-align / scrollback replay / reset backoff / settle-unresolved"整族特例，
  替换为单向规则：**可见窗口 W 行 + 溢出按行序 append 进 native scrollback；任何 mutable 内容不得提前入
  scrollback（等 finalized 一次性写）**。resize 只重画窗口；不承诺 scrollback 可改写。
- 收益：本轮"中部插入"与上一轮"resident tail 双写"的土壤同时消失；历史路径不再需要 6 态账本。
- 验收矩阵：流式溢出 / 归档 / finalize / resize（收缩+扩张）/ lease 往返 / partial write / resume-replay /
  长会话（≥5k cells）；真机 marker exactly-once + 无异常空行。

### P3 性能（与 P1/P2 交错执行）

- 增量编码：`terminalFrameCells` 只物化 viewport 区（历史行占位符不编码）；diff 只处理脏行；去掉历史写入触发的全屏 Invalidate。
- active markdown：稳定前缀 + 尾部窗口增量解析；每帧预算 1–2ms 的增量重解析（代码块按内容寻址缓存）。
- 去全屏深拷贝：plan 单所有者零拷贝；`ScreenModel` 差分复用（swap 而非 copy）。
- 验收基准（新增 bench）：注入 N 行/s 流，统计 p50/p95 帧时延、每帧分配、GC 次数、锁持有时间；
  目标：**每 delta 工作量 O(delta) + 常量帧**，p95 帧时延 < 16ms（流式稳态）。

### 6.1 验收矩阵（总表）

| 阶段 | 关键验收 | 风险 |
| --- | --- | --- |
| P0 | 单写端断言；真机 e2e 全绿；CI 门禁生效（白名单外 0 命中） | 低 |
| P1 | 状态字段/队列计数缩减；`-race` 全绿；长会话 marker exactly-once | 中 |
| P2 | 8 项场景矩阵 + 真机 e2e；删除的特例清单与代码量统计 | 中高 |
| P3 | 新基准报告（p95/分配/GC）；delta 成本 O(delta) 证明 | 中 |

## 7. 审查记录（Review）

### 7.1 审查方式

- 基线固定为 `a75d1c89`；文档保存后由 **2 个独立只读审查代理**（仅用 view/grep/glob/ls，禁止 shell）逐条核对：
  - 审查 A（§1/§2）：写端清单的"LIVE / 边缘可达 / 死码"判定与 file:line、装配链、死码抽查、行数核对；
  - 审查 B（§3/§4）：性能 TOP 的 file:line 与口径、队列常量、14 处镜像抽查、过度断言（inference vs fact）审计。
- 主审同步抽查高风险断言（`terminal_session.go:743/:907`、`chat_setup.go:84`、`chat_interaction.go:715`、
  `render/frame.go:10`、`history_commit_executor.go:133-194`、`history_effect_planner.go:1401-1407`）。

### 7.2 审查发现与修订

| 来源 | 发现 | 判定 | 修订落点 |
| --- | --- | --- | --- |
| 审查 A | §2 待核 8 组写端/装配断言（fence 链、唯一写路径、inputbox_editor 三处、标题/铃装配、status 调用点、stderr 清单、死码抽查） | **7 组 confirmed**；1 组为口径问题 | §2 结论不变；§2.2 增补动画 tick 的文件归属（`chat_notification.go:263-304`） |
| 审查 A | §1.1 "行数"实为**非空行数**，物理总行数更高（如 `terminal_session.go` 2,035、`chat_runtime_events.go` 10,946） | corrected（口径） | §1.1 标题改为"非空行数"并增口径注；§4 行号以物理总行为准 |
| 审查 B | `history_commit_executor.go` `WaitIdle` 成功路径 3 次、含 defer/fail 分支共 **5 处调用点**（`:140/:155/:166/:182/:192`） | corrected（docs 原文 ×3 未限定路径） | §4 TOP4 改为"成功路径 ×3；共 5 处调用点" |
| 审查 B | gateway mirror 队列**生产值 64**（`chat_ui_actor.go:173`）；兜底 1024 在 gateway 构造下不可达 | corrected | §1.3 表内更正为"生产队列 64；兜底 1024 不可达" |
| 审查 B | 720ms/op 应为 **723ms/op**（原文 "measured at 723ms/op on a 2000-cell transcript"，并含 plan-last-ms 4.7–6.9s / ~9 plans/min） | corrected（数字） | §0/§4 TOP5 改为 723ms/op 并补原注释读数 |
| 审查 B | 多处结构性推导被写成实测事实：goroutine 跳转数、锁段数、分配次数/行、cache miss 率、plan.Clone"纯浪费"、"结构必然" | inference | §0/§1.4/§4 TOP1/TOP2/TOP6/§5 已加限定词（"结构推导/估算/需实测"），保留代码事实与推导分层 |
| 审查 B | 14 处镜像抽查 5 项（几何/lease/帧号/行内容/空闲判定）与 4 个队列常量（eventQueue 576、backlog 512/2MiB、mailbox 256、planWorker 1） | **全部 confirmed** | 无需修订 |
| 主审 | `history_commit_executor` 实读 5 处 `WaitIdle`；`chat_setup.go:84`、`chat_interaction.go:715`、`frame.go:10` 抽查 | 与审查 B 一致 | 同上 |

未复算项（保留"台账口径"标注）：§1.2 全仓脚本计数（553 文件 / ~50 goroutine 点 / 237 sync 原语 / 708 测试文件 /
5,296 `func Test`）与 §1.3 的队列/状态机/锁/缓冲总数——由主审脚本与 statemap 台账统计，审查轮无 shell 未能独立复算。

### 7.3 审查结论

- 核心判定（多写端未归零、14 处镜像、native scrollback 事务化、全量渲染基线、P0–P3 收敛方向）**未被推翻**；
- 未发现虚构行号或伪造证据；审查发现的偏差均为口径/数字/措辞级别，已全部修订回填；
- 修订后文档与 `a75d1c89` 代码快照一致；文中剩余不确定性（未实测的 miss 率/分配数/墙钟影响与未复算计数）
  已显式标注为"推导/估算/台账口径"。

## 附录 A. 证据索引

- 统一渲染流程全景：`docs/plan/aicli-chat-unified-render-stall-analysis-and-hardening.md` §2、§3、§8。
- 生产装配与 fences：`backend/cmd/aicli/commands/chat_setup.go:76-84,231`、
  `chat_ui_actor.go:108/185/195-239`、`chat_interaction.go:676-720`。
- 唯一物理写：`backend/cmd/aicli/ui/terminal_session.go:867/1846/1854/1890`、
  `terminal_session_presenter.go:44-66`。
- 写端盘点明细：见本报告 §2（对应子代理台账 writers，2026-10-05）。
- 状态与队列计数、14 处镜像：见本报告 §3（对应子代理台账 statemap，2026-10-05）。
- 性能 TOP：见本报告 §4（对应子代理台账 perf，2026-10-05；含 `terminal_session_executor.go:1046` 的
  真实会话规模注释 6,622 cells / 291,842 rows）。
- 当日两次现场修复（本报告的动机样本）：`b19284db`（实时事件单车道）、`a75d1c89`（active 归档保持贴底锚定）。
