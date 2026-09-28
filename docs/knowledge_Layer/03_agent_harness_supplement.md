# Agent Harness 代码智能感知方案补充规格（已拆分索引）

> 版本：v1.0-supplement  
> 日期：2026-09-20  
> **拆分日期：2026-09-28**（Phase 0 文档治理，`06` §9 待办 #9；执行记录见 `supplement/16_api_events_telemetry_and_rollout.md` §21）  
> 适用：`agent_harness_technical_design_spec_sqlite.md`  
> 与：`low_token_multilanguage_ai_agent_harness_design.md`

> 定位：**本文件已不再承载正文**。原 §1–§15 按本文 §21 自身的拆分建议迁入 `supplement/*`；
> 本文件保留为**拆分索引 + 历史引用映射**，供旧引用（含 ADR 的 `Related` 行）解析。
> 事实源边界：**extension schema 的唯一事实源是 [`supplement/`](supplement/) 下的各文件**
> （core schema 仍是 `02_agent_harness_technical_design_spec_sqlite.md`）；
> 决策唯一事实源是 [`adr/`](adr/README.md)；落地计划与验收门槛是 `04_completeness_review_and_optimized_plan.md`。

---

## 0. 拆分索引（原章节 → 现落点）

章节号**沿用拆分前的原始编号**（故各 `supplement/NN_*.md` 内部编号可能不连续），
以便解析拆分前写下的 `03 §N.M` 形式引用。

| 原 `03` 章节 | 内容 | 现落点 |
|---|---|---|
| §0 | 补充范围（旧清单） | 本文件 §0（已改写为索引） |
| §1 | 稳定符号 ID 与重命名追踪 | [`supplement/01_symbol_identity.md`](supplement/01_symbol_identity.md) |
| §2 | 项目、模块、构建与依赖模型 | [`supplement/02_project_model.md`](supplement/02_project_model.md) |
| §3 | 类型、继承、实现、重载、泛型 | [`supplement/03_type_system.md`](supplement/03_type_system.md) |
| §4 | 名称解析与导入解析 | [`supplement/04_name_resolution.md`](supplement/04_name_resolution.md) |
| §5 | LSP 工程化规格 | [`supplement/05_runtime_integration_project_detection_and_lsp.md`](supplement/05_runtime_integration_project_detection_and_lsp.md) **§10**（见 §0.1 的偏离说明） |
| §6 | 版本向量、缓存失效与一致性 | [`supplement/06_cache_consistency.md`](supplement/06_cache_consistency.md) |
| §7 | 安全、隐私、沙箱与审计 | [`supplement/07_security.md`](supplement/07_security.md) |
| §8 | 评估基准与黄金任务集 | [`supplement/08_evaluation.md`](supplement/08_evaluation.md) |
| §9 | 动态候选与不确定图 | [`supplement/09_dynamic_graph.md`](supplement/09_dynamic_graph.md) |
| §10 | 测试智能 | [`supplement/10_test_intelligence.md`](supplement/10_test_intelligence.md) |
| §11 | 跨语言与 IDL | [`supplement/11_cross_language.md`](supplement/11_cross_language.md) |
| §12 | 运行时证据接口 | [`supplement/12_runtime_evidence.md`](supplement/12_runtime_evidence.md) |
| §13 | FTS5 多语言检索 | [`supplement/13_fts.md`](supplement/13_fts.md) |
| §14 | Context Compiler 防注入、冲突解决与可解释性 | [`supplement/14_context_safety.md`](supplement/14_context_safety.md) |
| §15 | 变更管理边界补充 | [`supplement/15_change_management.md`](supplement/15_change_management.md) |
| §16 | 建议新增 API | [`supplement/16_api_events_telemetry_and_rollout.md`](supplement/16_api_events_telemetry_and_rollout.md) §16 |
| §17 | 事件模型补充 | 同上 §17 |
| §18 | Telemetry 补充 | 同上 §18 |
| §19 | 实施优先级 | 同上 §19 |
| §20 | 验收标准 | 同上 §20 |
| §21 | 与原文档整合建议 → **拆分执行记录** | 同上 §21 |
| 附录 A | 新增表清单（聚合） | 同上 附录 A |
| 附录 B | 新增 API 清单（聚合） | 同上 附录 B |
| 附录 C | 新增事件清单（聚合） | 同上 附录 C |
| 结论 | — | 同上 结论 |

### 0.1 与原 §21 建议的偏离（唯一一处）

原 §21 建议的 `supplement/05_lsp_engineering.md` 槽位已被**既有的**集成文档
`05_runtime_integration_project_detection_and_lsp.md` 占用（该文件先于本次拆分落地），
因此原 §5（LSP 工程化规格）**并入该文件 §10**，而不是新建 `05_lsp_engineering.md`。
LSP 规格仍是单一落点：设计与接入在 `05` §1–§9，extension schema 在 `05` §10。

### 0.2 历史引用说明

- **按行号的历史引用**（如 ADR-0007 `Related` 的 `03 … L139–1150`）指向**拆分前**的 `03`；
  本文件已不含这些行，请用上表映射到 `supplement/*`，或用 git 历史查看原坐标。
  ADR-0007 已 `Accepted`，其正文按 `adr/README.md` §2 **不修改**，故仅在此说明。
- **`03 §5.4（L674）` 系旧编号笔误**：该坐标实为拆分前的 §6.4「一致性规则」，
  现落点为 [`supplement/06_cache_consistency.md`](supplement/06_cache_consistency.md) §6.4。
- `03 §5.1 / §5.2 / §5.3` 现为 `supplement/05…md` 的 §10.1 / §10.2 / §10.3。
