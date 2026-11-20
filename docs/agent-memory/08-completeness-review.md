# 方案完整性与闭环审查

> 审查日期：2026-10-22  
> 审查对象：`docs/agent-memory/` 全套方案  
> 原始审查结论：**设计层面已基本形成闭环；审查当时代码尚未实施。** 当前实施状态见 §1.1 与 §6。
> 审查后修订：`03`、`04`、`05`、`06`、`07` 已补齐关键契约；`06 §8` 已形成 M0-M5 + v1.1 的阶段 DoD。

## 0. 2026-11-19 架构收敛状态

本轮审查不是再增加一套抽取实现，而是把已有入口收敛为一条
receipt-first 正常路径：

```text
canonical turn commit
  -> durable capture receipt
  -> memory materialization
  -> phase1_extract/v2
  -> candidate + output receipt
  -> consolidation
  -> active memory
  -> retrieval/injection
```

当前代码已完成 receipt-backed 路由收敛：

- `Runtime.Run` 对 receipt-capable source 先 materialize，再运行 capture
  extraction；不再把同一 source 自动送入 legacy digest/session replay。
- `RunSession`、`RunSessionWindow`、`RunPendingTurnDigests`、
  `RecoverPendingPhase1` 保留为 pre-receipt/historical compatibility、
  migration 或 recovery；receipt-backed session 有 routing fence。
- legacy digest 查询、claim、状态更新隔离 `capture_receipt_id = ''`；
  `phase1_extract/v2` 使用 receipt 固定 range，并做 generation/incarnation、
  purge fence、lease、stale source validation。
- maintenance startup/ticker 已移除无条件 legacy digest sweep 和 host
  `RunIdle` extraction helper；startup 顺序为 materialization → capture
  recovery → legacy phase1 recovery → maintenance。
- generation disabled 仍可 materialize receipt，但不执行 LLM，也不 fallback
  到 legacy extraction。

这不是“所有历史数据已迁移”或“生产闭环已验收”：canonical SQLite 与 memory
SQLite 仍是两个 store，交接是 at-least-once；output receipt、candidate commit
和幂等重放必须继续由测试与运营证据证明。durable capture 提案文件仍保持
**方案提案（待评审，未实施）**，不能把提案内容当成已批准的 schema migration。

## 1. 审查结论先说

### 1.1 当前实际功能状态

当前仓库已有的闭环只有：

```text
手工 /memory add
  → notes.jsonl / memorystore
  → contextmgr 注入 top-k
  → Agent 使用
```

以及：

```text
会话 / 工具 observation
  → internal/memory 或 artifact/factledger
  → compact/checkpoint
```

原始审查时**没有**代码层面的自动抽取、Consolidation、summary revision、引用 usage、删除抑制和后台 job 闭环。因此：

- 当时不能说“整体功能已经完成闭环”；
- 当时应说“方案已经补齐为可实施闭环，代码尚未实现”。

2026-11-10 更新：M1 已落地 SQLite schema、`notes.jsonl` 迁移、兼容 facade、
stable/recall 注入、summary gate、pinned 预算、session mode、records/status/recall
API 和对应测试；M2 已落地 job lease/retry、bounded transcript reader、secret
redaction、结构化 candidate 写入、session-end/idle/manual 触发；M3 已落地
Consolidation、summary revision/invalidation/rebuild、usage/citation/exposure、
injection mode 与 pinned 独立预算；M4 已落地 memory tools、v2 治理 API、
import/export、Web 管理（含 job/recall trace、why-not、pin/unpin、restore/reset）
与结构化 `/memory` 扩展。M5 指标查询已落地，真实项目 shadow 评估与逐项目
上线仍待执行。

2026-11-11 补充：默认自动触发已对齐为 `idle + session_end`；新增
`TestAutomaticMemoryEndToEndWithoutManualSeed`，验证不手工写入时也能从
transcript 自动生成 candidate、Consolidation、summary 并进入召回。

同日仅使用项目层 `.aicli/runtime.yaml`（无额外 `-c` 覆盖）做 ConPTY 实测：
第一会话结束后自动生成 2 条 active fact，phase1/phase2 job 成功；第二会话的
provider request 包含 memory 内容，recall trace 为 `injected=1` 且 selected_ids
命中两条记录，usage 记录为 `cited`。

2026-11-11 追加：确认并实现 Turn Digest Outbox Phase 1 core + 基础窗口化读取作为写路径恢复补强。
turn-end 只写有界、脱敏 digest，不跑 LLM；receipt-backed 新回合由
materializer/capture worker 消费；session-end/idle/startup recovery 只保留给
无 receipt 的 pre-receipt/historical compatibility。Phase 1 仍以 canonical
transcript 为 source，不把 digest 当第二份 transcript。
大会话已接入 canonical window reader/job、per-window stale 与 L0→L1→L2 rollup；L0 retention 受 rollup 水位保护，L1 摘要通过 `turn_window` candidate 进入现有 Consolidation。已补真实 SQLite canonical store reopen、abrupt-exit 与 hard-kill E2E；真实终端关闭与 shadow 仍待补。
compaction 不改 canonical 时不再失效 canonical 派生记忆；backtrack 按 canonical cutoff/before range 与 tombstone IDs 精确失效 L0/L1/memory，legacy 事件回退 session 级保守失效。
只读最近一页时不得把更早 pending digest 标 done。详见
`patches/2026-11-11-turn-digest-runtime-outbox.md`。

2026-11-12 更新：关闭两个 P1 主链路缺口。

- startup recovery 新增独立 `TriggerStartup`，只处理 closed/archived/orphaned/stale
  session；active/idle session 留给相应 capture/legacy recovery。新增 active/idle 负向测试。
- `retention.*` 已接入 store/runtime maintenance ticker：candidate 过期、standard/ephemeral
  active 未使用过期、`maxRecords` 上限、tombstone 物理清理、audit/usage 清理与过期
  suppression 清理；默认 `retention.enabled=false`，避免升级隐式淘汰。
- `freshness.*` 已消费 per-kind TTL 与 `staleBehavior=warn`（仅低风险 kind；
  fact/reference/decision 永不禁用 warn gate）。
- `redaction.enabled/dropOnSecret` 已接入抽取、window summary、turn digest 与检索过滤。
- schema v7 为 `memory_events` / `memory_usage` 增加 `project_key`，保证 tombstone
  物理清理后仍能按项目隔离清理审计与 usage。
- citation allowlist 已合并本 turn 成功 `memory_read` 的 ID；read usage 由工具即时写入，
  最终答案只补 cited，不再重复计 read。
- `memory_recent` / `memory_why` / `memory_why_not` 已补专项测试；`why_not` 改走
  `DiagnoseInjection`，回传 dropped reasons。
- 定向测试通过：`memorystore` lifecycle、`memoryruntime` startup/freshness/redaction、
  `config` validation、`memorypolicy` mapping、`contextmgr`。

仍未关闭：M5 shadow 与真实终端关闭证据（Web/TUI parity 已在下方追加关闭）。

2026-11-12 追加：补齐 Web/CLI 控制面与 digest 观测。

- Web memory panel 已展示 `turn_digests` / `turn_windows` 计数、oldest pending age、
  pending sessions、backlog batches、recovered sessions。
- Web 已支持 L1 window purge（session/all）、session memory mode 设置、summary revision
  activate（回滚）。
- CLI 已补 `/memory session-mode <id> enabled|disabled|polluted` 与
  `/memory activate <summary-id>`；原有 `/memory mode` 与 `purge-windows` 保留。
- `memory_runtime_metrics`（schema v8）记录 recovered sessions；`memory/status` 直接读取。
- 定向验证：frontend `tsc -b`、memory panel vitest、i18n lint、Go memory/API/CLI/toolkit
  测试通过。

仍未关闭：M5 shadow、真实终端关闭证据、全量 `go test ./...` 稳定绿色。

2026-11-12 追加：摘要与召回质量。

- Consolidation 已消费 `consolidation.model`：优先 LLM Markdown 摘要，保留 deterministic
  fallback；fallback 原因写 summary metadata，不阻塞 job。
- 冲突归组从 exact text 改为 normalized subject（去标点/常见意图词、数字值占位、复数归一），
  可把 `timeout 30s` / `timeout 60s` 识别为同主题冲突。
- deterministic summary 按 kind 优先级、confidence、usage、recency 排序并去重。
- recall 新增 CJK Han bigram 分词、query hard gate、correction/decision/preference boost、
  recency/usage 指数衰减、confidence 权重和 duplicate 抑制。
- summary 预算改为“每个有内容的 kind 先保底一条，再按 kind 优先级回填”，并做最终
  渲染级预算裁剪；recall query 使用 goal + 最近 2 条 user message，多 kind 时按
  `limit/2` 做首轮多样性排序。

### 1.2 设计状态

原方案已经覆盖主流程，但审查发现以下关键点会导致“看似有方案、实际无法闭环”：

1. `off` 与现有手工 memory 语义冲突；
2. 缺少 session 级 memory mode 契约；
3. 删除/纠正后的记忆可能被重新抽取复活；
4. 缺少 revision/audit 事件，无法追溯“怎么改的”；
5. summary 没有固定输入集合，无法精确重建/回退；
6. project key 未定义稳定 canonicalization；
7. delete/reset/session mode 的授权边界不明确；
8. summary/hybrid 模式的 token 拆分未定义；
9. 用户显式“记住/忘掉/纠正”的 Agent 工具闭环缺失；
10. 缺少明确的 transcript reader 边界与触发接入点；
11. 子 agent/team 的记忆策略未定；
12. `summary` 模式与检索工具依赖未说明。

这些问题已在相关文档中修补。修补后的设计满足闭环不变量，但仍需 M0 决策和实际实现验证。

外部借鉴项已进一步按 `README §3.4` 收敛：working digest、user scope/cross-session snapshot、semantic retrieval 等不再混入 v1；v1 只保留按 kind retention、召回诊断、session context 等必要能力。

## 2. 闭环判定

### 2.1 端到端闭环链路

```text
[1] 会话产生
  → [2] 触发/入选
  → [3] 只读 transcript
  → [4] Phase 1 抽取/校验/脱敏
  → [5] candidate 落库 + source + event
  → [6] suppression 检查
  → [7] Phase 2 去重/冲突/摘要
  → [8] summary revision + summary_items
  → [9] 动态尾部注入
  → [10] 工具检索/读取
  → [11] citation/usage
  → [12] 用户纠正/遗忘
  → [13] 淘汰/导出/回滚
```

### 2.2 闭环不变量

设计必须始终满足：

1. **来源不变量**：每条 active 派生记忆至少有一个 `memory_sources` 记录；
2. **变更不变量**：每次 create/update/correct/supersede/delete 至少写一条 `memory_events`；
3. **摘要不变量**：每个 active summary revision 有 `memory_summary_items` 和 input watermark；
4. **注入不变量**：每次注入都记录 revision、memory ids、token 估算；
5. **引用不变量**：每个 cited usage 都能映射到存在的 memory id 和 session/turn；
6. **删除不变量**：删除产生 tombstone + suppression，后续检索不注入、后续抽取不复活；
7. **纠正不变量**：correction 产生新 revision，旧记录 superseded，pinned 记录不被自动覆盖；
8. **隔离不变量**：查询必须同时匹配 scope + project_key；隔离失败 fail closed；
9. **模式不变量**：`off` 下 v2 自动行为为零，手工 memory 独立保留；
10. **回滚不变量**：summary 可回到上一 revision，schema 不自动 downgrade，notes/DB 保留。

## 3. 审查发现与修补记录

| # | 发现 | 严重性 | 修补 |
| --- | --- | --- | --- |
| 1 | `off` 与手工 notes 冲突 | P1 | `03 §5.4`、`04 §6`、`05 M0/M1` 明确 `manual.enabled` 独立 |
| 2 | 无 session memory mode 契约 | P1 | `03 §5.6`、`04 §7.2`、`05 M0/M2` 增加 enabled/disabled/polluted |
| 3 | 删除后可能被重新抽取 | P0/P1 | `04 §3.3.1` 增加 `memory_suppressions`，`03 §7.2` 增加检查 |
| 4 | 缺少 revision/audit | P1 | `04 §3.2.1` 增加 `memory_events` |
| 5 | summary 不可精确重建 | P1 | `04 §3.5.1` 增加 `memory_summary_items` 和 `record_ids_json` |
| 6 | project key 不稳定 | P1 | `03 §5.5`、`07 §2.3` 定义 git origin/canonical path |
| 7 | destructive API 权限不清 | P1 | `04 §7.2` 增加管理员/本机授权要求 |
| 8 | summary/hybrid 预算不明 | P2 | `03 §8.2.1` 增加 pinned 预留与 80/20、50/20/30 拆分 |
| 9 | 缺用户显式 memory 操作闭环 | P1 | `03 §8.6`、`04 §9`、`05 M4` 增加 add/correct/forget |
| 10 | 缺 transcript reader/触发边界 | P1 | `03 §7.1` 增加只读 reader 和接入点 |
| 11 | 子 agent/team 策略不明 | P2 | `03 §5.7`、`04` 增加默认排除/无写权限 |
| 12 | summary 模式无工具依赖说明 | P2 | `03 §8.2.1`、`04 §9` 增加依赖矩阵 |
| 13 | 手工/纠正记录仍可能被自动覆盖 | P1 | `04 §3.1` 增加 `pinned`，`03 §8.6`、`05 M3`、`06` 增加测试 |
| 14 | 缺少“用户说忘掉”与自动删除的边界 | P1 | `03 §8.6` 明确 forget 默认仅用户/管理 API |
| 15 | audit/usage 表级联删除会丢失审计 | P1 | `04 §3.2.1/3.3` 取消审计表级联，物理清理前归档 |
| 16 | summary 只能切换 active，缺少显式回滚/导入 | P2 | `04 §7.2` 增加 summary activate 与 import，`06` 增加测试 |
| 17 | citation 可被模型编造，usage 可能虚增 | P1 | `03 §8.5`、`04 §10.2` 限制为本 turn 注入/读取集合，`06` 增加测试 |
| 18 | 读取使用边界不清，可能每轮都查记忆 | P2 | `03 §8.4`、`04 §10.1` 增加使用/跳过和过期验证规则 |
| 19 | summary 内容格式与 fallback 未定义 | P1 | `04 §10.1.1` 固化 Markdown 结构、长度、校验和确定性 fallback |
| 20 | hard-deleted 记录无法 restore | P2 | `04 §7.2` 明确 restore 只接受 tombstone，物理删除需导入恢复 |
| 21 | 短期/长期记忆与生命周期不够集中 | P1 | 新增 `09`：L0-L4 分层、晋升/降级/淘汰 |
| 22 | workspace 与 global 记录边界不够直观 | P1 | `09 §2/§3` 明确 workspace memory 与 user-scope global |
| 23 | 召回漏斗与 recall trace 不够具体 | P1 | `09 §5/§7`、`04 §3.3.2` 增加召回阶段与 trace |
| 24 | memory 预算与总 prompt 预算关系未公式化 | P1 | `09 §6` 增加总预算 clamp、拆分和截断顺序 |
| 25 | 缺少外部成熟实现的对照与取舍 | P2 | `10`：Nuphus、DeepSeek Harness、ai-memory 对比与借鉴优先级 |
| 26 | 外部借鉴项未明确 v1/v1.1/后续 | P1 | `README §3.4`、`05 §1.1` 固化版本范围；`04` 增加 retention_class |
| 27 | ai-memory 的 handoff / tiered brief / aging 尚未进入主设计 | P1 | 作为 v1.1 候选记录在 `10 §4/§6`，不改变 v1 范围 |
| 28 | v1.1 扩展点未在主架构/schema 中留位 | P1 | `03 §8.1` 增加扩展点；`04 §3.8` 定义 handoff、RRF trace 与不新增事实源的边界；`05 M0` 冻结“不建表/不写入” |
| 29 | 阶段验收缺 M0，M1-M5 只有定性门禁 | P1 | `06 §8` 扩为阶段 DoD：通用门禁、M0-M5 退出条件、量化门槛、v1.1 门禁和验收证据包 |
| 30 | 遗忘/更新有状态机，但缺“过时信息”契约 | P0 | `04 §3.1.1/§3.5.2` 增加 freshness 字段、TTL、summary watermark 门禁；`03 §7.5`、`09 §5.3.1`、`06/07` 增加 stale 过滤、回源验证、测试与指标 |

## 4. 按维度审查

### 4.1 功能完整性

| 维度 | 状态 | 说明 |
| --- | --- | --- |
| 手工记忆 | 已存在 | 不能因 v2 上线回归 |
| 自动抽取 | 已实现 receipt-first core | receipt-backed 新回合只走 capture worker；legacy session-end/idle/manual 仅兼容无 receipt 历史；shadow 质量待验 |
| 会话筛选 | 已实现 | `memory_session_modes` 表与 API；配置块仍缺 |
| 去重/合并 | 已实现 core | hash + source + Consolidation；语义相似仍弱 |
| 冲突/纠正 | 已实现 core | supersede + pinned + event |
| 摘要/索引 | 已实现 core | summary revision/items/gate/rebuild |
| 注入 | 已实现 | stable/recall、pinned budget、stale warn gate |
| 按需检索 | 已实现 | 工具或降级；recent/why/why-not 已有专项测试 |
| 引用/usage | 已实现 core | citation parser + usage；tool-read ID allowlist 已接入 |
| 删除/遗忘 | 已实现 | tombstone + suppression + lifecycle retention |
| 导出/恢复 | 已实现 | JSONL/Markdown + restore |
| 后台任务 | 已实现 | lease/retry/watermark + maintenance ticker |

### 4.2 数据完整性

- 每条记录有稳定 ID、scope、project_key、content hash；
- 每次变更写 event；
- summary 有 revision、input watermark、record items；
- 删除是状态变化，不是静默物理删除；
- 迁移幂等且保留原 notes；
- 读路径失败可降级。

### 4.3 安全完整性

- 记忆明确标记为历史数据；
- 不能授权工具/权限；
- secret 落库前脱敏；
- 跨项目/跨用户 fail closed；
- destructive API 需要授权；
- 自动记录默认 candidate；
- pinned/manual 不被自动覆盖。

### 4.4 运维完整性

- 有 status/jobs/audit/export；
- 有 retention 和 cleanup（candidate/active/tombstone/audit/usage/suppression）；
- 有回滚/降级；
- 有告警指标；
- 有数据库并发策略。

### 4.5 测试完整性

- 有迁移、隔离、注入、cache、抽取、Consolidation、usage、删除、权限测试；
- 已有 lifecycle、startup recovery、freshness/redaction 定向测试；
- 已有 receipt-backed capture worker、session-end routing fence、legacy recovery
  隔离、generation disabled materialization、canonical range stale validation、
  candidate/output 幂等与 maintenance schedule 定向测试；
- 有 E2E 和 Windows 7 验证计划；
- 受影响 backend 包定向测试已通过，但仍缺 M5 shadow、真实终端关闭、
  SQLite 锁压测和全量 `go test ./...` 稳定绿色证据；
- 测试全绿不等于历史 digest/session extraction rows 已迁移或积压已排空。

## 5. 仍然存在的残余风险

这些不是文档缺口，而是需要实现/实测才能关闭的风险：

1. **语义相似但 hash 不同的记忆复活**：v1 只做 hash 去重，未引入 embedding；需要 Consolidation 人工规则和用户 correction 优先。
2. **模型抽取质量不稳定**：必须通过 shadow 评估校准，不可仅靠 prompt。
3. **SQLite 在真实多进程/高负载下的锁行为**：需要实测和 health 回归。
4. **Windows 7 的 FTS5 能力**：未验证；设计已允许降级。
5. **citation block 的流式渲染/导出边界**：需要前后端联调。
6. **session mode 存 metadata 还是独立表**：按建议默认（独立表），M0 转 ADR。
7. **`summary` 模式无工具时是否默认**：按建议默认（允许，但标记 `no_retrieval_tools`），M0 转 ADR。
8. **历史 `.aicli/memory` 的 ID 兼容**：迁移实现时确认。
9. **项目移动/克隆的 project key 语义**：设计有规则，需真实路径测试。
10. **自动记忆的成本**：需要 shadow 数据决定默认触发频率和模型。
11. **源变化检测依赖 source hash/revision**：无法访问的 source 只能标记 `stale`，不能证明 `fresh`；per-kind TTL 仍需 M0 基线校准。
12. **历史双路径残留**：旧 digest、legacy phase1 job 与 receipt-backed 数据可能同时存在；当前以 routing fence、`capture_receipt_id=''` 隔离和启动 recovery 跳过保护，仍需迁移盘点与线上积压观测。
13. **跨 SQLite 崩溃窗口**：materialization 成功但 canonical receipt acknowledgement 失败时会 at-least-once 重放；必须持续验证 receipt/job/output receipt/candidate 幂等，不能假设跨库原子提交。
14. **提案与实现边界**：`2026-11-18-turn-end-durable-capture-and-async-extraction.md` 仍未评审/实施；任何 schema 或终端关闭结论都不能仅以该提案为证据。

## 6. 可实施性判断

| 阶段 | 是否可以立即开始 | 前置条件 |
| --- | --- | --- |
| M0 基线与契约 | 可以 | 按 `07 §8.1` 默认决策转 ADR；记录基线与失败矩阵 |
| M1 存储与读取 | 已实施 | M1-A/B/C 与目标测试已完成 |
| M2 抽取 | 已实施 receipt-first core + 定向测试；待 shadow 运营验收 | capture materialization/v2 worker、routing fence、legacy terminal-only recovery、job/reader/redaction/candidate/trigger 已落地；历史迁移与真实关闭证据待补 |
| M3 Consolidation | 已实施 core + 定向测试；待 shadow 运营验收 | Consolidation、summary gate/rebuild、conflict、exposure、lifecycle 已落地 |
| M4 工具/API/UI | 已实施 core；DoD 证据待补 | tools/API/import/restore/CLI、job/recall trace、Web why-not/pin/restore/reset 已落地；新增 turn digest/window 观测、L1 purge、session mode、summary activate/rollback Web/CLI 控制面 |
| M5 上线 | 部分可以 | 指标查询已落地；需要 shadow 数据和逐项目验收结果 |

## 7. 最终判断

**是否完成闭环：**

- **代码功能：M1-M4 core 已实现；M2 新回合 extraction 已收敛为 receipt-first；M4 仍需正式验收证据。** SQLite 存储、迁移、stable/recall 注入、capture/v2 抽取、Consolidation、summary、工具/API/CLI 与 Web 审计面（job/recall trace、why-not、pin/unpin、restore/reset）已落地；legacy 逻辑保留但仅用于 compatibility/recovery；M5 shadow 运营仍未完成。
- **设计方案：在本次审查修补后，已达到“可实施闭环”，但应以唯一 receipt-first 正常路径理解。** 从 receipt handoff、固定 range 抽取、存储、摘要、注入、引用、纠正、删除、迁移、回滚都有明确契约；legacy 入口不是第二条新回合路径。
- **生产可用：还没有。** 必须完成 M1-M5、历史 rows 迁移/盘点、真实终端关闭、SQLite 锁与全量测试证据及 shadow 评估，才能说生产功能闭环。

## 8. 下一步建议

1. 进入 M5 shadow 运营验收，不启用默认注入；
2. 用真实会话建立标注集，执行 M5 指标校准与逐项目上线策略；
3. 盘点并迁移/隔离历史 `capture_receipt_id=''` rows 与旧 `phase1_extract`
   jobs，验证 receipt-backed session 不再被 legacy recovery 消费；
4. 补真实终端关闭证据、跨 SQLite 崩溃重放证据、SQLite 锁压测与全量测试稳定绿色；
5. 任何默认开启前，先通过 `06-test-and-acceptance.md §8` 的对应阶段 DoD，
   重点验证单路径路由、删除复活、跨项目隔离、prompt cache 和 SQLite 锁四项
   P0/P1 门禁；不得以未评审 durable capture 提案替代实现证据。
