package remotetools

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/internal/toolcore/searchcore"
)

func grepFragmentTree(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	for name, content := range map[string]string{
		"a.txt": "foo bar foo\nfoo again\n",
		"b.txt": "foo\n",
		"c.txt": "nothing\n",
	} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

// TestRemoteGrepOnlyMatchingMatchesWalker pins the fragment contract across
// engines, including the column numbers taken from ripgrep's submatch offsets
// and the walker's byte offsets.
func TestRemoteGrepOnlyMatchingMatchesWalker(t *testing.T) {
	root := grepFragmentTree(t)
	runner := resolveRipgrepRunner()

	cases := []struct {
		name string
		opts searchcore.GrepOptions
		want []string
	}{
		{
			name: "fragments with columns",
			opts: searchcore.GrepOptions{Pattern: "foo", OnlyMatching: true},
			want: []string{"a.txt:1:1:foo", "a.txt:1:9:foo", "a.txt:2:1:foo", "b.txt:1:1:foo"},
		},
		{
			name: "invert keeps whole lines",
			opts: searchcore.GrepOptions{Pattern: "foo", OnlyMatching: true, InvertMatch: true},
			want: []string{"c.txt:1:nothing"},
		},
		{
			name: "count mode counts matches",
			opts: searchcore.GrepOptions{Pattern: "foo", OnlyMatching: true, Mode: searchcore.GrepModeCount},
			want: []string{"a.txt:3", "b.txt:1"},
		},
		{
			name: "max_count counts matching lines",
			opts: searchcore.GrepOptions{Pattern: "foo", OnlyMatching: true, MaxCount: 1, MaxCountSet: true},
			want: []string{"a.txt:1:1:foo", "a.txt:1:9:foo", "b.txt:1:1:foo"},
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

			if !runner.available() {
				return
			}
			rgResult, used, rgErr := runner.grep(context.Background(), opts)
			if rgErr != nil || !used {
				t.Fatalf("ripgrep used=%v err=%v", used, rgErr)
			}

			if tc.opts.Mode == searchcore.GrepModeCount {
				assertStringList(t, modeEntries(walker), tc.want)
				assertStringList(t, modeEntries(rgResult), tc.want)
				return
			}
			assertStringList(t, fragmentLabelsOf(walker), tc.want)
			assertStringList(t, fragmentLabelsOf(rgResult), tc.want)
		})
	}
}

func fragmentLabelsOf(result searchcore.GrepResult) []string {
	out := make([]string, 0, len(result.Matches))
	for _, match := range result.Matches {
		if match.Column > 0 {
			out = append(out, fmt.Sprintf("%s:%d:%d:%s", filepath.Base(match.File), match.Line, match.Column, match.Text))
			continue
		}
		out = append(out, fmt.Sprintf("%s:%d:%s", filepath.Base(match.File), match.Line, match.Text))
	}
	return out
}
