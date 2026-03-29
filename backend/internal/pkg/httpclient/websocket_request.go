package httpclient

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
)

// BuildWebSocketUpstreamURL 根据请求路径和 provider 配置构建上游 WebSocket URL。
func BuildWebSocketUpstreamURL(req *http.Request, providerCfg *agentconfig.Provider, model string) (string, error) {
	if req == nil || providerCfg == nil {
		return "", fmt.Errorf("request or provider config is nil")
	}

	normalizedPath := agentconfig.NormalizeRequestPath(req.URL.Path)

	queryValues, err := url.ParseQuery(req.URL.RawQuery)
	if err != nil {
		return "", fmt.Errorf("parse websocket query failed: %w", err)
	}
	if normalizedPath == "/v1/realtime" && strings.TrimSpace(model) != "" && strings.TrimSpace(queryValues.Get("model")) == "" {
		queryValues.Set("model", model)
	}

	queryString := ""
	if encoded := queryValues.Encode(); encoded != "" {
		queryString = "?" + encoded
	}

	upstreamURL := agentconfig.BuildUpstreamURLWithPath(*providerCfg, normalizedPath, queryString, model)
	upstreamURL = NormalizeWebSocketURLScheme(upstreamURL)
	if upstreamURL == "" {
		return "", fmt.Errorf("built websocket upstream url is empty")
	}

	return upstreamURL, nil
}

// BuildWebSocketUpstreamHeaders 构建上游 WS 握手头。
func BuildWebSocketUpstreamHeaders(req *http.Request, providerCfg *agentconfig.Provider, protocol, model string) http.Header {
	headers := make(http.Header)

	if req != nil {
		if betaHeader := strings.TrimSpace(req.Header.Get("OpenAI-Beta")); betaHeader != "" {
			headers.Set("OpenAI-Beta", betaHeader)
		}
		if userAgent := strings.TrimSpace(req.Header.Get("User-Agent")); userAgent != "" {
			headers.Set("User-Agent", userAgent)
		}
		if requestID := strings.TrimSpace(req.Header.Get("X-Request-ID")); requestID != "" {
			headers.Set("X-Request-ID", requestID)
		}
	}

	resolvedProtocol := agentconfig.NormalizeProtocol(protocol)
	if resolvedProtocol == "" && providerCfg != nil {
		resolvedProtocol = providerCfg.GetProtocol()
	}

	if providerCfg != nil {
		apiKey := providerCfg.GetAPIKey()
		switch resolvedProtocol {
		case "anthropic":
			if apiKey != "" {
				headers.Set("x-api-key", apiKey)
			}
			headers.Set("anthropic-version", "2023-06-01")
			headers.Set("anthropic-dangerous-direct-browser-access", "true")
		case "gemini":
			if apiKey != "" {
				headers.Set("x-goog-api-key", apiKey)
			}
		default:
			if apiKey != "" {
				headers.Set("Authorization", "Bearer "+apiKey)
			}
		}

		for key, value := range providerCfg.Headers {
			headers.Set(key, value)
		}
	}

	if strings.TrimSpace(model) != "" {
		headers.Set("X-Model", model)
	}

	return headers
}

// NormalizeWebSocketURLScheme 把 HTTP/HTTPS URL 转成 WS/WSS。
func NormalizeWebSocketURLScheme(rawURL string) string {
	if rawURL == "" {
		return ""
	}

	parsedURL, err := url.Parse(rawURL)
	if err != nil {
		return rawURL
	}

	switch strings.ToLower(parsedURL.Scheme) {
	case "http":
		parsedURL.Scheme = "ws"
	case "https":
		parsedURL.Scheme = "wss"
	}

	return parsedURL.String()
}
