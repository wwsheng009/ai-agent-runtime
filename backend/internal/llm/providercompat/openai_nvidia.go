package providercompat

import "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"

type nvidiaOpenAIAdapter struct {
	BaseAdapter
}

func (nvidiaOpenAIAdapter) Name() string {
	return "openai-nvidia"
}

func (nvidiaOpenAIAdapter) Match(ctx Context) bool {
	return IsNVIDIA(ctx.ProviderName, ctx.BaseURL)
}

func (nvidiaOpenAIAdapter) DefaultRuntimeCapability(Context) (agentconfig.ModelCapabilitySpec, bool) {
	return agentconfig.ModelCapabilitySpec{
		ReasoningModel:   true,
		ReasoningEfforts: []string{"minimal", "low", "medium", "high"},
	}, true
}

func (nvidiaOpenAIAdapter) DefaultLoginReasoningEfforts(Context) ([]string, bool) {
	return []string{"minimal", "low", "medium", "high"}, true
}

func (nvidiaOpenAIAdapter) LoginModelUsesDefaultReasoningEfforts(Context, string) (bool, bool) {
	return true, true
}
