package agent

import (
	"fmt"
	"strings"
)

// Registered built-in runtime tool names.
//
// Every name handed to a subagent allowlist must exist in the runtime tool
// registry (see internal/tools); a name that is not registered silently
// disappears from the child's tool surface. The constants below are asserted
// against the real registry by tool_vocabulary_contract_test.go.
const (
	toolNameView         = "view"
	toolNameGrep         = "grep"
	toolNameGlob         = "glob"
	toolNameLs           = "ls"
	toolNameShell        = "shell"
	toolNameWrite        = "write"
	toolNameEdit         = "edit"
	toolNameMultiEdit    = "multiedit"
	toolNameApplyPatch   = "apply_patch"
	toolNameAppendWrite  = "append_write"
	toolNameFetch        = "fetch"
	toolNameWebSearch    = "web_search"
	toolNameArtifactRead = "artifact_read"
)

// normalizeToolWhitelist trims and de-duplicates a requested tool allowlist.
//
// Names are deliberately not rewritten: a caller-provided name may belong to a
// broker/MCP tool rather than a built-in one, so silently mapping it onto
// another tool would change the requested surface. Unknown names are kept
// verbatim and are rejected later by validateChildToolAllowlist when they
// cannot be granted.
//
// nil stays nil so that "inherit the role defaults" keeps working, and an
// explicit empty slice stays empty so that the documented "disable every tool"
// contract (for example spawn_subagents with tools_whitelist: []) is preserved.
func normalizeToolWhitelist(names []string) []string {
	if names == nil {
		return nil
	}
	normalized := make([]string, 0, len(names))
	seen := make(map[string]bool, len(names))
	for _, name := range names {
		trimmed := strings.TrimSpace(name)
		if trimmed == "" || seen[trimmed] {
			continue
		}
		seen[trimmed] = true
		normalized = append(normalized, trimmed)
	}
	return normalized
}

// validateChildToolAllowlist rejects the silent allowlist blackout: a task that
// requested tools explicitly, but whose derived child policy grants none of
// them, must fail fast instead of building a child that has no tools at all.
//
// requested must hold the names as requested by the caller (before
// normalization) so that the error message can quote what was actually asked,
// and source names where they came from ("tools_whitelist" or the role
// defaults). An explicitly empty request is left alone: that is the documented
// "no tools for this child" contract.
func validateChildToolAllowlist(taskID, source string, requested []string, child *ToolExecutionPolicy) error {
	if child == nil || !child.AllowlistEnabled || len(child.AllowedTools) > 0 {
		return nil
	}
	if len(requested) == 0 {
		return nil
	}
	return fmt.Errorf(
		"subagent %q has %s [%s], but the parent session policy grants none of them, which would leave the child with an empty tool allowlist; use registered tool names (%s, %s, %s, ...) or omit tools_whitelist so the child inherits the role defaults",
		taskID,
		source,
		strings.Join(requested, ", "),
		toolNameView, toolNameGrep, toolNameShell,
	)
}

// legacyToolSuggestions maps retired built-in tool vocabulary onto the runtime
// tool that replaced it.
//
// The table only powers diagnostics: a request is never rewritten, because
// names such as read_file/write_file can equally belong to a real broker or MCP
// server. A retired name is reported only when its replacement is present in
// the agent's live tool surface, which cannot be true for an MCP-only tool.
var legacyToolSuggestions = map[string]string{
	"read_file":      toolNameView,
	"read_files":     toolNameView,
	"view_file":      toolNameView,
	"open_file":      toolNameView,
	"read_logs":      toolNameView,
	"list_directory": toolNameLs,
	"list_files":     toolNameLs,
	"list_dir":       toolNameLs,
	"ls_dir":         toolNameLs,
	"search_code":    toolNameGrep,
	"search_repo":    toolNameGrep,
	"grep_repo":      toolNameGrep,
	"find_in_files":  toolNameGrep,
	"write_file":     toolNameWrite,
	"create_file":    toolNameWrite,
	"edit_file":      toolNameEdit,
	"apply_diff":     toolNameApplyPatch,
	"run_tests":      toolNameShell,
	"run_command":    toolNameShell,
	"bash":           toolNameShell,
	"git_log":        toolNameShell,
	"git_status":     toolNameShell,
	"git_diff":       toolNameShell,
	"web_fetch":      toolNameFetch,
	"http_get":       toolNameFetch,
	"search_web":     toolNameWebSearch,
}

// agentToolSurface returns the tool names the agent can execute right now.
//
// A nil/empty surface yields nil, which means "unknown" rather than "no tools":
// callers must stay permissive, otherwise an agent whose manager has not been
// populated yet would reject every request.
func agentToolSurface(agent *Agent) map[string]bool {
	if agent == nil || agent.mcpManager == nil {
		return nil
	}
	tools := agent.mcpManager.ListTools()
	surface := make(map[string]bool, len(tools))
	for _, tool := range tools {
		if name := strings.TrimSpace(tool.Name); name != "" {
			surface[name] = true
		}
	}
	if len(surface) == 0 {
		return nil
	}
	return surface
}

// validateRequestedToolVocabulary rejects retired built-in tool names that the
// runtime can never grant.
//
// This covers the case the policy-level check cannot see: when the parent
// session has no allowlist, the requested names are copied into the child policy
// verbatim, so a stale name such as read_file produces an allowlist that matches
// no executable tool instead of an empty one.
//
// The check stays conservative - a name is rejected only when the live surface
// proves the runtime has replaced it - so a broker/MCP tool that happens to
// share a retired built-in name keeps working.
func validateRequestedToolVocabulary(taskID, source string, requested []string, surface map[string]bool) error {
	if len(requested) == 0 || len(surface) == 0 {
		return nil
	}
	stale := make([]string, 0, len(requested))
	pairs := make([]string, 0, len(requested))
	for _, name := range requested {
		trimmed := strings.TrimSpace(name)
		if trimmed == "" || surface[trimmed] {
			continue
		}
		replacement, ok := legacyToolSuggestions[strings.ToLower(trimmed)]
		if !ok || !surface[replacement] {
			continue
		}
		stale = append(stale, trimmed)
		pairs = append(pairs, fmt.Sprintf("%q -> %q", trimmed, replacement))
	}
	if len(stale) == 0 {
		return nil
	}
	return fmt.Errorf(
		"subagent %q has %s [%s], but the runtime no longer serves %s: the request can never be granted and the child would be left without a usable tool surface; use %s, or omit tools_whitelist so the child inherits the role defaults",
		taskID,
		source,
		strings.Join(requested, ", "),
		quoteToolNames(stale),
		strings.Join(pairs, ", "),
	)
}

func quoteToolNames(names []string) string {
	quoted := make([]string, 0, len(names))
	for _, name := range names {
		quoted = append(quoted, fmt.Sprintf("%q", name))
	}
	return strings.Join(quoted, ", ")
}

// agentChildPolicy derives the child tool policy of a task from the parent
// policy, falling back to the task's own request when the parent has none.
func agentChildPolicy(parent *Agent, task SubagentTask) *ToolExecutionPolicy {
	parentPolicy := (*ToolExecutionPolicy)(nil)
	if parent != nil {
		parentPolicy = parent.GetToolExecutionPolicy()
	}
	if parentPolicy != nil {
		return parentPolicy.DeriveChildForTask(task.ToolsWhitelist, task.ReadOnly, task.Role, subagentWritePaths(task))
	}
	child := NewToolExecutionPolicy(task.ToolsWhitelist, task.ReadOnly)
	child.SetCapabilityScope(CapabilitiesForTask(task.Role, task.ReadOnly, task.ToolsWhitelist, subagentWritePaths(task)))
	return child
}

// resolveChildToolSurface resolves the effective tool surface of a subagent
// task: it picks the requested allowlist (explicit whitelist or role defaults),
// rejects requests that could only produce a tool-less child, derives the child
// policy and narrows the task allowlist to what that policy actually grants.
//
// The scheduler and the child factory both go through it so the two paths
// cannot drift apart and silently disagree about the child's tools.
func resolveChildToolSurface(parent *Agent, task SubagentTask) (SubagentTask, *ToolExecutionPolicy, error) {
	parentPolicy := (*ToolExecutionPolicy)(nil)
	if parent != nil {
		parentPolicy = parent.GetToolExecutionPolicy()
	}
	source := "tools_whitelist"
	requested := task.ToolsWhitelist
	if requested == nil {
		source = "role defaults"
		requested = DefaultToolsForRole(task.Role)
	}
	normalized := normalizeToolWhitelist(requested)

	if err := validateRequestedToolVocabulary(task.ID, source, normalized, agentToolSurface(parent)); err != nil {
		return task, nil, err
	}

	task.ToolsWhitelist = normalized
	child := agentChildPolicy(parent, task)
	if err := validateChildToolAllowlist(task.ID, source, requested, child); err != nil {
		return task, nil, err
	}
	if child == nil {
		return task, nil, nil
	}
	task.ReadOnly = child.ReadOnly
	// Only a parent policy can narrow the request further; without one the
	// derived allowlist is exactly the requested set, so keeping the caller's
	// order avoids re-shuffling the allowlist for no reason.
	if child.AllowlistEnabled && parentPolicy != nil {
		task.ToolsWhitelist = child.AllowedToolNames()
	}
	return task, child, nil
}
