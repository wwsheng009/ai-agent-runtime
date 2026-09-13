package agentcontrol

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestReconcilerRunOnceCachesEnforceReport covers the host-facing contract:
// one pass converges the drift and the cached report is what `/debug` and the
// supervision HTTP payload render.
func TestReconcilerRunOnceCachesEnforceReport(t *testing.T) {
	ctx := context.Background()
	store := newTestGlobalAgentRegistryStore(t)
	_, err := store.UpsertAgentControlAgent(ctx, AgentRecord{
		AgentID:       "agent-a",
		RootSessionID: "root-1",
		AgentPath:     "/root/worker-a",
		SessionID:     "sess-a",
		AgentType:     AgentTypeChild,
		Status:        AgentStatusActive,
	})
	require.NoError(t, err)

	reconciler := &Reconciler{
		Store:    store,
		Lookup:   sessionLookupFromSnapshot(nil),
		Mode:     ReconcileModeEnforce,
		Interval: DefaultReconcileInterval,
	}
	report, err := reconciler.RunOnce(ctx)
	require.NoError(t, err)
	require.Equal(t, 1, report.Converged)
	require.Equal(t, string(ReconcileModeEnforce), report.Mode)

	cached, lastErr, ok := reconciler.LastReport()
	require.True(t, ok)
	require.Empty(t, lastErr)
	require.Equal(t, 1, cached.Converged)
	require.False(t, cached.ReconciledAt.IsZero())

	summary := reconciler.ReconcileSummary()
	require.Contains(t, summary, "reconcile=enforce")
	require.Contains(t, summary, "converged=1")
	require.Contains(t, summary, "last_reconcile=")

	after := activeReconcileTestRecords(ctx, t, store, "root-1")
	require.Len(t, after, 1)
	require.True(t, after[0].Closed())
}

// TestReconcilerObserveModeIsTheDefault locks the plan's safety default: an
// unset mode never writes, even when the pass finds drift.
func TestReconcilerObserveModeIsTheDefault(t *testing.T) {
	ctx := context.Background()
	store := newTestGlobalAgentRegistryStore(t)
	_, err := store.UpsertAgentControlAgent(ctx, AgentRecord{
		AgentID:       "agent-a",
		RootSessionID: "root-1",
		AgentPath:     "/root/worker-a",
		SessionID:     "sess-a",
		AgentType:     AgentTypeChild,
		Status:        AgentStatusActive,
	})
	require.NoError(t, err)

	reconciler := &Reconciler{Store: store, Lookup: sessionLookupFromSnapshot(nil)}
	report, err := reconciler.RunOnce(ctx)
	require.NoError(t, err)
	require.Equal(t, 1, report.IssueCount)
	require.Zero(t, report.Converged)

	after := activeReconcileTestRecords(ctx, t, store, "root-1")
	require.False(t, after[0].Closed())
	require.Contains(t, reconciler.ReconcileSummary(), "reconcile=observe")
}

// TestReconcilerRunOnceCachesError keeps failures visible instead of letting a
// broken pass look like an idle one.
func TestReconcilerRunOnceCachesError(t *testing.T) {
	reconciler := &Reconciler{
		List: func(context.Context) ([]AgentRecord, error) {
			return nil, errors.New("registry unavailable")
		},
	}
	_, err := reconciler.RunOnce(context.Background())
	require.Error(t, err)
	require.Contains(t, err.Error(), "registry unavailable")

	summary := reconciler.ReconcileSummary()
	require.Equal(t, "reconcile=error detail=registry unavailable", summary)
}

// TestReconcilerRunLoopPassesImmediatelyAndStops pins the loop contract used
// by both hosts: converge leftover drift at startup, then tick, then exit on
// context cancellation without leaking the goroutine.
func TestReconcilerRunLoopPassesImmediatelyAndStops(t *testing.T) {
	store := newTestGlobalAgentRegistryStore(t)
	_, err := store.UpsertAgentControlAgent(context.Background(), AgentRecord{
		AgentID:       "agent-a",
		RootSessionID: "root-1",
		AgentPath:     "/root/worker-a",
		SessionID:     "sess-a",
		AgentType:     AgentTypeChild,
		Status:        AgentStatusActive,
	})
	require.NoError(t, err)

	reconciler := &Reconciler{
		Store:    store,
		Lookup:   sessionLookupFromSnapshot(nil),
		Mode:     ReconcileModeEnforce,
		Interval: MinReconcileInterval,
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		reconciler.RunLoop(ctx)
	}()

	require.Eventually(t, func() bool {
		_, _, ok := reconciler.LastReport()
		return ok
	}, 5*time.Second, 10*time.Millisecond, "the startup pass must run before the first tick")

	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("RunLoop did not return after context cancellation")
	}

	after := activeReconcileTestRecords(context.Background(), t, store, "root-1")
	require.True(t, after[0].Closed())
}

// TestReconcilerModeAndIntervalNormalization covers the parsing used by host
// configuration and environment overrides.
func TestReconcilerModeAndIntervalNormalization(t *testing.T) {
	require.Equal(t, ReconcileModeEnforce, ParseReconcileMode(" ENFORCE "))
	require.Equal(t, ReconcileModeObserve, ParseReconcileMode(""))
	require.Equal(t, ReconcileModeObserve, ParseReconcileMode("aggressive"))

	require.Equal(t, DefaultReconcileInterval, NormalizeReconcileInterval(0))
	require.Equal(t, DefaultReconcileInterval, NormalizeReconcileInterval(-time.Minute))
	require.Equal(t, MinReconcileInterval, NormalizeReconcileInterval(time.Second))
	require.Equal(t, 30*time.Minute, NormalizeReconcileInterval(30*time.Minute))

	require.Equal(t, DefaultReconcileInterval, (*Reconciler)(nil).IntervalOrDefault())
	require.Equal(t, ReconcileModeObserve, (*Reconciler)(nil).ModeOrDefault())
	require.Equal(t, "reconcile=not_run", (*Reconciler)(nil).ReconcileSummary())
	_, err := (*Reconciler)(nil).RunOnce(context.Background())
	require.NoError(t, err)
	require.Equal(t, ReconcileModeEnforce, (&Reconciler{Mode: ReconcileModeEnforce}).ModeOrDefault())
}
