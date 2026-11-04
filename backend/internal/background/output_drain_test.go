package background

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
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

	late := []byte("after-terminal")
	_, err = writer.Write(late)
	require.NoError(t, err)

	result := decorateTaskOutputResult(TaskOutputResult{}, *job.snapshot())
	require.Equal(t, int64(len(late)), result.LateOutputCount)
	require.True(t, result.LateOutputDiscarded)
	require.NotEmpty(t, result.OutputDrainedAt)

	output, err := manager.ReadOutput(context.Background(), TaskOutputArgs{JobID: job.info.ID})
	require.NoError(t, err)
	require.Equal(t, "before-terminal", output.Output)
	require.NotContains(t, output.Output, "after-terminal")
}
