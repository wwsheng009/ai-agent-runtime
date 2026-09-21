package usageledger

import (
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/wwsheng009/ai-agent-runtime/internal/model/entity"
)

// Phase 0 交付 2：9 个知识层度量字段必须能落库并原样读回。
func TestSQLiteStore_KnowledgeMetricFieldsRoundTrip(t *testing.T) {
	store, err := NewSQLiteStore(&Config{
		Driver: "sqlite",
		DSN:    filepath.Join(t.TempDir(), "ledger.db"),
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()) })

	record := &entity.TokenUsageHistory{
		RequestID:                     "req-metrics",
		InputTokens:                   100,
		OutputTokens:                  40,
		Success:                       true,
		ExplorationTokens:             30,
		ReuseTokens:                   12,
		IndexLookupCount:              7,
		IndexHit:                      5,
		FallbackCount:                 2,
		UnsafeReuseCount:              0,
		ToolCallsPerTask:              9,
		RepeatedReadCount:             3,
		KnowledgeVersionMismatchCount: 0,
		CreatedAt:                     entity.Time(time.Now().UTC()),
	}
	require.NoError(t, store.Create(record))

	records, err := store.GetSince(time.Time{}, 10)
	require.NoError(t, err)
	require.Len(t, records, 1)

	got := records[0]
	require.Equal(t, 30, got.ExplorationTokens)
	require.Equal(t, 12, got.ReuseTokens)
	require.Equal(t, 7, got.IndexLookupCount)
	require.Equal(t, 5, got.IndexHit)
	require.Equal(t, 2, got.FallbackCount)
	require.Zero(t, got.UnsafeReuseCount)
	require.Equal(t, 9, got.ToolCallsPerTask)
	require.Equal(t, 3, got.RepeatedReadCount)
	require.Zero(t, got.KnowledgeVersionMismatchCount)
}

// 未设置度量字段时（mode=off 的全部既有调用方）新列必须恒为 0，保证
// “零行为变化”：ledger 复算结果与改动前一致。
func TestSQLiteStore_KnowledgeMetricsDefaultToZero(t *testing.T) {
	store, err := NewSQLiteStore(&Config{
		Driver: "sqlite",
		DSN:    filepath.Join(t.TempDir(), "ledger.db"),
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()) })

	require.NoError(t, store.Create(&entity.TokenUsageHistory{
		RequestID:    "req-legacy",
		InputTokens:  10,
		OutputTokens: 5,
		Success:      true,
		CreatedAt:    entity.Time(time.Now().UTC()),
	}))

	records, err := store.GetSince(time.Time{}, 10)
	require.NoError(t, err)
	require.Len(t, records, 1)

	got := records[0]
	require.Zero(t, got.ExplorationTokens)
	require.Zero(t, got.ReuseTokens)
	require.Zero(t, got.IndexLookupCount)
	require.Zero(t, got.IndexHit)
	require.Zero(t, got.FallbackCount)
	require.Zero(t, got.UnsafeReuseCount)
	require.Zero(t, got.ToolCallsPerTask)
	require.Zero(t, got.RepeatedReadCount)
	require.Zero(t, got.KnowledgeVersionMismatchCount)
}

// 旧库（只有 13 列）必须能被直接打开：init 补齐新列，历史行取 DEFAULT 0，
// 且补列后仍可继续写入。这是“向后兼容旧 SQLite 库”的回归点。
func TestSQLiteStore_UpgradesLegacySchema(t *testing.T) {
	dsn := filepath.Join(t.TempDir(), "legacy-ledger.db")

	legacy, err := sql.Open("sqlite3", dsn)
	require.NoError(t, err)
	_, err = legacy.Exec(`
		CREATE TABLE token_usage_history (
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
		)
	`)
	require.NoError(t, err)
	_, err = legacy.Exec(`
		INSERT INTO token_usage_history (
			id, request_id, input_tokens, output_tokens, total_tokens, success, created_at
		) VALUES ('legacy-1', 'req-legacy', 10, 5, 15, 1, ?)
	`, time.Now().UTC().Add(-time.Hour).Format(time.RFC3339Nano))
	require.NoError(t, err)
	require.NoError(t, legacy.Close())

	store, err := NewSQLiteStore(&Config{Driver: "sqlite", DSN: dsn})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()) })

	records, err := store.GetSince(time.Time{}, 10)
	require.NoError(t, err)
	require.Len(t, records, 1)
	require.Equal(t, "req-legacy", records[0].RequestID)
	require.Equal(t, 15, records[0].TotalTokens)
	require.Zero(t, records[0].IndexLookupCount, "补列后历史行的度量字段必须是 0")

	require.NoError(t, store.Create(&entity.TokenUsageHistory{
		RequestID:         "req-after-upgrade",
		IndexLookupCount:  4,
		IndexHit:          3,
		ToolCallsPerTask:  6,
		RepeatedReadCount: 1,
		CreatedAt:         entity.Time(time.Now().UTC()),
	}))

	after, err := store.GetSince(time.Now().UTC().Add(-time.Minute), 10)
	require.NoError(t, err)
	require.Len(t, after, 1)
	require.Equal(t, 4, after[0].IndexLookupCount)
	require.Equal(t, 3, after[0].IndexHit)
}
