package supervision

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func staleTestNotification(subjectID string, kind SubjectKind, severity Severity) Notification {
	return Notification{
		NotificationID:   "n-" + subjectID,
		RootScopeID:      "root-scope",
		SubjectKind:      kind,
		SubjectID:        subjectID,
		SubjectVersion:   1,
		EventSeq:         1,
		EventType:        "execution_deadline",
		Severity:         severity,
		SupervisionState: SupervisionBlocked,
		Reason:           "run blocked for inspection",
		ResolutionState:  ResolutionUnresolved,
		CreatedAt:        time.Now().UTC(),
		UpdatedAt:        time.Now().UTC(),
	}
}

func TestBuildDigest_MissingSubjectDowngradesCriticalToStale(t *testing.T) {
	store := testDigestStore(t, "supervision-digest-stale-mixed")
	ctx := context.Background()
	_, err := store.UpsertNotification(ctx, staleTestNotification("batch-missing", SubjectAgentRun, SeverityCritical))
	require.NoError(t, err)
	_, err = store.UpsertNotification(ctx, staleTestNotification("run-alive", SubjectAgentRun, SeverityCritical))
	require.NoError(t, err)

	digest, err := BuildDigest(ctx, store, DigestRequest{
		RootScopeID: "root-scope",
		SubjectPresence: func(_ context.Context, n Notification) (bool, bool) {
			return n.SubjectID == "run-alive", true
		},
	})
	require.NoError(t, err)
	require.Equal(t, 1, digest.CriticalUnresolved, "only the live subject stays critical")
	require.Equal(t, 1, digest.StaleSubjects)
	require.Contains(t, digest.Text, "stale_subjects: 1")
	require.Contains(t, digest.Text, "- agent_run batch-missing: stale (subject absent from control plane; no action required)")

	var staleItem *DigestItem
	for index := range digest.Items {
		if digest.Items[index].SubjectID == "batch-missing" {
			staleItem = &digest.Items[index]
		}
	}
	require.NotNil(t, staleItem)
	require.True(t, staleItem.Stale)
	require.False(t, staleItem.ActionRequired)
	require.Empty(t, staleItem.AllowedActions)
	require.Equal(t, "none", staleItem.RecommendedAction)
}

func TestBuildDigest_UncheckedSubjectKeepsSeverity(t *testing.T) {
	store := testDigestStore(t, "supervision-digest-stale-unchecked")
	ctx := context.Background()
	_, err := store.UpsertNotification(ctx, staleTestNotification("batch-unknown", SubjectAgentRun, SeverityCritical))
	require.NoError(t, err)

	digest, err := BuildDigest(ctx, store, DigestRequest{
		RootScopeID: "root-scope",
		SubjectPresence: func(_ context.Context, _ Notification) (bool, bool) {
			// checked=false must behave exactly like no resolver at all.
			return false, false
		},
	})
	require.NoError(t, err)
	require.Equal(t, 1, digest.CriticalUnresolved)
	require.Zero(t, digest.StaleSubjects)
	require.NotContains(t, digest.Text, "stale")
}

func TestBuildDigest_NoPresenceResolverKeepsExistingBehavior(t *testing.T) {
	store := testDigestStore(t, "supervision-digest-stale-default")
	ctx := context.Background()
	_, err := store.UpsertNotification(ctx, staleTestNotification("batch-missing", SubjectAgentRun, SeverityCritical))
	require.NoError(t, err)

	digest, err := BuildDigest(ctx, store, DigestRequest{RootScopeID: "root-scope"})
	require.NoError(t, err)
	require.Equal(t, 1, digest.CriticalUnresolved)
	require.Zero(t, digest.StaleSubjects)
}

func TestBuildDigest_ResolvedSubjectIgnoresPresence(t *testing.T) {
	store := testDigestStore(t, "supervision-digest-stale-resolved")
	ctx := context.Background()
	n := staleTestNotification("batch-closed", SubjectAgentRun, SeverityCritical)
	n.ResolutionState = ResolutionClosed
	_, err := store.UpsertNotification(ctx, n)
	require.NoError(t, err)

	calls := 0
	digest, err := BuildDigest(ctx, store, DigestRequest{
		RootScopeID: "root-scope",
		SubjectPresence: func(_ context.Context, _ Notification) (bool, bool) {
			calls++
			return false, true
		},
	})
	require.NoError(t, err)
	require.Zero(t, calls, "resolved rows must not spend a control-plane lookup")
	require.Zero(t, digest.StaleSubjects)
}
