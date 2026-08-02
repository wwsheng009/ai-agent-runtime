# 统一渲染编码器：事件流渲染顺序保证方案

> 状态：方案草稿（待评审）
> 日期：2026-08-02
> 范围：aicli 渲染管线（chat_runtime_events / chat_history_cell / chat_interaction）
> 参考：Codex 实现（`E:\projects\ai\codex\codex-rs`）

关联文档（本方案体系）：

- 渲染模型数据结构规格（RenderModel / Item / 变更集）：[aicli-event-stream-rendering-order-render-model-spec.md](./aicli-event-stream-rendering-order-render-model-spec.md)
- EventEncoder 接口与事件→操作映射设计：[aicli-event-stream-rendering-order-event-encoder-api-design.md](./aicli-event-stream-rendering-order-event-encoder-api-design.md)
- 迁移路线图与既有渲染计划的衔接：[aicli-event-stream-rendering-order-migration-roadmap.md](./aicli-event-stream-rendering-order-migration-roadmap.md)

与既有渲染/UI 文档的关系（渲染面各计划消费本方案输出的有序带身份模型）：

- 渲染面长期终局：[aicli-tui-unified-render-architecture-refactor-plan.md](./aicli-tui-unified-render-architecture-refactor-plan.md)（§6 事件/事务/排序规则消费 `ChangeSet` 与 `Item.ID/Seq`）
- 渲染引擎模块：[aicli-tui-render-engine-module-design.md](./aicli-tui-render-engine-module-design.md)（§3.1 数据流：状态生产者 → 编码器 → RenderEngine）
- 内容渲染 IR：[aicli-ui-ux-rendering-codex-reference-plan.md](./aicli-ui-ux-rendering-codex-reference-plan.md)（§6 `Renderable` 消费 `Item.Head`）
- 渲染面/数据面隔离历史：[aicli-tui-render-data-plane-codex-migration-plan.md](./aicli-tui-render-data-plane-codex-migration-plan.md)（数据面产生端收敛）
- 早期 UI 组件化与交互阶段规划：[aicli-ui-refactor-codex-inspired-plan.md](./aicli-ui-refactor-codex-inspired-plan.md)（其"事件驱动与限频刷新"的事件源由本方案编码器供给，只负责何时重绘）

## 1. 背景与问题

当前渲染管线中，多个事件流（上游 LLM 流式响应、本地工具执行、并行工具执行、会话生命周期事件等）从不同来源汇聚到渲染层。渲染层依赖局部机制拼凑保证顺序，无法回答三个基本问题：

1. **顺序**：一个信息块在 UI 上的最终位置由什么唯一决定？
2. **身份**：一个跨多次更新演进的"信息块"（流式消息、工具运行状态机）如何被稳定识别？
3. **因果**：子事件（工具输出、命令日志）如何可靠锚定到父事件（工具调用）？

用户的交互事件（`/debug`、`/model` 等）除外：它们不参与上游因果链，但其输出仍必须参与渲染总序（锚点为"按下时刻的当前尾部"）。

### 1.1 期望的最终机制

所有上游事件流（LLM 响应、本地工具事件、并行执行）都应经过**一个统一的编码器**，由编码器决定每个信息块的渲染位置与身份；渲染层只消费编码器的输出，不直接消费原始事件。

---

## 2. 现状分析（ai-agent-runtime）

### 2.1 事件汇聚：单 goroutine FIFO

- `chat_runtime_events.go:566-589`：所有 runtime 事件从 `eventQueue` 由单个 goroutine 串行消费。
- 渲染顺序 = channel 到达顺序，**无验证、无乱序检测、无排错手段**。
- 上游各流（LLM delta、工具事件、会话事件）混合在同一个队列，顺序完全依赖"恰好先到先渲染"。

### 2.2 唯一显式重排机制：只覆盖 assistant delta

- `chat_runtime_events.go:1978-2053` `orderAssistantDelta`：按 per-stream `nextSequence` 重排 delta，乱序 delta 暂存 `pending map[uint64]Event`。
- `chatAssistantStreamState{nextSequence, pending map[uint64]Event}`（`chat_runtime_events.go:63,109`）。
- 结论：**只有 LLM delta 一类事件有乱序重排**，工具事件、会话事件没有任何重排/验证。

### 2.3 因果关联：仅内存态、仅工具链

- `chat_history_cell.go:186-211` `toolChainCell`：用 ToolCallID 把 Running（viewport 态）与 Completed（历史 cell）关联。
- 局限：只覆盖工具链；是内存态关联，无持久身份；工具输出锚定依赖调用 ID 恰好一致。

### 2.4 去重 ≠ 排序

- `chat_runtime_events.go:2293-2354` `shouldRenderTimelineEvent` / DedupKey：只防重复渲染，不解决顺序。

### 2.5 渲染单元缺乏身份

- `chat_history_cell.go:36-43`：`historyCell` 接口只有 `Kind()` + `DisplayLines(width)`，**没有身份字段**。
- 唯一例外：`commandResultCell{id, sequence}`（`chat_history_cell.go:165-179`）。

### 2.6 结论

**当前没有统一编码器。** 顺序保证 = 三个局部机制的拼凑：

| 机制 | 位置 | 覆盖范围 |
| --- | --- | --- |
| 单 goroutine FIFO | chat_runtime_events.go:566 | 所有事件（无验证） |
| per-stream 序列重排 | chat_runtime_events.go:1978 | 仅 assistant delta |
| 内存 callID 因果 | chat_history_cell.go:186 | 仅工具链 |

不存在：全局序列、统一身份、因果锚定层、乱序/丢失检测、增量同步、模型重建能力。

---

## 3. Codex 如何解决（参考 `E:\projects\ai\codex`）

### 3.0 核心思想

> **渲染顺序不来自"事件到达顺序"，而来自一个统一数据结构（Thread）中的数组位置。**

Codex 的管线分为两层：

1. **编码层（core / app-server-protocol）**：所有事件经过 `ThreadHistoryBuilder`（统一编码器）转换成 `ThreadItem`，按语义 append/upsert 进 `Turn.items`。
2. **渲染层（tui / 桌面端）**：只遍历 Thread 数据结构生成 UI cells，**不直接消费事件流**。

### 3.1 统一事实源：Thread = turns → items

- `codex-rs/app-server-protocol/src/protocol/v2/item.rs:227-333`：`ThreadItem` 枚举，每个变体（UserMessage / AgentMessage / Reasoning / Plan / CommandExecution / FileChange / McpToolCall / DynamicToolCall / CollabAgentToolCall / …）**都携带 `id: String` 唯一标识**。
- UserMessage 额外携带 `client_id: Option<String>`（`item.rs:230-233`），用于客户端乐观更新对齐。
- 渲染端：`codex-rs/tui/src/thread_transcript.rs:48-142` `thread_to_transcript_cells` 遍历 `thread.turns.iter().flat_map(|turn| turn.items.iter())` 生成 HistoryCell。

> **渲染位置 = item 在数组中的位置，与事件到达顺序解耦。**

### 3.2 统一编码器：ThreadHistoryBuilder

- `codex-rs/app-server-protocol/src/protocol/thread_history.rs:234-241`：builder 持有 `turns`、`current_turn`、`next_item_index` 全局计数器。
- `handle_event`（`thread_history.rs:321-385`）：**一个全量 match**，把所有 `EventMsg` 变体映射为确定性的 item 操作：
  - **push（追加）**：新信息块（用户消息、agent 消息起点、命令执行起点、reasoning 块、MCP 调用起点…）
  - **upsert（更新）**：按 id 更新已有信息块（AgentMessage delta 流、CommandExecution 状态机 running→completed、McpToolCall begin→end 合并）
- 增量入口：`handle_event_with_changes`（:404）、`handle_rollout_item_with_changes`（:410）、`handle_rollout_items_with_changes`（:420）。
- 持久化重建入口：`build_turns_from_rollout_items`（:85）——**用同一个 builder 重放 rollout 记录，幂等重建完整 Thread**（顺序、id 全部还原）。

> **所有上游事件（LLM 响应、工具事件、命令执行、MCP、文件修改、回滚）都经过这唯一入口——这就是"统一编码器"。**

### 3.3 唯一标识：单调分配

- `thread_history.rs:1451-1455` `next_item_id()`：`item-{n}` 全局单调递增，由 builder 在事件处理时按序分配（core 单线程顺序消费事件，id 顺序 = 逻辑顺序）。
- truncate/rollback 后重算：`:1312-1316` `next_item_index = item_count + 1`，保证跨轮次唯一。

### 3.4 push vs upsert：两种语义

- `push_item_in_current_turn`（:1364-1375）：追加新 item 并记录变更。
- `upsert_item_in_current_turn` / `upsert_item_in_turn_id`（:1409-1419 / :1377-1407）：按 `item.id()` 查找替换；**找不到则追加（幂等）**——乱序/重复事件不会产生重复块。

### 3.5 增量变更集：UI 只应用变更

- `ThreadHistoryChangeSet { changed_items, changed_turns, removed_turn_ids }`（:113-118）。
- `ThreadHistoryChangeAccumulator`（:155-232）：按 `(turn_id, item_id)` 去重合并，同一 item 多次更新只发**最新快照**；被回滚 turn 的累积变更一并丢弃。
- UI 按变更集增量更新，保证 UI 与 Thread 一致且无中间态闪烁。

### 3.6 用户交互的乐观更新

- `UserMessage { id, client_id }`：客户端可在服务端确认前用 `client_id` 先行渲染（乐观 UI），服务端最终以 `id` 对齐、幂等去重。

### 3.7 回滚与重建

- 回滚：`ThreadRolledBackEvent` → `removed_turn_ids`，UI 整体移除对应 turn。
- 重建：rollout 持久化 + 同一 builder 重放，恢复顺序与身份。

### 3.8 Codex 机制小结

| 能力 | Codex 实现 |
| --- | --- |
| 统一编码入口 | `ThreadHistoryBuilder.handle_event` 全量 match |
| 全局顺序 | item 在 `Turn.items` 数组中的位置 |
| 唯一身份 | 每 item `id: String`（builder 单调分配） |
| 更新语义 | push（新块）/ upsert（按 id 更新，幂等） |
| 增量同步 | `ThreadHistoryChangeSet`（按 id 去重合并） |
| 因果锚定 | turn 分组 + 事件内嵌 id（如 tool_call_id） |
| 用户交互例外 | `client_id` 乐观更新 + 最终对齐 |
| 回滚 | `removed_turn_ids` 整 turn 移除 |
| 重建 | rollout 重放 + 同一 builder 幂等 |

---

## 4. 差距对照

| 维度 | ai-agent-runtime（现状） | Codex（参考） |
| --- | --- | --- |
| 统一编码入口 | 无；各流直接进渲染队列 | `ThreadHistoryBuilder.handle_event` 全量 match |
| 渲染顺序来源 | 事件 channel 到达顺序（FIFO） | Thread 数据结构的数组位置 |
| 信息块身份 | 无（historyCell 无 ID()；仅 commandResultCell 例外） | 每 item 全局唯一 `id` |
| 乱序处理 | 仅 assistant delta 单流重排 | push/upsert 幂等 + id 对齐，天然免疫乱序 |
| 更新语义 | 部分块整体重建 | append（新块）/ upsert（按 id 更新） |
| 因果锚定 | 仅工具链内存态 callID | turn 分组 + 事件内嵌 id + 变更集 |
| 增量同步 | 无 | `ThreadHistoryChangeSet` 按 id 去重 |
| 重复防护 | DedupKey 去重（仅渲染层） | id 幂等（编码层） |
| 回滚 | 无 | removed_turn_ids 整 turn 移除 |
| 重建 | 无（依赖内存态） | rollout 重放幂等重建 |
| 用户交互 | /debug、/model 直接插渲染 | client_id 乐观更新 + 最终对齐 |

---

## 5. 设计方案（ai-agent-runtime 落地）

### 5.1 目标

引入**统一渲染编码器**：所有上游事件（LLM 响应、工具执行、并行执行、会话事件）经唯一入口编码为"渲染模型"（有序、带身份、带因果）；渲染层只消费渲染模型。用户交互事件（`/debug`、`/model`）不进因果编码，但输出以"触发时刻模型尾部"为锚点参与总序。

### 5.2 渲染模型（与 Codex Thread 对齐）

新增渲染文档模型 `RenderModel`（对应 Codex `Thread`）：

```
RenderModel
├── Turns / 或扁平 Items：有序数组（顺序 = 渲染顺序）
└── Item（对应 Codex ThreadItem，升级 historyCell）
    ├── ID()          string   // 全局唯一，单调分配（item-{n}）
    ├── Seq()         uint64   // 提交序号（单调，仅追加语义）
    ├── Kind()        Kind     // 复用现有 Kind()
    ├── CauseID()     string   // 父事件 id（并行工具输出 → 工具调用 id）
    ├── Status()      State    // pending/running/completed（状态机）
    └── DisplayLines(width)    // 现有渲染接口保留
```

编码器内部持有全局 `nextItemID` / `nextSeq` 计数器（等价 `next_item_id`）。

### 5.3 统一编码器：EventEncoder

新增 `EventEncoder`（等价 `ThreadHistoryBuilder`）：

- 单入口：`Encode(event RuntimeEvent) → []ItemChange`
- 内部全量 match 所有事件类型，确定映射：
  - **append**：新信息块（assistant 消息起点、工具调用起点、reasoning 起点…）
  - **upsert**：按 `ID()` 更新既有块（delta 流、工具状态机 running→completed、输出追加）
  - **remove**：回滚/取消（按 ID 移除）
- 幂等：upsert 找不到目标时退化为 append（乱序免疫）。
- 并行工具：执行层发起时分配 CauseID（现有 callID 上移为编码层概念），输出事件携带 CauseID 锚定父块。

#### 5.3.1 双事件源：chatcore 类型 + legacy 呈现类型

事件总线对 aicli 是**全量订阅**（`Subscribe("", …)`），因此编码器会同时收到两类事件：

1. **chatcore 类型**（`internal/chat` 的 31 个 `EventXxx` 常量，如 `tool_started`、`assistant_delta`）：走 §5.3 的完整 op 映射，驱动 assistant / tool 等块的 append/upsert 状态机。
2. **legacy 呈现类型**（agent/skills 层直接 `emitRuntimeEvent` 的字符串，如 `tool.requested`、`subagent.started`、`llm.retry`、`team.task.completed`、`context.preflight.started`、`patch.applied`）：统一按**已知 system 呈现事件**处理——append 一个 system 块（信息不丢），**不计入 UnknownCount**（`/debug` 的 "Unknown Types:" 只反映真正未知的类型）。

规则与理由：

- legacy 类型一律归 `opSystem`，**不做** opToolStarted/opReasoning 等映射：它们与 chatcore 类型并存（如 `tool.requested` 与 `tool_started`），若映射为工具操作会与 chatcore 事件争用 `toolByID` 索引，导致工具块重复/状态错乱。
- 识别方式为前缀族白名单（`assistant.`、`context.preflight.`、`llm.`、`patch.`、`planning.`、`response.`、`subagent.`、`team.`、`tool.`），与穷举断言测试（编码器单测）联动：新增 emit 类型未入白名单时，`UnknownCount` 非零即被测试捕获。
- 对应关系：legacy 呈现事件与 timeline 渲染路径（`renderChatRuntimeTimelineEvent`）一致，双跑模式下编码器模型中的 system 块即这些事件的呈现投影。

### 5.4 渲染层改造

- 删除/降级"按事件到达顺序直接渲染"路径；改为**只消费 RenderModel 的有序数组**。
- `orderAssistantDelta` 泛化：不再单独处理 delta 重排，统一由编码器 upsert 语义覆盖（delta 到达即 upsert 到目标 item，天然有序）。
- 增量渲染：编码器输出 `ItemChange` 列表（等价 ChangeSet），按 ID 去重合并后应用。

### 5.5 用户交互例外（/debug、/model）

- **不进编码器**（不参与因果图、不分配 CauseID）。
- 渲染时记录"触发时刻模型尾部指针"，交互输出以该指针为锚点插入，保证总序不破坏。
- 等价于 Codex `client_id` 的乐观语义：先渲染占位，编码器后续事件以尾指针为界。

### 5.6 顺序保证的验证手段

- 编码器内对每个事件记录 `(receivedAt, seq)`，渲染前校验 `seq` 单调；乱序到达记录 warning 日志（不改 UI，靠 id 对齐）。
- 重复事件（同 ID 同内容）幂等跳过。
- 单元测试：乱序注入（先输出后调用、并行工具交错）→ 断言最终 RenderModel 顺序正确。

### 5.7 实施阶段

| 阶段 | 内容 | 验证 |
| --- | --- | --- |
| P1 模型+编码器骨架 | RenderModel / EventEncoder / ID+Seq+CauseID；match 现有全部事件类型 | 编码器单测：事件序列 → 模型断言 |
| P2 渲染层迁移 | view 改为消费 RenderModel；删除事件流直接渲染路径 | 既有 UI 行为回归 |
| P3 增量+乱序检测 | ItemChange 增量应用、去重合并、乱序 warning | 并行工具场景验证 |
| P4 持久化重放 | 事件日志重放重建 RenderModel（等价 rollout） | 会话恢复后 UI 与恢复前一致 |

---

## 6. 风险与开放问题

1. **并行工具锚定**：CauseID 需在执行发起时（而非输出时）分配并随事件传播；现有 callID 需确认所有并行执行路径均携带。
2. **事件枚举完整性**：编码器 match 必须覆盖全部上游事件类型，漏匹配 = 静默丢失；建议编译期/测试期强制穷举。
3. **/debug、/model 尾指针语义**：滚动压缩（activeband）、模型压缩时尾指针需随模型迁移，否则交互输出锚点漂移。
4. **既有 cell 兼容**：historyCell 接口扩展 ID() 需同步所有实现；可先提供默认实现（按指针地址）过渡。
5. **与 DedupKey 的关系**：编码层幂等后，渲染层 DedupKey 可降级为兜底，避免双重重叠逻辑。
