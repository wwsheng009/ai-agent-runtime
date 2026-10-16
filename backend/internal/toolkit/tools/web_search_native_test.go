package tools

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	agentconfig "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
	runtimecfg "github.com/wwsheng009/ai-agent-runtime/internal/config"
	runtimeerrors "github.com/wwsheng009/ai-agent-runtime/internal/errors"
	runtimeexecutor "github.com/wwsheng009/ai-agent-runtime/internal/executor"
)

// nativeSearchProviderConfig builds a global agent config whose default chat
// route is a provider that (optionally) declares the native web search
// capability bit for its default model.
func nativeSearchProviderConfig(withCapability bool) *agentconfig.Config {
	spec := agentconfig.ModelCapabilitySpec{}
	if withCapability {
		spec.NativeTools = agentconfig.NativeToolCapabilities{
			WebSearch: &agentconfig.NativeWebSearchCapability{Supported: true, ToolType: "text_and_image"},
		}
	}
	return &agentconfig.Config{
		AICLI: &agentconfig.AICLIConfig{
			Chat: &agentconfig.AICLIChatConfig{DefaultProvider: "deepseek", DefaultModel: "deepseek-chat"},
		},
		Providers: agentconfig.ProvidersConfig{
			Items: map[string]agentconfig.Provider{
				"deepseek": {
					Protocol:     "openai",
					APIKey:       "provider-key",
					DefaultModel: "deepseek-chat",
					ModelCapabilities: map[string]agentconfig.ModelCapabilitySpec{
						"deepseek-chat": spec,
					},
				},
			},
		},
	}
}

func nativeSearchConfig(mode, providers string, mutate func(*runtimecfg.NativeWebSearchConfig)) *runtimecfg.RuntimeConfig {
	cfg := &runtimecfg.RuntimeConfig{}
	cfg.WebSearch.Providers = providers
	cfg.WebSearch.Native.Mode = mode
	if mutate != nil {
		mutate(&cfg.WebSearch.Native)
	}
	return cfg
}

func newNativeSearchToolForTest(t *testing.T, cfg *runtimecfg.RuntimeConfig, providerCfg *agentconfig.Config) *WebSearchTool {
	t.Helper()
	t.Setenv(webSearchProvidersEnv, "")
	if cfg == nil {
		cfg = &runtimecfg.RuntimeConfig{}
	}
	tool := NewWebSearchToolWithConfig(cfg)
	if providerCfg != nil {
		tool.providerConfig = func() *agentconfig.Config { return providerCfg }
	}
	tool.providers = tool.buildProviders()
	return tool
}

func providerNames(tool *WebSearchTool) string {
	names := make([]string, 0, len(tool.providers))
	for _, provider := range tool.providers {
		names = append(names, provider.name)
	}
	return strings.Join(names, ",")
}

func TestWebSearchNativeChainGating(t *testing.T) {
	t.Run("mode off keeps chain untouched", func(t *testing.T) {
		tool := newNativeSearchToolForTest(t, nativeSearchConfig("", "native,bing", nil), nativeSearchProviderConfig(true))
		if got := providerNames(tool); got != "bing" {
			t.Fatalf("providers = %q, want native removed with mode=off", got)
		}
	})
	t.Run("prefer includes available native in place", func(t *testing.T) {
		tool := newNativeSearchToolForTest(t, nativeSearchConfig("prefer", "native,bing", nil), nativeSearchProviderConfig(true))
		if got := providerNames(tool); got != "native,bing" {
			t.Fatalf("providers = %q, want native first", got)
		}
	})
	t.Run("prefer without capability skips native", func(t *testing.T) {
		tool := newNativeSearchToolForTest(t, nativeSearchConfig("prefer", "native,bing", nil), nativeSearchProviderConfig(false))
		if got := providerNames(tool); got != "bing" {
			t.Fatalf("providers = %q, want native skipped without capability", got)
		}
	})
	t.Run("prefer with allowlist mismatch skips native", func(t *testing.T) {
		cfg := nativeSearchConfig("prefer", "native,bing", func(native *runtimecfg.NativeWebSearchConfig) {
			native.Allow = []string{"other/*"}
		})
		tool := newNativeSearchToolForTest(t, cfg, nativeSearchProviderConfig(true))
		if got := providerNames(tool); got != "bing" {
			t.Fatalf("providers = %q, want native skipped by allowlist", got)
		}
	})
	t.Run("force keeps native even when unavailable", func(t *testing.T) {
		tool := newNativeSearchToolForTest(t, nativeSearchConfig("force", "native", nil), &agentconfig.Config{})
		if got := providerNames(tool); got != "native" {
			t.Fatalf("providers = %q, want force to keep native", got)
		}
	})
	t.Run("explicit mode without native token prepends native", func(t *testing.T) {
		tool := newNativeSearchToolForTest(t, nativeSearchConfig("prefer", "bing", nil), nativeSearchProviderConfig(true))
		if got := providerNames(tool); got != "native,bing" {
			t.Fatalf("providers = %q, want native prepended", got)
		}
	})
}

func TestWebSearchNativeRequestAndMapping(t *testing.T) {
	tool := newNativeSearchToolForTest(t, nativeSearchConfig("prefer", "native", nil), nativeSearchProviderConfig(true))
	var captured *http.Request
	var capturedBody string
	tool.httpClient = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		captured = req
		body, _ := io.ReadAll(req.Body)
		capturedBody = string(body)
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body: io.NopCloser(strings.NewReader(`{"content":[
				{"type":"text","citations":[
					{"url":"https://a.example/1","cited_text":"引用A"},
					{"url":"https://b.example/2","cited_text":"引用B"}
				]},
				{"type":"web_search_tool_result","content":[
					{"type":"web_search_result","url":"https://a.example/1","title":"标题A"},
					{"type":"web_search_result","url":"https://b.example/2","title":"标题B"},
					{"type":"web_search_result","url":"https://a.example/1","title":"重复A"},
					{"type":"web_search_result","url":"https://c.example/3","title":"标题C"}
				]}
			]}`)),
		}, nil
	})}

	result, err := tool.Execute(context.Background(), map[string]interface{}{"query": "golang", "count": float64(2)})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected success, got error: %v", result.Error)
	}
	if result.Metadata["source"] != "native" {
		t.Fatalf("source = %v, want native", result.Metadata["source"])
	}
	if !strings.Contains(result.Content, "标题A") || !strings.Contains(result.Content, "引用A") {
		t.Fatalf("mapped content missing title/snippet: %q", result.Content)
	}
	if strings.Contains(result.Content, "标题C") {
		t.Fatalf("count cap not honored: %q", result.Content)
	}
	if captured == nil || captured.Method != http.MethodPost {
		t.Fatalf("expected a POST request, got %#v", captured)
	}
	if captured.URL.String() != "https://api.deepseek.com/anthropic/v1/messages" {
		t.Fatalf("endpoint = %q", captured.URL.String())
	}
	if captured.Header.Get("x-api-key") != "provider-key" || !strings.HasPrefix(captured.Header.Get("authorization"), "Bearer ") {
		t.Fatalf("auth headers missing: %#v", captured.Header)
	}
	if captured.Header.Get("anthropic-version") != "2023-06-01" {
		t.Fatalf("anthropic-version = %q", captured.Header.Get("anthropic-version"))
	}
	if !strings.Contains(capturedBody, `"web_search_20250305"`) || !strings.Contains(capturedBody, `"max_uses":5`) {
		t.Fatalf("native tool declaration missing from body: %s", capturedBody)
	}
	if !strings.Contains(capturedBody, "golang") {
		t.Fatalf("query missing from body: %s", capturedBody)
	}
}

func TestWebSearchNativeStrictNoResultBlock(t *testing.T) {
	tool := newNativeSearchToolForTest(t, nativeSearchConfig("prefer", "native", nil), nativeSearchProviderConfig(true))
	tool.httpClient = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(strings.NewReader(`{"content":[{"type":"text","text":"我认为答案是……"}]}`)),
		}, nil
	})}

	result, err := tool.Execute(context.Background(), map[string]interface{}{"query": "no-result-block"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Success {
		t.Fatalf("strict mode must fail without web_search_tool_result blocks")
	}
	if result.Error == nil || !strings.Contains(result.Error.Error(), "web_search_tool_result") {
		t.Fatalf("error should name the missing result block: %v", result.Error)
	}
}

func TestWebSearchNativeForceUnavailableIsStructured(t *testing.T) {
	tool := newNativeSearchToolForTest(t, nativeSearchConfig("force", "native", nil), &agentconfig.Config{})
	result, err := tool.Execute(context.Background(), map[string]interface{}{"query": "forced"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Success {
		t.Fatalf("force with no usable target must fail")
	}
	if got := result.Metadata["error_code"]; got != string(runtimeerrors.ErrSearchProviderUnavailable) {
		t.Fatalf("error_code = %v, want SEARCH_PROVIDER_UNAVAILABLE", got)
	}
	next, _ := result.Metadata["next_action"].(string)
	if !strings.Contains(next, "mode=force") {
		t.Fatalf("next_action should mention mode=force: %q", next)
	}
}

func TestWebSearchNativeFailureFallsBackToNextProvider(t *testing.T) {
	tool := newNativeSearchToolForTest(t, nativeSearchConfig("prefer", "native,bing", nil), nativeSearchProviderConfig(true))
	tool.httpClient = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		switch {
		case strings.Contains(req.URL.Host, "api.deepseek.com"):
			return &http.Response{StatusCode: http.StatusInternalServerError, Body: io.NopCloser(strings.NewReader("upstream exploded"))}, nil
		case req.URL.Host == "www.bing.com":
			body := `<ol id="b_results"><li class="b_algo"><h2><a href="https://example.com/page">回退标题</a></h2><p>回退摘要</p></li></ol>`
			return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body))}, nil
		default:
			t.Fatalf("unexpected host %s", req.URL.Host)
			return nil, nil
		}
	})}

	result, err := tool.Execute(context.Background(), map[string]interface{}{"query": "fallback"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Fatalf("fallback chain should succeed, got: %v", result.Error)
	}
	if result.Metadata["source"] != "bing" {
		t.Fatalf("source = %v, want bing after native failure", result.Metadata["source"])
	}
	if !strings.Contains(result.Content, "回退标题") {
		t.Fatalf("fallback content missing: %q", result.Content)
	}
	providerErrors, _ := result.Metadata["provider_errors"].(map[string]string)
	if providerErrors["native"] == "" {
		t.Fatalf("provider_errors should record the native failure: %#v", result.Metadata["provider_errors"])
	}
}

func TestWebSearchNativeRespectsSandboxNetworkPolicy(t *testing.T) {
	sandbox := runtimeexecutor.NewSandbox(&runtimeexecutor.SandboxConfig{DeniedHosts: []string{"api.deepseek.com"}})
	tool := newNativeSearchToolForTest(t, nativeSearchConfig("prefer", "native", nil), nativeSearchProviderConfig(true))
	tool.SetSandbox(sandbox)

	result, err := tool.Execute(context.Background(), map[string]interface{}{"query": "blocked"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Success {
		t.Fatal("expected native search to be denied by sandbox network policy")
	}
	if result.Error == nil || !strings.Contains(strings.ToLower(result.Error.Error()), "sandbox") {
		t.Fatalf("expected sandbox denial, got: %v", result.Error)
	}
}

func TestNativeAllowMatches(t *testing.T) {
	cases := []struct {
		name     string
		allow    []string
		provider string
		model    string
		want     bool
	}{
		{name: "empty allows all", allow: nil, provider: "deepseek", model: "m", want: true},
		{name: "provider glob", allow: []string{"deepseek/*"}, provider: "deepseek", model: "deepseek-chat", want: true},
		{name: "provider glob mismatch", allow: []string{"other/*"}, provider: "deepseek", model: "deepseek-chat", want: false},
		{name: "provider-only pattern", allow: []string{"DeepSeek"}, provider: "deepseek", model: "m", want: true},
		{name: "blank patterns are unrestricted", allow: []string{"  "}, provider: "deepseek", model: "m", want: true},
		{name: "invalid glob is ignored", allow: []string{"["}, provider: "deepseek", model: "m", want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := nativeAllowMatches(tc.allow, tc.provider, tc.model); got != tc.want {
				t.Fatalf("nativeAllowMatches(%v, %s, %s) = %v, want %v", tc.allow, tc.provider, tc.model, got, tc.want)
			}
		})
	}
}
