package agent

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wwsheng009/ai-agent-runtime/internal/events"
	"github.com/wwsheng009/ai-agent-runtime/internal/llm"
	"github.com/wwsheng009/ai-agent-runtime/internal/subagentbatch"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolbroker"
	runtimetypes "github.com/wwsheng009/ai-agent-runtime/internal/types"
)

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
