# 大会话 resume 优化方案（2026-09-24）

> 对象：`aicli resume <大会话>`（样本：6,197 条消息 / 6,719 cells / 语义行 ~16 万 / 事件日志 91 MB / 61,423 条记录）。
> 性质：**实施方案**（分阶段、可回滚、每条改动绑定验收断言）。
> 诊断证据、时间线、goroutine 取证见 `docs/e2e/resume-startup-performance.md`；
> 回归入口 `scripts/test-aicli-resume-startup-perf-e2e.ps1`（14 条断言）。
> 代码锚点以 2026-09-24 工作区为准（含未提交改动），行号会随改动漂移，实施前先 grep 符号名。

## 0. TL;DR

三条主线，按「性价比 × 风险」排序执行：

| 主线 | 现状（实测） | 目标 | 验收断言 |
|------|--------------|------|----------|
| **L1 事件日志重放裁剪** | 61,423 条全量 read+parse+apply = **6.9s**（占 ready 62%） | ≤1.5s（先 ≤3s） | P2 / P9 / P11 |
| **L2 临界区缩短** | 单次冻结 **3.04s**、p95 2.73s、累计 3.04s | 单次 ≤250ms、p95 ≤500ms | P12（+ 建议新增 P14/P15） |
| **L3 WASM 预热** | 首个 HTTP 请求 1.07s（wazero JIT） | ≤200ms | 探针（无专用断言） |

**执行顺序建议**：M1 = L3 + L2.1（隔离、低风险、3 行级改动）→ M2 = L2.2（缓存与增量；L2.5 已实测否决，
重规划改由 §4.5 重估 / §4.7 承接；另有 §4.8 的 P12 探针地板待处理，建议优先）→
M3 = L1.0 普查 + L1.1 便宜解析 → M4 = L1.2 双日志分流 → M5（可选）= L1.3 检查点。

**验收口径**：harness 四条 FAIL（P2 / P9 / P11 / P12）全绿，且 P1–P13 无回归；
即「进入恢复 ≤3s、第一行内容 ≤8s、重放规模随转录而非遥测增长、UI 端点不冻结」。

## 1. 基线（实测，非推断）

最近一次全量 run：`artifacts/aicli-resume-startup-perf-e2e/20260924-234117`（10 passed / 4 failed）。

启动打点（`AICLI_STARTUP_TIMING=1`，stderr）：

```
persistence               +  751ms  (total  779ms)
resume_metadata           +  281ms  (total 1.062s)
capabilities_runtime_host +  603ms  (total 1.753s)
bootstrap                 +    1ms  (total 1.769s)
eventlog_read             +  432ms  (total 2.201s)   ← R1
eventlog_parse            + 3.029s  (total 5.229s)   ← R1
eventlog_apply            + 3.455s  (total 8.685s)   ← R1
history_seed              + 1.951s  (total 10.636s)
ready                     +  466ms  (total 11.101s)
resume_history_deferred   + 4.277s  (total 15.378s)
```

复测波动（同一份日志，仅页缓存/CPU 状态不同）：`ready=+5,421ms`
（read 102 / parse 1,530 / apply 1,586 / seed 1,192 / ready 299）→ **R1 量级是 5–7s，判定必须用预算，不能用绝对秒数**。

记录构成（同一条 91MB 日志）：`assistant.reasoning` 20.47MB / 18,534 行、`tool.progress` 类 ≈23,045 行、
`llm.request.*` 5,121 行、`tool.*` 9,553 行；而 `assistant_message` 仅 ~19 条、命令注入 ~24 条。
即 **61,423 条里约 43 条（0.07%）与转录内容相关** —— 重放规模随遥测体积增长，而不是随转录规模增长（P11 断言的正是这条）。

## 2. 现状盘点：已经做了什么、还差什么

### 2.1 已落地（工作区未提交；属**正确性**修复，不是性能修复）

| 机制 | 锚点 | 作用 |
|------|------|------|
| 截断前缀续跑 | `history_effect_planner.go:801 continueTruncatedHistoryPlan` | 前缀交付完成后主动续跑，不再依赖「下一次 transcript 迁移」 |
| 续跑触发器（5 处非 ack 迁移） | `controller_state.go:272 / 290 / 331 / 338 / 358` | ack / batch-ack / settle / `ContinueHistoryPlanAction` / `ProjectionRecovered` 都能解锁续跑 |
| 执行器自愈 | `action.go:520 ContinueHistoryPlanAction`；`history_effect_queue.go:56 PlanIncomplete`、`:67 PlanStalled` | 队列空 + 无恢复义务 + 计划欠账 → 请求续跑；无法推进则置 Stall 防自旋 |
| 预算化 screen rows | `app_screen_layout.go:197 layoutBudgetCheckRows`、`:207 layoutTranscriptScreenRowsWithin`、`:225-228` 采样点 | screen rows 循环按 4096 行采样切断，**至少产出一行**避免「空前缀=没有历史」歧义 |
| 语义行 split 缓存 | `scene/layout_cache.go:31-34`（4096 条目 / 200k 行）、`:79-90 layoutSplitSourceLines` | cell → 语义行切片缓存（零拷贝共享 source） |

**它没有解决的**：续跑轮用**零值 deadline**（`history_effect_planner.go:801` 函数注释明确要求），
即最贵的一轮布局**故意不设预算**；而它仍在 `UIController.mu` 临界区内
（`controller.go:569 → :578`）。所以 P12 的 3.04s 冻结仍在 —— 正确性修好了，性能敞口没动。

### 2.2 仍然敞口

| # | 敞口 | 锚点 | 实测 |
|---|------|------|------|
| R1 | 全量重放，read/parse/apply 三段均无裁剪 | `chat_runtime_events.go:3333-3499` | 6.9s（占 ready 62%） |
| R2a | reduce 持锁执行 | `controller.go:563（解锁 apply）/ :569（加锁）/ :578（reduce）` | 冻结 3.04s |
| R2b | 语义布局无预算：`screenDeadline` 建立在 `LayoutRows` **之前**，被布局整个吃掉 | `history_effect_planner.go:78-84` | 单次 0.5–1.8s |
| R2c | split 缓存容量 < 转录规模 → 抖动 | `scene/layout_cache.go:32-33`（4096 < 6,719 cells；200k 行 ≈ 语义行数量级） | 重复 split |
| R3 | 首个请求撞 SQLite WASM JIT | wazero `compileSQLite` | 1.07s（仅首次） |

## 3. L1：事件日志重放裁剪（最大收益：6.9s → ≤1.5s）

### 3.1 现状链路（`backend/cmd/aicli/commands/chat_runtime_events.go`）

```
replaySessionLoadEventLog                     :3329
 └ replayEventLogWithLoadAuthorization        :3333
    ├ os.ReadFile(runtime-events.jsonl)       :3342   → markChatStartup("eventlog_read")   :3356
    ├ bufio.Scanner + 每行 json.Unmarshal      :3376-3444
    │   （注入记录 Type=="" 再解析一次）        :3395   → markChatStartup("eventlog_parse") :3451
    └ 逐条 SubmitXxx / Encode → applyChangeSet :3457-3495
        └ 仅 isChatRenderDataPlaneSuppressedEvent 跳过 :3490
          （chat_input_events.go:52-54，只覆盖 input.queue 诊断 + dynamic_status + user_submitted）
                                                 → markChatStartup("eventlog_apply") :3496
```

日志路径：`<RuntimeEventsDir>/runtime-events.jsonl`（`eventLogFilePath` `:3102`，含旧布局回退 `:3121-3129`）。

### 3.2 L1.0 重放必要性普查（**先做，决定可裁剪集合**）

> 动代码之前必须先有逐类型证据。当前「reasoning / tool.progress 不参与转录」是**推断**（来自体积与条数归因），
> 而 `isChatRenderDataPlaneSuppressedEvent` 的白名单极小 —— 说明绝大多数遥测事件**确实走了** Encode + applyChangeSet。
> 在没有普查表之前裁剪，风险是「resume 后 transcript 缺内容」。

做法（一次性，1–2 小时）：

1. 在 `:3459` 的 apply 循环里按 `en.event.Type` 累计：`applied` 次数、`len(Scene.Cells)` 前后差、
   是否更新了已存在的 mutable cell、payload 字节数。
2. 对样本会话产出一张表：**类型 → 是否新增 cell / 是否更新终态 cell / 是否只更新 mutable cell / 字节数**。
3. 判据：只有「**既不新增 cell、也不更新任何终态 cell**」的类型才进入裁剪白名单；
   `llm.request.*`、`assistant.reasoning`、`tool.progress` 三类里只裁普查证明无影响的那部分。
4. 保守规则：**识别不出类型 → 一律按不可裁剪处理**（宁可慢，不可丢）。

产出：`docs/e2e/resume-replay-census.md`（类型 × 影响表）+ 代码里的裁剪白名单常量（单一事实源，供 L1.1/L1.2 复用）。

### 3.3 L1.1 便宜解析（省 parse：2–3s → ~0.3s）

全行 `json.Unmarshal` 是为「知道这行是什么类型」付出的代价。改为字节级取 `"type"` 前缀：

- 在 `:3384` 之前插入 `eventLogTypePrefix(line)`：命中白名单 → 直接 `continue`（同时计数）；
- 未命中/识别失败 → 走原解析路径；
- 注入记录（`Type==""`，`:3391`）永远走原路径（它们承载 historyReset / 命令 / 交互注入）。

预期：61,423 行里 ~59k 行免解析 → `eventlog_parse` 3.0s → ~0.3s；
若普查证明这些类型也不需要 apply，则 `eventlog_apply` 同比例下降（3.45s → ~0.3s）。

风险控制：`eventLogFailures` 语义不变（跳过 ≠ 失败）；新增「跳过计数」并在 debug display 暴露，便于回归对比。

### 3.4 L1.2 双日志分流（根治写侧；新会话直接达标，旧日志兼容）

写侧（`appendEventLog` `:3135`）：按类型分流到两个 append-only 文件：

- `runtime-events.jsonl`（**转录日志**）：historyReset、注入记录、普查判定「会产生 cell」的事件 → **resume 只读它**；
- `runtime-telemetry.jsonl`：reasoning / tool.progress / llm.request.* 等 → 诊断、timeline、SSE 用，resume 不读。

读侧：`eventLogFilePath()`（`:3102`）保持不变（返回转录日志）；`eventLogReplayed/recorded/bytes`
语义改为「转录日志」，并在 `chat_debug_display_http.go:595` 的 `event_log` 块同时暴露两份计数（避免 P11 口径歧义）。

旧会话迁移：首次 resume 检测到「单文件且无分流」→ **后台**一次性 split（读一遍 + 写两份 + 原文件保留为 `.bak`），
**不阻塞本次启动**（本次仍走 L1.1 裁剪路径）。

预期：新会话 R1 从 6.9s → read ~10ms + parse ~0.1s + apply ~0.2s；P11 直接过。

### 3.5 L1.3 检查点记录（可选，最彻底）

在同一个 append-only 日志里，每次「转录静止」或每 K 条写一条 **checkpoint 记录**
（复用 `historyReset` 的「携带 seed 单位」模式：持久化历史 + 已终态 cell 的 source/元数据）。

- 重放规则：从**最后一条完整 checkpoint** 开始重放；无 checkpoint → 全量（向后兼容）；
- 收益：resume 成本 **O(增量)** 而不是 O(历史)，对 >500MB 会话是唯一可行解；
- 成本：checkpoint 序列化/校验/失效策略 + 「只在静止点写」的不变量；
- 建议：L1.2 落地并观测一段时间后再做（L1.2 已能覆盖当前样本量级）。

## 4. L2：临界区缩短（冻结 3.04s → ≤250ms）

### 4.1 现状

```go
c.mu.Unlock()                                     // controller.go:563
effects, panicked := c.apply(action, rev, ...)     // :566   ← 解锁执行（好）
c.mu.Lock()                                        // :569
c.state = reduceUIControllerState(c.state, ...)    // :578   ← 持锁执行（冻结在这里）
```

冻结链（goroutine 取证见诊断报告 §5）：

```
ui.(*UIController).Run                        controller.go:578      ← c.mu 持有
 └ ui.reduceUIControllerState                 controller_state.go:57
    └ ui.syncHistoryEffectsForTranscript      history_effect_planner.go:731
       └ planEligibleHistoryCommitsWithin     history_effect_planner.go:45
          └ ui.TranscriptState.LayoutRows     app_state.go:101
             └ ui/scene.LayoutTranscript      scene/layout.go:49
                └ scene.layoutSplitSourceLines / splitSourceLines
```

等待者（同刻被堵）：`postFollowupFromLegacyReducer`（`controller.go:459`，发布 composer）
与 `/debug/chat/status` → `State()`（`controller.go:947`）。

> 行号说明：上面的行号取自**当前工作区源码**；同一栈在 23:34 的 dump
> （`artifacts/aicli-resume-status-latency/20260924-233419/goroutine-stall-2.txt:767-783`）里显示的是
> **调用点**行号：`scene/layout.go:72`、`scene/layout_cache.go:87`、`app_state.go:109`、
> `history_effect_planner.go:82 / :762 / :732`、`controller_state.go:496`、`controller.go:578`。两套行号指向同一段代码。

### 4.2 L2.1 修正 deadline 顺序（3 行，先做）

`history_effect_planner.go:78-84` 现状：`screenDeadline` 在 `layoutRows` **之前**建立 →
被 1–2s 的 `LayoutTranscript` 整个吃掉 → screening 循环在第一个采样点（`index=4096`）拿到的 deadline 已过期
→ 每轮只返回同一个前缀 → `PlanStalled` 被置位、续跑 kick 被解除。
（工作区注释 `:69-77` 已经写明这条规则，**但代码没有照做** —— 属于半成品修复。）

改法：把

```go
screenDeadline := deadline
if !deadline.IsZero() { screenDeadline = time.Now().Add(historyCommitPlanningBudget) }
layoutRows := state.Transcript.LayoutRows(state.LayoutGeneration)
```

改为「先求值 `layoutRows`，再建立 `screenDeadline`」。

收益：screening 每轮拿到完整 250ms 预算，截断前缀真正前进、`PlanStalled` 不再误置位、
续跑次数与冻结次数同步下降（**不降低单次布局成本**，降低的是「重复烧同一份布局」的次数）。

验收：P12 冻结窗口数/累计下降；`history_planning_budget_test.go`、`history_resume_full_coverage_test.go` 保持绿。

### 4.3 L2.2 语义布局缓存/增量（把单次 1.8s 降一个量级）

两个独立问题：

1. **容量不足**：`splitSourceCacheMax=4096` < 6,719 cells，`splitSourceCacheMaxLines=200000`
   ≈ 语义行数量级（且 mutable cell 每个 revision 都会新增键）→ 必然抖动、重复 split。
   改法：容量按转录规模自适应（或改为「按 cell ID 强引用缓存 + 仅终态 cell 进入 LRU」），
   并把 `LayoutRow` 切片分配改为可复用缓冲。
2. **append-only cell 的 O(n²)**：mutable cell 的 source 每次 delta 都在增长，键里的 revision 变化
   → 每个 chunk 重切**整份** source（大命令输出 cell 上万行时尤其贵）。
   改法：缓存「已 split 的前缀行 + 已消费长度」，新 revision 只 split 新增后缀（行切片仍零拷贝共享 source）。

验收：新增基准 `BenchmarkLayoutTranscriptResumeScale`（6,719 cells / ~160k 行；冷/热两种缓存状态），
热路径目标 ≤20ms；harness P12 单次冻结 ≤250ms。

> **2026-09-25 落地**：基准已建（`scene/layout_scale_bench_test.go`，三态 cold/hot/append），
> 热路径 **52.2 ms → 8.5 ms（−84%）**、分配 **52.2 MB → 8.9 MB（−83%）** ⇒ 基准门禁达成（2.3× 余量）。
> 改动：三元组键 → 每 cell 单条目 + O(1) 惰性逐出（容量 4096→8192 条目 / 200k→400k 行）、
> append-only 前缀复用、`LayoutTranscript` 两遍预分配、`splitSourceLines` 精确预分配。
> **harness P12 仍未取数**（环境阻塞）⇒ 不得表述为「P12 已通过」。
> 实测与归因：`docs/e2e/resume-l2-2-layout-cache-measurement.md`。

### 4.4 L2.3 预算覆盖 LayoutTranscript（长尾保险）

给 `scene.LayoutTranscript` 加**按 cell 采样**（复用 `layoutBudgetCheckRows` 思路）并返回
「已布局前缀 + complete」，未完成部分留在缓存、下一轮续跑。

注意两条既有教训（都已在代码注释里）：
- **不能返回空前缀**（`app_screen_layout.go:218-224`：空前缀 = 「没有可交付历史」→ 永久空屏）；
- 截断窗口下跳过后缀 cell 的 whole-cell 提交（`history_effect_planner.go:109-115`：不同身份会写两遍）。

依赖关系：L2.2 之后单次全量已经便宜，L2.3 是「单 cell 上万行」的长尾保险，可在 M2 之后视 P12 余量决定是否做。

> **2026-09-25 更新（L2.5 实测）**：L2.2 之后布局相对规划已是零头 —— 整段较早页 1.7 ms、
> 全 transcript 热态 2.5 ms，而单次规划 104 ms 起（见 §4.6）。L2.3 因此**降级为可选项**：
> 它只在「单 cell 上万行」时才有意义（那种场景下单 cell 的布局本身可能占满预算）。

### 4.5 L2.4 reduce 移出锁（备选，成本最高，不推荐先做）

`Run` 循环是 `c.state` 的唯一写者（动作串行），理论上可在锁外算出 `nextState`，
持锁只做 `c.state = nextState; revision++`（`State()` 已是 `Clone()` 读）。
代价：每次动作需要深拷贝状态。**先测「拷贝成本 < 布局成本」再决定** —— 本轮已给出前半段数据：
transcript 安装/克隆 4,001 cells 只要 **0.45 ms**（仪器子基准 `install_only`），而锁内的大头是
规划（104 ms）与计划集成（~180 ms）。**若整状态深拷贝（再加 `LayoutRows`）仍在个位数 ms，
L2.4 就能把第二次规划的 602 ms 整体移出锁** —— 这比在规划器里做增量（§4.7）改动更小、收益更彻底。
待测：整状态 `Clone()` 的耗时与分配（可复用 `history_prepend_replan_bench_test.go` 的语料）。

### 4.6 L2.5 减少全量重规划次数（消掉启动后第二次冻结）—— **经测量否决**

`resume_history_deferred`（窗口化补齐较早页）完成后会**再触发一次全量规划**（诊断报告 §2）。
本轮用单进程仪器做了结构断言与耗时归因，结论是**本节原来的两条改法都不成立**：

- ① 「把多页合并成一次发布」—— **已是现状**：`startDeferredResumeHistoryLoad` 一次取回全部较早页、
  一次前插、一次重放（`chat_session.go:692–716`），没有可合并的触发次数。
- ② 「把更老页排除在指纹之外让 memo 命中」—— **正确性不成立**：较早页必须真的进入计划、
  且必须排在最新页之前（仪器断言 `firstOlder=0 < firstNewer=1000`）；memo 命中会跳过整个规划，
  1,000 个 commit 不会被铸造，补回的历史永远到不了 scrollback。

耗时归因（6,720 cells / 161k 行合成语料，3 次运行中位数）：

| 环节 | 耗时 | 占第二次规划（602 ms） |
|------|------|------------------------|
| transcript 安装/克隆 | 0.45 ms | 0.1% |
| 语义布局（较早页 1.7 ms + 全量热态 2.5 ms） | ≤5 ms | ~1% |
| 规划器 screening + 铸 commit（`plan_only` @4,001 cells） | 104 ms | — |
| reduce 计划集成（`first_plan` 284 − plan 104 − install 0.45） | ~180 ms | — |
| **整条 reduce（第二次规划）** | **602 ms**（562–657） | 100% |

⇒ **成本在规划器与计划集成，不在布局**；第二次相对第一次超线性（cells 比 1.68 / 耗时比 2.12），
与「全量重 screening + 全量集成」一致。

**验收口径同时作废**：「冻结窗口数 = 1」比 harness 的预算（单次 ≤2 s 且累计 ≤4 s）更严；
按 602 ms（生产约 2× → ~1.2 s）估计，两个窗口也可能落在预算内。

实测、结构证据与复跑配方：`docs/e2e/resume-l2-5-deferred-replan-measurement.md`。

### 4.7 L2.6 增量 screening（原 L2.5 的真实替代，更大）

把 L2.2 在布局层做成的「前缀复用」上移到计划层：按 cell 身份/revision 缓存已 screening 的屏幕行，
较早页插入时只 screening 新增前缀、复用后缀，并按「较早页在前」重组计划顺序。
直接消掉 §4.6 里 602 ms 的大头，且**不触碰**销毁式重放/授权语义。

前置：先做 §4.5 的整状态克隆测量 —— 若克隆是毫秒级，L2.4 用更小的改动就能把整段 602 ms 移出锁，
应优先于本条。

验收：`BenchmarkDeferredOlderPageReplan/second_plan_prepend` 相对本轮中位数（602 ms）≤1/3，
且 `TestDeferredOlderPagePrependReplansAndCoversOlderCells` 保持绿（覆盖与顺序不可回退）。

### 4.8 L2.7 去掉 `State()` 深拷贝地板（新，测量驱动，**先加观测**）

L2.5 的仪器（§4.6）顺带测到：`UIControllerState.Clone()` 的成本 **99.7% 在 history ledger**
（1,500 条目 = 157–180 ms / 96.5 MB；同一状态的 transcript 只要 0.27–0.48 ms），
因为 `ledger.Clone()` 逐条深拷贝 `Commit.Lines []render.Line`（`history_commit.go:607-619`），
而 `byToken` **只增不减**（全文件唯一的 `delete` 在 `byRange` 的 re-mint 路径，`:335-341`）。

两个调用点都在锁内，且都不是调试专用路径：

- `/debug/chat/status` —— **就是 P12 的探针**（`chat_debug_display_http.go:772` → `controller.go:943-950`），
  harness 每 200 ms 打一次，且 **`?fast=1` 不跳过 `app_state`**（契约测试要求 fast 也输出它）；
- `HistoryCommitExecutor.runOne` —— 每次提交调 `State()` **三次**（`:141/154/191`）。

⇒ **P12 的窗口时延包含探针自身的克隆时间**，存在随 ledger 增长的非零地板；L2.2 / L2.5 都动不了它。

执行顺序（**先观测、后改代码**）：

1. 把 `len(ledger.byToken)` + 估算字节 + 「单次 `State()` 克隆耗时」暴露到 `/debug/chat/status`，
   并纳入 §7.3 的 P16 归因打点 —— 先量出生产上的真实条目数与单次克隆耗时（本轮**未测到**）；
2. 探针侧：给 `UIController` 加「持锁只读摘要」访问器，`/debug/chat/status` 不再 `Clone()` 全状态；
3. 执行器侧：`runOne` 的三处 `State()` 换成定向访问器（`Pending()` / `Entry(token)` / `Acked(token)`），
   与 `history_effect_queue.go:139-142` 已做过的同型修法一致；
4. （中期）已 ack 的 entry 降级为不含 `Lines` 的终态记录，把地板从「曾铸出总数」降回「pending 数」。

验收：6,700 cells 会话上 `/debug/chat/status?fast=1` 探针时延 ≤50 ms 且**与 ledger 规模无关**；
`chat_debug_bounded_contract_test.go` 保持绿；harness P12 地板消失。
本条不触碰销毁式重放/授权语义，但改了 debug 端点的数据来源 ⇒ 需回归
`scripts/test-aicli-debug-endpoints-e2e.ps1`。

实测与代码证据：`docs/e2e/resume-l2-5-deferred-replan-measurement.md` §8。

## 5. L3：SQLite WASM 预热（1.07s → ≤200ms）

首个 HTTP 请求的 1.07s 花在 `ncruces/go-sqlite3` 的 wazero JIT 编译（`compileSQLite`）。

改法：在启动早期（`persistence` 阶段之后、`capabilities_runtime_host` 附近）并发发起一次
预热（打开一个连接 + 一次 trivial query），把 JIT 编译移出首个用户请求的关键路径；
失败只记日志、不影响启动。

验收：探针首请求 ≤200ms；harness P1–P13 无回归（该改动不触碰 UI 锁与重放路径，风险隔离）。

## 6. 里程碑与验收

| 里程碑 | 内容 | 预期收益 | 验收（预算断言） | 风险 / 回滚 |
|--------|------|----------|------------------|-------------|
| **M0** | 固化基线：跑一次全量 harness，留 evidence | — | 现状 10/14（已知 4 条 FAIL） | 无 |
| **M1** | L3 预热 + L2.1 deadline 顺序 —— **代码已落地，性能验收未完成** | 首请求 ≤200ms；截断不再空转、冻结次数下降 | **待复跑**：本机换页（14GB 机器仅剩 3.1GB 空闲）导致三次运行互相矛盾，见 `docs/e2e/resume-m1-ab-measurement.md`（含复跑配方）；期间 **P10 交付单调在 A/B 两次均 FAIL**，因环境噪声无法归因，安静机器复跑后再定 | 低；两条改动各自独立可回滚（L3 已有 `AICLI_SQLITE_WARMUP=0` 开关） |
| **M2** | L2.2 缓存/增量 —— **已完成**（`docs/e2e/resume-l2-2-layout-cache-measurement.md`）；L2.5 **经测量否决**（`docs/e2e/resume-l2-5-deferred-replan-measurement.md`），替代方案见 §4.5 重估 / §4.7 / **§4.8（建议优先）** | 热路径 **52.2 → 8.5 ms**、分配 **52.2 → 8.9 MB**（基准口径，目标 ≤20 ms 达成）；第二次规划（补齐较早页）实测 **602 ms**，其中布局仅 ~1%；另测得 `State()` 深拷贝地板（1,500 条目 = 180 ms） | 基准门禁 ✅（含新增 `BenchmarkDeferredOlderPageReplan` + 结构断言）；harness P12 仍**未取数**（环境阻塞） | 中；已由 oracle 单测（增量 == 全量切分）+ 15 包回归锁定 |
| **M3** | L1.0 普查 —— **已完成**（`docs/e2e/resume-replay-census.md`）+ L1.1 便宜解析 | **修正**：parse ≤0.5s 不可达 —— 可裁上限 ≈52% 行（Tier A 仅 6.9%）；现实目标 parse 3.0s → 1.4–1.6s。**`llm.request.finished` 经普查判定不可裁**（承载权威全文快照与排序闸状态） | P2 ≤3s、P9 ≤8s、P11 绿 | 中；裁剪白名单必须由普查驱动，未知类型一律不裁 |
| **M4** | L1.2 双日志分流 + 旧日志后台迁移 | R1 ≤0.5s（新会话） | P2 ≤1.5s（收紧预算）、P11 绿 | 中；写侧分流要保证 SSE/timeline 不丢事件（telemetry 文件仍在） |
| **M5**（可选） | L1.3 checkpoint | resume 成本 O(增量) | 新增预算断言（见 §7.3） | 高；仅在 M4 之后按需启动 |

## 7. 验收流程

### 7.1 命令

```pwsh
# 全量（构建 + 启动打点 + 恢复时间线 + UI 冻结取证）
pwsh -NoProfile -File scripts/test-aicli-resume-startup-perf-e2e.ps1

# 只量「卡住」（200ms 采样 /debug/chat/status，与 TUI 渲染帧共用同一把锁）
pwsh -NoProfile -File scripts/debug-aicli-resume-status-latency.ps1 -DurationSec 150

# goroutine 取证压缩（看谁占着锁）
python scripts/tmp-goroutine-map.py artifacts/aicli-resume-status-latency/<stamp>/goroutine-stall-2.txt aicli
```

### 7.2 现有断言 → 目标值

| 断言 | 当前 | M1 | M2 | M3 | M4 |
|------|------|----|----|----|----|
| P2 ready ≤ `-ReadyBudgetMs` | FAIL 5,421ms | 3,000ms | 3,000ms | **≤3,000ms** | **≤1,500ms**（收紧） |
| P9 首内容 ≤ `-FirstContentBudgetMs` | FAIL 11,222ms | 8,000ms | 8,000ms | **≤8,000ms** | ≤4,000ms（收紧） |
| P11 replayed ≤ `-ReplayRecordBudgetFactor`×cells | FAIL 61,447 / 26,876 | 不变 | 不变 | **≤4×** | ≤2×（收紧） |
| P12 单次冻结 ≤ `-UiStallWindowBudgetMs` / 累计 ≤ `-UiStallTotalBudgetMs` | FAIL 3,041ms | 下降 | **≤2,000ms** | 不变 | 不变 |

> **2026-09-25 修正（探针地板）**：P12 的探针端点 `/debug/chat/status` 在锁内深拷贝整个 history
> ledger（§4.8），因此 P12 的窗口时延包含**探针自身的克隆时间**，存在随 ledger 增长的非零地板。
> 判读 P12 时必须同时看 §7.3 的 P16 归因打点，否则会把探针开销误读成恢复开销。

### 7.3 建议新增断言（同一 harness，成本很低）

harness 已经在解析启动打点（`marks`）并计算每段 delta，新增两条只是加预算：

- **P14 `eventlog_parse` 预算**：`parse ≤ 1,000ms`（M3 后收紧到 500ms）；
- **P15 `eventlog_apply` 预算**：`apply ≤ 1,500ms`（M3 后收紧到 800ms）；
- **P16 单次规划预算**（需要打点）：在 `planEligibleHistoryCommitsWithin` 里给规划本体
  （screening + 铸 commit）加一次性耗时打点（仅 `AICLI_STARTUP_TIMING=1` 时输出），断言单次 ≤250ms。
  这是 P12 的**归因**断言：P12 说「用户被卡住」，P16 说「卡在规划」。
  **2026-09-25 修正**：原稿写的是「给 `LayoutTranscript` 加布局打点」，但 L2.5 实测显示布局只占
  第二次规划的 ~1%（2.5 ms vs 602 ms），布局打点会指向错误的方向；要归因的是规划器。
  单 cell 上万行的长尾场景仍需单独看布局（见 §4.4）。

### 7.4 反例 / 边界必须一起验

- 空日志 / 日志不存在（`publishReplayedScene` 空 Scene 路径，`:3339`）；
- 旧布局日志（`LegacyRuntimeEventsLogPaths`，`:3121-3129`）与「单文件无分流」的迁移路径；
- `AICLI_RESUME_WINDOWED_HISTORY=0`（一次性同步装载）下不回归；
- 单 cell 巨大会话（>1 万行命令输出）——L2.3 的目标场景；
- 注入记录（`Type==""`）与未知类型事件：**必须仍被完整重放**（L1 的保守规则）。

## 8. 风险与兼容性

| 风险 | 影响 | 缓解 |
|------|------|------|
| 裁剪白名单漏项 → resume 后 transcript 缺内容 | 高 | L1.0 普查驱动；未知类型不裁；新增单测：对「全类型样本日志」断言重放后 Scene 与实时路径等价（`chat_runtime_events_replay_test.go` 已有等价性基座） |
| 双日志分流后诊断能力下降 | 中 | telemetry 文件保留全量；`/debug/chat/display` 同时暴露两份计数 |
| 旧日志后台迁移写坏原文件 | 中 | 原文件保留 `.bak`；迁移失败只记日志；重放永远可回退到单文件路径 |
| 缓存/增量布局引入错行 | 中 | `history_resume_full_coverage_test.go`、`transcript_layout_cache_workset_test.go` 锁定语义；截断窗口的 whole-cell 跳过规则（`history_effect_planner.go:109-115`）不得破坏 |
| 预热 SQLite 与启动关键路径争抢 CPU | 低 | 预热放后台 goroutine，失败静默；打点观测 `capabilities_runtime_host` 段不恶化 |

## 9. 明确不做（本轮边界）

- **不改 scrollback 授权/重放语义**（销毁式重放、settle、epoch 记账）——这是正确性核心，性能方案不动它；
- **暂不做 L2.4（reduce 移出锁）**：深拷贝成本仍未完全证实（transcript 克隆 0.45 ms 已测，
  整状态含 `LayoutRows` 的克隆待测）；但 L2.5 实测已表明锁内大头是**规划**而非布局，
  若整状态克隆是毫秒级，L2.4 的优先级应高于 §4.7；
- **不动 `historyCommitPlanningBudget` 的 250ms 数值**（它是流式路径的节流阀，调整需要单独的流式回归）；
- **不为 P11/P12 放宽预算**：预算即验收线，只许收紧。

## 附录：锚点索引（2026-09-24 工作区）

| 符号 | 位置 |
|------|------|
| `replayEventLogWithLoadAuthorization` | `backend/cmd/aicli/commands/chat_runtime_events.go:3333` |
| 打点 `eventlog_read / parse / apply` | 同上 `:3356 / :3451 / :3496` |
| 解析循环 / apply 循环 | 同上 `:3376-3444 / :3457-3495` |
| 渲染数据面抑制白名单 | `backend/cmd/aicli/commands/chat_input_events.go:52-54` |
| 事件日志路径 | `backend/cmd/aicli/commands/chat_runtime_events.go:3102` |
| `continueTruncatedHistoryPlan` | `backend/cmd/aicli/ui/history_effect_planner.go:801` |
| `PlanIncomplete / PlanStalled` | `backend/cmd/aicli/ui/history_effect_queue.go:56 / :67` |
| `ContinueHistoryPlanAction` | `backend/cmd/aicli/ui/action.go:520` |
| 续跑触发点 | `backend/cmd/aicli/ui/controller_state.go:272 / 290 / 331 / 338 / 358` |
| `syncHistoryEffectsForTranscript(Within)` | `backend/cmd/aicli/ui/history_effect_planner.go:731 / :739` |
| deadline / `layoutRows` 顺序 | `backend/cmd/aicli/ui/history_effect_planner.go:78-84` |
| `layoutBudgetCheckRows` / 采样点 | `backend/cmd/aicli/ui/app_screen_layout.go:197 / :225-228` |
| `TranscriptState.LayoutRows` | `backend/cmd/aicli/ui/app_state.go:101` |
| `scene.LayoutTranscript` | `backend/cmd/aicli/ui/scene/layout.go:49` |
| split 缓存（容量/键/逐出） | `backend/cmd/aicli/ui/scene/layout_cache.go:31-34 / :79-90` |
| 持锁 reduce | `backend/cmd/aicli/ui/controller.go:563 / :569 / :578` |
| `State()` 读锁 | `backend/cmd/aicli/ui/controller.go:947` |
| 启动打点开关 | `backend/cmd/aicli/commands/chat_startup_timing.go:62-69` |
| 窗口化历史开关 | 同上 `:71-80` |
| harness（14 断言） | `scripts/test-aicli-resume-startup-perf-e2e.ps1`（预算参数 `:53-81`） |
