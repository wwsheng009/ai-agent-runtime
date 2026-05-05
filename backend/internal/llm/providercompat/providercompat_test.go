package providercompat

import (
	"reflect"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/internal/types"
)

func TestDefaultRuntimeCapability_SensenovaNVIDIAAndDeepSeek(t *testing.T) {
	spec, ok := DefaultRuntimeCapability(Context{
		Protocol: "openai",
		BaseURL:  "https://token.sensenova.cn/v1",
	})
	if !ok {
		t.Fatal("expected sensenova runtime capability")
	}
	if !spec.ReasoningModel || !reflect.DeepEqual(spec.ReasoningEfforts, []string{"low", "medium", "high", "none"}) {
		t.Fatalf("unexpected sensenova capability: %#v", spec)
	}

	spec, ok = DefaultRuntimeCapability(Context{
		Protocol: "openai",
		BaseURL:  "https://integrate.api.nvidia.com",
	})
	if !ok {
		t.Fatal("expected nvidia runtime capability")
	}
	if !spec.ReasoningModel || !reflect.DeepEqual(spec.ReasoningEfforts, []string{"minimal", "low", "medium", "high"}) {
		t.Fatalf("unexpected nvidia capability: %#v", spec)
	}

	spec, ok = DefaultRuntimeCapability(Context{
		Protocol:     "openai",
		ProviderName: "deepseek",
		BaseURL:      "https://api.deepseek.com",
	})
	if !ok {
		t.Fatal("expected deepseek runtime capability")
	}
	if !spec.ReasoningModel || !reflect.DeepEqual(spec.ReasoningEfforts, []string{"high", "max"}) {
		t.Fatalf("unexpected deepseek capability: %#v", spec)
	}
}

func TestDefaultLoginReasoningEfforts(t *testing.T) {
	if got := DefaultLoginReasoningEfforts(Context{Protocol: "openai"}); !reflect.DeepEqual(got, []string{"low", "medium", "high", "xhigh", "none"}) {
		t.Fatalf("unexpected openai defaults: %#v", got)
	}
	if got := DefaultLoginReasoningEfforts(Context{Protocol: "openai", ProviderName: "sensenova", BaseURL: "https://token.sensenova.cn/v1"}); !reflect.DeepEqual(got, []string{"low", "medium", "high", "none"}) {
		t.Fatalf("unexpected sensenova defaults: %#v", got)
	}
	if got := DefaultLoginReasoningEfforts(Context{Protocol: "openai", ProviderName: "deepseek", BaseURL: "https://api.deepseek.com"}); !reflect.DeepEqual(got, []string{"high", "max"}) {
		t.Fatalf("unexpected deepseek defaults: %#v", got)
	}
}

func TestAdapterRegistryPrecedenceAndCodexWildcard(t *testing.T) {
	chain := NewChain(Context{
		Protocol:     "openai",
		ProviderName: "sensenova",
		BaseURL:      "https://token.sensenova.cn/v1",
	})
	if len(chain.adapters) < 2 {
		t.Fatalf("expected sensenova and openai adapters, got %d", len(chain.adapters))
	}
	if got := chain.adapters[0].Name(); got != "openai-sensenova" {
		t.Fatalf("expected sensenova adapter before generic openai adapter, got %q", got)
	}
	if got := chain.DefaultLoginReasoningEfforts(); !reflect.DeepEqual(got, []string{"low", "medium", "high", "none"}) {
		t.Fatalf("expected provider-specific defaults to win, got %#v", got)
	}

	codexPath := Context{Protocol: "openai", BaseURL: "https://example.com/backend-api/codex/responses"}
	if !LoginUsesWildcardReasoningEfforts(codexPath) {
		t.Fatal("expected codex path wildcard reasoning efforts to be preserved")
	}
	if !LoginModelUsesDefaultReasoningEfforts(codexPath, "plain-model") {
		t.Fatal("expected codex path wildcard to apply defaults to discovered models")
	}
}

func TestLoginModelUsesDefaultReasoningEfforts(t *testing.T) {
	ctx := Context{Protocol: "openai", ProviderName: "sensenova", BaseURL: "https://token.sensenova.cn/v1"}
	if !LoginModelUsesDefaultReasoningEfforts(ctx, "sensenova-6.7-flash-lite") {
		t.Fatal("expected sensenova model to use defaults")
	}
	if LoginModelUsesDefaultReasoningEfforts(Context{Protocol: "openai"}, "plain-model") {
		t.Fatal("did not expect plain model to use defaults")
	}
	if !LoginModelUsesDefaultReasoningEfforts(Context{Protocol: "openai"}, "gpt-5.4-mini") {
		t.Fatal("expected openai reasoning model to use defaults")
	}
}

func TestNormalizeOpenAICompatibleMessages(t *testing.T) {
	messages := []map[string]interface{}{
		{"role": "system", "content": "first"},
		{"role": "system", "content": "second"},
		{"role": "user", "content": "ls"},
	}
	got := NormalizeOpenAICompatibleMessages(Context{Protocol: "openai", ProviderName: "sensenova", BaseURL: "https://token.sensenova.cn/v1"}, messages)
	if len(got) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(got))
	}
	if got[0]["content"] != "first\n\nsecond" {
		t.Fatalf("unexpected merged system content: %#v", got[0]["content"])
	}
	if got[1]["role"] != "user" {
		t.Fatalf("unexpected user message: %#v", got[1])
	}
}

func TestNormalizeToolCallArgumentsAndCodexSupport(t *testing.T) {
	if got := NormalizeToolCallArguments("null"); got != "{}" {
		t.Fatalf("expected null tool args to normalize to {}, got %q", got)
	}
	if got := NormalizeToolCallArguments("  "); got != "{}" {
		t.Fatalf("expected blank tool args to normalize to {}, got %q", got)
	}
	if got := NormalizeToolCallArguments(`{"path":"."}`); got != `{"path":"."}` {
		t.Fatalf("expected tool args to stay intact, got %q", got)
	}
	if SupportsMaxOutputTokens("https://chatgpt.com/backend-api/codex/responses", nil) {
		t.Fatal("expected chatgpt codex backend to disable max_output_tokens")
	}
	enabled := true
	if !SupportsMaxOutputTokens("https://chatgpt.com/backend-api/codex/responses", &enabled) {
		t.Fatal("expected explicit override to win")
	}
}

func TestNormalizeAssistantMessage_OpenAIToolCallsAndReasoning(t *testing.T) {
	message := map[string]interface{}{
		"role":      "assistant",
		"content":   "",
		"reasoning": "think",
		"tool_calls": []map[string]interface{}{
			{
				"id": "call_1",
				"function": map[string]interface{}{
					"name": "first",
					"arguments": map[string]interface{}{
						"path": ".",
					},
				},
			},
			{
				"name":      "legacy",
				"arguments": nil,
			},
		},
	}

	normalized := NormalizeAssistantMessage(Context{Protocol: "openai"}, message)
	if normalized["reasoning_content"] != "think" {
		t.Fatalf("expected reasoning_content to be populated, got %#v", normalized)
	}
	toolCalls, ok := normalized["tool_calls"].([]map[string]interface{})
	if !ok || len(toolCalls) != 2 {
		t.Fatalf("expected normalized tool_calls, got %#v", normalized["tool_calls"])
	}
	firstFn, _ := toolCalls[0]["function"].(map[string]interface{})
	if got := firstFn["arguments"]; got != `{"path":"."}` {
		t.Fatalf("expected object arguments to be encoded, got %#v", got)
	}
	secondFn, _ := toolCalls[1]["function"].(map[string]interface{})
	if secondFn["name"] != "legacy" || secondFn["arguments"] != "{}" {
		t.Fatalf("expected legacy function_call shape to normalize, got %#v", secondFn)
	}
	if toolCalls[1]["type"] != "function" {
		t.Fatalf("expected legacy tool call type=function, got %#v", toolCalls[1]["type"])
	}
}

func TestReplayableOpenAIReasoningContent(t *testing.T) {
	reasoning := &types.ReasoningBlock{
		Provider:       "deepseek",
		Summary:        "think",
		ReplayRequired: false,
	}

	got, ok := ReplayableOpenAIReasoningContent(Context{}, nil, reasoning)
	if !ok {
		t.Fatal("expected deepseek reasoning to be replayable")
	}
	if got != "think" {
		t.Fatalf("expected raw reasoning text to be replayed, got %q", got)
	}

	got, ok = ReplayableOpenAIReasoningContent(Context{ProviderName: "deepseek"}, []map[string]interface{}{{"role": "assistant"}}, nil)
	if !ok {
		t.Fatal("expected deepseek tool replay to be replayable")
	}
	if got != "" {
		t.Fatalf("expected empty reasoning text for replay without reasoning, got %q", got)
	}
}

func TestLooksLikeOpenAIReasoningModel(t *testing.T) {
	if !LooksLikeOpenAIReasoningModel("gpt-5.4-mini") {
		t.Fatal("expected gpt-5 model to look like an openai reasoning model")
	}
	if LooksLikeOpenAIReasoningModel("plain-model") {
		t.Fatal("did not expect plain model to look like reasoning model")
	}
	if !IsDeepSeekModel("deepseek-v4-pro") {
		t.Fatal("expected deepseek model to be detected separately")
	}
}
