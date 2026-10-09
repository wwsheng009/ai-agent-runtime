# aicli 渲染 P0（写端归一）实施台账

> 分支：`feat/render-p0-writer-unification`（基于 `76e6f8cc`）
> 依据：`docs/plan/aicli-unified-render-architecture-audit-20261005.md` §2/§6（P0）。
> 硬规则：**迁移一处 → 从 `backend/cmd/aicli/ui/writer_inventory_test.go` 基线删除对应条目；
> 不得为任何新交互功能新增条目。** 门禁测试：`go test ./cmd/aicli/ui/ -run TestUIInteractiveDirectWriterInventory`。
> 关联：残余写端差距（G2/G3/G9/G11）收敛见 `docs/plan/aicli-render-gap-closure-plan-20261006.md` 批次 A；
> 本台账条目与该方案 A1 逐项/登记表互相对应。
> 后续：legacy fallback 链退役分析与实施方案见 `docs/plan/aicli-legacy-fallback-retirement-plan-20261008.md`
> （结论：整体退役不可行——compat/plain 为产品承诺；可删死码与可拆物理绘制族按该方案 L1–L4 分批执行；
> D0 决议 2026-10-08：无外部消费者 → L1 直删；secret 收口选 (a)）。
> 后续登记（未排期）：L5 候选与 DEC 2026 跟踪项的立项评估见
> `docs/plan/aicli-render-l5-candidates-20261009.md`（触发式立项）。

## 1. 已完成

- [x] **ui 直写守卫 + 64 项债务基线**（commit `09190ee8`）。
  - 扫描语义：`ui/*.go` 生产文件的 `os.Stdout/os.Stderr` 触碰点（纯 `Fd()` 探测除外）、`fmt.Print*`、`TerminalOutput()`。
  - 基线分组：P0 实活目标（inputbox_editor ×3 组、status 兜底、osc_live）；lease/全屏已改道的 raw 兜底；
    legacy 死链打印机；被栅栏的 FixedBottomSurface；`TERM_SESSION_TRACE` 调试追踪。
- [x] **legacy 打印机孤岛删除 Batch 1**（2026-10-08）。
  - 删除 `ui/progress.go`（Progress/Spinner + PrintProgress/PrintSpinner）、`ui/shell_feedback.go`、
    `ui/toolcall.go` 及各自测试；`ui/output.go` 裁剪为仅存活符号 `TruncateVisible`
    （commands/chat_debug_document.go、ui/active_cell_projection.go 在用）。
  - 门禁基线 **65→50 条目（net −15）**：progress 6、shell_feedback 3、toolcall 4、output 2。
    判定依据：符号级 repo-wide 交叉验证（非测试引用 = 0；deadcode 因机器内存不足未能全量运行，
    以编译器 + 全量测试兜底）。
  - 验证：`go build ./cmd/aicli/commands` 绿；`go test ./cmd/aicli/ui` 13s 绿；
    `go test ./cmd/aicli/commands` 210s 绿；`TestUIInteractiveDirectWriterInventory` 绿。
  - `tool_output_safety_test.go` 保留活覆盖（SanitizeToolOutput / PreviewToolOutputANSI /
    StatusLine golden）；`ui/README.md` 章节目录同步。
- [x] **legacy 打印机孤岛删除 Batch 2**（2026-10-08）。
  - 删除零调用方函数：`welcome.go` PrintHelp/PrintGoodbye（帮助改为命令侧 structured 输出）；
    `theme.go` `(Theme).PrintBorder/PrintSeparator`；`layout.go`
    PrintMessage/PrintToChat/ClearChatArea/writeRightAligned；`input.go` PromptAssistant。
  - 附带清理（无门禁条目，纯死代码）：`message.go` DisplayToolMessage/DisplayErrorMessage；
    `separator.go` PrintEmptyLines/PrintSeparator/PrintThickSeparator/PrintThinSeparator
    （`PrintTitledSeparator`/`PrintSection`/`PrintEmptyLine` 保留：命令侧 legacy 兜底在用）。
  - 门禁基线 **50→41 条目（net −9）**；判定同 Batch 1（零调用方 + 编译器/测试兜底）。
  - 验证：`go test ./cmd/aicli/ui` 15.3s 绿；守卫门禁绿；`gofmt` 干净。
  - commands 全量复跑说明：本机资源紧张期出现 3 个互不相同的 team/streaming 时序用例偶发
    失败（`AutoStartTeamClosesNonLeadTeammate…`、`ReplayedTerminalEventClosesNonLeadTeammate…`、
    `StreamingAssistantFinalTailTransfersExactlyOnce…`）；同一用例隔离复跑 ×5/×20 全绿，
    期间编译器一度报 `Insufficient system resources`，按**环境偶发**登记。
    Batch 2 删除面全部为 ui 包零引用死代码（commands 编译器引用面为零，无法影响其运行期行为）。
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

- [x] **L1 legacy 死码退役（退役方案 L1-a/b/c，3 提交）**（2026-10-08）。
  - L1-a `48a9b3c5`：编辑器死链——`readPrompt` 链 + 无 hooks 包装 + `ReadWithHistory`；
    `writeEditorControlSequence` 去 nil 分支（`*LineEditorHooks` → `LineEditorHooks`）。
  - L1-b `48ca07c8`：`Terminal.PrintAt/RawMode/DisableEcho/EnsureExitOnSigInt`、`Status.PrintSuccessTo/PrintInfoTo`、
    `KeyHandler.WaitForESC/ManualInterrupt`；`Notify`/`PrintErrorTo`/`PrintWarningTo` 按逐符号复核保留。
  - L1-c `0b0fe123`：screen_lease raw DEC 1049 分支退役（租约统一走 transport、缺失 fail-closed）；
    `Disable` 租约退出 transport-only；测试迁移到 transport 断言；commands screen-framework helper
    注入 transport（修复 12 个 screen-framework 用例对 raw 租约路径的依赖）。
  - 门禁基线 **40→34 条目（net −6，口径校正详见方案 §5 L3-1 记录）**：`readPrompt`、`PrintAt`、`screen_lease`×3、`Disable`。
  - 验证：ui 全量（含 -race 子集）、commands 全量（174s）、`go build ./...`、门禁绿；
    环境偶发与暂缓项（InputBox legacy 方法簇）见方案 §5 执行记录。

- [x] **L2 unified 残留直写收口**（2026-10-08，`657bf253`；退役方案 §4.5 四项全部处置）。
  - secret 读经 `LineEditorHooks.OnTerminalText` 认领（标签经提示行预渲染 + 尾换行；
    未认领保留 raw 兜底，非 unified 字节不变）；编辑器自有字节 raw 兜底收敛 `writeEditorRaw`。
  - resume 通知 / runtime 配置加载告警改经 `NotifyChatDiagnostic`（stderr 兜底保留）；
    退出恢复提示与无 ANSI 降级告警登记 sanctioned console writer。
  - 门禁：**ui 债务 34→33（net −1，raw 引用 3→1）**；`TestUnifiedSessionSinglePhysicalWriterFence`
    扩展 secret 驱动；ui/commands 全量绿（commands 192s）；真机 e2e 待人工复跑（见方案 §5）。

- [x] **L3-1 FixedBottomSurface 拆壳首刀**（2026-10-08，`d57cf71b`）。
  - `Enable` 首帧块退役；DEC2026 framing 全链删除（开关/查询/包裹分支 + 裸 os.Stdout 写），
    写锁本体保留；`Disable` framing reset 随之删除；freeze 测试随符号删除，sync 用例收敛为「永不包裹」。
  - 门禁：**条目 33→32**；ui 全量（12.8s）+ commands 相关子集绿；基线口径校正（L1 40→34、L2 34→33）。
  - 文档口径：`tui-render-architecture.md` §2.2 已修正（删除「DEC 2026 同步框包裹」表述——实现为
    单帧一次 Write 原子提交）；「session 侧 2026 包裹」登记为跟踪项（方案 §5，默认不上路）。

- [x] **L3-2 FixedBottomSurface 物理绘制退役（state-only 收敛）**（2026-10-08，9 提交 `e3eff2fc`..`55c08567`）。
  - 删除物理绘制实现：`writeOutput` 物理分支、`appendOwnedDirectPaintLocked`、`insertHistoryLines*`、
    `flushHoldingLock`/`flushHandoffHoldingLock`、`renderOwnedViewportLocked`、`stageOwnedFrameLocked`、
    `reconcileOwnedViewportLocked`；三 `render*Locked` 保留 guard-only 空壳（~23 调用点不动）；
    fence API 及调用点保留，生产恒 fenced。
  - 语义保留：eager state-only handoff（前沿推进 + 双保留窗口软裁剪，无字节）；无效几何守卫；
    `/debug` paint trace 无事件时回退 row-ownership 表。
  - 测试：13 个 surface 测试文件迁移到 composed-frame/state oracle；A 组 3 文件删除；paint-trace
    白重绘计数族退役（引擎契约由 renderengine 测试保留）。
  - 门禁：**条目 32→27（net −5）**；`clearActiveBand`（:88）保留待 L3-3。
  - 验证：gofmt/vet/build 绿；ui 全量（11.8s/12.3s 双跑）绿；commands 相关子集绿；inventory 门禁绿。

- [x] **L3-3 FixedBottomSurface 残余退役（C 类收口）**（2026-10-08，`54037149`..`e4a19aad` + 测试迁移）。
  - 删除 `Disable` legacy teardown paint、`clearActiveBand` paint 分支、`repaintActiveBandLocked` 物理体
    （guard 壳保留）与 `surface.Apply`；死代码 `appendClearRowsSequence` 删除。
  - 测试：61 个 commands 测试迁移到 frame/历史窗口/AppState/unified presenter 四类观察面
    （含全量首跑暴露的 56 个 L3-2 legacy 直写存量失败清零）。
  - 门禁：**条目 27→26**；FixedBottomSurface 物理写族清零。
  - 验证：commands 全量 4668/0（双跑；1 个已登记环境 flake 隔离全绿）；ui 全量绿；build/vet/gofmt 绿。

- [x] **L4 门禁语义重构与降级正规化**（2026-10-08，单提交：门禁重构 + 文档正规化）。
  - writer inventory 拆两组：sanctioned console writers（受认可白名单类，零新增）/
    migration debt（必须递减，ceiling 只降不升）；并集精确匹配仍作回归栅栏。
  - 机械口径复测：受认可 21 条/24 点位 + 债务 4 条/4 点位 = 合计 **25 条/28 点位**
    （历史人工计数存在 +1 漂移，自 L4 起以机械口径为准）。
  - 降级正规化：compat/plain（console mode）链登记为受认可 writer；`consoleMode*` 命名文档层
    先行（`windows7-compat-internals.md` §6.2 互链）；验收矩阵追加 compat 场景（真机）。
  - 验证：writer inventory 门禁绿；ui 全量 + commands 单写端栅栏绿；gofmt/build/vet 绿。

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
3. [x] transient/modal/agent-panel composer 编辑器出口：
   - [x] modal + agent-panel（认领 + popup 折入输入）；
   - [x] secret（surface 预览下无直写，仅 legacy fallback）；
   - [x] transient line：显示属主 = 底部 prompt 行（方案 A）。
     `chatTransientLineComposer` 新增 `mergedPromptSupported`/`readLineMerged`/`mergedHooks`：
     固定 surface 且直读 stdin 时停靠草稿 → `ShowAnswerPrompt` 占用 prompt 行 →
     `OnChange`/`OnTerminalWrite` 折入（`SetPromptInputSnapshot`/`WritePromptEditorText`）→
     读毕 `DiscardPrompt` 归还草稿；无 surface/排队读保持原直读通道。
     测试：`TestChatTransientLineComposerReadsThroughBottomPromptRow`、
     `TestChatTransientLineMergedHooksFoldIntoBottomPrompt`、
     `TestChatTransientLineComposerFallsBackWithoutSurface`。
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
   （门禁运行说明见 §4 验收与 README「写端门禁」小节。）

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

- 每次迁移：`go test ./cmd/aicli/ui/ -run TestUIInteractiveDirectWriterInventory`（基线并集精确匹配：
  受认可类零新增、债务类只减不增；ceiling 常量只降不升）+ 目标包回归 + 真机 e2e
  （粘贴、焦点切换、标题、铃、长文本粘贴）。
- **L4 起追加 compat 场景（每批真机执行）**：`--compat-mode` 基本可用性（无 TUI 启动 →
  一轮对话回显/输出 → `/exit` 退出码 0）+ 无 ANSI 降级提示
  （`Warning: terminal does not support ANSI scroll-region rendering; using plain interactive mode`，
  stderr，无渲染字节污染）。
- 单写端运行时门禁（新增）：
  `go test ./cmd/aicli/commands/ -run 'TestUnifiedSessionSinglePhysicalWriterFence|TestChatSelectionOutputClaimsToDiagnosticSinkWhenSessionActive|TestChatControlSequenceWriter'`
  ——注入计数 writer + 进程 stdout/stderr 零字节断言，覆盖标题/铃/模式序列/动态诊断/直写/命令输出。
- composer 出口门禁：`go test ./cmd/aicli/commands/ -run 'TestChatTransientLineComposer|TestChatMergedAnswerPrompt|TestChatModalComposer|TestChatAgentPanelComposer'`。
- L4 门禁语义重构（2026-10-08）：基线拆 sanctioned console writers / migration debt 两组
  （机械口径 25 条/28 点位 = 受认可 21/24 + 债务 4/4）；`uiWriterMigrationDebtCeiling` 只降不升；
  分类移动必须同步更新 ceiling 与计划/台账。
- 完成态：ui 生产文件直写基线只剩受认可白名单类（启动期探针/句柄初始化、TRACE/诊断、
  console/plain 降级承重链、平台差异、启动期无租约回退；FixedBottomSurface 物理写族已清零）
  与有限债务（4 点位，随 L1-d / `ClearIfSupported` 改造递减）；交互期物理 writer 计数 = 1；
  CI 中门禁测试常开。
