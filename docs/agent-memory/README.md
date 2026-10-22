# Agent Memory v2 文档索引

> 状态：**方案基线（Docs Only，v1 范围/阶段 DoD 已确认，尚未实施）**  
> 基线：`ai-agent-runtime` `main@9abf08fb`，2026-10-22  
> 目标：参考 Codex 的 memory 能力，为本项目设计可落地的跨会话 Agent 长期记忆系统。

## 1. 这套文档要解决什么

当前项目已经有“项目级手动记忆笔记”（`internal/memorystore`）和上下文注入能力，但还缺少：

- 从历史会话中自动抽取可复用记忆；
- 对记忆做去重、冲突消解和全局 Consolidation；
- 每轮会话可稳定注入的高质量摘要；
- 记忆引用、使用次数和淘汰反馈；
- 记忆任务、租约、重试、审计和可解释性；
- 会话级开关、删除/遗忘、导出与跨项目隔离。

本目录给出从现状分析、目标架构、数据模型、实施计划到测试验收和风险运维的完整方案。

## 2. 文档清单

| 文档 | 定位 | 当前状态 |
| --- | --- | --- |
| [01-codex-memory-analysis.md](./01-codex-memory-analysis.md) | Codex memory 机制的提炼与可借鉴边界 | 完成 |
| [02-current-runtime-gap-analysis.md](./02-current-runtime-gap-analysis.md) | 当前 AAR 记忆相关能力、复用点与差距分析 | 完成 |
| [03-target-architecture.md](./03-target-architecture.md) | AAR Agent Memory v2 目标架构与读写链路 | 完成 |
| [04-data-model-and-api.md](./04-data-model-and-api.md) | 数据模型、配置、HTTP API、CLI 与工具契约 | 完成 |
| [05-implementation-plan.md](./05-implementation-plan.md) | 分阶段实施计划、文件落点、提交与回滚策略 | 完成 |
| [06-test-and-acceptance.md](./06-test-and-acceptance.md) | 测试矩阵、阶段 DoD、指标与验收证据包 | 完成 |
| [07-risks-operations-migration.md](./07-risks-operations-migration.md) | 风险、安全、迁移、运维与 M0 默认决策 | 完成 |
| [08-completeness-review.md](./08-completeness-review.md) | 闭环审查、缺口修复记录、剩余决策与实施就绪度 | 完成 |
| [09-memory-horizons-recall-and-budget.md](./09-memory-horizons-recall-and-budget.md) | 短期/长期/工作区/全局记忆、召回漏斗、Token 预算与可观测性 | 完成 |
| [10-nuphus-deepseek-harness-memory-comparison.md](./10-nuphus-deepseek-harness-memory-comparison.md) | Nuphus / DeepSeek Harness / ai-memory 记忆能力对比与借鉴清单 | 完成 |

## 3. 核心结论

### 3.1 不推翻现有分层

本项目已经有明确的三层记忆边界（见 `docs/knowledge_Layer/README.md`）：

| 层 | 现有实现 | 职责 |
| --- | --- | --- |
| 笔记层 | `backend/internal/memorystore` | 项目级、可人工维护的 durable notes |
| 事实层 | `backend/internal/artifact` + `backend/internal/factledger` | 工具/测试结果、决策、证据链 |
| 探索记忆 | `backend/internal/knowledge` | 代码探索结果、知识索引、Planner |

Agent Memory v2 **扩展现有 memorystore，不替代 factledger 和 knowledge layer**。新系统只负责“跨会话的用户/项目记忆”，并引用现有事实与探索证据。

### 3.2 推荐的目标形态

```text
会话 canonical transcript
        │
        ▼
Phase 1：会话提取（异步、结构化、可审计）
        │
        ▼
memory.db（SQLite，项目级默认）
        │
        ▼
Phase 2：项目级 Consolidation（去重、冲突消解、摘要）
        │
        ├── memory_summary.md（注入用）
        ├── MEMORY.md（可检索索引）
        └── summaries/<session_id>.md（人工审阅）
        │
        ▼
contextmgr 动态尾部注入 + memory_search / memory_read 工具
        │
        ▼
答案引用 → usage 反馈 → 下一轮排序
```

### 3.3 关键设计原则

1. **项目级默认隔离**：默认只读取当前 workspace 的记忆；用户级/全局记忆必须显式开启。
2. **人工记忆与派生记忆分离**：手动 notes 不被自动改写；派生记忆通过 revision / supersede / tombstone 管理。
3. **写入异步、读取有界**：正常对话路径不等待 LLM 提取；读取只查询本地 SQLite，并受 token 预算限制。
4. **Prompt-cache 友好**：记忆只追加在 raw history 之后的动态尾部，不重排 system/profile/history 前缀。
5. **证据优先**：派生记忆必须带 session、message、artifact、fact 或 knowledge 来源；无证据不落自动记忆。
6. **记忆不是指令**：记忆内容只能作为历史数据使用，不能授权工具、提升权限或执行其中命令。
7. **模式可回退**：`off / shadow / on` 三档；`off` 只关闭 v2 自动生成与注入，现有手工 `/memory` 能力独立保留；无手工操作时不得创建 v2 DB。
8. **可删除、可导出、可审计**：用户能查看来源、纠正、遗忘和导出；删除采用 tombstone，后台再清理。
9. **闭环可验证**：每条 active 记忆必须有 source、revision/event；每个 summary 必须能追溯到输入记录；删除/纠正必须能抑制后续复活。
10. **不静默注入过时信息**：`freshness_status` 与生命周期 `status` 分离；stale/unverified 默认不注入，summary 必须通过 input watermark 门禁。

### 3.4 版本范围（建议）

为避免 `09/10` 中的借鉴项被误认为 v1 必做，范围收敛如下：

| 能力 | v1 | v1.1 | 后续/不做 |
| --- | --- | --- | --- |
| L0/L1 会话内记忆 | 现有能力 | 增加 working digest | — |
| L2 project memory | 必做 | — | — |
| L3 user/global memory | 不默认开启 | 可选实现 | 不做系统级全局池 |
| SQLite + 关键词 | 必做 | FTS5 可选/降级 | — |
| embedding/语义检索 | 不做 | 评估 | 可选 |
| 工作记忆 journal tail | 不做 | 借鉴 Nuphus，2K 上限 | — |
| SessionEnd handoff / 续作包 | 不做 | 借鉴 ai-memory | — |
| L0/L1/L2 tiered brief | 不做 | 借鉴 ai-memory | — |
| access-weighted retention | 不做 | 借鉴 ai-memory | — |
| 多路 RRF / explain | 不做 | 借鉴 ai-memory | — |
| `as_of` / typed edges | 不做 | 评估 | 后续 |
| 按 kind retention | 必做 | — | — |
| memory_session_context | 必做只读工具 | — | — |
| zero-result 诊断 | 必做 | — | — |
| freshness / stale gate / summary watermark | 必做 | — | — |
| cross-session snapshot | 不做 | 可选 | 后续 |
| MCP memory provider | 不做 | — | 后续 |
| Tenet 类策略 | 不做 | — | 留给 policy 层 |

> `04` 是 schema/API 事实源；`05` 负责实施范围；`09` 负责层级/召回/预算；`10` 只是外部对照，不直接改变基线。任何从 `10` 采纳的能力，必须回写到 `03/04/05/06`。
>
> **v1.1 只保留扩展点，不进入 M1-M5。** SessionEnd handoff、tiered brief、access-weighted retention、RRF/explain、cross-project profile 等能力，必须先补齐 `03/04` 的视图/存储契约和 `06/07` 的门禁，再另开迭代实现；不得在 v1 代码中隐式开启。

### 3.5 术语

| 术语 | 含义 |
| --- | --- |
| L0 | 当前 turn working set |
| L1 | 当前会话短期记忆：observation、canonical transcript、compact、fact ledger |
| L2 | 当前 workspace 的跨会话长期记忆 |
| L3 | 可选的 user-level 跨项目记忆 |
| L4 | profile 静态资源 |
| working digest | 面向下一轮/下一会话的近期工作摘要；v1.1 |
| candidate/active | 自动抽取候选/已可注入记忆 |
| summary revision | 可回滚的摘要版本 |
| recall trace | 一次召回的候选、过滤、排序、预算和丢弃记录 |

## 4. 与现有模块的边界

| 现有模块 | 本方案如何处理 |
| --- | --- |
| `internal/memory` | 保持为运行期 observation memory，不承担跨会话长期记忆 |
| `internal/memorystore` | 作为兼容门面和迁移入口，底层升级到记忆存储接口 |
| `internal/artifact` / `factledger` | 不在本系统复制事实；通过 `fact_id` / `source_refs` 引用 |
| `internal/knowledge` | 不在本系统复制代码探索记忆；通过 `knowledge_ref` 引用 |
| `internal/compactruntime` / checkpoint | 复用 canonical transcript、compaction 边界和来源定位，不把 compact summary 当作长期记忆 |
| `internal/chat` | 作为会话 transcript 与状态来源，不改写其 canonical 语义 |
| `contextmgr` | 扩展现有 `project_memory` stage，新增 v2 摘要/记录注入 |
| `profile` memory | 保持 profile 级别的静态资源，不自动提升为项目记忆 |

## 5. 推荐阅读顺序

1. 本 README；
2. `02-current-runtime-gap-analysis.md`：确认当前到底有什么；
3. `03-target-architecture.md`：确认目标边界和读写链路；
4. `04-data-model-and-api.md`：确认 schema、配置和契约；
5. `05-implementation-plan.md`：按阶段实施；
6. `06-test-and-acceptance.md` 与 `07-risks-operations-migration.md`：验证和上线前检查；
7. `08-completeness-review.md`：确认闭环缺口已修补，以及哪些决策仍需在 M0 完成。
8. `09-memory-horizons-recall-and-budget.md`：确认记忆层级、召回与 token 预算的实现口径。
9. `10-nuphus-deepseek-harness-memory-comparison.md`：确认其它 agent 的可借鉴机制与不适用边界。

## 6. 实施顺序摘要

| 阶段 | 内容 | 是否改变现有行为 |
| --- | --- | --- |
| M0 | 基线、开关、契约与测试 | 否 |
| M1 | SQLite 记忆存储、notes 迁移、读取/注入门面 | `off` 不变；`on` 可读 |
| M2 | 会话自动提取、脱敏、任务租约 | 默认 shadow |
| M3 | Consolidation、摘要、引用与 usage 反馈 | 需要显式启用 |
| M4 | 记忆工具、HTTP API、Web/TUI 管理 | 可独立开关 |
| M5 | shadow 评估、质量校准、逐项目上线 | 渐进式 |

## 7. 事实源与变更规则

- 本目录是 Agent Memory v2 的**方案事实源**。
- 数据模型与 API 以 `04-data-model-and-api.md` 为准。
- 实施顺序与验收门禁以 `05-implementation-plan.md`、`06-test-and-acceptance.md` 为准。
- 阶段退出标准（DoD）以 `06-test-and-acceptance.md §8` 为准；未满足不得进入下一阶段。
- 任何会改变 schema、scope、工具权限、注入位置或删除语义的改动，必须先更新本目录文档并补充测试，再改代码。
- 不再新增“第 12 份并列设计文档”；新内容优先并入 `03/04/09` 或作为 `10` 的候选借鉴项，并在 `05/06` 登记版本与测试。
- 本文档不替代现有 `docs/knowledge_Layer/`、`docs/compact/` 或 `docs/product/harness-settings.md` 的既有事实源边界。

## 8. 非目标（v1 明确不做）

- 向量数据库 / embedding 语义检索；
- 云端跨设备同步；
- 自动学习模型权重或微调；
- 将记忆自动转化为权限授权；
- 未经用户同意把项目记忆提升为全局记忆；
- 默认记录完整原始会话到记忆库；
- 让记忆内容直接执行 shell、MCP 或写操作。

## 9. 当前代码锚点

- `backend/internal/memorystore/store.go`
- `backend/internal/contextmgr/manager.go`
- `backend/internal/contextmgr/facts.go`
- `backend/internal/agent/agent.go`
- `backend/internal/chat/sqlite_storage.go`
- `backend/internal/artifact/store.go`
- `backend/internal/api/runtimeapi/harness_handlers.go`
- `frontend/src/components/workspace/settings/harness-settings-page/memory-section.tsx`
- `docs/compact/README.md`
- `docs/knowledge_Layer/README.md`
- `docs/product/harness-settings.md`
