package remotetools

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/internal/toolcore/searchcore"
)

func grepMaxCountTree(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	for name, content := range map[string]string{
		"a.txt": "hit a1\nhit a2\nhit a3\n",
		"b.txt": "hit b1\nhit b2\n",
	} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func matchLabelsOf(result searchcore.GrepResult) []string {
	out := make([]string, 0, len(result.Matches))
	for _, match := range result.Matches {
		out = append(out, fmt.Sprintf("%s:%d:%s", filepath.Base(match.File), match.Line, match.Text))
	}
	return out
}

// TestRemoteGrepMaxCountMatchesWalker pins the per-file cap across engines,
// including the rg -m 0 contract (an explicit zero suppresses the file) that
// the walker implements itself.
func TestRemoteGrepMaxCountMatchesWalker(t *testing.T) {
	root := grepMaxCountTree(t)
	runner := resolveRipgrepRunner()

	cases := []struct {
		name string
		opts searchcore.GrepOptions
		want []string
	}{
		{
			name: "cap one content",
			opts: searchcore.GrepOptions{Pattern: "hit", MaxCount: 1, MaxCountSet: true},
			want: []string{"a.txt:1:hit a1", "b.txt:1:hit b1"},
		},
		{
			name: "cap two content",
			opts: searchcore.GrepOptions{Pattern: "hit", MaxCount: 2, MaxCountSet: true},
			want: []string{"a.txt:1:hit a1", "a.txt:2:hit a2", "b.txt:1:hit b1", "b.txt:2:hit b2"},
		},
		{
			name: "explicit zero content",
			opts: searchcore.GrepOptions{Pattern: "hit", MaxCount: 0, MaxCountSet: true},
			want: nil,
		},
		{
			name: "cap one count mode",
			opts: searchcore.GrepOptions{Pattern: "hit", Mode: searchcore.GrepModeCount, MaxCount: 1, MaxCountSet: true},
			want: []string{"a.txt:1:hit a1", "b.txt:1:hit b1"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			opts := tc.opts
			opts.Path = root
			opts.Limit = 50
			walker, err := searchcore.Grep(context.Background(), opts)
			if err != nil {
				t.Fatalf("walker: %v", err)
			}
			assertStringList(t, matchLabelsOf(walker), tc.want)

			if !runner.available() {
				return
			}
			rgResult, used, rgErr := runner.grep(context.Background(), opts)
			if rgErr != nil || !used {
				t.Fatalf("ripgrep used=%v err=%v", used, rgErr)
			}
			assertStringList(t, matchLabelsOf(rgResult), tc.want)
			if rgResult.Truncated != walker.Truncated {
				t.Fatalf("truncated: rg=%v walker=%v", rgResult.Truncated, walker.Truncated)
			}
		})
	}
}
