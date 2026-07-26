package toolkit

import (
	"context"
	"strings"
)

// ListToolsContext carries per-turn listing signals for ShouldList predicates.
// Fields are intentionally small and host-filled; tools ignore unknown/zero values.
type ListToolsContext struct {
	// PermissionMode is the active permission mode (default|accept_edits|plan|bypass_permissions).
	PermissionMode string
	// ReadOnly is true when the agent/tool policy is read-only.
	ReadOnly bool
	// PlanMode is true when plan mode is active.
	PlanMode bool
	// TeamActive is true when an active team run is bound into the request.
	TeamActive bool
	// CatalogSize is the pre-filter catalog size when known (optional).
	CatalogSize int
	// Extensions holds optional host-defined values without expanding this struct.
	Extensions map[string]interface{}
}

// ListableTool is an optional Tool capability. Tools that do not implement it
// are treated as always listable (ShouldList == true).
type ListableTool interface {
	ShouldList(ctx ListToolsContext) bool
}

// Metadata keys that influence listing when tools do not implement ListableTool.
// These are also honoured for ToolDefinition / MCP catalog metadata.
const (
	// MetaShouldList: bool. false hides the tool from model-facing lists.
	MetaShouldList = "should_list"
	// MetaListWhen: string. "always" (default), "team_active", "never".
	MetaListWhen = "list_when"
	// MetaCoreTool: bool. true marks a core tool that stays listed under search projection.
	MetaCoreTool = "core_tool"
	// MetaDeferLoading: bool. true marks tools that prefer deferred/search loading.
	MetaDeferLoading = "defer_loading"
)

// ListWhen values for MetaListWhen.
const (
	ListWhenAlways     = "always"
	ListWhenTeamActive = "team_active"
	ListWhenNever      = "never"
)

// ShouldList reports whether tool should appear in a model-facing catalog.
// Priority: ListableTool.ShouldList > metadata should_list/list_when > default true.
func ShouldList(tool Tool, listCtx ListToolsContext) bool {
	if tool == nil {
		return false
	}
	if listable, ok := tool.(ListableTool); ok {
		return listable.ShouldList(listCtx)
	}
	if provider, ok := tool.(ToolDefinitionMetadataProvider); ok {
		return ShouldListMetadata(provider.DefinitionMetadata(), listCtx)
	}
	return true
}

// ShouldListMetadata applies listing policy from definition/catalog metadata.
func ShouldListMetadata(metadata map[string]interface{}, listCtx ListToolsContext) bool {
	if len(metadata) == 0 {
		return true
	}
	if explicit, ok := metadataBool(metadata, MetaShouldList); ok {
		return explicit
	}
	switch normalizeListWhen(metadataString(metadata, MetaListWhen)) {
	case ListWhenNever:
		return false
	case ListWhenTeamActive:
		return listCtx.TeamActive
	default:
		return true
	}
}

// IsCoreTool reports whether metadata marks a tool as always-listed core surface.
func IsCoreTool(metadata map[string]interface{}, name string) bool {
	if explicit, ok := metadataBool(metadata, MetaCoreTool); ok {
		return explicit
	}
	return isBuiltinCoreToolName(name)
}

// ListToolsContextFromContext builds ListToolsContext from a request context
// and optional host overrides. Host fields take precedence over context values.
func ListToolsContextFromContext(ctx context.Context, host ListToolsContext) ListToolsContext {
	out := host
	if ctx == nil {
		return out
	}
	if stored, ok := ctx.Value(listToolsContextKey{}).(ListToolsContext); ok {
		if strings.TrimSpace(out.PermissionMode) == "" {
			out.PermissionMode = stored.PermissionMode
		}
		if !out.ReadOnly {
			out.ReadOnly = stored.ReadOnly
		}
		if !out.PlanMode {
			out.PlanMode = stored.PlanMode
		}
		if !out.TeamActive {
			out.TeamActive = stored.TeamActive
		}
		if out.CatalogSize == 0 {
			out.CatalogSize = stored.CatalogSize
		}
		if len(out.Extensions) == 0 && len(stored.Extensions) > 0 {
			out.Extensions = stored.Extensions
		}
	}
	return out
}

// WithListToolsContext stores ListToolsContext on a request context.
func WithListToolsContext(ctx context.Context, listCtx ListToolsContext) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, listToolsContextKey{}, listCtx)
}

type listToolsContextKey struct{}

func normalizeListWhen(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case ListWhenNever, "false", "off", "hidden":
		return ListWhenNever
	case ListWhenTeamActive, "team", "active_team", "requires_active_team_run":
		return ListWhenTeamActive
	case ListWhenAlways, "", "true", "on":
		return ListWhenAlways
	default:
		return strings.ToLower(strings.TrimSpace(value))
	}
}

func metadataBool(metadata map[string]interface{}, key string) (bool, bool) {
	if len(metadata) == 0 {
		return false, false
	}
	raw, ok := metadata[key]
	if !ok {
		return false, false
	}
	switch typed := raw.(type) {
	case bool:
		return typed, true
	case string:
		switch strings.ToLower(strings.TrimSpace(typed)) {
		case "true", "1", "yes", "on":
			return true, true
		case "false", "0", "no", "off":
			return false, true
		}
	case float64:
		return typed != 0, true
	case int:
		return typed != 0, true
	}
	return false, false
}

func metadataString(metadata map[string]interface{}, key string) string {
	if len(metadata) == 0 {
		return ""
	}
	raw, ok := metadata[key]
	if !ok || raw == nil {
		return ""
	}
	if text, ok := raw.(string); ok {
		return text
	}
	return ""
}

// Builtin core tools that stay listed when the catalog is projected through search.
func isBuiltinCoreToolName(name string) bool {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "shell", "bash", "execute_shell_command",
		"view", "grep", "glob", "ls",
		"write", "edit", "multiedit", "apply_patch", "append_write",
		"todos", "web_search", "fetch", "download",
		"ask_user_question", "enter_plan_mode", "exit_plan_mode",
		"background_task", "task_output",
		"spawn_agent", "spawn_team", "spawn_subagents",
		"wait_agent", "wait_team", "list_agents",
		"send_message", "send_input", "followup_task",
		"read_agent_events", "close_agent", "resume_agent",
		"apply_agent_worktree", "discard_agent_worktree",
		"resolve_agent_approval",
		"get_goal", "read_goal", "update_goal",
		ToolSearchName:
		return true
	default:
		return false
	}
}
