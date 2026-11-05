package agent

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wwsheng009/ai-agent-runtime/internal/llm"
	"github.com/wwsheng009/ai-agent-runtime/internal/subagentbatch"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolbroker"
	runtimetypes "github.com/wwsheng009/ai-agent-runtime/internal/types"
)

func TestObserveSynthesisWaitResultsEntersSynthesis(t *testing.T) {
	wait := toolbroker.AgentWaitResult{
		Agents: []toolbroker.AgentStatusResult{{
			ID:                "child-a",
			SessionID:         "sess-a",
			CurrentTaskID:     "A",
			CurrentTaskStatus: "succeeded",
			Output:            "child result",
		}},
		PendingCount: 0,
		NextAction:   "finalize",
		Obligations: []toolbroker.AgentWaitObligation{{
			ObligationID: "batch_1",
			SubjectKind:  "batch",
			SubjectID:    "batch_1",
			Terminal:     true,
		}},
	}
	toolbroker.AttachSynthesisPacket(&wait)

	loop := &ReActLoop{}
	loop.observeSynthesisWaitResults(context.Background(), "trace-1", "session-1", []toolExecutionResult{{
		Call:   runtimetypes.ToolCall{Name: toolbroker.ToolSubagentWait},
		Output: &wait,
	}})

	require.True(t, loop.synthesisRequired)
	require.NotNil(t, loop.synthesisPacket)
	require.Equal(t, "batch_1", loop.synthesisPacket.BatchID)
	require.False(t, loop.synthesisBudgetExhausted)
}

func TestObserveSynthesisWaitResultsMarksDurableResultsConsumed(t *testing.T) {
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

	loop.observeSynthesisWaitResults(ctx, "trace-1", "session-synthesis", []toolExecutionResult{{
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
		NextAction:   "finalize",
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

func TestObserveSynthesisWaitResultsIgnoresPending(t *testing.T) {
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

	loop := &ReActLoop{}
	loop.observeSynthesisWaitResults(context.Background(), "trace-1", "session-1", []toolExecutionResult{{
		Call:   runtimetypes.ToolCall{Name: toolbroker.ToolSubagentWait},
		Output: string(encoded),
	}})

	require.False(t, loop.synthesisRequired)
	require.Nil(t, loop.synthesisPacket)
}

func TestSynthesisFallbackOutputIncludesPacket(t *testing.T) {
	loop := &ReActLoop{synthesisPacket: &toolbroker.SynthesisPacket{
		BatchID:    "batch_1",
		TaskCount:  1,
		NextAction: "synthesize_and_finalize",
	}}
	output := loop.synthesisFallbackOutput()
	require.True(t, strings.Contains(output, "synthesis_budget_exhausted"))
	require.True(t, strings.Contains(output, "batch_1"))
	require.True(t, strings.Contains(output, "synthesize_and_finalize"))
}

func TestDenySynthesisToolCallsDeniesAndExhaustsBudget(t *testing.T) {
	runtime := llm.NewLLMRuntime(nil)
	agent := NewAgentWithLLM(&Config{Name: "synthesis-test"}, nil, runtime)
	loop := NewReActLoop(agent, runtime, &LoopReActConfig{})
	loop.synthesisRequired = true
	calls := []runtimetypes.ToolCall{{
		ID:   "call-1",
		Name: "shell",
		Args: map[string]interface{}{"command": "echo hi"},
	}}

	results := loop.denySynthesisToolCalls(context.Background(), "trace-1", "session-1", 1, calls)
	require.Len(t, results, 1)
	require.Contains(t, results[0].Error, "synthesis_required")
	require.False(t, loop.synthesisBudgetExhausted)

	_ = loop.denySynthesisToolCalls(context.Background(), "trace-1", "session-1", 2, calls)
	require.True(t, loop.synthesisBudgetExhausted)
}
