package remotetools

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/toolcore/searchcore"
)

// orderedRelativeMatches is relativeMatches without the final sort: sort
// semantics are exactly what this file asserts, so the helper must not erase
// the order it is meant to observe.
func orderedRelativeMatches(t *testing.T, root string, result searchcore.GrepResult) []string {
	t.Helper()
	out := make([]string, 0, len(result.Matches))
	for _, match := range result.Matches {
		rel, err := filepath.Rel(root, match.File)
		if err != nil {
			t.Fatalf("rel: %v", err)
		}
		out = append(out, fmt.Sprintf("%s:%d:%s", filepath.ToSlash(rel), match.Line, match.Text))
	}
	return out
}

func grepSortTree(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	for path, content := range map[string]string{
		"a.txt":        "needle a\n",
		"b.txt":        "needle b\n",
		"nested/c.txt": "needle c\n",
	} {
		full := filepath.Join(root, filepath.FromSlash(path))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	stamps := map[string]time.Time{
		"a.txt":        time.Date(2020, 1, 3, 0, 0, 0, 0, time.UTC),
		"b.txt":        time.Date(2020, 1, 2, 0, 0, 0, 0, time.UTC),
		"nested/c.txt": time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC),
	}
	for path, stamp := range stamps {
		if err := os.Chtimes(filepath.Join(root, filepath.FromSlash(path)), stamp, stamp); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

// TestRemoteGrepSortMatchesWalker pins the ordering group across engines: the
// same key must produce the same page order whether the walker or ripgrep did
// the scan, because the page boundaries depend on that order.
func TestRemoteGrepSortMatchesWalker(t *testing.T) {
	root := grepSortTree(t)
	runner := resolveRipgrepRunner()

	cases := []struct {
		name string
		opts searchcore.GrepOptions
		want []string
	}{
		{
			name: "path ascending",
			opts: searchcore.GrepOptions{Pattern: "needle", SortKey: "path", SortSet: true},
			want: []string{"a.txt:1:needle a", "b.txt:1:needle b", "nested/c.txt:1:needle c"},
		},
		{
			name: "path descending",
			opts: searchcore.GrepOptions{Pattern: "needle", SortKey: "path", SortReverse: true, SortSet: true},
			want: []string{"nested/c.txt:1:needle c", "b.txt:1:needle b", "a.txt:1:needle a"},
		},
		{
			name: "modified ascending",
			opts: searchcore.GrepOptions{Pattern: "needle", SortKey: "modified", SortSet: true},
			want: []string{"nested/c.txt:1:needle c", "b.txt:1:needle b", "a.txt:1:needle a"},
		},
		{
			name: "modified descending",
			opts: searchcore.GrepOptions{Pattern: "needle", SortKey: "modified", SortReverse: true, SortSet: true},
			want: []string{"a.txt:1:needle a", "b.txt:1:needle b", "nested/c.txt:1:needle c"},
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
			walkerLabels := orderedRelativeMatches(t, root, walker)
			assertStringList(t, walkerLabels, tc.want)

			if !runner.available() {
				return
			}
			rgResult, used, rgErr := runner.grep(context.Background(), opts)
			if rgErr != nil {
				t.Fatalf("ripgrep: %v", rgErr)
			}
			if !used {
				t.Fatal("ripgrep declined a sort key it supports")
			}
			assertStringList(t, orderedRelativeMatches(t, root, rgResult), tc.want)
		})
	}
}
