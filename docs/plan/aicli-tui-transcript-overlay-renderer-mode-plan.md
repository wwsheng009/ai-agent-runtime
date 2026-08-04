# aicli TUI transcript overlay、渲染模式与历史交接实施方案

状态：**in progress migration sub-plan（受 unified architecture 约束；不得独立启用第二个 terminal writer）**

日期：2026-08-04

上位规范：[aicli-tui-unified-render-architecture-refactor-plan.md](./aicli-tui-unified-render-architecture-refactor-plan.md)。若本文与上位规范、`AppState -> Layout/Compose -> TuiPresenter` 终局或其不变量冲突，以上位规范为准。

关联文档：

- owned 渲染状态/effect 收敛：[aicli-tui-owned-render-simplification-plan.md](./aicli-tui-owned-render-simplification-plan.md)；
- 施工顺序和门禁：[aicli-tui-owned-render-simplification-implementation-guide.md](./aicli-tui-owned-render-simplification-implementation-guide.md)；
- transcript/Scene 数据面：[aicli-tui-render-data-plane-codex-migration-plan.md](./aicli-tui-render-data-plane-codex-migration-plan.md)；
- 内容 `Document -> Block -> Line -> Span` 渲染：[aicli-ui-ux-rendering-codex-reference-plan.md](./aicli-ui-ux-rendering-codex-reference-plan.md)。

---

## 0. 结论

应当把“定稿历史的顺序交接”“主界面的可变内容”“完整 transcript 浏览”和 plain/JSON 输出分离；但不能把普通交互聊天重新设计成业务代码直接 stdout，只在 `Ctrl+T` 时才启用 owned viewport。

正确的分层是：

1. 已定稿的 transcript cell 以结构化 `HistoryCommit` effect 交接到 native scrollback；物理上可表现为顺序追加，但只有 Presenter 可以写终端。
2. assistant/reasoning/tool 的 mutable active cell 留在主界面 owned viewport；`ActiveBand` 是由 active source 派生的布局投影，不保存另一份可独立推进的正文。
3. `Ctrl+T` 打开 alternate-screen transcript pager，读取完整 semantic transcript，并在末尾附加 render-only live tail；主屏 Presenter 在 lease 期间暂停 flush。
4. JSON、pipe、noninteractive 和 chat TUI 生命周期外的 CLI 命令使用独立 Plain/JSON renderer，才允许线性写 writer。

因此，分离对象是**语义生命周期与物理 presentation mode**，不是“普通消息”与“特定事件”的业务类型。主屏和 pager 可共享同一个 immutable AppState snapshot 与 render IR，但任何时刻只能有一个 ScreenOwner 向同一 TTY 写 ANSI/光标/scroll-region 字节。

本文新增 `Ctrl+T` transcript pager 的产品/交互目标和实施顺序，不改变已有 `BoundaryPolicy`、`HistoryEffectQueue`、ScreenLease、Scene 或 Presenter 的终局职责。

### 0.1 当前实施切片（2026-08-04）

- 已完成：`TranscriptPagerModel/State` 从 `TranscriptState + ActiveCellState` 派生；mutable cell 只作为 render-only live tail，pager 不读取 `historyWindow`、`ScreenModel` 或 native scrollback。
- 已完成：pager 以 `CellID + cell-local visual row + layout generation` 维护锚点；append 时 bottom-follow 保持跟随，检查历史时保留锚点，replace/remove/resize 安全重定位。
- 已完成：`Ctrl+T` 仅在 owned interactive chat、无 popup、无现有 lease 时打开 `ScreenLease` 管理的 alternate pager；关闭后由 lease 对 primary 做完整 retained-state repaint，composer 草稿不丢失。
- 已完成：UI actor 增加 overlay/scroll/finalize 的 typed action 与 reducer 状态，lease release 会清理对应 overlay；stale finalization 不得清除较新的 active cell。
- 已完成：`HistoryCommit` reducer data plane 为 finalized cell 的 physical display range 分配 token；resize 只 rebase Pending payload，lease 冻结 Begin，Ack/Fail/Deferred 通过 typed barrier 回投，in-flight replacement/resize 与 writer failure 均转为显式 recovery 状态。
- 已完成：`HistoryCommitWakeEffect` 与 `HistoryCommitExecutor` 形成可注入的单一 effect-consumer seam。它按 token 顺序 claim 后才调用 terminal sink；`Deferred` 必须证明尚未写任何字节，短写/错误不会盲目重试，未解决的早期 terminal delivery 会阻止后续 range 越过它。
- 已完成：`ComposeAppRenderFrame` 从同一 `AppState` 保留 viewport-sized structured `render.Line`：transcript cell kind、ActiveBand 的 typed line 与 status document 不再在 frame seam 降为无样式字符串；每一 rich row 与原 `AppScreenRow.Text` 做 plain-text parity。eligible finalized `HistoryCommit` 复用相同 transcript role-tagged line，不再把 handoff 降成无样式字符串。未安装的 `TerminalSession` 已消费这份 rich frame（显式 `ThemeContext`，无全局终端 probe）并实现 `HistoryCommitSink`/`FlushTransaction` 前置契约；它也用同一 theme 解析 history line。它只接受已确认、同 generation、非 lease 的 primary projection，将已有 `render.Line` 经共同 `ANSIBackend` 编码为 cursor-neutral `HandoffPlan`；已 claim 的 history 可在同一次 Presenter write 中严格先于 viewport diff 和 cursor，完整写入后才确认 front 并镜像 scroll-region append。短写、错误或 writer panic 均保守地使 projection Unknown，no-write 条件只返回 Deferred。
- 已完成（shadow fallback）：`ActiveCellState` 携带 Scene kind；`ProjectActiveCellBand` 从 mutable source 的未 Ack prefix 后缀生成 bounded role-tagged lines。`LayoutAppState` 只在没有 legacy facade band 时使用这份 source-backed projection，legacy band 存在时保持它为唯一显示来源。该选择规则防止 active source 与 legacy projection 在同一帧重复，同时为最终 primary presenter 提供无 legacy 输入时的纯布局路径；它不接生产 terminal、也不替代尚未收敛的 active-stream range owner。
- 未完成：生产 `TerminalSession/TuiPresenter` 尚未接管该 sink；`FixedBottomSurface.historyWindow` 仍是唯一 legacy production handoff writer。因此当前代码**没有**让新 executor 与旧 handoff 同时写 terminal；切换必须以整条 primary presenter transaction 替换旧路径。

---

## 1. 问题与边界

### 1.1 当前混合态

当前 interactive 路径同时存在 coordinator 的稳定 prefix/stream cursor、`ActiveStreamController`、`assistantTurnTranscript`、`FixedBottomSurface.historyWindow`/`handoffFrontier`、`ScreenModel` front/back 和 native scrollback。这些对象各自可能判断同一内容“尚未输出”或“已经输出”。terminal write lock 只能避免字节交错，不能使多个状态机对历史、cursor 和 frame 覆盖关系达成一致。

已定稿块的 `writeRowsLocked` 虽已要求一次多行写入，仍只是 legacy adapter 的局部原子性；它不是 RendererMode 切换，也不是 HistoryCommit Ack。`historyWindow` 还是受容量约束的显示缓存，不能成为完整 transcript、会话恢复或 pager 的权威来源。

### 1.2 本方案解决的问题

- owned interactive 生命周期中，禁止业务 handler 旁路 Presenter 直接写 stdout/stderr；
- 历史交接与 ActiveBand/prompt/status/popup 的几何变化解耦；
- 主界面展示可变 tail，同时使完整历史可独立滚动浏览；
- alternate screen 不与 primary frame 同时写同一物理屏幕；
- resize、replay、overlay close 都从 semantic source 重建，而不从 `ScreenModel`、VT snapshot 或 native scrollback 反推；
- `Ctrl+T` 打开时不会遗漏正在运行的工具或流式回复，也不会把该内容再次作为 committed history 插入。

### 1.3 明确非目标

- 不将 `historyWindow` 扩容为完整 transcript；
- 不从 native scrollback 读取历史来构建 pager；
- 不在已有 primary renderer 外增加一个直接调用 `fmt.Print*` 的 transcript viewer；
- 不让 ActiveBand 成为 `Ctrl+T` pager 的第二个底部面板；
- 不以 `FixedBottomSurface.Disable()/Enable()` 实现临时全屏切换；
- 不用“同一文本出现次数”证明 exactly-once；repaint 可以合法重发字节，history handoff 必须以 token/range 证明。

---

## 2. Codex 参考与可迁移规则

参考项目：`E:\projects\ai\codex\codex-rs\tui\src`。

| Codex 结构 | 行为 | aicli 采用方式 |
| --- | --- | --- |
| `ChatWidget.transcript_cells` | 保存 committed `HistoryCell` | `TranscriptState`/`TuiScene` 保存稳定 cell、ID、revision 与 source |
| `ChatWidget.active_cell` | 保存可原地更新的 active tail | `ActiveCellState` 保存 mutable source、phase、stable/enqueued/acked range |
| `App::insert_history_cell` + `Tui::pending_history_lines` | finalized cell 先入 semantic history，再在同步 draw 前写入 scrollback | reducer 产生 tokenized `HistoryCommit`，Presenter 在 frame transaction 中执行并 Ack |
| `CustomTerminal.viewport_area` | front/back diff 仅覆盖 inline viewport | `TerminalProjectionState` 只缓存 physical projection，不能保存业务真相 |
| `TranscriptOverlay` | committed cells 加可缓存 live tail 的 pager | alternate presenter 使用 snapshot；live tail 仅为 render-only projection |
| `enter_alt_screen` / `leave_alt_screen` | pager 与 inline primary 互斥 | `ScreenLease` 暂停 primary，release 后 primary full repaint |

Codex 的关键结论是：历史可以在 normal inline 模式交接给 scrollback，但它仍保留 semantic source；active tail 可以在主 viewport 原地更新，但不反复进入 scrollback；`Ctrl+T` 是完整记录的 alternate-screen pager，不是从主 viewport 借用一个可变 ActiveBand。

---

## 3. 目标状态模型

### 3.1 状态归属

| 状态 | 权威内容 | 允许的 consumer | 不得承担 |
| --- | --- | --- | --- |
| `TranscriptState` | committed/finalized cells、稳定 ID、revision、source、boundary | primary Layout、transcript pager、replay/export | terminal rows、front buffer、cursor |
| `ActiveCellState` | 当前 mutable assistant/reasoning/tool source、phase、source range | primary Layout、pager live-tail adapter | 第二份 finalized transcript |
| `BottomPaneState` | prompt/editor、status、popup、focus、ActiveBand projection inputs | primary Layout | transcript 内容、handoff frontier |
| `GeometryState` | width、height、layout generation、viewport rect | primary/pager Layout | 业务文本 |
| `HistoryEffectQueue` | `Pending/InFlight/Acked` handoff record | primary Presenter | 无身份的逻辑行计数 |
| `TerminalProjectionState` | primary 或 alternate 的 front/back、cursor、Known/Unknown | 当前 Presenter | semantic source |

`ActiveBand` 由 `ActiveCellState + BottomPaneState + GeometryState` 纯派生。工具 Running/Progress 的摘要可以进入 ActiveBand；完成后的 tool chain 以一个 finalized cell 进入 transcript。它们不能各自维护独立可增长的正文缓冲。

### 3.2 语义与投影的正交生命周期

```text
Semantic cell:     Created -> Mutable -> Finalizing -> Finalized
Display projection: Retained -> CommitPending -> InFlight -> AckedHandedOff
Screen ownership:  PrimaryActive <-> AlternateActive (via lease)
```

`Finalized` 不等于“已交接到 native scrollback”；`AckedHandedOff` 不等于“应用可丢弃 source”；`AlternateActive` 也不暂停 reducer 对不可丢 runtime event 的处理。三条生命周期必须分别建模。

---

## 4. RendererMode 与物理输出路径

### 4.1 模式选择

```go
type RendererMode uint8

const (
    RendererOwnedInteractive RendererMode = iota
    RendererPlain
    RendererJSON
)
```

`RendererMode` 在 chat session 启动时根据 TTY、ANSI、scroll-region/terminal profile 和显式参数选择。它不是随一条消息或一个工具事件切换的开关。

| 场景 | 物理终端 owner | 输出方式 | 状态来源 |
| --- | --- | --- | --- |
| `RendererOwnedInteractive` primary | `TuiPresenter` | frame diff + tokenized scrollback handoff | 最新 AppState snapshot |
| `RendererOwnedInteractive` + transcript overlay | alternate pager presenter 持有 lease | alternate-screen full frame | 同一 snapshot 的 transcript + live tail |
| `RendererPlain` | `PlainRenderer` | 顺序 writer 写入 | RenderEvent/CommandResult 的 plain projection |
| `RendererJSON` | JSON renderer | JSON writer 写入 | 结构化结果 |

owned interactive mode 中，“历史顺序输出”只表示 Presenter 为 eligible finalized range 执行 cursor-neutral handoff。它不是 `chatInteractionCoordinator`、slash command、tool callback 或第三方 `io.Writer` 直接向 `os.Stdout` 写入的许可。

### 4.2 Primary frame

primary Layout 从同一 generation 的 snapshot 得到：retained transcript rows、derived ActiveBand rows、status、prompt/editor、popup、cursor intent、候选 `HistoryCommit`。Compositor 固定按以下顺序覆盖：

```text
background
  -> retained transcript viewport
  -> derived ActiveBand
  -> status
  -> prompt/editor
  -> popup stack
  -> cursor intent
```

Presenter 在一个 terminal transaction 内执行：验证 primary lease/generation、执行可交接的 history effects、设置 viewport geometry、对 frame diff 或 full repaint、恢复 cursor、确认 front，并将 Ack/Fail 作为 UIAction 投回 reducer。geometry 变化只产生重新布局，绝不创建 `HistoryCommit`。

### 4.3 Native scrollback handoff

每个 effect 至少具备以下稳定身份：

```go
type HistoryCommit struct {
    Token            uint64
    CellID           scene.CellID
    Revision         uint64
    SourceRange      SourceRange
    DisplayRange     DisplayRange
    LayoutGeneration uint64
    Lines            []render.Line
}
```

选择规则：仅从 finalized、retained、未 Ack 的最老 display range 选择；宽高/ActiveBand/popup/prompt 变化不参与 eligibility。完整 terminal write 成功后才 Ack 并推进 frontier；短写或失败时 effect 不 Ack、projection 转为 `Unknown`，下一帧从 semantic source full repaint。`historyWindow`、`handoffFrontier` 和 `ScreenModel` 只能作为迁移 adapter/projection cache，不能继续定义 handoff 正确性。

---

## 5. Ctrl+T transcript pager

### 5.1 用户可见语义

`Ctrl+T` 在 interactive chat 中打开或关闭“完整记录”页面：

- 进入 alternate screen，显示完整 committed transcript；
- 支持 pager 的向上/向下、PageUp/PageDown、Home/End 和退出；是否提供独立滚动条属于 pager UI 细节，不改变状态模型；
- 用户停留在底部时，最新 committed cell 自动跟随；用户上滚后保留其 scroll offset，不被新事件强制拉回底部；
- 正在运行的 assistant/tool 以 transcript 最后的 render-only live tail 显示；该 tail 不是 committed cell，不会触发 scrollback handoff，也不会修改 boundary；
- overlay 关闭后恢复主屏；主屏从最新 snapshot full repaint，而不是尝试“恢复旧终端字符”。

当前 `Ctrl+T` 在 line editor 中用于相邻字符换位。实施本功能时，chat-level key router 必须在 interactive TUI 生命周期中优先截获该绑定，并删除或迁移该编辑器语义；不能让两个 handler 竞争同一终端输入。plain/noninteractive 模式不注册该快捷键。

### 5.2 Pager 数据契约

```go
type TranscriptPagerModel struct {
    TranscriptRevision uint64
    Cells              []scene.TranscriptCell // immutable snapshot clone
    LiveTail           *PagerLiveTail         // render-only, optional
    FollowBottom       bool
    ScrollOffset       int
}

type PagerLiveTail struct {
    CellID           scene.CellID
    Revision         uint64
    Width            int
    IsContinuation   bool
    AnimationTick    *uint64
    Lines            []render.Line
}
```

`Cells` 必须来自 `AppState.Transcript`，而不是 `FixedBottomSurface.historyWindow`、`ScreenModel` front/back、`vt.Screen` 或 native scrollback。live tail cache key 至少包含 width、active cell revision、continuation 和 animation tick；只有 key 改变才重建末尾 renderable。这样 spinner/progress 在 pager 底部可刷新，又不会在每一帧重新测量全部历史。

### 5.3 Lease 生命周期

```text
Ctrl+T
  -> UIAction AcquireTranscriptOverlay
  -> wait primary transaction
  -> ScreenLease: PrimarySuspended -> AlternateActive
  -> alternate presenter renders pager frames

close / Ctrl+T / Esc
  -> stop alternate frame submission
  -> leave alternate screen
  -> invalidate primary projection
  -> apply latest geometry and AppState snapshot
  -> primary full repaint -> PrimaryActive
```

lease active 期间 reducer 继续处理 runtime/input/resize 事件，primary Presenter 只累计 latest state 和 draw intent，不写字节。alternate presenter 只写 alternate screen；后台 stdout/stderr、history handoff 和 primary cursor helper 一律不得穿透 lease。若 acquire/enter/render/leave/repaint 任一步失败，lease 必须幂等释放，并将 terminal 标为 degraded 或走受控 plain fallback。

---

## 6. 实施阶段

### Phase T0：契约与审计

1. 将本文加入所有相关 UI 计划的文档关系，明确其不是第二份终局规范。
2. 为 `Ctrl+T` 建立 key routing 测试，先固化现有 editor transpose 冲突，再修改绑定。
3. 增加 renderer-mode/owner trace：每次 terminal flush 记录 owner、lease ID、snapshot/frame generation、history token 和调用点。
4. 完成 direct-writer debt 分类；owned interactive 中新增 `fmt.Print*`、`os.Stdout`、`ui.WriteTerminal*` 直接调用应被 CI gate 拒绝。

出口：没有任何“为 pager 临时直出”的特例；模式和 key 冲突均有可执行测试。

### Phase T1：补齐 primary semantic source

1. 将所有 active assistant/reasoning/tool producer 收敛到 `ActiveCellState`；`ActiveBand` 只消费该 source 的 Layout projection。
2. 将 replay、user submit、command result、local error 和 runtime/tool event 全部投递同一 `UIAction`/Scene transaction 顺序。
3. 使 `LayoutAppState` 同时能纯派生 primary retained transcript、ActiveBand、bottom rows 和 cursor intent；不得读取 surface mutex/terminal/VT。
4. 保留 legacy surface 作为只读/Apply adapter，但标注其 authority 已让位给 AppState。

出口：完整 transcript、active tail 和 bottom pane 都能从 immutable snapshot 构造；不会从 `historyWindow` 取 pager 数据。

### Phase T2：Presenter 与 history effect 收敛

1. 实现 `TerminalSession`/`TuiPresenter` 的 single-writer transaction，并让 `ScreenModel` 缩小为 projection cache。
2. 引入 `HistoryEffectQueue`、`HistoryCommit`、Ack/Fail action 和 failure-to-Unknown recovery。
3. 将 `FixedBottomSurface.WriteOutput` 的 owned branch 改为投递 effect/draw intent；逐步删除 `commitExcessHistoryToScrollbackLocked`、headroom/补偿作为正确性机制。
4. 建立 primary full-frame parity、handoff token exactly-once 和 resize/reflow source parity。

出口：normal interactive 的历史交接、viewport frame 和 cursor 都只由 primary Presenter 写出；历史的“顺序追加”不再是 direct writer 路径。

### Phase T3：实现 transcript overlay

1. 新增 chat-level `ToggleTranscriptOverlay` action 和 pager state；触发时经 `ScreenLease` 进入 alternate screen。
2. 从 `AppState.Transcript` 建立 pager 的 committed cell renderables；按 `BoundaryPolicy` 和当前宽度重新布局，不复用 primary front/back 字符。
3. 增加 active live-tail adapter 与 cache key；active revision、width、continuation 或 animation tick 变化时才更新尾部。
4. pager 只保留自己的 scroll/follow-bottom/highlight 状态；它不得修改 transcript sequence、active source、history effect frontier 或 primary prompt。
5. release 时使 primary projection invalid，并使用 release 后最新 snapshot 进行一次完整 primary repaint。

出口：打开、运行中更新、resize、关闭、再次打开均不存在遗漏/重复 committed cell，且任何时刻 trace 中仅有一个 active ScreenOwner。

### Phase T4：移除过渡路径并验收

1. 删除 `AICLI_SCENE_PRESENTER` 的运行时双 presenter 语义；保留必要的 shadow/parity 测试，但不让 shadow writer 写 terminal。
2. 删除 interactive 生命周期内的 surface-aware raw adapter、legacy scroll compensation 与以 `historyWindow` 行索引为权威的 handoff 逻辑。
3. 保留 `RendererPlain`/JSON 的独立实现，并测试从 capability failure 到 plain fallback 的受控 teardown，禁止同一 session overlap。
4. 在 Windows Terminal、PowerShell、ConPTY、WSL、VS Code terminal、Zellij/黑名单 profile 与 pipe 环境执行真实终端验收。

出口：normal interactive、alternate transcript pager 与 plain/JSON 分别是完整且互斥的 pipeline；项目中不存在第二个 production terminal writer。

---

## 7. 测试矩阵与验收不变量

| 类别 | 必测场景 | 断言 |
| --- | --- | --- |
| renderer mode | TTY/plain/JSON/pipe/capability failure | 每 session 仅选择一个完整 pipeline |
| primary history | finalized user/assistant/tool/command 多 cell | `CellID + Revision + SourceRange` 只产生一次 Acked handoff |
| mutable tail | stream、tool progress、stable prefix、finalize/cancel | ActiveBand 更新不改 transcript boundary；finalize 不重复 append |
| geometry | ActiveBand grow/shrink、popup、prompt、resize | 不产生 HistoryCommit；最终 frame 从 snapshot 纯派生 |
| pager | `Ctrl+T` open/close、follow-bottom、user scroll、resize | committed cells 完整、有序且 scroll offset 语义正确 |
| pager live tail | tool running、streaming、spinner tick、finalize | tail 随 revision/key 更新；finalize 后恰好转为 committed cell |
| lease | acquire/release、nested request、enter/exit/repaint short write | primary/alternate 互斥，release 后 full repaint |
| recovery | handoff/frame 部分写入、terminal error | effect 不 Ack，front `Unknown`，无盲目重复写入 |
| direct writer gate | chat/command/runtime/tool 新代码 | owned interactive 路径没有未经 owner 的 stdout/stderr/terminal write |

必须长期保持以下不变量：

1. `UIController` 是 AppState 唯一 mutation owner；Presenter 是 TTY 唯一物理 writer。
2. ActiveBand、status、prompt、popup 不改变 transcript sequence/boundary，也不直接创建 history effect。
3. native scrollback 是 display copy，不是 pager/replay/recovery 数据库。
4. 一个 finalized cell 的最终显示可以触发 repaint，但同一 `Token + CellID + Revision + Range` 最多 Ack 一次。
5. overlay live tail 是 projection，不是 cell insertion；关闭 overlay 不应改变 semantic history。
6. primary 与 alternate presenter 永不并发 flush；`ScreenLease` release 后必须丢弃旧 primary front。

---

## 8. 文件与接口落点

以下为建议边界，实施时优先复用现有包而非新增平行框架：

| 位置 | 目标职责 |
| --- | --- |
| `cmd/aicli/ui/action.go`、`controller*.go` | `ToggleTranscriptOverlay`、lease/pager action、Reducer state transition |
| `cmd/aicli/ui/app_state.go`、`app_layout.go` | transcript/active/bottom/geometry snapshot 与 primary/pager pure layout 输入 |
| `cmd/aicli/ui/scene/` | cell identity、revision、boundary 与 full transcript source |
| `cmd/aicli/ui/renderengine/` 或最终 `ui/presenter/` | terminal transaction、front/back、history effect execute/Ack/Unknown recovery |
| `cmd/aicli/ui/screen_lease.go` | primary/alternate ownership和 release full repaint |
| `cmd/aicli/ui/transcript_pager.go`（建议新增） | pager scroll/follow-bottom/live-tail cache，不直接写 TTY |
| `cmd/aicli/commands/chat_input*.go`、key router | `Ctrl+T` 全局聊天绑定，替换 editor transpose 的冲突行为 |
| `cmd/aicli/commands/chat_interaction.go` | 过渡 adapter 收敛；不得继续增加 direct terminal 输出 |

实现前必须先确认每个新接口的 authority：Reducer 可修改语义 state，Layout 可读取 immutable snapshot，Presenter 可写 terminal，pager 可维护自身视图滚动状态；其余层不得越界。

## 9. 完成定义

本方案完成的标志不是出现一个全屏历史页面，而是同时满足：

1. `Ctrl+T` pager 与 normal primary 通过 ScreenLease 互斥运行，关闭后从最新 snapshot 恢复；
2. pager 始终展示完整 semantic transcript 和可选 live tail，绝不从 terminal scrollback 或 bounded window 拼装；
3. normal interactive 下 finalized history 只由 Presenter handoff，active content 只由主 viewport projection 展示；
4. `HistoryCommit` 的 token/range Ack、resize/reflow、short-write recovery 和 direct-writer gate 均有自动化覆盖；
5. legacy `historyWindow/frontier`、原生补偿和 runtime presenter flag 不再承担生产正确性；
6. plain/JSON/pipe 降级能力保持，并且不会与 owned interactive 物理输出重叠。
