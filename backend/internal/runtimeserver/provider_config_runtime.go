package runtimeserver

import (
	"time"

	agentconfig "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
	runtimellm "github.com/wwsheng009/ai-agent-runtime/internal/llm"
)

func buildRuntimeProviderConfigs(cfg *agentconfig.Config) map[string]*runtimellm.ProviderConfig {
	providerConfigs := make(map[string]*runtimellm.ProviderConfig)
	if cfg == nil {
		return providerConfigs
	}

	retryTuning := runtimellm.RetryTuningFromAgentConfig(cfg)
	retryRules := runtimellm.RetryRulesFromAgentConfig(cfg)

	for name, provider := range cfg.Providers.Items {
		if !provider.Enabled {
			continue
		}

		providerType := provider.GetType()
		if providerType == "" {
			continue
		}

		timeout := provider.Timeout
		if timeout <= 0 {
			timeout = cfg.Providers.Timeout
		}
		if timeout <= 0 {
			timeout = 60 * time.Second
		}
		maxRetries := runtimellm.ProviderMaxRetriesFromAgentConfig(cfg)

		providerConfigs[name] = &runtimellm.ProviderConfig{
			Type:                    providerType,
			APIKey:                  provider.GetAPIKey(),
			BaseURL:                 provider.BaseURL,
			APIPath:                 provider.APIPath,
			Timeout:                 timeout,
			MaxRetries:              maxRetries,
			RetryTuning:             retryTuning,
			RetryRules:              retryRules,
			DefaultModel:            provider.DefaultModel,
			SupportedModels:         cloneRuntimeStringSlice(provider.SupportedModels),
			ModelMappings:           cloneRuntimeStringMap(provider.ModelMappings),
			ModelCapabilities:       cloneRuntimeModelCapabilities(provider.ModelCapabilities),
			Headers:                 cloneRuntimeStringMap(provider.Headers),
			HeaderMappings:          cloneRuntimeStringMap(provider.HeaderMappings),
			HeaderMappingRules:      cloneRuntimeHeaderMappingRules(provider.HeaderMappingRules),
			SupportsMaxOutputTokens: provider.SupportsMaxOutputTokens,
			Proxy:                   agentconfig.EffectiveProxyConfig(&cfg.Providers.Proxy, provider.Proxy),
		}
	}

	return providerConfigs
}

func cloneRuntimeStringSlice(input []string) []string {
	if len(input) == 0 {
		return nil
	}
	return append([]string(nil), input...)
}

func cloneRuntimeStringMap(input map[string]string) map[string]string {
	if len(input) == 0 {
		return nil
	}
	output := make(map[string]string, len(input))
	for key, value := range input {
		output[key] = value
	}
	return output
}

func cloneRuntimeModelCapabilities(input map[string]agentconfig.ModelCapabilitySpec) map[string]agentconfig.ModelCapabilitySpec {
	if len(input) == 0 {
		return nil
	}
	output := make(map[string]agentconfig.ModelCapabilitySpec, len(input))
	for key, value := range input {
		cloned := value
		if len(value.InputModalities) > 0 {
			cloned.InputModalities = append([]string(nil), value.InputModalities...)
		}
		if len(value.ReasoningEfforts) > 0 {
			cloned.ReasoningEfforts = append([]string(nil), value.ReasoningEfforts...)
		}
		if len(value.ReasoningEffortBudgets) > 0 {
			cloned.ReasoningEffortBudgets = make(map[string]int, len(value.ReasoningEffortBudgets))
			for key, budget := range value.ReasoningEffortBudgets {
				cloned.ReasoningEffortBudgets[key] = budget
			}
		}
		output[key] = cloned
	}
	return output
}

func cloneRuntimeHeaderMappingRules(input []agentconfig.HeaderMappingRule) []runtimellm.HeaderMappingRule {
	if len(input) == 0 {
		return nil
	}
	output := make([]runtimellm.HeaderMappingRule, len(input))
	for i, rule := range input {
		output[i] = runtimellm.HeaderMappingRule{
			Name:         rule.Name,
			Enabled:      rule.Enabled,
			Header:       rule.Header,
			TargetHeader: rule.TargetHeader,
			MatchType:    rule.MatchType,
			Match:        rule.Match,
			Value:        rule.Value,
		}
	}
	return output
}
