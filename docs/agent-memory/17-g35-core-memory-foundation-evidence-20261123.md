# G3.5 Core Memory 基础切片证据（2026-11-23）

## 状态

**部分实施；不能据此关闭 G3.5。** 本切片只加入临时 SQLite 可验证的 Core candidate 存储、显式审核激活、归档和审计事件。它不是完整 Core rebuild、注入或分类预算实现。

Core 切片本身的 SQLite 验证只使用 Go 测试的 `t.TempDir()`。本轮没有手工访问、迁移、备份或修改真实 `.aicli/memory/memory.db`。

## 实施内容

- `backend/internal/memorystore/v2_schema.go`：新增 migration v22 `memory_v22_core_memory`，建立 `memory_core`、project/scope/version 唯一约束、每 scope 单 active partial unique index 及查询索引。
- `backend/internal/memorystore/v2_core.go`：新增 project-scope Core 数据类型及 bounded candidate/list/get 操作；candidate 要求至少一个来源记录，并检查来源位于当前 project/project scope、active、fresh、未归档、未删除、非 private、未过有效期且不含 secret-shaped 内容。内容、来源数量、token budget、actor 和 reason 均有边界。
- `ActivateCore` 仅接受 candidate，在一个写事务中重新验证来源仍为 active/fresh/可用，再将旧 active 标为 stale、新 candidate 标为 active，写 `memory_events` 审计事件；不允许模型直接覆盖 active。
- `ArchiveCore` 以 archived 状态保留历史并写审计事件，不物理删除。
- `backend/internal/memorystore/v2_core_test.go` 使用临时存储验证 migration 版本为 22、来源去重、候选/激活/替换/归档事件，以及缺来源、错误 scope、secret、过期/非 fresh/private 来源拒绝，并覆盖候选审核后来源过期时拒绝激活。

## 可复现验证

工作目录：`backend/`

```text
gofmt -w internal/memorystore/v2_core.go internal/memorystore/v2_core_test.go internal/memorystore/v2_schema.go
```

结果：完成，无格式化错误。

```text
go test ./internal/memorystore -run 'TestCoreMemory|TestFTS5CapabilityProbe' -count=1 -v
=== RUN   TestCoreMemoryCandidateActivationAndArchive
--- PASS: TestCoreMemoryCandidateActivationAndArchive (0.11s)
=== RUN   TestCoreMemoryRejectsUnscopedOrUnsafeCandidates
--- PASS: TestCoreMemoryRejectsUnscopedOrUnsafeCandidates (0.10s)
=== RUN   TestFTS5CapabilityProbe
    v2_fts_capability_test.go:26: fts_unavailable: sqlite3: SQL logic error: no such module: fts5
--- PASS: TestFTS5CapabilityProbe (0.14s)
PASS
ok   github.com/wwsheng009/ai-agent-runtime/internal/memorystore 0.609s
```

该命令在修正测试断言后通过。最初测试中事件数期望误写为 2（实际 candidate-created、activated、archived 共 3 条），且将 prompt-injection 启发式误认为 secret detector；现已改为准确的 secret-shaped fixture。

```text
go test ./internal/memorystore ./internal/memoryruntime ./internal/contextmgr ./internal/toolkit/tools -count=1
ok   github.com/wwsheng009/ai-agent-runtime/internal/memorystore 25.425s
ok   github.com/wwsheng009/ai-agent-runtime/internal/memoryruntime 40.322s
ok   github.com/wwsheng009/ai-agent-runtime/internal/contextmgr 1.125s
ok   github.com/wwsheng009/ai-agent-runtime/internal/toolkit/tools 62.347s
```

```text
go test ./internal/memorystore -run TestFTS5CapabilityProbe -count=1 -v
=== RUN   TestFTS5CapabilityProbe
    v2_fts_capability_test.go:26: fts_unavailable: sqlite3: SQL logic error: no such module: fts5
--- PASS: TestFTS5CapabilityProbe (0.13s)
PASS
ok   github.com/wwsheng009/ai-agent-runtime/internal/memorystore 0.377s
```

FTS5 probe 使用临时 DB。此环境仍不支持 FTS5；这不是 G1 通过证据，也不能用 LIKE fallback 代替 BM25。

更广范围的 `go test ./... -count=1` 已执行，但全仓仍有与本切片无关的失败项；失败项包括 `cmd/aicli/commands` 的 `TestLocalResumeAdmissionWiresRequiredBackgroundJobs`、`internal/sqliteutil` 的 `TestWriteTransactionsUseImmediateOptions`、`internal/lsp/baseline` 的 `TestIncrementalScanMatchesFullRescan` 和 `pkg/skillsapi` 的 `TestClient_AdminMutationUsesToken`，报错点不在本轮 Core/schema/证据文件。与本切片直接相关的 memorystore、memoryruntime、contextmgr、toolkit 组合测试仍通过；未因这些失败宣称全仓绿。

## 明确未完成 / 门禁状态

- 未实现 `core_rebuild` job，也未从 active summary、pinned stable records、usage 记录自动生成候选。
- 未接入 Core API、`memory_core_get` 工具、CLI、context builder 或 prompt stable prefix。
- 未实现可配置的 core/task/retrieved/procedure/reference 分类预算、裁剪顺序、recall trace/why-not 或默认 600 总预算兼容门禁。
- 当前 Core 仅支持 project scope；没有 tenant/owner/agent/user 隔离，不可视为 G2 已完成。
- G3.5 退出清单（API/tool/CLI、自动候选 rebuild、分类预算、注入硬上限及去重/失效规则）仍未满足；G3.5 状态保持部分实施。
- G1 仍 blocked（当前 driver 缺 FTS5）；G4 Episode/Dreaming、G5 entity/RRF/feedback/trust/eval 仍未由本切片实现。
- 未对 dogfood 数据库执行 schema 迁移；schema v22 仅在自动迁移的临时测试库中验证。
