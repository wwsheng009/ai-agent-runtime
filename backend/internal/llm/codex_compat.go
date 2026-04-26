package llm

import "strings"

const codexSupportsMaxOutputTokensMetadataKey = "supports_max_output_tokens"

func providerSupportsCodexMaxOutputTokens(baseURL string, explicit *bool) bool {
	if explicit != nil {
		return *explicit
	}
	return !isChatGPTCodexBackendBaseURL(baseURL)
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
	lower := strings.ToLower(strings.TrimSpace(baseURL))
	if lower == "" {
		return false
	}
	return strings.Contains(lower, "chatgpt.com/backend-api/codex/responses")
}
