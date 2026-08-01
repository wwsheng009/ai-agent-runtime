package supervision

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type fakeDescendantProvider struct {
	items []DescendantState
}

func (f *fakeDescendantProvider) ListDescendants(ctx context.Context, scope Scope) ([]DescendantState, error) {
	return f.items, nil
}

// TestBuildSnapshot_RollupAndAutoAction verifies the status rollup and the
// auto-action surfacing (doc 6.2 rule 6): a timed-out descendant with a
// runtime action in flight must show the action and forbid a duplicate.
func TestBuildSnapshot_RollupAndAutoAction(t *testing.T) {
	store := newTestStore(t, "supervision-snapshot-rollup")
	ctx := context.Background()
	now := time.Now().UTC()

	// Descendant 1: timed out with an in-flight runtime cancel.
	timeout := testNotification("child-timeout", 10)
	timeout.SupervisionState = SupervisionCanceling
	timeout.AutoActionID = "act_runtime_1"
	timeout.RecommendedAction = "cancel"
	_, err := store.UpsertNotification(ctx, timeout)
	require.NoError(t, err)

	// The runtime's in-flight action is a durable record too.
	_, err = store.CreateAction(ctx, ActionRecord{
		ActionID:    "act_runtime_1",
		RootScopeID: "root-session-1",
		TargetKind:  SubjectAgentRun,
		TargetID:    "child-timeout",
		Action:      ActionCancel,
		Status:      ActionRequested,
		CreatedAt:   now,
		Version:     1,
	})
	require.NoError(t, err)

	// Descendant 2: stalled (no notification yet).
	// Descendant 3: healthy running.
	provider := &fakeDescendantProvider{items: []DescendantState{
		{
			Kind:             SubjectAgentRun,
			ID:               "child-timeout",
			ExecutionStatus:  "running",
			SupervisionState: SupervisionCanceling,
			HeartbeatAgeMs:   30000,
			Reason:           "execution deadline exceeded",
		},
		{
			Kind:             SubjectAgentRun,
			ID:               "child-stalled",
			ExecutionStatus:  "running",
			SupervisionState: SupervisionStalled,
			HeartbeatAgeMs:   120000,
			Reason:           "no progress",
		},
		{
			Kind:             SubjectAgentRun,
			ID:               "child-healthy",
			ExecutionStatus:  "running",
			SupervisionState: SupervisionRunning,
			HeartbeatAgeMs:   500,
		},
	}}

	snapshot, err := BuildSnapshot(ctx, store, SnapshotRequest{
		Scope:    Scope{RootSessionID: "root-session-1"},
		Provider: provider,
	})
	require.NoError(t, err)
	require.Equal(t, 1, snapshot.Summary.Canceling)
	require.Equal(t, 1, snapshot.Summary.Stalled)
	require.Equal(t, 1, snapshot.Summary.Running)
	require.Equal(t, 0, snapshot.Summary.ActionRequired,
		"runtime action already in flight: parent must not act, only acknowledge")

	// Locate the timeout item and verify auto-action surfacing.
	var timeoutItem *SnapshotItem
	for i := range snapshot.Descendants {
		if snapshot.Descendants[i].ID == "child-timeout" {
			timeoutItem = &snapshot.Descendants[i]
		}
	}
	require.NotNil(t, timeoutItem)
	require.NotNil(t, timeoutItem.AutoAction)
	require.Equal(t, ActionCancel, timeoutItem.AutoAction.Action)
	require.Equal(t, ActionRequested, timeoutItem.AutoAction.Status)
	require.Equal(t, "act_runtime_1", timeoutItem.AutoAction.ActionID)
	require.False(t, AllowedAction(timeoutItem.AllowedActions, ActionCancel),
		"duplicate cancel must not be allowed while runtime action is in flight")
	require.True(t, AllowedAction(timeoutItem.AllowedActions, ActionAcknowledge))
	require.NotEmpty(t, timeoutItem.NotificationID)
}

// TestBuildSnapshot_HealthFilter verifies the abnormal filter keeps only
// abnormal/action-required rows.
func TestBuildSnapshot_HealthFilter(t *testing.T) {
	store := newTestStore(t, "supervision-snapshot-health")
	ctx := context.Background()

	// Only child-1 has a durable notification; child-2 is running and
	// child-3 is stalled without any notification yet.
	_, err := store.UpsertNotification(ctx, testNotification("child-1", 1))
	require.NoError(t, err)

	provider := &fakeDescendantProvider{items: []DescendantState{
		{Kind: SubjectAgentRun, ID: "child-1", SupervisionState: SupervisionTimedOut},
		{Kind: SubjectAgentRun, ID: "child-2", SupervisionState: SupervisionRunning},
		{Kind: SubjectAgentRun, ID: "child-3", SupervisionState: SupervisionStalled},
	}}

	snapshot, err := BuildSnapshot(ctx, store, SnapshotRequest{
		Scope:    Scope{RootSessionID: "root-session-1"},
		Provider: provider,
		Health:   "abnormal",
	})
	require.NoError(t, err)
	require.Len(t, snapshot.Descendants, 2)
	for _, item := range snapshot.Descendants {
		require.True(t, itemAbnormal(item))
	}
}

// TestBuildSnapshot_NotificationOnlySubject verifies subjects that disappeared
// from runtime state still surface through their durable notification.
func TestBuildSnapshot_NotificationOnlySubject(t *testing.T) {
	store := newTestStore(t, "supervision-snapshot-notif-only")
	ctx := context.Background()

	_, err := store.UpsertNotification(ctx, testNotification("child-gone", 7))
	require.NoError(t, err)

	// Provider knows nothing about child-gone.
	snapshot, err := BuildSnapshot(ctx, store, SnapshotRequest{
		Scope:    Scope{RootSessionID: "root-session-1"},
		Provider: &fakeDescendantProvider{},
	})
	require.NoError(t, err)
	require.Len(t, snapshot.Descendants, 1)
	require.Equal(t, "child-gone", snapshot.Descendants[0].ID)
	require.Equal(t, SupervisionTimedOut, snapshot.Descendants[0].SupervisionState)
	require.True(t, snapshot.Descendants[0].ActionRequired)
	require.Equal(t, int64(7), snapshot.LastChangeSeqOf("child-gone"))
}

// TestBuildSnapshot_Truncation verifies the limit and truncated flag.
func TestBuildSnapshot_Truncation(t *testing.T) {
	store := newTestStore(t, "supervision-snapshot-truncate")
	ctx := context.Background()

	for i := 0; i < 3; i++ {
		_, err := store.UpsertNotification(ctx, testNotification("child-t-"+string(rune('a'+i)), int64(i+1)))
		require.NoError(t, err)
	}
	provider := &fakeDescendantProvider{items: []DescendantState{
		{Kind: SubjectAgentRun, ID: "child-t-a", SupervisionState: SupervisionTimedOut},
		{Kind: SubjectAgentRun, ID: "child-t-b", SupervisionState: SupervisionTimedOut},
		{Kind: SubjectAgentRun, ID: "child-t-c", SupervisionState: SupervisionTimedOut},
	}}

	snapshot, err := BuildSnapshot(ctx, store, SnapshotRequest{
		Scope:    Scope{RootSessionID: "root-session-1"},
		Provider: provider,
		Limit:    2,
	})
	require.NoError(t, err)
	require.True(t, snapshot.Truncated)
	require.Len(t, snapshot.Descendants, 2)
	require.Equal(t, 3, snapshot.Summary.ActionRequired)
}

// LastChangeSeqOf returns the last_change_seq for a subject (test helper).
func (s *Snapshot) LastChangeSeqOf(id string) int64 {
	for _, item := range s.Descendants {
		if item.ID == id {
			return item.LastChangeSeq
		}
	}
	return 0
}

func TestSnapshot_Clock(t *testing.T) {
	// Guard: timeNow must be the production clock; tests that need a fake
	// clock override it via the package variable.
	before := timeNow()
	time.Sleep(2 * time.Millisecond)
	after := timeNow()
	require.False(t, after.Before(before))
}

// TestBuildSnapshot_AttachesExecutionRunFields verifies P6-3: snapshot items
// for agent sessions carry the durable execution run supervision fields
// (run id/status, attempt, deadlines, heartbeat and progress timestamps) so
// operators can judge health from a single view.
func TestBuildSnapshot_AttachesExecutionRunFields(t *testing.T) {
	store := newTestStore(t, "supervision-snapshot-run-fields")
	ctx := context.Background()
	now := time.Now().UTC()

	created, err := store.CreateExecutionRun(ctx, ExecutionRun{
		RunID:               "run_snap_1",
		Kind:                RunKindAgentRun,
		Workflow:            RunWorkflowSpawnAgent,
		SessionID:           "child-run-snap",
		AgentID:             "child-run-snap",
		Attempt:             2,
		MaxAttempts:         3,
		Status:              RunStatusRunning,
		OwnerID:             "owner-1",
		StartedAt:           now,
		LastHeartbeatAt:     now.Add(-5 * time.Second),
		LastProgressAt:      now.Add(-30 * time.Second),
		ExecutionDeadlineAt: timePtr(now.Add(60 * time.Second)),
		ProgressDeadlineAt:  timePtr(now.Add(45 * time.Second)),
		ApprovalDeadlineAt:  timePtr(now.Add(120 * time.Second)),
		CreatedAt:           now,
		UpdatedAt:           now,
	})
	require.NoError(t, err)
	require.True(t, created)

	provider := &fakeDescendantProvider{items: []DescendantState{
		{
			Kind:               SubjectAgentSession,
			ID:                 "child-run-snap",
			ExecutionStatus:    "running",
			SupervisionState:   SupervisionRunning,
			HeartbeatAgeMs:     5000,
			ProgressAgeMs:      30000,
			ExecutionDeadlineAt: timePtr(now.Add(60 * time.Second)),
		},
	}}

	snapshot, err := BuildSnapshot(ctx, store, SnapshotRequest{
		Scope:    Scope{RootSessionID: "root-session-1"},
		Provider: provider,
	})
	require.NoError(t, err)
	require.Len(t, snapshot.Descendants, 1)

	item := snapshot.Descendants[0]
	require.Equal(t, "run_snap_1", item.RunID)
	require.Equal(t, RunStatusRunning, item.RunStatus)
	require.Equal(t, 2, item.Attempt)
	require.Equal(t, 3, item.MaxAttempts)
	require.Equal(t, "owner-1", item.RunOwnerID)
	require.NotNil(t, item.ProgressDeadlineAt)
	require.NotNil(t, item.ApprovalDeadlineAt)
	require.NotNil(t, item.ExecutionDeadlineAt)
	require.NotNil(t, item.LastHeartbeatAt)
	require.NotNil(t, item.LastProgressAt)
	require.Equal(t, int64(5000), item.HeartbeatAgeMs)
}

func timePtr(t time.Time) *time.Time {
	return &t
}

// TestBuildSnapshot_AfterSeqCursor verifies the durable sequence cursor
// (doc 6.2 / P6-5): SnapshotSeq is the high-water mark of observed event
// sequences, NextSeq echoes it for catch-up, and the cursor never moves
// backwards below AfterSeq so a watcher can resume losslessly.
func TestBuildSnapshot_AfterSeqCursor(t *testing.T) {
	store := newTestStore(t, "supervision-snapshot-cursor")
	ctx := context.Background()

	old := testNotification("child-old", 10)
	old.SupervisionState = SupervisionTimedOut
	_, err := store.UpsertNotification(ctx, old)
	require.NoError(t, err)

	recent := testNotification("child-recent", 20)
	recent.SupervisionState = SupervisionTimedOut
	_, err = store.UpsertNotification(ctx, recent)
	require.NoError(t, err)

	provider := &fakeDescendantProvider{items: []DescendantState{
		{Kind: SubjectAgentRun, ID: "child-old", ExecutionStatus: "running", SupervisionState: SupervisionTimedOut},
		{Kind: SubjectAgentRun, ID: "child-recent", ExecutionStatus: "running", SupervisionState: SupervisionTimedOut},
	}}

	// Watcher already saw seq 15: high-water must still advance to 20.
	snapshot, err := BuildSnapshot(ctx, store, SnapshotRequest{
		Scope:    Scope{RootSessionID: "root-session-1"},
		AfterSeq: 15,
		Provider: provider,
	})
	require.NoError(t, err)
	require.Equal(t, int64(20), snapshot.SnapshotSeq)
	require.Equal(t, int64(20), snapshot.NextSeq)

	// Watcher ahead of the store: cursor must not move backwards.
	ahead, err := BuildSnapshot(ctx, store, SnapshotRequest{
		Scope:    Scope{RootSessionID: "root-session-1"},
		AfterSeq: 25,
		Provider: provider,
	})
	require.NoError(t, err)
	require.Equal(t, int64(25), ahead.SnapshotSeq)
	require.Equal(t, int64(25), ahead.NextSeq)
}
