package searchcore

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// TestGrepWalkerPageTwoKeepsOffsetWindow pins the page-2 flow through the
// shared Grep function with offset=2, limit=2 on a 3-match fixture: the walk
// stops one entry past the page, the window slices to the remaining match, and
// Truncated stays false (nothing beyond it).
func TestGrepWalkerPageTwoKeepsOffsetWindow(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "m.txt"), []byte("needle one\nkeep\nneedle two\nkeep\nneedle three\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	opts := GrepOptions{Pattern: "needle", Path: root, Limit: 2, Offset: 2}
	result, err := Grep(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Matches) != 1 {
		t.Fatalf("page 2 should hold 1 match, got %+v", result.Matches)
	}
	if result.Matches[0].Text != "needle three" {
		t.Fatalf("page 2 content = %+v", result.Matches)
	}
	if result.Truncated {
		t.Fatalf("page 2 is the last page, truncated must be false: %+v", result)
	}
	page := renderGrepPage(grepRenderInputs{
		paths: fakeRenderPaths{root: root}, result: &result,
		options: opts, mode: GrepModeContent, roots: []string{root},
	})
	if page.matchCount != 1 || !contains(page.content, "needle three") {
		t.Fatalf("renderer page = %q (matchCount=%d)", page.content, page.matchCount)
	}
}

func contains(haystack, needle string) bool {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}