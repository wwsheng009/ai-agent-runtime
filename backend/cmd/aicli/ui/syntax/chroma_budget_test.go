package syntax

import (
	"strings"
	"testing"
	"time"
)

// TestHighlightBudgetFallsBackToPlainLines proves the wall-clock lex budget
// actually interrupts a pathological highlight pass and degrades to plain
// lines instead of stalling the caller.
func TestHighlightBudgetFallsBackToPlainLines(t *testing.T) {
	h := &ChromaHighlighter{
		Limits:       DefaultLimits(),
		DefaultTheme: "catppuccin-latte",
		Budget:       1 * time.Millisecond,
	}
	// A large Go body forces the regex lexer through thousands of rule
	// matches; 1ms is far below what even a fast lex pass needs, so the
	// budget must interrupt it and degrade to plain lines.
	var b strings.Builder
	b.WriteString("```go\n")
	for i := 0; i < 3000; i++ {
		b.WriteString("func f() { if x { f() } }\n")
	}
	code := b.String()

	start := time.Now()
	lines, meta := h.Highlight(HighlightRequest{Code: code, Language: "go", Theme: "catppuccin-latte"})
	elapsed := time.Since(start)

	if meta.Highlighted {
		t.Fatalf("expected highlight to degrade under budget, got Highlighted=true")
	}
	if meta.FallbackReason != "highlight_budget_exceeded" {
		t.Fatalf("unexpected fallback reason %q", meta.FallbackReason)
	}
	if len(lines) == 0 {
		t.Fatal("plain fallback must still render all lines")
	}
	// The budget check runs between tokens; a single lexer step may still
	// spend up to chroma's per-rule match timeout, so allow generous slack
	// while still proving the call returned promptly.
	if elapsed > 2*time.Second {
		t.Fatalf("highlight took %v; budget did not bound the pass", elapsed)
	}
	if got := len(strings.Split(code, "\n")); got != len(lines) {
		t.Fatalf("plain fallback line count mismatch: got %d want %d", len(lines), got)
	}
}

// TestHighlightBudgetZeroMeansDefault ensures the zero-value highlighter
// still applies the default budget (and a trivial block highlights fine).
func TestHighlightBudgetZeroMeansDefault(t *testing.T) {
	h := &ChromaHighlighter{Limits: DefaultLimits(), DefaultTheme: "catppuccin-latte"}
	lines, meta := h.Highlight(HighlightRequest{Code: "func main() {}", Language: "go", Theme: "catppuccin-latte"})
	if !meta.Highlighted {
		t.Fatalf("expected trivial block to highlight, fallback=%q", meta.FallbackReason)
	}
	if len(lines) == 0 {
		t.Fatal("expected rendered lines")
	}
}
