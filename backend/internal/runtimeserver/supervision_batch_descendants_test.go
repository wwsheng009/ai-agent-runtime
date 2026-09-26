package runtimeserver

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/wwsheng009/ai-agent-runtime/internal/subagentbatch"
	"github.com/wwsheng009/ai-agent-runtime/internal/supervision"
)

// 运行中的 batch 必须投影为 running descendants：这正是 2026-09-26 会话里
// subagent_status 在 3 个子代理工作时返回 0 行 + next_action=finalize 的回归点。
func TestSupervisionDescendantsProjectRunningBatchTasks(t *testing.T) {
	ctx := context.Background()
	store, err := subagentbatch.NewSQLiteBatchStore(&subagentbatch.StoreConfig{
		Path: filepath.Join(t.TempDir(), "batches.sqlite"),
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })

	now := time.Now().UTC()
	progress := now.Add(-2 * time.Second)
	batch := &subagentbatch.SubagentBatch{
		BatchID:         subagentbatch.NewID("batch"),
		RootScopeID:     "parent-session",
		ParentSessionID: "parent-session",
		ExecutionMode:   subagentbatch.ExecutionModeBackground,
		Status:          subagentbatch.BatchRunning,
		TaskCount:       3,
		RunningCount:    2,
		CompletedCount:  1,
		HeartbeatAt:     now,
		CreatedAt:       now,
		UpdatedAt:       now,
		Version:         1,
	}
	tasks := []subagentbatch.SubagentTaskRecord{
		{TaskID: "task-done", ChildSessionID: "child-done", Status: subagentbatch.TaskSucceeded, OrderIndex: 1, UpdatedAt: now, Version: 1},
		{TaskID: "task-run", ChildSessionID: "child-run", Status: subagentbatch.TaskRunning, OrderIndex: 2, UpdatedAt: now, Version: 1, LastProgressAt: &progress},
		{TaskID: "task-run-2", ChildSessionID: "child-run-2", Status: subagentbatch.TaskRunning, OrderIndex: 3, UpdatedAt: now, Version: 1},
	}
	created, err := store.CreateBatch(ctx, batch, tasks)
	require.NoError(t, err)
	require.True(t, created)

	plane, err := BuildSupervisionControlPlane(t.TempDir(), supervision.Config{}, SupervisionRuntimeHooks{
		SubagentBatchStore: store,
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = plane.Close() })

	scope := supervision.Scope{RootSessionID: "parent-session"}
	states, err := plane.Provider.ListDescendants(ctx, scope)
	require.NoError(t, err)
	byID := make(map[string]supervision.DescendantState, len(states))
	for _, state := range states {
		byID[state.ID] = state
	}
	require.NotContains(t, byID, "child-done", "terminal tasks stay owned by the terminal notification projection")
	running, ok := byID["child-run"]
	require.True(t, ok, "a running batch task must be visible while it runs")
	require.Equal(t, supervision.SupervisionRunning, running.SupervisionState)
	require.Equal(t, string(subagentbatch.TaskRunning), running.ExecutionStatus)
	require.Greater(t, running.ProgressAgeMs, int64(0))
	require.Contains(t, running.Reason, "task-run")
	require.Contains(t, byID, "child-run-2")

	snapshot, err := supervision.BuildSnapshot(ctx, plane.Store, supervision.SnapshotRequest{
		Scope:    scope,
		Provider: plane.Provider,
	})
	require.NoError(t, err)
	require.Len(t, snapshot.Descendants, 2, "running children make the ledger non-empty before any terminal notification")
	for _, item := range snapshot.Descendants {
		require.Equal(t, supervision.SupervisionRunning, item.SupervisionState)
	}
	require.Equal(t, 2, snapshot.Summary.Running,
		"the rollup must keep subagent_status's finalize gate closed while children run")
}
