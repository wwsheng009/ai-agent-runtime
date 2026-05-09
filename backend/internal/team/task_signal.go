package team

import (
	"context"
	"time"
)

const (
	TaskSignalTaskCreated      = "task.created"
	TaskSignalTaskUpdated      = "task.updated"
	TaskSignalTaskStatus       = "task.status"
	TaskSignalTaskRetry        = "task.retry"
	TaskSignalTasksMarkedReady = "tasks.ready"
	TaskSignalTaskClaimed      = "task.claimed"
	TaskSignalTaskLeaseRenewed = "task.lease_renewed"
	TaskSignalTaskReleased     = "task.released"
	TaskSignalTaskBlocked      = "task.blocked"
)

// TaskSignal is an internal durable wake signal for task lifecycle changes.
// It is intentionally separate from MailMessage so scheduler wakeups do not
// pollute user-visible team mailbox counts, digests, or unread state.
type TaskSignal struct {
	Seq       int64      `json:"seq,omitempty"`
	TeamSeq   int64      `json:"team_seq,omitempty"`
	Workflow  string     `json:"workflow,omitempty"`
	TeamID    string     `json:"team_id"`
	TaskID    string     `json:"task_id,omitempty"`
	Kind      string     `json:"kind"`
	Status    TaskStatus `json:"status,omitempty"`
	CreatedAt time.Time  `json:"created_at"`
}

// TaskWatcherStore exposes low-latency task lifecycle notifications from
// stores that can wake waiters after durable signal inserts.
type TaskWatcherStore interface {
	WatchTaskSignals(ctx context.Context, teamID string) (<-chan TaskSignal, func())
}

// TaskSequenceStore exposes the durable high-water mark for task lifecycle
// signals in a team.
type TaskSequenceStore interface {
	LastTaskSignalSeq(ctx context.Context, teamID string) (int64, error)
}

// AgentControlTaskWatcherStore exposes task wake notifications through the
// AgentControl wake projection rather than the team-native signal table.
type AgentControlTaskWatcherStore interface {
	WatchAgentControlTaskSignals(ctx context.Context, workflow, teamID string) (<-chan TaskSignal, func())
}

// AgentControlTaskSequenceStore exposes the durable AgentControl task wake
// high-water mark.
type AgentControlTaskSequenceStore interface {
	LastAgentControlTaskSignalSeq(ctx context.Context, workflow, teamID string) (int64, error)
}
