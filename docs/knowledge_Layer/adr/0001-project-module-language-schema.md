# ADR-0001: Project/Module/Language 模型收敛

- **Status**: Proposed
- **Date**: 2026-09-20
- **Deciders**: 项目 owner
- **Gate**: `Phase1-start`（阻塞 Phase 1 开工）
- **Reversibility**: expensive（schema）——**但当前迁移成本为零**，见 §1.4
- **Supersedes**: `02` 的 `language_projects`（幽灵表）
- **Related**: `02` L300–313、L320–1039；`03` L136–259、L935–1001；`supplement/05` §3；`04` §4.3

---

## 1. Context

### 1.1 已核实的证据

**证据 1 — `language_projects` 是幽灵表。**
`02_agent_harness_technical_design_spec_sqlite.md` 的表清单（L300–313）列出：


但 `02` 的 `CREATE TABLE` 区块（L320–1039）实际只定义了 **22 张表**：
`workspaces, repositories, commits, files, file_versions, symbols, symbol_versions, references, calls, imports, exploration_sessions, exploration_nodes, exploration_edges, tasks, task_files, task_symbols, context_snapshots, context_items, tool_calls, tool_results, cache_entries, invalidation_events`。

**`language_projects` / `index_jobs` / `events` 三者在该区块中均未出现**——清单与 DDL 不一致。

**证据 2 — `03.projects.language` 是标量。**


**证据 3 — 本方案自身要求多语言。**
`supplement/05` §3.3 明确：同一目录同时有 `go.mod` 与 `package.json` 时"两者都记，project 变为 multi-kind"。
§3.9 的输出契约是 `"languages": ["ts","vue"]`。§3.6 还要求 **每个语言各自带 confidence 与 evidence**。
标量 `language` 列在这三点上均不可表达。

**证据 4 — `packages` 命名与"源码包"冲突。**
`03.packages`（L170）含 `package_manager` + `version`，语义是**依赖包**（npm/go module），
但 `packages` 在 Go/Java 语境中默认指**源码包**。同时 `03.dependency_versions`（L201）已存在，
与 `packages` 在"版本"上重叠。

**证据 5 — Phase 0 未开始。**
`../README.md` §4 落地进度表：Phase 0–8 全部"未开始"。
`04` §0.1 与 §5 同样确认 Phase 0 未启动。
**结论：`knowledge.db` 在任何机器上都不存在，没有任何已部署数据。**

### 1.2 问题陈述

存在三套重叠的项目概念，且都不可直接实现：

1. `02` 的 `language_projects`（无 DDL，幽灵）
2. `02` 的 `workspaces` / `repositories`（这两个有 DDL，但与"项目"不是同一粒度）
3. `03` 的 `projects` / `modules` / `packages`（有 DDL，但 `language` 标量、`packages` 命名冲突）

### 1.3 为什么必须在 Phase 1 之前解决

Phase 1 的索引 MVP 要写 `projects` 与文件归属。schema 一旦落地并写入数据，改列名/拆表就要写迁移。
这是 `Phase1-start` gate 的来源。

### 1.4 关键杠杆：现在迁移成本为零

由于证据 5，当前**不存在需要迁移的数据**。因此：

- "保留 `projects.language` 以求向后兼容"这一理由**不成立**。
- 干净模型的唯一成本是改文档，收益是永久消除歧义。
- 决策规则：**在没有部署数据时，选语义正确的模型；不要选兼容模型。**

---

## 2. Decision Drivers

| # | 判据 | 可检验形式 |
|---|---|---|
| D1 | 必须能表达多语言 project | 同一 project 可关联 ≥2 个 language，各自带 confidence |
| D2 | 必须能表达 multi-kind project | 同一 root 可同时是 `go-module` 与 `node-package` |
| D3 | language 必须可挂证据 | 每个 language 断言可追溯到 `go.mod` / `package.json:deps.vue` 等 |
| D4 | 单一事实源 | 同一信息不得有两处可写 |
| D5 | 命名无歧义 | `packages` 不得同时指依赖包与源码包 |
| D6 | 清单与 DDL 必须一致 | 机械可检验（见 ADR-0007） |
| D7 | v1 表数受控 | 新增表 ≤ 2（`04` §4.3 要求 ≤16） |
| D8 | 可支持 `stable_key` 与重命名迁移 | `supplement/05` §3.5 要求 `H(rel_path+kind)` 有落脚点 |

---

## 3. Considered Options

| 选项 | 描述 | 优点 | 代价 |
|---|---|---|---|
| **A** | 保持 `projects.language` 标量，另加 `project_languages` 辅助表 | 改动小 | 两处可写同一信息，违反 D4；主语言判定会被查询硬编码 |
| **B** | 删除 `projects.language` / `modules.language`，语言唯一落在 `project_languages` | 单一事实源（D4）；天然支持 D1/D3 | 常用"主语言"查询需 join + `role='primary'` |
| **C** | 多语言 = 拆成多个 project | 无 schema 改动 | 语义错误：一个 `go.mod` + 一个 `package.json` 同目录是一个 project 的两面，不是两个 project。违反 D2 |
| **D** | 保留 `language` 但定义为"主语言"，并加触发器同步 | 查询快 | 触发器是第二处可写逻辑；SQLite 触发器调试差；仍违反 D4 |
| **E** | 完全推迟到 Phase 2 | 不阻塞 | Phase 1 索引必须写文件归属，无法推迟。违反 gate |

---

## 4. Decision

采纳 **选项 B**，并附带四项清理。

### 4.1 `projects` 表（修改）


**删除**：`projects.language`（被 4.2 取代）。

### 4.2 `project_languages` 表（新增）


- `role='primary'` 恰好一行（由写入方保证，并有校验查询）。
- 常用"按语言过滤 project"由 `idx_project_languages_lang` 覆盖（回应选项 B 的代价）。

### 4.3 `modules` 表（修改）


理由：module 的语言由其自身文件**可精确推导**（`go.work` 的 use 目录必为 go；pnpm package 由所含文件判定），
持久化它是冗余的第二事实源（违反 D4）。
若 Phase 4 出现"module 语言无法从文件推导"的真实案例，再新增 `module_languages` 镜像 4.2——记为 follow-up，不预先造表（D7）。

### 4.4 `packages` → `dependency_packages`（重命名）


- 理由：消除与"源码包"的命名冲突（D5）。
- 声明/解析的语义重叠（`dependency_packages.version` vs `dependency_versions.dependency_version`）
  **本次不拆**，标记为 Phase 4 follow-up。理由是 Phase 1 不需要依赖图（D7）。
- 现在只重命名，因为重命名在零数据时免费（§1.4）。

### 4.5 `ignore_rules` 的语义澄清


理由：否则 DB 内规则与运行时 `fsscope` 规则会漂移，且 `ignore_rules_hash` 失去意义。

### 4.6 `generated_sources` 推迟

v1 使用 `files.is_generated BOOLEAN` + `files.generated_from_file_id`（见 `supplement/05` §3.4）。
`03.generated_sources` 提供更丰富的 provenance（`generator` / `generator_version` / `source_relation`），
推迟到 Phase 4。理由：Phase 1 只需"不要把生成代码当真实符号"（D7）。

### 4.7 幽灵表处置

| 幽灵表 | 处置 | 理由 |
|---|---|---|
| `language_projects` | **删除** | 语义 = `project_languages` 的逆视图，冗余；从未有 DDL |
| `index_jobs` | **补 DDL**（extension schema） | Phase 1 需要调度/进度/重试，见 ADR-0007 §4.3 |
| `events` | **删除** | 被 `03.events_outbox` + `consumer_offsets` 取代（后者有 outbox 语义与 offset） |

---

## 5. Rationale

逐条回应 Decision Drivers：

- **D1/D3**：`project_languages` 的每一行可携带自己的 `confidence` 与 `evidence_json`。标量列做不到。
- **D2**：`project_type` + `secondary_types_json` 表达 multi-kind。选项 C 把 multi-kind 误建模为多 project，被否决。
- **D4**：删除 `projects.language` / `modules.language` 后，语言信息只有一处可写。选项 A/D 都留下第二处可写点。
- **D5**：重命名为 `dependency_packages`。
- **D6/D7**：见 ADR-0007 与 §4.6/§4.7 的推迟清单。
- **D8**：新增 `stable_key` 列，使 `supplement/05` §3.5 的 `H(rel_path+kind)` 有落脚点；
  同时 `UNIQUE(workspace_id, root_path)` 让"同一 root 被检测两次"成为硬错误而非静默重复。

**被否决选项的真实缺陷**（不是"不够好"）：

- 选项 A 的致命点是：`projects.language` 与 `project_languages` 都能表达"这个 project 的主语言是什么"，
  任何查询都必须选一个，而两者会漂移。这正是 `04` 附录 B 反复出现的"双事实源"病。
- 选项 C 的致命点在 §3 已述：它把"一个目录、两套构建系统"错误地拆成两个 project，
  会导致 `code.find_refs` 的 `project` 作用域（`supplement/05` §3.8）失去意义。
- 选项 D 的致命点是 SQLite 触发器不可测、不可版本化，且仍是第二处可写逻辑。
- 选项 E 与 `Phase1-start` gate 冲突。

---

## 6. Consequences

### 6.1 Positive

- 多语言/multi-kind project 有了正确模型，`supplement/05` §3.9 的输出契约可直接落库。
- 消除 3 张幽灵表；表清单与 DDL 可机械校验（ADR-0007）。
- `stable_key` 与 `UNIQUE(workspace_id, root_path)` 让重命名迁移（`04` §4.4）有落点。
- `ignore_rules_hash` 的语义变清晰。

### 6.2 Negative / Accepted trade-offs

- "取 project 主语言"从读一列变为 `WHERE role='primary'` 的 join。
  **主动接受**：由 `idx_project_languages_lang` + 单行 primary 不变量覆盖，代价可忽略。
- `projects` 表列数增加（`stable_key` / `secondary_types_json` / `confidence` / `evidence_json` / `detected_at`）。
  **主动接受**：`stable_key` 与 `confidence` 是 `04` §4.4 的硬要求；`evidence_json` 用空间换可解释性。
- `dependency_packages` 与 `dependency_versions` 的语义重叠**被保留**。
  **主动接受**：拆分会引入 ≥2 张新表，违反 D7，且 Phase 1 不需要。

---

## 7. Reversal Plan

- **改回标量**：给 `projects` 加回 `language`，写 `UPDATE projects SET language = (SELECT language FROM project_languages WHERE role='primary')`。成本低但会重新引入 D4 违反——不建议。
- **改回 `packages` 命名**：`ALTER TABLE dependency_packages RENAME TO packages`。SQLite 支持，成本低。
- **恢复 `generated_sources` 到 v1**：纯新增，无迁移。
- 本 ADR 的所有改动在 Phase 1 前都是**文档改动**；Phase 1 后才有迁移成本。这是 gate 设在 `Phase1-start` 的原因。

---

## 8. Validation

| 检查 | 形式 | 门槛 |
|---|---|---|
| 多语言可表达 | 用 Go+TS 同目录样本写库，断言 `project_languages` 有 2 行 | 必须通过 |
| primary 唯一 | `SELECT project_id FROM project_languages WHERE role='primary' GROUP BY project_id HAVING COUNT(*)>1` | 必须为空 |
| root 唯一 | `SELECT workspace_id, root_path ... HAVING COUNT(*)>1` | 必须为空 |
| stable_key 稳定 | 同一 project 重扫两次，`stable_key` 不变 | 必须通过 |
| 重命名迁移 | 目录改名后，`stable_key` 通过 go.mod module path 匹配保留 | 必须通过 |
| 幽灵表清零 | ADR-0007 的文档不变量脚本 | 必须通过 |

---

## 9. Alternatives Rejected (and why)

| 选项 | 否决理由（一句话） |
|---|---|
| A（标量 + 辅助表） | 留下第二处可写点，语言信息会漂移 |
| C（多语言=多 project） | 语义错误，破坏 `code.*` 的 project 作用域 |
| D（标量 + 触发器同步） | 触发器是第二处可写逻辑且不可测 |
| E（推迟到 Phase 2） | Phase 1 必须写文件归属，无法推迟 |
| 保留 `language_projects` | 它是 `project_languages` 的逆视图，且从未有 DDL |
| 现在拆分 `dependency_packages` / `dependency_versions` | 违反 D7，Phase 1 不需要依赖图 |

---

## 10. Open Follow-ups

| 项 | Gate |
|---|---|
| `module_languages` 是否需要（module 语言能否总是从文件推导） | `Phase4-start` |
| 依赖「声明 vs 解析」是否拆表 | `Phase4-start` |
| `generated_sources` 是否进入 v1 | `Phase4-start` |
| `04` 附录 B 对应条目的状态更新 | `Phase1-start` |