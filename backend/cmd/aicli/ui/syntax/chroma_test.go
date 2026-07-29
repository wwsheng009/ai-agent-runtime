package syntax

import (
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/render"
)

func TestHighlightGoTokens(t *testing.T) {
	h := NewChromaHighlighter()
	lines, meta := h.Highlight(HighlightRequest{
		Code:     "func main() {\n\treturn\n}",
		Language: "go",
		Theme:    "monokai",
	})
	if !meta.Highlighted {
		t.Fatalf("expected highlight, meta=%+v", meta)
	}
	if len(lines) < 2 {
		t.Fatalf("expected multiple lines, got %d", len(lines))
	}
	var plain strings.Builder
	hasColor := false
	for _, line := range lines {
		for _, sp := range line.Spans {
			plain.WriteString(sp.Text)
			if sp.Style.Foreground.IsSet() {
				hasColor = true
			}
		}
		plain.WriteByte('\n')
	}
	if !strings.Contains(plain.String(), "func") || !strings.Contains(plain.String(), "main") {
		t.Fatalf("missing source text: %q", plain.String())
	}
	if !hasColor {
		t.Fatal("expected token foreground colors")
	}
}

func TestHighlightLimitFallback(t *testing.T) {
	h := NewChromaHighlighter()
	h.Limits = Limits{MaxBytes: 8, MaxLines: 100}
	lines, meta := h.Highlight(HighlightRequest{
		Code:     "package main\nfunc tooLong() {}",
		Language: "go",
	})
	if meta.Highlighted {
		t.Fatal("expected fallback when over limit")
	}
	if meta.FallbackReason != "limit_exceeded" {
		t.Fatalf("reason=%q", meta.FallbackReason)
	}
	plain := render.PlainBackend{}.Render(render.Document{
		Blocks: []render.Block{{Lines: lines}},
	})
	if !strings.Contains(plain, "package main") {
		t.Fatalf("fallback lost text: %q", plain)
	}
}

func TestNormalizeLanguageAliases(t *testing.T) {
	cases := map[string]string{
		"js":         "javascript",
		"TS":         "typescript",
		"py":         "python",
		"shell":      "bash",
		"yml":        "yaml",
		"golang":     "go",
		"md":         "markdown",
		"go title=x": "go",
	}
	for in, want := range cases {
		if got := NormalizeLanguage(in); got != want {
			t.Fatalf("NormalizeLanguage(%q)=%q want %q", in, got, want)
		}
	}
}

func TestDefaultHighlighterFollowsGlobalTheme(t *testing.T) {
	previous := GlobalDefaultTheme()
	t.Cleanup(func() { SetGlobalDefaultTheme(previous) })
	h := NewChromaHighlighter()
	SetGlobalDefaultTheme("dracula")
	_, meta := h.Highlight(HighlightRequest{Code: "const x = 1", Language: "go"})
	if meta.Theme != "dracula" {
		t.Fatalf("theme=%q, want global dracula", meta.Theme)
	}
}
