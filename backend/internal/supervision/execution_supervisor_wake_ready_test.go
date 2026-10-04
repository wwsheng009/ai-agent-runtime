package supervision

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestExecutionSupervisorNotifiesWakeReadyOnScan pins the 2026-10-04 fix: a
// scan-produced critical wake must hand the parent identity to the host's
// WakeReady hook at scheduling time. Without it an idle parent never reaches
// another runnable transition and the durable wake stays unclaimed forever.
func TestExecutionSupervisorNotifiesWakeReadyOnScan(t *testing.T) {
	supervisor, store := newTestExecutionSupervisor(t, "sup-wake-ready", ExecutionSupervisorConfig{
		Mode:                      "enforce",
		DefaultExecutionTimeout:   1 * time.Hour,
		DefaultProgressTimeout:    5 * time.Second,
		DefaultApprovalTimeout:    1 * time.Hour,
		DefaultCancelGrace:        15 * time.Second,
		StallEscalationMultiplier: 2,
		DecisionWindow:            10 * time.Second,
		DecisionWindowMax:         20 * time.Second,
	}, nil, nil)
	supervisor.Wakes = NewWakeScheduler(store, WakeSchedulerConfig{})

	type wakeCall struct {
		root   string
		parent string
		team   string
	}
	var calls []wakeCall
	supervisor.WakeReady = func(_ context.Context, root, parent, team string) {
		calls = append(calls, wakeCall{root: root, parent: parent, team: team})
	}

	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	supervisor.Now = func() time.Time { return now }
	_, err := supervisor.StartRun(ctx, RunSpec{
		RootSessionID:   "root-session",
		ParentSessionID: "parent-session",
		SessionID:       "child-1",
	})
	require.NoError(t, err)

	// Healthy run, no wake projected: the hook must stay silent.
	_, err = supervisor.ScanOnce(ctx)
	require.NoError(t, err)
	require.Empty(t, calls)

	// Escalation instant (deadline + 1x soft): progress_stalled is critical and
	// schedules a durable wake, so the host must be asked for an immediate drain
	// exactly once.
	supervisor.Now = func() time.Time { return now.Add(10 * time.Second) }
	_, err = supervisor.ScanOnce(ctx)
	require.NoError(t, err)
	require.Len(t, calls, 1, "a scan-produced critical wake must trigger the drain hook")
	require.Equal(t, "root-session", calls[0].root)
	require.Equal(t, "parent-session", calls[0].parent)
	require.Empty(t, calls[0].team)

	pending, err := store.ListWakePending(ctx, WakeFilter{RootScopeID: "root-session"})
	require.NoError(t, err)
	require.NotEmpty(t, pending, "the hook mirrors a wake that was actually scheduled")
}
