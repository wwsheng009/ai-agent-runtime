package remotetools

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/internal/toolcore/searchcore"
)

// TestRemoteGrepPatternFileMatchesWalker pins the pattern-file contract across
// engines: each line of the file becomes a pattern, and rg must search the same
// effective list (the core reads the file, not rg's -f, so both paths agree).
func TestRemoteGrepPatternFileMatchesWalker(t *testing.T) {
	root := t.TempDir()
	patterns := filepath.Join(root, "patterns.txt")
	if err := os.WriteFile(patterns, []byte("alpha\nbeta\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("alpha here\nbeta here\ngamma here\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runner := resolveRipgrepRunner()

	opts := searchcore.GrepOptions{PatternFiles: []string{patterns}, Path: root, Limit: 50}
	walker, err := searchcore.Grep(context.Background(), opts)
	if err != nil {
		t.Fatalf("walker: %v", err)
	}
	if len(walker.Matches) != 4 {
		t.Fatalf("walker matches = %+v, want 4 (2 in a.txt + 2 in patterns.txt)", walker.Matches)
	}
	if !runner.available() {
		return
	}
	rgResult, used, rgErr := runner.grep(context.Background(), opts)
	if rgErr != nil || !used {
		t.Fatalf("ripgrep used=%v err=%v", used, rgErr)
	}
	if len(rgResult.Matches) != len(walker.Matches) {
		t.Fatalf("engine mismatch: rg=%+v walker=%+v", rgResult.Matches, walker.Matches)
	}
	for i := range walker.Matches {
		got, want := rgResult.Matches[i], walker.Matches[i]
		if filepath.Base(got.File) != filepath.Base(want.File) || got.Line != want.Line || got.Text != want.Text {
			t.Fatalf("mismatch at %d: rg=%+v walker=%+v", i, got, want)
		}
	}
}
