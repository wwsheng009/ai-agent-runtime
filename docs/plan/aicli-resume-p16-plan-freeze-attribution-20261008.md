# P16 规划冻结归因（2026-10-08，resume 大会话）

> 对象：`test-aicli-resume-startup-perf-e2e.ps1` 的 P16（跨帧最大单次 transcript 规划耗时
> ≤ 250ms）在大会话（4225 cell / 86,864 行）恢复时实测 1.34–1.77s，是性能门禁唯一红项。
> **状态：已闭合（2026-10-08，`d60c0165`，220ms/250ms，见 §5）。**
> 本文是实测归因（非推断），供 P3 后续切片（S4+ / 规划器）实施参考。

## 0. TL;DR

冻结 = **收尾全量替换后，规划器在 UI actor 锁内一次性完成「全部 cell 的首次布局」**
（4225 cell / 86,864 行，1.37s），其中 **55% 是 diff 单元格渲染（`diff.SupplementDocument`
0.75s）**。缓存层健康（cell_rows 0 逐出、34.7MB < 64MB 上限），fence 折叠仅 0.09s——
**这不是缓存退化，而是「单遍规划把首次渲染全部压在锁内」的设计成本**
（P1-1 Stage 2「无预算单遍」）。压到 250ms 预算内需要架构级改动（见 §4），
没有安全的小修路径。

## 1. 现场证据

### 1.1 持锁者（goroutine-stall-1.txt，卡顿窗口 3671–5869ms，冻结 1422ms）

```
ui.transcriptFenceFoldString                 history_effect_planner.go:697
ui.transcriptFinalizedPrefixFence            history_effect_planner.go:686
ui.transcriptPlanMemoHit                     history_effect_planner.go:749
ui.syncHistoryEffectsForTranscript           history_effect_planner.go:612
ui.reduceUIControllerState                   controller_state.go:323
ui.(*UIController).Run                       controller.go:496        ← 持 UIController.mu
```

### 1.2 CPU profile（perf4，8s 窗口，`-focus cmd/aicli/ui`）

| 节点 | cum | 说明 |
|---|---|---|
| `syncHistoryEffectsForTranscript` | 1.66s | 锁内总成本 |
| ├ `planEligibleHistoryCommitsWithin` | 1.45s | 规划本体 |
| │ ├ `screenTranscriptPlanWindow` | 1.37s | 全量布局（screening） |
| │ │ └ `layoutTranscriptScreenRowsImpl` | 1.37s | |
| │ │   ├ `structuredTranscriptScreenRows` | 1.09s | |
| │ │   │ └ **`diff.RenderText` → `diff.SupplementDocument`** | **0.75s** | diff 单元格渲染 |
| │ │   │ └ `renderengine.RenderCache.Render` | 0.16s | |
| │ │   │ └ `reasoningScreenRows` | 0.14s | |
| │ │   └ `foldedToolChainScreenRows` + `toolFoldTargetRows` | 0.26s | |
| ├ `applyTranscriptPlan` | 0.11s | |
| └ `transcriptPlanMemoHit`（fence 折叠） | 0.10s | 其中 `transcriptFenceFoldString` 0.09s |

### 1.3 布局缓存统计（全量 status `app_state.layout_cache`，同一轮运行结束快照）

```
cell_rows: hits=2155 misses=4201 evictions=0 entries=4201 bytes=34.75MB hit_rate=0.34
plan:      hits=111  misses=3261 evictions=0 entries=244  bytes=0.55MB
fold_omit: hits=1058 misses=1959 evictions=0
```

**结论：容量充足、零逐出；4201 miss ≈ 全部 cell 的首次渲染**（不是逐出抖动，
不是键失效风暴）。`plan_count=8~9` 说明 memo 未命中发生了 8~9 次，末次（全量替换）
覆盖全部 cell，即 1.37s 的那次。

## 2. 已排除项

- **缓存容量/逐出**：0 evictions，字节水位 34.7MB < 64MB（`cellRowsCacheMaxBytes`）。
- **fence 折叠（memo 检查）**：0.09–0.10s/次，量级无关。
- **recovery 迭代间隔**：P1-1/P2 后交付事件驱动，迭代间隔不再是进度信号（P4 已改口径）。
- **事件日志重放**：`replayed=0`，不在装载路径上。

## 3. 机理

收尾「授权式全量替换」把 transcript 一次性换成全量（4225 cell）。替换后的第一次
`syncHistoryEffectsForTranscript`：

1. `transcriptPlanMemoHit` 未命中（fence/身份变化）→ 进入单遍规划；
2. `planEligibleHistoryCommitsWithin` → `screenTranscriptPlanWindow` 对**全部** cell
   做布局投影；每个 cell 首次渲染（miss）→ `structuredTranscriptScreenRows` →
   diff 单元格走 `diff.RenderText`（解析 unified/supplement + 语法高亮预算）；
3. `mintTranscriptPlan` 铸出 86,864 行的交付提交，交给执行器在随后 ~10s 内写入。

步骤 2 的首次渲染量是 O(全量历史)，全部发生在 `UIController.mu` 内（reduce 同步执行），
因此端点/键盘/帧泵被冻结 1.37s。

## 4. 修复路径（按性价比排序，均属 P3 后续切片）

1. **流式/增量 plan（推荐）**：把「一次铸全量提交」改为「按页/按块铸提交」，与执行器
   交付窗口（实测 ~10s）摊还；单次锁内规划量降为 O(页)，P16 自然达标。
   注意：与 P1-1 Stage 2「无预算单遍」的取舍需要在设计层裁决（截断语义曾引发正确性
   问题，增量必须保持「提交=完整 cell 前缀」不变量）。
2. **off-lock 规划**：把布局/铸提交移出 actor 临界区（快照输入 → 后台计算 → 原子应用），
   需要重新论证与帧一致性/ledger 对账的交互（P1-1 曾否决，需新证据）。
3. **diff 渲染提速**：0.75s 集中在 `diff.SupplementDocument`（大 diff + 高亮预算）。
   可评估按 (source,width,theme) 的内容寻址缓存（对首次渲染无效，但对多轮 pass 有效——
   当前已由 cellRowsCache 覆盖），或高亮预算/解析优化。
4. **（不建议）调大 P16 预算**：1.37s 对 4225 cell 会话是一次性成本，但门禁的意义是
   防止回归到「每 delta O(全量)」；调预算会掩盖真实的锁内长临界区。

## 5. 现状（2026-10-08 更新：首渲染并行化已落地）

**已实施修复（同会话 e2e A/B）**：布局投影改为两阶段——Phase 1 顺序扫描出段表
（gap/缓存命中/待渲染任务），Phase 2 用 worker 池并行渲染 miss（纯函数，共享缓存
自带锁，门限 64，默认 GOMAXPROCS），Phase 3 顺序装配（输出与顺序渲染逐行一致，
600-cell 等价性测试 + `-race` 钉住）。

| 读数 | 修复前 | 修复后 |
|---|---|---|
| P12 冻结窗口 | 1 个 / 1422ms | **0 个**（另一轮为启动探针时序伪影 1062ms，dump 显示 actor 空闲，非锁持有） |
| P16 规划 max | 1377ms | **538ms**（8 worker 时 645ms） |
| 收敛（全量交付） | 9.9–10.4s | 9.1–9.4s |
| 全量布局（锁内） | 1.37s | 0.54s cum（串行段：fence 折叠 + foldTarget + mint + reconcile） |

**已闭合（2026-10-08，`d60c0165`）**：把收尾全量替换后的首次渲染**按块摊进装载窗口**
——`warmTranscriptLayoutChunk` 只填 cell 行缓存（单次锁内 ≤ 512 cell，~25–60ms），
游标取「从尾部向前已预热的布局行数」（跨页复用 CellID 时行锚点与 ID 无关，生产实测
warm-rows=70733 全覆盖），装载期自投递续块、收尾前先补齐缺口再续跑规划。
**P16 规划 max 549ms → 220ms（≤250ms 预算达标）**；权威运行
`artifacts/aicli-resume-startup-perf-e2e/20261008-114244`：**P16 PASS（max=220ms）、
P12 PASS（0ms 冻结）、P1–P16 全 PASS**。§4 的「流式/增量 plan」与串行段压缩仍可
作为后续架构简化选项，但不再是门禁阻塞项。

**分相位读数（2026-10-08 晚，新增 `plan-cells/plan-screen-ms/plan-mint-ms/plan-apply-ms`
进 status，harness 可直接读）**：最长一次规划（4224 cells，509–536ms）的拆分：

| 相位 | 耗时 | 构成 |
|---|---|---|
| screen | **324–360ms** | 快照 + 全量布局（含并行首渲染墙钟；渲染 CPU 实测 3.1s/16 worker，其中 `diff.Document` 1.70s、`foldedToolChainScreenRows` 0.69s、RenderCache 0.26s；串行装配仅 ~50ms） |
| mint | 32–37ms | 铸 commit（86,863 行） |
| apply | 138–151ms | membership reconcile + 入队（86k ledger 遍历 + ~3.5k 新候选） |

读法：最长一次规划的成本主体是**新增 ~3500 cell 的首次渲染**（screen），不是重复
screening/铸提交。因此「增量 screening」只能省已渲染 cell 的重复部分；把首次渲染
提前到装载期（逐页补齐中间步落地即预热 cell 行缓存）早期只到 536→509ms——中间安装
仍被合并，收益小于预期。**最终由 `d60c0165` 的「装载期分块预热 + 尾部行锚点游标 +
收尾前补齐缺口」达成 220ms**（不依赖流式铸提交），见上。

- 门禁：P16 已闭合（220ms/250ms，`d60c0165`）；P12 已绿（用户可见冻结消除）。
- 一次性 vs 稳态：稳态流式（P3-S1/S2/S3 后）已 O(delta)；本条是装载末次单遍的成本。
- 测量入口：`-CpuProfileSeconds` + `app_state.layout_cache` + goroutine-stall dump，
  三件套已可直接复现本文全部数字。
