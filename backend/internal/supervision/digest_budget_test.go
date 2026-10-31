package supervision

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// Context budget (2026-10-31): a long-lived session kept an unacknowledged
// backlog of *completed* background jobs, and every preflight re-injected the
// same oldest slice until the injected prompt grew with the backlog instead of
// with the news (the real store held 663 unacknowledged job rows, with the
// digest re-sending the same 50 each turn). These tests pin the two guards:
// delivered bookkeeping-only rows become one-shot, and the injected text never
// exceeds the configured rune budget.

func jobNotification(subjectID, status string, seq int64) Notification {
	eventType, severity, state := BackgroundJobTerminalDisposition(status)
	return Notification{
		RootScopeID:           "root-session-1",
		TargetParentSessionID: "root-session-1",
		SubjectKind:           SubjectJob,
		SubjectID:             subjectID,
		SubjectVersion:        1,
		EventSeq:              seq,
		EventType:             eventType,
		Severity:              severity,
		SupervisionState:      state,
		Reason:                "background job " + subjectID + " " + status,
		DecisionState:         DecisionUnacknowledged,
		ResolutionState:       ResolutionUnresolved,
	}
}

// TestBuildDigest_WithholdsDeliveredBookkeepingRows pins the one-shot rule: a
// completed job (warning, no action beyond inspect/acknowledge/defer) stops
// reappearing once the parent has seen it, while critical rows and rows that
// still carry a real decision keep reappearing until acknowledged — and the
// suppressed row stays durable and keeps counting in the true-state counters.
func TestBuildDigest_WithholdsDeliveredBookkeepingRows(t *testing.T) {
	store := testDigestStore(t, "supervision-digest-repeat-guard")
	ctx := context.Background()

	rows := []Notification{
		jobNotification("job-repeat-done", "completed", 1),
		jobNotification("job-repeat-failed", "failed", 2),
		{
			RootScopeID:           "root-session-1",
			TargetParentSessionID: "root-session-1",
			SubjectKind:           SubjectAgentSession,
			SubjectID:             "child-approval",
			SubjectVersion:        1,
			EventSeq:              3,
			EventType:             EventAgentApprovalRequested,
			Severity:              SeverityWarning,
			SupervisionState:      SupervisionBlocked,
			Reason:                "child asks a question",
			DecisionState:         DecisionUnacknowledged,
			ResolutionState:       ResolutionUnresolved,
		},
	}
	for _, n := range rows {
		_, err := store.UpsertNotification(ctx, n)
		require.NoError(t, err)
	}

	req := DigestRequest{RootScopeID: "root-session-1", TargetParentSessionID: "root-session-1", Limit: 10}
	first, err := BuildDigest(ctx, store, req)
	require.NoError(t, err)
	require.Len(t, first.Items, 3)
	require.Zero(t, first.RepeatSuppressed)

	for _, item := range first.Items {
		require.NoError(t, MarkDigestItemDelivered(ctx, store, item, time.Now().UTC()))
	}

	second, err := BuildDigest(ctx, store, req)
	require.NoError(t, err)
	require.Equal(t, 1, second.RepeatSuppressed, "only the completed job is withheld")
	require.Len(t, second.Items, 2)

	kept := map[string]bool{}
	for _, item := range second.Items {
		kept[item.SubjectID] = true
	}
	require.False(t, kept["job-repeat-done"], "a seen completed job must not be re-injected")
	require.True(t, kept["job-repeat-failed"], "critical rows keep reappearing")
	require.True(t, kept["child-approval"], "rows with a real decision keep reappearing")
	require.Equal(t, 3, second.ActionRequired, "counters keep reporting the true unresolved state")

	durable, err := store.ListNotifications(ctx, NotificationFilter{
		RootScopeID:           "root-session-1",
		TargetParentSessionID: "root-session-1",
	})
	require.NoError(t, err)
	require.Len(t, durable, 3, "suppression withholds the injection, never the durable row")
}

// TestBuildDigest_TextStaysWithinCharBudget pins the hard context budget: the
// injected text never exceeds MaxChars (or the shipped default when the request
// leaves it empty), the highest-priority rows survive the trim, and the store
// keeps every durable row for the tools to read.
func TestBuildDigest_TextStaysWithinCharBudget(t *testing.T) {
	store := testDigestStore(t, "supervision-digest-char-budget")
	ctx := context.Background()

	const total = 25
	for i := 1; i <= total; i++ {
		n := testNotification(fmt.Sprintf("agent-budget-%02d", i), int64(i))
		n.Reason = strings.Repeat("failure narrative ", 20)
		_, err := store.UpsertNotification(ctx, n)
		require.NoError(t, err)
	}

	digest, err := BuildDigest(ctx, store, DigestRequest{
		RootScopeID:           "root-session-1",
		TargetParentSessionID: "root-session-1",
		Limit:                 total,
		MaxChars:              1200,
	})
	require.NoError(t, err)
	require.True(t, digest.Truncated)
	require.NotEmpty(t, digest.Items)
	require.LessOrEqual(t, len([]rune(digest.Text)), 1200)
	require.Equal(t, int64(1), digest.Items[0].EventSeq, "the highest-priority head rows survive the trim")

	wide, err := BuildDigest(ctx, store, DigestRequest{
		RootScopeID:           "root-session-1",
		TargetParentSessionID: "root-session-1",
		Limit:                 total,
	})
	require.NoError(t, err)
	require.LessOrEqual(t, len([]rune(wide.Text)), defaultDigestMaxChars,
		"an empty MaxChars keeps the shipped default budget")
	require.True(t, wide.Truncated)

	durable, err := store.ListNotifications(ctx, NotificationFilter{
		RootScopeID:           "root-session-1",
		TargetParentSessionID: "root-session-1",
	})
	require.NoError(t, err)
	require.Len(t, durable, total, "the budget trims the injection, never the durable rows")
}

// TestBuildDigest_DeferredRowKeepsReappearing pins the defer carve-out: an
// explicitly deferred row keeps its "show me later" re-entry contract after
// the deadline (delivery marking is monotonic, so the one-shot rule cannot
// track a re-show), while an ordinary delivered sibling is suppressed.
func TestBuildDigest_DeferredRowKeepsReappearing(t *testing.T) {
	store := testDigestStore(t, "supervision-digest-defer-reentry")
	ctx := context.Background()
	now := time.Now().UTC()

	created, err := store.UpsertNotification(ctx, jobNotification("job-defer-once", "completed", 1))
	require.NoError(t, err)
	sibling, err := store.UpsertNotification(ctx, jobNotification("job-sibling-silent", "completed", 2))
	require.NoError(t, err)

	// The deferred row was delivered + seen before its (now past) deadline.
	require.NoError(t, store.MarkNotificationDelivered(ctx, created.NotificationID, now.Add(-10*time.Minute)))
	require.NoError(t, store.MarkNotificationSeen(ctx, created.NotificationID, now.Add(-10*time.Minute)))
	current, err := store.GetNotification(ctx, created.NotificationID)
	require.NoError(t, err)
	ok, err := store.DeferNotification(ctx, created.NotificationID, now.Add(-5*time.Minute), "check later", current.Version)
	require.NoError(t, err)
	require.True(t, ok)
	require.NoError(t, store.MarkNotificationDelivered(ctx, sibling.NotificationID, now.Add(-10*time.Minute)))
	require.NoError(t, store.MarkNotificationSeen(ctx, sibling.NotificationID, now.Add(-10*time.Minute)))

	req := DigestRequest{RootScopeID: "root-session-1", TargetParentSessionID: "root-session-1", Limit: 10}
	reentry, err := BuildDigest(ctx, store, req)
	require.NoError(t, err)
	require.Len(t, reentry.Items, 1, "the deferred row re-enters after its deadline")
	require.Equal(t, "job-defer-once", reentry.Items[0].SubjectID)
	require.Equal(t, 1, reentry.RepeatSuppressed, "the ordinary delivered sibling stays silent")

	require.NoError(t, MarkDigestItemDelivered(ctx, store, reentry.Items[0], now))

	after, err := BuildDigest(ctx, store, req)
	require.NoError(t, err)
	require.Len(t, after.Items, 1, "the defer exemption keeps the row visible until acknowledged")
	require.Equal(t, "job-defer-once", after.Items[0].SubjectID)
	require.Equal(t, 1, after.RepeatSuppressed)
}
