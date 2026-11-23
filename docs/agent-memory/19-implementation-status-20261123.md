# AI Memory 实施状态快照（2026-11-23）

## 状态

**项目级 Agent Memory v2 主干已可运行；参考设计 `AI_Agent_Memory_Engine_Design v1.0` 的 G0–G5 门禁未整体关闭。**

本文件是只读检查快照，用于收敛 `15/16/17/18` 与当前工作区代码之间的状态差异。检查过程未修改 memory 代码，未读取、迁移或修改真实 `.aicli/memory/memory.db`。

关键事实：

- G3（记录版本、回滚、时间点查询）与 G3.5（Core Memory）已有代码和测试，但**尚未提交**；
- G0 代码闭环基本落地，但 **G0-5 真实 dogfood 恢复未执行**；
- G1 仍因 SQLite driver 缺少 FTS5 而 **blocked**；
- G2、G4、G5 未实施；
- 本轮 API 层定向测试因宿主分页文件/内存不足未取得结果，不能记为通过或失败。

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
| G3 版本/回滚/时间 | ✅ 代码级实现（未提交） | migration v20 `memory_versions`（append-only trigger）、v21 purge guard；`v2_versions.go:243/303/514`：`ListRecordVersionsPage`、`ListRecordsAsOf`、`RollbackRecord`；HTTP `/memory/records/{id}/versions`、`/rollback`；CLI `/memory history|versions`、`/memory rollback`；`v2_versions_test.go` 覆盖回滚、并发 revision、temporal as-of、purge fence | 未提交；无独立 G3 证据文档；`README/15/16` 仍记录“无 versions/rollback” |
| G3.5 Core Memory + 预算 | 🟡 部分实施 | migration v22 `memory_core`（单 active partial index）、v23 `memory_core_usage`；`v2_core.go`；只读工具 `memory_core_get`；HTTP `/memory/core`、`/memory/cores`、`/memory/core/rebuild`、`/memory/core/usage`；CLI `/memory core`；selector Core 注入 + 分类预算最小切片 | 见 `18` §未完成：Core 与 summary/pinned 去重与失效、usage 的 session-generation purge、独立运行态状态面、task/procedure/reference prompt 分区、1k–2.5k Core 预算校准均未完成 |
| G4 Episode + Dreaming | ❌ 未实施 | `memoryruntime/task_completion.go` 明确仅 future G4 boundary；`types.go:314/337` 同样是 hook；仅 `JobPriorityDreaming` 常量 | 无 Episode 表/持久化、无 Patch/Dreaming pipeline、无审核/应用/拒绝/回滚 |
| G5 混合检索/Trust/评测 | ❌ 未实施 | 无 entity/alias/relation 表，无 RRF/rerank，无 trust level，无 Recall@K/MRR/成本/延迟评测 | G5-R/G5-T/G5-Q 全部未关闭 |

---

## 3. 当前工作区状态

| 项 | 观测值 |
|---|---|
| 最新提交 | `960a40da 2026-11-23 16:56:33 +0800 feat: simplify plan mode on/off commands`（与 memory 无关） |
| `git status --short` | 232 项 |
| 已跟踪变更 | 164 个文件，`+14860 / −1446` |
| memory 相关变更 | 约 70 个文件，`+4232 / −353`（memorystore/runtime/assembly/API/CLI/contextmgr 等） |
| 文档状态 | `15/16/17/18` 与 `references/` 未跟踪；`README.md`、`04-data-model-and-api.md` 已修改 |

结论：G3/G3.5 的实现目前属于**工作区实现**，还不是可追溯的交付状态。提交前不应在对外状态中写成“已完成”。

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

API/CLI 定向测试本轮未取得有效结果：

```text
go test ./internal/api/runtimeapi -run 'TestMemory' -count=1

--- FAIL: TestMemoryAPIImportExportRoundTrip (0.55s)
panic: The paging file is too small for this operation to complete.
 .../github.com/ncruces/go-sqlite3/internal/sqlite3_wrap.(*Memory).commit
 .../internal/sqliteutil.OpenFileCtx
 .../internal/memorystore.(*Store).ensureV2
 .../internal/api/runtimeapi.(*Handler).ImportMemory
```

随后以 `GOFLAGS=-p=1` 重试单个 memory API 测试，编译阶段即因同一资源问题失败（`FAIL ... [build failed]`）。

判定：这是宿主 Windows 分页文件/内存不足导致的环境失败，**不是测试断言失败**；不能据此宣称 API 回归通过或失败。`16/18` 记录过该包此前通过，但本轮无新证据。

真实 `.aicli/memory/memory.db` 本轮未读取、未修改。`15` §1.4 的 37 条 pending digest、13 条 pending `phase1_extract/v2`、`memory_entries=0` 仍是 2026-11-22 的历史快照。

---

## 5. 文档一致性缺口

1. `README.md` 索引未收录 `17/18`（本文件新增后需一并补 `19`）。
2. `README.md` 与 `15` 仍写“无 `memory_versions`、无记录级回滚”，与工作区 v20/v21 实现不符。
3. `16` §0.4/§19.10 仍写“G1–G5 仍未实施”，与工作区 G3/G3.5 代码不符；该节是 2026-11-22 记录，需要追加 2026-11-23 状态段而不是改写历史结论。
4. `17/18` 明确 G3.5 未关闭，状态正确；但缺少 G3 的对应证据文件。

本文件作为 2026-11-23 的当前快照；`15/16/17/18` 保留各自历史结论。

---

## 6. 风险

| 风险 | 影响 |
|---|---|
| 大量 memory 变更未提交 | 无法回溯、无法按门禁拆分回滚；与文档状态脱节 |
| G3 无证据文档 | 即使测试通过，也不能按 `16` §0.2 关闭门禁 |
| G1 driver 阻断 | FTS/BM25 无法落地；检索质量与参考设计差距长期存在 |
| G0-5 未执行 | 无法证明真实库 backlog 可恢复、自动记忆可注入 |
| 宿主分页文件不足 | API 集成测试不可复现；CI/本机证据不稳定 |

---

## 7. 建议执行顺序

1. **提交 G3**：migration v20/v21、`v2_versions.go`、HTTP/CLI 面、`v2_versions_test.go`，补 `20-g3-record-versions-evidence-20261123.md`。
2. **提交 G3.5 切片**：v22/v23、Core 读写/rebuild/usage、预算切片；`17/18` 作为证据一并提交。
3. **同步文档**：README 索引补 `17/18/19`；`16` 追加 2026-11-23 状态段；保留 `15` 历史结论。
4. **资源恢复后补跑**：`internal/api/runtimeapi` 的 `TestMemory*` 与 `cmd/aicli/commands` 的 `Test(StructuredMemory|ChatMemory|HandleMemoryCommand)`，记录真实输出。
5. **G1 决策**：启用 FTS5 driver 或正式接受 `like_fallback` 并调整验收口径；未决策前 G1 保持 blocked。
6. **G2 优先于 G4/G5**：scope/tenant/owner/agent/task 列、backfill、fail-closed 读写与负向测试。
7. **G0-5**：先只读诊断 + `replay-preview` + `jobDeadlineRepair` dry-run，备份后再受控恢复。

---

## 8. 复现命令汇总

```text
cd backend
go test ./internal/memorystore ./internal/memoryruntime ./internal/memoryassembly ./internal/config -count=1
go test ./internal/memorystore -run 'TestFTS5CapabilityProbe' -count=1 -v
go test ./internal/memorystore -run 'TestRecordVersion|TestRecordRollback|TestListRecordsAsOf' -count=1
go test ./internal/api/runtimeapi -run 'TestMemory' -count=1   # 需足够内存/分页文件
go test ./cmd/aicli/commands -run 'Test(StructuredMemory|ChatMemory|HandleMemoryCommand)' -count=1
```

```text
git -C <repo> status --short
git -C <repo> log -5 --format='%h %ad %s' --date=iso
```