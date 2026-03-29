package hooks

import "encoding/json"

// Event represents a hook event name.
type Event string

const (
	EventSessionStart      Event = "session_start"
	EventSessionEnd        Event = "session_end"
	EventUserPromptSubmit  Event = "user_prompt_submit"
	EventPreToolUse        Event = "pre_tool_use"
	EventPermissionRequest Event = "permission_request"
	EventPostToolUse       Event = "post_tool_use"
	EventSubagentStart     Event = "subagent_start"
	EventSubagentStop      Event = "subagent_stop"
	EventCheckpointCreated Event = "checkpoint_created"
	EventRewindCompleted   Event = "rewind_completed"
)

// DecisionAction describes the hook decision outcome.
type DecisionAction string

const (
	DecisionContinue DecisionAction = "continue"
	DecisionBlock    DecisionAction = "block"
	DecisionModify   DecisionAction = "modify"
	DecisionNotify   DecisionAction = "notify"
	DecisionEnrich   DecisionAction = "enrich"
)

// Decision captures a hook decision.
type Decision struct {
	Action         DecisionAction    `json:"action"`
	Message        string            `json:"message,omitempty"`
	PatchedPayload json.RawMessage   `json:"patched_payload,omitempty"`
	ExtraContext   map[string]string `json:"extra_context,omitempty"`
}
