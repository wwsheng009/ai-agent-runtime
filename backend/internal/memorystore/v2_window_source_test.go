package memorystore

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestWindowSourceIdentityIsolatesStaleChecks(t *testing.T) {
	store, err := New(Config{Root: t.TempDir(), Mode: "on"})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	ctx := context.Background()

	record, err := store.InsertCandidate(ctx, Candidate{
		SessionID: "session-window", TurnID: "turn-window", SourceWindow: "1-4",
		ProjectKey: store.ProjectKey(), Kind: "decision", Text: "window scoped memory",
		Confidence: 0.9, SourceHash: "window-hash-a", SourceRevision: 4,
		SourceMessageIDs: []string{"msg-4"},
	})
	require.NoError(t, err)

	db, err := store.ensureV2()
	require.NoError(t, err)
	var sourceWindow string
	require.NoError(t, db.QueryRowContext(ctx, `
SELECT COALESCE(source_window, '') FROM memory_sources WHERE memory_id = ?
`, record.ID).Scan(&sourceWindow))
	require.Equal(t, "1-4", sourceWindow)

	stale, err := store.MarkSessionSourceChanged(ctx, "session-window", store.ProjectKey(), "full-session-hash", 10)
	require.NoError(t, err)
	require.Zero(t, stale, "full-session stale check must not touch window-scoped sources")

	stale, err = store.MarkWindowSourceChanged(ctx, "session-window", store.ProjectKey(), "2-4", "other-window-hash", 4)
	require.NoError(t, err)
	require.Zero(t, stale, "a different window must not mark this source stale")

	stale, err = store.MarkWindowSourceChanged(ctx, "session-window", store.ProjectKey(), "1-4", "window-hash-b", 4)
	require.NoError(t, err)
	require.Equal(t, 1, stale)

	updated, err := store.GetRecord(ctx, record.ID)
	require.NoError(t, err)
	require.Equal(t, FreshnessStale, updated.Freshness)
	require.Equal(t, "window_source_changed", updated.StaleReason)
}

func TestSessionSourceInvalidationCoversFullAndWindowSources(t *testing.T) {
	store, err := New(Config{Root: t.TempDir(), Mode: "on"})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	ctx := context.Background()

	full, err := store.InsertCandidate(ctx, Candidate{
		SessionID: "session-invalidate", TurnID: "turn-full",
		ProjectKey: store.ProjectKey(), Kind: "decision", Text: "full session memory",
		Confidence: 0.9, SourceHash: "hash-full", SourceRevision: 10,
		SourceMessageIDs: []string{"msg-10"},
	})
	require.NoError(t, err)
	window, err := store.InsertCandidate(ctx, Candidate{
		SessionID: "session-invalidate", TurnID: "turn-window", SourceWindow: "1-4",
		ProjectKey: store.ProjectKey(), Kind: "decision", Text: "window scoped memory",
		Confidence: 0.9, SourceHash: "hash-window", SourceRevision: 4,
		SourceMessageIDs: []string{"msg-4"},
	})
	require.NoError(t, err)

	stale, err := store.MarkSessionSourcesStale(ctx, "session-invalidate", store.ProjectKey(), "backtrack")
	require.NoError(t, err)
	require.Equal(t, 2, stale)
	for _, id := range []string{full.ID, window.ID} {
		updated, err := store.GetRecord(ctx, id)
		require.NoError(t, err)
		require.Equal(t, FreshnessStale, updated.Freshness)
		require.Equal(t, "backtrack", updated.StaleReason)
	}

	stale, err = store.MarkSessionSourcesStale(ctx, "session-invalidate", store.ProjectKey(), "backtrack")
	require.NoError(t, err)
	require.Zero(t, stale, "already stale records must not churn revisions")
}
