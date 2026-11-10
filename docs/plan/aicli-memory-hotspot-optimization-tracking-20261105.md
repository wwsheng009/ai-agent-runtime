# aicli 内存热点优化实施跟踪

更新时间: 2026-11-05
状态: **P0 + 性能核查补强已完成；尚待新二进制重启后的 pprof 差分验证**

关联分析:
- `tmp/aicli-mem-20261105/REPORT.md`
- `tmp/aicli-mem-final/REPORT.md`（旧二进制 16:50 复采，用于核对剩余热点）

## 1. 背景

2026-11-05 对 `backend/aicli-4x.exe` 做了在线诊断。结论是：没有发现无界泄漏；主要问题是高分配率、Go 堆高水位，以及若干可避免的重复加载/深拷贝。旧二进制 `31020` 实例在运行约 6.5 小时后累计分配已达 439 GB，live heap 168 MB、Go 已提交堆约 376 MB、MaxRSS 约 573 MB。

本跟踪只记录可独立验证、可回滚的优化，不把架构重构混入同一轮。

## 2. 已完成范围

### P0-1 MCP 工具面

- [x] `Registry.ToolCountByMCP`：只读计数，不为 `ToolCount` 克隆 schema。
- [x] `mcpStatusLocked` 不再调用 `ListToolsByMCP`。
- [x] `AgentAdapter.ListTools` 单次列表内缓存 governance/max-parallel status。
- [x] `tools.Manager.ListTools` 为 remote-name collision guard 做 server-scope 缓存。
- [x] `AgentAdapter.ListTools` 复用 descriptor metadata 的 `mcp_name`，不再逐工具 `FindTool` 和 schema clone。
- [x] 回归测试固定 48 工具/单 server 时：`GetMCPStatus ≤ 2`、`FindTool == 0`。

### P0-2 usage analytics / checkpoint 元数据读取

- [x] 新增 `loadSessionMetadataBestEffort`：优先 `LoadMetadata`，旧存储回退 `Load`。
- [x] `localSessionMetaLookup.SessionMeta` / `SessionLineage` 不再加载 prompt 历史。
- [x] `preserveLatestGoalMetadata` 不再加载 prompt 历史，不再逐 Message 深拷贝。
- [x] `SessionActor.persistSession` 合并 broker aliases / plan mode / MCP override 时改用 `loadSessionMetadataForMerge`。
- [x] 回归测试固定 metadata reader 与 fallback 语义。

### P0-3 Web sessions cache

- [x] `chatWebSessionsVersion` 流式 SHA-256，返回 64 hex digest。
- [x] cache key 从完整版本字符串变为摘要，不再长期保留数十 KB/数百 KB 的 key。
- [x] `webHTTPResponseCache` 增加 8 MiB key+body 总预算。
- [x] get/insert 主动清理 TTL 过期项；超大单响应不缓存。
- [x] 回归测试覆盖 digest 稳定性、字节预算和过期回收。

### P0-4 Web todo negative cache

- [x] “canonical 未找到 todo”的负结果缓存 2 秒。
- [x] 以 session ID + canonical message count 作为栅栏。
- [x] tool_end 写入新 todo 时清负缓存。
- [x] canonical 命中的正向结果不写 live tool_end cache，避免遮蔽更新。
- [x] 回归测试覆盖负缓存命中、计数变化和 canonical 正向不缓存。

### P0-5 TUI DeliveredCellIDs

- [x] `HistoryCommitLedger` 增量维护 delivered cell 计数。
- [x] `DeliveredCellIDs` 按需构建并缓存只读 bool snapshot，不再每次扫描全部 `byToken`。
- [x] `deliveryFacts` 直接消费 snapshot，不再二次复制 bool map。
- [x] 测试证明同一 ledger 状态下连续读取复用同一 snapshot。

## 3. 已补做的核查修正

| 发现 | 初版缺口 | 修正 |
|---|---|---|
| MCP server scope | 只缓存 adapter status，manager collision guard 仍逐工具查询 | `remoteScopedServers` per-list cache |
| MCP schema clone | adapter 仍逐工具 `FindTool` | 使用 descriptor `mcp_name` identity，`FindTool == 0` |
| checkpoint context merge | 只修 usage/goal，`persistSession` 仍 full Load | `loadSessionMetadataForMerge` |
| Web todo | canonical 正向误写 live cache | 正向结果不缓存，只缓存 negative |
| TUI eviction guard | `DeliveredCellIDs` 每次扫描并复制 | incremental counts + reusable bool snapshot |

旧二进制最新 pprof 中，这些路径仍是主要热点：

- `GetMCPStatus` / `mcpStatusLocked` / `ListToolsByMCP`：约 91.6 GB
- `SQLiteSessionStorage.Load`：约 71.8 GB，其中 usage/goal/persist 是主要调用者
- `syncHistoryEffectsForTranscript`：约 54.6 GB
- `DeliveredCellIDs`：约 9.35 GB

当前工作树已覆盖这些代码路径，但上述数字是**旧二进制累计值**，不能当成新代码已消除的证明。

## 4. 当前验证

### 构建与静态检查

```text
go build ./cmd/aicli ./cmd/aicli/ui ./cmd/aicli/commands \
  ./internal/chat ./internal/tools ./internal/mcp/manager ./internal/mcp/registry
go vet ./cmd/aicli/ui ./cmd/aicli/commands ./internal/chat \
  ./internal/tools ./internal/mcp/manager ./internal/mcp/registry
gofmt -l <changed Go files>
git diff --check
```

结果：全部通过，无格式/空白错误。

### 单元测试

```text
ok  cmd/aicli/ui        294.071s
ok  internal/tools        8.051s
ok  internal/chat        50.800s
ok  internal/mcp/manager  0.706s
ok  internal/mcp/registry 2.131s
ok  cmd/aicli/commands  253.051s
```

### Benchmark

48 个 MCP tools + `NewDefaultManager`（包含本地 builtin）：

```text
BenchmarkAgentAdapterListTools-16
约 5.2–5.9 ms/op
约 1.619–1.627 MB/op
约 15082–15086 allocs/op
```

同口径上一轮：

```text
约 6.4–7.2 ms/op
约 1.75–1.76 MB/op
约 16917–16938 allocs/op
```

本轮补做后下降约 7–8% 字节、11% 分配次数；该 benchmark 不是生产真实 schema 大小，主要价值是防止复杂度回弹。

### 补丁二进制

```text
go build -o tmp/aicli-mem-final/aicli-4x-patched.exe ./cmd/aicli
./tmp/aicli-mem-final/aicli-4x-patched.exe --version
```

构建和版本 smoke test 成功。

## 5. 到位性判断

### 已到位

- MCP 热路径的 O(N²) 放大已基本移除。
- usage analytics 和 checkpoint metadata 已不再为了少量 context 键反序列化 prompt history。
- Web sessions cache 的 20–23 MB 版本字符串/响应滞留已有明确预算。
- Web todo canonical 负缓存已消除高频重复读。
- TUI `DeliveredCellIDs` 的重复扫描/复制已移除。
- 相关 package 全部测试、build、vet、gofmt 通过。

### 尚未到位

1. **没有新二进制 pprof 差分。**
   - 当前运行的 `aicli-4x.exe` 仍是旧版本。
   - 代码层优化和 benchmark 只能证明路径被改写，不能证明端到端 RSS/GC 降低幅度。

2. **MCP tool list 仍是 O(N) schema clone。**
   - collision guard / adapter 不再 O(N²)，但 `m.mcp.ListTools()` 和 `Manager.ListTools()` 仍每次复制所有 tool schema。
   - 下一步需要 registry generation + surface cache，而不是盲目 TTL cache。

3. **legacy projection 每次 Load 可能扫 canonical。**
   - `healLegacyTruncatedProjection` 缺少持久化“已检查/healed” fence。
   - usage/persist 调用次数减少后，绝对压力会下降，但对未压缩大会话仍可能重复扫描。

4. **Web screen 仍先构建 body 再算 body ETag。**
   - 304 请求仍可能承担完整 transcript 构建和 `json.MarshalIndent`。
   - 需要内容版本指纹设计，不能仅靠 body hash。

5. **TUI layout/plan 本身仍是高分配源。**
   - `syncHistoryEffectsForTranscript` 旧 profile 54.6 GB。
   - 本轮只处理 delivered snapshot；完整 transcript plan/layout 增量仍需独立设计和 benchmark。

## 6. 下一步

- [ ] 在受控终端替换/启动 `aicli-4x-patched.exe`，开一个相同长会话，采集：
  - `TotalAlloc`
  - NumGC / GC CPU
  - `AgentAdapter.ListTools`
  - `SQLiteSessionStorage.Load`
  - `usageanalytics.handleEvent`
  - `HandleChatWebAPISessions`
  - `syncHistoryEffectsForTranscript`
  - `DeliveredCellIDs`
- [ ] 比较同负载 30–60 分钟 `alloc_space`，目标先验证 MCP + usage/persist 两项。
- [ ] 若 MCP 仍高，增加 registry/tool-surface generation cache。
- [ ] 设计 `projection_heal_fence`（metadata-only），避免无 compaction marker 的长会话反复扫 canonical。
- [ ] Web screen 引入 cheap revision/ETag，并在 revision 命中时跳过完整构建。
- [ ] 针对 TUI plan/layout 做独立 benchmark，再决定增量规划与 clone 策略。

## 7. 回滚边界

- 本轮未提交、未暂存，可按模块 revert：
  - MCP：`registry.go`、`manager.go`、`agent_adapter.go`
  - chat/persist：`actor.go`、`session.go`、goal/usage commands
  - Web：sessions cache/todo snapshot
  - TUI：`history_commit.go`、controller/queue/trim
- `DeliveredCellIDs` API 返回类型从 `map[CellID]struct{}` 改为 `map[CellID]bool`，外部调用需一并升级；当前仓库内调用和测试已同步。

## 11. 新二进制运行态复验（2026-11-05 22:17–22:24）

### 二进制口径

```text
backend/aicli-4x.exe 19:54 build
tmp/aicli-mem-final/aicli-4x-patched.exe 17:24 build
cmp: identical
```

因此本轮运行态 pprof 可以作为优化后代码的证据。

### 重启前三个较长生命周期实例

| PID | 会话 | RSS | Private | HeapAlloc | 累计分配 | GC | MaxRSS |
|---|---|---:|---:|---:|---:|---:|---:|
| 18600 | wechat resume | 247–261 MB | 324–338 MB | 101.3 MB | 58.4 GB | 803 | 290 MB |
| 23804 | SAP resume | 43–122 MB（working-set trim） | 263–267 MB | 74.0 MB | 34.7 GB | 651 | 268 MB |
| 24616 | current | 182 MB | 261 MB | 66.4 MB | 1.42 GB | 47 | 214 MB |

旧二进制（12:55 构建、运行约 6.5h）在 16:50 的对照：

- `31020`：439 GB、168 MB live、MaxRSS 573 MB
- `16748`：180 GB、104 MB live、MaxRSS 378 MB

### 热点消除证据（以 `18600` 旧/新同口径 profile）

| 路径 | 优化前累计 | 重启后累计 | 结果 |
|---|---:|---:|---|
| `usageanalytics.handleEvent` | 56.4 GB | 0.23 GB | 约 99.6% 下降 |
| `AgentAdapter.ListTools` | 101.4 GB | 1.45 GB | 约 98.6% 下降 |
| `GetMCPStatus` | 91.6 GB | profile 中未进入热点 | 状态 fan-out 消除 |
| `SQLiteSessionStorage.Load` | 71.8 GB | 0.083 GB | 约 99.9% 下降 |
| `syncHistoryEffectsForTranscript` | 54.6 GB | 3.47 GB | 约 93.6% 下降 |
| `DeliveredCellIDs` | 9.35 GB | 未进入 top | 增量 snapshot 生效 |

注意：这些不是完全相同负载的 A/B，只能证明主要调用路径已从 profile 消失/大幅收缩。

### 重启前残留的新热点

`18600` 重启后最大路径不再是 MCP/usage/Load，而是：

```text
SessionActor.persistSession          20.45 GB (36.5%)
└ checkpointSessionHistory           20.24 GB (36.2%)
  └ SQLiteSessionStorage.Update      20.07 GB (35.9%)
    ├ rebuildProjectionFromStoredBaseTx 9.41 GB (16.8%)
    │ ├ loadCanonicalTailMessagesTx      5.28 GB
    │ └ loadPromptProjectionMessagesTx   4.10 GB
    └ canonicalAppendStartTx          5.25 GB
```

该 session 当前：

- canonical 8326→8976，增长且活跃 checkpoint
- prompt projection 920→1568
- `storage.read_queries=3634`
- persist queue 未积压（`queue_depth=0`）

因此这是新的主要性能优化候选，但尚未形成旧实现那种全历史反复 Load 的行为。下一步应减少 checkpoint 事务内 canonical tail/prompt 重复反序列化，或为 projection rebuild 增加持久化 revision fence。

### 最新快照（22:24）

三个 `sessions --all` CLI 子进程已退出，当前只有三个 agent 会话：

| PID | RSS | Private | 说明 |
|---|---:|---:|---|
| 11752 | 122.2 MB | 241.0 MB | wechat resume |
| 21484 | 125.1 MB | 188.9 MB | SAP resume |
| 27792 | 207.4 MB | 278.8 MB | current repository session |

合计约：

```text
RSS ≈ 455 MB
Private commit ≈ 709 MB
```

系统总内存 13.9 GB，当前可用约 4.5 GB。进程集合刚重启约 1 分钟，尚未观察到持续泄漏特征；但长生命周期复验仍需运行 30–60 分钟后再采一次 profile。

## 12. 新热点定位与优化（2026-11-05 22:2x）

### 12.1 运行态证据

当前 `backend/aicli-4x.exe` 仍是 19:54 构建的 P0–P5 版本，尚未包含本节新优化。WeChat 会话实例
`tmp/new-hotspot/wechat_t0.pb.gz` / `wechat_t1.pb.gz` 的 60 秒 `alloc_space` 差分总量为：

```text
187.81 MB / 60s
```

主要路径：

```text
TerminalSessionExecutor.run        130.68 MB (69.58%)
└ ComposeTerminalFramePlan          48.63 MB
  ├ normalizeRow                    17.64 MB
  ├ ScreenModel.Resize              16.13 MB
  └ ScreenModel.Clone               15.11 MB
eventHub.appendJournalLocked        10.06 MB
```

pprof caller 确认 `Clone` 与 `Resize` 的 100% 调用者都是 `flushTransactionLocked`。

### 12.2 TUI 结构帧重复整屏分配

旧逻辑只要视口位置或尺寸变化：

```text
candidateScreen = s.screen.Clone()
candidateScreen.Resize(...)
```

`Resize` 会立即丢弃 Clone 得到的 front/back，因此每一帧先复制两张完整网格，再分配两张空白网格，
实际制造了四张整屏网格；前两张完全无用。

修复：

- `renderengine.ScreenModel.NewResizedModel(width,height)` 直接分配一张脱离已确认状态、
  `ProjectionUnknown + forceRepaint` 的空白候选，并保留诊断 trace。
- `flushTransactionLocked` 在 `needsResize` 时走新空白候选；不改变尺寸但仍需保留 staged 内容的
  租约/重建结构帧继续走 Clone。
- 失败回滚契约不变：已确认 `s.screen` 不被候选修改，成功时才替换。

验证：

```text
go vet ./cmd/aicli/ui/renderengine ./cmd/aicli/ui
gofmt / git diff --check
renderengine full package: PASS
cmd/aicli/ui full package: PASS (270.114s)
```

新增测试：

```text
TestScreenModelNewResizedModelReturnsDetachedBlankCandidate
```

### 12.3 checkpoint 稳态分配

当前仓库会话 120 秒差分：

```text
checkpointSessionHistory + SQLiteSessionStorage.Update   14.68 MB (24% of 61 MB)
└ loadMatchingPromptRowsTx                               11.11 MB (约 88% of Update)
```

`loadMatchingPromptRowsTx` 为了确认 caller history 是 stored prompt projection 的前缀，逐行执行
`json.Marshal(history[index])`；每次 checkpoint 都为完整前缀分配新的 JSON。此外 canonical tail/full
canonical loader 查询了实际未使用的 `preview_json`（以及 tail 的 `seq`），进一步放大稳态 SQL 解码分配。

修复：

- 增加 `promptPayloadEncoder`，在一个 checkpoint 事务内复用 `bytes.Buffer + json.Encoder`；
  `json.Encoder` 的输出契约与 `json.Marshal` 一致，仅移除每行一次 payload 分配。
- `appendPromptMessageTx` 复用同一 encoder，并且返回的 projection row 不再保留 DB 写入后即可丢弃的
  payload，只保留 message 和 size。
- canonical tail/full loader 仅读取 payload、artifact、byte_count、sha256；不扫描未使用 preview/seq。

### 12.4 后续运行态验收

源码修复完成不代表进程 RSS/profile 已下降。下一步必须：

1. 重新构建包含 12.2 / 12.3 的新 `aicli-4x.exe`；
2. 重启三个长生命周期会话；
3. 采至少 60–120 秒同进程 `alloc_space` baseline/delta；
4. 确认：
   - `ScreenModel.Clone` 从 structural-frame top 中消失或大幅下降；
   - `normalizeRow + NewResizedModel` 的合计低于旧 Clone/Resize/StageFrame 合计；
   - `loadMatchingPromptRowsTx` 稳态分配率显著下降；
   - `preview_json` 不再出现在 checkpoint pprof。

次要新热点 `eventHub.appendJournalLocked` 当前约 10 MB/60s；checkpoint 优化后若仍进入 top，再对 event
journal 做 ownership 优化，避免 publisher 与 hub 重复克隆。本地 auto-compaction 的 662 MB 是一次性
fit-search 峰值而非稳态分配，优先级低于上述两项。

### 12.5 结构帧候选分配基准

`BenchmarkScreenModelStructuralResizeCandidates`（120×30，每次创建一个结构帧候选）：

```text
CloneResize      305662 ns/op   740436 B/op   125 allocs/op
NewResizedModel  106876 ns/op   370260 B/op    63 allocs/op
```

候选物理网格分配量精确下降 50%，分配次数下降约 50%，耗时下降约 65%。此基准只覆盖旧
`Clone + Resize` 的重复网格部分，端到端收益还包含 StageFrame 与 composer 分配，需重启后的
live pprof delta 再确认。

### 12.6 checkpoint 编码基准与最终验证

`BenchmarkPromptProjection*`（每次操作编码 64 条投影消息）：

```text
json.Marshal          31983 ns/op   18455 B/op   192 allocs/op
reusable encoder      25534 ns/op   14363 B/op   128 allocs/op
```

新增字节等价测试 `TestPromptPayloadEncoderMatchesJSONMarshal`，覆盖 Unicode、tool-call 嵌套 map 和 HTML 转义。

最终源码树验证：

```text
go build ./cmd/aicli/ui/renderengine ./cmd/aicli/ui ./internal/chat       PASS
go vet ./cmd/aicli/ui/... ./internal/chat                                PASS
go test ./cmd/aicli/ui/renderengine                                       PASS (1.692s)
go test ./cmd/aicli/ui                                                   PASS (270.114s)
go test ./internal/chat                                                  PASS (44.710s)
gofmt / git diff --check                                                  clean
```

包含本节修复的新二进制：

```text
backend/tmp/aicli-mem-hotspot/aicli-4x-hotspotfix.exe
```

当前三个运行中的会话仍加载旧 `backend/aicli-4x.exe`，因此它们的实时 pprof 不包含 12.2/12.3。必须用上述新二进制重启后，再做 60–120 秒差分，才能把 benchmark 收益转化为运行态 RSS/profile 验收。

### 12.7 event journal ring 与所有权快照

WeChat 60 秒差分中 `eventHub.appendJournalLocked` 约 10.06 MB。原实现有两笔重复成本：

1. `cloneOutputEvent(ev)` 为 journal 再分离一次；
2. `h.journal = h.journal[1:]` 保留已淘汰前缀，达到 item capacity 后又通过 append 分配整份新 backing。

修复：

- journal 改为动态环形数组，达到配置的 `MaxItems` 后覆盖最旧槽位，不再 re-slice；
- 淘汰槽位清零，释放旧事件引用的字符串/指针；
- `appendJournalLocked` 直接取得 publisher 已构造的事件快照，不再为 journal 二次克隆；
- subscriber mailbox 与 `RecentEvents` 外部读取仍逐事件 clone，公开隔离语义不变。

新增测试：

```text
TestEventHubJournalRingKeepsNewestSuffixInOrder
TestEventHubJournalByteBudgetEvictsOldest
TestEventHubSnapshotDetachesPointerFields
```

验证：

```text
go vet ./cmd/aicli/ui/render/output                         PASS
go test ./cmd/aicli/ui/render/output                        PASS (2.272s)
gofmt / git diff --check                                    clean
```

新二进制已重新包含三项热点修复，仍位于：

```text
backend/tmp/aicli-mem-hotspot/aicli-4x-hotspotfix.exe
```

### 12.8 三项修复后的最终源码树验证

包含 12.2（TUI）、12.3（checkpoint）、12.7（event journal）后的最终结果：

```text
go build -p 1 ./cmd/aicli/ui/renderengine ./cmd/aicli/ui ./internal/chat       PASS
go vet ./cmd/aicli/ui/... ./internal/chat                                     PASS
go test ./cmd/aicli/ui/renderengine                                           PASS (1.692s)
go test ./cmd/aicli/ui/render/output                                          PASS (2.272s)
go test ./cmd/aicli/ui                                                       PASS (274.803s)
go test ./internal/chat                                                       PASS (44.710s)
gofmt / git diff --check                                                      clean
```

最终新二进制：

```text
backend/tmp/aicli-mem-hotspot/aicli-4x-hotspotfix.exe   57 MB
```

该二进制尚未被现有三个长生命周期进程加载；运行态 RSS/60–120s pprof delta 需重启后验收。
## 13. 2026-11-08 在线复采：高水位而非泄漏

### 13.1 现场证据

对 8 个运行中的 `aicli*.exe` 做 pprof + MemStats 采样（新二进制 2026-11-08 09:49 构建）：

| PID | 会话/形态 | HeapAlloc | HeapSys | Private | NextGC |
|---|---|---:|---:|---:|---:|
| 9852 | resume，5936 canonical messages | 159.2 MB | 422.8 MB | 437.5 MB | 319.4 MB |
| 18972 | resume，1924 canonical messages | 128.6 MB | 402.2 MB | 429.9 MB | 258.1 MB |
| 26800 | aicli-4x，旧二进制 11-07 | 209.4 MB | 564.9 MB | 587.8 MB | 364.0 MB |

结论：

- **没有无界泄漏特征**。强制 GC 后 live heap 稳定在 128–209 MB；8 分钟差分中
  `inuse_space` 仅小幅波动，没有单调增长。
- **Private 高来自堆高水位**：`HeapAlloc` 只占 `HeapSys` 的 35–40%，剩余是
  GOGC=100 允许的扩容余量、未完全清空的 in-use span、以及未归还的空闲 span。
  `HeapReleased` 只有 47–124 MB，Windows 上空闲期不会立刻把 HeapSys 降回。
- 新二进制稳态分配已从旧版本的数百 GB 降到约 2 MB/s；当前最大剩余热点是
  `LayoutTranscript`（8 分钟约 240 MB）和 SQLite canonical tail 解码，但它们是
  分配抖动，不是 live 泄漏。

### 13.2 本轮修复

1. **默认 GOGC 从 100 收紧到 50**（`memory_limit.go`）。
   - 存活堆 159 MB 时 NextGC 从约 319 MB 降到约 239 MB；
   - 用户设置 `GOGC` 或 `AICLI_GOGC` 时仍以用户值为准；
   - `AICLI_GOGC=off/none/0/-1` 保留显式关闭语义。
2. **默认 GOMEMLIMIT 从 512 MiB 降到 384 MiB**，给超大存活堆一个软边界
   （GOMEMLIMIT 不会 OOM-kill，超限只提高 GC 频率）。
3. **新增空闲堆归还循环**（`memory_reclaim.go`）：
   - 每 30 秒采样一次 `TotalAlloc`；
   - 连续 2 分钟累计分配 < 4 MiB 且已提交空闲堆 ≥ 64 MiB 时调用一次
     `debug.FreeOSMemory`；
   - 10 分钟冷却，避免把主动归还变成周期性全局 GC；
   - 只在实际空闲时触发，不干扰流式输出与交互输入。
   - `AICLI_MEMORY_RECLAIM=off`（或 `GOGC=off` 且未用 `AICLI_GOGC`
     覆盖）可显式关闭。

### 13.3 验证

```text
go test ./cmd/aicli -count=1                    PASS
go vet ./cmd/aicli                              PASS
GOFLAGS=-modfile=go.win7.mod go test ./cmd/aicli PASS
GOFLAGS=-modfile=go.win7.mod go vet ./cmd/aicli   PASS
go build -o tmp/aicli-mem-fix/aicli.exe ./cmd/aicli   PASS
tmp/aicli-mem-fix/aicli.exe --version           PASS
gofmt / git diff --check                        clean
```

新增测试锁定：默认/覆盖/关闭三种 GC 百分比语义、空闲判定边界、冷却窗口与
循环停止。运行中的旧进程不会自动获得本轮修复，需要替换二进制并重启会话后
复采 Private/HeapSys 才能看到端到端收益。

### 13.4 第二轮：直接消除分配峰值（2026-11-08）

上一轮的 GOGC/GOMEMLIMIT/空闲归还只是控高水位，没有减少真实分配。本轮按
pprof 的 `alloc_space` 热点做了四项代码级优化：

1. **请求体序列化不再"整字段 Marshal + 复制"**
   - `MarshalProviderRequestBody` 直接用 `json.Encoder` 写入最终 `bytes.Buffer`，
     只去掉 Encoder 追加的结尾换行。
   - 旧实现对每个顶层字段先 `json.Marshal(body[key])` 出一份同尺寸 `[]byte`，
     messages 字段在长会话可达数十 MB；新实现不再产生这次峰值副本。
   - 基准（40 messages + 12 tools 的合成请求）：
     `21.9 KB/op → 15.5 KB/op`（-29%），`76 → 73 allocs/op`。
   - 大字段基准（单条 4 MiB message）：
     `12.6 MB/op → 8.4 MB/op`（-33%），`11.0 ms/op → 9.2 ms/op`；
     等价于每个请求少一次与 messages 同尺寸的临时副本。
   - 新增逐字节等价测试，覆盖 HTML 转义、Unicode、嵌套 map、`json.RawMessage`、
     nil、浮点和大整数。

2. **HTTP 调试指纹不再重复编码大字段**
   - 新增 `parseHTTPDebugRawFields`：从已经序列化的请求体里切出
     `messages/input/tools/instructions/prompt_cache_key` 的原始 JSON 字节，
     直接做 SHA-256；不再为每个字段重新 `json.Encoder.Encode`。
   - 指纹值与旧路径逐字节一致（包括 Encoder 的结尾换行），因此
     `messages_sha256/cache_surface_sha256/request_sha256` 的语义不变。
   - 基准（有 reporter，40 messages + 12 tools）：
     `36.1 ms/op → 13.2 ms/op`（-63%），`97 → 41 allocs/op`（-58%）。
   - 大字段基准（单条 4 MiB message）：
     `1.41 MB/op, 9.60 ms/op` → `3.7 KB/op, 4.43 ms/op`
     （分配字节 -99.7%，耗时 -54%）。
   - 新增测试锁定新旧路径指纹等价、非法 body 回退、字段原始切片解析。

3. **transcript 全量布局行复用 backing array**
   - 新增 `scene.LayoutTranscriptInto(dst, ...)` 与
     `TranscriptState.LayoutRowsInto`。
   - planner 的 `syncHistoryEffectsForTranscript` 通过
     `UIControllerState.transcriptLayoutRows` 跨 planning pass 复用语义行切片；
     容量不足才重新分配，并预留 12.5% 增长余量。
   - transcript 收缩时清理复用 buffer 的尾部，避免旧 cell 正文被钉住。
   - 基准（6,719 cells / ~161k 行）：
     `hot: 8.93 MB/op, 4.20 ms/op` → `reuse: 0.53 MB/op, 2.85 ms/op`
     （字节 -94%，耗时 -32%）。
   - 完整 planner 基准（3,000 cells，含 screen + mint）：
     `40.3 MB/op, 210,744 allocs/op, 42.3 ms/op` →
     `31.2 MB/op, 113,619 allocs/op, 24.0 ms/op`
     （字节 -22.5%，分配次数 -46%，耗时 -43%）。
   - pprof 中 `LayoutTranscript` 累计约 24 GB；按每轮 rows 分配估算，复用后
     该路径的累计分配可下降一个数量级。

4. **裁剪路径零分配 CellsView**
   - `TuiScene` 新增 `CellsView()`，仅供已持 scene 锁的只读消费者使用。
   - `applySceneTrimPolicyLocked` 改用 `CellsView()`，不再每轮复制 O(cells)
     的指针切片。旧 pprof 中 `TuiScene.Cells` 约一半分配来自这里。
   - `Snapshot()` 仍返回独立切片，COW/快照不可变契约不变。

5. **空闲期释放可重建缓存**
   - `ui.TrimIdleCaches()` 在空闲归还窗口内把三类布局缓存预算临时压到
     1 MiB 触发逐出，再恢复原预算；同时清空宽度缓存与结构化文档渲染缓存。
   - 这部分是**真实 live heap 下降**：布局缓存约 24 MB、width cache 约
     6.5 MB，以及 renderengine document cache。
   - 只在连续 2 分钟累计分配 < 4 MiB 时触发；下一次用户交互按原预算重新
     填充，语义状态不受影响。

验证：

```text
go test ./internal/llm -count=1                 PASS
go test ./cmd/aicli/ui -count=1                 PASS (298s)
go test ./cmd/aicli/commands -count=1           PASS (240s)
go test ./cmd/aicli/ui/scene -count=1           PASS
go test ./cmd/aicli/commands -run TestSceneTrim|TestResidencyStatus PASS
go vet ./internal/llm ./cmd/aicli/ui/... ./cmd/aicli/commands ./cmd/aicli PASS
```

> 结论：本轮优化直接减少了请求构造/调试指纹/全量布局三条最大分配路径的
> 瞬时峰值与累计字节；上一轮的 GC/上限调整保留为高水位兜底，不再作为主要
> 修复手段。