# 统一渲染编码器：渲染模型（RenderModel / Item）数据结构规格

> 状态：**实施中（P1/P2 已落地且测试全绿；P3 渲染层切换与旧路径删除未完成；P4 锚点 partial）**
> 日期：2026-08-02（2026-08-07 状态更新）
> 上位方案：[aicli-event-stream-rendering-order-unified-encoder-plan.md](./aicli-event-stream-rendering-order-unified-encoder-plan.md)
> 配套文档：[EventEncoder 接口设计](./aicli-event-stream-rendering-order-event-encoder-api-design.md) ｜ [迁移路线图](./aicli-event-stream-rendering-order-migration-roadmap.md)
> 参考：Codex `app-server-protocol/src/protocol/v2/item.rs`、`protocol/thread_history.rs`

本文定义统一编码器的**输出数据模型**：渲染层唯一消费的、有序、带身份、带因果的"信息块"集合。它是 Codex `Thread / ThreadItem` 在本项目的对应物。

---

## 1. 定位与原则

### 1.1 渲染顺序的唯一来源

> **一个信息块在 UI 上的最终位置 = 它在 RenderModel 数组中的位置。**
> 任何上游事件（LLM delta、工具事件、并行输出、命令日志）都不直接决定位置；
> 它们只作为编码器输入，由编码器决定 append/upsert/remove。

### 1.2 身份的唯一来源

> **一个跨多次更新演进的信息块（流式消息、工具状态机）由 `Item.ID` 稳定识别。**
> ID 由编码器单调分配，与事件到达顺序解耦；渲染层不得自行发明身份。

### 1.3 因果的锚定

> **子事件（工具输出、命令日志）通过 `Item.CauseID` 锚定父块（工具调用）。**
> CauseID 在执行发起时分配并随事件传播，渲染层不做因果推断。

### 1.4 用户交互例外

`/debug`、`/model` 等用户交互事件**不进入编码器**，不分配 CauseID；
其输出以"触发时刻模型尾部指针"为锚点插入渲染序列（详见上位方案 §5.5）。

---

## 2. 顶层结构

```go
// RenderModel 对应 Codex Thread：有序信息块集合，渲染层唯一事实源。
type RenderModel struct {
    Items []*Item   // 有序数组；渲染顺序 = 数组顺序
    Tail  *Tail     // 模型尾部指针（用户交互锚点用）
}

// Tail 对应"触发时刻的模型尾部"：交互输出以此为锚点插入。
type Tail struct {
    ItemID string // 触发时刻最后一项的 ID
    Seq    uint64 // 触发时刻最后提交序号
}
```

- 渲染层遍历 `Items` 生成 UI cells（对应 Codex `thread_to_transcript_cells`）。
- `Tail` 由编码器在每次事件处理后推进；`/debug`、`/model` 输出捕获当前 Tail，
  之后以该 Tail 为锚点参与总序。

---

## 3. Item 字段规格

```go
type Item struct {
    ID       string   // 全局唯一，编码器单调分配："item-{n}"
    Seq      uint64   // 提交序号（单调、仅追加语义；见 §4.2）
    Kind     Kind     // 信息块类型（复用现有 historyCell Kind 语义，见 §5）
    CauseID  string   // 父事件 id（并行工具输出 → 工具调用 id；"" 表示无父）
    Status   Status   // 生命周期状态机：pending → running → completed / failed / canceled
    Head     *Block   // 当前渲染内容头（增量 upsert 的落点）
    Created  uint64   // 创建时的编码器时钟（单调）
    Updated  uint64   // 最近一次 upsert 的编码器时钟（单调）
}
```

### 3.1 字段明细

| 字段 | 类型 | 分配者 | 语义 | Codex 对应 |
| --- | --- | --- | --- | --- |
| `ID` | `string` | 编码器（`nextItemID`） | 全局唯一，跨轮次不重用；格式 `item-{n}` | `ThreadItem::id`（`item.rs:227-333`） |
| `Seq` | `uint64` | 编码器（`nextSeq`） | 每次提交递增；**仅追加语义**，用于乱序检测与重放校验 | `next_item_index`（`thread_history.rs:1451-1455`） |
| `Kind` | `Kind` | 编码器（事件类型映射） | 决定渲染样式与生命周期语义 | `ThreadItem` 枚举变体 |
| `CauseID` | `string` | 执行发起方 → 事件 → 编码器 | 因果锚定；空串 = top-level | 事件内嵌 `tool_call_id` / turn 分组 |
| `Status` | `Status` | 编码器（状态机） | 见 §4.1 | `CommandExecution::status`、`McpToolCall::status` |
| `Head` | `*Block` | 编码器（upsert） | 流式增量的最新内容快照 | item 的 `content` 字段 |
| `Created` / `Updated` | `uint64` | 编码器 | 审计与重放诊断 | builder 内部时钟 |

### 3.2 不变式

1. `ID` 全局唯一且单调分配：`nextItemID` 只在编码器内递增，不回退。
2. `Seq` 单调递增：同一编码器实例内，后提交的 Item 的 `Seq` 严格大于先提交的；
   `Created <= Updated`。
3. `Items` 数组顺序 == `Seq` 升序（重放后仍然成立）。
4. 一个 `Item` 只有一个 `CauseID`；`CauseID` 一旦分配不可变。
5. 渲染层**只读** `Items`；所有变更经编码器 API 发生。

---

## 4. 生命周期

### 4.1 状态机

```text
                  append（事件到达，新块）
                          │
                          ▼
                     ┌─────────┐
                     │ pending │ ← 工具调用已发起，输出未到
                     └─────────┘
                          │ upsert（首个输出/首个 delta）
                          ▼
                     ┌──────────┐
              ┌─────►│ running  │◄────┐
              │      └──────────┘     │ upsert（增量）
              │            │
              │            │ upsert（最终态）
              │            ▼
              │   ┌───────────────┐
              │   │ completed     │
              │   │ failed        │
              │   │ canceled      │ ← remove（回滚/取消）
              │   └───────────────┘
              │            │
              │            ▼
              └────────┌────────┐
                       │ 终态    │ 不得再被 upsert（终态后仅允许 remove）
                       └────────┘
```

- 状态迁移由编码器内的 `transition(item, event)` 校验；非法迁移（如
  completed → running）记录 warning 并拒绝。
- 对应 Codex：`CommandExecution` begin→running→completed 状态机
  （`thread_history.rs` upsert 分支），本项目现为 `commandResultCell` 的
  `sequence` 演进（`chat_history_cell.go:165-179`）——规格化后状态为显式字段。

### 4.2 提交语义（append / upsert / remove）

| 操作 | 触发事件示例 | 语义 | 幂等规则 |
| --- | --- | --- | --- |
| `append` | 用户消息、assistant 消息起点、工具调用发起、reasoning 块起点 | 在 `Items` 尾部追加新 Item，分配 `ID/Seq`，`Status=pending` | 同 `ID` 重复 append → 拒绝（ID 不重用） |
| `upsert` | assistant delta、工具输出、命令状态变化 | 按 `ID` 定位既有 Item，更新 `Head/Status/Updated`；**找不到则退化为 append** | 同 `ID` 同内容重复 upsert → 跳过（幂等） |
| `remove` | 回滚、取消、工具被终止 | 按 `ID` 从 `Items` 移除 | 移除不存在的 `ID` → 忽略（幂等） |

> upsert 退化规则是乱序免疫的关键：输出先于调用到达时，输出自成一个块；
> 调用事件随后到达时，按 `CauseID` 归并或按规则合并（详见编码器设计 §4）。

---

## 5. Kind 与现有 historyCell 的对应

`Item.Kind` 复用现有 `historyCell` 的 `Kind()` 语义（`chat_history_cell.go`），
并补齐身份字段后的统一枚举：

| `Item.Kind` | 现状对应 | 生命周期 | 说明 |
| --- | --- | --- | --- |
| `KindUser` | user cell | append 一次，不可变 | 用户消息 |
| `KindAssistant` | assistant cell（流式） | append → 多次 upsert → 终态 | delta 流 |
| `KindReasoning` | reasoning 块（`ReasoningBlock`） | append → upsert（summary 演进） | 可折叠 |
| `KindToolCall` | 工具调用 cell | append（pending）→ upsert（running/输出）→ 终态 | 携带 `CauseID` 供子输出锚定 |
| `KindToolOutput` | 工具输出 cell | append（CauseID 指向 ToolCall）→ upsert | 并行输出经 `CauseID` 归组 |
| `KindCommand` | `commandResultCell` | append → 状态机演进 | 现有 `{id, sequence}` 升级为完整 Item |
| `KindSystem` | 会话/诊断 cell | append 一次 | 不带 CauseID |
| `KindUserInteraction` | `/debug`、`/model` 输出 | append（Tail 锚定） | 不参与因果链，见 §1.4 |

### 5.1 与 `historyCell` 接口的演进

现状（`chat_history_cell.go:36-43`）：

```go
type historyCell interface {
    Kind() Kind
    DisplayLines(width int) []string
    // 无 ID()、无 Status() —— 身份与状态缺失
}
```

目标：接口增加身份与状态访问器（兼容过渡：默认实现按指针地址生成临时 ID）：

```go
type historyCell interface {
    Kind() Kind
    ID() string
    Seq() uint64
    Status() Status
    CauseID() string
    DisplayLines(width int) []string
}
```

- 过渡期：未接入编码器的 cell 由 `defaultCellID(cell)`（指针地址）兜底，
  保证渲染层代码可先行迁移到"按 ID 消费"。
- 终态：所有 cell 均由编码器产出，`ID/Seq/CauseID` 为编码器分配值。

---

## 6. 增量变更集（渲染层消费契约）

编码器每次 `Encode` 返回变更集（对应 Codex `ThreadHistoryChangeSet`）：

```go
type ItemChange struct {
    Op       Op     // Append / Upsert / Remove
    Item     *Item  // 变更后的完整 Item（Remove 时仅 ID 有效）
    Revision uint64 // 该 Item 的修订号（upsert 递增）
}

type ChangeSet struct {
    Changes []ItemChange   // 按 (ID, Revision) 去重合并后的有序变更
    Tail    *Tail          // 本次编码后的模型尾部
}
```

- 渲染层**只应用变更集**，不重放原始事件；同一 Item 的多次 upsert 合并为
  最新快照（对应 `ThreadHistoryChangeAccumulator`，`thread_history.rs:155-232`）。
- 变更集是渲染层与 Scene 事务（unified plan §6.2 `SceneTransaction`）的
  标准输入：一次编码可产出多条变更，打包为一次事务提交。

---

## 7. 持久化与重放

- 编码器输入事件按到达顺序持久化为事件日志（对应 Codex rollout items）。
- 恢复时用**同一编码器**重放事件日志 → 幂等重建 `RenderModel`；
  重建后的 `Items` 顺序、`ID`、`Seq` 与中断前一致（重放校验点：`Seq` 单调性）。
- 会话恢复后 UI 与恢复前一致（unified plan §6.3 规则 6："replay 读取持久化
  顺序并重建相同 cell sequence"由此获得数据面支撑）。

---

## 8. 与既有文档的衔接

| 文档 | 关系 |
| --- | --- |
| 上位方案：[unified-encoder-plan](./aicli-event-stream-rendering-order-unified-encoder-plan.md) | 本规格是其中 §5.2 的展开 |
| [EventEncoder 接口设计](./aicli-event-stream-rendering-order-event-encoder-api-design.md) | 本模型的产生者 |
| [迁移路线图](./aicli-event-stream-rendering-order-migration-roadmap.md) | 本模型的落地节奏 |
| unified-render-architecture-refactor-plan §5.2/§6.1 | 渲染层 Scene 消费本模型；`TranscriptCell` 与 `Item` 的映射见路线图 §5 |
| render-engine-module-design §3.1 | RenderEngine 的意图级 API 输入来自编码器变更集 |
| ui-ux-rendering-codex-reference-plan §6 | `Renderable` 渲染的 `Document` 对应 `Item.Head` 的产物 |
