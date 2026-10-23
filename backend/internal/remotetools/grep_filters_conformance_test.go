package remotetools

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/internal/toolcore/searchcore"
)

func grepFilterTree(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	for path, content := range map[string]string{
		"a.go":             "needle a\n",
		"b.ts":             "needle b\n",
		"nested/c.go":      "needle c\n",
		"nested/d_test.go": "needle d\n",
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

func relativeMatches(t *testing.T, root string, result searchcore.GrepResult) []string {
	t.Helper()
	out := make([]string, 0, len(result.Matches))
	for _, match := range result.Matches {
		rel, err := filepath.Rel(root, match.File)
		if err != nil {
			t.Fatalf("rel: %v", err)
		}
		out = append(out, fmt.Sprintf("%s:%d:%s", filepath.ToSlash(rel), match.Line, match.Text))
	}
	sort.Strings(out)
	return out
}

func assertStringList(t *testing.T, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
}

// TestRemoteGrepFiltersMatchWalker pins the include/exclude contract across
// engines: for every filter set the two paths must select the same files, and
// when ripgrep's glob dialect cannot express a filter the engine must decline
// instead of silently returning a different set.
func TestRemoteGrepFiltersMatchWalker(t *testing.T) {
	root := grepFilterTree(t)
	runner := resolveRipgrepRunner()

	cases := []struct {
		name         string
		opts         searchcore.GrepOptions
		want         []string
		expectWalker bool // rg must decline (unsupported glob dialect)
	}{
		{
			name: "include bare extension",
			opts: searchcore.GrepOptions{Pattern: "needle", Include: []string{"*.go"}},
			want: []string{"a.go:1:needle a", "nested/c.go:1:needle c", "nested/d_test.go:1:needle d"},
		},
		{
			name:         "include with relative path",
			opts:         searchcore.GrepOptions{Pattern: "needle", Include: []string{"nested/*.go"}},
			want:         []string{"nested/c.go:1:needle c", "nested/d_test.go:1:needle d"},
			expectWalker: true,
		},
		{
			name:         "brace include",
			opts:         searchcore.GrepOptions{Pattern: "needle", Include: []string{"*.{go,ts}"}},
			want:         []string{"a.go:1:needle a", "b.ts:1:needle b", "nested/c.go:1:needle c", "nested/d_test.go:1:needle d"},
			expectWalker: true,
		},
		{
			name: "exclude bare extension",
			opts: searchcore.GrepOptions{Pattern: "needle", Exclude: []string{"*.ts"}},
			want: []string{"a.go:1:needle a", "nested/c.go:1:needle c", "nested/d_test.go:1:needle d"},
		},
		{
			name: "exclude wins over include",
			opts: searchcore.GrepOptions{Pattern: "needle", Include: []string{"*.go"}, Exclude: []string{"*_test.go"}},
			want: []string{"a.go:1:needle a", "nested/c.go:1:needle c"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			opts := tc.opts
			opts.Path = root
			walker, err := searchcore.Grep(context.Background(), opts)
			if err != nil {
				t.Fatalf("walker: %v", err)
			}
			assertStringList(t, relativeMatches(t, root, walker), tc.want)

			if !runner.available() {
				return
			}
			rgResult, used, rgErr := runner.grep(context.Background(), opts)
			if rgErr != nil {
				t.Fatalf("ripgrep: %v", rgErr)
			}
			if tc.expectWalker {
				if used {
					t.Fatalf("ripgrep accepted a filter the walker owns; results would silently diverge")
				}
				return
			}
			if !used {
				t.Fatalf("ripgrep declined a simple filter it should handle")
			}
			assertStringList(t, relativeMatches(t, root, rgResult), tc.want)
		})
	}
}
