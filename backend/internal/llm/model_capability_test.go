package llm

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	agentconfig "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
)

type capabilityProvider struct {
	name         string
	capabilities map[string]agentconfig.ModelCapabilitySpec
}

func (p *capabilityProvider) Name() string { return p.name }

func (p *capabilityProvider) Call(ctx context.Context, req *LLMRequest) (*LLMResponse, error) {
	return &LLMResponse{Content: "ok", Model: req.Model}, nil
}

func (p *capabilityProvider) Stream(ctx context.Context, req *LLMRequest) (<-chan StreamChunk, error) {
	ch := make(chan StreamChunk, 1)
	ch <- StreamChunk{Type: EventTypeDone, Done: true}
	close(ch)
	return ch, nil
}

func (p *capabilityProvider) CountTokens(text string) int { return len(text) }

func (p *capabilityProvider) GetCapabilities() *ModelCapabilities {
	return &ModelCapabilities{SupportsStreaming: true}
}

func (p *capabilityProvider) CheckHealth(ctx context.Context) error { return nil }

func (p *capabilityProvider) ResolveModelCapability(requestedModel string) (string, agentconfig.ModelCapabilitySpec, bool) {
	capability, ok := ResolveModelCapabilitySpec(requestedModel, p.capabilities)
	return requestedModel, capability, ok
}

func TestResolveModelCapabilitySpecPrefersExactMatch(t *testing.T) {
	capability, ok := ResolveModelCapabilitySpec("gpt-5", map[string]agentconfig.ModelCapabilitySpec{
		"*":     {MaxContextTokens: 100000},
		"gpt-5": {MaxContextTokens: 272000},
	})
	require.True(t, ok)
	require.Equal(t, 272000, capability.MaxContextTokens)
}

func TestResolveModelCapabilitySpecFallsBackToWildcard(t *testing.T) {
	capability, ok := ResolveModelCapabilitySpec("gpt-5-mini", map[string]agentconfig.ModelCapabilitySpec{
		"*": {MaxContextTokens: 128000},
	})
	require.True(t, ok)
	require.Equal(t, 128000, capability.MaxContextTokens)
}

func TestResolveRuntimeModelCapabilityUsesResolvedProviderAlias(t *testing.T) {
	runtime := NewLLMRuntime(&RuntimeConfig{
		DefaultProvider: "provider-a",
		DefaultModel:    "gpt-5",
		MaxRetries:      0,
	})
	provider := &capabilityProvider{
		name: "provider-a",
		capabilities: map[string]agentconfig.ModelCapabilitySpec{
			"gpt-5": {MaxContextTokens: 272000, AutoCompactRatio: 0.9},
		},
	}
	require.NoError(t, runtime.RegisterProvider("provider-a", provider))
	require.NoError(t, runtime.RegisterProviderAlias("gpt-5", "provider-a"))

	resolvedProvider, resolvedModel, capability, ok := ResolveRuntimeModelCapability(runtime, "", "gpt-5")
	require.True(t, ok)
	require.Equal(t, "provider-a", resolvedProvider)
	require.Equal(t, "gpt-5", resolvedModel)
	require.Equal(t, 272000, capability.MaxContextTokens)
}

var _ Provider = (*capabilityProvider)(nil)
var _ ModelCapabilityResolver = (*capabilityProvider)(nil)
