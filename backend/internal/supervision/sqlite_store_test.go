package supervision

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func newTestStore(t *testing.T, name string) *SQLiteSupervisionStore {
	t.Helper()
	store, err := NewSQLiteSupervisionStore(&StoreConfig{
		DSN: "file:" + name + "?mode=memory&cache=shared",
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func testNotification(subjectID string, seq int64) Notification {
	return Notification{
		RootScopeID:           "root-session-1",
		TargetParentSessionID: "root-session-1",
		SubjectKind:           SubjectAgentRun,
		SubjectID:             subjectID,
		SubjectVersion:        1,
		EventSeq:              seq,
		EventType:             "timeout",
		Severity:              SeverityCritical,
		SupervisionState:      SupervisionTimedOut,
		Reason:                "execution deadline exceeded",
		DecisionState:         DecisionUnacknowledged,
		ResolutionState:       ResolutionUnresolved,
	}
}

// TestSQLiteStore_UpsertNotification_IdempotentKey verifies doc 6.3 rule 2:
// the same idempotency key (root_scope + subject + version + event_type)
// refreshes the same row instead of creating a duplicate.
func TestSQLiteStore_UpsertNotification_IdempotentKey(t *testing.T) {
	store := newTestStore(t, "supervision-upsert-idem")
	ctx := context.Background()

	first, err := store.UpsertNotification(ctx, testNotification("agent-1", 10))
	require.NoError(t, err)
	require.NotEmpty(t, first.NotificationID)
	require.Equal(t, int64(1), first.Version)

	// Same subject/version/event_type with a newer seq and state.
	updated := testNotification("agent-1", 11)
	updated.Reason = "deadline exceeded again"
	updated.SupervisionState = SupervisionOrphaned
	second, err := store.UpsertNotification(ctx, updated)
	require.NoError(t, err)
	require.NoError(t, err)
	require.Equal(t, first.NotificationID, second.NotificationID, "idempotency key must reuse the same row")
	require.Equal(t, int64(2), second.Version)
	require.Equal(t, int64(11), second.EventSeq)
	require.Equal(t, SupervisionOrphaned, second.SupervisionState)

	// A different subject gets its own row.
	other, err := store.UpsertNotification(ctx, testNotification("agent-2", 10))
	require.NoError(t, err)
	require.NotEqual(t, first.NotificationID, other.NotificationID)
}

// TestSQLiteStore_Notification_SeenDeliveredAck covers the delivery and
// decision sequence: delivered -> seen -> acknowledged (CAS), and that acked
// items leave the unresolved set.
func TestSQLiteStore_Notification_SeenDeliveredAck(t *testing.T) {
	store := newTestStore(t, "supervision-ack-seq")
	ctx := context.Background()
	now := time.Now().UTC()

	n, err := store.UpsertNotification(ctx, testNotification("agent-ack", 20))
	require.NoError(t, err)

	require.NoError(t, store.MarkNotificationDelivered(ctx, n.NotificationID, now))
	seen, err := store.GetNotification(ctx, n.NotificationID)
	require.NoError(t, err)
	require.Equal(t, DeliveryDelivered, seen.DeliveryState)
	require.NotNil(t, seen.DeliveredAt)

	require.NoError(t, store.MarkNotificationSeen(ctx, n.NotificationID, now))
	seen, err = store.GetNotification(ctx, n.NotificationID)
	require.NoError(t, err)
	require.Equal(t, DeliverySeen, seen.DeliveryState)

	// Stale CAS must fail.
	ok, err := store.AcknowledgeNotification(ctx, n.NotificationID, now, 99)
	require.NoError(t, err)
	require.False(t, ok, "stale version must not ack")

	ok, err = store.AcknowledgeNotification(ctx, n.NotificationID, now, seen.Version)
	require.NoError(t, err)
	require.True(t, ok)

	acked, err := store.GetNotification(ctx, n.NotificationID)
	require.NoError(t, err)
	require.Equal(t, DecisionAcknowledged, acked.DecisionState)
	require.False(t, acked.Unresolved())

	// Ack does not delete the row: the default listing is resolution-based,
	// so an acknowledged-but-unresolved item is still visible (but never
	// action-required). Resolving removes it.
	items, err := store.ListNotifications(ctx, NotificationFilter{RootScopeID: "root-session-1"})
	require.NoError(t, err)
	require.Len(t, items, 1)
	require.Equal(t, DecisionAcknowledged, items[0].DecisionState)
	require.False(t, items[0].ActionRequired())

	ok, err = store.ResolveNotification(ctx, n.NotificationID, ResolutionClosed, now, acked.Version)
	require.NoError(t, err)
	require.True(t, ok)

	items, err = store.ListNotifications(ctx, NotificationFilter{RootScopeID: "root-session-1"})
	require.NoError(t, err)
	require.Len(t, items, 0, "resolved items leave the unresolved listing")
}

// TestSQLiteStore_Notification_DeferReentry verifies defer semantics: a
// deferred-not-due item stays out of the action-required set; a deferred-past
// item re-enters it (doc 6.3 rule 6 / doc 6.4 defer re-entry).
func TestSQLiteStore_Notification_DeferReentry(t *testing.T) {
	store := newTestStore(t, "supervision-defer")
	ctx := context.Background()
	now := time.Now().UTC()

	n, err := store.UpsertNotification(ctx, testNotification("agent-defer", 30))
	require.NoError(t, err)

	future := now.Add(24 * time.Hour)
	ok, err := store.DeferNotification(ctx, n.NotificationID, future, "checking manually", n.Version)
	require.NoError(t, err)
	require.True(t, ok)

	deferred, err := store.GetNotification(ctx, n.NotificationID)
	require.NoError(t, err)
	require.Equal(t, DecisionDeferred, deferred.DecisionState)
	require.True(t, deferred.Unresolved(), "defer is not acknowledgement")
	require.False(t, deferred.ActionRequired(), "not due yet")

	past := now.Add(-time.Hour)
	ok, err = store.DeferNotification(ctx, n.NotificationID, past, "re-check now", deferred.Version)
	require.NoError(t, err)
	require.True(t, ok)

	due, err := store.GetNotification(ctx, n.NotificationID)
	require.NoError(t, err)
	require.True(t, due.ActionRequired(), "deferred past due re-enters the digest")
}

// TestSQLiteStore_Notification_Resolve verifies resolution leaves the
// unresolved set and is recoverable via IncludeResolved.
func TestSQLiteStore_Notification_Resolve(t *testing.T) {
	store := newTestStore(t, "supervision-resolve")
	ctx := context.Background()
	now := time.Now().UTC()

	n, err := store.UpsertNotification(ctx, testNotification("agent-resolve", 40))
	require.NoError(t, err)

	ok, err := store.ResolveNotification(ctx, n.NotificationID, ResolutionRecovered, now, n.Version)
	require.NoError(t, err)
	require.True(t, ok)

	items, err := store.ListNotifications(ctx, NotificationFilter{RootScopeID: "root-session-1"})
	require.NoError(t, err)
	require.Len(t, items, 0, "resolved items must leave the unresolved set")

	items, err = store.ListNotifications(ctx, NotificationFilter{RootScopeID: "root-session-1", IncludeResolved: true})
	require.NoError(t, err)
	require.Len(t, items, 1)
	require.Equal(t, ResolutionRecovered, items[0].ResolutionState)
}

// TestSQLiteStore_LastNotificationSeq verifies the per-root-scope high-water
// mark used by preflight cursors.
func TestSQLiteStore_LastNotificationSeq(t *testing.T) {
	store := newTestStore(t, "supervision-seq")
	ctx := context.Background()

	_, err := store.UpsertNotification(ctx, testNotification("agent-seq-1", 100))
	require.NoError(t, err)
	_, err = store.UpsertNotification(ctx, testNotification("agent-seq-2", 200))
	require.NoError(t, err)

	seq, err := store.LastNotificationSeq(ctx, "root-session-1")
	require.NoError(t, err)
	require.Equal(t, int64(200), seq)

	seq, err = store.LastNotificationSeq(ctx, "other-root")
	require.NoError(t, err)
	require.Equal(t, int64(0), seq)
}

// TestSQLiteStore_WakePending_ClaimIdempotent verifies doc 6.5 rule 3:
// a wake request is claimed exactly once and resolved rows disappear.
func TestSQLiteStore_WakePending_ClaimIdempotent(t *testing.T) {
	store := newTestStore(t, "supervision-wake")
	ctx := context.Background()
	now := time.Now().UTC()

	wake := WakePending{
		WakeID:                "wake-1",
		RootScopeID:           "root-session-1",
		TargetParentSessionID: "root-session-1",
		WakeReason:            "timeout",
		DedupKey:              "root-session-1|agent-wake|timeout",
		CreatedAt:             now,
	}
	require.NoError(t, store.InsertWakePending(ctx, wake))

	items, err := store.ListWakePending(ctx, WakeFilter{RootScopeID: "root-session-1", UnclaimedOnly: true})
	require.NoError(t, err)
	require.Len(t, items, 1)

	ok, err := store.ClaimWakePending(ctx, "wake-1", "turn-scheduler", now)
	require.NoError(t, err)
	require.True(t, ok)

	ok, err = store.ClaimWakePending(ctx, "wake-1", "turn-scheduler-2", now)
	require.NoError(t, err)
	require.False(t, ok, "wake must be claimed exactly once")

	require.NoError(t, store.ResolveWakePending(ctx, "wake-1"))
	items, err = store.ListWakePending(ctx, WakeFilter{RootScopeID: "root-session-1", UnclaimedOnly: true})
	require.NoError(t, err)
	require.Len(t, items, 0)
}

// TestSQLiteStore_TeamEdge_AncestorsAndCycle verifies the durable Team->Team
// parent edges (doc 6.1/6.7): root-first ancestor chains and cycle rejection.
func TestSQLiteStore_TeamEdge_AncestorsAndCycle(t *testing.T) {
	store := newTestStore(t, "supervision-team-edge")
	ctx := context.Background()
	now := time.Now().UTC()

	mkEdge := func(edgeID, parent, child string) TeamEdge {
		return TeamEdge{
			EdgeID:       edgeID,
			RootScopeID:  "root-team-1",
			RootTeamID:   "team-root",
			ParentTeamID: parent,
			ChildTeamID:  child,
			Relation:     "spawned",
			CreatedBy:    "lead-1",
			CreatedAt:    now,
			Status:       TeamEdgeStatusActive,
		}
	}

	_, err := store.UpsertTeamEdge(ctx, mkEdge("edge-ab", "team-root", "team-a"))
	require.NoError(t, err)
	_, err = store.UpsertTeamEdge(ctx, mkEdge("edge-bc", "team-a", "team-b"))
	require.NoError(t, err)

	children, err := store.ListChildTeams(ctx, "team-a")
	require.NoError(t, err)
	require.Len(t, children, 1)
	require.Equal(t, "team-b", children[0].ChildTeamID)

	ancestors, err := store.ListTeamAncestors(ctx, "team-b")
	require.NoError(t, err)
	require.Len(t, ancestors, 2, "root first: team-root -> team-a -> team-b")
	require.Equal(t, "team-root", ancestors[0].ParentTeamID)
	require.Equal(t, "team-b", ancestors[1].ChildTeamID)

	// Close the middle edge: team-b's chain shortens.
	require.NoError(t, store.CloseTeamEdge(ctx, "edge-ab", now))
	ancestors, err = store.ListTeamAncestors(ctx, "team-b")
	require.NoError(t, err)
	require.Len(t, ancestors, 1)
	require.Equal(t, "team-a", ancestors[0].ParentTeamID)

	// A cycle must be rejected instead of looping forever. Restore the root
	// edge first so the cycle team-root -> team-a -> team-b -> team-root is
	// complete.
	_, err = store.UpsertTeamEdge(ctx, mkEdge("edge-ab2", "team-root", "team-a"))
	require.NoError(t, err)
	_, err = store.UpsertTeamEdge(ctx, mkEdge("edge-ca", "team-b", "team-root"))
	require.NoError(t, err)
	_, err = store.ListTeamAncestors(ctx, "team-root")
	require.Error(t, err, "cycle in team edges must be rejected")
}
