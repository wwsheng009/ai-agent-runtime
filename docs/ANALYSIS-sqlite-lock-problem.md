# SQLite 锁问题深入分析

## 0. 问题背景

`/api/runtime/health` 端点挂起（>35s 无响应），`/healthz` 和 events 端点正常。而我们刚刚把 `defaultPersistence: file` 改为 `memory` 后 health 恢复正常（38ms）。这篇分析探讨**为什么 file 模式会导致 health 死锁**，以及背后的 sqlite 多进程锁机制。

---

## 1. 架构：多进程共享 SQLite 文件

### 1.1 进程拓扑

```
┌─────────────────────────────────────────────────────┐
│  aicli CLI (PID 31640, 启动 2026-08-30 19:52)      │
│  ┌─────────────────────────────────────────────────┐ │
│  │ 会话与服务: 长驻，一直写入 session_runtime.sqlite │ │
│  └─────────────────────────────────────────────────┘ │
│  ┌─────────────────────────────────────────────────┐ │
│  │ 子进程 aicli-x（5 个, PID 30380/30432/33980/   │ │
│  │ 37212/37640）                                   │ │
│  └─────────────────────────────────────────────────┘ │
└─────────────────────────────────────────────────────┘

┌─────────────────────────────────────────────────────┐
│  runtime-server (PID 34020, 启动 2026-08-31 09:10) │
│  ┌─────────────────────────────────────────────────┐ │
│  │ 提供 Web UI / 健康检查 / 轨迹查询等 API 服务     │ │
│  │ defaultPersistence: memory (修复后)               │ │
│  │ storePath: backend/data/runtime/session_runtime  │ │
│  └─────────────────────────────────────────────────┘ │
└─────────────────────────────────────────────────────┘
```

**关键事实**：aicli CLI 是一个**长驻进程**（运行超过 13 小时），持续写入共享 sqlite 文件。runtime-server 是在此**之后**启动的，也尝试打开同一个文件。

### 1.2 共享的 SQLite 文件（`C:\Users\vince\.aicli\sessions\runtime\`）

| 文件 | 大小 | WAL 文件 | 说明 |
|------|------|----------|------|
| `session_runtime.sqlite` | **62 MB** | 32 B | 会话运行时事件/状态 |
| `agent_control.sqlite` | **122 MB** | 5.5 MB | 代理控制注册表 |
| `artifacts.sqlite` | **1 GB** | 4.8 MB | 制品存储 |
| `background.sqlite` | **12 MB** | 4 MB | 后台任务 |
| `team_store.sqlite` | 319 KB | 0 B | 团队存储 |
| `subagent_batches.sqlite` | 335 KB | - | 子代理批次 |

**所有文件都是 WAL 模式**（有 `.sqlite-wal` 和 `.sqlite-shm` 侧车文件）。

---

## 2. SQLite 锁机制回顾

### 2.1 锁级别（SQLite 标准五级锁）

SQLite 在非 WAL 模式下有逐步升级的锁模型：

```
UNLOCKED → SHARED → RESERVED → PENDING → EXCLUSIVE
```

- **SHARED**：读锁。多个连接可以同时持有 SHARED 锁。
- **RESERVED**：写锁预备。一个连接可以持有 RESERVED 锁，其他连接仍可读（SHARED）。
- **PENDING**：写锁等待。阻止新 SHARED 锁获取，等现有 SHARED 释放后升级为 EXCLUSIVE。
- **EXCLUSIVE**：排他写锁。阻止所有其他连接的任何操作。

### 2.2 WAL 模式下的锁行为

WAL（Write-Ahead Logging）模式改变了锁行为：

```
┌─────────────────────────────────────────────────────────┐
│  WAL 模式隔离级别：                                                  │
│                                                                     │
│  读操作：从不阻塞写操作。读者读取 WAL 文件的最后一个完整的 checkpoint  │
│  之后的快照。                                                         │
│                                                                     │
│  写操作：不阻塞读操作。写者追加到 WAL 文件，不覆盖原始数据库文件。      │
│                                                                     │
│  一个写者：任意时刻只能有一个写者。WAL 文件上的写锁是 EXCLUSIVE 级别。    │
│                                                                     │
│  Checkpoint：将 WAL 内容合并回主数据库文件。需要 EXCLUSIVE 锁。        │
│  wal_checkpoint(TRUNCATE) 在合并后截断 WAL 文件，需要独占锁。         │
└─────────────────────────────────────────────────────────┘
```

**关键**：WAL 模式下读写不互斥，但**写-写互斥**，且 **checkpoint 需要独占锁**。

### 2.3 busy_timeout 机制

当操作无法获取所需锁时，SQLite 默认立即返回 `SQLITE_BUSY`。通过 `PRAGMA busy_timeout=N` 设置忙等待超时（毫秒）。在超时内，SQLite 在 busy handler 中循环等待并重试，超时后才返回 `SQLITE_BUSY`。

**关键问题**：busy handler 等待期间，Go 的 `context.Context` 取消信号**无法中断**底层 sqlite 的忙等待——这是 C 级（或 wasm 级）循环，不受 Go 调度器控制。

---

## 3. 驱动：ncruces/go-sqlite3

### 3.1 版本信息

| 构建 | 版本 | 位置 |
|------|------|------|
| 主构建 | **v0.32.0** | `go.mod` |
| Win7 兼容构建 | **v0.8.3** | `go.win7.mod` |

当前运行的 `configfix14` 二进制使用 **v0.8.3**（Win7 构建）。这是纯 Go sqlite3 实现（通过 wasm 或纯 Go 翻译的 SQLite）。

### 3.2 默认 busy_timeout

代码注释（`session_runtime_store.go:3913-3917`）指出：

> "this driver (ncruces/go-sqlite3) uses a **60s default lock wait** when busy_timeout is unset, so any pragma touching the database lock (auto_vacuum, journal_mode=WAL) would block for a minute per open attempt"

即：**如果未设置 busy_timeout，ncruces 驱动默认 60s 忙等待**。代码中 `PRAGMA busy_timeout=5000` 是**第一条 PRAGMA**，用来将超时限制在 5s。但**在执行第一条 PRAGMA busy_timeout 之前，驱动的默认 60s 超时生效**。

不过，`PRAGMA busy_timeout` 本身是一个设置操作，通常**不需要数据库锁**（它只设置连接属性），所以第一条 PRAGMA 不会阻塞。但紧接着的 `PRAGMA journal_mode=WAL` 和 `PRAGMA auto_vacuum=INCREMENTAL` 需要写锁，会在 busy_timeout（此时已设为 5s）内等待。

---

## 4. 重试循环：两层嵌套

### 4.1 第一层：`sqliteutil.OpenFile` + `RetryLocked`

所有 file-backed store（team、background、artifact、agent_control、subagent_batch、supervision）都通过统一入口 `sqliteutil.OpenFile(dsn, true)` 打开：

```go
// sqliteutil.go:92-118
func OpenFile(dsn string, failOnLock bool) (*sql.DB, error) {
    db, err := sql.Open("sqlite3", dsn)
    db.SetMaxOpenConns(1)
    db.SetMaxIdleConns(1)

    apply := func() error {
        // 第一条 PRAGMA：设置 busy_timeout = 5000ms
        db.ExecContext(context.Background(), "PRAGMA busy_timeout=5000")
        // 第二条 PRAGMA：设置 WAL 模式——需要写锁！
        db.ExecContext(context.Background(), "PRAGMA journal_mode=WAL")
        return nil
    }
    // 如果 failOnLock=true，用 RetryLocked 重试
    RetryLocked(apply)
}
```

**`RetryLocked`（sqliteutil.go:47-63）**：

```go
func RetryLocked(fn func() error) error {
    for attempt := 0; ; attempt++ {
        if attempt > 0 {
            wait := lockRetryWait(attempt-1)  // 50ms..500ms
            time.Sleep(wait)
        }
        lastErr = fn()
        if lastErr == nil { return nil }
        if !IsLockedError(lastErr) || attempt >= LockRetries { return lastErr }
        // 重试：创建新连接
    }
}
```

- `LockRetries = 10`
- `lockRetryBaseWait = 50ms`, `lockRetryMaxWait = 500ms`
- 退避总时间 ≈ 50+100+150+...+500 = **2.75s**

### 4.2 第二层：`openSQLiteRuntimeStoreWithLockRetry`（session_runtime_store.go）

session_runtime_store 使用自己的重试逻辑（与 `sqliteutil` 类似但独立）：

```go
func openSQLiteRuntimeStoreWithLockRetry(store *SQLiteRuntimeStore) (*SQLiteRuntimeStore, error) {
    for attempt := 0; ; attempt++ {
        if attempt > 0 {
            time.Sleep(sqliteLockRetryWait(attempt-1))  // 50ms..500ms
        }
        // 注意：使用 context.Background() —— 忽略调用方 ctx！
        if err := store.init(context.Background()); err == nil {
            return store, nil
        } else if !isSQLiteLockedError(err) || attempt >= sqliteLockRetries {
            return store, err
        }
        // 重试：创建新连接
        db, _ := sql.Open("sqlite3", store.dsn)
        store = &SQLiteRuntimeStore{db: db, ...}
    }
}
```

- `sqliteLockRetries = 10`
- 同样使用 `context.Background()` —— **关键**：不理会调用方的 ctx 超时

### 4.3 `init()` 的 PRAGMA 链（session_runtime_store.go:3918-3933）

```go
pragmas := []string{
    "PRAGMA busy_timeout=5000",          // 第一条：设超时（不持锁，快）
    "PRAGMA synchronous=NORMAL",         // 需要写锁
    "PRAGMA cache_size=-2048",
    "PRAGMA temp_store=FILE",
    "PRAGMA mmap_size=0",
    "PRAGMA foreign_keys=ON",
}
if s.fileBacked {
    pragmas = append(pragmas,
        "PRAGMA auto_vacuum=INCREMENTAL", // 需要写锁
        "PRAGMA journal_mode=WAL",        // 需要写锁 → 可能阻塞 busy_timeout
        "PRAGMA wal_autocheckpoint=256",
        "PRAGMA journal_size_limit=16777216",
    )
}
```

**阻塞点**：`PRAGMA journal_mode=WAL` 需要 RESERVED→EXCLUSIVE 锁。如果另一个进程（aicli CLI）持有写锁，此 PRAGMA 会等待 busy_timeout=5000ms 然后返回 `SQLITE_BUSY`。

### 4.4 总阻塞时间

```
单次 init() 最大阻塞 = busy_timeout (5s) + PRAGMA 执行时间
每次重试 = 单次 init + 退避等待 (50ms-500ms)
总重试次数 = 10
最坏情况总阻塞 = 10 × (5s + 0.5s) ≈ 55s
```

**这就是为什么 health 端点挂起 >35s**（curl -m 35 超时，实际可能 55s 后返回）。

---

## 5. 级联 Bug：`defaultPersistence: file` 的副作用

### 5.1 ApplyDefaults 做了什么

```go
// main.go:887-891
sessionruntime.ApplyDefaults(runtimeConfig, sessionruntime.ResolveOptions{
    Config:     runtimeConfig,
    ConfigFile: runtimeManager.GetFilePath(),
    Mode:       sessionruntime.ModeServer,
})
```

当 `cfg.SessionRuntime.DefaultPersistence = PersistenceFile` 时，`ResolvePaths` 将 `fileDefaults` 设为 true，随后：

```go
// ResolvePaths (paths.go:108-112)
if path := ResolvePath(configFile, cfg.Team.StorePath); path != "" {
    paths.TeamStorePath = path
} else if fileDefaults {
    paths.TeamStorePath = filepath.Join(runtimeDir, "team_store.sqlite")  // 共享路径！
}
// 同样针对 AgentControl、Artifact、Background...
```

**`ApplyDefaults` 随后将 `paths` 写回 `config`**：

```go
// ApplyDefaults (paths.go:164-165)
if config.Team.StorePath == "" && config.Team.StoreDSN == "" {
    config.Team.StorePath = paths.TeamStorePath  // 填充共享路径
}
```

### 5.2 级联效果

```
defaultPersistence: file
    ↓
fileDefaults = true
    ↓
paths.TeamStorePath     = C:\...\sessions\runtime\team_store.sqlite
paths.AgentControlStorePath = C:\...\sessions\runtime\agent_control.sqlite
paths.ArtifactStorePath = C:\...\sessions\runtime\artifacts.sqlite
paths.BackgroundStorePath = C:\...\sessions\runtime\background.sqlite
    ↓
handler.SetRuntimeConfig → refreshTeamStore → OpenFile(shared, true)
handler.SetRuntimeConfig → refreshAgentControlStore → OpenFile(shared, true)
handler.SetRuntimeConfig → refreshBackgroundStore → OpenFile(shared, true)
```

### 5.3 测试确证

`paths_test.go:41-89` 测试 `TestApplyDefaultsServerFilePersistenceFillsSharedPaths` 明确验证：

```go
cfg.SessionRuntime.DefaultPersistence = PersistenceFile
// 应用后：
assertPath(t, cfg.Team.StorePath, filepath.Join(runtimeDir, "team_store.sqlite"))
assertPath(t, cfg.Background.StorePath, filepath.Join(runtimeDir, "background.sqlite"))
assertPath(t, cfg.Artifact.StorePath, filepath.Join(runtimeDir, "artifacts.sqlite"))
```

**结论**：`defaultPersistence: file` 不仅影响 session runtime store，还级联到所有其他 store。

---

## 6. `executionDiagnosticsSnapshot`：死锁放大器

### 6.1 configfix14 新增

`execution_diagnostics.go` 是 configfix14 中新加入的模块。`/api/runtime/health` 端点调用 `runtimeStatusSnapshot` → `executionDiagnosticsSnapshot`：

```go
// execution_diagnostics.go:61-117
func (h *Handler) executionDiagnosticsSnapshot(ctx context.Context) map[string]interface{} {
    var wait sync.WaitGroup
    wait.Add(4)
    go func() { defer wait.Done(); sessions = h.sessionExecutionDiagnostics(ctx) }()
    go func() { defer wait.Done(); background = h.backgroundExecutionDiagnostics(ctx) }()
    go func() { defer wait.Done(); teams = h.teamExecutionDiagnostics(ctx) }()
    go func() { defer wait.Done(); agents = h.agentExecutionDiagnostics(ctx) }()
    wait.Wait()  // ← 等待全部 4 个 goroutine
    // ...
}
```

### 6.2 每个 goroutine 的 2s ctx 被忽略

每个 goroutine 使用 `executionDiagnosticsContext(parent)` 创建 2s 超时 ctx：

```go
func executionDiagnosticsContext(parent context.Context) (context.Context, context.CancelFunc) {
    return context.WithTimeout(parent, 2*time.Second)
}
```

但底层 sqlite 打开操作**忽略 ctx**：

```go
// openSQLiteRuntimeStoreWithLockRetry: 
store.init(context.Background())  // 不是传入的 ctx！

// RetryLocked:
db.ExecContext(context.Background(), "PRAGMA busy_timeout=5000")  // 不是传入的 ctx！
```

### 6.3 死锁链路

```
health 请求 → runtimeStatusSnapshot → executionDiagnosticsSnapshot
    ↓
wait.Add(4); 启动 4 个 goroutine
    ├── sessionExecutionDiagnostics
    │   → stateStore.LoadState(ctx, ...)  // ensureForRead → 文件不存在 → skip → 快
    │
    ├── teamExecutionDiagnostics
    │   → h.teamStore.ListTeams(ctx, ...)
    │   → ensure() → OpenFile("team_store.sqlite", true)
    │   → RetryLocked(apply) → PRAGMA journal_mode=WAL
    │   → aicli CLI 持有写锁 → 阻塞 5s → SQLITE_BUSY → 重试... ×10 = ~55s
    │
    ├── agentExecutionDiagnostics
    │   → h.agentControlAgentStore.ListAgents(ctx, ...)
    │   → ensure() → OpenFile("agent_control.sqlite", true)  // 122MB, 活跃写入
    │   → 阻塞 5s × 10 ≈ 55s
    │
    └── backgroundExecutionDiagnostics
        → h.backgroundManager.ListJobs(ctx, ...)
        → ensure() → OpenFile("background.sqlite", true)  // 12MB, WAL 4MB
        → 阻塞 5s × 10 ≈ 55s

wait.Wait() → 等待全部 4 个 → 最慢的 goroutine 约 55s → health 挂起 35s+
```

### 6.4 为什么 events 端点正常

events 端点使用 `ListEvents` → `ensureForRead()`（session_runtime_store.go:1674-1685）：

```go
func (s *SQLiteRuntimeStore) ensureForRead() (skipEmpty bool, err error) {
    if s.Opened() || s.durableFileExists() {
        return false, s.ensure()  // 文件已存在 → 真正打开
    }
    if strings.TrimSpace(s.path) == "" {
        return false, s.ensure()
    }
    return true, nil  // 文件不存在 → 跳过
}
```

对于专用路径 `backend/data/runtime/session_runtime.sqlite`——文件不存在（首次创建），`ensureForRead` 返回 `true`（skipEmpty），**不打开 SQLite，不触锁**。events 端点返回空结果快。

---

## 7. 修复原理

### 7.1 修复内容

```yaml
# 修复前
defaultPersistence: file
storePath: ../data/runtime/session_runtime.sqlite

# 修复后
defaultPersistence: memory   # 停止级联
storePath: ../data/runtime/session_runtime.sqlite  # 保留专用路径
```

### 7.2 为什么修复有效

1. **`defaultPersistence: memory`** → `ResolvePaths` 中 `fileDefaults = false` → `paths.TeamStorePath`、`paths.AgentControlStorePath`、`paths.ArtifactStorePath`、`paths.BackgroundStorePath` 保持空字符串 → `ApplyDefaults` 不填充这些路径 → `config.Team.StorePath` 等保持空 → handler 启动时**不创建 file-backed store** → `h.teamStore`、`h.agentControlAgentStore`、`h.backgroundManager` 等保持 nil/内存 → `executionDiagnosticsSnapshot` 的 goroutine 检查 `store == nil` → 立即返回 `"not_configured"` → 不触锁 → 快。

2. **`storePath: ../data/runtime/session_runtime.sqlite`** → `refreshSessionRuntimeStore` 读 `config.SessionRuntime.StorePath`（非空）→ `NewSQLiteRuntimeStore`（lazy，不立即打开）→ `sessionRuntimeStore` 是 SQLiteRuntimeStore（lazy）。`sessionExecutionDiagnostics` 调用 `LoadState` → `ensureForRead` → 文件不存在 → skipEmpty → 返回 nil，快。文件位置在 `backend/data/runtime/session_runtime.sqlite`（**专用路径**，不与 aicli CLI 共享）→ 即使将来打开，也不会锁冲突。

3. **`runtimeStatusSnapshot` 其他组件**：`CheckHealthWithMode`（recheck=none → 跳过）、`ProviderHealthSnapshot`（内存快照）、`bus.TraceStats`（内存）、`sessionPersistenceSnapshot`（`ResolvePaths` 纯计算）→ 全部快。

---

## 8. SQLite 锁问题总结

### 8.1 锁问题来源

| 来源 | 描述 |
|------|------|
| 多进程共享 | aicli CLI（长驻 13h+) + runtime-server 打开同一 sqlite 文件 |
| WAL 模式下写锁 | `PRAGMA journal_mode=WAL` 需要 EXCLUSIVE 锁 |
| 大文件 | session_runtime 62MB, agent_control 122MB, artifacts 1GB |
| 重试 × busy_timeout | 10 次 × 5s = 50s 最大阻塞 |
| ctx 忽略 | `context.Background()` 在重试循环中使调用方 ctx 超时无效 |
| 4 路 goroutine 扇出 | `executionDiagnosticsSnapshot` 的 `wait.Wait()` 等待全部 |

### 8.2 风险评估

| 风险 | 概率 | 严重性 | 说明 |
|------|------|--------|------|
| 共享路径锁冲突 | 高 | 高 | aicli CLI 持续持有写锁 |
| 专用路径锁冲突 | 低 | 中 | 仅多 runtime-server 实例时 |
| Close() 时 checkpoint 阻塞 | 中 | 低 | `wal_checkpoint(TRUNCATE)` 需独占锁 |
| 单连接模型阻塞 | 中 | 中 | 长写操作会阻塞后续所有读操作 |

### 8.3 建议

1. **避免共享 sqlite 文件**：每个服务进程使用自己的专用路径（已修复）。
2. **重试循环应支持 ctx 超时**：`openSQLiteRuntimeStoreWithLockRetry` 和 `RetryLocked` 应接受 `context.Context` 并在超时时提前退出，而不是无限等待 `context.Background()`。
3. **降低 busy_timeout**：对于只需要快速失败的场景（如健康检查中的诊断查询），可使用较短的 busy_timeout（如 500ms）或使用 `PRAGMA busy_timeout=0`（立即返回 SQLITE_BUSY）。
4. **WAL checkpoint 不阻塞**：`Close()` 时的 `wal_checkpoint(TRUNCATE)` 如果无法获取锁，应该优雅降级为 `PASSIVE` 模式而非阻塞。
5. **考虑使用 `_txlock=immediate`**：session_runtime_store 目前用 `sql.Open("sqlite3", s.dsn)` 直接打开，不设 `_txlock=immediate`，可能遇到 DEFERRED→RESERVED 升级时的死锁。team store 已在 DSN 中配置 `_txlock=immediate`。
---

## 9. 已实施修复（代码级，2026-08-31）

针对 8.3 建议，本轮实施以下代码级优化（对应任务：#2 ctx 穿透、#3 sqliteutil ctx 变体、#4 Close 降级、#1 快照硬超时）。

### 9.1 executionDiagnosticsSnapshot 硬超时（health 永不挂起）

**文件**：`backend/internal/api/skills/execution_diagnostics.go`

- 新增 `executionDiagnosticsSnapshotTimeout = 5 * time.Second`（整体快照硬上限）。
- 将原来的 `wait.Wait()`（无界等待全部 4 个 source goroutine）替换为**有界 channel 等待**：
  - 每个 source goroutine 完成时向 `doneCh`（容量 4）发送信号；
  - 主路径循环最多等 4 个信号，任一时刻超过 5s 即 `timedOut=true` 并立即返回；
  - 结果写入 `mu` 互斥保护的结构，避免超时返回后与仍在跑的 goroutine 产生数据竞争。
- 超时返回时对未完成的 source **填充 unavailable/timeout 占位**（非 nil map），保证 JSON 结构稳定：
  - `sessions.source = executionDiagnosticsSource(..., unavailable, "timeout")`，`sessionCounts`/`approvalCounts` 用 `newExecutionDiagnosticsCounts` 初始化；
  - background/teams/agents 同理。

**效果**：即使某个 store 因 sqlite 锁阻塞远超其 2s 预算，`/api/runtime/health` 也会在 5s 内有界返回，不再挂起。

### 9.2 session_runtime_store 的 ensure/open 重试支持 ctx 超时

**文件**：`backend/internal/chat/session_runtime_store.go`

- `openSQLiteRuntimeStoreWithLockRetry` → 重命名为 `openSQLiteRuntimeStoreWithLockRetryCtx(ctx, store)`：
  - 循环开头检查 `ctx.Err()`，已取消则关闭 db 立即返回；
  - `time.Sleep` 改为 `select { case <-time.After(wait): case <-ctx.Done(): }`；
  - `store.init(context.Background())` → `store.init(ctx)`（PRAGMA 链本就支持 ctx）。
- 保留旧签名包装器 `openSQLiteRuntimeStoreWithLockRetry(store)`（传 `context.Background()`），兼容无 ctx 调用点。
- 新增 `ensureCtx(ctx)` 与 `ensureForReadCtx(ctx)`：
  - 内部透传 ctx 到 `openSQLiteRuntimeStoreWithLockRetryCtx`；
  - 原有 `ensure()` / `ensureForRead()` 变为包装器（传 `context.Background()`），28 个调用点无需改动。
- 关键读路径接入 ctx 变体：
  - `LoadState(ctx, ...)` → `ensureForReadCtx(ctx)`（health 诊断直接调用）；
  - `ListEvents(ctx, ...)` → `ensureForReadCtx(ctx)`（Trajectory 面板数据源）。

**效果**：health 诊断中的 `LoadState` 查询受调用方 ctx（2s 预算）约束；即使库被并发进程长锁，重试循环也会在 ctx 取消时退出，而不是无视 ctx 跑满 10 次 × busy_timeout。

### 9.3 sqliteutil 增加 ctx 变体

**文件**：`backend/internal/sqliteutil/sqliteutil.go`

- `RetryLocked(fn)` → 拆分出 `RetryLockedCtx(ctx, fn)`：
  - 循环开头检查 `ctx.Err()`；
  - `time.Sleep` 改为 `select { case <-time.After(wait): case <-ctx.Done(): }`；
  - 原 `RetryLocked` 变为包装器（`context.Background()`），其余 5 个 store（team/background/artifact/subagentbatch/supervision）调用点零改动。
- `OpenFile(dsn, failOnLock)` → 拆分出 `OpenFileCtx(ctx, dsn, failOnLock)`：
  - PRAGMA 执行改用 `db.ExecContext(ctx, ...)`；
  - `RetryLocked` → `RetryLockedCtx`；
  - 原 `OpenFile` 变为包装器。

**效果**：为后续将 team/background/artifact 等 store 的打开路径接入 ctx 提供了基础；本轮保持这些 store 使用无 ctx 包装器（它们的 openOnce 只执行一次，不会反复阻塞）。

### 9.4 Close() 的 wal_checkpoint 降级 PASSIVE

**文件**：`backend/internal/chat/session_runtime_store.go`（`Close()`）

- 原逻辑：`PRAGMA wal_checkpoint(TRUNCATE)` 拿不到锁失败时，直接返回 `checkpoint runtime sqlite WAL: ...` 错误，导致 `Close()` 报错。
- 新逻辑：TRUNCATE 失败时**降级尝试 `PRAGMA wal_checkpoint(PASSIVE)`**（尽力冲刷 WAL，不需要独占锁），PASSIVE 也失败才返回错误。

**效果**：并发进程持有写锁时关闭 runtime store 不再因 checkpoint 失败报错，且关闭流程不会被强制阻塞。

### 9.5 验证结果

- `go build ./...`：**通过**（BUILD OK）。
- `go vet ./internal/sqliteutil/...`：**通过**。
- `go test ./internal/chat/... -short`：**ok**（15.2s）。
- `go test ./internal/api/skills/...`：**全部通过**，含：
  - `TestExecutionDiagnosticsSnapshotReadsReopenedSQLiteStores`；
  - `TestSessionExecutionDiagnosticsDoesNotHoldRuntimeLockDuringQuery`；
  - `TestAgentExecutionDiagnosticsDoesNotHoldRegistryLockDuringQuery`；
  - `TestGetRuntimeHealth_ReturnsSummary` / `TestGetRuntimeHealth_IncludesProfileMetadata`。

### 9.6 剩余风险

| 风险 | 说明 | 建议 |
|------|------|------|
| team/background/artifact 等 store 打开仍用无 ctx 包装器 | 它们的 `openOnce.Do` 只执行一次，首次打开若遇长锁仍可能阻塞到 RetryLocked 耗尽（≈2.75s+5s busy） | 若需要，可将这些 store 的 `ensure()` 改为 ctx 变体，接入 `sqliteutil.OpenFileCtx` |
| `LoadState` 之外的写路径仍用无 ctx `ensure()` | 写路径（AppendEvent/AcquireLease 等）不参与 health 快照，风险低 | 可选：逐步切换为 `ensureCtx` |
| 快照超时后仍有 goroutine 在后台继续跑 | 有界返回保证 health 不挂，但泄漏的 goroutine 会继续占用连接直到自身结束 | 观察日志 `[sqlite-lock]`，确认锁竞争频率 |
