package chatcore

import "github.com/ai-gateway/ai-agent-runtime/internal/types"

// OrchestrationSummary captures the shared orchestration view for chat entrypoints.
type OrchestrationSummary struct {
	Source        string `json:"source,omitempty"`
	Success       bool   `json:"success"`
	Steps         int    `json:"steps,omitempty"`
	ToolCallCount int    `json:"tool_call_count,omitempty"`
}

// PlanningSummary captures planning/subagent metadata for a chat result.
type PlanningSummary struct {
	Attempted         bool   `json:"attempted"`
	Source            string `json:"source,omitempty"`
	StepCount         int    `json:"step_count,omitempty"`
	SubagentTaskCount int    `json:"subagent_task_count,omitempty"`
}

// ToolExecutionSummary captures one replayed tool execution in a provider loop.
type ToolExecutionSummary struct {
	ToolCallID string `json:"tool_call_id,omitempty"`
	ToolName   string `json:"tool_name,omitempty"`
	Output     string `json:"output,omitempty"`
	Error      string `json:"error,omitempty"`
	Success    bool   `json:"success"`
}

// ChatResult is the transport-neutral result contract shared by chat entrypoints.
type ChatResult struct {
	Output         string                 `json:"output,omitempty"`
	TraceID        string                 `json:"trace_id,omitempty"`
	SessionID      string                 `json:"session_id,omitempty"`
	AgentID        string                 `json:"agent_id,omitempty"`
	Observations   []types.Observation    `json:"observations,omitempty"`
	Usage          *types.TokenUsage      `json:"usage,omitempty"`
	Duration       types.Duration         `json:"duration"`
	Error          string                 `json:"error,omitempty"`
	ToolExecutions []ToolExecutionSummary `json:"tool_executions,omitempty"`
	Orchestration  *OrchestrationSummary  `json:"orchestration,omitempty"`
	Planning       *PlanningSummary       `json:"planning,omitempty"`
}

// NewChatResult creates an empty shared result.
func NewChatResult() *ChatResult {
	return &ChatResult{
		Observations:   make([]types.Observation, 0),
		ToolExecutions: make([]ToolExecutionSummary, 0),
	}
}
