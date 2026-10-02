package providerops

import (
	"testing"

	config "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
)

// TestModelCapabilityViewProjectsEveryField 保证「模型编辑器」面板能回显并
// 写回 model_capabilities 的每个字段：视图漏字段 == Web 端编辑该字段被静默
// 丢弃。新增 ModelCapabilitySpec 字段时必须同步补充本断言。
func TestModelCapabilityViewProjectsEveryField(t *testing.T) {
	replay := false
	spec := config.ModelCapabilitySpec{
		InputModalities:        []string{"text", "image"},
		NativeTools:            config.NativeToolCapabilities{ImageGeneration: true, ImagesGenerationsAPI: true},
		ReasoningModel:         true,
		ReasoningEfforts:       []string{"low", "high"},
		ReasoningEffortBudgets: map[string]int{"high": 4096, "low": 0, " ": 99},
		DefaultReasoningEffort: "high",
		MaxContextTokens:       200000,
		MaxTokens:              32000,
		AutoCompactRatio:       0.85,
		AutoCompactTokenLimit:  150000,
		AutoCompactMode:        "aggressive",
		SupportsRemoteCompact:  true,
		ReplayReasoningContent: &replay,
		CompactReasoningEffort: "low",
	}
	view := ModelCapabilityView("  m-a  ", spec)
	if view.Name != "m-a" {
		t.Fatalf("name not trimmed: %q", view.Name)
	}
	if !view.ReasoningModel || len(view.ReasoningEfforts) != 2 ||
		view.DefaultReasoningEffort != "high" || view.CompactReasoningEffort != "low" {
		t.Fatalf("reasoning fields lost: %+v", view)
	}
	if view.MaxContextTokens != 200000 || view.MaxTokens != 32000 {
		t.Fatalf("context/output tokens lost: %+v", view)
	}
	if view.AutoCompactRatio != 0.85 || view.AutoCompactTokenLimit != 150000 ||
		view.AutoCompactMode != "aggressive" || !view.SupportsRemoteCompact {
		t.Fatalf("auto compact fields lost: %+v", view)
	}
	if view.ReplayReasoningContent == nil || *view.ReplayReasoningContent {
		t.Fatalf("replay tri-state lost: %+v", view.ReplayReasoningContent)
	}
	if len(view.InputModalities) != 2 || !view.NativeTools.ImageGeneration || !view.NativeTools.ImagesGenerationsAPI {
		t.Fatalf("modalities / native tools lost: %+v", view)
	}
	// 非正数预算等价于未声明，不进视图。
	if len(view.ReasoningEffortBudgets) != 1 || view.ReasoningEffortBudgets["high"] != 4096 {
		t.Fatalf("budgets must drop non-positive/blank keys: %+v", view.ReasoningEffortBudgets)
	}
}

// TestModelCapabilityViewCopiesSlices 视图不得与配置共享底层数组：Web 端在
// 面板里改档位列表时不能就地改掉 session 配置。
func TestModelCapabilityViewCopiesSlices(t *testing.T) {
	spec := config.ModelCapabilitySpec{
		ReasoningEfforts: []string{"low"},
		InputModalities:  []string{"text"},
	}
	view := ModelCapabilityView("m-a", spec)
	view.ReasoningEfforts[0] = "mutated"
	view.InputModalities[0] = "mutated"
	if spec.ReasoningEfforts[0] != "low" || spec.InputModalities[0] != "text" {
		t.Fatalf("spec mutated through view: %+v", spec)
	}
}

// TestModelCapabilityIsEmptyCoversReplayContract replay_reasoning_content 曾
// 漏判：只声明该契约的 spec 会被当作空条目丢弃，Web 端保存后配置消失。
func TestModelCapabilityIsEmptyCoversReplayContract(t *testing.T) {
	replay := true
	if ModelCapabilityIsEmpty(config.ModelCapabilitySpec{ReplayReasoningContent: &replay}) {
		t.Fatal("spec declaring only replay_reasoning_content must not be empty")
	}
	if !ModelCapabilityIsEmpty(config.ModelCapabilitySpec{}) {
		t.Fatal("zero spec must be empty")
	}
}
