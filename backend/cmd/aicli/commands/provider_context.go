package commands

import (
	"fmt"
	"sort"
	"strings"

	config "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
	"github.com/wwsheng009/ai-agent-runtime/internal/llm/adapter"
)

type providerExecutionContext struct {
	ProviderName   string
	Provider       config.Provider
	Adapter        adapter.ProtocolAdapter
	Model          string
	RequestedModel string
	ModelMapped    bool
}

func resolveProviderExecutionContext(cfg *config.Config, providerFlag, modelFlag string) (*providerExecutionContext, map[string]interface{}, error) {
	details := map[string]interface{}{}
	if cfg == nil {
		return nil, details, fmt.Errorf("config is nil")
	}

	providerName := providerFlag
	if providerName == "" {
		providerName = cfg.Providers.DefaultProvider
	}
	if providerName == "" {
		return nil, details, fmt.Errorf("no provider specified and no default provider configured")
	}
	details["provider"] = providerName

	provider, ok := cfg.Providers.Items[providerName]
	if !ok {
		if available := listEnabledProviderNames(cfg); len(available) > 0 {
			details["available_providers"] = available
		}
		return nil, details, fmt.Errorf("provider '%s' not found", providerName)
	}
	if !provider.Enabled {
		return nil, details, fmt.Errorf("provider '%s' is disabled", providerName)
	}

	modelName := modelFlag
	if modelName == "" {
		modelName = provider.DefaultModel
	}
	if modelName == "" {
		return nil, details, fmt.Errorf("no model specified and no default model configured for provider '%s'", providerName)
	}
	requestedModel := modelName
	mappedModel := config.ApplyModelMapping(&provider, modelName)
	details["requested_model"] = requestedModel
	details["model"] = mappedModel
	if mappedModel != requestedModel {
		details["mapped_model"] = mappedModel
	}

	return &providerExecutionContext{
		ProviderName:   providerName,
		Provider:       provider,
		Adapter:        adapter.GetAdapterOrDefault(provider.GetProtocol()),
		Model:          mappedModel,
		RequestedModel: requestedModel,
		ModelMapped:    mappedModel != requestedModel,
	}, details, nil
}

func listEnabledProviderNames(cfg *config.Config) []string {
	if cfg == nil {
		return nil
	}
	names := make([]string, 0, len(cfg.Providers.Items))
	for name, provider := range cfg.Providers.Items {
		if provider.Enabled {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names
}

func resolveEnabledProviderNameByProtocol(cfg *config.Config, protocol string, preferred ...string) (string, bool) {
	if cfg == nil {
		return "", false
	}
	protocol = strings.ToLower(strings.TrimSpace(protocol))
	if protocol == "" {
		return "", false
	}

	for _, candidate := range preferred {
		if name, ok := canonicalEnabledProviderName(cfg, candidate); ok {
			provider := cfg.Providers.Items[name]
			if strings.EqualFold(provider.GetProtocol(), protocol) {
				return name, true
			}
		}
	}

	if name, ok := canonicalEnabledProviderName(cfg, cfg.Providers.DefaultProvider); ok {
		provider := cfg.Providers.Items[name]
		if strings.EqualFold(provider.GetProtocol(), protocol) {
			return name, true
		}
	}

	matches := make([]string, 0, len(cfg.Providers.Items))
	for name, provider := range cfg.Providers.Items {
		if provider.Enabled && strings.EqualFold(provider.GetProtocol(), protocol) {
			matches = append(matches, name)
		}
	}
	sort.Strings(matches)
	if len(matches) == 1 {
		return matches[0], true
	}
	return "", false
}

func canonicalEnabledProviderName(cfg *config.Config, providerName string) (string, bool) {
	if cfg == nil {
		return "", false
	}
	providerName = strings.TrimSpace(providerName)
	if providerName == "" {
		return "", false
	}
	if provider, ok := cfg.Providers.Items[providerName]; ok && provider.Enabled {
		return providerName, true
	}
	for name, provider := range cfg.Providers.Items {
		if provider.Enabled && strings.EqualFold(name, providerName) {
			return name, true
		}
	}
	return "", false
}
