package types

import "testing"

func TestRequestClonePreservesThinkingAndReasoningFields(t *testing.T) {
	req := NewRequest("analyze")
	budget := 4096
	req.ReasoningEffort = "high"
	req.Thinking = &ThinkingConfig{
		Type:         "enabled",
		BudgetTokens: &budget,
	}

	cloned := req.Clone()
	if cloned == nil {
		t.Fatal("expected clone")
	}
	if cloned.ReasoningEffort != "high" {
		t.Fatalf("expected reasoning effort high, got %q", cloned.ReasoningEffort)
	}
	if cloned.Thinking == nil || cloned.Thinking.Type != "enabled" {
		t.Fatalf("expected thinking to be preserved, got %#v", cloned.Thinking)
	}
	if cloned.Thinking == req.Thinking {
		t.Fatal("expected thinking clone to be deep copied")
	}
	if cloned.Thinking.BudgetTokens == req.Thinking.BudgetTokens {
		t.Fatal("expected thinking budget pointer to be deep copied")
	}
}

func TestResolveThinkingConfigAndReasoningEffortFromContainers(t *testing.T) {
	options := map[string]interface{}{
		"thinking": map[string]interface{}{
			"type":          "adaptive",
			"effort":        "high",
			"budget_tokens": 8192,
		},
		"reasoning": map[string]interface{}{
			"effort": "medium",
		},
	}

	thinking := ResolveThinkingConfig(nil, options)
	if thinking == nil {
		t.Fatal("expected thinking config from options")
	}
	if thinking.Type != "adaptive" {
		t.Fatalf("expected adaptive thinking, got %q", thinking.Type)
	}
	if thinking.Effort != "high" {
		t.Fatalf("expected high thinking effort, got %q", thinking.Effort)
	}
	if thinking.BudgetTokens == nil || *thinking.BudgetTokens != 8192 {
		t.Fatalf("expected budget_tokens 8192, got %#v", thinking.BudgetTokens)
	}

	if effort := ResolveReasoningEffort("", options); effort != "medium" {
		t.Fatalf("expected reasoning effort medium, got %q", effort)
	}
}
