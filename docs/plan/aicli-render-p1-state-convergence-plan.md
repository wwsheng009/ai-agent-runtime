# P1 状态收敛实施子计划（统一渲染架构审计 · 第二阶段）

> 来源：`docs/plan/aicli-unified-render-architecture-audit-20261005.md` §6 P1。
> 基线：`feat/render-p0-writer-unification` @ `44f31cbf`（P0 写端归一完成，18 提交）。
> 状态：**侦察完成 + P1-1 第 1/2/3 步、P1-2a、P1-2b 第 1/3 小步与几何收敛第 1 部分、P1-3 第 1/2 小步已实施**
>（2026-10-06；三路 explore 原始报告要点已归档于 §6，全部证据带 `文件:行`）。

## 0. 结论摘要

- 根因：**writer 物理事实（frame/history tail/物理投影）与 reducer 语义事实（Geometry/LayoutGeneration/ledger）
  之间缺少显式边界**——既有 ack 栅栏缺失（1ms 轮询 + 三连 Wait 代偿），又有镜像字段在两层间来回抄写。
- 顺序（依赖驱动）：
  1. **P1-2a 零风险删除**（`LayoutGeneration` 字段已删；`historyTailCells` 复核后**撤回删除**、
     `historyPrepareHits/Misses` 复核后**暂缓**——见 §2.1 修订）；
  2. **P1-3 事件驱动 ack**（先建栅栏，消除无界 WaitIdle 死锁风险——P1-1 删 InFlight 的前提）；
  3. **P1-1 ledger 四步收敛**（InFlight/claim → 六态归一 → 计数器游标化 → 规划续跑组）；
  4. **P1-2b 派生收敛**（`historyStreamTailRows`/`historyTopAligned` 改 ledger ack 推导、几何 probe 收敛、frame 单点）。
- 不做：Frozen/ProjectionUnknown/ReconciliationRequired/ScrollbackReplayArmed/ProvenScrollbackEpoch/HandoffFrontier
  必须保留（lease 背压 + 恢复门 + 一次性破坏授权 + 独立坐标系，见 §1.4 风险 3-7）。

## 1. P1-1 ledger 收敛

### 1.1 六态清单（enum `ui/history_commit.go:89-122`）

| 状态 | 定义 | 语义 | 主要写点 | 主要读点 |
|---|---|---|---|---|
| pending | :92 | 已入队未 claim | Enqueue :279；DeferInFlight :320 | Pending() queue:248；HasPending :548；markInFlight 前置 :303 |
| in_flight | :93 | 已 claim、物理写窗口 | MarkInFlight :306；ackBatch :635 | Ack 前置 :400；Fail :475；Invalidate :364；executor 校验 :219 |
| acked | :94 | 物理写成功已进滚动区 | Ack :406；ackBatch :639 | hasSettledRecordForSource :284；skipRows 锚 planner:335-367 |
| failed | :95 | 写失败＝未决交付 | Fail :478；batch quarantine :663 | 未决谓词 :505；hasTerminalRecord :684 |
| invalidated | :96 | 语义替换取消（可能已部分写） | Invalidate :368-380 | 未决谓词 :506；prunable :574-575 |
| abandoned | :102 | settle 隔离终态（不重放、阻断同源再铸） | SettleUnresolvedWithoutReplay :510 | hasTerminalRecord :685；compaction :651-655 |

辅助：`Entry.State/AckFrame/MayHavePartiallyWritten/Failure` :124-131；终态吸收性 :203-204、:696-700。

### 1.2 删除候选 vs 必须保留

**删除候选**（全部带替代物）：
1. `InFlight + Begin/Defer claim 协议` → 替代＝"下一个待写 token 指针"（executor 直接持单调 claim 游标）。
   证据：history_commit.go:301-327；history_commit_executor.go:133-194；releaseClaimMiss :806-830；
   reducer 分支 controller_state.go:283-299/:360-369。
2. `claimSkips*/claimRejects*` 计数器（:108-112、:331-347）→ 并入单调计数器。
3. `Enqueued 区间` → 规划只按 Acked 游标推进后成为派生值（planner :1577-1622）；`Stable` 不可删。
4. 规划续跑组 `PlanIncomplete/PlanStalled/planResume*/planRequest*`（queue :57-92）→ 前提是规划单线程增量
   （无预算截断、无异步 screening）。
5. `lastPlanned*` memo（:119-182）→ 与 6 态解耦，可用单调 generation 显式失效，后置。

**必须保留**：Acked（交付事实 + 防重铸锚点，需"每 cell 已交付 source 前缀 + 来源身份 tombstone"表达）；
Failed/Invalidated/Abandoned（部分写不可由内存游标证明；可合并为单个 `Quarantined`，不可删）；
Frozen/ProjectionUnknown/ReconciliationRequired/ScrollbackReplayArmed/ProvenScrollbackEpoch/TerminalEpoch；
HandoffFrontier（渲染行坐标系，trim 重定基，与 ledger token 不可互替）。

### 1.3 四步删除顺序（每步独立可回滚提交）

1. **InFlight/claim 协议**：影响链 terminal_session_executor.go:843-1073 → controller_state.go:283-299 →
   history_effect_queue.go:506-529 → history_commit.go:301-327；
   测试重写面：history_commit_executor_test.go:17/:144/:176、terminal_session_executor_test.go:465/:541/:603/:895。
2. **六态归一**（queued/delivered/quarantined）：Fail/Invalidate/Settle history_commit.go:473-521、
   queue :649-716、controller_state.go:349-382、action.go:471-529；保留 unresolvedCount 与阻断语义。
3. **pendingCount/minNonTerminalToken 游标化**（队列头指针 + 交付游标）：history_commit.go:197-205/:301-310/:467-471/:703-708。
4. **规划续跑组删除**：planner :956-1070/:1168-1227、queue :57-92、action.go:549-589、executor :1039-1053
   ——必须在规划单线程化之后。

### 1.4 风险（删除前必须钉测试）

1. 乱序写：删 InFlight 后多 token 并发写会破坏滚动区顺序；替代物必须含"已交付行号单调游标 + 严格 FIFO 写"。
2. 部分写不可证明：Fail 恒为 unresolved（history_commit.go:481-483）；删 Failed/Invalidated 会破坏防重铸与 skipRows 证明（双写/永久空白）。
3. 重入/替换安全点：Pending 时 rebase、InFlight 时 invalidate 是唯一安全点（planner :1480-1500）；截断前缀只入队不逐出（:1518-1528）。
4. 超时/退避：executor reset backoff 依赖 ProjectionUnknown/ReconciliationRequired（terminal_session_executor.go:849-971）。
5. 异步规划栅栏：planRequestSeq/planInputsEpoch/指纹三重校验防"失效窗口结果复活已清游标"（queue :79-92）。
6. 终态吸收性与缓存单调性：Acked/Abandoned 后回退会破坏 minNonTerminalToken/unresolvedCount 单调假设。
7. 坐标系统混用：HandoffFrontier（渲染行）≠ ledger token/display range；trim/重定基不可混用。

### 1.5 P1-1 第 1 步实施记录（InFlight/claim → 单飞写游标，已完成）

- 设计修正（相对 §1.2 的"executor 私有游标"）：planner 的 InFlight 安全点
  （展示载荷变化时 invalidate 而非 rebase）与批量 ack 校验需要 reducer 侧可见，
  因此落地为 `HistoryEffectQueueState.WriteCursor`（单调标量，0=无）。旧协议由
  `hasOlderPendingOrInFlight` 保证同一时刻至多一个 InFlight，结构上等价。
- ledger（history_commit.go）：删除 `MarkInFlight`/`DeferInFlight`；`Ack`/`Fail`
  改为 Pending → 终态并各自维护 `pendingCount`；`Invalidate(token, mayHavePartiallyWritten)`
  由队列传入"是否持有游标"决定未决语义（部分写标记 + unresolvedCount）。
  `HistoryCommitInFlight` 常量保留但生产不再进入（step 2 六态归一删除）。
- queue（history_effect_queue.go）：`markInFlight` 语义 = 设置 WriteCursor
  （frozen/projection/generation/ordering/单飞校验，幂等重领）；`deferInFlight` = 清游标；
  `invalidate`/`ack`/`fail` 成功路径清游标；`ackBatch` 以"head==cursor、其余 Pending"
  校验并逐条推进游标；`rebasePending` 跳过游标 token；`hasInFlightActiveOriginDelivery`
  改判游标 token（finalize 延迟护栏）。
- reducer/planner：`historyCommitGate` 增加 WriteCursor 投影，`historyCommitClaimCurrent`
  改判 "Pending && cursor==token"；`syncHistoryEffectCandidates` 对游标 token 走旧
  InFlight 分支（载荷变化即 invalidate）。
- executor/snapshot：`terminalSessionClaimedBatchLocked` 的 claimed 判据改
  "Pending && WriteCursor==token"；`releaseClaimMiss` 语义不变（Deferred 清游标）。
- 测试重写面：history_commit_test（fixture 去 MarkInFlight、Invalidate 签名/语义）、
  queue/planner/executor/scrollback/settled 断言改"Pending+游标"；
  `TestFinalizeDefersTranscriptPlanWhileActiveBatchInFlight` 暴露并修复了
  finalize 护栏漏判（hasInFlightActiveOriginDelivery 未含游标）。
- 验证：ui 全量 + commands 全量 + 定向 -race（见提交记录）。

### 1.6 P1-1 第 2 步实施记录（六态归一，已完成）

- enum（history_commit.go）：`HistoryCommitQueued` / `HistoryCommitDelivered` /
  `HistoryCommitQuarantined` 三态；删除 `InFlight` / `Failed` / `Invalidated` /
  `Abandoned` 常量。物理写窗口不占状态（由 `WriteCursor` 表达，第 1 步已落地）。
- 不可合并的四个行为差异收敛为 quarantine 子类 `HistoryCommitQuarantine`
  （None/Failed/Invalidated/Settled），由 `HistoryCommitEntry.Unresolved()` /
  `BlocksRemint()` 两个谓词统一裁决：
  - Failed：恒未决、settle 前不可压缩、阻断同源再铸；
  - Invalidated：partial 时未决且阻断再铸；非 partial 已解决、可压缩、不阻断；
  - Settled：settle 后不再未决、可压缩，但永久阻断同源再铸。
  `unresolvedCount`、`minNonTerminalToken`、`prunableResolvedEntry`、
  `hasTerminalRecordForSource`、压缩 tombstone 全部改为谓词驱动，语义与旧六态逐一对照。
- queue：`markDeliveredBatchUnresolved` 按 Queued→Failed 隔离、Invalidated 保留子类
  并强化 partial 事实；`Summary` 读数改为 queued/delivered/quarantined
  （+quarantined-unresolved/failed/settled），`OldestInFlightToken/Generation`
  改为 `ClaimedToken/Generation`（直接取 `WriteCursor`）；
  `hasInFlightActiveOriginDelivery` 更名 `hasClaimedActiveOriginDelivery`
  （删除恒空的 legacy activeTokensByCell 扫描）。
- planner：`syncHistoryEffectCandidates` 删除 InFlight 分支（游标 token 在 queued
  分支内 invalidate 的安全点不变）；`advanceActiveCellEnqueuedFromEffects` 只把
  Queued/Delivered/failed-quarantine 计入 Enqueued 前沿（与旧
  Pending/InFlight/Acked/Failed 集合一致）。
- 观测面：`/debug` history_gates 改 `queued_count`/`oldest_queued_token`/
  `oldest_queued_generation` + `claimed_token`/`claimed_generation`；
  document 摘要行改 `queued/delivered/quarantined(-unresolved/-failed/-settled)`
  与 `claimed-token/claimed-gen`；`chat_resume_progress` 的收尾判据改 queued==0。
- 错误常量 `ErrCommitNotPending`/`ErrCommitNotInFlight` 保留原名（分类稳定），
  注释标明二者在三态下均表示"非 queued"。
- 验证（`1e8154d6`）：`go test ./cmd/aicli/ui/` 全量 + `./cmd/aicli/commands/` 全量通过；
  定向 `-race` 通过。全包 `-race` 仅
  `TestArmedResumeDeliversWholeTranscriptAcrossBudgetTruncation` 因 60s 收敛期限在
  race 插桩下超时，基线 `d8ec19ca` 同一用例同样失败（非本轮回归）。

### 1.7 P1-1 第 3 步实施记录（pendingCount/minNonTerminalToken 游标化，已完成）

- `pendingCount` 删除：空队列判据改由队列头指针 `queueHeadToken`（最小 Queued
  token，0=空）直接表达；`HasPending()` 不再维护与状态转移并行的计数镜像。
  外部构造的 ledger（测试直写 `byToken`）仅在 `len(tokens) != len(byToken)`
  时走扫描兜底（与 `orderedTokens()` 同一检测信号），生产路径保持 O(1)。
- `minNonTerminalToken` 更名 `queueHeadToken`（队列头指针），与
  `HistoryEffectQueueState.WriteCursor`（交付游标）配对；`advanceQueueHeadAfterTerminal`
  在 Queued→终态时向前跳过终态 token（摊还 O(1)），`hasOlderPendingOrInFlight`
  更名 `hasOlderQueuedToken`（claim 排序护栏读头指针）。
- 顺带删除死代码 `nextNonTerminalTokenByScan`；`nextNonTerminalToken` 更名
  `nextQueuedToken`；诊断 trace 的 pending 计数改走 `QueuedCount()` 扫描
  （仅 AIR_TRACE_HISTORY 开启时执行）。
- 回归钉：新增 `TestHistoryCommitLedger_QueueHeadCursorMatchesScan`，对乱序入队、
  中间取消、头指针推进、settle 隔离、Clone 与外部构造 ledger 逐状态比对
  `HasPending`/`hasOlderQueuedToken` 与全表扫描；barrier-flip 测试改用
  `QueuedCount()` 断言。
- 验证：ui 全量 + commands 全量通过；定向 `-race` 通过。

## 2. P1-2 可推导镜像收敛

### 2.1 镜像字段（14 项盘点，关键处置）

| 字段 | 位置 | 事实源 | 处置 |
|---|---|---|---|
| `historyTailCells` | terminal_session.go:351-357 | **非镜像**（见修订） | **撤回删除**：ledger `activeTokensByCell` 是"未终态 token"索引（ack 压缩即删），而本字段是"本 writer 已物理交付 cell"证明（投影清除才重置），用于丢失 ledger 证明后的重投递裁剪；直接替换会双向漂移（漏裁/误裁）。改列 P1-2b：ledger 需新增"投影重置后已交付 cell"索引方可替换，否则永久保留 |
| `LayoutGeneration` | app_state.go:43 | `Geometry.Generation`（105/108/109/473 全同值） | **删除**（访问器，P1-2a） |
| `historyPrepareHits/Misses` | terminal_session.go:382-383 | 无生产消费者（仅测试断言） | **暂缓**：6 个测试以其为缓存命中/失效的唯一观测点，直接删除会削弱 2 个测试的断言特异性；待 /debug 导出或测试改用 preparedHistory 身份断言后再删 |
| `historyStreamTailRows` | terminal_session.go:345-350 | ledger Acked 范围 + AckFrame（history_commit.go:128） | 降级：writer 私有证明，改 ack 推导（P1-2b） |
| `historyTopAligned` | terminal_session.go:358-364 | "已有行进滚动区"（stream tail 非空 + resident 空） | 降级：派生 + 不变量测试（P1-2b） |
| `historyTailRows` | terminal_session.go:344 | 物理已写缓存 | 保留（writer 私有，禁止回喂 reducer） |
| `preparedHistory` | terminal_session.go:298-308 | (commits,theme,width) memo | 保留（确保可重建/失效点完整） |
| `PaintTrace` | renderengine/paint_trace.go:69-134 | 纯观测 | 保留，钉死 /debug 专用 |
| presenter `lastWidth/lastHeight` | terminal_session_presenter.go:36-38 | AppState.Geometry | 降级：改读 AppState（P1-2b） |
| `activeBandGeneration` | fixed_bottom_surface.go:203 | legacy transient 栅栏 | unified 删除，legacy 标注 compat-only |
| `TerminalSession.generation` | terminal_session.go:337 | frame.LayoutGeneration 采纳值 | 保留（写入门闩，已确认单写点 1115） |

### 2.2 几何收敛（6 存储 + 4 raw probe → 1 probe + 1 权威）

- 现状：`Terminal` 缓存（terminal.go:17-18，probe :354/driver:75/:162）、`FixedBottomSurface.lastWidth/lastHeight`
  （fixed_bottom_surface.go:756，SyncTerminalGeometry :741-784）、presenter 去重镜像（:36-38）、
  `primaryTerminalGeometry` 旁路直读 `term.GetSize(os.Stdout)`（chat_interaction.go:725-747 + theme.go:292-307）、
  `AppState.Geometry`（唯一 reducer 权威，controller_state.go:53-60）、`TerminalSession.geometry`（只读采纳 :1114）。
- 目标：driver 单 probe + `AppState.Geometry` 单权威 + writer 只读采纳；删除 presenter 去重镜像、
  chat 侧 raw GetSize 调用点、unified 模式 surface 上报。改动面约 4 文件
  （terminal_session_presenter.go、chat_interaction.go、fixed_bottom_surface.go、theme.go）。
- 广播路径保留：presenter.publishGeometry → controller.TryPost(Resize{Applied:true}) → reducer（generation 单调防回退）
  → FlushEffect → executor barrier 后组帧 → TerminalSession 采纳。

### 2.3 帧号收敛（10 计数器/16 写入点）

- `TerminalSession.frame` 仅两处自增（terminal_session.go:1168 viewport、:1328 history-only）→ 抽 `confirmWriteLocked()` 单点。
- 其余 9 个计数器不合并，只更名隔离语义：`pumpFrames`、`eventSeq`、`bindingGeneration`、`cacheGeneration`、
  `bandPaintGeneration`、`paintTraceFrames` 等，并在文档声明"非 writer frame 身份"。
- `LayoutGeneration` 删除后由 `Geometry.Generation` 派生访问器提供。

### 2.4 before/after 计数（可脚本化 rg）

- before：`historyStreamTailRows` 12 行/1 文件；`historyTailCells|historyTopAligned|preparedHistory` 33+10 行；
  几何关键词 43 文件（19 生产）；`frame++|frames++|generation++|Generation++` 10 行/7 文件（含 seq/nextGen 16 写入点/10 文件）。
- after 目标（修订）：删除 1 项（`LayoutGeneration`，P1-2a 已落地）；撤回 1 项（`historyTailCells` 非镜像）；
  暂缓 1 项（prepareHits/Misses，待 /debug 导出或测试改造）；unified surface 上报删除归入 P1-2b；
  降级 4 项（streamTailRows、topAligned、presenter 镜像、PaintTrace）；事实源 = AppState.Geometry + TerminalSession.frame + ledger。

### 2.5 P1-2a 实施记录（已完成）

- `LayoutGeneration` 字段删除：生产 8 文件（app_state/controller/controller_state/app_layout/history_effect_planner/
  history_trace/terminal_session_snapshot/history_plan_diagnosis + commands 2 处 debug 读点）全部改读
  `state.Geometry.Generation`；写入点（Resize/主题）删除，字段不再抄写。
  测试 20+ 文件机械同步（70 处 `state.LayoutGeneration` → `state.Geometry.Generation`；AppState 字面量删除冗余字段，
  全部与既有 `Geometry.Generation` 同值）。
- `historyTailCells`：复核后撤回（见 §2.1）。
- `historyPrepareHits/Misses`：复核后暂缓（见 §2.1）。

### 2.6 P1-2b 第 1 小步实施记录（streamTailRows/topAligned 降级，已完成）

- 复核结论：`historyStreamTailRows`/`historyTopAligned`/`historyTailCells` 均为
  `TerminalSession` 私有字段，快照只导出 `HistoryRows: len(historyTailRows)`，
  不存在回喂 reducer 的镜像路径；审计的「可推导镜像」判定按 §2.1 修订为
  writer 私有 ack 证明，本次不删除（删掉会丢 archived 行的 dedup 证明）。
- 降级落地为不变量钉（新增 `terminal_session_stream_tail_test.go`，3 项）：
  1. `TestTerminalSessionStreamTailProofTracksAckedWrites`：acked 写入 → 有界
     （上界 outputBottom）后缀 append；active 归档只动 stream tail 不动 resident
     模型；finalized 溢出后 `topAligned` 置位并 sticky；显式 `resetScrollback`
     整族失效（tail/cells/topAligned 清零、投影已知位重建）。
  2. `TestTerminalSessionStreamTailProofResetsOnPartialWrite`：半写后
     stream tail / provenance / topAligned / 投影已知位一起复位，
     `historyInsertionContinuesScrollback` 不得再声称续接。
  3. `TestTerminalSessionStreamTailAppendOnlyAcrossDeliveries`：同 projection 内
     成功交付只追加、不重写已证明行（dedup 只裁本批 payload）。
- 事实源声明：stream tail 的事实源是「本 session 物理写成功的行」（ledger ack
  在 writer 之外不可见 archived 行，故不能由 ledger 重建）；resident tail 的事实源
  是 region 模型；两者交集之外正是 active 归档行。

### 2.7 P1-2b 第 3 小步实施记录（frame 单点，已完成；先于几何收敛落地）

- `TerminalSession.frame` 两处自增（viewport 事务、history-only 交付）收敛为
  `confirmWriteLocked()` 单点：成功物理写后 +1 并返回，deferred/失败不推进。
  新增 `TestTerminalSessionWriterFrameIsSingleAllocationPoint` 钉住
  「viewport 与 history-only 共享同一帧号序列、stale Deferred 不推进、半写失败
  不推进」。
- 更名隔离复核：计划提到的 `pumpFrames`/`paintTraceFrames`/`bandPaintGeneration`/
  `cacheGeneration`/`eventSeq` 在当前代码中已不存在；唯一同名语义是
  `render/output.bindingGeneration`（lease 绑定代），已天然隔离，无需改名。
  隔离语义以 `confirmWriteLocked` 注释声明：其他 generation/frame 计数是
  scheduler/cache/lease epoch，禁止与 `s.frame` 比较。

### 2.8 P1-2b 第 2 小步（几何收敛，第 1 部分已完成）

- presenter 去重镜像删除：`TerminalSessionPresenter.lastWidth/lastHeight` 删除，
  去重改读 reducer 权威 `UIController.Geometry()`（controller.go 新增窄访问器，
  与 `LayoutGeneration()` 同款短 `c.mu` 读）；`probePending` 保留（投递重试
  标记，非几何镜像）。满邮箱延迟重发语义不变（钉子测试通过）。
- `primaryTerminalGeometry` 无 surface 时 fail-closed：删除 `theme.go` raw
  `term.GetSize` 回退，探针只认 surface 缓存；unified 探针不再是第二个
  GetSize 权威。
- unified surface 上报门控：`maybeRefreshStreamGeometryLocked` 与
  `refreshActiveStreamViewportNow` **保留 surface 探针**（刷新 presenter 读取的
  缓存尺寸与 legacy 布局簿记），仅把「直接上报」`reportMeasuredSurfaceGeometryLocked`
  在 unified 下门控；unified 的 Resize 统一经 presenter probe → AppState.Geometry
  链路，legacy 路径与上报本体不变。`unifiedRendererEnabledLocked()` 免锁变体
  避免持 `c.mu` 时重入死锁。
  （修正记录：初版曾把探针一并门控——而该探针是当前 unified 下唯一的尺寸变化
  探测点（driver 单 probe 尚未落地），会导致 resize 失效；已改为仅门控上报。）
- 验证：ui 全量（128.4s）+ commands 全量（174.3s）+ 定向 `-race`
  （presenter/geometry）通过。另一次 commands 全量（181.7s）暴露既有 flaky 用例
  `TestPrintVisibleChatHistory_UnifiedHandoffsOverflowedCanonicalHistory`：
  基线 `d8ec19ca` 单测即可 1/12 复现（当前 2/12、全门控版 0/12，样本内不可区分），
  与本轮改动无因果证据；复跑全绿。
- driver 单 probe 与展示宽度缓存转发已在 §2.10 落地；本小步剩余：unified
  下 `ApplyGeometry` 纯采纳入口（见 §2.10 末条）。

### 2.9 验证中发现的既有竞态修复（executor WaitIdle/Request，已完成）

- 症状：commands 全量负载下
  `TestSuccessfulRequestBoundaryPreservesFortyLineFinalInNativeHistory` 以约 1/6
  概率 panic `sync: WaitGroup is reused before previous Wait has returned`
  （栈：`TerminalSessionExecutor.WaitIdle` ← presenter.WaitIdle ←
  `awaitUnifiedPresenterIdle`）。
- 根因：`TerminalSessionExecutor`/`HistoryCommitExecutor` 用 `sync.WaitGroup`
  做 idle 等待；`Request` 可在 `WaitIdle` 的 `Wait` 观察到计数 0 的同时执行
  `Add(1)`，而 WaitGroup 明确禁止「Wait 在途时复用」。
- 修复：删除 wg，改为 done 通道代际等待（`waitWorkerIdle` 循环：在 `e.mu` 下读
  running/done，running=false 才返回；`finishWorker` 在同一临界区置
  running=false、清 done、close）。HistoryCommitExecutor 同步改为 done 通道
  （其 run 原先无通道）。
- 回归钉：新增 `TestTerminalSessionExecutorWaitIdleRequestRace`（25 轮
  WaitIdle×Request 并发）；原 flaky 用例 20 连跑 0 失败。A/B 对照：基线
  `d8ec19ca` 1/12、全门控版 `8d8ae0fe` 6/12、修复后 0/20。
- 验证：ui 全量 128.4s + commands 全量 174.3s 通过。

### 2.10 P1-2b 几何收敛第 2 部分（driver 单探针 + 展示宽度缓存转发，已完成）

- `TerminalDriver.ProbeSize()` 成为 ui 包唯一 raw `term.GetSize` 探测点（经
  测试接缝 `driverSizeProbe` 注入）：探测成功写能力缓存并发布进程级尺寸备忘
  （`terminal_size_cache.go`），失败不污染缓存；`RefreshCapabilities` 复用
  `ProbeSize`。
- `TerminalDriver.Size()` 改为纯缓存读（不再 syscall）；`Terminal.updateSize`
  与 `FixedBottomSurface.refreshTerminalDimensionsLocked` 改走 `ProbeSize`。
  `RefreshSize` 的 `sizeProbeCount` 预算语义不变（surface 夹具断言 1 次/操作）。
- `GetTerminalWidth/Height/Size`（theme.go/terminal.go）改为进程级缓存转发：
  优先读最近一次真实探测结果，无缓存时做一次 raw 探测并发布；管道/无 TTY 的
  80x24 兜底不发布。30+ 展示宽度调用点（info/output/inputbox/separator/
  welcome/chat_* 等）经该入口收敛，不再逐次探测。
- 钉子：`terminal_size_cache_test.go` 2 项（单探针 + 缓存转发 + `Size()` 零
  syscall + 探测失败不污染缓存）。
- 验证：ui 全量 110.8s（单独运行）+ commands 全量 185.5s 绿。并发双包时
  `TestArmedResumeDeliversWholeTranscriptAcrossBudgetTruncation`（单独 98.7s
  通过，内部收敛期限对负载敏感）超时未收敛一次；单独复跑全绿，属既有负载
  敏感用例，与本改动无因果证据。
- unified 纯采纳入口复核（本项关闭）：unified 帧几何唯一来源是
  `ComposeAppRenderFrame`/`ComposeTerminalFramePlan` 从 `AppState.Geometry`
  纯派生（`app_render_frame_test.go:34` 已钉 `frame.Geometry ==
  state.Geometry`），`TerminalSession.Flush` 逐值采纳 `frame.Geometry`
  （terminal_session.go:1114），零几何由 `TerminalFramePlan.Valid()`
  fail-closed，全路径无 probe 回退——无需新增独立 `ApplyGeometry` API。
- 保留：`chat_setup.chatTerminalWriterWidth` 的 writer 专属 GetSize（语义是
  给定 writer 的宽度，不是进程终端）。

## 3. P1-3 WaitIdle 事件驱动 ack

### 3.1 现状（关键行）

- `UIController.WaitIdle` controller.go:892-901（cond 无界）；`WaitIdleTimeout` :906-925（**1ms 轮询 :924**）。
- `TerminalSessionExecutor.waitControllerIdle` :799-804（2s 有界，:795 常量；注释 :785-794 记录 33 分钟钉死现场）。
- `HistoryCommitExecutor.runOne` 内 **5 次无界 `controller.WaitIdle`**（:140/:155/:166/:182/:192；仅测试构造，生产主链是 TerminalSessionExecutor）。
- `coordinator.waitUIActorIdle` chat_ui_actor.go:1244-1249（无界）；`waitUIActorIdleBounded` :1261-1271（5s fail-closed）。
- 1ms 轮询点：controller.go:924；chat_ui_actor.go:479/:602；chat_runtime_events.go:1961/:1983/:2577/:2983。

### 3.2 三连 Wait（改造对象）

1. `TerminalSessionExecutor.runOne` 生产每轮：847（读 schedule）→ 1007 Post claim → 1012（claim 生效）→ 1070-1072 事务 → 1183 收尾；
   分支：1155/1159/891/834。
2. `writeDirectInteractiveOutput` chat_surface_output.go:489→492→497→501（依赖"更早 post 全部落地"）。
3. 屏幕/租约链：chat_screen_framework.go:398-402、415-417；chat_transcript_pager.go:51-55。
4. `HistoryCommitExecutor.runOne`（§3.1）。

### 3.3 ack 设计草案（最小改动面）

- ack 源 = `UIController.Run` 自身：apply 完成 + state 发布（controller.go:604）后登记"action 已应用"；
  批交付 `delivering=false`（:642-647）后登记"已可见"。新增 per-revision waiter 注册表 + `WaitActionApplied(target, timeout)`。
- 丢弃/合并路径必须立即 ack：Post 被拒（:331-334/:359-362）、coalesce 合并（:335-344）、plan worker Post 被拒
  （controller_plan_worker.go:104-107）。
- 替换点：executor 847/891/1012/1155/1159/1183/834 改"等刚 Post 的 revision"（834 必须等 Deferred revision 保 rebase 可见）；
  HistoryCommitExecutor 五处改 `PostAndAwait`；bridge :2937-2983 改三态 ack（admit/merge/drop）；
  backlog/deferred（1961/1983/2577）等的是队列非 actor，保留。
- 锁边界：waiter 登记与完成判定在 `c.mu` 内；唤醒在解锁后；ack 必须晚于 state 发布，flush 类等待包含 `delivering=false`。
- 保留有界超时（ack 丢失兜底）：presenter CloseTimeout 3s、executor 2s、`waitUIActorIdleBounded` 5s、
  bridge uiActionPostBudget 5s、defer drain 1500ms、退出 flush 300ms、plan worker timeout。

### 3.4 风险

- 死锁：无界 WaitIdle 在 actor followup 反等 worker 时互锁（生产 executor 已因现场改 2s）。
- 丢 ack：coalesce/拒绝路径不显式 ack → waiter 只能等超时。
- 语义变化：per-action ack 不保证 FIFO 前驱已交付 → 必须用"revision ≥ 前驱"栅栏（prompt 重影回归防线）。
- delivering 边界：ack 早于物理帧写入会让 presenter/geometry/flush 类等待失真。
- lease/held 交互：screen open 等待必须在 lease transition 发布后 ack（chat_screen_framework.go:24/:173/:387-405）；
  resize/theme 与 claim 交错的 ack 栅栏对应"快照可读"而非"仅出队"。

### 3.5 P1-3 第 1 小步实施记录（controller 侧 waiter，已完成）

- `PostTracked`/`TryPostTracked`/`PostDeferredTracked`：三态 outcome（admitted/merged/dropped）+ ticket；
  merge 时槽位 ticket 提升到 max（旧 poster 的 ticket 也被同一 apply 释放）；`Post`/`TryPost`/`PostDeferred`
  改为薄包装，投递语义与计数器口径不变。
- `queueTickets` 与 `queue` 在同一 reslice 点同生共死；`appliedTicket`/`visibleTicket` 单调水位：
  applied = reducer 返回且 state 已发布（controller.go Run 内 reduce 之后）；visible = 批内全部 effect（含 flush）已交付。
- `WaitActionApplied(ticket, timeout)` / `WaitActionVisible(ticket, timeout)`：channel 唤醒（无轮询、无辅助 goroutine）；
  waiter 表在 `c.mu` 下登记/释放；超时自摘除并复核水位（竞态不丢成功）；Run 在 close-drain 完成后 abort 未决 waiter。
- 钉测试 7 项（`controller_action_wait_test.go`）：admitted/merged/dropped、deferred、无 Run 超时、
  visible 等待 effect 交付、merge 双 ticket、满 mailbox 三态。
- 第 2/3 小步（executor 票据栅栏 + bridge 三态 ack）已完成，见 §3.6。
- 相邻修复（本次一并落地，commands 包）——「在途窗口」在单车道重构后残留的两处缺口：
  1. `trySendStreamEvent` 改为**先记账后入队**：原 send→account 窗口内消费者已出队并进入
     写帧，并发 delta 读到 `eventQueueBytes==0` 而直投；
  2. 新增 `streamWriteInFlight`（run() 处理流事件期间置位）：写入在途时（a）新 delta 一律
     合并进 backlog、（b）backlog worker **不得把流式头槽提升进 bounded queue**——
     否则合并面（backlog）被搬走，卡顿被裂成多次小重绘。
  症状：全量负载下 `TestChatRuntimeEvents_CoalescesStreamingDeltasWhileQueueBacksUp` 三连败
  （期望 `["Hello"," world!"]` 实得 `["Hello"," world","!"]`，隔离通过）；修复后该测试
  5x、`TestChatRuntimeEvents` 子集 3x 通过，全量复跑见提交记录。与 §3.3「合并/丢弃路径必须立即 ack」
  同域，属 bridge 第 3 小步的前置清障。

### 3.6 P1-3 第 2/3 小步实施记录（executor 票据栅栏 + bridge 三态 ack，已完成）

- executor（terminal_session_executor.go）：
  - `postControllerActionTracked`（全部 14 处 `e.controller.Post` 改走 tracked）+ `lastControllerTicket`；
    7 处 `waitControllerIdle`（847/891/1012/1155/1159/1183/834）迁移为票据栅栏。
  - 834/891/1012/1155/1159/1183 等"自己最近一次 post 已 apply"（`waitLastControllerAction`）；
    **847 读 schedule 前需可见"调用时刻已接受的全部 action"**（种子/替换快照异步 apply），
    用 `waitControllerAcceptedApplied`（`LastAcceptedTicket` + `WaitActionApplied`，finite 集合、
    事件驱动、2s 有界）。直接改 own-ticket 会让
    `TestPrintVisibleChatHistory_UnifiedHandoffsOverflowedCanonicalHistory` 在全量负载下
    稳定复现 17 个 entry 全 Pending（快照未 apply 即读 schedule 后退出且无后续唤醒）；
    修复后该测试与 commands 全量复跑绿，并新增钉测试
    `TestWaitControllerAcceptedAppliedWaitsForPriorAccepts`。
  - `WaitIdleTimeout` 生产调用点清零（仅剩测试使用）；生产路径 1ms 轮询消除。
- bridge（chat_runtime_events.go）：`postRuntimeEventToUIActorWithEpoch` 改
  `tryPostUIActionTracked` 三态（admitted/merged=送达；dropped=满/关闭）；满邮箱等待改事件驱动：
  `LastAcceptedTicket()` + `WaitActionApplied(remaining)`，删除 `time.Sleep(1ms)` 轮询；
  critical 重试 / 非 critical 有界丢弃语义不变。
- controller 增补：`lastAcceptedTicket` 仅由 accepted（admitted/merged）推进，dropped 票据
  永不悬挂；`LastAcceptedTicket()` 作为容量栅栏水位；钉测试
  `TestLastAcceptedTicketIsTheCapacityFence`。
- 验证：`go build`；ui 全量 + commands 全量（此前两连败的同一负载）；定向 `-race`
  （controller waiter/executor 栅栏/bridge 三态），见提交记录。

### 3.7 轮询消除核查（1ms 轮询点清单复核，已完成）

- 已消除：`chat_ui_actor.go` 的 prompt input / editor status 两个 dispatch
  flusher 由 1ms 重试改 `waitUIActorCapacity`（`LastAcceptedTicket` +
  `WaitActionApplied` 250ms 分片唤醒；循环语义不变——admitted/closed/shutdown
  才退出；无 accepted ticket 的防御回退保留 1ms）。既有两个「NeverWaitsFor
  FullMailbox」契约测试复跑通过。
- 保留（队列等待或有界超时兜底，非 actor idle 轮询）：
  - `controller.go:852`（`WaitIdleTimeout` 内部）：仅经保留的有界栅栏触达——
    退出 close drain（chat_ui_actor.go:1014）、legacy 有界辅助
    （chat_runtime_events.go:4787、chat_surface_output.go:538 300ms）、
    `waitUIActorIdleBounded` 5s（生产 7 处，§3.3 明确保留）。
  - `chat_runtime_events.go:1981/2003/2597`：backlog/deferred/字节预算的队列
    等待，§3.3 明确保留。
  - `chat_runtime_events.go:3030`：bridge 邮箱满事件驱动等待的防御回退
    （无 accepted ticket 时）。
- 结论：actor idle 等待路径（controller waiter / executor 票据栅栏 / bridge
  三态 ack / 两个 dispatch flusher）已无 1ms 轮询；剩余均为队列等待或有界
  超时兜底（保留项）。
- 验证：commands 全量 166.8s 绿；两个「NeverWaitsForFullMailbox」契约测试
  定向通过。

## 4. 实施顺序与回滚

1. **P1-2a**（已完成）：`LayoutGeneration` 字段删除；`historyTailCells` 撤回、`historyPrepareHits/Misses` 暂缓（见 §2.1/§2.5）。
2. **P1-3**（2-3 提交）：`WaitActionApplied` + Post 通知（controller.go，**第 1 小步已完成**，见 §3.5）
   → executor 五处替换 → bridge 三态 ack。
3. **P1-1**（4 提交，每步先加钉测试再删实现）：InFlight/claim → 六态归一 → 计数器游标 → 规划续跑组（后置，依赖规划单线程化）。
4. **P1-2b**（2-3 提交）：streamTailRows/topAligned 派生 → 几何收敛 → frame 单点 + 更名隔离。
- 每步独立可回滚；任一步 `-race` 或长会话验收不过即回滚该步，不带病前进。

## 5. 验收矩阵（P1 行）

| 项 | 命令/证据 | 目标 |
|---|---|---|
| 状态字段缩减 | §2.4 before/after 计数（rg 脚本）+ 删除特例清单 | 字段数按计划下降 |
| 竞态 | `go test ./cmd/aicli/ui ./cmd/aicli/commands -race` | 全绿 |
| 长会话 | 长会话 marker exactly-once（≥5k cells soak / resume-replay 矩阵） | 无重复/丢失/异常空行 |
| 既有回归 | 两包全量 + P0 门禁命令（写端清单/单写端/composer 出口） | 全绿 |
| 轮询消除 | §3.1 的 1ms 轮询点清单 | actor idle 等待无 1ms 轮询（保留项除外） |

### 5.1 全包 -race 验收执行记录（2026-10-06）

- 命令：`go test -race -count=1 -p 1 -timeout 3000s ./cmd/aicli/ui/ ./cmd/aicli/commands/`
  （日志 `E:\tmp\p1-race.txt`；ui 216.0s / commands 363.2s）。
- ui 包：2 失败 = 1 已修复 + 1 已知基线。
  1. `TestAsyncPlanWorkerConvergesResumedSessionThroughActor`（DATA RACE，0.11s）：
     P1-3 新增的 `TestLastAcceptedTicketIsTheCapacityFence` 未 join Run
     goroutine，测试结束后 reducer 仍在 `history_effect_planner.go:288` 读包级
     `historyCommitPlanningBudget`，与后续 plan-worker 测试写该变量竞争。
     **已修复**：`startControllerRunForTest`/`joinControllerRunForTest`
     （Close + 5s 有界 join），同文件 5 个用例全部接入。
  2. `TestArmedResumeDeliversWholeTranscriptAcrossBudgetTruncation`
     （153.0s 超时）：已知基线（§1.6：基线 `d8ec19ca` 同用例在 race 插桩下
     同样超时）。
- commands 包：5 用例失败、57 个 DATA RACE 报告，聚为 3 组竞争；命中代码区域
  均非本轮 P1 改动点（function_catalog/skills/chat_mesh 不在本轮改动清单，
  uiActor 字段读写点亦未触碰），但无基线 race 日志，按「既有嫌疑」修复：
  1. **function catalog（约 55 报告，已修复）**：后台
     `scheduleSkillsRuntimeRefresh → buildSkillsRuntimeBindingFromManager →
     registerFunction` 写 `entries`/registry 映射，请求线程
     `Stats()/syncFromRegistry/sharedCapabilityCatalog/clone*` 并发读；
     `function_catalog.go` 原全文件无锁。修复：catalog 增加 `sync.RWMutex`，
     写入口（register/Prune/Remove/Set*/ensureFunctionCatalog）全写锁；读路径
     改「锁内快照 + 锁外查询」（syncFromRegistry/BuiltinSchemas/SkillSchema/
     Names/Descriptor(s)/Stats/sharedCapabilitySnapshot）；Select/Execute 锁内
     只取引用与快照，慢速/可重入外部调用全部移到锁外。验证：hot-reload race
     ×2 零 DATA RACE、定向 race 集绿、commands 全量 181.3s 绿。
     残余风险收口（本轮）：新增锁内读访问器（`entryForRead`/
     `entriesSnapshot`/`skillFunctionForRead`/
     `executableSkillFunctionNamesForRead`/`registeredFunction`/
     `listRegisteredFunctions`/`unregisterRegisteredFunction`/
     `executeRegisteredFunctionWithMeta`），全部生产直读点已收口
     （command_invoke ×3、chat_tool_availability、chat_mcp_session_scope ×3、
     chat_skill_tool_surface ×2、chat_actor_executor ×1）；新增
     `TestAICLIFunctionCatalog_AccessorsRaceHotRegister` 栅栏（热注册 × 全
     访问器并发，`-race` 零报告）。
  1b. **skillsRuntimeBinding 热刷新原地写（本轮修复）**：
     `buildSkillsRuntimeBindingFromManager` 的 reuse 分支整体替换
     count/skillFunctions/skillFunctionsByPath/roots/exposure*/manager/
     mcpRuntime 等 12 个字段，请求线程原直读字段（约 25 处）无同步。
     修复：binding 增加 `sync.RWMutex` + 锁内快照访问器（Manager/MCPRuntime/
     Roots/ExposureMode/ExposureTopK/SkillFunctions/SkillFunctionForName；
     Count/Close/skillFunctionByPath/skillFunctionByName 内部加读锁；
     AnalyzeSkillExposure/orderedSkillFunctionNames/schemaForSkillFunction
     改锁内取快照），写路径在写锁内整体替换、释放后才取 catalog 锁（锁序
     catalog → binding，无嵌套）；全部读点（chat/mentions/completion/turn/
     skills command/reload/function_catalog ×3）已收口。新增
     `TestSkillsRuntimeBindingAccessorsRaceInPlaceRefresh` 栅栏（原地替换 ×
     全访问器并发，`-race` 零报告）。定向 race 集（热刷新真实路径 + mention/
     picker/catalog Select）绿；全包 race 终验 ok 361.7s 零报告（见提交记录）。
  1c. **既有 flake 修复（本轮顺带）**：
     `TestPrintVisibleChatHistory_UnifiedHandoffsOverflowedCanonicalHistory`
     等 4 处在 actor/presenter idle 后即时断言 `HistoryCommitDelivered`——
     两者都不覆盖 TerminalSessionExecutor 的异步 schedule；HEAD 上隔离
     `-race -count=20` 复现 2 次（非本轮引入）。修复：新增
     `awaitHistoryCommitDelivered`（显式 `executor.Request/WaitIdle` +
     ≤2s 有界轮询，与 terminal_projection 测试同一屏障）替换 4 处即时断言；
     `-count=10 -race` 全绿。
  2. **stdin 全局（1 报告，已修复）**：`replaceStdinWithNullDevice` 在 pump
     读循环存活期间恢复 `os.Stdin`，与 `chatStdinIsNullDevice`
     （chat_mesh.go:64）竞争。修复：`chatInputQueue` 增 `stdinLoopDone`
     （读循环退出时关闭，生产无消费者），测试侧 `joinStdinReadLoop` 在恢复
     全局前 join；两个 stdin 用例 `-race` 通过。
  3. **uiActor 发布（2 报告，已修复）**：`ensureUIActor` 在 `uiActorOnce.Do`
     内写 `c.uiActor` 与 `waitUIActorIdle/Timeout/Bounded` 直接读字段竞争。
     修复：`uiActorMu RWMutex` + `publishUIActor`/`currentUIActor` 访问器；
     ensureUIActor 走发布，全部生产读点（wait helpers/shutdown/bridge 热路径/
     debug document+HTTP/transcript pager/resume progress）改访问器，测试注入
     点改 `publishUIActor`。定向 `-race`（DiagnosticNotice 等 4 用例）通过。
- 结论：三组竞争全部修复。**最终门禁复跑**（同命令）：ui 230.2s 零 DATA
  RACE，仅剩已知基线 `TestArmedResume...`（161.3s 超时）；commands
  **ok 361.9s 全绿、零 DATA RACE**（修复前 57 报告 / 5 用例失败）。
  race 行验收通过（除已记录基线超时）。

## 6. 侦察报告归档

- ledger 报告：六态/队列标志/计数器全表 + 删除顺序 + 风险 1-7（§1 已蒸馏，原始 47KB 见会话 artifact）。
- mirrors 报告：14 项镜像表 + 几何 6 存储/4 probe + 帧号 10 计数器/16 写入点 + before 计数（§2 已蒸馏）。
- waitidle 报告：实现现状表 + 三连 Wait 全链 + ack 设计草案 + 风险（§3 已蒸馏）。
- 侦察批次工具白名单教训：explore 子代理需含 `ls`/`glob`，否则其探索习惯会触发 TOOL_DENIED 并终止运行
  （报告仍产出，但批次被标记 failed）。
