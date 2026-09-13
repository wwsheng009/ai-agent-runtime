package agentcontrol

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// P2-9 方案 3「自动 close」的共享驱动器：observe 只报候选、enforce 走同一套
// ReclaimAgentQuota 判定，并按 root 折叠成宿主事件（agent.reclaimed）。

func TestEvaluateReclaimOutcomeReportsCandidatesWithoutStore(t *testing.T) {
	now := time.Now().UTC()
	observations := []ReclaimObservation{
		{AgentID: "a", AgentPath: "/root/a", Status: AgentStatusActive, SessionMissing: true},
		{AgentID: "b", AgentPath: "/root/b", Status: AgentStatusActive, SessionTerminal: true},
		{AgentID: "c", AgentPath: "/root/c", Status: AgentStatusActive, SessionBusy: true},
	}

	outcome := EvaluateReclaimOutcome(observations, ReclaimPolicy{Now: now})
	require.Equal(t, 2, outcome.Candidates)
	require.Zero(t, outcome.Reclaimed())
	require.Zero(t, outcome.Rows)
	require.ElementsMatch(t, []string{ReclaimReasonSessionMissing, ReclaimReasonSessionTerminal}, outcome.Reasons)

	summary := outcome.Summary()
	require.Contains(t, summary, "reclaim_candidates=2")
	require.Contains(t, summary, ReclaimReasonSessionMissing)
	require.Contains(t, summary, ReclaimReasonSessionTerminal)
	require.NotContains(t, summary, "reclaimed=", "evaluate-only passes must not claim a close")
	require.NotContains(t, summary, "reclaim_failed=", "nothing was attempted, so nothing failed")

	empty := EvaluateReclaimOutcome([]ReclaimObservation{{AgentID: "c", AgentPath: "/root/c", Status: AgentStatusActive, SessionBusy: true}}, ReclaimPolicy{Now: now})
	require.Zero(t, empty.Candidates)
	require.Empty(t, empty.Summary(), "a busy-only pass has nothing to report")
}

func TestQuotaRootsGroupsQuotaChildrenInStableOrder(t *testing.T) {
	records := []AgentRecord{
		{AgentID: "root-b", AgentPath: "/root", AgentType: AgentTypeRoot, RootSessionID: "root-b"},
		{AgentID: "a", AgentPath: "/root/a", AgentType: AgentTypeChild, RootSessionID: "root-b", Status: AgentStatusActive},
		{AgentID: "b", AgentPath: "/root/b", AgentType: AgentTypeChild, RootSessionID: "root-a", Status: AgentStatusActive},
		{AgentID: "c", AgentPath: "/root/c", AgentType: AgentTypeChild, RootSessionID: "root-b", Status: AgentStatusClosed},
		{AgentID: "d", AgentPath: "/root/d", AgentType: AgentTypeChild, RootSessionID: "  ", Status: AgentStatusActive},
	}

	roots := QuotaRoots(records)
	require.Len(t, roots, 2)
	require.Equal(t, "root-a", roots[0].RootSessionID)
	require.Equal(t, []string{"/root/b"}, []string{roots[0].Children[0].AgentPath})
	require.Equal(t, "root-b", roots[1].RootSessionID)
	require.Len(t, roots[1].Children, 1)
	require.Equal(t, "/root/a", roots[1].Children[0].AgentPath)
}

func TestSweepAgentQuotaReclaimObserveModeNeverTouchesStore(t *testing.T) {
	now := time.Now().UTC()
	store := &fakeReclaimStore{rows: 5}
	records := []AgentRecord{
		{AgentID: "a", AgentPath: "/root/a", AgentType: AgentTypeChild, RootSessionID: "root-a", Status: AgentStatusActive},
		{AgentID: "b", AgentPath: "/root/b", AgentType: AgentTypeChild, RootSessionID: "root-b", Status: AgentStatusActive},
	}
	observe := func(_ context.Context, children []AgentRecord) []ReclaimObservation {
		observations := make([]ReclaimObservation, 0, len(children))
		for _, child := range children {
			observations = append(observations, ReclaimObservation{
				AgentID:          child.AgentID,
				AgentPath:        child.AgentPath,
				Status:           child.Status,
				SessionIdleSince: now.Add(-2 * time.Minute),
			})
		}
		return observations
	}
	sinks := 0

	// A nil store is legal in observe mode: the pass only evaluates.
	outcome, err := SweepAgentQuotaReclaim(context.Background(), nil, records, observe, ReclaimPolicy{IdleTimeout: time.Minute, Now: now}, false, func(context.Context, string, ReclaimOutcome) {
		sinks++
	})
	require.NoError(t, err)
	require.Equal(t, 2, outcome.Candidates)
	require.Equal(t, []string{ReclaimReasonIdleTimeout}, outcome.Reasons, "duplicate reasons collapse into one label")
	require.Zero(t, outcome.Reclaimed())
	require.Zero(t, sinks, "observe mode publishes no eviction event")
	require.Empty(t, store.closed)

	summary := outcome.Summary()
	require.Contains(t, summary, "reclaim_candidates=2")
	require.Contains(t, summary, "reclaim_reasons="+ReclaimReasonIdleTimeout)
	require.NotContains(t, summary, "reclaimed=")
}

func TestSweepAgentQuotaReclaimEnforceClosesPerRootAndPublishesSink(t *testing.T) {
	now := time.Now().UTC()
	store := &fakeReclaimStore{rows: 2}
	records := []AgentRecord{
		{AgentID: "a", AgentPath: "/root/a", AgentType: AgentTypeChild, RootSessionID: "root-b", Status: AgentStatusActive},
		{AgentID: "b", AgentPath: "/root/b", AgentType: AgentTypeChild, RootSessionID: "root-a", Status: AgentStatusActive},
	}
	observe := func(_ context.Context, children []AgentRecord) []ReclaimObservation {
		return []ReclaimObservation{{
			AgentID:          children[0].AgentID,
			AgentPath:        children[0].AgentPath,
			Status:           children[0].Status,
			SessionTerminal:  true,
			SessionIdleSince: now,
		}}
	}
	type sinkCall struct {
		root    string
		summary string
	}
	var calls []sinkCall

	outcome, err := SweepAgentQuotaReclaim(
		context.Background(),
		store,
		records,
		observe,
		ReclaimPolicy{Now: now},
		true,
		func(_ context.Context, rootSessionID string, pass ReclaimOutcome) {
			calls = append(calls, sinkCall{root: rootSessionID, summary: pass.Summary()})
		},
	)
	require.NoError(t, err)
	require.Equal(t, 2, outcome.Reclaimed())
	require.Equal(t, int64(4), outcome.Rows)
	require.Equal(t, []string{ReclaimReasonSessionTerminal}, outcome.Reasons)
	require.Equal(t, []string{
		"/root/b|" + ReclaimReasonSessionTerminal + "|root-a",
		"/root/a|" + ReclaimReasonSessionTerminal + "|root-b",
	}, store.closed, "roots are swept in sorted order")
	require.Equal(t, []string{"root-a", "root-b"}, []string{calls[0].root, calls[1].root})
	for _, call := range calls {
		require.Contains(t, call.summary, "reclaimed=1")
	}
}

func TestSweepAgentQuotaReclaimEnforceRequiresStore(t *testing.T) {
	_, err := SweepAgentQuotaReclaim(context.Background(), nil, nil, nil, ReclaimPolicy{}, true, nil)
	require.Error(t, err)
	require.Contains(t, err.Error(), "reclaim store")
}

func TestSweepAgentQuotaReclaimPerRootFailureDegradesToFirstError(t *testing.T) {
	now := time.Now().UTC()
	store := &fakeReclaimStore{rows: 1, failOn: "/root/a"}
	records := []AgentRecord{
		{AgentID: "a", AgentPath: "/root/a", AgentType: AgentTypeChild, RootSessionID: "root-a", Status: AgentStatusActive},
		{AgentID: "b", AgentPath: "/root/b", AgentType: AgentTypeChild, RootSessionID: "root-b", Status: AgentStatusActive},
	}
	observe := func(_ context.Context, children []AgentRecord) []ReclaimObservation {
		return []ReclaimObservation{{
			AgentID:        children[0].AgentID,
			AgentPath:      children[0].AgentPath,
			Status:         children[0].Status,
			SessionMissing: true,
		}}
	}
	published := []string{}

	outcome, err := SweepAgentQuotaReclaim(
		context.Background(),
		store,
		records,
		observe,
		ReclaimPolicy{Now: now},
		true,
		func(_ context.Context, rootSessionID string, _ ReclaimOutcome) {
			published = append(published, rootSessionID)
		},
	)
	require.NoError(t, err, "one refused eviction must not abort the sweep")
	require.Equal(t, 1, outcome.Reclaimed())
	require.Equal(t, 1, outcome.Failed)
	require.Contains(t, outcome.FirstError, "boom")
	require.Contains(t, outcome.Summary(), "reclaim_failed=1")
	require.Equal(t, []string{"root-b"}, published, "only closed subtrees are published")
}

func TestAppendReclaimReasonDeduplicates(t *testing.T) {
	reasons := AppendReclaimReason(nil, "  ")
	require.Empty(t, reasons)

	reasons = AppendReclaimReason(reasons, ReclaimReasonSessionTerminal)
	reasons = AppendReclaimReason(reasons, ReclaimReasonIdleTimeout)
	reasons = AppendReclaimReason(reasons, ReclaimReasonSessionTerminal)
	require.Equal(t, []string{ReclaimReasonSessionTerminal, ReclaimReasonIdleTimeout}, reasons)
}

func TestReconcilerFoldsReclaimOutcomeIntoReport(t *testing.T) {
	now := time.Now().UTC()
	observed := ReclaimOutcome{
		Decisions: []ReclaimDecision{{AgentPath: "/root/a", Reason: ReclaimReasonSessionTerminal}},
		Rows:      3,
		Failed:    1,
		Reasons:   []string{ReclaimReasonSessionTerminal},
	}
	var seenEnforce []bool

	reconciler := &Reconciler{
		Mode: ReconcileModeEnforce,
		List: func(context.Context) ([]AgentRecord, error) { return nil, nil },
		Reclaim: func(_ context.Context, _ []AgentRecord, enforce bool, _ time.Time) (ReclaimOutcome, error) {
			seenEnforce = append(seenEnforce, enforce)
			return observed, nil
		},
	}
	report, err := reconciler.RunOnce(context.Background())
	require.NoError(t, err)
	require.Equal(t, []bool{true}, seenEnforce, "enforce mode must ask the host for real evictions")
	require.Equal(t, 1, report.Reclaimed)
	require.Equal(t, int64(3), report.ReclaimedRows)
	require.Equal(t, 1, report.ReclaimFailed)
	require.Equal(t, []string{ReclaimReasonSessionTerminal}, report.ReclaimReasons)
	require.Empty(t, report.ReclaimError)
	require.Contains(t, report.Summary(), "reclaim_reasons="+ReclaimReasonSessionTerminal)
	require.Contains(t, report.Summary(), "reclaimed_rows=3")

	require.Less(t, report.ReconciledAt.Unix(), now.Add(time.Minute).Unix(), "the audit still stamps the pass")
}

func TestReconcilerKeepsAuditReportWhenReclaimFails(t *testing.T) {
	reconciler := &Reconciler{
		List: func(context.Context) ([]AgentRecord, error) { return nil, nil },
		Reclaim: func(context.Context, []AgentRecord, bool, time.Time) (ReclaimOutcome, error) {
			return ReclaimOutcome{}, fmt.Errorf("reclaim store is not initialized")
		},
	}
	report, err := reconciler.RunOnce(context.Background())
	require.NoError(t, err, "a failed eviction pass must not blank the audit result")
	require.Contains(t, report.ReclaimError, "reclaim store is not initialized")
	require.Zero(t, report.Reclaimed)
	require.Equal(t, string(ReconcileModeObserve), report.Mode)
}
