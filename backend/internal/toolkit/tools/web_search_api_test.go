package tools

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	runtimecfg "github.com/wwsheng009/ai-agent-runtime/internal/config"
	runtimeerrors "github.com/wwsheng009/ai-agent-runtime/internal/errors"
	runtimeexecutor "github.com/wwsheng009/ai-agent-runtime/internal/executor"
)

// newAPITestTool builds a tool whose chain is exactly `chain`, with the API
// credential environment cleared so each test controls resolution explicitly.
func newAPITestTool(t *testing.T, chain string, env map[string]string, mutate func(*runtimecfg.RuntimeConfig)) *WebSearchTool {
	t.Helper()
	t.Setenv(webSearchProvidersEnv, chain)
	t.Setenv(braveDefaultKeyEnv, "")
	t.Setenv(tavilyDefaultKeyEnv, "")
	for key, value := range env {
		t.Setenv(key, value)
	}
	cfg := &runtimecfg.RuntimeConfig{}
	if mutate != nil {
		mutate(cfg)
	}
	return NewWebSearchToolWithConfig(cfg)
}

func TestWebSearchAPIProviderConfigResolution(t *testing.T) {
	t.Run("literal key wins over env", func(t *testing.T) {
		tool := newAPITestTool(t, "brave", map[string]string{braveDefaultKeyEnv: "env-key"}, func(cfg *runtimecfg.RuntimeConfig) {
			cfg.WebSearch.API.Brave.APIKey = "literal-key"
		})
		if tool.brave.apiKey != "literal-key" || !tool.brave.available() {
			t.Fatalf("brave config = %+v", tool.brave)
		}
		if tool.brave.baseURL != braveDefaultBaseURL || tool.brave.maxResults != braveDefaultMaxResults {
			t.Fatalf("brave defaults = %+v", tool.brave)
		}
	})
	t.Run("env fallback and maxResults override", func(t *testing.T) {
		tool := newAPITestTool(t, "tavily", map[string]string{tavilyDefaultKeyEnv: "env-tavily"}, func(cfg *runtimecfg.RuntimeConfig) {
			cfg.WebSearch.API.Tavily.MaxResults = 3
			cfg.WebSearch.API.Tavily.BaseURL = "https://proxy.example/tavily"
		})
		if tool.tavily.apiKey != "env-tavily" || !tool.tavily.available() {
			t.Fatalf("tavily config = %+v", tool.tavily)
		}
		if tool.tavily.baseURL != "https://proxy.example/tavily" || tool.tavily.maxResults != 3 {
			t.Fatalf("tavily overrides = %+v", tool.tavily)
		}
	})
	t.Run("invalid endpoint is unavailable", func(t *testing.T) {
		tool := newAPITestTool(t, "brave", nil, func(cfg *runtimecfg.RuntimeConfig) {
			cfg.WebSearch.API.Brave.APIKey = "k"
			cfg.WebSearch.API.Brave.BaseURL = "not-a-url"
		})
		if tool.brave.available() {
			t.Fatalf("invalid baseURL must be unavailable")
		}
	})
}

func TestBraveProviderRequestAndMapping(t *testing.T) {
	tool := newAPITestTool(t, "brave", nil, func(cfg *runtimecfg.RuntimeConfig) {
		cfg.WebSearch.API.Brave.APIKey = "brave-key"
	})
	var captured *http.Request
	tool.httpClient = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		captured = req
		body := `{"web":{"results":[
			{"title":"Brave 结果一","url":"https://brave.example/1","description":"摘要 <b>加粗</b> &amp; 实体","page_age":"2026-10-01T00:00:00"},
			{"title":"Brave 结果二","url":"https://brave.example/2","description":"second"},
			{"title":"Brave 结果三","url":"https://brave.example/3","description":"third"}
		]}}`
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body))}, nil
	})}

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"query":      "brave query",
		"count":      float64(2),
		"time_range": "week",
		"language":   "zh-CN",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected success, got: %v", result.Error)
	}
	if result.Metadata["source"] != braveProviderName {
		t.Fatalf("source = %v", result.Metadata["source"])
	}
	if captured == nil || captured.Method != http.MethodGet || captured.URL.Host != "api.search.brave.com" {
		t.Fatalf("request = %#v", captured)
	}
	query := captured.URL.Query()
	if query.Get("q") != "brave query" || query.Get("count") != "2" || query.Get("freshness") != "pw" || query.Get("search_lang") != "zh-CN" {
		t.Fatalf("query params = %v", query.Encode())
	}
	if captured.Header.Get("x-subscription-token") != "brave-key" {
		t.Fatalf("subscription token missing: %#v", captured.Header)
	}
	if !strings.Contains(result.Content, "Brave 结果一") || strings.Contains(result.Content, "Brave 结果三") {
		t.Fatalf("count boundary or content wrong: %q", result.Content)
	}
	if !strings.Contains(result.Content, "摘要 加粗 & 实体") {
		t.Fatalf("snippet cleanup wrong: %q", result.Content)
	}
	if result.Metadata["time_range_supported"] != true || result.Metadata["language_supported"] != true {
		t.Fatalf("brave capability flags = %#v", result.Metadata)
	}
	sources, _ := result.Metadata["sources"].([]map[string]interface{})
	if len(sources) != 2 || sources[0]["published_at"] != "2026-10-01T00:00:00" {
		t.Fatalf("sources projection = %#v", result.Metadata["sources"])
	}
}

func TestTavilyProviderRequestAndMapping(t *testing.T) {
	tool := newAPITestTool(t, "tavily", nil, func(cfg *runtimecfg.RuntimeConfig) {
		cfg.WebSearch.API.Tavily.APIKey = "tavily-key"
	})
	var captured *http.Request
	var capturedBody string
	tool.httpClient = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		captured = req
		body, _ := io.ReadAll(req.Body)
		capturedBody = string(body)
		payload := `{"results":[{"title":"Tavily 结果","url":"https://tavily.example/1","content":"内容 <p>段落</p> &amp; 更多","published_date":"2026-09-30"}]}`
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(payload))}, nil
	})}

	result, err := tool.Execute(context.Background(), map[string]interface{}{"query": "tavily query"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success || result.Metadata["source"] != tavilyProviderName {
		t.Fatalf("tavily result = %+v / %v", result.Metadata, result.Error)
	}
	if captured == nil || captured.Method != http.MethodPost || captured.URL.Path != "/search" {
		t.Fatalf("request = %#v", captured)
	}
	if captured.Header.Get("authorization") != "Bearer tavily-key" {
		t.Fatalf("auth header = %#v", captured.Header)
	}
	if !strings.Contains(capturedBody, `"max_results":5`) || !strings.Contains(capturedBody, `"include_answer":false`) {
		t.Fatalf("body = %s", capturedBody)
	}
	if !strings.Contains(result.Content, "段落") || !strings.Contains(result.Content, "更多") || strings.Contains(result.Content, "<p>") {
		t.Fatalf("snippet cleanup wrong: %q", result.Content)
	}
	sources, _ := result.Metadata["sources"].([]map[string]interface{})
	if len(sources) != 1 || sources[0]["published_at"] != "2026-09-30" {
		t.Fatalf("sources projection = %#v", result.Metadata["sources"])
	}
}

func TestWebSearchAPIProviderUnavailableYieldsStructuredError(t *testing.T) {
	tool := newAPITestTool(t, "brave,tavily", nil, nil)
	if len(tool.providers) != 0 {
		t.Fatalf("providers = %d, want none without credentials", len(tool.providers))
	}
	result, err := tool.Execute(context.Background(), map[string]interface{}{"query": "no-backend"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Success {
		t.Fatalf("empty chain must fail")
	}
	if got := result.Metadata["error_code"]; got != string(runtimeerrors.ErrSearchProviderUnavailable) {
		t.Fatalf("error_code = %v, want SEARCH_PROVIDER_UNAVAILABLE", got)
	}
}

func TestBraveProviderRespectsSandboxNetworkPolicy(t *testing.T) {
	tool := newAPITestTool(t, "brave", nil, func(cfg *runtimecfg.RuntimeConfig) {
		cfg.WebSearch.API.Brave.APIKey = "brave-key"
	})
	tool.SetSandbox(runtimeexecutor.NewSandbox(&runtimeexecutor.SandboxConfig{DeniedHosts: []string{"api.search.brave.com"}}))

	result, err := tool.Execute(context.Background(), map[string]interface{}{"query": "blocked"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Success {
		t.Fatal("expected sandbox denial")
	}
	if result.Error == nil || !strings.Contains(strings.ToLower(result.Error.Error()), "sandbox") {
		t.Fatalf("expected sandbox denial, got: %v", result.Error)
	}
}

func TestBraveProviderRateLimitCarriesRetryAfter(t *testing.T) {
	tool := newAPITestTool(t, "brave", nil, func(cfg *runtimecfg.RuntimeConfig) {
		cfg.WebSearch.API.Brave.APIKey = "brave-key"
	})
	tool.httpClient = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusTooManyRequests,
			Header:     http.Header{"Retry-After": []string{"9"}},
			Body:       io.NopCloser(strings.NewReader(`{"error":"rate limited"}`)),
		}, nil
	})}

	result, err := tool.Execute(context.Background(), map[string]interface{}{"query": "limited"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Success {
		t.Fatalf("expected rate-limit failure")
	}
	if got := result.Metadata["error_code"]; got != string(runtimeerrors.ErrAPIRateLimit) {
		t.Fatalf("error_code = %v", got)
	}
	if result.Metadata["failure_provider"] != braveProviderName || result.Metadata["retry_after_ms"] != int64(9000) {
		t.Fatalf("failure metadata = %#v", result.Metadata)
	}
}
