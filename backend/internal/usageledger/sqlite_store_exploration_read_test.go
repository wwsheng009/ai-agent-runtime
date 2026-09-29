package usageledger

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/wwsheng009/ai-agent-runtime/internal/model/entity"
)

// TestSQLiteStore_ListExplorationAttribution 钉住 Phase1-shadow 复算所需的读口径：
// since 过滤、时间升序、limit、以及 NULL coverage/economy 的原样还原。
func TestSQLiteStore_ListExplorationAttribution(t *testing.T) {
	store := newLedgerProfileTestStore(t)
	ctx := context.Background()
	t0 := time.Date(2026, 9, 29, 10, 0, 0, 0, time.UTC)

	coverage, economy := 0.75, 0.9
	zero, quarter := 0.0, 0.25
	require.NoError(t, store.AppendExplorationAttribution(ctx, &entity.ExplorationAttribution{
		ID: "a1", Tool: "grep", ProjectID: "p", BaselineN: 4, CandidateN: 3, OverlapN: 3,
		BaselineTokens: 100, CandidateTokens: 90, Coverage: &coverage, Economy: &economy,
		Source: "heuristic", KnowledgeMode: "shadow", CreatedAt: t0,
	}))
	require.NoError(t, store.AppendExplorationAttribution(ctx, &entity.ExplorationAttribution{
		ID: "a2", Tool: "view", ProjectID: "p", BaselineN: 0, CandidateN: 0, OverlapN: 0,
		BaselineTokens: 10, CandidateTokens: 0, KnowledgeMode: "shadow", CreatedAt: t0.Add(time.Minute),
	}))
	require.NoError(t, store.AppendExplorationAttribution(ctx, &entity.ExplorationAttribution{
		ID: "a3", Tool: "grep", ProjectID: "p", BaselineN: 2, CandidateN: 1, OverlapN: 0,
		BaselineTokens: 20, CandidateTokens: 5, Coverage: &zero, Economy: &quarter,
		KnowledgeMode: "shadow", CreatedAt: t0.Add(2 * time.Minute),
	}))

	all, err := store.ListExplorationAttribution(ctx, t0.Add(-time.Second), 10)
	require.NoError(t, err)
	require.Len(t, all, 3)
	require.Equal(t, []string{"a1", "a2", "a3"}, []string{all[0].ID, all[1].ID, all[2].ID})
	require.NotNil(t, all[0].Coverage)
	require.InDelta(t, 0.75, *all[0].Coverage, 1e-9)
	require.NotNil(t, all[0].Economy)
	require.InDelta(t, 0.9, *all[0].Economy, 1e-9)
	require.True(t, all[0].CreatedAt.Equal(t0))
	// 零结果行必须保留，且 coverage/economy 还原为 NULL。
	require.Nil(t, all[1].Coverage)
	require.Nil(t, all[1].Economy)
	require.Equal(t, 0, all[1].BaselineN)
	require.False(t, all[1].Usable)

	// since 过滤：只看第二分钟之后。
	partial, err := store.ListExplorationAttribution(ctx, t0.Add(30*time.Second), 10)
	require.NoError(t, err)
	require.Len(t, partial, 2)
	require.Equal(t, "a2", partial[0].ID)

	// limit 生效。
	limited, err := store.ListExplorationAttribution(ctx, t0.Add(-time.Second), 1)
	require.NoError(t, err)
	require.Len(t, limited, 1)
	require.Equal(t, "a1", limited[0].ID)
}
