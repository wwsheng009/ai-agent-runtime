package memoryruntime

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
	"github.com/wwsheng009/ai-agent-runtime/internal/llm"
)

type liveMemoryCall struct {
	Stage, Model string
	Elapsed      time.Duration
	Usage        TokenUsage
	Succeeded    bool
}

type liveMemoryWireRequest struct {
	Model     string `json:"model"`
	Reasoning struct {
		Effort string `json:"effort"`
	} `json:"reasoning"`
}

// This wraps the real provider, not a fake response. Only routing/usage fields
// are retained; credentials, HTTP headers, raw bodies and reasoning are not.
type liveMemoryProvider struct {
	llm.Provider
	t                       *testing.T
	provider, model, effort string
	mu                      sync.Mutex
	calls                   []liveMemoryCall
	requests                []liveMemoryWireRequest
	statuses                []int
	wireErrors              []string
}

func newLiveMemoryLLM(t *testing.T) (*llm.LLMRuntime, *liveMemoryProvider) {
	t.Helper()
	if os.Getenv("AICLI_MEMORY_LIVE") != "1" {
		t.Skip("set AICLI_MEMORY_LIVE=1 and explicit CONFIG/PROVIDER/MODEL/REASONING to call a paid real service")
	}
	env := func(name string) string {
		value := strings.TrimSpace(os.Getenv("AICLI_MEMORY_LIVE_" + name))
		require.NotEmpty(t, value, "explicit live-test setting %s is required", name)
		return value
	}
	configPath, providerName, model, effort := env("CONFIG"), env("PROVIDER"), env("MODEL"), env("REASONING")
	_, err := os.Stat(configPath)
	require.NoError(t, err)
	manager, err := agentconfig.NewManager(configPath)
	require.NoError(t, err)
	cfg := manager.Config()
	configured, ok := cfg.Providers.Items[providerName]
	require.True(t, ok, "requested provider must exist; no provider fallback")
	require.True(t, configured.Enabled)
	require.Equal(t, "codex", configured.GetType(), "this wire assertion covers the Codex Responses protocol")
	require.Contains(t, configured.SupportedModels, model)
	require.Contains(t, configured.ModelCapabilities[model].ReasoningEfforts, effort)
	key := configured.GetAPIKey()
	require.NotEmpty(t, key, "configured provider credential could not be resolved")
	realProvider, err := llm.NewProvider(&llm.ProviderConfig{
		Name: providerName, Type: configured.GetType(), APIKey: key,
		BaseURL: configured.BaseURL, APIPath: configured.APIPath,
		CompatibilityProfile: configured.Compatibility.Profile,
		DefaultModel:         model, SupportedModels: []string{model},
		ModelMappings: configured.ModelMappings, ModelCapabilities: configured.ModelCapabilities,
		Headers:        agentconfig.EffectiveProviderHeaders(cfg.Providers.Headers, configured.Headers),
		HeaderMappings: configured.HeaderMappings,
		Proxy:          agentconfig.EffectiveProxyConfig(&cfg.Providers.Proxy, configured.Proxy),
		Timeout:        2 * time.Minute, ResponseHeaderTimeout: 2 * time.Minute,
		// A live acceptance failure must be visible, not hidden by many paid retries.
		MaxRetries: 0, MaxTransportRetries: 0,
	})
	require.NoError(t, err)
	audit := &liveMemoryProvider{
		Provider: realProvider, t: t, provider: providerName, model: model, effort: effort,
	}
	runtime := llm.NewLLMRuntime(&llm.RuntimeConfig{
		DefaultProvider: providerName, DefaultModel: model, DefaultTimeout: 2 * time.Minute, MaxRetries: 0,
	})
	require.NoError(t, runtime.RegisterProvider(providerName, audit))
	t.Logf("LIVE route provider=%s protocol=codex model=%s reasoning=%s", providerName, model, effort)
	return runtime, audit
}

func (p *liveMemoryProvider) Call(ctx context.Context, request *llm.LLMRequest) (*llm.LLMResponse, error) {
	if request.Provider != p.provider || request.Model != p.model || request.ReasoningEffort != p.effort {
		return nil, fmt.Errorf("live memory request changed the explicitly selected provider/model/reasoning")
	}
	stage := ""
	for _, name := range []string{"memory_extraction", "memory_turn_window", "memory_project_summary"} {
		if enabled, _ := request.Metadata[name].(bool); enabled {
			stage = name
			break
		}
	}
	if stage == "" {
		return nil, fmt.Errorf("unexpected non-memory request in live memory test")
	}
	ctx = llm.WithHTTPDebugReporter(ctx, func(event llm.HTTPDebugEvent) {
		p.mu.Lock()
		defer p.mu.Unlock()
		if event.Phase == "request" {
			body := event.RequestBodyRaw
			if len(body) == 0 {
				body = []byte(event.RequestBody)
			}
			var wire liveMemoryWireRequest
			if err := json.Unmarshal(body, &wire); err != nil {
				p.wireErrors = append(p.wireErrors, "cannot decode outbound request routing fields")
			} else {
				p.requests = append(p.requests, wire)
			}
		}
		if event.Phase == "response" && event.ResponseStatusCode != 0 {
			p.statuses = append(p.statuses, event.ResponseStatusCode)
		}
	})
	start := time.Now()
	response, err := p.Provider.Call(ctx, request)
	call := liveMemoryCall{Stage: stage, Elapsed: time.Since(start), Succeeded: err == nil}
	if response != nil {
		call.Model = response.Model
		if response.Usage != nil {
			call.Usage = TokenUsage{
				PromptTokens: response.Usage.PromptTokens, CompletionTokens: response.Usage.CompletionTokens,
				TotalTokens: response.Usage.TotalTokens,
			}
		}
	}
	p.mu.Lock()
	p.calls = append(p.calls, call)
	p.mu.Unlock()
	p.t.Logf("LIVE stage=%s response_model=%s success=%t elapsed=%s usage=%+v",
		stage, call.Model, call.Succeeded, call.Elapsed.Round(time.Millisecond), call.Usage)
	return response, err
}

func (p *liveMemoryProvider) verify(t *testing.T) {
	t.Helper()
	p.mu.Lock()
	defer p.mu.Unlock()
	require.Empty(t, p.wireErrors)
	require.Len(t, p.calls, 4, "two canonical pages, one L1 window, one project summary")
	require.Len(t, p.requests, len(p.calls), "each logical call must reach the real HTTP adapter exactly once")
	require.Len(t, p.statuses, len(p.calls))
	stages := map[string]int{}
	for _, call := range p.calls {
		require.True(t, call.Succeeded)
		require.Equal(t, p.model, call.Model)
		require.Positive(t, call.Usage.TotalTokens)
		stages[call.Stage]++
	}
	require.Equal(t, map[string]int{"memory_extraction": 2, "memory_turn_window": 1, "memory_project_summary": 1}, stages)
	for _, request := range p.requests {
		require.Equal(t, p.model, request.Model)
		require.Equal(t, p.effort, request.Reasoning.Effort, "verify the wire effort, not merely the configured value")
	}
	for _, status := range p.statuses {
		require.Equal(t, 200, status)
	}
	t.Logf("LIVE wire verified: requests=%d HTTP=200 model=%s reasoning=%s", len(p.requests), p.model, p.effort)
}
