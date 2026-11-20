package llm

import "github.com/wwsheng009/ai-agent-runtime/internal/llm/providercompat"

const codexSupportsMaxOutputTokensMetadataKey = "supports_max_output_tokens"

func providerSupportsCodexMaxOutputTokens(baseURL string, explicit *bool) bool {
	return providercompat.SupportsMaxOutputTokens(baseURL, explicit)
}
