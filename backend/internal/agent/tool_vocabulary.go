package agent

import (
	"fmt"
	"strings"

	runtimepolicy "github.com/wwsheng009/ai-agent-runtime/internal/policy"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolregistry"
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

// 退役内置工具名 → 现役替代名的表只保留在 toolregistry（LegacyReplacement）。
// 这里曾有过第二份副本，它既落后于注册表（缺 overwrite_file/multi_edit 等条目），
// 又把现役别名 bash 误列为退役名——于是 surface 含 shell 时，一个本来可执行的
// bash 请求会被判成"运行时已不再提供"而拒掉。
//
// 该表的既有契约不变：只做诊断，绝不改写请求（read_file/write_file 之类的名字
// 同样可能属于真实的 broker/MCP 工具），且只有替代名确实出现在 agent 的现役
// 工具面里才报告。

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
		// 退役名 → 现役替代名的单一真源在 toolregistry.LegacyReplacement。
		// agent 侧不再自持一份表：旧副本把现役别名 bash 也列成退役名，
		// 于是 surface 含 shell 时会把合法的 bash 请求当成"运行时已不再提供"拒掉；
		// 两侧各持一份表的漂移也是这一类 bug 的温床。
		replacement, ok := toolregistry.LegacyReplacement(trimmed)
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
		return parentPolicy.DeriveChildForTask(task.ToolsWhitelist, task.ReadOnly, subagentPolicyRole(task), subagentWritePaths(task))
	}
	child := NewToolExecutionPolicy(task.ToolsWhitelist, task.ReadOnly)
	child.SetCapabilityScope(CapabilitiesForTask(subagentPolicyRole(task), task.ReadOnly, task.ToolsWhitelist, subagentWritePaths(task)))
	return child
}

// subagentPolicyRole resolves the role string used for role-family tool
// defaults and the capability floor. Models set role, task_type, or agent_type
// depending on the spawn tool (spawn_subagents marks role as a deprecated
// routing alias and prefers task_type; spawn_agent uses agent_type), so all
// three feed the same family table. Keying only off task.Role silently sent
// "task_type-only" children down the unknown-role path: no role defaults, and
// an inherited shell tool that the derived capability scope then denied with
// "capability not allowed by execution policy: exec_shell".
func subagentPolicyRole(task SubagentTask) string {
	for _, candidate := range []string{task.Role, task.TaskType, task.AgentType} {
		if trimmed := strings.TrimSpace(candidate); trimmed != "" {
			return trimmed
		}
	}
	return ""
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
		requested = DefaultToolsForRole(subagentPolicyRole(task))
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
	task = appendShellSurfaceRouteWarnings(task, child)
	return task, child, nil
}

// appendShellSurfaceRouteWarnings adds non-blocking route warnings when the
// task goal clearly needs shell commands but the resolved child surface cannot
// run them. This is the dispatch-side half of the 2026-09-30 real-machine
// failure: a child whose whitelist omitted shell (or whose capability scope
// lacked exec_shell) spent its whole run probing for command tools, producing
// "tool not found: commands" / "capability not allowed ...: exec_shell".
// The warning reaches the parent in the spawn result so it can fix the surface
// at the decision point instead of re-spawning blindly.
func appendShellSurfaceRouteWarnings(task SubagentTask, child *ToolExecutionPolicy) SubagentTask {
	if child == nil || !goalHasShellIntent(task.Goal) {
		return task
	}
	if childSurfaceLacksShell(child) {
		task.RouteWarnings = appendRouteWarningOnce(task.RouteWarnings, shellSurfaceRouteWarning())
		return task
	}
	if child.ReadOnly && goalNeedsMutatingShell(task.Goal) {
		task.RouteWarnings = appendRouteWarningOnce(task.RouteWarnings, readOnlyShellRouteWarning())
	}
	return task
}

// childSurfaceLacksShell reports whether the derived policy cannot execute any
// shell command: either the allowlist has no shell-like tool, or the capability
// scope does not grant exec_shell. A nil policy means "no gate" (unknown, not
// denied), so it stays permissive.
func childSurfaceLacksShell(child *ToolExecutionPolicy) bool {
	if child == nil {
		return false
	}
	// The allowlist may carry wildcard patterns ("*" / "shell-like prefixes");
	// AllowedToolNames only lists the literal entries, so the wildcard-aware
	// AdmitsAnyShellLikeTool check guards against false "shell-less" warnings.
	if child.AllowlistEnabled && !child.AdmitsAnyShellLikeTool() && !containsShellLikeToolName(child.AllowedToolNames()) {
		return true
	}
	if child.CapabilityScopeEnabled {
		if err := child.AllowCapabilities([]runtimepolicy.Capability{runtimepolicy.CapExecShell}); err != nil {
			return true
		}
	}
	return false
}

func containsShellLikeToolName(names []string) bool {
	for _, name := range names {
		if runtimepolicy.IsShellLikeToolName(name) {
			return true
		}
	}
	return false
}

func appendRouteWarningOnce(warnings []string, warning string) []string {
	for _, existing := range warnings {
		if existing == warning {
			return warnings
		}
	}
	return append(warnings, warning)
}
