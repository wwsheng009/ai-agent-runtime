package subagentbatch

import (
	"context"
	"strings"
)

// TaskRow couples one durable batch task with the batch that owns it.
type TaskRow struct {
	BatchID string
	Task    SubagentTaskRecord
}

// ListTaskRowsForParent returns the durable task rows of one parent session's
// batches (newest batch first, tasks in order_index order). Terminal tasks are
// skipped unless includeTerminal.
//
// This is the single ledger read shared by every agent-list surface (local chat
// host and HTTP host) so "which batch rows does an agent list show" cannot
// drift between hosts again — the same reason the Find* lookups exist for
// single-id reads. Scope discipline matches Find*: only the parent session's
// own batches are scanned; callers must resolve the caller session themselves
// and never accept a model-supplied scope.
func ListTaskRowsForParent(ctx context.Context, store BatchStore, parentSessionID string, includeTerminal bool) ([]TaskRow, error) {
	parentSessionID = strings.TrimSpace(parentSessionID)
	if store == nil || parentSessionID == "" {
		return nil, nil
	}
	batches, err := store.ListBatches(ctx, BatchFilter{ParentSessionID: parentSessionID})
	if err != nil {
		return nil, err
	}
	rows := make([]TaskRow, 0)
	for _, batch := range batches {
		batchID := strings.TrimSpace(batch.BatchID)
		if batchID == "" {
			continue
		}
		tasks, err := store.ListTasks(ctx, batchID)
		if err != nil {
			return nil, err
		}
		for _, task := range tasks {
			if !includeTerminal && task.Status.Terminal() {
				continue
			}
			rows = append(rows, TaskRow{BatchID: batchID, Task: task})
		}
	}
	return rows, nil
}
