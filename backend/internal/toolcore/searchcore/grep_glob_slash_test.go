package searchcore

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// TestGrepGlobSlashPatternMatchesRelativePath pins the path-aware glob rule
// for slash patterns: `nested/*.txt` matches nested/also.txt (relative-path
// matching), never a bare basename elsewhere.
func TestGrepGlobSlashPatternMatchesRelativePath(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "nested"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "nested", "also.txt"), []byte("needle nested\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "visible.txt"), []byte("needle visible\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	result, err := Grep(context.Background(), GrepOptions{
		Pattern: "needle", Path: root, Glob: "nested/*.txt", Limit: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Matches) != 1 || filepath.Base(result.Matches[0].File) != "also.txt" {
		t.Fatalf("nested/*.txt must match nested/also.txt via the relative path, got %+v", result.Matches)
	}
}
