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

func TestBuildKeepsConversationPrefixStableWhenFactLedgerInjected(t *testing.T) {
	store, err := artifact.NewStore(&artifact.StoreConfig{Path: t.TempDir() + "/prefix.sqlite"})
	require.NoError(t, err)
	defer store.Close()
	ledger := factledger.New(store)
	_, err = ledger.Append(context.Background(), factledger.Fact{
		FactID: "session-fact", SessionID: "s", Scope: factledger.ScopeSession,
		Kind: "constraint", Subject: "user", Predicate: "requires", Value: "Chinese output",
		SourceEventID: "event:u1", EvidenceRefs: []string{"event:u1"},
	})
	require.NoError(t, err)

	history := []types.Message{
		*types.NewSystemMessage("system prompt"),
		*types.NewUserMessage("first user turn"),
		*types.NewAssistantMessage("first assistant turn"),
		// Active turn must end on user input; otherwise fact injection is suppressed
		// by activeUserTurnHasReplay once an assistant reply already exists.
		*types.NewUserMessage("continue with facts"),
	}
	manager := NewManager(DefaultBudget(), store)

	withoutFacts := manager.Build(context.Background(), BuildInput{
		SessionID: "", // no session id => no fact ledger
		Goal:      "continue",
		History:   history,
	})
	withFacts := manager.Build(context.Background(), BuildInput{
		SessionID: "s",
		Goal:      "continue",
		History:   history,
	})
	require.Equal(t, true, withFacts.Metadata["fact_ledger_injected"])
	require.NotEqual(t, true, withoutFacts.Metadata["fact_ledger_injected"])

	// History prefix must be byte-identical; ledger may only append after it.
	require.GreaterOrEqual(t, len(withFacts.Messages), len(withoutFacts.Messages))
	for i := range withoutFacts.Messages {
		require.Equal(t, withoutFacts.Messages[i].Role, withFacts.Messages[i].Role)
		require.Equal(t, withoutFacts.Messages[i].Content, withFacts.Messages[i].Content)
	}
	ledgerMsg := withFacts.Messages[len(withFacts.Messages)-1]
	require.Equal(t, "fact_ledger", ledgerMsg.Metadata.GetString("context_stage", ""))
	require.Contains(t, ledgerMsg.Content, "Chinese output")
}

func TestBuildDoesNotPrependFactLedgerBeforeHistory(t *testing.T) {
	store, err := artifact.NewStore(&artifact.StoreConfig{Path: t.TempDir() + "/order.sqlite"})
	require.NoError(t, err)
	defer store.Close()
	ledger := factledger.New(store)
	_, err = ledger.Append(context.Background(), factledger.Fact{
		FactID: "f1", SessionID: "s", Scope: factledger.ScopeSession,
		Kind: "execution", Subject: "grep", Predicate: "succeeded", Value: "ok",
		SourceEventID: "event:1", EvidenceRefs: []string{"event:1"},
	})
	require.NoError(t, err)

	manager := NewManager(DefaultBudget(), store)
	result := manager.Build(context.Background(), BuildInput{
		SessionID: "s",
		Goal:      "continue",
		History: []types.Message{
			*types.NewSystemMessage("system prompt"),
			*types.NewUserMessage("hello"),
		},
	})
	require.True(t, result.Metadata["fact_ledger_injected"].(bool))
	require.GreaterOrEqual(t, len(result.Messages), 3)
	require.Equal(t, "system", result.Messages[0].Role)
	require.Equal(t, "user", result.Messages[1].Role)
	require.Equal(t, "hello", result.Messages[1].Content)
	require.Equal(t, "fact_ledger", result.Messages[len(result.Messages)-1].Metadata.GetString("context_stage", ""))
}

func TestBuildRehydratesFactLedgerAfterMidTurnCompactHistory(t *testing.T) {
	store, err := artifact.NewStore(&artifact.StoreConfig{Path: t.TempDir() + "/rehydrate.sqlite"})
	require.NoError(t, err)
	defer store.Close()

	ledger := factledger.New(store)
	_, err = ledger.Append(context.Background(), factledger.Fact{
		FactID: "session-fact", SessionID: "s", Scope: factledger.ScopeSession,
		Kind: "constraint", Subject: "user", Predicate: "requires", Value: "Chinese output",
		SourceEventID: "event:u1", EvidenceRefs: []string{"event:u1"},
	})
	require.NoError(t, err)

	// Mid-turn compact leaves real user + tool replay + a trailing compaction
	// checkpoint. The checkpoint must not open a new turn window that hides
	// world-state rehydration.
	summary := types.NewUserMessage("Compacted context from earlier turns:\nRoot cause confirmed.")
	summary.Metadata["context_stage"] = "compaction"
	summary.Metadata["compact_phase"] = "mid_turn"
	history := []types.Message{
		*types.NewSystemMessage("system prompt"),
		*types.NewUserMessage("continue optimizing"),
		*types.NewAssistantMessage("inspecting"),
		*types.NewToolMessage("call-1", "tool output"),
		*summary,
	}

	manager := NewManager(DefaultBudget(), store)
	manager.Strategy.RecallMode = RecallModeDisabled
	manager.Strategy.WorkspaceMode = WorkspaceModeDisabled

	result := manager.Build(context.Background(), BuildInput{
		SessionID: "s",
		Goal:      "continue optimizing",
		History:   history,
	})
	require.True(t, result.Metadata["fact_ledger_injected"].(bool))

	// History prefix stays exact; fact ledger only appends after the compacted turn.
	require.GreaterOrEqual(t, len(result.Messages), len(history))
	require.Equal(t, history, result.Messages[:len(history)])
	ledgerMsg := result.Messages[len(result.Messages)-1]
	require.Equal(t, "fact_ledger", ledgerMsg.Metadata.GetString("context_stage", ""))
	require.True(t, ledgerMsg.Metadata.GetBool("context_snapshot", false))
	require.Contains(t, ledgerMsg.Content, "Chinese output")
}

func TestBuildSkipsFactLedgerWhenActiveTurnAlreadyHasSnapshot(t *testing.T) {
	store, err := artifact.NewStore(&artifact.StoreConfig{Path: t.TempDir() + "/skip-rehydrate.sqlite"})
	require.NoError(t, err)
	defer store.Close()

	ledger := factledger.New(store)
	_, err = ledger.Append(context.Background(), factledger.Fact{
		FactID: "session-fact", SessionID: "s", Scope: factledger.ScopeSession,
		Kind: "constraint", Subject: "user", Predicate: "requires", Value: "Chinese output",
		SourceEventID: "event:u1", EvidenceRefs: []string{"event:u1"},
	})
	require.NoError(t, err)

	frozen := types.NewAssistantMessage("Verified fact ledger (authoritative over compacted prose):\n- frozen only")
	frozen.Metadata["context_stage"] = "fact_ledger"
	frozen.Metadata["context_snapshot"] = true
	history := []types.Message{
		*types.NewSystemMessage("system prompt"),
		*types.NewUserMessage("continue"),
		*frozen,
		*types.NewAssistantMessage("calling tools"),
		*types.NewToolMessage("call-1", "tool output"),
	}

	manager := NewManager(DefaultBudget(), store)
	result := manager.Build(context.Background(), BuildInput{
		SessionID: "s",
		Goal:      "continue",
		History:   history,
	})
	require.Nil(t, result.Metadata["fact_ledger_injected"])
	require.Equal(t, history, result.Messages[:len(history)])

	factCount := 0
	for _, message := range result.Messages {
		if message.Metadata.GetString("context_stage", "") == "fact_ledger" {
			factCount++
			require.Contains(t, message.Content, "frozen only")
			require.NotContains(t, message.Content, "Chinese output")
		}
	}
	require.Equal(t, 1, factCount)
}
