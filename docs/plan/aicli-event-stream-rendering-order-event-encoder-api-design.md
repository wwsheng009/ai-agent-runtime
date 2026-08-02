# 统一渲染编码器：EventEncoder 接口与事件→操作映射设计

> 状态：方案草稿（待评审）
> 日期：2026-08-02
> 上位方案：[aicli-event-stream-rendering-order-unified-encoder-plan.md](./aicli-event-stream-rendering-order-unified-encoder-plan.md)
> 配套文档：[渲染模型数据结构规格](./aicli-event-stream-rendering-order-render-model-spec.md) ｜ [迁移路线图](./aicli-event-stream-rendering-order-migration-roadmap.md)
> 参考：Codex `protocol/thread_history.rs`（`ThreadHistoryBuilder`）

本文定义统一编码器 **EventEncoder** 的接口、事件→操作映射表、幂等与乱序规则。它是 Codex `ThreadHistoryBuilder` 在本项目的对应物：**所有上游事件的唯一编码入口**。

---

## 1. 接口设计

### 1.1 核心 API

```go
// EventEncoder 等价 Codex ThreadHistoryBuilder：单线程顺序消费事件，产出渲染模型。
type EventEncoder struct {
    model     *RenderModel   // 有序信息块集合（见 render-model-spec）
    nextItemID uint64        // item-{n} 单调分配
    nextSeq    uint64        // 提交序号单调分配
    clock      uint64        // 编码器时钟（每事件 +1，审计用）
    changes    *ChangeAccumulator // 增量变更集按 (ID, Revision) 去重合并
}

// Encode 处理单个上游事件，返回增量变更集（等价 handle_event + handle_event_with_changes）。
// 并发：非线程安全；由现有单 goroutine 事件循环（chat_runtime_events.go:566-589）独占调用。
func (e *EventEncoder) Encode(ev RuntimeEvent) *ChangeSet

// Replay 按持久化顺序重放事件日志，幂等重建模型（等价 build_turns_from_rollout_items）。
func (e *EventEncoder) Replay(events []RuntimeEvent) (*RenderModel, error)

// Snapshot 返回当前模型只读视图（渲染层消费；等价 thread_to_transcript_cells 的输入）。
func (e *EventEncoder) Snapshot() *RenderModel

// Tail 返回当前模型尾部指针（/debug、/model 锚点用）。
func (e *EventEncoder) Tail() *Tail
```

### 1.2 接入点（唯一入口约束）

```text
上游事件（LLM delta / 工具事件 / 并行输出 / 命令日志 / 会话事件）
        │
        ▼
┌───────────────────────────────────────────┐
│ EventEncoder.Encode(ev)  ← 唯一入口         │
│  1. 校验/记录 (clock, seq)                 │
│  2. match 事件类型 → 操作（§3 映射表）       │
│  3. 幂等与乱序检测（§4）                    │
│  4. 产出 ChangeSet（§5）                   │
└───────────────────────────────────────────┘
        │ ChangeSet（按 ID 去重合并）
        ▼
渲染层（Scene / RenderEngine 事务）——只消费变更集，不接触原始事件
```

- 现有 `chat_runtime_events.go` 的事件循环**从"直接喂渲染"改为"先喂编码器"**：
  循环内 `Encode(ev)`，将返回的 `ChangeSet` 转交渲染层事务提交。
- `chat_interaction.go` 的 assistant/error one-shot 路径同样经 `Encode`
  进模型，禁止旁路写渲染（unified plan 的 single-writer 原则在此获得编码层支撑）。

---

## 2. 状态与依赖

| 成员 | 说明 |
| --- | --- |
| `RenderModel` | 唯一事实源；编码器是唯一写者 |
| `nextItemID` / `nextSeq` | 单调计数器；truncate/回滚后按剩余项数重算（等价 `thread_history.rs:1312-1316`） |
| `ChangeAccumulator` | 等价 `ThreadHistoryChangeAccumulator`：`(turn_id, item_id)` 维度去重，同一 Item 多次更新只发最新快照；回滚 turn 的累积变更一并丢弃 |

---

## 3. 事件 → 操作映射表（全量 match）

以下为**必须穷举**的映射。编码器新增事件类型时，此表同步更新；
测试期用穷举断言（新增事件类型未映射 → 编译期/测试失败），防止静默丢失。

| # | 上游事件（现状来源） | 操作 | 目标 Item | 备注 |
| --- | --- | --- | --- | --- |
| 1 | 用户消息提交（`chat.go` send 路径） | append | `KindUser` | 不可变；对应 UserMessage |
| 2 | assistant 消息起点（`assistantStart`） | append | `KindAssistant` | `Status=pending` |
| 3 | assistant delta（`assistantDelta`） | upsert | 按 ID → `KindAssistant` | 乱序免疫见 §4.2 |
| 4 | assistant 完成 / error | upsert（终态） | `KindAssistant` | 终态后仅允许 remove |
| 5 | reasoning 块（`ReasoningBlock`） | append / upsert | `KindReasoning` | summary 演进 |
| 6 | 工具调用发起（builder 发起时） | append | `KindToolCall` | **分配 CauseID**，`Status=pending` |
| 7 | 工具调用状态变化（running） | upsert | `KindToolCall` | 状态机迁移 |
| 8 | 工具输出（并行/串行） | append（CauseID→ToolCall）或 upsert | `KindToolOutput` | 有 CauseID 时按父块归组；无父时独立块 |
| 9 | 命令执行起点（`commandResultCell`） | append | `KindCommand` | 现有 `{id, sequence}` 升级为 `Item.ID/Seq` |
| 10 | 命令执行输出 / 状态变化 | upsert | `KindCommand` | running→completed 等 |
| 11 | 会话/诊断事件（runtime diagnostic） | append | `KindSystem` | 无 CauseID；对应 async system event |
| 12 | `/debug`、`/model` 交互输出 | append（Tail 锚定） | `KindUserInteraction` | **不分配 CauseID**；捕获触发时刻 Tail |
| 13 | 回滚 / 取消（session backtrack、tool cancel） | remove | 按 ID 移除 | 对应 removed_turn_ids |
| 14 | 文件修改 / 工具链产物事件 | append / upsert | `KindToolOutput`（或独立 Kind） | 视既有事件类型定 |

### 3.1 映射规则要点

- **每个事件类型恰好对应一个操作**（append / upsert / remove / skip-dedup），
  不存在"事件直接写渲染"的分支。
- 现有 `orderAssistantDelta`（`chat_runtime_events.go:1978-2053`）的 per-stream
  重排**删除**：delta 到达即按 ID upsert，顺序由模型位置保证，不再需要重排队列。
- 现有 `completeBlockOutput`（`chat_interaction.go:967-968`）的"块边界空白"
  语义**上移**为 `Item.Status` 终态判定 + 渲染层 `BoundaryPolicy`
  （unified plan §7），编码器不负责空行。

---

## 4. 幂等与乱序规则

### 4.1 幂等

| 场景 | 行为 |
| --- | --- |
| upsert 找不到目标 ID | 退化为 append（等价 Codex upsert 幂等） |
| 同 ID 同内容重复 upsert | 跳过，不产生变更 |
| remove 不存在的 ID | 忽略 |
| 同 ID 重复 append | 拒绝（ID 不重用），记录 warning |

### 4.2 乱序检测

- 编码器按流记录已提交的最大 `lastSeq` 与乱序缓冲（`streamOrder`）。
- 乱序到达（`seq <= lastSeq` 的旧序 delta，或 `seq > lastSeq+1` 的空洞）：
  **缓冲不提交**，等到序后按 `sequence` 拼接（`1,3,2` → ABC），不丢信息、
  不错序；与旧终端 `orderAssistantDelta` 重排语义一致（四处契约统一：
  文档 / Encoder / Scene / 旧终端）。
- 流结束（assistant final / llm finished）时 flush 缓冲中未提交的 delta，
  保证最终内容完整；重复 sequence 的 delta 跳过（幂等，见 §4.1）。
- 并行工具输出与调用交错是**预期场景**：输出事件携带 CauseID，
  append 后由归并规则收敛，不需要调用点保证到达顺序。

### 4.3 重复防护的关系

- 编码层幂等（ID 对齐）成为**主防线**；
- 渲染层现有 DedupKey 降级为**兜底**（防止历史遗留旁路路径重复渲染），
  不承担顺序职责。

---

## 5. 增量变更集与事务提交

```go
type ChangeSet struct {
    Changes []ItemChange  // 按 (ID, Revision) 去重合并，有序
    Tail    *Tail         // 编码后模型尾部
}
```

- 渲染层每次收到 `ChangeSet` 打包为一个 `SceneTransaction` 提交
  （unified plan §6.2）：`AppendCell / UpdateCell / FinalizeCell / RemoveMutableCell`
  由 `ItemChange.Op` 直接映射。
- 一次 `Encode` 可能产出多条变更（如 final assistant + final tool chain），
  必须作为**一个事务**提交，用户看不到中间态。

---

## 6. 测试与验证设计

### 6.1 单元测试（编码器纯逻辑，不触渲染）

| 用例 | 断言 |
| --- | --- |
| 事件序列 → 模型快照 | `Items` 顺序、ID 唯一、Seq 单调 |
| delta 乱序注入（先输出后调用） | 最终模型顺序正确、无重复块 |
| 并行工具交错 | 输出按 CauseID 归组到父调用块 |
| 重复 upsert / remove 不存在 | 幂等，ChangeSet 无多余变更 |
| truncate 后重算计数器 | 新 ID 不冲突 |
| Replay 重放 | 重建模型与原始一致 |

### 6.2 穷举测试

- 事件类型枚举 → 映射表覆盖断言（新增事件类型未映射即失败）。

### 6.3 集成测试

- `chat_runtime_events.go` 循环改为 Encode 后：既有 UI 行为回归
  （历史顺序、工具链空行、assistant 流式）不变。

---

## 7. 与既有文档的衔接

| 文档 | 关系 |
| --- | --- |
| [render-model-spec](./aicli-event-stream-rendering-order-render-model-spec.md) | 本编码器的输出模型（字段/状态机/变更集） |
| [migration-roadmap](./aicli-event-stream-rendering-order-migration-roadmap.md) | 编码器接入现状管线的分阶段步骤 |
| unified-render-architecture-refactor-plan §6.1 | `RenderEvent` 的 `EventID` 由编码器 `Seq` 提供；§6.2 事务消费 `ChangeSet` |
| render-engine-module-design §3.1 | `Update(ScenePatch)` 的入参来自编码器变更集 |
