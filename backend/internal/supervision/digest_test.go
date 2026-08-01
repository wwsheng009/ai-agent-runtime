package supervision

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func testDigestStore(t *testing.T, name string) *SQLiteSupervisionStore {
	t.Helper()
	return newTestStore(t, name)
}

// TestBuildDigest_AggregatesCriticalChildren verifies doc 6.4: multiple
// children failing at once produce a single aggregated digest, and the text
// block lists every subject.
func TestBuildDigest_AggregatesCriticalChildren(t *testing.T) {
	store := testDigestStore(t, "supervision-digest-aggregate")
	ctx := context.Background()

	for i, subject := range []string{"child-1", "child-2", "child-3"} {
		n := testNotification(subject, int64(i+1))
		_, err := store.UpsertNotification(ctx, n)
		require.NoError(t, err)
	}

	digest, err := BuildDigest(ctx, store, DigestRequest{
		RootScopeID:           "root-session-1",
		TargetParentSessionID: "root-session-1",
	})
	require.NoError(t, err)
	require.Equal(t, 3, digest.CriticalUnresolved)
	require.Equal(t, 3, digest.ActionRequired)
	require.Len(t, digest.Items, 3)
	require.Equal(t, int64(3), digest.NextSeq)
	require.False(t, digest.Truncated)
	require.Contains(t, digest.Text, "child-1")
	require.Contains(t, digest.Text, "child-3")
	require.Contains(t, digest.Text, "recommended=cancel")
}

// TestBuildDigest_AfterSeqCursor verifies the preflight cursor: items with
// event_seq <= after_seq are excluded.
func TestBuildDigest_AfterSeqCursor(t *testing.T) {
	store := testDigestStore(t, "supervision-digest-cursor")
	ctx := context.Background()

	_, err := store.UpsertNotification(ctx, testNotification("child-1", 5))
	require.NoError(t, err)

	digest, err := BuildDigest(ctx, store, DigestRequest{
		RootScopeID:           "root-session-1",
		TargetParentSessionID: "root-session-1",
		AfterSeq:              5,
	})
	require.NoError(t, err)
	require.Len(t, digest.Items, 0, "nothing newer than the cursor")
	require.Equal(t, 0, digest.ActionRequired)

	digest, err = BuildDigest(ctx, store, DigestRequest{
		RootScopeID:           "root-session-1",
		TargetParentSessionID: "root-session-1",
		AfterSeq:              4,
	})
	require.NoError(t, err)
	require.Len(t, digest.Items, 1)
	require.Equal(t, int64(5), digest.NextSeq)
}

// TestBuildDigest_AckAndResolve verifies ack removes the item from the
// action-required set and resolve moves it to the resolved section.
func TestBuildDigest_AckAndResolve(t *testing.T) {
	store := testDigestStore(t, "supervision-digest-ack")
	ctx := context.Background()
	now := time.Now().UTC()

	acked, err := store.UpsertNotification(ctx, testNotification("child-ack", 10))
	require.NoError(t, err)
	ok, err := store.AcknowledgeNotification(ctx, acked.NotificationID, now, acked.Version)
	require.NoError(t, err)
	require.True(t, ok)

	resolved, err := store.UpsertNotification(ctx, testNotification("child-resolved", 11))
	require.NoError(t, err)
	ok, err = store.ResolveNotification(ctx, resolved.NotificationID, ResolutionRecovered, now, resolved.Version)
	require.NoError(t, err)
	require.True(t, ok)

	// Default digest: both drop out of action-required.
	digest, err := BuildDigest(ctx, store, DigestRequest{
		RootScopeID:           "root-session-1",
		TargetParentSessionID: "root-session-1",
	})
	require.NoError(t, err)
	require.Equal(t, 0, digest.ActionRequired)
	require.Equal(t, 0, digest.CriticalUnresolved)

	// With resolved-since: the resolved item is summarized compactly.
	digest, err = BuildDigest(ctx, store, DigestRequest{
		RootScopeID:           "root-session-1",
		TargetParentSessionID: "root-session-1",
		AfterSeq:              0,
		IncludeResolvedSince:  true,
	})
	require.NoError(t, err)
	require.Equal(t, 1, digest.ResolvedSinceLastTurn)
	require.Contains(t, digest.Text, "child-resolved: recovered; resolved")
}

// TestBuildDigest_DeferReentry verifies a deferred-past item re-enters the
// action-required digest while a deferred-not-due item stays out.
func TestBuildDigest_DeferReentry(t *testing.T) {
	store := testDigestStore(t, "supervision-digest-defer")
	ctx := context.Background()
	now := time.Now().UTC()

	future := now.Add(24 * time.Hour)
	n, err := store.UpsertNotification(ctx, testNotification("child-defer-future", 20))
	require.NoError(t, err)
	ok, err := store.DeferNotification(ctx, n.NotificationID, future, "later", n.Version)
	require.NoError(t, err)
	require.True(t, ok)

	digest, err := BuildDigest(ctx, store, DigestRequest{
		RootScopeID:           "root-session-1",
		TargetParentSessionID: "root-session-1",
	})
	require.NoError(t, err)
	require.Equal(t, 0, digest.ActionRequired, "deferred-not-due must not require action")
	require.Equal(t, 0, digest.CriticalUnresolved,
		"deferred-not-due must be absent from the ordinary preflight digest")
	require.Empty(t, digest.Items, "deferred-not-due must not be re-injected before its deadline")

	// Re-defer into the past: now due.
	got, err := store.GetNotification(ctx, n.NotificationID)
	require.NoError(t, err)
	ok, err = store.DeferNotification(ctx, got.NotificationID, now.Add(-time.Hour), "re-check", got.Version)
	require.NoError(t, err)
	require.True(t, ok)

	digest, err = BuildDigest(ctx, store, DigestRequest{
		RootScopeID:           "root-session-1",
		TargetParentSessionID: "root-session-1",
	})
	require.NoError(t, err)
	require.Equal(t, 1, digest.ActionRequired, "deferred-past must re-enter the digest")
	require.Contains(t, digest.Text, "child-defer-future")
}

// TestBuildDigest_Truncation verifies the budget cap plus cursor for full
// details (doc 6.4 rule 6: critical unresolved items always survive the
// budget).
func TestBuildDigest_Truncation(t *testing.T) {
	store := testDigestStore(t, "supervision-digest-truncate")
	ctx := context.Background()

	for i := 0; i < 5; i++ {
		_, err := store.UpsertNotification(ctx, testNotification("child-trunc-"+strings.Repeat("a", i+1), int64(i+1)))
		require.NoError(t, err)
	}

	digest, err := BuildDigest(ctx, store, DigestRequest{
		RootScopeID:           "root-session-1",
		TargetParentSessionID: "root-session-1",
		Limit:                 2,
	})
	require.NoError(t, err)
	require.True(t, digest.Truncated)
	require.Len(t, digest.Items, 2)
	require.Equal(t, int64(5), digest.NextSeq)
	require.Equal(t, 5, digest.ActionRequired, "counters stay accurate under truncation")
	require.Contains(t, digest.Text, "use supervision_snapshot")
}

// TestProjectAgentCompletion_FailedChildAppearsWithoutExplicitWait verifies
// the host lifecycle bridge writes the parent preflight inbox directly. The
// next parent turn can therefore observe a failed child without first calling
// a wait/read API.
func TestProjectAgentCompletion_FailedChildAppearsWithoutExplicitWait(t *testing.T) {
	store := testDigestStore(t, "supervision-project-agent-failure")
	ctx := context.Background()

	projected, err := ProjectAgentCompletion(
		ctx,
		store,
		nil,
		"parent-session",
		"parent-session",
		"child-session",
		"failed",
		"session_end",
	)
	require.NoError(t, err)
	require.NotEmpty(t, projected.NotificationID)
	require.Equal(t, SeverityCritical, projected.Severity)
	require.Equal(t, int64(1), projected.EventSeq)

	digest, err := BuildDigest(ctx, store, DigestRequest{
		RootScopeID:           "parent-session",
		TargetParentSessionID: "parent-session",
	})
	require.NoError(t, err)
	require.Equal(t, 1, digest.ActionRequired)
	require.Len(t, digest.Items, 1)
	require.Equal(t, "child-session", digest.Items[0].SubjectID)
	require.Contains(t, digest.Text, "child-session")
}

func TestProjectAgentCompletion_AllocatesRootScopedCursor(t *testing.T) {
	store := testDigestStore(t, "supervision-project-agent-cursor")
	ctx := context.Background()

	first, err := ProjectAgentCompletion(ctx, store, nil, "parent-session", "parent-session", "child-one", "failed", "session_end")
	require.NoError(t, err)
	second, err := ProjectAgentCompletion(ctx, store, nil, "parent-session", "parent-session", "child-two", "failed", "session_end")
	require.NoError(t, err)

	require.Equal(t, int64(1), first.EventSeq)
	require.Equal(t, int64(2), second.EventSeq)
}
