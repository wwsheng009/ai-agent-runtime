# UI 事件桥丢事件加固方案（deferred queue drop hardening）

> 状态：**PR-0/PR-1/PR-2/PR-3 已实施；PR-4 落点 A（单轮预算与软着陆）+ 落点 B（TUI 可见进度）
> + 落点 C（`/debug` turn 级指标与主 chat 预算接线）+ 第 5 条（prompt cache 熔断与
> `UPSTREAM_INVALID_RESPONSE` 聚合）已实施**（2026-09-14，代码已落地并通过单测）。
> **P0/P1 主链（PR-0..PR-4 与第 5 条）收敛**；§6.5 的 P2 残余项（web SSE 丢帧暴露、
> 稳态 watchdog、投影失效归因分类、`PostDeferred` 高水位降级）不在本次范围，另行排期；
> §10.1 待决策 4/5/6 与 §10.2 假设仍开放。提交边界与勘误见 §8.5。
> 日期：2026-09-14（本地 +08:00）
> 适用版本：当前仓库 `E:\projects\ai\ai-agent-runtime`（Go module：`backend`）
> 关联文档：
> - `docs/plan/aicli-chat-unified-render-stall-analysis-and-hardening.md`（§8 残余风险：8.1 io.Writer 不可取消 / 8.2 PostDeferred 无硬上限 / 8.3 无界 WaitIdle / 8.4 drain 超时后同 epoch 迟到事件）
> - `docs/aicli/debug-chat-status.md`（`/debug/chat/status` 区块契约）
> - `docs/plan/aicli-terminal-e2e-methodology.md`（E2E 方法学）

---

## 0. 实施状态（2026-09-14）

**已落地：PR-0（分类计数，只观测）+ PR-1（事件桥分级 + 就地合并 + 分类计数）
+ PR-2（消费端批处理 + 成本指标 + 降级可见）+ PR-3 落点 A/B（executor 判决窗口化 + observe 分类）
+ PR-4 落点 A（单轮预算单一事实源 + 80% 软着陆 + token 硬边界优雅收尾）+ 落点 B（软着陆水位在 TUI 状态行可见）
+ 落点 C（turn 生命周期事件指标 + 主 chat `--budget-tokens` 贯通）**

**后续增补（2026-09-14，现场二次取证，见 §8.6）**：`assistant.reasoning`/`assistant.delta`
总线别名归入流式合并（`assistantStreamAliasType`），终稿只取代文本增量、同 turn/stream 的
reasoning 尾部有界先行冲刷（`flushStreamTailBoundedLocked`）；落点
`chat_runtime_events.go` + `chat_runtime_events_stream_coalesce_test.go`。

PR-0 / PR-1：

| 落点 | 内容 |
|---|---|
| `backend/cmd/aicli/commands/chat_runtime_events.go` | `chatEventClass` / `chatEventClassifyMode`（`AICLI_EVENT_BRIDGE_CLASSIFY=off\|observe\|enforce`，默认 `enforce`）、`classifyChatRuntimeEvent`、`chatEventCoalesceKey`、`deferRuntimeEvent` 的入队前合并与 coalescible 腾挪、`Handle` 的 critical 路由（保留位 + 重试通道）、`deferredQueueClassStats`、`recordCriticalShutdownIfPending`、`WaitForCurrentEvents` 结算谓词纳入 `criticalPending`、`EndRun` 的 `critical_at_shutdown`/`degraded` |
| `backend/cmd/aicli/commands/chat_debug_display_http.go` | `scene.deferred_queue` 新增 `merged/evicted/dropped_by_class/dropped_by_type/evicted_by_type/peak_pending/peak_bytes/critical_pending/critical_peak_pending/critical_at_shutdown/degraded/mode`（只增字段）；`BeginRunKind` 清除上一轮 `degraded` |
| `backend/cmd/aicli/commands/chat_debug_document.go` | 纯文本同步输出 `Deferred Queue *` 与 `Event Bridge *` 行 |
| `backend/cmd/aicli/commands/chat_runtime_events_classify_test.go` | §6.1.7 六项 + 分类映射/环境开关/`observe`·`off` 回退档 + 在途槽位续投共 10 项用例 |
| `backend/cmd/aicli/commands/chat_runtime_events_deferred_test.go`、`chat_runtime_events_critical_lifecycle_test.go` | 按分级后的投递契约更新既有用例的取样事件类型（见下"契约更新"） |
| `docs/aicli/debug-chat-status.md` | `deferred_queue` 字段契约与判读、回滚开关 |

PR-2 / PR-3：

| 落点 | 内容 |
|---|---|
| `backend/cmd/aicli/ui/controller.go` | `Run()` 批处理（§6.2 第 1 条）：一次唤醒内连续 apply 已就绪 action（上限 `controllerBatchLimit=64`），批尾只交付一帧 `FlushEffect`（`DirtyFlags` 取并集）；逐 action 的 revision/顺序/因果 follow-up/panic 语义不变。批内 `delivering` 全程在锁内置位，`WaitIdle` 不会在"已 apply 未交付"窗口假空闲。`ControllerStats` 新增 `ReducerNanos/FlushCount/Batches/BatchSizeMax/BatchSizeP95/PostWaitNanos/PostWaitCount/PostWaitMaxNanos/PostWaitP95Nanos`（P95 由有界 `sampleRing` 在锁外计算） |
| `backend/cmd/aicli/commands/chat_runtime_events.go` | 生产者侧真实背压计时：`postRuntimeEventToUIActorWithEpoch` 用 `defer` 测量等待 mailbox 容量的时长并回填 `ObservePostWaitNanos`（首投即成功不产生样本）。降级摘要有锁发布：`chatEventBridgeDegradation{Merged,Evicted,Dropped}` + `atomic.Pointer`（合并路径 1s 节流、丢弃/驱逐强制立即发布）+ `DegradationSnapshot()` |
| `backend/cmd/aicli/commands/chat_interaction.go` | 状态行降级提示（§6.2 第 3 条）：本次 run 内 `dropped+evicted>0` 时追加 `· ⚠ events degraded: merged=N dropped=M`；纯 merged（设计内合并）不打扰用户 |
| `backend/cmd/aicli/commands/chat_debug_display_http.go`、`chat_debug_document.go` | `app_state.ui_actor`（JSON，仅消费端有活动时输出）与纯文本 `UI Actor:` 小节；字段契约同步到 `docs/aicli/debug-chat-status.md` |
| `backend/cmd/aicli/ui/controller_test.go` | 新增 `TestUIController_BatchesReadyActionsIntoOneFlush`（批内多 action：revision 逐条递增、`Batches=1`、`FlushCount=1`、Dirty 取并集）；`TestUIController_EffectsDeliveredInOrder` 按批处理契约更新为单帧 |
| `backend/cmd/aicli/ui/history_hot_path_bench_test.go` | 新增 `BenchmarkUIControllerBurstBatching`（每轮突发 256 条，输出 `flush_per_event` / `batches_per_event` / `reducer_ns/event`） |
| `backend/internal/runtimeobserve/`（PR-3 落点 B） | `recordRuntimeEventDrop` 区分 `filtered_by_type`（类型已知、仅因不在 v1 白名单被过滤）与 `unknown_events_dropped`（目录外真未知）；`known_types.go` 封闭目录、快照新增 `runtime.filtered_by_type`；回归 `TestObserveCollector_ClassifiesKnownNonAllowlistedAsFiltered` |
| `backend/cmd/aicli/commands/chat_debug_document.go`（文本模式） | `UI Actor:` 小节（`Processed/Flushes`、`Batches`、`Reducer Total`、`Post Wait`、`Pending`），与 JSON 同源同判读 |
| `backend/cmd/aicli/ui/terminal_session_executor.go`（PR-3 落点 A） | 新增窗口判决 `WindowDiagnosis`（保留窗口＝最近 64 次 iteration 且不早于最新一条 60s；`windowEntries`/`windowSpanMs`/`windowAgeMs` 给出窗口形状），`Diagnosis` 明确为 since_start 历史判决；空环/静默环返回 `idle`，不再把陈旧风暴当作 CURRENT |
| `backend/cmd/aicli/ui/executor_diag_export.go` | pprof 文本摘要并列输出 `windowDiagnosis (CURRENT)` 与 `diagnosis (SINCE START)`，消除单读误判 |
| `backend/cmd/aicli/commands/chat_debug_display_http.go` | `executor` 区块新增 `diagnosis_scope`/`window_diagnosis`/`window_entries`/`window_span_ms`/`window_age_ms`（只增字段） |
| `backend/cmd/aicli/commands/chat_debug_document.go`（PR-3 落点 A） | `/debug/chat/status?format=text` 新增 `Executor Recovery Diag:` 小节（CURRENT / SINCE START / Window Shape / Last Iteration） |
| `backend/cmd/aicli/ui/terminal_session_executor_test.go`、`commands/chat_debug_display_http_test.go` | 窗口判决 7 项新用例（idle/healthy/backoff/handoff/dead_guard/双边界/空环）+ "历史风暴、当前健康"回归 + 文本 scope 标注断言 |

PR-4 落点 A（2026-09-14）：

| 落点 | 内容 |
|---|---|
| `backend/internal/agent/turn_budget.go` | 单轮预算单一事实源：`TurnBudgetSpec/TurnBudgetUsage/TurnBudgetState` + `EvaluateTurnBudget`（`TurnBudgetSoftRatio=0.8` 收尾水位、100% 硬水位、按维度的 `HardReasons/SoftReasons`）+ `FormatTurnBudgetDuration`（`45s/32m/1h20m`）+ `TokensSpentFromBudget`（token 维度与 `remainingBudget` 同口径，透支保留真实值）+ `TurnBudgetSoftLandingMessage` / `TurnBudgetHardStopMessage`；进度行 `turn budget: step 240/300 · 32m/40m · tokens 62%` 只列已配置维度 |
| `backend/internal/agent/system_reminder.go`、`lifecycle_hooks.go` | 新增 reminder kind `turn_budget`（`NormalizeReminderKind` 收编；**不进** `IsPureAdvisoryReminderKind`，故 durable 收尾提醒会随会话持久化）+ `newTurnBudgetReminderMessage` |
| `backend/internal/agent/loop.go` | 每步开头用同一份 spec/usage 评估水位；达到 soft 时**恰好一次**注入 durable 收尾提醒并 emit `system_reminder.injected`（payload 增 `turn_budget_level/line/ratio/reasons`）；已是 hard 时不注入；token 硬边界从"静默失败"改为优雅收尾（`LimitReached=true` + `LimitReason="turn_budget"` + 用户可见续跑文案写回 assistant 历史 + `persistBuilderHistory` + stop-failure hook `turn_budget`）；步数上限退出补 `LimitReason="step_limit"`；预算判决在既有 defer 里对每条退出路径统一回填 |
| `backend/internal/agent/agent.go` | `Result` 只增字段 `turn_budget_level` / `turn_budget` / `turn_budget_soft_cue_injected` |
| `backend/internal/agent/turn_budget_test.go`、`turn_budget_loop_test.go` | 纯逻辑 8 项（水位边界 79/80/100%、行格式、缺省维度不输出、时长格式、文案、durable kind）+ 循环级 3 项（软着陆注入恰好一次且出现在第二次请求、token 硬边界不调用模型且写回历史、步数上限带 `step_limit` 与水位行） |

PR-4 落点 B（2026-09-14，TUI 可见进度）：

| 落点 | 内容 |
|---|---|
| `backend/internal/agent/loop.go` | 软着陆事件的 turn 归属**不再二次注入**：`emitRuntimeEvent`（`loop.go:1316-1329`）已按 `loop.turnID`（源自 run ctx，`loop.go:402`）对每个事件统一盖章，事件构造处保持裸 payload，避免双写同一字段；新增循环级回归 `TestReActLoop_Run_TurnBudgetReminderEventCarriesTurnIdentity` 走真实 `EventBus` 钉住"事件类型 + payload kind + turn_id + durable"四项宿主前置条件 |
| `backend/cmd/aicli/commands/chat_runtime_events.go` | 新增 `chatRuntimeSystemReminderInjectedEvent` 常量（桥按事件类型字符串分派，不导入 `agent` 包）、`chatEventBridgeTurnBudget{Line,Level,Ratio}`、`turnBudget atomic.Pointer[...]` 字段、`TurnBudgetSnapshot()`（无锁读）、`observeTurnBudgetReminder(event)`（要求 `isPrimarySessionEvent` + `kind=turn_budget` + 非空 `turn_budget_line`；归属判定复用调用点的 `shouldSuppressMismatchedPrimaryTurnEvent`，与 `applyLLMRequestStatus`/`applySessionCompactStatus` 同源）；`handleEvent` 在 `applySessionCompactStatus` 之后接线；`BeginRunKind` 清空上一轮水位 |
| `backend/cmd/aicli/commands/chat_interaction.go` | 状态行附加提示合并为 `appendStatusHintsLocked`（降级提示 + 预算水位）；新增 `appendTurnBudgetHintLocked`：`StateText` 非空时追加 `· turn budget: step N/M · …`（只取进度行，不重复输出 level/ratio），两个调用点（状态切换、秒级 tick）统一走合并入口 |
| `backend/cmd/aicli/commands/chat_runtime_events_turn_budget_test.go` | 7 项：水位镜像、非 `turn_budget` kind 忽略、跨 turn 忽略、非主会话忽略、`BeginRunKind` 清空、状态行追加、降级提示与水位提示并存顺序 |

PR-4 落点 C（2026-09-14，turn 级指标与主 chat 预算接线）：

| 落点 | 内容 |
|---|---|
| `backend/internal/agent/loop.go` | `agent.turn.started`（`trace_id`/`step=0`/`max_steps`/`budget_level`）与 `agent.turn.finished`（`trace_id`/`step`/`elapsed_ms`/`budget_level`/`budget_ratio`）由 run 的启动段与 defer 发射，终局水位与 `Result.TurnBudgetLine` 同源；turn 归属仍由 `emitRuntimeEvent` 按 `loop.turnID` 统一盖章。新增 `LoopReActConfig.TurnBudgetTokens`，`loop.run` 在调用方未显式给出 `loopRunOptions.BudgetTokens` 时回落到它——显式值优先，子代理/团队任务预算不被宿主配置覆盖 |
| `backend/cmd/aicli/commands/chat_command.go`、`chat_options.go`、`chat.go`、`chat_setup.go`、`chat_actor_host.go` | 主 chat 预算贯通：`--budget-tokens` → `chatCommandOptions.BudgetTokens` → `ChatSession.TurnBudgetTokens` → `buildLocalChatLoopConfig` → `LoopReActConfig.TurnBudgetTokens`；`internal/chat` 的 per-run 克隆（`cloneLoopConfigWithRouteOverride` 的结构体拷贝）原样保留该字段，`cloneLoopConfigForRun` 只在无 base 时使用不限额缺省 |
| `backend/internal/agent/turn_budget_loop_test.go`、`backend/cmd/aicli/commands/chat_turn_budget_test.go`、`backend/internal/chat/actor_test.go` | 5 项新回归：缺省预算生效 + 显式覆盖优先（loop 级 2 项）、CLI 参数映射与 `ChatSession`→`LoopReActConfig` 映射（commands 2 项）、actor per-run 克隆保真（chat 1 项）；另有既有 turn 生命周期归属用例（`TestReActLoop_Run_EmitsTurnLifecycleEvents`）钉住事件字段 |

**实施期发现并修复的两个缺口**：

1. 分类表的 critical 集合原只含 `runtimechat.EventToolFinished`（`tool_finished`），
   而总线上的真实工具终态别名是 `tool.completed`（`exec_event_bridge.go:71`、`agent_stdio_bridge.go:224`、
   `events/bus.go:1086`）。漏掉别名会让工具边界退回有序队列并被溢出丢弃，已补入并加断言。
2. **数据竞争（§8.1 race 闸门实测捕获）**：`runDeferredQueue` 在锁外读取队首槽位的 `event/size`，
   而该槽位仍留在 `deferredIndex` 中，发布者可在锁内 latest-wins 写入同一槽位
   （`chat_runtime_events.go:1262/1263` ↔ `:1401`，race 报告 2 处，见 §8.1 实测）。
   修复：队首进入"在途"态后不再被就地改写——最新值挂到 `pendingEvent/pendingSize/hasPending`
   （仍在 `deferredMu` 内写入），投递成功后由 worker 原位续投；`inFlight` 槽位仍可被驱逐
   （保住 §6.1.4 的腾挪优先级），其 pending 值随之降级（§9 风险表）。

**既有用例的契约更新（4 处，均有据）**：`tool.completed` 升为 critical、`tool.progress` 归入
coalescible 之后，以下既有用例的取样事件不再落在被测路径上，故按分级后的契约改用 ordered 类事件
（`checkpoint_created` / `tool.requested` / `tool_started`，并在用例内断言其类别）：

- `TestCriticalLifecycleUsesReservedQueueCapacity`：填充"普通容量"的必须是 non-critical 事件；
- `TestDeferredRuntimeEventsPreserveOrder` / `TestDeferredBacklogKeepsHandleEventsInOrder`：
  溢出队列只对 ordered/coalescible 保持 FIFO，critical 走保留位 + 重试通道；
- `TestDeferredRuntimeEventBacklogIsBounded`：容量上限只约束 ordered 类，
  coalescible 会就地合并（永不触顶）；
- `TestWaitDeferredDrainReportsPendingBacklog` / `TestWaitForCurrentEventsSeesDeferredBacklog`：
  排水屏障针对的是溢出队列积压（critical 在途由 `criticalPending` 单独结算，已有专门用例覆盖）。

跨类 FIFO 不再是契约：critical 可以越过仍滞留在溢出队列中的 ordered 事件（渲染按时间戳，
见 §8.4）。

**验证命令与结果（2026-09-14 实测）**：

```powershell
Set-Location E:\projects\ai\ai-agent-runtime\backend
go build ./cmd/aicli/...                                              # 通过
# PR-0 / PR-1：
# 该命令修复前报 2 处 DATA RACE；修复后：
go test -race -count=1 -timeout 900s ./cmd/aicli/commands/ -run 'ChatRuntimeEvents'   # ok 15.6s
go test -count=1 -timeout 1800s ./cmd/aicli/commands/                 # 整包 ok（78.4s ~ 87.9s）
go test -race -count=1 ./internal/runtimeobserve/                     # ok
go test -race -count=1 ./cmd/aicli/ui/... -run 'UIController|ExecutorDiag|FramePump'  # ok
# 定向回归（10 项分类用例 + 6 项 deferred 既有用例 + 保留位/终态/排水用例）：全绿，9.4s
# PR-2 / PR-3 落点 B：
go test -race -count=1 -timeout 300s ./cmd/aicli/ui/                  # 整包 ok 8.5s（含新增批处理用例）
go test -race -count=1 ./cmd/aicli/commands/ -run 'RuntimeEvent|DebugDisplay|SurfaceStatus|Deferred|Degrad|Bridge'  # ok 18.4s
go test -race -count=1 ./cmd/aicli/commands/ -run 'TestChatDebugDisplayUIActorCostBlock'  # ok（JSON app_state.ui_actor + 文本 UI Actor 小节）
go test -race -count=1 ./internal/runtimeobserve/                     # ok 2.7s（含 filtered_by_type 分类用例）
go test -count=1 -run '^$' -bench 'BenchmarkUIControllerBurstBatching' -benchtime 200x ./cmd/aicli/ui/
#   → 883833 ns/op（每轮 256 条，≈3.45µs/event）、flush_per_event=0.0158、batches_per_event=0.0158
#     即约 63 条 action 合并为一帧；批处理前该比值恒为 1（每条 action 各交付一帧，
#     对应 TestUIController_EffectsDeliveredInOrder 旧断言的 2 action → 2 帧）
# PR-3 落点 A：
go test -race -count=1 -timeout 300s ./cmd/aicli/ui/ -run 'ExecutorDiag'      # ok 1.3s（7 项窗口用例 + 既有 since_start 用例）
go test -race -count=1 -timeout 600s ./cmd/aicli/ui/                         # 整包 ok 8.7s
go test -race -count=1 -timeout 600s ./cmd/aicli/commands/ -run 'ChatDebugDisplay|Executor|SurfaceStatus|Docs'  # ok 39.1s
# PR-4 落点 A/B：
gofmt -l backend/internal/agent backend/cmd/aicli/commands                    # 空
go test -race -count=1 -run 'TurnBudget|TokenBudget|StepLimit' ./internal/agent/   # ok 3.886s（含事件 turn 归属回归）
go test -count=1 -run 'TurnBudget|StatusHints|IgnoresNonBudgetReminders' -v ./cmd/aicli/commands/  # 7/7 PASS，ok 0.196s
# PR-4 落点 C：
gofmt -l backend/internal/agent backend/internal/chat backend/cmd/aicli/commands     # 空
go test -count=1 -run 'TurnBudget' ./internal/agent/                                 # ok
go test -race -count=1 -timeout 600s -run 'TurnBudget|TurnLifecycle' ./internal/agent/   # ok 4.179s
go test -count=1 -run 'BudgetTokens|TurnBudget' ./cmd/aicli/commands/                # ok 0.176s
go test -count=1 -run 'CloneLoopConfig' ./internal/chat/                             # ok 0.113s
go test -count=1 -timeout 600s ./cmd/aicli/commands/                                 # ok 77.124s（整包回归）
```

**PR-2 实施期发现并修复的自有缺陷**：

1. 批循环初版在解锁状态下读 `c.queue/c.followups`（数据竞争），且批尾复用末段会解锁的
   `deliverTracked`，导致 `TestUIControllerCoalescesQueuedActiveUpdatesByCellID` 挂起。
   改为每轮在锁内显式判定收批条件，交付统一走不解锁的 `c.deliver`。
2. 批尾交付未置 `delivering`：`WaitIdle` 可能在"批已 apply 完但 flush 未交付"的窗口返回假空闲。
   改为批内只要有待交付输出就在锁内置位 `delivering`，批尾交付完成后再于锁内复位并 `Broadcast`。

**PR-3 落点 A 的判读契约（新增）**：`window_diagnosis` 是 CURRENT（最近 64 次 / 60s），
`diagnosis` 是 SINCE START；`window_age_ms > 60000` 且 CURRENT=idle 表示执行器确实安静、
不是观测盲区。排障只看 CURRENT，`diagnosis` 仅用于历史归因。

**PR-4 落点 B 的判读契约（新增）**：状态行上的 `turn budget: …` 是**通告时刻**的水位
（软着陆触发那一步的取样），`Result.TurnBudgetLine` 是**退出时刻**的终局水位；二者同源
但取样点不同（实测：事件行 `step 1/10`，终局行 `step 2/10`），宿主不得把事件行当终值。
水位是 per-run 状态：`BeginRunKind` 清空，跨 turn 不继承；行只在 `StateText` 非空
（运行中/完成摘要）时追加，空闲行不会长出预算文案。

**落点 C 已落地（2026-09-14）**：`agent.turn.started/finished` 由 Go 侧发射
（`trace_id`/`step`/`elapsed_ms`/`budget_level`/`budget_ratio`；turn 归属由 `emitRuntimeEvent`
按 `loop.turnID` 统一盖章），`/debug/chat/status` 的 turn 级指标区块由
`appendChatDebugTurnMetricsLines` 渲染；主 chat 的 `--budget-tokens` 已贯通到
`LoopReActConfig.TurnBudgetTokens`（显式子代理/团队任务预算优先）。

**第 5 条已落地（2026-09-14）**：prompt cache 熔断与 `UPSTREAM_INVALID_RESPONSE` 聚合见
`internal/agent/prompt_cache_breaker.go`（阈值触发 + 退避 + 聚合上报）。落点 A/B 的 recon
结论与语义边界见 §6.4。§8.3 的目标值复核工具见 §8.5；现场窗口复测待有活动会话时执行。

---

## 1. 结论摘要

2026-09-14 对活动会话 `session_20260913210527_7mj5o08M`（endpoint `http://127.0.0.1:64751`）
做全链路取证后确认：

1. **会话没有死锁。** agent 主循环、工具执行、文件产出、LLM 请求都在推进
   （12 分钟 60 次请求，`in_flight=0`、`errors=0`、`retries=0`）；历史投影 gates
   在 10:00 后自行收敛（`projection_unknown=false`、`reconciliation_required=false`、
   `pending=0`）。
2. **但 UI 事件桥在持续丢事件，且这是设计内的降级路径被常态触发。**
   `scene.deferred_queue.dropped` 在约 39 分钟内累计到 **35,050**，
   期间观测到的峰值秒级速率约 130/s、全时段均值约 16–22/s。
3. **该降级是"保护 LLM 回调延迟"的既有取舍**（`chat_runtime_events.go:271-279` 注释记录了
   历史事故：等待 UI 容量曾把子代理 LLM 调用拉长到 ~112s、让活跃 turn 看起来卡住数十分钟），
   因此**不能用"阻塞等待 UI"或"无限放大队列"来修复**。
4. 与丢事件**同期观测**到的其它异常：渲染投递序列曾冻结约 3 分钟（83940 → 恢复后
   84066→84240）、legacy/derived 文本对照出现 35 行差异、历史投影反复 `reconciliation`
   （累计 `invalidated=2130`、2 次 scrollback reset）。三项的因果关系**尚未验证**，
   按"假设"处理，见 §2.4 与 §10.2。
5. 另有一项**已确认的内容真丢失路径**：`assistant_message` 入队前会先丢弃该 turn 的
   合并增量（`:900-901`），而终稿本身仍可能被溢出队列丢弃（`:909-916`），
   导致整段文本在渲染面上消失（详见 §4.1）。
6. 另有两处**可观测性失真**放大了"卡住"的误判：
   - `executor.diagnosis` 是**自启动累计**判决，一旦历史上出现过 backoff+handoff 就永久返回
     `backoff_engaged_handing_off`（实测 10:04 仍报此值，而当时 gates 全绿、`in_flight=0`）；
   - observe 平面把**不在 v1 白名单里的已知总线事件**一律计入 `unknown_events_dropped`
     （实测 24,521，约 32/s），把"未知事件"这一异常信号稀释成常态噪音。

本方案的处置顺序：**P0-1 事件分级 + 入队前就地合并（治本，降低生产端速率）→
P0-2 消费端吞吐与降级可见（治本，提高消费端能力）→ P1-1 诊断窗口化与 observe 分类
（消除误导）→ P1-2 单轮预算与软着陆（降低长轮压力）→ P2 残余风险收敛**。

---

## 2. 现场证据（2026-09-14）

### 2.1 采集命令

```powershell
# 事件桥溢出队列（/debug/chat/status 的 scene 区块）
curl.exe -s -m 20 "http://127.0.0.1:64751/debug/chat/status" |
  jq -c '{pending:.scene.deferred_queue.pending, bytes:.scene.deferred_queue.bytes, dropped:.scene.deferred_queue.dropped}'

# 丢事件日志（drop 计数按 /64 节流打印 dropped_total）
Select-String -Path "$env:USERPROFILE\.aicli\chat-logs\2026\09\14\*.debug.log" `
  -Pattern 'deferred queue full' | Select-Object -Last 20

# observe 平面自监控（unknown/filtered 计数）
curl.exe -s -m 20 "http://127.0.0.1:64751/api/runtime/observe/v1/snapshot" |
  jq -c '.data.runtime | {unknown_events_dropped, event_ingress_dropped, projection_errors}'

# executor 诊断块
curl.exe -s -m 20 "http://127.0.0.1:64751/debug/chat/status" | jq -c '.executor'
```

### 2.2 观测数据

| 指标 | 采样值 | 说明 |
|---|---|---|
| `scene.deferred_queue.dropped` | 09:25:50 首条日志 = **1**；10:03:32 日志 = **35,008**；10:04:38 现场 = **35,050** | 约 39 分钟持续增长；10:00:38→10:03:29 区间约 +3,826（≈22/s）；日志相邻时间戳峰值约 130/s |
| `scene.deferred_queue.pending` | 在 0 与 63 之间抖动，未见长期贴顶 | 消费端**在消费但跟不上突发**，丢事件是周期性溢出而非永久卡死 |
| `unknown_events_dropped` | 10:00:38 = 16,939 → 10:04:38 = **24,521** | 约 +7,582/4min ≈ **32/s** |
| `llm.requests_total` | 09:52:58→10:04:38 = 60 | ≈5 req/min，`in_flight=0`、`errors=0`、`retries=0` |
| 渲染投递 `last_sequence` | 83940 冻结约 3 分钟后恢复，10:00 起 84066→84240 | 与事件桥压力同源的过载表现 |
| 历史投影 | `pending=0`、`projection_unknown=false`、`reconciliation_required=false`（10:00 起）；累计 `invalidated=2130`、scrollback reset = 2（reason=`reconciliation`） | gates 已自愈，但修正→失效路径仍高频 |
| 单轮 turn | 同一 turn >70min、step≈500、128 请求对、2174 条消息、Compact Gen#3、heap 296→340MB | 长轮是上述压力的持续来源 |
| executor 诊断 | `backoff_engaged_handing_off`（10:04 复采仍为真） | 累计判决，非实时状态 |
| prompt cache | 6 次 `UPSTREAM_INVALID_RESPONSE`（epoch 6/7/8/10/17，最后一次 09:51） | 长轮下的重试抖动 |

### 2.3 判定

- 事件桥的问题**不是"队列太小"**：`pending` 会回落到 0，说明消费端具备吞吐能力；
  问题在于**生产端在长轮中出现远超消费端瞬时能力的突发**，而现有结构只有
  "流式合并"与"子代理终态重试"两个极端，中间层全部挤在同一个 512 条 FIFO 里，
  溢出即丢弃。
- 因此修复方向必须是**降低生产端入队速率（分级 + 合并）**与**提高消费端吞吐**双管齐下，
  而不是调大 `chatRuntimeDeferredEventLimit`。

### 2.4 影响面界定（避免把问题说大或说小）

本方案针对的是 **TUI 渲染数据面**里 `chatRuntimeEventBridge` 的私有溢出队列。
经代码核对，其它消费者各有独立订阅与背压路径，**不经过**该队列：

| 消费者 | 数据来源 | 是否受本问题影响 |
|---|---|---|
| TUI 渲染（Scene / 协调器 / 帧泵） | bridge 有界队列 + 溢出队列 | ✅ 受影响（本方案范围） |
| `/web/` 微型客户端 SSE | 直接订阅 `host.EventBus`（`web_handlers.go:365`），自带 256 帧异步队列与丢帧计数（`web_handlers.go:123-134`、`:256-269`） | ⚠️ 独立链路，不共享本队列容量；其自身也存在"慢客户端丢帧"，见 §6.5 新增条目 |
| 事件日志 / 会话日志 | 独立订阅者落盘 | ❌ 不受影响（依据 `chat_debug_document.go:524-526` 注释；**未实测**） |
| observe 平面 / supervision | `host.EventBus` + 各自过滤 | ❌ 不受影响（其自身噪音问题见 §6.3） |

**已证 / 待验证的因果链**：

- **已证**：`dropped` 累计 35,050、速率 16–130/s（§2.2）；`pending` 会回落，说明消费端具备吞吐能力。
- **待验证假设（不作为本方案的验收依据，见 §10.2）**：
  1. 35 行 legacy/derived 文本 parity 差异与丢事件同源——两者都在渲染数据面，
     但 Scene 投影与旧路径是否处于同一条丢弃路径上**未验证**；
  2. 历史投影 `invalidated=2130` 由丢事件驱动——失效判定发生在
     `transcriptReplacementInvalidatesAckedHistory`（`ui/controller_state.go:619`），
     与事件桥丢弃是否相关**未验证**。

---

## 3. 现状链路与关键代码锚点

### 3.1 非流式事件投递链路

```
publisher: provider stream callback / tool loop / agent act
  └─ bridge.Handle(event)                                   chat_runtime_events.go:867
       ├─ isMergeableStreamEvent(event.Type)
       │     └─ enqueueStreamEvent(...)                      :876-882
       │        （已有合并：128 条 / 1MiB 阈值，latest-wins）
       ├─ isCriticalSubagentLifecycleEvent(event.Type)       :884-894, :920-935
       │     ├─ enqueueNonStreamEvent(budget=0)
       │     └─ enqueueCriticalRuntimeEventEventually(...)   ← 关键事件不丢（重试通道）
       └─ 其它非流式事件（工具边界/终态/审批/压缩/…）
             ├─ flushPendingStreamEventBounded(100ms)        :907
             ├─ enqueueNonStreamEvent(200ms 预算)            :909
             └─ 失败或已有积压 → deferRuntimeEvent(event)     :909-917
                     └─ deferredQueue FIFO
                         容量：512 条 / 2MiB，重试间隔 5ms     :285-287
                         溢出：deferredDropped++ 并丢弃        :992-1004
                         日志按 /64 节流                       :964-968
                     └─ runDeferredQueue 单 worker FIFO 重投    :1019-1056
```

> **注意（过载期容量旁路）**：`Handle` 在直接入队前会先检查 `deferredBacklogPending()`
> （`:909`、`:1093-1098`）——只要溢出队列非空，后续所有非流式事件都改走 deferred 路径，
> 以保持严格 FIFO。因此在持续过载期间，有界队列的 512+64 正常容量事实上被旁路，
> 全部非流式事件都依赖 512 条溢出队列兜底——这也是 `dropped` 快速累积的机制之一。

相关常量（`chat_runtime_events.go:237-292`）：

| 常量 | 值 | 作用 |
|---|---|---|
| `chatRuntimeEventQueueNormalCapacity` | 512 | 有界队列正常容量 |
| `chatRuntimeEventQueueCriticalReserve` | 64 | 关键事件保留位 |
| `chatRuntimeEventQueueByteLimit` | 4 MiB | 有界队列字节上限 |
| `chatRuntimeNonStreamEnqueueBudget` | 200 ms | 非流式事件等待有界队列的预算（**不可放大**，见 §4.1） |
| `uiActionPostBudget` | 5 s | bridge → UI actor 邮箱的投递等待上限 |
| `chatRuntimeDeferredEventLimit` | 512 | 溢出队列条数上限（溢出即丢） |
| `chatRuntimeDeferredEventByteLimit` | 2 MiB | 溢出队列字节上限 |
| `chatRuntimeDeferredRetryInterval` | 5 ms | 溢出队列重投间隔 |
| `chatRuntimeDeferredDrainBudget` | 1500 ms | EndRun 等待溢出队列排空的上限 |
| `chatRuntimeEndRunDrainTimeout` | 8 s | EndRun 普通 drain barrier 上限 |

### 3.2 消费端（UI actor）

- 邮箱容量：`ui/controller.go:24-27` 默认 **256**（`MailboxSize<=0` 时）。
- 投递语义：`Post`（可阻塞/可合并，`:231`）、`TryPost`（非阻塞，`:276`）、
  `PostDeferred`（逃生口，允许暂时超过 cap，`:319`）、`PostFollowup`（因果后继，`:373`）。
- 诊断计数已存在：`ControllerStats{Posted, Processed, Dropped, DeferredPosted,
  DeferredMerged, CapacityOverflow, PeakPending, Pending, Revision, LastAction}`
  （`ui/controller.go:130-140`，快照 `:718-735`）。
- 帧调度：`backend/cmd/aicli/ui/renderengine/frame_pump.go`（统一帧泵，已具备合帧能力）。

### 3.3 可观测性暴露面

| 位置 | 内容 |
|---|---|
| `commands/chat_debug_document.go:523-527` | 纯文本模式打印 `Deferred Queue Pending/Bytes/Dropped` |
| `commands/chat_debug_display_http.go:397-401` | JSON `scene.deferred_queue{pending,bytes,dropped}` |
| `commands/chat_runtime_events.go:1099-1110` | `deferredQueueStats()`（唯一数据源，无分类维度） |
| `ui/terminal_session_executor.go:62-70` | `ExecutorRecoveryDiag{..., Diagnosis, WindowRecoveriesPerSec}` |
| `ui/terminal_session_executor.go:323-325` | `Diagnosis` 判决（**累计量**） |
| `internal/runtimeobserve/projector.go:33-61` | v1 事件白名单（22 类）+ `IsAllowedType` |
| `internal/runtimeobserve/collector.go:225-260` | 非白名单事件 → `UnknownDropped++`（两处） |
| `internal/runtimeobserve/model.go:119-120` | `event_ingress_dropped` / `unknown_events_dropped` |
| `commands/web_schema.go:76-111` | 总线事件 → SSE 映射表（40+ 条，覆盖 compact/approval/question/job/动态状态/cache 分析等） |

---

## 4. 根因分析

### 4.1 根因 A：非流式事件缺少分级，全部挤进同一个有界 FIFO

`Handle` 目前只区分三类（`:876` / `:884` / `:900`）：

- **流式增量**：已合并（128 条 / 1MiB，latest-wins），溢出会降级为"更少更大的重绘"而不是丢事件。
- **子代理终态**：走专用重试通道，不丢。
- **其余全部非流式事件**：走同一条 200ms 预算 + 512 条 FIFO 路径，**溢出即丢弃**。

问题在于第三类的构成极其混杂：既有语义上必须到达的（`tool_finished`/`tool_failed`、
`assistant_message`、`approval_resolved`、`checkpoint_created`、`compact_*`），
也有天然可合并的（`usage.updated`、`dynamic_status`、`tool.progress`、
`cache_request_finished`、`context.reconciled`）。前者的丢失会让 TUI 出现"缺行/缺结果"，
后者则会以每秒数十条的速率把 512 条容量吃光，**把容量从关键事件手里抢走**。

注释（`:895-899`）声称"non-streaming events（finals, approvals, tool boundaries）are rare"，
这一前提在长轮 + 并发子代理场景下**已经不成立**。

**附加问题（内容真丢失，非仅"少一次重绘"）**：`Handle` 在入队决策**之前**就调用
`dropPendingStreamsForTerminal(event)`（`:900-901`），其语义是"终稿快照携带完整内容，
停滞的 UI 不必再等这些终将被丢弃的增量"（`:1337-1346`）。但终态事件随后仍可能
在 `deferRuntimeEvent` 里因队列满被丢弃（`:909-916`）——此时**增量已被丢弃、终稿也没到**，
该 turn 的文本在渲染面上整体消失。这解释了"内容假丢失"类投诉，也意味着
`assistant_message` 的保护级别必须高于它要取代的增量。

### 4.2 根因 B：消费端吞吐与生产端突发不匹配

- UI actor 邮箱仅 256；bridge 侧等待上限 5s（`uiActionPostBudget`），超过即转溢出队列。
- 大 transcript（2174 条消息）+ 长轮下 reducer/帧写成本上升，消费速率下降。
- 生产端在长轮中的事件密度却由工具调用与子代理驱动，出现毫秒级突发。
- 结果：`pending` 周期性冲高到上限并溢出——**丢事件是突发性的、持续的**，
  与"永久卡住"不同，但用户侧观感相同（渲染内容缺失、序列冻结）。

### 4.3 根因 C：可观测性把"设计内降级"渲染成"未知事件风暴"

- `collector.go:233-237` 与 `:255-259`：任何非白名单总线事件都计入 `UnknownDropped`。
  而 `web_schema.go:76-111` 表明这些类型是**已知**的（compact/approval/question/job/
  dynamic_status/cache_request_finished 等），只是 observe v1 白名单（`projector.go:33-56`，22 类）
  尚未覆盖。实测 24,521 次、约 32/s 的"unknown"实际是**已知事件的过滤计数**。
- `ui/terminal_session_executor.go:323-325`：判决只看累计计数器，
  一旦 `BackoffEngaged>0 && HandoffsWhileBackoff>0` 成立，`Diagnosis` 永远返回
  `backoff_engaged_handing_off`，与"当前是否健康"无关。现场 10:04（gates 全绿、`in_flight=0`）
  仍报该值，直接导致"会话卡住"的误判。
- `scene.deferred_queue` 只有 `pending/bytes/dropped` 三个标量，
  **没有任何分类维度**，无法判断丢的是关键事件还是可合并事件——这也是本方案要补的
  核心可观测性缺口。

### 4.4 根因 D：长轮无预算，压力长期持续

同一 turn 超过 70 分钟、step≈500、128 请求对、Compact Gen#3、heap 296→340MB，
伴随 6 次 `UPSTREAM_INVALID_RESPONSE` 与 prompt cache epoch 抖动。长轮本身不是 bug，
但它把 §4.1/§4.2 的过载从"瞬时"变成"常态"，并放大缓存与上下文压力。

---

## 5. 设计目标、非目标与约束

### 5.1 目标

| 编号 | 目标 | 可验证指标 |
|---|---|---|
| G1 | 关键事件在溢出下零丢失 | 溢出风暴测试中 critical 事件到达率 100% |
| G2 | 生产端入队速率显著下降（合并生效） | 同 key 事件 N 次入队 → 队列仅 1 条 |
| G3 | 溢出丢弃可解释、可分类 | `dropped_by_class` / `dropped_by_type` 出现在 `/debug/chat/status` |
| G4 | 降级对用户可见 | TUI 状态行在丢事件时显示降级提示（含计数） |
| G5 | 诊断反映"当前"而非"历史" | executor 判决可按窗口计算，累计值另行暴露 |
| G6 | observe 噪音归零 | `unknown_events_dropped` 增速回落到个位数/分钟 |

### 5.2 非目标（明确不做）

1. **不做阻塞等待**：不放大 `chatRuntimeNonStreamEnqueueBudget`（200ms）去等 UI 容量。
   依据 `chat_runtime_events.go:271-279` 记录的事故（子代理 LLM 调用被拉长到 ~112s、
   活跃 turn 看似卡住数十分钟）。
2. **不无限放大队列**：不把 `chatRuntimeDeferredEventLimit` / `ByteLimit` 当作修复手段。
   容量只能作为"突发吸收"的辅助，必须与合并、驱逐、消费端吞吐一起使用。
3. **不引入伪超时/伪取消**：不通过 goroutine+select 假取消 `io.Writer.Write`
   （既有加固文档 §8.1 的明文约束）；不通过超时丢弃已受理的关键事件。
4. **不改变既有外部契约语义**：SSE 事件名、`/debug/chat/status` 既有字段保持兼容，
   新增字段只增不改。
5. **不新增无界等待**：所有新增等待必须有界（既有 §8.3 纪律）。

### 5.3 必须保持的既有不变式

- bounded mailbox 背压（`ui/controller.go` 邮箱容量语义不变）；
- `TryPost` 非阻塞；`coalescable` latest-wins；`PostDeferred` 相对 FIFO；
- 事件字节预算背压；`EndRun` 有界等待（`chatRuntimeDeferredDrainBudget` / `chatRuntimeEndRunDrainTimeout`）；
- 失败写 fail-closed。

---

## 6. 方案

### 6.1 P0-1 事件分级 + 入队前就地合并（治本）

**落点**：`backend/cmd/aicli/commands/chat_runtime_events.go`
（`Handle` :867、`deferRuntimeEvent` :~985、`runDeferredQueue` :1019、`deferredQueueStats` :1103）。

#### 6.1.1 分类

```go
type chatEventClass int

const (
    eventClassStream      chatEventClass = iota // 已有流式合并路径（不变）
    eventClassCritical                          // 不丢：重试通道（含现有子代理终态）
    eventClassOrdered                           // 有序：FIFO，仅在极端溢出时丢
    eventClassCoalescible                       // 可合并：入队前 latest-wins
)

func classifyChatRuntimeEvent(eventType string) chatEventClass
```

| 类别 | 事件类型（首批） | 理由 |
|---|---|---|
| critical | `subagent.*` 终态（沿用 `isCriticalSubagentLifecycleEvent`，:920-935）、**`assistant_message`（assistant 流终稿）**、`session.end`、`run.end`、`tool_finished`/`tool_failed`、`approval_requested`/`approval_resolved`、`question_asked`/`question_answered`、`compact_failed` | 丢失会造成控制面状态缺失或不可恢复的交互断链；`assistant_message` 一旦被丢，其已取代的增量也已丢弃（§4.1），形成内容真丢失 |
| coalescible | `usage.updated`、`dynamic_status`、`tool.progress`、`cache_request_finished`、`context.reconciled`、`checkpoint` 类周期性指标 | 天然 latest-wins；高频且历史值无独立语义 |
| ordered | `tool_started`、`compact_started/completed/skipped`、`job.*`、`mailbox_received`、其它未分类 | 保留顺序语义，溢出时可丢但需计数 |
| stream | `assistant_delta`、`reasoning_delta`（现有 `isMergeableStreamEvent`），含总线双拼写别名 `assistant.reasoning`/`assistant.delta`（§8.6） | 不变；别名漏归类会让高频流式事件降级 ordered |

**兼容**：`isCriticalSubagentLifecycleEvent` 保留函数名与语义，仅在 `classifyChatRuntimeEvent`
内部调用，避免大面积测试改写。

#### 6.1.2 合并键

```go
func chatEventCoalesceKey(event runtimeevents.Event) string
```

| 事件 | 键构造 | 说明 |
|---|---|---|
| `usage.updated` | `usage\|<sessionID>\|<turnID>` | 同一 turn 只保留最新 usage |
| `dynamic_status` | `dynamic\|<sessionID>` | 状态行只关心当前值 |
| `tool.progress` | `toolprog\|<sessionID>\|<toolCallID>` | 单次工具调用的进度 |
| `cache_request_finished` | `cachefin\|<llmRequestID>` | 每条缓存记录终态唯一 |
| `context.reconciled` | `ctxrecon\|<sessionID>\|<turnID>` | 同一 turn 的调和结果 |
| 其它 coalescible | 无稳定键 → **降级为 ordered** | 宁可不合并，也不吞事件 |

#### 6.1.3 就地合并（in-place coalescing）

```go
type chatRuntimeQueuedEvent struct {
    event runtimeevents.Event
    size  int64
    key   string // 空 = 不可合并
    // 在途态：worker 正在锁外投递该槽位，event/size 不可再被发布者改写；
    // 此窗口内到达的最新值挂到 pending，投递成功后原位续投。
    inFlight     bool
    pendingEvent runtimeevents.Event
    pendingSize  int64
    hasPending   bool
}

// bridge 新增字段
deferredIndex          map[string]*chatRuntimeQueuedEvent // key -> 队列槽位
deferredMerged         uint64
deferredEvicted        uint64
deferredDroppedByClass map[chatEventClass]uint64
deferredDroppedByType  map[string]uint64 // 有界 top-N（如 16）
deferredPeakPending    int
deferredPeakBytes      int64
```

语义：**同 key 的新事件替换槽位内容与字节计数，但不改变槽位在队列中的位置**——
保持"该 key 首次出现位置"的全局顺序，同时实现 latest-wins。
这与既有流式合并的语义保持一致（`chat_runtime_events.go:260-265`：
可见 `sequence` 取最后一次，合并区间由 `coalesced_from` 表达）。

**在途窗口（race 修复）**：worker 读出队首后会在**不持锁**的情况下调用
`enqueueNonStreamEvent`，因此该槽位的 `event/size` 必须视为只读；同 key 的新值在这段时间里
写入 `pending*`（仍在 `deferredMu` 内），worker 投递成功后把 pending 提升为槽位内容并继续投递
（槽位与索引都不变，同一 key 的峰值占用仍是 1 个槽位）。这既消除了
`:1262/1263` ↔ `:1401` 的数据竞争，也避免最新值另开槽位或在窗口内丢失。

#### 6.1.4 溢出裁决（shed order）

队列满（条数或字节）时按固定顺序处置：

1. **合并优先**：新事件可合并且 key 命中 → 就地替换（命中在途槽位则挂 `pending`，
   见 §6.1.3），不消耗容量，`deferredMerged++`，返回。
2. **腾挪次之**：从队首向后（最旧优先）寻找 `coalescible` 槽位并驱逐，
   为新事件腾出位置；`deferredEvicted++`，记录被驱逐类型。在途槽位同样可被驱逐
   （否则消费者停摆时队首往往就是唯一可腾挪的槽位，腾挪将永不生效）；被驱逐槽位的
   pending 最新值随之降级——这是本策略允许的代价（`coalescible` 可丢，
   `ordered` 不应因它而被先丢）。
3. **兜底丢弃**：仍无空间 → 丢弃新事件（当前行为），
   按 `deferredDroppedByClass[class]++` / `deferredDroppedByType[type]++` 计数，
   日志沿用 `/64` 节流（`:964-968`）。

优先级：`critical > ordered > coalescible`，保证关键事件永远不因可合并事件而丢失。

#### 6.1.5 路由与兜底：critical 不进溢出队列

分类不只决定计数，还决定**入队路径**——这是"不丢"承诺的机制（仅靠 §6.1.4 的驱逐优先级
不足以支撑：若队列里已全是 ordered/critical，兜底步骤仍会丢新事件）：

| 类别 | 入队路径 | 满/超预算时的行为 |
|---|---|---|
| stream | 既有 `enqueueStreamEvent` 合并 | latest-wins，降级为更少更大的重绘 |
| critical | `enqueueNonStreamEvent`（`tryReserveEventQueueBytes(..., critical=true)`，可用 `chatRuntimeEventQueueCriticalReserve=64` 保留位）失败后 → 既有 `enqueueCriticalRuntimeEventEventually` 重试通道（`:937-959`，5ms 间隔重试，publisher 不阻塞） | **永不进入 `deferredQueue`**：`dropped_by_class[critical]` 恒为 0；`criticalPending`（`:941-951`）暴露在途计数与高水位 |
| ordered / coalescible | 200ms 预算失败 → `deferRuntimeEvent` | 按 §6.1.4 合并 / 驱逐 / 丢弃并计数 |

- **改动锚点**：`:1391` 的 `critical` 参数当前只由 `isCriticalSubagentLifecycleEvent` 决定，
  保留位实际只服务子代理终态；本方案把它改为由 `classifyChatRuntimeEvent` 驱动
  （PR-1 范围）。
- **顺序语义**：重试通道可能越过 deferred FIFO 中的 ordered 事件——与现状一致，
  `deferRuntimeEvent` 注释（`:976-979`）已声明该族可被超越；critical 多为控制面终态，
  消费端按终态权威处理。§6.1.7 需增加"critical 越过后 ordered 内部顺序仍保持"的用例。
- **有界性**：现有重试通道对子代理终态是"无限重试、不丢"（注释 `:952-957` 的既有取舍）；
  critical 集合扩大（`assistant_message` / `tool_finished` 等频次高于子代理终态）后，
  并发在途上限是否需要 `chatRuntimeCriticalRetryLimit` 属于决策项（§10.1 第 6 条），
  未决前按现状实现并在 `/debug` 暴露 `criticalPending` 峰值。
- **EndRun**：退出前除等 deferred 积压（`chatRuntimeDeferredDrainBudget`）外，
  还需在同一预算内等 `criticalPending` 归零；若预算内未归零，**不阻塞超时**，
  改为记录 `critical_at_shutdown` 计数并置 `degraded` 标记（拒绝静默丢失），
  负向用例见 §8.4 第 4 条。

#### 6.1.6 不变式与代价

- 仍**非阻塞**：新增操作都在 `deferredMu` 临界区内做有界比较/替换（索引查找 O(1)，
  尾部驱逐最坏 O(队列长度=512) 且仅在溢出时发生）。
- 仍**有界**：`deferredIndex` 条目数 ≤ 队列条数；`deferredDroppedByType` 为固定容量 top-N。
- **不改变顺序**：只替换槽位内容与驱逐 coalescible，队列内 ordered 之间不会相互超越；
  critical 不进队列，跨类投递时可能越过仍滞留在队列中的 ordered 事件（渲染按事件时间戳，
  见 §8.4）。
- **可回退（三档开关）**：按仓库既有 feature flag 范式（`AICLI_SCENE_PRESENTER`，`:311-321`）
  增加 `AICLI_EVENT_BRIDGE_CLASSIFY=off|observe|enforce`：
  `off` 完全回到当前行为；`observe` 只分类计数、不改投递行为（PR-0 默认）；
  `enforce` 启用就地合并与驱逐（PR-1 之后默认）。异常时降档即可，无需回滚代码。

#### 6.1.7 测试

- `TestChatRuntimeEvents_CoalescesUsageAndStatusInDeferredQueue`
  （同 key 多次入队 → 队列只增 1 条，内容为最后一次）
- `TestChatRuntimeEvents_EvictsCoalescibleBeforeDroppingOrdered`
- `TestChatRuntimeEvents_CriticalEventsNeverDropUnderOverflow`
  （持续灌入 5s，断言 critical 全到达，dropped 只落在 coalescible）
- `TestChatRuntimeEvents_DeferredStatsExposeClassCounters`
- `TestChatRuntimeEvents_AssistantTerminalNotDroppedAfterDeltaPurge`
  （队列打满时投递 `assistant_message`：断言终稿到达，或至少断言"增量未被单方面丢弃"）
- `TestChatRuntimeEvents_CriticalRetryDoesNotEnterDeferredQueue`
  （灌满 deferred 队列后投递 critical：断言队列中不含 critical、`criticalPending` 最终回落、
  ordered 之间的相对顺序不变）
- `TestChatRuntimeEvents_InFlightSlotPromotesPendingValue`
  （race 修复专属：队首在途时同 key 新值挂 pending，投递成功后原位续投；只占 1 个槽位、零竞争）
- 回归：`chat_runtime_events_deferred_test.go`（FIFO/上限/排水，已改用 ordered 类事件）、
  `chat_runtime_events_critical_lifecycle_test.go`（保留位，填充事件已改用 non-critical）、
  `chat_runtime_events_ordering_test.go` 必须保持通过。

### 6.2 P0-2 消费端吞吐与降级可见（治本）

**落点**：`backend/cmd/aicli/ui/controller.go`（`Run` :423、`Stats` :718）、
`backend/cmd/aicli/ui/renderengine/frame_pump.go`、`backend/cmd/aicli/ui/terminal_session_*`。

1. **批处理 flush（不改 revision 语义）**
   - `Run()` 处理完一条 action 后，**非阻塞**地再取若干条就绪 action 连续 apply，
     整批结束后只发一次 `FlushEffect`（批内去重相同 `DirtyFlags`）。
   - 逐 action 的 revision 递增语义保持不变，避免破坏既有测试与 IR 不变式；
     变的只是物理帧数量。
   - 无界等待禁止（§8.3）：批内取用必须是 `select default` 语义。
2. **成本可度量（先量后调）**
   - `ControllerStats` 增加：`ReducerNanos`、`FlushCount`、`PostWaitNanos`（bridge 投递等待时长直方图）、
     `BatchSizeP95`。
   - 终端写侧增加：`bytes_written`、`writes_total`、`write_latency_p95_ms`，暴露在
     `/debug/chat/status` 的 `app_state` 区块（只读、无锁化采样，不得引入新的阻塞点）。
   - 基准测试沿用既有范式：`backend/cmd/aicli/ui/history_hot_path_bench_test.go`。
3. **降级对用户可见（G4）**
   - TUI 状态行在 `dropped>0`（当前 run 内）时显示降级提示，例如
     `⚠ events degraded: merged=430 dropped=12`；`/debug` 仍是完整真相。
   - 依据：目前用户完全看不到丢事件，这是"看起来卡住"无法自证的核心原因。
4. **消费端容量复审（数据驱动）**
   - 仅在 §6.2 第 2 条（成本可度量）证明消费侧等待是主瓶颈时，才评估提高 `MailboxSize`
     或为桥接路径改用 `PostDeferred`（注意 §8.2 的高水位降级约束，见 §6.5）。

### 6.3 P1-1 诊断窗口化与 observe 分类（消除误导）

**落点 A：executor 判决去累计化**（`backend/cmd/aicli/ui/terminal_session_executor.go:323-325`）

- 新增窗口判决：基于保留窗口（`Entries`，如最近 64 条 / 最近 60s）计算
  `WindowDiagnosis ∈ {idle, healthy, backoff_engaged, backoff_engaged_handing_off, dead_guard}`。
- 既有 `Diagnosis` 字段**保留**并明确语义为 `since_start`；两者同时在 `/debug/chat/status`
  的 `executor` 区块输出，纯文本模式标注 `CURRENT` 与 `SINCE START`。
- 测试改写：`terminal_session_executor_test.go:1600-1606` 的断言迁移到 `WindowDiagnosis`，
  并为"历史发生过、当前健康"补一条回归用例。

**落点 B：observe 事件分类**（`backend/internal/runtimeobserve/collector.go:225-260`）

- 把 `UnknownDropped` 拆成两类：
  - `filtered_events_by_type`（top-N）：事件类型**已知**（存在于 `runtimechat` 常量或
    `web_schema.go:76-111` 映射表），仅因不在 v1 白名单而过滤；
  - `unknown_events_dropped`：真正未识别的类型（保持异常语义）。
- 快照新增 `runtime.filtered_by_type`（`model.go:119-120` 旁），
  `unknown_events_dropped` 只统计后者。
- 评估把高频且已脱敏的类型纳入白名单：`cache_request_finished`、`dynamic_status`、
  `compact_*`、`approval_*`、`question_*`（payload 已由 `payloadAllowKeys` 白名单字段化，
  `projector.go:71+`）。
- 回归：`runtimeobserve_test.go:202` 的 unknown 语义用例保留；
  新增 `TestObserveCollector_ClassifiesKnownNonAllowlistedAsFiltered`。

**落点 C（可选）**：observe SSE 缺口（`internal/runtimeobserve/service.go:120-121` 未开放流），
在 A/B 完成后评估，避免与本次加固耦合。

### 6.4 P1-2 单轮预算与软着陆（降低过载持续时间）

**最小 recon 结论（2026-09-14，已确认接入点）**：

| 维度 | 计数/执行点 | 现状 |
|---|---|---|
| steps | `internal/agent/loop.go` 主循环 `for step := 1; !stepExceedsLimit(loop.config.MaxSteps, step); step++`；退出段置 `LimitReached/StepLimit` | 硬边界已存在；**原先不写 `LimitReason`**（只有 stop-failure hook 带 `step_limit`） |
| wall clock | `loop.go` 把 `MaxRunDuration` 转成 `agentWithTimeoutCause(..., errReActRunTimeout)`；defer 内置 `LimitReason="run_timeout"` | 硬边界已存在，无水位提示 |
| tokens | `loopRunOptions.BudgetTokens` + `remainingBudget`（`think()` 每次拿回 usage 后扣减，压缩旁路同样扣减） | 硬边界原先只置 `Success=false` + `Error`，**不置 `LimitReached/LimitReason`，也不写回历史** |
| 时间源 | `types.Duration{Start,End}` / `GetDuration()`（`internal/types/common.go:5-17`） | 可直接复用 |
| 注入通道 | `agent.SystemReminder`（`internal/agent/system_reminder.go`）+ `loop.emitRuntimeEvent(EventSystemReminderInjected, ...)` | 复用，新增 kind `turn_budget` |

**预算三项的现有配置来源（无需新增配置）**：`maxSteps` = `agent.Config.MaxSteps`
（主 chat 路径 `internal/chat/actor.go:173` 生效）；`max_wall_clock` = `agent.Config.MaxRunDuration`
（同处 :175，CLI `--timeout`）；`max_tokens_per_turn` = `loopRunOptions.BudgetTokens`
（子代理任务 `task.BudgetTokens` 已接线；主 chat 的 `--budget-tokens` 目前只写进 `ChatOptions`
未下传，见下"待落地"）。

**已落地（PR-4 落点 A）**：

1. `backend/internal/agent/turn_budget.go`：单一事实源 `TurnBudgetSpec/TurnBudgetUsage/TurnBudgetState`
   + `EvaluateTurnBudget`（80% 收尾水位、100% 硬水位、按维度给 `HardReasons/SoftReasons`）
   + `FormatTurnBudgetDuration` + `TokensSpentFromBudget`（token 维度与执行路径同口径）
   + `TurnBudgetSoftLandingMessage` / `TurnBudgetHardStopMessage`；
   进度行格式与本节口径一致：`turn budget: step 240/300 · 32m/40m · tokens 62%`（只列已配置维度）。
2. `loop.go`：每步开头用同一份 spec/usage 评估；**恰好一次**软着陆注入
   （durable 的 `system_reminder.injected`，kind=`turn_budget`，payload 附带
   `turn_budget_level/line/ratio/reasons`），并持久化到会话历史；token 硬边界改为
   优雅收尾（`LimitReached=true`、`LimitReason="turn_budget"`、用户可见续跑文案写回 assistant
   历史 + `persistBuilderHistory` + stop-failure hook），不再静默截断。
   判决已是 hard 时**不再注入**收尾提示（模型读不到就只污染历史）。
3. `Result` 只增字段：`turn_budget_level` / `turn_budget`（进度行）/ `turn_budget_soft_cue_injected`；
   在既有 defer 里对**每条退出路径**（成功、步数上限、token 上限、超时、取消）统一回填，
   宿主无需二次推导水位。步数上限退出补 `LimitReason="step_limit"`。

**验证（2026-09-14，backend 模块）**：`gofmt -l` 对改动文件为空；`go build ./...` 退出码 0；
`go test -count=1 ./internal/agent/` ok 1.769s（含纯逻辑 8 项 + 循环级 3 项）；
`go test -race -count=1 -timeout 600s -run 'TurnBudget|TokenBudget|StepLimit' ./internal/agent/` ok 3.873s；
回归 `internal/chat` ok 14.878s、`internal/runtimeobserve` ok 0.822s、`cmd/aicli/commands` ok 79.475s。

**已落地（PR-4 落点 B：TUI 可见进度）**：

4. `backend/cmd/aicli/commands/chat_runtime_events.go`：桥新增 `system_reminder.injected`
   消费（字符串常量，不导入 `agent` 包以保持单向依赖），只镜像 `kind=turn_budget` 且带
   非空 `turn_budget_line` 的**主会话当前 turn**事件，落到 `atomic.Pointer` 快照
   （`TurnBudgetSnapshot()` 无锁读，与降级摘要同一并发手法）；`BeginRunKind` 清空。
5. `backend/cmd/aicli/commands/chat_interaction.go`：`appendStatusHintsLocked` 统一追加
   降级提示与预算水位；水位只在 `StateText` 非空时附在行尾
   （`◦ Running … · turn budget: step 240/300 · 32m/40m · tokens 62%`），
   状态切换与秒级 tick 两个渲染入口共用，空闲行不显示。
6. `backend/internal/agent/loop.go`：事件构造处**不再**二次注入 `turn_id`——`emitRuntimeEvent`
   已按 `loop.turnID` 统一盖章；新增循环级回归 `TestReActLoop_Run_TurnBudgetReminderEventCarriesTurnIdentity`
   走真实 `EventBus` 验证 `kind`/`turn_id`/`durable` 与通告时刻行文本，
   另钉住"事件行=通告时刻、`Result.TurnBudgetLine`=终局"这一取样差。

**落点 B 验证（2026-09-14，backend 模块）**：
`go test -race -count=1 -run 'TurnBudget|TokenBudget|StepLimit' ./internal/agent/` ok 3.886s；
`go test -count=1 -run 'TurnBudget|StatusHints|IgnoresNonBudgetReminders' -v ./cmd/aicli/commands/`
7/7 PASS、ok 0.196s（含"跨 turn 忽略""非主会话忽略""`BeginRunKind` 清空""空闲行不加水位"
与"降级提示 + 水位顺序"）。

**语义边界（有意为之）**：软着陆**不**派发 `EventCheckpointCreated`。该事件目前只由
checkpoint 管理器在真实快照后派发（`approved_tool.go:255`、`loop.go:2621`），在无水印快照的
情况下复用同名事件会让宿主误判"存在可回滚点"；可续跑性由 durable 收尾提醒 + 会话历史保证。

**已落地（2026-09-14，落点 C）**：

- `/debug/chat/status` 的 turn 级指标：`runtimeobserve` 白名单里的
  `agent.turn.started/finished`（`internal/runtimeobserve/model.go:39-40`）现在有真实 emitter，
  payload 带 `trace_id` / `step` / `elapsed_ms` / `budget_level` / `budget_ratio`；
  `appendChatDebugTurnMetricsLines`（`chat_debug_document.go:255-257`）把预算水位与最近一轮
  终局水位/耗时渲染进 `/debug` 区块。
- 主 chat 路径的 token 预算接线：`--budget-tokens` → `chatCommandOptions.BudgetTokens` →
  `ChatSession.TurnBudgetTokens` → `buildLocalChatLoopConfig` → `LoopReActConfig.TurnBudgetTokens`
  → `loop.run` 的缺省回填（`loopRunOptions.BudgetTokens` 显式值优先）。`--budget-tokens` 之前
  只存在于 `/agents routing test` 的路由测试路径，主 chat 一直是不限额。

**已落地（2026-09-14，第 5 条：prompt cache 熔断 + `UPSTREAM_INVALID_RESPONSE` 聚合）**：

1. `backend/internal/agent/prompt_cache_breaker.go`（新增）：run 级熔断器
   `PromptCacheBreaker`。同一 `prompt_fingerprint` **连续失败**达到阈值
   （缺省 3）→ 打开短期熔断窗口（缺省 2m），窗口内该指纹的下一次请求先退避
   （指数退避 2s→4s→…，上限 30s）；成功即清零（`ObserveSuccess`），冷却到期
   重新累积、再次跨阈值时退避升级（`trips` 递增）。`Tripped` 是**边沿**信号，
   窗口内继续失败只升级退避、不重复通告。全部入口带锁且 nil 接收者安全
   （未接线路径退化为观测空操作）。
2. `loop.go` 接线三处，**不改变 llm 包既有的有界重试语义**：
   `run()` 建实例并 `WithPromptCacheBreaker(currentCtx, …)` 下发（不落 loop 结构体，
   run 之间不共享计数）；`think()` 在盖章 `prompt_fingerprint` 之后、`llm.request.started`
   之前做"窗口内退避一次"（`llm.prompt_cache.backoff_applied`，退避可被 ctx 取消，
   不做伪等待，符合 §8.1）；失败路径记录连续失败并在跨阈值时发一次
   `llm.prompt_cache.breaker_tripped`，成功路径复位。
3. `UPSTREAM_INVALID_RESPONSE` 聚合：`runtimeRetryEventReporter` 在**源头**抑制
   第 2..N 条同类 `llm.retry`（首次仍外发，状态行还能看到"正在重试"），run 退出时
   由 `emitAggregatedRetryReport` 一次性发 `llm.retry.aggregated`
   （`severity=warn`、`error_code`、`count`、`prompt_fingerprints`、`prompt_fingerprint_count`）；
   现场 6 次 → 1 条首次 + 1 条汇总。其他错误码保持逐次上报不变。
   指纹随 `llm.retry` 一并外发，使"同一条 prompt 反复失败"在事件流里可归因。
4. `Result` 只增字段：`prompt_cache_breaker_trips`、`upstream_invalid_response_events`
   （同一次 run 的同一份状态，宿主/`/debug` 无需从事件流二次累加）。

**第 5 条验证（2026-09-14，backend 模块）**：`gofmt -l` 对改动文件为空；
`go build ./...` 退出码 0；`go vet ./internal/agent/` 退出码 0；
`go test -count=1 -run 'PromptCacheBreaker|PromptFingerprint|RetryReporterSuppresses|EmitAggregatedRetryReport' ./internal/agent/`
ok（8 项：阈值边沿/退避升级、每窗口至多一次退避 + 冷却重新武装、聚合只针对
`UPSTREAM_INVALID_RESPONSE`、成功清零 + nil 接收者安全、退避可取消、
指纹提取、6 次重试仅 1 条状态行、汇总上报只发一次）；
`go test -race -count=1 -timeout 600s -run 'PromptCacheBreaker' ./internal/agent/` ok 1.258s；
回归 `go test -count=1 ./internal/agent/` ok 1.947s、`./internal/llm/` ok 22.010s、
`./internal/chat/` ok 15.352s、`./cmd/aicli/commands/` ok 83.793s。

以下为原设计意图与验收口径，作为已落地条目的验收依据（第 1–5 条均已落地，按上文标注为准）：

1. **预算**：`max_steps` / `max_wall_clock` / `max_tokens_per_turn` 三项可配置；
   默认值建议取当前现场长轮的 60–70% 水位（如 steps≈300、wall-clock≈40min）。
2. **软着陆（80% 水位）**：向模型注入收尾指令、生成 checkpoint
   （复用 `EventCheckpointCreated`）、TUI 显示 `turn budget: step 240/300 · 32m/40m · tokens 62%`。
3. **硬边界（100%）**：优雅结束当前 turn（写 `session.end`、保留 transcript、提示用户续跑），
   禁止静默截断或丢弃未持久化内容。
4. **可观测性**：把 turn 级指标从 `*.debug.log` 提升到 `/debug/chat/status` 与 observe
   （`agent.turn.*` 事件补充 `step` / `elapsed_ms` / `budget_*`）。
5. **prompt cache 熔断**：同一 `prompt_fingerprint` 连续失败达到阈值 → 短期熔断 + 退避；
   `UPSTREAM_INVALID_RESPONSE` 聚合为 WARN 计数上报，不逐条刷日志（现场 6 次）。
6. **验收**：注入长任务脚本，断言 80% 提示出现、100% 软着陆、TUI 可见进度、
   session 状态与 transcript 完整。
   **验收结果（2026-09-14）**：`scripts/test-aicli-turn-budget-e2e.ps1` 用
   SequenceLLMProvider 注入脚本化长任务（无网络），3/3 组 41 项 PASS：
   - 80% 提示只注入一次（`...InjectsTurnBudgetSoftLandingOnce`，并断言提示进入第二次模型请求）；
   - 100% 硬边界优雅收尾：不再调用模型、收尾文案写入 `PersistHistory`（`...TokenBudgetHardStopIsGraceful`）；
   - TUI 可见进度：状态行出现 `turn budget: …`，与降级提示顺序共存；
   - `/debug` turn 区块与 `--budget-tokens` 全链路接线、observe turn 指标聚合；
   - 第 5 条熔断/聚合（阈值、退避、`UPSTREAM_INVALID_RESPONSE` 只聚合该类错误）。
   `session_end` 由 chat actor 在每个 turn 结束时统一发射（`internal/chat/actor.go`），
   预算用例未单独断言该事件；transcript 完整性以持久化回写断言为准。
   真实终端渲染由 §8.2 的两个 Windows Terminal E2E 覆盖（需交互桌面，本环境未复跑）。

### 6.5 P2 残余风险收敛（对齐既有加固文档 §8）

| 条目 | 现状 | 本方案动作 |
|---|---|---|
| §8.2 `PostDeferred` 无硬上限 | 队列可暂时超过 mailbox 容量，仅有监控 | **部分落地（2026-09-14）**：同 key 入队前合并 + `PeakPending` 暴露到 `/debug` 已实现；达到高水位（如 80% cap）时 coalescable 降级 `TryPost` **未实现**（P2，另行排期） |
| §8.1 `io.Writer` 不可取消 | 超时后遗留废弃写 goroutine | **不在本次范围**；保持 watchdog 诊断（超时日志 + goroutine dump + `terminalWritesAbandoned`）。后续若要根治，走可取消写通道（Windows overlapped/CancelIoEx 或专用写线程 + 可关闭句柄），禁止伪取消 |
| §8.3 无界 `WaitIdle` | 已有纪律，靠评审维持 | 新增代码一律 `WaitIdleTimeout` / 事件驱动；`Run` 批处理不得引入无界等待 |
| §8.4 drain 超时后同 epoch 迟到事件 | 语义由测试固化 | 暂不改；若需更严格隔离，增加 `finalizedRunEpoch` 并显式放行 ambient（团队编排等）事件 |
| **姊妹队列：web SSE 丢帧** | `/web/` 每客户端 256 帧队列，满即丢帧，仅累加 `Dropped()`（`web_handlers.go:131-134`、`:190-196`、`:256-269`），**客户端不可见** | 与 §6.1 同源的"分级 + 入队前合并"下沉到 `chatWebSSEStream.enqueue`；把 `Dropped()` 暴露到 `/web/api/status` 与 SSE `degraded` 事件（独立 PR，P2） |

**建议新增：稳态 watchdog（本次取证的直接产物）**

- 现状：只有启动期兜底（`commands/chat_startup_timing.go:91-101`：arm 后 90s 未到达 `ready` 即 `runtime.Stack` 全量 goroutine dump）。
- 提议：扩展为**稳态**触发——turn 进行中若 X 秒（如 60s）无 LLM 事件推进且无渲染交付，
  自动把 goroutine dump + `/debug/chat/status` JSON 落盘到 chat-log，并在 TUI 显示告警。
- 价值：把"看起来卡住"变成"有证据的自诊断"，本次 3 分钟渲染冻结正是缺少该机制才需要人工取证。

**建议新增：历史投影失效归因分类**

- 现状：只看到累计 `invalidated=2130` 与 `scrollback_reset_count=2`，无法判断构成。
- 提议：在 `transcriptReplacementInvalidatesAckedHistory`（`ui/controller_state.go:619`）等失效判定点
  增加原因分类计数（`touchesAckedPrefix` / `geometry` / `lease` / `drain-timeout`），
  暴露到 `/debug/chat/status` 的 `projection` 区块；有数据后再评估是否需要
  "仅重排尾部"的增量对账。

### 6.6 备选方案与否决理由

| 备选 | 为何不采用 |
|---|---|
| 放大 `chatRuntimeDeferredEventLimit` / `ByteLimit` | 只推迟溢出时间点；事件产生速率与消费速率的差值不变，长轮下仍会打满（`pending` 曾反复逼近上限），且直接抬高内存驻留 |
| 放大 `chatRuntimeNonStreamEnqueueBudget`（改为等待 UI 容量） | 违反 §5.2 第 1 条；历史事故已证明会把子代理 LLM 调用拉长到 ~112s、让活跃 turn 看似卡住数十分钟 |
| 由 publisher 侧提前降频（丢弃低价值事件） | 无分类依据时等于把"静默丢弃"提前到发布端，可观测性更差；本方案 `observe` 档正是为了先拿到分类数据 |
| 只做消费端提速（P0-2） | 消费端提速有上限（终端写带宽、reducer 成本），长轮突发可达 130/s；单靠提速会把压力转嫁为更高的 `pending` 峰值 |
| 让 UI actor 直接持有有界队列（去掉 bridge 中间层） | 架构级重构，破坏既有 epoch / 顺序 / 关键事件语义，与 §5.3 不变式冲突 |

---

## 7. 实施拆分

按"每步可独立验证、可独立回滚"拆成 5 个 PR（**PR-0 为只观测、不改行为的先导步**）：

### PR-0（先导，只观测）分类计数落地

- 改动：只加 `classifyChatRuntimeEvent` + `dropped_by_class` / `dropped_by_type` 计数与
  `/debug` 透出，**不改任何投递、合并、驱逐行为**（`AICLI_EVENT_BRIDGE_CLASSIFY=observe`）。
- 目的：回答"35,050 次丢弃里 coalescible 占多少、ordered 占多少"，为 PR-1 的收益预估与
  §8.3 的目标值提供基线；若 coalescible 占比很低，则应优先推进 PR-2（消费端吞吐）
  而非 PR-1。
- 验收：`off` 与 `observe` 两种模式下既有测试全绿、行为无差异（用既有
  `chat_runtime_events_deferred_test.go` 断言队列/丢弃计数不变即可证明）。

### PR-1（P0-1）事件桥分级 + 就地合并 + 分类计数

- 改动：`backend/cmd/aicli/commands/chat_runtime_events.go`
  （`classifyChatRuntimeEvent`、`chatEventCoalesceKey`、`deferRuntimeEvent`、
  `runDeferredQueue` 的索引维护、`deferredQueueStats` 扩展）；
  `backend/cmd/aicli/commands/chat_debug_document.go:523-527`、
  `chat_debug_display_http.go:397-401`（新增字段透出）。
- 测试：§6.1.7 六项 + 既有 deferred/critical/ordering 回归。
- 验收：新增溢出风暴测试中 critical 零丢失（由 §6.1.5 的路由保证：
  critical 不进 `deferredQueue`，`dropped_by_class[critical]` 恒为 0）；`dropped_by_class` 可见。

### PR-2（P0-2）消费端批处理 + 成本指标 + 降级可见

- 改动：`ui/controller.go`（批处理 flush、`ControllerStats` 扩展）、
  `ui/renderengine/frame_pump.go`（批内帧去重）、状态行降级提示。
- 测试：`ui/controller_test.go` 增批处理与"批内仅一次 flush"用例；
  基准 `history_hot_path_bench_test.go` 对比前后。
- 验收：`ReducerNanos`/`FlushCount`/`PostWaitNanos` 可见；批处理后等负载下 `pending` 峰值下降。

### PR-3（P1-1）诊断窗口化 + observe 分类

- 改动：`ui/terminal_session_executor.go`（`WindowDiagnosis`）、
  `internal/runtimeobserve/collector.go`（filtered vs unknown）、`model.go`（新字段）。
- 测试：更新 executor 测试；新增 observe 分类用例。
- 验收：`unknown_events_dropped` 增速回落；`executor` 区块同时给出 CURRENT 与 SINCE START。

### PR-4（P1-2，可独立排期）单轮预算与软着陆

- 前置：一次最小 recon 定位 agent 循环的 step/token 计数点。
- 改动：预算判定 + 软着陆注入 + turn 指标暴露 + cache 熔断。
- 验收：注入式长任务脚本通过。

### 依赖与工作量估算

| PR | 依赖 | 估算 | 说明 |
|---|---|---|---|
| PR-0 | 无 | 0.5–1 天 | 只加计数与透出，先拿到分类分布 |
| PR-1 | PR-0 的分类数据 | 2–3 天 | 合并/驱逐 + 5 项单测 + ordering 回归 |
| PR-2 | 无（可与 PR-1 并行） | 2–3 天 | 批处理 flush + 指标 + 基准对比 |
| PR-3 | 无 | 1–2 天 | executor 窗口化 + observe 分类 |
| PR-4 | 一次最小 recon | 3–5 天 | 预算/软着陆/熔断 + E2E 脚本 |

另需同步文档：`docs/aicli/debug-chat-status.md`（新增字段契约），随 PR-1/PR-3 一并更新。

---

## 8. 验证与验收

### 8.1 单元与竞态测试

```powershell
Set-Location E:\projects\ai\ai-agent-runtime\backend
go test -race -count=1 ./cmd/aicli/commands/ -run 'ChatRuntimeEvents'
go test -race -count=1 ./cmd/aicli/ui/... -run 'UIController|ExecutorDiag|FramePump'
go test -race -count=1 ./internal/runtimeobserve/
```

实测结果（2026-09-14，Windows + Go 1.25）：

- `-run 'ChatRuntimeEvents'`：修复前报 **2 处 `DATA RACE`**
  （`chat_runtime_events.go:1262/1263` ↔ `:1401`，`TestChatRuntimeEvents_CoalescesUsageAndStatusInDeferredQueue`
  内复现，见 §0/§6.1.3）；按"在途槽位 + pending 续投"修复后，同一命令零竞争报告（`ok 15.6s`）。
- 定向回归（新用例 10 项 + deferred 既有用例 6 项 + 保留位/终态/排水用例 + 5s 溢出风暴）
  在 `-race` 下全绿（9.4s，2026-09-14）。
- 整包回归 `go test -count=1 ./cmd/aicli/commands/`：`ok`（78.4s ~ 87.9s，含全部渲染/actor/诊断用例）；
  `./internal/runtimeobserve/` 与 `./cmd/aicli/ui/...` 竞态测试同样全绿。

### 8.2 端到端

```powershell
Set-Location E:\projects\ai\ai-agent-runtime
pwsh -File scripts/test-aicli-opencode-windows-terminal-e2e.ps1   # 统一渲染 + marker 恰好一次
pwsh -File scripts/test-aicli-windows-terminal-e2e.ps1            # 终端渲染基线
```

**受控注入式验收（2026-09-14 实测，无网络）**：

```powershell
pwsh -File scripts/test-aicli-turn-budget-e2e.ps1
# -> agent/turn-budget PASS(27) | commands/tui+bridge+debug PASS(11) | runtimeobserve/turn-metrics PASS(3)
# -> RESULT: PASS (3/3 groups)；原始日志 artifacts/turn-budget-e2e-20260914-130126.log（artifacts/ 已 gitignore）
```

该脚本把"长任务"以脚本化 LLM 响应注入真实 ReActLoop / 事件桥 / 状态行 / `/debug` 渲染链，
逐项覆盖 §6.4 第 6 条（见 §6.4 验收结果）。两个 Windows Terminal E2E 需要交互桌面与
provider 凭据，本环境未复跑，保持为**待执行的手工门禁**，不作为本次收敛证据。

### 8.3 现场指标看板（改动前后对比）

| 指标 | 现场基线（2026-09-14） | 目标 |
|---|---|---|
| `scene.deferred_queue.dropped` 增速 | 16–130/s | ≈0（仅异常态偶发） |
| `scene.deferred_queue.pending` 峰值 | 接近上限（512） | < 50% 上限 |
| `dropped_by_class[class]` | 不可观测（未分类） | 有分类计数；`critical` 恒为 0（§6.1.5 路由）、`coalescible` < 1/min；`criticalPending` 峰值与 `critical_at_shutdown` 可见 |
| `unknown_events_dropped` 增速 | ~32/s | 个位数/分钟 |
| `last_sequence` 冻结时长 | 曾 3min | < 5s |
| `executor` 判决 | 永久 `backoff_engaged_handing_off` | `WindowDiagnosis` 实时反映当前状态 |
| 单轮 step / 时长 | 500+ 步 / >70min | 有预算与软着陆，TUI 可见 |
| `/web/` SSE 丢帧 `Dropped()` | 不可观测（仅内部计数） | 暴露到 `/web/api/status`，>0 时可被客户端/看板发现 |

**验收判定**：连续两个 30 分钟观测窗口满足——`dropped` 增速 < 1/min、
`unknown_events_dropped` 增速 < 10/min、`pending` 峰值 < 256、
`WindowDiagnosis` 在健康期返回 `healthy`/`idle`。
目标值在 PR-0 落地后按实测分类分布复核一次（见 §7 依赖与工作量估算）。

**复核工具（2026-09-14 新增）**：

```powershell
pwsh -File scripts/measure-aicli-event-bridge-metrics.ps1 -Endpoint http://127.0.0.1:64751 -WindowSeconds 1800
```

脚本按上表目标计算 `dropped`/`unknown_events_dropped` 增速（首末采样差 / 窗口分钟）、
`pending` 峰值，并校对 `executor.window_diagnosis`（CURRENT 口径）；达标 PASS、未达标 FAIL、
无活动会话/端点不可达 exit 2（不产生假阴性结论）。已用 healthy / degraded / 不可达三类
mock 端点自测三条路径（PASS / FAIL / exit 2 均正确）。
**现场 30 分钟 ×2 窗口复测待有活动会话时执行**——2026-09-14 核查时 `127.0.0.1:64751`
已关闭，故本次不回填该表。

### 8.4 关键负向用例（必须通过）

- 持续灌入 5s 的溢出风暴中，`subagent.*` 终态 / `session.end` / `tool_finished` **零丢失**。
- UI actor 完全停摆（测试桩）时，publisher 不得被阻塞超过既有预算
  （`chatRuntimeNonStreamEnqueueBudget` / `uiActionPostBudget` 语义不变）。
- `EndRun` 在溢出队列仍有积压时，仍在 `chatRuntimeDeferredDrainBudget` 内返回。
- `EndRun` 时 critical 重试在途未归零：仍在同一预算内返回，且必须产生 `critical_at_shutdown`
  计数与 `degraded` 标记（不得静默丢失）。
- 在途槽位窗口：worker 投递过程中到达的同 key 最新值必须被续投（不得另开槽位、不得丢失），
  且该窗口内 race 闸门零报告（`TestChatRuntimeEvents_InFlightSlotPromotesPendingValue`）。
- 跨类顺序不是契约：critical 走保留位 + 重试通道，可以越过仍滞留在溢出队列中的 ordered 事件；
  渲染按事件时间戳处理，不依赖跨类到达顺序（`TestDeferredRuntimeEventsPreserveOrder` 只约束
  ordered 类内部 FIFO）。

### 8.5 交付边界、提交勘误与工具自测（2026-09-14）

**提交勘误**：`e8c72afc`（"丢事件加固 PR-0..PR-3"）只提交了消费方与 PR-0..PR-2 的契约；
`chatRunKind`（PR-1 degraded 清空 / PR-3 落点 B 判读）与 executor 窗口化字段（PR-3 落点 A）
留在工作区未提交，导致该提交与当时 HEAD（`f043487f`）均**无法编译**
（`undefined: chatRunKind`、`diag.WindowDiagnosis undefined` 等）。补交提交 `591496d7`
（2026-09-14）补齐落点 A + PR-4 + 第 5 条后，提交树恢复可构建：worktree 实测
`go build ./cmd/aicli/... ./internal/agent/... ./internal/runtimeobserve/... ./internal/chat/...`
EXIT 0（`go build ./...` 需 `internal/webui` 的 dist 产物，worktree 中缺该 gitignored 构建产物）。
`e8c72afc` 的提交信息范围以 `591496d7` 的勘误说明为准；该提交位于 15 个提交的链中间，
未做历史重写（改写会变更全部后代哈希）。

**验收工具**：

| 工具 | 用途 | 自测/实测结果 |
|---|---|---|
| `scripts/test-aicli-turn-budget-e2e.ps1` | §6.4 第 6 条受控注入式验收（stub provider，无网络） | 3/3 组 41 项 PASS（2026-09-14） |
| `scripts/measure-aicli-event-bridge-metrics.ps1` | §8.3 现场指标复核（dropped/unknown 增速、pending 峰值、window_diagnosis） | mock 三类端点：healthy PASS、degraded FAIL、不可达 exit 2 |

**本次未收敛/未复测项（如实标注，勿读作已完成）**：

- §8.2 两个 Windows Terminal E2E：需要交互桌面与 provider 凭据，本环境未复跑；
- §8.3 现场 30 分钟 ×2 观测窗口：核查时无活动会话（`127.0.0.1:64751` 已关闭），
  工具已就绪，待现场有活动会话时执行并回填表格；
- §6.5 P2 项（web SSE 丢帧暴露、稳态 watchdog、投影失效归因分类、`PostDeferred` 高水位降级）：
  未实现，另行排期（§0 已按此口径标注）；
- §10.1 待决策第 4/5/6 条与 §10.2 未验证假设：保持开放。

### 8.6 现场二次取证：reasoning 别名归流与终态清理边界（2026-09-14）

**取证对象**：活动会话 `session_20260913210527_7mj5o08M`（debug 平面 `http://127.0.0.1:58227`），
按用户约束**未重启进程**，因此下表计数是该会话**修复前**二进制的累计值，只用于定位根因，
不得当作修复后回归指标。

| 指标 | 值（修复前二进制） | 判读 |
|---|---|---|
| `Dropped By Class` | `coalescible=228 ordered=52255` | 队列压力几乎全部来自 ordered 家族 |
| `Dropped By Type` | `assistant.reasoning=52130`（占 ordered 丢弃 99.8%） | 本地 ReAct loop 的推理流在 ordered 家族被整批丢弃 |
| `Event Log` | `recorded=665 → 3376`，`failures=14783` 不再增长 | `.events` 目录惰性创建修复生效，历史失败计数保留 |
| `Event Journal Drops` / `Delivery Journal Drops` | 5912 / 1222 | 渲染侧账本丢弃，仍在 §6.5 P2 排期内 |

**根因**：`internal/agent/loop.go:1711`（流式 append，`format=stream_delta`）与 `:2094`
（步内 `mode=replace`）发出的是点分隔别名 `assistant.reasoning`，而
`isMergeableStreamEvent`、`streamEventText`、`mergeStreamEvents` 只认下划线常量
（`assistant_reasoning`/`assistant_delta`）。别名因此降级为 ordered，在 deferred 队列压力下
被整体丢弃；而只把别名加进 `isMergeableStreamEvent` 而不补齐文本提取与合并分支，合并路径
取不到文本会把增量整条吞掉（半修复态）。

**修复（三处，均带回归测试）**：

1. `assistantStreamAliasType` 统一归一化总线别名，`isMergeableStreamEvent`/`streamEventText`/
   `mergeStreamEvents` 共用，消除"归了类但提不到文本"的半修复态；
2. `dropPendingStreamsForTerminal` 只清理 assistant **文本**增量（`isAssistantTextStreamEvent`）：
   终稿快照取代的是自己的文本流，reasoning 属于另一个内容块，不受终稿取代；
3. 清理后只把**同一 turn/stream** 的 reasoning 尾部按 `chatStreamFlushBudget` 有界冲刷
   （`flushStreamTailBoundedLocked`），保证推理单元格仍先于 assistant 单元格落盘；其它
   turn/stream 的积压保持原语义留在 pending，不被本终态的预算代做时序决定
   （回归守卫：`TestAssistantTerminalDropsStalePendingAndEnqueues`、
   `TestChatRuntimeEvents_AssistantTerminalKeepsLateReasoningDeltas`）。

**A/B 证据（本机 worktree）**：

| 变体 | `TestLateReasoningAfterSuccessfulRequestBoundaryPrecedesAssistantFinal` |
|---|---|
| 原始（无别名归流） | `ok 0.314s` |
| 仅加别名、不补清理边界（半修复） | `FAIL 0.368s`（推理单元格消失，`cells=[assistant]`） |
| 完整修复（本批） | `ok`，并新增 `TestChatRuntimeEvents_AssistantTerminalKeepsLateReasoningDeltas` |

**过程勘误（避免把半修复读成已完成）**：第一版修复把 pending 全量按预算冲刷，导致
`TestAssistantTerminalDropsStalePendingAndEnqueues` 失败（终端替**别的 stream** 做了入队决定，
`pending after terminal = count 0, want only stream-2`）；收窄为"同 turn/stream 的 reasoning 才冲刷"
后全绿。该用例是这条边界的既有守卫，本次未改写其断言。

**边界（勿读作已完成）**：活动进程未重启，修复后的现场 30 分钟观测窗口未复测（§8.3 表格保持待回填）；
Web/TUI E2E 未复跑；`Event Journal Drops` 未收敛。

---

## 9. 风险、回滚与兼容性

| 风险 | 影响 | 缓解 |
|---|---|---|
| 合并改变统计口径（如 usage 只留最后一次） | 渲染层展示与预期不符 | 仅对声明 latest-wins 的类型合并；事件日志/会话日志仍完整；flag 可回退 |
| 驱逐策略误伤 | 关键信息缺失 | 只驱逐 `coalescible`；驱逐计数与类型可见；critical 永不参与驱逐 |
| 在途槽位被驱逐 | 该 coalescible 家族的最新值（pending）随槽位一起降级，UI 可能停在上一档 | 只在队列满且没有其它可腾挪槽位时发生；计入 `evicted` 且类型可见；latest-wins 语义本身允许丢弃旧值，且下一次同类事件会重新占位 |
| 批处理 flush 破坏既有帧语义 | 测试/交互回归 | 批内只去重 `FlushEffect`，逐 action revision 语义不变；分步提交 + 基准对比 |
| observe 分类收紧 | 告警阈值需同步 | 新字段只增；文档与看板同步更新语义 |
| 长轮预算过紧 | 正常任务被提前收尾 | 预算可配置，默认取现场水位的 60–70%；先软着陆后硬边界 |
| critical 重试通道在途无硬上限 | UI 长期停摆时并发重试（goroutine 与事件内存）增长；critical 集合扩大后比现状更易触发 | `criticalPending` 高水位与 `critical_at_shutdown` 暴露到 `/debug`；§10.1 第 6 条决定是否加 `chatRuntimeCriticalRetryLimit` 并显式降级告警 |

**回滚开关**：

- `AICLI_EVENT_BRIDGE_CLASSIFY=off|observe|enforce`：`off` 完全回到当前行为；
  `observe` 只计数不改行为；`enforce` 启用分级/合并/驱逐。异常时降档即可，无需回滚代码。
- UI 批处理以内部开关控制（默认 on），关闭后逐 action flush。
- 所有 `/debug/chat/status`、observe 变更**只增字段不改语义**，旧客户端不受影响。

---

## 10. 待决策与未验证假设（评审用）

### 10.1 待决策

1. ~~critical 集合是否包含 `tool_finished` / `tool_failed`（§6.1.1）~~ →
   **已决策（PR-1，2026-09-14）**：包含，并且必须包含总线真实别名 `tool.completed`
   （漏别名的后果见 §0）；保留位占用由 `critical_pending` / `critical_peak_pending` 观测。
2. ~~是否允许驱逐**已入队**的 coalescible 槽位（§6.1.4）~~ →
   **已决策（PR-1，2026-09-14）**：允许，且**在途槽位同样可驱逐**；
   若不允许，消费者停摆时队首就是唯一可腾挪槽位，腾挪将永不生效。代价见 §9 风险表。
3. ~~`AICLI_EVENT_BRIDGE_CLASSIFY` 在 PR-0 之后是否立即切 `enforce`，还是先观测一个迭代。~~ →
   **已决策（PR-1，2026-09-14）**：默认 `enforce`（未知取值也按 `enforce`，避免拼写错误静默降档）；
   需要观测或回滚时显式设 `observe` / `off`。
4. observe 白名单放宽的边界（§6.3）。
5. P1-2 预算默认值（§6.4）。
6. critical 重试通道是否设并发在途上限（`chatRuntimeCriticalRetryLimit`，如 128）：
   超限时显式降级告警（可能违反"绝不丢"承诺）还是沿用现状的无限重试（内存风险）；
   见 §6.1.5 / §9。

### 10.2 未验证假设（已知未知）

1. parity 35 行差异与丢事件的因果关系（§2.4）。
2. 历史投影 `invalidated=2130` 与丢事件的因果关系（§2.4）。
3. "事件日志/会话日志不受影响"目前只有代码注释依据，未实测（§2.4）。
4. P1-2 的 agent 循环接入点未取证（§6.4）。
5. 丢事件与"历史投影 pending token"是否存在直接贡献关系（现场 pending 已归零，
   建议在 PR-0 同期加一次采样验证）。

---

## 附录 A：取证命令备忘

```powershell
# 1) 当前溢出队列
curl.exe -s -m 20 "http://127.0.0.1:64751/debug/chat/status" |
  jq -c '.scene.deferred_queue'

# 2) 丢事件速率（对比两次采样）
1..2 | ForEach-Object {
  $d = (curl.exe -s -m 20 "http://127.0.0.1:64751/debug/chat/status" | jq -r '.scene.deferred_queue.dropped')
  "$(Get-Date -Format o) dropped=$d"
  Start-Sleep -Seconds 10
}

# 3) 日志侧 drop 计数（/64 节流打印）
Select-String -Path "$env:USERPROFILE\.aicli\chat-logs\*\*\*\*.debug.log" `
  -Pattern 'dropped_total=(\d+)' | Select-Object -Last 10

# 4) observe 自监控
curl.exe -s -m 20 "http://127.0.0.1:64751/api/runtime/observe/v1/snapshot" |
  jq -c '.data.runtime | {unknown_events_dropped, event_ingress_dropped, ring_current_bytes}'

# 5) goroutine 取证（卡住时）
curl.exe -s -m 25 -o "$env:TEMP\goro.txt" "http://127.0.0.1:64751/debug/pprof/goroutine?debug=2"
Select-String -Path "$env:TEMP\goro.txt" -Pattern '^goroutine ' | Measure-Object
```

## 附录 B：关键源码锚点索引

| 锚点 | 位置 | 用途 |
|---|---|---|
| `Handle` 分发 | `commands/chat_runtime_events.go:867` | 三类分支（stream / critical / 其它） |
| 非流式降级入口 | `chat_runtime_events.go:909-917` | 200ms 预算失败 → deferred |
| 关键事件通道 | `chat_runtime_events.go:884-894`, `:920-935` | 复用的分级先例 |
| 队列常量 | `chat_runtime_events.go:237-292` | 容量/预算/字节上限 |
| 溢出丢弃 | `chat_runtime_events.go:992-1004`，日志节流 `:964-968` | 当前唯一处置方式 |
| 溢出排空 worker | `chat_runtime_events.go:1019-1056` | FIFO 重投 |
| 统计出口 | `chat_runtime_events.go:1099-1110` | 待扩展为分类计数 |
| debug 透出 | `commands/chat_debug_document.go:523-527`、`chat_debug_display_http.go:397-401` | 文本/JSON 两处 |
| UI actor 容量 | `ui/controller.go:24-27` | 默认 256 |
| UI actor 投递/统计 | `ui/controller.go:231/276/319/373`, `:130-140`, `:718-735` | Post/TryPost/PostDeferred/Followup + Stats |
| 帧泵 | `ui/renderengine/frame_pump.go` | 合帧落点 |
| 启动 watchdog | `commands/chat_startup_timing.go:91-101` | 90s 未 ready 即 goroutine dump（拟扩展为稳态） |
| 失效判定点 | `ui/controller_state.go:617-621` | `transcriptReplacementInvalidatesAckedHistory`（拟加原因分类） |
| executor 判决 | `ui/terminal_session_executor.go:62-70`, `:323-325` | 累计判决（待窗口化） |
| observe 白名单 | `internal/runtimeobserve/projector.go:33-61` | 22 类 v1 白名单 |
| observe 计数 | `internal/runtimeobserve/collector.go:225-260` | unknown 计数点（待分类） |
| observe 快照 | `internal/runtimeobserve/model.go:119-120` | 计数契约 |
| SSE 映射表 | `commands/web_schema.go:76-111` | 已知事件类型全集的参考 |
| 既有加固文档 | `docs/plan/aicli-chat-unified-render-stall-analysis-and-hardening.md` §8 | 残余风险基线 |
| E2E 脚本 | `scripts/test-aicli-opencode-windows-terminal-e2e.ps1` 等 | 验收执行 |
