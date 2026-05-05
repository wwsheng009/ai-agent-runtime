package llm

import (
	"strings"

	agentconfig "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
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

func supportedProviderReasoningEffort(raw string, capability agentconfig.ModelCapabilitySpec, hasCapability bool) string {
	effort := runtimetypes.NormalizeReasoningEffort(raw)
	if effort == "" {
		return ""
	}
	if !hasCapability || len(capability.ReasoningEfforts) == 0 {
		return effort
	}
	for _, allowed := range capability.ReasoningEfforts {
		if strings.EqualFold(strings.TrimSpace(allowed), effort) {
			return effort
		}
	}
	return ""
}

func fallbackProviderModelCapability(providerName, protocol, baseURL string) (agentconfig.ModelCapabilitySpec, bool) {
	if !strings.EqualFold(strings.TrimSpace(protocol), "openai") {
		return agentconfig.ModelCapabilitySpec{}, false
	}
	name := strings.ToLower(strings.TrimSpace(providerName))
	normalizedBaseURL := strings.ToLower(strings.TrimSpace(baseURL))
	if isSensenovaProvider(name, normalizedBaseURL) {
		return agentconfig.ModelCapabilitySpec{
			ReasoningModel:   true,
			ReasoningEfforts: []string{"low", "medium", "high", "none"},
		}, true
	}
	if name != "nvidia" && !strings.Contains(normalizedBaseURL, "integrate.api.nvidia.com") {
		return agentconfig.ModelCapabilitySpec{}, false
	}
	return agentconfig.ModelCapabilitySpec{
		ReasoningModel:   true,
		ReasoningEfforts: []string{"minimal", "low", "medium", "high"},
	}, true
}

func isSensenovaProvider(providerName, baseURL string) bool {
	name := strings.ToLower(strings.TrimSpace(providerName))
	normalizedBaseURL := strings.ToLower(strings.TrimSpace(baseURL))
	return strings.Contains(name, "sensenova") || strings.Contains(normalizedBaseURL, "sensenova.cn")
}

func providerModelCapabilitiesWithFallback(capabilities map[string]agentconfig.ModelCapabilitySpec, providerName, protocol, baseURL string) map[string]agentconfig.ModelCapabilitySpec {
	capability, ok := fallbackProviderModelCapability(providerName, protocol, baseURL)
	if !ok {
		return capabilities
	}
	if len(capabilities) == 0 {
		return map[string]agentconfig.ModelCapabilitySpec{
			"*": capability,
		}
	}
	if _, exists := capabilities["*"]; exists {
		return capabilities
	}
	merged := CloneModelCapabilityMap(capabilities)
	merged["*"] = capability
	return merged
}
