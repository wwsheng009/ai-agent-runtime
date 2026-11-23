# AI Memory Engine 缺口实施方案（2026-11-22）

> 依据：[15-ai-memory-engine-design-gap-audit-20261122.md](./15-ai-memory-engine-design-gap-audit-20261122.md)（以下简称 `15`）与 [AI_Agent_Memory_Engine_Design.md](./references/AI_Agent_Memory_Engine_Design.md) v1.0。
>
> 定位：`15` 是缺口规格和验收口径；本文件是施工计划，只回答“按什么顺序改、改哪些文件、每步如何测试、何时可以关闭门禁”。
>
> 执行原则：**不再重复审计 `15` 已确认的缺口，不再以计划、分析或子任务状态替代代码和测试证据。** 每个工作包必须落到代码、迁移、测试或可复现的运维证据。
>
> 执行更新（2026-11-23）：G1-0 的 FTS5 阻断已通过 `ncruces/go-sqlite3/ext/fts5` + `sqlite3.AutoExtension` 解决；`memory_entries_fts`、BM25 搜索、HTTP/CLI/tool 统一路径、pinned/manual + FTS top-K 注入候选已落地。`win7compat` 仍保留 LIKE fallback。G1 剩余评测/性能证据，G0-5 backlog 尚未关闭。

## 0. 实施口径

### 0.1 与现有文档的关系

| 文档 | 本计划如何使用 |
|---|---|
| `15` | 缺口、关闭标准、G0–G5 门禁的唯一输入 |
| `04-data-model-and-api.md` | 现有字段、API、工具契约的兼容基线 |
| `05-implementation-plan.md` | 既有 M1–M5 实施方式；本计划不推翻，只补参考设计层缺口 |
| `06-test-and-acceptance.md` | 既有测试与证据包格式；本计划新增门禁测试沿用同一风格 |
| `14-runtime-gap-audit-and-fix-20261117.md` | 运行时写路径与统一执行核的既有约束 |
| `patches/2026-11-18-turn-end-durable-capture-and-async-extraction.md` | 仍是待评审提案；本计划不把未批准提案当作已实施前提 |

### 0.2 完成定义

一个工作包只有同时满足以下条件才算完成：

1. 代码和 schema migration 已落地；
2. 新增或修改的测试可重复通过；
3. `git diff --check` 无错误；
4. 门禁要求的证据已写入本文对应章节或独立 evidence 文件；
5. 失败、跳过、未覆盖项被明确记录，不用“其他测试通过”替代；
6. `Mode=off`、legacy JSONL 和既有 v2 行为未被无意改变。

### 0.3 本计划不做的事

- 不重新论证 `15` 中的 17 个缺口是否存在；
- 不在 FTS/评测之前先做 vector、Dreaming 或无约束模型写库；
- 不把 `phase2_consolidate succeeded` 当作 phase1 自动记忆闭环成功的证据；
- 不扩大 active session recovery 去消费未冻结 transcript；
- 不把 `memory_entries` 改造成全文 transcript 仓库；
- 不为了“统一”而删除 receipt、generation、purge fence、lease、stale source 校验；
- 不在本计划内顺带修复与缺口无关的交互式 CLI 测试。

### 0.4 当前已完成和未完成

已落地（G0 代码/fixture 证据，不代表 G0-5 dogfood 已关闭）：

- `StatusReport.ExtractionJobs` 与 `JobDiagnostic`；
- `Store.Status` 查询最近 `phase1_extract` / `phase1_extract/v2` job；
- HTTP `/api/runtime/memory/status` 返回 `extraction_jobs`；
- CLI `/memory status` 与 structured `/memory status` 展示 extraction job 诊断。
- G0-2 job operator、priority、deadline/timeout 与 admin retry/dead-letter 接口；
- G0-3 bounded active-session receipt extraction 与 `turn_threshold` trigger；task completion 目前仍只有 hook，没有生产 event source/consumer；
- G0-4 临时 SQLite E2E/restart/status fixture 测试；
- 历史缺失 deadline 的 bounded repair 实现，以及 ClaimJobs 的过期 pending 防启动保护（详见 §19.10）。

尚未完成：

- G0-5 真实 dogfood backlog 分类、dry-run 与受控恢复；本轮没有读取或修改真实库；
- G1–G5 的代码、迁移、测试和证据；
- 当前 driver 的 FTS5 能力确认。已知事实是：当前 SQLite 构建执行 `CREATE VIRTUAL TABLE ... USING fts5` 会报 `no such module: fts5`。这是 G1 的阻断项，不是可以在方案里假装已解决的问题。

### 0.5 环境事实和阻断项

1. **FTS5 驱动能力**：现有 `ncruces/go-sqlite3` 构建未带 FTS5。G1 必须先执行 `G1-0` 能力决策；未通过前 G1 状态只能是 `blocked`，不能以 LIKE 降级宣称 G1 关闭。
2. **命令包测试环境**：`cmd/aicli/commands` 中存在交互式、Skill、MCP、权限相关测试，整包运行会受宿主环境影响。各门禁使用定向测试；整包结果单独记录，不能用一个包级 `FAIL` 推断或否定某个门禁。
3. **dogfood 数据库**：`.aicli/memory/memory.db` 只用于只读诊断和受控恢复演练；任何清理前必须先备份，且不得提交数据库文件。

## 1. 目标架构与实施顺序

### 1.1 目标链路

```text
canonical turn commit
  -> durable capture receipt
  -> scope-aware materialization
  -> phase1_extract/v2 job
  -> candidate + source + output receipt
  -> consolidation / Dreaming patch
  -> versioned active memory
  -> hybrid retrieval (FTS/BM25 + entity + temporal [+ vector])
  -> context budget + trust-aware injection
  -> citation / usage / feedback
  -> evaluation / operations
```

### 1.2 门禁依赖

```text
G0 运行时闭环
  |
  +--> G1 检索基础（FTS/BM25 + 统一 SearchQuery）
  |       |
  |       +--> G5-R 混合检索、实体、时间、RRF
  |
  +--> G2 数据隔离（tenant/project/user/agent/session/task）
  |       |
  |       +--> G3 变更安全（versions/rollback/temporal write）
  |               |
  |               +--> G3.5 Core Memory / 分类预算
  |                       |
  |                       +--> G4 Episode + Dreaming Patch
  |                               |
  |                               +--> G5-T Trust + G5-Q Evaluation
```

说明：

- `G3.5` 不是 `15` 的原始门禁名，而是 G4 的进入条件（P1-06 Core Memory 与预算必须先稳定，否则 Episode/Dreaming 无法控制注入成本）。
- `G5` 拆成 `G5-R`（retrieval）、`G5-T`（trust）、`G5-Q`（quality/ops）三个证据子包；对外的 G5 门禁仍要求三个子包全部通过。
- G1 先做，但 SearchQuery 从一开始就预留 scope 字段；G2 只补数据列、授权和过滤，不重写检索语义。
- G2 的隔离是安全边界，优先于 G3/G4。G1 期间仍保持现有 project-only 行为，不因新增 FTS 放松隔离。

### 1.3 分批交付

| 批次 | 内容 | 可独立合并 | 默认开关 |
|---|---|---|---|
| B0 | 文档、基线测试、G0 status 字段测试 | 是 | 不改变行为 |
| B1 | G0 backlog 诊断、恢复、active session 消费、E2E | 是 | 保持现有 trigger 默认值 |
| B2 | G1 FTS 能力决策、FTS 索引、统一 SearchQuery、CLI/HTTP/tool 一致性 | 是 | FTS 可用时启用，否则显式降级 |
| B3 | G2 scope/tenant/owner/agent/task 隔离与 runtime 路由 | 是 | 默认 project-only，配置后启用 |
| B4 | G3 versions/rollback/temporal write | 是 | versions 默认写；rollback 需权限 |
| B5 | G3.5 Core Memory + 分类预算 | 是 | 保守预算，可配置 |
| B6 | G4 Episode + Dreaming Patch | 是 | Dreaming 默认 shadow/off |
| B7 | G5-R 实体/时间/RRF、G5-T trust、G5-Q 评测 | 分三个子包 | vector 默认 off |

每个批次单独提交，不把 schema、检索、prompt、UI、CLI 和运维脚本混在同一提交。

### 1.4 迁移与回滚总规则

1. 只做 additive migration；不删除、不重命名现有列，不改变旧列默认语义。
2. 新列必须有默认值或允许 NULL；存量行通过分批 backfill，避免长事务。
3. 新索引先建后回填；FTS 索引属于派生数据，可重建，不作为事实源。
4. 每个 migration 提供 `up` 后的计数校验；失败时保留原库备份，不做不可逆清理。
5. 回滚优先使用“关闭新读路径 + 保留新表/新列”；只有确认无引用后才允许 drop index/table。
6. 版本表、patch 表、审计表 append-only；禁止 UPDATE/DELETE（用 trigger 拒绝）。
7. 涉及 dogfood 库的迁移先在副本执行，再对真实库执行；证据记录命令和输出摘要。

## 2. 共同约束

### 2.1 兼容性

- `memory.mode=off`：不创建 v2 自动 DB、不调用模型、不注册新工具、不注入自动记忆；legacy JSONL 行为不变。
- `memory.mode=shadow`：允许写入和诊断，但注入仍按现有 shadow 规则；不得因新检索路径改变默认注入结果。
- `memory.mode=on`：才允许新检索、scope、Core、Dreaming 等能力生效。
- 旧 `phase1_extract`、`RunSession`、`RunSessionWindow`、`RunPendingTurnDigests`、`RecoverPendingPhase1` 继续作为 pre-receipt/historical 兼容路径；receipt-backed 正常路径只走 capture worker。
- 任何新字段/新表缺失时，读路径应回退到旧行为或显式降级，不能 panic。

### 2.2 隔离与 fail closed

- 所有读写、检索、summary、usage、audit、job、purge、export/import 查询必须经过统一的 scope filter builder。
- scope 上下文缺失、tenant/project 不匹配、owner/agent 不匹配时：返回空结果或明确错误，不得退回“全项目可见”。
- 跨 tenant 永远禁止；跨 project/user/agent/task 需要显式 grant 或同一 scope chain 的明确关联。
- 负向测试必须覆盖“没有权限时返回空/拒绝”，而不是只覆盖正向可见。

### 2.3 幂等、审计和来源

- 所有自动写入必须带 source IDs、session/turn/range、capture receipt/generation 或等价幂等身份。
- 版本、patch、rollback、scope 变更、trust 提升、Dreaming 应用都必须写 `memory_events` 或专用 audit 表。
- 自动抽取的 `ignored` 原因、空结果、失败原因必须可查询；不能只存在于一次 `RunResult`。
- 任何删除/抑制都必须同步 FTS、summary、usage 和注入候选；异步同步失败要有重试/诊断。

### 2.4 检索语义

- 统一入口：`memory_search`、HTTP records search、CLI list/search 调用同一个 store 查询方法。
- 默认不再区分 `source_kind=manual` 与自动抽取；manual 只是可选过滤条件。
- 文本查询至少覆盖：精确标识符、错误码、路径、函数名、版本号、CJK。
- 文本查询无结果与检索后端不可用必须区分：前者返回空并带诊断，后者返回 `search_backend=like_fallback` 和明确状态。
- 注入路径必须先做 SQL 层候选裁剪或 FTS top-K，不能无条件全表加载。

### 2.5 测试口径

- 单元测试：`go test ./internal/memorystore ./internal/memoryruntime ./internal/memoryassembly ./internal/contextmgr ./internal/toolkit/tools -count=1`。
- API 测试：`go test ./internal/api/runtimeapi -count=1`。
- CLI 定向测试：`go test ./cmd/aicli/commands -run '<具体测试名>' -count=1`；不把整包交互测试结果混入门禁。
- 每个门禁至少包含一个负向测试和一个重启/重放/幂等测试（适用时）。
- FTS 测试在驱动不支持时必须显式 skip 并输出 `fts_unavailable`；G1 不能据此标记通过。

## 3. G0：运行时闭环与可观测性

对应缺口：`GAP-P0-04`、`GAP-P2-02`（status 部分）、`GAP-P2-03`（dead-letter/priority/timeout 部分）。

### 3.0 进入条件

- 现有 capture receipt → materialization → `phase1_extract/v2` → candidate → consolidation → injection 测试仍通过；
- 不新增绕过 capture worker 的 transcript replay；
- `.aicli/memory/memory.db` 已备份（只读诊断阶段可先不备份，执行恢复前必须备份）。

### 3.1 G0-1 完成 status/backlog 契约

**目标**：让“为什么没有记忆”可以从 status 直接回答，而不是再次读代码。

**现有代码**

- `backend/internal/memorystore/v2_types.go:299-332`
- `backend/internal/memorystore/v2_helpers.go:1074-1209`
- `backend/internal/api/runtimeapi/memory_handlers.go:198-246`
- `backend/cmd/aicli/commands/chat_memory_command.go:117+`
- `backend/cmd/aicli/commands/chat_memory_document.go:47+`

**实施步骤**

1. 扩展 `StatusReport` / `JobDiagnostic`，保持 JSON additive：
   - `JobDiagnostic` 增加 `JobKey`、`SessionID`、`Priority`、`CreatedAt`、`DeadLetterAt`、`DeadLetterReason`、`TimeoutAt`；
   - `StatusReport` 增加 `PendingExtractionJobs`、`PendingExtractionSessions`、`OldestPendingJobAgeSeconds`、`DeadLetterJobs`、`LastExtractionAt`（可由 job 时间推导）、`SearchBackend`（G1 后使用）；
   - 不在 store 层塞入“runtime 是否运行”的进程状态；HTTP 层补充 `MemoryRuntimePresent`、`MemoryRuntimeWorkspace`、`GenerationEnabled/Trigger`、`IdleEnabled`、`LastMaintenanceAt`、`LastSkipReason` 等运行时字段。
2. `Store.Status` 的 job 查询从“最近 20 条”改为：
   - 按 `kind/status` 聚合；
   - 最近 20 条明细；
   - 单独统计 pending/leased/retry/dead-letter；
   - 对 `phase1_extract`、`phase1_extract/v2` 都展示，但标明 legacy/v2。
3. HTTP `/api/runtime/memory/status` 原样映射新字段；不要改变旧字段名。
4. CLI `/memory status` 输出：
   - `mode/scope/schema/workspace`；
   - `total/manual/active/candidate/superseded/stale`；
   - `pending digest`、`pending extraction job`、`oldest age`、`dead-letter`；
   - 最近 extraction job 明细（kind/id/status/attempt/available/lease/last_error）。
5. structured `/memory status` 使用同一 DTO，避免文本输出和 JSON 输出两套逻辑。

**测试**

- `backend/internal/memorystore/v2_status_test.go`（新增或就近扩展）：
  - 构造 pending/succeeded/failed/leased/dead-letter job；
  - 断言计数、最老 age、最近明细、legacy/v2 kind；
  - 断言 `Mode=off` 时 fallback 字段保持旧行为。
- `backend/internal/api/runtimeapi/memory_handlers_test.go`：
  - HTTP 响应包含 `extraction_jobs`、backlog 聚合和 runtime 字段；
  - 非 admin 不泄露敏感 last_error（按现有脱敏规则）。
- `backend/cmd/aicli/commands/chat_memory_v2_command_test.go`：
  - structured status 包含新字段；
  - 文本输出包含 pending/dead-letter 行。

**退出条件**

- 只读 status 即可区分：没有记忆、没有 candidate、有 pending digest、有 pending job、job 失败、job dead-letter、FTS 降级。

### 3.2 G0-2 backlog 分类、恢复和 dead-letter

**目标**：让 pending digest/job 有明确终态，不再无限 pending。

**实施步骤**

1. 在 `memorystore` 增加诊断/控制方法（命名可调整，但职责固定）：
   - `ListJobDiagnostics(ctx, JobFilter)`：按 kind/status/session/age/project 过滤；
   - `RequeueJob(ctx, id, reason)`：仅允许 failed/dead-letter/可重试 pending；
   - `DeadLetterJob(ctx, id, reason)`：写 `dead_letter_at/dead_letter_reason`，保留 payload；
   - `ReplayDigest(ctx, id, reason)`：仅对 orphaned digest 且 capture receipt/generation 校验通过的场景；generation mismatch 只能标记 skip/dead-letter，不得绕过 generation fence 重放。
2. 使用现有 `memory_jobs.dead_letter_at/dead_letter_reason` 字段（`v2_schema.go:638-639`），不新建第二套 job 状态表。
3. 默认 priority：
   - `phase1_extract/v2` > `summary_rebuild` > `phase2_consolidate` > `reindex` > `dreaming`（Dreaming 后续补）；
   - 具体数值写入 job 创建处，不在 claim 时临时推断。
4. timeout：
   - job 创建时写 `deadline_at`；
   - worker claim 后超过 deadline 且无法续租时，写 failed/dead-letter + `last_error`，不得只依赖 lease 过期。
5. API：
   - `GET /api/runtime/memory/jobs?kind=&status=&session_id=&limit=`（扩展现有）；
   - `POST /api/runtime/memory/jobs/{id}/retry`；
   - `POST /api/runtime/memory/jobs/{id}/dead-letter`；
   - 全部 admin 权限，写 audit。
6. CLI：
   - `/memory jobs`、`/memory jobs replay-preview [limit]`、`/memory jobs retry <id>`、`/memory jobs dead-letter <id>`；preview 是 local-runtime-only 的 read-only dry-run。
7. 恢复执行顺序：
   - 先 dry-run 输出分类报告；
   - 再处理 orphaned digest；generation mismatch 单独列出并 skip/dead-letter，不重放；
   - 再 retry 可重试失败；
   - 最后 dead-letter 明确不可恢复项；
   - 每步记录 job id、原状态、原因、结果。

**测试**

- dead-letter 状态迁移、重放幂等、priority 顺序、deadline 超时；
- 已 succeeded job 不允许 retry；
- lease 未过期时不允许被第二个 worker claim；
- 非 admin 调用返回 403；
- `Mode=off` 不创建 job、不执行恢复。

### 3.3 G0-3 active session 抽取策略

**目标**：解决 `trigger=session_end` 下长生命周期 active session 只产生 digest、不抽取的问题，同时不消费未冻结 transcript。

**实施步骤**

1. 保留现有安全边界：只处理有 durable capture receipt 的冻结 turn/range；没有 receipt 的 active session 不进入 capture worker。
2. 将 capture extraction drain 从“仅 idleEnabled”改为独立、可配置、bounded 的 maintenance 步骤：
   - `memory.maintenance.captureExtraction.enabled`（默认 true，仅 v2）；
   - `memory.maintenance.captureExtraction.batchSize`；
   - `memory.maintenance.captureExtraction.interval`；
   - 每轮只 claim 有到期 `available_at` 的 job，遵守 lease/timeout。
3. 增加 trigger：
   - `session_end`（现有）；
   - `idle`（现有）；
   - `turn_threshold`：活跃 session 的已完成 turn 达到阈值后，允许触发一次 capture extraction；
   - `task_completion`：G4 接入；G0 只预留枚举和 event hook，不改变默认行为。
4. `turn_threshold` 的判断只使用 canonical 已完成 turn 和 capture receipt；不得读取 streaming/未冻结内容。
5. startup recovery 仍只处理 terminal session；active session 由 maintenance 处理，避免启动时全量回放。
6. 记录 `LastExtractionAt`（DB/job 时间）、`LastMaintenanceAt`、`LastSkipReason`（runtime 状态），在 HTTP/CLI status 中可查。

**测试**

- active session 产生 N 个冻结 turn 后，不结束 session 也能完成至少一个 extraction job；
- 未冻结 turn 不被读取；
- capture receipt generation mismatch 时不提交 candidate；
- `trigger=session_end` 下 maintenance 仍能 drain 已到期 v2 job；
- 关闭 `captureExtraction.enabled` 时行为回到现状；
- 重启后 lease 过期 job 可恢复。

### 3.4 G0-4 多会话 E2E 与证据

**目标**：用真实链路证明“自动抽取记忆可以被新会话注入”，而不是只证明组件能跑。

**测试用例**

1. `TestG0ActiveSessionThresholdExtraction`：
   - 两个 session；
   - 第一个 session 不结束，达到 turn threshold；
   - worker 完成 capture-backed extraction；
   - candidate 经 consolidation 变 active；
   - 第二个新 session recall/injection 至少包含一条自动抽取记忆；
   - 断言不是 manual seed。
2. `TestG0CrashRecoveryAfterCaptureReceipt`：
   - materialization 后崩溃；
   - 重启恢复；
   - job 只完成一次，output receipt/candidate 不重复；
   - receipt generation mismatch 时不确认。
3. `TestG0StatusExplainsBacklog`：
   - 构造 pending/leased/failed/dead-letter；
   - status 输出可区分原因。
4. 扩展现有测试时优先复用：
   - `backend/internal/memoryruntime/auto_e2e_test.go`
   - `backend/internal/memoryruntime/capture_worker_test.go`
   - `backend/internal/memoryruntime/e2e_sqlite_test.go`
   - `backend/internal/memoryruntime/canonical_recovery_test.go`

**测试命令**

```bash
cd backend
go test ./internal/memoryruntime -run 'TestG0|TestAutomaticMemoryEndToEndWithoutManualSeed|TestE2ESQLiteCanonicalRestartRecovery|TestRunPendingTurnCaptureReceiptsCommitsOneTurnAtomically' -count=1
go test ./internal/memorystore ./internal/memoryassembly ./internal/api/runtimeapi -count=1
```

**证据**

- 测试输出；
- 测试中使用的 session/turn/receipt/job/candidate ID；
- 新会话注入的 record ID 与 source ID；
- status 快照（脱敏后）。

### 3.5 G0-5 dogfood 库恢复

**目标**：关闭 `15` §6 的运行时积压问题，但不在代码未验证前直接改真实库。

**步骤**

1. 备份：
   ```bash
   cp .aicli/memory/memory.db .aicli/memory/memory.db.pre-g0-<timestamp>
   ```
2. 用只读方式导出 backlog 分类：
   - 37/40 条 digest 的 session、status、created_at、capture_receipt_id；
   - 13/16 条 pending job 的 kind、attempt、available_at、lease_until、last_error；
   - 7 条 v2 failed、10 条 legacy failed 的 last_error 分组；
   - `memory_entries`、`memory_summaries`、`memory_usage` 计数。
3. 在副本上执行 dry-run 恢复计划，确认：
   - 哪些可 retry；
   - 哪些应 dead-letter；
   - 哪些是历史 legacy 数据，不应被 v2 消费；
   - 哪些 job 的 canonical source 已不可用。
4. 在真实库执行恢复；每条操作记录 job id、前后状态、原因。
5. 验证：
   - `memory_entries > 0`；
   - 至少一个 pending v2 job succeeded；
   - 至少一条自动抽取记忆进入 active；
   - 新会话可注入；
   - backlog 不再是无解释的 pending。

**注意**

- 不把历史 legacy failed job 强行标记 success；
- 不为了清零而删除 digest/job；
- 不把 `phase2_consolidate succeeded` 作为 G0 通过证据；
- 如果恢复后仍无法产生 active memory，回到 G0-3/G0-4 找原因，不用手工 insert 伪造结果。

### 3.6 G0 退出条件

- [ ] status 能解释 digest/job 的 pending、lease、attempt、last error、dead-letter；
- [ ] active session 有非 `session_end` 的安全抽取路径；
- [ ] 多会话 E2E 通过，且注入记录能追溯到自动抽取 source；
- [ ] dogfood 库 `memory_entries > 0`、至少一个 v2 job succeeded、新会话注入成功；
- [ ] 未关闭的 backlog 有明确分类和原因；
- [ ] `Mode=off` 行为回归通过。

## 4. G1：检索基础与统一语义

对应缺口：`GAP-P0-01`、`GAP-P2-01`（search scope/type/time 部分）、`GAP-P2-02`（CLI list/search 部分）、`GAP-P2-04`（FTS 所需字段部分）。

### 4.1 G1-0 FTS5 能力决策（阻断门）

**背景事实**

当前 SQLite driver 实测不支持 FTS5。`backend/internal/knowledge/store_sqlite_fts.go` 已采用“FTS5 可用则建 external-content 表 + trigger，不可用则清理残留 trigger 并降级 LIKE”的成熟模式。G1 不应重复试错，也不应把 LIKE 降级包装成 FTS5 完成。

**决策任务**

1. 在测试环境执行能力探针：
   ```sql
   CREATE VIRTUAL TABLE temp.fts_probe USING fts5(x);
   ```
   记录 driver、SQLite version、错误。
2. 评估三种方案，必须明确选择一种并记录理由：
   - A：引入带 FTS5 的 SQLite 构建/驱动；作为 G1 关闭环境；
   - B：使用 FTS4/其他可在当前 driver 工作的全文索引，但要证明 BM25 等价排序或改用可解释的替代评分；
   - C：当前 driver 不支持时保留 LIKE fallback，但 G1 标记 `blocked`，不得宣称关闭。
3. 推荐：**优先 A**；迁移和 store 代码沿用 knowledge 层的 best-effort 模式，FTS 对象不在核心 migration 中创建，避免 `no such module` 让整个 DB 打不开。
4. 驱动/构建变更必须隔离在 memorystore 的可注入 opener 或构建标签内，不得顺带替换 chat/artifact/catalog 等模块的 SQLite driver。
5. status 增加 `search_backend=fts5|fts4|like_fallback`、`fts_available`、`fts_error`。

**退出条件**

- 至少一个正式支持环境通过 `CREATE VIRTUAL TABLE ... USING fts5`；
- 明确 fallback 环境的行为和 G1 状态；
- 该决策写入本文件执行记录和配置文档。

### 4.2 G1-1 schema 与 FTS 索引

**前置字段**

在 `memory_entries` 增加（additive，默认空）：

- `title TEXT NOT NULL DEFAULT ''`
- `summary TEXT NOT NULL DEFAULT ''`

`content` 使用现有 `text`；tags 使用现有 `tags_json`。`category`、`canonical_id`、`superseded_by`、archived/invalidated 在 G3/G4 按需增加，不与 FTS 迁移混在一起。

**FTS 对象**

- `memory_entries_fts`：external-content FTS5 表，列为 `title, text, summary, tags_json`；
- `content='memory_entries', content_rowid='rowid'`；
- 触发器：`AI/AD/AU` 同步；删除/纠正/抑制/恢复都必须经过同一事务或可靠 outbox；
- 首次创建后执行 `INSERT INTO memory_entries_fts(memory_entries_fts) VALUES('rebuild')`；
- FTS5 不可用时清理 trigger，避免每次写 `memory_entries` 都报 `no such module`；
- 提供 `memory_entries_fts` 重建命令/管理 API，索引损坏时可恢复。

**tokenizer**

- 首选 `trigram`：支持 CJK 和代码标识符子串；
- 查询短于 3 字符时走受控 LIKE 补充，再与 FTS 结果去重；
- 如果目标 FTS5 构建不支持 trigram，退回 `unicode61` + CJK bigram query expansion，并把该选择写入 status/迁移注释；
- 无论选择哪种，必须用统一 corpus 锁定行为，不能只测英文单词。

**文件落点**

- 新增 `backend/internal/memorystore/v2_fts.go`（参考 `knowledge/store_sqlite_fts.go`）；
- `backend/internal/memorystore/v2_schema.go` 增加 title/summary 迁移；
- `backend/internal/memorystore/v2_store.go` 打开 store 后 `ensureMemoryFTS`；
- `backend/internal/memorystore/v2_types.go` 增加 FTS status 字段；
- tests：`v2_fts_test.go`。

### 4.3 G1-2 统一 SearchQuery / SearchRecords

**目标**：消除 manual-only、`LIKE` 全表扫描和 CLI/HTTP/tool 三套语义。

**新增类型（建议）**

```go
type SearchQuery struct {
    Text     string
    Scope    ScopeFilter   // G1 只填 project；G2 扩展
    Statuses []Status      // 默认 active+candidate
    Kinds    []string
    Tags     []string
    Source   string        // 可选 manual/auto
    AsOf     *time.Time    // G5 使用
    Limit    int
    Cursor   string
    Explain  bool
}

type SearchHit struct {
    Record      Record
    Score       float64
    MatchSource string // fts|like|recent|entity|semantic
    FTSRank     float64
}
```

**实施步骤**

1. 新增 `Store.SearchRecords(ctx, SearchQuery)`；`Search`/`List` 只保留兼容适配，不再复制过滤逻辑。
2. 文本查询：
   - FTS 可用：`memory_entries_fts MATCH ?` + `bm25()`，join 主表加 status/freshness/scope/suppression 条件，SQL `LIMIT` 截断；
   - FTS 不可用：受控 LIKE fallback，但显式标记 backend；
   - 空文本：按 updated_at/usage/confidence 排序，SQL 层 limit。
3. 排序基线：BM25 > recency/usage/confidence/trust 的可解释加权；具体权重进配置或常量，测试固定。
4. 统一 filter：
   - status 默认 `active` + `candidate`；
   - `include_superseded` / `include_deleted` 显式开关；
   - `source_kind` 可选，不再默认 manual；
   - tags 使用结构化匹配，不做 JSON 子串猜测。
5. HTTP `GET /api/runtime/memory/records`：
   - 新增 `q`、`status`（可重复）、`kind`、`tags`、`source_kind`、`limit`、`cursor`；
   - 保持旧参数兼容；响应增加 `search_backend`、`count`、`next_cursor`。
6. `memory_search`：
   - 增加 `scope`、`types[]`、`time_range` 参数（G2/G5 生效）；
   - 底层调用 `SearchRecords`；
   - 返回 `match_source`、`score`、`record`，保持旧字段兼容。
7. CLI：
   - `/memory list` 默认查 active+candidate，manual 仅可选过滤；
   - `/memory search` 使用 `SearchRecords`；
   - `/memory status` 使用 `Status()`/`Metrics()`，区分 total/manual/active/candidate/stale。

**测试**

- 精确标识符：`E_INVALID_ARGUMENT`、`internal/foo.go`、`NewStore`、`v1.2.3`；
- CJK：中文词、混合中英文、短查询（1–2 字）；
- 同步：insert/update/correct/supersede/delete/restore 后 FTS 结果正确；
- CLI/HTTP/tool 对同一 query 返回同一 record 集合（允许展示字段差异）；
- manual 与 auto-extracted 都能被 list/search 看到；
- FTS 不可用时明确返回 `like_fallback`，测试 skip 不等于 G1 通过。

### 4.4 G1-3 注入候选裁剪

**问题**：`loadInjectableRecords` 先加载全部 active 记录再内存排序，`15` 要求改为 SQL 层候选裁剪或 FTS top-K。

**实施步骤**

- 保留现有 stable/recall、pinned 预算、summary gate、privacy/freshness/suppression 过滤；
- 先按 scope/status/freshness 和查询文本做 SQL 候选裁剪（默认上限可配置，例如 200）；
- 再在候选集内做预算选择和 stable/recall 分段；
- 记录 `candidate_count`、`filtered_count`、`search_backend`、`truncated`；
- 新增 benchmark：10k/100k records 下注入候选加载时间不再随全表线性增长。

**测试**

- pinned/manual 在 summary 满预算时仍可见；
- stale/private/suppressed 不进候选；
- 候选上限截断时 `why-not` 能解释；
- 无查询时仍能选出近期/高价值记录；
- FTS 降级时结果不丢失。

### 4.5 G1 退出条件

- [ ] G1-0 能力决策完成，G1 关闭环境确实支持所选全文索引；
- [ ] FTS 同步/回填/重建/降级测试通过；
- [ ] `memory_search`、HTTP、CLI list/search 使用同一 `SearchRecords`；
- [ ] 自动抽取记忆在 CLI/HTTP/tool 均可见；
- [ ] 精确标识符、CJK、错误码、路径、函数名、版本号测试通过；
- [ ] 注入路径不再无条件全表加载，有基准证据；
- [ ] 如果只能 LIKE fallback，G1 明确标记 `blocked`，不得写成完成。

## 5. G2：Scope 与数据隔离

对应缺口：`GAP-P0-02`。

### 5.0 设计决策

现有 `scope` 字段只表达 project/user/profile，`project_key` 是唯一硬过滤。G2 不直接改变旧字段语义，而是增加可组合的 scope 维度，并保留 project 兼容：

- `tenant_id`：租户/组织隔离，默认空字符串表示单租户；
- `owner_id`：用户/负责人；
- `agent_id`：Agent 实例；
- `session_id`：会话级工作记忆；
- `task_id`：任务级工作记忆；
- `scope_kind`：`project|user|agent|session|task`；
- `scope_key`：对应 kind 的 canonical key。

读取优先级（从具体到通用）：

```text
task > session > agent > user > project
```

同一查询默认只返回当前 scope chain 内可见记录；跨 tenant 永远不可见。跨 project/user/agent/task 需要 `memory_scope_grants` 显式授权，且授权本身受 admin 权限和 audit 约束。

### 5.1 G2-1 schema 与 backfill

**字段**

`memory_entries` 增加：

- `tenant_id TEXT NOT NULL DEFAULT ''`
- `owner_id TEXT NOT NULL DEFAULT ''`
- `agent_id TEXT NOT NULL DEFAULT ''`
- `scope_kind TEXT NOT NULL DEFAULT 'project'`
- `scope_key TEXT NOT NULL DEFAULT ''`
- `task_id TEXT NOT NULL DEFAULT ''`
- `superseded_by TEXT`（G3 使用，可与版本迁移同批）

`session_id` 可复用现有 `source_session_id`，但为了 scope 查询稳定，建议增加显式 `scope_session_id` 或复用 `source_session_id` 并建索引；实施时二选一，不能同时维护两个值。

**索引**

- `(tenant_id, scope_kind, scope_key, status, updated_at DESC)`
- `(tenant_id, project_key, owner_id, agent_id, status)`
- `(tenant_id, task_id, status)`
- `(tenant_id, source_session_id, status)`

**关联表 audit/usage/job**

至少给以下表补 tenant/scope 快照，否则只隔离 entries 仍会泄漏：

- `memory_jobs`
- `memory_turn_digests`
- `memory_summaries`
- `memory_usage`
- `memory_events`
- `memory_recall_traces`
- `memory_suppressions`
- `memory_turn_output_receipts`（若按 scope 查询）

**backfill**

- 存量 entries：`tenant_id=''`、`owner_id=''`、`agent_id=''`、`scope_kind='project'`、`scope_key=project_key`；
- 存量 jobs/digests/summaries/events：`tenant_id=''`、`scope_kind='project'`、`scope_key=project_key`；
- 分批更新，每批记录行数；迁移后校验无 `scope_key=''` 的存量 project 行（新写入也必须禁止空 key）。

**授权表**

```sql
CREATE TABLE memory_scope_grants (
    id           TEXT PRIMARY KEY,
    tenant_id    TEXT NOT NULL DEFAULT '',
    grantor_id   TEXT NOT NULL,
    grantee_type TEXT NOT NULL, -- user|agent|task
    grantee_id   TEXT NOT NULL,
    scope_kind   TEXT NOT NULL,
    scope_key    TEXT NOT NULL,
    permission   TEXT NOT NULL, -- read|write|admin
    expires_at   TEXT,
    created_at   TEXT NOT NULL,
    revoked_at   TEXT
);
```

G2 只实现 read/write 的最小集合；跨 tenant 授权不允许。

### 5.2 G2-2 写入路径

1. 在 `StoreConfig` 或调用参数中增加 `ScopeContext`：
   ```go
   type ScopeContext struct {
       TenantID   string
       ProjectKey string
       OwnerID    string
       AgentID    string
       SessionID  string
       TaskID     string
   }
   ```
2. 所有手工写入、自动 candidate、correction、import 必须显式带 scope context；缺 tenant/project 时 fail closed。
3. 自动抽取默认写 `scope_kind='project'`，同时记录 owner/agent/session 来源；只有显式配置或任务完成 Episode 才写 user/agent/session/task scope。
4. capture materialization 和 `phase1_extract/v2` job 的幂等身份增加 tenant/project/owner/agent；worker 发现 job scope 与当前 runtime scope 不一致时拒绝执行并记录原因。
5. `memory_jobs`、`memory_turn_digests` 创建时写 scope 快照；不能用“查询时再 join entries”推断。
6. `memory_add` 工具只能写当前调用者 scope；管理员跨 scope 写入必须走专用 admin API 并写 audit。

### 5.3 G2-3 读取路径与 fail closed

1. 新增统一 `BuildScopeFilter(ctx, ScopeContext) (SQLFragment, args, error)`；禁止各查询自行拼 `project_key = ?`。
2. 查询链：
   - 当前 task/session/agent/user/project scope；
   - 同 tenant；
   - 未过期 grant；
   - suppression/freshness/privacy；
   - status 默认 active+candidate。
3. 所有入口接入：
   - records list/get/search
   - `memory_search` / `memory_recent` / `memory_session_context`
   - injection `SelectForInjectV2`
   - summary/usage/audit/recall traces
   - jobs/digests
   - purge/export/import
   - feedback/versions/patches（后续门禁）
4. 缺失 scope 时：
   - 读：返回空结果 + `scope_error=missing_context`，不得回退 project 全局；
   - 写：返回 validation error；
   - 后台 worker：skip + 记录原因，不创建无 scope 数据。
5. 删除/抑制/导出：
   - 只能操作当前 scope 或显式 grant；
   - export 必须带 scope manifest，import 必须校验 manifest 与目标 scope；
   - purge 必须同时清理 entries、sources、versions、usage、feedback、FTS、summary references。

### 5.4 G2-4 runtime-server 多 workspace 路由

**现状证据**：`memory_runtime_wiring.go:159-175` 只创建一个基于 `runtimeConfig.Workspace.Root` 的 runtime；`turn_digest_sink.go:70-76` 对 workspace mismatch 可能跳过。

**实施步骤**

1. 新增 per-workspace registry：
   ```go
   type memoryRuntimeRegistry struct {
       mu       sync.RWMutex
       runtimes map[string]*memoryruntime.Runtime // canonical workspace root
   }
   ```
2. session 的 workspace 先 canonicalize，再 lookup/create；禁止 default runtime 处理不匹配 workspace。
3. runtime 创建失败时：
   - 记录 `MemoryRuntimePresent=false`、`MemoryRuntimeError`；
   - 不把 session 静默丢弃；
   - 不把其他 workspace 的 DB 当作 fallback。
4. API `resolveMemoryWorkspace` 与 `openMemoryStore` 使用同一 canonicalization 规则；`workspace_path` 参数只允许解析到已注册或可创建的 runtime。
5. 测试：
   - 两个 workspace 同时存在，各自的 digest/job/entries 不串库；
   - workspace mismatch 时 status 明确报告；
   - runtime 重启后 registry 恢复；
   - 非 admin 不能查询其他 workspace。

### 5.5 G2 测试矩阵

| 测试 | 预期 |
|---|---|
| 同 project 不同 user | 互不可见（除非 grant） |
| 同 user 不同 project | 互不可见 |
| 同 project 不同 agent | 默认不串；agent scope 明确时按规则 |
| 同 session 不同 task | task scope 隔离 |
| 不同 tenant | 永远不可见 |
| 缺 scope context | 读空/写拒绝 |
| 过期 grant | 不可见 |
| purge/export/import | 只影响当前 scope |
| jobs/digests/summaries/usage | 只统计当前 scope |
| runtime-server 两 workspace | 不串库、不静默跳过 |

### 5.6 G2 退出条件

- [ ] 所有读写入口统一 scope filter，无遗留裸 `project_key = ?`；
- [ ] 跨 tenant/user/agent/task 负向测试通过；
- [ ] 缺 scope 时 fail closed；
- [ ] runtime-server 多 workspace 路由测试通过；
- [ ] export/import/purge/summary/usage/audit 均带 scope；
- [ ] `Mode=off` 和单 project 默认行为无回归。

## 6. G3：版本、回滚与时间

对应缺口：`GAP-P0-03`、`GAP-P1-03`（写入/历史基础部分）、`GAP-P2-04`（canonical/superseded/证据部分）。

### 6.1 G3-1 `memory_versions` schema

```sql
CREATE TABLE memory_versions (
    id              TEXT PRIMARY KEY,
    memory_id       TEXT NOT NULL,
    version         INTEGER NOT NULL,
    tenant_id       TEXT NOT NULL DEFAULT '',
    project_key     TEXT NOT NULL DEFAULT '',
    scope_kind      TEXT NOT NULL DEFAULT 'project',
    scope_key       TEXT NOT NULL DEFAULT '',
    kind            TEXT NOT NULL,
    status          TEXT NOT NULL,
    title           TEXT NOT NULL DEFAULT '',
    text            TEXT NOT NULL,
    summary         TEXT NOT NULL DEFAULT '',
    tags_json       TEXT NOT NULL DEFAULT '[]',
    metadata_json   TEXT NOT NULL DEFAULT '{}',
    valid_from      TEXT,
    valid_until     TEXT,
    changed_by      TEXT NOT NULL DEFAULT '',
    change_reason   TEXT NOT NULL DEFAULT '',
    change_type     TEXT NOT NULL, -- create|update|correct|supersede|delete|restore|rollback|import|dreaming
    source_event_id TEXT,
    created_at      TEXT NOT NULL,
    UNIQUE(memory_id, version)
);
```

约束：

- append-only；用 `BEFORE UPDATE/DELETE` trigger `RAISE(ABORT, 'memory_versions is append-only')`；
- 索引 `(memory_id, version DESC)`、`(tenant_id, project_key, created_at DESC)`；
- `version` 与 `memory_entries.revision` 对齐；每次提交后写入新状态快照；
- `changed_by`/`change_reason` 来自调用者或 job/patch，不能为空字符串（系统操作写 `system:<job_kind>`）。

### 6.2 G3-2 所有变更写版本

实现一个事务内 helper：

```go
func appendRecordVersionTx(ctx, tx, record, changeType, actor, reason, sourceEventID) error
```

必须接入：

- `Append` / 手工创建；
- 自动 candidate 创建（v1）；
- `UpdateRecord`；
- `CorrectRecord`；
- `SupersedeRecord`；
- `Delete` / `Forget`；
- `Restore`；
- Consolidation 的 promote/merge/supersede；
- import；
- G4 patch apply 和 rollback。

要求：

- 版本写入与主表变更同一事务；
- 任何路径漏写版本视为 bug，测试覆盖；
- 旧记录被 supersede 时，旧记录 `valid_until` 和新记录 `valid_from` 同时写入；
- 删除/恢复、summary 引用、FTS 同步在同一事务或可靠 outbox 中完成。

### 6.3 G3-3 rollback / restore

**API**

```http
POST /api/runtime/memory/records/{id}/rollback
{
  "version": 3,
  "expected_revision": 5,
  "reason": "revert incorrect consolidation supersede",
  "revert_supersede": true
}
```

**语义**

1. 读取目标版本；不存在返回 404；
2. 当前 revision 与 `expected_revision` 不一致返回 409（防并发覆盖）；
3. 追加新版本 `version=current+1`，`change_type='rollback'`；
4. 恢复目标版本的 text/title/summary/tags/metadata/status；
5. `revert_supersede=true` 时：
   - 找到当前 supersede 该记录的后继；
   - 将后继标记 `superseded`/`invalidated`，写 `valid_until`；
   - 恢复目标记录为 active，写 `valid_from`；
   - 若后继已被再次修改，返回冲突，要求先 rollback 后继或使用显式 force；
6. 写 audit、版本、FTS 同步；
7. 返回新 revision 和 rollback version id。

**工具/CLI**

- `memory_rollback`，admin 或 owner 权限；
- `/memory rollback <id> <version> [--reason ...]`；
- 只允许显式版本号，不允许“猜一个旧版本”。
- 当前 agent tool 已接通：默认不注册，需 `memory.tools.allowRollback=true`；执行要求 `confirm=true`、`expected_revision`、当前 session 精确所有权，且当前/目标快照必须非私有、非归档、非删除、无 secret。`revert_supersede` 只有显式传 true 且 successor 也属于当前 session、保持 active、非敏感时才放行；更完整的 admin supersede 回滚仍走受控 API/CLI。

**测试**

- update/correct/supersede/delete/restore 每一步都有版本；
- rollback 到任意历史版本生成新版本，不修改旧版本；
- 并发 revision 冲突返回 409；
- 自动 Consolidation supersede 可回滚，后继状态和 summary/FTS 同步；
- 非 admin/owner 拒绝；
- import/export 后版本仍可读。

### 6.4 G3-4 Temporal 写入

1. 创建记录：
   - `valid_from = now`（若未显式给）；
   - `valid_until = NULL`。
2. 语义变更（text/kind/关键事实）：
   - 旧状态快照写入 `memory_versions`，快照 `valid_until=now`；
   - 主记录 `valid_from=now`，版本递增。
3. 仅 tags/pinned/metadata 变更：
   - 写版本，但不重置 `valid_from`；
   - 在 change_type/metadata 中标记 `non_semantic`。
4. supersede/correct/delete：
   - 旧记录 `valid_until=now`；
   - 新记录 `valid_from=now`；
   - 删除保留 `deleted_at`，历史查询仍可按 valid 区间读取。
5. 历史查询基础方法（G5 暴露 API）：
   ```go
   SearchRecords(SearchQuery{AsOf: &t, Statuses: []Status{...}})
   ```
   条件：`valid_from <= as_of AND (valid_until IS NULL OR valid_until > as_of)`。

### 6.5 G3-5 冲突与自动 supersede 审计

- Consolidation 自动 supersede 必须写：
  - 旧 record ID + version；
  - 新 record ID + version；
  - 冲突原因、confidence、source IDs；
  - `change_type='supersede'`。
- 高风险事实反转（高 confidence 冲突、policy/procedure、跨 scope）在 G4 前：
  - 不自动 supersede；
  - 写 `memory_conflict` 诊断/audit，要求人工 correction；
  - G4 后统一转为 Dreaming patch 审核。
- 测试：
  - 自动 supersede 可追溯；
  - 高风险反转不静默覆盖；
  - rollback 能恢复旧事实并失效后继；
  - 语义矛盾的改述在 G3 至少被标记，不要求完整实体解析（G5 完成）。

### 6.6 G3 退出条件

- [ ] `memory_versions` 为 append-only，所有变更路径写版本；
- [ ] rollback API/tool/CLI 可用，含并发冲突和权限测试；
- [ ] 自动 supersede 可撤销，旧/新 valid 区间正确；
- [ ] create/update/correct/supersede/delete/restore 的版本测试通过；
- [ ] 历史事实基础查询可在 store 层按 `as_of` 返回；
- [ ] 高风险自动 supersede 不静默发生；
- [ ] FTS/summary/usage 与版本操作同步。

## 7. G3.5：Core Memory 与分类预算（G4 进入条件）

对应缺口：`GAP-P1-06`。

### 7.1 数据模型

```sql
CREATE TABLE memory_core (
    id             TEXT PRIMARY KEY,
    tenant_id      TEXT NOT NULL DEFAULT '',
    project_key    TEXT NOT NULL DEFAULT '',
    scope_kind     TEXT NOT NULL DEFAULT 'project',
    scope_key      TEXT NOT NULL DEFAULT '',
    content        TEXT NOT NULL,
    version        INTEGER NOT NULL DEFAULT 1,
    token_estimate INTEGER NOT NULL DEFAULT 0,
    status         TEXT NOT NULL DEFAULT 'candidate', -- candidate|active|stale|archived
    source_ids_json TEXT NOT NULL DEFAULT '[]',
    generated_by   TEXT NOT NULL DEFAULT '',
    approved_by    TEXT NOT NULL DEFAULT '',
    created_at     TEXT NOT NULL,
    updated_at     TEXT NOT NULL,
    UNIQUE(tenant_id, scope_kind, scope_key, version)
);

CREATE UNIQUE INDEX memory_core_one_active
  ON memory_core(tenant_id, scope_kind, scope_key)
  WHERE status = 'active';
```

- 每个 scope 只能有一个 active core；
- Core 是“稳定、少量、可审核”的摘要，不是 transcript 或 summary 的副本；
- Core 更新保留版本/来源；删除用 status archived，不物理删除；
- Core 不进入 `memory_entries`，避免与普通记录混淆。

### 7.2 生成与审核

1. 新增 job `core_rebuild`（priority 低于 extraction，高于普通 reindex）：
   - 输入：active summary、pinned stable records、近期高 usage 记录；
   - 输出：candidate core + source IDs；
   - 生成失败不改变当前 active core。
2. G3.5 阶段：
   - 自动生成 candidate；
   - 激活必须 admin 显式操作；
   - 不直接由模型覆盖 active core。
3. G4 后：
   - Core 变更可作为低风险 Dreaming patch 自动批准，但删除/大幅改写仍需审核。
4. API：
   - `GET /api/runtime/memory/core?workspace_path=...`
   - `POST /api/runtime/memory/core/rebuild`
   - `POST /api/runtime/memory/core/{id}/activate`
   - `POST /api/runtime/memory/core/{id}/archive`
5. 工具/CLI：
   - `memory_core_get`；
   - `/memory core`、`/memory core rebuild`。

**当前实施状态（2026-11-23 增量）**：Core 表、读取/管理 API、只读工具、context stable 注入与 `/memory core` 管理入口已有实现；CLI 与 admin-only `POST /api/runtime/memory/core/rebuild` 可同步触发一次 project-scoped、有界的 `core_rebuild` job，并在 worker lease/事务校验后创建 pending candidate，不会自动激活。配置 `memory.core.rebuild.{enabled,interval,maxRecords,maxUsageRows,tokenBudget}` 已接入 `RuntimeConfig` 和共享 runtime factory，并对上限进行校验；`enabled=true` 时已有 maintenance owner lease loop 会按 interval 调用 bounded Core worker，默认关闭。输入仍只覆盖 gated active summary、有限 pinned records，以及最近 90 天最多 500 条 usage rows 中可验证的 active/fresh 非 pinned 记录。仍没有 Core job 独立状态面、分类预算和完整 usage/exposure 闭环。不得把这项增量写成 B5/G3.5 完成。

### 7.3 分类预算

新增配置（保持默认兼容）：

```yaml
memory:
  injection:
    tokenBudget: 600            # 现有默认不变
    budgets:
      core: 0.20
      task: 0.15
      retrieved: 0.40
      procedure: 0.15
      reference: 0.10
    extendedPreset: false
```

- 默认仍使用现有 600 总预算；只有显式启用 extended preset 才使用 5k–12k 级预算；
- 分类预算按比例分配，剩余预算可回流到 retrieved；
- Core 始终优先于普通 retrieved；pinned/manual 语义保持；
- 预算不足时按固定顺序裁剪：reference → procedure → retrieved → task → core；
- 所有裁剪写 recall trace/why-not。

### 7.4 Context Builder 接入

- `contextmgr/project_memory.go` 增加 core/task/procedure/reference 分区；
- stable prefix：core + project summary；
- tail recall：task + retrieved + procedure/reference；
- 保持现有 stable/recall stage split 和 prompt bytes 兼容；
- `memory_core_get` 与注入使用同一来源，避免两套内容。

### 7.5 测试与退出

- Core 生成/激活/归档；
- 每个 scope 单 active；
- 分类预算硬上限与裁剪顺序；
- Core 不因 summary 失效而注入过期内容；
- Project 权重测试：Coding Agent 场景 project/core 优先于无关全局偏好；
- `mode=off`、默认 600 预算行为不变。

退出条件：

- [ ] Core 表/API/tool/CLI 可用；
- [ ] Core 生成不直接覆盖 active；
- [ ] 分类预算可配置且默认兼容；
- [ ] 注入总 Token 硬上限测试通过；
- [ ] Core/summary/pinned 去重和失效规则明确。

## 8. G4：Episode 与 Dreaming Patch

对应缺口：`GAP-P1-01`、`GAP-P1-02`、`GAP-P2-01`（dream tool 部分）、`GAP-P2-04`（证据/ignored 持久化）。

### 8.1 G4-1 Episode 数据模型

```sql
CREATE TABLE memory_episodes (
    id                 TEXT PRIMARY KEY,
    memory_id          TEXT NOT NULL,
    tenant_id          TEXT NOT NULL DEFAULT '',
    project_key        TEXT NOT NULL DEFAULT '',
    task_id            TEXT NOT NULL DEFAULT '',
    session_id         TEXT NOT NULL DEFAULT '',
    problem            TEXT NOT NULL,
    actions_json       TEXT NOT NULL DEFAULT '[]',
    result             TEXT NOT NULL DEFAULT '',
    failure_reason     TEXT NOT NULL DEFAULT '',
    outcome            TEXT NOT NULL, -- success|failed|partial|abandoned
    started_at         TEXT,
    ended_at           TEXT,
    source_event_ids_json TEXT NOT NULL DEFAULT '[]',
    created_at         TEXT NOT NULL,
    UNIQUE(memory_id)
);
```

- `memory_entries.kind` 增加 `episodic`、`reflective`；
- Episode 必须有 source event IDs；缺来源不允许自动晋升 active；
- Episode 的 text 是可读摘要，结构化字段是事实源；
- 失败 Episode 保留 `outcome=failed` 和 `failure_reason`，不得被 Consolidation 晋升为有效 procedure；
- `working` 状态边界：**不新增持久化 working memory**；工作态继续由 `contextmgr` 的 task state 承担，Memory Engine 只持久化完成后的 Episode/Reflective。该边界写入 `03/04` 文档。

### 8.2 G4-2 Task Completion trigger

1. `memoryruntime/types.go` 增加 `TriggerTaskCompletion`，但默认不启用。
2. 定义事件契约：
   ```go
   type TaskCompletionEvent struct {
       TenantID      string
       ProjectKey    string
       SessionID     string
       TaskID        string
       Outcome       string
       Problem       string
       Actions       []string
       Result        string
       FailureReason string
       SourceEventIDs []string
       StartedAt     time.Time
       EndedAt       time.Time
   }
   ```
3. 事件来源：
   - AgentControl task terminal transition 成功提交后发出；
   - 若当前任务生命周期没有 durable event，先补事件源，不允许从 transcript 猜任务完成；
   - 事件投递失败进入 outbox/retry，不直接丢。
4. 消费：
   - 生成 Episode candidate；
   - 经过 source 完整性校验；
   - consolidation 只把有来源的 Episode 晋升 active；
   - 新会话可按 project/task scope 召回 Episode。
5. 测试：
   - 成功/失败/部分完成；
   - 缺 source IDs 拒绝晋升；
   - 失败方案保留为 warning；
   - 重启/重复事件幂等；
   - 跨 project/session 不串。

### 8.3 G4-3 Reflective 与 negative learning

- Reflective 由 Dreaming 从多个 Episode 归纳，不直接写 active；
- 至少 2 个有来源 Episode 才能提出 reflective patch；
- 失败方案：
  - 作为 `pitfall`/`episodic(outcome=failed)` 保留；
  - 不自动变成 `workflow` 或 procedure；
  - 若后续成功方案覆盖失败方案，必须保留失败证据和原因；
- 测试：失败方案不会被 `PromoteCandidate` 晋升为有效 procedure；成功/失败冲突进入 patch 审核。

### 8.4 G4-4 Patch 模型

```sql
CREATE TABLE memory_patches (
    id                TEXT PRIMARY KEY,
    tenant_id         TEXT NOT NULL DEFAULT '',
    project_key       TEXT NOT NULL DEFAULT '',
    scope_kind        TEXT NOT NULL DEFAULT 'project',
    scope_key         TEXT NOT NULL DEFAULT '',
    operation         TEXT NOT NULL, -- add|update|merge|supersede|delete|move_scope|reindex
    target_ids_json   TEXT NOT NULL DEFAULT '[]',
    proposed_content  TEXT NOT NULL DEFAULT '',
    proposed_metadata_json TEXT NOT NULL DEFAULT '{}',
    reason            TEXT NOT NULL DEFAULT '',
    evidence_json     TEXT NOT NULL DEFAULT '[]',
    risk_level        TEXT NOT NULL, -- low|medium|high
    status            TEXT NOT NULL DEFAULT 'proposed', -- proposed|approved|rejected|applied|rolled_back|failed
    expected_revisions_json TEXT NOT NULL DEFAULT '{}',
    created_by        TEXT NOT NULL DEFAULT '',
    reviewed_by       TEXT NOT NULL DEFAULT '',
    review_note       TEXT NOT NULL DEFAULT '',
    created_at        TEXT NOT NULL,
    reviewed_at       TEXT,
    applied_at        TEXT,
    rollback_version_ids_json TEXT NOT NULL DEFAULT '[]'
);
```

```sql
CREATE TABLE memory_dream_runs (
    id            TEXT PRIMARY KEY,
    tenant_id     TEXT NOT NULL DEFAULT '',
    project_key   TEXT NOT NULL DEFAULT '',
    status        TEXT NOT NULL, -- queued|running|succeeded|failed|partial
    window_start  TEXT,
    window_end    TEXT,
    input_count   INTEGER NOT NULL DEFAULT 0,
    patch_count   INTEGER NOT NULL DEFAULT 0,
    token_cost    INTEGER NOT NULL DEFAULT 0,
    error         TEXT NOT NULL DEFAULT '',
    started_at    TEXT,
    finished_at   TEXT,
    created_at    TEXT NOT NULL
);
```

**风险分级**

| 风险 | 操作 | 默认策略 |
|---|---|---|
| low | 新增无冲突 reference、重复合并、索引修复、Core 小幅更新 | 自动批准/应用，保留审计 |
| medium | 更新已有内容、合并冲突事实、晋升 candidate | 需审核，或按项目策略自动+抽样 |
| high | 删除、跨 scope 移动、policy/procedure、trust 提升、高 confidence 事实反转 | 必须人工审核 |

### 8.5 G4-5 Dreaming pipeline

按参考设计的阶段拆分 job，禁止模型直接写 `memory_entries`：

1. `dream_replay`：
   - 选择窗口：最近 sessions/episodes、stale/conflict、未处理 patch；
   - 输入只读，生成 run 记录；
   - 不消费未冻结 transcript。
2. `dream_analyze`：
   - 模型输出结构化 patch proposals；
   - 每个 proposal 必须带 target IDs、evidence/source IDs、reason、risk 建议；
   - 输出校验失败则 run failed，不写 patch。
3. `dream_validate`：
   - schema、scope、trust、target revision、source 可访问性、风险策略；
   - 低风险进入 auto-apply；中/高风险进入 `proposed`；
   - 任何 scope 不匹配直接拒绝。
4. `dream_apply`：
   - 事务内校验 expected revisions；
   - 写 `memory_versions`、`memory_events`、FTS；
   - 更新 patch 状态 `applied`；
   - 失败保留原 patch 和错误。
5. Core/Summary 更新只通过 patch 或 `core_rebuild`，不由 Dreaming 直接覆盖。

### 8.6 G4-6 审核、应用、拒绝、回滚

API：

- `GET /api/runtime/memory/patches?status=&risk=`
- `POST /api/runtime/memory/patches/{id}/approve`
- `POST /api/runtime/memory/patches/{id}/reject`
- `POST /api/runtime/memory/patches/{id}/apply`
- `POST /api/runtime/memory/patches/{id}/rollback`
- `POST /api/runtime/memory/dreams/run`
- `GET /api/runtime/memory/dreams`

工具/CLI：

- `memory_dream_run`（admin）；
- `memory_patch_list` / `memory_patch_review`（admin）；
- `/memory dream run`、`/memory patches`、`/memory patch approve|reject|apply|rollback`。

回滚：

- 使用 `memory_versions` 恢复；
- 若 patch 应用后目标又被修改，返回冲突，要求先处理后续版本；
- 回滚本身写新版本和 audit，不删历史。

### 8.7 G4 测试矩阵

| 测试 | 预期 |
|---|---|
| Task completion → Episode | 有 source IDs、problem/actions/result/time |
| 缺 source 的 Episode | 拒绝晋升 |
| 失败方案 | 保留 warning，不晋升 procedure |
| Dreaming duplicate merge | 合并后来源保留 |
| 高置信事实反转 | 生成 high risk patch，不自动覆盖 |
| Patch approve/apply | 版本、audit、FTS 同步 |
| Patch reject | 数据不变 |
| Patch rollback | 恢复旧版本，后继处理明确 |
| 跨 scope patch | 拒绝 |
| 低风险 patch | 自动应用且可回滚 |
| 模型输出非法 | run failed，无数据写入 |

### 8.8 G4 退出条件

- [ ] Task Completion 可生成有来源 Episode；
- [ ] Episode 成功/失败/部分完成测试通过；
- [ ] Dreaming 至少生成一个可审 patch，支持 approve/reject/apply/rollback；
- [ ] 低风险自动、高风险审核；
- [ ] 失败方案不被晋升为有效 procedure；
- [ ] 来源、版本、audit、FTS 同步；
- [ ] Dreaming 默认关闭或 shadow，不直接影响默认注入。

## 9. G5：混合检索、Trust 与评测

对应缺口：`GAP-P1-04`、`GAP-P1-05`、`GAP-P1-07`、`GAP-P1-08`、`GAP-P2-01`（feedback/core/dream 工具）、`GAP-P2-05`。

G5 拆成三个子包，全部通过才算 G5 关闭。

### 9.1 G5-R：Entity、Temporal、RRF 与可选 Vector

#### 9.1.1 Entity / Alias / Relation

```sql
CREATE TABLE memory_entities (
    id             TEXT PRIMARY KEY,
    tenant_id      TEXT NOT NULL DEFAULT '',
    project_key    TEXT NOT NULL DEFAULT '',
    scope_kind     TEXT NOT NULL DEFAULT 'project',
    scope_key      TEXT NOT NULL DEFAULT '',
    canonical_name TEXT NOT NULL,
    entity_type    TEXT NOT NULL,
    aliases_json   TEXT NOT NULL DEFAULT '[]',
    metadata_json  TEXT NOT NULL DEFAULT '{}',
    created_at     TEXT NOT NULL,
    updated_at     TEXT NOT NULL,
    UNIQUE(tenant_id, scope_kind, scope_key, entity_type, canonical_name)
);

CREATE TABLE memory_entity_links (
    id          TEXT PRIMARY KEY,
    memory_id   TEXT NOT NULL,
    entity_id   TEXT NOT NULL,
    relation    TEXT NOT NULL,
    confidence  REAL NOT NULL DEFAULT 0,
    created_at  TEXT NOT NULL,
    UNIQUE(memory_id, entity_id, relation)
);
```

- aliases 支持项目名、函数名、错误码、服务名；
- entity 抽取先规则化（路径/标识符/大写常量/已知项目名），再考虑模型；
- 实体检索必须带 scope 过滤；
- 删除/纠正记忆时同步 entity link。

#### 9.1.2 Query understanding

新增 `memorystore.QueryIntent`：

```go
type QueryIntent struct {
    RawText       string
    Tokens        []string
    ExactTerms    []string // 路径/错误码/函数名/版本号
    EntityNames   []string
    TimeRange     *TimeRange
    RequestedKinds []string
    ScopeHints    []string
}
```

- 第一版使用确定性规则，不依赖模型；
- 检测反引号/引号内文本、路径、`E_*`、`*Error`、`vN.N.N`、日期范围；
- 将 intent 转为 FTS query、entity filter、temporal filter；
- 保持原始 query 用于 explain。

#### 9.1.3 Temporal as_of

- HTTP/API 增加 `as_of`、`time_range`；
- `SearchQuery.AsOf` 使用 G3 的 valid 区间；
- 默认 current：`valid_until IS NULL OR valid_until > now`，status active；
- historical：返回 valid 区间覆盖 as_of 的记录，可包含 superseded/invalidated；
- 响应标明 `temporal_mode=current|as_of`；
- 测试新旧事实、改述矛盾、时间范围、删除后历史可见性。

#### 9.1.4 RRF / rerank

- 召回源：
  1. FTS/BM25；
  2. entity/alias；
  3. temporal/scope；
  4. semantic（optional，默认关闭）。
- 融合：
  ```
  score = Σ weight_i / (k + rank_i)
  ```
  `k`、权重可配置；默认值写入常量并测试锁定；
- 融合后再应用 freshness/trust/usage/recency 轻量加权；
- reranker 可选，必须可关闭；关闭时结果可复现；
- `explain=true` 返回每路 rank/score/selected/dropped；
- 任何一路失败不影响其他路，但必须记录 `degraded_sources`。

#### 9.1.5 Vector（optional）

- 定义 `EmbeddingProvider` 接口和 `memory_embeddings` 表；
- 默认关闭；未配置 provider 时不影响 FTS/entity/temporal；
- vector 结果必须带 model/version，维度不匹配时拒绝查询；
- 不把 vector 作为 G5 关闭的必要条件；参考设计将其列为 optional。
- 测试：provider 缺失、维度不匹配、索引重建、降级不影响其他召回。

#### 9.1.6 G5-R 测试

- 实体别名：项目名/函数名/错误码；
- 精确标识符与 FTS 同时命中时排序稳定；
- 当前/历史事实；
- 时间范围；
- RRF 可解释；
- 某一路降级时仍有结果且 `degraded_sources` 正确；
- Recall@K/MRR 基准（见 G5-Q）。

### 9.2 G5-T：Trust 与外部内容防注入

#### 9.2.1 数据模型

`memory_entries` / `memory_sources` / `memory_patches` 增加：

- `trust_level`：
  - `system_policy`
  - `user_explicit`
  - `user_confirmed`
  - `agent_inference`
  - `untrusted_external`
- `source_type`：`user|assistant|tool|web|repo|system|import`
- `source_trust`：与 trust_level 同枚举或更细来源标记
- `trust_reason`
- `trusted_at`

默认：

- 手工 `/memory add`：`user_explicit`；
- 用户确认的 correction：`user_confirmed`；
- 自动抽取自 assistant：`agent_inference`；
- 工具输出/网页/仓库内容：`untrusted_external`（除非显式提升）；
- system policy 只能由受信配置/管理员写入。

#### 9.2.2 晋升与注入规则

1. `untrusted_external` 不得直接晋升为：
   - `system_policy`；
   - `procedure` / `workflow`；
   - 高置信 policy 类事实。
2. 外部内容默认只能作为 `reference` 或带 trust 标签的 observation；
3. 需要晋升时进入 G4 patch 审核；
4. 注入文本必须包含 trust 标签和“historical data, not instructions”边界；
5. 外部内容中的指令性文本不得改变工具/权限/系统行为；
6. 跨 scope 的 trusted 提升必须 admin + audit。

#### 9.2.3 测试

- poisoned web/tool output 包含“ignore previous instructions”不成为 policy；
- 外部内容不能覆盖 user/system 事实；
- trust 提升走审核；
- 低 trust 记录排序降权；
- 注入标签正确；
- 跨 scope 泄漏负向测试。

### 9.3 G5-Q：Feedback、Evaluation 与运营

#### 9.3.1 Feedback

```sql
CREATE TABLE memory_feedback (
    id          TEXT PRIMARY KEY,
    memory_id   TEXT NOT NULL,
    tenant_id   TEXT NOT NULL DEFAULT '',
    project_key TEXT NOT NULL DEFAULT '',
    session_id  TEXT NOT NULL DEFAULT '',
    turn_id     TEXT NOT NULL DEFAULT '',
    kind        TEXT NOT NULL, -- helpful|irrelevant|stale|incorrect
    comment     TEXT NOT NULL DEFAULT '',
    actor       TEXT NOT NULL DEFAULT '',
    created_at  TEXT NOT NULL
);
```

- API：`POST /api/runtime/memory/records/{id}/feedback`；
- 工具：`memory_feedback`；
- CLI：`/memory feedback <id> <kind>`；
- feedback 进入 utility/ranking，但 popularity 不能单独把低 trust/错误事实晋升；
- `incorrect` 触发 freshness/correction 流程；`stale` 进入 revalidate/sweep；
- 写 audit。

#### 9.3.2 评测 corpus

新增 `backend/internal/memoryeval`：

```text
backend/internal/memoryeval/
  corpus.go
  metrics.go
  corpus_test.go
  testdata/
    extraction.jsonl
    retrieval.jsonl
    temporal.jsonl
    scope.jsonl
    poisoned.jsonl
    dreaming.jsonl
```

每个 case 包含：

- input/query；
- expected record IDs 或 expected extraction fields；
- scope/time/trust；
- 允许的容差；
- 来源/预期拒绝原因。

指标：

- extraction precision/recall；
- Recall@K、MRR；
- conflict/stale rate；
- helpful usage；
- retrieval latency（p50/p95）；
- write/Dreaming token cost；
- token savings；
- repeat exploration；
- task success delta（需要运行时/任务结果接线，允许先 shadow 采集）。

#### 9.3.3 Metrics/API

- `/api/runtime/memory/metrics` 增加质量/延迟/成本字段；
- 保持旧计数兼容；
- 指标必须带 scope 和时间窗口；
- 评测结果写入 `docs/agent-memory/evidence/` 或 CI artifact，不提交大体积原始输出。

#### 9.3.4 Shadow 与上线

1. 先在单 project、`mode=shadow` 运行：
   - 记录 memory-on/off 的检索结果、注入 token、重复探索、任务结果；
   - 不改变默认注入。
2. 满足门槛后逐项目 `on`：
   - 每项目独立开关；
   - 可随时回退 shadow/off；
   - 回退不删除数据。
3. 运营报告：
   - backlog；
   - extraction 成功率；
   - recall 质量；
   - trust 拒绝/审核数量；
   - token/cost；
   - Dreaming patch 审核/回滚。

#### 9.3.5 G5-Q 退出条件

- [ ] 固定 corpus 可重复运行；
- [ ] Recall@K/MRR/precision/recall/latency/cost 有基线；
- [ ] scope/temporal/negative/poisoned/Dreaming 专项测试通过；
- [ ] feedback 进入排序且有审计；
- [ ] shadow 结果支持继续/停止决策；
- [ ] 指标 API 和运营报告可查。

## 10. P2 缺口落点

| P2 缺口 | 落点 | 说明 |
|---|---|---|
| `GAP-P2-01` API/Tool 不完整 | G1 `memory_search` 过滤；G3 `memory_rollback`；G3.5 `memory_core_get`；G4 `memory_dream_run`/patch；G5 `memory_feedback`；G1 `memory_pin/unpin` | 所有新工具带 scope/policy/audit |
| `GAP-P2-02` CLI list/search/status 不一致 | G0 status；G1 list/search | 默认查全部 active/candidate，manual 可选过滤 |
| `GAP-P2-03` dead-letter/priority/timeout | G0-2 | 使用现有字段，补状态/迁移/重放 API |
| `GAP-P2-04` schema/证据不完整 | G1 title/summary；G3 canonical/superseded/versions；G4 episode/patch/source quote/ignored diagnostics | `memory_sources.quote` 保存原始 evidence snippet |
| `GAP-P2-05` 默认配置偏离 | G5 文档/onboarding preset | 不强制改变保守默认，避免升级突变 |

### 10.1 `memory_sources.quote` 与 ignored 诊断

- 新增 source 字段（或独立 evidence 表）：
  - `quote` 保存原始 evidence snippet，不是 memory text；
  - `source_type`、`source_trust`、`source_observed_at`；
- 抽取 ignored reasons 落库：
  - 表 `memory_extraction_diagnostics` 或 output receipt payload；
  - 字段：job_id、session_id、turn_id、reason、source_ids、created_at；
- `/memory status` / jobs / audit 可查；
- 测试：source quote 不等于 memory text；ignored 原因在 job 完成后仍可查。

## 11. 文件落点与改动矩阵

> 新文件名是建议名；实施时如与既有分支冲突，可在同一 package 内调整，但职责边界不能合并成“大文件”。

### 11.1 memorystore

| 文件 | 改动 |
|---|---|
| `backend/internal/memorystore/v2_schema.go` | 追加 v17+ additive migrations：title/summary、scope 字段、versions、core、patches、episodes、feedback、trust、diagnostics；不把 FTS5 DDL 放进核心 migration |
| `backend/internal/memorystore/v2_types.go` | 扩展 `StatusReport`、`JobDiagnostic`、`Record`、`RecordFilter`、`SearchQuery`、`ScopeContext`、`Version`、`CoreMemory`、`Patch`、`Episode`、`Feedback` |
| `backend/internal/memorystore/v2_helpers.go` | `Status` 聚合、统一 list/search/filter、版本/scope 查询辅助 |
| `backend/internal/memorystore/v2_store.go` | `SearchRecords`、注入候选裁剪、scope filter、manual-only 移除 |
| `backend/internal/memorystore/v2_runtime.go` | job dead-letter/priority/timeout/retry/replay |
| `backend/internal/memorystore/v2_capture_materialization.go` | job/scope 快照、幂等身份扩展 |
| 新增 `v2_fts.go` | FTS5 ensure/probe/rebuild/trigger/fallback，参考 knowledge 层 |
| 新增 `v2_scope.go` | scope context、filter builder、grants、canonicalization |
| 新增 `v2_versions.go` | versions append-only、rollback、版本读取 |
| 新增 `v2_temporal.go` | valid_from/valid_until、as_of 查询基础 |
| 新增 `v2_core.go` | Core Memory CRUD/rebuild/activate |
| 新增 `v2_dream.go` | patches/dream runs 持久化、apply/reject/rollback |
| 新增 `v2_episode.go` | Episode 持久化与 source 完整性 |
| 新增 `v2_feedback.go` | feedback 写入、utility 更新 |
| 新增 `v2_trust.go` | trust level/source trust 过滤与晋升规则 |
| 新增 `v2_eval_metrics.go` | Recall@K/MRR/latency/cost/token savings 指标 |

### 11.2 memoryruntime

| 文件 | 改动 |
|---|---|
| `backend/internal/memoryruntime/types.go` | `TriggerTaskCompletion`、Episode/Dreaming/Patch 类型、scope 传递 |
| `backend/internal/memoryruntime/runtime.go` | candidate 写入 scope/trust/version；kind 扩展；ignored 诊断 |
| `backend/internal/memoryruntime/extractor.go` | source type/trust、episode/reflective、ignored reasons |
| `backend/internal/memoryruntime/consolidate.go` | 版本、valid 区间、risk 识别、失败方案不晋升、Dreaming patch 衔接 |
| `backend/internal/memoryruntime/turn_digest.go` | capture extraction drain、active session trigger、status 统计 |
| `backend/internal/memoryruntime/capture_materialization.go` | scope 快照、job 幂等、active session 安全边界 |
| 新增 `backend/internal/memoryruntime/episode.go` | Task Completion → Episode |
| 新增 `backend/internal/memoryruntime/dreaming.go` | Replay/Analyze/Validate/Apply |
| 新增 `backend/internal/memoryruntime/task_completion.go` | task completion event 消费/幂等 |

### 11.3 memoryassembly / contextmgr / wiring

| 文件 | 改动 |
|---|---|
| `backend/internal/memoryassembly/maintenance.go` | capture extraction 独立 drain、interval/batch、LastMaintenanceAt/LastExtractionAt |
| `backend/internal/memoryassembly/*` | per-workspace runtime registry、runtime 状态 |
| `backend/internal/api/runtimeapi/memory_runtime_wiring.go` | 多 workspace 创建/路由/错误可见 |
| `backend/internal/memoryruntime/turn_digest_sink.go` | mismatch 不再静默跳过；使用 registry |
| `backend/internal/contextmgr/project_memory.go` | core/task/retrieved/procedure/reference 预算、trust 标签、FTS 候选 |
| `backend/internal/contextmgr/manager.go` | 分类预算配置接入、stable/recall 兼容 |
| `backend/internal/config/manager.go` | 新配置项、默认值、校验、onboarding preset |

### 11.4 API / tools / CLI

| 文件 | 改动 |
|---|---|
| `backend/internal/api/runtimeapi/memory_handlers.go` | status/jobs/retry/dead-letter、search、versions/rollback、core、patches/dreams、feedback/metrics、trust |
| `backend/internal/api/runtimeapi/handler.go` | 新路由；保持旧路由兼容 |
| `backend/internal/toolkit/tools/memory_tool_defs.go` | `memory_feedback`、`memory_pin/unpin`、`memory_core_get`、`memory_rollback`、`memory_dream_run`、patch 工具、search 过滤 |
| `backend/internal/toolkit/tools/memory_tools.go` | 工具实现、scope/policy/audit |
| `backend/cmd/aicli/commands/chat_memory_command.go` | list/search/status 统一、jobs/core/feedback/rollback/dream 子命令 |
| `backend/cmd/aicli/commands/chat_memory_document.go` | structured status/search/core 输出 |
| `backend/internal/memoryeval/*` | corpus、metrics、专项测试 |

### 11.5 测试文件

| 门禁 | 重点测试 |
|---|---|
| G0 | `v2_status_test.go`、`memory_handlers_test.go`、`chat_memory_v2_command_test.go`、`capture_worker_test.go`、`auto_e2e_test.go`、`e2e_sqlite_test.go` |
| G1 | `v2_fts_test.go`、`v2_search_test.go`、`v2_store_test.go`、`memory_tools_test.go`、CLI/HTTP parity |
| G2 | `v2_scope_test.go`、`memory_runtime_wiring_test.go`、`turn_digest_sink_test.go`、purge/export/import isolation |
| G3 | `v2_versions_test.go`、`v2_temporal_test.go`、`v2_corrections_test.go`、`consolidate_test.go` |
| G3.5 | `v2_core_test.go`、`contextmgr/project_memory_test.go`、预算测试 |
| G4 | `episode_test.go`、`dreaming_test.go`、`v2_dream_test.go`、patch apply/rollback E2E |
| G5 | `memoryeval/*_test.go`、`v2_entity_test.go`、`v2_trust_test.go`、`v2_feedback_test.go`、metrics API |

## 12. 测试与证据

### 12.1 每批必须执行的命令

```bash
cd backend
gofmt -w <本批修改的 Go 文件>
go test ./internal/memorystore ./internal/memoryruntime ./internal/memoryassembly \
  ./internal/contextmgr ./internal/toolkit/tools -count=1
go test ./internal/api/runtimeapi -count=1
go test ./cmd/aicli/commands -run '<本批新增的定向测试名>' -count=1
cd ..
git diff --check
```

按门禁追加：

```bash
# G0
go test ./internal/memoryruntime -run 'TestG0|TestAutomaticMemoryEndToEndWithoutManualSeed|TestE2ESQLiteCanonicalRestartRecovery|TestRunPendingTurnCaptureReceiptsCommitsOneTurnAtomically' -count=1

# G1（FTS 环境）
go test ./internal/memorystore -run 'Test.*FTS|Test.*Search|Test.*CJK|Test.*Exact' -count=1

# G2
go test ./internal/memorystore ./internal/memoryruntime ./internal/api/runtimeapi -run 'Test.*Scope|Test.*Isolation|Test.*Workspace' -count=1

# G3
go test ./internal/memorystore ./internal/memoryruntime -run 'Test.*Version|Test.*Rollback|Test.*Temporal|Test.*Supersede' -count=1

# G4
go test ./internal/memoryruntime ./internal/memorystore -run 'Test.*Episode|Test.*Dream|Test.*Patch|Test.*NegativeLearning' -count=1

# G5
go test ./internal/memoryeval ./internal/memorystore ./internal/contextmgr -run 'Test.*Eval|Test.*Recall|Test.*Trust|Test.*Feedback|Test.*Entity' -count=1
```

### 12.2 证据文件格式

建议新增 `docs/agent-memory/evidence/`（执行时创建）：

```text
G0-runtime-closure-<date>.md
G1-retrieval-<date>.md
G2-scope-<date>.md
G3-versions-<date>.md
G4-dreaming-<date>.md
G5-quality-<date>.md
```

每个 evidence 文件记录：

- commit/工作区状态；
- schema version；
- 执行命令与结果摘要；
- 使用的 fixture/session/task ID；
- 正向/负向测试；
- 已知失败/skip 及原因；
- dogfood DB 前后计数（脱敏）；
- 门禁结论：`pass|blocked|fail`。

### 12.3 命令包测试说明

`cmd/aicli/commands` 整包包含交互式 provider/model、Skill、MCP、权限模式测试，受宿主环境影响。执行规则：

1. 每个门禁只跑与改动相关的 `-run` 测试；
2. 整包失败时，先看具体 `--- FAIL` 测试名，不把包级 `FAIL` 当作本门禁失败；
3. 只有与本次修改直接相关的测试失败才阻断门禁；
4. 既有环境敏感失败单独记录，不在本计划内顺手修复，除非它阻塞交付。

## 13. 发布、开关与回滚

### 13.1 配置开关

建议新增（名称可按现有 config 风格调整）：

```yaml
memory:
  search:
    fts:
      enabled: true
      tokenizer: trigram
      candidateLimit: 200
  scope:
    enabled: false          # 默认 project-only；配置后启用多级 scope
  versions:
    enabled: true           # 写入版本；关闭仅用于紧急回退
  core:
    enabled: false
  dreaming:
    enabled: false          # 先 shadow
  trust:
    enabled: true           # 外部内容默认 untrusted
  eval:
    enabled: true           # shadow 指标采集
```

默认策略：

- 不强制改变 `memory.mode=off`、`memory.tools.enabled=false`；
- FTS 不可用时自动 `like_fallback` 并显示状态；
- scope 默认 project-only，避免升级后旧数据不可见；
- versions 默认写，提供紧急关闭开关；
- Core/Dreaming 默认关闭；
- trust 默认启用，但只影响新写入/新抽取；旧数据 backfill 为 `agent_inference` 或 `user_explicit` 时需按来源保守判断；
- vector 默认关闭。

### 13.2 回滚策略

| 变更 | 回滚 |
|---|---|
| FTS 索引 | 删除 trigger + drop FTS 表，重建可恢复；不影响主表 |
| scope 字段 | 关闭 scope 读路径，退回 project filter；新列保留 |
| versions | 关闭 rollback API；版本表保留，不删除 |
| Core | 关闭注入/core 读路径；active core 保留 |
| Dreaming | 关闭 job 和 API；已应用 patch 通过 versions 回滚 |
| trust | 关闭 trust promotion 规则；旧记录保留标签 |
| eval | 停止采集；不影响读写 |

### 13.3 迁移演练

1. 复制 dogfood DB 到临时路径；
2. 在副本执行全部 migrations；
3. 校验 schema version、行数、scope backfill、FTS rebuild；
4. 运行 G0/G1 定向 E2E；
5. 通过后再对真实库执行；
6. 任何一步失败，停止并保留副本/日志。

## 14. 风险与应对

| 风险 | 影响 | 应对 |
|---|---|---|
| 当前 driver 无 FTS5 | G1 无法按 FTS5 关闭 | G1-0 阻断门；先解决 driver/build，或明确 blocked，不用 LIKE 冒充 |
| scope 迁移导致旧数据不可见 | 升级后“记忆消失” | 默认 project-only；backfill 校验；status 显示 scope |
| scope 过滤遗漏 | 跨用户/Agent 泄漏 | 统一 filter builder；负向测试；代码审查禁止裸 project_key |
| 版本写漏 | 无法回滚/审计 | 事务 helper；所有 mutation 测试；append-only trigger |
| rollback 覆盖后续修改 | 数据丢失 | expected_revision + 409；revert_supersede 显式开关 |
| Dreaming 模型直接写库 | 不可控变更 | patch-first；分析阶段只读；apply 事务校验 |
| 外部内容注入 | 信任边界失效 | 默认 untrusted；promotion 审核；poison 测试 |
| 评测指标不可复现 | 无法判断收益 | 固定 corpus；记录 model/prompt/version；CI artifact |
| 整包 CLI 测试环境噪声 | 误判门禁 | 定向测试；单独记录环境敏感失败 |
| dogfood 恢复破坏数据 | 不可逆 | 备份、dry-run、逐 job 记录、禁止伪造 active |

## 15. 必须先锁定的实施决策

这些不是开放式分析，而是开工前必须写入配置/常量/测试的决策：

| 决策 | 建议默认 | 记录位置 |
|---|---|---|
| FTS 方案 | 优先带 FTS5 的构建；否则 G1 blocked | `v2_fts.go` 注释 + status + G1 evidence |
| FTS tokenizer | trigram；短查询走 LIKE 补充 | `v2_fts.go` + corpus 测试 |
| scope 优先级 | task > session > agent > user > project | `v2_scope.go` + 负向测试 |
| 自动抽取默认 scope | project，附 owner/agent/session 来源 | `runtime.go` + config |
| rollback 冲突 | expected_revision 必须匹配；revert 显式 | `v2_versions.go` + API 测试 |
| Core 激活 | G3.5 阶段 admin 显式；G4 后可低风险自动 | `v2_core.go` + patch 规则 |
| Dreaming 自动批准 | 仅 low risk；medium/high 审核 | `dreaming.go` + patch 测试 |
| 外部内容 trust | 默认 untrusted_external | `v2_trust.go` + poison 测试 |
| vector | 默认关闭，optional | `EmbeddingProvider` + config |
| 默认预算 | 保持 600；extended preset 显式启用 | `config/manager.go` + context 测试 |

## 16. 执行顺序清单

### B0：基线与 G0 status 测试

- [ ] 为现有 `ExtractionJobs` 增加 store/API/CLI 测试；
- [ ] 修正 status 聚合字段；
- [ ] 不改变默认行为；
- [ ] 提交：`test(memory): cover extraction job status diagnostics`。

### B1：G0 闭环

- [ ] G0-1 status；
- [ ] G0-2 job dead-letter/priority/timeout/retry；
- [ ] G0-3 active session capture extraction；
- [ ] G0-4 E2E；
- [ ] G0-5 dogfood 恢复；
- [ ] 提交按“status / jobs / maintenance / e2e”拆分。

### B2：G1 检索

- [ ] G1-0 FTS 能力决策；
- [ ] G1-1 FTS schema/trigger/rebuild；
- [ ] G1-2 SearchRecords；
- [ ] G1-3 CLI/HTTP/tool 一致；
- [ ] G1-4 注入裁剪；
- [ ] G1 evidence。

### B3：G2 隔离

- [ ] schema/backfill；
- [ ] filter builder；
- [ ] runtime registry；
- [ ] API/tools/CLI 接入；
- [ ] 负向测试；
- [ ] G2 evidence。

### B4：G3 版本与回滚

- [ ] versions schema/trigger；
- [ ] 所有 mutation 写版本；
- [ ] rollback API/tool/CLI；
- [ ] temporal write；
- [ ] 冲突审计；
- [ ] G3 evidence。

### B5：G3.5 Core

- [x] Core schema、candidate/history API、memory_core_get 工具、`/memory core` 人工管理 CLI；
- [x] CLI 触发的有界、项目级合并 `core_rebuild` job 与待审核 candidate 生成（不自动激活）；
- [x] opt-in 后台 maintenance scheduler/worker（复用 owner lease 与 project-scoped `core_rebuild` job；默认关闭）；
- [x] admin-only、同步有界 rebuild HTTP API 与 Core rebuild YAML/runtime 参数接线（API 仍同步；后台周期调度默认关闭）；
- [ ] 分类预算；
- [x] context builder stable 接入（当前 shared total budget；分类预算另计）；
- [x] Core 预算/来源失效/总预算测试（分类预算测试仍待实现）。

### B6：G4 Episode + Dreaming

- [ ] Episode schema/trigger；
- [ ] Task Completion 事件；
- [ ] patch/dream runs；
- [ ] pipeline；
- [ ] review/apply/rollback；
- [ ] negative learning；
- [ ] G4 evidence。

### B7：G5 Retrieval/Trust/Eval

- [ ] entity/alias/relation；
- [ ] temporal as_of；
- [ ] RRF/rerank；
- [ ] vector optional；
- [ ] trust；
- [ ] feedback；
- [ ] eval corpus/metrics/shadow；
- [ ] G5-R/G5-T/G5-Q evidence。

## 17. MVP 验收项与工作包映射

| # | 参考设计 MVP 验收项 | 主要工作包 | 门禁 |
|---|---|---|---|
| 1 | 用户显式保存并在新会话召回 | 现有手动写入 + G0 注入回归 + G1 可见性 | G0/G1 |
| 2 | Task Completion 生成有证据的 Episode | G4-1/G4-2 | G4 |
| 3 | Semantic + 精确技术标识符检索 | G1 FTS/标识符 + G5-R entity/vector（optional） | G1/G5-R |
| 4 | Project scope 隔离 | 现有 project filter + G2 scope 回归 | G2 |
| 5 | 事实更新不擦除历史，可查当前/历史 | G3 versions/temporal + G5-R as_of | G3/G5-R |
| 6 | Memory context 遵守硬 Token 预算 | 现有预算 + G3.5 分类预算 | G3.5 |
| 7 | Dreaming 产生可审 Patch、去重保来源、可回滚 | G4-4/G4-5/G4-6 | G4 |
| 8 | 外部注入内容不能成为可信指令记忆 | G5-T trust/poison 测试 | G5-T |
| 9 | 用户可 inspect/correct/delete，删除级联 | G1 可见性 + G2 scope + G3 rollback/versions | G1/G2/G3 |
| 10 | Metrics 暴露抽取/检索/效用/成本/延迟 | G5-Q corpus/metrics/shadow | G5-Q |

## 18. 最终验收清单

- [ ] G0：dogfood `memory_entries > 0`，至少一个 v2 job succeeded，新会话注入自动抽取记忆；
- [ ] G1：FTS/BM25 在支持环境关闭；若 G1-0 判定为 blocked，则整体门禁不得标记通过；精确标识符/CJK、CLI/HTTP/tool 一致、注入 top-K；
- [ ] G2：跨 tenant/project/user/agent/session/task 负向隔离；
- [ ] G3：versions、rollback、supersede 可撤销、valid 区间；
- [ ] G3.5：Core + 分类预算，默认 600 预算兼容；
- [ ] G4：Episode + patch + 审核/拒绝/回滚 + negative learning；
- [ ] G5-R：entity/temporal/RRF，vector optional；
- [ ] G5-T：外部内容不能成为 trusted policy；
- [ ] G5-Q：Recall@K/MRR/cost/latency/task delta 可重复评测；
- [ ] 所有门禁 evidence 记录真实命令、真实输出和未关闭项；
- [ ] `git diff --check` 通过；
- [ ] 未回滚用户已有文档改动；README 仅追加本计划索引项。

## 19. 本轮执行记录（2026-11-22）

本轮按 B1 的 G0 范围落地了可在临时测试数据库中验证的代码和证据；本节只记录已经执行的工作，不把未覆盖项写成完成。

### 19.1 G0-1/G0-2：状态、job operator、priority 和 timeout

已落地：

- `memory_job_operator_audit` schema migration（v17）以及 operator audit；
- `ListJobDiagnostics`、`RequeueJob`、`DeadLetterJob`，包含 maintenance lease、purge fence、CAS 冲突和状态限制；
- phase1/summary/turn-rollup/phase2 等 job 的默认 priority；
- `deadline_at`、默认 `30m` job timeout、heartbeat 不延长 deadline、过期 lease reclaim，以及 deadline 超时后的 failed/dead-letter 迁移；
- admin-only HTTP retry/dead-letter API 和 CLI：
  `/memory jobs retry <id> --yes [--reason <text>]`、
  `/memory jobs dead-letter <id> --yes [--reason <text>]`；
- status/API/CLI 的诊断脱敏：非 admin 不返回 `last_error`、dead-letter 原因或 runtime/provider 错误；CLI 只显示 presence/摘要。

已覆盖的回归包括 succeeded job 不可 retry、CAS 冲突不写 audit、priority/deadline、lease/deadline 边界、admin 权限、`Mode=off` 不执行 operator，以及缺少 `--yes` 时只返回用法而不执行。

本轮继续补齐：

- 已实现 `ReplayDigest`：仅针对 receipt-backed orphan，先校验 canonical receipt identity、session generation、closed range、range hash、purge generation 和 maintenance lease；重复调用保持幂等并记录 operator audit；
- 已实现 bounded recovery dry-run 分类报告，并通过 admin-only `GET /api/runtime/memory/digests/replay-preview?limit=N` 暴露；端点要求已绑定 runtime，以便校验 canonical chat receipt，不回退到 memory-only 检查，也不向响应暴露 provider/runtime/database 原始错误；
- 已用临时 SQLite fixture 覆盖 dry-run no-write、orphan replay、重复 replay、queued/already-processed/missing-receipt/invalid-identity/stale-generation/lease-lost 等分类和 API 授权/脱敏；
- 已加入本地 CLI `/memory jobs replay-preview [limit]`：只在已绑定、且 ProjectKey 与当前 workspace 相同的 memory runtime 上执行 bounded dry-run；缺少 runtime、workspace 不匹配或分类错误均 fail closed，并隐藏内部诊断；
- 已加入 project-scoped、read-only 的 `Store.SummarizeJobs`：汇总 pending due/deferred、active lease、自动 retryable、running/deferred blocked、dead-letter、oldest pending age，并按 kind/status/priority 返回最多 32 个分组；分组标签做长度限制、空白规范化、Unicode 控制字符清理和 secret redaction，不返回 job key、session ID 或 raw diagnostics；
- 已加入 admin-only `GET /api/runtime/memory/jobs/summary`，沿用 memory admin authorization、mode-off 快速空结果和 workspace/store wiring；structured/local `/memory jobs` 同时展示同一（含 kind/status/priority 分组）汇总和 bounded recent job 摘要，汇总失败时保留原有列表降级；
- `v2_job_operator_test.go` 覆盖 backlog 分类、project isolation、32-group bound、oldest age、diagnostic 不泄露、group label bound/redaction；`memory_handlers_test.go` 覆盖 admin、mode-off 不建库和响应脱敏；CLI focused test 覆盖 `/memory jobs` 汇总展示；
- 后续安全复核发现 `kind` 可由持久化任务数据提供，故分组标签另过滤 Unicode 控制字符（包括 ANSI ESC）；`TestSummarizeJobsBoundsAndRedactsGroupLabels` 覆盖控制字符、secret 和 128-rune 限制；
- `Priority == 0` 仍兼容现有“未提供 priority / Dreaming priority”双重语义，未在本轮扩大重构。

### 19.2 G0-3：active-session capture extraction

已落地：

- durable capture receipt 的 materialization、canonical closed range、session generation 和 stale-source 校验继续作为唯一抽取边界；
- `RunPendingTurnCaptureReceiptsBounded` 提供显式 work limit；
- startup recovery、idle extraction 和独立的 periodic capture-extraction maintenance 均使用 bounded drain；
- `memory.maintenance.captureExtraction.enabled/batchSize/interval` 配置及校验已接入，默认值为 enabled、50、5 分钟；
- active session 不需要 `session_end`，但只有已冻结 receipt-backed turn/range 才能被消费；未冻结 transcript 不会被 recovery 读取。

`turn_threshold` 已有 bounded maintenance hook：它只统计 generation-scoped canonical capture receipt，校验当前 session generation 与 eligibility，再调用 receipt-backed extraction worker；不会从 live transcript 推断 turn，也不会读取未冻结范围。`turn_threshold_test.go` 覆盖阈值前不执行、冻结 receipt extraction、幂等重复运行和 generation mismatch；maintenance 测试覆盖 trigger gate。

`task_completion` 仍是窄边界 hook，不是 team durable event 闭环：`HandleTaskCompletionEvent` 校验调用方提供的 durable/source identity、project/session generation 和 bounded/redacted payload，然后仅委托给 `TaskCompletionConsumer`。目前 team 的 `task.completed` / `task.failed` durable event 没有完整携带当前 memory project、session incarnation/generation 等调用所需身份，且没有实际 consumer wiring；因此未从内存通知或 transcript 猜补身份。该 hook 不创建 Episode、不生成/提升 active memory，不能作为 G4 完成证据。

### 19.3 G0-4：临时 SQLite 端到端证据

新增/修改的证据测试覆盖：

- `TestG0ActiveSessionCaptureReceiptExtraction`：active session 在不结束的情况下处理冻结 receipt，断言不读取完整 transcript、source session/generation/turn 归属正确，并可在新 session 的 injection selection 中选中；
- `TestG0CrashRecoveryAfterCaptureReceipt`：重启后 receipt extraction 可恢复且重复执行幂等，generation mismatch 被 defer；
- `TestG0StatusExplainsBacklog`：status 能解释 pending extraction backlog；
- `TestAutomaticMemoryEndToEndWithoutManualSeed`：不手工 seed，验证 transcript → candidate → consolidation → active memory → recall 的链路。

### 19.4 G0-5 和真实 dogfood 库

G0-5 未执行。为遵守本轮门禁，未访问、未清理、未 reset、未覆盖：

```text
.aicli/memory/memory.db
```

因此没有真实 dogfood `memory_entries > 0`、v2 succeeded job 或新会话注入证据；G0 整体不能标记为关闭，最终验收清单中的 G0 保持未勾选。

### 19.5 G1 状态

新增的 `TestFTS5CapabilityProbe` 只在 `t.TempDir()` 创建的临时 memory 数据库上执行同一 driver 的能力探针；当前 SQLite driver 仍失败，原始错误为：

```text
no such module: fts5
```

因此 G1 保持 `blocked`，没有把 LIKE 降级冒充 FTS/BM25 完成；fallback 状态保留：

```text
search_backend=like_fallback
```

G1-1 及后续 FTS/SearchQuery/混合检索工作本轮未关闭。

### 19.6 可复现验证记录

以下命令均在临时测试数据库或测试 fixture 上执行并通过：

```text
cd backend
go test ./internal/memorystore -run 'Test(SummarizeJobs|FTS5CapabilityProbe)' -count=1 -v
=== RUN   TestFTS5CapabilityProbe
    v2_fts_capability_test.go:26: fts_unavailable: sqlite3: SQL logic error: no such module: fts5
--- PASS: TestFTS5CapabilityProbe
...
ok   ./internal/memorystore

go test ./internal/memorystore ./internal/api/runtimeapi -run 'Test(SummarizeJobs|MemoryAPIJobSummary)' -count=1
ok   ./internal/memorystore
ok   ./internal/api/runtimeapi

go test ./cmd/aicli/commands -run 'TestStructuredMemory(JobOperations|V2Commands|StatusAndJobsOmitRawDiagnostics|JobsModeOffDoesNotCreateDatabase)' -count=1
ok   ./cmd/aicli/commands

gofmt -w cmd/aicli/commands/chat_memory_v2_command_test.go
go test ./cmd/aicli/commands -run 'Test(G0|Memory|StructuredMemory|ChatMemory)' -count=1
ok   github.com/wwsheng009/ai-agent-runtime/cmd/aicli/commands 2.529s

go test ./internal/memoryruntime ./internal/memoryassembly ./internal/memorystore -count=1
ok  	github.com/wwsheng009/ai-agent-runtime/internal/memoryruntime	29.896s
ok  	github.com/wwsheng009/ai-agent-runtime/internal/memoryassembly	1.367s
ok  	github.com/wwsheng009/ai-agent-runtime/internal/memorystore	14.915s

go test ./internal/api/runtimeapi -run 'TestMemory(StatusReportsExtractionBacklogAndRedactsDiagnostics|APIJobOperatorsRequireAdminAndRedactReason|APIStatusOffDoesNotCreateDB)' -count=1
ok  	github.com/wwsheng009/ai-agent-runtime/internal/api/runtimeapi	2.070s

go test ./internal/config -run 'TestMemoryConfig' -count=1
ok  	github.com/wwsheng009/ai-agent-runtime/internal/config	1.464s
```

仓库根目录执行 `git diff --check` 通过。工作区中既有 cache/reasoning、analytics、web 和其他非 memory 修改均未清理或回滚。

---

### 19.7 G0 recovery replay / operator preview 补充

在本节原有 G0-2/G0-3 实现后，新增完成：

- `memorystore.ReplayDigest` 和 dry-run 分类器：receipt identity / generation binding / purge generation / existing output receipt / job identity / maintenance lease 均在 store 事务边界重新校验；仅 orphan 可补建 job，已有 job、终态 digest 和无效/过期身份不被盲目重放；
- `memoryruntime.PreviewDigestReplayRecovery`：读取 durable canonical receipt 和当前 session generation，最多返回 500 个 oldest-first 分类项，不触碰 canonical receipt ack 或 memory job/digest 状态；
- admin-only HTTP `GET /api/runtime/memory/digests/replay-preview` 和本地 CLI `/memory jobs replay-preview [limit]`；CLI 检查已绑定 runtime 的 ProjectKey 与当前 workspace 一致；两个入口对失败都不暴露 provider/runtime/database 原始诊断；
- 临时 SQLite 测试覆盖 replay 分类/idempotency/dry-run no-write，runtime receipt/current-generation 验证，API admin/runtime binding/error redaction，以及 CLI limit clamp/workspace isolation；没有访问真实 dogfood 数据库。

新增验证结果（2026-11-22）：

```text
cd backend
go test ./internal/memorystore ./internal/memoryruntime ./internal/memoryassembly ./internal/api/runtimeapi -count=1
ok   ./internal/memorystore
ok   ./internal/memoryruntime
ok   ./internal/memoryassembly
ok   ./internal/api/runtimeapi

go test ./cmd/aicli/commands -run 'Test(G0|Memory|StructuredMemory|ChatMemory)' -count=1
ok   ./cmd/aicli/commands

# CLI replay-preview changed afterward; focused structured command coverage:
go test ./cmd/aicli/commands -run 'TestStructuredMemory(DigestReplayPreview|V2Commands|JobOperations|JobsModeOffDoesNotCreateDatabase)' -count=1
ok   ./cmd/aicli/commands

git diff --check
# exit 0
```

上述测试只使用测试临时目录/fixture。真实 `.aicli/memory/memory.db` 未读取或修改；G1 FTS5 与 G0-5 dogfood 门禁继续保持 blocked/not run。

### 19.8 本轮 operator summary 安全复核与回归

对分组标签新增 Unicode 控制字符清理，避免持久化 `kind` 中的 ANSI escape 被 structured/local `/memory jobs` 原样写入终端。相关实现和测试已执行 `gofmt`，`gofmt -d` 无输出。

2026-11-22 在 `backend` 目录重新执行：

```text
go test ./internal/memorystore ./internal/memoryruntime ./internal/memoryassembly ./internal/api/runtimeapi -count=1
ok   github.com/wwsheng009/ai-agent-runtime/internal/memorystore        21.689s
ok   github.com/wwsheng009/ai-agent-runtime/internal/memoryruntime      34.748s
ok   github.com/wwsheng009/ai-agent-runtime/internal/memoryassembly     1.281s
ok   github.com/wwsheng009/ai-agent-runtime/internal/api/runtimeapi   211.391s

go test ./internal/team -count=1
ok   github.com/wwsheng009/ai-agent-runtime/internal/team 4.758s

go test ./cmd/aicli/commands -run 'Test(G0|Memory|StructuredMemory|ChatMemory)' -count=1
ok   github.com/wwsheng009/ai-agent-runtime/cmd/aicli/commands 2.666s

go test ./cmd/aicli/commands -run 'TestStructuredMemory(DigestReplayPreview|V2Commands|JobOperations|JobsModeOffDoesNotCreateDatabase)' -count=1
ok   github.com/wwsheng009/ai-agent-runtime/cmd/aicli/commands 1.858s

go test ./internal/config -run 'TestMemoryConfig' -count=1
ok   github.com/wwsheng009/ai-agent-runtime/internal/config 1.628s

go test ./internal/memorystore -run 'Test(SummarizeJobs|FTS5CapabilityProbe)' -count=1 -v
fts_unavailable: sqlite3: SQL logic error: no such module: fts5
--- PASS: TestFTS5CapabilityProbe
--- PASS: TestSummarizeJobsClassifiesProjectBacklogWithoutDiagnostics
--- PASS: TestSummarizeJobsIsProjectScopedAndBoundsGroups
--- PASS: TestSummarizeJobsBoundsAndRedactsGroupLabels

gofmt -d <relevant memory Go files>
# no output

git diff --check
# exit 0
```

`internal/team` 回归测试通过；本轮没有改 team durable-event schema 或接入 task-completion consumer。以上验证使用测试临时数据库/fixture，不代表真实 dogfood 恢复；G0-5 仍未运行，G1 仍因 FTS5 不可用而 blocked。

随后补齐 CLI 与 API/store summary 的 priority 分组一致性，并复跑：

```text
go test ./cmd/aicli/commands -run 'TestStructuredMemory(JobOperations|V2Commands|StatusAndJobsOmitRawDiagnostics|JobsModeOffDoesNotCreateDatabase)' -count=1
ok   github.com/wwsheng009/ai-agent-runtime/cmd/aicli/commands 1.678s
```

---

**执行提醒**：本文件是施工清单，不是又一轮审计入口。开工后只允许围绕上述工作包读取必要代码、修改、测试和记录证据；任何新发现先判断是否属于已列缺口，属于则直接落到对应工作包，不属于则记录为非目标，不扩大本轮范围。

### 19.9 P2-02 active records list/search 补充（2026-11-22）

本轮移除了 v2 `List` / `Search` 的 `source_kind='manual'` 隐式限制：

- 默认 list/search 现在覆盖当前 project scope 下所有 `active` memory records，包括 `session_extract` 自动记忆；
- 新增 `ListWithOptions` / `ListOptions` 和 `SearchOptions.SourceKind`，允许显式按 source kind 过滤；保留 `manual` 作为可选过滤值；
- `RecordFilter.SourceKind` 接入本地记录列表和 `GET /api/runtime/memory/records?source_kind=...`；
- 本地 structured/legacy CLI 支持 `/memory list [n] --source-kind <kind>`、`/memory search <query> --source-kind <kind>`；runtime-server CLI list/search 也传递可选过滤值；
- CLI 展示自动记忆的 source kind，并对 source label 做 secret redaction、控制字符清理和长度限制；
- mode=off 下仍走 legacy JSONL；source filter 只按 Note.Source 筛选，不创建 v2 DB。

当前仅关闭了 P2-02 中 “默认 list/search 不再 manual-only” 的窄缺口，不代表 G1 或完整 P2-02 已完成：list 默认仍为 active-only（不包含 candidate），search 仍是现有 LIKE/keyword scoring，不是统一 `SearchRecords`、FTS5 或 BM25；G1 仍因 Go SQLite driver 缺少 FTS5 而 blocked。

临时数据库覆盖了 manual 与 active `session_extract` 默认可见、显式 source-kind 过滤、legacy mode source filter、API source filter 和 CLI 输出脱敏。未读取或修改真实 dogfood DB。

验证（工作目录 `backend`）：

```text
go test ./internal/memorystore -count=1
ok   github.com/wwsheng009/ai-agent-runtime/internal/memorystore

go test ./internal/api/runtimeapi -count=1
ok   github.com/wwsheng009/ai-agent-runtime/internal/api/runtimeapi

go test ./cmd/aicli/commands -run 'Test(StructuredMemory|ChatMemory|TryExecuteStructuredChatCommandMemory|HandleMemoryCommand_AddListSearch)' -count=1
ok   github.com/wwsheng009/ai-agent-runtime/cmd/aicli/commands

go test ./cmd/aicli/commands -run 'Test.*RuntimeServer.*|TestChatRuntimeServer.*' -count=1
ok   github.com/wwsheng009/ai-agent-runtime/cmd/aicli/commands

gofmt -d <changed Go files>
# no output

git diff --check
# exit 0
```

### 19.10 历史 job deadline repair 与 claim fail-closed（2026-11-22）

本轮针对 G0-5 运行态中“历史 `memory_jobs.deadline_at` 为空，导致 pending backlog 绕过绝对超时”的代码缺口补齐了临时数据库可验证的修复，但没有把真实 dogfood 数据库当作测试 fixture：

- `memory.maintenance.jobDeadlineRepair` 新增显式 opt-in 配置，默认关闭；启用后使用 maintenance owner lease，按有界 batch 在启动恢复和周期 maintenance 中执行，默认 batch 为 100、周期为 30 分钟；
- `Store.PlanMissingJobDeadlines` 提供无写入 dry-run，`Store.RepairMissingJobDeadlines` 只处理当前 project 下、仍为 pending 且已满足 available/retry/deferred 时间门槛的历史 job，使用 durable `created_at + DefaultMemoryJobTimeout` 推导 deadline；
- repair 不触碰 running job；无效 `created_at`、purge fence 冲突和并发状态变化均 fail closed/分类为 skipped；实际写入需要 maintenance lease，并记录 `memory_job_operator_audit`；
- `ClaimJobs` 在 maintenance repair 尚未运行时也会对缺失 deadline 的 pending row 做同样的 durable inference；过期 row 在启动 worker 前转为 `failed`，达到最大尝试次数则转为 `dead_letter`，写入超时原因和推导出的 deadline；malformed `created_at` 不进入 claim candidate，不会被猜测或启动；
- lease reclaim、heartbeat 和正常 claim 均不再用新的 lease 时间覆盖 absolute `deadline_at`；因此历史修复和 worker claim 都不会把绝对超时无限向后推移。

临时 SQLite fixture 覆盖了：

- 缺失 deadline 的 future/expired pending job：future job 只被 claim 一次并持久化推导 deadline，expired job 不启动；
- malformed `created_at` 的 fail-closed；
- expired pending 的 failed/dead-letter 分类；
- dry-run no-write、实际 repair、maintenance lease、project isolation、running job 不修改、purge fence skip 和 operator audit。

本轮实际验证（工作目录 `backend`）：

```text
gofmt -w internal/memorystore/v2_runtime.go
go test ./internal/memorystore -run 'Test(ClaimRepairsMissingDeadlineBeforeStartingOrExpiringPendingJob|ClaimLeavesMalformedCreatedAtPendingWithoutStarting|ClaimDoesNotStartExpiredPendingJobs|RepairMissingJobDeadlines)' -count=1
ok   github.com/wwsheng009/ai-agent-runtime/internal/memorystore

go test ./internal/memorystore ./internal/memoryassembly ./internal/config -count=1
ok   github.com/wwsheng009/ai-agent-runtime/internal/memorystore
ok   github.com/wwsheng009/ai-agent-runtime/internal/memoryassembly
ok   github.com/wwsheng009/ai-agent-runtime/internal/config

go test ./internal/memoryruntime ./internal/api/runtimeapi -count=1
ok   github.com/wwsheng009/ai-agent-runtime/internal/memoryruntime
ok   github.com/wwsheng009/ai-agent-runtime/internal/api/runtimeapi

go test ./cmd/aicli/commands -run 'TestStructuredMemoryV2Commands|TestChatMemoryStatusReportsRuntimePresence|TestStructuredMemoryStatusAndJobsOmitRawDiagnostics|TestStructuredMemoryJobOperations|TestStructuredMemoryJobsModeOffDoesNotCreateDatabase|TestStructuredMemoryDigestReplayPreviewRequiresBoundRuntime|TestHandleMemoryCommand_AddListSearch' -count=1
ok   github.com/wwsheng009/ai-agent-runtime/cmd/aicli/commands
```

上述命令只使用 `t.TempDir()` SQLite fixture；真实 `.aicli/memory/memory.db` 本轮未读取、未修改、未清理、未 reset。该修复仍不能关闭 G0-5：真实 backlog 的只读分类、dry-run 结果和受控恢复尚未执行；G1–G5 仍未实施，G1 仍受当前 Go SQLite driver 缺少 FTS5 阻断。
### 19.11 2026-11-23 状态更新

本节不重写 2026-11-22 的执行记录，只记录之后的代码落地情况；当前状态以 `19-implementation-status-20261123.md` 和 `20-g3-record-versions-evidence-20261123.md` 为准。

已提交到 `main`：

- `8513f579`：G0–G3.5 memory capabilities（capture replay、job operator/deadline repair、versions v20/v21、Core v22/v23、budget/usage、API/CLI）。
- `a9c7a461`：Core answer citation 与 `memory_injection_budget_*` 选项。
- `eb413e1f`：`aicli onboarding memory` preset。

状态边界：

- G3 代码与测试已落地，但门禁仍受 G1 FTS 同步、G2 tenant、G4 高风险 supersede 依赖限制；
- G3.5 仍按 `18` 的未完成清单保持部分实施；
- G0-5 仍未执行，真实 dogfood 库未读取/恢复；
- G1-0 driver 能力已解决（`sqlitedriver/register_fts5.go` 在主线自动注册 ncruces FTS5 扩展，探针返回 `fts_available`）；FTS 索引/同步/BM25（`8ddd48c1`）、统一 `SearchRecords`（`c8acdc30`）与有界注入候选（`22c4d03a`）已提交；1k/10k 基准显示延迟仍近线性，需索引优化；多值/G2 scope 与 100k 待补，G1 仍未关闭。