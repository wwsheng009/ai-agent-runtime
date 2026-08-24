package markdown

import (
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/render"
)

// TestReproReasoningNewlinesPreserved reproduces the reported regression:
// unified renderer output fuses words that were separated by single newlines
// in the reasoning body (e.g. "architecture" + "Designing" -> "architectureDesigning").
func TestReproReasoningNewlinesPreserved(t *testing.T) {
	body := "Inspecting buildSessionActor architecture\n" +
		"Designing per-actor coordinator with idempotent replay\n" +
		"Planning per-actor coordinator instantiation and shutdown"
	opts := Options{Width: 100}
	doc := Render(body, opts)
	plain := render.PlainBackend{}.Render(doc)
	t.Logf("plain output: %q", plain)
	if strings.Contains(plain, "architectureDesigning") {
		t.Fatalf("FUSION: newline between words was dropped: %q", plain)
	}
	// The renderer may convert soft line breaks to a space; both are valid.
	if !strings.Contains(plain, "architecture") || !strings.Contains(plain, "Designing") {
		t.Fatalf("content missing: %q", plain)
	}
}

// TestReproWrapEmbeddedNewline checks render.Wrap behavior when a span's text
// contains an embedded newline (as produced by collectInline for soft breaks).
func TestReproWrapEmbeddedNewline(t *testing.T) {
	line := render.Line{Spans: []render.Span{{Text: "architecture\n Designing per-actor coordinator with idempotent replay"}}}
	wrapped := render.Wrap(line, 40, render.WrapOptions{BreakWord: true})
	var b strings.Builder
	for i, l := range wrapped {
		if i > 0 {
			b.WriteString("\n|")
		}
		for _, sp := range l.Spans {
			b.WriteString(sp.Text)
		}
	}
	got := b.String()
	t.Logf("wrapped: %q", got)
	if strings.Contains(got, "architectureDesigning") {
		t.Fatalf("FUSION after Wrap: %q", got)
	}
}

// TestWrapNoDanglingSpaces guards the wrap-point space allocation: the
// inter-word separator must not survive as a leading space of a continuation
// line nor as a trailing space of a full line.
func TestWrapNoDanglingSpaces(t *testing.T) {
	cases := []struct {
		text  string
		width int
	}{
		{"the quick brown fox jumps over the lazy dog and keeps running fast", 30},
		{"This sentence has bold words inside and wraps at width", 28},
		{"the command go test ./... takes a while to run", 28},
		{"Inspecting buildSessionActor architecture\nDesigning per-actor coordinator with idempotent replay", 40},
		{"supercalifragilisticexpialidocious another word", 20},
	}
	for _, tc := range cases {
		opts := Options{Width: tc.width}
		doc := Render(tc.text, opts)
		for _, line := range (render.PlainBackend{}).RenderLines(doc) {
			if strings.HasSuffix(line, " ") {
				t.Fatalf("width=%d trailing space on %q (input %q)", tc.width, line, tc.text)
			}
			if strings.HasPrefix(line, " ") {
				t.Fatalf("width=%d leading space on %q (input %q)", tc.width, line, tc.text)
			}
		}
	}
}

// TestCJKSoftbreakNoSpace ensures soft line breaks between CJK characters do
// not produce a space inside a sentence (the most visible "extra space" for
// Chinese/Japanese output), while Latin soft breaks keep the space separator.
func TestCJKSoftbreakNoSpace(t *testing.T) {
	got := render.PlainBackend{}.Render(Render("第一行内容\n第二行内容\n第三行", Options{Width: 60}))
	if want := "第一行内容第二行内容第三行"; got != want {
		t.Fatalf("CJK soft break inserted a space: %q, want %q", got, want)
	}
	mixed := render.PlainBackend{}.Render(Render("hello world\n中文段落内容 continuation", Options{Width: 60}))
	if want := "hello world 中文段落内容 continuation"; mixed != want {
		t.Fatalf("mixed soft break spacing changed: %q, want %q", mixed, want)
	}
	latin := render.PlainBackend{}.Render(Render("aaa bbb\nccc ddd", Options{Width: 60}))
	if want := "aaa bbb ccc ddd"; latin != want {
		t.Fatalf("latin soft break spacing changed: %q, want %q", latin, want)
	}
}

func TestReportedChineseReasoningSentenceDoesNotGainSpaces(t *testing.T) {
	cases := []struct {
		name, source, want string
	}{
		{
			name:   "reported reasoning",
			source: "根据  \n交接摘要，主  \n任务  \n已  \n全部完成：备份、删旧  \n库、恢复新库、切换启动  \n验证、收  \n尾清理都  \n已完成。剩余  \n工作只有  \n一项：向  \n用户提交最终报告。",
			want:   "根据交接摘要，主任务已全部完成：备份、删旧库、恢复新库、切换启动验证、收尾清理都已完成。剩余工作只有一项：向用户提交最终报告。",
		},
		{name: "styled CJK", source: "**根据**  \n**交接摘要**", want: "根据交接摘要"},
		{name: "mixed", source: "中文  \nhello", want: "中文 hello"},
		{name: "Latin", source: "hello  \nworld", want: "hello world"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := (render.PlainBackend{}).Render(Render(tc.source, Options{Width: 300})); got != tc.want {
				t.Fatalf("hard-break spacing changed\n got: %q\nwant: %q", got, tc.want)
			}
		})
	}
}

// TestBlockquoteContinuationKeepsPrefix ensures every rendered quote line
// carries the "│ " marker, including wrap continuations.
func TestBlockquoteContinuationKeepsPrefix(t *testing.T) {
	lines := render.PlainBackend{}.RenderLines(Render("> a very long quoted line that wraps around here", Options{Width: 24}))
	if len(lines) < 2 {
		t.Fatalf("expected wrapped quote, got %d lines: %q", len(lines), lines)
	}
	for i, line := range lines {
		if !strings.HasPrefix(line, "│ ") {
			t.Fatalf("line %d lost the quote prefix: %q (all: %q)", i, line, lines)
		}
	}
}
