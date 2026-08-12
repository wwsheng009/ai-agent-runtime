# aicli chat 统一渲染卡住风险分析与加固方案

> 状态：已实施并完成真实终端验证
> 适用版本：当前仓库 E:\projects\ai\ai-agent-runtime（Go module：backend）

## 1. 结论摘要

本次任务对 aicli chat 的统一渲染链路做了全链路审查，覆盖事件桥、UI actor、reducer、
统一 presenter/executor、TerminalSession 物理写入、EndRun 结束路径与 Shutdown
关闭路径，并分别用普通测试、race 测试以及真实 Windows Terminal + opencode.ai 做了验证。

审查结论：

1. 正常路径（用户指定的 model=deepseek-v4-flash、reasoning_effort=max、
   provider=opencode.ai、流式）**未复现真实卡死**：模型约 25 秒完成渲染，reasoning、
   正文、40 个验证 marker、Ready prompt、footer、/exit 全部通过，runner_exit_code=0。
2. 上一轮 E2E 报告的 status=failed 是**断言问题而非渲染卡死**：统一渲染对 assistant
   续行加了两空格缩进，E2E 的 marker 正则要求从行首开始，导致 40 个 marker 全部被误判缺失。
3. 发现并修复了一个确定性的测试竞态：3 个测试在 presenter 初始异步 frame 写回
   bytes.Buffer 之前立即 Reset()，-race 可稳定复现 writer 竞态。
4. 仍存在一个真实但难以在通用层消除的残余风险：物理 io.Writer.Write 没有超时或取消
   能力，若 Windows Console/ConPTY/管道宿主永久阻塞，TerminalSessionExecutor.Close() 与
   Shutdown() 会无限等待。不能用“goroutine + select + 超时”伪装修复，否则会引入并发写
   与关闭后写入问题。

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
  v
Reducer -> AppState（唯一状态权威）
  |  产出 FlushEffect / HistoryCommitWakeEffect
  v
TerminalSessionPresenter.HandleEffect（effect callback，非阻塞）
  |  publishGeometry()：TryPost(Resize)
  |  executor.HandleEffect()：Request() 合并唤醒
  v
TerminalSessionExecutor（独立 worker，最多一个）
  |  runOne：WaitIdle -> claim history token -> 读取 immutable AppState 快照
  |        -> 组合 TerminalTransactionPlan -> FlushTransaction
  v
TerminalSession（单 writer：session mutex + 全局 terminal 写锁）
  |  Write 到物理终端
  v
Ack / Failed / Deferred -> controller.Post(...) 回投 actor
  v
下一轮调度 / EndRun / Shutdown
```

关键角色与文件：

- backend/cmd/aicli/commands/chat_runtime_events.go：事件桥、eventQueue、EndRun、WaitForCurrentEvents。
- backend/cmd/aicli/commands/chat_ui_actor.go：actor 生命周期、prompt 非阻塞入口、surface facade 入口、waitUIActorIdle。
- backend/cmd/aicli/ui/controller.go：Post/TryPost/PostDeferred/PostFollowup、WaitIdle/WaitIdleTimeout、historyCommitWakeNeeded。
- backend/cmd/aicli/ui/terminal_session_presenter.go：effect 消费与几何发布。
- backend/cmd/aicli/ui/terminal_session_executor.go：物理 worker 与 history token 所有权。
- backend/cmd/aicli/ui/terminal_session.go：物理帧、事务、交替屏幕。
- backend/cmd/aicli/ui/renderengine/frame_pump.go：统一帧调度器。

## 3. 阻塞点与锁顺序

| 位置 | 阻塞条件 | 现有路径是否死锁 | 说明 |
| --- | --- | --- | --- |
| bridge eventQueue | 队列满 / 字节预算满 | 否（有界、EndRun 有界 drain） | 生产者阻塞只是背压；bridge worker 不持 UI 锁等待 actor。 |
| UIController.Post | durable/barrier 邮箱满 | 否 | 调用方通常在阻塞 Post 前释放 coordinator 锁；actor 单 goroutine 总会消费。 |
| UIController.TryPost | 邮箱满/已关闭 | 否 | 立即返回 false，prompt 热路径依赖它。 |
| UIController.PostDeferred | 无（内部队列） | 否 | 为持锁 facade 提供 FIFO 逃生口；可暂时超过容量。 |
| UIController.WaitIdle | 队列非空 / in-flight / delivering | 否（但需纪律） | 无界等待；只能由非 reducer、非 effect callback 的受控路径调用。 |
| deliver(effects) | effect callback 同步执行 | 否 | presenter callback 只 TryPost + Request，不写终端、不等待 worker。 |
| TerminalSessionExecutor.runOne | controller.WaitIdle() | 否 | worker 等待 actor；actor 不反向等待 worker。 |
| TerminalSessionExecutor.publishResult | controller.Post(...) 邮箱满 | 否 | 只是 worker 自己阻塞；reducer 仍可消费并继续。 |
| TerminalSession.FlushTransaction | io.Writer.Write 永久阻塞 | **是（残余风险）** | 通用 io.Writer 无取消；Close()/Shutdown() 会无限等待。 |
| FramePump callback | Post(Timer/Draw) 邮箱满 | 否（可恢复性风险） | 唯一 scheduler 会停住，定时器延迟；当前不构成锁死环。 |

锁顺序纪律：

- 生产路径不持 coordinator.mu 做 blocking Post；surface facade 持锁时走 PostDeferred（chat_ui_actor.go 的 postSurfaceFacadeAction）。
- reducer 内的因果派生动作使用 PostFollowup/PostActionEffect，不参与外部邮箱容量竞争。
- effect 回调不做物理写，只唤醒 TerminalSessionExecutor；物理写发生在独立 worker。
- Shutdown 顺序：发布 shutdown -> 取消 timer -> presenter Close（先 detach effect consumer，再 executor.Close()）-> actor Close 并排空 -> render engine Shutdown。
  该顺序保证 effect callback 先停止，再关闭物理 writer。

## 4. 已排除的锁死环

对正常统一渲染路径逐层检查后，未发现确定的内部锁死环：

- coordinator 锁：blocking Post 前通常已释放 c.mu；surface facade 持锁时走 PostDeferred，
  不等待 bounded mailbox。
- reducer 内派生动作：走 ReducerContext.PostFollowup / PostActionEffect，不消耗外部邮箱容量，
  因此不会出现“reducer 等邮箱、邮箱等 reducer”的环。
- effect consumer：TerminalSessionPresenter.HandleEffect 只做 TryPost(Resize) 与
  executor.Request()，不写终端、不等待物理 worker，因此 actor 不会在 deliver 阶段被物理写卡住。
- 物理 worker：TerminalSessionExecutor 在独立 goroutine 中等待 actor idle 并执行 Write；
  即使 Write 永久阻塞，actor 仍可继续消费 runtime/input 动作（resize 竞态测试已证明）。
- HistoryCommit：Resize、LeaseReleased、HistoryProjectionRecovered、HistoryScrollbackReconciled
  等动作由 historyCommitWakeNeeded 重新唤醒 Pending worker；Unknown/部分写走 fail-closed 恢复。
- Shutdown：先 detach effect consumer 再关闭 worker，最后关闭 actor；不存在 effect callback
  等待关闭锁、关闭锁等待 callback 的交叉。

## 5. 确认并修复的问题

### 5.1 E2E 断言与 assistant chrome 缩进

现象：上一轮 opencode E2E 的 manifest 为 status=failed，但 UIA dump 中 40 个验证行全部存在
（WTLIVE...-01 到 -40 terminal history validation），footer 正确显示 deepseek-v4-f... max · opencode.ai，
Ready prompt 已恢复，/exit 成功，runner_exit_code=0，forced_cleanup_count=0。

根因：统一渲染对 assistant 续行添加两空格缩进；
scripts/test-aicli-opencode-windows-terminal-e2e.ps1 的 Get-StandaloneMarkerEvidence 与
Get-MarkerBlankLineViolations 使用 ^ 锚定行首，导致 marker 行全部 MatchCount=0，
并连带 reasoning_before_marker01 失败。

修复：

1. Get-StandaloneMarkerEvidence：正则改为 (?m)^[\t ]* 允许 assistant chrome 前缀，
   但 marker 与固定文本之间仍只允许恰好一个 ASCII 空格，内部格式不放松。
2. Get-MarkerBlankLineViolations：marker 模式同样允许行首空白，继续检查 01..40 之间的空行。
3. helper self-test 新增：缩进 marker 必须被接受（Index 为缩进后位置）；缩进连续 marker
   不得报空行违规；缩进后的内部双空格仍必须拒绝。

验证：修复后 opencode E2E 25.7 秒 PASS，40/40 marker exactly once，
marker 顺序严格递增，reasoning 在 marker01 之前，退出码 0。

### 5.2 测试 writer 生命周期竞态

现象：go test -race 在 TestUnifiedInteractiveLegacyCommandsAreFencedBeforeLegacyHandlers
（chat_command_result_test.go:186）报告 bytes.Buffer Reset 与 presenter 初始 frame 写并发；
完整日志见 output/chat-render-race.log。

根因：enableUnifiedRendererWithWriter 返回只表示 presenter 已 attach，初始 frame 由
TerminalSessionExecutor 异步写回；测试立即 terminal.Reset() 与 writer goroutine 竞争。
生产 os.Stdout 不会被 Reset，因此这是测试生命周期问题，但也暴露了初始化 API 的异步契约。

修复：在以下文件的 Reset 前补齐 waitUIActorIdle() + awaitUnifiedPresenterIdle(t, ...)：

- backend/cmd/aicli/commands/chat_command_result_test.go
- backend/cmd/aicli/commands/zz_prompt_immediate_render_test.go
- backend/cmd/aicli/commands/zz_prompt_render_debug_test.go

其余类似测试（backtrack/plan/timeline/simple/surface/ui_actor）此前已按同一模式等待。
不建议把生产 enableUnifiedRendererWithWriter 改为无界同步等待初始 frame，否则会把终端
writer 阻塞直接转化为启动卡死。

## 6. 活性回归测试覆盖（既有，已确认）

以下测试已经覆盖本次审查关心的活性契约，无需重复新增：

| 测试 | 验证点 |
| --- | --- |
| TestUIController_BoundedMailboxBackpressure | durable 满队列时 Post 背压，Run 排空后恢复 |
| TestUIController_TryPostBackpressure | 邮箱满 TryPost 立即返回 false，coalescable 仍可合并 |
| TestUIController_CoalescableLatestWinsAndDirtyUnion | Draw/Timer 同 key latest-wins + dirty 并集 |
| TestUIController_FollowupBypassesFullExternalMailbox | reducer followup 不等待自己的外部容量 |
| TestUIController_PostDeferredPreservesFIFOBeyondMailboxCapacity | 持锁 facade 不等待邮箱，FIFO 不越序 |
| TestChatInteractionCoordinatorPromptInputNeverWaitsForFullMailbox | 邮箱满 prompt 输入仍立即返回并最终送达 |
| TestChatInteractionCoordinatorPromptInputNeverWaitsForActorDrain | 输入不被 actor 排空拖住 |
| TestChatInteractionCoordinatorPromptInputNeverWaitsForCoordinatorRenderLock | 持 c.mu 时输入不等待渲染锁 |
| TestChatInteractionCoordinatorPromptEditorStatusNeverWaitsForFullMailbox | editor status 同款非阻塞入口 |
| TestTerminalSessionExecutorResizeRacingInFlightHistoryReconcilesAndDrains | writer 阻塞时 actor 仍处理 Resize；恢复后重新 wake 并 drain |
| TestTerminalSessionExecutorSecondResizeRacingScrollbackResetStillRecovers | 连续 Resize 竞态仍收敛 |
| TestTerminalSessionExecutorFrameFailureReconcilesWithoutBlindHandoff | 失败写 fail-closed，不盲目 handoff |
| TestTerminalSessionExecutorPartialHistoryWriteReconcilesWithoutResize | 部分写后显式恢复 |
| TestHistoryCommitExecutor_SinkPanicFailsAndLeavesNoWorkerHang | sink panic 不留下 worker 悬挂 |
| TestChatRuntimeEvents_HandleBackpressuresOnRetainedBytes | 事件字节预算背压 |
| TestChatRuntimeEvents_WaitForCurrentEventsWaitsForLateArrivingEvents | EndRun 前有界等待迟到事件 |

结论：mailbox 满输入仍响应、持锁 facade 不等待、history Deferred 重新 wake、terminal worker
阻塞时 actor 仍推进等关键活性契约均已由测试固化。

## 7. 验证结果汇总

所有命令均在本机真实执行：

| 验证项 | 结果 | 耗时 |
| --- | --- | --- |
| go test -count=1 ./cmd/aicli/ui ./cmd/aicli/ui/renderengine ./cmd/aicli/commands | PASS | ui 1.8s / renderengine 1.3s / commands 60.8s |
| go test -race -count=1 ./cmd/aicli/ui ./cmd/aicli/ui/renderengine ./cmd/aicli/commands | PASS | ui 4.5s / renderengine 2.8s / commands 90.2s |
| scripts/test-aicli-windows-terminal-e2e.ps1 -TimeoutSeconds 45 | PASS | 8.2s |
| scripts/test-aicli-opencode-windows-terminal-e2e.ps1 -TimeoutSeconds 300 | PASS | 25.7s |

opencode E2E 关键证据：

- provider：opencode.ai，base_url=https://opencode.ai/zen/go，compatibility=opencode-console-go-2026-07
- model：deepseek-v4-flash；reasoning_effort=max；stream=true；yolo=true；tools=disabled
- Markers：40/40 各恰好出现一次；marker 顺序严格递增
- Scrollback：marker01 进入完整 DocumentRange 且不在 VisibleRanges；marker40 可见
- Completion：request_completed=True；ready_prompt_restored=True；UIA stable 3/3 样本
- Ordering：reasoning_before_marker01=True；provider summary artifact projected 且 exactly once
- Exit：/exit confirmed；runner_exit_code=0；forced_cleanup_count=0
- Manifest：output/aicli-terminal-e2e/opencode-wt-d0590c5d2f3f4c518e655a5d0e809aa9/manifest.json

## 8. 残余风险与加固建议

### 8.1 物理 io.Writer.Write 无取消（最高风险）

NaN
  会等待 executor，因此 ConPTY/管道宿主永久阻塞会挂起退出。
- 禁止做法：不要用 goroutine + select + 超时“假装取消”Write。通用 io.Writer 无法安全中断，
  强行超时返回会造成并发写、关闭后写入和部分帧状态错乱。
- 建议：在 TerminalSession 之上引入可取消的写通道抽象（Windows 上优先使用带取消语义的
  WriteFile/overlapped、或专用写线程 + 可关闭句柄）；落地前为 Shutdown 增加 watchdog 诊断
  （超时日志 + goroutine dump），保持单 writer 所有权不变。

### 8.2 无界 WaitIdle 纪律

- WaitIdle 只能由测试、确定性路径和非 reducer/effect callback 的受控路径调用。
- 若未来在 reducer 或 effect callback 中反向等待 executor/worker，会立刻形成锁死环；
  新代码应一律使用 WaitIdleTimeout 或事件驱动。

### 8.3 FramePump callback 的 blocking Post

- 当前 FramePump 的 Timer/Draw callback 使用 blocking Post；邮箱满时唯一 scheduler 会阻塞，
  表现为定时器延迟，不构成现有死锁，但会降低故障恢复性。
- 建议：Timer/Draw 也改 TryPost + pending/coalesce 语义，与 prompt 热路径一致。

### 8.4 EndRun drain timeout 结果被忽略

- EndRun 调用 WaitForCurrentEvents(8s) 但未检查返回值；超时后仍继续 finalize、设置 stage、
  恢复 prompt，迟到事件随后仍可能修改 Scene/状态。
- 建议：EndRun 超时后进入隔离模式，迟到事件按 runEpoch 丢弃或仅记日志，避免污染下一 run epoch。

### 8.5 PostDeferred 无容量上限

- PostDeferred 可让内部队列暂时超过 mailbox 容量；actor 长期不消费时队列会增长。
- 建议：限定 facade 路径数量，并在 Stats()/诊断中监控 Pending 峰值。

## 9. 结论

在用户指定的 opencode.ai + deepseek-v4-flash + max 真实流程中，aicli chat 统一渲染
**未复现真实卡死**：流式输出、reasoning、历史滚动、Ready prompt、footer 与退出均正常。
此前 E2E 的失败是断言未适配 assistant 续行缩进，race 失败是测试在异步初始 frame 完成前
重置 writer，两者均已修复并通过普通/race/真实终端三层验证。

当前唯一无法在通用层彻底消除的卡住点是物理 io.Writer.Write 永久阻塞导致 Shutdown 等待；
应通过可取消写通道或 watchdog 诊断继续加固，而不是用不安全的伪超时掩盖。

## 附：本方案相关改动文件

- scripts/test-aicli-opencode-windows-terminal-e2e.ps1
- backend/cmd/aicli/commands/chat_command_result_test.go
- backend/cmd/aicli/commands/zz_prompt_immediate_render_test.go
- backend/cmd/aicli/commands/zz_prompt_render_debug_test.go
- docs/plan/aicli-chat-unified-render-stall-analysis-and-hardening.md
