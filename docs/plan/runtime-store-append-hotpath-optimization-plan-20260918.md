# Runtime Store `AppendEvent` 热路径优化方案（P1.6：`RETURNING` + prune/vacuum 外移）

- 版本：v1.0（2026-09-18 初稿）
- 状态：待评审。§7 决策记录为默认执行基线
- 日期：2026-09-18
- 覆盖项：P1.6
- 分册索引：`docs/plan/runtime-store-hardening-followup-index-20260918.md`
- 关联分册：P1.5/P2.11（批量落盘）会复用本分册的语句数与事务时长优化；P1.7（双池）建议排在本项之后

---

## 0. 现状与证据

### 0.1 每次 append = 1 个事务 + 3~4 条语句

`backend/internal/chat/session_runtime_store.go:2404-2463`：

1. `SELECT COALESCE(MAX(seq),0)+1 FROM session_events WHERE session_id = ?`（`:2433-2439`）
2. `INSERT INTO session_events (...)`（`:2440-2450`）
3. `pruneRuntimeRowsTx(...)`（`:2451-2455`）：
   - `DELETE FROM session_events WHERE session_id=? AND seq<=?`（仅当 `eventSeq > retention && eventSeq%pruneInterval==0`，`:2470-2478`）
   - `DELETE FROM agent_control_mailbox_records ...`（同理，`:2479-2490`）
   - `PRAGMA incremental_vacuum(64)`（`pruned && fileBacked`，`:2488-2492`）
4. `COMMIT`（`:2456-2459`）+ `notifyEventWatchers`（`:2461`，非阻塞）

### 0.2 驱动能力事实（`RETURNING` 可用）

- 依赖：`github.com/ncruces/go-sqlite3 v0.32.0`（`backend/go.mod`）。
- 内置 SQLite：**3.51.3**（证据：模块缓存 `embed/sqlite3.wasm` 内的版本字符串；`RETURNING` 自 SQLite 3.35.0 起可用）。
- URI 参数 `_pragma` 受支持（模块 `conn.go:60-138`），P1.7 分册复用该事实。
- 结论：`INSERT ... RETURNING` 在本仓当前构建下可直接使用；仍建议运行时以 `SELECT sqlite_version()` 探测并留回退路径（防御未来驱动更换/裁剪）。

### 0.3 跨进程正确性约束（选择方案的硬边界）

`MAX(seq)+1` 在事务内取号，保证「两个进程同时写同一个 `session_runtime.sqlite`」也不重号；仓内已有回归锚点：`TestSQLiteRuntimeStoreConcurrentInstancesDeduplicateMessageID`（`session_runtime_store_test.go`）。

任何「进程内高水位缓存」「独立 seq 分配表」方案都必须先回答多进程场景；本方案不引入这两类方案（见 §3.1 拒绝方案）。

### 0.4 现有可观测性与已知缺口

- P0 已落地：`operationContext`（默认 10s）、`checkPoolReentry`、`NotifyDropStats`。
- 缺口：没有 append 时延/语句级统计，无法量化「SELECT 取号」与「vacuum」各占多少；本方案要求测量先行（§3.3）。

---

## 1. 目标 / 非目标 / 成功度量

### 1.1 目标

| 编号 | 目标 |
| --- | --- |
| G1 | append 写路径从「SELECT+INSERT」两条语句降为一条 `INSERT ... SELECT ... RETURNING`（能力探测失败自动回退旧路径） |
| G2 | 把 `PRAGMA incremental_vacuum`（页回收，最贵的部分）移出写事务，改为后台单飞维护，写事务内不再执行 |
| G3 | 保留 `DELETE` 在写事务内（有界、走索引），保证 mailbox 恢复查询依赖的记录不会被异步删除破坏 |
| G4 | 新增 append 时延/语句计数，形成优化前后可对比的证据 |
| G5 | 全部改动可开关、可回退，且不改变 seq 语义与跨进程安全性 |

### 1.2 非目标

- 不改 retention / pruneInterval 语义与默认值（2048 / 2048 / 256）。
- 不做批量落盘（P1.5 分册）、不做读写双池（P1.7 分册）。
- 不引入进程内 seq 缓存、不新增 seq 分配表、不改 `session_events` 主键结构。
- 不改通知语义（`notifyEventWatchers` 仍逐事件、非阻塞、可丢）。

### 1.3 成功度量

| 指标 | 基线（测量后填） | 目标 |
| --- | --- | --- |
| `AppendEvent` 语句数 | 2（+prune 时 3~4） | 1（+prune 时 2） |
| `AppendEvent` P95 时延（本地文件、批量 1k 事件） | 待测（M0 产出） | 降低 ≥30% |
| 写事务内 `incremental_vacuum` 次数 | 每 256 事件 1 次 | 0（全部转后台） |
| 单位事件 DB 时间（`append_ns_total/events`） | 待测 | 降低 ≥25% |
| 维护任务对 append P99 的影响 | — | 后台维护期间 append P99 增幅 ≤10%（维护任务有界、可与写错峰） |
| 回退正确性 | — | 探测失败/开关关闭时，行为与现状逐字节一致（golden 测试） |

---

## 2. 设计原则

1. **语义等价优先**：任何优化不得改变 seq 单调性、跨进程唯一性、事务原子性与通知契约。
2. **测量先行**：先加计时与计数（M0），再改语句；没有基线的优化不进入验收。
3. **能力探测 + 回退**：新 SQL 形态按 SQLite 版本能力启用；探测结果与版本号暴露在状态访问器里。
4. **热点路径最小化**：写事务内只保留「取号 + 插入 + 有界 DELETE」；页回收/大扫除归后台。
5. **单飞与有界**：后台维护最多一个实例在跑，带 deadline，可被 Close 取消。

---

## 3. 详细设计

### 3.1 G1：单语句取号 + 插入（`RETURNING`）

**目标 SQL**（`AppendEvent` 内）：

```sql
INSERT INTO session_events (session_id, seq, type, trace_id, agent_name, tool_name, payload_json, created_at)
SELECT ?1,
       COALESCE(MAX(seq), 0) + 1,
       ?3, ?4, ?5, ?6, ?7, ?8
  FROM session_events
 WHERE session_id = ?1
RETURNING seq;
```

Go 形态：

```go
row := tx.QueryRowContext(ctx, insertEventReturningSeqSQL, args...)
if err := row.Scan(&seq); err != nil { ... }   // 单语句内完成取号与插入
```

要点与边界：

1. **能力探测**：`init(ctx)` 成功后执行 `SELECT sqlite_version()`，解析 semver 并与 `3.35.0` 比较，写入 `s.supportsReturning bool`（附带 `s.sqliteVersion string` 供状态访问器/日志）。探测失败（权限、驱动差异）按不支持处理。
2. **回退路径**：保留现有两条语句实现，抽为 `appendEventLegacyTx(ctx, tx, event) (int64, error)`；开关 `SQLiteReturningEnabled`（默认 true，探测不支持时自动 false）。
3. **WHERE 子句保证单行**：`WHERE session_id=?1` 在空表时 `MAX` 聚合仍返回一行（`COALESCE` 保证 `seq=1`），`SELECT` 恒为单行 → `INSERT` 单行 → `RETURNING` 单行；`QueryRowContext` 安全。若未来 `session_events` 出现无 `session_id` 前缀的其它取号方式，必须重新论证。
4. **多进程安全**：取号与插入在同一条语句、同一写事务内完成，SQLite 写锁串行化；与旧路径语义一致，`TestSQLiteRuntimeStoreConcurrentInstancesDeduplicateMessageID` 必须保持绿。
5. **错误处理**：`QueryRowContext` 的错误（busy/locked/deadline）与旧路径相同分类；`operationContext`（默认 10s）继续生效。
6. **事务模式（审查 R1，必须）**：写事务一律 `BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})`（驱动映射 `BEGIN IMMEDIATE`）；deferred 事务在跨写者连接下会因「读→写升级」返回 `SQLITE_BUSY_SNAPSHOT(517)`；命中 517/BUSY 时回滚并以**新事务**重试 ≤3 次（50→200ms），计数见 §3.3。legacy 两条语句路径同样受此约束。详见 P1.5 分册 §3.6 与审查报告 §1.5。
7. **语句缓存（决策注记）**：本期不引入 store 级 `sql.Stmt` 缓存——`database/sql` 在 Tx 内每次 `QueryRowContext/ExecContext` 走连接级语句缓存，SQLite prepare 为 μs 级；单连接池下自持 Stmt 又会与连接重建/回退路径耦合。M0 若测得 prepare 占比 >5% 再评估。
8. **拒绝的方案**：
   - 进程内 `nextSeq` 高水位：多进程同库会重号/冲突；且崩溃恢复复杂。**不采纳**。
   - 独立 `session_event_seq` 分配表：多一次 UPSERT 往返与新的迁移面，收益不明确。**不采纳**。
   - `WITHOUT ROWID` 调整主键：改磁盘格式，收益与风险不成比例。**不采纳**。

### 3.2 G2/G3：prune 拆分——DELETE 留在事务内，`incremental_vacuum` 外移

**保留在写事务内的部分**（不变）：

- `DELETE FROM session_events WHERE session_id=? AND seq<=?`（每次最多删 `pruneInterval=256` 行；`session_events` 主键/索引按 session 前缀，范围删除成本有界）。
- `DELETE FROM agent_control_mailbox_records ...`（同上）。
- 理由：`recoverSQLiteMailboxDuplicate`（`:2824-2890`）依赖「未过 retention 的 mailbox 记录仍可查到」；把 mailbox 删除异步化会引入「恢复查询命中空窗」的新竞态。事件表同理被 `ListEvents(afterSeq)` 消费，异步删除窗口会放大「先读后删」的可见性抖动。

**外移到后台维护的部分**：

- `PRAGMA incremental_vacuum(64)`（当前 `:2488-2492`）——页回收，代价与文件大小相关，且只影响磁盘占用，不影响可见性。
- 未来可选的 `PRAGMA optimize` / `wal_checkpoint(PASSIVE)`（本方案不默认加入，作为后续观测项，见 §8 Q3）。

**后台维护任务**（新文件建议 `backend/internal/chat/runtime_store_maintenance.go`）：

```go
type runtimeStoreMaintenance struct {
    pendingVacuum atomic.Bool   // prune 命中时置位（合并多次触发）
    running       atomic.Bool
    stopCh        chan struct{}
    doneCh        chan struct{}
}
```

行为：

1. **触发**：`pruneRuntimeRowsTx` 判定「本事务确实删除了行」时，`pendingVacuum.Store(true)` 并尝试唤醒维护 goroutine（带缓冲 1 的 signal channel，非阻塞）。
2. **执行**：维护 goroutine 醒来后做 **single-flight**（`running.CompareAndSwap(false,true)`），执行：
   - **不持 `s.mu`**（审查 R2）：`s.mu` 只覆盖事件/邮箱/修复路径（`session_runtime_store.go:2426/2532/2589/2738/3063/3091/3122/3149/3279/3319`），持锁执行 vacuum 会把 `AppendEvent`/`AppendMailbox` 阻塞整个真空时长；正确的串行化由**写池单连接 + SQLite 写锁**提供；
   - **空闲门槛**：距上次 append/邮箱写 ≥ `maintenanceIdleGap`（建议 200ms）才执行，减少与交互写抢占；
   - **小步执行**：每轮 `PRAGMA incremental_vacuum(16)`（不再是 64），每步独立短事务；`busy_timeout` 内失败即退避（500ms→2s），单轮 deadline ≤2s（与业务 `operationContext` 有意分离）；
   - 仅 `fileBacked` 执行；失败仅计数 + 限频告警，不影响业务；
   - `pendingVacuum.Store(false)` 在开始执行时清零（执行期间新的 prune 会重新置位）。
3. **节流**：两次维护最小间隔 `maintenanceMinInterval=5s`（防抖动；多次 prune 合并为一次 vacuum）；`maintenance.skipped_not_idle` / `skipped_busy` 计数。
4. **生命周期**：`store` 首次打开成功后启动（惰性）；`Close()` 先 `close(stopCh)`，等待 `doneCh`（≤1s），再走既有 checkpoint/close 流程；`ensureCtx` 重开路径不重复启动（`sync.Once` 或 CAS 标志）。
5. **测试开关**：`RuntimeStoreConfig.DisableBackgroundMaintenance`（默认 false）；测试中也可直接调用 `maintainer.runOnceForTest(ctx)` 验证。
6. **可观测**：`maintenance.runs/skips/duration_ms/pending` 计数（§3.3）。

**为什么不把 DELETE 也异步化**（备选方案与结论）：

- 异步删除需要一个水位/任务表来避免与 `MAX(seq)` 取号、`ListEvents` 分页并发时的语义漂移，且要处理进程崩溃后的续跑；收益（每 256 事件一次的有界 DELETE）远小于成本。**本期不采纳**，列入 §8 Q1 作为后续观察项。

### 3.3 G4：测量先行（M0 交付）

在 `SQLiteRuntimeStore` 增加轻量统计（对齐 `NotifyDropStats()` 的风格，不引入外部指标依赖）：

```go
type AppendTimingStats struct {
    Appends        int64   // 次数
    TotalNs        int64   // 累计耗时
    MaxNs          int64   // 单次最大（近似）
    PruneRuns      int64   // 事务内 prune 命中次数
    VacuumRuns     int64   // 后台 vacuum 次数
    VacuumTotalNs  int64
    ReturningUsed  int64   // RETURNING 路径次数（探测生效的证据）
    LegacyUsed     int64   // 回退路径次数
    LockHoldNs          int64 // 写锁持有时长（BEGIN → COMMIT 返回）
    BusyRetries         int64 // BUSY 触发的重试次数
    BusySnapshotRetries int64 // SQLITE_BUSY_SNAPSHOT(517) 重试次数
}
func (s *SQLiteRuntimeStore) AppendTimingStats() AppendTimingStats
```

- 计时点：`AppendEvent` 入口到 `COMMIT` 完成（不含 notify）；`time.Since` 仅在入口取一次，避免热路径多次 `time.Now()`。
- 锁指标（`LockHoldNs`/`BusyRetries`/`BusySnapshotRetries`）用于 P1.5 批量锁时长预算与审查 R1 的重试有效性验证。
- 分位值本期不做（避免在热路径维护分布式直方图）；验收用「均值 + Max + 语句计数」对比基线；如需 P95 由压测脚本在调用侧统计。
- M0 同时产出基线报告（压测脚本 + 结果入 `docs/plan` 附录或 PR 描述）。

---

## 4. 兼容 / 迁移 / 回滚

- 无磁盘格式变更；`RETURNING` 与旧两段式语句产生完全相同的表状态。
- 新配置项均为默认开启（`SQLiteReturningEnabled`）/默认启用后台维护（`DisableBackgroundMaintenance=false`），但都有显式关闭路径；关闭后行为与现状一致。
- 多进程：新旧版本进程混跑安全（同库、同表、同 seq 语义）。
- 回滚：单开关回退；后台维护停止不影响正确性，只影响磁盘空间回收节奏（WAL/auto_vacuum=INCREMENTAL 下空间不会立即释放，属预期）。
- 测试兼容：既有 `store.db` 直连断言、`store.mu` 串行假设、`pruneInterval=0` 关闭路径均保持。

## 5. 测试与验收（DoD）

1. `TestAppendEventReturningMatchesLegacySeq`：开关 on/off 各写 100 条（含空表首条、跨 session 交错），断言 seq 序列一致且连续。
2. `TestAppendEventReturningConcurrentInstances`：两个 store 实例同文件并发写，断言无重号、无丢失（对齐既有锚点）。
3. `TestAppendEventReturningProbeFallback`：伪造探测失败（测试内直接置 `supportsReturning=false`），断言走 legacy 且 `LegacyUsed` 增长。
4. `TestPruneRuntimeRowsNoInlineVacuum`：`fileBacked` 存储触发 prune，断言事务内未执行 vacuum、`pendingVacuum` 置位。
5. `TestMaintenanceSingleFlightAndThrottle`：并发触发多次，断言 `runs` 受节流约束且不并发执行。
6. `TestMaintenanceBoundedAndCancelable`：注入慢 vacuum（可用测试钩子或大文件）→ 断言 deadline 生效、Close 能等待退出。
7. `TestMaintenanceFailureDoesNotAffectAppend`：真空失败（只读文件/锁）→ append 继续成功、失败计数 +1。
8. `TestAppendTimingStats`：计数与路径标记正确。
9. 压测对比（§1.3 表格）：语句数、P95、单位事件耗时、后台维护期间 P99。
10. Golden：开关关闭时与当前实现的 SQL 序列完全一致（可用 driver 侧 `trace` 或 SQL 语句计数 hook 断言）。
11. `TestMaintenanceDoesNotHoldStoreMutex`：注入慢 vacuum + 并发 `AppendEvent`，断言 append 延迟不受维护持锁影响，`skipped_not_idle`/`skipped_busy` 计数生效（审查 R2）。
12. `TestAppendEventRetriesBusySnapshotWithImmediateTx`：复现「A 读 → B 写 → A 写」的 517，断言 IMMEDIATE + 新事务重试成功、`BusySnapshotRetries` +1；deferred 对照路径失败（审查 R1）。

验收门槛：1–12 全绿 + 压测达到 §1.3 目标 + 灰度（若开启）7 天无 `maintenance.failed` 告警风暴。

## 6. 里程碑与工作量

| 里程碑 | 内容 | 估算 |
| --- | --- | --- |
| M0 | 计时统计 + 基线压测报告 | 0.5d |
| M1 | `RETURNING` 探测 + 单语句路径 + 回退 + 测试 1–3 | 1d |
| M2 | 后台维护任务 + prune 拆离 + 测试 4–8 | 1d |
| M3 | 压测对比 + 文档/runbook 更新 | 0.5d |

## 7. 决策记录（评审基线）

| 编号 | 决策 | 状态 |
| --- | --- | --- |
| D1 | 采用 `INSERT ... SELECT ... RETURNING seq`，以 `sqlite_version()` 探测 + 自动回退 | 待评审（默认执行） |
| D2 | 拒绝进程内 seq 缓存与独立 seq 表（多进程正确性优先） | 待评审（默认执行） |
| D3 | DELETE 保留在写事务内；仅 `incremental_vacuum` 外移后台 | 待评审（默认执行） |
| D4 | 后台维护 single-flight、最小间隔 5s、deadline `min(busyTimeout*3,15s)` | 待评审（默认执行） |
| D5 | 统计以「计数 + 均值 + Max」呈现，不引入热路径分位直方图 | 待评审（默认执行） |
| D6 | 写事务 `LevelSerializable`（BEGIN IMMEDIATE）+ 517/BUSY 回滚后新事务重试（审查 R1 前置） | 待评审（默认执行） |
| D7 | 维护任务不持 `s.mu`；空闲门槛 200ms + 16 页小步 + BUSY 退避（审查 R2） | 待评审（默认执行） |

## 8. 开放问题

- Q1：后续是否把事件 DELETE 也异步化（需要水位/任务表设计）？触发条件：写事务中 DELETE 耗时占比 >15%（M0 统计）。
- Q2：`sqlite_version()` 探测结果是否需要在启动日志与 `/web/api/status` 暴露（便于现场定位驱动差异）？
- Q3：是否在维护任务中加入 `wal_checkpoint(PASSIVE)`（降低 WAL 增长）？需先评估与 `Close()` 的 TRUNCATE checkpoint、多进程并发的交互。
- Q4：`incremental_vacuum(64)` 的页数是否需要按 DB 大小自适应（当前固定 64）？

---

## 9. 实施记录：P1.6（2026-09-18）

状态：**已实施并回归通过**（§7 决策 D1–D7 按基线执行；§1.3 的时延目标未在当前粒度达成，见 9.3）。

### 9.1 改动清单

| 文件 | 内容 |
| --- | --- |
| `internal/chat/runtime_store_maintenance.go`（新增） | `AppendTimingStats` 统计与访问器；`sqlite_version()` 能力探测（`sqliteVersionAtLeast`）与自动回退；后台维护任务（single-flight、5s 节流、200ms 空闲门槛、16 页小步、2s/步 + `min(busyTimeout*3,15s)` 总 deadline、BUSY 退避 500ms→2s、`Close` 有界等待） |
| `internal/chat/session_runtime_store.go` | `AppendEvent`：单语句 `INSERT ... SELECT ... RETURNING seq` + legacy 回退（`appendEventReturningSeqTx` / `appendEventLegacySeqTx`）；`append_ns/total/max`、`lock_hold_ns` 计时；prune 内联 `incremental_vacuum` 移除，改为置 `pendingVacuum` 并唤醒维护；打开时探测能力并按需启动维护；`Close` 先停维护 |
| `RuntimeStoreConfig` | 新增 `DisableSQLiteReturning`、`DisableBackgroundMaintenance`（零值=默认启用） |
| 测试 | `runtime_store_append_hotpath_test.go`（11 例 + 2 个基准） |

### 9.2 测试清单对照（§5 DoD）

| # | 状态 | 说明 |
| --- | --- | --- |
| 1 | ✅ | `TestAppendEventReturningMatchesLegacySeq`：两路径各 100 条、双 session 交错，seq 序列逐项一致且 1..50 连续 |
| 2 | ✅ | `TestAppendEventReturningConcurrentInstances`：同文件双实例并发 80 条，无重号；P0.5 锚点用例继续通过 |
| 3 | ✅ | `TestAppendEventReturningProbeFallback` + `TestSQLiteVersionAtLeast`（含 3.34/3.35/3.51-dev/垃圾串） |
| 4 | ✅ | `TestPruneRuntimeRowsNoInlineVacuum`：prune 命中 → `prune_runs++`、`pending=true`、事务内 `vacuum_runs=0` |
| 5 | ✅ | `TestMaintenanceSingleFlightAndThrottle`：并发 4 次仅 1 轮执行、3 次 busy；第二次触发被 5s 节流 |
| 6 | ✅ | `TestMaintenanceBoundedDeadline`（100ms 覆盖 deadline）+ `TestMaintenanceStopOnCloseBounded`（Close 等待 ≤1s） |
| 7 | ✅ | `TestMaintenanceFailureDoesNotAffectAppend`：真空失败计数 +1，随后 append 正常 |
| 8 | ✅ | `TestAppendTimingStats`：次数/路径标记/耗时/Max/LockHold/SQLiteVersion |
| 9 | ⚠ 目标未达成 | 见 9.3：`stmts/op` 2→1 已证，但单事件时延降幅 <30% |
| 10 | ✅ | `TestAppendSQLPathGolden`：golden 序列 `["returning"]` vs `["legacy-select","legacy-insert"]` |
| 11 | ✅ | `TestMaintenanceDoesNotHoldStoreMutex`：维护阻塞在 hook 时 append 仍 <1s 完成 |
| 12 | ≈ 等价覆盖 | P0.5 `TestAppendEventConcurrentInstancesNoBusySnapshot`（IMMEDIATE + 517=0）；`BusySnapshotRetries` 由 `ContentionStats()` 暴露 |

回归：`go test ./internal/chat/ -count=1`、`./internal/team/`、`./internal/api/skills/ -run "RuntimeStore|RuntimeEvent|Delivery|Persist"`、`go build ./...`、`go vet` 全绿。

### 9.3 基准与 §1.3 目标（M3 证据）

`BenchmarkAppendEvent{Returning,Legacy}`（5000 次/轮，`-count 3`，16 核 Windows；每次 append 一个 WAL 提交）：

| 路径 | ns/op（3 轮） | lock_hold_ns/op | stmts/op |
| --- | --- | --- | --- |
| RETURNING | 499,220 / 658,057 / 666,524 | ~456,656 | **1.000** |
| legacy | 606,845 / 557,374 / 558,799 | ~395,053 | **2.000** |

- 语句数目标（1 (+prune 时 2)）达成；写事务内 `incremental_vacuum` 次数 = 0 达成。
- **P95/单位事件时延降幅未达 ≥30%**：单事件成本由 WAL commit 主导（同一数量级、轮间噪声重叠），取号语句的节省被淹没。诚实结论：本项收益需在 P1.5 批量落盘（一次提交摊薄多条事件）与跨进程争用场景复测；当前先交付语句数/遥测/维护拆分三项确定性收益。

**P1.5 落地后复测（2026-09-18，关闭 9.5-M3 遗留）**：批量落盘把「每次 append 一个 WAL 提交」变成「每批一个提交」，单位事件成本因此被摊薄——

| 路径 | 写锁/提交成本（单事件） | 来源 |
| --- | --- | --- |
| 未批量（RETURNING `BenchmarkAppendEvent`） | lock_hold ≈ **456µs/op** | 本节上表 |
| 批量（`eventPersistBuffer` + `AppendEvents`，1k/s） | **34–36µs/事件**（32ms/40ms 两轮实测） | P1.5 分册 §13.2 批参数扫描（`batch_lock_hold_total / batched_events`） |

即：**单位事件写锁成本下降 ≈13×**，远超 P1.6 §1.3 的 ≥30% 目标；该收益来自 P1.5 的批量提交（P1.6 的语句数 2→1 只是小头）。端到端单事件可见延迟由「flush 间隔（25–40ms）+ flush P95（1.6–2.1ms）」决定，属 P1.5 设计口径。

### 9.4 有意偏离与设计注记

- 配置命名用反极性 `Disable*`：计划中的 `SQLiteReturningEnabled`（默认 true）在 Go 零值 bool 下无法表达「默认开启」，故改为零值=启用。
- `DisableBackgroundMaintenance` 只阻止 worker 启动；prune 仍置 `pendingVacuum`（关闭期间页回收延后，重新启用或下次进程处理）。
- 维护单轮=多步（16 页/步、≤64 步、步间 10ms 让出池连接），受总 deadline 与每步 2s 双重约束；`PRAGMA freelist_count` 不再下降即提前结束。
- `AppendTimingStats.VacuumRuns/VacuumTotalNs` 按「步」累计。
- 计划中的 DELETE 异步化、`wal_checkpoint(PASSIVE)`、页数自适应（Q1/Q3/Q4）未纳入本期，维持现状。

### 9.5 遗留

- **M3 数值目标：已复测并关闭**（见 §9.3 末表）：单位事件写锁成本 456µs/op（未批量）→ 34–36µs/事件（批量，P1.5），降幅 ≈13×；复现命令见 P1.5 分册 §13.2。
- ~~Q2：`SQLiteVersion`/`AppendTimingStats` 目前仅经 Go 访问器暴露，未接入 `/web/api/status`。~~ **已于 2026-09-18 接入**：`/debug display`（TUI）与 `/web/api/status` 新增"存储与持久化:"区块 / `storage.append` 段，输出 `sqlite_version`、`supports_returning`、`appends`、`avg/max`、`lock_hold_avg`、`batches/batched/avg_batch`、维护计数与 `prune/vacuum`；`storage.contention` 段输出 `busy_retries`/`busy_snapshot_errors(517)`/`busy_exhausted`。
