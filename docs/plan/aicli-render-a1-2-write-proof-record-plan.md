# A1-2 写事务记录（proof record）设计：事实分类替换推定

> 依据：`docs/plan/aicli-render-a1-write-proof-recon-20261007.md` §4 A1-2；
> `docs/plan/aicli-render-remaining-defect-ledger-20261006.md` A1；
> `docs/architecture/aicli-tui-renderer-architecture-design.md` §3.4/§3.6/§11-3；
> `docs/plan/aicli-ui-handoff-inflight-strand-hardening-plan-20261005.md` §543-546。
> 前置：A1-1 partial-write 验收矩阵已落地（`1e223029`/`7637cd56`）。

## 1. 目标

- executor↔session 边界产出**写事务证明（proof record）**：每笔物理事务的事实分类
  （NotAttempted / Committed / FailedZeroBytes / Partial / Abandoned）+ 覆盖 token 集。
- executor/queue 不再用「游标置位 ⇒ 可能已写」「Frame.Err ⇒ 零写」等推定分支：
  - 零写证明（FailedZeroBytes）→ 同 token 重试（Deferred）；
  - Partial / Abandoned（宿主状态未知）→ 未决隔离（Failed+partial），settle 为终态；
  - Committed → 按覆盖集 ack。
- settle 定稿为 proof 终态：显式放弃区分 + unresolved gate 谓词化。

## 2. 事实模型

```go
type terminalWriteOutcome uint8
const (
    terminalWriteNotAttempted terminalWriteOutcome = iota // 无字节/未调用 sink
    terminalWriteCommitted                                // 整笔字节已证明落盘
    terminalWriteFailedZero                               // 可证明零写（宿主无字节）
    terminalWritePartial                                  // 0 < n < len（短写/panic 后重启）
    terminalWriteAbandoned                                // 已派发的 syscall 被放弃，宿主状态未知
)
type terminalWriteProof struct {
    Outcome      terminalWriteOutcome
    FlushedBytes int
    TotalBytes   int
    Err          error
}
```

| 来源事实 | Outcome | ledger 分类 |
|---|---|---|
| receipt `committed` / 直写 n==len 且 err==nil | Committed | Ack（覆盖集） |
| receipt `failed_zero_bytes`（含 AfterStart/Zero） | FailedZero | Deferred（同 token 重试） |
| 写前拒绝 / pre-admission / pre-abort / 空载荷 | FailedZero / NotAttempted | Deferred |
| receipt `unknown_partial` / panic / 0<n<len | Partial | Failed+partial（unresolved） |
| **`canceled_after_start`（已派发 syscall 被放弃）** | **Abandoned** | **Failed+partial（unresolved）** |
| abortable wrapper：abort 命中「请求未派发」分支 | FailedZero | Deferred（零写证明） |
| abortable wrapper：abort 命中「in-flight」分支 | Abandoned | Failed+partial（unresolved） |
| abort 与 outcome 同时就绪 | 按已到达的 outcome 事实返回 | 对应行 |

> **纠偏（A1-2a 核心）**：`receiptError` 原把 `CanceledBeforeIO` 与 `CanceledAfterStart`
> 折叠为同一 `ErrTerminalWriteAborted`；abortable wrapper 亦不区分「未派发 / in-flight」；
> probe 仅按 `bytesWritten>0` 记 partial。三者叠加导致**放弃的 syscall（可能完整落盘）
> 被当作零写 → Deferred 可重试**。证明模型下 Abandoned 必须走未决隔离。

## 3. 现状推定点与替换映射

| 推定点 | 现状 | 替换 |
|---|---|---|
| Q6（executor 零写判据） | `history.Err != nil && result.Frame.Err != nil && !partial` 合取 | `proof.Outcome == FailedZero`（proof 为 nil 时保留旧合取兜底） |
| abort 二义 | 两类取消折叠 + wrapper 不区分派发 | Abandoned 事实 + probe partial 标记 |
| Q2（queue invalidate） | `WriteCursor==token` 推定可能已写（`history_effect_queue.go:496-510`） | **已落地（A1-2b，§7.2）**：pending-invalidation + 结果动作 proof 解析 |
| Q3/Q4（batch 失配） | `markDeliveredBatchUnresolved` 兜底（`:607-639`） | Q4 游标释放已落地（§7.2）；Q3 covered 集为 A1-2b 尾项 |
| Q5（Deferred 条件） | 由 `Deferred && Err==nil && !partial` 驱动 | 保留 + 零写分支改 proof 驱动（A1-2a） |
| S6（session tail 去重） | 物理投影证明 + 文本重合 | A1-2c 评估（ledger proof 落账后） |

## 4. 切片

- **A1-2a（本次）**：outcome 贯通（aborter 二义 → receipt 两类 → probe → proof）+
  executor 按 proof 分类（Q6 关闭）+ A1-1 abort 用例纠偏（in-flight abort = unresolved）。
- **A1-2b**：queue/reducer 推定替换：invalidate 不再读 WriteCursor，改读最近 proof
  （executor 在结果动作中携带 outcome）；batch covered 集直接判。
  **状态（2026-10-07）**：Q2 + Q4 游标释放已落地（§7.2）；Q3 covered 集尾项待做。
- **A1-2c**：settle 定稿为 proof 终态（放弃区分谓词化）+ 门槛删除面评估
  （E1/E2/E3/S3/S4/S6），更新设计文档 §3.4/§3.6。

## 5. 验收

- A1-2a：aborter 单测（in-flight → Abandoned、未派发 → 零写）+ A1-1 abort 用例纠偏
  + 既有 fail-closed 组 + CloseTimeout/presenter/output abort 组 + 全量 ui 绿。
- A1-2b：门槛清单 Q2/Q3/Q4 逐条有替代迁移记录；fail-closed 语义不回退。
- A1-2c：设计文档 §3.4 与实现一致；settle 终态有专项用例。

## 6. 风险

1. **Abandoned→Failed 改变 shutdown 路径分类**（原 Deferred）：executor 会走恢复回执，
   会话已中止时写全部被拒（零物理字节），由 settle 吸收；不影响正常路径。
2. **竞态窗口**：abort 与 outcome 同时就绪时按 outcome 事实返回（syscall 已返回，
   宿主状态已知），不引入不确定性。
3. **legacy 兜底**：proof 为 nil 的调用方（测试 executor、早期返回路径）保留旧分类，
   避免一次性迁移面过大。

## 7. 实施记录

### 7.1 A1-2a（2026-10-07，已落地）

- **outcome 贯通**：
  - `terminal_write_aborter.go`：新增 `errTerminalWriteAbandoned`（`%w` 包裹
    `ErrTerminalWriteAborted`，保持 abort 身份）；abort 命中「已派发 in-flight」→
    Abandoned；命中「等待派发」或入口检查 → 普通 abort（零写证明）；outcome 先于
    abort 就绪时按已返回的字节事实上报（不再折叠为零写 abort）。
  - `terminal_session.go`：`receiptError` 拆分 `CanceledBeforeIO`（零写）与
    `CanceledAfterStart`（Abandoned）；`submitWithPortLocked` 全分支携带 outcome；
    probe 对 Abandoned 置 `MayHavePartiallyWritten=true`；`flushTransactionLocked`
    在成功/失败路径附 `TerminalTransactionResult.Proof`（Outcome/FlushedBytes/
    TotalBytes/Err）；nil proof = 未达写路径（lease/generation 早退）→ 消费方走
    legacy 兜底。
  - `terminal_session_executor.go`：`publishResult` 零写判据改为
    `proof.zeroWriteProven()`（FailedZero/NotAttempted），partial 判据并入
    `proof.possiblyWritten()`；legacy 合取兜底保留。
- **行为纠偏**：in-flight abort（放弃的 syscall 可能完整落盘）由 Deferred 可重试
  改为 **Failed+partial 未决隔离**；
  `TestTerminalSessionExecutorAbortDuringBlockedHistoryHandoffStaysFailClosed`
  断言同步更新（Quarantined + MayHavePartiallyWritten + Failure + unknown + 无重放 + 无 3J）。
- **新增用例**：`TestAbortableTerminalWriterAbortInFlightIsAbandoned`、
  `TestAbortableTerminalWriterAbortBeforeDispatchIsZeroProven`（派发二义单测）。
- **验证**：定向组（含 CloseTimeout / zero-byte 重试 / panic / gateway abort）绿；
  家族 `-count=20` 绿；`-race` 绿；ui 全量 88.0s 绿；commands 全量随提交记录。
- **待办**：A1-2b（Q2/Q3/Q4 推定替换）、A1-2c（settle proof 终态 + 门槛删除面）。

### 7.2 A1-2b（2026-10-07，Q2/Q4 已落地）

- **pending-invalidation 模型（Q2 关闭）**：`HistoryCommitEntry.InvalidationPending` +
  `ledger.MarkInvalidationPending/ResolveInvalidation`。`queue.invalidate` 对 claimed
  token 只登记意图、保留 claim；分类由写结果动作的 proof 解析：
  - Deferred / 零写失败 → 干净失效（**不置 ProjectionUnknown**，尾部工作继续交付）；
  - Ack（已提交）→ 失效 + `MayHavePartiallyWritten`（旧字节在屏、来源已变）→ 未决隔离；
  - partial 失败 → 失效 + partial → 未决隔离。
- **executor 写前门控**：`historyCommitGate.EntryInvalidationPending`；`runOne` 在 claim
  校验前发现失效即发零写 Deferred（不把失效载荷写出去）。
- **Q4 邻近修复**：`markDeliveredBatchUnresolved` 终结被 claim 成员时释放 `WriteCursor`
  （此前游标会钉死在一个已终态 token 上，后续 claim 永远 out-of-order）；`ackBatch`
  校验新增 pending-invalidation fail-closed。
- **行为变化**：claimed 失效不再立即置 unknown；零写证明下投影保持已知、尾部工作不中断。
  原 `TranscriptBoundaryChangeInvalidatesInFlightHandoff` 用例改为 pending + 解析矩阵
  （Deferred 干净 / Ack 未决），新增 fail 矩阵、batch 游标释放、executor 零写门控用例。
- **验证**：queue/reducer `-count=20` 绿；executor `-count=10` 绿；`-race` 绿；ui 全量
  113.5s 绿；commands 全量随提交记录。
- **尾项**：Q3——`HistoryCommitsAcknowledged` 携带 covered 集，使
  `markDeliveredBatchUnresolved` 失配路径结构性不可达（当前仍为兜底）。
