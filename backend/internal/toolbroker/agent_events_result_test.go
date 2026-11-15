package toolbroker

import (
	"testing"

	"github.com/stretchr/testify/require"

	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
)

func TestBuildAgentEventsResultDeepClonesPayloadAndTracksSeq(t *testing.T) {
	payload := map[string]interface{}{
		"seq":    float64(7),
		"nested": map[string]interface{}{"items": []interface{}{"a", "b"}},
	}
	event := runtimeevents.Event{
		Type:      "test.event",
		TraceID:   "trace-1",
		SessionID: "child-1",
		ToolName:  "view",
		AgentName: "worker",
		Payload:   payload,
	}
	result := BuildAgentEventsResult(" child-1 ", []runtimeevents.Event{event})
	require.Equal(t, "child-1", result.SessionID)
	require.Equal(t, 1, result.Count)
	require.Equal(t, int64(7), result.LatestSeq)
	require.Len(t, result.Events, 1)
	require.Equal(t, int64(7), result.Events[0].Seq)
	require.Equal(t, "consume_events: use returned events now; only re-call subagent_events with a higher after_seq when new events are needed", result.NextAction)

	// Mutating the source payload must not rewrite the projected event.
	payload["nested"].(map[string]interface{})["items"].([]interface{})[0] = "changed"
	require.Equal(t, "a", result.Events[0].Payload["nested"].(map[string]interface{})["items"].([]interface{})[0])
}

func TestBuildAgentEventsTimeoutResultUsesTimeoutGuidance(t *testing.T) {
	result := BuildAgentEventsTimeoutResult("child-1", nil)
	require.True(t, result.TimedOut)
	require.Zero(t, result.Count)
	require.NotEmpty(t, result.NextAction)
	require.Contains(t, result.NextAction, "stop_empty_event_poll")
}
