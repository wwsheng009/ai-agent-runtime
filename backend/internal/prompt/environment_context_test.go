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
