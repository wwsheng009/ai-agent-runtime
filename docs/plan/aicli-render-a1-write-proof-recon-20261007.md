# A1 写证明件侦察（2026-10-07）：现状盘点 → 行序交付游标 + 行级所有权状态机

> 性质：只读侦察 + 切片方案（不改代码）。三路并行：交付账/规划层、executor 帧/移交边界、
> partial write 覆盖。
> 依据：`docs/plan/aicli-render-remaining-defect-ledger-20261006.md` A1/A3 行；
> `docs/architecture/aicli-tui-renderer-architecture-design.md` §3.4/§3.6/§11-3；
> `docs/plan/aicli-ui-handoff-inflight-strand-hardening-plan-20261005.md` §525-546；
> P1-1 已落地基线：`docs/plan/aicli-render-p1-state-convergence-plan.md` §1.5–1.7
> （`WriteCursor`/三态归一/`queueHeadToken`）。

## 0. 目标形态（定稿引用）

- 交付账 = 已交付 finalized 行前缀单调游标 + 至多一个在途 claim（单飞写证明）（§3.4）。
- 写成功 → 帧与移交互斥由**状态迁移**保证；写失败/中止 → source-backed 恢复重绘 + 新 epoch；
  ledger 只记录已证明事实的身份（硬化 §543-546）。
- 设计倾向：不需要 per-commit 写证明（epoch 恢复取代事务回执，§11-3）；需验证 partial write
  场景（本侦察第 3 节给出覆盖缺口与用例矩阵）。

## 0.1 侦察基线校正（重要）

基线 HEAD `562daf5c`。台账 A1/A3 的部分依据在 A2 三路特例族收口后已**过时**，A1 需按当前
代码重新表述：

1. `1cace024` 的 `hasInFlightActiveOriginDelivery` / `hasClaimedActiveOriginDelivery` /
   `HistoryCommitInFlight` / Origin 面已随 `e3236de9`（A2 第二刀）整体删除，生产 grep 零命中。
2. A3 台账引用的 `activeAckedRenderedPrefixRows`（`history_effect_planner.go:331-367`）已随
   `130cc7f5` 删除；Ack 即置 `Lines=nil`（`history_commit.go:388`），已交付载荷增长已止血；
   剩余载荷面 = tombstone 集合 + Queued entry 载荷。
3. `TerminalEpoch` 全仓无生产推进点（仅投影复制）；恢复恒为 settle 就地隔离
   （`terminal_session_executor.go:1120-1124`），"epoch 恢复"目前是**待建能力**而非现状。
4. 1cace024 两个守护用例已改名/删除（`130cc7f5` 重写为 `a2_stop_minting_test.go` 语义反转用例），
   无 `-count`/`-race` 记录承接。

## 1. 现状：交付账/规划层

- **三态 + 事务子类**：`Queued/Delivered/Quarantined`（`history_commit.go:80-92`）+ Quarantine
  子类 `Failed/Invalidated/Settled`（`:100-107`）+ `MayHavePartiallyWritten` + `unresolvedCount`。
  迁移点：Enqueue `:266-309`；Rebase `:314-338`；Ack `:368-392`（`Lines=nil :388`）；
  Invalidate `:349-363`；Fail `:430-445`；批失配 `markDeliveredBatchUnresolved`
  （`history_effect_queue.go:607-639`）；Settle `:457-478`（reducer `controller_state.go:353-363`）；
  压缩 `compactResolvedIfLarge :578-598`（唯一生产调用 `queue.ack :532`）。
- **claim**：executor 读 schedule（`terminal_session_snapshot.go:35-65`）→ `BeginHistoryCommit`
  （token+LayoutGeneration，`action.go:431-437`）→ `markInFlight`（`history_effect_queue.go:440-470`）
  置 `WriteCursor`；快照二次核验（`terminal_session_snapshot.go:114-143`）。ack 要求
  `WriteCursor==token`（`:512-534`）；fail 清游标 + `ProjectionUnknown+ReconciliationRequired`
  （`:690-710`）；defer 释放（`:641-659`）；invalidate 以 `WriteCursor==token` 推定"可能已写"
  （`:496-510`）。
- **「已交付前缀」不存在单一游标**：`queueHeadToken` 是**未交付前沿**（反向，`history_commit.go:510-521`）；
  交付事实按身份查询（`hasTerminalRecordForSource :645-662` 等）；终端侧
  `historyTailCells/rows/streamTailRows`（`terminal_session.go:317-334`）是物理投影证明，
  partial 时整族复位（`:1226-1241`）。
- **tombstone/压缩**：门限 4096/2048（`:570-573`）；tombstone 粒度
  `(cell, revision, source range, fragment)`（`:194-200`），随会话单调增长、无聚合。
- **能/不能区分「已落盘/未落盘」**：Delivered = executor ack 证明；Deferred /
  Invalidated-non-partial = 零写证明；Failed（恒 unresolved）/ Invalidated-partial / 批量失配
  = 无证明；`SettleUnresolvedWithoutReplay` 显式放弃区分（no-replay 吸收）。
- **双协议**：生产 `TerminalSessionExecutor`（`terminal_session_executor.go:884-1094`，
  presenter 装配 `terminal_session_presenter.go:50-64`）；`HistoryCommitExecutor`
  （`history_commit_executor.go:38-60`）同协议但仅测试引用。

## 2. 现状：executor 时序门槛与恢复

- **1cace024 等价保护现状**：门槛删除后，保护 = planner 对游标 token 不 rebase、载荷变化直接
  invalidate（`history_effect_planner.go:841-849, 957-963`）+ generation 闸门→Deferred
  （`releaseClaimMiss :864-876`）+ ackBatch 失配隔离。**resident-tail 双写仍靠时序条件
  （写游标窗口）+ 会话侧 tail 去重止血，未由状态机保证**（与台账判断一致）。
- **门槛清单（摘要）**：executor 侧 E1/E2 墙钟退避（`:541-556, 625-635`）、E3 claim-miss 补偿
  （`:864-876`）、E4 ticket fence（保留）、E5 backoff 内继续投递、E6/E7 恢复/settle 回执条件；
  session 侧 S3/S4 历史可写性（`terminal_session.go:866, 908`）、S6 tail 去重
  （`:957-972, 1469-1488`）、S7 partial 清证明（保留）；queue 侧 Q1 五重 gate、Q2 游标推定
  "可能已写"、Q3/Q4 批量证明、Q5 Deferred、Q6 fail 未分离零写。
- **可删候选**：E1/E2（→proof record 结算）、E3（claim+snapshot 原子化后）、S3/S4（→claim
  Attached + 事务级 proof）、S6（ledger proof 落账后）、Q2-Q6 推定分支。
  **A1-2c 定稿判定（2026-10-07）**：六门槛全部转为「带前提的保留项」，本轮无删除项；
  逐条判定与前提见 §5 A1-2c。
- **必须保留**：generation 身份栅栏、排序护栏（`hasOlderQueuedToken`/`queueHeadToken`）、
  unresolved 未结算门（改 proof 谓词）、lease/freeze、S5/S7/S9 物理缓存与 partial 失效、
  gateway 字节事实、ticket ordering fence。
- **恢复现状**：unknown 入口 = Failed（含 stale 代）、stale ack、写中 invalidate、替换触碰
  acked（`controller_state.go:435-437`）等；source-backed 重建恒 settle、不清 scrollback、
  不重导；无新 epoch 发出者（历史 replay 路径已随 P2 S1–S4 删除）。

## 3. 现状：partial write 覆盖（§11-3 验收缺口）

- 写入路径：`writeTerminalBytesKindLocked`（panic→partial `:1639-1645`；probe bytes>0→partial
  `:1647-1650`）；gateway `submitWithPortLocked`（committed / failed_zero_bytes / unknown_partial
  `:1698-1738`）；presenter 短写→`ErrShortWrite` 不重试（`renderengine/presenter.go:71-95`）；
  sink 归一化（`render/output/types.go:365-430` / `physical_sink.go:346-450`）。
- 现有测试强断言组：session 层 short/panic/zero 全族（`terminal_session_test.go:1293-1741`）、
  executor 层（`terminal_session_executor_test.go:737/865/913/1176/1261`）、append-only 语义
  （`history_append_only_semantics_test.go:110`）、sink 故障注入族。
- **六个缺口**：
  1. DEC 2026 内失败无注入（`terminal_lock.go:78-81` begin/end 为裸 `os.Stdout` 写且忽略错误，
     `terminal_write_lock_sync_test.go:36-65` 仅 happy-path）；
  2. 历史区半写无行边界/行中间粒度 + 无 VT 屏级"半行未成行"断言；
  3. 合并事务无「frame 段完整、history 段被截」用例（现仅整批折半）；
  4. epoch/replay 事务中注入 short/zero/panic 无覆盖；
  5. executor 级 abort-during-history 无覆盖（仅 frame 级 CloseTimeout）；
  6. partial-write 组无 `-count`/`-race` 记录；1cace024 守护用例无承接。

## 4. 差距与切片方案（A1-1..A1-4）

| # | 切片 | 内容 | 前置 | 验收 |
|---|---|---|---|---|
| A1-1 | **partial-write 验收矩阵** | 6 个故障注入用例（§5 表 1–6）+ 守护重建（表 8）；真缺陷即 fail-closed 修复 | 无 | 新用例 + 既有 fail-closed 组 + `-count=40` + `-race` |
| A1-2 | **写事务记录（proof record）** | executor↔session 边界引入事务事实（Started/Committed/Aborted/UnknownPartial + 覆盖 token 集），invalidate/ack/fail 按事实分类；删 Q2-Q6 推定分支；settle 定稿为 proof 终态 | A1-1 | 门槛清单删除项逐条有替代迁移；fail-closed 语义不回退 |
| A1-3 | **行序交付游标** | ledger 正向 delivered-row cursor + claim 身份挂 range/fragment；规划只从游标之后；据此评估 Quarantine 子类/tombstone/ackBatch 删除面。**设计冻结见 `docs/plan/aicli-render-a1-3-row-cursor-plan.md`（2026-10-07）** | A1-2 | 无重复铸造/无丢行不变式 + 宽回归 + 真机 e2e |
| A1-4 | **A3 重估** | tombstone 聚合或随游标删除；更新台账 A3 依据（原函数已删） | A1-3 | 长会话载荷/内存核算 + 文档同步 |

## 5. A1-1 先红后绿用例矩阵

| # | 用例名 | 场景 | 断言点 |
|---|---|---|---|
| 1 | TestTerminalSessionDEC2026ShortWriteInsideSynchronizedFrame | DEC 2026 开启，begin 后截断 | end 恰一次、无悬挂 2026、partial 分类 |
| 2 | TestTerminalSessionPartialHistoryWriteMidRowVTState | 行中间截断，字节喂 vt.Screen | 半行不得显示为完整行、HistoryKnown=false、下一笔 Deferred |
| 3 | TestTerminalSessionMergedTransactionFrameFullHistoryTruncated | frame 段收满、history 段报错 | frame FullRepaint、history partial=true、tail 证明清除 |
| 4 | TestTerminalSessionExecutorEpochReplayPartialWriteSettlesFailClosed | resume/replay 事务中短写 | 不盲重放、不重复 epoch、marker 恰一次、obligation 清零 |
| 5 | TestTerminalSessionExecutorAbortDuringHistoryHandoffFailsClosed | 阻塞历史写中 Abort | entry Failed+partial、ProjectionUnknown、无重放 |
| 6 | TestTerminalSessionExecutorPartialWriteDuringLeaseReconcile | lease 期间短写 | queued token 保持 Pending、不得 partial ack |
| 7 | （基线）既有 fail-closed 组 | — | 保持绿 |
| 8 | 守护重建 TestFinalizeNoMintThenWholeSourceDelivery（HEAD 版） | finalize 无在途时全量恰一次 | `-count=40` + `-race` 记录 |

验证脚本：`go test ./cmd/aicli/ui/ -run '<1-6|8>' -count=1`（先红）→ 修复后 `-count=40`
与 `-race -count=1`，结果写入 plan/ledger。

### 5.1 实施记录（2026-10-07，A1-1 批 1+2 落地）

- **#1 DEC 2026 内失败**：不适用统一会话——DEC 2026 帧包裹是冻结的 legacy-only 路径
  （`renderengine/terminal_lock.go:31-34` 明示 unified sessions 禁止启用；属 P0 写端债务），
  统一会话无此注入缝。
- **#2 行中间截断**：`TestTerminalSessionPartialHistoryWriteMidRowStaysFailClosed`
  （helper 扩展 `cutMarker/cutOffset` 定点截断）：VT 屏级断言"前缀可见、标记行不成行、
  不入 scrollback" + 投影失证 + 下一笔 Deferred 零字节。
- **#3 合并事务 history 段截断**：`TestTerminalSessionMergedTransactionCutInsideHistoryFailsClosed`
  ——截断点后 viewport 字节零泄漏（新 viewport 标记缺席）、frame/history 双 ErrShortWrite+partial、
  投影 unknown、下一笔 Deferred。
- **#5 abort 中历史移交**：`TestTerminalSessionExecutorAbortDuringBlockedHistoryHandoffStaysFailClosed`
  ——零写证明 → Deferred（token 保持 Queued 可重试）；收敛后 ProjectionUnknown=true、
  WriteCursor=0；中止后重试不再触达物理 writer（writeCount 不增）、无 3J。
- **#4 校正**：resume/replay 失败注入已有覆盖（`TestResumeFailingWriterReconciliationStopsWithinBoundE2E`：
  `short=true` + unknown 投影 + 有界重试），无需新用例。
- **#6 lease**：session 级 partial 进出备用屏已有覆盖（`terminal_session_test.go` 备用屏组）；
  executor 级 lease 不写由既有用例保证，暂不新增。
- **#8 回归记录**：drain + a2 守护（`TestTerminalSessionExecutorDrainsFinalTranscriptDeliveryAfterStreaming`、
  `TestFinalizeActiveCellPlansWholeSourceFromZero/WithoutDeferral`）`-count=40` 与 `-race` 均绿。
- 提交：`1e223029`（批 1+2）；验证：三用例 `-count=20`/`-race` 绿、既有 partial 家族绿。

- **A1-2a（`c29865fd`）**：写事务证明贯通——abort 派发二义纠偏（in-flight → Failed+partial
  未决隔离）、executor 按 proof 分类（Q6 关闭）；设计与实施记录见
  `docs/plan/aicli-render-a1-2-write-proof-record-plan.md`。

- **A1-2b（`b99c6bd1`）**：claimed 失效改 pending-invalidation + 结果动作 proof 解析
  （Q2 关闭）；executor 写前门控零写 Deferred；`markDeliveredBatchUnresolved` 释放
  被 claim 游标（Q4 邻近修复）。

- **Q3（`31cf5f72`）**：覆盖集逐 token 解析——`ackBatch` 不再整批失配回退，
  `markDeliveredBatchUnresolved` 删除；仅畸形覆盖集 fail-closed，其余按单 token
  证明分类（pending 失效→未决隔离、身份/同代变化→仅该 token 隔离、竞态 rebase
  照常交付）。见设计记录 §7.3。

- **A1-2c（2026-10-07）**：settle 定稿为 proof 终态 + 门槛删除面评估。
  - **定稿**：Partial/Abandoned（不可证明）→ Failed+partial 未决隔离 → source-backed 原地重建
    （viewport 重绘；不清 scrollback、不推进 epoch、不重导）→ settle 原地吸收。**显式放弃区分**：
    不再判定未证明字节是否落盘；settle 是所有不可证明记录的唯一恢复终态。unresolved gate =
    proof 谓词（`hasUnresolvedTerminalDelivery()` ⇔ Failed / invalidated-partial 终态）。
  - **设计文档对齐**：§1.2 G5、§3.4、§3.6、§5.2/§5.4/§5.6、§9.3.4、§9.4、INV-8 的
    「失败 → 新 epoch 导入」表述全部改为 settle 终态（§3.4/§3.6 为本轮验收面）。
  - **专项用例**：`TestHistoryEffectsReducer_SettleTerminalStateIsProofDerived`
    （Committed/FailedZero 不置位；Partial 置位；settle 清位 + 幂等 + 迟到 ack 不复活）。
  - **门槛删除面（逐条判定；无本轮可删项）**：

    | 门槛 | 证据 | 判定 | 删除前提 |
    |---|---|---|---|
    | E1/E2 恢复墙钟退避 | `terminal_session_executor.go:541-556, 625-635` | 保留 | 「成功但义务未清」循环结构性不可达（恢复帧事务内携带 settle 决策），或恢复循环整体退役（P3）；proof record 本身不消解该循环 |
    | E3 claim-miss 补偿 | `terminal_session_executor.go:864-876` | 保留 | claim+snapshot 原子化（同一 reducer 版本栅栏内接受 claim，头指针不能在其下变化）；此前删除会留下 stranded claim 死锁（无 aging watchdog） |
    | S3/S4 历史可写性 | `terminal_session.go:866-871, 913` | 保留 | claim 携带 Attached 投影证明（投影已知/边界已知并入 claim 快照），写端不再自行判定；属 A1-3/A2 结构面 |
    | S6 tail 去重 | `terminal_session.go:962-978, 1469-1488` | 保留 | ledger 行级 proof 落账（重发结构性不可能）+ planner 不 rebase 游标 token；否则 resident-tail 双写回归 |
    | E4/E5/E6/E7、Q1、S5/S7/S9 | §2 清单 | 保留 | 既有保留项，不在本轮删除面 |

  - **风险 #1 关闭**：settle 与 epoch 冲突以「settle 终态 + 不推进 epoch」定稿；partial 区间在
    settle 后原地退役（来源身份终态、永不重发），后续 token 按行序从 resident 之后续写——
    不需要 partial-token ack，也不需要新的行游标起点定义。

## 6. 风险与开放问题

1. ~~**settle vs epoch 冲突**~~ **已关闭（A1-2c，2026-10-07）**：设计文档统一为 settle 终态
   （不推进 epoch、不重导）；partial 区间 settle 后原地退役，后续 token 按行序续写，
   无 partial-token ack 需求。见 §5 A1-2c。
2. **游标粒度**：physical display row vs `(cell, source range, fragment)`；Markdown fragment 与
   wrap 行不一一对应，resize 改行数（generation）。
3. **tombstone 去留**：游标后若只从游标之后规划可删；否则 A3 需先有聚合表示。
4. **双 executor 收敛**：删 `HistoryCommitExecutor` 还是让 presenter 切到它。
5. **proof record 归属**：reducer ledger / TerminalSession / 复用 gateway receipt（后者缺逐 token
   粒度）。
6. **wake 协议**：删除时序门槛后 proof 状态变化必须成为唯一唤醒源，
   `controller.go:587-637` 白名单需同步重构（否则 stall 风险转移）。
7. **DEC 2026 可注入性**：`terminal_lock.go` 直写 `os.Stdout`，A1-1 用例 1 需先定注入缝
   （writer 注入或 session 侧包裹），属 P0 写端归一边界。
