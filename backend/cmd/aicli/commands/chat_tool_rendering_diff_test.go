package commands

import (
	"fmt"
	"strings"
	"testing"

	uidiff "github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/diff"
)

// TestRenderEditedDiffOutput_KeepsHeaderLikeAddedLines covers an added line
// whose own text begins with "++ ". The local parser used to read the raw
// "+++ ..." row as a file header and silently retargeted the whole diff.
func TestRenderEditedDiffOutput_KeepsHeaderLikeAddedLines(t *testing.T) {
	output := strings.Join([]string{
		"--- a/notes.md",
		"+++ b/notes.md",
		"@@ -1,2 +1,3 @@",
		" intro",
		"+++ nested bullet",
	}, "\n")

	got := renderEditedDiffOutput(output)
	want := strings.Join([]string{
		`• Edited notes.md (+1 -0)`,
		`        1   intro`,
		`        2 + ++ nested bullet`,
	}, "\n")
	if got != want {
		t.Fatalf("unexpected render for header-like added line:\nwant:\n%s\n\ngot:\n%s", want, got)
	}
}

// TestRenderEditedDiffOutput_SkipsNoNewlineMarker keeps the git "\ No newline"
// marker out of the numbered preview: it carries no line number.
func TestRenderEditedDiffOutput_SkipsNoNewlineMarker(t *testing.T) {
	output := strings.Join([]string{
		"--- a/app.go",
		"+++ b/app.go",
		"@@ -1,1 +1,1 @@",
		"-old",
		`\ No newline at end of file`,
		"+new",
	}, "\n")

	got := renderEditedDiffOutput(output)
	want := strings.Join([]string{
		`• Edited app.go (+1 -1)`,
		`        1 - old`,
		`        1 + new`,
	}, "\n")
	if got != want {
		t.Fatalf("unexpected render around no-newline marker:\nwant:\n%s\n\ngot:\n%s", want, got)
	}
}

func TestRenderDiffOutput_KeepsMultipleGitFiles(t *testing.T) {
	output := strings.Join([]string{
		"diff --git a/first.go b/first.go",
		"index 1111111..2222222 100644",
		"--- a/first.go",
		"+++ b/first.go",
		"@@ -1 +1 @@",
		"-package old",
		"+package first",
		"diff --git a/second.ts b/second.ts",
		"index 3333333..4444444 100644",
		"--- a/second.ts",
		"+++ b/second.ts",
		"@@ -1 +1 @@",
		"-const oldValue = 0",
		"+const value = 1",
	}, "\n")

	got := renderDiffOutput(output, "Diff")
	for _, want := range []string{
		"• Diff first.go (+1 -1)",
		"package first",
		"• Diff second.ts (+1 -1)",
		"const value = 1",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("multi-file git diff lost %q:\n%s", want, got)
		}
	}
}

// TestRenderEditedDiffOutput_MarksBudgetTruncation covers a diff larger than
// the shared parse budget: the preview must end with the elision marker instead
// of stopping without explanation.
func TestRenderEditedDiffOutput_MarksBudgetTruncation(t *testing.T) {
	budget := uidiff.DefaultParseOptions().MaxLines
	var b strings.Builder
	b.WriteString("--- a/generated.go\n+++ b/generated.go\n")
	b.WriteString(fmt.Sprintf("@@ -1,%d +1,%d @@\n", budget+1, budget+1))
	for i := 0; i < budget+1; i++ {
		b.WriteString(" row\n")
	}

	lines := strings.Split(renderEditedDiffOutput(b.String()), "\n")
	if len(lines) < 3 {
		t.Fatalf("expected a long preview, got %d lines", len(lines))
	}
	if !strings.HasPrefix(lines[0], "• Edited generated.go") {
		t.Fatalf("unexpected header: %q", lines[0])
	}
	if got, want := lines[len(lines)-1], "    "+renderedDiffElisionRow; got != want {
		t.Fatalf("last line=%q, want elision marker %q", got, want)
	}
	// Header + parsed rows (the @@ line consumes one unit of the budget) + marker.
	if got, want := len(lines), 1+(budget-1)+1; got != want {
		t.Fatalf("preview lines=%d, want %d", got, want)
	}
}
