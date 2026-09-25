# Resume 历史交付闭环 E2E（E2E-RESUME-01）

> 场景 ID：**E2E-RESUME-01**（resume 之后整份 transcript 必须交付到原生 scrollback）
> Harness：`scripts/test-aicli-resume-history-e2e.ps1`
> 首次固化验证：2026-09-24（Windows / pwsh 7，真实 provider，会话
> `session_20260924072950_ltYRU9tG`，transcript 291,920 行），两种模式全绿
> （渲染器模式 7/7，行模式 5/5）。运行证据见
> `artifacts/aicli-resume-history-e2e/20260924-225608/`（渲染器）与
> `artifacts/aicli-resume-history-e2e/20260924-225334/`（行模式）。
> 方法论：[debug-guide.md](./debug-guide.md)（独立进程 + `/debug/*` + 读屏 + 机器可校验断言）。

## 1. 为什么需要这个场景

`aicli resume` 大 transcript 会话时，历史只交付了最旧的一段，**尾部永久缺失**。
现场进程（`aicli-planfix.exe`，端口 49296）的稳态读数：

```
cells=6647  next=1288  acked=322  pending=0  in-flight=0  failed=0  invalidated=0
plan_incomplete=true  plan_stalled=true  cell_rows_misses=59  plan_misses=1261  epoch=3
```

这个现场最危险的地方是**它看起来是「空队列健康态」**：`pending=0`、`in-flight=0`、
`failed=0`，执行器空闲。只看「队列排空」会误判为已收敛，但 `plan_incomplete=true` /
`plan_stalled=true` 说明计划在 4096 行采样边界被截断后再也没有续跑，缺失的尾部
（291,920 行中的绝大部分）**永远不会被规划**——注入 `/status` 也不自愈。

根因（已修）：`backend/cmd/aicli/ui/history_effect_planner.go` 的截断续跑分支把续跑
放在探针位置默认的 `planSyncBudget`（250ms）里，而 `state.Transcript.LayoutRows(...)`
在同一表达式里执行、O(全历史) 布局（秒级）吃掉整个预算，于是每一轮都在同一 4096 行
边界再次截断，`PlanStalled` 永久放弃计划。修复：截断续跑改为**无预算**（续跑不在流式
热路径上——`pendingCount` 门已保证队列排空，每个截断计划最多走到一次）。

## 2. 被测契约（稳态判据）

resume 收敛后必须同时满足：

| 维度 | 判据 | 事故现场 |
|------|------|----------|
| 计划完整 | `history_gates.plan_incomplete=false` | `true` |
| 续跑未死锁 | `history_gates.plan_stalled=false` | `true` |
| 队列排空 | `history_effects.pending=0` 且 `in-flight=0` | 同为 0（**假象**，不可单独作为判据） |
| 交付覆盖 | `acked >= scene.cells`（每个 cell 至少一条已交付提交） | `322 / 6647` |
| 全量规划 | `cell_rows_misses`、`plan_misses` 都 >= 90% `cells` | `59 / 1261` |
| 终端可见 | 读屏尾部 needle 出现在写往终端的字节流 / 屏幕尾部非空 | 尾部缺失 |

> `/debug/chat/status` 的字段形状：`app_state.history_effects` 是**预渲染字符串**
> （`"pending=0 in-flight=0 acked=… next=… epoch=…"`），`history_gates` /
> `layout_cache` 才是对象；harness 用正则解析前者。

## 3. 手工复现（debug-guide 路径）

```powershell
# 1) 用当前源码构建（关键：不要复用旧二进制）
cd backend; go build -o .tmp/aicli-resume-e2e.exe ./cmd/aicli; cd ..

# 2) 独立进程启动（无 -NoNewWindow：子进程要有自己的控制台，否则 stdin EOF 提前退出）
Start-Process backend\.tmp\aicli-resume-e2e.exe `
  -ArgumentList 'resume','session_20260924072950_ltYRU9tG','--yolo','--pprof','--debug','--web-port','49296' `
  -WorkingDirectory (Get-Location)

# 3) 收敛后读终态（约 1 分钟；next/acked 必须远大于 1288/322）
Invoke-RestMethod 'http://127.0.0.1:49296/debug/chat/status' |
  Select-Object -ExpandProperty app_state | Format-List history_effects, history_gates, layout_cache

# 4) 读屏：尾部必须能看到 transcript 末尾（提示符上方是最近的消息）
Invoke-RestMethod 'http://127.0.0.1:49296/web/api/screen?view=tui&tail=12' | Select-Object -ExpandProperty text

# 5) 收尾
Invoke-WebRequest 'http://127.0.0.1:49296/web/api/input' -Method POST `
  -Body ([Text.Encoding]::UTF8.GetBytes('{"prompt":"/exit"}')) `
  -ContentType 'application/json; charset=utf-8' -UseBasicParsing
```

## 4. 两种运行模式（同一被测命令，取决于 stdout 是否被判定为终端）

| 模式 | 启动 | `app_state` | 断言集合 |
|------|------|-------------|----------|
| 渲染器模式（默认） | 不重定向 stdout → 子进程 attach 渲染器 | `available=true`，计数器可读 | A1–A7 |
| 行模式（`-RedirectTerminalStream`） | 重定向 stdout/stderr → `term.IsTerminal(stdout)=false` → 不 attach | `available=false` + `reason` 非空（**契约**，不是降级） | A1′–A3′（模式契约 + 流证据）+ A6/A7 |

行模式的 `reason` 固定为
`no interactive renderer: uiActor not attached (requires a real interactive terminal)`。
两种模式都**不**加 `-NoNewWindow`：没有控制台的父进程会让子进程 stdin 立刻 EOF，
进程在恢复历史之后自行 `exit 0`，读不到收敛后的终态。

## 5. 断言表

| # | 断言 | 判据 | 事故对照 |
|---|------|------|----------|
| A1 | 计划完整 | `plan_incomplete=false` 且渲染器已 attach | `true` |
| A2 | 续跑未死锁 | `plan_stalled=false` | `true` |
| A3 | 队列排空 | `pending=0` 且 `in-flight=0` | 同为 0（假象） |
| A4 | 每个 cell 已交付 | `acked >= scene.cells` | `322 < 6647` |
| A5 | 整份 transcript 已规划 | `cell_rows_misses >= 0.9*cells` 且 `plan_misses >= 0.9*cells` | `59 / 1261` |
| A6 | 尾部到了终端 | 行模式：读屏 needle 出现在 `aicli.stdout.log`；渲染器模式：屏幕尾部非空且含正文 | 尾部缺失 |
| A7 | 优雅退出 | POST `/web/api/input {"prompt":"/exit"}` 后 `exit code 0` | — |

A1′/A2′/A3′（行模式替代 A1–A5）：`app_state.available=false` 且 `reason` 非空；
终端字节流 >= 1 MiB；计数器按契约不可用（由 A6 的流证据承担）。

## 6. Harness 用法

```powershell
# 默认：渲染器模式（断言 history-effects 终态与布局缓存）
pwsh -NoProfile -File scripts/test-aicli-resume-history-e2e.ps1

# 行模式：捕获终端字节流，用读屏 needle 反查（CI / 无桌面）
pwsh -NoProfile -File scripts/test-aicli-resume-history-e2e.ps1 -RedirectTerminalStream

# 固定端口 / 复用构建 / 指定会话
pwsh -NoProfile -File scripts/test-aicli-resume-history-e2e.ps1 -Port 49296 -SkipBuild `
  -SessionId session_20260924072950_ltYRU9tG
```

参数：`-ExePath`（默认 `backend/.tmp/aicli-resume-history-e2e.exe`）、`-SessionId`、
`-Port`（0 = 自动挑空闲端口）、`-StartupTimeoutSec`、`-ConvergeTimeoutSec`、
`-ExitTimeoutSec`、`-ArtifactDir`、`-SkipBuild`、`-RedirectTerminalStream`、`-KeepAlive`。
退出码 0 = 全部断言通过，1 = 有 FAIL（逐条列在 stdout 与 `summary.json`）。

证据目录（`artifacts/aicli-resume-history-e2e/<stamp>/`）：`run.log`、`summary.json`、
`evidence.json`（首/末次采样、gates、layout_cache、屏幕尾部）、`go-build.log`、
`aicli.stdout.log` / `aicli.stderr.log`（行模式）。

## 7. 首次固化运行证据（2026-09-24）

| | 事故现场（旧二进制） | 修复后（渲染器模式） | 修复后（行模式） |
|---|---|---|---|
| cells | 6647 | 6679 | — |
| next / acked | 1288 / **322** | 449265 / **149169** | — |
| pending / in-flight | 0 / 0 | 0 / 0 | — |
| plan_incomplete / plan_stalled | **true / true** | **false / false** | — |
| cell_rows_misses / plan_misses | **59 / 1261** | **18970 / 9691** | — |
| 终端流字节 | — | — | **10,312,495** |
| 读屏 needle 命中 | 尾部缺失 | 屏幕尾部含正文 | `backend/internal/api/ski` 命中 |
| 退出码 | — | 0 | 0 |
| 断言 | — | **7/7 PASS** | **5/5 PASS** |

配套的确定性回归测试（单测层，与 e2e 互补）：
`backend/cmd/aicli/ui/history_planning_budget_test.go` 的
`TestTruncatedTranscriptPlanContinuesUntilComplete`——**去掉修复即复现**（每一轮都停在
4096 行采样点），**加回修复即通过**；`backend/cmd/aicli/ui/history_resume_full_coverage_test.go`
驱动真实 planner+executor 断言全量覆盖。

## 8. 失败排查

1. **先确认二进制含修复**：`Get-Item <exe>, backend\cmd\aicli\ui\history_effect_planner.go |
   Select-Object Name, LastWriteTime`——二进制构建时间必须晚于修复落地时间。
   2026-09-24 用户复测所用 `backend/aicli-planfix.exe` 构建于 22:11:22，而修复落地于
   22:31:45，那次复测跑的不是修复后的代码（这是「新命令同样有问题」的直接原因）。
2. `plan_incomplete=true` 且 `next` 很小 → 计划仍被截断：检查续跑分支是否又带上了预算。
3. `app_state.available=false` → 当前是行模式：改用渲染器模式，或按 §5 用 A6 的流证据判据。
4. `A6` needle 未命中 → 读屏尾部与终端流不一致：确认 harness 的 `-RedirectTerminalStream`
   与 `stdout` 日志来自**同一个**进程实例。
5. `A7` 退出码非 0 → 先看 `aicli.stderr.log`；`/exit` 之前请确认进程未提前退出
   （启动方式是否误加了 `-NoNewWindow`）。

## 9. 与既有场景的分工

- [debug-guide.md](./debug-guide.md)（E2E-DEBUG-01/02/03）回答"HTTP 控制面 / 网格能不能
  可靠驱动一次真实 turn"；本场景回答"**resume 的历史交付是否完整**"——它的失败模式恰好
  会让前者全绿（`pending=0` 假象），所以必须独立成场景。
- 本场景**不进** `scripts/test-aicli-e2e-all.ps1` 一键回归：断言依赖「已存在的大 transcript
  会话」（本次为 291,920 行）这一前置条件，CI 无法自建；请按需手动/定时运行。
- Windows Terminal 场景（`scripts/test-aicli-opencode-windows-terminal-e2e.ps1`）验证
  "用户实际看到的渲染对不对"，本场景验证"该写进终端的历史有没有被规划并交付"。
