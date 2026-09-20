# ADR-0007: 幽灵表清理与文档不变量

- **Status**: Proposed
- **Date**: 2026-09-20
- **Deciders**: 项目 owner
- **Gate**: `Phase1-start`
- **Reversibility**: cheap（删总览条目 + 迁一段 DDL + 加一个检查脚本）
- **Supersedes**: `02` §8 数据模型总览中 6 个无 DDL 的表名；`04` L546 的 `index_jobs` DDL（落点迁移）
- **Related**: `02_agent_harness_technical_design_spec_sqlite.md` L271–313（总览）、L320–1039（DDL）、L2159（`symbol_fts`）；`03_agent_harness_supplement.md` L139–1150；`04_completeness_review_and_optimized_plan.md` L546–558、L568、§4.3、附录 B；`adr/README.md` §7；ADR-0001 §4.7

---

## 1. Context

### 1.1 已核实的证据

**证据 1 — `02` 的总览块列了 28 个表名，DDL 只定义了 22 张。**

`02` §8 数据模型总览（L275–312）列出的名字：

```text
workspaces, repositories, branches, commits,
files, file_versions,
symbols, symbol_versions,
references, imports,
calls, inheritance, dependencies,
exploration_sessions, exploration_nodes, exploration_edges,
tasks, task_files, task_symbols,
context_snapshots, context_items,
tool_calls, tool_results,
cache_entries, invalidation_events,
language_projects, index_jobs, events
```

但 `02` 的 `CREATE TABLE` 区块（L320–1039）实际只定义 **22 张**：

```text
workspaces, repositories, commits, files, file_versions,
symbols, symbol_versions, references, calls, imports,
exploration_sessions, exploration_nodes, exploration_edges,
tasks, task_files, task_symbols,
context_snapshots, context_items,
tool_calls, tool_results, cache_entries, invalidation_events
```

另有 1 张虚拟表 `symbol_fts`（L2159）。

**差额 = 6 个无 DDL 的名字**：`branches`、`inheritance`、`dependencies`、
`language_projects`、`index_jobs`、`events`。

**证据 2 — `03` 不含 `branches` / `inheritance` / `dependencies` 的 DDL，但含改名后的对应物。**

`03` 的 `CREATE TABLE` 清单（grep 全文件）中**没有** `branches`、`inheritance`、`dependencies`，
但有：

- `03` L294：`CREATE TABLE inheritance_edges (...)`
- `03` L201：`CREATE TABLE dependency_versions (...)`

即：`02` 的裸名与 `03` 的实际表名**不一致**。这不是"缺失"，是**命名漂移**。

**证据 3 — `index_jobs` 的 DDL 落在 `04`，而不是 `02` 或 `03`。**

`04` L545–558：

```sql
-- 10. 索引任务（可观测 + 可重试 + 串行化）
CREATE TABLE index_jobs (
    id            TEXT PRIMARY KEY,
    workspace_id  TEXT NOT NULL,
    kind          TEXT NOT NULL,           -- light|deep|full_rebuild|gc
    status        TEXT NOT NULL,           -- queued|running|done|failed|cancelled
    files_total   INTEGER NOT NULL DEFAULT 0,
    files_done    INTEGER NOT NULL DEFAULT 0,
    error         TEXT,
    started_at    INTEGER,
    finished_at   INTEGER,
    FOREIGN KEY(workspace_id) REFERENCES workspaces(id) ON DELETE CASCADE
);
CREATE INDEX idx_index_jobs_ws ON index_jobs(workspace_id, status);
```

这**直接违反** `adr/README.md` §7 与 `../README.md` §7 声明的
"DDL 只允许出现在 `02`（core）与 `supplement/*`（extension），`04` 只引用"。
`04` 是"落地计划与验收事实源"，不是 schema 事实源。

**证据 4 — `04` §4.3 已声明 v1 表数上限，且已有推迟清单。**

- `../README.md` §3 记录：`04` §4.3 规定 "v1 表集：≤ 16 张"。
- `04` L568 明确列出推迟到 v2+ 的表（"03 附录 A 的其余 34 张"）。

即 `04` 已经有了"哪些进 v1、哪些推迟"的裁量，**但 `02` 的总览块完全没有表达这件事**——
它把 v1 core、extension、已推迟、以及根本不存在的名字**混在同一个无序列表里**。

**证据 5 — ADR-0001 已裁决其中 2 个。**

ADR-0001 §4.7：

| 幽灵表 | 处置 |
|---|---|
| `language_projects` | **删除** |
| `index_jobs` | **补 DDL**（extension schema） |
| `events` | **删除** |

且 ADR-0001 D6 明确要求"清单与 DDL 必须一致，机械可检验（见 ADR-0007）"。
**本 ADR 就是那个"ADR-0007"。**

### 1.2 问题陈述

三类缺陷，根因是同一个：**`02` 的总览块没有语义分类，且没有机械校验**。

| 缺陷 | 表现 | 后果 |
|---|---|---|
| **幽灵名** | 6 个名字无 DDL | 读者以为它们存在；实现者去建不存在的表 |
| **命名漂移** | `inheritance` vs `inheritance_edges` | 同一概念两个名字，注定出现"双事实源" |
| **DDL 越位** | `index_jobs` DDL 在 `04` | 破坏单一事实源；`04` 每次改动都可能静默改 schema |

`04` 附录 B 的 18 条矛盾点里，B6（项目模型重叠）正是这一类。
说明该缺陷**已被观察到**，但只被当成单点问题逐条裁决，
**没有建立防止复发的机制**——所以它会反复出现。

### 1.3 风险不对称

| 方向 | 失败形态 | 代价 |
|---|---|---|
| 只修当前 6 个 | 下一个 Phase 再长出新的幽灵名 | **中**（重复消耗裁决成本） |
| 修 6 个 + 建立机械不变量 | 一次性脚本 + 一条 CI 规则 | **低** |

**结论：必须同时修"实例"与"产生实例的机制"。**

---

## 2. Decision Drivers

| # | 判据 | 可检验形式 |
|---|---|---|
| D1 | 每个总览名必须可判定"有 DDL / 指向 extension / 已推迟 / 不存在" | 无未分类条目 |
| D2 | DDL 只允许出现在 `02` 与 `03`/`supplement/*` | `grep -n "CREATE TABLE" 04` 为空 |
| D3 | 同一概念不得有两个名字 | 全库表名集合无近义重复 |
| D4 | 不变量必须机械可检验，且进 CI | 存在一个脚本/测试，失败即阻断 |
| D5 | `04` 只能引用表名 | `04` 中的 `CREATE TABLE` 全部移除 |
| D6 | 不得为"对齐"而制造新表 | 本 ADR 只删/迁/改名，不新增 |
| D7 | 表数上限声明必须与 DDL 实际数量自洽 | 不变量脚本同时校验两者 |

---

## 3. Considered Options

| 选项 | 描述 | 优点 | 代价 |
|---|---|---|---|
| **A** | 给 6 个幽灵名逐个补 DDL | 总览与 DDL 立刻一致 | 造出 6 张 v1 不需要的表，违反 D6 与 `04` §4.3 |
| **B** | 只把 6 个名字从总览删掉 | 最小改动 | 不修命名漂移，不修 DDL 越位，不建机制（违反 D1/D2/D4） |
| **C** | 逐个分类处置 + 总览三分组 + 机械不变量 | 修实例也修机制（D1–D5） | 需写一个检查脚本 |
| **D** | 用代码生成 `02` 总览（从 DDL 反推） | 永不漂移 | 需把 `02` 的 DDL 变成机器可读的唯一输入；改动面大；且无法表达"已推迟" |
| **E** | 把所有 extension 表也搬进 `02` | 一处看全 | 违反 `03`/`02` 的分层；`02` 会膨胀到 70+ 张 |
| **F** | 只写文档规则，不做脚本 | 零成本 | 规则无强制力，`04` L546 就是"有规则但无人查"的活证据 |

---

## 4. Decision

采纳 **选项 C**，并做五项配套。

### 4.1 六个幽灵名的逐个处置（D1、D3、D6）

| 总览名 | 处置 | 理由 |
|---|---|---|
| `branches` | **删除** | `repositories.current_branch` + `commits.parent_hash` 已表达 v1 所需；无分支图需求 |
| `inheritance` | **删除并改名指向** | 正确落点是 `03.inheritance_edges`（extension，L294）；`02` 的裸名是命名漂移 |
| `dependencies` | **删除并改名指向** | 正确落点是 `03.dependency_versions`（extension，L201）；v1 不做依赖图（`04` L568 已推迟） |
| `language_projects` | **删除** | ADR-0001 §4.7 已裁：由 `project_languages` 取代，幽灵表从未有 DDL |
| `index_jobs` | **保留，补 DDL，但落点归 `supplement/`（extension）** | Phase 1 需要调度/进度/重试；DDL 现错放在 `04`，必须迁移（见 4.3） |
| `events` | **删除** | ADR-0001 §4.7 已裁：被 `03.events_outbox` + `consumer_offsets` 取代；v1 用 `invalidation_events` |

### 4.2 `02` §8 总览块改为三分组（D1）

总览块必须且只必须包含三组，**组内名字与 DDL 存在性严格对应**：

```text
【v1 core】——本文件随后给出 DDL
  workspaces, repositories, commits, files, file_versions,
  symbols, symbol_versions, references, calls, imports,
  exploration_sessions, exploration_nodes, exploration_edges,
  tasks, task_files, task_symbols,
  context_snapshots, context_items,
  tool_calls, tool_results, cache_entries, invalidation_events,
  symbol_fts (virtual)

【extension】——DDL 在 03 / supplement/*，不在本文件
  projects, modules, dependency_packages, project_languages,
  symbol_aliases, symbol_renames,
  index_jobs, ...

【deferred】——不在 v1，见 04 §4.3 推迟清单
  branches, inheritance, dependencies, events, language_projects,
  type_relations, local_variables, lsp_servers, ...
```

- **禁止**把 extension / deferred 的名字与 v1 core 混列。
- 原 `【v1 core】` 中的 22 张即当前 DDL 的实际内容（证据 1）。
- `branches` / `events` / `language_projects` 归入 `【deferred】` 时
  必须标注"已删除"或"被 X 取代"，而不是与真正的未来表并列。

### 4.3 DDL 越位修复（D2、D5）

- 把 `04` L545–558 的 `index_jobs` DDL **迁移**到 `03`/`supplement/*`（extension schema 事实源）。
- `04` 中该位置改为引用：`index_jobs` 的定义见 extension schema；`04` 只保留其**用途与验收指标**。
- 迁移后 `grep -n "CREATE TABLE" 04_completeness_review_and_optimized_plan.md` **必须为空**（D5）。

### 4.4 机械不变量检查（D4）

新增 `scripts/check_knowledge_doc_invariants.go`（或等价测试），检查五条：

| # | 不变量 | 检查方式 |
|---|---|---|
| I1 | `02` §8 总览的 `【v1 core】` 名单 == `02` 的 `CREATE TABLE` 名单（含虚拟表） | 解析围栏块 + 提取表名，集合相等 |
| I2 | DDL 只出现在 `02` 与 `03`/`supplement/*` | 在 `04`、`01`、`README.md`、`adr/*` 上 grep `CREATE TABLE`，必须为空 |
| I3 | 无命名漂移 | 全库表名集合中不存在 `{X, X_edges}` / `{X, X_versions}` 这类同概念双名（维护一份 allowlist 显式豁免） |
| I4 | 总览中每个名字属于且仅属于一个分组 | 解析三分组，检查交集为空、并集等于总览全部名字 |
| I5 | 表数声明自洽 | `04` §4.3 声明的 v1 上限与 `02` 的 v1 core 实际数量一致 |

- I5 的作用：**本 ADR 不预设该不一致是否存在**。当前已知"`04` §4.3 声明 ≤16"与
  "`02` 有 22 张 DDL"这两个数字**疑为不自洽**，但需核对 `04` §4.3 的原文口径
  （可能其"≤16"另有所指）。**由 I5 裁决，不由本 ADR 臆断。**
- 脚本失败即阻断（CI 或 `go test`）。

### 4.5 与 `04` 附录 B 的关系

- `04` 附录 B 的 18 条是**散文列表**，`adr/README.md` §8 已把它标记为待转换。
- 本 ADR 只处理其中属于"文档不变量"类的条目（B6 及同类）；
  其余条目仍按 `adr/README.md` §8 的流程逐条判定。
- **不在本 ADR 内一次性裁决全部 18 条**——那会让本 ADR 变成第二个散文列表，
  正是 `adr/README.md` §1 要消除的反模式。

---

## 5. Rationale

逐条回应 Decision Drivers：

- **D1/D6**：选项 A 被否决的**真实缺陷**是它会**为对齐而造表**。
  "总览与 DDL 一致"是好目标，但达成方式必须是"删掉不存在的名字"，
  而不是"为不存在的名字建表"。后者的代价是 v1 表数膨胀、迁移面扩大，
  而收益只是一个文档数字变好看。**这是典型的目标错位。**
- **D2/D5**：选项 F 被否决的**真实缺陷**是 `04` L546 就是活证据——
  `adr/README.md` §7 早就声明了 DDL 落点规则，但 `04` 依然承载了 DDL。
  **规则没有检查就是建议。** 因此 D4 的脚本不是可选项。
- **D3**：命名漂移（`inheritance` vs `inheritance_edges`）是最隐蔽的一类，
  因为它看起来"两边都有"，实际是两个概念各写一半。I3 用 allowlist 显式豁免已知合理对
  （如 `symbols` / `symbol_versions`），其余命中即失败。
- **D4**：I1–I5 全部机械可检验。这是 `adr/README.md` §3"可逆性分级"里
  `cheap` 类的典型：改文档 + 一个脚本，回退成本接近零。
- **D7**：I5 存在，但**本 ADR 不预设结论**。我只有一个二手数字（来自 `../README.md` §3 的转述），
  没有读到 `04` §4.3 的原文口径。把它变成一条会失败/通过的检查，
  比在 ADR 里写一个可能错误的断言更诚实，也更有用。

**关于选项 D（代码生成总览）**：它原则上最优（永不漂移），
但需要把 `02` 的 DDL 变成机器可读的唯一输入，且**无法表达"已推迟"这一状态**——
而"已推迟"恰恰是本次要新增的最重要信息。因此本 ADR 选择"三分组 + 检查"，
把代码生成记为后续可选演进（见 §10）。

---

## 6. Consequences

### 6.1 Positive

- 6 个幽灵名有明确去向，不再需要逐次裁决。
- `02` 总览块首次能回答"这张表在 v1 里吗"。
- DDL 越位被修复，`04` 回到"落地计划"职责。
- 不变量进 CI，`04` 附录 B 那类问题**不再复发**。
- ADR-0001 的 D6 得到落地（它显式引用了本 ADR）。

### 6.2 Negative / Accepted trade-offs

- **多一个检查脚本与其维护成本**（含一份 allowlist）。主动接受：这是 D4 的必要成本。
- **`03`/`supplement/*` 需要接收 `index_jobs` 的 DDL**，即 extension schema 内容会变化。
  主动接受：这本就是 extension 的正确落点。
- **I5 可能一开始就失败**，意味着需要先裁决 `04` §4.3 的口径。主动接受：
  一个暴露真实矛盾的失败检查，比一个掩盖它的绿色检查有价值得多。
- **三分组需要人工维护归类**。主动接受：归类是**决策**，不应自动化。

---

## 7. Reversal Plan

- 想撤销检查：删除脚本与 CI 挂钩，其余改动不受影响。
- 想恢复某个幽灵名：在三分组中重新归类 + 补 DDL（走新 ADR）。
- 想把 `index_jobs` DDL 放回 `04`：需先推翻 §4.3，属新 ADR 范畴。
- **无数据迁移**：Phase 0 未开始，`knowledge.db` 在任何机器上都不存在。

---

## 8. Validation

| 检查 | 形式 | 门槛 |
|---|---|---|
| I1 集合相等 | 运行脚本 | 必须通过 |
| I2 无越位 DDL | `grep -n "CREATE TABLE" 04_*.md` | 输出为空 |
| I3 无命名漂移 | 运行脚本（allowlist 外无命中） | 必须通过 |
| I4 分组互斥完备 | 运行脚本 | 必须通过 |
| I5 表数自洽 | 运行脚本 | 必须通过（若不通过，先裁决 `04` §4.3 口径） |
| 6 个幽灵名有去向 | 逐一在三分组中可查到归类 | 必须通过 |
| `index_jobs` DDL 已迁 | `02`/`03` 中找到，`04` 中只剩引用 | 必须通过 |
| 脚本进 CI | 故意制造一个幽灵名，CI 必须失败 | 必须通过 |
| ADR 自身不复制 DDL | `grep -n "CREATE TABLE" adr/` 仅允许出现在引用块中 | 必须通过 |

---

## 9. Alternatives Rejected (and why)

| 选项 | 否决理由（一句话） |
|---|---|
| A（给 6 个名字补 DDL） | 为对齐而造 v1 不需要的表，目标错位 |
| B（只删名字） | 不修命名漂移与 DDL 越位，也不建防复发机制 |
| D（代码生成总览） | 无法表达"已推迟"状态，而这是本次最重要的新增信息 |
| E（extension 全搬进 `02`） | 破坏 core/extension 分层，`02` 膨胀到 70+ 张 |
| F（只写规则不做脚本） | `04` L546 已证明"无检查的规则等于没有规则" |
| 在本 ADR 内一次裁决 `04` 附录 B 全部 18 条 | 会造出第二个散文列表，正是要消除的反模式 |

---

## 10. Open Follow-ups

| 项 | Gate |
|---|---|
| `04` §4.3 "v1 ≤ 16 张"的原文口径核对与裁决（由 I5 暴露） | `Phase1-start` |
| `04` 附录 B 其余条目按 `adr/README.md` §8 逐条转换 | `Phase1-start` |
| 是否把 I1–I5 演进为"从 DDL 代码生成总览" | `Phase2-start` |
| `index_jobs` 迁移后 `04` 相应段落的引用改写 | `Phase1-start` |
| I3 allowlist 的初始内容（`symbols`/`symbol_versions`、`files`/`file_versions` 等） | `Phase1-start` |
| 是否把该脚本挂到 `go test ./...` 而非独立 CI step | `Phase1-start` |
