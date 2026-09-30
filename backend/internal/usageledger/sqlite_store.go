package usageledger

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/model/entity"

	_ "github.com/wwsheng009/ai-agent-runtime/internal/sqlitedriver"
)

// Config describes how the usage ledger store should connect.
type Config struct {
	Driver string
	DSN    string
}

// SQLiteStore persists usage ledger records in sqlite.
type SQLiteStore struct {
	db *sql.DB
}

// NewSQLiteStore opens or creates a sqlite-backed usage ledger store.
func NewSQLiteStore(cfg *Config) (*SQLiteStore, error) {
	if cfg == nil {
		cfg = &Config{}
	}

	driver := strings.ToLower(strings.TrimSpace(cfg.Driver))
	switch driver {
	case "", "sqlite", "sqlite3":
	default:
		return nil, fmt.Errorf("unsupported usage ledger driver: %s", cfg.Driver)
	}

	dsn, err := resolveSQLiteDSN(cfg.DSN)
	if err != nil {
		return nil, err
	}

	db, err := sql.Open("sqlite3", dsn)
	if err != nil {
		return nil, fmt.Errorf("open usage ledger db: %w", err)
	}

	store := &SQLiteStore{db: db}
	if err := store.init(context.Background()); err != nil {
		_ = db.Close()
		return nil, err
	}
	return store, nil
}

// Close closes the underlying database handle.
func (s *SQLiteStore) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

// Create persists a usage ledger record.
func (s *SQLiteStore) Create(history *entity.TokenUsageHistory) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("usage ledger store is not initialized")
	}
	if history == nil {
		return fmt.Errorf("usage ledger record is required")
	}

	record := *history
	record.BeforeCreate()

	var metadataJSON interface{}
	if len(record.Metadata) > 0 {
		payload, err := json.Marshal(record.Metadata)
		if err != nil {
			return fmt.Errorf("marshal usage ledger metadata: %w", err)
		}
		metadataJSON = string(payload)
	}

	_, err := s.db.ExecContext(context.Background(), `
		INSERT INTO token_usage_history (
			id, request_id, model_id, provider_id, input_tokens, output_tokens, total_tokens,
			message_count, max_tokens, success, status_code, metadata_json, created_at,
			exploration_tokens, reuse_tokens, index_lookup_count, index_hit, fallback_count,
			unsafe_reuse_count, tool_calls_per_task, repeated_read_count,
			knowledge_version_mismatch_count
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`,
		record.ID,
		nullIfEmpty(record.RequestID),
		nullIfEmpty(record.ModelID),
		nullIfEmpty(record.ProviderID),
		record.InputTokens,
		record.OutputTokens,
		record.TotalTokens,
		record.MessageCount,
		record.MaxTokens,
		boolToInt(record.Success),
		record.StatusCode,
		metadataJSON,
		time.Time(record.CreatedAt).UTC().Format(time.RFC3339Nano),
		record.ExplorationTokens,
		record.ReuseTokens,
		record.IndexLookupCount,
		record.IndexHit,
		record.FallbackCount,
		record.UnsafeReuseCount,
		record.ToolCallsPerTask,
		record.RepeatedReadCount,
		record.KnowledgeVersionMismatchCount,
	)
	if err != nil {
		return fmt.Errorf("insert usage ledger record: %w", err)
	}
	return nil
}

// GetSince returns records newer than or equal to the provided timestamp, newest first.
func (s *SQLiteStore) GetSince(since time.Time, limit int) ([]*entity.TokenUsageHistory, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("usage ledger store is not initialized")
	}
	if limit <= 0 {
		limit = 100
	}

	query := `
		SELECT id, request_id, model_id, provider_id, input_tokens, output_tokens, total_tokens,
		       message_count, max_tokens, success, status_code, metadata_json, created_at,
		       exploration_tokens, reuse_tokens, index_lookup_count, index_hit, fallback_count,
		       unsafe_reuse_count, tool_calls_per_task, repeated_read_count,
		       knowledge_version_mismatch_count
		FROM token_usage_history
	`
	args := make([]interface{}, 0, 2)
	if !since.IsZero() {
		query += ` WHERE created_at >= ?`
		args = append(args, since.UTC().Format(time.RFC3339Nano))
	}
	query += ` ORDER BY created_at DESC, id DESC LIMIT ?`
	args = append(args, limit)

	rows, err := s.db.QueryContext(context.Background(), query, args...)
	if err != nil {
		return nil, fmt.Errorf("query usage ledger records: %w", err)
	}
	defer rows.Close()

	records := make([]*entity.TokenUsageHistory, 0, limit)
	for rows.Next() {
		record := &entity.TokenUsageHistory{}
		var (
			requestID  sql.NullString
			modelID    sql.NullString
			providerID sql.NullString
			successInt int
			metadata   sql.NullString
		)
		if err := rows.Scan(
			&record.ID,
			&requestID,
			&modelID,
			&providerID,
			&record.InputTokens,
			&record.OutputTokens,
			&record.TotalTokens,
			&record.MessageCount,
			&record.MaxTokens,
			&successInt,
			&record.StatusCode,
			&metadata,
			&record.CreatedAt,
			&record.ExplorationTokens,
			&record.ReuseTokens,
			&record.IndexLookupCount,
			&record.IndexHit,
			&record.FallbackCount,
			&record.UnsafeReuseCount,
			&record.ToolCallsPerTask,
			&record.RepeatedReadCount,
			&record.KnowledgeVersionMismatchCount,
		); err != nil {
			return nil, fmt.Errorf("scan usage ledger record: %w", err)
		}
		if requestID.Valid {
			record.RequestID = requestID.String
		}
		if modelID.Valid {
			record.ModelID = modelID.String
		}
		if providerID.Valid {
			record.ProviderID = providerID.String
		}
		record.Success = successInt != 0
		if metadata.Valid && strings.TrimSpace(metadata.String) != "" {
			if err := json.Unmarshal([]byte(metadata.String), &record.Metadata); err != nil {
				return nil, fmt.Errorf("decode usage ledger metadata: %w", err)
			}
		}
		records = append(records, record)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return records, nil
}

func (s *SQLiteStore) init(ctx context.Context) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("usage ledger store is not initialized")
	}
	statements := []string{
		`CREATE TABLE IF NOT EXISTS token_usage_history (
			id TEXT PRIMARY KEY,
			request_id TEXT,
			model_id TEXT,
			provider_id TEXT,
			input_tokens INTEGER NOT NULL DEFAULT 0,
			output_tokens INTEGER NOT NULL DEFAULT 0,
			total_tokens INTEGER NOT NULL DEFAULT 0,
			message_count INTEGER NOT NULL DEFAULT 0,
			max_tokens INTEGER NOT NULL DEFAULT 0,
			success INTEGER NOT NULL DEFAULT 0,
			status_code INTEGER NOT NULL DEFAULT 0,
			metadata_json BLOB,
			created_at TEXT NOT NULL,
			exploration_tokens INTEGER NOT NULL DEFAULT 0,
			reuse_tokens INTEGER NOT NULL DEFAULT 0,
			index_lookup_count INTEGER NOT NULL DEFAULT 0,
			index_hit INTEGER NOT NULL DEFAULT 0,
			fallback_count INTEGER NOT NULL DEFAULT 0,
			unsafe_reuse_count INTEGER NOT NULL DEFAULT 0,
			tool_calls_per_task INTEGER NOT NULL DEFAULT 0,
			repeated_read_count INTEGER NOT NULL DEFAULT 0,
			knowledge_version_mismatch_count INTEGER NOT NULL DEFAULT 0
		)`,
		`CREATE INDEX IF NOT EXISTS idx_token_usage_history_created_at
			ON token_usage_history(created_at DESC, id DESC)`,
		// Phase 0 交付 7 / ADR-0003 §4.1：探索归因与 shadow 差异率度量。
		// 只建表与索引，不产生数据（mode=off 下没有任何调用方会写入）。
		// DDL 与 ADR-0003 §4.1 逐列对齐；追加语句复用 IF NOT EXISTS 幂等语义（D7）。
		// baseline_files_n / overlap_files_n 是 ADR-0008 §4 的 additive file-level
		// 列（grep 主判据），历史库经 ensureExplorationFileColumns 补齐。
		`CREATE TABLE IF NOT EXISTS exploration_attribution (
			id TEXT PRIMARY KEY,
			session_id TEXT,
			turn_id TEXT,
			request_id TEXT,
			tool TEXT NOT NULL,
			query_hash TEXT,
			project_id TEXT,
			baseline_n INTEGER NOT NULL DEFAULT 0,
			baseline_files_n INTEGER NOT NULL DEFAULT 0,
			candidate_n INTEGER NOT NULL DEFAULT 0,
			overlap_n INTEGER NOT NULL DEFAULT 0,
			overlap_files_n INTEGER NOT NULL DEFAULT 0,
			baseline_tokens INTEGER NOT NULL DEFAULT 0,
			candidate_tokens INTEGER NOT NULL DEFAULT 0,
			coverage REAL,
			economy REAL,
			usable INTEGER NOT NULL DEFAULT 0,
			source TEXT,
			knowledge_mode TEXT NOT NULL,
			created_at TEXT NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS idx_exploration_attribution_created_at
			ON exploration_attribution(created_at DESC, id DESC)`,
		`CREATE INDEX IF NOT EXISTS idx_exploration_attribution_tool_time
			ON exploration_attribution(tool, created_at DESC)`,
	}
	for _, statement := range statements {
		if _, err := s.db.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("initialize usage ledger store: %w", err)
		}
	}
	if err := s.ensureLedgerMetricColumns(ctx); err != nil {
		return err
	}
	return s.ensureExplorationFileColumns(ctx)
}

// ledgerMetricColumns 是 Phase 0（04 §5 交付 2）新增的 9 个知识层度量列。
//
// 它们必须对既有库幂等补齐：老库只有 13 列，缺列时 Create 会直接报
// "no such column"，而 ADD COLUMN 是元数据级变更，历史行取 DEFAULT 0，
// 旧数据的语义与改动前完全一致（mode=off 时新列恒为 0）。
var ledgerMetricColumns = []string{
	"exploration_tokens",
	"reuse_tokens",
	"index_lookup_count",
	"index_hit",
	"fallback_count",
	"unsafe_reuse_count",
	"tool_calls_per_task",
	"repeated_read_count",
	"knowledge_version_mismatch_count",
}

// ensureLedgerMetricColumns 为既有库补齐 Phase 0 新增列。
func (s *SQLiteStore) ensureLedgerMetricColumns(ctx context.Context) error {
	existing, err := s.tableColumns(ctx, "token_usage_history")
	if err != nil {
		return err
	}
	for _, column := range ledgerMetricColumns {
		if existing[column] {
			continue
		}
		statement := `ALTER TABLE token_usage_history ADD COLUMN ` + column + ` INTEGER NOT NULL DEFAULT 0`
		if _, err := s.db.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("add usage ledger column %s: %w", column, err)
		}
	}
	return nil
}

// explorationFileColumns 是 ADR-0008 §4 / §8.1 为 exploration_attribution
// 新增的 2 个 file-level 列。与 ledgerMetricColumns 同策略：老库缺列时
// ADD COLUMN 是元数据级变更，历史行取 DEFAULT 0，历史语义与改动前一致
// （ADR-0008 §6.2：历史行不参与 file-level M1）。
var explorationFileColumns = []string{
	"baseline_files_n",
	"overlap_files_n",
}

// ensureExplorationFileColumns 为既有库补齐 ADR-0008 §4 的 file-level 列。
func (s *SQLiteStore) ensureExplorationFileColumns(ctx context.Context) error {
	existing, err := s.tableColumns(ctx, "exploration_attribution")
	if err != nil {
		return err
	}
	for _, column := range explorationFileColumns {
		if existing[column] {
			continue
		}
		statement := `ALTER TABLE exploration_attribution ADD COLUMN ` + column + ` INTEGER NOT NULL DEFAULT 0`
		if _, err := s.db.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("add exploration attribution column %s: %w", column, err)
		}
	}
	return nil
}

// tableColumns 返回指定表的现有列名集合（表名只允许内部常量，不做拼接注入面）。
func (s *SQLiteStore) tableColumns(ctx context.Context, table string) (map[string]bool, error) {
	rows, err := s.db.QueryContext(ctx, `PRAGMA table_info(`+table+`)`)
	if err != nil {
		return nil, fmt.Errorf("inspect %s schema: %w", table, err)
	}
	defer rows.Close()

	columns := make(map[string]bool)
	for rows.Next() {
		var (
			cid       int
			name      string
			ctype     string
			notNull   int
			dfltValue sql.NullString
			primary   int
		)
		if err := rows.Scan(&cid, &name, &ctype, &notNull, &dfltValue, &primary); err != nil {
			return nil, fmt.Errorf("scan %s schema: %w", table, err)
		}
		columns[name] = true
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return columns, nil
}

// AppendExplorationAttribution 追加一行 exploration_attribution（ADR-0003 §4.1）。
//
// 该表与 token_usage_history 完全隔离：追加本表数据不得以任何形式写入
// token_usage_history（D3 第 1 条）。coverage / economy 允许 NULL
// （baseline_n == 0 或 baseline_tokens == 0 时无定义）。
func (s *SQLiteStore) AppendExplorationAttribution(ctx context.Context, rec *entity.ExplorationAttribution) error {
	if rec == nil {
		return nil
	}
	if s == nil || s.db == nil {
		return fmt.Errorf("usageledger: append exploration attribution: store is not open")
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO exploration_attribution (
			id, session_id, turn_id, request_id, tool, query_hash, project_id,
			baseline_n, baseline_files_n, candidate_n, overlap_n, overlap_files_n,
			baseline_tokens, candidate_tokens,
			coverage, economy, usable, source, knowledge_mode, created_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		rec.ID,
		nullIfEmpty(rec.SessionID),
		nullIfEmpty(rec.TurnID),
		nullIfEmpty(rec.RequestID),
		rec.Tool,
		nullIfEmpty(rec.QueryHash),
		nullIfEmpty(rec.ProjectID),
		rec.BaselineN,
		rec.BaselineFilesN,
		rec.CandidateN,
		rec.OverlapN,
		rec.OverlapFilesN,
		rec.BaselineTokens,
		rec.CandidateTokens,
		nullableFloat(rec.Coverage),
		nullableFloat(rec.Economy),
		boolToInt(rec.Usable),
		nullIfEmpty(rec.Source),
		rec.KnowledgeMode,
		rec.CreatedAt.UTC().Format(time.RFC3339Nano),
	)
	if err != nil {
		return fmt.Errorf("usageledger: append exploration attribution: %w", err)
	}
	return nil
}

func nullableFloat(value *float64) interface{} {
	if value == nil {
		return nil
	}
	return *value
}

func resolveSQLiteDSN(dsn string) (string, error) {
	dsn = strings.TrimSpace(dsn)
	if dsn == "" {
		return "", fmt.Errorf("usage ledger dsn is required")
	}
	if isSQLiteURI(dsn) || dsn == ":memory:" {
		return dsn, nil
	}
	dir := filepath.Dir(dsn)
	if dir != "." && dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return "", fmt.Errorf("create usage ledger directory: %w", err)
		}
	}
	return dsn, nil
}

func isSQLiteURI(dsn string) bool {
	if strings.HasPrefix(dsn, "file:") {
		return true
	}
	return strings.Contains(dsn, "?")
}

func nullIfEmpty(value string) interface{} {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	return value
}

func boolToInt(value bool) int {
	if value {
		return 1
	}
	return 0
}
