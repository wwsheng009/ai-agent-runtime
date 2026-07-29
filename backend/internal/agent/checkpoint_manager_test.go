package agent

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/wwsheng009/ai-agent-runtime/internal/artifact"
	"github.com/wwsheng009/ai-agent-runtime/internal/checkpoint"
)

func TestCheckpointCaptureDisabledStillAllowsRestore(t *testing.T) {
	store, err := artifact.NewStore(nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })

	dir := t.TempDir()
	path := filepath.Join(dir, "sample.txt")
	require.NoError(t, os.WriteFile(path, []byte("after"), 0o644))
	beforeBlobID, beforeHash, err := store.SaveBlob(context.Background(), []byte("before"))
	require.NoError(t, err)
	targetID, err := store.SaveCheckpoint(context.Background(), artifact.Checkpoint{
		SessionID:    "session-restore-disabled",
		MessageCount: 1,
		Metadata:     map[string]interface{}{"message_count": 1},
		CreatedAt:    time.Unix(1, 0).UTC(),
	})
	require.NoError(t, err)
	laterID, err := store.SaveCheckpoint(context.Background(), artifact.Checkpoint{
		SessionID:    "session-restore-disabled",
		MessageCount: 2,
		Metadata:     map[string]interface{}{"message_count": 2},
		CreatedAt:    time.Unix(2, 0).UTC(),
	})
	require.NoError(t, err)
	require.NoError(t, store.SaveCheckpointFiles(context.Background(), laterID, []artifact.CheckpointFile{{
		Path:         path,
		Op:           "update",
		BeforeBlobID: beforeBlobID,
		BeforeHash:   beforeHash,
	}}))

	a := &Agent{artifacts: store, checkpointDisabled: true}
	require.Nil(t, a.GetCheckpointManager())
	restoreManager := a.GetCheckpointRestoreManager()
	require.NotNil(t, restoreManager)

	result, err := restoreManager.Restore(context.Background(), checkpoint.RestoreRequest{
		SessionID:    "session-restore-disabled",
		CheckpointID: targetID,
		Mode:         checkpoint.RestoreCode,
	})
	require.NoError(t, err)
	require.Contains(t, result.AppliedPaths, path)
	content, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, "before", string(content))
	require.Nil(t, a.GetCheckpointManager(), "restore access must not re-enable capture")
}

func TestApplyCheckpointConfigIsStickyAndConfiguresLeanStorage(t *testing.T) {
	store, err := artifact.NewStore(nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })

	a := &Agent{artifacts: store, checkpointDisabled: true}
	a.ApplyCheckpointConfig(true, 12345, CheckpointStorageOptions{
		StoreMode:                checkpoint.StoreModeDiff,
		ConversationSnapshot:     false,
		MaxDiffBytes:             2048,
		MaxCheckpointsPerSession: 7,
	})
	manager := a.GetCheckpointManager()
	require.NotNil(t, manager)
	require.True(t, a.CheckpointCaptureEnabled())
	require.Equal(t, int64(12345), manager.MaxFileBytes)
	require.Equal(t, checkpoint.StoreModeDiff, manager.StoreMode)
	require.False(t, manager.ConversationSnapshot)
	require.Equal(t, int64(2048), manager.MaxDiffBytes)
	require.Equal(t, 7, manager.MaxCheckpointsPerSession)

	a.ApplyCheckpointConfig(true, 0)
	require.Equal(t, int64(0), manager.MaxFileBytes, "zero must explicitly disable the file-size cap")

	a.ApplyCheckpointConfig(false, 0)
	require.Nil(t, a.GetCheckpointManager())
	require.False(t, a.CheckpointCaptureEnabled())
	require.NotNil(t, a.GetCheckpointRestoreManager())
	require.Nil(t, a.GetCheckpointManager())
}
