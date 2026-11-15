package runtimeapi

import (
	"github.com/wwsheng009/ai-agent-runtime/internal/agent"
	runtimecfg "github.com/wwsheng009/ai-agent-runtime/internal/config"
	"github.com/wwsheng009/ai-agent-runtime/internal/runtimehost"
	"github.com/wwsheng009/ai-agent-runtime/internal/types"
)

// buildAgentChatLoopConfig assembles the stateless /api/agent/chat loop from
// the same shared Loop builder used by actor sessions. Parallel-tool defaults
// (including MaxParallelToolCalls=4) deliberately stay identical to the actor
// path; EnableParallelTools remains the gate for whether they are used.
func buildAgentChatLoopConfig(
	agentConfig *agent.Config,
	selectedConfig *runtimecfg.RuntimeConfig,
	reasoningEffort string,
	thinking *types.ThinkingConfig,
	budgetTokens int,
) *agent.LoopReActConfig {
	config := runtimehost.BuildLoopReActConfig(runtimehost.LoopConfigSpec{
		RuntimeConfig:            selectedConfig,
		RequestedReasoningEffort: reasoningEffort,
		TurnBudgetTokens:         budgetTokens,
	})
	if agentConfig != nil {
		config.MaxSteps = agentConfig.MaxSteps
		config.MaxToolCalls = agentConfig.MaxToolCalls
		config.MaxRunDuration = agentConfig.MaxRunDuration
		config.MaxExplorationSteps = agentConfig.MaxExplorationSteps
		config.MaxRepeatedToolCalls = agentConfig.MaxRepeatedToolCalls
		config.MaxRepeatedPollCalls = agentConfig.MaxRepeatedPollCalls
	}
	config.EnableThought = true
	config.EnableToolCalls = true
	config.EnableParallelTools = selectedConfig != nil && selectedConfig.Agent.EnableParallelTools
	if selectedConfig != nil && selectedConfig.Agent.MaxParallelToolCalls > 0 {
		config.MaxParallelToolCalls = selectedConfig.Agent.MaxParallelToolCalls
	}
	config.ReasoningEffort = types.ResolveReasoningEffort(reasoningEffort)
	config.Thinking = types.ResolveThinkingConfig(thinking)
	config.Temperature = 0.7
	return config
}
