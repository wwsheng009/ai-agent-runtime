# L2.2 语义布局缓存/增量 实测记录 — 2026-09-25

> 结论先行：**基准门禁达成**——`BenchmarkLayoutTranscriptResumeScale`（6,719 cells / 161,256 行）
> 热路径 **52.2 ms → 8.5 ms（−84%）**、分配 **52.2 MB → 8.9 MB（−83%）**，目标 ≤20 ms 有 2.3× 余量。
> **harness P12 门禁（单次冻结 ≤250 ms）本轮仍未取数**（本机空闲内存 1,551 MB / 6 个 aicli 实例，
> 见 §6），因此**不得**把本记录表述为「P12 已通过」。

## 1. 基准与语料

新增 `backend/cmd/aicli/ui/scene/layout_scale_bench_test.go`（计划 §4.3 指定的验收仪器）：

- 语料：6,719 cells × 24 行 = **161,256 语义行**（生产恢复会话实测 6,719 cells / 146,535 行，同量级）；
  每行 ~85 B 的中文正文，source 以 `\n` 结尾，全部 `CellCommitted`。
- 三个子基准，对应三种真实缓存状态：
  | 子基准 | 语义 |
  |---|---|
  | `cold` | 每轮清空共享缓存 → 首次恢复/缓存抖动时的上界（全量 split） |
  | `hot` | 缓存已预热、内容不变 → 稳态帧路径（每次 delta 都会走） |
  | `append` | mutable 尾 cell 每轮追加一行（revision++）→ append-only cell 的增长路径 |

运行：`go test ./cmd/aicli/ui/scene/ -run '^$' -bench BenchmarkLayoutTranscriptResumeScale -benchtime 5x -count 1`

## 2. 结果（同机同语料，AMD Ryzen 7 5800H / 16 线程）

| 子基准 | before | after | Δ 时间 | Δ 字节 | Δ allocs |
|---|---|---|---|---|---|
| `hot` | 52.2 ms / 52.2 MB / 73,423 | **8.5 ms / 8.9 MB / 33,068** | **−83.7%** | −82.9% | −55.0% |
| `append` | 48.9 ms / 52.2 MB / 73,442 | **7.5 ms / 8.9 MB / 33,077** | **−84.7%** | −82.9% | −55.0% |
| `cold` | 49.7 ms / 53.2 MB / 73,489 | 43.4 ms / 13.5 MB / 39,854 | −12.7% | −74.6% | −45.8% |

（after 为最终树 `-benchtime 5x`；`-benchtime 3x` 独立复跑得 44.6 / 9.2 / 7.8 ms，与上表一致 ⇒ 数字稳定。）

**before 暴露的核心事实：`hot ≈ cold`（52.2 ms vs 49.7 ms）** —— 共享 split 缓存在恢复会话量级下
**几乎零收益**，即计划 §4.3 判断的「容量不足 → 必然抖动」成立，且实测抖动幅度是 100%。

## 3. 归因（memprofile `-sample_index=alloc_objects`，缓存重写后、split 预分配前）

cold 73,449 allocs/op 的构成：

| 站点 | 占比 | 说明 |
|---|---|---|
| `splitSourceLines` | 54.5% | 41,785 allocs/op —— append 增长：24 行 cell 要 6 次分配 |
| `fmt.Sprintf` | 11.9% | **语料构造**（一次性，不在计时循环内），非每轮成本 |
| `TranscriptCell.BoundaryMeta` | 10.7% | 8,192 allocs/op，每次 gap 决策 2 次投影 |
| `LayoutTranscript` | 7.1% | `&BoundaryKey{}`（每个 gap row 一次）与两处切片分配 |

最终树 CPU profile（cold 20x）：`splitSourceLines` flat **60.2%**、`LayoutTranscript` flat 5.5%、
`strings.Count`（countbody）3.9% ⇒ **冷路径已是「切分本身」的成本**（扫 13.7 MB + 造 161k 子串），
不是分配开销，无进一步低风险收益。

## 4. 三处改动

### 4.1 `scene/layout_cache.go`：三元组键 → 「一个 cell 一个条目」+ O(1) 惰性逐出

旧键 `(ID, Revision, source)` + 条目上限 4096 在 6,719 cells 下有两个复合缺陷：

1. 容量不足 ⇒ 每轮稳定 miss 2,600+ cell（hot ≈ cold 的直接原因）；
2. `put` 为去重做 `order` 的 **O(n) 线性扫描**（4096 条目 × 每轮 6,719 次 put）；
3. mutable cell 每个 revision 都留一份**完整 source + 行切片**（上万行 cell × 上千 revision 的 O(n²) 内存）。

新结构：`entries map[CellID]splitSourceEntry`，条目内带 `(revision, source)` 做一致性校验
（稳态下与 `cell.Source` 是同一字符串头，`==` 退化为指针比较，零字符串开销）；
逐出改为 FIFO 日志 + 单调 `stamp` 的**惰性删除**（摊还 O(1)，过期日志项直接跳过）。
容量：条目上限 4096 → **8192**（覆盖 6,719 工作集），行预算 200,000 → **400,000**
（恢复会话实测 146,535 行，留一倍余量给「单 cell 上万行」的长尾）。

**刻意的取舍**：只保留每 cell 最新一份。若调用方用**更旧 revision** 的 cell 做布局，
该 cell 会 miss 并重算（旧实现可能命中）——代价只是重切一次，**结果不变**（缓存是纯记忆化）。
换来的是内存上界从「每 revision 一份」变成「每 cell 一份」。

### 4.2 增量前缀复用（append-only cell 的 O(n²)）

条目记 `consumed`（source 中被完整行覆盖的前缀长度）。新 source 以上一份为前缀时
（COW 下 append-only 的正常形态），复用 `lines[:len-1]` 完整行，只 split `source[consumed:]`；
否则（替换/截断）回落全量切分。`append` 子基准 48.9 → 7.5 ms 即此项 + 4.1 的合计效果。

### 4.3 `LayoutTranscript` 两遍 + `splitSourceLines` 预分配

- `LayoutTranscript`：先解析各行并统计总行数，再 `make([]LayoutRow, 0, total+len(cells))`
  一次分配（旧实现 `var rows []LayoutRow` + append 增长，为 7.7 MB 的最终切片反复复制）。
- `splitSourceLines`：`strings.Count(source, "\n")+1` 精确预分配（旧实现 `[]string{}` + append
  增长，24 行 cell 要 6 次分配；cold 的 41.8k allocs/op 即此项）。
- 语义零变化：切分结果逐元素不变（见 §5 oracle），仅分配次数变化。

## 5. 正确性证据

`scene/layout_cache_test.go` 重写为新 API，**原三条行为断言全部保留**（命中 / revision 变化必 miss /
同 ID 同 revision 不同内容必 miss / FIFO 逐出最旧），并新增：

| 用例 | 钉住的性质 |
|---|---|
| `TestSplitSourceLinesCacheGrowthKeepsOneEntryPerCell` | 100 次增长后 `len(entries)==1`、`reuses==99`（内存上界 + 复用确实发生） |
| `TestSplitSourceLinesCacheIncrementalMatchesFullSplit` | **oracle**：4 组追加序列（无换行单行 / 空尾巴 / 追加不完整行 / 同内容异 revision）逐步与 `splitSourceLines` 逐元素比对；反向（截断）必须回落全量且仍正确 |
| `TestLayoutSplitSourceLinesCacheRoundTrip` | 缓存路径与直接切分一致；空 source → nil；**nil cell → nil**（旧实现会 panic） |

回归：`go test ./cmd/aicli/ui/scene/ -count=1` ✅；
**`go test -p 2 ./cmd/aicli/ui/... -count=1` → 15/15 包全绿**（含 `ui` 18.9s、`render`、`renderengine`、
`render/encoding`，即本改动的全部调用面）。

## 6. 未验证 / 未做

1. **harness P12（单次冻结 ≤250 ms、累计 ≤4 s）未取数**：测量时本机空闲 1,551 MB / 6 个 aicli 实例，
   M1 记录的换页噪声仍未消除（见 `resume-m1-ab-measurement.md`）。本记录只声明基准口径。
2. **`rows` 物化本身**：hot 的 8.9 MB/op 里 7.7 MB 是 `[]LayoutRow`（API 契约：调用方要拿整片行）。
   再降一个量级需要调用方层「按 transcript revision 记忆化」或迭代器化 API —— 未做，属后续项。
3. `BoundaryMeta()` 每次调用约 1 次分配（hot 6.5k allocs/op ≈ 0.3 MB）——属 boundary 包投影，
   未动（避免与 boundary 语义/其它在途改动交叉）。
4. `L2.5`（消掉启动后第二次全量重规划）**未做**：其验收是 harness 口径（P12 冻结窗口数），
   与 §6.1 同一阻塞。

## 7. 复跑配方

```pwsh
# 基准（冷/热/增长三态）
go test ./cmd/aicli/ui/scene/ -run '^$' -bench BenchmarkLayoutTranscriptResumeScale -benchtime 5x -count 1

# 归因（分配站点 / CPU）
go test ./cmd/aicli/ui/scene/ -run '^$' -bench 'BenchmarkLayoutTranscriptResumeScale/cold' -benchtime 20x -count 1 -memprofile "$env:TEMP\scene_cold.mem"
go tool pprof -top -sample_index=alloc_objects "$env:TEMP\scene_cold.mem"

# 回归面
go test -p 2 ./cmd/aicli/ui/... -count=1
```
