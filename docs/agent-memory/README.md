# Agent Memory v2 文档索引

> 状态：**方案基线 + M1/M2/M3/M4 core 已实施（含 Web why-not/pin/restore/reset）；M4 正式 E2E/证据包与 M5 shadow 运营待完成**
> 2026-11-11 追加：Turn Digest Phase 1 core + 窗口化读取 + per-window stale + L0→L1→L2 rollup + canonical range invalidation 已实现；真实 E2E / shadow 待补（见 `patches/`）。
> 2026-11-18 追加：统一执行核 `TurnDigestSink` 接线修复；真实 ConPTY 双会话 E2E PASS，覆盖 digest → phase1 → phase2 → 跨会话注入/召回（见 `14-runtime-gap-audit-and-fix-20261117.md`）。
> 2026-11-22 追加：按 `references/AI_Agent_Memory_Engine_Design.md` v1.0 完成参考设计层缺口审查；结论是当前实现为项目级 Memory v2，尚未覆盖 FTS5/混合检索、Dreaming Patch、版本回滚、完整 scope、Temporal/Trust/评测等（见 `15-ai-memory-engine-design-gap-audit-20261122.md`）。
> 2026-11-23 追加：G3 记录版本/回滚与 G3.5 Core 已提交（`8513f579`/`a9c7a461`/`eb413e1f`）；FTS5 索引与 driver 已落地（`d05145f6`/`8ddd48c1`），统一 SearchQuery/BM25 待补。当前状态快照见 `19-implementation-status-20261123.md`。G0-5、G1–G5 门禁仍未关闭。
> 基线：`ai-agent-runtime` `main@9abf08fb`，2026-10-22  
> 目标：参考 Codex 的 memory 能力，为本项目设计可落地的跨会话 Agent 长期记忆系统。

## 0. 2026-11-19 架构收敛更新

本目录的统一口径是：**新回合只有一条正常写入/处理链路**，不得让
receipt、digest、window 三套对象各自成为事实源：

```text
canonical chat turn commit
  -> durable capture receipt
  -> memory materialization
  -> phase1_extract/v2 job
  -> candidate records + output receipt
  -> consolidation
  -> active memory
  -> retrieval/injection
```

边界和兼容规则：

- canonical receipt 是 canonical chat turn 的 handoff，不是第二个 event source；
- memory digest 是 memory-side materialization/progress record，不是第二份 transcript；
- extraction job 是可租约的 work item，不是事实源；
- `TurnWindow` 只是派生 rollup/summary，不是新的 transcript 或事实源；
- `RunSession`、`RunSessionWindow`、`RunPendingTurnDigests` 和
  `RecoverPendingPhase1` 必须保留，但只服务于 pre-receipt/historical
  数据的 compatibility、migration 或 recovery；
- receipt-capable source 的正常入口只走 materialization/capture worker；
  maintenance 不再通过 `RunIdle` 或无条件 legacy digest sweep 产生第二条
  extraction 路径。没有 receipt contract 的 source 仅作为 legacy adapter；
- legacy digest 查询和状态更新必须限制 `capture_receipt_id = ''`，receipt-backed
  session 对 legacy consumer 采用 session-level routing fence；
- canonical SQLite 到 memory SQLite 没有跨库事务，交接是 at-least-once，
  依靠 receipt、turn/range/revision 幂等身份、generation/incarnation、
  purge fence、worker lease、stale source validation、output receipt 和
  candidate commit 收敛；
- 测试全绿只证明验收边界，不自动证明历史数据已经迁移或架构已经完全收敛。

对象职责表、数据字段和验收门禁分别以 `03`、`04`、`06` 为准；实现差距和
尚未消除的历史迁移风险以 `08` 为准。`patches/2026-11-18-turn-end-durable-capture-and-async-extraction.md`
仍是**方案提案，待评审；未实施**，不能把本文的收敛说明当作该提案的批准记录。

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
| [05-implementation-plan.md](./05-implementation-plan.md) | 分阶段实施计划、M1 代码级施工说明、文件落点、提交与回滚策略 | 完成（M1/M2/M3/M4 core 与 Web 审计面已实施；M4 证据包与 M5 运营待执行） |
| [06-test-and-acceptance.md](./06-test-and-acceptance.md) | 测试矩阵、阶段 DoD、指标与验收证据包 | 完成 |
| [07-risks-operations-migration.md](./07-risks-operations-migration.md) | 风险、安全、迁移、运维与 M0 默认决策 | 完成 |
| [08-completeness-review.md](./08-completeness-review.md) | 闭环审查、缺口修复记录、剩余决策与实施就绪度 | 完成 |
| [09-memory-horizons-recall-and-budget.md](./09-memory-horizons-recall-and-budget.md) | 短期/长期/工作区/全局记忆、召回漏斗、Token 预算与可观测性 | 完成 |
| [10-nuphus-deepseek-harness-memory-comparison.md](./10-nuphus-deepseek-harness-memory-comparison.md) | Nuphus / DeepSeek Harness / ai-memory 记忆能力对比与借鉴清单 | 完成 |
| [11-fact-ledger-convergence-decision.md](./11-fact-ledger-convergence-decision.md) | Fact Ledger 收敛决策记录：过程产物/结论切分、实测证据、停止投入理由与重启条件 | **已执行**（本目录其余文档为方案基线） |
| [12-hermes-agent-memory-system-analysis.md](./12-hermes-agent-memory-system-analysis.md) | Hermes Agent 记忆系统架构与实现分析：短期压缩、内置长期记忆、外部 Provider、会话检索与借鉴边界 | 完成（外部方案分析，Docs Only） |
| [13-memory-injection-and-learning-update-plan.md](./13-memory-injection-and-learning-update-plan.md) | Agent Memory v2 读路径注入与写路径学习更新闭环方案：请求注入、缓存契约、抽取/Consolidation、freshness/usage/suppression；含闭环/效率/更新召回整体审查 | 方案 + 审查（v1.1 已回写 03/04/05/06/09；M1/M2/M3/M4 core 与 Web 审计面已实施，M4 证据包与 M5 运营待执行） |
| [14-runtime-gap-audit-and-fix-20261117.md](./14-runtime-gap-audit-and-fix-20261117.md) | 运行时缺口审查：多 workspace DB 证据、Turn Digest 断点、host 生命周期/超时/恢复问题与本次修复记录 | 第三轮优化 + 统一执行核 TurnDigestSink 修复完成；真实 ConPTY 双会话 E2E PASS；崩溃后 stale-active digest 恢复仍待设计 |
| [15-ai-memory-engine-design-gap-audit-20261122.md](./15-ai-memory-engine-design-gap-audit-20261122.md) | 按 `references/AI_Agent_Memory_Engine_Design.md` v1.0 审查当前 AI Memory 实现：功能覆盖、P0/P1/P2 缺口、MVP 验收对照、运行时积压证据与补齐路线图 | 完成（2026-11-22 静态审查 + 定向测试；运行时积压与 G0–G5 门禁待关闭） |
| [16-ai-memory-engine-implementation-plan-20261122.md](./16-ai-memory-engine-implementation-plan-20261122.md) | 按 `15` 的 17 个缺口制定施工计划：G0–G5 工作包、schema/API/工具/CLI 文件落点、测试矩阵、迁移/回滚、证据与执行顺序 | 方案完成，待执行（G0 status 可观测性已有部分代码；FTS5 driver 能力为 G1 阻断门） |
| [17-g35-core-memory-foundation-evidence-20261123.md](./17-g35-core-memory-foundation-evidence-20261123.md) | G3.5 Core Memory 基础切片：migration v22、候选来源资格、显式审核激活、归档与审计事件 | 部分实施；已提交；G3.5 仍未关闭 |
| [18-g35-core-read-surface-evidence-20261123.md](./18-g35-core-read-surface-evidence-20261123.md) | G3.5 Core 读取/管理面：只读工具、HTTP/CLI、重建 job、selector 注入、分类预算最小切片与独立 usage ledger | 部分实施；已提交；G3.5 仍未关闭 |
| [19-implementation-status-20261123.md](./19-implementation-status-20261123.md) | 2026-11-23 实施状态快照：G0–G5 门禁矩阵、提交状态、测试证据与文档一致性缺口 | **当前状态**（G3/G3.5 已提交；G1 blocked；G0-5/G2/G4/G5 未关闭） |
| [20-g3-record-versions-evidence-20261123.md](./20-g3-record-versions-evidence-20261123.md) | G3 记录版本、回滚与 as-of 查询证据：schema v20/v21、版本写入接入、rollback 语义、API/CLI/tool 与定向测试 | 代码级主体通过并已提交；FTS/G2 tenant/G4 高风险 supersede 依赖未关闭 |

> `12/13` 是外部对照与落地细化；`13` v1.1 已回写 `03/04/05/06/09`。

### 2.1 变更提案（非基线）

schema / 触发 / 写路径的变更先以 patch proposal 记录；决策确认后再回写基线文档：

| 提案 | 定位 | 当前状态 |
| --- | --- | --- |
| [patches/2026-11-11-turn-digest-runtime-outbox.md](./patches/2026-11-11-turn-digest-runtime-outbox.md) | Turn Digest 运行时 Outbox：turn-end 只写轻量 digest；receipt-backed 新回合由 capture worker 消费，无 receipt 历史由 session-end/idle/startup compatibility recovery 消费；Phase 1 不把 digest 当作第二份 transcript；含多 turn 分批/窗口化/L0→L1→L2 rollup/range invalidation | **Phase 1 core + 窗口化 + per-window stale + rollup + range invalidation 已实现；新回合路由以 2026-11-19 收敛规则为准** |
| [patches/2026-11-18-turn-end-durable-capture-and-async-extraction.md](./patches/2026-11-18-turn-end-durable-capture-and-async-extraction.md) | 写路径修正提案：turn 终态 durable capture + immediate enqueue；turn-range extraction job；session liveness/orphaned recovery；maintenance owner；session TTL drain/dead-letter | **方案提案，待评审；未实施** |

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
- 尚未评审的变更先放在 `patches/` 下；评审通过后再回写 `03/04/05/06/13` 等基线文档。
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
