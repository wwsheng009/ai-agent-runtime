package runtimeapi

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wwsheng009/ai-agent-runtime/internal/chat"
	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
	"github.com/wwsheng009/ai-agent-runtime/internal/supervision"
)

// TestPublishTurnResumedPersistsStartPhase pins the API host's wake-start edge:
// the event must be appended to the session event store with a seq, not only
// published on the live bus, and it must be distinguishable from the later
// settled closure by phase.
func TestPublishTurnResumedPersistsStartPhase(t *testing.T) {
	store := chat.NewInMemoryRuntimeStore(8)
	handler := &Handler{sessionEventStore: store}
	var live []runtimeevents.Event
	handler.getRuntimeEventBus().Subscribe(runtimeevents.EventTurnResumed, func(event runtimeevents.Event) {
		live = append(live, event)
	})

	handler.publishTurnResumed(supervision.ResumeAnnouncement{
		TurnID:          "turn-api-resume",
		ParentSessionID: "session-api-resume",
		RootScopeID:     "session-api-resume",
		Trigger:         supervision.ResumeTriggerTerminal,
		WakeReasons:     []string{supervision.WakeReasonObligationSettled},
		WakeIDs:         []string{"wake-api-resume"},
		PendingCount:    0,
		Status:          "completed",
		Terminal:        true,
		Summary:         "settled",
	})

	require.Len(t, live, 1)
	require.Equal(t, "started", live[0].Payload["phase"])
	require.NotZero(t, live[0].Payload["seq"])

	persisted, err := store.ListEvents(context.Background(), "session-api-resume", 0, 0)
	require.NoError(t, err)
	require.Len(t, persisted, 1)
	require.Equal(t, runtimeevents.EventTurnResumed, persisted[0].Type)
	require.Equal(t, "started", persisted[0].Payload["phase"])
	require.NotZero(t, persisted[0].Payload["seq"])
}
