package chat

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestRetireDurablePendingInteractionClearsStateAndReturnsEvents(t *testing.T) {
	ctx := context.Background()
	store := NewInMemoryRuntimeStore(8)
	state := &RuntimeState{
		SessionID:     "sess-retire",
		Status:        SessionWaitingApproval,
		CurrentTurnID: "turn-1",
		PendingApproval: &ApprovalRequest{
			ID:         "approval-1",
			ToolName:   "shell_command",
			ToolCallID: "call-1",
		},
		PendingQuestion: &UserQuestionRequest{
			ID:       "question-1",
			Prompt:   "continue?",
			Required: true,
		},
		UpdatedAt: time.Now().UTC(),
	}
	require.NoError(t, store.SaveState(ctx, state))

	events, changed, err := RetireDurablePendingInteraction(ctx, store, state.SessionID, "agent_session_closed")
	require.NoError(t, err)
	require.True(t, changed)
	require.Len(t, events, 2)
	require.Equal(t, EventApprovalResolved, events[0].Type)
	require.Equal(t, ApprovalResolutionRunTerminated, events[0].Payload["resolution"])
	require.Equal(t, false, events[0].Payload["resumed"])
	require.Equal(t, "agent_session_closed", events[0].Payload["run_terminal_reason"])
	require.Equal(t, EventQuestionAnswered, events[1].Type)
	require.Equal(t, "agent_session_closed", events[1].Payload["resolution"])

	stored, err := store.LoadState(ctx, state.SessionID)
	require.NoError(t, err)
	require.Equal(t, SessionStopped, stored.Status)
	require.Empty(t, stored.CurrentTurnID)
	require.Nil(t, stored.PendingApproval)
	require.Nil(t, stored.PendingQuestion)
}

func TestExpireDurableApprovalOnlyRetiresElapsedWindow(t *testing.T) {
	ctx := context.Background()
	store := NewInMemoryRuntimeStore(8)
	now := time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC)

	live := &RuntimeState{
		SessionID: "sess-live",
		Status:    SessionWaitingApproval,
		PendingApproval: &ApprovalRequest{
			ID:        "approval-live",
			ExpiresAt: now.Add(time.Minute),
		},
	}
	require.NoError(t, store.SaveState(ctx, live))
	events, changed, err := ExpireDurableApproval(ctx, store, live.SessionID, now)
	require.NoError(t, err)
	require.False(t, changed)
	require.Empty(t, events)

	expired := &RuntimeState{
		SessionID: "sess-expired",
		Status:    SessionWaitingApproval,
		PendingApproval: &ApprovalRequest{
			ID:        "approval-expired",
			ExpiresAt: now.Add(-time.Minute),
		},
	}
	require.NoError(t, store.SaveState(ctx, expired))
	events, changed, err = ExpireDurableApproval(ctx, store, expired.SessionID, now)
	require.NoError(t, err)
	require.True(t, changed)
	require.Len(t, events, 1)
	require.Equal(t, ApprovalResolutionExpired, events[0].Payload["resolution"])

	stored, err := store.LoadState(ctx, expired.SessionID)
	require.NoError(t, err)
	require.Equal(t, SessionStopped, stored.Status)
	require.Nil(t, stored.PendingApproval)
}
