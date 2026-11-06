package chat

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSessionActorLoadSessionMetadataForMergePrefersMetadataReader(t *testing.T) {
	ctx := context.Background()
	inner := NewInMemoryStorage()
	session := NewSession("metadata-merge-user")
	session.SetContext("merge_probe", "metadata-value")
	require.NoError(t, inner.Save(ctx, session))

	store := &metadataMergeCountingStore{SessionStorage: inner}
	actor := &SessionActor{sessionStore: store}

	loaded, err := actor.loadSessionMetadataForMerge(ctx, session.ID)
	require.NoError(t, err)
	value, ok := loaded.GetContext("merge_probe")
	require.True(t, ok)
	assert.Equal(t, "metadata-value", value)
	assert.Equal(t, 1, store.metadataCalls)
	assert.Zero(t, store.loadCalls, "checkpoint merge must not deserialize prompt history")
}

func TestSessionActorLoadSessionMetadataForMergeFallsBackToLoad(t *testing.T) {
	ctx := context.Background()
	store := NewInMemoryStorage()
	session := NewSession("metadata-merge-fallback-user")
	session.SetContext("merge_probe", "fallback-value")
	require.NoError(t, store.Save(ctx, session))

	actor := &SessionActor{sessionStore: SessionStorage(store)}
	loaded, err := actor.loadSessionMetadataForMerge(ctx, session.ID)
	require.NoError(t, err)
	value, ok := loaded.GetContext("merge_probe")
	require.True(t, ok)
	assert.Equal(t, "fallback-value", value)
}

type metadataMergeCountingStore struct {
	SessionStorage
	metadataCalls int
	loadCalls     int
}

func (s *metadataMergeCountingStore) Load(ctx context.Context, sessionID string) (*Session, error) {
	s.loadCalls++
	return s.SessionStorage.Load(ctx, sessionID)
}

func (s *metadataMergeCountingStore) LoadMetadata(ctx context.Context, sessionID string) (*Session, error) {
	s.metadataCalls++
	// InMemoryStorage has no metadata-only reader; use a full load behind the
	// metadata API so the test can still observe which API persistSession chose.
	return s.SessionStorage.Load(ctx, sessionID)
}
