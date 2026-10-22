# 工具层 benchmark 基线（2026-10-22）

- 设计文档：`docs/plan/tool-layer-abstraction-design-20261022.md` §15.2
- 代码版本：`910c4f05`
- 运行脚本：`backend/scripts/tool-layer-bench.ps1`
  （10k + 256-tool 默认；`AICLI_TOOLCORE_BENCH_LARGE=1` 追加 100k）
- 运行环境：
  - Windows / amd64
  - Go `go1.27.1 windows/amd64`
  - CPU：AMD Ryzen 7 5800H with Radeon Graphics
  - 为降低当前机器的内存压力，benchmark 使用 `GOGC=20`、`GOMAXPROCS=2`，
    因此数字偏保守，只作为**首轮机器基线**，不是最终 CI 门槛。
- 语料：`internal/toolcore/searchcore` 的 `benchmarkTree`
  - 10k 文件，每文件 1 KiB，共约 10 MB；
  - 目录分布 `d000..d099`；
  - benchmark 前预热 OS 文件缓存。

## 1. medium-repo（10k 文件）

命令：

```powershell
$env:GOGC='20'; $env:GOMAXPROCS='2'
go test -run '^$' `
  -bench 'BenchmarkGlob10kNoMatch|BenchmarkGrep10kNoMatch' `
  -benchmem -count=3 ./internal/toolcore/searchcore
```

结果：

| benchmark | run | ns/op | MB/s | peak_heap_B | B/op | allocs/op |
|-----------|-----|-------|------|-------------|------|-----------|
| `BenchmarkGlob10kNoMatch` | 1 | 55,883,558 | — | — | 4,553,498 | 51,595 |
| `BenchmarkGlob10kNoMatch` | 2 | 55,600,903 | — | — | 4,579,335 | 51,596 |
| `BenchmarkGlob10kNoMatch` | 3 | 50,814,825 | — | — | 4,545,697 | 51,595 |
| `BenchmarkGrep10kNoMatch` | 1 | 3,290,632,200 | 3.11 | 1,280,416 | 11,105,856 | 91,748 |
| `BenchmarkGrep10kNoMatch` | 2 | 3,362,231,300 | 3.05 | 1,290,704 | 11,069,600 | 91,720 |
| `BenchmarkGrep10kNoMatch` | 3 | 2,984,682,800 | 3.43 | 1,292,080 | 11,168,040 | 91,717 |

中位数（3 次）：

| benchmark | median ns/op | median B/op |
|-----------|--------------|-------------|
| glob 10k | ~55.6 ms | ~4.55 MB |
| grep 10k | ~3.36 s | ~11.1 MB（累计）/ ~1.29 MB（峰值 heap） |

说明：

- glob 的分配量稳定在 ~4.55 MB、~51.6k allocs；
- grep 的累计分配稳定在 ~11.1 MB、~91.7k allocs，但**峰值 heap 只有
  ~1.29 MB**（20ms 采样 `/memory/classes/heap/objects:bytes`），说明遍历
  是流式有界的，不能用 B/op 判断峰值；
- 当前机器/受限并发下 grep 吞吐约 3 MB/s，属于**保守基线**，需在高性能
  CI 机器上复测后再定为回归门槛。
- 同一脚本第二次运行观察到：glob ~42.6–47.9 ms、grep ~2.28–3.16 s、
  grep 峰值 heap ~1.29 MB；宿主负载会造成约 2× 波动，CI 门槛必须在
  静默机上取 3 次中位数后固化。

## 2. large-repo（100k 文件）

命令：

```powershell
$env:GOGC='20'; $env:GOMAXPROCS='2'; $env:AICLI_TOOLCORE_BENCH_LARGE='1'
go test -run '^$' `
  -bench 'BenchmarkGlob100kNoMatch|BenchmarkGrep100kNoMatch' `
  -benchmem -benchtime=1x -count=1 ./internal/toolcore/searchcore
```

结果（`-benchtime=1x -count=1`，tree 创建在计时之外）：

| benchmark | ns/op | MB/s | peak_heap_B | B/op | allocs/op |
|-----------|-------|------|-------------|------|-----------|
| `BenchmarkGlob100kNoMatch` | 418,426,300 | — | — | 43,903,392 | 502,141 |
| `BenchmarkGrep100kNoMatch` | 39,079,639,800 | 2.62 | 1,471,776 | 113,838,480 | 903,152 |

10k → 100k 对照：

| 指标 | 10k | 100k | 比例 |
|------|-----|------|------|
| glob 耗时（中位数） | ~55.6 ms | ~418 ms | ~7.5× |
| glob B/op | ~4.55 MB | ~43.9 MB | ~9.7× |
| grep 耗时（中位数） | ~3.36 s | ~39.1 s | ~11.6× |
| grep 峰值 heap | ~1.29 MB | ~1.47 MB | ~1.14× |
| grep B/op（累计分配） | ~11.1 MB | ~113.8 MB | ~10.3× |

重要说明：

- `B/op` 是**单次操作的累计分配量**，不是进程峰值内存；10k→100k 的 B/op
  近似线性增长，但**峰值 heap 只从 ~1.29 MB 升到 ~1.47 MB（~1.14×）**，
  说明遍历是流式有界的；
- §15.2.2 的“grep 峰值内存不随文件总量线性增长”已有关闭证据（20ms 采样
  `/memory/classes/heap/objects:bytes`）；
- grep 100k 单次约 39s，若要进 CI 回归门槛，建议改用 10k 语料或抽样目录。

## 3. 工具列表（256 tools）

命令：

```powershell
$env:GOGC='20'; $env:GOMAXPROCS='2'
go test -run '^$' `
  -bench 'BenchmarkRegistryDefinitions256|BenchmarkRegistryList256' `
  -benchmem -count=3 ./internal/toolcore
```

结果：

| benchmark | run | ns/op | B/op | allocs/op |
|-----------|-----|-------|------|-----------|
| `BenchmarkRegistryDefinitions256` | 1 | 163,055 | 111,360 | 514 |
| `BenchmarkRegistryDefinitions256` | 2 | 159,733 | 111,360 | 514 |
| `BenchmarkRegistryDefinitions256` | 3 | 152,481 | 111,360 | 514 |
| `BenchmarkRegistryList256` | 1 | 57,775 | 9,728 | 2 |
| `BenchmarkRegistryList256` | 2 | 55,322 | 9,728 | 2 |
| `BenchmarkRegistryList256` | 3 | 56,793 | 9,728 | 2 |

中位数：

| benchmark | median ns/op |
|-----------|--------------|
| Definitions 256 | ~159.7 µs |
| List 256 | ~56.8 µs |

对照 §15.2.2“256 tools list p95 ≤ 50ms”：当前实现有两个数量级余量。

## 4. MCP 32 KiB（in-process vs MCP）

命令：

```powershell
$env:GOGC='20'; $env:GOMAXPROCS='2'
go test -run '^$' `
  -bench 'BenchmarkInProcess32KiB|BenchmarkMCPConvert32KiB|BenchmarkMCPRoundTrip32KiB' `
  -benchmem -count=3 ./internal/toolkit
```

结果：

| benchmark | run | ns/op | MB/s | B/op | allocs/op |
|-----------|-----|-------|------|------|-----------|
| `BenchmarkInProcess32KiB` | 1 | 33.42 | 980,410 | 8 | 1 |
| `BenchmarkInProcess32KiB` | 2 | 33.83 | 968,514 | 8 | 1 |
| `BenchmarkInProcess32KiB` | 3 | 46.18 | 709,638 | 8 | 1 |
| `BenchmarkMCPConvert32KiB` | 1 | 696.5 | 47,047 | 480 | 6 |
| `BenchmarkMCPConvert32KiB` | 2 | 1,124 | 29,159 | 480 | 6 |
| `BenchmarkMCPConvert32KiB` | 3 | 723.1 | 45,315 | 480 | 6 |
| `BenchmarkMCPRoundTrip32KiB` | 1 | 1,091,724 | 30.01 | 823,844 | 186 |
| `BenchmarkMCPRoundTrip32KiB` | 2 | 1,086,501 | 30.16 | 823,756 | 186 |
| `BenchmarkMCPRoundTrip32KiB` | 3 | 1,077,712 | 30.41 | 824,007 | 186 |

中位数：

| benchmark | median ns/op | 说明 |
|-----------|--------------|------|
| in-process 直调 | ~33.8 ns | 仅返回同一个内存字符串，不含任何 payload 拷贝 |
| `buildMCPCallToolResult` | ~723 ns | 32 KiB 内容的 MCP 内容转换 |
| in-memory MCP 往返 | ~1.09 ms | 完整 JSON-RPC/SDK 编解码 + 传输 |

解读：

- 32 KiB 内容**转换**本身是亚微秒级；
- 完整 MCP 往返 ~1.09 ms，主要成本是 JSON-RPC framing/SDK，而不是 payload
  转换；
- §15.2.2 的“MCP 32 KiB 序列化 p95 ≤10% 相对 in-process”**不能**用一个
  零工作量 echo 基线评判：in-process 基线只有 ~34 ns，任何协议开销都会被放大
  到远大于 10%。该门槛需要换成真实工具工作量（如 grep/view 读文件）的基线
  后再定论，当前标记为 **部分覆盖**。

## 5. read core 直调对照（view，2000 行窗口）

命令：

```powershell
$env:GOGC='20'; $env:GOMAXPROCS='2'
go test -run '^$' `
  -bench 'BenchmarkViewCoreDirect|BenchmarkViewHandlerRuntime' `
  -benchmem -count=3 ./internal/toolcore/viewcore
```

夹具：20,000 行，每行约 1 KiB（long-line 语料），窗口 2000 行；
`Direct` 直接调用 `Read/Render/Metadata`，`HandlerRuntime` 走完整
`viewcore.NewHandler` + 注入 `Runtime`（路径解析、stat、参数解析、输出整形）。

结果：

| benchmark | run | ns/op | MB/s | B/op | allocs/op |
|-----------|-----|-------|------|------|-----------|
| `BenchmarkViewCoreDirect` | 1 | 11,078,426 | 1871 | 13,370,056 | 5,831 |
| `BenchmarkViewCoreDirect` | 2 | 12,779,830 | 1622 | 13,370,278 | 5,831 |
| `BenchmarkViewCoreDirect` | 3 | 12,772,034 | 1623 | 13,370,437 | 5,831 |
| `BenchmarkViewHandlerRuntime` | 1 | 13,659,250 | 1518 | 13,371,596 | 5,840 |
| `BenchmarkViewHandlerRuntime` | 2 | 14,769,153 | 1404 | 13,371,539 | 5,840 |
| `BenchmarkViewHandlerRuntime` | 3 | 12,720,339 | 1630 | 13,371,706 | 5,841 |

中位数与解读：

| 指标 | Direct | Handler | 差值 |
|------|--------|---------|------|
| median ns/op | ~12.77 ms | ~13.66 ms | ~+7.0% |
| B/op | ~13.37 MB | ~13.37 MB | 基本一致 |
| allocs/op | 5,831 | 5,840 | +9 |

- 长行语料下，适配层开销约 7%，已接近但未稳定低于 §15.2.2 的 5% 门槛；
- 宿主噪声约 ±10%（第三次 handler run 快于 direct 中位数），5% 门槛需在
  静默 CI 机器上取 p95 后判定；
- 早前短行夹具（400 行窗口）下开销约 30–45%，说明该指标对小 I/O 工作量
  敏感；固定 stat/解析成本会被放大，必须以设计指定的 2000 行/长行语料为准；
- 顺带优化：Handler 原先 `Lstat + Stat` 两次系统调用已合并为单次 `os.Stat`
  + `IsRegular`，既降低开销，也补上 symlink→FIFO 的漏检。

## 6. 与 §15.2.2 的对照

| 指标 | 设计目标 | 当前证据 |
|------|----------|----------|
| grep 峰值内存不随文件总量线性增长 | 流式/有界 buffer | ✅ 已覆盖：峰值 heap 10k→100k 为 ~1.29→1.47 MB（文件 ×10，峰值 ~1.14×）；B/op 仍是累计分配指标 |
| read core p95 开销 ≤ 5% | 相对直接实现 | ⚠️ 部分覆盖：long-line 2000 行窗口下中位开销 ~7%，宿主噪声 ±10%；需静默 CI 取 p95 判定 |
| MCP 32 KiB 序列化 p95 ≤ 10% | 相对 in-process | ⚠️ 部分覆盖：转换 ~0.72 µs；in-memory 往返 ~1.09 ms，主要为协议开销；需真实工具工作量基线才能评判 10% 门槛 |
| 256 tools list p95 ≤ 50 ms | 含 schema/filter | ✅ 已覆盖：Definitions ~0.16 ms、List ~0.06 ms（中位数） |
| shell capture ≤ CaptureLimit | 不把完整 capture 放进模型 | 已有契约测试，未做 benchmark |
| 回归门槛：中位数超基线 10% 失败 | CI 3 次取中位数 | 本文件提供首轮基线 |