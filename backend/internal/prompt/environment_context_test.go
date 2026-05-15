package prompt

import (
	"strings"
	"testing"
)

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
}
