package commands

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/wwsheng009/ai-agent-runtime/internal/agentcontrol"
	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
	runtimegoal "github.com/wwsheng009/ai-agent-runtime/internal/goal"
)

func TestLocalSessionMetaLookupPrefersMetadataReader(t *testing.T) {
	ctx := context.Background()
	store := &metadataCountingSessionStore{SessionStorage: runtimechat.NewInMemoryStorage()}
	session := runtimechat.NewSession("usage-meta-user")
	session.Metadata.Title = "metadata title"
	session.Metadata.LastModel = "test-model"
	session.Metadata.Context["provider"] = "test-provider"
	session.Metadata.Context["workspace_path"] = `E:\workspace`
	session.Metadata.Context[agentcontrol.SessionContextParentSessionID] = "parent-session"
	session.Metadata.Context[agentcontrol.SessionContextRootSessionID] = "root-session"
	session.Metadata.Context[agentcontrol.SessionContextAgentID] = "agent-1"
	require.NoError(t, store.Save(ctx, session))

	lookup := &localSessionMetaLookup{store: store}
	meta, ok := lookup.SessionMeta(session.ID)
	require.True(t, ok)
	require.Equal(t, "metadata title", meta.Title)
	require.Equal(t, "test-provider", meta.Provider)
	require.Equal(t, "test-model", meta.Model)
	require.Equal(t, `E:\workspace`, meta.WorkingDirectory)

	lineage, ok := lookup.SessionLineage(session.ID)
	require.True(t, ok)
	require.Equal(t, "parent-session", lineage.ParentSessionID)
	require.Equal(t, "root-session", lineage.RootSessionID)
	require.Equal(t, "agent-1", lineage.SubagentID)

	require.Equal(t, 2, store.metadataCalls)
	require.Zero(t, store.loadCalls, "usage metadata lookups must not deserialize prompt history")
}

func TestLocalSessionMetaLookupFallsBackToLoad(t *testing.T) {
	ctx := context.Background()
	inner := runtimechat.NewInMemoryStorage()
	store := &fullLoadCountingStore{SessionStorage: inner}
	session := runtimechat.NewSession("usage-meta-fallback")
	session.Metadata.Title = "fallback title"
	require.NoError(t, store.Save(ctx, session))

	meta, ok := (&localSessionMetaLookup{store: store}).SessionMeta(session.ID)
	require.True(t, ok)
	require.Equal(t, "fallback title", meta.Title)
	require.Equal(t, 1, store.loads)
}

// fullLoadCountingStore deliberately does not implement
// runtimechat.SessionStorageMetadataReader: it pins the compatibility fallback.
type fullLoadCountingStore struct {
	runtimechat.SessionStorage
	loads int
}

func (s *fullLoadCountingStore) Load(ctx context.Context, sessionID string) (*runtimechat.Session, error) {
	s.loads++
	return s.SessionStorage.Load(ctx, sessionID)
}

func TestLocalGoalPersistHookUsesMetadataReaderAndPreservesGoal(t *testing.T) {
	ctx := context.Background()
	store := &metadataCountingSessionStore{SessionStorage: runtimechat.NewInMemoryStorage()}
	session := runtimechat.NewSession("goal-metadata-user")
	goal, err := runtimegoal.NewSessionGoal(session.ID, "preserve this goal", time.Now())
	require.NoError(t, err)
	require.NoError(t, runtimegoal.NewMetadataStore().Put(session, goal))
	require.NoError(t, store.Save(ctx, session))

	stale := session.CloneForMetadataUpdate()
	require.NoError(t, runtimegoal.NewMetadataStore().Clear(stale))

	prepared, err := localGoalPersistHook(store)(ctx, stale)
	require.NoError(t, err)
	preserved, ok, err := runtimegoal.NewMetadataStore().Get(prepared)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, goal.Objective, preserved.Objective)
	require.Equal(t, 1, store.metadataCalls)
	require.Zero(t, store.loadCalls, "goal merge must not load/deserialize the full history")
}
