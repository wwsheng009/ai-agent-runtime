package modelrouting

import (
	"fmt"
	"strings"

	agentconfig "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
)

const (
	ScopeSubagent = "subagent"
	ScopeTeam     = "team"

	RoutingSourceSubagent          = "subagent"
	RoutingSourceTeamIndependent   = "team_independent"
	RoutingSourceSubagentInherited = "subagent_inherited"
)

type ConfigParentOverrides struct {
	Provider        string
	Model           string
	ReasoningEffort string
}

// ResolveConfigScope selects the effective Subagent or Team route policy.
func ResolveConfigScope(
	cfg *agentconfig.Config,
	rawScope string,
	workflow string,
) (string, string, *agentconfig.AICLISubagentRoutingConfig, error) {
	scope := strings.ToLower(strings.TrimSpace(rawScope))
	switch scope {
	case "", "auto":
		if normalizeRouteWorkflow(workflow) == "spawn_team" {
			scope = ScopeTeam
		} else {
			scope = ScopeSubagent
		}
	case "subagent", "subagents", "child":
		scope = ScopeSubagent
	case "team", "teams":
		scope = ScopeTeam
	default:
		return "", "", nil, fmt.Errorf(
			"invalid route scope %q; expected auto, subagent, or team",
			rawScope,
		)
	}

	if scope == ScopeTeam {
		if cfg != nil && cfg.AICLI != nil && cfg.AICLI.Teams != nil && cfg.AICLI.Teams.Routing != nil {
			return scope, RoutingSourceTeamIndependent, cfg.AICLI.Teams.Routing, nil
		}
		return scope, RoutingSourceSubagentInherited, agentconfig.EffectiveTeamRoutingConfig(cfg), nil
	}
	if cfg != nil && cfg.AICLI != nil && cfg.AICLI.Subagents != nil {
		return scope, RoutingSourceSubagent, cfg.AICLI.Subagents.Routing, nil
	}
	return scope, RoutingSourceSubagent, nil, nil
}

func normalizeRouteWorkflow(workflow string) string {
	workflow = strings.ToLower(strings.TrimSpace(workflow))
	switch workflow {
	case "team", "spawn_team", "spawn-team":
		return "spawn_team"
	default:
		return workflow
	}
}

// ResolveParentDefaults derives the same parent fallback used by CLI dry-runs
// and Web route previews.
func ResolveParentDefaults(
	cfg *agentconfig.Config,
	catalog ProviderCatalog,
	overrides ConfigParentOverrides,
) ParentDefaults {
	providerName := strings.TrimSpace(overrides.Provider)
	if providerName == "" && cfg != nil && cfg.AICLI != nil && cfg.AICLI.Chat != nil {
		preferred := strings.TrimSpace(cfg.AICLI.Chat.DefaultProvider)
		if configuredProviderEnabled(cfg, preferred) {
			providerName = preferred
		}
	}
	if providerName == "" && cfg != nil {
		providerName = strings.TrimSpace(cfg.Providers.DefaultProvider)
	}
	if catalog != nil {
		if resolved := strings.TrimSpace(catalog.ResolveProviderName(providerName)); resolved != "" {
			providerName = resolved
		}
	}

	model := strings.TrimSpace(overrides.Model)
	if model == "" && cfg != nil && cfg.AICLI != nil && cfg.AICLI.Chat != nil {
		model = strings.TrimSpace(cfg.AICLI.Chat.DefaultModel)
	}
	result := ParentDefaults{
		Provider:        providerName,
		Model:           model,
		ReasoningEffort: resolveParentReasoning(cfg, overrides.ReasoningEffort),
	}
	if cfg == nil || providerName == "" {
		return result
	}
	provider, ok := cfg.Providers.Items[providerName]
	if !ok || !provider.Enabled {
		return result
	}
	if result.Model == "" {
		result.Model = strings.TrimSpace(provider.DefaultModel)
	}
	if result.Model != "" {
		result.Model = agentconfig.ApplyModelMapping(&provider, result.Model)
	}
	result.MaxTokens = provider.GetMaxTokensLimit()
	result.Timeout = provider.Timeout
	return result
}

func configuredProviderEnabled(cfg *agentconfig.Config, providerName string) bool {
	if cfg == nil {
		return false
	}
	provider, ok := cfg.Providers.Items[strings.TrimSpace(providerName)]
	return ok && provider.Enabled
}

func resolveParentReasoning(cfg *agentconfig.Config, override string) string {
	if effort := strings.TrimSpace(override); effort != "" {
		return effort
	}
	if cfg != nil && cfg.AICLI != nil && cfg.AICLI.Chat != nil {
		return strings.TrimSpace(cfg.AICLI.Chat.ReasoningEffort)
	}
	return ""
}
