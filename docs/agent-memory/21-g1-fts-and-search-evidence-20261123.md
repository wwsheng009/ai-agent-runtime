# G1 FTS5 与统一检索证据（2026-11-23）

## 状态

**FTS5 能力、索引同步、BM25 排序与统一 `SearchRecords` 已落地并提交；G1 仍不能关闭。**

已提交：

- `91972cb5`：`sqlitedriver/register_fts5.go` 在主line自动注册 ncruces FTS5 扩展（`!win7compat`）。
- `8ddd48c1`：`memory_entries_fts` external-content FTS5 表、trigram tokenizer、AI/AD/AU 触发器、`bm25()` 排序、短查询/CJK LIKE fallback。
- `c8acdc30`：`memorystore.SearchRecords` 统一 HTTP、CLI `/memory search`、`memory_search` 工具。
- `0bfb0450`：CLI status 测试接受 FTS5 backend。
- `22c4d03a`：注入候选改为 pinned（100）+ manual（100）+ FTS/recent（200）的有界集合，替换全量 active 扫描。

未关闭原因：

1. 注入候选裁剪已提交并有 450 条记录的功能测试，但还没有 10k/100k 基准证据；
2. 统一 SearchQuery 目前仍是单 status/kind/tag；多状态、多 kind/tags 结构化过滤与 G2 scope 字段尚未接入；
3. CLI/HTTP/tool 使用同一 store 方法已通过定向测试，但还没有一个跨三端同一 corpus 的端到端证据包；
4. win7compat 构建使用 pinned driver 的内嵌 FTS5，需要单独构建验证。

---

## 1. 实施内容

### 1.1 driver 能力

`backend/internal/sqlitedriver/register_fts5.go`：

```go
//go:build !win7compat
func init() { sqlite3.AutoExtension(fts5.Register) }
```

- 主line driver 的 FTS5 是可选扩展；不注册时 `CREATE VIRTUAL TABLE ... USING fts5` 报 `no such module: fts5`。
- win7compat 使用 pinned driver 的内嵌 Wasm SQLite，已包含 FTS5，且没有 `ext/fts5` 包，因此排除。

### 1.2 FTS 索引

`backend/internal/memorystore/v2_fts.go`：

- `memory_entries_fts(title, text, summary, tags_json)`，`content='memory_entries'`，`content_rowid='rowid'`，`tokenize='trigram'`；
- AI/AD/AU 触发器同步 insert/update/delete；
- 首次创建或 trigger 缺失时执行 `rebuild`；
- FTS5 不可用时删除 FTS 对象，普通写入继续使用 LIKE fallback；
- 文本查询使用 `MATCH ?` + `ORDER BY bm25(...)`，短于 3 rune 或 FTS 失败时回落到有界 LIKE；
- `Status().SearchBackend` 返回 `fts5` 或 `like_fallback`。

### 1.3 统一检索入口

`backend/internal/memorystore/v2_search.go`：

```go
type RecordSearchQuery struct {
    Text, Kind, Tag, SourceKind, SourceSessionID string
    Status Status
    PinnedOnly, IncludeArchived bool
    AsOf *time.Time
    Limit int
    Cursor string
}
type RecordSearchHit struct { Record Record; Score float64; MatchSource string }
type RecordSearchPage struct { Items []RecordSearchHit; NextCursor string; Count int; Backend string }
```

`Store.SearchRecords` 映射到现有 `RecordFilter` / `ListRecords`，所有调用方共享 FTS5、LIKE fallback、project/status/freshness/suppression 过滤与分页。

已切换：

- `GET /api/runtime/memory/records`：响应新增 `search_backend`；
- CLI `/memory search`（structured + legacy）：显示 `match=fts5|like_fallback`；
- `memory_search` 工具：`diagnostics.search_backend`。

---

## 2. 验证记录

### 2.1 FTS 能力与索引

```text
go test ./internal/memorystore -run 'TestFTS5CapabilityProbe|TestMemoryFTS' -count=1 -v
fts_available
--- PASS: TestMemoryFTSExactIdentifierAndCJKRSearch
--- PASS: TestMemoryFTSStaysSynchronizedAcrossUpdateDeleteAndReopen
--- PASS: TestMemoryFTSFallsBackForShortQueries
ok  internal/memorystore  1.054s
```

### 2.2 统一检索

```text
go test ./internal/memorystore -run 'TestSearchRecords' -count=1 -v
--- PASS: TestSearchRecordsUsesUnifiedFTSAndMatchesListRecords
--- PASS: TestSearchRecordsShortQueryFallsBackToLike
--- PASS: TestSearchRecordsSupportsLegacyJSONLMode
ok  internal/memorystore  0.528s

go test ./internal/api/runtimeapi \
  -run 'TestMemoryAPIRecordsLifecycleAndStatus|TestMemoryAPIRecordsSourceKindFilter|TestMemoryStatus' \
  -count=1
ok  internal/api/runtimeapi  2.404s

go test ./cmd/aicli/commands \
  -run 'TestStructuredMemoryHistoryRollbackAndSafeProjection|TestChatMemory|TestHandleMemoryCommand_AddListSearch' \
  -count=1
ok  cmd/aicli/commands  3.115s

go test ./internal/toolkit/tools \
  -run 'TestMemoryToolsLifecycle|TestMemoryToolsRecentWhyAndWhyNot|TestMemoryCoreGet' \
  -count=1
ok  internal/toolkit/tools  3.366s
```

### 2.3 全包回归（干净 worktree）

```text
go test ./internal/memorystore -count=1
ok  internal/memorystore  37.365s
```

### 2.4 注入候选裁剪

```text
go test ./internal/memorystore \
  -run 'TestInjectionUsesBoundedCandidatesWithPinnedAndFTSTopK|TestSelectForInjectV2NoMatchKeepsEmptySelection' \
  -count=1 -v
--- PASS: TestInjectionUsesBoundedCandidatesWithPinnedAndFTSTopK (3.73s)
--- PASS: TestSelectForInjectV2NoMatchKeepsEmptySelection (0.21s)
```

测试覆盖 450 条无关 active 记录 + 1 条 query needle + pinned 记录；选区保留 pinned/needle，`CandidateCount <= 300`。

关键包构建通过：memorystore、runtimeapi、toolkit/tools、commands。
`cmd/aicli` / `cmd/runtime-server` 的完整构建当前受主工作区未完成的 supervision/team 改动阻塞，不属于本切片。

---

## 3. G1 退出条件对照

| 条件 | 结果 | 说明 |
|---|---|---|
| G1-0 全文索引能力 | ✅ | `fts_available` |
| FTS 同步/回填/重建/降级 | ✅（临时库） | trigger + rebuild + 短查询 fallback |
| `memory_search`/HTTP/CLI 使用同一检索 | ✅ | 均调用 `SearchRecords`；定向测试通过 |
| 自动抽取记忆在 CLI/HTTP/tool 可见 | ✅ | 现有 source-kind/active 测试通过 |
| 精确标识符/CJK/路径/函数名/版本号 | ✅（store 层） | `TestMemoryFTSExactIdentifierAndCJKRSearch` |
| 注入路径不全表加载 + 基准 | 🟡 | `loadInjectableCandidates` 已提交，450 条记录测试通过；缺 10k/100k 基准 |
| 多 status/kind/tags 与 scope 过滤 | ❌ | 当前 SearchQuery 仍单值；G2 未实施 |
| 三端同一 corpus 端到端证据 | 🟡 | 定向测试通过；尚无单一 corpus 证据包 |

---

## 4. 后续

1. 补 10k/100k benchmark 与 why-not 截断证据；
2. 扩展多 status/kind/tags 结构化过滤，接入 G2 scope；
3. 建立 CLI/HTTP/tool 同一 corpus 的端到端测试；
4. 补 win7compat 构建验证；
5. 上述完成后才可关闭 G1。