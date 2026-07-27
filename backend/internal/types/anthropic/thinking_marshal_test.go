package anthropic

import (
	"encoding/json"
	"testing"
)

func TestThinkingMarshalJSON_OmitsAdaptiveEffort(t *testing.T) {
	raw, err := json.Marshal(Thinking{Type: "adaptive", Effort: "high"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var decoded map[string]interface{}
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if decoded["type"] != "adaptive" {
		t.Fatalf("type=%v want adaptive", decoded["type"])
	}
	if _, ok := decoded["effort"]; ok {
		t.Fatalf("adaptive JSON must omit effort, got %s", string(raw))
	}
	if _, ok := decoded["budget_tokens"]; ok {
		t.Fatalf("adaptive JSON must omit budget_tokens, got %s", string(raw))
	}
}

func TestThinkingMarshalJSON_KeepsEnabledEffortAndBudget(t *testing.T) {
	budget := 16384
	raw, err := json.Marshal(Thinking{Type: "enabled", Effort: "high", BudgetTokens: &budget})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var decoded map[string]interface{}
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if decoded["type"] != "enabled" {
		t.Fatalf("type=%v want enabled", decoded["type"])
	}
	if decoded["effort"] != "high" {
		t.Fatalf("effort=%v want high", decoded["effort"])
	}
	if decoded["budget_tokens"] != float64(16384) {
		t.Fatalf("budget_tokens=%v want 16384", decoded["budget_tokens"])
	}
}
