# aicli TUI owned 渲染简化方案：统一状态、单事件循环与确认式 Presenter

状态：**approved migration sub-plan（受 unified architecture 约束；尚未实施完成）**

日期：2026-08-03

上位规范：`docs/plan/aicli-tui-unified-render-architecture-refactor-plan.md`

关联实施子计划：`docs/plan/aicli-tui-transcript-overlay-renderer-mode-plan.md`（明确 primary/alternate renderer ownership、`Ctrl+T` pager 与 history handoff 的边界；不替代本文的 state/effect 收敛）。

## 0. 文档定位与结论

本文处理 owned viewport 中的重复 history handoff、ActiveBand grow/shrink 空白、状态源分裂和多调度器竞争。本文是统一架构母计划的迁移子计划，不独立定义终局；与母计划冲突时，以母计划的不变量和状态模型为准。

结论：当前问题不是某一个 `commitExcessHistoryToScrollbackLocked` 调用时机错误，而是同一语义范围被 coordinator、surface、ScreenModel 和 native scrollback 的多个局部状态机同时判定为“尚未输出”或“已经输出”。正确修复采用：

1. 一个 `UIController` actor 串行处理全部 UI action；
2. 一个 retained `AppState` 保存 transcript、active cell 和 bottom pane；
3. 一个 tokenized `HistoryEffectQueue` 管理 native scrollback 提交；
4. 一个 pure Layout/Compose 从不可变 snapshot 生成 frame；
5. 一个 transactional Presenter 独占终端，并以 Ack/Fail action 回报结果；
6. `TerminalProjectionState` 显式区分 `Known/Unknown`，失败后从 AppState 恢复。

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

- `UIController` 已提供 bounded mailbox、revision、coalescing、barrier、shutdown 和 starvation 覆盖；普通 runtime event 通过 bridge 的有界 ingress 转为 `RuntimeEvent` action，editor snapshot 经 durable `InputEvent`，显式 active-stream refresh 经 `Resize` barrier。
- FramePump 的 dynamic-status、stable-commit、prompt callback 只生产 `Timer`；active-stream callback 只生产 coalescable `DrawRequested`。它们的 legacy adapter 绘制仍在 reducer 中发生，尚未被 Presenter transaction 替代。
- surface facade 已接入 action；`ScreenLease` acquire/release 成功后投递 Lease barrier，`EffectResult` 有 reducer 入口。lease transport、effect token、Ack/Fail、Known/Unknown recovery 仍属于后续阶段。
- reducer 内触发的 surface facade 使用 controller causal follow-up queue，在当前 action 后、下一外部 action 前获取独立 revision；该队列不消耗 bounded external mailbox 槽位，避免满 mailbox 下 reducer 自投递等待。它是 legacy adapter 的重入安全措施，不是 presenter/effect queue。
- `approval.requested` 与 `question.asked` 继续走 bridge worker 的同步交互路径，避免在 actor mailbox 内阻塞 stdin；完成条件是 effect/result 化后删除该受控例外，而不是仅把原函数搬进 reducer。
- 直接 writer 在 `ClearPrompt -> WriteOutput` 交接处使用 actor idle barrier，避免 prompt reserve 尚未清除时把输出投影到旧 geometry。这是 adapter 期兼容措施，不是终局 writer API。

出口：AppState mutation 和 presenter sequencing 只有一个 goroutine owner；现有视觉行为保持。

### Phase 2：统一 AppState/Scene

- 扩展当前 transcript-only `TuiScene`，或引入包含它的 `AppState`；
- 建立 `ActiveCellState`，ActiveBand 改为派生 projection；
- prompt/status/popup/focus/geometry/lease 进入同一 snapshot；
- `FlushPolicy` 真正驱动统一 frame scheduler。

**实际进度（2026-08-04，Phase 2 partial）**

- `ui.AppState` 和 `UIController.AppState()` 已提供深拷贝 snapshot；controller transition state 嵌入该 AppState，geometry/lease 不再在 transition ledger 之外保存第二份字段。
- runtime Scene snapshot 在普通 `RuntimeEvent` reducer 完成 ChangeSet mapping 后，经 causal `ReplaceTranscriptAction` 进入 actor；mutable Scene cell 只派生 active semantic `CellID/Revision/Source`，不猜测 stable/enqueued/acked range。
- live user submit、structured command result、local error 三类直接 Scene injection 也在 legacy coordinator 完成写入并释放 `c.mu` 后投递 `ReplaceTranscriptAction`，所以背压不会与 reducer 的 coordinator lock 形成等待，AppState 可追上这些非 runtime transcript producer；事件日志 `replayEventLog` 在成功重建 Scene 后也投递一次完整 snapshot，避免 AppState 保留重放前 transcript；其余未枚举 producer 仍待全量审计与接线。
- 已 action 化的 input/status/prompt/popup/legacy ActiveBand facade 同步更新 `AppState.Bottom`。本轮将 prompt reset/rows/notice/editor、incremental input tracking、单独 persistent/dynamic status 与 composer preview 一并接入；`Apply` 与同步 facade 的终端字节 parity、以及 AppState popup-preservation/snapshot-detach 回归均已覆盖。popup owner/priority、suspended `PopupStack` 和 tokenized `PopupHandle` 的 begin/update/clear 已有同一份 pure `BottomPaneState` transition；handle 在 facade 边界分配后以 durable begin action 入队，保证同 token 的后续操作 FIFO 排在其后，snapshot 对 stack 深拷贝，`UpdatePopupAction` 也已由 coordinator reducer 应用。该切片仍只使 AppState 追踪 legacy surface adapter，不能将其误读为 popup/presenter owner 已收敛。ActiveBand 仍是迁移期 projection input，不是 final semantic active owner；纯 Layout production compose、全 producer 覆盖、同步 cursor helper/完整 focus policy、Presenter 与 parity 尚未完成。
- `LayoutAppState` 已从一个 immutable AppState 派生 Scene boundary rows 和 bottom pane row allocation/cursor intent，且无 terminal I/O、mutex read 或 effect 推进。本轮新增纯 `BottomPaneGeometryPolicy`：ActiveBand budget/top gap、margin、popup budget 与 prompt viewport 都从 `BottomPaneState + GeometryState` 推导；prompt 保存 logical cursor/absolute visual row/total rows/viewport start，并在 resize 时由 source 重新测量，不读取 legacy cursor cache。`LayoutBottomPaneRows` 已输出 bottom reserve 的 plain text/owner plan，并对普通/多行 prompt、popup/composer、短终端压力、窄宽/宽屏和同一 semantic state 的 resize cycle 与 legacy snapshot 建立 parity；新增 `LayoutAppScreen` 纯 screen-row shadow，将 retained transcript 的 cell identity/gap 与 bottom overlay 放入同一 viewport-sized plain layout，并明确排除 mutable cell 以避免 active/band 重复。legacy owner annotation 覆盖多行实际 prompt 文本，却不把 popup 的空输入 gap 误标为 Prompt。legacy surface probe 的已测宽高经 `Resize{Applied:true}` barrier 回投 actor；只有实际 geometry 改变才推进 generation，回投本身不形成二次 probe/reflow。该 shadow 尚未形成 physical Compose、full-screen legacy text/owner parity 或 Presenter diff。

出口：coordinator/surface 不再各自保存可独立推进的 UI 正文和 bottom component 状态。

### Phase 3：TerminalSession 与 effect queue

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

## 9. 被否决方案记录

2026-08-02 版本曾提出“两个物理所有者 + `AppendNewRows/ScrollExistingRows` + 全局 Frame/Scrollback mode + `committedBoundary int`”。评审后否决，原因如下：

- 缺少独立 semantic source truth 和 pending effect 层；
- boundary 单位不明确，无法跨 width/layout generation 证明 exactly-once；
- 每个新 turn 都会产生 mutable cell，全局单向 mode 不成立；
- geometry scroll 与 content commit 混淆；
- `ScreenModel 始终一致` 无法覆盖短写、lease、resize 和旁路 mutation；
- 删除 prefix cursor 与 Codex streaming 的 queued/emitted 设计相冲突。

旧版本的故障基线、代码地图和“字节重复不等于语义重复”分析已吸收到本文 §1、§2 和 §7；其目标架构和原阶段 0–5 不再具有施工效力。
