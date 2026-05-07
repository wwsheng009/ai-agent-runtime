package agentcontrol

import (
	"context"
	"strings"
	"time"
)

// TaskRecord is the storage-neutral AgentControl read model for work assigned
// to an agent. Workflow implementations can project their native task state
// into this shape without moving write ownership into AgentControl yet.
type TaskRecord struct {
	ID           string    `json:"id"`
	Workflow     string    `json:"workflow,omitempty"`
	TeamID       string    `json:"team_id,omitempty"`
	ParentTaskID string    `json:"parent_task_id,omitempty"`
	Assignee     string    `json:"assignee,omitempty"`
	SessionID    string    `json:"session_id,omitempty"`
	Path         string    `json:"path,omitempty"`
	Title        string    `json:"title,omitempty"`
	Summary      string    `json:"summary,omitempty"`
	Status       string    `json:"status,omitempty"`
	Priority     int       `json:"priority,omitempty"`
	CreatedAt    time.Time `json:"created_at,omitempty"`
	UpdatedAt    time.Time `json:"updated_at,omitempty"`
}

// TaskFilter describes AgentControl task registry read filters without tying
// callers to a workflow-specific storage table.
type TaskFilter struct {
	Workflow   string
	TeamID     string
	Assignee   string
	Status     []string
	PathPrefix string
	Limit      int
}

// TaskWakeFilter identifies the workflow-scoped task wake stream a scheduler
// or orchestrator wants to consume.
type TaskWakeFilter struct {
	Workflow string
	TeamID   string
}

// TaskWakeEvent is the AgentControl-shaped task lifecycle wake event. The
// sequence is scoped by the backing workflow and filter, so callers should pair
// it with the same TaskWakeFilter when doing durable catch-up.
type TaskWakeEvent struct {
	Seq       int64     `json:"seq,omitempty"`
	Workflow  string    `json:"workflow,omitempty"`
	TeamID    string    `json:"team_id,omitempty"`
	TaskID    string    `json:"task_id,omitempty"`
	Kind      string    `json:"kind,omitempty"`
	Status    string    `json:"status,omitempty"`
	CreatedAt time.Time `json:"created_at,omitempty"`
}

// TaskDependencyRecord is the storage-neutral AgentControl read model for a
// task graph dependency edge.
type TaskDependencyRecord struct {
	ID          string    `json:"id,omitempty"`
	Workflow    string    `json:"workflow,omitempty"`
	TeamID      string    `json:"team_id,omitempty"`
	TaskID      string    `json:"task_id,omitempty"`
	DependsOnID string    `json:"depends_on_id,omitempty"`
	CreatedAt   time.Time `json:"created_at,omitempty"`
}

// TaskDependencyFilter describes which task graph edges to read through the
// AgentControl dependency seam.
type TaskDependencyFilter struct {
	Workflow          string
	TeamID            string
	TaskID            string
	DependsOnID       string
	IncludeDependents bool
}

// TaskRegistryReader is the read side of the AgentControl task registry seam.
// Workflow-specific implementations can project their native task model into
// TaskRecord while AgentControl consumers depend only on this interface.
type TaskRegistryReader interface {
	ListAgentControlTasks(ctx context.Context, filter TaskFilter) ([]TaskRecord, error)
}

// TaskDependencyReader is the read side of the AgentControl task graph seam.
type TaskDependencyReader interface {
	ListAgentControlTaskDependencies(ctx context.Context, filter TaskDependencyFilter) ([]TaskDependencyRecord, error)
}

// TaskWakeWatcher exposes task lifecycle wake notifications through the
// AgentControl task seam instead of a workflow-specific watcher type.
type TaskWakeWatcher interface {
	WatchAgentControlTaskWake(ctx context.Context, filter TaskWakeFilter) (<-chan TaskWakeEvent, func())
}

// TaskWakeSequencer exposes the durable high-water mark for an AgentControl
// task wake stream.
type TaskWakeSequencer interface {
	LastAgentControlTaskWakeSeq(ctx context.Context, filter TaskWakeFilter) (int64, error)
}

// TaskWakeSource is the combined watcher/sequence substrate consumed by
// orchestrators that need durable task wake semantics.
type TaskWakeSource interface {
	TaskWakeWatcher
	TaskWakeSequencer
}

// TaskStatusUpdateRequest describes a storage-neutral task status transition
// request. Workflow adapters remain responsible for mapping status strings to
// their native task lifecycle types.
type TaskStatusUpdateRequest struct {
	ID       string
	Workflow string
	Status   string
	Summary  string
}

// TaskCreateRequest describes task creation through the AgentControl task
// registry write seam without depending on a workflow-specific task table.
type TaskCreateRequest struct {
	ID           string
	Workflow     string
	TeamID       string
	ParentTaskID string
	Title        string
	Goal         string
	Status       string
	Priority     int
	Assignee     string
	Inputs       []string
	ReadPaths    []string
	WritePaths   []string
	Deliverables []string
	Summary      string
	ResultRef    string
}

// TaskDependencyCreateRequest describes creating a dependency edge through the
// AgentControl task graph seam without depending on a workflow-specific graph
// table.
type TaskDependencyCreateRequest struct {
	Workflow    string
	TeamID      string
	TaskID      string
	DependsOnID string
}

// TaskReleaseRequest describes a lease release/status reset through the
// AgentControl task registry write seam.
type TaskReleaseRequest struct {
	ID       string
	Workflow string
	Status   string
	Summary  string
}

// TaskLeaseRenewRequest describes a task lease renewal through the
// AgentControl task registry write seam.
type TaskLeaseRenewRequest struct {
	ID         string
	Workflow   string
	LeaseUntil time.Time
}

// TaskClaimRequest describes a task claim attempt through the AgentControl
// task registry write seam.
type TaskClaimRequest struct {
	ID              string
	Workflow        string
	TeamID          string
	Assignee        string
	LeaseUntil      time.Time
	ExpectedVersion int64
	ReadPaths       []string
	WritePaths      []string
	UsePathClaims   bool
	WorkspaceRoot   string
}

// TaskTerminalUpdateRequest describes a terminal done/failed task transition
// through the AgentControl task registry write seam.
type TaskTerminalUpdateRequest struct {
	ID              string
	Workflow        string
	TeamID          string
	Status          string
	Summary         string
	ResultRef       *string
	TeammateID      string
	SkipStateUpdate bool
}

// TaskBlockRequest describes a blocked task transition through the
// AgentControl task registry write seam.
type TaskBlockRequest struct {
	ID              string
	Workflow        string
	Summary         string
	TeammateID      string
	SkipStateUpdate bool
}

// TaskRegistryStatusWriter is the first write-side AgentControl task seam.
// It intentionally starts with status updates so existing workflow stores can
// adopt it without moving create/claim/lease ownership in one step.
type TaskRegistryStatusWriter interface {
	UpdateAgentControlTaskStatus(ctx context.Context, request TaskStatusUpdateRequest) (*TaskRecord, error)
}

// TaskRegistryCreateWriter exposes task creation through the AgentControl task
// registry seam.
type TaskRegistryCreateWriter interface {
	CreateAgentControlTask(ctx context.Context, request TaskCreateRequest) (*TaskRecord, error)
}

// TaskDependencyCreateWriter exposes dependency edge creation through the
// AgentControl task graph seam.
type TaskDependencyCreateWriter interface {
	CreateAgentControlTaskDependency(ctx context.Context, request TaskDependencyCreateRequest) error
}

// TaskRegistryReleaseWriter exposes task release through the AgentControl task
// registry seam while workflow stores remain responsible for native lease
// bookkeeping.
type TaskRegistryReleaseWriter interface {
	ReleaseAgentControlTask(ctx context.Context, request TaskReleaseRequest) (*TaskRecord, error)
}

// TaskRegistryLeaseRenewWriter exposes task lease renewal through the
// AgentControl task registry seam.
type TaskRegistryLeaseRenewWriter interface {
	RenewAgentControlTaskLease(ctx context.Context, request TaskLeaseRenewRequest) (*TaskRecord, error)
}

// TaskRegistryClaimWriter exposes task claim attempts through the AgentControl
// task registry seam. The bool reports whether the optimistic claim succeeded.
type TaskRegistryClaimWriter interface {
	ClaimAgentControlTask(ctx context.Context, request TaskClaimRequest) (*TaskRecord, bool, error)
}

// TaskRegistryTerminalWriter exposes terminal task transitions through the
// AgentControl task registry seam.
type TaskRegistryTerminalWriter interface {
	UpdateAgentControlTaskTerminal(ctx context.Context, request TaskTerminalUpdateRequest) (*TaskRecord, error)
}

// TaskRegistryBlockWriter exposes blocked task transitions through the
// AgentControl task registry seam.
type TaskRegistryBlockWriter interface {
	BlockAgentControlTask(ctx context.Context, request TaskBlockRequest) (*TaskRecord, error)
}

// Normalize trims string fields without changing workflow ownership.
func (r TaskRecord) Normalize() TaskRecord {
	r.ID = strings.TrimSpace(r.ID)
	r.Workflow = strings.TrimSpace(r.Workflow)
	r.TeamID = strings.TrimSpace(r.TeamID)
	r.ParentTaskID = strings.TrimSpace(r.ParentTaskID)
	r.Assignee = strings.TrimSpace(r.Assignee)
	r.SessionID = strings.TrimSpace(r.SessionID)
	r.Path = strings.TrimSpace(r.Path)
	r.Title = strings.TrimSpace(r.Title)
	r.Summary = strings.TrimSpace(r.Summary)
	r.Status = strings.TrimSpace(r.Status)
	return r
}

// Normalize trims task wake filter fields.
func (f TaskWakeFilter) Normalize() TaskWakeFilter {
	f.Workflow = strings.TrimSpace(f.Workflow)
	f.TeamID = strings.TrimSpace(f.TeamID)
	return f
}

// Normalize trims task wake event fields.
func (e TaskWakeEvent) Normalize() TaskWakeEvent {
	e.Workflow = strings.TrimSpace(e.Workflow)
	e.TeamID = strings.TrimSpace(e.TeamID)
	e.TaskID = strings.TrimSpace(e.TaskID)
	e.Kind = strings.TrimSpace(e.Kind)
	e.Status = strings.TrimSpace(e.Status)
	return e
}

// Normalize trims task dependency record fields.
func (r TaskDependencyRecord) Normalize() TaskDependencyRecord {
	r.ID = strings.TrimSpace(r.ID)
	r.Workflow = strings.TrimSpace(r.Workflow)
	r.TeamID = strings.TrimSpace(r.TeamID)
	r.TaskID = strings.TrimSpace(r.TaskID)
	r.DependsOnID = strings.TrimSpace(r.DependsOnID)
	return r
}

// Normalize trims task dependency filter fields.
func (f TaskDependencyFilter) Normalize() TaskDependencyFilter {
	f.Workflow = strings.TrimSpace(f.Workflow)
	f.TeamID = strings.TrimSpace(f.TeamID)
	f.TaskID = strings.TrimSpace(f.TaskID)
	f.DependsOnID = strings.TrimSpace(f.DependsOnID)
	return f
}

// Normalize trims string fields in a task status update request.
func (r TaskStatusUpdateRequest) Normalize() TaskStatusUpdateRequest {
	r.ID = strings.TrimSpace(r.ID)
	r.Workflow = strings.TrimSpace(r.Workflow)
	r.Status = strings.TrimSpace(r.Status)
	r.Summary = strings.TrimSpace(r.Summary)
	return r
}

// Normalize trims string fields in a task creation request.
func (r TaskCreateRequest) Normalize() TaskCreateRequest {
	r.ID = strings.TrimSpace(r.ID)
	r.Workflow = strings.TrimSpace(r.Workflow)
	r.TeamID = strings.TrimSpace(r.TeamID)
	r.ParentTaskID = strings.TrimSpace(r.ParentTaskID)
	r.Title = strings.TrimSpace(r.Title)
	r.Goal = strings.TrimSpace(r.Goal)
	r.Status = strings.TrimSpace(r.Status)
	r.Assignee = strings.TrimSpace(r.Assignee)
	r.Summary = strings.TrimSpace(r.Summary)
	r.ResultRef = strings.TrimSpace(r.ResultRef)
	return r
}

// Normalize trims string fields in a dependency creation request.
func (r TaskDependencyCreateRequest) Normalize() TaskDependencyCreateRequest {
	r.Workflow = strings.TrimSpace(r.Workflow)
	r.TeamID = strings.TrimSpace(r.TeamID)
	r.TaskID = strings.TrimSpace(r.TaskID)
	r.DependsOnID = strings.TrimSpace(r.DependsOnID)
	return r
}

// Normalize trims string fields in a task release request.
func (r TaskReleaseRequest) Normalize() TaskReleaseRequest {
	r.ID = strings.TrimSpace(r.ID)
	r.Workflow = strings.TrimSpace(r.Workflow)
	r.Status = strings.TrimSpace(r.Status)
	r.Summary = strings.TrimSpace(r.Summary)
	return r
}

// Normalize trims string fields in a task lease renewal request.
func (r TaskLeaseRenewRequest) Normalize() TaskLeaseRenewRequest {
	r.ID = strings.TrimSpace(r.ID)
	r.Workflow = strings.TrimSpace(r.Workflow)
	return r
}

// Normalize trims string fields in a task claim request.
func (r TaskClaimRequest) Normalize() TaskClaimRequest {
	r.ID = strings.TrimSpace(r.ID)
	r.Workflow = strings.TrimSpace(r.Workflow)
	r.TeamID = strings.TrimSpace(r.TeamID)
	r.Assignee = strings.TrimSpace(r.Assignee)
	r.WorkspaceRoot = strings.TrimSpace(r.WorkspaceRoot)
	return r
}

// Normalize trims string fields in a terminal task update request.
func (r TaskTerminalUpdateRequest) Normalize() TaskTerminalUpdateRequest {
	r.ID = strings.TrimSpace(r.ID)
	r.Workflow = strings.TrimSpace(r.Workflow)
	r.TeamID = strings.TrimSpace(r.TeamID)
	r.Status = strings.TrimSpace(r.Status)
	r.Summary = strings.TrimSpace(r.Summary)
	r.TeammateID = strings.TrimSpace(r.TeammateID)
	return r
}

// Normalize trims string fields in a task block request.
func (r TaskBlockRequest) Normalize() TaskBlockRequest {
	r.ID = strings.TrimSpace(r.ID)
	r.Workflow = strings.TrimSpace(r.Workflow)
	r.Summary = strings.TrimSpace(r.Summary)
	r.TeammateID = strings.TrimSpace(r.TeammateID)
	return r
}
