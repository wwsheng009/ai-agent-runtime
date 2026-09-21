# CHANGELOG — Code Knowledge Runtime（知识层）

> 本文件是 `docs/knowledge_Layer/` 的**变更历史唯一事实源**（见 [`README.md`](README.md) §3）。
> 变更流程见 [`README.md`](README.md) §6。
> 本目录在 2026-09-20 之前没有变更记录；下列为首批条目。

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
  （`README.md` §1 已标注，涉及链接改写，未在本批处理）。
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
