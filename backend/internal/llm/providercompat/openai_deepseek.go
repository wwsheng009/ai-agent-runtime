package providercompat

import (
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
	"github.com/wwsheng009/ai-agent-runtime/internal/types"
)

type deepSeekOpenAIAdapter struct {
	BaseAdapter
}

func (deepSeekOpenAIAdapter) Name() string {
	return "openai-deepseek"
}

func (deepSeekOpenAIAdapter) Match(ctx Context) bool {
	return IsDeepSeek(ctx.ProviderName, ctx.BaseURL, ctx.Model)
}

func (deepSeekOpenAIAdapter) DefaultRuntimeCapability(Context) (agentconfig.ModelCapabilitySpec, bool) {
	return agentconfig.ModelCapabilitySpec{
		ReasoningModel:   true,
		ReasoningEfforts: []string{"high", "max"},
	}, true
}

func (deepSeekOpenAIAdapter) DefaultLoginReasoningEfforts(Context) ([]string, bool) {
	return []string{"high", "max"}, true
}

func (deepSeekOpenAIAdapter) LoginModelHint(_ Context, modelID string) (bool, bool) {
	if IsDeepSeekModel(modelID) {
		return true, true
	}
	return false, false
}

func (deepSeekOpenAIAdapter) LoginModelUsesDefaultReasoningEfforts(_ Context, modelID string) (bool, bool) {
	if IsDeepSeekModel(modelID) {
		return true, true
	}
	return false, false
}

func (deepSeekOpenAIAdapter) ReplayableOpenAIReasoningContent(ctx Context, toolCalls []map[string]interface{}, reasoning *types.ReasoningBlock) (string, bool) {
	provider := ctx.ProviderName
	if reasoning != nil {
		if normalized := strings.ToLower(strings.TrimSpace(reasoning.Provider)); normalized != "" {
			provider = normalized
		}
		if IsDeepSeek(provider, ctx.BaseURL, ctx.Model) || IsDeepSeek("", "", reasoning.Provider) {
			return reasoning.RawDisplayText(), true
		}
		return "", false
	}
	// DeepSeek thinking mode rejects requests where an assistant message in a
	// tool-calling conversation lacks the reasoning_content key entirely
	// ("The reasoning_content in the thinking mode must be passed back to the
	// API", HTTP 400). The upstream accepts an explicit empty value, so once
	// the provider is DeepSeek every assistant message must carry the key —
	// regardless of whether this turn had tool calls or persisted reasoning.
	// Without this, plain-text assistant turns inside a tool loop (no
	// tool_calls, reasoning lost on replay) fail the whole request.
	if IsDeepSeek(provider, ctx.BaseURL, ctx.Model) {
		return "", true
	}
	return "", false
}
