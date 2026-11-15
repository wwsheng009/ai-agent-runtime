package runtimeapi

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wwsheng009/ai-agent-runtime/internal/agent"
	runtimecfg "github.com/wwsheng009/ai-agent-runtime/internal/config"
)

func TestBuildAgentChatLoopConfigCarriesBudgetAndSharedDefaults(t *testing.T) {
	runtimeConfig := &runtimecfg.RuntimeConfig{
		Agent: runtimecfg.AgentConfig{
			EnableParallelTools:  true,
			MaxParallelToolCalls: 3,
		},
		Context: runtimecfg.ContextConfig{AccountingMode: "estimate_only"},
	}
	agentConfig := &agent.Config{
		MaxSteps:             9,
		MaxToolCalls:         8,
		MaxExplorationSteps:  7,
		MaxRepeatedToolCalls: 6,
		MaxRepeatedPollCalls: 5,
	}
	loop := buildAgentChatLoopConfig(agentConfig, runtimeConfig, " high ", nil, 4200)
	require.Equal(t, 4200, loop.TurnBudgetTokens)
	require.Equal(t, "estimate_only", loop.ContextAccountingMode)
	require.Equal(t, 9, loop.MaxSteps)
	require.Equal(t, 8, loop.MaxToolCalls)
	require.Equal(t, 7, loop.MaxExplorationSteps)
	require.Equal(t, 6, loop.MaxRepeatedToolCalls)
	require.Equal(t, 5, loop.MaxRepeatedPollCalls)
	require.True(t, loop.EnableParallelTools)
	require.Equal(t, 3, loop.MaxParallelToolCalls)
	require.Equal(t, "high", loop.ReasoningEffort)
}

func TestBuildAgentChatLoopConfigKeepsSharedParallelDefault(t *testing.T) {
	loop := buildAgentChatLoopConfig(&agent.Config{}, nil, "", nil, 0)
	require.False(t, loop.EnableParallelTools)
	require.Equal(t, 4, loop.MaxParallelToolCalls,
		"the configured default must match the shared actor loop; EnableParallelTools still gates use")
	require.Zero(t, loop.TurnBudgetTokens)
}
