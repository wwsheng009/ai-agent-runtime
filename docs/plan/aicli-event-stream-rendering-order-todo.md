# aicli 事件流渲染顺序：统一编码器实施待办清单

> 状态：`draft`（待办清单，非方案文档）
> 创建日期：2026-08-02
> 关联文档：
> - `aicli-event-stream-rendering-order-event-encoder-api-design.md`
> - `aicli-event-stream-rendering-order-migration-roadmap.md`
> - `aicli-event-stream-rendering-order-render-model-spec.md`
> - `aicli-event-stream-rendering-order-unified-encoder-plan.md`
>
> 结论快照：2026-08-02 12:54 初版；**2026-08-02 15:30 复审更新；2026-08-02 16:30 第三轮更新**。
> 总体状态：**编码器层（P1/P2）落地完成且测试全绿；命令/error 注入已补（切片 11）；生产 UI 仍未切换到统一编码器（P3 未完成），整体为 `partial`，最终验收未通过。**

## 1. 背景

四份方案的目标是：所有上游事件经唯一入口编码（EventEncoder → ChangeSet → Scene），渲染层只消费 RenderModel/Scene，删除旧直接渲染路径，实现顺序、身份、因果、幂等、状态机五条不变量。

复审结论：**P1 落地；P2 编码器层主体完成（乱序、reasoning 独立索引、终态保护、幂等、suppression 顺序、用户注入均已完成并有测试）；P3 presenter 切换与 single-writer 收口未完成；命令/error 注入、Tail 锚定插入、ChangeAccumulator、feature flag、文档入库仍缺失。**

三层状态口径（本文档所有条目均按此标注）：
- **[代码已存在]**：有实现或测试，但可能未接入主路径
- **[已接入主路径]**：生产路径实际调用
- **[验收未通过]**：对应验收项不成立或 UI 回归未全绿

## 2. 最高优先级阻断项（切换 presenter 前必须解决）

- [x] **统一乱序语义**：已统一为"有序缓冲拼接"（`1,3,2` → ABC）。
  - 编码器 `encoder.go` streamOrder 缓冲拼接；`scene_test.go` 乱序断言已改为 ABC 且通过；`encoder_test.go` 固化 ABCDE/ABC；旧终端 `orderAssistantDelta` 重排同为 ABC——四方一致。
  - ⚠️ **遗留：API 文档 §4.2 仍是旧语义**（"seq < lastSeq → 不改变模型"，encoder-api-design.md:121-127），需更新为缓冲拼接语义。
- [x] **拆分 reasoning 与 assistant 索引**：`reasoningBy` 独立索引已实现，reasoning 不再覆盖 assistant Item；有专项测试。
- [x] **完成事件映射**：14 类映射基本齐备（当前缺口清单见 §4）。
- [x] **实现严格状态机与终态保护**：`upsertItem` 拒绝终态后变更；final 后 delta 丢弃；孤儿 final 直接终态；`Status.Terminal()` 为全局不变量；有终态保护测试。
- [x] **实现真实幂等**：seq 重复跳过、tool started 幂等、tool output 幂等、同 ID 同内容 mutate 跳过；有幂等测试。
- [x] **只把通过 ownership/suppression 校验的事件送入 Encoder**：编码已移到 suppression 判断之后（chat_runtime_events.go:1624）。
- [x] **将命令、error 迁入统一入口**：用户与 one-shot assistant 已迁入；**命令经 `SubmitCommand` 注入（KindCommand 终态块），error 经 `SubmitError` 注入（KindSystem 终态块，复用现有枚举，无需新增 KindError）**——桥接在 coordinator 提交点（chat_interaction.go:3194-3202 命令、3328-3337 error），均有 live-parity / replay / 空输入零行为测试。
- [ ] **接入真实 Scene presenter 后删除旧路径**：删除 `orderAssistantDelta`（chat_runtime_events.go:2404 仍活跃，与新 encoder 乱序缓冲双份状态机）、raw-event terminal 写入、替换 `completeBlockOutput/gapBeforeBlockLocked`，并添加可回退 feature flag。
- [x] **实现 Tail 锚定插入与真实 UI replay**：`SubmitUserInteraction`（KindUserInteraction 终态块 + `ItemChange.AfterID` 锚定插入 + 锚点缺失退化 append + 不推进 Tail）、`Scene.InsertCell`（锚点必须存在，INV-FRAME-01 回滚）、`ChangeSetMapper` AfterID→InsertCell 映射、bridge `recordInteractionAnchor`/`consumePendingInteraction`/`submitUserInteraction`（chat_interaction.go RenderCommandDocument 提交点）与事件日志交互记录 replay 恢复；测试：encoder（锚定/退化/nil）、scene（插入顺序/回滚）、from_changeset（AfterID 映射）、commands（端到端锚定注入 + replay 重建等价）。

## 3. 验收项状态（对照四份文档）

| 验收项 | 状态 | 原因 |
|---|---|---|
| 顺序 | 部分 | Encoder/Scene/旧终端已统一 ABC；API 文档未同步 |
| 身份 | 部分 | `item-N`、`cell-N`、`command:N` 仍并存（P3 删除旧路径后收敛） |
| 因果 | 部分 | 正常 tool start→finish 可归组；output-before-start 经缓冲拼接收敛；remove/backtrack 仍映射 system |
| 用户交互 | 通过 | Tail 锚定插入完整实现（编码器锚定插入 + Scene InsertCell + 提交点注入 + replay 恢复，均有测试） |
| 幂等 | 通过 | 重复 delta/tool started/tool output/mutate 均有幂等测试 |
| 状态机 | 通过 | 终态保护 + 孤儿 final 直接终态 + 测试 |
| 事件完整性 | 部分 | 已知事件枚举测试是人工维护清单，非编译期穷举 |
| 持久化恢复 | 部分 | 模型/Scene 等价（含交互注入锚点位置重建），真实 UI 等价未证明 |
| UI 回归全绿 | 通过 | ui/renderengine/scene/boundary/encoding/commands 全部测试通过（含 paint-trace 修复，见 §6） |
| 单写者 | 失败 | Encoder/Scene 与旧 Interaction 同时存在（P3 未做） |

## 4. 逐文档缺口清单

### 4.1 事件编码器 API 设计（event-encoder-api-design.md）

- [ ] **[验收未通过] 唯一入口**：当前仍是双跑（`chat_runtime_events.go` 编码后继续走旧渲染路径 1441-1499、2211-2245）。
- [x] **用户消息进入 Encoder**：`submitUserInput` 已接入（chat_interaction.go:3297-3298），有 `chat_runtime_events_user_input_test.go`。
- [x] **本地命令进入 Encoder**：`RenderCommandDocument` 提交点注入 `submitCommand(RenderDocumentPlain(doc))`（chat_interaction.go:3194-3202），KindCommand 终态块。
- [x] **one-shot assistant 进入 Encoder**：统一走 Encode（chat_runtime_events.go:1624，EventAssistantMessage → opAssistantFinal）。
- [x] **error 进入 Encoder**：`RenderError` 提交点注入 `submitError(...)`（chat_interaction.go:3328-3337），KindSystem 终态块（会话/诊断语义，复用现有枚举）。
- [ ] **[代码已存在但未接入] `removeItem` 无生产调用点**：rewind/backtrack/job cancel 被映射为 system 而非 remove（encoder.go:159-170）。
- [ ] **[验收未通过] 文件修改、tool progress、command lifecycle 的 Kind/状态机映射**。
- [x] **乱序语义文档同步**：API 文档 §4.2 已更新为缓冲拼接语义（1,3,2 → ABC，四处契约统一）。
- [x] **reasoning 类型污染**：已修复（reasoningBy 独立索引）。
- [x] **幂等**：已补齐（重复 sequence delta / tool start / tool finished / 同 ID 同内容）。
- [ ] **[验收未通过] `ChangeAccumulator` 未实现**：ChangeSet 只是当前调用直接追加 slice，无设计要求的累加器。
- [ ] **[部分] `Replay` 契约**：`Replay` 自身不 Reset（encoder.go:91-117），调用者必须显式新建 Encoder 或先 Reset；已有 replay 测试（幂等重建），API 保证仍需文档化。

### 4.2 渲染模型规格（render-model-spec.md）

- [ ] **[验收未通过] `Head` 类型**：规格为 `Head *Block`，实际为 `Head string`（model.go:47-56）；模型仍是文本快照层，未承载 RichDocument/Block 语义。
- [x] **生命周期状态机**：终态保护已实现（upsert 拒绝终态、final 后 delta 丢弃、孤儿 final 直接终态）；tool failure/cancel 转 failed/canceled 与 rewind/backtrack remove 仍缺。
- [ ] **[验收未通过] 唯一身份**：`item-N`（encoder.go:249-256）、`cell-N`（chat_history_cell.go:85-96）、`command:N`（chat_interaction.go:3077-3081）三套并存；"渲染层不得自行发明身份"未实现（P3 删除旧路径后收敛）。
- [ ] **[验收未通过] RenderModel 非 UI 唯一事实源**：Scene 消费 ChangeSet 但 terminal presenter 仍从原始事件/旧 coordinator 接收内容；`scene.RenderText` 仅用于测试与 `/debug`（render.go:23-37；chat_debug_document.go:185-203）。

### 4.3 迁移路线图（migration-roadmap.md）

- [x] **[已接入主路径] P1 RenderModel + Item**：类型与 historyCell 接口已存在（chat_history_cell.go:39-96）；遗留：fallback 使用 `cell-N` 而非文档所述指针地址。
- [ ] **[部分] P2 Encoder 骨架 + 双跑**：双跑接入完成（chat_runtime_events.go:620-649），乱序/幂等/状态机已补齐，穷举契约未完成。
- [ ] **[基础设施完成，切换未完成] P3 只消费 ChangeSet**：ChangeSetMapper、SceneController.Submit、事务回滚、BoundaryPolicy、LayoutTranscript、RenderText 均已落地且有 parity 测试；但生产 presenter 未接入，旧路径未删除。
- [x] **P4 Tail 锚点**：捕获 + 锚定插入完成（`recordInteractionAnchor` → `SubmitUserInteraction`/`InsertCell`，chat_runtime_events.go:703-711、1151-1183；scene.go:246-257），有端到端与 replay 测试。

### 4.4 统一编码器上位方案（unified-encoder-plan.md）

- [ ] **[部分，文档待同步] P3 增量 + 乱序检测**：代码已统一为缓冲拼接，文档需同步。
- [ ] **[部分] P4 持久化重放**：模型与 Scene 重建完成，真实 UI 恢复未完成。
- [ ] **[验收未通过] 核心目标**："所有上游事件经唯一入口编码；渲染层只消费 RenderModel"——当前仍是 RuntimeEvent → ① EventEncoder→ChangeSet→Scene（影子面）② 旧事件处理→Interaction→Terminal（可见面）双路径。

## 5. 测试缺口

- [x] **乱序语义统一测试**：encoder_test.go 固化 ABCDE/ABC；scene_test.go 乱序断言已改为 ABC；旧终端 orderAssistantDelta 对齐 ABC。⚠️ 三方 golden 对拍测试仍缺（单一断言各自独立）。
- [x] **reasoning/assistant 独立 Item 测试**：已补。
- [x] **终态保护测试**：已补。
- [x] **幂等测试**：已补（重复 sequence delta、重复 tool started/finished、同 ID 同内容）。
- [ ] **[缺失] 事件穷举测试**：现为人工维护的已知事件清单，需要编译期/生成式穷举（14 类映射全覆盖）。
- [x] **replay 幂等测试**：已补（Replay 幂等重建模型）。
- [x] **suppression 一致性测试**：编码位置已移到 suppression 之后。
- [ ] **[缺失] 迁移开关测试**：feature flag 未实现，无两态对比测试。

## 6. 当前测试现状（2026-08-02 复审）

### 通过

- `go test ./cmd/aicli/ui ./cmd/aicli/ui/renderengine ./cmd/aicli/ui/scene ./cmd/aicli/ui/boundary ./cmd/aicli/ui/render/...` —— 全部通过。
- `go test ./cmd/aicli/commands -count=1` —— 通过（约 45s）。
- Scene/RenderText parity、用户注入、乱序、幂等、终态、replay 定向测试通过。

### 已修复的失败项

- [x] `TestPaintDebugRowTagWhiteCounterGrows` / `TestPaintDebugRowTagStarMarksLastFrameWhite` —— 由提交 369e863 修复（hash 键分裂、Reset 后 resyncPending、composedPlanLocked star 相位）。
- [x] **本轮新发现并修复**：`TestStreamingAppendBottomDeltaIsNotWhiteRepaint` 失败 —— `PaintTrace.recordFrame` 的 resyncPending 分支把**未 painted 的 history 行**也标为 changed，产生虚假 MissingPaints 突发；已修复为只标记 painted 行（paint_trace.go:210-220）。修复后 ui 包全绿。

### 工作树卫生

- [ ] `git diff --check` 失败：`backend/internal/background/manager.go` 存在大量尾随空白（范围外变更，但需清理后才能干净提交）。
- [ ] **垃圾文件**：`backend/test_commands_out.txt`（测试输出遗留）应删除。
- [ ] **文档未入库**：四份方案文档 + 本待办文件均为 untracked，未形成稳定基线。

## 7. 风险与观察

- [ ] **双事实源风险（最高，未消除）**：Encoder/Scene（影子面）与旧 Interaction（可见面）双路径并存；目前乱序语义已对齐，但任何基于单侧证据的"已切换"结论都不可信。
- [ ] **旧路径双份状态机**：旧 `orderAssistantDelta` 与新 encoder 乱序缓冲并存，语义对齐但冗余——删除属 P3。
- [ ] **工作区并发变更**：审计期间存在另一写入者（paint-trace 诊断改造、编码器实现均在工作树中演进）；paint_trace.go / screen_model.go / fixed_bottom_surface_paint_trace_test.go 当前为未提交修改。
- [ ] **文档未入库**：方案文档与代码现状存在漂移（§2 乱序语义、§4.1 用户/one-shot 注入），需在合入前同步。
- [ ] **后台任务队列异常**：本轮启动的 `go test ./cmd/aicli/commands` 后台任务一直处于 queued 未启动，改为前台执行完成（约 45s）。

## 8. 建议执行顺序（复审更新）

1. ~~统一乱序语义~~（代码已统一 ABC；API 文档 §4.2 已同步；剩余：三方 golden 对拍测试）。
2. ~~reasoning 索引 / 终态保护 / 幂等 / suppression 顺序~~（已完成，测试全绿）。
3. ~~命令注入（SubmitCommand + KindCommand）与 error 注入（SubmitError + KindSystem）~~（已完成：encoder API + bridge 注入 + replay 日志 + live-parity/replay/零行为测试）。
4. 实现 Tail 锚定插入与真实 UI replay。
5. 添加 feature flag，切换真实 Scene presenter。
6. 删除旧路径（orderAssistantDelta、raw-event terminal 写入、completeBlockOutput/gapBeforeBlockLocked）。
7. 实现 ChangeAccumulator、明确 Replay 契约、补齐事件穷举测试与迁移开关测试。
8. ~~修复 paint-debug 测试失败~~（369e863 + resyncPending 修复，ui 包已全绿）。
9. 清理：删除 `test_commands_out.txt`、修复 manager.go 尾随空白、同步四份文档并入库（git add + 评审）。
