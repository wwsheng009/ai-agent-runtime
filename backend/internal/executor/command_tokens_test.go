package executor

import (
	"reflect"
	"testing"
)

func TestSplitCommandTokens_PreservesQuotedPathSegments(t *testing.T) {
	tokens := SplitCommandTokens(`cat "frontend/src/pages/setting/runtime file.yaml" | head -n 1`)
	want := []string{
		"cat",
		"frontend/src/pages/setting/runtime file.yaml",
		"|",
		"head",
		"-n",
		"1",
	}
	if !reflect.DeepEqual(tokens, want) {
		t.Fatalf("expected tokens %v, got %v", want, tokens)
	}
}

func TestSplitCommandTokens_HandlesPipeWithoutWhitespace(t *testing.T) {
	tokens := SplitCommandTokens(`git diff -- internal/gateway/handlers/admin_config.go |head -200`)
	want := []string{
		"git",
		"diff",
		"--",
		"internal/gateway/handlers/admin_config.go",
		"|",
		"head",
		"-200",
	}
	if !reflect.DeepEqual(tokens, want) {
		t.Fatalf("expected tokens %v, got %v", want, tokens)
	}
}

func TestHasPipedHeadToken(t *testing.T) {
	if !HasPipedHeadToken(SplitCommandTokens(`git diff | head -200`)) {
		t.Fatal("expected piped head token to be detected")
	}
	if HasPipedHeadToken(SplitCommandTokens(`head -200`)) {
		t.Fatal("did not expect standalone head to count as piped head")
	}
}

func TestIsGitDiffCommand(t *testing.T) {
	for _, command := range []string{
		`git diff`,
		`git.exe --no-pager diff -- app.go`,
		`git -C "E:\projects\repo" diff --cached`,
		`Get-Location; git diff | Select-Object -First 20`,
	} {
		if !IsGitDiffCommand(command) {
			t.Fatalf("expected git diff command: %q", command)
		}
	}
	for _, command := range []string{
		`git status`,
		`git -C diff status`,
		`Write-Output "git diff"`,
		`git show HEAD`,
	} {
		if IsGitDiffCommand(command) {
			t.Fatalf("unexpected git diff command: %q", command)
		}
	}
}

func TestLooksLikeUnifiedDiffOutput(t *testing.T) {
	if !LooksLikeUnifiedDiffOutput("diff --git a/app.go b/app.go\nindex 1..2 100644\n--- a/app.go\n+++ b/app.go\n@@ -1 +1 @@\n-old\n+new") {
		t.Fatal("expected unified diff output")
	}
	for _, output := range []string{
		"app.go | 2 +-\n1 file changed",
		"--- not enough\n+++ headers only",
		"",
	} {
		if LooksLikeUnifiedDiffOutput(output) {
			t.Fatalf("unexpected unified diff output: %q", output)
		}
	}
}
