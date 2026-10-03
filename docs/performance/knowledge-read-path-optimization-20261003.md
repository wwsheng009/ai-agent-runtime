# 知识层读路径优化：实施与验证记录

- 日期：2026-10-03
- 根因分析：[tui-turn-latency-root-cause-20261003.md](./tui-turn-latency-root-cause-20261003.md)
- 状态：P0/P1 已实施并通过验证；**P2 实测否决，未落地**（结论见 §5）
- 约束：不引入原生 SQLite，**全程 CGO=0**（纯 Go WASM 引擎）
- 验证库：`.aicli/knowledge/knowledge.db`（355 MiB / files 5,134 / symbols 56,453 / refs 501,382 的只读副本）

## 1. 修复清单

### P0-a 断开放大链（收益最大）

| 项 | 位置 | 说明 |
|---|---|---|
| `skillToolSurface.ResolveToolSource` | `backend/cmd/aicli/commands/chat_skill_tool_surface.go:88` | 补上便宜路径：内层 surface 认得就转发，skill 函数按逻辑 MCP 面报 `mcp`，都不认得返回空串 |

这是最外层 `agent.mcpManager`（`chat_actor_host.go` 的 `wrapSkillToolSurface`），
缺这个方法会让 `resolveToolSourceForRequest` 的类型断言整体失败，85 个工具全部
回落到 `FindTool`。补上后归类变成注册表查表，**不再重建工具表、不再跑 Stats**。

### P0-b 只取真正需要的标量 + 快照复用

| 项 | 位置 | 说明 |
|---|---|---|
| `sqliteStore.IndexedAt` | `backend/internal/knowledge/store_sqlite_read.go:418` | 档位判定只需要快照时间戳，用一次 `MAX` 查询替代四个聚合；与 `Stats.IndexedAt` 共用 `statsIndexedAtSQL`，口径不会漂移 |
| `indexedAtReader` 结构化快路径 | `backend/internal/tools/code_index_resolver.go:138` | 按能力探测，`Store` 接口不变；识别不到（例如测试替身）自动回退 `Stats` |
| 快照视图缓存 | `backend/internal/tools/code_index_resolver.go:172,191,233` | `filePaths/FileStamps/IndexedAt` 按 `(db 路径, workspace)` 缓存 10 s；库文件 size/mtime 变化立即失效 |

细节：
- `codeIndexSnapshotTTL = 10 * time.Second`（`var`，便于测试收紧）。
- `writer` 与陈旧度按**调用时刻**重算，不吃 TTL —— 缓存回来的只是库内 `IndexedAt`，
  时间推进带来的陈旧度增长不受影响。
- 每次解析仍**逐次拷贝**三张映射：历史行为是"每次解析返回全新映射"，直接共享
  缓存对象会让两处调用互相看见对方的改动（`codeIndexResolver` 句柄对调用方可见）。
- 只缓存成功结果：库读失败不落缓存，下次重试。

### P1 让 `Stats` 本身变便宜

| 项 | 位置 | 说明 |
|---|---|---|
| refs 子查询强制 join 顺序 | `backend/internal/knowledge/store_sqlite_read.go:355` (`statsRefsSQL`) | 用 `CROSS JOIN` 钉死 files → refs；语义与原来的 `refs JOIN files` 逐字等价（仍排除软删文件） |
| 覆盖索引迁移 | `backend/internal/knowledge/migrations/0005_stats_covering_index.sql` | `idx_symbols_ws_deleted ON symbols(workspace_id, deleted_at)` |

为什么必须显式钉 join 顺序：SQLite 自选的是 `SEARCH r USING INDEX idx_refs_from`
再逐行回探 `files` 主键（50 万次）；反过来用几千个文件做外层、内层走
`idx_refs_file` 探测快约 3 倍。`CROSS JOIN` 是 SQLite 中禁止优化器重排两侧的写法。

为什么 files 侧**不带** `workspace_id` 过滤：保持与原始谓词逐字等价，避免把
"refs 指向另一个 workspace 的文件"这类脏数据从计数里悄悄漏掉。

## 2. 实测结果

### 2.1 单次解析取数（真实库副本，生产同款驱动）

```
writer 打开 + 应用 0005 迁移     166 ms   schema=v5
OpenStore(readOnly)               3 ms
FindWorkspace                     0 ms
Stats()（新 SQL）               544 ms   files=5134 symbols=56453 refs=501382
Stats 旧 SQL（同库同驱动）       1712 ms
ListActiveFiles                  19 ms   files=5134
IndexedAt()（快路径）              0 ms   ×10 次共 2 ms

单次工具面解析取数：旧 1712 ms（Stats 四聚合） → 新 19 ms
```

即**单次解析取数 1712 ms → 19 ms（约 90×）**；快照 TTL 命中的解析 ≈ 0 ms。

子查询与 join 顺序的逐项测量（同一副本）：

| 方案 | 耗时 |
|---|---|
| 原 SQL：`refs JOIN files`（SQLite 自选 refs→files） | 1,524 / 1,507 ms |
| 新 SQL：`CROSS JOIN` 强制 files→refs（语义等价） | **518 / 490 ms** |
| `refs ... file_id IN (SELECT id FROM files ...)` | 1,294 / 1,240 ms |
| `(SELECT id FROM files ...) JOIN refs`（优化器仍可重排） | 1,553 ms |
| symbols COUNT（旧索引 `idx_symbols_qualified`） | 265 ms |
| symbols COUNT（新索引 `idx_symbols_ws_deleted`） | **11 ms** |
| 全量 Stats：旧 SQL | 1,781 / 1,908 ms |
| 全量 Stats：新 SQL + 新索引 | **567 ms** |

`Stats` 整体 **1,712 ms → 544 ms（约 3.1×）**。该出口仍服务
`/api/runtime/knowledge/status` 与 `aicli knowledge status`。

### 2.2 CPU profile 对照

| | 修复前（旧二进制） | 修复后（新二进制，含修复） |
|---|---|---|
| 窗口 / 采样 | 20 s / **21.27 s（106%）** | 8 s / **2.19 s（27%）** |
| `knowledge.(*sqliteStore).Stats` | **19.0 s（89.3%）** | **不出现**（`-peek` 无采样） |
| `_sqlite3VdbeExec`（SQLite 引擎） | 19.31 s（90.79%） | 0.09 s（4.11%） |
| `computeAvailableTools` | 19.3 s | **0.06 s（2.74%）** |

修复后 `computeAvailableTools` 的 0.06 s 里，0.05 s 是
`skillToolSurface.ListTools` 真正构建描述符的成本，0.01 s 是参数归一化 ——
**这就是这条路径应有的成本**。

### 2.3 真实会话端到端（新会话 `session_20261003113010_9HVebUYP`）

新进程 `aicli.exe` 构建于 **11:30:02**（含本轮全部改动）。
`GET /web/api/knowledge/status` 确认路径确实被走到：
`mode=on, role=reader, schema_version=5, workspace=E:\projects\ai\ai-agent-runtime`。

| 回合 | `duration_ms` | steps |
|---|---|---|
| `turn_51120e5c` | 1,276 | 1 |
| `turn_a4707cd7` | 1,124 | 1 |
| `turn_2d372a60` | 1,365 | 1 |

一次完整分解（`turn_51120e5c`）：

```
11:34:03.019  注入 prompt（POST /web/api/input）
03:34:03.529  llm.request.started        ← 本地开销 510 ms
              LLM duration_ms=1061（first_token_ms=1051）
11:34:04.xxx  turn.finished duration_ms=1276
```

对照根因文档：**回合 153.9 s → 1.1–1.4 s；输入 → 首次 LLM 请求 133–144 s → 0.27–0.51 s**。
回合时间的 80%+ 现在是模型自身。

## 3. 新增回归测试

| 测试 | 钉住的不变量 |
|---|---|
| `backend/internal/knowledge/stats_fastpath_test.go` | `IndexedAt ≡ Stats.IndexedAt`（空库 / 单文件回退 / 成功 light / 失败 light / 截断 light 五种状态）；软删文件的 refs 不计入 |
| `backend/internal/tools/code_index_resolver_cache_test.go` | 热路径**一次都不许调 `Stats`**；TTL 内复用快照；库文件变化与 TTL 到期失效；缺 `IndexedAt` 能力时回退 `Stats` 且可用性不变 |
| `backend/internal/agent/tool_source_cost_test.go` | surface 实现 `ResolveToolSource` 时**不得回落 `FindTool`**（计数为 0）；未实现时兜底语义不变 |
| `backend/cmd/aicli/commands/chat_skill_tool_surface_test.go` | 本 surface 必须实现 `ResolveToolSource`，转发内层结论、skill 函数报 `mcp`、不认识返回空串，且全程不调 `FindTool` |

`internal/tools` 里用"内嵌 `knowledge.Store` 接口"的替身：未覆写的方法一旦被调用
即 panic，把"不该走的路径"变成硬失败而不是静默通过。

## 4. 构建与测试状态

构建（均通过）：

```powershell
cd backend
go build ./...                                                   # 默认
$env:CGO_ENABLED="0"; go build -p 1 ./internal/knowledge/... ./internal/tools/... `
    ./internal/agent/... ./cmd/aicli/commands/...                # 显式 CGO=0
go build -p 1 -tags win7compat ./internal/knowledge/... ./internal/tools/...   # Win7 兼容标签
go build -modfile=<abs>\go.win7.mod -mod=mod -p 1 -tags win7compat `
    ./internal/knowledge/... ./internal/tools/...                # Win7 工具链钉子（ncruces v0.22 + embed）
```

测试：

| 包 | 结果 |
|---|---|
| `internal/knowledge` | ok（全量 39.5 s） |
| `internal/tools` | ok |
| `internal/agent` | ok |
| `cmd/aicli/commands` | 改动范围内测试全绿（`TestSkillToolSurface*` / `TestLocalChatToolPolicy*`）；**全量套件当时不能作为闸门**，原因见下 |

`cmd/aicli/commands` 全量套件在本次验证时不可用，原因不是本轮改动：

1. 该工作区当时**正在被并行编辑**（两次连跑失败集合完全不同）。其中
   `TestChatInteractiveDirectWriterInventory` 是直接统计源码文件里
   `fmt.Print`/`os.Std*` 行数的**清单测试**，源码在变它就必然失败。
2. 编译测试二进制时出现过 `fatal error: runtime: cannot allocate memory`
   （本机 14 GB 内存，3 个 `gopls` 占约 4.4 GB）——这是**构建期 OOM**，日志中
   没有任何 `--- FAIL:` 行。
3. `internal/knowledge` 的 watcher 计时类用例在同机高负载下偶发失败，
   单独重跑 0.65 s 通过。

复跑建议（待编辑落定后）：

```powershell
cd backend
go test -p 1 ./cmd/aicli/commands/ -count=1
```

## 5. P2：PRAGMA 调优 —— 实测否决，未落地

原计划：给 knowledge 只读连接加 `PRAGMA cache_size` / `PRAGMA mmap_size`。
动机是旧 profile 里 68% 的 CPU 花在 `vfsRead → os.File.ReadAt` 的 `pread` 上。

### 5.1 测量结果

同一份 355 MiB 真实库，跑修复后的 `Stats`：

| 设置 | 三次耗时 |
|---|---|
| 默认 `cache_size=-2000`（2 MiB） | 511 / 494 / 507 ms |
| `cache_size=-16384`（16 MiB） | 527 / 509 / 504 ms |
| `cache_size=-65536`（64 MiB） | 522 / 508 / 474 ms |
| `PRAGMA mmap_size` 读回 | **`no rows in result set`**（设置无任何反应） |

### 5.2 为什么两个旋钮都无效

**`cache_size`**：profile 里的读盘是**全表扫描的首次触碰读页**，页缓存对"每页只读
一次"没有帮助；而原先真正的病是**重复**（同一重查询被跑 57 遍）。P0-a 把它降到
一遍，P0-b 干脆换成一次 `MAX` —— **已经没有东西需要缓存了**。反过来把每连接
缓存从 2 MiB 提到 64 MiB 是实打实的内存开销，在内存受限环境上是纯亏。

**`mmap_size`**：不是配置问题，是**平台不支持**。驱动 `go-sqlite3@v0.35.6` 的
`vfs/` 里 `NewMemoryMapper` 只有两份实现，靠 build tag 二选一：

| 文件 | build tag | 行为 |
|---|---|---|
| `vfs/map.go` | `linux \|\| darwin \|\| freebsd` | 真映射 |
| `vfs/map_other.go` | `!(linux \|\| darwin \|\| freebsd)` | 返回 `nil`，PRAGMA 被跳过 |

注意这个集合**比 unix 小**：openbsd / netbsd / dragonfly / illumos 有完整的锁与
shm 实现，但**没有** mmap mapper。

### 5.3 结论与后续设计（若将来需要）

**不落地**：在唯一可测的平台（Windows）上两个旋钮都是空转；提交"看起来对但测不出
效果"的 PRAGMA 违反本目录的记录约定。

如果将来要在 Linux/macOS/FreeBSD 部署，正确做法**不是**写 `switch runtime.GOOS`
（那要把第三方驱动的平台矩阵抄进我们代码，驱动一升级就漂移），而是**运行期能力探测**：

```go
// 探测而非假设：先设一次再读回，只有平台/VFS 真的实现 xMmap 才会拿到 > 0。
// Windows 上是 no rows（sql.ErrNoRows），静默跳过；必须 best-effort，
// 探测失败绝不能让 store 打不开。
if _, err := db.ExecContext(ctx, "PRAGMA mmap_size="+strconv.Itoa(mmapBytes)); err != nil {
    return
}
var got int64
if err := db.QueryRowContext(ctx, "PRAGMA mmap_size").Scan(&got); err != nil || got <= 0 {
    return
}
// 生效：建议打一行日志，便于在目标平台上自证而不是靠猜
```

该方案在 Windows 上的"跳过"分支可测（本文即为其证据）；Linux 上的收益是
**有依据的假设**（mmap 正是消掉 profile 里那批 pread），但**未实测**，不得写成结论。

## 6. 风险、兼容与回滚

| 风险 | 说明 | 处置 |
|---|---|---|
| schema v4 → v5 | 迁移 0005 使库版本升高；**writer 迁移之前，只读进程会拒绝打开**（`store schema v4 is older than this binary (v5)`）。表现为 code.* 工具降级为 grep 回退（fail-closed），不是崩溃 | 启动 owner 进程（aicli TUI）一次即完成迁移；实测 writer 打开 + 迁移 166 ms |
| 快照 TTL 10 s | 索引刚推进时，档位可能最多晚 10 s 升档 | 方向保守：`IndexedAt` 偏旧只会让陈旧度更大、档位更低；文件指纹偏旧只会判 stale 或"无法判定"，工具回退 grep。**不会把陈旧当新鲜** |
| 缓存规模 | 快照缓存条目数 = `(db 路径, workspace)` 组合数，与既有的只读句柄缓存同阶且不主动淘汰 | 与现有 `codeIndexCache` 行为一致；每个 workspace 一份，生产上由 workspace 数量界定 |
| 回滚 | 三处改动互相独立 | 可单独回退任一修复；迁移 0005 是纯加索引，回退代码后残留索引无害 |
| 原生 SQLite | 未引入 | 四种构建组合均验证通过，包括 Win7 工具链钉子 |

## 7. 一句话总结

网络从来不是瓶颈：**是 agent 在把工具面构建跑了 57 遍，每遍都在 355 MiB 知识库上
做一次 50 万行聚合。** 断开放大链、只取需要的标量、把剩下的那次查询做便宜，
回合时间从 153.9 s 回到 1.1–1.4 s，而 LLM 依然是其中最大的一块。
