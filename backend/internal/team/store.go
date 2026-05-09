package team

import (
	"context"
	"time"
)

// StoreConfig controls the persistence settings for the team store.
type StoreConfig struct {
	Path string
	DSN  string
}

// TaskDependencyReaderStore exposes full dependency edge records for stores
// that can return graph metadata beyond the legacy id-only dependency APIs.
type TaskDependencyReaderStore interface {
	ListTaskDependencyRecords(ctx context.Context, filter TaskDependencyFilter) ([]TaskDependency, error)
}

// TaskGraphEventReaderStore exposes a global task graph event cursor for
// stores that can read task-related team events across teams.
type TaskGraphEventReaderStore interface {
	ListTaskGraphEvents(ctx context.Context, filter TaskGraphEventFilter) ([]TaskGraphEvent, error)
}

// AgentControlMailboxWatcherStore exposes mailbox wake notifications through
// the AgentControl wake projection rather than the team-native mailbox table.
type AgentControlMailboxWatcherStore interface {
	WatchAgentControlMailboxSignals(ctx context.Context, workflow, teamID string) (<-chan MailMessage, func())
}

// AgentControlMailboxSequenceStore exposes the AgentControl mailbox wake
// high-water mark.
type AgentControlMailboxSequenceStore interface {
	LastAgentControlMailboxSignalSeq(ctx context.Context, workflow, teamID string) (int64, error)
}

// Store defines persistence operations required by the team subsystem.
type Store interface {
	Close() error

	CreateTeam(ctx context.Context, team Team) (string, error)
	GetTeam(ctx context.Context, id string) (*Team, error)
	ListTeams(ctx context.Context, filter TeamFilter) ([]Team, error)
	UpdateTeam(ctx context.Context, team Team) error
	UpdateTeamStatus(ctx context.Context, id string, status TeamStatus) error
	DeleteTeam(ctx context.Context, id string) error
	ListTeamIDs(ctx context.Context) ([]string, error)

	UpsertTeammate(ctx context.Context, teammate Teammate) (string, error)
	GetTeammate(ctx context.Context, id string) (*Teammate, error)
	ListTeammates(ctx context.Context, teamID string) ([]Teammate, error)
	UpdateTeammateState(ctx context.Context, id string, state TeammateState) error
	UpdateTeammateHeartbeat(ctx context.Context, id string, heartbeat time.Time) error

	CreateTask(ctx context.Context, task Task) (string, error)
	GetTask(ctx context.Context, id string) (*Task, error)
	ListTasks(ctx context.Context, filter TaskFilter) ([]Task, error)
	UpdateTask(ctx context.Context, task Task) error
	UpdateTaskStatus(ctx context.Context, id string, status TaskStatus, summary string) error
	IncrementTaskRetry(ctx context.Context, id string) error
	MarkReadyTasks(ctx context.Context, teamID string) (int64, error)
	ClaimTask(ctx context.Context, id string, assignee string, leaseUntil time.Time, expectedVersion int64) (bool, error)
	ClaimTaskWithPathClaims(ctx context.Context, task Task, assignee string, leaseUntil time.Time, workspaceRoot string) (bool, error)
	RenewTaskLease(ctx context.Context, id string, leaseUntil time.Time) error
	ReleaseTask(ctx context.Context, id string, status TaskStatus) error
	BlockTask(ctx context.Context, id string, summary string) error

	AddTaskDependency(ctx context.Context, taskID, dependsOnID string) error
	ListTaskDependencies(ctx context.Context, taskID string) ([]string, error)
	ListTaskDependents(ctx context.Context, taskID string) ([]string, error)

	InsertMail(ctx context.Context, message MailMessage) (string, error)
	ListMail(ctx context.Context, filter MailFilter) ([]MailMessage, error)
	AckMail(ctx context.Context, teamID, messageID string, ackedAt time.Time) error
	RecordMailReceipt(ctx context.Context, receipt MailReceipt) error
	ListMailReceipts(ctx context.Context, teamID, messageID string) ([]MailReceipt, error)

	ListPathClaims(ctx context.Context, teamID string) ([]PathClaim, error)
	CreatePathClaims(ctx context.Context, claims []PathClaim) error
	ReleasePathClaimsByTask(ctx context.Context, taskID string) error
	RenewPathClaimsByTask(ctx context.Context, taskID string, leaseUntil time.Time) error
	DeleteExpiredPathClaims(ctx context.Context, teamID string, asOf time.Time) (int64, error)

	AppendTeamEvent(ctx context.Context, event TeamEvent) (int64, error)
	ListTeamEvents(ctx context.Context, filter TeamEventFilter) ([]TeamEventRecord, error)
}
