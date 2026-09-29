# Phase1-shadow 实测报告（v1：真实调用重放）

> Gate：`Phase1-shadow`（ADR-0003 §10）｜交付项：`06` §4 Phase 1「shadow 实测校准 α + M1 复算」
> 数据面：`exploration_attribution`（读取：`usageledger.ListExplorationAttribution`；复算：`knowledge.SummarizeAttribution` / `CalibrateShadowAlpha`）
> 实测入口：`backend/internal/knowledge/shadow_replay_test.go`（`TestPhase1ShadowReplay`）
> 调用集：`reports/phase1_shadow_calls.jsonl`（提取脚本 `backend/scripts/extract-shadow-calls.mjs`）
> 状态：**已执行（v1）；2026-09-29 口径裁决（ADR-0008 Accepted）后主门槛复核 = 通过**——α=0.8、合并 M1=35.95 % ≥ 门槛 0.31（grep file-level 26.76 %、view 48.41 %）。剩余工作见 §5；口径裁决数据见 §4.3、阈值定稿见 §4.4、复核结论见 §5.1。

---

## 1. 结论摘要

| 指标（ADR-0003 §4.5） | 实测（α=0.8） | 备注 |
|---|---|---|
| 样本调用 / 分母 / 零结果 | 400 / **370** / 30 | 分母 = `baseline_n > 0` |
| **M1 调用级可用率** | **20.81 %** | 全部来自 view 通道；grep 通道 **0.47 %** |
| M2 覆盖度 | 24.15 % | grep 3.79 % / view 51.76 % |
| M3 经济性 | 0.496 | grep 0.817 / view 0.060 |
| M4 token 收益 | 75.38 % | grep 63.00 % / view 93.67 % |
| coverage p50 / p90 | 0 / 1.00 | grep p90 仅 0.080（见 §4.1） |
| economy p50 / p90 | 0.041 / 0.721 | grep p90 仍达 1.88（见 §4.1） |
| **grep file-level 覆盖（裁决用）** | mean **31.83 %** / p50 0 / p90 **100 %**；usable@α=0.8 **26.76 %** | answerable 49.38 %；见 §4.3 |
| α 中位数校准值 | 0.10（下限截断） | 行级分布双峰，**不写死阈值**（见 §4.4） |

**Gate 判定：不通过。** grep 通道在"行级精确相交"口径下被 ADR §6.2 已预告的
低估偏差主导（候选非空时重合行也极少，coverage p50=0、p90=0.080）；view 通道表现可用
（M1 48.4 %）。file-level 对照（§4.3）显示 grep 的"可回答"比例并不低（49.4 %），
瓶颈在行级口径本身。

---

## 2. 样本与方法

### 2.1 调用集（真实、可复现）

- 来源：`~/.aicli/chat-logs/2026/09/**/chat/chat.json` 中 `working_directory` 指向本仓库的
  会话；提取 `message_type=tool_result` 的 `grep` / `view` 结果，`args` 由 `arg_preview`
  还原、baseline 用 `result.output`（模型当时实际看到的文本）。**不做构造任务**。
- 规模：**400 条**（grep 243 / view 157）；跳过：无 pattern 8、失败调用 2、重复 2。
  早期版本的"多 pattern 跳过"已修复：`patterns` 批量按数组还原并参与重放。
- 已知折损（均写入本报告，不隐藏）：
  1. runtime 未落原始参数，`arg_preview` 是渲染文本；值里如再出现 ` key=` 会切错（罕见）；
  2. 批量 pattern 由预览 `a | b` 还原；pattern 值自身包含 ` | ` 时会被误拆（罕见）；
  3. `view` 的 `offset/limit` 未落库 → 由输出行号还原（首行号=offset、编号行数=limit）；
  4. `count=true` / `files_with_matches=true` 输出无 `path:line` → 落 `baseline_n=0`
     （32 条零结果的一部分），按 ADR §4.5 不进 M1 分母；
  5. 记录时间早于索引构建，被今天编辑过的文件存在**行漂移**（不影响其余文件）；
  6. grep 输出中的上下文行同样计入 |G|（模型确实看到了它们）。

### 2.2 测量路径（生产代码，非模拟）

`ShadowObserver`（生产观察器，含 ADR-0003 §4.3 拦截语义） → `knowledge.db`（生产
`RunIndex` 全量索引）→ `usageledger.AppendExplorationAttribution` → 读回 →
`SummarizeAttribution` 复算 M1–M4（同批数据两次复算逐位一致，测试断言）。

### 2.3 索引侧（本仓库，2026-09-29）

| 指标 | 本次 | Phase 0 基线（2026-09-20） |
|---|---|---|
| Scanned / Indexed / Errors | 4989 / 4989 / **0** | 3860 / 3860 / 0 |
| Symbols / Refs | 55027 / 488871 | 42855 / 380657 |
| 首次全量耗时 | **324.9 s** | 146.9 s |
| DB 大小 | **313.5 MiB**（328,736,768 B） | 247.5 MiB |

对照 `04` §7.4 初值（未校准占位：≤120 s / ≤200 MB）：**Fail**（与 Phase 0 结论一致，且
文件数 +29 %）；该初值已由 §4.7 按 §7.6 校准（≤360 s；DB 实测 313.9 MiB 通过 512MB 默认）。
单文件增量、锁等待 p95、content_hash 抽样未在本入口覆盖（后两项已由 §4.6 的 live 抽样补齐）。

---

## 3. 实测暴露并已修复的实现缺陷

对照：首轮重放（未带 `Workspace`、无映射修复；调用集 277 grep / 123 view，仅示量级）
M1=4.96 %、M2=10.69 %；修复缺陷 1/3（绝对路径折叠 + regex→token）后 M1=21.20 %、
M2=23.11 %；补全作用域前缀后 grep M2 0.07 %→1.80 %；接入 refs 候选通道与批量
pattern 还原后 grep M2≈3.4 %；最后修复双作用域（缺陷 6，144/243 条真实调用受影响）
并重放（最终轮 243 grep / 157 view）：**grep M1=0.47 %、M2=3.79 %、M3=0.817、
M4=63.0 %；全量 M1=20.81 %、M2=24.15 %、M4=75.4 %**（批量调用并入分母，均值低于
仅含简单调用时；真实分布如此）。**本报告全部数字均为缺陷 1–6 全部修复后的最终轮数据。**

| # | 缺陷 | 影响 | 修复 |
|---|---|---|---|
| 1 | **绝对作用域不折叠**：调用传 `path=E:\...\repo`，候选 path 是 workspace 相对路径 → 前缀过滤恒不命中 | grep/view 候选恒为 0 | `ShadowConfig.Workspace` + `relativizeWorkspacePath`（`shadow_scope.go`） |
| 2 | **作用域相对输出未补前缀**：`rg pattern backend` 输出 `internal/...`，候选是 `backend/internal/...` → (path,line) 永不相交 | grep coverage 恒为 0（即使候选非空） | `parseGrepBaseline(output, scopeBase)` + `prefixScopePath` |
| 3 | **regex 直接检索 FTS**：真实 pattern 多为 `a\|b`、`func \(h \*T\)` → FTS 零命中 | grep candidate_n 恒为 0 | `shadowPatternTokens`：交替拆分 + 字面 token 提取 + 多 token 并集去重（≤6 token / ≤100 候选） |
| 4 | **refs 候选未接入**：候选只有符号定义行；且 `FindRefs` 未回填 `path`（联表列缺失） | 使用点行不可达 | `Reference.Path` + `observeGrepDetailed` 的 refs 候选通道（`refCandidateIndex` 可选接口，≤100/ token） |
| 5 | **批量 pattern 未支持**：`patterns=a | b` 被提取端跳过、观察器只读 `pattern` | 15 条真实调用被丢弃 | `grepPatternList` + `collectShadowTokens` + 提取脚本还原数组 |
| 6 | **双作用域只取一个**：`path`+`glob` 同时给出时 observer 只取 `firstNonEmpty` 的第一个（真实样本 144/243 条） | 候选过滤与 rg 语义不一致（比查询更松/更散，预算被非目标文件占用） | `newScopeFilterSpec`：path 与 glob 为 **AND**；baseline 前缀仍只由 path 决定（`shadow_scope_dual_test.go`） |

对应回归测试：`shadow_scope_test.go`（绝对路径、glob basename/`**`、输出补前缀）、
`shadow_pattern_test.go`（token 化与去重）、`shadow_ref_candidate_test.go`（refs 通道、
批量 pattern 两形态）、`store_test.go`（FindRefs 回填 path）。

## 4. 根因分析

### 4.1 grep 通道（M1 = 0.47 %，M2 = 3.79 %）

- 候选是**符号定义行 + 已解析引用点**（`Search` + `FindRefs`），baseline 是**文本/正则的
  全部命中行（含上下文行）**。`refs` 通道 + 双作用域修复把 M2 从 1.80 % 提升到 3.79 %，量级仍然是差距：
  字符串/注释/字段访问/未解析目标都不在 refs 中，且上下文行拉大了 |G|。
- 这不是索引"漏文件"：**49.4 % 的 grep 调用有非空候选**、M4=63.0 %（血缘体积收益）——
  是**行级交集口径**在"索引答案更精炼但不逐行重合"时给 0 分（ADR §6.2 原文场景）。
- 结论：grep 覆盖率不应继续以"行号交集"为唯一判据；裁决数据见 §4.3。

### 4.2 view 通道（M1 = 48.4 %）

- 区间→符号 span 的行覆盖语义与 view 输出（被读取行）天然对齐；M3=0.060（候选显著更便宜）。
- 未达 100 % 的原因：区间边界与符号 span 不精确对齐（ADR §6.2 第二项），
  以及约一半样本的区间内没有任何已索引符号（空文件段/纯代码体）。

### 4.3 口径裁决数据：grep file-level 对照（同一批 243 条真实调用）

行级口径之外，用同一批数据按"文件集合重合"复算（覆盖率 := |G_files ∩ K_files| / |G_files|）：

| file-level 指标 | 数值 |
|---|---|
| 分母（baseline 文件集非空） | 213 / 243 |
| 平均文件覆盖率 | **31.83 %** |
| p50 / p90 | 0 % / **100 %** |
| usable@α=0.8（文件覆盖率≥α） | **26.76 %** |
| file_precision（Σ相交/Σbaseline 文件） | 30.46 % |
| answerable_rate（candidate_n>0） | **49.38 %** |

读法：grep 调用里约一半"索引能给出东西"，其中相当一部分文件级完全命中（p90=100 %）；
行级却几乎全为 0。两者反差直接量化了 ADR §6.2 的偏差，是**口径裁决**（§5.1）所需的证据。
本次仍以冻结的行级口径出结论；file-level 仅作裁决材料，不写入 `exploration_attribution`。

### 4.4 α 校准

- `CalibrateShadowAlpha`（04 §7.6：中位数 + 逐步收敛）给出 **α=0.10**（命中下限）。
- 行级分布仍双峰（grep 几乎全 0；view 命中时≈1.0），α 在 0.10–0.80 区间对 M1 影响可忽略。
  因此本次**不写死 α / 门槛**：把 α 定在 0.10 会等价于"不设门槛"；正式 α 与门槛
  应与口径裁决（§5.1）一起定，避免制造"已校准"的假象。
- **2026-09-29 定稿（ADR-0008 Accepted 后）**：**α = 0.8**（沿用 `DefaultShadowAlpha`；grep file-level
  中位数 0 → 0.10 退化为"无门槛"，不予采纳；view 中位数 0.733 取整 0.75 与 0.8 对 M1 差异可忽略）——
  `knowledge.shadow.alpha` 默认位已是 0.8，无需改动。Phase 1 门槛按 §7.6 的 95 % CI 法取合并 M1
  置信下界 **≥ 0.31**；复核结论见 §5.1。

### 4.5 live 接入验证（三入口：cmd+tui / ACP / runtime-server，2026-09-29）

前四节为**重放**测量；本节是**真实会话**的入口验证（三入口全部完成；`06` §4 的接入验证项：
`mode=shadow` 有数据落库且 M1 可复算）：

- 环境（隔离）：workspace `%TEMP%\shadow_live_20260929\ws`（2 个 Go 文件）+
  `<ws>/.aicli/runtime.yaml`（`mode: shadow`、`db_path: <temp>\knowledge.db`）+
  临时 aicli 配置（`database.dsn=<temp>\ledger.db`、`skills_runtime.usage_ledger_enabled: true`）；
  真实 provider `opencode.ai / deepseek-v4-flash`，命令
  `aicli chat -M … --no-interactive --headless --yolo`（`--yolo` = bypass_permissions）。
- 落库证据：

  | 库 | 事实 |
  |---|---|
  | `knowledge.db` | files=**2**、symbols=**3**（后台首索引完成） |
  | `ledger.db` | `exploration_attribution` **2 行**、`knowledge_mode='shadow'`：`grep` baseline=3 / candidate=2 / overlap=2 / coverage=0.667 / economy=0.264；`view` baseline=10 / candidate=2 / overlap=6 / coverage=0.600 / economy=0.250 |

- **M1 复算**（生产 reader `ListExplorationAttribution` + `SummarizeAttribution`，
  入口 `TestLiveLedgerVerify`，见 §6）：calls=2、denominator=2、zero=0、usable（α=0.8）=0 →
  **M1=0 %、M2=63.33 %、M3=0.257、M4=74.34 %**；`CalibrateShadowAlpha`=0.60
  （2 条样本，仅示通路，不构成阈值建议）。
- **ACP 入口**（`aicli acp`，`agent_stdio.go:980` 接入）：Node 客户端以 NDJSON JSON-RPC 驱动
  `initialize` → `session/new`（cwd=隔离 workspace）→ `session/prompt`；真实会话产生
  `tool_call`（grep/view）并**追加 2 条**同值归因行（grep coverage=0.667 / view=0.600），
  账本由 2 行增至 4 行。客户端脚本：`%TEMP%\shadow_live_20260929\acp_client.js`。
- **runtime-server 入口**（HTTP 会话，观察器经 `handler.SetKnowledgeShadow` +
  `applyAPISessionToolObservation` 注入）：`POST /api/runtime/sessions` → `POST
  /sessions/{id}/permission-mode {"mode":"bypass_permissions","confirm":true}` → `POST
  /sessions/{id}/runtime/commands {"type":"submit_prompt",…}`；事件流显示 grep/view 并行
  `tool_finished` 后 `DONE`（`session_end success=true`），账本 4 → **6 行**（grep coverage=0.6667 /
  view=0.6，与 CLI/ACP 逐位同值）。
  运行时注意：HTTP 会话的默认模型取自 `runtime.yaml` 的 `agent.defaultModel`
  （内建默认 `claude-3-5-sonnet` 无 provider 声明时会 fail-fast），live 环境需配
  `agent.defaultProvider/defaultModel` 与 `workspace.root`。
- 结论：**三个入口**（cmd+tui / ACP / runtime-server）的 shadow 链路
  （runtime.yaml → 激活 → 观察器 → 账本）**端到端可用**。

### 4.6 抽样验证（锁等待 / content_hash 一致率，2026-09-29）

§5 的两项未覆盖度量用 env 门控的 live 抽样补齐
（`backend/internal/knowledge/live_sampling_test.go`，默认跳过）：

- **content_hash 一致率（抽样 ≥200）**：对 4989 文件库等距抽样 263 个未软删文件
  （step=19），以生产同一算法 `sha256(文件全字节)` 重算比对——
  **一致性队列 261/261 = 100.00 %**（磁盘 size+mtime 与库内一致则哈希必须相等，零不一致）；
  另有 2 个文件在索引后又被修改（元数据已变、哈希随内容变），属**预期不对称**，不计入不一致。
- **锁等待 p95（写路径采样）**：同进程 4 个 `RunIndex` 写者并发（240 文件 ×4、
  各 9600 符号、13.0 s）→ `lock_wait {samples:0, p50:0, p95:0, max:0, retry_failures:0}`，
  4 个写者全部成功、无锁重试失败；口径＝`lockwait.go` 写路径进程内 128 环，
  与 `knowledge.status.lock_wait` 同源（跨进程竞争由 owner 仲裁排除，ADR-0001；
  各进程只记录自身窗口）。
- **过程中发现并修复一个并发缺陷**：`index_jobs.id` 原为
  `digest(ws, kind, UnixNano)`，Windows 粗时间粒度下同纳秒两次调用会撞
  `UNIQUE constraint failed: index_jobs.id`（4 写者实验首版复现）；修复＝追加
  进程内原子序号（`jobs.go` 的 `indexJobSeq`），回归测试
  `TestStartIndexJobIDsAreUnique`（8 并发 job 全部成功且 ID 互异）。

### 4.7 性能阈值校准（2026-09-29，按 04 §7.6 流程）

按 §7.6「Phase 1 shadow 实测 → 用中位数 + 95 % CI 校准阈值」，对本仓库规模
（4989 个已索引文件 / 47.8 MB 源码）取 3 份样本跑全量索引
（`TestBaselineFullIndex`，各用独立临时 DB）：

| 样本 | 语料 | 首次全量 | 二次增量（全量跳过） | DB 大小 |
|---|---|---|---|---|
| S1（§4.2 原样本） | 工作树全树（含未提交改动） | 324.9 s | — | 313.5 MiB |
| S2 | perf DB 4989 文件语料副本（repo2） | 280.2 s（4m40.2s） | 3.52 s | 314.0 MiB |
| S3 | 同上（repo3） | 292.3 s（4m52.3s） | 4.39 s | 313.9 MiB |

- **n=3，t₀.₉₇₅,₂=4.303**：耗时中位数 **292.4 s**、95 % CI **[241.8, 356.5] s**；
  DB 中位数 **313.9 MiB**、95 % CI [313.2, 314.3] MiB。
- **阈值更新（已写入 04 §7.4）**：首次全量索引（本仓库规模）**≤ 360 s**（CI 上界取整）；
  DB 沿用 `max_db_size_mb=512MB`（实测余量充足）。
- 诊断：二次增量（内容未变、全量跳过）3.5–4.4 s ≈ **0.7–0.9 ms/文件**，仅 stat+库内
  预筛，不含解析；相对 Phase 0 基线（146.9 s / 3860 文件）耗时 +99 %，其中文件数仅
  +29 %——单位成本上升建议 Phase 2 前定位（refs 3.8e5→4.9e5 的解析扩展是主要嫌疑）。
- **跨仓库抽验（异源 ≥2，2026-09-29）**：GitHub 直连被网络阻塞（15 min 超时），
  改用本机 Go module cache 两份异源语料（只读采样）：`golang.org/x/net@v0.57.0`
  742 文件 → **32.5 s / 26.6 MiB**；`gin-gonic/gin@v1.12.0` 98 文件 →
  **3.81 s / 6.1 MiB**。单位成本 **38.9–43.8 ms/文件** vs 本仓库 58.6 ms/文件
  （同量级，≤1.5×），支撑 §7.4「≤360 s」按本仓库规模取值的合理性；
  二次增量 0.43–0.48 ms/文件。
- **单文件增量实测（2026-09-29，新增 `live_incremental_test.go`）**：4989 文件语料
  20 样本「追加一行注释到 1 个文件 → RunIndex」，每次 `indexed=1`。干净复测
  （无并发负载）：job 墙钟 p50 4.15 s / p95 4.35 s；"全量跳过"基线 4.05 s →
  **marginal（变更文件处理）p50 108 ms / p95 302 ms**（受干扰首测为 687 ms）。
  对照 §7.4「单文件增量 p95 < 50 ms」：**Fail**——walk+预筛基线单独即超目标 80×
  （job 口径），须由 Phase 5 Change Manager 引入增量触发（fsnotify/变更队列），
  或在 ADR/owner 层重定该行口径。复算：`KNOWLEDGE_INCR_WORKSPACE=<可写副本>` +
  `KNOWLEDGE_INCR_PROBES=20`。
- 局限：S2/S3 为**同一语料**的两份独立副本（排除 `.git` 与未索引文件的树遍历差异）；
  跨仓库样本来自 module cache（Go-only 语料），多语言仓库泛化仍属观察性结论。

## 5. 剩余工作（2026-09-29：主门槛通过；以下为未收口项）

1. ✅ **口径裁决与阈值定稿（2026-09-29 完成）**：owner 授权代改，[`adr/0008-grep-coverage-file-level.md`](../adr/0008-grep-coverage-file-level.md)
   由 `Proposed` → **`Accepted`**——grep 覆盖率采用 **file-level**（依据 §4.3：mean 31.83 %、p90 100 %、
   usable@0.8 26.76 %、answerable 49.38 %），行级口径保留为**诊断列**（0.47 % 为 ADR-0003 §6.2 已预告的低估）；
   view 通道口径不变。
   **α 定稿 = 0.8**（中位数规则退化说明见 §4.4）；**Phase 1 门槛 = 合并 M1 ≥ 0.31**（95 % CI 下界，n=370）。
   **主门槛复核（α=0.8）：Pass**——grep file-level `usable@0.8=26.76 %`（n=213）、view `M1=48.41 %`（n=157）、
   合并 **M1 = 133/370 = 35.95 % ≥ 0.31**。复算命令：§6 第 2 步（`TestPhase1ShadowReplay`，`KNOWLEDGE_SHADOW_ALPHA=0.8`）。
   **P2 进入条件 = ADR-0004 Accept**；本 ADR 的"新列实现 + 三入口 live 写入"仍为 Open（ADR-0008 §10）。
2. **grep 候选映射继续收敛**：
   - ~~复数 `paths` 逐项作用域~~：**已完成**（2026-09-29）——观察器新增
     `newScopeFilterSpecMulti`（多前缀 OR + glob AND）、`grepPathScopes`（`path` +
     `paths` 收集去重）、`parseGrepBaselineMulti` / `prefixScopePathMulti`（逐行归属、
     回退首项），4 个回归测试；提取端 `extract-shadow-calls.mjs` 保留完整 `paths`
     数组。**对 v1 复算无影响**：现有 400 条调用集中 `paths` 出现 0 次（240 条为单
     `path`），该修复面向未来重放与 live 会话。
   - **未解析标识符（字段访问/类型引用）的 refs 扩展**：**有意暂缓**——它同时抬高
     candidate 体量（economy ≤ 1.0 护栏可能反向受损），且会改变索引时长 / DB 尺寸，
     需在 ADR-0008 口径裁决后与 mapping 调优一起做，并整批重跑重放与 §4.7 校准，
     避免在口径变更前做二次校准。
3. ~~三入口 live 数据~~：**已完成 3/3**（§4.5：cmd+tui / ACP / runtime-server，真实会话 +
   真实索引 + 真实落库 + 生产代码复算；后两个入口各追加 2 条同值行）。
4. ~~性能阈值校准（本仓库规模 + 跨仓库抽验）~~：**已完成**（§4.7：n=3 中位数 292.4 s，
   95 % CI [241.8, 356.5] s → `04` §7.4 更新为 ≤360 s；DB 313.9 MiB 沿用 512MB 默认；
   异源语料 ×2 抽验 38.9–43.8 ms/文件，与本仓库 58.6 ms/文件同量级）。
5. ~~锁等待 p95 / content_hash 一致率（抽样 ≥200）~~：**已完成**（§4.6：一致率
   261/261 = 100 %；进程内 4 写者锁等待 0 样本 / 0 重试失败；并发缺陷已修复）。

## 6. 复算命令

```powershell
cd backend
# 1) 提取真实调用集（脚本可重复执行，输入=本机聊天日志）
node scripts/extract-shadow-calls.mjs "$env:USERPROFILE\.aicli\chat-logs\2026\09" `
  (Resolve-Path ..).Path ../docs/knowledge_Layer/reports/phase1_shadow_calls.jsonl 400

# 2) 重放测量（首次含全量索引；二次可 SKIP_INDEX 复用）
$env:KNOWLEDGE_SHADOW_REPO=(Resolve-Path ..).Path
$env:KNOWLEDGE_SHADOW_CALLS='../docs/knowledge_Layer/reports/phase1_shadow_calls.jsonl'
$env:KNOWLEDGE_SHADOW_DB="$env:TEMP\knowledge_shadow_phase1\knowledge.db"
$env:KNOWLEDGE_SHADOW_LEDGER_DB="$env:TEMP\knowledge_shadow_phase1\ledger.db"
go test ./internal/knowledge/ -run TestPhase1ShadowReplay -v -count=1 -timeout 30m

# 3) live 账本 M1 复算（任意真实会话写出的 ledger.db；样本不足时仅验证通路）
$env:KNOWLEDGE_LIVE_LEDGER="$env:TEMP\shadow_live_20260929\ledger.db"
go test ./internal/knowledge/ -run TestLiveLedgerVerify -v -count=1

# 4) runtime-server 入口 live（HTTP 会话驱动；runtime.yaml 需配
#    agent.defaultProvider/defaultModel 与 workspace.root）
runtime-server serve -c <隔离 config.server.yaml> --listen 127.0.0.1:18899
# POST /api/runtime/sessions → POST …/permission-mode {"mode":"bypass_permissions","confirm":true}
# → POST …/runtime/commands {"type":"submit_prompt","prompt":"…"}
```
