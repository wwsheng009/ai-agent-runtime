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
