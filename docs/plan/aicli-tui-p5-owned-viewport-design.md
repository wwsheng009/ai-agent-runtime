# P5 专项设计：aicli 保留式底部 viewport + 不可变 scrollback（owned viewport backend）

状态: **historical/superseded baseline（缺陷机制已由 unified inline viewport 替换）**

更新时间: **2026-08-06**

关联文档:
- 长期架构基线: `docs/plan/aicli-tui-unified-render-architecture-refactor-plan.md`（Scene、single screen owner、事务式 frame、fullscreen lease 与旧路径删除）
- transcript pager、RendererMode 与 history handoff 的后续实施：`docs/plan/aicli-tui-transcript-overlay-renderer-mode-plan.md`（primary/alternate 所有权边界；本文不定义其终局）
- 母计划: `docs/plan/aicli-tui-render-data-plane-codex-migration-plan.md`（P1–P4.2a 已完成，本文是其 P5 展开）
- 相关: `docs/plan/aicli-ui-refactor-codex-inspired-plan.md`
- 上游数据面: `docs/plan/aicli-event-stream-rendering-order-unified-encoder-plan.md`（事件 → 有序带身份 RenderModel/ChangeSet；本页"cell 模型（数据面）"的**内容源**，cell 身份对应 `Item.ID`）

> 本文只记录 owned viewport、ActiveBand、history handoff、resize/reflow 和 P5.6 gap 的历史实施事实，不再定义终局。P5.0–P5.6 的若干功能已接线，但 `historyWindow/headroom/handoffFrontier/commitExcess` 仍共同参与正确性，重复 handoff 专项测试仍未通过，因此不能把“切片已接线”解释为“P5 退出条件已满足”。跨模块目标、状态所有权和迁移顺序统一以 `aicli-tui-unified-render-architecture-refactor-plan.md` 为准。

> **后继实现状态（2026-08-05）**：本文记录的 whole-screen owned viewport、`historyWindow/headroom/handoffFrontier/commitExcess` 不再是 interactive production 机制。当前 `TerminalSession` 仅拥有 bottom inline viewport 的 `ScreenModel`；finalized rows 与 mutable stable overflow 使用 reducer-owned tokenized effects 插入顶部 terminal history region，形成当前可见历史 tail 与 native scrollback。viewport diff 被物理 fence 在 bottom region，prompt/status/popup 不会进入 scrollback；active finalize 和 rich Markdown 已有 exactly-once 回归，真实 WezTerm/ConPTY scrollback probe 已通过。后续不得从本文恢复 whole-screen redraw、geometry-driven handoff 或 legacy frontier。

> **2026-08-06 最终 disposition**：P5 文中 `historyWindow/headroom/handoffFrontier/commitExcess` 组合机制只保留为历史故障样本，已经不是待完成设计，也不得用于修补 production。后继 inline 机制已通过真实 Windows Terminal/provider run `opencode-wt-1176ea6f5afc4fa597964cc30b50a984`：40 条 finalized marker exactly-once，最早行位于 native scrollback、最新行位于主界面、异常空行 0、正常 `/exit` 后进程退出码 0。真实回归进一步证明 P5 的两类隐蔽缺陷：无 source identity 的内部空行会阻断 final suffix handoff；native overflow 后继续 bottom-align resident tail 会在容量增长时制造中间 headroom。后继实现分别采用 newline-backed fragment identity 与 sticky top-aligned resident history；P5 不再有独立实施阶段，其后续清理统一由母计划管理。

> 该实机 run 同时确认 reasoning 语义内容在 final answer 前正常显示；仅 raw `assistant.reasoning` 协议标签未泄漏。成功的 `llm.request.finished` 在后继实现中只表示 transport boundary，必须等待 authoritative `assistant_message` 才完成正常 assistant finalization。

> **禁止作为新实现依据的过渡决策**：全局 Frame/Scrollback mode、无类型 `committedBoundary int`、几何变化驱动 history commit、依赖 RI/SD 从 native scrollback 拉回、以 `ScreenModel` 代替 semantic source truth。相关故障分析可以复用，但新实现必须走 unified plan 的 UI actor + AppState + tokenized effect + transactional Presenter。

---

## 1. 背景与目标

### 1.1 为什么需要 P5

到 P4.2a 为止，渲染面/数据面隔离的“增量安全区”已做完：原子帧提交（P1）、回放纯
路径（P2）、间距真值表单源化（P3a）、一次性完整块全部走 cell 接缝（P4.1/P4.2a）。
但三件长期收益最大的事被同一个根因卡住：

1. **resize 重排可见历史**（窄→宽重新换行）——P4.3。
2. **工具链 Running→…→Completed 合并为单个可重绘 cell**（Codex `ExecCell`）——P3b/P4.2b。
3. **删除两套易回归的状态机**：表面滚动补偿（`scrollCompensatedRows` /
   `pendingScrollDownRows` / `outputCursorOnBlankRow` / `outputScrollDebtRows`）与
   协程空行/间距标记（`lastCompletedAsyncLine` / `promptAfterBlockGap` 已删除；
   `completeBlockOutput` 暂仅保留为统一 cell 边界判定）。

根因：**当前是立即模式（immediate-mode）**——历史行一旦经 `WriteOutput` 写入终端原生
scrollback 就不可再改。代码亦明确承认这一点（见 §2.1）。要 reflow / 原地重绘 tool cell /
用“重画”取代“补偿”，就必须让**应用自己拥有一块 viewport 缓冲**，历史通过一个光标中性
的原语一次性交接给 scrollback。这正是 Codex TUI 的模型。

### 1.2 目标（P5 完成时）

- 应用拥有底部 viewport 的**双缓冲**（back/front），每帧 diff 后最小化写终端。
- 历史仅通过**单一** `insertHistoryLines(rows)` 原语进入 scrollback：在 viewport 上方
  腾行、光标中性、原子。
- `historyCell`（P4.1/P4.2a）与 `assistantTurnTranscript`（`chat_interaction_transcript.go`）
  **收敛为单一 cell 模型**，`DisplayLines(width)` 变为**宽度感知**。
- resize 时按新宽度从 cell 重排 viewport 内可见历史（带行数上限，参考 Codex
  `resize_reflow` 的 cap）。
- 工具链合并为单个可重绘 cell：Running 阶段在 viewport 内重绘，完成后作为一个 cell
  一次性 `insertHistoryLines`。
- **删除**两套状态机（§9）；cell 内间距由 cell 内容决定，跨 cell 间距由统一边界策略决定。

### 1.3 非目标

- 不做全屏 alt-screen TUI（保留“底部固定带 + 上方原生 scrollback”的形态）。
- 不改变 JSON / 非交互 / 管道输出路径（`NoInteractive` / `JSONOutput` 原样）。
- 不引入第三方 TUI 框架；复用现有 `render.*` 与已抽出的 `ui/vt.Screen` 模型。

---

## 2. 现状约束（代码事实，均已核实）

### 2.1 终端历史不可重写（立即模式）

`cmd/aicli/commands/chat_interaction_transcript.go`：
- `assistantTranscriptBlock` 注释：“**已写入 scrollback 的行，reflow 永不重写**；块只
  保留 source 范围供 finalization/resize 逻辑推理。”
- `assistantTurnTranscript` 注释：“**终端历史无法重写**；NeedsConsolidation 记录最终快照
  与已发出 source 的分歧以避免二次绘制。”

含义：P4.3 的“resize 重排已滚出的可见历史”在当前架构**做不到**，必须先有 owned viewport。

### 2.2 已存在的 VT 屏幕模型（P5 的后端雏形）

`cmd/aicli/ui/vt/screen.go`（package `vt`，由原 `ui/uitest` 转正）：
- 文档原话：“**replays the byte stream a surface actually writes and reconstructs the
  resulting rows**”——即已是一个“字节流→行”的重建器。
- 支持子集：CR/LF/IND/RI、DECSC/DECRC（ESC 7/8、CSI s/u）、CUP、CUU/CUD/CUF/CUB、
  **DECSTBM**、**SD(CSI T)**、CSI L（插入行）、CSI M（删除行）、EL、ED、SGR、OSC skip；
  **宽度感知**（`render.Width`，CJK/emoji 占两列并正确换行）。
- 测试侧 `chat_interaction_screen_test.go` 以 `screenVT{ *vt.Screen }` 包装使用。

含义：P5 不必从零写 VT/双缓冲；可将该模型（或其抽象）**转正为生产后端的行缓冲/合成层**，
测试与生产共享同一“行真相”。

### 2.3 现有底部带与滚动补偿（P5 要替换的机制）

`cmd/aicli/ui/fixed_bottom_surface.go`：
- 能力门槛 `canEnableLocked`：需 `caps.Interactive && caps.ANSI && caps.ScrollRegion`；
  Zellij 的 DECSTBM 不兼容走保守 legacy 路径（约 2269–2272）。
- `applyLayoutLocked`（约 2281+）用 **DECSTBM**（`terminalScrollRegionSequence`）设定输出区，
  底部预留变化时用 **SD/CSI Ps T**（`appendOutputScrollDownForBottomReserveShrinkSequence`
  → `terminalScrollDownSequence`）整块滚动补偿。
- 补偿状态机字段：`scrollCompensatedRows` / `pendingScrollDownRows` /
  `outputCursorOnBlankRow` / `outputScrollDebtRows`（约 104–117），在 `applyLayoutLocked`
  与 `WriteOutput`/`WriteSoftTrackedOutput`（约 598–820）中读写。
- `Terminal`（`terminal.go`）：`Width()/Height()`、`updateSize()`、`ResetScrollRegion()`、
  `Capabilities()`。

含义：补偿机制是“**在立即模式下模拟腾行**”的产物；owned viewport 下它整体被
`insertHistoryLines` + 每帧 diff 取代。

### 2.4 间距/空行状态机（P3a 已单源化，P5.6 已删除 async-chain 推断）

`cmd/aicli/commands/chat_interaction.go` 已删除 `lastCompletedAsyncLine`、
`promptAfterBlockGap` 和 `gapForAsyncLine`。`completeBlockOutput` 只回答“前面是否已有完整
block”，由 `gapBeforeBlockLocked` 统一生成最多一个跨 cell 空行；Running 是 viewport-only，
不会修改 history spacing。cell 的 `DisplayLines` 只负责 cell 内部稠密布局。

### 2.5 Codex 参考（概念对齐，非逐行移植）

- `tui.rs`：`insert_history_lines(...)`、`clear_for_viewport_change(...)`（viewport 变更时
  重画）；自定义 `Terminal` 记 `last_known_screen_size`。
- `chatwidget.rs`：`add_to_history(cell)`；finalized transcript 与 live wrapping 用同一
  viewport 宽度。
- `resize_reflow`：resize 时按新宽从 cell 重排，带行数 cap 防止历史抖动。

---

## 3. 原 P5 目标架构（historical，非当前终局）

```
             ┌───────────────────────── 终端物理屏 ─────────────────────────┐
 原生         │  ... 已滚出的历史（不可变，终端 scrollback 拥有）...          │
 scrollback   │  [由 insertHistoryLines 一次性交接，光标中性]                │
             ├──────────────────────────────────────────────────────────────┤
 owned        │  viewport（应用拥有的双缓冲区域）                            │
 viewport     │   - 顶部：最近 N 行历史 cell 的宽度感知渲染（可 reflow/重绘） │
             │   - 底部预留：ActiveBand / status / prompt / popup            │
             └──────────────────────────────────────────────────────────────┘
```

三层职责：

1. **cell 模型（数据面，唯一真相）**：`historyCell` 统一承载 user/assistant/tool/
   supplement/reasoning；每 cell 保留源（文本 / markdown / `render.Document` /
   tool 结构），`DisplayLines(width)` 宽度感知。`assistantTurnTranscript` 收敛进来（活动
   assistant 流即“一个可变 cell”）。

2. **viewport 合成器（渲染面）**：把“底部预留 + 最近若干历史 cell”合成为一个 back buffer
   行矩阵（复用 `ui/vt.Screen` 的行/宽度模型）；与 front buffer diff，产出最小 ANSI。
   每帧只重画变化单元格。**补偿逻辑消失**：底部预留增减只是 back buffer 行数变化，diff 自然处理。

3. **history 交接原语**：当某 cell 从“viewport 内可变”变为“最终、要滚入 scrollback”，调用
   单一 `insertHistoryLines(rows)`：在 viewport 上方腾出行、把 rows 写入、**保持光标位置
   语义中性**（进入/退出该原语时 back buffer 与光标状态自洽）。这是唯一向 scrollback 写历史的路径。

数据流：`event → 更新/新增 cell（数据面）→ 标记 dirty → 合成 back buffer → diff → flush`。
历史滚出：`cell finalize → insertHistoryLines(cell.DisplayLines(width)) → 从 viewport 顶部移除`。

> **衔接（2026-08-02）**：上行的"数据面"指**渲染层内部**的 cell 状态（本页 P5 范围）；其**内容与身份来源**是上游统一编码器的 `RenderModel/ChangeSet`（`Item.ID` 即 cell 身份，`Item.Head` 即 cell 源内容，见 [unified-encoder-plan](./aicli-event-stream-rendering-order-unified-encoder-plan.md) 与 [render-model-spec](./aicli-event-stream-rendering-order-render-model-spec.md)）。事件不直接新增/更新 cell，先经编码器产出变更集，本页"更新/新增 cell"环节改为消费该变更集。

---

## 4. 原 P5 设计决策（historical）

### D1. viewport 后端如何“拥有”

- 新增 `ui` 内后端类型（暂名 `ViewportBackend`），持有 back/front 两个行缓冲（`[][]Cell`，
  复用 `uitest` 的 `Cell`/宽度语义，抽到共享内部包，测试与生产同源）。
- `FixedBottomSurface` 的“底部预留合成 + 输出区”改为**向 back buffer 写**，不再直接向
  `io.Writer` 逐段写 + DECSTBM 补偿。
- 每帧 `flushLocked`：diff(front,back) → 生成 CUP+SGR+文本的最小序列 → 一次性写终端 → swap。

### D2. 历史交接：单一 `insertHistoryLines`

- 语义对齐 Codex `insert_history_lines`：把行插入 viewport 顶部之上（进入原生 scrollback），
  viewport 内容整体不动（视觉上历史“长出来”，底部带不动）。
- 实现：在 viewport 顶行之上用 DECSTBM 限定 + RI/SD 腾行并写入；**封装为唯一函数**，其余
  代码不得直接对 scrollback 发 ANSI（可加 lint/测试守卫）。
- 光标中性：进出该函数前后，back buffer 记录的逻辑光标与终端光标一致（用 DECSC/DECRC 包裹）。

### D3. cell 模型统一与宽度感知

- `historyCell` 增加 `DisplayLines(width)` 的**真实换行**实现（基于 `render.Width`）；现有
  忽略 width 的 cell 逐个升级；渲染仍由 `render.*`/`ui.Format*` 产 `render.Document`，由
  viewport 合成器按宽换行，避免与既有 surface 换行分叉。
- `assistantTurnTranscript` 的 Source/Blocks 归入一个 `assistantStreamCell`（可变 cell）：
  流式增量更新其 source，finalize 时定型并 `insertHistoryLines`。移除“已发出/分歧/二次
  绘制”这类立即模式补丁（`EmittedDiverged` / `NeedsConsolidation`）。

### D4. 工具链合并为单个可重绘 cell（P3b/P4.2b）

- Running/Progress 阶段：`toolChainCell` 停留在 viewport 内（不进 scrollback），随事件**原地
  重绘**（Running→Running→…）。
- 完成阶段：整链定型为一个 cell，一次 `insertHistoryLines` 进 scrollback。
- 实时反馈不回退：Running 立即在 viewport 可见；只是它此刻属于“可变 viewport”而非 scrollback。
- Running ActiveBand 与 retained history 之间有一个可折叠的语义 top gap；完成后 final cell 按统一边界策略提交。Completed/Failed 标题与其 output/detail 在单 cell 内稠密，独立 final tool/event cells 之间一个空行。删除 `gapForAsyncLine` 的逐行推断需求。

### D5. 能力降级与回退

- `caps.Interactive && caps.ANSI && caps.ScrollRegion` 不满足（含 Zellij DECSTBM 黑名单）→
  **回退到当前立即模式路径**（保留一版），owned viewport 仅在能力满足时启用。
- 生产切换时必须提供环境急停（预定 `AICLI_TUI_OWNED_VIEWPORT=0`）；当前影子/S1 代码尚未接入该开关。
- 非交互/JSON/管道：完全绕过 viewport（沿用现有分支）。

### D6. 并发与写锁

- 复用现有 `WithTerminalWriteLock`；viewport flush 必须在锁内且**单写者**。合成（back buffer
  更新）可在协程锁内准备，flush 时统一提交，避免 ActiveBand/status 增长插入历史空洞
  （P1 已确立“整块一次写”的原则，这里延伸为“整帧一次 diff-flush”）。

---

## 5. 历史分步实施记录

> 原则：每步 gofmt + `go build` + 目标包全测 + `go vet` 全绿方可进入下一步；任何一步都保持
> “能力不足即回退旧路径”。VT 断言（`vt.Screen`）作为主回归。

### P5.0 ✅ 已完成（2026-07-30，共享行缓冲抽出，零行为变更）

- 把 VT 屏幕模型（`Screen`/`Cell`/行矩阵/宽度语义 + 全部方法）从 `cmd/aicli/ui/uitest`
  移到**生产可用包 `cmd/aicli/ui/vt`**（package `uitest`→`vt`，文件整体移动、内容不变），
  其自带单测一并迁入 `ui/vt` 并全绿（11 项）。
- **决策修正**：原计划让 `uitest` 保留薄封装转发；核实后发现全仓仅 1 个消费者
  （`commands/chat_interaction_screen_test.go`，测试文件），故直接把它重指到 `ui/vt`、
  **删除 `uitest` 包**，避免留一个永久转发 shim（更干净、同等风险）。
- 验证：`go build ./cmd/aicli/...`、`go test ./cmd/aicli/ui/...`（含 `ui/vt`）、
  `go test ./cmd/aicli/commands` 全绿；`go vet ./cmd/aicli/...`、`gofmt -l` 干净。
- 回退：纯移动 + 改包名 + 重指 import，逆操作即可还原。

### P5.1 ✅ 已完成（2026-07-30，`viewport.Backend` 双缓冲 diff，影子模式）

- 新增 `cmd/aicli/ui/viewport` 包与 `viewport.Backend`：持 front/back 两个 `vt.Cell` 网格；
  `StageFrame`/`StageRow` 写 back，`Flush()` 逐行 diff front→back、产出最小 ANSI（CUP + 每
  单元 SGR 复位/设置 + 文本；宽字符跳过续列、空白以空格清除）并 swap `front=back`。
- **仅影子模式**：无任何生产渲染路径构造它；正确性由“经 `ui/vt` 往返”验证——
  `feed(blank→front)` 后 `feed(front→back)` 在屏幕模型上等价于 `feed(blank→back)`。
- 测试（`ui/viewport` 全绿）：文本改动 / 新增行 / 清空中间行 / CJK 宽字符互换 / SGR 变更 /
  滚动样式 / 变宽变窄 共 8 例往返一致（文本 + 逐行 SGR）；diff 局部性（改一行只寻址该行）；
  无改动 / 重复帧幂等为空；resize 后整帧重绘。`go build` / `go vet` / `gofmt`、`ui/...` 全包绿。
- 与原设计的细化：原描述为“喂 surface 字节流、重建行对比”；实现改为更直接的**双缓冲 diff
  生产者**——后端自身产出帧，再用 `vt` 往返做影子校验（而非重建旁路），验证更强。
- 回退：新增独立包、无生产接线，删除即可。

### P5.2 —— 底部预留改走 back buffer（仅底部带，历史仍旧路径）

- ActiveBand / status / prompt / popup 的合成改为写 back buffer + `flush`，替换其 DECSTBM
  逐段写；**历史输出仍走旧 `WriteOutput`**。
- 目的：先在“底部带”验证 diff-flush，与历史解耦。删除**底部带相关**的补偿分支。
- 测试：`chat_interaction_midstream_blank_test`、`live_vs_replay_blank`、status/popup 全绿；
  VT 对比底部带无空洞。
- 回退：flag 切回旧底部带合成。
- **已确认缺陷（P5.2 必须修复并加回归）**：当前 band 增长用
  `appendOutputScrollUpForBottomReserveGrowthSequence` 把输出区整体上滚（顶部历史滚入
  scrollback），band 收缩再用 SD（`appendOutputScrollDownForBottomReserveShrinkSequence`，
  `CSI T`）在**区域顶部插空行**把内容下移——但已滚走的历史无法取回，于是**空行被画到屏幕顶部、
  盖在屏幕外历史之上**。`vt` 复现：`ui/TestBottomReserveShrinkCompensationDrawsBlanksAtTop`
  （grow 1→3、shrink 3→1 后 row1/row2 变空、L1/L2 丢失）。P5.2 让 band 在 owned back buffer 中
  就地合成、覆盖底部行，**不再为 band 增减上滚/下滚历史**，从根上消除该缺陷；届时把该刻画测试
  反转为“顶部不得出现补偿空行、历史保持锚定”的回归。

#### P5.2a ✅ 已完成（2026-07-30，owned-viewport 合成器 + 修复证明，影子）

- 新增 `viewport.Compose(width,height,history,bottom)`：把 owned 历史 + 底部预留合成整屏
  `vt.Cell` 帧——底部预留占末 N 行，输出区**顶部对齐**显示最近历史行；band 增长隐藏最旧行、
  收缩则**恢复**它们（调用方持全量历史，不再滚入 scrollback）。
- **修复证明（影子，未接生产）**：`viewport/TestCompose_GrowShrinkKeepsHistoryAnchored` 用
  与缺陷相同的 grow 1→3→1 场景，经 `Backend`+`vt` 往返后 L1..L5 全部恢复、顶部无补偿空行，
  与 `ui/TestBottomReserveShrinkCompensationDrawsBlanksAtTop`（旧路径缺陷）形成对照。另有
  窗口选择 / 短历史留白两项 `Compose` 单测。`go build`/`gofmt`、`ui/...` 全包绿。

#### P5.2b.1 ✗ 已回退（2026-07-30，flag 化“清空代替 SD”探针失败）

- 试过：`AICLI_TUI_OWNED_VIEWPORT` flag，打开时 `appendPendingOutputScrollDownLocked` 不发 SD、
  改为清空腾出的底部条。**已回退**（flag 删除，代码回到原 SD 行为）。
- 结论（经无条件探针实测）：收缩 SD 是“无空隙 / ActiveBand 布局中性 / live-vs-replay 行一致”
  等不变量的**承重件**——无条件去掉它会一次性打红 15+ 测试，含
  `EOSFusionLeavesNoBlankGap`、`StableCommitThenBandShrinkKeepsAdjacency`、
  `ActiveBandIsLayoutNeutral`、`LiveStreamScreenLayoutParityWithReplay`、
  `MidStreamScreenRowsMatchReplayRows`。
- 即：**顶部不盖历史** 与 **底部不留空隙 / 布局中性** 在立即模式下互斥；两者只能靠“保留历史 +
  重渲染”同时满足 = owned viewport（`Compose` 已在 P5.2a 证明）。所以该缺陷**不能**用改补偿的
  小补丁修，也不该藏在 flag 后——正确修法是把底部带 + 历史整体搬进 owned viewport 作为默认路径，
  一次性替换掉这套补偿。缺陷仍在（`ui/TestBottomReserveShrinkCompensationDrawsBlanksAtTop` 刻画之），
  待下面的 P5.2b/P5.3 落地根治。

#### P5.2b（修订）—— 底部带整体走 owned 渲染（默认，非 flag）

- 把 ActiveBand / status / prompt / popup 合成从 DECSTBM 逐段直写 + 补偿改为：构造底部 cell 行 →
  `Compose`（配合 P5.3 的历史窗口）→ `Backend.Flush` 写终端；删除**底部带相关**补偿分支
  （grow/shrink 序列、`pendingScrollDownRows` / `scrollCompensatedRows` 等）。
- **必须与 P5.3 合并推进**：只有同时拥有历史窗口，收缩才能“重渲染历史贴住底部带”，从而**同时**满足
  无空隙 + 顶部不盖历史 + 布局中性——这正是探针失败揭示的硬约束。
- 保留急停 killswitch（默认走新路径；env 仅用于紧急回退，不是“开启 bug 修复”）；能力不足即旧路径。
- 测试：现有 `EOSFusionLeavesNoBlankGap` / `StableCommitThenBandShrinkKeepsAdjacency` /
  `ActiveBandIsLayoutNeutral` / `LiveStream*Parity` / `midstream_blank` 全部保持绿；把
  `ui/TestBottomReserveShrinkCompensationDrawsBlanksAtTop` 反转为“顶部不得出现补偿空行、历史锚定”。
- 这是**改动最大的一步**（重排底部带渲染 + 历史窗口 + 删补偿），分小步落地、每步跑上述全套回归。

#### 实施拆解（小步、每步保持全绿）

- **S1 ✅ 历史窗口捕获（2026-07-30）**：`FixedBottomSurface.historyWindow`（logical 提交行，
  `writeOutput` 单钩子捕获所有 scrollback 写；partial-line coalesce；`historyWindowMaxLines` 上限；
  `HistoryWindowForTest`）。纯 additive、无渲染改动；`ui/...`+`commands`+`build`+`gofmt` 全绿；
  单测 `HistoryWindowCapturesCommittedLines`/`CoalescesPartialLines`/`Bounds`。
- **S2 ✅ 底部预留 → cells（2026-07-30）**：新增只读 `BottomRowsSnapshot() [][]vt.Cell`，
  由共享 `status/popup/prompt/ActiveBand` paint plan 物化完整底部预留（含显式空 margin/gap、
  structured style、CJK continuation cell）；现有 immediate-mode writer 仍是生产输出，但改为消费同一 paint plan，
  避免复制 UI 规则。ANSI 经 `vt.Screen` 结构化解析，不做 ad-hoc stripping；VT 对照测试覆盖 prompt、
  notice、dynamic status、ActiveBand grow/shrink/diff/clear、popup open/close/composer，以及 SGR+wide cells。
  `ui/...`、`commands`、`build`、`vet` 全绿。
- **S3 ✅ 影子合成校验（2026-07-30）**：`HistoryRowsSnapshot()` 按当前宽度将保留的 styled logical history
  经 `vt.Screen` wrap/materialize；`ComposedFrameForTest()` 调用 `viewport.Compose(historyCells, bottomCells)`，
  不发任何终端字节。VT 对照覆盖 full history、CJK/wide、dynamic status、ActiveBand grow，以及 popup grow；
  增长帧与 legacy screen 逐 cell 一致。ActiveBand shrink / popup close 的 shadow delta 被单测明确刻画为旧
  immediate-mode 补偿造成的差异（底部行仍一致），作为 S4 切换后的反转目标。
- **S4 切换渲染（默认）**：✅ 底部带改走 `Compose`+`Backend.Flush`，删底部带补偿分支；VT 不变量测试
  （`EOSFusionLeavesNoBlankGap` 等）保持绿作为真值，序列级单测迁移为 VT 断言。
- **S5 收尾**：✅ resize / clear / soft-tail rewrite 与历史窗口对账；把
  `TestBottomReserveShrinkCompensationDrawsBlanksAtTop` 反转为“顶部不得出现补偿空行、历史锚定”回归。

### P5.3 ⚠ 部分落地、验收未通过 —— `insertHistoryLines` 唯一原语 + 历史交接

- 历史 cell 定型时改用 `insertHistoryLines(cell.DisplayLines(width))`；viewport 顶部维护
  “最近 N 行历史”窗口。删除 `pendingScrollDownRows`/`outputScrollDebtRows`/
  `outputCursorOnBlankRow`/`scrollCompensatedRows`（§9）。
- 测试：历史与底部带无重叠/无吞行（VT tail parity）；`RawOutputBeforeHistory`、
  `SeparatesFinalToolCells`、`MatchesLiveCompleteBlockRendering`、`ReplayIgnores*` 全绿。
- 回退：flag 切回旧“WriteOutput + 补偿”。

### P5.4 —— cell 模型统一 + 宽度感知 `DisplayLines` (completed)
- [x] P5.4-S1: `assistantStreamCell` + width-aware DisplayLines (raw source, no immediate padding)
- [x] P5.4-S2: `widthAwareDisplayLines` helper + all 4 historyCell implementations (user/assistant/supplement/document)
- [x] P5.4-S3: `EmittedDiverged`/`NeedsConsolidation` fields and all debug references removed; `assistantStreamCell` used for finalized assistant turns
- [x] 流式 golden、中英混排换行、`assistant` finalize 与 live 一致；所有相关测试通过
- 回退：无（默认 owned viewport 路径）

### P5.5 ⚠ 部分落地、仍依赖旧保留状态 —— resize 重排（P4.3）

- 监听 `Terminal.updateSize()`（SIGWINCH/轮询）→ 按新宽重排 viewport 内历史 cell，带**行数
  cap**（超过 cap 的历史不重排，直接以旧行呈现，避免抖动，对齐 Codex `resize_reflow`）。
- 测试：40/80/120 列 golden；窄→宽→窄往返一致；cap 生效；不触碰已滚出 scrollback。
- 回退：flag；关闭则 resize 仅重排底部带（现状行为）。

### P5.6 ✅ 内容/gap 行为已落地 —— 工具链合并单 cell（P3b/P4.2b）

- `toolChainCell` 在 viewport 内重绘 Running；完成后一次 `insertHistoryLines`。Running 不进入 scrollback，也不修改 history spacing。
- 删除 `gapForAsyncLine`、`lastCompletedAsyncLine` 和 prompt-gap 的 async-chain 推断；跨 cell 统一为最多一个空行，tool cell 内部保持稠密。
- 测试覆盖“独立 final tool cells 间单空行 + Completed/Failed 与自身 output 相邻”；Running 实时可见，且 ActiveBand 与 retained history 之间的 top gap 在短终端可折叠。
- 回退：flag；关闭则回到逐行 supplement（现状）。

### P5.7 —— 收尾：删旧路径开关 + 文档

- 观察一版（灰度/自测）稳定后，移除 P5 owned viewport 已替代的立即模式旧路径与 flag（或保留 session-level env 急停一版）。
- 与 `aicli-tui-unified-render-architecture-refactor-plan.md` 的 P0–P3、P8–P9 对账：P5.7 只删除已经被 Scene/presenter 或明确 plain renderer 替代的路径，不提前删除仍承担降级职责的代码。
- 回填母计划状态；满足本文 P5 范围验收后标记 implemented。全局 single-owner、CommandResult、fullscreen lease 等长期任务继续由统一架构文档跟踪。

---

## 6. 测试矩阵

| 维度 | 断言方式 | 覆盖步骤 |
| --- | --- | --- |
| 底部带无空洞 | `vt.Screen` + `maxBlankRunAboveBottom` | P5.2+ |
| 历史/底部带 tail parity | `assertScreenTailParity`（live vs replay） | P5.3+ |
| final tool/event cell 间单空行 + cell 内稠密 | `SeparatesFinalToolCells` + ActiveBand gap VT/snapshot | P5.6 |
| live 与 replay 完整块一致 | `MatchesLiveCompleteBlockRendering` | P5.3+ |
| 回放不触发补偿 | `ReplayIgnoresPromptCompensation` | P5.3+ |
| 间距真值表 | `TestGapPolicyTruthTable`（逐步淘汰为 cell 布局测试） | P3a→P5.6 |
| cell 逐行等价 | `*Cell_DisplayLinesMatchLegacyPipeline`（升级为宽度感知版） | P5.4 |
| diff 最小性 / swap | `ViewportBackend` 单测 | P5.1 |
| 宽字符/CJK 换行 | VT 宽度断言 | P5.4/P5.5 |
| resize 重排 golden | 40/80/120 + 往返 + cap | P5.5 |
| 能力降级回退 | 关 flag / 非 ANSI / Zellij 走旧路径 | 全程 |
| Running 实时可见 | 完成前 VT 含 Running 行 | P5.6 |

补充：保留 `go test ./cmd/aicli/commands` 与 `./cmd/aicli/ui/...` 全包作为每步护栏；已知
`TestPromptStartupSessionSelectionWithReader_RetriesAfterInvalidChoice` 为既有 spinner 计时
flake，P5 期间若复现应先隔离确认再排除（不得作为 P5 回归误判）。

---

## 7. 风险与回退

- **最高风险**：`insertHistoryLines` 光标中性 / scrollback 交接错误 → 历史吞行、重叠、光标漂移。
  缓解：P5.3 独立成步、VT tail parity 强断言、`insertHistoryLines` 为唯一入口便于定位。
- **平台差异**：Windows ConPTY、Zellij DECSTBM、tmux、各终端 SD/DECSTBM 行为差异。
  缓解：`caps.ScrollRegion` 门槛 + Zellij 黑名单沿用；不满足即旧路径。
- **性能**：每帧 diff 成本。缓解：脏区最小化、行级/单元格级 diff、合并高频事件（沿用
  `DefaultGeometryProbeMinInterval` 之类节流）。
- **回退策略**：生产切换步骤须增加 feature flag + `AICLI_TUI_OWNED_VIEWPORT` env 急停；
  当前影子/S1 切片没有生产开关。旧立即模式路径至少保留到 P5.7 观察期结束。

---

## 8. 验收标准（P5 owned viewport 范围完成；TUI 长期终局见统一架构文档）

> **当前判定：未满足。** 以下条目保留为历史 P5 验收基线，不代表当前完成。尤其 §9 中的 handoff/headroom/legacy 状态仍存在，重复 handoff 专项仍失败；后续不再单独按 P5 结构补齐，而是在 unified plan 的 P4–P8 迁移中以 tokenized projection effect 替代。

1. resize 窄→宽历史正确重排（cap 内），不触碰已滚出 scrollback。
2. 工具调用为单 cell、Running 实时可见且 viewport-only；final cell 内稠密、与其他 final event cell 间单空行。
3. cell 内空行由 cell 内容决定，跨 cell 空行由统一边界策略决定；`gapForAsyncLine` 逐行推断被移除。
4. §9 两套状态机字段全部删除且无回归。
5. 能力不足时行为与今日立即模式一致（可回退）。

---

## 9. 删除 / 收敛清单（P5 完成时）

表面滚动补偿（`fixed_bottom_surface.go`）：
- `scrollCompensatedRows`、`pendingScrollDownRows`、`outputCursorOnBlankRow`、
  `outputScrollDebtRows` 及其在 `applyLayoutLocked` / `WriteOutput` / `WriteSoftTrackedOutput`
  的读写；`appendOutputScrollDownForBottomReserveShrinkSequence` 等补偿序列构造。

协程空行/间距推断（`chat_interaction.go`）：
- 已删除 `lastCompletedAsyncLine`、`promptAfterBlockGap` 与 `gapForAsyncLine`；
  `completeBlockOutput`/`gapBeforeBlockLocked` 暂作为统一跨 cell 边界策略，待 history cell 容器直接持有边界后再删除。

立即模式补丁（`chat_interaction_transcript.go`）：
- `assistantTurnTranscript.EmittedDiverged` / `NeedsConsolidation` / `RetainedSourceBytes`
  等“不可重写历史”的绕行字段（收敛进可变 assistantStreamCell 后不再需要）。

> 注：删除须在对应 P5.x 步骤内、其替代路径通过全测后进行；不得提前删除导致中间态失衡。

---

## 10. 未决问题（评审时确认）

1. ~~共享行缓冲落在哪个包~~ **已定（P5.0）**：`cmd/aicli/ui/vt`（非 internal，`ui` 生产
   代码与测试皆可导入；已删除 `uitest` 包，无转发 shim）。
2. viewport 内“最近 N 行历史”窗口大小与 resize cap 具体取值（对齐 Codex 还是按本项目终端高）。
3. feature flag 命名与灰度策略（单总开关 vs 每步子开关）。
4. `insertHistoryLines` 在非 DECSTBM 终端的降级实现是否值得做，还是直接旧路径。
5. 工具链 cell 的 Running 重绘频率与节流阈值。

---

## 11. 当前下一步

P5 不再独立推进新的状态机。下一步统一转入长期架构计划：

- 先建立 `UIAction` mailbox 和 UIController 单一写者，现有 surface setter 退化为 adapter；
- 将 transcript、active cell、ActiveBand projection、prompt/status/popup 收进统一 AppState；
- 以 tokenized `HistoryCommit` effect 替换 `commitExcess + frontier + headroom + Invalidate`；
- 几何变化只更新布局并 compose，不推进 handoff，不从 native scrollback 拉回；
- 完成 Presenter ownership、Known/Unknown recovery 和 ack/failure 注入后，再删除 §9 遗留符号；
- P5 文档保持 historical 状态，不再改标 implemented；最终验收只在 unified plan P9 记录。
