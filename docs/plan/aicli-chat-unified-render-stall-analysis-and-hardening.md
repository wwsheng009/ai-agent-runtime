# aicli chat 统一渲染卡住风险分析与加固方案

> 状态：已复现真实卡住并完成修复、回归测试与真实终端验证
> 适用版本：当前仓库 E:\projects\ai\ai-agent-runtime（Go module：backend）

## 1. 结论摘要

本次任务对 aicli chat 的统一渲染链路做了全链路审查与加固，覆盖事件桥、UI actor、reducer、
统一 presenter/executor、TerminalSession 物理写入、EndRun 结束路径与 Shutdown 关闭路径，
并分别用普通测试、race 测试、真实 Windows Terminal + opencode.ai 做了验证。

审查结论：

1. 已复现真实卡住：artifact `output\aicli-terminal-e2e\opencode-wt-e1906138df764ed2a48a8b710cacd651`
   的最终状态为 `ReconciliationRequired=true`、`ProjectionUnknown=false`、`HasPending=false`，
   但最后一条 Transcript token 永久停留在 Pending，Ready prompt 未恢复。
2. 根因：旧 executor 只在 `ProjectionUnknown` 时调度恢复，遗漏了独立的 scrollback
   reconciliation（viewport 已 known 但 `ReconciliationRequired=true`）这一调度分支，
   因此最终尾部提交永远不会被再次 claim。
3. 已修复：executor 将 viewport recovery 与 scrollback reconciliation 视为两个独立可调度阶段；
   `recoveryActionable` 同时覆盖 `ProjectionUnknown` 与 `ReconciliationRequired`，
   并排除 lease/frozen（二者不具备 actionability）。
4. 上一轮 E2E 的 marker 正则误判（assistant 缩进导致 `^` 锚定失败）是次要断言问题，
   已修复并保留为回归覆盖，但不是失败根因。
5. 本轮进一步消除三类活性风险：compact completed 事件处理中的 `RLock -> Lock` 重入死锁、
   runtime context 字段锁外读写竞态、TerminalSessionExecutor worker 退出时 done channel
   生命周期竞态。
6. 仍存在一个真实但无法在通用层完全消除的残余风险：物理 io.Writer.Write 没有取消能力；
   abort 只能切断会话后续写入，不能中断已进入底层 OS syscall 的 goroutine。

## 2. 统一渲染流程全景

当前统一渲染的生产链路如下（箭头表示数据流/唤醒方向）：

```text
运行时事件流
  |
  v
chatRuntimeEventBridge（bridge worker）
  |  eventQueue：容量 512 + 字节预算；满时生产者阻塞（reserveEventQueueBytes）
  |  普通事件 -> coordinator.postUIAction(ui.RuntimeEvent)（durable）
  |  approval/question -> actor barrier 后同步占用 stdin
  v
UIController（bounded mailbox，默认 256）
  |  单 goroutine Run；durable/barrier 满时 Post 阻塞；coalescable 合并
  |  reducer 内派生动作走 ReducerContext.PostFollowup（不占外部容量）
  |  Timer/Draw 走 PostDeferred，避免唯一 FramePump 被邮箱反压卡住
  v
Reducer -> AppState（唯一状态权威）
  |  产出 FlushEffect / HistoryCommitWakeEffect
  v
TerminalSessionPresenter.HandleEffect（effect callback，非阻塞）
  |  publishGeometry()：TryPost(Resize)
  |  executor.HandleEffect()：Request() 合并唤醒
  v
TerminalSessionExecutor（独立 worker，最多一个）
  |  runOne：WaitIdle -> 读取 immutable AppState 快照
  |        -> viewport recovery 或 scrollback reconciliation
  |        -> claim history token -> 组合 TerminalTransactionPlan -> FlushTransaction
  v
TerminalSession（单 writer：session mutex + 全局 terminal 写锁 + abortable writer）
  |  Write 到物理终端
  v
Ack / Failed / Deferred -> controller.Post(...) 回投 actor
  v
下一轮调度 / EndRun / Shutdown
```

关键角色与文件：

- backend/cmd/aicli/commands/chat_runtime_events.go：事件桥、eventQueue、EndRun、WaitForCurrentEvents。
- backend/cmd/aicli/commands/chat_ui_actor.go：actor 生命周期、prompt 非阻塞入口、surface facade 入口、waitUIActorIdle。
- backend/cmd/aicli/commands/chat_team_binding.go：runtime context 快照/写入辅助函数与锁协议。
- backend/cmd/aicli/ui/controller.go：Post/TryPost/PostDeferred/PostFollowup、WaitIdle/WaitIdleTimeout、historyCommitWakeNeeded。
- backend/cmd/aicli/ui/terminal_session_presenter.go：effect 消费与几何发布。
- backend/cmd/aicli/ui/terminal_session_executor.go：物理 worker、恢复调度与 history token 所有权。
- backend/cmd/aicli/ui/terminal_session.go：物理帧、事务、交替屏幕。
- backend/cmd/aicli/ui/terminal_write_aborter.go：abortable terminal writer 边界。
- backend/cmd/aicli/ui/renderengine/frame_pump.go：统一帧调度器。

## 3. 阻塞点与锁顺序

| 位置 | 阻塞条件 | 现有路径是否死锁 | 说明 |
| --- | --- | --- | --- |
| bridge eventQueue | 队列满 / 字节预算满 | 否（有界、EndRun 有界 drain） | 生产者阻塞只是背压；bridge worker 不持 UI 锁等待 actor。 |
| UIController.Post | durable/barrier 邮箱满 | 否 | 调用方通常在阻塞 Post 前释放 coordinator 锁；actor 单 goroutine 总会消费。 |
| UIController.TryPost | 邮箱满/已关闭 | 否 | 立即返回 false，prompt 热路径依赖它。 |
| UIController.PostDeferred | 无（内部队列） | 否 | 为持锁 facade 与 FramePump callback 提供 FIFO 逃生口；可暂时超过容量。 |
| UIController.WaitIdle | 队列非空 / in-flight / delivering | 否（但需纪律） | 无界等待；只能由非 reducer、非 effect callback 的受控路径调用。 |
| deliver(effects) | effect callback 同步执行 | 否 | presenter callback 只 TryPost + Request，不写终端、不等待 worker。 |
| TerminalSessionExecutor.runOne | controller.WaitIdle() | 否 | worker 等待 actor；actor 不反向等待 worker。 |
| TerminalSessionExecutor.publishResult | controller.Post(...) 邮箱满 | 否 | 只是 worker 自己阻塞；reducer 仍可消费并继续。 |
| TerminalSession.FlushTransaction | io.Writer.Write 永久阻塞 | **是（残余风险）** | 通用 io.Writer 无取消；abort 只切断后续写入，不中断已进入 syscall 的 goroutine。 |
| FramePump callback | 无（已改 PostDeferred） | 否（已加固） | Timer/Draw 走 PostDeferred；唯一 scheduler 不会被邮箱反压停住。 |

锁顺序纪律：

- 生产路径不持 coordinator.mu 做 blocking Post；surface facade 持锁时走 PostDeferred（chat_ui_actor.go 的 postSurfaceFacadeAction）。
- reducer 内的因果派生动作使用 PostFollowup/PostActionEffect，不参与外部邮箱容量竞争。
- effect 回调不做物理写，只唤醒 TerminalSessionExecutor；物理写发生在独立 worker。
- Shutdown 顺序：发布 shutdown -> 取消 timer -> presenter Close（先 detach effect consumer，再 executor.CloseTimeout）-> actor Close 并排空 -> render engine Shutdown。
  该顺序保证 effect callback 先停止，再关闭物理 writer。
## 4. 已排除的锁死环

逐层复查统一渲染路径后，除第 5 节记录并已修复的问题外，未发现其他确定性锁死环：

- coordinator 锁：blocking Post 前释放 c.mu；surface facade 持锁时走 PostDeferred（postSurfaceFacadeAction）。
- reducer 内派生动作：走 ReducerContext.PostFollowup / PostActionEffect，不消耗外部邮箱容量，不会形成“reducer 等邮箱、邮箱等 reducer”的环。
- effect consumer：TerminalSessionPresenter.HandleEffect 只做 TryPost(Resize) 与 executor.Request()，不写终端、不等待物理 worker。
- 物理 worker：TerminalSessionExecutor 在独立 goroutine 中等待 actor idle 并执行 Write；即使 Write 永久阻塞，actor 仍可继续消费 runtime/input 动作。
- FramePump：唯一 scheduler 的 Timer/Draw callback 已改用 postScheduledUIAction -> PostDeferred，不再被 bounded mailbox 反压停住；邮箱满时最多让 deferred 队列暂时超过容量，scheduler 保持活性。
- HistoryCommit：Resize、LeaseReleased、HistoryProjectionRecovered、HistoryScrollbackReconciled 等动作由 historyCommitWakeNeeded 重新唤醒 Pending worker；Unknown/部分写走 fail-closed 恢复。
- Shutdown：presenter CloseTimeout 先 detach effect consumer，再以有界时间关闭 worker；超时后 AbortTerminalWrite 释放物理 writer，最后关闭 actor 与 render engine。不存在 effect callback 等待关闭锁、关闭锁等待 callback 的交叉。

## 5. 确认并修复的问题

### 5.1 真实卡住根因：scrollback reconciliation 调度遗漏

现象（真实停滞 artifact output/aicli-terminal-e2e/opencode-wt-e1906138df764ed2a48a8b710cacd651）：

- 终末状态 ReconciliationRequired=true、ProjectionUnknown=false、HasPending=false；
- 最后一条 Transcript token 永久停留在 Pending，Ready prompt 未恢复；
- 模型早已完成输出，卡住完全发生在统一渲染/提交链路，而不是 provider 流式延迟。

根因：旧 executor 只在 ProjectionUnknown 时调度恢复，遗漏了独立的 scrollback reconciliation 分支。viewport 已 known 但 ReconciliationRequired=true 时没有任何调度入口，最终尾部提交永远不会被再次 claim。

修复（terminal_session_snapshot.go / terminal_session_executor.go / controller.go）：

1. 调度语义改为 recoveryActionable = !Lease.Active && !HistoryEffects.Frozen && (projectionUnknown || reconciliationRequired)，viewport recovery 与 scrollback reconciliation 是两个独立可调度阶段。
2. historyCommitWakeNeeded 在 recovery actionable 时对 HistoryCommitFailed / HistoryCommitsAcknowledged / HistoryCommitAcknowledged / HistoryCommitDeferred / ReplaceTranscriptAction / SetThemeContextAction / SetActiveCellAction / UpdateActiveCellAction / SetSemanticActiveCellProjectionAction / FinalizeActiveCellAction / Resize / LeaseReleased / HistoryProjectionRecovered / HistoryScrollbackReconciled 均重新唤醒 worker。
3. terminalSessionHasActionableWork() 统一表达“有 pending token 或 lease/frozen 未阻塞的恢复义务”；publishResult 在一个 worker 生命周期内继续 drain 尾部，而不是每次事务后退出等待。
4. lease 或 frozen 状态保留义务但不具 actionability，避免 executor 对 Deferred 计划空转。

回归覆盖：TestTerminalSessionExecutorDrainsFinalResidentTailQueuedDuringBlockedFrameWrite（阻塞写期间入队的最终 resident tail 在恢复后完整落盘且恰好一次）、TestHistoryCommitWakeNeededForStandaloneScrollbackReconciliation、TestTerminalSessionClaimMissRequiresRetryOnlyForActionableScheduleChange。

### 5.2 上一轮 E2E marker 断言与 assistant chrome 缩进（次要问题）

现象：上一轮 opencode E2E manifest 为 status=failed，但 UIA dump 中 40 个验证行全部存在，footer、Ready prompt、/exit 与退出码均正常。

根因：统一渲染对 assistant 续行添加两空格缩进，scripts/test-aicli-opencode-windows-terminal-e2e.ps1 的 marker 正则使用 ^ 锚定行首，导致全部 marker 被误判缺失。

修复：Get-StandaloneMarkerEvidence 与 Get-MarkerBlankLineViolations 改为 (?m)^[\t ]* 允许 chrome 前缀，但 marker 与固定文本之间仍只允许一个 ASCII 空格；helper self-test 增加缩进接受与内部双空格拒绝用例。

### 5.3 测试 writer 生命周期竞态（次要问题）

现象：go test -race 报告 bytes.Buffer Reset 与 presenter 初始异步 frame 写并发（chat_command_result_test.go 等）。

根因：enableUnifiedRendererWithWriter 返回时只表示 presenter 已 attach，初始 frame 由 TerminalSessionExecutor 异步写回；测试立即 Reset 会与 writer goroutine 竞争。生产 os.Stdout 不会被 Reset，因此这是测试生命周期问题。

修复：在 chat_command_result_test.go、zz_prompt_immediate_render_test.go、zz_prompt_render_debug_test.go 的 Reset 前补齐 waitUIActorIdle() + awaitUnifiedPresenterIdle(t, ...)。不建议把生产初始化改为无界等待初始 frame，否则会把终端 writer 阻塞直接转化为启动卡死。

### 5.4 runtime context 锁协议与 compact completed 重入死锁

现象：handleEvent 对 EventSessionCompactCompleted 恢复会话时，在 runtimeCtxMu.RLock 内调用 restoreChatRuntimeContext（需要 Lock），sync.RWMutex 不可重入升级，会确定性死锁；此外大量 bridge 路径直接锁外读写 DebugMode / PermissionMode / ApprovalReuseMode / SelectedAgentTarget / ActiveTeam 等共享字段，存在数据竞态。

修复（chat_team_binding.go / chat_runtime_events.go / chat_session.go 等）：

1. 新增 chatRuntimeContextSnapshot 与 snapshotChatRuntimeContext，RLock 下一次性克隆受保护字段；新增 chatSessionDebugMode / chatSessionPermissionMode / chatSessionApprovalReuseMode / chatSessionSelectedAgentTarget / chatSessionActiveTeam / chatSessionRequestedPermissionMode / chatSessionEffectivePermissionMode 只读辅助函数。
2. handleEvent 不再整体持有 runtimeCtxMu.RLock；compact completed 恢复路径在锁外准备后进入写锁。
3. validateAmbientTeamBinding 改为锁外完成 TeamStore I/O，再在锁内复查条件后变更；syncChatRuntimeContext、debug 文档、tool debug、direct invocation、permission hint、approval scope 等读取点全部改走快照。
4. ChatSession.runtimeCtxMu 注释更新：RequestedPermissionMode、EffectivePermissionMode 纳入保护字段。

回归覆盖：TestChatRuntimeEventBridge_SessionCompactCompletedRestoresRuntimeStateWithoutReentrantDeadlock（3s 超时断言不死锁并验证 runtime session 恢复）。

### 5.5 executor worker done 生命周期与有界关闭

现象：旧 executor 的 run() 使用 defer close(done)，但 running=false 在 defer 前发布；Request 在两者之间会复用同一个 done channel，两个 worker 同时 close 一个 channel（-count 下可复现 panic）。另外旧 Close 对永久阻塞的 io.Writer 无限等待。

修复（terminal_session_executor.go / terminal_session_presenter.go / terminal_write_aborter.go / chat_ui_actor.go）：

1. Request() 每次启动 worker 创建新 done channel；finishWorker(done) 在同一 mu 临界区内设置 running=false、清空 e.done 并关闭该 worker channel。
2. CloseTimeout(timeout) 提供有界等待；Close() 等价于 CloseTimeout(0)（无限等待语义保留给明确调用方）。
3. TerminalSessionPresenter.CloseTimeout 先让健康 worker 完成；超时后调用 AbortTerminalWrite() 释放物理 writer，再等 terminalWriterAbortGrace。
4. 新增 abortableTerminalWriter：单 dispatcher goroutine 串行化底层 Write；abort 后拒绝一切后续写入（会话不再产生第二个 owner），但不会中断已进入 OS syscall 的 goroutine。
5. chatInteractionCoordinator.closeUIActor 增加 watchdog（chatUIActorAbortGrace 后 AbortTerminalWrite）与 CloseTimeout(chatUIActorCloseTimeout)，失败时记录 terminalWritesAbandoned。

回归覆盖：TestTerminalSessionExecutorWorkerTeardownReusesFreshDoneChannel（300 次 churn，race -count=20）、TestTerminalSessionExecutorCloseTimeoutAbortsBlockedWrite、TestTerminalSessionPresenterCloseTimeoutAbortsBlockedWrite。

### 5.6 EndRun drain timeout 与 run epoch 隔离

现象：旧 WaitForCurrentEvents 不返回结果，EndRun 对 8s 超时完全无感知；legacy interaction barrier 使用无界 waitUIActorIdle，actor 长期不消费时 bridge worker 会卡在 barrier；actor Post 等待循环可能越过 EndRun 一直等待邮箱。

修复（chat_runtime_events.go）：

1. WaitForCurrentEvents 返回 bool；EndRun 超时后写 session debug 日志。
2. legacy interaction barrier 改为 waitUIActorIdleBounded，超时丢弃事件并记录。
3. RuntimeEvent 投递带 run epoch；postRuntimeEventToUIActorWithEpoch 在等待邮箱时检查 isRunEpochCurrent，下一个 BeginRun 推进 epoch 后立即释放等待循环并丢弃迟到事件。
4. EndRun 之后的 ambient 事件（异步团队编排等）按设计仍可渲染，直到下一个 BeginRun 隔离；避免把“运行结束后的后台事件”误杀。

回归覆盖：TestChatRuntimeEvents_WaitForCurrentEventsReportsTimeout、TestChatRuntimeEvents_WaitForCurrentEventsReportsSettled、TestChatRuntimeEvents_NextRunEpochRejectsLateQueuedEventAndAmbientRunEndEventsStillRender、TestChatRuntimeEvents_NextRunEpochDropsActorRuntimeActionsWhileRunEndActionsStayValid。

### 5.7 FramePump 非阻塞投递与 deferred 诊断

现象：Timer/Draw callback 若使用 blocking Post，唯一 FramePump scheduler 会在邮箱满时停住，表现为定时器延迟，降低故障恢复性。

修复：postScheduledUIAction 统一走 PostDeferred（chat_ui_actor.go），持锁 facade 的 postSurfaceFacadeAction 同样使用 deferred 逃生口；PostDeferred 新增 DeferredPosted / DeferredMerged / CapacityOverflow / PeakPending 诊断计数（controller.go）。

回归覆盖：TestChatInteractionCoordinatorPostScheduledUIActionNeverWaitsForFullMailbox、TestUIController_PostDeferredStatsTrackOverflowAndMerge。
## 6. 活性回归测试覆盖

本轮新增回归测试：

| 测试 | 验证点 |
| --- | --- |
| TestTerminalSessionExecutorDrainsFinalResidentTailQueuedDuringBlockedFrameWrite | 阻塞写期间入队的最终尾部在恢复后完整落盘、40 个 marker 恰好一次、无多余空行 |
| TestHistoryCommitWakeNeededForStandaloneScrollbackReconciliation | viewport known + reconciliation required 必须唤醒；lease/frozen 不唤醒 |
| TestTerminalSessionClaimMissRequiresRetryOnlyForActionableScheduleChange | 仅 actionable 调度变化触发重试 |
| TestTerminalSessionExecutorWorkerTeardownReusesFreshDoneChannel | 300 次 worker churn 不再复用旧 done channel |
| TestTerminalSessionExecutorCloseTimeoutAbortsBlockedWrite | CloseTimeout 超时后 AbortTerminalWrite 释放阻塞写 |
| TestTerminalSessionPresenterCloseTimeoutAbortsBlockedWrite | presenter 有界关闭自中止阻塞写 |
| TestChatRuntimeEventBridge_SessionCompactCompletedRestoresRuntimeStateWithoutReentrantDeadlock | compact completed 不再 RLock->Lock 重入死锁 |
| TestChatRuntimeEvents_WaitForCurrentEventsReportsTimeout / ReportsSettled | drain 超时/稳定语义 |
| TestChatRuntimeEvents_NextRunEpochRejectsLateQueuedEventAndAmbientRunEndEventsStillRender | 下一 run 丢弃迟到事件，EndRun 后 ambient 事件仍渲染 |
| TestChatRuntimeEvents_NextRunEpochDropsActorRuntimeActionsWhileRunEndActionsStayValid | actor 侧 epoch fence |
| TestChatInteractionCoordinatorPostScheduledUIActionNeverWaitsForFullMailbox | 邮箱满时 FramePump 投递不阻塞 |
| TestUIController_PostDeferredStatsTrackOverflowAndMerge | deferred 计数、峰值、溢出统计 |

既有活性契约（继续有效）：bounded mailbox 背压、TryPost 非阻塞、coalescable latest-wins、followup 不占外部容量、PostDeferred FIFO、prompt 输入三不等待、resize 竞态收敛、失败写 fail-closed、部分写显式恢复、sink panic 不悬挂、事件字节预算背压、EndRun 有界等待迟到事件。

## 7. 验证结果汇总

所有命令均在本机真实执行（backend 目录）：

| 验证项 | 结果 | 耗时/关键数据 |
| --- | --- | --- |
| go test -count=1 -timeout 240s ./cmd/aicli/ui ./cmd/aicli/ui/renderengine ./cmd/aicli/commands | PASS | commands 约 61.9s，合计约 65s |
| go test -race -count=1 -timeout 300s 同上三包 | PASS | 约 123s |
| go test -race -count=20 -run '^TestTerminalSessionExecutorWorkerTeardownReusesFreshDoneChannel$' ./cmd/aicli/ui | PASS | worker 生命周期压力 |
| go test -count=20 -run '^TestTerminalSessionExecutorDrainsFinalResidentTailQueuedDuringBlockedFrameWrite$' ./cmd/aicli/ui | PASS | 尾部 drain 压力 |
| scripts/test-aicli-windows-terminal-e2e.ps1 -TimeoutSeconds 45 | PASS | 72/72 history rows exactly once |
| scripts/test-aicli-opencode-windows-terminal-e2e.ps1 -TimeoutSeconds 300 | PASS | 约 27.5s；最新 artifact 7bd5c6f 通过 |

opencode E2E 关键证据（artifact output/aicli-terminal-e2e/opencode-wt-7bd5c6f66d634ffca71b8562ac595872，上一轮 b6eb95a 同样通过）：

- provider：opencode.ai，base_url=https://opencode.ai/zen/go
- model：deepseek-v4-flash；reasoning_effort=max
- Markers：40/40 各恰好出现一次；marker 顺序严格递增
- Reasoning：reasoning_before_marker_01=true；provider summary 正确投影
- Completion：ready_prompt_restored=true；UIA stable 3/3 样本
- Exit：runner_exit_code=0；forced_cleanup_count=0
- 异常空行：abnormal_blank_line_gap_count=0

停滞对照 artifact：output/aicli-terminal-e2e/opencode-wt-e1906138df764ed2a48a8b710cacd651 保留为真实复现证据。
## 8. 残余风险与加固建议

### 8.1 物理 io.Writer.Write 无法真正取消（最高风险）

abortableTerminalWriter 已让会话在 shutdown 时拒绝后续写入并释放单 writer 所有权，因此 Close/Shutdown 不再无限等待；但若底层 Write 已进入 OS syscall 且永久阻塞，该 dispatcher goroutine 会作为废弃 goroutine 遗留（进程仍可退出，因为不等待它）。

- 禁止做法：不要用 goroutine + select + 超时“假装取消”Write，也不要在 abort 后允许第二个写入者，否则会造成并发写、关闭后写入与部分帧状态错乱。
- 建议：在 TerminalSession 之上引入真正可取消的写通道（Windows 上优先使用带取消语义的 WriteFile/overlapped，或专用写线程 + 可关闭句柄）；落地前保留 watchdog 诊断（超时日志 + goroutine dump）与 terminalWritesAbandoned 状态。

### 8.2 PostDeferred 无硬容量上限

PostDeferred 是持锁 facade 与 FramePump callback 的逃生口，队列可暂时超过 mailbox 容量；现在已通过 DeferredPosted/DeferredMerged/CapacityOverflow/PeakPending 监控，但没有硬性拒绝。actor 长期不消费时队列仍会增长。

建议：限定 deferred 路径数量或在高水位时降级为 TryPost + 重试/合并；在 /debug 中展示 PeakPending 峰值。

### 8.3 无界 WaitIdle 纪律

WaitIdle 只能由测试、确定性路径和非 reducer/effect callback 的受控路径调用；在 reducer 或 effect callback 中反向等待 executor/worker 会立即形成锁死环。新代码应一律使用 WaitIdleTimeout 或事件驱动。

### 8.4 EndRun drain 超时后的同 epoch 迟到事件

EndRun 超时已记录日志、barrier 有界、actor 等待循环 epoch-fenced；但 EndRun 本身不推进 runEpoch（为保留 ambient 后台事件渲染），因此 drain 超时后、下一个 BeginRun 之前，同 epoch 的迟到事件仍可能被应用。

建议：若需要更严格隔离，可增加独立的 finalized 标记或 finalizedRunEpoch；代价是必须显式放行异步团队编排等 ambient 事件。当前语义已由测试固化。

## 9. 结论

在用户指定的 opencode.ai + deepseek-v4-flash + reasoning_effort=max 真实流程中，已复现并修复统一渲染的真实卡住：旧 executor 遗漏 scrollback reconciliation 调度，导致 ReconciliationRequired=true 时尾部 token 永久 Pending。修复后，40/40 marker 恰好一次、Ready prompt 恢复、退出码 0。

本轮同时消除了三类活性风险：compact completed 的 RLock->Lock 重入死锁与 runtime context 锁外读写竞态、executor worker done channel 生命周期竞态、物理写阻塞导致的无界关闭；并让 FramePump 投递非阻塞、EndRun drain 可观测且 epoch 隔离。

当前唯一无法在通用层完全消除的卡住点是底层 io.Writer.Write 永久阻塞时遗留的废弃写 goroutine；应通过可取消写通道或 watchdog 诊断继续加固，而不是用不安全的伪超时掩盖。

## 附：本方案相关改动文件

- docs/plan/aicli-chat-unified-render-stall-analysis-and-hardening.md
- scripts/test-aicli-opencode-windows-terminal-e2e.ps1
- backend/cmd/aicli/ui/controller.go、controller_test.go
- backend/cmd/aicli/ui/terminal_session.go、terminal_session_snapshot.go、terminal_session_executor.go、terminal_session_executor_test.go、terminal_session_presenter.go、terminal_session_presenter_test.go、terminal_write_aborter.go
- backend/cmd/aicli/commands/chat_runtime_events.go、chat_runtime_events_test.go、chat_team_binding.go、chat_session.go、chat_ui_actor.go、chat_ui_actor_surface_test.go、chat_debug_document.go、chat_tool_debug.go、command_invoke.go、command.go 及 commands 其余快照化改造文件
- backend/cmd/aicli/commands/chat_command_result_test.go、zz_prompt_immediate_render_test.go、zz_prompt_render_debug_test.go