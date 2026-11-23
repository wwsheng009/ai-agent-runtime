# G3 记录版本、回滚与时间点查询证据（2026-11-23）

## 状态

**G3 主体代码级实现完成，定向测试通过；代码尚未提交，G3 门禁不能关闭。**

本轮只读取/验证工作区代码与临时 SQLite fixture，未访问、迁移或修改真实 `.aicli/memory/memory.db`。

不能关闭 G3 的原因：

1. 变更未提交，无法形成可追溯交付；
2. `memory_versions` 使用 `record_json` 快照存储，缺少计划 §6.1 中的 `tenant_id`、独立 `kind/status/title/text/summary/tags_json/metadata_json` 列和 tenant 索引（tenant 依赖 G2）；
3. FTS 同步未实现（G1 因 driver 缺少 FTS5 而 blocked）；
4. 高风险自动 supersede 的“不静默覆盖”审计/门禁未在本切片验证（G3-5/G4 边界）；
5. G3 退出条件中的 summary/usage 全链路同步未完整覆盖。

---

## 1. 实施内容

### 1.1 schema

`backend/internal/memorystore/v2_schema.go`：

- migration v20 `memory_v20_record_versions`
  - `memory_versions(id, memory_id, version, project_key, scope_kind, scope_key, record_json, changed_by, change_reason, change_type, source_event_id, valid_from, valid_until, created_at)`
  - `UNIQUE(memory_id, version)`
  - 索引 `(memory_id, version DESC)`、`(project_key, created_at DESC)`
  - `BEFORE UPDATE/DELETE` trigger 强制 append-only
- migration v21 `memory_v21_record_version_purge_guard`
  - `memory_version_purge_guards(project_key)`，仅项目 purge 路径可删除版本
- `v2_versions.go` 启动 backfill：为存量记录写入 `baseline` 版本（`INSERT OR IGNORE`，可重复执行）

### 1.2 版本写入接入

`appendRecordVersionTx` / `appendCurrentRecordVersionTx` 已接入：

- 创建：`v2_store.go`
- 更新/删除/恢复：`v2_helpers.go`
- correction：`v2_corrections.go`
- restore/生命周期：`v2_governance.go`
- invalidation：`v2_invalidation.go`
- retention/过期清理：`v2_retention.go`
- consolidation/promote/merge/supersede/import：`v2_runtime.go`
- turn window/rollup：`v2_turn_window.go`
- rollback 自身：`v2_versions.go`

版本写入与主表变更在同一事务；`changed_by`、`change_reason`、`change_type` 有缺省规范化。

### 1.3 rollback

`Store.RollbackRecord`（`v2_versions.go`）：

- 必须提供 `target_version`、`expected_revision`、`reason`；
- 当前 revision 不匹配返回并发冲突，不覆盖；
- 目标版本恢复 text/title/summary/tags/metadata/status/validity，并以 `change_type=rollback` 追加**新版本**，旧版本不可变；
- `revert_supersede=true` 时：
  - 要求当前记录为 superseded 且目标为 active；
  - 校验后继仍为预期记录；后继已被修改则拒绝；
  - 后继标记 superseded/invalidated、写 `valid_until`、追加版本；
  - 后继 summary 失效；
- 校验 maintenance lease、session purge fence、项目/scope 边界。

### 1.4 时间点查询

`Store.ListRecordsAsOf`（`v2_versions.go`）：

- 按 `valid_from <= as_of AND (valid_until IS NULL OR valid_until > as_of)` 返回历史快照；
- 使用解析后的时间戳而非 SQLite 文本排序；
- 复用 project/scope 过滤并支持分页。

### 1.5 API / CLI / 工具

- HTTP：
  - `GET /api/runtime/memory/records/{id}/versions`
  - `POST /api/runtime/memory/records/{id}/rollback`
  - `GET /api/runtime/memory/records?as_of=<RFC3339>`
- CLI：
  - `/memory history|versions <id>`
  - `/memory rollback <id> <version> --expected-revision <n> --reason <text> --yes [--revert-supersede]`
- 工具：
  - `memory_rollback`：默认不注册，需 `memory.tools.allowRollback=true`；要求 `confirm=true`、expected revision、当前 session 所有权、非 private/archived/deleted/secret；`revert_supersede` 仅在 successor 同属当前 session 且仍为安全 active 记录时放行。
  - `memory_history`：只读历史查询。

### 1.6 文档

`docs/agent-memory/04-data-model-and-api.md` 已补：

- `memory.tools.allowRollback`
- rollback API/CLI 语义与权限
- versions/rollback 工具边界

---

## 2. 验证记录

工作目录：`backend/`

### 2.1 store 层

```text
GOFLAGS=-p=1 go test ./internal/memorystore \
  -run 'TestRecordVersion|TestRecordRollback|TestListRecordsAsOf' -count=1 -v
```

结果：

```text
--- PASS: TestRecordVersionsCaptureUpdatesAndRollbackAsNewRevision (0.10s)
--- PASS: TestRecordVersionBaselineBackfillIsIdempotentAndAppendOnly (0.08s)
--- PASS: TestRecordVersionPurgeGuardRollsBackOnFailure (0.08s)
--- PASS: TestRecordVersionListAndRollbackAreProjectScopedAndRevisionGuarded (0.12s)
--- PASS: TestRecordVersionsCoverDeleteRestoreCorrectionAndSupersede (0.11s)
--- PASS: TestRecordRollbackHonorsPurgeAndMaintenanceFences (0.08s)
--- PASS: TestRecordRollbackRejectsChangedSupersedingSuccessor (0.08s)
--- PASS: TestListRecordsAsOfTracksCreateUpdateDeleteAndRestore (0.08s)
--- PASS: TestListRecordsAsOfValidatesScopeAndPaginatesWithinProject (0.11s)
--- PASS: TestListRecordsAsOfUsesParsedTimestampsNotSQLiteTextOrdering (0.09s)
PASS
ok  github.com/wwsheng009/ai-agent-runtime/internal/memorystore  1.112s
```

### 2.2 API 层

```text
GOFLAGS=-p=1 go test ./internal/api/runtimeapi \
  -run 'TestMemoryAPIRecordVersionsAndRollback|TestMemoryAPIRollbackAuthorizationWorkspaceAndValidation|TestMemoryAPIRecordAsOfRequiresAdminAndReturnsHistoricalSnapshot' \
  -count=1 -v
```

结果：

```text
--- PASS: TestMemoryAPIRecordVersionsAndRollback (0.48s)
--- PASS: TestMemoryAPIRollbackAuthorizationWorkspaceAndValidation (0.24s)
--- PASS: TestMemoryAPIRecordAsOfRequiresAdminAndReturnsHistoricalSnapshot (0.34s)
PASS
ok  github.com/wwsheng009/ai-agent-runtime/internal/api/runtimeapi  1.282s
```

### 2.3 CLI

```text
GOFLAGS=-p=1 go test ./cmd/aicli/commands \
  -run '^TestStructuredMemoryHistoryRollbackAndSafeProjection$' -count=1
```

结果：

```text
ok  github.com/wwsheng009/ai-agent-runtime/cmd/aicli/commands  0.605s
```

### 2.4 工具与注册门禁

```text
GOFLAGS=-p=1 go test ./internal/toolkit/tools \
  -run 'TestMemoryRollbackRequiresConfirmOwnershipAndConfigGate|TestMemoryToolsLifecycle|TestMemoryToolsHidePrivateRecordsFromAgentSurface' -count=1
ok  github.com/wwsheng009/ai-agent-runtime/internal/toolkit/tools  4.724s

GOFLAGS=-p=1 go test ./internal/tools \
  -run 'TestMemoryToolRegistrationConditions|TestMemoryHistoryToolRegistrationAndSchema' -count=1
ok  github.com/wwsheng009/ai-agent-runtime/internal/tools  0.260s
```

### 2.5 格式

```text
gofmt -l <G3 related memory/API/CLI files>
# 无输出
```

---

## 3. G3 退出条件对照

| 退出条件 | 结果 | 说明 |
|---|---|---|
| `memory_versions` append-only，所有变更路径写版本 | ✅（临时库） | trigger + 13 处写入接入；测试覆盖 create/update/correct/supersede/delete/restore/import |
| rollback API/tool/CLI 可用，含并发冲突和权限测试 | ✅（定向） | store/API/CLI/tool 测试通过；`revert_supersede` 有冲突保护 |
| 自动 supersede 可撤销，valid 区间正确 | 🟡 | 后继可失效且写 valid_until；完整 G4 patch 回滚不在本切片 |
| create/update/correct/supersede/delete/restore 版本测试 | ✅ | `TestRecordVersionsCoverDeleteRestoreCorrectionAndSupersede` |
| 历史查询 store 层按 `as_of` 返回 | ✅ | `ListRecordsAsOf` + 3 个测试 |
| 高风险自动 supersede 不静默发生 | ❌ 未验证 | 依赖 G4 冲突审计/人工 correction 边界 |
| FTS/summary/usage 与版本操作同步 | ❌ | FTS 被 G1 FTS5 阻断；summary 仅 rollback successor 失效路径覆盖；usage 未联动 |
| schema 与计划 §6.1 一致 | 🟡 | 采用 `record_json` 快照；无 `tenant_id` 和独立列/索引（tenant 依赖 G2） |

结论：**G3 代码主体可用，但 G3 门禁不能关闭，也不能在文档中标记为已交付。**

---

## 4. 提交阻塞（未执行 commit）

本轮**没有执行 git commit**。原因不是测试失败，而是工作区多个未提交工作流交错，无法在不破坏可编译性的前提下做 G3 单独提交：

| 文件 | 混合内容 |
|---|---|
| `backend/internal/api/runtimeapi/handler.go` | memory 路由 + team product 路由重写 + workspace/tenant scope 字段 + suspension projector |
| `backend/internal/agent/loop.go` | Core citation + reasoning effort analytics + shell job runtime + turn suspension reconcile |
| `backend/internal/config/manager.go`、`runtime.yaml` | memory 配置（相对独立，可切分） |
| `backend/cmd/aicli/commands/web/*` | memory Web + 其他 UI 变更 |
| 工作区总量 | 232 项；164 个已跟踪文件 `+14860/−1446` |

如果按整文件提交 memory 路径，会把 team product、workspace claims、reasoning analytics、turn suspension 等无关变更带入；如果只提交 G3 新增文件，则 commit 快照无法编译（`v2_versions.go` 依赖 `v2_schema.go`/`v2_types.go`/`v2_helpers.go` 等同步改动）。

因此需要先做 hunk 级切分，或先将无关工作流提交/隔离后再提交 G3。未提交是为了避免污染历史或产生不可编译 commit。

---

## 5. 后续执行建议

1. 先用 hunk 级方式切出 G3 的共享文件改动（`v2_schema.go` v20/v21、`v2_types.go` 版本类型、`v2_store.go` as_of、`v2_helpers.go` 等），在独立 worktree 验证 build + 本节测试后提交；
2. `handler.go` 只保留 memory 路由 hunk；`loop.go` 只保留 Core citation hunk；
3. G3 提交通过后再切 G3.5 Core 与 G0 证据；
4. 提交前补 `git diff --check` 与本文件中的测试输出；
5. 真实 dogfood 库仍保持未访问状态。