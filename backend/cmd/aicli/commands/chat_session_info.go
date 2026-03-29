package commands

import (
	"net/url"
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui"
)

func buildChatSessionInfo(session *ChatSession) ui.SessionInfo {
	if session == nil {
		return ui.SessionInfo{}
	}

	endpointURL := strings.TrimSpace(session.BaseURL)
	if endpointURL == "" {
		endpointURL = buildChatSessionEndpoint(session)
	}

	isReasoningModel := false
	if session.Adapter != nil {
		isReasoningModel = session.Adapter.IsReasoningModel(session.Model)
	}

	return ui.SessionInfo{
		ProviderName:     session.ProviderName,
		Protocol:         session.Provider.GetProtocol(),
		ModelName:        session.Model,
		EndpointURL:      endpointURL,
		Host:             extractChatSessionHost(endpointURL),
		KeyCount:         len(session.Provider.GetAllAPIKeys()),
		Timeout:          formatChatSessionTimeout(session),
		IsStream:         session.Stream,
		IsReasoningModel: isReasoningModel,
	}
}

func buildChatSessionEndpoint(session *ChatSession) string {
	if session == nil {
		return ""
	}
	if session.Adapter == nil {
		return strings.TrimSpace(session.Provider.BaseURL)
	}
	return buildProviderURL(session.Provider, session.Adapter.GetAPIPath(), session.Model)
}

func extractChatSessionHost(rawURL string) string {
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(parsed.Host)
}

func formatChatSessionTimeout(session *ChatSession) string {
	if session == nil {
		return ""
	}
	if session.RequestTimeout > 0 {
		return session.RequestTimeout.String()
	}
	if session.Provider.Timeout > 0 {
		return session.Provider.Timeout.String()
	}
	return ""
}
