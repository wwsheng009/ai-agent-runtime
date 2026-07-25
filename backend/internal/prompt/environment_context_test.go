package prompt

import (
	"strings"
	"testing"
)

func TestRenderShellExecutionGuidance_PrefersDedicatedSearchTools(t *testing.T) {
	got := RenderShellExecutionGuidance()

	if !strings.Contains(got, "Shell guidance:") {
		t.Fatalf("expected guidance heading, got:\n%s", got)
	}
	if !strings.Contains(got, "Prefer toolkit `grep`") {
		t.Fatalf("expected dedicated grep guidance, got:\n%s", got)
	}
	if !strings.Contains(got, "Never invoke toolkit tool names as shell commands") {
		t.Fatalf("expected shell-vs-toolkit misuse guidance, got:\n%s", got)
	}
	if !strings.Contains(got, "commands") {
		t.Fatalf("expected bash commands batch guidance, got:\n%s", got)
	}
	if !strings.Contains(got, "empty search results") {
		t.Fatalf("expected empty-search recovery guidance, got:\n%s", got)
	}
	if !strings.Contains(got, "literal=true") {
		t.Fatalf("expected shell-search recovery to mention literal=true, got:\n%s", got)
	}
	if !strings.Contains(got, "-g") && !strings.Contains(got, "path argument") {
		t.Fatalf("expected path-glob guidance for shell rg, got:\n%s", got)
	}
	gotLower := strings.ToLower(got)
	if strings.Contains(gotLower, "powershell") || strings.Contains(gotLower, "pwsh") {
		if !strings.Contains(got, "heredoc") {
			t.Fatalf("expected Windows heredoc guidance, got:\n%s", got)
		}
		if !strings.Contains(got, "&&") {
			t.Fatalf("expected PowerShell bash-operator guidance, got:\n%s", got)
		}
		if !strings.Contains(gotLower, "os error 123") && !strings.Contains(got, "-g") {
			t.Fatalf("expected Windows path-glob / os error guidance, got:\n%s", got)
		}
	}
}

func TestRenderParallelToolGuidance_EncouragesBatchedReadOnlyInspections(t *testing.T) {
	got := RenderParallelToolGuidance()

	if !strings.Contains(got, "Parallel tool guidance:") {
		t.Fatalf("expected guidance heading, got:\n%s", got)
	}
	if !strings.Contains(got, "independent read-only inspections") {
		t.Fatalf("expected read-only batching guidance, got:\n%s", got)
	}
	if !strings.Contains(got, "supports_parallel=true") {
		t.Fatalf("expected explicit supports_parallel guidance, got:\n%s", got)
	}
	if !strings.Contains(got, "same assistant turn") {
		t.Fatalf("expected parallel batching guidance, got:\n%s", got)
	}
	if !strings.Contains(got, "view.files") || !strings.Contains(got, "grep.patterns") || !strings.Contains(got, "grep.paths") {
		t.Fatalf("expected structured batch parameter guidance, got:\n%s", got)
	}
	if !strings.Contains(got, "all predictable independent evidence") {
		t.Fatalf("expected single-turn evidence gathering guidance, got:\n%s", got)
	}
	if !strings.Contains(got, "shell.commands") || !strings.Contains(got, "true data dependencies") {
		t.Fatalf("expected structured shell batch guidance, got:\n%s", got)
	}
	if !strings.Contains(got, "dependent tool calls serial") {
		t.Fatalf("expected serial dependency guidance, got:\n%s", got)
	}
}

func TestRenderFileEditingGuidance_PrefersApplyPatchForCodeEdits(t *testing.T) {
	got := RenderFileEditingGuidance()

	if !strings.Contains(got, "File editing guidance:") {
		t.Fatalf("expected guidance heading, got:\n%s", got)
	}
	if !strings.Contains(got, "Use `apply_patch` for code edits") {
		t.Fatalf("expected apply_patch-first guidance, got:\n%s", got)
	}
	if !strings.Contains(got, "use `edit` only for a small exact string") {
		t.Fatalf("expected constrained edit guidance, got:\n%s", got)
	}
	if !strings.Contains(got, "view/grep") {
		t.Fatalf("expected verification guidance, got:\n%s", got)
	}
	if !strings.Contains(got, "stale @@ context") {
		t.Fatalf("expected apply_patch context guidance, got:\n%s", got)
	}
	if !strings.Contains(got, "every content line must start with `+`") {
		t.Fatalf("expected Add File line-prefix guidance, got:\n%s", got)
	}
	if !strings.Contains(got, "at most one task `in_progress`") {
		t.Fatalf("expected todos single in_progress guidance, got:\n%s", got)
	}
}

func TestRenderTaskDifficultyGuidance_IncludesSubagentRoutingMetadata(t *testing.T) {
	got := RenderTaskDifficultyGuidance()

	for _, expected := range []string{
		"Task difficulty rating and subagent delegation policy:",
		"easy, normal, hard, expert",
		"include difficulty and difficulty_rationale for every child task",
		"leave provider/model empty unless the user explicitly asked",
		"runtime maps difficulty to local provider/model configuration",
	} {
		if !strings.Contains(got, expected) {
			t.Fatalf("expected %q in guidance, got:\n%s", expected, got)
		}
	}
}
