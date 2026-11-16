package runtimeapi

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/wwsheng009/ai-agent-runtime/internal/chat"
	"github.com/wwsheng009/ai-agent-runtime/internal/events"
)

func TestStopSessionForTerminalStateRetiresDurablePendingInteraction(t *testing.T) {
	ctx := context.Background()
	handler := NewHandler(nil, nil, nil)
	manager := chat.NewSessionManager(chat.NewInMemoryStorage(), nil)
	t.Cleanup(manager.Stop)
	handler.SetSessionManager(manager)
	session, err := manager.Create(ctx, "close-lifecycle-user")
	require.NoError(t, err)

	store := chat.NewInMemoryRuntimeStore(16)
	handler.sessionRuntimeStore = store
	handler.sessionEventStore = store
	require.NoError(t, store.SaveState(ctx, &chat.RuntimeState{
		SessionID:     session.ID,
		Status:        chat.SessionWaitingApproval,
		CurrentTurnID: "turn-close",
		PendingApproval: &chat.ApprovalRequest{
			ID:        "approval-close",
			ToolName:  "apply_patch",
			ExpiresAt: time.Now().Add(time.Minute),
		},
		PendingQuestion: &chat.UserQuestionRequest{ID: "question-close", Required: true},
	}))

	handler.stopSessionForTerminalState(ctx, session.ID, "session_closed")

	stored, err := store.LoadState(ctx, session.ID)
	require.NoError(t, err)
	require.Equal(t, chat.SessionStopped, stored.Status)
	require.Nil(t, stored.PendingApproval)
	require.Nil(t, stored.PendingQuestion)

	events := handler.getRuntimeEventBus().Query(events.QueryFilter{
		EventType: chat.EventApprovalResolved,
		Limit:     10,
	})
	require.Len(t, events, 1)
	require.Equal(t, chat.ApprovalResolutionRunTerminated, events[0].Payload["resolution"])
}

func TestExpireDurableApprovalIfNeededSweepsRestoredApproval(t *testing.T) {
	ctx := context.Background()
	handler := NewHandler(nil, nil, nil)
	store := chat.NewInMemoryRuntimeStore(8)
	handler.sessionRuntimeStore = store
	handler.sessionEventStore = store
	state := &chat.RuntimeState{
		SessionID: "restored-expired",
		Status:    chat.SessionWaitingApproval,
		PendingApproval: &chat.ApprovalRequest{
			ID:        "approval-expired",
			ExpiresAt: time.Now().Add(-time.Second),
		},
	}
	require.NoError(t, store.SaveState(ctx, state))

	reconciled, changed := handler.expireDurableApprovalIfNeeded(ctx, state.SessionID, state)
	require.True(t, changed)
	require.Equal(t, chat.SessionStopped, reconciled.Status)
	require.Nil(t, reconciled.PendingApproval)
}
