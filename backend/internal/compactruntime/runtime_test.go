package compactruntime

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	agentconfig "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
	"github.com/wwsheng009/ai-agent-runtime/internal/artifact"
	"github.com/wwsheng009/ai-agent-runtime/internal/contextmgr"
	"github.com/wwsheng009/ai-agent-runtime/internal/llm"
	"github.com/wwsheng009/ai-agent-runtime/internal/types"
)

type compactTestProvider struct {
	name              string
	callCount         int
	capabilities      map[string]agentconfig.ModelCapabilitySpec
	lastRequest       *llm.LLMRequest
	responseContent   string
	responseReasoning string
	responseErr       error
	allowEmpty        bool
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
	if p.responseErr != nil {
		return nil, p.responseErr
	}
	content := p.responseContent
	if content == "" && strings.TrimSpace(p.responseReasoning) == "" && !p.allowEmpty {
		content = "User goal preserved. Key tool results preserved. Continue from the latest turns."
	}
	return &llm.LLMResponse{
		Content:   content,
		Reasoning: p.responseReasoning,
		Model:     req.Model,
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

func TestMaybeCompactUsesModelSpecificCompactSettings(t *testing.T) {
	runtime := llm.NewLLMRuntime(&llm.RuntimeConfig{
		DefaultProvider: "provider-a",
		DefaultModel:    "deepseek-v4-pro",
		MaxRetries:      0,
	})
	provider := &compactTestProvider{
		name: "provider-a",
		capabilities: map[string]agentconfig.ModelCapabilitySpec{
			"deepseek-v4-pro": {
				MaxContextTokens:       272000,
				MaxTokens:              4096,
				AutoCompactTokenLimit:  200,
				CompactReasoningEffort: "none",
			},
		},
	}
	require.NoError(t, runtime.RegisterProvider("provider-a", provider))
	require.NoError(t, runtime.RegisterProviderAlias("deepseek-v4-pro", "provider-a"))

	compactor := New(runtime, nil)
	result, _, err := compactor.MaybeCompact(context.Background(), Request{
		SessionID:          "session-compact-settings",
		Provider:           "provider-a",
		Model:              "deepseek-v4-pro",
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
	assert.Equal(t, 4096, provider.lastRequest.MaxTokens)
	assert.Equal(t, "none", provider.lastRequest.ReasoningEffort)
}

func TestMaybeCompactDefaultCompactRequestDisablesToolsAndOmitsReasoningEffort(t *testing.T) {
	runtime := llm.NewLLMRuntime(&llm.RuntimeConfig{
		DefaultProvider: "provider-a",
		DefaultModel:    "deepseek-v4-flash",
		MaxRetries:      0,
	})
	provider := &compactTestProvider{
		name: "provider-a",
		capabilities: map[string]agentconfig.ModelCapabilitySpec{
			"deepseek-v4-flash": {
				MaxContextTokens:      272000,
				AutoCompactTokenLimit: 200,
			},
		},
	}
	require.NoError(t, runtime.RegisterProvider("provider-a", provider))
	require.NoError(t, runtime.RegisterProviderAlias("deepseek-v4-flash", "provider-a"))

	compactor := New(runtime, nil)
	result, _, err := compactor.MaybeCompact(context.Background(), Request{
		SessionID:          "session-compact-request-shape",
		Provider:           "provider-a",
		Model:              "deepseek-v4-flash",
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
	assert.Empty(t, provider.lastRequest.ReasoningEffort)
	assert.Equal(t, "compact", provider.lastRequest.Metadata[llm.MetadataKeyInternalOperation])
	assert.Equal(t, true, provider.lastRequest.Metadata[llm.MetadataKeyDisableTools])
	assert.Equal(t, true, provider.lastRequest.Metadata[llm.MetadataKeyDisableMetaTools])
}

func TestMaybeCompactFallsBackToDeterministicSummaryWhenProviderSummaryEmpty(t *testing.T) {
	runtime := llm.NewLLMRuntime(&llm.RuntimeConfig{
		DefaultProvider: "provider-a",
		DefaultModel:    "gpt-5",
		MaxRetries:      0,
	})
	provider := &compactTestProvider{
		name:       "provider-a",
		allowEmpty: true,
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
		SessionID:          "session-compact-fallback-empty",
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
	require.NotEmpty(t, result.ReplacementHistory)
	require.Equal(t, "", status.Reason)
	summary := result.ReplacementHistory[len(result.ReplacementHistory)-1]
	require.Equal(t, "compaction", summary.Metadata.GetString("context_stage", ""))
	require.Equal(t, "deterministic_fallback", summary.Metadata.GetString("summary_source", ""))
	require.Contains(t, summary.Content, "Fallback summary generated locally")
	require.Equal(t, "deterministic_fallback", result.UsageSource)
}

func TestMaybeCompactFallsBackToDeterministicSummaryWhenProviderErrors(t *testing.T) {
	runtime := llm.NewLLMRuntime(&llm.RuntimeConfig{
		DefaultProvider: "provider-a",
		DefaultModel:    "gpt-5",
		MaxRetries:      0,
	})
	provider := &compactTestProvider{
		name:        "provider-a",
		responseErr: errors.New("upstream compact failed"),
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
	result, _, err := compactor.MaybeCompact(context.Background(), Request{
		SessionID:          "session-compact-fallback-error",
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
	summary := result.ReplacementHistory[len(result.ReplacementHistory)-1]
	require.Equal(t, "deterministic_fallback", summary.Metadata.GetString("summary_source", ""))
	require.Contains(t, summary.Metadata.GetString("summary_fallback_reason", ""), "upstream compact failed")
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

func TestMaybeCompactUsesReasoningFallbackWhenContentIsEmpty(t *testing.T) {
	runtime := llm.NewLLMRuntime(&llm.RuntimeConfig{
		DefaultProvider: "provider-a",
		DefaultModel:    "gpt-5",
		MaxRetries:      0,
	})
	provider := &compactTestProvider{
		name:              "provider-a",
		responseContent:   "",
		responseReasoning: "User goal preserved. Key tool results preserved. Continue from the latest turns.",
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
		SessionID:          "session-compact-reasoning-fallback",
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
	require.Equal(t, 1, provider.callCount)
	require.NotEmpty(t, result.ReplacementHistory)

	summaryMessage := result.ReplacementHistory[len(result.ReplacementHistory)-1]
	require.Equal(t, "user", summaryMessage.Role)
	require.Equal(t, "compaction", summaryMessage.Metadata["context_stage"])
	require.Contains(t, summaryMessage.Content, localSummaryHeading)
	require.Contains(t, summaryMessage.Content, "User goal preserved. Key tool results preserved.")
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
	cloned.Tools = append([]types.ToolDefinition(nil), req.Tools...)
	if len(req.Metadata) > 0 {
		cloned.Metadata = make(map[string]interface{}, len(req.Metadata))
		for key, value := range req.Metadata {
			cloned.Metadata[key] = value
		}
	}
	return &cloned
}
