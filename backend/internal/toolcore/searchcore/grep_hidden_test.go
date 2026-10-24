package searchcore

import (
	"os"
	"path/filepath"
	"testing"
)

func grepHiddenFixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	for path, content := range map[string]string{
		"visible.txt":     "needle visible\n",
		".hidden.txt":     "needle hidden\n",
		".git/config":     "needle gitconfig\n",
		"nested/ok.txt":   "needle nested\n",
		".cache/deep.txt": "needle cached\n",
	} {
		full := filepath.Join(root, filepath.FromSlash(path))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

// TestGrepHiddenPolicy pins the single tool-wide hidden contract: dotfiles and
// dot-directories are skipped unless IncludeHidden is set, and an explicitly
// requested hidden root is always searched.
func TestGrepHiddenPolicy(t *testing.T) {
	root := grepHiddenFixture(t)

	assertFileList(t, grepFilterMatches(t, root, GrepOptions{Pattern: "needle", Limit: 50}),
		[]string{"nested/ok.txt", "visible.txt"})

	// With hidden enabled every dot-path is searched, including .git: there is
	// no hardcoded skip list left, exactly like the local tool.
	withHidden := grepFilterMatches(t, root, GrepOptions{Pattern: "needle", IncludeHidden: true})
	assertFileList(t, withHidden, []string{".cache/deep.txt", ".git/config", ".hidden.txt", "nested/ok.txt", "visible.txt"})

	// An explicitly requested hidden directory stays searchable: the exemption
	// is about the given root, not about its name.
	explicitRoot := grepFilterMatches(t, filepath.Join(root, ".cache"), GrepOptions{Pattern: "needle"})
	if len(explicitRoot) != 1 {
		t.Fatalf("an explicit hidden root must be searched, got %v", explicitRoot)
	}
}

func TestGrepHiddenDirectorySegmentsAreSkippedWithoutIncludeHidden(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "src", ".internal"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "src", "keep.txt"), []byte("needle keep\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "src", ".internal", "deep.txt"), []byte("needle deep\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	assertFileList(t, grepFilterMatches(t, root, GrepOptions{Pattern: "needle"}),
		[]string{"src/keep.txt"})
	assertFileList(t, grepFilterMatches(t, root, GrepOptions{Pattern: "needle", IncludeHidden: true}),
		[]string{"src/.internal/deep.txt", "src/keep.txt"})
}
