package chat

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/wwsheng009/ai-agent-runtime/internal/agent"
	runtimeerrors "github.com/wwsheng009/ai-agent-runtime/internal/errors"
	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
)

func newSelfHealTestActor(t *testing.T, hook func(context.Context, string) error, bus *runtimeevents.Bus) (*SessionActor, *InMemoryStorage, *Session) {
	t.Helper()
	ctx := context.Background()
	storage := NewInMemoryStorage()
	manager := NewSessionManager(storage, nil)
	session, err := manager.CreateSession(ctx, "self-heal-user")
	require.NoError(t, err)

	runtimeStore := NewInMemoryRuntimeStore(8)
	actor, err := NewSessionActor(session.ID, SessionActorConfig{
		Agent:         agent.NewAgent(&agent.Config{Name: "self-heal-agent", Model: "test-model", MaxSteps: 1}, nil),
		SessionStore:  storage,
		StateStore:    runtimeStore,
		EventStore:    runtimeStore,
		EventBus:      bus,
		EnsureSession: hook,
	})
	require.NoError(t, err)
	return actor, storage, session
}

func TestSessionActorLoadSessionSelfHealsMissingRow(t *testing.T) {
	ctx := context.Background()
	bus := runtimeevents.NewBus()
	healCalls := 0

	var (
		actor    *SessionActor
		storage  *InMemoryStorage
		original *Session
	)
	hook := func(_ context.Context, sessionID string) error {
		healCalls++
		assert.Equal(t, actor.id, sessionID)
		return storage.Save(ctx, original.Clone())
	}
	actor, storage, original = newSelfHealTestActor(t, hook, bus)
	require.NoError(t, storage.Delete(ctx, actor.id))

	recovered := make(chan runtimeevents.Event, 4)
	bus.Subscribe("session_recovered_from_missing_row", func(event runtimeevents.Event) {
		recovered <- event
	})

	loaded, err := actor.loadSession(ctx)
	require.NoError(t, err)
	require.NotNil(t, loaded)
	assert.Equal(t, actor.id, loaded.ID)
	assert.Equal(t, 1, healCalls)

	select {
	case event := <-recovered:
		assert.Equal(t, actor.id, event.SessionID)
		assert.Equal(t, "load", event.Payload["trigger"])
	case <-time.After(time.Second):
		t.Fatal("expected session_recovered_from_missing_row event")
	}
}

func TestSessionActorLoadSessionCachesFailedSelfHeal(t *testing.T) {
	ctx := context.Background()
	healCalls := 0
	hook := func(context.Context, string) error {
		healCalls++
		return errors.New("store offline")
	}
	actor, storage, _ := newSelfHealTestActor(t, hook, nil)
	require.NoError(t, storage.Delete(ctx, actor.id))

	_, err := actor.loadSession(ctx)
	require.Error(t, err)
	assert.True(t, runtimeerrors.Is(err, runtimeerrors.ErrSessionNotFound), "want SESSION_NOT_FOUND, got %v", err)

	// The negative cache must prevent a retry storm while the row stays missing.
	_, err = actor.loadSession(ctx)
	require.Error(t, err)
	assert.Equal(t, 1, healCalls, "failed self-heal must be negatively cached")
}

func TestSessionActorPersistSessionRecreatesMissingRow(t *testing.T) {
	ctx := context.Background()
	hook := func(context.Context, string) error {
		return errors.New("host restore unavailable")
	}
	actor, storage, _ := newSelfHealTestActor(t, hook, nil)

	loaded, err := storage.Load(ctx, actor.id)
	require.NoError(t, err)
	require.NotNil(t, loaded)
	require.NoError(t, storage.Delete(ctx, actor.id))

	require.NoError(t, actor.persistSession(ctx, loaded))

	restored, err := storage.Load(ctx, actor.id)
	require.NoError(t, err)
	require.NotNil(t, restored)
	assert.Equal(t, actor.id, restored.ID)
}

func TestSessionActorPersistSessionWithoutHookKeepsTypedError(t *testing.T) {
	ctx := context.Background()
	actor, storage, _ := newSelfHealTestActor(t, nil, nil)

	loaded, err := storage.Load(ctx, actor.id)
	require.NoError(t, err)
	require.NoError(t, storage.Delete(ctx, actor.id))

	err = actor.persistSession(ctx, loaded)
	require.Error(t, err)
	assert.True(t, runtimeerrors.Is(err, runtimeerrors.ErrSessionNotFound), "want SESSION_NOT_FOUND, got %v", err)
}
