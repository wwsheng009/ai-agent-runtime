package llm

import "github.com/wwsheng009/ai-agent-runtime/internal/llm/providercompat"

const codexSupportsMaxOutputTokensMetadataKey = "supports_max_output_tokens"

func providerSupportsCodexMaxOutputTokens(baseURL string, explicit *bool) bool {
	return providercompat.SupportsMaxOutputTokens(baseURL, explicit)
}

func selectedProviderSupportsCodexMaxOutputTokens(selected *SelectedResource) bool {
	if selected == nil || selected.Provider == nil {
		return true
	}
	return providerSupportsCodexMaxOutputTokens(
		selected.Provider.BaseURL,
		selected.Provider.SupportsMaxOutputTokens,
	)
}

func isChatGPTCodexBackendBaseURL(baseURL string) bool {
	return providercompat.IsChatGPTCodexBackend(baseURL)
}
