package skills

import (
	"testing"

	"github.com/stretchr/testify/require"
	agentconfig "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
)

func TestHandlerSetAICLIConfigKeepsImmutableRoutingSnapshot(t *testing.T) {
	enabled := true
	config := &agentconfig.Config{AICLI: &agentconfig.AICLIConfig{
		Subagents: &agentconfig.AICLISubagentsConfig{
			Routing: &agentconfig.AICLISubagentRoutingConfig{Enabled: &enabled},
		},
	}}
	handler := NewHandler(nil, nil, nil)
	handler.SetAICLIConfig(config)

	config.AICLI.Subagents.Routing.Levels = map[string]agentconfig.AICLISubagentRouteProfile{
		"hard": {Provider: "mutated"},
	}

	routing := handler.subagentRoutingConfig()
	require.NotNil(t, routing)
	require.NotNil(t, routing.Enabled)
	require.True(t, *routing.Enabled)
	require.Empty(t, routing.Levels)
}

func TestHandlerSetAICLIConfigCanClearRoutingSnapshot(t *testing.T) {
	handler := NewHandler(nil, nil, nil)
	handler.SetAICLIConfig(&agentconfig.Config{AICLI: &agentconfig.AICLIConfig{
		Teams: &agentconfig.AICLITeamsConfig{
			Routing: &agentconfig.AICLISubagentRoutingConfig{},
		},
	}})
	require.NotNil(t, handler.teamRoutingConfig())

	handler.SetAICLIConfig(nil)

	require.Nil(t, handler.subagentRoutingConfig())
	require.Nil(t, handler.teamRoutingConfig())
}
