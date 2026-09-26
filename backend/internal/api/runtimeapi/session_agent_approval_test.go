package runtimeapi

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/wwsheng009/ai-agent-runtime/internal/chat"
	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
	"github.com/wwsheng009/ai-agent-runtime/internal/supervision"
)

// TestAPIAgentApprovalSubscriptionProjectsAndResolves verifies the P0-2 host
// bridge end to end: a child approval edge published by the runtime reaches
// the durable parent inbox through the existing per-child subscription, and
// the matching resolution edge clears it again.
func TestAPIAgentApprovalSubscriptionProjectsAndResolves(t *testing.T) {
	ctx := context.Background()
	handler, store, _ := newAPIWakeTestHandler(t, "api-approval-bridge")
	bus := handler.getRuntimeEventBus()
	require.NotNil(t, bus)

	sessionManager := chat.NewSessionManager(chat.NewInMemoryStorage(), nil)
	defer sessionManager.Stop()
	handler.SetSessionManager(sessionManager)

	parent, err := sessionManager.Create(ctx, "user-approval")
	require.NoError(t, err)
	child := chat.NewSession(parent.UserID)
	child.ID = "child-approval-1"
	require.NoError(t, sessionManager.GetStorage().Save(ctx, child))

	controller := handler.getAgentSessionController()
	require.NotNil(t, controller)
	controller.subscribeAgentCompletion(parent.ID, child)

	bus.Publish(runtimeevents.Event{
		Type:      chat.EventApprovalRequested,
		SessionID: child.ID,
		Payload: map[string]interface{}{
			"request_id": "req-bridge-1",
			"tool_name":  "write_file",
		},
		Timestamp: time.Now().UTC(),
	})

	digest, err := supervision.BuildDigest(ctx, store, supervision.DigestRequest{
		RootScopeID:           parent.ID,
		TargetParentSessionID: parent.ID,
	})
	require.NoError(t, err)
	require.Equal(t, 1, digest.ActionRequired, "the parent must see the pending approval")
	require.Contains(t, digest.Text, "request_id=req-bridge-1")
	require.Contains(t, digest.Text, "tool=write_file")

	// The bridge also drives the parent wake path; whether the durable row is
	// still pending depends on the runnable check, so assert the wake-eligible
	// severity instead of a transient row state.
	notifications, err := store.ListNotifications(ctx, supervision.NotificationFilter{
		RootScopeID: parent.ID,
		SubjectID:   child.ID,
	})
	require.NoError(t, err)
	require.Len(t, notifications, 1)
	require.Equal(t, supervision.SeverityCritical, notifications[0].Severity)

	bus.Publish(runtimeevents.Event{
		Type:      chat.EventApprovalResolved,
		SessionID: child.ID,
		Payload: map[string]interface{}{
			"request_id": "req-bridge-1",
			"allowed":    true,
		},
		Timestamp: time.Now().UTC(),
	})

	digest, err = supervision.BuildDigest(ctx, store, supervision.DigestRequest{
		RootScopeID:           parent.ID,
		TargetParentSessionID: parent.ID,
	})
	require.NoError(t, err)
	require.Equal(t, 0, digest.ActionRequired, "a resolved approval must leave ordinary preflight")
	require.Empty(t, digest.Items)
}

// TestAPIAgentQuestionSubscriptionProjectsDigestOnly verifies that a child
// question reaches the digest without spending the critical wake budget.
func TestAPIAgentQuestionSubscriptionProjectsDigestOnly(t *testing.T) {
	ctx := context.Background()
	handler, store, _ := newAPIWakeTestHandler(t, "api-question-bridge")
	bus := handler.getRuntimeEventBus()
	require.NotNil(t, bus)

	sessionManager := chat.NewSessionManager(chat.NewInMemoryStorage(), nil)
	defer sessionManager.Stop()
	handler.SetSessionManager(sessionManager)

	parent, err := sessionManager.Create(ctx, "user-question")
	require.NoError(t, err)
	child := chat.NewSession(parent.UserID)
	child.ID = "child-question-1"
	require.NoError(t, sessionManager.GetStorage().Save(ctx, child))

	controller := handler.getAgentSessionController()
	require.NotNil(t, controller)
	controller.subscribeAgentCompletion(parent.ID, child)

	bus.Publish(runtimeevents.Event{
		Type:      chat.EventQuestionAsked,
		SessionID: child.ID,
		Payload: map[string]interface{}{
			"question_id": "q-bridge-1",
			"prompt":      "Which environment should the migration target?",
		},
		Timestamp: time.Now().UTC(),
	})

	digest, err := supervision.BuildDigest(ctx, store, supervision.DigestRequest{
		RootScopeID:           parent.ID,
		TargetParentSessionID: parent.ID,
	})
	require.NoError(t, err)
	require.Equal(t, 1, digest.ActionRequired)
	require.Contains(t, digest.Text, "q-bridge-1")
	require.Contains(t, digest.Text, "Which environment")

	pending, err := store.ListWakePending(ctx, supervision.WakeFilter{
		RootScopeID:          parent.ID,
		TargetParentSessionID: parent.ID,
		UnclaimedOnly:        true,
	})
	require.NoError(t, err)
	require.Empty(t, pending, "questions stay digest-only")
}
