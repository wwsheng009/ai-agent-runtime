package agent

import (
	"strings"
	"testing"
)

func TestPromptBuilder_BuildSubagentPrompt_IncludesParallelToolGuidance(t *testing.T) {
	builder := NewPromptBuilder()
	prompt := builder.BuildSubagentPrompt(nil, SubagentTask{
		Goal:     "Inspect the workspace",
		ReadOnly: true,
	})

	if !strings.Contains(prompt, "Parallel tool guidance:") {
		t.Fatalf("expected parallel guidance, got:\n%s", prompt)
	}
	if !strings.Contains(prompt, "same assistant turn") {
		t.Fatalf("expected batching guidance, got:\n%s", prompt)
	}
	if !strings.Contains(prompt, "read-only subagent") {
		t.Fatalf("expected read-only subagent guidance, got:\n%s", prompt)
	}
}

func TestPromptBuilder_BuildSubagentPrompt_IncludesRoutingMetadata(t *testing.T) {
	builder := NewPromptBuilder()
	prompt := builder.BuildSubagentPrompt(nil, SubagentTask{
		Goal:                "Verify routing behavior",
		Role:                "verifier",
		Difficulty:          "hard",
		DifficultyRationale: "Cross-provider route.",
		Provider:            "remote",
		Model:               "strong-model",
		ReasoningEffort:     "high",
		RoutingSource:       "role_override",
		ReadOnly:            true,
	})

	if !strings.Contains(prompt, "Subtask difficulty: hard.") {
		t.Fatalf("expected difficulty metadata, got:\n%s", prompt)
	}
	if !strings.Contains(prompt, "Difficulty rationale: Cross-provider route.") {
		t.Fatalf("expected difficulty rationale, got:\n%s", prompt)
	}
	if !strings.Contains(prompt, "Runtime routing: provider=remote, model=strong-model, reasoning_effort=high, source=role_override.") {
		t.Fatalf("expected routing metadata, got:\n%s", prompt)
	}
}
