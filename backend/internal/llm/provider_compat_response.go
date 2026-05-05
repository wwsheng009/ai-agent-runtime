package llm

import "github.com/wwsheng009/ai-agent-runtime/internal/llm/providercompat"

func (p *ProviderWrapper) normalizeAssistantMessage(model string, assistantMsg map[string]interface{}) map[string]interface{} {
	if p == nil || p.config == nil {
		return assistantMsg
	}
	return providercompat.NormalizeAssistantMessage(providercompat.Context{
		Protocol:                p.config.Type,
		BaseURL:                 p.config.BaseURL,
		APIPath:                 p.config.APIPath,
		Model:                   model,
		SupportsMaxOutputTokens: p.config.SupportsMaxOutputTokens,
		ConfiguredCapabilities:  p.config.ModelCapabilities,
	}, assistantMsg)
}

func normalizeGatewayAssistantMessage(selected *SelectedResource, protocol, model string, assistantMsg map[string]interface{}) map[string]interface{} {
	var supportsMaxOutputTokens *bool
	apiPath := ""
	if selected != nil && selected.Provider != nil {
		supportsMaxOutputTokens = selected.Provider.SupportsMaxOutputTokens
		apiPath = selected.Provider.APIPath
	}
	return providercompat.NormalizeAssistantMessage(providercompat.Context{
		ProviderName:            providerNameFromSelected(selected),
		Protocol:                protocol,
		BaseURL:                 baseURLFromSelected(selected),
		APIPath:                 apiPath,
		Model:                   model,
		SupportsMaxOutputTokens: supportsMaxOutputTokens,
		ConfiguredCapabilities:  selectedProviderModelCapabilities(selected),
	}, assistantMsg)
}
