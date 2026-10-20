package chat

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestClearHistoryMarksSessionStartClear(t *testing.T) {
	ctx := context.Background()
	manager := NewSessionManager(NewInMemoryStorage(), nil)
	session, err := manager.CreateSession(ctx, "hook-clear-user")
	require.NoError(t, err)

	require.NoError(t, manager.ClearHistory(ctx, session.ID))

	loaded, err := manager.Get(ctx, session.ID)
	require.NoError(t, err)
	raw, ok := loaded.GetContext(ContextPendingSessionStartSource)
	require.True(t, ok)
	require.Equal(t, "clear", raw)
}

func TestSessionManagerCloseInvokesCloseHook(t *testing.T) {
	ctx := context.Background()
	manager := NewSessionManager(NewInMemoryStorage(), nil)
	session, err := manager.CreateSession(ctx, "hook-close-user")
	require.NoError(t, err)

	var closedSessionID string
	manager.SetSessionCloseHook(func(_ context.Context, session *Session) {
		closedSessionID = session.ID
	})
	require.NoError(t, manager.Close(ctx, session.ID))
	require.Equal(t, session.ID, closedSessionID)
}

func TestSessionManagerCloseHookIsIdempotent(t *testing.T) {
	ctx := context.Background()
	manager := NewSessionManager(NewInMemoryStorage(), nil)
	session, err := manager.CreateSession(ctx, "hook-close-idempotent")
	require.NoError(t, err)

	calls := 0
	manager.SetSessionCloseHook(func(_ context.Context, _ *Session) {
		calls++
	})
	require.NoError(t, manager.Close(ctx, session.ID))
	require.NoError(t, manager.Close(ctx, session.ID))
	require.Equal(t, 1, calls)
}
