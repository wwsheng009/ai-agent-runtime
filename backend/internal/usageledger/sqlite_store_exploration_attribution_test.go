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

// explorationAttributionColumns 是 ADR-0003 §4.1 冻结的列集（顺序无关）。
var explorationAttributionColumns = []string{
	"id", "session_id", "turn_id", "request_id", "tool", "query_hash", "project_id",
	"baseline_n", "candidate_n", "overlap_n", "baseline_tokens", "candidate_tokens",
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
