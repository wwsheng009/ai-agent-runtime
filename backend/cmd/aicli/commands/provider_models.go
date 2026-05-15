package commands

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	config "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
	"github.com/wwsheng009/ai-agent-runtime/internal/llm/adapter"
	httpclient "github.com/wwsheng009/ai-agent-runtime/internal/pkg/httpclient"
)

type providerModelInfo struct {
	ID                  string                 `json:"id"`
	DisplayName         string                 `json:"display_name,omitempty"`
	InputModalities     []string               `json:"input_modalities,omitempty"`
	ReasoningEfforts    []string               `json:"reasoning_efforts,omitempty"`
	MaxContextTokens    int                    `json:"max_context_tokens,omitempty"`
	SupportsRemoteCodex bool                   `json:"supports_remote_codex,omitempty"`
	Raw                 map[string]interface{} `json:"-"`
}

type providerModelsValidationRequest struct {
	Config        *config.Config
	ProviderName  string
	Provider      config.Provider
	LoginProtocol string
	ModelsPath    string
	Timeout       time.Duration
}

type providerModelsValidationResult struct {
	Endpoint   string              `json:"endpoint"`
	StatusCode int                 `json:"status_code"`
	Models     []providerModelInfo `json:"models"`
	VerifiedAt string              `json:"verified_at"`
}

func validateProviderModels(req providerModelsValidationRequest) (*providerModelsValidationResult, error) {
	loginProtocol := normalizeLoginProtocol(req.LoginProtocol, req.Provider.AuthMode)
	modelsPath := resolveProviderModelsPath(loginProtocol, req.Provider, req.ModelsPath)
	endpoint, err := buildProviderModelsURL(req.Provider, modelsPath)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(req.Provider.GetAPIKey()) == "" && !strings.EqualFold(req.Provider.AuthMode, "oauth") {
		return nil, fmt.Errorf("api key is required for provider models validation")
	}

	httpReq, err := http.NewRequest(http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("create models request: %w", err)
	}
	headers := buildProviderModelsHeaders(req.Provider, loginProtocol)
	for key, value := range headers {
		if strings.TrimSpace(value) == "" {
			continue
		}
		httpReq.Header.Set(key, value)
	}
	httpReq.Header.Set("Accept", "application/json")

	client := http.DefaultClient
	if req.Config != nil {
		client = httpclient.GetHTTPClientWithProvider(req.Config, &req.Provider)
	}
	if req.Timeout > 0 {
		cloned := *client
		cloned.Timeout = req.Timeout
		client = &cloned
	}

	start := time.Now()
	resp, err := client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("models request failed: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 10<<20))
	if err != nil {
		return nil, fmt.Errorf("read models response: %w", err)
	}
	_ = start

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("models endpoint %s returned HTTP %d: %s", endpoint, resp.StatusCode, responsePreview(body, 600))
	}
	models, err := parseProviderModelsResponse(body, loginProtocol)
	if err != nil {
		return nil, fmt.Errorf("parse models response from %s: %w", endpoint, err)
	}
	if len(models) == 0 {
		return nil, fmt.Errorf("models endpoint %s returned an empty model list", endpoint)
	}
	return &providerModelsValidationResult{
		Endpoint:   endpoint,
		StatusCode: resp.StatusCode,
		Models:     models,
		VerifiedAt: time.Now().UTC().Format(time.RFC3339),
	}, nil
}

func buildProviderModelsHeaders(provider config.Provider, loginProtocol string) map[string]string {
	runtimeProtocol := runtimeProtocolForLoginProtocol(loginProtocol)
	provider.Protocol = runtimeProtocol
	llmAdapter := adapter.GetAdapterOrDefault(runtimeProtocol)
	return llmAdapter.BuildHeaders(adapter.AdapterConfig{
		Type:    runtimeProtocol,
		APIKey:  provider.GetAPIKey(),
		Headers: provider.Headers,
	})
}

func resolveProviderModelsPath(loginProtocol string, provider config.Provider, override string) string {
	if trimmed := strings.TrimSpace(override); trimmed != "" {
		return trimmed
	}
	if trimmed := strings.TrimSpace(provider.ModelsPath); trimmed != "" {
		return trimmed
	}
	return defaultModelsPath(loginProtocol, provider.BaseURL)
}

func defaultModelsPath(loginProtocol, baseURL string) string {
	switch normalizeLoginProtocol(loginProtocol, "") {
	case providerLoginProtocolOpenAIImage:
		return "/v1/models"
	case "gemini":
		return "/v1beta/models"
	case "codex-apikey", "codex-oauth":
		lowerBase := strings.ToLower(strings.TrimSpace(baseURL))
		if strings.Contains(lowerBase, "chatgpt.com") {
			if strings.Contains(lowerBase, "/backend-api/codex") {
				return "/models"
			}
			return "/backend-api/codex/models"
		}
		return "/v1/models"
	default:
		return "/v1/models"
	}
}

func buildProviderModelsURL(provider config.Provider, modelsPath string) (string, error) {
	modelsPath = strings.TrimSpace(modelsPath)
	if modelsPath == "" {
		return "", fmt.Errorf("models path is required")
	}
	if parsed, err := url.Parse(modelsPath); err == nil && parsed.IsAbs() {
		return modelsPath, nil
	}
	baseURL := strings.TrimRight(strings.TrimSpace(provider.BaseURL), "/")
	if baseURL == "" {
		return "", fmt.Errorf("base url is required")
	}
	if !strings.HasPrefix(modelsPath, "/") {
		modelsPath = "/" + modelsPath
	}
	return config.JoinBaseURLAndPath(baseURL, modelsPath), nil
}

func parseProviderModelsResponse(raw []byte, loginProtocol string) ([]providerModelInfo, error) {
	var decoded interface{}
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return nil, err
	}
	models := collectProviderModels(decoded, normalizeLoginProtocol(loginProtocol, ""))
	return dedupeProviderModels(models), nil
}

func collectProviderModels(value interface{}, loginProtocol string) []providerModelInfo {
	switch typed := value.(type) {
	case []interface{}:
		return collectProviderModelsFromList(typed, loginProtocol)
	case map[string]interface{}:
		for _, key := range []string{"data", "models", "items"} {
			if list, ok := typed[key].([]interface{}); ok {
				return collectProviderModelsFromList(list, loginProtocol)
			}
		}
		for _, key := range []string{"result", "response"} {
			if nested, ok := typed[key]; ok {
				if models := collectProviderModels(nested, loginProtocol); len(models) > 0 {
					return models
				}
			}
		}
	}
	return nil
}

func collectProviderModelsFromList(items []interface{}, loginProtocol string) []providerModelInfo {
	models := make([]providerModelInfo, 0, len(items))
	for _, item := range items {
		switch typed := item.(type) {
		case string:
			if id := normalizeProviderModelID(typed, loginProtocol); id != "" {
				models = append(models, providerModelInfo{ID: id, DisplayName: id})
			}
		case map[string]interface{}:
			if model := providerModelInfoFromMap(typed, loginProtocol); model.ID != "" {
				models = append(models, model)
			}
		}
	}
	return models
}

func providerModelInfoFromMap(item map[string]interface{}, loginProtocol string) providerModelInfo {
	id := firstStringField(item, "id", "slug", "name", "model")
	id = normalizeProviderModelID(id, loginProtocol)
	if id == "" {
		return providerModelInfo{}
	}
	displayName := firstStringField(item, "display_name", "displayName", "title", "name")
	if displayName == "" {
		displayName = id
	}
	return providerModelInfo{
		ID:                  id,
		DisplayName:         displayName,
		InputModalities:     firstStringSliceField(item, "input_modalities", "inputModalities", "modalities"),
		ReasoningEfforts:    providerModelReasoningEfforts(item),
		MaxContextTokens:    firstIntField(item, "max_context_tokens", "maxContextTokens", "context_window", "contextWindow", "context_length"),
		SupportsRemoteCodex: firstBoolField(item, "supports_remote_codex", "supportsRemoteCodex"),
		Raw:                 item,
	}
}

func providerModelReasoningEfforts(item map[string]interface{}) []string {
	values := make([]string, 0, 8)
	values = append(values, allStringSliceFields(item, "reasoning_efforts", "reasoningEfforts", "supported_reasoning_efforts")...)
	values = append(values, allStringSliceFields(item, "thinking_efforts", "thinkingEfforts", "supported_thinking_efforts")...)
	return dedupeProviderStringOptions(values)
}

func normalizeProviderModelID(id, loginProtocol string) string {
	id = strings.TrimSpace(id)
	if id == "" {
		return ""
	}
	if normalizeLoginProtocol(loginProtocol, "") == "gemini" {
		id = strings.TrimPrefix(id, "models/")
	}
	return id
}

func dedupeProviderModels(models []providerModelInfo) []providerModelInfo {
	seen := make(map[string]struct{}, len(models))
	out := make([]providerModelInfo, 0, len(models))
	for _, model := range models {
		model.ID = strings.TrimSpace(model.ID)
		if model.ID == "" {
			continue
		}
		key := strings.ToLower(model.ID)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, model)
	}
	return out
}

func providerModelIDs(models []providerModelInfo) []string {
	ids := make([]string, 0, len(models))
	seen := make(map[string]struct{}, len(models))
	for _, model := range models {
		id := strings.TrimSpace(model.ID)
		if id == "" {
			continue
		}
		key := strings.ToLower(id)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		ids = append(ids, id)
	}
	return ids
}

func firstStringField(item map[string]interface{}, keys ...string) string {
	for _, key := range keys {
		if value, ok := item[key].(string); ok && strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func firstStringSliceField(item map[string]interface{}, keys ...string) []string {
	for _, key := range keys {
		if values := stringSliceField(item, key); len(values) > 0 {
			return values
		}
	}
	return nil
}

func allStringSliceFields(item map[string]interface{}, keys ...string) []string {
	values := make([]string, 0)
	for _, key := range keys {
		values = append(values, stringSliceField(item, key)...)
	}
	return values
}

func stringSliceField(item map[string]interface{}, key string) []string {
	values, ok := item[key]
	if !ok {
		return nil
	}
	switch typed := values.(type) {
	case []string:
		return append([]string(nil), typed...)
	case []interface{}:
		out := make([]string, 0, len(typed))
		for _, value := range typed {
			if text, ok := value.(string); ok && strings.TrimSpace(text) != "" {
				out = append(out, strings.TrimSpace(text))
			}
		}
		return out
	default:
		return nil
	}
}

func dedupeProviderStringOptions(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	out := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		key := strings.ToLower(value)
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, value)
	}
	return out
}

func firstIntField(item map[string]interface{}, keys ...string) int {
	for _, key := range keys {
		value, ok := item[key]
		if !ok {
			continue
		}
		switch typed := value.(type) {
		case int:
			return typed
		case float64:
			return int(typed)
		case json.Number:
			n, _ := typed.Int64()
			return int(n)
		}
	}
	return 0
}

func firstBoolField(item map[string]interface{}, keys ...string) bool {
	for _, key := range keys {
		if value, ok := item[key].(bool); ok {
			return value
		}
	}
	return false
}

func responsePreview(raw []byte, limit int) string {
	text := strings.TrimSpace(string(raw))
	if limit > 0 && len(text) > limit {
		return text[:limit] + "..."
	}
	return text
}
