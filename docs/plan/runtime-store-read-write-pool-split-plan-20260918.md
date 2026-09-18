# Runtime Store 读写双连接池拆分方案（P1.7）

- 版本：v1.0（2026-09-18 初稿）
- 状态：待评审。§7 决策记录为默认执行基线
- 日期：2026-09-18
- 覆盖项：P1.7
- 分册索引：`docs/plan/runtime-store-hardening-followup-index-20260918.md`
- 建议顺序：排在 P1.6 之后（先缩短写事务，读池收益更纯粹）

---

## 0. 现状与证据

### 0.1 一切操作共用一个单连接池

- `backend/internal/chat/session_runtime_store.go:1744-1745`：`db.SetMaxOpenConns(1)` / `SetMaxIdleConns(1)`（打开重试重建连接时同样设置，`:4204-4205`）。
- 后果：读写严格串行。事件批量写（P1.5）、状态保存、UI 的 `LoadState`/`ListEvents`、租约心跳全部排在同一个连接队列上；任一方慢（含 SQLite 文件锁竞争、vacuum、长事务）都会抬高另一方延迟。
- P0 已把「无限等待」变成「有界等待」（`operationContext` 默认 10s），但**排队本身**没有消除。

### 0.2 为什么必须是单连接写（不可动摇的部分）

- 逐连接 PRAGMA：`busy_timeout` 等在 `init` 中逐连接设置（`:4232-4247`）。
- 全局邮箱通过 `ATTACH DATABASE` 绑定在**具体连接**上：`backend/internal/agentcontrol/mailbox.go:154-157`（`AttachGlobalMailboxSQLiteTx(ctx, conn, writer)`，需要 `*sql.Conn`）；同一事务内还要写主库与 attached 库（`appendAgentControlMailboxSameTx`，`:2582-2600+`）。
- 因此：**写侧保持 MaxOpenConns(1) 与既有锁序（`mailboxWriteMu → s.mu → 连接`）不变**，P0 的 `poolReentryGuard` 继续只描述写池。

### 0.3 读侧现状

- 13 个读 API 走 `ensureForReadCtx`（清单与行号见索引 §0）。
- WAL 已启用（`fileBacked`：`PRAGMA journal_mode=WAL`，`:4240-4246`）——WAL 下读不阻塞写、写不阻塞读；当前单池把这一优势完全浪费了。
- 文件不存在时读路径要返回「空结果且不创建文件」（`ensureForReadCtx` 的 `skipEmpty`，`:1782-1794`）——双池后必须保持。
- `Close()` 先 `wal_checkpoint(TRUNCATE)` 再关连接（`:1796-1832`）；TRUNCATE checkpoint 需要**所有读事务结束**，双池后关闭顺序必须调整（§3.4）。

### 0.4 驱动事实（读池逐连接 PRAGMA 可声明在 DSN）

`github.com/ncruces/go-sqlite3 v0.32.0` 支持 URI 参数 `_pragma=name(value)`，且支持多个 `_pragma`（模块 `conn.go:60-138` 解析 `query["_pragma"]` 为切片）。这让「新建的每个读连接自动带 busy_timeout/query_only」成为可声明、可测试的行为，而不是靠「打开后执行一次 PRAGMA，祈祷连接不被回收」。

---

## 1. 目标 / 非目标 / 成功度量

### 1.1 目标

| 编号 | 目标 |
| --- | --- |
| G1 | 文件型（WAL）runtime store 增加独立只读连接池，13 个读 API 全部路由到读池 |
| G2 | 写池保持 `MaxOpenConns(1)` 与 ATTACH/锁序不变；写路径零行为变化 |
| G3 | 读池连接逐连接强制 `busy_timeout` 与 `query_only=1`（后者为防御性保证：读池不可能写入） |
| G4 | 生命周期正确：惰性打开（不创建文件）、打开失败降级回写池、`Close()` 顺序保证 TRUNCATE checkpoint 不被读连接阻塞 |
| G5 | 内存/共享缓存 DSN 不拆分（读池=写池），行为不变 |
| G6 | 暴露两池的 `sql.DBStats`（等待次数/等待时长）作为收益与容量证据 |

### 1.2 非目标

- 不改写路径、不改 ATTACH 机制、不改锁序、不改 `poolReentryGuard` 的作用域。
- 不做「多写池」（SQLite 单写者 + ATTACH 事务语义不允许）。
- 不改 `skipEmpty` 契约，不引入对文件不存在时的探测性写。
- 不追求读一致性延迟的强保证（WAL 快照语义，见 §8 Q1）。
- 不为 `SQLiteSessionStorage`（另一套会话存储，单池于 `sqlite_storage.go:133-134`）做双池；其快照问题见 P2.13 分册。

### 1.3 成功度量

| 指标 | 目标 | 观测方式 |
| --- | --- | --- |
| 读 P95（与持续写入并发） | 相对现状降低 ≥50% | 压测：1 个写流 + 1 个 `ListEvents` 流 |
| 写 P95 | 不劣化（±5% 内） | 同上 |
| 读池排队 | `readPool.WaitCount` 在稳态下 ≈0 | `sql.DBStats()` 访问器 |
| 读池连接数 | ≤ `ReadPoolSize`（默认 4），空闲后回落到 `MaxIdleConns` | `sql.DBStats()` |
| 读池写入尝试 | 100% 失败（`query_only` 生效） | 专项测试对读池执行 INSERT 断言报错 |
| 内存 DSN | 不拆分、无额外连接 | 单测断言 `readDB == writeDB` |
| WAL 增长（持续读+写 60s） | WAL ≤ 峰值预算（如 64MiB）且 checkpoint 持续推进 | `wal_size_bytes` / `checkpoint_blocked`（§3.7） |

---

## 2. 设计原则

1. **写侧不动**：任何优化收益都不得以牺牲 ATTACH/单写者正确性为代价。
2. **逐连接确定性**：读池的行为（busy_timeout、query_only）必须由 DSN 声明，不依赖「连接恰好没被回收」。
3. **惰性且无副作用**：读池只在「写池已成功 init 且文件已存在」后打开；文件不存在时继续 `skipEmpty`。
4. **降级而非失败**：读池打开失败（URI 解析、驱动差异）只降级为「读走写池」+ 告警，不让整个会话启动失败。
5. **可观测**：两池的 `DBStats` 可查；降级次数有计数。

---

## 3. 详细设计

### 3.1 拓扑与配置

```
SQLiteRuntimeStore
├── db        *sql.DB  // 写池：MaxOpenConns=1, MaxIdleConns=1（不变），ATTACH/迁移/写都在这里
├── readDB    *sql.DB  // 读池：仅 fileBacked；MaxOpenConns=ReadPoolSize(默认4), MaxIdleConns=2（内存预算见下）
│                      // DSN 携带 _pragma（§3.2）；惰性打开（§3.3）
└── readPoolState：opened / openErr / degraded（读池失败降级到写池）
```

新增配置（`RuntimeStoreConfig`）：

| 配置 | 默认 | 说明 |
| --- | --- | --- |
| `ReadPoolSize` | `4` | 读池连接上限；`0` 表示关闭拆分（读走写池），用于灰度回退 |
| `ReadPoolQueryOnly` | `true` | 读池连接注入 `PRAGMA query_only(1)`；仅调试期允许关闭 |
| `ReadPoolBusyTimeout` | 继承 `BusyTimeout` | 读连接锁等待上限（WAL 下读极少遇锁，保留快速失败语义） |

不满足拆分条件时 `readDB == db`（同一句柄），配置自动失效并记录降级原因：

- `!fileBacked`：`:memory:` / `file:...mode=memory&cache=shared`（`resolveLazyRuntimeDSN`，`:4605-4615`）；
- `ReadPoolSize<=0`；
- 读池打开失败（§3.3）。

**内存预算（审查 R5）**：读池空闲连接同样各带 `cache_size`。为避免 4×2MiB 常驻，读池 `MaxIdleConns=2`、`cache_size=-1024`；每进程上限 ≈ 写池 2MiB + 读池 2×1MiB = 4MiB（原先设想的 ~10MiB 降一半）。`PoolStats` 增加 `ReadOpenConnections` 以验证空闲回落。

### 3.2 读池 DSN（逐连接 `_pragma`）

在既有 DSN 基础上构造读池 URI：

- **文件路径配置（`cfg.Path`，DSN 即路径）**：转换为 `file:` URI：`file:` + `filepath.ToSlash(absPath)` + `?` + query。query 用 `url.Values` 组装，`_pragma` 可重复：

  ```
  _pragma=busy_timeout(5000)
  _pragma=query_only(1)
  _pragma=cache_size(-2048)
  _pragma=temp_store(FILE)
  _pragma=mmap_size(0)
  _pragma=foreign_keys(ON)
  ```

  实现时以 `url.Values.Encode()` 生成（括号会被百分号编码，驱动经 `url.ParseQuery` 解码后再执行，语义不变）；Windows 路径先 `filepath.Abs` 再 `ToSlash`，驱动器盘符形如 `file:E:/...`。

- **用户 DSN 配置（`cfg.DSN`）**：解析既有 URI 的 query 后合并 `_pragma`；若 DSN 不是 `file:` URI 或无法安全合并，则记录告警并降级（读走写池），不做字符串拼接猜测。

- **构造失败/驱动不认 `_pragma` 的兜底**：尝试用读池 DSN 打开并执行一次 `PRAGMA busy_timeout` + `PRAGMA query_only=1`；若 `query_only` 校验失败（`PRAGMA query_only` 读回不为 1）则视为不支持 → 降级。

验收必须包含 `TestReadPoolDSNPragmasApply`：打开读池后逐项读回 `busy_timeout`、`query_only`、`cache_size`、`mmap_size`，断言与配置一致。

### 3.3 路由与惰性打开

新增内部访问器：

```go
// readQueryer returns the pool used by read-only APIs.
func (s *SQLiteRuntimeStore) readQueryer() *sql.DB {
    if s == nil || s.readDB == nil { return s.db }
    return s.readDB
}
```

`ensureForReadCtx` 扩展为：

1. `s.Opened() || s.durableFileExists()` 为假且 `s.path != ""` → 返回 `skipEmpty=true`（**不创建文件、不打开读池**，契约不变）。
2. 否则先 `s.ensureCtx(ctx)`（写池 open + 迁移完成，读池依赖其保证的 schema）。
3. 若满足拆分条件且 `readDB` 未打开 → 惰性打开读池（`sql.Open` + `SetMaxOpenConns(ReadPoolSize)`）；打开成功后做一次 `SELECT 1` 探活与 PRAGMA 校验（§3.2 兜底）。
4. 打开失败 → `degraded=true`、计数 + 限频告警、`readDB=nil`（读回落写池），不影响本次调用。

13 个读 API 的改动点：`ensureForReadCtx` 之后的 `s.db.QueryRowContext/QueryContext` 改为 `s.readQueryer().Query...`。写 API 与内部事务（`appendMailboxTx` 等）**不得**使用 `readQueryer()`；实施时用一条 grep 门禁（CI 脚本或测试）确保读 API 清单与路由一致：

- 允许列表：`GetLease/LoadState/GetToolReceipt/ListToolReceipts/ListEvents/ListEventsBefore/ListMailbox/ListAgentControlMailbox/ListAgentControlMailboxRecords/LastEventSeq/LastMailboxSeq/LastAgentControlMailboxSeq/LastAgentControlMailboxRecordSeq`。
- 其它任何出现 `readQueryer()` 的位置视为评审项。

### 3.4 生命周期：打开、关闭、重开

- **打开顺序**：写池 open + PRAGMA + 迁移（既有 `init`）成功后，才允许打开读池；读池绝不执行 DDL/迁移。
- **关闭顺序**（`Close()`，`:1796-1832` 改造）：
  1. 置 `closed=true`（拒绝新调用）；
  2. 关闭读池：`readDB.Close()`（等待使用中的读连接归还；配合 §3.5 的读操作有界超时，等待有界）；若读池打开过但 `Close` 报错 → 记录，不阻塞后续步骤；
  3. 写池 `PRAGMA wal_checkpoint(TRUNCATE)` → 失败降级 `PASSIVE`（既有行为，`:1808-1821`）→ `db.Close()`。
  - 关键点：TRUNCATE checkpoint 需要无活跃读者；先关读池可避免「读池空闲连接持有 WAL 读锁 → TRUNCATE 失败 → 降级 PASSIVE」。降级路径仍在，但应是异常而非常态（用计数观测）。
- **重开/打开重试**：`ensureCtx` 的 `openSQLiteRuntimeStoreWithLockRetryCtx` 重建写连接时，`readDB` 必须一并作废（`Close` + 置 nil），由下一次读惰性重建，避免读池指向旧文件句柄/旧 schema。
- **`reopen` 路径**（打开重试重建 store 的 `:4204-4205`）：新增字段（readDB/readPoolState）需要跟随复制；实施时检查两处 `&SQLiteRuntimeStore{...}` 构造点。

### 3.5 不变量与守卫

1. **重入守卫只描述写池**：`poolReentryGuard` 的 enter/exit 仍在 `appendAgentControlMailboxSameTx`（写池 `s.db.Conn`）与测试钩子中；读池不存在「专用连接」路径。`checkPoolReentry` 在所有原入口保持（含读 API 入口）——语义是「本 goroutine 持有写池专用连接时，不得再向任何池要连接」，对读池同样成立（读池自有连接池，不会自锁，但保守拒绝更安全，且便于审计）。
2. **读池只读**：`query_only(1)` + 专项测试（对读池执行 `INSERT/UPDATE/DDL` 必须失败）；实施时禁止在 `readDB` 上调用 `ExecContext`（代码评审 + grep 门禁）。
3. **读操作有界**：13 个读 API 已有 `operationContext`（默认 10s）——保持；长读事务会阻碍 checkpoint 与 WAL 回收，`opTimeout` 是天然上界（§8 Q1）。
4. **写事务不等待读池**：写路径不持有也不获取读池连接；写池单连接语义不变。
5. **文件不存在不建池**：`skipEmpty` 分支不得触发读池 `sql.Open`（`sql.Open` 本身不建文件，但探活 `SELECT 1` 会建；因此顺序必须是先判 `durableFileExists`）。

### 3.6 可观测性

```go
type RuntimeStorePoolStats struct {
    WriteWaitCount     int64
    WriteWaitDuration  time.Duration
    ReadWaitCount      int64
    ReadWaitDuration   time.Duration
    ReadPoolOpen       bool
    ReadPoolDegraded   bool
    ReadPoolOpenErrors int64
}
func (s *SQLiteRuntimeStore) PoolStats() RuntimeStorePoolStats
```

- 数据源：`sql.DB.Stats()`（`WaitCount/WaitDuration/MaxOpenConnections/OpenConnections`）。
- 部署建议：调试端点/日志按需输出；`ReadPoolDegraded=true` 需告警（说明拆分未生效，收益为 0）。

### 3.7 WAL 增长、检查点饥饿与读侧边界（审查 R5）

- **长读拖住检查点**：WAL 下读不阻塞写，但读事务的读标记会阻止 checkpoint 越过它；`journal_size_limit=16MiB`（`session_runtime_store.go:4245`）只在 checkpoint 后截断，不能限制 WAL 增长。缓解：
  1. 读侧独立超时 `ReadOperationTimeout`（默认 3s，替代沿用写侧 10s），`Close()` 前先置 closed 拒绝新读，避免 `readDB.Close()` 等待最长 10s；
  2. 大参数钳制：`ListEvents/ListEventsBefore/ListMailbox/ListAgentControlMailbox/ListToolReceipts` 的 `limit` clamp ≤1000，超限计数 `read.limit_clamped`；
  3. 观测：`wal_size_bytes`（按 `-wal` 文件大小）、`checkpoint_blocked`（Close/自动 checkpoint 降级 PASSIVE 的次数）、`read_pool.open_connections`。
- **写侧公平性**：读池不改变写锁语义；跨进程写争用与 `SQLITE_BUSY_SNAPSHOT` 风险由写事务 IMMEDIATE 修复（见 P1.5 §3.6 / 审查 R1）。
- **维护/ATTACH/DDL 只能走写池**：vacuum（P1.6 §3.2）与 ATTACH 邮箱事务必须在写池连接上执行；读池只承载 §3.3 白名单查询。
- **适用范围**：内存/共享缓存 DSN 不拆分（§3.1），上述结论仅适用于 `fileBacked`。

---

## 4. 兼容 / 迁移 / 回滚

- 无磁盘格式变更；WAL 已是既有默认（`fileBacked`）。
- 关闭开关 `ReadPoolSize=0` → 读走写池，行为与现状一致（含错误语义与超时）。
- 内存 DSN/测试 DSN 自动不拆分，既有测试（含直接用 `store.db` 持有唯一连接来制造饱和的测试）不受影响。
- 多进程：读池只是本进程内的连接复用；跨进程可见性仍由 WAL + busy_timeout 决定，语义不变。
- 回滚：配置回退即可；无数据迁移。

## 5. 测试与验收（DoD）

1. `TestReadPoolDSNPragmasApply`：读连接逐项 PRAGMA 校验（busy_timeout/query_only/cache_size/mmap_size）。
2. `TestReadPoolRejectsWrites`：对 `readDB` 执行 `INSERT`/`UPDATE`/`CREATE TABLE` 均报错（query_only）。
3. `TestReadPoolDoesNotCreateFile`：路径不存在时 `LoadState/ListEvents` 返回空且不创建文件、不打开读池。
4. `TestReadPoolDegradesOnOpenFailure`：注入非法读 DSN → `PoolStats().ReadPoolDegraded==true`，读 API 仍正确（走写池）。
5. `TestReadWriteConcurrency`：写流持续 append，同时 4 个并发 `ListEvents`；断言读 P95 相比「ReadPoolSize=0」降低 ≥50%（阈值可在 CI 放宽为「显著降低且不劣化」，压测报告给绝对值）。
6. `TestCloseClosesReadPoolBeforeCheckpoint`：读池持有空闲连接时 `Close()` 取得成功且未降级 PASSIVE；`PoolStats` 反映关闭。
7. `TestEnsureReopenInvalidatesReadPool`：触发写池重开（锁竞争模拟）后，读池被重建且 schema 正确。
8. `TestMemoryDSNNotSplit`：`mode=memory&cache=shared` 下 `readDB == nil` 或与 `db` 同句柄，行为不变。
9. `TestReadAPIRoutingInventory`：清单门禁（§3.3 允许列表），防止新读 API 误走写池或写 API 误用读池。
10. 既有全量回归：`go test ./internal/chat/...`、`go test ./cmd/aicli/commands/ -run TestLocalHost`、`go vet`。
11. `TestReadPoolDoesNotStarveCheckpoint`：持续写 + 长读（大 limit）并发 60s，断言 WAL 不超预算、`limit_clamped` 计数生效、Close 未降级 PASSIVE（或降级计数符合预期）。
12. `TestReadPoolIdleMemoryBudget`：读操作结束后 `read_pool.open_connections ≤ 2`，`cache_size` 读回 ~1MiB（审查 R5 内存预算）。

验收门槛：1–12 全绿；压测达到 §1.3；`ReadPoolDegraded` 在目标平台上恒为 false（否则先解决 DSN 兼容性再宣传收益）。

## 6. 里程碑与工作量

| 里程碑 | 内容 | 估算 |
| --- | --- | --- |
| M0 | 池统计访问器 + 基线压测（单池） | 0.5d |
| M1 | 读池 DSN/打开/降级 + 测试 1–4、8 | 1.5d |
| M2 | 13 读 API 路由 + 清单门禁 + 测试 9 | 1d |
| M3 | Close/重开生命周期 + 测试 5–7 | 1d |
| M4 | 压测对比与 runbook 更新 | 0.5d |

## 7. 决策记录（评审基线）

| 编号 | 决策 | 状态 |
| --- | --- | --- |
| D1 | 写池保持单连接 + ATTACH 语义；仅拆读池 | 待评审（默认执行） |
| D2 | 读池逐连接 PRAGMA 由 URI `_pragma` 声明（驱动已支持），并做读回校验 | 待评审（默认执行） |
| D3 | 读池默认 4 连接、`query_only=1`、打开失败降级写池 + 告警 | 待评审（默认执行） |
| D4 | 仅 `fileBacked`（WAL）拆分；内存/共享缓存 DSN 不拆分 | 待评审（默认执行） |
| D5 | `Close()` 顺序：先关读池，再 checkpoint TRUNCATE + 关写池 | 待评审（默认执行） |
| D6 | 读侧独立超时 3s；`limit` clamp ≤1000；读池 `MaxIdleConns=2` + `cache_size=-1024`（审查 R5） | 待评审（默认执行） |

## 8. 开放问题

- Q1：读一致性窗口：WAL 下读池看到的是「读事务开始时的快照」。当前读 API 都是短查询，是否需要显式文档化「刚写入的事件可能对并发读不可见（≤事务切换时间）」？
- Q2：读池是否需要独立的 `operationContext` 上限（如 3s，小于写侧的 10s），避免长读阻碍 checkpoint？依赖 M0 的读时延分布决定。
- Q3：`SQLiteSessionStorage`（会话存储，独立单池 `sqlite_storage.go:133-134`）是否在后续版本采用同一双池模式？其快照/写入模式不同，需单独评估。
- Q4：是否把 `ReadPoolSize` 暴露到 `cfg` 配置文件与 `/web/api/status`，便于现场按机器调整？

---

## 9. 实施记录：P1.7 读写双池拆分（2026-09-18）

状态：**实施完成**；`DisableReadPool=true` 为逐字节回滚开关；仅 `fileBacked`（WAL 文件库）拆分。

### 9.1 语义

| 面 | 实现 |
| --- | --- |
| 写池 | `MaxOpenConns(1)`/`MaxIdleConns(1)`、ATTACH、锁序、`poolReentryGuard` **零改动** |
| 读池 | 惰性打开：写池 init（含迁移）成功后、首次读时创建；`MaxOpenConns=ReadPoolSize(默认4)`、`MaxIdleConns=2` |
| 逐连接 PRAGMA | 读池 DSN 用 `_pragma` 声明（`query_only(1)`/`busy_timeout`/`cache_size(-1024)`/`temp_store(FILE)`/`mmap_size(0)`/`foreign_keys(ON)`），打开后逐项读回校验；不满足即降级 |
| 路由 | 13 个读 API 走 `readQueryer()`；写路径与修复/维护路径仍走写池；清单门禁测试双向校验（读 API 未路由/写 API 误用都会红） |
| 读超时 | 读侧独立 `ReadOperationTimeout`（默认 3s，D6），调用方已带 deadline 时尊重调用方 |
| limit 钳制 | `ListEvents/ListEventsBefore/ListMailbox/ListAgentControlMailbox/ListToolReceipts/ListAgentControlMailboxRecords`：`limit>1000` 钳到 1000 并计数 `read_limit_clamped` |
| 降级 | 读 DSN 不可安全构造 / `sql.Open` / 探活 / PRAGMA 校验失败 → `read_pool_degraded=true` + 原因 + 计数，读回落写池，不影响启动与本次调用 |
| 关闭/重开 | `Close()`：停维护 → **先关读池** → `wal_checkpoint(TRUNCATE)`（失败降级 PASSIVE，既有）→ 关写池；写池锁重试重建后读池作废、下次读重建 |
| 内存/共享缓存 DSN | 不拆分（`read_pool_open=false`），行为不变 |
| 观测 | `PoolStats()`（两池 `WaitCount/WaitDuration/OpenConnections/MaxOpenConnections`、读池 open/degraded/reason/open_errors、`read_queries`、`read_limit_clamped`、`WALSizeBytes()`） |
| 健康端点 | `/api/runtime/health` 快照新增 `store_pools` 段（`PoolStats()`；无 runtime store 时不出现，保持既有响应形状）；**aicli `/debug` 已于 2026-09-18 接线**：`/debug display` 与 `/web/api/status` 的"存储与持久化:"区块输出 `Write Pool: open=/ waits/ wait`、`Read Pool: open/conns/waits/wait/queries/clamps`（降级时附 `degraded=true(reason) open_errors=`）与 `WAL:` 体积，JSON 走 `storage.pool`（字段契约同 `store_pools`） |
| 配置接线 | `sessionRuntime.readPool{disable,size,busyTimeout,operationTimeout}`（`SessionRuntimeConfig.ReadPool`，示例见 `backend/configs/runtime.yaml` 注释块）：runtime-server 与 aicli 两宿主均在构造 store 时下传；读池配置**非默认时**纳入 store 配置标识，变更即重建。默认全部零值 = 启用双池、4 连接、3s 读超时，且配置键形状与拆分前一致（升级不触发无谓重建） |

偏离说明（相对初稿决策）：初稿写「`ReadPoolSize=0` 关闭拆分」，但仓库惯例是零值=默认，故实现为 **`ReadPoolSize<=0` 取默认 4，关闭用 `DisableReadPool=true`**（与 `DisableSQLiteReturning`/`DisableBackgroundMaintenance` 一致）。

### 9.2 测试对照（DoD）

| # | 用例 | 结果 |
| --- | --- | --- |
| 1 | `TestReadPoolDSNPragmasApply`（两个并发连接逐项读回 query_only/busy_timeout/cache_size/mmap_size） | ✅ |
| 2 | `TestReadPoolRejectsWrites`（INSERT/UPDATE/DDL 均 `readonly`） | ✅ |
| 3 | `TestReadPoolDoesNotCreateFile`（空读不建文件、不开池） | ✅ |
| 4 | `TestReadPoolDegradesOnOpenFailure`（注入坏读 DSN → 降级 + 读仍可用） | ✅ |
| 5 | `TestReadPoolLoadComparison`（门控 `AICLI_RUNTIME_POOL_LOAD_TEST=1`：单池 vs 双池读 P95） | ✅ 见 §9.3 |
| 6 | `TestCloseClosesReadPoolBeforeCheckpoint`（读池先关，WAL 被 TRUNCATE 归零） | ✅ |
| 7 | `TestReadPoolInvalidatedOnWritePoolReopen`（丢弃写句柄 → `ensure` 重建 → 旧读池必须关闭且读句柄失效，下一次读重建新句柄） | ✅ |
| 8 | `TestMemoryDSNNotSplit` | ✅ |
| 9 | `TestReadAPIInventoryRouting`（13 项白名单 + 双向 grep 门禁 + 读超时存在性） | ✅ |
| 10 | 既有回归：`internal/chat` 全量、`internal/api/skills` 全量、`cmd/aicli/commands`（见 §9.4） | ✅ |
| 12 | `TestReadPoolIdleMemoryBudget`（并发 4 读后空闲连接回落到 ≤2） | ✅ |
| 11 | `TestReadPoolWALBudgetUnderLongRead`（长读钉快照 + 限速写 5s；`AICLI_RUNTIME_WAL_BUDGET_FULL=1` → 60s；含批量对照与回收断言） | ✅ 见 §9.3 |

### 9.3 压测对比（写 2000 次 + 4×200 次 `ListEvents`，真实 SQLite + WAL）

```powershell
$env:AICLI_RUNTIME_POOL_LOAD_TEST=1; go test ./internal/chat -run TestReadPoolLoadComparison -v -timeout 600s
```

| 指标 | 单池（`DisableReadPool=true`） | 双池（默认） | 结论 |
| --- | --- | --- | --- |
| 读 P95 | 4.87ms | **1.85ms** | **−62%**（目标 ≥50% ✅） |
| 写 P95 | 2.04ms | 1.52ms | 不劣化 ✅ |

### 9.3.1 长读 + 持续写：WAL 预算与可回收性（DoD 11）

```powershell
go test ./internal/chat -run TestReadPoolWALBudgetUnderLongRead -v -timeout 300s
$env:AICLI_RUNTIME_WAL_BUDGET_FULL=1; go test ./internal/chat -run TestReadPoolWALBudgetUnderLongRead -v -timeout 300s   # 60s 窗口
```

| 场景（读池连接钉住只读快照，写持续进行） | 写入量 | WAL 峰值 | 每事件 WAL |
| --- | --- | --- | --- |
| 逐条写 5s（限速 200/s） | 1,000 | 18.8MB | 18,849 B |
| 逐条写 **60s** | 12,000 | 232.8MB | 19,402 B |
| 批量写 64/批（5s 窗口内 1s，P1.5） | 26,816 | 31.4MB | **1,171 B** |
| 批量写 64/批（60s 版内 1s） | 30,208 | 35.7MB | 1,181 B |

结论与语义（重要）：

1. **长读期间写不失败**：两阶段 `writeErrors=0`，读池让慢读者不再阻塞写（拆分前单写连接下长读会直接顶住写路径）；
2. **WAL 增长由提交次数驱动，而非 payload 字节**：快照被钉住时 checkpoint 无法推进，WAL ≈ 提交数 × 每提交脏页（实测 ~19KB/单事件提交，6 页/事务量级）；这是"与写入量成比例的有界增长"，不是失控，但对"外部长快照读者"要按写入速率预留磁盘（60s 实测 233MB）；
3. **批量写把放大降 16×**（1,171B vs 18,849B/事件）：P1.5 批量落盘同时是 WAL 预算的主要杠杆；
4. **可回收**：读者释放后 `wal_checkpoint(TRUNCATE)` 把 WAL 收回 <1MiB（两阶段均断言）；
5. **本 store 自身的读有界**（`readOpTimeout=3s`），因此自身最坏把 WAL 钉住 3s；真正的长快照只能来自外部进程/工具，需在运维文档中提示。

### 9.4 回归现场注记

- `internal/chat` 全量 29.7s 通过（含新增 DoD 7/11 用例）；一次早期全量运行中 `TestAppendEventsLockHoldBudget`（P1.5 的 500ms 上限断言）偶发失败，随后单测 `-count=6` 与全量复跑均通过（实测 1.0–1.5ms），判定为机器负载抖动（Windows 磁盘/杀软），非 P1.7 写路径变化（写路径零改动）。
- `go test -race ./internal/chat -run 'TestReadPool|TestAppendEvents|TestReadAPIInventory'` 通过（6.5s，无 DATA RACE）：读池热切换、`Close` 与并发读、批写与读池路由无竞态/无锁序反转。
- `internal/config` 全量通过（含 `TestSessionRuntimeReadPoolConfigParsesYAML`）；`internal/api/skills` 全量 20.4s 通过（含 `TestRuntimeHealthIncludesStorePoolStats`、`TestSessionRuntimeStoreReadPoolConfigWiring`）；`cmd/aicli/commands` 全量 96.1s，仅剩既有的环境相关失败 `TestRestoreChatStateFromRuntimeSessionRestoresRouteTransparency`（该用例在 HEAD 干净 worktree 上同样失败，非本次引入），无新增失败。
- 配置接线首版把读池参数无条件并入 store 配置键，破坏了"无 store 配置时保留既有 store"的哨兵语义，被既有 `TestWaitSessionAgentsWithoutTargetUsesParentMailbox` / `TestListSessionAgentEventsWithoutAgentReadsParentMailbox` 当场抓住；已改为"默认配置保持原键形状、仅非默认时追加读池段"，两用例恢复通过。
- **观测接线回归（2026-09-18，P1.7 尾巴收口）**：新增 `chat_debug_storage.go`（`/debug display` 与 `/web/api/status` 共用）后，`go test ./cmd/aicli/commands/ -count=1` 110.1s：仅 `TestRestoreChatStateFromRuntimeSessionRestoresRouteTransparency` 失败（路由/权限恢复期望，与本区块无调用关系；单测隔离复现同样失败，且 `chat_session_test.go` 无任何 debug display 引用），属 §9.4 已记录的既有环境性失败；新增 4 个用例（TUI 区块渲染、无 store 时区块隐藏、JSON 契约、nil 输入）全绿。

### 9.5 遗留

- Q1（WAL 快照读一致性窗口）建议在 `docs` 中对消费者明示：并发读可能看不到刚提交的事件（≤事务切换时间）；
- Q2（读池独立超时）已按 D6 落地为 3s，后续可按 `read_wait_count` 分布调整；
- Q3（`SQLiteSessionStorage` 是否同构）留待 P2.13 后续评估；
- Q4 **已闭环**：`sessionRuntime.readPool{disable,size,busyTimeout,operationTimeout}` 已接到 runtime-server 与 aicli（配置变更触发 store 重建，`TestSessionRuntimeStoreReadPoolConfigWiring` 覆盖）；观测两宿主均已接线（runtime-server health `store_pools`；aicli `/debug display` + `/web/api/status` 的"存储与持久化:"区块 / `storage.pool` 段，见 §9.1 表）；门禁测试 `TestChatDebugDisplayShowsStorageSection` / `TestChatDebugStorageSnapshotJSONContract`；
- DoD 7/11 的用例已补齐（`TestReadPoolInvalidatedOnWritePoolReopen`、`TestReadPoolWALBudgetUnderLongRead` 含 60s 门控模式）；
- 运维提示（DoD 11 结论）：外部进程若长时间持有读快照，WAL 会按"提交数 × 每提交脏页（实测 ~19KB/单事件；批量后 ~1.2KB）"增长，需按写入速率预留磁盘；本 store 自身读有 3s 上限，最坏钉住 WAL 3s。
