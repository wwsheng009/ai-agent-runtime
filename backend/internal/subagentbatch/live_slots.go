package subagentbatch

import (
	"context"
	"strings"
)

// LiveTaskFilter scopes a live-task count. RootScopeID matches the batch's
// root_scope_id (the scope supervision and the spawn gate key on);
// ParentSessionID matches the batch's immediate parent session. Empty fields are
// ignored, so an empty filter counts live tasks across the whole store.
type LiveTaskFilter struct {
	RootScopeID     string
	ParentSessionID string
}

// LiveTaskCounter is an optional BatchStore extension: it reports how many
// non-terminal task records a scope currently holds across every batch. Hosts
// use it to enforce the live-subagent ceiling (agents.maxThreads) on the batch
// path, so subagent_spawn_batch can no longer accumulate unbounded idle children the
// way the per-batch/global concurrency limiter alone allowed.
//
// It is deliberately an optional interface (checked by type assertion) rather
// than a method on BatchStore, so existing store implementations and test fakes
// keep compiling; a store that does not implement it simply reports no live
// count, which degrades the gate to registry-only counting.
type LiveTaskCounter interface {
	CountLiveTasks(ctx context.Context, filter LiveTaskFilter) (int, error)
}

// LiveTaskStatuses returns the task statuses that still hold a live slot. It is
// the exact complement of TaskStatus.Terminal(), so a task frees its slot the
// moment it settles.
func LiveTaskStatuses() []TaskStatus {
	return []TaskStatus{TaskPending, TaskReady, TaskRunning}
}

// LiveTaskStatusPlaceholders renders the live statuses as a comma-separated
// list of SQL placeholders for an IN (...) clause, with the matching args. It
// keeps the terminal-status boundary defined once (LiveTaskStatuses) instead of
// duplicated as a literal in each store query.
func LiveTaskStatusPlaceholders() (string, []interface{}) {
	statuses := LiveTaskStatuses()
	args := make([]interface{}, 0, len(statuses))
	parts := make([]string, 0, len(statuses))
	for _, status := range statuses {
		parts = append(parts, "?")
		args = append(args, string(status))
	}
	return strings.Join(parts, ","), args
}
