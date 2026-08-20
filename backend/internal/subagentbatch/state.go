package subagentbatch

import (
	"fmt"
	"strings"
)

// batchTransitions defines the allowed monotonic batch state graph
// (plan §4.3: queued -> running -> partially_completed -> terminal, plus
// cancel/clock transitions that can arrive from any non-terminal state).
var batchTransitions = map[BatchStatus]map[BatchStatus]bool{
	BatchQueued: {
		BatchRunning:            true,
		BatchPartiallyCompleted: true, // degenerate: all tasks terminal in one pass
		BatchCanceled:           true,
		BatchTimedOut:           true,
		BatchOrphaned:           true,
		BatchFailed:             true, // preflight/validation failure
	},
	BatchRunning: {
		BatchRunning:            true, // re-heartbeat/refresh keeps identity
		BatchPartiallyCompleted: true,
		BatchCompleted:          true,
		BatchFailed:             true,
		BatchCanceled:           true,
		BatchTimedOut:           true,
		BatchOrphaned:           true,
	},
	BatchPartiallyCompleted: {
		BatchPartiallyCompleted: true,
		BatchCompleted:          true,
		BatchFailed:             true,
		BatchCanceled:           true,
		BatchTimedOut:           true,
		BatchOrphaned:           true,
	},
	BatchCompleted: {},
	BatchFailed:    {},
	BatchCanceled:  {},
	BatchTimedOut:  {},
	BatchOrphaned:  {},
}

// taskTransitions defines the allowed per-task monotonic state graph.
var taskTransitions = map[TaskStatus]map[TaskStatus]bool{
	TaskPending: {
		TaskReady:    true,
		TaskRunning:  true, // degenerate fast-path
		TaskCanceled: true,
		TaskTimedOut: true, // batch deadline passed before the task ever started
		TaskSkipped:  true,
	},
	TaskReady: {
		TaskRunning:  true,
		TaskCanceled: true,
		TaskSkipped:  true,
		TaskTimedOut: true,
	},
	TaskRunning: {
		TaskRunning:   true, // progress refresh
		TaskSucceeded: true,
		TaskFailed:    true,
		TaskCanceled:  true,
		TaskTimedOut:  true,
	},
	TaskSucceeded: {},
	TaskFailed:    {},
	TaskCanceled:  {},
	TaskTimedOut:  {},
	TaskSkipped:   {},
}

// ValidateBatchTransition returns an error when from->to is not allowed.
// from == to (non-terminal refresh) is permitted so heartbeat/progress updates
// can reuse the same mutation path without bumping a distinct transition.
func ValidateBatchTransition(from, to BatchStatus) error {
	if from == to {
		return nil
	}
	if allowed, ok := batchTransitions[from]; ok && allowed[to] {
		return nil
	}
	return fmt.Errorf("subagentbatch: invalid batch transition %s -> %s", from, to)
}

// ValidateTaskTransition is the task-vocabulary counterpart.
func ValidateTaskTransition(from, to TaskStatus) error {
	if from == to {
		return nil
	}
	if allowed, ok := taskTransitions[from]; ok && allowed[to] {
		return nil
	}
	return fmt.Errorf("subagentbatch: invalid task transition %s -> %s", from, to)
}

// CanonicalErrorClass buckets an error string into a low-cardinality class for
// lifecycle records and dashboards.
func CanonicalErrorClass(err error) string {
	if err == nil {
		return ""
	}
	msg := strings.ToLower(err.Error())
	switch {
	case strings.Contains(msg, "timeout"), strings.Contains(msg, "context deadline"), strings.Contains(msg, "timed out"):
		return "timeout"
	case strings.Contains(msg, "cancel"):
		return "canceled"
	case strings.Contains(msg, "depth"):
		return "depth_limit"
	case strings.Contains(msg, "denied"), strings.Contains(msg, "policy"):
		return "policy"
	case strings.Contains(msg, "provider"), strings.Contains(msg, "api"):
		return "provider"
	case strings.Contains(msg, "mcp"):
		return "mcp"
	case strings.Contains(msg, "no such host"), strings.Contains(msg, "connect"):
		return "network"
	default:
		return "error"
	}
}
