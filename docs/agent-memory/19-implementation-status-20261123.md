# AI Memory 实施状态快照（2026-11-23）

## 状态

**项目级 Agent Memory v2 主干已可运行；参考设计 `AI_Agent_Memory_Engine_Design v1.0` 的 G0–G5 门禁未整体关闭。**

本文件是只读检查快照，用于收敛 `15/16/17/18` 与当前工作区代码之间的状态差异。检查过程未修改 memory 代码，未读取、迁移或修改真实 `.aicli/memory/memory.db`。

关键事实：

- G3（记录版本、回滚、时间点查询）与 G3.5（Core Memory）已有代码和测试，并已提交到 `main`（`8513f579`/`a9c7a461`/`eb413e1f`）；
- G0 代码闭环基本落地，但 **G0-5 真实 dogfood 恢复未执行**；
- G1 检索基础已提交：FTS5 driver（`91972cb5`）、FTS 索引/同步/BM25/fallback（`8ddd48c1`）、统一 `SearchRecords`（`c8acdc30`）、有界注入候选（`22c4d03a`）、候选查询索引（`72b275ce`）、store-level 多值过滤（`0443f6ad`）与 `memory_search` 多值接线（`5d972874`）；1k/10k/100k 性能基准已记录，HTTP/CLI 多值接线与质量证据待补；
- G2 scope 隔离主要实现已提交（`a4fca327`）；jobs/digests/summaries/export/import/purge/audit 与 runtime-server 多 workspace 证据仍待补；G4/G5 未实施；
- API/CLI 定向测试此前在宿主资源恢复后通过；本次当前工作区 API 重跑被未提交的 `internal/supervision` 签名漂移阻塞；FTS driver 扩展与 FTS 实现均已提交并通过 memory 包测试。

---

## 1. 检查范围与方法

| 项 | 内容 |
|---|---|
| 文档对照 | `15` 缺口审查、`16` 施工计划、`17/18` G3.5 证据、`README` 索引 |
| 代码对照 | `memorystore`、`memoryruntime`、`memoryassembly`、`api/runtimeapi`、`cmd/aicli/commands` |
| schema 对照 | `memorystore/v2_schema.go`：`HEAD` 已提交 migration v10–v25（v25 为注入候选查询索引） |
| 测试 | 定向 Go 包测试（仅临时 SQLite fixture） |
| 真实 dogfood DB | **未访问**；`15` 中的积压数字仅为 2026-11-22 快照，未在本轮复核 |

验证口径：代码存在不等于门禁关闭。门禁关闭仍需提交、可复现测试、证据记录，以及 `16` §0.2 要求的失败/跳过项说明。

---

## 2. 门禁状态矩阵

| 门禁 | 状态 | 代码/文档证据 | 未关闭原因 |
|---|---|---|---|
| Memory v2 主干 | ✅ 可用 | `memorystore` + `memoryruntime` + `memoryassembly` + `contextmgr` + API/CLI/Web；capture receipt → extract → consolidate → summary → inject → usage/citation | 属于既有项目级能力，不等于参考设计全量验收 |
| G0 运行时闭环 | 🟡 代码/临时库证据已落地 | `16` §19.1–19.10：status、backlog 诊断、job operator、replay-preview、deadline repair、active-session capture | **G0-5 未执行**：未对真实 backlog 做只读分类、dry-run 与受控恢复；无真实 `memory_entries > 0`、v2 job succeeded、跨会话注入证据 |
| G1 检索基础 | 🟡 部分实施（FTS5 + 统一检索 + 有界注入） | `91972cb5` FTS5 driver；`8ddd48c1` FTS 索引/同步/BM25/fallback；`c8acdc30` `SearchRecords`；`22c4d03a` 有界注入候选；`72b275ce` 候选查询索引；`2404cd83` 实际 backend label；`0443f6ad` store-level 多值过滤；`5d972874` tool 多值；win7compat 全量通过；证据见 `21` | v25 后 1k/10k/100k 候选查询为 `2.61/2.94/9.54 ms/op`，覆盖索引且无临时 B-tree；仍缺 HTTP/CLI 多值接线与三端同一 corpus 证据；不能宣称 G1 关闭 |
| G2 Scope 隔离 | 🟡 部分实施（已提交 `a4fca327`） | migration v24 scope 列/索引/backfill；`v2_scope.go` ScopeContext/grants/fail-closed visibility；list/search/FTS/usage/versions/governance/API 接入；7 个 scope 测试；证据见 `22` | jobs/digests/summaries/export/import/purge/audit 未逐一证明；runtime-server 多 workspace 证据未记录；不能宣称 G2 关闭 |
| G3 版本/回滚/时间 | ✅ 代码级实现（已提交 `8513f579`） | migration v20 `memory_versions`（append-only trigger）、v21 purge guard；`v2_versions.go:243/303/514`：`ListRecordVersionsPage`、`ListRecordsAsOf`、`RollbackRecord`；HTTP `/memory/records/{id}/versions`、`/rollback`；CLI `/memory history|versions`、`/memory rollback`；`v2_versions_test.go` 覆盖回滚、并发 revision、temporal as-of、purge fence | 证据见 `20`；门禁仍受 G1 检索验收、G2 尚未关闭的运营面、G4 高风险 supersede 依赖限制 |
| G3.5 Core Memory + 预算 | 🟡 部分实施（已提交 `8513f579`/`a9c7a461`） | migration v22 `memory_core`（单 active partial index）、v23 `memory_core_usage`；`v2_core.go`；只读工具 `memory_core_get`；HTTP `/memory/core`、`/memory/cores`、`/memory/core/rebuild`、`/memory/core/usage`；CLI `/memory core`；selector Core 注入 + 分类预算最小切片 | 见 `18` §未完成：Core 与 summary/pinned 去重与失效、usage 的 session-generation purge、独立运行态状态面、task/procedure/reference prompt 分区、1k–2.5k Core 预算校准均未完成 |
| G4 Episode + Dreaming | ❌ 未实施 | `memoryruntime/task_completion.go` 明确仅 future G4 boundary；`types.go:314/337` 同样是 hook；仅 `JobPriorityDreaming` 常量 | 无 Episode 表/持久化、无 Patch/Dreaming pipeline、无审核/应用/拒绝/回滚 |
| G5 混合检索/Trust/评测 | ❌ 未实施 | 无 entity/alias/relation 表，无 RRF/rerank，无 trust level，无 Recall@K/MRR/成本/延迟评测 | G5-R/G5-T/G5-Q 全部未关闭 |

---

## 3. 当前工作区状态

| 项 | 观测值 |
|---|---|
| memory 提交 | `8513f579` G0–G3.5 capabilities；`a9c7a461` Core citation/预算选项；`eb413e1f` onboarding preset；`91972cb5` FTS5 driver；`8ddd48c1` FTS 索引；`c8acdc30` 统一检索；`22c4d03a` 有界注入候选；`a4fca327` G2 scope 隔离；`72b275ce` v25 候选查询索引；`2404cd83` 实际 backend label；`0443f6ad` store-level 多值过滤；`5d972874` tool 多值过滤；`0bfb0450` CLI 测试 |
| `main` HEAD | `5d972874`（tool 多值过滤；v25 在 `72b275ce`，G2 证据在 `d11ee26c`） |
| `git status --short` | 约 150 项（本轮复核期间；主要为 team/workspace/suspension/analytics/前端、supervision 及文档工作区改动；并发编辑持续变化，应以实时命令结果为准） |
| 已提交 memory 变更 | 首批 98 文件 `+17678 / −530`，另有 Core citation 4 文件与 onboarding 3 文件 |
| memory 工作区状态 | G2 scope 代码已随 `a4fca327`、v25 候选查询索引已随 `72b275ce` 提交；当前未见 `backend/internal/memorystore` 的未提交跟踪改动 |
| 文档状态 | `15/16/17/18/20/22`、`README.md` 与 `references/` 已由 `d11ee26c` 提交；本文件 `19` 与 G1 证据 `21` 仍有工作区修改 |

结论：G2 scope 基础层、G3/G3.5、FTS5 检索与 v25 候选查询索引已有可追溯提交；主工作区还有并发进行的 team/workspace/supervision 等工作流及文档修改；G2 仍因运营面和多 workspace 证据不足而未关闭。

### 整体闭环判定（发布口径）

**结论：当前不能把 Agent Memory 视为整体闭环，也不能作为一个完整、可交付、可运行、可验证的功能发布。**

目前存在的是“基础链路可运行 + 多个能力分阶段施工”，而不是一个已经通过端到端验收的产品闭环：

| 范围 | 当前判定 | 说明 |
|---|---|---|
| Memory v2 基础路径 | 🟡 技术路径存在 | `capture → extract → consolidate → summary → inject → usage/citation` 已有代码和临时 SQLite 定向测试；尚未证明真实 dogfood、真实 backlog 恢复以及跨会话注入 |
| 记录生命周期 | 🟡 子流程可用 | record/search/version/rollback 的代码和部分 API/CLI/tool 测试存在，但不能替代全链路验收 |
| 隔离与运营 | 🟡 未闭环 | G2 的基础 scope 已有；jobs、digests、summaries、export/import、purge、audit 及 runtime-server 多 workspace 证据不完整 |
| 完整设计能力 | ❌ 未完成 | G4 Episode/Dreaming 与 G5 混合检索/Trust/评测尚未实施 |

阻断整体交付的不是某一个索引或接口，而是缺少一个固定的 vertical slice 发布门禁：同一 owner/workspace、同一 corpus，需要从写入或 capture 开始，经持久化、检索、注入、引用/usage、版本操作和失败恢复，最后由 HTTP/CLI/tool 以一致结果通过端到端测试。当前文档只有分阶段 G0–G5 状态，没有把这条完整逻辑作为单独的“通过/不通过”验收项。

在整体闭环前，至少必须完成以下最小发布路径：

1. 使用可信 owner/workspace capture 一条记录并完成 extract/consolidate/summary；
2. 通过统一 `SearchRecords` 检索同一条记录，并在后续会话完成有界注入；
3. 返回 citation/usage/status，验证版本、rollback、purge 的生命周期语义；
4. 覆盖 job 失败、重试/恢复、scope 负向授权以及 HTTP/CLI/tool 同一 corpus；
5. 在当前可构建工作区或隔离提交上运行端到端测试，并补真实 dogfood 证据。

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

go test ./internal/memorystore -run 'TestMemoryFTS|TestFTS5CapabilityProbe' -count=1 -v
fts_available
--- PASS: TestMemoryFTSExactIdentifierAndCJKRSearch
--- PASS: TestMemoryFTSStaysSynchronizedAcrossUpdateDeleteAndReopen
--- PASS: TestMemoryFTSFallsBackForShortQueries
ok  github.com/wwsheng009/ai-agent-runtime/internal/memorystore  1.054s

go test ./internal/memorystore -count=1
ok  github.com/wwsheng009/ai-agent-runtime/internal/memorystore  30.546s

go test ./internal/memorystore -run 'TestSearchRecords' -count=1
ok  github.com/wwsheng009/ai-agent-runtime/internal/memorystore  0.528s

go test ./internal/api/runtimeapi \
  -run 'TestMemoryAPIRecordsLifecycleAndStatus|TestMemoryAPIRecordsSourceKindFilter|TestMemoryStatus' \
  -count=1
ok  github.com/wwsheng009/ai-agent-runtime/internal/api/runtimeapi  2.404s
```

先前快照中关键包曾构建通过：memorystore/runtime/assembly、contextmgr、config、runtimeapi、toolkit/tools、tools、policy、commands、agent、`cmd/aicli`、`cmd/runtime-server`；本次当前工作区 API 重跑未进入测试阶段，先被未提交的 `internal/supervision` 签名漂移阻塞。

判定的边界：上述结果仅覆盖临时 SQLite fixture；FTS driver、索引/同步、BM25 排序、LIKE fallback、统一 `RecordSearchQuery`/`SearchRecords`、store-level 多值过滤、tool 多值接线、win7compat 全量测试、1k/10k/100k 基准与实际 backend label 均已提交/验证，但 HTTP/CLI 多值接线、三端同一 corpus 质量证据仍不足，因此 G1 不因测试通过而关闭。

本次核对在当时的 `HEAD=72b275ce`（G2 代码基线为 `a4fca327`，v25 索引已提交）追加验证；当前 `HEAD` 已推进到 `0443f6ad`。

```text
go test ./internal/memorystore -run 'TestMemoryScope|TestSearchRecords|TestMemoryFTS|TestFTS5CapabilityProbe' -count=1
ok  github.com/wwsheng009/ai-agent-runtime/internal/memorystore  2.603s

go test ./internal/memorystore -count=1
ok  github.com/wwsheng009/ai-agent-runtime/internal/memorystore  51.616s

go test ./internal/api/runtimeapi \
  -run 'TestMemoryAPIRecordsScopeIsolation|TestMemoryAPIRecordsLifecycleAndStatus|TestMemoryAPICore|TestMemoryAPIRecordVersionsAndRollback|TestMemoryStatus' \
  -count=1
FAIL（当前未提交的 internal/supervision 签名漂移导致编译失败，未进入 memory API 测试）
  wake_consumer.go / wake_self_check.go: MarkWakeDelivery*、ReleaseWakePending 参数缺少新增的 time.Time
```

此前在该工作区的较早状态中 API 定向测试曾通过（`5.750s`）；由于随后出现上述非 memory 工作区改动，不能把该结果当作当前整树 API 通过证据。`memoryruntime`、`memoryassembly`、`config` 的此前通过结果同样保留为先前快照证据。

真实 `.aicli/memory/memory.db` 本轮未读取、未修改。`15` §1.4 的 37 条 pending digest、13 条 pending `phase1_extract/v2`、`memory_entries=0` 仍是 2026-11-22 的历史快照。

---

## 5. 文档一致性缺口

1. `README.md` 索引已补 `17/18/19/20`，顶部状态已注明 G3/G3.5 已提交。
2. `15` 是 2026-11-22 的历史审查，其中“无 versions/rollback”保留为历史结论；当前状态以 `19/20` 为准。
3. `16` §0.4/§19.10 仍是 2026-11-22 执行记录；当前的 G1/G2 增量见其 §19.11–§19.12 及已由 `d11ee26c` 提交的 `22`，本文件的 follow-up 尚未提交。
4. `17/18` 明确 G3.5 未关闭，状态正确；G3 的对应证据文件为 `20`。

本文件作为 2026-11-23 的当前快照；`15/16/17/18` 保留各自历史结论。

---

## 6. 风险

| 风险 | 影响 |
|---|---|
| G1 检索验收未完成 | `RecordSearchQuery`/`SearchRecords`、FTS5/BM25、CLI/HTTP/tool 单值统一入口、store-level 与 tool 多值过滤、win7compat 全量测试、1k/10k/100k 基准与实际 backend label 已存在；仍缺 HTTP/CLI 多值接线、三端同一 corpus 质量证据 |
| FTS 索引是派生数据 | 迁移/重建/损坏恢复需依赖 store 的 ensure/rebuild 逻辑，不能当作事实源 |
| G0-5 未执行 | 无法证明真实库 backlog 可恢复、自动记忆可注入 |
| 宿主分页文件不足 | 大包集成测试偶发编译/链接失败；需分步、限并发验证 |
| 主工作区并发编辑 | FTS/G2 scope/v25 索引代码已提交；supervision、team/agent、workspace、前端和文档仍有未提交改动，提交/合并需分开处理 |

---

## 7. 建议执行顺序

1. **已完成**：G3/G3.5 提交、Core citation、onboarding、证据文档与 API/CLI/tool 定向验证。
2. **G1 收尾**：FTS 索引、driver、`SearchRecords`、有界注入候选、v25 查询索引、1k/10k/100k 基准、实际 backend label、store-level 多值过滤、tool 多值与 win7compat 验证已完成；下一步补 HTTP/CLI 多值接线与三端同一 corpus 证据。
3. **G2 收尾**：主要 scope 列/backfill/fail-closed 读写与负向测试已提交；下一步补 jobs/digests/summaries/export/import/purge/audit 的 scope 审计与 runtime-server 多 workspace 证据。
4. **G0-5**：先只读诊断 + `replay-preview` + `jobDeadlineRepair` dry-run，备份后再受控恢复。
5. **文档/工作区收尾**：提交本文件 `19` 与 `21` 的 follow-up，并将 supervision/team/agent 等并发改动分开处理；`16/20/22/README` 已在 `d11ee26c` 提交，v25 已在 `72b275ce` 提交。

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

注意：`sqliteutil` 已自动注册 FTS5，`TestFTS5CapabilityProbe` 返回 `fts_available`；FTS 代码、v25 候选查询索引、100k 基准与实际 backend label 已落地，G1 的检索质量证据仍待补。migration v24 的 G2 scope 基础层已提交，但 G2 尚未整体关闭。

```text
git -C <repo> status --short
git -C <repo> log -5 --format='%h %ad %s' --date=iso
```