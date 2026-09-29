# CHANGELOG — Code Knowledge Runtime（知识层）

> 本文件是 `docs/knowledge_Layer/` 的**变更历史唯一事实源**（见 [`README.md`](README.md) §3）。
> 变更流程见 [`README.md`](README.md) §6。
> 本目录在 2026-09-20 之前没有变更记录；下列为首批条目。

---

## 2026-09-29 — Phase 2 开工规划落档（Exploration Memory + Context Planner）

`06` §4 Phase 2 追加子节 **「Phase 2 开工规划（2026-09-29）」**：工作流 W0–W7（按依赖排序，每条含目标 / 文件落点 / 测试清单 / 验收映射 G1–G4 / 前置依赖 / 风险）、第一步最小切片 S1（标注"可在下一轮直接开工"）、与 ADR-0008 配套项（新列 + live 写入）的先后关系。

### Changed

- `06` §4 Phase 2：追加开工规划子节；Phase 2「状态」行：`门禁已解除，待开工` → **`规划完成（2026-09-29），待开工`**（并指向规划子节与最小首片 S1）。
- `06` §1.1 Phase 2 行同步为 **规划完成（2026-09-29），待开工**，指向 §4 Phase 2 规划子节。

### Notes

- 本轮为**只读核对 + 文档落档**：不写业务代码、不改 `04` / `adr/*`、不 git commit。
- 与并行写者核验：落档前检索 `docs/knowledge_Layer/` 无既有 Phase 2 开工规划（无重复/覆盖）；对 `06` 的编辑均先重读目标行。
- 规划发现（缺口，已写入 `06` 规划子节"现状核对"）：`exploration_sessions/nodes/edges` 建表已在 `knowledge/migrations/0001_init.sql`（但无 Go 读写代码）；`contextmgr` 无 `KnowledgeMode` / `Knowledge` 字段；工作区级 `knowledge_version` 无生成器；ADR-0008 的 `baseline_files_n` / `overlap_files_n` 新列与三入口 live 写入仍未实现（列为 W0，建议先于 W2 收口）。
- 首片 S1 = W1 前半（探索记忆存储层 + `WorkspaceVersion` + 测试），零迁移、可在下一轮直接开工。
- 旁注（非 Phase 2 阻塞，待复核）：`backend/internal/knowledge/config.go` 的 `DefaultMaxDBSizeMB = 200` 与 `04` §7.4 校准值 512MB 不一致（Phase 5 GC 触发点）。

---

## 2026-09-29 — ADR-0004 Accept（陈旧索引下 `code.*` 工具面）：P2 门禁解除

owner 授权代改并记录裁决（先例：ADR-0008 / 0009）：ADR-0004 由 `Proposed` 改为 **`Accepted`**。

### Changed

- `adr/0004-stale-index-tool-surface.md`：`Status: Proposed` → `Accepted`，加注"2026-09-29，项目 owner 授权代改（先例：0008/0009）"；§10 追加接受记录；**正文其余不动**（Accepted 后不可改正文）。
- `adr/README.md`：§5 索引表 `0004` 行（Proposed → **Accepted**）+ 新增"接受与落地状态（2026-09-29）"段落；2026-09-28 段落的剩余 Proposed 列表尾注同步。
- `06` §0 一句话状态 / §1.1 Phase 2 行（未开始 → **门禁已解除，待开工**）/ §1.2 阻塞清单 / §3 ADR 表 / §4 Phase 2 前置与状态；`README.md` §0 当前阶段 / §2 文档状态表 / §4 进度表 Phase 2 行同步。
- `supplement/05` §9 表第 4 行、`GLOSSARY.md` §4 的 `S_fresh` / `S_max` 条目（"须由 Phase 0 校准" → Gate = `Phase2-start`，补齐 2026-09-21 Gate 修订）同步。

### Notes

- 关键决策（陈旧索引下 `code.*` 工具面口径）：按陈旧度**分级注册**——新鲜全开；中等只开定义类 `code.find_symbol` / `code.search`；过旧全关；关系类 `code.find_refs` / `code.callers` / `code.impact` 陈旧时**不注册**（杜绝静默错误）。schema 恒定、描述可变；三个逃生舱开关。
- `S_fresh`（60s）/ `S_max`（15min）阈值：**Gate = `Phase2-start`**（2026-09-21 由 `Phase0-baseline` 修订），初值**不阻塞 Accept**；§10 其余跟进项同 Gate。
- **P2 门禁解除**（ADR-0008 §8.1 的"P2 进入条件 = ADR-0004 Accept"已满足），Phase 2 待开工；未开工事实不变（`04` §5 Phase 2 状态仍为"未开始"，本轮未改）。

---

## 2026-09-29 — ADR-0008 裁决落地（file-level 口径）+ α 定稿与 Phase 1 主门槛复核通过

owner 授权代改并记录裁决：ADR-0008 采纳**选项 A**——grep 通道主判据 = **file-level 覆盖**（行级保留为诊断；view 口径不变）。

### Changed

- `adr/0008-grep-coverage-file-level.md`：`Proposed` → **`Accepted`**；新增 §8.1 落地与阈值定稿、§10 行状态更新；`adr/README.md` 索引与说明同步；`adr/0003` §10 的"α 与 Phase 1 门槛数值"待办标记完成。
- `reports/phase1_shadow_report.md` §1 / §4.4 / §5.1：口径裁决依据（file-level mean 31.83 %、p90 100 %、usable@0.8 26.76 %、answerable 49.38 %；行级 0.47 % 仅作诊断）、α 定稿与 Phase 1 复核结论落稿。
- `06` §1.1 / §1.3 / §3 ADR 表 / §4 Phase 1 状态行、`04` §5 Phase 1 状态、`README.md` §4 进度表与说明同步。
- config：`knowledge.shadow.alpha` 字段已存在且默认即建议值 0.8，**无需改动**。

### Verified

- **α 定稿 = 0.8**；**Phase 1 门槛 = 合并 M1 ≥ 0.31**（95 % CI 下界，α=0.8）。
- **Phase 1 主门槛复核 = Pass**：grep file-level `usable@0.8 = 26.76 %`（n=213）、view `M1 = 48.41 %`（n=157）、合并 **M1 = 133/370 = 35.95 % ≥ 0.31**；复算：报告 §6 第 2 步（`TestPhase1ShadowReplay`，`KNOWLEDGE_SHADOW_ALPHA=0.8`）。
- **P2 进入条件 = ADR-0004 Accept**；ADR-0008 的"新列实现 + 三入口 live 写入新列"仍为 Open。

---

## 2026-09-29 — ADR-0009 落地（v1 表集口径，Accepted）：不变量检查器 5/5 PASS

ADR-0009 由 `Proposed` 改为 `Accepted`（项目 owner 授权代改，先例见 2026-09-28 条目），并完成 §4 落地与 `06` §9 #19。

### Changed

- `04` §0.3：v1 表集声明 `≤16` → **`≤23`**（= `02` §8【v1 core】的 22 表 + `symbol_fts` 虚表；不含 `schema_migrations` / extension / deferred）。
- `04` §4.3 标题 → "P0/P1 最小数据模型（16 张；v1 core 的子集）"；其余 15 处 DDL（14 表 + FTS 虚表，含索引）**引用化**，`04` 彻底无 DDL（#19）。例外映射（无 `02` DDL 的三项）：`schema_migrations` → `internal/migrate`、`symbol_aliases` → `supplement/01` §1.3、FTS 虚表 → `02` §73 的规范名 `symbol_fts`（旧稿名 `symbols_fts`）。
- `02` §8：三分组落地——【v1 core】23 名 /【extension】1 名（`index_jobs`）/【已推迟】5 名（`branches`/`inheritance`/`dependencies`/`language_projects`/`events`；删除名以删除线或注记出现，不进入名字集合）；删除"本块与下方 DDL 不一致"旧注记。
- `README.md` §3：v1 表集转述同步为 ≤23（见 `04` §0.3）；`adr/README.md`：`0009` 状态同步为 **Accepted**。
- `06` §9：#2 / #3 / #7 / #19 / #20 标记完成并补结果；相邻待办仅剩 #5。
- ADR-0009 §8.1 / §10：追加落地记录（含例外映射与行号提示）。

### Verified

- `cd backend; go run scripts/check_knowledge_doc_invariants.go` = **5/5 PASS**：I1（23 = 23）、I2（0 越位 DDL）、I3（0 漂移）、I4（三分组 23+1+2）、I5（声明 23 = 实际 23）。

---

## 2026-09-29 — ADR-0009 起草（v1 表集口径裁决，Proposed）

回应 ADR-0007 §10 的 `Phase1-start` 待办（I5：`04` 声明 ≤16 vs `02` 实际 23）与 `06` §9 待办 #2/#7/#20。

### Added

- ADR [`adr/0009-v1-table-set-scope.md`](adr/0009-v1-table-set-scope.md)（**Proposed**）：
  推荐 **选项 C——上限与分组解耦，`v1 表集上限` 只约束【v1 core】**（当前 23 = 22 表 + `symbol_fts`；
  extension/deferred 与 `schema_migrations` 不计入）；附 28 名三分组建议（22 core + `symbol_fts` 补列 /
  1 extension / 5 deferred）、6 幽灵名去向与 I3 两对漂移的规范名（`inheritance_edges`、`dependency_versions`）。
  等待 owner 裁决。

### Notes

- 裁决前不动事实源：本轮未修改 `02`/`04`/`supplement/*`/`06`；`adr/README.md` 索引表同步登记 `0009 = Proposed`。

---

## 2026-09-29 — Phase1-shadow 实测 v1（真实调用重放）与候选映射修复

执行 `Phase1-shadow` Gate（ADR-0003 §10）：需要 shadow 数据校准 α 并复算 M1。
此前仓库只有 `exploration_attribution` 的写入路径：既无读取/聚合工具，也无无头测量入口。

### Added

- `usageledger.ListExplorationAttribution`：按时间读取归因行（NULL coverage/economy 原样还原）。
- `knowledge.SummarizeAttribution` / `CalibrateShadowAlpha`：M1–M4（mean/p50/p90 +
  按 tool/source/project 分组），usable 用传入 α 重算；同批数据重复复算逐位一致。
- 实测入口 `knowledge.TestPhase1ShadowReplay`（env-gated，CI 跳过）：真实调用重放 →
  生产 `ShadowObserver` → 真实 `RunIndex` 索引 → 真实 `usageledger` 落库 → 复算报告。
- 调用集提取脚本 `backend/scripts/extract-shadow-calls.mjs` 与调用集
  `reports/phase1_shadow_calls.jsonl`（400 条真实 grep/view：235/165）。
- `knowledge.Config.Alpha`（yaml `alpha`）+ `Activation.Config()`；`ShadowObserverFor`
  把配置 α 传入观察器；`runtime.yaml` 增加说明（缺省 0.8 仍是联调初值）。
- 报告 `reports/phase1_shadow_report.md`。
- ADR 提案 [`adr/0008-grep-coverage-file-level.md`](adr/0008-grep-coverage-file-level.md)
  （**Proposed**）：grep 通道主判据改 file-level 覆盖（新增两列），行级保留为诊断；
  依据报告 §4.1/§4.3 的实测反差（行级 M1=0.47 % vs 文件级 usable@0.8=26.76 %），
  并回应 ADR-0003 §10 的"抽样核对报告"待办。等待 owner 裁决。
- **live 接入验证（首个入口，2026-09-29）**：`aicli chat`（cmd+tui 宿主）以 `mode=shadow`
  跑真实会话（隔离 workspace + 临时 runtime.yaml/账本 DSN）——`knowledge.db` 落 2 文件 /
  3 符号，`ledger.db` 落 2 条 `exploration_attribution`（grep coverage=0.667、view=0.600）；
  新增 `TestLiveLedgerVerify`（env 门控）走生产 reader 复算 M1–M4（小样本 M1=0 %、
  M2=63.33 %、M4=74.34 %）。同日 **ACP 入口验证通过**（`aicli acp` + Node NDJSON JSON-RPC
  客户端：initialize → session/new → session/prompt；grep/view `tool_call` 后账本追加 2 条
  同值行）与 **runtime-server 入口验证通过**（HTTP：会话创建 → `permission-mode`
  `bypass_permissions`+confirm → `runtime/commands submit_prompt`；grep/view 并行执行后
  `DONE`，账本再追加 2 条同值行）——**live 验证 3/3**。注意：HTTP 会话默认模型来自
  `runtime.yaml` `agent.defaultModel`（内建 `claude-3-5-sonnet` 无 provider 声明会 fail-fast）。
- **抽样补齐 §5 未覆盖度量（2026-09-29）**：新增 env 门控 live 抽样
  `internal/knowledge/live_sampling_test.go`——content_hash 一致率 **261/261 = 100 %**
  （263 抽样；2 例为索引后被修改的预期不对称），同进程 4 写者锁等待
  `samples=0 / p95=0 / retry_failures=0`（240 文件 ×4、13.0 s 全部成功）。
  过程中修复 `index_jobs.id` 同纳秒碰撞（`jobs.go` 追加进程内原子序号 `indexJobSeq`），
  回归 `TestStartIndexJobIDsAreUnique`（8 并发 job 全成功且 ID 互异）。
- **性能阈值校准（04 §7.6 / 报告 §4.7，2026-09-29）**：等规模样本 n=3（原工作树 +
  2 个 4989 文件语料副本）全量索引——耗时中位数 **292.4 s**、95 % CI [241.8, 356.5] s；
  DB 中位数 313.9 MiB（CI [313.2, 314.3] MiB）；**`04` §7.4「首次全量索引（本仓库规模）」
  由未校准占位 ≤120 s 校准为 ≤ 360 s**（CI 上界取整）；二次增量（全量跳过）0.7–0.9 ms/文件。
  跨仓库抽验（module cache 异源语料 ×2：`x/net@v0.57.0` 742 文件 32.5 s、
  `gin@v1.12.0` 98 文件 3.81 s；**38.9–43.8 ms/文件**）与本仓库 58.6 ms/文件同量级；
  GitHub 直连被网络阻塞，故用本机 module cache 取样。
- **多 `paths` 逐项作用域（2026-09-29，报告 §5 第 2 项）**：grep shadow 映射支持 rg
  多根语义——`shadow_scope.go` 前缀集合 OR + `shadow.go` 的 `grepPathScopes` /
  `parseGrepBaselineMulti` / `prefixScopePathMulti`（逐行归属某作用域、否则回退首项），
  4 个回归测试（含观察器端到端 union 用例）；提取端 `extract-shadow-calls.mjs` 不再
  截取 `paths` 首项而是保留整组。对 v1 复算无影响（现有 400 条调用集中 `paths`=0，
  240 条为单 `path`），修复面向未来重放与 live 会话。
- **单文件增量实测（`04` §7.4 最后一行落数，2026-09-29）**：新增 env 门控
  `live_incremental_test.go`（20 样本、逐次 `indexed=1` 断言）；干净复测 job 墙钟
  p50 4.15 s / p95 4.35 s、"全量跳过"基线 4.05 s（遍历≈0.81 ms/文件）→
  **marginal p50 108 ms / p95 302 ms**（受干扰首测 p95 687 ms）；对照「p95 < 50 ms」
  **Fail**，须 Phase 5 增量触发（fsnotify）或重定口径。
- **文档不变量检查落地（06 §9 #3，2026-09-29）**：新增 `backend/scripts/check_knowledge_doc_invariants.go`
  （ADR-0007 §4.4 的 I1–I5，stdlib、`go run`、失败 exit≠0、含正向 fixture 自测）。
  首次实测 **5/5 FAIL**（即 #2/#19/#20 尚未执行的事实）：I1 六幽灵名
  （`branches`/`dependencies`/`events`/`index_jobs`/`inheritance`/`language_projects`）
  + `symbol_fts` 未列入；I2 `04` 内 15 处越位 DDL；I3 两对命名漂移
  `{inheritance, inheritance_edges}`/`{dependencies, dependency_versions}`；
  I4 `02` §8 尚无三分组；**I5 声明 ≤16 vs 实际 23**（22 表 + 虚表）→ #7 裁决输入就绪；
  已登记 #20「`02` §8 三分组落地」。

### Fixed（重放实测暴露的 shadow 缺陷）

- 绝对 path / file_path 未折叠为 workspace 相对路径 → 候选恒为空（`relativizeWorkspacePath`）。
- grep 输出路径未按作用域目录补前缀（rg 以 path 为根输出相对路径）→ (path,line) 永不相交
  （`parseGrepBaseline(output, scopeBase)`）。
- regex pattern 直接检索 FTS → 零命中；改为交替拆分 + 字面 token 并集去重（≤6 token，`shadowPatternTokens`）。
- refs 候选未接入且 `FindRefs` 未回填 `path` → 使用点行不可达；新增 `Reference.Path` +
  观察器 refs 候选通道（可选接口 `refCandidateIndex`），并把批量 `patterns` 还原为多次检索
  （`grepPatternList` / `collectShadowTokens` + 提取脚本数组还原）。
- 双作用域只取其一（`path`+`glob` 同时给出时只取 `firstNonEmpty` 的第一个，真实样本
  144/243 条）→ `newScopeFilterSpec` 让 path 与 glob 为 AND，baseline 前缀仍只由 path 决定。

### Findings

- **M1=20.81 %（n=370）**：view 通道 48.4 %（可用），grep 行级 **0.47 %**（M2 3.79 %）；
  同一批数据的 file-level 对照：grep mean **31.83 %**、p90 100 %、usable@0.8 26.76 %、
  answerable **49.38 %**——量化证明瓶颈是行级口径（ADR-0003 §6.2 偏差），需新 ADR 裁决。
- 索引侧复测（4989 文件）：324.9 s / 313.5 MiB，对照 `04` §7.4 初值仍 Fail（同 Phase 0 结论）。
- α 中位数校准=0.10，行级分布双峰（α∈[0.1,0.8] 对 M1 影响可忽略），本次**不写死阈值**。

---

## 2026-09-29 — Phase 1 交付 1/3 收口（Java/C++ 粗符号 + 文件软删除）与 shadow 口径修正

起因：`06` 实施状态核查发现两处交付缺口与一处口径漂移——交付 1 要求"补 Java / Rust / C++
粗符号"但 Java/C++ 未实现；交付 3 要求"删除文件标记 `deleted_at`"但索引只做 `content_hash`
增量、不对账删除；`04`/`06` 声称 shadow 对比写 `invalidation_events`，而该表 reason 闭集是
变更源事件（`04` §4.3），ADR-0003 也未定义 shadow 写该表。

### Added

- **Java 粗符号**（`adapter_builtin.go` 的 `javaSymbolPatterns`）：class / interface / enum /
  record + 带修饰符的方法/构造器（支持 `@Annotation` 前缀与 `public` 可见性判定）；
  `default -> handle();` 等 switch 箭头与字段初始化不误收。`.java` 此前只有文件行、没有符号行。
- **C/C++ 粗符号**（`cppSymbolPatterns`，注册 `cpp` 与 `c`）：class / struct / union / enum +
  具名函数（含 `Foo::bar` 限定、`const` / `noexcept` / 尾置返回类型）；`statementKeywords`
  守卫排除 `return compute(x);` 这类语句。已知折损："最令人烦恼的解析"式变量构造可能误收
  （代码注释已标注，由 Phase 4 深索引收敛）。
- **`migrations/0002_file_soft_delete.sql`**：`files.deleted_at` 列 + `(workspace_id, deleted_at)`
  索引；既有 v1 库打开时自动升级（含升级路径测试）。
- **Store 接口**：`ListActiveFiles` / `MarkFilesDeleted`（单事务：`files.deleted_at` +
  `index_state=stale` + 其 `symbols.deleted_at` 同步标记；幂等；reader 返回 `ErrReadOnlyStore`）。
- **reader 版本守卫**：`verifyInitialized` 在 schema 版本落后于本二进制时显式报错
  （提示以 writer 打开一次完成迁移），避免旧库半可用。
- **`RunIndex` 删除对账**：完整遍历（`!Truncated`）后把"库内登记、磁盘缺失"的文件软删除，
  新增 `IndexResult.Deleted` 计数；文件恢复时 `UpsertFile` 清空标记、`ReplaceSymbols`
  重建符号（复活）。`truncated` 时绝不对账，避免把未遍历到的文件误标为删除。
- **读路径过滤**：FindSymbols / FindRefs / Search / searchLike / symbolIDsByName / Stats
  全部排除 `f.deleted_at IS NOT NULL` 的文件及其符号/引用。
- **测试**：`adapter_java_cpp_test.go`、`indexer_soft_delete_test.go`、`store_test.go` 的
  `TestMarkFilesDeletedIsIdempotent` / `TestSoftDeleteMigrationUpgradesV1Store`。

### Changed

- **`04` §5 Phase 1 交付 4 / `06` §4 Phase 1 交付 4**：shadow 对比只写
  `exploration_attribution`，明确**不写 `invalidation_events`**（口径修正）；
  `store.go` 的 `RecordInvalidation` 注释同步。
- **`06` §1.1/§4/§5、`04` §5、`README.md` §1/§4**：交付 1–6 状态、实际文件落点与
  配置段落点校正（原预测的 `indexer_light.go` / `query.go` / `knowledge_test.go` 未单独
  落盘；`knowledge:` 段实际只在 `runtime.yaml` / `runtime.win7.yaml`）。

### Notes

- `KnowledgeVersion`（磁盘契约版本，DB 文件名的一部分）保持 1：0002 是增量迁移，不改变
  `stable_key` 身份语义；`schema_migrations` 版本随之前进到 2。
- 仍未收敛：`Phase1-shadow` 实测（α 校准 + M1 复算）；Phase 1 尚未验收。

---

## 2026-09-28 — Phase 1 交付 5（`knowledge.status` CLI / HTTP 状态面）

起因：关闭 `06` §4 Phase 1 交付 5 —— 把索引状态、文件数 / 符号数、DB 大小、最近 job、
锁等待 p95 暴露为统一只读状态面（CLI `aicli knowledge status` + HTTP
`GET /api/runtime/knowledge/status`），为 `Phase1-shadow` 实测与日常诊断提供观测入口。

### Added

- **`backend/internal/knowledge/status.go`（`StatusReport` / `Layer.Status` / `OwnerPID` / `lookupWorkspace`）** ——
  统一只读载荷：mode / role / owner pid / workspace / db 路径与大小 / schema 版本 /
  files / symbols / refs / indexed_at + staleness / index_running / last_job / lock_wait /
  degraded_reason。不触发索引、不写库（`lookupWorkspace` 走 `FindWorkspace`，reader 也可用）；
  `degraded_reason` 覆盖「未索引 / reader 降级 / 上次索引失败」，零值不静默。
  `activation.go` 增加 `Activation.Status` / `IndexRunning` / `Stats`（合并本进程后台索引生命周期）。
- **`backend/internal/knowledge/jobs.go`** —— `index_jobs` 运行账本：`StartIndexJob` /
  `UpdateIndexJob` / `FinishIndexJob` / `LatestIndexJob`（ADR-0007 §4.3 / `04` §4.1 的
  「先写表再执行」）；错误摘要截断到 512B、空串落 NULL。`indexer.go` 的 `RunIndex` 接入：
  先落 running，每 256 个文件上报进度，结束落 done/failed + files_total/done + 时长
  （终态上报用 `context.WithoutCancel` + 5s 超时——ctx 取消常伴随索引失败，「失败」恰是最该落库的事实）。
- **`backend/internal/knowledge/lockwait.go`** —— 写路径锁等待采样（有界 128 环 + 最近秩分位，
  `Samples` / `P50MS` / `P95MS` / `MaxMS` / `RetryFailures`）；进程内、无持久化，p95 为近似分位。
- **`backend/internal/sqliteutil`（`RetryLockedCtxObserved`）** —— 与 `RetryLockedCtx` 同语义，
  额外把单次调用累计退避上报给观察器；`RetryLockedCtx` 改为 nil-observer 包装。
  `store_sqlite.execWrite` 接线，并在重试耗尽（最终锁错误）时记 `observeFailure`。
- **HTTP 面（`runtimeapi/knowledge_handlers.go`）** —— `GET /api/runtime/knowledge/status`：
  未接线 / mode=off 返回 200 + off 载荷（404/503 会迫使调用方猜状态）；内部错误 500 统一错误体；
  路由注册于 `handler.go`，句柄经 `SetKnowledgeActivation` 注入（runtime-server 启动装配）。
- **CLI 面（`cmd/aicli/commands/knowledge.go`）** —— `aicli knowledge status [--workspace] [--json] [--timeout]`：
  工作区锚点与三入口同源（flag → runtime.yaml workspace.root → cwd）；`SkipInitialIndex`
  保证查询不触发索引；默认人类可读快照，`--json` 与 HTTP 载荷同形；mode=off 时打印显式启用提示。
- **测试**：`knowledge/status_test.go`（行数/job/锁等待/reader 降级/off 载荷/owner→reader）、
  `sqliteutil/lock_wait_observe_test.go`（无等待不上报 / 累计 / 非锁错误 / 取消仍上报）、
  `runtimeapi/knowledge_status_handler_test.go`（off / nil handler / 接线载荷 / nil 重置）、
  `cmd/aicli/commands/knowledge_status_test.go`（字节格式化 / 文本面字段 / off 提示 / flag 契约）。

### Changed

- **`knowledge/config.go`**：新增 `StorePathFor`（与 `Open` 落点逐字节一致，状态面不打开 store 也能报路径）。
- **`cmd/runtime-server/main.go`**：`handler.SetKnowledgeActivation(knowledgeActivation)`（与 shadow 观察器同源）。
- **`06` §1.1 / §4 Phase 1、`04` §5 Phase 1、`README` 阶段行**：交付 5 由「未开始」改为
  「已完成」，并登记落点与下一步（`Phase1-shadow` 实测：α 校准 + M1 复算）。

### Notes

- 锁等待口径：样本来自**本进程写路径**每次锁冲突的累计退避；进程重启清零（「当前进程经历过的
  锁竞争」才是诊断所需语义）。单写者拓扑下稳态应接近全零；非零即提示并发写者或长事务。
- 状态面契约：**只读**（不触发索引、不写库）、**nil-safe**（off / 未接线返回 off 载荷而非错误）、
  **可解释**（degraded_reason）。验收口径「锁等待 p95 < 50ms」待 `Phase1-shadow` 实测。
- 仍未收敛：`Phase1-shadow` 实测（α 校准 + M1 复算）——交付 5 是其观测入口。

---

## 2026-09-28 — Phase 1 交付 4（shadow 拦截 `grep` / `view`）

起因：关闭 `06` §4 Phase 1 交付 4 —— `mode=shadow` 下在既有 `grep` / `view` 执行路径上
拦截并旁路对比（ADR-0003 §4.3 / §4.4），使 `exploration_attribution` 开始产生数据、
M1 可复算。

### Added

- **`backend/internal/knowledge/shadow.go`（新增 `ShadowObserver` / `ShadowObserverFor` / `ShadowConfig` / `ShadowIndex` / `AttributionSink` / `ObservedCall`）** ——
  只读索引侧候选（`Search` / `FindSymbols`）与工具实际输出（baseline）逐调用对比，
  计算 `baseline_n` / `candidate_n` / `overlap_n` / `coverage` / `economy` / `usable`
  （ADR-0003 §4.2 口径；零结果 `coverage` 落 NULL，§4.5）；落库只写
  `query_hash = sha256(tool + "\x00" + pattern + "\x00" + scope)` 与
  `project_id = ProjectIDForWorkspace(ws)`（哈希短键，不含绝对路径，§4.6）。
  `DefaultShadowAlpha = 0.8` 仅为联调初值，**不得**作为验收门槛（§4.2 / §10）。
- **`usageledger.AppendExplorationAttribution`**（`sqlite_store.go`）—— 18 列插入，
  浮点为 NULL 语义；**不写 `token_usage_history`**（D3 不变量，由测试钉住）。
- **`agent.LoopReActConfig.OnToolObserved`**（`internal/agent/loop.go`）—— 在 MCP 分支
  与并行批次出口上报工具最终结果；hook 为 nil 时零行为变化。
- **三入口接线**：aicli cmd/tui（`applyLocalChatToolObservation`，`chat_actor_host.go`）、
  aicli acp（`attachSessionKnowledge` 挂 `ChatSession.Knowledge`，`agent_stdio.go`）、
  runtime-server（`Handler.SetKnowledgeShadow` + `applyAPISessionToolObservation`）。
- **测试**：`knowledge/shadow_test.go`（grep 覆盖率 / 零结果落库 / view 区间覆盖 /
  全覆盖可用 / 跳过与 mode=off / sink 错误语义 + 工厂与 project_id 契约）、
  `usageledger/sqlite_store_exploration_attribution_write_test.go`（回读 + 零基线 NULL +
  不污染 `token_usage_history`）、`agent/loop_observe_test.go`、
  `runtimeapi/knowledge_shadow_wiring_test.go`。

### Changed

- **acp 引用计数修正**（`agent_stdio.go`）：`attachSessionKnowledge` 与 TUI 同口径把句柄挂到
  `ChatSession.Knowledge`；`closeSessionLocked` 仅在句柄**未**挂在 chat 上时释放，
  避免与 `finalizeChatSession` 双释放导致 refs 少计。
- **`06` §1.1 / §4 Phase 1、`04` §5 Phase 1、`README` 阶段行**：交付 4 由「未开始」改为
  「已完成」，并登记落点与下一步（交付 5 → `Phase1-shadow` 实测）。

### Notes

- 观察器契约：**尽力而为**（落库失败只 debug，不冒泡为 turn 失败）、**只读**（不改工具
  结果）、`mode=off` / 未接线时 hook 为 nil，与无知识层逐字节一致。
- 仍未收敛：**交付 5**（`knowledge.status`）；`Phase1-shadow` 实测（α 校准 + M1 复算）。

---

## 2026-09-28 — 补记：ADR 0001/0003/0007 裁决为 Accepted、Phase 0 文档治理（#8 / #9）与 `index_jobs` DDL 迁移（#1）

起因：Phase 0 文档治理收尾与 Phase 1 门禁解除。owner 授权代改 ADR 状态并记录裁决；随后按
ADR-0007 §4.3 补做 `index_jobs` DDL 迁移。Phase 1 开工见下一条。

### Changed

- **ADR 裁决**：`adr/0001`、`adr/0003`（**仅口径**；§10 的 α 阈值仍受 `Phase1-shadow` 约束）、`adr/0007` 由 `Proposed` → `Accepted`（2026-09-28）；三项头部加 `Accepted` 记录行，`adr/README.md` §4 状态表 / §5 说明同步。**Phase 1 的 `Phase1-start` 门禁解除**。ADR 决策正文未改动（Accept 前已完成的 #11 / #16 修订见 2026-09-21 条目）。
- **#8 归档**：`00_Code_Intelligence_Project_Knowledge_Layer.md` 以 `git mv` 移入 `archive/`，文首加"归档说明"（仅供追溯，不作事实源）；`README.md` §1、`04` 评审对象行、`06` §9 同步。
- **#9a `03` 拆分**：`03_agent_harness_supplement.md` 拆为 `supplement/01`–`16`（正文逐段搬迁、章节号沿用原编号；`03` §5 并入既有 `supplement/05` §10），`03` 重写为**拆分索引 + 历史引用映射**（`03 §5.4(L674)` 更正为 `§6.4`）。引用同步：`README.md`、`04`、`06`、`adr/README.md`、`adr/0006`、`docs/lsp/*`（4 个文件）。
- **#9b `01` 边界**：`01` 文首加 **§0 定位与事实源边界**（逐节指认权威落点；正文未删减）；逐节删减登记为 **#18**（Phase 1 开工前）。
- **#1 `index_jobs` 迁移**：`04` §4.3 的 `CREATE TABLE index_jobs` + 索引**逐字节**迁至 [`supplement/15_change_management.md`](supplement/15_change_management.md) §15.3（extension schema）；`04` 只留用途 / 验收指标 / 引用（ADR-0007 §4.3）。

### Notes

- 本轮为**文档结构治理**：不改变设计决策、不改变 DDL 语义、不改动 `Accepted` ADR 的决策正文。
- 仍未收敛（`06` §9）：**#2** `02` §8 三分组、**#3** I1–I5 不变量脚本、**#19** `04` §4.3 其余 15 张表 DDL 的引用化（依赖 **#7** 对 "v1 ≤ 16 张" 口径的裁决）、**#18** `01` 逐节删减。

---

## 2026-09-28 — Phase 1 交付 6（接入/激活）+ 探索归因 9 指标暴露

起因：Phase 1 门禁（ADR-0001 / 0007 / 0003 口径）已解除，开工。本次关闭两项
此前"只有文档、没有代码"的交付：`06` §4 Phase 1 交付 6「接入（激活）」与
`06` §5.2 / 附录 A 步骤 6「`usageanalytics` 暴露 9 个探索归因指标」。

### Added

- **`backend/internal/knowledge/activation.go`（新增 `Activation` / `Activate` / `ActivationOptions`）** ——
  三个入口共用的"接入"原语：打开 store → 判角色（owner/reader）→ owner 在后台跑首次全量索引。
  `mode=off` 时在任何磁盘操作之前返回 `(nil, nil)`；`Close` 取消并**等待**后台索引退出（索引持写事务，
  先关 store 会撞锁）。`Activation` 所有方法 nil-safe，接入方无需分支。
- **`backend/cmd/aicli/commands/chat_knowledge.go`（新增）** —— `cmd/aicli`（chat/tui）与
  `cmd/aicli agent stdio`（ACP）共用：按 workspace 引用计数的接入表 + 进程级一次性释放。
  一个 ACP 宿主进程可服务多个 workspace，故不能"进程级单例 + 首次 Close 释放"。
- **`backend/cmd/runtime-server/knowledge_boot.go`（新增）** —— `bootRuntimeServerKnowledge`：
  workspace 锚点 = `runtime.yaml` 的 `workspace.root`（相对配置文件解析）→ 进程 cwd；
  两者皆空则 warn + 降级为 off；`mode=off` 时不调用 `Activate`（零副作用）。

### Changed

- **`backend/cmd/aicli/commands/chat.go`** —— `ChatSession` 新增 `Knowledge *knowledge.Activation`；
  `HandleChat` 在 exit-cleanup 注册处接入 `attachChatKnowledge` + `releaseAllChatKnowledge`。
- **`backend/cmd/aicli/commands/chat_setup.go`** —— `buildChatFinalCleanup` 在 finalize 会话后
  归还引用。
- **`backend/cmd/aicli/commands/agent_stdio.go`** —— `acpHostSession` 新增 `knowledge` /
  `knowledgeWorkspace`；`session/new` 在记录 workspace 之后接入；`closeSessionLocked` 归还引用。
- **`backend/cmd/runtime-server/main.go`** —— `runtimeServerApp` 新增 `knowledge` 字段，启动阶段
  （workspace 解析后、对外服务前）调用 `bootRuntimeServerKnowledge`；`close()` 最后释放知识层。
- **`backend/internal/api/runtimeapi/usage_ledger_group.go`** —— `usageLedgerProfileGroup` 增加
  9 个探索归因字段（**全部 `omitempty`**），`aggregateUsageLedgerByProfile` 按组求和。
- **`backend/pkg/skillsapi/client.go`** —— `UsageLedgerRecord` 增加 9 字段；新增
  `UsageLedgerProfileGroup`；`GetUsageLedgerResponse` 增加 `group_by` / `groups` / `grouped_total`
  （`omitempty`）；`GetUsageLedgerParams` 增加 `GroupBy`，客户端在非空时才发 `group_by` 查询参数。

### Notes

- **`mode=off` 硬不变量**：三入口在 off 下不建库、不建锁文件、不起 goroutine，行为与"无知识层"
  逐字节一致；9 个归因指标在 off 下恒为 0 且因 `omitempty` 不出现在任何响应里。
- **刻意偏离 `06` §4 Phase 1 交付 6 的措辞**：计划写"向 `internal/background` 注册
  `knowledge.index.initial` 任务"，但 `internal/background.Manager` 只有 shell 作业通道
  （`SubmitShell`），进程内任务无公开注册口。把索引塞进 shell 作业会多起一个进程并重复打开同一个
  store，反而破坏单写者不变量。因此首次全量索引由 `Activate` 内部后台 goroutine 承担
  （同样不阻塞启动 / turn，Close 可取消并等待退出）。
- **`06` §5.2 的路径近似**：该表把"暴露 9 指标"的落点写成 `internal/usageanalytics/*`，但该包不读
  `usageledger`；ledger 的实际读取与聚合面在 `internal/api/runtimeapi`（`GetUsageLedger`）。
  本次按实际落点实现，`06` 属索引文档、不构成决策依据。
- 验证：`go build ./...` 干净；`go test ./internal/knowledge/ ./internal/usageledger/
  ./internal/api/runtimeapi/ ./pkg/skillsapi/ -count=1` 全绿。

---

## 2026-09-28 — Phase 0 交付 7 落地：`exploration_attribution` 建表

起因：核对"Phase 0 是否真的可验收"时发现，`04` §5 / `06` §4 的 Phase 0 交付 7
（`exploration_attribution` 表 + 2 索引，2026-09-21 由 §9.2 #15 补入归属）**只有文档、没有代码**：
`usageledger` 的 `init()` statements 里既无建表也无索引，
Phase 0 验收门槛"可被 `sqliteutil.OpenFileCtx` 打开且重复 init 幂等"此前无法通过。

### Changed

- **`backend/internal/usageledger/sqlite_store.go`** —— `init()` 的 statements 追加
  `CREATE TABLE IF NOT EXISTS exploration_attribution`（18 列，与 ADR-0003 §4.1 逐列对齐）
  与两个索引 `idx_exploration_attribution_created_at` / `idx_exploration_attribution_tool_time`。
  复用既有 `IF NOT EXISTS` 幂等语义（ADR-0003 D7），**只建表与索引，不产生数据**。
- **`backend/internal/usageledger/sqlite_store_exploration_attribution_test.go`**（新增）——
  4 个用例：①列集与两索引齐全且表为空；②同一 DSN 反复 init 幂等、`sqlite_master` 只登记 1 次；
  ③可被 `sqliteutil.OpenFileCtx` 打开（验收门槛原文）；④D3 非污染——重新 init 后
  `token_usage_history` 的行数与 token 聚合逐字节不变。

### Notes

- **`mode=off` 行为零变化**：只新增空表与索引，没有任何写入路径，ledger 记录与聚合结果与改动前一致。
- 验证：`gofmt`/`go vet` 干净；`go test ./internal/usageledger/ ./internal/knowledge/ -count=1` 全绿。
- 仍未落地（不属本次范围）：`06` §5.2 / 附录 A 步骤 6 提到的"`usageanalytics` 暴露 9 个新指标"
  与 §9 文档治理尾项 #8 / #9；Phase 1 及之后仍受 ADR-0001 / 0003 / 0007 门禁约束。
  （**2026-09-28 更新**：9 指标已在本文件"Phase 1 交付 6（接入/激活）+ 探索归因 9 指标暴露"条目中落地；#8 / #9 与 `index_jobs` 迁移已落地；ADR-0001 / 0003 / 0007 已 Accept、门禁解除——见顶部"补记"条目。）

---

## 2026-09-21 — 修复规划缺口（#10–#16 关闭；修复中另发现并修复 #17）

起因：owner 指示"针对缺口进行修复"。上一条（核查发现）登记的 7 项缺口**已全部修复**；
修复过程中又发现**同类缺陷 1 项**（#17，ADR-0004 陈旧度阈值 Gate），一并修复。
`06` §9.1 保留为发现记录，新增 §9.2 为修复记录。

### Changed

- **`adr/0003-exploration-attribution-metrics.md`**（仍 `Proposed`，修订就地标注日期）
  - 头部 `Gate`：#11 —— 阈值 Gate `Phase0-baseline` → **`Phase1-shadow`**。
  - §4.1：#16 —— D3 由字面"一列不改、一行不变"改写为**三条可检验形式**（不新增行 / 不改变既有聚合 / 不改变历史行语义），并注明 Phase 0 已实现的 9 列 `ADD COLUMN … DEFAULT 0` **满足**该实质要求。
  - §8 验证表"不污染"行同步改写。
  - §1.2 / §2 D4 / §4.2 / §5 / §6.1 / §10：α 的产出点由"Phase 0 产出"改为"Phase 1 shadow 实测产出"；§10 增"Gate 变更说明"。
- **`04_completeness_review_and_optimized_plan.md`**
  - §5 Phase 0：#15 —— 新增交付 7 `exploration_attribution` 表（只建表与埋点骨架，不产生数据）；验收门槛补"重复 init 幂等"。
  - §5 Phase 1：#10 —— 新增交付 6「接入（激活）」（runtime-server / aicli cmd+tui / aicli acp）；#13 —— 交付 4 由 `code.search` 改为**拦截既有 `grep`/`view`**；#14 —— 验收门槛改为**主门槛（M1）+ 诊断指标**。
  - §7.6：#11 —— 校准流程改为"Phase 0 出基线与警告 / Phase 1 shadow 出阈值"。
- **`06_implementation_index_and_guidance.md`**
  - §1.2 / §3 / §4 Phase 0 / §4 Phase 1 / §4.2 / §5.1 / §5.2 同步上述修订；新增 §9.2 修复记录表。
- **`reports/phase0_baseline_report.md`**：#12 —— 新增 §7（ADR-0003 §6.2 强制内容）：`coverage` 低估警告 + 抽样核对状态（Phase 0 无 shadow 数据，顺延至 `Phase1-shadow`）+ Phase 1 执行清单。
- **`README.md`**：顶部阶段行与 §1 状态表更新；§2 门禁清单加入 `0003`；§7 新增"阈值类 Gate 必须可达成"。
- **`adr/0004-stale-index-tool-surface.md`**：#17 —— `S_fresh` / `S_max` 的 Gate 由 `Phase0-baseline` 改为 **`Phase2-start`**；§4.1 “由 Phase 0 校准”同步修订。
- **Gate 词汇表登记处**（新增 `Phase1-shadow` 取值）：`GLOSSARY.md` §… 字段表、`adr/0000-template.md`、`adr/README.md` §4 Gate 表 / §5 索引表 / §8 B3 行、`supplement/05` §9.3 表。

### 修复性质（供 owner 复核）

| 类别 | 项 | 说明 |
|---|---|---|
| 文档一致性修复（**不改变设计决策**） | #10 #12 #13 #15 | 把已存在于 `supplement/05` 或 ADR-0003 的内容补上 Phase 归属，或对齐两套口径 |
| 修订 `Proposed` ADR 正文 | #11 #14 #16 #17 | `0003` / `0004` 仍为 `Proposed`，按 `adr/README.md` 在 Accept 前修订属正常流程；改动已就地标注日期，owner 可在 Accept 时一并复核 |

### Notes

- **#11 是本批的关键**：原 Gate 结构性不可达（Phase 0 是 `mode=off`，产不出 shadow 对比数据），
  会让 ADR-0003 永远无法 Accept、Phase 1 被自锁。修订后 **ADR-0003 的 Accept 不再被阈值阻塞**。
- **#17 是同一根因的第二个实例**：凡是“需要索引 / shadow / reader 观测数据”的阈值，都不能挂在
  `Phase0-baseline`。已在 `GLOSSARY.md` 的 `Gate` 定义里写明这条判据，防止再犯。
- **#16 未改任何代码**：Phase 0 已实现的 `ADD COLUMN`（请求级粒度、历史行取 `DEFAULT 0`）本就满足
  D3 的实质要求，冲突只在字面；修订方向是**把 D3 写成它本来的意思**，不是放宽它。
- 仍未解决但已登记的相邻项：`04` §5 Phase 1 的"≤ 120s / ≤ 200MB"初值已被实测击穿
  （146.9s / 247.5 MiB），Gate 是 `04` §7.4 评审，**不属于本次 7 项缺口**。

---

## 2026-09-21 — 核查发现 7 项 Phase 1 规划缺口（未改任何事实源；同日已修复，见上条）

起因：复核"`knowledge.Open` 未接入 aicli → shadow 为 no-op"与"5 条 ADR 仍 Proposed"两条状态时，
追问"这是否只是未实现"。结论：**不是**——其中 7 项属于**计划自身的缺口**（任务写在规格或 ADR 里，
却没有 Phase 归属；或两个事实源对同一验收给了不同口径），已登记为 `06` §9.1 #10–#16。

### Changed

- `docs/knowledge_Layer/06_implementation_index_and_guidance.md` — 新增 §9.1"规划缺口"表（#10–#16）；
  §1.2 阻塞清单同步标注（ADR-0003 阈值门禁不可达 + Phase 1 规划缺口 7 项）。

### Findings（摘要）

| # | 缺口 | 证据 |
|---|---|---|
| 10 | Phase 1 缺"激活"交付项：`knowledge.Open` 接入 aicli / runtime-server 只在 `supplement/05` §2/§8，`04`/`06` 的 Phase 1 交付与文件落点均无 | `supplement/05` L107/L124/L510/L576-578 |
| 11 | ADR-0003 阈值门禁 `Phase0-baseline` 结构性不可达（α 需 shadow 数据，而 Phase 0 无 shadow） | `adr/0003` §10、§4.2 |
| 12 | ADR-0003 §6.2 强制内容（coverage 低估警告 + 抽样核对）未写进 Phase 0 报告 | `adr/0003` §6.2 vs 报告全文 |
| 13 | Phase 1 shadow 以 `code.search` 定义，但 `code.search` 是 Phase 3 交付 | `04` §5 Phase 1 交付 4 vs Phase 3 交付 1 |
| 14 | Phase 1 验收两套口径：`04` "top-10 差异率 < 15%" vs ADR-0003 §4.5 M1 主门槛 | `04` §5 Phase 1 验收 vs `adr/0003` §4.5 |
| 15 | `exploration_attribution` 表仅存在于 ADR-0003，无 Phase 归属（M1 由它计算） | `adr/0003` §4.1；`04`/`06` 全文无 |
| 16 | ADR-0003 D3 字面"一列不改"与已实现 Phase 0（`ADD COLUMN` 9 列）冲突 | `sqlite_store.go` L236-244/L262-287 |

### Notes

- **本次只登记缺口，未修改 `04` / `adr/*` 等事实源**——按 `README.md` §6 变更流程，
  涉及验收口径与决策的改动须先经 owner / 评审裁决。
- #10 与 #11 互为因果：激活项无归属 → Phase 0 拿不到 shadow 数据 → ADR-0003 阈值门禁无法满足
  → Phase 1 被自锁。**这两项应作为同一个问题裁决。**

---

## 2026-09-21 — Phase 0 状态定稿：核心 5 交付完成

### Changed

- `docs/knowledge_Layer/06_implementation_index_and_guidance.md` — Phase 0 状态定稿为"核心 5 交付已完成"
  （非全面封版：文档治理尾项 §9 条目 8 / 9 仍挂起）。添加 Phase 1 进入条件与校准建议
  （3 个仓库样本：首次全量 ≤ 0.6ms×refs 且 ≤ 300s；DB ≤ 1KiB×refs 且 ≤ 300MB）。
- `docs/knowledge_Layer/reports/phase0_baseline_report.md` — 状态由"部分完成"→"完成"；
  §6 标题与交付 3 判定表更新为"完成（A/B 延期）"。

### Notes

- Phase 0 工程验收已就绪：5/5 交付落地 + ledger 可复算 + `TestPhase0TaskBaselineSummarize` PASS。
- 进入 Phase 1 的两个硬门禁仍未满足：ADR-0001 + ADR-0007 需 owner Accept；ADR-0003 阈值
  待 `04` §7.6 校准落稿。

---

## 2026-09-21 — Phase 0 任务侧基线（mode=off）完成

起因：`usageledger` 接入 aicli（`chat_cache_local.go`）后，`reports/phase0_baseline_report.md`
§6 的"任务侧"子项得以执行。

### Added

- `backend/internal/knowledge/baseline_run_test.go` — `TestPhase0TaskBaselineSummarize`：
  从 `gateway.db token_usage_history` 读取 7 条 `llm_runtime` 记录，调用
  `BaselineReport.Summarize` + `Percentiles`，产出 mode=off 任务侧基线。

### Changed

- `docs/knowledge_Layer/reports/phase0_baseline_report.md` — §6 回填 5 个真实任务的
  基线结果（7 LLM 请求 / 150,768 tokens / p50=6,000ms / p95=12,600ms）；标题与状态
  更新为"任务侧 mode=off 完成"。
- `docs/knowledge_Layer/06_implementation_index_and_guidance.md` — §4 Phase 0 交付 3
  状态更新：任务侧 5 个真实任务已跑，A/B（off vs shadow）延期至 Phase 1。

### Results

| 指标 | 值 |
|---|---|
| 样本 | 7 LLM 请求 / 5 真实任务 |
| TotalTokens | 150,768 |
| SuccessfulTasks | 7 |
| FailedTasks | 0 |
| ExplorationTokenShare | 0.000000（mode=off） |
| ToolCallsPerTask | 0.00 |
| RepeatedReadPerTask | 0.00 |
| Latency p50 | 6,000 ms |
| Latency p95 | 12,600 ms |
| SafetyViolations | [] |

### Notes

- `shadow` A/B 延期：`cmd/aicli/` 未接入 `knowledge.Open`，`mode=shadow` 在 aicli 为
  no-op，待 Phase 1 知识层接入后补跑。

---

## 2026-09-21 — 补上 LSP 落地文档的反向引用

起因：`docs/lsp/`（2026-09-21 建立）已在自身 README 声明"服务于 `docs/knowledge_Layer` 已评审的 LSP 规格"，但本目录**没有任何反向指针**，事实源与落地方案文档脱节（缺口）。

| 文件 | 改动 |
|---|---|
| [`README.md`](README.md) | §1 表格后增加"跨目录入口"说明；§3 单一事实源声明表新增 LSP 落地方案行 |
| [`06_implementation_index_and_guidance.md`](06_implementation_index_and_guidance.md) | §2 文档地图新增 `../lsp/` 行 |
| [`adr/0002-acp-lsp-ownership.md`](adr/0002-acp-lsp-ownership.md) | §10 Open Follow-ups 新增一条（Gate = `Phase4-start`） |
| [`adr/0006-lsp-position-encoding-boundary.md`](adr/0006-lsp-position-encoding-boundary.md) | §10 Open Follow-ups 表后新增"实现侧落点"引用，重申本文件仍是编码转换边界与缓存键的唯一事实源 |
| [`supplement/05_runtime_integration_project_detection_and_lsp.md`](supplement/05_runtime_integration_project_detection_and_lsp.md) | §9 已裁决问题表前新增 `../../lsp/` 实现侧入口与边界声明（不新增散文条目，仅引用） |

本次改动**不新增 schema、不改 DDL、不改任何决策**，仅建立引用关系。

---

## 2026-09-20 — Phase 0 续：轻索引收回到"顶层声明"（builtin/3）与相邻计划交叉评审

起因：builtin/2 的首次诚实基线显示仍有 **5080 行符号身份被合并**（1093 个文件）。一次性诊断
（对全仓跑一遍 `Extract` 并按 `stable_key` 分组）推翻了初版归因：合并行里 **4295 行（84.5%）是
函数体内的局部变量**（`var output bytes.Buffer`、`let container: HTMLDivElement;`）——适配器把
Deep Index 的内容放进了 Light Index，直接违反 `04` §2 的 Lazy 原则（"v1 默认只索引文件 +
**顶层符号** + imports；方法体、局部变量、类型关系不索引"）。

### Changed

- `backend/internal/knowledge/adapter_builtin.go` — 新增 `topLevelOnly(language)` / `isIndented(line)`：
  go / typescript / javascript 的声明规则只接受**顶格**声明（Python 早已用 `^def`/`^class` 锚定；
  Rust 的 impl 方法依赖缩进作用域，不适用）。缩进行仍照常产出**调用点引用**。
- `backend/internal/knowledge/version.go` — `AdapterVersion` `builtin/2` → `builtin/3`。
- `docs/knowledge_Layer/reports/phase0_baseline_report.md` — §2/§3 回填实测数字；§4.4 重写为真实
  归因（附 kind / 语言分布）；新增 §4.5（残余 732 行的归因）、§4.6（refs 口径变化）。
- `docs/knowledge_Layer/06_implementation_index_and_guidance.md` — §8 修订 `cache_entries` 一行
  （与 `llm-cache-analytics` 计划是**命名撞车**，不是同一张表）；Phase 0 交付 5 标记完成。

### Added

- `backend/internal/knowledge/adapter_scope_test.go` — 顶层/局部作用域回归用例。
- `docs/knowledge_Layer/reports/phase0_cross_review.md` — Phase 0 交付 5：与 5 份相邻计划 +
  `02`/`03` schema 事实源的交叉核对（6 条动作项，其中 A6 是 Phase 1 硬门禁）。

### Fixed

- **局部变量被当成符号**：builtin/2 下同目录同名同签名的局部变量塌缩成同一身份，本仓库实测
  1093 个文件 / 5080 行被合并。builtin/3 后降到 **229 个文件 / 732 行**（−85.6%）。
- **缩进声明行吞掉调用点**：builtin/2 对 `var x = f(...)` 这类行先按声明匹配再 `continue`，
  行内真实调用 `f(...)` 丢失；builtin/3 让这些行落进调用点扫描，refs 反而 +2.2%
  （372286 → 380657）。两处同源：都在把轻索引从错误的作用域收回正确的作用域。

### Deviations / 已知限制（需评审）

- `symbolNamespace` 用"文件所在目录"表达包/模块边界：对 Go（包=目录）正确，对 TS/JS/Python
  （模块=文件）过粗，残余 732 行合并即由此产生（`scripts/*.go` 的 build-tag 隔离多 main 程序同理）。
  收敛方案（Go=目录、TS/JS/Python=文件、build-tag 文件加文件名分量）列入 Phase 1，
  届时需再升一次 `AdapterVersion` 并重跑基线。
- 冷启动样本 n=3（本仓库 + gin + prometheus），`06` Phase 1 的 120s / 200MB 初值按报告 §5
  建议改为**每 ref 口径**（≤ 0.6ms × refs、≤ 1 KiB × refs），定稿需 `04` §7.4 评审。

### 外部仓库对照（同日追加，报告 §2.4）

`git clone` 在本机网络下只有 ~13 KB/s（7 分钟仅 5 MB），改用 codeload tarball + `tar -xzf`；
两个外部样本的测量命令与 §1 完全相同，仓库为 gin `3b08cd72`（99 文件）、prometheus `1d6fe378`（1010 文件）。

| 指标 | gin | prometheus | 本仓库 |
|---|---|---|---|
| 首次全量 | 2.66s | 47.3s | 146.9s |
| 每文件 | 26.9ms | 46.9ms | 38.1ms |
| 每 ref | 0.269ms | 0.359ms | 0.386ms |
| DB 每 ref | 701 B | 647 B | 682 B |
| 合并身份 | 1.06% | 1.24% | 1.68% |

结论：**"每文件"不是稳定口径，"每 ref"才是**（跨仓库 ±20% 内），Phase 1 门槛应按 refs 表达。

### 开启 usage_ledger（任务侧基线前置）

报告 §6 步骤 1 要求"跑任务前必须打开 usage_ledger"。修改 `backend/configs/config.yaml` 与
`config.runtime.snapshot.yaml` 的 env 默认 `SKILLS_RUNTIME_USAGE_LEDGER_ENABLED:-false`→`:-true`
（仍可 `SKILLS_RUNTIME_USAGE_LEDGER_ENABLED=false` 关）；代码默认仍为 `false` 不变。bench 证据
（`internal/usageledger/sqlite_store_bench_test.go`）：单写 **3.09ms/op**、并发 0 丢失、
444 bytes/记录（100k ≈ 42 MiB）——3 ms ≪ 一次 LLM 轮次，故默认开启不增可感知延迟。
同时在 `~/.aicli/.env` 追加 `SKILLS_RUNTIME_USAGE_LEDGER_ENABLED=true` 作为运行时开关
（`~/.aicli/config.yaml:13595` 的 `${...:-false}` 订阅优先，重启 aicli 后生效）。

---

## 2026-09-20 — Phase 0 开工：知识层骨架落地与 `stable_key` 身份缺陷修复

起因：`backend/internal/knowledge/` 存在一份**未提交且无法编译**的半成品（而 `06` §4 仍写"Phase 0 未开始"）。
本次把它补成可编译、可测量的 Phase 0/1 骨架，并在首次真机基线上发现一个会让索引**静默丢掉三分之一文件**的缺陷。

### Added

- `backend/internal/knowledge/baseline.go`、`baseline_test.go` — `04` §7.7 基线报告的数据面（token 构成、重复读取、p50/p95 复算）。
- `backend/internal/knowledge/baseline_index_test.go` — 索引侧基线测量入口（`KNOWLEDGE_BASELINE_REPO=<path>` 开启，默认 skip）。
- `backend/internal/knowledge/telemetry.go`、`telemetry_test.go` — 9 个探索归因字段的采集器（并发安全 `Recorder`）。
- `backend/internal/config/knowledge_config_test.go` — `ValidateKnowledgeConfig`（实现在 `manager.go`，
  复用 `knowledge.DefaultConfig()` / `ParseMode`）的用例：mode 三值解析、负数限额拒绝。
- `backend/internal/usageledger/sqlite_store_knowledge_metrics_test.go` — 9 字段 round-trip 与**旧 13 列库升级**用例。
- `docs/knowledge_Layer/reports/phase0_baseline_report.md` — Phase 0 基线报告（索引侧已实测；任务侧待跑）。

### Changed

- `backend/internal/usageledger/sqlite_store.go` — 新增 9 列，并用 `PRAGMA table_info` 做幂等 `ALTER TABLE` 补列（旧库兼容）。
- `backend/internal/model/entity/token_usage_history.go` — 9 个 `omitempty` 归因字段。
- `backend/configs/runtime.yaml`、`runtime.win7.yaml` — 新增 `knowledge:` 段，默认 `mode: "off"`。
- `backend/internal/knowledge/adapter_builtin.go` — `stable_key` 补上 `namespace` 分量（`symbolNamespace`）。
- `backend/internal/knowledge/store_sqlite.go` — `ReplaceSymbols` 容忍 `stable_key` 冲突：保留身份行、改挂宿主，并记录 `adapter_conflict`。
- `backend/internal/knowledge/version.go` — `AdapterVersion` `builtin/1` → `builtin/2`（让 builtin/1 写出的错误身份表自然失效）。

### Fixed

- **`stable_key` 缺 `namespace`（`04` §4.4 的 `normalize(namespace_or_package_or_module)` 被传成空串）**：本仓库 3860 个候选文件里有
  **1365 个（35%）**的符号写入被 `idx_symbols_stable` 唯一约束拒绝，`ReplaceSymbols` 整份失败、`refs` 一并丢失，
  且 `RunIndex` 只把它计入 `errors`，从表面看像是"解析失败"。修复后 `errors=0`，符号数 22348 → 60899。

### Deviations（偏离设计文档，需评审）

- `04` §4.4 歧义规则要求"stable_key 冲突时**使该符号 `confidence` 降级**"，但 v1 DDL 的 `symbols` 表**没有 `confidence` 列**，
  因此只能落地为"记录 `adapter_conflict` 事件 + 保持身份行唯一"。是否加列待 Phase 1 决定。
- 冲突时采用 **last-writer-wins**（身份不变、`file_id` 改挂最近写入的文件），而不是丢弃后写入者：
  丢行会让符号从库里消失，LWW 只换宿主，`refs` 指向的 `symbol_id` 不受影响。

---

## 2026-09-20 — 决策层建立与文档一致性修复

本次变更的起因是一次文档完整性核查，发现三类缺陷：

1. `README.md` 声明 `adr/` 为"决策唯一事实源"，但该目录**不存在**；
2. `adr/` 建成后其索引表列了 7 个 ADR，其中 6 个**从未落盘**（悬空链接）；
3. `02` §8 的总览清单（28 个名字）与其 `CREATE TABLE` 区块（22 张）**不一致**，且 `04` 承载了本不属于它的 DDL。

### Added

- **`adr/` 决策记录目录**（此前仅在 `README.md` §1 中声明为"待创建"）：
  - `adr/README.md` — ADR 索引与流程（状态生命周期、可逆性分级、Decided-by gate、索引表）
  - `adr/0000-template.md` — ADR 模板
  - `adr/0001-project-module-language-schema.md` — Project/Module/Language 模型收敛
  - `adr/0002-acp-lsp-ownership.md` — ACP 下 LSP 归属与能力面
  - `adr/0003-exploration-attribution-metrics.md` — 探索归因与 shadow 差异率度量
  - `adr/0004-stale-index-tool-surface.md` — 陈旧索引下的 `code.*` 工具面
  - `adr/0005-windows-child-process-lifecycle.md` — Windows 子进程树生命周期与复用既有 process guard
  - `adr/0006-lsp-position-encoding-boundary.md` — LSP 位置编码转换边界与缓存键
  - `adr/0007-phantom-tables-and-doc-invariants.md` — 幽灵表清理与文档不变量
- **`GLOSSARY.md`** — 术语表（此前声明为"待创建"；本次建立，覆盖现有文档中已定义或反复使用的术语）
- **`CHANGELOG.md`** — 本文件

### Changed

- **`README.md`**
  - §1 文档状态表：`adr/` 由“待创建”改为“已建立（`0000` + `0001`–`0007`，全部 `Proposed`）”；
    `GLOSSARY.md` / `CHANGELOG.md` 由“待创建”改为“已建立（2026-09-20）”；
    新增 `supplement/` 行（**部分建立**，当前仅 `05`），`03` 行状态细化为“待拆分（`supplement/05` 已建立）”。
  - §2 实施者阅读顺序：新增“先读 `adr/`”，并声明 `04` 附录 B **不是**决策依据；
    `supplement/01` 引用改为 `03` §1（标注“待拆分为 `supplement/01`”），`02` 的提示改为“待补应用顺序声明”标注。
  - §3：已裁决关键决策的指针由"`04` §3.2 与附录 B"改为 `adr/`。
  - §7 关键约束：DDL 约束补充"extension schema 例外"与"`04` 不得含 `CREATE TABLE`"。

- **`supplement/05_runtime_integration_project_detection_and_lsp.md`**
  - §2.3：LSP 默认策略由 `external_preferred` 改为 `off`，并加 ADR-0002 指针与废弃理由
    （ACP v1 能力面中不存在任何 LSP 能力位，"先探测 editor 是否提供 LSP"在 v1 无对象可探测）。
  - §2.4 入口对照表：同步该默认值。
  - §3.1：Project/Module 收敛的表述改为指向 ADR-0001（对应 `04` 附录 B 的 B6）。
  - §9：由"待裁决问题（需 ADR）"改为"已裁决问题（ADR 索引）"，含 5 项裁决映射表
    与 2 项新增 ADR；原始提问移入 §9.1 保留备查。

- **`adr/README.md`**
  - §5 索引表：`0005`/`0006` 标题与文件对齐；`0007` 的"取代"列补齐。
  - §5：新增落盘状态与"ADR 不得复制 DDL"说明。
  - §7：明确 DDL 规则的**精确边界**（不得复制已存在 DDL；新增对象可给拟议 DDL；
    本规则只约束 `knowledge.db`，其他库以自身代码为事实源）。
  - §8：新增 `04` 附录 B → ADR 的已完成映射（B3 → 0003；B6 → 0001；B15 待转）。

- **`04_completeness_review_and_optimized_plan.md`**
  - 附录 B：加注"本附录是散文列表，不构成决策依据"，指针改指 `adr/`，记录已完成映射。
  - 第 10 节 `index_jobs`：加注 **DDL 越位**（ADR-0007 §4.3），待 ADR 被 Accept 后迁移到 extension schema。

- **`02_agent_harness_technical_design_spec_sqlite.md`**
  - §8 数据模型总览：加注"清单 28 名 vs DDL 22 张"，列出 6 个无 DDL 的名字
    （`branches`/`inheritance`/`dependencies`/`language_projects`/`index_jobs`/`events`）
    与对应 ADR，并说明 ADR-0007 要求的三分组重构。
  - 文件头：加注"**待补 schema 应用顺序声明**"（`04` 附录 B 的 B15）。

- **`03_agent_harness_supplement.md`**
  - §5.3 位置编码规则：加注 ADR-0006 指出的 6 项缺口（协商、canonical 定义、
    非 BMP/组合字符、BOM/CRLF、`document_version` 语义升级、`lsp_servers` 补列）。
  - §5.2 `lsp_servers`：加注待补 `position_encoding` 列（ADR-0006 §4.3）。

### Notes

- **本批新增的 7 个 ADR 全部为 `Proposed`**，需 owner 按 `adr/README.md` §2 逐个 Accept。
  `Accepted` 之后 ADR 正文不可修改，要改需新写 ADR 并在 `Supersedes` 引用旧号。
- **凡 ADR 要求改动其他文档正文的，本次一律只加指针/标注，不执行正文改动**，
  以避免在决策被接受前产生既成事实。受此约束的待办：
  - `04` L546 的 `index_jobs` DDL 迁移（ADR-0007 §4.3）
  - `02` §8 的三分组重构与不变量脚本（ADR-0007 §4.2/§4.4）
  - `03` §5.3 规则改写与 `lsp_servers` 补列（ADR-0006 §4.1–§4.3）
- **唯一例外**是 `supplement/05` §2.3 的 `external_preferred`：该表述所依赖的能力面
  在 `backend/internal/acp/types.go` 中**不存在**，属**事实错误**而非设计分歧，故直接修正。
- **仍未执行（既有建议）**：`00_Code_Intelligence_Project_Knowledge_Layer.md` 移入 `archive/`
  （`README.md` §1 已标注，涉及链接改写，未在本批处理）。**2026-09-28 已执行**：`git mv` → `archive/` + 文首归档说明，见顶部"补记"条目与 `06` §9.3。
- **仍未解决（已知矛盾，待裁决）**：`04` §4.3 声明的 "v1 表集 ≤ 16 张" 与 `02` 实际的
  22 张 core DDL 疑为不自洽。ADR-0007 的不变量 **I5** 专门检测此项，由检查结果裁决，ADR 未预设结论。

- **同日补记（第二轮一致性核查）**：`02` 文件头的"待补 schema 应用顺序声明"标注，
  在首批编辑中因 `multiedit` 部分失败（失败项 `old_string` 起于 `> 适用：AI Co...`）
  而**未实际落盘**；本次已补写于 `02` 顶部，`CHANGELOG` 原记录描述的是预期状态，
  现与磁盘一致。同轮另修正 `README.md` §1 中 `GLOSSARY.md` / `CHANGELOG.md` 的
  "待创建"陈旧状态、新增 `supplement/` 行、并修正 §2 对 `supplement/01`（尚未落盘）的引用。

- **新增 `06_implementation_index_and_guidance.md`（方案实施索引与指引）**：以实施者视角重组本目录——
  §3 ADR 门禁表（含 `Gate`、阻塞 Phase、状态）、§4 分阶段实施指引（Phase 0–8，每项含目标 / 前置 ADR /
  交付 / 文件落点 / 验收 / 回滚）、§5 文件落点索引（新增 / 修改 / 明确不新增）、§6 验收与度量入口、
  §7 变更与治理流程、§8 边界、§9 已知阻塞与待办、附录 A Phase 0 首日清单。
  本文定位为**索引 / 指引**：不复制 DDL、不复制阈值、不复制 ADR 正文，冲突时以事实源原文为准。
  同步更新 `README.md` §1 状态表、§2 实施者阅读顺序、§7 元文档例外，以及 `04` 附录 C.1 文件清单。
