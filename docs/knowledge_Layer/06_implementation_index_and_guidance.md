# 06 — 方案实施索引与指引

> 版本：v1.0
> 日期：2026-09-20
> 定位：`docs/knowledge_Layer/` 的**实施入口**。`README.md` 回答"每份文档是什么"，本文回答"**要动手时，从哪开始、按什么顺序、每步碰哪些文件、被谁阻塞、怎么验收**"。
> 事实源声明：本文是**索引与指引**，不是设计文档，也不是决策依据。设计以 `01`/`02`/`03`/`supplement/*` 为准，决策以 `adr/*` 为准，落地计划与验收以 `04` 为准。

---

## 0. 三分钟上手

| 角色 | 从这里开始 |
|---|---|
| 项目 owner | §3 ADR 门禁表 —— 先 Accept 阻塞项（当前 `0001`、`0007` 卡 Phase 1） |
| 实施者 | §4 的 Phase 0 → 附录 A 首日清单 |
| 评审者 | `04` §2 / §6 / 附录 B，配合本文 §3 |
| 新人 | `README.md` §2 → 本文 §1、§2 |

**一句话状态**：Phase 0 **进行中**（2026-09-20 起）——5 项交付里 1 / 2 / 4 已落地，3（基线报告）索引侧已实测、任务侧待跑，5 未开始；7 条 ADR 仍全部 `Proposed`，其中 `0001`、`0007` 是 Phase 1 的硬门禁。

---

## 1. 当前状态一页纸

### 1.1 阶段

| Phase | 内容 | 状态 | 进入条件 |
|---|---|---|---|
| 0 | 基线与契约 | **进行中** | 无（可立即开工） |
| 1 | 索引 MVP（shadow） | 未开始 | ADR-0001、ADR-0003（口径）、ADR-0007 被 Accept |
| 2 | Exploration Memory + Planner | 未开始 | ADR-0004 Accept；Phase 1 验收通过 |
| 3 | Code API 与工具面收敛 | 未开始 | Phase 2 验收通过 |
| 4 | Adapter SPI 与可选 LSP | 未开始 | ADR-0002 / 0005 / 0006 Accept；Phase 1 验收通过 |
| 5 | Change Manager 与一致性 | 未开始 | Phase 3 合入后 |
| 6 | Context Compiler 深度集成 | 未开始 | Phase 2 + 3 + 5 验收通过 |
| 7 | Semantic Retrieval（可选） | 未开始 | Phase 6 验收通过 |
| 8+ | 跨语言 / Runtime Evidence / ABAP | **明确推迟** | v1 稳定且有真实需求后单独立项 |

### 1.2 开工前必须清的阻塞

1. **ADR-0001**（`Phase1-start`）：Project/Module/Language 模型收敛 → 等 owner Accept
2. **ADR-0007**（`Phase1-start`）：幽灵表清理与文档不变量 → 等 owner Accept
3. **ADR-0003**（`Phase0-baseline` 定阈值 / `Phase1-start` 定口径）：shadow 差异率分母定义
4. 其余 4 条（`0002` / `0004` / `0005` / `0006`）按各自 Gate 在对应 Phase 前 Accept 即可

> 细节见 §3。**在门禁 ADR 被 Accept 之前，Phase 1 及之后的实现不得开工**；Phase 0 可立即开始。

---

## 2. 文档地图

| 文档 | 回答什么问题 | 什么时候读 | 事实源角色 |
|---|---|---|---|
| `README.md` | 目录索引、事实源声明、Phase 进度 | 首次进入本目录 | 索引 |
| `01_low_token_multilanguage_ai_agent_harness_design.md` | 架构意图（原则 / 分层 / 目标 / 非目标） | 理解"为什么做" | 架构意图 |
| `02_agent_harness_technical_design_spec_sqlite.md` | core schema 的 DDL（最全） | 写 store / 迁移时 | **core schema 唯一事实源** |
| `03_agent_harness_supplement.md` | 补充规格（稳定 ID / 类型 / LSP / 安全 / 评估） | 写 extension schema 时 | **extension schema 事实源** |
| `supplement/05_runtime_integration_project_detection_and_lsp.md` | runtime 集成 / 项目类型感知 / LSP 接入 | Phase 0、Phase 4 前 | 集成规格（不复制 DDL） |
| [`../lsp/`](../lsp/README.md) | LSP 实施方案：crush 参考分析 / runtime 集成设计 / 实施顺序与验收（A1–A11） | Phase 0 末、Phase 4 开工前 | 实施方案（**非**事实源） |
| `04_completeness_review_and_optimized_plan.md` | 完整性评审 + v1 方案 + Phase 路线 + 验收 | 实施与验收全程 | **落地计划与验收事实源** |
| `GLOSSARY.md` | 术语规范名 | 任何命名之前 | **术语唯一事实源** |
| `CHANGELOG.md` | 变更历史 | 改动前后 | 变更历史 |
| `adr/README.md` + `adr/000x-*.md` | 决策及其 Gate | **动手前** | **决策唯一事实源** |
| **本文 `06`** | 实施顺序 / 门禁 / 文件落点 / 验收入口 | 每次开工前 | 实施索引（**非**事实源） |

**读者路径**

- 新人：`README.md` → `04` §0 TL;DR → `01` 架构意图 → `GLOSSARY.md`
- 实施者：**本文 §3 → §4 → §5** → `04` §3 / §4 / §5 → `02`（注意顶部 schema 应用顺序标注）→ `03` §1、`supplement/05`
- 评审者：`04` §2 / §6 / 附录 B → 对照 `01`/`02`/`03` 原文 → 在 `adr/` 记录裁决

---

## 3. ADR 门禁表（开工前必读）

> **决策唯一事实源是 [`adr/`](adr/README.md)。** `04` 附录 B 与 `supplement/05` §9 是历史散文列表，**不是决策依据**。
> 只有项目 owner 能把 `Proposed` 改为 `Accepted`；Accepted 后正文不可改，要改就写新 ADR 并在 `Supersedes` 引用旧号。

| ADR | 主题 | Gate | 阻塞 | 状态 | 可逆性 |
|---|---|---|---|---|---|
| [0001](adr/0001-project-module-language-schema.md) | Project/Module/Language 模型收敛 | **Phase1-start** | Phase 1 | Proposed | expensive（当前零迁移成本） |
| [0002](adr/0002-acp-lsp-ownership.md) | ACP 下 LSP 归属与能力面 | Phase4-start | Phase 4 | Proposed | cheap |
| [0003](adr/0003-exploration-attribution-metrics.md) | 探索归因与 shadow 差异率度量 | Phase0-baseline（阈值）/ Phase1-start（口径） | Phase 0 阈值、Phase 1 口径 | Proposed | cheap |
| [0004](adr/0004-stale-index-tool-surface.md) | 陈旧索引下的 `code.*` 工具面 | Phase2-start | Phase 2 | Proposed | cheap |
| [0005](adr/0005-windows-child-process-lifecycle.md) | Windows 子进程树生命周期 | Phase4-start | Phase 4 | Proposed | moderate |
| [0006](adr/0006-lsp-position-encoding-boundary.md) | LSP 位置编码转换边界与缓存键 | Phase4-start | Phase 4 | Proposed | moderate |
| [0007](adr/0007-phantom-tables-and-doc-invariants.md) | 幽灵表清理与文档不变量 | **Phase1-start** | Phase 1 | Proposed | cheap |

**建议的 Accept 顺序**：owner 先集中处理 `0001` + `0007`（Phase 1 硬门禁）；Phase 0 基线跑完后再定 `0003` 的阈值部分；`0002` / `0004` / `0005` / `0006` 在对应 Phase 前处理即可。

> **注意**：ADR-0005 表面是"Job Object 还是 `taskkill /T` 二选一"，但**答案已存在于仓库代码**——`internal/executor/process_guard_windows.go` 已以 Job Object（`KILL_ON_JOB_CLOSE`）为主、`taskkill /T /F` 为降级。ADR-0005 的实质是"复用既有守卫"。

**仍未转 ADR 的条目**（不构成决策依据）：`04` 附录 B 其余条目、`supplement/05` §9 之外的散文项。转 ADR 的流程见 `adr/README.md` §8。

---

## 4. 分阶段实施指引

> 每 Phase 的**完整**交付 / 验收 / 回滚原文在 `04` §5；本节是实施视角的压缩索引。
> **排期铁律**（`04` §5）：① 每 Phase 必须能独立上线、独立回滚、独立测量；② 前一 Phase 验收未通过，不得进入下一 Phase；③ 任何 Phase 都不得改变 `knowledge.mode=off` 时的行为；④ 每 Phase 结束必须更新 `04` 的状态列与 `README.md`。

### Phase 0 — 基线与契约（建议 1 个迭代）

- **目标**：先能测量，再谈优化；冻结 v1 schema 与接口。
- **前置 ADR**：ADR-0003 的**阈值部分**需 Phase 0 基线跑完后才能定。
- **交付**
  1. `knowledge.mode = "off"` 为默认值；知识层代码可存在但完全不参与任何路径。
  2. `usageledger` 扩展 9 个字段：`exploration_tokens`、`reuse_tokens`、`index_lookup_count`、`index_hit`、`fallback_count`、`unsafe_reuse_count`、`tool_calls_per_task`、`repeated_read_count`、`knowledge_version_mismatch_count`。
  3. 基线报告：本仓库（排除 `node_modules` / `dist` / `.aicli`）+ 1 个外部 Go 仓库，跑 5–10 个代表任务，记录 token 构成、工具调用数、重复读取次数、p95 延迟。
  4. v1 DDL（`04` §4.3）+ `schema_migrations` + 迁移脚本骨架（接入 `internal/migrate`）。
  5. 与相邻计划的交叉评审（见 §8；§8 表列 5 行）。
- **文件落点**：新增 `backend/internal/knowledge/{models,config,version,store,telemetry}.go`、`migrations/0001_init.sql`；修改 `backend/internal/usageledger/sqlite_store.go`、`backend/internal/usageanalytics/*`、`backend/internal/sqliteutil/sqliteutil.go`、`backend/internal/migrate/*`、`backend/configs/*.yaml`；文档治理 `00` / `01` / `02` / `03`。
- **验收门槛**：能回答"每个任务平均多少 token 花在探索 / 重复读取"，且数字可由 ledger **复算**（同一份数据两次计算结果一致）；`mode=off` 下全量回归与改动前一致；schema 能被 `sqliteutil.OpenFileCtx` 打开，无 `database is locked`、无 `PRAGMA` 报错。
- **回滚**：删除 knowledge 包与配置项，零行为影响。
- **状态**：**进行中**（2026-09-20）。
  1. ✅ `knowledge.mode = "off"` 默认值 —— `knowledge/config.go` + `configs/*.yaml`，用例 `knowledge_config_test.go`。
  2. ✅ `usageledger` 9 个归因字段 —— `sqlite_store.go` 幂等补列（旧库兼容）+ `entity.TokenUsageHistory` + `knowledge/telemetry.go` 采集器。
  3. 🟡 基线报告 —— **索引侧已完成**（[`reports/phase0_baseline_report.md`](reports/phase0_baseline_report.md)：本仓库 3860 文件 / 146.9s / 247.5 MiB / 覆盖率 100%；外部 gin 99 文件、prometheus 1010 文件，见报告 §2.4）；任务侧 5–10 个代表任务待跑（原因与步骤见报告 §6）。
  4. ✅ v1 DDL + `schema_migrations` + 迁移骨架 —— `knowledge/migrations/0001_init.sql`（`internal/migrate/*` 无需改动）。
  5. ✅ 与相邻计划的交叉评审 —— 完成，见 [`reports/phase0_cross_review.md`](reports/phase0_cross_review.md)。结论：4 份计划均无实现层冲突（composer 计划已显式把"内容检索/索引"划给本方案）；发现 1 处**命名撞车**（`cache_entries`，见 §8 已修）与 1 处**跨文档 schema 命名漂移**（`refs`/`references`、`symbols_fts`/`symbol_fts`，属 ADR-0001/0007 的 Phase 1 硬门禁）。

  实测副产物：修掉两个会让索引"少干活却看起来达标"的缺陷——`stable_key` 缺 `namespace`（35% 文件的符号与引用整份丢失）与**局部变量被当成符号**（5080 行身份合并；builtin/3 起降到 732 行）。见 `CHANGELOG.md` 2026-09-20 两条与报告 §4.1 / §4.4。
  门槛预判：Phase 1 的"首次全量 ≤ 120s""DB ≤ 200MB"两条**初值已被本仓库实测击穿**（146.9s / 247.5 MiB）；3 个仓库对照（报告 §2.4）显示成本应按"每 ref"表达，§5 建议改为"≤ 0.6ms × refs 且 ≤ 300s""≤ 1 KiB × refs 且 ≤ 300MB"，定稿需 `04` §7.4 评审。

### Phase 1 — 索引 MVP（只读，影子模式）

- **目标**：持久化 file / symbol / refs 轻索引 + FTS5；不改变任何模型可见行为。
- **前置 ADR**：**ADR-0001**、**ADR-0007**（均 `Phase1-start`）、ADR-0003（口径）。
- **交付**
  1. `knowledge/index`：从 `workspace/scanner.go` 升级。保留正则作为 builtin adapter；补 Java / Rust / C++ 粗符号（`class` / `func` / `fn` / `struct` / `interface` 级别）；**修正测试文件被忽略的问题**——改为索引并写 `files.is_test=1` / `symbols.is_test=1`，`ignorePatterns` 中的 `.*\.test\.(go|py|js|ts)$` 与 `^_\w+` 必须移除或改为标记，否则 `code.tests` 与影响面分析永久为空；输出 `content_hash`、`is_generated`、`language`、`size`、`mtime_ns`。
  2. `knowledge/store`：`04` §4.3 的 v1 表 + `symbols_fts` 同步触发器。
  3. 增量：仅 `content_hash` 变化才重解析；删除文件标记 `deleted_at`，不立即物理删除。
  4. `knowledge.mode=shadow`：`code.search` 内部同时算索引结果与 grep 结果，**返回 grep 结果**，把差异写入对比日志与 `invalidation_events`。
  5. `knowledge.status` CLI / HTTP：索引状态、文件数、符号数、DB 大小、最近 job、锁等待 p95。
- **文件落点**：新增 `knowledge/store_sqlite.go`、`indexer.go`、`indexer_light.go`、`adapter_builtin.go`、`query.go`、`owner.go`、`knowledge_test.go`；修改 `workspace/{scanner,symbol_index,context_builder}.go`、`sqliteutil`、`events` / `runtimeevents`。
- **验收门槛**：本仓库首次全量索引 ≤ 实测基线（先测后定，初值 ≤ 120s）；单文件增量 < 50ms；DB ≤ 200MB；shadow 下 `code.search` 与 `grep` 的 top-10 文件集合差异率 < 15% 且每条差异可解释；`files.content_hash` 与磁盘一致率 100%（抽样 ≥ 200 文件）；锁等待 p95 < 50ms。
- **回滚**：`mode=off` + 删除 `knowledge.db`。
- **状态**：未开始。

### Phase 2 — Exploration Memory + Context Planner

- **目标**：解决"多轮重复探索"，这是 `00` / `01` 共同认定的最高 ROI。
- **前置 ADR**：ADR-0004；Phase 1 验收通过。
- **交付**
  1. `exploration_sessions` / `nodes` / `edges` 落库；节点带 `knowledge_version`。
  2. `memorystore`（长期笔记，不自动写入）与 exploration memory（任务工作集，自动写入）分层；两者在 `context_items` 中用 `item_type` 区分。
  3. `contextmgr` 新增 `KnowledgeMode = off | signals | broad`，与 `WorkspaceMode` / `RecallMode` 对称；`Strategy` 增加 `MinKnowledgeQueryLength`、`ReuseConfidenceFloor`。
  4. `knowledge.Planner`：输出 `Plan{Reuse, Explore, Degraded, Reason}`；复用需满足 `04` §4.4 阈值。
  5. 复用必须做一次验证读取（confidence < 0.90 时）。
  6. 多 Agent 语义：探索节点写 `session_id + task_id + workspace_id`；只读子代理不写索引、不写 exploration memory；跨任务复用阈值 ≥ 0.90。
- **验收门槛**：多轮任务重复工具调用次数下降 ≥ 30%（用 ledger 归因，样本 ≥ 20，且给出置信区间）；`unsafe_reuse_count = 0`；端到端 p95 延迟增幅 ≤ 10%；关闭 `KnowledgeMode` 后指标回到基线（可逆）。
- **回滚**：`KnowledgeMode=off`。
- **状态**：未开始。

### Phase 3 — Code API 与工具面收敛

- **目标**：把探索变成查询；与现有工具并存且可灰度。
- **前置**：Phase 2 验收通过。
- **交付**
  1. `code.search` / `code.inspect` / `code.navigate` / `code.references` / `code.callers` 注册进 `toolkit.Registry`。
  2. 统一返回结构：`source` / `confidence` / `version` / `range` / `truncated` / `next_cursor` / `explanation` / `degraded`。
  3. 降级协议（`04` §4.6）：索引不可用 / 低置信 / 出错 → fallback 到 `grep` / `view`，返回带 `source="fallback"`。
  4. `view` 增加可选 `symbol` 参数（按符号读取），无索引时退化为行范围。
  5. 系统提示更新：明确 `code.*` 与 `grep` / `view` 的分工；与 `aicli-tool-capability-convergence-plan.md` 合并评审。
- **文件落点**：新增 `toolkit/tools/code_*.go`（5 个）；修改 `toolkit/registry.go`、`toolkit/tools/view.go`、`configs/model_cards.yaml`。
- **验收门槛**：典型任务（"改 timeout 默认值""谁调用 X""影响面分析"）探索 token 下降 ≥ 40%；无索引环境下 `code.*` 100% 可用（降级路径覆盖所有工具）；fallback 触发率 ≤ 30%；工具调用总数不增加。
- **回滚**：工具开关关闭，回到 `grep` / `view`。
- **状态**：未开始。

### Phase 4 — Adapter SPI 与可选 LSP

- **目标**：把语言差异移出 core。
- **前置 ADR**：ADR-0002、ADR-0005、ADR-0006（均 `Phase4-start`）；Phase 1 验收通过。
- **交付**
  1. `LanguageAdapter` SPI + 能力声明（`AdapterCapabilities`）。
  2. builtin adapter（v1 默认）、tree-sitter adapter（可选）、lsp adapter（可选，进程外）。
  3. LSP 进程管理：启动 / 崩溃 / 超时 / 内存上限 / 僵尸回收；Windows 优先验证。
  4. adapter / parser 版本参与 `stable_key` 与 `confidence`；版本变化触发 `full_rebuild_on_adapter_change`。
  5. 离线模式：无 LSP 时仍可 search / symbol / file / 基本图。
- **文件落点**：新增 `knowledge/adapter.go`、`adapter_treesitter.go`、`adapter_lsp.go`、`indexer_deep.go`、`testdata/golden/`，以及 `knowledge/lsp/` 子包；`supplement/05` §8 给出集成点（`internal/acp/`、`cmd/runtime-server/main.go` 等）。
- **验收门槛**：Go 仓库 definition / references 精度 ≥ 90%、召回 ≥ 85%（golden set，人工标注 ≥ 200 条）；LSP 崩溃 100% 降级不阻断 agent；进程数 ≤ `lsp.max_processes`；内存 ≤ `lsp.memory_limit_mb`；关闭 adapter 后 builtin 仍可用。
- **回滚**：关闭 adapter，退回 builtin。
- **状态**：未开始。

### Phase 5 — Change Manager 与一致性

- **目标**：索引与代码变化同步且可证明正确。
- **前置**：Phase 3 合入后（工具面先稳定，再引入变更源）。
- **交付**
  1. 三类变更源：agent edit hook（主，同步标记）、git diff（校正）、fsnotify（优化，可关闭）。
  2. debounce + `index_jobs` 串行队列 + owner 仲裁（`04` §4.7）。
  3. 版本向量：`knowledge_version` 落地并参与 cache key 与 context item。
  4. GC：`deleted_at` 超过 30 天或 N 个版本的符号 / 文件清理；DB 超过 `max_db_size_mb` 时触发。
  5. 增量 vs 全量等价性测试：固定样本仓库，编辑 N 次后比较增量索引与全量重建结果。
  6. schema 迁移 + 版本拒绝（新 DB 不被旧代码打开）。
- **文件落点**：新增 `knowledge/change.go`、`equivalence_test.go`；修改 `owner.go`、`migrate/*`、`events`。
- **验收门槛**：agent 连续编辑 100 次后，增量索引与全量重建 diff = 0；外部 `git checkout` 后 stale 判定正确率 100%（样本 ≥ 50 次）；锁等待 p95 < 50ms、锁重试失败率 < 0.1%；双实例同开同一 workspace，后到者正确降级只读，无锁死。
- **回滚**：关闭 watcher，退回显式 `knowledge.reindex`。
- **状态**：未开始。

### Phase 6 — Context Compiler 深度集成

- **目标**：把知识层输出变成最小上下文。
- **前置**：Phase 2 + 3 + 5 验收通过。
- **交付**
  1. `contextpack` 新增 `knowledge.Provider`（只读）。
  2. `contextmgr.LayerPlan` 增加 knowledge layer，映射到 hot / warm / cold。
  3. Observation Compressor 与 `compactruntime` 合并，避免两套压缩。
  4. 防注入 / 信任等级 / 冲突解决（对应 `03` §14）：`context_items.trust` 与 `reason` 落地。
  5. `context_snapshots` / `items` 可解释性：每个 item 有 source / version / trust / reason / stale。
- **文件落点**：新增 `contextpack/knowledge_provider.go`、`knowledge/compiler.go`；修改 `contextmgr/manager.go`、`contextpack/context_pack.go`。
- **验收门槛**：相同任务上下文 token 下降 ≥ 25% 且任务成功率不降（A/B，样本 ≥ 20）；`stale` item 注入数 = 0；compiler 缓存命中 p95 < 50ms、未命中 p95 < 200ms。
- **回滚**：provider 开关关闭。
- **状态**：未开始。

### Phase 7 — Semantic Retrieval（可选，后置）

- **交付**：FTS5 优先；embedding 默认关闭（开启需显式配置 + 隐私确认）；本地 embedding 优先，`openai` provider 需二次确认并写审计；rerank = `lexical + symbol + graph + task_relevance + recency + confidence`。
- **验收门槛**：在"不知道符号名"的任务上 recall 有可测提升（golden set）；默认配置下出网次数 = 0；开启 embedding 后端到端延迟增幅 ≤ 20%。
- **回滚**：`embedding.enabled=false`。
- **状态**：未开始。

### Phase 8+ — 明确推迟

跨语言 IDL / HTTP / RPC 图、Runtime Evidence、ABAP 适配、分布式索引、自动引用修复。**不在 v1 承诺范围内**，需在 v1 稳定并有真实需求后单独立项。

### 4.1 Phase 依赖图

```text
Phase 0 (基线/契约)
   │
   ▼
Phase 1 (索引 MVP / shadow)
   │
   ├──────────────► Phase 4 (Adapter SPI / LSP)  ──┐
   ▼                                               │
Phase 2 (Exploration Memory / Planner)             │
   │                                               │
   ▼                                               ▼
Phase 3 (Code API / 工具面)  ◄────────────  Phase 5 (Change Manager / 一致性)
   │                                               │
   └──────────────► Phase 6 (Context Compiler) ◄───┘
                          │
                          ▼
                    Phase 7 (Semantic, 可选)
```

说明：Phase 4 与 Phase 2 无强依赖，可并行；Phase 5 必须在 Phase 3 之后合入；Phase 6 依赖 Phase 2 + 3 + 5。

### 4.2 里程碑判定表

| 里程碑 | 判定条件 | 不通过时的决策 |
|---|---|---|
| M0 可测量 | Phase 0 验收通过 | 若基线显示探索 token 占比 < 10%，则降优先级 |
| M1 索引可信 | Phase 1 验收通过（shadow 差异率达标） | 重构索引，而非进入 Phase 2 |
| M2 复用安全 | Phase 2 验收通过（`unsafe_reuse=0`） | 回退 Phase 1 |
| M3 工具可切换 | Phase 3 验收通过（fallback ≤ 30%） | 暂缓 Phase 4 |
| M4 一致可证 | Phase 5 验收通过（增量 = 全量） | 禁止开启 `mode=on` |
| M5 端到端收益 | Phase 6 验收通过（token ↓ ≥ 25% 且成功率不降） | 保持 shadow，不 GA |

---

## 5. 文件落点索引

> 权威清单在 `04` 附录 C；本节按 Phase 重组，便于排期。
> **集成点**（runtime-server / aicli cmd tui / aicli acp / 项目类型感知 / LSP）另见 `supplement/05` §8。

### 5.1 新增（v1 范围）

| 路径 | 用途 | Phase |
|---|---|---|
| `backend/internal/knowledge/models.go` | 记录类型与 `Plan` | 0 |
| `backend/internal/knowledge/config.go` | `knowledge.*` 配置解析与默认值 | 0 |
| `backend/internal/knowledge/version.go` | `knowledge_version`、`stable_key`、`confidence` | 0 |
| `backend/internal/knowledge/store.go` | `Store` 接口 | 0 |
| `backend/internal/knowledge/store_sqlite.go` | 基于 `sqliteutil.OpenFileCtx` 的实现 | 1 |
| `backend/internal/knowledge/migrations/0001_init.sql` | v1 DDL | 0 |
| `backend/internal/knowledge/owner.go` | 单写者仲裁（owner.json + 心跳 + PID 校验） | 1/5 |
| `backend/internal/knowledge/indexer.go` | `Indexer` 接口与调度 | 1 |
| `backend/internal/knowledge/indexer_light.go` | 轻索引（文件 + 顶层符号 + imports） | 1 |
| `backend/internal/knowledge/indexer_deep.go` | 深索引（按需，异步可取消） | 4 |
| `backend/internal/knowledge/adapter.go` | `LanguageAdapter` SPI + 能力声明 | 4 |
| `backend/internal/knowledge/adapter_builtin.go` | 内置正则适配器（从 `workspace/scanner` 升级） | 1 |
| `backend/internal/knowledge/adapter_treesitter.go` | tree-sitter 适配器（可选） | 4 |
| `backend/internal/knowledge/adapter_lsp.go` | LSP 适配器（可选，进程外） | 4 |
| `backend/internal/knowledge/query.go` | 检索：FTS5 + 符号精确 + 图遍历 | 1/3 |
| `backend/internal/knowledge/planner.go` | `Planner`：复用 / 探索决策 | 2 |
| `backend/internal/knowledge/compiler.go` | `Compiler`：最小上下文生成 | 6 |
| `backend/internal/knowledge/change.go` | `ChangeManager`：三类变更源 | 5 |
| `backend/internal/knowledge/telemetry.go` | 指标埋点（写入 `usageledger`） | 0/1 |
| `backend/internal/knowledge/knowledge_test.go` | 单元测试 | 1 |
| `backend/internal/knowledge/equivalence_test.go` | 增量 vs 全量等价性测试 | 5 |
| `backend/internal/knowledge/testdata/golden/` | golden set（符号 / 引用标注） | 4 |
| `backend/internal/contextpack/knowledge_provider.go` | `knowledge.Provider` | 6 |
| `backend/internal/toolkit/tools/code_search.go` | `code.search` | 3 |
| `backend/internal/toolkit/tools/code_inspect.go` | `code.inspect` | 3 |
| `backend/internal/toolkit/tools/code_navigate.go` | `code.navigate` | 3 |
| `backend/internal/toolkit/tools/code_references.go` | `code.references` | 3 |
| `backend/internal/toolkit/tools/code_callers.go` | `code.callers` | 3 |
| `docs/knowledge_Layer/README.md` | 文档索引 | 0 |
| `docs/knowledge_Layer/GLOSSARY.md` | 术语表 | 0 |
| `docs/knowledge_Layer/CHANGELOG.md` | 变更日志 | 0 |
| `docs/knowledge_Layer/adr/0001..0007-*.md` | 7 条初始决策 | 0 |
| `docs/knowledge_Layer/06_implementation_index_and_guidance.md` | **本文** | 0 |

### 5.2 修改

| 路径 | 修改点 | Phase |
|---|---|---|
| `backend/internal/workspace/scanner.go` | 测试文件改标记；补 Java / Rust / C++ 粗符号；输出 `content_hash` / `is_generated` | 1 |
| `backend/internal/workspace/symbol_index.go` | 支持由持久 Store 构建，而非每次全量 scan | 1 |
| `backend/internal/workspace/context_builder.go` | 从 Store 读取；补 `knowledge_version` | 1/2 |
| `backend/internal/contextmgr/manager.go` | 新增 `KnowledgeMode`、`Knowledge` 字段、`BuildInput` 字段 | 2/6 |
| `backend/internal/contextpack/context_pack.go` | 注册 `knowledge.Provider` | 6 |
| `backend/internal/toolkit/registry.go` | 注册 `code.*` | 3 |
| `backend/internal/toolkit/tools/view.go` | 增加可选 `symbol` 参数 | 3 |
| `backend/internal/usageledger/sqlite_store.go` | 新增探索归因字段 | 0 |
| `backend/internal/usageanalytics/*` | 聚合与展示新指标 | 0 |
| `backend/internal/sqliteutil/sqliteutil.go` | 增加 `foreign_keys=ON` 选项与锁等待埋点 | 0/1 |
| `backend/internal/migrate/*` | 接入 knowledge 迁移 | 0 |
| `backend/internal/events` / `runtimeevents` | knowledge 事件接入 | 1/5 |
| `backend/configs/config.yaml` | `knowledge.*` 配置段 | 0 |
| `backend/configs/runtime.yaml` / `runtime.win7.yaml` / `config.runtime.snapshot.yaml` | 同上 | 0 |
| `backend/configs/model_cards.yaml` | 若涉及工具 / 模式提示词 | 3 |
| `docs/knowledge_Layer/01_*.md` | 去重、加锚点、去 PostgreSQL | 0 |
| `docs/knowledge_Layer/02_*.md` | 顶部声明 schema 应用顺序；删除 / 标注 PostgreSQL | 0 |
| `docs/knowledge_Layer/03_*.md` | 拆分为 `supplement/` | 0 |
| `docs/knowledge_Layer/00_*.md` | 移入 `archive/` 并加免责声明 | 0 |

### 5.3 明确不新增（复用既有资产）

- 不新增 blob 存储（用 `internal/artifact`）。
- 不新增事件总线（用 `internal/events` / `runtimeevents`）。
- 不新增记忆文件格式（用 `memorystore` / `factledger`）。
- 不新增 workspace 主键（用 `workspaceregistry`）。
- 不新增压缩器（用 `compactruntime`）。
- 不新增权限模型（用 `policy` / `fsscope` / `toolbroker`）。
- 不新增 usage 账本（用 `usageledger`）。
- 不新增独立 knowledge 服务 / 端口。
- 不新增"第 5 份并列设计文档"（本文 `06` 是索引，不是设计文档）。
- 不复制 core DDL（core 事实源在 `02`）。
- 不静默安装 LSP。
- v1 不引入新的第三方 Go 依赖（SQLite 驱动与 FTS5 已具备；tree-sitter 推迟到 Phase 4 并单独评审）。

---

## 6. 验收与度量入口

- **指标定义**：`04` §7.1–§7.5（收益 / 正确性与安全硬门槛 / 性能 / 测量方法）。
- **阈值校准**：`04` §7.6 —— 必须用 Phase 0 基线校准，**不得写死**。
- **验收报告模板**：`04` §7.7 —— **每个 Phase 必须产出**。
- **里程碑判定**：`04` §5.2（即本文 §4.2）。
- **集成 / LSP 专项指标**：`supplement/05` §7。

**硬门槛速记**（来自 `04` §7.3，完整定义以原文为准）：`unsafe_reuse = 0`；`stale` item 注入 = 0；无索引环境 `code.*` 100% 可用；双实例无锁死；出网 = 0（默认配置）。

---

## 7. 变更与治理流程

```text
提出变更
  → 若涉及 schema / 接口 / 术语 / 已决策项：先写或更新 ADR（adr/）
  → 更新唯一事实源文档（02 / 03 / supplement / GLOSSARY）
  → 更新 CHANGELOG.md
  → 更新 README.md 的状态表与进度表
  → 若影响落地计划：更新 04 的 Phase 状态与验收
  → 若 Phase 状态变化：更新本文 §1
```

**关键约束速查**

- 不要在本目录新增"第 5 份并列设计文档"；新内容归入 `supplement/` 或 `adr/`。（本文 `06` 是**索引 / 指引**，非设计文档。）
- 不要在 `02` 之外复制 core DDL（extension schema 例外，落点是 `03` / `supplement/*`）。**`04` 不得含 `CREATE TABLE`**——见 ADR-0007 §4.3，现存 `index_jobs` DDL 待迁移。
- 不要在没有基线的情况下写死阈值。
- 不要让 `knowledge.mode=off` 时的行为发生任何改变。
- 任何新增表都必须回答"没有它哪个 Phase 会失败"，否则推迟。
- ADR 不得复制已存在的 DDL；ADR **可以**给出新增对象的拟议 DDL（该对象尚无事实源），一旦 Accept 必须落到 `02`（core）或 `03` / `supplement/*`（extension），ADR 改为引用。

---

## 8. 边界（不要重复实现）

完整表见 `README.md` §5。速查：

| 相邻计划 | 边界 |
|---|---|
| `docs/plan/aicli-tool-capability-convergence-plan.md` | 工具命名与优先级以其为准；本方案只补 `code.*` 的索引侧语义与降级协议 |
| `docs/plan/tool-output-artifact-cascade-audit-and-optimization-plan-20260919.md` | 工具输出归档 / 截断以其为准；本方案复用 `internal/artifact`。**L4 ↔ `view` 契约（已实现）**：`view` 的默认窗口 `viewDefaultLimit` 已刻意收窄，避免 L4 按其 `ModelToolTextByteBudget`（默认 12 KiB）静默二次截断 `view` 文本、把模型推去 `artifact_read` 字节分页而绕过 `view` 自身的 `offset`/`limit` 续读协议；`view` 用 `is_truncated` / `long_lines_truncated` 声明"还有更多"，`agent/tool_runtime_events.go` 经 `truncatedToolMetadata(metadata["is_truncated"])` 透传。 |
| `docs/plan/composer-at-file-reference-workspace-search-plan.md` | 工作区搜索 UI / 交互以其为准；本方案提供可选索引后端 |
| `docs/plan/llm-cache-analytics-unified-plan.md` | 缓存与分析口径以其为准。**注意**：该计划定义的是 LLM prompt cache（`prompt_cache_key` / `prompt_cache_epoch` / `prompt_fingerprint`，均为事件载荷字段），**不定义任何表**；知识层的 `cache_entries`（`cache_type ∈ retrieval\|compile\|summary`，键含 `knowledge_version`）是另一个概念、另一个 DB、另一套失效规则，不得绑到 prompt cache 代际语义上（见 `reports/phase0_cross_review.md` §2.4） |
| `docs/plan/session-usage-analytics-and-agent-diagnostics-plan.md` | 指标埋点与展示以其为准；本方案只新增探索归因字段 |
| `docs/plan/codex-compact-token-usage-observation-analysis.md` | 压缩策略以其为准；本方案复用 `compactruntime` |
| `docs/plan/agent-trajectory-view-implementation-plan.md` | 轨迹展示以其为准；`exploration_*` 表可作为其数据源 |
| `docs/ANALYSIS-sqlite-lock-problem.md` | SQLite 锁问题的既有分析；本方案的单写者仲裁是其直接对策 |

---

## 9. 已知阻塞与待办（等 owner Accept 后才执行）

> 以下均为**显式待办**，文档中已正确标注；**在触发条件满足前不得执行**。

| # | 待办 | 触发条件 | 落点 |
|---|---|---|---|
| 1 | `04` L546 的 `CREATE TABLE index_jobs` 迁移到 extension schema | ADR-0007 被 Accept | `03` / `supplement/*` |
| 2 | `02` §8 总览块中 6 个无 DDL 的表名：清理或补 DDL | ADR-0007 被 Accept | `02` §8 |
| 3 | 文档不变量 I1–I5 落地为可执行检查 | ADR-0007 被 Accept | `02` / `04` |
| 4 | `03` §5.3 三条规则改写；`lsp_servers` 补 `position_encoding` 列 | ADR-0006 被 Accept | `03` |
| 5 | `02` 顶部加"core 先行；启用 extension 前必须先应用 `supplement/*` 的 ALTER"声明 | B15 转 ADR 并 Accept | `02` 顶部（**当前仅有"待补"标注**） |
| 6 | `04` 附录 B 其余条目逐条判定：决策 → ADR；笔误 → 直接改文档并记 `CHANGELOG`；风险 → 留 `04` §6 | Phase 1 开工前 | `adr/`、`04` 附录 B |
| 7 | `04` §4.3 声明的"v1 表集 ≤ 16 张"与 `02` 实际 22 张 core DDL 的不自洽 | 由 ADR-0007 不变量 I5 检查结果裁决 | — |
| 8 | `00_Code_Intelligence_Project_Knowledge_Layer.md` 移入 `archive/` 并加免责声明 | Phase 0 文档治理 | `archive/` |
| 9 | `01_*.md` 去重重写；`03_*.md` 拆分为 `supplement/*` | Phase 0 | `01` / `03` / `supplement/*` |

---

## 附录 A：Phase 0 首日清单

1. 读 `README.md` §3（事实源）、本文 §3（门禁）、`adr/README.md` §2（状态生命周期）。
2. 确认 owner 已 Accept **ADR-0001 / ADR-0007**（Phase 1 硬门禁）。若尚未 Accept，本阶段只做 Phase 0 范围内的基线、契约与文档治理。
3. 在 `backend/internal/knowledge/` 建 `models.go` / `config.go` / `version.go` / `store.go` 骨架。
4. 落 `migrations/0001_init.sql`（`04` §4.3 的 v1 DDL）。
5. 改 `backend/configs/*.yaml` 增加 `knowledge:` 段，默认 `mode: "off"`。
6. 改 `backend/internal/usageledger/sqlite_store.go` 增加 9 个探索归因字段，并在 `usageanalytics` 暴露。
7. 跑基线：本仓库 + 1 个外部 Go 仓库，5–10 个代表任务，按 `04` §7.7 模板产出基线报告。
8. 更新 `04` §5 的 Phase 0 状态、`README.md` §4 进度表、`CHANGELOG.md`。

---

## 附录 B：命名与术语速查

- 术语唯一事实源：`GLOSSARY.md`。
- 关键名：`knowledge.mode = off | shadow | on`；`KnowledgeMode = off | signals | broad`；`stable_key`；`confidence`；`knowledge_version`；`is_test`；`owner`（单写者仲裁）。
- 工具面：`code.search` / `code.inspect` / `code.navigate` / `code.references` / `code.callers`。
- 代码位置：`backend/internal/knowledge/`；DB 路径：`<workspace>/.aicli/knowledge/knowledge.db`。

---

## 附录 C：本文自我限制

- 本文是**索引与指引**，所有内容的权威原文在 `01` / `02` / `03` / `supplement/*`、`adr/*`、`04`。若本文与原文冲突，**以原文为准**，并应修正本文。
- 本文不复制 DDL、不复制阈值、不复制 ADR 正文；只给指针与实施视角的压缩。
- Phase 交付 / 验收中的数字来自 `04` §5，均为**初始建议值**，必须由 Phase 0 基线校准（`04` §7.6）。
- 本文不验证任何 LSP 实际握手（`internal/lsp` 尚不存在）；相关细节以 `supplement/05` 附录 F 的自我限制为准。

---

> 变更记录见 [`CHANGELOG.md`](CHANGELOG.md)；决策见 [`adr/`](adr/README.md)；落地计划与验收见 [`04_completeness_review_and_optimized_plan.md`](04_completeness_review_and_optimized_plan.md)；目录总索引见 [`README.md`](README.md)。
