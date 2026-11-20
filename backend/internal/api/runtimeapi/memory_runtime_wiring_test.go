package runtimeapi

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wwsheng009/ai-agent-runtime/internal/chat"
	"github.com/wwsheng009/ai-agent-runtime/internal/memoryruntime"
	"github.com/wwsheng009/ai-agent-runtime/internal/memorystore"
)

type recordingV2PurgeStorage struct {
	chat.SessionStorage
	record chat.SessionPurgeRecord

	ackSessionGeneration      int64
	ackPurgeGeneration        int64
	completeSessionGeneration int64
	completePurgeGeneration   int64
}

func (s *recordingV2PurgeStorage) BeginSessionPurge(context.Context, string, string) (chat.SessionPurgeRecord, error) {
	return s.record, nil
}

func (s *recordingV2PurgeStorage) AcknowledgeSessionMemoryPurge(context.Context, string, int64) error {
	return nil
}

func (s *recordingV2PurgeStorage) CompleteSessionPurge(context.Context, string, int64) error {
	return nil
}

func (s *recordingV2PurgeStorage) ListPendingSessionPurges(context.Context, int) ([]chat.SessionPurgeRecord, error) {
	return nil, nil
}

func (s *recordingV2PurgeStorage) AcknowledgeSessionMemoryPurgeWithGenerations(
	_ context.Context, _ string, sessionGeneration, purgeGeneration int64,
) error {
	s.ackSessionGeneration = sessionGeneration
	s.ackPurgeGeneration = purgeGeneration
	return nil
}

func (s *recordingV2PurgeStorage) CompleteSessionPurgeWithGenerations(
	_ context.Context, _ string, sessionGeneration, purgeGeneration int64,
) error {
	s.completeSessionGeneration = sessionGeneration
	s.completePurgeGeneration = purgeGeneration
	return nil
}

func TestWireSessionMemoryPurgeCoordinatorUsesIndependentGenerations(t *testing.T) {
	ctx := context.Background()
	storage := &recordingV2PurgeStorage{
		SessionStorage: chat.NewInMemoryStorage(),
		record: chat.SessionPurgeRecord{
			SessionID:         "api-v2-purge-session",
			SessionGeneration: 41,
			PurgeGeneration:   7,
			Generation:        7,
			MemoryRequired:    true,
			State:             "draining",
		},
	}
	manager := chat.NewSessionManager(storage, &chat.SessionManagerConfig{
		CleanupInterval: 0,
		AutoArchive:     false,
	})
	defer manager.Stop()

	store, err := memorystore.New(memorystore.Config{
		Root:       t.TempDir(),
		Mode:       "on",
		ProjectKey: "api-v2-wiring",
	})
	require.NoError(t, err)
	runtime := memoryruntime.New(store, nil, nil, nil, memoryruntime.Config{
		Mode:       "on",
		Workspace:  t.TempDir(),
		ProjectKey: store.ProjectKey(),
	})
	t.Cleanup(func() { require.NoError(t, store.Close()) })

	wireSessionMemoryPurgeCoordinator(manager, runtime)
	require.NoError(t, manager.Delete(ctx, storage.record.SessionID))
	require.Equal(t, int64(41), storage.ackSessionGeneration)
	require.Equal(t, int64(7), storage.ackPurgeGeneration)
	require.Equal(t, int64(41), storage.completeSessionGeneration)
	require.Equal(t, int64(7), storage.completePurgeGeneration)
}
