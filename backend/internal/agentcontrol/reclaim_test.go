package agentcontrol

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestResolveMaxThreads(t *testing.T) {
	cases := []struct {
		name        string
		raw         int
		defaultMax  int
		wantLimit   int
		wantUnlimit bool
	}{
		{name: "zero falls back to default", raw: 0, defaultMax: 6, wantLimit: 6},
		{name: "zero with zero default stays unlimited", raw: 0, defaultMax: 0, wantUnlimit: true},
		{name: "explicit unlimited", raw: MaxThreadsUnlimited, defaultMax: 6, wantUnlimit: true},
		{name: "positive quota wins", raw: 3, defaultMax: 6, wantLimit: 3},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			limit, unlimited := ResolveMaxThreads(tc.raw, tc.defaultMax)
			require.Equal(t, tc.wantUnlimit, unlimited)
			require.Equal(t, tc.wantLimit, limit)
		})
	}
}

func TestQuotaChildrenFiltersRootAndTerminalRows(t *testing.T) {
	closedAt := time.Now().UTC()
	records := []AgentRecord{
		{AgentID: "root", AgentPath: "/root", AgentType: AgentTypeRoot, Status: AgentStatusActive},
		{AgentID: "child-active", AgentPath: "/root/active", Status: AgentStatusActive},
		{AgentID: "child-unset-status", AgentPath: "/root/unset"},
		{AgentID: "child-closed", AgentPath: "/root/closed", Status: AgentStatusClosed},
		{AgentID: "child-stale", AgentPath: "/root/stale", Status: AgentStatusStale},
		{AgentID: "child-closed-at", AgentPath: "/root/closed-at", Status: AgentStatusActive, ClosedAt: &closedAt},
	}
	children := QuotaChildren(records)
	require.Len(t, children, 2)
	require.Equal(t, "/root/active", children[0].AgentPath)
	require.Equal(t, "/root/unset", children[1].AgentPath)
	require.False(t, records[3].HoldsQuota())
	require.True(t, records[1].HoldsQuota())
}

func TestReclaimableProtectsLiveChildren(t *testing.T) {
	now := time.Date(2026, 9, 13, 12, 0, 0, 0, time.UTC)
	policy := ReclaimPolicy{IdleTimeout: 5 * time.Minute, Now: now}
	idle := func(d time.Duration) ReclaimObservation {
		return ReclaimObservation{
			AgentPath:        "/root/idle",
			Status:           AgentStatusActive,
			SessionIdleSince: now.Add(-d),
		}
	}

	require.Equal(t, ReclaimReasonIdleTimeout, Reclaimable(idle(10*time.Minute), policy))

	busy := idle(10 * time.Minute)
	busy.SessionBusy = true
	require.Empty(t, Reclaimable(busy, policy), "running/approval-parked children must never be reclaimed")

	// Idle eviction is opt-in: with the default policy a live idle child holds
	// its slot even when it has been idle for hours.
	require.Empty(t, Reclaimable(idle(time.Hour), ReclaimPolicy{Now: now}))

	missing := ReclaimObservation{AgentPath: "/root/gone", Status: AgentStatusActive, SessionMissing: true}
	require.Equal(t, ReclaimReasonSessionMissing, Reclaimable(missing, ReclaimPolicy{Now: now}))

	terminal := ReclaimObservation{AgentPath: "/root/old", Status: AgentStatusActive, SessionTerminal: true, SessionBusy: true}
	require.Equal(t, ReclaimReasonSessionTerminal, Reclaimable(terminal, ReclaimPolicy{Now: now}))

	closed := idle(time.Hour)
	closed.Status = AgentStatusClosed
	require.Empty(t, Reclaimable(closed, policy))

	unknown := ReclaimObservation{AgentPath: "/root/unknown", Status: AgentStatusActive}
	require.Empty(t, Reclaimable(unknown, policy), "unknown last activity must disable idle eviction")
}

func TestSelectReclaimableOrdersByAgentPath(t *testing.T) {
	now := time.Now().UTC()
	observations := []ReclaimObservation{
		{AgentPath: "/root/z", Status: AgentStatusActive, SessionMissing: true},
		{AgentPath: "/root/a", Status: AgentStatusActive, SessionTerminal: true},
		{AgentPath: "/root/live", Status: AgentStatusActive},
	}
	decisions := SelectReclaimable(observations, ReclaimPolicy{Now: now})
	require.Len(t, decisions, 2)
	require.Equal(t, "/root/a", decisions[0].AgentPath)
	require.Equal(t, "/root/z", decisions[1].AgentPath)
	require.Equal(t, "reclaimed:"+ReclaimReasonSessionMissing, decisions[1].EventKind())
}

func TestThreadLimitMessageCarriesNextActionAndOccupants(t *testing.T) {
	occupants := []ThreadOccupant{
		{AgentPath: "/root/a", SessionID: "sess-a", Status: AgentStatusActive, IdleFor: 3 * time.Minute},
		{AgentPath: "/root/b", Status: AgentStatusActive},
		{AgentPath: "/root/c", Status: AgentStatusActive, IdleFor: time.Minute},
		{AgentPath: "/root/d", Status: AgentStatusActive},
		{AgentPath: "/root/e", Status: AgentStatusActive},
	}
	message := ThreadLimitMessage(6, 6, occupants, "reclaimed=1 reclaimed_rows=2")

	require.Contains(t, message, "agent spawn thread limit reached: max_threads=6 active_children=6")
	require.Contains(t, message, "reclaimed=1 reclaimed_rows=2")
	require.Contains(t, message, "next_action="+ThreadLimitNextAction)
	require.Contains(t, message, "close_agent")
	require.Contains(t, message, fmt.Sprintf("set %d for unlimited", MaxThreadsUnlimited))
	require.Contains(t, message, "path=/root/a session=sess-a status=active idle=3m0s")
	require.Contains(t, message, "idle=unknown")
	require.Contains(t, message, "+1 more")

	bare := ThreadLimitMessage(2, 2, nil)
	require.Contains(t, bare, "next_action="+ThreadLimitNextAction)
	require.NotContains(t, bare, "occupants=[")
}

type fakeReclaimStore struct {
	closed []string
	rows   int64
	failOn string
}

func (f *fakeReclaimStore) ReclaimAgentControlAgentSubtree(_ context.Context, rootSessionID string, agentPath string, reason string, _ time.Time) (int64, error) {
	if f.failOn != "" && agentPath == f.failOn {
		return 0, fmt.Errorf("boom: %s", agentPath)
	}
	f.closed = append(f.closed, agentPath+"|"+reason+"|"+rootSessionID)
	return f.rows, nil
}

func TestReclaimAgentQuotaClosesSelectedChildrenAndReportsFailures(t *testing.T) {
	now := time.Now().UTC()
	store := &fakeReclaimStore{rows: 2, failOn: "/root/b"}
	observations := []ReclaimObservation{
		{AgentID: "a", AgentPath: "/root/a", Status: AgentStatusActive, SessionMissing: true},
		{AgentID: "b", AgentPath: "/root/b", Status: AgentStatusActive, SessionTerminal: true},
		{AgentID: "c", AgentPath: "/root/c", Status: AgentStatusActive, SessionBusy: true},
	}
	outcome, err := ReclaimAgentQuota(context.Background(), store, "root-session", observations, ReclaimPolicy{Now: now})
	require.NoError(t, err)
	require.Equal(t, 1, outcome.Reclaimed())
	require.Equal(t, 1, outcome.Failed)
	require.Equal(t, int64(2), outcome.Rows)
	require.Contains(t, outcome.FirstError, "boom")
	require.Equal(t, []string{"/root/a|" + ReclaimReasonSessionMissing + "|root-session"}, store.closed)

	summary := outcome.Summary()
	require.Contains(t, summary, "reclaimed=1")
	require.Contains(t, summary, "reclaimed_rows=2")
	require.Contains(t, summary, "reclaim_failed=1")
	require.Contains(t, summary, "reclaim_reasons="+ReclaimReasonSessionMissing)
	require.Contains(t, summary, "reclaim_error=boom")
}

func TestReclaimAgentQuotaRequiresStoreAndRootSession(t *testing.T) {
	_, err := ReclaimAgentQuota(context.Background(), nil, "root-session", nil, ReclaimPolicy{})
	require.Error(t, err)

	store := &fakeReclaimStore{}
	_, err = ReclaimAgentQuota(context.Background(), store, "   ", nil, ReclaimPolicy{})
	require.Error(t, err)
	require.Empty(t, store.closed)

	outcome, err := ReclaimAgentQuota(context.Background(), store, "root-session", nil, ReclaimPolicy{})
	require.NoError(t, err)
	require.Zero(t, outcome.Reclaimed())
	require.Empty(t, outcome.Summary(), "a pass that attempted nothing must not add diagnostics")
}

func TestSQLiteReclaimAgentControlAgentSubtreeEmitsReclaimWake(t *testing.T) {
	ctx := context.Background()
	store := newTestGlobalAgentRegistryStore(t)
	root := AgentRecord{
		AgentID:       "root",
		RootSessionID: "root-session",
		SessionID:     "root-session",
		AgentPath:     "/root",
		AgentType:     AgentTypeRoot,
	}
	_, err := store.ReserveAgentControlAgentSpawn(ctx, root, AgentRecord{
		AgentID:         "worker",
		RootSessionID:   "root-session",
		ParentAgentID:   "root",
		ParentSessionID: "root-session",
		SessionID:       "worker-session",
		AgentPath:       "/root/worker",
		Depth:           1,
		AgentType:       AgentTypeChild,
		Workflow:        WorkflowSpawnAgent,
	}, 3)
	require.NoError(t, err)

	watchCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	wake, unwatch := store.WatchAgentControlAgentWake(watchCtx, AgentWakeFilter{
		RootSessionID: "root-session",
		AgentPath:     "/root/worker",
	})
	defer unwatch()

	rows, err := store.ReclaimAgentControlAgentSubtree(ctx, "root-session", "/root/worker", ReclaimReasonIdleTimeout, time.Now().UTC())
	require.NoError(t, err)
	require.Equal(t, int64(1), rows)
	select {
	case event := <-wake:
		require.Equal(t, AgentStatusClosed, event.Status)
		require.Equal(t, "reclaimed:"+ReclaimReasonIdleTimeout, event.EventKind)
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for reclaim wake")
	}

	records, err := store.ListAgentControlAgents(ctx, AgentFilter{RootSessionID: "root-session", IncludeClosed: true})
	require.NoError(t, err)
	for _, record := range records {
		if record.AgentPath == "/root/worker" {
			require.True(t, record.Closed())
			require.Equal(t, AgentStatusClosed, record.Status)
		}
	}
}
