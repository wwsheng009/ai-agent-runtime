package factledger

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/wwsheng009/ai-agent-runtime/internal/artifact"
)

func TestLedgerSupersedesWithoutDeletingHistory(t *testing.T) {
	store, err := artifact.NewStore(&artifact.StoreConfig{Path: t.TempDir() + "/facts.sqlite"})
	require.NoError(t, err)
	defer store.Close()

	ledger := New(store)
	first, err := ledger.Append(context.Background(), Fact{
		FactID: "fact-old", SessionID: "session-1", GoalID: "goal-1", Scope: ScopeGoal,
		Kind: "decision", Subject: "runtime", Predicate: "port", Value: 8101,
		SourceEventID: "evt-1", EvidenceRefs: []string{"artifact:health-old"}, Confidence: 0.8,
	})
	require.NoError(t, err)
	_, err = ledger.Append(context.Background(), Fact{
		FactID: "fact-new", SessionID: "session-1", GoalID: "goal-1", Scope: ScopeGoal,
		Kind: "decision", Subject: "runtime", Predicate: "port", Value: 8201,
		SourceEventID: "evt-2", EvidenceRefs: []string{"artifact:health-new"}, Confidence: 1,
		Supersedes: first.FactID,
	})
	require.NoError(t, err)

	history, err := ledger.ListHistory(context.Background(), Query{SessionID: "session-1", GoalID: "goal-1"})
	require.NoError(t, err)
	require.Len(t, history, 2)
	active, err := ledger.ListActive(context.Background(), Query{SessionID: "session-1", GoalID: "goal-1"})
	require.NoError(t, err)
	require.Len(t, active, 1)
	require.Equal(t, "fact-new", active[0].FactID)
}

func TestLedgerScopesGoalsAndPersistsInvalidation(t *testing.T) {
	store, err := artifact.NewStore(&artifact.StoreConfig{Path: t.TempDir() + "/facts.sqlite"})
	require.NoError(t, err)
	defer store.Close()
	ledger := New(store)

	for _, fact := range []Fact{
		{FactID: "session", SessionID: "s", Scope: ScopeSession, Kind: "constraint", Subject: "user", Predicate: "language", Value: "zh", SourceEventID: "e1", EvidenceRefs: []string{"event:e1"}},
		{FactID: "goal-a", SessionID: "s", GoalID: "a", Scope: ScopeGoal, Kind: "test_result", Subject: "go", Predicate: "passed", Value: true, SourceEventID: "e2", EvidenceRefs: []string{"test:go"}},
		{FactID: "goal-b", SessionID: "s", GoalID: "b", Scope: ScopeGoal, Kind: "test_result", Subject: "web", Predicate: "passed", Value: true, SourceEventID: "e3", EvidenceRefs: []string{"test:web"}},
	} {
		_, err = ledger.Append(context.Background(), fact)
		require.NoError(t, err)
	}

	active, err := ledger.ListActive(context.Background(), Query{SessionID: "s", GoalID: "a"})
	require.NoError(t, err)
	require.Len(t, active, 2)
	require.NoError(t, ledger.Invalidate(context.Background(), "s", "a", "goal-a", "evt-correction", time.Now()))
	active, err = ledger.ListActive(context.Background(), Query{SessionID: "s", GoalID: "a"})
	require.NoError(t, err)
	require.Len(t, active, 1)
	require.Equal(t, "session", active[0].FactID)
}
