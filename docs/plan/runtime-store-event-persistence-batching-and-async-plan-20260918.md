# Runtime Store 事件持久化批量化与发布链异步方案（P1.5 + P2.11）

- 版本：v1.0（2026-09-18 初稿）
- 状态：待评审。§9 决策记录为默认执行基线：评审中未否决的条目即按该决策实施；否决时在 §9 原地更新状态与替代决策（保留历史行）
- 日期：2026-09-18
- 覆盖项：P1.5（事件持久化批量化）、P2.11（`Bus.Publish` 发布链上的持久化异步化）
- 分册索引：`docs/plan/runtime-store-hardening-followup-index-20260918.md`
- 前置已落地：P0 池重入修复/操作超时/重入守卫、`NotifyDropStats`（`backend/internal/chat/runtime_store_hardening.go`）

---

## 0. 现状与问题证据

### 0.1 两条桥接都是「逐事件、同步、在发布者 goroutine 内」落盘

- aicli 宿主：`backend/cmd/aicli/commands/chat_actor_host.go:871-897`
  `h.EventBus.SubscribeCancelable("", handler)`，handler 内直接 `h.EventStore.AppendEvent(context.Background(), mapped)`。
- runtime-server 宿主：`backend/internal/api/skills/handler.go:4056-4081`
  `bus.Subscribe("", func(event){ ... store.AppendEvent(context.Background(), mapped) })`（另有未命中 P0-2 的 delivery drop 记录）。
- `Bus.Publish` 同步派发：`backend/internal/events/bus.go:307-330`——`for _, handler := range all { handler(event) }`，无队列、无超时、无 panic 隔离。

结论：事件生产者（actor 循环、工具执行、子代理协调器）每发布一个事件，都要等待一次 SQLite 写事务提交；单连接池 + 单写者 `s.mu` 下这是串行等待，P0 之前甚至会因池重入永久卡死（已修复），但**每事件一次事务**的吞吐上限仍在。

### 0.2 现状写入路径（`AppendEvent`，`session_runtime_store.go:2404-2463`）

每个事件一个事务，事务内 3~4 条语句：

1. `SELECT COALESCE(MAX(seq),0)+1 FROM session_events WHERE session_id = ?`（取 seq，`:2433-2439`）
2. `INSERT INTO session_events (...)`（`:2440-2450`）
3. `pruneRuntimeRowsTx(...)`（`:2451-2455`；`pruneInterval=256` 的整数倍才真正 DELETE，附 `PRAGMA incremental_vacuum(64)`，`:2465-2494`）
4. `COMMIT`（`:2456-2459`），随后 `notifyEventWatchers(seq, event)`（`:2461`）

无批量 API：全仓 `AppendEvents(` / `AppendEventBatch` 无匹配（仅 `AppendEvent`）。

### 0.3 生产者已有「先落盘、后发布」的特例

`backend/internal/chat/actor.go:5264-5275`：`SessionActor.publish` 先 `AppendEvent`，成功后回填 `event.Payload["seq"]`，再 `eventBus.Publish(event)`；桥接层对携带 `payload["seq"]` 的事件跳过（说明该事件已持久化）。

结论：桥接层不是唯一写入口，批量/异步改造必须**保留**这条去重规则，且不得改变「已带 seq 的事件不再重复落盘」的语义。

### 0.4 消费者契约

- `WatchEvents`：进程内通知通道，buffer=1（`:3853-3862`），满时丢弃并计数（`NotifyDropStats().Events`，已落地）。
- 类型契约注释明确：调用方必须用 `ListEvents(afterSeq)` 做 durable catch-up（`:195-199`、`:206-213`）。
- 因此通知本身可以合并/延迟，**但 durable 数据不得丢**；顺序由 `seq` 决定，不由通知次数决定。

### 0.5 为什么必须做

- 事件洪峰（流式 delta、工具输出、子代理进度）下，每次 publish 都要等待一次写事务（BEGIN/COMMIT + 写锁获取 + WAL 帧追加；注意 WAL + `synchronous=NORMAL` 下 **commit 不做 fsync**，见 §4），直接抬高 TUI/SSE 首帧延迟。
- 单连接池把「读 UI 状态」与「写事件」放在同一串行队列（P1.7 单独解决），批量写会把写侧对队列的占用次数降低 1~2 个数量级。
- 逐事件事务在磁盘抖动/多进程锁竞争下放大 P99；批量把 N 次锁等待压成 1 次。

---

## 1. 目标 / 非目标 / 成功度量

### 1.1 目标

| 编号 | 目标 |
| --- | --- |
| G1 | 提供 store 级批量写入 API：一次事务提交 N 个事件，seq 与单条写入完全等价（单调、无空洞、跨进程安全） |
| G2 | 桥接层以「有界缓冲 + 触发式 flush」调用批量 API，降低事务次数（P1.5） |
| G3 | 持久化从 `Bus.Publish` 的发布者 goroutine 中移出（P2.11），发布延迟与磁盘无关 |
| G4 | 队列/缓冲有界、溢出可解释（背压或计数丢弃 + 告警），关闭时 flush，绝不静默丢 |
| G5 | 双宿主共用一套实现；关闭开关后行为与现状逐字节一致 |

### 1.2 非目标

- 不改变 `session_events` 表结构与 retention 语义（prune 仍按 `eventRetention/pruneInterval`；prune 外移见 P1.6 分册）。
- 不改变 `Bus` 对其他订阅者的同步派发语义（仅持久化 handler 异步化；全局异步需另立方案，见 §10）。
- 不引入第二份「哪些事件必须落盘」清单：继续使用 `runtimeevents.IsPersistedEventType` 注册表（`internal/events/contract.go`）。
- 不改变 `payload["seq"]` 去重规则与 `WatchEvents` 的「通知可能丢、必须按 seq 追平」契约。
- 不做跨进程批量（每个进程独立批量；多进程同库由 SQLite 锁与 seq 分配保证正确性）。

### 1.3 成功度量（上线后 30 天）

| 指标 | 目标 | 观测方式 |
| --- | --- | --- |
| 事件写事务次数 / 事件数 | ≤ 1/32（batch 均值 ≥32） | 新增计数 `persist.batches`、`persist.events` |
| 发布者阻塞时间：`Publish`→返回 P95 | ≤ 1ms（原为一次写事务 RTT） | 新增直方图 `persist.dispatch_latency`（P2.11 后） |
| durable 丢失率（flush 后仍缺的事件） | 0 | `persist.dropped` 必须恒为 0；仅允许「队列超限时同步降级」不丢 |
| flush 尾部延迟：最后一个事件入队→落盘 P95 | ≤ 50ms（间隔 25ms 配置下） | `persist.flush_latency` |
| 崩溃窗口内未被持久化的事件数（进程被 kill -9） | ≤ batch 上限且可解释 | 故障注入测试 + runbook 说明 |
| 关闭/退出：unsubscribe 后队列清空 | 100%（有界超时内） | 单测 + `persist.shutdown_flush_timeout` 计数 |

---

## 2. 设计原则

1. **durable 优先于通知**：先提交事务，再发通知；通知丢失可由 `ListEvents(afterSeq)` 追平，数据丢失不可恢复。
2. **seq 是唯一顺序真源**：不引入 `ORDER BY timestamp` 或到达顺序依赖；同一 session 的批量内事件按入队顺序分配连续 seq。
3. **批内 all-or-nothing**：一个批量事务失败则整批回滚并重试（有限次 + 退避），不允许「部分事件已落盘、部分丢失」的静默状态。
4. **有界**：队列有容量上限；flush 有 deadline；重试有次数；关闭有超时。所有超限都有计数与限频日志。
5. **单实现双宿主**：缓冲/flush 逻辑实现一次（`internal/chat` 或 `internal/events` 包内），aicli 与 runtime-server 只做接线。
6. **可独立回滚**：P1.5（批量）与 P2.11（异步）分别是独立开关；任一关闭都不触发磁盘格式变更。

---

## 3. 详细设计

### 3.1 P1.5-A：store 批量写入 API

新增（`session_runtime_store.go`）：

```go
// AppendEvents stores a batch of events in one transaction.
// Returns the assigned seq for each event, in input order.
// All-or-nothing: on error no event of the batch is visible.
func (s *SQLiteRuntimeStore) AppendEvents(ctx context.Context, events []runtimeevents.Event) ([]int64, error)
```

实现要点：

1. **入口守卫**：`ensureCtx(ctx)` → `operationContext(ctx)` → `checkPoolReentry("AppendEvents")`（与单条一致；注意顺序：ensure 在前，见已落地的 P0.3 修订）。
2. **空批/单条**：`len==0` 直接返回；`len==1` 委托 `AppendEvent`，避免两套语义。
3. **批上限**：`maxAppendEventsBatch = 128`（常量）。超过由桥接层拆批；store 内对超限返回明确错误，防止一次事务持有写锁过久。
4. **seq 分配**：在事务内**按 session 分组**，每组一次 `SELECT COALESCE(MAX(seq),0) FROM session_events WHERE session_id = ?`，随后组内按输入顺序 `base+1, base+2, ...` 递增。理由：与单条写入的 `MAX(seq)+1` 完全同源，跨进程安全；批量内必须避免对同一行重复 `MAX`（会退化 + 语义漂移）。
5. **写入**：`tx.PrepareContext` 复用 `INSERT INTO session_events(...)`（`:2440-2445` 同 SQL），循环 Exec；`payload_json` 逐条 `json.Marshal`（失败即整批回滚并返回错误）。
6. **时间戳**：与单条一致，零值填充 `time.Now().UTC()`（同一批共享一个 now，保持批内一致）。
7. **prune**：整批只调用一次 `pruneRuntimeRowsTx(ctx, tx, sessionID, maxSeqOfBatch, 0)`，用批内最大 seq 判定（单条路径保持原样）。
8. **通知**：`COMMIT` 成功后，按输入顺序对每个事件 `notifyEventWatchers(seq, event)`（非阻塞、可丢、已计数）。若未来需要合并通知，需先改 `WatchEvents` 契约（见 §10 Q2）。
9. **错误分类**：`context.DeadlineExceeded`/SQLite busy 视为可重试；其他（schema、marshal）不可重试。重试策略由桥接层实现（§3.2），store 只保证批语义干净。

回归锚点：`TestSQLiteRuntimeStoreConcurrentInstancesDeduplicateMessageID`（多实例同库）与 seq 单调性测试必须在批量路径下补齐（§7）。

### 3.2 P1.5-B：桥接层有界缓冲（新组件 `eventPersistBuffer`）

位置：`backend/internal/chat/event_persist_buffer.go`（新文件，双宿主共用；不放进 `internal/events` 以避免 events 包依赖 chat store）。

```go
type EventPersistBatchStore interface {
    AppendEvents(ctx context.Context, events []runtimeevents.Event) ([]int64, error)
}

type EventPersistBufferConfig struct {
    BatchSize        int           // 默认 64
    FlushInterval    time.Duration // 默认 25ms
    QueueLimit       int           // 默认 4096 条
    QueueBytesLimit  int64         // 默认 16MiB（按 ApproximateEventBytes 估算）
    FlushTimeout     time.Duration // 默认 10s（复用 store OperationTimeout）
    ShutdownTimeout  time.Duration // 默认 2s
    CriticalTypes    func(eventType string) bool // 默认查 events 注册表新增的 PersistCritical 标记
}
```

行为：

- **入队**：`Enqueue(event)` 在调用方 goroutine 内做「非阻塞入队」；队列满 → 按 `FailMode` 处理：
  - `block`（默认）：同步落盘该事件（走 `AppendEvent`，带 `operationContext`），保证不丢、把背压还给生产者；
  - `drop`（仅在显式配置时）：计数 + 限频告警，示例用于纯 delta 类可重建事件。
- **触发 flush**：达到 `BatchSize`、或距上次 flush 超过 `FlushInterval`、或事件类型被 `CriticalTypes` 命中（例如 approval / run 终态 / 工具完成，具体清单在 `internal/events/contract.go` 注册表扩展字段，禁止第二份白名单）。
- **flush 执行**：单 worker（每 buffer 一个 goroutine）取一批 ≤`BatchSize`，调用 `AppendEvents`；失败时按 50ms→500ms 退避重试 ≤3 次，仍失败则记录 `persist.failed` 并**保留该批在队首**，等待下一次触发（不得丢弃）；连续失败超过阈值时进入「同步直写」降级模式并告警（避免队列无限积压）。
- **顺序**：同一 buffer 单 worker 串行处理，天然保证同 session 入队顺序 = seq 顺序。
- **关闭**：`Close(ctx)` 停表 + 尽力 flush（`ShutdownTimeout` 内）；超时则记录 `persist.shutdown_timeout` 并将剩余条数写入日志；宿主退出流程（aicli 的 `cleanupFns` / runtime-server 的 shutdown）显式调用。

接线：

- aicli：`bindRuntimeEventPersistence`（`chat_actor_host.go:871-897`）的 handler 从「直接 AppendEvent」改为 `buffer.Enqueue(mapped)`；保留 `payload["seq"]` 跳过规则与 `IsPersistedEventType` 判定（判定仍先做，避免把必然丢弃的事件入队）。
- runtime-server：`attachRuntimeEventBridge`（`handler.go:4056-4081`）同样接入；`recordRuntimeEventDeliveryDrop` 的未命中路径不变。
- 关闭接线：两宿主的既有 unmount/cleanup 路径追加 `buffer.Close(ctx)`（aicli：`h.cleanupFns`；runtime-server：handler 的关闭流程，实施时定位）。

### 3.3 P2.11：把持久化移出发布者 goroutine

P1.5-B 已经让 handler 只做「判定 + 入队」（O(1)），但 `Bus.Publish` 仍是同步调用 `Enqueue`。若 `Enqueue` 只做非阻塞操作，发布者已不再等待磁盘。本 Phase 在此基础上做两件事：

1. **队列写入零阻塞保证**：`Enqueue` 内部只做 map/ring 写入 + 触发信号（channel 非阻塞 send 或 `sync.Cond`），不调用任何 store 方法（`block` 降级模式除外，且降级只在队列满时发生，已有计数）。
2. **重试/降级全部转移到 worker**：发布路径只负责入队与计数；所有错误处理在 worker 内完成。

验收方式：`Publish`→返回的 P95 与磁盘延迟解耦（在 store 被故障注入拖慢时，发布者延迟不上升，队列深度上升）。

### 3.4 时序（文本）

```
生产者 goroutine                     persist worker            SQLite(write pool)
  publish(event)                        │                          │
  ├─ 判定 IsPersistedEventType          │                          │
  ├─ 跳过 payload["seq"]                │                          │
  └─ buffer.Enqueue(mapped) ──► queue ──┤                          │
     return（不等磁盘）                  │ 触发：batch/interval/critical
                                        ├─ AppendEvents(batch) ───►│ BEGIN
                                        │                          │ SELECT MAX(seq) per session
                                        │                          │ INSERT ×N
                                        │                          │ prune(maxSeq)
                                        │◄──── seq[] ──────────────┤ COMMIT
                                        ├─ notifyEventWatchers ×N  │
                                        └─ 失败：退避重试 ≤3，保序不丢
```

### 3.5 配置项

| 配置 | 默认 | 说明 |
| --- | --- | --- |
| `EventPersistBatchingEnabled` | `false`（灰度期默认关） | 关闭时桥接走原同步单条路径，行为逐字节不变 |
| `EventPersistBatchSize` | 64 | 单批上限（≤ store `maxAppendEventsBatch=128`） |
| `EventPersistFlushInterval` | 25ms | 尾部延迟与吞吐的平衡点 |
| `EventPersistQueueLimit` / `QueueBytesLimit` | 4096 / 16MiB | 溢出策略见 §3.2；默认 `block` 同步降级，不丢 |
| `EventPersistAsyncDispatch` | `false` | P2.11 独立开关；开启前置条件：batching=true |
| `EventPersistShutdownTimeout` | 2s | 退出 flush 上限 |

配置读取遵循仓内既有 config 约定（`internal/config`）；测试默认关闭，专项测试显式开启。

### 3.6 锁与事务模式（必须遵守：R1/R3/R4）

本节约束来自审查报告 `runtime-store-hardening-plan-review-20260918.md`，是 P1.5/P2.11 的**实施前置**：

1. **写事务一律 IMMEDIATE**：现状所有写事务用 `BeginTx(ctx, nil)`（driver 默认 `BEGIN` deferred），而事件/邮箱/租约路径是「先读后写」（`SELECT MAX(seq)` → `INSERT`，`session_runtime_store.go:1850/1908/2427/2533/2618/2739/3321`）。WAL 下跨写者连接会命中 `SQLITE_BUSY_SNAPSHOT(517)`（官方定义：另一连接已写入、先前读失效），**重试同一事务不可能成功**，`busy_timeout` 也不能使其成功。实施时改为 `BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})`——驱动映射为 `BEGIN IMMEDIATE`（`driver.go:336-360`）；DSN `_txlock=immediate` 为备选（需 URI 化 DSN，见 P1.7 §3.2）。
2. **517/BUSY 分类重试**：用 `sqlite3.Error.ExtendedCode()` 判定（`sqlite3.BUSY_SNAPSHOT`=517，`const.go:118`）；命中则**回滚后以新事务重试**（≤3 次，50→200ms 退避）+ 计数；禁止在同一事务内重试。
3. **多库（ATTACH）锁序不变量**：同一事务内同时写 session 库与 global mailbox 时，**必须先 global mailbox、后本地库**（现状两个写点均遵守：`session_runtime_store.go:2657→2667`、`team/sqlite_store.go:2349→2359`）。新增 ATTACH 写点必须遵守，否则跨进程双文件锁反序会互等到 `busy_timeout` 超时。
4. **批量锁持有时长预算**：单批写锁持有（事务开始→COMMIT 返回）目标 **P95 ≤5ms**；超限优先降 `EventPersistBatchSize`（64→32），不做无限拆批；指标 `persist.batch_lock_hold_ns`。
5. **桥接错误可见性**：runtime-server 桥接目前忽略 `AppendEvent` 错误（`internal/api/skills/handler.go:4078`），与 aicli 的限频告警不对称；实施批量/异步时必须对齐（或统一走 `eventPersistBuffer` 的失败路径），禁止静默丢弃。

---

## 4. 兼容与迁移

- **磁盘**：无格式变更；`session_events` 表结构、seq 语义、retention 不变。批量写入对旧库零迁移。
- **API**：新增 `AppendEvents` 与 `eventPersistBuffer`；既有 `AppendEvent` 语义不变（`len==1` 委托关系不对外暴露）。
- **行为兼容**：开关关闭时，桥接层代码路径与原实现完全一致（保留 `payload["seq"]` 跳过与 `IsPersistedEventType` 判定顺序）。
- **进程兼容**：同一 `session_runtime.sqlite` 可同时被「旧版逐条进程」与「新版批量进程」写入——两者都用 `MAX(seq)+1`，seq 单调性由 SQLite 事务保证（已有跨实例测试锚点）。
- **持久化语义（事实修正）**：WAL + `synchronous=NORMAL`（`session_runtime_store.go:4240-4246`）下 **commit 不做 fsync**。进程被 `kill -9` 不丢已提交的 WAL 帧（`write` 已到达 OS 页缓存）；**断电**可能丢「自上次 checkpoint 以来」的提交——这是现状既有语义，批量不放大。批量新增的窗口只是「已入队未提交」（≤25ms/64 条，§6）。
- **测试兼容**：既有测试通过 `store.appendEvent` 逐条断言 seq/事件数；批量路径新增独立测试，不改变既有断言。
- **灰度**：`EventPersistBatchingEnabled` 先只在一个宿主（建议 runtime-server）开启，观察 7 天 `persist.*` 指标与事件缺口再切 aicli；两宿主可独立回滚。

## 5. 可观测性

新增计数器/直方图（挂在 store 或 buffer，暴露方式对齐 `NotifyDropStats()` 的轻量访问器 + 调试端点）：

| 指标 | 含义 | 用途 |
| --- | --- | --- |
| `persist.enqueued` / `persist.flushed_events` | 入队/落盘事件数 | 缺口检测（差值应仅等于当前队列深度） |
| `persist.batches` | 批量事务次数 | 批均值 = flushed_events/batches |
| `persist.flush_latency`（P50/P95/P99） | 入队→COMMIT 完成 | 尾部延迟 |
| `persist.queue_depth` / `queue_bytes` | 当前队列 | 背压/积压告警 |
| `persist.sync_fallback` | 队列满触发的同步降级次数 | 容量是否不足 |
| `persist.retry` / `persist.failed` | 重试与最终失败批次数 | 磁盘/锁健康 |
| `persist.shutdown_timeout` | 退出时未 flush 完的条数 | 必须恒为 0 的告警项 |
| `persist.busy_errors` / `persist.busy_snapshot_errors` | SQLite `BUSY` / `BUSY_SNAPSHOT(517)` 次数 | 跨进程写争用与读→写升级失败（§3.6） |
| `persist.tx_retries` | 517/BUSY 触发的重试次数 | 重试是否有效（上限 3 次） |
| `persist.batch_lock_hold_ns` | 单批写锁持有时长 | 跨进程公平性；P95 目标 ≤5ms |
| `NotifyDropStats()`（已有） | 通知通道丢弃 | 与 durable 缺口区分：通知丢 ≠ 数据丢 |

告警建议：`persist.failed > 0`、`persist.shutdown_timeout > 0`、`queue_depth` 持续 > 80% 上限。

## 6. 风险与回滚

| 风险 | 等级 | 缓解 | 回滚 |
| --- | --- | --- | --- |
| 批量引入 seq 空洞/重复 | 高 | 批内单 tx；按 session 一次性取 MAX；专门的多实例并发测试；灰度期用「逐条写入进程」对照 seq 连续性 | 关闭 `EventPersistBatchingEnabled` |
| 进程崩溃（kill -9）丢失「已入队未提交」事件（≤25ms/64 条） | 中 | 关键事件类型强制同步落盘；runbook 明示窗口；接受度由评审决定（§9 D3）。断电窗口为「自上次 checkpoint 以来」，属现状既有（WAL+NORMAL），批量不放大 | 同上 |
| 未先修复 deferred 升级缺陷（517）就启用批量 → 失败被批量路径掩盖、事件静默丢失 | 高 | §3.6 作为实施前置（IMMEDIATE + 517 重试 + 错误可见性）；建议 P0.5 先行 | 关闭 batching；回退单条路径 |
| 批量放大写锁持有时长 → 跨进程（aicli/runtime-server/多实例）P99 恶化 | 中 | 批量上限 64；`batch_lock_hold_ns` P95 ≤5ms 预算；超限自动降批（64→32）；busy 计数告警 | 关闭 batching |
| 队列积压导致内存上涨 | 中 | 双上限（条数+字节）+ `block` 降级 + 深度告警 | 关闭 batching（回归逐条） |
| 关闭时尾部事件未落盘 | 中 | `Close` 有界 flush + 超时计数；宿主退出顺序显式化 | — |
| 通知次数变化影响消费者 | 中 | 本期不改通知契约（仍逐事件 notify）；消费者本就按 seq 追平 | — |
| 异步后事件顺序与其它订阅者观察到的不一致 | 中 | 仅持久化 handler 异步；其他 handler 保持同步派发顺序；同 session 单 worker 串行 | 关闭 `EventPersistAsyncDispatch` |
| 双宿主实现漂移 | 低 | 单实现共享类型；接线处的 golden 测试对比两宿主入队/落盘行为 | — |

## 7. 测试与验收（DoD）

单元测试：

1. `TestAppendEventsAssignsContiguousSeqsPerSession`：批内/跨 session 混合，断言各 session seq 连续、无空洞、返回顺序与输入一致。
2. `TestAppendEventsIsAtomicOnFailure`：注入一条 marshal 失败/约束冲突，断言整批不可见（`ListEvents` 前后数量不变）。
3. `TestAppendEventsRespectsMaxBatch`：超限返回错误，不产生部分写。
4. `TestEventPersistBufferFlushTriggers`：按 size / interval / critical 三种触发各一例。
5. `TestEventPersistBufferOverflowBlocksInsteadOfDropping`：队列满时同步降级，事件最终全部落盘、`sync_fallback==1`。
6. `TestEventPersistBufferRetriesOnBusyAndKeepsOrder`：注入 busy/timeout，断言重试后顺序不乱、无丢失。
7. `TestEventPersistBufferCloseFlushesTail`：关闭后 `ListEvents` 包含尾部事件；超时场景计数正确。
8. `TestEventPersistBufferSkipsAlreadyPersistedSeq`：`payload["seq"]` 事件不入队（契约保持）。

集成/端到端：

9. 双宿主桥接 golden：aicli（`bindRuntimeEventPersistence`）与 runtime-server（`attachRuntimeEventBridge`）对同一事件序列产生相同的落盘结果（类型别名映射、跳过规则一致）。
10. SSE/TUI 追平：批量开启后，消费者仅靠 `ListEvents(afterSeq)` 能拿到全部事件；`NotifyDropStats` 增加不影响最终视图。
11. 多进程：两个 store 实例（同文件）并发批量写，断言无重复 seq、无丢失（对齐既有 `TestSQLiteRuntimeStoreConcurrentInstancesDeduplicateMessageID`）。
12. 故障注入：写盘只读/磁盘满 → 重试计数上升但进程不崩；恢复后队列继续推进。
13. 压测：1k events/s × 60s，对比开关前后的写事务数（目标 ≥32× 降幅）、`Publish` P95（目标 ≤1ms）、flush P95（≤50ms）。
14. `TestAppendEventUsesImmediateTxNoBusySnapshot`：双 `*sql.DB` 句柄（或双进程）确定性复现「A 读 → B 写 → A 写」，断言 deferred 路径返回 517、IMMEDIATE 路径（+重试）成功，`busy_snapshot_errors` 计数正确。
15. `TestAppendEventsLockHoldBudget`：批 64 下统计 `batch_lock_hold_ns`，断言 P95 ≤5ms（CI 可用宽松阈值，压测报告给绝对值）。
16. `TestRuntimeServerBridgeSurfacesAppendErrors`：注入 store 失败，断言 runtime-server 侧产生限频告警/计数（与 aicli 对齐，§3.6 第 5 条）。

验收门槛：以上 1–16 全绿 + 灰度 7 天 `persist.failed==0 && persist.shutdown_timeout==0`（runtime-server 先行）。

## 8. 里程碑与工作量

| 里程碑 | 内容 | 依赖 | 估算 |
| --- | --- | --- | --- |
| M0（0.5d） | 度量先行：`AppendTimingStats`/批计数骨架 + 基线压测脚本 | 无 | 0.5d |
| M1（1.5d） | store `AppendEvents` + 单测（§7.1–3） | M0 | 1.5d |
| M2（1.5d） | `eventPersistBuffer` + 单测（§7.4–8） | M1 | 1.5d |
| M3（1d） | 双宿主接线 + 集成测试（§7.9–11） | M2 | 1d |
| M4（1d） | 灰度开关/配置/告警 + runbook 增补 | M3 | 1d |
| M5（0.5d） | 压测与验收报告 | M4 | 0.5d |

P2.11（`AsyncDispatch`）在 M3 之后可独立排 0.5–1d；建议观察一期再开。

## 9. 决策记录（评审基线）

| 编号 | 决策 | 状态 |
| --- | --- | --- |
| D1 | 通知保持逐事件（不做合并），消费者继续用 `ListEvents(afterSeq)` 追平 | 待评审（默认执行） |
| D2 | 批语义 all-or-nothing；批上限 128（store）/64（buffer） | 待评审（默认执行） |
| D3 | 关键事件类型（approval / run 终态 / 工具完成，具体清单落在 `events` 注册表）绕过批量、同步落盘 | 待评审（默认执行） |
| D4 | 默认关闭批量与异步，灰度期 runtime-server 先行 7 天 | 待评审（默认执行） |
| D5 | 队列溢出默认 `block`（同步降级、不丢），`drop` 仅显式配置 | 待评审（默认执行） |
| D6 | 崩溃窗口内 ≤25ms/64 条事件可丢，以 runbook 明示 | 待评审（默认执行；若要求 0 窗口则关键类型必须全覆盖） |
| D7 | 写事务使用 `LevelSerializable`（BEGIN IMMEDIATE）；517/BUSY 回滚后以新事务重试 ≤3 次（R1 前置，建议 P0.5 先行） | 待评审（默认执行） |
| D8 | 多库事务锁序固化为「global mailbox 先、本地库后」，新增 ATTACH 写点须遵守 | 待评审（默认执行） |

## 10. 开放问题

- Q1：`CriticalTypes` 的注册表字段命名与迁移（`contract.go` 是否有 `persist_critical` 概念的最自然落点）？
- Q2：是否要在后续版本把 `WatchEvents` 通知合并为「最新 seq 提示」以进一步降低通知开销？需要消费者侧（TUI/SSE）同步评审。
- Q3：批量的重试上限与「同步直写降级」阈值是否需要按宿主区分（TUI 交互 vs 服务端）？
- Q4：`AppendEvents` 是否需要跨 session 的全局写锁排序（当前仅按 session 分组取 seq，跨 session 无顺序约束）？
- Q5：517/BUSY 重试放在 store 层还是 buffer worker 层？（双层重试会放大等待；建议 store 负责事务级重试，buffer 只负责批次级保留与退避。）

---

## 11. 实施记录：P1.5 M0–M2（2026-09-18）

状态：**M0–M2 已完成并回归通过；M3（双宿主接线/配置）/M4/M5 待续**。决策 D1/D2/D5/D7 按基线执行；D3 以 `CriticalTypes` 钩子预留、注册表清单留待接线期落地。

### 11.1 改动清单

| 文件 | 内容 |
| --- | --- |
| `internal/chat/session_runtime_store_batch.go`（新增） | `AppendEvents`：空批直返、`len==1` 委托 `AppendEvent`、`>128` 明确报错；事务外校验+序列化；事务内按 session 分组一次 `MAX(seq)` 取基、组内 `base+i` 递增；`PrepareContext` 复用 INSERT；每 session 一次 prune；提交后逐事件 `notifyEventWatchers`；整批 `RetryWriteTx`（IMMEDIATE + 517/BUSY 新事务重试）；测试钩子 `runtimeAppendBatchHook` 用于 all-or-nothing 注入 |
| `internal/chat/event_persist_buffer.go`（新增） | `EventPersistBuffer` + `EventPersistBatchStore`：非阻塞入队、双上限（条数/字节，`ApproximateEventBytes` 估算）、`block`（默认，同步直写不退）与 `drop`（显式，计数）溢出策略、size/interval/critical 三种触发、单 worker 保序、失败保批 + 50→200→500ms 退避重试（≤3）、连续失败 ≥3 批进入同步降级、`Close` 有界 flush + `shutdown_timeout` 计数、`payload["seq"]` 跳过规则内置、`Stats()` 暴露 §5 指标 |
| `internal/chat/runtime_store_maintenance.go` | `AppendTimingStats` 增补 `Batches/BatchedEvents/BatchTotalNs/BatchLockHoldNs`（§3.6.4 的 `persist.batch_lock_hold_ns`） |
| 测试 | `runtime_store_batch_test.go`（5 例）、`event_persist_buffer_test.go`（7 例） |

### 11.2 测试清单对照（§7 DoD）

| # | 状态 | 说明 |
| --- | --- | --- |
| 1 | ✅ | `TestAppendEventsAssignsContiguousSeqsPerSession`：跨 session 交错批 → `[1,1,2,2,3,3]`，第二批衔接 `[4,5]` |
| 2 | ✅ | `TestAppendEventsIsAtomicOnFailure`：第 3 条注入失败 → 整批不可见；清除注入后同批 seq 从 1 重新开始（无空洞） |
| 3 | ✅ | `TestAppendEventsRespectsMaxBatch`：129 条报错且零写入；2 条正常 |
| 4 | ✅ | `TestEventPersistBufferFlushTriggers`：size / interval / critical 三触发 |
| 5 | ✅ | `TestEventPersistBufferOverflowBlocksInsteadOfDropping`（同步降级 + 关闭后总数不丢）与 `...DropsOnlyWhenConfigured` |
| 6 | ✅ | `TestEventPersistBufferRetriesOnBusyAndKeepsOrder`：首批注入 BUSY → 重试成功，顺序不变、无丢失 |
| 7 | ✅ | `TestEventPersistBufferCloseFlushesTail`（尾部全落盘 + 关闭后入队计数拒绝）与 `...CloseTimeoutCountsRemaining`（有界返回 + `shutdown_timeout` 计数） |
| 8 | ✅ | `TestEventPersistBufferSkipsAlreadyPersistedSeq` |
| 11 | ✅ | `TestAppendEventsConcurrentInstances`：同文件双实例分批并发 56 条，无重号无丢失 |
| 15 | ✅ | `TestAppendEventsLockHoldBudget`：批 64 的 `batch_lock_hold_ns` 绝对值写入测试日志，CI 断言有界（<500ms）；P95≤5ms 属灰度压测项 |
| 9/10/12/13/14/16 | ⏳ 待接线/压测 | 均依赖 M3 双宿主接线（9、10、16）或压测环境（12、13）；14 已由 P0.5 `TestAppendEventConcurrentInstancesNoBusySnapshot` 等价覆盖 |

回归：`go test ./internal/chat/ -count=1`、`go build ./...`、`go vet ./internal/chat/` 全绿。

### 11.3 落地注记与偏离

- **默认关闭已由「不接线」保证**：组件不自动接入任何宿主，`EventPersistBatchingEnabled=false` 时桥接保持原逐条路径；接线期再把配置与开关接到宿主。
- **block 溢出的顺序语义**：同步直写发生在生产者 goroutine，可能与队列中更早事件形成 seq 交错（计划 §3.2 已知）；不丢数据、不产生空洞，接线期在 runbook 中标注。
- **重试分层**：store 层 `RetryWriteTx` 负责事务级 517/BUSY（P0.5/§3.6.2），buffer 层只负责批次级保留与退避（≤3），符合 Q5 建议。
- **D3 关键类型**：`CriticalTypes` 钩子已就绪并测试（critical 触发立即 flush）；注册表字段（`contract.go` 的 `persist_critical`）与清单留待 M3 一并落地。
- 字节上限按 `events.ApproximateEventBytes` 估算；`pending` 批的字节数同样计入上限。

### 11.4 下一步（M3/M4/M5）

1. **M3 接线**：aicli `bindRuntimeEventPersistence`、runtime-server `attachRuntimeEventBridge` 改为「判定 → `buffer.Enqueue`」；两宿主 shutdown/cleanup 显式 `buffer.Close(ctx)`；runtime-server 侧补错误可见性（§3.6.5）。
2. **M4**：`runtimecfg.RuntimeConfig`（`SessionRuntime` 段）+ `internal/config` 增加 §3.5 六项配置与解析；调试端点暴露 `persist.*`。
3. **M5**：1k events/s × 60s 压测（批均值、Publish P95、flush P95、`batch_lock_hold_ns` P95）与灰度验收；同时复测 P1.6 的单事件时延目标。

---

## 12. 实施记录：P1.5 M3 + M4（配置部分）（2026-09-18）

状态：**M3 双宿主接线完成并回归通过；M4 的配置项已落地，调试端点暴露与 runbook 待续**。默认关闭（`batchingEnabled: false`）时两宿主保持原逐条同步路径。

### 12.1 改动清单

| 文件 | 内容 |
| --- | --- |
| `internal/config/manager.go` | `SessionRuntimeConfig.EventPersist` + `EventPersistConfig`（batchingEnabled / batchSize / flushInterval / queueLimit / queueBytesLimit / shutdownTimeout / failMode / asyncDispatch；零值即默认关闭） |
| `internal/chat/event_persist_buffer.go` | `EventPersistSettings`（宿主配置中性映射，双宿主共用）+ `BufferConfig()` / `ShutdownTimeoutOrDefault()` |
| `cmd/aicli/commands/chat_actor_host.go` | `runtimeEventPersistSettings()`；`bindRuntimeEventPersistence`：开关开启且 store 支持 `AppendEvents` 时创建 buffer，handler 改为 `Enqueue`；store 不支持时告警并保持同步路径；`cleanupFns` 逆序执行 → 先退订、后 `buffer.Close` |
| `internal/api/skills/handler.go` | `runtimeEventPersistMu`/`runtimeEventPersistBuffer` 字段；`attachRuntimeEventBridge` 改为「判定 → buffer.Enqueue」或原同步路径；`runtimeEventPersistSettings()` / `newRuntimeEventPersistBuffer()` / `CloseRuntimeEventPersistence()`（缺省 2s 有界 flush） |
| `cmd/runtime-server/main.go` | `runtimeServerApp.close()` 在关库前调用 `CloseRuntimeEventPersistence()` |

### 12.2 测试对照（§7 DoD 增补）

| # | 状态 | 说明 |
| --- | --- | --- |
| 9 | ✅（双宿主行为对齐） | aicli：`TestLocalRuntimeEventBridgeBatchesAndFlushesOnClose` / `...SyncPathWhenDisabled` / `...KeepsPersistedSeqSkipRule`；runtime-server：`TestRuntimeServerEventBridgeBatchesAndClosesCleanly` / `...SyncWhenDisabled` |
| 10 | ≈ 等价覆盖 | 断言只走 `ListEvents`（durable 追平）即可拿到全部事件；通知链路未改（D1） |
| 16 | ✅（分支覆盖） | runtime-server 同步路径仍走 `recordRuntimeEventPersistError`；批量路径失败由 buffer 的 `failed` 计数 + 限频告警承担（`TestEventPersistBufferRetriesOnBusyAndKeepsOrder` / `...CloseTimeoutCountsRemaining`） |
| 12/13 | ⏳ 待压测 | 故障注入与 1k events/s 压测属 M5 |
| 14 | ✅ | P0.5 锚点（IMMEDIATE + 517=0）不变，本轮全量回归覆盖 |

回归：`go test ./internal/chat/`、`./internal/api/skills/`、`./cmd/aicli/commands/`（全量）、`./internal/config/`、`go build ./...` 全绿（默认关闭状态，即开关关闭时既有行为逐字节保持）。

### 12.3 落地注记

- **默认安全**：`EventPersistConfig` 零值（`batchingEnabled=false`）不创建 buffer；两宿主在关闭态与接线前代码路径一致（handler 判定顺序不变：空 session → payload[seq] → 注册表判定）。
- **store 不支持批量时**（如内存 store）：告警一次并回退同步路径，不静默丢（§3.6.5 对齐）。
- **shutdown 顺序**：aicli 由 `cleanupFns` 逆序保证「退订 → flush → 关 store」；runtime-server 由 `app.close()` 显式先 flush。
- **AsyncDispatch**：配置与统计字段已就绪，但尚未改变派发时机（`Bus.Publish` 仍同步调用 `Enqueue`；Enqueue 本身零磁盘等待）。P2.11 独立分册实施时启用。

### 12.4 剩余（M4 尾 + M5）

1. `persist.*` 计数接入调试端点（对齐 `NotifyDropStats()` 的访问器风格）；runbook 增补崩溃窗口 §6 与灰度步骤。
2. D3 关键类型：`events` 注册表增加 `persist_critical` 字段与清单，接线为 `EventPersistSettings.CriticalTypes`。
3. M5 压测：1k events/s × 60s（批均值、Publish P95、flush P95、`batch_lock_hold_ns` P95），并复测 P1.6 时延目标。

---

## 13. 实施记录：P1.5 M4 尾 + D3 + M5 验收（2026-09-18）

状态：**P1.5 全部实施项完成**（M5 压测通过；仅 aicli `/debug` 端点可视化列为可选后续）。默认关闭不变。

### 13.1 改动清单

| 文件 | 内容 |
| --- | --- |
| `internal/events/contract.go` | `Contract.PersistCritical` 字段 + `IsPersistCriticalEventType()`；标记 `tool.completed` / `approval_requested` / `approval_resolved` / `session_start` / `session_end` / `session_interrupted` / `checkpoint_created`（D3 清单落注册表，单一真源） |
| 两宿主 settings | `CriticalTypes: runtimeevents.IsPersistCriticalEventType`（命中立即触发 flush） |
| `internal/api/skills/execution_diagnostics.go` + `handler.go` | 健康快照在启用批量时附带 `persist`（`EventPersistBufferStats` 只读快照）；关闭时不出现该段，响应形状不变 |
| `internal/chat/event_persist_buffer.go` | flush 延迟滑动窗口（512）与 `flush_p95_ns`；worker 续刷策略修正为「整批就绪才续刷」（`flushOnce`/`flushBatch`/`hasFullBatch`），drain 仍逐批推进 |
| 测试 | `contract_persist_critical_test.go`、`TestEventPersistBufferLoad1k`（env 门控）、health persist 两例 |

### 13.2 M5 压测验收（1k events/s × 60s，真实 SQLite + WAL）

命令（可复现）：

```powershell
$env:AICLI_RUNTIME_LOAD_TEST=1; go test ./internal/chat -run TestEventPersistBufferLoad1k -v -timeout 300s
# 快速复测（20s）与批参数覆盖（验证「写事务压缩 ≥32×」）：
$env:AICLI_RUNTIME_LOAD_TEST_SECONDS=20; $env:AICLI_RUNTIME_LOAD_TEST_FLUSH_MS=40; go test ./internal/chat -run TestEventPersistBufferLoad1k -v -timeout 300s
```

2026-09-18 本机（Windows，16 核）实测：

| 指标 | 实测 | 目标（§1.3） | 判定 |
| --- | --- | --- | --- |
| 入队/落盘 | 60,000 / 60,000（rate=1000/s，零丢失） | durable 丢失率 0 | ✅ |
| 写事务次数 / 批均值 | 2,400 批，avg_batch = **25.0** | ≤1/32 事件 | ⚠ 25×（见 13.3） |
| flush 延迟 P95 | **1.566ms**（max 14.14ms） | ≤50ms | ✅ |
| 发布链阻塞（max Enqueue） | **1.001ms** | Publish P95 ≤1ms（P2.11 后） | ✅（P2.11 开启后正式验） |
| 写锁持有 | 批总量 2.478s；≈1.03ms/批、41µs/事件 | 批 P95 ≤5ms | ✅ |
| failed / dropped / sync_fallback / retry | 0 / 0 / 0 / 0 | failed=0、shutdown_timeout=0 | ✅ |
| 关闭 flush | `Close` 返回 nil，shutdown_timeout=0 | 100% 有界 flush | ✅ |

结论：**丢事件率 0、flush P95 优于目标 30 倍、发布链零磁盘等待**；写事务压缩 25×。

批参数扫描（2026-09-18 复测，20s/次，其余配置同上；测试新增 `AICLI_RUNTIME_LOAD_TEST_FLUSH_MS`/`_BATCH` 旋钮）：

| `flushInterval` | 批均值（≈写事务压缩倍率） | flush P95 | failed/dropped | 判定 |
| --- | --- | --- | --- | --- |
| 25ms（默认） | 25.0 | 1.566ms | 0/0 | 尾部延迟优先 |
| 32ms | 31.9 | 1.731ms | 0/0 | 贴线（未严格达标） |
| **40ms** | **39.9** | 2.098ms | 0/0 | **≥32× 达标 ✅** |

结论：`≥32×` 目标以 **`flushInterval=40ms`** 稳定达成（39.9×），且 flush P95 仍比 50ms 目标低 24×；

### 13.3 与 §1.3 目标的偏差说明（诚实口径）

- 1k/s × 25ms 窗口的理论批均值就是 25，实测精确命中；**≥32× 需要 `FlushInterval ≥32ms`**（32ms 实测 31.9× 贴线，40ms 实测 39.9× 达标），批大小仍受 `BatchSize=64` 限制。默认值维持 25ms（尾部延迟优先），要在灰度中换取 ≥32× 写事务压缩就把 `sessionRuntime.eventPersist.flushInterval` 调到 `40ms`。
- 8 月初版实现（排空即续刷）实测 avg_batch=10.1、批 5,968 次；本次修正后为 25.0、2,400 次，**同一负载下事务数再降 2.5×**。该修正同时保留突发语义：队列 ≥BatchSize 时连续整批推进。
- 压测把 `EventRetention` 放大到 1e6 以避免 retention 裁剪干扰「不丢」断言；生产 retention 语义未变。

### 13.4 D3 落地清单（注册表 `PersistCritical`）

`tool.completed`、`approval_requested`、`approval_resolved`、`session_start`、`session_end`、`session_interrupted`、`checkpoint_created`。

语义：命中即 signal → worker 立刻取批（含此前排队事件）落盘，把崩溃窗口从「≤25ms/≤64 条」压缩到「当前队列」。未登记类型/非 A 通道类型恒为 false（`TestPersistCriticalRegistry` 门禁）。`tool.requested`、`assistant_delta` 等高频/可重建事件**不**在清单内，避免牺牲批量收益。

### 13.5 可观测性接入现状

- runtime-server：`/api/runtime/health`（execution diagnostics 快照）在启用批量时返回 `persist`（enqueued/flushed/batches/flush_p95/failed/dropped/queue_depth/…），关闭时不出现该键。
- aicli：宿主 `runtimeEventBuffer.Stats()` 已接入调试文档——`/debug display`（TUI）与 `/web/api/status?format=text` 的"存储与持久化:"区块输出 `批量落盘: enabled=true async=… enqueued=… flushed=… batches=… avg_batch=… flush_p95=… queue=… failed=… dropped=… retry=… sync_fallback=…`（缓冲未启用时输出 `批量落盘: disabled`）；`/web/api/status`（JSON）的 `storage.persist` 段直接序列化 `EventPersistBufferStats`（字段契约与 runtime-server health 的 `persist` 段同构）。实现见 `cmd/aicli/commands/chat_debug_storage.go`。
- 告警建议不变：`failed > 0`、`shutdown_timeout > 0`、`queue_depth` 持续 >80% 上限。

### 13.6 运行手册（灰度/回滚）

1. **开启（建议 runtime-server 先行 7 天）**：在配置 `sessionRuntime.eventPersist` 写入
   ```yaml
   sessionRuntime:
     eventPersist:
       batchingEnabled: true
       batchSize: 64            # 可选，默认 64
       flushInterval: 25ms      # 可选；吞吐优先可调 32-50ms
       queueLimit: 4096         # 可选
       queueBytesLimit: 16777216
       shutdownTimeout: 2s
       failMode: block          # 默认；drop 仅用于可重建 delta 且需显式接受丢弃
       asyncDispatch: false     # P2.11 独立开关，本阶段保持 false
   ```
2. **观察**：`/api/runtime/health → persist`；重点 `failed`、`shutdown_timeout`（必须为 0）、`queue_depth`（<80%）、`flush_p95`（≤50ms）。
3. **崩溃窗口语义**（§6 复述）：已入队未提交事件在 `kill -9` 下最多丢「当前批」；断电窗口为「自上次 checkpoint 以来」（WAL+NORMAL 既有语义，批量不放大）。关键类型即时 flush，窗口≈当前队列。
4. **回滚**：`batchingEnabled: false`（两宿主独立、即时生效，无需重启磁盘格式迁移）；回滚后桥接回到逐条同步路径，与接线前行为一致。
5. **故障处置**：`failed>0` 持续 → 查磁盘/权限与 SQLite busy；缓冲会保序重试并自动进入同步降级，不丢事件；必要时关闭开关止血。

---

## 14. 实施记录：P2.11 发布链异步派发（2026-09-18）

状态：**实施完成**；`EventPersistAsyncDispatch` 默认关闭，前置条件 `batchingEnabled=true`（不满足时两宿主告警并忽略）。

### 14.1 语义（相对 P1.5 的增量）

| 维度 | P1.5（AsyncDispatch=false） | P2.11（AsyncDispatch=true） |
| --- | --- | --- |
| 正常入队 | 队列写入 + 触发信号（零磁盘） | 同左（逐字节一致） |
| 队列满 | 立即同步兜底写（发布者做磁盘 I/O，计数 `sync_fallback`） | 先等 worker 腾挪（`DispatchWaitTimeout`，默认 250ms）；等到空位则重试入队（计 `dispatch_waits`），超时才同步兜底（计 `dispatch_wait_timeouts` + `sync_fallback`） |
| degraded（连续失败） | 同步兜底 | 同左（degraded 是失败态，直接兜底不等待） |
| 关闭/停止 | 有界 drain | 同左；等待中的发布链被 `stopCh` 立即唤醒 |

实现要点：
- `Enqueue` 拆为「快路径 `tryEnqueue`（只做队列/字节计数+信号）+ 溢出策略分支」；新增 `spaceCh`（容量 1，`takeBatch` 取走一批后非阻塞唤醒），`waitForQueueSpace` 有界等待（timer/stopCh 双出口）。
- 新增派发延迟滑动窗口：`dispatch_p95_ns` / `dispatch_max_ns` / `dispatch_waits` / `dispatch_wait_timeouts`；仅异步模式记录（默认路径零成本）。
- 两宿主接线处新增前置条件告警：`asyncDispatch=true` 且 `batchingEnabled=false` 时忽略并告警（§3.5 约束）。

### 14.2 测试

| 用例 | 断言 |
| --- | --- |
| `TestEventPersistBufferAsyncDispatchWaitsForSpaceInsteadOfSyncWrite` | 队列满→等位成功→`dispatch_waits=1`、`sync_fallback=0`、零丢失 |
| `TestEventPersistBufferAsyncWaitTimeoutFallsBackAndCounts` | 等不到空位→超时计数→同步兜底，事件不丢 |
| `TestEventPersistBufferSyncModeStillWritesInlineWhenFull` | 关闭异步时语义与 P1.5 一致（`sync_fallback=1`） |
| `TestEventPersistBufferAsyncDispatchDecouplesPublishFromSlowStore`（§3.3 验收） | store 阻塞 ≥150ms 时发布链 max <10ms、`dispatch_p95 <10ms`、`flush_max ≥150ms`、零失败 |

### 14.3 M5 复测（1k events/s × 60s，AsyncDispatch=true）

```powershell
$env:AICLI_RUNTIME_LOAD_TEST=1; go test ./internal/chat -run TestEventPersistBufferLoad1k -v -timeout 300s
```

| 指标 | 实测 | 目标 |
| --- | --- | --- |
| 入队/落盘 | 60,000 / 60,000（零丢失） | 丢失率 0 ✅ |
| 批均值 / 写事务 | 25.0 / 2,401 批 | 25×（见 §13.3 口径） |
| flush P95 / max | 2.33ms / 22.05ms | ≤50ms ✅ |
| **dispatch P95（Publish→return）** | **<1µs（显示 0s）**，max 646µs | ≤1ms ✅ |
| waits / wait_timeouts | 0 / 0 | — |
| 写锁持有 | ≈1.24ms/批、49µs/事件 | 批 P95 ≤5ms ✅ |

### 14.4 顺带修复（同一工作树内的在途改动，非本分册范围）

`chat_runtime_events_stream_coalesce_test.go` 的 `TestEnqueueStreamEventBoundsPendingBytes` 仍是旧口径（assistant 文本增量超字节预算即丢弃），与同文件已更新的新策略（assistant 文本增量不允许丢弃、超限保留积压）冲突。已按新策略更新该用例（重命名 `TestEnqueueStreamEventKeepsAssistantDeltasOverByteBudget`），并补非 assistant 类型仍按硬预算丢弃的断言。

### 14.5 遗留

- aicli `/debug` 的 persist 段可视化（runtime-server 已在 `/api/runtime/health` 暴露；aicli 仅 Go 访问器）。
- Q3（按宿主区分重试/降级阈值）与 Q5（重试分层）仍按 §10 开放问题跟踪。
