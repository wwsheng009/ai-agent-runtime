# Phase 0 交叉评审（相邻计划 + schema 事实源）

> 交付项：`06` §4 Phase 0 交付 5（"与 4 份相邻计划的交叉评审"，边界表见 `06` §8 / `README.md` §5）
> 日期：2026-09-20 ｜ 方式：只读比对（grep + 逐行核对），每条结论附证据
> 口径说明：`06` §4 写"4 份"，但 `06` §8 的表列了 **5 行**；本评审按 5 行全部核对，并把该计数不一致记为文档小缺陷（§4 D5）。

---

## 1. 结论摘要

| # | 相邻计划 / 事实源 | 边界判定 | 是否有冲突 | 动作 |
|---|---|---|---|---|
| P1 | `docs/plan/aicli-tool-capability-convergence-plan.md` | 工具命名/可用性以其为准 | 否（该计划全文**未出现** `code.*`/knowledge） | Phase 1 注册 `code.*` 时必须进它的 capability 面与 parity 测试（A1） |
| P2 | `docs/plan/tool-output-artifact-cascade-audit-and-optimization-plan-20260919.md` | 工具输出归档/截断以其为准，复用 `internal/artifact` | 否（Phase 0 不产出工具输出） | Phase 1 的 `code.*` 返回必须走同一截断/归档协议（A2） |
| P3 | `docs/plan/composer-at-file-reference-workspace-search-plan.md` | 工作区搜索 UI 以其为准；本方案只提供可选索引后端 | 否，**边界已被对方显式声明** | 其 P2 若立项"符号索引"，必须消费 `knowledge.db`，不得另建索引（A3） |
| P4 | `docs/plan/llm-cache-analytics-unified-plan.md` | 缓存与分析口径以其为准 | **是（命名撞车，非物理冲突）** | 修订 `06` §8 该行：两者不是同一个 `cache_entries`（A4） |
| P5 | `docs/plan/session-usage-analytics-and-agent-diagnostics-plan.md` | 指标埋点与展示以其为准；本方案只加探索归因字段 | 否（9 个字段在该计划中不存在） | 该计划需登记这 9 个字段的来源（A5） |
| S | `02` / `03`（schema 事实源） | core=`02`，extension=`03` | **是（命名漂移，跨文档）** | ADR-0001/0007 Accept 时给出对照表与"独立 schema 域"裁决（A6） |

---

## 2. 逐份评审

### 2.1 P1 `aicli-tool-capability-convergence-plan.md`

- 边界原文（`06` §8）："工具命名与优先级以其为准；本方案只补 `code.*` 的索引侧语义与降级协议"。
- 核对：`grep 'code\.|knowledge'` → **无命中**。该计划的权威面实际是**能力可用性**（`chatToolAvailable`、
  "tool surface parity tests"、"generic capability-to-MCP adapter / capability-to-function adapter"、
  "推荐优先级" §）。它约束的是"工具在 chat / MCP / function 三个表面上是否同时可用"，而不是工具命名表。
- 判定：无冲突。但"以其为准"目前是**空引用**——它还不知道 `code.*` 会存在。
- 动作 A1（Phase 1）：`code.*` 注册时必须同时进入 (a) `session/new` 的 tools 列表（`supplement/05` §"能力协商"要求不新增顶层 capability）、
  (b) 三个表面的 parity 测试，否则会出现"chat 有、MCP 无"的静默缺口。

### 2.2 P2 `tool-output-artifact-cascade-audit-and-optimization-plan-20260919.md`

- 边界原文（`06` §8）："工具输出归档 / 截断以其为准；本方案复用 `internal/artifact`"，
  并记录了 **L4 ↔ `view` 契约**：`view` 的默认窗口刻意收窄，避免 L4 按其 `ModelToolTextByteBudget`（默认 12 KiB）
  二次截断，`view` 用 `is_truncated` 声明"还有更多"。
- 核对：Phase 0 的交付物（配置项、ledger 字段、DDL、基线）都不产出模型可见文本，**不在该契约的射程内**。
- 判定：无冲突，但 Phase 1 的 `code.search` / `code.symbols` 是**新的大输出面**，必须照 `view` 的先例处理：
  自带 `offset`/`limit` 续读协议 + `is_truncated` 元数据 + 超限走 artifact 归档，而不是让 L4 静默二次截断。
- 动作 A2（Phase 1）：在 `code.*` 的工具契约里显式写明预算与截断字段。

### 2.3 P3 `composer-at-file-reference-workspace-search-plan.md`

- 边界原文（`06` §8）："工作区搜索 UI / 交互以其为准；本方案提供可选索引后端"。
- 核对（该计划自身已把边界写死）：
  - §2.2 非目标："**不建常驻全仓索引**、不引入文件系统 watcher / inotify 服务（P2 可选评估）"、
    "不做文件**内容**检索（关键字搜正文不属于本功能；grep/检索是另一能力面）"。
  - §5 P2："目录列表 TTL 缓存或**符号索引**（需先解决一致性与内存上限）"。
- 判定：**无重叠**，且对方已把"内容检索/索引"明确划给我们（"另一能力面"）。
- 动作 A3（P2 立项时）：若其 P2 要"符号索引"，只能消费 `knowledge.db`；两套索引并存会立刻产生一致性与内存双份成本。

### 2.4 P4 `llm-cache-analytics-unified-plan.md`

- 边界原文（`06` §8）："缓存与分析口径以其为准；`cache_entries` 需对齐其 cache key 规范"。
- 核对：
  - 该计划定义的是 **LLM prompt cache 代际**：`prompt_cache_epoch` / `prompt_cache_key`（`sess_...#prompt-cache-epoch-N`）/
    `prompt_fingerprint`，以及 `usage_cached_tokens` 等**事件载荷字段**；`grep 'cache_entries'` → **无命中**（它不定义任何表）。
  - 我们的 `cache_entries`（`04` §4.3 第 9 张表，已落 `migrations/0001_init.sql`）是**知识层持久缓存**：
    `cache_type ∈ retrieval|compile|summary`，键含 `knowledge_version`，与 prompt cache 无关。
- 判定：**命名撞车**。`06` §8 那句"对齐其 cache key 规范"**没有可对齐的对象**（对方不定义表/键格式），
  照字面执行会把知识层缓存错绑到 prompt cache 的代际语义上。
- 动作 A4（Phase 0 内可做）：改写 `06` §8 该行，明确二者是不同概念、不同 DB、不同失效规则。

### 2.5 P5 `session-usage-analytics-and-agent-diagnostics-plan.md`

- 边界原文（`06` §8）："指标埋点与展示以其为准；本方案只新增探索归因字段"。
- 核对：`grep 'exploration_tokens|reuse_tokens|index_lookup_count|index_hit|fallback_count|unsafe_reuse_count|tool_calls_per_task|repeated_read_count|knowledge_version_mismatch'`
  → **无命中**。即这 9 个字段在该计划中尚不存在，不存在命名冲突；但"以其为准"要求**字段的展示口径**由它定义。
- 判定：无冲突。我们已是这 9 个字段的**事实源**（`entity.TokenUsageHistory` + `usageledger` 幂等补列）。
- 动作 A5（Phase 0/1 交界）：在该计划登记"探索归因字段由 knowledge 层产出、命名与单位以 `entity.TokenUsageHistory` 为准"。

---

## 3. schema 事实源交叉核对（`02` core / `03` extension / `04` §4.3 / 实现）

`04` C2 已裁决 knowledge 使用**独立 DB 文件**（`<workspace>/.aicli/knowledge/knowledge.db`），
C3 裁决 `02`=core、`03`=extension 双文件。因此下列差异**不是物理冲突**，但 ADR-0001 D6 / ADR-0007
要求"清单与 DDL 一致且机械可检验"，必须显式裁决。

| 概念 | `02` core | `03` extension | `04` §4.3 = 我们的 `0001_init.sql` | 判定 |
|---|---|---|---|---|
| 引用 | `references`（`source_symbol_id`/`target_symbol_id`） | — | `refs`（`from_symbol_id`/`to_symbol_id`） | 改名，需对照表 |
| 符号全文检索 | `symbol_fts`（L2159） | `symbol_fts`（L1206） | `symbols_fts`（列集收窄为 name/qualified_name/signature） | **一名两写**，需裁决唯一名 |
| 缓存 | `cache_entries(cache_key PK, cache_type, value, version, dependencies_json)` | — | `cache_entries(id PK, workspace_id, cache_key, cache_type, payload_json, knowledge_version, …)` | 同名不同形，见 A4 |
| 符号别名 | — | `symbol_aliases(+valid_from_version/valid_to_version)` | `symbol_aliases`（无版本区间） | 列集差异，Phase 2 需要版本区间时补 |
| 符号稳定键 | — | `ALTER symbols ADD stable_key` + `idx_symbols_stable_key` | 直接建在表内 + `idx_symbols_stable` | 索引名不同（`stable` vs `stable_key`） |
| `index_jobs` | 幽灵名（无 DDL） | — | 有 DDL（`04` §4.3 越位，文件内已注明待迁） | ADR-0007 已裁决：迁 extension |

**动作 A6（Phase 1 硬门禁）**：ADR-0001 / ADR-0007 都标 `Phase1-start`，Accept 时必须同时给出：
(a) "knowledge.db 是独立 schema 域，`04` §4.3 是其唯一事实源；`02` 的名字不自动适用于它"的裁决；
(b) 上表的逐行对照（哪些是**有意简化**、哪些要改名统一）；
(c) ADR-0007 要求的机械校验（清单 ↔ DDL 一致）脚本落点。

---

## 4. 动作项汇总

| 编号 | 动作 | 何时 | 归属 |
|---|---|---|---|
| A1 | `code.*` 进入三表面（chat/MCP/function）与 parity 测试 | Phase 1 | 工具面 |
| A2 | `code.*` 输出的预算/截断/artifact 契约 | Phase 1 | 工具面 |
| A3 | composer P2"符号索引"只能消费 `knowledge.db` | P2 立项时 | 前端 + 索引 |
| A4 | 改写 `06` §8 的 `cache_entries` 一行，消除命名撞车 | **Phase 0 内**（已做，见 `06` §8） | 文档 |
| A5 | 9 个归因字段在 session-usage 计划登记来源与口径 | Phase 0/1 交界 | 文档 + 埋点 |
| A6 | `02`/`03`/`04`/实现 的 schema 对照裁决 + 机械校验 | Phase 1 硬门禁 | ADR-0001 / 0007 |
| D5 | `06` §4 写"4 份"、§8 列 5 行，计数不一致 | Phase 0 内（已修） | 文档 |
