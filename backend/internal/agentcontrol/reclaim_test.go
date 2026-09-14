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
	// noopOn 模拟并发 sweep 的过期清单：这一行/子树已被别人关掉，store 的
	// UPDATE（closed_at IS NULL）没有匹配到任何行，所以返回 rows=0。
	noopOn string
}

func (f *fakeReclaimStore) ReclaimAgentControlAgentSubtree(_ context.Context, rootSessionID string, agentPath string, reason string, _ time.Time) (int64, error) {
	if f.failOn != "" && agentPath == f.failOn {
		return 0, fmt.Errorf("boom: %s", agentPath)
	}
	if f.noopOn != "" && agentPath == f.noopOn {
		return 0, nil
	}
	f.closed = append(f.closed, agentPath+"|"+reason+"|"+rootSessionID)
	return f.rows, nil
}

// 真实故障回归（2026-09-13 real-test 日志）：并发的 sweep 各自持有一份"关闭前"
// 的清单，后到的观察者会把别人刚关掉的子会话再选一遍。store 的 UPDATE 带
// closed_at IS NULL，因此这次调用的 rows=0——它既不是一次回收，也不能进
// reclaimed/reasons/agent_paths，否则同一批子会话会被重复打印（多行的直接来源）。
func TestReclaimAgentQuotaIgnoresStaleListingNoOps(t *testing.T) {
	now := time.Now().UTC()
	store := &fakeReclaimStore{rows: 1, noopOn: "/root/already-closed"}
	observations := []ReclaimObservation{
		{AgentID: "a", AgentPath: "/root/already-closed", Status: AgentStatusActive, SessionTerminal: true},
		{AgentID: "b", AgentPath: "/root/fresh", Status: AgentStatusActive, SessionTerminal: true},
	}

	outcome, err := ReclaimAgentQuota(context.Background(), store, "root-session", observations, ReclaimPolicy{Now: now})
	require.NoError(t, err)
	require.Equal(t, 1, outcome.Reclaimed(), "0 行的空动作不算一次回收")
	require.Equal(t, int64(1), outcome.Rows)
	require.Len(t, outcome.Decisions, 1)
	require.Equal(t, "/root/fresh", outcome.Decisions[0].AgentPath)
	require.Equal(t, []string{ReclaimReasonSessionTerminal}, outcome.Reasons)
	require.Equal(t, "reclaimed=1 reclaimed_rows=1 reclaim_reasons=session_terminal", outcome.Summary())

	payload := ReclaimEventPayload(ReclaimSourceReconcile, outcome)
	require.Equal(t, []string{"/root/fresh"}, payload["agent_paths"], "已被别人关掉的子会话不再出现在事件里")
	require.Equal(t, 1, payload["reclaimed"])

	// 全部命中过期清单时不发布也不报错：没有真实改动就没有事件。
	allStale, err := ReclaimAgentQuota(context.Background(), store, "root-session", []ReclaimObservation{
		{AgentID: "a", AgentPath: "/root/already-closed", Status: AgentStatusActive, SessionTerminal: true},
	}, ReclaimPolicy{Now: now})
	require.NoError(t, err)
	require.Zero(t, allStale.Reclaimed())
	require.Empty(t, allStale.Summary())
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

// TestReclaimSummaryFoldsDuplicateReasons pins the fixed one-liner: a pass over
// N children that all hit the same reason prints one label, not one label per
// child (live residual: "reclaim_reasons=session_terminal,session_terminal,
// session_terminal" was repeated on every spawn attempt the gate refused).
func TestReclaimSummaryFoldsDuplicateReasons(t *testing.T) {
	now := time.Now().UTC()
	store := &fakeReclaimStore{rows: 1}
	observations := []ReclaimObservation{
		{AgentID: "a", AgentPath: "/root/a", Status: AgentStatusActive, SessionTerminal: true},
		{AgentID: "b", AgentPath: "/root/b", Status: AgentStatusActive, SessionTerminal: true},
		{AgentID: "c", AgentPath: "/root/c", Status: AgentStatusActive, SessionTerminal: true},
	}

	outcome, err := ReclaimAgentQuota(context.Background(), store, "root-session", observations, ReclaimPolicy{Now: now})
	require.NoError(t, err)
	require.Equal(t, 3, outcome.Reclaimed())
	require.Equal(t, []string{ReclaimReasonSessionTerminal}, outcome.Reasons)
	require.Equal(t, "reclaimed=3 reclaimed_rows=3 reclaim_reasons=session_terminal", outcome.Summary())

	// Observe mode shares the label, so `/debug` reads the same either way.
	evaluated := EvaluateReclaimOutcome(observations, ReclaimPolicy{Now: now})
	require.Equal(t, []string{ReclaimReasonSessionTerminal}, evaluated.Reasons)
	require.Equal(t, "reclaim_candidates=3 reclaim_reasons=session_terminal", evaluated.Summary())

	// Hand-built outcomes (hosts that never fill Reasons) are folded by Summary.
	handBuilt := ReclaimOutcome{
		Rows: 3,
		Decisions: []ReclaimDecision{
			{AgentPath: "/root/a", Reason: ReclaimReasonSessionTerminal},
			{AgentPath: "/root/b", Reason: ReclaimReasonSessionTerminal},
		},
	}
	require.Equal(t, "reclaimed=2 reclaimed_rows=3 reclaim_reasons=session_terminal", handBuilt.Summary())

	// Mixed reasons keep their first-seen order so the label stays stable.
	mixed := ReclaimOutcome{
		Decisions: []ReclaimDecision{
			{AgentPath: "/root/a", Reason: ReclaimReasonSessionMissing},
			{AgentPath: "/root/b", Reason: ReclaimReasonSessionTerminal},
			{AgentPath: "/root/c", Reason: ReclaimReasonSessionMissing},
		},
	}
	require.Equal(t, "reclaimed=3 reclaim_reasons=session_missing,session_terminal", mixed.Summary())
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

// TestReclaimHumanSummary pins the operator-facing one-liner added for the
// "reclaimed=3 reclaimed_rows=3 reclaim_reasons=session_terminal" legibility
// report: the very same outcome that renders counters through Summary() must
// also render one zh-CN sentence saying what happened and whether the reader
// has to care. Counters stay untouched (spawn gate, /debug, event payload).
func TestReclaimHumanSummary(t *testing.T) {
	terminal := ReclaimOutcome{
		Rows: 3,
		Decisions: []ReclaimDecision{
			{Reason: ReclaimReasonSessionTerminal},
			{Reason: ReclaimReasonSessionTerminal},
			{Reason: ReclaimReasonSessionTerminal},
		},
	}
	require.Equal(t, "reclaimed=3 reclaimed_rows=3 reclaim_reasons=session_terminal", terminal.Summary())
	require.Equal(t, "已自动回收 3 个已结束的子会话；释放 3 个线程槽位", terminal.HumanSummary())

	// Mixed reasons keep Summary()'s first-seen order, folded into one
	// parenthesised list so the sentence cannot grow with the child count.
	mixed := ReclaimOutcome{
		Decisions: []ReclaimDecision{
			{Reason: ReclaimReasonSessionMissing},
			{Reason: ReclaimReasonIdleTimeout},
			{Reason: ReclaimReasonSessionMissing},
		},
	}
	require.Equal(t, "reclaimed=3 reclaim_reasons=session_missing,idle_timeout", mixed.Summary())
	require.Equal(t,
		"已自动回收 3 个子 agent（已不存在的子会话、长期空闲的子会话）；释放 3 个线程槽位",
		mixed.HumanSummary())

	// Observe mode (candidates only) must not claim an eviction happened.
	require.Equal(t, "可回收 2 个已结束的子会话（仅评估，未执行回收）",
		ReclaimOutcome{Candidates: 2, Reasons: []string{ReclaimReasonSessionTerminal}}.HumanSummary())

	// Failures are the only part an operator must act on, so they carry the
	// marker and the first error verbatim.
	require.Equal(t,
		"未能回收任何子 agent；1 个未能回收：registry row is locked（需要关注）",
		ReclaimOutcome{Failed: 1, FirstError: "registry row is locked"}.HumanSummary())

	// A reason the label table does not know yet stays visible in contract form
	// instead of being glued onto the classifier.
	require.Equal(t, "已自动回收 1 个子 agent（reason=new_reason）；释放 1 个线程槽位",
		ReclaimOutcome{Decisions: []ReclaimDecision{{Reason: "new_reason"}}}.HumanSummary())

	// Nothing attempted → no line at all (callers omit it rather than print noise).
	require.Empty(t, ReclaimOutcome{}.HumanSummary())
}

// TestReclaimReasonLabel pins the shared wording table so every surface
// (`/agents cleanup`, the CLI timeline note, spawn-gate diagnostics) explains an
// eviction with the same words; unknown values fall through unchanged rather
// than being hidden behind a generic label.
func TestReclaimReasonLabel(t *testing.T) {
	require.Equal(t, "已结束的子会话", ReclaimReasonLabel(ReclaimReasonSessionTerminal))
	require.Equal(t, "已不存在的子会话", ReclaimReasonLabel(ReclaimReasonSessionMissing))
	require.Equal(t, "长期空闲的子会话", ReclaimReasonLabel(ReclaimReasonIdleTimeout))
	require.Equal(t, "future_reason", ReclaimReasonLabel(" future_reason "))
	require.Empty(t, ReclaimReasonLabel("   "))
}
