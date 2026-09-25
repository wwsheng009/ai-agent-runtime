package chat

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"path/filepath"
	"strings"
	"time"
)

// ---------------------------------------------------------------------------
// 会话库读池：WAL 下的读并发。
//
// 历史约束：会话库连接池恒为单连接（SetMaxOpenConns(1)），一次大会话的 Save /
// 历史反序列化会把所有并发读（/web/api/status、屏幕快照、历史分页、会话列表）
// 排到同一把连接锁后面，表现为「页面卡住、各端点相互饿死」。
//
// WAL 本身允许「多读者 + 单写者」，瓶颈只在连接数。这里因此把只读查询分流到
// 独立读池（N 连接），写路径保持原写池（单连接，写事务语义逐字节不变）：
//   - 读池惰性打开（首次读请求时），打开/校验失败 → sticky 降级回写池；
//   - 逐连接 PRAGMA 必须走 DSN `_pragma`（连接池会新建连接，init 时对唯一连接
//     执行的那套 PRAGMA 不会自动继承）；
//   - 读池打开后用一次回读校验确认 PRAGMA 真的生效，失败即降级（宁可退回单连接，
//     也不要带着错误的 busy_timeout/foreign_keys 跑）。
//
// 范式与 runtime store 的读池（runtime_store_read_pool.go）保持一致。
// ---------------------------------------------------------------------------

const (
	// sqliteSessionReadPoolDefaultSize 是读池默认连接数：足以覆盖「列表 + 状态 +
	// 屏幕快照 + 历史分页」的并发读，又不至于让每个连接的 cache_size 叠加失控。
	sqliteSessionReadPoolDefaultSize = 4
	// sqliteSessionReadPoolMaxIdle 是空闲读连接上限（内存预算，R5 审查同款约束）。
	sqliteSessionReadPoolMaxIdle = 2
	// sqliteSessionReadPoolOpenTimeout 是读池打开 + 校验的总预算。
	sqliteSessionReadPoolOpenTimeout = 5 * time.Second
)

// sqliteSessionReadPoolPragmas 是读池每条连接都必须具备的 PRAGMA（与写池
// applyConnectionPRAGMAs 对齐）。参数化值由 cfg 决定。
func (s *SQLiteSessionStorage) sqliteSessionReadPoolPragmas() []string {
	return []string{
		fmt.Sprintf("busy_timeout(%d)", s.cfg.BusyTimeout.Milliseconds()),
		"synchronous(NORMAL)",
		fmt.Sprintf("cache_size(-%d)", s.cfg.SQLiteCacheKiB),
		"temp_store(FILE)",
		// 与写池一致：默认禁用 mmap，避免大库把内存 pin 住。
		"mmap_size(0)",
		"foreign_keys(ON)",
	}
}

// sqliteSessionReadPoolDSN 构造读池的 file: URI（逐连接 `_pragma` 内联）。
// 路径不可用时返回空串（调用方降级回写池）。
func (s *SQLiteSessionStorage) sqliteSessionReadPoolDSN() string {
	path := strings.TrimSpace(s.cfg.Path)
	if path == "" {
		return ""
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return ""
	}
	values := url.Values{}
	for _, pragma := range s.sqliteSessionReadPoolPragmas() {
		values.Add("_pragma", pragma)
	}
	return "file:" + filepath.ToSlash(absolute) + "?" + values.Encode()
}

// ensureReadPool 惰性打开读池。失败一律 sticky 降级（之后所有读回落到写池），
// 绝不让「读池不可用」升级成「读不可用」。
func (s *SQLiteSessionStorage) ensureReadPool(ctx context.Context) {
	if s == nil || s.db == nil {
		return
	}
	s.readPoolMu.Lock()
	defer s.readPoolMu.Unlock()
	if s.readPoolOpen || s.readPoolDegraded {
		return
	}
	size := s.cfg.ReadPoolSize
	if size <= 0 {
		size = sqliteSessionReadPoolDefaultSize
	}
	if size <= 1 {
		// 显式配置 1：保持历史单连接行为（用于排障/极端内存受限场景）。
		s.readPoolDegraded = true
		return
	}
	dsn := strings.TrimSpace(s.readDSN)
	if dsn == "" {
		dsn = s.sqliteSessionReadPoolDSN()
	}
	if dsn == "" {
		s.readPoolDegraded = true
		return
	}
	db, err := sql.Open("sqlite3", dsn)
	if err != nil {
		s.readPoolDegraded = true
		return
	}
	db.SetMaxOpenConns(size)
	db.SetMaxIdleConns(sqliteSessionReadPoolMaxIdle)

	openCtx, cancel := context.WithTimeout(context.Background(), sqliteSessionReadPoolOpenTimeout)
	defer cancel()
	if err := db.PingContext(openCtx); err != nil {
		_ = db.Close()
		s.readPoolDegraded = true
		return
	}
	if err := verifySQLiteSessionReadPoolPragmas(openCtx, db, s.cfg.BusyTimeout.Milliseconds(), s.cfg.SQLiteCacheKiB); err != nil {
		_ = db.Close()
		s.readPoolDegraded = true
		return
	}
	s.readDB = db
	s.readPoolOpen = true
	_ = ctx
}

// readPoolHandle 返回只读查询应使用的连接池：读池可用时用它，否则回落到写池。
// 语义在所有分支上都与历史一致（同一个 scanSQLiteSession / 同一套查询）。
func (s *SQLiteSessionStorage) readPoolHandle() *sql.DB {
	if s == nil {
		return nil
	}
	s.ensureReadPool(context.Background())
	s.readPoolMu.Lock()
	db := s.readDB
	s.readPoolMu.Unlock()
	if db != nil {
		return db
	}
	return s.db
}

// closeReadPool 关闭读池（写池由 CloseStorage 负责）。幂等。
func (s *SQLiteSessionStorage) closeReadPool() {
	if s == nil {
		return
	}
	s.readPoolMu.Lock()
	db := s.readDB
	s.readDB = nil
	s.readPoolOpen = false
	s.readPoolDegraded = true
	s.readPoolMu.Unlock()
	if db != nil {
		_ = db.Close()
	}
}

// verifySQLiteSessionReadPoolPragmas 回读逐连接 PRAGMA，确认 DSN `_pragma` 生效。
// 任何一项不符即视为读池不可信（降级回写池），而不是带着错误配置继续服务。
func verifySQLiteSessionReadPoolPragmas(ctx context.Context, db *sql.DB, busyTimeoutMs int64, cacheKiB int) error {
	if db == nil {
		return fmt.Errorf("read pool is not open")
	}
	var timeout int64
	if err := db.QueryRowContext(ctx, "PRAGMA busy_timeout").Scan(&timeout); err != nil {
		return fmt.Errorf("verify busy_timeout: %w", err)
	}
	if timeout != busyTimeoutMs {
		return fmt.Errorf("read pool busy_timeout = %d, want %d", timeout, busyTimeoutMs)
	}
	var cacheSize int64
	if err := db.QueryRowContext(ctx, "PRAGMA cache_size").Scan(&cacheSize); err != nil {
		return fmt.Errorf("verify cache_size: %w", err)
	}
	if cacheSize != -int64(cacheKiB) {
		return fmt.Errorf("read pool cache_size = %d, want %d", cacheSize, -int64(cacheKiB))
	}
	var foreignKeys int64
	if err := db.QueryRowContext(ctx, "PRAGMA foreign_keys").Scan(&foreignKeys); err != nil {
		return fmt.Errorf("verify foreign_keys: %w", err)
	}
	if foreignKeys != 1 {
		return fmt.Errorf("read pool foreign_keys = %d, want 1", foreignKeys)
	}
	return nil
}
