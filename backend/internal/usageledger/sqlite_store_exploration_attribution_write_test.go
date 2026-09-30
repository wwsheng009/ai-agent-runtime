package usageledger

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/wwsheng009/ai-agent-runtime/internal/model/entity"
)

// Phase 1 交付 4：AppendExplorationAttribution 必须可写入并原样读回，
// 且不污染 token_usage_history（ADR-0003 §4.1 D3 第 1 条）。
func TestSQLiteStore_AppendExplorationAttributionRoundTrip(t *testing.T) {
	store := newLedgerProfileTestStore(t)
	ctx := context.Background()

	var before int
	require.NoError(t, store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM token_usage_history`).Scan(&before))

	coverage, economy := 0.5, 0.75
	rec := &entity.ExplorationAttribution{
		ID: "attr-1", SessionID: "sess-1", TurnID: "turn-1", RequestID: "req-1",
		Tool: "grep", QueryHash: strings.Repeat("a", 64), ProjectID: "proj",
		BaselineN: 4, BaselineFilesN: 2, CandidateN: 2, OverlapN: 2, OverlapFilesN: 2,
		BaselineTokens: 100, CandidateTokens: 75,
		Coverage: &coverage, Economy: &economy, Usable: true,
		Source: "heuristic", KnowledgeMode: "shadow", CreatedAt: time.Now().UTC(),
	}
	require.NoError(t, store.AppendExplorationAttribution(ctx, rec))

	var (
		gotTool, gotHash, gotMode             string
		gotBaselineN, gotBaselineFilesN       int
		gotCandidateN, gotOverlapFilesN       int
		gotOverlapN, gotUsable                int
		gotBaselineTokens, gotCandidateTokens int
		gotCoverage, gotEconomy               float64
	)
	require.NoError(t, store.db.QueryRowContext(ctx, `
		SELECT tool, query_hash, baseline_n, baseline_files_n, candidate_n, overlap_n,
		       overlap_files_n, baseline_tokens, candidate_tokens, coverage, economy,
		       usable, knowledge_mode
		FROM exploration_attribution WHERE id = ?`, "attr-1").Scan(
		&gotTool, &gotHash, &gotBaselineN, &gotBaselineFilesN, &gotCandidateN, &gotOverlapN,
		&gotOverlapFilesN, &gotBaselineTokens, &gotCandidateTokens, &gotCoverage, &gotEconomy,
		&gotUsable, &gotMode))
	require.Equal(t, "grep", gotTool)
	require.Equal(t, strings.Repeat("a", 64), gotHash)
	require.Equal(t, 4, gotBaselineN)
	require.Equal(t, 2, gotBaselineFilesN, "ADR-0008 §4：file-level 分母落库")
	require.Equal(t, 2, gotCandidateN)
	require.Equal(t, 2, gotOverlapN)
	require.Equal(t, 2, gotOverlapFilesN, "ADR-0008 §4：file-level 分子落库")
	require.Equal(t, 100, gotBaselineTokens)
	require.Equal(t, 75, gotCandidateTokens)
	require.InDelta(t, 0.5, gotCoverage, 1e-9)
	require.InDelta(t, 0.75, gotEconomy, 1e-9)
	require.Equal(t, 1, gotUsable)
	require.Equal(t, "shadow", gotMode)

	var after int
	require.NoError(t, store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM token_usage_history`).Scan(&after))
	require.Equal(t, before, after, "exploration_attribution 不得写入 token_usage_history")
}

// 零结果调用（baseline_n = 0）：coverage / economy 落 NULL（ADR-0003 §4.2）。
func TestSQLiteStore_AppendExplorationAttributionZeroBaselineNulls(t *testing.T) {
	store := newLedgerProfileTestStore(t)
	ctx := context.Background()

	rec := &entity.ExplorationAttribution{
		ID: "attr-zero", Tool: "grep", KnowledgeMode: "shadow", CreatedAt: time.Now().UTC(),
	}
	require.NoError(t, store.AppendExplorationAttribution(ctx, rec))

	var coverage, economy sql.NullFloat64
	require.NoError(t, store.db.QueryRowContext(ctx, `
		SELECT coverage, economy FROM exploration_attribution WHERE id = ?`, "attr-zero").
		Scan(&coverage, &economy))
	require.False(t, coverage.Valid, "baseline_n = 0 时 coverage 必须为 NULL")
	require.False(t, economy.Valid, "baseline_tokens = 0 时 economy 必须为 NULL")
}
