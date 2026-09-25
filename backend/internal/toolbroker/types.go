package toolbroker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/agentcontrol"
	"github.com/wwsheng009/ai-agent-runtime/internal/background"
	"github.com/wwsheng009/ai-agent-runtime/internal/team"
)

// ErrAgentSessionClosed reports an instruction aimed at a terminal
// (closed/archived) child session. Both hosts return it wrapped so callers can
// use errors.Is while still reading the target session from the message; the
// instruction is never silently dropped (P0-3a, plan §3.3).
var ErrAgentSessionClosed = errors.New("agent session is closed")

// AgentSessionClosedError builds the terminal-target rejection shared by both
// hosts. It keeps the P0-3a contract (the wrapped ErrAgentSessionClosed stays
// errors.Is-visible, so the instruction is never silently dropped) and carries
// the C3-7 / AC-P2-7d receipt hint: a terminal target can never accept the
// delivery, so the caller must converge instead of retrying the same send.
func AgentSessionClosedError(toolName, sessionID string) error {
	return fmt.Errorf(
		"%s target %s: %w; next_action=inspect|finalize — the target is terminal so this instruction was not delivered; read its durable result (subagent_inspect_task) or converge the parent instead of retrying",
		strings.TrimSpace(toolName), strings.TrimSpace(sessionID), ErrAgentSessionClosed,
	)
}

// AgentSteerApprovalFirstNextAction is the C3-7 / AC-P2-7b receipt hint: a steer
// aimed at a child blocked on tool approval must not jump the approval gate. The
// message stays queued and is injected only after the current run ends — that is,
// after the approval is resolved — so the caller is told to resolve the approval
// first instead of steering around it.
func AgentSteerApprovalFirstNextAction(approvalID, reason string) string {
	detail := strings.TrimSpace(reason)
	if id := strings.TrimSpace(approvalID); id != "" {
		if detail != "" {
			detail = id + " " + detail
		} else {
			detail = id
		}
	}
	if detail == "" {
		detail = "pending tool approval"
	}
	return "approval_first: the target is blocked on a pending approval (" + detail +
		"); call resolve_agent_approval with allow=true|false before steering — this message stays queued and is injected only after the approval resolves; do not bypass the approval gate"
}

// AgentWaitSteerInterruptNextAction explains an early wait exit caused by the
// caller being steered or interrupted (ESC, new input, or a turn cancel) instead
// of the observation window elapsing (C3-4 constraint ③, AC-P2-7e / AC-P2-4c).
func AgentWaitSteerInterruptNextAction() string {
	return "steer_pending: the wait segment ended early because the caller was interrupted or received new input (steer/ESC); handle the pending input now instead of re-waiting — the observed children keep running"
}

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
	TaskType            string   `json:"task_type,omitempty"`
	TaskSubject         string   `json:"task_subject,omitempty"`
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
	TeamID        string `json:"team_id"`
	Status        string `json:"status"`
	Terminal      bool   `json:"terminal"`
	TimedOut      bool   `json:"timed_out"`
	WaitTimeoutMs int    `json:"wait_timeout_ms,omitempty"`
	// WaitTimeoutRequestedMs / WaitTimeoutClamped echo how the requested team
	// observation window was normalized against agents.minWaitTimeoutMs /
	// agents.maxWaitTimeoutMs so a clamped wait is never silent.
	WaitTimeoutRequestedMs int  `json:"wait_timeout_requested_ms,omitempty"`
	WaitTimeoutClamped     bool `json:"wait_timeout_clamped,omitempty"`
	// WaitedMs is the observation window actually spent waiting, in
	// milliseconds (plan §C3-4 统一返回契约 waited_ms), measured from the start
	// of the wait loop so it stays comparable with wait_agent's waited_ms.
	WaitedMs           int64                  `json:"waited_ms,omitempty"`
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
	// Obligations / TerminalCount / TerminalDelta / PendingCount carry the same
	// wait-ledger view wait_agent returns (plan §C3-4 统一返回契约 / AC-P2-4g
	// 共享实现), scoped to the awaited team's tasks: subject_kind "team_task",
	// obligation id = task id. PendingCount counts the non-terminal task rows —
	// the rows that forbid the parent turn from finalizing (I1). TerminalDelta
	// lists the tasks that reached terminal state during this wait segment.
	Obligations   []AgentWaitObligation `json:"obligations,omitempty"`
	TerminalCount int                   `json:"terminal_count,omitempty"`
	TerminalDelta []string              `json:"terminal_delta,omitempty"`
	PendingCount  int                   `json:"pending_count,omitempty"`
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
	TaskType            string   `json:"task_type,omitempty"`
	TaskSubject         string   `json:"task_subject,omitempty"`
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
	// PlanPath is the primary plan artifact path (default plan.md). The broker
	// also accepts an array here (first entry = primary artifact, remaining
	// entries join the write allowlist) for callers that pass a plan file list.
	PlanPath string `json:"plan_path,omitempty"`
	// PlanWritePaths lists additional plan files that stay writable while plan
	// mode is active. It is a write-allowlist union with PlanPath: the primary
	// path remains the artifact surfaced in results and reviews.
	PlanWritePaths []string `json:"plan_write_paths,omitempty"`
	// Source identifies the entry author: "user" (host/CLI/API) or "model"
	// (agent tool call). Empty defaults to "user".
	Source string `json:"source,omitempty"`
}

// ExitPlanModeArgs describes exit_plan_mode tool input.
type ExitPlanModeArgs struct {
	// Decision is required: approve | request_changes | quit.
	Decision string `json:"decision"`
	// Notes are optional free-form notes recorded with the exit decision.
	Notes string `json:"notes,omitempty"`
	// Source identifies the decision author: "user" (host/CLI/API) or "model"
	// (agent tool call). Empty defaults to "user" for compatibility. Model
	// approve/quit is recorded as an exit request unless the host runs with
	// model autonomy enabled.
	Source string `json:"source,omitempty"`
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
	// PendingExitRequest is true when a model/host exit request is waiting for
	// the user's verdict; the session stays in plan mode meanwhile.
	PendingExitRequest bool `json:"pending_exit_request,omitempty"`
	// ExitSource is the author of the last decision/request: user|model.
	ExitSource string `json:"exit_source,omitempty"`
	// ReviewRound counts completed review rounds for the current plan session.
	ReviewRound int `json:"review_round,omitempty"`
	// PendingReviewNotes carries user review feedback not yet delivered to the model.
	PendingReviewNotes string `json:"pending_review_notes,omitempty"`
}

// PlanModeController toggles durable plan mode for the current session mid-turn.
// Implementations must persist session plan_mode context, apply the live
// permission engine, and update RunMeta.PermissionMode when present so the
// remainder of the turn evaluates tools under plan (or restored) mode.
type PlanModeController interface {
	EnterPlanMode(ctx context.Context, sessionID string, args EnterPlanModeArgs) (*PlanModeResult, error)
	ExitPlanMode(ctx context.Context, sessionID string, args ExitPlanModeArgs) (*PlanModeResult, error)
}

// PlanReviewArgs describes plan_review tool input.
type PlanReviewArgs struct {
	// PlanID selects an archived plan (planstore id; may contain "/", e.g.
	// "ai-agent-runtime/plan"). When set it wins over PlanPath.
	PlanID string `json:"plan_id,omitempty"`
	// PlanPath selects a plan file directly, relative to the session workspace.
	// Empty means the current session's active plan (or last known plan path).
	PlanPath string `json:"plan_path,omitempty"`
	// Version selects an archived snapshot version; 0 means latest.
	Version int `json:"version,omitempty"`
}

// PlanReviewResult is the review payload returned by plan_review: the plan text
// plus the state a reviewer needs (status, round, verdict entry points).
type PlanReviewResult struct {
	Active      bool   `json:"active,omitempty"`
	Status      string `json:"status,omitempty"`
	PlanID      string `json:"plan_id,omitempty"`
	PlanPath    string `json:"plan_path,omitempty"`
	Version     int    `json:"version,omitempty"`
	ReviewRound int    `json:"review_round,omitempty"`
	Source      string `json:"source,omitempty"` // session|archive
	Content     string `json:"content,omitempty"`
	ContentSize int    `json:"content_size,omitempty"`
	Truncated   bool   `json:"truncated,omitempty"`
	// VerdictOptions lists the host commands a user can run to decide the review.
	VerdictOptions []string `json:"verdict_options,omitempty"`
	// Hint is a ready-to-relay sentence the model can show the user.
	Hint string `json:"hint,omitempty"`
}

// PlanReviewController loads a plan (session or archived) for the review
// surface behind the plan_review tool. Implementations must not mutate plan
// state: opening a review never decides it.
type PlanReviewController interface {
	ReviewPlan(ctx context.Context, sessionID string, args PlanReviewArgs) (*PlanReviewResult, error)
}

// SpawnAgentArgs describes a lightweight child-agent session request.
type SpawnAgentArgs struct {
	ID                  string `json:"id,omitempty"`
	SessionID           string `json:"session_id,omitempty"`
	Message             string `json:"message,omitempty"`
	AgentType           string `json:"agent_type,omitempty"`
	Difficulty          string `json:"difficulty,omitempty"`
	DifficultyRationale string `json:"difficulty_rationale,omitempty"`
	TaskType            string `json:"task_type,omitempty"`
	TaskSubject         string `json:"task_subject,omitempty"`
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
	Isolation   string `json:"isolation,omitempty"`
	ReadOnly    bool   `json:"read_only,omitempty"`
	ForkContext *bool  `json:"fork_context,omitempty"`
	ForkTurns   string `json:"fork_turns,omitempty"`
	// ParentToolCallID 是发起本次 spawn 的父侧 tool_call_id，由 broker 在执行
	// spawn_agent 工具调用时内部注入（不来自模型入参，json:"-" 不落地/不回显）。
	// 宿主把它写进子会话上下文，subagent.progress 镜像据此回填
	// parent_tool_call_id，供前端/ACP 把进度归位到 spawn_agent 行。
	ParentToolCallID string `json:"-"`
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
	TaskType                 string   `json:"task_type,omitempty"`
	TaskSubject              string   `json:"task_subject,omitempty"`
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
	// Delivered/Triggered/Duplicate mirror AgentMessageResult for send_input,
	// whose v2 return type is this status result (P0-3a, plan §3.3). All three
	// are omitted when false, so pre-v2 hosts serialize the same fields.
	Delivered bool `json:"delivered,omitempty"`
	Triggered bool `json:"triggered,omitempty"`
	Duplicate bool `json:"duplicate,omitempty"`
	TimedOut  bool `json:"timed_out,omitempty"`
	// NextAction carries the actionable guidance for a steer receipt, e.g. the
	// AC-P2-7b approval-first hint when the target is blocked on tool approval
	// (see AgentSteerApprovalFirstNextAction). Omitted when empty so hosts that
	// do not set it serialize the same fields as before.
	NextAction string `json:"next_action,omitempty"`
	// RunID is the durable execution run identity assigned by the execution
	// supervisor at spawn time (doc 7.1). Empty when supervision is disabled.
	RunID               string `json:"run_id,omitempty"`
	ExecutionDeadlineAt string `json:"execution_deadline_at,omitempty"`
	SupervisionPolicy   string `json:"supervision_policy,omitempty"`
	// RunStatus is the supervision execution run status (running,
	// waiting_approval, timed_out, orphaned, ...). More precise than the
	// session-level Status for diagnosing stalled children.
	RunStatus                string   `json:"run_status,omitempty"`
	Attempt                  int      `json:"attempt,omitempty"`
	MaxAttempts              int      `json:"max_attempts,omitempty"`
	RunOwnerID               string   `json:"run_owner_id,omitempty"`
	LastHeartbeatAt          string   `json:"last_heartbeat_at,omitempty"`
	LastProgressAt           string   `json:"last_progress_at,omitempty"`
	ProgressDeadlineAt       string   `json:"progress_deadline_at,omitempty"`
	CancelDeadlineAt         string   `json:"cancel_deadline_at,omitempty"`
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
	WaitTimeoutMs    int                 `json:"wait_timeout_ms,omitempty"`
	// WaitTimeoutRequestedMs / WaitTimeoutClamped echo how the requested
	// observation window was normalized against agents.minWaitTimeoutMs /
	// agents.maxWaitTimeoutMs so a clamped wait is never silent.
	WaitTimeoutRequestedMs int      `json:"wait_timeout_requested_ms,omitempty"`
	WaitTimeoutClamped     bool     `json:"wait_timeout_clamped,omitempty"`
	ExecutionContinues     bool     `json:"execution_continues,omitempty"`
	ReadyCount             int      `json:"ready_count,omitempty"`
	PendingCount           int      `json:"pending_count,omitempty"`
	ReadyIDs               []string `json:"ready_ids,omitempty"`
	PendingIDs             []string `json:"pending_ids,omitempty"`
	WaitedMs               int64    `json:"waited_ms,omitempty"`
	NextAction             string   `json:"next_action,omitempty"`
	// Interrupted reports that the wait segment ended because the caller was
	// steered/interrupted instead of the observation window elapsing. AC-P2-7e /
	// AC-P2-4c: such a wait must end immediately and say so, never masquerade as
	// a timeout.
	Interrupted bool `json:"interrupted,omitempty"`
	// Obligations is the wait-time view of the current turn's obligation ledger
	// (plan §C3-4 统一返回契约). It is read back from the durable §6.12
	// parked-turn record plus the batch control plane, so a parent sees exactly
	// the obligations that gate its turn instead of a caller-supplied summary.
	// TerminalCount counts the rows that are already terminal; TerminalDelta
	// lists the obligation ids that reached terminal during this wait segment.
	// PendingCount keeps its session-view meaning (ready/pending agent
	// sessions) — the ledger's pending rows are the non-terminal entries of
	// Obligations.
	Obligations   []AgentWaitObligation `json:"obligations,omitempty"`
	TerminalCount int                   `json:"terminal_count,omitempty"`
	TerminalDelta []string              `json:"terminal_delta,omitempty"`
}

// AgentWaitObligation is one row of the wait-time obligation ledger view
// (plan §C3-4 统一返回契约 obligations[]). Today every obligation is a
// dispatched batch, so SubjectKind is "batch" and the obligation id is the
// batch id; the row is shaped so agent_run / team_task subjects can be added
// later without changing the wait contract.
type AgentWaitObligation struct {
	ObligationID string `json:"obligation_id"`
	SubjectKind  string `json:"subject_kind,omitempty"`
	SubjectID    string `json:"subject_id,omitempty"`
	State        string `json:"state,omitempty"`
	// Terminal reports whether this obligation can no longer transition. A row
	// that is not terminal is exactly what forbids the parent turn from
	// finalizing (I1).
	Terminal bool `json:"terminal,omitempty"`
	// DeadlineAt is the obligation's declared deadline (the batch deadline,
	// falling back to the parked turn's decision window) in RFC3339.
	DeadlineAt string `json:"deadline_at,omitempty"`
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

// ApplyAgentWaitTimeout records the normalized observation window on a wait
// result. requestedMs is echoed verbatim (0 means "host default"), effectiveMs
// is the window the host actually used, and clamped reports whether the request
// was pinned to agents.minWaitTimeoutMs / agents.maxWaitTimeoutMs.
func ApplyAgentWaitTimeout(result *AgentWaitResult, requestedMs, effectiveMs int, clamped bool) *AgentWaitResult {
	if result == nil {
		return nil
	}
	result.WaitTimeoutRequestedMs = requestedMs
	if effectiveMs > 0 {
		result.WaitTimeoutMs = effectiveMs
	}
	result.WaitTimeoutClamped = clamped
	return result
}

// ApplyAgentWaitLedger attaches the current turn's obligation ledger view to a
// wait result and derives next_action from it (plan §C3-4 统一返回契约,
// AC-P2-4d/AC-P2-4e). baselineTerminal holds the obligation ids that were
// already terminal before this wait segment started, so terminal_delta reports
// exactly what finished during the wait.
//
// The ledger is decisive in one direction only: when no row is pending, there
// is nothing left to wait for, so next_action becomes "finalize" (the
// empty-ledger rule). When rows are pending the parent must not finalize (I1),
// so an existing guidance string is preserved and only an empty one is filled
// with the conservative continue_wait/inspect hint. Call this after
// FinalizeAgentWaitResult so the ledger rule is the last word on next_action.
func ApplyAgentWaitLedger(result *AgentWaitResult, obligations []AgentWaitObligation, baselineTerminal []string) *AgentWaitResult {
	if result == nil || len(obligations) == 0 {
		return result
	}
	view := SummarizeAgentWaitLedger(obligations, baselineTerminal)
	result.Obligations = view.Obligations
	// Counts are derived from the rows, so re-applying the ledger to the same
	// result stays idempotent instead of double-counting.
	result.TerminalCount = view.TerminalCount
	if len(view.TerminalDelta) > 0 {
		result.TerminalDelta = view.TerminalDelta
	}
	switch {
	case view.PendingCount == 0:
		result.NextAction = "finalize"
	case strings.TrimSpace(result.NextAction) == "":
		if result.TimedOut {
			result.NextAction = "continue_wait"
		} else {
			result.NextAction = "inspect"
		}
	}
	return result
}

// AgentWaitLedgerView is the shared obligation-ledger computation behind both
// wait_agent (ApplyAgentWaitLedger) and wait_team (ApplyWaitTeamLedger), so the
// two wait paths cannot drift in how rows, counts and the terminal delta are
// derived (plan §C3-4 统一返回契约 / AC-P2-4g 共享实现).
type AgentWaitLedgerView struct {
	Obligations   []AgentWaitObligation
	TerminalCount int
	PendingCount  int
	TerminalDelta []string
}

// SummarizeAgentWaitLedger derives the ledger view from the rows and the
// pre-segment terminal baseline. A non-terminal row is exactly what forbids the
// parent turn from finalizing (I1); TerminalDelta reports only the rows that
// reached terminal state after the baseline.
func SummarizeAgentWaitLedger(obligations []AgentWaitObligation, baselineTerminal []string) AgentWaitLedgerView {
	view := AgentWaitLedgerView{Obligations: obligations}
	baseline := make(map[string]struct{}, len(baselineTerminal))
	for _, id := range baselineTerminal {
		if trimmed := strings.TrimSpace(id); trimmed != "" {
			baseline[trimmed] = struct{}{}
		}
	}
	for _, row := range obligations {
		if !row.Terminal {
			view.PendingCount++
			continue
		}
		view.TerminalCount++
		if _, seen := baseline[strings.TrimSpace(row.ObligationID)]; !seen {
			view.TerminalDelta = append(view.TerminalDelta, row.ObligationID)
		}
	}
	return view
}

// ApplyWaitTeamLedger attaches the team-task obligation ledger to a wait_team
// result using the same row computation as wait_agent (AC-P2-4g 共享实现). Rows
// are the awaited team's tasks with subject_kind "team_task".
//
// It is decisive in one direction only, and deliberately narrower than the
// wait_agent rule: "finalize" is only stamped when the team itself is terminal
// and no task row is pending. A non-terminal team (e.g. one that has not
// planned its tasks yet, so the ledger is empty) must never produce "finalize",
// and more specific guidance already on the result — such as "team is terminal
// but summary is not ready" — is preserved instead of being flattened. This is
// the fail-open direction of plan §13.8: never claim the parent may stop while
// the awaited team is still running.
func ApplyWaitTeamLedger(result *WaitTeamResult, obligations []AgentWaitObligation, baselineTerminal []string) *WaitTeamResult {
	if result == nil || len(obligations) == 0 {
		return result
	}
	view := SummarizeAgentWaitLedger(obligations, baselineTerminal)
	result.Obligations = view.Obligations
	result.TerminalCount = view.TerminalCount
	result.PendingCount = view.PendingCount
	if len(view.TerminalDelta) > 0 {
		result.TerminalDelta = view.TerminalDelta
	}
	if result.Terminal && view.PendingCount == 0 && strings.TrimSpace(result.NextAction) == "" {
		result.NextAction = "finalize"
	}
	return result
}

// ApplyWaitTeamTimeout records the normalized observation window on a wait_team
// result. wait_team shares the agents.minWaitTimeoutMs / agents.maxWaitTimeoutMs
// policy with wait_agent, so a team wait must echo the same request/effective/
// clamped triple: requestedMs is echoed verbatim (0 means "host default"),
// effectiveMs is the window the host actually used, and clamped reports whether
// the request was pinned to a bound.
func ApplyWaitTeamTimeout(result *WaitTeamResult, requestedMs, effectiveMs int, clamped bool) *WaitTeamResult {
	if result == nil {
		return nil
	}
	result.WaitTimeoutRequestedMs = requestedMs
	if effectiveMs > 0 {
		result.WaitTimeoutMs = effectiveMs
	}
	result.WaitTimeoutClamped = clamped
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
	if result.Interrupted {
		// AC-P2-7e / AC-P2-4c：被 steer/打断而提前结束的等待段优先于其它调度引导，
		// 且不得谎报为观测窗口超时。
		result.TimedOut = false
		result.ExecutionContinues = true
		result.NextAction = AgentWaitSteerInterruptNextAction()
		return result
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
//
// v2 semantics (P0-3a, plan §3.3) add Queued and Duplicate; both are omitted
// when false so a host that keeps MessageSemanticsV2 disabled still serializes
// exactly the pre-v2 result.
type AgentMessageResult struct {
	TargetSessionID string `json:"target_session_id"`
	Delivered       bool   `json:"delivered"`
	Triggered       bool   `json:"triggered,omitempty"`
	// Queued reports that delivery waits in the child mailbox until the child
	// consumes it (a busy followup_task / send_input(interrupt=false), or every
	// send_message).
	Queued bool `json:"queued,omitempty"`
	// Duplicate reports that the durable mailbox id was already committed, so
	// the retry is an idempotent hit and no second turn/message is produced.
	Duplicate bool               `json:"duplicate,omitempty"`
	Status    *AgentStatusResult `json:"status,omitempty"`
}

// Approval resolutions reported on AgentApprovalResult.Resolution. They mirror
// the chat approval_resolved event so both hosts describe one decision the same
// way (P0-3/P1-1).
const (
	ApprovalResolutionAllowed             = "allowed"
	ApprovalResolutionDenied              = "denied"
	ApprovalResolutionExpired             = "expired"
	ApprovalResolutionRunTerminated       = "run_terminated"
	ApprovalResolutionRunTerminalNoResume = "run_terminal_no_resume"
)

// ApprovalResolutionNotApplied reports whether a resolution describes a decision
// that was recorded but never applied to the child run, because that run had
// already terminated. Hosts must report those decisions as not allowed and as
// not resumed (P0-3).
func ApprovalResolutionNotApplied(resolution string) bool {
	switch strings.TrimSpace(resolution) {
	case ApprovalResolutionRunTerminated, ApprovalResolutionRunTerminalNoResume:
		return true
	default:
		return false
	}
}

// AgentApprovalResult reports the resolved child-agent tool approval.
type AgentApprovalResult struct {
	SessionID string `json:"session_id"`
	RequestID string `json:"request_id"`
	Allowed   bool   `json:"allowed"`
	Resolved  bool   `json:"resolved"`
	// Resumed reports whether a recovery run was started to apply the decision.
	// Resolution=run_terminal_no_resume with Resumed=false means the child run had
	// already ended (execution deadline / external cancel) and was not restarted:
	// the decision is recorded, no work continues.
	Resumed    bool               `json:"resumed"`
	Resolution string             `json:"resolution,omitempty"`
	Status     *AgentStatusResult `json:"status,omitempty"`
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
	// View optionally projects the returned window (plan P1-5 方案 3). "" or
	// "all" keeps every event; "tool_progress" keeps tool events plus the
	// lifecycle events a parent must not miss (terminal states, approvals).
	View string `json:"view,omitempty"`
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
	// HasMore reports that the event store held at least one more event than the
	// returned window (the read over-fetches one row to detect it).
	HasMore bool `json:"has_more,omitempty"`
	// UnreadCount is how many events remain beyond the returned window. It is
	// capped at AgentEventsUnreadProbeLimit, so a value at the cap means
	// "at least this many".
	UnreadCount int `json:"unread_count,omitempty"`
	// View is the canonical projection that produced Events (plan P1-5 方案 3).
	// It is only set when the caller asked for a projection, so a default
	// `view=all` read keeps its historical JSON shape.
	View string `json:"view,omitempty"`
	// Filtered counts events the projection dropped from the raw read window.
	Filtered int `json:"filtered,omitempty"`
	// Unchanged reports that this read returned the same window as the previous
	// read of the same caller/target/after_seq: the event high-water mark did not
	// advance, so the payload repeats the previous answer (and would otherwise hit
	// the provider prompt cache byte-for-byte). It is the explicit signal behind
	// plan P1-7's repeated-after_seq guidance.
	Unchanged bool `json:"unchanged,omitempty"`
	// RepeatCount counts consecutive identical reads of that unchanged window. It
	// is >= 1 whenever Unchanged is set, so a caller can tell "same answer again"
	// from "the window did not move the first time".
	RepeatCount int    `json:"repeat_count,omitempty"`
	NextAction  string `json:"next_action,omitempty"`
}

// AgentEventsUnreadProbeLimit bounds the follow-up store probe that counts how
// many events remain beyond a returned read window.
const AgentEventsUnreadProbeLimit = 200

// CapAgentEventsUnread bounds an unread probe count to AgentEventsUnreadProbeLimit.
func CapAgentEventsUnread(count int) int {
	if count <= 0 {
		return 0
	}
	if count > AgentEventsUnreadProbeLimit {
		return AgentEventsUnreadProbeLimit
	}
	return count
}

// Agent events views (plan P1-5 方案 3): a parent that only needs progress can
// ask for the tool projection instead of paying tokens for every assistant and
// reasoning chunk in the window.
const (
	AgentEventsViewAll          = "all"
	AgentEventsViewToolProgress = "tool_progress"
)

// agentEventsToolProgressSticky lists the non-tool event types the
// tool_progress view keeps. Losing a terminal state or an approval request
// would be worse than the tokens the projection saves, so they always survive
// it (P1-5 测试与验收: 不丢终态).
var agentEventsToolProgressSticky = map[string]bool{
	"session_end":        true,
	"agent.completed":    true,
	"agent.failed":       true,
	"agent.cancelled":    true,
	"agent.reclaimed":    true,
	"approval_requested": true,
	"approval_resolved":  true,
}

// NormalizeAgentEventsView canonicalizes a requested view. Unknown values
// degrade to AgentEventsViewAll so a typo can never silently hide events.
func NormalizeAgentEventsView(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case AgentEventsViewToolProgress:
		return AgentEventsViewToolProgress
	default:
		return AgentEventsViewAll
	}
}

// KeepsAgentEventForView reports whether one event survives the view. The
// "all" view (and any unknown value) keeps everything.
func KeepsAgentEventForView(view, eventType string) bool {
	if NormalizeAgentEventsView(view) != AgentEventsViewToolProgress {
		return true
	}
	normalized := strings.ToLower(strings.TrimSpace(eventType))
	if normalized == "" {
		return false
	}
	if strings.HasPrefix(normalized, "tool.") {
		return true
	}
	return agentEventsToolProgressSticky[normalized]
}

// ApplyAgentEventsView projects result.Events for the requested view and records
// the canonical view plus how many events it dropped. `view=all`, unknown views
// and nil results are no-ops, so callers that do not opt in see no shape change.
//
// Pagination metadata (has_more/unread_count/next_action) is intentionally left
// alone: it describes the raw window, and after_seq=latest_seq still advances
// past the filtered events, so the parent's cursor contract does not change.
func ApplyAgentEventsView(result *AgentEventsResult, view string) *AgentEventsResult {
	if result == nil {
		return nil
	}
	canonical := NormalizeAgentEventsView(view)
	if canonical == AgentEventsViewAll {
		return result
	}
	result.View = canonical
	if len(result.Events) == 0 {
		return result
	}
	kept := make([]AgentEventItem, 0, len(result.Events))
	for _, event := range result.Events {
		if KeepsAgentEventForView(canonical, event.Type) {
			kept = append(kept, event)
			continue
		}
		result.Filtered++
	}
	result.Events = kept
	result.Count = len(kept)
	return result
}

// ApplyAgentEventsPagination annotates a read window with has_more/unread_count
// and refreshes next_action so a parent consumes the current page before
// re-reading with a higher after_seq. hasMore=false leaves the result untouched
// (including the polling guidance set by FinalizeAgentEventsResult).
func ApplyAgentEventsPagination(result *AgentEventsResult, hasMore bool, unread int) *AgentEventsResult {
	if result == nil || !hasMore {
		return result
	}
	result.HasMore = true
	unread = CapAgentEventsUnread(unread)
	result.UnreadCount = unread
	detail := "more event(s) remain"
	switch {
	case unread >= AgentEventsUnreadProbeLimit:
		detail = fmt.Sprintf("at least %d more event(s) remain", unread)
	case unread > 0:
		detail = fmt.Sprintf("%d more event(s) remain", unread)
	}
	result.NextAction = fmt.Sprintf("consume_events_then_advance: %d event(s) returned (latest_seq=%d); %s — consume these first, then re-call read_agent_events with after_seq=%d", result.Count, result.LatestSeq, detail, result.LatestSeq)
	return result
}

// MarkAgentEventsRepeatedRead records an explicit "you already read this exact
// window" signal on a read that repeats the previous caller/target/after_seq
// window without new events. Without it the second answer is byte-identical to
// the first (a prompt-cache hit), which leaves the model without in-band
// evidence that its cursor did not move (plan P1-7 待补).
func MarkAgentEventsRepeatedRead(result *AgentEventsResult, afterSeq int64, repeatCount int) *AgentEventsResult {
	if result == nil {
		return nil
	}
	if repeatCount < 1 {
		repeatCount = 1
	}
	result.Unchanged = true
	result.RepeatCount = repeatCount
	result.NextAction = fmt.Sprintf("unchanged_window: identical read #%d with after_seq=%d returned the same window (no new events); the payload adds no information — do other work, use wait_agent for readiness or a longer wait, and advance after_seq only once new events exist", repeatCount, afterSeq)
	return result
}

// ApplyAgentWorktreeArgs applies a child's worktree isolation changes into the main repo.
type ApplyAgentWorktreeArgs struct {
	ID        string `json:"id,omitempty"`
	SessionID string `json:"session_id,omitempty"`
	// Paths limits apply to specific relative paths. Empty = all tracked changes.
	Paths []string `json:"paths,omitempty"`
	// Keep preserves the worktree after apply (default false removes it).
	Keep bool `json:"keep,omitempty"`
	// Force overwrites local main-tree modifications instead of refusing the
	// apply (H14 preflight). Default false: conflicts fail the call with a
	// next_action instead of silently losing local edits.
	Force bool `json:"force,omitempty"`
}

// DiscardAgentWorktreeArgs discards a child's worktree isolation without applying changes.
type DiscardAgentWorktreeArgs struct {
	ID        string `json:"id,omitempty"`
	SessionID string `json:"session_id,omitempty"`
}

// AgentWorktreeResult reports apply/discard outcomes for worktree isolation.
type AgentWorktreeResult struct {
	ID             string   `json:"id,omitempty"`
	SessionID      string   `json:"session_id,omitempty"`
	Action         string   `json:"action"` // apply | discard
	Isolation      string   `json:"isolation,omitempty"`
	WorktreePath   string   `json:"worktree_path,omitempty"`
	WorktreeBranch string   `json:"worktree_branch,omitempty"`
	RepoRoot       string   `json:"repo_root,omitempty"`
	DiffStat       string   `json:"diff_stat,omitempty"`
	Paths          []string `json:"paths,omitempty"`
	Applied        bool     `json:"applied,omitempty"`
	Discarded      bool     `json:"discarded,omitempty"`
	Removed        bool     `json:"removed,omitempty"`
	Kept           bool     `json:"kept,omitempty"`
	// Conflicts lists main-tree paths whose local changes blocked the apply
	// (H14 preflight, force=false).
	Conflicts []string `json:"conflicts,omitempty"`
	// SkippedPaths lists worktree changes outside the requested paths filter,
	// which this call did not apply.
	SkippedPaths []string `json:"skipped_paths,omitempty"`
	// NextAction carries the actionable guidance for a refused apply.
	NextAction string             `json:"next_action,omitempty"`
	Status     *AgentStatusResult `json:"status,omitempty"`
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
	AgentSessionContextParentToolCallID      = agentcontrol.SessionContextParentToolCallID
	AgentSessionContextRootSessionID         = agentcontrol.SessionContextRootSessionID
	AgentSessionContextAgentType             = agentcontrol.SessionContextAgentType
	AgentSessionContextRequestedModel        = agentcontrol.SessionContextRequestedModel
	AgentSessionContextDifficulty            = agentcontrol.SessionContextDifficulty
	AgentSessionContextDifficultySource      = agentcontrol.SessionContextDifficultySource
	AgentSessionContextDifficultyRationale   = agentcontrol.SessionContextDifficultyRationale
	AgentSessionContextTaskType              = agentcontrol.SessionContextTaskType
	AgentSessionContextTaskSubject           = agentcontrol.SessionContextTaskSubject
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
	if taskType := strings.TrimSpace(args.TaskType); taskType != "" {
		session.SetContext(AgentSessionContextTaskType, taskType)
	}
	if taskSubject := strings.TrimSpace(args.TaskSubject); taskSubject != "" {
		session.SetContext(AgentSessionContextTaskSubject, taskSubject)
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
	if taskType := agentcontrol.ContextString(session, AgentSessionContextTaskType); taskType != "" {
		result.TaskType = taskType
	}
	if taskSubject := agentcontrol.ContextString(session, AgentSessionContextTaskSubject); taskSubject != "" {
		result.TaskSubject = taskSubject
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
