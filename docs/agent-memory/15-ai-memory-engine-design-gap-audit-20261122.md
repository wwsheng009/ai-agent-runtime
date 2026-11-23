# AI Memory Engine 设计缺口审查（2026-11-22）

> 参考设计：[AI_Agent_Memory_Engine_Design.md](./references/AI_Agent_Memory_Engine_Design.md) v1.0<br>
> 审查对象：`backend/internal/memorystore`、`memoryruntime`、`memoryassembly`、`memorypolicy`、`memoryredact`、`contextmgr`、`toolkit/tools`、`api/runtimeapi`、CLI 与当前 workspace 的 `.aicli/memory/memory.db`<br>
> 审查方式：静态代码对照 + 现有测试执行 + dogfood 记忆库只读检查<br>
> 结论：当前实现是一个可运行的项目级 Agent Memory v2，覆盖参考设计 Phase 1 的写入主干和部分治理/上下文能力；但距离完整六子系统 Memory Engine 仍有结构性缺口，不能按“设计已完整实现”验收。

## 0. 审查口径

### 0.1 与既有文档的关系

本文件不是对 `08-completeness-review.md` 或 `14-runtime-gap-audit-and-fix-20261117.md` 的重复，也不推翻它们：

- `08` 审查的是仓库自身 Agent Memory v2 方案的闭环与实施状态；
- `14` 审查的是 Turn Digest 写路径、host 生命周期与运行时接线；
- 本文件以 `references/AI_Agent_Memory_Engine_Design.md` v1.0 为基准，审查的是**参考设计层面的功能覆盖度与架构缺口**。

因此可能出现：

- 按仓库自身 v1 范围，某项能力是“后续项”；
- 按参考设计，同一项仍是明确缺口。

本文件采用后者作为判定口径，并在需要时标注“仓库自身范围”。

### 0.2 严重性定义

| 级别 | 定义 |
|---|---|
| P0 | 阻塞参考设计 MVP 或存在正确性、数据隔离、可回滚性风险；在宣称闭环前必须解决 |
| P1 | 完整设计所必需，或显著影响检索质量、学习闭环、信任边界与运营能力 |
| P2 | 控制面一致性、可观测性、默认可配置性和实现细节问题 |

### 0.3 验证记录

以下定向测试已执行并通过：

```bash
go test ./internal/memorystore ./internal/memoryruntime ./internal/memoryassembly \
  ./internal/memorypolicy ./internal/memoryredact ./internal/config \
  ./internal/contextmgr ./internal/toolkit/tools -count=1
```

测试通过只证明当前组件边界没有回归，不等同于参考设计验收，也不等同于运行时写路径已经闭环。

## 1. 结论摘要

### 1.1 当前实现是什么

当前实现是一个**项目级、SQLite-first、异步抽取 + Consolidation + Summary + 注入**的记忆系统：

```text
canonical turn commit
  -> capture receipt / turn digest
  -> phase1_extract/v2
  -> candidate + source + output receipt
  -> phase2_consolidate
  -> active memory / summary revision
  -> stable + recall injection
  -> citation / usage
  -> admin API / tools / session mode / suppression
```

它在工程可靠性上投入较多：receipt、generation、purge fence、job lease、retry、source stale、summary gate、脱敏、suppression、export/import/reset 等都有实现。

### 1.2 当前实现不是什么

它不是参考设计中的完整 Memory Engine：

- 没有 FTS5/BM25；
- 没有 semantic/entity/temporal 召回和 RRF；
- 没有 tenant/user/agent/task scope 层级；
- 没有 Dreaming replay / analyze / represent / verify / patch 流程；
- 没有 Patch 审核、风险分级与记录级回滚；
- 没有 `memory_versions`；
- 没有可实际查询的 `valid_from/valid_until` 历史事实；
- 没有 trust level 与 external content 防注入模型；
- 没有 Core Memory 独立存储与 API；
- 没有可重复的质量评测语料与 Recall@K/MRR/成本/延迟指标。

### 1.3 MVP 验收概览

按参考设计 §19 的 10 条 MVP 验收标准：

| 结果 | 数量 | 条目 |
|---|---:|---|
| 通过 | 4 | 显式保存/跨会话召回、项目隔离、Token 预算、检查/纠正/删除 |
| 部分 | 3 | 事实更新历史、外部内容防注入、指标 |
| 未通过 | 3 | Task Completion Episode、Semantic + 精确检索、Dreaming Patch/回滚 |

### 1.4 最重要的五个结构性缺口

1. **检索层缺失**：FTS5 是参考设计 Phase 1 必须项，但当前只有 `LIKE` + 内存打分。
2. **隔离层缺失**：只有 project scope，无法满足 tenant/user/agent/task 级隔离。
3. **版本与回滚缺失**：自动 supersede 无法受控回滚，`UpdateRecord` 不保存旧内容版本。
4. **Dreaming 缺失**：当前只有 Consolidation，没有 Patch-first 的后台整理管线。
5. **运行时闭环未证明**：当前 dogfood 库有 37 条 pending digest、13 条 pending `phase1_extract/v2` job，但 `memory_entries` 为 0。

## 2. 当前实现基线

以下能力应保留，后续补齐不应推翻：

| 能力 | 当前状态 | 关键实现 |
|---|---|---|
| SQLite schema/migrations | 已实现，当前库 schema v16 | `memorystore/v2_schema.go` |
| Job queue | claim/lease/heartbeat/retry/attempt/幂等 job key | `memorystore/v2_runtime.go` |
| Turn capture receipt | canonical commit 后异步交接，有 generation/purge fence | `chat/turn_digest.go`、`memoryruntime/turn_digest.go` |
| 显式记忆 | `/memory add`、HTTP POST、`memory_add`，默认 pinned | `chat_memory_command.go`、`memory_tools.go` |
| 自动抽取 | LLM 输出结构化 candidate，要求 source message IDs | `memoryruntime/extractor.go`、`runtime.go` |
| Consolidation | candidate 晋升、冲突处理、supersede、summary 重建 | `memoryruntime/consolidate.go` |
| Summary revision | input watermark、summary items、gate、activate | `memorystore/v2_summary.go`、`v2_governance.go` |
| 注入 | stable/recall 分段、pinned 预算、硬 Token 预算 | `memorystore/v2_store.go`、`contextmgr/project_memory.go` |
| Usage/citation | injected_only/cited/read | `memorystore/v2_usage.go`、`v2_citation.go` |
| 删除与抑制 | tombstone + suppression + summary invalidation | `memorystore/v2_helpers.go`、`v2_session_purge.go` |
| 脱敏与权限 | secret detection、private 过滤、admin auth | `memoryredact`、`memory_handlers.go` |
| 管理 API | records、why-not、jobs、summaries、usage、metrics、audit、export/import/reset | `api/runtimeapi/handler.go` |
| Project 隔离 | project_key canonicalization + 查询硬过滤 | `memorystore/projectkey.go`、`v2_store.go` |

## 3. 设计对照矩阵

| 设计域 | 参考设计 | 当前实现 | 判定 |
|---|---|---|---|
| SQLite 主存 | §9 | SQLite schema/migrations/WAL/事务已实现 | 部分完成 |
| FTS5/BM25 | §6/§9 必须 | 无 FTS 虚拟表；`LIKE` + 内存评分 | 缺失 |
| 原始事件 | §5 不可变事件 | canonical transcript/receipt 在 chat store；memory 侧只有 digest/source 引用 | 部分 |
| 显式写入 | §5/§10 | `/memory add`、API、`memory_add` | 已实现 |
| 自动抽取 | §5 | turn digest → LLM extraction → candidate | 部分 |
| Salience scoring | §5 规则优先 + 评分公式 | 无规则过滤器、无 importance/durability/reusability/utility/explicitness | 缺失 |
| Memory Types | §4 | preference/decision/fact/workflow/pitfall/reference/open_question/correction | 部分映射 |
| Storage Tiers | §4 | L2 完整；L0 外置；L1 用 summary+pinned 近似；L3 在 canonical store | 部分 |
| Keyword retrieval | §6 | 子串匹配、CJK bigram、recency/usage 评分 | 部分 |
| Semantic retrieval | §6 | 无 embedding/vector | 缺失 |
| Entity retrieval | §6 | 无 entity/alias/relation 表 | 缺失 |
| Temporal retrieval | §6/§12 | schema 有字段，生产代码基本不写入；无历史查询 | 缺失 |
| RRF/Rerank | §6 | 无多路融合、无 reranker | 缺失 |
| Scope hierarchy | §6 | 只有 project，配置拒绝 user scope | 部分 |
| Context Builder | §7 | stable/recall 分段、预算、summary gate、citation | 部分完成 |
| Core Memory | §4/§7 | 无 `memory_core` 表；无 `memory_core_get` | 缺失 |
| Dreaming | §8 | 只有周期性 Consolidation，无 Dreaming 管线 | 缺失 |
| Patch-first | §8 | 无 patch 表、risk/status/review | 缺失 |
| Version/Rollback | §12 | revision/event；无 `memory_versions`；记录级无回滚 | 部分 |
| Lifecycle/Temporal | §12 | candidate/active/superseded/deleted/expired；无 archived/invalidated；历史事实现状不可查 | 部分 |
| Job system | §11 | lease/retry/幂等/恢复；dead-letter 未实际使用 | 部分 |
| Memory API/Tools | §10 | 有 search/read/recent/add/correct/forget/stats/why/why_not | 部分 |
| Project Memory | §13 | project-scoped generic memory + knowledge 层分离 | 部分 |
| Security/Governance | §14 | 脱敏/private/admin/suppression/audit/export | 部分 |
| Trust model | §14 | 无 trust level，外部内容无 trust 标记 | 缺失 |
| Evaluation/Metrics | §15 | 有组件/E2E 测试与计数；无质量语料与 Recall@K/MRR/成本/延迟 | 部分 |

## 4. 缺口清单

### 4.1 P0 缺口

#### GAP-P0-01：FTS5/BM25 缺失，关键词检索退化为内存子串匹配

**证据**

- schema 中没有 `CREATE VIRTUAL TABLE ... USING fts5`，也没有 FTS 同步 trigger：
  - `backend/internal/memorystore/v2_schema.go:18-230`
- 工具搜索使用 `ListRecords`，实际 SQL 为 `LOWER(text) LIKE ? OR LOWER(tags_json) LIKE ?`：
  - `backend/internal/memorystore/v2_helpers.go:651-670`
- CLI `/memory search` 走 `Store.Search`，v2 路径又只查 `source_kind='manual'`：
  - `backend/cmd/aicli/commands/chat_memory_command.go:89-108`
  - `backend/internal/memorystore/store.go:302-313`
  - `backend/internal/memorystore/v2_store.go:409-421`、`449-485`
- 注入候选通过 `loadInjectableRecords` 读取全部 active 记录后在内存排序，没有 SQL 层 top-K：
  - `backend/internal/memorystore/v2_store.go:831-868`

**影响**

- 错误码、路径、函数名、版本号等精确标识符没有 FTS/BM25 支持。
- 自动抽取记忆在 CLI 的 list/search 中不可见，用户无法通过统一入口检查和纠正。
- 数据量增长后每次注入都全表加载，延迟和内存占用不可控。
- 参考设计 MVP 第 3 条“Semantic + 精确技术标识符检索”不通过。

**关闭标准**

1. 新增 FTS5 表，覆盖 title/content/summary/tags，并建立迁移与回填。
2. FTS 与主表在同一事务或可靠 outbox 中同步，删除/纠错/抑制必须同步删除或失效索引。
3. `memory_search`、HTTP records search、CLI list/search 使用同一检索语义，不再区分 manual-only 与 auto-extracted。
4. 增加精确标识符、CJK、错误码、路径、函数名、版本号的检索测试。
5. 注入路径改为 SQL 层候选裁剪或 FTS top-K + 预算选择，不再无条件全量加载。

#### GAP-P0-02：Scope 只有 project，缺少 tenant/user/agent/task 隔离

**证据**

- `Scope` 类型虽然声明了 `project/user/profile`，但配置校验只接受 project：
  - `backend/internal/config/manager.go:1728-1732`
- 常规查询硬编码 `ScopeProject`：
  - `backend/internal/memorystore/v2_store.go:720`、`782`、`845-851`、`934-948`
- `memory_entries` 没有 `tenant_id`、`owner_id`、`agent_id`、`scope_type/scope_id` 字段：
  - `backend/internal/memorystore/v2_schema.go:18-50`
- 同一 workspace 下不同用户共享同一 memory DB/project_key；UserID 只用于会话列表过滤：
  - `backend/internal/memoryruntime/chat_source.go:198-199`
- runtime-server 只创建一个基于 `runtimeConfig.Workspace.Root` 的 memory runtime，其他 workspace 的 session 可能因 workspace mismatch 被跳过：
  - `backend/internal/api/runtimeapi/memory_runtime_wiring.go:159-175`
  - `backend/internal/memoryruntime/turn_digest_sink.go:70-76`

**影响**

- 同一项目内多用户、多 Agent 可能互相污染记忆。
- 子 Agent 默认被排除，但开启 `includeSubagents` 后没有 agent_id 做内容隔离。
- Task/Session 级工作记忆无法进入 memory engine。
- 参考设计 §6 的 scope hierarchy 与 §14 的 tenant/user/project isolation 无法满足。
- MVP 第 4 条只对“单 project”通过，不是完整 scope 隔离。

**关闭标准**

1. schema 增加 tenant/owner/agent 与 `scope_type/scope_id`，或明确用 project key + owner/agent 的复合键实现等价隔离。
2. 所有读写、检索、summary、usage、audit、job、purge 查询统一带上 scope/owner 条件，隔离失败 fail closed。
3. 实现 project/user/agent/session/task 至少五级的检索优先级；跨 scope 必须显式授权或明确关联。
4. runtime-server 按 workspace 建立或路由 memory runtime，禁止用一个 workspace 的 runtime 处理其他 workspace 的 session。
5. 增加跨项目、跨用户、跨 Agent、跨 Task 的隔离测试与负向测试。

#### GAP-P0-03：版本与回滚不完整，自动 supersede 无法受控撤销

**证据**

- 没有 `memory_versions` 表；当前 schema 只有 `memory_entries.revision` 与 `memory_events`：
  - `backend/internal/memorystore/v2_schema.go:18-230`
- `UpdateRecord` 直接覆盖文本、tags、pinned、status，旧内容不落版本：
  - `backend/internal/memorystore/v2_helpers.go:864-947`
- `CorrectRecord` 会保留旧记录为 superseded，这对纠正是好的，但不是通用版本表：
  - `backend/internal/memorystore/v2_corrections.go:14-85`
- `SupersedeRecord` 后没有 API 可以恢复被自动 supersede 的记录；只有 summary revision 可 `ActivateSummary` 回滚：
  - `backend/internal/memorystore/v2_governance.go:131-181`

**影响**

- 设计 §8/§12 要求的“派生 Memory 可更新且保留版本、可回滚”没有完全满足。
- Consolidation 误 supersede 后只能改 DB，不能走审计化回滚。
- 事实更新虽然有 correction 路径，但通用更新和自动冲突处理缺少历史快照。
- MVP 第 5、7 条只能部分通过或未通过。

**关闭标准**

1. 新增 append-only `memory_versions`，保存 memory_id、version、content、summary、changed_by、change_reason、change_type、created_at。
2. create/update/correct/supersede/delete/restore 全部写版本与 audit event。
3. 提供记录级 rollback/restore-to-version API，至少允许回滚自动 Consolidation 产生的 supersede。
4. 自动 supersede 必须可追溯、可撤销，高风险事实反转进入审核队列。
5. 增加版本历史、回滚、并发冲突、自动 supersede 撤销测试。

#### GAP-P0-04：运行时写路径闭环未被当前 dogfood 数据证明

**证据**

审查时读取 workspace 的 `.aicli/memory/memory.db`（schema v16）；下列为 2026-11-22 审查快照，不是实时计数：

| 表 | 审查快照值 |
|---|---:|
| `memory_entries` | 0 |
| `memory_sources` | 0 |
| `memory_events` | 0 |
| `memory_summaries` | 0 |
| `memory_usage` | 0 |
| `memory_turn_digests` | 37，全部 pending |
| `memory_jobs` | 13 条 `phase1_extract/v2` pending；7 条 failed；10 条 legacy phase1 failed |
| `memory_turn_output_receipts` | 0 |

代码侧存在一个可解释的长会话积压路径：在 `trigger=session_end` 下，维护 ticker 只执行 capture materialization；`capture_extraction` 仅在 `idleEnabled` 时周期运行。startup recovery 又只处理 terminal session：

- `backend/internal/memoryassembly/maintenance.go:250-320`
- `backend/internal/memoryruntime/turn_digest.go:947-951`

**影响**

- 审查快照中没有可注入的 active 记忆，但存在大量 pending digest/job。
- 现有 E2E 通过不能代表当前 dogfood 库已闭环。
- 长生命周期 active session 可能长期不抽取，直到会话关闭。
- Phase 2 “成功”可能只是无 candidate 输入的空成功。

**关闭标准**

1. 对审查快照中的 37 条 digest、13 条 pending job 给出处理结果：完成、跳过、dead-letter 或明确失败原因。
2. 明确 active session 的抽取策略：`session_end` 之外必须有 event threshold / idle / task completion 消费路径。
3. `/memory status` 与 `/api/runtime/memory/status` 展示 backlog、attempt、last error、workspace、runtime 实际状态。
4. 新增真实多会话 E2E：digest → candidate → active → summary → 新会话注入；同时覆盖 active session 与崩溃恢复。
5. 明确禁止用 `phase2_consolidate succeeded` 作为写路径健康证据。

### 4.2 P1 缺口

#### GAP-P1-01：Dreaming 子系统缺失，Consolidation 不能替代 Patch-first

**证据**

- 当前 job 类型只有 `phase1_extract`、`phase1_extract/v2`、`phase2_consolidate`、`summary_rebuild`、turn window rollup：
  - `backend/internal/memoryruntime/consolidate.go:82`、`250`
  - `backend/internal/memoryruntime/runtime.go:237`
  - `backend/internal/memoryruntime/turn_digest.go:344`
- 全仓 memory 代码未发现 Dreaming/Dream 实现。
- Consolidation 会自动 `PromoteCandidate`，并按优先级自动 `SupersedeRecord`：
  - `backend/internal/memoryruntime/consolidate.go:115-179`
  - `708-759`
  - `843-860`

**影响**

- 参考设计 §8 的 Replay/Analyze/Consolidate/Represent/Validate/Commit 流程缺失。
- 没有 Patch 表、risk/status/review、人工审核、回滚。
- 高风险事实反转、删除重要记忆、跨 scope 移动没有审核门禁。
- 无法做跨 Session 的反思、知识缺口分析、Wiki/Entity Graph 更新。

**关闭标准**

1. 新增 Dreaming job pipeline 与 `memory_patches`/等价 patch 模型。
2. 低风险新增/重复合并/索引修复可自动批准；高风险冲突、删除、跨 scope 必须审核。
3. Patch 必须带 operation、target IDs、proposed content、reason、evidence、risk、status。
4. 提交保存旧版本与 audit，可回滚。
5. 增加 Dreaming merge、冲突反转、来源保留、审核拒绝、回滚测试。

#### GAP-P1-02：Memory Types 只覆盖项目事实，缺 Episode/Working/Reflective

**证据**

- 当前允许 kind：`preference/decision/fact/workflow/pitfall/reference/open_question/correction`：
  - `backend/internal/memoryruntime/runtime.go:625-703`
- 没有 `episodic`、`working`、`reflective`、`project` 独立结构。
- 没有 Task Completion 触发；触发只有 idle/session_end/startup/manual/both：
  - `backend/internal/memoryruntime/types.go:16-24`

**影响**

- 无法生成参考设计 §5/§19 要求的 grounded Episode。
- 无法保留“问题、行动、结果、失败原因、时间”。
- 无法做 Reflection、failure learning、procedural success rate。
- 跨会话连续性主要依赖 facts + summary，不是经历层 + 反思层。

**关闭标准**

1. 增加 Episode 类型与 Task Completion 触发，Episode 必须包含 source event IDs、问题、行动、结果、失败原因、时间。
2. 增加 working 状态或明确与 contextmgr task state 的边界。
3. 增加 reflective 类型与从多个 Episode 归纳规律的受控流程。
4. 增加 negative learning：失败方案保留为 warning，不晋升为有效 procedure。
5. 增加 Task Completion Episode 的 E2E 与来源完整性测试。

#### GAP-P1-03：Temporal 字段存在但基本没有生产写入，历史事实查询缺失

**证据**

- schema 有 `valid_from/valid_until`：
  - `backend/internal/memorystore/v2_schema.go:42-43`
- 生产代码基本只读取 `valid_until` 判断过期，没有写入有效区间：
  - `backend/internal/memorystore/v2_store.go:750`、`848`
- 没有 `as_of`、current/historical 查询 API。
- 冲突检测主要是完全归一化文本 + 少量 scalar assignment 正则：
  - `backend/internal/memoryruntime/consolidate.go:761-824`

**影响**

- 设计 §12 的“当前事实 vs 历史事实”无法回答。
- 新旧事实可能同时 active，或旧事实被错误 supersede；语义矛盾的改述无法识别。
- MVP 第 5 条只能部分通过。

**关闭标准**

1. 抽取/纠正/冲突提交时写入 valid_from；替代时写旧记录 valid_until。
2. 检索支持 current（默认）与 as_of/historical。
3. 增加实体/属性级冲突检测，至少覆盖同一 subject/predicate 的语义矛盾。
4. 增加新旧事实、改述矛盾、历史查询、时间范围测试。

#### GAP-P1-04：混合检索完全缺失

**证据**

- 无 embedding/vector/entity/relation/topic 表。
- 无 RRF、reranker、query understanding。
- 当前只有 project-scope active 记录 + LIKE/内存 token score + recency/usage/confidence：
  - `backend/internal/memorystore/v2_helpers.go:98-162`
  - `backend/internal/memorystore/v2_store.go:499-702`

**影响**

- 参考设计 §6 的四路召回无法实现。
- 语义相关经验、实体别名、项目名、时间条件都无法可靠召回。
- 仅靠关键词硬门槛，改述查询会召回失败。

**关闭标准**

1. 先建立 FTS/BM25 与评测，再引入 vector；vector 保持 optional/replaceable。
2. 增加 entity/alias/relation 表与实体检索。
3. 增加 query understanding 与 scope/type/time filter。
4. 增加 RRF 融合与可选 rerank。
5. 增加 Recall@K/MRR/延迟评测。

#### GAP-P1-05：Trust 模型与外部内容防注入不足

**证据**

- 注入提示明确“historical data, not instructions”，抽取 prompt 也说 transcript 是 data。
- 但 schema 只有 `sensitivity=internal/private`，没有 trust level/source trust。
- 没有 external content 标记、poison test、跨 scope 权限模型。

**影响**

- 外部网页、工具输出、仓库内容可能被抽成 fact/workflow 并注入。
- 无法区分 system policy、user explicit、user-confirmed、agent inference、untrusted external。
- MVP 第 8 条只能算提示层防护，不能算可验证信任边界。

**关闭标准**

1. 增加 trust level 与 source trust 字段，抽取时携带来源类型。
2. 外部内容默认 untrusted，不得直接晋升为 policy/procedure。
3. 高风险内容进入审核或只作为 reference。
4. 增加 poisoned external content、prompt injection、跨 scope 泄漏测试。

#### GAP-P1-06：Core Memory 缺失，预算远低于设计

**证据**

- 无 `memory_core` 表，无 `memory_core_get`。
- summary + pinned 只是近似 L1。
- 默认 `memory.injection.tokenBudget=600`，compact/extended 为 400/900：
  - `backend/internal/config/manager.go:1222-1302`
  - `backend/internal/contextmgr/manager.go:240-273`
- 没有 core/task/procedure/reference 分类预算。

**影响**

- 参考设计 §7 的 5k–12k 总记忆预算与分类预算无法落地。
- Core Memory 不能保持“小而稳定”的独立内容与生命周期。
- Coding Agent 无法提高 Project Memory 权重并降低无关全局偏好。

**关闭标准**

1. 新增 Core Memory 表、生成/审核/预算/API。
2. Context Builder 增加 core/task/retrieved/procedure/reference 分类预算。
3. `memory_core_get` 工具与 `/memory core` 查询。
4. 增加 Core 预算、核心记忆注入相关性、Project 权重测试。

#### GAP-P1-07：Feedback 闭环缺失

**证据**

- 当前 usage 只有 `injected_only/cited/read`：
  - `backend/internal/memorystore/v2_usage.go:18-20`
- 没有 `memory_feedback`，没有 helpful/irrelevant/stale/incorrect。

**影响**

- 无法根据用户反馈调整 utility/ranking。
- 无法将“多次有用记忆”晋升候选。
- 无法量化 helpful-memory usage 与 task outcome。

**关闭标准**

1. 增加 `memory_feedback` 工具/API 与 usage kind。
2. feedback 进入 ranking/utility，但不允许 popularity 单独晋升错误事实。
3. 增加 helpful/irrelevant/stale/incorrect 的排序与审计测试。

#### GAP-P1-08：Evaluation 与 Metrics 不足

**证据**

- 当前 metrics 只有记录/status/freshness/kind、job、usage、recall token、summary revision：
  - `backend/internal/memorystore/v2_metrics.go:12-27`
- 缺 extraction precision/recall、Recall@K、MRR、conflict/stale rate、helpful usage、task success delta、repeat exploration、token savings、write/Dreaming cost、retrieval latency。
- memory 包没有可重复质量语料或 poisoned/scope/temporal 专项评测。

**影响**

- 无法证明系统比“每次重新探索”更好。
- 无法做 M5 shadow/上线决策。
- 参数调优只能凭感觉。

**关闭标准**

1. 建立固定 corpus 与 expected extraction/retrieval。
2. 增加 Recall@K、MRR、precision/recall、latency、cost、token savings 指标。
3. 增加 scope isolation、temporal update、negative learning、poisoned content、Dreaming merge 测试。
4. 将指标接入 `/memory/metrics` 和运营报告。
### 4.3 P2 缺口
#### GAP-P2-01：Memory API/Tool 面不完整

**证据**

当前工具：

- `memory_search`
- `memory_read`
- `memory_session_context`
- `memory_recent`
- `memory_stats`
- `memory_add`
- `memory_correct`
- `memory_forget`
- `memory_why`
- `memory_why_not`

见 `backend/internal/toolkit/tools/memory_tool_defs.go:8-97`。

缺少：

- `memory_feedback`
- `memory_pin` / `memory_unpin`
- `memory_core_get`
- `memory_dream_run`
- `memory_update`（当前仅有 correction 语义）
- `memory_search` 的 `scope`、`time_range`、`types[]`

**影响**

- Agent 不能主动完成完整记忆管理闭环。
- pin 只能通过 add/patch，feedback 完全缺失。
- 搜索参数与参考设计 §10 不一致。

**关闭标准**

- 按需补齐工具与 JSON schema；保持 scope/policy/audit。
- `memory_pin/unpin` 与 `memory_feedback` 至少进入 v1.1。
- `memory_dream_run` 在 Dreaming 落地后提供，并受管理员权限门禁。

#### GAP-P2-02：CLI list/search/status 与 v2 自动记忆不一致

**证据**

- `/memory list` 使用 `store.List`，v2 路径只列 manual：
  - `backend/cmd/aicli/commands/chat_memory_command.go:63-87`
- `/memory search` 使用 `store.Search`，v2 路径只查 manual：
  - `backend/cmd/aicli/commands/chat_memory_command.go:89-108`
  - `backend/internal/memorystore/v2_store.go:409-421`
- `/memory status total=` 也基于 manual list，不是全部 memory 统计：
  - `backend/cmd/aicli/commands/chat_memory_document.go:51-59`

**影响**

- 用户通过 CLI 看不到自动抽取记忆，无法检查/纠正。
- 与 Agent `memory_search`/HTTP records 的可见性不一致。
- 运维容易误判“没有记忆”。

**关闭标准**

- `/memory list/search` 默认查全部 active/candidate，manual 仅作为可选过滤。
- `/memory status` 使用 `Status()`/`Metrics()`，区分 total、manual、active、candidate、stale。
- 增加 CLI 与 HTTP 结果一致性测试。

#### GAP-P2-03：Job dead-letter、priority、timeout 未真正使用

**证据**

- schema 有 `dead_letter_at/dead_letter_reason`：
  - `backend/internal/memorystore/v2_schema.go:638-639`
- 生产代码未见 dead-letter 写入；失败后主要是 pending/retry 或 failed。
- priority 字段存在，但 job 创建基本使用默认 0。
- deadline_at 被设置，但未见强制 timeout 执行路径。

**影响**

- 失败任务没有可区分的 dead-letter 队列。
- 无法按重要性调度 Dreaming/Extract/Reindex。
- 超时依赖 lease，不是 job deadline。

**关闭标准**

- 明确 dead-letter 状态/迁移/重放 API。
- 为 job 类型设置默认 priority。
- 对超时任务写 dead-letter 或失败原因。
- 增加 dead-letter、priority、timeout 测试。

#### GAP-P2-04：Schema 与证据字段不完整

**证据**

当前 `memory_entries` 缺少参考设计中的：

- `category`
- `title`
- `summary`
- `canonical_id`
- `superseded_by`（旧记录侧字段）
- `archived/invalidated` 状态

`memory_sources.quote` 写入的是 memory text 本身，不是原始 evidence snippet：
- `backend/internal/memorystore/v2_store.go:367-395`

抽取的 `ignored` reasons 只在 `RunResult` 中短暂存在，没有持久化诊断：
- `backend/internal/memoryruntime/runtime.go:281-283`
- `backend/internal/memoryruntime/turn_digest.go:443-445`

**影响**

- 记忆对象表达力不足，Project/Procedure/Reference 难以结构化。
- 证据引用有 source IDs，但缺少原始证据片段。
- 无法解释“为什么这次没有抽取某条内容”。

**关闭标准**

- 按实际需要补 `category/title/summary/canonical_id` 与 archived/invalidated 状态。
- `memory_sources.quote` 保存原始 evidence snippet，memory text 单独存。
- ignored reasons 落库或进入 extraction output receipt/diagnostics。
- 增加 schema migration 与证据完整性测试。

#### GAP-P2-05：默认配置与设计示例偏离

**证据**

- 默认 `memory.mode=off`：
  - `backend/internal/config/manager.go:1222-1230`
- 默认 `memory.tools.enabled=false`：
  - `backend/internal/config/manager.go:1302`
- 默认 injection token budget 600，而非设计示例的 5000 retrieved：
  - `backend/internal/config/manager.go:1259-1265`

**影响**

- 开箱即用体验与参考设计示例不一致。
- 自动记忆默认不启用，Agent 工具默认不可用。
- 容易把“未配置”误判为“实现缺失”。

**关闭标准**

这是产品默认值选择，不一定是代码缺陷。建议：

1. 文档明确推荐配置；
2. 提供 onboarding preset；
3. 不强制改变保守默认，避免升级行为突变。

## 5. MVP 验收对照

| # | 参考设计 §19 验收项 | 结果 | 说明 |
|---|---|---|---|
| 1 | 用户显式保存并在新会话召回 | 通过 | `/memory add`、API、tool + stable/recall 注入 |
| 2 | Task Completion 生成有证据的 Episode | 未通过 | 无 Episode 类型，无 task completion trigger |
| 3 | Semantic + 精确技术标识符检索 | 未通过 | 无 semantic，无 FTS5/BM25 |
| 4 | Project scope 隔离 | 通过 | project_key + 查询硬过滤；仅 project |
| 5 | 事实更新不擦除历史，可查当前/历史 | 部分 | correction/supersede 保留旧行；无 valid_from/time query/versions |
| 6 | Memory context 遵守硬 Token 预算 | 通过 | selector 裁剪 + contextmgr 总预算 |
| 7 | Dreaming 产生可审 Patch、去重保来源、可回滚 | 未通过 | 无 Dreaming/patch；记录级无回滚 |
| 8 | 外部注入内容不能成为可信指令记忆 | 部分 | 有 prompt 边界和脱敏；无 trust model/专项测试 |
| 9 | 用户可 inspect/correct/delete，删除级联 | 通过 | API/tool 基本齐全；CLI list/search 仍只 manual |
| 10 | Metrics 暴露抽取/检索/效用/成本/延迟 | 部分 | 只有计数/usage/token，无质量/延迟/成本 |

## 6. 运行时积压证据与未决问题

### 6.1 当前 dogfood 库状态

下表重列该 workspace `.aicli/memory/memory.db` 的审查快照（schema v16），不是实时计数。本次复读时数据库已继续变化：`memory_entries=4`（均为 candidate、active=0）、pending digest=40、`phase1_extract/v2` pending=16；v2 failed=7、legacy failed=10 未变。

| 表/指标 | 审查快照值 |
|---|---:|
| `memory_entries` | 0 |
| `memory_sources` | 0 |
| `memory_events` | 0 |
| `memory_summaries` | 0 |
| `memory_usage` | 0 |
| `memory_turn_digests` | 37，全部 pending |
| `memory_turn_output_receipts` | 0 |
| `phase1_extract/v2` pending | 13 |
| `phase1_extract/v2` failed | 7 |
| legacy `phase1_extract` failed | 10 |
| `phase2_consolidate` succeeded | 11 |

### 6.2 不能据此直接断言“所有 host 都不工作”

该库可能包含历史版本遗留、不同配置、不同 host 的积压；单次只读检查不能证明所有运行时路径都失效。它足以证明：

- 当前 workspace 的 dogfood 数据没有可注入记忆；
- 审查快照存在大量未消费 digest/job；
- “组件测试 + 历史 E2E 通过”不足以宣称当前运行闭环已完成；
- 需要解释 13 条 pending job 为什么没有被 worker 消费。

### 6.3 代码侧可解释路径

在 `trigger=session_end` 下：

- maintenance 启动后执行 capture materialization；
- `capture_extraction` 只在 `idleEnabled` 时周期运行；
- startup recovery 只处理 terminal session；
- 因此 active 长会话可以持续产生 digest，但暂不抽取。

相关代码：

- `backend/internal/memoryassembly/maintenance.go:250-320`
- `backend/internal/memoryruntime/turn_digest.go:947-951`
- `backend/internal/memoryruntime/turn_digest.go:152`

### 6.4 需要先回答的问题

1. 这 13 条 pending job 来自哪些 session，session 当前状态是什么？
2. 当前配置的 `trigger`、`turnDigest`、`generation.enabled` 是什么？
3. 当前 workspace 是否运行了 memory maintenance owner？
4. 这些 job 的 `available_at` 是否已到期，为什么没有 worker claim？
5. 是否需要一次性 recovery 后清理，还是代码仍有静默跳过路径？

## 7. 补齐路线图

### Phase A：运行时闭环与可观测性

**目标**：先证明“当前实现能写、能抽、能注入”。

- 清理/恢复 37 条 digest 和 13 条 pending job；
- 明确 active session 抽取策略；
- `/memory status` 与 HTTP status 暴露 backlog/error/workspace/runtime；
- 真实多会话 E2E：digest → candidate → active → summary → new session injection；
- 禁止用 phase2 空成功证明链路健康。

**退出条件**：

- `memory_entries > 0`；
- 至少一个 pending job 成功完成；
- 新会话可注入至少一条自动抽取记忆；
- backlog 有明确状态，不是无限 pending。

### Phase B：P0 数据与检索基础

**目标**：满足参考设计 Phase 1 必须项。

- FTS5/BM25；
- `memory_versions` 与回滚；
- scope/owner/agent/task 字段与查询隔离；
- Core Memory 表与预算；
- CLI/HTTP/tool 统一检索语义。

**退出条件**：

- MVP 第 3、4、5、7 条的对应子项有专项测试；
- FTS 精确标识符测试通过；
- 跨用户/跨 Agent 负向隔离测试通过；
- 记录级 rollback 测试通过。

### Phase C：Dreaming 与 Episode

**目标**：从“静态事实摘要”升级为“经历 + 反思 + Patch 整理”。

- Task Completion Episode；
- Dreaming job pipeline；
- memory_patches、risk/review/approval；
- 自动低风险合并、高风险审核；
- 记录级回滚。

**退出条件**：

- Task Completion 生成有来源的 Episode；
- Dreaming 至少生成一个可审 Patch 并能提交/拒绝/回滚；
- 失败方案保留为 warning，不晋升为有效 procedure；
- 来源不丢失。

### Phase D：Hybrid Retrieval 与知识表示

**目标**：补四路召回与项目知识。

- entity/alias/relation；
- temporal query；
- semantic retrieval/vector（可选）；
- RRF/rerank；
- Wiki/Topic/Knowledge Gap。

**退出条件**：

- Recall@K/MRR 基准建立；
- 实体别名与精确标识符检索通过；
- 当前/历史事实查询通过；
- 低相关召回率有可观测指标。

### Phase E：Trust、评测与运营

**目标**：让 Memory 可被信任、可被度量、可上线。

- trust level/source trust；
- external content poison 测试；
- extraction precision/recall；
- task success delta、repeat exploration、token savings；
- latency/cost；
- M5 shadow 与逐项目上线。

**退出条件**：

- 外部注入内容不能成为 trusted policy；
- 评测 corpus 可重复运行；
- 指标进入 API/报告；
- shadow 结果支持继续/停止决策。

## 8. 关闭顺序与门禁

| 门禁 | 必须满足 | 证据 |
|---|---|---|
| G0 运行时闭环 | pending digest/job 可解释、可恢复；entries > 0；新会话注入 | dogfood DB + E2E |
| G1 检索基础 | FTS5 + 精确标识符 + CLI/HTTP/tool 一致 | 检索测试 |
| G2 数据隔离 | project/user/agent/task 隔离；runtime-server 多 workspace | 负向测试 |
| G3 变更安全 | versions + rollback + supersede 可撤销 | 版本/回滚测试 |
| G4 Dreaming | Episode + Patch + 审核 + 回滚 | Dreaming E2E |
| G5 质量与运营 | Recall@K/MRR/成本/延迟/task delta | 评测报告 |

不建议跳过 G0/G1 直接做 vector 或 Dreaming：否则会在不可靠的写路径和不可度量的检索上叠加复杂度。

## 9. 非目标与边界

以下不是本文件要求立即实现的内容：

- MCP memory tools 是可选，不作为 P0。
- Vector index 在参考设计中是 optional，但必须在 FTS/评测之后做。
- 不要求保留所有历史手工 notes 的旧 JSONL 行为；v2 迁移策略以 `04/05/07` 为准。
- 不把 `memory_entries` 改成全文 Transcript；原始 Archive 仍由 canonical transcript/文件存储承担。
- 不把 Dreaming 做成无约束模型直接写库；必须 Patch-first。

## 10. 关键代码索引

| 主题 | 文件 |
|---|---|
| schema/migrations | `backend/internal/memorystore/v2_schema.go` |
| 领域类型/status/scope | `backend/internal/memorystore/v2_types.go` |
| store/检索/注入选择 | `backend/internal/memorystore/v2_store.go`、`v2_helpers.go` |
| 显式写入/correction/suppression | `backend/internal/memorystore/v2_store.go`、`v2_corrections.go` |
| summary/revision/gate | `backend/internal/memorystore/v2_summary.go`、`v2_governance.go` |
| job queue/lease/recovery | `backend/internal/memorystore/v2_runtime.go` |
| turn digest/capture receipt | `backend/internal/memoryruntime/turn_digest.go`、`turn_digest_sink.go` |
| extractor/validation | `backend/internal/memoryruntime/extractor.go`、`runtime.go` |
| Consolidation/conflict | `backend/internal/memoryruntime/consolidate.go` |
| maintenance/ticker | `backend/internal/memoryassembly/maintenance.go` |
| context injection/budget | `backend/internal/contextmgr/project_memory.go`、`manager.go` |
| tools | `backend/internal/toolkit/tools/memory_tools.go`、`memory_tool_defs.go` |
| HTTP API | `backend/internal/api/runtimeapi/memory_handlers.go`、`handler.go` |
| CLI | `backend/cmd/aicli/commands/chat_memory_command.go`、`chat_memory_document.go` |
| project key | `backend/internal/memorystore/projectkey.go` |
| metrics | `backend/internal/memorystore/v2_metrics.go` |

## 11. 结论

当前 AI Memory 模块的工程质量基础较好，已有真实可用的项目级记忆写读链路和治理面；但按参考设计 v1.0 衡量，它仍处于**Phase 1 主干 + Phase 3 雏形**，不是完整 Memory Engine。

后续最正确的顺序不是继续堆“摘要/抽取”功能，而是：

```text
先证明运行时闭环
  -> FTS5 + 版本 + scope
  -> Dreaming Patch + Episode
  -> Hybrid Retrieval + Temporal/Entity
  -> Trust + Evaluation + Operations
```

在 G0–G5 门禁通过前，不应对外宣称“参考设计已实现”或“M5 已完成”。