package hooks

import (
	"encoding/json"
	"fmt"
	"strings"
)

// Codex-compatible hook output parsing.
//
// Codex accepts three output shapes from command hooks:
//   - empty stdout: no effect;
//   - plain text stdout: additional context for the model (context-capable
//     events only);
//   - a JSON object using the shared universal fields (continue/stopReason/
//     suppressOutput/systemMessage) plus event-specific fields
//     (decision/reason/hookSpecificOutput).
//
// Exit code 2 is a blocking signal for events that support blocking, with the
// reason read from stderr. Other non-zero exits are failures; by default the
// runtime fails open, matching Codex's non-blocking hook failure semantics.

// CodexOutput is the normalized result of one handler run.
type CodexOutput struct {
	Continue       bool
	StopReason     string
	SuppressOutput bool
	SystemMessage  string

	// DecisionValue is the legacy block decision ("block" or "").
	DecisionValue string
	Reason        string

	// PermissionDecisionValue is allow|deny|ask from PreToolUse.
	PermissionDecisionValue string
	PermissionReason        string
	UpdatedInput            map[string]interface{}

	// AdditionalContext is model-visible context from the hook.
	AdditionalContext string

	// InvalidReason marks a malformed/unsupported output. The runtime treats
	// invalid control output as non-blocking and records a warning.
	InvalidReason string
	// LooksLikeJSON reports whether stdout started like a JSON value. It lets
	// callers distinguish "plain text context" from "broken JSON".
	LooksLikeJSON bool
}

type codexUniversalOutput struct {
	Continue       *bool  `json:"continue"`
	StopReason     string `json:"stopReason"`
	SuppressOutput bool   `json:"suppressOutput"`
	SystemMessage  string `json:"systemMessage"`
}

type codexHookSpecificOutput struct {
	HookEventName            string                 `json:"hookEventName"`
	AdditionalContext        string                 `json:"additionalContext"`
	PermissionDecision       string                 `json:"permissionDecision"`
	PermissionDecisionReason string                 `json:"permissionDecisionReason"`
	UpdatedInput             map[string]interface{} `json:"updatedInput"`
	// PermissionRequest uses a nested decision object:
	//   {"behavior":"allow|deny","message":"...","interrupt":false}
	Decision *codexPermissionDecision `json:"decision"`
}

type codexPermissionDecision struct {
	Behavior           string `json:"behavior"`
	Message            string `json:"message"`
	Interrupt          bool   `json:"interrupt"`
	UpdatedInput       any    `json:"updatedInput"`
	UpdatedPermissions any    `json:"updatedPermissions"`
}

type codexRawOutput struct {
	codexUniversalOutput
	Decision           string                   `json:"decision"`
	Reason             string                   `json:"reason"`
	HookSpecificOutput *codexHookSpecificOutput `json:"hookSpecificOutput"`
	// Native runtime fields. Existing runtime.yaml hooks emit these; keeping
	// them here preserves backward compatibility while the Codex fields are
	// evaluated with higher priority when both are present.
	Action         string            `json:"action"`
	Message        string            `json:"message"`
	PatchedPayload json.RawMessage   `json:"patched_payload"`
	ExtraContext   map[string]string `json:"extra_context"`
}

// ParseCodexOutput parses stdout/stderr/exit code for one event.
func ParseCodexOutput(event Event, stdout, stderr string, exitCode int) CodexOutput {
	out := CodexOutput{
		Continue:      true,
		LooksLikeJSON: looksLikeJSON(stdout),
	}
	if exitCode == 2 && eventSupportsBlocking(event) {
		reason := strings.TrimSpace(stderr)
		if reason == "" {
			out.InvalidReason = fmt.Sprintf("%s hook exited with code 2 but did not write a blocking reason to stderr", eventLabel(event))
			return out
		}
		out.DecisionValue = "block"
		out.Reason = reason
		return out
	}
	if exitCode != 0 {
		out.InvalidReason = fmt.Sprintf("hook exited with code %d", exitCode)
		return out
	}

	trimmed := strings.TrimSpace(stdout)
	if trimmed == "" {
		return out
	}
	if !looksLikeJSON(trimmed) {
		if eventSupportsAdditionalContext(event) {
			out.AdditionalContext = trimmed
		}
		return out
	}

	var raw codexRawOutput
	if err := json.Unmarshal([]byte(trimmed), &raw); err != nil {
		out.InvalidReason = fmt.Sprintf("%s hook returned invalid JSON output", eventLabel(event))
		return out
	}
	// Reject arrays/scalars: Codex requires a JSON object.
	var probe map[string]json.RawMessage
	if err := json.Unmarshal([]byte(trimmed), &probe); err != nil {
		out.InvalidReason = fmt.Sprintf("%s hook returned invalid JSON output", eventLabel(event))
		return out
	}

	if raw.Continue != nil {
		out.Continue = *raw.Continue
	}
	out.StopReason = strings.TrimSpace(raw.StopReason)
	out.SuppressOutput = raw.SuppressOutput
	out.SystemMessage = strings.TrimSpace(raw.SystemMessage)
	out.DecisionValue = strings.ToLower(strings.TrimSpace(raw.Decision))
	out.Reason = strings.TrimSpace(raw.Reason)
	// Native action mapping. Codex-specific decision/permission fields take
	// precedence if both are present.
	nativeAction := strings.ToLower(strings.TrimSpace(raw.Action))
	if out.DecisionValue == "" && nativeAction == string(DecisionBlock) {
		out.DecisionValue = "block"
		if out.Reason == "" {
			out.Reason = strings.TrimSpace(raw.Message)
		}
	}
	if out.SystemMessage == "" && strings.TrimSpace(raw.Message) != "" {
		out.SystemMessage = strings.TrimSpace(raw.Message)
	}
	if nativeAction == string(DecisionModify) && len(raw.PatchedPayload) > 0 {
		var updated map[string]interface{}
		if json.Unmarshal(raw.PatchedPayload, &updated) == nil {
			out.UpdatedInput = updated
		}
	}
	if len(raw.ExtraContext) > 0 {
		out.AdditionalContext = strings.TrimSpace(raw.ExtraContext["additional_context"])
	}

	if specific := raw.HookSpecificOutput; specific != nil {
		if specific.HookEventName != "" && !strings.EqualFold(strings.TrimSpace(specific.HookEventName), codexEventLabel(event)) {
			out.InvalidReason = fmt.Sprintf("%s hook returned hookSpecificOutput for %q", eventLabel(event), specific.HookEventName)
			return out
		}
		out.AdditionalContext = strings.TrimSpace(specific.AdditionalContext)
		out.PermissionDecisionValue = strings.ToLower(strings.TrimSpace(specific.PermissionDecision))
		out.PermissionReason = strings.TrimSpace(specific.PermissionDecisionReason)
		out.UpdatedInput = specific.UpdatedInput
		if decision := specific.Decision; decision != nil {
			behavior := strings.ToLower(strings.TrimSpace(decision.Behavior))
			if event != EventPermissionRequest {
				out.InvalidReason = fmt.Sprintf("%s hook returned unsupported nested decision", eventLabel(event))
				return out
			}
			if decision.Interrupt || decision.UpdatedInput != nil || decision.UpdatedPermissions != nil {
				out.InvalidReason = fmt.Sprintf("%s hook returned unsupported decision fields", eventLabel(event))
				return out
			}
			if behavior != "allow" && behavior != "deny" {
				out.InvalidReason = fmt.Sprintf("%s hook returned unsupported permission decision %q", eventLabel(event), behavior)
				return out
			}
			out.PermissionDecisionValue = behavior
			if behavior == "deny" {
				out.PermissionReason = strings.TrimSpace(decision.Message)
			}
		}
	}

	// Universal control fields are not supported on all events. Keep the
	// runtime conservative: ignore unsupported control instead of silently
	// widening the event's contract.
	if !out.Continue && !eventSupportsStop(event) {
		out.InvalidReason = fmt.Sprintf("%s hook returned unsupported continue:false", eventLabel(event))
		return out
	}
	if out.StopReason != "" && !eventSupportsStop(event) {
		out.InvalidReason = fmt.Sprintf("%s hook returned unsupported stopReason", eventLabel(event))
		return out
	}
	if out.SuppressOutput && event != EventPostToolUse && event != EventPostCompact && event != EventSessionEnd {
		out.InvalidReason = fmt.Sprintf("%s hook returned unsupported suppressOutput", eventLabel(event))
		return out
	}

	if out.DecisionValue == "block" {
		if !eventSupportsBlocking(event) {
			out.InvalidReason = fmt.Sprintf("%s hook returned unsupported decision:block", eventLabel(event))
			return out
		}
		if out.Reason == "" {
			out.InvalidReason = fmt.Sprintf("%s hook returned decision:block without a non-empty reason", eventLabel(event))
			return out
		}
	} else if out.DecisionValue != "" {
		out.InvalidReason = fmt.Sprintf("%s hook returned unsupported decision:%s", eventLabel(event), out.DecisionValue)
		return out
	}

	if out.PermissionDecisionValue != "" && event != EventPreToolUse {
		// PermissionRequest legitimately uses permissionDecisionValue internally
		// after mapping the nested behavior object.
		if event != EventPermissionRequest {
			out.InvalidReason = fmt.Sprintf("%s hook returned unsupported permissionDecision", eventLabel(event))
			return out
		}
	}
	// Codex only permits updatedInput together with PreToolUse allow. Without
	// allow, the output is invalid and must not be applied.
	if len(out.UpdatedInput) > 0 && event == EventPreToolUse && out.PermissionDecisionValue != "allow" {
		out.InvalidReason = "PreToolUse hook returned updatedInput without permissionDecision:allow"
		return out
	}
	// Codex treats allow without updatedInput as unsupported for PreToolUse:
	// the only meaningful allow is one that rewrites input. Keep the hook
	// non-blocking but record the invalid output so hosts can surface it.
	if event == EventPreToolUse && out.PermissionDecisionValue == "allow" && len(out.UpdatedInput) == 0 {
		out.InvalidReason = "PreToolUse hook returned permissionDecision:allow without updatedInput"
		return out
	}
	return out
}

// PermissionDecision converts a PermissionRequest hook output into the
// runtime's Decision. "allow" is intentionally not an unconditional bypass:
// it only means "the hook did not deny"; the normal static policy and hard
// deny rules still run afterwards. "deny" becomes a block.
func (out CodexOutput) ToPermissionDecision() Decision {
	if out.InvalidReason != "" {
		return Decision{Action: DecisionContinue}
	}
	if out.PermissionDecisionValue == "deny" {
		message := firstNonEmpty(out.PermissionReason, out.Reason)
		if message == "" {
			message = "hook denied the approval"
		}
		return Decision{Action: DecisionBlock, Message: message}
	}
	if out.PermissionDecisionValue == "allow" {
		// The runtime has no "hook pre-approval" channel that can bypass the
		// static policy; returning continue preserves the hard-deny/sandbox
		// guarantees. The allow reason is still exposed as a message.
		return Decision{Action: DecisionContinue, Message: out.PermissionReason}
	}
	decision := out.ToDecision()
	return decision
}

// Decision converts the Codex output to the runtime's Decision. It is only
// called for synchronous handlers; async handlers discard control effects.
func (out CodexOutput) ToDecision() Decision {
	decision := Decision{Action: DecisionContinue}
	if out.InvalidReason != "" {
		return decision
	}
	// continue:false is a terminal stop, not a block: Codex stops processing
	// instead of asking the agent for another step.
	if !out.Continue {
		decision.Action = DecisionStop
		decision.Message = firstNonEmpty(out.StopReason, out.Reason, out.SystemMessage)
		if decision.Message == "" {
			decision.Message = "hook stopped processing"
		}
		return decision
	}
	if out.PermissionDecisionValue == "deny" {
		message := firstNonEmpty(out.PermissionReason, out.Reason)
		if message == "" {
			message = "hook denied the tool call"
		}
		decision.Action = DecisionBlock
		decision.Message = message
		return decision
	}
	if out.DecisionValue == "block" {
		message := firstNonEmpty(out.Reason, out.PermissionReason, out.StopReason)
		if message == "" {
			message = "hook blocked execution"
		}
		decision.Action = DecisionBlock
		decision.Message = message
		return decision
	}
	if len(out.UpdatedInput) > 0 {
		if encoded, err := json.Marshal(out.UpdatedInput); err == nil {
			// Codex only permits updatedInput together with allow. Keep the
			// allow semantics (no bypass) but hand the patched args to the
			// runtime so the tool sees the hook's replacement.
			decision.Action = DecisionModify
			decision.PatchedPayload = encoded
		}
	}
	if out.AdditionalContext != "" {
		decision.ExtraContext = map[string]string{"additional_context": out.AdditionalContext}
		if decision.Action == DecisionContinue {
			decision.Action = DecisionEnrich
		}
	}
	if out.SystemMessage != "" && decision.Message == "" {
		decision.Message = out.SystemMessage
	}
	return decision
}

func looksLikeJSON(stdout string) bool {
	trimmed := strings.TrimSpace(stdout)
	return strings.HasPrefix(trimmed, "{") || strings.HasPrefix(trimmed, "[")
}

func eventSupportsAdditionalContext(event Event) bool {
	switch event {
	case EventPreToolUse, EventPostToolUse, EventSessionStart,
		EventUserPromptSubmit, EventSubagentStart:
		return true
	default:
		return false
	}
}

func eventSupportsBlocking(event Event) bool {
	switch event {
	case EventPreToolUse, EventPostToolUse, EventUserPromptSubmit,
		EventStop, EventSubagentStop, EventPermissionRequest,
		// Native runtime extension: pre_compact block skips compaction.
		EventPreCompact:
		return true
	default:
		return false
	}
}

func eventSupportsStop(event Event) bool {
	switch event {
	case EventSessionStart, EventUserPromptSubmit, EventStop, EventSubagentStop,
		EventPostToolUse:
		return true
	default:
		return false
	}
}

func eventLabel(event Event) string {
	switch event {
	case EventPreToolUse:
		return "PreToolUse"
	case EventPermissionRequest:
		return "PermissionRequest"
	case EventPostToolUse:
		return "PostToolUse"
	case EventPreCompact:
		return "PreCompact"
	case EventPostCompact:
		return "PostCompact"
	case EventSessionStart:
		return "SessionStart"
	case EventSessionEnd:
		return "SessionEnd"
	case EventUserPromptSubmit:
		return "UserPromptSubmit"
	case EventSubagentStart:
		return "SubagentStart"
	case EventSubagentStop:
		return "SubagentStop"
	case EventStop:
		return "Stop"
	case EventInterrupt:
		return "Interrupt"
	default:
		return string(event)
	}
}

func codexEventLabel(event Event) string {
	return eventLabel(event)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
