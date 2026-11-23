package tools

import (
	runtimecfg "github.com/wwsheng009/ai-agent-runtime/internal/config"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolkit"
)

func NewMemorySearchTool(cfg *runtimecfg.RuntimeConfig) toolkit.Tool {
	return newMemoryTool(cfg, "search", "memory_search",
		"Search project memory by query, kinds, tags, or statuses. Read-only and bounded.",
		memorySchema(map[string]interface{}{
			"query":       memoryStringProp("Search query."),
			"kind":        memoryStringProp("Optional memory kind filter."),
			"tag":         memoryStringProp("Optional tag filter."),
			"kinds":       memoryStringArrayProp("Optional memory kind filters (all values are OR)."),
			"tags":        memoryStringArrayProp("Optional tag filters (all tags must match)."),
			"source_kind": memoryStringProp("Optional provenance filter, such as manual or session_extract."),
			"status":      memoryEnumProp("Optional status filter (agent surface).", "active", "candidate"),
			"statuses":    memoryStringArrayProp("Optional status filters (agent surface: active/candidate)."),
			"limit":       memoryIntProp("Maximum results (default 20, max 100).", 1, 100),
		}), false)
}

func NewMemoryReadTool(cfg *runtimecfg.RuntimeConfig) toolkit.Tool {
	return newMemoryTool(cfg, "read", "memory_read",
		"Read one project memory record by id, including usage evidence. Records a read usage event.",
		memorySchema(map[string]interface{}{
			"id": memoryStringProp("Memory id, for example mem_..."),
		}, "id"), false)
}

func NewMemorySessionContextTool(cfg *runtimecfg.RuntimeConfig) toolkit.Tool {
	return newMemoryTool(cfg, "session_context", "memory_session_context",
		"List bounded project-memory evidence associated with one session. Read-only.",
		memorySchema(map[string]interface{}{
			"session_id": memoryStringProp("Session id."),
			"limit":      memoryIntProp("Maximum results (default 20, max 100).", 1, 100),
		}, "session_id"), false)
}

func NewMemoryRecentTool(cfg *runtimecfg.RuntimeConfig) toolkit.Tool {
	return newMemoryTool(cfg, "recent", "memory_recent",
		"List recent active project memories. Diagnostic and read-only.",
		memorySchema(map[string]interface{}{
			"kind":  memoryStringProp("Optional memory kind filter."),
			"limit": memoryIntProp("Maximum results (default 20, max 100).", 1, 100),
		}), false)
}

func NewMemoryStatsTool(cfg *runtimecfg.RuntimeConfig) toolkit.Tool {
	return newMemoryTool(cfg, "stats", "memory_stats",
		"Return project-memory counts, freshness, summaries, and job status. Read-only.",
		memorySchema(map[string]interface{}{}), false)
}

func NewMemoryCoreGetTool(cfg *runtimecfg.RuntimeConfig) toolkit.Tool {
	return newMemoryTool(cfg, "core_get", "memory_core_get",
		"Read the active, reviewed project Core Memory. Returns no candidate or archived content.",
		memorySchema(map[string]interface{}{}), false)
}

func NewMemoryAddTool(cfg *runtimecfg.RuntimeConfig) toolkit.Tool {
	return newMemoryTool(cfg, "add", "memory_add",
		"Add an explicit manual project memory. Use only when the user explicitly asks to remember something.",
		memorySchema(map[string]interface{}{
			"text":         memoryStringProp("Memory text to persist."),
			"title":        memoryStringProp("Optional short memory title."),
			"summary":      memoryStringProp("Optional bounded memory summary."),
			"canonical_id": memoryStringProp("Optional stable canonical identity shared by revisions."),
			"kind":         memoryStringProp("Optional kind: preference/decision/fact/workflow/pitfall/reference/open_question."),
			"tags":         memoryStringArrayProp("Optional tags."),
			"pinned":       memoryBoolProp("Pin this memory so it is always injected first."),
		}, "text"), true)
}

func NewMemoryPinTool(cfg *runtimecfg.RuntimeConfig) toolkit.Tool {
	return newMemoryTool(cfg, "pin", "memory_pin",
		"Pin or unpin one project memory so it participates in the stable injection budget.",
		memorySchema(map[string]interface{}{
			"id":     memoryStringProp("Memory id."),
			"pinned": memoryBoolProp("Whether to pin the record; defaults to true."),
		}, "id"), true)
}

func NewMemoryCorrectTool(cfg *runtimecfg.RuntimeConfig) toolkit.Tool {
	return newMemoryTool(cfg, "correct", "memory_correct",
		"Create a correction revision for an existing memory. Use only when the user explicitly corrects it.",
		memorySchema(map[string]interface{}{
			"id":   memoryStringProp("Memory id to correct."),
			"text": memoryStringProp("Corrected memory text."),
		}, "id", "text"), true)
}

func NewMemoryForgetTool(cfg *runtimecfg.RuntimeConfig) toolkit.Tool {
	return newMemoryTool(cfg, "forget", "memory_forget",
		"Tombstone a project memory and suppress recreation. Disabled unless memory.tools.allowForget is enabled; requires confirm=true.",
		memorySchema(map[string]interface{}{
			"id":      memoryStringProp("Memory id to forget."),
			"confirm": memoryBoolProp("Must be true to confirm deletion."),
		}, "id", "confirm"), true)
}

func NewMemoryWhyTool(cfg *runtimecfg.RuntimeConfig) toolkit.Tool {
	return newMemoryTool(cfg, "why", "memory_why",
		"Explain one memory record with its provenance, timestamps, and usage events.",
		memorySchema(map[string]interface{}{
			"id": memoryStringProp("Memory id."),
		}, "id"), false)
}

func NewMemoryWhyNotTool(cfg *runtimecfg.RuntimeConfig) toolkit.Tool {
	return newMemoryTool(cfg, "why_not", "memory_why_not",
		"Explain why a query selected or dropped memory records under the current budget.",
		memorySchema(map[string]interface{}{
			"query":        memoryStringProp("Recall query."),
			"limit":        memoryIntProp("Maximum selected records (default 5, max 20).", 1, 20),
			"token_budget": memoryIntProp("Optional token budget (max 4000).", 1, 4000),
		}, "query"), false)
}

func NewMemoryHistoryTool(cfg *runtimecfg.RuntimeConfig) toolkit.Tool {
	return newMemoryTool(cfg, "history", "memory_history",
		"Read a bounded, safe revision history for one agent-visible project memory owned by the current session.",
		memorySchema(map[string]interface{}{
			"id":    memoryStringProp("Memory id."),
			"limit": memoryIntProp("Maximum history revisions (default 50, max 100).", 1, 100),
		}, "id"), false)
}

func NewMemoryRollbackTool(cfg *runtimecfg.RuntimeConfig) toolkit.Tool {
	return newMemoryTool(cfg, "rollback", "memory_rollback",
		"Restore one historical revision of a non-sensitive project memory owned by the current session. Disabled unless memory.tools.allowRollback is enabled; requires confirm=true and the current expected_revision. Set revert_supersede=true only when the superseding successor is also owned by this session and non-sensitive.",
		memorySchema(map[string]interface{}{
			"id":                memoryStringProp("Memory id."),
			"target_version":    memoryIntProp("Historical version to restore.", 1, 9007199254740991),
			"expected_revision": memoryIntProp("Current revision; prevents overwriting concurrent changes.", 1, 9007199254740991),
			"reason":            memoryStringProp("Audited reason for the rollback; do not include secrets."),
			"confirm":           memoryBoolProp("Must be true to confirm the rollback."),
			"revert_supersede":  memoryBoolProp("Explicitly invalidate the current superseding successor; both records must be current-session-owned and non-sensitive."),
		}, "id", "target_version", "expected_revision", "reason", "confirm"), true)
}

func memorySchema(properties map[string]interface{}, required ...string) map[string]interface{} {
	schema := map[string]interface{}{
		"type":       "object",
		"properties": properties,
	}
	if len(required) > 0 {
		schema["required"] = required
	}
	return schema
}

func memoryStringProp(description string) map[string]interface{} {
	return map[string]interface{}{"type": "string", "description": description}
}

func memoryStringArrayProp(description string) map[string]interface{} {
	return map[string]interface{}{
		"type": "array", "description": description,
		"items": map[string]interface{}{"type": "string"},
	}
}

func memoryBoolProp(description string) map[string]interface{} {
	return map[string]interface{}{"type": "boolean", "description": description}
}

func memoryIntProp(description string, minimum, maximum int) map[string]interface{} {
	return map[string]interface{}{
		"type": "integer", "description": description, "minimum": minimum, "maximum": maximum,
	}
}

func memoryEnumProp(description string, values ...string) map[string]interface{} {
	return map[string]interface{}{"type": "string", "description": description, "enum": values}
}
