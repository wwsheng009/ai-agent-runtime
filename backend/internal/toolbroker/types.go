package toolbroker

import (
	"context"
	"time"

	"github.com/ai-gateway/ai-agent-runtime/internal/background"
	"github.com/ai-gateway/ai-agent-runtime/internal/team"
)

// UserQuestionRequest captures a prompt that needs user input.
type UserQuestionRequest struct {
	ID          string     `json:"id"`
	SessionID   string     `json:"session_id"`
	ToolCallID  string     `json:"tool_call_id,omitempty"`
	Prompt      string     `json:"prompt"`
	Suggestions []string   `json:"suggestions,omitempty"`
	Required    bool       `json:"required"`
	CreatedAt   time.Time  `json:"created_at,omitempty"`
	ExpiresAt   *time.Time `json:"expires_at,omitempty"`
}

// AskUserQuestionArgs describes the ask_user_question tool input.
type AskUserQuestionArgs struct {
	Prompt      string   `json:"prompt"`
	Suggestions []string `json:"suggestions,omitempty"`
	Required    bool     `json:"required"`
}

// AskUserQuestionResult is returned when the user answers.
type AskUserQuestionResult struct {
	QuestionID string `json:"question_id"`
	Answer     string `json:"answer"`
}

// BackgroundTaskArgs describes background task submission.
type BackgroundTaskArgs = background.BackgroundTaskArgs

// BackgroundTaskResult reports a submitted job.
type BackgroundTaskResult = background.BackgroundTaskResult

// TaskOutputArgs reads task output from an offset.
type TaskOutputArgs = background.TaskOutputArgs

// TaskOutputResult returns output chunk information.
type TaskOutputResult = background.TaskOutputResult

// SpawnTeamArgs describes a request to create a team plus optional teammates/tasks.
type SpawnTeamArgs struct {
	TeamID        string              `json:"team_id,omitempty"`
	WorkspaceID   string              `json:"workspace_id,omitempty"`
	LeadSessionID string              `json:"lead_session_id,omitempty"`
	Strategy      string              `json:"strategy,omitempty"`
	Status        string              `json:"status,omitempty"`
	MaxTeammates  int                 `json:"max_teammates,omitempty"`
	MaxWriters    int                 `json:"max_writers,omitempty"`
	AllowExisting *bool               `json:"allow_existing,omitempty"`
	AutoStart     *bool               `json:"auto_start,omitempty"`
	Teammates     []SpawnTeammateSpec `json:"teammates,omitempty"`
	Tasks         []SpawnTaskSpec     `json:"tasks,omitempty"`
}

// SpawnTeammateSpec describes a teammate record to upsert.
type SpawnTeammateSpec struct {
	ID           string   `json:"id,omitempty"`
	Name         string   `json:"name,omitempty"`
	Profile      string   `json:"profile,omitempty"`
	SessionID    string   `json:"session_id,omitempty"`
	State        string   `json:"state,omitempty"`
	Capabilities []string `json:"capabilities,omitempty"`
}

// SpawnTaskSpec describes a task to create in the team.
type SpawnTaskSpec struct {
	ID           string   `json:"id,omitempty"`
	Title        string   `json:"title,omitempty"`
	Goal         string   `json:"goal,omitempty"`
	Inputs       []string `json:"inputs,omitempty"`
	ReadPaths    []string `json:"read_paths,omitempty"`
	WritePaths   []string `json:"write_paths,omitempty"`
	Deliverables []string `json:"deliverables,omitempty"`
	Priority     int      `json:"priority,omitempty"`
	Assignee     string   `json:"assignee,omitempty"`
	DependsOn    []string `json:"depends_on,omitempty"`
}

// SpawnTeamResult returns created entities for a spawn_team call.
type SpawnTeamResult struct {
	TeamID        string   `json:"team_id"`
	CreatedTeam   bool     `json:"created_team"`
	AutoStarted   bool     `json:"auto_started"`
	TeammateIDs   []string `json:"teammate_ids,omitempty"`
	TaskIDs       []string `json:"task_ids,omitempty"`
	TeammateCount int      `json:"teammate_count"`
	TaskCount     int      `json:"task_count"`
}

// TeamMailboxDispatcher delivers mailbox events to active team sessions.
type TeamMailboxDispatcher interface {
	DispatchTeamMailboxMessage(ctx context.Context, message team.MailMessage) error
}

// SendTeamMessageArgs describes mailbox writes for a team run.
type SendTeamMessageArgs struct {
	TeamID   string                 `json:"team_id,omitempty"`
	ToAgent  string                 `json:"to_agent,omitempty"`
	Kind     string                 `json:"kind,omitempty"`
	Body     string                 `json:"body"`
	TaskID   string                 `json:"task_id,omitempty"`
	Metadata map[string]interface{} `json:"metadata,omitempty"`
}

// SendTeamMessageResult reports the inserted mailbox message.
type SendTeamMessageResult struct {
	MessageID string `json:"message_id"`
	TeamID    string `json:"team_id"`
	FromAgent string `json:"from_agent"`
	ToAgent   string `json:"to_agent"`
	Kind      string `json:"kind"`
	TaskID    string `json:"task_id,omitempty"`
}

// ReadMailboxDigestArgs describes a request for unread mailbox context.
type ReadMailboxDigestArgs struct {
	TeamID   string `json:"team_id,omitempty"`
	AgentID  string `json:"agent_id,omitempty"`
	Limit    int    `json:"limit,omitempty"`
	MarkRead *bool  `json:"mark_read,omitempty"`
}

// ReadMailboxDigestResult returns the current digest for a teammate.
type ReadMailboxDigestResult struct {
	TeamID       string   `json:"team_id"`
	AgentID      string   `json:"agent_id"`
	Digest       string   `json:"digest"`
	MessageIDs   []string `json:"message_ids,omitempty"`
	MessageCount int      `json:"message_count"`
	MarkedRead   bool     `json:"marked_read"`
}

// ReadTaskSpecArgs describes task lookup for team execution.
type ReadTaskSpecArgs struct {
	TeamID string `json:"team_id,omitempty"`
	TaskID string `json:"task_id,omitempty"`
}

// ReadTaskSpecResult returns a structured task spec.
type ReadTaskSpecResult struct {
	TaskID       string   `json:"task_id"`
	TeamID       string   `json:"team_id"`
	Title        string   `json:"title,omitempty"`
	Goal         string   `json:"goal,omitempty"`
	Inputs       []string `json:"inputs,omitempty"`
	Status       string   `json:"status,omitempty"`
	Priority     int      `json:"priority,omitempty"`
	Assignee     string   `json:"assignee,omitempty"`
	ReadPaths    []string `json:"read_paths,omitempty"`
	WritePaths   []string `json:"write_paths,omitempty"`
	Deliverables []string `json:"deliverables,omitempty"`
	Summary      string   `json:"summary,omitempty"`
	ResultRef    string   `json:"result_ref,omitempty"`
}

// ReadTaskContextArgs describes a request for richer task execution context.
type ReadTaskContextArgs struct {
	TeamID              string `json:"team_id,omitempty"`
	TaskID              string `json:"task_id,omitempty"`
	IncludeDependencies *bool  `json:"include_dependencies,omitempty"`
	IncludeMailbox      *bool  `json:"include_mailbox,omitempty"`
	MailboxLimit        int    `json:"mailbox_limit,omitempty"`
	MarkRead            *bool  `json:"mark_read,omitempty"`
	ContextBudget       int    `json:"context_budget,omitempty"`
}

// ReadTaskContextResult returns structured task context for a team run.
type ReadTaskContextResult struct {
	Spec          ReadTaskSpecResult `json:"spec"`
	TeamContext   string             `json:"team_context,omitempty"`
	MailboxDigest string             `json:"mailbox_digest,omitempty"`
	MessageIDs    []string           `json:"message_ids,omitempty"`
	MessageCount  int                `json:"message_count,omitempty"`
	MarkedRead    bool               `json:"marked_read,omitempty"`
	Dependencies  []string           `json:"dependencies,omitempty"`
	Dependents    []string           `json:"dependents,omitempty"`
}

// ReportTaskOutcomeArgs reports a structured task outcome for the current team task.
type ReportTaskOutcomeArgs struct {
	TeamID     string `json:"team_id,omitempty"`
	TaskID     string `json:"task_id,omitempty"`
	TaskStatus string `json:"task_status,omitempty"`
	Summary    string `json:"summary,omitempty"`
	Blocker    string `json:"blocker,omitempty"`
	HandoffTo  string `json:"handoff_to,omitempty"`
	ResultRef  string `json:"result_ref,omitempty"`
	NotifyLead *bool  `json:"notify_lead,omitempty"`
	AutoReplan *bool  `json:"auto_replan,omitempty"`
}

// ReportTaskOutcomeResult reports the stored task outcome and any follow-up work.
type ReportTaskOutcomeResult struct {
	TaskID          string   `json:"task_id"`
	TeamID          string   `json:"team_id"`
	Status          string   `json:"status"`
	Outcome         string   `json:"outcome,omitempty"`
	Summary         string   `json:"summary,omitempty"`
	Blocker         string   `json:"blocker,omitempty"`
	ResultRef       string   `json:"result_ref,omitempty"`
	BlockedBy       string   `json:"blocked_by,omitempty"`
	HandoffTo       string   `json:"handoff_to,omitempty"`
	MessageID       string   `json:"message_id,omitempty"`
	Replanned       bool     `json:"replanned"`
	PlannedTaskIDs  []string `json:"planned_task_ids,omitempty"`
	DependencyCount int      `json:"dependency_count,omitempty"`
	ReplanError     string   `json:"replan_error,omitempty"`
}

// BlockCurrentTaskArgs marks the current team task as blocked.
type BlockCurrentTaskArgs = ReportTaskOutcomeArgs

// BlockCurrentTaskResult reports the blocked task outcome.
type BlockCurrentTaskResult = ReportTaskOutcomeResult

// UserInputHandler handles user input requests.
type UserInputHandler interface {
	AskUserQuestion(ctx context.Context, req UserQuestionRequest) (string, error)
}

// SpawnAgentArgs describes a lightweight child-agent session request.
type SpawnAgentArgs struct {
	ID          string `json:"id,omitempty"`
	SessionID   string `json:"session_id,omitempty"`
	Message     string `json:"message,omitempty"`
	AgentType   string `json:"agent_type,omitempty"`
	Model       string `json:"model,omitempty"`
	ForkContext *bool  `json:"fork_context,omitempty"`
}

// SendAgentInputArgs describes a follow-up input for an existing child agent.
type SendAgentInputArgs struct {
	ID        string `json:"id,omitempty"`
	SessionID string `json:"session_id,omitempty"`
	Message   string `json:"message,omitempty"`
	Interrupt *bool  `json:"interrupt,omitempty"`
}

// WaitAgentArgs waits for a child agent to reach an idle or blocked state.
type WaitAgentArgs struct {
	ID         string   `json:"id,omitempty"`
	SessionID  string   `json:"session_id,omitempty"`
	IDs        []string `json:"ids,omitempty"`
	SessionIDs []string `json:"session_ids,omitempty"`
	TimeoutMs  int      `json:"timeout_ms,omitempty"`
}

// AgentStatusResult returns the current state of a lightweight child agent session.
type AgentStatusResult struct {
	ID                 string `json:"id"`
	SessionID          string `json:"session_id"`
	ParentSessionID    string `json:"parent_session_id,omitempty"`
	AgentType          string `json:"agent_type,omitempty"`
	Status             string `json:"status"`
	Exists             bool   `json:"exists"`
	Created            bool   `json:"created,omitempty"`
	Queued             bool   `json:"queued,omitempty"`
	TimedOut           bool   `json:"timed_out,omitempty"`
	PendingApproval    bool   `json:"pending_approval,omitempty"`
	PendingQuestion    bool   `json:"pending_question,omitempty"`
	MessageCount       int    `json:"message_count,omitempty"`
	Output             string `json:"output,omitempty"`
	Error              string `json:"error,omitempty"`
	SessionState       string `json:"session_state,omitempty"`
	CurrentTurnID      string `json:"current_turn_id,omitempty"`
	PendingToolName    string `json:"pending_tool_name,omitempty"`
	PendingToolCallID  string `json:"pending_tool_call_id,omitempty"`
	LastMessageRole    string `json:"last_message_role,omitempty"`
	LastMessagePreview string `json:"last_message_preview,omitempty"`
}

// AgentWaitResult reports the outcome of waiting on one or more child agent sessions.
type AgentWaitResult struct {
	Agent            *AgentStatusResult  `json:"agent,omitempty"`
	Agents           []AgentStatusResult `json:"agents,omitempty"`
	MatchedID        string              `json:"matched_id,omitempty"`
	MatchedSessionID string              `json:"matched_session_id,omitempty"`
	TimedOut         bool                `json:"timed_out,omitempty"`
	ReadyCount       int                 `json:"ready_count,omitempty"`
	PendingCount     int                 `json:"pending_count,omitempty"`
}

// ReadAgentEventsArgs reads child-agent runtime events from the session event store.
type ReadAgentEventsArgs struct {
	ID        string `json:"id,omitempty"`
	SessionID string `json:"session_id,omitempty"`
	AfterSeq  int64  `json:"after_seq,omitempty"`
	Limit     int    `json:"limit,omitempty"`
	WaitMs    int    `json:"wait_ms,omitempty"`
}

// AgentEventItem is a lightweight runtime event view for child-agent sessions.
type AgentEventItem struct {
	Seq       int64                  `json:"seq,omitempty"`
	Type      string                 `json:"type"`
	TraceID   string                 `json:"trace_id,omitempty"`
	SessionID string                 `json:"session_id,omitempty"`
	ToolName  string                 `json:"tool_name,omitempty"`
	AgentName string                 `json:"agent_name,omitempty"`
	Timestamp time.Time              `json:"timestamp"`
	Payload   map[string]interface{} `json:"payload,omitempty"`
}

// AgentEventsResult returns recent runtime events for a child agent session.
type AgentEventsResult struct {
	SessionID string           `json:"session_id"`
	Events    []AgentEventItem `json:"events,omitempty"`
	Count     int              `json:"count"`
	LatestSeq int64            `json:"latest_seq,omitempty"`
	TimedOut  bool             `json:"timed_out,omitempty"`
}

// AgentSessionController provides lightweight child-agent lifecycle operations.
type AgentSessionController interface {
	Spawn(ctx context.Context, parentSessionID string, args SpawnAgentArgs) (*AgentStatusResult, error)
	SendInput(ctx context.Context, args SendAgentInputArgs) (*AgentStatusResult, error)
	Wait(ctx context.Context, args WaitAgentArgs) (*AgentWaitResult, error)
	ReadEvents(ctx context.Context, args ReadAgentEventsArgs) (*AgentEventsResult, error)
	Close(ctx context.Context, sessionID string) (*AgentStatusResult, error)
	Resume(ctx context.Context, sessionID string) (*AgentStatusResult, error)
}

const (
	AgentSessionContextParentSessionID = "agent_parent_session_id"
	AgentSessionContextAgentType       = "agent_type"
	AgentSessionContextRequestedModel  = "agent_requested_model"
)
