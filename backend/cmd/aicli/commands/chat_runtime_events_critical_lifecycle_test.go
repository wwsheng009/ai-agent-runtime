package commands

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
)

func TestCriticalSubagentLifecycleClassifier(t *testing.T) {
	for _, eventType := range []string{
		"subagent.completed",
		"subagent.denied",
		"subagent.batch.completed",
		"subagent.batch.failed",
		"subagent.batch.canceled",
		"subagent.batch.timed_out",
		"subagent.batch.orphaned",
		"subagent.batch.circuit_open",
	} {
		require.True(t, isCriticalSubagentLifecycleEvent(eventType), eventType)
	}
	for _, eventType := range []string{"subagent.started", "subagent.batch.started", "tool.completed", "assistant.delta"} {
		require.False(t, isCriticalSubagentLifecycleEvent(eventType), eventType)
	}
}

func TestCriticalSubagentTerminalBypassesTurnMismatch(t *testing.T) {
	bridge := newChatRuntimeEventBridge(&ChatSession{})
	bridge.primarySessionID = "parent-session"
	bridge.runStarted = true
	bridge.runActive = true
	bridge.activeTurnID = "new-turn"

	critical := runtimeevents.Event{
		Type:      "subagent.batch.failed",
		SessionID: "parent-session",
		Payload: map[string]interface{}{
			"turn_id":  "old-turn",
			"batch_id": "batch-1",
		},
	}
	require.False(t, bridge.shouldSuppressMismatchedPrimaryTurnEvent(critical))

	ordinary := critical
	ordinary.Type = "tool.completed"
	require.True(t, bridge.shouldSuppressMismatchedPrimaryTurnEvent(ordinary))
}

func TestCriticalLifecycleUsesReservedQueueCapacity(t *testing.T) {
	bridge := newChatRuntimeEventBridge(&ChatSession{})
	bridge.runEpoch = 1
	for i := 0; i < bridge.normalEventQueueCapacity(); i++ {
		accepted := bridge.enqueueNonStreamEvent(runtimeevents.Event{Type: "tool.completed"}, 1, 0)
		require.True(t, accepted, "ordinary event %d should fill normal capacity", i)
	}
	require.False(t, bridge.enqueueNonStreamEvent(runtimeevents.Event{Type: "tool.completed"}, 1, 0))
	require.True(t, bridge.enqueueNonStreamEvent(runtimeevents.Event{Type: "subagent.batch.failed"}, 1, 0))
}

func TestCriticalTerminalSurvivesRunEpochAdvance(t *testing.T) {
	session := &ChatSession{}
	bridge := newChatRuntimeEventBridge(session)
	bridge.runStarted = true
	bridge.runEpoch = 2
	var lines []string
	bridge.writeLine = func(line string) { lines = append(lines, line) }

	bridge.handleQueuedEvent(chatRuntimeQueuedEvent{
		epoch: 1,
		size:  1,
		event: runtimeevents.Event{
			Type: "subagent.batch.timed_out",
			Payload: map[string]interface{}{
				"batch_id": "batch-late",
				"error":    "deadline exceeded",
			},
		},
	})

	require.Len(t, lines, 1)
	require.True(t, strings.Contains(lines[0], "timed_out"), lines[0])
}

func TestRenderSubagentBatchTerminalStatuses(t *testing.T) {
	for _, eventType := range []string{
		"subagent.batch.failed",
		"subagent.batch.canceled",
		"subagent.batch.timed_out",
		"subagent.batch.orphaned",
		"subagent.batch.circuit_open",
	} {
		rendered := renderChatRuntimeTimelineEvent(runtimeevents.Event{
			Type: eventType,
			Payload: map[string]interface{}{
				"batch_id": "batch-1",
				"error":    "terminal reason",
			},
		})
		require.NotEmpty(t, rendered.Line, eventType)
		require.Contains(t, rendered.Line, strings.TrimPrefix(eventType, "subagent.batch."))
		require.Contains(t, rendered.Line, "terminal reason")
	}
}

func TestEndRunDrainTimeoutCannotFinalizeAsSuccess(t *testing.T) {
	bridge := newChatRuntimeEventBridge(&ChatSession{})
	bridge.markEndRunDrainTimeout()
	require.Error(t, bridge.RunError())
	require.Contains(t, bridge.RunError().Error(), "critical lifecycle delivery may still be pending")
}
