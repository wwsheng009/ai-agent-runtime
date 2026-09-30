package usageledger

import (
	"context"
	"database/sql"
	"path/filepath"
	"sort"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/wwsheng009/ai-agent-runtime/internal/model/entity"
	"github.com/wwsheng009/ai-agent-runtime/internal/sqliteutil"
)

// explorationAttributionColumns 是 ADR-0003 §4.1 冻结的列集 + ADR-0008 §4
// 追加的两个 file-level 列（顺序无关）。
var explorationAttributionColumns = []string{
	"id", "session_id", "turn_id", "request_id", "tool", "query_hash", "project_id",
	"baseline_n", "baseline_files_n", "candidate_n", "overlap_n", "overlap_files_n",
	"baseline_tokens", "candidate_tokens",
	"coverage", "economy", "usable", "source", "knowledge_mode", "created_at",
}

// Phase 0 交付 7（ADR-0003 §4.1）：init() 必须建出 exploration_attribution 与两个索引，
// 且该表可被 sqliteutil.OpenFileCtx 直接打开（无 lock / PRAGMA 报错）。
func TestSQLiteStore_CreatesExplorationAttributionTable(t *testing.T) {
	dsn := filepath.Join(t.TempDir(), "ledger.db")

	store, err := NewSQLiteStore(&Config{Driver: "sqlite", DSN: dsn})
	require.NoError(t, err)
	require.NoError(t, store.Close())

	db, err := sqliteutil.OpenFileCtx(context.Background(), dsn, false)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })

	require.ElementsMatch(t, explorationAttributionColumns, tableColumns(t, db, "exploration_attribution"))

	for _, index := range []string{
		"idx_exploration_attribution_created_at",
		"idx_exploration_attribution_tool_time",
	} {
		require.True(t, indexExists(t, db, index), "缺少索引 %s", index)
	}

	// 只建表不产生数据：Phase 0（mode=off）下该表必须为空。
	var rows int
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM exploration_attribution`).Scan(&rows))
	require.Zero(t, rows)
}

// 重复 init 必须幂等：同一 DSN 再开一次不报错，且 sqlite_master 中
// exploration_attribution 只登记一次（ADR-0003 §8 验收门槛）。
func TestSQLiteStore_ExplorationAttributionInitIsIdempotent(t *testing.T) {
	dsn := filepath.Join(t.TempDir(), "ledger.db")

	for i := 0; i < 3; i++ {
		store, err := NewSQLiteStore(&Config{Driver: "sqlite", DSN: dsn})
		require.NoError(t, err)
		require.NoError(t, store.Close())
	}

	db, err := sqliteutil.OpenFileCtx(context.Background(), dsn, false)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })

	var count int
	require.NoError(t, db.QueryRow(
		`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'exploration_attribution'`,
	).Scan(&count))
	require.Equal(t, 1, count)
}

// D3（ADR-0003 §4.1 修订版）：新增归因表不得改变 token_usage_history 的既有聚合结果。
func TestSQLiteStore_ExplorationAttributionDoesNotPolluteLedgerAggregates(t *testing.T) {
	dsn := filepath.Join(t.TempDir(), "ledger.db")

	store, err := NewSQLiteStore(&Config{Driver: "sqlite", DSN: dsn})
	require.NoError(t, err)
	require.NoError(t, store.Create(&entity.TokenUsageHistory{
		RequestID:    "req-d3",
		InputTokens:  10,
		OutputTokens: 5,
		TotalTokens:  15,
		Success:      true,
		CreatedAt:    entity.Time(time.Now().UTC()),
	}))
	require.NoError(t, store.Close())

	before := ledgerAggregates(t, dsn)

	// 再次 init（追加语句走 IF NOT EXISTS 分支）后聚合必须逐字节一致。
	again, err := NewSQLiteStore(&Config{Driver: "sqlite", DSN: dsn})
	require.NoError(t, err)
	require.NoError(t, again.Close())

	require.Equal(t, before, ledgerAggregates(t, dsn))
}

// ledgerAggregate 是 token_usage_history 的既有聚合快照（行数 + token 求和）。
type ledgerAggregate struct {
	Rows         int
	TotalTokens  int
	InputTokens  int
	OutputTokens int
}

// ledgerAggregates 返回 token_usage_history 的既有聚合快照。
func ledgerAggregates(t *testing.T, dsn string) ledgerAggregate {
	t.Helper()

	db, err := sql.Open("sqlite3", dsn)
	require.NoError(t, err)
	defer db.Close()

	var snapshot ledgerAggregate
	require.NoError(t, db.QueryRow(`
		SELECT COUNT(*), COALESCE(SUM(total_tokens), 0), COALESCE(SUM(input_tokens), 0), COALESCE(SUM(output_tokens), 0)
		FROM token_usage_history
	`).Scan(&snapshot.Rows, &snapshot.TotalTokens, &snapshot.InputTokens, &snapshot.OutputTokens))
	return snapshot
}

// tableColumns 返回指定表的列名（排序后）。
func tableColumns(t *testing.T, db *sql.DB, table string) []string {
	t.Helper()

	rows, err := db.Query(`SELECT name FROM pragma_table_info(?)`, table)
	require.NoError(t, err)
	defer rows.Close()

	columns := make([]string, 0, len(explorationAttributionColumns))
	for rows.Next() {
		var name string
		require.NoError(t, rows.Scan(&name))
		columns = append(columns, name)
	}
	require.NoError(t, rows.Err())
	sort.Strings(columns)
	return columns
}

// indexExists 报告指定索引是否已登记。
func indexExists(t *testing.T, db *sql.DB, name string) bool {
	t.Helper()

	var count int
	require.NoError(t, db.QueryRow(
		`SELECT COUNT(*) FROM sqlite_master WHERE type = 'index' AND name = ?`, name,
	).Scan(&count))
	return count > 0
}

// ADR-0008 §4 / D4：老库（无 file-level 列）打开时必须 additive 补齐两列，
// 历史行取 DEFAULT 0 且行级列语义不变；随后新行可写入并原样读回。
func TestSQLiteStore_ExplorationAttributionFileLevelAdditiveMigration(t *testing.T) {
	dsn := filepath.Join(t.TempDir(), "legacy-ledger.db")

	// 1) 手工构造 ADR-0003 时代的旧表（无 ADR-0008 两列）并写入一行历史数据。
	legacy, err := sql.Open("sqlite3", dsn)
	require.NoError(t, err)
	_, err = legacy.Exec(`
		CREATE TABLE exploration_attribution (
			id TEXT PRIMARY KEY,
			session_id TEXT,
			turn_id TEXT,
			request_id TEXT,
			tool TEXT NOT NULL,
			query_hash TEXT,
			project_id TEXT,
			baseline_n INTEGER NOT NULL DEFAULT 0,
			candidate_n INTEGER NOT NULL DEFAULT 0,
			overlap_n INTEGER NOT NULL DEFAULT 0,
			baseline_tokens INTEGER NOT NULL DEFAULT 0,
			candidate_tokens INTEGER NOT NULL DEFAULT 0,
			coverage REAL,
			economy REAL,
			usable INTEGER NOT NULL DEFAULT 0,
			source TEXT,
			knowledge_mode TEXT NOT NULL,
			created_at TEXT NOT NULL
		)`)
	require.NoError(t, err)
	_, err = legacy.Exec(`INSERT INTO exploration_attribution (
			id, tool, baseline_n, candidate_n, overlap_n, baseline_tokens, candidate_tokens,
			coverage, economy, usable, knowledge_mode, created_at
		) VALUES ('legacy-1', 'grep', 3, 1, 1, 30, 10, 0.5, 0.5, 0, 'shadow', ?)`,
		time.Date(2026, 9, 28, 10, 0, 0, 0, time.UTC).Format(time.RFC3339Nano))
	require.NoError(t, err)
	require.NoError(t, legacy.Close())

	// 2) 用生产 store 打开：additive 迁移必须补齐两列。
	store, err := NewSQLiteStore(&Config{Driver: "sqlite", DSN: dsn})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()) })

	// 3) 历史行：file-level 列取 0（不参与 file-level M1），行级字段原样。
	ctx := context.Background()
	rows, err := store.ListExplorationAttribution(ctx, time.Time{}, 10)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Equal(t, "legacy-1", rows[0].ID)
	require.Zero(t, rows[0].BaselineFilesN, "历史行不参与 file-level M1")
	require.Zero(t, rows[0].OverlapFilesN)
	require.Equal(t, 3, rows[0].BaselineN, "历史行行级列语义不变")

	// 4) 新行可写入新列并读回。
	require.NoError(t, store.AppendExplorationAttribution(ctx, &entity.ExplorationAttribution{
		ID: "new-1", Tool: "grep", KnowledgeMode: "shadow",
		BaselineN: 2, BaselineFilesN: 2, CandidateN: 1, OverlapN: 1, OverlapFilesN: 1,
		BaselineTokens: 20, CandidateTokens: 5,
		CreatedAt: time.Date(2026, 9, 29, 10, 0, 0, 0, time.UTC),
	}))
	rows, err = store.ListExplorationAttribution(ctx, time.Time{}, 10)
	require.NoError(t, err)
	require.Len(t, rows, 2)
	require.Equal(t, 2, rows[1].BaselineFilesN)
	require.Equal(t, 1, rows[1].OverlapFilesN)

	// 5) 迁移幂等：列集合与全新库定义一致。
	require.ElementsMatch(t, explorationAttributionColumns, tableColumns(t, store.db, "exploration_attribution"))
}
