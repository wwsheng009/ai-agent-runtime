package agent

import (
	"fmt"
	"strings"

	runtimepolicy "github.com/wwsheng009/ai-agent-runtime/internal/policy"
	"github.com/wwsheng009/ai-agent-runtime/internal/types"
)

// Unified system-reminder / ephemeral instruction channel (R3).
//
// Product intent (aligned with Grok harness learning notes):
//   - Transient policy cues use a single envelope so models and hosts can
//     recognize them without polluting the durable system prompt.
//   - Producers share kind metadata + stable event names.
//   - Persist policy is explicit: recovery turns that must survive the next
//     model call may be durable; pure advisories stay prompt-only / strip-on-persist.
//
// Envelope (model-visible text):
//
//	<system-reminder kind="...">
//	...body...
//	</system-reminder>
const (
	// Metadata keys on reminder messages / tool-result advisories.
	MetaSystemReminder        = "system_reminder"
	MetaSystemReminderKind    = "system_reminder_kind"
	MetaEphemeralInstruction  = "ephemeral_instruction"
	MetaSystemReminderDurable = "system_reminder_durable"

	// Reminder kinds (stable product surface).
	ReminderKindCompletionRequirement = "completion_requirement"
	ReminderKindStopHook              = "stop_hook"
	ReminderKindDoomLoop              = "doom_loop"
	ReminderKindDispositionReplay     = "disposition_replay"
	ReminderKindExplorationStall      = "exploration_stall"
	ReminderKindPlanMode              = "plan_mode"
	ReminderKindRuntimeAdvisory       = "runtime_advisory"

	// Runtime events for hosts/telemetry.
	EventSystemReminderInjected = "system_reminder.injected"
)

// SystemReminder is a structured ephemeral instruction for the model.
type SystemReminder struct {
	// Kind classifies the producer (completion, doom_loop, plan_mode, ...).
	Kind string
	// Body is the human/model-readable instruction (without the XML envelope).
	Body string
	// Durable when true may be written into session history (recovery continuity).
	// When false, hosts should treat it as prompt-only / strip-on-persist.
	Durable bool
	// Extra optional metadata merged into the message Metadata map.
	Extra types.Metadata
}

// NormalizeReminderKind canonicalizes empty/unknown kinds to runtime_advisory.
func NormalizeReminderKind(kind string) string {
	kind = strings.ToLower(strings.TrimSpace(kind))
	switch kind {
	case ReminderKindCompletionRequirement,
		ReminderKindStopHook,
		ReminderKindDoomLoop,
		ReminderKindDispositionReplay,
		ReminderKindExplorationStall,
		ReminderKindPlanMode,
		ReminderKindRuntimeAdvisory:
		return kind
	case "":
		return ReminderKindRuntimeAdvisory
	default:
		// Preserve custom kinds for forward compatibility, but keep them tidy.
		return kind
	}
}

// FormatSystemReminder wraps body in the canonical <system-reminder> envelope.
func FormatSystemReminder(kind, body string) string {
	kind = NormalizeReminderKind(kind)
	body = strings.TrimSpace(body)
	if body == "" {
		return ""
	}
	return fmt.Sprintf("<system-reminder kind=%q>\n%s\n</system-reminder>", kind, body)
}

// NewSystemReminderMessage builds a user-role reminder message with channel metadata.
// Role remains "user" so providers that only allow system at turn start still accept it.
func NewSystemReminderMessage(rem SystemReminder) *types.Message {
	kind := NormalizeReminderKind(rem.Kind)
	content := FormatSystemReminder(kind, rem.Body)
	if content == "" {
		return nil
	}
	msg := types.NewUserMessage(content)
	if msg.Metadata == nil {
		msg.Metadata = types.NewMetadata()
	}
	msg.Metadata[MetaSystemReminder] = true
	msg.Metadata[MetaSystemReminderKind] = kind
	msg.Metadata[MetaEphemeralInstruction] = true
	if rem.Durable {
		msg.Metadata[MetaSystemReminderDurable] = true
	} else {
		// Explicit false so hosts can strip-on-persist without kind heuristics alone.
		msg.Metadata[MetaSystemReminderDurable] = false
	}
	for key, value := range rem.Extra {
		if strings.TrimSpace(key) == "" {
			continue
		}
		msg.Metadata[key] = value
	}
	return msg
}

// IsSystemReminder reports whether a message carries the unified reminder channel.
func IsSystemReminder(msg types.Message) bool {
	if len(msg.Metadata) == 0 {
		// Content-shape fallback for older persisted reminders without metadata.
		content := strings.TrimSpace(msg.Content)
		return strings.HasPrefix(content, "<system-reminder") && strings.Contains(content, "</system-reminder>")
	}
	if truthyMetadata(msg.Metadata[MetaSystemReminder]) {
		return true
	}
	if truthyMetadata(msg.Metadata[MetaEphemeralInstruction]) {
		return true
	}
	// Content-shape fallback for older persisted reminders.
	content := strings.TrimSpace(msg.Content)
	return strings.HasPrefix(content, "<system-reminder") && strings.Contains(content, "</system-reminder>")
}

// IsSystemReminderDurable reports whether a reminder should survive session persist.
// Missing durable flag defaults to true for standalone recovery messages that predate
// explicit false metadata; pure tool-result advisories always set durable=false.
func IsSystemReminderDurable(msg types.Message) bool {
	if !IsSystemReminder(msg) {
		return false
	}
	if len(msg.Metadata) == 0 {
		// Content-shape-only legacy: treat as durable recovery-style reminder.
		return true
	}
	if raw, ok := msg.Metadata[MetaSystemReminderDurable]; ok {
		return truthyMetadata(raw)
	}
	// Tool-result pure advisories are tagged ephemeral + semantic_repeat_advisory
	// without durable=true; strip them on persist.
	if truthyMetadata(msg.Metadata["semantic_repeat_advisory"]) {
		return false
	}
	if IsPureAdvisoryReminderKind(ReminderKindOf(msg)) && msg.Role == "tool" {
		return false
	}
	return true
}

// ReminderKindOf returns the kind metadata when present.
func ReminderKindOf(msg types.Message) string {
	if len(msg.Metadata) == 0 {
		return ""
	}
	if kind, ok := msg.Metadata[MetaSystemReminderKind].(string); ok {
		return NormalizeReminderKind(kind)
	}
	return ""
}

// IsPureAdvisoryReminderKind is true for non-recovery, prompt-only advisories.
// Completion and stop-hook recovery messages stay durable by default. Plan mode
// is also prompt-only, but is classified separately because its lifecycle is
// driven by durable plan state and the live permission engine.
func IsPureAdvisoryReminderKind(kind string) bool {
	switch NormalizeReminderKind(kind) {
	case ReminderKindDoomLoop, ReminderKindDispositionReplay, ReminderKindExplorationStall, ReminderKindRuntimeAdvisory:
		return true
	default:
		return false
	}
}

// SystemReminderEventPayload builds the product event payload for injection.
func SystemReminderEventPayload(traceID string, step int, rem SystemReminder) map[string]interface{} {
	return map[string]interface{}{
		"trace_id": traceID,
		"step":     step,
		"kind":     NormalizeReminderKind(rem.Kind),
		"durable":  rem.Durable,
		"body":     strings.TrimSpace(rem.Body),
	}
}

// PlanModeReminderBody is the standard plan-mode ephemeral instruction.
func PlanModeReminderBody(planPath string) string {
	path := strings.TrimSpace(planPath)
	lines := []string{
		"You are in plan mode.",
		"Prefer read-only investigation and write only to the allowed plan path.",
		"Do not implement product code changes until the plan is approved and plan mode exits.",
		"When the plan is ready, summarize it for review and wait for approve / request_changes / quit.",
	}
	if path != "" {
		lines = append(lines, "Plan file path: "+path)
	}
	return strings.Join(lines, " ")
}

// planModeSystemReminder returns a one-shot plan-mode reminder when the
// permission engine is currently in mode=plan. Nil when not in plan mode or
// when the current prompt already carries a plan_mode reminder.
func (loop *ReActLoop) planModeSystemReminder(history []types.Message) *types.Message {
	if loop == nil || loop.agent == nil {
		return nil
	}
	engine := loop.agent.GetPermissionEngine()
	if engine == nil || engine.Mode != runtimepolicy.ModePlan {
		return nil
	}
	if historyHasReminderKind(history, ReminderKindPlanMode) {
		return nil
	}
	planPath := ""
	if len(engine.PlanWriteAllowPaths) > 0 {
		planPath = strings.TrimSpace(engine.PlanWriteAllowPaths[0])
	}
	return NewSystemReminderMessage(SystemReminder{
		Kind: ReminderKindPlanMode,
		Body: PlanModeReminderBody(planPath),
		// Plan state and permission mode are durable already. Keeping this model
		// instruction in chat history would make it survive approve/quit and
		// incorrectly constrain later implementation turns.
		Durable: false,
		Extra: types.Metadata{
			"plan_mode_reminder": true,
			"permission_mode":    string(runtimepolicy.ModePlan),
		},
	})
}

// stripPlanModeSystemReminders removes prompt instructions left by an earlier
// plan-mode turn. A fresh reminder is injected from the live permission engine
// when plan mode is still active; otherwise the old instruction must not reach
// the model after approve/quit. The content fallback also cleans legacy
// reminders persisted before explicit reminder metadata was introduced.
func stripPlanModeSystemReminders(history []types.Message) []types.Message {
	if len(history) == 0 {
		return history
	}
	filtered := make([]types.Message, 0, len(history))
	for _, msg := range history {
		if IsSystemReminder(msg) &&
			(ReminderKindOf(msg) == ReminderKindPlanMode || strings.Contains(msg.Content, `kind="plan_mode"`)) {
			continue
		}
		filtered = append(filtered, msg)
	}
	return filtered
}

// historyHasReminderKind reports whether any message is a system-reminder of kind.
func historyHasReminderKind(history []types.Message, kind string) bool {
	kind = NormalizeReminderKind(kind)
	for _, msg := range history {
		if !IsSystemReminder(msg) {
			continue
		}
		if ReminderKindOf(msg) == kind {
			return true
		}
		// Content fallback for plan mode body without metadata.
		if kind == ReminderKindPlanMode && strings.Contains(msg.Content, `kind="plan_mode"`) {
			return true
		}
	}
	return false
}

// StripTrailingSystemReminderEnvelope removes a trailing <system-reminder> block
// from tool-result content while preserving the primary tool output.
func StripTrailingSystemReminderEnvelope(content string) string {
	content = strings.TrimRight(content, " \t\r\n")
	const closeTag = "</system-reminder>"
	if !strings.HasSuffix(content, closeTag) {
		return content
	}
	openIdx := strings.LastIndex(content, "<system-reminder")
	if openIdx < 0 {
		return content
	}
	// Only strip when the reminder is a trailing appendix (optionally after blank lines).
	return strings.TrimRight(content[:openIdx], " \t\r\n")
}

// StripEphemeralAdvisoryFromPayload returns a durable copy of a tool payload
// without pure system-reminder advisory text/metadata. Prompt builders keep the
// original payload so the next model call still sees the advisory.
func StripEphemeralAdvisoryFromPayload(payload ToolResultPayload) ToolResultPayload {
	out := ToolResultPayload{
		ToolCallID: payload.ToolCallID,
		Content:    payload.Content,
	}
	if len(payload.Metadata) > 0 {
		out.Metadata = payload.Metadata.Clone()
	}
	if out.Metadata == nil {
		return out
	}
	isAdvisory := truthyMetadata(out.Metadata["semantic_repeat_advisory"]) ||
		(truthyMetadata(out.Metadata[MetaSystemReminder]) && IsPureAdvisoryReminderKind(fmt.Sprint(out.Metadata[MetaSystemReminderKind])))
	if !isAdvisory {
		// Explicit durable=false without pure-advisory kind still strips envelope.
		if raw, ok := out.Metadata[MetaSystemReminderDurable]; ok && !truthyMetadata(raw) {
			isAdvisory = truthyMetadata(out.Metadata[MetaSystemReminder])
		}
	}
	if !isAdvisory {
		return out
	}
	out.Content = StripTrailingSystemReminderEnvelope(out.Content)
	delete(out.Metadata, "semantic_repeat_advisory")
	delete(out.Metadata, MetaSystemReminder)
	delete(out.Metadata, MetaSystemReminderKind)
	delete(out.Metadata, MetaEphemeralInstruction)
	delete(out.Metadata, MetaSystemReminderDurable)
	if len(out.Metadata) == 0 {
		out.Metadata = nil
	}
	return out
}

// DurableToolResultPayloads clones payloads with pure advisories stripped for
// durable session history. Prompt path should keep the unstripped payloads.
func DurableToolResultPayloads(payloads []ToolResultPayload) []ToolResultPayload {
	if len(payloads) == 0 {
		return nil
	}
	out := make([]ToolResultPayload, len(payloads))
	for i, payload := range payloads {
		out[i] = StripEphemeralAdvisoryFromPayload(payload)
	}
	return out
}

// DurableMessagesForPersist filters/strips non-durable system reminders before
// writing session history. Standalone Durable=false user reminders are dropped;
// tool messages keep their primary content with trailing advisory envelopes removed.
func DurableMessagesForPersist(messages []types.Message) []types.Message {
	if len(messages) == 0 {
		return nil
	}
	out := make([]types.Message, 0, len(messages))
	for _, msg := range messages {
		if IsSystemReminder(msg) && msg.Role != "tool" && !IsSystemReminderDurable(msg) {
			continue
		}
		cloned := *msg.Clone()
		if cloned.Role == "tool" && IsSystemReminder(cloned) && !IsSystemReminderDurable(cloned) {
			cloned.Content = StripTrailingSystemReminderEnvelope(cloned.Content)
			if cloned.Metadata != nil {
				delete(cloned.Metadata, "semantic_repeat_advisory")
				delete(cloned.Metadata, MetaSystemReminder)
				delete(cloned.Metadata, MetaSystemReminderKind)
				delete(cloned.Metadata, MetaEphemeralInstruction)
				delete(cloned.Metadata, MetaSystemReminderDurable)
				if len(cloned.Metadata) == 0 {
					cloned.Metadata = nil
				}
			}
		}
		out = append(out, cloned)
	}
	return out
}

func truthyMetadata(value interface{}) bool {
	switch v := value.(type) {
	case bool:
		return v
	case string:
		switch strings.ToLower(strings.TrimSpace(v)) {
		case "1", "true", "yes", "on":
			return true
		}
	case int:
		return v != 0
	case int64:
		return v != 0
	case float64:
		return v != 0
	}
	return false
}
