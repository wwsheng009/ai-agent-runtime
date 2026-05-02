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
