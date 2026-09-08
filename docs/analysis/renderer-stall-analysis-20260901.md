# 会话主屏幕统一渲染器停止更新分析报告

> 文档状态：现场故障分析归档（原始分析报告，2026-09-01 自仓库根 `analysis/` 目录迁移整理至 `docs/analysis/`）
> 分析日期：2026-09-01
> 数据源：`/debug/chat/status` 两次轮询 + 进程 goroutine dump（原始 dump 已移除，未随文档归档）
> 主要范围：`backend/cmd/aicli/ui/controller_state.go`、`terminal_session_executor.go`、`terminal_session.go`、`history_effect_queue.go`
> 关联文档：
> - 修复方案与实施记录：`docs/plan/aicli-chat-unified-render-stall-analysis-and-hardening.md`
> - 统一渲染器架构总览：`docs/architecture/aicli-chat-unified-renderer-architecture.md`
> - `/debug/chat/status` 端点说明：`docs/aicli/debug-chat-status.md`
> - 同主题复盘（resume 恢复回退三层缺陷）：`docs/analysis/aicli-resume-recovery-backoff-postmortem.md`
> - Active Band 丢失问题分析：`docs/unified-renderer-band-analysis.md`

## 会话信息

- 会话 ID: `session_20260901183932_EuPk69XO`
- 分析时间: 2026-09-01 19:04 CST
- 数据源: `/debug/chat/status` 两次轮询（18:57:42 → 19:04:50，间隔约 7 分钟）

---

## 核心发现

**统一渲染器（主屏幕/scrollback 主体）的 history-commit 交付管线完全冻结**，但 **active band（底部视口/活动单元格）通过 plain viewport flush 持续更新**。以下是证据链。

---

## 关键数据对比（两次轮询，7 分钟间隔）

| 指标 | 18:57:42 | 19:04:50 | 变化 | 信号 |
|------|----------|----------|------|------|
| **encode_count** | 29,805 | 30,893 | **+1,088** ✅ | 编码器健康 |
| **scene.revision** | 29,480 | 30,417 | **+937** ✅ | scene 模型在更新 |
| **model_items** | 280 | 316 | **+36** ✅ | 模型项在增长 |
| **primary_committed** | 19,477 | 20,853 | **+1,376** | 输出在增长，但... |
| **flushes_while_backoff** | 16,684 | 18,060 | **+1,376** ⚠️ | 每次 recovery = 一次 plain flush |
| **handoffs_while_backoff** | 990 | 990 | **0** ❌ | **无 handoff！** |
| **acked** | 1,814 | 1,814 | **0** ❌ | **无 ack！** |
| **oldest_pending_token** | 2,137 | 2,137 | **0** ❌ | **队头冻结** |
| **pending_count** | 291 | 683 | **+392** ⚠️ | 新 token 持续入队但不交付 |
| **invalidated** | 88 | 109 | **+21** ⚠️ | 持续 invalidate |
| **layout_generation** | 2 | 2 | **0** ❌ | **冻结** |
| **ReconciliationRequired** | true | true | 无变化 | 从未收敛 |
| **event_log.failures** | 29,805 | 30,892 | +1,087 | 每次 encode 都失败 |
| **event_log.recorded** | 0 | 0 | 0 | 从未写入成功 |

---

## 根因分析

### 0. 为什么是"运行一段时间后"才发生（时间触发机制）

这不是随机故障，而是流式会话运行到一定阶段后必然触发的状态转移：

**前置积累（正常运行期）：**
- 会话中的 assistant/reasoning/tool 内容以 **mutable cell（流式进行中）** 形式渲染（active band 路径）；
- 在 cell 尚未 finalize 时，其**稳定前缀就已经被 ACK 进 native scrollback**（`acked=1,814` 就是这段积累期）；
- 会话越长、流式单元越多、ACK 前缀越长。

**触发事件（第一次"修正已 ACK 内容"的 finalize）：**

`controller_state.go:380-383` — `FinalizeActiveCellAction` 到达时，
`finalizedActiveCorrectionTouchesAckedPrefix`（557-569 行）检测到 **finalized
快照的前缀与已经 ACK 进 scrollback 的内容不一致**：

```go
end := active.Acked.End
return end > len(active.Source) || end > len(cell.Source) || active.Source[:end] != cell.Source[:end]
```

即：流式 delta 最终合并/折叠（reasoning 折叠、markdown 重渲染、chunk 边界、
工具链重排）的结果，与"边流边 ACK"的旧内容不同 → 已写入 scrollback 的字节
被"修正"了。同理 `ReplaceTranscriptAction` 的
`transcriptReplacementInvalidatesAckedHistory`（577 行起：插入/删除/重排/修正
越过已确认字节）也会置位。触发即：

```go
state.HistoryEffects.ProjectionUnknown = true
state.HistoryEffects.ReconciliationRequired = true
```

**为什么需要时间：** 边流边 ACK 的前缀必须足够长，才可能在 finalize 时出现
与旧内容的分歧；这是概率性事件，会话运行越久、流式单元越多越容易发生。一旦
第一次发生且此后没有 Resize/Theme 事件，死锁永久锁定。

**首次修复尝试失败：** executor 执行了唯一一次 scrollback reset
（`scrollback_reset_count=1, reason="reconciliation", terminal_epoch=1`），但
reset 后的 replay 重新计划 transcript（`syncHistoryEffectsForTranscript`），
源头的修正内容依旧存在，obligation 被再次置位 → **reset 不收敛** →
`armRecoveryBackoff` 武装一次（`armed_backoff=1`）。

**永久冻结：** `layout_generation` 冻结在 2 → success-mode backoff 永久抑制
reset → `HistoryScrollbackReconciled`（需要新 epoch 才清除
`ReconciliationRequired`）永不发出 → 死锁。

### 1. 死锁条件：fail-closed 屏障 × backoff guard

```
 hasUnresolvedTerminalDelivery()
          │
          ▼  block ordered history delivery
 ┌─────────────────────────────────┐
 │   ReconciliationRequired=true   │ ← 只能由 scrollback reset 清除
 │   (reconcileScrollback)         │
 └──────────┬──────────────────────┘
            │  execution needed
            ▼
 ┌─────────────────────────────────┐
 │  scrollbackResetBackoff()      │
 │  engaged (success mode)        │  ← layout_generation 未前进(始终=2)
 │  → 抑制 reset+replay          │
 └──────────┬──────────────────────┘
            │  deadlock!
            ▼
  plain viewport flush only (active band)
```

**触发链：**

1. 某次 in-flight history commit 被 invalidated 且 `MayHavePartiallyWritten=true`（状态历史：invalidated 88→109，持续增长）

2. `history_effect_queue.go:296` → `markDeliveredBatchUnresolved` → `ReconciliationRequired=true`，`ledger.unresolvedCount++`

3. `hasUnresolvedTerminalDelivery()` 开始阻止有序交付（`ackBatch` 返回 `ErrHistoryCommitRecoveryPending`）

4. 恢复义务（`ReconciliationRequired=true`）使 `recoveryActionable=true`，executor 每次醒来都尝试恢复

5. `scrollbackResetBackoff()` 在 **success mode** 下抑制 scrollback reset：
   - 条件：`lastResetFailed=false`（writer 健康）**且** `layout_generation == lastResetGeneration`（未前进）
   - 上一次 scrollback reset（`scrollback_reset_count=1`，reason="reconciliation"）后，generation 冻结在 2
   - 只有 `Resize`/`SetThemeContextAction` 才会推进 `layout_generation`，streaming 不会

6. 在 backoff success mode 下，executor 尝试 handoff pending token：
   - 但 `schedule.pendingToken` 受 `hasUnresolvedTerminalDelivery()` 影响而不可用（`terminalSessionSchedule` 在 `ledger.orderedTokens()` 中找到了 token，但 `terminalSessionClaimedBatchLocked` 在 claim 时验证 `entry.State == HistoryCommitInFlight`，而 `BeginHistoryCommit` 没成功发出，因为 `hasUnresolvedTerminalDelivery` 的屏障使 `pendingToken` 在 schedule 中不可暴露）

7. 降级为 **plain viewport flush**（`composeTerminalViewportTransactionPlan(appState, nil)`）：
   - 只重绘底部视口（prompt + active cell）
   - 不交付出任何 history token
   - 不 reset scrollback
   - 不 ack 任何 token

8. 结果：executor 每 ~500ms 醒来一次，跑一次 plain flush，回到睡眠。**无限循环，直至 layout generation 发生变化。**

### 2. 为什么 active band 仍然更新

代码注释明确说明（`terminal_session_executor.go` 注释）：

> "Suppressing the reset must NOT suppress the handoff of new content: the active band commits new messages as pending history tokens."
> "A plain viewport transaction... keeps prompt rendering live while still suppressing the expensive reset+replay."

`FlushesWhileBackoff = 16,684 → 18,060` 证实了 active band 在每个 backoff cycle 都通过 `composeTerminalViewportTransactionPlan` + `e.session.FlushTransaction(plan)` 持续刷新。这就是用户看到的 `activeband` 更新的来源。

### 3. 为什么 primary_committed 也在增长（但这是假象）

`primary_committed` 从 19,477 → 20,853（+1,376），但增量完全等于 `flushes_while_backoff` 增量（+1,376）和 `total_recoveries` 增量（+1,376）。每个 plain flush 产生一个 primary commit，这些 commit 是底部视口（active band）的重绘，不是 transcript scrollback 的内容交付。

### 4. event_log 写入全部失败（独立问题）

`recorded=0, failures=30,892`，因为 `.events` 目录不存在（`Test-Path` 返回 False）。`appendEventLogLine` 调用 `os.OpenFile(path, os.O_CREATE|...)` 但父目录从未被创建，导致每次 OpenFile 都返回错误。这是 best-effort 路径，失败只计数不阻塞，**不是渲染器停摆的原因**，但会导致：
- 会话无法持久化事件日志
- 重启后 `replayEventLog()` 返回 0，无法恢复画布
- 影响 resume/replay 功能

---

## 结论

| 现象 | 原因 |
|------|------|
| 主屏幕 scrollback 停止更新 | `ReconciliationRequired=true` 无法收敛，executor 在 backoff 中抑制 scrollback reset，history-commit 管线彻底冻结 |
| active band 持续更新 | backoff 的 plain flush 路径专门保持底部视口（prompt + active cell）live |
| 硬件：layout_generation=2 冻结 | 只有 Resize/ThemeChange 推进 generation，streaming 不会 |
| 软件：`hasUnresolvedTerminalDelivery()` 屏障 | 前一次 in-flight invalidate 留下 partial-write 记录，阻止有序历史交付 |
| 附加：event_log 全部失败(30,892 次) | `.events` 目录从未创建，但不阻塞渲染 |

**本质：fail-closed 屏障（partial write 保护）与 backoff guard（scrollback reset 频率限制）在 layout_generation 冻结时形成死锁。** 这是设计层面的交互预期——backoff 设计假设 layout_generation 最终会推进，但当用户只做 prompt/streaming 交互而不调整终端尺寸时，generation 永不推进，死锁成为永久状态。

---

## 修复建议

### 短期（解救当前会话）
- 调整终端尺寸（`Resize` 会推进 `layout_generation`，从而解除 backoff）
- 或执行 `/theme` 切换主题（`SetThemeContextAction` 也会推进 generation）

### 中期（代码修复）
- **【已实施】** 在 `terminal_session_executor.go` 中给 success-mode backoff 增加
  **时间窗解除 + 重试预算**（方案 2 + 3 合并）：
  - `terminalScrollbackResetRetryWindow = 2s`：距上次 reset 超过窗口即允许
    一次同 generation 的重试，打破"generation 冻结 → 永久停车"死锁；
  - `terminalScrollbackResetMaxRetries = 3`：同一 generation 下连续
    non-converging success-mode reset 最多 3 次，预算耗尽后停车直到真正的
    geometry/theme 变化，防止永不收敛的 reset+replay 忙循环；
  - 窗口必须明显大于一个恢复周期（WaitIdle 排干 replay 约 500ms）：最初的
    100ms 窗口在下次调度读之前就过期，guard 永不生效（实测 439 次 armed /
    0 次 engage 的失败模式）。
  - 回归测试：`TestTerminalSessionExecutorScrollbackResetBackoffIsGenerationBased`
    （双因素语义锁定）+ `TestTerminalSessionExecutorReconciliationRetryAfterDeadlock`
    （端到端复现死锁并验证收敛）。

### 长期
- 用 `layout_generation` + `seconds_since_last_reset` 双因素解除 backoff guard，
  而不是仅依赖 generation 改变（本次修复即落地此方向，剩余工作是调优窗口/预算参数）
- 考虑在 `ReconciliationRequired` 持续超过某个阈值时自动生成一个 `HistoryScrollbackReconciled` 事件

---

*本次分析基于 `/debug/chat/status` 两次轮询数据、goroutine dump、以及 render engine/executor 源码阅读。*