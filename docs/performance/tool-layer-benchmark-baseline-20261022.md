# 工具层 benchmark 基线（2026-10-22）

- 设计文档：`docs/plan/tool-layer-abstraction-design-20261022.md` §15.2
- 代码版本：`910c4f05`
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

| benchmark | run | ns/op | MB/s | B/op | allocs/op |
|-----------|-----|-------|------|------|-----------|
| `BenchmarkGlob10kNoMatch` | 1 | 55,883,558 | — | 4,553,498 | 51,595 |
| `BenchmarkGlob10kNoMatch` | 2 | 55,600,903 | — | 4,579,335 | 51,596 |
| `BenchmarkGlob10kNoMatch` | 3 | 50,814,825 | — | 4,545,697 | 51,595 |
| `BenchmarkGrep10kNoMatch` | 1 | 3,345,692,100 | 3.06 | 11,163,944 | 91,724 |
| `BenchmarkGrep10kNoMatch` | 2 | 3,513,501,500 | 2.91 | 11,062,160 | 91,727 |
| `BenchmarkGrep10kNoMatch` | 3 | 3,461,170,000 | 2.96 | 11,192,496 | 91,723 |

中位数（3 次）：

| benchmark | median ns/op | median B/op |
|-----------|--------------|-------------|
| glob 10k | ~55.6 ms | ~4.55 MB |
| grep 10k | ~3.46 s | ~11.1 MB |

说明：

- glob 的分配量稳定在 ~4.55 MB、~51.6k allocs；
- grep 的分配量稳定在 ~11.1 MB、~91.7k allocs，未随 10k→100k 语料线性
  增长需要在 100k 运行中对照；
- 当前机器/受限并发下 grep 吞吐约 3 MB/s，属于**保守基线**，需在高性能
  CI 机器上复测后再定为回归门槛。

## 2. large-repo（100k 文件）

命令：

```powershell
$env:GOGC='20'; $env:GOMAXPROCS='2'; $env:AICLI_TOOLCORE_BENCH_LARGE='1'
go test -run '^$' `
  -bench 'BenchmarkGlob100kNoMatch|BenchmarkGrep100kNoMatch' `
  -benchmem -benchtime=1x -count=1 ./internal/toolcore/searchcore
```

状态：待本轮运行结果回填。

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

## 4. 与 §15.2.2 的对照

| 指标 | 设计目标 | 当前证据 |
|------|----------|----------|
| grep 峰值内存不随文件总量线性增长 | 流式/有界 buffer | 待 100k 对照 |
| read core p95 开销 ≤ 5% | 相对直接实现 | 未覆盖（缺 surface 对比基准） |
| MCP 32 KiB 序列化 p95 ≤ 10% | 相对 in-process | 未覆盖 |
| 256 tools list p95 ≤ 50 ms | 含 schema/filter | ✅ 已覆盖：Definitions ~0.16 ms、List ~0.06 ms（中位数） |
| shell capture ≤ CaptureLimit | 不把完整 capture 放进模型 | 已有契约测试，未做 benchmark |
| 回归门槛：中位数超基线 10% 失败 | CI 3 次取中位数 | 本文件提供首轮基线 |