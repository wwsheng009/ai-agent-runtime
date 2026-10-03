# 性能专题

本目录收录**实测驱动**的性能问题诊断与优化记录。每份文档都必须能回答三个问题：
现象怎么测出来的、根因是哪条代码路径、改动前后同一口径下的数字是多少。

判定标准（与《指导思想》一致）：**多动手少空想，多实测少猜测**。
没有测量支撑的"优化"不进代码，进文档时也必须显式标注为"未验证"。

## 文档清单

| 文档 | 主题 | 状态 |
|---|---|---|
| [tui-turn-latency-root-cause-20261003.md](./tui-turn-latency-root-cause-20261003.md) | TUI 回合延迟根因：工具面把一次知识库全表聚合放大成 56 次 | 已定位 |
| [knowledge-read-path-optimization-20261003.md](./knowledge-read-path-optimization-20261003.md) | 上述根因的修复实施与实测；含 P2（PRAGMA）否定结论与平台矩阵 | 已实施并验证 |

## TL;DR（2026-10-03）

现象：LLM 网络请求 0.6–2.4 s，但 aicli TUI 从用户输入到输出要**两分多钟**。

根因：外层工具面 `skillToolSurface` 未实现 `ResolveToolSource`，导致 agent 在
`computeAvailableTools` 里对**每个**工具都回落到 `FindTool` —— 而 `FindTool`
为了找一个工具会重建整张工具表，每次都要重跑知识层 `Stats`（四个聚合，
355 MiB 库上单次 ~2.5 s）。85 个工具 → 56 次全表聚合 ≈ 137 s。

| 口径 | 修复前 | 修复后 | 方法 |
|---|---|---|---|
| 回合总时长 | 153,927 / 374,353 ms | **1,124–1,365 ms** | `/web/api/turn` 的 `duration_ms` |
| 输入 → 首次 LLM 请求 | 133,000–144,000 ms | **267 / 510 ms** | `runtime-events.jsonl` 时间戳差 |
| 单次工具面解析取数 | 1,712 ms | **19 ms**（TTL 内 ~0 ms） | 真实库副本 + 生产同款驱动探针 |
| `knowledge.Stats` | 1,712 ms | **544 ms** | 同上 |
| `Stats` 在 CPU 采样中的占比 | 19.0 s / 89.3% | **不出现** | `/debug/pprof/profile` + `go tool pprof -peek` |
| 进程 CPU（同窗口） | 106%（单核打满） | 27%（大部分空闲） | 同上 |

修复未改变架构：仍是单写者 / 多读者、仍是纯 Go SQLite 驱动（**CGO=0 全程成立**，
未引入原生 SQLite）。

## 复现入口

```powershell
# 1. 回合耗时（权威口径：runtime 自报的 duration_ms）
curl.exe -s "http://127.0.0.1:<port>/web/api/turn"

# 2. 逐请求 LLM 耗时（duration_ms / first_token_ms / cache_status）
curl.exe -s "http://127.0.0.1:<port>/web/api/cache/requests?limit=10"

# 3. CPU profile：抓的时候要有回合在跑，否则抓到的是空转
curl.exe -s -o cpu.pb.gz "http://127.0.0.1:<port>/debug/pprof/profile?seconds=20"
go tool pprof -top -cum cpu.pb.gz
go tool pprof -peek "sqliteStore..Stats" cpu.pb.gz   # 修复前 19s，修复后为空

# 4. 用户可见时间线（输入 → 首次 LLM 请求的空白就在这里）
#    chat-logs/<年>/<月>/<日>/<session_id>/events/runtime-events.jsonl
```

`<port>` 见 `/debug/endpoints`（回环监听；写操作需 `X-AICLI-Token`，可从
`GET /web/api/token` 读取）。

## 记录约定

1. **先量化再动手**：写清测量命令、样本量、机器条件（本机 14 GB 内存，属于受限环境）。
2. **区分"有依据的假设"与"实测"**：例如"Linux 上 mmap 应当有效"是假设，
   在 Windows 上无法验证，必须标注，不允许写成结论。
3. **否定结果也要留档**：测了没效果的方案（如 `PRAGMA cache_size`）同样记录，
   避免后人重复踩。
4. **结论必须落到文件与行号**：便于回归时直接定位。
