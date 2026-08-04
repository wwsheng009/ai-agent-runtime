# aicli TUI 统一 AppState/Scene、单屏所有者与事务式渲染长期重构设计

状态：**approved target architecture / implementing（唯一规范性终局；transcript Scene 数据面与 Phase 1 UI actor/action adapter 已部分落地，统一 AppState、effect queue 与 Presenter 所有权切换尚未完成）**

更新时间：**2026-08-04**

适用范围：`backend/cmd/aicli/commands`、`backend/cmd/aicli/ui` 及所有在 chat interactive 生命周期内产生可见输出的 runtime/tool/diagnostic 组件。

关联文档（按职责分层）：

- 当前 owned viewport 实施基线：`docs/plan/aicli-tui-p5-owned-viewport-design.md`；
- owned rendering 迁移子计划：`docs/plan/aicli-tui-owned-render-simplification-plan.md`；
- owned rendering 实施契约：`docs/plan/aicli-tui-owned-render-simplification-implementation-guide.md`；
- transcript overlay、RendererMode 与 history handoff 的实施子计划：`docs/plan/aicli-tui-transcript-overlay-renderer-mode-plan.md`；
- owned rendering 评审与 disposition：`docs/analysis/aicli-tui-owned-render-simplification-plan-review.md`；
- 渲染面/数据面迁移母计划：`docs/plan/aicli-tui-render-data-plane-codex-migration-plan.md`；
- ActiveBand/scroll compensation 专项历史：`docs/plan/aicli-activeband-scrollback-compensation-blank-lines-fix-plan.md`；
- 内容、样式、Markdown、Diff 与结构化渲染 IR：`docs/plan/aicli-ui-ux-rendering-codex-reference-plan.md`；
- 早期 UI 组件化与交互阶段规划：`docs/plan/aicli-ui-refactor-codex-inspired-plan.md`；
- 2026-07-29 内容渲染实施审查：`docs/plan/aicli-ui-rendering-implementation-review.md`；
- Phase 0 渲染路径和组件清点：`docs/plan/aicli-ui-rendering-phase0-inventory.md`；
- 上游事件统一编码器（事件 → 有序带身份模型，本计划 §6 事件/事务/排序规则的输入源）：`docs/plan/aicli-event-stream-rendering-order-unified-encoder-plan.md` 及配套 [render-model-spec](./aicli-event-stream-rendering-order-render-model-spec.md)、[event-encoder-api-design](./aicli-event-stream-rendering-order-event-encoder-api-design.md)、[migration-roadmap](./aicli-event-stream-rendering-order-migration-roadmap.md)。

> 本文不是 `/debug display` 的局部修复说明，也不否定已有 P1–P5 工作。P5 文档只记录 owned viewport 的历史实施事实和已知缺陷，不再定义终局；owned-render-simplification 文档只在与本文一致的范围内作为迁移子计划。本文是跨模块唯一规范源：物理屏幕所有权、AppState/Scene、事件与 effect、frame 事务、history/cell/gap、fullscreen、scrollback handoff 以及旧路径删除均以本文为准。任何子计划若引入全局 `Frame mode -> Scrollback mode`、无类型 `committedBoundary int`、依赖 native scrollback 拉回，或把 `ScreenModel` 提升为业务真相源，均视为与本文冲突，不得实施。

---

## 实施状态（2026-08-03）

本文的终局 `AppState -> Layout/Compose -> TuiPresenter` 尚未整体落地；以下状态仅记录已验证的实施切片，不能将 transcript-only Scene 或过渡 adapter 误读为终局 single-writer 已完成。

> **状态口径**：P4–P9 是目标阶段区间，不是一个可整体标记为“已落地”的功能。当前已完成 transcript Scene、ChangeSet 映射、文本投影、shadow/parity 探针、**Phase 1 UI actor/action adapter 的首批接线**，以及 **Phase 2 AppState snapshot 的首批投影**：普通 runtime event、input snapshot、explicit resize、lease barrier、surface facade 和 FramePump 的 `Timer`/`DrawRequested` 意图均经过 `UIController`；普通 runtime event 的 Scene snapshot 及其 mutable semantic cell 可经 causal action 进入 AppState，live user submit、structured command result、local error 也在释放 coordinator mutex 后投递同一完整 snapshot，事件日志 replay 成功重建 Scene 后亦投递一次 snapshot；`LayoutAppState` 已可从该 snapshot 纯派生 transcript boundary rows 和 bottom allocation，不读取 terminal/surface mutex 或推进 effect。popup owner/priority、suspended `PopupStack` 和 tokenized `PopupHandle` 的 begin/update/clear 已走 durable facade action，并在 `BottomPaneState` 以相同纯 transition 保留恢复语义，handle 在 begin action 入队前分配，后续同 token 操作保持 FIFO。本轮进一步将 prompt reset/rows/notice/editor、incremental input tracking、单独 persistent/dynamic status 与 composer preview 纳入同一 action/reducer state，并由 Apply/sync parity 与 snapshot isolation 回归约束；`UpdatePopupAction` 也已进入 coordinator adapter。reducer 触发 facade 时使用 causal follow-up queue，不因 bounded external mailbox 满而 self-wait；reducer panic 会丢弃本 action 新增的 causal child，避免半 action 提交。生产可见状态仍由 coordinator、`FixedBottomSurface`、ActiveStream 与 Scene 多处维护；审批/问答仍为同步 legacy interaction exception，`EffectResult` 尚无 TerminalSession/effect queue source。ActiveBand 仍是 legacy projection input，同步 cursor-move helper、完整 focus policy 和全部 producer 尚未收敛到 AppState；physical Compose、Presenter、history Ack 也尚未完成。因而当前总体状态仍是 **partial data-plane implementation**。

> **Phase 2 本轮增量（2026-08-04）**：已建立纯 `BottomPaneGeometryPolicy`，以 `BottomPaneState + GeometryState` 重算 ActiveBand budget/gap、prompt/composer margin、popup budget 和长草稿 viewport；prompt snapshot 记录逻辑 cursor、absolute visual row、total rows 与 viewport start，`LayoutAppState` 可在 resize 后只从语义 source 重新测量可见 text rows。`LayoutBottomPaneRows` 已形成 bottom reserve plain text/owner plan，并以单行/多行 prompt、popup/composer、短终端压力与 geometry cycle 和 legacy snapshot 做初步 parity；新增 `LayoutAppScreen` screen-row shadow，将 retained transcript identity/gap 与 bottom overlay 放进同一 plain viewport，并排除 mutable cell 防止 active/band 重复；legacy owner map 覆盖真实多行 prompt 文本，但不再把 popup 的空输入 gap 标为 Prompt。legacy surface 完成 probe 后以 `Resize{Applied:true}` causal barrier 回投测得尺寸，只有真实变化才推进 generation，回投不触发二次 probe/reflow。该数据桥不等于 physical Compose、全屏 legacy text/owner parity、TerminalSession 或唯一 terminal writer 已完成。

> **ActiveBand source-projection 增量（2026-08-04）**：`ActiveCellState` 现携带 Scene kind，纯 `ProjectActiveCellBand` 从 `Acked.End` 后的语义 source（仅合法 UTF-8 prefix range）按逻辑行、宽度与 active-band 行预算生成 role-tagged tail。`LayoutAppState` 在没有 legacy facade band 时采用该候选；legacy facade 输入存在时保持它为唯一视觉来源，禁止两者叠加。该规则补足未来 AppState Compose 的 source-backed fallback，并通过 Ack suffix、尾部裁剪、UTF-8 边界与 legacy precedence 回归约束；它不是 production presenter 切换，亦未把 coordinator/ActiveStream 的 streaming range cursor 误称为已删除。

> **Phase 2 shadow parity 补充（2026-08-04）**：`LayoutAppScreen` 的 semantic boundary 空行在物理 owner 上为 Transcript，并以 `TranscriptGap + CellID` 保留语义身份，避免混同 bottom reserve 的无主 Gap。transcript physical-row 展开复用纯内存 `vt.Screen`，覆盖 deferred wrap、宽字符、leading combining mark、tab stop 与 SGR；以等价 legacy logical history source 的 full-screen owner/text matrix 覆盖 popup 空输入 gap、多行 prompt/popup、超长 tail、geometry cycle 和一行 terminal。`BottomPaneRowPlan` 显式记录 prompt input start/rows，cursor intent 不会将 notice/editor context 误当输入首行；一行 terminal 的 output boundary 为 0，status 不会被 transcript 覆盖。这是纯内存 shadow，不读 terminal/surface state，尚不覆盖 cursor-parking、handoff/scrollback、lease 或真实 terminal failure，不能宣称 physical Compose 或生产 full-frame parity。

> **History effect data-plane 增量（2026-08-04）**：`AppState.HistoryEffects` 已持有 tokenized Pending/InFlight/Acked/Failed/Invalidated ledger；planner 按与 `LayoutAppScreen` 相同的物理换行结果选择完整 finalized cell。geometry transition 对仍 eligible 的 Pending 仅 rebase，失去 eligibility 只标 Invalidated，均不 mint/delete token；in-flight resize/replacement 必须转 Unknown/recovery。Begin 按 token 顺序、lease/Unknown/generation 受控，Ack 才释放 payload；`HistoryCommitExecutor` 只是一条注入 sink 的非生产 transaction seam，用于验证 Begin -> result -> Ack/Fail/Deferred 的 actor 顺序、短写/sink panic 保守失败和 deferred 无忙循环。失败或 in-flight invalidation 即使经过 visible-frame recovery 也继续阻断后续 token；现有 `HistoryScrollbackReconciled{LayoutGeneration, TerminalEpoch}` 只允许在 terminal reset/replacement 加已确认 source-backed frame 后开启新物理 epoch、丢弃旧 delivery ledger 并从 semantic transcript 重新 mint 单调 token，不能用普通 repaint 静默跳过。尚无 production terminal owner 发出该 barrier，它也未接 `FixedBottomSurface`，production history handoff 仍完全由 legacy path 负责。

> **TerminalSession seam 增量（2026-08-04）**：新增独立、未安装的 `ui.TerminalSession`。`ComposeAppRenderFrame` 先从唯一 `AppState` snapshot 得到与 plain screen row 一一对应的 rich `render.Line` frame，保留 transcript cell kind role、typed ActiveBand 与 status document；eligible finalized `HistoryCommit` 复用该 transcript role line，`ComposeTerminalFramePlan` 同时携带 plain/structured rows，并拒绝 plain-text 不一致的 structured row。TerminalSession 使用由 terminal owner 显式提供的 `ThemeContext` 解析/编码 frame 和 handoff row，theme/profile transition 会 invalidate projection；flush 不读取全局 terminal。它私有持有 viewport-sized `ScreenModel`、cursor、lease、generation 和 frame counter；initial/resize、lease release、显式 invalidation 与短写/error/panic 后均走 Unknown/full repaint，row-to-cell 复用 `vt.Screen`，stale plan 不写 terminal。其 `TerminalTransactionPlan` 可选择一个已 claim 的 `HistoryCommit`：只有同 generation、Known、未 resize、非 lease primary 才按 lease/generation -> geometry -> history handoff -> viewport diff -> cursor -> one target write -> front confirmation 组装；Unknown/resize/lease 则 history Deferred、仅写 source-backed recovery viewport。未安装 `TerminalSessionExecutor` 已验证 actor claim -> fresh snapshot -> transaction -> typed history result、frame invalidated/recovered 的回投顺序，且不会因 worker retry busy loop；隔离的 `HistoryCommitSink` 仍用于单 effect fault seam。它没有连接 `FixedBottomSurface`、runtime/screen lease transport 或 production executor，因此不构成 P3/P4 complete 或 single-writer cutover。

| 阶段 | 当前状态 | 已验证证据 | 仍待完成 |
| --- | --- | --- | --- |
| P0 旁路审计 | **进行中** | `TestChatInteractiveDirectWriterInventory` 以 AST 扫描 `commands/chat*.go` 与 `command.go`，固化 **155 个 grouped debt entries / 550 个 call sites**（491 `fmt.Print*`、49 `fmt.Fprint*(os.Std*)`、1 `io.WriteString(os.Std*)`、9 `ui.WriteTerminal*`）；新增 group 或 count 变化默认使测试失败，并报告实际行号。`/debug display`、`/status`、`/load`、`/goal`、`/memory`、`/stream` 与 `/title`/`/rename` 成功路径已有 raw-stdout/structured producer fence 及 atomic command-cell 回归；`TestChatSystemOutputWriter_ActiveTurnMirrorSurvivesOwnedViewportRepaint` 覆盖 active-turn mirror 在 status/ActiveBand 重绘后仍只出现一次。`functions/builder.go` 的遗留 function-call parser 已删除全部 direct writer，并有 source fence 与 malformed-call 语义测试。 | 每条 chat/command debt entry 标注 owned-safe/plain-only/startup-shutdown/待迁移 owner；补 owner/frame trace；迁移后逐项从基线删除。 |
| P1 CommandResult | **部分完成** | `/debug display`、`/status`、成功 `/load <session-id>`、`/goal`（status/clear/pause/resume/complete/set）、`/memory`（status/add/list/search）、`/stream`（status/toggle/set）与成功 `/title`/`/rename` 已以结构化 `CommandResult` 进入单个 command cell（另有 `chat_title_document.go`）；handler 禁止 direct terminal writer 的测试已覆盖全部新式 producer。`/goal <objective>` 在确认 cell 提交后经 `CommandResult.SendObjective` 走正常 send pipeline 触发 AI 目标请求；`/stream` toggle 复用既有 `persistStreamCommandPreference`；title mutation 与成功确认 cell 共用 side-effect boundary。参数错误、持久化/store 错误、`--json` 变体与 nil session 仍走 legacy 路径，以使错误在全部输出模式中可见；成功 `/load` 在确认 cell 后通过 `CommandResult.ReplayHistory` 逐消息回放历史。 | 迁移 `/skills`（selection/modal 交互，留待 order-3 桶）及其余 slash command，删除 `beginDirectInteractiveOutput` 作为通用命令协议。 |
| P3 ScreenLease | **首批完成** | resume、backtrack、theme fullscreen picker 已通过 `ScreenLease` 取得 alternate screen；主屏 flush suspend、release full repaint、DEC 1049 事务边界均有测试。 | 将 lease 上移到最终 presenter API，补 signal safety 与完整失败注入矩阵。 |
| P4–P9 Scene 终局 | **数据层、映射、消费端与文本投影已落地；渲染层切换已具备完整文本等价基线 + 真实写入路径运行时对照（含用户输入块），切换本身未开始** | owned viewport、front/back diff、history window、ActiveBand 与部分 reflow/handoff 能力已存在；**BoundaryPolicy 已落地**（`ui/boundary` 纯函数 + §7.3 规则表全行测试 + INV-GAP 不变量测试，见 §21 切片 4）；**Scene 数据层核心已落地**（`ui/scene`：`TuiScene`/`TranscriptCell`（8 类 kind/4 态 phase/ChainKey 归组）、`SceneTransaction` + `SceneController.Submit`（快照式原子回滚，INV-FRAME-01）、`LayoutTranscript`（gap 决策委托 `boundary.ResolveGap`，INV-GAP-03）、~30 个测试覆盖 INV-SCENE-02/03/04 与 §7.2 内部空行保留，见 §21 切片 3 注记）；**ChangeSet→SceneTransaction 映射已落地**（`ui/scene/from_changeset.go` 的 `ChangeSetMapper`：`ItemChange.Op` → `AppendCell/UpdateCell/FinalizeCell/RemoveMutableCell`；CellID 由 `Item.ID`（"item-{n}"）解析（重放身份稳定，INV-SCENE-02）；cell Revision 统一递增（tool 输出合并与 tool_call 更新共享 cell，INV-SCENE-03）；tool_output 按 CauseID 合并进链首、孤儿独立成块；映射/合并/孤儿/移除约束/revision 单调/重放确定性与事务原子性测试，见 §21 切片 5 注记）；**ChangeSet 消费端（bridge）已落地**（`chatRuntimeEventBridge` 在事件接入点 `applyChangeSet` 即时 `ChangeSetMapper.Apply` 提交进 `renderScene`，`sceneMu` 保护 + 失败计数/最近错误诊断；`replayEventLog` 按 append-only 事件日志幂等重建 Scene；`/debug` 输出 "Unified Render Scene:" 审计段对照 CellID/顺序；模型顺序跟随/重放重建/失败计数/乱序增量一致性测试，见 §21 切片 6 注记）；**渲染层切换可行性已验证**（gap parity 双跑测试：同一会话序列下旧路径空行序列 == `LayoutTranscript` gap 行序列，逐项一致，见 §21 切片 7；`/debug` 审计段含 `Layout Rows`/`Layout Gaps` 摘要）；**文本投影层已落地**（`ui/scene.RenderText`：Scene 快照 → 最终文本行，gap 行投影为空行，无状态纯函数；完整文本 parity 测试：真实事件流 → EventEncoder → ChangeSet → Scene → RenderText 与旧路径 coordinator 输出**逐行一致**（含内容文本，不只 gap），tool 链 gap 结构测试，见 §21 切片 8；`/debug` 审计段增加 `Layout Text Rows`）；**`RenderText` 已接入真实写入路径（运行时双跑文本对照）**（coordinator `writeRowsLocked` 完整块提交点挂可选探针 `textParityFn`，bridge `checkTextParity` 按块把旧路径实际行序列与 Scene 快照 `RenderText` 逐行对照，matched/missed + 最近不一致详情入 `/debug` `Text Parity` 审计段；构造器与 `ensureChatRuntimeEventBridge` 双保险接线；真实写入路径 matched 测试 + 分歧检测 + nil 旁路安全测试，见 §21 切片 9）；**用户输入数据面通道已闭合**（编码器 `SubmitUserInput` 提交 KindUser 终态块；bridge `submitUserInput` 经 `renderMu` 串行化、走 `applyChangeSet` 同一提交路径并落事件日志；`replayEventLog` 支持事件 + 用户输入混合记录按全序重放恢复；coordinator live 用户提交点注入、历史回放路径不注入；探针 `checkTextParity` 重构为按 cell 对照（user cell 剥离 `"> "` 样式前缀与 prompt 重绘 gap）；交错真实序列 4/4 matched + 全量文本/gap 位置一致 + 混合日志重放等价 + 回放边界测试，见 §21 切片 10）。 | **coordinator gap 状态机切换已完成（切片 11）**：`completeBlockOutput` 全局布尔与旧 helper（`gapForTopLevelMessage`/`gapForEventBlock`/`gapIfPriorComplete`）已删除，完整块写入统一 `gapBeforeBlockLocked(next boundary.CellMeta)` 委托 `boundary.ResolveGap` 规则表（INV-GAP-03）；`writeRowsLocked`/`writeCompleteBlockLocked`/`commitHistoryCellLocked` 三入口携带 CellMeta 并在提交后 `markBlockCommittedLocked` 推进 `lastBlockMeta`，prompt 重绘 gap 由 `gapPreWritten` 显式消费；流式残差/end-reasoning divider/稳定提交按 `streamBoundaryMetaLocked`/`nextSupplementMetaLocked` 提交，`resetStreamLocked`/`resetBlockBoundaryLocked` 重置流 ID（跨 turn 不串 ID）。剩余：presenter/渲染层切换（以 Scene 快照 `RenderText` 为 presenter 写出行源；交互主路径与事件流链路的完整文本等价已由切片 7/8 parity 测试固化，真实写入路径逐块对照已由切片 9/10 探针审计）、统一 runtime/tool event writer、legacy 删除及 PTY/ConPTY 验收。 |

P0 inventory 是迁移债务账本和 regression fence，**不是**对 raw terminal writer 的授权；它刻意按 file/function/kind/count 分组，避免普通源码行号移动造成基线漂移，同时保留实际行号用于失败诊断。旧的 [Phase 0 inventory](./aicli-ui-rendering-phase0-inventory.md) 仅是内容渲染时期的抽样记录，不是这个 owned-interactive 门禁的第二份 allowlist。

---

## 0. 执行摘要

当前 TUI 已经具备 owned viewport、front/back diff、ActiveBand、prompt、popup、history window 等 retained-mode 能力，但生产路径中仍长期保留 immediate-mode 输出：slash command、runtime diagnostics、tool builder 或 library 直接调用 `fmt.Print*`、`os.Stdout`、`os.Stderr` 或 terminal primitive。两类路径写入同一个主终端，却只在字节层共享锁，不共享 history、layout、cursor、front buffer、handoff frontier 和 frame generation。

这导致的症状不是彼此独立的偶发缺陷，而是同一个所有权缺失问题的不同表现：

- 历史消息重复追加、finalize 后再次绘制；
- raw 输出出现在旧消息、ActiveBand 或 prompt 中间；
- 下一帧覆盖不在 Scene 中的文字；
- user/assistant/event/tool/supplement/reasoning block 的 gap 不一致；
- ActiveBand grow/shrink 后永久留下空洞；
- fullscreen 使用 `Disable()/Enable()` 后 retained history 和 UI state 丢失；
- resize/reflow、native scrollback handoff 和 soft output 各自维护“已输出”状态，真相源分裂；
- 旧 immediate renderer 与新 retained renderer 同时存在，修复一条路径后其他入口仍可复现。

长期目标不是把所有 UI 状态塞进一个无边界的大对象，而是建立以下架构约束：

```text
多个业务生产者
  -> 一个有序 RenderEvent 队列
  -> 一个权威 AppState（包含 transcript Scene）
  -> 一个 Layout/Compose 流程
  -> 一个 TuiPresenter
  -> 一个物理 TerminalWriter
```

允许多个逻辑 layer；不允许多个 physical screen writer。允许 JSON、pipe、noninteractive 使用 plain renderer；但一个会话生命周期只能选择一种 renderer mode。允许 fullscreen 使用 alternate screen；但必须通过排他的 screen lease 获取物理终端所有权，不能销毁主 Scene。

本文的核心决策为：

1. `AppState` 是交互 UI 的唯一状态真相，现有 `TuiScene` 是其中的 transcript 子状态；终端屏幕和 Backend front buffer 都不是业务数据源；
2. 所有永久 transcript 输出都以有稳定身份的 semantic cell 存储，显示行由 `DisplayLines(width)` 派生；
3. cell 内部默认稠密，cell 之间的 gap 由单一 boundary policy 计算，gap 不由调用点自行拼接空行；
4. mutable cell 的 update/finalize 是 replace/commit transaction，不得通过“再 append 一份 final 文本”完成；
5. 每个逻辑 block 通过一次 Scene transaction 提交，每个 frame 通过一次 presenter transaction flush；
6. 只有持有 screen ownership 的 presenter 能输出 ANSI 和终端字节；
7. fullscreen 使用 alternate-screen lease；lease 期间主 Scene 可继续更新但暂停主屏 flush，释放后 invalidation + full repaint；
8. handoff frontier 单调前进，同一个 cell/revision/row range 最多向 native scrollback 交接一次；
9. replay 与 live 使用相同的 cell sequence、boundary policy、layout 和 presenter；
10. 迁移完成后删除 production legacy renderer、gap 布尔补偿状态机和 fullscreen `Disable()/Enable()` 暂停路径。

---

## 1. 当前问题与根因

### 1.1 当前的两条主输出路径

正确的 retained 路径：

```text
Chat/Event/Command Result
  -> chatInteractionCoordinator / scene adapter
  -> FixedBottomSurface.WriteOutput
  -> historyWindow / retained state
  -> viewport.Compose
  -> Backend front/back diff
  -> Terminal
```

高风险 immediate 路径：

```text
Handler/Library/Runtime
  -> fmt.Print* / os.Stdout / os.Stderr / direct terminal write
  -> 当前物理光标
  -> Terminal
```

raw 路径不会同步：

- retained history；
- Backend front buffer；
- 当前 Scene revision；
- layout 与 bottom reserve；
- cursor intent；
- soft/mutable cell revision；
- native scrollback handoff frontier。

因此 terminal mutex 只能防止字节交叉，不能使两个 renderer 对屏幕状态达成一致。任何一次 status tick、prompt repaint、ActiveBand resize、popup 开关或 terminal resize，都可能让 retained presenter 覆盖、移动或重复 raw 输出。

### 1.2 已确认的具体架构风险

1. `FixedBottomSurface.WriteOutput` 会将 owned output 追加到 `historyWindow` 并重新渲染 viewport；raw stdout 不经过该流程。
2. viewport Backend 使用 front/back diff；任何旁路终端 mutation 都会使 front buffer 与物理屏幕失真。
3. `chatInteractionCoordinator.writeRowsLocked` 已证明完整 block 必须一次多行写入；逐行释放 surface lock 会允许 ActiveBand/status 尺寸变化插入永久空洞。
4. `completeBlockOutput` 和多个 `gapFor*` helper 仍体现“根据前一次调用推断 gap”的历史模型；长期应被 cell boundary policy 取代。
5. `Disable()` 会清空 popup、composer、prompt、ActiveBand、status、Backend 和 retained history；它是 destructive teardown，不是 fullscreen suspend。
6. 当前 history window 同时承担可见历史、restore headroom、soft rewrite 和 native scrollback handoff，cell identity 与 row identity 不完整，容易出现二次提交或错误 suffix replace。
7. runtime/tool 的部分 writer 已 surface-aware，但仍存在直接 stdout/stderr 的 active-turn diagnostics，说明输出契约尚未在模块边界强制执行。

### 1.3 当前代码锚点

以下路径是实施审计和首批迁移的主要入口；行号可能随实现变化，评审时以符号名为准：

| 关注点 | 当前代码锚点 | 长期归属 |
| --- | --- | --- |
| surface/Layout/InputBox 初始化 | `backend/cmd/aicli/commands/chat_setup.go` | renderer mode bootstrap |
| chat transcript 路由 | `backend/cmd/aicli/commands/chat_transcript_renderer.go` | RenderEvent adapter |
| 完整 block 原子写入与现有 gap helper | `backend/cmd/aicli/commands/chat_interaction.go` | Scene transaction + BoundaryPolicy |
| owned history、soft output、handoff、Disable | `backend/cmd/aicli/ui/fixed_bottom_surface.go` | Scene/Presenter/Handoff 分拆 |
| viewport 合成 | `backend/cmd/aicli/ui/viewport/compose.go` | Compositor |
| front/back diff | `backend/cmd/aicli/ui/viewport/backend.go` | TuiPresenter |
| 过渡期 surface-aware direct output | `backend/cmd/aicli/commands/chat_surface_output.go` | CommandResult adapter |
| `/debug display` raw 输出 | `backend/cmd/aicli/commands/chat_debug.go` | 首批 CommandResult 迁移 |
| active-turn tool diagnostics | `backend/cmd/aicli/functions/builder.go` | logger/RenderEventWriter |
| alternate screen frame | `backend/cmd/aicli/ui/fullscreen_list.go` | fullscreen presenter + lease |
| fullscreen 调用者 | `backend/cmd/aicli/commands/chat_resume_command.go`、`backend/cmd/aicli/commands/chat_backtrack_select.go`、`backend/cmd/aicli/commands/chat_theme_command.go` | 统一 ScreenLease |

### 1.4 症状到根因的映射

| 症状 | 直接原因 | 深层根因 |
| --- | --- | --- |
| `/debug display` 嵌入消息流中间 | handler 直接写当前物理光标 | command 没有返回 Scene mutation；主屏有多个 writer |
| 下一帧覆盖命令输出 | Backend front 不包含 raw 文本 | front buffer 与物理屏幕分裂 |
| final assistant/tool 重复绘制 | mutable 内容与 final 内容分别 append | 缺少稳定 cell ID、revision 和原子 finalize |
| event/tool block gap 时有时无 | 调用点拼空行或读取历史布尔标志 | gap 不是 boundary domain model |
| ActiveBand 变化后历史出现洞 | 多行 block 分次输出或 bottom reserve 中途改变 | logical block/frame 不是事务 |
| resize 后行序、换行或重复异常 | 以旧 display rows 推断 source | semantic cell 不是唯一真相源 |
| fullscreen 返回后历史丢失 | 使用 `Disable()/Enable()` | 没有 screen lease 与 suspend/resume 状态机 |
| scrollback 重复/漏行 | retained rows 和 handed-off rows 缺少稳定交接记录 | handoff frontier/record 不完整 |

### 1.5 为什么不能继续做局部补丁

只把 `/debug` 改成 surface-aware block 可以修复当前命令，但不能阻止新的 handler、library 或 runtime callback 再次旁路。继续增加 `lastXXX`、`alreadyPrinted`、`promptAfterXXX` 或 scroll compensation 布尔字段，会把终端物理状态反向变成业务真相，导致状态组合指数增长。

长期方案必须先确立所有权和数据模型，再迁移入口，最后删除旧模型；不能长期让两个 production renderer 双写并以视觉结果“碰巧一致”作为正确性标准。

---

## 2. 目标、非目标与成功定义

### 2.1 长期目标

- 交互会话中所有可见内容都可追溯到一个 Scene node/cell 或 overlay state；
- 同一时刻只有一个组件拥有主终端/alternate screen 的物理写权限；
- history、mutable turn、ActiveBand、status、prompt、popup 的职责和生命周期清晰；
- live、replay、resume、resize 使用同一 cell/layout/boundary 逻辑；
- block 不重复、不覆盖、不乱序，gap 可由规则表确定；
- fullscreen 不销毁主 Scene，退出后可从权威状态完整恢复；
- scrollback handoff 可证明 exactly-once，retained tail 可安全 reflow；
- 迁移后能删除旧 immediate production path 和补偿状态机，而不是永久兼容。

### 2.2 非目标

- 不改变 JSON schema、pipe 输出和独立非 chat CLI command 的 plain-output 语义；
- 不要求应用无限期保留所有已交给 native scrollback 的 display rows；
- 不将 popup、status、prompt 写进 transcript；
- 不以第三方 TUI 框架替换全部现有代码；可以复用现有 viewport、render、VT 和 terminal abstraction；
- 不在一个阶段内大爆炸式重写所有 command 和 UI 组件；采用可验证的小切片迁移；
- 不通过全局重定向 `os.Stdout` 掩盖违规调用，最终仍需在 API 和 CI 层消除旁路。

### 2.3 成功定义

完成态必须同时满足：

1. owned interactive 模式下，production 代码不存在未授权终端写入；
2. 一个逻辑 cell 在 live、finalize、replay、resize、handoff 后仍保持稳定身份；
3. gap 由一处纯函数/规则表生成，业务调用者不插入语义空行；
4. fullscreen、popup、prompt、status 更新不改变 transcript 序列；
5. terminal partial write、resize storm、cancel/panic 后可 invalidation 并从 Scene 恢复；
6. 真实 Windows ConPTY 与至少一种非 Windows ANSI terminal 通过验收；
7. legacy production renderer 和旧补偿状态机有明确删除证据。

---

## 3. 设计原则与强不变量

以下不变量属于架构门禁；实现阶段不得以“当前视觉正常”为理由绕过。

### 3.1 所有权不变量

- **INV-OWNER-01**：owned interactive 会话中，只有当前 `ScreenOwner` 可以调用 terminal byte/ANSI primitive。
- **INV-OWNER-02**：主屏 presenter 与 fullscreen presenter 不能同时持有 screen lease。
- **INV-OWNER-03**：terminal write lock 只在 owner 内部用于 frame 字节原子性，不能作为多 renderer 共存协议。
- **INV-OWNER-04**：plain、JSON、owned renderer mode 在 session 初始化时选定；进入 owned 生命周期后不能临时降级为 raw stdout，再恢复 owned。

### 3.2 Scene 与 history 不变量

- **INV-SCENE-01**：`AppState` 是交互 UI 的唯一权威状态；transcript Scene 是其子状态，物理屏幕、VT snapshot、Backend front 都是派生缓存。
- **INV-SCENE-02**：每个 transcript cell 有不可复用的 `CellID` 和单调递增 `Sequence`。
- **INV-SCENE-03**：mutable update 必须携带匹配的 `CellID` 与更大的 `Revision`；旧 revision 不得覆盖新 revision。
- **INV-SCENE-04**：finalize 是同一 cell 的状态迁移，不是 append 新 cell。
- **INV-SCENE-05**：prompt、status、popup、ActiveBand 不属于 transcript，不改变 transcript sequence 或 boundary state。

### 3.3 block 与 gap 不变量

- **INV-GAP-01**：cell 内没有隐式首尾空行；内容本身的空行属于 cell source。
- **INV-GAP-02**：独立 top-level transcript cells 之间最多一个语义 gap。
- **INV-GAP-03**：gap 由 `BoundaryPolicy(prev, next)` 生成，不能由 renderer 调用点逐行推断。
- **INV-GAP-04**：mutable update、ActiveBand redraw、resize、replay 和 handoff 不改变既有 cell boundary。
- **INV-GAP-05**：空 block、被过滤事件和无可见内容的 update 不推进 boundary state。

### 3.4 frame 与失败恢复不变量

- **INV-FRAME-01**：一个 Scene transaction 要么完整应用，要么不应用；不能提交半个 tool/event block。
- **INV-FRAME-02**：一次 presenter frame 只能基于一个不可变 AppState snapshot 和一个 terminal size generation。
- **INV-FRAME-03**：front buffer 只在 terminal flush 全部成功后更新。
- **INV-FRAME-04**：partial/failed write 后 front buffer 必须失效，下一次使用 full repaint；不得继续基于未知物理屏幕做 diff。
- **INV-FRAME-05**：frame 完成后的物理 cursor 必须等于 snapshot 中的 `CursorIntent`。

### 3.5 scrollback handoff 不变量

- **INV-HANDOFF-01**：handoff frontier 单调前进，不能回退到已交给 native scrollback 的 range。
- **INV-HANDOFF-02**：同一 `CellID + Revision + DisplayRange + LayoutGeneration` 最多 handoff 一次。
- **INV-HANDOFF-03**：只有 committed、不可再 mutation 的内容可以 handoff。
- **INV-HANDOFF-04**：handoff 不产生新 cell、不改变 gap，也不能再次走普通 append 路径。
- **INV-HANDOFF-05**：resize 只 reflow retained tail；已进入 native scrollback 的物理行视为不可重排。

---

## 4. 目标架构与职责边界

### 4.1 总体数据流

```text
 User Input   Chat Runtime   Tool Runtime   Slash Command   Diagnostics
     │             │              │               │              │
     └─────────────┴──────────────┴───────────────┴──────────────┘
                                   │
                          RenderEvent / CommandResult
                                   │
                         bounded ordered UI queue
                                   │
                            SceneController
                     validate -> reduce -> transaction
                                   │
                                TuiScene
     ┌──────────────────────────────────────────────────────────────┐
     │ Transcript Cells / Mutable Cells / Handoff State            │
     │ ActiveBand / Status / Prompt+Editor / Popup Stack            │
     │ Cursor Intent / Theme / Dimensions / Generations            │
     └──────────────────────────────────────────────────────────────┘
                                   │ snapshot
                              LayoutEngine
                    semantic cells -> width-aware rows
                                   │
                               Compositor
                 history + bottom layers + overlays + cursor
                                   │ Frame
                              TuiPresenter
                   back buffer -> diff -> atomic terminal flush
                                   │
                    ScreenLease + TerminalWriter (single owner)
                                   │
                               Physical TTY
```

### 4.2 组件职责

| 组件 | 负责 | 不负责 |
| --- | --- | --- |
| `SceneController` | 串行消费事件、校验 revision、事务式修改 Scene | ANSI、终端坐标、直接输出 |
| `AppState` | transcript Scene、active/bottom/geometry/lease 状态与 generation | 终端 I/O、diff 算法 |
| `LayoutEngine` | 按 width/theme 将 source 转为 display rows，测量各 layer | 修改 Scene、写终端 |
| `Compositor` | 将可见 history、ActiveBand、status、prompt、popup 合成为 frame | 业务事件排序、stdout |
| `TuiPresenter` | front/back、diff、cursor、同步更新、flush、失败失效 | 业务 cell gap 决策 |
| `ScrollbackHandoff` | 选择 eligible committed range、记录 exactly-once frontier | mutable cell、prompt/popup |
| `ScreenLeaseManager` | primary/alternate 所有权与 suspend/resume | 清空 Scene |
| `PlainRenderer` | JSON/noninteractive/pipe 的顺序输出 | owned interactive 主屏 |

### 4.3 renderer mode

```go
type RendererMode uint8

const (
    RendererOwnedInteractive RendererMode = iota
    RendererPlain
    RendererJSON
)
```

选择规则：

- interactive + ANSI/terminal capability 满足：`RendererOwnedInteractive`；
- pipe、`NoInteractive` 或 capability 不满足：`RendererPlain`；
- JSON output：`RendererJSON`。

每种 mode 使用各自完整 pipeline。禁止同一 session 中以 `fmt.Print*` 临时穿透 owned presenter。若 capability 在运行期失效，应执行明确的 controlled teardown/degrade：先停止事件接收或切换到可重建状态，不能让两个 renderer 重叠工作。

### 4.4 建议的包边界

可在现有目录中渐进拆分，命名仅为建议：

```text
ui/scene/        semantic state、cell、boundary、event reducer
ui/layout/       width-aware DisplayLines 与 pane measurement
ui/viewport/     frame compose 与 front/back backend
ui/presenter/    terminal flush、cursor、frame generation
ui/screenlease/  primary/alternate ownership
commands/        CommandResult 与 scene adapter，不含 ANSI/stdout
```

### 4.5 目标运行范式与状态分层（规范性补充）

终局采用 **actor 单事件循环 + 单向数据流 reducer + retained AppState + command/effect queue + transactional presenter**。`FramePump` 只能合并 draw intent，不能执行会重新读取和修改业务状态的任意 callback。

```text
Runtime / Input / Resize / Timer / Lease / EffectResult
                         |
                    UIAction mailbox
                         |
              UIController / Reducer（唯一写者）
                 | AppState snapshot
                 | TerminalEffect queue
                 | dirty/frame request
                         |
                  Pure Layout/Compose
                         |
              FramePlan + CursorPlan + EffectPlan
                         |
              TerminalSession transaction
                         |
                  Ack/Fail UIAction
```

状态必须按职责分开，不能再统一命名为 `history`：

| 状态 | 内容 | 所有权 |
| --- | --- | --- |
| `TranscriptState` | semantic cells、稳定 ID、revision、finalization | reducer 唯一写 |
| `ActiveCellState` | 当前 assistant/reasoning/tool 的 mutable source 与 stable range | reducer 唯一写 |
| `BottomPaneState` | ActiveBand 的派生展示、status、prompt、popup、focus | reducer 唯一写 |
| `GeometryState` | terminal size、viewport rect、layout generation | reducer 唯一写 |
| `LeaseState` | primary/alternate ownership 与 barrier | reducer 唯一写 |
| `TerminalProjectionState` | front/back、cursor、scroll region、Known/Unknown | presenter 独占 |
| `HistoryEffectQueue` | tokenized pending/in-flight/acked handoff | reducer 生成，presenter 执行 |

`ActiveCellState` 是流式文本的语义源；ActiveBand 只是 Layout 根据 active cell、工具进度和 bottom-pane policy 生成的可见投影。不得同时在 ActiveBand buffer 与 mutable transcript cell 中维护两份可独立推进的正文。

语义生命周期与物理交接生命周期必须分离：

```text
Semantic:   Mutable -> Finalized
Projection: Retained -> CommitPending -> InFlight -> AckedHandedOff
```

cell `Revision` 解决内容版本；effect cursor 解决“已排队/已写入/已确认”的副作用进度。二者不能互相替代。允许一个 streaming controller 保存类型明确的 `enqueued`/`acked` source range，但禁止 coordinator、surface 和 presenter 分别保存同一范围的平行游标。

重构期间可保留 `FixedBottomSurface` 作为 facade，但其内部职责必须逐步委托给上述组件；最终 facade 不再同时维护业务状态、terminal mutation 和补偿状态机。

---

## 5. AppState 与 Scene 数据模型

### 5.1 顶层 AppState

```go
type AppState struct {
    Revision        uint64
    EventSequence   uint64
    Transcript      TranscriptState
    ActiveBand      ActiveBandState
    Status          StatusState
    Prompt          PromptState
    Popups          PopupStack
    Cursor          CursorIntent
    Theme           ThemeState
    Terminal        TerminalState
    Fullscreen      FullscreenState
}
```

本文早期使用“顶层 `TuiScene`”表示全部 UI 状态；为避免与当前 `ui/scene.TuiScene`（仅 transcript cells）混淆，终局统一称 `AppState`。现有 `ui/scene.TuiScene` 迁移后成为 `AppState.Transcript` 的实现或被 `TranscriptState` 包装，不得继续同时声称自己包含全部 overlay/geometry/lease 状态。

职责边界：

- `Transcript` 是持久语义历史；
- `ActiveBand` 表示当前运行中、允许高频更新但尚未提交到 transcript 的活动区域；
- `Status` 是瞬时状态，不参与历史顺序；
- `Prompt`/editor 是输入状态，不参与 transcript；
- `Popups` 是 overlay stack，不修改下层数据；
- `Cursor` 是 frame 输出意图，不从物理 cursor 反推；
- `Terminal` 只保存 width/height/capability/generation，不保存业务文本。

### 5.2 Transcript cell

```go
type CellID uint64

type TranscriptCell struct {
    ID          CellID
    Sequence    uint64
    Kind        CellKind
    Source      RichDocument
    Revision    uint64
    Phase       CellPhase
    Boundary    BoundaryClass
    Provenance  Provenance
    CreatedAt   time.Time
    FinalizedAt *time.Time
}

type CellPhase uint8

const (
    CellMutable CellPhase = iota
    CellFinalized
)
```

`CellPhase` 只描述语义可变性。`CommitPending/InFlight/AckedHandedOff` 属于 `HistoryEffectQueue`/`TerminalProjectionState`，不得写回成另一套业务内容状态。当前代码中的 `CellCommitted/CellPartiallyHandedOff/CellHandedOff` 是过渡实现，迁移时拆分；在拆分完成前不得由这些 phase 决定是否丢弃 semantic source。

`Kind` 至少区分：

- user；
- assistant；
- tool chain；
- runtime event；
- supplement/reasoning；
- system/notice/warning；
- command result；
- diagnostic（仅显式允许进入交互 transcript 的诊断）。

`Source` 保存语义内容或 render document，而不是终端换行后的字符串数组。`DisplayLines(width, theme)` 是派生函数。ANSI 不应成为 source truth；样式应尽可能保存为 span/role，最终由 presenter 编码。

### 5.3 稳定身份与 revision

- 创建 cell 时分配 `CellID`，整个 live → final → replay/persist 映射期间不变；
- `Sequence` 只在创建 top-level cell 时增加，update/finalize 不增加；
- 每次 mutable update 增加 `Revision`；过期 update 可被安全丢弃；
- finalization 将 `CellMutable` 转为 `CellFinalized`，并在同一 transaction 中清除 active projection；
- 不允许用文本相等判断“是否已经输出”，文本相同不代表相同 cell，文本不同也不代表需要新 append。

### 5.4 ActiveBand 的边界

ActiveBand 只承载仍在运行、需要频繁重绘且不应进入不可变历史的状态，例如：

- assistant streaming 尚未稳定的 tail；
- running tool chain；
- 当前 turn 的进度、elapsed 或 spinner；
- 等待授权、等待子任务等可变状态。

ActiveBand 更新：

- 不增加 transcript sequence；
- 不改变 committed cell boundary；
- 不触发 native scrollback handoff；
- finalize 时通过一个 transaction 转换/合并为 committed cell，不能先 append final 再清 ActiveBand。

### 5.5 Popup、status 与 prompt

- popup 只改变 overlay stack；关闭后下层 frame 从 Scene 重新合成；
- status 可以被 coalesce，只保留最新 revision；
- prompt/editor 保存 source、selection、viewport 和 cursor intent；
- 三者都不贡献 transcript gap，不参与 history replay，不得写入 `historyWindow`。

---

## 6. RenderEvent、排序与 Scene transaction

### 6.1 事件模型

业务层不得表达“移动光标、清行、打印空行”，只能表达 UI 意图：

```go
type RenderEvent interface {
    EventID() uint64
    Source() EventSource
}

type AppendCell struct { Cell TranscriptCell }
type UpdateCell struct { ID CellID; Revision uint64; Source RichDocument }
type FinalizeCell struct { ID CellID; Revision uint64; Source RichDocument }
type RemoveMutableCell struct { ID CellID; Revision uint64 }
type UpdateActiveBand struct { Revision uint64; Model ActiveBandModel }
type UpdateStatus struct { Revision uint64; Model StatusModel }
type UpdatePrompt struct { Revision uint64; Model PromptModel }
type PushPopup struct { Popup PopupModel }
type PopPopup struct { PopupID string }
type Resize struct { Width, Height int; Generation uint64 }
type EnterFullscreen struct { Request FullscreenRequest }
type ExitFullscreen struct { LeaseID uint64 }
```

事件可以来自多个 goroutine，但进入 UI queue 后必须获得全局消费顺序。跨 source 有业务因果关系时使用 parent event/turn/tool sequence，而不是依赖 goroutine 调度时间。

> **上游衔接（统一编码器）**：`RenderEvent` 的 `CellID` 与业务 sequence 的**分配者**是上游统一编码器（`EventEncoder`，见 [unified-encoder-plan](./aicli-event-stream-rendering-order-unified-encoder-plan.md)）。所有上游事件（LLM delta、工具事件、命令日志）先经编码器产出 `ChangeSet`（按 `Item.ID` 去重合并、携带 `Seq/CauseID`），本层的 `AppendCell/UpdateCell/FinalizeCell/RemoveMutableCell` 由 `ChangeSet` 直接映射为一次 `SceneTransaction` 提交；本层**不负责**顺序推断与身份分配，只负责模型到屏幕的事务化呈现。用户交互事件（`/debug`、`/model`）不参与编码器因果链，其输出以编码器 `Tail` 为锚点进入本层序列。

### 6.2 transaction 生命周期

每次消费一个事件或一个明确的事件 batch：

```text
Validate
  -> Reduce to candidate Scene
  -> Check invariants
  -> Commit Scene revision
  -> Enqueue terminal effects / mark dirty
  -> Produce immutable snapshot
  -> frame scheduler coalesces draw intent
  -> Layout/Compose
  -> Presenter transaction
  -> Ack/Fail 作为新 UIAction 返回 reducer
```

Scene commit 与 terminal flush 不是同一个事务：Scene transaction 保证业务状态原子；Presenter transaction 保证单次物理写原子。front buffer、handoff frontier 和 effect ack 只能在完整写成功后推进。失败时 reducer 保留未确认 effect，Presenter 将 projection 标记为 `Unknown`，下一帧执行 recovery barrier + full repaint。

如果事件涉及“final assistant + final tool chain + clear ActiveBand + update status”，应允许作为一个 `SceneTransaction` 批量提交，确保用户永远看不到中间状态。

```go
type SceneTransaction struct {
    Cause      EventID
    Mutations  []SceneMutation
    Flush      FlushPolicy
}
```

`FlushPolicy` 可区分 immediate、coalescable 和 no-primary-flush-during-lease，但不能绕过 Scene。

### 6.3 排序规则

1. 用户提交产生 user cell 后，才开始对应 assistant turn；
2. 同一 turn 内依据 runtime event sequence 排序，不以完成回调到达时间重新排序；
3. mutable update 只更新自己的 `CellID`，不能插入另一个 committed cell 前面；
4. async system/diagnostic event 若无父 sequence，作为独立 top-level cell 在被 SceneController 接收时分配 sequence；
5. status/progress 不使用 transcript sequence；
6. replay 读取持久化顺序并重建相同 cell sequence；不得通过“逐行模拟 live 输出”恢复历史。

### 6.4 队列与背压

事件队列必须 bounded，但不同事件采用不同策略：

| 事件 | 队列满时策略 |
| --- | --- |
| committed cell append/finalize | 不得丢；阻塞、持久化或触发受控故障 |
| user input/prompt submit | 不得静默丢失 |
| mutable streaming update | 可按 `CellID` 合并，只保留最新 revision |
| status/spinner/elapsed | 可 coalesce，只保留最新状态 |
| resize | 可 coalesce，只保留最新 generation |
| popup push/pop、lease acquire/release | 不得重排或丢失 |

背压处理不能回退到 raw stdout。

---

## 7. 统一 block boundary 与 gap policy

### 7.1 基本模型

Gap 是两个 top-level transcript cell 之间的语义边界，不是某个 cell 尾部保存的空字符串，也不是“上一次是否打印过完整 block”的全局布尔值。

```go
type BoundaryClass uint8

const (
    BoundaryDense BoundaryClass = iota
    BoundaryNormal
    BoundarySection
)

func ResolveGap(prev, next TranscriptCell) GapRows
```

实现初期 `GapRows` 只允许 `0` 或 `1`。若未来确需 section divider，应引入显式 separator cell/style，不能把多个不可追踪空行塞进 history。

### 7.2 规范化规则

- cell source 不保存由 renderer 添加的 leading/trailing blank row；
- markdown/code/preformatted source 中用户实际提供的内部空行必须保留；
- renderer 对 source 做行尾规范化，但不得用 `TrimSpace` 破坏代码块内部空白；
- gap 在 layout/compose 阶段由相邻 cell metadata 派生；
- handoff 时 gap 与后继 cell 的归属必须明确，建议作为 boundary row 归属于后继 cell 的 display projection，或使用独立 `BoundaryRecord`，二者选一后全局一致。

### 7.3 规则表

| 前一项 | 后一项 | gap | 说明 |
| --- | --- | ---: | --- |
| 无 | 任意首 cell | 0 | transcript 不以空行开头 |
| user | assistant | 1 | 独立 top-level 对话块 |
| assistant | user | 1 | turn 边界 |
| 任意 committed top-level | 独立 command/system/notice | 1 | 最多一个语义 gap |
| 同一 tool-chain cell 内的 tool events | 下一 tool event | 0 | cell 内稠密 |
| 独立 final tool/event cell | 下一独立 final cell | 1 | top-level boundary |
| supplement/reasoning 属于同一 assistant cell | 同 cell 后续 section | 由 cell 内 layout 决定，默认 0 | 不读取全局 gap 状态 |
| mutable cell revision N | revision N+1 | 不适用 | replace，不创建边界 |
| mutable cell | 同 ID finalization | 不新增 | replace/commit transaction |
| ActiveBand/status/prompt/popup | 任意 transcript cell | 不参与 | overlay 不能改变 transcript gap |
| filtered/empty event | 任意 | 不参与 | 不推进 boundary |
| replay cell | replay next cell | 与 live 相同 | 禁止 replay 特例 |
| handoff range | retained next cell | 保持原 boundary | handoff 不重新计算业务顺序 |

若产品最终希望某些组合稠密，只修改这一规则表及其测试，不在不同 renderer 中增加 `gapForTool`、`gapForAsyncLine` 等分支。

### 7.4 gap 所有权

必须选择并固定一种实现：

**推荐方案：派生 boundary row。** Scene 只保存 semantic cells，`LayoutTranscript` 遍历相邻 cell 时插入 gap row，并为该 row 标记稳定的 boundary key：

```text
BoundaryKey = (PrevCellID, NextCellID, PolicyVersion)
```

这样可以：

- resize 时稳定重算；
- mutable update 不重复添加空行；
- handoff 时以 boundary key 判断是否已提交；
- 测试可以直接断言 cell sequence 与 boundary sequence。

禁止同时在 cell source 尾部和 boundary policy 中保存 gap，否则必然出现双空行。

---

## 8. History、mutable cell 与 exactly-once correctness

### 8.1 Cell 生命周期

```text
Created
  -> Mutable (optional, revision 1..N)
  -> Finalizing
  -> Finalized
```

对应的 display projection 独立经历：

```text
Retained
  -> CommitPending
  -> InFlight
  -> AckedHandedOff
```

超大 cell 的部分交接只更新 projection record，不改变 semantic cell phase。这样 resize/replay 仍能从 source truth 生成内容，而 Presenter 可以独立证明某个 display range 是否已经写入 native scrollback。

一般 cell 应在完整 committed 后按 cell 边界交接。超大单 cell 若必须分片，分片必须具有稳定身份：

```go
type DisplaySliceID struct {
    CellID           CellID
    CellRevision     uint64
    LayoutGeneration uint64
    StartSourceUnit  uint64
    EndSourceUnit    uint64
}
```

不应只用“当前 historyWindow 前 N 行”作为唯一交接身份，因为 resize 后 display row 数量可能改变。

### 8.2 Finalization 规则

Finalization 必须在一个 transaction 内完成：

```text
validate expected revision
  -> replace mutable source with final source
  -> set phase finalized
  -> clear corresponding ActiveBand projection
  -> recompute boundary/layout
  -> compose one frame
  -> flush
```

禁止以下模式：

```text
append final text
clear running text
猜测旧文本是否已出现
按 suffix 字符串替换失败后 reset history
```

当 final source 与 latest mutable source 相同时，仍执行状态迁移，但 display diff 可以为空；不能据此再 append 一份。

### 8.3 Handoff frontier

建议保存结构化 handoff 状态：

```go
type HandoffState struct {
    Generation uint64
    Frontier   HandoffCursor
    Records    Ring[HandoffRecord]
}

type HandoffRecord struct {
    Token       uint64
    CellID      CellID
    Revision    uint64
    SourceRange SourceRange
    RowCount    int
    Width       int
    Frame       uint64
}
```

流程：

1. 从 AppState snapshot 选择 finalized、仍属 retained projection 且满足容量策略的最老 range；geometry change 不参与 eligibility；
2. 生成唯一 handoff token；
3. presenter 在拥有主屏时执行 cursor-neutral history insertion；
4. terminal flush 全部成功后提交 record 和 frontier；
5. 失败则不推进 frontier，front buffer invalid；
6. 重试使用同一 token/range，防止上层重新创建 append；
7. handed-off range 不再参与普通 viewport history append。

### 8.4 Retained tail 与 source truth

应用需要保留足够的 semantic cells/source range 用于：

- 当前 viewport 展示；
- bottom reserve grow/shrink 后恢复；
- resize reflow；
- mutable tail finalization；
- fullscreen 返回后的 full repaint。

物理 terminal scrollback 只保存不可变历史的展示副本，不是恢复 Scene 的数据库。session persistence/replay 必须来自聊天数据模型或 semantic cells，而不是读取 terminal 或 Backend front。

### 8.5 Soft output 收敛

现有 soft output/suffix rewrite 应收敛为 mutable cell update：

- `WriteSoftTrackedOutput` 对应 `UpdateCell(CellID, Revision, Source)`；
- stable prefix 可作为同一 cell 的 committed source range，或在明确边界后拆为已 committed cell；
- replacement 失败不应清空全部 history；revision/source-range 校验失败时记录 invariant violation，并从权威 Scene full repaint；
- 不再用文本 suffix 是否匹配来决定 history 所有权。

---

## 9. Layout、Compose 与事务式 Frame

### 9.1 Layout 输入与输出

Layout 输入必须是同一代 snapshot：

```go
type LayoutInput struct {
    SceneRevision     uint64
    TerminalWidth     int
    TerminalHeight    int
    ResizeGeneration  uint64
    ThemeGeneration   uint64
}
```

输出至少包括：

- retained transcript display rows；
- ActiveBand rows；
- status rows；
- prompt/editor rows及 cursor；
- popup overlay rectangles；
- handoff candidates；
- frame generation 与 snapshot identity。

任何组件不得在 layout 中读取实时可变 Scene；否则一个 frame 可能混用不同 revision。

### 9.2 Compose 层级

建议固定合成次序：

```text
1. base background
2. retained transcript viewport
3. ActiveBand
4. status
5. prompt/editor
6. popup/modal overlay stack
7. cursor intent
```

popup 可以遮盖下层，但关闭 popup 后必须从同一 AppState snapshot 重画下层，不得依赖“恢复之前终端字符”。

### 9.3 Presenter frame transaction

```text
Acquire primary screen ownership
  -> verify lease/generation
  -> build back buffer
  -> diff against known-valid front
  -> emit synchronized terminal update
  -> verify full write
  -> commit front buffer + frame generation
  -> release write section
```

若 front invalid，则跳过 diff，执行 full repaint。full repaint 仍然只能从 Scene/Layout/Compose 生成。

frame flush 期间不允许其他 terminal writer 插入字节。Scene event 可以继续入队，但必须在下一 frame 消费，不能修改正在提交的 snapshot。

### 9.4 Cursor 与 scroll region

- cursor 位置由 `CursorIntent` 决定；
- presenter 是唯一允许 Save/Restore/MoveTo/SetScrollRegion 的组件；
- frame 结束时验证 cursor 是否在合法 prompt/editor cell；
- history handoff primitive 必须 cursor-neutral，或在同一 frame transaction 中恢复确定位置；
- 业务 handler 不知道 terminal row/column，也不能调用 BeginOutput 来猜测光标状态。

---

## 10. 终端写入策略与输出接口

### 10.1 单一物理 writer

所有 ANSI 和 terminal bytes 必须集中到 presenter/terminal sink：

```go
type TerminalSink interface {
    FlushFrame(ctx context.Context, owner ScreenOwner, frame EncodedFrame) error
}
```

`ScreenOwner` 由 lease manager 发放，不能由业务代码构造。terminal lock 位于 `FlushFrame` 内部，业务层不直接获取。

### 10.2 合法的 raw/plain 输出范围

raw/plain renderer 在以下场景合法：

- JSON output；
- noninteractive；
- stdout 是 pipe/file；
- owned surface 启用前的启动失败；
- owned surface完成 destructive shutdown 后的最终退出阶段；
- 独立、未进入 chat TUI 生命周期的 CLI subcommand。

禁止的是：owned interactive 生命周期中任意业务代码绕过 Scene/presenter 写同一主 terminal。

### 10.3 Slash command 契约

目标接口：

```go
type CommandResult struct {
    Blocks  []RenderBlock
    Popup   *PopupModel
    Action  CommandAction
    Notice  *StatusModel
    // ReplayHistory: 仅 /load 类带加载副作用的命令使用；owned interactive
    // 提交确认 cell 后按逐消息 replay 渲染器回放历史，plain/JSON 投影忽略。
    ReplayHistory bool
}

type ChatCommand interface {
    Execute(context.Context, CommandInput) (CommandResult, error)
}
```

command handler：

- 可以读取 session/runtime state；
- 返回结构化结果或 action；
- 不调用 `fmt.Print*`、`os.Stdout`、terminal primitive；
- 不决定 gap、光标和物理换行；
- 多段 debug/status 文档作为一个或多个明确的 top-level block 原子提交。

`/debug display` 是首批迁移样例，但不能成为特例。现有 surface-aware direct output 可作为过渡 adapter，长期仍应返回 `CommandResult`。

`/load` 是副作用型样例：成功输出 = 确认文档（atomic command cell）+ 历史回放（逐消息 cell）。回放不得并入命令文档，否则会破坏 cell 边界与 replay 语义（见 INV-GAP-04 / §8.1）；dispatch 在确认 cell 提交后按 `ReplayHistory` 触发回放，回放渲染器自行选择 surface 或 plain 输出路径。

### 10.4 Runtime/tool/diagnostic 契约

对于接受 `io.Writer` 的组件，提供 Scene-aware adapter：

```go
type RenderEventWriter struct {
    CellID CellID
    Post   func(RenderEvent) error
}
```

adapter 负责：

- UTF-8/行片段聚合；
- 限流与大小上限；
- 将可见输出映射为 mutable/diagnostic event；
- 在 close/finalize 时提交完整 block；
- 不直接触碰 terminal。

纯开发日志写结构化 logger 或文件，不默认进入 transcript。stderr 不能被视为 owned terminal 的“另一条安全通道”；若 stdout/stderr 指向同一 TTY，二者同样受 screen ownership 约束。

### 10.5 审计和 CI 门禁

建立 owned-interactive 范围的旁路审计：

- 静态扫描 `fmt.Print*`、`log.Print*`、`os.Stdout`、`os.Stderr`、`Terminal.*` direct calls；当前 chat/command 的 P0 AST gate 已覆盖其中可直接归属的 `fmt`、`os`、`io` 和 `ui.WriteTerminal*` 调用；
- debt inventory 的每项必须注明 owned-safe、plain-only、startup/shutdown 或待迁移原因与 owner；基线不是允许新增旁路的 allowlist；
- 新 command handler 测试使用 forbidden writer，发现直接输出立即失败；
- debug/test build 记录 terminal sink owner、frame 和调用点；
- P0 分类完成后将 audit 扩展到 `log.Print*`、terminal primitive 和 active-turn producer，并在 CI 中运行。

不建议长期依赖全局替换 `os.Stdout`，因为它会隐藏错误依赖、影响非交互路径和第三方库；API 收敛与静态门禁才是最终方案。

---

## 11. Fullscreen 与 Screen Lease 生命周期

### 11.1 状态机

```text
PrimaryActive
  -> LeaseAcquiring
  -> PrimarySuspended
  -> AlternateActive
  -> AlternateReleasing
  -> PrimaryInvalidated
  -> PrimaryFullRepaint
  -> PrimaryActive
```

`Disable()` 属于：

```text
PrimaryActive -> ShuttingDown -> Disabled
```

两者语义完全不同，不能复用。

### 11.2 Lease API

```go
type ScreenLease interface {
    ID() uint64
    Mode() ScreenMode
    Release(context.Context) error
}

func (p *TuiPresenter) AcquireAlternateScreen(
    ctx context.Context,
    req FullscreenRequest,
) (ScreenLease, error)
```

Acquire：

1. 等待当前 primary frame 完成；
2. 标记 primary flush suspended；
3. 保留完整 Scene、front buffer metadata 和事件队列；
4. 在同一个 terminal ownership transaction 中进入 alternate screen；
5. 将物理写权限交给 fullscreen presenter。

Lease active 期间：

- 主 SceneController 继续处理不可丢事件；
- primary presenter 不 flush；
- status/resize/mutable update 可合并；
- committed transcript event 必须保留；
- fullscreen presenter 只能写 alternate screen；
- 禁止后台 stdout 绕过 lease。

Release：

1. fullscreen presenter 停止提交；
2. 退出 alternate screen 并恢复基础 terminal mode；
3. primary front buffer 标记 invalid；
4. 处理最新 resize generation；
5. 从最新 AppState snapshot full repaint；
6. 恢复 primary flush。

### 11.3 异常安全

- cancel、error、panic 必须通过 `defer`/guard 释放 lease；
- release 幂等，同一 lease 只能恢复一次；
- acquire 失败不能留下 `PrimarySuspended`；
- alternate exit 失败时执行 best-effort terminal reset，并把 presenter 标记为 degraded；
- 测试注入 acquire/enter/render/exit/repaint 每个阶段的失败；
- process signal handler 只做安全终端恢复，不尝试在 signal context 中重建复杂 Scene。

### 11.4 迁移对象

所有 fullscreen 入口统一使用 lease，包括但不限于：

- resume session picker；
- backtrack selector；
- theme selector；
- 未来 model/tool/agent fullscreen picker。

迁移完成后，禁止用 `FixedBottomSurface.Disable()/Enable()` 实现临时 modal/fullscreen。

### 11.5 实施状态（2026-07-31）

首批 foundation 已落地：

- 新增 `ScreenLease` 接口与 `FixedBottomSurface.AcquireAlternateScreen(ctx, FullscreenRequest) (ScreenLease, error)`：
  - 单租约不变量：同一时刻只允许一个 alternate lease，重复获取返回 `ErrScreenLeaseBusy`；
  - `Release` 幂等；lease 实例持有唯一 id；
  - acquire 失败不留下悬挂状态；`Disable()`（进程 teardown）会清掉 lease 且不再向 alternate screen 写入；
- 主 presenter flush 抑制：lease 生效期间所有主屏写入路径（owned 帧、legacy layout/status/popup/prompt/active band、光标移动、scroll debt flush、`WriteOutput` 的终端写）全部短路，retained state 照常更新；
- Release 全量重绘：使 double-buffer 失效后从最新 retained scene 完整 recompose（owned 模式），legacy 模式走 `Enable()` 同款 paint 序列；
- 三个 fullscreen 入口（resume session picker、backtrack selector、theme selector）已从 `Disable()/Enable()` 迁移到 lease；`Disable()/Enable()` 仅保留给进程级 shutdown（`chat_setup` cleanup）；
- DEC 1049 transport 已收进 lease 的同一个 terminal ownership transaction：
  - `AcquireAlternateScreen` 在持有 terminal write lock 的临界区内依次写入 `\x1b[?1049h` / `\x1b[r` / `\x1b[?25l` / `\x1b[2J` / `\x1b[H` 并原子地标记 lease 活跃，主 presenter 无法在“进入 alternate screen”与“primary flush suspended”之间插入任何字节；
  - enter 写失败时在同一锁内 best-effort 回滚 exit 序列，不留下 `PrimarySuspended`，surface 立即可重新 acquire；
  - `Release` 在同一锁内先写 exit 序列（`\x1b[?25h` / `\x1b[r` / `\x1b[?1049l`）再全量重绘 primary，exit→repaint 对终端原子；exit 写失败仍继续重绘并向上返回错误；
  - picker 通过新增 `SelectFullScreenListWithLease` 感知 lease：list 跳过自身的 DEC 1049 序列（避免双写 alternate buffer），只保留 stdin raw-mode 处理；未持有 lease 的调用路径（`SelectFullScreenList`）行为不变；
  - 序列写入使用 `writeLeaseSequencesLocked`（锁内直写），不复用 `writeFullScreenSequences`（后者经 `WriteTerminalText` 会再次获取不可重入的 terminal write lock）；
  - `alternateWriter` 字段允许测试注入字节 sink 断言 enter/exit 边界；testMode 下默认 `io.Discard`，不污染真实终端；
- 回归测试：`screen_lease_test.go` 覆盖 lifecycle、单租约、flush 抑制、release 重绘、teardown 安全，以及新增的 enter/exit 序列边界断言、enter 失败回滚、exit 失败仍重绘三个失败注入用例。

lease 已拥有完整的 alternate-screen 所有权契约（enter/suspend、exit/repaint 各在同一 transaction）。后续阶段应把 lease 上移到 TuiPresenter（§11.2 的最终 API），并补齐 signal-handler 安全恢复（signal 上下文只做终端恢复、不重建 Scene）与 acquire/enter/render/exit/repaint 每一阶段的系统化失败注入。

---

## 12. Resize、Reflow 与 Native Scrollback Handoff

### 12.1 Resize 处理

resize 是 Scene event，不是任意组件直接调用 terminal update：

```text
OS resize signal
  -> coalesced Resize(width, height, generation)
  -> update TerminalState
  -> invalidate layout cache
  -> reflow retained semantic cells
  -> recompute bottom panes and cursor
  -> full/diff frame
```

同一 frame 只使用一个 width/height generation。resize storm 可 coalesce，但最终 generation 必须被绘制。

### 12.2 Reflow 规则

- 使用 cell source 和 style span 重新生成 display rows；
- 不使用旧 physical rows 拼接新 source；
- retained tail 和 mutable cells可 reflow；
- 已 handoff 到 native scrollback 的历史不尝试重写；
- 宽字符、emoji、combining marks、ANSI style continuity 必须由共享 layout/render 库处理；
- 设置 retained source/display cap，防止极端 resize 导致无界 CPU 和内存开销；
- cap 不能破坏 cell identity、handoff frontier 或重复输出。

### 12.3 Handoff 时机

handoff 只能在以下条件同时满足时执行：

- primary screen lease active；
- terminal size generation 稳定；
- range 对应 committed revision；
- range 已离开可重绘/restore headroom；
- 当前无 fullscreen lease 切换；
- presenter 可以在一个 frame transaction 中完成 insert + redraw + cursor restore。

ActiveBand grow/shrink 只是 viewport layout 变化，不得生成 history commit effect，也不得推进 handoff cursor。收缩后从仍属 `Retained` 的 semantic projection 重新 compose；绝不从 native scrollback 拉回。若 retained projection 受容量策略限制，允许可预测地减少可见历史，但不能以任意固定 `headroom`、文本重写或重复 handoff 补偿。

### 12.4 Scrollback 与 Scene 的关系

```text
Session/semantic history      权威、可 replay
Retained Scene tail           权威、可 reflow/repaint
Native terminal scrollback    已交接 display 副本、不可重写
Presenter front buffer        缓存、可随时 invalid
Physical screen               输出目标、永不作为数据源
```

这五者必须在代码类型和命名上明确区分，禁止统一称为 `history` 后依赖调用者猜测其语义。

---

## 13. 并发、锁与调度

### 13.1 单 UI owner

目标态必须使用单 `UIController` actor 串行执行：

- event reduce；
- transaction commit；
- snapshot generation；
- layout/compose 调度；
- presenter frame sequencing；
- lease state transition。

runtime、tool、network、timer goroutine 只投递 event。这样可将大量跨字段 mutex 不变量收敛为事件顺序不变量。

迁移期间若现有 adapter 暂时必须保留 mutex，仍应保持锁顺序：

```text
Scene state lock
  -> snapshot release
  -> presenter state lock
  -> terminal write lock
```

不得在 terminal write lock 内调用可能回调业务层、等待队列或获取 Scene lock 的代码。

`FramePump` 的 callback 只能向 UI mailbox 投递 `DrawRequested`/timer action；不得直接获取 coordinator/surface 锁、修改 AppState 或调用 flush。最终只有 UI actor 能创建 snapshot 和启动 presenter transaction。

### 13.2 Frame 调度

- user submit、cell append/finalize、popup push/pop：要求及时 frame；
- streaming mutable update：在低延迟窗口内合并，例如一帧一次；
- status/spinner：低优先级 coalesce；
- resize：优先于普通 diff，通常触发 full layout；
- fullscreen lease transition：高优先级 barrier；
- handoff：只在 committed scene/frame barrier 上运行。

### 13.3 防止饥饿

持续 streaming 不得让 command result、user input 或 lease release 永久等待。队列调度需要区分：

- durable ordering lane；
- coalescable visual lane；
- control/barrier lane。

但最终 Scene commit 仍形成一个全局 revision 顺序，不能让不同 lane 各自直接写屏。

### 13.4 错误策略

- 事件校验失败：记录 invariant violation，不写 terminal；
- 过期 mutable revision：安全丢弃并计数；
- presenter write 失败：front invalid，停止 incremental diff；
- Scene 无法恢复的内部错误：受控退出 owned mode 并执行一次 terminal cleanup，不能在未知屏幕上继续两个 renderer；
- 输出过大：在 domain 层生成明确 truncation cell/marker，不在 writer 中静默截断导致 block 半提交。

---

## 14. 分阶段实施计划

迁移采用“先建立约束与 adapter，再迁移入口，再切所有权，最后删除旧路径”的顺序。任何阶段都不允许两个 production renderer 对同一 terminal 双写；shadow compare 只能比较内存 frame，不能双提交。

### 14.1 阶段总览

| 阶段 | 目标 | 关键交付物 | 依赖 | 退出条件 |
| --- | --- | --- | --- | --- |
| P0 | 建立旁路写入清单与门禁 | writer inventory、allowlist、CI audit、PTY 复现 | 无 | owned 生命周期所有 direct writer 有 owner/迁移项 |
| P1 | 统一 slash command 输出 | `CommandResult`、command adapter、迁移高风险命令 | P0 | command handler 不直接 stdout/terminal |
| P2 | 清理 runtime/tool diagnostics | `RenderEventWriter`、logger 分流、active-turn writer 迁移 | P0 | active turn 无 raw stdout/stderr |
| P3 | 引入 screen lease | lease manager、fullscreen suspend/resume、异常恢复 | P0 | fullscreen 不再 Disable/Enable surface |
| P4 | 建立 UI actor 与状态入口 | `UIAction` mailbox、Reducer、effect result action、legacy adapter | P0–P3 | 业务 callback 不再直接 mutation + flush |
| P5 | 统一 AppState/semantic cell | transcript/active/bottom state、stable ID/revision、replay adapter | P4 | live/replay/finalize/overlay 由同一 snapshot 驱动 |
| P6 | 统一 boundary/gap | `BoundaryPolicy`、规则表测试、删除 gap helper | P5 | gap 只由 policy 生成 |
| P7 | 完成 reflow/handoff | source-aware layout、handoff record/frontier | P4–P6 | resize/handoff exactly-once |
| P8 | 删除 legacy 与补偿状态机 | 删除旧 renderer、raw adapter、旧 flags | P1–P7 | production 只有一个 owner/presenter |
| P9 | 全量验收与性能收口 | property、VT、PTY/ConPTY、benchmark、runbook | P8 | 满足第 19 节全部验收标准 |

### 14.2 P0：直接输出审计与安全网（进行中）

已完成的安全网：

- `TestChatInteractiveDirectWriterInventory` 对 `commands/chat*.go` 和 `command.go` 做 AST 扫描，覆盖 `fmt.Print*`、`fmt.Fprint*(os.Stdout/os.Stderr)`、直接 `os.Std*.Write*`、`io.WriteString(os.Std*)` 及 `ui.WriteTerminal*`；
- 当前基线为 155 个 file/function/kind 分组、550 个 call site；新增分组或调用次数变化均默认失败，失败信息包含实际行号；
- `/debug display`、`/status`、`/load` 与 `/title`/`/rename` 成功路径已验证不写 raw stdout、各作为一个 atomic command cell 提交，并在 prompt/status/ActiveBand repaint 与 resize recompose 后不重复或丢失；参数错误与加载/同步失败保留 legacy 报错路径（错误 message 需在所有模式下可见）。
- `cmd/aicli/functions/builder.go` 已删除 legacy tool-call parsing 的 `fmt.Print*` / `os.Stderr` diagnostics；解析层静默保留 raw/incomplete call，由上层决定结构化诊断。`TestFunctionCallBuilder_HasNoDirectTerminalWriter` 阻止该库重新取得 terminal sink。

剩余工作项：

- 对 tool/runtime active-turn direct output 建立并发复现；
- 增加 owner/frame debug trace；
- 为每个现有 debt entry 补 owned-safe、plain-only、startup/shutdown 或待迁移分类和 owner；
- 按分类迁移入口并从 inventory 删除，而不是扩充 baseline。

本阶段不大规模重写，仅阻止问题继续增长。

### 14.3 P1：CommandResult 收敛

优先顺序：

1. `/debug`、`/status`、`/load`、`/title`，建立结构化命令样板（`/debug display`、`/status`、`/load`、`/title`/`/rename` 均已迁移；`/load` 确认 cell 采用 `chat_load_document.go`，历史回放经 `CommandResult.ReplayHistory` 在提交后触发）；
2. `/goal`、`/memory`、`/stream`、`/skills` 等文档型输出（前三者已迁移，`/skills` 的 selection/modal 仍待迁移）；
3. `/resume`、`/backtrack`、`/theme` 等带 modal/fullscreen action 的命令；
4. 其余 slash command；
5. 删除 `beginDirectInteractiveOutput` 作为通用 command 生命周期协议，仅保留必要的过渡 facade。

每个 command result 作为明确 block transaction 进入 Scene。测试必须在 command 后触发 prompt/status repaint，不能只捕获 stdout 字符串。

### 14.4 P2：Runtime/tool diagnostics 收敛

- 将 builder/parser warning 改为 logger 或 diagnostic render event；
- 将 tool output mirror 替换为 cell-aware writer；
- 区分用户可见 tool result、运行进度与开发日志；
- 为 streaming fragment 建立 accumulator 和 revision；
- stdout/stderr 指向 TTY 时统一受 Scene ownership 管理。

### 14.5 P3：Fullscreen lease

- 实现 lease state machine 和异常安全 guard；
- 迁移所有 fullscreen selector；
- lease active 时模拟后台 chat/tool/status/resize 事件；
- release 后强制 front invalidation + full repaint；
- 删除 fullscreen 路径中的 surface Disable/Enable。

### 14.6 P4–P6：核心模型切换

- 先建立 `UIAction`/`UIController` 单一入口；现有 coordinator/surface setter 只作为投递 action 的 adapter；
- 将 `FixedBottomSurface` 变为 facade，内部委托 SceneController/Layout/Presenter；
- 为现有 history output 生成稳定 cell identity；
- 将 mutable source 收进 `ActiveCellState`，ActiveBand 只保留派生 projection；
- replay/resume 直接建立 cell sequence；
- 用 `BoundaryPolicy` 替换 `completeBlockOutput`/`gapFor*` 推断；
- 引入 tokenized terminal effect queue，write 成功后以 ack action 推进 projection；
- 设置 feature flag 只选择旧或新 presenter，禁止双写；可 shadow compose 比较 frame。

### 14.7 P7：Resize/Reflow/Handoff

- `DisplayLines(width)` 从 semantic source 派生；
- retained tail 在 resize 时重排；
- 建立 source range handoff record；
- frame 成功后才推进 frontier；
- 覆盖 terminal shrink/grow、wide char、长 tool cell、popup/fullscreen 并发；
- 删除依赖旧 display row suffix 的 soft rewrite。

### 14.8 P8–P9：删除与验收

- 删除 production immediate renderer 和所有无主 terminal write；
- 删除旧 scroll compensation/gap flags；
- 保留 plain/JSON 独立 renderer；
- 完成真实终端矩阵、性能基线和故障注入；
- 移除临时 killswitch 前至少经过一个稳定发布周期；若保留 emergency flag，必须是“整个 session 选择 renderer mode”，不能运行期混用。

---

## 15. 删除与收敛清单

迁移完成后逐项确认，不允许只标记 deprecated 后长期保留可达路径。

### 15.1 输出入口

- [ ] owned interactive command handler 中的 `fmt.Print*`；
- [ ] owned interactive runtime/tool 中的 `os.Stdout`/`os.Stderr` direct write；
- [ ] 业务层 direct terminal cursor/scroll-region/clear-line 操作；
- [ ] 将 `BeginOutput` 当作 raw 输出许可的调用方式；
- [ ] 同一 session 在 retained/plain renderer 之间临时切换。

### 15.2 Gap 与 block 状态

- [ ] `completeBlockOutput` 作为全局 gap truth；
- [ ] `gapFor*` 分散 helper；
- [ ] cell source 首尾人为拼接的空行；
- [ ] replay 特有的 gap 推断；
- [ ] ActiveBand/status 更新对 committed gap state 的修改。

### 15.3 History 与 soft output

- [ ] 以纯文本 suffix 匹配作为 cell ownership；
- [ ] finalize 时 append 第二份 final 内容；
- [ ] replacement 失败即清空全部 owned history；
- [ ] retained history 与 native handoff 各自维护不可关联的行计数；
- [ ] 从 Backend/terminal screen 反推业务历史。

### 15.4 Fullscreen 与生命周期

- [ ] fullscreen 前 `Disable()`、退出后 `Enable()`；
- [ ] alternate screen active 时 primary presenter flush；
- [ ] fullscreen renderer 绕过 screen owner；
- [ ] lease release 后继续使用旧 front buffer 做增量 diff。

### 15.5 旧补偿状态机

在 owned architecture 完全接管后，评估并删除或限制到 plain legacy-only：

- [ ] `scrollCompensatedRows`；
- [ ] `pendingScrollDownRows`；
- [ ] `outputCursorOnBlankRow`；
- [ ] `outputScrollDebtRows`；
- [ ] 与旧 immediate scroll region 绑定的补偿分支；
- [ ] production legacy renderer 可达入口。

### 15.6 Markdown / ActiveBand / 历史消息统一管理现状（2026-08-01 排查结论）

三者**已有统一管理，但统一在「单一引擎 + 单一 writer + 单一 coordinator」，不是「单一 Scene」**；用户观察到的"markdown 单独渲染、与工具事件流覆盖"对应的是仍存在的 raw stdout 旁路与双渲染调用点，而非无管理状态。

已统一的部分：

- **单一 markdown 引擎**：`ui/markdown`（goldmark AST + Chroma → `render.Document`）是唯一内容渲染真源；ActiveBand 直播（`ActiveStreamController.activeDocumentLocked` → `markdown.Render(ActiveBandBodyOptions)`）与历史回放/scrollback（`Formatter.Format` → `AssistantBodyOptions`）共享 `ApplyAssistantBodyContract`（Hyperlinks=false、TableMode=Auto），并有 `TestAssistantBodyEngines_SharedContractParity` 等 parity 测试保证 blank/plain 一致（active_stream.go L458-466、formatter/markdown.go L174-176、assistant_options.go L12-20）。
- **单一 surface 写入入口**：owned 路径下所有内容经 `FixedBottomSurface.WriteOutput` → `appendHistoryWindowLocked` → `renderOwnedViewportLocked`（historyWindow + ActiveBand 一帧合成）；`WriteSoftTrackedOutput` 单独保留 assistant soft 尾巴供 reflow。
- **单一 coordinator 互斥入口**（chat_interaction.go）：assistant 流 `paintActiveStreamLocked`、tool 流 `syncAgentStageActiveBandLocked`、稳定前缀滚动提交 `commitActiveStableScrollbackLocked`（用 `session.Formatter.Format` 的 rows delta，与历史 replay 同构）、band 帧 `publishActiveStreamFrameLocked` → `SetActiveBandStyled`。
- **band 所有权规则已显式化**：`streamingActive || reasoningActive` 时 tool 事件不碰 band（L4931-4932）；assistant 内容总是覆盖 stale tool cell（L3992-3995）；tool 结束 → `Cancel` + `ClearActiveBand`；直播与持久历史共用 `aicliTranscriptRenderer` 与 `Formatter`。

仍存在的分裂点（P4–P9 收敛对象）：

- **双渲染调用点**：live band 用 `markdown.Render(ActiveBandBodyOptions)`（自带 Highlighter、`HideHighlightFallback=true`），scrollback 与历史用 `Formatter.Format`（`AssistantBodyOptions`）——靠 contract + parity 测试对齐，不是同一代码路径；收敛方向是以 `Formatter.FormatDocument` 为结构化真源、band 只做 highlighter/holdback 差异化。
- **raw stdout 旁路**：P0 inventory 155 组/552 个 call site 直接写终端，绕过 surface 所有权——"下一帧覆盖不在 Scene 中的文字 / raw 输出出现在 ActiveBand 中间"的真源即此（§1.1、§1.4）。
- **historyWindow 仍是 `[]string`**，无 cell/row identity；gap 由 `completeBlockOutput`/`gapFor*` 按前一次调用推断（历史模型），未切到 `BoundaryPolicy`（INV-GAP-03 未实现）。

Phase 5 当前已先落地三个 streaming data-plane seam，并完成一个受控 shadow adapter：其一是 `UpdateActiveCellAction`，以 `CellID + ExpectedRevision` 校验 mutable 更新，携带同一 source 的 `Stable/Enqueued/Acked` prefix range，并在 reducer 中拒绝乱序、越界和非 UTF-8 边界；同 CellID 的待处理 mutable update 可 latest-wins 合并，source correction 会清空旧 effect range。其二是 `ActiveStreamController.SourceSnapshot()` 与 `AdvanceActiveSource`、`MarkActiveEnqueued`、`MarkActiveAcked`，只读暴露 semantic source/kind/range，不暴露 rendered rows 或 terminal cache，且明确 tool display 不是 transcript source；queued-but-unacked suffix 在确认 Ack 前不得从 active projection 消失。其三是纯 `ActiveCellStateFromSourceSnapshot`/`UpdateActiveCellActionFromSourceSnapshot` mapping，显式接收 `CellID/Revision/EnqueuedEnd/AckedEnd`，忽略 controller-local `CommittedEnd`，拒绝 overlay display、身份缺失、回退/越界 range 与 UTF-8 半 rune。`chatInteractionCoordinator.RenderAssistantDelta`、reasoning delta/finalize 以及 tool-running stage 的 begin/progress/display/finish 现在在解锁后投递该 action 作为 AppState shadow mirror；reasoning 映射为 `KindSupplement` source，tool display 仅映射为空 source `KindToolChain`，finish 以 `ClearActiveCellAction` 清除 shadow active，并以 mounted Scene cell 为前置条件。assistant 的 `CompleteAssistantResponse`/`FinalizeAssistantDelta` 还会在 reset 前对已 committed 的 immutable Scene snapshot 投递 fenced `FinalizeActiveCellAction`，由 reducer 在一笔 shadow state transition 中替换 final transcript 并清除同一 active；active shadow revision 可与 Scene final revision 相等，只有更旧的 final snapshot 才会被拒绝。它不改变 legacy ActiveBand/surface writer，也不将 Scene revision 伪装成 physical Ack。新增 live/finalize/cancel、短写、source correction、delta/final race、mapping、coordinator unlock、equal-revision finalization、reasoning/tool lifecycle 和 display isolation 测试均已通过。该 adapter 尚未替换 `ActiveStreamController`、`assistantTurnTranscript`、`softEmitted*`、`streamRenderedPrefixLen`、`streamEnqueuedPrefixLen`、`stableCommitQueue` 或 `FixedBottomSurface`，未连接 production `TerminalSession`；因此本节列出的 production 分裂点和 single-writer 门禁仍全部有效。

补充的 legacy tool lifecycle slice 已把带稳定 `tool_call_id` 的 `tool.requested`、`tool.progress`、`tool.completed` 以及 failed/cancelled 变体纳入同一 mutable `KindToolChain`：progress 原地更新 Scene source，终态合并 tool output 并只产生一个 committed chain，重复 final 由 encoder 去重；缺少稳定身份的事件保留 system fallback，绝不猜测调用归属。真实 `chatRuntimeEventBridge -> UIController -> AppState` 集成回归已验证 active cell 的 kind/source、progress 更新、final 清理、合并 source 与无重复历史。该切片只扩大 Scene/AppState data-plane 覆盖，不改变 streaming range owner、legacy presenter 或 production `TerminalSession` 尚未迁移的状态。

随后补充的 snapshot/ledger merge slice 固化了运行时 delta 的因果顺序：`ReplaceTranscriptAction` 对相同 mutable `CellID/Kind/Source/Revision`（以及 actor 中更高 revision 的 active source）保留现有 `Stable/Enqueued/Acked`，只有 Scene source 前进、finalized 或 active cell 被移除时才替换/清除 active ledger。连续 assistant delta 的真实 `chatRuntimeEventBridge -> UIController` 回归证明下一次 Scene snapshot 不会抹掉 shadow range；该 slice 仍是 AppState data-plane 保护，不接 production `TerminalSession`，也不改变 `FixedBottomSurface` 的唯一 writer 角色。

同一切片还修正了 tool-stage shadow adapter 的重入边界：四个会在 runtime reducer 中执行的 stage helper 现在都使用 controller causal follow-up，external mailbox 满时仍能在当前 action 结束后按独立 revision 更新 AppState。`TestToolStageShadowUsesCausalFollowup` 覆盖该背压场景；它只是 reducer/action 顺序修复，不是 presenter/effect queue 接线。

---

## 16. 测试策略与矩阵

### 16.1 测试分层

1. **纯模型单元测试**：cell lifecycle、revision、boundary、queue coalesce；
2. **Layout/Compose golden**：不同 width/height/theme 下的 rows 和 cursor；
3. **Presenter/VT 测试**：执行 ANSI stream 后重建物理屏幕，验证与 frame 一致；
4. **属性/不变量测试**：随机事件序列、resize、update/finalize、popup/fullscreen；
5. **命令集成测试**：command result 与下一帧 repaint；
6. **并发/race 测试**：runtime event、timer、input、lease 同时发生；
7. **PTY/ConPTY 测试**：真实 terminal semantics；
8. **人工验收**：Windows Terminal、ConPTY、常见 ANSI terminal、tmux/zellij 可支持模式；
9. **性能测试**：长 session、resize storm、高频 streaming、超长 tool result。

### 16.2 核心测试矩阵

| 场景 | 必须断言 |
| --- | --- |
| user → assistant streaming → final | 同一 assistant `CellID`；final 后只出现一次 |
| running tool → multiple events → completed | cell 内稠密；finalize 不追加副本 |
| tool final cell → next event cell | 恰好一个 policy gap |
| `/debug display` 后 status tick/prompt repaint | debug block 顺序不变、不覆盖、不消失 |
| command 输出多行时 ActiveBand grow | block 行连续，无永久空洞 |
| popup open/close | transcript sequence、handoff frontier 不变 |
| fullscreen 期间到达 assistant/tool event | alternate screen 不被污染；release 后事件可见 |
| fullscreen cancel/panic | lease 释放；主屏 full repaint；Scene 不丢失 |
| width 120→60→120 | retained source 不变；可见 rows 可逆重算范围内一致 |
| height grow/shrink | 不重复 handoff；prompt/cursor 始终合法 |
| partial terminal write | front invalid；下一次 full repaint |
| 过期 mutable revision | 新 revision 保留；不回滚 |
| 空/filtered event | 不产生 cell 或 gap |
| replay 与 live 同一 transcript | cell/boundary sequence 相同 |
| native handoff 重试 | 同一 range 最多成功一次 |
| stdout/stderr 违规写 | CI/test guard 失败并报告调用点 |

### 16.3 属性测试建议

随机生成事件序列并持续验证：

```text
CellID 唯一
Sequence 严格递增
Revision 单调
Committed cell 不再 mutation
GapRows ∈ {0,1}
No orphan boundary
Handoff frontier 单调
No handed-off range re-appended
At most one active screen lease
Front valid => VT(screen) == presenter front
Cursor within terminal bounds
```

可以针对每个随机序列在任意位置插入 resize、popup、status update、fullscreen acquire/release 和 write failure，以发现手工案例难覆盖的组合。

### 16.4 真实终端验收

至少覆盖：

- Windows ConPTY / Windows Terminal；
- 非 Windows ANSI terminal；
- terminal height 很小、width 很窄；
- CJK、emoji、combining character；
- 快速连续 resize；
- 长时间 streaming；
- shell-like tool 大量输出；
- fullscreen 进入/退出和 Ctrl+C；
- terminal capability 不满足时的 plain-mode 选择。

录制时应使用 marker/cell ID 校验，不仅依赖截图肉眼观察。

---

## 17. 可观测性与诊断

### 17.1 建议字段

开发/debug 模式记录结构化、无敏感正文的元数据：

```text
renderer_mode
scene_revision / event_sequence
frame_generation / resize_generation
typed_event / transaction_id
cell_id / cell_kind / cell_revision / cell_phase
transcript_cell_count / retained_cell_count
boundary_key / boundary_gap_rows
active_band_revision / status_revision / prompt_revision
screen_owner / lease_id / lease_state
front_valid / full_repaint_reason
handoff_token / handoff_frontier / handed_off_rows
queue_depth / coalesced_updates / dropped_stale_revisions
terminal_width / terminal_height
flush_bytes / diff_cells / frame_latency
```

不得默认记录完整用户对话、tool secret、环境变量或未经脱敏的 command output。测试可使用 synthetic marker 和 CellID。

### 17.2 关键计数器

- `tui_unowned_terminal_write_total`：目标长期为 0；
- `tui_full_repaint_total{reason}`；
- `tui_stale_cell_update_total`；
- `tui_invariant_violation_total{type}`；
- `tui_handoff_retry_total`；
- `tui_handoff_duplicate_prevented_total`；
- `tui_scene_queue_depth`；
- `tui_frame_latency_ms`；
- `tui_frame_diff_cells`；
- `tui_screen_lease_active`。

### 17.3 Debug snapshot

`/debug display` 最终应读取 Scene/Presenter 的只读 snapshot，并以普通 `CommandResult` 显示，例如：

- renderer mode；
- current owner/lease；
- Scene/frame/resize generation；
- transcript cell 数量及 phase 汇总；
- retained/handoff frontier；
- active band/status/prompt/popup 摘要；
- front buffer 是否有效及上次 invalidation 原因。

读取 debug 状态不能修改 Scene，也不能通过 raw stdout 输出。

---

## 18. 风险与回退策略

### 18.1 主要风险

| 风险 | 影响 | 缓解 |
| --- | --- | --- |
| Scene 模型与现有 event 语义不一致 | 丢事件或错误合并 | 先 adapter/shadow model，建立 sequence/revision 测试 |
| handoff exactly-once 实现错误 | 原生 scrollback 重复/漏行 | token/record、成功后推进 frontier、故障注入 |
| 大量 semantic history 占用内存 | 长 session 内存增长 | retained source cap、持久层 replay、不可变旧 range 释放 |
| full repaint 过多 | 闪烁和性能下降 | 只在 invalid/resize/lease release 使用，正常路径 diff |
| queue backpressure | UI 延迟或 runtime 阻塞 | durable/coalescable 分类、指标与限流 |
| Windows/Unix ANSI 差异 | 某平台 cursor/scroll 异常 | VT + ConPTY + PTY 双层测试 |
| 迁移期新旧逻辑交叉 | 更难定位的双写 | session-level feature flag，只选一个 presenter |
| fullscreen 异常退出 | terminal mode 残留 | 幂等 lease guard、best-effort reset、signal cleanup |

### 18.2 回退原则

- 每个阶段保留小粒度 feature flag；
- flag 必须在 session 创建时选择完整 renderer pipeline；
- 不允许在同一 session 中“新 presenter 出错后直接继续 raw 输出”；
- 允许内存 shadow compose 比较新旧 frame，但只能有一个 renderer 写 terminal；
- handoff/front buffer 异常时，优先 invalidation + Scene full repaint；
- 若某阶段必须回退，回退该阶段的所有权切换，不恢复已经证明错误的混合双写；
- emergency plain-mode 回退要执行明确 teardown，并提示功能降级，不能假装 retained history 可继续。

### 18.3 发布节奏

建议：

1. 测试/开发环境默认启用；
2. opt-in canary，收集无正文指标；
3. interactive owned-capable terminal 默认启用，保留 session-level killswitch；
4. 一轮稳定发布后删除旧 production path；
5. 再一轮稳定发布后删除迁移 adapter 和过期 feature flag。

---

## 19. 最终验收标准

### 19.1 架构验收

- [ ] `AppState` 是 transcript、active、bottom、geometry 与 lease 的唯一权威来源；
- [ ] SceneController 串行提交 mutation，frame 使用不可变 snapshot；
- [ ] owned interactive 模式只有一个 terminal owner；
- [ ] presenter 之外没有可达的业务 terminal primitive；
- [ ] command/runtime/tool 输出都通过结构化 event/result；
- [ ] fullscreen 通过 screen lease，不销毁主 Scene；
- [ ] plain/JSON renderer 与 owned renderer 生命周期完全分离。

### 19.2 History 和 gap 验收

- [ ] 所有 top-level cell 有稳定 ID/sequence/revision/phase；
- [ ] mutable finalization 不 append 副本；
- [ ] replay/live 使用同一 cell/boundary pipeline；
- [ ] gap 只有一个权威 policy；
- [ ] cell 内部稠密，独立 cell 之间符合规则表且最多一个 gap；
- [ ] ActiveBand/status/prompt/popup 不影响 transcript gap；
- [ ] handoff frontier 单调且 exactly-once。

### 19.3 渲染和生命周期验收

- [ ] command block 在任意下一帧后位置和内容稳定；
- [ ] resize/reflow 不重复或丢失 retained cell；
- [ ] partial write 后能 full repaint 恢复；
- [ ] fullscreen 期间事件不丢失、不污染 alternate screen；
- [ ] release 后从最新 Scene full repaint；
- [ ] cursor、scroll region 和 front buffer 强不变量通过；
- [ ] 旧 compensation/gap/legacy production path 已删除或仅限明确 plain-only 代码。

### 19.4 质量门禁

- [ ] model/layout/presenter/unit/property 测试通过；
- [ ] race 测试通过；
- [ ] commands/ui 全量 Go tests 通过；
- [ ] Windows ConPTY 与非 Windows PTY 验收通过；
- [ ] 长 session、长 tool output、resize storm 性能达到基线；
- [ ] 无正文 telemetry 中 `unowned_terminal_write_total == 0`；
- [ ] 文档、runbook、故障回退流程同步更新。

---

## 20. 架构决策记录

### ADR-01：保留多个逻辑 layer，但只允许一个物理 writer

**决定**：history、ActiveBand、status、prompt、popup 继续分层；统一通过 compositor/presenter 提交。

**原因**：职责分层有利于状态管理；冲突来自它们或业务组件直接竞争 terminal，而不是“层”本身。

### ADR-02：Scene 是真相源，terminal 不是

**决定**：所有恢复、reflow、fullscreen return 均从 Scene 生成。

**原因**：terminal scrollback 和 physical cursor 不可可靠查询，也无法表达 cell identity/revision。

### ADR-03：Gap 是 boundary policy，不是输出字符串

**决定**：gap 由相邻 semantic cells 纯函数生成。

**原因**：可统一 live/replay/resize/handoff，并消除调用顺序布尔状态。

### ADR-04：Finalization 是 replace/commit transaction

**决定**：mutable 与 final 共享 CellID，final 不创建第二份 history cell。

**原因**：从数据模型上消除重复绘制，而不是用文本比对补救。

### ADR-05：Fullscreen 使用 lease，不使用 Disable/Enable

**决定**：alternate screen 临时独占 terminal；主 Scene 暂停 flush 但继续存在。

**原因**：Disable 是 destructive teardown，会清空 retained state。

### ADR-06：不长期双写两个 production renderer

**决定**：feature flag 在 session 层选择 renderer；shadow compare 只比较内存结果。

**原因**：双写本身就是当前问题根因，无法作为稳定迁移方案。

### ADR-07：Native scrollback 是不可变副本

**决定**：semantic history 与 retained tail 保持权威，handoff 后物理行不再 reflow。

**原因**：兼顾原生 scrollback 用户体验、可重绘 viewport 和有限内存。

---

## 21. 建议的首个实施切片

为了以最小风险验证本文架构，建议第一个实现切片只完成以下闭环：

1. 定义最小 `CommandResult{Blocks}` 和 command-to-scene adapter；
2. 将 `/debug display` 从 raw stdout 迁移为一个原子 command cell；
3. 为 cell 分配稳定 `CellID/Sequence`；**已落地**：`backend/cmd/aicli/ui/scene/scene.go` 的 `TuiScene` 维护 `nextID`/`nextSeq`，`AppendCell` 在 ID 为空时分配全局唯一 `CellID`（INV-SCENE-02）、top-level cell 分配单调 `Sequence`（§5.3，tool-chain 成员不推进）；`ApplyCellMutation` 按 INV-SCENE-02/03/04 校验（重复 ID/旧 revision/不可变 cell 拒绝）；`Snapshot()` 返回不可变副本供 Layout/Compose 派生；
4. 使用最小 `BoundaryPolicy` 计算与前一 cell 的 0/1 gap；**已落地**：`backend/cmd/aicli/ui/boundary/boundary.go` 提供 `ResolveGap(prev, next CellMeta) GapRows`（GapRows 只允许 0/1，含 `BoundaryClass` 枚举），测试覆盖 §7.3 规则表全部行（首 cell 0、user/assistant 互转 1、top-level→command/system 1、同 tool-chain 稠密 0、独立 final cell 1、同 ID mutable update/finalize 不新增边界、replay 与 live 相同、handoff 保持原 boundary）及 INV-GAP-02（穷举组合 gap 恒 ≤1）与 INV-GAP-05（过滤事件不产生前导 gap）；`CellMeta` 是 `TranscriptCell` 的最小投影，Scene 终局落地后由 `TranscriptCell` 派生，无需改 policy；
5. 测试执行 `/debug display` 后依次触发 status update、prompt repaint、ActiveBand grow/shrink 和 resize；
6. 断言 debug cell 只出现一次、顺序不变、gap 符合 policy、Backend/VT 最终 frame 一致；
7. 增加 command handler direct stdout 禁止测试，防止新增旁路。

该切片不要求先完成全部 Scene 拆分，但其 API 和测试必须符合最终模型，避免再次形成只能服务 `/debug` 的临时方案。

随后第二个切片应迁移 `functions/builder.go` 等 active-turn diagnostics，第三个切片引入 fullscreen lease。完成这三个切片后，再进入 Scene/Layout/Presenter 的核心拆分，可显著降低重构期间双写和状态丢失风险。

**切片 4 注记（BoundaryPolicy）**：已落地 `backend/cmd/aicli/ui/boundary`（纯函数包）。`ResolveGap(prev, next CellMeta)` 按 §7.3 规则表输出 0/1 gap，测试固化规则表全行（首 cell、同 ID replace/commit、同 ChainKey 稠密、其余独立 top-level 一个 gap）与 INV-GAP-02/05 不变量。渲染层切换时直接消费，不再由调用点推断空行。

**切片 5 注记（Scene 数据层 + ChangeSet 映射）**：已落地 `backend/cmd/aicli/ui/scene`。数据层（`TuiScene`/`TranscriptCell`/`SceneTransaction`/`SceneController.Submit`/`LayoutTranscript`）见上文切片 3 注记；映射器 `from_changeset.go` 的 `ChangeSetMapper` 消费 `encoding.ChangeSet`：`ItemChange.Op` → `AppendCell/UpdateCell/FinalizeCell/RemoveMutableCell`，CellID 由 `Item.ID`（"item-{n}"）解析（重放身份稳定，INV-SCENE-02），cell Revision 由映射器统一递增（tool 输出合并与 tool_call 自身更新共享同一 cell，INV-SCENE-03 单调），tool_output 按 CauseID 合并进链首（§7.3 稠密）且孤儿输出独立成块并计数，tool_call Head 演进时拆分替换保留已合并输出。测试覆盖映射表全行、合并/孤儿/移除约束（committed remove 显式失败）、revision 单调（同批影子状态）、重放确定性与事务原子性（INV-FRAME-01）。

**切片 6 注记（ChangeSet 消费端：bridge 接入）**：已落地 `backend/cmd/aicli/commands/chat_runtime_events.go`。`chatRuntimeEventBridge` 增加 P3 消费端：`renderScene *scene.TuiScene` + `renderMapper *scene.ChangeSetMapper`（`sceneMu` RWMutex 保护，`sceneApplyFailures`/`sceneLastError` 入诊断）。事件接入点 `applyChangeSet` 在编码器产出 ChangeSet 后即时 `ChangeSetMapper.Apply` 映射提交进 Scene（含失败计数与错误记录）；`appendEventLog` 维持 append-only 事件日志，`replayEventLog` 在会话重启/日志重放时重建 `renderScene`/`renderMapper` 并清零诊断（幂等，与实时路径等价）；`/debug` 文档新增 "Unified Render Scene:" 审计段（Cells/Revision/Apply Failures/Last Error，`sceneSnapshot` 与 `renderModelSnapshot` 对照：CellID 应等于 `Item.ID` 数字部分、顺序应等于模型数组顺序）。测试（`chat_runtime_events_scene_test.go`）覆盖模型顺序跟随、日志重放重建 Scene、映射失败计数、乱序增量一致性（双跑模型逐项一致），`-race` 通过。

**切片 7 注记（渲染层切换可行性：旧路径 gap 状态机 ↔ LayoutTranscript 双跑等价）**：已落地 `backend/cmd/aicli/commands/chat_runtime_events_gap_parity_test.go` 的 `TestRenderLayer_GapParity_LegacyCoordinatorVsLayoutTranscript`。测试以同一会话序列（user → assistant → supplement → user → assistant → assistant）分别驱动两条真实路径：旧路径用真实 `chatInteractionCoordinator` 公开方法（`RenderSubmittedUserInput`/`RenderAssistant`/`RenderAsyncLine`，块间插入 `writePromptGapLocked`——`PrintPrompt` 在用户输入前消费上一块残留 gap 的真实实现），从输出文本提取空行（gap）序列；新路径用 `ChangeSetMapper.Apply` + `LayoutTranscript`（gap 决策委托 `boundary.ResolveGap` 规则表）提取 gap 行序列。断言两侧内容行数与 gap 位置逐项一致（6 内容行、5 gap 于 [1 3 5 7 9]）。该等价性证明：渲染层切换（删除 `completeBlockOutput` 状态机）后交互主路径的块间空行分布保持不变。`/debug` "Unified Render Scene:" 审计段同步增加 `Layout Rows`/`Layout Gaps` 摘要（`sceneSnapshot` → `LayoutTranscript` 直接计算），供双跑对比审计。

**切片 8 注记（渲染层文本投影 + 完整文本等价）**：已落地 `backend/cmd/aicli/ui/scene/render.go` 的 `RenderText(cells, policyVersion) []string`：把 `LayoutTranscript` 行序列投影为最终文本行（gap 行投影为空字符串，内容行为 source 原文行；无 ANSI/样式/宽度换行——样式与 width-aware `DisplayLines` 属于 presenter 层，本函数只承诺行结构与语义内容与 Layout 一致）。纯函数，replay/live 复用同一投影；`render_test.go` 覆盖空 Scene、首 cell 无 gap、top-level gap 行、内部空行保留（§7.2）、tool 链稠密、与 `LayoutTranscript` 行结构与数量完全一致（gap 行数 == boundary 行数）。commands 层 `chat_runtime_events_render_text_parity_test.go` 新增两个测试：`TestRenderLayer_TextParity_EventStreamVsLegacyCoordinator` 用真实事件流（`EventLLMRequestStarted`/`EventAssistantDelta`/`EventAssistantMessage`/`EventLLMRequestFinished`）驱动真实 bridge 事件接入点（`encodeRenderModelEvent` → EventEncoder → ChangeSet → Scene），其 `RenderText` 输出与旧路径 coordinator 输出**逐行一致**（含内容行文本与 gap 空行，3 行 `["你好" "" "世界"]`）——比切片 7 的 gap 位置等价更强，证明渲染层切换后最终文本不变；`TestRenderLayer_TextParity_ToolChainRenderText` 用真实事件流（system + assistant 流式 + tool 链）断言 `RenderText` 的 gap 结构符合 §7.3 规则表（独立 top-level 之间 1 gap、tool 链内稠密）。`/debug` "Unified Render Scene:" 审计段增加 `Layout Text Rows`（`RenderText` 行数，含 gap 空行），供双跑人工对照旧路径实际输出行数。

**切片 9 注记（渲染层运行时双跑文本对照：`RenderText` 接入真实写入路径）**：已落地 `backend/cmd/aicli/commands/chat_runtime_events_text_parity_test.go` 与 coordinator/bridge 探针。`chatInteractionCoordinator` 的完整块提交点 `writeRowsLocked`（所有原子完整块路径——assistant 一次成型、one-shot completion、用户输入、command cell、tool 输出——的唯一汇聚点）在块写出后把"本块实际行序列（含跨块 gap 空行）"交给可选探针 `textParityFn`（nil 时零行为变化，默认旁路）；`SetTextParityProbe` 供注入/清除。bridge 侧实现 `checkTextParity`：取 Scene 快照 → `RenderText`，按已消费行数把本块序列与对应片段逐行对照——越界（Scene 落后/旧路径超前）或逐行不等记 `textParityMissed` + `textParityLastErr`（块号/行号/两侧文本），全等则消费该段 `textParityMatched++`。真实运行时接线双保险：`newChatInteractionCoordinator` 构造时 bridge 已存在则直接注入，`ensureChatRuntimeEventBridge` 创建 bridge 后对已存在 coordinator 补注入（覆盖两种启动顺序）。`/debug` "Unified Render Scene:" 审计段新增 `Text Parity Blocks/Matched/Missed/Last Error`（Missed>0 时给出首个不一致详情，供切换前排查）。测试 3 个：`TestRenderLayer_TextParity_RealWritePathBlocks` 用真实事件流（2 turn）驱动 bridge + coordinator 自动接线，两个 `RenderAssistant` 完整块经真实写入点全部 matched（blocks=2/matched=2/missed=0），并断言 `/debug` 审计段展示统计；`TestRenderLayer_TextParity_DetectsDivergence` 只编码 1 turn（Scene 1 cell）却提交 2 块，第 2 块越界被记为 missed 且 Last Error 指向块 2（证明探针真实对照而非空转）；`TestRenderLayer_TextParity_NilProbeSafe` 无 bridge 时旧路径完整块行为不变（3 行 `["你好" "" "世界"]`）。该探针把切片 7/8 的"测试内手工驱动等价"推进为"真实写入路径逐块自动对照"，是渲染层切换（删除 `gapBeforeBlockLocked`/`completeBlockOutput` 状态机）前的运行时审计基线。

**切片 10 注记（用户输入数据面通道：真实 Scene cell 序列覆盖全部 top-level 块）**：切片 9 探针审计暴露切换前的数据面缺口——runtime 事件流**没有用户输入事件类型**（`internal/chat/events.go` 全部 33 个事件类型无 user 消息；编码器 `classify` 无 KindUser 映射），因此真实 Scene 永远没有 user cell，"完整块序列 == Scene cell 序列"对用户块不成立，删除 `completeBlockOutput` 状态机后用户消息前的 gap 决策将失去数据面来源。本切片闭合该缺口（用户输入是会话 transcript 内容，不属于 P4 交互输出锚点范畴——`/debug`、`/model` 等交互输出仍只以编码器 Tail 为锚点，不进入因果链）：① 编码器 `EventEncoder.SubmitUserInput(text)` 把用户输入提交为 KindUser 终态块（`StatusCompleted`，append 即终态提交 INV-SCENE-04；时钟/统计/Tail 更新与 `Encode` 对齐，与切片 7 parity 基线的手工 KindUser item 一致）；② bridge `submitUserInput(text)` 在 coordinator 用户块提交前直连注入——`renderMu` 与事件循环串行化（编码器非线程安全，事件循环 goroutine 与 coordinator 渲染 goroutine 并发访问；`encodeRenderModelEvent` 同步加锁）、`applyChangeSet` 同一提交路径、并落事件日志（`{"user_input":...}` 记录行，`runtimeevents.Event.Type` 恒非空，replay 以此区分事件行与用户输入行，保持同一全序）；③ `replayEventLog` 支持混合记录重放（事件 + 用户输入逐条按序恢复，重放后 Scene 与实时路径等价）；④ coordinator `renderUserEchoLocked` 增加 `injectUserInput` 参数——live 提交（`RenderSubmittedUserInput`）注入，历史回放（`RenderReplayedUserInput`）不注入（回放由 replayEventLog 恢复，重复注入会产生重复 cell）；⑤ 探针 `checkTextParity` 重构为**按 cell 逐块对照**（`LayoutTranscript` gap 行归属后继 cell，每完整块对应一个已完成 cell；user cell 剥离样式前缀 `"> "`——RenderText 只投影语义内容，样式属于 presenter 层——并忽略前导 gap 行，因为旧路径 user 块的前导 gap 由 prompt 重绘 `writePromptGapLocked` 输出、不在块行内；`textParityConsumed` 行游标改为 `textParityCell` cell 游标）。测试 3 个（`chat_runtime_events_user_input_test.go`）：`TestRenderLayer_TextParity_LiveUserInputBlocks` 用真实交错序列（U1 → turn-1 事件流 → U2 → turn-2 事件流，用户输入经 coordinator live 提交点注入）断言探针 4/4 matched（2 user + 2 assistant）、Scene `RenderText` 全量与旧路径输出一致（含 gap 空行，user 行样式归一化）、`LayoutTranscript` gap 位置与旧路径空行逐项一致；`TestRenderLayer_Scene_ReplayRestoresUserInput` 断言 11 条混合记录（9 事件 + 2 用户输入）日志经新 bridge 重放后 Scene 与实时路径等价（cell kinds/行数/逐行文本）；`TestRenderLayer_UserInput_ReplayPathDoesNotInject` 固化边界——回放路径不注入（cell 数不变、输出不变），live 路径注入（cell +1、`RenderText` 含用户文本）。`-race` 通过。至此真实数据面 cell 序列 == coordinator 完整块序列对**全部 top-level 块**（含用户输入）成立，渲染层切换的等价基线完整。

**切片 11 注记（coordinator gap 决策状态机切换：删除 `completeBlockOutput` 全局布尔，统一 `boundary.ResolveGap` 规则表决策，INV-GAP-03 落地）**：切片 7–10 完成"旧路径 == 新路径"等价基线与运行时对照后，本切片把旧路径的 gap 推断状态机本身替换为与 Scene 侧相同的规则表决策，为 presenter/渲染层切换（P3 剩余）清掉最后一个旧状态机。① 状态：删除 `completeBlockOutput bool` 字段，新增 `lastBlockMeta boundary.CellMeta`（前一完整块的 boundary 元数据；ID 空 = 尚无完整块）、`gapPreWritten bool`（prompt 重绘是否已把下一块的语义 gap 提前写出）、`streamCellID string`（当前 assistant 流的 boundary 身份，同流内残差 chunk 共享同一 ID → ResolveGap 同 ID 稠密）、`supplementBlockSeq`/`errorBlockSeq uint64`（无 historyCell 的完整块——end-reasoning divider、error 块——的稳定 ID 分配器）。② 决策：删除 `gapForTopLevelMessage`/`gapForEventBlock`/`gapIfPriorComplete` 三个旧 helper，统一新 `gapBeforeBlockLocked(next boundary.CellMeta) blockGap`——`gapPreWritten` 时返回 gapNone（已提前消费），否则 `boundary.ResolveGap(lastBlockMeta, next) == GapOne` → gapBlank；首块（lastBlockMeta.ID 空）恒 gapNone。③ 写入入口：`writeRowsLocked`/`writeCompleteBlockLocked`/`commitHistoryCellLocked` 增加 `meta boundary.CellMeta` 参数，块提交后 `markBlockCommittedLocked(meta)` 推进 `lastBlockMeta` 并清 `gapPreWritten`（下一个块的前导 gap 是全新决策）。④ 元数据来源：historyCell 提交用 `cellBoundaryMeta(cell)`（`cellIdentity` 稳定 ID + kind 映射 User/Assistant/Tool/Command/System + ChainKey 从 CauseID 投影，tool 链同链稠密保持）；assistant 一次成型/one-shot completion/流式残差/首 paint/稳定提交用 `streamBoundaryMetaLocked()`（首次分配，同流同 ID）；reasoning supplement（含 end-reasoning divider）用 `nextSupplementMetaLocked()`；error 块用 `nextErrorMetaLocked()`；用户消息块显式 gapNone + `cellBoundaryMeta`。⑤ 生命周期：`writePromptGapLocked` 重写为显式消费模型——`lastBlockMeta.ID != "" && !gapPreWritten` 时写空行并置 `gapPreWritten=true`（旧 `completeBlockOutput` 消费语义显式化，INV-GAP-05：ActiveBand/status/prompt/popup 与 filtered/empty event 不触碰状态）；**流式打断补偿**——`writeIndentedStreamingDeltaLocked`/`renderBufferedAssistantStreamLocked` 增量写出后置 `gapPreWritten=true`（旧 `completeBlockOutput=false` 的等价显式化：增量行或随后的终止空行已提供块间视觉分隔，经 `beginMessageLocked` 打断插入的独立块如 tool_result 不再重复写 gap；若缺此补偿，打断块会多出一个 gapBlank，在 mid-line 终止场景形成双空行）；正常 finalize 的 `markBlockCommittedLocked` 清除标记后恢复规则表决策；`ResetRunState`/`Shutdown` 调 `resetBlockBoundaryLocked()`（清 lastBlockMeta/gapPreWritten/streamCellID）；`resetStreamLocked` 清 `streamCellID`——**跨 turn 的 assistant 流分配不同 ID**，两个连续流的首块之间按 `ResolveGap`（assistant→assistant = GapOne）正常出 gap（修复：若不重置，turn 间共享同 ID 会误判稠密吞掉 turn 分隔）。⑥ 测试：`chat_interaction_gap_policy_test.go` 更新为规则表真值表 + 预写 gap 消费 + supplement 间距 + nil 安全，新增 `TestGapPolicyStreamIDLifecycle`（流内同 ID 稠密、resetStream/resetBlockBoundary 后分配新 ID、reset 后首块无前导 gap）；`chat_interaction_newline_ownership_test.go` 全部块调用点迁移到新签名；既有 RenderLayer/bridge parity 测试（切片 7/8/9/10）不改语义全部通过；`-race` 通过。至此旧路径 gap 决策与 Scene `LayoutTranscript` 使用**同一规则表**，删除旧状态机的切换前准备完成。

---

## 22. 文档维护规则

每次开始或完成一个阶段时，必须同步：

1. 文档顶部状态和更新时间；
2. 第 14 节阶段表的状态、依赖和退出证据；
3. 第 15 节删除清单；
4. 第 16 节新增或反转的回归测试；
5. 第 19 节验收项；
6. 若改变不变量或模型，新增/更新 ADR，并说明替代关系；
7. 证据至少包含测试名、命令结果、VT/golden、PTY/ConPTY 记录之一；
8. 不以“代码已合并”作为完成标准，必须证明对应所有权和不变量成立。
