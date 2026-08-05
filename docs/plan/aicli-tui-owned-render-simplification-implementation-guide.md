# aicli TUI owned 渲染简化：实施契约

上位规范：`docs/plan/aicli-tui-unified-render-architecture-refactor-plan.md`

专项方案：`docs/plan/aicli-tui-owned-render-simplification-plan.md`

transcript pager/模式切换实施子计划：`docs/plan/aicli-tui-transcript-overlay-renderer-mode-plan.md`

评审：`docs/analysis/aicli-tui-owned-render-simplification-plan-review.md`

状态：**approved execution contract / core inline cutover implemented and TTY verified**

日期：2026-08-06

适用范围：`backend/cmd/aicli/ui/`、`backend/cmd/aicli/commands/` 的 owned interactive 渲染路径。

> **当前实施口径（2026-08-05）**：interactive TTY 的 primary writer 已直接切到 `TerminalSessionPresenter`，`FixedBottomSurface` 的 physical write 已单向 fence，且 `AICLI_TUI=legacy/plain/off/0` 不再能选择旧 interactive renderer。assistant active/final、local/direct output、MCP notice、tool final result 与 persisted history reconcile 均已进入 `Scene -> AppState -> TerminalSession`；`ScreenLease` 的 alternate transport 也使用同一 writer。本文早期“未安装 presenter / legacy 是唯一 writer / shadow adapter”描述仅保留历史施工上下文，以母计划 §15.7–15.8 为准。

> **当前施工基线（2026-08-06，覆盖后文旧阶段快照）**：生产 primary 已改为 `top native-history region + bottom inline viewport`。finalized transcript 的全部 display rows 和 mutable active 的稳定 overflow prefix 通过 reducer-owned `HistoryCommit` 插入 `1..OutputBottomRow`；`ScreenModel` 只能持有 `OutputBottomRow+1..terminalHeight`，不能重绘 history。内部空行与 trailing empty row 均持有稳定 `SourceRange/FragmentID`；finalize 只提交尚未 Ack 的 tail。active 使用 `Stable/Enqueued/Acked` 连续 source range，只有 Ack 才从 live tail 隐藏；Markdown handoff 保持结构化 IR。native scrollback 首次溢出后，resident tail 必须 sticky top-aligned。一次事务顺序固定为 viewport boundary transition、history insert、viewport diff、cursor。零字节失败在 recovery 后重试同一 token，partial write 保持 Unknown 且不得盲重试。

> **验证基线**：`go test ./cmd/aicli/... -count=1`、`go test -race ./cmd/aicli/ui ./cmd/aicli/commands -count=1`、`go vet ./cmd/aicli/ui ./cmd/aicli/commands` 均通过。真实 provider Windows Terminal E2E manifest 位于 `output/aicli-terminal-e2e/opencode-wt-1176ea6f5afc4fa597964cc30b50a984/manifest.json`：40 个 marker 恰一次、严格有序且连续，reasoning 语义内容位于 final answer 前，仅 raw runtime event 标签未泄漏；退出使用 `AttachConsole+WriteConsoleInputW`，helper/runner code 均为 0、forced cleanup 为 0。此 run 不等同于 compatibility cleanup、全宿主矩阵或长会话验收完成。后文 whole-screen、retained-frame 或未安装 seam 的文字仅为历史，不得恢复。

> **事件终态契约**：成功的 `llm.request.finished` 只关闭 transport request，不关闭 assistant active stream。authoritative `assistant_message` 到达后才执行正常 finalization；失败 request、interrupt、session end 或 run-end fallback 才可提前收尾。这个顺序与 `FinalizeActiveCellAction` 和 HistoryCommit 的 exactly-once handoff 共用同一门禁。

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
| IR-13 | 编辑器逐键回调不得等待 actor drain 或 mailbox 容量 | actor 阻塞/满邮箱输入回归 |
| IR-14 | primary 顶部 history region 必须保留最新 finalized tail；pager 不得替代该可见性 | VT + 真实宿主 scrollback 断言 |
| IR-15 | 不连续 history bootstrap 必须把全部 finalized rows 作为有序 effect batch 插入顶部区域；批量 Ack 必须由 reducer 校验 | scrollback + visible tail + batch-token 回归 |
| IR-16 | `ScreenModel` 只能缓存 bottom inline viewport，任何 viewport diff 都不得清除或寻址 history region | CUP/ED/whole-clear fence 测试 |
| IR-17 | writer 明确零字节失败可用同 token 重试；可能 partial write 必须 Unknown/fail-closed | zero-write retry + short-write no-retry 测试 |
| IR-18 | finalized plain source 的内部空行和 trailing empty row 必须保留稳定 `SourceRange/FragmentID`；finalize 只能提交未 Ack tail | source identity + finalize no-replay 测试 |
| IR-19 | native scrollback 溢出后，resident history tail 必须 sticky top-aligned；scrollback/viewport 边界不得产生非语义空白断层 | VT + 真实宿主连续性断言 |

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
    Origin           HistoryCommitOrigin // Transcript 或 Active
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

> **2026-08-06 disposition**：Phase 3/4 的 production writer 与 tokenized handoff 核心已完成；Phase 0-2/5 的 primary rendering 核心已实施，但 producer、同步 interaction 和 compatibility state 清理继续；Phase 6 已通过完整 Go/race/vet 与一次真实 Windows Terminal/provider 验收，legacy 删除、更多宿主和长会话矩阵仍在进行。详细状态以专项方案 §6 表格为准。下列“未安装”“legacy writer”“bug 仍红”等语句是逐阶段施工时的入口/出口描述，不是当前状态。

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
7. 为 persisted multi-turn history、active reasoning、Markdown、单个超大 cell 增加 VT 最终主屏断言：output region 必须有 retained tail，bottom pane 必须有 active band，overflow prefix 必须在 modeled scrollback。

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
- `EffectResult` 仍只是通用 delivery diagnostic，不能被当作 history Ack。tokenized `HistoryEffectQueue` 使用独立的 Begin/Ack/Fail/Deferred action；它当前只是一套 reducer-owned data plane，尚无 production `TerminalSession` source，也不能据此声称 physical recovery 已完成。
- `RuntimeEvent` reducer 触发 surface facade 时，facade action 进入 controller 的 causal follow-up queue：它们在当前 action 后、下一个外部 mailbox action 前分别按 revision 处理，不占用 external mailbox 容量。这样 mailbox 已满时 reducer 不会等待自身恢复消费；外部 facade 仍按正常 bounded `Post` 路径投递。`UIController` 已增加 `ContextualReducer`/`ReducerContext`，新迁移代码必须使用执行期 capability；旧 facade 的 `PostFollowup` 仅作为过渡适配，并校验实际 reducer goroutine，不能因为全局 `inFlight` 为 true 就被外部 producer 抢占；`PostActionEffect` 也已由 controller 转入同一 follow-up lane，不再误交给外部 effect sink。该机制只解决 Phase 1 legacy adapter 的 re-entry/backpressure，不把 legacy coordinator mutation 误称为终局 reducer/AppState。
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
- `ReplaceTranscriptAction` 同步 Scene snapshot 时不再无条件清空 actor 已发布的 streaming ledger：若 snapshot 与当前 active cell 具有相同 `CellID/Kind/Source/Revision`，或当前 revision 明确更新，则保留当前 `Stable/Enqueued/Acked`；Scene source 前进、cell finalized 或 active cell 被移除时仍替换/清除。这样每个 runtime delta 的 Scene semantic snapshot 与 shadow range update 可以按 causal 顺序合并，queued-but-unacked suffix 不会在下一事件 snapshot 中消失。该规则只维护 AppState data-plane，不改变 legacy presenter ownership。
- live user submit、structured command result 与 local error 这三类非 runtime Scene producer 完成 legacy coordinator 写入并**释放 `c.mu` 后**，也会投递同一 `ReplaceTranscriptAction`；这避免 bounded mailbox backpressure 与 reducer 获取 coordinator mutex 形成锁等待。它们投递完整 immutable Scene snapshot，因此 AppState 不会落后于这三类 direct injection。事件日志 `replayEventLog` 成功重建 Scene 后也只投递一次完整 snapshot，使重放后的 AppState 不会保留旧 transcript。
- 本轮完成 direct supplement 和 direct assistant fallback 的第一批收口：`EventEncoder.SubmitSupplement`/`SubmitAssistant`、`KindSupplement`、`ChangeSetMapper`、对应 `eventLogInjection` 字段与 replay 构成独立 typed data plane；`RenderLocalSupplement`/`RenderLocalAssistant` 只供没有 runtime event 对应物的本地通知或最终回复调用，在 legacy coordinator 写入完成、释放 `c.mu` 后发布 `ReplaceTranscriptAction`。输入拒绝、busy queue、ESC、goal/retry、provider retry、日志保存失败、direct invocation、live transcript supplement 和 legacy non-stream response fallback 均已迁入；历史 replay 明确不注入。`RenderAsyncLine`/`RenderAssistant` 仍专供已经先经 `chatRuntimeEventBridge.Encode` 映射的 runtime timeline/assistant 和显式兼容/replay 路径，严禁在其中无条件补注入，否则会生成重复 Scene cell；对应 no-duplicate 回归已固化。随后，chat-core direct tool 的 `tool_requested`/`tool_result` 已通过 `SubmitToolCall`/`SubmitToolResult` 进入同一 `KindToolChain`：稳定 `tool_call_id` 只创建一个 mutable chain，运行态不提交 legacy history，最终输出合并后一次完成；桥接层把等价 typed runtime event 写入 event log，replay 复建同一 chain，history renderer 则使用 projection-only `RenderReplayedToolChainEvent`，不得二次注入。缺少稳定 call ID 的 direct tool 保守维持 legacy projection，绝不猜测绑定。runtime approval transcript 与 raw system-output writer 仍需按各自 semantic source 逐项审计，不能借此宣称所有 producer 或 terminal writer 已收敛。
- 已进入 actor 的 input/status/prompt/popup/legacy ActiveBand facade action 会更新 `AppState.Bottom`。本轮补齐 prompt reset/rows/notice/editor、incremental input tracking、单独 persistent/dynamic status 与 composer preview 的状态迁移，并以 `Apply`/同步 facade 字节 parity 和 AppState snapshot 回归固化。popup 的 owner/priority、suspended `PopupStack`、tokenized `PopupHandle` begin/update/clear 已有纯 state transition，并且 `BeginPopupInputForOwner*` 先分配 token 再投递 durable action，后续 update/clear 因 FIFO 排在 begin 之后；`AppState()` 深拷贝 stack。它仍是 legacy surface adapter，而不是最终 popup/presenter owner：仍有同步 cursor-move helper、完整 focus policy 和所有 producer 待收敛。ActiveBand 仍标为 legacy projection input，尚不是从 `ActiveCellState` 纯派生的最终实现。
- `ActiveCellState` 现保留 Scene `Kind`，并新增纯函数 `ProjectActiveCellBand`：它仅从 mutable source 的 `Acked.End..len(Source)` 未确认后缀、Cell kind 与 geometry 推导有 role 的 band 行；多行 source 先按逻辑行展开再按 viewport 尾部预算裁剪，UTF-8 半 rune 或非 prefix Ack range 会拒绝投影而非猜测边界。`LayoutAppState` 在**不存在 legacy ActiveBand facade 输入**时才选择该投影；legacy 输入存在时仍单独优先，绝不把两份正文叠加。该 fallback 已有 source range、rich role、resize tail、unsafe range 与 legacy-precedence 测试，但尚未接替 `ActiveStreamController`/`FixedBottomSurface` 的生产显示输入，也没有宣称 streaming range owner 已收敛。
- Phase 5 的首个 data-plane slice 已增加 `UpdateActiveCellAction`：mutable cell 更新携带 `ExpectedCellID/ExpectedRevision` 与完整 source-range ledger，按 `CellID` 做 latest-wins 合并；reducer 只接受严格递增 revision，并校验 `acked <= enqueued <= stable <= source`、prefix/UTF-8 边界。source correction 会清空旧 effect range，不能把 queued-but-unacked 游标带到新 source。该 action 目前是未安装到 production streaming producer 的 typed seam；Scene-derived `ReplaceTranscriptAction` 仍保持全零 range，不能据此宣称单一 streaming owner 已完成。
- Phase 5 的第二个 data-plane slice 已增加 `ActiveStreamController.SourceSnapshot()` 只读迁移 seam，以及纯函数 `AdvanceActiveSource`、`MarkActiveEnqueued`、`MarkActiveAcked`。snapshot 只暴露 semantic source、cell kind 和 byte range，不暴露 `BufferBackend`、rendered rows 或 terminal cache；running tool 的 viewport display 明确不会进入 transcript source。transition helper 保持 `acked <= enqueued <= stable <= source`，source correction 清空旧 effect range，queued-but-unacked tail 在 Ack 前保持可见，并拒绝回退 Ack、乱序 revision 与 UTF-8 半 rune 边界。新增 assistant/tool/finalize/cancel、queued/ack、correction 与 race 回归均已通过。该 seam 仍未接入 `chatInteractionCoordinator`、Scene producer、`FixedBottomSurface` 或 `TerminalSessionExecutor`，也未删除 `streamRenderedPrefixLen`、`streamEnqueuedPrefixLen`、`stableCommitQueue`、`softEmitted*` 等 legacy 状态；Phase 5 streaming single-owner 仍未完成。
- Phase 5 的纯 mapping slice 已增加 `ActiveCellStateFromSourceSnapshot`：`CellID`、`Revision`、`EnqueuedEnd`、`AckedEnd` 必须由调用方显式提供；它只把 assistant/reasoning/tool 的 semantic kind 映射到 Scene kind，并且永远忽略 controller-local `CommittedEnd`。tool/status overlay 携带 display 时会被拒绝为 transcript source，缺少身份、回退 Ack、越界或 UTF-8 半 rune boundary 也会拒绝。该函数和测试是后续 coordinator adapter 的前置契约，尚无 production producer 调用它。
- Phase 5 的受控 shadow adapter 已接入 `chatInteractionCoordinator.RenderAssistantDelta`：在 `c.mu` 内只读取 mounted Scene active cell 与 `ActiveStreamSourceSnapshot`，构造 `UpdateActiveCellAction` 后于解锁之后投递；UI actor reducer 可因此镜像 assistant semantic source，但 legacy `ActiveStreamController`/`FixedBottomSurface` 仍是唯一 production visual owner。adapter 不读取或猜测 physical Ack；没有 mounted Scene cell 时不投递，source correction 时清除旧 range，tool display 只映射为无 source 的 running-tool identity。新增 coordinator unlock、CellID/Revision fence、tool display isolation 与 AppState shadow parity 测试。该 adapter 尚未驱动 `LayoutAppState` 的 production compose，也未接 `TerminalSession`。
- 同一受控 shadow adapter 现覆盖 `SetAgentStageDetail`、`SetToolAgentStage`、`SetToolAgentStageDisplay` 与 `FinishToolAgentStage` 的 running-tool 生命周期：tool display 只产生 `KindToolChain` 的空 source active identity，结束时投递 `ClearActiveCellAction`；所有 action 均在 coordinator 解锁后进入 mailbox。该切片仍不把 tool display 写入 transcript，也不改变 legacy ActiveBand 的 physical owner；production finalization transaction 和 Presenter 尚未迁移。
- reasoning delta 也已进入 shadow source bridge：`RenderReasoningDelta` 将 `ReasoningBlock.RawDisplayText()` 作为 `KindSupplement` semantic source，`CompleteReasoningResponse` 与 `FinalizeReasoningDelta` 均在解锁后投递带 `CellID + Kind` fence 的 `ClearActiveCellAction`。清理不使用 exact revision，允许同一 semantic cell 的 queued source update 先到达 reducer；不同 cell/kind 的 delayed clear 会被拒绝。reasoning source 与 tool overlay 的 mapping 规则已分离，仍不改变旧的 reasoning scrollback/ActiveBand 写入路径。
- assistant 完成路径现有一个受控的 finalization shadow transaction：`CompleteAssistantResponse` 与 `FinalizeAssistantDelta` 在 legacy stream 完成、但 reset 前，读取已由 EventEncoder/Scene 提交的 immutable final snapshot，构造 `FinalizeActiveCellAction{Snapshot, ExpectedActiveCellID, ExpectedActiveRevision, ExpectedActiveKind, ExpectedActiveKindKnown:true}`，并在释放 `c.mu` 后以 causal follow-up 投递。`CellKind` 的零值是合法 `KindUser`，因此不能把零值偷用为“未设置”；旧 action 仅在 `ExpectedActiveKindKnown=false` 时维持兼容 wildcard。reducer 只有在当前 active identity/revision/kind 精确匹配且 snapshot 中同一 cell 已终态时才同时替换 transcript 并清除 active；shadow source update 可能占用与 Scene final 相同的 revision，因此 final Scene revision 允许等于 active fence，严格更小仍拒绝。这个 action 不写 terminal、不猜 Ack、不 append 第二份正文；后续 `ReplaceTranscriptAction` 仍保留为全量 Scene snapshot 的兜底同步。它只是 Phase 5 data-plane 事务，HistoryCommit effect、production Presenter 和 legacy final writer 的删除仍未完成。
- 本轮补充 runtime-event 因果回归：连续 assistant delta 经 `chatRuntimeEventBridge -> UIController` 后，第二个 delta 的 `Stable.End` 在随后 `ReplaceTranscriptAction` 到达后仍保持 source 尾部；覆盖 Scene snapshot 与 shadow range update 的合并顺序。另将 tool-stage shadow 的四个 coordinator adapter 改用 causal follow-up，满 external mailbox 时不会在当前 reducer 内 self-wait，并由 `TestToolStageShadowUsesCausalFollowup` 固化。该测试证明保留 ledger/投递因果不是只在手工 reducer 调用中成立，仍不把它解释为 production handoff Ack。
- Phase 5 的 legacy tool lifecycle slice 已完成 data-plane 接线：`tool.requested`、`tool.progress`、`tool.completed` 及 failed/cancelled 变体在携带稳定 `tool_call_id` 时进入同一 mutable `KindToolChain`，progress 原地更新 source，最终事件把 tool output 合并并只生成一个 committed chain；重复 final 由 encoder 去重。缺少稳定身份或必要 payload 的 legacy tool 事件仍保留 system fallback，不会猜测绑定到其他调用。真实 `chatRuntimeEventBridge -> UIController -> AppState` 序列回归已覆盖请求/进度/完成、active 清理、最终 source 合并与重复完成；该 slice 仍只收敛 Scene/AppState 数据面，不代表 ActiveStream range owner 或 production presenter 已迁移。
- `ui.LayoutAppState(AppState)` 已提供纯内存 layout snapshot：它从 semantic transcript 调用 `scene.LayoutTranscript` 派生 boundary rows，并计算 bottom pane 的 status/prompt/popup/legacy-band row allocation 与 cursor focus。该函数不读取 surface mutex、terminal、ScreenModel 或 effect progress；目前只形成 layout 数据，不生成 physical frame/diff。
- 本轮补齐纯布局的 geometry 输入契约：`BottomPaneGeometryPolicy` 只由 `BottomPaneState + GeometryState` 计算 ActiveBand 尾部预算/顶部间隔、composer margin、popup 预算及 prompt 最大可见行；长草稿保存 logical cursor、absolute visual row、total rows 与 viewport start，`LayoutAppState` 在 width/height generation 改变时从 prompt source 重算可见 text rows，绝不从 surface cursor cache 反读。`LayoutBottomPaneRows` 已生成不含 ANSI 的 bottom reserve row/owner/text plan，并对普通、多行 prompt、popup/composer、短终端压力、窄宽/宽屏切换和同一语义状态的 geometry 循环建立 legacy snapshot parity。新增 `LayoutAppScreen` 纯 screen-row shadow，把 retained transcript 的 CellID/gap 与 bottom owner plan 放进一个 viewport-sized plain layout；mutable cell 明确排除，避免将 active source 与 legacy band 双重纳入。legacy owner map 现在只把实际 prompt 文本 viewport 标为 Prompt，popup 的空输入占位仍是 Gap；诊断快照同时携带 prompt 的 absolute row 与 viewport start，避免将相对 cursor 行误作 geometry-independent source。legacy probe 完成后以 `Resize{Applied:true}` causal barrier 回投已测宽高；reducer 仅在实际几何变化时递增 `Geometry.Generation/LayoutGeneration`，且该回投不再次 probe 或 reflow。新增纯派生、prompt resize reflow、generation、bottom matrix、screen-layout 与 coordinator bridge 回归。
- `LayoutAppScreen` 的 full-screen in-memory parity 已扩展为 owner/text matrix：semantic boundary gap 的 physical owner 与 legacy history 一致为 Transcript，`TranscriptGap + CellID` 仍保留边界语义；纯 `vt.Screen` physical-row expansion 覆盖 deferred wrap、宽字符、leading combining mark、tab stop 和 SGR。矩阵以等价 legacy logical history source 覆盖 popup 空输入 gap、多行 prompt/popup、短屏、超长 tail 和 geometry cycle，不写 terminal；`BottomPaneRowPlan` 明确提供 prompt input start/rows，避免 cursor intent 使用 notice/editor 的 Prompt owner 行；一行 terminal 的 output boundary 是 0。该证据不覆盖 cursor-parking、handoff/scrollback、lease 或 writer failure，仍不是生产 physical Compose parity。
- 任务 7 的纯内存 frame parity 已落地：`ComposeAppTextLayout(AppState)` 复用 `LayoutAppScreen` 的行布局（transcript wrap、mutable cell 排除、bottom RowPlan），只附加 cursor intent——prompt 焦点从 `BottomPaneRowPlan.PromptInputStartRow/PromptInputRows` 显式 metadata 定位输入区并把光标行 clamp 在区内（几何裁剪时绝不落 margin/gap/status），popup 焦点落在最后可见 popup 行（列 = 显示宽度 + 1，与 legacy `moveToPopupInputLocked` 一致）；未知列、隐藏 prompt 或 `BottomFocusNone` 时返回 nil，不猜 streaming 的 Stable/Enqueued/Acked。`FixedBottomSurface.FrameParityWithAppLayout(AppState)` 逐行比较 legacy composed plan 与 snapshot 派生帧的纯文本，返回差异报告（一致为 `parity: identical`，否则逐行 `legacy=/derived=`），仅内存比较、不写 terminal、不拦截 production 渲染；报告的差异即 AppState 快照覆盖缺口（如 legacy history buffer 与 AppState.Transcript 尚未同源时的行数/文本差、legacy ActiveBand 样式化渲染与纯文本投影差）。`app_compose_test.go` 固化行一致性、单/多行 prompt cursor、popup cursor、未知抑制与确定性/深拷贝。
- follow-up 若所属 reducer panic 会被整个丢弃，防止父 action 未提交而 facade/Scene child action 单独提交。该原子性只覆盖 controller action continuation，不等于完整 Scene transaction/terminal transaction 已完成。
- 已补 `HistoryEffectQueue` 的 reducer data plane：eligible finalized **physical display range** 只在 semantic transcript transition 入队；resize 对仍 eligible 的 Pending 仅 rebase，失去 eligibility 的 Pending 只标 Invalidated 而不删除 token，in-flight resize/replacement 使 projection 进入 Unknown；lease 冻结 Begin 但允许已 in-flight Ack；stale generation/recovery barrier 不推进 token 或错误清 Unknown。Ack 释放 effect payload。`HistoryCommitWakeEffect` 只唤醒 consumer 读取 actor 的最新 snapshot，避免把旧 `[]Line` payload 交给 worker；`HistoryCommitExecutor` 以注入 `HistoryCommitSink` 按 token claim/result 做单一 transaction seam，defer 必须证明未写任何字节，possible partial write（包括 sink panic 或缺少底层 error）都走 Failed/Unknown。failure 或 in-flight invalidation 即使经过 visible-frame recovery 也会阻止后续 token 越过未解决 range。只有 terminal owner 在 reset/replacement 后且 current generation source-backed recovery 已确认时，才能投递单调 `HistoryScrollbackReconciled`；它开启新的 physical epoch、丢弃旧 delivery ledger、由语义 transcript 重建新 token，不能以普通 repaint 绕过失败范围。当前没有 production emitter 或 `FixedBottomSurface` 接线，避免 legacy handoff 与新 effect 双写。
- Phase 3 已新增独立的 `TerminalSession` frame/sink seam：`ComposeAppRenderFrame` 从一个 AppState snapshot 生成 viewport-sized rich `render.Line` frame（transcript kind role、typed ActiveBand 与 status document）并同现有 `AppScreenRow.Text` 做逐行 plain parity；eligible finalized `HistoryCommit` 复用同一 transcript role line。`ComposeTerminalFramePlan` 同时携带这两种投影，禁止 rich/text 不一致的 frame 写 terminal。TerminalSession 用调用方显式设置的 `ThemeContext` 编码 structured rows 和 history handoff，theme/profile change 强制 source-backed repaint，flush 时不探测全局 terminal；它私有持有 `ScreenModel` front/back cache、geometry/layout generation、lease、cursor 与 frame counter。initial/resize/lease-release/explicit invalidate 均以 Unknown/full repaint 起始，完整 target write 后才 `ConfirmFlush`，short write/error/panic 保持 Unknown 并丢弃 cursor cache。`TerminalTransactionPlan` 可附带一个已 claim 的 `HistoryCommit`：仅在同 generation、Known、非 lease primary 且 geometry 未变化时，以 `HandoffPlan -> viewport diff -> cursor` 组装为一次 Presenter target write，并在成功后同步 `ApplyRegionAppend` 与 frame；Unknown/resize/lease 时只提交 recovery viewport frame，history 严格 Deferred。新增的 `TerminalSessionExecutor` 是未安装 actor bridge：它读取最新 snapshot、claim oldest token、调用同一 transaction，并回投 Deferred/Ack/Failed 以及 frame 的 `HistoryProjectionInvalidated/Recovered`，用于覆盖 recovery 后再 handoff 的因果顺序；不连接 runtime、`FixedBottomSurface` 或 live terminal。隔离的 `HistoryCommitSink` 仍保留作单 effect fault seam。行到 cell 的展开复用 `vt.Screen`，覆盖 cursor、lease、rich frame、样式 handoff、短写和 recovery。不能据此声称 single-writer 切换完成。
- `/debug display` 现已只读展示 AppState revision/layout generation/geometry/lease、HistoryEffect 状态汇总与 `FrameParityWithAppLayout` 的内存逐行报告；该诊断不读取 historyWindow/scrollback 作为语义源，也不写 terminal。覆盖真实 cursor-parking/handoff/lease 状态的 production full-frame parity、所有 Scene producer action 化和 production `TerminalSession` 仍未实现；`FixedBottomSurface` 仍为 legacy physical adapter。`Resize{Applied:true}` 只是 legacy probe 到 AppState 的数据桥，不是 Presenter 几何 transaction。
- 本轮完成 Ctrl+T pager 的 actor 状态收敛切片：生产 alternate loop 通过一次 `TranscriptPagerView` 读取当前 lease 对应的 `AppState.Transcript`、`Active` 与 `TranscriptOverlay.Pager`，避免把相邻 AppState revision 的正文和锚点混配；滚动、Home/End、`j/k/g/G` 只投递带 `LeaseID` 的 `TranscriptPagerScroll`/`TranscriptPagerSetFollowBottom`，不再在 production loop 内推进第二份 durable scroll state。loop 会比较 actor 已发布的 pager state，即使 action 的 reducer 在按键后的下一 poll 才完成也必定重绘，不依赖下一次输入或 transcript delta；若配置了 actor view 但 poster 不可用，则输入保持只读而不会回退到 local scroll mutation。reducer 拒绝跨 lease 的延迟输入；打开 overlay 前先以 causal `ReplaceTranscriptAction` 发布最新 Scene。无 actor 的 standalone/test loop 仍可使用显式 local fallback。该切片闭合了 Ctrl+T 的 content/view-state 双源问题，但不改变 primary `FixedBottomSurface` writer 或 production presenter/handoff 尚未切换的状态。
- 输入 ingress 已补齐响应性边界：`SetPromptInputSnapshot`/`RenderPromptInputSnapshot` 先同步更新由独立 `promptInputMu` 保护的 coordinator draft cache，再投递带单调 `Sequence` 的完整 `InputEvent` 快照；逐键路径不获取 coordinator 的 terminal/render `c.mu`，因此不会等待 runtime 格式化或 terminal 写入。生产 sequence action 可 latest-wins 合并，且在 actor mailbox 满时使用单一 deferred retry 保存最后一个快照，逐键路径不再调用 `WaitIdle` 或阻塞 `Post`。编辑器状态行 `SetPromptEditorStatusAction` 同样改为 latest-wins coalescable action，并由独立 deferred ingress 在容量为 1 的满邮箱下异步重试；slash completion 对正常文本不再重复投递空 popup 的 `ClearPopupAction`，只在已有 completion popup 时清理一次，避免未启用命令补全时的每键 durable action。`Render=true` 在合并时单调保留，避免较新的 semantic-only snapshot 吞掉待执行的 prompt 重绘；`ResetPromptState`、`DiscardPrompt` 与 shutdown 均推进 sequence 并清除过期 deferred snapshot，旧 action 无法复活已清空草稿。实际 terminal 写入仍只发生在 actor reducer 的既有 surface adapter，未引入第二个 writer，也未切换 `FixedBottomSurface` history handoff。回归覆盖 actor drain 阻塞、容量为 1 的满邮箱、编辑器状态行背压、coordinator render lock 与 reset 后旧快照。

**Primary presenter cutover（2026-08-05）**

- interactive chat setup 现在先给 `FixedBottomSurface` 加 physical-write fence，再创建 `TerminalSessionPresenter` 并通过 `UIController.SetEffectConsumer` late-bind 为唯一 effect consumer；旧 surface 继续承载兼容 facade 状态，但任何 `FlushEffect`、`HistoryCommitWakeEffect`、history transaction、viewport diff 和 cursor 字节都只能经 presenter 内的 `TerminalSessionExecutor` 发出。
- `SetPrimaryPresenter` 在 legacy surface 仍可写时拒绝 attach；surface replacement 在 unified mode 下也会自动保持 fence。关闭顺序为 presenter 先解绑 effect consumer 并 drain worker，随后 actor drain/close，避免 teardown 后仍有 terminal byte。`TerminalSession`/executor 仍会发布到 `ChatSession` 供诊断与既有兼容边界使用，但不应绕过 presenter lifecycle 直接调用。
- composer 的 `OnTerminalWrite` 在 unified mode 被显式 claim，不能 fallback 到 raw `io.Writer`；`InputEvent` 的异步 state 更新触发 presenter frame。因此 mailbox 压力不会阻塞输入，也不会让编辑器和 presenter 两个路径同时移动 cursor。
- 本切片完成的是**primary physical writer authority**，不是迁移终点。`ActiveStreamController`、legacy stable/soft state、`historyWindow` 兼容缓存、所有 producer 的 AppState 覆盖、Ctrl+T pager 的端到端交互/PTY 验收和 raw writer inventory 仍须继续收敛；这些剩余模块不得重新获得主屏写权限。`Ctrl+T` 的 alternate-screen transport 已经由 `ScreenLease` 委派给同一 presenter，不能再 fallback 到 raw stdout。上文标记“TerminalSession 未安装/FixedBottomSurface 是唯一 production writer”的历史描述以本节为准。

**验证复核（2026-08-04）**

- `go test ./cmd/aicli/ui/... -count=1`、`go build ./cmd/aicli/...`、`git diff --check` 全部通过；`go test -race ./cmd/aicli/ui` 的 TerminalSession、HistoryCommit、Compose、ActiveCellProjection 重点集合通过。
- 本轮新增的 `ActiveStream`/`ActiveCell`/mapping race 集合通过（`go test -race ./cmd/aicli/ui -run 'ActiveCellStateFromSourceSnapshot|ActiveCell|ActiveStream|FinalizeActiveCell' -count=1`）。全量 `go test -race ./cmd/aicli/ui/... ./cmd/aicli/commands/...` 仍暴露既有测试隔离问题：commands 屏幕测试并发替换全局 `os.Stdout`，后台 legacy UI actor/surface writer 同时执行 `fmt.Print`/terminal flush，产生 race；该失败不在本轮 source/range seam 路径，不能标为全量 race 通过。
- `/exit` 已纳入 `printDirectInteractiveOutput` 的 surface-aware 完成边界，`TestTTY_LiveLoop_RendersRealScreen` 连续 10 次通过；direct-writer inventory 相应从 `handleCommand` 35 项下降为 34 项。
- legacy tool lifecycle 回归通过：`TestChatRuntimeEventBridge_ToolLifecycleMirrorsSceneActiveCell` 验证真实 UI actor 序列的 mutable tool cell、progress source、final merge/active 清理和重复 final 去重；`TestChatRuntimeEventBridge_IdentitylessToolProgressFallsBackToSystem` 验证无 `tool_call_id` 时保留 system/timeline fallback，不挂载错误 active cell。编码器与 bridge 重点包测试均通过。
- `go test ./cmd/aicli/commands -count=1` 的全包运行仍可能受到既有跨测试全局 `os.Stdout`/异步 spinner 泄漏影响，当前复现的 `TestPromptStartupSessionSelectionWithReader_RetriesAfterInvalidChoice` 与 `TestChatRuntimeEvents_ToolRunningIsViewportOnlyAndFinalCommitsOnce` 单独运行及重复运行均通过。该问题属于基线测试隔离债务，不改变本轮 ActiveCell/ActiveBand 语义实现结论。
- finalization shadow slice 回归：`go test ./cmd/aicli/ui/... -count=1`、`go build ./cmd/aicli/...`、`go vet ./cmd/aicli/ui/... ./cmd/aicli/commands/...` 以及新增的 ui/commands target race 集合均通过。一次 `go test ./cmd/aicli/commands/... -count=1` 全包运行仍在既有终端噪声环境中失败于 `TestChatInteractionCoordinator_MidStreamActiveBandLeavesNoBlankGap`；该测试单独连续运行 10 次通过，失败输出含并发 spinner/ANSI 写入，尚无证据表明它来自本切片。不得把这一结果标为全包绿，也不得为消除 flake 放宽 ActiveBand 无空洞断言。

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
5. finalization 作为一个 reducer transaction：校验 revision、更新 final source、标 Finalized、清 active projection、仅为未 Ack tail 生成 eligible effect、request frame；finalized plain source 的内部空行和 trailing empty row 必须继续携带稳定 `SourceRange/FragmentID`。
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
5. 已通过一次真实 Windows Terminal/ConPTY provider E2E（见 §0 manifest）；继续验证至少一种非 Windows PTY，以及 Windows 的长会话、resize/fullscreen/400+ 行组合矩阵和退出恢复，不能把单次已通过 run 误报为全矩阵完成。
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

# 真实 Windows Terminal buffer/scrollback（在 backend 目录执行）
pwsh -File ..\scripts\test-aicli-windows-terminal-e2e.ps1 -TimeoutSeconds 45

# 真实 provider + 真实 Windows Terminal；测试 prompt 由 --prompt 启动参数自动提交
pwsh -File ..\scripts\test-aicli-opencode-windows-terminal-e2e.ps1 -TimeoutSeconds 180
```

真实 provider 脚本不得通过 `SendInput`、剪贴板或 UI Automation 注入测试 prompt；这些机制只能用于完成断言后的 `/exit` 清理。runner 生成的命令行必须显式包含单一 `--prompt`，并在退出码文件出现后才报告完整 PASS。

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

**切片 13 注记（chat-core direct tool typed chain）**：带稳定 `tool_call_id` 的 `tool_requested`/`tool_result` 已通过 `SubmitToolCall` 与 `SubmitToolResultDisplay` 进入统一 Scene。请求只建立一个 mutable `KindToolChain`，不提交 legacy history；完成块以 `display_head` 作为 canonical source，原始 output/error 仅保留在等价 runtime event log 中，不再追加第二个 output cell。bridge replay 与 live 使用同一 display source，`RenderReplayedToolChainEvent` 明确为 projection-only；无稳定 ID 的 direct tool 保守留在 legacy fallback。新增 direct tool Scene/ActiveBand、event-log replay、history no-inject、single-chain 与 text-parity 回归；该切片仍不连接 TerminalSession/HistoryCommit，也不处理 approval transcript 或 raw system-output writer。

**切片 14 注记（TerminalSession transaction preflight gate）**：补齐 shadow `TerminalSession` 的 history/frame 原子确认门禁。若 HistoryCommit 已在内存中完成 eligibility/preparation、但同一 transaction 的 viewport frame 在任何 terminal bytes 写入前校验失败，history 结果必须为 `Deferred`，不得返回空 success 并让 executor 发布 `HistoryCommitAcknowledged`。回归覆盖确认：无 handoff 或 frame bytes 写出、既有 confirmed projection 不被改写、history token 保留给后续有效 recovery/transaction。该切片不连接 production `FixedBottomSurface`，不改变 lease transport 或 single-writer authority；它只收紧未来 presenter transaction 的 fail-closed contract。

**切片 15 注记（semantic boundary change and history payload rebase）**：`syncHistoryEffectsForTranscript` 现按 CellID/revision/source identity 对比当前候选的 display range、layout generation 与结构化 render lines。pending token 在尚未开始 terminal write 时遇到前置 cell boundary/gap 变化，保留原 token 并 rebase payload；in-flight token 遇到同类变化则标记可能已部分写入并进入 ProjectionUnknown，禁止旧 payload Ack 或生成重复 token。新增 pending rebase、in-flight invalidation/no-duplicate 回归；layout/Scene 仍为纯 data-plane，未连接 production writer。

**切片 16 注记（priority transcript and raw tool-output ownership）**：approval/question 的 request 现只建立待同步输入使用的 stable identity，不建立 mutable Scene item；同步输入结束后 `SubmitPriorityPromptTranscript` 才追加一个 completed `KindPriorityPrompt`。这保证先前已提交的 permission/reuse hint 在 legacy history、Scene、AppState 中都先于 prompt transcript；resolved/answered 可在 transcript 前到达，但只记录生命周期，不得阻断其后追加，重复 request/resolution 不会追加第二个 cell。bridge 将 request key 与最终转录落入 event log，replay 重建同一单 cell；没有 runtime source 的 permission hint、reuse hint、local decision 才通过 `RenderLocalSupplement` 进入独立 typed cell，已 Encode 的 runtime event 不再误用 `RenderAsyncLine` 注入。对稳定 `tool_call_id` 的 direct shell 输出，`chatLiveToolOutputWriter` 只更新 ActiveBand stage，line/byte limit notice 同样不进入 history；actor runtime 顶层 retained `OutputMirror` 已删除，runtime `tool.progress` 是其唯一 live projection。无稳定身份或非 owned 交互保留 legacy fallback，避免并发 tool 归属猜测或静默丢失数据。新增 priority order/race/live/replay/single-cell、tool output no-commit/limit/actor-mirror 与专项 race 回归；这仍是 data-plane/legacy adapter 收敛，不连接 production TerminalSession，`FixedBottomSurface` 仍是唯一 production physical writer。

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
