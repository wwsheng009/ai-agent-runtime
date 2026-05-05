package commands

import (
	"io"

	config "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
	"github.com/wwsheng009/ai-agent-runtime/internal/llm/providercompat"
)

func providerCompatContext(providerName string, provider config.Provider, model string) providercompat.Context {
	return providercompat.Context{
		ProviderName:            providerName,
		Protocol:                provider.GetProtocol(),
		BaseURL:                 provider.BaseURL,
		APIPath:                 provider.APIPath,
		Model:                   model,
		SupportsMaxOutputTokens: provider.SupportsMaxOutputTokens,
		ConfiguredCapabilities:  provider.ModelCapabilities,
	}
}

func normalizeProviderAssistantMessage(providerName string, provider config.Provider, model string, assistantMsg map[string]interface{}) map[string]interface{} {
	return providercompat.NormalizeAssistantMessage(providerCompatContext(providerName, provider, model), assistantMsg)
}

func prepareProviderRequestBody(providerName string, provider config.Provider, model string, body map[string]interface{}) map[string]interface{} {
	return providercompat.PrepareRequestBody(providerCompatContext(providerName, provider, model), body)
}

func normalizeProviderStreamReader(providerName string, provider config.Provider, model string, reader io.Reader) io.Reader {
	return providercompat.NormalizeStreamReader(providerCompatContext(providerName, provider, model), reader)
}

func normalizeProviderStreamChunk(providerName string, provider config.Provider, model string, chunk map[string]interface{}) map[string]interface{} {
	return providercompat.NormalizeStreamChunk(providerCompatContext(providerName, provider, model), chunk)
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

func prepareChatSessionRequestBody(session *ChatSession, body map[string]interface{}) map[string]interface{} {
	if session == nil {
		return body
	}
	return prepareProviderRequestBody(session.ProviderName, session.Provider, session.Model, body)
}

func preparePipeSessionRequestBody(session *PipeSession, body map[string]interface{}) map[string]interface{} {
	if session == nil {
		return body
	}
	return prepareProviderRequestBody(session.ProviderName, session.Provider, session.Model, body)
}

func normalizeChatSessionStreamReader(session *ChatSession, reader io.Reader) io.Reader {
	if session == nil {
		return reader
	}
	return normalizeProviderStreamReader(session.ProviderName, session.Provider, session.Model, reader)
}

func normalizePipeSessionStreamChunk(session *PipeSession, chunk map[string]interface{}) map[string]interface{} {
	if session == nil {
		return chunk
	}
	return normalizeProviderStreamChunk(session.ProviderName, session.Provider, session.Model, chunk)
}
