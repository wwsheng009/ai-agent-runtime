package skills

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/wwsheng009/ai-agent-runtime/internal/chat"
	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
	"github.com/wwsheng009/ai-agent-runtime/internal/skill"
	"github.com/wwsheng009/ai-agent-runtime/internal/supervision"
)

func newAPIWakeTestHandler(t *testing.T, name string) (*Handler, *supervision.SQLiteSupervisionStore, *supervision.WakeScheduler) {
	t.Helper()
	store, err := supervision.NewSQLiteSupervisionStore(&supervision.StoreConfig{
		DSN: "file:" + name + "?mode=memory&cache=shared",
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	scheduler := supervision.NewWakeScheduler(store, supervision.WakeSchedulerConfig{})

	handler := NewHandler(skill.NewRegistry(nil), nil, nil)
	handler.SetSupervisionStore(store)
	handler.SetSupervisionWakeScheduler(scheduler)
	return handler, store, scheduler
}

// TestAPIControllerWakeSupervisedParent_NoWakesNoOp verifies the API wake
// consumer is a safe no-op without pending wakes or a configured scheduler.
func TestAPIControllerWakeSupervisedParent_NoWakesNoOp(t *testing.T) {
	handler, _, _ := newAPIWakeTestHandler(t, "api-wake-noop")
	controller := &sessionAgentController{handler: handler}
	ctx := context.Background()

	require.NoError(t, controller.wakeSupervisedParent(ctx, "root-session", "root-session"))
	require.NoError(t, controller.wakeSupervisedParent(ctx, "", ""))
	require.NoError(t, controller.wakeSupervisedParent(ctx, "root-session", "root-session"))

	// Scheduler absent: no-op as well.
	bare := NewHandler(skill.NewRegistry(nil), nil, nil)
	bareController := &sessionAgentController{handler: bare}
	require.NoError(t, bareController.wakeSupervisedParent(ctx, "root-session", "root-session"))
}

// TestAPIControllerWakeSupervisedParent_BusyActorKeepsWake verifies that a
// parent without a runnable actor keeps the wake durable (doc 6.5 rule 2):
// the API host never starts a turn for a session that has no live idle actor.
func TestAPIControllerWakeSupervisedParent_BusyActorKeepsWake(t *testing.T) {
	handler, store, scheduler := newAPIWakeTestHandler(t, "api-wake-busy")
	controller := &sessionAgentController{handler: handler}
	ctx := context.Background()

	_, err := supervision.ProjectLifecycle(ctx, store, scheduler, supervision.LifecycleProjection{
		RootScopeID:           "root-session",
		TargetParentSessionID: "root-session",
		SubjectKind:           supervision.SubjectAgentRun,
		SubjectID:             "child-1",
		EventType:             "exception",
		Severity:              supervision.SeverityCritical,
		SupervisionState:      supervision.SupervisionBlocked,
	})
	require.NoError(t, err)

	// No live actor in the hub: the wake must stay pending.
	err = controller.wakeSupervisedParent(ctx, "root-session", "root-session")
	require.ErrorIs(t, err, supervision.ErrWakeParentBusy)

	pending, err := store.ListWakePending(ctx, supervision.WakeFilter{
		RootScopeID:           "root-session",
		TargetParentSessionID: "root-session",
		UnclaimedOnly:         true,
	})
	require.NoError(t, err)
	require.Len(t, pending, 1, "wake must remain durable while parent is not runnable")
}

// TestAPITurnEndSubscription_BindsWithoutSideEffects verifies the turn-end
// wake subscription is bound once when the scheduler is installed and that
// published session-end events are handled safely for unknown sessions.
func TestAPITurnEndSubscription_BindsWithoutSideEffects(t *testing.T) {
	handler, store, scheduler := newAPIWakeTestHandler(t, "api-wake-turnend")
	ctx := context.Background()

	// Install again: the subscription must remain bound exactly once
	// (no duplicate deliveries possible).
	handler.SetSupervisionWakeScheduler(scheduler)

	// Unknown session turn end: no panic, no wake consumption.
	bus := handler.getRuntimeEventBus()
	require.NotNil(t, bus)
	bus.Publish(runtimeevents.Event{
		Type:      chat.EventSessionEnd,
		SessionID: "unknown-session",
		Payload:   map[string]interface{}{"success": true},
	})

	// A critical wake for the unknown parent must still be pending: the
	// subscription found no runnable actor and no session record.
	_, err := supervision.ProjectLifecycle(ctx, store, scheduler, supervision.LifecycleProjection{
		RootScopeID:           "unknown-session",
		TargetParentSessionID: "unknown-session",
		SubjectKind:           supervision.SubjectAgentRun,
		SubjectID:             "child-1",
		EventType:             "exception",
		Severity:              supervision.SeverityCritical,
		SupervisionState:      supervision.SupervisionBlocked,
	})
	require.NoError(t, err)
	bus.Publish(runtimeevents.Event{
		Type:      chat.EventSessionEnd,
		SessionID: "unknown-session",
		Payload:   map[string]interface{}{"success": true},
	})
	time.Sleep(200 * time.Millisecond)

	pending, err := store.ListWakePending(ctx, supervision.WakeFilter{
		RootScopeID:           "unknown-session",
		TargetParentSessionID: "unknown-session",
		UnclaimedOnly:         true,
	})
	require.NoError(t, err)
	require.Len(t, pending, 1, "turn end without a runnable session must keep the wake pending")
}
