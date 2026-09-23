package supervision

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// unlimitedWakeConfig keeps the budget out of the way: these tests exercise the
// C2-5 capacity gate, not the auto-wake budget.
func unlimitedWakeConfig() WakeSchedulerConfig {
	return WakeSchedulerConfig{
		MaxAutoWakePerWindow:     -1,
		MaxApprovalWakePerWindow: -1,
		MaxProgressWakePerWindow: -1,
	}
}

// TestResumeQueueEscalation_Bounds pins the bounded-FIFO boundary (A6): the
// zero-value policy must select the package defaults, depth must win over
// timeout, and a fresh queue must not escalate.
func TestResumeQueueEscalation_Bounds(t *testing.T) {
	var policy ResumeQueuePolicy
	require.Equal(t, defaultResumeQueueTimeout, policy.EffectiveTimeout())
	require.Equal(t, defaultResumeQueueMaxDepth, policy.EffectiveMaxDepth())

	now := time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC)
	fresh := ResumeQueueState{QueuedAt: now, Position: 1, Depth: 1}
	cause, escalate := ResumeQueueEscalation(fresh, policy, now)
	require.False(t, escalate, "a freshly queued resume inside the bounds must keep waiting")
	require.Empty(t, cause)

	// Timeout boundary: exactly at the bound escalates, one second short does not.
	waited := ResumeQueueState{QueuedAt: now.Add(-defaultResumeQueueTimeout), Position: 1, Depth: 1}
	cause, escalate = ResumeQueueEscalation(waited, policy, now)
	require.True(t, escalate)
	require.Equal(t, ResumeGateQueueTimeout, cause)

	short := ResumeQueueState{QueuedAt: now.Add(-defaultResumeQueueTimeout + time.Second), Position: 1, Depth: 1}
	_, escalate = ResumeQueueEscalation(short, policy, now)
	require.False(t, escalate)

	// Depth wins: an over-deep queue escalates immediately even when fresh.
	deep := ResumeQueueState{QueuedAt: now, Position: 1, Depth: defaultResumeQueueMaxDepth + 1}
	cause, escalate = ResumeQueueEscalation(deep, policy, now)
	require.True(t, escalate)
	require.Equal(t, ResumeGateQueueDepth, cause)

	// A wake that is no longer queued and has no clock cannot escalate on time.
	gone := ResumeQueueState{Position: 0, Depth: 1}
	_, escalate = ResumeQueueEscalation(gone, policy, now)
	require.False(t, escalate)
}

// TestResumeQueueState_QueueLine pins the digest line that shows the parent its
// queue position (A6: digest 显示排队位次).
func TestResumeQueueState_QueueLine(t *testing.T) {
	state := ResumeQueueState{
		Position: 2,
		Depth:    5,
		Gate:     ResumeGateConcurrency,
		Waited:   90 * time.Second,
	}
	line := state.QueueLine()
	require.Contains(t, line, "位次 2/5")
	require.Contains(t, line, ResumeGateConcurrency)
	require.Contains(t, line, "1m30s")

	require.Empty(t, ResumeQueueState{Position: 0, Depth: 3}.QueueLine())
}

// TestWakeConsumer_CapacityDeniedKeepsWakeQueued is the A6 core: a denied
// resume must not start a turn, must not drop the wake, and the very same wake
// must still deliver once capacity frees up.
func TestWakeConsumer_CapacityDeniedKeepsWakeQueued(t *testing.T) {
	store := newTestStore(t, "resume-capacity-defer")
	ctx := context.Background()
	scheduler := NewWakeScheduler(store, unlimitedWakeConfig())

	allowed := false
	var seen []ResumeQueueState
	delivered := 0
	consumer := &WakeConsumer{
		Wakes:    scheduler,
		Runnable: func(ctx context.Context, rootScopeID, parentSessionID, parentTeamID string) bool { return true },
		ResumeCapacity: ResumeCapacityProbeFunc(func(ctx context.Context, req ResumeCapacityRequest) (ResumeCapacityVerdict, error) {
			if allowed {
				return ResumeCapacityVerdict{Allowed: true}, nil
			}
			require.Equal(t, "root-session-a6", req.RootScopeID)
			require.Equal(t, "root-session-a6", req.TargetParentSessionID)
			require.Len(t, req.WakeIDs, 1)
			require.False(t, req.QueuedAt.IsZero(), "the gate needs the queue clock")
			return ResumeCapacityVerdict{Allowed: false, Reason: ResumeGateConcurrency, Detail: "subagent slots exhausted"}, nil
		}),
		OnResumeDeferred: func(ctx context.Context, state ResumeQueueState) {
			seen = append(seen, state)
		},
		Deliver: func(ctx context.Context, parentSessionID, rootScopeID string, digest *Digest, wakeIDs []string) error {
			delivered++
			return nil
		},
	}

	_, err := ProjectLifecycle(ctx, store, scheduler, LifecycleProjection{
		RootScopeID:           "root-session-a6",
		TargetParentSessionID: "root-session-a6",
		SubjectKind:           SubjectAgentRun,
		SubjectID:             "child-a6",
		EventType:             "timeout",
		Severity:              SeverityCritical,
		SupervisionState:      SupervisionTimedOut,
	})
	require.NoError(t, err)

	require.NoError(t, consumer.MaybeWakeParent(ctx, "root-session-a6", "", "root-session-a6"))
	require.Zero(t, delivered, "a denied resume must not start a parent turn")

	require.Len(t, seen, 1, "the host must observe the deferral so it can show the queue position")
	require.Equal(t, ResumeGateConcurrency, seen[0].Gate)
	require.Equal(t, 1, seen[0].Position, "the released wake is first in the FIFO")
	require.Equal(t, 1, seen[0].Depth)

	queued, err := store.ListWakePending(ctx, WakeFilter{RootScopeID: "root-session-a6", UnclaimedOnly: true})
	require.NoError(t, err)
	require.Len(t, queued, 1, "the deferred wake must stay durable and unclaimed")
	require.Equal(t, "root-session-a6", queued[0].RootScopeID)

	// Capacity frees up: the same wake must still start exactly one resume.
	allowed = true
	require.NoError(t, consumer.MaybeWakeParent(ctx, "root-session-a6", "", "root-session-a6"))
	require.Equal(t, 1, delivered, "the queued wake must deliver once capacity frees up")

	queued, err = store.ListWakePending(ctx, WakeFilter{RootScopeID: "root-session-a6", UnclaimedOnly: true})
	require.NoError(t, err)
	require.Empty(t, queued, "a delivered wake is consumed, not queued again")
}

// TestWakeConsumer_QueueTimeoutEscalatesOnce verifies the bounded-FIFO
// escalation (A6/EC-H9): a resume that waits past the policy timeout raises one
// durable critical + action_required notification while the wake itself stays
// queued for the next window.
func TestWakeConsumer_QueueTimeoutEscalatesOnce(t *testing.T) {
	store := newTestStore(t, "resume-queue-timeout")
	ctx := context.Background()
	scheduler := NewWakeScheduler(store, unlimitedWakeConfig())
	now := time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC)
	scheduler.now = func() time.Time { return now }

	delivered := 0
	consumer := &WakeConsumer{
		Wakes:        scheduler,
		Runnable:     func(ctx context.Context, rootScopeID, parentSessionID, parentTeamID string) bool { return true },
		ResumeQueue:  ResumeQueuePolicy{Timeout: 5 * time.Minute},
		ResumeCapacity: ResumeCapacityProbeFunc(func(ctx context.Context, req ResumeCapacityRequest) (ResumeCapacityVerdict, error) {
			return ResumeCapacityVerdict{Allowed: false, Reason: ResumeGateDepth, Detail: "descendant depth at max"}, nil
		}),
		Deliver: func(ctx context.Context, parentSessionID, rootScopeID string, digest *Digest, wakeIDs []string) error {
			delivered++
			return nil
		},
	}

	_, err := ProjectLifecycle(ctx, store, scheduler, LifecycleProjection{
		RootScopeID:           "root-session-queue",
		TargetParentSessionID: "root-session-queue",
		SubjectKind:           SubjectAgentRun,
		SubjectID:             "child-queue",
		EventType:             "timeout",
		Severity:              SeverityCritical,
		SupervisionState:      SupervisionTimedOut,
	})
	require.NoError(t, err)

	// Backdate the queued row: the wake has been waiting well past the timeout.
	db, err := store.dbOrErr()
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, `UPDATE supervision_wake_pending SET created_at = ?`, formatSupervisionTime(now.Add(-30*time.Minute)))
	require.NoError(t, err)

	require.NoError(t, consumer.MaybeWakeParent(ctx, "root-session-queue", "", "root-session-queue"))
	require.Zero(t, delivered)

	// The escalation is the only notification whose subject is the parent
	// session itself (the lifecycle row targets the child run).
	escalationFilter := NotificationFilter{
		RootScopeID:          "root-session-queue",
		TargetParentSessionID: "root-session-queue",
		SubjectKind:          SubjectAgentSession,
		SubjectID:            "root-session-queue",
	}
	notes, err := store.ListNotifications(ctx, escalationFilter)
	require.NoError(t, err)
	require.Len(t, notes, 1, "the stalled queue must escalate as one durable notification")
	require.Equal(t, "resume.queue.escalated", notes[0].EventType)
	require.Equal(t, SeverityCritical, notes[0].Severity)
	require.True(t, notes[0].ActionRequired(), "the escalation must be action_required")
	require.Contains(t, notes[0].RecommendedAction, ResumeGateDepth)
	escalatedID := notes[0].NotificationID

	// A second drain must refresh the same row, not spam a second escalation,
	// and the wake must still be queued for the next runnable window.
	require.NoError(t, consumer.MaybeWakeParent(ctx, "root-session-queue", "", "root-session-queue"))
	notes, err = store.ListNotifications(ctx, escalationFilter)
	require.NoError(t, err)
	require.Len(t, notes, 1)
	require.Equal(t, escalatedID, notes[0].NotificationID)
	require.Greater(t, notes[0].Version, int64(1), "the refresh must bump the notification version")

	queued, err := store.ListWakePending(ctx, WakeFilter{RootScopeID: "root-session-queue", UnclaimedOnly: true})
	require.NoError(t, err)
	require.Len(t, queued, 1, "escalating must not consume the wake")
}

// TestWakeConsumer_CapacityProbeErrorFailsOpen pins the degradation rule: an
// unreadable limiter must never wedge supervision, so a probe error keeps the
// pre-A6 behavior (deliver).
func TestWakeConsumer_CapacityProbeErrorFailsOpen(t *testing.T) {
	store := newTestStore(t, "resume-capacity-error")
	ctx := context.Background()
	scheduler := NewWakeScheduler(store, unlimitedWakeConfig())

	delivered := 0
	consumer := &WakeConsumer{
		Wakes:    scheduler,
		Runnable: func(ctx context.Context, rootScopeID, parentSessionID, parentTeamID string) bool { return true },
		ResumeCapacity: ResumeCapacityProbeFunc(func(ctx context.Context, req ResumeCapacityRequest) (ResumeCapacityVerdict, error) {
			return ResumeCapacityVerdict{}, errors.New("limiter unavailable")
		}),
		Deliver: func(ctx context.Context, parentSessionID, rootScopeID string, digest *Digest, wakeIDs []string) error {
			delivered++
			return nil
		},
	}

	_, err := ProjectLifecycle(ctx, store, scheduler, LifecycleProjection{
		RootScopeID:           "root-session-open",
		TargetParentSessionID: "root-session-open",
		SubjectKind:           SubjectAgentRun,
		SubjectID:             "child-open",
		EventType:             "timeout",
		Severity:              SeverityCritical,
		SupervisionState:      SupervisionTimedOut,
	})
	require.NoError(t, err)

	require.NoError(t, consumer.MaybeWakeParent(ctx, "root-session-open", "", "root-session-open"))
	require.Equal(t, 1, delivered, "a probe error must fail open")
}

// TestSQLiteStore_ReleaseWakePending pins the claim-owner guard: only the owner
// may return a claim to the queue (A6), so a stale release cannot resurrect a
// row a concurrent drainer is about to deliver.
func TestSQLiteStore_ReleaseWakePending(t *testing.T) {
	store := newTestStore(t, "release-wake-pending")
	ctx := context.Background()
	now := time.Now().UTC()

	require.NoError(t, store.InsertWakePending(ctx, WakePending{
		WakeID:                "wake-release-1",
		RootScopeID:           "root-release",
		TargetParentSessionID: "root-release",
		WakeReason:            WakeReasonExecutionTimeout,
	}))
	ok, err := store.ClaimWakePending(ctx, "wake-release-1", WakeClaimOwner, now)
	require.NoError(t, err)
	require.True(t, ok)

	released, err := store.ReleaseWakePending(ctx, "wake-release-1", "some-other-owner")
	require.NoError(t, err)
	require.False(t, released, "a foreign owner must not un-claim the row")

	pending, err := store.ListWakePending(ctx, WakeFilter{RootScopeID: "root-release", UnclaimedOnly: true})
	require.NoError(t, err)
	require.Empty(t, pending)

	released, err = store.ReleaseWakePending(ctx, "wake-release-1", WakeClaimOwner)
	require.NoError(t, err)
	require.True(t, released)

	pending, err = store.ListWakePending(ctx, WakeFilter{RootScopeID: "root-release", UnclaimedOnly: true})
	require.NoError(t, err)
	require.Len(t, pending, 1, "the released wake must be queued again")
	require.Equal(t, "wake-release-1", pending[0].WakeID)
}

// TestWakeScheduler_QueuePosition pins the FIFO readout: positions follow
// created_at order and an unknown wake reports position 0 with the live depth.
func TestWakeScheduler_QueuePosition(t *testing.T) {
	store := newTestStore(t, "queue-position")
	ctx := context.Background()
	scheduler := NewWakeScheduler(store, unlimitedWakeConfig())

	for _, id := range []string{"wake-pos-1", "wake-pos-2", "wake-pos-3"} {
		require.NoError(t, store.InsertWakePending(ctx, WakePending{
			WakeID:                id,
			RootScopeID:           "root-pos",
			TargetParentSessionID: "root-pos",
			WakeReason:            WakeReasonExecutionTimeout,
			// Distinct dedup keys: same-scope wakes with the same reason
			// coalesce by design, and this test needs three queued rows.
			DedupKey: "dedup-" + id,
		}))
		time.Sleep(2 * time.Millisecond) // created_at ordering is the FIFO
	}

	pos, err := scheduler.QueuePosition(ctx, "root-pos", "root-pos", "", "wake-pos-2")
	require.NoError(t, err)
	require.Equal(t, 2, pos.Position)
	require.Equal(t, 3, pos.Total)

	pos, err = scheduler.QueuePosition(ctx, "root-pos", "root-pos", "", "wake-missing")
	require.NoError(t, err)
	require.Zero(t, pos.Position)
	require.Equal(t, 3, pos.Total)

	depth, err := scheduler.PendingDepth(ctx, "root-pos", "root-pos", "")
	require.NoError(t, err)
	require.Equal(t, 3, depth)
}

// TestNewSubagentCapacityProbe_TransientCapacityOnly pins the A6 gate boundary:
// the probe expresses **transient capacity** only (process-wide subagent slots
// are saturated). Static policy — delegationPolicy / shouldExposeSpawnSubagents
// — must NOT deny here: a report-only resume spawns nothing, so denying it would
// queue the wake forever and keep escalating (a behavior regression).
func TestNewSubagentCapacityProbe_TransientCapacityOnly(t *testing.T) {
	ctx := context.Background()
	req := ResumeCapacityRequest{TargetParentSessionID: "root-session"}

	require.Nil(t, NewSubagentCapacityProbe(nil), "nil view ⇒ nil probe（回滚开关：旧行为）")

	view := SubagentCapacityView{Limit: 2, InFlight: 0}
	probe := NewSubagentCapacityProbe(func() SubagentCapacityView { return view })
	require.NotNil(t, probe)

	verdict, err := probe.CanResume(ctx, req)
	require.NoError(t, err)
	require.True(t, verdict.Allowed, "空队列必须放行")

	view.InFlight = 1
	verdict, err = probe.CanResume(ctx, req)
	require.NoError(t, err)
	require.True(t, verdict.Allowed, "未占满必须放行")

	view.InFlight = 2
	verdict, err = probe.CanResume(ctx, req)
	require.NoError(t, err)
	require.False(t, verdict.Allowed, "槽位占满必须拒绝（保持 pending，不丢 wake）")
	require.Equal(t, ResumeGateConcurrency, verdict.Reason)
	require.Contains(t, verdict.Detail, "2/2")

	// 探测无状态：额度释放后同一条 wake 必须能重新投递。
	view.InFlight = 1
	verdict, err = probe.CanResume(ctx, req)
	require.NoError(t, err)
	require.True(t, verdict.Allowed)

	// 未配置上限（Limit<=0）即使 InFlight 很大也放行：旧行为/回滚开关。
	unlimited := NewSubagentCapacityProbe(func() SubagentCapacityView {
		return SubagentCapacityView{Limit: 0, InFlight: 99}
	})
	verdict, err = unlimited.CanResume(ctx, req)
	require.NoError(t, err)
	require.True(t, verdict.Allowed)
}
