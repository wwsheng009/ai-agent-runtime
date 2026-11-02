package types

import "strings"

// Metadata keys for request-scoped (ephemeral) context layers.
//
// The context manager rebuilds these layers for every provider request; they
// describe the current request, not the conversation. They may travel with the
// request but must never end up in the durable transcript.
const (
	// MetadataKeyContextStage names the injected context layer ("fact_ledger",
	// "recall", "workspace", ...).
	MetadataKeyContextStage = "context_stage"
	// MetadataKeyContextSnapshot marks a layer generated for the current turn.
	MetadataKeyContextSnapshot = "context_snapshot"
	// MetadataKeyTransientPrompt marks a prompt that only exists for one run
	// (goal/completion audit re-prompts). Callers must not persist it as a user
	// turn.
	MetadataKeyTransientPrompt = "transient_prompt"
)

// Host-authored control-plane prompts ride into the transcript as ordinary user
// turns. They are scaffolding, not user assertions: a supervision digest line
// such as “child session failed” states runtime bookkeeping, and a durable
// consumer that files it as a user-claimed fact is repeating the exact mistake
// the fact ledger had to be rebuilt to avoid.
const (
	// SupervisionDigestPromptHeader opens the progress digest the host injects
	// when waking a parent turn (supervision's digest brief renderer).
	SupervisionDigestPromptHeader = "[子任务与后台作业进展]"
	// SupervisionPromptTag opens every host-submitted supervision turn, both the
	// fixed auto-wake prompt and the resume prompt.
	SupervisionPromptTag = "[supervision]"
)

var controlPlanePromptMarkers = []string{SupervisionDigestPromptHeader, SupervisionPromptTag}

// IsControlPlanePromptText reports whether a message body begins with a
// host-authored control-plane tag rather than something a user asserted.
func IsControlPlanePromptText(text string) bool {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return false
	}
	for _, marker := range controlPlanePromptMarkers {
		if strings.HasPrefix(trimmed, marker) {
			return true
		}
	}
	return false
}

// requestScopedContextStages lists the injected layers that are rebuilt per
// request. Values mirror contextmgr's stage names.
var requestScopedContextStages = map[string]bool{
	"active_execution": true,
	"active_goal":      true,
	"correction":       true,
	"environment":      true,
	"fact_ledger":      true,
	"ledger":           true,
	"observation":      true,
	"profile":          true,
	"project_memory":   true,
	"recall":           true,
	"request_prefix":   true,
	"runtime_notice":   true,
	"team":             true,
	"todo_state":       true,
	"warm_memory":      true,
	"workspace":        true,
}

// IsRequestScopedContextMessage reports whether a message exists only for the
// current provider request. The durable write path drops those messages so a
// long run cannot accumulate one extra copy of every context layer per
// checkpoint (each rebuild mints fresh message ids, so identity alignment alone
// cannot collapse them).
func IsRequestScopedContextMessage(msg Message) bool {
	if msg.Metadata == nil {
		return false
	}
	if msg.Metadata.GetBool(MetadataKeyContextSnapshot, false) {
		return true
	}
	if msg.Metadata.GetBool(MetadataKeyTransientPrompt, false) {
		return true
	}
	stage := strings.ToLower(strings.TrimSpace(msg.Metadata.GetString(MetadataKeyContextStage, "")))
	if stage == "" {
		return false
	}
	return requestScopedContextStages[stage]
}

// MarkRequestScopedContext stamps a message as request-scoped context so the
// durable write path can drop it even when the prefix-based strip cannot match
// it (for example after compaction rewrote the head of the transcript).
func MarkRequestScopedContext(msg *Message, stage string) {
	if msg == nil {
		return
	}
	if msg.Metadata == nil {
		msg.Metadata = NewMetadata()
	}
	if trimmed := strings.TrimSpace(stage); trimmed != "" {
		msg.Metadata.Set(MetadataKeyContextStage, trimmed)
	}
	msg.Metadata.Set(MetadataKeyContextSnapshot, true)
}
