package llm

import (
	agentconfig "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
	"github.com/wwsheng009/ai-agent-runtime/internal/llm/providercompat"
	runtimetypes "github.com/wwsheng009/ai-agent-runtime/internal/types"
)

type ThinkingConfig = runtimetypes.ThinkingConfig

func cloneThinkingConfig(thinking *ThinkingConfig) *ThinkingConfig {
	return runtimetypes.CloneThinkingConfig(thinking)
}

func resolveThinkingConfig(explicit *ThinkingConfig, containers ...map[string]interface{}) *ThinkingConfig {
	return runtimetypes.ResolveThinkingConfig(explicit, containers...)
}

func resolveReasoningEffort(explicit string, containers ...map[string]interface{}) string {
	return runtimetypes.ResolveReasoningEffort(explicit, containers...)
}

type RequestReasoningConfig struct {
	ReasoningEffort string
	Thinking        *ThinkingConfig
}

func resolveRequestReasoningConfig(explicitReasoningEffort string, explicitThinking *ThinkingConfig, containers ...map[string]interface{}) RequestReasoningConfig {
	return RequestReasoningConfig{
		ReasoningEffort: resolveReasoningEffort(explicitReasoningEffort, containers...),
		Thinking:        resolveThinkingConfig(explicitThinking, containers...),
	}
}

func providerModelCapabilitiesWithFallback(capabilities map[string]agentconfig.ModelCapabilitySpec, providerName, protocol, baseURL string) map[string]agentconfig.ModelCapabilitySpec {
	return providercompat.MergeCapabilities(providercompat.Context{
		ProviderName: providerName,
		Protocol:     protocol,
		BaseURL:      baseURL,
	}, capabilities)
}
