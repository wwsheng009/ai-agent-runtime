package chat

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestArchiveIdleSessionsPreservesUpdatedAt(t *testing.T) {
	storage := NewInMemoryStorage()
	manager := NewSessionManager(storage, &SessionManagerConfig{
		TTL:             24 * time.Hour,
		CleanupInterval: 0,
		AutoArchive:     false,
		IdleTimeout:     time.Hour,
	})
	t.Cleanup(manager.Stop)

	ctx := context.Background()
	session, err := manager.Create(ctx, "idle-preserve-user")
	require.NoError(t, err)
	oldUpdatedAt := time.Now().Add(-2 * time.Hour).UTC().Truncate(time.Microsecond)
	session.UpdatedAt = oldUpdatedAt
	session.PreserveUpdatedAt = true
	require.NoError(t, storage.Update(ctx, session))

	manager.ArchiveIdleSessions()

	loaded, err := manager.GetMetadata(ctx, session.ID)
	require.NoError(t, err)
	require.Equal(t, StateIdle, loaded.State)
	require.True(t, loaded.UpdatedAt.Equal(oldUpdatedAt),
		"idle sweep changed updated_at: %s -> %s", oldUpdatedAt, loaded.UpdatedAt)
}
