# G2 Scope 隔离实现证据（2026-11-23）

## 状态

**G2 的 schema、ScopeContext/grant、主要读写路径与 API scope 绑定已提交并通过测试。**

G2 现在按 Project Memory v1 的 project scope 达到**代码级收尾**：

```text
scope = project
project_key = canonical workspace identity
```

这不等同于声明“完整多 tenant 产品能力已关闭”。同 project_key 下的跨 tenant
隔离已覆盖所有带 tenant 身份的 v1 表；仍缺少 tenant 身份的内部表已在 §5.3
逐项列出，并明确不属于 v1 发布 profile。

本轮 G2 收尾改动尚未提交。发布关闭仍需在隔离 worktree 或固定提交上重跑本文
命令并保存 commit hash；当前结论只是工作区证据。

提交：

- `a4fca327`：tenant/owner/agent/session/task scope 隔离（migration v24、visibility chain、grants、读写 predicate、API context、测试）。

已覆盖：

- `memory_entries` 的 `tenant_id/owner_id/agent_id/task_id/scope_kind/scope_key` 列与索引；
- `memory_events`、`memory_usage`、`memory_suppressions`、`memory_recall_traces`、`memory_jobs`、`memory_summaries`、`memory_turn_digests`、`memory_turn_output_receipts`、`memory_versions`、`memory_core`、`memory_core_usage` 的 scope 列/backfill；
- `ScopeContext` + `WithMemoryScope`/`MemoryScopeFromContext`；
- `MemoryScopeGrant` 的创建/撤销与过期校验；
- list/search/FTS/usage/versions/governance 等路径的 scope predicate；
- API 读写的 scope 绑定（`memoryRequestContext`）；
- 跨 user/agent/session/task/tenant 的负向测试。
- 2026-11-23 增量：migration v30 为 `memory_session_purges`/`memory_turn_capture_generations` 增加 tenant 身份并重建主键；purge/fence/capture 查询与删除路径接入 tenant；`TestMemorySessionPurgeIsTenantScoped` 验证同 project/session 的两个 tenant 互不删除。`memory_jobs`/`memory_summaries`/`memory_turn_digests`/`memory_turn_windows` 的操作查询已补 tenant 作用域；audit/export/import 已补 tenant 隔离测试（legacy 空 tenant 事件按关联记录 fail closed；跨 tenant 同 ID import fail closed）；`TestMemoryWorkspaceRegistryKeepsRecordCorporaIsolated` 与 `TestMemoryWorkspaceStatusKeepsDigestAndJobBacklogsIsolated` 验证两 workspace 的 records corpus、digest backlog 与 job backlog 不互见；`memorystore` 全包测试已恢复通过。
- 2026-11-23 G2 收尾增量：status 计数、job get/claim/reclaim/heartbeat/complete/fail/operator/deadline repair、correction/suppression、source invalidation、version history/as-of/rollback、core/core usage、metrics 的 entries/usage/traces/jobs 读取全部补 tenant predicate；新增 `TestMemoryG2*` 五组跨 tenant 负向测试；`TestMemoryV30UpgradePreservesLegacyPurgeAndCaptureIdentity` 验证 v29→v30 legacy `tenant_id=''` 保留与同 project/session、同 receipt 的跨 tenant 共存。

当前关闭范围：

1. v1 的 project scope 读写、删除、运营、审计、导出、导入、版本和 rollback 均已有同一 project/tenant fail-closed 语义；
2. 全仓 `project_key` 审计已完成并分类，见 §5；
3. `Mode=off` 已有 runtime / HTTP / CLI 三面独立证据，见 §6。

---

## 1. 实现内容

### 1.1 schema v24

`backend/internal/memorystore/v2_schema.go`：

- migration `memory_v24_scope_isolation`；
- `memory_entries` 增加 `tenant_id/owner_id/agent_id/task_id/scope_kind/scope_key`；
- 同步扩展 events/usage/suppressions/recall traces/jobs/summaries/turn digests/output receipts/versions/core/core usage；
- backfill：空 scope 行按 project key 回填；
- 索引：
  - `(tenant_id, scope_kind, scope_key, status, updated_at DESC)`
  - `(tenant_id, project_key, owner_id, agent_id, status)`
  - `(tenant_id, task_id, status)`
  - `(tenant_id, source_session_id, status)`

### 1.2 visibility chain

`backend/internal/memorystore/v2_scope.go`：

- `ScopeContext` 包含 tenant/project/owner/agent/session/task；
- `normalizeScopeContext`：
  - 缺 project key 拒绝；
  - 跨 tenant 拒绝；
  - user/profile/agent/session/task scope 的 key 缺失时拒绝；
- `buildMemoryScopeVisibility`：
  - project scope 始终可见（同 tenant/project）；
  - 显式 grant 可以放行更窄 scope；
  - grant 过期、tenant 不匹配、scope 不匹配均不可见；
- `memoryScopeVisibility` 生成 SQL predicate + args，读/写权限分别使用 `ScopePermissionRead/Write`；
- 缺 scope context 时 fail closed（读空/写拒绝）。

### 1.3 接入路径

- `ListRecords` / `SearchRecords` / FTS：
  - `SELECT` 增加 scope 列；
  - WHERE 使用 `memoryScopeVisibility`；
- `GetRecord` / 更新 / 删除 / 恢复：
  - 使用 write/read predicate；
- `v2_usage.go`：
  - usage 写入前校验记录在调用者可见 scope；
  - usage 查询按 scope 过滤；
- `v2_versions.go`：
  - 版本快照写入 tenant/scope 列；
  - rollback/as-of 路径按 scope 校验；
- `v2_governance.go`：
  - restore/revalidate/生命周期写路径带 scope predicate；
- API：
  - `memoryRequestContext` 从请求解析 scope（admin 才允许显式 header）；
  - record list/get/create/patch/delete 使用统一 context；
  - `memoryRecordDTO` 暴露 tenant/owner/agent/task/scope 字段。

---

## 2. 验证记录

工作目录：`backend/`

### 2.1 scope 定向测试

```text
go test ./internal/memorystore -run 'TestMemoryScope' -count=1 -v
--- PASS: TestMemoryScopeUserIsolationAndGrantLifecycle
--- PASS: TestMemoryScopeAgentSessionTaskIsolation
--- PASS: TestMemoryScopeTenantAndMissingContextFailClosed
--- PASS: TestMemoryScopeExpiredGrantIsInvisible
--- PASS: TestMemoryScopeInjectionUsesCallerChain
--- PASS: TestMemoryScopeUsageIsolation
--- PASS: TestMemoryScopeSchemaAndBackfillRepair
PASS
ok  internal/memorystore  1.034s
```

### 2.2 API scope 测试

```text
go test ./internal/api/runtimeapi -run 'TestMemoryAPIRecordsScopeIsolation' -count=1 -v
--- PASS: TestMemoryAPIRecordsScopeIsolation (0.78s)
```

### 2.3 干净 worktree 全量回归（HEAD `a4fca327`）

```text
go test ./internal/memorystore -count=1
ok  internal/memorystore  49.194s

go test ./internal/memoryruntime -count=1
ok  internal/memoryruntime  52.755s

go test ./internal/api/runtimeapi \
  -run 'TestMemoryAPIRecordsScopeIsolation|TestMemoryAPIRecordsLifecycleAndStatus|TestMemoryStatus' -count=1
ok  internal/api/runtimeapi  2.333s

go test ./cmd/aicli/commands -run 'TestStructuredMemory|TestChatMemory' -count=1
ok  cmd/aicli/commands  6.489s

go build ./internal/memorystore ./internal/api/runtimeapi ./internal/memoryruntime ./cmd/aicli/commands
# exit 0
```

---

## 3. G2 退出条件对照

| 条件 | 结果 | 说明 |
|---|---|---|
| 所有读写入口统一 scope filter | ✅（v1 project scope） | list/search/FTS/usage/versions/governance、jobs/summaries/turn digest/window/audit/export/import/purge、status、correction/suppression、source invalidation、job operator/deadline、core/core usage、metrics 读取已补 tenant/scope；剩余无 tenant 列内部表见 §5.3 |
| 跨 tenant/user/agent/task 负向测试 | ✅ | 7 个 scope 测试 + 5 个 `TestMemoryG2*` 测试 |
| 缺 scope 时 fail closed | ✅ | 缺 project/tenant 不匹配/缺失 scope key 均拒绝 |
| runtime-server 多 workspace 路由测试 | ✅（状态/records） | registry、records corpus、digest backlog、job backlog 隔离测试通过 |
| export/import/purge/summary/usage/audit 带 scope | ✅（定向） | usage/versions/governance/summary/audit/export/import/purge 已有 tenant 定向测试 |
| `Mode=off` / 单 project 默认无回归 | ✅ | runtime 零调用、HTTP 零建库、CLI 零建库三面独立证据见 §6 |
| v30 升级 | ✅ | v29 legacy purge/capture 行保留 `tenant_id=''`；同 project/session 与同 receipt 可由另一 tenant 共存 |

---

## 4. G2 收尾测试

工作目录：`backend/`

```text
go test ./internal/memorystore -run 'TestMemoryG2' -count=1
ok  internal/memorystore  2.912s

go test ./internal/memorystore -run 'TestMemoryV30UpgradePreservesLegacyPurgeAndCaptureIdentity' -count=1
--- PASS: TestMemoryV30UpgradePreservesLegacyPurgeAndCaptureIdentity (0.31s)

go test ./internal/memorystore \
  -run 'TestMemory(Scope|JobOperational|SummaryOperational|TurnOperational|AuditAndExport|ImportIsTenant|SessionPurgeIsTenant|WorkspaceRegistry|WorkspaceStatus)' \
  -count=1
ok  internal/memorystore  6.810s

go test ./internal/memorystore -count=1
ok  internal/memorystore  101.760s

go test ./internal/api/runtimeapi -run 'TestMemory' -count=1
ok  internal/api/runtimeapi  34.097s

go test ./cmd/aicli/commands \
  -run 'Test(StructuredMemory|ChatMemory|HandleMemoryCommand|MemoryCrossSurface)' -count=1
ok  cmd/aicli/commands  17.832s

go test ./internal/toolkit/tools -run 'TestMemory' -count=1
ok  internal/toolkit/tools  9.560s

go test ./internal/memoryruntime -count=1
ok  internal/memoryruntime  88.291s

go test ./internal/memoryassembly -count=1
ok  internal/memoryassembly  3.585s
```

新增负向测试：

```text
TestMemoryG2StatusRecordCountsAreTenantScoped
TestMemoryG2JobIdentityAndOperatorAreTenantScoped
TestMemoryG2CorrectionSuppressionIsTenantScoped
TestMemoryG2SourceInvalidationIsTenantScoped
TestMemoryG2VersionHistoryAndRollbackAreTenantScoped
TestMemoryV30UpgradePreservesLegacyPurgeAndCaptureIdentity
```

`TestMemoryG2JobIdentityAndOperatorAreTenantScoped` 同时验证：

- 同 project 下同 kind/job_key 落入另一 tenant 时 fail closed；
- `GetJob` / `ClaimJobs` / `DeadLetterJob` 不跨 tenant；
- tenant B 的 operator 操作不会改变 tenant A 的 job 状态。

---

## 5. 全仓 project_key 审计

### 5.1 审计口径

扫描范围：

```text
backend/internal/memorystore/*.go
排除：*_test.go、v2_schema.go、v2_migrate.go
```

对每个包含 `project_key` 的 SQL literal 检查其 `SELECT/INSERT/UPDATE/DELETE`
作用域：

- 带 `tenant_id` 或统一 scope predicate：通过；
- 仅 project 键但属于 §5.3 已列出的无 tenant 内部表或已文档化的 v1.1/G4/G5
  冻结路径：豁免；
- 其余：视为未分类裸查询，必须修复。

本轮修复后，v1 记录/运营/版本/审计/检索/注入主路径无未分类裸查询。

### 5.2 本轮修复的裸查询

- `v2_helpers.go`：status 的 entries 状态/manual/freshness/pinned 计数；
- `v2_runtime.go`：`GetJob`、`getJobByKey`、claim/reclaim、heartbeat、complete、
  fail、promote、supersede、candidate revive、source invalidation、stale base
  extraction cancellation；
- `v2_job_operator.go`：operator job select/update；
- `v2_job_deadline_repair.go`：deadline scan/update；
- `v2_core.go` / `v2_core_usage.go`：core 源校验、版本分配/查询/激活/归档、
  core usage；
- `v2_corrections.go`：correction 的 entry update 与 suppression 写入；
- `v2_store.go`：suppression check 与 injection suppression hash；
- `v2_invalidation.go`：source invalidation 查询与 entry update；
- `v2_governance.go`：restore/revalidate/freshness sweep/reset；
- `v2_metrics.go`：entries、usage、recall traces、summary rebuild job 计数；
- `v2_versions.go`：version history/as-of/rollback/suppression/version baseline；
- `v2_evidence.go`：record source 与 extraction diagnostics 读取。

### 5.3 明确豁免（不纳入 v1 多 tenant 保证）

| 对象/路径 | 原因 |
|---|---|
| `memory_session_modes` | 表没有 tenant 列；v1 为单 tenant project/session 模式 |
| `memory_runtime_metrics` | 项目级运行计数，无 tenant 维度 |
| `memory_maintenance_leases` | 项目级 maintenance fencing，无 tenant 维度 |
| `memory_job_operator_audit` | 无 tenant 列；无公开 list API，job id/project 是审计键 |
| `memory_extraction_diagnostics` insert | 表无 tenant 列；读取已 JOIN `memory_jobs` 按 tenant/project fail closed |
| `memory_version_purge_guards` | 仅控制 version purge trigger 的临时 guard |
| `memory_summaries` revision 分配 | legacy `UNIQUE(scope, project_key, summary_kind, revision)` 仍是 project 级；读取/写入按 tenant，revision 仅共享序号 |
| `memory_events` legacy insert | 事件写入保留空 tenant 兼容；读取按关联 entry/summary tenant + project fail closed |
| retention / G4 / G5 路径 | v1 冻结或后版本范围，不进入 G2 发布 profile |

同 project 下的 content hash、job identity、summary revision 仍有 project 级唯一
约束。跨 tenant 同 project 发生同 identity 时是**冲突/fail closed**，不是可见性
泄漏。v1 的 canonical workspace identity 要求 project key 全局唯一，因此该
限制不阻断 v1；完整 multi-tenant 同 project 共存属于后续 RFC。

---

## 6. Mode=off 独立证据

### 6.1 Runtime 零模型调用

```text
go test ./internal/memoryruntime \
  -run 'TestRuntimeOffMakesNoCalls|TestModeOffDoesNotConsumeMaterializedReceipt' -count=1 -v

--- PASS: TestModeOffDoesNotConsumeMaterializedReceipt
--- PASS: TestRuntimeOffMakesNoCalls
ok  internal/memoryruntime  1.233s
```

覆盖：

- `TriggerIdle` 在 mode=off 时 source/list/extract 调用数均为 0；
- 已 materialize 的 capture receipt 在 mode=off 时不被消费，digest 保持 pending，
  source transcript 读取次数为 0。

### 6.2 HTTP 零建库

```text
go test ./internal/api/runtimeapi \
  -run 'TestMemoryAPIStatusOffDoesNotCreateDB|TestMemoryAPIJobSummaryModeOffDoesNotCreateDatabase|TestMemoryAPICoreRebuildModeOffDoesNotCreateDatabase' \
  -count=1 -v

--- PASS: TestMemoryAPICoreRebuildModeOffDoesNotCreateDatabase
--- PASS: TestMemoryAPIStatusOffDoesNotCreateDB
--- PASS: TestMemoryAPIJobSummaryModeOffDoesNotCreateDatabase
ok  internal/api/runtimeapi  1.468s
```

`TestMemoryAPIStatusOffDoesNotCreateDB` 在 conclusion 前依次访问 status、jobs、
summaries、usage、metrics、audit、export、operator retry/dead-letter、
deadline repair 和 import，最后断言 `.aicli/memory/memory.db` 不存在。

### 6.3 CLI 零建库

```text
go test ./cmd/aicli/commands \
  -run 'TestStructuredMemoryJobsModeOffDoesNotCreateDatabase' -count=1 -v

--- PASS: TestStructuredMemoryJobsModeOffDoesNotCreateDatabase
ok  cmd/aicli/commands  0.583s
```

### 6.4 结论

`off` 是“关闭自动 v2 副作用”，不是删除手工兼容路径。当前证据证明：

- 不打开/创建 v2 DB；
- 不调用模型、不消费 capture receipt；
- HTTP/CLI 运营命令返回可预期的关闭/冲突语义；
- memory unavailable 时 Agent 主链路不被阻塞。

---

## 7. 后续

1. G2 按 v1 project scope 代码级收尾；提交与隔离复现后才是发布关闭，不把剩余 multi-tenant same-project 扩展并入 v1；
2. G0-5 仍需 not_orphaned/failed job 分类、真实库迁移和新会话注入验证；
3. P3 的五个 release gate 与隔离提交复现仍未完成；
4. 工作区并发改动已出现 v31 `memory_dream_runs` 增量；v30 是本 G2 证据的迁移，
   v31 不属于本证据范围，发布施工必须在隔离 worktree/固定提交上重新核对。