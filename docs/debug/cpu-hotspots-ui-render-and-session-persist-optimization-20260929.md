# 会话 CPU 热点分析与优化方案（UI 渲染 + 会话持久化）

> 目标会话：`session_20260927073805_QbWBceF5`（pid 4168）
> 采集时间：2026-09-29 18:20 CST ｜ 环境：Windows + pwsh ｜ 数据来源：`http://127.0.0.1:52977/debug/pprof/`
> profile 留档：`logs/pprof/aicli_cpu_4168_20260929_1820.pprof`（`logs/` 已被 .gitignore 忽略）
> 状态：P0-1、P0-3 已实施并验证（2026-09-29）；P0-2 待定（见 §7/§8）

## 1. 结论摘要

30 秒 CPU profile（29.65s 采样 / 98.6% 覆盖）与进程级 CPU 采样显示：高 CPU 由**两条可优化路径**叠加，另有系统调用/IO 与 GC 放大副作用：

| 优先级 | 热点 | 实测占比（cum） | 根因一句话 | 预期收益（需复测验证） |
|---|---|---|---|---|
| P0-1 ✅ | `toolFoldTargetRows → toolFoldOmits → BuildPreview` | 5.88s / 19.8% | 每次布局 pass 对每个折叠工具 cell 重复执行**未缓存**的 `BuildPreview`（全量 ANSI 解析 + 截断） | 已实施：基准 485ms → 1.61ms / 趟（见 §7） |
| P0-2 ✅ | `updateSessionTx → loadCanonicalMessagesTx` | 4.16s / 14.0% | checkpoint 落入 default 分支，**全量读取 6472 条 canonical + JSON 解码 + 重建投影** | 已实施：基准 307.9ms → 20.7ms / 次（见 §8） |
| P1-1 | Windows `cgocall` / IO | 10.40s flat / 35.1% | go test 子进程输出管道、`WaitForSingleObject`、sqlite-wasm 等（含阻塞等待） | 非纯 CPU；随 P0-2 与子进程结束自然回落 |
| P1-2 | GC | 2.68s / 9.0% | 布局/解码分配量大 + 814 MB working set | 随 P0/P1-1 分配下降；必要时再评估 GOGC 与保留上限 |

不改变的功能语义（非目标）：transcript 折叠仍是显示层投影、`Ctrl+T` 提示仍只挂最近一次折叠；checkpoint 的最终一致性仍由 turn 结束的 post-turn sync 兜底。

## 2. 采集与基线

### 2.1 目标进程与接口

| 项 | 值 |
|---|---|
| `/web/api/health` | `pid=4168`，`node_id=node-4168-20260929T003248Z`，`uptime=35228s`，`busy=true`，`mesh_ready=true` |
| 会话 | `session_20260927073805_QbWBceF5`，历史消息 **6472** 条，model items **9431** 个 |
| `/debug/pprof/` 索引、`{cmdline,symbol,allocs,block,goroutine,mutex,threadcreate}` | 全部 HTTP 200 |
| `/debug/pprof/profile` | 抓取成功：30s → 94,253 B，`Total samples = 29.65s (98.60%)` |
| 自定义 `/debug/pprof/executor` | 可用；`diagnosis=healthy`、`windowDiagnosis=idle`、`frameErrorsInWindow=0` |

### 2.2 CPU 基线

- 进程级 `Get-Counter '\Process(aicli*)\% Processor Time'`（2s 粒度）：在 **~5% 与 ~226% 之间周期性波动**——低负载帧 + 突发批处理，不是稳定满载（28 线程，多核口径）。
- 进程累计 CPU：`13675.9s`（uptime 9.78h，均值约 0.39 core）。
- `/web/api/status` 佐证：`plan-count=3152`、`plan-last-ms=6171`、`plan-max-ms=8409`（单次历史提交计划最长 8.4s）、`reducer_nanos=413.7s`、`history_effects in-flight=0 failed=0 abandoned=442`。
- 堆压力：`WorkingSet64=814 MB`；`/debug/pprof/heap?debug=1` 15 秒内输出 69 MB 仍未结束（dump 体积过大，后续分析建议用 `debug=0` protobuf）。

### 2.3 归因口径

Windows 上 Go 的 syscall 经 `runtime.cgocall` 记账，profile 中 35% 的 flat 包含 `os/exec` 读子进程输出（`internal/poll.execIO` 4.58s、`syscall.ReadFile` 4.36s）、`WaitForSingleObject` 2.66s、sqlite-wasm `Xsqlite3_step` 2.38s 等**阻塞/等待**时间。评估"用户态 CPU 燃烧"时应按 cum 调用链拆分，不能把 cgocall 全部当作优化空间。

## 3. 热点归因（证据链）

### 3.1 UI transcript 规划/布局（占采样 30.1%）

cum 链：

```text
UIController.Run                                   8.92s  30.08%
└─ reduceUIControllerState                         8.17s  27.55%
   └─ syncHistoryEffectsForTranscriptWithin        7.53s  25.40%
      └─ planEligibleHistoryCommitsWithin          7.09s  23.91%
         └─ layoutTranscriptScreenRowsWithin       6.16s  20.78%
            └─ toolFoldTargetRows                  5.88s  19.83%
               └─ toolFoldOmits                    5.27s  17.77%
                  └─ cell.BuildPreview             5.27s  17.77%
                     ├─ render.ANSIToLines         2.51s   8.47%
                     ├─ render.Truncate            1.61s  （BuildPreview 内）
                     └─ render.PlainBackend.Render 0.57s
```

代码落点与根因：

- `layoutTranscriptScreenRowsWithin`（`backend/cmd/aicli/ui/app_screen_layout.go:207`）在**每次 pass 开头**调用 `toolFoldTargetRows`（:216）。
- `toolFoldTargetRows`（`backend/cmd/aicli/ui/tool_fold.go:71`）遍历 layout rows，对每个候选 cell 调 `toolFoldOmits`（`tool_fold.go:38`）→ `cell.BuildPreview`（`backend/cmd/aicli/ui/cell/preview.go:93`）：完整执行 `ANSIToLines` → 逐行 `PlainBackend.Render` → `LineWidth` → `Truncate`，**全程无缓存**。
- 同一份 source 的渲染行其实已按内容缓存：`foldedToolChainScreenRows`（`app_screen_layout.go:467`）的结果落在 `sharedCellRows` LRU（`backend/cmd/aicli/ui/transcript_layout_cache.go:190`，8192 条 / 64 MiB）。缺缓存的是"是否折叠（omission 判定）"这一半——恰恰是每次 pass 都重跑的部分。
- 放大因素：长会话（9431 items）下 `syncHistoryEffectsForTranscriptWithin` 在 memo miss 时做完整规划（预算 6–8s），每个新 finalize 的 cell 都会触发一次全量布局扫描。

补充：`toolFoldTarget`（`tool_fold.go:57`，pager/首屏另一入口）有同样问题。

### 3.2 会话历史 checkpoint 持久化（占采样 14.2%）

cum 链：

```text
notifyHistoryCheckpoint → checkpointSessionHistory        4.21s  14.20%
└─ persistSession                                         4.21s
   └─ SQLiteSessionStorage.Update                          4.16s  14.03%
      └─ updateSessionTx                                   4.14s  13.96%
         └─ loadCanonicalMessagesTx                        4.00s  13.49%
            ├─ encoding/json.Unmarshal                     2.06s
            ├─ database/sql.(*Rows).Next                   1.51s
            └─ readCanonicalPayload                        0.32s
```

代码落点与根因：

- `checkpointSessionHistory`（`backend/internal/chat/actor.go:3433`）按 `DefaultSessionCheckpointInterval=15s` 节流（`history_checkpoint.go:16`）；长 turn 内会多次触发。
- `updateSessionTx`（`backend/internal/chat/sqlite_storage.go:786`）在 `appendOnly` 与 metadata-only 两个廉价分支不成立时，落入 default 分支（:837-862）：`loadCanonicalMessagesTx`（:1543）**全量读取 session_messages 并逐条 `json.Unmarshal`**，随后 `buildHotProjection` 重建 hot projection、`replacePromptMessagesTx` 全量重写投影。
- hot projection 的实际需求只有 **128 条 / 2 MiB**（`session_storage_factory.go:50-51`），但重建输入是全量 6472 条；解码成本随历史线性增长——正是本会话 4s/pass 的来源。
- `loadMatchingPromptRowsTx`（:1656）的 `prefixMatches` 判定是"存储投影 vs `history[0..]`"的逐条 payload 字节比较；一旦存储投影是**窗口**而非前缀（或消息元数据在 persist 前被 `stripMessagesWithMetadataKeys` / `PruneRequestScopedHistory` 改写），`prefixMatches=false`，即落入全量路径。

### 3.3 其它证据

- `encoding/json.checkValid` 0.96s、`json.unquoteBytes` 0.62s：解码路径的分配/校验成本，与 3.2 同源。
- `runtime.mallocgc` 2.57s、`gcBgMarkWorker` 2.68s：与布局/解码的临时分配同源。
- 事件日志：`journal_drops=70608`、`delivery_journal_evictions=17396`、渲染 `duplicate_count=21210`、`out_of_order_count=6967`——裁剪机制在工作，但转录规模是长期压力源。

## 4. 优化方案

### P0-1 折叠 omission 判定记忆化（UI 布局）

**目标**：`toolFoldOmits` / `toolFoldTargetRows` / `toolFoldTarget` 不再对同一 source 重复执行 `BuildPreview`。

**实现落点**：`backend/cmd/aicli/ui/tool_fold.go`（新增 memo，可放 `transcript_layout_cache.go` 同目录新文件）。

要点：

1. 新增内容寻址 LRU（与 `sharedCellRows` 同风格的 `cellLayoutLRU` 复用）：
   - key：`sha256(source)` + `len(source)` + `omissionCacheVersion`（`toolFoldOptions("")` 是固定投影参数，公式变更时递增版本号即可整体失效）；
   - value：`{omitted bool}`（按需可扩为 `{TotalLines, OmittedLines, ByteTruncated}` 供诊断）；
   - 容量与 `sharedCellRows` 同量级（条目上限按 cell 工作集取 8192；字节预算不需要——value 是常数大小）。
2. `toolFoldOmits` 先查 memo，miss 才调 `BuildPreview`，随后写回。
3. `toolFoldTargetRows` 增加单次调用内的 `seen map[scene.CellID]struct{}` 去重：当前实现只靠 `candidate.ID == target` 跳过，同一个非折叠 cell 的多行会重复判定；`toolFoldTarget` 同理按 cell ID 去重。
4. （可选，收益合并）让 `foldedToolChainScreenRows` 在 miss 时顺带把 omission 结果写入 memo，使首轮布局就为后续 pass 预热。

**预期**：长会话每轮 pass 的 5.27s `BuildPreview` 退化为 memo 查询；稳态（memo 全热）下该链接近采样噪声。

**风险**：memo 键必须覆盖所有影响判定的输入——omission 只依赖 `source` 与固定 options，收敛清晰；`cellUsesFoldedToolPresentation` 判定留在 memo 之外（先短路，避免污染）。

**回归**：`backend/cmd/aicli/ui/tool_fold_test.go` 现有 4 用例（4 行预算、最近折叠提示、pager 展开、展开意图）；新增 memo 命中/失效单测（source 变化必须重算）与并发访问测试。

### P0-2 omission 轻量预判（P0-1 的补充，可选）

`toolFoldOmits` 只需要 `OmittedLines > 0 || ByteTruncated`，不需要完整 `render.Line`。可为新 cell 实现零分配快速扫描：

- 先按 `MaxBytes=8KiB` 做长度截断判定（超限 → 直接 `ByteTruncated=true`，仍需确认是否 `OmittedLines>0`）；
- 再单遍扫描统计行数（含"丢弃尾部空行"语义），与 `HeadLines+TailLines` 比较；
- 只有边界情况（尾部空行、单行超宽触发 marker 逻辑）才回调 `BuildPreview` 精确判定。

**约束**：`tool_fold.go:32-37` 的注释明确要求判定与 `BuildPreview` 本身一致，因此必须做**差分测试**：对随机 + 边界输入（空串、仅换行、尾随空行、CJK、超宽单行、>8KiB）断言快速扫描与 `BuildPreview` 结果完全一致，否则宁可只做 P0-1。

### P0-3 checkpoint 快速路径与有界投影重建（会话持久化）

**目标**：把 checkpoint 的 default 分支从"全量 canonical 解码"改为"O(1) 边界证明 + 有界窗口重建"。

#### 步骤 0：先加分支归因观测（先测量再改）

在 `updateSessionTx` 记录并暴露计数器：命中分支（appendOnly / prefix-match / default）、`prefixMatches=false` 的判定位置（第几条开始 payload 不等、投影行数 vs history 长度）、单次 `loadCanonicalMessagesTx` 耗时与解码条数。没有这组数据，无法确认 default 分支的真实触发比例与主因（窗口 vs 元数据改写）。

#### 步骤 A：增加 `identity_hash` 列，支撑 O(1) 边界证明

现有 `sha256` 列是 payload 字节摘要，而 `incomingHistoryReachesNewest` 的语义是 **role+content 身份相等**（`messageIdentityEqual`，`sqlite_storage.go:1610`），两者不等价。方案：

- `ALTER TABLE session_messages ADD COLUMN identity_hash BLOB`（仓库已有 `ensureSQLiteColumn` 迁移模式，:524）；
- `identity_hash = sha256(len(role)||role||content)`，写入路径统一填充；历史行惰性回填（读路径发现 NULL 时按旧全量路径处理并回写）；
- 新增索引 `(session_id, identity_hash)`。

#### 步骤 B：append-only 快速路径

当 `prefixMatches=false`（投影是窗口、或元数据改写导致字节比较失败）时，先做 O(1) 证明而不是全量加载：

```sql
-- 最新 canonical 行
SELECT seq, identity_hash FROM session_messages
WHERE session_id = ? ORDER BY seq DESC LIMIT 1;
-- 入参最新消息是否存在于 canonical 及其位置
SELECT seq FROM session_messages
WHERE session_id = ? AND identity_hash = ? ORDER BY seq DESC LIMIT 1;
```

判定（与 `incomingHistoryReachesNewest` 保持同语义）：

1. `hash(session.History[len-1]) == 最新行 hash` → incoming 已到最新：直接走 append 增量（`insertCanonicalEncodedTx` + `appendPromptMessageTx`），**不 loadCanonicalMessagesTx**；
2. 入参最新消息在 canonical 中存在但非最新（seq != count）→ 视为 stale window，走有界重建（步骤 C）；
3. 入参最新消息不存在于 canonical → 新消息（如 compaction summary），走有界重建并沿用现有"honor caller replacement"逻辑。

rewind / `HistoryTruncated` / 删除分支保持现状，不进入快速路径。

#### 步骤 C：有界窗口重建 hot projection

`buildHotProjection` 需要四类消息：最新一条、system/developer、最近一条 compaction 锚点、尾部窗口（128 条 / 2 MiB）。全部可用索引查询替代全量扫描：

- 尾部：`ORDER BY seq DESC LIMIT HotHistoryMessages`（主键 `(session_id, seq)` 直接支持）；
- system/developer：`WHERE role IN ('system','developer')`（`role` 列已存在；只解码命中行，通常个位数）；
- compaction 锚点：优先读 `sessions.metadata` 中维护的"最近 compaction seq"（写入 compaction 检查点行时顺手更新）；缺失时退化为一次性小范围扫描（只解码 metadata 含 `context_stage=compaction` 的候选行）；
- 兼容哨兵：若锚点缺失且候选扫描不可行，回退现有全量路径（渐进式优化，不破坏正确性）。

#### 步骤 D：增量写投影与节流调优

- default 分支目前 `replacePromptMessagesTx` 全量重写投影；快速路径命中时改为 `appendPromptMessageTx` 增量追加（函数已存在，:1721）。
- 可选节流：`checkpointInterval=15s` 之外，增加"距上次落库的增量 < 16 条且 < 256 KiB 则跳过"的二次门槛（turn 结束 post-turn sync 仍是最终保证）；该参数建议做成配置而非硬编码。

**预期**：`loadCanonicalMessagesTx` 链从 4s/30s 降至 O(1) 边界查询 + 有界解码（百条量级），checkpoint 期间 UI/actor 锁竞争显著缓解。

**风险与回归**：

- 快速路径必须与 `incomingHistoryReachesNewest` + `prefixMatches` 语义**等价**，尤其重复消息（`collapseDuplicateMessages`）与 rewrite 场景；建议用差分测试：构造随机历史序列，对比"快速路径分支结果"与"强制旧路径结果"的投影/计数是否逐一相同；
- 现有用例：`sqlite_storage_canonical_append_test.go`、`sqlite_storage_projection_rebuild_test.go`、`sqlite_storage_backtrack_truncate_test.go`、`actor_checkpoint_test.go`、`history_checkpoint_test.go` 必须全绿；
- 保留开关（环境变量或配置）以便线上快速回退到全量路径。

### P1-1 阻塞 IO / 子进程管道（随场景自然回落）

profile 中 `os/exec` 输出转发（`io.Copy` 3.42s）与 `WaitForSingleObject` 2.66s 属于 `go test` 子进程的真实工作。可评估的小优化：capture 层已有 head/tail 窗口（`internal/executor/output_capture.go`），确认超量输出场景不再向 UI 逐行转发，避免解析与事件放大（本会话 `event_journal_drops` 已达 70608）。

### P1-2 GC 与堆

- 分配侧：P0-1/P0-3 直接减少大头（`mallocgc` 2.57s、`gcBgMarkWorker` 2.68s）。
- 保留侧：确认 transcript / 事件日志 / render encoder 的保留上限策略；本会话 814 MB working set 偏高，建议后续单独抓 `heap`（`debug=0`）定位常驻对象，而不是本次 CPU 话题内盲调 GOGC。
- 分析提示：`/debug/pprof/heap?debug=1` 输出 >69 MB 且 15s 未结束，脚本/文档应默认用 protobuf + `go tool pprof -sample_index=inuse_space`。

## 5. 验证与验收

### 5.1 复测方法（同口径对比）

```powershell
# 1) 确认目标仍 busy（活跃 turn 的负载才有代表性）
curl.exe -s http://127.0.0.1:52977/web/api/health
# 2) 抓 30s CPU profile
curl.exe -sS -m 90 -o "$env:TEMP\aicli_cpu_after.pprof" `
  "http://127.0.0.1:52977/debug/pprof/profile?seconds=30"
# 3) 对比热点（先 -top，再 -top -cum；关注 §3 两条链的占比）
go tool pprof -top       E:\projects\ai-agent-runtime\backend\aicli.exe "$env:TEMP\aicli_cpu_after.pprof"
go tool pprof -top -cum E:\projects\ai-agent-runtime\backend\aicli.exe "$env:TEMP\aicli_cpu_after.pprof"
# 4) 状态面佐证
curl.exe -s http://127.0.0.1:52977/web/api/status   # plan-last-ms / plan-max-ms / reducer_nanos
```

### 5.2 基准与单测

> 以下命令在 `backend/`（Go module 根）目录执行。

```powershell
# UI 布局热路径基准（恢复会话 / 尾部窗口 vs 全量）
go test ./cmd/aicli/ui/ -run '^$' -bench 'BenchmarkLayoutAppScreenResumedSession|BenchmarkLayoutTranscriptTailVsFull|BenchmarkTranscriptLayoutRowsResumedSession' -benchmem
# P0-1 折叠 omission 缓存基准（对照：未缓存 / memo 命中 / 长会话整趟扫描）
go test ./cmd/aicli/ui/ -run '^$' -bench 'BenchmarkToolFold' -benchtime=3x -benchmem
go test ./cmd/aicli/ui/scene/ -run '^$' -bench BenchmarkLayoutTranscriptResumeScale -benchmem
# 会话持久化基准
go test ./internal/chat/ -run '^$' -bench BenchmarkSQLiteSessionStorageAppendBounded -benchmem
# 相关回归
go test ./cmd/aicli/ui/... ./internal/chat/...
```

建议为 P0-1/P0-3 各补一个可复现基准：

- `BenchmarkToolFoldTargetRowsResumedSession`：N 个折叠 cell 的 rows，b.N 次调用；断言 P0-1 后分配/耗时相对当前基线下降 ≥10×。
- `BenchmarkSessionUpdateAppendFastPath`：6472 条历史 + 末尾追加 1 条，对比快速路径与旧全量路径；断言快速路径不调用全量解码且投影结果一致。

### 5.3 验收指标（以复测为准）

| 指标 | 当前 | 目标 |
|---|---|---|
| `toolFoldTargetRows → BuildPreview` cum 占比 | 19.8% | < 3%（memo 热后） |
| `loadCanonicalMessagesTx` cum 占比 | 13.5% | < 3% |
| `plan-max-ms`（`/web/api/status`） | 8409ms | 明显下降（布局与规划不再被 omission 重算拖累） |
| checkpoint 单次落库耗时 | 4s 量级（30s 窗口内 4.16s） | ≤ 200ms 量级（有界查询 + 有界解码） |
| working set | 814 MB | 不随历史线性增长（后续 heap 专项确认） |

### 5.4 实施顺序建议

1. P0-1（低风险、收益直接、可独立合入）→ 复测 profile；
2. P0-3 步骤 0（加观测）→ 收集 default 分支真实占比与触发原因；
3. P0-3 A/B（identity_hash + append 快速路径）→ 差分测试 + 回归；
4. P0-3 C/D（有界重建 + 增量投影 + 节流）→ 复测；
5. P0-2（差分测试通过再上）；P1 视复测结果决定是否继续。

## 6. 附录

### 6.1 关键数值速查

- 采样：`Duration=30.07s`，`Total samples=29.65s (98.60%)`；flat 前三：`runtime.cgocall 10.40s (35.08%)`、`memclrNoHeapPointers 0.92s (3.10%)`、`semawakeup 0.78s (2.63%)`。
- UI 链：`reduceUIControllerState 8.17s`、`syncHistoryEffectsForTranscriptWithin 7.53s`、`planEligibleHistoryCommitsWithin 7.09s`、`layoutTranscriptScreenRowsWithin 6.16s`、`toolFoldTargetRows 5.88s`、`toolFoldOmits 5.27s`、`cell.BuildPreview 5.27s`、`render.ANSIToLines 2.51s`、`render.Truncate 1.61s`。
- 持久化链：`checkpointSessionHistory 4.21s`、`SQLiteSessionStorage.Update 4.16s`、`updateSessionTx 4.14s`、`loadCanonicalMessagesTx 4.00s`（`json.Unmarshal 2.06s`、`Rows.Next 1.51s`）。
- IO/GC：`internal/poll.execIO 4.58s`、`syscall.ReadFile 4.36s`、`WaitForSingleObject 2.66s`、`io.Copy 3.42s`、`runtime.mallocgc 2.57s`、`gcBgMarkWorker 2.68s`。
- 进程：`CPU=13675.9s`、`WorkingSet=814 MB`、28 线程；`Get-Counter` 峰值 226%。
- 会话：历史 6472 条、model items 9431、`plan-count=3152`、`plan-max-ms=8409`、`reducer_nanos=413.7s`、`journal_drops=70608`。

### 6.2 代码位置索引

| 关注点 | 位置 |
|---|---|
| omission 判定 / fold target | `backend/cmd/aicli/ui/tool_fold.go:38,57,71` |
| omission 判定缓存（P0-1 新增） | `backend/cmd/aicli/ui/tool_fold_omission_cache.go`；测试/基准 `tool_fold_omission_cache_test.go`、`tool_fold_omission_bench_test.go` |
| 通用 LRU（P0-1 key 泛化） | `backend/cmd/aicli/ui/transcript_layout_cache.go:41`（`cellLayoutLRU[K comparable, V any]`） |
| preview 构建（未缓存热点） | `backend/cmd/aicli/ui/cell/preview.go:93` |
| 布局入口（每次 pass 调用 fold target） | `backend/cmd/aicli/ui/app_screen_layout.go:207,216,467` |
| 折叠 cell 行缓存 | `backend/cmd/aicli/ui/transcript_layout_cache.go:190,195` |
| 规划预算 / memo | `backend/cmd/aicli/ui/history_effect_planner.go:45,741,808` |
| checkpoint 节流 | `backend/internal/chat/history_checkpoint.go:16,78` |
| checkpoint / persist | `backend/internal/chat/actor.go:3299,3433` |
| update 分支 / 全量解码 | `backend/internal/chat/sqlite_storage.go:786,837,1543,1656` |
| hot projection 预算 | `backend/internal/chat/session_storage_factory.go:50-51` |
| 基准参考 | `backend/cmd/aicli/ui/history_hot_path_bench_test.go:265,287,323`；`backend/internal/chat/sqlite_storage_benchmark_test.go:12` |

## 7. 实施记录（P0-1：折叠 omission 判定记忆化）

### 7.1 变更内容

| 文件 | 变更 |
|---|---|
| `backend/cmd/aicli/ui/tool_fold.go` | `toolFoldOmits` 改为查 memo（`sharedFoldOmissions.omits`），判定本体提取为 `toolFoldOmitsUncached`；`toolFoldTargetRows` 增加逐 cell `checked` 去重（同一 cell 多行只判定一次，mutable 跳过语义不变） |
| `backend/cmd/aicli/ui/tool_fold_omission_cache.go`（新增） | 内容寻址 LRU：key = `source + 版本号`（`foldOmissionMemoVersion`），容量 8192 条 / 8 MiB；提供 `omits` / `warm` / `stats` / `reset` |
| `backend/cmd/aicli/ui/app_screen_layout.go` | `foldedToolChainScreenRows` 在 `BuildPreview` 后回填 omission 结论，新 cell 冷启动不重复计算 |
| `backend/cmd/aicli/ui/transcript_layout_cache.go` | `cellLayoutLRU` 键类型泛化为 `K comparable`，布局行缓存与 omission 缓存复用同一 LRU 实现；`TranscriptLayoutCacheStats` 增加 `FoldOmit*` 计数 |
| `backend/cmd/aicli/commands/chat_debug_display_http.go` | `/debug/chat/status` 的 `app_state.layout_cache` 增加 `fold_omit_*`（含命中率），可直接观测缓存效果 |
| `backend/cmd/aicli/ui/tool_fold_omission_cache_test.go`（新增） | 3 个测试：按 source 记忆化、与未缓存判定差分（8 组边界输入）、逐 cell 去重 |
| `backend/cmd/aicli/ui/tool_fold_omission_bench_test.go`（新增） | 4 个基准（含复刻旧实现的 Legacy 对照） |

**为什么安全**：判定只依赖 `cell.Source` 与固定的 `toolFoldOptions("")` 显示预算，内容寻址天然免失效；`toolFoldOmitsUncached` 仍是判定语义的唯一出处，缓存只是旁路；LRU 有 8 MiB 字节上限，key 持有的 source 字符串与 transcript cell 共享底层字节，不复制正文。

### 7.2 基准对比（Windows / Xeon E5-2686 v4 / go1.26，`-benchtime=3x -benchmem`）

| 基准 | ns/op | B/op | allocs/op |
|---|---:|---:|---:|
| `BenchmarkToolFoldOmitsUncached`（优化前单次判定） | 133,500 | 29,840 | 382 |
| `BenchmarkToolFoldOmitsMemoHit`（memo 命中） | 933 | 0 | 0 |
| `BenchmarkToolFoldTargetRowsResumedSessionLegacy`（1024 cell × 3 行，旧实现） | 485,211,733 | 30.5 MB | 391,215 |
| `BenchmarkToolFoldTargetRowsResumedSession`（同场景，memo + 去重） | 1,612,000 | 128 KB | 21 |

- 单次判定 ≈ **143×**；长会话整趟 fold-target 扫描 ≈ **301×**（耗时）、**237×**（分配字节）、**18,600×**（分配次数）。
- 旧实现 485ms/趟与线上采样 5.88s/30s（约 12 趟）吻合，证明基准复刻了 profile 中的热点形态。

### 7.3 回归验证

| 命令 | 结果 |
|---|---|
| `go test ./cmd/aicli/ui/ -run 'FoldOmission\|ToolFold' -v -count=1` | PASS（新增 3 测试；既有折叠契约测试由整包回归覆盖） |
| `go test ./cmd/aicli/ui/ -count=1 -skip 'TestReadInteractiveLine'` | ok 83.4s |
| `go test ./cmd/aicli/ui/style/` | ok（`TestProbeOSCDefaultColorsDeadlineTimeout` 在高负载下偶发 856ms>deadline，单独复跑 PASS；与本改动无交集） |
| `go test ./cmd/aicli/commands/ -run 'TestChatDebugDisplay\|TestChatDebugJSON\|...'` | ok（debug display JSON 扩展无回归） |
| `go build ./cmd/aicli/...` | OK |

> 整包 `go test ./cmd/aicli/ui/...` 直接运行会命中 `TestReadInteractiveLine_DisplaysBracketedPasteAfterIdleBeforeEndMarker` 在无 TTY 环境挂起（10 分钟超时）：环境性问题，与本次改动无关；CI/本地回归请带 `-skip 'TestReadInteractiveLine'` 或提供真实终端。

### 7.4 待办 / 下一步

1. **线上效果需重启验证**：pid 4168 仍运行旧二进制（`backend\aicli.exe` 被进程占用），P0-1 的线上收益要用新代码构建并重启的会话复抓 profile（预期 `toolFoldTargetRows → BuildPreview` 占比从 19.8% 降到 <3%；`app_state.layout_cache.fold_omit_*` 可直接看命中率）。
2. P0-3 按 §5.4 顺序推进：先做 `updateSessionTx` 分支归因观测（回答 default 分支占比与 `prefixMatches=false` 主因），再实施 `identity_hash` 快速路径与有界投影重建。

## 8. 实施记录（P0-3：checkpoint 投影重建快速路径 + 有界回填）

### 8.1 变更内容

| 文件 | 变更 |
|---|---|
| `backend/internal/chat/sqlite_storage_identity.go`（新增） | `canonicalMessageIdentityHash`（role+content 长度前缀 sha256，语义与 `messageIdentityEqual` 一致）；`incomingHistoryReachesNewestFastTx`（O(1) 点查证明）；`canonicalIdentityHashesCompleteTx`；`backfillCanonicalIdentityHashesTx`（轻量回填，best-effort）；`CanonicalRebuildStats` 归因访问器 |
| `backend/internal/chat/sqlite_storage.go` | schema v2：`session_messages.identity_hash BLOB` + `(session_id, identity_hash)` 索引（迁移幂等）；写入路径统一填指纹；snapshot schema/复制列同步；`updateSessionTx` default 分支先走快速证明，未证明才全量解码并在同一事务内回填 |
| `backend/internal/chat/sqlite_storage_identity_fastpath_test.go`（新增） | 差分一致性、回填自愈、stale 回退、指纹语义、证明分支 5 个测试 |
| `backend/internal/chat/sqlite_storage_benchmark_test.go` | 新增 A/B 基准（4096 条 canonical、同形 default 分支负载），并在基准内断言目标分支确实命中 |

**等价性论证**（快速路径只在可证明时生效）：

1. 入参最新消息与 canonical 最新行 role+content 一致 → 旧逻辑 `incomingHistoryReachesNewest` 在倒序扫描的**第一项**（最后一行）即命中并返回 true；
2. 入参最新消息在 canonical 中完全不存在（指纹完整性已确认）→ 旧逻辑扫描全表后返回 true（honor caller replacement，compaction summary 等）；
3. canonical 为空 → 旧代码把 `canonical` 落回 `session.History`，同样等价于 true；
4. 其余情况（存在于更早位置 = stale；最新行无指纹 = 未回填）→ 返回 false，**原封不动**执行既有全量路径，且失败时行为与旧代码逐行一致（包括 `loadErr` 时 source 回落 history）。

### 8.2 基准对比（4096 条 canonical，同形 default 分支负载，`-benchtime=10x -benchmem`）

| 基准 | ns/op | B/op | allocs/op |
|---|---:|---:|---:|
| `BenchmarkUpdateSessionProjectionRebuildLegacy`（P0-3 前：全量解码） | 307,850,720 | 9,062,349 | 154,528 |
| `BenchmarkUpdateSessionProjectionRebuildFastPath`（identity_hash 点查） | 20,693,570 | 456,886 | 7,154 |

- 单次 checkpoint 投影重建 ≈ **14.9× 提速**、内存 **19.8× 下降**、分配次数 **21.6× 下降**；全量成本随历史线性增长（生产 6472 条时旧路径实测 ~4s/次），快速路径与之无关。
- 基准内置归因断言：legacy 必须 `FullCanonicalLoads >= b.N` 且零快速证明；fast 必须零全量解码，防止基准退化成无效对比。

### 8.3 迁移与兼容

- schema v1 → v2：打开旧库时执行一次 `ALTER TABLE ... ADD COLUMN identity_hash` + 建索引；`PRAGMA user_version` 随之升到 2。
- 旧行指纹惰性回填：升级后该会话第一次落入全量回退时，在同一事务内批量补齐（轻量 role/content 解析 + prepared UPDATE）；之后所有 checkpoint 走点查快速路径。回填失败只记日志，绝不改变持久化语义（该行保持 NULL → 下次继续走全量）。
- 新旧二进制并存：旧二进制不写新列（NULL）且跳过 v2 迁移，新二进制对 NULL 行保守回退，不会读错。

### 8.4 回归验证

| 命令 | 结果 |
|---|---|
| `go test ./internal/chat/ -run 'Identity\|ProjectionRebuild\|CanonicalAppend\|StaleShorter\|SchemaVersion' -count=1` | PASS |
| `go test ./internal/chat/ -run 'SchemaMigrationAddsIdentityHashToLegacyDatabase' -count=1` | PASS（v1→v2：补列 + 重建索引 + 历史行可读，指纹留待惰性回填） |
| `go test ./internal/chat/ -run 'Snapshot\|...' -count=1`（含 `TestSnapshotSessionSchemaParity`） | PASS（修复 snapshot schema 同步后） |
| `go test ./internal/chat/ -count=1`（整包） | ok 62.3s |
| `go test ./cmd/aicli/commands/ -run 'TestExitThenResumeKeepsLastTurn\|TestSendMessagePersistsUserPromptAtInputTime\|TestHistoryLoadAndResume...\|TestApplySelectedBacktrack...\|TestUnifiedStartupReplaysEventLog...' -count=1` | ok 4.3s（消费方 resume/backtrack 回归） |
| `go test ./internal/chatcore/ -count=1` | ok |
| `go build ./...`（整模块） | OK |

> 首次整包回归暴露 `TestSnapshotSessionSchemaParity` 失败：snapshot 表与主表的列清单必须严格一致。已在 `sqliteSessionSnapshotSchema` 与 `copySQLiteSessionSnapshot` 同步 `identity_hash`，该用例是本次改动的重要守门测试。

### 8.5 待办

1. 线上复测需重启进程（当前 pid 仍运行旧二进制）；重启后用 `/debug/chat/status` 的 storage/plan 区块与 pprof 复测 `updateSessionTx` 占比，预期从 14% 降到 <3%。
2. `CanonicalRebuildStats()` 已导出但尚未接入 `/web/api/status`（存储区块当前挂在 Runtime Store 上，接线会话存储需要额外宿主引用）；接入后可在线看 `full_canonical_loads / fast_identity_proofs / backfilled_rows`。
3. 投影重建的「有界窗口」优化（原 P0-3 步骤 C）暂缓：快速路径已覆盖正常增长会话的绝大多数 checkpoint，stale 回退属于罕见路径，先观察线上占比再决定是否继续下沉。

### 8.6 线上验证（2026-09-29 20:06 重启后，会话 `session_20260927073805_QbWBceF5`）

**版本与迁移证据**

- 运行进程 pid 15536（20:06:09 启动），二进制 `backend/aicli.exe`（20:06:03 写入）；`go tool nm` 确认包含 `incomingHistoryReachesNewestFastTx` / `backfillCanonicalIdentityHashesTx` / `canonicalMessageIdentityHash`。
- 生产库 `C:\Users\wwsheng\.aicli\sessions\session_history.sqlite`：`PRAGMA user_version=2`、`identity_hash` 列与 `idx_session_messages_identity` 索引在线创建成功。
- **惰性回填按会话生效**：目标会话 6960 行指纹覆盖 100%（`null=0`）；其余 14 个会话合计 14713 行仍为 NULL（库内 21651 行）。启动未做全库重写。
- 一次性迁移成本可观测：`append` 统计 `max_ns=2.11s`（首次全量回退 + 6938 行回填，同事务）；此后 `busy_retries=0`，写等待 4 次共 1.08s。

**CPU 对照（同一会话、修复前 vs 修复后）**

| 函数 | 修复前 18:20（cum） | 重启后 20:11（cum，busy） | 20:16/20:19（稳态/间歇） |
|---|---:|---:|---:|
| `updateSessionTx` | 4.16s / 14.0% | 0.14s / 1.25% | 0 采样 |
| `loadCanonicalMessagesTx` | 其中 2.06s `json.Unmarshal` + 1.51s `Rows.Next` | **0 采样** | 0 采样 |
| `toolFoldTargetRows → toolFoldOmits → BuildPreview` | 5.88s / 19.8% | 0.08s / 0.72%（`foldOmissionCache.omits`） | 0 采样 |
| `syncHistoryEffectsForTranscriptWithin` | 未进前列 | 2.39s / 21.4% | 0 采样 |

> 注：两次 busy 窗口的总采样与负载不同（18:20 为长会话持续渲染期；20:11 为重启后恢复+首个 turn），百分比只作方向性对照；决定性证据是 `loadCanonicalMessagesTx` 与 `BuildPreview` 在新窗口 0 采样，且 `updateSessionTx` 子树只剩 `canonicalAppendStartTx`(0.06s)/`loadMatchingPromptRowsTx`(0.04s)/`replacePromptMessagesTx`(0.03s)/`buildHotProjection`(0.01s)。

**新观察项（候选 P0-4：history-effects 同步）**

20:11 窗口内 `syncHistoryEffectsForTranscriptWithin` 2.39s/21.4%，构成：

- `planEligibleHistoryCommitsWithin` 1.52s（63.6%）：`planMarkdownCellHistoryCommits` 0.41s、`planPlainCellHistoryCommits` 0.34s（内含 `sourceLineRanges` 0.16s）、`layoutTranscriptScreenRowsWithin` 0.32s、`growslice` 0.20s；
- `syncHistoryEffectCandidates` 0.57s + `syncHistoryEffectCandidatesPrefix` 0.29s（`enqueueHistoryCandidates` 占 0.47s）；
- 调用来源：`continueTruncatedHistoryPlan` 1.03s（43%，长会话恢复续算）+ `syncHistoryEffectsForTranscript` 1.36s。

20:16 与 20:19 窗口该函数族 0 采样，且 20:19 的 CPU 主要在 `chatBusyComposerCapture.ReadLine`（32.5%）与后台任务日志读取（`os.ReadFile` 19.3%）。20:22（当前 turn 中段、非恢复期）复采：`syncHistoryEffectsForTranscriptWithin` 与 `updateSessionTx` 均 0 采样（`go tool pprof -peek` no matches），CPU 为 cgocall/输入等待。结论：该热点与「长会话恢复/首轮 history replay」强相关，**现判定为恢复期瞬态**；若后续在非恢复期长 turn 中复现，再按 P0-4 方向处理（`planEligibleHistoryCommitsWithin` 按 transcript epoch 记忆化 / 按 frontier 增量计算）。

**留档**：`logs/pprof/aicli_cpu_15536_20260929_2011.pprof`、`_2016.pprof`、`_2019.pprof`、`_2022.pprof`（`logs/` 已 gitignore）。

#### 追加观察（21:12–21:15，同一进程 uptime ~66min）

- **21:13 profile 存储/UI 路径全部 0 采样**：`updateSessionTx`、`loadCanonicalMessagesTx`、`backfillCanonicalIdentityHashesTx`、`syncHistoryEffectsForTranscriptWithin`、`reduceUIControllerState` 均 no matches。CPU 80.6% 为 cgocall，其中 49.2% 是 `mcp.(*pipeRWC).Read → json.Decoder.Decode`（chrome-devtools MCP，30 tools，healthCheck 正常）——真实工具流量解码，非重复计算。
- **append 统计**：`appends=19968`、`max_ns=5.51s`、`lock_hold_ns=207.8s`（均值 ~10.4ms）、`write_wait=10 次/2.16s`、`busy_retries=0`。5.51s 为单次离群（首次回填 2.1s 之后的新最大值）；同窗口全量回退路径 0 采样，指向单次大 payload 事件落盘而非重复全量解码，**继续观察是否复现**。
- **会话指纹覆盖**：目标会话 7120 行 100% 覆盖；全库仅此 1 个会话有指纹（49 个会话、全局 null=13716）。库总行数 21651→20836 的下降来自会话被删除/清理（含原 985 行的 `session_20260508170857_55Z1QXkY` 已不在），**不是**回填行为。
- **当前 turn**：`turn_3284da94…` 已运行 ~1h，屏显为等待后台任务（`task_output job_id=job_ref_b6f6eee6dfee timeout_ms=900000 wait=exit`），busy 属正常等待；`/debug/pprof/executor` 持续 `reconciliationRequired=true` + revision 递增，是实时重绘调度而非异常。
