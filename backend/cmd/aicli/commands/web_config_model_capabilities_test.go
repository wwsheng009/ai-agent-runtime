package commands

import (
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
)

// ---------------------------------------------------------------------------
// 「模型编辑器」写回：model_capabilities 逐字段合并
// ---------------------------------------------------------------------------

func webStrPtr(s string) *string                     { return &s }
func webIntPtr(i int) *int                           { return &i }
func webFloatPtr(f float64) *float64                 { return &f }
func webBoolPtr(b bool) *bool                        { return &b }
func webStringsPtr(v []string) *[]string             { return &v }
func webBudgetsPtr(v map[string]int) *map[string]int { return &v }

// TestMergeModelCapabilityUpdatesContextFields 覆盖模型编辑器新增的核心场景：
// 上下文窗口 / 最大输出等字段的写入与非 nil 零值清空。
func TestMergeModelCapabilityUpdatesContextFields(t *testing.T) {
	base := map[string]agentconfig.ModelCapabilitySpec{
		"m-a": {MaxContextTokens: 8000, MaxTokens: 1000, AutoCompactRatio: 0.8},
		"m-b": {MaxContextTokens: 128000},
	}
	merged := mergeModelCapabilityUpdates(base, map[string]chatWebModelCapabilityUpdate{
		"m-a": {
			MaxContextTokens:      webIntPtr(200000),
			MaxTokens:             webIntPtr(32000),
			AutoCompactRatio:      webFloatPtr(0.9),
			AutoCompactTokenLimit: webIntPtr(180000),
			AutoCompactMode:       webStrPtr("aggressive"),
			SupportsRemoteCompact: webBoolPtr(true),
		},
	})
	a := merged["m-a"]
	if a.MaxContextTokens != 200000 || a.MaxTokens != 32000 {
		t.Fatalf("context/output tokens not applied: %+v", a)
	}
	if a.AutoCompactRatio != 0.9 || a.AutoCompactTokenLimit != 180000 || a.AutoCompactMode != "aggressive" || !a.SupportsRemoteCompact {
		t.Fatalf("auto compact fields not applied: %+v", a)
	}
	// 未提交的模型必须原样保留（不得因全量写回丢失其它模型的能力声明）。
	if merged["m-b"].MaxContextTokens != 128000 {
		t.Fatalf("unrelated model mutated: %+v", merged["m-b"])
	}
	// nil 字段 = 不修改。
	if merged["m-a"].ReasoningModel || merged["m-a"].ReasoningEfforts != nil {
		t.Fatalf("nil fields must stay untouched: %+v", a)
	}
}

// TestMergeModelCapabilityUpdatesExplicitClear 非 nil 零值必须显式清空，
// 而不是被当作「未提交」忽略——否则用户无法在编辑器里删掉某个字段。
func TestMergeModelCapabilityUpdatesExplicitClear(t *testing.T) {
	replay := true
	base := map[string]agentconfig.ModelCapabilitySpec{
		"m-a": {
			ReasoningModel:         true,
			ReasoningEfforts:       []string{"low", "high"},
			ReasoningEffortBudgets: map[string]int{"high": 4096},
			DefaultReasoningEffort: "high",
			CompactReasoningEffort: "low",
			MaxContextTokens:       128000,
			MaxTokens:              8192,
			AutoCompactMode:        "auto",
			InputModalities:        []string{"text", "image"},
			NativeTools:            agentconfig.NativeToolCapabilities{ImageGeneration: true},
			ReplayReasoningContent: &replay,
		},
	}
	merged := mergeModelCapabilityUpdates(base, map[string]chatWebModelCapabilityUpdate{
		"m-a": {
			ReasoningModel:         webBoolPtr(false),
			ReasoningEfforts:       webStringsPtr([]string{}),
			ReasoningEffortBudgets: webBudgetsPtr(map[string]int{}),
			DefaultReasoningEffort: webStrPtr(""),
			CompactReasoningEffort: webStrPtr(""),
			MaxContextTokens:       webIntPtr(0),
			MaxTokens:              webIntPtr(0),
			AutoCompactMode:        webStrPtr(""),
			SupportsRemoteCompact:  webBoolPtr(false),
			ReplayReasoningContent: webBoolPtr(false),
			InputModalities:        webStringsPtr([]string{}),
			NativeTools:            &chatWebModelNativeToolsUpdate{ImageGeneration: webBoolPtr(false)},
		},
	})
	a := merged["m-a"]
	if a.ReasoningModel || a.MaxContextTokens != 0 || a.MaxTokens != 0 ||
		a.DefaultReasoningEffort != "" || a.CompactReasoningEffort != "" ||
		a.AutoCompactMode != "" || a.SupportsRemoteCompact ||
		len(a.ReasoningEfforts) != 0 || len(a.ReasoningEffortBudgets) != 0 ||
		len(a.InputModalities) != 0 || a.NativeTools.ImageGeneration {
		t.Fatalf("explicit clear not honoured: %+v", a)
	}
	// 三态契约：显式 false 必须落成非 nil 的 false（不是「未声明」）。
	if a.ReplayReasoningContent == nil || *a.ReplayReasoningContent {
		t.Fatalf("replay_reasoning_content tri-state lost: %+v", a.ReplayReasoningContent)
	}
}

// TestMergeModelCapabilityUpdatesReplayTriState 未提交 replay_reasoning_content
// 时必须保持三态指针原值（含 nil）。
func TestMergeModelCapabilityUpdatesReplayTriState(t *testing.T) {
	base := map[string]agentconfig.ModelCapabilitySpec{"m-a": {}}
	merged := mergeModelCapabilityUpdates(base, map[string]chatWebModelCapabilityUpdate{
		"m-a": {MaxContextTokens: webIntPtr(10)},
	})
	if merged["m-a"].ReplayReasoningContent != nil {
		t.Fatalf("undeclared replay contract must stay nil, got %+v", merged["m-a"].ReplayReasoningContent)
	}
	merged = mergeModelCapabilityUpdates(merged, map[string]chatWebModelCapabilityUpdate{
		"m-a": {ReplayReasoningContent: webBoolPtr(true)},
	})
	if merged["m-a"].ReplayReasoningContent == nil || !*merged["m-a"].ReplayReasoningContent {
		t.Fatalf("replay contract not set: %+v", merged["m-a"].ReplayReasoningContent)
	}
}

// TestMergeModelCapabilityUpdatesTrimsAndRejectsNegative 保证脏输入不会污染
// 配置文件：空模型名被丢弃，负数 token 预算收敛为 0。
func TestMergeModelCapabilityUpdatesTrimsAndRejectsNegative(t *testing.T) {
	merged := mergeModelCapabilityUpdates(nil, map[string]chatWebModelCapabilityUpdate{
		"  ": {MaxContextTokens: webIntPtr(5)},
		" m-c ": {
			MaxContextTokens:       webIntPtr(-1000),
			MaxTokens:              webIntPtr(-1),
			AutoCompactRatio:       webFloatPtr(-0.5),
			AutoCompactTokenLimit:  webIntPtr(-7),
			ReasoningEfforts:       webStringsPtr([]string{" low ", "", "high", "low"}),
			ReasoningEffortBudgets: webBudgetsPtr(map[string]int{"high": 4096, "low": 0, " ": 100}),
			InputModalities:        webStringsPtr([]string{" text ", "image", "text"}),
		},
	})
	if len(merged) != 1 {
		t.Fatalf("blank model name must be dropped, got %+v", merged)
	}
	c, ok := merged["m-c"]
	if !ok {
		t.Fatalf("model name should be trimmed: %+v", merged)
	}
	if c.MaxContextTokens != 0 || c.MaxTokens != 0 || c.AutoCompactRatio != 0 || c.AutoCompactTokenLimit != 0 {
		t.Fatalf("negative values must clamp to 0: %+v", c)
	}
	if strings.Join(c.ReasoningEfforts, ",") != "low,high" {
		t.Fatalf("reasoning efforts not trimmed/deduped: %+v", c.ReasoningEfforts)
	}
	if len(c.ReasoningEffortBudgets) != 1 || c.ReasoningEffortBudgets["high"] != 4096 {
		t.Fatalf("budgets not trimmed: %+v", c.ReasoningEffortBudgets)
	}
	if strings.Join(c.InputModalities, ",") != "text,image" {
		t.Fatalf("input modalities not trimmed/deduped: %+v", c.InputModalities)
	}
}

// TestMergeModelCapabilitiesDoesNotMutateBase 合并必须基于拷贝：直接改
// session 配置里的 capability 切片会让未保存的编辑污染内存配置。
func TestMergeModelCapabilitiesDoesNotMutateBase(t *testing.T) {
	base := map[string]agentconfig.ModelCapabilitySpec{
		"m-a": {ReasoningEfforts: []string{"low"}, ReasoningEffortBudgets: map[string]int{"low": 10}},
	}
	merged := mergeModelCapabilities(base, map[string]chatWebModelReasoningUpdate{
		"m-a": {ReasoningEfforts: webStringsPtr([]string{"high"})},
	})
	if strings.Join(merged["m-a"].ReasoningEfforts, ",") != "high" {
		t.Fatalf("update not applied: %+v", merged["m-a"])
	}
	if strings.Join(base["m-a"].ReasoningEfforts, ",") != "low" {
		t.Fatalf("base slice mutated: %+v", base["m-a"].ReasoningEfforts)
	}
	if base["m-a"].ReasoningEffortBudgets["low"] != 10 {
		t.Fatalf("base budgets mutated: %+v", base["m-a"].ReasoningEffortBudgets)
	}
	// 空请求时也要返回拷贝（不是共享引用）。
	same := mergeModelCapabilities(base, nil)
	same["m-a"].ReasoningEfforts[0] = "mutated"
	if base["m-a"].ReasoningEfforts[0] != "low" {
		t.Fatalf("empty request returned shared map: %+v", base["m-a"].ReasoningEfforts)
	}
}
