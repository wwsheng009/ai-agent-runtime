package llm

import (
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
)

// TestProviderWrapperResolveRequestMaxTokens 锁定 API 宿主补齐输出预算的口径：
// 推理模型取模型默认预算（不受 8k 槽位预留），常规模型保留 8k 预留；解析不到
// 时返回 false，由调用方保持既有兜底。
func TestProviderWrapperResolveRequestMaxTokens(t *testing.T) {
	reasoningProvider := &ProviderWrapper{config: &ProviderConfig{
		Type: "openai",
		ModelCapabilities: map[string]agentconfig.ModelCapabilitySpec{
			"budget-probe-reasoning": {ReasoningModel: true},
		},
	}}
	budget, ok := reasoningProvider.ResolveRequestMaxTokens("budget-probe-reasoning")
	if !ok || budget != DefaultModelMaxOutputTokens {
		t.Fatalf("reasoning model budget = %d ok=%v, want %d", budget, ok, DefaultModelMaxOutputTokens)
	}

	regularProvider := &ProviderWrapper{config: &ProviderConfig{
		Type: "openai",
		ModelCapabilities: map[string]agentconfig.ModelCapabilitySpec{
			"budget-probe-regular": {},
		},
	}}
	budget, ok = regularProvider.ResolveRequestMaxTokens("budget-probe-regular")
	if !ok || budget != CappedDefaultMaxTokens {
		t.Fatalf("regular model budget = %d ok=%v, want %d", budget, ok, CappedDefaultMaxTokens)
	}

	// capability 声明的上限低于默认预算时按上限收敛（非推理模型再套 8k 预留）。
	smallProvider := &ProviderWrapper{config: &ProviderConfig{
		Type: "openai",
		ModelCapabilities: map[string]agentconfig.ModelCapabilitySpec{
			"budget-probe-small": {MaxTokens: 4096},
		},
	}}
	budget, ok = smallProvider.ResolveRequestMaxTokens("budget-probe-small")
	if !ok || budget != 4096 {
		t.Fatalf("small-capability budget = %d ok=%v, want 4096", budget, ok)
	}

	var nilProvider *ProviderWrapper
	if _, ok := nilProvider.ResolveRequestMaxTokens("budget-probe-regular"); ok {
		t.Fatalf("nil provider must not resolve a budget")
	}
}
