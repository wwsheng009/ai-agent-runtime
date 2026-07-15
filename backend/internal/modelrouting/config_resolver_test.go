package modelrouting

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	agentconfig "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
)

func TestResolveConfigScopeUsesIndependentOrInheritedTeamPolicy(t *testing.T) {
	enabled := true
	subagentRouting := &agentconfig.AICLISubagentRoutingConfig{Enabled: &enabled}
	cfg := &agentconfig.Config{
		AICLI: &agentconfig.AICLIConfig{
			Subagents: &agentconfig.AICLISubagentsConfig{Routing: subagentRouting},
			Teams:     &agentconfig.AICLITeamsConfig{},
		},
	}

	scope, source, routing, err := ResolveConfigScope(cfg, "auto", "spawn_team")
	require.NoError(t, err)
	require.Equal(t, ScopeTeam, scope)
	require.Equal(t, RoutingSourceSubagentInherited, source)
	require.Same(t, subagentRouting, routing)

	teamRouting := &agentconfig.AICLISubagentRoutingConfig{DefaultDifficulty: DifficultyHard}
	cfg.AICLI.Teams.Routing = teamRouting
	scope, source, routing, err = ResolveConfigScope(cfg, "team", "")
	require.NoError(t, err)
	require.Equal(t, RoutingSourceTeamIndependent, source)
	require.Same(t, teamRouting, routing)
}

func TestResolveParentDefaultsUsesCanonicalProviderModel(t *testing.T) {
	cfg := &agentconfig.Config{
		AICLI: &agentconfig.AICLIConfig{
			Chat: &agentconfig.AICLIChatConfig{
				DefaultProvider: "primary",
				DefaultModel:    "friendly",
				ReasoningEffort: "medium",
			},
		},
	}
	cfg.Providers.DefaultProvider = "fallback"
	cfg.Providers.Items = map[string]agentconfig.Provider{
		"primary": {
			Enabled:        true,
			DefaultModel:   "friendly",
			ModelMappings:  map[string]string{"friendly": "canonical"},
			MaxTokensLimit: 8192,
			Timeout:        2 * time.Minute,
		},
	}

	parent := ResolveParentDefaults(cfg, NewConfigCatalog(cfg), ConfigParentOverrides{})

	require.Equal(t, "primary", parent.Provider)
	require.Equal(t, "canonical", parent.Model)
	require.Equal(t, "medium", parent.ReasoningEffort)
	require.Equal(t, 8192, parent.MaxTokens)
	require.Equal(t, 2*time.Minute, parent.Timeout)
}
