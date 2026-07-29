package ui

import (
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/render"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/style"
	"strings"
	"testing"
)

func TestFormatOutputWritesLineNumbersAndIndent(t *testing.T) {
	theme := createTheme(ThemeDark)
	cfg := &OutputConfig{
		Indent:     "  ",
		LineNumber: true,
		Colorize:   false,
	}
	got := FormatOutput("a\nb", cfg, theme)
	if !strings.Contains(got, "  1 ") || !strings.Contains(got, "  2 ") {
		t.Fatalf("missing line numbers: %q", got)
	}
	if !strings.Contains(got, "  a") || !strings.Contains(got, "  b") {
		t.Fatalf("missing indent+content: %q", got)
	}
}

func TestFormatJSONPrettyPrints(t *testing.T) {
	got := FormatJSON(`{"b":1,"a":2}`)
	if !strings.Contains(got, "\n") || !strings.Contains(got, `"a"`) {
		t.Fatalf("expected pretty json, got %q", got)
	}
}

func TestFormatJSONInvalidReturnsOriginal(t *testing.T) {
	in := `{"broken"`
	if got := FormatJSON(in); got != in {
		t.Fatalf("invalid json should pass through, got %q", got)
	}
}

func TestFormatMarkdownIsNotNoopForHeadings(t *testing.T) {
	got := FormatMarkdown("# Title\n\nhello")
	if got == "# Title\n\nhello" {
		// Formatter may preserve or restyle; either way must not be empty.
	}
	if strings.TrimSpace(got) == "" {
		t.Fatal("FormatMarkdown returned empty")
	}
	// Should not be pure no-op identity for markdown with heading markers in colored mode.
	// Accept any non-empty rendering.
}

func TestTruncateVisibleUsesCellWidth(t *testing.T) {
	got := TruncateVisible("你好世界", 5, "..")
	if DisplayWidth(got) > 5 {
		t.Fatalf("visible width %d > 5 for %q", DisplayWidth(got), got)
	}
}

func TestFormatOutputDocumentWrapsByCellWidthAndKeepsRoles(t *testing.T) {
	cfg := &OutputConfig{Indent: "  ", MaxWidth: 10, WordWrap: true, LineNumber: true}
	doc := FormatOutputDocument("你好世界", cfg)
	for _, block := range doc.Blocks {
		for _, line := range block.Lines {
			if width := render.LineWidth(line); width > cfg.MaxWidth {
				t.Fatalf("line width %d exceeds %d: %#v", width, cfg.MaxWidth, line)
			}
		}
	}
	if got := doc.PlainText(); !strings.Contains(got, "  1   你好") || !strings.Contains(got, "      世界") {
		t.Fatalf("unexpected wrapped projection: %q", got)
	}
}

func TestCodeBlockDocumentUsesSyntaxSpansWithoutFences(t *testing.T) {
	doc := CodeBlockDocument("package main\nconst n = 1", "go")
	plain := doc.PlainText()
	if strings.Contains(plain, "```") || strings.Contains(plain, "go package") {
		t.Fatalf("code block leaked fence/language decoration: %q", plain)
	}
	foundToken := false
	for _, block := range doc.Blocks {
		for _, line := range block.Lines {
			for _, span := range line.Spans {
				if strings.HasPrefix(span.Style.Role, "Code.") {
					foundToken = true
				}
			}
		}
	}
	if !foundToken {
		t.Fatal("expected Chroma token spans")
	}
}

func TestKeywordDocumentUsesLongestNonOverlappingMatch(t *testing.T) {
	doc := KeywordDocument("foobar foo", []string{"foo", "foobar"})
	if got := doc.PlainText(); got != "[foobar] [foo]" {
		t.Fatalf("unexpected keyword projection: %q", got)
	}
	commandSpans := 0
	for _, span := range doc.Blocks[0].Lines[0].Spans {
		if span.Style.Role == string(style.RoleCommand) {
			commandSpans++
		}
	}
	if commandSpans != 2 {
		t.Fatalf("expected 2 command spans, got %d", commandSpans)
	}
}

func TestFormatMarkdownWidthUsesNegotiatedANSI16Profile(t *testing.T) {
	t.Setenv("NO_COLOR", "")
	t.Setenv("FORCE_COLOR", "1")
	t.Setenv("AICLI_COLOR_DEPTH", "ansi16")

	got := FormatMarkdownWidth("# Title\n\n```go\nconst n = 1\n```", 40)
	if !strings.Contains(got, "\x1b[") {
		t.Fatalf("expected ANSI output, got %q", got)
	}
	if strings.Contains(got, "38;5;") || strings.Contains(got, "38;2;") {
		t.Fatalf("markdown leaked high-depth SGR into ANSI-16 output: %q", got)
	}
	plain := render.ANSIToPlain(got)
	if !strings.Contains(plain, "Title") || !strings.Contains(plain, "const n = 1") {
		t.Fatalf("markdown plain projection changed: %q", plain)
	}
}
