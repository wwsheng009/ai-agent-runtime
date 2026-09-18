package commands

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
)

func newDurabilityTestChatSession(t *testing.T, userID string) (*ChatSession, *runtimechat.InMemoryStorage, *runtimechat.Session) {
	t.Helper()
	ctx := context.Background()
	storage := runtimechat.NewInMemoryStorage()
	manager := runtimechat.NewSessionManager(storage, nil)
	runtimeSession, err := manager.CreateSession(ctx, userID)
	require.NoError(t, err)
	session := &ChatSession{
		SessionManager: manager,
		RuntimeSession: runtimeSession,
	}
	return session, storage, runtimeSession
}

func TestEnsureSessionDurableBeforeActorFlushesDeferredShell(t *testing.T) {
	ctx := context.Background()
	session, storage, runtimeSession := newDurabilityTestChatSession(t, "durable-user")
	// Simulate a deferred (lazy SQLite) shell: the session exists only in
	// memory until the first durable flush.
	require.NoError(t, storage.Delete(ctx, runtimeSession.ID))
	session.runtimeSessionUnpersisted = true

	require.NoError(t, ensureSessionDurableBeforeActor(session))

	loaded, err := storage.Load(ctx, runtimeSession.ID)
	require.NoError(t, err)
	require.NotNil(t, loaded)
	assert.False(t, session.runtimeSessionUnpersisted)
}

func TestEnsureSessionDurableBeforeActorSkipsPersistedSession(t *testing.T) {
	session, storage, runtimeSession := newDurabilityTestChatSession(t, "durable-user")

	require.NoError(t, ensureSessionDurableBeforeActor(session))

	loaded, err := storage.Load(context.Background(), runtimeSession.ID)
	require.NoError(t, err)
	require.NotNil(t, loaded)
	assert.False(t, session.runtimeSessionUnpersisted)
}

func TestRestoreChatRuntimeSessionRowRecreatesDeletedRow(t *testing.T) {
	ctx := context.Background()
	session, storage, runtimeSession := newDurabilityTestChatSession(t, "restore-user")
	require.NoError(t, storage.Delete(ctx, runtimeSession.ID))

	require.NoError(t, restoreChatRuntimeSessionRow(ctx, session, runtimeSession.ID))

	loaded, err := storage.Load(ctx, runtimeSession.ID)
	require.NoError(t, err)
	require.NotNil(t, loaded)
	assert.Equal(t, runtimeSession.ID, loaded.ID)
}

func TestRestoreChatRuntimeSessionRowRefusesForeignSessionID(t *testing.T) {
	ctx := context.Background()
	session, _, _ := newDurabilityTestChatSession(t, "restore-user")

	err := restoreChatRuntimeSessionRow(ctx, session, "other-session-id")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "host snapshot holds")
}

func TestRestoreChatRuntimeSessionRowRefusesClosedSession(t *testing.T) {
	ctx := context.Background()
	session, storage, runtimeSession := newDurabilityTestChatSession(t, "restore-user")
	require.NoError(t, storage.Delete(ctx, runtimeSession.ID))
	session.RuntimeSession.State = runtimechat.StateClosed

	err := restoreChatRuntimeSessionRow(ctx, session, runtimeSession.ID)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "refusing to restore")
}

func TestRestoreChatRuntimeSessionRowRespectsDisableSwitch(t *testing.T) {
	t.Setenv("AICLI_SESSION_SELF_HEAL", "0")
	ctx := context.Background()
	session, storage, runtimeSession := newDurabilityTestChatSession(t, "restore-user")
	require.NoError(t, storage.Delete(ctx, runtimeSession.ID))

	err := restoreChatRuntimeSessionRow(ctx, session, runtimeSession.ID)
	require.Error(t, err)
	_, loadErr := storage.Load(ctx, runtimeSession.ID)
	require.Error(t, loadErr)
}

func TestEnsureSessionRowLoadableRestoresMissingRow(t *testing.T) {
	ctx := context.Background()
	session, storage, runtimeSession := newDurabilityTestChatSession(t, "restore-user")
	require.NoError(t, storage.Delete(ctx, runtimeSession.ID))

	require.NoError(t, ensureSessionRowLoadable(ctx, storage, runtimeSession.ID, session))

	loaded, err := storage.Load(ctx, runtimeSession.ID)
	require.NoError(t, err)
	require.NotNil(t, loaded)
}
