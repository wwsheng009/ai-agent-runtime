package background

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/wwsheng009/ai-agent-runtime/internal/outputdrain"
)

// TestLateOutputAfterTerminalIsDiscardedAndCounted pins the P1-2 drain
// boundary: once the job is terminal, a producer write is counted and dropped
// instead of extending the settled result or emitting another output event.
func TestLateOutputAfterTerminalIsDiscardedAndCounted(t *testing.T) {
	manager := NewManager(Config{})
	t.Cleanup(func() { require.NoError(t, manager.Close()) })
	job := newObservationTestJob(t, manager, "job-late-output")
	writer := manager.newJobOutputWriter(context.Background(), job, nil, "stdout")

	_, err := writer.Write([]byte("before-terminal"))
	require.NoError(t, err)

	job.mu.Lock()
	job.info.Status = StatusCompleted
	finished := time.Now().UTC()
	job.info.FinishedAt = &finished
	job.mu.Unlock()
	manager.settleTerminalTransitionEffects(job.info.ID)

	resultBeforeLate := decorateTaskOutputResult(TaskOutputResult{}, *job.snapshot())
	require.NotNil(t, resultBeforeLate.OutputDrain)
	require.Equal(t, int64(1), resultBeforeLate.OutputDrain.Version)
	require.Equal(t, "drained", string(resultBeforeLate.OutputDrain.Phase))
	require.Equal(t, int64(len("before-terminal")), resultBeforeLate.OutputDrain.AbsorbedBytes)
	require.Equal(t, int64(len("before-terminal")), resultBeforeLate.OutputDrain.DrainedBytes)
	require.Zero(t, resultBeforeLate.OutputDrain.LateBytes)

	late := []byte("after-terminal")
	_, err = writer.Write(late)
	require.NoError(t, err)

	result := decorateTaskOutputResult(TaskOutputResult{}, *job.snapshot())
	require.Equal(t, int64(len(late)), result.LateOutputCount)
	require.True(t, result.LateOutputDiscarded)
	require.NotEmpty(t, result.OutputDrainedAt)
	require.NotNil(t, result.OutputDrain)
	require.Equal(t, int64(2), result.OutputDrain.Version)
	require.Equal(t, "late_discarded", string(result.OutputDrain.Phase))
	require.Equal(t, int64(len(late)), result.OutputDrain.LateBytes)
	require.True(t, result.OutputDrain.LateDiscarded)
	require.Equal(t, int64(len("before-terminal")), result.OutputDrain.AbsorbedBytes)
	require.Equal(t, int64(len("before-terminal")), result.OutputDrain.DrainedBytes)
	require.Equal(t, "discard_late", result.OutputDrain.ConflictPolicy)

	output, err := manager.ReadOutput(context.Background(), TaskOutputArgs{JobID: job.info.ID})
	require.NoError(t, err)
	require.Equal(t, "before-terminal", output.Output)
	require.NotContains(t, output.Output, "after-terminal")
}

func TestOutputDrainLedgerPersistsAcrossStoreReload(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "background.sqlite")
	store, err := NewSQLiteStore(&StoreConfig{Path: path})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })

	now := time.Now().UTC()
	job := Job{
		ID:         "job-drain-persist",
		SessionID:  "sess-drain",
		Kind:       "shell",
		Command:    "echo stable",
		Status:     StatusCompleted,
		CreatedAt:  now,
		FinishedAt: &now,
		Metadata:   map[string]interface{}{},
	}
	require.NoError(t, store.SaveJob(ctx, job))
	ledger := outputdrain.Ledger{
		Version:        1,
		Phase:          outputdrain.PhaseDrained,
		DrainedAt:      now.Format(time.RFC3339Nano),
		AbsorbedBytes:  11,
		DrainedBytes:   11,
		ConflictPolicy: outputdrain.ConflictDiscardLate,
	}
	require.NoError(t, store.UpdateJobOutputDrain(ctx, job.ID, ledger))

	loaded, err := store.GetJob(ctx, job.ID)
	require.NoError(t, err)
	require.NotNil(t, loaded)
	result := decorateTaskOutputResult(TaskOutputResult{}, *loaded)
	require.NotNil(t, result.OutputDrain)
	require.Equal(t, int64(1), result.OutputDrain.Version)
	require.Equal(t, int64(11), result.OutputDrain.DrainedBytes)

	ledger = ledger.AddLate(7)
	require.NoError(t, store.UpdateJobOutputDrain(ctx, job.ID, ledger))
	reloaded, err := store.GetJob(ctx, job.ID)
	require.NoError(t, err)
	require.NotNil(t, reloaded)
	result = decorateTaskOutputResult(TaskOutputResult{}, *reloaded)
	require.NotNil(t, result.OutputDrain)
	require.Equal(t, int64(2), result.OutputDrain.Version)
	require.Equal(t, int64(7), result.OutputDrain.LateBytes)
	require.True(t, result.OutputDrain.LateDiscarded)
	require.Equal(t, int64(11), result.OutputDrain.AbsorbedBytes)

	manager := &Manager{store: store}
	metrics, err := manager.ReadOutputDrainMetrics(ctx, time.Time{}, time.Time{})
	require.NoError(t, err)
	require.Equal(t, 1, metrics.Records)
	require.Equal(t, int64(2), metrics.Versions)
	require.Equal(t, int64(7), metrics.LateBytes)
	require.Equal(t, 1, metrics.LateDiscarded)
}
