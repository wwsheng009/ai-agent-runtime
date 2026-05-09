package llm

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/wwsheng009/ai-agent-runtime/internal/types"
)

type captureProvider struct {
	name    string
	lastReq *LLMRequest
}

func (p *captureProvider) Name() string { return p.name }

func (p *captureProvider) Call(ctx context.Context, req *LLMRequest) (*LLMResponse, error) {
	cloned := *req
	p.lastReq = &cloned
	return &LLMResponse{
		Content: "provider:" + p.name,
		Model:   req.Model,
	}, nil
}

func (p *captureProvider) Stream(ctx context.Context, req *LLMRequest) (<-chan StreamChunk, error) {
	ch := make(chan StreamChunk, 1)
	ch <- StreamChunk{Type: EventTypeDone, Done: true}
	close(ch)
	return ch, nil
}

func (p *captureProvider) CountTokens(text string) int { return len(text) }

func (p *captureProvider) GetCapabilities() *ModelCapabilities {
	return &ModelCapabilities{SupportsStreaming: true}
}

func (p *captureProvider) CheckHealth(ctx context.Context) error { return nil }

type flakyProvider struct {
	name  string
	errs  []error
	resp  *LLMResponse
	calls int
}

func (p *flakyProvider) Name() string { return p.name }

func (p *flakyProvider) Call(ctx context.Context, req *LLMRequest) (*LLMResponse, error) {
	p.calls++
	if index := p.calls - 1; index < len(p.errs) && p.errs[index] != nil {
		return nil, p.errs[index]
	}
	if p.resp != nil {
		return p.resp, nil
	}
	return &LLMResponse{
		Content: "provider:" + p.name,
		Model:   req.Model,
	}, nil
}

func (p *flakyProvider) Stream(ctx context.Context, req *LLMRequest) (<-chan StreamChunk, error) {
	ch := make(chan StreamChunk, 1)
	ch <- StreamChunk{Type: EventTypeDone, Done: true}
	close(ch)
	return ch, nil
}

func (p *flakyProvider) CountTokens(text string) int { return len(text) }

func (p *flakyProvider) GetCapabilities() *ModelCapabilities {
	return &ModelCapabilities{SupportsStreaming: true}
}

func (p *flakyProvider) CheckHealth(ctx context.Context) error { return nil }

type debugFlakyProvider struct {
	name  string
	errs  []error
	resp  *LLMResponse
	calls int
}

func (p *debugFlakyProvider) Name() string { return p.name }

func (p *debugFlakyProvider) Call(ctx context.Context, req *LLMRequest) (*LLMResponse, error) {
	p.calls++
	reportHTTPDebug(ctx, HTTPDebugEvent{
		Source: "test_provider",
		Phase:  "response",
		Model:  req.Model,
		Error:  fmt.Sprintf("call-%d", p.calls),
	})
	if index := p.calls - 1; index < len(p.errs) && p.errs[index] != nil {
		return nil, p.errs[index]
	}
	if p.resp != nil {
		return p.resp, nil
	}
	return &LLMResponse{
		Content: "provider:" + p.name,
		Model:   req.Model,
	}, nil
}

func (p *debugFlakyProvider) Stream(ctx context.Context, req *LLMRequest) (<-chan StreamChunk, error) {
	ch := make(chan StreamChunk, 1)
	ch <- StreamChunk{Type: EventTypeDone, Done: true}
	close(ch)
	return ch, nil
}

func (p *debugFlakyProvider) CountTokens(text string) int { return len(text) }

func (p *debugFlakyProvider) GetCapabilities() *ModelCapabilities {
	return &ModelCapabilities{SupportsStreaming: true}
}

func (p *debugFlakyProvider) CheckHealth(ctx context.Context) error { return nil }

type streamSequenceProvider struct {
	name       string
	streams    [][]StreamChunk
	streamErrs []error
	calls      int
}

func (p *streamSequenceProvider) Name() string { return p.name }

func (p *streamSequenceProvider) Call(ctx context.Context, req *LLMRequest) (*LLMResponse, error) {
	return &LLMResponse{Content: "provider:" + p.name, Model: req.Model}, nil
}

func (p *streamSequenceProvider) Stream(ctx context.Context, req *LLMRequest) (<-chan StreamChunk, error) {
	p.calls++
	index := p.calls - 1
	if index < len(p.streamErrs) && p.streamErrs[index] != nil {
		return nil, p.streamErrs[index]
	}

	var chunks []StreamChunk
	if index < len(p.streams) {
		chunks = p.streams[index]
	}
	ch := make(chan StreamChunk, len(chunks))
	for _, chunk := range chunks {
		ch <- chunk
	}
	close(ch)
	return ch, nil
}

func (p *streamSequenceProvider) CountTokens(text string) int { return len(text) }

func (p *streamSequenceProvider) GetCapabilities() *ModelCapabilities {
	return &ModelCapabilities{SupportsStreaming: true}
}

func (p *streamSequenceProvider) CheckHealth(ctx context.Context) error { return nil }

func TestLLMRuntime_Call_UsesExplicitProviderAndPreservesModel(t *testing.T) {
	runtime := NewLLMRuntime(&RuntimeConfig{
		DefaultProvider: "provider-a",
		DefaultModel:    "model-a",
		MaxRetries:      0,
	})
	providerA := &captureProvider{name: "provider-a"}
	providerB := &captureProvider{name: "provider-b"}
	require.NoError(t, runtime.RegisterProvider("provider-a", providerA))
	require.NoError(t, runtime.RegisterProvider("provider-b", providerB))

	resp, err := runtime.Call(context.Background(), &LLMRequest{
		Provider: "provider-b",
		Model:    "model-b",
		Messages: []types.Message{{Role: "user", Content: "hello"}},
	})
	require.NoError(t, err)
	require.NotNil(t, resp)
	assert.Equal(t, "provider:provider-b", resp.Content)
	require.NotNil(t, providerB.lastReq)
	assert.Equal(t, "provider-b", providerB.lastReq.Provider)
	assert.Equal(t, "model-b", providerB.lastReq.Model)
	assert.Nil(t, providerA.lastReq)
}

func TestLLMRuntime_Call_UsesDefaultProviderWhenRequestOmitsProvider(t *testing.T) {
	runtime := NewLLMRuntime(&RuntimeConfig{
		DefaultProvider: "provider-b",
		DefaultModel:    "model-b",
		MaxRetries:      0,
	})
	providerA := &captureProvider{name: "provider-a"}
	providerB := &captureProvider{name: "provider-b"}
	require.NoError(t, runtime.RegisterProvider("provider-a", providerA))
	require.NoError(t, runtime.RegisterProvider("provider-b", providerB))

	resp, err := runtime.Call(context.Background(), &LLMRequest{
		Messages: []types.Message{{Role: "user", Content: "hello"}},
	})
	require.NoError(t, err)
	require.NotNil(t, resp)
	assert.Equal(t, "provider:provider-b", resp.Content)
	require.NotNil(t, providerB.lastReq)
	assert.Equal(t, "provider-b", providerB.lastReq.Provider)
	assert.Equal(t, "model-b", providerB.lastReq.Model)
	assert.Nil(t, providerA.lastReq)
}

func TestLLMRuntime_Call_DoesNotRetryMissingRequiredParameterErrors(t *testing.T) {
	runtime := NewLLMRuntime(&RuntimeConfig{
		DefaultProvider: "provider-a",
		DefaultModel:    "gpt-5.4-mini",
		MaxRetries:      3,
	})
	provider := &flakyProvider{
		name: "provider-a",
		errs: []error{
			fmt.Errorf("failed to handle stream response: codex response failed: Missing required parameter: 'input[11].summary'."),
		},
	}
	require.NoError(t, runtime.RegisterProvider("provider-a", provider))

	_, err := runtime.Call(context.Background(), &LLMRequest{
		Messages: []types.Message{{Role: "user", Content: "hello"}},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "Missing required parameter")
	assert.Equal(t, 1, provider.calls)
}

func TestLLMRuntime_Call_RetriesRetryableProviderErrors(t *testing.T) {
	runtime := NewLLMRuntime(&RuntimeConfig{
		DefaultProvider: "provider-a",
		DefaultModel:    "gpt-5.4-mini",
		MaxRetries:      3,
	})
	provider := &flakyProvider{
		name: "provider-a",
		errs: []error{
			fmt.Errorf("HTTP 500: {\"error\":{\"message\":\"temporary upstream failure\"}}"),
			fmt.Errorf("HTTP 500: {\"error\":{\"message\":\"temporary upstream failure\"}}"),
		},
		resp: &LLMResponse{
			Content: "recovered",
			Model:   "gpt-5.4-mini",
		},
	}
	require.NoError(t, runtime.RegisterProvider("provider-a", provider))

	resp, err := runtime.Call(context.Background(), &LLMRequest{
		Messages: []types.Message{{Role: "user", Content: "hello"}},
	})
	require.NoError(t, err)
	require.NotNil(t, resp)
	assert.Equal(t, "recovered", resp.Content)
	assert.Equal(t, 3, provider.calls)
}

func TestLLMRuntime_Call_ReportsRetryDebugEventAndPropagatesAttemptContext(t *testing.T) {
	runtime := NewLLMRuntime(&RuntimeConfig{
		DefaultProvider: "provider-a",
		DefaultModel:    "gpt-5.4-mini",
		MaxRetries:      2,
		RetryTuning: RetryTuning{
			BaseDelay: 10 * time.Millisecond,
		},
	})
	provider := &debugFlakyProvider{
		name: "provider-a",
		errs: []error{
			fmt.Errorf("HTTP 500: {\"error\":{\"message\":\"temporary upstream failure\"}}"),
		},
		resp: &LLMResponse{
			Content: "recovered",
			Model:   "gpt-5.4-mini",
		},
	}
	require.NoError(t, runtime.RegisterProvider("provider-a", provider))

	var events []HTTPDebugEvent
	ctx := WithHTTPDebugReporter(context.Background(), func(event HTTPDebugEvent) {
		events = append(events, event)
	})
	var retryEvents []RetryEvent
	ctx = WithRetryEventReporter(ctx, func(event RetryEvent) {
		retryEvents = append(retryEvents, event)
	})
	resp, err := runtime.Call(ctx, &LLMRequest{
		Messages: []types.Message{{Role: "user", Content: "hello"}},
	})
	require.NoError(t, err)
	require.NotNil(t, resp)
	assert.Equal(t, "recovered", resp.Content)
	assert.Equal(t, 2, provider.calls)

	require.Len(t, events, 3)
	assert.Equal(t, "test_provider", events[0].Source)
	assert.Equal(t, "response", events[0].Phase)
	assert.Equal(t, 1, events[0].Attempt)
	assert.Equal(t, 3, events[0].MaxAttempts)
	assert.Equal(t, "llm_runtime", events[1].Source)
	assert.Equal(t, "retry", events[1].Phase)
	assert.Equal(t, "provider-a", events[1].Provider)
	assert.Equal(t, "gpt-5.4-mini", events[1].Model)
	assert.Equal(t, 1, events[1].Attempt)
	assert.Equal(t, 3, events[1].MaxAttempts)
	assert.Equal(t, "http_500", events[1].RetryReason)
	assert.GreaterOrEqual(t, events[1].RetryDelayMS, int64(10))
	assert.Equal(t, "test_provider", events[2].Source)
	assert.Equal(t, "response", events[2].Phase)
	assert.Equal(t, 2, events[2].Attempt)
	assert.Equal(t, 3, events[2].MaxAttempts)

	require.Len(t, retryEvents, 1)
	assert.Equal(t, "llm_runtime", retryEvents[0].Source)
	assert.Equal(t, "provider-a", retryEvents[0].Provider)
	assert.Equal(t, "gpt-5.4-mini", retryEvents[0].Model)
	assert.Equal(t, 1, retryEvents[0].Attempt)
	assert.Equal(t, 3, retryEvents[0].MaxAttempts)
	assert.Equal(t, "http_500", retryEvents[0].RetryReason)
	assert.GreaterOrEqual(t, retryEvents[0].RetryDelayMS, int64(10))
	assert.Contains(t, retryEvents[0].Error, "HTTP 500")
}

func TestLLMRuntime_Call_DoesNotRetryRetryExhaustedErrors(t *testing.T) {
	runtime := NewLLMRuntime(&RuntimeConfig{
		DefaultProvider: "provider-a",
		DefaultModel:    "gpt-5.4-mini",
		MaxRetries:      3,
	})
	provider := &flakyProvider{
		name: "provider-a",
		errs: []error{
			markRetryExhausted("all retry attempts failed", 2, fmt.Errorf("HTTP 500: {\"error\":{\"message\":\"temporary upstream failure\"}}")),
		},
	}
	require.NoError(t, runtime.RegisterProvider("provider-a", provider))

	_, err := runtime.Call(context.Background(), &LLMRequest{
		Messages: []types.Message{{Role: "user", Content: "hello"}},
	})
	require.Error(t, err)
	assert.Equal(t, 1, provider.calls)
}

type mockResourceManager struct{}

func (m *mockResourceManager) SelectResource(retryInfo RetryInfo) (*SelectedResource, error) {
	return &SelectedResource{Provider: &ProviderResource{Name: "mock", Type: "openai", BaseURL: "http://localhost"}}, nil
}

func (m *mockResourceManager) RecordResult(selected *SelectedResource, success bool, err error, statusCode int, latencyMs int64) {
}

func TestLLMRuntime_RegisterGatewayClient(t *testing.T) {
	runtime := NewLLMRuntime(&RuntimeConfig{
		DefaultTimeout: 15 * time.Second,
		MaxRetries:     2,
		RetryTuning: RetryTuning{
			BaseDelay:  300 * time.Millisecond,
			MaxDelay:   2 * time.Second,
			Multiplier: 1.6,
		},
		RetryRules: []RetryRule{
			{
				Name:       "http_5xx_retry",
				Enabled:    true,
				MaxRetries: 4,
				StatusCode: RetryStatusCodeMatcher{Range: "500-504"},
			},
		},
	})

	// Use a minimal mock ResourceManager
	rm := &mockResourceManager{}

	err := runtime.RegisterGatewayClient("gateway-default", rm, "gpt-4o")
	require.NoError(t, err)

	provider, err := runtime.GetProvider("gateway-default")
	require.NoError(t, err)

	gatewayProvider, ok := provider.(*GatewayClient)
	require.True(t, ok)
	assert.Equal(t, "gpt-4o", gatewayProvider.defaultModel)
	assert.Equal(t, 2, gatewayProvider.maxRetries)
	assert.Equal(t, 15*time.Second, gatewayProvider.defaultTimeout)
	assert.Equal(t, 300*time.Millisecond, gatewayProvider.retryTuning.BaseDelay)
	assert.Equal(t, 2*time.Second, gatewayProvider.retryTuning.MaxDelay)
	assert.Equal(t, 1.6, gatewayProvider.retryTuning.Multiplier)
	require.Len(t, gatewayProvider.retryRules, 1)
	assert.Equal(t, "http_5xx_retry", gatewayProvider.retryRules[0].Name)
	assert.Equal(t, 4, gatewayProvider.retryRules[0].MaxRetries)
	assert.Contains(t, runtime.ListProviders(), "gateway-default")
}

func TestLLMRuntime_Stream_RetriesErrorChunkBeforeText(t *testing.T) {
	runtime := NewLLMRuntime(&RuntimeConfig{
		DefaultProvider: "provider-a",
		DefaultModel:    "gpt-5.4-mini",
		MaxRetries:      2,
		RetryTuning: RetryTuning{
			BaseDelay: time.Millisecond,
		},
	})
	provider := &streamSequenceProvider{
		name: "provider-a",
		streams: [][]StreamChunk{
			{
				{Type: EventTypeError, Error: "HTTP 503: temporary upstream failure", Done: true},
			},
			{
				{Type: EventTypeText, Content: "hello"},
				{Type: EventTypeDone, Done: true},
			},
		},
	}
	require.NoError(t, runtime.RegisterProvider("provider-a", provider))

	stream, err := runtime.Stream(context.Background(), &LLMRequest{
		Messages: []types.Message{{Role: "user", Content: "hello"}},
	})
	require.NoError(t, err)

	var chunks []StreamChunk
	for chunk := range stream {
		chunks = append(chunks, chunk)
	}

	require.Len(t, chunks, 2)
	assert.Equal(t, EventTypeText, chunks[0].Type)
	assert.Equal(t, "hello", chunks[0].Content)
	assert.Equal(t, EventTypeDone, chunks[1].Type)
	assert.Equal(t, 2, provider.calls)
}

func TestLLMRuntime_Stream_DoesNotRetryAfterText(t *testing.T) {
	runtime := NewLLMRuntime(&RuntimeConfig{
		DefaultProvider: "provider-a",
		DefaultModel:    "gpt-5.4-mini",
		MaxRetries:      2,
		RetryTuning: RetryTuning{
			BaseDelay: time.Millisecond,
		},
	})
	provider := &streamSequenceProvider{
		name: "provider-a",
		streams: [][]StreamChunk{
			{
				{Type: EventTypeText, Content: "partial"},
				{Type: EventTypeError, Error: "HTTP 503: temporary upstream failure", Done: true},
			},
			{
				{Type: EventTypeText, Content: "should-not-run"},
				{Type: EventTypeDone, Done: true},
			},
		},
	}
	require.NoError(t, runtime.RegisterProvider("provider-a", provider))

	stream, err := runtime.Stream(context.Background(), &LLMRequest{
		Messages: []types.Message{{Role: "user", Content: "hello"}},
	})
	require.NoError(t, err)

	var chunks []StreamChunk
	for chunk := range stream {
		chunks = append(chunks, chunk)
	}

	require.Len(t, chunks, 2)
	assert.Equal(t, EventTypeText, chunks[0].Type)
	assert.Equal(t, "partial", chunks[0].Content)
	assert.Equal(t, EventTypeError, chunks[1].Type)
	assert.Equal(t, 1, provider.calls)
}

func TestLLMRuntime_Stream_UsesConfiguredRetryRules(t *testing.T) {
	runtime := NewLLMRuntime(&RuntimeConfig{
		DefaultProvider: "provider-a",
		DefaultModel:    "gpt-5.4-mini",
		MaxRetries:      1,
		RetryTuning: RetryTuning{
			BaseDelay: time.Millisecond,
		},
		RetryRules: []RetryRule{
			{
				Name:       "configured_stream_retry",
				Enabled:    true,
				MaxRetries: 2,
				Keyword: RetryKeywordMatcher{
					Values: []string{"retry_me"},
				},
			},
		},
	})
	provider := &streamSequenceProvider{
		name: "provider-a",
		streams: [][]StreamChunk{
			{
				{Type: EventTypeError, Error: "retry_me: transient stream failure", Done: true},
			},
			{
				{Type: EventTypeText, Content: "recovered"},
				{Type: EventTypeDone, Done: true},
			},
		},
	}
	require.NoError(t, runtime.RegisterProvider("provider-a", provider))

	stream, err := runtime.Stream(context.Background(), &LLMRequest{
		Messages: []types.Message{{Role: "user", Content: "hello"}},
	})
	require.NoError(t, err)

	var content string
	for chunk := range stream {
		if chunk.Type == EventTypeText {
			content += chunk.Content
		}
	}

	assert.Equal(t, "recovered", content)
	assert.Equal(t, 2, provider.calls)
}

func TestLLMRuntime_Stream_RetriesImmediateProviderError(t *testing.T) {
	runtime := NewLLMRuntime(&RuntimeConfig{
		DefaultProvider: "provider-a",
		DefaultModel:    "gpt-5.4-mini",
		MaxRetries:      2,
		RetryTuning: RetryTuning{
			BaseDelay: time.Millisecond,
		},
	})
	provider := &streamSequenceProvider{
		name: "provider-a",
		streamErrs: []error{
			fmt.Errorf("HTTP 503: temporary upstream failure"),
		},
		streams: [][]StreamChunk{
			nil,
			{
				{Type: EventTypeText, Content: "after-immediate-error"},
				{Type: EventTypeDone, Done: true},
			},
		},
	}
	require.NoError(t, runtime.RegisterProvider("provider-a", provider))

	stream, err := runtime.Stream(context.Background(), &LLMRequest{
		Messages: []types.Message{{Role: "user", Content: "hello"}},
	})
	require.NoError(t, err)

	var content string
	for chunk := range stream {
		if chunk.Type == EventTypeText {
			content += chunk.Content
		}
	}

	assert.Equal(t, "after-immediate-error", content)
	assert.Equal(t, 2, provider.calls)
}

func TestLLMRuntime_ReplaceProviderRegistrationResetsAliases(t *testing.T) {
	runtime := NewLLMRuntime(&RuntimeConfig{MaxRetries: 0})
	providerA := &captureProvider{name: "provider-a"}
	require.NoError(t, runtime.RegisterProvider("provider-a", providerA))
	require.NoError(t, runtime.RegisterProviderAlias("model-old", "provider-a"))

	providerB := &captureProvider{name: "provider-a"}
	require.NoError(t, runtime.ReplaceProviderRegistration("provider-a", providerB, "model-new"))

	require.Nil(t, runtime.ProviderAliases("model-old"))
	require.ElementsMatch(t, []string{"model-new", "provider-a"}, runtime.ProviderAliases("provider-a"))

	resp, err := runtime.Call(context.Background(), &LLMRequest{
		Model:    "model-new",
		Messages: []types.Message{{Role: "user", Content: "hello"}},
	})
	require.NoError(t, err)
	require.NotNil(t, resp)
	require.NotNil(t, providerB.lastReq)
	assert.Equal(t, "provider:provider-a", resp.Content)
}

func TestLLMRuntime_ProviderAliasesRemainScopedWhenGlobalAliasCollides(t *testing.T) {
	runtime := NewLLMRuntime(&RuntimeConfig{MaxRetries: 0})
	require.NoError(t, runtime.RegisterProvider("provider-a", &captureProvider{name: "provider-a"}))
	require.NoError(t, runtime.RegisterProvider("provider-b", &captureProvider{name: "provider-b"}))

	require.NoError(t, runtime.RegisterProviderAlias("shared-model", "provider-a"))
	require.NoError(t, runtime.RegisterProviderAlias("shared-model", "provider-b"))
	require.NoError(t, runtime.RegisterProviderAlias("provider-a-only", "provider-a"))
	require.NoError(t, runtime.RegisterProviderAlias("provider-b-only", "provider-b"))

	assert.Equal(t, "provider-b", runtime.ResolveProviderName("shared-model"))
	assert.ElementsMatch(t,
		[]string{"provider-a", "provider-a-only", "shared-model"},
		runtime.ProviderAliases("provider-a"),
	)
	assert.ElementsMatch(t,
		[]string{"provider-b", "provider-b-only", "shared-model"},
		runtime.ProviderAliases("provider-b"),
	)
}
