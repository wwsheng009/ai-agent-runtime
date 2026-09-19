# ESC 中断优先级与 Loop 中断健壮性分析及实施方案

- 日期：2026-09-18
- 状态：**阶段 A–E 已实施；阶段 F P0~P2b 已实施（P3 清理待真实终端验证）**。
  实施与验证记录见 §10、§12.5–§12.7；残余待验证项（PTY/winpty、模态语义取样、P3 删旧路径）见 §10/§12.6 清单。
- 关联：
  - `docs/plan/supervision-approval-resume-past-deadline-fix-plan.md`（审批终态守卫，与本方案的取消/停止语义相邻）
  - `docs/plan/session-user-turn-backtrack-plan.md`（Esc 与回退的按键语义边界）
  - `docs/plan/workspace-chat-realtime-streaming.md`（Web 实时流与停止按钮契约）
  - `docs/plan/runtime-observability-supervision-http-api-plan.md`（runtime commands / active turn 注册表的 HTTP 契约）
- 证据来源：2026-09-18 本会话代码审计，全部结论附 `file:line`；未做运行时扰动实验，标注“待验证”的条目已显式说明。

## 0. 摘要

中断链路本身是**四层结构**（入口 → 会话 → 回合 → loop），其中：

- **Loop 核心是健壮的**：step 边界检查、子组件 `ctx.Done()`、LLM 重试退避可取消、取消被分类为 `canceled` 而非失败，且 goal 自动续跑、回合自动重跑都有中断守卫（见 §3）。
- **问题集中在“入口可达性”与“UI 诚实性”**：
  1. Web 端**没有任何 ESC→停止绑定**（只有 Stop 按钮）；
  2. 状态行的 `esc to interrupt` 由**状态种类**决定，与「本 UI 是否真的 armed 了 ESC 消费者」无关；
  3. TUI 的 ESC 消费者生命周期只覆盖**本地 `sendMessage` 窗口**；非交互/PTY 宿主、非 TUI 宿主
     （runtime-server / 监督唤醒的 detached run）、模态 overlay 占用期都无人消费（详见 §2.6 / §4）；
  4. Windows 轮询只认“`ESC` 前没有任何其他输入”的裸 ESC，带草稿时容易被吞。

实测触发样本：会话 `session_20260918195740_wY5wLpMM` 在 provider 限流重试中出现
`◦ Retrying step=1 attempt=3/10 … (8m 31s • esc to interrupt)`，用户在 TUI/Web 按 ESC 均无响应、
无法退出 busy 状态。限流本身不是缺陷；缺陷是**界面承诺了可中断，但按键没有消费者/停止语义不完整**。

## 1. 现象与复现路径

| 路径 | 预期 | 实际（2026-09-18 复核实测口径） | 判定 |
| --- | --- | --- | --- |
| **Web 运行中**按 `Esc` | 停止当前回合 | 无任何绑定，事件被忽略 | **缺陷（P1-1）** |
| Web 空闲按 `Esc` | 进入/退出回合回溯导航 | `canBacktrack=!isResponding` 时为回溯；运行中该 hook 直接返回 | 正常（`use-session-backtrack.ts:100-103`） |
| TUI 空闲、空草稿按 `Esc` | 回溯选择器 | 打开用户回合回溯选择器，**不中断** | 正常（`inputbox_editor.go:1273-1280`、`chat.go:1357-1360`） |
| TUI 空闲、有草稿按 `Esc` | 保留草稿 | 无操作（既不中断也不清空草稿） | 正常（同上 + `inputbox_editor_test.go:1527+`） |
| TUI 忙碌（capture 活跃）按 `Esc` | 中断 | `onCancel`→`cancelled`→中断，**任何草稿都中断且保留草稿** | 正常（`chat_composer.go:438-443`、`chat_busy_input.go:82-90`） |
| TUI 忙碌（capture 不活跃、KeyHandler armed）按 `Esc` | 中断 | watcher 消费并中断 | 正常（`chat_escape_interrupt.go:9-48`） |
| TUI 忙碌（非交互/PTY）按 `Esc` | 中断 | KeyHandler 在 setup 即被禁用，watcher 为 no-op；busy 期间无消费者 | **缺陷（P2-9）** |
| 审批/提问等待按 `Esc` | 中断 | priority 分支中断并结束本 capture 循环 | 正常（行为细节待确认，见 §11） |
| 限流重试退避中按 `Esc` | 立即中止 | 消费者在时 `waitRetryDelay` 可被取消；否则无反应 | 取决于 P1-2/P1-3/P2-9 |
| 模态 overlay 打开时按 `Esc` | 关闭 overlay | overlay 独占 stdin（设计如此），但状态行仍可能显示可中断 | 正常行为 + 提示缺陷（P1-2） |
| Stopping 清理中按 `Esc` | 无操作 | watcher 按 `IsInterrupted` 静默丢弃重复 ESC | 正常但无反馈（P2-10） |

复现最小脚本（无需真实限流）：在 TUI 本地回合进行中打开 Web 页面按 `Esc`；或以 Web 注入 prompt
（`/web/api/input`）让 TUI 处于“显示中但非本地 sendMessage”的窗口后再按 `Esc`。

## 2. 当前中断机制梳理（分层）

### 2.1 入口层（5 条通道）

| 通道 | 触发 → 处理链 | 关键代码 | 覆盖范围 |
| --- | --- | --- | --- |
| TUI 物理 ESC | `ui.KeyHandler` 轮询 → `notifyChan` → `startChatEscapeInterruptWatcher` → `InterruptPreservePendingInput` | `backend/cmd/aicli/ui/keyhandler.go:40-106`；`commands/chat_escape_interrupt.go:9-48`；`commands/chat_send.go:85-86` | **仅本地 `sendMessage` 执行窗口**（进入时 Arm、返回时 Disarm） |
| TUI 忙碌期 ESC（捕获模式） | busy capture 抢占 stdin、`KeyHandler.Suspend()`；编辑器把 ESC 解码为 `editorKeyInterrupt` → `capture.Cancelled()` → `interruptChatTurnFromBusyInputCancel` | `commands/chat_busy_input.go:16-41、82-90`；`ui/inputbox_editor.go:2184`；可用性 `ui/inputbox_editor.go:312-321` | 依赖 `SupportsCancelableInteractiveInputRead()`；不可用时该通道整体不启动 |
| Ctrl+C 信号 | `setupSignalHandler` → `chatInterruptExitState.handleInterruptSignal`：空闲一次退出；busy 第一次中断、2s 内第二次退出；Stopping 视为退出 | `commands/chat_exit.go:19-68`；`commands/chat_unix.go` | Windows 仅 Ctrl+C；有“二次确认退出”窗口语义 |
| Web / HTTP | ① `POST /web/api/input {"type":"interrupt"}` → `handleWebInterrupt` → `session.Interrupt()`；② `POST /api/runtime/sessions/{id}/runtime/commands {"type":"interrupt"}` → 先查进程内 active-turn 注册表，未命中回落 durable actor | ① `commands/web_handlers.go:653-673`；② `internal/api/skills/session_runtime_handlers.go:849-872、983-1000`；注册表 `internal/api/skills/session_active_turn.go` | ①仅本进程 Web 客户端（丢弃排队输入）；②跨客户端一致，支持 `turn_id` 精确匹配 |
| 监督 / 执行期限取消 | `execution_supervisor` 决策（执行期限、预算）→ `RunInterrupter.InterruptRun` → `AgentSessionRunInterrupter` → `Controller.Close` 停止子会话 | `internal/supervision/execution_supervisor.go:80-84、540-549`；`internal/toolbroker/execution_host_adapters.go:29-51` | 子会话 run 的强制取消；`run.CancelSource=decision` 与用户中断可区分 |

**ESC 消费者是否存在的判定条件（三者需同时满足）**：

1. `shouldInitializeChatKeyHandler` 为真——要求交互终端（`commands/chat_setup.go:306-311`）；
2. watcher 启动时 `KeyHandler.IsEnabled()` 为真（`commands/chat_escape_interrupt.go:10`）；
3. busy capture 需 `shouldUseInteractiveLineEditor`（同一交互终端判定，`commands/chat_input_queue.go:1118-1123`）
   且 `SupportsCancelableInteractiveInputRead()` 为真（`ui/inputbox_editor.go:312-321`）。

同进程 Web 注入的 prompt 经 `injectChatWebPrompt` 进入 `InputQueue` 并由本地主循环 `sendMessage`
执行（`commands/web_handlers.go:676-711`），因此与 TUI 共享同一 arm 窗口；**不存在“Web 回合天然无人消费”
的推断**——无消费者的是非交互/PTY、非 TUI 宿主与 overlay 占用期（准确边界见 §2.6）。

### 2.2 会话层（统一中断动作与清理）

`ChatSession.interrupt(preserve)`（`commands/chat.go:276-308`）：

1. 置 `interrupted` 原子位（`:280`）；
2. 进入 Stopping 阶段（`:286`）；
3. 启动**异步** interrupt cleanup（`:288`，幂等注册 `:343-371`）；
4. 调用 `cancelFunc()` 取消本轮本地 ctx（`:290`），ctx 由 `newChatCancelContext` 创建并标记
   `cancel_source=user_interrupt`（`commands/chat_setup.go:301-304`，`internal/execution/timeout.go:68-82`）；
5. UI 复位（`:292-307`），preserve 语义下保留草稿并恢复 pending 输入。

cleanup 主体（`commands/chat_interrupt.go:20-41`）按顺序：

```text
prepareTeamInterrupt（team 记账：StopLoop + markTeamInterrupted，chat_interrupt.go:43-60）
  → interruptActorRun（actor.Interrupt ≤1.5s + SessionHub.StopContext，chat_interrupt.go:182-199）
  → markRuntimeSessionStopped
  → interruptChildAgentRuns（子会话级联，chat_interrupt.go:62-97）
```

总预算 5s（`chat.go:386`，常量 `chat_interrupt.go:16-17`）；完成后 `finishInterruptCleanupUI`
清 Stopping（`chat.go:458-465`）。

主循环每轮迭代重置中断状态并重建 cancel ctx（`chat.go:1283-1289`），即“一次 ESC 只作用于当前回合”。

### 2.3 回合层（取消句柄注册表，幂等契约）

`activeTurnRegistry`（`internal/api/skills/session_active_turn.go`）：

- `beginCancelable` 登记在途回合并持有 `cancel(source) bool` 句柄；`once` 保证同一回合只真实取消一次（`:91-133`）；
- 设计动机（注释 `:79-90`）：**detached 回合的 runCtx 是 `context.WithoutCancel`**，刷新/重连后的页面
  只能通过进程内注册表句柄停回合；
- 取消结果机读化：`cancelled / already_cancelled / no_active_turn / turn_mismatch / not_cancelable`
  （`:141-166`）；`turn_mismatch` 由 HTTP 层升级为 409（`session_runtime_handlers.go:861-866`），
  防止迟到的 stop 误杀新回合；
- handler 侧登记与 `cancel_source=user_interrupt` 回填：`internal/api/skills/handler.go:2015-2031、2078-2086`。

**actor 侧中断语义**（`internal/chat/actor.go:1206-1241`）：`handleInterrupt` 取消当前 run 的 ctx、
把状态置 `SessionStopped`、清空 `PendingTool/PendingApproval/PendingQuestion`、发布
`session_interrupted` 事件；随后 `retireInterruptedSessionRunAfter(run, 250ms)`（`:35`）给
“可感知取消的执行尾”一个短暂回传窗口——**不等待 run 返回**。真正阻塞公开调用方的是
`SessionHub.StopContext`（`commands/chat_interrupt.go:182-199`）与 actor 的 run 收尾。

### 2.4 Loop / 执行层（中断传播）

- ReAct 主循环每步开头检查 `currentCtx.Err()`，分别生成 `run_timeout` / `canceled`
  （`internal/agent/loop.go:715-728`）；收尾统一把 `context.Canceled / DeadlineExceeded`
  归类为 `canceled` 而非失败（`loop.go:511-527`）；
- 子组件均 `select ctx.Done()`：scheduler（`scheduler.go:228`）、并行工具调度
  （`tool_parallel_scheduler.go:316`）、子代理批处理（`subagent_batch_coordinator.go:847/1163/1243`）、
  子代理重试（`subagent_retry.go:169`）；
- LLM 重试退避可取消：`internal/llm/retry_policy.go:589-602`（`waitRetryDelay` 用 `select ctx.Done`），
  失败直接向上返回（`retry_executor.go:87-89`）；
- 长耗时 shell 采用 `exec.CommandContext`（`cmd/aicli/functions/shell.go:107`），ctx 取消即杀进程；
- CLI actor 执行器把会话 ctx 传入 `actor.SubmitPrompt`（`commands/chat_actor_executor.go:33-56、131-236`），
  等待就绪循环同样尊重 ctx（`:78-105`）。

### 2.5 展示层（状态行承诺）

动态状态行 `interruptible` 完全由**状态种类 switch** 决定
（`commands/chat_interaction.go:2402-2447`）：Thinking / Streaming / Retrying / Tool / Approval / Answer
一律 `true`；只有 Stopping / Selection / Confirmation / Secret / Panel 为 `false`。
TUI 侧拼 ` (N • esc to interrupt)`（`:2382-2393`），Web 侧同名字段由 `:891-925` 发布。
**该计算不包含“本 UI 是否真的 armed 了 ESC 消费者”** —— 这是 P1-2 的根因。

### 2.6 ESC 优先级与消费者矩阵（2026-09-18 复核补全）

| 状态 | 消费者（优先级从高到低） | 行为 | 证据 |
| --- | --- | --- | --- |
| 空闲，空草稿 | 编辑器 `editorKeyCancelPopup` | 打开用户回合回溯选择器；不中断 | `ui/inputbox_editor.go:1273-1280`、`commands/chat.go:1357-1360` |
| 空闲，有草稿 | 编辑器 | 无操作（草稿保留） | 同上 + `ui/inputbox_editor_test.go:1527+` |
| 模态 overlay 打开 | overlay 自身的 `editorKeyInterrupt` | 关闭 overlay（不中断回合） | `ui/transcript_pager.go:689-704`、`ui/fullscreen_list.go:394`、`ui/debug_overlay.go:162` |
| 回合运行，capture 活跃 | busy capture `onCancel`（KeyHandler 已 Suspend） | 中断回合；任何草稿都中断，`PreserveDraft` 保留草稿 | `commands/chat_composer.go:438-443`、`commands/chat_busy_input.go:82-90` |
| 回合运行，capture 不活跃 | KeyHandler → watcher | 中断 | `commands/chat_escape_interrupt.go:9-48` |
| 回合运行，捕获不可用（非交互/PTY） | 无 | **无响应**（仅 Ctrl+C） | `commands/chat_setup.go:306-311`、`ui/inputbox_editor.go:312-321` |
| 审批/提问等待（priority 模式） | busy capture priority 分支 | 中断；随后 `signalReadError` 并结束本 capture 循环 | `commands/chat_busy_input.go:82-90` |
| Stopping / 已中断 | watcher 按 `IsInterrupted` 丢弃 | 无操作（不重复取消），也无用户反馈 | `commands/chat_escape_interrupt.go:27-31` |
| Web 空闲 | `use-session-backtrack-keyboard` | 进入/退出回溯导航（`canBacktrack=!isResponding`） | `frontend/src/hooks/workspace/session-backtrack/use-session-backtrack-keyboard.ts:79-105`、`use-session-backtrack.ts:100-103` |
| Web 运行中 | 无 | **无响应** | 见 §4 P1-1 |

## 3. 已验证的健壮点（正向清单，勿在实施中破坏）

| # | 结论 | 证据 |
| --- | --- | --- |
| 1 | 取消是一等语义：step 边界检查 + `canceled` 分类，不写失败记录 | `loop.go:715-728`、`loop.go:511-527` |
| 2 | 回合级自动重跑在中断后立即刹车 | `commands/chat_turn_auto_retry.go:100-111`（`IsInterrupted` 检查） |
| 3 | goal 自动续跑有中断守卫（`interrupted` → 不续跑） | `commands/chat_goal_auto_continue.go:123-125`；判定入口 `:110-149` |
| 4 | 注册表取消幂等、防误杀（`once` + `turn_mismatch` 409） | `session_active_turn.go:91-133、248+`；`session_runtime_handlers.go:858-872` |
| 5 | LLM 重试退避可被 ctx 取消（限流场景 ESC 有效的关键） | `internal/llm/retry_policy.go:589-602`、`retry_executor.go:87-89` |
| 6 | 工具/子代理/审批等待均有 ctx 或终态守卫 | `scheduler.go:228`、`tool_parallel_scheduler.go:316`、`subagent_batch_coordinator.go:847/1163/1243`、`internal/chat/actor.go:49-64、149-158`（+`docs/plan/supervision-approval-resume-past-deadline-fix-plan.md`） |
| 7 | shell 工具 ctx 取消即终止进程 | `cmd/aicli/functions/shell.go:107`（`exec.CommandContext`） |
| 8 | Ctrl+C 语义完整（中断/双击退出/Stopping 直接退出） | `commands/chat_exit.go:19-68` |

> 备注：此前分析的“goal 续跑可能在 ESC 后被拉起”已核实**不成立**——`shouldAutoContinueActiveGoalDecision`
> 在 `chat_goal_auto_continue.go:123-125` 有 `IsInterrupted()` 守卫。本方案不再将其列为缺陷。

## 4. 问题清单（按严重度）

### P1-1 Web 端没有 ESC→停止绑定（直接对应用户现象）

- 证据：前端全仓 `Escape` 仅用于菜单/弹窗/侧栏（`frontend/src/hooks/workspace/composer/use-composer-menu.ts:328`、
  `frontend/src/hooks/workspace/session-backtrack/use-session-backtrack-keyboard.ts:57`、
  `frontend/src/components/workspace/...` 多处）；运行中停止只有 Stop 按钮
  `handleStopResponding`（`frontend/src/pages/workspace-page.tsx:226-240`）。
- 触发：Web 页面任意运行中回合按 `Esc`。
- 影响：按键无任何反馈；用户以为系统死锁；与 TUI 的 esc 承诺不一致。
- 方案：见 §5 阶段 A。

### P1-2 `esc to interrupt` 是状态种类驱动的“静态承诺”

- 证据：`commands/chat_interaction.go:2402-2447`（switch by kind），TUI `:2382-2393`，Web payload `:891-925`。
- 触发：web/actor 注入回合、非交互/兼容输入模式、`SupportsCancelableInteractiveInputRead()==false`、
  消费者已 Disarm 的任何窗口。
- 影响：提示可中断但无人消费；正是“按了没反应”的核心误导来源。
- 方案：见 §5 阶段 B（`interruptible` 能力化）。

### P1-3 TUI ESC 消费者的生命周期与宿主导向

- 证据：watcher 在 `commands/chat_send.go:85-86` 启动/停止；`KeyHandler` 默认 disarmed
  （`ui/keyhandler.go:40-47`）；busy capture 仅在 `sendMessage` 内启动（`commands/chat_busy_input.go:16-33`）。
- **精确边界（本轮复核修正）**：同进程 Web 注入的 prompt 经 `injectChatWebPrompt` 进 `InputQueue`
  （`commands/web_handlers.go:676-711`），仍由本地主循环 `sendMessage` 执行，因此 watcher/capture
  在 arm 窗口内可用；真正“无人消费”的是：
  ① 非交互/PTY 宿主（见 P2-9）；
  ② 无 TUI 进程的宿主（runtime-server、监督唤醒的 detached run——只登记在 active-turn 注册表）；
  ③ overlay 打开期间（Esc 归 overlay，但状态行仍承诺可中断，见 P1-2）；
  ④ watcher 的 arm 窗口外（turn 已结束/未开始）。
- 影响：上述场景中“esc to interrupt”仍显示，但按键无人消费；用户以为死锁。
- 方案：见 §5 阶段 B（能力化）与阶段 C（会话级 watcher + 优先级仲裁）。

### P1-4 Windows 裸 ESC 检测条件苛刻

- 证据：`ui/keyhandler_windows.go:106-130`（`peeked != 1` 直接放弃；把多字节序列当 Alt/导航）、
  `:86-104`（ESC 之前存在“可产生输入”的记录即放弃，保守保留草稿）。
- 触发：上一次读取后有草稿/粘贴残留、或输入缓冲里 ESC 前有其他字节。
- 影响：ESC 被漏检/被当作普通输入吞掉；“按了没反应”的 Windows 本地诱因。
- 方案：见 §5 阶段 D。

### P2-5 三方 stdin 所有权竞争，无统一优先级表

- 证据：KeyHandler 轮询（`ui/keyhandler.go`）、busy capture（`commands/chat_busy_input.go:30-41` 的
  Suspend/Resume）、模态 overlay（`ui/transcript_pager.go:689-704`、`ui/fullscreen_list.go:394`、
  `ui/debug_overlay.go:162`）各自直接读同一 stdin，靠“谁在跑”隐式仲裁。
- 影响：优先级不可推理：overlay 打开时 ESC 只关 overlay；capture 与 KeyHandler 的
  suspend/arm 竞态窗口可能短时无人消费。
- 方案：见 §5 阶段 F（统一输入仲裁器/处理器栈），短期先在阶段 B 把“消费者状态”显式化。

### P2-6 cleanup 结果与 UI 诚实性脱钩

- 证据：`actor.Interrupt` 1.5s 上限（`commands/chat_interrupt.go:191-193`）、总预算 5s（`commands/chat.go:386`）；
  cleanup 无论是否真正停止，结束即 `finishInterruptCleanupUI` 清 Stopping（`commands/chat.go:458-465`）。
- 影响：极端情况下 UI 显示已回到 Ready，但 actor/子代理仍在跑，形成“已停止”的假象。
- 方案：见 §5 阶段 E（cleanup 返回结果码；超时不静默清 Stopping）。

### P2-7 子代理级联受 `userID` 非空限制

- 证据：`commands/chat_interrupt.go:62-65`（`SessionUserID` 为空直接返回，不级联）。
- 影响：主 actor 被中断，子会话可能继续执行并消耗 token。
- 方案：见 §5 阶段 E（userID 为空时回退按 parent/ancestor 关系扫描，`localAgentHasAncestor` 已具备）。

### P2-8 Web 与 TUI 中断语义不一致

- 证据：Web `session.Interrupt()` 丢弃排队输入（`commands/web_handlers.go:666-673`）；
  TUI Esc 保留排队输入（`commands/chat_escape_interrupt.go:32` → `InterruptPreservePendingInput`）。
- 影响：同一会话多客户端操作时行为漂移；用户不清楚排队消息是否保留。
- 方案：见 §5 阶段 E（统一 preserve 语义或按参数区分 stop / stop-and-discard）。

### P2-9 非交互/PTY 宿主在 busy 期间没有 ESC 消费者

- 证据：`shouldInitializeChatKeyHandler` 要求交互终端（`commands/chat_setup.go:306-311`）；
  watcher 要求 `KeyHandler.IsEnabled()`（`commands/chat_escape_interrupt.go:10`）；
  busy capture 要求 `shouldUseInteractiveLineEditor`（`commands/chat_input_queue.go:1118-1123`）
  且 `SupportsCancelableInteractiveInputRead()`（`ui/inputbox_editor.go:312-321`）。
- 触发：winpty/MobaXterm/SSH 管道或字符设备场景（`chat.go:1252-1266` 明确存在该输入路径），
  turn 运行期间按 `Esc`。
- 影响：与 P1-2 叠加时表现为“提示可中断但按键无效”；仅 Ctrl+C 信号可用。
- 方案：阶段 B 先把该状态显式标为不可中断；阶段 C 提供无 TTY 的替代中断入口。

### P2-10 重复 ESC / 停止后 ESC 无反馈

- 证据：Stopping 或已中断后再次 ESC 被静默丢弃（`commands/chat_escape_interrupt.go:27-31`）；
  Web 端运行中 ESC 无任何事件产出。
- 影响：用户无法区分“已受理 / 无目标 / 未消费”，加重“无法退出状态”的感受。
- 方案：忽略重复 ESC 时给一次性轻提示（如“已在停止中…”）；Web 端在阶段 A 补齐真实反馈。

### P3-9 取消来源标记的完备性（残余风险）

- 证据：本地 ctx 标记（`commands/chat_setup.go:301-304`）与注册表 `cancel_source`
  （`internal/api/skills/handler.go:2027-2030`）已对齐同一字面量 `user_interrupt`；
  但外部/远程取消不会置 CLI 侧 `ChatSession.interrupted`。
- 影响：依赖 `IsInterrupted()` 的守卫（goal 续跑、自动重跑）在“跨进程取消”场景不生效（当前场景较窄，
  同进程 Web 路径会正常置位）。
- 方案：见 §5 阶段 E（守卫补 `ctx.Err()==Canceled || CancelSource!=parent_context` 兜底）。

### 非目标（明确不做/已排除）

- 不改变“限流重试策略/次数/退避曲线”（`internal/llm/retry_policy*.go`）——限流是外部事实，本方案只保证
  ESC 可达且可立即取消退避。
- 不重构 actor 生命周期与租约模型；不引入跨进程取消总线（注册表为进程内，已有文档化边界）。
- 不改变 overlay 用 ESC 关闭自身的行为（符合惯例），只解决“打开 overlay 时 esc to interrupt 提示”的误导。

## 5. 实施方案（阶段 A–F）

> 原则：先解决“按键有没有消费者”（A/B/C），再解决“消费者是否可靠”（D），最后做语义/结构收敛（E/F）。
> 每阶段独立可发布、可回滚；A/B 为最小可用修复（对应本次问题）。

### 阶段 A：Web 端 `Esc` → 停止当前回合（P1-1）

- 目标：运行中且无模态打开时，`Esc` 与点击 Stop 完全等价（含 resumed/detached 回合）。
- 改动点：
  - `frontend/src/pages/workspace-page.tsx`：把 `handleStopResponding`（`:231-240`）暴露给键盘层；
  - 新增/复用一个 workspace 级 hook（参考 `hooks/workspace/session-backtrack/use-session-backtrack-keyboard.ts:57`
    的 window keydown 模式），条件：`event.key === "Escape"` 且无 modal/菜单/对话框处于打开状态、
    `currentSessionResponding` 为真；
  - `preventDefault` 仅在真正处理时调用，避免与现有 overlay 的 Esc 冲突。
- 实现要点：不新造停止 API，直接复用现有 `stopResponding / stopResumedTurn` 分支
  （`workspace-page.tsx:233-239`，后者调用 `session-turn-control.ts` 的 `interruptSessionTurn`）。
  与回溯导航不冲突：`canBacktrack` 要求 `!isResponding`（`use-session-backtrack.ts:100-103`），
  并可复用其 modal 判定（`document.querySelector('[aria-modal="true"]')`）避免抢键。
- 测试：新增前端单测——运行中 Esc 触发一次停止请求；modal 打开时 Esc 不触发；idle 时 Esc 无副作用。
- 验收：Web 页面运行中按 Esc，行为与 Stop 按钮一致，且只发一次 interrupt。
- 回滚：删除 keydown 注册即可。

### 阶段 B：`interruptible` 能力化（P1-2）

- 目标：状态行只在“本 UI 确实 armed 了 ESC 消费者”时显示 `esc to interrupt`。
- 改动点：
  - 会话层新增只读查询（建议放 `commands/chat_escape_interrupt.go` 或 `chat_interaction.go`）：
    `chatEscapeInterruptAvailable(session) bool`，综合：
    ① `KeyHandler.IsEnabled() && armed && !suspended`（需给 `ui.KeyHandler` 增补 `Armed()`/`Suspended()` 只读方法）；
    ② busy capture 活跃标志（`chat_busy_input.go` 已有 external input capture 状态，可复用/补只读）；
    ③ 非 `NoInteractive`、非 `JSONOutput`；
  - `chatDynamicStatusAction` 调用点（`chat_interaction.go:2402` 上下文）把“状态种类可中断”与
    “消费者可用”做 AND；不可用时 Web payload 的 `interruptible=false`（`chat_interaction.go:891-925`），
    TUI 后缀替换为 `(N • stop via /stop or web)` 之类的可执行提示（文案可配置）。
- 实现要点：保持函数纯度——把“能力”作为参数注入（如 `chatDynamicStatusAction(status, mode, escAvailable bool)`），
  避免在渲染层直接查运行时状态，便于测试。
- 测试：表驱动单测覆盖 8 种状态 × 消费者有无 的矩阵；现有
  `chat_interaction_test.go:253-261`（Stopping 不显示提示）保持通过。
- 验收：无消费者的场景不再出现 `esc to interrupt`；有消费者时行为不变。
- 回滚：开关/常量回退为“始终按状态种类”。

### 阶段 C：TUI 会话级 ESC 消费者（P1-3）

- 目标：只要本会话有在途回合（不论由 TUI、Web、actor 还是监督唤醒启动），TUI 的 Esc 都能中断。
- 改动点：
  - 新增会话级 watcher（建议 `commands/chat_escape_interrupt.go` 扩展）：生命周期与
    `ChatSession` 一致，turn 开始时 Arm、turn 结束/清理完成时 Disarm；与
    `sendMessage` 局部 watcher 用引用计数/角色互斥，避免双消费者重复消费；
  - 与 busy capture 的优先级：capture 活跃时由 capture 消费（现有行为），否则由会话级 watcher 消费；
  - 与模态的优先级：overlay 打开时 Esc 仍优先关 overlay（保持现状），但这期间状态行的
    `interruptible` 依阶段 B 应为 false 或提示“先关闭面板”。
- 实现要点：需要 actor/事件桥告知“turn 开始/结束”的稳定钩子（`ensureChatRuntimeEventBridge` 的
  `BeginRun/EndRun`，`chat_actor_executor.go:162-165`），避免仅绑定 `sendMessage`。
- 测试：集成测试——web 注入回合（不经过本地 sendMessage）+ `KeyHandler.Notify()` → 断言
  `IsInterrupted()==true` 且 actor 被 stop；模态打开时 Notify 不触发停止。
- 验收：TUI 与 Web 同时挂同一会话时，TUI Esc 与 Web Stop 等效。
- 回滚：watcher 常驻改为原 `sendMessage` 作用域。

### 阶段 D：Windows 裸 ESC 检测增强（P1-4）

- 目标：输入缓冲中 ESC 前存在其他字节时仍能识别为中断，而不是整体放弃。
- 改动点：`ui/keyhandler_windows.go:106-130`（管道 `PeekNamedPipe` 路径）与 `:86-104`
  （console records 路径）：
  - 允许“ESC 前有普通输入”：把 ESC 之前的内容保留/回灌至缓存（或标记为 pending input 交还编辑器），
    只消费 ESC 字节；
  - 仅对真正的转义序列前缀（`ESC [` / `ESC O` 且后续仍在缓冲）保持“当作 Alt/导航”的现有判断，
    并设定明确的字节上限（超限按裸 ESC 处理）。
- 测试：Windows 单测（可用 `testing` 抽象 reader 注入字节流）：`"abc\x1b" → 中断 + "abc" 保留`、
  `"\x1b[A" → 方向键不触发中断`、连续两次 ESC 的去重。
- 验收：带草稿时按 ESC 仍可中断且不丢草稿。
- 回滚：恢复 `peeked == 1` 保守判定。

### 阶段 E：cleanup 诚实性 / 级联 / 语义统一 / 守卫兜底（P2-6/7/8 + P3-9）

1. `interruptActiveRuns` 返回结果结构（`stopped bool / timedOut bool / childrenSkipped []string`），
   `finishInterruptCleanupUI` 仅在 `stopped` 时清 Stopping；超时时保留并追加可重试提示（`chat.go:386-389、458-465`）；
2. `interruptChildAgentRuns`：`userID` 为空时仍按 parent/ancestor 扫描
   （`chat_interrupt.go:62-65` 放宽；复用 `localAgentHasAncestor`）；
3. Web stop 语义对齐：`handleWebInterrupt` 改为 preserve（或加 `discard_pending` 参数，默认 preserve），
   与 TUI 一致（`web_handlers.go:666-673`）；
4. 守卫兜底：`shouldAutoContinueActiveGoalDecision` / `shouldAutoRetryTurnError` 增加
   `ctx.Err()==context.Canceled || execution.CancelSource(ctx)!="parent_context"` 判定
   （`chat_goal_auto_continue.go:110-149`、`chat_turn_auto_retry.go:100-111`）。
- 测试：cleanup 超时不进 Ready；空 userID 的子会话被中断；Web stop 保留排队输入；
  取消来源非 parent_context 时不触发续跑/重跑。

### 阶段 F：统一输入仲裁（P2-5，中期）

- 目标：把 ESC（及其他键）的消费者从“多点各自读 stdin”收敛为显式优先级栈。
- 建议模型（由高到低）：`modal/overlay > busy capture > 会话级 ESC watcher > 空闲编辑器`；
  每个消费者注册/注销声明“我是否要独占 stdin / 是否只旁路监听”，`KeyHandler` 的 arm/suspend
  由仲裁器统一驱动，替代现有的散点 Suspend/Resume（`chat_busy_input.go:30-41`）。
- 交付：设计文档 + 迁移步骤（保留旧路径降级开关），避免一次性大重构。
- 测试：消费者矩阵单测；交互回归（TUI 本地回合 / web 注入回合 / overlay / 审批输入 / 粘贴）。

## 6. 测试计划

| 层级 | 用例 | 位置建议 |
| --- | --- | --- |
| Go 单测 | `interruptible` 矩阵（状态 × 消费者可用性） | `commands/chat_interaction_test.go` |
| Go 单测 | 会话级 watcher：web 注入回合 + `Notify()` → 中断；模态打开 → 不中断 | `commands/chat_escape_interrupt_test.go` |
| Go 单测 | Windows ESC 字节流：`"abc\x1b"`、`"\x1b[A"`、连续 ESC | `ui/keyhandler_windows_test.go` |
| Go 单测 | cleanup 结果码：正常停止 / actor 超时；`userID==""` 级联 | `commands/chat_interrupt_test.go` |
| Go 单测 | 取消来源兜底：`CancelSource!=parent_context` 时不续跑/不重跑 | `commands/chat_goal_auto_continue_test.go` 等 |
| 前端单测 | 运行中 Esc 触发停止一次；modal 打开不触发；idle 无副作用 | `frontend/src/pages/workspace-page.test.tsx`（或就近 hook 测试） |
| 手工/集成 | 限流重试期间 ESC 立即终止（mock provider 注入 429+长退避） | `chat_tty_live_loop_*` 测试基建 |
| 手工/集成 | TUI+Web 双客户端：TUI Esc、Web Stop 等效；子代理同步停止 | 手工 runbook（可补 `docs/plan` 运行手册） |
| Go 单测 | 空闲 Esc 仍进回溯；有草稿 Esc 不清草稿（防阶段 C 回归） | `ui/inputbox_editor_test.go`（既有用例） |
| 交互回归 | busy capture 与 watcher 不双触发：capture 活跃时仅 capture 消费 | `commands/chat_escape_interrupt_test.go`、`commands/chat_interaction_test.go` |
| 手工/集成 | 非交互/PTY 场景 busy 期间 Esc 行为符合能力化提示（阶段 B 后） | winpty/MobaXterm/SSH 手工验证 |

回归红线：现有 `chat_escape_interrupt_test.go`、`chat_interrupt_test.go`、
`chat_exit_test.go`、`session_runtime_handlers_test.go`（interrupt 契约）、
`session-turn-control.test.ts` 必须全绿。

## 7. 验收标准

- [ ] Web 页面运行中按 `Esc`：与 Stop 按钮完全等效，且只发一次 interrupt（幂等）。
- [ ] 无 ESC 消费者的场景不显示 `esc to interrupt`；显示时按键必然可中断（能力与承诺一致）。
- [ ] TUI 在 web/actor 注入回合下按 `Esc` 能中断，且不影响排队输入（preserve 语义）。
- [ ] Windows 带草稿按 `Esc` 可中断且草稿保留；方向键不受影响。
- [ ] 限流重试退避中按 `Esc`：退避立即返回，回合在秒级内进入 Stopping 并回到 Ready。
- [ ] interrupt cleanup 未真正停止时，UI 不谎报 Ready（保留 Stopping + 可重试提示）。
- [ ] 子代理在 `userID` 为空时仍被级联中断。
- [ ] 取消后不触发 goal 自动续跑 / 回合自动重跑（含 cancel_source 非 parent_context 场景）。

## 8. 风险与回滚

| 风险 | 缓解 |
| --- | --- |
| 新增全局 Esc 绑定与现有 overlay Esc 冲突 | 统一“modal 未打开”判定；preventDefault 只在处理时调用；阶段 A 单测覆盖 |
| 会话级 watcher 与 busy capture 双消费 | 引用计数/角色互斥；阶段 C 集成测试覆盖双路径 |
| `interruptible` 能力化导致状态行文案频繁闪变 | 能力状态加去抖（仅在 armed/suspended 变化时更新）；文案保持稳定模板 |
| Windows 改动引入输入错位（丢字符） | 字节回灌需严格保序；单测覆盖混合输入；必要时灰度高危路径（旧行为开关） |
| cleanup 结果化改变现有 UI 行为 | 仅在“超时未停止”这一分支改变行为；其余路径保持现语义 |

整体回滚：各阶段独立提交，可单独 revert；无数据迁移、无持久化契约变更。

## 9. 关键证据索引（复核用）

- 入口与消费者：`commands/chat_send.go:85-86`、`commands/chat_escape_interrupt.go:9-48`、
  `commands/chat_busy_input.go:16-41、82-90`、`ui/keyhandler.go:40-106`、
  `ui/keyhandler_windows.go:86-130`、`ui/inputbox_editor.go:312-321、2184`
- 会话层：`commands/chat.go:276-308、364-389、391-434、458-465、1283-1289`、
  `commands/chat_interrupt.go:16-41、62-97、182-199`、`commands/chat_exit.go:19-68`
- 回合注册表：`internal/api/skills/session_active_turn.go:79-166、173-214、248+`、
  `internal/api/skills/handler.go:2015-2031、2078-2086`、
  `internal/api/skills/session_runtime_handlers.go:849-872、983-1000`
- Web：`commands/web_handlers.go:653-673`、`frontend/src/pages/workspace-page.tsx:226-240`、
  `frontend/src/api/runtime/session-turn-control.ts`
- Loop/执行：`internal/agent/loop.go:511-527、715-728`、`internal/agent/scheduler.go:228`、
  `internal/agent/tool_parallel_scheduler.go:316`、`internal/agent/subagent_batch_coordinator.go:847/1163/1243`、
  `internal/agent/subagent_retry.go:169`、`internal/llm/retry_policy.go:589-602`、
  `internal/llm/retry_executor.go:87-89`、`cmd/aicli/functions/shell.go:107`
- 守卫：`commands/chat_turn_auto_retry.go:100-111`、`commands/chat_goal_auto_continue.go:110-149`、
  `internal/chat/actor.go:49-64、149-158`
- 展示层：`commands/chat_interaction.go:891-925、2402-2447`
- 监督取消通道：`internal/supervision/execution_supervisor.go:80-84、540-549`、
  `internal/toolbroker/execution_host_adapters.go:29-51`
- actor 中断实现：`internal/chat/actor.go:35、580-591、1206-1241`
- ESC 优先级矩阵（§2.6）：`ui/inputbox_editor.go:1261-1280`、`commands/chat_composer.go:438-443`、
  `commands/chat.go:1352-1360`、`frontend/src/hooks/workspace/session-backtrack/use-session-backtrack.ts:100-103`

## 10. 实施记录（2026-09-18）

| 阶段 | 状态 | 改动 | 验证结果 |
| --- | --- | --- | --- |
| A | **已实施** | 新增 `frontend/src/hooks/workspace/use-escape-stop-responding.ts`；接线 `frontend/src/pages/workspace-page.tsx` | 新增 6 条 vitest 全过；`tsc --noEmit` 通过 |
| B | **已实施** | `ui/keyhandler.go` 增加 `Armed()/Suspended()`；新增 `commands/chat_escape_availability.go`；`commands/chat_interaction.go` 增加 `...CompletionAndEsc` 并在两处生产调用点传真实能力 | 新增单测通过；既有状态行矩阵用例保持通过 |
| C | **已实施** | `chat_escape_interrupt.go` 改为会话级引用计数消费者（重叠回合共享 Arm/goroutine，释放最后一个才 Disarm 并排空）；`chat_actor_executor.go` 两处 actor 回合入口、`chat_actor_host.go` 监督唤醒回合均注册消费者；busy capture 仍以 `Suspend()` 保持最高优先级 | 新增 3 条消费者单测（共享 Arm、释放后停止消费、actor-only 回合可中断）全过；既有 watcher 用例保持通过 |
| D | **已实施（控制台路径）** | `ui/inputbox_editor_windows.go` 新增 `readConsoleInputRecords/writeConsoleInputRecords`；`ui/keyhandler_windows.go` 改为“前缀保留式”消费（排空至 ESC 后回注前缀） | Windows 单测改写并通过；管道（winpty/SSH）路径维持保守策略（仅裸 ESC），其增强列入后续 |
| E | **已实施** | P2-6：`chat_interrupt.go` 增加 `chatInterruptCleanupOutcome`（stopped/actors/failedActors），`interruptActorRun` 返回是否真正停妥（仅超时/取消计失败），`chat.go` 仅在 `stopped` 时清 Stopping、否则保留并追加可重试提示；P2-7：userID 为空回退 `SessionStorageAllLister`；P2-8：`discard_pending` 默认 preserve；P3-9：取消守卫 | 新增 3 条 cleanup 单测（未完成保留 Stopping、完成清 Stopping、nil host 幂等）；既有中断/级联用例保持通过 |
| F | **P0~P2b 已实施**（P3 删旧路径待真实终端验证） | 见 §12/§12.6；仲裁器 + Snapshot；P1 不窄化读仲裁器；P2a 目标态映射与分叉遥测；P2b 单写者路径（开关打开时跳过散点 Arm/Suspend，全部由 `syncChatInputArbitration` 驱动）；灰度开关 `AICLI_CHAT_INPUT_ARBITRATION_ENFORCE`（默认关）；P2-10 重复 Esc 提示 | 仲裁/可用性/消费者/cleanup/P2a/P2b 共 23 用例 `-race` 全绿；实机验证与 P3 步骤见 §12.6 |

验证方式与备注：

- 隔离验证：用 `git worktree`（HEAD）叠加本次全部改动后执行
  `go test ./cmd/aicli/commands/ ./cmd/aicli/ui/ -count=1`。结果：`ui` 全绿；
  `commands` 仅 `TestRestoreChatStateFromRuntimeSessionRestoresRouteTransparency` 失败，
  该失败已在**纯 HEAD（无本次改动）复现**，判定为仓库既有缺陷（路由透明性断言，
  `chat_session_test.go:997`），与本次改动无关。
- 回归修复：P3-9 初版守卫把 `context.DeadlineExceeded` 一并拦截，导致既有用例
  `TestSendMessage_AutoContinuesActiveGoalAfterInitialError` 失败（该用例要求 deadline
  初始错误可被 goal 续跑恢复）。已收窄为仅拦截 `context.Canceled`，deadline 恢复路径保持原行为。
- 既有断言更新：`chat_retry_status_live_e2e_test.go` 原写死 `esc to interrupt` 文案，
  现按“提示由消费者能力决定”改为只校验 `(Ns • ` 时钟前缀。
- 未实施项：阶段 F 的 P2/P3（单一写者与清理，需真实 TTY/PTY 灰度验证，入口判定见 §12.5）；
  阶段 D 的管道（winpty/SSH）路径增强（当前保持“仅裸 ESC”的保守策略，避免吞掉远端按键）。
- 既有测试建模更新：`chat_surface_status_matrix_test.go` 两处状态行用例原先用无消费者的
  空会话断言 `esc to interrupt`；阶段 B 后提示由真实能力决定，测试改为提供活跃消费者
  （`newInterruptibleStatusTestSession`），断言意图（时钟推进 + 可中断后缀）保持不变。
- **最新回归（P1 后，2026-09-18）**：`go test ./cmd/aicli/commands/ ./cmd/aicli/ui/ -count=1`，
  `ui` 全绿；`commands` 仅剩 `chat_session_test.go:997`（已在纯 HEAD 复现的既有缺陷）。
  期间并发写入者曾造成 `chat_model_switch_test.go:453`、`chat_command_result_test.go:1006`
  失败，现已随其改动收敛；本次改动未触碰对方文件。
- **P2a 后全量回归补充**：另出现 `TestExecuteStructuredProviderCommandPinnedProviderSkipsPickerStage`
  的顺序相关失败——单跑与“本次改动全集”组合跑均通过，且该用例落在并发写入者正在修改的
  `chat_model_command.go`/`chat_model_picker.go`（21:11 仍在写入），判定非本次改动引入；
  最终以其收工后的全量回归为准。
- **独立复验（2026-09-19 凌晨，全量口径）**：
  - 后端：`go vet ./...` 全绿；`go test ./...` 仅 `cmd/aicli/commands` 的
    `TestRestoreChatStateFromRuntimeSessionRestoresRouteTransparency` 失败，已在纯 HEAD
    （`git worktree` detached `8c5d9797`）复现，判定仓库既有缺陷、与本次改动无关；
    核心 ESC/仲裁/中断/cleanup 用例 122 条 `-race` 全绿。
  - 前端：全量 vitest 2547/2547 全绿（含本次新增用例）；全量 Playwright 102 用例中
    14 失败（trajectory 4、sidebar-session-actions 4、workspace-chat 2、usage-observability 2、
    design-tokens 1、sidebar-directory-indent 1），全部在纯 HEAD 基线（另一 worktree、自带
    dist 构建）复现同一失败集，与本次改动无关。
  - 后端全模块收口复验（2026-09-19 晨）：`go build ./...` 与 `go vet ./...` 全绿；
    `go test ./...` 全模块仅 `cmd/aicli/commands` 一包失败，单独重跑该包复核确认失败用例
    仍仅有 `TestRestoreChatStateFromRuntimeSessionRestoresRouteTransparency`（无新增失败用例），
    维持上述既有缺陷归判不变。

## 11. 审查记录（2026-09-18 复核）

本轮审查对文档做了以下修正/补全（均以代码证据为准）：

1. **修正 P1-3 过宽表述**：同进程 Web 注入 prompt 走 `InputQueue` → 本地 `sendMessage`
   （`commands/web_handlers.go:676-711`），watcher/capture 仍在 arm 窗口内；无消费者边界收敛为
   非交互/PTY、无 TUI 进程宿主、overlay 占用期、arm 窗口外。
2. **补入第 5 条中断通道**：监督执行期限取消（`execution_supervisor.go:540-549` →
   `AgentSessionRunInterrupter.InterruptRun` → `Controller.Close`）。
3. **补全 actor 侧中断语义**：`handleInterrupt` 不等待 run 返回，250ms grace 后由
   `SessionHub.StopContext` 兜底（`actor.go:35、1206-1241`）。
4. **新增 §2.6 ESC 优先级矩阵**：覆盖空闲/忙碌/审批/Stopping/模态/Web 各状态，
   并修正“TUI 空闲 Esc 即中断”的错误直觉（空闲空草稿=回溯，有草稿=无操作）。
5. **新增 P2-9**（非交互/PTY busy 期间无消费者）与 **P2-10**（重复 ESC 无反馈）。

遗留待验证（实施前建议实测确认，不在本文档结论内）：

- winpty/SSH PTY 下 `SupportsCancelableInteractiveInputRead()` 与 Windows console records /
  `PeekNamedPipe` 两条路径的实际可用性；
- 审批/提问 priority 模式按 Esc 后 capture 循环退出的 UI 表现（是否会留下无人回收的 prompt 行）；
- 纯 runtime-server（无 aicli TUI 进程）部署下停止按钮/ESC 的完整链路（本方案以同进程 Web 为准）；
- `interruptActiveSessionRun` 是否给 run ctx 打 `cancel_source` 标记（影响上层取消分类与守卫兜底）。

## 12. 阶段 F：统一输入仲裁（2026-09-18；设计已交付，P0~P2b 已实施，P3 待验证）

> 本文是阶段 F 的设计文档 + 迁移步骤 + 实施记录（§12.5–§12.7）。前置事实：当前 stdin 消费者由
> `KeyHandler.Arm/Suspend`（散点调用：`chat_busy_input.go:30-42`、`chat_escape_interrupt.go`）
> 与 `InputQueue` capture 各管一半，优先级只存在于注释里。

### 12.1 优先级模型（由高到低）

| 级别 | 消费者 | 行为 | 是否独占 stdin |
| --- | --- | --- | --- |
| L0 | modal / overlay / priority popup（审批、提问） | 接管按键；Esc 关面板 | 独占 |
| L1 | busy queued-input capture | 读行、路由排队输入；Esc=中断 | 独占 |
| L2 | 会话级 ESC 消费者（阶段 C） | 旁路监听 ESC；中断当前回合 | 只旁路 |
| L3 | 空闲编辑器 / 回溯导航 | 编辑、历史、Esc=回溯 | 独占 |

不变式：

- 任一时刻最多一个“独占”级别持有 stdin；L2 仅在无独占者时真正 arm KeyHandler。
- `esc to interrupt` 提示 = 存在 L1 或（L2 且 arm 且未 suspend）——即阶段 B 的
  `chatEscapeInterruptAvailable` 语义。P1 已改为“仲裁器快照 OR KeyHandler/capture 位”的
  不窄化实现：modal 期仲裁器按本节目标语义返回 false，而位路径返回 true，分叉由 DebugMode
  日志采集，供 P2 单一写者灰度决策（详见 §12.5）。

### 12.2 接口草案（包内）

```go
type chatInputOwner int

const (
	chatInputOwnerNone chatInputOwner = iota
	chatInputOwnerModal
	chatInputOwnerBusyCapture
	chatInputOwnerEditor
)

type chatInputArbitrator struct {
	mu             sync.Mutex
	owner          chatInputOwner
	modalDepth     int
	captureDepth   int
	escConsumers   int
}

func (a *chatInputArbitrator) BeginModal() (release func())
func (a *chatInputArbitrator) BeginBusyCapture() (release func())
func (a *chatInputArbitrator) BeginESCConsumer() (release func())
func (a *chatInputArbitrator) EditorActive() (release func())
func (a *chatInputArbitrator) Snapshot() chatInputArbitrationSnapshot // 供状态行/诊断
```

- `KeyHandler` 的 `Arm/Disarm/Suspend/Resume` 只允许由仲裁器（和测试）调用；散点调用收敛为
  `Begin*/release`。
- `chatInputArbitrationSnapshot{EscAvailable bool, Owner chatInputOwner, BlockedReason string}`
  供状态行与诊断日志使用。

### 12.3 迁移步骤（每步可独立回滚）

1. **P0 影子模式**（无行为变化）：引入仲裁器与 `Snapshot()`；散点路径同时调用 `Begin*/release`，
   但 `Arm/Suspend` 仍按旧逻辑执行；只增加诊断日志对比“仲裁器判定 vs 实际位”。
   开关：`chat.input_arbitration_shadow`（默认开）。
2. **P1 只读替换**：`chatEscapeInterruptAvailable` 改读 `Snapshot()`；行为等价（状态行矩阵用例不变）。
3. **P2 单一写者**：`Arm/Disarm/Suspend/Resume` 全部改为仲裁器驱动，删除散点调用；
   开关 `chat.input_arbitration_enforce`（默认关，灰度开启后旧路径保留一个版本）。
4. **P3 清理**：删除开关与旧路径，补齐 L0~L3 优先级矩阵单测 + winpty/SSH 集成回归。

### 12.4 测试矩阵

| 场景 | 期望 |
| --- | --- |
| modal + 运行中 | Esc 关 modal；不停止回合；提示按 §12.1 不变式渲染 |
| capture + 运行中 | Esc 中断一次；草稿保留 |
| 仅 L2（actor/wake 回合） | Esc 中断；无 capture 时不双消费 |
| 编辑器空闲（无在途） | Esc 走回溯；不产生中断请求 |
| 并发注册/释放（表驱动） | 所有权单调；无 Suspended 残留（`-race`） |
| 诊断快照 | `Snapshot()` 与实际可中断性一致 |

冲突解决：L0 打开期间到达的 ESC 由 L0 消费（不冒泡）；L1 退出瞬间残留的 ESC 由 L2 消费；
L2 在 `IsInterrupted()` 为真时丢弃重复 ESC，但每个中断周期给出一次“停止处理中”提示
（P2-10 已实施，见 §12.5）。

### 12.5 P0 实施记录（2026-09-18）

- 代码：`commands/chat_input_arbitration.go`（`chatInputArbitrator` +
  `chatInputArbitrationSnapshot` + `beginChatInputShadowLevel`）；`ChatSession` 增加
  `inputArbitrationMu/inputArbitration`。影子登记点：ESC 消费者
  （`chat_escape_interrupt.go`）、busy capture（`chat_busy_input.go`）、priority prompt
  两处（`chat_surface_output.go`，modal 级）。
- 行为不变性：未改动任何 `Arm/Disarm/Suspend/Resume` 调用与 capture 路由；仲裁器仅登记并在
  DebugMode 输出 `[input-arbitration] ... legacy_esc_available=...` 快照日志，供灰度期对比。
- 测试：`chat_input_arbitration_test.go` 4 条（L0~L3 优先级矩阵、幂等释放、nil 安全、
  ESC 消费者引用计数联动），`-race` 全绿。
- **P1 已实施（不窄化读仲裁器）**：审查确认 priority prompt 的 Esc 经
  `capture.Cancelled()` → `interruptChatTurnFromBusyInputCancel` 是真实中断路径，而 TUI
  其余模态（面板/选择器）的 Esc 语义未逐一验证。因此 P1 落地为
  `chatEscapeInterruptAvailable = 仲裁器快照 OR KeyHandler/capture 位`
  （`chat_escape_availability.go`）：行为等价、既有状态行矩阵用例零改动，同时 modal 期
  “仲裁器 false vs 位路径 true”的分叉会写入 DebugMode 日志
  （`arbitrated_esc=` / `bits_esc=`），作为 P2 单一写者灰度与 modal 语义决策的真实数据。
- **P2-10 已实施**：Stopping 期重复 Esc 每个中断周期给出一次
  “停止处理中 - 正在取消运行并释放资源”提示（`escapeStoppingNoticeShown` 原子门，中断开始与
  `ResetInterrupt` 时复位），不再完全静默；测试
  `TestChatEscapeStoppingNoticeIsRateLimitedPerInterrupt`（`-race`）。

### 12.6 P2a 实施记录与灰度手册（2026-09-18）

代码（`chat_input_arbitration.go`）：

- 目标态映射 `chatInputArbitrationDesiredKeyHandlerState`：`Arm = ESC 消费者>0`、
  `Suspend = captureDepth>0`；modal 不改变 KeyHandler 位（P2b 决策点，见 §12.5）。
- `syncChatInputArbitration` 在每次仲裁转移后比较期望态与实际位：分叉恒在 DebugMode
  记录（`[input-arbitration] divergence ...`）；仅当灰度开关打开时纠偏。
- 灰度开关：`AICLI_CHAT_INPUT_ARBITRATION_ENFORCE`（默认关；P2b 落地时改接 runtime
  config `chat.input_arbitration_enforce`）。
- 测试：映射表、与散点接线逐位一致、漂移纠偏（关/开两态）、开关解析，共 4 条。

P2b（删除散点 Arm/Suspend，单一写者）进入条件与验证步骤：

**P2b 实施状态（2026-09-18）：单写者路径已实现于灰度开关内。** 开关打开时
`chat_escape_interrupt.go`/`chat_busy_input.go` 跳过散点 `Arm/Disarm/Suspend/Resume`，全部由
`syncChatInputArbitration` 驱动；开关关闭时旧路径保留（按方案“旧路径保留一个版本”）。单测覆盖：
单写者 Arm/Disarm 与共享引用计数、capture 期 Suspend/Resume、漂移纠偏、开关解析。剩余仅为下述
步骤 2–4 的真实终端验证，通过后即可删除旧路径分支（P3）。

1. **正常路径零分叉**：以 DebugMode 跑一轮完整交互（前台回合、队列输入 capture、审批
   priority prompt、goal 续跑、监督唤醒、Web 注入），grep
   `[input-arbitration] divergence` 应为空——出现分叉即说明存在绕过仲裁器的位写入，先补接线。
2. **模态策略取样**：grep `arbitrated_esc=false bits_esc=true` 的时段并对照当时 UI：若这些
   时段（priority prompt 打开）按 Esc 确实中断回合，则 P2b 保持“modal 不改变位”；若 Esc
   只关闭面板，则 P2b 让 modal 参与 `Disarm/Suspend`。
3. **强制模式彩排**：`AICLI_CHAT_INPUT_ARBITRATION_ENFORCE=1` 重跑步骤 1 的场景，确认
   ESC/排队输入/审批行为与关闭时逐项一致。
4. 满足 1–3 后删除散点调用，并用 `-race` 全量回归 + Windows 真实终端与 winpty/SSH 各一轮
   （与阶段 D 管道路径的验证合并进行）。

### 12.7 远程实机验证记录（2026-09-18，Web 面）

目标：`session_20260918215257_5eb4PdXc`（TUI 进程 + 其 Web 面 `http://127.0.0.1:59661/web/`）与
控制台 `http://localhost:5193/workspace/sessions/session_20260918192107_ZpWf5Uyd`（React 前端，
阶段 A 的 window 级 Esc 路径）。

| # | 场景 | 操作 | 证据 | 结论 |
|---|---|---|---|---|
| 1 | 控制台 Esc→stop（阶段 A） | Ctrl+Enter 发送 120 行长生成 → 1s 后 Esc | 前端 dispatch `POST /api/runtime/sessions/{id}/runtime/commands`（fetch 钩子记录）；`active_turn=null`；UI `stopped` + "RESPONSE STOPPED" + "已停止"；证据产物 `agent-chat-response-*.json` | ✅ 通过 |
| 2 | 控制台 Esc→stop（流式半途） | 500 行提示词 → 6s 后 Esc（仅 reasoning 阶段） | `active_turn=null`；"Response stopped before any text was returned."；部分推理保留，`/retry` 提示 | ✅ 通过 |
| 3 | 微客户端 Esc→interrupt | 输入框回车发送 300 行 → 3.5s 后 Esc（焦点在输入框） | `POST /web/api/input {"type":"interrupt"}`；turn `interrupted`（13.8s）；按钮复位"发送" | ✅ 通过 |
| 4 | 中断后 TUI 侧清理（E4） | 承接 #3 | 状态页：`model error [USER_CANCELLED, retryable=false]` → `agent.turn.finished` → "当前运行已停止…"/`/retry` 建议；`Turns Running: 0`，无 Stopping/streaming 残留 | ✅ 通过 |
| 5 | 忙时 Web 排队 + Esc 保留（P2-9/P2-8） | 400 行回合运行中经 Web 再注入"只回复两个字符：收到"（`pending_inputs=1`）→ Esc | turn `interrupted`；`pending_inputs→0`；TUI 合成帧显示 composer 已被还原为 `> 只回复两个字符：收到` | ✅ 通过（保留语义生效） |
| 6 | TUI 侧仲裁遥测（F P0~P2b） | — | 进程 `Debug Mode: off`，且需真实 TTY 按键，本轮无法覆盖 | ⏳ 待办 |

**实测发现（两条 UX 缺口，均非本次实现引入）**

1. **微客户端 Esc 仅在输入框聚焦时生效**：`web/js/chat.js` 把 `Escape` 绑在
   `promptEl.addEventListener("keydown")`，鼠标点击"发送"后焦点在按钮上，Esc 无效
   （本轮首次实测即复现）。控制台（阶段 A）用 `window` 级监听，无此问题。建议把微客户端
   Esc 提升到 document 级（保留"快捷键面板打开时先关闭"的既有优先级）。
2. **跨端可见性**：忙时经 Web 注入的排队消息，中断后按 preserve 语义还原到 **TUI composer**
   （`Interaction.SetPromptInput`），Web 页面输入框与 `pending_inputs` 计数均看不到，从 Web 端
   观察形似"消息丢失"。若 Web 是主操作面，可考虑事件回投（`session_interrupted` 载荷带
   restored_prompt）。

**其它观察**：`/debug/chat/status` 的 `AppState parity` 显示 38 行 legacy/derived 差异
（scrollback 重放场景），与中断链路无关，未影响本次结论；`Debug Mode: off` 时
`[input-arbitration]` 日志不可用，P2b 的零分叉检查需以 `--debug` 重跑。
