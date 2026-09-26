package chat

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	logpkg "github.com/wwsheng009/ai-agent-runtime/internal/pkg/logger"
)

// P1.7：runtime store 读写双连接池拆分。
//
// 写池（s.db）保持 MaxOpenConns(1)：ATTACH 全局邮箱绑定具体连接、写事务
// 锁序（mailboxWriteMu → s.mu → 连接）与 poolReentryGuard 都依赖"单写连接"。
// 读池（s.readDB）只承载 §3.3 白名单的只读查询，且仅对 fileBacked（WAL）生效：
// 逐连接 PRAGMA 由 DSN `_pragma` 声明（busy_timeout / query_only / cache_size），
// 打开失败或 DSN 不可安全合并时降级回写池并计数，绝不让启动失败。

const (
	// defaultRuntimeReadPoolSize 读池连接上限（DisableReadPool=false 且未显式配置时）。
	defaultRuntimeReadPoolSize = 4
	// runtimeReadPoolMaxIdleConns 读池空闲连接上限（审查 R5：内存预算）。
	runtimeReadPoolMaxIdleConns = 2
	// runtimeReadPoolCacheKiB 读池每连接 page cache（审查 R5：与写池 2MiB 解耦）。
	runtimeReadPoolCacheKiB = 1024
	// runtimeReadPoolOpenTimeout 读池探活/PRAGMA 校验上限。
	runtimeReadPoolOpenTimeout = 5 * time.Second
	// defaultRuntimeReadOperationTimeout 单个读操作上限（D6，小于写侧 10s）。
	defaultRuntimeReadOperationTimeout = 3 * time.Second
	// maxRuntimeReadLimit 读 API 单次返回条数上限（§3.7 大参数钳制）。
	maxRuntimeReadLimit = 1000
)

// RuntimeStorePoolStats 暴露两池排队/连接统计与读池生命周期（P1.7 §3.6）。
type RuntimeStorePoolStats struct {
	WriteWaitCount          int64         `json:"write_wait_count"`
	WriteWaitDuration       time.Duration `json:"write_wait_duration"`
	WriteOpenConnections    int           `json:"write_open_connections"`
	WriteMaxOpenConnections int           `json:"write_max_open_connections"`

	ReadWaitCount          int64         `json:"read_wait_count"`
	ReadWaitDuration       time.Duration `json:"read_wait_duration"`
	ReadOpenConnections    int           `json:"read_open_connections"`
	ReadMaxOpenConnections int           `json:"read_max_open_connections"`
	ReadQueries            int64         `json:"read_queries"`

	ReadPoolOpen           bool   `json:"read_pool_open"`
	ReadPoolDegraded       bool   `json:"read_pool_degraded"`
	ReadPoolDegradedReason string `json:"read_pool_degraded_reason,omitempty"`
	ReadPoolOpenErrors     int64  `json:"read_pool_open_errors"`

	ReadLimitClamps int64 `json:"read_limit_clamped"`
	WALSizeBytes    int64 `json:"wal_size_bytes"`
}

// PoolStats 返回两池统计快照；读池未打开/已降级时读侧字段为零值。
func (s *SQLiteRuntimeStore) PoolStats() RuntimeStorePoolStats {
	if s == nil {
		return RuntimeStorePoolStats{}
	}
	var stats RuntimeStorePoolStats
	s.mu.Lock()
	writeDB := s.db
	s.mu.Unlock()
	if writeDB != nil {
		dbStats := writeDB.Stats()
		stats.WriteWaitCount = dbStats.WaitCount
		stats.WriteWaitDuration = dbStats.WaitDuration
		stats.WriteOpenConnections = dbStats.OpenConnections
		stats.WriteMaxOpenConnections = dbStats.MaxOpenConnections
	}
	s.readPoolMu.Lock()
	readDB := s.readDB
	stats.ReadPoolOpen = s.readPoolOpen
	stats.ReadPoolDegraded = s.readPoolDegraded
	stats.ReadPoolDegradedReason = s.readPoolDegradedReason
	stats.ReadPoolOpenErrors = s.readPoolOpenErrors
	s.readPoolMu.Unlock()
	if readDB != nil {
		dbStats := readDB.Stats()
		stats.ReadWaitCount = dbStats.WaitCount
		stats.ReadWaitDuration = dbStats.WaitDuration
		stats.ReadOpenConnections = dbStats.OpenConnections
		stats.ReadMaxOpenConnections = dbStats.MaxOpenConnections
	}
	stats.ReadQueries = s.readQueries.Load()
	stats.ReadLimitClamps = s.readLimitClamps.Load()
	stats.WALSizeBytes = s.WALSizeBytes()
	return stats
}

// WALSizeBytes 返回 -wal 文件大小（不存在/内存库时为 0），用于 checkpoint 观测。
func (s *SQLiteRuntimeStore) WALSizeBytes() int64 {
	if s == nil || !s.fileBacked || strings.TrimSpace(s.path) == "" {
		return 0
	}
	info, err := os.Stat(s.path + "-wal")
	if err != nil {
		return 0
	}
	return info.Size()
}

// readQueryer 返回只读 API 使用的连接池：读池可用时用读池，否则回落写池。
// 只允许 §3.3 白名单的读 API 调用（清单门禁见 runtime_store_read_pool_test.go）。
func (s *SQLiteRuntimeStore) readQueryer() *sql.DB {
	if s == nil {
		return nil
	}
	s.readQueries.Add(1)
	s.readPoolMu.Lock()
	readDB := s.readDB
	s.readPoolMu.Unlock()
	if readDB != nil {
		return readDB
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.db
}

// readOperationContext 读侧独立超时（D6）：默认 3s，调用方已带 deadline 时尊重之。
func (s *SQLiteRuntimeStore) readOperationContext(ctx context.Context) (context.Context, context.CancelFunc) {
	if ctx == nil {
		ctx = context.Background()
	}
	if s == nil || s.readOpTimeout <= 0 {
		return ctx, func() {}
	}
	if _, ok := ctx.Deadline(); ok {
		return ctx, func() {}
	}
	return context.WithTimeout(ctx, s.readOpTimeout)
}

// clampReadLimit 钳制读 API 的 limit（≤1000）并计数；limit<=0 保持"不限量"语义。
func (s *SQLiteRuntimeStore) clampReadLimit(limit int) int {
	if limit <= maxRuntimeReadLimit {
		return limit
	}
	if s != nil {
		s.readLimitClamps.Add(1)
	}
	return maxRuntimeReadLimit
}

// buildRuntimeReadPoolDSN 构造读池 DSN：逐连接 `_pragma` 声明 busy_timeout /
// query_only / cache_size 等，避免"打开后 PRAGMA 一次，连接被回收后失效"。
// 返回 splittable=false 表示无法安全构造（此时读走写池，不报错）。
func buildRuntimeReadPoolDSN(cfg *RuntimeStoreConfig, path string, busyTimeout time.Duration, queryOnly bool) (string, bool, error) {
	pragmas := make([]string, 0, 6)
	if queryOnly {
		pragmas = append(pragmas, "query_only(1)")
	}
	pragmas = append(pragmas,
		fmt.Sprintf("busy_timeout(%d)", busyTimeout.Milliseconds()),
		fmt.Sprintf("cache_size(-%d)", runtimeReadPoolCacheKiB),
		"temp_store(FILE)",
		"mmap_size(0)",
		"foreign_keys(ON)",
	)
	values := url.Values{}
	for _, pragma := range pragmas {
		values.Add("_pragma", pragma)
	}

	if strings.TrimSpace(path) != "" {
		abs, err := filepath.Abs(path)
		if err != nil {
			return "", false, fmt.Errorf("resolve runtime read pool path: %w", err)
		}
		// `?`/`#` 会破坏 URI 解析；含这类字符时降级而非猜测转义。
		if strings.ContainsAny(abs, "?#") {
			return "", false, fmt.Errorf("runtime store path contains URI metacharacters: %s", abs)
		}
		return "file:" + filepath.ToSlash(abs) + "?" + values.Encode(), true, nil
	}
	dsn := strings.TrimSpace(cfg.DSN)
	if dsn == "" {
		return "", false, nil
	}
	parsed, err := url.Parse(dsn)
	if err != nil || parsed.Scheme != "file" {
		return "", false, fmt.Errorf("runtime store dsn is not a file: URI")
	}
	query := parsed.Query()
	for _, pragma := range pragmas {
		query.Add("_pragma", pragma)
	}
	parsed.RawQuery = query.Encode()
	return parsed.String(), true, nil
}

// ensureReadPool 惰性打开读池：仅在写池已 init 成功（schema 就绪）后调用。
// 打开/校验失败时降级（sticky）并计数，不影响调用方本次读（回落写池）。
func (s *SQLiteRuntimeStore) ensureReadPool(ctx context.Context) {
	if s == nil || !s.fileBacked || s.disableReadPool || s.readPoolSize <= 0 || strings.TrimSpace(s.readDSN) == "" {
		return
	}
	if ctx != nil && ctx.Err() != nil {
		return
	}
	s.readPoolMu.Lock()
	defer s.readPoolMu.Unlock()
	if s.readDB != nil || s.readPoolDegraded {
		return
	}
	db, err := sql.Open("sqlite3", s.readDSN)
	if err != nil {
		s.degradeReadPoolLocked(fmt.Errorf("open: %w", err))
		return
	}
	db.SetMaxOpenConns(s.readPoolSize)
	db.SetMaxIdleConns(runtimeReadPoolMaxIdleConns)
	openCtx, cancel := context.WithTimeout(context.Background(), runtimeReadPoolOpenTimeout)
	defer cancel()
	if err := db.PingContext(openCtx); err != nil {
		_ = db.Close()
		s.degradeReadPoolLocked(fmt.Errorf("ping: %w", err))
		return
	}
	if err := verifyRuntimeReadPoolPragmas(openCtx, db, s.readBusyTimeout.Milliseconds(), s.readQueryOnly); err != nil {
		_ = db.Close()
		s.degradeReadPoolLocked(err)
		return
	}
	s.readDB = db
	s.readPoolOpen = true
}

// verifyRuntimeReadPoolPragmas 读回逐连接 PRAGMA，确认 DSN `_pragma` 真正生效。
func verifyRuntimeReadPoolPragmas(ctx context.Context, db *sql.DB, busyTimeoutMs int64, queryOnly bool) error {
	if db == nil {
		return fmt.Errorf("read pool is not open")
	}
	if queryOnly {
		var mode int
		if err := db.QueryRowContext(ctx, "PRAGMA query_only").Scan(&mode); err != nil {
			return fmt.Errorf("verify query_only: %w", err)
		}
		if mode != 1 {
			return fmt.Errorf("read pool query_only = %d, want 1", mode)
		}
	}
	var timeout int64
	if err := db.QueryRowContext(ctx, "PRAGMA busy_timeout").Scan(&timeout); err != nil {
		return fmt.Errorf("verify busy_timeout: %w", err)
	}
	if timeout != busyTimeoutMs {
		return fmt.Errorf("read pool busy_timeout = %d, want %d", timeout, busyTimeoutMs)
	}
	var cacheKiB int64
	if err := db.QueryRowContext(ctx, "PRAGMA cache_size").Scan(&cacheKiB); err != nil {
		return fmt.Errorf("verify cache_size: %w", err)
	}
	if cacheKiB != -runtimeReadPoolCacheKiB {
		return fmt.Errorf("read pool cache_size = %d, want %d", cacheKiB, -runtimeReadPoolCacheKiB)
	}
	var mmap int64
	// 新版驱动（ncruces/go-sqlite3 v0.35+，纯 Go VFS）在 VFS 不支持 mmap 时
	// `PRAGMA mmap_size` 会返回零行；对我们而言“没有行”与“值为 0”等价，都是
	// “未启用 mmap”，不能因此把读池降级掉。
	if err := db.QueryRowContext(ctx, "PRAGMA mmap_size").Scan(&mmap); err != nil {
		if !errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("verify mmap_size: %w", err)
		}
		mmap = 0
	}
	if mmap != 0 {
		return fmt.Errorf("read pool mmap_size = %d, want 0", mmap)
	}
	return nil
}

// degradeReadPoolLocked 记录降级原因（sticky，避免每次读都重试打开）。
// 调用方必须持有 readPoolMu。
func (s *SQLiteRuntimeStore) degradeReadPoolLocked(err error) {
	s.readPoolDegraded = true
	s.readPoolOpenErrors++
	reason := "read pool unavailable"
	if err != nil {
		reason = err.Error()
	}
	s.readPoolDegradedReason = reason
	logpkg.Warnf("[runtime-store] read pool degraded to write pool: %s", reason)
}

// closeReadPool 关闭读池并复位状态；必须在写池 checkpoint(TRUNCATE) 之前调用，
// 否则空闲读连接持有的 WAL 读标记会让 TRUNCATE 降级为 PASSIVE。
func (s *SQLiteRuntimeStore) closeReadPool() {
	if s == nil {
		return
	}
	s.readPoolMu.Lock()
	db := s.readDB
	s.readDB = nil
	s.readPoolOpen = false
	s.readPoolMu.Unlock()
	if db != nil {
		_ = db.Close()
	}
}
