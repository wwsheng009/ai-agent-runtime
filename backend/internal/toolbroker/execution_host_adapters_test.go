package toolbroker

import (
	"context"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/internal/supervision"
	"github.com/stretchr/testify/require"
)

func TestAgentSessionRunInterrupterClosesLiveSession(t *testing.T) {
	controller := &fakeAgentSessionController{}
	interrupter := AgentSessionRunInterrupter{Controller: controller}

	err := interrupter.InterruptRun(context.Background(), supervision.ExecutionRun{
		RunID:     "run_1",
		SessionID: "child-live",
		AgentID:   "child-live",
	})
	require.NoError(t, err)
	require.Equal(t, "child-live", controller.lastClose)
}

func TestAgentSessionRunInterrupterFallsBackToAgentID(t *testing.T) {
	controller := &fakeAgentSessionController{}
	interrupter := AgentSessionRunInterrupter{Controller: controller}

	err := interrupter.InterruptRun(context.Background(), supervision.ExecutionRun{
		RunID:   "run_2",
		AgentID: "child-by-id",
	})
	require.NoError(t, err)
	require.Equal(t, "child-by-id", controller.lastClose)
}

func TestAgentSessionRunInterrupterNoopWithoutSessionOrController(t *testing.T) {
	require.NoError(t, AgentSessionRunInterrupter{}.InterruptRun(context.Background(), supervision.ExecutionRun{RunID: "run_3"}))

	controller := &fakeAgentSessionController{}
	interrupter := AgentSessionRunInterrupter{Controller: controller}
	require.NoError(t, interrupter.InterruptRun(context.Background(), supervision.ExecutionRun{RunID: "run_4"}))
	require.Empty(t, controller.lastClose)
}

func TestCompletionDispatchFuncPassthrough(t *testing.T) {
	entry := supervision.CompletionOutboxEntry{
		OutboxID:        "outbox_1",
		RunID:           "run_5",
		SessionID:       "child-1",
		ParentSessionID: "parent-1",
		Status:          supervision.RunStatusSucceeded,
		IdempotencyKey:  "subagent_completion:run_5:1",
	}
	var got supervision.CompletionOutboxEntry
	dispatcher := CompletionDispatchFunc(func(ctx context.Context, e supervision.CompletionOutboxEntry) (int64, error) {
		got = e
		return 42, nil
	})
	seq, err := dispatcher.DispatchCompletion(context.Background(), entry)
	require.NoError(t, err)
	require.Equal(t, int64(42), seq)
	require.Equal(t, entry, got)
}

func TestCompletionDispatchFuncNilIsNoop(t *testing.T) {
	var dispatcher CompletionDispatchFunc
	seq, err := dispatcher.DispatchCompletion(context.Background(), supervision.CompletionOutboxEntry{OutboxID: "outbox_2"})
	require.NoError(t, err)
	require.Equal(t, int64(0), seq)
}
