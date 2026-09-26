package agent

import (
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/internal/agentdef"
)

// Config option keys a host may set to anchor portable agent-definition
// discovery for this agent. Empty options fall back to agentdef defaults
// (cwd project root + user home), which matches the CLI chat use case.
const (
	// AgentdefProjectRootOptionKey overrides the project root used for
	// .agents/agents discovery inside the agent package.
	AgentdefProjectRootOptionKey = "agentdef_project_root"
	// AgentdefProfileRootOptionKey adds the profile agents/<id>/agent.yaml root.
	AgentdefProfileRootOptionKey = "agentdef_profile_root"
)

// agentdefDiscoverOptions returns discovery options for this agent.
func (a *Agent) agentdefDiscoverOptions() agentdef.DiscoverOptions {
	opts := agentdef.DiscoverOptions{}
	if a == nil {
		return opts
	}
	config := a.GetConfig()
	if config == nil || len(config.Options) == 0 {
		return opts
	}
	if value, ok := config.Options[AgentdefProjectRootOptionKey].(string); ok {
		opts.ProjectRoot = strings.TrimSpace(value)
	}
	if value, ok := config.Options[AgentdefProfileRootOptionKey].(string); ok {
		opts.ProfileRoot = strings.TrimSpace(value)
	}
	return opts
}

// AgentDefinitionsSummary renders the portable agent catalog for the
// model-visible spawn_subagents description. Discovery errors degrade to an
// empty string: tool availability must never depend on filesystem state.
func (a *Agent) AgentDefinitionsSummary() string {
	if a == nil {
		return ""
	}
	return agentdef.ModelVisibleSummary(a.agentdefDiscoverOptions(), 8, 900)
}

// applyAgentdefTaskDefaults fills omitted task fields from the referenced
// portable agent definition (agent_type). Explicit task fields always win, and
// resolution failures never reject the batch: they surface as route warnings
// so the parent can correct the agent_type instead of losing the child.
func applyAgentdefTaskDefaults(parent *Agent, task SubagentTask) SubagentTask {
	agentType := strings.TrimSpace(task.AgentType)
	if agentType == "" {
		return task
	}
	def, err := agentdef.Resolve(agentType, parent.agentdefDiscoverOptions())
	if err != nil || def == nil {
		task.RouteWarnings = append(task.RouteWarnings, "agent_type_not_found:"+agentType)
		return task
	}
	binding, err := agentdef.BuildBinding(def)
	if err != nil || binding == nil {
		task.RouteWarnings = append(task.RouteWarnings, "agent_type_binding_failed:"+agentType)
		return task
	}

	if !task.ReadOnly && binding.ReadOnly != nil && *binding.ReadOnly {
		task.ReadOnly = true
		if strings.TrimSpace(task.ReadOnlySource) == "" {
			task.ReadOnlySource = "agentdef:" + binding.AgentID
		}
	}
	if task.ToolsWhitelist == nil && !binding.ToolsWildcard && len(binding.ToolAllowlist) > 0 {
		task.ToolsWhitelist = filterAgentdefDeniedTools(binding.ToolAllowlist, binding.ToolDenylist)
	}
	if strings.TrimSpace(task.Model) == "" {
		task.Model = strings.TrimSpace(binding.Model)
	}
	if strings.TrimSpace(task.Provider) == "" {
		task.Provider = strings.TrimSpace(binding.Provider)
	}
	if strings.TrimSpace(task.ReasoningEffort) == "" {
		task.ReasoningEffort = strings.TrimSpace(binding.ReasoningEffort)
	}
	if task.MaxTurns == 0 {
		task.MaxTurns = binding.MaxTurns
	}
	if strings.TrimSpace(task.CompletionRequirement) == "" {
		task.CompletionRequirement = string(binding.CompletionRequirement)
	}
	for _, warning := range binding.Warnings {
		task.RouteWarnings = append(task.RouteWarnings, "agentdef_warning:"+warning)
	}
	return task
}

// filterAgentdefDeniedTools applies a definition's disallowedTools deny list to
// its own allowlist. Task-level explicit whitelists are never narrowed here:
// explicit spawn arguments stay authoritative.
func filterAgentdefDeniedTools(allow, deny []string) []string {
	if len(allow) == 0 {
		return nil
	}
	if len(deny) == 0 {
		return append([]string(nil), allow...)
	}
	denied := make(map[string]struct{}, len(deny))
	for _, tool := range deny {
		denied[strings.ToLower(strings.TrimSpace(tool))] = struct{}{}
	}
	out := make([]string, 0, len(allow))
	for _, tool := range allow {
		if _, blocked := denied[strings.ToLower(strings.TrimSpace(tool))]; blocked {
			continue
		}
		out = append(out, tool)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
