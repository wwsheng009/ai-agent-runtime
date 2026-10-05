# aicli UI 历史投递 stranded InFlight 与 actor 锁内规划：架构缺陷评审与加固计划（2026-10-05）

> 状态：评审完成；P0 已实施并通过回归；P1 部分实施（2026-10-05，见 §5 实施记录）。
> 现场：`session_20260930210352_V5o7MDYL`（进程 PID 15908，二进制 rev `3b48c5ed`，已包含 `046adc09`
> 的 fold 缓存扩容与 executor 有界等待修复）。
> 关联文档：`docs/plan/aicli-chat-unified-render-stall-analysis-and-hardening.md`、
> `docs/analysis/renderer-stall-analysis-20260901.md`、
> `docs/debug/ui-actor-stall-tool-completed-drop-and-snapshot-hotpath-20260930.md`、
> `docs/debug/cpu-hotspots-ui-render-and-session-persist-optimization-20260929.md`、
> `docs/e2e/resume-history-e2e.md`。

## 1. 结论摘要

本会话的"总是卡住"不是单一 bug，而是五个架构级缺陷叠加；其中 **D1 是可复现、可判定的
活性死锁，且与回合是否在跑无关**：

1. **D1（P0）handoff 死锁**：token 被 reducer 接受为 InFlight 后，若事务快照因 generation
   前移而拒绝组合，executor 既不会写、也不会回投任何 `Deferred/Failed` 结果；该 token 永久停留
   InFlight。排序护栏（`hasOlderPendingOrInFlight`）随即拒绝所有更晚 token 的认领，而调度器只扫描
   Pending、看不到它，全仓也没有老化/租约/看门狗。一个 stranded claim 即永久堵死整条 native
   history 管线。
2. **D2（P1）单锁 actor 承载无界工作**：`c.mu` 持锁期间执行全量 transcript 深拷贝与全量历史规划；
   `continueTruncatedHistoryPlan` 还用零值 deadline 做**无预算**规划。所有输入、重绘、executor
   快照都排在这把锁后面，单次持锁无上界（现场 plan-max 108s）。
3. **D3（P1）唤醒协议是事件白名单驱动**：`HistoryCommitWakeEffect` 只对 13 类 action 发射；
   `Deferred`、`HistoryProjectionInvalidated`、部分 `PlanIncomplete` 场景没有自唤醒，活性依赖
   "下一次恰好有白名单 action"，无周期性兜底。
4. **D4（P2）无界数据模型**：ledger `byToken/tokens/byRange/bySource` 无删除路径（现场 136,498
   条、其中 135,717 已 acked 仍常驻）；Scene/Transcript 同样无保留上限（8,830 cells / 270,976
   行）。为绕开 O(N) 扫描积累的单调计数器（`minNonTerminalToken/unresolvedCount/
   activeAckPlanVersion`）是持续打补丁的症状，不是解。
5. **D5（P0 附带）观测与语义盲区**：`in-flight` 只暴露计数、不暴露 token 与年龄；executor 的
   recovery 诊断环不覆盖 normal claim 路径，所以现场"82 分钟无诊断"既不能证明它空闲、也不能
   定位卡住的 token；状态行时钟是整轮时钟，"Running X (54m)" 让人误判单步卡死。

## 2. 现场证据（2026-10-05）

采样窗口（`GET /web/api/status`）：

| 指标 | 08:21 | 08:31 | 08:53 | 判读 |
| --- | --- | --- | --- | --- |
| `history_effects.pending` | 33 | 36 | **47** | 持续铸 token，只增不减 |
| `oldest_pending_token` | 289960 | 289960 | **289960** | 队头 10+ 分钟不动 |
| `in-flight` | 1 | 1 | **1** | 恒定 stranded claim |
| `render_output.sealed / last_sequence` | 3988 | 3988 | **3988** | 物理投递记录全程冻结 |
| executor recovery 诊断年龄 | ~50min | ~59min | **~82min** | 无迭代记录（且不覆盖 normal claim） |
| UI revision | 24372 | 26982 | **35631** | actor 仍在推进，排除"进程死了" |

补充事实：

- `GET /web/api/turn`：该回合已于 **08:50:29** 结束（`duration_ms=4,620,276` ≈ 77 分钟、
  73 步、输入 20.7M token）。**会话空闲后队列仍在增长且仍不投递**，排除"长回合导致"的解释。
- pprof goroutine dump（08:2x）：UI 线程在 `reduceUIControllerState → syncHistoryEffectsForTranscript
  → planEligibleHistoryCommitsWithin → layoutTranscriptScreenRowsWithin → toolFoldTargetRows`，
  同时 FramePump 线程阻塞在 `PostDeferred → c.mu.Lock`（`controller.go:410`）。
- 04-30 修复效果确认：`fold_omit` 缓存命中 99.37%、0 逐出；`cell_rows` 缓存顶满
  67.1MB/64MiB（81,249 逐出）说明 O(history) 成本仍在，但已不是本轮死锁主因。

现场签名（stranded InFlight + 排序护栏 + 无看门狗）可与进程内状态一一对应；具体卡住的 token
身份在现有诊断面不可见，P0.2 补上该观测。

## 3. 架构缺陷

### D1 handoff：严格排序 + 单次握手 + 静默失败（P0，现场直接病因）

代码路径（全部为已读行号）：

1. executor 读 schedule（只挑 `State == Pending`，`terminal_session_snapshot.go:59-74`）→
   `Post(BeginHistoryCommit)`（`terminal_session_executor.go:951-960`）。
2. reducer 执行 `markInFlight`（`controller_state.go:278-289`）：要求无更老 Pending/InFlight
   （`history_effect_queue.go:367-390`、`history_commit.go:575-580`），**错误被静默忽略**。
3. `waitControllerIdle` 会顺带 drain 排在后面的 `Resize/SetTheme`（`terminal_session_executor.go:958`），
   `state.LayoutGeneration` 前移；随后 `terminalSessionSnapshot` 里
   `terminalSessionClaimedBatchLocked` 因 `entry.Commit.LayoutGeneration != state.LayoutGeneration`
   返回 nil（`terminal_session_snapshot.go:127-135`）。
4. 事务因此**从未执行**，但没有任何回投动作把该 token 释放（`terminal_session_executor.go:1000-1011`
   只做 claim-miss 重试判断）。按语义这里应当发 `HistoryCommitDeferred`（"证明没有写任何字节"，
   `controller_state.go:348-357`），这正是本次 P0 修复点。
5. 排序护栏 `hasOlderPendingOrInFlight` 用 `minNonTerminalToken` 把后续所有 claim 判为
   `ErrHistoryCommitOutOfOrder`；而 schedule 永远看不到 InFlight，形成永久队头阻塞。
6. 解除路径只剩全量 replan 的 `syncHistoryEffectCandidates` invalidate 或显式 replay epoch；
   空闲 resumed 会话两条都等不到。**当前进程 15908 只能重启恢复**（ledger 为内存态）。

次生漏洞（同属 D3，但由 D1 暴露）：

- `HistoryCommitDeferred` 不在 pending 白名单（`controller.go:696-702`），回 Pending 后无自唤醒；
- `HistoryProjectionInvalidated` 不在白名单（帧写错误路径 post 后只能等下一次交互）；
- 截断规划在 `UpdateActiveCellAction` 场景可 `PlanIncomplete=true` 而无后续 trigger
  （`history_effect_queue.go:43-68` 注释自证同类现场）；
- Frozen/Lease 期间 park 依赖外部释放，本层无看门狗。

违反的设计不变量：C1"可恢复工作必须有显式调度入口，不得遗漏唤醒"（stall 文档 §5.1）、
C5"generation 隔离"只做了写侧隔离、没有配对的 abort/reclaim 路径。

### D2 actor 单锁承载无界工作（P1）

- `Run` 在 `c.mu` 内执行 `reduceUIControllerState`（`controller.go:577-586`），内含：
  - `FinalizeActiveCellAction` 全量 `NewTranscriptState` 深拷（`controller_state.go:585`、
    `app_state.go:84-101`）；
  - `syncHistoryEffectsForTranscript` 全量布局规划（`controller_state.go` 多处、预算
    `history_effect_planner.go:24` = 250ms/轮）；
  - `continueTruncatedHistoryPlan` → `syncHistoryEffectsForTranscriptWithin(state, time.Time{})`
    —— **零值 deadline = 无预算**（`history_effect_planner.go:888`、779-783 注释自证）。
- 读侧同一把锁：`State()/AppState()/DiagnosticState()` 深拷 transcript 与整份 ledger，
  代码注释自证单次 ~96MB/~180ms（`controller.go:962-967`）。
- 后果：锁持有时间无上界；frame pump（goroutine dump 实证）、executor 快照、durable Post
  全部被牵连。与 B5"所有等待有界"的意图相悖——不是等待无界，是临界区无界。

### D3 唤醒协议（P1）

见 D1 次生漏洞。唤醒是"action 白名单 ∨ 可见帧 FlushEffect"的事件驱动通道，没有周期性
deadline 排序或 tick 兜底；失败/超时路径不保证被本轮或下一轮重新调度。

### D4 无界数据模型（P2）

- ledger 注释明说 `tokens` 与 `byToken` 无删除路径、只增（`history_commit.go:219-222`）；
  `Clone/Entries/Summary` 都是 O(全量)。现场 136,498 条目的常驻内存与诊断扫描成本。
- Scene `cells` append-only（`scene.go:450/499/545`），无保留/分段上限；O(全量) 规划是结构性的。
- 邮箱侧：`PostDeferred` 无容量上限且与外部共享 FIFO；`followups` 无上限且优先于外部队列
  （并发代理报告 D8/D9），与 B1/B2"有界 + followup 不占外部容量"矛盾。

### D5 观测与状态语义（P0 附带）

- `in-flight` 只有计数；无 oldest-inflight-token/age，无法定位、无法告警。
- executor recovery 诊断环只记录 recovery 分支；normal claim/claim-miss 无痕，导致"静默
  忙转"和"空闲"不可区分。
- `dynamicStatusStarted` 是整轮时钟（`chat_interaction.go:1277-1298`），跨 tool/retry 阶段
  不重置；"Running X (Nm)" 应读作"本轮已运行 N 分钟"，当前文案易被读成"该步卡了 N 分钟"。
- 长轮成本：本轮 77 分钟 / 73 步 / 20.7M 输入 token，且 `Turn Budget: <none this run>`；
  长轮预算缺位（历史文档 §2.3 根因 D 已记录过一次）。

## 4. 与设计不变量的对照

| 不变量（出处） | 实现现状 |
| --- | --- |
| B1/B2 有界邮箱、非阻塞、followup 不占外部容量（event-bridge §5.2-6.1） | PostDeferred 无上限共享 FIFO；followups 无上限且优先 → 背压倒置风险 |
| B5 所有等待有界、reducer/effect callback 不得反向等待（stall §8.3） | 等待已加界（2s），但**锁内无预算规划**让临界区无界，等价延迟不可控 |
| C1 可恢复工作必须有显式调度入口、不丢唤醒（stall §5.1） | Deferred / ProjectionInvalidated / 部分 PlanIncomplete 无自唤醒；claim-miss 无回投 → D1 |
| C5 generation 隔离：旧 generation 的 Ack/frame 不得确认新代（architecture §4.4） | 写侧隔离正确；但被拒绝的 claim 没有 abort/reclaim，隔离变成永久泄漏 |
| D1/D2 handoff 单调前进、exactly-once（refactor-plan INV-HANDOFF-01/02） | 排序护栏保证单调，但一个 stranded claim 让整个 frontier 永停 |
| E3 缓存只存派生结果、可重建（architecture §13.2） | 成立；但无界 ledger/scene 不是缓存，无法重建出"有界" |

## 5. 修复计划

### P0（已实施，2026-10-05：最小闭环，修复现场直接病因）

- **P0.1 claim-miss 显式释放**（`terminal_session_executor.go` runOne 的
  `claimedToken != 0 && snapshot.claimed == nil` 分支）：补投
  `HistoryCommitDeferred{Token: claimedToken, LayoutGeneration: schedule.pendingGeneration}`。
  语义依据：该分支未执行任何写（compose/Flush 从未运行），Deferred 即"证明零字节"；
  reducer 会 `deferInFlight` → `Pending`，generation 不一致时 `rebasePendingHistoryEffects`
  把载荷重基到当前代。若 markInFlight 本就被拒（token 非 InFlight），该动作是安全 no-op。
- **P0.2 观测**：executor 增加 `claimMissReleases` 计数并附到 RecoveryDiag/导出；
  `/debug` 可见 claim-miss 释放次数（不再"无痕"）。
- **P0.3 回归测试**：锁定"claim 被接受 → generation 前移 → 快照拒绝 → token 回 Pending 并可
  重新投递"的完整链路；同时覆盖"markInFlight 被拒时 Deferred 为 no-op"。

验收标准：

1. 构造 stale-generation InFlight token，执行一次 executor 周期后：ledger 中该 token 回到
   Pending（rebase 到当前 generation），无永久 InFlight；
2. 随后一次正常 claim 可以推进队头（不再被 `ErrHistoryCommitOutOfOrder` 永久拒绝）；
3. `claimMissReleases` 计数按预期递增；
4. `go test ./cmd/aicli/ui/...` 通过（含 race 视 CI 而定）。

运维注：对已卡住的进程 15908，P0 只防复发；该进程内存中的 stranded token 不会自愈，
需要用一次全量 replan（不可直接触发）或重启恢复。这是本缺陷"不可自愈"的直接后果，也是
P0.2 必须补观测的原因。

### P0 实施记录（2026-10-05）

- 代码：
  - `backend/cmd/aicli/ui/terminal_session_executor.go`：新增 `releaseClaimMiss`（对
    claim-miss 显式 Post `HistoryCommitDeferred` 并做有界 drain，让 reducer 把已接受的
    claim 回 Pending 并 rebase 到当前 generation），在常规认领点
    （`claimedToken != 0 && snapshot.claimed == nil`）与 backoff success-mode 认领点接入；
    新增 `diagClaimMissReleases` 计数并从 `RecoveryDiag()` 输出为 `claimMissReleases`。
  - `backend/cmd/aicli/ui/executor_diag_export.go`：文本导出新增 `claimMissReleases` 行。
  - `backend/cmd/aicli/ui/terminal_session_executor_test.go`：新增
    `TestTerminalSessionExecutorClaimMissReleasesStrandedInFlight`。测试通过
    `ReducerContext.PostFollowup` 在 BeginHistoryCommit 的同一批注入 Resize，
    确定性复现"markInFlight 已接受 → waitControllerIdle 排空 resize → 快照拒绝"；
    断言 token 不再停留 InFlight、队列排空、`ClaimMissReleases >= 1`。未打补丁时该测试会在
    排空超时与 `ClaimMissReleases == 0` 两条断言上失败（计数断言保证先红后绿）。
- 验证（在 HEAD `d9e0bc2e` 的隔离 worktree 中注入上述文件后运行，避免并发 WIP 干扰）：
  - `go test ./cmd/aicli/ui/ -run 'TestTerminalSessionExecutor' -count=1` → ok（用例 32.1s）；
  - `go test ./cmd/aicli/ui/ -run 'TestTerminalSession|TestHistory' -count=1` → ok（20.7s）；
  - `gofmt -l` 三个文件均无输出。
- 备注：主工作区当时存在并发中的无关 WIP（`internal/chat` 等，`actor_abandon.go` 缺
  `time` import 导致主树 `go build` 失败）；本次验证刻意在隔离 worktree 完成，未触碰该 WIP。
  现场进程 15908 的既有 stranded token 仍需重启才能清除（ledger 为内存态）。

### P1（部分实施，2026-10-05）

1. **截断规划的续跑触发与唤醒**（P1.1）
   - P1.1a（已实施）：空队列 + `PlanIncomplete && !PlanStalled` 时，`historyCommitWakeNeeded`
     对任意 action 发射唤醒（带 Frozen/ProjectionUnknown/unresolved 同源门），让 executor 的
     `ContinueHistoryPlanAction` kick 在空闲 resume 会话里可达；同时把
     `HistoryProjectionInvalidated` 加入 recovery 白名单——该 action 自身置
     ProjectionUnknown，此前却没有唤醒出口。
   - P1.1b（设计已评审，待实现）：**不能**简单给 `continueTruncatedHistoryPlan` 加预算
     （planner 注释 `history_effect_planner.go:877-887` 自证：冷缓存大前缀下每轮有预算的
     pass 会停在同一样本点 → NextToken 不变 → PlanStalled 永久置位）。经独立只读审查
     （2026-10-05，expert）确定的正确方案是 **"有界前缀推进 + 末尾一次热缓存全量 pass"**：
     1. `layoutTranscriptScreenRowsWithin` 增加 startRow 变体：预算只允许落在 cell 边界
        （命中时若当前 cell 已整块 append，则 nextRow 前移到该 cell 语义行末尾；不得跨
        gap），返回 `(rows, complete, nextRow)`；`startRow>0` 允许返回 0 行且未前进，
        调用方必须按 stalled 处理，绝不能当"没有历史"。
     2. 新增 `planEligibleHistoryCommitsWithinFrom(state, deadline, startRow, screenRowsBefore)`：
        `displayStart=screenRowsBefore`、`firstVisible=screenRowsBefore+len(rows)`
        （`firstVisible` 当前恒等于 len(rows)，直接沿用后缀长度会跳过全部续跑候选）；
        `wholeCellHistoryCommit` 调用点的 DisplayRange 必须全局化（否则
        `historyCommitKey`/presentation 比较错位，产生重复 token）；complete 仅当 walk 到末尾。
     3. memo miss 时若游标指纹匹配且未被显式 invalidate → 前缀轮（只调
        `syncHistoryEffectCandidatesPrefix`，更新游标）；walk 到达末尾后做**一次无预算全量
        pass**（此时布局缓存全热），并用 `syncHistoryEffectCandidates(...,0)` 做 membership
        踢除，然后清游标 + `recordTranscriptPlanMemo`。**绝不允许 suffix-only 调 membership
        踢除**：该函数把候选列表当"完整有效集合"（:1099-1127），后缀会误杀全部前缀
        Pending/InFlight token（`TestSyncHistoryEffectCandidatesPrefixKeepsPendingTail`
        守护的正是这条）。
     4. 游标指纹 = memo 全部输入 + memo generation；`invalidateTranscriptPlanMemo` 与
        reconcile（TerminalEpoch 已在指纹内）必须清游标。**前置缺口**：`activeAckPlanVersion`
        目前只增无人消费，它变化会让 `skipRows` 变大、候选收缩而指纹不变——需要纳入失效判据；
        但它每次 active ack 都递增，直接放进 memo 会让交付期每次 ack 都全量重规划，必须先
        设计"按 finalized cell 作用域"的版本或等价谓词，不能裸加。
     5. 截断规则（history_effect_planner.go:114-119）必须在 cell 对齐后放开为"cell 完整
        包含即可使用 whole-cell fallback"，否则**不可映射 plain cell（tab/控制符）在被截断
        前缀里将永久缺失**（本轮审查发现的正确性缺口，属实现前必须解决的 blocker）。
     实现测试清单：属性断言"逐轮续跑产出的 identity 并集 == 全量规划 identity 集"（覆盖
     plain / 结构化 markdown / reasoning / 折叠 tool / 不可映射 plain 五类）、无重复 token、
     物理行数相等、结构化大 cell（>layoutBudgetCheckRows）中部截断的边界行为、active ack 使
     游标作废、memo 强制失效清游标、小非零预算下复用 `assertHistoryCoverage` 的端到端覆盖。
2. 规划移出 `c.mu`（P1.2，未实施）：锁内只取输入快照，锁外 `planEligibleHistoryCommits`，
   锁内只 reconcile；`State()/debug` 改无锁快照（或复用 transcript 不可变快照）。
3. D5 观测（已实施）：`HistoryEffectQueueSummary` 增加
   `OldestInFlightToken/OldestInFlightGeneration`，`/debug` 摘要输出
   `oldest-inflight-token/gen`，使 "in-flight generation 落后于 layout generation" 的
   stranded 签名可被直接读出（此前只有计数，无法与健康写入区分）。reducer 侧
   `BeginHistoryCommit` 的拒绝/跳过也全部留痕：`ClaimSkipsStaleAction`（动作 generation
   过期，未尝试认领）与 `ClaimRejectsOutOfOrder/Gate/Stale/Invalid`，经 Summary 输出到
   `/debug`（`claim-skips-stale-action=… claim-rejects-*=…`）。此前这些拒绝完全静默——
   stranded InFlight 事故中排序护栏拒绝了每一次后续认领，却没有任何痕迹。

### P1 实施记录（2026-10-05）

- 代码：
  - `backend/cmd/aicli/ui/controller.go`：空队列分支增加
    `planContinuationPending() && !Frozen && !ProjectionUnknown && !hasUnresolvedTerminalDelivery()`
    自唤醒；recovery 白名单加入 `HistoryProjectionInvalidated`。
  - `backend/cmd/aicli/ui/controller_test.go`：新增
    `TestHistoryCommitWakeNeededForTruncatedPlanContinuation`（incomplete→唤醒；
    stalled/frozen→不唤醒；projection-unknown 下仅 recovery 白名单 action 唤醒；
    invalidated→唤醒 recovery）。
  - `backend/cmd/aicli/ui/history_effect_queue.go`、`history_diagnostic_state_test.go`：
    Summary 增加 oldest in-flight token/generation，并同步逐条遍历等价性断言。
  - `backend/cmd/aicli/ui/history_effect_queue.go`、`controller_state.go`：新增
    claim 拒绝/跳过计数与 `recordClaimRefusal` 分类（out-of-order / gate / stale /
    invalid / stale-action），`Summary()` 暴露 5 个计数。
  - `backend/cmd/aicli/ui/controller_test.go`：新增
    `TestBeginHistoryCommitRefusalsAreObservable`（4 类拒绝/跳过路径 + Summary 断言；
    并锁定"拒绝不改变 token 状态、不触发 ProjectionUnknown"）。
  - `backend/cmd/aicli/commands/chat_debug_document.go`：debug 摘要追加
    `oldest-inflight-token/oldest-inflight-gen` 与 5 个 claim 拒绝计数。
- 验证：
  - `go test ./cmd/aicli/ui/ -run 'TestHistoryCommitWakeNeeded|TestTerminalSessionExecutor' -count=1` → ok；
  - `go test ./cmd/aicli/ui/ -run 'TestBeginHistoryCommitRefusalsAreObservable|TestHistoryEffectQueueSummary|TestHistoryCommitWakeNeeded|TestTerminalSessionExecutor' -count=1` → ok；
  - `go test ./cmd/aicli/ui/ -run 'TestHistoryEffectQueueSummary|TestHistoryDiagnostic|TestHistoryCommitWakeNeeded' -count=1` → ok；
  - `go test ./cmd/aicli/commands/ -run 'TestHistoryEffectDiagnosticsExposeScrollbackReplayGrant' -count=1` → ok；
  - 涉及文件 `gofmt -l` 均无输出。

### P1.1b 实施进展（2026-10-05）

- **Stage 1+2（已实施）**：
  - `app_screen_layout.go`：新增 `layoutTranscriptScreenRowsFrom`（startRow 续跑，返回
    `(rows, complete, nextRow)`）。**截断语义修正为"预算命中后补完当前 cell 再返回"**：
    实测发现"按 cell 前移游标"仍会切出残缺窗口——预算恰好命中某 cell 的内容行、而它的
    前导 gap 行已进入窗口时，plan 侧把"只含 gap 的切片"当成完整 cell，触发 whole-cell
    回退并铸出与全量规划不同的身份（bug 再现：prefix cell 513 src={0,570} vs full 的
    7 个 fragment）。补完当前 cell 后每个窗口只含完整 cell，nextRow 天然落在 cell 边界。
  - `history_effect_planner.go`：新增 `planEligibleHistoryCommitsWithinFrom(state, deadline,
    startRow, screenRowsBefore) → (commits, complete, nextRow, screenRows)`；DisplayRange
    以 `screenRowsBefore` 为全局基址、`firstVisible` 全局化；whole-cell fallback 判据由
    `complete` 改为"cell 完整包含在窗口内"（修掉不可映射 plain cell 在续跑前缀永久缺失
    的缺口）；旧入口退化为 startRow=0 代理（行为不变，除判据修正本身）。
  - 新增 `TestPlanEligibleHistoryCommitsResumeUnionMatchesFullPlan`：预算 0 下逐轮续跑的
    提交并集（historyCommitKey 身份 + 行数多重集）与无预算全量规划完全一致；夹具覆盖
    plain / 结构化 markdown / 折叠工具链 / **不可映射 plain（tab → whole-cell fallback）**。
  - 验证：`go test ./cmd/aicli/ui/ -run 'TestLayoutTranscript|TestPlanEligibleHistoryCommitsResumeUnionMatchesFullPlan|TestHistory|TestSync|TestTranscript|TestTerminalSessionExecutor' -count=1`
    → 除下述既有间歇测试外全部通过；`TestTruncatedTranscriptPlanContinuesUntilComplete`
    单跑（含新身份语义断言）56.2s 通过。
- **Stage 3（已实施）**：
  - `history_effect_queue.go`：`HistoryEffectQueueState` 新增 `planResume*` 游标
    （row / screenRows / `transcriptPlanInputs` 指纹）与 `store/clearTranscriptPlanResume`；
    `invalidateTranscriptPlanMemo` 同步清游标（armed replay / no-op install 后 ledger
    可能整体被替换，旧游标会漏掉前缀——审查 A.4 的空屏路径）。
  - `history_effect_planner.go`：`syncHistoryEffectsForTranscriptWithin` 返回
    `(completed, advanced)`；指纹匹配时从游标续跑（一个预算的前缀轮），游标走到末尾
    时由 `finishResumedTranscriptPlan` 做**一次无预算全量 pass** 完成 membership 踢除、
    清游标、落 memo；指纹不匹配/无游标则从 0 重规划。`transcriptPlanMemoHit` 在
    `PlanIncomplete` 时直接失效（取代原先截断路径的显式 `invalidateTranscriptPlanMemo`
    ——后者现在会连游标一起清掉）。`continueTruncatedHistoryPlan` 改为**有预算**的游标
    推进（每轮一个 `historyCommitPlanningBudget`），仅当"未完成且游标未前进"才置
    `PlanStalled`（新 token 数不再是进展判据：新窗口可能整体落在已有终态记录的区域）。
  - 新增 `TestTranscriptPlanResumeCursorAdvancesUntilComplete`（预算 0 下 reducer 级多轮
    收敛：游标严格前进、每轮有界、最终覆盖全部 finalized cell）与
    `TestTranscriptPlanResumeClearedByForcedInvalidation`（显式失效清游标）。
  - 验证：预算 0 单元测试单跑 54.4s PASS（耐心窗口 60→150s，注释说明多轮收敛成本）；
    `TestArmedResumeDeliversWholeTranscriptAcrossBudgetTruncation` PASS 81.4s；
    `TestExecutorContinuesIncompletePlanWithoutAckTrigger` PASS 19.3s；
    `TestHistory|TestTranscript|TestLayoutTranscript|TestPlanEligible|TestSync|TestTerminalSessionExecutor`
    组 PASS 18.9s。
- **P1.1c（已实施，补 P1.1b 留下的缺口）**：`activeAckPlanVersion` 原先无消费方，
  存在"finalized cell 的 active-origin ack 收缩 skipRows、memo 却仍命中"的路径。
  实现为**按 finalized cell 作用域的队列侧计数器** `finalizedActiveAckPlanVersion`：
  reducer 的 ack 处理器在提交 `Origin==Active` 且"不再等于当前活跃可变 cell"时推进
  （仍活跃的 ack 不计入，否则流式热路径退化为每 ack 全量重规划）；memo 判据与
  `transcriptPlanInputs` 游标指纹都纳入该版本。新增
  `TestTranscriptPlanMemoTracksFinalizedActiveAcks` 与
  `TestNoteFinalizedActiveAckScopesToFinalizedCells`；宽测试组（History/Transcript/
  Sync/Executor/NativeScrollback）PASS 22.6s。
- 既有测试观察（A/B 在 HEAD 复现，与 Stage 1/2 无因果）：
  - `TestTruncatedTranscriptPlanContinuesUntilComplete`：60s 内部窗口在负载下必然
    超时（HEAD 复现 62.5s 失败）；Stage 3 起该用例走多轮 0 预算收敛，耐心窗口已提到
    150s。`next=12600`（reconcile 前 6300 + 前缀 3585 + 续跑后缀）为既有 token 语义。
  - `TestKeyHandlerStart_DoesNotPollSessionInputWhileSuspended`：在 HEAD 同样稳定失败
    （该文件最近由 79eb8c9a / a95a98ea 触碰，非本线改动）。
  - `TestTerminalSessionExecutorDrainsFinalResidentTailQueuedDuringBlockedFrameWrite`：
    间歇 `marker count=2`（Stage 2 之前已出现一次；单跑通过），归入既有 flake 待排查。

### P1.2 设计评审稿（规划移出 c.mu）

**现状与目标**
- 规划（`syncHistoryEffectsForTranscriptWithin`）在 actor（`c.mu` 内）运行；最贵的是
  screening/layout（逐 cell 布局、markdown/chroma 重渲染、wrap），铸 commit 只在其次。
- 目标：screening 移出锁（plan worker goroutine），锁内只保留三件事：
  ① 快照构造（O(cells) 拷贝 header）；② 铸 commit（读 live ledger + 入队，窗口有界）；
  ③ 收尾（membership 踢除 + 清游标 + 落 memo）。
- 复用 P1.1b 游标实现"窗口化"：一次请求 = 一个 screening 窗口，收敛仍由
  `PlanIncomplete` + executor kick 驱动，锁内单轮成本上界 = 一个窗口的铸 commit。

**相位拆分（代码级）**
1. `screenTranscriptPlanWindow(snapshot, deadline, startRow) → (rows, complete, nextRow)`：
   纯布局，**不读 ledger、不读 HistoryEffects**。`layoutRows`（`LayoutTranscript`）与
   fold target 的派生也在 worker 内进行——它们单独就可能吃掉整个预算
   （history_effect_planner.go:80-96），留在锁内等于目标落空。输入只有快照。
2. `mintTranscriptPlanWindow(state, snapshot, rows, screenRowsBefore) → (commits, ...)`：
   锁内。现有 `planEligibleHistoryCommitsWithinFrom` 的组行逻辑原样保留；**分组数据用
   快照的 cells/byID**（结构化等价，不依赖 fence 假设），frontier / activeCommits /
   skipRows / settled 用 live 状态（见下）。
3. `finishResumedTranscriptPlan`（Stage 3 已有）：末尾一次无预算全量 pass 完成
   membership 踢除、清游标、落 memo。

**快照与所有权**
- `transcriptPlanSnapshot{cells []TranscriptCell, mutable map[CellID]struct{}, geometry,
  theme, generation, projection}`；Source 字符串共享（不可变），cells 做**值拷贝**
  （reducer 可能原地改 `Cells[i]`；6600 cell ≈ 1.3MB/请求，可接受）。byID / layoutRows
  / fold target 全部在 worker 内派生（`LayoutRows` 每次返回 `scene.LayoutTranscript`
  生成的独立行值，app_state.go:184-197，归 worker 独占）。
- 承载不变量是"**生产代码无对既有 transcript cell / layoutRows 的原地写**"（审查确认
  全仓只有测试在写，如 app_layout_test.go:57）——必须加**守护测试**固化它，而不是依赖
  口头约定。
- ledger **不进快照**：settled / skipRows / frontier(Active) 全在 mint 相位读 live
  状态。因此不存在"账本快照过期"这类竞态；陈旧只能来自布局输入，用指纹判定（见下）。

**请求/结果与栅栏**
- 队列状态新增：`planRequestSeq uint64`（单调）、`planRequestInFlight bool`、
  `planInputsEpoch uint64`（显式失效序号）、快照指纹（复用 `transcriptPlanInputs`）。
- **planInputsEpoch**：`invalidateTranscriptPlanMemo`（armed replay / no-op install）
  自增该序号；请求携带、结果接收时比对。没有它，一个"失效之前产生、失效之后到达"的
  结果会通过指纹比对并 `storeTranscriptPlanResume` 复活刚被清掉的游标（重演 A.4 的
  空屏路径）；TerminalEpoch 只覆盖 reconcileScrollback，不覆盖显式失效。
- reducer 需要规划时（memo miss / 游标续跑）：若无在飞请求 → seq++、构造快照、
  **非阻塞投递**（`select{case ch<-req: default: 覆盖槽位}`；actor 绝不能阻塞在 send
  上——worker 可能正阻塞在 `c.Post` 等锁，会形成死锁环）。
- worker：screen → Post(`HistoryPlanWindowReady{seq, planInputsEpoch, fingerprint,
  rows, complete, nextRow, screenRows, screenMs}`)。结果 action 必须是 **ClassBarrier /
  不可 coalesce**（同 ContinueHistoryPlanAction，action.go:564-568），否则批处理 merge
  会把它丢掉。
- reducer 收结果（顺序固定）：① `seq != planRequestSeq` → 丢弃、**不清 in-flight**
  （新请求仍有效）；② 失效序号/指纹不匹配，或 Frozen / ProjectionUnknown /
  ReconciliationRequired / geometry 门被翻起（controller.go:705-708、
  history_effect_planner.go:948-953）→ 丢弃并**立即按当前输入重新请求**（seq++）；
  ③ 通过 → mint + enqueue + Stage 3 游标/收尾，清 in-flight。
  ②的"立即重请求"是必须的：首次（无游标）规划被丢弃时 PlanIncomplete 仍为 false，
  executor kick 与空队列唤醒都不会触发，空闲会话（正是 resume 场景）会永久停摆。
- `continueTruncatedHistoryPlan`：在飞时不重复请求、返回 true 且不置 PlanStalled。
- **kick 门（阻断项修正）**：`planContinuationPending()`（= `PlanIncomplete &&
  !PlanStalled`）必须改为"在飞时为 false"——否则 executor 的
  `claimedToken==0 && schedule.planIncomplete` 分支会每轮 Post 一枚 barrier Continue
  （terminal_session_executor.go:1039-1052，run 无 sleep），形成 actor↔executor 热旋转。
  在飞即"进展已委托"，结果 action 自身的 wake 谓词负责重新唤醒。

**生命周期**
- worker 与 actor 同生命周期：`Run` 启动；用 `done` channel 通知退出，**不关闭请求
  channel**（Run 排空途中 reducer 仍可能投递 → close 会 panic）；worker 的 Post 在
  Close 后返回 false 即退出；测试 teardown 用 `WaitPlanWorker`（新增），避免 goroutine
  泄漏。
- `WaitIdle` **必须纳入 `planRequestInFlight`**（controller.go:863-876 明确不等待异步
  worker、也不读 revision，设计稿原判断错误）：否则装载后 WaitIdle 立刻返回、覆盖度
  断言读到半应用状态——现有 E2E 全部依赖它。

**诊断（P16 拆分）**
- 新增 `LastScreenMs/MaxScreenMs`（worker 侧）与 `LastMintMs/MaxMintMs`（锁内），
  现有 `LastPlanMs/MaxPlanMs` 保持 = screen + mint（兼容 /debug 与既有断言）。
  P1.2 验收标准：锁内 mint 的 MaxMintMs 显著低于现状 MaxPlanMs。

**测试计划**
- 窗口化收敛：现有 E2E（ArmedResume / ExecutorContinues / budget 测试）worker 化后
  必须继续 PASS——它们是最好的回归网。
- 陈旧丢弃：请求在飞时改 geometry/theme/transcript → 结果被丢弃 → 下一轮收敛（新增单测）。
- 生命周期：Close 时不泄漏、不 panic；晚到结果被丢弃。
- 竞态：`go test -race` 跑 History/Transcript/Executor 组（worker 与 actor 并发）。
- 判据交互：在飞期间 P1.1b 游标/P1.1c 版本变化 → 结果丢弃而非半应用。

**风险与开放问题（评审后更新）**
1. **缓存缺失去重（审查新增）**：共享缓存只有 mutex、没有 singleflight——actor 与
   worker 同时 miss 同一 cell 会各渲染一份（只浪费 CPU，LRU 计费正确）。接受为已知
   成本；若 P16 显示重复渲染显著，再加 per-key 合并。
2. **丢弃风暴**：in-flight 期间连续变更 → 结果反复丢弃 + 立即重请求；丢弃只烧 worker
   CPU（不占锁），请求通道的覆盖语义天然合并。新增 `PlanDiscarded` 计数观察。
3. **mint 相位锁内成本上界**：窗口 4096 语义行 ≈ 最多数千 commit 的入队；是否恒为
   ms 级需 P16 实测；不达标则窗口按 fragment 预算再切。
4. **快照 cells 值拷贝成本**：每请求 O(cells) ≈ 1.3MB（6600 cell），窗口数 ~70 时总量
   ~90MB 拷贝，可接受；若成热点可改为按 frontier 前缀截断 cells。
5. ~~快照 COW 假设~~：已自证（LayoutRows 独立派生），且承载不变量收敛为"无原地写者"
   + 守护测试（见"快照与所有权"）。

### P1.2 实施进展（2026-10-05）

- **Stage A（已实施）**：纯重构，零行为变更目标。
  - `history_effect_planner.go` 拆出 `transcriptPlanSnapshotFor`（锁内构造：cells 值
    切片别名 + byID/mutable/layoutRows/width/generation/theme）、
    `screenTranscriptPlanWindow`（纯布局、不读 ledger/HistoryEffects）、
    `mintTranscriptPlanWindow`（锁内：快照解读 rows + live frontier/Active/skipRows/
    settled）；`planEligibleHistoryCommitsWithinFrom` 退化为"快照 → screen → mint"
    三步，入口签名与语义不变。
  - 新增 `TestTranscriptCellsNotMutatedInPlaceByReducers`：守护"无原地写者"不变量
    （持有旧 cells 切片 + 逐 cell 拷贝，跑替换/几何/armed replay 三类 reducer 后比对）。
- **顺带修复（A/B 定位的真实缺陷）**：screening 预算门限
  `time.Now().After(deadline)` → `!time.Now().Before(deadline)`。Windows 上
  `time.Now()` 刻度可能粗于两次调用间隔：`budget=0`（deadline == 创建时刻）时整轮
  screening 可能判"未过期"，于是"0 预算必然在第一个采样点切断"的契约随机失效。
  证据链：HEAD `-count=4` 8/8 过 → Stage A 版本复现失败（第 3/4 轮）→ 临时走线显示
  失败轮 index=0/4096/8192 三处 `after=false`（整轮落在一个时钟刻度内）→ 改为
  `!Before` 后 `-count=8` 16/16 过。副作用：`TestTruncatedTranscriptPlanContinuesUntilComplete`
  从 52-56s 提速到 26.3s（确定性首点截断，少走空轮）。
  - 同类观察（未改）：active 交接循环的 `After(deadline)`（history_effect_planner.go:430）
    在 0 预算下可能不提前 break；该路径同受粗刻度影响但无正确性后果，列入观察。
- 验证：宽测试组（History/Transcript/Plan/Sync/Executor/NativeScrollback/两个 E2E）
  PASS 76.6s；预算 0 生命周期用例 PASS 26.3s；`gofmt -l` 无输出。
- **Stage B（待实施）**：worker + 请求/结果 action + seq/planInputsEpoch/指纹三重栅栏
  + kick 门 + WaitIdle/WaitPlanWorker 生命周期（按设计稿执行）。

### P2（结构性）

1. ledger 终态压缩（按 epoch 剪枝/聚合 acked 条目，保留 source 身份去重的最小集）。
2. Scene/Transcript 分段/保留上限；`PostDeferred`/`followups` 容量与优先级契约对齐文档 B1/B2。
3. 状态行"阶段时钟 / 回合时钟"分离；长轮预算与软着陆在 UI 侧可见。

## 6. 验证

- 单测：`go test ./cmd/aicli/ui/ -run 'TerminalSessionExecutor|HistoryCommit' -count=1`。
- 回归护栏：P0.3 新测试必须能在未打补丁的代码上失败（先红后绿）。
- 现场观察表达式（修复上线后）：
  - `history_gates.pending_count` 在空闲会话中回落到 0；
  - `oldest_pending_token` 不再长期冻结；
  - `executor` 区块出现 `claimMissReleases` 且 `in-flight` 不再恒为 1；
  - `render_output.delivery_records_sealed` 随投递前进。

## 7. 风险与回滚

- P0.1 的 Deferred 只用于"事务未开始"的 claim-miss 分支，不会触发重写/重复写；最坏情况是
  对一个非 InFlight token 的 no-op 动作，无副作用。
- 单文件改动 + 单测；回滚即 revert `terminal_session_executor.go`（及可选诊断字段）。
- 诊断字段只增不改既有契约（JSON 新增键），对旧消费者向后兼容。
