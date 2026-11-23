# AI Memory 实施状态快照（2026-11-23）

## 状态

**项目级 Agent Memory v2 主干已可运行；参考设计 `AI_Agent_Memory_Engine_Design v1.0` 的 G0–G5 门禁未整体关闭。**

本文件是只读检查快照，用于收敛 `15/16/17/18` 与当前工作区代码之间的状态差异。检查过程未修改 memory 代码，未读取、迁移或修改真实 `.aicli/memory/memory.db`。

关键事实：

- G3（记录版本、回滚、时间点查询）与 G3.5（Core Memory）已有代码和测试，并已提交到 `main`（`8513f579`/`a9c7a461`/`eb413e1f`）；
- G0 代码闭环基本落地，但 **G0-5 真实 dogfood 恢复未执行**；
- G1 仍因 SQLite driver 缺少 FTS5 而 **blocked**；
- G2、G4、G5 未实施；
- API/CLI 定向测试后续在宿主资源恢复后通过；FTS 相关测试仍因 driver 缺少 FTS5 而失败（G1 blocked）。

---

## 1. 检查范围与方法

| 项 | 内容 |
|---|---|
| 文档对照 | `15` 缺口审查、`16` 施工计划、`17/18` G3.5 证据、`README` 索引 |
| 代码对照 | `memorystore`、`memoryruntime`、`memoryassembly`、`api/runtimeapi`、`cmd/aicli/commands` |
| schema 对照 | `memorystore/v2_schema.go` migration v10–v23 |
| 测试 | 定向 Go 包测试（仅临时 SQLite fixture） |
| 真实 dogfood DB | **未访问**；`15` 中的积压数字仅为 2026-11-22 快照，未在本轮复核 |

验证口径：代码存在不等于门禁关闭。门禁关闭仍需提交、可复现测试、证据记录，以及 `16` §0.2 要求的失败/跳过项说明。

---

## 2. 门禁状态矩阵

| 门禁 | 状态 | 代码/文档证据 | 未关闭原因 |
|---|---|---|---|
| Memory v2 主干 | ✅ 可用 | `memorystore` + `memoryruntime` + `memoryassembly` + `contextmgr` + API/CLI/Web；capture receipt → extract → consolidate → summary → inject → usage/citation | 属于既有项目级能力，不等于参考设计全量验收 |
| G0 运行时闭环 | 🟡 代码/临时库证据已落地 | `16` §19.1–19.10：status、backlog 诊断、job operator、replay-preview、deadline repair、active-session capture | **G0-5 未执行**：未对真实 backlog 做只读分类、dry-run 与受控恢复；无真实 `memory_entries > 0`、v2 job succeeded、跨会话注入证据 |
| G1 检索基础 | ⛔ blocked | `v2_fts_capability_test.go` 实测 `no such module: fts5`；当前为 `like_fallback` | driver 能力阻断；无 FTS 虚拟表、无统一 `SearchQuery`/BM25，不能以 LIKE 降级宣称关闭 |
| G2 Scope 隔离 | ❌ 未实施 | `v2_schema.go` 无 tenant/owner/agent/task 列；`v2_types.go:16-18` 仅 `project/user/profile` Scope | 仅 project 级硬过滤；无跨 tenant/user/agent/task 负向隔离测试 |
| G3 版本/回滚/时间 | ✅ 代码级实现（已提交 `8513f579`） | migration v20 `memory_versions`（append-only trigger）、v21 purge guard；`v2_versions.go:243/303/514`：`ListRecordVersionsPage`、`ListRecordsAsOf`、`RollbackRecord`；HTTP `/memory/records/{id}/versions`、`/rollback`；CLI `/memory history|versions`、`/memory rollback`；`v2_versions_test.go` 覆盖回滚、并发 revision、temporal as-of、purge fence | 证据见 `20`；门禁仍受 G1 FTS 同步、G2 tenant、G4 高风险 supersede 依赖限制 |
| G3.5 Core Memory + 预算 | 🟡 部分实施（已提交 `8513f579`/`a9c7a461`） | migration v22 `memory_core`（单 active partial index）、v23 `memory_core_usage`；`v2_core.go`；只读工具 `memory_core_get`；HTTP `/memory/core`、`/memory/cores`、`/memory/core/rebuild`、`/memory/core/usage`；CLI `/memory core`；selector Core 注入 + 分类预算最小切片 | 见 `18` §未完成：Core 与 summary/pinned 去重与失效、usage 的 session-generation purge、独立运行态状态面、task/procedure/reference prompt 分区、1k–2.5k Core 预算校准均未完成 |
| G4 Episode + Dreaming | ❌ 未实施 | `memoryruntime/task_completion.go` 明确仅 future G4 boundary；`types.go:314/337` 同样是 hook；仅 `JobPriorityDreaming` 常量 | 无 Episode 表/持久化、无 Patch/Dreaming pipeline、无审核/应用/拒绝/回滚 |
| G5 混合检索/Trust/评测 | ❌ 未实施 | 无 entity/alias/relation 表，无 RRF/rerank，无 trust level，无 Recall@K/MRR/成本/延迟评测 | G5-R/G5-T/G5-Q 全部未关闭 |

---

## 3. 当前工作区状态

| 项 | 观测值 |
|---|---|
| memory 提交 | `8513f579` G0–G3.5 capabilities；`a9c7a461` Core citation/预算选项；`eb413e1f` onboarding preset |
| `main` HEAD | `28f2a2b1`（含上述提交与证据文档） |
| `git status --short` | 154 项，均为未提交的 FTS/team/workspace/suspension/analytics/前端等后续工作 |
| 已提交 memory 变更 | 首批 98 文件 `+17678 / −530`，另有 Core citation 4 文件与 onboarding 3 文件 |
| 仍留在工作区的 memory 变更 | `v2_fts.go`/`v2_fts_test.go`、FTS 对 `store.go`/`v2_helpers.go`/`v2_store.go` 的集成，以及 20:00 后对 `v2_core.go`/`v2_digest_replay.go`/`v2_job_deadline_repair.go`/`v2_status_test.go` 等文件的继续编辑 |
| 文档状态 | `15/16/17/18/19/20` 与 `references/` 已提交；`README.md` 索引已更新 |

结论：G3/G3.5 已有可追溯提交；主工作区剩余的 memory 差异属于并发进行中的 FTS/G1 与少量后续修正，不代表已提交能力丢失。

---

## 4. 本轮可复现验证

工作目录：`backend/`

```text
go test ./internal/memorystore ./internal/memoryruntime ./internal/memoryassembly ./internal/config -count=1

ok  github.com/wwsheng009/ai-agent-runtime/internal/memorystore     30.751s
ok  github.com/wwsheng009/ai-agent-runtime/internal/memoryruntime   44.572s
ok  github.com/wwsheng009/ai-agent-runtime/internal/memoryassembly   2.092s
ok  github.com/wwsheng009/ai-agent-runtime/internal/config           2.540s
```

早先一次 API 全量定向测试曾因宿主分页文件不足失败：

```text
go test ./internal/api/runtimeapi -run 'TestMemory' -count=1

--- FAIL: TestMemoryAPIImportExportRoundTrip (0.55s)
panic: The paging file is too small for this operation to complete.
 .../github.com/ncruces/go-sqlite3/internal/sqlite3_wrap.(*Memory).commit
 .../internal/sqliteutil.OpenFileCtx
 .../internal/memorystore.(*Store).ensureV2
 .../internal/api/runtimeapi.(*Handler).ImportMemory
```

资源恢复后，提交前在隔离 worktree 以 `GOFLAGS=-p=1` 重新验证并通过：

```text
go test ./internal/api/runtimeapi \
  -run 'TestMemoryAPIRecordVersionsAndRollback|TestMemoryAPIRollbackAuthorizationWorkspaceAndValidation|TestMemoryAPIRecordAsOfRequiresAdminAndReturnsHistoricalSnapshot|TestMemoryAPI.*Core|TestMemoryStatus' \
  -count=1
ok  github.com/wwsheng009/ai-agent-runtime/internal/api/runtimeapi  4.620s

go test ./cmd/aicli/commands \
  -run 'TestStructuredMemoryHistoryRollbackAndSafeProjection|TestChatMemoryCore|TestStructuredMemoryV2Commands' \
  -count=1 -vet=off
ok  github.com/wwsheng009/ai-agent-runtime/cmd/aicli/commands  2.704s

go test ./internal/toolkit/tools -run 'TestMemory' -count=1 -vet=off
ok  github.com/wwsheng009/ai-agent-runtime/internal/toolkit/tools  3.773s

go test ./internal/tools -run 'TestMemory' -count=1 -vet=off
ok  github.com/wwsheng009/ai-agent-runtime/internal/tools  0.204s

go test ./internal/agent -run 'TestMemory|TestCore|TestRecordMemory' -count=1 -vet=off
ok  github.com/wwsheng009/ai-agent-runtime/internal/agent  0.282s

go test ./cmd/aicli/commands -run 'TestMemoryOnboarding|TestOnboarding' -count=1 -vet=off
ok  github.com/wwsheng009/ai-agent-runtime/cmd/aicli/commands  0.356s
```

关键包构建通过：memorystore/runtime/assembly、contextmgr、config、runtimeapi、toolkit/tools、tools、policy、commands、agent、`cmd/aicli`、`cmd/runtime-server`。

判定的边界：上述结果仅覆盖临时 SQLite fixture 与已提交能力；FTS 测试仍因 `no such module: fts5` 失败，G1 不因这些通过而关闭。

真实 `.aicli/memory/memory.db` 本轮未读取、未修改。`15` §1.4 的 37 条 pending digest、13 条 pending `phase1_extract/v2`、`memory_entries=0` 仍是 2026-11-22 的历史快照。

---

## 5. 文档一致性缺口

1. `README.md` 索引已补 `17/18/19/20`，顶部状态已注明 G3/G3.5 已提交。
2. `15` 是 2026-11-22 的历史审查，其中“无 versions/rollback”保留为历史结论；当前状态以 `19/20` 为准。
3. `16` §0.4/§19.10 仍是 2026-11-22 执行记录，G1–G5 描述属于当时状态；当前状态以 `19/20` 为准。
4. `17/18` 明确 G3.5 未关闭，状态正确；G3 的对应证据文件为 `20`。

本文件作为 2026-11-23 的当前快照；`15/16/17/18` 保留各自历史结论。

---

## 6. 风险

| 风险 | 影响 |
|---|---|
| 工作区仍在进行 FTS/G1 改造 | `v2_fts_test.go` 当前失败；若直接合并会引入不稳定测试 |
| G1 driver 阻断 | FTS/BM25 无法落地；检索质量与参考设计差距长期存在 |
| G0-5 未执行 | 无法证明真实库 backlog 可恢复、自动记忆可注入 |
| 宿主分页文件不足 | 大包集成测试偶发编译/链接失败；需分步、限并发验证 |
| 主工作区并发编辑 | 提交/合并需避开正在修改的 FTS 与 team/agent 文件 |

---

## 7. 建议执行顺序

1. **已完成**：G3/G3.5 提交、Core citation、onboarding、证据文档与 API/CLI/tool 定向验证。
2. **G1 决策**：先收敛工作区中的 FTS 实现与 driver 能力；启用 FTS5 或正式接受 `like_fallback`。未决策前 G1 保持 blocked。
3. **G2 优先于 G4/G5**：scope/tenant/owner/agent/task 列、backfill、fail-closed 读写与负向测试。
4. **G0-5**：先只读诊断 + `replay-preview` + `jobDeadlineRepair` dry-run，备份后再受控恢复。
5. **后续 memory 修正**：把 20:00 后对 `v2_core.go`/`v2_digest_replay.go`/`v2_job_deadline_repair.go` 等工作区改动与 FTS 改造分开提交。

---

## 8. 复现命令汇总

```text
cd backend
go test ./internal/memorystore ./internal/memoryruntime ./internal/memoryassembly ./internal/config -count=1
go test ./internal/memorystore -run 'TestFTS5CapabilityProbe' -count=1 -v
go test ./internal/memorystore -run 'TestRecordVersion|TestRecordRollback|TestListRecordsAsOf' -count=1
go test ./internal/api/runtimeapi -run 'TestMemory' -count=1   # 需足够内存/分页文件
go test ./cmd/aicli/commands -run 'Test(StructuredMemory|ChatMemory|HandleMemoryCommand|MemoryOnboarding)' -count=1
go test ./internal/agent -run 'TestMemory|TestCore|TestRecordMemory' -count=1
go test ./internal/toolkit/tools -run 'TestMemory' -count=1
go test ./internal/tools -run 'TestMemory' -count=1
```

注意：工作区若保留正在开发的 FTS 实现，`go test ./internal/memorystore` 会因 driver 缺少 FTS5 而失败；这是 G1 未关闭的预期状态，不应通过删除测试或伪装 LIKE 关闭。

```text
git -C <repo> status --short
git -C <repo> log -5 --format='%h %ad %s' --date=iso
```