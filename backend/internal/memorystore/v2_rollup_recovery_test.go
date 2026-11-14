package memorystore

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRollupRecoveryDiscoveryIsBoundedScopedAndIndependent(t *testing.T) {
	ctx := context.Background()
	store, err := New(Config{Root: t.TempDir(), ProjectKey: "project-a", Mode: "on"})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	for _, digest := range []TurnDigest{
		{ProjectKey: "project-a", SessionID: "a", TurnID: "1", Status: TurnDigestDone},
		{ProjectKey: "project-a", SessionID: "a", TurnID: "2", Status: TurnDigestDone, RollupStatus: TurnDigestRollupFailed},
		{ProjectKey: "project-a", SessionID: "b", TurnID: "1", Status: TurnDigestDone},
		{ProjectKey: "project-a", SessionID: "c", TurnID: "1", Status: TurnDigestPending},
		{ProjectKey: "project-a", SessionID: "d", TurnID: "1", Status: TurnDigestDone, RollupStatus: TurnDigestRollupDone},
		{ProjectKey: "project-a", SessionID: "e", TurnID: "1", Status: TurnDigestDone, RollupStatus: TurnDigestRollupSkipped},
		{ProjectKey: "project-a", SessionID: "f", TurnID: "1", Status: TurnDigestInvalidated},
		{ProjectKey: "project-b", SessionID: "a", TurnID: "1", Status: TurnDigestDone},
	} {
		_, err := store.RecordTurnDigest(ctx, digest)
		require.NoError(t, err)
	}
	first, err := store.ListTurnDigestRollupSessions(ctx, "", "", 1)
	require.NoError(t, err)
	require.Len(t, first, 1)
	require.Equal(t, "a", first[0].SessionID)
	require.Equal(t, "project-a", first[0].ProjectKey)
	require.Equal(t, 2, first[0].Pending)
	second, err := store.ListTurnDigestRollupSessions(ctx, "", first[0].SessionID, 1)
	require.NoError(t, err)
	require.Len(t, second, 1)
	require.Equal(t, "b", second[0].SessionID)
	last, err := store.ListTurnDigestRollupSessions(ctx, "", second[0].SessionID, 10)
	require.NoError(t, err)
	require.Empty(t, last)
	other, err := store.ListTurnDigestRollupSessions(ctx, "project-b", "", 10)
	require.NoError(t, err)
	require.Len(t, other, 1)
	require.Equal(t, "project-b", other[0].ProjectKey)
	require.Equal(t, 1, other[0].Pending)
	extraction, err := store.ListPendingTurnDigestSessions(ctx, "project-a", 10)
	require.NoError(t, err)
	require.Len(t, extraction, 1)
	require.Equal(t, "c", extraction[0].SessionID)
	for _, sessionID := range []string{"a", "c", "d", "e", "f"} {
		hasOutbox, err := store.HasTurnDigestsForSession(ctx, sessionID)
		require.NoError(t, err)
		require.True(t, hasOutbox, "terminal/deferred outbox rows are not legacy history")
	}
	_, err = store.RecordTurnDigest(ctx, TurnDigest{ProjectKey: "project-b", SessionID: "foreign-only", TurnID: "1"})
	require.NoError(t, err)
	for _, sessionID := range []string{"foreign-only", "legacy"} {
		hasOutbox, err := store.HasTurnDigestsForSession(ctx, sessionID)
		require.NoError(t, err)
		require.False(t, hasOutbox)
	}
}
