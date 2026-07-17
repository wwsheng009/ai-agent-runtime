package compactruntime

import (
	"context"
	"errors"
	"fmt"
	"math"
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
	streamCount       int
	capabilities      map[string]agentconfig.ModelCapabilitySpec
	lastRequest       *llm.LLMRequest
	responseContent   string
	responseReasoning string
	responseErr       error
	streamErr         error
	streamChunks      []llm.StreamChunk
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
	p.streamCount++
	p.lastRequest = cloneLLMRequest(req)
	if p.streamErr != nil {
		return nil, p.streamErr
	}
	if p.responseErr != nil {
		return nil, p.responseErr
	}
	chunks := p.streamChunks
	if chunks == nil {
		chunks = p.defaultStreamChunks()
	}
	ch := make(chan llm.StreamChunk, len(chunks))
	for _, chunk := range chunks {
		ch <- chunk
	}
	close(ch)
	return ch, nil
}

func (p *compactTestProvider) defaultStreamChunks() []llm.StreamChunk {
	content := p.responseContent
	if content == "" && strings.TrimSpace(p.responseReasoning) == "" && !p.allowEmpty {
		content = "User goal preserved. Key tool results preserved. Continue from the latest turns."
	}
	chunks := make([]llm.StreamChunk, 0, 3)
	if content != "" {
		chunks = append(chunks, llm.StreamChunk{Type: llm.EventTypeText, Content: content})
	}
	if p.responseReasoning != "" {
		chunks = append(chunks, llm.StreamChunk{Type: llm.EventTypeReasoning, Content: p.responseReasoning})
	}
	chunks = append(chunks, llm.StreamChunk{Type: llm.EventTypeDone, Done: true})
	return chunks
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
	require.Equal(t, 0, provider.callCount)
	require.Equal(t, 1, provider.streamCount)
	require.Len(t, result.ReplacementHistory, 4)
	require.Equal(t, "system", result.ReplacementHistory[0].Role)
	require.Equal(t, "user", result.ReplacementHistory[1].Role)
	require.Equal(t, "user", result.ReplacementHistory[2].Role)
	require.Equal(t, "assistant", result.ReplacementHistory[3].Role)
	require.Equal(t, "compaction", result.ReplacementHistory[1].Metadata["context_stage"])
	require.Equal(t, ModeLocal, result.ReplacementHistory[1].Metadata["compact_mode"])
	require.Equal(t, "Continue and summarize the root cause.", result.ReplacementHistory[2].Content)
	require.Equal(t, "The latest logs point at a missing provider config.", result.ReplacementHistory[3].Content)
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
	require.Equal(t, 1, provider.streamCount)
	require.NotNil(t, provider.lastRequest)
	assert.Equal(t, 4096, provider.lastRequest.MaxTokens)
	assert.Equal(t, "none", provider.lastRequest.ReasoningEffort)
}

func TestFitLocalCompactSummaryMaxTokensReservesRecentContext(t *testing.T) {
	counter := func(messages []types.Message) int { return len(messages) * 60 }
	maxTokens := fitLocalCompactSummaryMaxTokens(
		2048,
		Request{ReplacementTokenLimit: 600},
		[]types.Message{*types.NewSystemMessage("system")},
		[]types.Message{
			*types.NewUserMessage("active request"),
			*types.NewAssistantMessage("current progress"),
		},
		counter,
	)

	require.Equal(t, 240, maxTokens)
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
	require.Equal(t, 0, provider.callCount)
	require.Equal(t, 1, provider.streamCount)
	require.NotNil(t, provider.lastRequest)
	assert.True(t, provider.lastRequest.Stream)
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
	require.Equal(t, 1, provider.streamCount)
	require.NotEmpty(t, result.ReplacementHistory)
	require.Equal(t, "", status.Reason)
	summary := requireCompactionMessage(t, result.ReplacementHistory)
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
	require.Equal(t, 1, provider.streamCount)
	summary := requireCompactionMessage(t, result.ReplacementHistory)
	require.Equal(t, "deterministic_fallback", summary.Metadata.GetString("summary_source", ""))
	require.Contains(t, summary.Metadata.GetString("summary_fallback_reason", ""), "upstream compact failed")
}

func TestMaybeCompactReducesOversizedInputBeforeProviderCompaction(t *testing.T) {
	runtime := llm.NewLLMRuntime(&llm.RuntimeConfig{
		DefaultProvider: "provider-a",
		DefaultModel:    "gpt-5",
		MaxRetries:      0,
	})
	provider := &compactTestProvider{
		name: "provider-a",
		capabilities: map[string]agentconfig.ModelCapabilitySpec{
			"gpt-5": {
				MaxContextTokens:      5000,
				AutoCompactTokenLimit: 100,
			},
		},
	}
	require.NoError(t, runtime.RegisterProvider("provider-a", provider))
	require.NoError(t, runtime.RegisterProviderAlias("gpt-5", "provider-a"))

	history := compactTestHistory()
	history = append(history, *types.NewToolMessage("call-large", strings.Repeat("large output ", 1000)))
	result, _, err := New(runtime, nil).MaybeCompact(context.Background(), Request{
		SessionID: "session-compact-preflight",
		Provider:  "provider-a",
		Model:     "gpt-5",
		History:   history,
		CountTokens: func(messages []types.Message) int {
			return len(messages) * 100
		},
	})

	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, 1, provider.streamCount, "a fitted reduced request should preserve provider compaction")
	require.NotNil(t, provider.lastRequest)
	require.Equal(t, true, provider.lastRequest.Metadata["compact_input_reduced"])
	require.Greater(t, provider.lastRequest.Metadata["compact_omitted_messages"].(int), 0)
	require.Empty(t, localCompactionRequestBudgetFailure(runtime, provider.lastRequest, threshold{MaxContextTokens: 5000}))
	summary := requireCompactionMessage(t, result.ReplacementHistory)
	require.NotEqual(t, "deterministic_fallback", summary.Metadata.GetString("summary_source", ""))
}

func TestLocalCompactionRequestBudgetRejectsOversizedSerializedInput(t *testing.T) {
	runtime := llm.NewLLMRuntime(nil)
	request := &llm.LLMRequest{
		Messages:  []types.Message{*types.NewUserMessage(strings.Repeat("x", localCompactMaxRequestBytes+1))},
		MaxTokens: 128,
	}
	reason := localCompactionRequestBudgetFailure(runtime, request, threshold{MaxContextTokens: 2_000_000})
	require.Contains(t, reason, "serialized input")
	require.Contains(t, reason, "limit")
}

func TestBuildFittedLocalCompactionRequestKeepsToolBatchBoundary(t *testing.T) {
	runtime := llm.NewLLMRuntime(nil)
	history := []types.Message{
		*types.NewUserMessage(strings.Repeat("old request ", 500)),
		{Role: "assistant", ToolCalls: []types.ToolCall{{ID: "call-1", Name: "view"}}},
		*types.NewToolMessage("call-1", strings.Repeat("large tool result ", 500)),
		*types.NewUserMessage("latest request"),
	}
	req := Request{Provider: "provider-a", Model: "gpt-5"}
	require.NotContains(t, safeLocalCompactionStarts(history), 2)
	fitted := buildFittedLocalCompactionLLMRequest(
		runtime, req, threshold{MaxContextTokens: 3600}, nil, history, 128, "", "oversized",
	)

	require.NotNil(t, fitted)
	require.Empty(t, localCompactionRequestBudgetFailure(runtime, fitted, threshold{MaxContextTokens: 3600}))
	omitted := fitted.Metadata["compact_omitted_messages"].(int)
	require.NotEqual(t, 2, omitted, "the retained suffix must not begin with an orphaned tool result")
	for _, message := range fitted.Messages {
		if message.Role == "tool" {
			require.NotEmpty(t, strings.TrimSpace(message.ToolCallID))
		}
	}
}

func TestSelectCompactionRecentMessagesHonorsCountAndKeepsAssistantProgress(t *testing.T) {
	history := []types.Message{
		*types.NewUserMessage("old request"),
		*types.NewAssistantMessage("old progress"),
		*types.NewUserMessage("current request"),
		*types.NewAssistantMessage("current progress"),
	}
	counter := func(messages []types.Message) int { return len(messages) * 10 }

	retained := selectCompactionRecentMessages(history, 2, counter, 100)
	require.Len(t, retained, 2)
	assert.Equal(t, "current request", retained[0].Content)
	assert.Equal(t, "current progress", retained[1].Content)

	retained = selectCompactionRecentMessages(history, 4, counter, 100)
	require.Len(t, retained, 4)
}

func TestSelectCompactionRecentMessagesKeepsCompleteToolBatch(t *testing.T) {
	assistant := types.NewAssistantMessage("Inspecting both files.")
	assistant.ToolCalls = []types.ToolCall{
		{ID: "call-1", Name: "view"},
		{ID: "call-2", Name: "view"},
	}
	history := []types.Message{
		*types.NewUserMessage("inspect the files"),
		*assistant,
		*types.NewToolMessage("call-1", "first result"),
		*types.NewToolMessage("call-2", "second result"),
	}

	retained := selectCompactionRecentMessages(history, 4, nil, 0)
	require.Len(t, retained, 4)
	require.Len(t, retained[1].ToolCalls, 2)
	assert.Equal(t, "call-1", retained[2].ToolCallID)
	assert.Equal(t, "call-2", retained[3].ToolCallID)
}

func TestSelectCompactionRecentMessagesDropsIncompleteToolBatch(t *testing.T) {
	assistant := types.NewAssistantMessage("Inspecting both files.")
	assistant.ToolCalls = []types.ToolCall{
		{ID: "call-1", Name: "view"},
		{ID: "call-2", Name: "view"},
	}
	history := []types.Message{
		*types.NewUserMessage("inspect the files"),
		*assistant,
		*types.NewToolMessage("call-1", "only first result"),
	}

	retained := selectCompactionRecentMessages(history, 8, nil, 0)
	require.Len(t, retained, 1)
	assert.Equal(t, "inspect the files", retained[0].Content)
}

func TestSelectCompactionRecentMessagesDropsStaleSummaryAndOrphanTool(t *testing.T) {
	oldSummary := types.NewUserMessage("old compacted objective")
	oldSummary.Metadata["context_stage"] = "compaction"
	history := []types.Message{
		*oldSummary,
		*types.NewToolMessage("orphan-call", "orphan result"),
		*types.NewUserMessage("current request"),
		*types.NewAssistantMessage("current progress"),
	}

	retained := selectCompactionRecentMessages(history, 8, nil, 0)
	require.Len(t, retained, 2)
	assert.Equal(t, "current request", retained[0].Content)
	assert.Equal(t, "current progress", retained[1].Content)
	for _, message := range retained {
		assert.NotEqual(t, "tool", message.Role)
		assert.NotEqual(t, "compaction", message.Metadata.GetString("context_stage", ""))
	}
}

func TestSelectCompactionRecentMessagesPinsOversizedActiveUser(t *testing.T) {
	history := []types.Message{
		*types.NewUserMessage("old request"),
		*types.NewAssistantMessage("old progress"),
		*types.NewUserMessage(strings.Repeat("current request ", 20)),
	}
	counter := func(messages []types.Message) int {
		total := 0
		for _, message := range messages {
			total += len(message.Content)
		}
		return total
	}

	retained := selectCompactionRecentMessages(history, 8, counter, 10)
	require.Len(t, retained, 1)
	assert.Contains(t, retained[0].Content, "current request")
}

func TestBuildDeterministicCompactSummaryRetainsLatestItems(t *testing.T) {
	history := make([]types.Message, 0, 100)
	for index := 1; index <= 20; index++ {
		history = append(history, *types.NewUserMessage(fmt.Sprintf("user request %d %s", index, strings.Repeat("u", 180))))
	}
	for index := 1; index <= 24; index++ {
		history = append(history, *types.NewAssistantMessage(fmt.Sprintf("assistant progress %d %s", index, strings.Repeat("a", 180))))
	}
	for index := 1; index <= 36; index++ {
		assistant := types.NewAssistantMessage("")
		assistant.ToolCalls = []types.ToolCall{{ID: fmt.Sprintf("call-%d", index), Name: fmt.Sprintf("tool-%d", index)}}
		history = append(history, *assistant)
		toolMessage := types.NewToolMessage(fmt.Sprintf("call-%d", index), fmt.Sprintf("tool outcome %d %s", index, strings.Repeat("t", 180)))
		if index == 36 {
			toolMessage.Metadata["tool_error"] = "final failure"
		}
		history = append(history, *toolMessage)
	}

	summary := buildDeterministicCompactSummary(history, "provider unavailable")
	require.NotContains(t, summary, "\n- user request 1 ")
	require.Contains(t, summary, "user request 20")
	require.NotContains(t, summary, "\n- assistant progress 1 ")
	require.Contains(t, summary, "assistant progress 24")
	require.NotContains(t, summary, "\n- tool-1: tool outcome 1 ")
	require.Contains(t, summary, "\n- tool-36: tool outcome 36 ")
	require.Contains(t, summary, "Recent tool failures to account for:")
	require.Contains(t, summary, "tool-36: final failure")
}

func TestBuildDeterministicCompactSummaryUsesAvailableBudgetForShortItems(t *testing.T) {
	history := make([]types.Message, 0, 42)
	for index := 1; index <= 10; index++ {
		history = append(history, *types.NewUserMessage(fmt.Sprintf("short user request %d", index)))
	}
	for index := 1; index <= 12; index++ {
		history = append(history, *types.NewAssistantMessage(fmt.Sprintf("short assistant progress %d", index)))
	}
	for index := 1; index <= 20; index++ {
		history = append(history, *types.NewToolMessage(fmt.Sprintf("short-call-%d", index), fmt.Sprintf("short tool outcome %d", index)))
	}

	summary := buildDeterministicCompactSummary(history, "provider unavailable")
	require.Contains(t, summary, "short user request 1")
	require.Contains(t, summary, "short assistant progress 1")
	require.Contains(t, summary, "short tool outcome 1")
	require.Contains(t, summary, "short tool outcome 20")
}

func TestBuildDeterministicCompactSummaryCarriesPriorCompactedContext(t *testing.T) {
	prior := types.NewUserMessage("Original objective: keep unattended agent execution running until the task is complete.")
	prior.Metadata["context_stage"] = "compaction"
	history := []types.Message{
		*prior,
		*types.NewAssistantMessage("Implemented the first recovery improvement."),
		*types.NewUserMessage("Continue optimizing."),
	}

	summary := buildDeterministicCompactSummary(history, "provider unavailable")
	require.Contains(t, summary, "Prior compacted context:")
	require.Contains(t, summary, "Original objective: keep unattended agent execution running")
	require.Contains(t, summary, "Continue optimizing.")
}

func TestMaybeCompactFallsBackToDeterministicSummaryWhenStreamRequestsToolCall(t *testing.T) {
	runtime := llm.NewLLMRuntime(&llm.RuntimeConfig{
		DefaultProvider: "provider-a",
		DefaultModel:    "gpt-5",
		MaxRetries:      0,
	})
	provider := &compactTestProvider{
		name: "provider-a",
		streamChunks: []llm.StreamChunk{
			{
				Type: llm.EventTypeDone,
				Done: true,
				Metadata: map[string]interface{}{
					"finish_reason": "tool_calls",
				},
			},
		},
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
		SessionID:          "session-compact-fallback-tool-call",
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
	require.Equal(t, 0, provider.callCount)
	require.Equal(t, 1, provider.streamCount)

	summary := requireCompactionMessage(t, result.ReplacementHistory)
	require.Equal(t, "deterministic_fallback", summary.Metadata.GetString("summary_source", ""))
	require.Contains(t, summary.Metadata.GetString("summary_fallback_reason", ""), "tool_calls")
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

func TestMaybeCompactUsesObservedTokensForTrigger(t *testing.T) {
	runtime := llm.NewLLMRuntime(&llm.RuntimeConfig{
		DefaultProvider: "provider-a",
		DefaultModel:    "gpt-5",
		MaxRetries:      0,
	})
	provider := &compactTestProvider{
		name: "provider-a",
		capabilities: map[string]agentconfig.ModelCapabilitySpec{
			"gpt-5": {MaxContextTokens: 5000, AutoCompactTokenLimit: 90},
		},
	}
	require.NoError(t, runtime.RegisterProvider("provider-a", provider))
	require.NoError(t, runtime.RegisterProviderAlias("gpt-5", "provider-a"))

	compactor := New(runtime, nil)
	result, status, err := compactor.MaybeCompact(context.Background(), Request{
		SessionID:         "session-observed",
		Provider:          "provider-a",
		Model:             "gpt-5",
		History:           compactTestHistory(),
		Phase:             PhasePreTurn,
		ObservedTokens:    95,
		HasObservedTokens: true,
		CountTokens: func(messages []types.Message) int {
			return 10
		},
	})
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, 95, status.TokenBefore)
	require.Equal(t, 95, result.TokenBefore)
	require.Equal(t, 1, provider.streamCount)
}

func TestMaybeCompactSkipsWhenObservedTokensBelowLimitEvenIfHistoryEstimateIsHigh(t *testing.T) {
	runtime := llm.NewLLMRuntime(&llm.RuntimeConfig{
		DefaultProvider: "provider-a",
		DefaultModel:    "gpt-5",
		MaxRetries:      0,
	})
	provider := &compactTestProvider{
		name: "provider-a",
		capabilities: map[string]agentconfig.ModelCapabilitySpec{
			"gpt-5": {MaxContextTokens: 100, AutoCompactTokenLimit: 90},
		},
	}
	require.NoError(t, runtime.RegisterProvider("provider-a", provider))
	require.NoError(t, runtime.RegisterProviderAlias("gpt-5", "provider-a"))

	compactor := New(runtime, nil)
	result, status, err := compactor.MaybeCompact(context.Background(), Request{
		SessionID:         "session-observed-skip",
		Provider:          "provider-a",
		Model:             "gpt-5",
		History:           compactTestHistory(),
		Phase:             PhasePreTurn,
		ObservedTokens:    80,
		HasObservedTokens: true,
		CountTokens: func(messages []types.Message) int {
			return 1000
		},
	})
	require.NoError(t, err)
	require.Nil(t, result)
	require.Equal(t, "below_limit", status.Reason)
	require.Equal(t, 80, status.TokenBefore)
	require.Equal(t, 0, provider.callCount)
}

func TestMaybeCompactTreatsExplicitZeroObservedTokensAsCurrentUsage(t *testing.T) {
	runtime := llm.NewLLMRuntime(&llm.RuntimeConfig{
		DefaultProvider: "provider-a",
		DefaultModel:    "gpt-5",
		MaxRetries:      0,
	})
	provider := &compactTestProvider{
		name: "provider-a",
		capabilities: map[string]agentconfig.ModelCapabilitySpec{
			"gpt-5": {MaxContextTokens: 100, AutoCompactTokenLimit: 90},
		},
	}
	require.NoError(t, runtime.RegisterProvider("provider-a", provider))
	require.NoError(t, runtime.RegisterProviderAlias("gpt-5", "provider-a"))

	compactor := New(runtime, nil)
	result, status, err := compactor.MaybeCompact(context.Background(), Request{
		SessionID:         "session-observed-zero",
		Provider:          "provider-a",
		Model:             "gpt-5",
		History:           compactTestHistory(),
		Phase:             PhasePreTurn,
		ObservedTokens:    0,
		HasObservedTokens: true,
		CountTokens: func(messages []types.Message) int {
			return 1000
		},
	})
	require.NoError(t, err)
	require.Nil(t, result)
	require.Equal(t, "below_limit", status.Reason)
	require.Equal(t, 0, status.TokenBefore)
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
	require.Equal(t, 1, provider.streamCount)

	second, _, err := compactor.MaybeCompact(context.Background(), request)
	require.NoError(t, err)
	require.NotNil(t, second)
	require.Len(t, second.CheckpointIDs, 1)
	require.Equal(t, 1, provider.streamCount)
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
	require.Equal(t, 1, provider.streamCount)
}

func TestMaybeCompactMissingCapabilityUsesDefaultWindowAndReportsResolvedProviderAndModel(t *testing.T) {
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
			return defaultAutoCompactContextWindow
		},
	})
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, "", status.Reason)
	require.Equal(t, "provider-a", status.ResolvedProvider)
	require.Equal(t, "gpt-5.5", status.ResolvedModel)
	require.Equal(t, defaultAutoCompactContextWindow, status.TokenBefore)
	require.Equal(t, defaultAutoCompactContextWindow, status.MaxContextTokens)
	require.Equal(t, int(math.Floor(float64(defaultAutoCompactContextWindow)*defaultAutoCompactRatio)), status.TriggerTokenLimit)
	require.Equal(t, 1, provider.streamCount)
}

func TestMaybeCompactMissingCapabilitySkipsBelowDefaultWindow(t *testing.T) {
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
		SessionID:         "session-compact-missing-capability-below-default",
		Model:             "gpt-5.5",
		History:           compactTestHistory(),
		Phase:             PhasePreTurn,
		ObservedTokens:    0,
		HasObservedTokens: true,
		CountTokens: func(messages []types.Message) int {
			return defaultAutoCompactContextWindow
		},
	})
	require.NoError(t, err)
	require.Nil(t, result)
	require.Equal(t, "below_limit", status.Reason)
	require.Equal(t, "provider-a", status.ResolvedProvider)
	require.Equal(t, "gpt-5.5", status.ResolvedModel)
	require.Equal(t, 0, status.TokenBefore)
	require.Equal(t, defaultAutoCompactContextWindow, status.MaxContextTokens)
	require.Equal(t, 0, provider.callCount)
}

func TestMaybeCompactCapabilityWithoutContextLimitFallsBackToDefaultWindow(t *testing.T) {
	runtime := llm.NewLLMRuntime(&llm.RuntimeConfig{
		DefaultProvider: "provider-a",
		DefaultModel:    "deepseek-v4-pro",
		MaxRetries:      0,
	})
	provider := &compactTestProvider{
		name: "provider-a",
		capabilities: map[string]agentconfig.ModelCapabilitySpec{
			"deepseek-v4-pro": {
				ReasoningModel: true,
				ReasoningEfforts: []string{
					"high",
					"max",
				},
			},
		},
	}
	require.NoError(t, runtime.RegisterProvider("provider-a", provider))
	require.NoError(t, runtime.RegisterProviderAlias("deepseek-v4-pro", "provider-a"))

	compactor := New(runtime, nil)
	result, status, err := compactor.MaybeCompact(context.Background(), Request{
		SessionID:         "session-compact-partial-capability",
		Model:             "deepseek-v4-pro",
		History:           compactTestHistory(),
		Phase:             PhasePreTurn,
		ObservedTokens:    defaultAutoCompactContextWindow,
		HasObservedTokens: true,
		CountTokens: func(messages []types.Message) int {
			return 10
		},
	})
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, "", status.Reason)
	require.Equal(t, "provider-a", status.ResolvedProvider)
	require.Equal(t, "deepseek-v4-pro", status.ResolvedModel)
	require.Equal(t, defaultAutoCompactContextWindow, status.TokenBefore)
	require.Equal(t, defaultAutoCompactContextWindow, status.MaxContextTokens)
	require.Equal(t, int(math.Floor(float64(defaultAutoCompactContextWindow)*defaultAutoCompactRatio)), status.TriggerTokenLimit)
	require.Equal(t, 1, provider.streamCount)
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
	require.Equal(t, 1, provider.streamCount)
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
	require.Equal(t, 1, provider.streamCount)
	require.NotEmpty(t, result.ReplacementHistory)

	summaryMessage := requireCompactionMessage(t, result.ReplacementHistory)
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
	require.Len(t, result.ReplacementHistory, 3)
	require.Equal(t, "system", result.ReplacementHistory[0].Role)
	require.Equal(t, "compaction", result.ReplacementHistory[1].Metadata["context_stage"])
	require.Equal(t, "Continue from the latest findings.", result.ReplacementHistory[2].Content)
}

func TestMaybeCompactMidTurnKeepsRealUsersAndLatestToolReplayBeforeSummary(t *testing.T) {
	runtime := llm.NewLLMRuntime(&llm.RuntimeConfig{
		DefaultProvider: "provider-a",
		DefaultModel:    "gpt-5",
		MaxRetries:      0,
	})
	provider := &compactTestProvider{
		name:            "provider-a",
		responseContent: "Root cause confirmed. Continue by applying and testing the fix.",
		capabilities: map[string]agentconfig.ModelCapabilitySpec{
			"gpt-5": {AutoCompactTokenLimit: 100},
		},
	}
	require.NoError(t, runtime.RegisterProvider("provider-a", provider))
	require.NoError(t, runtime.RegisterProviderAlias("gpt-5", "provider-a"))

	staleSummary := buildCompactionMessage("stale summary", "", 0, 2, PhasePreTurn)
	firstCall := types.NewAssistantMessage("Inspect the first log.")
	firstCall.ToolCalls = []types.ToolCall{{ID: "call-1", Name: "read", Args: map[string]interface{}{"path": "first.log"}}}
	latestCall := types.NewAssistantMessage("Verify the latest result.")
	latestCall.ToolCalls = []types.ToolCall{{ID: "call-2", Name: "read", Args: map[string]interface{}{"path": "latest.log"}}}
	staleWorkspace := types.NewUserMessage("stale workspace wrapper")
	staleWorkspace.Metadata["context_stage"] = "workspace"
	history := []types.Message{
		*types.NewSystemMessage("current canonical instructions"),
		*types.NewUserMessage("Fix the original build failure."),
		*staleSummary,
		*staleWorkspace,
		*firstCall,
		*types.NewToolMessage("call-1", "first result"),
		*types.NewUserMessage("Preserve compatibility while fixing it."),
		*latestCall,
		*types.NewToolMessage("call-2", "latest verified result"),
	}
	counter := func(messages []types.Message) int {
		total := 0
		for _, message := range messages {
			total += len(message.Content) + len(message.ToolCalls)*20
		}
		return total
	}

	result, _, err := New(runtime, nil).MaybeCompact(context.Background(), Request{
		SessionID:             "session-mid-turn-shape",
		Model:                 "gpt-5",
		Force:                 true,
		History:               history,
		KeepRecentMessages:    2,
		ReplacementTokenLimit: 2000,
		Phase:                 PhaseMidTurn,
		CountTokens:           counter,
	})
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, PhaseMidTurn, result.Phase)
	require.Len(t, result.ReplacementHistory, 6)
	require.Equal(t, "current canonical instructions", result.ReplacementHistory[0].Content)
	require.Equal(t, "Fix the original build failure.", result.ReplacementHistory[1].Content)
	require.Equal(t, "Preserve compatibility while fixing it.", result.ReplacementHistory[2].Content)
	require.Equal(t, "call-2", result.ReplacementHistory[3].ToolCalls[0].ID)
	require.Equal(t, "call-2", result.ReplacementHistory[4].ToolCallID)
	require.Equal(t, "compaction", result.ReplacementHistory[5].Metadata.GetString("context_stage", ""))
	require.Equal(t, PhaseMidTurn, result.ReplacementHistory[5].Metadata.GetString("compact_phase", ""))
	require.Equal(t, "provider", result.ReplacementHistory[5].Metadata.GetString("summary_source", ""))
	require.Contains(t, result.ReplacementHistory[5].Content, "Root cause confirmed")
	require.NotNil(t, provider.lastRequest)
	for _, message := range provider.lastRequest.Messages {
		require.NotEqual(t, "stale workspace wrapper", message.Content)
	}
	for _, message := range result.ReplacementHistory {
		require.NotEqual(t, "stale summary", message.Content)
		require.NotEqual(t, "stale workspace wrapper", message.Content)
	}
	require.NotContains(t, buildDeterministicCompactSummary(history[1:], "provider unavailable"), "stale workspace wrapper")
}

func TestMaybeCompactRepeatedMidTurnReplacesPriorSummaryInsteadOfStacking(t *testing.T) {
	runtime := llm.NewLLMRuntime(&llm.RuntimeConfig{DefaultProvider: "provider-a", DefaultModel: "gpt-5", MaxRetries: 0})
	provider := &compactTestProvider{
		name:            "provider-a",
		responseContent: "first semantic checkpoint",
		capabilities: map[string]agentconfig.ModelCapabilitySpec{
			"gpt-5": {AutoCompactTokenLimit: 100},
		},
	}
	require.NoError(t, runtime.RegisterProvider("provider-a", provider))
	require.NoError(t, runtime.RegisterProviderAlias("gpt-5", "provider-a"))
	counter := func(messages []types.Message) int {
		total := 0
		for _, message := range messages {
			total += len(message.Content) + len(message.ToolCalls)*20
		}
		return total
	}
	firstCall := types.NewAssistantMessage("inspect")
	firstCall.ToolCalls = []types.ToolCall{{ID: "call-1", Name: "read"}}
	first, _, err := New(runtime, nil).MaybeCompact(context.Background(), Request{
		SessionID: "session-repeated-mid-turn", Model: "gpt-5", Force: true, Phase: PhaseMidTurn,
		History: []types.Message{
			*types.NewSystemMessage("canonical"),
			*types.NewUserMessage("complete the original task"),
			*firstCall,
			*types.NewToolMessage("call-1", "first evidence"),
		},
		KeepRecentMessages: 2, ReplacementTokenLimit: 2000, CountTokens: counter,
	})
	require.NoError(t, err)
	require.NotNil(t, first)

	secondCall := types.NewAssistantMessage("verify")
	secondCall.ToolCalls = []types.ToolCall{{ID: "call-2", Name: "read"}}
	secondHistory := append(cloneMessages(first.ReplacementHistory), *secondCall, *types.NewToolMessage("call-2", "second evidence"))
	provider.responseContent = "second semantic checkpoint"
	second, _, err := New(runtime, nil).MaybeCompact(context.Background(), Request{
		SessionID: "session-repeated-mid-turn", Model: "gpt-5", Force: true, Phase: PhaseMidTurn,
		History: secondHistory, KeepRecentMessages: 2, ReplacementTokenLimit: 2000, CountTokens: counter,
	})
	require.NoError(t, err)
	require.NotNil(t, second)

	compactionCount := 0
	userGoalCount := 0
	for _, message := range second.ReplacementHistory {
		if isCompactionMessage(message) {
			compactionCount++
		}
		if message.Content == "complete the original task" {
			userGoalCount++
		}
		require.NotContains(t, message.Content, "first semantic checkpoint")
	}
	require.Equal(t, 1, compactionCount)
	require.Equal(t, 1, userGoalCount)
	require.Contains(t, second.ReplacementHistory[len(second.ReplacementHistory)-1].Content, "second semantic checkpoint")
}

func TestMaybeCompactReplacementShrinksAndDoesNotAccumulateSummaries(t *testing.T) {
	runtime := llm.NewLLMRuntime(&llm.RuntimeConfig{
		DefaultProvider: "provider-a",
		DefaultModel:    "gpt-5",
		MaxRetries:      0,
	})
	provider := &compactTestProvider{
		name: "provider-a",
		capabilities: map[string]agentconfig.ModelCapabilitySpec{
			"gpt-5": {AutoCompactTokenLimit: 100},
		},
	}
	require.NoError(t, runtime.RegisterProvider("provider-a", provider))
	require.NoError(t, runtime.RegisterProviderAlias("gpt-5", "provider-a"))

	history := []types.Message{*types.NewSystemMessage("system")}
	for index := 0; index < 8; index++ {
		history = append(history,
			*types.NewUserMessage(fmt.Sprintf("request-%d %s", index, strings.Repeat("u", 400))),
			*types.NewAssistantMessage(fmt.Sprintf("progress-%d %s", index, strings.Repeat("a", 400))),
		)
	}
	counter := func(messages []types.Message) int {
		total := 0
		for _, message := range messages {
			total += len(message.Content)
		}
		return total
	}
	compactor := New(runtime, nil)
	result, _, err := compactor.MaybeCompact(context.Background(), Request{
		SessionID: "session-bounded-replacement", Model: "gpt-5", History: history,
		KeepRecentMessages: 2, CountTokens: counter,
	})
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Less(t, result.TokenAfter, result.TokenBefore)
	require.Equal(t, len(history)-1-2, result.CompactedMessages)

	second, _, err := compactor.MaybeCompact(context.Background(), Request{
		SessionID: "session-bounded-replacement", Model: "gpt-5", Force: true,
		History: result.ReplacementHistory, KeepRecentMessages: 2, CountTokens: counter,
	})
	require.NoError(t, err)
	require.NotNil(t, second)
	compactionCount := 0
	for _, message := range second.ReplacementHistory {
		if message.Metadata.GetString("context_stage", "") == "compaction" {
			compactionCount++
		}
	}
	require.Equal(t, 1, compactionCount)
}

func requireCompactionMessage(t *testing.T, messages []types.Message) types.Message {
	t.Helper()
	for _, message := range messages {
		if message.Metadata.GetString("context_stage", "") == "compaction" {
			return message
		}
	}
	require.FailNow(t, "compaction message not found")
	return types.Message{}
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
