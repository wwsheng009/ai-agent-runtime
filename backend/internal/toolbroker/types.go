package toolbroker

import (
	"context"
	"encoding/json"
	"fmt"
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
	TeamID             string                 `json:"team_id"`
	Status             string                 `json:"status"`
	Terminal           bool                   `json:"terminal"`
	TimedOut           bool                   `json:"timed_out"`
	WaitTimeoutMs      int                    `json:"wait_timeout_ms,omitempty"`
	ExecutionContinues bool                   `json:"execution_continues,omitempty"`
	NextAction         string                 `json:"next_action,omitempty"`
	SummaryReady       bool                   `json:"summary_ready"`
	Summary            string                 `json:"summary,omitempty"`
	SummarySource      string                 `json:"summary_source,omitempty"`
	SummaryPayload     map[string]interface{} `json:"summary_payload,omitempty"`
	SummaryEventSeq    int64                  `json:"summary_event_seq,omitempty"`
	Events             []WaitTeamEventResult  `json:"events,omitempty"`
	EventCount         int                    `json:"event_count"`
	LatestSeq          int64                  `json:"latest_seq,omitempty"`
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

// EnterPlanModeArgs describes enter_plan_mode tool input.
type EnterPlanModeArgs struct {
	// PlanPath is the plan artifact path (default plan.md).
	PlanPath string `json:"plan_path,omitempty"`
}

// ExitPlanModeArgs describes exit_plan_mode tool input.
type ExitPlanModeArgs struct {
	// Decision is required: approve | request_changes | quit.
	Decision string `json:"decision"`
	// Notes are optional free-form notes recorded with the exit decision.
	Notes string `json:"notes,omitempty"`
}

// PlanModeResult reports plan-mode enter/exit outcome for agent tools.
type PlanModeResult struct {
	Active          bool     `json:"active"`
	Status          string   `json:"status,omitempty"`
	PlanPath        string   `json:"plan_path,omitempty"`
	PermissionMode  string   `json:"permission_mode,omitempty"`
	PreviousMode    string   `json:"previous_mode,omitempty"`
	ExitDecision    string   `json:"exit_decision,omitempty"`
	Notes           string   `json:"notes,omitempty"`
	WriteAllowPaths []string `json:"write_allow_paths,omitempty"`
	EnteredAt       string   `json:"entered_at,omitempty"`
	ExitedAt        string   `json:"exited_at,omitempty"`
}

// PlanModeController toggles durable plan mode for the current session mid-turn.
// Implementations must persist session plan_mode context, apply the live
// permission engine, and update RunMeta.PermissionMode when present so the
// remainder of the turn evaluates tools under plan (or restored) mode.
type PlanModeController interface {
	EnterPlanMode(ctx context.Context, sessionID string, args EnterPlanModeArgs) (*PlanModeResult, error)
	ExitPlanMode(ctx context.Context, sessionID string, args ExitPlanModeArgs) (*PlanModeResult, error)
}

// SpawnAgentArgs describes a lightweight child-agent session request.
type SpawnAgentArgs struct {
	ID                  string `json:"id,omitempty"`
	SessionID           string `json:"session_id,omitempty"`
	Message             string `json:"message,omitempty"`
	AgentType           string `json:"agent_type,omitempty"`
	Difficulty          string `json:"difficulty,omitempty"`
	DifficultyRationale string `json:"difficulty_rationale,omitempty"`
	Provider            string `json:"provider,omitempty"`
	Model               string `json:"model,omitempty"`
	ReasoningEffort     string `json:"reasoning_effort,omitempty"`
	ThinkingEffort      string `json:"thinking_effort,omitempty"`
	PermissionMode      string `json:"permission_mode,omitempty"`
	// CompletionRequirement is retained for wire compatibility, but ordinary
	// spawn_agent children only support none. Team workers receive complete_task
	// from TeammateRunner RunMeta after a real task assignment is bound.
	CompletionRequirement string `json:"completion_requirement,omitempty"`
	// Isolation is none|worktree. Empty normalizes to none. worktree fails closed
	// (no silent main-tree fallback) when git worktree creation is unavailable.
	Isolation                string   `json:"isolation,omitempty"`
	ReadOnly                 bool     `json:"read_only,omitempty"`
	ForkContext              *bool    `json:"fork_context,omitempty"`
	ForkTurns                string   `json:"fork_turns,omitempty"`
	// Execution supervision timeouts (doc 7.2). Zero means "use operator
	// default"; a negative value is rejected by the broker.
	TimeoutSec               int64    `json:"timeout_sec,omitempty"`
	ProgressTimeoutSec       int64    `json:"progress_timeout_sec,omitempty"`
	ApprovalTimeoutSec       int64    `json:"approval_timeout_sec,omitempty"`
	CancelGraceSec           int64    `json:"cancel_grace_sec,omitempty"`
	DifficultySource         string   `json:"-"`
	RouteSource              string   `json:"-"`
	RouteWarnings            []string `json:"-"`
	FallbackUsed             bool     `json:"-"`
	FallbackReason           string   `json:"-"`
	RequestedProvider        string   `json:"-"`
	RequestedModel           string   `json:"-"`
	RequestedReasoningEffort string   `json:"-"`
	RequestedPermissionMode  string   `json:"-"`
	EffectivePermissionMode  string   `json:"-"`
	RequestedRouteCaptured   bool     `json:"-"`
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
	ReadOnly                 bool     `json:"read_only,omitempty"`
	Isolation                string   `json:"isolation,omitempty"`
	WorktreePath             string   `json:"worktree_path,omitempty"`
	WorktreeBranch           string   `json:"worktree_branch,omitempty"`
	WorktreeRepoRoot         string   `json:"worktree_repo_root,omitempty"`
	Difficulty               string   `json:"difficulty,omitempty"`
	DifficultySource         string   `json:"difficulty_source,omitempty"`
	DifficultyRationale      string   `json:"difficulty_rationale,omitempty"`
	RouteSource              string   `json:"route_source,omitempty"`
	RouteWarnings            []string `json:"route_warnings,omitempty"`
	FallbackUsed             bool     `json:"fallback_used,omitempty"`
	FallbackReason           string   `json:"fallback_reason,omitempty"`
	RequestedProvider        string   `json:"requested_provider,omitempty"`
	EffectiveProvider        string   `json:"effective_provider,omitempty"`
	RequestedModel           string   `json:"requested_model,omitempty"`
	EffectiveModel           string   `json:"effective_model,omitempty"`
	RequestedReasoningEffort string   `json:"requested_reasoning_effort,omitempty"`
	EffectiveReasoningEffort string   `json:"effective_reasoning_effort,omitempty"`
	RequestedPermissionMode  string   `json:"requested_permission_mode,omitempty"`
	EffectivePermissionMode  string   `json:"effective_permission_mode,omitempty"`
	Status                   string   `json:"status"`
	Exists                   bool     `json:"exists"`
	Created                  bool     `json:"created,omitempty"`
	Queued                   bool     `json:"queued,omitempty"`
	TimedOut                 bool     `json:"timed_out,omitempty"`
	// RunID is the durable execution run identity assigned by the execution
	// supervisor at spawn time (doc 7.1). Empty when supervision is disabled.
	RunID                string `json:"run_id,omitempty"`
	ExecutionDeadlineAt  string `json:"execution_deadline_at,omitempty"`
	SupervisionPolicy    string `json:"supervision_policy,omitempty"`
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
	Agent              *AgentStatusResult  `json:"agent,omitempty"`
	Agents             []AgentStatusResult `json:"agents,omitempty"`
	Event              *AgentEventItem     `json:"event,omitempty"`
	Events             []AgentEventItem    `json:"events,omitempty"`
	MatchedID          string              `json:"matched_id,omitempty"`
	MatchedSessionID   string              `json:"matched_session_id,omitempty"`
	LatestSeq          int64               `json:"latest_seq,omitempty"`
	TimedOut           bool                `json:"timed_out,omitempty"`
	WaitTimeoutMs      int                 `json:"wait_timeout_ms,omitempty"`
	ExecutionContinues bool                `json:"execution_continues,omitempty"`
	ReadyCount         int                 `json:"ready_count,omitempty"`
	PendingCount       int                 `json:"pending_count,omitempty"`
	ReadyIDs           []string            `json:"ready_ids,omitempty"`
	PendingIDs         []string            `json:"pending_ids,omitempty"`
	WaitedMs           int64               `json:"waited_ms,omitempty"`
	NextAction         string              `json:"next_action,omitempty"`
}

// MarshalJSON keeps the legacy matched-agent view without serializing the same
// potentially large final answer twice. Batch waits retain the lightweight
// matched agent and the complete agents list.
func (r AgentWaitResult) MarshalJSON() ([]byte, error) {
	type wireAgentWaitResult AgentWaitResult
	wire := wireAgentWaitResult(r)
	if r.Agent != nil && agentWaitListContains(r.Agents, r.Agent) {
		if len(r.Agents) == 1 {
			wire.Agents = nil
		} else {
			matched := *r.Agent
			matched.Output = ""
			wire.Agent = &matched
		}
	}
	return json.Marshal(wire)
}

func agentWaitListContains(agents []AgentStatusResult, target *AgentStatusResult) bool {
	if target == nil {
		return false
	}
	for index := range agents {
		if strings.TrimSpace(agents[index].SessionID) != "" && strings.EqualFold(strings.TrimSpace(agents[index].SessionID), strings.TrimSpace(target.SessionID)) {
			return true
		}
		if strings.TrimSpace(agents[index].ID) != "" && strings.EqualFold(strings.TrimSpace(agents[index].ID), strings.TrimSpace(target.ID)) {
			return true
		}
	}
	return false
}

// FinalizeAgentEventsResult adds compact polling guidance so parents do not
// spin forever on empty read_agent_events results (doom-loop exempt tool).
func FinalizeAgentEventsResult(result *AgentEventsResult) *AgentEventsResult {
	if result == nil {
		return nil
	}
	if strings.TrimSpace(result.NextAction) != "" {
		return result
	}
	hasApproval := false
	for _, event := range result.Events {
		eventType := strings.ToLower(strings.TrimSpace(event.Type))
		if strings.Contains(eventType, "approval") || strings.Contains(eventType, "waiting_approval") {
			hasApproval = true
			break
		}
		if payload := event.Payload; payload != nil {
			if status, ok := payload["status"].(string); ok && strings.EqualFold(strings.TrimSpace(status), "waiting_approval") {
				hasApproval = true
				break
			}
			if pending, ok := payload["pending_approval"].(bool); ok && pending {
				hasApproval = true
				break
			}
		}
	}
	switch {
	case hasApproval:
		result.NextAction = "resolve_pending_approval: call resolve_agent_approval with allow=true|false; do not re-poll read_agent_events for the same approval"
	case result.Count > 0:
		result.NextAction = "consume_events: use returned events now; only re-call read_agent_events with a higher after_seq when new events are needed"
	case result.TimedOut:
		result.NextAction = "stop_empty_event_poll: timed out with 0 events; use wait_agent for child readiness, send_message/followup_task for work, or proceed without re-calling the same read_agent_events"
	default:
		// Non-blocking empty read (wait_ms=0 or no wait). Explicitly steer away
		// from tight unchanged polling loops that doom-loop exempts.
		result.NextAction = "stop_empty_event_poll: 0 events returned; do not immediately re-call read_agent_events with the same id/after_seq. Prefer wait_agent for readiness, or use wait_ms>0 once if waiting for a specific new event"
	}
	return result
}

// FinalizeAgentWaitResult adds compact scheduling guidance shared by local and
// runtime-server wait implementations.
func FinalizeAgentWaitResult(result *AgentWaitResult, startedAt time.Time) *AgentWaitResult {
	if result == nil {
		return nil
	}
	if !startedAt.IsZero() {
		result.WaitedMs = time.Since(startedAt).Milliseconds()
		if result.WaitedMs == 0 {
			result.WaitedMs = 1
		}
	}
	if result.Event != nil || len(result.Events) > 0 {
		result.NextAction = "consume_mailbox_events"
	} else if agentWaitHasPendingApproval(result) {
		result.NextAction = agentWaitPendingApprovalNextAction(result)
	} else if result.TimedOut && result.PendingCount > 0 {
		result.ExecutionContinues = true
		result.NextAction = "continue_independent_work_before_waiting_again: wait timeout only ended this observation; pending child execution continues. Do not immediately re-call wait_agent with the same ids/timeout while independent parent work remains; consume any ready outputs first, then wait only for still-pending children"
	} else if result.ReadyCount > 0 && result.PendingCount > 0 {
		result.NextAction = "consume_ready_outputs_and_continue_independent_work: use ready child outputs now; keep other independent work moving instead of blocking only on pending agents"
	} else if result.ReadyCount > 0 {
		result.NextAction = "consume_ready_outputs: use the returned ready outputs and do not re-wait for already-ready agents"
	} else if result.TimedOut {
		result.NextAction = "continue_independent_work_before_waiting_again: wait timeout only ended this observation; do other work or inspect child status before waiting again"
	}
	return result
}

func agentWaitHasPendingApproval(result *AgentWaitResult) bool {
	if result == nil {
		return false
	}
	if result.Agent != nil && result.Agent.PendingApproval {
		return true
	}
	for index := range result.Agents {
		if result.Agents[index].PendingApproval {
			return true
		}
	}
	return false
}

// agentWaitPendingApprovalNextAction steers parents toward resolve_agent_approval
// instead of re-wait/poll loops when a child is blocked on tool approval.
func agentWaitPendingApprovalNextAction(result *AgentWaitResult) string {
	pending := firstAgentWaitPendingApproval(result)
	if pending == nil {
		return "resolve_pending_approval: call resolve_agent_approval with allow=true|false; do not re-wait or poll for the same approval"
	}
	sessionRef := firstNonEmptyToolValue(pending.ID, pending.SessionID, pending.Path)
	requestID := strings.TrimSpace(pending.PendingApprovalID)
	parts := []string{"resolve_pending_approval"}
	if sessionRef != "" && requestID != "" {
		parts = append(parts, fmt.Sprintf("call resolve_agent_approval with id=%q request_id=%q allow=true|false", sessionRef, requestID))
	} else if sessionRef != "" {
		parts = append(parts, fmt.Sprintf("inspect pending_approval_id for id=%q then call resolve_agent_approval with allow=true|false", sessionRef))
	} else if requestID != "" {
		parts = append(parts, fmt.Sprintf("call resolve_agent_approval with request_id=%q allow=true|false", requestID))
	} else {
		parts = append(parts, "call resolve_agent_approval with allow=true|false")
	}
	parts = append(parts, "do not re-wait, poll, or start a fallback agent for the same approval")
	return strings.Join(parts, ": ")
}

func firstAgentWaitPendingApproval(result *AgentWaitResult) *AgentStatusResult {
	if result == nil {
		return nil
	}
	if result.Agent != nil && result.Agent.PendingApproval {
		return result.Agent
	}
	for index := range result.Agents {
		if result.Agents[index].PendingApproval {
			agent := result.Agents[index]
			return &agent
		}
	}
	return nil
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
	NextAction string           `json:"next_action,omitempty"`
}

// ApplyAgentWorktreeArgs applies a child's worktree isolation changes into the main repo.
type ApplyAgentWorktreeArgs struct {
	ID        string `json:"id,omitempty"`
	SessionID string `json:"session_id,omitempty"`
	// Paths limits apply to specific relative paths. Empty = all tracked changes.
	Paths []string `json:"paths,omitempty"`
	// Keep preserves the worktree after apply (default false removes it).
	Keep bool `json:"keep,omitempty"`
}

// DiscardAgentWorktreeArgs discards a child's worktree isolation without applying changes.
type DiscardAgentWorktreeArgs struct {
	ID        string `json:"id,omitempty"`
	SessionID string `json:"session_id,omitempty"`
}

// AgentWorktreeResult reports apply/discard outcomes for worktree isolation.
type AgentWorktreeResult struct {
	ID             string             `json:"id,omitempty"`
	SessionID      string             `json:"session_id,omitempty"`
	Action         string             `json:"action"` // apply | discard
	Isolation      string             `json:"isolation,omitempty"`
	WorktreePath   string             `json:"worktree_path,omitempty"`
	WorktreeBranch string             `json:"worktree_branch,omitempty"`
	RepoRoot       string             `json:"repo_root,omitempty"`
	DiffStat       string             `json:"diff_stat,omitempty"`
	Paths          []string           `json:"paths,omitempty"`
	Applied        bool               `json:"applied,omitempty"`
	Discarded      bool               `json:"discarded,omitempty"`
	Removed        bool               `json:"removed,omitempty"`
	Kept           bool               `json:"kept,omitempty"`
	Status         *AgentStatusResult `json:"status,omitempty"`
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
	// ApplyWorktree copies isolation changes into the main tree (optional path filter).
	// Default removes the worktree after apply unless Keep=true.
	ApplyWorktree(ctx context.Context, args ApplyAgentWorktreeArgs) (*AgentWorktreeResult, error)
	// DiscardWorktree removes isolation without applying; main tree stays unchanged.
	DiscardWorktree(ctx context.Context, args DiscardAgentWorktreeArgs) (*AgentWorktreeResult, error)
}

const (
	AgentSessionContextProviderName          = "provider_name"
	AgentSessionContextModel                 = "model"
	AgentSessionContextReasoningEffort       = "reasoning_effort"
	AgentSessionContextParentSessionID       = agentcontrol.SessionContextParentSessionID
	AgentSessionContextRootSessionID         = agentcontrol.SessionContextRootSessionID
	AgentSessionContextAgentType             = agentcontrol.SessionContextAgentType
	AgentSessionContextRequestedModel        = agentcontrol.SessionContextRequestedModel
	AgentSessionContextDifficulty            = agentcontrol.SessionContextDifficulty
	AgentSessionContextDifficultySource      = agentcontrol.SessionContextDifficultySource
	AgentSessionContextDifficultyRationale   = agentcontrol.SessionContextDifficultyRationale
	AgentSessionContextRouteSource           = agentcontrol.SessionContextRouteSource
	AgentSessionContextRouteWarnings         = agentcontrol.SessionContextRouteWarnings
	AgentSessionContextFallbackUsed          = agentcontrol.SessionContextFallbackUsed
	AgentSessionContextFallbackReason        = agentcontrol.SessionContextFallbackReason
	AgentSessionContextPath                  = agentcontrol.SessionContextPath
	AgentSessionContextDepth                 = agentcontrol.SessionContextDepth
	AgentSessionContextTeamID                = agentcontrol.SessionContextTeamID
	AgentSessionContextTeammateID            = agentcontrol.SessionContextTeammateID
	AgentSessionContextPermissionMode        = "permission_mode"
	AgentSessionContextCompletionRequirement = "completion_requirement"
	AgentSessionContextReadOnly              = "read_only"
	AgentSessionContextIsolation             = "isolation"
	AgentSessionContextWorktreePath          = "worktree_path"
	AgentSessionContextWorktreeBranch        = "worktree_branch"
	AgentSessionContextWorktreeRepoRoot      = "worktree_repo_root"
	// AgentSessionContextWorktreeDisposition records parent decision on isolation:
	// applied | discarded. Empty means still pending explicit apply/discard/close.
	AgentSessionContextWorktreeDisposition = "worktree_disposition"
	// AgentSessionContextWritePaths is the default write scope for a child
	// session. Worktree isolation binds this to the isolation root so later
	// claim/task code can inherit the isolated workspace as write_paths.
	AgentSessionContextWritePaths = "write_paths"

	WorktreeDispositionApplied                  = "applied"
	WorktreeDispositionDiscarded                = "discarded"
	AgentSessionContextRequestedProvider        = "agent_requested_provider"
	AgentSessionContextRequestedReasoningEffort = "agent_requested_reasoning_effort"
	AgentSessionContextRequestedPermissionMode  = "agent_requested_permission_mode"
	AgentSessionContextEffectivePermissionMode  = "agent_effective_permission_mode"
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
	if provider := strings.TrimSpace(args.RequestedProvider); provider != "" {
		session.SetContext(AgentSessionContextRequestedProvider, provider)
	}
	if model := strings.TrimSpace(args.Model); model != "" {
		session.SetContext(AgentSessionContextModel, model)
	}
	if model := strings.TrimSpace(args.RequestedModel); model != "" {
		session.SetContext(AgentSessionContextRequestedModel, model)
	} else if model := strings.TrimSpace(args.Model); model != "" {
		session.SetContext(AgentSessionContextRequestedModel, model)
	}
	effort := strings.TrimSpace(args.ReasoningEffort)
	if effort == "" {
		effort = strings.TrimSpace(args.ThinkingEffort)
	}
	if effort != "" {
		session.SetContext(AgentSessionContextReasoningEffort, effort)
	}
	if effort := strings.TrimSpace(args.RequestedReasoningEffort); effort != "" {
		session.SetContext(AgentSessionContextRequestedReasoningEffort, effort)
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
	if permissionMode := strings.TrimSpace(args.RequestedPermissionMode); permissionMode != "" {
		session.SetContext(AgentSessionContextRequestedPermissionMode, permissionMode)
	}
	effectivePermissionMode := firstNonEmptyRouteString(args.EffectivePermissionMode, args.PermissionMode)
	if effectivePermissionMode != "" {
		session.SetContext(AgentSessionContextEffectivePermissionMode, effectivePermissionMode)
	}
	// Forking copies parent context, but ordinary spawn_agent children never own
	// the parent's Team completion contract. Persist none even when omitted so a
	// cloned complete_task value cannot survive into initial or resumed runs.
	session.SetContext(AgentSessionContextCompletionRequirement, "none")
	if isolation := strings.TrimSpace(args.Isolation); isolation != "" {
		session.SetContext(AgentSessionContextIsolation, isolation)
	}
	if args.ReadOnly {
		session.SetContext(AgentSessionContextReadOnly, true)
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
		result.EffectiveProvider = provider
	}
	result.RequestedProvider = agentcontrol.ContextString(session, AgentSessionContextRequestedProvider)
	result.RequestedModel = agentcontrol.ContextString(session, AgentSessionContextRequestedModel)
	model := agentcontrol.ContextString(session, AgentSessionContextModel)
	if model == "" {
		model = result.RequestedModel
	}
	if model != "" {
		result.Model = model
		result.EffectiveModel = model
	}
	if effort := agentcontrol.ContextString(session, AgentSessionContextReasoningEffort); effort != "" {
		result.ReasoningEffort = effort
		result.EffectiveReasoningEffort = effort
	}
	result.RequestedReasoningEffort = agentcontrol.ContextString(session, AgentSessionContextRequestedReasoningEffort)
	if permissionMode := agentcontrol.ContextString(session, AgentSessionContextPermissionMode); permissionMode != "" {
		result.PermissionMode = permissionMode
	}
	result.RequestedPermissionMode = agentcontrol.ContextString(session, AgentSessionContextRequestedPermissionMode)
	result.EffectivePermissionMode = agentcontrol.ContextString(session, AgentSessionContextEffectivePermissionMode)
	if result.EffectivePermissionMode == "" {
		result.EffectivePermissionMode = result.PermissionMode
	}
	if value, ok := session.GetContext(AgentSessionContextReadOnly); ok {
		result.ReadOnly, _ = value.(bool)
	}
	if isolation := agentcontrol.ContextString(session, AgentSessionContextIsolation); isolation != "" {
		result.Isolation = isolation
	}
	if path := agentcontrol.ContextString(session, AgentSessionContextWorktreePath); path != "" {
		result.WorktreePath = path
	}
	if branch := agentcontrol.ContextString(session, AgentSessionContextWorktreeBranch); branch != "" {
		result.WorktreeBranch = branch
	}
	if repoRoot := agentcontrol.ContextString(session, AgentSessionContextWorktreeRepoRoot); repoRoot != "" {
		result.WorktreeRepoRoot = repoRoot
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
	if status.ReadOnly {
		payload["read_only"] = true
	}
	if status.Isolation != "" {
		payload["isolation"] = status.Isolation
	}
	if status.WorktreePath != "" {
		payload["worktree_path"] = status.WorktreePath
	}
	if status.WorktreeBranch != "" {
		payload["worktree_branch"] = status.WorktreeBranch
	}
	if status.WorktreeRepoRoot != "" {
		payload["worktree_repo_root"] = status.WorktreeRepoRoot
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
	for key, value := range map[string]string{
		"requested_provider":         status.RequestedProvider,
		"effective_provider":         firstNonEmptyRouteString(status.EffectiveProvider, status.Provider),
		"requested_model":            status.RequestedModel,
		"effective_model":            firstNonEmptyRouteString(status.EffectiveModel, status.Model),
		"requested_reasoning_effort": status.RequestedReasoningEffort,
		"effective_reasoning_effort": firstNonEmptyRouteString(status.EffectiveReasoningEffort, status.ReasoningEffort),
		"requested_permission_mode":  status.RequestedPermissionMode,
		"effective_permission_mode":  firstNonEmptyRouteString(status.EffectivePermissionMode, status.PermissionMode),
	} {
		if value = strings.TrimSpace(value); value != "" {
			payload[key] = value
		}
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
	record.RequestedProvider = strings.TrimSpace(args.RequestedProvider)
	record.EffectiveProvider = strings.TrimSpace(args.Provider)
	record.RequestedModel = strings.TrimSpace(args.RequestedModel)
	record.EffectiveModel = strings.TrimSpace(args.Model)
	record.RequestedReasoningEffort = strings.TrimSpace(args.RequestedReasoningEffort)
	record.EffectiveReasoningEffort = firstNonEmptyRouteString(args.ReasoningEffort, args.ThinkingEffort)
	record.RequestedPermissionMode = strings.TrimSpace(args.RequestedPermissionMode)
	record.EffectivePermissionMode = firstNonEmptyRouteString(args.EffectivePermissionMode, args.PermissionMode)
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
	record.RequestedProvider = strings.TrimSpace(status.RequestedProvider)
	record.EffectiveProvider = firstNonEmptyRouteString(status.EffectiveProvider, status.Provider)
	record.RequestedModel = strings.TrimSpace(status.RequestedModel)
	record.EffectiveModel = firstNonEmptyRouteString(status.EffectiveModel, status.Model)
	record.RequestedReasoningEffort = strings.TrimSpace(status.RequestedReasoningEffort)
	record.EffectiveReasoningEffort = firstNonEmptyRouteString(status.EffectiveReasoningEffort, status.ReasoningEffort)
	record.RequestedPermissionMode = strings.TrimSpace(status.RequestedPermissionMode)
	record.EffectivePermissionMode = firstNonEmptyRouteString(status.EffectivePermissionMode, status.PermissionMode)
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
	if result.RequestedProvider == "" {
		result.RequestedProvider = strings.TrimSpace(record.RequestedProvider)
	}
	if result.EffectiveProvider == "" {
		result.EffectiveProvider = firstNonEmptyRouteString(record.EffectiveProvider, record.Provider)
	}
	if result.RequestedModel == "" {
		result.RequestedModel = strings.TrimSpace(record.RequestedModel)
	}
	if result.EffectiveModel == "" {
		result.EffectiveModel = firstNonEmptyRouteString(record.EffectiveModel, record.Model)
	}
	if result.RequestedReasoningEffort == "" {
		result.RequestedReasoningEffort = strings.TrimSpace(record.RequestedReasoningEffort)
	}
	if result.EffectiveReasoningEffort == "" {
		result.EffectiveReasoningEffort = firstNonEmptyRouteString(record.EffectiveReasoningEffort, record.ReasoningEffort)
	}
	if result.RequestedPermissionMode == "" {
		result.RequestedPermissionMode = strings.TrimSpace(record.RequestedPermissionMode)
	}
	if result.EffectivePermissionMode == "" {
		result.EffectivePermissionMode = strings.TrimSpace(record.EffectivePermissionMode)
	}
}

func SpawnAgentRunMeta(args SpawnAgentArgs) *team.RunMeta {
	permissionMode := strings.TrimSpace(args.PermissionMode)
	if permissionMode == "" && strings.TrimSpace(args.CompletionRequirement) == "" {
		return nil
	}
	return &team.RunMeta{
		PermissionMode:        permissionMode,
		CompletionRequirement: "none",
	}
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
