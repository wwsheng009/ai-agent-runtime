# Runtime Store 加固方案审查报告（完整性 / 数据库锁 / 性能）

- 版本：v1.0（2026-09-18 审查稿）
- 状态：审查结论已回写到对应分册；R1 为**现有代码的阻断级缺陷**，建议作为 P0.5 先行修复
- 审查对象：
  - `docs/plan/runtime-store-event-persistence-batching-and-async-plan-20260918.md`（P1.5/P2.11）
  - `docs/plan/runtime-store-append-hotpath-optimization-plan-20260918.md`（P1.6）
  - `docs/plan/runtime-store-read-write-pool-split-plan-20260918.md`（P1.7）
  - `docs/plan/sqlite-session-snapshot-hardening-plan-20260918.md`（P2.13）
  - `docs/plan/runtime-store-hardening-followup-index-20260918.md`
- 审查方法：代码事实核查（行号为 2026-09-18 工作区快照）+ SQLite 官方语义核对（rescode 517、WAL）+ 驱动能力核对（`ncruces/go-sqlite3 v0.32.0` driver.go）+ 方案逐项完整性核对表（§3）

---

## 0. 结论摘要

1. **R1（阻断，现有代码而非方案）**：所有写事务都用 `BeginTx(ctx, nil)`（driver 默认 `BEGIN` deferred），且其中事件/邮箱/租约路径是「先读后写」。在 WAL + 多写者进程（aicli 与 runtime-server 共享会话库、两个 aicli 实例、team store 与 chat 共享 global mailbox）下，读→写升级会返回 `SQLITE_BUSY_SNAPSHOT(517)`：官方语义为「另一连接已写入使先前读失效」，**重试同一事务不可能成功**，必须回滚后用 `BEGIN IMMEDIATE` 重开。当前 runtime-server 桥接对 `AppendEvent` 错误是 `_, _ =`（忽略）→ **事件静默丢失**；aicli 侧仅限频告警。三份性能方案都建立在这条现有路径上，若不先修，批量/异步会掩盖而不是解决丢事件。
2. **R2（高，方案缺陷）**：P1.6 维护任务的「通过 `s.mu` 串行化」是反模式——`s.mu` 只覆盖部分写方法（事件/邮箱/修复），且维护持锁会把 `AppendEvent` 阻塞整个 vacuum 时长，与「不增加热路径负担」目标冲突。正确做法是**不持 `s.mu`**，靠写池单连接 + SQLite 写锁串行化，并用「空闲门槛 + 小步 vacuum + BUSY 退避」控制抢占。
3. **R3–R6（中）**：多库（ATTACH）事务锁序未在方案中固化为不变量；批量写锁持有时长无预算；读池的 WAL 增长/检查点饥饿与内存预算缺失；快照与检查点/进程被杀残留的锁交互只写了一半。
4. **R7（中，事实性错误）**：P1.5 分册「每次 publish 等待一次 fsync」不成立——存储是 `journal_mode=WAL` + `synchronous=NORMAL`，**commit 不 fsync**（checkpoint 时才同步）。批量收益主要来自事务次数/锁获取/WAL 帧与语句开销，收益估算与崩溃窗口表述都必须修正。
5. **R8–R10（低-中）**：错误可见性不对称（runtime-server 忽略错误）；锁相关指标（BUSY/BUSY_SNAPSHOT/锁持有时长/WAL 大小）缺失；语句缓存决策未记录；跨进程测试建议补真双进程用例。

---

## 1. 锁分析（事实基线）

### 1.1 锁层次

| 层 | 机制 | 作用域 | 证据 |
| --- | --- | --- | --- |
| L1 进程内邮箱写锁 | `mailboxWriteMu` | 全局邮箱写入 | `runtime_store_hardening.go` 注释的锁序（`mailboxWriteMu → mu → 池连接`） |
| L2 进程内 store 写锁 | `s.mu` | **仅部分写方法**（事件/邮箱/global mailbox/修复） | `:2426, 2532, 2589, 2738, 3063, 3091, 3122, 3149, 3279, 3319`；`SaveState`（`:2217` 直接 `ExecContext`）、`DeleteState`、工具收据、租约（`:1850/:1908`）**不持** |
| L3 进程内连接池 | `SetMaxOpenConns(1)` | 全 store | `:1744-1745`、`:4204-4205` |
| L4 跨进程 DB 锁 | SQLite 文件锁（WAL 写锁 + 读标记） | 单文件 | WAL 启用 `:4240-4246` |
| L5 跨进程多库锁 | 同一事务内 session 库 + ATTACH 的 global mailbox 库 | 两文件 | `session_runtime_store.go:2611`、`team/sqlite_store.go:2325` |

关键结论：**进程内写串行化实际由 L3（单连接池）保证，而不是 `s.mu`**。方案（尤其 P1.6 维护任务、P1.7 双池）若把 `s.mu` 当作全局写锁来设计，会同时产生「锁不住」与「过度锁」两类错误。

### 1.2 写事务模式矩阵（现状）

| 方法 | 事务入口 | 首语句 | 读→写升级风险 | 持 `s.mu` |
| --- | --- | --- | --- | --- |
| `AcquireLease` `:1834` | `BeginTx(ctx,nil)` `:1850` | `SELECT`（现有租约 `:1856`）→ upsert `:1866` | **有** | 否 |
| `RenewLease` `:1887` | `BeginTx(ctx,nil)` `:1908` | 需复核（若先 SELECT 则有） | 可能 | 否 |
| `AppendEvent` `:2404` | `BeginTx(ctx,nil)` `:2427` | `SELECT MAX(seq)` `:2433` → INSERT `:2440` | **有** | 是 `:2426` |
| `AppendMailbox` `:2498` | `BeginTx(ctx,nil)` `:2533` | `SELECT MAX`（seq）→ INSERT | **有** | 是 `:2532` |
| `appendAgentControlMailboxSameTx` `:2582` | `conn.BeginTx(ctx,nil)` `:2618` | 写 global mailbox `:2657` → 写本地库 `:2667` | 写-写，无升级；**有多库锁序** | 是 `:2589` |
| `AppendAgentControlMailbox` `:2700` | `BeginTx(ctx,nil)` `:2739` | `SELECT MAX` → INSERT | **有** | 是 `:2738` |
| `repairRuntimeMailboxLocalProjectionRecord` `:3315` | `BeginTx(ctx,nil)` `:3321` | 复核 | 可能 | 是 `:3319` |
| `SaveState` `:2130` | 单语句 autocommit `:2217` | `INSERT ... ON CONFLICT`（纯写） | 无 | 否 |
| 工具收据/`DeleteState` | 单语句 autocommit | 纯写/删除 | 无 | 否 |

驱动事实（`driver.go:180-190`、`:336-360`）：`_txlock` 缺省为空 → `BEGIN`（deferred）；`sql.TxOptions{Isolation: sql.LevelSerializable}` 映射为 `BEGIN IMMEDIATE`；`ReadOnly` 事务额外 `PRAGMA query_only=on`。

### 1.3 多库（ATTACH）事务锁序

两个写点当前顺序**一致**（global mailbox 先、本地库后），这是必须固化的不变量：

- chat：`session_runtime_store.go:2611` ATTACH → `:2657` 写 global mailbox → `:2667` 写本地库。
- team：`sqlite_store.go:2325` ATTACH → `:2349` 写 global mailbox → `:2359` 写 team 库。

风险：未来任一新写点若「先本地库、后 global mailbox」，与既有写点并发即构成跨进程两文件锁的反序，双方各自等待对方持有的第二个锁 → 直到 `busy_timeout`(5s) 超时失败。方案中未固化该不变量（R3）。

### 1.4 检查点与读事务

- `Close()`：`PRAGMA wal_checkpoint(TRUNCATE)` → 失败降级 `PASSIVE`（`:1808-1821`）。TRUNCATE 需要**所有**读事务结束（含其它进程的读标记）。
- P1.7 读池：读事务结束时读标记释放，checkpoint 可推进；**长读查询**（如大 `limit` 的 `ListEvents`）会拖住 checkpoint → WAL 增长（R5）。`journal_size_limit=16MiB` 只在 checkpoint 后截断，不能限制增长。
- P2.13 快照：`SnapshotSession` 在专用连接上开读事务（`sqlite_storage.go:270-283`），会阻塞会话存储 `CloseStorage` 的 TRUNCATE checkpoint（`:321-324`）；跨进程快照同理会拖住 runtime store 的 Close 降级路径（R6）。
- `VACUUM INTO`（全库快照 `:228`）同样以读事务形式持有读标记。

### 1.5 `SQLITE_BUSY_SNAPSHOT(517)` 专项

官方定义（sqlite.org/rescode.html 517）：

> WAL 模式下，一个连接试图把读事务提升为写事务，但发现另一个连接已经写入、先前读已失效 → 返回 `SQLITE_BUSY_SNAPSHOT`；场景：进程 A 开始读事务并 SELECT，进程 B 更新数据库，进程 A 再尝试写入。

触发前提是**两个写者连接**（跨进程或跨 `*sql.DB` 句柄）。本仓明确存在该场景：
- `docs/plan/runtime-server-aicli-shared-session-mechanism-plan.md`（共享会话库）；
- 多 aicli 实例同 workspace；
- chat runtime store 与 team store 共享 global mailbox 文件。

失败放大路径：
1. 事件/邮箱 append 报 `database is locked (517)`；
2. runtime-server 桥接忽略错误（`internal/api/skills/handler.go:4078` `_, _ = store.AppendEvent`）→ **durable 丢失且无计数**；
3. aicli 桥接仅限频告警（`chat_actor_host.go:892-900`）→ 可见但仍在丢；
4. 该错误**不能**靠重试同一事务解决，`busy_timeout` 也无法使其成功（快照已过期），必须 `ROLLBACK` + 新事务 + `BEGIN IMMEDIATE`。

修复（P0.5，建议并入 P1.5/P1.6 的前置补丁）：
- 所有 read-then-write 的写事务改 `BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})`（driver 映射 `BEGIN IMMEDIATE`）；或路径 DSN 加 `_txlock=immediate`（需 URI 化 DSN，见 §2.6，改动面更大，推荐前者）。
- 错误分类：`sqlite3.Error` 的 `ExtendedCode()==sqlite3.BUSY_SNAPSHOT`（`error.go:13-30`、`const.go:118`）→ 回滚后重试（≤3 次，50→200ms 退避）+ 计数；`BUSY`/`BUSY_TIMEOUT` 同策略。
- 桥接错误可见性对齐：runtime-server 复用 aicli 的限频告警；新增 `busy`/`busy_snapshot` 计数。
- 测试：双 `*sql.DB` 句柄（或双进程）确定性复现 517，并断言 IMMEDIATE 路径下不再出现；断言重试计数。

> 影响面确认：该缺陷与 P0 已修的「池重入死锁」是两类独立故障；P0 修复不会消除 517，因为成因是**跨连接 WAL 快照**而非池自锁。

---

## 2. 性能分析

### 2.1 现状成本模型（WAL + synchronous=NORMAL）

`init` 设置 `journal_mode=WAL`、`synchronous=NORMAL`、`wal_autocheckpoint=256`、`journal_size_limit=16MiB`（`:4240-4246`）。在 WAL + NORMAL 下：

- **commit 不做 fsync**（同步发生在 checkpoint 前/时），所以「每事件一次 fsync」不是当前成本；
- 单事件 append 的主要成本 ≈ `BEGIN`/`COMMIT` 两个往返 + 写锁获取/释放 + WAL 帧追加（write() 系统调用路径）+ 语句准备与执行（`tx.QueryRowContext` 每次重新 prepare）+ JSON marshal；
- 跨进程存在写锁争用时，成本 = 锁等待（busy_timeout 内）+ 获取失败/升级失败（517）。

因此：
- P1.5 批量化的正确收益表述是「把 N 次 BEGIN/COMMIT/锁获取/语句准备合并为 1 次」；预期事务次数降幅 ≥32×（按批 64），**但单事件平均耗时降幅取决于锁与提交占比，需 M0 实测，不应预设 30×**；
- P2.11 把落盘移出发布 goroutine 的收益最大（发布延迟与磁盘/锁解耦），这也是文档 §1.3 「Publish P95 ≤1ms」指标成立的前提（当前每条 publish 至少等一次 COMMIT）。

### 2.2 P1.5 分册的具体事实性错误（已回写修正）

| 位置 | 原文 | 事实 | 修正 |
| --- | --- | --- | --- |
| §0.5 | 「每次 publish 等待一次 fsync/事务」 | WAL+NORMAL 下 commit 不 fsync | 改为「等待一次写事务（BEGIN/COMMIT + 写锁 + WAL 帧）」 |
| §6 风险表 | 「进程崩溃丢失批窗口内事件（≤25ms/64 条）」 | kill -9 不丢已提交 WAL 帧（OS 页缓存已 write）；断电才丢「自上次 checkpoint 以来」且为现状既有 | 区分「进程崩溃 = 未提交队列 ≤25ms/64 条」与「断电 = 自上次 checkpoint（既有语义，批量不放大）」 |

### 2.3 批量写锁持有时长（方案缺失，R4）

批量事务把 N 次短锁合并为 1 次长锁；在双写者进程下，长锁直接提升对方 P99：

- 预算：单批写锁持有 P95 ≤5ms（本地 SSD、批 64、单表）；M0 压测测出基线后再定；
- 超限策略：降低 `EventPersistBatchSize`（64→32）而不是无限拆批；
- 度量：`persist.batch_lock_hold_ns`（事务开始→COMMIT 返回）与 `persist.batch_size` 分布；
- 公平性：连续高负载下若对方进程持续 `BUSY`，需告警而非静默重试（busy 计数）。

### 2.4 维护任务（`incremental_vacuum`）的锁成本（R2）

现状：`pruneRuntimeRowsTx` 在写事务内执行 `PRAGMA incremental_vacuum(64)`（`:2488-2492`）——最多移动 64 页，期间持有写锁（跨进程阻塞其它写者），并在 WAL 中产生帧。

方案缺陷（Doc 2 §3.2）：写成「通过 `s.mu` 串行化」。实际影响：

- `s.mu` 不覆盖 `SaveState`/租约/工具收据（§1.2），**锁不住全部写**；
- 更严重的是反向问题：若维护任务持 `s.mu` 执行 vacuum，则 `AppendEvent`/`AppendMailbox` 会被阻塞整个 vacuum 时长（几十 ms 级），把「外移以保护热路径」变成「外移但阻塞热路径」。

修正设计（已回写 Doc 2）：

1. 维护任务**不持 `s.mu`**；串行化由写池单连接 + SQLite 写锁提供；
2. 空闲门槛：距上次 append/邮箱写 > `maintenanceIdleGap`（建议 200ms）才执行，避免与交互写抢占；
3. 小步执行：每轮 `incremental_vacuum(16)`，`busy_timeout` 内失败即退避（500ms→2s），单轮 deadline（≤2s）；
4. single-flight + 最小间隔 5s（保留）；
5. 计数：`maintenance.runs/skips_busy/skipped_not_idle/duration_ms`。

### 2.5 读池收益与风险（R5）

- 收益：WAL 下读不阻塞写；13 个读 API 不再与写事务排队。预期读 P95 在持续写负载下降幅显著（文档目标 ≥50%），写侧不加锁不变。
- 风险 1（WAL 增长/检查点饥饿）：长读查询拖住读标记 → checkpoint 无法推进 → WAL 增长。缓解：读操作沿用 10s `operationContext`（建议读侧单独配 `ReadOperationTimeout=3s`，见 Doc 3 Q2）；对大参数按上限钳制（`ListEvents` 等 `limit` 建议 clamp ≤1000 并计数）；新增 `wal_size_bytes`/`checkpoint_blocked` 观测。
- 风险 2（内存）：读池 `MaxOpenConns=4` + `cache_size=-2048` 若 4 条空闲连接常驻 = 额外 ~8MiB/进程（写池另有 2MiB）。建议：`MaxIdleConns=2`、读池 `cache_size=-1024`，并在配置说明中给出每进程预算。
- 风险 3（关闭等待）：`readDB.Close()` 会等待在途读查询；最坏 = 读超时（10s）。建议读侧超时默认 3s，Close 前先置 closed 拒绝新读。

### 2.6 DSN/URI 相关的隐藏成本（跨分册）

- 现状 `cfg.Path` 直接作为 DSN（`resolveLazyRuntimeDSN` `:4605-4615`），**没有 URI query**，因此 `_pragma`/`_txlock` 都无法声明；
- P1.7 读池必须引入 URI 构造（`file:` + query）；若同时选择「DSN 层 `_txlock=immediate`」，写池也要 URI 化；
- 建议：先落一个共享的 `buildSQLiteFileDSN(path, params)` 工具 + Windows 路径用例测试（盘符、空格、非 ASCII），**写事务 IMMEDIATE 用 `TxOptions` 而非 DSN**，把 URI 化限定在新增读池/快照池范围内，减少对现有写路径的扰动。

### 2.7 快照性能

- 现状 F1（阻塞会话存储单连接）分析正确；补充：快照读事务还会**阻塞会话存储 `CloseStorage` 的 TRUNCATE checkpoint**（`:323`），跨进程快照也会拖住 runtime store 的 Close；两者都有 PASSIVE 降级兜底，但降级本身需计数（现无）。
- `VACUUM INTO` 全库快照持有读标记直到写完，建议同样走专用连接池 + 超时。
- kill -9 时快照临时目录 `.aicli-session-snapshot-*` 与半成品 destination 不会被清理（defer 不执行）；建议启动时清扫超过 N 小时的残留目录，或在文档中明示。

---

## 3. 完整性核对表

图例：✅ 已覆盖；🟡 部分覆盖（本次审查已回写补齐）；❌ 原缺失（本次审查已回写补齐）。

| 维度 | P1.5/P2.11 | P1.6 | P1.7 | P2.13 |
| --- | --- | --- | --- | --- |
| 锁层次与 `s.mu` 真实覆盖面 | 🟡 新增 §3.6 | 🟡 修正维护锁设计 | ✅ §3.5 | ✅ §2.1-2.3 |
| 写事务模式（IMMEDIATE/升级风险） | ❌→🟡 新增 §3.6（引用 R1） | ❌→🟡 新增 §3.1 注记 | 🟡 §3.5 引用 | ✅ 主库只读无升级 |
| 多库 ATTACH 锁序不变量 | 🟡 引用 | — | — | — |
| 批量写锁持有时长预算 | ❌→🟡 §3.6 | — | — | — |
| 检查点/WAL 增长交互 | 🟡 §3.6 | ✅ §3.2 | ❌→🟡 §3.7 | ❌→🟡 §2.8 |
| 崩溃/断电持久化语义 | ❌→🟡 §4 修正 | — | — | ✅ §2.3-2.5 |
| 内存预算 | — | — | ❌→🟡 §3.1 注记 | ✅ §2.4 |
| 锁/争用指标（BUSY/517/锁时长） | ❌→🟡 §5 | ❌→🟡 §3.3 | ❌→🟡 §3.6 | 🟡 §2.7 |
| 错误可见性（不静默丢） | 🟡 §3.2 | — | — | — |
| 回滚开关 | ✅ §3.5/§4 | ✅ §4 | ✅ §4 | ✅ §3 |
| 测试矩阵（含跨连接/锁用例） | 🟡 §7（新增 517/锁时长） | 🟡 §5（新增真空抢占） | 🟡 §5（新增 WAL/内存） | 🟡 §4（新增 checkpoint/残留） |

---

## 4. 审查发现清单

| 编号 | 级别 | 发现 | 证据 | 影响 | 处置 |
| --- | --- | --- | --- | --- | --- |
| R1 | **阻断（现有代码）** | 写事务全为 deferred 且读后写；跨写者连接触发 `SQLITE_BUSY_SNAPSHOT(517)`；runtime-server 桥接忽略错误 | `BeginTx(ctx,nil)` ×7（§1.2）；rescode 517；`handler.go:4078` | 多进程写场景下事件/邮箱写入失败并可能静默丢失 | P0.5：`LevelSerializable`（BEGIN IMMEDIATE）+ 分类重试 + 计数 + 桥接可见性；本文 §1.5 |
| R2 | 高（方案） | P1.6 维护任务持 `s.mu` 会阻塞事件/邮箱写，且锁不住全部写 | `s.mu` 覆盖面 §1.2；Doc 2 §3.2 | 外移 vacuum 反而伤害热路径 | 已回写 Doc 2 §3.2（不持锁 + 空闲门槛 + 小步 + 退避） |
| R3 | 中（方案） | 多库 ATTACH 事务锁序未固化为不变量 | chat `:2657→:2667`、team `:2349→:2359` | 未来新增写点可能反序 → 跨进程双文件锁互等直至 busy_timeout | 已回写 Doc 1 §3.6 不变量 |
| R4 | 中（方案） | 批量写锁持有时长无预算与度量 | Doc 1 §3.5 | 两进程写时 P99 恶化不可控 | 已回写 Doc 1 §3.6/§5 |
| R5 | 中（方案） | 读池 WAL 增长/checkpoint 饥饿与内存预算缺失 | Doc 3 §3.1-3.5 | WAL 膨胀、内存用量低估、Close 等待超预期 | 已回写 Doc 3 §3.7 |
| R6 | 中（方案） | 快照读事务对 checkpoint/Close 的阻塞与 kill -9 残留未写入方案 | Doc 4 §2.1；`sqlite_storage.go:270-283, 321-324` | 关闭降级 PASSIVE、临时目录残留 | 已回写 Doc 4 §2.8 |
| R7 | 中（准确性） | 「每事件 fsync」错误 | `:4240-4246`（WAL+NORMAL） | 收益与崩溃窗口表述失真 | 已回写 Doc 1 §0.5/§4 |
| R8 | 中 | 桥接错误可见性不对称（runtime-server 忽略） | `handler.go:4078` vs `chat_actor_host.go:892-900` | 静默丢事件 | 已回写 Doc 1 §3.2/§5，建议并入 P0.5 |
| R9 | 低 | 锁相关指标缺失（BUSY/517/锁持有/WAL 大小/检查点降级） | 各分册指标表 | 无法定位锁争用与回归 | 已回写各分册可观测性 |
| R10 | 低 | 语句缓存决策未记录；跨进程测试仅 in-process 双实例 | Doc 1 §3.1 等 | 决策无据可查；Windows 跨进程锁行为未覆盖 | 已回写 Doc 2 §3.1 注记；测试建议见各分册 DoD |

---

## 5. 已核查但未发现问题（避免过度设计）

- **seq 语义**：`MAX(seq)+1` 在写事务内取号，P1.6 的 `INSERT ... SELECT ... RETURNING` 与其同源；拒绝进程内 seq 缓存的理由（多进程重号）成立。
- **邮箱重复恢复**：`recoverSQLiteMailboxDuplicate` 使用已持有连接（P0 已修）且恢复窗口 ≤2s；P1.6 仅外移 vacuum、保留 DELETE 在事务内，不影响恢复可见性——该取舍正确。
- **内存 DSN**：`mode=memory&cache=shared` 不拆分读池的结论正确（WAL 不可用、共享缓存生命周期绑定连接）。
- **`query_only`**：仅加在读池，快照池不加（需要写 attached destination）——两者区分正确。
- **驱动能力**：`_pragma`（多值）与 `_txlock`/`LevelSerializable→IMMEDIATE` 均已验证存在（v0.32.0）。
- **批量上限**：store 128 / buffer 64 的二次上限设计合理；只需补锁持有时长预算（R4）。

## 6. 回写清单（本次审查已应用到分册）

| 分册 | 回写内容 |
| --- | --- |
| P1.5/P2.11 | §0.5 事实修正；新增 §3.6「锁与事务模式」（IMMEDIATE、517 重试、多库锁序、批量锁时长预算、崩溃竞态语义）；§5 指标新增 BUSY/517/锁时长；§6 风险表修正；§7 测试新增 517/锁时长用例；§4 持久化语义修正 |
| P1.6 | §3.1 事务模式注记；§3.2 维护任务锁设计修正（不持 `s.mu`、空闲门槛、小步 vacuum、退避）；§1.3/§3.3 指标新增锁时长与 busy；§5 测试新增抢占/517 用例 |
| P1.7 | 新增 §3.7「WAL 增长/检查点饥饿/内存预算/读超时」；§3.1 内存预算注记；§5 测试新增 WAL 与内存用例 |
| P2.13 | 新增 §2.8「锁与检查点交互、跨进程与残留清理」；§4 测试新增 checkpoint/残留用例 |
| 索引 | 增加本审查报告链接；标注 R1 为 P0.5 前置 |

---

## 7. 实施记录：P0.5（2026-09-18）

状态：**已实施并通过回归**（对应索引 §1 第 0 项）。

### 7.1 改动清单

| 文件 | 内容 |
| --- | --- |
| `internal/sqliteutil/tx.go`（新增） | `WriteTxOptions`（`LevelSerializable` → 驱动 `BEGIN IMMEDIATE`）；`IsBusyError`/`IsBusySnapshotError`（`errors.Is` 语义，兼容驱动返回的 `ErrorCode`/`ExtendedErrorCode`/`*Error` 三种形态）；`RetryWriteTx`（≤3 次新事务、50/100ms 退避、受 ctx 约束） |
| `internal/sqliteutil/tx_test.go`（新增） | 5 用例：517 危害复现（deferred 读→写 + 另一连接提交）、IMMEDIATE 修复语义、锁超时分类、BUSY 重试成功、重试耗尽 |
| `internal/chat/session_runtime_store.go` | 7 处写事务全部 IMMEDIATE（AcquireLease/RenewLease/AppendEvent/AppendMailbox/agent-control×2/修复）；`AppendEvent` 拆为「预检 → `RetryWriteTx` → `appendEventTx`」，仅 BUSY 家族重试 |
| `internal/chat/runtime_store_contention.go`（新增） | `SQLiteContentionStats{BusyRetries, BusySnapshotErrors, BusyExhausted}` 计数与 `ContentionStats()` 访问器 |
| `internal/chat/sqlite_storage.go` | 2 处写事务 IMMEDIATE（`beginWriteTx`、`ClearMessages`） |
| `internal/team/sqlite_store.go` | 2 处写事务 IMMEDIATE（`WithImmediateTx`、ATTACH 同事务写 global mailbox）；修正误导注释（原注释称 DSN 已带 `_txlock=immediate`，实际没有） |
| `internal/api/skills/handler.go` | 2 处 `_, _ = store.AppendEvent` → 失败计数 + 限频(5s)告警 |
| `internal/api/skills/runtime_event_delivery.go` | 新增 `persist_errors` 指标（`runtime_event_delivery` 快照键）与 `recordRuntimeEventPersistError` |
| `internal/chat/runtime_store_busy_snapshot_test.go`、`internal/api/skills/runtime_event_persist_error_test.go`（新增） | 双实例并发 append：seq 连续、无 517、无 BUSY 逃逸；桥接失败计数 |

### 7.2 验证证据

- `go build ./...`（backend 全仓）通过。
- `go test ./internal/sqliteutil/... -count=1` 通过（含 517 复现与修复证据）。
- `go test ./internal/chat/ -count=1` 通过（19.2s，含新增并发回归）。
- `go test ./internal/team/ -count=1` 通过（9.1s）。
- `go test ./internal/api/skills/ -run "TestRecordRuntimeEventPersistErrorCounts|RuntimeEventDelivery" -count=1` 通过。
- `go vet ./internal/sqliteutil/... ./internal/chat/ ./internal/team/ ./internal/api/skills/` 通过。
- 说明：skills 全量包存在来自**并发会话未跟踪文件**（`codex_list_catalog*.go`、`codex_list_test.go`）的测试隔离型失败，隔离运行通过，与本次改动无关。

### 7.3 有意偏离与理由

- 重试包装只加在 `AppendEvent`（最高频、结构最清晰）；其余写事务只做 IMMEDIATE。理由：IMMEDIATE 已在根上消除 517；普通 BUSY 由 5s `busy_timeout` 在驱动内等待，重试主要用于兜底「持锁者超时耗尽/异常」。邮箱与租约路径各自已有恢复/退避语义，外层再加同构重试会重复。
- `appendAgentControlMailboxSameTx` 只改事务模式不加外层重试：其内部已有 `recoverSQLiteMailboxDuplicate` 幂等恢复，外层重试会改变恢复语义。

### 7.4 P0.5b 实施记录（2026-09-18，已完成）— 逐 store 收敛写事务模式

§7.4 原遗留清单的处置：**先按"该连接池 DSN 是否已注入 `_txlock=immediate`"分类**，只对真正 deferred 的池动手。

| 站点 | DSN `_txlock` | 处置 |
| --- | --- | --- |
| `internal/agentcontrol/global_agent_store.go:464`（COUNT 名额 → upsert，跨进程 agent 注册表） | 无 | **改为 `sqliteutil.WriteTxOptions`**（读后写，517 高危） |
| `internal/artifact/checkpoint_files.go:224`（SELECT 待回收 → DELETE） | 无 | **改为 `WriteTxOptions`**（读后写） |
| `internal/artifact/checkpoint_files.go:73`（纯 INSERT 循环） | 无 | 改 `WriteTxOptions`（一致性；同文件两处统一） |
| `internal/background/store.go:630`（`MAX(seq)+1` → INSERT，aicli/runtime-server 共享库） | 无 | **改为 `WriteTxOptions`**（读后写，实测共享库最危险） |
| `internal/background/store.go:586`（DELETE 循环） | 无 | 改 `WriteTxOptions`（一致性） |
| `internal/migrate/migrate.go:84`（DDL/INSERT 迁移） | 调用方决定 | 改 `WriteTxOptions`（UpSQL 可含读后写；迁移失败会中断启动） |
| `internal/subagentbatch/sqlite_store.go` ×5 | **有**（`batchDSNOptions`） | 不动（已等价 IMMEDIATE），登记门禁白名单 |
| `internal/supervision/sqlite_store.go:480` | **有**（`supervisionAppendDSN`） | 不动，登记白名单 |
| `internal/usageanalytics/store_stats.go:413`（原清单未列出，本次扫描新增） | **有**（`writableDSN`） | 不动，登记白名单 |
| `cmd/session-dedupe/main.go:280` | **有**（自带 DSN 构造） | 不动，登记白名单 |

统一方案裁决：**不采用**在 `sqliteutil.OpenFileCtx` 统一追加 `_txlock=immediate`——该 DSN 同样服务只读连接（会让读事务也取写锁，违背 P1.7 读池设计）；改为**逐 store `WriteTxOptions`**（只影响写事务）。

新增门禁（防回归）：`internal/sqliteutil/tx_gate_test.go::TestWriteTransactionsUseImmediateOptions` 全仓扫描非测试代码的 `BeginTx(` 调用，除白名单外必须显式 `WriteTxOptions`；白名单自校验（对应包目录必须真的存在 `_txlock` 注入，防止清单腐烂）。已用临时探针文件验证门禁**确实会红**后删除探针。

验证：

- `go build ./...` 通过；`go test ./internal/sqliteutil/ ./internal/background/ ./internal/artifact/ ./internal/agentcontrol/ ./internal/migrate/ -count=1` 通过（见 §7.5）。
- 新增 `internal/background/store_append_busy_snapshot_test.go`：两个 store 实例（等价双进程）并发 `AppendEvent`，seq 必须连续、零错误。**诚实注记**：该用例在 deferred 实现下也能通过（517 复现窗口仅微秒级），故它是并发烟雾/连续性回归，不是 517 判别器；517 的判别由门禁 + `tx_test.go` 的确定性用例承担（`TestDeferredReadThenWriteFailsWithBusySnapshot` / `TestImmediateTxReadThenWriteSucceeds`）。
- 明确不做：把 `_txlock=immediate` 写进所有 store 的 DSN（见上"统一方案裁决"）。

### 7.5 P0.5b 回归结果（2026-09-18）

`go test -count=1` 各包结果：`internal/sqliteutil` ✅（含门禁与 517 用例）、`internal/background` ✅、`internal/artifact` ✅、`internal/agentcontrol` ✅、`internal/migrate` ✅；`go build ./...` ✅。
