package memorystore

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestMemorySessionPurgeIsTenantScoped(t *testing.T) {
	root := t.TempDir()
	project := "tenant-purge-project"
	tenantA, err := New(Config{
		Root: root, Mode: "on", ProjectKey: project,
		Scope: ScopeContext{TenantID: "tenant-a", ProjectKey: project},
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, tenantA.Close()) })
	tenantB, err := New(Config{
		Root: root, Mode: "on", ProjectKey: project,
		Scope: ScopeContext{TenantID: "tenant-b", ProjectKey: project},
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, tenantB.Close()) })
	ctx := context.Background()

	seed := func(store *Store, key string) (string, string, string) {
		t.Helper()
		record, insertErr := store.InsertCandidate(ctx, Candidate{
			SessionID: "shared-purge-session", SessionGeneration: 1, TurnID: key,
			Kind: "fact", Text: "tenant purge marker " + key, Confidence: 0.9,
			SourceHash: "purge:" + key, SourceRevision: 1,
		})
		require.NoError(t, insertErr)
		digest, digestErr := store.RecordTurnDigest(ctx, TurnDigest{
			SessionID: "shared-purge-session", SessionGeneration: 1, TurnID: key,
			Status: TurnDigestPending,
		})
		require.NoError(t, digestErr)
		window, _, windowErr := store.EnsureTurnWindow(ctx, TurnWindow{
			ProjectKey: project, SessionID: "shared-purge-session", SessionGeneration: 1,
			WindowIndex: 1, StartDigestSeq: digest.DigestSeq, EndDigestSeq: digest.DigestSeq,
			TurnCount: 1, SourceHash: "window:" + key, Status: TurnWindowPending,
		})
		require.NoError(t, windowErr)
		return record.ID, digest.ID, window.ID
	}
	recordA, digestA, windowA := seed(tenantA, "a")
	recordB, digestB, windowB := seed(tenantB, "b")

	_, err = tenantA.PurgeSessionWithGenerations(ctx, "shared-purge-session", 1, 1)
	require.NoError(t, err)

	_, err = tenantA.GetRecord(WithMemoryScope(ctx, ScopeContext{TenantID: "tenant-a"}), recordA)
	require.Error(t, err, "tenant A record must be purged")
	_, err = tenantA.GetTurnDigestByID(ctx, digestA)
	require.Error(t, err, "tenant A digest must be purged")
	_, err = tenantA.GetTurnWindow(ctx, windowA)
	require.Error(t, err, "tenant A window must be purged")

	_, err = tenantB.GetRecord(WithMemoryScope(ctx, ScopeContext{TenantID: "tenant-b"}), recordB)
	require.NoError(t, err, "tenant B record must survive")
	_, err = tenantB.GetTurnDigestByID(ctx, digestB)
	require.NoError(t, err, "tenant B digest must survive")
	_, err = tenantB.GetTurnWindow(ctx, windowB)
	require.NoError(t, err, "tenant B window must survive")
}
