package searchcore

import (
	"os"
	"path/filepath"
	"testing"
)

// TestGrepExplicitIgnoreFileRelativeToRoot pins the ignore_file resolution
// contract: a relative ignore_file spelling resolves against the search root's
// directory, and its rules hide matching files.
func TestGrepExplicitIgnoreFileRelativeToRoot(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "custom.ignore"), []byte("ignored.txt\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "ignored.txt"), []byte("needle\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "kept.txt"), []byte("needle\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	assertFileList(t, grepFilterMatches(t, root, GrepOptions{
		Pattern: "needle", IgnoreFiles: []string{filepath.Join(root, "custom.ignore")},
	}), []string{"kept.txt"})

	// no_ignore_files disables the explicit file only.
	assertFileList(t, grepFilterMatches(t, root, GrepOptions{
		Pattern: "needle", IgnoreFiles: []string{filepath.Join(root, "custom.ignore")}, NoIgnoreFiles: true,
	}), []string{"ignored.txt", "kept.txt"})
}