package searchcore

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func patternFileFixture(t *testing.T) (root, patterns string) {
	t.Helper()
	root = t.TempDir()
	patterns = filepath.Join(root, "patterns.txt")
	if err := os.WriteFile(patterns, []byte("alpha\nbeta\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for name, content := range map[string]string{
		"a.txt":        "alpha here\nbeta here\ngamma here\n",
		"patterns.txt": "alpha\nbeta\n",
	} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root, patterns
}

// TestGrepPatternFiles pins the shared pattern-file contract: each line is one
// pattern, OR-joined with any direct spelling, so both engines search the same
// effective pattern list.
func TestGrepPatternFiles(t *testing.T) {
	root, patterns := patternFileFixture(t)

	got := grepFilterMatches(t, root, GrepOptions{PatternFiles: []string{patterns}})
	// The pattern file itself contains the pattern text, so it matches too.
	assertFileList(t, got, []string{"a.txt", "patterns.txt"})

	withDirect := grepFilterMatches(t, root, GrepOptions{
		Pattern: "gamma", PatternFiles: []string{patterns},
	})
	if len(withDirect) != 2 {
		t.Fatalf("direct+file patterns should still match one file, got %v", withDirect)
	}

	result, err := Grep(context.Background(), GrepOptions{PatternFiles: []string{patterns}, Path: root})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Matches) != 4 {
		t.Fatalf("pattern file should match alpha and beta lines, got %+v", result.Matches)
	}
}

func TestGrepPatternFilesMissingIsReported(t *testing.T) {
	root, _ := patternFileFixture(t)
	_, err := Grep(context.Background(), GrepOptions{
		PatternFiles: []string{filepath.Join(root, "absent.txt")}, Path: root,
	})
	if err == nil {
		t.Fatal("a missing pattern_file must fail instead of silently matching nothing")
	}
}
