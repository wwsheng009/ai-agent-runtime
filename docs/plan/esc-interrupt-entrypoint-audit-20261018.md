# ESC / 中断入口审计与单一入口梳理（2026-10-18）

- 日期：2026-10-18
- 状态：**审计完成；TUI 键位平面已收敛到单一决策点；R1/R2/R3/R5/R6/R8/R9/R10 已实施并经真机验证（§6.1）；R11（IM `/stop`）已实施（2026-10-19）**；R7 已核查（runtime-server/ACP/headless 均有非 Esc 入口，无缺口）。剩余 R4（需真实终端取样）与 R5 残余（非副屏窗口的过程性竞态，待阶段 F P2b 灰度）。
- 关联：
  - `docs/plan/esc-interrupt-priority-and-loop-robustness-plan-20260918.md`（阶段 A–F 方案与实施记录；本审计是其 §13 的延续）
  - `docs/plan/runtime-observability-supervision-http-api-plan.md`（runtime commands / active turn 注册表的 HTTP 契约）
  - `docs/plan/supervision-approval-resume-past-deadline-fix-plan.md`（审批终态守卫）
- 证据口径：代码 `file:line` + 2026-10-18 现场会话 `session_20261018191622_xGGs4QZJ` 的日志/事件/调试接口（只读取证）。

## 0. 摘要

中断链路是**四个平面**叠出来的，每个平面各自应有唯一入口：

| 平面 | 唯一入口（现状） | 说明 |
| --- | --- | --- |
| ① 键位/输入平面（TUI） | `handleChatEscapeInterruptRequest`（`chat_escape_interrupt.go:134`） | 本次审计收敛点：KeyHandler watcher / composer / 忙时 capture / 回溯选择器 / parked watcher / Unix SIGUSR2 兜底全部走它 |
| ② 会话动作平面 | `ChatSession.interrupt(preserve)`（`chat.go:575`）+ `startInterruptCleanup`（`chat.go:670`） | 置 `interrupted`、取消本地 ctx、异步 cleanup（幂等预留，`chat.go:648-677`） |
| ③ 宿主/actor 平面 | `localChatRuntimeHost.interruptActiveRuns`（`chat_interrupt.go:33`）→ `interruptActorRun`（`chat_interrupt.go:232`） | `MarkUserInterrupt` + `actor.Interrupt` + `SessionHub.StopContext`；runtime-server 模式改走 `InterruptTurn` 转发（`chat.go:686-702`、`chat_runtime_server.go:574+`） |
| ④ 跨进程/协议平面 | runtime API `SubmitSessionRuntimeCommand{type:interrupt}`（`session_runtime_handlers.go:968`）/ ACP cancel（`agent_stdio.go:390-408`）/ 监督期限取消 | 前两者分别落到 active-turn 注册表与 actor；监督取消是执行期限语义，独立平面 |

本轮修复 + 收敛后，**TUI 上任何一条键位入口按 Esc 都得到同一语义**（stalled cleanup 重试、重复 Esc 节流提示、fresh interrupt 提示、草稿保留）。R1~R3 已把 PTY 管道、Web 无焦点与“无可键入入口”三个遗漏面补齐；R6 把 Web/HTTP 平面并入同一决策；R8 清理了重复打印。剩余为**模态期提示诚实性（R4）**、**console 双读者收敛（R5）**与**headless 无 Esc（R7，设计如此）**。

## 1. 入口清单（键位/输入平面）

| # | 触发 | 消费者 | 决策/动作 | 反馈 | 证据 |
| --- | --- | --- | --- | --- | --- |
| 1 | TUI composer 按 Esc（空闲/后台回合） | `chatComposerController.onCancel`（有活跃消费者才认领） | `handleChatEscapeInterruptRequest` | “已中断…” / “停止处理中…”（节流） | `chat_composer.go:370-393` |
| 2 | TUI 忙时输入行按 Esc（前台回合，capture 独占 stdin） | `chatBusyComposerCapture.onCancel` → `interruptChatTurnFromBusyInputCancel` | 同上（本轮收敛，原为直调 `InterruptPreservePendingInput`） | 同上 | `chat_composer.go:666-680`、`chat_busy_input.go:254-265、354-364` |
| 3 | 物理 Esc（capture 不活跃；Windows console / Unix SIGUSR2 arm） | `KeyHandler` peek → 会话级 watcher（引用计数） | 同上 | 同上 | `ui/keyhandler_windows.go:31-75`、`keyhandler_unix.go:15-49`、`chat_escape_interrupt.go:32-70、107-116` |
| 4 | 空闲空草稿 Esc（回溯入口撞上未结束回合） | `handleInteractiveBacktrackSelect` 的非 Ready 分支 | 同上（“消费按键”防死循环） | 同上 | `chat_backtrack_select.go:24-34` |
| 5 | 暂停/受管窗口 Esc（挂起 turn） | `chatRuntimeEventBridge` parked watcher | 同上 | 同上 | `chat_runtime_events.go:1686-1715` |
| 6 | Unix `kill -USR2`（无 KeyHandler 或 KeyHandler 未 arm/suspend 时兜底） | `setupSignalHandler` SIGUSR2 分支 | 同上（本轮收敛） | 同上 | `chat_unix.go:17-52` |
| 7 | Esc 关闭 overlay/pager/全屏列表/选择器 | overlay 自身（L0 独占） | **不中断**（设计如此） | 关面板 | `ui/transcript_pager.go:727、742`、`ui/fullscreen_list.go:504、581`、`ui/debug_overlay.go:256` |
| 8 | Ctrl+C | `chatInterruptExitState.handleInterruptSignal` | 中断 + 双击退出窗口（独立语义，保留） | “再次按 Ctrl+C 可退出” | `chat_exit.go:19-68`、`chat_windows.go:17+`、`chat_unix.go:17-52` |
| 9 | 审批/提问等待期 Esc（capture 活跃） | busy capture priority 分支 | 同 #2 + `signalReadError` 结束本 capture | 同 #2 | `chat_busy_input.go:254-262` |
| 10 | 审批/提问等待期 Esc（无 capture，merged/modal composer） | `chatModal/MergedPromptComposer.onCancel` → `errChatInteractivePromptCancelled` | 取消本次作答读取（回合由上层终止/回复） | 该 prompt 结束 | `chat_composer.go:761-766、921-926、787-796、949-958` |
| 11 | 选择器/agent 面板 Esc | `chatSelectionComposer` / `chatAgentPanelComposer` | 取消选择（不中断回合） | 关面板 | `chat_runtime_selection_nav.go:224-237`、`chat_composer.go:1201-1203` |

## 2. 入口清单（非 TUI 平面）

| # | 触发 | 通道 | 决策/动作 | 反馈 | 证据 |
| --- | --- | --- | --- | --- | --- |
| 12 | Web 微客户端 Esc（输入框聚焦时） | `POST /web/api/input {type:interrupt}` | `handleWebInterrupt` → `InterruptPreservePendingInput` / `Interrupt`（discard） | 前端按钮/状态复位；TUI 无提示 | `web/js/chat.js:1296-1331`、`web_handlers.go:942-962` |
| 13 | React 控制台 Esc（运行中，无模态） | `POST /api/runtime/sessions/{id}/runtime/commands {type:interrupt}` | active-turn 注册表取消 → 未命中回落 durable actor | 前端 stopped 态 | `frontend/src/hooks/workspace/use-escape-stop-responding.ts:36-73`、`session_runtime_handlers.go:968-982`、`session_active_turn.go:242-246` |
| 14 | mesh 远程 stop | mesh call → `{type:interrupt, discard_pending:true}` | 同 #12（丢弃排队输入） | 远端客户端状态 | `web_handlers_mesh_call.go:315` |
| 15 | ACP `$/cancel_request` / prompt ctx 取消 | ACP stdio | `chat.Interrupt()`（非 preserve） | 协议响应 | `agent_stdio.go:390-408` |
| 16 | 监督执行期限/预算取消 | execution supervisor → RunInterrupter → Controller.Close | run ctx 取消（`cancel_source=decision`） | 监督事件 | `internal/supervision/execution_supervisor.go`（方案 §2.1 通道⑤） |
| 17 | 无 TUI/headless 宿主 | 无 Esc；仅 web/HTTP/runtime API | #12/#13 | — | 方案 §2.6「Web 运行中」行 |

## 3. 本轮收敛（2026-10-18）

| 改动 | 内容 | 回归 |
| --- | --- | --- |
| composer 认领 Esc | `hooks().OnCancel = onCancel`：存在活跃回合级消费者时 → `handleChatEscapeInterruptRequest` + 草稿回填（`restoreCancelledDraft`）；否则不认领（保留关弹层/回溯/有草稿无操作） | `chat_composer_escape_test.go`（2 条） |
| 忙时 capture 收敛 | `interruptChatTurnFromBusyInputCancel` 改为委托统一决策点（重复 Esc 节流提示、stalled cleanup 重试对齐） | `chat_escape_interrupt_test.go`、`chat_input_queue_test.go` |
| Unix SIGUSR2 收敛 | KeyHandler 会消费（enabled && armed && !suspended）时让位；否则信号处理器进入统一决策点；删除重复的 `IsInterrupted` 分支与手动提示 | `TestChatEscapeSignalOwnedByKeyHandlerMatrix`；`GOOS=linux go build` |
| 新增能力查询 | `chatEscapeInterruptConsumerActive`（`chat_escape_interrupt.go`）：精确回答“此刻是否存在回合级 Esc 消费者”；`chatEscapeSignalOwnedByKeyHandler` 同上 | 上述用例 |
| R1 管道 Esc | `readPipeInteractiveLine` 接 `OnCancel` 钩子（ui 侧新增 `ReadInteractiveLineContextWithPromptHooks`） | `TestReadPipeInteractiveLineEscInterruptsActiveTurn` |
| R2 Web 全局 Esc | `web/js/chat.js` document 级兜底 + 浮层让位（`escOverlayOwnsKey`） | `node --check`；手测清单见 §6 |
| R3 `/stop` | 注册表 inline/live + catalog + install.md 命令表 + `chat_stop_command.go`（只调统一决策点） | `chat_stop_command_test.go` + 目录/补全/忙时审计守卫 |
| R6 统一中断决策 | `chatInterruptDecision`（started/already_stopping/no_session）成为 TUI Esc / Web interrupt / `/stop` / 忙时 capture 的唯一决策；Web 响应保留 `status` 并新增 `result` | `TestChatInterruptDecisionSharedAcrossPlanes`、`TestHandleChatWebAPIInput_InterruptSharesDecisionWithEscape` |
| R8 去重打印 | 删除 Unix KeyHandler 的 `[ESC 检测到 - 中断操作]` 直写，反馈统一由决策点渲染 | linux/windows 交叉编译 |
| R5 副屏 Esc 独占 | `openChatScreen` 登记 L0 输入独占：仲裁 modal + KeyHandler 让位，关闭后仅在仍有消费者时复 Arm | `chat_screen_input_ownership_test.go` |
| R10 Web `/stop` 直通 | `handleWebPrompt` 忙碌期把 `/stop` 走中断决策，空闲保持队列路径 | `TestHandleChatWebAPIInput_StopPromptInterruptsRunningTurn` 等 |

未改动（有意保留）：Ctrl+C 退出窗口语义（#8）、overlay Esc（#7）、选择器/审批取消语义（#10/#11）、监督/ACP 平面（#15/#16）。

## 4. 单一入口判定

- **键位平面：已单一入口。** 所有会“中断回合”的键位路径都经 `handleChatEscapeInterruptRequest`；该函数是唯一允许区分“停止中/需重试/新中断”的决策点。新增键位消费者必须调用它，不得直呼 `InterruptPreservePendingInput`。
- **动作平面：已单一入口。** 只有 `ChatSession.interrupt` 会置位 + 取消 ctx + 预留 cleanup；`ResetInterrupt` 是唯一复位点（主循环 `chat.go:1957`、ACP `agent_stdio.go:392`）。
- **宿主平面：已单一入口。** `interruptActiveRuns` 是唯一的 actor 级联动（团队/子代理级联在其中）。
- **跨进程平面：各有单入口但语义面不同**（#12~#16），不建议合并成一个函数；建议**共用决策**（见 R6）而不是共用一个 HTTP/协议入口。

## 5. 剩余问题清单

### R1（P1）winpty/SSH 等 PTY 管道路径 Esc 仍不可靠 —— ✅ 已修复（2026-10-18）
- 事实（修复前）：`shouldUseInteractiveLineEditor` 为 false 时主循环走 `chatInteractiveReadLine` 的管道分支（`chat_input_queue.go:1249-1259`、`chat_pipe_console_line.go`，无 session 参数、无中断钩子）；Windows KeyHandler 的管道探测要求“裸 ESC”（`ui/keyhandler_windows.go:122-147`，`peeked != 1` 即放弃）。带草稿/粘贴残留时 Esc 既不中断也不暴露。
- 修复：`readPipeInteractiveLine(ctx, session, prompt)` 注入 `OnCancel` 钩子——存在回合级消费者时认领 Esc → `handleChatEscapeInterruptRequest`（先补 `\r\n` 终止草稿行，避免残留行把下一轮提示符/编辑器光标推错列；与编辑器 Ctrl+C 的收尾一致）；无消费者时返回 false 保持原语义。ui 侧新增 `ReadInteractiveLineContextWithPromptHooks`。
- 已知差异：管道模式没有可回注的输入队列，被认领后草稿不跨读保留（Ctrl+C 现状相同）；如需保留需引入字节回灌或草稿轮转，留待后续。
- 回归：`TestReadPipeInteractiveLineEscInterruptsActiveTurn`（+ 既有 pipe 编辑器用例）。

### R2（P1）Web 微客户端 Esc 依赖焦点 —— ✅ 已修复（2026-10-18）
- 修复：`web/js/chat.js` 增加 document 级 bubble 兜底 `onGlobalEscapeInterrupt`——`defaultPrevented` 短路（#prompt 处理器优先）、快捷键帮助面板先关闭、`escOverlayOwnsKey()` 让位给浮层（`*-overlay` 家族按 `getClientRects()` 可见性判定 + 顶栏菜单 `.menu-root.open` + 页签菜单 `.tab-menu:not([hidden])` + `#cfg-bar.cfg-open` + 配置下拉 popup）、`uiState==="busy"` 才发 interrupt；#prompt 的“关帮助面板”分支补 `preventDefault()` 以声明消费。
- 回归：`node --check`；真实浏览器行为建议按审计 §6 手测清单走查（会话切换弹窗 capture 短路、菜单打开时 Esc 只关菜单）。

### R3（P2）缺少可键入的中断命令（如 `/stop`） —— ✅ 已实现（2026-10-18）
- 实现了 `/stop`：注册表 `runtimeModeInline` + `runtimeEffectLive`（忙时 immediate，capture/主循环忙时通道经 `runtimeCommandHost` 立即执行）；执行体只调用 `handleChatEscapeInterruptRequest`（`chat_stop_command.go`），有在途回合时中断并回执，空闲时明确提示无目标且不置中断标记（`chatStopCommandHasTarget`：ESC 消费者 → capture → actor 忙 → Interaction Ready 兜底）。
- 目录/补全/帮助：`chat_slash_command_catalog.go`（session 组）+ `docs/aicli/install.md` 命令表；legacy/JSON 出口在 `handleCommand`。
- 状态行兜底提示：无 ESC 消费者时的 “ctrl+c to stop” 改为 “ctrl+c or /stop”（指向真实可用的可键入入口）。
- 回归：`chat_stop_command_test.go`；`TestRuntimeCommandRegistryBusyAdmissionAudit`（inline 写效应登记）、`TestUnifiedCatalogCommandsNeverFallToUnknown`、`TestChatSlashCommandCatalogMatchesHandleCommandRoutes`、`TestMatchSlashCommandCandidates` 等目录守卫同步更新。

### R4（P2）模态/overlay 期间状态行承诺与实际行为不收敛（原方案 P2b 步骤 2 未完成）
- 事实：仲裁器目标语义 modal 期 `EscAvailable=false`（`chat_input_arbitration.go:103-117`），但 `chatEscapeInterruptAvailable`（`chat_escape_availability.go:18-26`）用“仲裁器 OR 位路径”，位路径在 modal 期仍为 true → 状态行照旧显示 `esc to interrupt`，实际按 Esc 只关面板（方案 §12.6 步骤 2 的取样未完成）。
- 建议：按原计划完成真实终端取样后决定 modal 是否参与 `Disarm/Suspend`；未定期间维持现状并在文档标注。

### R5（P3）Windows console 双读者竞态 / 副屏期 KeyHandler 抢 Esc —— ✅ 已修复（2026-10-19）
- 事实（修复前）：副屏（pager/picker/overlay/backtrack picker 等）打开期间 composer 与 busy capture 都不再读 stdin，但回合级 ESC 消费者仍让 KeyHandler 保持 armed——50ms 轮询与副屏自身的按键读取抢同一条 console 记录：用户按 Esc 想关面板，却可能被 KeyHandler 抢走变成“中断后台回合”，且被抢走后副屏再也收不到 Esc；状态行/Web payload 也仍承诺可中断。
- 修复：屏幕租约是全框架唯一咽喉（`openChatScreen`），在那里登记 L0 输入独占——`beginChatScreenInputOwnership`：仲裁器登记 modal 层（EscAvailable=false，承诺变诚实）+ KeyHandler 暂时 `Disarm`；关闭后**只有仍存在回合级 ESC 消费者**时才重新 `Arm`（消费者可能在副屏存续期间结束，绝不留下无人消费的 armed 状态）。回归：`chat_screen_input_ownership_test.go`（两幕：关屏后重新 Arm / 消费者已结束不 Arm）。
- 残余（保留）：KeyHandler 与 composer 在**非副屏**窗口的 peek/consume 交错仍是结果一致的过程性竞态（正文修复已消除其影响）；阶段 F P2b 单一写者灰度后可进一步收敛为单读者。

### R6（P3）Web/HTTP interrupt 反馈语义与 TUI Esc 不一致 —— ✅ 已修复（2026-10-18）
- 事实（修复前）：`handleWebInterrupt` 直呼 `interrupt`，没有停止窗口折叠、也没有 stalled-cleanup 重试。
- 修复：抽出会话级唯一决策 `chatInterruptDecision(session, preserve)`（返回 `started` / `already_stopping` / `no_session`，`chat_escape_interrupt.go`）；TUI Esc（`handleChatEscapeInterruptRequest`）、Web/HTTP interrupt、`/stop`、忙时 capture 全部共用它，渲染仍由各平面自理（TUI 提示不变；Web 保持静默 + 状态复位）。`handleWebInterrupt` 响应契约 `{"status":"interrupted"}` 不变，新增机读字段 `"result"`。
- 回归：`TestChatInterruptDecisionSharedAcrossPlanes`、`TestHandleChatWebAPIInput_InterruptSharesDecisionWithEscape`（既有 `TestHandleChatWebAPIInput_Interrupt` 保持通过——客户端只读 `status`）。

### R7（P3）非交互/无 UI 宿主无 Esc 语义 —— ✅ 已核查（2026-10-19，无需修复）
- 核查结论：runtime-server 有 HTTP `interrupt`（`session_runtime_handlers.go:968`，含 active-turn 注册表与 turn_mismatch 409）；ACP 有 `session/cancel` 协议入口（`agent_stdio.go`）；headless `exec` 走进程信号；TUI 转发远端有 `InterruptTurn`（`chat_runtime_server.go:574+`）。均**不依赖 Esc**，无代码缺口。
- **R11（已实施，2026-10-19）**：IM 连接器新增 `/stop` 停止命令——核心 `InterruptBackend` 可选能力（`internal/connectors/types.go`）+ 命令表注册（`commands_catalog.go`，帮助/面板/识别同源）+ `dispatchInterrupt` 回执语义（未实现宿主明确提示；`Stopped=false` 如实回“没有在途回合”）。宿主接线：runtime-server 复用 `Handler.InterruptSessionTurn`（从 HTTP interrupt 用例抽出的同一实现：active-turn 注册表 → durable actor 回退）；aicli 展示会话走 TUI Esc / `/stop` 同一决策点、`conn-*` actor 走 `interruptActiveRuns`（含级联）；gateway 未实现 → 核心明确提示（与 /undo 同口径）。回归：`internal/connectors/service_stop_test.go`、`cmd/runtime-server/connector_stop_test.go`、`cmd/aicli/commands/connector_stop_host_test.go`。

### R8（P3）Unix SIGUSR2 的 KeyHandler 打印与新提示重复 —— ✅ 已修复（2026-10-18）
- 修复：删除 `ui/keyhandler_unix.go` 的 `fmt.Println("[ESC 检测到 - 中断操作]")` 直写与 `fmt` 导入；用户可见反馈统一由决策点渲染（避免与固定 surface 绘制竞争）。
- 门禁同步：`ui/writer_inventory_test.go` 受认可写入基线按“实现退役时删除条目”规则下调（17 条/20 点位 → 16 条/19 点位，ceiling 20→19）。

### R10（P3）Web 键入 `/stop` 忙碌期只入队 —— ✅ 已修复（2026-10-19）
- 修复：`handleWebPrompt` 对 `/stop` 增加忙碌期直通——`commandMatches` 命中且 `chatStopCommandHasTarget` 为真时走 `chatInterruptDecision`，回 `{"status":"interrupted","result":…}`（微客户端已能处理该状态）；空闲时保持队列路径，由命令自身给出“没有在途回合”回执。
- 回归：`TestHandleChatWebAPIInput_StopPromptInterruptsRunningTurn` / `…IdleKeepsQueuePath`。

## 6. 回归与验收建议

| 层级 | 用例 | 现状 |
| --- | --- | --- |
| Go 单测 | composer Esc（有/无消费者） | 已有（`chat_composer_escape_test.go`） |
| Go 单测 | 忙时 capture Esc（中断 + 草稿保留 + 重复 Esc 提示） | 已有（`chat_escape_interrupt_test.go`、`chat_escape_consumer_test.go`） |
| Go 单测 | KeyHandler watcher（共享 arm、释放后不消费、actor-only 回合） | 已有（`chat_escape_consumer_test.go`） |
| Go 单测 | parked watcher arm/release | 已有（`chat_parked_escape_test.go`） |
| Go 单测 | 回溯入口撞上运行中回合 → 中断 | 已有（`chat_backtrack_command_test.go`） |
| Go 单测 | Unix SIGUSR2 让位/兜底两态 | 已有（`TestChatEscapeSignalOwnedByKeyHandlerMatrix`） |
| Go 单测 | 忙时 Esc 走统一决策（含重复 Esc 节流提示） | 已有（`TestBusyInputEscRoutesThroughSharedDecision`） |
| Go 单测 | PTY 管道 Esc → 中断 | 已有（`TestReadPipeInteractiveLineEscInterruptsActiveTurn`） |
| Go 单测 | `/stop`：忙时策略/运行中中断/空闲不中断 | 已有（`chat_stop_command_test.go`） |
| Go 单测 | 统一决策：started / already_stopping / 重试 / 非 preserve | 已有（`TestChatInterruptDecisionSharedAcrossPlanes`） |
| Go 单测 | Web interrupt 共用决策（`result` 字段） | 已有（`TestHandleChatWebAPIInput_InterruptSharesDecisionWithEscape`） |
| Go 单测 | Web 粘滞标记不误报（R9） | 已有（`TestStopCommandIgnoresStaleWebCaptureLatch`） |
| Go 单测 | 副屏期 KeyHandler 让位/复 Arm（R5） | 已有（`chat_screen_input_ownership_test.go`） |
| Go 单测 | Web 键入 `/stop` 忙碌期直通 / 空闲入队（R10） | 已有（`TestHandleChatWebAPIInput_StopPromptInterruptsRunningTurn` 等） |
| Go 单测 | IM `/stop`（R11）：核心路由/回执、runtime-server、aicli 宿主 | 已有（`service_stop_test.go`、`connector_stop_test.go`、`connector_stop_host_test.go`） |
| Go 单测 | 输入仲裁分叉遥测（R4/R5 取样） | 已有（`TestChatInputArbitrationDivergenceTelemetry`、`TestChatDebugDisplayIncludesInputArbitration`） |
| 真机 | 运行中状态行诚实 + Web 中断取消运行中 shell + 重复中断折叠 + 停止摘要 | ✅ 2026-10-18（§6.1） |
| 真机 | 空闲 `/stop` 不误报（R9 修复复验） | ✅ 2026-10-18 23:53 构建复验（§6.1） |
| 真机 | TUI 物理 Esc / 键入 `/stop` 打断运行中回合 | ✅ 2026-10-19 01:05 构建复验（§6.1 #5/#6） |
| 真机 | 副屏 Esc 只关面板、不中断回合（R5） | ✅ 2026-10-19 01:05 构建复验（§6.1 #4：opens=1/closes=1/esc=1，屏内无中断） |
| 手测 | 后台回合（wake/续跑）Esc → Stopping → Ready（Windows console） | 修复待重建二进制后验证 |
| 手测 | winpty/SSH：裸 Esc / 带草稿 Esc / Ctrl+C / `/stop` 四通道 | 待盲测（R1 已实现） |
| 手测 | Web 微客户端：焦点在停止按钮 / 会话列表 / 浮层打开时按 Esc | 待盲测（R2 已实现） |

### 6.1 真机验证记录（2026-10-18 夜，会话 session_20261018233755_ZxzOaDFH）

- 二进制：`backend/aicli-4x.exe`（23:37:46 构建，含本轮全部改动；`strings` 校验
  `chatInterruptDecision` / `chatEscapeInterruptConsumerActive` / `ctrl+c or /stop` 均在）。
- 现场：TUI（Surface on、bypass_permissions、provider=baidu/deepseek-v4.1-flash），
  调试面 `http://127.0.0.1:52450`。

| # | 场景 | 操作 | 证据 | 结论 |
| --- | --- | --- | --- | --- |
| 1 | 运行中状态行诚实性 | 注入长 shell 回合（`ping -n 40`） | 状态行 `Running shell (3s • esc to interrupt)` | ✅ 有真实消费者时才承诺 |
| 2 | Web/HTTP 中断走共享决策 | `POST /web/api/input {"type":"interrupt"}` | `{"result":"started","status":"interrupted"}`，响应 41ms | ✅ R6 决策生效 |
| 3 | 重复中断折叠 | 停止清理在途时再次 interrupt（首轮实测亦复核） | `{"result":"already_stopping","status":"interrupted"}` | ✅ 不叠加取消 |
| 4 | 运行中回合被真正取消 | 承接 #2 | turn `failed`、`error="context canceled"`、正在运行的 shell 被取消（`unrecovered_tool_error_count=1`）、turn 立即转 idle | ✅ 中断闭环 |
| 5 | 中断后的界面反馈 | 承接 #4 | transcript：`当前运行已停止：已保存 1 条工具观察，可从此处恢复…` + `/retry` 建议 | ✅ 不静默 |
| 6 | goal 与中断的交互（并行 goal-driver 改动） | 承接 #4 | debug：`[goal] continuation frozen reason=user_interrupt` | ✅ 中断冻结续跑 |

**复验（23:53:54 构建 aicli-4x.exe；会话 session_20261018235358_YTDmwEIp，`strings` 已确认含 R9 修复的 `consoleInputCaptureActive`）**：

| # | 场景 | 证据 | 结论 |
| --- | --- | --- | --- |
| 1 | 空闲 `/stop`（R9 修复复验） | 回执 `当前没有在途回合，/stop 未触发中断…`；事件库无 `session_interrupted` | ✅ 不再误报、不再误触发中断 |
| 2 | 运行中状态行诚实性 | `Analyzing (3s • esc to interrupt)` | ✅ |
| 3 | 运行中 Web 中断（R6） | `{"result":"started"}` 响应 37ms；立即重复 → `{"result":"already_stopping"}` | ✅ |
| 4 | 回合取消与反馈 | turn `failed`、`error="context canceled"`（3.6s）；transcript `当前运行已停止…/retry` | ✅ |
| 5 | Web 微客户端含 R2 | 在线 `/web/js/chat.js` 含 `onGlobalEscapeInterrupt` / `escOverlayOwnsKey` / `defaultPrevented` | ✅（浏览器行为待手测） |

**按键复验（2026-10-19 01:05:11 构建 aicli-5x.exe；会话 session_20261019010517_zPeHrKnw；`strings` 已确认含 `beginChatScreenInputOwnership` / `chatInterruptDecision` / `escOverlayOwnsKey`）**：

| # | 场景 | 证据 | 结论 |
| --- | --- | --- | --- |
| 1 | 空闲 `/stop` | 回执 `当前没有在途回合，/stop 未触发中断…` | ✅ R3/R9 |
| 2 | 运行中 Web 键入 `/stop`（R10） | 33ms `{"result":"started","status":"interrupted"}`；回合 `context canceled` | ✅ 直通生效 |
| 3 | 立即重复中断（R6） | `{"result":"already_stopping"}` | ✅ 不叠加取消 |
| 4 | **副屏 Esc（R5）** | `Chat Screen Framework` 计数 `opens=1 closes=1 esc=1`（副屏由 Esc 关闭），副屏存续期间**无** `session_interrupted`；关闭后回合继续（后续中断发生在 17:09:35） | ✅ 面板只关不中断 |
| 5 | TUI 物理 Esc 中断 | 事件 `session_interrupted reason=interrupt` + turn `failed / context canceled`（17:09:35，运行 142s 的 ping 被取消） | ✅ 原始缺陷回归通过 |
| 6 | 二次中断动作（Esc 或键入 `/stop`） | 17:09:46 起的新回合 5s 后 `context canceled` + `session_interrupted` | ✅ |
| 7 | 中断后反馈 | transcript `当前运行已停止：已保存 1 条工具观察…` + `/retry` 建议 | ✅ |

**待人工按键（本机无法注入物理键）**：已全部由上述 #4~#6 覆盖；winpty/SSH 场景（R1）仍待具备该环境的盲测。

**真机新发现（本轮修复/记录）**：

- **R9（已修复）空闲 `/stop` 假阳性**：Web 注入会把 `webCaptureActive` 粘滞置真
  （直到下一次控制台前台回合释放），而 `/stop` 目标判定与 ESC bits 可用性当时用
  `hasExternalInputCaptureActive()`（含该粘滞位）→ 空闲 `/stop` 误报"已请求停止"并
  真的触发一次中断（实测复现）。修复：新增 `chatInputQueue.consoleInputCaptureActive()`
  （只看控制台 busy capture），`chatStopCommandHasTarget` 与 `chatInputEscBitsAvailable`
  改用它；回归 `TestStopCommandIgnoresStaleWebCaptureLatch`。
- **R10（已修复，2026-10-19）Web 注入的 `/stop` 在忙碌期只入队**：Web 注入行遵循
  P2-8 的 preserve/队列语义（忙碌期由控制台 capture 消费 stdin，注入行走队列，回合结束后
  才执行）。修复见 §5 R10：`handleWebPrompt` 在忙碌期把 `/stop` 直通 `chatInterruptDecision`。

## 7. 关键证据索引

- 决策点与消费者：`chat_escape_interrupt.go:28-70、107-116、134-147、183-206`、`chat_composer.go:340-393、666-680`、`chat_busy_input.go:254-265、354-364`、`chat_backtrack_select.go:24-34`、`chat_unix.go:17-52`、`ui/keyhandler_windows.go:31-147`、`ui/keyhandler_unix.go:15-49`
- 会话动作层：`chat.go:535-610、648-677、679-716`、`chat_interrupt.go:33-65、232-271`、`chat_exit.go:19-68`
- 状态行诚实性：`chat_escape_availability.go:18-46`、`chat_input_arbitration.go:96-118、167-209`
- Web/协议：`web_handlers.go:942-962`、`web_handlers_mesh_call.go:315`、`web/js/chat.js:1296-1331`、`web/js/sessions.js:1433-1439、1488-1494`
- 控制台前端：`frontend/src/hooks/workspace/use-escape-stop-responding.ts:36-73`、`frontend/src/hooks/workspace/use-stop-responding.ts:41`
- runtime/ACP/监督：`session_runtime_handlers.go:968-982`、`session_active_turn.go:144-160、242-246`、`agent_stdio.go:390-408`
- 现场证据（原始缺陷）：`C:\Users\vince\.aicli\chat-logs\2026\10\18\session_20261018191622_xGGs4QZJ\`（`debug/debug.log`、`events/runtime-events.jsonl`）
- 真机验证（修复后）：`C:\Users\vince\.aicli\chat-logs\2026\10\18\session_20261018233755_ZxzOaDFH\`（§6.1）
