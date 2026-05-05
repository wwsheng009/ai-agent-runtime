package commands

import (
	config "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
	"github.com/wwsheng009/ai-agent-runtime/internal/llm/providercompat"
)

func normalizeProviderAssistantMessage(providerName string, provider config.Provider, model string, assistantMsg map[string]interface{}) map[string]interface{} {
	return providercompat.NormalizeAssistantMessage(providercompat.Context{
		ProviderName:            providerName,
		Protocol:                provider.GetProtocol(),
		BaseURL:                 provider.BaseURL,
		APIPath:                 provider.APIPath,
		Model:                   model,
		SupportsMaxOutputTokens: provider.SupportsMaxOutputTokens,
		ConfiguredCapabilities:  provider.ModelCapabilities,
	}, assistantMsg)
}

func normalizeChatSessionAssistantMessage(session *ChatSession, assistantMsg map[string]interface{}) map[string]interface{} {
	if session == nil {
		return assistantMsg
	}
	return normalizeProviderAssistantMessage(session.ProviderName, session.Provider, session.Model, assistantMsg)
}

func normalizePipeSessionAssistantMessage(session *PipeSession, assistantMsg map[string]interface{}) map[string]interface{} {
	if session == nil {
		return assistantMsg
	}
	return normalizeProviderAssistantMessage(session.ProviderName, session.Provider, session.Model, assistantMsg)
}
