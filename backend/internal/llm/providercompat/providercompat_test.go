package providercompat

import (
	"io"
	"reflect"
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
	llmadapter "github.com/wwsheng009/ai-agent-runtime/internal/llm/adapter"
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

func TestMergeCapabilitiesAddsProviderWildcardFallback(t *testing.T) {
	configured := map[string]agentconfig.ModelCapabilitySpec{
		"custom-model": {
			MaxTokens: 1024,
		},
	}
	merged := MergeCapabilities(Context{
		Protocol:     "openai",
		ProviderName: "sensenova",
		BaseURL:      "https://token.sensenova.cn/v1",
	}, configured)

	if _, exists := configured["*"]; exists {
		t.Fatal("expected configured capabilities not to be mutated")
	}
	if merged["custom-model"].MaxTokens != 1024 {
		t.Fatalf("expected configured model capability to be preserved, got %#v", merged["custom-model"])
	}
	wildcard, ok := merged["*"]
	if !ok {
		t.Fatalf("expected wildcard fallback capability, got %#v", merged)
	}
	if !wildcard.ReasoningModel || !reflect.DeepEqual(wildcard.ReasoningEfforts, []string{"low", "medium", "high", "none"}) {
		t.Fatalf("unexpected wildcard fallback: %#v", wildcard)
	}
}

func TestMergeCapabilitiesKeepsConfiguredWildcard(t *testing.T) {
	configured := map[string]agentconfig.ModelCapabilitySpec{
		"*": {
			ReasoningModel:   true,
			ReasoningEfforts: []string{"custom"},
		},
	}
	merged := MergeCapabilities(Context{
		Protocol: "openai",
		BaseURL:  "https://integrate.api.nvidia.com",
	}, configured)
	if !reflect.DeepEqual(merged["*"].ReasoningEfforts, []string{"custom"}) {
		t.Fatalf("expected configured wildcard to win, got %#v", merged["*"])
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

func TestLoginModelHintPrefersProviderSpecificModel(t *testing.T) {
	got := LoginModelHint(Context{}, []string{"plain-model", "deepseek-v4-pro"})
	if got != "deepseek-v4-pro" {
		t.Fatalf("expected provider-specific login model hint, got %q", got)
	}
	if got := LoginModelHint(Context{}, []string{"plain-model"}); got != "plain-model" {
		t.Fatalf("expected first model fallback, got %q", got)
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

func TestPrepareRequestBody_OpenAIToolCallArguments(t *testing.T) {
	body := map[string]interface{}{
		"model": "test-model",
		"messages": []map[string]interface{}{
			{
				"role": "assistant",
				"tool_calls": []map[string]interface{}{
					{
						"id": "call_1",
						"function": map[string]interface{}{
							"name":      "list_files",
							"arguments": nil,
						},
					},
				},
			},
		},
	}

	normalized := PrepareRequestBody(Context{Protocol: "openai"}, body)
	messages := normalized["messages"].([]map[string]interface{})
	toolCalls := messages[0]["tool_calls"].([]map[string]interface{})
	fn := toolCalls[0]["function"].(map[string]interface{})
	if fn["arguments"] != "{}" {
		t.Fatalf("expected request body tool arguments to normalize, got %#v", fn["arguments"])
	}
}

func TestPrepareRequestBody_ChatGPTCodexDropsMaxOutputTokens(t *testing.T) {
	body := map[string]interface{}{
		"model":             "gpt-5-codex",
		"input":             "hi",
		"max_output_tokens": 100,
	}
	normalized := PrepareRequestBody(Context{
		Protocol: "codex",
		BaseURL:  "https://chatgpt.com/backend-api/codex/responses",
	}, body)
	if _, exists := normalized["max_output_tokens"]; exists {
		t.Fatalf("expected max_output_tokens to be dropped, got %#v", normalized)
	}
	if _, exists := body["max_output_tokens"]; !exists {
		t.Fatal("expected original body not to be mutated")
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

func TestNormalizeProcessResult_OpenAIToolCallsAndReasoningBlock(t *testing.T) {
	result := &llmadapter.ProcessResult{
		Reasoning:        "think",
		ReasoningPresent: true,
		HasToolCalls:     true,
		ToolCalls: []map[string]interface{}{
			{
				"id": "call_1",
				"function": map[string]interface{}{
					"name":      "list_files",
					"arguments": map[string]interface{}{"path": "."},
				},
			},
		},
	}

	NormalizeProcessResult(Context{Protocol: "openai"}, result)
	fn, _ := result.ToolCalls[0]["function"].(map[string]interface{})
	if fn["arguments"] != `{"path":"."}` {
		t.Fatalf("expected tool arguments to be encoded, got %#v", fn["arguments"])
	}
	if result.ReasoningBlock == nil || result.ReasoningBlock.DisplayText() != "think" {
		t.Fatalf("expected reasoning block to be populated, got %#v", result.ReasoningBlock)
	}
}

func TestNormalizeStreamChunkAndReader_OpenAIReasoningAlias(t *testing.T) {
	chunk := map[string]interface{}{
		"choices": []interface{}{
			map[string]interface{}{
				"delta": map[string]interface{}{
					"reasoning": "think",
					"tool_calls": []interface{}{
						map[string]interface{}{
							"function": map[string]interface{}{
								"name":      "list_files",
								"arguments": map[string]interface{}{"path": "."},
							},
						},
					},
				},
			},
		},
	}
	normalized := NormalizeStreamChunk(Context{Protocol: "openai"}, chunk)
	choice := normalized["choices"].([]interface{})[0].(map[string]interface{})
	delta := choice["delta"].(map[string]interface{})
	if delta["reasoning_content"] != "think" {
		t.Fatalf("expected reasoning_content to be populated, got %#v", delta)
	}
	toolCall := delta["tool_calls"].([]interface{})[0].(map[string]interface{})
	fn := toolCall["function"].(map[string]interface{})
	if fn["arguments"] != `{"path":"."}` {
		t.Fatalf("expected stream tool arguments to be encoded, got %#v", fn["arguments"])
	}

	reader := NormalizeStreamReader(Context{Protocol: "openai"}, strings.NewReader("data: {\"choices\":[{\"delta\":{\"reasoning\":\"think\"}}]}\n\n"))
	payload, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("read normalized stream: %v", err)
	}
	if !strings.Contains(string(payload), `"reasoning_content":"think"`) {
		t.Fatalf("expected normalized stream payload, got %s", payload)
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
