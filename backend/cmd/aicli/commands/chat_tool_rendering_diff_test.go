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
func TestRenderEditedDiffOutput_KeepsHeaderLikeAddedLines(t *testing.T) {	output := strings.Join([]string{
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

// TestExtractFencedDiff_IgnoresMarkdownFencesInsideDiffBody is the regression
// for "git diff renders only the start": a Markdown code fence inside the diff
// body (context row " ```go", or add/delete rows "-```"/"+```") used to be
// mistaken for the closing ```diff fence, truncating the diff at the first
// inner fence and dropping every later hunk. Only a BARE ``` line closes.
func TestExtractFencedDiff_IgnoresMarkdownFencesInsideDiffBody(t *testing.T) {
	output := strings.Join([]string{
		"补丁已应用：修改 1；影响 1 个路径",
		"",
		"文件差异:",
		"```diff",
		"--- a/README.md",
		"+++ b/README.md",
		"@@ -1,7 +1,7 @@",
		" # Title",
		" ",
		" ```go",
		"-func old() {}",
		"+func new() {}",
		" ```",
		" body",
		"```",
		"",
	}, "\n")

	got := extractFencedDiff(output)
	for _, want := range []string{
		"--- a/README.md",
		"@@ -1,7 +1,7 @@",
		"-func old() {}",
		"+func new() {}",
		" body",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("extracted diff missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "\n---\n") {
		t.Fatalf("unexpected trailing content in extracted diff:\n%s", got)
	}
}

// TestRenderEditedDiffOutput_MarkdownFenceKeepsEveryHunk renders the full
// apply_patch-style fenced diff even when the edited file contains ``` rows.
func TestRenderEditedDiffOutput_MarkdownFenceKeepsEveryHunk(t *testing.T) {
	output := strings.Join([]string{
		"补丁已应用：修改 1；影响 1 个路径",
		"",
		"文件差异:",
		"```diff",
		"--- a/README.md",
		"+++ b/README.md",
		"@@ -1,7 +1,7 @@",
		" # Title",
		" ",
		" ```go",
		"-func old() {}",
		"+func new() {}",
		" ```",
		" body",
		"```",
		"",
	}, "\n")

	got := renderEditedDiffOutput(output)
	for _, want := range []string{
		"• Edited README.md",
		"- func old() {}",
		"+ func new() {}",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("render lost %q:\n%s", want, got)
		}
	}
}

// TestRenderStructuredDiffToolOutput_LabelsReadOnlyDiffSeparately pins the
// "查看 vs 编辑" distinction: a markdown-format diff whose tool is a read-only
// inspection (shell / view) must render as "• Diff", while a real editing tool
// (edit / apply_patch / namespaced .edit) keeps "• Edited".
func TestRenderStructuredDiffToolOutput_LabelsReadOnlyDiffSeparately(t *testing.T) {
	diff := strings.Join([]string{
		"--- a/app.go",
		"+++ b/app.go",
		"@@ -1,1 +1,1 @@",
		"-old",
		"+new",
	}, "\n")

	// Read-only shell inspection must never be labeled an edit.
	for _, tool := range []string{"execute_shell_command", "bash", "shell"} {
		got := renderStructuredDiffToolOutput(map[string]interface{}{
			"tool_name":                 tool,
			"render_output_format":      "markdown",
			"render_output_untruncated": true,
			"render_output":             diff,
		})
		if strings.Contains(got, "• Edited app.go") {
			t.Fatalf("read-only tool %q was mislabeled as edit:\n%s", tool, got)
		}
		if !strings.Contains(got, "• Diff app.go (+1 -1)") {
			t.Fatalf("read-only tool %q expected Diff label, got:\n%s", tool, got)
		}
	}

	// Real editing tools keep the edit label.
	for _, tool := range []string{"edit", "apply_patch", "apply", "filesystem.edit_file", "mcp/apply_patch"} {
		got := renderStructuredDiffToolOutput(map[string]interface{}{
			"tool_name":                 tool,
			"render_output_format":      "markdown",
			"render_output_untruncated": true,
			"render_output":             diff,
		})
		if !strings.Contains(got, "• Edited app.go (+1 -1)") {
			t.Fatalf("editing tool %q lost Edit label:\n%s", tool, got)
		}
		if strings.Contains(got, "• Diff app.go") {
			t.Fatalf("editing tool %q was mislabeled as Diff:\n%s", tool, got)
		}
	}
}

// TestEditingSharedToolRenderOutput_RejectsShellLikeTools pins the producer-side
// guard: shell tools (whose output is read-only, e.g. `git diff`) are never fed
// into the editing render path, so the structured shell-diff path owns them and
// labels them "• Diff" instead of "• Edited".
func TestEditingSharedToolRenderOutput_RejectsShellLikeTools(t *testing.T) {
	for _, tool := range []string{"execute_shell_command", "bash", "shell", "run"} {
		if got := editingSharedToolRenderOutput(tool, "--- a/a.go\n+++ b/a.go\n@@ -1 +1 @@\n-old\n+new"); got != "" {
			t.Fatalf("shell-like tool %q must not produce editing render output, got %q", tool, got)
		}
	}
	// Non-shell editing tools still produce render output.
	for _, tool := range []string{"edit", "apply_patch", "patch", "filesystem.edit_file"} {
		if got := editingSharedToolRenderOutput(tool, "ok"); got == "" {
			t.Fatalf("editing tool %q should produce render output", tool)
		}
	}
}
