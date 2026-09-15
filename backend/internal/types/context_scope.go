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
