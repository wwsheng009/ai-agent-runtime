package remotetools

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/internal/toolcore/searchcore"
)

// TestRemoteRipgrepGrepOffsetMatchesWalker pins the shared pagination contract:
// the ripgrep engine and the pure-Go walker must return the same page for the
// same offset. Before ApplyGrepWindow the rg path ignored offset entirely and
// repeated page one forever.
func TestRemoteRipgrepGrepOffsetMatchesWalker(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "hits.txt")
	if err := os.WriteFile(target, []byte("hit one\nmiss\nhit two\nhit three\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runner := resolveRipgrepRunner()
	if !runner.available() {
		t.Skip("ripgrep is not available")
	}

	for _, offset := range []int{0, 1, 2, 3} {
		opts := searchcore.GrepOptions{Pattern: "hit", Path: target, Limit: 1, Offset: offset}
		rgResult, used, err := runner.grep(context.Background(), opts)
		if err != nil || !used {
			t.Fatalf("offset %d: used=%v err=%v", offset, used, err)
		}
		walker, err := searchcore.Grep(context.Background(), opts)
		if err != nil {
			t.Fatalf("walker offset %d: %v", offset, err)
		}
		if len(rgResult.Matches) != len(walker.Matches) {
			t.Fatalf("offset %d: rg=%+v walker=%+v", offset, rgResult.Matches, walker.Matches)
		}
		for i := range walker.Matches {
			got, want := rgResult.Matches[i], walker.Matches[i]
			if filepath.Base(got.File) != filepath.Base(want.File) || got.Line != want.Line || got.Text != want.Text {
				t.Fatalf("offset %d mismatch at %d: rg=%+v walker=%+v", offset, i, got, want)
			}
		}
		if rgResult.Truncated != walker.Truncated {
			t.Fatalf("offset %d: rg truncated=%v walker truncated=%v", offset, rgResult.Truncated, walker.Truncated)
		}
	}
}
