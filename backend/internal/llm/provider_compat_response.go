package llm

import (
	"io"

	"github.com/wwsheng009/ai-agent-runtime/internal/llm/providercompat"
)

func (p *ProviderWrapper) providerCompatContext(model string) providercompat.Context {
	if p == nil || p.config == nil {
		return providercompat.Context{Model: model}
	}
	return providercompat.Context{
		Protocol:                p.config.Type,
		BaseURL:                 p.config.BaseURL,
		APIPath:                 p.config.APIPath,
		Model:                   model,
		SupportsMaxOutputTokens: p.config.SupportsMaxOutputTokens,
		ConfiguredCapabilities:  p.config.ModelCapabilities,
	}
}

func (p *ProviderWrapper) normalizeAssistantMessage(model string, assistantMsg map[string]interface{}) map[string]interface{} {
	return providercompat.NormalizeAssistantMessage(p.providerCompatContext(model), assistantMsg)
}

func (p *ProviderWrapper) prepareRequestBody(model string, body map[string]interface{}) map[string]interface{} {
	return providercompat.PrepareRequestBody(p.providerCompatContext(model), body)
}

func (p *ProviderWrapper) normalizeStreamReader(model string, reader io.Reader) io.Reader {
	return providercompat.NormalizeStreamReader(p.providerCompatContext(model), reader)
}

func gatewayProviderCompatContext(selected *SelectedResource, protocol, model string) providercompat.Context {
	var supportsMaxOutputTokens *bool
	apiPath := ""
	if selected != nil && selected.Provider != nil {
		supportsMaxOutputTokens = selected.Provider.SupportsMaxOutputTokens
		apiPath = selected.Provider.APIPath
	}
	return providercompat.Context{
		ProviderName:            providerNameFromSelected(selected),
		Protocol:                protocol,
		BaseURL:                 baseURLFromSelected(selected),
		APIPath:                 apiPath,
		Model:                   model,
		SupportsMaxOutputTokens: supportsMaxOutputTokens,
		ConfiguredCapabilities:  selectedProviderModelCapabilities(selected),
	}
}

func normalizeGatewayAssistantMessage(selected *SelectedResource, protocol, model string, assistantMsg map[string]interface{}) map[string]interface{} {
	return providercompat.NormalizeAssistantMessage(gatewayProviderCompatContext(selected, protocol, model), assistantMsg)
}

func prepareGatewayRequestBody(selected *SelectedResource, protocol, model string, body map[string]interface{}) map[string]interface{} {
	return providercompat.PrepareRequestBody(gatewayProviderCompatContext(selected, protocol, model), body)
}

func normalizeGatewayStreamReader(selected *SelectedResource, protocol, model string, reader io.Reader) io.Reader {
	return providercompat.NormalizeStreamReader(gatewayProviderCompatContext(selected, protocol, model), reader)
}
