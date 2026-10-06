# P2 特例族侦察归档（2026-10-06）

> 目的：为 P2「历史线性化」切片定序提供执行图（file:line 以当日工作区为准）。
> 来源：只读侦察批 `batch_2aaf16fd48ac9da9`（a2-replay / a2-topalign 完成；a2-archive 被只读 shell 策略误杀，已重派）。
> 目标语义（审计 §6 P2）：可见窗口 W 行 + 溢出按行序 append 进 native scrollback；任何 mutable 内容
> 不得提前入 scrollback（等 finalized 一次性写）；resize 只重画窗口；不承诺 scrollback 可改写。

## 1. scrollback replay / settle-unresolved / reconcile（a2-replay）

### 结论
- 该族 = 两条恢复路径：**armed 销毁式重放**（`ArmScrollbackReplay` 授权族，**可删**）与
  **非销毁 settle**（append-only 兼容路径，**就是目标语义，整体保留**）。
- `TerminalEpoch` **保留（语义降级）**：从"物理替换代"降为"语义装载代"，继续作 stale-callback 栅栏。
- `HistoryCommitAcknowledged` 不是终端事务回执，是"字节已交给 PTY"的本地完成证明（顺序 + 去重）
  → 保留为本地写成功水位（`hasTerminalRecordForSource` 依赖）。

### 状态机主链（file:line）
- 授权：`ReplaceTranscriptAction{ArmScrollbackReplay}` → controller_state.go:487-497（arm + Required + memo 失效）；
  no-op 安装分支 :503-517（防"清屏后无内容可写"空屏）。
- 选计划：terminal_session_snapshot.go:43-97 → terminal_session_executor.go:1170-1177
  （armed ⇒ `composeTerminalViewportScrollbackReconciliationPlan`（resetScrollback=true）；未 armed ⇒ SettleHistoryProjection）。
- 物理：terminal_session.go:898-904 / :978-980（`\x1b[3J`）/ :1126-1137；回执 executor:1242-1250。
- 对账：controller_state.go:441-459（门：epoch!=0 && !Lease && !Frozen && !ProjectionUnknown）→
  `reconcileScrollback`（queue:839-848：epoch 单调、整体换 ledger、NextToken 单调、清 Required）。
- 延迟消费：`HistoryProjectionRecovered` controller_state.go:391-411。
- settle：queue:818-826 + history_commit.go:512-533（unresolved → Quarantined/Settled，清计数）；
  terminal_session.go:1138-1146（保留 tail 锚点：未证明区间原地隔离、永不重发、从最后已证明行续写）。

### 可删性表（摘要）
| 对象 | 评估 | 替代 |
|---|---|---|
| `ArmScrollbackReplay` / `armScrollbackReplay` / `clearScrollbackReplayAuthorization` / `ScrollbackReplayArmed` | 可删 | 装载代 loadEpoch；reducer 内联重建 ledger |
| `resetScrollback` + 两个 composer + debug composer | 可删 | `SettleHistoryProjection` 成为唯一恢复计划 |
| `HistoryScrollbackReconciled` + reducer 分支 | 可删 | epoch 推进并入 `ReplaceTranscriptAction` 归约 |
| `ProvenScrollbackEpoch` / `recordProvenScrollbackReplacement` | 可删 | 无物理不可逆 act 后不存在"已发生未锚定" |
| `reconcileScrollback` | 需替代 | `beginSemanticEpoch`（epoch++、换 ledger、清 Required，reducer 内同步） |
| `TerminalEpoch` | 保留（语义化） | stale-callback 栅栏（token+epoch 双重） |
| `ProjectionUnknown` | 保留 | 帧证明有效性，与 scrollback 无关 |
| `ReconciliationRequired` | 保留（可合并） | 未证明交付恢复触发；与 Unknown 清除时机不同（两段式） |
| `settleUnresolvedWithoutReplay` 全链 | 保留 | append-only 目标策略 |
| executor success-mode backoff | 可删/简化 | 无 reset 后无"成功不收敛"；保留 generation 级失败限速 |
| `ScrollbackResetCount` / `LastScrollbackResetReason` / `\x1b[3J` 写路径 | 可删 | 负断言：全交互路径 3J 计数恒 0 |

### 切片（建议顺序）
- **S1** 生产可达销毁计划 → 恒 settle；`ArmScrollbackReplay` 语义改 loadEpoch（保留字段与 debug composer，编译面最小）。
- **S2** 删 `ProvenScrollbackEpoch` / `HistoryScrollbackReconciled` 分支；epoch 推进内联进 `ReplaceTranscriptAction`。
- **S3** 删 success-mode 背压 + `ScrollbackReset` 结果/计数/3J 写路径；保留失败限速。
- **S4** 诊断字段 / debug composer / production guard / 唤醒列表清理。
- **S5** 目标语义用例：追加交付不重发、settle 后从最后已证明行续写无空洞、语义代后旧 token 不可复活、3J 恒 0。

### 风险
- `/resume` 与 `/backtrack` 行为变化：不再清屏重放；backtrack 改历史时 append-only 无法物理回滚 →
  语义必须定义为"只追加修正"（需产品决策）。
- `TerminalEpoch` 误删 → stale ack 复活（history_effect_queue_test.go:620 钉住）。
- backoff 简化过度 → busy loop 回归（executor:149-170 记录的 439 arms/0 engages、~2 核事故）。
- ledger 整体替换必须与语义代推进原子完成，否则"记录在、内容不在"的空屏反向变体。

## 2. sticky top-align / resident tail / reset backoff（a2-topalign）

### 插入锚定规则全表（12 场景）
| # | 场景 | 判据 | 锚定结果 | file:line |
|---|---|---|---|---|
| 1 | finalized 插入未溢出 | `len(resident)>0 && !topAligned` | 底锚：DECSTBM + `CSI M` 删 headroom，写 `capacity-fill+1` 起 | terminal_session.go:1673-1684 |
| 2 | finalized 插入溢出 | `fill<len(inserted)` | 余量 HandoffPlan LF → scrollback；`topAligned=true` | :1687-1690 |
| 3 | 已 topAligned 插入 | `topAligned` | 顶锚续接：写 `len(resident)+1` 起，下方留白 | :1678-1680 |
| 4 | resident 空 + stream tail 非空 | `!topAligned && resident空 && tail非空` | 强制 topAligned 顶锚续接（0d5ecda6 启发式） | :1360-1371（调用 :1031-1035/:1310-1314） |
| 5 | active 归档（整批 Active） | `historyBatchIsActiveOrigin` | 分块 LF 滚入 scrollback；重画锚依 topAligned | :1022-1029/:1302-1308/:1732-1787 |
| 6 | 容量收缩 | `newCapacity<oldCapacity` | topAligned: overflow HandoffPlan；底锚: DL 空白 + HandoffPlan | :1497-1556 |
| 7 | 容量扩张 | `newCapacity>oldCapacity` | 清转移行 + IL 推 resident 到底部 | :1541-1555 |
| 8 | resize（宽高变化） | `resizeRebuild` | 跳过 transition，band clear，本事务禁 history 写入 | :972-982/:1846-1860 |
| 9 | 显式 scrollback reset | `plan.resetScrollback` | `\x1b[r\x1b[0m\x1b[H\x1b[2J\x1b[3J`；topAligned=false、tails/cells 清 | :978-980/:1126-1137/:1828-1830 |
| 10 | 初始化投影 | `initializeHistoryProjection` | 清 history region；topAligned=false | :983-985 |
| 11 | settle | `SettleHistoryProjection` | 不写字节；tails/锚保留 | :1138-1146 |
| 12 | 半写/失败 | `MayHavePartiallyWritten` | stream tail/resident/cells/topAligned/projectionKnown 整族复位 | :1073-1093/:1326-1335/:562-573/:648-656 |

### 目标语义删除/改写点
1. `terminalHistoryInsertionANSI` 底锚分支（:1665-1684）→ 统一「resident 之后按序写，写满 LF 溢出」（**主删除点**）。
2. `historyTopAligned` + `historyInsertionContinuesScrollback`（:358-364/:1360-1371）→ 统一 append 后无必要。
3. `terminalActiveHistoryArchiveANSI` 底锚分支（:1769-1786）→ active/finalized 物理写路径合并（planner Origin 保留给 ledger ack）。
4. `terminalViewportTransitionANSI`（:1510-1555）→ 删 DL/IL 补偿；收缩溢出用 stream tail 重推。
5. **保留**：`terminalHistoryPayloadToWrite` dedup（:1605-1630）、stream tail/cells 证明、半写整族复位。
6. 测试改写：stream_tail 第 1/4 步与 active_archive 的 bottom-anchor 断言**同切片重写**；
   shrink/grow 与 alt 往返改"窗口内按序"断言。

### reset backoff
- 存在理由：reset→replay-all→fail→reset 无界循环（~2 核事故）；常量 executor:137-169
  （100ms failed 窗口 / 10ms yield / 2s retry 窗口 / 3 次预算）。
- 可删性：**不能裸删**。先删 success-mode 预算（保留窗口 + failed 限速），观察 diag
  （ArmedBackoff/BackoffEngaged）后评估整 guard。

### 切片
- **S1**（低风险）统一 `terminalHistoryInsertionANSI` 为按序 append + 同步改断言。
- **S2** 删 `historyInsertionContinuesScrollback` 与 topAligned 插入侧；archive 折叠为同一 writer。
- **S3** transition 收敛为 window-only；native_scrollback_regression 全组回归。
- **S4**（最高爆炸半径）backoff：先删 success-mode 预算，再评估整 guard。

### 风险
- 删 topAligned 会同时破坏 transition/archive 的 repaint 锚与两个测试 → 必须同切片重写。
- 不得重新引入 DECSTBM/DECSC（会 reflow 已交付行，:1832-1846 注释）。
- active/finalized 合并触及 ledger ack 证明（Active 条目不剪除、ack 需渲染行）与 planner `ActiveBandRows`
  → 跨模块一致性风险。

## 3. active 溢出归档（a2-archive，报告已在策略误杀前完成）

### 结论（关键）
**只做「停止铸 active 提交」即可达成目标语义**：`activeAckedRenderedPrefixRows` 依据 ledger 中
Delivered 的 Active-origin 条目计算 skipRows；没有 active 条目时 skipRows 恒 0，finalize 自然从
source 0 一次铸全量 —— 无需先重构 skipRows 管线。

### 调用链（摘要，file:line）
- 流式入口：`UpdateActiveCellAction` → `syncHistoryEffectsForActiveCell`（controller_state.go:568-583）→
  `planMutableActiveCellHistoryCommitsWithTheme`（planner:398-501；门槛 :399-403；plain 溢出 :413-499；
  markdown/reasoning :404-412/:503-585）→ `syncHistoryEffectCandidates` → `enqueueHistoryCandidates` →
  `advanceActiveCellEnqueuedFromEffects`（:1580-1632）。
- 全会话路径同样重铸：`mintTranscriptPlanWindow` :185-190（frontierActive ← `canonicalHistoryCommitFrontier` :315-329）。
- 交付：executor Pending → markInFlight → FlushTransaction → `commitHistoryRowsLocked`
  （terminal_session.go:1298；单条 :1302-1308 / 批量 :1021-1040）→ `terminalActiveHistoryArchiveANSI`
  （:1732-1780：逐 chunk 涂顶 + 空 HandoffPlan 滚入 scrollback；**不**追加 historyTailRows :1345-1347）。
- 续接：`historyInsertionContinuesScrollback`（:1360-1371；调用 :1031/:1311）。
- ack 推进：`advanceActiveCellLedgerOnAck`（controller_state.go:1045-1075）、`noteFinalizedActiveAck`（:1086-1097）。
- finalize：`FinalizeActiveCellAction`（:589-606）→ 全量规划 + `activeAckedRenderedPrefixRows` 反推 skipRows（planner:335-367）。
- 装配层（commands/）对 Origin 无分支：删除无需改装配，只需同步测试 fixture。

### 测试清单（要点）
- 归档直测 4 个：`terminal_session_active_archive_test.go` :16/:63/:106/:181（改为"mutable 期间无历史写入 + finalize 恰好一次"）。
- resident tail / finalize：`history_effect_planner_test.go` :349/:414；`native_scrollback_streaming_test.go:11`；
  `terminal_session_executor_test.go:169`；`terminal_session_stream_tail_test.go` :21/:124/:162；
  `mutable_active_handoff_regression_test.go:74`。
- BOUNDARY-FINAL：`commands/chat_runtime_events_terminal_projection_test.go` :569/:691（"恰好一次/相邻"断言保留；
  :780 的 "split acked prefix" 与 :349 的 skipRows 预期改为"finalize 从 0 铸全量"）。
- 保留：`history_planning_budget_test.go` 3 个；`fixed_bottom_surface_test.go` :1632/:1653。

### 风险
1. **中断丢行（最大语义变化）**：现状契约"band 之上的行必须已过物理 writer"；删除后未 finalize 的流式前缀
   只存在内存与 band 尾部，进程被杀即不在 scrollback。需显式决策：接受（mutable 未 finalize 即非持久）
   或补 abort 时 finalize/flush 路径。
2. finalize 一次性写的失败面：部分写失败 → ProjectionUnknown + 重放，必须能从 0 重写且不双写（最需压测）。
3. `PlanIncomplete` / `hasClaimedActiveOriginDelivery` 门只保护 active 批；删除后需确认 transcript 批在飞时
   finalize 仍有等价护栏。
4. resume：ledger 纯内存，删除后重放只剩 transcript-origin，"归档行不属于 resident 模型"特例消失。

### 切片（两刀制，a2-archive2 精化版；均可独立回滚）
- **第一刀（行为反转，3 源文件 + 测试）**：
  1. 规划停铸：planner:188-190/:237 删 active append；`syncHistoryEffectsForActiveCell` 先空转
     （立即 return，最小 diff），调用点不动。
  2. 交付分支统一：terminal_session.go:1302-1308 与 :1021-1041 的 active 分支并入 insertion；
     `terminalActiveHistoryArchiveANSI` 暂留为死代码。
  3. 测试反转：archive 4 例 + `mutable_active_handoff_regression_test.go:16`（断言反转：
     finalize 前**不得**出现 early marker）+ stream_tail active 断言。
  4. **刻意保留**：`HistoryCommitActive`、`activeTokensByCell`、`advanceActiveCellLedgerOnAck`、
     `activeAckedRenderedPrefixRows`、`lastPlannedActive*`、`hasClaimedActiveOriginDelivery`——
     Acked 恒 0 时它们自然退化为无行为，回滚面最小。
- **第二刀（清理，行为中性）**：删 skipRows 管线/index、`advanceActiveCell*`、`noteFinalizedActiveAck`、
  `finalizedActiveAckPlanVersion`、`lastPlannedActive*`、`activeTokensByCell`/`activeAckPlanVersion`、
  `hasClaimedActiveOriginDelivery` 及 Active revision 豁免、`terminalActiveHistoryArchiveANSI`/`historyBatchIsActiveOrigin`。
- **第三刀（可选，高风险）**：再评估删 `ActiveCellState.Enqueued/Acked` 与 `MarkActiveAcked/Enqueued`、
  `HistoryCommitActive` 枚举；`historyStreamTailRows/historyTailCells`（**replay 去重证明，不可删**）
  与 `historyTopAligned` **全程保留**。
- **风险排序**：第一刀交付分支 > 第一刀规划停铸 > 第二刀；每刀后跑 `go test ./cmd/aicli/ui/...`，
  重点观察 `native_scrollback_*`、`terminal_session_executor_test.go`、`history_planning_budget_test.go`。
- **最大回归面**：finalize 一次性写必须与旧「active 前缀 + 后缀」逐字节等价（reasoning 投影形状警告
  planner:511-513），用 `:724/:762` 的替代断言固化。
