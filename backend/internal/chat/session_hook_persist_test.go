package chat

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSessionStartMarkerSurvivesReload(t *testing.T) {
	ctx := context.Background()
	storage := NewInMemoryStorage()
	manager := NewSessionManager(storage, nil)
	session, err := manager.CreateSession(ctx, "hook-marker-user")
	require.NoError(t, err)

	session.SetContext(sessionHookStartedContextKey, true)
	require.NoError(t, manager.Update(ctx, session))

	reloaded, err := manager.Get(ctx, session.ID)
	require.NoError(t, err)
	raw, ok := reloaded.GetContext(sessionHookStartedContextKey)
	require.True(t, ok)
	require.Equal(t, true, raw)
}
