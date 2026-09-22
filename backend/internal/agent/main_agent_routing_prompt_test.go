package agent

import (
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
	"github.com/wwsheng009/ai-agent-runtime/internal/types"
)

// TestMainAgentRoutingSystemFragmentStableAndGuarded 覆盖 §5.4 / §7.3：引导片段只在
// enabled=true 时产出，文本 turn 内逐字节稳定（INV-2），且不出现 provider/model（INV-3）。
func TestMainAgentRoutingSystemFragmentStableAndGuarded(t *testing.T) {
	if got := mainAgentRoutingSystemFragment(nil); got != "" {
		t.Fatalf("nil config must produce no fragment, got %q", got)
	}
	if got := mainAgentRoutingSystemFragment(&agentconfig.AICLIMainAgentRoutingConfig{Enabled: false}); got != "" {
		t.Fatalf("disabled config must produce no fragment, got %q", got)
	}

	cfg := mainAgentRouteTestConfig().MainAgentRouting
	first := mainAgentRoutingSystemFragment(cfg)
	if first == "" {
		t.Fatal("enabled config must produce a fragment")
	}
	if second := mainAgentRoutingSystemFragment(cfg); second != first {
		t.Fatalf("fragment must be byte-stable within a turn:\n%q\n%q", first, second)
	}
	// 档位来自排序后的 mainAgentAllowedLevels：顺序与配置书写顺序无关。
	unsorted := &agentconfig.AICLIMainAgentRoutingConfig{
		Enabled: true,
		Levels:  []string{"hard", "easy", "normal"},
	}
	if got := mainAgentRoutingSystemFragment(unsorted); got != first {
		t.Fatalf("fragment must not depend on configured level order:\n%q\n%q", got, first)
	}
	for _, level := range []string{"easy", "hard", "normal"} {
		if !strings.Contains(first, level) {
			t.Fatalf("fragment must list level %q: %q", level, first)
		}
	}
	if !strings.Contains(first, predictTaskDifficultyToolName) {
		t.Fatalf("fragment must name the reporting tool: %q", first)
	}
	// U18 补充（doc9 v4）：有限 task_type 类别列表必须注入，且只含分类语义。
	if !strings.Contains(first, "task_type") {
		t.Fatalf("fragment must list the task_type parameter: %q", first)
	}
	for _, taskType := range []string{"explore", "migrate", "verify", "generate"} {
		if !strings.Contains(first, taskType) {
			t.Fatalf("fragment must list task category %q: %q", taskType, first)
		}
	}
	lowered := strings.ToLower(first)
	for _, forbidden := range []string{"provider", "model", "anthropic", "claude"} {
		if strings.Contains(lowered, forbidden) {
			t.Fatalf("fragment must not expose %q (INV-3): %q", forbidden, first)
		}
	}
}

// TestMainAgentRoutingSystemMessageIsPromptOnly 覆盖 §5.4 的注入语义：片段走
// system-reminder 通道并标记 Durable=false，落盘前被剥掉——既不进持久历史，也不会
// 在后续 turn 累积成重复片段（INV-1）。
func TestMainAgentRoutingSystemMessageIsPromptOnly(t *testing.T) {
	if msg := mainAgentRoutingSystemMessage(nil); msg != nil {
		t.Fatalf("nil config must not produce a message, got %#v", msg)
	}
	if msg := mainAgentRoutingSystemMessage(&agentconfig.AICLIMainAgentRoutingConfig{Enabled: false}); msg != nil {
		t.Fatalf("disabled config must not produce a message, got %#v", msg)
	}

	msg := mainAgentRoutingSystemMessage(mainAgentRouteTestConfig().MainAgentRouting)
	if msg == nil {
		t.Fatal("enabled config must produce a message")
	}
	if msg.Role != "system" {
		t.Fatalf("fragment role = %q, want system", msg.Role)
	}
	if !IsSystemReminder(*msg) {
		t.Fatal("fragment must use the unified system-reminder channel")
	}
	if IsSystemReminderDurable(*msg) {
		t.Fatal("fragment must be prompt-only (Durable=false)")
	}
	if got := msg.Metadata[MetaSystemReminderKind]; got != ReminderKindMainAgentRouting {
		t.Fatalf("reminder kind = %v, want %q", got, ReminderKindMainAgentRouting)
	}
	if kept := DurableMessagesForPersist([]types.Message{*msg}); len(kept) != 0 {
		t.Fatalf("prompt-only fragment must be stripped before persist, kept %d message(s)", len(kept))
	}
}

// TestPredictTaskDifficultyExemptFromDoomLoopRepeat 覆盖 §5.4：难度上报是 meta 工具，
// 重复上报同一档位是合法行为（引擎侧闩锁），不得累计成语义重复指纹——否则一个多步
// turn 会被 MaxRepeatedToolCalls 硬停。
func TestPredictTaskDifficultyExemptFromDoomLoopRepeat(t *testing.T) {
	tracker := NewDoomLoopTracker(2)
	calls := []types.ToolCall{{
		ID:   "c1",
		Name: predictTaskDifficultyToolName,
		Args: map[string]interface{}{"difficulty": "hard", "rationale": "same work"},
	}}
	for i := 0; i < 5; i++ {
		obs := tracker.ObserveSemanticToolBatch(calls)
		if obs.ShouldStop {
			t.Fatalf("meta report must never hard-stop the turn (iteration %d)", i)
		}
		if obs.Fingerprint != "" || obs.RepeatCount != 0 {
			t.Fatalf("meta report must stay out of the repeat fingerprint, got %#v", obs)
		}
	}

	// 反向控制：非豁免工具在同一 tracker 上仍然会被计数并硬停。
	real := []types.ToolCall{{Name: "view", Args: map[string]interface{}{"file_path": "x.go"}}}
	if obs := tracker.ObserveSemanticToolBatch(real); obs.RepeatCount != 1 {
		t.Fatalf("real tool must still be fingerprinted, got %#v", obs)
	}
	if obs := tracker.ObserveSemanticToolBatch(real); !obs.ShouldStop {
		t.Fatalf("real tool repeat must still hard-stop at the configured limit, got %#v", obs)
	}
}
