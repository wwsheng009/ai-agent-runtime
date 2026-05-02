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
			message_count, max_tokens, success, status_code, metadata_json, created_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
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
		       message_count, max_tokens, success, status_code, metadata_json, created_at
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
			created_at TEXT NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS idx_token_usage_history_created_at
			ON token_usage_history(created_at DESC, id DESC)`,
	}
	for _, statement := range statements {
		if _, err := s.db.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("initialize usage ledger store: %w", err)
		}
	}
	return nil
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
