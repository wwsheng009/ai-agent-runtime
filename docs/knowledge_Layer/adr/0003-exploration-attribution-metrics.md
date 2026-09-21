# ADR-0003: 探索归因与 shadow 差异率度量

- **Status**: Proposed
- **Date**: 2026-09-20
- **Deciders**: 项目 owner
- **Gate**: `Phase1-start`（口径）/ **`Phase1-shadow`**（阈值：α 与 Phase 1 门槛数值，2026-09-21 由 `Phase0-baseline` 改，见 §10）
- **Reversibility**: cheap（仅测量，不改产品行为）
- **Supersedes**: `supplement/05` §9.3 的"分母定义"提问
- **Related**: `backend/internal/usageledger/sqlite_store.go` L193–222；`04` §7.6、§7.1、§7.2；`supplement/05` §1.4

---

## 1. Context

### 1.1 已核实的证据

**证据 1 — `usageledger` 只有一张表，且扩展点是现成的。**

`backend/internal/usageledger/sqlite_store.go` 的 `init()`（L193–222）：

```go
statements := []string{
    `CREATE TABLE IF NOT EXISTS token_usage_history (
        id TEXT PRIMARY KEY,
        request_id TEXT,
        model_id TEXT,
        provider_id TEXT,
        input_tokens INTEGER NOT NULL DEFAULT 0,
        output_tokens INTEGER NOT NULL DEFAULT 0,
        total_tokens INTEGER NOT NULL DEFAULT 0,
        message_count INTEGER NOT NULL DEFAULT 0,
        max_tokens INTEGER NOT NULL DEFAULT 0,
        success INTEGER NOT NULL DEFAULT 0,
        status_code INTEGER NOT NULL DEFAULT 0,
        metadata_json BLOB,
        created_at TEXT NOT NULL
    )`,
    `CREATE INDEX IF NOT EXISTS idx_token_usage_history_created_at
        ON token_usage_history(created_at DESC, id DESC)`,
}
for _, statement := range statements {
    if _, err := s.db.ExecContext(ctx, statement); err != nil { ... }
}
```

- 这是一个 **幂等的 `CREATE TABLE IF NOT EXISTS` 列表** —— 追加一条语句即完成 schema 扩展。
- `token_usage_history` 的粒度是**一次 LLM 请求**（`request_id` / `model_id` / `provider_id` / `input_tokens` / `output_tokens`）。

**证据 2 — 粒度不匹配。**

shadow 差异率的观测单元是**一次代码检索工具调用**（`grep` / `view`），
不是一次 LLM 请求。把工具调用行塞进 `token_usage_history` 是**范畴错误**：
会污染 `04` §7.2 的 token 统计口径，且 `request_id` 对工具调用无意义。

**证据 3 — `04` 的既有规则。**

- `04` §7.6：阈值必须由 Phase 0 基线校准，"没有基线就不要写死阈值"。
- `04` §7.1：指标必须可测、可归因、可复算。

### 1.2 问题陈述

`supplement/05` §9.3 问的是：

> `shadow` 模式的差异率分母定义：按 turn、按 `grep/view` 调用次数、还是按 token？

这个问题**混合了两件不同的事**：

1. **度量口径**（instrument）——现在就能定，且必须现在定，否则 Phase 0 收集的数据不可比。
2. **阈值**（threshold）——**现在不能定**，必须等基线数据（`04` §7.6）。
   > 2026-09-21 修订：原文写"等 Phase 0 基线"。但 α 需要 **shadow 对比数据**，而 Phase 0 是 `mode=off`——该 Gate 结构性不可达。产出 Gate 改为 `Phase1-shadow`，见 §10。

把它们混在一个"待裁决"里，会导致两种失败：
要么草率定了阈值（违反 `04` §7.6），要么因为阈值不能定而连口径也不定（Phase 0 白跑）。

**本 ADR 的核心动作：把口径与阈值拆开，口径现在定死，阈值显式推迟。**

---

## 2. Decision Drivers

| # | 判据 | 可检验形式 |
|---|---|---|
| D1 | 口径必须可复算 | 给定同一批调用记录，任何人算出同一个数 |
| D2 | 必须有明确分母，且分母排除无真值样本 | 零结果调用不计入主指标，单独计数 |
| D3 | 不得污染既有 token 统计 | `token_usage_history` 行数与语义不变 |
| D4 | 阈值不得现在写死 | 主指标阈值标注为**由基线数据产出**（Gate = `Phase1-shadow`，见 §10） |
| D5 | 不得存储用户查询明文 | 只存 hash |
| D6 | 必须能定位"索引在哪种查询上失败" | 记录 tool / project / source 维度 |
| D7 | 扩展方式必须符合既有代码习惯 | 追加到 `init()` 的 statements 列表 |
| D8 | 必须能表达"索引更贵但更全"与"索引更便宜但漏"两种失败 | 覆盖度与经济性分开度量 |

---

## 3. Considered Options

### 3.1 存储位置

| 选项 | 描述 | 优点 | 代价 |
|---|---|---|---|
| **A** | 新增表 `exploration_attribution` 到 usageledger store | 粒度正确（D1）；可查询（D6）；符合 D3/D7 | 多一张表 |
| **B** | 写入 `token_usage_history.metadata_json` | 零 schema 改动 | 粒度错误（证据 2）；污染 token 统计（违反 D3）；需 JSON1 才能查询 |
| **C** | 写入 knowledge DB | 靠近数据源 | 归因数据与 session/usage 分析分离，破坏 `04` 的"埋点以 usage 分析为准" |
| **D** | 只记日志不落库 | 最简单 | 无法复算（违反 D1） |

### 3.2 度量单元

| 单元 | 定义 | 评估 |
|---|---|---|
| per-turn | 每轮聚合 | 丢失"哪个查询失败"，无法定位（违反 D6） |
| **per-tool-call** | 每次被拦截的 `grep`/`view` | 粒度匹配（证据 2），可定位 |
| per-token | 按 token 加权 | 单次大文件读取会主导，噪声大 |
| per-file | 索引是否覆盖被读文件 | 需文件级归因，Phase 1 尚未有 |
| per-symbol | 符号查找是否命中 | 仅适用于符号查询，不适用于 grep shadow |

### 3.3 主指标定义

| 选项 | 定义 | 评估 |
|---|---|---|
| 集合完全一致 | `K == G` | 过严：索引返回定义、grep 返回 40 处文本匹配时判为失败，但那可能更好 |
| **覆盖度 + 经济性双条件** | 见 §4.2 | 能同时表达 D8 的两种失败 |
| 只要非空 | `K != ∅` | 可被"返回整个文件"钻空子 |
| 只看 token | `tokens(K) <= tokens(G)` | 会奖励"返回空结果" |

---

## 4. Decision

采纳 **3.1-A + 3.2-per-tool-call + 3.3-双条件**。

### 4.1 表结构（追加到 usageledger `init()` 的 statements 列表）

```sql
CREATE TABLE IF NOT EXISTS exploration_attribution (
    id TEXT PRIMARY KEY,
    session_id TEXT,
    turn_id TEXT,
    request_id TEXT,
    tool TEXT NOT NULL,              -- grep | view
    query_hash TEXT,                 -- sha256(tool + pattern + scope)，不存明文
    project_id TEXT,
    baseline_n INTEGER NOT NULL DEFAULT 0,     -- |G|：被拦截调用实际返回的条目数
    candidate_n INTEGER NOT NULL DEFAULT 0,    -- |K|：索引侧候选条目数
    overlap_n INTEGER NOT NULL DEFAULT 0,      -- |G ∩ K|
    baseline_tokens INTEGER NOT NULL DEFAULT 0,
    candidate_tokens INTEGER NOT NULL DEFAULT 0,
    coverage REAL,                   -- overlap_n / baseline_n
    economy REAL,                    -- candidate_tokens / baseline_tokens
    usable INTEGER NOT NULL DEFAULT 0,         -- 0/1，见 4.2
    source TEXT,                     -- parser | lsp | heuristic
    knowledge_mode TEXT NOT NULL,    -- shadow | on
    created_at TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_exploration_attribution_created_at
    ON exploration_attribution(created_at DESC, id DESC);

CREATE INDEX IF NOT EXISTS idx_exploration_attribution_tool_time
    ON exploration_attribution(tool, created_at DESC);
```

- 追加到既有 `statements` 切片，复用 `CREATE TABLE IF NOT EXISTS` 幂等语义（D7）。
- **D3 的准确含义（2026-09-21 修订）**：不得**污染** `token_usage_history` 的既有统计口径。可检验形式为三条同时成立：
  1. **不新增行**——本表数据不得以任何形式写入 `token_usage_history`（这是 §3.1 选项 B 被否决的实质原因）；
  2. **不改变既有聚合结果**——对既有列的任意 `SUM` / `COUNT` / `AVG`，在追加列前后结果一致；
  3. **不改变历史行语义**——追加列必须可空或带非破坏 `DEFAULT`，历史行取默认值即等价于改动前。
  > 原措辞"`token_usage_history` 一列不改、一行不变"是**字面**约束，与 Phase 0 交付 2 已实现的 `ALTER TABLE … ADD COLUMN`（9 列，`INTEGER NOT NULL DEFAULT 0`，见 `backend/internal/usageledger/sqlite_store.go` L257–290）冲突。按上述三条，该实现**满足** D3 的实质要求（请求级粒度、历史行语义不变、`mode=off` 时新列恒为 0）。本次修订是**把 D3 写成它本来的意思**，不是放宽它。

### 4.2 判定规则

对每一次被拦截的调用：

```text
coverage := overlap_n / baseline_n          （baseline_n > 0 时）
economy  := candidate_tokens / baseline_tokens
usable   := (coverage >= α) AND (economy <= 1.0)
```

- **α 不在本 ADR 决定。** α 由 **Phase 1 shadow 实测**产出（D4）。
  初始探索值 0.8，仅用于管线联调，**不得作为验收门槛**。
  > 2026-09-21 修订：原写"α 是 Phase 0 的产出"。α 是 shadow 覆盖率阈值，需要 `grep` vs 索引的**逐调用对比数据**；Phase 0 为 `mode=off` 且知识层未接入任何进程，**结构上不可能产出该数据**。产出 Gate 改为 `Phase1-shadow`，见 §10。

### 4.3 拦截范围（明确边界）

| 工具 | 是否 shadow | 理由 |
|---|---|---|
| `grep` | **是** | 代码检索的主要路径，直接对应 `code.search` |
| `view` | **是** | 文件/区间读取，对应索引的 chunk/symbol 视图 |
| `glob` | **否** | 文件发现成本极低，索引收益小，纳入会稀释口径 |
| 其他 | 否 | — |

### 4.4 候选查询映射

| 被拦截调用 | 索引侧候选 |
|---|---|
| `grep pattern`（带 path/glob） | `code.search(pattern)`，作用域限制为同一 path/glob |
| `view file offset/limit` | 该区间的索引 chunk / 符号视图 |

### 4.5 指标族

| 编号 | 名称 | 公式 | 分母 | 用途 |
|---|---|---|---|---|
| **M1** | 调用级可用率 | `avg(usable)` | `baseline_n > 0` 的被拦截调用 | **Phase 1 主门槛** |
| **M2** | 覆盖度 | `avg(coverage)` | 同上 | 定位漏检（D8） |
| **M3** | 经济性 | `avg(economy)` | 同上 | 定位更贵（D8） |
| **M4** | token 收益 | `Σ(baseline_tokens − candidate_tokens) / Σ baseline_tokens` | 同 M1 | Phase 2 参考，非门槛 |

- **零结果调用**（`baseline_n == 0`）落库但**不进 M1/M2/M3 分母**（D2），
  单独以 `baseline_n = 0` 过滤即可统计其数量，作为"索引未被触发"的透明性指标。
- 聚合同时输出 `mean / p50 / p90`，避免被少数极端调用主导。

### 4.6 隐私

- `query_hash = sha256(tool + "\x00" + pattern + "\x00" + scope)`，**不存 pattern 明文**（D5）。
- `project_id` 存稳定键，不存绝对路径。
- 与 `03` 的既有脱敏策略一致；本表不引入新的敏感面。

---

## 5. Rationale

逐条回应 Decision Drivers：

- **D1/D7**：口径完全由列定义决定，任意人可复算；扩展方式就是往 `init()` 的切片里加一条，与既有代码完全一致。
- **D2**：零结果调用没有真值——把"索引返回空"和"grep 也没找到"混在一起，会让一个完全失效的索引看起来命中率很高。因此显式排除并单独计数。
- **D3**：选项 B 被否决的**真实缺陷**不只是"不优雅"：`token_usage_history` 是 `04` §7.2 收益指标的**输入表**，往里塞工具调用行会让"M4 token 收益"的分子分母同时失真。这是**测量污染**，比多一张表贵得多。
- **D4**：α 与门槛显式标注为**由基线数据产出**、不写死。这是本 ADR 与 `04` §7.6 的一致性要求。（2026-09-21：产出 Gate 由 `Phase0-baseline` 修正为 `Phase1-shadow`，理由见 §10。）
- **D5**：`query_hash` 取代明文。
- **D6**：`tool` / `project_id` / `source` 三维可下钻，直接回答"索引在哪种查询上失败"。
- **D8**：选项"只看 token"被否决的**真实缺陷**是它可以被"返回空结果"刷到最优——经济性必须与覆盖度同时成立才有意义。这正是 §4.2 用 AND 而不是加权和的原因：加权和会让"极便宜但漏一半"和"完整但略贵"得到相近分数，掩盖两种完全不同的失败。

**关于为什么把口径与阈值拆开**：`04` §7.6 明确"没有基线就不要写死阈值"。
但 §7.1 又要求指标"可测、可归因、可复算"。
两者的正确组合是：**口径（可复算的定义）现在就冻结，阈值（α、门槛数值）由 Phase 0 产出。**
如果口径也不定，Phase 0 收集的数据将无法聚合，基线的意义会被抹掉。

---

## 6. Consequences

### 6.1 Positive

- 表与口径在 Phase 0 落地（DDL + 埋点骨架），Phase 1 shadow 一开始产生数据即可算 M1–M4，无需返工。
- 定位能力：可直接查询"哪个 tool / 哪个 project / 哪种 source 下 coverage 最低"。
- 与 `04` §7.6 的校准流程天然衔接。
- 零 schema 迁移风险（追加 `IF NOT EXISTS`）。

### 6.2 Negative / Accepted trade-offs

- **多一张表。** 主动接受：这是 D1/D3 的必要代价。
- **`coverage` 对"索引答案更好但不重合"的调用会低估。** 例如 `grep "func Foo"` 返回 40 处文本匹配，而 `code.search` 返回 3 个定义。主动接受：`overlap_n / baseline_n` 会偏低。缓解方式是同时看 `candidate_n` 与 M4（token 收益），并在 Phase 0 报告里对 `candidate_n < baseline_n` 的样本单独抽样人工核对。**这是本 ADR 已知的最大度量偏差，必须写进 Phase 0 报告。**
- **`view` 的区间映射不精确**（`offset/limit` 与索引 chunk 边界未必对齐）。主动接受，Phase 1 记录原始 offset。

---

## 7. Reversal Plan

- 撤销测量：`DROP TABLE exploration_attribution`，或停止写入，成本接近零。
- 改口径：新增列 + 回填（Phase 0 数据量小）。
- 提升为永久指标：把表从 usageledger 迁到正式 analytics schema，走一次 ADR。
- **无产品行为影响**：shadow 模式下不暴露工具，本 ADR 不改任何用户可见行为。

---

## 8. Validation

| 检查 | 形式 | 门槛 |
|---|---|---|
| 可复算 | 同一批记录重算 M1–M4，结果一致 | 必须通过 |
| 不污染 | `token_usage_history` **行数不变**，且既有列的 `SUM` / `COUNT` 与追加列前一致（D3 三条，见 §4.1） | 必须通过 |
| 零结果排除 | 构造 `baseline_n = 0` 样本，确认不进 M1 分母 | 必须通过 |
| 无明文 | 全表扫描确认无 pattern 原文 | 必须通过 |
| 幂等 | 重复 init 不报错、不重复建表 | 必须通过 |
| 定位能力 | 按 `tool` / `project_id` / `source` 分组能产出可读报告 | 必须通过 |
| 阈值未写死 | 文档与代码中 α 标注为 Phase 0 产出 | 必须通过 |

---

## 9. Alternatives Rejected (and why)

| 选项 | 否决理由（一句话） |
|---|---|
| 写入 `metadata_json` | 粒度错误，且会污染 `04` §7.2 的 token 口径 |
| 按 turn 聚合 | 无法定位是哪个查询失败（违反 D6） |
| 按 token 加权 | 单次大文件读取主导结果，噪声不可用 |
| 集合完全一致 | 过严，会把"更精炼的答案"判为失败 |
| 只要非空 | 可被"返回整个文件"钻空子 |
| 只看 token 经济性 | 奖励空结果，与覆盖度必须同时成立 |
| 现在就定 α 与门槛 | 违反 `04` §7.6 |
| 连口径也一起推迟 | Phase 0 数据不可聚合，基线失去意义 |

---

## 10. Open Follow-ups

| 项 | Gate |
|---|---|
| α 的取值与 Phase 1 门槛数值 | **`Phase1-shadow`**（2026-09-21 由 `Phase0-baseline` 改，见下） |
| `coverage` 低估问题的**警告写入 Phase 0 报告** | `Phase0-baseline`（**已完成**，见 `reports/phase0_baseline_report.md` §7） |
| `coverage` 低估问题的**抽样核对报告**（`candidate_n < baseline_n` 样本人工核对） | `Phase1-shadow`（Phase 0 无 shadow 数据，无法抽样） |
| `view` 区间与 chunk 边界的对齐精度 | `Phase1-start` |
| 是否需要 `per-file` / `per-symbol` 归因层 | `Phase2-start` |
| 是否把本表提升为正式 analytics schema | `Phase3-start` |
| 与 `docs/plan/session-usage-analytics-and-agent-diagnostics-plan.md` 的字段对齐 | `Phase1-start` |

**Gate 变更说明（2026-09-21）**：原 Gate `Phase0-baseline` 被理解为"Phase 0 基线跑完即可定 α"。但 α 是 **shadow 覆盖率阈值**，其取值必须由 `grep` 与索引的**逐调用对比数据**校准；而 Phase 0 是 `mode=off`，且知识层此前未接入任何进程（`04` §5 Phase 1 交付 6 于同日补入）。后果是：

- Phase 0 **结构上不可能**产出 α → 原 Gate 永远无法满足 → 本 ADR 无法 Accept → **Phase 1 被自锁**。
- 现拆为两个 Gate：`Phase0-baseline` 负责**基线与偏差警告**（可达成，已完成）；`Phase1-shadow` 负责 **α 与门槛数值**（shadow 数据积累后达成）。
- 因此**本 ADR 的 Accept 不再被阈值阻塞**：口径部分按 `Phase1-start` 生效，阈值部分显式标注为 `Phase1-shadow` 产出——这与 §4.2"α 不在本 ADR 决定"完全一致。
