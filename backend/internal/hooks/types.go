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
	// EventPostToolUseFailure fires when a tool call ends with a non-empty error.
	// It is additive to EventPostToolUse (both may fire for the same call).
	EventPostToolUseFailure Event = "post_tool_use_failure"
	EventSubagentStart      Event = "subagent_start"
	EventSubagentStop       Event = "subagent_stop"
	// EventStop fires when an agent is about to finish a turn/run successfully
	// (no further tool calls). DecisionBlock keeps the agent running when step
	// budget remains (Grok-style stop gate).
	EventStop Event = "stop"
	// EventStopFailure fires when a run ends unsuccessfully (limits, empty
	// terminal reply, unrecovered tool errors, missing completion requirement).
	EventStopFailure Event = "stop_failure"
	// EventPreCompact / EventPostCompact bracket context compaction.
	// PreCompact DecisionBlock skips the compaction attempt.
	EventPreCompact        Event = "pre_compact"
	EventPostCompact       Event = "post_compact"
	EventCheckpointCreated  Event = "checkpoint_created"
	EventRewindCompleted    Event = "rewind_completed"
	EventBacktrackCompleted Event = "backtrack_completed"
)

// IsBlockingAction reports whether a decision should halt the enclosing flow.
func IsBlockingAction(action DecisionAction) bool {
	return action == DecisionBlock
}

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
