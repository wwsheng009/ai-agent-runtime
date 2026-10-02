package tooloutline

import (
	"reflect"
	"strings"
	"testing"
)

func TestTreeIndentLinesMarksContentUnderHead(t *testing.T) {
	got := TreeIndentLines([]string{"• Completed view file=a.go", "line 1", "line 2"})
	want := []string{"• Completed view file=a.go", "  │  line 1", "  └  line 2"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("TreeIndentLines = %q, want %q", got, want)
	}
}

func TestTreeIndentLinesStripsOneLegacyContentIndent(t *testing.T) {
	got := TreeIndentLines([]string{"head", "  body", "  last"})
	want := []string{"head", "  │  body", "  └  last"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("TreeIndentLines = %q, want %q", got, want)
	}
}

func TestTreeIndentLinesSingleLineUnchanged(t *testing.T) {
	lines := []string{"• Completed ls path=docs"}
	got := TreeIndentLines(lines)
	if !reflect.DeepEqual(got, lines) {
		t.Fatalf("TreeIndentLines = %q, want %q", got, lines)
	}
}

func TestTreeIndentTextMarksEveryLine(t *testing.T) {
	got := TreeIndentText("a\nb\nc")
	want := "  │  a\n  │  b\n  └  c"
	if got != want {
		t.Fatalf("TreeIndentText = %q, want %q", got, want)
	}
}

func TestTreeIndentTextSingleLineUnchanged(t *testing.T) {
	const output = "24 matches"
	if got := TreeIndentText(output); got != output {
		t.Fatalf("TreeIndentText = %q, want %q", got, output)
	}
}

// TestTreeIndentProjectionsShareMarkerGeometry 守住两条路径的跨路径契约：
// legacy 的 head+content 形态与统一编码器的独立文本形态，在内容行没有
// legacy "  " 前缀时，必须产出完全一致的标记列。
func TestTreeIndentProjectionsShareMarkerGeometry(t *testing.T) {
	content := []string{"a.go:1: `foo`", "", "统计: 2 个文件, 0 个目录"}
	legacy := TreeIndentLines(append([]string{"head"}, content...))[1:]
	standalone := strings.Split(TreeIndentText(strings.Join(content, "\n")), "\n")
	if !reflect.DeepEqual(legacy, standalone) {
		t.Fatalf("legacy %q != standalone %q", legacy, standalone)
	}
}

func TestDeclaredMarkdown(t *testing.T) {
	for _, tt := range []struct {
		format string
		want   bool
	}{
		{"markdown", true},
		{" Markdown ", true},
		{"diff", false},
		{"", false},
	} {
		if got := DeclaredMarkdown(tt.format); got != tt.want {
			t.Fatalf("DeclaredMarkdown(%q) = %v, want %v", tt.format, got, tt.want)
		}
	}
}

func TestLooksLikeDiffText(t *testing.T) {
	for _, tt := range []struct {
		name string
		text string
		want bool
	}{
		{"git diff", "diff --git a/a.go b/a.go\nindex 1..2 100644\n--- a/a.go\n+++ b/a.go\n@@ -1 +1 @@", true},
		{"unified hunks", "--- a/a.go\n+++ b/a.go\n@@ -1 +1 @@", true},
		{"edited head", "• Edited a.go\n  changed 2 lines", true},
		{"diff head", "• Diff b.go\n  changed 1 line", true},
		{"plain output", "a.go:1: `foo`\n# doc heading\n- list item", false},
		{"partial markers", "--- a only\nno plus or hunk", false},
		{"empty", "   \n", false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := LooksLikeDiffText(tt.text); got != tt.want {
				t.Fatalf("LooksLikeDiffText(%q) = %v, want %v", tt.text, got, tt.want)
			}
		})
	}
}
