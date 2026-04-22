package chat

import (
	"encoding/json"
	"time"

	runtimepolicy "github.com/wwsheng009/ai-agent-runtime/internal/policy"
	"github.com/wwsheng009/ai-agent-runtime/internal/team"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolbroker"
	"github.com/wwsheng009/ai-agent-runtime/internal/types"
)

// PendingToolInvocation captures the tool call that is currently paused.
type PendingToolInvocation struct {
	ToolCallID           string            `json:"tool_call_id"`
	ToolName             string            `json:"tool_name"`
	ArgsJSON             json.RawMessage   `json:"args_json,omitempty"`
	BatchToolCalls       []PendingToolCall `json:"batch_tool_calls,omitempty"`
	ExecutionState       string            `json:"execution_state,omitempty"`
	ExecutionStartedAt   time.Time         `json:"execution_started_at,omitempty"`
	ResultMessageJSON    json.RawMessage   `json:"result_message_json,omitempty"`
	ExecutionCompletedAt time.Time         `json:"execution_completed_at,omitempty"`
	CreatedAt            time.Time         `json:"created_at,omitempty"`
}

// PendingToolCall captures one tool call within the paused assistant turn batch.
type PendingToolCall struct {
	ToolCallID string          `json:"tool_call_id"`
	ToolName   string          `json:"tool_name"`
	ArgsJSON   json.RawMessage `json:"args_json,omitempty"`
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
	FrozenTurnTools     []types.ToolDefinition `json:"frozen_turn_tools,omitempty"`
	FrozenTurnToolsSet  bool                   `json:"frozen_turn_tools_set,omitempty"`
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
	clone.FrozenTurnTools = cloneRuntimeToolDefinitions(s.FrozenTurnTools)
	if s.PendingTool != nil {
		pendingTool := *s.PendingTool
		if len(pendingTool.ArgsJSON) > 0 {
			pendingTool.ArgsJSON = append(json.RawMessage(nil), pendingTool.ArgsJSON...)
		}
		if len(pendingTool.BatchToolCalls) > 0 {
			pendingTool.BatchToolCalls = make([]PendingToolCall, len(s.PendingTool.BatchToolCalls))
			for index := range s.PendingTool.BatchToolCalls {
				pendingTool.BatchToolCalls[index] = PendingToolCall{
					ToolCallID: s.PendingTool.BatchToolCalls[index].ToolCallID,
					ToolName:   s.PendingTool.BatchToolCalls[index].ToolName,
				}
				if len(s.PendingTool.BatchToolCalls[index].ArgsJSON) > 0 {
					pendingTool.BatchToolCalls[index].ArgsJSON = append(json.RawMessage(nil), s.PendingTool.BatchToolCalls[index].ArgsJSON...)
				}
			}
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

func cloneRuntimeToolDefinitions(input []types.ToolDefinition) []types.ToolDefinition {
	if len(input) == 0 {
		return nil
	}
	cloned := make([]types.ToolDefinition, len(input))
	for index, tool := range input {
		cloned[index] = types.ToolDefinition{
			Name:        tool.Name,
			Description: tool.Description,
			Parameters:  cloneRuntimeInterfaceMap(tool.Parameters),
			Metadata:    cloneRuntimeInterfaceMap(tool.Metadata),
		}
	}
	return cloned
}

func cloneRuntimeInterfaceMap(input map[string]interface{}) map[string]interface{} {
	if len(input) == 0 {
		return nil
	}
	cloned := make(map[string]interface{}, len(input))
	for key, value := range input {
		cloned[key] = cloneRuntimeInterfaceValue(value)
	}
	return cloned
}

func cloneRuntimeInterfaceValue(value interface{}) interface{} {
	switch typed := value.(type) {
	case map[string]interface{}:
		return cloneRuntimeInterfaceMap(typed)
	case []interface{}:
		cloned := make([]interface{}, len(typed))
		for index, item := range typed {
			cloned[index] = cloneRuntimeInterfaceValue(item)
		}
		return cloned
	default:
		return typed
	}
}
