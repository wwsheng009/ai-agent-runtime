package subagentbatch

import (
	"context"
	"time"
)

// TerminalBatchStatuses returns the batch statuses that can no longer
// transition. Only these are eligible for retention pruning, so the sweep can
// never delete a batch whose children may still be running.
func TerminalBatchStatuses() []BatchStatus {
	return []BatchStatus{BatchCompleted, BatchFailed, BatchCanceled, BatchTimedOut, BatchOrphaned}
}

// TerminalBatchPruner is an optional BatchStore extension: it deletes terminal
// batches (and their task rows) whose finished_at predates the cutoff. It is the
// batch-ledger counterpart to the registry terminal retention, so the durable
// batch history — and the /agents rows projected from it — cannot grow without
// bound. Only terminal batches are eligible, so it can never delete live work.
type TerminalBatchPruner interface {
	// PruneTerminalBatches removes at most `limit` terminal batches older than
	// olderThan and returns how many batches were deleted. A zero cutoff or a
	// non-positive limit are no-ops.
	PruneTerminalBatches(ctx context.Context, olderThan time.Time, limit int) (int64, error)
}

// DefaultTerminalBatchPruneLimit bounds one prune pass so a large backlog is
// drained across several reconcile ticks instead of one long transaction.
const DefaultTerminalBatchPruneLimit = 500
