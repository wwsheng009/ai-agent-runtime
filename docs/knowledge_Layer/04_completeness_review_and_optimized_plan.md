# knowledge_Layer 方案完整性评审与优化落地计划

> 日期：2026-09-20
> 评审对象：`archive/00_Code_Intelligence_Project_Knowledge_Layer.md`（2026-09-28 归档）、`01_low_token_multilanguage_ai_agent_harness_design.md`、`02_agent_harness_technical_design_spec_sqlite.md`、`03_agent_harness_supplement.md`（2026-09-28 拆分为 `supplement/*`）
> 结合基准：本仓库 `E:\projects\ai\ai-agent-runtime`（Go / SQLite / Windows 优先）
> 难度评级：hard（架构 + 跨模块一致性 + 迁移风险，需要分阶段验证）
> 结论一句话：**方向正确、分层合理，但当前四份文档尚不是"可执行方案"，而是"设计意图 + 规格草稿 + 补充清单"的混合体；必须补上置信度定义、单写者并发模型、与现有模块的落点映射、最小 v1 表集、可测验收口径和非目标清单，才具备开工条件。**

---

## 0. TL;DR

### 0.1 四份文档的真实定位

| 文档 | 真实形态 | 可否作为规范 | 主要问题 |
|---|---|---|---|
| 00 | 聊天记录合集（至少 3 段回答拼接，章节编号重启两次） | 否 | 与 01 约 80% 重复；含"下载"链接等对话残留；含 PostgreSQL 与 SQLite 自相矛盾 |
| 01 | 架构意图文档（52 节，结构完整） | 部分 | 含大量与 02 重复的 DDL/接口；无状态标记；Phase 划分与 02/03 不一致 |
| 02 | 技术规格草稿（89+ 节，SQLite DDL 最全） | 是（schema 唯一事实源） | 未包含 03 的 `ALTER`；`language_projects` 与 03 的 `projects/modules/packages` 概念重叠未合并 |
| 03 | 补充规格（21 节 + 3 附录，最接近可执行） | 部分 | 一次引入 ~50 张新表，粒度对 v1 过重；P0 里混入类型系统/LSP 工程化等超前项 |

### 0.2 最重要的 8 个缺口（按风险排序）

1. **置信度（confidence）没有计算定义**——全篇用它决定"要不要探索/能不能复用"，但只有示例数值，没有公式、来源权重、校准与降级规则。这是承重墙。
2. **多进程/多实例写入没有解决**——本仓库 `internal/sqliteutil` 注释明确记录了 `aicli local` 与 `runtime-server` 同库写锁竞争导致"启动长时间无响应"；方案只写了 WAL/busy_timeout，没有单写者仲裁。
3. **与现有上下文系统的集成点未定义**——`contextmgr.Manager`（Budget / LayerPlan Hot-Warm-Cold / WorkspaceMode / RecallMode）+ `contextpack` Provider + `memorystore` + `factledger` 已构成事实上的 Context Planner/Compiler/Project Memory。方案若另起一套，会产生"双上下文 + 三套记忆"。
4. **工具面冲突未回答**——已有 P0–P3 的 `grep/view/glob/ls`，模型已被提示优先使用；新增 `code.*` 的并存/降级/灰度协议缺失，且与本仓库 `aicli-tool-capability-convergence-plan.md` 会打架。
5. **Windows 现实缺席**——本仓库 Windows 优先（AGENTS.md、win7 构建、pwsh）：fsnotify 可靠性、长路径、LSP 僵尸进程回收、大小写不敏感路径均未覆盖。
6. **隐私与出网无边界**——仓库已有 `internal/embedding/openai_provider.go`；一旦接入，代码片段会出网。默认忽略清单（`.env*`、`*.pem`、`*.key`、`credentials*`、`.aicli/`）未落到可执行集合。
7. **验收指标不可测/不可归因**——`重复探索 Token < 20%`、`Context Cache Hit > 50%` 等缺少分母口径、基线、样本集与统计方法；与"阈值应实测后定"自相矛盾。
8. **没有非目标与迁移/回滚**——P0 混入类型系统、泛型、跨语言 IDL、Runtime Evidence；无 schema migration、无 feature flag、无"索引坏了怎么办"。

### 0.3 优化后的主策略

- **不新建独立服务**：v1 以库形式落在 `backend/internal/knowledge/`，进程外只允许 LSP 子进程。
- **单写者 + 只读降级**：`knowledge.db` 同一 workspace 同一时刻只有一个 owner 写者；无 owner 时整层降级为"只读索引 + 现有工具 fallback"。
- **复用优先**：artifact / memorystore / factledger / compactruntime / contextmgr / contextpack / sqliteutil / usageledger / toolkit / policy 全部复用，不重建。
- **v1 表集 ≤ 23 张**（= `02` §8【v1 core】的 22 表 + `symbol_fts` 虚表；含 FTS 虚拟表；不含 migration 簿记表（归 `internal/migrate`）与 extension/deferred；口径见 [ADR-0009](adr/0009-v1-table-set-scope.md)），03 附录 A 的 50 张表按 P0/P1/P2 重新裁剪，类型系统/跨语言/Runtime Evidence 明确推迟。
- **影子模式先行**：`knowledge.mode = off | shadow | on`，shadow 期间只用索引计算但返回旧结果并记录差异，用真实差异率换正确性信心。
- **不替换 P0 工具**：`code.*` 是增强前端，索引不可用时内部 fallback 到现有 grep/view 并在返回中标注 `source:"fallback"`。

---

## 1. 评审方法与证据

### 1.1 方法

1. 结构级：提取四份文档全部标题，建立章节地图与重叠检测。
2. 内容级：抽取关键设计断言（架构、UCM、API、DDL、置信度、缓存、变更、安全、验收），逐条检查是否有定义、是否可执行、是否自洽。
3. 项目级：读取本仓库与知识层直接相关的模块，建立"方案能力 → 现有模块 → 落点/冲突"映射。
4. 对抗级：对每条断言反问"如果这条错了会怎样""谁来验证""怎么回滚"。

### 1.2 已核实的仓库证据（用于第 3 节）

| 路径 | 已核实内容 |
|---|---|
| `backend/internal/workspace/symbol_index.go` | `SymbolInfo{Name,Type,Language,File,Line,LineEnd,References}`；`SymbolIndex{byName,byFile,all}`；`NewSymbolIndex(scan)`；`Search(query, limit)`。纯内存、依赖全量 scan。 |
| `backend/internal/workspace/scanner.go` | `Language` 常量含 Go/Python/JS/TS/Java/Rust/CPP/C/Unknown；`extensionToLang`；`ignorePatterns` 含 `.*\.test\.(go|py|js|ts)$` 与 `^_\w+`；`ignorePathComponents` 含 `.aicli/node_modules/vendor/dist/build/__pycache__`；符号提取为正则（Go/Python/JS/TS），**Java/Rust/C++ 有语言枚举但无符号正则**。 |
| `backend/internal/workspace/context_builder.go` | `ContextBuilderConfig{MaxFiles:5,MaxSymbols:8,MaxReferences:3,MaxChunks:6}`；`WorkspaceContext{Query,Files,Symbols,References,Chunks,Summary}`；`NewContextBuilderWithIndexes(...)` 注释明确"索引构建在扫描大仓库时开销可观，多次请求复用同一套索引可避免重复构建"。 |
| `backend/internal/contextmgr/manager.go` | `Budget{MaxPromptTokens,MaxMessages,KeepRecentMessages,MaxRecallResults,MaxObservationItems,MaxProjectMemory,ProjectMemoryTokens}`；`DefaultBudget` = 12000 token / 24 msg / keep 8；Profile `compact/balanced/extended` 与 `hot/warm/cold`；`CompactionMode=summary|ledger_preferred`；`RecallMode/WorkspaceMode=disabled|signals|broad`；`ObservationMode=all|failures`；`Manager{Artifact,Ledger,Facts,TeamContext,Workspace,ProjectMemory,Events,Agent}`；`BuildInput` 含 WorkspaceID/SessionID/GoalID/TaskID/TeamID/Goal/History/Memory/Observations/CountTokens/PromptBudget；`BuildResult{Messages,Metadata}`；`LayerSpec/LayerPlan{ProfileContext,Hot,Warm,Cold}`。 |
| `backend/internal/contextpack/context_pack.go` | Provider 模式：`Provider{Name,Build}`；`Input{Prompt,Messages,Session,Profile,Workspace,WorkspacePath,TeamID,TaskID}`；`Builder.AddProvider/Build`。 |
| `backend/internal/memorystore/store.go` | 文件型 `<project>/.aicli/memory/notes.jsonl`；`Note{ID,Text,Tags,Source,SessionID,CreatedAt}`；`DefaultSearchLimit=5`、`DefaultInjectLimit=5`、`DefaultTokenBudget=600`；关键词检索，无 embedding。 |
| `backend/internal/factledger/` | `ledger.go` / `query.go`：持久化事实账本，已被 `contextmgr.Manager.Facts` 使用。 |
| `backend/internal/sqliteutil/sqliteutil.go` | `OpenFileCtx`：`journal_mode=WAL`、`busy_timeout=5000ms`、`MaxOpenConns(1)/MaxIdleConns(1)`；`RetryLockedCtx` 10 次退避（50ms→500ms）；包注释明确"aicli local 与 runtime-server 可能同时打开同一批 SQLite 文件……并发打开时表现为启动长时间无响应"。 |
| `backend/internal/artifact/` | 工具结果归档与 `Search(ctx, sessionID, query, limit)`；现网已出现 `artifact_id=art_...` 的截断回读机制。 |
| `backend/internal/compactruntime/` + `contextmgr/compact.go` | 观察/历史压缩运行时（local/remote）。 |
| `backend/internal/embedding/` | `index.go`（`VectorDim`、`Normalize`、`CosineSimilarity`、`EuclideanDistance`）+ `openai_provider.go` + `search.go`。 |
| `backend/internal/toolkit/` + `tools/` | 57 个工具文件：`grep/glob/ls/view/apply_patch/edit/multiedit/write/append_write/artifact_read/bash/execute_shell_command/fetch/download/web_search/sourcegraph/todos/git_worktree` 等；README 给出 P0–P3 优先级表；另有 `search.go`/`search_tool.go`。 |
| `backend/internal/usageledger/` + `internal/usageanalytics/` | SQLite 持久化的 token/usage 账本与查询分析。 |
| `backend/internal/{policy,fsscope,foldertrust,isolation,toolbroker,toolargs,toolctx}` | 既有权限/沙箱/参数治理面。 |
| `backend/internal/{workspace,workspaceregistry,filebrowse,gitbrowse,checkpoint,sessionmeta,historyguard,contextreconcile}` | 既有工作区身份、文件/git 浏览、会话检查点与上下文一致性原语。 |
| `backend/internal/ripgrep/resolver.go` | 仅 ripgrep 二进制解析；无自建检索引擎。 |
| `docs/ANALYSIS-sqlite-lock-problem.md` | 仓库内已存在 SQLite 锁问题分析。 |
| `docs/plan/`（120 份） | 相邻计划：`aicli-tool-capability-convergence-plan.md`、`tool-output-artifact-cascade-audit-and-optimization-plan-20260919.md`、`composer-at-file-reference-workspace-search-plan.md`、`llm-cache-analytics-unified-plan.md`、`session-usage-analytics-and-agent-diagnostics-plan.md`、`codex-compact-token-usage-observation-analysis.md`、`agent-trajectory-view-implementation-plan.md`。 |
| `AGENTS.md` | Windows 命令行长度约束（`cmd.exe` 8191）；大补丁/长脚本走文件；`.aicli` 属忽略目录。 |
| `backend/configs/*.yaml` | 已有 `workspace/context/compact/recall` 配置项（`config.yaml` 命中 23 处，`model_cards.yaml` 47 处）。 |


---

## 2. 四份文档的完整性问题

### 2.1 文档级问题（治理层）

| 编号 | 问题 | 证据 | 影响 |
|---|---|---|---|
| D1 | 00 不是规范，是聊天记录 | 章节编号在 `1442` 与 `2295` 两处重启为"# 1."；结尾出现 `[下载完整方案文档](sandbox:/mnt/data/...)` | 读者会把重复内容当独立结论；无法引用 |
| D2 | 00 与 01 高度重复 | 00 的"Project Index / Project Map / Hierarchical Context / Exploration Memory / Exploration Graph / Tool Output 压缩 / Symbol 级读取 / Context 分层 / Context Cache / Git Commit 失效 / 数据库设计 / Agent Loop / Context Planner / Confidence / 不建议 Vector DB"与 01 的 §3–§52 基本一一对应 | 维护两份、漂移风险高 |
| D3 | 没有索引与状态标记 | 目录下只有 4 个 md，无 README、无 Accepted/Proposed/Deferred、无版本变更记录 | 新人不知道从哪读、哪条已决策 |
| D4 | 编号体系互不兼容 | 00 用中文序号 + 两次重启；01 用 `# 1..52`；02 用 `# 1..89+`；03 用 `# 0..21` + 附录 A/B/C | 跨文档引用只能靠文字描述 |
| D5 | 术语不统一 | 00 出现"Code Intelligence Layer / Project Knowledge Layer / Agent Knowledge Runtime"；01/03 用"Code Knowledge Runtime"；02 用"Agent Harness" | 同一概念多个名字，评审时容易错位 |
| D6 | 02 与 03 的 schema 有应用顺序依赖但未声明 | 03 §1.3 是 `ALTER TABLE symbols ADD COLUMN stable_key/...`，依赖 02 §15 的 `symbols` 定义；02 顶部没有任何"需先/后应用 03"的说明 | 只读 02 或只读 03 都会得到不完整 schema |
| D7 | 自相矛盾：数据库选型 | 00 末尾写"具体到 PostgreSQL 表结构"；02 全篇 SQLite（§7 明确 `.agent/harness.db` + WAL） | 实施者需要自行裁决 |
| D8 | 自相矛盾：Phase 优先级 | 00 的 Phase 1 = Exploration Cache；01 的 Phase 0 = Telemetry / Phase 1 = Exploration Cache；03 的 P0 = 稳定符号 ID + 项目模型 + 类型系统 + 名称解析 + LSP 工程化 | "第一步做什么"有三个答案 |
| D9 | 自相矛盾：阈值是否写死 | 01 §38 说"具体比例应通过 telemetry 实测，不应写死"；03 §20.2 直接给固定阈值（重复探索 Token < 20% 等） | 验收时无法判断以哪个为准 |
| D10 | DB 路径与本仓库目录约定不一致 | 02 §7 用 `.agent/harness.db`；本仓库既有约定是 `<project>/.aicli/...`（`memorystore` 用 `.aicli/memory`，`scanner` 忽略 `.aicli`） | 会产生第二个隐藏目录，且可能未被 ignore |
| D11 | 02 §85 的 Go 目录树与本仓库 monorepo 结构冲突 | 02 给的是 `agent-harness/{cmd/harness, internal/knowledge, internal/language, migrations, ...}`；本仓库是 `backend/{cmd,internal}` | 实施者需要自行映射 |
| D12 | 没有非目标、没有迁移、没有回滚 | 四份文档均无"v1 不做什么""schema 如何升级""索引损坏如何降级" | 范围膨胀与上线风险 |

### 2.2 方案级问题（设计层）

按"如果不补会直接导致返工或事故"排序：

**G1 置信度（confidence）没有定义（最高风险）**
- 依赖点：01 §9（动态语言 candidates + confidence）、01 §14（confidence 决定是否重新探索）、02 §55（Confidence 策略）、03 §9.3/§9.4（解析状态与置信度示例）。
- 现状：只有示例值（0.94 / 0.63 / 0.96 / 0.32 / 0.88），没有公式、没有来源权重表、没有校准方法、没有冲突处理、没有"置信度错了会怎样"的兜底。
- 后果：Context Planner 会在"过度探索"（token 回归）与"过早复用"（改错代码）之间无控振荡。
- 需要补：可计算的定义 + 阈值策略 + 冲突事件 + 与验证读取的绑定。

**G2 多进程/多实例写入没有解决**
- 依赖点：02 §63（SQLite 并发策略：WAL/busy_timeout/短事务/批量写/预编译语句）、02 §7（PRAGMA）。
- 现状：本仓库 `internal/sqliteutil` 的包注释明确记录 `aicli local` 与 `runtime-server` 可能同时打开同一批 SQLite 文件，且"并发打开时表现为启动长时间无响应"；已有 `docs/ANALYSIS-sqlite-lock-problem.md`。
- 缺口：索引器是独立进程还是进程内？多个 aicli 实例同时索引同一 workspace 怎么办？runtime-server 托管多 session 时写者是谁？没有 owner 选举、写队列、per-workspace 锁。
- 后果：知识层会成为新的锁热点，直接复现已知事故。

**G3 与现有上下文系统的集成点未定义**
- 现状：`contextmgr.Manager` 已有 Budget/Profile（compact/balanced/extended、hot/warm/cold）、`LayerPlan{ProfileContext,Hot,Warm,Cold}`、`CompactionMode`、`RecallMode/WorkspaceMode=disabled|signals|broad`、`ObservationMode=all|failures`、`ProjectMemoryStore`、`Facts`、`Artifact`、`Workspace` builder；`contextpack.Builder` 已有 Provider 机制。
- 缺口：方案的 Context Planner / Context Compiler / Exploration Memory / Project Memory 分别是"新增 layer""新增 Provider""替换 Manager""独立子系统"——未决策。
- 后果：出现两套上下文装配、三套记忆（memorystore notes / factledger facts / exploration memory），预算与失效逻辑互相打架。

**G4 工具面冲突与迁移未回答**
- 现状：`toolkit` 已有 P0–P3 工具（grep/view/glob/ls/apply_patch/edit/write/bash…），README 明确优先级；本仓库另有 `docs/plan/aicli-tool-capability-convergence-plan.md`。
- 缺口：`code.*` 与 `grep/view` 的并存协议、降级协议、返回结构兼容、灰度开关、系统提示如何更新。
- 后果：模型选错工具；两条收敛路线互相冲突。

**G5 Windows 现实缺席**
- 缺口：fsnotify 在 Windows 的可靠性/延迟/丢事件；长路径（>260）；大小写不敏感路径导致的 symbol 重复；LSP 子进程的启动/崩溃/僵尸回收/内存上限；pwsh 与 cmd 的命令行长度约束（AGENTS.md 已明确 8191）。
- 后果：Change Manager 与 Adapter 层在主力开发平台上不可靠。

**G6 隐私与出网无边界**
- 现状：仓库已有 `internal/embedding/openai_provider.go`，可直接把文本发往第三方。
- 缺口：默认忽略清单未落成可执行集合；没有"默认不出网"约束；没有 redaction 规则与 audit 的对接细节；没有 embedding provider 白名单/本地优先策略。
- 后果：一次误配置即可把私有代码送出。

**G7 验收指标不可测/不可归因**
- 缺口：`重复探索 Token < 20%` 的分母与归因规则；`Context Cache Hit > 50%` 的 cache key 定义与命中口径；`符号解析精度 > 90%` 的 golden set 从哪来；单任务方差如何处理。
- 现状：本仓库已有 `usageledger`（SQLite）+ `usageanalytics`，完全可以先出基线再定阈值。
- 后果：Phase 无法通过/无法拒绝，验收变成主观判断。

**G8 增量索引的"正确性"未定义**
- 缺口：增量结果与全量结果如何证明一致；adapter/parser 版本升级导致的全量失效策略；rename/move/delete 的引用修复算法（03 §1.4 只给了规则，没给算法）；索引损坏的检测与重建。
- 后果：索引静默漂移，Context Compiler 基于错误知识给出"高置信度"的错误上下文。

**G9 多 Agent / 子代理语义缺失**
- 现状：本仓库有 `spawn_agent` / `spawn_subagents` / `spawn_team` / `supervision` / `subagentbatch` / `team`。
- 缺口：知识层是 per-session、per-task 还是 per-workspace 共享？父子会话是否共享 exploration memory？只读子代理是否写索引？并发子代理同时触发索引如何串行化？
- 后果：并发写冲突 + 探索记忆污染 + token 统计无法归因。

**G10 "任务（task）"模型缺失**
- 现状：02 §24–§26 有 `tasks` / `task_files` / `task_symbols`，但没有生命周期定义；本仓库已有 `internal/goal` 与 `get_goal/update_goal`。
- 缺口：task 与 session / goal / turn 的关系；task 何时创建、合并、关闭；exploration memory 是否 per-task。
- 后果：`task_*` 表会变成孤儿表。

**G11 索引范围与忽略规则未成体系**
- 现状：`scanner.go` 内置忽略 `.git/.idea/.vscode/node_modules/vendor/dist/build/__pycache__/.DS_Store/.aicli`，并忽略测试文件 `.*\.test\.(go|py|js|ts)$` 与 `^_\w+`。
- 缺口：`.gitignore` 与内置规则、用户规则、安全规则的优先级；生成代码（03 有 `generated_sources` 但无默认策略）；大文件/二进制阈值；本仓库自身 `dist/`、`logs/`、`node_modules/`、`.aicli/`、`tmp/`、`.tmp/` 的处置。
- 附带发现：**当前 scanner 把测试文件直接忽略**，若照搬会导致方案的 `code.tests` / `TestIndex` 永远为空——必须改为"索引但标记 `is_test`"。

**G12 性能预算不完整**
- 已有：`Light Index < 300ms/file`、`常见 Symbol 查询 < 100ms`、`Context Compiler 缓存命中 < 200ms`。
- 缺口：首次全量索引上限、DB 大小上限、内存上限、索引对主 turn 的 CPU/IO 抢占、fast index 是同步还是异步（02 §68 只说 deep index async）。
- 后果：编辑路径上同步建索引会直接放大每次 edit 延迟。

**G13 观测性与 API 暴露缺失**
- 现状：本仓库有 `runtime-server` HTTP/SSE、`internal/events`（`runtimeevents.Publisher`）、`observability`、`runtimeobserve`、`cacheanalytics`。
- 缺口：索引状态（building/fresh/stale/degraded/error）、覆盖率、命中率如何暴露；02 §33 / 03 §17 的事件如何进入现有 events 总线。
- 后果：运维不可见，出问题只能看日志。

**G14 安全模型粒度不足**
- 现状：03 §7 提出 `security_redactions` / `tool_permissions` / `audit_log`；本仓库已有 `policy` / `fsscope` / `foldertrust` / `isolation` / `toolbroker`。
- 缺口：知识层读取范围是否受现有 fsscope 约束？索引内容是否可能绕过 agent 的文件读取权限？LSP 子进程的沙箱与网络访问？
- 后果：知识层成为权限旁路。

**G15 "不做"清单缺失**
- 后果：03 的 P0 把"类型/继承/实现/泛型""名称解析""LSP 工程化"列为必须，但此时系统连持久化 file/symbol 索引都还没有。资源会被投入到低 ROI 的高阶语义上。

### 2.3 能力覆盖矩阵

图例：● 完整定义并可执行 / ◐ 有描述但缺关键定义 / ○ 仅提及 / — 缺失

| 能力 | 00 | 01 | 02 | 03 | 本项目现状 |
|---|---|---|---|---|---|
| Token 问题陈述与目标 | ● | ● | ◐ | ◐ | ◐（有 usage 账本，无探索归因） |
| Project/Code Knowledge 架构 | ● | ● | ● | ● | ○（`workspace` 内存索引） |
| Universal Code Model | ◐ | ● | ● | ◐ | ○ |
| File/Symbol 索引 | ◐ | ● | ● | ● | ◐（正则、内存、全量重建） |
| Reference/Call/Import 图 | ◐ | ● | ● | ● | ◐（`reference.go`，无 calls/imports 持久化） |
| 类型/继承/实现/重载/泛型 | — | ○ | ○ | ● | — |
| 名称解析/导入绑定 | — | ○ | ○ | ● | — |
| Language Adapter SPI | ● | ● | ● | ◐ | — |
| LSP 工程化（进程/诊断/位置编码） | ○ | ◐ | ◐ | ● | — |
| Tree-sitter | ◐ | ● | ● | ○ | — |
| 动态语言候选与置信度 | ◐ | ● | ◐ | ◐ | — |
| Exploration Memory | ● | ● | ● | ○ | ◐（`memorystore` notes，非探索图） |
| Exploration Graph | ● | ● | ● | — | — |
| Context Planner | ● | ● | ● | ◐ | ◐（`contextmgr` Budget/Strategy/LayerPlan） |
| Context Compiler | ● | ● | ● | ◐ | ◐（`contextpack` Provider） |
| Context 分层 L0–L3 | ● | ● | ○ | ◐ | ◐（hot/warm/cold/profile） |
| Tool Output 压缩 | ● | ● | ● | ◐ | ●（`artifact` + `compactruntime`） |
| Symbol 级读取 | ● | ● | ● | ○ | ◐（`view` 按行，无 symbol 参数） |
| 增量索引 | ● | ● | ● | ◐ | — |
| Fresh/Dirty/Stale/Unknown | ● | ● | ● | ◐ | ◐（`contextreconcile`/`historyguard`） |
| Change Manager（Agent/Git/Watcher） | ● | ● | ● | ● | ◐（`gitbrowse`，无 watcher） |
| Cache / Cache Key / Invalidation | ● | ● | ● | ● | ◐（`contextmgr` 内有复用，无持久缓存表） |
| 版本向量/一致性 | ◐ | ◐ | ◐ | ● | ◐（`checkpoint`/`sessionmeta`） |
| SQLite Schema | ◐ | ◐ | ● | ● | ●（多库，`sqliteutil` 统一基线） |
| Code Intelligence API（code.*） | ● | ● | ● | ● | — |
| 语义检索/FTS/向量 | ◐ | ● | ● | ● | ◐（`embedding` 存在，未接入上下文） |
| 测试智能 | — | ○ | ○ | ● | — |
| 跨语言/IDL | ○ | ○ | ● | ● | — |
| Runtime Evidence | — | ○ | ● | ● | — |
| 安全/隐私/审计 | — | ◐ | ○ | ● | ◐（policy/fsscope，未覆盖知识层） |
| 评估基准/黄金集 | — | ○ | ○ | ● | — |
| Telemetry/KPI | ● | ● | ○ | ● | ●（usageledger/usageanalytics） |
| 多进程/多实例并发 | — | — | ◐ | — | ●（sqliteutil 已解决一部分） |
| Windows 适配 | — | — | — | — | ●（AGENTS.md 等） |
| 多 Agent 语义 | — | — | — | — | ●（spawn/team/supervision） |
| 迁移/回滚/灰度 | — | — | — | — | ◐（`internal/migrate`） |
| 非目标 | — | ◐（"不建议的架构"） | — | — | — |

**读法**：方案在"架构思想"上覆盖充分（●集中在 00/01），但在"工程可执行"上缺口集中在 03 之外——尤其 G1/G2/G3/G4/G7/G8 六项，以及矩阵右下角"迁移/多 Agent/Windows"三行的空白。

---

## 3. 与 ai-agent-runtime 现状的对齐分析

### 3.1 可复用资产（不要重建）

| 方案能力 | 现有模块 | 复用方式 | 需改造点 |
|---|---|---|---|
| File/Symbol 轻索引 | `workspace/scanner.go`、`workspace/symbol_index.go` | 作为 v1 的 builtin adapter 与内存索引 | 补 Java/Rust/C++ 粗符号；测试文件改为 `is_test` 标记而非忽略；输出 content_hash |
| 引用图 | `workspace/reference.go` | 作为 `references` 表的抽取器 | 增加 confidence/source；持久化 |
| 工作区上下文 | `workspace/context_builder.go` | 已有 `NewContextBuilderWithIndexes` 复用意图 | 从"每次 scan 重建"升级为"读持久索引"；补 version |
| Context Planner | `contextmgr/manager.go`（Budget/Profile/LayerPlan/Strategy/模式枚举） | 新增 `KnowledgeMode`，与 `WorkspaceMode/RecallMode` 对称 | 增加 knowledge layer；接入 confidence 阈值 |
| Context Compiler | `contextmgr.Build()` + `contextpack.Builder` | 新增 `knowledge` Provider，或新增 layer | 定义 item 级 version/trust/reason |
| Project Memory | `memorystore`（notes.jsonl） | 保留为"长期事实/人工笔记" | 与 exploration memory 分层，不合并 |
| Facts | `factledger` | 保留为事实账本 | 探索结论可写入 fact 时需去重 |
| Raw Tool Output | `artifact`（含 Search + artifact_id 回读） | 直接复用 | 与 `tool_results` 表的关系：artifact 为 blob 存储，表只存索引元数据 |
| Observation 压缩 | `compactruntime` + `contextmgr/compact.go` | 直接复用 | 增加 knowledge 相关压缩模板 |
| SQLite 并发基线 | `sqliteutil.OpenFileCtx`（WAL/busy_timeout/单连接/RetryLocked） | knowledge.db 必须走同一入口 | 增加 `foreign_keys=ON`；增加 owner 仲裁 |
| Telemetry | `usageledger` + `usageanalytics` | 扩展字段而非新建 | 增加 exploration 归因字段 |
| 语义检索 | `embedding`（含 openai provider） | 后置、默认关闭 | 本地优先、出网审计 |
| 工具注册 | `toolkit.Registry` + `tools/` | `code.*` 作为新工具注册 | 统一返回结构；fallback 到 `grep/view` |
| 权限/沙箱 | `policy`/`fsscope`/`foldertrust`/`isolation`/`toolbroker` | 知识层读取必须走同一策略 | 明确索引范围受 fsscope 约束 |
| 会话一致性 | `checkpoint`/`sessionmeta`/`historyguard`/`contextreconcile` | 版本/失效判定统一在此扩展 | 避免知识层自建第二套 stale 判定 |
| 工作区身份 | `workspace`/`workspaceregistry` | `workspaces.id` 复用其 ID | 禁止新造 workspace 主键 |
| 文件/Git 浏览 | `filebrowse`/`gitbrowse` | Change Manager 的变更源 | 增加 commit→changed files 映射 |
| 迁移 | `internal/migrate` | knowledge schema 迁移接入 | 版本表与回滚 |

### 3.2 必须裁决的冲突点

| 编号 | 冲突 | 备选 | 建议裁决 |
|---|---|---|---|
| C1 | 独立服务 vs 进程内库 | (a) 独立 `code-intel` 服务；(b) `backend/internal/knowledge` 库；(c) 仅 LSP 进程外 | **v1 选 (b)+(c)**。理由：本仓库是 monorepo 且已有 runtime-server；独立服务会引入部署/鉴权/序列化成本，且与"单机单 workspace"主场景不匹配。 |
| C2 | DB 位置 | (a) `.agent/harness.db`（02）；(b) `.aicli/knowledge/knowledge.db`；(c) 用户级缓存目录 | **选 (b)**。与既有 `.aicli/memory` 一致，且已在 `scanner` 忽略列表中，避免污染项目树与误索引。 |
| C3 | schema 事实源 | (a) 02 为唯一源，03 的 ALTER 合并回 02；(b) 02=core、03=extension 双文件 | **选 (b) 但强制声明顺序**：02 顶部写明"core schema，必须先应用；03 §1.3 为必要 ALTER"，并把 03 中 v1 需要的表提升为 `core-v1`。 |
| C4 | `language_projects`（02）vs `projects/modules/packages`（03） | 合并 / 二选一 / 分层 | **合并**：`projects` 表达构建单元；`modules` 表达目录/包边界；删除 `language_projects` 或降级为 `projects.language` 的视图。 |
| C5 | `references` 绑 `symbol_id` 还是 `symbol_version` | 03 §1.4 要求绑 version | **绑 version**：`references` 增加 `to_symbol_version`；`symbol_versions` 必须存在；提供回填算法。 |
| C6 | 向量检索定位 | (a) 主路径；(b) 可选后置 | **选 (b)**：FTS5 + 符号精确 + 图遍历为主；embedding 默认 off。 |
| C7 | `code.*` 是否替换 grep/view | 替换 / 并存 / 增强前端 | **选增强前端**：保留 P0 工具；`code.*` 内部 fallback；`knowledge.mode` 控制启用。 |
| C8 | exploration memory 归属 | per-session / per-task / per-workspace | **per-task 写入、per-workspace 复用**：节点带 `session_id` + `task_id` + `workspace_id`；跨任务复用时要求 confidence 阈值更高。 |
| C9 | 测试文件处理 | 忽略（现状）/ 索引并标记 | **索引并标记 `is_test`**，否则 `code.tests` 与影响面分析永久失效。 |
| C10 | PostgreSQL 提法 | 保留 / 删除 | **删除或明确标注为"未来可选、v1 不用"**，避免实施歧义。 |


---

## 4. 优化后的方案（v1）

### 4.1 设计原则修订

保留 02 §2.1 的五项原则，但每项补上"本仓库可执行定义"：

| 原则 | 原表述 | 修订后（可执行） |
|---|---|---|
| Incremental | 只处理变化的文件 | 以 `files.content_hash` 为唯一变更判据；agent 编辑走 write/edit 钩子（同步标记），外部变更走 git diff（准）与 fsnotify（快但不可信，仅作触发）；任何一条路径都不得直接写 symbols，必须先写 `index_jobs` 再串行执行 |
| Lazy | 不主动分析 Agent 不需要的代码 | v1 默认只索引"文件 + 顶层符号 + imports"；方法体、局部变量、类型关系不索引；`code.*` 查询触发的按需深索引（deep index）异步执行并可取消 |
| Cached | 已经探索过的内容不重复探索 | 探索缓存以 `(workspace_id, task_id, query_hash, knowledge_version)` 为 key；命中要求 confidence ≥ 阈值且未 stale；跨任务复用要求更高阈值 |
| Versioned | 所有知识和 Context 都绑定代码版本 | `knowledge_version = H(workspace_id, per-file content_hash merkle, adapter_versions, parser_versions, schema_version)`；context item 必须携带 `knowledge_version`，不一致即拒绝注入 |
| Language-Agnostic | Harness 不理解具体语言 | core 不得出现 `if language == "go"`；语言知识全部通过 `LanguageAdapter` SPI；v1 只实现 builtin adapter，LSP/Tree-sitter 为可选实现 |

新增三条本项目必需的原则：

6. **Single-Writer**：同一 workspace 的 `knowledge.db` 在同一时刻只有一个写者进程；其他进程只读。写者通过 `<workspace>/.aicli/knowledge/owner.json`（PID + 启动时间 + heartbeat）仲裁；心跳超时（建议 30s）后允许抢占，抢占前必须先验证旧 PID 不存在。
7. **Degrade-Not-Fail**：知识层任何失败（索引未建、DB 锁超时、LSP 崩溃、adapter 报错、版本不一致）都不得导致 agent turn 失败；必须返回 `degraded` 标记并 fallback 到现有工具。
8. **Shadow-Then-On**：任何会改变模型可见输出的能力，必须先经过 `shadow` 模式（计算但不返回），用真实差异率换取上线信心。

### 4.2 目标架构（v1）

```text
                        ┌──────────────────────────────────────────┐
                        │              aicli / runtime-server       │
                        │                                          │
                        │   contextmgr.Manager.Build()             │
                        │        ├── Budget / LayerPlan            │
                        │        ├── WorkspaceMode / RecallMode    │
                        │        └── KnowledgeMode (新增)          │
                        │                 │                        │
                        │                 ▼                        │
                        │      contextpack.Builder                 │
                        │        └── knowledge.Provider (新增)     │
                        └─────────────────┬────────────────────────┘
                                          │
                    ┌─────────────────────┼─────────────────────┐
                    ▼                     ▼                     ▼
            knowledge.Planner     knowledge.Query       knowledge.Compiler
            （探索/复用决策）      （检索/图遍历）        （最小上下文生成）
                    │                     │                     │
                    └─────────────────────┼─────────────────────┘
                                          ▼
                        ┌──────────────────────────────────────┐
                        │        knowledge.Store (SQLite)       │
                        │  files / symbols / references /       │
                        │  imports / calls / exploration_* /    │
                        │  context_* / cache_entries / jobs /   │
                        │  schema_migrations / *_fts            │
                        │  走 sqliteutil.OpenFileCtx + 单写者   │
                        └───────────────┬──────────────────────┘
                                        ▼
                        ┌──────────────────────────────────────┐
                        │        knowledge.Indexer              │
                        │  builtin adapter（v1 默认）           │
                        │  tree-sitter adapter（可选）          │
                        │  lsp adapter（可选，进程外）          │
                        └───────────────┬──────────────────────┘
                                        ▼
                        ┌──────────────────────────────────────┐
                        │        knowledge.ChangeManager        │
                        │  agent-edit hook（主，同步标记）       │
                        │  git diff（校正，turn 前/后）          │
                        │  fsnotify（优化，可关闭）              │
                        └──────────────────────────────────────┘
```

关键点：
- **只有一个 Store**，所有写入串行经过 `Indexer` → `Store`，`Store` 内部用单连接 + `sqliteutil.RetryLocked`。
- **Planner/Query/Compiler 都是纯读**，可被多个 session 并发调用（WAL 允许）。
- **ChangeManager 不直接写库**，只产出 `ChangeEvent`，由 owner 写者消费。
- **Provider 只读**：`knowledge.Provider.Build()` 不触发索引写入；缺索引时返回 `degraded` 并让 contextmgr 走原有路径。

### 4.3 P0/P1 最小数据模型（16 张；v1 core 的子集）

裁剪原则：只保留"没有它就无法实现 P0/P1 能力"的表；03 附录 A 的 50 张表按此重新归类。

> **2026-09-29 引用化（`06` §9 #19，ADR-0009 落地）**：本节原先内联的 15 处 DDL 语句（14 处建表 + 1 处 FTS 虚表，含其索引）已全部删除，只保留用途 / 验收指标 / 引用（ADR-0007 D5、不变量 I2）。"16 张"的口径不变：= 12 个 core 概念（`refs` 在 `02` 的规范名为 `references`）+ 2 张 extension（`symbol_aliases`、`index_jobs`）+ 1 张 migrate 簿记（`schema_migrations`）+ 1 张 FTS 虚表；其中属于 v1 core 的子集见 §0.3 与 ADR-0009 §4.1。

- **0. 迁移簿记（`schema_migrations`）：不建知识表**。**用途**：迁移版本 / 校验和；**验收指标**：版本高于代码支持则拒绝打开（§7.6 与 R12）；**引用**：由 `internal/migrate` 统一创建维护（`backend/internal/knowledge/migrations/0001_init.sql` 头注 1；ADR-0009 §4.1 裁决其不计入 v1 表集，原 DDL 结构会与实现冲突）。
- **1. 工作区 `workspaces`**（id 复用 workspaceregistry，不新造主键）。**用途**：工作区标识与根路径；**验收指标**：单写者仲裁与只读降级（§4.1 / §4.7）；**引用**：`02` §9。
- **2. 文件 `files`**。**用途**：文件清单、内容哈希与索引状态（`index_state`）；**验收指标**：§7.4（首次全量、单文件增量 p95）；**引用**：`02` §12–13。
- **3. 符号 `symbols`**。**用途**：符号主表（`stable_key` 见 §4.4）；**验收指标**：§7.3（精度 / 召回）；**引用**：`02` §15–16。
- **4. 符号版本 `symbol_versions`**。**用途**：引用 / 缓存绑版本（落实 03 §1.4 规则 5）；**验收指标**：§7.3 与 §7.6；**引用**：`02` §17。
- **5. `symbol_aliases`（extension）**。**用途**：重命名 / 移动 / 重载别名，引用修复的事实源（03 §1.3）；**验收指标**：§4.4 的重命名迁移路径；**引用**：[`supplement/01_symbol_identity.md`](supplement/01_symbol_identity.md) §1.3（extension 唯一事实源；按 ADR-0009 §10 待并入 extension 组名单）。
- **6. 引用 `refs`**。**用途**：引用 / 调用 / 导入边（`kind` + `confidence` + `source`）；**验收指标**：§7.3；**引用**：`02` §18（规范名 `references`；`refs` ↔ `references` 的收敛见 ADR-0009 §10）。
- **7. 探索记忆三表（`exploration_sessions` / `exploration_nodes` / `exploration_edges`）**。**用途**：多轮探索复用（最高 ROI）；**验收指标**：§7.2 与 M1（ADR-0003）；**引用**：`02` §21 / §22 / §23。
- **8. 上下文两表（`context_snapshots` / `context_items`）**。**用途**：可解释性与 stale 拒绝注入；**验收指标**：§7.3 / §7.5；**引用**：`02` §27 / §28。
- **9. 缓存与失效两表（`cache_entries` / `invalidation_events`）**。**用途**：持久缓存（retrieval/compile/summary）与失效事件（file_changed/git_sync/…）；**验收指标**：§7.2 / §7.6；**引用**：`02` §31 / §32。

**10. 索引任务（`index_jobs`）：定义见 extension schema**

> **2026-09-28 迁出**（ADR-0007 §4.3 / `06` §9 待办 #1）：本节原先内联 `CREATE TABLE`，违反"`04` 只引用表名"（ADR-0007 D5）。
> 表 DDL 现落在 [`supplement/15_change_management.md`](supplement/15_change_management.md) §15.3；`04` 只保留用途与验收指标。
> `04` §4.3 其余 15 处 DDL 的引用化已于 **2026-09-29 执行**（ADR-0009 Accepted / `06` §9 #19）；本节及其余条目现已全部只含用途 / 验收指标 / 引用。

- **用途**：owner（单写者）的持久化写队列——owner 内单 goroutine 串行消费；任何变更路径（agent 编辑钩子 / `git diff` / fsnotify）都必须**先写 `index_jobs` 再串行执行**，不得直接写 `symbols`（§4.1 / §4.7）。
- **验收指标**：见 §7.4（首次全量索引、单文件增量 p95）与 §7.6（校准口径）；数据来源列 `started_at` / `finished_at` / `files_total` / `files_done`。

**11. FTS5 检索（`symbol_fts` 虚表，多语言检索，与 `symbols` 同步）**

- **用途**：`name` / `qualified_name` / `signature` 的全文检索（v1 语义检索主通道；embedding 默认关闭）。
- **验收指标**：§7.3（检索召回）与 §7.6。
- **引用**：`02` §73（该文件规范名 `symbol_fts`；本文件旧稿名 `symbols_fts`、列集差异与实现的收敛见 ADR-0009 §10 与 `0001_init.sql` 头注 2）。

**明确推迟到 v2+ 的表**（03 附录 A 的其余 34 张）：
`projects/modules/packages/build_configs/dependency_versions/generated_sources/ignore_rules`（v1 用配置 + 内置规则替代）、`type_relations/inheritance_edges/implementation_edges/overloads/generic_params/parameters/local_variables/annotations`、`import_bindings/name_resolutions/scope_bindings`、`lsp_servers/lsp_documents/lsp_diagnostics`、`version_vectors/cache_dependencies/events_outbox/consumer_offsets`（v1 用 `knowledge_version` + `invalidation_events` 替代）、`security_redactions/tool_permissions/audit_log`（v1 复用 `policy`/`fsscope` + 日志）、`eval_*`（v1 用测试 golden 文件）、`call_candidates/reference_candidates`（v1 用 `confidence` 列替代）、`tests/test_symbols/test_runs/coverage_evidence`（v1 用 `files.is_test` + `symbols.is_test` 替代）、`cross_language_links/idl_contracts/api_endpoints/rpc_methods/message_topics/db_schema_links`、`runtime_evidence/runtime_edges/runtime_symbol_stats`、`change_events/git_sync_state`（v1 复用 `internal/events` 与 `gitbrowse`）。

> 注意：`symbol_aliases` 与 `symbol_versions` 虽小但必须进 v1——03 §1.4 的 5 条规则中有 3 条依赖它们，而"引用绑版本"是防止用旧上下文改错代码的关键机制，不能推迟。

### 4.4 stable_key 与 confidence 的可执行定义（补 G1/G8）

**stable_key**（采纳 03 §1.2，补上版本化与归一化）：

```text
stable_key = sha1(
    language
  + "|" + kind
  + "|" + normalize(namespace_or_package_or_module)
  + "|" + normalize(owner_qualified_name)
  + "|" + normalize(qualified_name)
  + "|" + signature_hash          // 支持重载的语言必须包含
)
normalize(s) = 去空白、统一分隔符、大小写按语言规则（大小写敏感语言保留原样）
signature_hash = sha1(normalized_signature + adapter_version)
```

- 重命名：新增 `symbol_aliases(alias_type='rename')`，旧 stable_key 作为 alias 指向新 symbol；`refs` 中指向旧 symbol 的记录通过 alias 迁移，迁移不了的标记 `confidence=0.3` 并进入待人工确认队列（v1 只记录，不阻塞）。
- 移动：stable_key 不变，只更新 `file_id`/行列；若 `owner_qualified_name` 变化（跨包移动）则按重命名处理。
- 删除：`deleted_at` 标记，保留 30 天或 N 个版本后由 GC 清理。
- 歧义：同一 workspace 内 stable_key 冲突视为 adapter 缺陷，必须记录 `adapter_conflict` 事件并使该符号 `confidence` 降级。

**confidence**（补 G1，全部可计算）：

```text
confidence(symbol_or_edge) =
      source_weight
    * agreement_factor
    * (1 - staleness_penalty)
    * (1 - ambiguity_penalty)

source_weight:
    lsp_resolved            = 0.95
    runtime_evidence        = 0.90
    tree_sitter_resolved    = 0.80
    tree_sitter_heuristic   = 0.65
    regex_builtin           = 0.55
    fts_lexical             = 0.40

agreement_factor:
    多来源一致      = 1.00
    单来源          = 0.90
    多来源冲突      = 0.70（同时写 conflict 事件，取最高 source_weight）

staleness_penalty:
    file.content_hash 与索引记录不一致   = 0.50
    adapter_version 与当前 adapter 不一致 = 0.30
    parser_version 不一致                = 0.30
    knowledge_version 不匹配             = 1.00（直接不可用）

ambiguity_penalty:
    候选目标数 > 1 且无法消解 = 0.30
    候选目标数 > 1 但可按类型/导入消解 = 0.10
```

**阈值策略（可配置，Phase 0 后校准；默认值用于起步）**：

| 场景 | 阈值 | 行为 |
|---|---|---|
| 同任务内复用探索节点 | ≥ 0.80 | 直接复用，不再探索 |
| 同任务内复用但涉及写操作 | ≥ 0.90 | 复用 + 强制一次验证读取 |
| 跨任务复用 | ≥ 0.90 | 复用 + 强制验证读取 |
| 0.50–0.80 | — | 复用但标记"待验证"，允许一次低成本确认 |
| < 0.50 | — | 触发探索；探索失败则 fallback 到 grep/view |

任何 `confidence < 阈值` 却仍被注入上下文的情况都必须写 `context_items.reason` 并计入 `unsafe_reuse_count`（应为 0）。

### 4.5 接口边界（Go，落点在本仓库）

```go
// backend/internal/knowledge/store.go
type Store interface {
    KnowledgeVersion(ctx context.Context, workspaceID string) (string, error)
    UpsertFiles(ctx context.Context, files []FileRecord) error
    ReplaceFileSymbols(ctx context.Context, fileID string, symbols []SymbolRecord, refs []RefRecord) error
    LookupSymbol(ctx context.Context, q SymbolQuery) ([]SymbolRecord, error)
    References(ctx context.Context, symbolID string, limit int) ([]RefRecord, error)
    Callers(ctx context.Context, symbolID string, limit int) ([]RefRecord, error)
    Status(ctx context.Context, workspaceID string) (IndexStatus, error)
}

// backend/internal/knowledge/indexer.go
type Indexer interface {
    IndexLight(ctx context.Context, file FileInput) error
    IndexDeep(ctx context.Context, file FileInput) error
    Reindex(ctx context.Context, workspaceID string, paths []string) error
}

// backend/internal/knowledge/adapter.go
type LanguageAdapter interface {
    Name() string
    Version() string
    Detect(path string, head []byte) (Language, bool)
    Extract(ctx context.Context, file FileInput) ([]SymbolRecord, []RefRecord, error)
    Capabilities() AdapterCapabilities // definition|references|callers|types|tests
}

// backend/internal/knowledge/planner.go
type Planner interface {
    Plan(ctx context.Context, in PlanInput) (Plan, error)
}
type Plan struct {
    Reuse      []ReuseItem   // 直接复用，带 confidence 与 knowledge_version
    Explore    []ExploreItem // 需要探索
    Degraded   bool
    Reason     string
}

// backend/internal/knowledge/compiler.go
type Compiler interface {
    Compile(ctx context.Context, in CompileInput) (*CompiledContext, error)
}

// backend/internal/knowledge/change.go
type ChangeManager interface {
    OnAgentEdit(ctx context.Context, ev AgentEditEvent) error   // 主路径，同步标记
    OnGitSync(ctx context.Context, ev GitSyncEvent) error       // 校正路径
    OnFSNotify(ctx context.Context, ev FSNotifyEvent) error     // 优化路径，可关闭
}
```

与现有模块的对接方式：

- `contextmgr.Manager` 增加字段 `Knowledge *knowledge.Planner` 与 `KnowledgeMode string`；`BuildInput` 增加 `TaskID`（已有）与 `WorkspaceID`（已有）。
- `contextpack` 增加 `knowledge.Provider`，实现 `Provider{Name() = "knowledge", Build(ctx, input)}`；provider 内部只读。
- `toolkit.Registry` 注册 `code.search` / `code.inspect` / `code.navigate` / `code.references` / `code.callers`；实现内部持有 `knowledge.Query`，不可用时调用现有 `tools.GrepTool` / `tools.ViewTool`。
- `usageledger` 增加字段：`exploration_tokens`、`reuse_tokens`、`index_lookup_count`、`index_hit`、`fallback_count`、`unsafe_reuse_count`。
- `internal/migrate` 接入 `schema_migrations`；knowledge 的迁移脚本单独目录 `backend/internal/knowledge/migrations/`。

### 4.6 工具面收敛（补 G4）

**决策：`code.*` 是增强前端，不替换 P0 工具。**

| 现有工具 | 新增工具 | 关系 |
|---|---|---|
| grep | code.search | code.search 先查符号/FTS，不足时内部调用 grep；返回中标注 `source` |
| view（按行） | code.inspect（按符号） | view 增加可选 `symbol` 参数；无索引时退化为行范围 |
| glob/ls | code.navigate（按图） | code.navigate 只做图遍历，不做文件列举 |
| — | code.references / code.callers | 无现有对应；索引不可用时返回 `degraded` 并提示用 grep |

**降级协议**（必须实现并测试）：

```text
code.search 调用链：
  1. Planner 判定 knowledge.mode
  2. off      → 直接走 grep（返回 source="fallback"）
  3. shadow   → 走索引计算 + 走 grep 返回；记录差异
  4. on       → 走索引；结果为空或 confidence < 0.5 时补一次 grep 并合并
  5. 任何错误 → 走 grep（返回 source="fallback"，附 error 摘要）
```

**系统提示更新**：在工具选择指引中明确"优先 `code.*` 做符号级问题；`grep` 用于文本/配置/日志；索引不可用时两者等价"。这一步必须与本仓库 `aicli-tool-capability-convergence-plan.md` 合并评审，避免两套收敛方案冲突。

### 4.7 并发与存储（补 G2）

| 问题 | 方案 |
|---|---|
| 谁写 | 每个 workspace 一个 owner 写者：优先由当前 agent 进程担任；若已有活跃 owner（`owner.json` 心跳 < 30s 且 PID 存活）则本进程只读 |
| 多实例 | 两个 aicli 同时打开同一 workspace：先到者为写者，后者只读；后者可请求"接管"，需前者心跳超时 |
| runtime-server | runtime-server 是托管多 session 的天然 owner；aicli local 模式在检测到 runtime-server owner 时只读 |
| 写队列 | owner 内单 goroutine 消费 `index_jobs`；agent edit 只入队不阻塞 turn；若队列深度超阈值则丢弃 deep index 只保留 light index |
| 锁等待 | 所有知识库操作走 `sqliteutil.OpenFileCtx`；读操作 p95 锁等待 > 50ms 时记录告警并降级为"不查索引" |
| 外部变更 | turn 开始前做一次 `git status --porcelain` + `git diff --name-only`（若可用）；不可用则依赖 fsnotify；两者都不可用则标记 `index_state=unknown` 并在 context 中降权 |
| 损坏恢复 | `PRAGMA integrity_check` 在启动时异步执行；失败则重命名 DB 并触发 full_rebuild；重建期间 `knowledge.mode` 自动降为 off |
| 迁移 | `schema_migrations` 版本化；新版本写出的 DB 不被旧版本打开（版本高于代码支持则拒绝，降级为只读 + 提示） |

**新增配置**（写入 `backend/configs/config.yaml` 与 `runtime.yaml`，与既有 `workspace/context/compact/recall` 同级）：

```yaml
knowledge:
  mode: "off"            # off | shadow | on
  db_path: ""            # 默认 <workspace>/.aicli/knowledge/knowledge.db
  adapter: "builtin"     # builtin | tree-sitter | lsp
  lsp:
    enabled: false
    servers: {}          # language -> command
    max_processes: 2
    memory_limit_mb: 512
  index:
    max_file_size_kb: 1024
    include_tests: true
    deep_index: "async"  # sync | async | off
    debounce_ms: 500
    full_rebuild_on_adapter_change: true
  ignore:
    use_gitignore: true
    builtin: true
    extra: []
    secrets: ["**/.env*", "**/*.pem", "**/*.key", "**/credentials*", "**/id_rsa*"]
  embedding:
    enabled: false       # 默认关闭，开启需显式确认
    provider: "local"    # local | openai
  limits:
    max_symbols: 200000
    max_db_size_mb: 512
    max_index_time_per_turn_ms: 200
```

### 4.8 安全与隐私（补 G6/G14）

1. **默认不出网**：`embedding.enabled=false`；开启 `provider=openai` 时必须二次确认并在日志与 UI 中显示"代码内容将发送到第三方"。
2. **默认忽略 secret**：`**/.env*`、`**/*.pem`、`**/*.key`、`**/credentials*`、`**/id_rsa*`、`**/.git-credentials`、`**/.aws/**`、`**/.ssh/**`；这些文件不索引、不入 FTS、不出现在 context 中。
3. **索引范围受现有权限约束**：知识层读取文件必须经过 `fsscope`/`policy`，不得绕过 agent 的文件读取权限；只读子代理不得扩大索引范围。
4. **审计**：每次 embedding 出网记录 provider/模型/字符数（不记录内容）；每次 `unsafe_reuse` 记录 symbol/version/阈值。
5. **LSP 沙箱**：LSP 子进程继承最小环境变量；禁止网络（可用 OS 级 sandbox 或至少不在配置中注入 API key）；崩溃后回收子进程树。

### 4.9 非目标（v1 明确不做）

- 不做 ABAP 适配、不做 SAP 相关概念。
- 不做跨语言 IDL/HTTP/RPC/Message 图。
- 不做 Runtime Evidence。
- 不做分布式/远程索引服务；不做多机共享索引。
- 不做向量检索作为主路径；embedding 默认关闭。
- 不做类型系统/泛型/重载/继承的完整建模（只保留 `kind` 与 `signature` 字符串）。
- 不做 LSP 全量能力（v1 只用 definition/references，且可选）。
- 不做 Java/Rust/C++ 的语义级支持（只做文件 + 粗符号）。
- 不做 UI（先只暴露 HTTP/CLI 状态）。
- 不做"自动修复引用"（只记录 alias，不自动改写代码）。


---

## 5. 分阶段落地路线（带验收门槛与回滚）

排期原则：

1. 每个 Phase 必须能独立上线、独立回滚、独立测量。
2. 前一个 Phase 的验收未通过，不得进入下一个 Phase。
3. 任何 Phase 都不得让 `knowledge.mode=off` 时的行为发生改变（作为回归测试基线）。
4. 每个 Phase 结束时必须更新本文档的"状态"列与 `docs/knowledge_Layer/README.md`。

### Phase 0 — 基线与契约（建议 1 个迭代）

**目标**：先能测量，再谈优化；冻结 v1 schema 与接口。

**交付**

1. `knowledge.mode = "off"` 为默认值；知识层代码可存在但完全不参与任何路径。
2. `usageledger` 扩展字段：`exploration_tokens`、`reuse_tokens`、`index_lookup_count`、`index_hit`、`fallback_count`、`unsafe_reuse_count`、`tool_calls_per_task`、`repeated_read_count`、`knowledge_version_mismatch_count`。
3. 基线报告：以本仓库（排除 `node_modules`/`dist`/`.aicli`）+ 1 个外部 Go 仓库为样本，跑 5–10 个代表任务，记录 token 构成、工具调用数、重复读取次数、p95 延迟。
4. `docs/knowledge_Layer/README.md` 索引 + 本文档落盘。
5. v1 DDL（§4.3）+ `schema_migrations` + 迁移脚本骨架（接入 `internal/migrate`）。
6. 一次与相邻计划的交叉评审：`aicli-tool-capability-convergence-plan.md`、`tool-output-artifact-cascade-audit-and-optimization-plan-20260919.md`、`composer-at-file-reference-workspace-search-plan.md`、`llm-cache-analytics-unified-plan.md`。
7. `exploration_attribution` 表（ADR-0003 §4.1）：追加到 `usageledger` `init()` 的 statements 切片（含两个索引）。**Phase 0 只建表与埋点骨架，不产生数据**（`mode=off` 下不存在 shadow 调用）；M1–M4 的计算入口同时就位。
   - 归属理由（2026-09-21 补入）：该表此前只写在 ADR-0003 §4.1，`04` / `06` 均未列 Phase，形成"**M1（Phase 1 主门槛）由一张没有 Phase 归属的表计算**"。ADR-0003 §6.1 要求 Phase 0 结束时它已存在，故归 Phase 0。

**验收门槛**

- 能回答"当前每个任务平均多少 token 花在探索/重复读取"，且数字可由 ledger 复算（同一份数据两次计算结果一致）。
- `knowledge.mode=off` 下全量回归测试与改动前一致。
- schema 能被 `sqliteutil.OpenFileCtx` 打开，无 `database is locked`、无 `PRAGMA` 报错。
- `exploration_attribution` 表可被 `sqliteutil.OpenFileCtx` 打开；重复 init 幂等、不重复建表（ADR-0003 §8）。

**回滚**：删除 knowledge 包与配置项，零行为影响。

**状态**：**核心交付已完成**（2026-09-20）——`mode=off` 默认、`usageledger` 9 个归因字段、v1 DDL + 迁移骨架、基线报告（3 个仓库 + 5 个真实任务）、相邻计划交叉评审（详见 `06` §4 Phase 0 与 [`reports/phase0_baseline_report.md`](reports/phase0_baseline_report.md)）。
**A/B（off vs shadow）延期至 Phase 1**——知识层未接入任何进程，shadow 为 no-op。
**交付 7（`exploration_attribution`）已于 2026-09-28 落地**——`usageledger` `init()` 追加建表与 2 索引（只建表，不产生数据），见 `CHANGELOG.md`。
**交付 2 的"`usageanalytics` 暴露 9 指标"已于 2026-09-28 落地**——聚合面在 `internal/api/runtimeapi/usage_ledger_group.go`（`group_by=profile`），SDK 类型见 `pkg/skillsapi`；`internal/usageanalytics` 不读 ledger，故落点与本节文字表述不同（见 `CHANGELOG.md`）。
文档治理尾项（`06` §9 条目 8 / 9）已于 2026-09-28 完成（见 `06` §9.3），属文档维护，不影响工程验收。

### Phase 1 — 索引 MVP（只读，影子模式）

**目标**：持久化 file / symbol / refs 轻索引 + FTS5；不改变任何模型可见行为。

**交付**

1. `knowledge/index`：从 `workspace/scanner.go` 升级。
   - 保留正则作为 builtin adapter；补 Java / Rust / C++ 粗符号（`class`、`func`、`fn`、`struct`、`interface` 级别）。
   - **修正测试文件被忽略的问题**：改为索引并写 `files.is_test=1` / `symbols.is_test=1`；`ignorePatterns` 里的 `.*\.test\.(go|py|js|ts)$` 与 `^_\w+` 必须移除或改为标记，否则 `code.tests` 与影响面分析永久为空。
   - 输出 `content_hash`、`is_generated`、`language`、`size`、`mtime_ns`。
2. `knowledge/store`：§4.3 的 v1 表 + `symbols_fts` 同步触发器。
3. 增量：仅 `content_hash` 变化才重解析；删除文件标记 `deleted_at` 而不是立即物理删除。
4. `knowledge.mode=shadow`：在**既有 `grep` / `view` 工具的执行路径上做拦截**（ADR-0003 §4.3 拦截范围、§4.4 候选查询映射），索引侧同时算候选结果，**仍返回 `grep` / `view` 的原结果**，把逐调用对比写入 `exploration_attribution`（**不写 `invalidation_events`**——该表 reason 闭集为 `file_changed|git_sync|adapter_upgraded|schema_upgraded|manual`，属变更源事件，见 §4.3；2026-09-29 口径修正）。
   - **注意（2026-09-21 修订）**：`code.search` 是 **Phase 3** 交付（见下 Phase 3 交付 1）。Phase 1 **不新增任何工具**，只拦截既有工具；原表述用 `code.search` 定义 Phase 1 shadow，会让 Phase 1 依赖 Phase 3 产物而无法开工。
5. `knowledge.status` CLI / HTTP：索引状态、文件数、符号数、DB 大小、最近 job、锁等待 p95。
6. **接入（激活）——三个入口**：`knowledge.Open` 接入 `cmd/runtime-server`（启动阶段调用 + 向 `internal/background` 注册索引任务，默认 writer owner）、`cmd/aicli` cmd/tui（`commands/chat.go` 解析 workspace 后调用）、`cmd/aicli` acp。规格见 `supplement/05` §2、§8。
   - **归属理由（2026-09-21 补入）**：本项此前只写在 `supplement/05`，`04` 与 `06` §5.2 均未列。**没有这一项，Phase 1 的 shadow 没有任何进程会打开知识层，验收无法进行**——本次 A/B（off vs shadow）延期即此因。

**验收门槛**

- **主门槛（唯一 Pass/Fail 判据）**：ADR-0003 §4.5 的 **M1 调用级可用率**——在 `baseline_n > 0` 的被拦截调用上，`usable = (coverage ≥ α) AND (economy ≤ 1.0)` 的均值达标。**α 由本 Phase 的 shadow 实测校准后写入 config**（ADR-0003 §10，Gate = `Phase1-shadow`；**2026-09-29 定稿：α=0.8，门槛 = 合并 M1 ≥ 0.31，复核通过**）。
- **诊断指标（不判 Pass/Fail，用于定位失败）**：M2 覆盖度、M3 经济性、M4 token 收益（ADR-0003 §4.5）；以及 `code.search` 与 `grep` 的 top-10 文件集合差异率 < 15%——差异率**保留为可解释性诊断**，不再是验收口径（2026-09-21 修订，消除与 ADR-0003 §4.5 的双口径冲突）。
- 本仓库（排除 `node_modules`/`dist`/`.aicli`）首次全量索引耗时 ≤ 实测基线（**2026-09-29 已校准：≤ 360 s**，报告 §4.7）；单文件增量 < 50ms（**实测 Fail**：marginal p95 302 ms，须 Phase 5 增量触发或口径重议）。
- DB 大小 ≤ `max_db_size_mb`（默认 512MB；实测 313.9 MiB）——原 200MB 初值已被 §7.4 校准取代；`max_db_size_mb` 生效时可触发 GC。
- `files.content_hash` 与磁盘一致率 100%（抽样 ≥ 200 文件）——2026-09-29 实测 261/261。
- 锁等待 p95 < 50ms——2026-09-29 抽样 0 样本 / 0 重试失败（证据弱，报告 §4.6）。
- **接入验证**：三个入口（runtime-server / aicli cmd+tui / aicli acp）在 `mode=off` 下行为与改动前完全一致；`mode=shadow` 下 `exploration_attribution` 有数据落库，且 M1 可复算（同批记录两次计算结果一致）。

**回滚**：`mode=off` + 删除 `knowledge.db`。

**状态**：**进行中**（2026-09-28 开工）——交付 1–3 的代码已在 `internal/knowledge`（`indexer.go` / `store_sqlite.go` / `adapter_builtin.go` / `owner.go` / 增量 `content_hash`）；**交付 6「接入（激活）」已完成**（`internal/knowledge/activation.go` + `cmd/runtime-server` / `cmd/aicli` cmd+tui / `cmd/aicli` acp 三入口，见 `CHANGELOG.md`）；**交付 4（shadow 拦截 `grep` / `view`）已完成**（2026-09-28：三入口接线 + `exploration_attribution` 落库，见 `CHANGELOG.md`）；**交付 5（`knowledge.status` 面）已完成**（2026-09-28：CLI + HTTP 状态面，见 `CHANGELOG.md`）；**2026-09-29 收口交付 1/3**——交付 1 补上 Java/C++ 粗符号（`adapter_builtin.go` 的 `javaSymbolPatterns` / `cppSymbolPatterns` + `adapter_java_cpp_test.go`），交付 3 补上删除对账（迁移 `0002_file_soft_delete.sql`、`files.deleted_at`、`ListActiveFiles` / `MarkFilesDeleted`、`RunIndex` 软删除与复活，含测试）——交付 1–6 全部落地；**2026-09-29 `Phase1-shadow` 实测 v1**（真实调用重放 n=400）：M1=20.81 %（view 48.4 % / grep 行级 0.47 %）、M2=24.15 %，主门槛不通过；grep file-level 对照 mean 31.83 %、answerable 49.4 %，瓶颈为行级口径（§6.2），需新 ADR 裁决；**2026-09-29 裁决后复核（ADR-0008 Accepted、α=0.8）：grep file-level 26.76 %、view 48.41 %、合并 M1=35.95 % ≥ 0.31 → 主门槛通过**（P2 进入条件 = ADR-0004 Accept；ADR-0008 新列实现与 grep 映射收敛仍为 Open）；见 `reports/phase1_shadow_report.md`（含 §4.5 live 验证：aicli cmd+tui + ACP + runtime-server，3/3 入口）。本 Phase 主门槛已通过，整体验收的剩余项见报告 §5。

### Phase 2 — Exploration Memory + Context Planner

**目标**：解决"多轮重复探索"，这是 00/01 共同认定的最高 ROI。

**交付**

1. `exploration_sessions/nodes/edges` 落库；节点带 `knowledge_version`。
2. `memorystore`（长期笔记）与 exploration memory（任务工作集）分层：前者不自动写入，后者自动写入；两者在 `context_items` 中用 `item_type` 区分。
3. `contextmgr` 新增 `KnowledgeMode = off | signals | broad`，与 `WorkspaceMode/RecallMode` 对称；`Strategy` 增加 `MinKnowledgeQueryLength`、`ReuseConfidenceFloor`。
4. `knowledge.Planner`：输出 `Plan{Reuse, Explore, Degraded, Reason}`；复用需满足 §4.4 阈值。
5. 复用必须做一次验证读取（confidence < 0.90 时）。
6. 多 Agent 语义：探索节点写 `session_id + task_id + workspace_id`；只读子代理不写索引、不写 exploration memory；跨任务复用阈值 ≥ 0.90。

**验收门槛**

- 多轮任务的重复工具调用次数下降 ≥ 30%（用 ledger 归因，样本 ≥ 20 个任务，且需给出置信区间）。
- `unsafe_reuse_count = 0`（复用 stale 或低置信内容）。
- 端到端 p95 延迟增幅 ≤ 10%。
- 关闭 `KnowledgeMode` 后指标回到基线（可逆）。

**回滚**：`KnowledgeMode=off`。

**状态**：实现完成（2026-09-30）——W1–W7 全覆盖（W7 分 W7a 激活装配 / W7b 测量段两切片，登记见 `06` §4 Phase 2）；遗留：真实 on-mode A/B ≥20 任务实测待跑（报告落点 `reports/phase2_exploration_report.md`）、`verify_requested` 消费方未接线、`broad` 档阈值校准留后续（`06` §9 #21 确认预存 / #22 flaky，不阻塞）。

### Phase 3 — Code API 与工具面收敛

**目标**：把探索变成查询；与现有工具并存且可灰度。

**交付**

1. `code.search` / `code.inspect` / `code.navigate` / `code.references` / `code.callers` 注册进 `toolkit.Registry`。
2. 统一返回结构：`source` / `confidence` / `version` / `range` / `truncated` / `next_cursor` / `explanation` / `degraded`。
3. 降级协议（§4.6）实现并测试：索引不可用 / 低置信 / 出错 → fallback 到 `grep` / `view`，返回带 `source="fallback"`。
4. `view` 增加可选 `symbol` 参数（按符号读取），无索引时退化为行范围。
5. 系统提示更新：明确 `code.*` 与 `grep/view` 的分工；与 `aicli-tool-capability-convergence-plan.md` 合并评审。

**验收门槛**

- 典型任务（"改 timeout 默认值""谁调用 X""影响面分析"）探索 token 下降 ≥ 40%。
- 无索引环境下 `code.*` 100% 可用（降级路径测试覆盖所有工具）。
- fallback 触发率可观测，且不高于 30%（否则说明索引质量不达标）。
- 工具调用总数不增加（避免"两个工具都试一遍"造成的反向回归）。

**回滚**：工具开关关闭，回到 `grep/view`。

**状态**：实现完成（2026-09-30）——交付 1–5 全部落地（工具注册名以下划线形式；`knowledge.code_tools` 默认 off，灰度开启；降级协议含 mode=off / shadow / on 三档口径）；遗留：收益类验收（探索 token ↓≥40%、fallback ≤30%、调用总数不增加）与 M3 判定待测量轮；`configs/model_cards.yaml` 无工具面清单未改（系统提示载体为工具描述）；`cmd/toolkit-mcp-server` 无 workspace 上下文未接。

### Phase 4 — Adapter SPI 与可选 LSP

**目标**：把语言差异移出 core。

**交付**

1. `LanguageAdapter` SPI + 能力声明（`AdapterCapabilities`）。
2. builtin adapter（v1 默认）、tree-sitter adapter（可选）、lsp adapter（可选，进程外）。
3. LSP 进程管理：启动 / 崩溃 / 超时 / 内存上限 / 僵尸回收；Windows 优先验证。
4. adapter / parser 版本参与 `stable_key` 与 `confidence`；版本变化触发 `full_rebuild_on_adapter_change`。
5. 离线模式：无 LSP 时仍可 search / symbol / file / 基本图。

**验收门槛**

- Go 仓库 definition / references 精度 ≥ 90%、召回 ≥ 85%（golden set，人工标注 ≥ 200 条）。
- LSP 崩溃 100% 降级不阻断 agent；进程数 ≤ `lsp.max_processes`；内存 ≤ `lsp.memory_limit_mb`。
- 关闭 adapter 后 builtin 仍可用（能力矩阵测试）。

**回滚**：`knowledge.adapter=builtin`，LSP 关闭。

**状态**：实现完成（2026-09-30）——交付 1–5 全部落地（SPI/能力声明、builtin+tree-sitter(降级)+lsp(进程外) 三通道、进程管理含锁/上限/崩溃回收与 executor 守卫、adapter 版本参与身份与全量重建、离线降级），并完成**语义通道接入工具面**（引用类三工具 + `code_navigate` 按位置查定义；`source=lsp`；位置口径转换集中一处）+ 索引引用抽取字符串守卫 + **ACP `knowledge.lsp.mode` select 接线**（ADR-0002 §4.2；会话级覆盖在运行时配置副本上落地）。验证：34 例新测试；golden set 1765 条（≥200），builtin definition P=1.0000 / R=0.8510；LSP live：definition P=0.9241–0.9750 / R=0.9125–0.9750 ✓，references P=1.0000 / R=1.0000 ✓（原 0.7674 为裁决口径错误 + 索引字符串误报，已修）。✅ 前置 ADR-0002/0005/0006 已于 2026-09-30 授权代改 Accept；遗留：tree-sitter 未接语法、按名字 definition 仍走索引、会话覆盖未持久化（重启回配置默认）、内存探测口径（tasklist）、live 测量需独占运行且异常终止会遗留 gopls。

### Phase 5 — Change Manager 与一致性

**目标**：索引与代码变化同步且可证明正确。

**交付**

1. 三类变更源：agent edit hook（主，同步标记）、git diff（校正）、fsnotify（优化，可关闭）。
2. debounce + `index_jobs` 串行队列 + owner 仲裁（§4.7）。
3. 版本向量：`knowledge_version` 落地并参与 cache key 与 context item。
4. GC：`deleted_at` 超过 30 天或 N 个版本的符号 / 文件清理；DB 超过 `max_db_size_mb` 时触发。
5. 增量 vs 全量等价性测试：固定样本仓库，编辑 N 次后比较增量索引与全量重建结果。
6. schema 迁移 + 版本拒绝（新 DB 不被旧代码打开）。

**验收门槛**

- agent 连续编辑 100 次后，增量索引与全量重建结果 diff = 0。
- 外部 `git checkout` 后 stale 判定正确率 100%（样本 ≥ 50 次）。
- 索引写不阻塞会话读：锁等待 p95 < 50ms，锁重试失败率 < 0.1%。
- 双实例同时打开同一 workspace：后到者正确降级为只读，无锁死、无长时间无响应。

**回滚**：关闭 watcher，退回显式 `knowledge.reindex`。

**状态**：**实现侧收口 + 真实会话 E2E 完成（切片 1–8，2026-10-01）**——交付 1（**三类变更源**：edit hook 全链 + git/stat 外部校正 + fsnotify 可选源 `knowledge.watch`）、交付 2（debounce + 串行队列）、交付 3（**版本向量参与复用判定三处口径一致**：索引侧代次化缓存 + 判定点校正 + `#pending` 标记；判定侧与注入侧对未稳定 token 一律 fail-closed）、交付 4（GC）、交付 5（增量 vs 全量等价性）、交付 6（迁移版本拒绝）、**R12 第三段（库损坏留证重建 + 重建期降级）**、**三项验收门槛的可复现化**与**真实会话远程 E2E**（7 轮 `POST /web/api/invoke`，报告见 [`reports/phase5_real_session_e2e_report.md`](reports/phase5_real_session_e2e_report.md)）全部落地：① `IndexPaths`（定向增量：不遍历/不写 adapter 版本/越界与后缀计入 Errors/磁盘已删走软删除/adapter 版本不一致整体跳过）+ `change_queue.go`（Mark 非阻塞幂等去重 → debounce 300ms → 单 worker 串行，MaxBatch 256，Close 幂等等待；**修复"重复标记重置 debounce"的饥饿缺陷**）+ `IndexJobKindIncremental`；② edit hook：`toolctx.WithFileChangeNotifier` + 编辑类工具（write/edit/multiedit/append_write/apply_patch/download）落盘成功后报告 → agent 两条工具执行 ctx 注入 → 队列；队列所有权在 `Layer`（多 Activation 共享 worker）；shadow 档发布句柄但不设 mode（不注入 prompt）；③ **变更源 2（外部校正）**：`change_git.go`（HEAD 移动 + 工作树状态，按转移报告、`GIT_OPTIONAL_LOCKS=0` 只读）+ `change_scan.go`（已索引文件 size+mtime 比对 + `Fresh` 集合过滤 git 侧假阳性，`git checkout --` 还原的唯一发现者）+ `change_sync.go`（`Layer.SyncExternalChanges` 三源合并入队、分源节流；`Layer.ObserveVersion` 作为判定点：发现变更即失效版本缓存并给 token 附 `#pendingN`，fail-closed）+ 版本缓存**代次化**（`ChangeQueue.Generation`，修掉"失效后又被写回旧版本"竞态）+ agent `run()` turn 边界触发；④ **迁移版本拒绝**：`migrate.Apply` 公共收口点拒绝"库 schema 新于本二进制"（`ErrSchemaNewer`）+ knowledge 只读路径 `verifyInitialized` 同步补上，构成双向口径（落后：reader 拒绝 / owner 迁移；超前：两条路径都拒绝）；⑤ **GC**：`GCDeleted` 单事务先显式删 symbols（外键级联不触发 FTS 触发器）再删 files（级联 refs/versions/aliases）+ 保留期 30 天可配 + `shouldAutoGC`（超 `max_db_size_mb` + 10 分钟冷却）+ `Layer.RunGC`/`maybeAutoGC` + `StatusReport.GC`；`DefaultMaxDBSizeMB` 200 → 512（§7.4 校准值）；⑥ **验收门槛可复现化**（`acceptance_phase5_test.go`）：100 次连续编辑（经变更队列）后与终态全量 ID 级 diff = 0；50 轮 `git checkout --` 外部写盘 → 检出 50/50、索引追上后误报 0；写侧持续增量 + 读侧并发读 → 读延迟 p95 ≈ 1.09ms、锁等待 p95 = 0ms、重试失败 0（门槛 50ms / < 0.1%）；第 4 条门槛（双实例只读降级）由既有 activation/owner 用例覆盖；⑦ **等价性测试（ID 级逐行）当场捕获并修复既有缺陷**：`loadKnownSymbols` 未排除被重写文件的旧符号 → 同名新旧并存"歧义即不绑定" → 引用静默丢 `to_symbol_id`（全量重建对自遮蔽同样中招）。验证：45 例新测试 + 3 项验收用例 + 4 例恢复用例 + 4 例 watcher 用例 + 4 例交付 3 用例（含 `-race`）+ `go build ./...` OK + `knowledge`/`migrate`/`agent`/`toolkit`/`tools`/`toolctx`/`contextmgr`/`runtimeapi`/`subagentbatch`/`artifact`/`supervision`/`agentcontrol`/`cmd/runtime-server` 全绿；改 2/加 1/删 1 后增量与终态全量 diff = 0；edit hook 端到端通过；外部校正端到端（真实临时 git 仓库：外部改写 / `git checkout --` 还原 → 判定立即保守 → 索引追上后稳定）通过；GC 端到端（过期软删除清理 + 保留期 + 触发口径 + 只读拒绝）通过；恢复端到端（损坏库留证重建 + 状态面降级解释 + 版本不匹配绝不留证）通过；watcher 端到端（真实 fsnotify 事件：空闲期新建/修改代码文件 0.70s 内进索引、忽略路径不入索引、默认 off 无监听、配额耗尽显式降级）通过；交付 3 端到端（两侧同为 `#pending3` → explore/不可用/Stale，未稳定条目在注入前被过滤）通过；真实会话远程 E2E 通过（7 轮，三方证据对齐；报告见 `reports/phase5_real_session_e2e_report.md`）。登记① 同日收口：新增 `GET /web/api/knowledge[/status]`（与 `aicli knowledge status`、runtime-server 同源同形，mode=off 返回最小载荷而非 404；真实进程实测 owner 态 `watch.active=true` 与 reader 降级态）；登记③ 同日收口：真机 `watch:on`（零 turn）外部新建/修改/删除 ~0.35–0.40s 由事件路径吸收（并修复 fsnotify 漏掉 `Remove` 事件）。遗留：on-mode A/B（≥20 任务）与端到端 p95 统计门槛（测量轮）；登记发现 `FindSymbols` 无 workspace 过滤（生产 store 与 workspace 一一对应，故不可达）。

### Phase 6 — Context Compiler 深度集成

**目标**：把知识层输出变成最小上下文。

**交付**

1. `contextpack` 新增 `knowledge.Provider`（只读）。
2. `contextmgr.LayerPlan` 增加 knowledge layer，映射到 hot / warm / cold。
3. Observation Compressor 与 `compactruntime` 合并，避免两套压缩。
4. 防注入 / 信任等级 / 冲突解决（对应 03 §14）：`context_items.trust` 与 `reason` 落地。
5. `context_snapshots/items` 可解释性：每个 item 有 source / version / trust / reason / stale。

**验收门槛**

- 相同任务上下文 token 下降 ≥ 25%，任务成功率不降（A/B，样本 ≥ 20）。
- `stale` item 注入数 = 0（有 stale 标记的 item 绝不进 prompt）。
- compiler 缓存命中 p95 < 50ms；未命中 p95 < 200ms。
- 关闭 knowledge provider 后行为回到 baseline。

**回滚**：provider 开关关闭。

**状态**：**切片 1–5 落地（2026-10-01）**——①语义内核 `knowledge/compiler.go`（信任等级闭集 + 冲突优先级 + stale/下限/预算过滤 + `context_items` 语义镜像 + data block 渲染）+ 8 例测试；②compile 层缓存（`cache_entries` 窄接口 + 确定性键（含知识版本/编译器版本）+ 降级直算 + 命中/时延指标；真库 200 次实测 hit p95 0.54ms / miss p95 1.07ms）+ 8 例测试；③contextmgr 接线（注入走 `CompilePlan` + data block + `KnowledgeCache` + `LayerPlan.knowledge` 与 hot/warm/cold tier；off 可逆）+ 3 例测试；④contextpack 只读 provider（结构化视图 + 有界 digest data block + Reduce 保块 + on 门控装配）+ 5 例测试；⑤`context_snapshots`/`context_items` 落库（迁移 0004 + 只记注入条目 + 幂等 + reader 粘性停用 + 真库读写）+ 7 例测试。前置（Phase 2/3/5 验收）已满足；Phase 5 的 on-mode A/B 与端到端 p95 仍待测量轮（与 Phase 6 的 A/B 合并跑）。

### Phase 7 — Semantic Retrieval（可选，后置）

**交付**

1. FTS5 优先；embedding 默认关闭，开启需显式配置 + 隐私确认。
2. 本地 embedding 优先；`openai` provider 需二次确认并写审计。
3. rerank：`lexical + symbol + graph + task_relevance + recency + confidence`（对应 02 §76）。

**验收门槛**

- 在"不知道符号名"的任务上 recall 有可测提升（golden set）。
- 默认配置下出网次数 = 0。
- 开启 embedding 后端到端延迟增幅 ≤ 20%。

**回滚**：`embedding.enabled=false`。

**状态**：未开始

### Phase 8+ — 明确推迟

跨语言 IDL / HTTP / RPC 图、Runtime Evidence、ABAP 适配、分布式索引、自动引用修复。**不在 v1 承诺范围内**，需在 v1 稳定并有真实需求后单独立项。

### 5.1 Phase 依赖图

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

说明：Phase 4 与 Phase 2 无强依赖，可并行；Phase 5 必须在 Phase 3 之后合入（工具面先稳定，再引入变更源）；Phase 6 依赖 Phase 2 + Phase 3 + Phase 5。

### 5.2 里程碑判定表

| 里程碑 | 判定条件 | 决策 |
|---|---|---|
| M0 可测量 | Phase 0 验收通过 | 继续 / 若基线显示探索 token 占比 < 10% 则降优先级 |
| M1 索引可信 | Phase 1 验收通过（shadow 差异率达标） | 继续 / 否则重构索引而非进入 Phase 2 |
| M2 复用安全 | Phase 2 验收通过（unsafe_reuse=0） | 继续 / 否则回退 Phase 1 |
| M3 工具可切换 | Phase 3 验收通过（fallback ≤ 30%） | 继续 / 否则暂缓 Phase 4 |
| M4 一致可证 | Phase 5 验收通过（增量=全量） | 继续 / 否则禁止开启 `mode=on` |
| M5 端到端收益 | Phase 6 验收通过（token ↓ ≥ 25% 且成功率不降） | 正式 GA / 否则保持 shadow |

---

## 6. 风险登记与对策

| ID | 风险 | 触发信号 | 影响 | 对策 | 责任阶段 |
|---|---|---|---|---|---|
| R1 | 多进程写锁导致启动无响应（本仓库已发生过） | `database is locked` 重试失败、启动 > 5s | 高 | 单写者 owner + 只读降级 + 全部走 `sqliteutil` + 锁等待 p95 告警 | Phase 0/1/5 |
| R2 | 索引静默漂移，Context Compiler 基于错误知识给出高置信度上下文 | 增量/全量 diff ≠ 0；`unsafe_reuse > 0` | 高 | shadow 模式 + 增量全量等价性测试 + `knowledge_version` 强校验 + stale 拒绝注入 | Phase 1/5/6 |
| R3 | 工具面混乱：模型在 `grep` 与 `code.*` 之间反复试探 | 工具调用数上升、fallback 率 > 30% | 中高 | 不替换 P0 工具；`code.*` 为增强前端；系统提示明确分工；shadow 先行 | Phase 3 |
| R4 | 上下文双真相：会话 fresh 但索引 stale | 同一符号出现两个版本 | 高 | 统一版本向量；stale 判定收敛到 `contextreconcile`/`historyguard`；context item 绑 version | Phase 2/5/6 |
| R5 | 隐私泄露：embedding 把私有代码出网 | 出网审计非 0 | 高 | 默认 off；本地优先；secret 默认忽略；出网需二次确认 | Phase 7 |
| R6 | Windows 特有问题：watcher 丢事件、长路径、LSP 僵尸 | watcher 事件与 git diff 不一致；进程数增长 | 中高 | watcher 非唯一真相；git diff 校正；agent edit hook 为主；长路径前缀；进程回收 | Phase 4/5 |
| R7 | 过度设计：v1 就上类型系统/跨语言/Runtime Evidence | 表数量 > 16；Phase 0–2 延期 | 中高 | 非目标清单（§4.9）；表数量上限；任何新增表需评审并记录 ADR | 全程 |
| R8 | 性能回归：索引抢占主 turn | 端到端 p95 上升 > 10% | 中 | 索引异步；`max_index_time_per_turn_ms`；每 Phase 设 p95 门槛；可关闭 | Phase 1–6 |
| R9 | 文档漂移：四份文档继续各自演化 | 术语/编号/schema 再次分叉 | 中 | README 索引；02 为 schema 唯一源；03 拆分为 supplement；变更日志；ADR | Phase 0 |
| R10 | 多 Agent 并发写索引 / 探索记忆污染 | 并发 `index_jobs`；父子会话同一节点冲突 | 中 | per-task 写入 + per-workspace 复用；只读子代理不写索引；owner 串行化 | Phase 2/5 |
| R11 | 忽略规则不完整导致索引噪声（`dist/`、`logs/`、生成代码） | DB 膨胀、检索噪声 | 中 | 内置 + `.gitignore` + 用户规则 + secret 规则四级；优先级明确；`max_file_size_kb` | Phase 1 |
| R12 | schema 迁移失败导致 DB 不可用 | 启动报版本不匹配 | 中 | `schema_migrations` + 版本拒绝 + 损坏时重命名重建 + 重建期降级 off | Phase 0/5 |
| R13 | 评估缺失导致无法判断收益 | Phase 验收靠主观 | 中 | Phase 0 先建 golden set 与基线；指标从 ledger 可复算 | Phase 0/4 |
| R14 | 与既有计划冲突（tool-capability-convergence / workspace-search / llm-cache-analytics） | 重复实现或相互矛盾 | 中 | Phase 0 做一次计划交叉评审；明确依赖与边界 | Phase 0/3 |
| R15 | 权限旁路：知识层读到 agent 本来无权读的文件 | 索引内容超出 `fsscope` 允许范围 | 高 | 索引范围必须走 `policy`/`fsscope`；secret 默认忽略；audit 记录 | Phase 1/5 |
| R16 | 单写者仲裁失效（心跳误判、PID 复用） | 两个进程同时认为自己是 owner | 高 | owner.json 记录 PID + 进程启动时间 + boot id；抢占前二次校验；冲突时以 DB 内 `owner_epoch` 为准 | Phase 5 |
| R17 | 首次索引成本过高导致用户放弃 | 首次索引 > 5 分钟或 CPU 100% | 中 | 分批 + 后台 + 可中断；优先索引"最近打开/最近修改"文件；首次只做 light index | Phase 1 |
| R18 | FTS5 中文/Unicode 分词不达预期 | 中文标识符检索召回低 | 中 | v1 明确 FTS 只面向标识符与英文注释；中文需求走 grep fallback；`tokenize` 可配置 | Phase 1/7 |
| R19 | 索引与 git 子模块 / worktree 冲突 | 多 worktree 共享同一 `.aicli` | 中 | `workspaces.id` 加入 worktree 维度或显式隔离 DB 路径 | Phase 1/5 |
| R20 | 评估 golden set 与真实任务分布偏离 | 指标好看但用户无感 | 中 | golden set 必须来自真实任务日志（`usageledger`），每季度重采样 | Phase 0/4 |


---

## 7. 验收指标（可测、可归因、可复算）

### 7.1 指标定义原则

1. **每个指标必须能从现有数据复算**：优先从 `usageledger` / `usageanalytics` 取数；知识层自身统计（索引状态、命中、fallback）写入同一账本，不另建一套。
2. **必须定义分母**：例如"重复探索率"的分母是"同一 session 内对文件/符号的读取总次数"，不是"工具调用总数"。
3. **必须先有基线再定阈值**：Phase 0 出基线，Phase 1/2 实测校准，最终阈值写入配置默认值。禁止在没有基线的情况下把 03 §20.2 的固定数字当验收标准。
4. **单任务方差大 → 用中位数 + 置信区间 + 样本量**：样本 ≥ 20 个任务；报告 `median` 与 `95% CI`，不只看均值。
5. **收益指标必须配反向护栏指标**：token 降了但任务成功率降了，等于没降。

### 7.2 收益指标

| 指标 | 定义 / 公式 | 数据来源 | 目标（Phase 6 GA） |
|---|---|---|---|
| 探索 token 占比 | `exploration_tokens / total_tokens` | `usageledger`（新增字段） | 相对基线下降 ≥ 40% |
| 重复探索率 | `repeated_read_count / total_read_count`（同一 session 内对同一 file/symbol 的重复读取） | `usageledger` | 相对基线下降 ≥ 30% |
| 上下文 token / 任务 | `context_tokens / task` | `contextmgr.BuildResult.Metadata` + ledger | 相对基线下降 ≥ 25% |
| 工具调用数 / 任务 | `tool_calls_per_task` | `usageledger` | **不增加**（护栏） |
| tokens / 成功改动 | `total_tokens / successful_edits` | `usageledger` + git diff | 下降 ≥ 20% |
| 索引命中率 | `index_hit / index_lookup_count` | knowledge 统计 | ≥ 70% |
| fallback 率 | `fallback_count / code_tool_calls` | knowledge 统计 | ≤ 30% |
| 缓存命中率 | `cache_hit / cache_lookup`（retrieval/compile 分层统计） | knowledge 统计 | ≥ 50%（compile 层） |

### 7.3 正确性与安全指标（硬门槛）

| 指标 | 定义 | 目标 | 违反后果 |
|---|---|---|---|
| `unsafe_reuse_count` | 复用了 `stale=1` 或 `confidence < 阈值` 的内容并注入 prompt 的次数 | **= 0** | 阻断 Phase 2 验收 |
| `stale_item_injected` | `context_items.stale=1` 且进入最终 prompt 的条数 | **= 0** | 阻断 Phase 6 验收 |
| `knowledge_version_mismatch_count` | context item 版本与当前 `knowledge_version` 不一致却仍被使用的次数 | **= 0** | 阻断 Phase 6 验收 |
| 增量/全量一致性 | 编辑 N 次后 `diff(增量索引, 全量重建)` 的差异条数 | **= 0** | 阻断 Phase 5 验收 |
| 符号解析精度 | golden set 上 `correct / total` | ≥ 90% | 阻断 Phase 4 验收 |
| 符号解析召回 | golden set 上 `found / should_find` | ≥ 85% | 阻断 Phase 4 验收 |
| 引用解析召回 | 同上（references 维度） | ≥ 85% | 阻断 Phase 4 验收 |
| 出网次数（默认配置） | embedding/远程调用计数 | **= 0** | 阻断 Phase 7 验收 |
| 任务成功率 | A/B 对比中任务完成率 | 不低于基线（允许 -2% 以内波动） | 阻断 Phase 6 验收 |

### 7.4 性能指标

| 指标 | 目标 | 测量点 |
|---|---|---|
| 首次全量索引（本仓库规模） | ≤ 360 s（2026-09-29 `Phase1-shadow` 校准：n=3 中位数 292.4 s，95 % CI [241.8, 356.5] s，4989 文件 / 47.8 MB 源码；跨仓库抽验 ×2：38.9–43.8 ms/文件；原初值 ≤120 s 为未校准占位，报告 §4.7） | `index_jobs` 记录 |
| 单文件增量索引 | **p95 < 50 ms（当前 Fail）**：2026-09-29 实测（4989 文件 / 20 样本 / `indexed=1`）job 墙钟 p95 4.35 s、marginal p95 302 ms，仅"全量跳过"基线就 4.05 s——须 Phase 5 Change Manager 增量触发（fsnotify）或重定口径（报告 §4.7） | `index_jobs` 明细 |
| 符号查询（`code.inspect`） | p95 < 100ms | knowledge 统计 |
| 上下文编译（缓存命中） | p95 < 50ms | `context_snapshots` 耗时 |
| 上下文编译（未命中） | p95 < 200ms | 同上 |
| 锁等待 | p95 < 50ms，重试失败率 < 0.1% | `sqliteutil.RetryLocked` 埋点 |
| 单 turn 索引耗时上限 | ≤ `max_index_time_per_turn_ms`（默认 200ms） | knowledge 统计 |
| DB 大小 | ≤ `max_db_size_mb`（默认 512MB）；实测中位数 313.9 MiB（n=3，95 % CI [313.2, 314.3] MiB） | 文件系统 |
| 端到端 p95 延迟增幅 | ≤ 10% | 会话级埋点 |

### 7.5 测量方法

1. **A/B 对比**：同一任务集分别跑 `knowledge.mode=off` 与 `on`，比较收益与护栏指标。任务集必须来自真实 `usageledger` 日志采样，不能只用构造任务。
2. **Shadow 差异分析**：`mode=shadow` 下记录 `code.search` 与 `grep` 的结果差异，人工抽检差异原因（符号精确 vs 文本匹配、索引缺失、规则差异）。
3. **Golden set**：Phase 4 前人工标注 ≥ 200 条 Go 符号/引用关系；Phase 7 前补充"语义检索"样本。Golden set 每季度从真实任务日志重新采样，避免分布漂移。
4. **一致性测试**：固定样本仓库，脚本化编辑 N 次，比较增量索引与全量重建结果。
5. **锁与并发测试**：双进程同时打开同一 workspace，验证后到者只读、无锁死、无长启动。

### 7.6 阈值校准流程

```text
Phase 0：测量基线（无知识层；mode=off）
   ↓
设定初始探索值（本文档 §4.4 的默认值，仅用于管线联调；不得作为验收门槛）
   ↓
Phase 1：shadow 实测 → 校准 α 与 Phase 1 门槛数值   [Gate: Phase1-shadow]
   ↓
Phase 2：on 模式实测 → 校准收益类阈值（含 M4）
   ↓
用中位数 + 95% CI 校准阈值（写入 config 默认值）
   ↓
Phase 3–6：每 Phase 复测，阈值随真实分布更新
   ↓
每季度重采样 golden set 与任务集，防止分布漂移
```

> **2026-09-21 修订（Gate 可达性）**：原流程把 α 与 Phase 1 门槛数值的产出点写作 `Phase0-baseline`。但 α 是 shadow 覆盖率阈值，需要 `grep` 与索引的逐调用对比数据；Phase 0 是 `mode=off` 且知识层未接入任何进程——**该 Gate 结构性不可达，会自锁 Phase 1**。
> 现明确：**Phase 0 只产出"无知识层"的基线数值与偏差警告；α 与 Phase 1 门槛数值的 Gate 是 `Phase1-shadow`**（ADR-0003 §10）。这与 §7.1"没有基线就不要写死阈值"不冲突——被推迟的是**阈值**，不是**口径**。

### 7.7 验收报告模板（每个 Phase 必须产出）

```text
Phase: N
样本: <任务集 id> / <仓库> / <样本量>
时间: <起止>
配置: knowledge.mode=..., adapter=..., embedding=...

收益指标:   基线 → 当前（变化%）  [95% CI]
护栏指标:   基线 → 当前（变化%）
正确性指标: 数值（是否达标）
性能指标:   p50/p95（是否达标）
反例:       <失败/退化案例与原因>
结论:       Pass / Fail
回滚验证:   已验证（是/否）
```

---

## 8. 文档治理建议（如何重构这 4 份文档）

### 8.1 目标目录结构

```text
docs/knowledge_Layer/
  README.md                       索引 + 状态 + 阅读顺序 + 单一事实源声明
  01_architecture_intent.md       架构意图（由现 01 去重后重写）
  02_core_spec_sqlite.md          core schema / 接口唯一事实源
  04_completeness_review_and_optimized_plan.md   本文（评审 + 落地计划）
  GLOSSARY.md                     术语表（唯一术语源）
  CHANGELOG.md                    变更日志
  archive/
    00_code_intelligence_chatlog.md   现 00，顶部加"非规范、仅供追溯"声明
  supplement/
    01_stable_symbol_id.md
    02_project_model.md
    03_types_inheritance.md
    04_name_resolution.md
    05_lsp_engineering.md
    06_version_vector_consistency.md
    07_security_privacy_audit.md
    08_eval_benchmark.md
    09_dynamic_candidates.md
    10_test_intelligence.md
    11_cross_language_idl.md
    12_runtime_evidence.md
    13_fts_multilingual.md
    14_context_compiler_safety.md
    15_change_management.md
    16_new_apis.md
    17_event_model.md
    18_telemetry_kpi.md
  adr/
    0001-single-writer-arbitration.md
    0002-code-tools-are-enhancement-not-replacement.md
    0003-sqlite-not-postgresql-for-v1.md
    0004-db-location-aicli-knowledge.md
    0005-v1-table-set-16-tables.md
    0006-tests-are-indexed-not-ignored.md
    0007-confidence-thresholds.md
```

### 8.2 单一事实源声明（写入 README 顶部）

| 内容 | 唯一事实源 | 其他文档的处理 |
|---|---|---|
| 数据库 schema（core） | `02_core_spec_sqlite.md` | 其他文档只引用，不复制 DDL |
| 数据库 schema（extension） | `supplement/*.md`，且必须在 02 顶部声明应用顺序 | 02 顶部："core schema；启用 extension 前必须先应用 supplement/01 的 ALTER" |
| 术语 | `GLOSSARY.md` | 所有文档使用术语表中的规范名 |
| 架构意图与原则 | `01_architecture_intent.md` | 02/03 不重复原则性内容 |
| 落地计划与验收 | 本文（04） | Phase 状态在 README 与本文同步 |
| 决策记录 | `adr/*.md` | 任何推翻既有设计的改动必须先写 ADR |
| 变更历史 | `CHANGELOG.md` | 每次 schema/接口/术语变更必须记录 |

### 8.3 具体治理动作

| 编号 | 动作 | 对象 | 说明 |
|---|---|---|---|
| A1 | 移入 archive 并加免责声明 | 现 00 | 顶部声明："本文为早期对话记录，含重复与已被推翻的提法（如 PostgreSQL），不作为规范。" |
| A2 | 去重重写 | 现 01 | 删除与 02 重复的 DDL/接口，只保留架构意图、原则、分层、目标与非目标；加稳定锚点 |
| A3 | 声明应用顺序 + 删除 PostgreSQL | 现 02 | 顶部加 core/extension 顺序声明；PostgreSQL 提法删除或标注"未来可选，v1 不用" |
| A4 | 拆分为 supplement/ 子文件 | 现 03 | 按 03 §21 自己的建议拆成 18 篇，每篇顶部标 `Status: Proposed / Accepted / Deferred` |
| A5 | 新建 README.md | 新 | 见附录 D |
| A6 | 新建 GLOSSARY.md | 新 | 见附录 A |
| A7 | 新建 CHANGELOG.md | 新 | 从本评审开始记录 |
| A8 | 新建 adr/ 并落 7 条初始决策 | 新 | §8.1 列出的 0001–0007 |
| A9 | 统一编号与锚点 | 全部 | 用稳定 slug（如 `#stable-key`、`#confidence`）替代"第 X 章" |
| A10 | 合并术语 | 全部 | Code Intelligence Layer / Project Knowledge Layer / Agent Knowledge Runtime → 统一为 **Code Knowledge Runtime** |

### 8.4 变更流程（写入 README）

```text
提出变更
  → 若是 schema / 接口 / 术语 / 已决策项的改动：先写 ADR
  → 更新唯一事实源文档
  → 更新 CHANGELOG.md
  → 更新 README.md 的状态表
  → 若影响落地计划：更新 04 的 Phase 状态与验收
```

### 8.5 与相邻计划的边界（必须写清，避免重复实现）

| 相邻计划 | 边界 |
|---|---|
| `aicli-tool-capability-convergence-plan.md` | 工具命名与优先级以该计划为准；本文只补充 `code.*` 的索引侧语义与降级协议 |
| `tool-output-artifact-cascade-audit-and-optimization-plan-20260919.md` | 工具输出归档/截断以该计划为准；本文只要求知识层复用 `internal/artifact`，不新建 blob 存储 |
| `composer-at-file-reference-workspace-search-plan.md` | 工作区搜索的 UI/交互以该计划为准；本文提供其可选的索引后端 |
| `llm-cache-analytics-unified-plan.md` | 缓存与分析口径以该计划为准；本文的 `cache_entries` 需与其 cache key 规范对齐 |
| `session-usage-analytics-and-agent-diagnostics-plan.md` | 指标埋点与展示以该计划为准；本文只新增探索归因字段 |
| `codex-compact-token-usage-observation-analysis.md` | 压缩策略以该分析为准；本文的 Observation Compressor 必须复用 `compactruntime`，不另起一套 |
| `agent-trajectory-view-implementation-plan.md` | 轨迹展示以该计划为准；本文的 `exploration_*` 表可作为其数据源之一 |


---

## 附录 A：术语表（唯一术语源，建议同步到 GLOSSARY.md）

| 规范术语（中/英） | 定义 | 禁止 / 避免的叫法 |
|---|---|---|
| 代码知识运行时 / Code Knowledge Runtime | 本方案的整体：索引 + 检索 + 规划 + 编译 + 变更同步 | Code Intelligence Layer、Project Knowledge Layer、Agent Knowledge Runtime |
| 统一代码模型 / Universal Code Model (UCM) | 跨语言的中间表示：file / symbol / ref / import / call 的最小公共结构 | 通用 AST、统一 AST |
| 语言适配器 / Language Adapter | 把某语言源码转成 UCM 的插件（builtin / tree-sitter / LSP / custom） | 语言插件、parser |
| 轻索引 / Light Index | 文件 + 顶层符号 + imports，快速可用 | fast index（易与"快速模式"混淆，可作为同义词但需在术语表登记） |
| 深索引 / Deep Index | 方法体、局部、类型关系等按需索引 | full index（易与全量重建混淆） |
| 探索记忆 / Exploration Memory | 任务内"已看过什么、结论是什么"的工作集 | 会话记忆、上下文记忆 |
| 探索图 / Exploration Graph | 探索节点与边构成的图 | 知识图谱（易与代码图混淆） |
| 上下文规划器 / Context Planner | 决定"复用还是探索" | 上下文选择器 |
| 上下文编译器 / Context Compiler | 把知识裁剪成最小上下文 | 上下文生成器、prompt builder |
| 上下文条目 / Context Item | 进入 prompt 的最小单元，带 source/version/trust/reason | context chunk（易与文本切片混淆） |
| 知识版本 / Knowledge Version | `H(workspace, file hashes, adapter versions, schema version)` | 索引版本（易与 schema 版本混淆） |
| 稳定符号键 / Stable Key | 跨重命名/移动保持稳定的符号标识 | symbol id（id 是数据库主键，两者不同） |
| 置信度 / Confidence | 见 §4.4 公式 | 分数、概率（除非真的做了概率校准） |
| 信任等级 / Trust Level | `high/medium/low/untrusted`，用于防注入 | 置信度（两者不同：trust 是来源可信性，confidence 是解析准确性） |
| 降级 / Degraded | 知识层不可用但不阻断 agent | 失败、错误 |
| 影子模式 / Shadow Mode | 计算但不返回，用于验证正确性 | 旁路模式、dry run |
| 单写者 / Single Writer | 同一 workspace 同一时刻仅一个写者进程 | 主节点、leader（除非真的做了选举） |
| 所有者 / Owner | 持有 knowledge.db 写权的进程 | 主进程、server |
| 项目记忆 / Project Memory | `memorystore` 的长期笔记（`.aicli/memory/notes.jsonl`） | 探索记忆（两者分层） |
| 事实账本 / Fact Ledger | `factledger` 的持久化事实 | 项目记忆 |
| 工具输出归档 / Tool Output Artifact | `internal/artifact` 的原始输出存储 | 工具结果表（表只存元数据） |
| 变更管理器 / Change Manager | agent edit / git diff / fsnotify 三类变更源 | 文件监听器 |
| 回退 / Fallback | 知识层不可用时回到 grep/view | 降级（降级指知识层可用但能力受限） |

---

## 附录 B：已识别的具体矛盾点（可逐条裁决）

> **注意（2026-09-20）**：本附录是**散文列表**，**不构成决策依据**。决策的唯一事实源已迁至 [`adr/`](adr/README.md)。
> 逐条转换流程见 [`adr/README.md` §8](adr/README.md)。
> 已完成映射：**B3 → ADR-0003**（口径已定、阈值待 Phase 0）；**B6 → ADR-0001**（Project/Module/Language 收敛）。

| # | 矛盾 | 位置 | 冲突内容 | 建议裁决 |
|---|---|---|---|---|
| B1 | 数据库选型 | 00 末尾 vs 02 §7/§63 | 00 提 PostgreSQL；02 全篇 SQLite | **v1 用 SQLite**；PostgreSQL 删除或标注"未来可选" |
| B2 | 第一步做什么 | 00 Phase 1 vs 01 Phase 0/1 vs 03 P0 | 探索缓存 / Telemetry / 稳定符号 ID + 类型系统 + LSP | **以本文 §5 为准**：Phase 0 基线与契约 |
| B3 | 阈值是否写死 | 01 §38 vs 03 §20.2 | "应实测" vs 固定数字 | **先基线后阈值**，§7.6 |
| B4 | DB 路径 | 02 §7 vs 仓库约定 | `.agent/harness.db` vs `.aicli/...` | **`.aicli/knowledge/knowledge.db`** |
| B5 | 代码目录 | 02 §85 vs 仓库结构 | `agent-harness/{cmd,internal}` vs `backend/{cmd,internal}` | **`backend/internal/knowledge/`** |
| B6 | 项目模型重叠 | 02 的 `language_projects` vs 03 §2 的 `projects/modules/packages` | 概念重叠，未合并 | **合并**：删 `language_projects`，用 `projects` + `modules` |
| B7 | 引用绑版本 | 02 的 `references` 绑 `symbol_id` vs 03 §1.4 规则 5 | 03 要求绑 `symbol_version` | **绑 version**，v1 必须建 `symbol_versions` |
| B8 | 运行模式 | 00 末尾（独立服务）vs 02 §6（Embedded/Local Service/Future Distributed） | 未决策 | **进程内库 + 可选 LSP 子进程**；不做独立服务 |
| B9 | 测试文件 | 仓库 `scanner.go` 忽略 `*.test.*` vs 03 §10 `TestIndex`/`code.tests` | 直接冲突，会导致测试智能永久为空 | **索引并标记 `is_test`** |
| B10 | 向量检索定位 | 00/01"不需要独立 Vector DB" vs 03 §13 FTS5 + 语义检索 | 表述张力 | **FTS5 为主，embedding 可选且默认关闭** |
| B11 | Context Compiler 落点 | 方案的独立组件 vs 仓库 `contextmgr` + `contextpack` | 会产生第二套上下文系统 | **扩展 `contextmgr`（新增 layer/模式）+ `contextpack` 新增 Provider** |
| B12 | 记忆分层 | 方案 Project Memory / Exploration Memory vs 仓库 `memorystore` / `factledger` | 三套记忆可能并存 | **三层分离**：notes（长期，人工）/ facts（事实账本）/ exploration（任务工作集，自动） |
| B13 | task 与 goal | 02 §24 的 `tasks` vs 仓库 `internal/goal` | 概念重叠 | **task 映射到 goal/turn**，不新建平行概念；`tasks` 表降级为 `exploration_sessions.task_id` 的外键引用 |
| B14 | confidence 无公式 | 01 §9/§14、02 §55、03 §9 | 只有示例值 | **补 §4.4 公式** |
| B15 | schema 应用顺序 | 02 与 03 §1.3 | 03 的 `ALTER` 依赖 02 的 `symbols`，但 02 未声明 | **02 顶部声明顺序**：core 先行，extension 必须显式应用 |
| B16 | Phase 数量与命名 | 00 六个 / 01 九个 / 02 五原则+89 节 / 03 P0-P2 | 四套优先级 | **统一为本文 §5 的 Phase 0–8**，其余文档改为引用 |
| B17 | 语言支持分级 | 01 Tier0–4 vs 03 P0 含 LSP 工程化 | 范围不一致 | **v1 只承诺 Go 一等、TS/Python/JS 二等、Java/Rust/C++ 仅文件+粗符号** |
| B18 | 事件模型落点 | 02 §33 / 03 §17 的事件清单 vs 仓库 `runtimeevents` | 会产生第二套事件总线 | **复用 `internal/events` / `runtimeevents.Publisher`**，不新建 outbox（`events_outbox` 推迟） |

---

## 附录 C：需新增 / 修改的文件清单（本仓库落点预测）

### C.1 新增（v1 范围）

| 路径 | 用途 | Phase |
|---|---|---|
| `backend/internal/knowledge/models.go` | `FileRecord`/`SymbolRecord`/`RefRecord`/`IndexStatus`/`Plan` 等 | 0 |
| `backend/internal/knowledge/config.go` | `knowledge.*` 配置解析与默认值 | 0 |
| `backend/internal/knowledge/version.go` | `knowledge_version`、`stable_key`、`confidence` 计算 | 0 |
| `backend/internal/knowledge/store.go` | `Store` 接口 | 0 |
| `backend/internal/knowledge/store_sqlite.go` | 基于 `sqliteutil.OpenFileCtx` 的实现 | 1 |
| `backend/internal/knowledge/migrations/0001_init.sql` | §4.3 的 v1 DDL | 0 |
| `backend/internal/knowledge/owner.go` | 单写者仲裁（owner.json + 心跳 + PID 校验） | 1/5 |
| `backend/internal/knowledge/indexer.go` | `Indexer` 接口与调度 | 1 |
| `backend/internal/knowledge/indexer_light.go` | 轻索引（文件 + 顶层符号 + imports） | 1 |
| `backend/internal/knowledge/indexer_deep.go` | 深索引（按需，异步可取消） | 4 |
| `backend/internal/knowledge/adapter.go` | `LanguageAdapter` SPI + 能力声明 | 4 |
| `backend/internal/knowledge/adapter_builtin.go` | 内置正则适配器（从 `workspace/scanner` 升级） | 1 |
| `backend/internal/knowledge/adapter_treesitter.go` | tree-sitter 适配器（可选） | 4 |
| `backend/internal/knowledge/adapter_lsp.go` | LSP 适配器（可选，进程外） | 4 |
| `backend/internal/knowledge/query.go` | 检索：FTS5 + 符号精确 + 图遍历 | 1/3 |
| `backend/internal/knowledge/planner.go` | `Planner`：复用/探索决策 | 2 |
| `backend/internal/knowledge/compiler.go` | `Compiler`：最小上下文生成 | 6 |
| `backend/internal/knowledge/change.go` | `ChangeManager`：三类变更源 | 5 |
| `backend/internal/knowledge/telemetry.go` | 指标埋点（写入 `usageledger`） | 0/1 |
| `backend/internal/knowledge/knowledge_test.go` | 单元测试 | 1 |
| `backend/internal/knowledge/equivalence_test.go` | 增量 vs 全量等价性测试 | 5 |
| `backend/internal/knowledge/testdata/golden/` | golden set（符号/引用标注） | 4 |
| `backend/internal/contextpack/knowledge_provider.go` | `knowledge.Provider` | 6 |
| `backend/internal/toolkit/tools/code_search.go` | `code.search` | 3 |
| `backend/internal/toolkit/tools/code_inspect.go` | `code.inspect` | 3 |
| `backend/internal/toolkit/tools/code_navigate.go` | `code.navigate` | 3 |
| `backend/internal/toolkit/tools/code_references.go` | `code.references` | 3 |
| `backend/internal/toolkit/tools/code_callers.go` | `code.callers` | 3 |
| `docs/knowledge_Layer/README.md` | 文档索引（本次已落盘） | 0 |
| `docs/knowledge_Layer/GLOSSARY.md` | 术语表（附录 A） | 0 |
| `docs/knowledge_Layer/CHANGELOG.md` | 变更日志 | 0 |
| `docs/knowledge_Layer/adr/0001..0007-*.md` | 7 条初始决策 | 0 |
| `docs/knowledge_Layer/06_implementation_index_and_guidance.md` | 方案实施索引与指引（实施入口，非设计文档） | 0 |

### C.2 修改

| 路径 | 修改点 | Phase |
|---|---|---|
| `backend/internal/workspace/scanner.go` | 测试文件改为标记而非忽略；补 Java/Rust/C++ 粗符号；输出 `content_hash`/`is_generated` | 1 |
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
| `backend/configs/model_cards.yaml` | 若涉及工具/模式提示词 | 3 |
| `docs/knowledge_Layer/01_*.md` | 去重、加锚点、去 PostgreSQL | 0 |
| `docs/knowledge_Layer/02_*.md` | 顶部声明 schema 应用顺序；删除/标注 PostgreSQL | 0 |
| `docs/knowledge_Layer/03_*.md` | 拆分为 `supplement/` | 0 |
| `docs/knowledge_Layer/00_*.md` | 移入 `archive/` 并加免责声明 | 0 |

### C.3 明确不新增（避免重复造轮子）

- 不新增 blob 存储（用 `internal/artifact`）。
- 不新增事件总线（用 `internal/events` / `runtimeevents`）。
- 不新增记忆文件格式（用 `memorystore` / `factledger`）。
- 不新增 workspace 主键（用 `workspaceregistry`）。
- 不新增压缩器（用 `compactruntime`）。
- 不新增权限模型（用 `policy` / `fsscope` / `toolbroker`）。
- 不新增 usage 账本（用 `usageledger`）。
- v1 不引入新的第三方 Go 依赖（SQLite 驱动与 FTS5 已具备；tree-sitter 推迟到 Phase 4 并单独评审）。

---

## 附录 D：README 索引（已落盘）

`docs/knowledge_Layer/README.md` 已按本附录内容落盘，包含：

1. 文档状态表（每份文档的定位、状态、事实源角色）。
2. 推荐阅读顺序（新人 / 实施者 / 评审者三条路径）。
3. 单一事实源声明。
4. 术语入口（指向 `GLOSSARY.md`）。
5. 落地进度表（Phase 0–8 状态）。
6. 与相邻计划的边界。
7. 变更流程。

---

## 附录 E：本次评审的自我限制（避免误导）

1. 本文对 00/01/02/03 的引用基于已读取的章节范围；`02` 的 90 节之后与 `03` 的部分小节未逐行读取，若有与本文结论冲突的原文，以原文为准并应回填到 §2.1 的文档级问题表。
2. 本文给出的所有阈值（如 `≤ 120s`、`≥ 30%`、`≥ 40%`）均为**初始建议值**，不是验收标准；验收标准必须按 §7.6 用 Phase 0 基线校准。
3. 本文对仓库模块的描述基于实际读取的文件；若某模块在评审后被重构，需同步更新 §3.1 的映射表。
4. 本文不构成对 00/01/02/03 作者意图的否定；四份文档在架构方向上是一致的，本文的批评集中在"可执行性、可验证性、与本仓库现状的对齐"三点。
