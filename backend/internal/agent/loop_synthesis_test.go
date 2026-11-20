package agent

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wwsheng009/ai-agent-runtime/internal/events"
	"github.com/wwsheng009/ai-agent-runtime/internal/llm"
	"github.com/wwsheng009/ai-agent-runtime/internal/subagentbatch"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolbroker"
	runtimetypes "github.com/wwsheng009/ai-agent-runtime/internal/types"
)

type synthesisMarkerFailStore struct {
	subagentbatch.BatchStore
	err error
}

func (s synthesisMarkerFailStore) MarkTurnResultsConsumed(context.Context, string, string) error {
	return s.err
}

// Embedding the interface intentionally hides the concrete store's optional
// marker method.
type synthesisMarkerUnavailableStore struct {
	subagentbatch.BatchStore
}

func TestObserveChildWaitResultsRecordsDeliveryWithoutFinalization(t *testing.T) {
	wait := toolbroker.AgentWaitResult{
		Agents: []toolbroker.AgentStatusResult{{
			ID:                "child-a",
			SessionID:         "sess-a",
			CurrentTaskID:     "A",
			CurrentTaskStatus: "succeeded",
			Output:            "child result",
		}},
		PendingCount: 0,
		NextAction:   toolbroker.ContinueParentWorkNextAction,
		Obligations: []toolbroker.AgentWaitObligation{{
			ObligationID: "batch_1",
			SubjectKind:  "batch",
			SubjectID:    "batch_1",
			Terminal:     true,
		}},
	}
	toolbroker.AttachSynthesisPacket(&wait)

	runtime := llm.NewLLMRuntime(nil)
	ag := NewAgentWithLLM(&Config{Name: "child-results"}, nil, runtime)
	bus := events.NewBus()
	var received events.Event
	bus.Subscribe("tool_loop.child_results_received", func(event events.Event) { received = event })
	ag.SetEventBus(bus)
	loop := &ReActLoop{agent: ag}
	loop.observeChildWaitResults(context.Background(), "trace-1", "session-1", []toolExecutionResult{{
		Call:   runtimetypes.ToolCall{Name: toolbroker.ToolSubagentWait},
		Output: &wait,
	}})

	require.True(t, wait.ChildrenSettled)
	require.False(t, wait.FinalizationRequired)
	require.Equal(t, "batch_1", received.Payload["batch_id"])
	require.Equal(t, toolbroker.ContinueParentWorkNextAction, received.Payload["next_action"])
	require.Equal(t, "not_applicable", received.Payload["results_consumption_status"])
	require.Equal(t, false, received.Payload["results_consumed"])
}

func TestObserveChildWaitResultsMarksDurableResultsConsumed(t *testing.T) {
	ctx := context.Background()
	store, err := subagentbatch.NewSQLiteBatchStore(&subagentbatch.StoreConfig{
		Path: filepath.Join(t.TempDir(), "synthesis-results.db"),
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })

	require.NoError(t, store.ParkTurnSuspension(ctx, &subagentbatch.TurnSuspension{
		TurnID:        "turn-synthesis",
		SessionID:     "session-synthesis",
		ObligationIDs: []string{"batch-synthesis"},
	}))

	runtime := llm.NewLLMRuntime(nil)
	ag := NewAgentWithLLM(&Config{Name: "synthesis-results"}, nil, runtime)
	ag.SetSubagentBatchCoordinator(NewSubagentBatchCoordinator(SubagentBatchCoordinatorConfig{Store: store}))
	loop := &ReActLoop{agent: ag, turnID: "turn-synthesis"}

	wait := toolbroker.AgentWaitResult{
		Agents: []toolbroker.AgentStatusResult{{
			ID:                "child-a",
			SessionID:         "sess-a",
			CurrentTaskID:     "A",
			CurrentTaskStatus: "succeeded",
		}},
		PendingCount: 0,
		Obligations: []toolbroker.AgentWaitObligation{{
			ObligationID: "batch-synthesis",
			SubjectKind:  "batch",
			SubjectID:    "batch-synthesis",
			Terminal:     true,
		}},
	}
	toolbroker.AttachSynthesisPacket(&wait)

	loop.observeChildWaitResults(ctx, "trace-1", "session-synthesis", []toolExecutionResult{{
		Call:   runtimetypes.ToolCall{Name: toolbroker.ToolSubagentWait},
		Output: &wait,
	}})

	record, ok, err := store.GetTurnSuspension(ctx, "session-synthesis", "turn-synthesis")
	require.NoError(t, err)
	require.True(t, ok)
	require.True(t, record.ResultsConsumed,
		"the durable gate must record that subagent_wait delivered the child results")
}

func TestObserveChildWaitResultsMarkerFailureDoesNotReportConsumption(t *testing.T) {
	ctx := context.Background()
	baseStore, err := subagentbatch.NewSQLiteBatchStore(&subagentbatch.StoreConfig{
		Path: filepath.Join(t.TempDir(), "synthesis-marker-failure.db"),
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = baseStore.Close() })
	store := synthesisMarkerFailStore{BatchStore: baseStore, err: errors.New("marker write failed")}
	require.NoError(t, store.ParkTurnSuspension(ctx, &subagentbatch.TurnSuspension{
		TurnID:        "turn-marker-failure",
		SessionID:     "session-marker-failure",
		ObligationIDs: []string{"batch-marker-failure"},
	}))

	ag := NewAgentWithLLM(&Config{Name: "synthesis-marker-failure"}, nil, llm.NewLLMRuntime(nil))
	ag.SetSubagentBatchCoordinator(NewSubagentBatchCoordinator(SubagentBatchCoordinatorConfig{Store: store}))
	loop := &ReActLoop{agent: ag, turnID: "turn-marker-failure"}
	bus := events.NewBus()
	var received events.Event
	var failed events.Event
	bus.Subscribe("tool_loop.child_results_received", func(event events.Event) { received = event })
	bus.Subscribe("tool_loop.child_results_consumption_failed", func(event events.Event) { failed = event })
	ag.SetEventBus(bus)

	wait := toolbroker.AgentWaitResult{
		PendingCount: 0,
		Agents: []toolbroker.AgentStatusResult{{
			ID:                "child-a",
			CurrentTaskID:     "task-marker-failure",
			CurrentTaskStatus: "failed",
			Output:            "child failed",
		}},
		Obligations: []toolbroker.AgentWaitObligation{{
			ObligationID: "batch-marker-failure",
			SubjectKind:  "batch",
			SubjectID:    "batch-marker-failure",
			Terminal:     true,
		}},
	}
	toolbroker.AttachSynthesisPacket(&wait)
	err = loop.observeChildWaitResults(ctx, "trace-failure", "session-marker-failure", []toolExecutionResult{{
		Call:   runtimetypes.ToolCall{Name: toolbroker.ToolSubagentWait},
		Output: &wait,
	}})
	require.ErrorContains(t, err, "marker write failed")
	require.Equal(t, "failed", received.Payload["results_consumption_status"])
	require.Equal(t, false, received.Payload["results_consumed"])
	require.Equal(t, "failed", failed.Payload["results_consumption_status"])
	require.Contains(t, failed.Payload["results_consumption_error"], "marker write failed")

	record, ok, err := baseStore.GetTurnSuspension(ctx, "session-marker-failure", "turn-marker-failure")
	require.NoError(t, err)
	require.True(t, ok)
	require.False(t, record.ResultsConsumed, "a failed marker must leave the durable gate closed")
}

func TestMarkTurnResultsConsumedReportsMissingDurableCapability(t *testing.T) {
	baseStore, err := subagentbatch.NewSQLiteBatchStore(&subagentbatch.StoreConfig{
		Path: filepath.Join(t.TempDir(), "synthesis-marker-unavailable.db"),
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = baseStore.Close() })
	store := synthesisMarkerUnavailableStore{BatchStore: baseStore}
	ag := NewAgentWithLLM(&Config{Name: "synthesis-marker-unavailable"}, nil, llm.NewLLMRuntime(nil))
	ag.SetSubagentBatchCoordinator(NewSubagentBatchCoordinator(SubagentBatchCoordinatorConfig{Store: store}))
	loop := &ReActLoop{agent: ag, turnID: "turn-marker-unavailable"}

	status, err := loop.markTurnResultsConsumed(context.Background(), "session-marker-unavailable")
	require.Equal(t, turnResultsConsumptionUnavailable, status)
	require.ErrorContains(t, err, "does not support")
}

func TestSynthesisWaitResultAcceptsJSONString(t *testing.T) {
	wait := toolbroker.AgentWaitResult{
		Agents: []toolbroker.AgentStatusResult{{
			ID:                "child-a",
			SessionID:         "sess-a",
			CurrentTaskID:     "A",
			CurrentTaskStatus: "succeeded",
			Output:            "child result",
		}},
		PendingCount: 0,
		NextAction:   toolbroker.ContinueParentWorkNextAction,
		Obligations: []toolbroker.AgentWaitObligation{{
			ObligationID: "batch_1",
			SubjectKind:  "batch",
			Terminal:     true,
		}},
	}
	toolbroker.AttachSynthesisPacket(&wait)
	encoded, err := json.Marshal(wait)
	require.NoError(t, err)

	decoded, ok := synthesisWaitResult(string(encoded))
	require.True(t, ok)
	require.NotNil(t, decoded)
	require.Equal(t, "batch_1", decoded.Synthesis.BatchID)
}

func TestObserveChildWaitResultsIgnoresPending(t *testing.T) {
	wait := toolbroker.AgentWaitResult{
		Agents: []toolbroker.AgentStatusResult{{
			ID:                "child-a",
			SessionID:         "sess-a",
			CurrentTaskID:     "A",
			CurrentTaskStatus: "running",
		}},
		PendingCount: 1,
		NextAction:   "continue_wait",
		Obligations: []toolbroker.AgentWaitObligation{{
			ObligationID: "batch_1",
			SubjectKind:  "batch",
			SubjectID:    "batch_1",
		}},
	}
	toolbroker.AttachSynthesisPacket(&wait)
	encoded, err := json.Marshal(wait)
	require.NoError(t, err)

	runtime := llm.NewLLMRuntime(nil)
	ag := NewAgentWithLLM(&Config{Name: "child-results"}, nil, runtime)
	bus := events.NewBus()
	received := false
	bus.Subscribe("tool_loop.child_results_received", func(events.Event) { received = true })
	ag.SetEventBus(bus)
	loop := &ReActLoop{agent: ag}
	loop.observeChildWaitResults(context.Background(), "trace-1", "session-1", []toolExecutionResult{{
		Call:   runtimetypes.ToolCall{Name: toolbroker.ToolSubagentWait},
		Output: string(encoded),
	}})

	require.False(t, received)
	require.False(t, wait.ChildrenSettled)
	require.False(t, wait.FinalizationRequired)
}
