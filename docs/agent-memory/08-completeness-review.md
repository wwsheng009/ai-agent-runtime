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


## 本轮 Memory ConPTY turn identity 进展（2026-11-20）

CLI actor 提交已补充 authoritative turn identity 回调：先传入 provisional ID，再由 actor 在 durable suspension 查找后返回最终 ID；若该值取代 provisional ID，仅重键末尾同内容 user 行，避免重复 transcript 行，并使 capture receipt 的 turn membership 检查可命中。actor 单测覆盖 durable suspension 覆盖 provisional ID，CLI 临时 SQLite 测试覆盖持久化重键与幂等。相关针对性 Go 测试通过，具体记录见 durable-capture 提案 §19。

该改动尚未通过真实 Memory ConPTY E2E；provider/auth 与跨会话召回没有验证，hard-kill、跨 SQLite 崩溃恢复、性能/并发门禁及 shadow 运营验证继续未完成。`/memory status runtime=ready`、HTTP 200 或代码单测都不作为 capture 成功或生产验收证据。

## 本轮增量记录：active-session hard-kill、generation 审计与锁统计（2026-11-20）

本记录只追加验证证据，不改变本文对生产状态的判断，也不把未评审的 durable-capture 提案视为已验收。

### 已新增并通过的 active-session 故障覆盖

`backend/internal/memoryruntime/e2e_sqlite_test.go` 新增
`TestE2ESQLiteActiveCaptureHardKillRecovery`。child 进程保持
`chat.StateActive`，连续通过真实 `SaveWithTurnCapture` 与
`UpdateWithTurnCapture` 写入两轮；canonical session 与 capture receipt 在同一
canonical SQLite 事务提交。父进程 hard-kill 后重新打开 canonical/memory 两个
临时 SQLite，验证 active 状态、4 条 canonical message、两个 receipt 的
generation/revision/range hash、receipt 校验、`1-2`/`3-4` ranges，以及
materialization/extraction recovery 的幂等性（第二次 recovery 不增加 extractor
调用）。

本轮证据：

- `TestE2ESQLiteActiveCaptureHardKillRecovery`：通过；
- 与既有 `TestE2ESQLiteHardKillRecovery`、`TestE2ESQLiteCanonicalRestartRecovery`
  一起定向运行：通过；
- 测试只使用临时目录，未修改真实 SQLite 实例数据或 suspension 状态。

这关闭了“hard-kill 前先把 session 改为 closed”的测试缺口，但不等于关闭了
所有跨 SQLite 故障边界；receipt materialization、receipt ack、candidate/output
receipt、purge 各阶段的完整 hard-kill 矩阵仍待执行。

### generation / purge 审计结果

现有 generation-qualified 路径已核对：

- receipt、capture materialization、turn digest/job、candidate provenance 和
  output receipt 使用正的 `session_generation`；generation 不匹配时跳过或
  fail closed；
- `PurgeSessionWithGenerations` 在 memory SQLite 单事务写 tombstone，并对
  entries/digests/jobs/windows 等带 incarnation 的数据使用精确 generation；
  已完成 purge 的旧 incarnation 重试不会按 session ID 清理新 incarnation；
- claim/complete/fail/candidate commit 会再次检查 purge fence；没有可证明
  incarnation 的 generationless legacy job 在 fence 后不可继续；
- 相关定向测试（purge 幂等、新 incarnation 隔离、claimed commit fence、
  candidate writer fence、generation binding、legacy job fence）均通过。

这次审计没有证据要求扩大 session-ID 级删除或修改 purge 实现。仍未关闭的边界
包括 generationless/混合代数 window、window 状态转换的完整 generation fence、
未版本化 recall trace/session mode，以及跨 SQLite purge 崩溃矩阵；不得把这些
数据混代读取或把 generationless 数据当成严格 purge 证据。

### AppendEvents 锁计时零值根因与修复

此前 `TestAppendEventsLockHoldBudget` 在 `Batches == 1` 的同时偶发
`BatchLockHoldNs == 0`。根因已复现：Windows 上 `time.Now()` 携带的 monotonic
clock 分辨率可能粗于 1ns，极短的成功事务在两个相同 clock tick 之间完成，
因此 `time.Since(txStart).Nanoseconds()` 返回 0；批次计数在事务返回后仍会递增，
于是出现“批次存在但锁耗时样本为零”的表象。独立 Go micro-probe 在本机
1,000,000 次 `time.Now(); time.Since(start)` 中观察到大量零值。

实现保留性能预算和断言不变，新增共享的 `elapsedNanoseconds` 计时转换：成功
操作若被平台时钟量化为零，只记录 1ns 下限；单条/批量总耗时和锁持有耗时均使用
该转换，不能把已完成操作与“没有样本”混淆。没有放宽 500ms 上限，也没有改变
SQLite 写事务、锁范围或重试策略。`TestAppendEventsLockHoldBudget -count=50`
现已全部通过（其中少数样本显示为 1ns，说明正是时钟量化路径）。

### 本轮定向与包级验证

- `go test ./internal/memoryruntime ./internal/memorystore`：通过；
- `go test ./internal/chat`：通过；
- active hard-kill、canonical restart、purge/generation 和锁预算定向测试：通过。

仍未完成：真实 Memory ConPTY/provider/auth 与跨会话召回、跨 SQLite 全交接步骤的
故障注入矩阵、全量性能/并发门禁、历史 legacy backlog 盘点以及 M5 shadow 运营
验收。因此本轮不宣称生产验收通过；`go test` 的局部绿色也不能替代上述证据。


### 本轮增量记录：memory materialization/receipt ack hard-kill 窗口（2026-11-18）

新增 `TestE2ESQLiteMaterializationAckHardKillRecovery`，在独立临时 workspace 中使用两份真实 SQLite 文件：child 先以 `SaveWithTurnCapture` 原子提交 active canonical session 与 receipt，再提交 memory 侧 digest/job 事务，随后在 canonical receipt ack 前由 parent hard-kill。重启后验证 receipt 仍为 `captured`、digest/job 仍为同一稳定 ID 且只有一份 pending job；materializer 重试完成 canonical ack，不新建 digest/job，worker 仅抽取一次并将 candidate/job 置为终态。再次 materialize/recovery 不增加 extractor 调用。

本轮定向证据：

- `go test ./internal/memoryruntime -run 'TestE2ESQLite(MaterializationAckHardKillRecovery|ActiveCaptureHardKillRecovery|CrashRecovery)$' -count=20`：通过；
- `go test ./internal/memoryruntime -count=1`：通过；
- `go test ./internal/memorystore -count=1`：通过；
- `go test ./internal/chat -run 'TestAppendEventsLockHoldBudget|Test.*TurnCapture|Test.*Generation' -count=5`：通过。

该测试只使用 `t.TempDir()`，没有读写真实 SQLite 实例或 suspension 状态。它关闭的是“memory 事务已提交、canonical ack 尚未提交”的测试缺口；仍未关闭跨 SQLite purge/worker output receipt 的完整 hard-kill 矩阵、真实终端/ConPTY、全量性能并发门禁及 M5 shadow 运营验收，因此本轮仍不宣称生产验收通过。


### 全量复核补充（2026-11-18）

尝试运行 `go test ./...` 后，受当前工作树其他改动影响仍未全绿；本轮没有为避免扩大范围而修改这些非 memory handoff 文件。当前可复现的阻塞包括：

- `cmd/aicli/ui/renderengine` 测试编译失败：`blankGrid` 在 `screen_model.go` 与 `screen_model_compat_test.go` 重复定义；
- `cmd/contractgen` 的 `TestGeneratedFileIsUpToDate`：frontend `event-contract.ts` 与 events 注册表不一致；
- `internal/events` 的 `TestEmitRuntimeEventLiteralsAreRegistered`：`tool_loop.child_results_consumption_failed` 尚未登记。

`cmd/scheduled` 与 `internal/agent` 的相应定向测试复跑通过，但不能抵消全量门禁未通过的事实。上述问题与本轮新增的跨 SQLite handoff 测试无直接代码路径关系，仍需在独立变更中收敛后才能重新评估全量验收。


## 本轮增量记录：跨 SQLite session purge hard-kill 恢复矩阵（2026-11-18）

本轮仅追加临时 SQLite 故障注入证据，不改变本文对生产状态的判断，也不把 durable-capture 提案视为已批准或已验收。

`backend/internal/memoryruntime/e2e_sqlite_test.go` 新增
`TestE2ESQLiteSessionPurgeHardKillRecovery`，child 在同一临时 workspace 中创建
canonical session（带正 `session_generation`）和 generation-qualified memory
candidate，然后分别在以下持久化边界写 marker 并等待 parent hard-kill：

- canonical `BeginSessionPurge` fence/journal 提交后、memory tombstone 提交前；
- memory purge/tombstone 单事务提交后、canonical memory ack 前；
- canonical memory ack 提交后、canonical complete/delete 前。

parent 重开两份 SQLite 后先检查边界状态，再通过真实
`SessionManager.ResumePendingSessionPurges` 和 V2 generation-qualified coordinator
恢复。恢复验证包括：目标 incarnation 的 candidate 精确删除、memory tombstone
`purged` 及 generation/purge generation 一致、canonical session 删除、journal
进入 `complete`，以及第二次 recovery 返回零并不重复写入。marker 只包含 session ID、
generation、purge generation 和阶段，不含 transcript、凭证或上游响应。

本轮证据：

- `go test ./internal/memoryruntime -run TestE2ESQLiteSessionPurgeHardKillRecovery -count=1`：通过；
- `go test ./internal/chat ./internal/memorystore ./internal/memoryruntime`：通过；
- `git diff --check`：通过。

测试只使用 `t.TempDir()`，没有修改真实 SQLite 实例数据或 suspension 状态。该矩阵
关闭了当前实现中 canonical fence、memory purge、canonical ack/complete 之间的
基本 restart recovery 覆盖缺口，但不等于跨 SQLite 并发注入、真实 Memory ConPTY/
provider/auth、跨会话召回、性能/并发门禁或 shadow 运营验收完成。因此本轮仍不宣称
生产验收通过；generationless/混代派生状态等既有边界仍按前述风险处理。
## 本轮增量记录：turn window generation/source fence（2026-11-18）

> 本节只追加实现与验证证据，不改变本文对生产状态的判断，也不把 durable-capture 提案视为已批准或已验收。

本轮补入并接入 generation-qualified turn window 状态转换：

- `EnsureTurnWindowForGeneration` 要求正 `session_generation` 与非空
  `source_hash`；`CompleteTurnWindowForGeneration`、`FailTurnWindowForGeneration`
  和 `MarkTurnWindowProcessingForGeneration` 只按
  `id + session_generation + source_hash + 预期状态` 条件更新，条件不匹配时返回
  `sql.ErrNoRows`，旧 worker 不能覆盖新 source 或新 incarnation 的状态。
- canonical rollup 路径已全部切换到上述 API；legacy
  `EnsureTurnWindow/CompleteTurnWindow/FailTurnWindow/MarkTurnWindowProcessing`
  仅保留给 migration/diagnostic 与测试调用，不再作为 canonical rollup 路径。
- 正 generation 但 `source_hash` 为空的既有 window 不能证明摘要来源；再次通过
  generation-qualified 入口刷新时按 source changed 处理，不复用旧 summary
  正文/状态作为当前 source 的已验证输出。
- 未增加 session-ID 级 purge/fence；generationless 或混合代数 window 仍不能作为
  严格 purge 完成证据，也不把不同 incarnation 的数据混读。

本轮验证（仅使用 `t.TempDir()` 临时 SQLite，未读写真实 SQLite 实例或 suspension
状态）：

```text
go vet ./internal/memoryruntime ./internal/memorystore
go test -run '^$' ./internal/memoryruntime ./internal/memorystore
go test ./internal/memorystore -run '^TestGenerationQualifiedTurnWindowStateTransitionsFenceStaleWorker$' -count=1
go test ./internal/memoryruntime -run '^(TestRunSessionEndRollsUpL1AndL2|TestRunSessionEndKeepsL0WhenWindowSummaryFails|TestRecoveryRollupOnlyWorkSurvivesFailureAndRestart|TestRecoveryIdleRunRepairsRollupOnlyWork|TestRollupCancellationPersistsFailureAndRecoversWithoutExtraction)$' -count=1
go test -race ./internal/memoryruntime -run '^TestSessionEndPipelineUsesIndependentBoundedPhaseBudgets$' -count=1
go test ./internal/memoryruntime ./internal/memorystore -count=1
```

以上命令均通过。此前该 race 用例的失败来自测试预算过紧（750ms/900ms 在 race
instrumentation 下不足），本轮只调整测试超时为 10s 与相应断言，未修改生产阶段
预算实现；race 用例已复跑通过。

依赖包编译检查
`go test -run '^$' ./internal/chat ./internal/memoryassembly ./internal/api/runtimeapi ./cmd/aicli/commands`
通过，未执行这些包的测试逻辑。

仍未完成：跨 SQLite 全交接步骤的 hard-kill/并发故障矩阵、真实 Memory ConPTY/
provider/auth 与跨会话召回、全量性能/并发门禁、legacy backlog 盘点和 M5 shadow
运营验收。因此本轮仍不宣称生产验收通过；局部测试通过不能替代上述证据。
### 本轮增量记录：capture worker claim/commit hard-kill 边界（2026-11-18）

> 本节只追加实现与验证证据，不改变本文对生产状态的判断，也不把 durable-capture 提案视为已批准或已验收。

`backend/internal/memoryruntime/e2e_sqlite_test.go` 新增
`TestE2ESQLiteCaptureWorkerHardKillRecovery`，使用 `t.TempDir()` 下两份真实
SQLite（canonical `sessions.sqlite` 与 memory SQLite），覆盖两个此前缺失的
worker 重启边界：

- `worker_claimed`：child 原子提交 canonical turn/receipt，物化 memory
  digest/job 并 ack receipt，随后只 claim job（45s lease）便写 marker 并等待
  hard-kill。parent 重开后确认 digest 仍 pending、job 仍 running、candidate/
  output receipt 均为 0；通过注入 worker 时钟越过 lease，复用同一 job ID 重新
  claim，完成一次抽取与原子 commit，最终为 done/succeeded、candidate=1、
  output receipt=1；再次 recovery 不增加 extractor 调用。
- `worker_committed`：child 完成同一 worker 的原子
  candidate/output-receipt/job/digest commit 后写 marker 并被 hard-kill。
  parent 重开后确认 commit 已可见；recovery 返回 `Processed=0`，extractor
  调用为 0，candidate/output receipt 均保持 1，不重复抽取或写入。

marker 仅保存 session/receipt/digest/job ID、generation 与阶段，不保存
transcript、候选正文、凭证、Authorization header、完整上游响应或 secret ref。
测试只使用临时目录，没有修改真实 SQLite 实例数据或 suspension 状态。

本轮证据：

```text
go test ./internal/memoryruntime -run '^TestE2ESQLiteCaptureWorkerHardKillRecovery$' -count=1 -v
go test ./internal/memoryruntime -run '^TestE2ESQLiteCaptureWorkerHardKillRecovery$' -count=10
go test -race ./internal/memoryruntime -run '^TestE2ESQLiteCaptureWorkerHardKillRecovery$' -count=1 -v
go test ./internal/memoryruntime ./internal/memorystore -count=1
go vet ./internal/memoryruntime
```

以上命令均通过。该证据关闭 claim 后未提交与原子 commit 后重启两个边界，但不覆盖
extractor/provider 调用过程中断、跨 SQLite 并发 purge 与 worker commit 的全组合、
分布式原子性、真实 Memory ConPTY/provider/auth、跨会话召回、全量性能/并发门禁、
legacy backlog 盘点或 M5 shadow 运营验收；本轮仍不宣称生产验收通过。
### 本轮增量记录：turn window purge fence（2026-11-18）

> 本节只追加实现与验证证据，不改变本文对生产状态的判断，也不把 durable-capture 提案视为已批准或已验收。

补齐 `memory_turn_windows` 写入的 purge fence。此前 window 已具备
generation/source-hash 条件更新，但迟到 worker 仍可能在 memory session purge
提交后创建或刷新 window。本轮在同一个 memory SQLite 写事务内加入 tombstone 校验：

- `EnsureTurnWindow` / `EnsureTurnWindowForGeneration` 在查询或插入 window 前检查
  `memory_session_purges`，被 fence 的 incarnation 不能创建/刷新 window。
- `CompleteTurnWindow`、`FailTurnWindow`、`MarkTurnWindowProcessing` 及其
  generation-qualified 版本先解析 window 存储的 `project_key/session_id/
  session_generation`，再执行同一 fence 检查后更新。
- 正 generation 使用严格 `generation <= fenced_session_generation` 拒绝；
  `state=fenced` 时一律拒绝，只有 tombstone 已完成且 generation 更大时才允许新
  incarnation 继续。
- generationless window 无法证明 incarnation，存在任何 session tombstone 后即被
  拒绝。没有新增 session-ID 级 purge/fence，也没有按可复用 session ID 清理新的
  incarnation。

新增 `TestTurnWindowWritesRespectSessionPurgeFence`，覆盖：purged generation 的
迟到创建、generationless ensure/complete/fail/mark 拒绝、legacy summary 不被迟到
写入覆盖，以及 generation 2 在 generation 1 tombstone 后正常创建和完成。

验证结果：

```text
go test ./internal/memorystore -run '^(TestTurnWindowWritesRespectSessionPurgeFence|TestGenerationQualifiedTurnWindowStateTransitionsFenceStaleWorker|TestPurgeSessionIsIdempotentAndAllowsNewIncarnation)$' -count=1
go test -race ./internal/memorystore -run '^(TestTurnWindowWritesRespectSessionPurgeFence|TestGenerationQualifiedTurnWindowStateTransitionsFenceStaleWorker)$' -count=1
go test ./internal/memorystore ./internal/memoryruntime -count=1
go test -run '^$' ./internal/chat ./internal/memoryassembly ./internal/api/runtimeapi ./cmd/aicli/commands -count=1
go vet ./internal/memorystore
```

以上均通过；测试只使用 `t.TempDir()` 临时 SQLite，未修改真实 SQLite 实例数据或
suspension 状态。该增量关闭 canonical turn-window 写入的 purge tombstone 缺口，
但仍不覆盖 generationless 历史窗口迁移、跨 SQLite 并发 purge 全组合、真实
Memory ConPTY/provider/auth、跨会话召回、全量性能/并发门禁、legacy backlog 盘点
或 M5 shadow 运营验收，因此仍不宣称生产验收通过。
### 本轮增量记录：recall candidate lookup O(n²) 消除（2026-11-18）

> 本节只追加实现与验证证据，不改变本文对生产状态的判断，也不把局部分项基准写成完整性能门禁。

retrieval 在候选排序后分别执行 kind diversification 和 token-budget loop；
此前两处都通过 `recordByID(records, id)` 逐候选线性扫描完整 records slice，
候选数增长时形成 O(candidates × records) 的重复查找。本轮改为一次构建
`map[string]int`（record ID → slice index），两处共用 O(1) 查找：

- 不再复制完整 `Record` 到索引，减少内存和分配；
- diversification 的 kind 顺序与预算选择语义不变；
- 查询无匹配候选时仍走原有 hard gate，不改变过滤条件或 trace 行为；
- 没有新增 SQL、索引或 schema，不改变持久化状态。

新增 `BenchmarkDiversifyRecallOrderLookup`，在本机 Windows/AMD Ryzen 7 5800H
上以 2048 records/candidates、limit=10 对比索引实现与旧线性查找形态：

```text
indexed-16     2284    255039 ns/op    600738 B/op    11 allocs/op
linear-16        21  29138510 ns/op    491525 B/op     2 allocs/op
```

即该热点约 114× faster；这是微基准，不是端到端 retrieval p95 或生产容量结论。

验证结果：

```text
go test ./internal/memorystore -run '^(TestSelectForInjectV2DropsNearDuplicateRecallRows|TestSelectForInjectV2DiversifiesRecallKinds|TestMemoryStorePinnedBudgetSurvivesSummaryFull)$' -count=1
go test -race ./internal/memorystore -run '^(TestSelectForInjectV2DropsNearDuplicateRecallRows|TestSelectForInjectV2DiversifiesRecallKinds)$' -count=1
go test ./internal/memorystore ./internal/memoryruntime -count=1
go vet ./internal/memorystore
```

以上均通过；测试只使用 `t.TempDir()` 临时 SQLite，未修改真实 SQLite 实例数据或
suspension 状态。完整 retrieval p95、并发/长读 SQLite 锁门禁、真实 Memory
ConPTY/provider/auth、跨会话召回、legacy backlog 盘点和 M5 shadow 运营验收仍未
完成，因此本轮仍不宣称生产验收通过。
### 本轮增量记录：model-call job lease heartbeat（2026-11-18）

> 本节只追加实现与验证证据，不改变本文对生产状态的判断，也不把局部测试写成生产验收。

此前 job 只在 claim 时写入 `lease_until/deadline_at`，没有运行期续租。默认
5 分钟 lease 下，blocking provider/摘要调用可能超过 lease：worker 的最终 commit
会被拒绝，恢复 worker 也可能在旧调用仍运行时重新 claim，造成重复模型调用。
本轮补齐 owner/attempt-fenced heartbeat：

- 新增 `Store.HeartbeatJobForWorkerAttempt`，只允许当前
  `job_id + project_key + running + worker_id + attempt` 且原 lease 尚未过期时续租；
  同时更新 `lease_until/deadline_at/heartbeat_at`。迟到或已 reassign 的 heartbeat
  返回 `ErrJobLeaseLost`，不会复活过期 claim。
- heartbeat 事务继续校验 project maintenance lease 与 session purge fence。
- 新增 `withJobLeaseHeartbeat`：调用期间按 `lease/3` 续租，单次 heartbeat 超时 5s；
  续租失败通过 cancel cause 取消模型 context，调用方在 commit 前 fail closed。
- 已接入 receipt-backed phase1 worker、legacy session/window extraction、
  byte-page extraction、window rollup、phase2 consolidation 与 stale-summary rebuild。
  phase2 claim 现在显式使用配置 Lease，与 heartbeat 周期一致。
- `TestJobHeartbeatExtendsLiveLeaseAndRejectsStaleAttempt` 覆盖成功续租、错误
  worker/attempt 与过期 lease 拒绝；`TestWithJobLeaseHeartbeatCancelsWhenClaimIsReassigned`
  覆盖 reassign 后取消模型调用；`TestRunPendingTurnCaptureReceiptsHeartbeatsSlowExtractionLease`
  以 500ms 初始 lease 运行 1.2s extractor，验证仍能原子提交。

验证结果：

```text
go test ./internal/memorystore ./internal/memoryruntime -count=1
go test -race ./internal/memorystore -run '^TestJobHeartbeatExtendsLiveLeaseAndRejectsStaleAttempt$' -count=1
go test -race ./internal/memoryruntime -run '^(TestWithJobLeaseHeartbeatCancelsWhenClaimIsReassigned|TestRunPendingTurnCaptureReceiptsHeartbeatsSlowExtractionLease)$' -count=1
go vet ./internal/memorystore ./internal/memoryruntime
```

以上均通过；测试只使用 `t.TempDir()` 临时 SQLite，未修改真实 SQLite 实例数据或
suspension 状态。该增量仍不覆盖真实 provider 长时阻断、网络层半开连接、跨进程
reassign 压测、性能/并发门禁、Memory ConPTY/provider/auth、跨会话召回、legacy
backlog 盘点和 M5 shadow 运营验收，因此不宣称生产验收通过。
### 本轮增量记录：retrieval 评分与扫描热路径优化（2026-11-18）

> 本节只追加实现、测试与本地基准证据，不改变本文对生产状态的判断；合成基准不等同于线上 p95。

本轮新增 `BenchmarkSelectForInjectV2LargeActiveSet`，先暴露完整
`SelectForInjectV2` 在 512/2048 个全匹配 active records 下的真实成本，再逐项消除：

- selection 内部候选不再构造完整 `SearchHit/Note`（避免为每个候选复制 text/tags），
  改为 `recordIndex + score`，排序、kind diversification 与预算循环直接访问同一
  records slice；对应微基准从旧线性查找形态 ~27.9ms 降到 ~0.055ms。
- `recallDedupKey` 改为单次 builder 规范化，省去重复 ToLower、Fields/Join 及
  中间字符串。
- `memoryredact.DetectSecret/Redact` 增加保守的 secret 关键词 fast reject；只有可能
  命中时才执行 9 条权威正则。新增覆盖全部 9 条规则和 benign text 的表驱动测试，
  不改变正则最终判定。
- 非诊断 injection 使用 13 列轻量投影扫描；入选 Stable/Recall 记录再按 ID 批量
  补全为完整 Record。补全时保留预算截断后的 Text，避免重新暴露超预算正文。
  诊断路径和 GetRecord/详情路径继续读取完整记录。

同一机器、2048 全匹配 records、limit=10、TokenBudget=600：

```text
优化前  512: 10.03ms/op, 2.81MB/op, 37003 allocs/op
优化前 2048: 40.06ms/op, 13.27MB/op, 147661 allocs/op

优化后  512:  3.76ms/op, 1.56MB/op, 13131 allocs/op
优化后 2048: 15.00ms/op, 8.21MB/op, 50016 allocs/op
```

该合成场景的 2048 路径已低于文档暂定的本地检索 20ms M0 门槛，但仍不是端到端
prompt/injection p95，也不是多进程/真实 SQLite 压测结论。

验证通过：

```text
go test ./internal/memorystore ./internal/memoryruntime ./internal/memoryredact -count=1
go test -race ./internal/memorystore -run '^(TestSelectForInjectV2HydratesSelectedRecordAndPreservesFittedText|TestSelectForInjectV2NoMatchKeepsEmptySelection|TestSelectForInjectV2DropsNearDuplicateRecallRows|TestSelectForInjectV2DiversifiesRecallKinds)$' -count=1
go test -race ./internal/memoryredact -count=1
go vet ./internal/memorystore
```

测试只使用 `t.TempDir()` 临时 SQLite；profile artifact 已清理，未修改真实 SQLite
实例或 suspension 状态。完整 SQLite 并发/长读性能门禁、真实 provider/ConPTY、
跨会话召回、legacy backlog 盘点和 M5 shadow 运营验收仍未完成，因此仍不宣称
生产验收通过。
### 本轮增量记录：全仓 Go 测试阻塞修复（2026-11-18）

> 本节只追加修复与验证证据，不改变本文对生产状态的判断；“全仓 Go 测试绿”仍不等于真实产品验收。

本轮先以 `go test -run '^$' ./...` 定位编译阻塞，再以
`go test ./... -count=1` 收敛实际失败，修复内容：

- `renderengine`：删除测试文件与生产代码重复的 `blankGrid`，改用生产 helper。
- event contract：登记 `tool_loop.child_results_consumption_failed`（0 通道，保持
  原投递行为），同步 `runtimeobserve` 已知类型目录，并运行 `contractgen` 更新前端
  `event-contract.ts`；生成物此前还落后多个已登记类型，本次一并收敛。
- 配置写锁：`aicli-remote-tools init` 在目标配置写入前持
  `agentconfig.LockConfigFileWrite`；`mcpmount` token/mount 快照在
  `save`/`Save` 入口持同一路径锁，再执行原子替换。
- SQLite 在线维护护栏：`storage prune-sessions` 的 CLI 帮助/错误文本从裸
  `VACUUM` 改为“离线压缩”，实际压缩仍仅经 allowlisted `storage compact`。
- auto-compaction 测试：确认 1280 token 预算处于 active-turn replay 估算边界，
  会导致 req2 前提前 compaction、响应序列错位；按实测 1240 重新标定，连续
  100 次保持“req1 存活、req2 后触发”的预期序列。
- `storefile` 数据目录锁：释放时先把 canonical lock 目录原子 rename 到唯一
  release 路径，再清理；避免 Windows 偶发 `RemoveAll` 失败把锁保留到 30s
  stale 阈值后阻塞等待者。
- `skillsapi` usage scope 测试：按现行 admin 授权契约配置 admin token；产品
  端点权限语义未放宽。

验证结果：

```text
go test -run '^$' ./...
go test ./... -count=1
go test ./internal/agent -count=3
go test ./internal/agent -run '^(TestReActLoop_RunWithSession_AutoCompactionRecoveryContinuesAfterPromptPreflightFailure|TestHeaderGuardExhaustionIsExplicitNotSilent)$' -count=20
go test ./internal/connectors/storefile -run '^TestConcurrentOwnerClaimSingleWinner$' -count=100
go test ./internal/memorystore -count=5
go test ./internal/events -run '^TestRegisteredTypesAreKnownToObserveCatalog$' -count=1
go test ./internal/sqliteutil -run '^TestNoOnlineDatabaseFileMaintenanceStatements$' -count=1
go test ./internal/configwriteguard -run '^TestConfigFileWritesStayUnderSharedWriteLock$' -count=1
go test ./pkg/skillsapi -run '^TestClient_UsageScopeSeparatesQuota$' -count=1
npx tsc -b
git diff --check
```

以上均通过。测试使用临时目录/SQLite fixture，未主动修改真实 SQLite 实例或
suspension 状态。仍不宣称生产验收通过：真实 provider/ConPTY、跨会话召回、
SQLite 多进程锁压测、legacy backlog 盘点与 M5 shadow 运营验收仍未完成。
### 本轮增量记录：receipt-first 主路径缺口修复（2026-11-18）

> 本节只追加实现与验证证据，不改变本文对生产状态的判断，也不把局部主路径测试写成生产验收。

本轮逐段审计正常主路径
`canonical terminal commit -> receipt -> materialize -> phase1_extract/v2 ->
candidate/output receipt -> consolidation -> injection`，确认并修复两个缺口：

- materialization 此前在 `MaterializeTurnCaptureReceipt*` 中只校验 generation
  与 canonical range，未复用 worker 的 `TriggerSessionEnd` eligibility。disabled/
  polluted session、subagent/ephemeral、workspace mismatch 与 rollout-age 仍会创建
  pending digest/job，worker 又永远跳过，形成不可 claim 的队列毒化。
  现在 materialization 使用同一 eligibility fence；ineligible receipt 不 ack、
  不创建 job，进入 `deferred`（1h 重试）。generation 缺失或 session generation
  不匹配同样 deferred，避免永久占据 captured 队首页。
- `TurnDigest.Enabled=false` 时 host 仍注入 `TurnDigestSink`，actor 仍提交 canonical
  receipt，但 sink 随后跳过 materialization，留下不可消费的 captured receipt。
  现在 aicli 与 runtime-server 仅在 `TurnDigestEnabled=true` 时注入 sink；
  `RecordTurnDigest` receipt 分支仍保留 defense-in-depth 检查，ineligible/
  generation-mismatch 作为 skipped capture 返回 nil。

新增/更新测试覆盖：

- `TestMaterializeTurnCaptureReceiptSkipsIneligibleSessions`：session mode
  disabled/polluted、missing generation、subagent、ephemeral、workspace
  missing/mismatch、rollout age；
- `TestRecordTurnDigestCaptureReceiptIneligibleIsSkipped`；
- `TestMaterializeTurnCaptureReceiptsSkipsReusedSessionGeneration`：断言 stale
  generation 变为 deferred，且不创建 job；
- aicli `TestLocalTurnDigestSinkWiresBothActorPaths` 与 runtime-server
  `TestSessionDepsWiresTurnDigestSinkOnlyWhenEnabled`：关闭 turn digest 时 sink=nil。

验证通过：

```text
go test ./internal/memoryruntime ./internal/memoryassembly ./internal/api/runtimeapi -count=1
go test -race ./internal/memoryruntime -run '^(TestMaterializeTurnCaptureReceiptSkipsIneligibleSessions|TestRecordTurnDigestCaptureReceiptIneligibleIsSkipped|TestMaterializeTurnCaptureReceiptsSkipsReusedSessionGeneration)$' -count=1
go test ./cmd/aicli/commands -run '^TestLocalTurnDigestSinkWiresBothActorPaths$' -count=5
go test ./... -count=1 -p=4
```

默认高并行度 `go test ./... -count=1` 仍可能在非主路径的 Windows 时序用例上
偶发失败（llm header cleanup、maintenance lease、MSYS job object）；相关用例隔离
重复通过，`-p=4` 全仓通过。测试只使用临时 SQLite/fixture，未主动修改真实 SQLite
实例或 suspension 状态。真实 provider/ConPTY、跨会话召回、多进程 SQLite 压测、
legacy backlog 与 M5 shadow 运营验收仍未完成，因此仍不宣称生产验收通过。
