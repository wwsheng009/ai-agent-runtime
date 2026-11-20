package llm

import "github.com/wwsheng009/ai-agent-runtime/internal/llm/providercompat"

func providerSupportsCodexMaxOutputTokens(baseURL string, explicit *bool) bool {
	return providercompat.SupportsMaxOutputTokens(baseURL, explicit)
}
