package skills

import (
	"strings"
	"testing"

	runtimeprompt "github.com/wwsheng009/ai-agent-runtime/internal/prompt"
)

func TestBuildRuntimeInstructionMessages_IncludesTaskDifficultyGuidanceWithoutProfile(t *testing.T) {
	messages := buildRuntimeInstructionMessages(nil, "", "codex")
	if len(messages) == 0 {
		t.Fatal("expected runtime instruction message")
	}
	content := primarySystemInstructionContent(messages)
	if !strings.Contains(content, "Task difficulty rating and subagent delegation policy:") {
		t.Fatalf("expected task difficulty guidance, got:\n%s", content)
	}
	if !strings.Contains(content, "difficulty_rationale") {
		t.Fatalf("expected difficulty rationale guidance, got:\n%s", content)
	}
}

func TestBuildRuntimeInstructionMessages_PreservesPromptTextAndAddsTaskDifficultyGuidance(t *testing.T) {
	messages := buildRuntimeInstructionMessages(&profileRuntimeState{
		PromptText: "Profile system prompt.",
	}, "", "codex")
	content := primarySystemInstructionContent(messages)
	if !strings.Contains(content, "Profile system prompt.") {
		t.Fatalf("expected profile prompt text, got:\n%s", content)
	}
	if !strings.Contains(content, "Task difficulty rating and subagent delegation policy:") {
		t.Fatalf("expected task difficulty guidance, got:\n%s", content)
	}
}

func TestBuildRuntimeInstructionMessages_AddsTaskDifficultyGuidanceToStructuredLayers(t *testing.T) {
	layers := runtimeprompt.NewLayers()
	layers.AddLayer(runtimeprompt.LayerBase, "Profile", "Profile system prompt.", "profile")
	layers.AddLayer(runtimeprompt.LayerDeveloper, "Tools", "Prefer rg when available.", "profile")

	messages := buildRuntimeInstructionMessages(&profileRuntimeState{
		PromptLayers: layers,
	}, "", "codex")
	content := primarySystemInstructionContent(messages)
	if !strings.Contains(content, "Profile system prompt.") {
		t.Fatalf("expected structured profile prompt, got:\n%s", content)
	}
	if !strings.Contains(content, "Task difficulty rating and subagent delegation policy:") {
		t.Fatalf("expected task difficulty guidance, got:\n%s", content)
	}
	if !messageListContainsText(messages, "Prefer rg when available.") {
		t.Fatalf("expected developer instruction to be preserved, got: %#v", messages)
	}
}
