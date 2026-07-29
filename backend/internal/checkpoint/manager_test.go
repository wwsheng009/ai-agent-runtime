package checkpoint

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/wwsheng009/ai-agent-runtime/internal/artifact"
	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
	runtimetypes "github.com/wwsheng009/ai-agent-runtime/internal/types"
)

func TestManagerRestoreConversationPreviewReturnsConversationPlan(t *testing.T) {
	store, err := artifact.NewStore(nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })

	checkpointID, err := store.SaveCheckpoint(context.Background(), artifact.Checkpoint{
		SessionID:    "session-1",
		Reason:       "tool:edit",
		MessageCount: 3,
		Metadata: map[string]interface{}{
			"message_count": 3,
		},
	})
	require.NoError(t, err)

	manager := NewManager(store, nil)
	result, err := manager.Restore(context.Background(), RestoreRequest{
		SessionID:    "session-1",
		CheckpointID: checkpointID,
		Mode:         RestoreConversation,
		PreviewOnly:  true,
	})
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.True(t, result.ConversationChanged)
	assert.Equal(t, 3, result.ConversationHead)
	assert.Empty(t, result.AppliedPaths)
	assert.Contains(t, result.Preview, "conversation: rewind visible history to 3 message(s)")
}

func TestManagerRestoreBothRestoresCodeAndConversationPlan(t *testing.T) {
	store, err := artifact.NewStore(nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })

	dir := t.TempDir()
	path := filepath.Join(dir, "sample.txt")
	require.NoError(t, os.WriteFile(path, []byte("after"), 0o644))
	sharedCreatedAt := time.Unix(100, 0).UTC()

	targetID, err := store.SaveCheckpoint(context.Background(), artifact.Checkpoint{
		SessionID:    "session-1",
		Reason:       "tool:edit",
		MessageCount: 2,
		Metadata: map[string]interface{}{
			"message_count": 2,
			"files": []map[string]interface{}{
				{
					"path":          path,
					"op":            "update",
					"before":        "before",
					"after":         "mid",
					"before_exists": true,
					"after_exists":  true,
				},
			},
		},
		CreatedAt: sharedCreatedAt,
	})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path, []byte("after"), 0o644))

	_, err = store.SaveCheckpoint(context.Background(), artifact.Checkpoint{
		SessionID:    "session-1",
		Reason:       "tool:edit",
		MessageCount: 3,
		Metadata: map[string]interface{}{
			"message_count": 3,
			"files": []map[string]interface{}{
				{
					"path":          path,
					"op":            "update",
					"before":        "mid",
					"after":         "after",
					"before_exists": true,
					"after_exists":  true,
				},
			},
		},
		CreatedAt: sharedCreatedAt,
	})
	require.NoError(t, err)

	manager := NewManager(store, nil)
	result, err := manager.Restore(context.Background(), RestoreRequest{
		SessionID:    "session-1",
		CheckpointID: targetID,
		Mode:         RestoreBoth,
	})
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Contains(t, result.AppliedPaths, path)
	assert.True(t, result.ConversationChanged)
	assert.Equal(t, 2, result.ConversationHead)

	content, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, "mid", string(content))
}

func TestManagerAfterMutationSkipsNoopPath(t *testing.T) {
	store, err := artifact.NewStore(nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })

	path := filepath.Join(t.TempDir(), "missing.txt")
	manager := NewManager(store, nil)
	checkpointID, err := manager.AfterMutation(context.Background(), &PendingCheckpoint{
		SessionID:  "session-noop",
		ToolName:   "edit",
		ToolCallID: "tool-noop",
		Paths:      []string{path},
		Snapshots: map[string]*FileSnapshot{
			path: {Path: path, BeforeExists: false},
		},
	}, nil, "")
	require.NoError(t, err)
	assert.Empty(t, checkpointID)

	checkpoints, err := store.ListCheckpoints(context.Background(), "session-noop", 0, 0)
	require.NoError(t, err)
	assert.Empty(t, checkpoints)
}

func TestManagerAfterMutationEnforcesSessionRetention(t *testing.T) {
	ctx := context.Background()
	store, err := artifact.NewStore(nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })

	path := filepath.Join(t.TempDir(), "sample.txt")
	require.NoError(t, os.WriteFile(path, []byte("zero"), 0o644))
	manager := NewManager(store, nil)
	manager.MaxCheckpointsPerSession = 1

	firstPending, err := manager.BeforeMutation(ctx, "session-retention", "edit", "tool-1", map[string]interface{}{"path": path})
	require.NoError(t, err)
	require.NotNil(t, firstPending)
	require.NoError(t, os.WriteFile(path, []byte("one"), 0o644))
	firstID, err := manager.AfterMutation(ctx, firstPending, nil, "")
	require.NoError(t, err)
	require.NotEmpty(t, firstID)

	secondPending, err := manager.BeforeMutation(ctx, "session-retention", "edit", "tool-2", map[string]interface{}{"path": path})
	require.NoError(t, err)
	require.NotNil(t, secondPending)
	require.NoError(t, os.WriteFile(path, []byte("two"), 0o644))
	secondID, err := manager.AfterMutation(ctx, secondPending, nil, "")
	require.NoError(t, err)
	require.NotEmpty(t, secondID)

	checkpoints, err := store.ListCheckpoints(ctx, "session-retention", 0, 0)
	require.NoError(t, err)
	require.Len(t, checkpoints, 1)
	assert.Equal(t, secondID, checkpoints[0].ID)
	first, err := store.GetCheckpoint(ctx, firstID)
	require.NoError(t, err)
	assert.Nil(t, first)
}

func TestManagerFullModeStoresFullMetadataAndAfterBlob(t *testing.T) {
	ctx := context.Background()
	store, err := artifact.NewStore(nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })

	path := filepath.Join(t.TempDir(), "sample.txt")
	require.NoError(t, os.WriteFile(path, []byte("before"), 0o644))
	manager := NewManager(store, nil)
	manager.StoreMode = StoreModeFull
	manager.MaxDiffBytes = 5
	pending, err := manager.BeforeMutation(ctx, "session-full", "edit", "tool-full", map[string]interface{}{"path": path})
	require.NoError(t, err)
	require.NotNil(t, pending)
	require.NoError(t, os.WriteFile(path, []byte("after"), 0o644))

	checkpointID, err := manager.AfterMutation(ctx, pending, map[string]interface{}{"patch": "123456789"}, "")
	require.NoError(t, err)
	require.NotEmpty(t, checkpointID)
	checkpoint, err := store.GetCheckpoint(ctx, checkpointID)
	require.NoError(t, err)
	require.NotNil(t, checkpoint)
	files, ok := checkpoint.Metadata["files"].([]interface{})
	require.True(t, ok)
	require.Len(t, files, 1)
	metadataFile, ok := files[0].(map[string]interface{})
	require.True(t, ok)
	assert.Equal(t, "before", metadataFile["before"])
	assert.Equal(t, "after", metadataFile["after"])
	assert.Equal(t, "12345", metadataFile["diff"])

	records, err := store.GetCheckpointFiles(ctx, checkpointID)
	require.NoError(t, err)
	require.Len(t, records, 1)
	assert.NotEmpty(t, records[0].BeforeBlobID)
	assert.NotEmpty(t, records[0].AfterBlobID)
	assert.Equal(t, "12345", records[0].DiffText)
}

func TestManagerRestoreMissingBeforeBlobDoesNotTruncateFile(t *testing.T) {
	ctx := context.Background()
	store, err := artifact.NewStore(nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })

	path := filepath.Join(t.TempDir(), "sample.txt")
	require.NoError(t, os.WriteFile(path, []byte("current"), 0o644))
	targetID, err := store.SaveCheckpoint(ctx, artifact.Checkpoint{
		SessionID: "session-missing-blob",
		CreatedAt: time.Unix(1, 0).UTC(),
	})
	require.NoError(t, err)
	laterID, err := store.SaveCheckpoint(ctx, artifact.Checkpoint{
		SessionID: "session-missing-blob",
		CreatedAt: time.Unix(2, 0).UTC(),
	})
	require.NoError(t, err)
	require.NoError(t, store.SaveCheckpointFiles(ctx, laterID, []artifact.CheckpointFile{{
		Path:         path,
		Op:           "update",
		BeforeBlobID: "blob_missing",
	}}))

	result, err := NewManager(store, nil).Restore(ctx, RestoreRequest{
		SessionID:    "session-missing-blob",
		CheckpointID: targetID,
		Mode:         RestoreCode,
	})
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Empty(t, result.AppliedPaths)
	require.NotEmpty(t, result.Errors)
	assert.Contains(t, result.Errors[0], "before blob not found")
	content, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, "current", string(content))
}

func TestManagerRestoreIncompleteFileRowFallsBackToLegacyMetadata(t *testing.T) {
	ctx := context.Background()
	store, err := artifact.NewStore(nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })

	path := filepath.Join(t.TempDir(), "sample.txt")
	require.NoError(t, os.WriteFile(path, []byte("current"), 0o644))
	targetID, err := store.SaveCheckpoint(ctx, artifact.Checkpoint{
		SessionID: "session-metadata-fallback",
		CreatedAt: time.Unix(1, 0).UTC(),
	})
	require.NoError(t, err)
	laterID, err := store.SaveCheckpoint(ctx, artifact.Checkpoint{
		SessionID: "session-metadata-fallback",
		Metadata: map[string]interface{}{
			"files": []map[string]interface{}{{
				"path":          path,
				"op":            "update",
				"before":        "legacy-before",
				"after":         "current",
				"before_exists": true,
				"after_exists":  true,
			}},
		},
		CreatedAt: time.Unix(2, 0).UTC(),
	})
	require.NoError(t, err)
	require.NoError(t, store.SaveCheckpointFiles(ctx, laterID, []artifact.CheckpointFile{{
		Path:         path,
		Op:           "update",
		BeforeBlobID: "blob_missing",
	}}))

	result, err := NewManager(store, nil).Restore(ctx, RestoreRequest{
		SessionID:    "session-metadata-fallback",
		CheckpointID: targetID,
		Mode:         RestoreCode,
	})
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Contains(t, result.AppliedPaths, path)
	assert.Empty(t, result.Errors)
	content, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, "legacy-before", string(content))
}

func TestManagerRestoreConversationReturnsExactSnapshotWhenAvailable(t *testing.T) {
	store, err := artifact.NewStore(nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })

	manager := NewManager(store, nil)
	manager.ConversationSnapshot = true
	pending := &PendingCheckpoint{
		SessionID:    "session-1",
		ToolName:     "execute_shell_command",
		ToolCallID:   "tool_1",
		MessageCount: 2,
		Conversation: []runtimetypes.Message{
			*runtimetypes.NewUserMessage("before"),
			*runtimetypes.NewAssistantMessage("during"),
		},
	}

	checkpointID, err := manager.AfterMutation(context.Background(), pending, nil, "")
	require.NoError(t, err)

	result, err := manager.Restore(context.Background(), RestoreRequest{
		SessionID:    "session-1",
		CheckpointID: checkpointID,
		Mode:         RestoreConversation,
	})
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.True(t, result.ConversationChanged)
	assert.True(t, result.ConversationExact)
	assert.Equal(t, 2, result.ConversationHead)
	require.Len(t, result.ConversationMessages, 2)
	assert.Equal(t, "user", result.ConversationMessages[0].Role)
	assert.Equal(t, "before", result.ConversationMessages[0].Content)
	assert.Equal(t, "assistant", result.ConversationMessages[1].Role)
	assert.Equal(t, "during", result.ConversationMessages[1].Content)
}

func TestManagerPreviewConversationMarksExactSnapshotWhenAvailable(t *testing.T) {
	store, err := artifact.NewStore(nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })

	manager := NewManager(store, nil)
	manager.ConversationSnapshot = true
	pending := &PendingCheckpoint{
		SessionID:    "session-1",
		ToolName:     "execute_shell_command",
		ToolCallID:   "tool_1",
		MessageCount: 1,
		Conversation: []runtimetypes.Message{
			*runtimetypes.NewUserMessage("before"),
		},
	}

	checkpointID, err := manager.AfterMutation(context.Background(), pending, nil, "")
	require.NoError(t, err)

	result, err := manager.Restore(context.Background(), RestoreRequest{
		SessionID:    "session-1",
		CheckpointID: checkpointID,
		Mode:         RestoreConversation,
		PreviewOnly:  true,
	})
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.True(t, result.ConversationChanged)
	assert.True(t, result.ConversationExact)
	assert.Contains(t, result.Preview, "conversation: restore 1 message(s)")
}

func TestManagerRestoreConversationBackfillsLegacyCheckpointFromLaterExactSnapshot(t *testing.T) {
	ctx := context.Background()
	store, err := artifact.NewStore(nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })

	manager := NewManager(store, nil)
	manager.ConversationSnapshot = true
	legacyMessages := []runtimetypes.Message{
		*runtimetypes.NewUserMessage("before"),
	}
	targetID, err := store.SaveCheckpoint(ctx, artifact.Checkpoint{
		SessionID:    "session-1",
		Reason:       "tool:edit",
		HistoryHash:  legacyConversationHash(legacyMessages),
		MessageCount: 1,
		Metadata: map[string]interface{}{
			"message_count": 1,
		},
		CreatedAt: time.Now().Add(-1 * time.Minute).UTC(),
	})
	require.NoError(t, err)

	_, err = manager.AfterMutation(ctx, &PendingCheckpoint{
		SessionID:    "session-1",
		ToolName:     "execute_shell_command",
		ToolCallID:   "tool_2",
		MessageCount: 2,
		Conversation: []runtimetypes.Message{
			*runtimetypes.NewUserMessage("before"),
			*runtimetypes.NewAssistantMessage("during"),
		},
	}, nil, "")
	require.NoError(t, err)

	result, err := manager.Restore(ctx, RestoreRequest{
		SessionID:    "session-1",
		CheckpointID: targetID,
		Mode:         RestoreConversation,
	})
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.True(t, result.ConversationChanged)
	assert.True(t, result.ConversationExact)
	require.Len(t, result.ConversationMessages, 1)
	assert.Equal(t, "before", result.ConversationMessages[0].Content)

	target, err := store.GetCheckpoint(ctx, targetID)
	require.NoError(t, err)
	require.NotNil(t, target)
	assert.NotEmpty(t, target.Metadata["conversation_blob_id"])
	assert.Equal(t, float64(1), target.Metadata["conversation_message_count"])
}

func TestManagerRestoreConversationBackfillDoesNotPersistWhenSnapshotsDisabled(t *testing.T) {
	ctx := context.Background()
	store, err := artifact.NewStore(nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })

	manager := NewManager(store, nil)
	manager.ConversationSnapshot = true
	legacyMessages := []runtimetypes.Message{
		*runtimetypes.NewUserMessage("before"),
	}
	targetID, err := store.SaveCheckpoint(ctx, artifact.Checkpoint{
		SessionID:    "session-backfill-disabled",
		Reason:       "tool:edit",
		HistoryHash:  legacyConversationHash(legacyMessages),
		MessageCount: 1,
		Metadata: map[string]interface{}{
			"message_count": 1,
		},
		CreatedAt: time.Now().Add(-1 * time.Minute).UTC(),
	})
	require.NoError(t, err)

	_, err = manager.AfterMutation(ctx, &PendingCheckpoint{
		SessionID:    "session-backfill-disabled",
		ToolName:     "execute_shell_command",
		ToolCallID:   "tool_2",
		MessageCount: 2,
		Conversation: []runtimetypes.Message{
			*runtimetypes.NewUserMessage("before"),
			*runtimetypes.NewAssistantMessage("during"),
		},
	}, nil, "")
	require.NoError(t, err)

	manager.ConversationSnapshot = false
	result, err := manager.Restore(ctx, RestoreRequest{
		SessionID:    "session-backfill-disabled",
		CheckpointID: targetID,
		Mode:         RestoreConversation,
	})
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.True(t, result.ConversationExact)
	require.Len(t, result.ConversationMessages, 1)
	assert.Equal(t, "before", result.ConversationMessages[0].Content)

	target, err := store.GetCheckpoint(ctx, targetID)
	require.NoError(t, err)
	require.NotNil(t, target)
	_, ok := target.Metadata["conversation_blob_id"]
	assert.False(t, ok)
}

func TestManagerPreviewConversationBackfillsLegacyCheckpointWithoutPersisting(t *testing.T) {
	ctx := context.Background()
	store, err := artifact.NewStore(nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })

	manager := NewManager(store, nil)
	manager.ConversationSnapshot = true
	legacyMessages := []runtimetypes.Message{
		*runtimetypes.NewUserMessage("before"),
	}
	targetID, err := store.SaveCheckpoint(ctx, artifact.Checkpoint{
		SessionID:    "session-1",
		Reason:       "tool:edit",
		HistoryHash:  legacyConversationHash(legacyMessages),
		MessageCount: 1,
		Metadata: map[string]interface{}{
			"message_count": 1,
		},
		CreatedAt: time.Now().Add(-1 * time.Minute).UTC(),
	})
	require.NoError(t, err)

	_, err = manager.AfterMutation(ctx, &PendingCheckpoint{
		SessionID:    "session-1",
		ToolName:     "execute_shell_command",
		ToolCallID:   "tool_2",
		MessageCount: 2,
		Conversation: []runtimetypes.Message{
			*runtimetypes.NewUserMessage("before"),
			*runtimetypes.NewAssistantMessage("during"),
		},
	}, nil, "")
	require.NoError(t, err)

	result, err := manager.Restore(ctx, RestoreRequest{
		SessionID:    "session-1",
		CheckpointID: targetID,
		Mode:         RestoreConversation,
		PreviewOnly:  true,
	})
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.True(t, result.ConversationExact)
	assert.Contains(t, result.Preview, "conversation: restore 1 message(s)")

	target, err := store.GetCheckpoint(ctx, targetID)
	require.NoError(t, err)
	require.NotNil(t, target)
	_, ok := target.Metadata["conversation_blob_id"]
	assert.False(t, ok)
}

func TestManagerRestoreConversationBackfillRequiresMatchingHashOrCount(t *testing.T) {
	ctx := context.Background()
	store, err := artifact.NewStore(nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })

	manager := NewManager(store, nil)
	manager.ConversationSnapshot = true
	targetID, err := store.SaveCheckpoint(ctx, artifact.Checkpoint{
		SessionID:    "session-1",
		Reason:       "tool:edit",
		MessageCount: 1,
		Metadata: map[string]interface{}{
			"message_count": 1,
		},
		CreatedAt: time.Now().Add(-1 * time.Minute).UTC(),
	})
	require.NoError(t, err)

	_, err = manager.AfterMutation(ctx, &PendingCheckpoint{
		SessionID:    "session-1",
		ToolName:     "execute_shell_command",
		ToolCallID:   "tool_2",
		MessageCount: 2,
		Conversation: []runtimetypes.Message{
			*runtimetypes.NewUserMessage("before"),
			*runtimetypes.NewAssistantMessage("during"),
		},
	}, nil, "")
	require.NoError(t, err)

	result, err := manager.Restore(ctx, RestoreRequest{
		SessionID:    "session-1",
		CheckpointID: targetID,
		Mode:         RestoreConversation,
	})
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.False(t, result.ConversationExact)
	assert.Empty(t, result.ConversationMessages)

	target, err := store.GetCheckpoint(ctx, targetID)
	require.NoError(t, err)
	require.NotNil(t, target)
	_, ok := target.Metadata["conversation_blob_id"]
	assert.False(t, ok)
}

func TestManagerShellFallbackSnapshotsFilesFromWorkingDirectory(t *testing.T) {
	store, err := artifact.NewStore(nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })

	dir := t.TempDir()
	path := filepath.Join(dir, "sample.txt")
	require.NoError(t, os.WriteFile(path, []byte("before"), 0o644))

	manager := NewManager(store, nil)
	pending, err := manager.BeforeMutation(context.Background(), "session-shell-fallback", "execute_shell_command", "tool_shell_1", map[string]interface{}{
		"command": "echo updated > sample.txt",
		"cwd":     dir,
	})
	require.NoError(t, err)
	require.NotNil(t, pending)

	require.NoError(t, os.WriteFile(path, []byte("after"), 0o644))

	checkpointID, err := manager.AfterMutation(context.Background(), pending, nil, "")
	require.NoError(t, err)
	require.NotEmpty(t, checkpointID)

	checkpoint, err := store.GetCheckpoint(context.Background(), checkpointID)
	require.NoError(t, err)
	require.NotNil(t, checkpoint)

	assert.Nil(t, checkpoint.Metadata["files"])
	assert.Equal(t, float64(1), checkpoint.Metadata["file_count"])
	fileRecords, err := store.GetCheckpointFiles(context.Background(), checkpointID)
	require.NoError(t, err)
	require.Len(t, fileRecords, 1)
	assert.Equal(t, filepath.Clean(path), filepath.Clean(fileRecords[0].Path))
	assert.NotEmpty(t, fileRecords[0].BeforeBlobID)
	assert.Empty(t, fileRecords[0].AfterBlobID)
	before, err := store.LoadBlob(context.Background(), fileRecords[0].BeforeBlobID)
	require.NoError(t, err)
	assert.Equal(t, "before", string(before))
}

func TestManagerAfterMutation_PublishesCheckpointEventWithTraceAndProvenance(t *testing.T) {
	store, err := artifact.NewStore(nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })

	bus := runtimeevents.NewBusWithRetention(16)
	manager := NewManager(store, bus)

	path := filepath.Join(t.TempDir(), "sample.txt")
	require.NoError(t, os.WriteFile(path, []byte("before"), 0o644))

	pending, err := manager.BeforeMutation(context.Background(), "session-checkpoint-event", "execute_shell_command", "tool_checkpoint_1", map[string]interface{}{
		"path": path,
	})
	require.NoError(t, err)
	require.NotNil(t, pending)

	require.NoError(t, os.WriteFile(path, []byte("after"), 0o644))

	checkpointID, err := manager.AfterMutation(context.Background(), pending, map[string]interface{}{
		"trace_id": "trace-checkpoint-event",
		"source_refs": []string{
			"profile-resource:memory:E:/profiles/dev/agents/tester/memory/memory.json",
			"profile-resource:notes:E:/profiles/dev/agents/tester/context/notes.md",
		},
	}, "")
	require.NoError(t, err)
	require.NotEmpty(t, checkpointID)

	events := bus.Trace("trace-checkpoint-event", 10)
	require.Len(t, events, 1)
	event := events[0]
	assert.Equal(t, "checkpoint_created", event.Type)
	assert.Equal(t, "trace-checkpoint-event", event.TraceID)
	assert.Equal(t, "session-checkpoint-event", event.SessionID)
	assert.Equal(t, "execute_shell_command", event.ToolName)
	assert.Equal(t, checkpointID, event.Payload["checkpoint_id"])
	assert.Contains(t, event.Payload["source_refs"], "profile-resource:memory:E:/profiles/dev/agents/tester/memory/memory.json")
	assert.Contains(t, event.Payload["profile_source_refs"], "profile-resource:notes:E:/profiles/dev/agents/tester/context/notes.md")
}

func TestManagerShellFallbackRecordsDirectorySnapshotErrors(t *testing.T) {
	store, err := artifact.NewStore(nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })

	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "a.txt"), []byte("a"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "b.txt"), []byte("b"), 0o644))

	manager := NewManager(store, nil)
	manager.MaxDirectoryFiles = 1

	pending, err := manager.BeforeMutation(context.Background(), "session-shell-error", "execute_shell_command", "tool_shell_err", map[string]interface{}{
		"command": "echo updated",
		"cwd":     dir,
	})
	require.NoError(t, err)
	require.NotNil(t, pending)
	require.NotEmpty(t, pending.DirectorySnapshotErrors)

	checkpointID, err := manager.AfterMutation(context.Background(), pending, nil, "")
	require.NoError(t, err)
	require.NotEmpty(t, checkpointID)

	checkpoint, err := store.GetCheckpoint(context.Background(), checkpointID)
	require.NoError(t, err)
	require.NotNil(t, checkpoint)

	rawErrors, ok := checkpoint.Metadata["directory_snapshot_errors"].([]interface{})
	require.True(t, ok)
	require.NotEmpty(t, rawErrors)
	assert.Contains(t, rawErrors[0], "snapshot for")
}

func TestManagerShellFallbackSkipsNodeModules(t *testing.T) {
	store, err := artifact.NewStore(nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })

	dir := t.TempDir()
	packageFile := filepath.Join(dir, "package.json")
	nodeModuleFile := filepath.Join(dir, "node_modules", "dep", "index.js")
	require.NoError(t, os.WriteFile(packageFile, []byte("{}"), 0o644))
	require.NoError(t, os.MkdirAll(filepath.Dir(nodeModuleFile), 0o755))
	require.NoError(t, os.WriteFile(nodeModuleFile, []byte("module.exports = {}"), 0o644))

	manager := NewManager(store, nil)
	pending, err := manager.BeforeMutation(context.Background(), "session-shell-skip", "execute_shell_command", "tool_shell_skip", map[string]interface{}{
		"command": "echo updated",
		"cwd":     dir,
	})
	require.NoError(t, err)
	require.NotNil(t, pending)

	foundPackage := false
	foundNodeModule := false
	for _, path := range pending.Paths {
		switch filepath.Clean(path) {
		case filepath.Clean(packageFile):
			foundPackage = true
		case filepath.Clean(nodeModuleFile):
			foundNodeModule = true
		}
	}
	assert.True(t, foundPackage)
	assert.False(t, foundNodeModule)
}
