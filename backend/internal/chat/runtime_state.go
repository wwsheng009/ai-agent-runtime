package chat

import (
	"encoding/json"
	"strings"
	"time"

	runtimepolicy "github.com/wwsheng009/ai-agent-runtime/internal/policy"
	"github.com/wwsheng009/ai-agent-runtime/internal/team"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolbroker"
	"github.com/wwsheng009/ai-agent-runtime/internal/types"
)

// PendingToolInvocation captures the tool call that is currently paused.
type PendingToolInvocation struct {
	ToolCallID           string            `json:"tool_call_id"`
	ToolType             string            `json:"tool_type,omitempty"`
	ToolName             string            `json:"tool_name"`
	ArgsJSON             json.RawMessage   `json:"args_json,omitempty"`
	RawInput             string            `json:"raw_input,omitempty"`
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
	ToolType   string          `json:"tool_type,omitempty"`
	ToolName   string          `json:"tool_name"`
	ArgsJSON   json.RawMessage `json:"args_json,omitempty"`
	RawInput   string          `json:"raw_input,omitempty"`
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

	// OK / FailureCategory 是回执的结果证据（方案 §0.3：失败工具同样落回执）。
	// nil 表示未知（不猜测）。这两项只进入事件载荷与内存回执；SQLite 表仍以
	// message_json 为权威内容，因此不需要 schema 变更。
	OK              *bool  `json:"ok,omitempty"`
	FailureCategory string `json:"failure_category,omitempty"`

	// ArgsJSON is the tool call arguments (JSON-encoded parameter object).
	// It is emitted only in the tool_receipt_recorded / tool_receipt_replayed
	// event payload so the trajectory can reconstruct call arguments after
	// recovery. It is NOT persisted to the SQLite receipt table (which keeps
	// message_json as the authoritative column and therefore needs no schema
	// migration).
	ArgsJSON json.RawMessage `json:"args_json,omitempty"`
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
	SessionID     string        `json:"session_id"`
	Status        SessionStatus `json:"status"`
	CurrentTurnID string        `json:"current_turn_id,omitempty"`
	// SuspendedTurnID 记录本会话处于**挂起态**（§6.12 parked turn）的托管 turn：
	// 父 turn 派发 durable background obligations 后收尾、账本未终态时，该 turn
	// 并未结束，只是没有正在执行的 run。此后到达的 steer（用户输入 / send_input
	// / continue）以**同一 turn_id** 起新 episode，而不是新开 turn（C3-7 /
	// AC-P2-7a）。空串表示没有挂起 turn，普通输入照常新开 turn。
	//
	// 该字段是**派生缓存**：每次使用前都会回查 durable batch 控制面上的 §6.12
	// 记录，记录被清（resume 收尾 / 放弃）后立即失效，因此不会把 turn_id 永久
	// 粘住（EC-E1 的防线之一）。
	SuspendedTurnID              string                 `json:"suspended_turn_id,omitempty"`
	CurrentCheckpointID          string                 `json:"current_checkpoint_id,omitempty"`
	CurrentRunMeta               *team.RunMeta          `json:"current_run_meta,omitempty"`
	AmbientRunMeta               *team.RunMeta          `json:"ambient_run_meta,omitempty"`
	StableToolSurface            []types.ToolDefinition `json:"stable_tool_surface,omitempty"`
	StableToolSurfaceSet         bool                   `json:"stable_tool_surface_set,omitempty"`
	StableToolSurfaceBinding     string                 `json:"stable_tool_surface_binding,omitempty"`
	StableToolSurfaceFingerprint string                 `json:"stable_tool_surface_fingerprint,omitempty"`
	FrozenTurnTools              []types.ToolDefinition `json:"frozen_turn_tools,omitempty"`
	FrozenTurnToolsSet           bool                   `json:"frozen_turn_tools_set,omitempty"`
	PendingTool                  *PendingToolInvocation `json:"pending_tool,omitempty"`
	PendingApproval              *ApprovalRequest       `json:"pending_approval,omitempty"`
	PendingQuestion              *UserQuestionRequest   `json:"pending_question,omitempty"`
	// LastRunTerminalReason records why the most recent run ended in a terminal
	// cancellation class (run_timeout/deadline/execution_context/parent_*). It is
	// set while the run tail converges the terminal state and cleared when the
	// next run starts. A pending approval that survived such a run must never
	// resume it: the deadline already fired (see
	// docs/plan/supervision-approval-resume-past-deadline-fix-plan.md).
	LastRunTerminalReason string    `json:"last_run_terminal_reason,omitempty"`
	HeadOffset            int64     `json:"head_offset"`
	ActiveJobIDs          []string  `json:"active_job_ids,omitempty"`
	UpdatedAt             time.Time `json:"updated_at"`
}

// RuntimeStateSummary is the allocation-free projection used by status polls.
// Large tool schemas and replay receipts intentionally stay out of this view.
type RuntimeStateSummary struct {
	SessionID           string
	Status              SessionStatus
	CurrentTurnID       string
	CurrentCheckpointID string
	// SuspendedTurnID 是 §6.12 挂起记录在状态里的派生标记：非空表示本会话持有一个
	// `awaiting_obligations` 的托管 turn（账本未空，只是没有正在执行的 run）。它来自
	// durable 状态，因此重启后的宿主看到与挂起进程一致的答案（C4-3 / AC-P3-3c）。
	SuspendedTurnID          string
	PendingTool              bool
	PendingToolCallID        string
	PendingToolName          string
	PendingApproval          bool
	PendingApprovalID        string
	PendingApprovalReason    string
	PendingApprovalRiskLevel string
	PendingQuestion          bool
	ActiveJobCount           int
}

// statusBusy reports whether a turn is executing (or blocked) in this process
// right now. It is the pre-C4-3 Busy() definition, kept separate because
// resume / steer admission must not be blocked by a parked turn.
func (s RuntimeStateSummary) statusBusy() bool {
	switch s.Status {
	case SessionRunning, SessionWaitingApproval, SessionWaitingInput, SessionRewinding:
		return true
	default:
		return false
	}
}

// AwaitingObligations reports whether the session owns a parked managed turn
// (design §6.7 state `awaiting_obligations`): the turn has not finished, it is
// only waiting for its durable obligations, so it has no run of its own.
func (s RuntimeStateSummary) AwaitingObligations() bool {
	return strings.TrimSpace(s.SuspendedTurnID) != ""
}

// Busy reports whether the summary represents a state that cannot accept a new
// turn. A parked managed turn counts as busy (Q10：托管 turn 期间不允许并发新
// turn)。重启正是这条断言的现场：挂起 turn 跨进程存活，忽略它的 summary 会把
// 会话报成空闲（"假空闲"），让第二个 turn 与 resume 抢跑。
func (s RuntimeStateSummary) Busy() bool {
	return s.statusBusy() || s.AwaitingObligations()
}

// AcceptsResume reports whether the session can take a resume episode right
// now (wake / steer / progress-check delivery). Resume is not a new turn: it
// reuses the parked `turn_id`（§6.15），so a parked session accepts it even
// though Busy() is true, while an executing turn still does not.
func (s RuntimeStateSummary) AcceptsResume() bool {
	return !s.statusBusy()
}

// Summary returns a small immutable projection suitable for frequent polling.
func (s *RuntimeState) Summary() RuntimeStateSummary {
	if s == nil {
		return RuntimeStateSummary{}
	}
	summary := RuntimeStateSummary{
		SessionID:           s.SessionID,
		Status:              s.Status,
		CurrentTurnID:       s.CurrentTurnID,
		CurrentCheckpointID: s.CurrentCheckpointID,
		SuspendedTurnID:     s.SuspendedTurnID,
		PendingTool:         s.PendingTool != nil,
		PendingApproval:     s.PendingApproval != nil,
		PendingQuestion:     s.PendingQuestion != nil,
		ActiveJobCount:      len(s.ActiveJobIDs),
	}
	if s.PendingTool != nil {
		summary.PendingToolCallID = s.PendingTool.ToolCallID
		summary.PendingToolName = s.PendingTool.ToolName
	}
	if s.PendingApproval != nil {
		summary.PendingApprovalID = s.PendingApproval.ID
		summary.PendingApprovalReason = s.PendingApproval.Reason
		summary.PendingApprovalRiskLevel = s.PendingApproval.RiskLevel
	}
	return summary
}

// Clone returns a defensive copy of the runtime state.
func (s *RuntimeState) Clone() *RuntimeState {
	return s.clone(true, true)
}

// CloneForInspection returns a defensive copy without large persisted payloads
// that status checks and control-flow decisions do not need.
func (s *RuntimeState) CloneForInspection() *RuntimeState {
	return s.clone(false, false)
}

func (s *RuntimeState) clone(includeToolSurfaces, includeToolResult bool) *RuntimeState {
	if s == nil {
		return nil
	}
	clone := *s
	clone.CurrentRunMeta = s.CurrentRunMeta.Clone()
	clone.AmbientRunMeta = s.AmbientRunMeta.Clone()
	clone.StableToolSurface = nil
	clone.FrozenTurnTools = nil
	if includeToolSurfaces {
		clone.StableToolSurface = cloneRuntimeToolDefinitions(s.StableToolSurface)
		if runtimeToolDefinitionsShareBacking(s.StableToolSurface, s.FrozenTurnTools) {
			clone.FrozenTurnTools = clone.StableToolSurface
		} else {
			clone.FrozenTurnTools = cloneRuntimeToolDefinitions(s.FrozenTurnTools)
		}
	} else {
		clone.StableToolSurfaceSet = false
		clone.FrozenTurnToolsSet = false
	}
	clone.PendingTool = clonePendingToolInvocation(s.PendingTool, includeToolResult)
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

func runtimeToolDefinitionsShareBacking(left, right []types.ToolDefinition) bool {
	return len(left) > 0 && len(left) == len(right) && &left[0] == &right[0]
}

func clonePendingToolInvocation(pending *PendingToolInvocation, includeResult bool) *PendingToolInvocation {
	if pending == nil {
		return nil
	}
	cloned := *pending
	cloned.ArgsJSON = append(json.RawMessage(nil), pending.ArgsJSON...)
	if len(pending.BatchToolCalls) > 0 {
		cloned.BatchToolCalls = make([]PendingToolCall, len(pending.BatchToolCalls))
		for index, call := range pending.BatchToolCalls {
			cloned.BatchToolCalls[index] = PendingToolCall{
				ToolCallID: call.ToolCallID,
				ToolType:   call.ToolType,
				ToolName:   call.ToolName,
				ArgsJSON:   append(json.RawMessage(nil), call.ArgsJSON...),
				RawInput:   call.RawInput,
			}
		}
	}
	cloned.ResultMessageJSON = nil
	if includeResult {
		cloned.ResultMessageJSON = append(json.RawMessage(nil), pending.ResultMessageJSON...)
	}
	return &cloned
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
			Parameters:  cloneRuntimeToolParameters(tool.Parameters),
			Metadata:    cloneRuntimeInterfaceMap(tool.Metadata),
		}
	}
	return cloned
}

func cloneRuntimeMessages(input []types.Message) []types.Message {
	if len(input) == 0 {
		return nil
	}
	cloned := make([]types.Message, len(input))
	for index, message := range input {
		item := types.Message{
			Role:       message.Role,
			Content:    message.Content,
			ToolCallID: message.ToolCallID,
		}
		if len(message.ContentParts) > 0 {
			item.ContentParts = append([]types.ContentPart(nil), message.ContentParts...)
		}
		if len(message.ToolCalls) > 0 {
			item.ToolCalls = append([]types.ToolCall(nil), message.ToolCalls...)
		}
		if message.Metadata != nil {
			item.Metadata = message.Metadata.Clone()
		}
		cloned[index] = item
	}
	return cloned
}

func cloneRuntimeToolParameters(input map[string]interface{}) map[string]interface{} {
	if input == nil {
		return nil
	}
	cloned := make(map[string]interface{}, len(input))
	for key, value := range input {
		cloned[key] = cloneRuntimeToolParameterValue(value)
	}
	normalizeRuntimeToolParameterObjectSchema(cloned)
	return cloned
}

func normalizeRuntimeToolParameterObjectSchema(schema map[string]interface{}) {
	if schema == nil {
		return
	}
	if schemaType, _ := schema["type"].(string); schemaType != "object" {
		return
	}
	if properties, exists := schema["properties"]; exists {
		if _, ok := properties.(map[string]interface{}); !ok {
			schema["properties"] = map[string]interface{}{}
		}
	}
}

func normalizeRuntimeToolDefinitionsInPlace(tools []types.ToolDefinition) {
	for index := range tools {
		normalizeRuntimeToolParameterValueInPlace(tools[index].Parameters)
	}
}

func normalizeRuntimeToolParameterValueInPlace(value interface{}) {
	switch typed := value.(type) {
	case map[string]interface{}:
		for _, item := range typed {
			normalizeRuntimeToolParameterValueInPlace(item)
		}
		normalizeRuntimeToolParameterObjectSchema(typed)
	case []interface{}:
		for _, item := range typed {
			normalizeRuntimeToolParameterValueInPlace(item)
		}
	}
}

func cloneRuntimeToolParameterValue(value interface{}) interface{} {
	switch typed := value.(type) {
	case map[string]interface{}:
		return cloneRuntimeToolParameters(typed)
	case []interface{}:
		cloned := make([]interface{}, len(typed))
		for index, item := range typed {
			cloned[index] = cloneRuntimeToolParameterValue(item)
		}
		return cloned
	default:
		return typed
	}
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
