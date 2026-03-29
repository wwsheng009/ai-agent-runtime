package llm

import (
	"context"
	"testing"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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
	assert.Contains(t, runtime.ListProviders(), "gateway-default")
}
