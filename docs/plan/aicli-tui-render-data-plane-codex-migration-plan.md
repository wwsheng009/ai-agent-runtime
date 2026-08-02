# aicli TUI 渲染面/数据面隔离与 Codex 对齐迁移方案

状态: **implementing / historical migration record（P1–P5.6 的当前实施结果由 P5 专项文档承接；P2 完整纯提交、direct output、single-owner、fullscreen lease 与 legacy 删除继续由统一长期架构计划推进）**

更新时间: **2026-07-31**

## 0. 文档定位

本文是以下既有计划的**收敛与续作**，聚焦一条更窄但反复出问题的主线——
**渲染面（terminal 布局补偿）与数据面（transcript / session.Messages）的隔离**，
并给出参考 Codex TUI 的分阶段迁移路径：

- 上位计划：[aicli-ui-refactor-codex-inspired-plan.md](./aicli-ui-refactor-codex-inspired-plan.md)
- 渲染专题：[aicli-ui-ux-rendering-codex-reference-plan.md](./aicli-ui-ux-rendering-codex-reference-plan.md)
- 实施审查：[aicli-ui-rendering-implementation-review.md](./aicli-ui-rendering-implementation-review.md)
- 数据解耦：[aicli-chat-session-messages-decouple-plan.md](./aicli-chat-session-messages-decouple-plan.md)
- P5 当前实施真相：[aicli-tui-p5-owned-viewport-design.md](./aicli-tui-p5-owned-viewport-design.md)
- 长期架构基线：[aicli-tui-unified-render-architecture-refactor-plan.md](./aicli-tui-unified-render-architecture-refactor-plan.md)
- 数据面产生端收敛（事件 → 有序带身份模型，统一编码器）：[aicli-event-stream-rendering-order-unified-encoder-plan.md](./aicli-event-stream-rendering-order-unified-encoder-plan.md) 及配套 [render-model-spec](./aicli-event-stream-rendering-order-render-model-spec.md)、[event-encoder-api-design](./aicli-event-stream-rendering-order-event-encoder-api-design.md)、[migration-roadmap](./aicli-event-stream-rendering-order-migration-roadmap.md)

> 本文保留 P1–P5 的迁移动机、Codex 对照与阶段历史。当前 owned viewport、ActiveBand、history handoff 和 P5.6 gap 行为以 P5 专项文档为准；跨模块的 Scene/Presenter、single physical writer、CommandResult、fullscreen lease 和删除计划以统一长期架构文档为准。

参考实现：`E:\projects\ai\codex\codex-rs\tui`（Rust + ratatui + crossterm）。

本文不重复上述文档已覆盖的颜色/主题/Markdown/事件建模等内容，只处理
“已定稿历史被终端布局补偿污染”“块间空行由脆弱状态机推断”“回放复用实时写入路径”
这一类结构性问题，并把它们对齐到 Codex 的渲染范式。

## 1. 背景与硬约束

### 1.1 两个独立机制，绝不混用

- **A. 渲染面 / 布局补偿**：`pendingScrollDownRows`、`outputScrollDebtRows`、
  `scrollCompensatedRows`、`outputCursorOnBlankRow` —— 仅当底部预留
  （prompt / ActiveBand / popup / status）增减时做**几何补偿**。
- **B. 内容面 / 数据面**：`streamBuffer`、`transcript.Source/Blocks`、
  `session.Messages` —— 纯源文本，**永不**为了“补布局窟窿”而改写内容。

P5.6 当前实现仍通过显式 `blockGap`（`gapNone` / `gapBlank`）和统一入口生成跨 cell 空行，`completeBlockOutput` 仅是过渡边界状态；长期由 `BoundaryPolicy(prev, next)` 取代调用点推断。任何状态都不得在流式中途凭空造空行。

### 1.2 反复出现的 bug 类（本线索的动机）

1. 连续工具异步行之间出现异常空行（历史稠密度丢失）。
2. 终端补偿在等待态回放时串入历史消息，吞掉消息分隔空行（已定位并临时修复）。
3. 立即模式多步重绘导致的撕裂/闪烁。

根因是**范式**：当前用“立即模式手写 ANSI + 有状态补偿 + 空行状态机”重造了
Codex 用“保留式 viewport 差分 + 原生 scrollback 不可变提交 + cell 自带间距”
天然获得的东西。

## 2. Codex 参考模型（要点，含源引）

1. **inline viewport，非 alt-screen**：主 UI 是底部锚定的内联 viewport，
   alt-screen 只留给 overlay（`tui.rs:375`）。
2. **已定稿历史 = 原生 scrollback，一次性不可变**：`insert_history` 用
   `SetScrollRegion(1..viewport.top)` + 反向索引 `\x1bM` 在 viewport 上方腾行写入，
   写完 `ResetScrollRegion` 并把光标还回 `last_known_cursor_pos`——**光标中性**
   （`insert_history.rs:1-4,193-245`）。写入后应用永不再动这些行。
3. **每帧只差分 viewport**：ratatui 双缓冲 `diff_buffers` 只覆盖 viewport（active
   cell + bottom pane），并把行尾空白合并成一次 `ClearToEnd`
   （`custom_terminal.rs:299-306`）。
4. **每帧包同步更新（DEC 2026）**：`stdout().sync_update(...)`，viewport 差分 +
   历史 flush + 光标移动原子提交（`tui.rs:898`）。
5. **间距属于 cell/boundary 模型**：Codex 的部分空行由 cell `display_lines` 表达；aicli 当前约束为 cell 内部稠密、跨 top-level cell 由统一 boundary policy 生成，二者都禁止业务调用点依靠历史布尔状态逐行推断。
6. **三视图数据模型 + 单一真源**：`display_lines`(屏幕) / `raw_lines`(可复制) /
   `transcript_lines`(导出/overlay)，真源是 `transcript_cells: Vec<Arc<dyn HistoryCell>>`；
   scrollback 是“某一宽度的派生产物”（`history_cell/mod.rs:189-298`,
   `app/history_ui.rs`, `app/resize_reflow.rs:38-45`）。
7. **resize 用源重建**：宽度变化时按新宽度从 cell 重排可见 scrollback（有行数上限，
   `resize_reflow_cap.rs`）；流式 cell 存 markdown 源，无损重排。
8. **事件先经统一编码器再进数据面**：`transcript_cells` 的内容不是事件直接写入的，
   而是 `ThreadHistoryBuilder.handle_event`（`thread_history.rs:321-385`）把全部
   EventMsg 编码为带 `id` 的 `ThreadItem`（append/upsert 幂等），`transcript`
   只消费编码后的有序模型。本项目对应机制见
   [unified-encoder-plan](./aicli-event-stream-rendering-order-unified-encoder-plan.md)：
   数据面（transcript/历史）的产生端应收敛为统一编码器输出，事件不得旁路直写。

## 3. 当前架构差距映射

| 维度 | Codex | 当前项目 | 迁移阶段 |
|---|---|---|---|
| 帧原子性 | DEC 2026 同步更新 | 无（逐段写，可撕裂） | **P1 ✅** |
| 回放路径 | 从 cell 纯渲染 + 提交 | 复用实时协程写入路径（含 prompt 副作用） | P2 |
| 块间空行 | cell/boundary 模型 | P5.6 已删除 async-chain 推断；`blockGap/completeBlockOutput` 仍为过渡边界实现 | P3/P5.6→统一计划 P6 |
| 历史数据模型 | `transcript_cells` 单一真源 | `session.Messages` + 分散格式化 | P4 |
| 底部区渲染 | ratatui 双缓冲差分 | 立即模式手写 + 补偿债务状态机 | P5 |
| resize 重排 | 从源按新宽度重建（有 cap） | 仅重排 soft tail | P4/P5 |

## 4. 分阶段方案

风险随阶段递增，逐阶段落地、每步带回归；每阶段都保留能力门控与降级路径。

### P1 —— 同步更新（原子帧）✅ 已完成（2026-07-30）

目标：多步重绘一次性原子提交，消除撕裂/闪烁（Codex `sync_update`）。

已实现：
- `terminal_driver.go`：能力新增 `SynchronizedOutput`（仅 `ansi && vt` 为真）。
- `terminal_write_lock.go`：`WithTerminalWriteLock` 启用时用
  `\x1b[?2026h … \x1b[?2026l` 包裹整批；panic 也会 `defer` 闭合；env 急停
  `AICLI_DISABLE_SYNC_UPDATE`；开关 `SetTerminalSynchronizedFrames`。
- `fixed_bottom_surface.go`：生产 `Enable()` 依能力开启、`Disable()` 关闭；
  测试走 `EnableForTest` 永不开启 → byte-exact 断言零改动。

验证：新增 `terminal_write_lock_sync_test.go`（默认关闭无括号 / 启用整批只包一层 /
连续批次各自成帧且平衡 / env 急停 / 非 TTY 保守关闭）；`ui` 全包 + `commands` 全包 +
`go vet` + `gofmt` 全绿。

风险/回退：极低；env 急停可一键关闭；不支持 2026 的终端忽略未知私有模式。

### P2 —— 回放纯渲染路径 ⚠️ 部分完成（2026-07-30）

目标：`printVisibleChatHistory` 及其它回放入口**不再调用实时协程方法**
（`RenderSubmittedUserInput` / `RenderAssistant` / `RenderAsyncLine`），改为
“从 `session.Messages` 构建块行 → 经无副作用的提交入口写出”，把上一轮
`historyReplayActive` 补丁升级为结构性解耦。

实现要点：
- 复用现有格式化器（`FormatUserMessage`/`FormatAssistantRendered`/tool 渲染）产出
  每个块的行集合（含块自带的显式 `blockGap`），不触碰 prompt/waiting/补偿状态。
- 新增 surface 侧或 coordinator 侧的 `commitHistoryBlock(lines, gap)` 纯提交入口：
  只做“可选显式空行 + 原子多行写出”，绝不 ShowPrompt / 不累计补偿。
- 保留 header（`RenderSupplement`）与首个 user 回显之间“无空行”的既有语义。

测试：
- 复用 `TestPrintVisibleChatHistory_ReplayIgnoresPromptCompensation`（等待/非等待逐行一致）。
- 复用 live-vs-replay parity 与 tool-chain denseness 回归。
- 新增：断言回放路径**不产生任何 ShowPrompt / 补偿序列**（可在 screenVT 或字节流上校验）。

风险：中。触及回放路径，但断言矩阵强（skeleton + parity）。回退：保留旧路径开关一版。

已实现（本次）：
- 删除协程全局可变标记 `historyReplayActive` 及 `SetHistoryReplayActive`（消除全局态/交错隐患）。
- 抽出 `renderUserEchoLocked(input, allowPromptRestore)`；`RenderSubmittedUserInput`（实时，
  `true`）与新增 `RenderReplayedUserInput`（回放，`false`，永不 ShowPrompt）共用之。
- transcript renderer 新增 `replay` 标记与 `newAICLIReplayTranscriptRenderer`；`RenderUser`
  在回放时走 `RenderReplayedUserInput`。`printVisibleChatHistory` 改用回放 renderer，
  回放意图在调用点显式，而非依赖全局 mode。
- 其余块（assistant/tool/supplement）当前仍共用实时 coordinator 渲染路径；它们没有 user
  echo 的 ShowPrompt 副作用，但仍会经过 `beginMessageLocked` 和间距状态。完整的
  `commitHistoryBlock(lines, gap)` 无副作用提交入口尚未接入，故 P2 不标记为整体完成。

验证：`ReplayIgnoresPromptCompensation`、`LiveStreamBlankParityWithReplay`、
`KeepsToolChainDense`、`MatchesLiveCompleteBlockRendering`、`RenderSubmittedUserInput*`、
`RawOutputBeforeHistory` 定向回归通过。完整 `commands` 包仍可能触发已知 spinner 计时
flake，须以隔离复跑确认；全局 `gofmt -l` 须在提交前重新验证。

### P3 —— 间距策略单源化 + 真值表锁定

拆为两半：**P3a（间距单源化，安全切片）已完成**；**P3b（空行物理内嵌进 cell
内容、工具链合并为单 cell）并入 P4**，因为它必须依赖 cell 模型才能干净落地——
在无 cell 模型时“每个块自带空行”仍需按前一块类型判断，等价于把状态机搬进块渲染，
反而更散。

#### P3a ✅ 已完成（2026-07-30，安全切片）

目标：在不改行为的前提下，把分散的空行判断收敛到**唯一一处**并用真值表锁死，
终结“空行反复回归”这一类 bug。

已实现（本次）：
- 新增 `gapBeforeBlockLocked(promptWasVisible, promptAfterBlockGap)` 作为唯一空行策略核心；
  `gapForTopLevelMessage` 与 `gapForAsyncLine` 改为其薄封装（async 仅多一条
  `previousAsyncLine → gapNone` 的工具链稠密短路）。消除两处近乎重复的判断体。
- 保持三标记（`completeBlockOutput`/`lastCompletedAsyncLine`/`promptAfterBlockGap`）语义不变，
  仅收敛读取点；行为逐位保留。

验证：新增 `chat_interaction_gap_policy_test.go` 穷举 8 组
`(promptWasVisible, promptAfterBlockGap, completeBlockOutput)` 真值表 + 工具链稠密 +
`gapIfPriorComplete` + nil 接收者；`KeepsToolChainDense`/`Blank`/`LiveStreamBlankParity`/
`MatchesLiveCompleteBlockRendering`/`ReplayIgnoresPromptCompensation` 全绿；
`commands` 全包 + `go vet` + `gofmt` 全绿。

#### P3b（并入 P4）

目标：空行成为 cell `DisplayLines` 的一部分；工具链（Running→Running→Completed）
合并为**单个 cell** 一次性渲染，从根本上取消“逐异步行判断空行”的需求（Codex 即如此）。
依赖 P4 的 cell 模型，故随 P4 一并实现。

### P4 —— cell 化历史模型 + resize 重排

目标：引入 `HistoryCell`-式抽象（`DisplayLines` + Kind + 保留每 cell 源），使
`session.Messages` → cell → 行的映射稳定；resize 时按新宽度从 cell 重排可见历史
（加行数上限，参考 `resize_reflow_cap.rs`）；流式块保存 markdown 源做无损重排。
分子步落地，每步带回归。

#### P4.1 ✅ 已完成（2026-07-30，只读 cell 接缝）

- 新增 `chat_history_cell.go`：`historyCell` 接口（`Kind()` + `DisplayLines(width)`）、
  `historyCellKind` 枚举、首个具体类型 `userMessageCell{source}`（保留原始输入源）。
- 新增协程提交接缝 `commitHistoryCellLocked(cell, gap)` → `writeRowsLocked(cell.DisplayLines(w), gap)`。
- `renderUserEchoLocked` 改为构建并提交 `userMessageCell`，与
  `writeCompleteBlockLocked(FormatUserMessage)` **逐行等价**。
- 验证：`TestUserMessageCell_DisplayLinesMatchLegacyPipeline`（多输入 × 40/80/120/0 宽逐行等价、
  含多行/CJK）+ 编译期接口断言；`RenderSubmittedUserInput*`/`ReplayIgnoresPromptCompensation`/
  `LiveStreamBlankParity`/`MatchesLiveCompleteBlockRendering`/`KeepsToolChainDense` 全绿；
  `commands` 全包 + `go vet` + `gofmt` 全绿。

#### P4.2a ✅ 已完成（2026-07-30，其余一次性块 cell 化）

把剩余的一次性完整块入口迁移到 cell，行为逐行等价：
- 新增 `assistantMessageCell`（持 formatter 渲染后 body）、`supplementLineCell`
  （async/supplement 行）、`asyncDocumentCell`（持 `render.Document`）。
- `RenderAssistant` / `RenderAsyncLine` / `RenderAsyncDocument` 改走
  `commitHistoryCellLocked(...)`；每个 cell 的 `DisplayLines` 与旧
  `writeCompleteBlockLocked(FormatAssistantRendered/FormatAssistantSupplementBlock/RenderDocumentANSI)`
  逐行等价。
- 验证：`TestAssistantMessageCell_*`/`TestSupplementLineCell_*`/`TestAsyncDocumentCell_*`
  多输入 × 多宽度逐行等价 + 编译期接口断言；`Blank`/`LiveStreamBlankParity`/
  `MatchesLiveCompleteBlockRendering`/`KeepsToolChainDense`/`ReplayIgnores*` 全绿；
  `commands` 全包（复跑）+ `go vet` + `gofmt` 全绿。
  （复跑中一次 `TestPromptStartupSessionSelectionWithReader_RetriesAfterInvalidChoice`
  为既有 spinner 计时 flake：隔离 3/3 通过、复跑全绿，与本次逐行等价改动无关。）

#### P4.2b（= P3b，工具链合一）依赖 P5

工具链 Running→…→Completed 合并为**单个可重绘 cell**（Codex `ExecCell`）需要
“Running 暂存于可变 viewport、完成后原地重绘并提交”的保留式视口能力——当前
Running/Completed 都经 `RenderAsyncLine` 立即写入不可变 scrollback。若现在强行缓冲合并，
会**延迟 Running 的实时反馈**（UX 回退）。故工具链合一随 **P5 保留式 viewport** 一起做；
稠密度当前已由 P3a 的 `gapForAsyncLine` + `KeepsToolChainDense` 回归保证，无功能缺口。

#### P4.3 —— 经代码核实并入 P5

读代码核实（`chat_interaction_transcript.go`）后重排：
- `assistantTranscriptBlock` 明确注释：**已写入 scrollback 的行，reflow 永不重写**，块只
  保留 source 范围供 finalization/resize 逻辑推理；`assistantTurnTranscript` 亦注明
  “**终端历史无法重写**”。即当前立即模式下，**resize 无法重排已滚出的可见历史**——这是
  P5 保留式 viewport 才能提供的能力。
- 活动流已有源保留模型 `assistantTurnTranscript`（Source/Blocks/宽度/行）。再引入独立的
  `transcriptCells` 存储会与它及 `session.Messages` **重复**，且当前**无消费者**。

结论：P4.3 的“保留式 cell 存储 + resize 重排”并入 **P5**（届时由 owned viewport 消费
宽度感知的 `DisplayLines`，并与既有 `assistantTurnTranscript` 收敛为单一 cell 模型），
不在缺 P5 时提前搭建投机脚手架。

附（本次顺带清理）：`commitHistoryCellLocked` 原先每次提交都调用全局
`ui.GetTerminalWidth()` 探测，而当前 cell 与宽度无关；改为传 `0` 并注释，去掉热路径上的
无用副作用（`commands` 全包 + `go vet` + `gofmt` 复跑全绿）。

### P5 —— 保留式底部 viewport + 不可变 scrollback（owned viewport 范围终局）

目标：把已抽出的 `ui/vt.Screen` 模型用于生产双缓冲后端；底部区经 buffer diff
增量刷新；历史经单一光标中性 `insertHistoryLines`（DECSTBM 上方腾行）一次性提交，
从而**整体删除**滚动补偿与 gap 两套状态机。

测试：production 与 test 同源 buffer；层级 profile 矩阵；PTY/ConPTY smoke。

风险：最高。需在 P2-P4 收口后单独立项。

**专项设计文档（当前实施真相）**：`docs/plan/aicli-tui-p5-owned-viewport-design.md`
——含目标架构、关键决策（owned viewport / history handoff / cell 统一）、P5.0–P5.7、测试矩阵与删除清单。P5.0–P5.6 已完成；`835386e` 锁定 P5.6 的 tool/event gap 行为；P5.7 待完成 P5 范围旧路径和文档收尾。

**P5 之后的跨模块终局**：`docs/plan/aicli-tui-unified-render-architecture-refactor-plan.md`，负责统一 Scene/Presenter、single physical writer、CommandResult/runtime diagnostics、fullscreen lease、exactly-once handoff 强不变量和全局 legacy 删除。

## 5. 验证与回退策略

- 每阶段必须：`go test ./cmd/aicli/ui/...`、`go test ./cmd/aicli/commands`、
  `go vet`、`gofmt -l` 全绿；变更 byte-exact 断言前先确认是有意变更。
- 强不变量测试（保留并扩展）：layout-neutral、committed-rows-intact、
  live-vs-replay parity、tool-chain denseness、replay-ignores-compensation。
- 保真边界：`screenVT` 只重建可见网格，scrollback 盲区用“等价路径逐行一致”对照式
  断言覆盖；P5 后由生产同源 buffer 消除该盲区。
- 回退开关：P1 `AICLI_DISABLE_SYNC_UPDATE`；P2 保留旧回放路径一版；P3 按块类型灰度；
  P4/P5 立独立特性开关。

## 6. 关键文件索引

- 参考：`codex-rs/tui/src/{tui.rs,custom_terminal.rs,insert_history.rs,history_cell/mod.rs,app/resize_reflow.rs}`
- 当前：
  - `backend/cmd/aicli/ui/{terminal_driver.go,terminal_write_lock.go,fixed_bottom_surface.go}`
  - `backend/cmd/aicli/commands/{chat_interaction.go,chat_history.go,chat_transcript_renderer.go,chat_tool_rendering.go}`
  - 测试：`terminal_write_lock_sync_test.go`、`chat_history_replay_compensation_test.go`、
    `chat_surface_reserve_scroll_invariant_test.go`、`chat_interaction_live_vs_replay_blank_test.go`

## 7. 待决问题

- P2 完整纯提交入口放在 surface 还是 coordinator：倾向 surface（更贴近 Codex
  `insert_history` 的“纯提交”定位），但需评估对 soft-commit / ActiveBand 的耦合。
- P4 是否需要与 `aicli-chat-session-messages-decouple-plan.md` 的数据解耦合并推进。
- ~~P5 行缓冲/后端选型~~：已采用 `ui/vt` 共享 cell/screen 模型，并另立最小
  `viewport.Backend` 双缓冲 diff；当前仍为影子模式。

## 8. 执行顺序

P1 ✅ → P2 user replay 隔离 ✅ / 完整纯提交转统一计划 P1–P2 → P3a ✅ → P4.1 ✅ → P4.2a ✅ →
P5.0–P5.5 ✅（owned viewport、history handoff、cell/width、resize/reflow）→ P5.6 ✅（tool cell 内稠密、独立 final tool/event cell 间单空行、Running viewport-only）→ **P5.7 收尾待推进**。

后续不再在本文扩展新的补偿或 direct-output 特例；统一 Scene/Presenter、single-owner、fullscreen lease、全局 boundary policy、legacy 删除和 PTY/ConPTY 验收按 `aicli-tui-unified-render-architecture-refactor-plan.md` P0–P9 推进。
