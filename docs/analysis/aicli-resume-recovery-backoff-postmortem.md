# aicli resume 恢复回退（recovery backoff）三层架构缺陷复盘

> 文档状态：事后复盘（post-mortem），记录一次线上问题的完整定位、修复与观测增强。
> 分析日期：2026-08-31
> 代码基线：`7a34f61`（`fix(ui): keep plain viewport flushes alive during success-mode recovery backoff`）、`8ae68e9`（`feat(ui): derived loop-health diagnosis + window counters + text summary on executor pprof endpoint`）
> 主要范围：`backend/cmd/aicli/ui/terminal_session_executor.go`、`backend/cmd/aicli/ui/executor_diag_export.go`、`backend/cmd/aicli/ui/terminal_session_executor_test.go`
> 验证命令：`go test ./cmd/aicli/ui/ -count=1`、`go build ./cmd/aicli/...`、`go vet ./cmd/aicli/ui/`（均通过）

## 1. 结论摘要

`aicli resume <session>` 在恢复历史回放时出现过两个连续症状，根因是终端会话 executor 恢复路径上的三个架构缺陷叠加：

1. **进度判据选错**：用 `Revision`（只要有动作就递增）当作"恢复是否在推进"的信号，导致恢复义务永不收敛，每周期重放 transcript，形成约 2 核 CPU 的 busy loop。
2. **成功与失败被当成同一回事**：为修复 busy loop 引入的 generation-based 持久 backoff 矫枉过正——backoff 生效后 executor 直接 yield 不再 flush 任何帧，而 prompt 输入渲染依赖 executor 的 flush，于是 resume 后用户输入区冻结。
3. **观测是"快照"而非"过程"**：pprof 端点只有单次读数计数，无法区分"死 guard（armed 很多但从不 engaged）"与"健康回退"，也没有"backoff 期间到底还在不在渲染"的探针，导致输入冻结长时间查不出来。

修复与增强已落地（`8ae68e9`、`7a34f61`），全部测试通过，工作树干净。

---

## 2. 现象与复现

- 复现命令：`aicli resume session_20260831075849_BEoIydwM --yolo --pprof --debug`
- 症状 A：进程 CPU 持续约 185%（约 2 个核），`resume` 后历史被一遍遍重放。
- 症状 B：修复 busy loop 后出现的新症状——resume 完成后 prompt 输入区冻结，用户敲键没有反应（输入渲染依赖 executor 的 flush，而 flush 被 backoff 抑制了）。
- 观测方式：通过 `--pprof` 暴露的 HTTP 诊断端点读取 executor 恢复诊断快照，不靠猜；用 `go test` 锁行为。

---

## 3. 三层架构背景

终端会话的执行链路是一条"语义 → 状态 → 物理"的三层管道：

| 层 | 角色 | 关键事实 |
| --- | --- | --- |
| 语义层 | transcript / 事件编码 | 历史事件的规范化全序 |
| 状态层 | `UIController` / `AppState` | 每帧应显示什么；`LayoutGeneration` 是几何/布局代次 |
| 物理投影层 | `TerminalSessionExecutor` → `TerminalSession` | 把一帧事务写到物理终端；唯一的物理 writer |

executor 的恢复路径围绕两个义务标志工作：

- `ProjectionUnknown`：物理终端当前状态无法证明与语义一致（例如写失败、无法确认）。
- `ReconciliationRequired`：历史/scrollback 需要与语义源对齐（例如 scrollback reset 后需要重放）。

`recoveryActionable` 的判定是 `!Lease.Active && !Frozen && (ProjectionUnknown || ReconciliationRequired)`。

当一次恢复尝试无法收敛时，executor 会 `armRecoveryBackoff` 进入持久 backoff，用 `scrollbackResetBackoff(stateGeneration)` 在后续周期抑制重复的恢复尝试。

---

## 4. 缺陷 1：进度判据选错（Revision ≠ 进展）

### 症状

transcript replay / streaming resume 时，每个 executor 周期会 post 约 240 次 `Revision` 递增，每次递增都会重新置位 `ProjectionUnknown` / `ReconciliationRequired`。早期 guard 用 `Revision` 判断"恢复是否在推进"。

### 根因

`Revision` 是"有动作"的信号，不是"有收敛"的信号。恢复义务被每周期重新置位，revision 永远在变 → guard 永远判定"还在推进" → 永不进入收敛 → 每周期重放历史 → ~2 核 busy loop。

### 教训

**真正的进展信号是 `LayoutGeneration`（几何/主题变化），`Revision` 只是"有动作"不代表"有收敛"。** 判定"恢复是否在推进"必须用布局代次，而不是任何计数器/动作号。

---

## 5. 缺陷 2：成功与失败被当成同一回事（本次核心缺陷）

### 症状

上一轮 generation-based 持久 backoff 修复了 busy loop，但矫枉过正：backoff 生效后，`runOne` 直接 yield、不再 flush 任何帧。prompt 输入渲染依赖 executor 的 flush，因此 resume 后每次唤醒都进 backoff 分支 → 输入区冻结。

### 根因

backoff 把两种完全不同的场景压成了同一个"抑制恢复"动作：

- **failed-mode**（`lastResetFailed=true`）：物理 writer 真的坏了。backoff 期间必须完全不碰 writer，否则会向坏 writer 反复写入。
- **success-mode**（`lastResetFailed=false`）：writer 是健康的，只是恢复义务在当前布局代次下不会收敛（`ProjectionUnknown`/`ReconciliationRequired` 被每周期重新置位）。此时正确的动作是：**抑制昂贵的 scrollback reset + 重放**（避免重新进入 busy loop），但**仍然执行普通视口 flush** 让 UI 存活。

### 关键区分

backoff 抑制的应当是"scrollback reset + 重放"这一昂贵动作，而不是"渲染本身"。把"不碰 writer"（failed）和"不重置 scrollback"（success）混为一谈，就是输入冻结的直接原因。

---

## 6. 缺陷 3：观测是"快照"而非"过程"，且缺关键维度

### 症状

输入冻结这类"进程活着但 UI 死了"的问题，长时间查不出来。

### 根因

之前的 pprof 诊断只有单次读数计数，存在两个盲区：

1. **无法区分"死 guard"与"健康回退"**：`armedBackoff` 增加但从不 `engaged`（死 guard，恢复义务永不收敛）vs 正常回退（engaged 且很快恢复），单靠累计计数看不出来。
2. **没有"backoff 期间到底还在不在渲染"的探针**：`backoffEngaged` 只说明"抑制发生了"，不说明"UI 是否还活着"。这正是输入冻结查不出来的直接原因——需要一个能证明"backoff 期间仍在 flush"的可观测信号。

### 教训

诊断计数器必须是**可证明过程的**，而不是只有存在性。一个布尔/计数若无法区分"健康"与"卡死"，就没有诊断价值；至少需要一个"loop 是否还在推进"的时间戳与一个"关键动作是否仍在执行"的探针。

---

## 7. 修复方案（commit `7a34f61`）

### 7.1 区分 backoff 的两种模式

新增 `scrollbackResetSuccessMode()`：返回 `!lastResetFailed`，即当前 engaged 的 backoff 是否来自"成功但未收敛"的恢复（writer 健康）。

### 7.2 success-mode 下保持视口 flush

`runOne()` 的 backoff 分支中，当 `scrollbackResetSuccessMode()` 为真时：

1. 读取 `terminalSessionSnapshot(0)`，若 `terminalSessionSnapshotRecoveryActionable(snapshot)` 成立；
2. 用 `composeTerminalViewportTransactionPlan` 构造**普通视口事务**（不做 scrollback reset）；
3. `e.session.FlushTransaction(plan)` 执行写入，`publishResult` 发布结果；
4. 记录一条 `FlushedWhileBackoff: true` 的恢复诊断条目，然后 `return false` 结束本周期。

效果：

- **prompt 输入渲染保持存活**（底部 surface 继续被刷新）；
- **昂贵的 scrollback reset + 重放仍被抑制**（不会重新进入 busy loop）；
- **义务可自然愈合**：`publishResult` 可能 post `HistoryProjectionRecovered`——当普通视口重绘证明投影已知时，恢复义务清除，guard 自然退出。

failed-mode（writer 损坏）保持原样：不碰 writer，直到有界窗口过期。

### 7.3 修复边界

- `scrollbackResetBackoff(stateGeneration)` 的进度 guard（按 `LayoutGeneration` 判定）保持不变。
- `armRecoveryBackoff(result, startGeneration)`、`recordScrollbackReset(epoch, stateGeneration, failed)` 语义不变。
- 新行为由 `TestTerminalSessionExecutorFlushesWhileSuccessBackoff` 锁定：success-mode backoff 期间必须执行 flush 且不执行 scrollback reset；failed-mode 仍不触碰 writer。

---

## 8. 观测增强（commit `8ae68e9`）

针对缺陷 3，executor 的 pprof 诊断端点（`ExecutorDiagSnapshot` / `executorDiagTextSummary`）新增：

| 维度 | 内容 |
| --- | --- |
| 派生诊断 | `executorDiagDiagnosis` 输出结论：`idle` / `healthy` / `backoff_engaged` / `dead_guard` / `unknown` |
| 窗口计数器 | generation advances / frame errors / scrollback resets / recoveries 的每秒（窗口）速率，配合 `GeneratedAtUnixMs` 计算 loop 是否仍在推进 |
| backoff 存活探针 | `FlushesWhileBackoff` 累计计数 + 末条 diag 的 `flushedWhileBackoff` 标志——**证明 backoff 期间渲染仍活着** |
| 文本摘要 | `?format=text` 输出人类可读摘要，含 `flushesWhileBackoff` / `lastFlushedWhileBackoff` 行 |

用法：连续两次轮询 `GeneratedAtUnixMs` 与计数器，若 `FlushesWhileBackoff` 在增长，说明 prompt 渲染在 backoff 下仍存活；若 `diagnosis=dead_guard` 且计数器不动，说明 loop 卡死。

---

## 9. 验证

- `go test ./cmd/aicli/ui/ -count=1`：全部通过（含新增 `TestTerminalSessionExecutorFlushesWhileSuccessBackoff`）。
- `go build ./cmd/aicli/...`：通过。
- `go vet ./cmd/aicli/ui/`：通过。
- 提交 `7a34f61`、`8ae68e9` 后工作树干净（`docs/analysis/` 下本文档为唯一新增）。

---

## 10. 经验教训与后续建议

### 经验教训

1. **判定"在推进"要用收敛信号，不是动作信号**：`Revision` 递增只说明"有动作"，`LayoutGeneration` 才说明"布局在变化"。恢复 guard 一律按布局代次判定。
2. **抑制动作 ≠ 抑制渲染**：backoff/限流的目标是"昂贵的恢复动作"，不是"UI 渲染"。任何抑制路径都必须自问：这条路径上的用户可见渲染是否还活着？
3. **诊断要有"过程证明"**：计数器 + 时间戳 + 派生结论，才能区分"健康回退"与"死 guard"；单布尔/单计数不足以支撑线上排障。
4. **修复要锁行为**：新测试必须覆盖两种 backoff 模式（success 与 failed）的行为差异，防止未来回归成"全抑制"或"全放开"。

### 后续建议

- 给 failed-mode 的恢复增加更强的告警/日志（writer 损坏是环境级故障，应快速暴露）。
- 考虑把 `FlushesWhileBackoff` 探针接入运行时状态摘要（`docs/architecture/aicli-chat-unified-renderer-architecture.md` 中描述的 render-output-gateway 侧），让非 pprof 途径也能观测。
- 对 `recoveryActionable` 的"每周期重新置位义务"行为做源头治理：若义务在同一布局代次内反复置位，应在置位路径上做一次判重，从根上减少无谓重放。
