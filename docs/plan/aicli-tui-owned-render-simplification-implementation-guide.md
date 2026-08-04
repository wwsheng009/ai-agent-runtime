# aicli TUI owned 渲染简化：实施契约

上位规范：`docs/plan/aicli-tui-unified-render-architecture-refactor-plan.md`

专项方案：`docs/plan/aicli-tui-owned-render-simplification-plan.md`

transcript pager/模式切换实施子计划：`docs/plan/aicli-tui-transcript-overlay-renderer-mode-plan.md`

评审：`docs/analysis/aicli-tui-owned-render-simplification-plan-review.md`

状态：**approved execution contract / implementation not complete**

日期：2026-08-03

适用范围：`backend/cmd/aicli/ui/`、`backend/cmd/aicli/commands/` 的 owned interactive 渲染路径。

## 0. 使用规则

本文只定义施工顺序、允许的中间态、门禁与完成定义。设计决策只在上位规范和专项方案中修改，实施者不得在代码中临时发明第三套所有权模型。

每个提交必须：

1. 属于一个阶段和一个可独立验证的任务；
2. 先明确权威状态与 effect owner；
3. 附带相应测试或保持既有 parity；
4. 不让两个 production presenter 同时写 terminal；
5. 不删除用户已有的工作树实验或无关改动；
6. 在 Windows 上控制补丁/命令长度，长改动按函数和文件分块。

阶段可以拆为多个小提交，不要求“单阶段单提交”。不允许跳过 UI owner/AppState 阶段，直接实现新的 handoff boundary。

## 1. 目标与非目标

### 1.1 目标

- 所有 UI mutation 经 `UIAction` mailbox 和单一 reducer；
- transcript、active cell、bottom pane、geometry、lease 形成一个 AppState snapshot；
- Layout/Compose 为纯派生；
- Presenter/TerminalSession 是唯一物理 writer；
- history handoff 使用 tokenized effect 和 Ack/Fail；
- terminal projection 显式 Known/Unknown；
- streaming 只有一个 typed queued/acked source-range owner；
- 删除 `commitExcess/headroom/frontier` 补偿链和旧 production renderer。

### 1.2 非目标

- 不重写 JSON/plain/noninteractive renderer；
- 不依赖第三方 TUI 框架；
- 不把 prompt/status/popup 写进 transcript；
- 不承诺终端字节中的相同文本永不因 repaint/reflow 重发；
- 不把 native scrollback 变成可查询数据库；
- 不以 RI/SD 拉回作为正确性前提；
- 不在迁移中顺手改产品文案、主题或 Markdown 视觉规范。

## 2. 施工铁律

| ID | 铁律 | 审查方法 |
| --- | --- | --- |
| IR-1 | AppState 只有 UIController reducer 一个写者 | producer 只能 `Post(UIAction)` |
| IR-2 | terminal bytes 只有 Presenter/TerminalSession 一个 writer | AST inventory + terminal writer fence |
| IR-3 | semantic source、effect progress、physical projection 是三种状态 | 类型和包边界不可混用 |
| IR-4 | geometry mutation 不生成 HistoryCommit | reducer effect 测试 |
| IR-5 | history effect 必须有 Token/CellID/Revision/Range/Generation | 禁止裸 `[]string` boundary |
| IR-6 | 只有完整写成功后的 Ack 推进 front/projection | short-write/failure 测试 |
| IR-7 | projection Unknown 时禁止 incremental diff | recovery barrier 测试 |
| IR-8 | Scene revision 不替代 queued/acked effect range | streaming invariant 测试 |
| IR-9 | ActiveBand 是 projection，正文源只在 ActiveCell/Transcript | snapshot ownership 审查 |
| IR-10 | repaint 可重发字节；exactly-once 以 token/range 判断 | 禁止文本 hash 作为权威断言 |
| IR-11 | FramePump callback 只投递 action | callback source fence |
| IR-12 | resize/replay 从 semantic source 派生 | 禁止从 front/VT/terminal 反推 |

## 3. 必须落地的核心类型

命名可按仓库风格调整，语义不可弱化：

```go
type UIAction interface{ isUIAction() }

type AppState struct {
    Revision   uint64
    Transcript TranscriptState
    Active     ActiveCellState
    Bottom     BottomPaneState
    Geometry   GeometryState
    Lease      LeaseState
}

type ProjectionValidity uint8
const (
    ProjectionUnknown ProjectionValidity = iota
    ProjectionKnown
)

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

effect result 必须作为 action 返回：

```go
type TerminalEffectAck struct { Token uint64; Frame uint64 }
type TerminalEffectFailed struct { Token uint64; Err error; MayHavePartiallyWritten bool }
```

`MayHavePartiallyWritten=true` 时不得盲目重放同一 batch。projection 进入 Unknown，由 recovery policy 决定 clear/rebuild/controlled teardown。

## 4. 阶段执行

### Phase 0：测试语义与安全网

**任务**

1. 为测试引入稳定的 `CellID/CommitToken/Range` ledger；测试样本可使用唯一文本辅助定位，但不得以文本本身作为身份。
2. 保留并改造：
   - `TestFixedBottomSurface_BandShrinkRestoreNeverReplaysHandedOffHistory`；
   - `TestBottomReserveShrinkRestoresHistoryWithoutBlankingTop`。
3. `vt.Screen` 可记录 scrollback/effect 序列；不实现或依赖“RI/SD 从 native scrollback 拉回 semantic row”。
4. 增加 fake writer：零写失败、部分写失败、成功、重复 Ack、stale generation。
5. 将 `go test ./cmd/aicli/ui/... -count=1` 加入 CI hard gate。
6. 盘点 `zz_` 诊断测试，标记保留至哪个阶段，不在本阶段删除。

**出口**

- 测试可区分 viewport repaint、resize replay、history handoff；
- 当前重复 bug 仍可稳定复现；
- fault injection 基础设施可独立运行；
- 没有生产行为变化。

### Phase 1：UI actor 与 action adapter

**任务**

1. 新增 bounded UI mailbox 和 `UIController.Run`；定义 durable、coalescable、barrier action 分类。
2. RuntimeEvent、Input、Resize、Timer、Lease、DrawRequested、EffectResult 统一投递 action。
3. 将 `FramePump.Schedule*` callback 改为只 `Post` action；禁止 callback 直接获取 coordinator/surface lock。
4. 现有 `SetActiveBandStyled/ClearActiveBand/SetStatusModels/prompt/popup` 保留为 facade，但内部只投递 action。
5. reducer 初期可调用 legacy adapter 生成相同输出，确保行为不变；terminal writer 仍只有一条 production 路径。
6. 增加 action replay、coalescing、barrier、shutdown 和 starvation 测试。

**实施快照（2026-08-03，partial）**

- `ui.UIController`、bounded mailbox、durable/coalescable/barrier 分类及 replay/coalescing/barrier/shutdown/starvation 测试已落地；controller-owned transition snapshot 记录 geometry/lease/draw/effect-result barrier facts（明确不是完整 AppState）。普通 runtime event、editor input snapshot、显式 resize、四类 FramePump 到期回调和 surface facade 已进入 action 顺序。
- active-stream 帧到期现在产生 coalescable `DrawRequested`；dynamic-status、stable-commit、prompt 仍使用 typed `Timer`。callback 本身不再获取 coordinator/surface 锁或执行业务 mutation。
- `ScreenLease` acquire/release 在既有 DEC 1049 物理事务成功后投递 `LeaseAcquired`/`LeaseReleased` barrier；reducer 只记录 Phase 1 logical lease adapter，尚未把 transport 移入 TerminalSession。
- `EffectResult` 已有 barrier reducer 入口和回归测试，但没有 production `TerminalSession` 或 tokenized `HistoryCommit` source；不得把该账本误读为 Ack/failure recovery 已完成。
- `RuntimeEvent` reducer 触发 surface facade 时，facade action 进入 controller 的 causal follow-up queue：它们在当前 action 后、下一个外部 mailbox action 前分别按 revision 处理，不占用 external mailbox 容量。这样 mailbox 已满时 reducer 不会等待自身恢复消费；外部 facade 仍按正常 bounded `Post` 路径投递。该机制只解决 Phase 1 legacy adapter 的 re-entry/backpressure，不把 legacy coordinator mutation 误称为终局 reducer/AppState。
- 审批、问答仍是明确的 legacy synchronous exception：它们的 stdin/modal 流程尚未拆成 interaction effect/result，不能在 reducer 内等待。`waitUIActorIdle` 仅用于既有同步边界和测试，不能从 reducer 调用。
- 当前 surface/coordinator legacy adapter 仍会写 terminal；这不是 Phase 3 Presenter ownership，也不构成 AppState/纯 Layout 完成信号。
- facade 接线以 `FixedBottomSurface.SetUIActorPoster` + `postFacadeAction` 统一投递。除仍需同步光标定位的 `SetPromptCursor`/`MoveToPromptCursor` helper 外，ActiveBand、三种 status 更新、prompt 展示/重置/行数/notice/editor/input tracking、composer preview，以及 popup 的 begin/update/clear 都已由 typed action 经 `surface.Apply` legacy adapter 同步应用；`UpdatePopupAction` 也已进入 coordinator reducer。曾暴露 `ShowPrompt→ClearPromptRows` 异步配对回归（ShowPrompt 先异步、紧随的同步 ClearPromptRows 对尚未渲染的 prompt 失效导致 mid-stream prompt 残留），已通过把 `ClearPromptRows` 一并接入 facade 队列修复，并补 facade_action_test 接线和同步输出 parity 断言。
- 分层回归（2026-08-03 终验）：`go test ./cmd/aicli/ui/... -count=1` 全绿；`go test ./cmd/aicli/commands/... -count=1` 仅剩 1 个已知 pre-existing 失败 `TestRenderLayer_UserInput_ReplayPathDoesNotInject`（基线与接线后均失败，见测试基线清单）；`RetriesAfterInvalidChoice` 系列与 `ReplayVisibleChatHistoryAfterTruncationSkipsSystemOnly` 为 pre-existing 跨测试 stdout spinner 泄漏（`\x1b[s…\x1b[u`）的随机 flaky 受害者（单独/连跑 PASS，`stripAsyncTerminalNoise` 与泄漏源均存在于 HEAD 基线）；`go build ./cmd/aicli/...` 与 `git diff --check` 通过。`go vet ./cmd/aicli/...` 仍被既有 `ui/renderengine/handoff_plan.go:68` 的 `WriteTo` 签名告警阻断，本轮未改该无关债务。

**出口**

- 业务 producer 不直接 mutation surface；
- 每个 frame 有单一 action/revision 顺序；
- FramePump 不再是任意 callback executor；
- 现有 visual/VT/parity 测试保持。

### Phase 2：统一 AppState 与纯布局

**任务**

1. 以现有 `ui/scene` transcript 为组成部分建立完整 AppState，不再把 transcript-only Scene 直接宣称为全部 UI 状态。
2. 新增 `ActiveCellState`：raw semantic source、revision、stable range、stream phase；ActiveBand 只由 Layout 派生。
3. 新增 `BottomPaneState`：status、prompt/editor、popup stack、focus；统一 row allocation、overlay priority 和 cursor intent。
4. geometry/lease 进入 AppState；每个 snapshot 携带 terminal/layout generation。
5. Layout/Compose 不读取实时 mutex state，不写 terminal，不推进 effect。
6. 将 `scene.FlushPolicy` 接到统一 frame scheduler，而不是 setter 内同步 flush。
7. 建立旧 path vs AppState snapshot 的 frame/text parity，仅比较内存结果。

**实施快照（2026-08-04，partial）**

- `ui.AppState` 已建立 `Transcript`、`Active`、`Bottom`、`Geometry`、`Lease`、`LayoutGeneration` 的可深拷贝快照形状；`UIControllerState` 嵌入该 state，不再平行保存一份 geometry/lease。`UIController.AppState()` 只返回脱离 actor 内存的 snapshot，供后续纯 Layout 消费。
- 普通 runtime event 完成既有 ChangeSet -> Scene mapping 后，将 `scene.Snapshot` 作为当前 RuntimeEvent 的 causal `ReplaceTranscriptAction` 交回 actor；它不从 terminal、ScreenModel 或 historyWindow 反推正文。Scene mutable cell 暂可派生 `ActiveCellState` 的 `CellID/Revision/Source`，但 `Stable/Enqueued/Acked` range 保持未知零值，不能猜测或复制 coordinator 的 streaming cursor。
- live user submit、structured command result 与 local error 这三类非 runtime Scene producer 完成 legacy coordinator 写入并**释放 `c.mu` 后**，也会投递同一 `ReplaceTranscriptAction`；这避免 bounded mailbox backpressure 与 reducer 获取 coordinator mutex 形成锁等待。它们投递完整 immutable Scene snapshot，因此 AppState 不会落后于这三类 direct injection。事件日志 `replayEventLog` 成功重建 Scene 后也只投递一次完整 snapshot，使重放后的 AppState 不会保留旧 transcript；其他尚未枚举的 producer 仍待逐项迁移。
- 已进入 actor 的 input/status/prompt/popup/legacy ActiveBand facade action 会更新 `AppState.Bottom`。本轮补齐 prompt reset/rows/notice/editor、incremental input tracking、单独 persistent/dynamic status 与 composer preview 的状态迁移，并以 `Apply`/同步 facade 字节 parity 和 AppState snapshot 回归固化。popup 的 owner/priority、suspended `PopupStack`、tokenized `PopupHandle` begin/update/clear 已有纯 state transition，并且 `BeginPopupInputForOwner*` 先分配 token 再投递 durable action，后续 update/clear 因 FIFO 排在 begin 之后；`AppState()` 深拷贝 stack。它仍是 legacy surface adapter，而不是最终 popup/presenter owner：仍有同步 cursor-move helper、完整 focus policy 和所有 producer 待收敛。ActiveBand 仍标为 legacy projection input，尚不是从 `ActiveCellState` 纯派生的最终实现。
- `ui.LayoutAppState(AppState)` 已提供纯内存 layout snapshot：它从 semantic transcript 调用 `scene.LayoutTranscript` 派生 boundary rows，并计算 bottom pane 的 status/prompt/popup/legacy-band row allocation 与 cursor focus。该函数不读取 surface mutex、terminal、ScreenModel 或 effect progress；目前只形成 layout 数据，不生成 physical frame/diff。
- 本轮补齐纯布局的 geometry 输入契约：`BottomPaneGeometryPolicy` 只由 `BottomPaneState + GeometryState` 计算 ActiveBand 尾部预算/顶部间隔、composer margin、popup 预算及 prompt 最大可见行；长草稿保存 logical cursor、absolute visual row、total rows 与 viewport start，`LayoutAppState` 在 width/height generation 改变时从 prompt source 重算可见 text rows，绝不从 surface cursor cache 反读。`LayoutBottomPaneRows` 已生成不含 ANSI 的 bottom reserve row/owner/text plan，并对普通、多行 prompt、popup/composer、短终端压力、窄宽/宽屏切换和同一语义状态的 geometry 循环建立 legacy snapshot parity。新增 `LayoutAppScreen` 纯 screen-row shadow，把 retained transcript 的 CellID/gap 与 bottom owner plan 放进一个 viewport-sized plain layout；mutable cell 明确排除，避免将 active source 与 legacy band 双重纳入。legacy owner map 现在只把实际 prompt 文本 viewport 标为 Prompt，popup 的空输入占位仍是 Gap；诊断快照同时携带 prompt 的 absolute row 与 viewport start，避免将相对 cursor 行误作 geometry-independent source。legacy probe 完成后以 `Resize{Applied:true}` causal barrier 回投已测宽高；reducer 仅在实际几何变化时递增 `Geometry.Generation/LayoutGeneration`，且该回投不再次 probe 或 reflow。新增纯派生、prompt resize reflow、generation、bottom matrix、screen-layout 与 coordinator bridge 回归。
- follow-up 若所属 reducer panic 会被整个丢弃，防止父 action 未提交而 facade/Scene child action 单独提交。该原子性只覆盖 controller action continuation，不等于完整 Scene transaction/terminal transaction 已完成。
- 尚未实现 physical `Compose`/frame parity 的 production shadow、完整 screen-row ownership/text parity、所有 Scene producer action 化、TerminalSession 或 HistoryCommit effect queue；`FixedBottomSurface` 仍为 legacy physical adapter。`Resize{Applied:true}` 只是 legacy probe 到 AppState 的数据桥，不是 Presenter 几何 transaction。

**出口**

- 一个 snapshot 可以完整生成 history/active/bottom/overlay/cursor；
- ActiveBand buffer 不再是独立正文真相源；
- popup 关闭可从 snapshot 恢复下层；
- resize frame 不混用不同 generation。

### Phase 3：TerminalSession 与投影恢复

**任务**

1. 将 `ScreenModel` 尺寸收敛为 owned viewport area；它只表示 physical projection cache。
2. Presenter 持有 front/back、cursor、scroll region、projection validity 和 frame generation。
3. 固定 transaction 顺序：lease/generation -> geometry -> history effects -> viewport diff -> cursor。
4. terminal 完整写成功后才 commit front；短写/错误立即标 Unknown。
5. Unknown 状态下一帧执行 recovery barrier/full repaint；resize、lease release、capability change 都可显式触发 Unknown。
6. normal geometry diff 可以内部使用 scroll 优化，但必须与 full repaint 等价，且优化失败可关闭。
7. 为 cursor、wide cell、SGR、DECSTBM reset、synchronized update 增加 effect-level 测试。

**出口**

- 所有 ANSI 由 TerminalSession 产生；
- partial write 不会把 front 标成成功；
- Unknown 后不做 incremental diff；
- ScreenModel 不再被业务层读取为 history source。

### Phase 4：确认式 history handoff

**任务**

1. 为 eligible finalized display range 创建唯一 `HistoryCommit`；effect 进入 Pending，不同步推进 cell/frontier。
2. Presenter 将 Pending 标 InFlight 并在 frame transaction 中执行；成功返回 Ack，失败返回 Failed。
3. reducer 处理 Ack 后记录 `Token + CellID + Revision + Range + Generation`，再释放 effect payload。
4. geometry action 不创建 effect；删除 band/popup/prompt 路径中的 `commitExcessHistoryToScrollbackLocked`。
5. 删除固定 `historyWindowHeadroom` 和 `HandoffFrontier.TrimPrefix/Clamp` 的 correctness 职责。
6. 将 `appendOwnedDirectPaintLocked` 与 full-frame path 收敛到同一个 effect/frame plan。
7. 不使用字符串相等决定是否重试/去重。

**出口**

- 两个专项测试通过；
- 同一 token/range 最多一个 Ack；
- band grow/shrink 不改变 effect queue；
- stale revision/generation effect 被 reducer 拒绝；
- failure/partial-write 恢复测试通过。

### Phase 5：streaming 与 Scene 权威收敛

**任务**

1. 将 assistant/reasoning/tool-running delta 统一更新 `ActiveCellState`/Scene mutable cell。
2. 建立一个 streaming range owner，明确 `raw source`、`stable boundary`、`enqueued range`、`acked range`。
3. 删除 coordinator、assistant transcript、soft output 和 surface 中对同一 source range 的平行游标；不得为了删字段而丢失 queued-but-unacked 区间。
4. stable tail 的 HistoryCommit source 来自同一 semantic cell/Layout；不再单独调用另一套 Formatter pipeline。
5. finalization 作为一个 reducer transaction：校验 revision、更新 final source、标 Finalized、清 active projection、生成 eligible effect、request frame。
6. live/replay/resume/resize 使用同一 DisplayLines/Layout；AICLI Scene flag 只做 session-level presenter 选择，最终删除。

**出口**

- 可见正文 100% 来自 AppState snapshot；
- final 不通过 append 第二份文本完成；
- streaming invariant 为 `acked <= enqueued <= stable <= source`；
- parity、cancel、delta/final race、tool/reasoning 测试通过。

### Phase 6：删除、性能与真实终端验收

**任务**

1. 删除 production legacy immediate renderer、raw adapter、旧 Scene fallback/flag。
2. 删除不可达的 `historyWindow/headroom/handoffFrontier/commitExcess/legacyReserve` 和诊断 `zz_` 文件。
3. 保留 plain/JSON renderer，但 renderer mode 只在 session 初始化选择。
4. 跑全量、race、fault injection、benchmark；修复新增泄漏、锁反转和 starvation。
5. 人工验证 Windows Terminal + ConPTY，以及至少一种非 Windows PTY：stream、band、popup、resize、fullscreen、400+ 行、退出恢复。
6. 更新母计划状态、P8/P9 删除清单和 runbook。

**出口**

- 满足 §8 DoD；
- production 只有 AppState -> Layout -> Presenter 路径；
- emergency 回退若暂留，只能在 session 启动时选择完整 renderer，不能运行时双写。

## 5. 提交纪律

1. 一个提交只做一个可描述的 ownership/state/effect 变化；阶段可拆多提交。
2. 测试与实现可在同一提交，但测试意图必须先明确，不能通过弱化语义掩盖失败。
3. 新旧 adapter 并存时，提交说明必须写明哪一方是 production authority、哪一方只 shadow。
4. 删除旧状态必须在替代路径和 fault tests 通过后进行。
5. 不要求为了形式一次性重命名/移动大文件；优先小切片和可回滚 facade。
6. 不把 benchmark 波动当 correctness；也不能以 correctness 为由接受无界 Layout/GC 回退。

推荐提交信息：

```text
render(ui): P1 post geometry changes through UIController
render(scene): P2 derive active band from ActiveCellState
render(presenter): P3 invalidate projection on partial write
render(history): P4 ack tokenized history commits
```

## 6. 回归门禁

在 `backend/` 下执行：

```powershell
go build ./cmd/aicli/...
go vet ./cmd/aicli/ui/... ./cmd/aicli/commands/...
go test ./cmd/aicli/ui/... -count=1
go test ./cmd/aicli/commands/... -count=1
go test -race ./cmd/aicli/ui/... ./cmd/aicli/commands/...
```

阶段专项：

```powershell
# handoff / geometry
go test ./cmd/aicli/ui -run 'BandShrink|BottomReserve|HistoryCommit|Projection' -count=1 -v

# Scene / event / streaming
go test ./cmd/aicli/commands -run 'ChatRuntimeEvents|RenderLayer|ActiveStream|Interaction' -count=1

# fault injection
go test ./cmd/aicli/ui/... -run 'ShortWrite|WriterFailure|Unknown|DuplicateAck|StaleGeneration' -count=1

# benchmark（Phase 2 前记录，Phase 6 对照）
go test ./cmd/aicli/ui/... -run '^$' -bench 'ActiveStream|Layout|Compose|Presenter' -benchmem
```

若仓库已有与本任务无关的全量测试失败，必须记录并用目标包/专项证明本改动；不得删除或跳过新增的 owned-render hard gate。

## 7. 禁止清单

- 禁止新增无单位 `committedBoundary int`、文本 hash frontier 或固定恢复 headroom；
- 禁止把 `ScreenModel`/terminal/VT 当 semantic source；
- 禁止 geometry setter 直接调用 history insertion；
- 禁止 FramePump/timer callback 直接 mutation + flush；
- 禁止用 Scene revision 代替 terminal effect Ack；
- 禁止删除全部 prefix/source-range 状态而没有 queued/acked 等价物；
- 禁止 partial write 后继续基于旧 front diff；
- 禁止依赖 RI/SD 从 scrollback 拉回；
- 禁止 shadow presenter 写 terminal；
- 禁止运行时在 legacy/Scene presenter 之间切换并共享同一屏幕；
- 禁止以字符串出现次数作为通用 exactly-once 指标；
- 禁止提前删除 legacy 使中间阶段失去唯一 production path。

## 8. 完成定义

1. 所有 UI producer 只投递 action；UIController 是 AppState 唯一写者。
2. 一个 snapshot 完整表达 transcript、active、bottom、geometry、lease、cursor intent。
3. ActiveBand 不保存独立正文；popup/status/prompt 不修改 transcript boundary。
4. Presenter 是唯一 terminal writer，front 只在完整成功后更新。
5. projection 具备 Known/Unknown，短写和 lease/resize barrier 可恢复。
6. history commit 使用 Token/CellID/Revision/Range/Generation，只有 Ack 推进。
7. geometry change 不创建 commit effect；两个专项测试通过。
8. streaming 只有一个 queued/acked range owner，finalization 是单一 reducer transaction。
9. live/replay/resume/resize 使用同一 semantic source/Layout。
10. production legacy renderer、raw writer、旧补偿状态和临时 flag 已删除或不可达。
11. ui/commands 全量、race、fault injection、benchmark 和真实终端清单通过。
12. `/debug` 可观测 AppState revision、layout generation、projection validity、pending/inflight/acked tokens。

## 9. 阻断与回退

出现以下任一情况立即停止进入下一阶段：

- 同一时刻出现两个 production terminal writer；
- reducer 外仍有新增 AppState mutation；
- partial write 后 front 被错误提交；
- band geometry action 生成 history effect；
- queued-but-unacked stable range 丢失或重复进入 active tail；
- effect 没有 token/range/generation；
- Scene shadow mismatch 被静默吞掉；
- 测试只能依靠 native scrollback pullback 才通过。

回退以完整阶段/adapter authority 为边界：关闭新 adapter 时必须整条 session 回到旧 renderer，不能保留新 reducer 状态却让旧 presenter继续写同一屏幕。已经 Ack 的 native scrollback effect 不可“撤销”；回退时依赖 semantic source 做 controlled rebuild，而不是逆向滚动终端。
