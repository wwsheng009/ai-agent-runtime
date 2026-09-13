package usageanalytics

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	_ "github.com/wwsheng009/ai-agent-runtime/internal/sqlitedriver"

	"github.com/wwsheng009/ai-agent-runtime/internal/aiclipaths"
)

// DefaultDBFileName 分析库文件名；与 session_runtime.sqlite 同目录。
const DefaultDBFileName = "usage_analytics.sqlite"

// EnvDBPath 覆盖默认分析库路径（测试/自定义部署）。
// 未设置时使用契约默认值，生产行为不变。
const EnvDBPath = "AICLI_USAGE_ANALYTICS_DB"

// defaultBusyTimeout SQLite 写锁等待（事件驱动写入为短事务）。
const defaultBusyTimeout = 5 * time.Second

// 打开期瞬时锁重试（与 internal/chat.SQLiteRuntimeStore 同策略）。
//
// 并发场景是常态而非例外：一台开发机上会同时跑多个 aicli 进程与
// runtime-server，它们共享同一个分析库并各自在启动时 migrate。migrate 需要
// 写锁，落败的连接会拿到 "database is locked"。此前 Open 直接把错误抛给
// 调用方（调用方静默吞掉），于是落败进程在整个生命周期内一条用量行都不写：
// 分析库看着"几乎为空"，而镜像缓存表却有完整明细。
const (
	openLockRetries       = 10
	openLockRetryBaseWait = 50 * time.Millisecond
	openLockRetryMaxWait  = 500 * time.Millisecond
)

// writeLockRetryWait 单条写入遇到瞬时锁冲突时的一次性重试间隔。
const writeLockRetryWait = 25 * time.Millisecond

// DefaultDBPath 返回契约规定的默认分析库路径：
// ~/.aicli/sessions/runtime/usage_analytics.sqlite。
// 环境变量 AICLI_USAGE_ANALYTICS_DB 可覆盖（测试隔离用，默认值不变）。
func DefaultDBPath() string {
	if override := strings.TrimSpace(os.Getenv(EnvDBPath)); override != "" {
		return override
	}
	return filepath.Join(aiclipaths.DefaultSessionsDir(), "runtime", DefaultDBFileName)
}

// PathFromRuntimeStore 由 runtime store 的落盘路径推导同目录分析库路径。
// 空路径返回空字符串（调用方决定回退策略）。
func PathFromRuntimeStore(runtimeStorePath string) string {
	path := strings.TrimSpace(runtimeStorePath)
	if path == "" {
		return ""
	}
	return filepath.Join(filepath.Dir(path), DefaultDBFileName)
}

// Config 打开参数。
type Config struct {
	// Path 分析库路径；空值使用 DefaultDBPath()。
	Path string
	// ReadOnly 只读打开：库不存在时所有查询返回空结果而非错误。
	ReadOnly bool
	// BusyTimeout 写锁等待；零值使用 defaultBusyTimeout。
	BusyTimeout time.Duration
}

// Store 是分析库句柄：迁移、语句重用与只读降级。
type Store struct {
	db       *sql.DB
	path     string
	readOnly bool
	// empty 只读打开但库/表不存在：查询返回空结果（不是错误）。
	empty bool

	mu sync.Mutex
}

// Open 打开（必要时创建并迁移）分析库。
//
// 抛出的错误只可能是确定性失败（路径不可写、schema 损坏）；瞬时写锁冲突
// （并发进程同时 migrate）按 openLockRetries 退避重试，避免多进程同时启动
// 时部分进程静默失去采集能力。
func Open(cfg Config) (*Store, error) {
	path := strings.TrimSpace(cfg.Path)
	if path == "" {
		path = DefaultDBPath()
	}
	path = filepath.Clean(path)

	if cfg.ReadOnly {
		return openReadOnly(path, cfg.BusyTimeout), nil
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("create usage analytics dir: %w", err)
	}
	var lastErr error
	for attempt := 0; attempt <= openLockRetries; attempt++ {
		if attempt > 0 {
			time.Sleep(openLockRetryWait(attempt - 1))
		}
		store, err := openWritable(path, cfg.BusyTimeout)
		if err == nil {
			if attempt > 0 {
				degradeWarn("分析库 %s 在 %d 次锁重试后打开成功（并发进程启动竞争写锁）", path, attempt)
			}
			return store, nil
		}
		lastErr = err
		if !isLockedError(err) {
			return nil, err
		}
	}
	return nil, fmt.Errorf("open usage analytics db after %d lock retries: %w", openLockRetries, lastErr)
}

// openWritable 建立一次可写连接并完成迁移（不做锁重试，由 Open 负责）。
func openWritable(path string, busyTimeout time.Duration) (*Store, error) {
	store := &Store{path: path}
	db, err := sql.Open("sqlite3", path)
	if err != nil {
		return nil, fmt.Errorf("open usage analytics db: %w", err)
	}
	// 单连接是必须的：PRAGMA 是连接级设置，池化多连接时 busy_timeout 只会
	// 落在恰好吃到该语句的那条连接上，其余连接在锁冲突上立即 SQLITE_BUSY；
	// 而写入错误此前被调用方静默丢弃 → 丢行且无痕迹。分析库的写入是事件
	// 驱动的短事务，单连接足够（与 internal/chat.SQLiteRuntimeStore 一致）。
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	store.db = db
	if err := store.migrate(busyTimeout); err != nil {
		_ = db.Close()
		store.db = nil
		return nil, err
	}
	return store, nil
}

// openReadOnly 只读打开：库不存在时返回空 store（查询返回空结果而非错误）。
func openReadOnly(path string, busyTimeout time.Duration) *Store {
	store := &Store{path: path, readOnly: true}
	if _, err := os.Stat(path); err != nil {
		store.empty = true
		return store
	}
	db, err := sql.Open("sqlite3", readOnlyDSN(path))
	if err != nil {
		store.empty = true
		return store
	}
	db.SetMaxOpenConns(2)
	if err := db.Ping(); err != nil {
		_ = db.Close()
		store.empty = true
		return store
	}
	if busyTimeout <= 0 {
		busyTimeout = defaultBusyTimeout
	}
	// 只读连接同样吃写锁等待：checkpoint / 迁移期间的读不应直接失败。
	_, _ = db.Exec(fmt.Sprintf("PRAGMA busy_timeout=%d", busyTimeout.Milliseconds()))
	store.db = db
	return store
}

// openLockRetryWait 返回第 retry 次重试（0-based）前的退避时长。
func openLockRetryWait(retry int) time.Duration {
	if retry < 0 {
		retry = 0
	}
	wait := openLockRetryBaseWait
	for i := 0; i < retry && wait < openLockRetryMaxWait; i++ {
		wait *= 2
	}
	if wait > openLockRetryMaxWait {
		wait = openLockRetryMaxWait
	}
	return wait
}

// isLockedError 报告 err 是否为瞬时 SQLite 锁冲突。
func isLockedError(err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "database is locked") ||
		strings.Contains(message, "database table is locked") ||
		strings.Contains(message, "database schema is locked")
}

// Close 关闭数据库句柄（幂等）。
func (s *Store) Close() error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.db == nil {
		return nil
	}
	err := s.db.Close()
	s.db = nil
	return err
}

// Path 返回分析库路径。
func (s *Store) Path() string {
	if s == nil {
		return ""
	}
	return s.path
}

// ReadOnly 报告打开模式。
func (s *Store) ReadOnly() bool {
	if s == nil {
		return false
	}
	return s.readOnly
}

// Empty 报告只读降级（库或表不存在）。
func (s *Store) Empty() bool {
	if s == nil {
		return true
	}
	return s.empty || s.db == nil
}

func readOnlyDSN(path string) string {
	slashed := filepath.ToSlash(path)
	if strings.HasPrefix(slashed, "file:") {
		return slashed
	}
	if !strings.HasPrefix(slashed, "/") && !looksLikeDrivePath(slashed) {
		slashed = "./" + slashed
	}
	return "file:" + slashed + "?mode=ro"
}

func looksLikeDrivePath(path string) bool {
	return len(path) >= 3 && path[1] == ':' && (path[2] == '/' || path[2] == '\\')
}

// migrate 建表 + 建索引（幂等，v1 契约表结构）。
func (s *Store) migrate(busyTimeout time.Duration) error {
	if s == nil || s.db == nil {
		return nil
	}
	if busyTimeout <= 0 {
		busyTimeout = defaultBusyTimeout
	}
	statements := []string{
		fmt.Sprintf("PRAGMA busy_timeout=%d", busyTimeout.Milliseconds()),
		"PRAGMA journal_mode=WAL",
		"PRAGMA synchronous=NORMAL",
		"PRAGMA foreign_keys=ON",
		`CREATE TABLE IF NOT EXISTS usage_requests (
  llm_request_id TEXT PRIMARY KEY,
  session_id TEXT NOT NULL,
  trace_id TEXT NOT NULL DEFAULT '',
  turn_id TEXT NOT NULL DEFAULT '',
  step INTEGER NOT NULL DEFAULT 0,
  provider TEXT NOT NULL DEFAULT '',
  model TEXT NOT NULL DEFAULT '',
  status TEXT NOT NULL DEFAULT '',
  cache_status TEXT NOT NULL DEFAULT '',
  success INTEGER NOT NULL DEFAULT 0,
  error_category TEXT NOT NULL DEFAULT '',
  started_at_unix_nano INTEGER NOT NULL DEFAULT 0,
  duration_ms INTEGER NOT NULL DEFAULT 0,
  prompt_tokens INTEGER NOT NULL DEFAULT 0,
  completion_tokens INTEGER NOT NULL DEFAULT 0,
  cache_read_tokens INTEGER NOT NULL DEFAULT 0,
  cache_creation_tokens INTEGER NOT NULL DEFAULT 0,
  reasoning_tokens INTEGER NOT NULL DEFAULT 0,
  total_tokens INTEGER NOT NULL DEFAULT 0,
  usage_available INTEGER NOT NULL DEFAULT 0,
  record_json BLOB NOT NULL DEFAULT ''
)`,
		`CREATE INDEX IF NOT EXISTS idx_usage_requests_session_started ON usage_requests(session_id, started_at_unix_nano DESC)`,
		`CREATE INDEX IF NOT EXISTS idx_usage_requests_started ON usage_requests(started_at_unix_nano DESC)`,
		`CREATE INDEX IF NOT EXISTS idx_usage_requests_provider ON usage_requests(provider)`,
		`CREATE INDEX IF NOT EXISTS idx_usage_requests_model ON usage_requests(model)`,
		`CREATE INDEX IF NOT EXISTS idx_usage_requests_trace ON usage_requests(session_id, trace_id, step)`,
		`CREATE TABLE IF NOT EXISTS usage_sessions (
  session_id TEXT PRIMARY KEY,
  title TEXT NOT NULL DEFAULT '',
  project_path TEXT NOT NULL DEFAULT '',
  working_directory TEXT NOT NULL DEFAULT '',
  provider TEXT NOT NULL DEFAULT '',
  model TEXT NOT NULL DEFAULT '',
  protocol TEXT NOT NULL DEFAULT '',
  status TEXT NOT NULL DEFAULT '',
  started_at_unix_nano INTEGER NOT NULL DEFAULT 0,
  ended_at_unix_nano INTEGER NOT NULL DEFAULT 0,
  updated_at_unix_nano INTEGER NOT NULL DEFAULT 0,
  meta_json BLOB
)`,
		`CREATE INDEX IF NOT EXISTS idx_usage_sessions_started ON usage_sessions(started_at_unix_nano DESC)`,
		`CREATE INDEX IF NOT EXISTS idx_usage_sessions_project ON usage_sessions(project_path)`,
		`CREATE INDEX IF NOT EXISTS idx_usage_sessions_provider ON usage_sessions(provider)`,
		`CREATE INDEX IF NOT EXISTS idx_usage_sessions_status ON usage_sessions(status)`,
	}
	for _, statement := range statements {
		if _, err := s.db.Exec(statement); err != nil {
			return fmt.Errorf("migrate usage analytics db: %w", err)
		}
	}
	return nil
}

// query 在只读降级时返回 (nil, false)：调用方给出空结果。
func (s *Store) query(query string, args ...interface{}) (*sql.Rows, bool, error) {
	if s == nil || s.db == nil || s.empty {
		return nil, false, nil
	}
	rows, err := s.db.Query(query, args...)
	if err != nil {
		if isMissingTableError(err) {
			return nil, false, nil
		}
		return nil, true, err
	}
	return rows, true, nil
}

// execContext 在只读降级时静默跳过。
func (s *Store) exec(query string, args ...interface{}) (bool, error) {
	if s == nil || s.db == nil || s.empty || s.readOnly {
		return false, nil
	}
	if _, err := s.db.Exec(query, args...); err != nil {
		return true, err
	}
	return true, nil
}

// execWithLockRetry 写一条语句，瞬时锁冲突时重试一次。
//
// 多进程共享同一分析库是常态：事件写入是短事务，重试一次即可覆盖绝大多数
// 竞争窗口；仍失败时把错误交回调用方（collector 会留下诊断而不是静默丢行）。
func (s *Store) execWithLockRetry(query string, args ...interface{}) error {
	_, err := s.exec(query, args...)
	if err == nil || !isLockedError(err) {
		return err
	}
	time.Sleep(writeLockRetryWait)
	if _, retryErr := s.exec(query, args...); retryErr != nil {
		return retryErr
	}
	return nil
}

func isMissingTableError(err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "no such table") || strings.Contains(message, "does not exist")
}
