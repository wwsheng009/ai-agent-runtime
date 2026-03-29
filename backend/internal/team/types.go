package team

import "time"

// TeamStatus represents the lifecycle state of a team.
type TeamStatus string

const (
	TeamStatusActive TeamStatus = "active"
	TeamStatusPaused TeamStatus = "paused"
	TeamStatusDone   TeamStatus = "done"
	TeamStatusFailed TeamStatus = "failed"
)

// TeammateState represents the current availability of a teammate.
type TeammateState string

const (
	TeammateStateIdle    TeammateState = "idle"
	TeammateStateBusy    TeammateState = "busy"
	TeammateStateBlocked TeammateState = "blocked"
	TeammateStateOffline TeammateState = "offline"
)

// TaskStatus represents the lifecycle state of a task.
type TaskStatus string

const (
	TaskStatusPending   TaskStatus = "pending"
	TaskStatusReady     TaskStatus = "ready"
	TaskStatusRunning   TaskStatus = "running"
	TaskStatusBlocked   TaskStatus = "blocked"
	TaskStatusDone      TaskStatus = "done"
	TaskStatusFailed    TaskStatus = "failed"
	TaskStatusCancelled TaskStatus = "cancelled"
)

// PathClaimMode indicates whether a path is claimed for read or write.
type PathClaimMode string

const (
	PathClaimRead  PathClaimMode = "read"
	PathClaimWrite PathClaimMode = "write"
)

// Team describes a collaborating group of agents.
type Team struct {
	ID            string     `json:"id"`
	WorkspaceID   string     `json:"workspace_id"`
	LeadSessionID string     `json:"lead_session_id"`
	Status        TeamStatus `json:"status"`
	Strategy      string     `json:"strategy"`
	MaxTeammates  int        `json:"max_teammates"`
	MaxWriters    int        `json:"max_writers"`
	CreatedAt     time.Time  `json:"created_at"`
	UpdatedAt     time.Time  `json:"updated_at"`
}

// Teammate represents an individual agent session inside a team.
type Teammate struct {
	ID            string        `json:"id"`
	TeamID        string        `json:"team_id"`
	Name          string        `json:"name"`
	Profile       string        `json:"profile"`
	SessionID     string        `json:"session_id"`
	State         TeammateState `json:"state"`
	LastHeartbeat time.Time     `json:"last_heartbeat"`
	Capabilities  []string      `json:"capabilities"`
	CreatedAt     time.Time     `json:"created_at"`
	UpdatedAt     time.Time     `json:"updated_at"`
}

// Task represents a unit of work assigned to a teammate.
type Task struct {
	ID           string     `json:"id"`
	TeamID       string     `json:"team_id"`
	ParentTaskID *string    `json:"parent_task_id,omitempty"`
	Title        string     `json:"title"`
	Goal         string     `json:"goal"`
	Inputs       []string   `json:"inputs,omitempty"`
	Status       TaskStatus `json:"status"`
	Priority     int        `json:"priority"`
	Assignee     *string    `json:"assignee,omitempty"`
	LeaseUntil   *time.Time `json:"lease_until,omitempty"`
	RetryCount   int        `json:"retry_count"`
	ReadPaths    []string   `json:"read_paths,omitempty"`
	WritePaths   []string   `json:"write_paths,omitempty"`
	Deliverables []string   `json:"deliverables,omitempty"`
	Summary      string     `json:"summary,omitempty"`
	ResultRef    *string    `json:"result_ref,omitempty"`
	Version      int64      `json:"version"`
	CreatedAt    time.Time  `json:"created_at"`
	UpdatedAt    time.Time  `json:"updated_at"`
}

// MailMessage represents a message exchanged inside the team mailbox.
type MailMessage struct {
	ID        string                 `json:"id"`
	TeamID    string                 `json:"team_id"`
	FromAgent string                 `json:"from_agent"`
	ToAgent   string                 `json:"to_agent"`
	TaskID    *string                `json:"task_id,omitempty"`
	Kind      string                 `json:"kind"`
	Body      string                 `json:"body"`
	Metadata  map[string]interface{} `json:"metadata,omitempty"`
	CreatedAt time.Time              `json:"created_at"`
	AckedAt   *time.Time             `json:"acked_at,omitempty"`
}

// MailReceipt records that an agent has acknowledged a mailbox message.
type MailReceipt struct {
	MessageID string    `json:"message_id"`
	TeamID    string    `json:"team_id"`
	AgentID   string    `json:"agent_id"`
	AckedAt   time.Time `json:"acked_at"`
}

// PathClaim describes a claimed filesystem path for a task.
type PathClaim struct {
	ID           string        `json:"id"`
	TeamID       string        `json:"team_id"`
	TaskID       string        `json:"task_id"`
	OwnerAgentID string        `json:"owner_agent_id"`
	Path         string        `json:"path"`
	Mode         PathClaimMode `json:"mode"`
	LeaseUntil   time.Time     `json:"lease_until"`
}

// TaskDependency describes an edge in the task DAG.
type TaskDependency struct {
	TaskID      string    `json:"task_id"`
	DependsOnID string    `json:"depends_on_id"`
	CreatedAt   time.Time `json:"created_at"`
}

// TeamFilter allows filtering teams in the store.
type TeamFilter struct {
	WorkspaceID string
	Status      TeamStatus
	Limit       int
}

// TaskFilter allows filtering tasks in the store.
type TaskFilter struct {
	TeamID       string
	Status       []TaskStatus
	Assignee     *string
	ParentTaskID *string
	Limit        int
}

// MailFilter allows filtering mailbox messages.
type MailFilter struct {
	TeamID           string
	FromAgent        string
	ToAgent          string
	TaskID           string
	Kind             string
	UnreadOnly       bool
	IncludeBroadcast bool
	Since            *time.Time
	Limit            int
}

// Conflict describes a path claim conflict.
type Conflict struct {
	Path           string        `json:"path"`
	ExistingPath   string        `json:"existing_path"`
	ExistingOwner  string        `json:"existing_owner"`
	ExistingTaskID string        `json:"existing_task_id"`
	ExistingMode   PathClaimMode `json:"existing_mode"`
}

// Assignment links a task with the teammate chosen to work on it.
type Assignment struct {
	Task     Task     `json:"task"`
	Teammate Teammate `json:"teammate"`
}

// LeaseReclaim describes a reclaimed task lease.
type LeaseReclaim struct {
	Task               Task       `json:"task"`
	PreviousAssignee   string     `json:"previous_assignee,omitempty"`
	PreviousLeaseUntil *time.Time `json:"previous_lease_until,omitempty"`
}
