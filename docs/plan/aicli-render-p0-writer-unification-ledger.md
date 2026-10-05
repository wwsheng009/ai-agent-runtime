# aicli 渲染 P0（写端归一）实施台账

> 分支：`feat/render-p0-writer-unification`（基于 `76e6f8cc`）
> 依据：`docs/plan/aicli-unified-render-architecture-audit-20261005.md` §2/§6（P0）。
> 硬规则：**迁移一处 → 从 `backend/cmd/aicli/ui/writer_inventory_test.go` 基线删除对应条目；
> 不得为任何新交互功能新增条目。** 门禁测试：`go test ./cmd/aicli/ui/ -run TestUIInteractiveDirectWriterInventory`。

## 1. 已完成

- [x] **ui 直写守卫 + 64 项债务基线**（commit `09190ee8`）。
  - 扫描语义：`ui/*.go` 生产文件的 `os.Stdout/os.Stderr` 触碰点（纯 `Fd()` 探测除外）、`fmt.Print*`、`TerminalOutput()`。
  - 基线分组：P0 实活目标（inputbox_editor ×3 组、status 兜底、osc_live）；lease/全屏已改道的 raw 兜底；
    legacy 死链打印机；被栅栏的 FixedBottomSurface；`TERM_SESSION_TRACE` 调试追踪。
- [x] **控制序列旁路 API + inputbox_editor 模式序列迁移**（commit `c159a118`）。
  - `TerminalSession.WritePromptEditorControl(sequence)`：以 `TransactionPromptEditor` kind 提交，
    与帧/历史共用 `transactionMu`（控制字节不可能插入帧字节中间）。
  - `LineEditorHooks.OnTerminalControl func(sequence string) bool`：宿主认领接口；未认领保留 raw 回退。
  - `inputbox_editor` 四处序列（`readPromptWithHooksContext` 启用/禁用、`readPrompt` 启用/禁用）改经钩子；
    写端基线 `readPromptWithHooksContext` 3→1、`readPrompt` 3→1、新增 `writeEditorControlSequence` 1（net -3 refs）。
  - 生产接线：主 / busy / merged / selection composer 全部经 `chatInteractionCoordinator.WritePromptEditorControl`
    → session（无 unified 会话时返回 false，回退 raw）。
  - 测试：`TestWriteEditorControlSequence{ClaimsViaHook,FallsBackToRawWriter}`、
    `TestTerminalSessionWritePromptEditorControl`；ui 全量绿。
- [x] **transient/modal/agent-panel composer 编辑器出口（第一段）**。
  - modal（priority popup）与 agent-panel（modal popup）的输入行显示权都在 surface popup：
    编辑器直写=双画。两者改为认领（`OnTerminalWrite`，仅 popup 有效时）+
    控制序列经 session；agent-panel 新增 `UpdateInput` 把输入折入 popup composer line
    （否则认领后打字不可见）。
  - transient line 无 surface 显示属主（`ReadTransientLineWithHooks` 无 prompt/popup）：
    本段仅接控制序列，text 直写保留 raw，待设计显示属主后再认领。
  - secret：surface 预览路径已抑制 prompt 直写，平台密码读取无回显，无编辑器直写；
    仅保留 legacy fallback。
- [x] **标题/铃控制序列经 session**。
  - 新增 `TransactionTerminalTitle` kind；`TerminalSession.WriteTerminalControl(kind, sequence)`
    成为通用控制通道（`WritePromptEditorControl` / `WriteTerminalTitle` / `WriteTerminalBell`
    委托），全部走 transactionMu 与帧/历史串行化。
  - commands 侧新增 `chatControlSequenceWriter` 适配器：unified 会话认领时经
    coordinator → session 提交；否则回退 raw `os.Stdout`（非 unified 字节不变）。
    `initializeChatTitleNotifier` / `initializeChatSoundNotifier` 装配点接入。
  - 测试：`TestTerminalSessionWriteTerminalTitleAndBell`（gateway kind 断言）、
    `TestChatControlSequenceWriter{FallsBackToRawWriter,RoutesThroughUnifiedSession}`。

## 2. 关键侦察结论（决定迁移顺序）

1. **bracketed-paste / focus-change 序列是承重写，不能 claim 后丢弃。**
   `inputbox_editor.go:264/267`（生产活路径）与 `:208/214`（无 hooks 包装器路径）直写
   `\x1b[?2004h/l`、`\x1b[?1004h/l`；unified 路径下没有其他组件启用这两个模式
   （`terminal.go` 的 `EnableBracketedPaste/EnableFocusChange` 只服务 legacy `Terminal`）。
   因此必须先提供**控制序列旁路 API**：session/presenter 内部把控制序列与帧写串行化
   （例如经 output port 的 control 通道或在同一 `terminalWriteMu` 临界区内提交），
   再由 `LineEditorHooks` 暴露 `ControlWriter`（或控制序列回调）。
2. `readPromptWithHooksContext`（`:264/267`）是生产活路径
   （`chat_input_queue.go:1205→1220` → `chat_composer.go:96`）；
   无 hooks 的 `readPrompt`（`:208/214`）只被本文件 3 个公共包装器（`:131/:139/:167`）调用，
   commands 侧未见调用——迁移时确认是否死链，一并改道或删除。
3. `writeEditorText`（`:439-451`）：主/busy/merged composer 已被 `OnTerminalWrite` 吞掉（无字节）；
   transient/modal/agent-panel composer 传空 hooks → raw 直写。需为这些 composer 补 hooks/sink。
4. 标题/铃：`terminal_title.go:52/74`、`terminal_bell.go:27` 拿的是构造时注入的 writer，
   `chat_setup.go:702/703` 传入 `os.Stdout`。这不是 ui 包内直写（不在本守卫基线），
   但属审计 §2.2 的 P0 项——需要 session/port 级 control sink 后改装配。
5. `terminal_session.go` 的 3 处 `fmt.Print` 由 `TERM_SESSION_TRACE` 门控，属诊断通道；
   迁移目标是把诊断输出与交互输出彻底分离（日志文件或显式诊断 sink），不阻塞主流程。

## 3. 下一步（按序执行）

1. [x] 控制序列旁路 API（`c159a118`）。
2. [x] `inputbox_editor` 模式序列迁移（`c159a118`）；余下 1 ref/函数是编辑器读循环的 stdin/stdout 绑定。
3. [~] transient/modal/agent-panel composer 编辑器出口：
   - [x] modal + agent-panel（认领 + popup 折入输入）；
   - [x] secret（surface 预览下无直写，仅 legacy fallback）；
   - [ ] transient line：无显示属主，需先设计显示（底部 prompt 行或 popup）再认领 text 直写。
4. [x] 标题/铃装配到 control sink（`chatControlSequenceWriter`）。
5. [x] `status.go` 兜底路径与 stderr 收编（核实完成 + 收口防线）：
   - `ui.Print*`（status.go 快捷函数）在 commands 的交互期调用点大多已有 popup/fail-closed
     守卫：export 在 `usePopup` 时把 warning 进 popup（`chat_export_command.go:438`）；
     `chat.go` / `chat_restored_pending.go` 仅在 `!unifiedInteractiveOutputMustFailClosed`
     时走 raw。这些是 legacy-only 兜底：保持守卫、不迁移字节。
   - 逐点核实结论（unified 会话期可达性，全链证据）：
     - `chat_model_switch.go:766/781`：不可达——分派器 `selectRuntimeReasoningEffort:645` 的
       popup 守卫（646）在 unified 恒真 → `...Popup`；766/781 在 `...Legacy`（692-783）。
     - `chat_model_command.go:595`：不可达——分派器 472-480（守卫 476）恒走 popup；
       unified 下 `/model`、`/provider` 在 handler 入口即分流到 structured CommandResult。
     - `chat_resume_command.go:738/751`：不可达——两处都在 `if usePopup` 的 else 分支。
     - `chat.go` / `chat_reasoning.go`：仅启动期可达（presenter attach 之前，
       `prepareChatRuntimeState` 早于 `bootstrapChatSessionShell`），raw stderr 无会话期冲突。
     - `chat_session.go:2029-2096`：死代码（`maybeSelectStartupSession` 无生产调用者）。
     - `chat_selection_output.go:101`：unified 会话期调用者全被拦截（skills/theme 入口分流；
       其余为启动期/死代码）。
   - 收口防线：`chat_selection_output.go` 的 `printChatSelectionLine` /
     `printChatSelectionPrompt` / `printChatSelectionWarning` 增加
     `chatSelectionDiagnosticClaim`——登记中的交互会话存在时投递动态栏、绝不写裸
     stdout/stderr；无会话（启动期）保持原字节。测试
     `TestChatSelectionOutputClaimsToDiagnosticSinkWhenSessionActive`。
     范围刻意不含 `writeChatParts` 通用行写：`printChatSessionInfoRow` 等会话信息行
     也走它，劫持会改变调试/恢复输出的归属与绘制时序（回归实测：`/debug on` 的
     信息行被劫持 → 动态栏重绘 → paint trace 记录事件，破坏
     `TestDebugDisplayNoRenderPaintTraceWithoutEvents`）。
   - stderr：mesh 等后台告警已走 `NotifyChatDiagnostic` → 动态栏（`chat_diagnostic.go`
     契约）；其余为启动期 warning（presenter attach 前）保持 stderr。交互期新告警必须走
     `NotifyChatDiagnostic`，不得直写 stderr。
6. [x] 单写端断言测试：`TestUnifiedSessionSinglePhysicalWriterFence` 注入计数 writer，
   在统一会话存活期驱动标题/铃/编辑器模式序列/动态诊断/直写输出/命令输出，
   断言全部落在同一物理 writer、进程 stdout/stderr 零字节。
   （门禁运行说明见 §4 验收与 README 待补。）

## 3.1 已知基线问题（非本分支引入）

- `TestSuccessfulRequestBoundaryPreservesFortyLineFinalInNativeHistory`（commands）：
  `BOUNDARY-FINAL-32` 与 33 不相邻（active 归档与 resident 尾部之间留白，原生历史不连续）。
  - A/B 定位：`b19284db` 上 `-count=3` **全过**；`a75d1c89`（active 归档保持贴底锚定）之后**稳定失败**。
    即该回归由上一轮修复引入（当时只回归了 `cmd/aicli/ui`，漏跑 commands 包）。
  - 冲突契约：
    - `TestTerminalSessionActiveArchiveKeepsBottomAnchorForLaterMessages`（a75d1c89 新增，ui）要求
      active 归档后 finalized 尾部**保持贴底**，后续实时消息贴底追加；
    - 本测试要求 active 归档（流式 40 行前缀）与 resident 尾部**连续**（resident 必须从 row 1 接续
      scrollback），即归档后 top-align。
  - 两者对同一"active 归档"给出相反锚位期望：本质是审计 §6 P2 指出的
    "active 归档/replay/settle 整族特例"无法用局部补丁同时满足。
  - **已修复（`0d5ecda6`）**：判据 = `resident 模型为空 && !topAligned &&
    historyStreamTailRows 非空`（active 归档只写 stream tail、不拥有 resident 行，即已有行跨入
    scrollback）→ finalized 插入强制顶锚从 row 1 续接归档流；resident 非空时保持贴底
    （`a75d1c89` 的现场语义保留）。两个插入调用点同时生效。
  - 新增单元测试 `TestTerminalSessionInsertionContinuesArchivedScrollback` 钉住该契约；
    `cmd/aicli/ui` 与 `cmd/aicli/commands` 全量回归均绿。

## 4. 验收

- 每次迁移：`go test ./cmd/aicli/ui/ -run TestUIInteractiveDirectWriterInventory`（计数必须按预期下降）+
  目标包回归 + 真机 e2e（粘贴、焦点切换、标题、铃、长文本粘贴）。
- 单写端运行时门禁（新增）：
  `go test ./cmd/aicli/commands/ -run 'TestUnifiedSessionSinglePhysicalWriterFence|TestChatSelectionOutputClaimsToDiagnosticSinkWhenSessionActive|TestChatControlSequenceWriter'`
  ——注入计数 writer + 进程 stdout/stderr 零字节断言，覆盖标题/铃/模式序列/动态诊断/直写/命令输出。
- 完成态：ui 生产文件直写基线只剩白名单类（被栅栏 surface / TRACE / 启动期 probe），
  交互期物理 writer 计数 = 1；CI 中门禁测试常开。
