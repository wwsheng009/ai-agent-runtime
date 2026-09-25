# Resume 启动性能与「卡住」诊断（2026-09-24）

> 用户报告：`aicli resume <大会话>` 启动后**卡住很久才进入恢复**。
> 本文是实测结论（不是推断），含复现命令、时间线、goroutine 证据与修复方向。

## 0. TL;DR

大会话（本次样本：6197 条消息 / 6714 cells / 29.99 万行计划 / 事件日志 91 MB / 61,423 条记录）
上，resume 的成本被**两段互相独立**的开销主导：

| # | 开销 | 实测 | 用户感受 |
|---|------|------|----------|
| R1 | 事件日志全量重放（read 0.43s + parse 3.03s + apply 3.46s ≈ **6.9s**） | 占 ready 的 62% | 启动后长时间**没有任何内容**（composer ready 之前） |
| R2 | `reduceUIControllerState` 持 `UIController` 锁做**无预算**的 scene 布局（6698 cells） | 单次 0.5–1.8s（探针）；最长单次冻结 2.9–3.0s（harness） | ready 之后**仍**卡：按键不回显、屏幕不刷新 |
| R3 | 首个 HTTP 请求撞上 SQLite WASM JIT 编译（wazero `compileSQLite`） | 1.07s（仅首个请求） | 次要；影响早期探针/首帧交互 |

交付节奏本身是健康的（acked 单调递增、recovery 迭代密集、无 acked 回退），
**问题不是「不交付」，而是「进入交付之前」与「交付期间」被长临界区堵住**。

## 1. 复现

```pwsh
# 1) 端到端性能/回归（启动打点 + 恢复时间线 + UI 端点冻结 + 卡顿取证）
pwsh -NoProfile -File scripts/test-aicli-resume-startup-perf-e2e.ps1

# 2) 只量「卡住」：200ms 采样 /debug/chat/status（与 TUI 渲染帧共用同一把锁）
pwsh -NoProfile -File scripts/debug-aicli-resume-status-latency.ps1 -DurationSec 150

# 3) 把 goroutine dump 压成「状态 + 本项目帧」清单，看谁占着锁
python scripts/tmp-goroutine-map.py artifacts/aicli-resume-status-latency/<stamp>/goroutine-stall-2.txt aicli
```

## 2. 启动阶段分布（AICLI_STARTUP_TIMING=1，stderr 打点）

样本 run（`artifacts/aicli-resume-status-latency/20260924-233419`）：

```
persistence            +  751ms  (total  779ms)   打开会话库
resume_metadata        +  281ms  (total 1.062s)
capabilities_runtime_host + 603ms (total 1.753s)
bootstrap              +    1ms  (total 1.769s)
eventlog_read          +  432ms  (total 2.201s)   os.ReadFile 91MB（页缓存命中）
eventlog_parse         + 3.029s  (total 5.229s)   bufio.Scanner + json.Unmarshal × 61,423
eventlog_apply         + 3.455s  (total 8.685s)   61,423 × Encode/applyChangeSet
history_seed           + 1.951s  (total 10.636s)  最新页 seed + reconcile + 统一帧
ready                  +  466ms  (total 11.101s)  publish + 首帧
resume_history_deferred + 4.277s (total 15.378s)  后台补齐较早页（完成后又一次全量重规划）
```

> 复测波动（同一条 91MB 日志，仅进程/页缓存与 CPU 状态不同）：
> `ready=+5,421ms`（read=+102 / parse=+1,530 / apply=+1,586 / seed=+1,192 / ready=+299）。
> 即 R1 的量级是 **5–7s**，不是固定值；判定必须用预算，不能用绝对秒数。

**记录构成归因**（同一条 91MB 日志）：`assistant.reasoning` 20.47MB / 18,534 行、
`tool.progress` 类 ≈ 23,045 行、`llm.request.*` 5,121 行、`tool.*` 9,553 行；
而**真正对应转录的 `assistant_message` 仅 ~19 条、命令注入 ~24 条** ——
61,423 条里约 **43 条（0.07%）** 与转录内容相关。
即：重放规模随**遥测体积**增长，而不是随转录规模增长（harness 的 P11 断言正是这条）。

## 3. 恢复时间线（相对进程启动）

| 事件 | 实测 |
|------|------|
| 首个非空 `scene.cells` | 6,498 ms |
| 第一行历史真正交付（acked>0） | 13,868 ms |
| 计划首次 incomplete | ~6.7 s |
| 收敛（pending=0/in-flight=0/acked≥cells） | 64,132 ms |
| recovery 迭代 | 316 次 / 21 s（≈15/s），acked 单调递增 |

## 4. 「卡住」的量化：UI/debug 端点时延

`/debug/chat/status` 与 TUI 渲染帧、web 客户端读的是**同一把** `ui.(*UIController).mu`
（`controller.go:947` `State()` → `c.mu.Lock()`），所以它的时延就是
「此刻按键有没有回显 / 屏幕有没有刷新」的代理指标。200ms 采样、curl 硬超时：

| 指标 | 实测 |
|------|------|
| fast status p50 | **358 ms** |
| fast status p90 | **712 ms** |
| fast status p99 / max | **1,799 ms** |
| 请求超时（>3s 无响应） | 1 次 |
| 最长单次冻结 | **2,889 ms** |
| 窗口跨度（含采样间隔/取证等待） | 3,867 ms |

同场复测（加固后的 harness，`artifacts/aicli-resume-startup-perf-e2e/20260924-234117`）：

| 指标 | 实测 |
|------|------|
| 采样帧 / 其中超时 | 32 / 1（curl exit=28，3s 硬超时） |
| fast status p95 / max | **2,733.7 ms / 3,041.2 ms** |
| 卡顿窗口 | 1 个：`7124ms→14510ms`（span 7,386 ms，freeze 3,041 ms） |
| 冻结累计 | 3,041 ms |

对照：空闲态同一端点 **2–8 ms**。也就是说恢复期间 UI 路径被拖慢了 **~100–1000 倍**。

## 5. 根因：长临界区（持锁做重活）

`controller.go` 的 apply 循环里，`c.apply()` 是**解锁**执行的，但紧接着的
`reduceUIControllerState()` 是**持锁**执行的：

```go
c.mu.Unlock()                                    // controller.go:563
effects, panicked := c.apply(action, rev, ...)    // :566   ← 解锁执行（好）
c.mu.Lock()                                      // :569
c.state = reduceUIControllerState(c.state, ...)   // :578   ← 持锁执行（问题）
```

而 reduce 里最贵的一段**没有任何预算**：

```
UIController.Run (controller.go:578)                       ← c.mu 持有
 └ reduceUIControllerState (controller_state.go:496)
    └ syncHistoryEffectsForTranscript (history_effect_planner.go:732)
       └ syncHistoryEffectsForTranscriptWithin (:762)
          └ planEligibleHistoryCommitsWithin (:82)
             └ Transcript.LayoutRows (app_state.go:109)
                └ scene.LayoutTranscript (scene/layout.go:72)   ← 全量 6698 cells
                   └ layoutSplitSourceLines → splitSourceLines
```

关键点在 `history_effect_planner.go:62-81` 的注释里写得很明确：
scene 布局被当作「这一轮的**前置工作**」，`screenDeadline` 是在它**之后**才建立的
（"预算从 screening 自身开始计时"）。于是：

* `historyCommitPlanningBudget = 250ms`（`history_effect_planner.go:24`）只覆盖
  **screen rows 循环**（`layoutTranscriptScreenRowsWithin`，每 `layoutBudgetCheckRows = 4096`
  行采样一次，`app_screen_layout.go:197`），
* **不覆盖** `scene.LayoutTranscript` 本身 —— 单 cell 巨大时（149k 行会话里存在
  上万行的命令输出 cell）`splitSourceLines` 是 O(行数) 且无采样点，
* 也不覆盖 `continueTruncatedHistoryPlan` 的续跑轮（注释明确要求传**零值 deadline**，
  `history_effect_planner.go:736-738`）。

卡顿瞬间的 goroutine 证据（`goroutine-stall-2.txt`，goroutine 44 `[runnable]`）：

```
ui.(*UIController).Run  controller.go:578
ui.reduceUIControllerState  controller_state.go:496
ui.syncHistoryEffectsForTranscript  history_effect_planner.go:732
ui.syncHistoryEffectsForTranscriptWithin  history_effect_planner.go:762
ui.planEligibleHistoryCommitsWithin  history_effect_planner.go:82
ui.TranscriptState.LayoutRows  app_state.go:109
ui/scene.LayoutTranscript  scene/layout.go:72          ← 正在布局 0x1a2a = 6698 个 cell
ui/scene.layoutSplitSourceLines  scene/layout_cache.go:87
ui/scene.splitSourceLines  scene/layout.go:91
```

同一时刻的**等待者**（被这把锁堵住的人）：

```
#1 [sync.Mutex.Lock]  ui.(*UIController).postFollowupFromLegacyReducer  controller.go:467
                     ← chatInteractionCoordinator.postSurfaceFacadeAction (chat_ui_actor.go:492)
                     ← FixedBottomSurface.ShowPrompt (fixed_bottom_surface.go:1844)
#6466 [sync.Mutex.Lock] commands.BuildChatDebugDisplaySnapshotWithOptions (chat_debug_display_http.go:757)
                        ← ui.(*UIController).State (controller.go:947)      ← /debug/chat/status
```

即：**启动期间的「发布 composer/首帧」与「任何 debug/web/TUI 读取」都排在同一把锁后面**。

## 6. 建议修复方向（按性价比排序）

> 分阶段实施方案（里程碑、验收预算、风险与回滚）见
> `docs/plan/resume-large-session-optimization-plan-20260924.md`；本节保留结论级排序，方案文档给出可落地步骤。

1. **让 scene 布局可被预算切断**（首选，改动局限在 `ui/scene` + 规划器）：
   `LayoutTranscript` 目前「没有部分结果」（`history_effect_planner.go:62`）。给它加
   按 cell/按行的采样点（复用 `layoutBudgetCheckRows` 思路），把未完成部分存进
   `layout_cache`，下一轮 reduce 从断点续跑。这样临界区被限制在采样粒度内，
   与 `screen rows` 循环的既有设计一致。
2. **把 reduce 的重活移出锁**：`Run` 循环是 `c.state` 的唯一写者（动作串行），
   理论上可以在锁外算出 `nextState`，持锁只做 `c.state = nextState` + revision++
   （`State()` 已经是 `Clone()` 读）。代价是每次动作要深拷贝状态（大转录上不便宜），
   需要按「拷贝成本 < 布局成本」评估。
3. **减少全量重布局的次数**：`resume_history_deferred`（补齐较早页）完成后会再触发
   一次全量规划；把多页合并成一次发布、或让追加的**较老**页不影响已保留视口的
   计划输入，就能把「启动后第二次卡顿」消掉。
4. **事件日志瘦身/索引**（对应 R1）：重放时按记录类型过滤（reasoning/tool.progress/
   llm.request 不参与转录），或为会话维护一个「转录索引」，让 resume 只读与转录
   相关的记录（本样本 43 / 61,423）。这是「进入恢复」前那 6.9s 的直接来源。
5. **预热 SQLite WASM**（对应 R3）：首个请求 1.07s 花在 `ncruces/go-sqlite3`
   的 wazero JIT 编译上，可在启动早期并发预热。

## 7. 回归测试（已加固）

`scripts/test-aicli-resume-startup-perf-e2e.ps1` 现有 14 条断言（P1..P13 + P2b）。
最近一次全量跑（`20260924-234117`）：**10 passed / 4 failed**。

| 断言 | 内容 | 实测 |
|------|------|------|
| P1 | 启动打点可解析、含 ready、elapsed 单调 | PASS（marks=20） |
| P2 | 进程启动 → composer ready ≤ `ReadyBudgetMs`(3s) | **FAIL** +5,421 ms |
| P2b | 首次 recovery 早于计划 settled（交错而非串行） | PASS（6,567 ms < 32,804 ms） |
| P3 | 增量交付：acked 在 ≥3 个采样帧推进 | PASS（13 帧） |
| P4 | 执行器无静默窗口：相邻 recovery 迭代 ≤6s | PASS（4,022 ms） |
| P5 | 无内建 90s 启动挂起 dump | PASS |
| P6 | 收敛：计划完整 + 队列排空 + acked ≥ cells | PASS（acked=149,327 / cells=6,719） |
| P7 | `/exit` 优雅退出 code 0 | PASS |
| P8 | `AICLI_STARTUP_TIMING` 生效 | PASS |
| P9 | 进程启动 → 第一行历史交付 ≤ `FirstContentBudgetMs`(8s) | **FAIL** 11,222 ms ← 用户报告的「卡住很久」 |
| P10 | acked 从不回退（计划重置/重投递） | PASS |
| P11 | replayed 记录数 ≤ 4 × cells | **FAIL**（61,447 条 / 91.1MB vs 26,876） |
| P12 | UI 端点：最长单次冻结 ≤2s 且冻结累计 ≤4s | **FAIL**（3,041 ms / 3,041 ms） |
| P13 | 一旦卡顿，artifact 内必须有 goroutine dump（含持锁者） | PASS（1 份） |

> **harness 自身的坑（本轮又修掉一层）**：P12 第一次跑时把 3s 冻结判成了 PASS ——
> 卡顿窗口只在「开帧」写 `end_ms`、关闭帧不推进，`span` 退化成 0；而且用 span 而不是
> 「窗口内最大单次探针时延」当冻结时长。现在 `freeze = max(status_ms in window)`，
> span 只作参考列。这是「测试对卡住无感」的第二层盲区（第一层是采样传输
> `Invoke-WebRequest -TimeoutSec` 在响应体停滞时不兑现，已换 `curl.exe -m`）。

harness 自身的采样传输已换成 `curl.exe -m`（外部硬超时）：端点被锁卡住时拿到
`exit=28` 而不是把 harness 自己挂死（此前 PowerShell `-TimeoutSec` 在响应体停滞时
不总是兑现，实测让 harness 静默了 6 分钟，且 timeline 停在卡住那一刻 —— 这本身
就是「测试对卡住无感」的盲区，现已修掉）。

每帧新增 `status_ms` / `status_exit` / `exec_ms` 字段，卡顿窗口会被记进
`timeline.jsonl`，并自动落 `goroutine-stall-*.txt` / `screen-stall-*.json` 取证。
