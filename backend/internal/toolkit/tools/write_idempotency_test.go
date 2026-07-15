package tools

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	runtimeerrors "github.com/wwsheng009/ai-agent-runtime/internal/errors"
)

func TestReliabilityEvalWriteIdempotencyProtection(t *testing.T) {
	t.Run("whole_file_compare_and_swap", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "state.txt")
		tool := NewWriteTool()

		first, err := tool.Execute(context.Background(), map[string]interface{}{
			"file_path": path, "content": "version-one", "expected_sha256": "absent",
		})
		require.NoError(t, err)
		require.True(t, first.Success)

		replay, err := tool.Execute(context.Background(), map[string]interface{}{
			"file_path": path, "content": "version-one", "expected_sha256": "absent",
		})
		require.NoError(t, err)
		require.True(t, replay.Success)
		require.Equal(t, true, replay.Metadata["idempotent_replay"])
		require.Empty(t, replay.Metadata["mutated_paths"])

		stale, err := tool.Execute(context.Background(), map[string]interface{}{
			"file_path": path, "content": "version-two", "expected_sha256": "absent",
		})
		require.NoError(t, err)
		require.False(t, stale.Success)
		require.True(t, runtimeerrors.Is(stale.Error, runtimeerrors.ErrWritePrecondition))

		updated, err := tool.Execute(context.Background(), map[string]interface{}{
			"file_path": path, "content": "version-two",
			"expected_sha256": writeContentRevision(true, "version-one"),
		})
		require.NoError(t, err)
		require.True(t, updated.Success)
		content, err := os.ReadFile(path)
		require.NoError(t, err)
		require.Equal(t, "version-two", string(content))
	})

	t.Run("append_offset_replay", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "chunks.txt")
		tool := NewAppendWriteTool()
		args := map[string]interface{}{
			"file_path": path, "content": "alpha", "expected_offset": float64(0),
		}
		first, err := tool.Execute(context.Background(), args)
		require.NoError(t, err)
		require.True(t, first.Success)

		replay, err := tool.Execute(context.Background(), args)
		require.NoError(t, err)
		require.True(t, replay.Success)
		require.Equal(t, true, replay.Metadata["idempotent_replay"])

		stale, err := tool.Execute(context.Background(), map[string]interface{}{
			"file_path": path, "content": "beta", "expected_offset": float64(0),
		})
		require.NoError(t, err)
		require.False(t, stale.Success)
		require.True(t, runtimeerrors.Is(stale.Error, runtimeerrors.ErrWritePrecondition))

		second, err := tool.Execute(context.Background(), map[string]interface{}{
			"file_path": path, "content": "beta", "expected_offset": float64(len("alpha")),
		})
		require.NoError(t, err)
		require.True(t, second.Success)
		content, err := os.ReadFile(path)
		require.NoError(t, err)
		require.Equal(t, "alphabeta", string(content))
	})
}
