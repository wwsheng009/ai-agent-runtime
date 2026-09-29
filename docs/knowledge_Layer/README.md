# Code Knowledge Runtime（知识层）文档索引

> 最后更新：2026-09-29
> 本目录描述"代码知识运行时"（Code Knowledge Runtime）的设计与落地计划。
> 当前阶段：**Phase 0 已落地**（2026-09-20 核心 5 交付 + 2026-09-28 交付 7 `exploration_attribution` 建表）；**Phase 1 主门槛已通过**（2026-09-28 门禁 ADR-0001 / 0003（口径）/ 0007 已 Accept；2026-09-29 交付 1–6 全部完成——含 Java/C++ 粗符号与文件软删除对账；**同日 `Phase1-shadow` 实测 v1 已执行**：真实调用重放 n=400，M1=20.81 %（view 48.4 % / grep 行级 0.47 %；grep file-level 对照 mean 31.8 %）——2026-09-29 经 ADR-0008（file-level，Accepted）+ α=0.8 裁决后复核：**主门槛通过**（合并 M1=35.95 % ≥ 0.31）；**P2 门禁已解除**（2026-09-29：ADR-0004 Accepted，待开工）；**三个入口 live 验证已通过**（aicli cmd+tui + ACP + runtime-server，见 [`reports/phase1_shadow_report.md`](reports/phase1_shadow_report.md) §4.5）。

---

## 1. 文档状态表

| 文档 | 定位 | 状态 | 事实源角色 |
|---|---|---|---|
| [`archive/00_Code_Intelligence_Project_Knowledge_Layer.md`](archive/00_Code_Intelligence_Project_Knowledge_Layer.md) | 早期对话记录合集 | **已归档**（2026-09-28 移入 `archive/`） | 仅供追溯，不作规范 |
| `01_low_token_multilanguage_ai_agent_harness_design.md` | 架构意图（原则、分层、目标、非目标） | **已加事实源边界**（2026-09-28，见其 §0；逐节删减未执行） | 架构意图 |
| `02_agent_harness_technical_design_spec_sqlite.md` | 技术规格（SQLite DDL 最全） | 待补应用顺序声明 | **core schema 唯一事实源** |
| `03_agent_harness_supplement.md` | **拆分索引**（原补充规格；正文已迁入 `supplement/*`） | **已拆分**（2026-09-28） | 历史引用映射；**extension schema 事实源 = `supplement/*`** |
| `supplement/` | `03` 拆分出的补充规格（`01`–`16`，含既有的集成文档 `05`） | **已建立**（2026-09-28 拆分完成） | **extension schema 唯一事实源** + 集成 / 检测 / LSP 规格 |
| `04_completeness_review_and_optimized_plan.md` | 完整性评审 + 优化落地计划 | **已完成，执行中** | 落地计划与验收事实源 |
| `GLOSSARY.md` | 术语表 | **已建立**（2026-09-20） | **术语唯一事实源** |
| `CHANGELOG.md` | 变更日志 | **已建立**（2026-09-20） | 变更历史 |
| `adr/` | 决策记录（`0000` 模板 + `0001`–`0009`） | **已建立**（2026-09-20）；`0001`/`0003`/`0007` 于 2026-09-28 Accept，`0008`/`0009`/`0004` 于 2026-09-29 Accept，`0002`/`0005`/`0006` 仍 `Proposed` | **决策唯一事实源** |
| `06_implementation_index_and_guidance.md` | 方案实施索引与指引（实施入口） | **已建立**（2026-09-20） | 实施索引（**非**事实源） |

> **跨目录入口**：本目录 LSP 规格（[`supplement/05_runtime_integration_project_detection_and_lsp.md`](supplement/05_runtime_integration_project_detection_and_lsp.md) §1–§9 设计与接入、§10 extension schema）的**落地实施文档**位于 [`../lsp/`](../lsp/README.md)（参考实现分析 → runtime 集成设计 → 实施顺序与验收）。该目录仅为**实施方案**，不改变本目录的事实源边界。

---

## 2. 推荐阅读顺序

**新人（了解是什么）**
1. 本 README
2. `04` 的 §0 TL;DR
3. `01` 的架构意图部分
4. `GLOSSARY.md`

**实施者（准备动手）**

> **先读 [`06`](06_implementation_index_and_guidance.md)（实施索引：顺序 / 门禁 / 文件落点）与 [`adr/`](adr/README.md)（决策）。**
> 凡 `Gate` 为 `Phase1-start` 的 ADR（当前为 `0001`、`0003`、`0007`）必须在动手前被 owner Accept；
> `04` 附录 B 只是历史散文列表，**不是**决策依据。

0. `06_implementation_index_and_guidance.md` —— 实施顺序、ADR 门禁、每 Phase 文件落点与验收
1. `04` 的 §3（与仓库现状对齐）→ §4（v1 方案）→ §5（路线图）
2. `02` 的 core schema（注意顶部“待补应用顺序声明”标注）
3. `supplement/01_symbol_identity.md`（稳定符号 ID）、`supplement/05`（集成 / 项目检测 / LSP 工程化，extension schema 见其 §10）
4. `04` 的 §7（验收指标）

**评审者（判断对不对）**
1. `04` 的 §2（完整性问题）→ §6（风险登记）→ 附录 B（矛盾点）
2. 对照 `01` / `02` 与 `supplement/*`（原 `03`）原文逐条裁决
3. 在 `adr/` 记录裁决结果

---

## 3. 单一事实源声明

| 内容 | 唯一事实源 | 其他文档 |
|---|---|---|
| core 数据库 schema | `02` | 只引用，不复制 DDL |
| extension schema | `supplement/*`（2026-09-28 由 `03` 拆入；`03` 为拆分索引），且必须在 `02` 顶部声明应用顺序 | — |
| 架构意图与原则 | `01` | 02/03 不重复原则性内容 |
| 术语 | `GLOSSARY.md` | 所有文档使用规范名 |
| 决策 | `adr/*.md` | 推翻既有设计必须先写 ADR |
| 落地计划与验收 | `04` | Phase 状态与本 README 同步 |
| LSP 落地实施方案 | [`../lsp/`](../lsp/README.md) | 只引用，不复制 LSP 规格；与 `supplement/*` 冲突时以本目录为准 |
| 变更历史 | `CHANGELOG.md` | — |

**已裁决的关键决策（决策唯一事实源：[`adr/`](adr/README.md)；下列为摘要，`04` 附录 B 已不再是决策依据）**

- 数据库：v1 用 SQLite（不用 PostgreSQL）。
- 运行模式：进程内库 + 可选 LSP 子进程（不做独立服务）。
- DB 路径：`<workspace>/.aicli/knowledge/knowledge.db`。
- 代码位置：`backend/internal/knowledge/`。
- 工具面：`code.*` 是 `grep/view` 的增强前端，不替换。
- 记忆分层：`memorystore`（笔记）/ `factledger`（事实）/ exploration memory（任务工作集）三层分离。
- 测试文件：索引并标记 `is_test`，不再忽略。
- 语义检索：FTS5 为主，embedding 默认关闭。
- v1 表集：≤ 23 张（见 `04` §0.3；= `02` §8【v1 core】的 22 表 + `symbol_fts` 虚表；口径见 [ADR-0009](adr/0009-v1-table-set-scope.md)）。
- 并发：单写者（owner 仲裁）+ 只读降级。

---

## 4. 落地进度

| Phase | 内容 | 状态 | 验收门槛摘要 |
|---|---|---|---|
| 0 | 基线与契约 | **已完成**（2026-09-28） | 可测量、`mode=off` 行为不变 |
| 1 | 索引 MVP（shadow） | **主门槛通过**（2026-09-29：交付 1–6 已完成；shadow v1 经 ADR-0008 file-level 口径复核，合并 M1=35.95 % ≥ 0.31；live 验证 3/3 入口通过；单文件增量口径待重议） | M1 调用级可用率（α=0.8，门槛 0.31 已定稿） |
| 2 | Exploration Memory + Planner | **门禁已解除，待开工**（2026-09-29：ADR-0004 Accepted） | 重复探索 ↓ ≥ 30%、`unsafe_reuse=0` |
| 3 | Code API 与工具面收敛 | 未开始 | 探索 token ↓ ≥ 40%、fallback ≤ 30% |
| 4 | Adapter SPI 与可选 LSP | 未开始 | 精度 ≥ 90%、召回 ≥ 85% |
| 5 | Change Manager 与一致性 | 未开始 | 增量 = 全量、stale 判定 100% |
| 6 | Context Compiler 深度集成 | 未开始 | 上下文 token ↓ ≥ 25%、成功率不降 |
| 7 | Semantic Retrieval（可选） | 未开始 | recall 提升、出网 = 0 |
| 8+ | 跨语言 / Runtime Evidence / ABAP | **明确推迟** | — |

> 阈值已按 `04` §7.6 完成校准（2026-09-29）：首次全量 **≤ 360 s**（n=3 中位 292.4 s；
> 跨仓库抽验 38.9–43.8 ms/文件）、DB 沿用默认 512MB（实测 313.9 MiB）；单文件增量
> **Fail**（marginal p95 302 ms，待 Phase 5 增量触发或口径重议）。详见 `04` §7.4 与
> [`reports/phase1_shadow_report.md`](reports/phase1_shadow_report.md) §4.7。
>
> 2026-09-29 `Phase1-shadow` 实测 v1（真实调用重放，n=400）：
> M1=20.81 %（view 48.4 % / grep 行级 0.5 %）、M2=24.15 %、M4=75.4 %；grep file-level
> 对照 mean 31.83 %（p90 100 %、answerable 49.4 %，待新 ADR 裁决口径）；索引侧 4989 文件 324.9 s / 313.5 MiB。
> 结论=**主门槛通过**（2026-09-29 口径裁决后复核：ADR-0008 采用 file-level、α=0.8、合并 M1=35.95 % ≥ 门槛 0.31；grep 行级 0.47 % 降为诊断）；
> 同日**三个入口 live 验证通过**（aicli cmd+tui + ACP + runtime-server：真实会话落库 grep coverage=0.667 / view=0.600；后两个入口各追加 2 条同值行；生产代码复算 M1–M4，报告 §4.5）。
> 复算 / 校准入口与剩余工作见 [`reports/phase1_shadow_report.md`](reports/phase1_shadow_report.md)。

---

## 5. 与相邻计划的边界

| 相邻计划 | 边界 |
|---|---|
| `docs/plan/aicli-tool-capability-convergence-plan.md` | 工具命名与优先级以其为准；本方案只补 `code.*` 的索引侧语义与降级协议 |
| `docs/plan/tool-output-artifact-cascade-audit-and-optimization-plan-20260919.md` | 工具输出归档/截断以其为准；本方案复用 `internal/artifact` |
| `docs/plan/composer-at-file-reference-workspace-search-plan.md` | 工作区搜索 UI/交互以其为准；本方案提供可选索引后端 |
| `docs/plan/llm-cache-analytics-unified-plan.md` | 缓存与分析口径以其为准；`cache_entries` 需对齐其 cache key 规范 |
| `docs/plan/session-usage-analytics-and-agent-diagnostics-plan.md` | 指标埋点与展示以其为准；本方案只新增探索归因字段 |
| `docs/plan/codex-compact-token-usage-observation-analysis.md` | 压缩策略以其为准；本方案复用 `compactruntime` |
| `docs/plan/agent-trajectory-view-implementation-plan.md` | 轨迹展示以其为准；`exploration_*` 表可作为其数据源 |
| `docs/ANALYSIS-sqlite-lock-problem.md` | SQLite 锁问题的既有分析；本方案的单写者仲裁是其直接对策 |

---

## 6. 变更流程

```text
提出变更
  → 若是 schema / 接口 / 术语 / 已决策项的改动：先写 ADR
  → 更新唯一事实源文档
  → 更新 CHANGELOG.md
  → 更新本 README 的状态表与进度表
  → 若影响落地计划：更新 04 的 Phase 状态与验收
```

---

## 7. 关键约束速查

- 不要在本目录新增"第 5 份并列设计文档"；新内容应归入 `supplement/` 或 `adr/`。
- 不要在 `02` 之外复制 DDL（extension schema 例外，落点是 `supplement/*`；`03` 为拆分索引）。**`04` 不得含 `CREATE TABLE`**——见 ADR-0007 §4.3；`index_jobs` DDL 已于 **2026-09-28 迁至 [`supplement/15`](supplement/15_change_management.md) §15.3**，`04` §4.3 其余 core 表 DDL 的引用化见 `06` §9 待办 **#19**。
- 不要在没有基线的情况下写死阈值。
- **阈值类 Gate 必须可达成**：α 与 Phase 1 门槛数值的 Gate 是 `Phase1-shadow`，**不是** `Phase0-baseline`（Phase 0 为 `mode=off`，结构上产不出 shadow 对比数据）——见 ADR-0003 §10。
- 不要让 `knowledge.mode=off` 时的行为发生任何改变。
- 任何新增表都必须回答"没有它哪个 Phase 会失败"，否则推迟。
- `06_implementation_index_and_guidance.md` 是**实施索引 / 指引**，不是"第 5 份并列设计文档"；新增此类元文档需同步更新 §1 状态表与 `CHANGELOG.md`。
