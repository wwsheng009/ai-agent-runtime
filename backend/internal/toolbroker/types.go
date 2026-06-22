package toolbroker

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/agentcontrol"
	"github.com/wwsheng009/ai-agent-runtime/internal/background"
	"github.com/wwsheng009/ai-agent-runtime/internal/team"
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
	ID                  string   `json:"id,omitempty"`
	Title               string   `json:"title,omitempty"`
	Goal                string   `json:"goal,omitempty"`
	Difficulty          string   `json:"difficulty,omitempty"`
	DifficultyRationale string   `json:"difficulty_rationale,omitempty"`
	Inputs              []string `json:"inputs,omitempty"`
	ReadPaths           []string `json:"read_paths,omitempty"`
	WritePaths          []string `json:"write_paths,omitempty"`
	Deliverables        []string `json:"deliverables,omitempty"`
	Priority            int      `json:"priority,omitempty"`
	Assignee            string   `json:"assignee,omitempty"`
	DependsOn           []string `json:"depends_on,omitempty"`
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

// WaitTeamArgs describes a durable wait/read request for a spawned team run.
type WaitTeamArgs struct {
	TeamID         string `json:"team_id,omitempty"`
	AfterSeq       int64  `json:"after_seq,omitempty"`
	TimeoutMs      int    `json:"timeout_ms,omitempty"`
	Limit          int    `json:"limit,omitempty"`
	RequireSummary *bool  `json:"require_summary,omitempty"`
}

// WaitTeamEventResult returns one persisted team lifecycle event.
type WaitTeamEventResult struct {
	Seq       int64                  `json:"seq"`
	Type      string                 `json:"type"`
	TeamID    string                 `json:"team_id"`
	Payload   map[string]interface{} `json:"payload,omitempty"`
	CreatedAt time.Time              `json:"created_at,omitempty"`
}

// WaitTeamResult returns terminal state plus recent durable lifecycle events.
type WaitTeamResult struct {
	TeamID          string                `json:"team_id"`
	Status          string                `json:"status"`
	Terminal        bool                  `json:"terminal"`
	TimedOut        bool                  `json:"timed_out"`
	SummaryReady    bool                  `json:"summary_ready"`
	Summary         string                `json:"summary,omitempty"`
	SummarySource   string                `json:"summary_source,omitempty"`
	SummaryEventSeq int64                 `json:"summary_event_seq,omitempty"`
	Events          []WaitTeamEventResult `json:"events,omitempty"`
	EventCount      int                   `json:"event_count"`
	LatestSeq       int64                 `json:"latest_seq,omitempty"`
}

// TeamMailboxDispatcher delivers mailbox events to active team sessions.
type TeamMailboxDispatcher interface {
	DispatchTeamMailboxMessage(ctx context.Context, message team.MailMessage) error
}

// TeamTeammateAgentProjector optionally projects spawn_team teammates into the
// AgentControl identity graph immediately after team store writes.
type TeamTeammateAgentProjector interface {
	SyncTeamTeammateAgent(ctx context.Context, previous *team.Teammate, teammate team.Teammate) error
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
	TaskID              string   `json:"task_id"`
	TeamID              string   `json:"team_id"`
	Title               string   `json:"title,omitempty"`
	Goal                string   `json:"goal,omitempty"`
	Difficulty          string   `json:"difficulty,omitempty"`
	DifficultyRationale string   `json:"difficulty_rationale,omitempty"`
	Inputs              []string `json:"inputs,omitempty"`
	Status              string   `json:"status,omitempty"`
	Priority            int      `json:"priority,omitempty"`
	Assignee            string   `json:"assignee,omitempty"`
	ReadPaths           []string `json:"read_paths,omitempty"`
	WritePaths          []string `json:"write_paths,omitempty"`
	Deliverables        []string `json:"deliverables,omitempty"`
	Summary             string   `json:"summary,omitempty"`
	ResultRef           string   `json:"result_ref,omitempty"`
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
	ID                  string   `json:"id,omitempty"`
	SessionID           string   `json:"session_id,omitempty"`
	Message             string   `json:"message,omitempty"`
	AgentType           string   `json:"agent_type,omitempty"`
	Difficulty          string   `json:"difficulty,omitempty"`
	DifficultyRationale string   `json:"difficulty_rationale,omitempty"`
	Provider            string   `json:"provider,omitempty"`
	Model               string   `json:"model,omitempty"`
	ReasoningEffort     string   `json:"reasoning_effort,omitempty"`
	ThinkingEffort      string   `json:"thinking_effort,omitempty"`
	PermissionMode      string   `json:"permission_mode,omitempty"`
	ForkContext         *bool    `json:"fork_context,omitempty"`
	ForkTurns           string   `json:"fork_turns,omitempty"`
	DifficultySource    string   `json:"-"`
	RouteSource         string   `json:"-"`
	RouteWarnings       []string `json:"-"`
	FallbackUsed        bool     `json:"-"`
	FallbackReason      string   `json:"-"`
}

// SendAgentInputArgs describes a follow-up input for an existing child agent.
type SendAgentInputArgs struct {
	ID        string `json:"id,omitempty"`
	SessionID string `json:"session_id,omitempty"`
	Message   string `json:"message,omitempty"`
	Interrupt *bool  `json:"interrupt,omitempty"`
}

// ResolveAgentApprovalArgs resolves a pending tool approval in a child agent.
type ResolveAgentApprovalArgs struct {
	ID          string          `json:"id,omitempty"`
	SessionID   string          `json:"session_id,omitempty"`
	RequestID   string          `json:"request_id"`
	Allow       bool            `json:"allow"`
	PatchedArgs json.RawMessage `json:"patched_args,omitempty"`
}

// WaitAgentArgs waits for child agent status, or for parent mailbox events
// when MailboxOnly is true.
type WaitAgentArgs struct {
	ID          string   `json:"id,omitempty"`
	SessionID   string   `json:"session_id,omitempty"`
	IDs         []string `json:"ids,omitempty"`
	SessionIDs  []string `json:"session_ids,omitempty"`
	AfterSeq    int64    `json:"after_seq,omitempty"`
	TimeoutMs   int      `json:"timeout_ms,omitempty"`
	MailboxOnly bool     `json:"mailbox_only,omitempty"`
}

// ListAgentsArgs lists lightweight child-agent sessions under a parent/root.
type ListAgentsArgs struct {
	ParentSessionID string `json:"parent_session_id,omitempty"`
	PathPrefix      string `json:"path_prefix,omitempty"`
	IncludeClosed   bool   `json:"include_closed,omitempty"`
}

// AgentMessageArgs describes an inter-agent message target and body.
type AgentMessageArgs struct {
	Target    string `json:"target,omitempty"`
	ID        string `json:"id,omitempty"`
	SessionID string `json:"session_id,omitempty"`
	Message   string `json:"message,omitempty"`
}

// AgentStatusResult returns the current state of a lightweight child agent session.
type AgentStatusResult struct {
	ID                       string   `json:"id"`
	SessionID                string   `json:"session_id"`
	ParentSessionID          string   `json:"parent_session_id,omitempty"`
	Path                     string   `json:"path,omitempty"`
	Depth                    int      `json:"depth,omitempty"`
	AgentType                string   `json:"agent_type,omitempty"`
	TeamID                   string   `json:"team_id,omitempty"`
	TeammateID               string   `json:"teammate_id,omitempty"`
	CurrentTaskID            string   `json:"current_task_id,omitempty"`
	CurrentTaskStatus        string   `json:"current_task_status,omitempty"`
	Provider                 string   `json:"provider,omitempty"`
	Model                    string   `json:"model,omitempty"`
	ReasoningEffort          string   `json:"reasoning_effort,omitempty"`
	PermissionMode           string   `json:"permission_mode,omitempty"`
	Difficulty               string   `json:"difficulty,omitempty"`
	DifficultySource         string   `json:"difficulty_source,omitempty"`
	DifficultyRationale      string   `json:"difficulty_rationale,omitempty"`
	RouteSource              string   `json:"route_source,omitempty"`
	RouteWarnings            []string `json:"route_warnings,omitempty"`
	FallbackUsed             bool     `json:"fallback_used,omitempty"`
	FallbackReason           string   `json:"fallback_reason,omitempty"`
	Status                   string   `json:"status"`
	Exists                   bool     `json:"exists"`
	Created                  bool     `json:"created,omitempty"`
	Queued                   bool     `json:"queued,omitempty"`
	TimedOut                 bool     `json:"timed_out,omitempty"`
	PendingApproval          bool     `json:"pending_approval,omitempty"`
	PendingApprovalID        string   `json:"pending_approval_id,omitempty"`
	PendingApprovalReason    string   `json:"pending_approval_reason,omitempty"`
	PendingApprovalRiskLevel string   `json:"pending_approval_risk_level,omitempty"`
	PendingQuestion          bool     `json:"pending_question,omitempty"`
	MessageCount             int      `json:"message_count,omitempty"`
	Output                   string   `json:"output,omitempty"`
	Error                    string   `json:"error,omitempty"`
	SessionState             string   `json:"session_state,omitempty"`
	CurrentTurnID            string   `json:"current_turn_id,omitempty"`
	PendingToolName          string   `json:"pending_tool_name,omitempty"`
	PendingToolCallID        string   `json:"pending_tool_call_id,omitempty"`
	LastMessageRole          string   `json:"last_message_role,omitempty"`
	LastMessagePreview       string   `json:"last_message_preview,omitempty"`
	ClosedCount              int      `json:"closed_count,omitempty"`
	ClosedSessionIDs         []string `json:"closed_session_ids,omitempty"`
}

// AgentWaitResult reports the outcome of child-status or mailbox-event wait.
type AgentWaitResult struct {
	Agent            *AgentStatusResult  `json:"agent,omitempty"`
	Agents           []AgentStatusResult `json:"agents,omitempty"`
	Event            *AgentEventItem     `json:"event,omitempty"`
	Events           []AgentEventItem    `json:"events,omitempty"`
	MatchedID        string              `json:"matched_id,omitempty"`
	MatchedSessionID string              `json:"matched_session_id,omitempty"`
	LatestSeq        int64               `json:"latest_seq,omitempty"`
	TimedOut         bool                `json:"timed_out,omitempty"`
	ReadyCount       int                 `json:"ready_count,omitempty"`
	PendingCount     int                 `json:"pending_count,omitempty"`
}

// AgentListResult reports known child-agent sessions.
type AgentListResult struct {
	Agents []AgentStatusResult `json:"agents,omitempty"`
	Count  int                 `json:"count"`
}

// AgentMessageResult reports queued inter-agent communication.
type AgentMessageResult struct {
	TargetSessionID string             `json:"target_session_id"`
	Delivered       bool               `json:"delivered"`
	Triggered       bool               `json:"triggered,omitempty"`
	Status          *AgentStatusResult `json:"status,omitempty"`
}

// AgentApprovalResult reports the resolved child-agent tool approval.
type AgentApprovalResult struct {
	SessionID string             `json:"session_id"`
	RequestID string             `json:"request_id"`
	Allowed   bool               `json:"allowed"`
	Resolved  bool               `json:"resolved"`
	Status    *AgentStatusResult `json:"status,omitempty"`
}

// ReadAgentEventsArgs reads child-agent runtime events, or parent mailbox/collab
// events when MailboxOnly is true.
type ReadAgentEventsArgs struct {
	ID          string `json:"id,omitempty"`
	SessionID   string `json:"session_id,omitempty"`
	AfterSeq    int64  `json:"after_seq,omitempty"`
	Limit       int    `json:"limit,omitempty"`
	WaitMs      int    `json:"wait_ms,omitempty"`
	MailboxOnly bool   `json:"mailbox_only,omitempty"`
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

// AgentEventsResult returns recent child runtime or parent mailbox/collab events.
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
	List(ctx context.Context, parentSessionID string, args ListAgentsArgs) (*AgentListResult, error)
	SendMessage(ctx context.Context, fromSessionID string, args AgentMessageArgs) (*AgentMessageResult, error)
	FollowupTask(ctx context.Context, fromSessionID string, args AgentMessageArgs) (*AgentMessageResult, error)
	SendInput(ctx context.Context, args SendAgentInputArgs) (*AgentStatusResult, error)
	ResolveApproval(ctx context.Context, args ResolveAgentApprovalArgs) (*AgentApprovalResult, error)
	Wait(ctx context.Context, args WaitAgentArgs) (*AgentWaitResult, error)
	ReadEvents(ctx context.Context, args ReadAgentEventsArgs) (*AgentEventsResult, error)
	Close(ctx context.Context, sessionID string) (*AgentStatusResult, error)
	Resume(ctx context.Context, sessionID string) (*AgentStatusResult, error)
}

const (
	AgentSessionContextProviderName        = "provider_name"
	AgentSessionContextModel               = "model"
	AgentSessionContextReasoningEffort     = "reasoning_effort"
	AgentSessionContextParentSessionID     = agentcontrol.SessionContextParentSessionID
	AgentSessionContextRootSessionID       = agentcontrol.SessionContextRootSessionID
	AgentSessionContextAgentType           = agentcontrol.SessionContextAgentType
	AgentSessionContextRequestedModel      = agentcontrol.SessionContextRequestedModel
	AgentSessionContextDifficulty          = agentcontrol.SessionContextDifficulty
	AgentSessionContextDifficultySource    = agentcontrol.SessionContextDifficultySource
	AgentSessionContextDifficultyRationale = agentcontrol.SessionContextDifficultyRationale
	AgentSessionContextRouteSource         = agentcontrol.SessionContextRouteSource
	AgentSessionContextRouteWarnings       = agentcontrol.SessionContextRouteWarnings
	AgentSessionContextFallbackUsed        = agentcontrol.SessionContextFallbackUsed
	AgentSessionContextFallbackReason      = agentcontrol.SessionContextFallbackReason
	AgentSessionContextPath                = agentcontrol.SessionContextPath
	AgentSessionContextDepth               = agentcontrol.SessionContextDepth
	AgentSessionContextTeamID              = agentcontrol.SessionContextTeamID
	AgentSessionContextTeammateID          = agentcontrol.SessionContextTeammateID
	AgentSessionContextPermissionMode      = "permission_mode"
)

// ApplySpawnAgentRouteContext persists spawn_agent route hints on a child
// session. Provider/model/reasoning use canonical session metadata keys so
// actor builders can recover the same route after restart/resume.
func ApplySpawnAgentRouteContext(session agentcontrol.ContextSetter, args SpawnAgentArgs) {
	if session == nil {
		return
	}
	if provider := strings.TrimSpace(args.Provider); provider != "" {
		session.SetContext(AgentSessionContextProviderName, provider)
	}
	if model := strings.TrimSpace(args.Model); model != "" {
		session.SetContext(AgentSessionContextRequestedModel, model)
		session.SetContext(AgentSessionContextModel, model)
	}
	effort := strings.TrimSpace(args.ReasoningEffort)
	if effort == "" {
		effort = strings.TrimSpace(args.ThinkingEffort)
	}
	if effort != "" {
		session.SetContext(AgentSessionContextReasoningEffort, effort)
	}
	if difficulty := strings.TrimSpace(args.Difficulty); difficulty != "" {
		session.SetContext(AgentSessionContextDifficulty, difficulty)
	}
	if source := strings.TrimSpace(args.DifficultySource); source != "" {
		session.SetContext(AgentSessionContextDifficultySource, source)
	}
	if rationale := strings.TrimSpace(args.DifficultyRationale); rationale != "" {
		session.SetContext(AgentSessionContextDifficultyRationale, rationale)
	}
	if source := strings.TrimSpace(args.RouteSource); source != "" {
		session.SetContext(AgentSessionContextRouteSource, source)
	}
	if args.FallbackUsed {
		session.SetContext(AgentSessionContextFallbackUsed, true)
	}
	if reason := strings.TrimSpace(args.FallbackReason); reason != "" {
		session.SetContext(AgentSessionContextFallbackReason, reason)
	}
	if len(args.RouteWarnings) > 0 {
		warnings := make([]string, 0, len(args.RouteWarnings))
		for _, warning := range args.RouteWarnings {
			if warning = strings.TrimSpace(warning); warning != "" {
				warnings = append(warnings, warning)
			}
		}
		if len(warnings) > 0 {
			session.SetContext(AgentSessionContextRouteWarnings, warnings)
		}
	}
	if permissionMode := strings.TrimSpace(args.PermissionMode); permissionMode != "" {
		session.SetContext(AgentSessionContextPermissionMode, permissionMode)
	}
}

// ApplySpawnAgentRouteStatusContext copies persisted spawn_agent route
// metadata from a session context into an agent status result.
func ApplySpawnAgentRouteStatusContext(result *AgentStatusResult, session agentcontrol.ContextGetter) {
	if result == nil || session == nil {
		return
	}
	if provider := agentcontrol.ContextString(session, AgentSessionContextProviderName); provider != "" {
		result.Provider = provider
	}
	model := agentcontrol.ContextString(session, AgentSessionContextRequestedModel)
	if model == "" {
		model = agentcontrol.ContextString(session, AgentSessionContextModel)
	}
	if model != "" {
		result.Model = model
	}
	if effort := agentcontrol.ContextString(session, AgentSessionContextReasoningEffort); effort != "" {
		result.ReasoningEffort = effort
	}
	if permissionMode := agentcontrol.ContextString(session, AgentSessionContextPermissionMode); permissionMode != "" {
		result.PermissionMode = permissionMode
	}
	if difficulty := agentcontrol.ContextString(session, AgentSessionContextDifficulty); difficulty != "" {
		result.Difficulty = difficulty
	}
	if source := agentcontrol.ContextString(session, AgentSessionContextDifficultySource); source != "" {
		result.DifficultySource = source
	}
	if rationale := agentcontrol.ContextString(session, AgentSessionContextDifficultyRationale); rationale != "" {
		result.DifficultyRationale = rationale
	}
	if source := agentcontrol.ContextString(session, AgentSessionContextRouteSource); source != "" {
		result.RouteSource = source
	}
	if warnings := spawnAgentRouteWarningsFromContext(session); len(warnings) > 0 {
		result.RouteWarnings = warnings
	}
	if spawnAgentFallbackUsedFromContext(session) {
		result.FallbackUsed = true
	}
	if reason := agentcontrol.ContextString(session, AgentSessionContextFallbackReason); reason != "" {
		result.FallbackReason = reason
	}
}

// AddSpawnAgentRoutePayload copies route metadata into completion payloads.
func AddSpawnAgentRoutePayload(payload map[string]interface{}, session agentcontrol.ContextGetter) {
	if payload == nil || session == nil {
		return
	}
	status := AgentStatusResult{}
	ApplySpawnAgentRouteStatusContext(&status, session)
	if status.Difficulty != "" {
		payload["difficulty"] = status.Difficulty
	}
	if status.DifficultySource != "" {
		payload["difficulty_source"] = status.DifficultySource
	}
	if status.DifficultyRationale != "" {
		payload["difficulty_rationale"] = status.DifficultyRationale
	}
	if status.Provider != "" {
		payload["route_provider"] = status.Provider
	}
	if status.Model != "" {
		payload["route_model"] = status.Model
	}
	if status.ReasoningEffort != "" {
		payload["route_reasoning_effort"] = status.ReasoningEffort
	}
	if status.PermissionMode != "" {
		payload["permission_mode"] = status.PermissionMode
	}
	if status.RouteSource != "" {
		payload["route_source"] = status.RouteSource
	}
	if len(status.RouteWarnings) > 0 {
		payload["route_warnings"] = append([]string(nil), status.RouteWarnings...)
	}
	if status.FallbackUsed {
		payload["fallback_used"] = true
	}
	if status.FallbackReason != "" {
		payload["fallback_reason"] = status.FallbackReason
	}
}

func ApplySpawnAgentRouteRecord(record *agentcontrol.AgentRecord, args SpawnAgentArgs) {
	if record == nil {
		return
	}
	record.Provider = strings.TrimSpace(args.Provider)
	record.Model = strings.TrimSpace(args.Model)
	record.ReasoningEffort = firstNonEmptyRouteString(args.ReasoningEffort, args.ThinkingEffort)
	record.Difficulty = strings.TrimSpace(args.Difficulty)
	record.DifficultySource = strings.TrimSpace(args.DifficultySource)
	record.DifficultyRationale = strings.TrimSpace(args.DifficultyRationale)
	record.RouteSource = strings.TrimSpace(args.RouteSource)
	record.RouteWarnings = trimNonEmptyStrings(args.RouteWarnings)
	record.FallbackUsed = args.FallbackUsed
	record.FallbackReason = strings.TrimSpace(args.FallbackReason)
}

func ApplySpawnAgentRouteRecordContext(record *agentcontrol.AgentRecord, session agentcontrol.ContextGetter) {
	if record == nil || session == nil {
		return
	}
	status := AgentStatusResult{}
	ApplySpawnAgentRouteStatusContext(&status, session)
	record.Provider = strings.TrimSpace(status.Provider)
	record.Model = strings.TrimSpace(status.Model)
	record.ReasoningEffort = strings.TrimSpace(status.ReasoningEffort)
	record.Difficulty = strings.TrimSpace(status.Difficulty)
	record.DifficultySource = strings.TrimSpace(status.DifficultySource)
	record.DifficultyRationale = strings.TrimSpace(status.DifficultyRationale)
	record.RouteSource = strings.TrimSpace(status.RouteSource)
	record.RouteWarnings = trimNonEmptyStrings(status.RouteWarnings)
	record.FallbackUsed = status.FallbackUsed
	record.FallbackReason = strings.TrimSpace(status.FallbackReason)
}

func ApplySpawnAgentRouteStatusRecord(result *AgentStatusResult, record agentcontrol.AgentRecord) {
	if result == nil {
		return
	}
	if result.Provider == "" {
		result.Provider = strings.TrimSpace(record.Provider)
	}
	if result.Model == "" {
		result.Model = strings.TrimSpace(record.Model)
	}
	if result.ReasoningEffort == "" {
		result.ReasoningEffort = strings.TrimSpace(record.ReasoningEffort)
	}
	if result.Difficulty == "" {
		result.Difficulty = strings.TrimSpace(record.Difficulty)
	}
	if result.DifficultySource == "" {
		result.DifficultySource = strings.TrimSpace(record.DifficultySource)
	}
	if result.DifficultyRationale == "" {
		result.DifficultyRationale = strings.TrimSpace(record.DifficultyRationale)
	}
	if result.RouteSource == "" {
		result.RouteSource = strings.TrimSpace(record.RouteSource)
	}
	if len(result.RouteWarnings) == 0 {
		result.RouteWarnings = trimNonEmptyStrings(record.RouteWarnings)
	}
	if !result.FallbackUsed {
		result.FallbackUsed = record.FallbackUsed
	}
	if result.FallbackReason == "" {
		result.FallbackReason = strings.TrimSpace(record.FallbackReason)
	}
}

func SpawnAgentRunMeta(args SpawnAgentArgs) *team.RunMeta {
	permissionMode := strings.TrimSpace(args.PermissionMode)
	if permissionMode == "" {
		return nil
	}
	return &team.RunMeta{PermissionMode: permissionMode}
}

func firstNonEmptyRouteString(values ...string) string {
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
}

func spawnAgentRouteWarningsFromContext(session agentcontrol.ContextGetter) []string {
	if session == nil {
		return nil
	}
	value, ok := session.GetContext(AgentSessionContextRouteWarnings)
	if !ok {
		return nil
	}
	switch typed := value.(type) {
	case []string:
		return trimNonEmptyStrings(typed)
	case []interface{}:
		warnings := make([]string, 0, len(typed))
		for _, item := range typed {
			if text, ok := item.(string); ok {
				warnings = append(warnings, text)
			}
		}
		return trimNonEmptyStrings(warnings)
	case string:
		return trimNonEmptyStrings([]string{typed})
	default:
		return nil
	}
}

func spawnAgentFallbackUsedFromContext(session agentcontrol.ContextGetter) bool {
	if session == nil {
		return false
	}
	value, ok := session.GetContext(AgentSessionContextFallbackUsed)
	if !ok {
		return false
	}
	switch typed := value.(type) {
	case bool:
		return typed
	case string:
		return strings.EqualFold(strings.TrimSpace(typed), "true")
	default:
		return false
	}
}

func trimNonEmptyStrings(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	result := make([]string, 0, len(values))
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			result = append(result, value)
		}
	}
	return result
}
