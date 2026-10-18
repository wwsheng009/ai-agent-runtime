package agent

import (
	"strings"
	"testing"

	runtimeprompt "github.com/wwsheng009/ai-agent-runtime/internal/prompt"
)

func TestPromptBuilder_BuildSubagentPrompt_IncludesParallelToolGuidance(t *testing.T) {
	builder := NewPromptBuilder()
	prompt := builder.BuildSubagentPrompt(nil, SubagentTask{
		Goal:     "Inspect the workspace",
		ReadOnly: true,
	}, nil)

	if !strings.Contains(prompt, "Parallel tool guidance:") {
		t.Fatalf("expected parallel guidance, got:\n%s", prompt)
	}
	if !strings.Contains(prompt, "same assistant turn") {
		t.Fatalf("expected batching guidance, got:\n%s", prompt)
	}
	if !strings.Contains(prompt, "read-only subagent") {
		t.Fatalf("expected read-only subagent guidance, got:\n%s", prompt)
	}
	for _, guidance := range []string{
		"exactly one command per command field",
		"shell commands array",
		"cannot be overridden by approval or bypass_permissions",
	} {
		if !strings.Contains(prompt, guidance) {
			t.Fatalf("expected read-only shell guidance %q, got:\n%s", guidance, prompt)
		}
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
	}, nil)

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

// The child must inherit only the surface-independent head. Guidance written
// for the parent's surface (for example the todos rule inside file-editing
// guidance) must never leak into a narrower child.
func TestPromptBuilder_UsesSurfaceIndependentHead(t *testing.T) {
	parent := &Config{
		SystemPrompt: "PARENT HEAD\n\nFile editing guidance:\n- LEAKED_PARENT_GUIDANCE\n- When using `todos`, keep at most one task in_progress.",
		Options: map[string]interface{}{
			ChildPromptHeadOption: "PARENT HEAD",
		},
	}
	// The child only has shell: todos guidance must stay hidden, while the
	// inherited parent block (including its todos rule) must be gone entirely.
	facts := &runtimeprompt.ToolGuidanceFacts{Only: map[string]bool{"shell": true}}
	prompt := NewPromptBuilder().BuildSubagentPrompt(parent, SubagentTask{Goal: "write a file"}, facts)

	if !strings.Contains(prompt, "PARENT HEAD") {
		t.Fatalf("expected the head to be inherited, got:\n%s", prompt)
	}
	if strings.Contains(prompt, "LEAKED_PARENT_GUIDANCE") {
		t.Fatalf("parent surface guidance leaked into the child prompt:\n%s", prompt)
	}
	if strings.Contains(prompt, "When using `todos`") {
		t.Fatalf("parent surface guidance leaked into the child prompt:\n%s", prompt)
	}
}

// Guidance follows the child's own resolved surface: a surface without the
// parallel read tools must not carry the parallel section, while a surface with
// todos carries the task-list section.
func TestPromptBuilder_GatesGuidanceByChildFacts(t *testing.T) {
	facts := &runtimeprompt.ToolGuidanceFacts{
		Only: map[string]bool{"todos": true},
	}
	prompt := NewPromptBuilder().BuildSubagentPrompt(&Config{}, SubagentTask{Goal: "track tasks"}, facts)

	if strings.Contains(prompt, "Parallel tool guidance:") {
		t.Fatalf("parallel guidance must be withheld when no read tool is visible:\n%s", prompt)
	}
	if !strings.Contains(prompt, "Task list guidance:") {
		t.Fatalf("todos guidance must appear when todos is visible:\n%s", prompt)
	}
}

// A host may explicitly provide an empty head (no profile, no environment).
// That must not fall back to the parent's guidance-bearing SystemPrompt.
func TestPromptBuilder_EmptyHeadDoesNotFallBackToParentPrompt(t *testing.T) {
	parent := &Config{
		SystemPrompt: "LEAKED_PARENT_GUIDANCE\n\nFile editing guidance:\n- leaked",
		Options: map[string]interface{}{
			ChildPromptHeadOption: "",
		},
	}
	prompt := NewPromptBuilder().BuildSubagentPrompt(parent, SubagentTask{Goal: "inspect"}, nil)
	if strings.Contains(prompt, "LEAKED_PARENT_GUIDANCE") {
		t.Fatalf("empty head must not fall back to the parent prompt:\n%s", prompt)
	}
}
