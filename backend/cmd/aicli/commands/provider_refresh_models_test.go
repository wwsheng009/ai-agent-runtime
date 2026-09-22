package commands

import (
	"testing"

	config "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
)

func boolPtrForRefreshTest(value bool) *bool { return &value }

func TestRefreshProviderModelCapabilitiesFromEndpointEndpointWins(t *testing.T) {
	provider := config.Provider{
		Protocol: "openai",
		ModelCapabilities: map[string]config.ModelCapabilitySpec{
			// 旧配置：兜底 model card 写下的单模态 + 旧上下文，且带本地专有字段。
			"z-ai/glm-5.3-flash": {
				InputModalities:        []string{"text"},
				MaxContextTokens:       131072,
				NativeTools:            config.NativeToolCapabilities{ImageGeneration: true},
				ReplayReasoningContent: boolPtrForRefreshTest(true),
				AutoCompactRatio:       0.8,
				AutoCompactTokenLimit:  100000,
				AutoCompactMode:        "aggressive",
				ReasoningEffortBudgets: map[string]int{"high": 8192},
			},
		},
	}
	models := []providerModelInfo{
		{
			ID:               "z-ai/glm-5.3-flash",
			InputModalities:  []string{"text", "image", "video"},
			ReasoningModel:   true,
			ReasoningEfforts: []string{"max", "high", "low"},
			MaxContextTokens: 1048576,
			MaxTokens:        131072,
		},
	}

	build := refreshProviderModelCapabilitiesFromEndpoint("openrouter", "openai", provider, models)
	if !build.summary.Updated {
		t.Fatalf("summary.Updated = false, want true")
	}
	if build.summary.ChangedModels != 1 || build.summary.AddedModels != 0 {
		t.Fatalf("summary changed=%d added=%d, want 1/0", build.summary.ChangedModels, build.summary.AddedModels)
	}

	spec := build.nextCapabilities["z-ai/glm-5.3-flash"]
	// 端点优先：过期的单模态与旧上下文被端点值覆盖。
	if got, want := len(spec.InputModalities), 3; got != want {
		t.Errorf("InputModalities = %v, want 3 entries (text/image/video)", spec.InputModalities)
	}
	if !refreshTestContains(spec.InputModalities, "image") || !refreshTestContains(spec.InputModalities, "video") {
		t.Errorf("InputModalities = %v, want endpoint multimodal values", spec.InputModalities)
	}
	if spec.MaxContextTokens != 1048576 {
		t.Errorf("MaxContextTokens = %d, want 1048576", spec.MaxContextTokens)
	}
	if spec.MaxTokens != 131072 {
		t.Errorf("MaxTokens = %d, want 131072", spec.MaxTokens)
	}
	if !spec.ReasoningModel {
		t.Errorf("ReasoningModel = false, want true")
	}
	if got, want := spec.ReasoningEfforts, []string{"max", "high", "low"}; !equalStringSlicesForRefreshTest(got, want) {
		t.Errorf("ReasoningEfforts = %v, want %v", got, want)
	}
	// 端点未声明的本地字段必须保留。
	if !spec.NativeTools.ImageGeneration {
		t.Errorf("NativeTools.ImageGeneration = false, want preserved true")
	}
	if spec.ReplayReasoningContent == nil || !*spec.ReplayReasoningContent {
		t.Errorf("ReplayReasoningContent = %v, want preserved true", spec.ReplayReasoningContent)
	}
	if spec.AutoCompactRatio != 0.8 || spec.AutoCompactTokenLimit != 100000 || spec.AutoCompactMode != "aggressive" {
		t.Errorf("auto compact settings lost: %#v", spec)
	}
	if spec.ReasoningEffortBudgets["high"] != 8192 {
		t.Errorf("ReasoningEffortBudgets = %v, want preserved", spec.ReasoningEffortBudgets)
	}

	// 摘要应指出被改动的字段名。
	changedFields := build.summary.Models[0].ChangedFields
	if !refreshTestContains(changedFields, "input_modalities") {
		t.Errorf("ChangedFields = %v, want input_modalities", changedFields)
	}
	if !refreshTestContains(changedFields, "max_context_tokens") {
		t.Errorf("ChangedFields = %v, want max_context_tokens", changedFields)
	}
}

func TestRefreshProviderModelCapabilitiesFromEndpointAddsAndKeepsOthers(t *testing.T) {
	provider := config.Provider{
		Protocol: "openai",
		ModelCapabilities: map[string]config.ModelCapabilitySpec{
			"local/untouched": {InputModalities: []string{"text"}, MaxContextTokens: 4096},
		},
	}
	models := []providerModelInfo{
		{ID: "new/model", InputModalities: []string{"text", "image"}, MaxContextTokens: 200000},
		{ID: "empty/model"},
	}

	build := refreshProviderModelCapabilitiesFromEndpoint("openrouter", "openai", provider, models)
	if build.summary.AddedModels != 1 {
		t.Fatalf("summary.AddedModels = %d, want 1", build.summary.AddedModels)
	}
	if _, exists := build.nextCapabilities["empty/model"]; exists {
		t.Errorf("empty/model should not be created without any capability data")
	}
	if _, exists := build.nextCapabilities["local/untouched"]; !exists {
		t.Errorf("local/untouched should be preserved")
	}
	added := build.nextCapabilities["new/model"]
	if added.MaxContextTokens != 200000 || len(added.InputModalities) != 2 {
		t.Errorf("new/model = %#v, want endpoint values", added)
	}
	if build.summary.Models[0].Added != true {
		t.Errorf("model result Added = false, want true")
	}
}

func TestRefreshProviderModelCapabilitiesFromEndpointIdempotent(t *testing.T) {
	provider := config.Provider{
		Protocol: "openai",
		ModelCapabilities: map[string]config.ModelCapabilitySpec{
			"z-ai/glm-5.3-flash": {
				InputModalities:  []string{"text", "image", "video"},
				MaxContextTokens: 1048576,
				ReasoningModel:   true,
				ReasoningEfforts: []string{"max", "high", "low"},
			},
		},
	}
	models := []providerModelInfo{
		{
			ID:               "z-ai/glm-5.3-flash",
			InputModalities:  []string{"text", "image", "video"},
			ReasoningModel:   true,
			ReasoningEfforts: []string{"max", "high", "low"},
			MaxContextTokens: 1048576,
		},
	}

	build := refreshProviderModelCapabilitiesFromEndpoint("openrouter", "openai", provider, models)
	if build.summary.Updated {
		t.Fatalf("summary.Updated = true, want false for identical endpoint data")
	}
	if build.summary.Reason != "already_up_to_date" {
		t.Fatalf("summary.Reason = %q, want already_up_to_date", build.summary.Reason)
	}
	if build.summary.ChangedModels != 0 {
		t.Fatalf("summary.ChangedModels = %d, want 0", build.summary.ChangedModels)
	}
}

// TestProviderLoginModelCapabilitySpecCarriesReasoningAndMaxTokens 锁定端点元数据
// → capabilities 的投影：新增的 reasoning_model / default_reasoning_effort /
// max_tokens 三个字段不再丢失。
func TestProviderLoginModelCapabilitySpecCarriesReasoningAndMaxTokens(t *testing.T) {
	spec := providerLoginModelCapabilitySpec(providerModelInfo{
		ID:                     "z-ai/glm-5.3-flash",
		InputModalities:        []string{"text", "image"},
		ReasoningModel:         true,
		DefaultReasoningEffort: "high",
		MaxContextTokens:       1048576,
		MaxTokens:              131072,
	})
	if !spec.ReasoningModel {
		t.Errorf("ReasoningModel = false, want true (declared without effort list)")
	}
	if spec.DefaultReasoningEffort != "high" {
		t.Errorf("DefaultReasoningEffort = %q, want high", spec.DefaultReasoningEffort)
	}
	if spec.MaxTokens != 131072 {
		t.Errorf("MaxTokens = %d, want 131072", spec.MaxTokens)
	}
	if spec.MaxContextTokens != 1048576 {
		t.Errorf("MaxContextTokens = %d, want 1048576", spec.MaxContextTokens)
	}

	merged := mergeProviderLoginModelCapabilitySpec(config.ModelCapabilitySpec{}, spec)
	if merged.MaxTokens != 131072 || merged.DefaultReasoningEffort != "high" || !merged.ReasoningModel {
		t.Errorf("mergeProviderLoginModelCapabilitySpec dropped fields: %#v", merged)
	}
}

func refreshTestContains(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func equalStringSlicesForRefreshTest(left, right []string) bool {
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
