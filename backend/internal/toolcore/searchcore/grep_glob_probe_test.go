package searchcore

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// TestGrepWalkerDoubleStarGlobThroughHandlerFixture reproduces the exact remote
// fixture layout through the core walker to pin why `**/skip.txt` may miss:
// the ignore engine's default consults `.gitignore`; node_modules is NOT on
// any skip list anymore. This test documents the actual selection.
func TestGrepWalkerDoubleStarGlobThroughHandlerFixture(t *testing.T) {
	root := t.TempDir()
	for path, content := range map[string]string{
		"visible.txt":           "needle visible\n",
		".hidden.txt":           "needle hidden\n",
		".gitignore":            "ignored.txt\n",
		"ignored.txt":           "needle ignored\n",
		"node_modules/skip.txt": "needle dependency\n",
		"nested/also.txt":       "needle nested\n",
	} {
		full := filepath.Join(root, filepath.FromSlash(path))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	result, err := Grep(context.Background(), GrepOptions{
		Pattern: "needle", Path: root, Glob: "**/skip.txt", Limit: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	assertFileList(t, grepFilterMatches(t, root, GrepOptions{Pattern: "needle", Glob: "**/skip.txt"}),
		[]string{"node_modules/skip.txt"})
	if len(result.Matches) != 1 {
		t.Fatalf("walker must reach node_modules/skip.txt, got %+v", result.Matches)
	}
}
