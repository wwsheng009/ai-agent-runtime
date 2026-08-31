# aicli TUI owned 渲染简化方案：统一状态、单事件循环与确认式 Presenter

状态：**core render cutover implemented / real TTY verified（compatibility cleanup 继续）**

日期：2026-08-06

上位规范：`docs/plan/aicli-tui-unified-render-architecture-refactor-plan.md`

关联实施子计划：`docs/plan/aicli-tui-transcript-overlay-renderer-mode-plan.md`（明确 primary/alternate renderer ownership、`Ctrl+T` pager 与 history handoff 的边界；不替代本文的 state/effect 收敛）。

> **实施状态更新（2026-08-06）**：本方案中的 direct cutover 已对 interactive TTY 生效。生产 terminal bytes 只经 `TerminalSessionPresenter/TerminalSession`，legacy `FixedBottomSurface` 已不可逆 fence；旧 `AICLI_TUI=legacy/plain/off/0` interactive escape hatch 已删除。`Scene -> AppState` 已承担 assistant active/final、direct local output、MCP/tool final 与 persisted history reconcile，避免旧 history/ActiveBand 的重复生产渲染。finalized plain source 的内部空行与 trailing empty row 已有稳定 `SourceRange/FragmentID`；finalize 只补交未 Ack tail，绝不重放 active 的已 Ack prefix。本文保留的旧链路图与“当前基线”仅用于解释故障根因，不表示它们仍是 interactive production path；删除 compatibility state、审批/问答 effect 化、异常 finalization policy、其余 producer 收敛、跨更多终端与长会话验收仍未完成。

> **inline viewport 终局更正（2026-08-06）**：primary 不再采用 whole-screen `ScreenModel` 重画 transcript tail。所有 finalized display rows，以及 mutable active 中已经稳定且超出 band 的前缀，均由 reducer-owned tokenized `HistoryCommit` 插入物理顶部区域 `1..OutputBottomRow`；该区域顶部锚定，溢出直接进入终端原生 scrollback，最新历史自然保留在当前主界面的顶部区域。scrollback 溢出后 resident history tail 必须 sticky top-aligned，scrollback/viewport 边界不得出现非语义空白断层。`ScreenModel` 只缓存并 diff `OutputBottomRow+1..terminalHeight` 的 bottom inline viewport，其中包含 active tail、prompt、status 和 popup。viewport diff 不能寻址或清除 history region。active Ack 后才从 live projection 扣除前缀，finalize 通过结构化 payload 证明并跳过已交接范围，因此不重复、不裁切；finalized plain source 的内部空行和 trailing empty row 均保留稳定 `SourceRange/FragmentID`，且只补交未 Ack tail。Markdown handoff 保留 rich `render.Line`，不回退 raw Markdown。零字节 writer error 保留同一 token 等待 recovery 后重试；可能部分写入则 fail closed，禁止盲重放。

> **验收证据（2026-08-06）**：`go test ./cmd/aicli/... -count=1`、`go test -race ./cmd/aicli/ui ./cmd/aicli/commands -count=1`、`go vet ./cmd/aicli/ui ./cmd/aicli/commands` 均通过。真实 WezTerm/ConPTY pane 中直接运行 TTY probe，并由宿主 `wezterm cli get-text` 独立读取 scrollback：84 条历史 marker 全部存在且各一次，最早行可从负行 scrollback 读取，prompt/status 未进入 scrollback。`scripts/test-aicli-windows-terminal-e2e.ps1` 通过 UI Automation TextPattern 独立验证 72 条 synthetic 增量历史全部唯一，并验证 committed Markdown 只显示渲染后的 heading/emphasis/code 内容。`scripts/test-aicli-opencode-windows-terminal-e2e.ps1` 使用真实 opencode.ai provider，并由 CLI `--prompt` 自动提交首轮而非键盘注入；40/40 响应 marker 在宿主 `DocumentRange` 中各一次，首行已滚出可见区但仍在 scrollback，末行可见。chat artifact 中唯一 reasoning summary 的完整非空白内容已与宿主文档交叉匹配并位于首个 final marker 前；未出现的是 raw runtime event 标签。`/exit` 与 runner exit code 0 均获确认。本文后续任何“whole-screen renderer”“retained transcript tail 由 ScreenModel 重画”或“TerminalSession 未安装”的段落均只属于历史施工记录，不再是实施依据。

> **最终实机证据（2026-08-06）**：真实 provider 结论由 run `output/aicli-terminal-e2e/opencode-wt-7347d2bf0b0346c1ba975085b3c8b2eb/manifest.json` 固化。稳定 UIA `DocumentRange`/`VisibleRanges` 均已采集；40/40 marker exactly-once、严格递增且连续，marker 01 `document=true/visible=false`、marker 40 visible；唯一 reasoning artifact 共 810 字符，其 SHA-256、宿主投影 exactly-once 结果和相对 final marker 的顺序均已写入 manifest；`assistant.reasoning`、`llm.request.started`、`llm.request.finished` 三个 raw runtime event label 均不存在，异常空行数 0，路由 artifact 完整，三次 UIA snapshot 稳定。测试 harness 使用 executable path + run ID 精确定位 aicli 子进程，并通过独立 `AttachConsole+WriteConsoleInputW` helper 写 `/exit`；helper/runner code 均为 0、forced cleanup 为 0，失败时不放宽退出门禁。该通过 run 不免除 compatibility cleanup、更多终端宿主或长会话门禁。

> **补充根因与永久约束（2026-08-06）**：D1-D9/N1-N6 的设计还需要两个实现级不变量才能真正闭合。其一，每个需要交接的 display row（包括内部空行和尾随空行）必须具有非空、可比较的 source/fragment identity，否则 `Acked` prefix 后的 finalized suffix 可能被整体漏交；plain source 现以 newline byte range 和 fragment ID 表达这些空行。其二，一旦已有语义 history 进入 native scrollback，resident tail 必须 sticky top-aligned；capacity 变化不能重新 bottom-align，否则会在 scrollback 与主屏 tail 之间制造非语义空白。resize rebuild、partial/zero write 和 alternate-screen 往返分别有明确的 alignment/projection 状态转换与测试。

> **成功终态的事件顺序（2026-08-06）**：`llm.request.finished` 是 transport boundary，不是 assistant semantic final。成功请求必须保持 active stream，直到 authoritative `assistant_message` 完成 Scene finalization 和未 Ack tail 的 history handoff；失败 request、interrupt、session end 与 run-end fallback 才可提前关闭。禁止再以 request-finished 替代最终消息，否则最后一段 coalesced assistant 内容可能留在 active viewport 而没有进入 native history。

## 0. 文档定位与结论

本文处理 owned viewport 中的重复 history handoff、ActiveBand grow/shrink 空白、状态源分裂和多调度器竞争。本文是统一架构母计划的迁移子计划，不独立定义终局；与母计划冲突时，以母计划的不变量和状态模型为准。

结论：当前问题不是某一个 `commitExcessHistoryToScrollbackLocked` 调用时机错误，而是同一语义范围被 coordinator、surface、ScreenModel 和 native scrollback 的多个局部状态机同时判定为“尚未输出”或“已经输出”。正确修复采用：

1. 一个 `UIController` actor 串行处理全部 UI action；
2. 一个 retained `AppState` 保存 transcript、active cell 和 bottom pane；
3. 一个 tokenized `HistoryEffectQueue` 管理 native scrollback 提交；
4. 一个 pure Layout/Compose 从不可变 snapshot 生成 frame；
5. 一个 transactional Presenter 独占终端，并以 Ack/Fail action 回报结果；
6. `TerminalProjectionState` 显式区分 `Known/Unknown`，失败后从 AppState 恢复。

**主界面历史契约**：normal primary 必须形成一条连续物理信息流。最新 finalized rows 保留在顶部 history region，较早 rows 由同一区域滚入 native scrollback；主界面不是只显示当前 assistant/reasoning 的单屏画布。`ActiveBand` 只能使用 bottom inline viewport，不能覆盖、清除或替换 history region。`Ctrl+T`/`/history` 是完整 transcript 的补充 pager，不得作为主屏历史缺失的替代方案。首次/不连续 history reconcile 必须先在内存中准备有序 commit batch，再在同一 terminal transaction 中执行 viewport boundary transition、顶部 history insert、bottom viewport diff 和 cursor restore；禁止重新用 whole-screen frame 绘制 finalized transcript。

本方案明确否决以下旧候选：

- 全局 `Frame mode -> Scrollback mode`；
- 无单位的 `committedBoundary int`；
- “写入 scrollback 后应用不再保留 semantic source”；
- band 几何变化触发 history commit；
- 依赖 RI/SD 从 native scrollback 拉回；
- 以 `ScreenModel` 或物理终端作为业务真相源；
- 以字符串出现次数作为 exactly-once 身份。

## 1. 当前基线与故障证据

基线提交记录为 `135a072`。当前工作树含 ScreenModel/VT 实验改动，因此精确重复次数可能变化，但结构性故障仍存在。

### 1.1 专项测试

| 测试 | 当前工作树结果 | 语义 |
| --- | --- | --- |
| `TestFixedBottomSurface_BandShrinkRestoreNeverReplaysHandedOffHistory` | **失败** | 同一 handoff range 被重复提交或重新进入 history 路径 |
| `TestBottomReserveShrinkRestoresHistoryWithoutBlankingTop` | **通过** | 实验性模型滚动已改善 shrink 顶部空白，但未解决重复 handoff |

最近一次实测 frontier 为 `17 -> 17 -> 36 -> 36`；`line-36..39` 仍出现 4 次、`line-40..41` 出现 3 次，多行出现 2 次。测试 2 转绿不能证明 owned rendering 已完成，只证明一个几何症状得到局部缓解。

### 1.2 当前重复链路

```text
WriteOutput
  -> appendHistoryWindowLocked
  -> commitExcessHistoryToScrollbackLocked
  -> appendOwnedDirectPaintLocked
  -> stageOwnedFrameLocked
  -> CommitRange / Flush
```

几何变化还会触发：

```text
Set/Clear ActiveBand 或 popup/prompt
  -> applyOwnedViewportGeometryLocked
  -> commitExcessHistoryToScrollbackLocked
  -> Invalidate
  -> renderOwnedViewportLocked
  -> 再次 commitExcess + full-frame diff
```

同一内容因此可能先作为 history insertion 写入，再作为 viewport diff 重画，随后又在窄 scroll region 中重复提交。

### 1.3 多层提交游标

当前至少存在以下相互独立的进度判断：

- runtime bridge 的 event sequence、final/delta digest；
- coordinator 的 `streamRenderedPrefixLen`、`streamEnqueuedPrefixLen`；
- ActiveStreamController 的 stable queue/visible tail；
- assistant transcript 的 emitted/enqueued source range；
- soft output source range；
- surface 的 `historyWindow`、`handoffFrontier`；
- ScreenModel front/back；
- native terminal scrollback。

这些状态并非都应删除。合法的 streaming/effect cursor 必须保留，但每一种副作用只能有一个 owner，并使用明确的 source/display range。问题是多个层重复维护同一进度，而不是“存在计数器”本身。

## 2. Codex 对照与可迁移原则

Codex 的关键结构为：

- `App` 在一个 `select!` 事件循环内串行处理 app event、terminal input、thread event 和 server event；
- widget 只请求 draw，`FrameRequester` 合并请求，不执行任意业务 callback；
- `transcript_cells` 保存 semantic history，active cell 保存 mutable tail；
- finalized history 先进入 `pending_history_lines`，再在一次 synchronized terminal update 中、viewport draw 之前统一 flush；
- `custom_terminal` 的 diff buffer 只覆盖 `viewport_area`；
- resize/reflow 从 transcript source 重建，而不是从旧物理行猜 source；
- streaming controller 同时区分 `enqueued_stable_len` 和 `emitted_stable_len`，避免排队未确认范围又出现在 mutable tail。

“写完即忘”只适用于一次性的 display effect/batch，不适用于 semantic history。native scrollback 是输出副本，不是恢复数据库。

迁移时采用以下原则：

1. 语义身份使用 `CellID/Revision/SourceRange`；
2. layout 身份增加 `LayoutGeneration/DisplayRange`；
3. terminal effect 使用唯一 `CommitToken`；
4. 只有完整写成功后的 Ack 才推进 projection cursor；
5. repaint 可以合法重发字节，但不得产生第二个已确认 handoff record；
6. geometry change 只修改 AppState/layout，不创建 history commit。

## 3. 目标状态与所有权

```text
Runtime/Input/Resize/Timer/Lease/EffectResult
                    |
               UIAction mailbox
                    |
          UIController / Reducer
             | AppState snapshot
             | TerminalEffect queue
             | DrawRequested
                    |
             Layout / Compose
                    |
       FramePlan + CursorPlan + EffectPlan
                    |
      TerminalSession / Presenter transaction
                    |
             Ack/Fail UIAction
```

### 3.1 状态分层

| 状态 | 权威内容 | 不得包含 |
| --- | --- | --- |
| `TranscriptState` | semantic finalized cells、ID、revision、boundary | ANSI、terminal row、front buffer |
| `ActiveCellState` | mutable source、stable source range、stream phase | 独立的最终 transcript 副本 |
| `BottomPaneState` | status、prompt、popup、focus、ActiveBand projection inputs | history handoff frontier |
| `GeometryState` | terminal size、viewport rect、layout generation | 业务文本 |
| `TerminalProjectionState` | front/back、cursor、Known/Unknown | semantic truth |
| `HistoryEffectQueue` | Pending/InFlight/Acked commit records | 无身份的文本行计数 |

ActiveBand 是 active cell/tool progress 的布局投影，不是第二个正文 buffer。Prompt/status/popup 不进入 transcript，但必须和 transcript 一起由同一个 snapshot 布局。

### 3.2 生命周期

```text
Semantic cell: Mutable -> Finalized
Display range: Retained -> CommitPending -> InFlight -> AckedHandedOff
```

每个 turn 都可产生新的 mutable cell，因此不存在应用级单调 Frame/Scrollback mode。单调性只属于具体 `CommitToken` 和 display/source range。

### 3.3 History effect 契约

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

`Lines` 是本次 effect 的不可变 payload，可在 Ack 后释放；semantic source 仍由 transcript/session model 保留。Presenter 不根据文本相等去重，而根据 token 和 range 执行/确认。

Presenter transaction 顺序固定为：

```text
verify lease + terminal generation
  -> apply viewport geometry
  -> flush eligible history effects
  -> update viewport area
  -> compose/diff viewport exactly once
  -> restore cursor
  -> commit front buffer
  -> emit Ack actions
```

任何短写或失败：不 Ack effect，不推进 projection；front 标记 `Unknown`；下一次通过 recovery barrier/full repaint 恢复。`Invalidate` 从正常补偿手段退回明确的失败、resize generation 或 lease 生命周期恢复手段。

### 3.4 Geometry 契约

ActiveBand/prompt/popup/status 的行数变化只产生 `GeometryChanged`/state mutation。Layout 从 snapshot 重新分配 viewport rows。Presenter 可以把某些 frame diff 优化为 terminal scroll sequence，但该优化是物理实现细节：

- 不产生 semantic/history commit；
- 不推进 handoff；
- 不要求从 native scrollback 拉回；
- 优化失败可退化为 full repaint；
- 屏幕正确性以最终 frame 和 Known projection 为准。

## 4. 强不变量

| ID | 内容 |
| --- | --- |
| INV-O1 | 只有 UIController reducer 能修改 AppState |
| INV-O2 | 只有 Presenter/TerminalSession 能写 ANSI/terminal bytes |
| INV-O3 | frame 只消费一个不可变 AppState + geometry generation |
| INV-O4 | finalized cell 不通过 append 第二份 final 文本完成 |
| INV-O5 | 同一 `Token + CellID + Revision + Range` 最多 Ack 一次 |
| INV-O6 | geometry mutation 不创建 HistoryCommit |
| INV-O7 | front 只在完整 flush 成功后更新；失败转 Unknown |
| INV-O8 | semantic source 不从 terminal、VT snapshot 或 front buffer 反推 |
| INV-O9 | streaming 的 queued/acked range 只由一个 controller/effect owner 维护 |
| INV-O10 | popup/prompt/status/ActiveBand 更新不改变 transcript sequence/boundary |

不把“相同文本字节只出现一次”列为不变量。原位 repaint、resize source replay 可以合法重发文本；测试必须断言 semantic identity、handoff token 和最终屏幕序列。

## 5. 改动范围

### 5.1 删除或退役

- 正常路径中的 `commitExcessHistoryToScrollbackLocked`；
- `historyWindowHeadroom` 和以固定行数猜恢复能力的策略；
- `handoffFrontier.TrimPrefix/Clamp` 的文本数组对账语义；
- geometry setter 内的 history commit、直接 render 和独立 flush；
- `appendOwnedDirectPaintLocked` 与 full-frame path 的双重提交判断；
- `CommitRange` 对未知 terminal side effect 的掩盖式补偿；
- surface/coordinator 中重复的 soft tail、rendered prefix 和 transcript range；
- FramePump 按 key 执行业务 callback 的能力；
- production legacy renderer、raw writer 和临时 Scene fallback（最终阶段）。

### 5.2 新增或收敛

- `UIAction`、bounded mailbox、`UIController` reducer；
- 完整 `AppState`：transcript/active/bottom/geometry/lease；
- pure `Layout(snapshot)` 和统一 BottomPane allocation；
- `HistoryCommit` effect queue 与 Ack/Fail action；
- `TerminalProjectionState{Known, Unknown}`；
- viewport-sized Presenter/ScreenModel；
- typed streaming source ranges；
- reducer/model property tests 和 terminal effect fault injection。

## 6. 迁移阶段

每阶段允许拆成多个小提交；阶段出口必须保持 production 只有一个 terminal writer。shadow compare 只比较内存 snapshot/frame，禁止双写终端。

以下状态表是 2026-08-06 的权威 phase disposition；各 Phase 下保留的 `partial`、`未安装`、`legacy writer` 描述是当时施工快照，不能覆盖本表。

| Phase | 当前状态 | 判定 |
| --- | --- | --- |
| 0 语义基线与故障注入 | **核心完成，门禁持续扩展** | 重复、空行、final tail、scrollback continuity、writer failure 和生产链路测试已覆盖；长会话/更多宿主矩阵继续补充 |
| 1 UI actor 单一入口 | **核心完成，interaction cleanup 继续** | primary render/effect 已串行化；approval/question 等同步 legacy interaction 仍待 typed effect 化 |
| 2 AppState/Scene | **primary 核心完成，producer cleanup 继续** | assistant/reasoning/tool/history 与 bottom state 已进入统一 snapshot；剩余 raw producer 和 compatibility mirror 待删除 |
| 3 TerminalSession | **核心完成** | production primary 已切换到唯一 Presenter/TerminalSession writer，Known/Unknown 与 transaction recovery 已落地 |
| 4 tokenized history handoff | **核心完成** | active/final history 以 typed token/range Ack，内部空行和 top-aligned resident tail 已闭合 |
| 5 streaming 单源化 | **核心路径完成，旧状态删除继续** | authoritative final、未 Ack tail、Markdown IR 和 exactly-once 已验证；平行 compatibility cursor/state 仍须收敛 |
| 6 删除与验收 | **进行中** | 完整 Go/race/vet 与真实 Windows Terminal/provider 已通过；legacy 算法删除、更多终端宿主及长会话组合矩阵未完成 |

### Phase 0：语义基线与故障注入

- 将两个专项测试改为 `CellID/CommitToken/Range` 语义断言；字符串计数仅保留诊断；
- VT 模型记录 terminal effect 和推出行，但不模拟“从 scrollback 拉回”作为正确性前提；
- 增加 short write、writer error、retry、duplicate Ack、stale generation 测试；
- CI hard gate 纳入 `go test ./cmd/aicli/ui/... -count=1`。

出口：测试能区分 repaint、source replay 和重复 handoff；当前 bug 仍红且原因明确。

### Phase 1：建立 UI actor 单一入口

- 定义 `UIAction`、mailbox、priority/barrier；
- RuntimeEvent、Input、Resize、Timer、Lease、FrameRequested 全部投递 action；
- FramePump callback 只投递 action；
- surface setters 变成 adapter，不直接 render/flush。

**实际进度（2026-08-03，Phase 1 partial）**

- `UIController` 已提供 bounded mailbox、revision、coalescing、barrier、shutdown 和 starvation 覆盖；普通 runtime event 通过 bridge 的有界 ingress 转为 `RuntimeEvent` action，editor snapshot 以带单调 `Sequence` 的完整 `InputEvent` 进入 actor。draft cache 由独立 `promptInputMu` 保护，逐键输入不取得 coordinator 的 render/terminal `c.mu`；生产 sequence action latest-wins 合并，邮箱满时 coordinator 保留最后一个 deferred snapshot 并由单一 retry 投递，因此逐键输入不等待 actor drain、mailbox 容量或旧渲染临界区；`Sequence=0` 的 legacy/test action 仍保持 durable FIFO。显式 active-stream refresh 经 `Resize` barrier。
- FramePump 的 dynamic-status、stable-commit、prompt callback 只生产 `Timer`；active-stream callback 只生产 coalescable `DrawRequested`。它们的 legacy adapter 绘制仍在 reducer 中发生，尚未被 Presenter transaction 替代。
- surface facade 已接入 action；`ScreenLease` acquire/release 成功后投递 Lease barrier，`EffectResult` 有 reducer 入口。lease transport、effect token、Ack/Fail、Known/Unknown recovery 仍属于后续阶段。
- reducer 内触发的 surface facade 使用 controller causal follow-up queue，在当前 action 后、下一外部 action 前获取独立 revision；该队列不消耗 bounded external mailbox 槽位，避免满 mailbox 下 reducer 自投递等待。新 reducer 通过 `ReducerContext` 的短生命周期 capability 投递；尚未迁移的 facade adapter 也必须校验实际 reducer 执行上下文，绝不能以全局 `inFlight` 判断替外部 producer 开后门。它是 legacy adapter 的重入安全措施，不是 presenter/effect queue。
- `approval.requested` 与 `question.asked` 继续走 bridge worker 的同步交互路径，避免在 actor mailbox 内阻塞 stdin；完成条件是 effect/result 化后删除该受控例外，而不是仅把原函数搬进 reducer。
- 直接 writer 在 `ClearPrompt -> WriteOutput` 交接处使用 actor idle barrier，避免 prompt reserve 尚未清除时把输出投影到旧 geometry。这是 adapter 期兼容措施，不是终局 writer API。

出口：AppState mutation 和 presenter sequencing 只有一个 goroutine owner；现有视觉行为保持。

### Phase 2：统一 AppState/Scene

- 扩展当前 transcript-only `TuiScene`，或引入包含它的 `AppState`；
- 建立 `ActiveCellState`，ActiveBand 改为派生 projection；
- prompt/status/popup/focus/geometry/lease 进入同一 snapshot；
- `FlushPolicy` 真正驱动统一 frame scheduler。

**历史实施记录（2026-08-04，Phase 2 partial）**

> 本节记录 direct cutover 之前的实施快照。2026-08-05 起 interactive TTY 的当前所有权以本文开头的实施状态更新和母计划 §15.7/§15.8 为准：`TerminalSessionPresenter` 已是唯一 primary writer；下列“shadow”“未安装”和 legacy writer 表述不再描述当前 production path。

- `ui.AppState` 和 `UIController.AppState()` 已提供深拷贝 snapshot；controller transition state 嵌入该 AppState，geometry/lease 不再在 transition ledger 之外保存第二份字段。
- runtime Scene snapshot 在普通 `RuntimeEvent` reducer 完成 ChangeSet mapping 后，经 causal `ReplaceTranscriptAction` 进入 actor；mutable Scene cell 只派生 active semantic `CellID/Revision/Source`，不猜测 stable/enqueued/acked range。
- live user submit、structured command result、local error 三类直接 Scene injection 也在 legacy coordinator 完成写入并释放 `c.mu` 后投递 `ReplaceTranscriptAction`，所以背压不会与 reducer 的 coordinator lock 形成等待，AppState 可追上这些非 runtime transcript producer；事件日志 `replayEventLog` 在成功重建 Scene 后也投递一次完整 snapshot，避免 AppState 保留重放前 transcript。
- direct supplement 与 direct assistant fallback 的第一批 local producer 已收口到独立 typed path：`EventEncoder.SubmitSupplement` 产生 completed `KindSupplement`，`SubmitAssistant` 产生 completed `KindAssistant`，均经 mapper 进入 Scene，并以对应 `eventLogInjection` 字段保持 live/replay 全序一致。`RenderLocalSupplement`/`RenderLocalAssistant` 在 legacy write 后、coordinator 解锁后才发布 snapshot；输入拒绝、busy queue、ESC、goal/retry、provider retry、日志保存失败、direct invocation、live transcript supplement 及 legacy non-stream response fallback 已接线，history replay 明确不注入。已由 runtime bridge 先 `Encode` 的 timeline/assistant 继续走 projection-only `RenderAsyncLine`/`RenderAssistant`，测试禁止它们再追加 Scene cell，避免本项目最关键的 runtime event/direct output 双重渲染回归。chat-core direct tool 的 request/result 现走 `SubmitToolCall`/`SubmitToolResult`：有稳定 `tool_call_id` 时只维护一个 mutable `KindToolChain`，running 仅留在 legacy ActiveBand，result 与 output 合并为一个 committed Scene chain；桥接层以等价 typed runtime event 持久化，event-log replay 可重建相同 chain。历史重放改走 projection-only `RenderReplayedToolChainEvent`，不会把已有 tool history 再注入 Scene；没有稳定 ID 的 direct tool 保留 legacy projection fallback，不能猜测 chain 归属。approval transcript 与 raw system-output 仍是明确待审计边界；本切片不改变 `FixedBottomSurface` 的 writer 所有权。
- 已 action 化的 input/status/prompt/popup/legacy ActiveBand facade 同步更新 `AppState.Bottom`。本轮将 prompt reset/rows/notice/editor、incremental input tracking、单独 persistent/dynamic status 与 composer preview 一并接入；`Apply` 与同步 facade 的终端字节 parity、以及 AppState popup-preservation/snapshot-detach 回归均已覆盖。popup owner/priority、suspended `PopupStack` 和 tokenized `PopupHandle` 的 begin/update/clear 已有同一份 pure `BottomPaneState` transition；handle 在 facade 边界分配后以 durable begin action 入队，保证同 token 的后续操作 FIFO 排在其后，snapshot 对 stack 深拷贝，`UpdatePopupAction` 也已由 coordinator reducer 应用。该切片仍只使 AppState 追踪 legacy surface adapter，不能将其误读为 popup/presenter owner 已收敛。ActiveBand 仍是迁移期 projection input，不是 final semantic active owner；纯 Layout production compose、全 producer 覆盖、同步 cursor helper/完整 focus policy、Presenter 与 parity 尚未完成。
- 为收束 ActiveBand 的正文所有权，`ActiveCellState` 已增加 Scene `Kind`，且纯 `ProjectActiveCellBand(ActiveCellState, GeometryState)` 会从 `Acked.End` 之后的 source 后缀生成 bounded structured lines。未 Ack 的 queued range 会保留在 band，避免 handoff 尚未成功时出现可见空洞；Ack range 必须是 UTF-8 rune 边界上的 `[0,end)` prefix。`LayoutAppState` 仅在无 legacy facade band 时采用此投影，legacy band 存在则保持唯一且优先的显示来源。因此这一切片为新 primary Compose 提供 source-backed fallback，却不改变当前 legacy terminal writer，也不允许 ActiveStream/legacy band 与纯投影并排渲染同一正文。
- Phase 5 data-plane 已增加可合并的 `UpdateActiveCellAction`。它以 `ExpectedCellID/ExpectedRevision` 作为因果栅栏，携带完整 source 与 `Stable/Enqueued/Acked` range；reducer 拒绝乱序/越界/非 UTF-8 边界，保持 `acked <= enqueued <= stable <= source`，source correction 时清除旧范围。当前仅完成 typed reducer seam 与测试，尚未连接 `ActiveStreamController`、Scene producer 或 production presenter。
- Phase 5 第二个 data-plane slice 已补齐 `ActiveStreamController.SourceSnapshot()` 只读 source bridge，以及 `AdvanceActiveSource`、`MarkActiveEnqueued`、`MarkActiveAcked` 纯 range transition。它把 semantic source 和 effect progress 的字节边界显式分离：tool display 不能伪装成 transcript source，queued-but-unacked suffix 在 physical Ack 前仍留在 ActiveBand；source correction、delta/final 同 revision、回退 Ack 和非 UTF-8 边界均拒绝或清除旧范围。测试覆盖 live/finalize/cancel/tool、coalescing、短写语义与 race。该 slice 只是未来 coordinator/Scene adapter 的只读迁移契约，未接 production writer，也未替换 `streamRenderedPrefixLen`、`streamEnqueuedPrefixLen`、`stableCommitQueue`、`softEmitted*`、legacy `historyWindow` 或 `FixedBottomSurface`。
- 随后的纯 mapping slice 增加 `ActiveCellStateFromSourceSnapshot`：显式接收 `CellID + Revision + EnqueuedEnd + AckedEnd`，只映射 semantic kind；不会把 `ActiveStreamSourceSnapshot.CommittedEnd` 猜成物理 Ack，tool/status display 也不能成为 transcript source。它为后续 coordinator adapter 建立身份/range/UTF-8 校验边界，目前仍没有 production 调用点。
- 受控 adapter 已接入 `RenderAssistantDelta` 的 shadow 数据面：只有已 mounted 的 Scene active cell 才生成 `UpdateActiveCellAction`，action 在释放 coordinator mutex 后进入 UI actor，保持 `CellID + ExpectedRevision` fence；legacy ActiveBand/surface 输出不变，tool display 不进入 `ActiveCellState.Source`。该接线只验证 AppState mirror 和 producer unlock 顺序，尚未让 Layout/Presenter 使用它，也没有删除任何 legacy streaming/handoff 游标。
- 该 adapter 已扩展到 tool-running stage 的 begin/progress/display/finish 生命周期；running tool 仅镜像 semantic `KindToolChain` 和空 source，finish 通过 `ClearActiveCellAction` 清理 shadow active。它仍是 AppState shadow，不是 tool display 的 transcript owner，也没有让 Layout/Presenter 接管 ActiveBand。
- reasoning delta/finalize 也已接入同一 shadow bridge：reasoning 作为 `KindSupplement` source，`CompleteReasoningResponse` 与 `FinalizeReasoningDelta` 都在 coordinator 解锁后以 `CellID + Kind` fence 清理属于自身 identity 的 shadow active；清理允许同一 semantic cell 的 queued source update 先到达，但不会清空不同 cell/kind 的 delayed completion；legacy reasoning writer 保持不变，尚未纳入 production Layout/Presenter。
- assistant finalization shadow 已接入 `CompleteAssistantResponse` 与 `FinalizeAssistantDelta`：在 legacy stream reset 前，adapter 只接受已 committed 的 immutable Scene snapshot，并以 `ExpectedActiveCellID + ExpectedActiveRevision + ExpectedActiveKind + ExpectedActiveKindKnown=true` 构造 `FinalizeActiveCellAction`。reducer 在同一 action 中替换 final transcript snapshot 并清除匹配 active，拒绝 stale active、语义 kind 不匹配或 mutable Scene snapshot；`CellKind` 零值为合法 `KindUser`，未设置 kind fence 必须显式以 `Known=false` 表示。shadow update 与 Scene final 可占用相同 revision，故接受 final Scene revision `>=` active fence，不能错误要求 `>`。这消除了 AppState shadow 中的 final append + clear 双 action，但它不写 terminal，runtime 的完整 `ReplaceTranscriptAction` 仍作为数据同步兜底，legacy final writer/HistoryCommit/Presenter 仍是后续工作。
- `ReplaceTranscriptAction` 现对同一 mutable `CellID/Kind/Source/Revision` 做 ledger-preserving merge：Scene snapshot 继续负责 semantic transcript，actor 已确认的 `Stable/Enqueued/Acked` 只在 source 前进、finalize 或 active cell 切换时替换/清除。连续 runtime delta 的集成回归验证 shadow range 不会被下一次 snapshot 退回零值；该合并规则不授予 shadow presenter 终端写权限。
- tool-stage shadow 的 `SetAgentStageDetail`、`SetToolAgentStage`、`SetToolAgentStageDisplay`、`FinishToolAgentStage` 也统一进入 causal follow-up；它们在 runtime reducer 内生成的 AppState mirror 不再等待 bounded external mailbox，且不改变 legacy ActiveBand 的 production writer。
- Ctrl+T transcript pager 的 view state 已接入 actor authority：生产 pager loop 从一次 `TranscriptPagerView` 回读当前 lease 对应的 `AppState.Transcript`、`Active` 与 `TranscriptOverlay.Pager`，正文与锚点不会跨 AppState revision 混配；按键只提交带 `LeaseID` 的 scroll/follow action，reducer 拒绝旧 lease 的延迟输入。pager loop 还会检测 actor 已发布的 pager state 变化，因此异步 reducer 消费 input 后会主动重绘，而非等待下一次按键或 transcript 更新；若 actor view 已配置而 action poster 不可用，输入保持只读，绝不回退到本地 scroll mutation。pager snapshot 在打开前先发布 Scene 到 actor，运行中不再把 Scene snapshot 与独立 local scroll state 混合作为 production source；local state 仅保留给无 actor 的测试/兼容 loop。该切片只完成 alternate renderer 的状态 ownership，不接 `TerminalSession`，也不改变 `FixedBottomSurface` 的 primary writer。
- `LayoutAppState` 已从一个 immutable AppState 派生 Scene boundary rows 和 bottom pane row allocation/cursor intent，且无 terminal I/O、mutex read 或 effect 推进。本轮新增纯 `BottomPaneGeometryPolicy`：ActiveBand budget/top gap、margin、popup budget 与 prompt viewport 都从 `BottomPaneState + GeometryState` 推导；prompt 保存 logical cursor/absolute visual row/total rows/viewport start，并在 resize 时由 source 重新测量，不读取 legacy cursor cache。`LayoutBottomPaneRows` 已输出 bottom reserve 的 plain text/owner plan，并对普通/多行 prompt、popup/composer、短终端压力、窄宽/宽屏和同一 semantic state 的 resize cycle 与 legacy snapshot 建立 parity；新增 `LayoutAppScreen` 纯 screen-row shadow，将 retained transcript 的 cell identity/gap 与 bottom overlay 放入同一 viewport-sized plain layout，并明确排除 mutable cell 以避免 active/band 重复。legacy owner annotation 覆盖多行实际 prompt 文本，却不把 popup 的空输入 gap 误标为 Prompt。legacy surface probe 的已测宽高经 `Resize{Applied:true}` barrier 回投 actor；只有实际 geometry 改变才推进 generation，回投本身不形成二次 probe/reflow。该 shadow 尚未形成 physical Compose、full-screen legacy text/owner parity 或 Presenter diff。

- `LayoutAppScreen` 现有 full-screen in-memory owner/text matrix：semantic boundary gap 在物理 owner 上为 Transcript，并以 `TranscriptGap + CellID` 记录语义身份；transcript row expansion 复用纯 `vt.Screen`，因此 deferred wrap、宽字符、leading combining mark、tab stop 和 SGR 不再另写一套近似宽度算法。矩阵使用等价 legacy logical history source，覆盖 popup 空输入 gap、多行 prompt/popup、短屏、超长 tail 与 geometry cycle；一行 terminal 的 output boundary 为 0。`BottomPaneRowPlan` 暴露 prompt input start/rows，使 cursor intent 不会选择 notice/editor context 行。它不读终端或 surface 状态，故不覆盖 cursor-parking、handoff/scrollback、lease/短写，仍不是 physical Compose 或 production full-frame parity。

出口：coordinator/surface 不再各自保存可独立推进的 UI 正文和 bottom component 状态。

### Phase 3：TerminalSession 与 effect queue

> 以下是 direct cutover 前的 Phase 3 实施快照。当前 interactive TTY 中，`TerminalSessionPresenter/TerminalSessionExecutor` 已接入 runtime，独占 primary terminal transaction；`FixedBottomSurface` 的 physical write 与 legacy history handoff 已被 fence。保留本段是为了说明 effect queue、Known/Unknown、recovery 和 tokenized history 的演化约束，而不是把 `TerminalSession` 重新降级为“未安装”的 shadow seam。其余 compatibility state、完整 producer 收敛和 legacy 删除仍是后续硬门禁。

当前已完成 effect queue 的 reducer/data-plane、注入 sink executor，以及一个未安装的 `TerminalSession` viewport/history-sink seam。`ComposeAppRenderFrame` 先从 AppState 生成 rich `render.Line` row（transcript kind role、typed ActiveBand、status document）并与 `AppScreenRow.Text` 做逐行 plain parity；`ComposeTerminalFramePlan` 同时保留两份投影，session 拒绝 rich/text 不一致的 frame。该 session 已覆盖 private front/back projection、Known/Unknown、generation、lease defer/release full repaint、cursor、explicit ThemeContext/profile transition 和 short-write/panic recovery；它可在已确认的同 generation、未 resize、非 lease primary 上通过 `TerminalTransactionPlan` 把结构化 `HistoryCommit.Lines` 经共同 `ANSIBackend + HandoffPlan`、viewport diff 与 cursor 合成为一次 target write，成功后镜像物理 scroll append；Unknown/resize/lease 时 history 严格 Deferred、frame 仅完成 recovery。未安装的 `TerminalSessionExecutor` 已把 controller claim -> immutable snapshot -> transaction -> Deferred/Ack/Failed + projection invalidated/recovered 的因果回投固定为 worker seam，覆盖 recovery 后再 handoff，仍不接 live runtime。失败/in-flight invalidation 后，`HistoryScrollbackReconciled` 只接受由 future terminal owner 在 scrollback reset/replacement 与确认恢复后发出的单调 epoch，并据此丢弃旧 ledger、重新从 semantic source mint token；普通 repaint 仍不能跳过未知 delivery。隔离 `HistoryCommitSink` 仍只作单 effect fault seam。但它未接 runtime/`FixedBottomSurface`，production executor、single-writer 切换与 legacy handoff 删除仍是硬门禁，不能因这些类型和测试已存在而跳过。

- Presenter buffer 尺寸收敛为 viewport area；
- 引入 Known/Unknown projection、cursor plan、generation barrier；
- HistoryCommit 只入队，不在 reducer 内同步推进 handoff；
- 一次 terminal transaction 执行 effects + viewport diff + cursor；结果返回 Ack/Fail action。

出口：短写不推进 front/handoff；失败后可从 AppState full repaint。

### Phase 4：tokenized history handoff

- finalized eligible range 生成唯一 CommitToken；
- 删除 geometry path 的 commitExcess；
- 删除 headroom/frontier 文本数组补偿；
- band grow/shrink 仅 layout/recompose；
- Ack 后释放 effect payload，semantic source 仍可 replay。

出口：两个专项测试通过；同一 token/range 最多一个 Ack record。

### Phase 5：streaming 单源化

- 合并 stable/tail、assistant transcript 和 soft output 的平行游标；
- 保留一个类型明确的 raw source + enqueued/acked range controller；
- finalization 在一个 reducer transaction 中完成 mutable -> finalized、clear active projection、enqueue eligible effect；
- live/replay/resize 共用同一 `DisplayLines`/Layout。

出口：用户可见正文来自 Scene/AppState snapshot；不存在 final append + clear active 的双路径。

### Phase 6：删除与验收

- 删除 legacy immediate renderer、raw output adapter、旧 flags 和诊断 `zz_` 文件；
- 删除 `historyWindow/headroom/frontier/commitExcess` 与重复 prefix/source range；
- 完成 Windows Terminal/ConPTY 人工验收和非 Windows PTY 验收；
- 完成 race、benchmark 和 resize/lease/popup 组合矩阵。

出口：满足母计划 P9 和本文 §8 DoD。

## 7. 测试与风险矩阵

| 维度 | 必须验证 |
| --- | --- |
| reducer | action replay 确定性、旧 revision、重复 Ack、barrier 顺序 |
| streaming | delta/final race、queued 未 Ack tail、cancel、tool-running、reasoning |
| geometry | band/prompt/popup grow-shrink、短终端、resize storm |
| handoff | exactly-once token、超大 cell 分片、width generation 变化 |
| terminal | short write、writer error、Unknown recovery、cursor、scroll region reset |
| content | CJK/emoji/combining、SGR continuity、Markdown/code/table、长 URL |
| lifecycle | fullscreen lease、resume/backtrack、shutdown/signal、capability degrade |
| concurrency | `go test -race`、持续 status/timer + streaming + resize |
| performance | frame coalescing、Layout CPU/alloc、effect batch、400+ 行输出 |

主要风险及处理：

1. **UI actor 引入延迟**：durable/control/coalescable action 分类，最终仍形成单一 revision 顺序。
2. **effect 重试重复写**：terminal write 无事务回滚能力；失败后 projection Unknown，禁止盲目重放不确定 effect。恢复策略必须区分“明确零写入”和“可能部分写入”，后者执行 controlled rebuild，而不是直接重试同一字节流。
3. **semantic source 内存**：session model 持久化全量语义；TUI Scene 可按 cell identity 保留可重建索引与 bounded retained projection，不能以删除 source 换取错误的终端所有权。
4. **迁移双状态**：adapter 期必须标明权威源；每个领域只能有一个 production writer/owner，shadow 数据不得反向驱动输出。
5. **终端差异**：正确性不依赖 scrollback 拉回；终端滚动只作为 Presenter 可替换优化。

## 8. 完成定义

1. 所有 Runtime/Input/Resize/Timer/Lease 通过 UI mailbox，FramePump 不执行 mutation callback。
2. transcript、active cell、bottom pane、geometry、lease 能从一个 AppState snapshot 完整布局。
3. history effect 具有 Token/CellID/Revision/Range/Generation，只有 Ack 推进 projection。
4. geometry change 不产生 history effect；band grow/shrink 不重复、不丢失、不依赖 native pullback。
5. terminal short write/failure 后 front 为 Unknown，可从 AppState 恢复。
6. streaming 只剩一个 source-range controller；Scene revision 不被误用为 effect progress。
7. 两个专项、ui/commands 全量、race、fault injection 和真实终端清单通过。
8. `historyWindowHeadroom`、`commitExcessHistoryToScrollbackLocked`、旧 compensation 和 production fallback renderer 不可达或已删除。
9. `/debug` 展示 AppState revision、layout generation、projection state、pending/inflight/acked token，不以文本 hash 作为正确性指标。
10. 母计划状态和删除清单同步，P5 保持 historical，不再存在相互竞争的终局文档。

**切片 13 施工记录（chat-core direct tool）**：direct `tool_requested`/`tool_result` 已以稳定 `tool_call_id` 通过 `SubmitToolCall` 与 `SubmitToolResultDisplay` 维护单一 mutable `KindToolChain`。运行态不写 legacy history；最终 Scene cell 使用与 legacy complete block 相同的 `display_head`，raw output/error 只随等价 runtime event 进入 event log，replay 不会再追加输出。history renderer 走 `RenderReplayedToolChainEvent` projection-only，缺 call ID 时不猜测归属。新增 live/replay/single-chain/no-duplicate/text-parity 测试；仍不改变 `FixedBottomSurface`、ActiveBand physical owner、HistoryCommit 或 TerminalSession。

**切片 14 施工记录（TerminalSession transaction preflight gate）**：shadow `TerminalSession` 在 history handoff 已完成内存准备、但 viewport frame 尚未开始 terminal write 即校验失败时返回 `HistoryCommitResult{Deferred:true}`，不再以空 success 触发 Ack；新增回归验证无 bytes 写出、confirmed projection 不被改写、token 保留到后续有效 transaction。该切片仍不连接 `FixedBottomSurface` 或 production presenter。

**切片 15 施工记录（semantic boundary change and history payload rebase）**：history planner 现按非 token 展示字段检测同一 source 的 display range/gap/render-line 变化。pending effect 保留原 token 并 rebase 新 payload；in-flight effect 在旧 payload 可能已写入时 fail-closed invalidation，进入 ProjectionUnknown，禁止旧 Ack 或重复 handoff。新增 pending rebase 与 in-flight invalidation/no-duplicate 测试；该切片只强化 reducer-owned effect ledger/data-plane，不改变 physical writer authority。

**切片 16 施工记录（priority transcript and raw tool-output ownership）**：approval/question request 现在只登记待同步交互的 stable identity，不提前占用 Scene 顺序；同步 prompt 转录完成时才以 request key 追加一个 completed `KindPriorityPrompt`。这样 permission/reuse hint 先于 transcript 出现在 legacy history、Scene 与 AppState；resolved/answered 可早到但不会阻断 late transcript，重复 request/resolution 与 history replay 均不创建第二份 Scene cell。local permission/decision 等没有 runtime event 的输出改走 `RenderLocalSupplement` typed injection，已 Encode runtime event 保持 projection-only。稳定 `tool_call_id` 的 shell raw bytes 已限定为 ActiveBand-only stage projection，completion 保留唯一 normalized tool result cell；actor executor 的顶层 retained `OutputMirror` 已删除，因为 stable runtime `tool.progress` 已是带 call identity 的 live owner。无 call ID/non-owned 兼容路径暂保留 legacy fallback。新增 priority order/race/single-cell/replay、raw-output no-history/limit 和 actor context no-mirror 回归；本切片不改变 `FixedBottomSurface`、TerminalSession 或 production Presenter authority。

**切片 17 施工记录（2026-08-31，legacy 删除与 Phase 6 状态盘点）**：完成 Phase 6 前三项的实质性删除，并逐项核对 §5.1/Phase 6 清单的可行性。

已删除（生产代码 + 关联测试）：

1. 诊断 `zz_` 测试文件 4 个（ui ×2 + commands ×2）。
2. legacy surface binding 链：`render/output/binding.go`（−222）、`binding_test.go`（−282）主体删除，`gateway.go`/`mirror_scheduler.go` 相应瘦身，`FixedBottomSurface.legacyBinding` 字段与 `fixed_bottom_surface.go`/`terminal.go` 的 legacy 分支同步清除。
3. `LegacyTransactionAdapter`/`LegacyImmediateAdapter` 及其适配层引用全部删除（全仓 grep 无匹配）。**勘误（2026-08-31 状态盘点）**：`vt_emulator_adapter.go`（211 行，`VtTerminalEmulator`）与 `render/output/virtual_terminal.go`（349 行，`VirtualTerminalSink`）**并未删除，仍为活跃代码**——它们是 owned 模式观察面的 emulator/sink 基础设施，被 `terminal_session_observe_test.go`、`parity_test.go`、`capture_upgrade_test.go`、`benchmark_test.go`、`testfixture_test.go` 持续引用；本切片早期记录中"全部删除"的表述有误，实际删除范围以本条勘误为准。
4. legacyReserve 渲染序列链整链删除：`appendPendingOutputScrollDownLocked`/`flushPendingOutputScrollDownLocked`/`appendOutputScrollDebtLocked`/`flushOutputScrollDebtLocked`/`markOutputWrittenLocked`/`appendOutputScrollUp|DownForBottomReserveGrowth|ShrinkSequence` 7 个函数、`flushLegacyANSIHoldingLock`，以及 `SettleOutputDebt`/`BeginOutput`/`writeOutput`/`RewriteSoftOutputTail`/`ClearCommittedHistoryForReplay`/`setPromptRowsImpl`/`setPromptNoticeLineImpl`/`repaintActiveBandLocked`/`clearActiveBand`/`repaintStatusUpdateLocked` 中的 legacy-only flush/debt 分支体。`renderengine/legacy_reserve.go` 仅保留 `LegacyReserveState` 4 字段诊断结构，`legacy_reserve_test.go` 删除。
5. `applyLayoutLocked`/`applyLayoutWithSizeLocked`/`appendApplyLayoutSequenceLocked`/`appendApplyLayoutSequenceWithSizeLocked` 的非 owned 路径改为布局簿记（更新 `lastWidth/lastHeight/lastBottomRows` 并清零 legacy 字段），不再 emit DECSTBM/scroll 序列——`SyncTerminalGeometry*` 三测试因此恢复通过。
6. fixed_bottom_surface_test.go 删除 22 个 legacy 渲染合约测试（−896），`terminalScrollRegionSequence` 死函数删除。

逐项核对结论（与 §5.1/Phase 6 清单的分歧，需母计划口径修正）：

- `historyWindow`、`commitExcessHistoryToScrollbackLocked`、`handoffFrontier`（含 `AdvanceTo/TrimPrefix/Clamp/Value`）**均为 owned 模式活跃代码，不可删除**；§5.1 与 Phase 6 中"删除 historyWindow/commitExcess/frontier"条目应改为"删除其 legacy-only 调用面"（已完成）。
- `headroom` 仅为注释语义，无独立符号。
- `renderStatusLocked`/`renderPopupLocked`/`renderPromptRowsLocked` 的 legacy 即时渲染主体（直写 `TerminalOutput()`）是**活跃代码**：~20 个双路径调用点 + 31 个 legacy 行为合约测试依赖。删除尝试已回退；其退役需要独立的"legacy 测试迁移到 owned 模式"里程碑（测试环境默认 non-owned，断言 TerminalOutput 序列），不在本切片内强推。
- `activeHandoffFrontier`/`historyWindowAbsorbed` 已不存在（此前清理）。

验证：`go build ./...`、`go vet`、`go test ./cmd/aicli/ui/...`（15 包，-count=1）全绿；`go test -race ./cmd/aicli/ui/` 绿。`commands` 包仅剩 1 个环境性失败（`TestLoadLocalChatRuntimeConfig_DefaultsTeamStorePathToSessionRuntimeDir`：本机运行中的 aicli 进程占用 `data/runtime/session_runtime.sqlite`，与删除工作无关）。剩余：renderer-mode session 初始化选择、非 Windows PTY 验收、Windows 长会话/resize/400+ 行矩阵、P8/P9 清单与 runbook 同步（见实施指南 Phase 6 任务 3/5/6）。

2026-08-02 版本曾提出“两个物理所有者 + `AppendNewRows/ScrollExistingRows` + 全局 Frame/Scrollback mode + `committedBoundary int`”。评审后否决，原因如下：

- 缺少独立 semantic source truth 和 pending effect 层；
- boundary 单位不明确，无法跨 width/layout generation 证明 exactly-once；
- 每个新 turn 都会产生 mutable cell，全局单向 mode 不成立；
- geometry scroll 与 content commit 混淆；
- `ScreenModel 始终一致` 无法覆盖短写、lease、resize 和旁路 mutation；
- 删除 prefix cursor 与 Codex streaming 的 queued/emitted 设计相冲突。

旧版本的故障基线、代码地图和“字节重复不等于语义重复”分析已吸收到本文 §1、§2 和 §7；其目标架构和原阶段 0–5 不再具有施工效力。
