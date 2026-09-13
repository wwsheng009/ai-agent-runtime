package skills

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/wwsheng009/ai-agent-runtime/internal/supervision"
)

// TestAPIControllerSelfCheck_KeepsWakeWithoutRunnableActor verifies the API
// wiring of plan P1-6 方案 4: the turn-end self-check is opt-in, and even when
// enabled it must obey the same runnable gate as the auto-wake path, so a
// parent without a live idle actor keeps its wake durable instead of queueing
// a concurrent turn.
func TestAPIControllerSelfCheck_KeepsWakeWithoutRunnableActor(t *testing.T) {
	handler, store, scheduler := newAPIWakeTestHandlerWithConfig(t, "api-wake-self-check", supervision.WakeSchedulerConfig{
		RateWindow:           time.Hour,
		MaxAutoWakePerWindow: 1,
		SelfCheckPerWindow:   1,
	})
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

	// No live actor in the hub: the self-check must not start a turn and must
	// not drop the wake.
	require.NoError(t, controller.selfCheckSupervisedParent(ctx, "root-session", "root-session"))
	pending, err := store.ListWakePending(ctx, supervision.WakeFilter{
		RootScopeID:           "root-session",
		TargetParentSessionID: "root-session",
		UnclaimedOnly:         true,
	})
	require.NoError(t, err)
	require.Len(t, pending, 1, "a parent without a runnable actor must keep the wake pending")

	// The switch is read from the scheduler: with the default allowance (0) the
	// hook is a no-op, so the wake above stays the only one.
	disabled, disabledStore, disabledScheduler := newAPIWakeTestHandlerWithConfig(t, "api-wake-self-check-off", supervision.WakeSchedulerConfig{
		RateWindow:           time.Hour,
		MaxAutoWakePerWindow: 1,
	})
	disabledController := &sessionAgentController{handler: disabled}
	_, err = supervision.ProjectLifecycle(ctx, disabledStore, disabledScheduler, supervision.LifecycleProjection{
		RootScopeID:           "root-session",
		TargetParentSessionID: "root-session",
		SubjectKind:           supervision.SubjectAgentRun,
		SubjectID:             "child-1",
		EventType:             "exception",
		Severity:              supervision.SeverityCritical,
		SupervisionState:      supervision.SupervisionBlocked,
	})
	require.NoError(t, err)
	require.NoError(t, disabledController.selfCheckSupervisedParent(ctx, "root-session", "root-session"))
	disabledPending, err := disabledStore.ListWakePending(ctx, supervision.WakeFilter{
		RootScopeID:           "root-session",
		TargetParentSessionID: "root-session",
		UnclaimedOnly:         true,
	})
	require.NoError(t, err)
	require.Len(t, disabledPending, 1, "the disabled switch keeps the wake for the next natural turn")
}
