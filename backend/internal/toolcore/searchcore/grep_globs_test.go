package searchcore

import (
	"os"
	"path/filepath"
	"testing"
)

// TestGrepGlobsArrayAndNegation pins the array spelling of `glob`: several
// patterns union, `!`-prefixed entries exclude (the local tool's partition
// rule), and the single-value spelling still works alongside.
func TestGrepGlobsArrayAndNegation(t *testing.T) {
	root := t.TempDir()
	for path, content := range map[string]string{
		"pkg/main.go":     "needle\n",
		"pkg/nested/d.go": "needle\n",
		"vendor/skip.go":  "needle\n",
		"other/keep.go":   "needle\n",
	} {
		full := filepath.Join(root, filepath.FromSlash(path))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	// Array with negation: **/*.go minus vendor/** keeps the other trees.
	got := grepFilterMatches(t, root, GrepOptions{Pattern: "needle", Globs: []string{"**/*.go", "!vendor/**"}})
	assertFileList(t, got, []string{"other/keep.go", "pkg/main.go", "pkg/nested/d.go"})

	// Path-aware pattern: pkg/*.go matches only pkg's direct files.
	got = grepFilterMatches(t, root, GrepOptions{Pattern: "needle", Globs: []string{"pkg/*.go"}})
	assertFileList(t, got, []string{"pkg/main.go"})

	// The single-value spelling still works on its own.
	got = grepFilterMatches(t, root, GrepOptions{Pattern: "needle", Glob: "*.go"})
	assertFileList(t, got, []string{"other/keep.go", "pkg/main.go", "pkg/nested/d.go", "vendor/skip.go"})
}
