package supervision

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// staticProgressSource stands in for the P0-B batch projection: it returns a
// fixed rollup so the progress-wake contract can be pinned without a real batch
// store.
type staticProgressSource struct {
	groups []ProgressGroup
}

func (s staticProgressSource) ListProgress(context.Context, ProgressRequest) ([]ProgressGroup, error) {
	return s.groups, nil
}

func runningProgressGroup() ProgressGroup {
	return ProgressGroup{
		GroupID:        "batch-1",
		Label:          "batch-1",
		Total:          2,
		Completed:      1,
		Running:        1,
		LastProgressAt: time.Now().UTC(),
	}
}

type progressWakeProbe struct {
	deliveries int
	lastDigest *Digest
}

// newProgressWakeHarness builds the production shape of P2-D: a scheduler with
// the progress projection wired and a consumer that records deliveries instead
// of starting a parent turn.
func newProgressWakeHarness(t *testing.T, name string, maxAutoWake int) (*SQLiteSupervisionStore, *WakeScheduler, *WakeConsumer, *progressWakeProbe) {
	t.Helper()
	store := newTestStore(t, name)
	scheduler := NewWakeScheduler(store, WakeSchedulerConfig{
		RateWindow:           time.Hour,
		MaxAutoWakePerWindow: maxAutoWake,
	})
	scheduler.SetProgressSource(staticProgressSource{groups: []ProgressGroup{runningProgressGroup()}})
	probe := &progressWakeProbe{}
	consumer := &WakeConsumer{
		Wakes:    scheduler,
		Runnable: func(context.Context, string, string, string) bool { return true },
		Deliver: func(_ context.Context, _, _ string, digest *Digest, _ []string) error {
			probe.deliveries++
			probe.lastDigest = digest
			return nil
		},
	}
	return store, scheduler, consumer, probe
}

// TestWakeConsumer_ProgressOnlyWakeDeliversRollup pins P2-D: the progress check
// has no lifecycle notification, so the P0-B rollup is the only content its
// digest can carry. That rollup must be enough to start the report turn, the
// turn must spend the bounded wake budget exactly once, and the claimed wake
// must be resolved so the next check can schedule a fresh one.
func TestWakeConsumer_ProgressOnlyWakeDeliversRollup(t *testing.T) {
	store, scheduler, consumer, probe := newProgressWakeHarness(t, "wake-consumer-progress-only", 1)
	ctx := context.Background()

	_, err := scheduler.ScheduleWake(ctx, WakeRequest{
		RootScopeID:           "root-session-1",
		TargetParentSessionID: "root-session-1",
		WakeReason:            WakeReasonProgressCheck,
	})
	require.NoError(t, err)

	require.NoError(t, consumer.MaybeWakeParent(ctx, "root-session-1", "", "root-session-1"))
	require.Equal(t, 1, probe.deliveries,
		"a progress-only digest carries the rollup and must start the report turn")
	require.NotNil(t, probe.lastDigest)
	require.NotEmpty(t, probe.lastDigest.Progress, "the wake digest must carry the P0-B rollup")
	require.Contains(t, probe.lastDigest.Text, "progress:", "the injected text must show the rollup")

	pending, err := store.ListWakePending(ctx, WakeFilter{
		RootScopeID:           "root-session-1",
		TargetParentSessionID: "root-session-1",
		UnclaimedOnly:         true,
	})
	require.NoError(t, err)
	require.Empty(t, pending, "the delivered progress wake is consumed, not left dangling")

	require.False(t, scheduler.AllowAutoWake(ctx, "root-session-1", WakeBudgetClassOther, time.Now().UTC()),
		"the delivered report turn spends the bounded class budget")
}

// TestWakeConsumer_ProgressSource_StaleLifecycleWakeStaysSilent pins the other
// half of the rule: wiring a progress source must not turn a lifecycle wake
// whose notification disappeared while the parent was busy into a
// progress-only parent turn, and such a drain must not spend budget either.
func TestWakeConsumer_ProgressSource_StaleLifecycleWakeStaysSilent(t *testing.T) {
	store, scheduler, consumer, probe := newProgressWakeHarness(t, "wake-consumer-stale-lifecycle", 1)
	ctx := context.Background()

	_, err := scheduler.ScheduleWake(ctx, WakeRequest{
		RootScopeID:           "root-session-1",
		TargetParentSessionID: "root-session-1",
		WakeReason:            WakeReasonExecutionFailed,
	})
	require.NoError(t, err)

	require.NoError(t, consumer.MaybeWakeParent(ctx, "root-session-1", "", "root-session-1"))
	require.Zero(t, probe.deliveries,
		"a lifecycle wake with no notification must not launch a content-free turn")

	pending, err := store.ListWakePending(ctx, WakeFilter{
		RootScopeID:           "root-session-1",
		TargetParentSessionID: "root-session-1",
		UnclaimedOnly:         true,
	})
	require.NoError(t, err)
	require.Empty(t, pending, "the stale durable wake is still cleaned up")

	require.True(t, scheduler.AllowAutoWake(ctx, "root-session-1", WakeBudgetClassFailure, time.Now().UTC()),
		"a wake that never became a turn must not spend the next window")
}

// TestWakeScheduler_ProgressSourceUnwiredKeepsDigestEmpty pins the opt-in
// boundary: without the progress projection the drained digest stays exactly as
// it was before P0-B, so existing hosts cannot start progress turns by accident.
func TestWakeScheduler_ProgressSourceUnwiredKeepsDigestEmpty(t *testing.T) {
	store := newTestStore(t, "wake-scheduler-progress-unwired")
	ctx := context.Background()
	scheduler := NewWakeScheduler(store, WakeSchedulerConfig{RateWindow: time.Hour})

	_, err := scheduler.ScheduleWake(ctx, WakeRequest{
		RootScopeID:           "root-session-1",
		TargetParentSessionID: "root-session-1",
		WakeReason:            WakeReasonProgressCheck,
	})
	require.NoError(t, err)

	claimed, digest, err := scheduler.DrainRunnable(ctx, "root-session-1", "", "root-session-1",
		func(context.Context, string, string, string) bool { return true })
	require.NoError(t, err)
	require.Len(t, claimed, 1)
	require.Empty(t, digest.Progress, "an unwired host must not grow a progress rollup")
	require.False(t, digestDeliverable(claimed, digest),
		"without the rollup the progress wake has no content and must stay silent")
}
