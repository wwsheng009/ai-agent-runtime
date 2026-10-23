package remotetools

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/internal/toolcore/searchcore"
)

// TestRemoteGlobOffsetPages ensures the remote rg path honors the same offset
// contract as the walker: page two must not repeat page one.
func TestRemoteGlobOffsetPages(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"a.txt", "b.txt", "c.txt"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	runner := resolveRipgrepRunner()
	if !runner.available() {
		t.Skip("ripgrep is not available")
	}
	first, used, err := runner.glob(context.Background(), searchcore.GlobOptions{
		Base: root, Pattern: "*.txt", Limit: 1,
	})
	if err != nil || !used {
		t.Fatalf("first page: used=%v err=%v", used, err)
	}
	second, used, err := runner.glob(context.Background(), searchcore.GlobOptions{
		Base: root, Pattern: "*.txt", Limit: 1, Offset: 1,
	})
	if err != nil || !used {
		t.Fatalf("second page: used=%v err=%v", used, err)
	}
	if len(first.Matches) != 1 || len(second.Matches) != 1 {
		t.Fatalf("unexpected pages: first=%v second=%v", first.Matches, second.Matches)
	}
	if first.Matches[0] == second.Matches[0] {
		t.Fatalf("offset repeated the same file: %v", first.Matches[0])
	}
}
