package chat

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	logpkg "github.com/wwsheng009/ai-agent-runtime/internal/pkg/logger"
)

// P2.13：会话快照（SnapshotSession / Snapshot）加固的 store 侧内核。
//
// 设计基线（docs/plan/sqlite-session-snapshot-hardening-plan-20260918.md §6）：
//   - D1 快照走专用单连接池，打开失败降级主池并计数；
//   - D2 destination 以 O_CREATE|O_EXCL 原子预留，只清理本次自建文件；
//   - D4 SessionSnapshotTimeout / SessionSnapshotMaxBytes 有界预算与预检；
//   - D6/D7 事务不变量：快照事务只读主库、只写 O_EXCL 新建的 destination
//     普通库；禁止在该事务内写主库，也不对主库连接加 query_only
//     （会用同一条 SQLite 连接语义禁止写 attached 库）。
var (
	// ErrSessionSnapshotTooLarge 表示预检估算超过 SessionSnapshotMaxBytes。
	ErrSessionSnapshotTooLarge = errors.New("sqlite session snapshot too large")
	// ErrSessionSnapshotTimeout 表示快照超出 SessionSnapshotTimeout。
	ErrSessionSnapshotTimeout = errors.New("sqlite session snapshot timed out")
	// ErrSessionSnapshotDrift 表示复制后的一致性校验失败（行数/字节/版本）。
	ErrSessionSnapshotDrift = errors.New("sqlite session snapshot consistency check failed")
)

type sessionSnapshotCounters struct {
	total             atomic.Int64
	succeeded         atomic.Int64
	failed            atomic.Int64
	degraded          atomic.Int64
	timeouts          atomic.Int64
	tooLarge          atomic.Int64
	schemaDrift       atomic.Int64
	checkpointBlocked atomic.Int64
	lastDurationMS    atomic.Int64
	lastBytes         atomic.Int64
	lastRowsSessions  atomic.Int64
	lastRowsMessages  atomic.Int64
	lastRowsPrompts   atomic.Int64
}

// SessionSnapshotStats 是快照遥测快照（P2.13/G7）。
type SessionSnapshotStats struct {
	Total              int64 `json:"total"`
	Succeeded          int64 `json:"succeeded"`
	Failed             int64 `json:"failed"`
	DegradedToMainPool int64 `json:"degraded_to_main_pool"`
	Timeouts           int64 `json:"timeouts"`
	TooLarge           int64 `json:"too_large"`
	SchemaDrift        int64 `json:"schema_drift"`
	CheckpointBlocked  int64 `json:"checkpoint_blocked"`
	LastDurationMillis int64 `json:"last_duration_ms"`
	LastBytes          int64 `json:"last_bytes"`
	LastRowsSessions   int64 `json:"last_rows_sessions"`
	LastRowsMessages   int64 `json:"last_rows_messages"`
	LastRowsPrompts    int64 `json:"last_rows_prompt_messages"`
}

// SnapshotStats 返回会话快照的累计计数与最近一次观测值。
func (s *SQLiteSessionStorage) SnapshotStats() SessionSnapshotStats {
	if s == nil {
		return SessionSnapshotStats{}
	}
	c := &s.snapshotStats
	return SessionSnapshotStats{
		Total:              c.total.Load(),
		Succeeded:          c.succeeded.Load(),
		Failed:             c.failed.Load(),
		DegradedToMainPool: c.degraded.Load(),
		Timeouts:           c.timeouts.Load(),
		TooLarge:           c.tooLarge.Load(),
		SchemaDrift:        c.schemaDrift.Load(),
		CheckpointBlocked:  c.checkpointBlocked.Load(),
		LastDurationMillis: c.lastDurationMS.Load(),
		LastBytes:          c.lastBytes.Load(),
		LastRowsSessions:   c.lastRowsSessions.Load(),
		LastRowsMessages:   c.lastRowsMessages.Load(),
		LastRowsPrompts:    c.lastRowsPrompts.Load(),
	}
}

// snapshotPool 返回快照专用连接池（惰性打开、单连接）。同一失败只记录一次，
// 后续调用直接复用失败结果（不反复尝试打开）。
func (s *SQLiteSessionStorage) snapshotPool() (*sql.DB, error) {
	s.snapshotMu.Lock()
	defer s.snapshotMu.Unlock()
	if s.snapshotDB != nil {
		return s.snapshotDB, nil
	}
	if s.snapshotOpenErr != nil {
		return nil, s.snapshotOpenErr
	}
	dsn := strings.TrimSpace(s.snapshotDSNOverride)
	if dsn == "" {
		dsn = s.cfg.Path
	}
	db, err := openSessionSnapshotPool(dsn, s.cfg)
	if err != nil {
		s.snapshotOpenErr = err
		return nil, err
	}
	s.snapshotDB = db
	return db, nil
}

func openSessionSnapshotPool(dsn string, cfg PersistentSessionStorageConfig) (*sql.DB, error) {
	db, err := sql.Open("sqlite3", dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite snapshot pool: %w", err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	// 逐连接基线（连接池恒为 1，与主池同源）：busy_timeout 保证锁等待，
	// temp_store=FILE 避免大快照吃内存，mmap_size=0 避免映射放大。
	// 注意：不设置 query_only —— 快照事务要写 ATTACH 的 destination 库。
	pragmas := []string{
		"PRAGMA busy_timeout=" + strconv.Itoa(int(cfg.BusyTimeout/time.Millisecond)),
		"PRAGMA cache_size=-" + strconv.Itoa(cfg.SQLiteCacheKiB),
		"PRAGMA temp_store=FILE",
		"PRAGMA mmap_size=0",
	}
	for _, pragma := range pragmas {
		if _, err := db.Exec(pragma); err != nil {
			_ = db.Close()
			return nil, fmt.Errorf("apply sqlite snapshot pragma %q: %w", pragma, err)
		}
	}
	return db, nil
}

// snapshotConnection 取快照连接：优先专用池，打开失败降级主池（D1）。
func (s *SQLiteSessionStorage) snapshotConnection(ctx context.Context) (connection *sql.Conn, degraded bool, err error) {
	db, poolErr := s.snapshotPool()
	if poolErr != nil {
		s.snapshotStats.degraded.Add(1)
		if logger := logpkg.S(); logger != nil {
			logger.Warnf("[session-snapshot] dedicated snapshot pool unavailable, falling back to main pool: %v", poolErr)
		}
		db = s.db
		degraded = true
	}
	connection, err = db.Conn(ctx)
	if err != nil {
		return nil, degraded, fmt.Errorf("open sqlite snapshot connection: %w", err)
	}
	return connection, degraded, nil
}

func (s *SQLiteSessionStorage) closeSnapshotPool() {
	s.snapshotMu.Lock()
	db := s.snapshotDB
	s.snapshotDB = nil
	s.snapshotOpenErr = nil
	s.snapshotMu.Unlock()
	if db != nil {
		_ = db.Close()
	}
}

// snapshotDestination 是 destination 的归属权凭据：只有本次以 O_EXCL 成功
// 预留的文件才允许在失败路径删除（D2）。
type snapshotDestination struct {
	path    string
	created bool
}

func (d snapshotDestination) removeIfOwned() {
	if d.created && d.path != "" {
		_ = os.Remove(d.path)
	}
}

// reserveSnapshotDestination 以 O_CREATE|O_EXCL 原子创建空文件，关闭
// stat-then-create 的 TOCTOU 窗口。SQLite 将零长度文件视为空库，ATTACH 可用。
func reserveSnapshotDestination(destinationPath string) (snapshotDestination, error) {
	trimmed := strings.TrimSpace(destinationPath)
	if trimmed == "" {
		return snapshotDestination{}, fmt.Errorf("sqlite snapshot destination cannot be empty")
	}
	resolved, err := filepath.Abs(trimmed)
	if err != nil {
		return snapshotDestination{}, fmt.Errorf("resolve sqlite snapshot path: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(resolved), 0o755); err != nil {
		return snapshotDestination{}, fmt.Errorf("create sqlite snapshot directory: %w", err)
	}
	file, err := os.OpenFile(resolved, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		if os.IsExist(err) {
			return snapshotDestination{}, fmt.Errorf("sqlite snapshot destination already exists")
		}
		return snapshotDestination{}, fmt.Errorf("reserve sqlite snapshot destination: %w", err)
	}
	if err := file.Close(); err != nil {
		_ = os.Remove(resolved)
		return snapshotDestination{}, fmt.Errorf("close sqlite snapshot destination: %w", err)
	}
	return snapshotDestination{path: resolved, created: true}, nil
}

// preflightSnapshotSize 在复制前估算会话体积（消息与投影字节 ×1.3 页开销余量），
// 超过 SessionSnapshotMaxBytes 时给出可解释错误（G4/D4）。
func (s *SQLiteSessionStorage) preflightSnapshotSize(ctx context.Context, connection *sql.Conn, sessionID string) error {
	if s == nil || s.cfg.SessionSnapshotMaxBytes <= 0 {
		return nil
	}
	var messageBytes, promptBytes int64
	if err := connection.QueryRowContext(ctx, `
		SELECT COALESCE((SELECT SUM(byte_count) FROM session_messages WHERE session_id = ?), 0),
		       COALESCE((SELECT SUM(byte_count) FROM session_prompt_messages WHERE session_id = ?), 0)
	`, sessionID, sessionID).Scan(&messageBytes, &promptBytes); err != nil {
		return fmt.Errorf("estimate sqlite session snapshot size: %w", err)
	}
	estimate := (messageBytes + promptBytes) * 13 / 10
	if estimate > s.cfg.SessionSnapshotMaxBytes {
		return fmt.Errorf("%w: estimated_bytes=%d limit_bytes=%d", ErrSessionSnapshotTooLarge, estimate, s.cfg.SessionSnapshotMaxBytes)
	}
	return nil
}

type snapshotConsistency struct {
	Sessions         int64
	SnapshotSessions int64
	Messages         int64
	SnapshotMessages int64
	PromptMessages   int64
	SnapshotPrompts  int64
	MessageBytes     int64
	SnapshotBytes    int64
}

// verifySQLiteSessionSnapshot 在同一事务内比对主库与快照库的行数与字节数
// （G6/F7），并确认快照库落在预期 schema 上。任何不一致都返回
// ErrSessionSnapshotDrift，调用方回滚并清理 destination。
func verifySQLiteSessionSnapshot(ctx context.Context, connection *sql.Conn, sessionID string) (snapshotConsistency, error) {
	var result snapshotConsistency
	if err := connection.QueryRowContext(ctx, `
		SELECT (SELECT COUNT(*) FROM main.sessions WHERE id = ?),
		       (SELECT COUNT(*) FROM snapshot.sessions WHERE id = ?)
	`, sessionID, sessionID).Scan(&result.Sessions, &result.SnapshotSessions); err != nil {
		return result, fmt.Errorf("verify sqlite session snapshot sessions: %w", err)
	}
	if err := connection.QueryRowContext(ctx, `
		SELECT (SELECT COUNT(*) FROM main.session_messages WHERE session_id = ?),
		       (SELECT COUNT(*) FROM snapshot.session_messages WHERE session_id = ?),
		       COALESCE((SELECT SUM(byte_count) FROM main.session_messages WHERE session_id = ?), 0),
		       COALESCE((SELECT SUM(byte_count) FROM snapshot.session_messages WHERE session_id = ?), 0)
	`, sessionID, sessionID, sessionID, sessionID).Scan(
		&result.Messages, &result.SnapshotMessages, &result.MessageBytes, &result.SnapshotBytes,
	); err != nil {
		return result, fmt.Errorf("verify sqlite session snapshot messages: %w", err)
	}
	if err := connection.QueryRowContext(ctx, `
		SELECT (SELECT COUNT(*) FROM main.session_prompt_messages WHERE session_id = ?),
		       (SELECT COUNT(*) FROM snapshot.session_prompt_messages WHERE session_id = ?)
	`, sessionID, sessionID).Scan(&result.PromptMessages, &result.SnapshotPrompts); err != nil {
		return result, fmt.Errorf("verify sqlite session snapshot prompt messages: %w", err)
	}
	if result.Sessions != result.SnapshotSessions ||
		result.Messages != result.SnapshotMessages ||
		result.PromptMessages != result.SnapshotPrompts ||
		result.MessageBytes != result.SnapshotBytes {
		return result, fmt.Errorf(
			"%w: sessions=%d/%d messages=%d/%d prompt_messages=%d/%d message_bytes=%d/%d",
			ErrSessionSnapshotDrift,
			result.Sessions, result.SnapshotSessions,
			result.Messages, result.SnapshotMessages,
			result.PromptMessages, result.SnapshotPrompts,
			result.MessageBytes, result.SnapshotBytes,
		)
	}
	return result, nil
}

// copyMainUserVersionToSnapshot 让快照库携带与主库一致的 schema 版本，
// 供下游工具判断快照结构（G6）。
func copyMainUserVersionToSnapshot(ctx context.Context, connection *sql.Conn) error {
	var version int
	if err := connection.QueryRowContext(ctx, `PRAGMA main.user_version`).Scan(&version); err != nil {
		return fmt.Errorf("read sqlite session user_version: %w", err)
	}
	if _, err := connection.ExecContext(ctx, fmt.Sprintf(`PRAGMA snapshot.user_version = %d`, version)); err != nil {
		return fmt.Errorf("write sqlite snapshot user_version: %w", err)
	}
	return nil
}

// recordSnapshotResult 汇总一次快照的结果：计数、最近耗时/字节与结构化日志。
func (s *SQLiteSessionStorage) recordSnapshotResult(sessionID string, start time.Time, degraded bool, destination string, rows *snapshotConsistency, err error) {
	if s == nil {
		return
	}
	c := &s.snapshotStats
	duration := time.Since(start)
	c.total.Add(1)
	c.lastDurationMS.Store(duration.Milliseconds())
	if info, statErr := os.Stat(destination); statErr == nil && info != nil {
		c.lastBytes.Store(info.Size())
	}
	if rows != nil {
		c.lastRowsSessions.Store(rows.SnapshotSessions)
		c.lastRowsMessages.Store(rows.SnapshotMessages)
		c.lastRowsPrompts.Store(rows.SnapshotPrompts)
	}
	result := "ok"
	if err != nil {
		result = "io_error"
		switch {
		case errors.Is(err, context.DeadlineExceeded), errors.Is(err, ErrSessionSnapshotTimeout):
			result = "timeout"
			c.timeouts.Add(1)
		case errors.Is(err, ErrSessionSnapshotTooLarge):
			result = "too_large"
			c.tooLarge.Add(1)
		case errors.Is(err, ErrSessionSnapshotDrift):
			result = "schema_drift"
			c.schemaDrift.Add(1)
		case errors.Is(err, context.Canceled):
			result = "canceled"
		}
		c.failed.Add(1)
	} else {
		c.succeeded.Add(1)
	}
	stats := s.SnapshotStats()
	if logger := logpkg.S(); logger != nil {
		logger.Infof("[session-snapshot] session_id=%s result=%s duration_ms=%d bytes=%d rows_sessions=%d rows_messages=%d rows_prompt_messages=%d total=%d failed=%d degraded=%d timeout=%d too_large=%d drift=%d checkpoint_blocked=%d err=%v",
			sessionID, result, duration.Milliseconds(), stats.LastBytes,
			stats.LastRowsSessions, stats.LastRowsMessages, stats.LastRowsPrompts,
			stats.Total, stats.Failed, stats.DegradedToMainPool, stats.Timeouts, stats.TooLarge, stats.SchemaDrift, stats.CheckpointBlocked, err)
	}
}

// snapshotCleanupContext 为失败清理（ROLLBACK/DETACH/REMOVE）提供独立短预算，
// 避免清理路径本身再次挂起（P2.13/G2）。
func snapshotCleanupContext() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 2*time.Second)
}
