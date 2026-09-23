package supervision

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestBuildDigest_ResumeQueuePosition pins AC-P1-5a's "digest 带位次" half: the
// parent's queued resumes are visible in the preflight digest with the FIFO
// position, scoped to that parent, and the line disappears the moment the queue
// drains.
func TestBuildDigest_ResumeQueuePosition(t *testing.T) {
	store := newTestStore(t, "digest-resume-queue")
	ctx := context.Background()

	for _, id := range []string{"wake-queue-1", "wake-queue-2"} {
		require.NoError(t, store.InsertWakePending(ctx, WakePending{
			WakeID:                id,
			RootScopeID:           "root-digest-queue",
			TargetParentSessionID: "parent-digest-queue",
			WakeReason:            WakeReasonExecutionTimeout,
			// Distinct dedup keys: same-scope wakes with the same reason
			// coalesce by design, and this test needs two queued rows.
			DedupKey: "dedup-" + id,
		}))
	}
	// A third row for another parent must not leak into this parent's digest.
	require.NoError(t, store.InsertWakePending(ctx, WakePending{
		WakeID:                "wake-queue-other",
		RootScopeID:           "root-digest-queue",
		TargetParentSessionID: "parent-other",
		WakeReason:            WakeReasonExecutionTimeout,
		DedupKey:              "dedup-wake-queue-other",
	}))

	digest, err := BuildDigest(ctx, store, DigestRequest{
		RootScopeID:           "root-digest-queue",
		TargetParentSessionID: "parent-digest-queue",
	})
	require.NoError(t, err)
	require.NotNil(t, digest.ResumeQueue)
	require.Equal(t, 2, digest.ResumeQueue.Pending)
	require.Equal(t, 1, digest.ResumeQueue.HeadPosition)
	require.Contains(t, digest.Text, "resume_queue: 排队中 2 条，位次 1/2")

	other, err := BuildDigest(ctx, store, DigestRequest{
		RootScopeID:           "root-digest-queue",
		TargetParentSessionID: "parent-other",
	})
	require.NoError(t, err)
	require.NotNil(t, other.ResumeQueue)
	require.Equal(t, 1, other.ResumeQueue.Pending)

	// A scope-level read has no target parent: no queue line at all.
	scope, err := BuildDigest(ctx, store, DigestRequest{RootScopeID: "root-digest-queue"})
	require.NoError(t, err)
	require.Nil(t, scope.ResumeQueue)

	// Draining the queue removes the line: the summary is computed live from
	// the durable rows, so it can never report a resume that already ran.
	require.NoError(t, store.ResolveWakePending(ctx, "wake-queue-1"))
	require.NoError(t, store.ResolveWakePending(ctx, "wake-queue-2"))
	drained, err := BuildDigest(ctx, store, DigestRequest{
		RootScopeID:           "root-digest-queue",
		TargetParentSessionID: "parent-digest-queue",
	})
	require.NoError(t, err)
	require.Nil(t, drained.ResumeQueue)
	require.NotContains(t, drained.Text, "resume_queue")
}

// TestFormatDigestText_ResumeQueueWaited pins the age half of the queue line.
// It is asserted through formatDigestText because the store assigns created_at
// at insert time, so a queued row cannot be backdated in an integration test.
func TestFormatDigestText_ResumeQueueWaited(t *testing.T) {
	text := formatDigestText(&Digest{
		ResumeQueue: &ResumeQueueSummary{Pending: 3, HeadPosition: 1, OldestWaitedSeconds: 90},
	})
	require.Contains(t, text, "resume_queue: 排队中 3 条，位次 1/3")
	require.Contains(t, text, "最早已等待 1m30s")

	require.NotContains(t, formatDigestText(&Digest{}), "resume_queue")
}

// TestWakeConsumer_DeferredResumeIsVisibleInDigest is the end-to-end A6
// assertion: a resume denied by the capacity gate stays queued (never dropped)
// and the parent's own digest shows the queue position; the line disappears
// once the resume actually starts.
func TestWakeConsumer_DeferredResumeIsVisibleInDigest(t *testing.T) {
	store := newTestStore(t, "digest-deferred-resume")
	ctx := context.Background()
	scheduler := NewWakeScheduler(store, unlimitedWakeConfig())

	allowed := false
	delivered := 0
	consumer := &WakeConsumer{
		Wakes:    scheduler,
		Runnable: func(ctx context.Context, rootScopeID, parentSessionID, parentTeamID string) bool { return true },
		ResumeCapacity: ResumeCapacityProbeFunc(func(ctx context.Context, req ResumeCapacityRequest) (ResumeCapacityVerdict, error) {
			if allowed {
				return ResumeCapacityVerdict{Allowed: true}, nil
			}
			return ResumeCapacityVerdict{
				Allowed: false,
				Reason:  ResumeGateConcurrency,
				Detail:  "subagent slots exhausted",
			}, nil
		}),
		Deliver: func(ctx context.Context, parentSessionID, rootScopeID string, digest *Digest, wakeIDs []string) error {
			delivered++
			return nil
		},
	}

	_, err := ProjectLifecycle(ctx, store, scheduler, LifecycleProjection{
		RootScopeID:           "root-deferred-digest",
		TargetParentSessionID: "root-deferred-digest",
		SubjectKind:           SubjectAgentRun,
		SubjectID:             "child-deferred-digest",
		EventType:             "timeout",
		Severity:              SeverityCritical,
		SupervisionState:      SupervisionTimedOut,
	})
	require.NoError(t, err)

	require.NoError(t, consumer.MaybeWakeParent(ctx, "root-deferred-digest", "", "root-deferred-digest"))
	require.Zero(t, delivered, "the denied resume must not start a turn")

	digest, err := BuildDigest(ctx, store, DigestRequest{
		RootScopeID:           "root-deferred-digest",
		TargetParentSessionID: "root-deferred-digest",
	})
	require.NoError(t, err)
	require.NotNil(t, digest.ResumeQueue, "the queued resume must be visible to the parent")
	require.Equal(t, 1, digest.ResumeQueue.Pending)
	require.Contains(t, digest.Text, "位次 1/1")

	pending, err := store.ListWakePending(ctx, WakeFilter{RootScopeID: "root-deferred-digest", UnclaimedOnly: true})
	require.NoError(t, err)
	require.Len(t, pending, 1, "a denied resume must stay queued, never dropped")

	allowed = true
	require.NoError(t, consumer.MaybeWakeParent(ctx, "root-deferred-digest", "", "root-deferred-digest"))
	require.Equal(t, 1, delivered)

	drained, err := BuildDigest(ctx, store, DigestRequest{
		RootScopeID:           "root-deferred-digest",
		TargetParentSessionID: "root-deferred-digest",
	})
	require.NoError(t, err)
	require.Nil(t, drained.ResumeQueue, "a delivered resume leaves the queue")
}
