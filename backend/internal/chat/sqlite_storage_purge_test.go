package chat

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/wwsheng009/ai-agent-runtime/internal/types"
)

func TestSQLiteSessionPurgeFencesCanonicalWritesAndIsIdempotent(t *testing.T) {
	ctx := context.Background()
	store := newTestSQLiteSessionStorage(t, nil)
	session := NewSession("purge-fence-user")
	session.ID = "purge-fence-session"
	require.NoError(t, store.Save(ctx, session))
	require.NoError(t, store.AddMessage(ctx, session.ID, *types.NewUserMessage("before purge")))

	purge, err := store.BeginSessionPurge(ctx, session.ID, "test")
	require.NoError(t, err)
	require.Equal(t, int64(1), purge.Generation)
	require.Equal(t, "draining", purge.State)

	replayed, err := store.BeginSessionPurge(ctx, session.ID, "replayed")
	require.NoError(t, err)
	require.Equal(t, purge.Generation, replayed.Generation)
	require.Equal(t, purge.Reason, replayed.Reason)

	err = store.AddMessage(ctx, session.ID, *types.NewUserMessage("after purge"))
	require.ErrorIs(t, err, ErrSessionPurgeInProgress)
	require.ErrorIs(t, store.AcknowledgeSessionMemoryPurge(ctx, session.ID, purge.Generation+1),
		ErrSessionPurgeGenerationMismatch)

	require.NoError(t, store.AcknowledgeSessionMemoryPurge(ctx, session.ID, purge.Generation))
	require.NoError(t, store.AcknowledgeSessionMemoryPurge(ctx, session.ID, purge.Generation))
	require.NoError(t, store.CompleteSessionPurge(ctx, session.ID, purge.Generation))
	require.NoError(t, store.CompleteSessionPurge(ctx, session.ID, purge.Generation))
	_, err = store.Load(ctx, session.ID)
	require.ErrorIs(t, err, ErrSessionNotFound)

	// A recreated row must move to a strictly newer generation; otherwise a
	// delayed acknowledgement from the old incarnation could target it.
	recreated := NewSession("purge-fence-user")
	recreated.ID = session.ID
	require.NoError(t, store.Save(ctx, recreated))
	next, err := store.BeginSessionPurge(ctx, session.ID, "second")
	require.NoError(t, err)
	require.Equal(t, purge.Generation+1, next.Generation)
}

func TestSQLiteSessionPurgeWaitsForRequiredMemoryAcknowledgement(t *testing.T) {
	ctx := context.Background()
	store := newTestSQLiteSessionStorage(t, nil)
	store.SetSessionPurgeMemoryRequired(true)
	session := NewSession("purge-required-user")
	session.ID = "purge-required-session"
	require.NoError(t, store.Save(ctx, session))

	purge, err := store.BeginSessionPurge(ctx, session.ID, "required")
	require.NoError(t, err)
	require.True(t, purge.MemoryRequired)
	require.False(t, purge.MemoryAcknowledged)
	require.ErrorIs(t, store.CompleteSessionPurge(ctx, session.ID, purge.Generation),
		ErrSessionPurgeCoordinatorUnavailable)

	pending, err := store.ListPendingSessionPurges(ctx, 10)
	require.NoError(t, err)
	require.Len(t, pending, 1)
	require.Equal(t, purge.Generation, pending[0].Generation)
	require.False(t, pending[0].MemoryAcknowledged)

	require.NoError(t, store.AcknowledgeSessionMemoryPurge(ctx, session.ID, purge.Generation))
	require.NoError(t, store.CompleteSessionPurge(ctx, session.ID, purge.Generation))
}

func TestSQLiteSessionPurgeUsesIndependentSessionAndPurgeGenerations(t *testing.T) {
	ctx := context.Background()
	store := newTestSQLiteSessionStorage(t, nil)
	session := NewSession("purge-generation-user")
	session.ID = "purge-generation-session"
	require.NoError(t, store.Save(ctx, session))
	_, err := store.db.ExecContext(ctx, `
		UPDATE sessions SET session_generation = 41 WHERE id = ?
	`, session.ID)
	require.NoError(t, err)

	purge, err := store.BeginSessionPurge(ctx, session.ID, "generation-test")
	require.NoError(t, err)
	require.Equal(t, int64(41), purge.SessionGeneration)
	require.Equal(t, int64(1), purge.PurgeGeneration)
	require.Equal(t, purge.PurgeGeneration, purge.Generation)

	pending, err := store.ListPendingSessionPurges(ctx, 10)
	require.NoError(t, err)
	require.Len(t, pending, 1)
	require.Equal(t, purge.SessionGeneration, pending[0].SessionGeneration)
	require.Equal(t, purge.PurgeGeneration, pending[0].PurgeGeneration)

	require.ErrorIs(t, store.AcknowledgeSessionMemoryPurgeWithGenerations(
		ctx, session.ID, purge.SessionGeneration+1, purge.PurgeGeneration),
		ErrSessionPurgeGenerationMismatch)
	require.NoError(t, store.AcknowledgeSessionMemoryPurgeWithGenerations(
		ctx, session.ID, purge.SessionGeneration, purge.PurgeGeneration))
	require.ErrorIs(t, store.CompleteSessionPurgeWithGenerations(
		ctx, session.ID, purge.SessionGeneration, purge.PurgeGeneration+1),
		ErrSessionPurgeGenerationMismatch)
	require.NoError(t, store.CompleteSessionPurgeWithGenerations(
		ctx, session.ID, purge.SessionGeneration, purge.PurgeGeneration))
}

func TestSQLiteSessionManagerTTLUsesPurgeProtocol(t *testing.T) {
	ctx := context.Background()
	store := newTestSQLiteSessionStorage(t, nil)
	session := NewSession("purge-ttl-user")
	session.ID = "purge-ttl-session"
	require.NoError(t, store.Save(ctx, session))
	_, err := store.db.ExecContext(ctx, `
		UPDATE sessions
		SET updated_at = ?, expires_at = NULL
		WHERE id = ?
	`, time.Now().UTC().Add(-48*time.Hour).Format(time.RFC3339Nano), session.ID)
	require.NoError(t, err)

	manager := NewSessionManager(store, &SessionManagerConfig{
		TTL:             time.Hour,
		CleanupInterval: 0,
		AutoArchive:     false,
	})
	t.Cleanup(manager.Stop)
	removed, err := manager.CleanupExpired(ctx)
	require.NoError(t, err)
	require.Equal(t, 1, removed)
	_, err = store.Load(ctx, session.ID)
	require.ErrorIs(t, err, ErrSessionNotFound)

	var journalState string
	require.NoError(t, store.db.QueryRowContext(ctx, `
		SELECT state FROM session_purge_journal WHERE session_id = ?
	`, session.ID).Scan(&journalState))
	require.Equal(t, "complete", journalState)
}

func TestSQLiteMaterializedTurnCaptureReceiptsUseStableKeysetPages(t *testing.T) {
	ctx := context.Background()
	store := newTestSQLiteSessionStorage(t, nil)
	base := time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)
	for index, id := range []string{"capture-a", "capture-b", "capture-c"} {
		at := base.Add(time.Duration(index) * time.Second).Format(time.RFC3339Nano)
		_, err := store.db.ExecContext(ctx, `
INSERT INTO session_turn_capture_receipts(
    id, session_id, turn_id, message_seq_start, message_seq_end,
    canonical_revision, range_hash, state, created_at, materialized_at
) VALUES (?, 'page-session', ?, 1, 1, 1, ?, 'materialized', ?, ?)
`, id, id, id+"-hash", at, at)
		require.NoError(t, err)
	}

	firstPage, err := store.ListMaterializedTurnCaptureReceiptsPage(ctx, nil, 2)
	require.NoError(t, err)
	require.Len(t, firstPage, 2)
	require.Equal(t, []string{"capture-a", "capture-b"},
		[]string{firstPage[0].ID, firstPage[1].ID})

	secondPage, err := store.ListMaterializedTurnCaptureReceiptsPage(ctx, &firstPage[1], 2)
	require.NoError(t, err)
	require.Len(t, secondPage, 1)
	require.Equal(t, "capture-c", secondPage[0].ID)

	scopedPage, err := store.ListMaterializedTurnCaptureReceiptsForSessionPage(
		ctx, "page-session", &firstPage[1], 2)
	require.NoError(t, err)
	require.Len(t, scopedPage, 1)
	require.Equal(t, "capture-c", scopedPage[0].ID)
}

func TestSQLiteStorageCleanupDoesNotBypassRequiredMemoryPurge(t *testing.T) {
	ctx := context.Background()
	store := newTestSQLiteSessionStorage(t, nil)
	store.SetSessionPurgeMemoryRequired(true)
	session := NewSession("direct-cleanup-required-user")
	session.ID = "direct-cleanup-required-session"
	require.NoError(t, store.Save(ctx, session))
	_, err := store.db.ExecContext(ctx, `
		UPDATE sessions
		SET updated_at = ?, expires_at = NULL
		WHERE id = ?
	`, time.Now().UTC().Add(-48*time.Hour).Format(time.RFC3339Nano), session.ID)
	require.NoError(t, err)

	removed, err := store.Cleanup(ctx, time.Now().UTC().Add(-time.Hour))
	require.Zero(t, removed)
	require.ErrorIs(t, err, ErrSessionPurgeCoordinatorUnavailable)

	var state string
	require.NoError(t, store.db.QueryRowContext(ctx, `
		SELECT purge_state FROM sessions WHERE id = ?
	`, session.ID).Scan(&state))
	require.Equal(t, "draining", state)
	var journalState string
	require.NoError(t, store.db.QueryRowContext(ctx, `
		SELECT state FROM session_purge_journal WHERE session_id = ?
	`, session.ID).Scan(&journalState))
	require.Equal(t, "draining", journalState)
}
