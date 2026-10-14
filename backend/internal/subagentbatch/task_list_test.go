package subagentbatch

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestListTaskRowsForParentFiltersTerminalRows 钉住共享账本读取面的口径：
// 只扫指定父会话的批次（作用域纪律与 Find* 一致）；默认过滤终态行，
// includeTerminal=true 时全量；行携带批次 id，供调用方投影批次/任务身份。
func TestListTaskRowsForParentFiltersTerminalRows(t *testing.T) {
	ctx := context.Background()
	store, err := NewSQLiteBatchStore(nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })

	now := time.Now().UTC()
	batch := &SubagentBatch{
		BatchID:         "batch-shared-read",
		RootScopeID:     "parent-session",
		ParentSessionID: "parent-session",
		ExecutionMode:   ExecutionModeBackground,
		Status:          BatchRunning,
		TaskCount:       3,
		RunningCount:    1,
		HeartbeatAt:     now,
		CreatedAt:       now,
		UpdatedAt:       now,
		Version:         1,
	}
	created, err := store.CreateBatch(ctx, batch, []SubagentTaskRecord{
		{TaskID: "t-run", BatchID: batch.BatchID, OrderIndex: 1, UpdatedAt: now, Version: 1, ChildSessionID: "child-run", Status: TaskRunning},
		{TaskID: "t-done", BatchID: batch.BatchID, OrderIndex: 2, UpdatedAt: now, Version: 1, ChildSessionID: "child-done", Status: TaskSucceeded},
		{TaskID: "t-failed", BatchID: batch.BatchID, OrderIndex: 3, UpdatedAt: now, Version: 1, Status: TaskFailed},
	})
	require.NoError(t, err)
	require.True(t, created)

	// 另一个父会话的批次对本次读取不可见（不得跨会话取身份）。
	other := &SubagentBatch{
		BatchID:         "batch-other-parent",
		RootScopeID:     "other-session",
		ParentSessionID: "other-session",
		ExecutionMode:   ExecutionModeBackground,
		Status:          BatchRunning,
		TaskCount:       1,
		RunningCount:    1,
		HeartbeatAt:     now,
		CreatedAt:       now,
		UpdatedAt:       now,
		Version:         1,
	}
	_, err = store.CreateBatch(ctx, other, []SubagentTaskRecord{
		{TaskID: "t-other", BatchID: other.BatchID, OrderIndex: 1, UpdatedAt: now, Version: 1, Status: TaskRunning},
	})
	require.NoError(t, err)

	rows, err := ListTaskRowsForParent(ctx, store, "parent-session", false)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Equal(t, "batch-shared-read", rows[0].BatchID)
	require.Equal(t, "t-run", rows[0].Task.TaskID)
	require.Equal(t, "child-run", rows[0].Task.ChildSessionID)

	rows, err = ListTaskRowsForParent(ctx, store, "parent-session", true)
	require.NoError(t, err)
	require.Len(t, rows, 3)
	require.Equal(t, "t-failed", rows[2].Task.TaskID, "order_index order is preserved")

	rows, err = ListTaskRowsForParent(ctx, nil, "parent-session", true)
	require.NoError(t, err)
	require.Empty(t, rows)
	rows, err = ListTaskRowsForParent(ctx, store, "  ", true)
	require.NoError(t, err)
	require.Empty(t, rows)
}
