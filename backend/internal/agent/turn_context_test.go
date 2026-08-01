package agent

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
)

func TestReActLoopEmitRuntimeEventCarriesDurableTurnID(t *testing.T) {
	agent := &Agent{config: &Config{Name: "test-agent"}}
	bus := runtimeevents.NewBus()
	var received runtimeevents.Event
	bus.Subscribe("llm.request.started", func(event runtimeevents.Event) {
		received = event
	})
	agent.SetEventBus(bus)

	loop := &ReActLoop{agent: agent, turnID: "turn-123"}
	loop.emitRuntimeEvent("llm.request.started", "session-1", "", map[string]interface{}{
		"trace_id": "trace-1",
	})

	require.Equal(t, "turn-123", received.Payload["turn_id"])
	require.Equal(t, "trace-1", received.TraceID)
}

func TestRuntimeEventPayloadWithTurnIDPreservesPayload(t *testing.T) {
	payload := map[string]interface{}{"value": "kept"}
	got := runtimeEventPayloadWithTurnID(WithTurnID(context.Background(), "turn-456"), payload)

	require.Equal(t, "kept", payload["value"])
	require.Equal(t, "kept", got["value"])
	require.Equal(t, "turn-456", got["turn_id"])
}
