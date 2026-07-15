package contextmgr

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/wwsheng009/ai-agent-runtime/internal/artifact"
	"github.com/wwsheng009/ai-agent-runtime/internal/factledger"
	"github.com/wwsheng009/ai-agent-runtime/internal/types"
)

func TestBuildInjectsOnlyActiveGoalFactsWithEvidence(t *testing.T) {
	store, err := artifact.NewStore(&artifact.StoreConfig{Path: t.TempDir() + "/context.sqlite"})
	require.NoError(t, err)
	defer store.Close()
	ledger := factledger.New(store)
	for _, fact := range []factledger.Fact{
		{FactID: "session-fact", SessionID: "s", Scope: factledger.ScopeSession, Kind: "constraint", Subject: "user", Predicate: "requires", Value: "Chinese output", SourceEventID: "event:u1", EvidenceRefs: []string{"event:u1"}},
		{FactID: "goal-a", SessionID: "s", GoalID: "a", Scope: factledger.ScopeGoal, Kind: "test_result", Subject: "backend", Predicate: "passed", Value: true, SourceEventID: "event:t1", EvidenceRefs: []string{"test:go"}},
		{FactID: "goal-b", SessionID: "s", GoalID: "b", Scope: factledger.ScopeGoal, Kind: "test_result", Subject: "frontend", Predicate: "passed", Value: true, SourceEventID: "event:t2", EvidenceRefs: []string{"test:web"}},
	} {
		_, err = ledger.Append(context.Background(), fact)
		require.NoError(t, err)
	}

	manager := NewManager(DefaultBudget(), store)
	result := manager.Build(context.Background(), BuildInput{
		SessionID: "s", GoalID: "a", Goal: "continue", History: []types.Message{*types.NewUserMessage("continue")},
	})
	require.True(t, result.Metadata["fact_ledger_injected"].(bool))
	require.Equal(t, 2, result.Metadata["fact_count"])
	joined := ""
	for _, message := range result.Messages {
		joined += message.Content
	}
	require.Contains(t, joined, "Chinese output")
	require.Contains(t, joined, "test:go")
	require.NotContains(t, joined, "test:web")
}

func TestRecordObservationsPersistsEvidenceLinkedTestFact(t *testing.T) {
	store, err := artifact.NewStore(&artifact.StoreConfig{Path: t.TempDir() + "/observations.sqlite"})
	require.NoError(t, err)
	defer store.Close()
	manager := NewManager(DefaultBudget(), store)
	observation := types.NewObservation("step-1", "go_test")
	observation.WithOutput("ok package").WithMetric("artifact_refs", []string{"artifact:test-log"}).MarkSuccess()
	manager.RecordObservations(context.Background(), FactObservationInput{
		TraceID: "trace-1", SessionID: "s", GoalID: "g", Observations: []types.Observation{*observation},
	})

	facts, err := factledger.New(store).ListActive(context.Background(), factledger.Query{SessionID: "s", GoalID: "g"})
	require.NoError(t, err)
	require.Len(t, facts, 1)
	require.Equal(t, "test_result", facts[0].Kind)
	require.Equal(t, "succeeded", facts[0].Predicate)
	require.Equal(t, []string{"artifact:test-log"}, facts[0].EvidenceRefs)
}
