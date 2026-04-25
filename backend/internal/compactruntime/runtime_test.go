package compactruntime

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	agentconfig "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
	"github.com/wwsheng009/ai-agent-runtime/internal/artifact"
	"github.com/wwsheng009/ai-agent-runtime/internal/contextmgr"
	"github.com/wwsheng009/ai-agent-runtime/internal/llm"
	"github.com/wwsheng009/ai-agent-runtime/internal/types"
)

type compactTestProvider struct {
	name         string
	callCount    int
	capabilities map[string]agentconfig.ModelCapabilitySpec
	lastRequest  *llm.LLMRequest
}

type compactRemoteProvider struct {
	*compactTestProvider
	remoteCallCount int
	response        *llm.RemoteCompactResponse
}

func (p *compactTestProvider) Name() string { return p.name }

func (p *compactTestProvider) Call(ctx context.Context, req *llm.LLMRequest) (*llm.LLMResponse, error) {
	p.callCount++
	p.lastRequest = cloneLLMRequest(req)
	return &llm.LLMResponse{
		Content: "User goal preserved. Key tool results preserved. Continue from the latest turns.",
		Model:   req.Model,
	}, nil
}

func (p *compactTestProvider) Stream(ctx context.Context, req *llm.LLMRequest) (<-chan llm.StreamChunk, error) {
	ch := make(chan llm.StreamChunk, 1)
	ch <- llm.StreamChunk{Type: llm.EventTypeDone, Done: true}
	close(ch)
	return ch, nil
}

func (p *compactTestProvider) CountTokens(text string) int { return len(text) }

func (p *compactTestProvider) GetCapabilities() *llm.ModelCapabilities {
	return &llm.ModelCapabilities{SupportsStreaming: true}
}

func (p *compactTestProvider) CheckHealth(ctx context.Context) error { return nil }

func (p *compactTestProvider) ResolveModelCapability(requestedModel string) (string, agentconfig.ModelCapabilitySpec, bool) {
	capability, ok := llm.ResolveModelCapabilitySpec(requestedModel, p.capabilities)
	return requestedModel, capability, ok
}

func (p *compactRemoteProvider) RemoteCompact(ctx context.Context, req llm.RemoteCompactRequest) (*llm.RemoteCompactResponse, error) {
	p.remoteCallCount++
	if p.response == nil {
		return nil, nil
	}
	response := &llm.RemoteCompactResponse{
		CompactedMessages: p.response.CompactedMessages,
		CheckpointIDs:     append([]string(nil), p.response.CheckpointIDs...),
	}
	if len(p.response.ReplacementHistory) > 0 {
		response.ReplacementHistory = cloneMessages(p.response.ReplacementHistory)
	}
	return response, nil
}

func TestMaybeCompactUsesModelSpecificLimit(t *testing.T) {
	runtime := llm.NewLLMRuntime(&llm.RuntimeConfig{
		DefaultProvider: "provider-a",
		DefaultModel:    "gpt-5",
		MaxRetries:      0,
	})
	provider := &compactTestProvider{
		name: "provider-a",
		capabilities: map[string]agentconfig.ModelCapabilitySpec{
			"gpt-5": {
				MaxContextTokens:      272000,
				AutoCompactTokenLimit: 200,
			},
		},
	}
	require.NoError(t, runtime.RegisterProvider("provider-a", provider))
	require.NoError(t, runtime.RegisterProviderAlias("gpt-5", "provider-a"))

	compactor := New(runtime, nil)
	result, status, err := compactor.MaybeCompact(context.Background(), Request{
		SessionID:          "session-compact-limit",
		Provider:           "provider-a",
		Model:              "gpt-5",
		History:            compactTestHistory(),
		KeepRecentMessages: 2,
		Phase:              PhasePreTurn,
		CountTokens: func(messages []types.Message) int {
			return len(messages) * 60
		},
	})
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, 200, result.TriggerTokenLimit)
	require.Equal(t, "provider-a", status.ResolvedProvider)
	require.Equal(t, 1, provider.callCount)
	require.Len(t, result.ReplacementHistory, 4)
	require.Equal(t, "system", result.ReplacementHistory[0].Role)
	require.Equal(t, "user", result.ReplacementHistory[1].Role)
	require.Equal(t, "user", result.ReplacementHistory[2].Role)
	require.Equal(t, "user", result.ReplacementHistory[3].Role)
	require.Equal(t, "compaction", result.ReplacementHistory[3].Metadata["context_stage"])
	require.Equal(t, ModeLocal, result.ReplacementHistory[3].Metadata["compact_mode"])
	require.Equal(t, 2, result.CompactedMessages)
}

func TestMaybeCompactFallsBackToWildcardAndSkipsBelowLimit(t *testing.T) {
	runtime := llm.NewLLMRuntime(&llm.RuntimeConfig{
		DefaultProvider: "provider-a",
		DefaultModel:    "gpt-5-mini",
		MaxRetries:      0,
	})
	provider := &compactTestProvider{
		name: "provider-a",
		capabilities: map[string]agentconfig.ModelCapabilitySpec{
			"*": {MaxContextTokens: 100},
		},
	}
	require.NoError(t, runtime.RegisterProvider("provider-a", provider))
	require.NoError(t, runtime.RegisterProviderAlias("gpt-5-mini", "provider-a"))

	compactor := New(runtime, nil)
	result, status, err := compactor.MaybeCompact(context.Background(), Request{
		SessionID: "session-compact-skip",
		Model:     "gpt-5-mini",
		History:   compactTestHistory(),
		Phase:     PhasePreTurn,
		CountTokens: func(messages []types.Message) int {
			return len(messages) * 10
		},
	})
	require.NoError(t, err)
	require.Nil(t, result)
	require.Equal(t, "below_limit", status.Reason)
	require.Equal(t, 90, status.TriggerTokenLimit)
	require.Equal(t, 0, provider.callCount)
}

func TestMaybeCompactReusesSummaryCheckpointWithoutSecondLLMCall(t *testing.T) {
	store, err := artifact.NewStore(nil)
	require.NoError(t, err)

	runtime := llm.NewLLMRuntime(&llm.RuntimeConfig{
		DefaultProvider: "provider-a",
		DefaultModel:    "gpt-5",
		MaxRetries:      0,
	})
	provider := &compactTestProvider{
		name: "provider-a",
		capabilities: map[string]agentconfig.ModelCapabilitySpec{
			"gpt-5": {AutoCompactTokenLimit: 150},
		},
	}
	require.NoError(t, runtime.RegisterProvider("provider-a", provider))
	require.NoError(t, runtime.RegisterProviderAlias("gpt-5", "provider-a"))

	manager := &contextmgr.Manager{Ledger: store}
	compactor := New(runtime, manager)
	request := Request{
		SessionID:          "session-compact-checkpoint",
		Model:              "gpt-5",
		History:            compactTestHistory(),
		KeepRecentMessages: 2,
		Phase:              PhasePreTurn,
		CountTokens: func(messages []types.Message) int {
			return len(messages) * 60
		},
	}

	first, _, err := compactor.MaybeCompact(context.Background(), request)
	require.NoError(t, err)
	require.NotNil(t, first)
	require.Len(t, first.CheckpointIDs, 1)
	require.Equal(t, 1, provider.callCount)

	second, _, err := compactor.MaybeCompact(context.Background(), request)
	require.NoError(t, err)
	require.NotNil(t, second)
	require.Len(t, second.CheckpointIDs, 1)
	require.Equal(t, 1, provider.callCount)
}

func TestMaybeCompactUsesRemoteAdapterWhenCapabilitySupportsIt(t *testing.T) {
	runtime := llm.NewLLMRuntime(&llm.RuntimeConfig{
		DefaultProvider: "provider-a",
		DefaultModel:    "gpt-5",
		MaxRetries:      0,
	})
	provider := &compactRemoteProvider{
		compactTestProvider: &compactTestProvider{
			name: "provider-a",
			capabilities: map[string]agentconfig.ModelCapabilitySpec{
				"gpt-5": {
					AutoCompactTokenLimit: 150,
					SupportsRemoteCompact: true,
				},
			},
		},
		response: &llm.RemoteCompactResponse{
			ReplacementHistory: []types.Message{
				*types.NewSystemMessage("You are a helpful assistant."),
				*types.NewAssistantMessage("Compacted context from remote provider."),
				*types.NewUserMessage("Continue and summarize the root cause."),
			},
			CompactedMessages: 2,
			CheckpointIDs:     []string{"remote-checkpoint-1"},
		},
	}
	require.NoError(t, runtime.RegisterProvider("provider-a", provider))
	require.NoError(t, runtime.RegisterProviderAlias("gpt-5", "provider-a"))

	compactor := New(runtime, nil)
	result, status, err := compactor.MaybeCompact(context.Background(), Request{
		SessionID:          "session-compact-remote",
		Model:              "gpt-5",
		History:            compactTestHistory(),
		KeepRecentMessages: 2,
		Phase:              PhasePreTurn,
		CountTokens: func(messages []types.Message) int {
			return len(messages) * 60
		},
	})
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, ModeRemote, status.Mode)
	require.Equal(t, ModeRemote, result.Mode)
	require.Equal(t, 0, provider.callCount)
	require.Equal(t, 1, provider.remoteCallCount)
	require.Len(t, result.ReplacementHistory, 3)
	require.Equal(t, "remote-checkpoint-1", result.CheckpointIDs[0])
}

func TestMaybeCompactSkipsWhenRemoteModeSelectedButProviderUnsupported(t *testing.T) {
	runtime := llm.NewLLMRuntime(&llm.RuntimeConfig{
		DefaultProvider: "provider-a",
		DefaultModel:    "gpt-5",
		MaxRetries:      0,
	})
	provider := &compactTestProvider{
		name: "provider-a",
		capabilities: map[string]agentconfig.ModelCapabilitySpec{
			"gpt-5": {
				AutoCompactTokenLimit: 150,
				AutoCompactMode:       ModeRemote,
			},
		},
	}
	require.NoError(t, runtime.RegisterProvider("provider-a", provider))
	require.NoError(t, runtime.RegisterProviderAlias("gpt-5", "provider-a"))

	compactor := New(runtime, nil)
	result, status, err := compactor.MaybeCompact(context.Background(), Request{
		SessionID:          "session-compact-remote-skip",
		Model:              "gpt-5",
		History:            compactTestHistory(),
		KeepRecentMessages: 2,
		Phase:              PhasePreTurn,
		CountTokens: func(messages []types.Message) int {
			return len(messages) * 60
		},
	})
	require.NoError(t, err)
	require.Nil(t, result)
	require.Equal(t, ModeRemote, status.Mode)
	require.Equal(t, "remote_compact_unsupported", status.Reason)
	require.Equal(t, 0, provider.callCount)
}

func TestMaybeCompactForceBypassesBelowLimit(t *testing.T) {
	runtime := llm.NewLLMRuntime(&llm.RuntimeConfig{
		DefaultProvider: "provider-a",
		DefaultModel:    "gpt-5",
		MaxRetries:      0,
	})
	provider := &compactTestProvider{
		name: "provider-a",
		capabilities: map[string]agentconfig.ModelCapabilitySpec{
			"gpt-5": {
				AutoCompactTokenLimit: 1000,
			},
		},
	}
	require.NoError(t, runtime.RegisterProvider("provider-a", provider))
	require.NoError(t, runtime.RegisterProviderAlias("gpt-5", "provider-a"))

	compactor := New(runtime, nil)
	result, status, err := compactor.MaybeCompact(context.Background(), Request{
		SessionID:          "session-compact-force",
		Model:              "gpt-5",
		Force:              true,
		History:            compactTestHistory(),
		KeepRecentMessages: 2,
		Phase:              PhasePreTurn,
		CountTokens: func(messages []types.Message) int {
			return len(messages) * 10
		},
	})
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, "provider-a", status.ResolvedProvider)
	require.Equal(t, 1, provider.callCount)
}

func TestMaybeCompactMissingCapabilityReportsResolvedProviderAndModel(t *testing.T) {
	runtime := llm.NewLLMRuntime(&llm.RuntimeConfig{
		DefaultProvider: "provider-a",
		DefaultModel:    "gpt-5.5",
		MaxRetries:      0,
	})
	provider := &compactTestProvider{
		name:         "provider-a",
		capabilities: map[string]agentconfig.ModelCapabilitySpec{},
	}
	require.NoError(t, runtime.RegisterProvider("provider-a", provider))
	require.NoError(t, runtime.RegisterProviderAlias("gpt-5.5", "provider-a"))

	compactor := New(runtime, nil)
	result, status, err := compactor.MaybeCompact(context.Background(), Request{
		SessionID: "session-compact-missing-capability",
		Model:     "gpt-5.5",
		History:   compactTestHistory(),
		Phase:     PhasePreTurn,
		CountTokens: func(messages []types.Message) int {
			return len(messages) * 60
		},
	})
	require.NoError(t, err)
	require.Nil(t, result)
	require.Equal(t, "missing_model_capability", status.Reason)
	require.Equal(t, "provider-a", status.ResolvedProvider)
	require.Equal(t, "gpt-5.5", status.ResolvedModel)
	require.Equal(t, len(compactTestHistory())*60, status.TokenBefore)
}

func TestMaybeCompactForceLocalDoesNotRequireModelCapability(t *testing.T) {
	runtime := llm.NewLLMRuntime(&llm.RuntimeConfig{
		DefaultProvider: "provider-a",
		DefaultModel:    "gpt-5.5",
		MaxRetries:      0,
	})
	provider := &compactTestProvider{
		name:         "provider-a",
		capabilities: map[string]agentconfig.ModelCapabilitySpec{},
	}
	require.NoError(t, runtime.RegisterProvider("provider-a", provider))
	require.NoError(t, runtime.RegisterProviderAlias("gpt-5.5", "provider-a"))

	compactor := New(runtime, nil)
	result, status, err := compactor.MaybeCompact(context.Background(), Request{
		SessionID:          "session-compact-force-local-no-capability",
		Model:              "gpt-5.5",
		Mode:               ModeLocal,
		Force:              true,
		History:            compactTestHistory(),
		KeepRecentMessages: 2,
		Phase:              PhasePreTurn,
		CountTokens: func(messages []types.Message) int {
			return len(messages) * 60
		},
	})
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, ModeLocal, status.Mode)
	require.Equal(t, "provider-a", status.ResolvedProvider)
	require.Equal(t, "gpt-5.5", status.ResolvedModel)
	require.Equal(t, 1, provider.callCount)
}

func TestMaybeCompactLocalRequestUsesOriginalMessagesAndCompactPrompt(t *testing.T) {
	runtime := llm.NewLLMRuntime(&llm.RuntimeConfig{
		DefaultProvider: "provider-a",
		DefaultModel:    "gpt-5",
		MaxRetries:      0,
	})
	provider := &compactTestProvider{
		name: "provider-a",
		capabilities: map[string]agentconfig.ModelCapabilitySpec{
			"gpt-5": {
				AutoCompactTokenLimit: 100,
			},
		},
	}
	require.NoError(t, runtime.RegisterProvider("provider-a", provider))
	require.NoError(t, runtime.RegisterProviderAlias("gpt-5", "provider-a"))

	compactor := New(runtime, nil)
	result, _, err := compactor.MaybeCompact(context.Background(), Request{
		SessionID:          "session-compact-local-shape",
		Model:              "gpt-5",
		History:            compactTestHistory(),
		KeepRecentMessages: 2,
		Phase:              PhasePreTurn,
		CountTokens: func(messages []types.Message) int {
			return len(messages) * 60
		},
	})
	require.NoError(t, err)
	require.NotNil(t, result)
	require.NotNil(t, provider.lastRequest)
	require.Len(t, provider.lastRequest.Messages, 6)
	require.Equal(t, "system", provider.lastRequest.Messages[0].Role)
	require.Equal(t, "You are a helpful assistant.", provider.lastRequest.Messages[0].Content)
	require.Equal(t, "user", provider.lastRequest.Messages[1].Role)
	require.Equal(t, "Investigate the build failure.", provider.lastRequest.Messages[1].Content)
	require.Equal(t, "assistant", provider.lastRequest.Messages[2].Role)
	require.Equal(t, "I will inspect the failing module.", provider.lastRequest.Messages[2].Content)
	require.Equal(t, "user", provider.lastRequest.Messages[3].Role)
	require.Equal(t, "Continue and summarize the root cause.", provider.lastRequest.Messages[3].Content)
	require.Equal(t, "assistant", provider.lastRequest.Messages[4].Role)
	require.Equal(t, "The latest logs point at a missing provider config.", provider.lastRequest.Messages[4].Content)
	require.Equal(t, "user", provider.lastRequest.Messages[5].Role)
	require.Equal(t, localCompactionPrompt, provider.lastRequest.Messages[5].Content)
	for _, message := range provider.lastRequest.Messages {
		require.NotContains(t, message.Content, "Summarize this earlier conversation history for continued execution:")
		require.NotContains(t, message.Content, "[1] role=")
	}
}

func TestMaybeCompactKeepsTrailingActiveUserAfterSummary(t *testing.T) {
	runtime := llm.NewLLMRuntime(&llm.RuntimeConfig{
		DefaultProvider: "provider-a",
		DefaultModel:    "gpt-5",
		MaxRetries:      0,
	})
	provider := &compactTestProvider{
		name: "provider-a",
		capabilities: map[string]agentconfig.ModelCapabilitySpec{
			"gpt-5": {
				AutoCompactTokenLimit: 100,
			},
		},
	}
	require.NoError(t, runtime.RegisterProvider("provider-a", provider))
	require.NoError(t, runtime.RegisterProviderAlias("gpt-5", "provider-a"))

	compactor := New(runtime, nil)
	result, _, err := compactor.MaybeCompact(context.Background(), Request{
		SessionID: "session-compact-trailing-user",
		Model:     "gpt-5",
		History: []types.Message{
			*types.NewSystemMessage("You are a helpful assistant."),
			*types.NewUserMessage("Investigate the build failure."),
			*types.NewAssistantMessage("I inspected the failing module."),
			*types.NewUserMessage("Continue from the latest findings."),
		},
		KeepRecentMessages: 1,
		Phase:              PhasePreTurn,
		CountTokens: func(messages []types.Message) int {
			return len(messages) * 60
		},
	})
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Len(t, result.ReplacementHistory, 4)
	require.Equal(t, "system", result.ReplacementHistory[0].Role)
	require.Equal(t, "user", result.ReplacementHistory[1].Role)
	require.Equal(t, "compaction", result.ReplacementHistory[2].Metadata["context_stage"])
	require.Equal(t, "Continue from the latest findings.", result.ReplacementHistory[3].Content)
}

func compactTestHistory() []types.Message {
	return []types.Message{
		*types.NewSystemMessage("You are a helpful assistant."),
		*types.NewUserMessage("Investigate the build failure."),
		*types.NewAssistantMessage("I will inspect the failing module."),
		*types.NewUserMessage("Continue and summarize the root cause."),
		*types.NewAssistantMessage("The latest logs point at a missing provider config."),
	}
}

var _ llm.Provider = (*compactTestProvider)(nil)
var _ llm.ModelCapabilityResolver = (*compactTestProvider)(nil)
var _ llm.Provider = (*compactRemoteProvider)(nil)
var _ llm.ModelCapabilityResolver = (*compactRemoteProvider)(nil)
var _ llm.RemoteCompactionProvider = (*compactRemoteProvider)(nil)

func cloneLLMRequest(req *llm.LLMRequest) *llm.LLMRequest {
	if req == nil {
		return nil
	}
	cloned := *req
	cloned.Messages = cloneMessages(req.Messages)
	if len(req.Metadata) > 0 {
		cloned.Metadata = make(map[string]interface{}, len(req.Metadata))
		for key, value := range req.Metadata {
			cloned.Metadata[key] = value
		}
	}
	return &cloned
}
