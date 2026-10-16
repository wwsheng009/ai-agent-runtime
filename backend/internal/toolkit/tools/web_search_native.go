package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"strings"

	agentconfig "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
	runtimecfg "github.com/wwsheng009/ai-agent-runtime/internal/config"
)

// ---------------------------------------------------------------------------
// Provider/model-native web search (P2-5b/c).
//
// The "native" chain member routes one search slot through a provider/model
// that declares the native web search capability bit (P2-5a) and speaks an
// Anthropic-compatible Messages API with the server-side web_search_20250305
// tool. DeepSeek's Anthropic-compatible endpoint is the first fixture; any
// provider whose protocol is anthropic can be pinned instead via
// webSearch.native.provider/baseURL. The member joins the chain only when
// webSearch.native.mode enables it; the default ("off") leaves the chain
// untouched.
// ---------------------------------------------------------------------------

const (
	webSearchNativeName       = "native"
	webSearchNativeModeOff    = "off"
	webSearchNativeModePrefer = "prefer"
	webSearchNativeModeForce  = "force"

	defaultNativeMaxUses    = 5
	defaultNativeAPIKeyEnv  = "DEEPSEEK_API_KEY"
	defaultNativeBaseURL    = "https://api.deepseek.com/anthropic/v1"
	defaultNativeAPIVersion = "2023-06-01"
	defaultNativeMaxTokens  = 4096
)

// webSearchNativeConfig is the resolved native-search configuration (runtime
// config + built-in defaults). Zero/absent config fields resolve to defaults,
// so NewWebSearchTool() keeps the historical behavior.
type webSearchNativeConfig struct {
	mode       string
	allow      []string
	provider   string
	model      string
	maxUses    int
	apiKey     string
	apiKeyEnv  string
	baseURL    string
	apiVersion string
	maxTokens  int
}

// enabled reports whether the native member may participate in the chain at all.
func (c webSearchNativeConfig) enabled() bool {
	return c.mode == webSearchNativeModePrefer || c.mode == webSearchNativeModeForce
}

// resolveWebSearchNativeConfig resolves the native-search configuration. An
// unknown mode fails safe to "off": a typo must never silently enable native
// routing.
func resolveWebSearchNativeConfig(cfg *runtimecfg.RuntimeConfig) webSearchNativeConfig {
	resolved := webSearchNativeConfig{
		mode:       webSearchNativeModeOff,
		apiKeyEnv:  defaultNativeAPIKeyEnv,
		apiVersion: defaultNativeAPIVersion,
		maxUses:    defaultNativeMaxUses,
		maxTokens:  defaultNativeMaxTokens,
	}
	if cfg == nil {
		return resolved
	}
	native := cfg.WebSearch.Native
	switch strings.ToLower(strings.TrimSpace(native.Mode)) {
	case webSearchNativeModePrefer:
		resolved.mode = webSearchNativeModePrefer
	case webSearchNativeModeForce:
		resolved.mode = webSearchNativeModeForce
	case "", webSearchNativeModeOff:
		resolved.mode = webSearchNativeModeOff
	}
	for _, pattern := range native.Allow {
		if trimmed := strings.TrimSpace(pattern); trimmed != "" {
			resolved.allow = append(resolved.allow, trimmed)
		}
	}
	resolved.provider = strings.TrimSpace(native.Provider)
	resolved.model = strings.TrimSpace(native.Model)
	if native.MaxUses > 0 {
		resolved.maxUses = native.MaxUses
	}
	if native.MaxTokens > 0 {
		resolved.maxTokens = native.MaxTokens
	}
	resolved.apiKey = strings.TrimSpace(native.APIKey)
	if env := strings.TrimSpace(native.APIKeyEnv); env != "" {
		resolved.apiKeyEnv = env
	}
	resolved.baseURL = strings.TrimSpace(native.BaseURL)
	if version := strings.TrimSpace(native.APIVersion); version != "" {
		resolved.apiVersion = version
	}
	return resolved
}

// webSearchProvidersFromConfig returns the runtime-config chain order (the
// legacy WEB_SEARCH_PROVIDERS env var still wins when set).
func webSearchProvidersFromConfig(cfg *runtimecfg.RuntimeConfig) string {
	if cfg == nil {
		return ""
	}
	return strings.TrimSpace(cfg.WebSearch.Providers)
}

// nativeSearchTarget is the resolved, validated native-search target.
type nativeSearchTarget struct {
	provider string
	model    string
	endpoint string
	apiKey   string
}

// searchProviderUnavailableError marks a native-routing configuration failure
// (mode=force) so the chain classifier can stamp SEARCH_PROVIDER_UNAVAILABLE.
type searchProviderUnavailableError struct{ reason string }

func (e *searchProviderUnavailableError) Error() string {
	return "原生搜索不可用: " + e.reason
}

// resolveNativeSearchTarget resolves the target provider/model, validates the
// capability bit (P2-5a), the allowlist, the endpoint, and the credential. It
// performs local checks only (no network I/O).
func (w *WebSearchTool) resolveNativeSearchTarget() (nativeSearchTarget, error) {
	target := nativeSearchTarget{}
	cfg := w.native
	if !cfg.enabled() {
		return target, fmt.Errorf("webSearch.native.mode 为 off")
	}

	providerName := cfg.provider
	modelName := cfg.model
	global := w.providerConfig()
	if global != nil {
		if global.AICLI != nil && global.AICLI.Chat != nil {
			if providerName == "" {
				providerName = strings.TrimSpace(global.AICLI.Chat.DefaultProvider)
			}
			if modelName == "" {
				modelName = strings.TrimSpace(global.AICLI.Chat.DefaultModel)
			}
		}
		if providerName == "" {
			providerName = strings.TrimSpace(global.Providers.DefaultProvider)
		}
	}
	if providerName == "" {
		return target, fmt.Errorf("未指定目标 provider（webSearch.native.provider 与会话默认均为空）")
	}
	provider := lookupAgentProvider(global, providerName)
	if provider == nil {
		return target, fmt.Errorf("目标 provider %q 未在 agent 配置中注册", providerName)
	}
	if modelName == "" {
		modelName = strings.TrimSpace(provider.DefaultModel)
	}
	if modelName == "" {
		return target, fmt.Errorf("目标 provider %q 没有可用模型（webSearch.native.model 为空且未配置默认模型）", providerName)
	}
	if !nativeAllowMatches(cfg.allow, providerName, modelName) {
		return target, fmt.Errorf("目标 %s/%s 不在 allowlist 中", providerName, modelName)
	}

	capability, ok := agentconfig.ResolveModelCapabilitySpec(modelName, provider.ModelCapabilities)
	if !ok || !capability.EffectiveNativeWebSearch().Supported {
		return target, fmt.Errorf("模型 %q 未声明原生搜索能力（model capability native_tools.web_search / supports_search_tool）", modelName)
	}

	endpoint := cfg.baseURL
	if endpoint == "" && strings.EqualFold(strings.TrimSpace(provider.Protocol), "anthropic") {
		endpoint = strings.TrimSpace(provider.BaseURL)
	}
	if endpoint == "" {
		endpoint = defaultNativeBaseURL
	}
	if parsed, err := url.Parse(endpoint); err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return target, fmt.Errorf("原生搜索端点无效: %q", endpoint)
	}

	apiKey := cfg.apiKey
	if apiKey == "" {
		apiKey = strings.TrimSpace(provider.APIKey)
	}
	if apiKey == "" && cfg.apiKeyEnv != "" {
		apiKey = strings.TrimSpace(os.Getenv(cfg.apiKeyEnv))
	}
	if apiKey == "" {
		return target, fmt.Errorf("缺少凭据（%s 为空且 provider %q 未配置 key）", cfg.apiKeyEnv, providerName)
	}
	if cfg.maxUses <= 0 || cfg.maxTokens <= 0 {
		return target, fmt.Errorf("maxUses/maxTokens 必须为正整数")
	}

	target.provider = providerName
	target.model = modelName
	target.endpoint = endpoint
	target.apiKey = apiKey
	return target, nil
}

// nativeAvailable reports whether the native member should join a prefer-mode
// chain. It never performs network I/O.
func (w *WebSearchTool) nativeAvailable() bool {
	_, err := w.resolveNativeSearchTarget()
	return err == nil
}

// lookupAgentProvider finds a provider entry by name (case-insensitive).
func lookupAgentProvider(cfg *agentconfig.Config, name string) *agentconfig.Provider {
	name = strings.TrimSpace(name)
	if cfg == nil || name == "" {
		return nil
	}
	if provider, ok := cfg.Providers.Items[name]; ok {
		copied := provider
		return &copied
	}
	for key, provider := range cfg.Providers.Items {
		if strings.EqualFold(strings.TrimSpace(key), name) {
			copied := provider
			return &copied
		}
	}
	return nil
}

// nativeAllowMatches matches provider/model glob patterns: "deepseek/*" matches
// "deepseek/<model>", a pattern without "/" matches the provider name alone.
// An empty allowlist does not restrict (capability/credential gates still apply).
func nativeAllowMatches(allow []string, provider, model string) bool {
	if len(allow) == 0 {
		return true
	}
	effective := 0
	for _, pattern := range allow {
		if strings.TrimSpace(pattern) != "" {
			effective++
		}
	}
	if effective == 0 {
		return true
	}
	full := strings.ToLower(provider + "/" + model)
	providerOnly := strings.ToLower(provider)
	for _, pattern := range allow {
		pattern = strings.ToLower(strings.TrimSpace(pattern))
		if pattern == "" {
			continue
		}
		if matched, err := path.Match(pattern, full); err == nil && matched {
			return true
		}
		if !strings.Contains(pattern, "/") {
			if matched, err := path.Match(pattern, providerOnly); err == nil && matched {
				return true
			}
		}
	}
	return false
} // searchNativeProvider runs one search through the native target. Unavailable
// configuration surfaces as searchProviderUnavailableError so the chain can
// classify it as SEARCH_PROVIDER_UNAVAILABLE.
func (w *WebSearchTool) searchNativeProvider(ctx context.Context, query string, count int) (string, []DuckDuckGoResult, error) {
	target, err := w.resolveNativeSearchTarget()
	if err != nil {
		return "", nil, &searchProviderUnavailableError{reason: err.Error()}
	}
	results, err := w.callNativeSearch(ctx, target, query, count)
	if err != nil {
		return "", nil, err
	}
	return webSearchNativeName, results, nil
}

// callNativeSearch performs the Anthropic-compatible Messages request with the
// server-side web_search tool. Redirects are rejected: a 3xx response is an
// error instead of shipping the credential to a different origin.
func (w *WebSearchTool) callNativeSearch(ctx context.Context, target nativeSearchTarget, query string, count int) ([]DuckDuckGoResult, error) {
	if count < 1 {
		count = defaultNativeMaxUses
	}
	endpoint := strings.TrimRight(target.endpoint, "/") + "/messages"
	if err := w.checkURL(endpoint); err != nil {
		return nil, err
	}

	body, err := json.Marshal(nativeAnthropicRequest{
		Model:     target.model,
		MaxTokens: w.native.maxTokens,
		Messages: []nativeAnthropicMessage{{
			Role: "user",
			Content: []nativeAnthropicTextBlock{{
				Type: "text",
				Text: "Perform a web search for the query: " + query,
			}},
		}},
		Tools: []nativeAnthropicTool{{
			Type:    "web_search_20250305",
			Name:    "web_search",
			MaxUses: w.native.maxUses,
		}},
	})
	if err != nil {
		return nil, fmt.Errorf("原生搜索请求编码失败: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	// Official DeepSeek expects x-api-key; an Anthropic-compatible proxy may
	// expect Authorization: Bearer — send both so either resolves.
	req.Header.Set("x-api-key", target.apiKey)
	req.Header.Set("authorization", "Bearer "+target.apiKey)
	req.Header.Set("anthropic-version", w.native.apiVersion)
	req.Header.Set("content-type", "application/json")
	req.Header.Set("accept", "application/json")
	req.Header.Set("user-agent", "ai-agent-runtime/"+webSearchNativeName)

	client := *w.httpClient
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	recordWebSearchFinalHost(ctx, resp)

	payload, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, webSearchHTTPStatusError(resp.StatusCode, resp.Header,
			fmt.Sprintf("原生搜索错误 (状态码 %d): %s", resp.StatusCode, strings.TrimSpace(string(payload))))
	}

	var parsed nativeAnthropicResponse
	if err := json.Unmarshal(payload, &parsed); err != nil {
		return nil, fmt.Errorf("原生搜索响应无法解析: %w", err)
	}
	return mapNativeAnthropicResponse(&parsed, count)
}

// mapNativeAnthropicResponse maps web_search_tool_result blocks to chain
// results, joining each URL to its first text-citation excerpt as the snippet
// (DSH citationSnippets semantics). Absence of any result block is an error —
// strict mode: never scrape prose as a fallback.
func mapNativeAnthropicResponse(resp *nativeAnthropicResponse, count int) ([]DuckDuckGoResult, error) {
	if resp == nil {
		return nil, fmt.Errorf("原生搜索响应为空")
	}
	snippets := make(map[string]string)
	for _, block := range resp.Content {
		if block.Type != "text" {
			continue
		}
		for _, citation := range block.Citations {
			urlText := strings.TrimSpace(citation.URL)
			text := strings.TrimSpace(citation.CitedText)
			if urlText == "" || text == "" {
				continue
			}
			if _, exists := snippets[urlText]; !exists {
				snippets[urlText] = text
			}
		}
	}

	results := make([]DuckDuckGoResult, 0, count)
	seen := make(map[string]struct{})
	sawResultBlock := false
	for _, block := range resp.Content {
		if block.Type != "web_search_tool_result" {
			continue
		}
		sawResultBlock = true
		for _, item := range block.Content {
			if item.Type != "web_search_result" {
				continue
			}
			urlText := strings.TrimSpace(item.URL)
			if urlText == "" {
				continue
			}
			if _, exists := seen[urlText]; exists {
				continue
			}
			seen[urlText] = struct{}{}
			title := strings.TrimSpace(item.Title)
			snippet := snippets[urlText]
			if snippet == "" {
				snippet = title
			}
			results = append(results, DuckDuckGoResult{Title: title, URL: urlText, Snippet: snippet})
			if len(results) >= count {
				break
			}
		}
		if len(results) >= count {
			break
		}
	}
	if !sawResultBlock {
		return nil, fmt.Errorf("原生搜索未返回 web_search_tool_result 块（请求可能未触发原生搜索）")
	}
	return results, nil
}

// nativeAnthropicRequest is the provider-private Messages request subset for
// one auxiliary search (deliberately not shared with the conversation-loop
// adapter, which owns its own wire types).
type nativeAnthropicRequest struct {
	Model     string                   `json:"model"`
	MaxTokens int                      `json:"max_tokens"`
	Messages  []nativeAnthropicMessage `json:"messages"`
	Tools     []nativeAnthropicTool    `json:"tools"`
}

type nativeAnthropicMessage struct {
	Role    string                     `json:"role"`
	Content []nativeAnthropicTextBlock `json:"content"`
}

type nativeAnthropicTextBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type nativeAnthropicTool struct {
	Type    string `json:"type"`
	Name    string `json:"name"`
	MaxUses int    `json:"max_uses,omitempty"`
}

type nativeAnthropicResponse struct {
	Content []nativeAnthropicContentBlock `json:"content"`
}

type nativeAnthropicContentBlock struct {
	Type      string                        `json:"type"`
	Text      string                        `json:"text"`
	Citations []nativeAnthropicCitation     `json:"citations"`
	Content   []nativeAnthropicSearchResult `json:"content"`
}

type nativeAnthropicCitation struct {
	URL       string `json:"url"`
	CitedText string `json:"cited_text"`
}

type nativeAnthropicSearchResult struct {
	Type    string `json:"type"`
	URL     string `json:"url"`
	Title   string `json:"title"`
	PageAge string `json:"page_age"`
}
