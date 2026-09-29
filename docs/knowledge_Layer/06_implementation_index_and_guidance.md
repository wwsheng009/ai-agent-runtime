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

**一句话状态**：Phase 0 **核心 5 交付已完成**（2026-09-20）——含基线报告（3 个仓库 + 5 真实任务 + 7 条 LLM 记录）；**Phase 1 门禁已解除**：`0001` / `0007` / `0003`（口径）已于 2026-09-28 标为 `Accepted`（owner 授权代改，裁决记录见 `CHANGELOG.md`），其余条按各自 Gate 在对应 Phase 前 Accept；ADR-0003 的 α 阈值已实测并**定稿**（2026-09-29：ADR-0008 Accepted 采用 file-level 口径、α=0.8、Phase 1 门槛 M1 ≥ 0.31，主门槛复核通过；见 `04` §7.6 与报告 §5.1）。**ADR-0004 已于 2026-09-29 Accepted，P2 门禁解除（待开工）**。文档治理尾项（§9 条目 8 / 9：`00_` 归档 + `01` 边界 / `03` 拆分）**已于 2026-09-28 完成**（记录见 `CHANGELOG.md` 补记与本文 §9.3；`01` 正文逐节删减仍留待办 **#18**），属文档维护，**不影响工程验收**。

---

## 1. 当前状态一页纸

### 1.1 阶段

| Phase | 内容 | 状态 | 进入条件 |
|---|---|---|---|
| 0 | 基线与契约 | **核心 5 交付已完成**（2026-09-20）；文档治理尾项已完成（§9 8/9，2026-09-28） | 无（可立即开工） |
| 1 | 索引 MVP（shadow） | **主门槛通过**（2026-09-29：交付 1–6 完成；shadow v1 经 ADR-0008 file-level 口径复核，合并 M1=35.95 % ≥ 0.31；live 验证 3/3 入口通过；性能已按 §7.4 校准（≤360 s）；单文件增量实测 Fail，待口径重议） | ✅ 已满足：ADR-0001 / 0003（口径）/ 0007，且 0008 / 0009 已于 2026-09-29 Accept |
| 2 | Exploration Memory + Planner | **规划完成（2026-09-29），待开工**（门禁已解除：ADR-0004 Accepted；开工规划见 §4 Phase 2「Phase 2 开工规划（2026-09-29）」） | ✅ 已满足：ADR-0004 已于 2026-09-29 Accept；Phase 1 主门槛已通过（剩余工程项见 [`reports/phase1_shadow_report.md`](reports/phase1_shadow_report.md) §5） |
| 3 | Code API 与工具面收敛 | 未开始 | Phase 2 验收通过 |
| 4 | Adapter SPI 与可选 LSP | 未开始 | ADR-0002 / 0005 / 0006 Accept；Phase 1 验收通过 |
| 5 | Change Manager 与一致性 | 未开始 | Phase 3 合入后 |
| 6 | Context Compiler 深度集成 | 未开始 | Phase 2 + 3 + 5 验收通过 |
| 7 | Semantic Retrieval（可选） | 未开始 | Phase 6 验收通过 |
| 8+ | 跨语言 / Runtime Evidence / ABAP | **明确推迟** | v1 稳定且有真实需求后单独立项 |

### 1.2 开工前必须清的阻塞

1. ~~**ADR-0001**（`Phase1-start`）：Project/Module/Language 模型收敛~~ → ✅ **Accepted（2026-09-28）**
2. ~~**ADR-0007**（`Phase1-start`）：幽灵表清理与文档不变量~~ → ✅ **Accepted（2026-09-28）**（其 §9 待办 1–3 的触发条件已满足，执行另行排期）
3. **ADR-0003**（`Phase1-start` 定口径 / **`Phase1-shadow`** 定阈值）：shadow 差异率分母定义 → ✅ **口径已于 2026-09-28 Accepted**；阈值与口径已定稿（2026-09-29：**ADR-0008 Accepted** 采用 file-level、α=0.8、Phase 1 门槛 M1 ≥ 0.31，主门槛复核通过）（**门禁可达性已于 2026-09-21 修复**，原 `Phase0-baseline` 阈值 Gate 结构性不可达，见 §9.1 #11）
4. ~~其余 4 条（`0002` / `0004` / `0005` / `0006`）按各自 Gate 在对应 Phase 前 Accept 即可~~ → `0004` 已于 2026-09-29 Accepted（**Phase 2 门禁解除**）；其余 3 条（`0002` / `0005` / `0006`）按各自 Gate 在对应 Phase 前 Accept 即可
5. ~~**Phase 1 规划缺口 7 项**~~ —— ✅ **已于 2026-09-21 全部修复**，见 §9.1 #10–#16 的“修复”列

> 细节见 §3。~~**在门禁 ADR 被 Accept 之前，Phase 1 及之后的实现不得开工**~~；**Phase 1 门禁已于 2026-09-28 解除**（`0001` / `0007` / `0003` 口径已 Accepted），Phase 1 可开工；**Phase 2 门禁已于 2026-09-29 解除**（`0004` 已 Accepted），Phase 2 待开工；Phase 0 已开始并完成核心交付。

---

## 2. 文档地图

| 文档 | 回答什么问题 | 什么时候读 | 事实源角色 |
|---|---|---|---|
| `README.md` | 目录索引、事实源声明、Phase 进度 | 首次进入本目录 | 索引 |
| `01_low_token_multilanguage_ai_agent_harness_design.md` | 架构意图（原则 / 分层 / 目标 / 非目标） | 理解"为什么做" | 架构意图 |
| `02_agent_harness_technical_design_spec_sqlite.md` | core schema 的 DDL（最全） | 写 store / 迁移时 | **core schema 唯一事实源** |
| `supplement/*`（`03` 为**拆分索引**） | 补充规格（稳定 ID / 项目模型 / 类型 / 名称解析 / LSP / 安全 / 评估 …） | 写 extension schema 时 | **extension schema 唯一事实源** |
| `supplement/05_runtime_integration_project_detection_and_lsp.md` | runtime 集成 / 项目类型感知 / LSP 接入；**§10 = LSP extension schema**（原 `03` §5） | Phase 0、Phase 4 前 | 集成规格（不复制 core DDL） |
| [`../lsp/`](../lsp/README.md) | LSP 实施方案：crush 参考分析 / runtime 集成设计 / 实施顺序与验收（A1–A11） | Phase 0 末、Phase 4 开工前 | 实施方案（**非**事实源） |
| `04_completeness_review_and_optimized_plan.md` | 完整性评审 + v1 方案 + Phase 路线 + 验收 | 实施与验收全程 | **落地计划与验收事实源** |
| `GLOSSARY.md` | 术语规范名 | 任何命名之前 | **术语唯一事实源** |
| `CHANGELOG.md` | 变更历史 | 改动前后 | 变更历史 |
| `adr/README.md` + `adr/000x-*.md` | 决策及其 Gate | **动手前** | **决策唯一事实源** |
| **本文 `06`** | 实施顺序 / 门禁 / 文件落点 / 验收入口 | 每次开工前 | 实施索引（**非**事实源） |

**读者路径**

- 新人：`README.md` → `04` §0 TL;DR → `01` 架构意图 → `GLOSSARY.md`
- 实施者：**本文 §3 → §4 → §5** → `04` §3 / §4 / §5 → `02`（注意顶部 schema 应用顺序标注）→ `supplement/01`、`supplement/05`
- 评审者：`04` §2 / §6 / 附录 B → 对照 `01` / `02` 与 `supplement/*`（原 `03`）原文 → 在 `adr/` 记录裁决

---

## 3. ADR 门禁表（开工前必读）

> **决策唯一事实源是 [`adr/`](adr/README.md)。** `04` 附录 B 与 `supplement/05` §9 是历史散文列表，**不是决策依据**。
> 只有项目 owner 能把 `Proposed` 改为 `Accepted`；Accepted 后正文不可改，要改就写新 ADR 并在 `Supersedes` 引用旧号。

| ADR | 主题 | Gate | 阻塞 | 状态 | 可逆性 |
|---|---|---|---|---|---|
| [0001](adr/0001-project-module-language-schema.md) | Project/Module/Language 模型收敛 | **Phase1-start** | Phase 1 | **Accepted**（2026-09-28） | expensive（当前零迁移成本） |
| [0002](adr/0002-acp-lsp-ownership.md) | ACP 下 LSP 归属与能力面 | Phase4-start | Phase 4 | Proposed | cheap |
| [0003](adr/0003-exploration-attribution-metrics.md) | 探索归因与 shadow 差异率度量 | Phase1-start（口径）/ **Phase1-shadow**（阈值） | Phase 1 口径；阈值待 shadow | **Accepted**（2026-09-28，口径） | cheap |
| [0004](adr/0004-stale-index-tool-surface.md) | 陈旧索引下的 `code.*` 工具面 | Phase2-start | Phase 2 | **Accepted**（2026-09-29，owner 授权代改） | cheap |
| [0005](adr/0005-windows-child-process-lifecycle.md) | Windows 子进程树生命周期 | Phase4-start | Phase 4 | Proposed | moderate |
| [0006](adr/0006-lsp-position-encoding-boundary.md) | LSP 位置编码转换边界与缓存键 | Phase4-start | Phase 4 | Proposed | moderate |
| [0007](adr/0007-phantom-tables-and-doc-invariants.md) | 幽灵表清理与文档不变量 | **Phase1-start** | Phase 1 | **Accepted**（2026-09-28） | cheap |
| [0008](adr/0008-grep-coverage-file-level.md) | grep 通道探索归因采用 file-level 覆盖口径 | **Phase1-shadow** | Phase 1 阈值 | **Accepted**（2026-09-29） | cheap（仅测量） |
| [0009](adr/0009-v1-table-set-scope.md) | v1 表集口径裁决（上限定义域、三分组与命名规范） | **Phase1-start** | 文档不变量 I1/I4/I5 | **Accepted**（2026-09-29） | cheap |

**建议的 Accept 顺序**：owner 先集中处理 `0001` + `0007` + `0003`（三条都是 Phase 1 门禁）。`0003` 的 Accept **不再被阈值阻塞**——口径部分按 `Phase1-start` 生效，阈值部分标注为 `Phase1-shadow` 产出（2026-09-21 修订；原 Gate `Phase0-baseline` 结构性不可达）。`0002` / `0005` / `0006` 在对应 Phase 前处理即可（`0004` 已于 2026-09-29 Accepted，见下）。

> ✅ **已于 2026-09-28 执行**：`0001` / `0007` / `0003`（口径）已由 `Proposed` 改为 `Accepted`，Phase 1 门禁解除。

> ✅ **2026-09-29 追加**：`0008` / `0009` 已由 `Proposed` 改为 `Accepted`（owner 授权代改）；`0008` 阈值定稿 α=0.8、Phase 1 门槛 M1 ≥ 0.31，主门槛复核通过；`0009` 落地后不变量检查器 5/5 PASS（`CHANGELOG.md` 同日条目）。

> ✅ **2026-09-29 追加（Phase 2 门禁）**：`0004` 已由 `Proposed` 改为 `Accepted`（owner 授权代改，先例 `0008` / `0009`）——陈旧索引下 `code.*` 采用**按陈旧度分级注册**（关系类陈旧时不注册，杜绝静默错误）；`S_fresh` / `S_max` 为初始值（60s / 15min），实际取值 Gate = `Phase2-start` 且**不阻塞 Accept**。**P2 门禁解除，Phase 2 待开工。**

> **注意**：ADR-0005 表面是"Job Object 还是 `taskkill /T` 二选一"，但**答案已存在于仓库代码**——`internal/executor/process_guard_windows.go` 已以 Job Object（`KILL_ON_JOB_CLOSE`）为主、`taskkill /T /F` 为降级。ADR-0005 的实质是"复用既有守卫"。

**仍未转 ADR 的条目**（不构成决策依据）：`04` 附录 B 其余条目、`supplement/05` §9 之外的散文项。转 ADR 的流程见 `adr/README.md` §8。

---

## 4. 分阶段实施指引

> 每 Phase 的**完整**交付 / 验收 / 回滚原文在 `04` §5；本节是实施视角的压缩索引。
> **排期铁律**（`04` §5）：① 每 Phase 必须能独立上线、独立回滚、独立测量；② 前一 Phase 验收未通过，不得进入下一 Phase；③ 任何 Phase 都不得改变 `knowledge.mode=off` 时的行为；④ 每 Phase 结束必须更新 `04` 的状态列与 `README.md`。

### Phase 0 — 基线与契约（建议 1 个迭代）

- **目标**：先能测量，再谈优化；冻结 v1 schema 与接口。
- **前置 ADR**：ADR-0003 的**口径部分**（表结构 + 判定规则）需在 Phase 0 落地；**阈值部分**（α 与 Phase 1 门槛数值）Gate = `Phase1-shadow`，Phase 0 不定（2026-09-21 修订；原写“Phase 0 基线跑完后定”，结构性不可达）。
- **交付**
  1. `knowledge.mode = "off"` 为默认值；知识层代码可存在但完全不参与任何路径。
  2. `usageledger` 扩展 9 个字段：`exploration_tokens`、`reuse_tokens`、`index_lookup_count`、`index_hit`、`fallback_count`、`unsafe_reuse_count`、`tool_calls_per_task`、`repeated_read_count`、`knowledge_version_mismatch_count`。
  3. 基线报告：本仓库（排除 `node_modules` / `dist` / `.aicli`）+ 1 个外部 Go 仓库，跑 5–10 个代表任务，记录 token 构成、工具调用数、重复读取次数、p95 延迟。
  4. v1 DDL（`04` §4.3）+ `schema_migrations` + 迁移脚本骨架（接入 `internal/migrate`）。
  5. 与相邻计划的交叉评审（见 §8；§8 表列 5 行）。
  6. `exploration_attribution` 表（ADR-0003 §4.1）：追加到 `usageledger` `init()` 的 statements 切片 + 两个索引；**只建表与埋点骨架，不产生数据**（`mode=off` 下无 shadow 调用）。2026-09-21 补入归属。
- **文件落点**：新增 `backend/internal/knowledge/{models,config,version,store,telemetry}.go`、`migrations/0001_init.sql`；修改 `backend/internal/usageledger/sqlite_store.go`（含 `exploration_attribution` 建表，不新增文件）、`backend/internal/usageanalytics/*`、`backend/internal/sqliteutil/sqliteutil.go`、`backend/internal/migrate/*`、`backend/configs/*.yaml`；文档治理 `00` / `01` / `02` / `03`。
- **验收门槛**：能回答“每个任务平均多少 token 花在探索 / 重复读取”，且数字可由 ledger **复算**（同一份数据两次计算结果一致）；`mode=off` 下全量回归与改动前一致；schema 能被 `sqliteutil.OpenFileCtx` 打开，无 `database is locked`、无 `PRAGMA` 报错；`exploration_attribution` 可被 `sqliteutil.OpenFileCtx` 打开且重复 init 幂等、不重复建表（ADR-0003 §8）。
- **回滚**：删除 knowledge 包与配置项，零行为影响。
- **状态**：**核心 5 交付已完成**（2026-09-20）——`mode=off` 默认、usageledger 9 归因字段、v1 DDL + 迁移骨架、基线报告（3 个仓库 + 5 真实任务）、相邻计划交叉评审。A/B（off vs shadow）明确延期至 Phase 1（`cmd/aicli` 当时未接入 `knowledge.Open`，见 `reports/phase0_baseline_report.md` §6；**2026-09-28 已接入**，见 §4 Phase 1 交付 6）。文档治理尾项（§9 条目 8 / 9）已于 2026-09-28 完成（见 §9.3），属维护不影响验收。
- **Phase 1 进入条件**（见 §3）：ADR-0001 + ADR-0007 需 owner Accept；ADR-0003 阈值（Phase 0-baseline gate）待 `04` §7.6 用本报告校准后落稿。**准备就绪的校准建议**（3 个仓库样本，n=3）：首次全量 ≤ 0.6ms×refs 且 ≤ 300s；DB ≤ 1KiB×refs 且 ≤ 300MB；两条均附 `code.status` 的 `(files, refs, bytes)` 三元组。详见 `reports/phase0_baseline_report.md` §5。
  1. ✅ `knowledge.mode = "off"` 默认值 —— `knowledge/config.go` + `configs/*.yaml`，用例 `knowledge_config_test.go`。
  2. ✅ `usageledger` 9 个归因字段 —— `sqlite_store.go` 幂等补列（旧库兼容）+ `entity.TokenUsageHistory` + `knowledge/telemetry.go` 采集器。
  3. ✅ 基线报告 —— **索引侧已完成**（[`reports/phase0_baseline_report.md`](reports/phase0_baseline_report.md)：本仓库 3860 文件 / 146.9s / 247.5 MiB / 覆盖率 100%；外部 gin 99 文件、prometheus 1010 文件，见报告 §2.4）；**任务侧 5 个真实任务已跑**（mode=off，7 条 LLM 记录，详见报告 §6）；A/B（off vs shadow）**延期**至 Phase 1（aicli 当时未接入 `knowledge.Open`，shadow 为 no-op，故无法在 aicli 测量，待 Phase 1 接入后补跑；**2026-09-28 已接入**，见 §4 Phase 1 交付 6）。
  4. ✅ v1 DDL + `schema_migrations` + 迁移骨架 —— `knowledge/migrations/0001_init.sql`（`internal/migrate/*` 无需改动）。
  5. ✅ 与相邻计划的交叉评审 —— 完成，见 [`reports/phase0_cross_review.md`](reports/phase0_cross_review.md)。结论：4 份计划均无实现层冲突（composer 计划已显式把"内容检索/索引"划给本方案）；发现 1 处**命名撞车**（`cache_entries`，见 §8 已修）与 1 处**跨文档 schema 命名漂移**（`refs`/`references`、`symbols_fts`/`symbol_fts`，属 ADR-0001/0007 的 Phase 1 硬门禁）。

  实测副产物：修掉两个会让索引"少干活却看起来达标"的缺陷——`stable_key` 缺 `namespace`（35% 文件的符号与引用整份丢失）与**局部变量被当成符号**（5080 行身份合并；builtin/3 起降到 732 行）。见 `CHANGELOG.md` 2026-09-20 两条与报告 §4.1 / §4.4。
  门槛预判：Phase 1 的"首次全量 ≤ 120s""DB ≤ 200MB"两条**初值已被本仓库实测击穿**（146.9s / 247.5 MiB）；3 个仓库对照（报告 §2.4）显示成本应按"每 ref"表达，§5 建议改为"≤ 0.6ms × refs 且 ≤ 300s""≤ 1 KiB × refs 且 ≤ 300MB"，定稿需 `04` §7.4 评审。

### Phase 1 — 索引 MVP（只读，影子模式）

- **目标**：持久化 file / symbol / refs 轻索引 + FTS5；不改变任何模型可见行为。
- **前置 ADR**：**ADR-0001**、**ADR-0007**、**ADR-0003**（口径）——均 `Phase1-start`。
- **交付**
  1. `knowledge/index`：从 `workspace/scanner.go` 升级。保留正则作为 builtin adapter；补 Java / Rust / C++ 粗符号（`class` / `func` / `fn` / `struct` / `interface` 级别）；**修正测试文件被忽略的问题**——改为索引并写 `files.is_test=1` / `symbols.is_test=1`，`ignorePatterns` 中的 `.*\.test\.(go|py|js|ts)$` 与 `^_\w+` 必须移除或改为标记，否则 `code.tests` 与影响面分析永久为空；输出 `content_hash`、`is_generated`、`language`、`size`、`mtime_ns`。
  2. `knowledge/store`：`04` §4.3 的 v1 表 + `symbols_fts` 同步触发器。
  3. 增量：仅 `content_hash` 变化才重解析；删除文件标记 `deleted_at`，不立即物理删除。
  4. `knowledge.mode=shadow`：在**既有 `grep` / `view` 的执行路径上拦截**（ADR-0003 §4.3 拦截范围 / §4.4 候选查询映射），索引侧同时算候选结果，**仍返回原结果**，逐调用对比写入 `exploration_attribution`（不写 `invalidation_events`：该表只承载变更源类事件，reason 闭集见 `04` §4.3；2026-09-29 口径修正）。**Phase 1 不新增工具**——`code.search` 是 Phase 3 交付（见 §4 Phase 3）；原表述误用 Phase 3 产物定义 Phase 1 shadow。**2026-09-28 已落地**：观察器 `internal/knowledge/shadow.go`（`ShadowObserver` / `ShadowObserverFor`：只读 `Search` / `FindSymbols` 算候选 + 旁路落库，失败只 debug 不冒泡，隐私只落 `query_hash`）；`agent.LoopReActConfig.OnToolObserved` 在 MCP 分支与并行批次出口上报最终结果（`internal/agent/loop.go`）；三入口接线（aicli cmd/tui `applyLocalChatToolObservation`、acp `attachSessionKnowledge`、runtime-server `SetKnowledgeShadow` + `applyAPISessionToolObservation`）。
  5. `knowledge.status` CLI / HTTP：索引状态、文件数、符号数、DB 大小、最近 job、锁等待 p95。
  6. **接入（激活）**：`knowledge.Open` 接入 `cmd/runtime-server`（启动阶段调用 + 向 `internal/background` 注册索引任务，默认 writer owner）、`cmd/aicli` cmd/tui（`commands/chat.go` 解析 workspace 后调用）、`cmd/aicli` acp。规格见 `supplement/05` §2 / §8。**没有这一项，Phase 1 的 shadow 没有任何进程会打开知识层，验收无法进行**（2026-09-21 补入归属；本次 A/B 延期即此因）。**2026-09-28 实现偏离**：`internal/background` 只有 shell 作业通道（`SubmitShell`），无进程内任务注册口；首次全量索引由 `knowledge.Activate` 内部 goroutine 承担（见 `CHANGELOG.md`）。
- **文件落点**：新增 `knowledge/store_sqlite.go`（读路径在 `store_sqlite_read.go`、FTS 在 `store_sqlite_fts.go`）、`indexer.go`、`adapter_builtin.go`、`owner.go`、`activation.go`、`shadow.go`、`jobs.go`、`lockwait.go`、`status.go`、`migrations/0001_init.sql` / `0002_file_soft_delete.sql` 及配套 `*_test.go`（2026-09-29 校正：原预测的 `indexer_light.go` / `query.go` / `knowledge_test.go` 未单独落盘）；`workspace/*` 未改动（轻索引自带 walker，测试文件按 `is_test` 标记）；修改 `sqliteutil`、`events` / `runtimeevents`；**接入修改 `cmd/runtime-server/main.go`、`cmd/aicli/commands/chat.go` 及其 acp 入口**（2026-09-21 补入）。**交付 4 落点（2026-09-28）**：新增 `knowledge/shadow.go`、`knowledge/shadow_test.go`、`runtimeapi/knowledge_shadow_wiring_test.go`、`agent/loop_observe_test.go`、`usageledger/sqlite_store_exploration_attribution_write_test.go`；修改 `usageledger/sqlite_store.go`（`AppendExplorationAttribution`）、`agent/loop.go`（`OnToolObserved`）、`runtimeapi/{handler,session_runtime_support}.go`、`cmd/runtime-server/{main,knowledge_boot}.go`、`cmd/aicli/commands/{chat_actor_host,agent_stdio}.go`。**交付 5 落点（2026-09-28）**：新增 `knowledge/{jobs,lockwait,status}.go`、`runtimeapi/knowledge_handlers.go`、`cmd/aicli/commands/knowledge.go` 及对应测试（`knowledge/status_test.go`、`sqliteutil/lock_wait_observe_test.go`、`runtimeapi/knowledge_status_handler_test.go`、`cmd/aicli/commands/knowledge_status_test.go`）；修改 `knowledge/{activation,config,indexer,store,store_sqlite}.go`、`sqliteutil/sqliteutil.go`、`runtimeapi/handler.go`、`cmd/runtime-server/main.go`、`cmd/aicli/main.go`。
- **验收门槛**（2026-09-21 修订：**主门槛与诊断指标分离**）：**主门槛 = ADR-0003 §4.5 的 M1 调用级可用率**——`baseline_n > 0` 的被拦截调用上 `usable = (coverage ≥ α) AND (economy ≤ 1.0)` 的均值达标；**α 由本 Phase 的 shadow 实测校准**（Gate = `Phase1-shadow`）；**诊断指标（不判 Pass/Fail，用于定位失败）** = M2 覆盖度 / M3 经济性 / M4 token 收益、以及 `code.search` 与 `grep` 的 top-10 文件集合差异率 < 15%（原为验收口径，现降为诊断，消除与 ADR-0003 §4.5 的双口径冲突）；本仓库首次全量索引 ≤ 实测基线（先测后定，初值 ≤ 120s）；单文件增量 < 50ms；DB ≤ 200MB；`files.content_hash` 与磁盘一致率 100%（抽样 ≥ 200 文件）；锁等待 p95 < 50ms；**接入验证**：三入口 `mode=off` 行为与改动前一致、`mode=shadow` 有数据落库且 M1 可复算。**2026-09-29 实测**：全量 ≤360 s（§7.4）；单文件增量 **Fail**（marginal p95 302 ms）；DB 313.9 MiB；content_hash 261/261；锁抽样 0 样本（报告 §4.6/§4.7）。
- **回滚**：`mode=off` + 删除 `knowledge.db`。
- **状态**：**进行中**（2026-09-28 开工）。交付 1–3 的代码已在 `internal/knowledge`；**交付 6「接入（激活）」已完成**（三入口）；**交付 4（shadow 拦截 `grep` / `view`）已完成**（2026-09-28：三入口接线 + `exploration_attribution` 落库，见 `CHANGELOG.md`）；**交付 5（`knowledge.status`）已完成**（2026-09-28：CLI `aicli knowledge status` + HTTP `GET /api/runtime/knowledge/status`，见 `CHANGELOG.md`）；**2026-09-29 收口交付 1/3**——Java/C++ 粗符号与文件软删除对账（`files.deleted_at` / `MarkFilesDeleted` / 复活）已落地，交付 1–6 全部完成（见 `CHANGELOG.md`）。**2026-09-29 `Phase1-shadow` 实测 v1 已执行**（真实调用重放 n=400：M1=20.81 %、M2=24.15 %、M4=75.4 %；view 48.4 % / grep 行级 0.47 %（grep file-level 对照 mean 31.83 %、answerable 49.4 %））——主门槛不通过，瓶颈为行级口径（ADR-0003 §6.2），需新 ADR 裁决（file-level 或 per-file 归因提前）；**2026-09-29 三个入口 live 验证通过**（aicli cmd+tui + ACP + runtime-server，见报告 §4.5）；**2026-09-29 抽样补齐** content_hash 一致率 261/261 = 100 %、锁等待 4 写者 0 样本 / 0 重试失败（报告 §4.6，含 `index_jobs.id` 并发碰撞修复）；**2026-09-29 口径裁决后复核（ADR-0008 Accepted + α=0.8）：grep file-level 26.76 %、view 48.41 %、合并 M1=35.95 % ≥ 0.31 → 主门槛通过**（P2 进入条件 = ADR-0004 Accept，**已于 2026-09-29 满足**）；剩余工程项见 `reports/phase1_shadow_report.md` §5（新列实现、grep 映射收敛、单文件增量口径重议）。**2026-09-29 再收口**：性能 §7.4 校准（≤360 s、跨仓 38.9–43.8 ms/文件）+ 单文件增量实测 Fail（marginal p95 302 ms）+ 多 `paths` 作用域落地（报告 §5 第 2 项部分）。

### Phase 2 — Exploration Memory + Context Planner

- **目标**：解决"多轮重复探索"，这是 `00` / `01` 共同认定的最高 ROI。
- **前置 ADR**：ADR-0004 ✅ **Accepted（2026-09-29）**；Phase 1 主门槛通过（剩余工程项见 [`reports/phase1_shadow_report.md`](reports/phase1_shadow_report.md) §5，不构成 ADR 门禁）。
- **交付**
  1. `exploration_sessions` / `nodes` / `edges` 落库；节点带 `knowledge_version`。
  2. `memorystore`（长期笔记，不自动写入）与 exploration memory（任务工作集，自动写入）分层；两者在 `context_items` 中用 `item_type` 区分。
  3. `contextmgr` 新增 `KnowledgeMode = off | signals | broad`，与 `WorkspaceMode` / `RecallMode` 对称；`Strategy` 增加 `MinKnowledgeQueryLength`、`ReuseConfidenceFloor`。
  4. `knowledge.Planner`：输出 `Plan{Reuse, Explore, Degraded, Reason}`；复用需满足 `04` §4.4 阈值。
  5. 复用必须做一次验证读取（confidence < 0.90 时）。
  6. 多 Agent 语义：探索节点写 `session_id + task_id + workspace_id`；只读子代理不写索引、不写 exploration memory；跨任务复用阈值 ≥ 0.90。
- **验收门槛**：多轮任务重复工具调用次数下降 ≥ 30%（用 ledger 归因，样本 ≥ 20，且给出置信区间）；`unsafe_reuse_count = 0`；端到端 p95 延迟增幅 ≤ 10%；关闭 `KnowledgeMode` 后指标回到基线（可逆）。
- **回滚**：`KnowledgeMode=off`。
- **状态**：**规划完成（2026-09-29），待开工**（门禁已解除：ADR-0004 Accepted；开工规划见本小节「Phase 2 开工规划（2026-09-29）」，最小首片 S1 可在下一轮直接开工）。

### Phase 2 开工规划（2026-09-29）

> 本节是 Phase 2 的**实施编排**（工作流 / 文件落点 / 测试 / 验收映射 / 依赖与风险）；交付内容与验收门槛仍以 `04` §5 Phase 2 为准，决策以 `adr/*` 为准。落档前已检索确认：`docs/knowledge_Layer/` 无既有 Phase 2 开工规划（无重复覆盖）。

**门槛编号**（下文"验收映射"引用；原文见本节上方"验收门槛"）：

| 编号 | 门槛 |
|---|---|
| G1 | 多轮任务重复工具调用次数下降 ≥ 30%（ledger 归因，样本 ≥ 20 个任务，附置信区间） |
| G2 | `unsafe_reuse_count = 0` |
| G3 | 端到端 p95 延迟增幅 ≤ 10% |
| G4 | 关闭 `KnowledgeMode` 后指标回到基线（可逆） |

**现状核对（2026-09-29，只读）**：

- `exploration_sessions` / `exploration_nodes` / `exploration_edges` **建表已存在**（`backend/internal/knowledge/migrations/0001_init.sql` L126–163；节点含 `knowledge_version` 列），但 `Store` 接口与 `backend/internal/knowledge/*` **没有任何读写代码**——Phase 2 交付 1 的剩余部分是 Go 契约与写入路径，不是 DDL。
- `backend/internal/knowledge` 现有：`store.go`（窄接口）/ `store_sqlite*.go`（读写分文件）/ `activation.go`（三入口共用接入原语）/ `shadow.go`（Phase 1 观测器）/ `telemetry.go`（`UnsafeReuseCount` / `RepeatedReadCount` 等计数位与 `SafetyViolations()`）。
- `backend/internal/contextmgr/manager.go`：`Strategy` / `Manager` **无** `KnowledgeMode`、`Knowledge` 字段（现有 `RecallMode` / `WorkspaceMode` 可作对称样板）；`backend/internal/contextpack/context_pack.go` 已有 `Provider` 接口；`backend/internal/memorystore/store.go`（`notes.jsonl` 长期笔记，不自动写入）已存在。
- `agent.LoopReActConfig.OnToolObserved`（`backend/internal/agent/loop.go`）与三入口接线（`backend/internal/runtimeapi/session_runtime_support.go`、`backend/cmd/aicli/commands/{chat_actor_host,agent_stdio}.go`、`backend/cmd/runtime-server/main.go`）已在 Phase 1 落地，可直接复用为探索记忆采集通道。
- **规划发现的缺口**：① 工作区级 `knowledge_version`（`H(workspace, file hashes, adapter versions, schema version)`）**无生成器**（`version.go` 只有 DB 契约常量 `KnowledgeVersion=1`）；② ADR-0008 的 `baseline_files_n` / `overlap_files_n` 新列与三入口 live 写入**仍未实现**（列为 W0）。

**工作流拆分（按依赖排序）**：

| 工作流 | 目标（一句话） | 前置 | 验收映射 |
|---|---|---|---|
| W0 | ADR-0008 收口：新列 + 三入口 live 写入（Phase 1 遗留项，非 Phase 2 交付） | 无 | Phase 1 接入验证；为 W7 供 file-level 诊断 |
| W1 | 探索记忆持久化层：DTO + store 读写 + `knowledge_version` 生成器 | 无 | G1 数据底座 |
| W2 | 自动采集（Recorder）与三入口接线 | W1；W0（同批文件） | G1 / G2 / G3 |
| W3 | 置信度 / 版本 / 阈值（Reuse Gate） | W1 | G2 |
| W4 | `knowledge.Planner`（`Plan{Reuse, Explore, Degraded, Reason}`） | W1、W3 | G1 / G2 |
| W5 | `contextmgr` 集成（`KnowledgeMode` + `Strategy` + 注入） | W4 | G1 / G3 / G4 |
| W6 | 多 Agent 语义与写入门禁 | W2 | G2 |
| W7 | 验收测量与报告（A/B ≥ 20 任务 + 复算） | W2–W6（+W0） | G1–G4 |

#### W0 — ADR-0008 收口：新列 + live 写入（Phase 1 遗留项）

- **目标**：为 `exploration_attribution` 追加 `baseline_files_n` / `overlap_files_n`（additive 列，缺省 0），在 shadow 观测与三入口 live 路径写入，满足 ADR-0008 §8.1 / §10 的两项 Open（"新列实现" + "三入口 live 写入"）。
- **文件落点**：
  - 修改 `backend/internal/usageledger/sqlite_store.go`（`statements` 追加两列 + `AppendExplorationAttribution` + 读路径）；`backend/internal/model/entity/exploration_attribution.go`（追加两字段）。
  - 修改 `backend/internal/knowledge/shadow.go`（写入侧填 file-level 值；`ObservationDetail` 已有两侧文件集合）与 `backend/internal/knowledge/attribution.go`（file-level M1 复算；行级列保留为诊断）。
  - 修改三入口接线：`backend/internal/runtimeapi/session_runtime_support.go`、`backend/cmd/aicli/commands/{chat_actor_host,agent_stdio}.go`、`backend/cmd/runtime-server/main.go`。
- **测试清单**：`backend/internal/usageledger/sqlite_store_exploration_attribution_test.go`（D4：历史行 `SUM/COUNT/AVG` 不变、补列幂等）；`backend/internal/knowledge/shadow_test.go` / `shadow_replay_test.go`（file-level 指标与 `reports/phase1_shadow_report.md` §4.3 逐位一致，±1e-9）；三入口 live 写入回归（接入验证）。
- **验收映射**：ADR-0008 §8.1 的"三入口 live 数据须写入新列"（Phase 1 验收项）；为 W7 提供 file-level 反例诊断。
- **前置**：无（ADR-0008 已于 2026-09-29 Accepted）；**建议先于 W2/W3 落盘**（见本节末"与 ADR-0008 配套项的先后关系"）。
- **风险**：补列必须 additive 且幂等（旧库兼容）；与并行写者在 `usageledger/sqlite_store.go` 的改动冲突；live 写入需要真实会话 × 三入口，成本高于单测。

#### W1 — 探索记忆持久化层（DTO + store 读写 + `knowledge_version` 生成器）

- **目标**：把三张探索记忆表变成可读写的包内契约；节点写 `knowledge_version`；提供"任务工作集"（`workspace_id + task_id`）与跨任务（`target`）两条查询路径；沿用 Phase 1 的单写者 / 只读降级语义（reader 写入硬失败，不静默降级）。
- **文件落点**：
  - 新增 `backend/internal/knowledge/exploration.go`（DTO：`ExplorationSession` / `ExplorationNode` / `ExplorationEdge`；`NodeType`（file|symbol|query|answer）与 `EdgeType`（calls|references|contains|derived_from）枚举；scope 校验；ID 由既有 `digest` 派生）。
  - 新增 `backend/internal/knowledge/store_sqlite_exploration.go`（写路径：`UpsertExplorationSession`、`AppendExplorationNode`（同 target 幂等：`use_count+1`、刷新 `last_used_at`）、`AppendExplorationEdge`、`TouchExplorationNode`；全部经 `execWrite`）。
  - 新增 `backend/internal/knowledge/store_sqlite_exploration_read.go`（读路径：`LookupExplorationNodes`（workspace/task/target/type/limit）、`LatestExplorationSession`；只读句柄可用）。
  - 修改 `backend/internal/knowledge/store.go`（仅追加方法、不改既有签名）。
  - 新增 `backend/internal/knowledge/version_hash.go`（补齐缺口：`WorkspaceVersion(ctx, store, workspaceID)` = 文件 `content_hash` 集合 + `AdapterVersion` + schema 版本的稳定哈希；Phase 5 版本向量复用同一函数）。
  - 迁移：**默认不加**（0001 现有 `idx_explore_sessions` / `idx_explore_nodes_target` / `idx_explore_edges_from` 已覆盖按 `exploration_id` 前缀的读写）；仅当 `EXPLAIN QUERY PLAN` 显示跨任务查询全扫时，新增 additive `backend/internal/knowledge/migrations/0003_exploration_lookup.sql`（只 `CREATE INDEX IF NOT EXISTS`，**不新增表**，不触碰 ADR-0009 表集口径）。
- **测试清单**：新增 `exploration_test.go`（枚举/作用域校验、ID 稳定性、`WorkspaceVersion` 对文件哈希 / adapter / schema 变化的敏感性与同输入幂等）；新增 `store_sqlite_exploration_test.go`（写入幂等、`use_count`/`last_used_at`、`knowledge_version` 必填、`ON DELETE CASCADE`、reader 只读返回 `ErrReadOnlyStore`、workspace 隔离、重复 init 幂等）；新增 `store_sqlite_exploration_read_test.go`（task/target/type 过滤、limit 默认、排序稳定）。
- **验收映射**：G1 的数据底座（"重复读取哪一 file/symbol"的归因来源）；Phase 2 交付 1 的剩余部分；G2 需要的 `knowledge_version` 字段。
- **前置**：无（表已在 0001，S1 可直接开工）。
- **风险**：① 每工具调用写行会放大 DB 体积 → 同 target 去重 + 每任务节点上限；② 跨任务查询依赖 `sessions` join，慢则按实测补索引；③ 多进程写沿用 owner 仲裁，不新增机制。

#### W2 — 自动采集（Recorder）与三入口接线

- **目标**：复用既有 `OnToolObserved` 只读钩子，把 `grep` / `view` 的最终结果写入探索记忆（任务工作集，**自动写入**，与 `memorystore` 的人工长期笔记分层）；节点写 `session_id + task_id + workspace_id`；契约与 Phase 1 shadow 一致（尽力而为、不得修改结果、失败不冒泡），`mode=off` 零写入。
- **文件落点**：
  - 新增 `backend/internal/knowledge/exploration_recorder.go`（`ObservedCall` → session/node/edge 映射；隐私只落 `query_hash`，复用 shadow 口径；同 target 合并；采样/批量；`nil`-safe）。
  - 修改 `backend/internal/knowledge/activation.go`（`Activation.Recorder()`：仅 `mode=shadow|on` 且 owner 时非 nil；off/reader 返回 nil）。
  - 修改 `backend/internal/runtimeapi/{handler.go,session_runtime_support.go}`（`applyAPISessionToolObservation` 同链路调用 Recorder，注入 session/task/turn）。
  - 修改 `backend/cmd/aicli/commands/{chat_actor_host,agent_stdio}.go`（`applyLocalChatToolObservation` / `attachSessionKnowledge` 同链路）。
  - 修改 `backend/cmd/runtime-server/{main.go,knowledge_boot.go}`（装配 Recorder）。
- **测试清单**：`backend/internal/knowledge/exploration_recorder_test.go`（映射、合并、上限、错误不冒泡、nil-safe）；`backend/internal/runtimeapi/exploration_wiring_test.go`（off 零写入、未接线不 panic；可仿 `knowledge_shadow_wiring_test.go`）；`backend/internal/agent/loop_observe_test.go` 扩展（Recorder 不改变结果/错误路径）。
- **验收映射**：G1（复用下降的数据来源）；G2（写入必须带 confidence 与 version）；G3（写入开销必须落在预算内，异步化）。
- **前置**：W1；**W0**（同批接线文件与 `usageledger`，避免二次改动/合并冲突）。
- **风险**：① 高频写入推高 p95 → 异步 + 批量，`max_nodes_per_task` / `max_edges_per_task` 上限；② 部分入口 `task_id` 为空 → 回退 `session_id + turn_id` 并显式标注，不编造 task 语义；③ shadow 下写 memory 是否可接受 → 明确"写 memory **不注入、不改变模型可见行为**"，与 ADR-0003 的 shadow 定义相容；off 必须零写入（硬约束）。

#### W3 — 置信度 / 版本 / 阈值（Reuse Gate）

- **目标**：实现 `04` §4.4 的可执行 confidence（`source_weight × agreement × (1-staleness) × (1-ambiguity)`；`knowledge_version` 不匹配 → 直接不可用）并落地保守默认阈值：同任务 ≥ 0.80（涉及写操作 ≥ 0.90）、跨任务 ≥ 0.90、0.50–0.80 复用但标记待验证、< 0.50 触发探索。
- **文件落点**：新增 `backend/internal/knowledge/confidence.go` + `confidence_test.go`；修改 `backend/internal/knowledge/config.go`（`Planner` 配置：`min_reuse_confidence` / `write_reuse_confidence` / `cross_task_confidence` / `verify_read_below`，含 `Normalize` 默认值）与 `backend/configs/*.yaml`（`knowledge.planner.*` 模板，注释标注"初值待 Phase 2 实测校准"）。
- **测试清单**：表驱动覆盖 source_weight（0.95/0.90/0.80/0.65/0.55/0.40）、agreement（1.0/0.9/0.7）、staleness（0.5/0.3/0.3/版本不匹配）、ambiguity（0.3/0.1）；阈值边界（== 阈值视为通过）；config 归一化、YAML 往返、`< 0.90` 时验证读取标志。
- **验收映射**：G2（`unsafe_reuse_count=0` 的唯一判定基础）；G1（阈值属 `04` §7.6 的 Phase 2 复校准项）。
- **前置**：W1（节点带 version）。
- **风险**：confidence 是新量化口径，默认值只是初值 → 验收前必须用实测回写；不得与 `contextmgr` 的 `trust`（来源可信性）混用。

#### W4 — `knowledge.Planner`

- **目标**：实现 `Planner.Plan(ctx, PlanInput) (Plan, error)`，输出 `Plan{Reuse, Explore, Degraded, Reason}`；`Reuse` 项带 `confidence` + `knowledge_version`；`confidence < 0.90` 时必须安排一次验证读取（用既有 `grep` / `view` 实现，**不依赖 Phase 3 的 `code.*`**）。
- **文件落点**：新增 `backend/internal/knowledge/planner.go` + `planner_test.go`；修改 `backend/internal/knowledge/knowledge.go`（`Layer.Plan`：nil/off 返回空 Plan，不 panic）。
- **测试清单**：决策矩阵（off / reader / 索引不可用 / 无命中 / 低置信 / 跨任务 / 版本不匹配 / 写操作 / 查询长度 < `MinKnowledgeQueryLength`）；验证读取标记；`Degraded=true` 且不冒泡错误；同一输入两次 Plan 结果一致（可复算）。
- **验收映射**：G1（复用替代重复探索）；G2（Planner 是认知层唯一复用出口，`unsafe_reuse` 从它归零）。
- **前置**：W1 + W3；W2 只影响真实数据、不阻塞单测。
- **风险**：Planner 只产出决策、不执行；进入 prompt 的一端在 W5 必须做二次 stale/version 过滤（为 Phase 6 的 `stale_item_injected=0` 留扣）。

#### W5 — `contextmgr` 集成（`KnowledgeMode` + `Strategy` + 注入）

- **目标**：`contextmgr` 新增 `KnowledgeMode = off | signals | broad`（与 `WorkspaceMode` / `RecallMode` 对称）；`Strategy` 增 `MinKnowledgeQueryLength`、`ReuseConfidenceFloor`；`on` 下按档位把 Planner 的 `Reuse` 项装配进上下文（`signals` 只注入摘要/信号，`broad` 注入条目）；`off` 零调用、零注入，行为与改动前逐字节一致。
- **文件落点**：修改 `backend/internal/contextmgr/manager.go`（常量、`Strategy`、`Manager.Knowledge`、`BuildInput` 透传、`StrategyForProfile` / `ResolveStrategy` 默认与覆盖）；新增 `backend/internal/contextmgr/knowledge.go`（`Reuse` → 消息/metadata；token 预算内截断；条目带 `item_type=exploration`、`source=memory`、`knowledge_version`、`confidence`、`reason`）；新增 `backend/internal/contextmgr/knowledge_test.go`；修改 `backend/internal/contextmgr/manager_test.go`（off 等于基线）。
- **测试清单**：off / signals / broad 三档；fake Planner 断言 **off 下零调用**；预算截断与最小查询长度；`ReuseConfidenceFloor` 覆盖；`Strategy` 归一化与 profile 默认；注入集合不含 stale / 版本不匹配项。
- **验收映射**：G4（回到基线的主承载：`KnowledgeMode=off`）；G1（注入替代表述减少探索）；G3（注入受 token 预算约束）。
- **前置**：W4。
- **风险**：与 Phase 6 的 `contextpack knowledge.Provider` / `LayerPlan` knowledge 层 / `context_items` 落库边界重叠 → 本 Phase 只做 `contextmgr` 内注入与 metadata；`context_items` 的**写入路径留给 Phase 6**（交付 2 的 `item_type` 是分层口径约定，不提前实现落库）。

#### W6 — 多 Agent 语义与写入门禁

- **目标**：探索节点写 `session_id + task_id + workspace_id`；只读子代理**不写索引、不写 exploration memory**；跨任务复用阈值 ≥ 0.90（配置已在 W3，W6 负责写入侧硬拦与作用域隔离）。
- **文件落点**：修改 `backend/internal/knowledge/exploration_recorder.go`（写前置门禁：`ReadOnly` / 子代理类型；跨任务查询过滤）；修改 `backend/internal/agent/*`（**若现无"只读子代理"标志，引入最小枚举并透传**，开工时先核实）；修改 `backend/internal/runtimeapi/session_runtime_support.go` 与 `backend/cmd/aicli/commands/*`（把子代理模式传进 Recorder）。
- **测试清单**：新增 `backend/internal/knowledge/exploration_scope_test.go`（只读子代理零写入、跨任务阈值过滤、workspace 隔离）；新增 `backend/internal/runtimeapi/subagent_knowledge_write_guard_test.go`（编排路径断言）。
- **验收映射**：G2（脏写防护）；`04` §6 风险 R10 的对策项。
- **前置**：W2。
- **风险**：子代理只读标志可能不存在 → 需最小新增；判定错误的方向必须是"默认不写"（漏记可接受、脏写不可接受）；跨任务阈值默认 0.90 不得下调。

#### W7 — 验收测量与报告（A/B ≥ 20 任务 + 复算）

- **目标**：以真实任务集跑 `on` vs `off` A/B ≥ 20 个任务，产出 Phase 2 验收报告（`04` §7.7 模板），G1–G4 全部可复算。
- **文件落点**：新增 `docs/knowledge_Layer/reports/phase2_exploration_report.md`；新增 `backend/internal/knowledge/exploration_report_test.go`（从 `usageledger` + telemetry 复算 G1/G2，报告数字与复算逐位一致）；若缺会话级 p95 埋点，最小追加（开工时核实 `internal/observability` 或既有 runtime 统计）；**验收通过后**才更新 `06` §1.1 / §4 与 `04` §5 Phase 2 状态。
- **测试/测量清单**：复算一致性（同一批记录两次计算一致）；A/B 任务集来自真实 ledger 日志采样；G1 报 median + 95% CI（n ≥ 20）；G2 `unsafe_reuse_count=0`；G3 会话级 p95 增幅 ≤ 10%；G4 关闭 `KnowledgeMode` 后指标回基线。
- **验收映射**：G1–G4 全部。
- **前置**：W2–W6（W0 提供 file-level 反例诊断）。
- **风险**：样本量与分布漂移（同一任务集两臂、真实日志采样）；p95 埋点可能缺失；`on` 实测需要用户显式开启（默认 off）。

#### 第一步最小切片（S1，可在下一轮直接开工）

S1 = W1 前半（纯新增文件 + 接口追加，不触碰 W0 的热点文件，无门禁依赖；建议与 W0 并行）：

| # | 文件 | 动作 |
|---|---|---|
| 1 | `backend/internal/knowledge/exploration.go` | 新增：DTO / 枚举 / 作用域校验 / ID 派生 |
| 2 | `backend/internal/knowledge/store_sqlite_exploration.go` | 新增：session upsert、node append/touch、edge append |
| 3 | `backend/internal/knowledge/store_sqlite_exploration_read.go` | 新增：task scope + target 查询 |
| 4 | `backend/internal/knowledge/version_hash.go` | 新增：`WorkspaceVersion`（补齐现状缺口） |
| 5 | `backend/internal/knowledge/store.go` | 修改：仅追加接口方法 |
| 6 | `backend/internal/knowledge/{exploration_test.go,store_sqlite_exploration_test.go,store_sqlite_exploration_read_test.go}` | 新增：见 W1 测试清单 |

DoD（完成判据）：① 零迁移即可读写三表；② 同 target 重复写幂等（`use_count` 累加、`last_used_at` 刷新）；③ 节点 `knowledge_version` 非空；④ reader 写返回 `ErrReadOnlyStore`；⑤ `mode=off` 下 `cd backend; go test ./internal/knowledge/...` 全绿且行为不变。

#### 与 ADR-0008 配套项（新列 + live 写入）的先后关系

- **定性**：`baseline_files_n` / `overlap_files_n` 新列与三入口 live 写入是 **Phase 1 收口项**（ADR-0008 §8.1 / §10），**不是 Phase 2 交付**，也不构成 Phase 2 门禁（P2 门禁 = ADR-0004 Accept，2026-09-29 已满足）。
- **顺序：W0 先做，且不晚于 W2**。理由：① W0 与 W2 修改同一批文件与接线点（`knowledge/shadow.go`、`usageledger/sqlite_store.go`、`runtimeapi/session_runtime_support.go`、`cmd/aicli/commands/*`、`cmd/runtime-server/main.go`），先收口可避免二次触碰与并行写者合并冲突；② ADR-0003 §10 的 per-file / per-symbol 归因层 Gate = `Phase2-start`，W7 的失例分析会直接消费 file-level 列；③ 三入口 live 回归与 W2 的入口回归可合并为一次。
- **可并行部分**：W0 与 **W1**（全部新增文件 / 接口追加，零重叠）可同时开工；**W2 起需等 W0 落盘**。
- **口径边界**：W0 只做 additive 列 + 写入 + 复算；不改 ADR-0003 的 view 口径与 economy / usable 公式（ADR-0008 §4）。

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
| M1 索引可信 | Phase 1 验收通过（**ADR-0003 M1 调用级可用率达标**；差异率降为诊断） | 重构索引，而非进入 Phase 2 |
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
| （表，非文件）`exploration_attribution` | 探索归因与 shadow 差异率（ADR-0003 §4.1）；落在 `usageledger` 既有 `init()` statements 中，**不新增文件** | 0 |
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

> 2026-09-29 落盘核对：原预测的 `indexer_light.go` / `query.go` / `knowledge_test.go` 未单独成文件（功能分别在 `indexer.go`、`store_sqlite_read.go`、各 `*_test.go`）；Phase 1 实际新增 `store_sqlite_fts.go`、`activation.go`、`shadow.go`、`jobs.go`、`lockwait.go`、`status.go`、`migrations/0002_file_soft_delete.sql`。

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
| `backend/internal/usageledger/sqlite_store.go` | 新增探索归因字段（9 列，幂等 `ADD COLUMN`）**+ `exploration_attribution` 表与 2 个索引**（ADR-0003 §4.1） | 0 |
| `backend/internal/usageanalytics/*` | 聚合与展示新指标（2026-09-28 更正：实际聚合与暴露落在 `internal/api/runtimeapi` 的 `GetUsageLedger`（`group_by`）；`usageanalytics` 不读 ledger，见 `CHANGELOG.md`） | 0 |
| `backend/internal/sqliteutil/sqliteutil.go` | 增加 `foreign_keys=ON` 选项与锁等待埋点 | 0/1 |
| `backend/internal/migrate/*` | 接入 knowledge 迁移 | 0 |
| `backend/internal/events` / `runtimeevents` | knowledge 事件接入 | 1/5 |
| `backend/cmd/runtime-server/main.go` | 启动阶段 `knowledge.Open`；向 `internal/background` 注册索引任务（2026-09-28 实现偏离：改由 `knowledge.Activate` 内部 goroutine 承担，见 `CHANGELOG.md`）；默认 writer owner | 1 |
| `backend/cmd/aicli/commands/chat.go`（含 acp 入口） | workspace 解析后 `knowledge.Open`；reader / owner 竞争 | 1 |
| `backend/configs/runtime.yaml` / `runtime.win7.yaml` | `knowledge:` 配置段（2026-09-29 校正：`config.yaml` / `config.runtime.snapshot.yaml` 无该段，运行时零值即 off） | 0 |
| `backend/configs/model_cards.yaml` | 若涉及工具 / 模式提示词 | 3 |
| `docs/knowledge_Layer/01_*.md` | 去重、加锚点、去 PostgreSQL（2026-09-28：已加"§0 事实源边界"；正文逐节删减见 §9 待办 #18） | 0 |
| `docs/knowledge_Layer/02_*.md` | 顶部声明 schema 应用顺序；删除 / 标注 PostgreSQL | 0 |
| `docs/knowledge_Layer/03_*.md` | 拆分为 `supplement/`（2026-09-28 已完成，`03` 变为拆分索引） | 0 |
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
- 不要在 `02` 之外复制 core DDL（extension schema 例外，落点是 `supplement/*`；`03` 为拆分索引）。**`04` 不得含 `CREATE TABLE`**——见 ADR-0007 §4.3；`index_jobs` DDL 已于 **2026-09-28 迁至 `supplement/15` §15.3**，其余 core 表 DDL 的引用化见 §9 待办 **#19**。
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
| 1 | ✅ **已完成**（2026-09-28，ADR-0007 解锁后）：`04` §4.3 的 `CREATE TABLE index_jobs` 迁至 `supplement/15_change_management.md` §15.3；`04` 只留用途 / 验收指标 / 引用 | ADR-0007 被 Accept（已满足） | `04` §4.3、`supplement/15` §15.3 |
| 2 | ✅ **已完成（2026-09-29，ADR-0009 落地）**：`02` §8 三分组归类清理——【v1 core】23 名（22 表 + `symbol_fts`）、【extension】1 名（`index_jobs`）、【已推迟】5 名（`branches`/`inheritance`/`dependencies`/`language_projects`/`events`；删除名以 `~~…~~`/注记出现，不进入名字集合）；6 幽灵名去向见 ADR-0009 附录 B，顶部旧注记同步删除 | ADR-0009 被 Accept（2026-09-29 已满足） | `02` §8 |
| 3 | ✅ **已完成并复跑（2026-09-29）**：`backend/scripts/check_knowledge_doc_invariants.go`（stdlib、`go run`、失败 exit≠0、含正向 fixture 自测）。ADR-0009 落地后复跑 **5/5 PASS**：I1 23=23；I2 零越位（`04` 15 处 DDL 已引用化）；I3 零漂移；I4 三分组（23+1+2）；I5 声明 23=实际 23（落地前基线 5/5 FAIL 见 `CHANGELOG.md`） | ADR-0007 被 Accept（已满足） | `02` / `04` |
| 4 | `supplement/05` §10.3（原 `03` §5.3）三条规则改写；`lsp_servers` 补 `position_encoding` 列 | ADR-0006 被 Accept | `supplement/05` §10.3 |
| 5 | `02` 顶部加"core 先行；启用 extension 前必须先应用 `supplement/*` 的 ALTER"声明 | B15 转 ADR 并 Accept | `02` 顶部（**当前仅有"待补"标注**） |
| 6 | `04` 附录 B 其余条目逐条判定：决策 → ADR；笔误 → 直接改文档并记 `CHANGELOG`；风险 → 留 `04` §6 | Phase 1 开工前 | `adr/`、`04` 附录 B |
| 7 | ✅ **已裁决并落地（2026-09-29）**：ADR-0009 采纳选项 C——上限只约束【v1 core】，数字 16→23；`schema_migrations` 不计入（归 `internal/migrate`）；extension/deferred 不计入。`04` §0.3 与 §4.3 标题已同步，I5 转绿 | 由 I5 检查结果裁决（已产出；ADR-0009 Accepted） | `04` §0.3/§4.3、ADR-0009 |
| 8 | ✅ **已完成**（2026-09-28）：`00_Code_Intelligence_Project_Knowledge_Layer.md` 移入 `archive/` 并加免责声明 | Phase 0 文档治理 | `archive/` |
| 9 | ✅ **已完成**（2026-09-28）：`03_*.md` 拆分为 `supplement/01`–`16`（`03` 变为拆分索引）；`01_*.md` 见 **#18** | Phase 0 | `01` / `03` / `supplement/*` |
| 20 | ✅ **已完成（2026-09-29）**：`02` §8 三分组落地（ADR-0007 D1 / ADR-0009 附录 D）：`【v1 core】/【extension】/【已推迟】` + 6 幽灵名归类 + `symbol_fts` 补列；复跑 I1/I4 转绿（详见 #2/#3） | ADR-0009 被 Accept（已满足） | `02` §8 |

### 9.1 规划缺口（2026-09-21 核查发现，同日已修复）

> 下列 7 项**不是"未实现"**，而是**计划自身的缺口**：任务已写进规格（`supplement/05`）或 ADR（`0003`），却没有 Phase 归属；或两个事实源对同一验收给了不同口径。
> **本表保留为发现记录（历史）；逐项修复见 §9.2。**

| # | 缺口 | 为什么是缺口 | 触发条件 | 落点 |
|---|---|---|---|---|
| 10 | **Phase 1 缺"激活"交付项** | `knowledge.Open` 接入 `cmd/aicli`（tui / acp）与 `cmd/runtime-server` 只写在 `supplement/05` §2 / §8；`04` §5 Phase 1 的交付与文件落点、`06` §5.2 的修改清单**均无此项** → Phase 1 的 shadow 没有任何进程打开知识层，验收无法进行（本次 A/B 延期即此因） | Phase 1 开工前 | `04` §5 Phase 1、`06` §5.2 |
| 11 | **ADR-0003 阈值门禁结构性不可达** | α 与 Phase 1 门槛数值挂在 `Phase0-baseline`（ADR-0003 §10），但该数据需 shadow 对比，而 Phase 0 为 `mode=off` 且未接入（见 #10）→ 门禁永远无法满足 → ADR-0003 无法 Accept → Phase 1 被自锁 | ADR-0003 Accept 前 | `adr/0003` §10、`04` §7.6 |
| 12 | **ADR-0003 §6.2 强制内容缺失** | ADR 要求"`coverage` 低估"警告与 `candidate_n < baseline_n` 抽样核对**必须写进 Phase 0 报告**；报告（含 §6）无此内容 | 随 #11 一并裁决 | `reports/phase0_baseline_report.md` |
| 13 | **Phase 1 shadow 用 Phase 3 的产物定义** | `04` §5 Phase 1 交付 4 与验收均以 `code.search` 表述，而 `code.search` 是 **Phase 3** 交付（`04` §5 Phase 3 交付 1）；ADR-0003 §4.3/§4.4 定义的实际机制是**拦截既有 `grep` / `view`** | Phase 1 开工前 | `04` §5 Phase 1 |
| 14 | **Phase 1 验收两套口径未对齐** | `04` §5 Phase 1："`code.search` 与 `grep` 的 top-10 文件集合差异率 < 15%"；ADR-0003 §4.5：**M1（调用级可用率，`coverage ≥ α` 且 `economy ≤ 1.0`）为 Phase 1 主门槛**。同一 Phase 存在两个验收定义 | Phase 1 开工前 | `04` §5 / §7.6、`adr/0003` |
| 15 | **`exploration_attribution` 表无 Phase 归属** | 该表由 ADR-0003 §4.1 创立、M1 由其计算，但**仅出现在 ADR-0003**；`04` §5 各 Phase 交付与 `06` §5.1 / §5.2 均未列 | ADR-0003 Accept 后 | `04` §5、`06` §5.1 |
| 16 | **ADR-0003 的 D3 字面不变量与已实现 Phase 0 冲突** | ADR 写"`token_usage_history` **一列不改、一行不变**"，而 Phase 0 交付 2 已用 `ALTER TABLE … ADD COLUMN` 加 9 列（`sqlite_store.go` L236–244 / L262–287）。实质兼容（请求级粒度、`DEFAULT 0` 保持旧行语义），但**字面冲突必须在 Accept 前裁决**：改措辞，或改落点 | ADR-0003 Accept 前 | `adr/0003` §4.1 / D3 措辞，或改落点 |
| **17** | **ADR-0004 陈旧度阈值的 Gate 同属结构性不可达**（**修复过程中新发现**） | `S_fresh` / `S_max` 是 **reader 观测到的陈旧度**阈值，其分布需索引 + 多进程仲裁 + 心跳；而 ADR-0004 §4.1 写“由 Phase 0 校准”、§10 Gate 写 `Phase0-baseline`。Phase 0 为 `mode=off`、无索引、无 reader → **同 #11 一类缺陷**。区别：ADR-0004 的 Accept **不被该值阻塞**（§4.1 已声明只是初始值），缺陷是**校准会永久悬空** | Phase 2 开工前 | `adr/0004` §4.1 / §10 |

---

### 9.2 缺口修复记录（2026-09-21）

> **8 项**（#10–#17）全部修复。**修复性质分两类**：
> (a) 把已存在于规格 / ADR 的内容补上 Phase 归属或对齐口径——**文档一致性修复，不改变任何设计决策**；
> (b) 修改 `adr/0003` 的 Gate 与 D3 措辞——该 ADR 仍为 `Proposed`，按 `adr/README.md` 在 Accept 前修订属正常流程，**正文修订已就地标注日期**，owner 可在 Accept 时一并复核。

| # | 修复动作 | 落点 | 验证 |
|---|---|---|---|
| 10 | Phase 1 **新增交付 6「接入（激活）」**：`knowledge.Open` 接入 runtime-server / aicli cmd+tui / aicli acp；文件落点补 `cmd/runtime-server/main.go`、`cmd/aicli/commands/chat.go` | `04` §5 Phase 1、`06` §4 Phase 1 / §5.2 | 三入口在 `mode=off` 下行为不变；`mode=shadow` 有数据落库 |
| 11 | α 与 Phase 1 门槛数值的 Gate 由 `Phase0-baseline` 改为 **`Phase1-shadow`**；拆为“Phase 0 出基线与警告 / Phase 1 shadow 出阈值”；**ADR-0003 的 Accept 不再被阈值阻塞** | `adr/0003` 头部 Gate / §1.2 / §2 D4 / §4.2 / §5 / §6.1 / §8 / §10、`04` §7.6、`06` §1.2 / §3 / §4 | Gate 现可达成：shadow 数据产生于 Phase 1，而 Phase 1 已可开工 |
| 12 | Phase 0 报告补写 ADR-0003 §6.2 要求的 `coverage` 低估警告与抽样核对**状态说明**（Phase 0 无 shadow 数据，抽样核对本身顺延至 `Phase1-shadow`） | `reports/phase0_baseline_report.md` §7 | ADR-0003 §10 对应行标为“已完成” |
| 13 | Phase 1 shadow 的机制表述由 `code.search`（Phase 3 产物）改为**拦截既有 `grep` / `view`**；明确 Phase 1 不新增工具 | `04` §5 Phase 1 交付 4、`06` §4 Phase 1 交付 4 | 与 ADR-0003 §4.3 / §4.4 一致；Phase 1 不再依赖 Phase 3 |
| 14 | Phase 1 验收改为**主门槛 + 诊断指标**：主门槛 = ADR-0003 §4.5 的 **M1**；“top-10 差异率 < 15%”降为**诊断**（不判 Pass/Fail） | `04` §5 Phase 1 验收门槛、`06` §4 Phase 1 验收门槛 / §4.2 | 同一 Phase 只剩一个 Pass/Fail 判据 |
| 15 | `exploration_attribution` 表**归入 Phase 0 交付 7**（只建表与埋点骨架，不产生数据）；`06` §5.1 / §5.2 补登 | `04` §5 Phase 0、`06` §4 Phase 0 / §5.1 / §5.2 | ADR-0003 §8“重复 init 幂等”进 Phase 0 验收门槛 |
| 16 | ADR-0003 **D3 改为三条可检验形式**（不新增行 / 不改变既有聚合 / 不改变历史行语义），并注明 Phase 0 的 9 列 `ADD COLUMN DEFAULT 0` **满足**该实质要求；§8 验证表同步改写 | `adr/0003` §4.1 / §8 | 实现与不变量不再字面冲突；Accept 前无需改代码 |
| **17** | ADR-0004 §10 的 `S_fresh` / `S_max` Gate 由 `Phase0-baseline` 改为 **`Phase2-start`**；§4.1 “由 Phase 0 校准”同步修订并注明“Accept 不被该值阻塞” | `adr/0004` §4.1 / §10、`adr/README.md` §4 / §5 | Gate 指向可达成；校准不再悬空 |

**仍未解决但已登记的相邻项**：`04` §5 Phase 1 的“首次全量 ≤ 120s”“DB ≤ 200MB”两条初值已被实测击穿（146.9s / 247.5 MiB），建议改为“≤ 0.6ms × refs 且 ≤ 300s”“≤ 1KiB × refs 且 ≤ 300MB”——该改动已于 **2026-09-29 经 `04` §7.4 评审落定**：首次全量 **≤ 360 s**（n=3 中位 292.4 s）、DB 沿用默认 512MB（实测 313.9 MiB）；未采用“每 ref”建议（历史建议存于 phase0 报告 §5）。

---

### 9.3 文档治理完成记录（2026-09-28）

> 对应 §9 待办 **#8** 与 **#9**（触发条件为 "Phase 0 文档治理"，Phase 0 已于 2026-09-28 完成）。
> 执行性质：**文档结构重组，不改变任何设计决策、不改变任何 DDL 语义、不改动 `Accepted` ADR 正文**。

| # | 动作 | 落点 | 验证 |
|---|---|---|---|
| 8 | `00_Code_Intelligence_Project_Knowledge_Layer.md` 移入 `archive/` 并在文首加免责声明 | `archive/00_…md`、`README.md` §1、`04` 评审对象行 | `git mv` 保留历史；文首声明"仅供追溯，不作规范" |
| 9a | `03_agent_harness_supplement.md` 按自身 §21 的建议拆分为 `supplement/01`–`16`（正文逐段搬迁，章节号沿用原编号）；`03` 变为**拆分索引 + 历史引用映射** | `supplement/*`、`03`、`README.md` §1/§2/§3、`06` §2、`adr/README.md` §7、`docs/lsp/*` 4 个文件、`adr/0006` 的 `Related` 行 | 唯一偏离：`05` 槽位已被既有集成文档占用 → `03` §5 并入其 §10；`03 §5.4(L674)` 更正为 `§6.4` |
| 9b | `01_*.md` 去重：在文首加 **§0 定位与事实源边界**（逐节指认权威落点），**正文未删减** | `01` §0、`README.md` §1 | 残留工作登记为 **#18** |
| 1 | ✅ **已完成**（2026-09-28，ADR-0007 解锁后）：`index_jobs` DDL 由 `04` §4.3 迁至 `supplement/15_change_management.md` §15.3；`04` 只留用途 / 验收指标 / 引用 | `04` §4.3、`supplement/15` §15.3、`README.md` §7、`adr/README.md` §5、`02` §8 标注 | DDL 逐字节搬迁；其余 core 表 DDL 的引用化见 #19（已于 2026-09-29 完成） |

| # | 待办（新开） | 触发条件 | 落点 |
|---|---|---|---|
| 18 | `01_*.md` 正文**逐节删减**（把 `02`/`supplement/*`/`04` 已拥有的正文改为指针，只保留架构意图与理由）——本轮只加了边界声明，未删正文 | Phase 1 开工前（可与 Phase 1 并行） | `01` |
| 19 | ✅ **已完成（2026-09-29，ADR-0009 落地后）**：`04` §4.3 其余 15 处 DDL（14 × 建表 + 1 × FTS 虚表，及配套索引）全部引用化，`04` 彻底无 DDL（I2 转绿）；例外映射（`schema_migrations`→`internal/migrate`、`symbol_aliases`→`supplement/01` §1.3、FTS 虚表→`02` §73 的 `symbol_fts`）见 ADR-0009 §8.1 | **I5 裁决后**（已满足：ADR-0009 Accepted） | `04` §4.3 |

> 触发条件已满足但**尚未执行**的相邻待办仅剩：**#5**（B15 应用顺序声明）。**#2** / **#3** / **#7** / **#19** / **#20** 已于 2026-09-29（ADR-0009 落地）完成，检查器复跑 5/5 PASS。

---

## 附录 A：Phase 0 首日清单

1. 读 `README.md` §3（事实源）、本文 §3（门禁）、`adr/README.md` §2（状态生命周期）。
2. 确认 owner 已 Accept **ADR-0001 / ADR-0007 / ADR-0003（口径）**（Phase 1 硬门禁）——**已于 2026-09-28 完成**。
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
