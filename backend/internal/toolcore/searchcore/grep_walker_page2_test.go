package searchcore

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// TestGrepWalkerPaginationPageTwoThroughGrep reproduces the walker page-2 call
// end to end: the walk stops one entry past the page, the shared window slices
// by offset, and the second page starts at the remaining match.
func TestGrepWalkerPaginationPageTwoThroughGrep(t *testing.T) {
	root := t.TempDir()
	body := "needle one\nkeep\nneedle two\nkeep\nneedle three\n"
	if err := os.WriteFile(filepath.Join(root, "match.txt"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	result, err := Grep(context.Background(), GrepOptions{
		Pattern: "needle", Path: root, Limit: 2, Offset: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Matches) != 1 {
		t.Fatalf("page 2 should hold the 1 remaining match, got %+v", result.Matches)
	}
	if result.Matches[0].Text != "needle three" {
		t.Fatalf("page 2 content = %+v", result.Matches)
	}
}