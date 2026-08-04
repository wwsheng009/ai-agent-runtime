# 统一渲染编码器：迁移路线图与既有渲染计划的衔接

> 状态：方案草稿（待评审）
> 日期：2026-08-02
> 上位方案：[aicli-event-stream-rendering-order-unified-encoder-plan.md](./aicli-event-stream-rendering-order-unified-encoder-plan.md)
> 配套文档：[渲染模型数据结构规格](./aicli-event-stream-rendering-order-render-model-spec.md) ｜ [EventEncoder 接口设计](./aicli-event-stream-rendering-order-event-encoder-api-design.md)

本文回答两个问题：**统一编码器方案与现有渲染/UI 计划是什么关系**，以及**按什么顺序落地**。

---

## 1. 文档关系总览

统一编码器方案是**数据面（事件 → 模型）**的收敛；既有渲染文档是**渲染面（模型 → 屏幕）**的收敛。二者在"有序、带身份的信息块"处对接：

```text
上游事件流 ──► EventEncoder（新）──► RenderModel / ChangeSet（新）
                                        │
                    ┌───────────────────┴───────────────────┐
                    ▼                                       ▼
         unified-render-architecture（Scene/事务/排序规则）   render-engine-module（Frame/布局/输出）
                    │                                       │
                    └───────────────────┬───────────────────┘
                                        ▼
                                ui-ux-rendering（Document/主题/IR）
                                        ▼
                                    物理终端
```

| 既有文档 | 与本方案的分工 | 本方案提供的输入 |
| --- | --- | --- |
| [aicli-tui-unified-render-architecture-refactor-plan.md](./aicli-tui-unified-render-architecture-refactor-plan.md) | 渲染面长期终局：Scene 数据模型、事件事务、screen 所有权、history/cell/gap 规则 | `Item`（带 `ID/Seq/Status`）、`ChangeSet`（事务输入）、Tail 锚点 |
| [aicli-tui-transcript-overlay-renderer-mode-plan.md](./aicli-tui-transcript-overlay-renderer-mode-plan.md) | primary/alternate RendererMode、`Ctrl+T` transcript pager 与 history handoff 的实施顺序 | 完整 transcript cell、mutable active cell 的 revision/source range |
| [aicli-tui-render-engine-module-design.md](./aicli-tui-render-engine-module-design.md) | 渲染引擎模块：FramePump、ScreenModel、Presenter、行所有权 | `ChangeSet` → `Update(ScenePatch)` 的标准输入；CellID 由 `Item.ID` 提供 |
| [aicli-ui-ux-rendering-codex-reference-plan.md](./aicli-ui-ux-rendering-codex-reference-plan.md) | 内容渲染 IR：Document/Block/Span、主题、ANSI 安全 | `Item.Head`（RichDocument）→ `Renderable` 的输入 |
| [aicli-tui-render-data-plane-codex-migration-plan.md](./aicli-tui-render-data-plane-codex-migration-plan.md) | 渲染面/数据面隔离历史与 P1–P5 实施记录 | 数据面（transcript）的**产生端**收敛：事件不再直接进 transcript，先经编码器 |
| [aicli-ui-refactor-codex-inspired-plan.md](./aicli-ui-refactor-codex-inspired-plan.md) | 早期 UI 组件化与交互阶段规划 | 其中"事件驱动与限频刷新"的事件源由编码器供给 |

**结论**：本方案不替代上述任何文档，而是它们的**上游补全**——把"事件如何变成有序、带身份的模型"这一层补上，让渲染层各计划可以按契约消费。

---

## 2. 迁移原则

1. **渲染层先行解耦，编码器后接入**：渲染层先按"按 ID 消费"改造（对现有 cell
   提供默认 ID），编码器接入时渲染层无感知。
2. **不重复造机制**：现有单 goroutine FIFO（`chat_runtime_events.go:566-589`）
   保留为**编码器的调用循环**，只是循环体从"直接渲染"改为 `Encode`。
3. **每阶段可回退**：编码器接入点以 feature 开关控制；关闭时走旧路径。
4. **与既有计划并行推进**：unified-render / render-engine 的 Scene 终局
   落地时直接消费编码器输出；不必等对方完成。

---

## 3. 分阶段迁移

### P1 —— RenderModel + Item 规格落地（纯新增，无行为变化）

- 新增 `RenderModel` / `Item` / `Tail` / `ChangeSet` 类型（render-model-spec §2/§3/§6）。
- `historyCell` 接口增加 `ID()/Seq()/Status()/CauseID()`，提供指针地址默认实现
  （兼容过渡，render-model-spec §5.1）。
- **验证**：类型单测；现有渲染行为零变化。

### P2 —— EventEncoder 骨架 + 全量映射表

- 新增 `EventEncoder`（event-encoder-api-design §1），实现 14 行映射表
  （§3）中的事件类型。
- 接入 `chat_runtime_events.go` 事件循环：`Encode(ev)` 产出 `ChangeSet`，
  同时**继续走旧渲染路径**（双跑模式，变更集仅记录不应用）。
- **验证**：编码器单测（顺序/幂等/乱序/穷举）；双跑对比日志中模型顺序与
  旧路径渲染顺序一致。

### P3 —— 渲染层切换：只消费 ChangeSet

- 渲染层事件处理改为：收到 `ChangeSet` → 映射为 `SceneTransaction`
  （AppendCell/UpdateCell/FinalizeCell/RemoveMutableCell）→ 提交。
- 删除 `orderAssistantDelta` 重排路径；`completeBlockOutput` 语义上移为
  `Item.Status` 终态 + 渲染层 BoundaryPolicy。
- ✅ 前置组件 **BoundaryPolicy** 已落地（`ui/boundary` 纯函数）：`ResolveGap`
  按 unified plan §7.3 规则表输出 0/1 gap；测试固化规则表全行 +
  INV-GAP-02/05 不变量。渲染层切换时直接消费，不再由调用点推断空行。
- ✅ **Scene 数据层核心已落地**（unified plan P4–P9 的 `ui/scene` 包）：
  `TuiScene`（CellID/Sequence 分配、revision、ChainKey 归组）、
  `SceneTransaction` + `SceneController.Submit`（快照式原子回滚）、
  `LayoutTranscript`（gap 决策委托 `boundary.ResolveGap`）；~30 个测试全绿。
- ✅ **`ChangeSet` → `SceneTransaction` 映射已落地**（`ui/scene/from_changeset.go`
  的 `ChangeSetMapper`）：`ItemChange.Op` 直接映射为
  `AppendCell/UpdateCell/FinalizeCell/RemoveMutableCell`；CellID 由
  `Item.ID`（"item-{n}"）解析，重放时身份稳定（INV-SCENE-02）；cell
  Revision 由映射器统一递增（tool 输出合并与 tool_call 自身更新共享
  同一 cell，INV-SCENE-03 单调）；tool_output 按 CauseID 合并进链首
  （§7.3 稠密，不新建 cell），孤儿输出独立成块并计数；tool_call Head
  恒定（编码器事实），演进时拆分替换保留已合并输出；未知 ItemKind
  降级为诊断 cell；纯 update 事务标记 `FlushCoalescable`。测试覆盖
  映射表全行、合并/孤儿/移除约束、revision 单调（影子状态）、
  重放确定性与事务原子性。
- ✅ **ChangeSet 消费端（bridge 接入）已落地**：`chatRuntimeEventBridge`
  在事件接入点即时映射提交 ChangeSet（`applyChangeSet` →
  `ChangeSetMapper.Apply` → `SceneController.Submit`，`sceneMu` 保护，
  失败计数/最近错误入诊断）；会话重启后 `replayEventLog` 按 append-only
  事件日志幂等重建 Scene 并清零诊断；`/debug` 输出 "Unified Render
  Scene:" 审计段（Cells/Revision/Apply Failures/Last Error，快照与模型
  对照：CellID 应等于 `Item.ID` 数字部分、顺序应等于模型数组顺序）。
  测试覆盖模型顺序跟随、重放重建、失败计数、乱序增量一致性
  （`chat_runtime_events_scene_test.go`，4 个，含 `-race` 通过）。
- ✅ **渲染层切换可行性已固化**：`TestRenderLayer_GapParity_LegacyCoordinatorVsLayoutTranscript`
  双跑同一会话序列（user→assistant→supplement→user→assistant→assistant）：
  旧路径空行序列（真实 coordinator 方法 + `writePromptGapLocked` 消费）与
  `LayoutTranscript` gap 行序列逐项一致（6 内容行 / 5 gap 于 [1 3 5 7 9]）；
  `/debug` 审计段增加 `Layout Rows`/`Layout Gaps` 摘要（见 unified plan §21 切片 7）。
- ✅ **渲染层文本投影 + 完整文本等价已固化**（切片 8）：
  `ui/scene.RenderText` 把 Scene 快照投影为最终文本行（gap 行 → 空行，
  无状态纯函数，replay/live 复用）；`TestRenderLayer_TextParity_EventStreamVsLegacyCoordinator`
  用真实事件流（LLMRequestStarted/AssistantDelta/AssistantMessage/Finished）
  驱动真实 bridge 事件接入点，`RenderText` 输出与旧路径 coordinator 输出
  **逐行一致**（含内容文本，3 行 `["你好" "" "世界"]`）；tool 链 gap 结构
  测试 + `/debug` 审计段 `Layout Text Rows`（见 unified plan §21 切片 8）。
- ✅ **`RenderText` 已接入真实写入路径（运行时双跑文本对照，切片 9）**：
  coordinator 完整块提交点 `writeRowsLocked` 挂可选探针 `textParityFn`，
  每块写出后把实际行序列（含跨块 gap 空行）交给 bridge `checkTextParity`，
  与 Scene 快照 `RenderText` 对应片段逐行对照（越界/不等记 missed + 最近
  不一致详情）；构造器与 `ensureChatRuntimeEventBridge` 双保险接线；
  `/debug` 审计段新增 `Text Parity Blocks/Matched/Missed/Last Error`。
  测试：真实事件流 + 自动接线完整块全部 matched、Scene 落后时第 2 块越界
  被检出（证明探针真实对照）、无 bridge 时旁路零行为变化（见 unified plan
  §21 切片 9）。
- ✅ **用户输入数据面通道已闭合（切片 10）**：
  runtime 事件流原本没有用户输入事件，真实 Scene 永远没有 user cell，
  切换后用户消息前的 gap 决策失去数据面来源；本切片补齐：编码器
  `SubmitUserInput` 把用户输入提交为 KindUser 终态块，bridge
  `submitUserInput` 经 `renderMu` 与事件循环串行化、走 `applyChangeSet`
  同一提交路径并落事件日志；`replayEventLog` 支持事件 + 用户输入混合
  记录按全序重放恢复；coordinator live 用户提交点注入、历史回放路径
  不注入（避免重复 cell）；探针 `checkTextParity` 重构为按 cell 对照
  （user cell 剥离 `"> "` 样式前缀与 prompt 重绘 gap）。测试：交错真实
  序列（U→turn→U→turn）4/4 matched + 全量文本/gap 位置一致、11 条混合
  记录日志重放与实时等价、回放不注入/live 注入边界（见 unified plan
  §21 切片 10）。
- ✅ coordinator gap 决策状态机已切换（unified plan §21 切片 11）：
  `completeBlockOutput` 全局布尔与 `gapForTopLevelMessage`/`gapForEventBlock`/
  `gapIfPriorComplete` 旧 helper 已删除，完整块写入统一
  `gapBeforeBlockLocked(next boundary.CellMeta)` 委托 `boundary.ResolveGap`
  规则表决策并 `markBlockCommittedLocked` 推进状态；prompt 重绘 gap 由
  `gapPreWritten` 显式消费；流式残差/divider/稳定提交按流 ID 提交，
  `resetStreamLocked`/`resetBlockBoundaryLocked` 重置流 ID（跨 turn 不串 ID）。
- ⏳ 剩余：presenter/渲染层切换与旧路径删除（以 Scene 快照
  `RenderText` 为 presenter 写出行源；切片 7/8 parity 固化完整文本等价、
  切片 9 探针 + 切片 10 用户输入通道使真实数据面 cell 序列 == coordinator
  完整块序列对全部 top-level 块（含用户输入）成立，UI 主渲染仍走双跑模式
  旧路径）。
- **验证**：既有 UI 行为回归（历史顺序、工具链空行、assistant 流式、
  并行工具交错）；乱序注入测试证明 UI 顺序由模型保证。

### P4 —— 用户交互例外 + Tail 锚点

- 状态：**partial（锚点捕获/审计闭环已落地；渲染序列锚定插入待 P3）**
- ✅ `/debug`、`/model` 触发时刻捕获 `Encoder.Tail()`：
  `bridge.recordInteractionAnchor(source)`（值类型快照，模型后续增长不影响
  已记录锚点）；`lastInteractionAnchor()` 供诊断审计。
- ✅ `/debug` 文档输出 "Interaction Anchor:" 行（来源/次数/时刻）。
- ✅ 单测固化快照语义：空模型返回 nil、锚点不随模型增长漂移、
  新交互覆盖为最新模型尾部。
- ⏳ 渲染层以锚点插入渲染序列（`KindUserInteraction` 不分配 CauseID）
  待 P3 切换（Scene 终局）后生效。
- 验证：交互输出在流式/工具执行中触发时位置正确（模型尾部）。

### P5 —— 持久化重放

- 状态：**partial（事件日志持久化 + Replay 幂等重建已落地；UI 恢复一致待 P3）**
- ✅ 进入编码器的全部事件 append-only 持久化为 `sessionDir/runtime-events.jsonl`
  （JSON lines；无 Logger 或写入失败时 best-effort 跳过并计数，不阻塞事件循环）。
- ✅ `start()` 订阅前重放日志，幂等重建渲染模型；`Replay` 前模型为空。
- ✅ 重启后新事件继续追加到同一日志；再次重启重放覆盖全部事件。
- ✅ 单测固化：重放模型与实时编码模型等价（ID/Seq/Kind/Tail 一致、Seq 单调）、
  日志缺失静默跳过、损坏行返回明确错误。
- ⏳ 会话恢复后 UI 与恢复前一致（渲染层消费重建模型）待 P3（Scene 终局）。
- 验证：中断恢复场景，重建模型 `Seq` 单调、ID 不冲突。

---

## 4. 文件级迁移映射

| 文件 | 现状职责 | 迁移后职责 |
| --- | --- | --- |
| `chat_runtime_events.go` | 事件循环 + per-stream delta 重排 + 直接渲染 | 事件循环（保留）→ `Encode`；删除 `orderAssistantDelta` |
| `chat_history_cell.go` | historyCell 接口 + 各 cell 实现 | 接口加 ID/Seq/Status/CauseID；`commandResultCell` 升级为 Item |
| `chat_interaction.go` | assistant/error one-shot 写渲染 | 经 `Encode` 进模型；`completeBlockOutput` 语义上移 |
| `chat.go` | 用户消息/命令输出 | 用户消息经 `Encode`；slash command 输出经 `Encode`（`KindUserInteraction` 除外规则） |
| `ui/`（renderengine、scene 等） | 渲染层 | 只消费 `ChangeSet`；CellID 使用 `Item.ID` |
| 新增：`encoding/`（或 `ui/render/encoding`） | — | `RenderModel` / `EventEncoder` / `ChangeSet` / 映射表 |

---

## 5. 与渲染层各计划的落地顺序配合

| 阶段 | 本方案动作 | 并行推进的既有计划 |
| --- | --- | --- |
| 近期 | P1（类型）+ P2（编码器骨架，双跑） | render-engine A–D 收口；unified plan P0/P1 继续 |
| 中期 | P3（渲染层切换） | unified plan Scene 终局（P4–P9）——场景模型直接以 `ChangeSet` 为事务输入 |
| 中后期 | P4（Tail 锚点） | unified plan fullscreen lease、handoff 收口 |
| 后期 | P5（持久化重放） | unified plan §6.3 规则 6（replay 重建相同 cell sequence）获得数据面支撑 |

---

## 6. 验收标准

1. **顺序**：任意事件序列（含乱序注入）渲染结果 = 编码器模型顺序；单元测试固化。
2. **身份**：流式消息/工具状态机全程一个 ID；无重复块；DedupKey 兜底计数为零。
3. **因果**：并行工具输出全部归组到父调用；无孤儿输出（无 CauseID 的按规则独立成块）。
4. **用户交互**：`/debug`、`/model` 在流式/工具执行中触发，输出锚定模型尾部。
5. **回归**：既有 UI 行为测试全绿；`chat_runtime_events` / `chat_history_cell` /
   `chat_interaction` 相关测试无回归。

---

## 7. 风险与回退

| 风险 | 缓解 | 回退 |
| --- | --- | --- |
| 事件类型遗漏（静默丢失） | 穷举断言测试（编译/测试期强制） | 补映射 + 双跑对比日志 |
| 渲染层切换引入回归 | P3 前完成双跑对比；按功能灰度 | feature 开关回退旧路径 |
| Tail 锚点在压缩/滚动时漂移 | P4 时同步模型压缩迁移规则（上位方案风险 3） | 交互输出降级为普通 append |
| 与 Scene 终局时序冲突 | 本方案以 ChangeSet 为契约，Scene 实现可后置 | 渲染层暂以旧路径消费模型 |
