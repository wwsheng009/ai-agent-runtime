package usageanalytics

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"strings"
	"time"
)

// ============================================================================
// schema v3：usage_sessions 预聚合计数列 + turn 去重键。
//
// 方案：docs/plan/usage-analytics-query-performance-optimization-plan-20260917.md
// §5.1（计数列）、§5.2（turn 去重表）、§6（迁移/回填/对账）。
//
// 设计要点：
//   - 计数列由请求终态写入在同一事务内做增量维护（§5.3），读取路径不再聚合
//     usage_requests 全表；
//   - 只读打开不跑迁移：user_version 未到 3 或列缺失时 statsReady()=false，
//     读路径自动回退旧 sessionSelect（§5.4）；
//   - 逃生开关 AICLI_USAGE_ANALYTICS_DISABLE_STATS=1 可强制读路径回退，
//     无需回滚版本（§10）。
// ============================================================================

// statsSchemaVersion 预聚合计数列的 schema 版本。
// v4 追加首字时间聚合列（c_total_first_token_ms / c_first_token_samples）。
const statsSchemaVersion = 4

// EnvDisableStats 逃生开关：置为 1/true/yes/on 时读路径强制走旧 sessionSelect。
// 仅影响读路径；schema v3 库的写入侧仍维护计数，避免回退期数据腐化。
const EnvDisableStats = "AICLI_USAGE_ANALYTICS_DISABLE_STATS"

// statsTurnKeysTable 保存 (session_id, turn_key) 去重键，替代不可增量的
// COUNT(DISTINCT ...)。turn_key 语义与旧 sessionSelect 保持一致。
const statsTurnKeysTable = "usage_session_turn_keys"

// statsTurnKeyExpr 旧 sessionSelect 的 turn 归属键表达式（usage_requests 别名域）。
const statsTurnKeyExpr = "COALESCE(NULLIF(trace_id, ''), NULLIF(turn_id, ''), llm_request_id)"

// aliasedTurnKeyExpr 与 statsTurnKeyExpr 同义，用于 usage_requests 别名为 r 的子查询。
const aliasedTurnKeyExpr = "COALESCE(NULLIF(r.trace_id, ''), NULLIF(r.turn_id, ''), r.llm_request_id)"

// statsColumnDef 单个预聚合列定义（顺序稳定：迁移、探测与文档共用）。
type statsColumnDef struct {
	name string
	ddl  string
}

var statsColumnDefs = []statsColumnDef{
	{"c_total_requests", "INTEGER NOT NULL DEFAULT 0"},
	{"c_llm_successes", "INTEGER NOT NULL DEFAULT 0"},
	{"c_llm_errors", "INTEGER NOT NULL DEFAULT 0"},
	{"c_requests_with_usage", "INTEGER NOT NULL DEFAULT 0"},
	{"c_total_tokens", "INTEGER NOT NULL DEFAULT 0"},
	{"c_prompt_tokens", "INTEGER NOT NULL DEFAULT 0"},
	{"c_completion_tokens", "INTEGER NOT NULL DEFAULT 0"},
	{"c_cached_tokens", "INTEGER NOT NULL DEFAULT 0"},
	{"c_reasoning_tokens", "INTEGER NOT NULL DEFAULT 0"},
	{"c_total_duration_ms", "INTEGER NOT NULL DEFAULT 0"},
	{"c_duration_samples", "INTEGER NOT NULL DEFAULT 0"},
	{"c_total_first_token_ms", "INTEGER NOT NULL DEFAULT 0"},
	{"c_first_token_samples", "INTEGER NOT NULL DEFAULT 0"},
	{"c_turn_count", "INTEGER NOT NULL DEFAULT 0"},
	{"c_failed_turns", "INTEGER NOT NULL DEFAULT 0"},
	{"c_first_started_at", "INTEGER NOT NULL DEFAULT 0"},
	{"c_last_started_at", "INTEGER NOT NULL DEFAULT 0"},
}

var statsColumnSet = func() map[string]struct{} {
	set := make(map[string]struct{}, len(statsColumnDefs))
	for _, column := range statsColumnDefs {
		set[column.name] = struct{}{}
	}
	return set
}()

const statsTurnKeysDDL = `CREATE TABLE IF NOT EXISTS ` + statsTurnKeysTable + ` (
  session_id TEXT NOT NULL,
  turn_key   TEXT NOT NULL,
  failed     INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY (session_id, turn_key)
) WITHOUT ROWID`

// ---------------------------------------------------------------------------
// 能力探测
// ---------------------------------------------------------------------------

// statsDisabledByEnv 报告逃生开关是否生效（每次读取，便于测试与运行期切换）。
func statsDisabledByEnv() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(EnvDisableStats))) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

// StatsReady 报告读路径是否可安全使用预聚合列（导出供 CLI/健康检查展示）。
func (s *Store) StatsReady() bool {
	return s.statsReady()
}

// StatsSchemaReady 报告底层库是否具备 v3 预聚合列（不受逃生开关影响）。
// 维护命令（rebuild-stats/prune）以此为门控：逃生开关只影响读路径。
func (s *Store) StatsSchemaReady() bool {
	return s.statsColumnsReady()
}

// statsReady 读路径判定：schema 就绪 且 未被逃生开关禁用。
func (s *Store) statsReady() bool {
	if statsDisabledByEnv() {
		return false
	}
	return s.statsColumnsReady()
}

// statsColumnsReady 报告底层库是否具备 v3 预聚合列（不看过剩开关）。
// 写入侧以此为开关，保证逃生开关只影响读路径。
func (s *Store) statsColumnsReady() bool {
	if s == nil || s.db == nil || s.empty {
		return false
	}
	s.statsOnce.Do(func() {
		s.statsAvailable = s.detectStatsSchema()
	})
	return s.statsAvailable
}

// markStatsSchemaReady 可写迁移成功后直接置位，避免二次探测。
func (s *Store) markStatsSchemaReady() {
	if s == nil {
		return
	}
	s.statsOnce.Do(func() { s.statsAvailable = true })
}

// detectStatsSchema 探测 user_version 与列集合；只读库同样适用（不改库）。
func (s *Store) detectStatsSchema() bool {
	version, err := s.schemaVersion()
	if err != nil || version < statsSchemaVersion {
		return false
	}
	rows, err := s.db.Query("PRAGMA table_info(usage_sessions)")
	if err != nil {
		return false
	}
	defer rows.Close()
	found := 0
	for rows.Next() {
		var (
			cid       int
			name      string
			columnTyp string
			notNull   int
			defaultV  sql.NullString
			pk        int
		)
		if err := rows.Scan(&cid, &name, &columnTyp, &notNull, &defaultV, &pk); err != nil {
			return false
		}
		if _, ok := statsColumnSet[name]; ok {
			found++
		}
	}
	if err := rows.Err(); err != nil {
		return false
	}
	return found == len(statsColumnDefs)
}

// hasColumn 报告表是否含指定列（用于旧库部分 schema 兼容）。
func (s *Store) hasColumn(table, column string) (bool, error) {
	if s == nil || s.db == nil || s.empty {
		return false, nil
	}
	rows, err := s.db.Query("PRAGMA table_info(" + table + ")")
	if err != nil {
		return false, fmt.Errorf("inspect %s columns: %w", table, err)
	}
	defer rows.Close()
	for rows.Next() {
		var (
			cid       int
			name      string
			columnTyp string
			notNull   int
			defaultV  sql.NullString
			pk        int
		)
		if err := rows.Scan(&cid, &name, &columnTyp, &notNull, &defaultV, &pk); err != nil {
			return false, fmt.Errorf("scan %s columns: %w", table, err)
		}
		if name == column {
			return true, nil
		}
	}
	if err := rows.Err(); err != nil {
		return false, fmt.Errorf("inspect %s columns: %w", table, err)
	}
	return false, nil
}

// requestsStatsColumnsComplete 报告 usage_requests 是否具备 v3 回填所需的
// 全部列。v1 部分 schema（历史遗留/裁剪库）缺列时跳过 v3，保持 v2 与旧读路径。
func (s *Store) requestsStatsColumnsComplete() (bool, error) {
	required := []string{
		"session_id", "trace_id", "turn_id", "success", "usage_available",
		"total_tokens", "prompt_tokens", "completion_tokens", "cache_read_tokens",
		"reasoning_tokens", "duration_ms", "first_token_ms", "started_at_unix_nano", "llm_request_id",
	}
	for _, column := range required {
		ok, err := s.hasColumn("usage_requests", column)
		if err != nil {
			return false, err
		}
		if !ok {
			return false, nil
		}
	}
	return true, nil
}

// schemaVersion 读取 PRAGMA user_version（空库/降级返回 0）。
func (s *Store) schemaVersion() (int, error) {
	if s == nil || s.db == nil || s.empty {
		return 0, nil
	}
	var version int
	if err := s.db.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		return 0, fmt.Errorf("read usage analytics user_version: %w", err)
	}
	return version, nil
}

// ---------------------------------------------------------------------------
// 迁移（§6.1）：加列 + turn 去重表 + 一次性回填 + user_version=3，单事务原子。
// ---------------------------------------------------------------------------

// migrateStatsV3 在单个写事务内完成 schema v3 升级；重复调用幂等。
func (s *Store) migrateStatsV3() error {
	if s == nil || s.db == nil || s.empty || s.readOnly {
		return nil
	}
	complete, err := s.requestsStatsColumnsComplete()
	if err != nil {
		return err
	}
	if !complete {
		// 部分 schema 旧库：不具备回填条件，保持 v2 并让读路径回退旧 sessionSelect。
		return nil
	}
	version, err := s.schemaVersion()
	if err != nil {
		return err
	}
	if version >= statsSchemaVersion {
		s.markStatsSchemaReady()
		return nil
	}
	if err := s.withWriteTx(func(tx *sql.Tx) error {
		if err := ensureStatsColumnsTx(tx); err != nil {
			return err
		}
		if _, err := tx.Exec(statsTurnKeysDDL); err != nil {
			return fmt.Errorf("create %s: %w", statsTurnKeysTable, err)
		}
		if err := rebuildSessionStatsTx(tx, ""); err != nil {
			return err
		}
		if _, err := tx.Exec(fmt.Sprintf("PRAGMA user_version = %d", statsSchemaVersion)); err != nil {
			return fmt.Errorf("set usage analytics user_version=%d: %w", statsSchemaVersion, err)
		}
		return nil
	}); err != nil {
		return err
	}
	s.markStatsSchemaReady()
	return nil
}

// ensureStatsColumnsTx 按 PRAGMA table_info 判重后补齐缺失列
// （SQLite 不支持 ADD COLUMN IF NOT EXISTS）。
func ensureStatsColumnsTx(tx *sql.Tx) error {
	rows, err := tx.Query("PRAGMA table_info(usage_sessions)")
	if err != nil {
		return fmt.Errorf("inspect usage_sessions columns: %w", err)
	}
	existing := make(map[string]struct{}, len(statsColumnDefs))
	for rows.Next() {
		var (
			cid       int
			name      string
			columnTyp string
			notNull   int
			defaultV  sql.NullString
			pk        int
		)
		if err := rows.Scan(&cid, &name, &columnTyp, &notNull, &defaultV, &pk); err != nil {
			_ = rows.Close()
			return fmt.Errorf("scan usage_sessions columns: %w", err)
		}
		existing[name] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return fmt.Errorf("inspect usage_sessions columns: %w", err)
	}
	_ = rows.Close()
	for _, column := range statsColumnDefs {
		if _, ok := existing[column.name]; ok {
			continue
		}
		statement := fmt.Sprintf("ALTER TABLE usage_sessions ADD COLUMN %s %s", column.name, column.ddl)
		if _, err := tx.Exec(statement); err != nil {
			return fmt.Errorf("add usage_sessions.%s: %w", column.name, err)
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// 回填/对账（§6.2/§6.3）：从 usage_requests 全量重算计数列 + turn 去重键。
// sessionID 为空表示全库；否则仅重建该会话。
// ---------------------------------------------------------------------------

func rebuildSessionStatsTx(tx *sql.Tx, sessionID string) error {
	if tx == nil {
		return nil
	}
	scope := ""
	args := []interface{}{}
	if trimmed := strings.TrimSpace(sessionID); trimmed != "" {
		scope = " WHERE session_id = ?"
		args = append(args, trimmed)
	}
	if _, err := tx.Exec("DELETE FROM "+statsTurnKeysTable+scope, args...); err != nil {
		return fmt.Errorf("reset %s: %w", statsTurnKeysTable, err)
	}
	insertKeys := `INSERT OR IGNORE INTO ` + statsTurnKeysTable + `(session_id, turn_key, failed)
SELECT session_id, ` + statsTurnKeyExpr + `, MAX(CASE WHEN success = 0 THEN 1 ELSE 0 END)
FROM usage_requests` + scope + `
GROUP BY session_id, ` + statsTurnKeyExpr
	if _, err := tx.Exec(insertKeys, args...); err != nil {
		return fmt.Errorf("rebuild %s: %w", statsTurnKeysTable, err)
	}
	update := `UPDATE usage_sessions SET
  c_total_requests      = COALESCE((SELECT COUNT(*) FROM usage_requests r WHERE r.session_id = usage_sessions.session_id), 0),
  c_llm_successes       = COALESCE((SELECT SUM(CASE WHEN r.success = 1 THEN 1 ELSE 0 END) FROM usage_requests r WHERE r.session_id = usage_sessions.session_id), 0),
  c_llm_errors          = COALESCE((SELECT SUM(CASE WHEN r.success = 0 THEN 1 ELSE 0 END) FROM usage_requests r WHERE r.session_id = usage_sessions.session_id), 0),
  c_requests_with_usage = COALESCE((SELECT SUM(CASE WHEN r.usage_available = 1 THEN 1 ELSE 0 END) FROM usage_requests r WHERE r.session_id = usage_sessions.session_id), 0),
  c_total_tokens        = COALESCE((SELECT SUM(r.total_tokens) FROM usage_requests r WHERE r.session_id = usage_sessions.session_id), 0),
  c_prompt_tokens       = COALESCE((SELECT SUM(r.prompt_tokens) FROM usage_requests r WHERE r.session_id = usage_sessions.session_id), 0),
  c_completion_tokens   = COALESCE((SELECT SUM(r.completion_tokens) FROM usage_requests r WHERE r.session_id = usage_sessions.session_id), 0),
  c_cached_tokens       = COALESCE((SELECT SUM(r.cache_read_tokens) FROM usage_requests r WHERE r.session_id = usage_sessions.session_id), 0),
  c_reasoning_tokens    = COALESCE((SELECT SUM(r.reasoning_tokens) FROM usage_requests r WHERE r.session_id = usage_sessions.session_id), 0),
  c_total_duration_ms   = COALESCE((SELECT SUM(r.duration_ms) FROM usage_requests r WHERE r.session_id = usage_sessions.session_id), 0),
  c_duration_samples    = COALESCE((SELECT SUM(CASE WHEN r.duration_ms <> 0 THEN 1 ELSE 0 END) FROM usage_requests r WHERE r.session_id = usage_sessions.session_id), 0),
  c_total_first_token_ms = COALESCE((SELECT SUM(r.first_token_ms) FROM usage_requests r WHERE r.session_id = usage_sessions.session_id), 0),
  c_first_token_samples  = COALESCE((SELECT SUM(CASE WHEN r.first_token_ms <> 0 THEN 1 ELSE 0 END) FROM usage_requests r WHERE r.session_id = usage_sessions.session_id), 0),
  c_turn_count          = (SELECT COUNT(*) FROM ` + statsTurnKeysTable + ` k WHERE k.session_id = usage_sessions.session_id),
  c_failed_turns        = (SELECT COUNT(*) FROM ` + statsTurnKeysTable + ` k WHERE k.session_id = usage_sessions.session_id AND k.failed = 1),
  c_first_started_at    = COALESCE((SELECT MIN(NULLIF(r.started_at_unix_nano, 0)) FROM usage_requests r WHERE r.session_id = usage_sessions.session_id), 0),
  c_last_started_at     = COALESCE((SELECT MAX(r.started_at_unix_nano) FROM usage_requests r WHERE r.session_id = usage_sessions.session_id), 0)`
	if scope != "" {
		update += scope
	}
	if _, err := tx.Exec(update, args...); err != nil {
		return fmt.Errorf("rebuild usage_sessions stats: %w", err)
	}
	return nil
}

// RebuildSessionStats 重建单个会话的预聚合计数与 turn 去重键（§6.3 对账命令）。
// 空 sessionID 返回参数错误。
func (s *Store) RebuildSessionStats(sessionID string) error {
	trimmed := strings.TrimSpace(sessionID)
	if trimmed == "" {
		return fmt.Errorf("rebuild session stats: session id is required")
	}
	if !s.statsColumnsReady() {
		return fmt.Errorf("rebuild session stats: analytics schema v%d is not ready", statsSchemaVersion)
	}
	return s.withWriteTx(func(tx *sql.Tx) error {
		return rebuildSessionStatsTx(tx, trimmed)
	})
}

// RebuildAllSessionStats 重建全库会话的预聚合计数与 turn 去重键。
func (s *Store) RebuildAllSessionStats() error {
	if !s.statsColumnsReady() {
		return fmt.Errorf("rebuild session stats: analytics schema v%d is not ready", statsSchemaVersion)
	}
	return s.withWriteTx(func(tx *sql.Tx) error {
		return rebuildSessionStatsTx(tx, "")
	})
}

// ---------------------------------------------------------------------------
// 写事务（§5.3）：_txlock=immediate DSN + 单连接池，天然串行；
// 锁冲突沿用 execWithLockRetry 的一次性重试策略。
// ---------------------------------------------------------------------------

const writeTxLockRetries = 1

// withWriteTx 在单个事务内执行写操作；fn 必须只使用传入的 tx
// （单连接池下混用 s.db 会等待连接造成死锁）。
func (s *Store) withWriteTx(fn func(tx *sql.Tx) error) error {
	if s == nil || s.db == nil || s.empty || s.readOnly || fn == nil {
		return nil
	}
	var lastErr error
	for attempt := 0; attempt <= writeTxLockRetries; attempt++ {
		if attempt > 0 {
			time.Sleep(writeLockRetryWait)
		}
		tx, err := s.db.BeginTx(context.Background(), nil)
		if err != nil {
			if isLockedError(err) {
				lastErr = err
				continue
			}
			return fmt.Errorf("begin usage analytics write tx: %w", err)
		}
		if err := fn(tx); err != nil {
			_ = tx.Rollback()
			if isLockedError(err) {
				lastErr = err
				continue
			}
			return err
		}
		if err := tx.Commit(); err != nil {
			if isLockedError(err) {
				lastErr = err
				continue
			}
			return fmt.Errorf("commit usage analytics write tx: %w", err)
		}
		return nil
	}
	return fmt.Errorf("usage analytics write tx after %d lock retries: %w", writeTxLockRetries, lastErr)
}
