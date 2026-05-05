package providercompat

import (
	"reflect"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/internal/types"
)

func TestDefaultRuntimeCapability_SensenovaAndNVIDIA(t *testing.T) {
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
