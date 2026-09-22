package usageanalytics

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
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

	// statsOnce/statsAvailable：schema v3 预聚合列能力探测缓存
	// （可写迁移成功后直接置位；只读库按 user_version + 列集合惰性探测）。
	statsOnce      sync.Once
	statsAvailable bool
	// statsGen 写入代数：请求终态落库后自增，供服务端查询缓存惰性失效。
	statsGen atomic.Uint64
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
	// _txlock=immediate：写事务一开始就取写锁。默认 deferred 事务在
	// "先读旧计数、再写" 的升级路径上遇到并发进程写锁会立即返回
	// SQLITE_BUSY（busy_timeout 不适用），而分析库在多进程间共享是常态。
	db, err := sql.Open("sqlite3", writableDSN(path))
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

// writableDSN 构造可写 DSN：文件 URI + _txlock=immediate（见 openWritable）。
func writableDSN(path string) string {
	slashed := filepath.ToSlash(path)
	if strings.HasPrefix(slashed, "file:") {
		if strings.Contains(slashed, "?") {
			return slashed + "&_txlock=immediate"
		}
		return slashed + "?_txlock=immediate"
	}
	if !strings.HasPrefix(slashed, "/") && !looksLikeDrivePath(slashed) {
		slashed = "./" + slashed
	}
	return "file:" + slashed + "?_txlock=immediate"
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
  first_token_ms INTEGER NOT NULL DEFAULT 0,
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
		// 复合索引：时间窗 + 常用过滤维度，覆盖 Summarize/ListSessions 的 WHERE + ORDER BY
		`CREATE INDEX IF NOT EXISTS idx_usage_sessions_time_provider_model_status ON usage_sessions(started_at_unix_nano DESC, provider, model, status)`,
		// ---- schema v2（方案 §5.1）：工具调用 / 子代理 / 回合级终值 ----
		`CREATE TABLE IF NOT EXISTS usage_tool_calls (
  tool_call_id            TEXT PRIMARY KEY,
  session_id              TEXT NOT NULL DEFAULT '',
  trace_id                TEXT NOT NULL DEFAULT '',
  turn_id                 TEXT NOT NULL DEFAULT '',
  step                    INTEGER NOT NULL DEFAULT 0,
  tool_name               TEXT NOT NULL DEFAULT '',
  source                  TEXT NOT NULL DEFAULT '',
  kind                    TEXT NOT NULL DEFAULT '',
  outcome                 TEXT NOT NULL DEFAULT '',
  ok                      INTEGER,
  empty_result            INTEGER NOT NULL DEFAULT 0,
  error_code              TEXT NOT NULL DEFAULT '',
  retryable               INTEGER,
  failed_count            INTEGER NOT NULL DEFAULT 0,
  succeeded_count         INTEGER NOT NULL DEFAULT 0,
  started_at_unix_nano    INTEGER NOT NULL DEFAULT 0,
  completed_at_unix_nano  INTEGER NOT NULL DEFAULT 0,
  duration_ms             INTEGER NOT NULL DEFAULT 0,
  record_json             BLOB
)`,
		`CREATE INDEX IF NOT EXISTS idx_usage_tool_calls_session ON usage_tool_calls(session_id, started_at_unix_nano DESC)`,
		`CREATE INDEX IF NOT EXISTS idx_usage_tool_calls_name ON usage_tool_calls(tool_name)`,
		`CREATE INDEX IF NOT EXISTS idx_usage_tool_calls_outcome ON usage_tool_calls(outcome, error_code)`,
		`CREATE TABLE IF NOT EXISTS usage_subagents (
  subagent_id             TEXT NOT NULL DEFAULT '',
  parent_session_id       TEXT NOT NULL DEFAULT '',
  child_session_id        TEXT NOT NULL DEFAULT '',
  role                    TEXT NOT NULL DEFAULT '',
  read_only               INTEGER NOT NULL DEFAULT 0,
  success                 INTEGER,
  completion_reason       TEXT NOT NULL DEFAULT '',
  failure_category        TEXT NOT NULL DEFAULT '',
  error_code              TEXT NOT NULL DEFAULT '',
  attempt                 INTEGER NOT NULL DEFAULT 1,
  max_attempts            INTEGER NOT NULL DEFAULT 1,
  retry_reason            TEXT NOT NULL DEFAULT '',
  id_synthesized          INTEGER NOT NULL DEFAULT 0,
  duration_ms             INTEGER NOT NULL DEFAULT 0,
  started_at_unix_nano    INTEGER NOT NULL DEFAULT 0,
  completed_at_unix_nano  INTEGER NOT NULL DEFAULT 0,
  usage_total_tokens      INTEGER NOT NULL DEFAULT 0,
  source                  TEXT NOT NULL DEFAULT '',
  conflict_count          INTEGER NOT NULL DEFAULT 0,
  record_json             BLOB,
  PRIMARY KEY (subagent_id, parent_session_id)
)`,
		`CREATE INDEX IF NOT EXISTS idx_usage_subagents_session ON usage_subagents(parent_session_id, completed_at_unix_nano DESC)`,
		`CREATE INDEX IF NOT EXISTS idx_usage_subagents_fail ON usage_subagents(success, failure_category)`,
		`CREATE TABLE IF NOT EXISTS usage_turns (
  session_id                   TEXT NOT NULL,
  turn_id                      TEXT NOT NULL,
  trace_id                     TEXT NOT NULL DEFAULT '',
  success                      INTEGER,
  completion_reason            TEXT NOT NULL DEFAULT '',
  error_code                   TEXT NOT NULL DEFAULT '',
  steps                        INTEGER NOT NULL DEFAULT 0,
  duration_ms                  INTEGER NOT NULL DEFAULT 0,
  tool_error_count             INTEGER NOT NULL DEFAULT 0,
  recovered_tool_error_count   INTEGER NOT NULL DEFAULT 0,
  unrecovered_tool_error_count INTEGER NOT NULL DEFAULT 0,
  prompt_tokens                INTEGER NOT NULL DEFAULT 0,
  completion_tokens            INTEGER NOT NULL DEFAULT 0,
  total_tokens                 INTEGER NOT NULL DEFAULT 0,
  cache_read_tokens            INTEGER NOT NULL DEFAULT 0,
  reasoning_tokens             INTEGER NOT NULL DEFAULT 0,
  started_at_unix_nano         INTEGER NOT NULL DEFAULT 0,
  ended_at_unix_nano           INTEGER NOT NULL DEFAULT 0,
  record_json                  BLOB,
  PRIMARY KEY (session_id, turn_id)
)`,
		`CREATE INDEX IF NOT EXISTS idx_usage_turns_time ON usage_turns(session_id, ended_at_unix_nano DESC)`,
		// ---- 路由切换观测（主 Agent / 子 Agent）：统一表 + 维度索引 ----
		// 幂等 DDL：老库打开时自动补表，无需单独版本迁移（与 v2 三表同策略）。
		`CREATE TABLE IF NOT EXISTS usage_routes (
  route_event_id          TEXT PRIMARY KEY,
  session_id              TEXT NOT NULL DEFAULT '',
  parent_session_id       TEXT NOT NULL DEFAULT '',
  child_session_id        TEXT NOT NULL DEFAULT '',
  trace_id                TEXT NOT NULL DEFAULT '',
  scope                   TEXT NOT NULL DEFAULT '',
  kind                    TEXT NOT NULL DEFAULT '',
  agent_id                TEXT NOT NULL DEFAULT '',
  role                    TEXT NOT NULL DEFAULT '',
  goal                    TEXT NOT NULL DEFAULT '',
  step                    INTEGER NOT NULL DEFAULT 0,
  reason                  TEXT NOT NULL DEFAULT '',
  source                  TEXT NOT NULL DEFAULT '',
  difficulty              TEXT NOT NULL DEFAULT '',
  difficulty_source       TEXT NOT NULL DEFAULT '',
  provider                TEXT NOT NULL DEFAULT '',
  model                   TEXT NOT NULL DEFAULT '',
  reasoning_effort        TEXT NOT NULL DEFAULT '',
  route_changed           INTEGER,
  fallback_used           INTEGER,
  fallback_reason         TEXT NOT NULL DEFAULT '',
  candidate_count         INTEGER NOT NULL DEFAULT 0,
  warning_count           INTEGER NOT NULL DEFAULT 0,
  attempt                 INTEGER NOT NULL DEFAULT 0,
  max_attempts            INTEGER NOT NULL DEFAULT 0,
  batch_id                TEXT NOT NULL DEFAULT '',
  recorded_at_unix_nano   INTEGER NOT NULL DEFAULT 0,
  warnings_json           TEXT NOT NULL DEFAULT '',
  candidates_json         TEXT NOT NULL DEFAULT '',
  record_json             BLOB
)`,
		`CREATE INDEX IF NOT EXISTS idx_usage_routes_session ON usage_routes(session_id, recorded_at_unix_nano DESC)`,
		`CREATE INDEX IF NOT EXISTS idx_usage_routes_time ON usage_routes(recorded_at_unix_nano DESC)`,
		`CREATE INDEX IF NOT EXISTS idx_usage_routes_scope ON usage_routes(scope, kind, recorded_at_unix_nano DESC)`,
		`CREATE INDEX IF NOT EXISTS idx_usage_routes_route ON usage_routes(provider, model)`,
		`CREATE INDEX IF NOT EXISTS idx_usage_routes_source ON usage_routes(source, difficulty)`,
	}
	for _, statement := range statements {
		if _, err := s.db.Exec(statement); err != nil {
			return fmt.Errorf("migrate usage analytics db: %w", err)
		}
	}
	// v4 增量列：旧库（v1/v2/v3）缺 first_token_ms 时补齐（SQLite 无
	// ADD COLUMN IF NOT EXISTS）。只读库不改库：读路径按列存在性退化，
	// 显示"未采集"而不是报错。
	if !s.readOnly {
		if hasFirstToken, err := s.hasColumn("usage_requests", "first_token_ms"); err != nil {
			return err
		} else if !hasFirstToken {
			if _, err := s.db.Exec("ALTER TABLE usage_requests ADD COLUMN first_token_ms INTEGER NOT NULL DEFAULT 0"); err != nil {
				return fmt.Errorf("migrate usage analytics db: %w", err)
			}
		}
	}
	// 可选索引：旧库可能缺少 error_category 列（v1 部分 schema），先探测再建。
	// v5 增量列：usage_routes.goal（子代理任务目标）。旧库缺列时补齐；只读库不改库，
	// 读路径按列存在性退化（见 query_routes.go 的 goalExpr）。
	if !s.readOnly {
		if hasGoal, err := s.hasColumn("usage_routes", "goal"); err != nil {
			return err
		} else if !hasGoal {
			if _, err := s.db.Exec("ALTER TABLE usage_routes ADD COLUMN goal TEXT NOT NULL DEFAULT ''"); err != nil {
				return fmt.Errorf("migrate usage analytics db: %w", err)
			}
		}
	}
	// 错误模式部分索引（Phase 4）只服务 usage_requests(error_category)。
	if hasErrorCategory, err := s.hasColumn("usage_requests", "error_category"); err != nil {
		return err
	} else if hasErrorCategory {
		if _, err := s.db.Exec(`CREATE INDEX IF NOT EXISTS idx_usage_requests_error_category ON usage_requests(error_category) WHERE error_category <> ''`); err != nil {
			return fmt.Errorf("migrate usage analytics db: %w", err)
		}
	}
	// 版本门控迁移：v1/v2 基础表 → v2 版本号 → v3 预聚合列（§6.1）。
	// 各步骤幂等；v3 迁移失败回滚后库保持 v2 可读。
	version, err := s.schemaVersion()
	if err != nil {
		return err
	}
	if version < 2 {
		if _, err := s.db.Exec("PRAGMA user_version = 2"); err != nil {
			return fmt.Errorf("migrate usage analytics db: %w", err)
		}
	}
	return s.migrateStatsV3()
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
