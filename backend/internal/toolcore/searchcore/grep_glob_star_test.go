package searchcore

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// TestGrepGlobDoubleStarMatchesNestedFilename pins the path-aware glob rule
// for `**`-prefixed filename patterns: `**/skip.txt` must match a skip.txt
// inside node_modules (the fixture layout the remote conformance uses).
func TestGrepGlobDoubleStarMatchesNestedFilename(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "node_modules"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "node_modules", "skip.txt"), []byte("needle dependency\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "visible.txt"), []byte("needle visible\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	result, err := Grep(context.Background(), GrepOptions{
		Pattern: "needle", Path: root, Glob: "**/skip.txt", Limit: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Matches) != 1 || filepath.Base(result.Matches[0].File) != "skip.txt" {
		t.Fatalf("**/skip.txt must match the nested file, got %+v", result.Matches)
	}
}
