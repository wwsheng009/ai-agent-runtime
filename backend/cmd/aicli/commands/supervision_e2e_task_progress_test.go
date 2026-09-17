package commands

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/wwsheng009/ai-agent-runtime/internal/subagentbatch"
)

// TestSupervisionE2E_TaskProgressAgeFromLastProgressAt is the R1 acceptance on
// the CLI host path (plan §6.1 R1 / §8 M1): once the progress producer writes
// SubagentTaskRecord.LastProgressAt, the parent preflight rollup must report
// the real progress age instead of falling back to the 60s batch heartbeat.
//
// It pins the whole read chain end to end: durable row projection
// (sqlite_store.scanTaskRow) → BatchProgressSource.taskProgressTime →
// ProgressSummary/RunningTasks rendering.
func TestSupervisionE2E_TaskProgressAgeFromLastProgressAt(t *testing.T) {
	host := newLocalSupervisionTestHost(t)
	ctx := context.Background()
	batchStore, err := subagentbatch.NewSQLiteBatchStore(&subagentbatch.StoreConfig{
		Path: filepath.Join(t.TempDir(), "subagent_batches_progress_age.db"),
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = batchStore.Close() })
	host.SubagentBatches = batchStore

	progressAt := time.Now().UTC().Add(-7 * time.Second)
	_, err = batchStore.CreateBatch(ctx, &subagentbatch.SubagentBatch{
		BatchID:         "batch-progress-age-1",
		RootScopeID:     "parent-session",
		ParentSessionID: "parent-session",
		ExecutionMode:   subagentbatch.ExecutionModeBackground,
		Status:          subagentbatch.BatchRunning,
		TaskCount:       2,
		RunningCount:    2,
	}, []subagentbatch.SubagentTaskRecord{
		{TaskID: "worker-progress", ChildSessionID: "session-progress", Status: subagentbatch.TaskRunning, LastProgressAt: &progressAt},
		{TaskID: "worker-silent", ChildSessionID: "session-silent", Status: subagentbatch.TaskRunning},
	})
	require.NoError(t, err)

	prompt, err := injectLocalSupervisionPreflight(ctx, host, "parent-session", "continue work", nil)
	require.NoError(t, err)
	require.Contains(t, prompt, "worker-progress: running; session=session-progress; last progress 7s ago",
		"a running task with a durable LastProgressAt must report the real progress age")
	require.NotContains(t, prompt, "worker-progress: running; session=session-progress; no progress yet",
		"the heartbeat fallback must not shadow the recorded progress stamp")
	require.Contains(t, prompt, "worker-silent: running; session=session-silent; last progress just now ago",
		"a task without a recorded stamp falls back to the row update time, not to a fabricated value")
	require.Contains(t, prompt, "batch-progress-age-1: 0/2 completed, 2 running")
}
