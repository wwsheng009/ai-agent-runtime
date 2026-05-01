package commands

import (
	"encoding/json"
	"testing"

	config "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
	runtimechatcore "github.com/wwsheng009/ai-agent-runtime/internal/chatcore"
	"github.com/wwsheng009/ai-agent-runtime/internal/llm/adapter"
)

// TestAdapterRequestConfig_PopulatesModelCapabilityFields 验证 adapterRequestConfig
// 从 session.Provider.ModelCapabilities 中正确填充 ReasoningEffortBudgets 与
// ReasoningModel 字段，以便协议 adapter 可以在请求层派生 thinking/reasoning 配置。
func TestAdapterRequestConfig_PopulatesModelCapabilityFields(t *testing.T) {
	session := &ChatSession{
		ProviderName: "anthropic",
		Provider: config.Provider{
			Enabled:  true,
			Protocol: "anthropic",
			ModelCapabilities: map[string]config.ModelCapabilitySpec{
				"claude-sonnet-4-6": {
					ReasoningModel:   true,
					ReasoningEfforts: []string{"low", "medium", "high"},
					ReasoningEffortBudgets: map[string]int{
						"low":    4096,
						"medium": 8192,
						"high":   16384,
					},
				},
			},
		},
		Adapter:         adapter.GetAdapterOrDefault("anthropic"),
		Model:           "claude-sonnet-4-6",
		ReasoningEffort: "high",
	}

	cfg := adapterRequestConfig(session, nil, runtimechatcore.ProviderTurnRequest{Stream: false})

	if cfg.ReasoningEffort != "high" {
		t.Fatalf("expected reasoning_effort=high, got %q", cfg.ReasoningEffort)
	}
	if !cfg.ReasoningModel {
		t.Fatal("expected reasoning_model=true from capability")
	}
	if got := cfg.ReasoningEffortBudgets["high"]; got != 16384 {
		t.Fatalf("expected budgets[high]=16384, got %d", got)
	}
	if len(cfg.ReasoningEffortBudgets) != 3 {
		t.Fatalf("expected 3 budget entries, got %d", len(cfg.ReasoningEffortBudgets))
	}
}

// TestAdapterRequestConfig_AnthropicBuildRequestDerivesThinking 验证 Anthropic
// adapter 在请求层使用 ReasoningEffort + ReasoningEffortBudgets 生成 thinking 配置，
// 而不是依赖会话层直接传入协议特定字段。
func TestAdapterRequestConfig_AnthropicBuildRequestDerivesThinking(t *testing.T) {
	session := &ChatSession{
		ProviderName: "anthropic",
		Provider: config.Provider{
			Enabled:  true,
			Protocol: "anthropic",
			ModelCapabilities: map[string]config.ModelCapabilitySpec{
				"claude-sonnet-4-6": {
					ReasoningModel: true,
					ReasoningEffortBudgets: map[string]int{
						"high": 16384,
					},
				},
			},
		},
		Adapter:         adapter.GetAdapterOrDefault("anthropic"),
		Model:           "claude-sonnet-4-6",
		ReasoningEffort: "high",
	}

	cfg := adapterRequestConfig(session, []map[string]interface{}{{"role": "user", "content": "hi"}}, runtimechatcore.ProviderTurnRequest{Stream: false})
	body := session.Adapter.BuildRequest(cfg)

	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal anthropic request body: %v", err)
	}
	var normalized map[string]interface{}
	if err := json.Unmarshal(raw, &normalized); err != nil {
		t.Fatalf("unmarshal anthropic request body: %v", err)
	}
	thinking, ok := normalized["thinking"].(map[string]interface{})
	if !ok {
		t.Fatalf("expected thinking map in anthropic request, body=%s", raw)
	}
	if got, _ := thinking["type"].(string); got != "enabled" {
		t.Fatalf("expected thinking.type=enabled, got %v", thinking["type"])
	}
	budget, ok := thinking["budget_tokens"].(float64)
	if !ok || int(budget) != 16384 {
		t.Fatalf("expected budget_tokens=16384, got %#v", thinking["budget_tokens"])
	}
}

// TestAdapterRequestConfig_GeminiBuildRequestDerivesThinkingConfig 验证 Gemini
// adapter 在请求层生成 thinkingConfig.thinkingBudget，会话层不需要感知协议差异。
func TestAdapterRequestConfig_GeminiBuildRequestDerivesThinkingConfig(t *testing.T) {
	session := &ChatSession{
		ProviderName: "gemini",
		Provider: config.Provider{
			Enabled:  true,
			Protocol: "gemini",
			ModelCapabilities: map[string]config.ModelCapabilitySpec{
				"gemini-2.5-pro": {
					ReasoningModel: true,
					ReasoningEffortBudgets: map[string]int{
						"medium": 8192,
					},
				},
			},
		},
		Adapter:         adapter.GetAdapterOrDefault("gemini"),
		Model:           "gemini-2.5-pro",
		ReasoningEffort: "medium",
	}

	cfg := adapterRequestConfig(session, []map[string]interface{}{{"role": "user", "content": "hi"}}, runtimechatcore.ProviderTurnRequest{Stream: false})
	body := session.Adapter.BuildRequest(cfg)

	generation, ok := body["generationConfig"].(map[string]interface{})
	if !ok {
		raw, _ := json.Marshal(body)
		t.Fatalf("expected generationConfig in gemini request, body=%s", raw)
	}
	thinkingConfig, ok := generation["thinkingConfig"].(map[string]interface{})
	if !ok {
		raw, _ := json.Marshal(body)
		t.Fatalf("expected thinkingConfig inside gemini generationConfig, body=%s", raw)
	}
	switch budget := thinkingConfig["thinkingBudget"].(type) {
	case int:
		if budget != 8192 {
			t.Fatalf("expected thinkingBudget=8192, got %d", budget)
		}
	case float64:
		if int(budget) != 8192 {
			t.Fatalf("expected thinkingBudget=8192, got %v", budget)
		}
	default:
		t.Fatalf("unexpected thinkingBudget type %T value=%#v", thinkingConfig["thinkingBudget"], thinkingConfig["thinkingBudget"])
	}
}
