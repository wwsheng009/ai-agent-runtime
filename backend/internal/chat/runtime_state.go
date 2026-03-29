package chat

import (
	"encoding/json"
	"time"

	runtimepolicy "github.com/ai-gateway/ai-agent-runtime/internal/policy"
	"github.com/ai-gateway/ai-agent-runtime/internal/toolbroker"
	"github.com/ai-gateway/ai-agent-runtime/internal/team"
)

// PendingToolInvocation captures the tool call that is currently paused.
type PendingToolInvocation struct {
	ToolCallID           string          `json:"tool_call_id"`
	ToolName             string          `json:"tool_name"`
	ArgsJSON             json.RawMessage `json:"args_json,omitempty"`
	ExecutionState       string          `json:"execution_state,omitempty"`
	ExecutionStartedAt   time.Time       `json:"execution_started_at,omitempty"`
	ResultMessageJSON    json.RawMessage `json:"result_message_json,omitempty"`
	ExecutionCompletedAt time.Time       `json:"execution_completed_at,omitempty"`
	CreatedAt            time.Time       `json:"created_at,omitempty"`
}

const (
	PendingToolExecutionStarted   = "started"
	PendingToolExecutionCompleted = "completed"
)

// ToolExecutionReceipt stores a persisted tool result that can be replayed after a crash.
type ToolExecutionReceipt struct {
	SessionID   string          `json:"session_id"`
	ToolCallID  string          `json:"tool_call_id"`
	ToolName    string          `json:"tool_name,omitempty"`
	MessageJSON json.RawMessage `json:"message_json"`
	CreatedAt   time.Time       `json:"created_at"`
}

// SessionStatus represents the lifecycle state of a session actor.
type SessionStatus string

const (
	SessionIdle            SessionStatus = "idle"
	SessionRunning         SessionStatus = "running"
	SessionWaitingApproval SessionStatus = "waiting_approval"
	SessionWaitingInput    SessionStatus = "waiting_input"
	SessionRewinding       SessionStatus = "rewinding"
	SessionStopped         SessionStatus = "stopped"
)

// ApprovalRequest captures an interactive approval requirement for tool execution.
type ApprovalRequest = runtimepolicy.ApprovalRequest

// UserQuestionRequest captures a question that requires user input before resuming.
type UserQuestionRequest = toolbroker.UserQuestionRequest

// RuntimeState tracks the session actor state across turns.
type RuntimeState struct {
	SessionID           string                 `json:"session_id"`
	Status              SessionStatus          `json:"status"`
	CurrentTurnID       string                 `json:"current_turn_id,omitempty"`
	CurrentCheckpointID string                 `json:"current_checkpoint_id,omitempty"`
	CurrentRunMeta      *team.RunMeta          `json:"current_run_meta,omitempty"`
	AmbientRunMeta      *team.RunMeta          `json:"ambient_run_meta,omitempty"`
	PendingTool         *PendingToolInvocation `json:"pending_tool,omitempty"`
	PendingApproval     *ApprovalRequest       `json:"pending_approval,omitempty"`
	PendingQuestion     *UserQuestionRequest   `json:"pending_question,omitempty"`
	HeadOffset          int64                  `json:"head_offset"`
	ActiveJobIDs        []string               `json:"active_job_ids,omitempty"`
	UpdatedAt           time.Time              `json:"updated_at"`
}

// Clone returns a defensive copy of the runtime state.
func (s *RuntimeState) Clone() *RuntimeState {
	if s == nil {
		return nil
	}
	clone := *s
	clone.CurrentRunMeta = s.CurrentRunMeta.Clone()
	clone.AmbientRunMeta = s.AmbientRunMeta.Clone()
	if s.PendingTool != nil {
		pendingTool := *s.PendingTool
		if len(pendingTool.ArgsJSON) > 0 {
			pendingTool.ArgsJSON = append(json.RawMessage(nil), pendingTool.ArgsJSON...)
		}
		if len(pendingTool.ResultMessageJSON) > 0 {
			pendingTool.ResultMessageJSON = append(json.RawMessage(nil), pendingTool.ResultMessageJSON...)
		}
		clone.PendingTool = &pendingTool
	}
	if s.PendingApproval != nil {
		approval := *s.PendingApproval
		if len(approval.ArgsJSON) > 0 {
			approval.ArgsJSON = append(json.RawMessage(nil), approval.ArgsJSON...)
		}
		clone.PendingApproval = &approval
	}
	if s.PendingQuestion != nil {
		question := *s.PendingQuestion
		if len(question.Suggestions) > 0 {
			question.Suggestions = append([]string(nil), question.Suggestions...)
		}
		clone.PendingQuestion = &question
	}
	if len(s.ActiveJobIDs) > 0 {
		clone.ActiveJobIDs = append([]string(nil), s.ActiveJobIDs...)
	}
	return &clone
}
