# G2 Scope 隔离实现证据（2026-11-23）

## 状态

**G2 的 schema、ScopeContext/grant、主要读写路径与 API scope 绑定已提交并通过测试；G2 仍不能整体关闭。**

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

未关闭原因：

1. `jobs`/`digests`/`summaries`/`export/import`/`purge`/`audit` 尚未逐一证明全部带 scope 过滤；
2. 没有“无遗留裸 `project_key = ?`”的全仓审计证据；
3. runtime-server 多 workspace 路由的 scope 隔离测试未记录；
4. `Mode=off` 与单 project 默认行为虽有测试，但未形成独立证据包。

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
| 所有读写入口统一 scope filter | 🟡 | 主要 list/search/FTS/usage/versions/governance 已接入；jobs/digests/summaries/audit/export/import/purge 未逐一证明 |
| 跨 tenant/user/agent/task 负向测试 | ✅ | 7 个 scope 测试 |
| 缺 scope 时 fail closed | ✅ | 缺 project/tenant 不匹配/缺失 scope key 均拒绝 |
| runtime-server 多 workspace 路由测试 | ❌ 未记录 | 未在本切片验证 |
| export/import/purge/summary/usage/audit 带 scope | 🟡 | usage/versions/governance 已带；其余待审计 |
| `Mode=off` / 单 project 默认无回归 | ✅（测试） | 现有 legacy 与 project 测试通过；独立证据待补 |

---

## 4. 后续

1. 审计并补齐 jobs/digests/summaries/audit/export/import/purge 的 scope predicate；
2. 增加全仓 `project_key = ?` 裸查询审计测试/脚本；
3. 补 runtime-server 两 workspace 路由隔离测试；
4. 补 G2 完整证据包后关闭 G2。