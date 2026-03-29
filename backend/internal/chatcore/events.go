package chatcore

// EventType is the normalized event stream contract for chat entrypoints.
type EventType string

const (
	EventPlanning EventType = "planning"
	EventSubagent EventType = "subagent"
	EventTool     EventType = "tool"
	EventResult   EventType = "result"
	EventWarning  EventType = "warning"
)

// ChatEvent is the normalized event payload emitted by shared chat-core helpers.
type ChatEvent struct {
	Type       EventType              `json:"type"`
	Stage      string                 `json:"stage,omitempty"`
	Content    string                 `json:"content,omitempty"`
	ToolName   string                 `json:"tool_name,omitempty"`
	ToolCallID string                 `json:"tool_call_id,omitempty"`
	Arguments  map[string]interface{} `json:"arguments,omitempty"`
	Output     string                 `json:"output,omitempty"`
	Error      string                 `json:"error,omitempty"`
	Success    bool                   `json:"success,omitempty"`
	Metadata   map[string]interface{} `json:"metadata,omitempty"`
}
