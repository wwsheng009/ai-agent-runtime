# ADR-0004: 陈旧索引下的 `code.*` 工具面

- **Status**: Proposed
- **Date**: 2026-09-20
- **Deciders**: 项目 owner
- **Gate**: `Phase2-start`
- **Reversibility**: cheap（注册策略 + 描述文案）
- **Supersedes**: `supplement/05` §9.4 的"是否注册"提问
- **Related**: `backend/internal/toolkit/registry.go` L15/L31/L172；`backend/internal/toolkit/tools/apply_patch_test.go` L880–927（`RegisterGroup` 既有签名）；`backend/internal/toolkit/interface.go` L32–34；`listable.go` L32/L60；`mcp_adapter.go` L31；`supplement/05` §1.3、§2.2；`04` §4.6

---

## 1. Context

### 1.1 已核实的证据

**证据 1 — 工具注册是注册表 + 分组开关。**

`backend/internal/toolkit/registry.go`：

```go
type Registry struct { ... }                            // L15
func (r *Registry) Register(tool Tool) error { ... }     // L31
```

`backend/internal/toolkit/tools/apply_patch_test.go` L880–927 固化了分组 API 的存在与签名：

```go
func (m *Manager) RegisterGroup(name string, active bool) error { ... }
```

即有 `(name, active)` 形式的分组激活开关。这为"按条件注册一组工具"提供了**现成机制**。

**证据 2 — 工具定义可携带模型可见元数据。**

`backend/internal/toolkit/interface.go` L32–34：

```go
// ToolDefinitionMetadataProvider allows tools to expose extra definition metadata.
type ToolDefinitionMetadataProvider interface { ... }
```

`listable.go` L32/L60、`mcp_adapter.go` L31、`registry.go` L172 均消费该接口。
即：工具对模型的描述文本是**可编程的**，不是硬编码。

**证据 3 — reader 模式确实会产生陈旧快照。**

`supplement/05` §1.3：reader 以 `mode=ro&_query_only=1` 打开，
读到的是"截至上次 writer 提交的快照"，并带 `snapshot_ts` 与 `staleness`。
这是设计内的常态（runtime-server 是 writer，aicli tui 是 reader 是最常见形态），不是异常。

### 1.2 问题陈述

`supplement/05` §9.4 问：

> reader 模式下 `code.*` 是否仍注册给模型？

这个提法把问题**二值化**了，而真正的风险分布是连续的：

- **定义类查询**（`code.find_symbol`、`code.search`）：陈旧只导致"新符号查不到"。
  模型查不到会自然回退到 `grep`。**失败是显式的、可自愈的。**
- **关系类查询**（`code.find_refs`、`code.callers`、`code.impact`）：
  陈旧导致"10 秒前新增的调用者不存在"。
  模型会据此得出"没有调用者"并**据此重构代码**。
  **失败是静默的、不可自愈的、会造成实际破坏。**

**这两类的风险量级差了一个数量级，不能用同一个开关处理。**

### 1.3 关键约束：本仓库的模型已被引导优先用 `grep`/`view`

本会话的环境指引（`AGENTS.md` / 系统提示）明确要求模型：

> Prefer toolkit `grep` for code search instead of shell `rg`/`grep`
> Prefer toolkit `ls`/`glob`/`view` for filesystem inspection

即模型**已经有**一套可靠、实时、被反复强化的首选工具。
在这个前提下引入一个**可能陈旧**的竞争来源，是净负收益——
除非我们让陈旧性对模型**可见且可判断**。

---

## 2. Decision Drivers

| # | 判据 | 可检验形式 |
|---|---|---|
| D1 | 不得静默返回陈旧的关系类结果 | 构造"新增调用者后立即查询"的场景，断言不返回"无调用者" |
| D2 | 定义类查询在陈旧时仍应可用 | 陈旧 reader 下 `code.find_symbol` 仍注册且能命中旧符号 |
| D3 | 陈旧性必须对模型可见 | 每次结果含 `staleness_seconds` 与 `snapshot_ts` |
| D4 | 同一工具的 JSON schema 不得随模式变化 | 对比两模式下同一工具的定义 schema 完全一致 |
| D5 | 必须能一键全关 | 存在让 `code.*` 全部不注册的开关 |
| D6 | 实现必须复用既有分组机制 | 使用 `RegisterGroup(name, active)`，不新造机制 |
| D7 | 阈值不得现在写死 | 边界值标注为 Phase 0 产出 |

---

## 3. Considered Options

| 选项 | 描述 | 优点 | 代价 |
|---|---|---|---|
| **A** | 不注册任何 `code.*` | 零陈旧风险（D1） | 完全放弃索引收益；reader 是常态场景 → 等于放弃整个方案 |
| **B** | 全部注册，带 `staleness` 字段 | 收益最大 | 违反 D1：关系类静默错误仍在 |
| **C** | 全部注册，但结果文本加"可能陈旧"警告 | 简单 | 警告是软约束；模型在高负载下会忽略；不满足 D1 的"必须不返回" |
| **D** | 按陈旧度分级：新鲜全开；中等只开定义类；过旧全关 | 风险与收益匹配（D1+D2） | 需定义两档边界（阈值推迟，见 D7） |
| **E** | 全部注册，但关系类查询内部强制回退到实时 `grep` | 结果永远实时 | 等于关系类永远没有索引加速，且 `find_refs` 语义被破坏（返回文本匹配而非引用） |

---

## 4. Decision

采纳 **选项 D**，并做四项配套。

### 4.1 分级注册表

设 `S = staleness_seconds`，两档边界 `S_fresh`（初始 60s）、`S_max`（初始 15min），
**两者均为初始值，由 Phase 0 校准（D7）**：

| 条件 | writer | reader，`S ≤ S_fresh` | reader，`S_fresh < S ≤ S_max` | reader，`S > S_max` |
|---|---|---|---|---|
| `code.find_symbol` | ✅ | ✅ | ✅ | ❌ |
| `code.search` | ✅ | ✅ | ✅ | ❌ |
| `code.find_refs` | ✅ | ✅ | ❌ | ❌ |
| `code.callers` | ✅ | ✅ | ❌ | ❌ |
| `code.impact` | ✅ | ✅ | ❌ | ❌ |

- 过旧档（`S > S_max`）等价于 `knowledge.mode=off` 的工具面。
- 分组名统一为 `code`，通过 `RegisterGroup("code", active)` 与细粒度子组实现（D6）。
- 每次 heartbeat 变化或 writer 提交后**重新评估**并切换分组，不重启会话。

### 4.2 结果契约（不随模式变化的部分）

**所有** `code.*` 结果必须包含：

```json
{
  "source": "index",
  "snapshot_ts": 1758345600,
  "staleness_seconds": 12,
  "completeness": "full"
}
```

- `completeness ∈ {full, partial, fallback}`。
- reader 模式下 `staleness_seconds` 必须为**实际值**，不得为 0 或省略（D3）。
- writer 模式下 `staleness_seconds = 0`。

### 4.3 schema 恒定，描述可变

- **JSON schema 完全不变**（D4）：同一工具在任意模式下对模型的参数与返回结构一致。
  理由：随模式变化的 schema 会让模型的行为不可预测，也会让 prompt 缓存失效。
- **`description` 文本按模式变化**（复用证据 2 的 `ToolDefinitionMetadataProvider`）。

只有**被注册**的工具才需要描述变体。因此实践中只有一种描述变体需要实现：
当 reader 且 `S_fresh < S ≤ S_max` 时，`code.find_symbol` / `code.search` 的 description 追加：

```text
结果来自本地索引快照，可能落后约 N 秒。若需确认某个符号是否存在或已删除，请用 grep 复核。
```

### 4.4 逃生舱

| 开关 | 效果 |
|---|---|
| `knowledge.mode=off` | 不注册任何 `code.*`，不建目录（全局硬闸） |
| `knowledge.tools.stale_reader=off` | 即使 reader 也按 writer 策略注册（用户自担风险） |
| `knowledge.tools.enabled=false` | 索引照跑，但不注册工具（shadow 之外的第三种观测态） |

### 4.5 与 `04` §4.6 工具面收敛的关系

`04` §4.6 规定"`code.*` 是 `grep`/`view` 的增强前端，不替换"。
本 ADR 与之相容：分级注册只减少 `code.*` 的暴露面，**从不减少 `grep`/`view`**。
在任何分级下，`grep`/`view` 都可用且实时。

---

## 5. Rationale

逐条回应 Decision Drivers：

- **D1**：关系类查询在陈旧时**不注册**，因此不存在"返回静默错误"的路径。
  选项 B/C 被否决的**真实缺陷**是：它们试图用**提示**约束模型，但提示是软约束。
  而"10 秒前的调用者不存在"这种错误的后果是不可逆的（改错代码）。
  **对不可逆后果必须用硬约束（不注册），不能用软约束（警告）。**
- **D2**：定义类在中等陈旧下仍注册。理由：查不到新符号会导致模型回退 `grep`，失败是显式的。这与 D1 的"静默错误"性质完全不同。
- **D3**：§4.2 强制字段。
- **D4**：§4.3 明确 schema 恒定。这也是对 prompt 缓存友好（本仓库有 `internal/cacheanalytics` 与 `llm-cache-analytics-unified-plan.md`）。
- **D5**：§4.4 三个开关。
- **D6**：§4.1 复用 `RegisterGroup`。
- **D7**：`S_fresh` / `S_max` 标注初始值。

**关于选项 E**：它看起来"兼顾安全与收益"，但被否决的**真实缺陷**是
`code.find_refs` 若内部退化为文本匹配，它就不再是 `find_refs`，而是 `grep` 的别名。
这会让模型学到错误的工具语义（"find_refs 有时返回引用，有时返回文本匹配"），
比不提供该工具更糟。**工具语义必须稳定。**

---

## 6. Consequences

### 6.1 Positive

- reader（最常见形态）下仍然拿到定义类索引收益，同时**不存在**静默关系类错误。
- 模型可通过 `staleness_seconds` 自行判断是否需要 `grep` 复核。
- schema 恒定 → prompt 缓存友好，模型行为可预测。
- 复用既有分组机制，实现量小。

### 6.2 Negative / Accepted trade-offs

- **中等陈旧下 `code.find_refs` 不可用**，模型必须用 `grep` 或等 writer 提交。主动接受：这正是 D1 想要的结果。
- **两档边界值先给了初始值（60s / 15min），有写死阈值的嫌疑。** 主动接受但加约束：初始值只用于 Phase 2 跑通分级逻辑，**Phase 2 验收前必须用 Phase 0/1 数据替换**（D7）。
- **reader 下定义类仍可能漏新符号**。主动接受：漏检是显式失败，模型会回退。

---

## 7. Reversal Plan

- 想改为"reader 也全开"：`knowledge.tools.stale_reader=off` 一行配置，无需发版。
- 想改为"reader 全关"：把 `S_fresh` / `S_max` 都设为 0。
- 想撤销分级逻辑：删除 `RegisterGroup` 的条件分支，恢复无条件注册。
- **无数据迁移**：注册策略不持久化。

---

## 8. Validation

| 检查 | 形式 | 门槛 |
|---|---|---|
| 无静默错误 | 写文件新增调用者 → 立即以 reader 查询 `code.callers` | 该工具未注册（或返回不含该调用的结果且 `staleness_seconds` 非零） |
| 定义类可用 | 陈旧 reader 下 `code.find_symbol` 命中旧符号 | 必须通过 |
| 字段强制 | 全模式扫描 `code.*` 结果 | 全部含 `source`/`snapshot_ts`/`staleness_seconds`/`completeness` |
| schema 恒定 | 两模式下同一工具的 JSON schema diff | 为空 |
| 描述变体 | 中等陈旧下 `code.find_symbol` 描述含陈旧提示 | 必须通过 |
| 分组切换 | heartbeat 变化后无需重启即切换分组 | 必须通过 |
| grep 不受影响 | 所有分级下 `grep`/`view` 均可用 | 必须通过 |
| 阈值可替换 | 代码中 `S_fresh`/`S_max` 是常量而非字面量散布 | 必须通过 |

---

## 9. Alternatives Rejected (and why)

| 选项 | 否决理由（一句话） |
|---|---|
| A（全不注册） | reader 是常态，等于放弃整个方案 |
| B（全注册） | 关系类静默错误不可逆，必须硬约束 |
| C（全注册 + 警告） | 软约束在高负载下会被忽略，对不可逆后果不足 |
| E（关系类内部回退 grep） | 破坏工具语义稳定性，比不提供更糟 |
| 按 `knowledge.mode` 而非 `staleness` 分级 | `shadow`/`on` 描述的是暴露策略，不是数据新鲜度；reader 可以是 `on` 且很新鲜 |
| 让 `code.*` 内部实时读盘来"自愈" | 那就不再是索引工具，退化为 grep 的重复实现 |

---

## 10. Open Follow-ups

| 项 | Gate |
|---|---|
| `S_fresh` / `S_max` 的实际取值 | `Phase0-baseline` |
| 是否需要对 `code.impact` 单独设更严的档位 | `Phase2-start` |
| writer 提交后 reader 的通知机制（心跳轮询 vs 通知） | `Phase2-start` |
| 与 `04` §4.6 工具命名/优先级表的最终对齐 | `Phase2-start` |
| 描述变体对 prompt 缓存命中率的影响测量 | `Phase3-start` |
