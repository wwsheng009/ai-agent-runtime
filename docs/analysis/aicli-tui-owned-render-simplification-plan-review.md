# aicli TUI owned 渲染简化方案评审

评审对象：`docs/plan/aicli-tui-owned-render-simplification-plan.md`

对照：`docs/plan/aicli-tui-unified-render-architecture-refactor-plan.md` 与 `E:\projects\ai\codex\codex-rs\tui\src`

状态：**completed / post-cutover verified（原方案 rejected；修订方案已实施并完成核心实机验收）**

日期：2026-08-06

## 0. 2026-08-06 实施后复评

本节是当前权威结论，覆盖后文 2026-08-04 对“尚未安装”“仍由 legacy writer 负责”的施工期描述。修订方案的核心目标已经落地：production interactive terminal 只有 `TerminalSessionPresenter/TerminalSession` 一个 physical writer；`AppState/Scene` 是语义来源；tokenized `HistoryEffectQueue` 管理交接；top terminal-owned history region 与 bottom application-owned inline viewport 分离；`ScreenModel` 不再持有或重画 finalized history；active stable prefix 只有 Ack 后才从 live projection 移除；finalize 只提交未 Ack suffix；Markdown 交接保留结构化 IR。

真实 provider Windows Terminal run `output/aicli-terminal-e2e/opencode-wt-7347d2bf0b0346c1ba975085b3c8b2eb/manifest.json` 给出端到端证据：40/40 finalized marker 各一次、严格有序且连续，marker 01 在完整 `DocumentRange` 但不在 visible range，marker 40 可见；provider chat artifact 中唯一 reasoning summary 的完整非空白内容在 `DocumentRange` 中恰好投影一次并位于首 marker 前；raw runtime event 标签未泄漏，异常空行数为 0，三次 UIA snapshot 稳定。`/exit` 通过按 executable path + run ID 精确定位的 `AttachConsole+WriteConsoleInputW` helper 发出，helper/runner code 均为 0，forced cleanup 为 0。`--prompt` 与 `chat --prompt` 的首次自动提交、交互继续和 `--no-interactive` 退出边界均有命令级回归。

这里的 reasoning 结论是“语义内容正常显示且顺序正确”；未出现的是 raw `assistant.reasoning` 事件名称，不是 reasoning 正文本身。

实施后又发现并关闭两个原评审未充分具体化的缺口：

1. finalized plain source 的内部空行曾没有非空 source identity，导致 Ack prefix 后的 suffix planner 放弃整个 cell；现以 newline byte range 与 fragment identity 表达内部/尾随空行。
2. native scrollback 已发生后，resident history 继续 bottom-align 会在 capacity 增长时把空 headroom 插入连续信息流；现首次真实 overflow 后切为 sticky top alignment，并覆盖 shrink/grow、alternate-screen round-trip、partial/zero write 与 resize rebuild。
3. 成功的 `llm.request.finished` 曾被误当作 assistant semantic final，提前重置 active stream，使稍后到达的 authoritative `assistant_message` 无法提交最后 coalesced tail；现成功 request-finished 只作为 transport boundary，失败、interrupt、session end 和 run-end fallback 才可提前收尾。

因此 G1-G12 对核心 render path 的 disposition 为 **implemented and verified**，而不是继续停留在 proposed/implementing。仍在进行的 compatibility state 删除、剩余 producer/同步 interaction effect 化、更多 terminal host 与长会话矩阵属于后续工程清理，不允许重新引入旧 whole-screen viewport、多个 writer、geometry-driven commit 或无类型 frontier。

## 1. 结论

原方案对 `historyWindow/native scrollback/ScreenModel` 三方对账、`commitExcess -> Invalidate -> full diff -> CommitRange` 补偿链和测试语义误区的诊断是准确的；但“两个物理所有者 + 全局 Frame/Scrollback mode + `committedBoundary int` + `ScrollExistingRows`”不能作为目标架构。

原评审曾把该核心判定为“完整、逻辑自洽、可执行”，该结论撤回。原因不是方案范围偏窄，而是核心状态模型本身遗漏 semantic truth/effect acknowledgement，并与上位母计划及 Codex streaming 机制冲突。

2026-08-03 修订版已经：

- 明确 unified plan 是唯一规范源；
- 将 UI actor/reducer、AppState、effect queue、Presenter transaction 纳入范围；
- 将 semantic lifecycle 与 physical projection lifecycle 分离；
- 用 CommitToken/CellID/Revision/Range/Generation 替代无类型 boundary；
- 把 geometry scroll 降为 Presenter 可选优化；
- 保留一个合法的 queued/acked streaming cursor owner；
- 增加 short write、Unknown recovery 和 Ack/Fail 测试要求。

因此修订版可作为迁移子计划批准，但不得脱离母计划独立改变终局。

## 2. 2026-08-04 pre-cutover 实现判断（历史）

> 本节保留评审时的迁移依据，不描述 2026-08-06 production 状态；当前结论只取本文 §0。

### 2.1 多个状态源仍在生产路径

同一 assistant 输出目前跨以下载体和游标推进：

1. runtime bridge event/model/Scene shadow；
2. coordinator `streamBuffer`、rendered/enqueued prefix、stable commit queue；
3. ActiveStreamController stable/tail；
4. assistant transcript emitted/enqueued range；
5. surface `historyWindow/softOutput/handoffFrontier`；
6. ScreenModel front/back；
7. native scrollback。

这些状态中，semantic source、stream queue progress、terminal projection 都有存在必要；问题是边界没有类型化且多个层分别决定同一范围是否应输出。

### 2.2 FramePump 不是 UI event loop

`UIController` mailbox 与 Phase 1 legacy reducer 已存在；普通 runtime event、input snapshot、explicit resize、surface facade 和 lease barrier 已进入该 action 顺序。FramePump 的 dynamic-status/stable-commit/prompt callback 只投递 `Timer`，active-stream callback 只投递 coalescable `DrawRequested`，不再在 callback 内获取 coordinator/surface 锁或执行 mutation。

这仍不是终局 UI event loop：reducer 仍调用 coordinator/surface legacy adapter，审批/问答仍在 bridge worker 同步等待 stdin，`EffectResult` 也尚无 TerminalSession 来源。现有 tokenized `HistoryEffectQueue` 与注入 sink executor 仅验证 reducer/effect 生命周期，未接 production terminal，不能与 legacy handoff 并写。已补 Phase 1 re-entry guard：reducer 触发的 facade mutation 进入 actor causal follow-up queue，在当前 action 后、下一外部 action 前以独立 revision 执行，不占用 bounded external mailbox，因此满队列不会形成 self-wait。该 guard 只封住 adapter 死锁，不能替代后续纯 reducer output/effect、完整 AppState 与 Presenter 收敛。

新增的 `TerminalSession` 仍仅是未安装的 physical-frame seam：`ComposeAppRenderFrame` 已从 AppState 构造 rich `render.Line` viewport frame，并以逐行 plain parity 防止结构化样式与文本 row 漂移；TerminalSession 以显式 ThemeContext 编码该 IR，theme/profile transition 会触发 recovery。它验证 viewport front/back 的 Known/Unknown、cursor、generation、lease release 与写失败恢复；`TerminalTransactionPlan` 已能在 Known、同 generation、未 resize 的 primary 上把一个已 claim history handoff、viewport diff 与 cursor 合并成一次 target write，Unknown/resize/lease 时 history 必须 Deferred 并先完成 recovery frame。`TerminalSessionExecutor` 也只是一条测试/迁移 worker seam，固定 actor claim、fresh snapshot、typed outcome 和 projection invalidated/recovered 的回投，未接生产 effect/runtime。它没有 production executor 或 runtime/`FixedBottomSurface` 接线，隔离 sink 也只作 fault seam，因此不改变“production legacy writer 仍是唯一生效路径”的状态口径。

### 2.3 Scene 仍是 transcript-only

当前 `ui/scene.TuiScene` 已有 cell、transaction、ChangeSet mapper、RenderText 和 parity 测试，但代码明确只包含 transcript。Phase 2 已增加 `ui.AppState` snapshot 并由 `UIController` 持有：普通 runtime event 的 Scene snapshot、geometry/lease 与已经 action 化的 input/status/prompt/popup/legacy ActiveBand 都可进入该 snapshot；live user submit、structured command result、local error 三类 direct injection 在释放 coordinator mutex 后也投递完整 Scene snapshot，避免 actor 背压锁等待；事件日志 replay 成功重建 Scene 后也投递一次完整 snapshot；mutable Scene cell 可派生带 kind 的 active semantic source。本轮还把 prompt reset/rows/notice/editor、incremental input tracking、单独 persistent/dynamic status 与 composer preview 收敛到相同 `BottomPaneState`，并以 Apply/sync parity 和 snapshot isolation 测试约束。popup 的 owner/priority、suspended `PopupStack` 与 tokenized `PopupHandle` begin/update/clear 现已按同一份纯 `BottomPaneState` transition 更新，handle 在入队前分配，因此 lifecycle 不必直接 mutation surface，coordinator 也会应用 `UpdatePopupAction`；snapshot 会深拷贝背景 layer。`LayoutAppState` 已作为纯内存派生，使用 Scene boundary rows 和 bottom row allocation 产生 layout snapshot，不读取 terminal/surface mutex。本轮还把 ActiveBand/prompt/popup 的 geometry policy 抽为 `BottomPaneState + GeometryState` 的纯函数；prompt viewport 保留逻辑 cursor 与 source 可重新测量的状态，legacy probe 只经 `Resize{Applied:true}` barrier 回投已测 geometry；`LayoutBottomPaneRows` 已以 plain text/owner 输出 bottom reserve，并覆盖普通/多行 prompt、popup/composer、短终端压力和 geometry 循环的 legacy snapshot parity；新增 `LayoutAppScreen` screen-row shadow，把 retained transcript 的 identity/gap 与 bottom overlay 纳入同一 plain viewport，并排除 mutable cell 防止 active/band 重复。`ProjectActiveCellBand` 已能从 active source 的未 Ack 后缀按 UTF-8 safe range、逻辑行、viewport 尾部预算与 semantic role 生成 candidate；仅当 legacy facade band 缺席时才将 candidate 放入 BottomPane row plan，legacy 输入存在时严格优先，因而不会把同一正文并排绘制两次。legacy owner annotation 仅覆盖实际 prompt 文本，避免把 popup 空输入 gap 误判为 Prompt。它仍不是完整收敛：现有 ActiveStream/legacy band producer 和 streaming range owner 尚未迁移，同步 cursor-move helper、完整 focus policy 与全部 producer 未迁移，physical Compose/Presenter/effect queue 仍不存在；`AICLI_SCENE_PRESENTER` 只覆盖部分完整块。

补充：`LayoutAppScreen` 已有 full-screen in-memory owner/text matrix。semantic boundary gap 的 physical owner 与 legacy history 一致为 Transcript，`TranscriptGap + CellID` 保留边界身份；physical-row expansion 使用纯 `vt.Screen`，覆盖 deferred wrap、宽字符、leading combining mark、tab stop 与 SGR。矩阵以等价 legacy logical history source 覆盖 popup 空输入 gap、多行 prompt/popup、短屏、超长 tail、geometry cycle 和一行 terminal；`BottomPaneRowPlan` 的 explicit prompt input start/rows 避免 cursor intent 误用 notice/editor context。该 shadow 不读 terminal/surface，未覆盖 cursor-parking、handoff/scrollback、lease 和短写，不能作为 physical Compose 或 single-writer 已完成的证据。

补充状态（Phase 5 data-plane slice）：`UpdateActiveCellAction` 已作为未安装 seam 提供 `CellID + ExpectedRevision` 因果栅栏和 `Stable/Enqueued/Acked` range invariant；同 CellID mutable update 可合并，source correction 会清除旧 range。该切片不改变上段结论：`ActiveStreamController`、`assistantTurnTranscript`、`softEmitted*` 与 production presenter 尚未收敛，`TerminalSession`/effect queue 仍只用于迁移测试和 fault seam。

新增状态（Phase 5 legacy tool lifecycle）：带稳定 `tool_call_id` 的 `tool.requested`、`tool.progress`、`tool.completed` 及 failed/cancelled 事件现已按同一 mutable `KindToolChain` 编码；进度更新复用原 cell，终态合并 output 后只提交一个 committed chain，重复 final 幂等去重。缺少调用身份的 legacy tool 事件仍降级为 system，避免错误绑定。真实 bridge/UIController/AppState 集成测试已覆盖该序列及 active 清理，但这只是 Scene/AppState data-plane 覆盖，不能改变 streaming range owner、legacy surface 或 production TerminalSession 尚未切换的审查结论。

本轮补充了 shadow 的两个迁移期安全条件。第一，`ReplaceTranscriptAction` 同步相同 mutable source 时保留 actor 已发布的 `Stable/Enqueued/Acked` ledger，source 前进、finalize 或 active cell 移除才替换/清除；连续 runtime delta 集成回归已证明下一 Scene snapshot 不会退回该 range。第二，tool-stage 的四个 shadow adapter 与 assistant delta 一样使用 causal follow-up，满 external mailbox 时不会使当前 runtime reducer 等待自身恢复消费。两者都只保证 AppState data-plane 的因果一致性与无自等待，不构成 terminal handoff Ack、Presenter 接线或 single-writer 切换。

### 2.4 当前专项状态

Phase 1 action adapter 已部分完成：mailbox 分类、replay/coalescing/barrier/shutdown/starvation 测试，以及 runtime/input/resize/timer/facade/lease 的首批生产接线均已落地；新增 mailbox 饱和下的 reducer-facade causal follow-up 回归，防止同一 actor 自等待。`UIControllerState` 只记录 geometry/lease/draw/effect-result barrier facts，不能被误称为完整 AppState；`EffectResult` 也不能当作 ack，approval/question 仍是明确例外。

顶部空白专项已有回归覆盖，但 band shrink/history 重复的根因仍在多个 handoff owner。局部 ScreenModel 滚动同步和 action 顺序只能减少竞态，不能替代 AppState、TerminalSession 和 tokenized HistoryCommit。

## 3. 原方案缺陷及处置

| ID | 缺陷 | 严重性 | 修订处置 |
| --- | --- | --- | --- |
| G1 | 只有两个物理所有者，遗漏 semantic truth 与 pending effect | blocker | 增加 AppState、HistoryEffectQueue、TerminalProjectionState |
| G2 | 全局 Frame mode -> Scrollback mode 不符合逐 cell 生命周期 | blocker | 改为每个 cell/range 的 Retained/Pending/InFlight/Acked |
| G3 | `committedBoundary int` 没有单位且不能跨 resize | blocker | 使用 Token + CellID + Revision + Range + LayoutGeneration |
| G4 | “写后不保留副本”会破坏 replay/reflow | blocker | 只释放 Ack 后 effect payload，保留 semantic source |
| G5 | `AppendNewRows` 把已在 ActiveBand 可见的 stable range称为新内容 | high | 改为 `HistoryCommit` effect，表达物理交接而非首次显示 |
| G6 | `ScrollExistingRows` 被提升为业务几何原语 | high | geometry 只改 state/layout；scroll 仅为 Presenter 优化 |
| G7 | INV-S3 要求 ScreenModel 始终一致，忽略短写/lease/resize | blocker | 引入 Known/Unknown projection 与 recovery barrier |
| G8 | 没有 UI actor/reducer，FramePump callback 仍可 mutation + flush | blocker | Phase 1 先建立 UIAction mailbox 和唯一写者 |
| G9 | ActiveBand、prompt、popup、status 没有统一 bottom state | high | 纳入 BottomPaneState、focus/cursor/layout allocation |
| G10 | 以字符串/hash 计数证明 exactly-once | high | 测试/观测改用 semantic ID 和 CommitToken |
| G11 | 删除所有 prefix cursor，与 Codex queued/emitted 机制冲突 | blocker | 允许一个类型明确的 source-range/effect owner |
| G12 | VT 测试拟模拟 native scrollback 拉回 | high | VT 记录 effects/scrollback，但正确性不依赖 pullback |

原评审中的 G1–G7（Scene 悬空、coordinator 双路径、调度分裂、光标无模型、legacy 删除、性能、测试范围）仍成立；原 G8“进程级文本重复计数”被替换为 token/range 观测，因为相同文本可能属于不同 cell，repaint 也可能合法重发。

## 4. Codex 对照结论

Codex 值得迁移的不是某条 ANSI 序列，而是职责和时间顺序：

1. `App` 单事件循环串行仲裁 app/input/thread/server event；
2. `FrameRequester` 只合并 draw，不执行业务 mutation；
3. `transcript_cells` 保存 semantic history，active cell 保存 mutable tail；
4. finalized rows 先进入 pending history queue；
5. synchronized update 中先 flush pending history，再 draw viewport；
6. viewport buffer 与 native scrollback 物理区域分离；
7. resize 从 transcript source rebuild；
8. streaming controller 明确区分 enqueued 与 emitted stable range。

由此得到三个纠正：

- “write-once”描述 finalized semantic cell/commit identity，不表示终端字节永不因 repaint 重发；
- “写完即忘”描述 pending display batch，不表示删除 transcript source；
- prefix cursor 不是天然技术债，重复 owner 和无类型 cursor 才是。

## 5. 修订版完整性评估

| 维度 | 判定 | 说明 |
| --- | --- | --- |
| 问题基线 | 完整 | 保留专项测试、调用链和多游标事实 |
| 上位关系 | 完整 | unified plan 唯一规范，P5 historical |
| 状态模型 | 完整 | semantic/active/bottom/geometry/projection/effect 分层 |
| 事件流 | 完整 | UIAction mailbox + reducer 唯一写者 |
| 屏幕管理 | 完整 | Presenter 独占、Known/Unknown、cursor/generation |
| handoff | 完整 | token/range + pending/inflight/acked + Ack/Fail |
| geometry | 完整 | layout mutation 与 history commit 解耦 |
| streaming | 基本完整 | 规定单 owner；具体 source-range API 在实施阶段落地 |
| 测试 | 完整 | semantic/effect/fault/race/terminal 矩阵 |
| 迁移 | 完整 | 先 owner/state，再 effect/handoff，最后删除 |

## 6. 实施门禁

修订版批准不等于允许跳过阶段。以下条件是开始删除旧状态前的硬门禁：

1. `UIController` 已成为唯一 AppState 写者；
2. FramePump callback 已全部退化为 action producer；
3. AppState snapshot 能完整表达 transcript/active/bottom/geometry/lease；
4. TerminalSession 有 Known/Unknown 和 short-write 测试；
5. HistoryCommit 有 token/range/generation 和 Ack/Fail；
6. 当前 Scene shadow parity 没有被误当成终局 presenter；
7. production 同一时刻只有一个 terminal writer；
8. geometry change 测试断言 effect queue 不增长。

以下实现应在 code review 中直接拒绝：

- 新增无单位 `committedBoundary` 或字符串 hash frontier；
- 使用 Scene revision 代替 terminal effect ack；
- 在 setter/timer callback 中直接 render/flush；
- band grow/shrink 调用 history commit；
- 失败后在物理状态不确定时直接重试相同 effect bytes；
- 从 ScreenModel/VT/native scrollback 反推 semantic source；
- 为了删 prefix 字段而丢失 queued-but-unacked 范围。

## 7. 最终意见

原 2026-08-02 方案：**拒绝实施，保留故障分析价值。**

2026-08-03 修订方案：**批准为统一架构下的迁移子计划。** 它现在覆盖了用户关心的历史重复、状态管理、屏幕管理、组件协调和事件流，不再通过另一套局部边界状态机绕过 Scene/UI owner。

剩余风险主要在实现而非文档方向：Go 现有 coordinator/surface 体量大，actor adapter 阶段必须保证视觉行为不变；terminal short write 的“不确定是否部分生效”需要明确 recovery 策略；streaming queued/acked range 必须集中但不能被粗暴删除。这些均已进入方案测试矩阵和阶段出口。

2026-08-06 实施后 disposition：**核心 render cutover accepted and verified。** G1-G12 的架构门禁已在 production path 落地，short/zero write、active prefix/final suffix、空白 source row、sticky top alignment、resize/alternate round-trip 及真实 Windows Terminal native scrollback 均有回归证据。未完成项仅按母计划继续做 compatibility/producer cleanup 和更广宿主矩阵，不再阻塞本专项核心目标状态。
