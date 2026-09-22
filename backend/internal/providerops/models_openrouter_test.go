package providerops

import (
	"strings"
	"testing"
)

// openRouterModelsFixture 是 OpenRouter /v1/models 的裁剪样本，保留 2026-09
// 实测的嵌套形状：architecture / top_provider / supported_parameters / reasoning。
const openRouterModelsFixture = `{
  "data": [
    {
      "id": "z-ai/glm-5.3-flash",
      "canonical_slug": "z-ai/glm-5.3-flash-20260901",
      "name": "Z.ai: GLM 5.3 Flash",
      "created": 1756684800,
      "context_length": 1310720,
      "architecture": {
        "modality": "text+image+video->text",
        "input_modalities": ["text", "image", "video"],
        "output_modalities": ["text"],
        "tokenizer": "Other"
      },
      "pricing": {"prompt": "0.0000002", "completion": "0.0000008"},
      "top_provider": {"context_length": 1048576, "max_completion_tokens": 131072, "is_moderated": false},
      "supported_parameters": ["tools", "tool_choice", "reasoning", "include_reasoning", "reasoning_effort"],
      "reasoning": {
        "mandatory": false,
        "default_enabled": true,
        "supported_efforts": ["max", "high", "low"],
        "default_effort": "high"
      }
    },
    {
      "id": "z-ai/glm-5.2:free",
      "name": "Z.ai: GLM 5.2 (free)",
      "context_length": 32768,
      "architecture": {"modality": "text->text", "input_modalities": ["text"]},
      "top_provider": {"context_length": 32768, "max_completion_tokens": 8192},
      "supported_parameters": ["max_tokens", "temperature"]
    },
    {
      "id": "acme/plain-model",
      "name": "Acme Plain",
      "context_length": 64000,
      "architecture": {"modality": "text->text"}
    }
  ]
}`

func TestDetectModelListCategory(t *testing.T) {
	tests := []struct {
		name         string
		providerName string
		baseURL      string
		raw          string
		want         ModelListCategory
	}{
		{
			name:         "provider name hits openrouter",
			providerName: "openrouter",
			baseURL:      "https://example.com/api/v1",
			raw:          openRouterModelsFixture,
			want:         ModelListCategoryOpenRouter,
		},
		{
			name:         "base url hits openrouter",
			providerName: "aggregator",
			baseURL:      "https://openrouter.ai/api/v1",
			raw:          `{"data":[{"id":"gpt-4o-mini"}]}`,
			want:         ModelListCategoryOpenRouter,
		},
		{
			name:         "payload shape hits openrouter",
			providerName: "aggregator",
			baseURL:      "https://aggregator.example/v1",
			raw:          openRouterModelsFixture,
			want:         ModelListCategoryOpenRouter,
		},
		{
			name:         "flat gateway payload stays generic",
			providerName: "gateway",
			baseURL:      "https://gateway.example/v1",
			raw:          `{"data":[{"id":"gpt-4o-mini","context_length":128000,"input_modalities":["text","image"]}]}`,
			want:         ModelListCategoryGeneric,
		},
		{
			name:         "empty payload falls back to generic",
			providerName: "gateway",
			raw:          "",
			want:         ModelListCategoryGeneric,
		},
		{
			name:         "invalid payload falls back to generic",
			providerName: "gateway",
			raw:          "not-json",
			want:         ModelListCategoryGeneric,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := DetectModelListCategory(test.providerName, test.baseURL, []byte(test.raw))
			if got != test.want {
				t.Fatalf("DetectModelListCategory() = %q, want %q", got, test.want)
			}
		})
	}
}

// TestDetectModelListCategoryShapeProbeLimit 锁定探测只扫前若干条：头部条目为
// 扁平形状时不会被第 6 条之后的嵌套条目误判（避免探测成本与误判）。
func TestDetectModelListCategoryShapeProbeLimit(t *testing.T) {
	var builder strings.Builder
	builder.WriteString(`{"data":[`)
	for index := 0; index < modelListCategoryShapeProbeLimit; index++ {
		if index > 0 {
			builder.WriteString(",")
		}
		builder.WriteString(`{"id":"flat-model-` + string(rune('a'+index)) + `"}`)
	}
	builder.WriteString(`,{"id":"nested-model","architecture":{"modality":"text->text"}}]}`)

	got := DetectModelListCategory("gateway", "https://gateway.example/v1", []byte(builder.String()))
	if got != ModelListCategoryGeneric {
		t.Fatalf("shape probe scanned past limit: got %q, want %q", got, ModelListCategoryGeneric)
	}
}

func TestParseProviderModelsResponseForCategoryOpenRouter(t *testing.T) {
	models, err := ParseProviderModelsResponseForCategory([]byte(openRouterModelsFixture), "openai", ModelListCategoryOpenRouter)
	if err != nil {
		t.Fatalf("ParseProviderModelsResponseForCategory() error = %v", err)
	}
	if len(models) != 3 {
		t.Fatalf("len(models) = %d, want 3", len(models))
	}

	byID := make(map[string]ModelInfo, len(models))
	for _, model := range models {
		byID[model.ID] = model
	}

	reasoning := byID["z-ai/glm-5.3-flash"]
	if reasoning.ID == "" {
		t.Fatalf("model z-ai/glm-5.3-flash missing: %#v", byID)
	}
	if got, want := reasoning.InputModalities, []string{"text", "image", "video"}; !equalStringSlices(got, want) {
		t.Errorf("InputModalities = %v, want %v", got, want)
	}
	// 有效上下文取 min(context_length, top_provider.context_length)。
	if reasoning.MaxContextTokens != 1048576 {
		t.Errorf("MaxContextTokens = %d, want 1048576", reasoning.MaxContextTokens)
	}
	if reasoning.MaxTokens != 131072 {
		t.Errorf("MaxTokens = %d, want 131072", reasoning.MaxTokens)
	}
	if !reasoning.ReasoningModel {
		t.Errorf("ReasoningModel = false, want true")
	}
	if got, want := reasoning.ReasoningEfforts, []string{"max", "high", "low"}; !equalStringSlices(got, want) {
		t.Errorf("ReasoningEfforts = %v, want %v", got, want)
	}
	if reasoning.DefaultReasoningEffort != "high" {
		t.Errorf("DefaultReasoningEffort = %q, want %q", reasoning.DefaultReasoningEffort, "high")
	}
	if !reasoning.SupportsTools {
		t.Errorf("SupportsTools = false, want true")
	}

	// :free 变体必须原样保留（不剥离变体后缀），且只带 text 模态。
	free := byID["z-ai/glm-5.2:free"]
	if free.ID == "" {
		t.Fatalf("model z-ai/glm-5.2:free missing: %#v", byID)
	}
	if free.MaxContextTokens != 32768 {
		t.Errorf("free MaxContextTokens = %d, want 32768", free.MaxContextTokens)
	}
	if free.MaxTokens != 8192 {
		t.Errorf("free MaxTokens = %d, want 8192", free.MaxTokens)
	}
	if free.ReasoningModel {
		t.Errorf("free ReasoningModel = true, want false")
	}
	if free.SupportsTools {
		t.Errorf("free SupportsTools = true, want false")
	}

	// architecture.input_modalities 缺失时由 modality 推导（"text->text"）。
	plain := byID["acme/plain-model"]
	if got, want := plain.InputModalities, []string{"text"}; !equalStringSlices(got, want) {
		t.Errorf("plain InputModalities = %v, want %v", got, want)
	}
	if plain.MaxContextTokens != 64000 {
		t.Errorf("plain MaxContextTokens = %d, want 64000", plain.MaxContextTokens)
	}
}

// TestOpenRouterModalityFallback 覆盖没有 input_modalities、只有 modality 的镜像站。
func TestOpenRouterModalityFallback(t *testing.T) {
	raw := `{"data":[{"id":"mirror/vision","architecture":{"modality":"text+image->text"},"context_length":128000}]}`
	models, err := ParseProviderModelsResponseForCategory([]byte(raw), "openai", ModelListCategoryOpenRouter)
	if err != nil {
		t.Fatalf("ParseProviderModelsResponseForCategory() error = %v", err)
	}
	if len(models) != 1 {
		t.Fatalf("len(models) = %d, want 1", len(models))
	}
	if got, want := models[0].InputModalities, []string{"text", "image"}; !equalStringSlices(got, want) {
		t.Fatalf("InputModalities = %v, want %v", got, want)
	}
}

// TestOpenRouterReasoningModelFromSupportedParameters 覆盖 reasoning 对象缺失、
// 仅由 supported_parameters 声明推理能力的同族网关。
func TestOpenRouterReasoningModelFromSupportedParameters(t *testing.T) {
	raw := `{"data":[{"id":"mirror/thinker","supported_parameters":["reasoning_effort","tools"]}]}`
	models, err := ParseProviderModelsResponseForCategory([]byte(raw), "openai", ModelListCategoryOpenRouter)
	if err != nil {
		t.Fatalf("ParseProviderModelsResponseForCategory() error = %v", err)
	}
	if len(models) != 1 {
		t.Fatalf("len(models) = %d, want 1", len(models))
	}
	if !models[0].ReasoningModel {
		t.Fatalf("ReasoningModel = false, want true")
	}
	if !models[0].SupportsTools {
		t.Fatalf("SupportsTools = false, want true")
	}
}

// TestGenericCategoryParsingUnchanged 锁定通用类别行为不回归：扁平键照旧解析，
// 嵌套字段不参与。
func TestGenericCategoryParsingUnchanged(t *testing.T) {
	raw := `{"data":[{"id":"gpt-4o-mini","context_length":128000,"input_modalities":["text","image"],"reasoning_efforts":["low","high"],"supported_parameters":["tools"]}]}`
	models, err := ParseProviderModelsResponseForCategory([]byte(raw), "openai", ModelListCategoryGeneric)
	if err != nil {
		t.Fatalf("ParseProviderModelsResponseForCategory() error = %v", err)
	}
	if len(models) != 1 {
		t.Fatalf("len(models) = %d, want 1", len(models))
	}
	model := models[0]
	if model.MaxContextTokens != 128000 {
		t.Errorf("MaxContextTokens = %d, want 128000", model.MaxContextTokens)
	}
	if got, want := model.InputModalities, []string{"text", "image"}; !equalStringSlices(got, want) {
		t.Errorf("InputModalities = %v, want %v", got, want)
	}
	if !model.SupportsTools {
		t.Errorf("SupportsTools = false, want true")
	}
}

func equalStringSlices(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}
