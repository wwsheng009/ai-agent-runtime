# 16 — API / 事件 / Telemetry 补充、实施优先级、验收标准与整合记录

> 定位：**supplement（extension schema 事实源）**，不是第 5 份并列设计文档（`README.md` §7）。
> 来源：由 `03_agent_harness_supplement.md` 于 **2026-09-28 拆分**迁入（`06` §9 待办 #9 / `03` §21 自身的拆分建议）。
> **章节号沿用 `03` 原始编号**（因此本文件内编号可能不连续），用于解析拆分前的历史引用；映射见 `../03_agent_harness_supplement.md` §0。
> 事实源边界：core schema 以 `02_agent_harness_technical_design_spec_sqlite.md` 为准；extension schema 以本目录 `supplement/*` 为准；决策以 `../adr/` 为准；落地计划与验收门槛以 `../04_completeness_review_and_optimized_plan.md` 为准。

---

# 16. 建议新增 API

当前 `code.*` 不够完整。建议补：

```text
code.definition
code.references
code.callers
code.callees
code.implementations
code.types
code.tests
code.history
code.owners
code.status
code.reindex
code.validate
```

统一返回结构：

```json
{
  "source": "LSP|Tree-sitter|FTS|Runtime",
  "confidence": 0.96,
  "version": "executor.go@def456",
  "range": {
    "start_line": 10,
    "start_column": 2,
    "end_line": 20,
    "end_column": 3
  },
  "truncated": false,
  "next_cursor": null,
  "explanation": "matched by LSP definition"
}
```

---

# 17. 事件模型补充

新增事件：

```text
project.discovered
module.discovered
build_config.changed
dependency.changed

symbol.renamed
symbol.moved
symbol.deleted

type_relation.changed
implementation.changed
overload.changed

name_resolution.changed
import_binding.changed

lsp.started
lsp.stopped
lsp.crashed
lsp.diagnostics.updated

cache.dependency.changed
version_vector.changed
events_outbox.pending
events_outbox.published

security.redaction.applied
tool.permission.denied
audit.log.created

eval.run.started
eval.run.completed
eval.result.recorded

runtime.evidence.ingested
runtime.edge.updated
```

---

# 18. Telemetry 补充

新增指标：

```text
symbol_resolution_precision
symbol_resolution_recall
reference_resolution_rate
call_resolution_rate
ambiguous_resolution_ratio
lsp_fallback_ratio
lsp_error_ratio
cache_invalidation_latency
context_stale_detection_count
context_recompile_count
redaction_count
permission_denied_count
eval_precision
eval_recall
eval_f1
cross_language_link_count
runtime_evidence_coverage
```

核心 KPI 补充：

```text
Code Retrieval Precision
Code Retrieval Recall
Impact Analysis Accuracy
Trace Accuracy
Test Discovery Accuracy
Cache Invalidation Correctness
Context Reuse Correctness
```

---

# 19. 实施优先级

## P0：必须补

```text
稳定符号 ID
项目/构建模型
类型/继承/实现
名称解析
LSP 工程化
版本向量与缓存一致性
安全隐私沙箱
评估基准
```

## P1：强烈建议补

```text
动态候选与不确定图
测试智能
跨语言与 IDL
运行时证据接口
FTS5 多语言检索
Context 防注入与冲突解决
变更管理边界
```

## P2：后置预留

```text
向量检索
Git 历史智能
代码所有权
分布式 PostgreSQL + Vector DB
多 Agent 共享知识
SAP Runtime / ABAP Compiler 深度集成
```

---

# 20. 验收标准

## 20.1 功能验收

```text
1. 符号重命名后，引用和缓存可追踪。
2. 项目/模块/构建配置可发现。
3. 类型、继承、实现、重载可查询。
4. LSP 不可用时自动降级。
5. 文件变更后缓存正确失效。
6. 敏感信息不会进入 LLM Context。
7. 评估基准可重复运行。
```

## 20.2 指标验收

```text
重复探索 Token < 20%
Context Cache Hit > 50%
常见 Symbol 查询 < 100ms
Light Index < 300ms / file
Context Compiler 缓存命中 < 200ms
符号解析精度 > 90%
引用解析召回 > 85%
```

具体阈值应根据项目规模、语言和硬件调整。

---

# 21. 拆分执行记录（原"与原文档整合建议"）

> 本节原为"建议将本补充文件拆分为 `supplement/01_symbol_identity.md` … `15_change_management.md`"。
> **该拆分已于 2026-09-28 执行完毕**（`06` §9 待办 #9），故本节改为执行记录。
> 执行前的原文见 git 历史（`03_agent_harness_supplement.md` 的 §21）。

已落盘的拆分（章节号沿用 `03` 原始编号；完整映射表见 `../03_agent_harness_supplement.md` §0）：

```text
supplement/
  01_symbol_identity.md
  02_project_model.md
  03_type_system.md
  04_name_resolution.md
  05_runtime_integration_project_detection_and_lsp.md   ← 既有文件，03 §5 并入其 §10
  06_cache_consistency.md
  07_security.md
  08_evaluation.md
  09_dynamic_graph.md
  10_test_intelligence.md
  11_cross_language.md
  12_runtime_evidence.md
  13_fts.md
  14_context_safety.md
  15_change_management.md
  16_api_events_telemetry_and_rollout.md                 ← 原 §16–§21、附录 A/B/C、结论
```

**与原建议的唯一偏离**：原建议的 `05_lsp_engineering.md` 槽位已被既有的集成文档
`05_runtime_integration_project_detection_and_lsp.md` 占用（该文件先于本次拆分落地），
因此 `03` §5（LSP 工程化规格）并入该文件 §10，而不是新建 `05_lsp_engineering.md`。
LSP 规格因此仍是"单一落点"：设计与接入在 `05` §1–§9，extension schema 在 `05` §10。

**`03_agent_harness_supplement.md` 保留为**：拆分索引 + 历史引用映射（不再承载正文）。

---

# 附录 A：新增表清单

> 本附录是**聚合清单**（拆分前一次性列出全部新增表）；每张表的 DDL 与语义以对应
> `supplement/NN_*.md` 为准，本附录不构成第二处定义。

```text
symbol_aliases
symbol_renames
projects
modules
packages
build_configs
dependency_versions
generated_sources
ignore_rules
type_relations
inheritance_edges
implementation_edges
overloads
generic_params
parameters
local_variables
annotations
import_bindings
name_resolutions
scope_bindings
lsp_servers
lsp_documents
lsp_diagnostics
version_vectors
cache_dependencies
events_outbox
consumer_offsets
security_redactions
tool_permissions
audit_log
eval_projects
eval_tasks
eval_golden_symbols
eval_runs
eval_results
call_candidates
reference_candidates
tests
test_symbols
test_runs
coverage_evidence
cross_language_links
idl_contracts
api_endpoints
rpc_methods
message_topics
db_schema_links
runtime_evidence
runtime_edges
runtime_symbol_stats
change_events
git_sync_state
```

---

# 附录 B：新增 API 清单

```text
code.definition
code.references
code.callers
code.callees
code.implementations
code.types
code.tests
code.history
code.owners
code.status
code.reindex
code.validate
```

---

# 附录 C：新增事件清单

```text
project.discovered
module.discovered
build_config.changed
dependency.changed
symbol.renamed
symbol.moved
symbol.deleted
type_relation.changed
implementation.changed
overload.changed
name_resolution.changed
import_binding.changed
lsp.started
lsp.stopped
lsp.crashed
lsp.diagnostics.updated
cache.dependency.changed
version_vector.changed
events_outbox.pending
events_outbox.published
security.redaction.applied
tool.permission.denied
audit.log.created
eval.run.started
eval.run.completed
eval.result.recorded
runtime.evidence.ingested
runtime.edge.updated
```

---

# 结论

原方案的三层架构、UCM、增量索引、Exploration Graph、Context Compiler 方向正确。  
本补充文件主要补齐：

```text
稳定身份
项目模型
类型系统
名称解析
LSP 工程化
缓存一致性
安全隐私
评估基准
动态图
测试智能
跨语言
运行时证据
FTS 多语言
Context 安全
变更边界
```

补完这些后，Code Knowledge Runtime 才能从“能设计”进入“能实现、能验证、能演进”。
