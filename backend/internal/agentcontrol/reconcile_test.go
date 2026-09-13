package agentcontrol

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// sessionLookupFromSnapshot builds a lookup over a fixed snapshot table. A
// session id that is absent from the table is reported as missing, which is
// what the audit turns into ACTIVE_AGENT_SESSION_MISSING.
func sessionLookupFromSnapshot(snapshots map[string]SessionBindingSnapshot) SessionBindingLookup {
	return func(ctx context.Context, sessionID string) (SessionBindingSnapshot, error) {
		snapshot, ok := snapshots[sessionID]
		if !ok {
			return SessionBindingSnapshot{SessionID: sessionID}, nil
		}
		snapshot.SessionID = sessionID
		return snapshot, nil
	}
}

func activeReconcileTestRecords(ctx context.Context, t *testing.T, store AgentRegistryStore, rootSessionID string) []AgentRecord {
	t.Helper()
	records, err := store.ListAgentControlAgents(ctx, AgentFilter{RootSessionID: rootSessionID, IncludeClosed: true})
	require.NoError(t, err)
	return records
}

// TestReconcileAgentSessionConsistency_ObserveOnlyReports locks the default
// mode: the pass reads the registry, reports drift and writes nothing.
func TestReconcileAgentSessionConsistency_ObserveOnlyReports(t *testing.T) {
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

	records := activeReconcileTestRecords(ctx, t, store, "root-1")
	report, err := ReconcileAgentSessionConsistency(ctx, store, records, sessionLookupFromSnapshot(nil), ReconcileModeObserve)
	require.NoError(t, err)
	require.Equal(t, string(ReconcileModeObserve), report.Mode)
	require.Equal(t, 1, report.IssueCount)
	require.Equal(t, IssueActiveAgentSessionMissing, report.Issues[0].Code)
	require.Equal(t, 0, report.Converged)
	require.Empty(t, report.Actions)

	after := activeReconcileTestRecords(ctx, t, store, "root-1")
	require.Len(t, after, 1)
	require.False(t, after[0].Closed(), "observe mode must not write")
}

// TestReconcileAgentSessionConsistency_EnforceReleasesQuota is the plan
// acceptance scenario: an ACTIVE_AGENT_SESSION_MISSING row keeps the quota
// occupied until an enforce pass converges it.
func TestReconcileAgentSessionConsistency_EnforceReleasesQuota(t *testing.T) {
	ctx := context.Background()
	store := newTestGlobalAgentRegistryStore(t)
	root := AgentRecord{
		AgentID:       "root-agent",
		RootSessionID: "root-1",
		AgentPath:     "/root",
		SessionID:     "sess-root",
		AgentType:     AgentTypeRoot,
		Status:        AgentStatusActive,
	}
	childA := AgentRecord{
		AgentID:       "agent-a",
		RootSessionID: "root-1",
		AgentPath:     "/root/worker-a",
		SessionID:     "sess-a",
		AgentType:     AgentTypeChild,
		Status:        AgentStatusActive,
	}
	_, err := store.ReserveAgentControlAgentSpawn(ctx, root, childA, 1)
	require.NoError(t, err)

	childB := AgentRecord{
		AgentID:       "agent-b",
		RootSessionID: "root-1",
		AgentPath:     "/root/worker-b",
		SessionID:     "sess-b",
		AgentType:     AgentTypeChild,
		Status:        AgentStatusActive,
	}
	_, err = store.ReserveAgentControlAgentSpawn(ctx, root, childB, 1)
	require.Error(t, err)
	require.Contains(t, err.Error(), "max_threads=1")

	// The child container is gone (TTL cleanup, crash) while the registry row
	// still looks active.
	records := activeReconcileTestRecords(ctx, t, store, "root-1")
	report, err := ReconcileAgentSessionConsistency(ctx, store, records, sessionLookupFromSnapshot(map[string]SessionBindingSnapshot{
		"sess-root": {Exists: true, Status: "running"},
	}), ReconcileModeEnforce)
	require.NoError(t, err)
	require.Equal(t, 1, report.IssueCount)
	require.Equal(t, 1, report.Converged)
	require.EqualValues(t, 1, report.ConvergedRows)
	require.Equal(t, ReconcileActionClose, report.Actions[0].Action)
	require.Equal(t, "agent-a", report.Actions[0].AgentID)

	// Quota released: the same reservation now succeeds.
	_, err = store.ReserveAgentControlAgentSpawn(ctx, root, childB, 1)
	require.NoError(t, err)

	converged, err := store.ListAgentControlAgents(ctx, AgentFilter{AgentID: "agent-a", IncludeClosed: true})
	require.NoError(t, err)
	require.Len(t, converged, 1)
	require.True(t, converged[0].Closed())
}

// TestReconcileAgentSessionConsistency_EnforceMarksStale covers the
// diagnostics-preserving branch: a stale finding uses the stale marker when the
// store supports it instead of closing the row.
func TestReconcileAgentSessionConsistency_EnforceMarksStale(t *testing.T) {
	ctx := context.Background()
	store := newTestGlobalAgentRegistryStore(t)
	_, err := store.UpsertAgentControlAgent(ctx, AgentRecord{
		AgentID:       "agent-stale",
		RootSessionID: "root-1",
		AgentPath:     "/root/worker-stale",
		SessionID:     "sess-stale",
		AgentType:     AgentTypeChild,
		Status:        AgentStatusActive,
	})
	require.NoError(t, err)

	records := activeReconcileTestRecords(ctx, t, store, "root-1")
	report, err := ReconcileAgentSessionConsistency(ctx, store, records, sessionLookupFromSnapshot(map[string]SessionBindingSnapshot{
		"sess-stale": {Exists: true, Status: "running", Stale: true},
	}), ReconcileModeEnforce)
	require.NoError(t, err)
	require.Equal(t, 1, report.Converged)
	require.Equal(t, ReconcileActionMarkStale, report.Actions[0].Action)

	after := activeReconcileTestRecords(ctx, t, store, "root-1")
	require.Len(t, after, 1)
	require.Equal(t, AgentStatusStale, after[0].Status)
	require.True(t, after[0].Closed())
}

// TestReconcileAgentSessionConsistency_StaleFallsBackToClose verifies the
// optional-interface fallback: a store without the stale marker still releases
// the quota by closing the subtree.
func TestReconcileAgentSessionConsistency_StaleFallsBackToClose(t *testing.T) {
	ctx := context.Background()
	inner := newTestGlobalAgentRegistryStore(t)
	_, err := inner.UpsertAgentControlAgent(ctx, AgentRecord{
		AgentID:       "agent-stale",
		RootSessionID: "root-1",
		AgentPath:     "/root/worker-stale",
		SessionID:     "sess-stale",
		AgentType:     AgentTypeChild,
		Status:        AgentStatusActive,
	})
	require.NoError(t, err)

	// Embedding the interface (not the concrete store) hides the optional
	// AgentStaleMarker implementation, exactly like an older store
	// implementation would.
	var store AgentRegistryStore = struct{ AgentRegistryStore }{inner}
	records := activeReconcileTestRecords(ctx, t, store, "root-1")
	report, err := ReconcileAgentSessionConsistency(ctx, store, records, sessionLookupFromSnapshot(map[string]SessionBindingSnapshot{
		"sess-stale": {Exists: true, Status: "running", Stale: true},
	}), ReconcileModeEnforce)
	require.NoError(t, err)
	require.Equal(t, 1, report.Converged)
	require.Equal(t, ReconcileActionClose, report.Actions[0].Action)

	after := activeReconcileTestRecords(ctx, t, store, "root-1")
	require.True(t, after[0].Closed())
}

// TestReconcileAgentSessionConsistency_LeavesLiveAndWaitingApprovalAlone is the
// safety contract from the plan: a running or approval-parked session must
// never be marked stale/closed by the pass.
func TestReconcileAgentSessionConsistency_LeavesLiveAndWaitingApprovalAlone(t *testing.T) {
	ctx := context.Background()
	store := newTestGlobalAgentRegistryStore(t)
	for _, record := range []AgentRecord{
		{AgentID: "agent-run", RootSessionID: "root-1", AgentPath: "/root/run", SessionID: "sess-run", AgentType: AgentTypeChild, Status: AgentStatusActive},
		{AgentID: "agent-approval", RootSessionID: "root-1", AgentPath: "/root/approval", SessionID: "sess-approval", AgentType: AgentTypeChild, Status: AgentStatusActive},
	} {
		_, err := store.UpsertAgentControlAgent(ctx, record)
		require.NoError(t, err)
	}

	records := activeReconcileTestRecords(ctx, t, store, "root-1")
	report, err := ReconcileAgentSessionConsistency(ctx, store, records, sessionLookupFromSnapshot(map[string]SessionBindingSnapshot{
		"sess-run":      {Exists: true, Status: "running"},
		"sess-approval": {Exists: true, Status: "waiting_approval"},
	}), ReconcileModeEnforce)
	require.NoError(t, err)
	require.Zero(t, report.IssueCount)
	require.Zero(t, report.Converged)
	require.Empty(t, report.Actions)

	after := activeReconcileTestRecords(ctx, t, store, "root-1")
	require.Len(t, after, 2)
	for _, record := range after {
		require.False(t, record.Closed(), "live session %s must stay active", record.SessionID)
	}
}

// TestReconcileAgentSessionConsistency_Idempotent verifies that a second pass
// over reloaded records converges nothing and does not rewrite rows.
func TestReconcileAgentSessionConsistency_Idempotent(t *testing.T) {
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
	lookup := sessionLookupFromSnapshot(nil)

	first, err := ReconcileAgentSessionConsistency(ctx, store, activeReconcileTestRecords(ctx, t, store, "root-1"), lookup, ReconcileModeEnforce)
	require.NoError(t, err)
	require.Equal(t, 1, first.Converged)

	convergedRows := activeReconcileTestRecords(ctx, t, store, "root-1")
	require.Len(t, convergedRows, 1)
	convergedAt := convergedRows[0].UpdatedAt

	second, err := ReconcileAgentSessionConsistency(ctx, store, activeReconcileTestRecords(ctx, t, store, "root-1"), lookup, ReconcileModeEnforce)
	require.NoError(t, err)
	require.Zero(t, second.IssueCount, "a closed row is not audited again")
	require.Zero(t, second.Converged)
	require.Empty(t, second.Actions)

	after := activeReconcileTestRecords(ctx, t, store, "root-1")
	require.Len(t, after, 1)
	require.True(t, after[0].UpdatedAt.Equal(convergedAt), "second pass must not rewrite the row")
}

// TestReconcileAgentSessionConsistency_SkipsUnroutableAndUnknownRecords covers
// the defensive skips: findings without a routing path or without a matching
// input record never touch the store.
func TestReconcileAgentSessionConsistency_SkipsUnroutableAndUnknownRecords(t *testing.T) {
	ctx := context.Background()
	store := newTestGlobalAgentRegistryStore(t)
	records := []AgentRecord{
		{AgentID: "agent-no-path", RootSessionID: "root-1", AgentPath: "", SessionID: "sess-x", AgentType: AgentTypeChild, Status: AgentStatusActive},
		{AgentID: "agent-ok", RootSessionID: "root-1", AgentPath: "/root/ok", SessionID: "sess-ok", AgentType: AgentTypeChild, Status: AgentStatusActive},
	}
	report, err := ReconcileAgentSessionConsistency(ctx, store, records, sessionLookupFromSnapshot(nil), ReconcileModeEnforce)
	require.NoError(t, err)
	require.Equal(t, 2, report.IssueCount)
	require.Equal(t, 1, report.Converged, "only the routable record is converged")
	require.Equal(t, 1, report.Skipped)

	var skipped ReconcileAction
	for _, action := range report.Actions {
		if action.Action == ReconcileActionSkip {
			skipped = action
		}
	}
	require.Equal(t, "agent-no-path", skipped.AgentID)
	require.Contains(t, skipped.Reason, "no root session or agent path")

	// Enforce mode without a store skips instead of panicking.
	noStore, err := ReconcileAgentSessionConsistency(ctx, nil, records, sessionLookupFromSnapshot(nil), ReconcileModeEnforce)
	require.NoError(t, err)
	require.Zero(t, noStore.Converged)
	require.Equal(t, 2, noStore.Skipped)
}

// TestReconcileReport_SummaryAndConvergenceFlag covers the diagnostic surface
// used by /debug and host status lines.
func TestReconcileReport_SummaryAndConvergenceFlag(t *testing.T) {
	report := ReconcileReport{
		Mode:         string(ReconcileModeEnforce),
		ReconciledAt: time.Date(2026, 9, 13, 10, 0, 0, 0, time.UTC),
		IssueCount:   3,
		Converged:    2,
		Skipped:      1,
		Issues: []ConsistencyAuditIssue{
			{Code: IssueActiveAgentSessionMissing},
			{Code: IssueAgentSessionIDMismatch},
		},
	}
	require.True(t, report.HasConvergenceIssues())
	summary := report.Summary()
	require.Contains(t, summary, "reconcile=enforce")
	require.Contains(t, summary, "consistency_issues=3")
	require.Contains(t, summary, "converged=2")
	require.Contains(t, summary, "skipped=1")
	require.Contains(t, summary, "last_reconcile=2026-09-13T10:00:00Z")

	empty := ReconcileReport{}
	require.False(t, empty.HasConvergenceIssues())
	require.Equal(t, "reconcile=not_run", empty.Summary())
}
