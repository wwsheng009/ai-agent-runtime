package remotetools

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/internal/toolcore/searchcore"
)

func grepTypeTree(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	for path, content := range map[string]string{
		"a.go":         "needle go\n",
		"a.gold":       "needle gold\n",
		"a.md":         "needle md\n",
		"b.py":         "needle py\n",
		"e.txt":        "needle text\n",
		"nested/d.cpp": "needle cpp\n",
		"nested/c.go":  "needle nested go\n",
		"nested/d.hpp": "needle header\n",
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

// TestRemoteGrepTypeFiltersMatchWalker pins the type/type_not contract across
// engines: the shared table expands a type name into the same globs the walker
// matches, and a type spelling the glob dialect cannot express falls back to
// the walker instead of silently selecting a different file set.
func TestRemoteGrepTypeFiltersMatchWalker(t *testing.T) {
	root := grepTypeTree(t)
	runner := resolveRipgrepRunner()

	cases := []struct {
		name         string
		opts         searchcore.GrepOptions
		want         []string
		expectWalker bool
	}{
		{
			name: "type go",
			opts: searchcore.GrepOptions{Pattern: "needle", Type: "go"},
			want: []string{"a.go:1:needle go", "nested/c.go:1:needle nested go"},
		},
		{
			name: "type cpp expands to several globs",
			opts: searchcore.GrepOptions{Pattern: "needle", Type: "cpp"},
			want: []string{"nested/d.cpp:1:needle cpp", "nested/d.hpp:1:needle header"},
		},
		{
			name: "type_not removes a type",
			opts: searchcore.GrepOptions{Pattern: "needle", TypeNot: "go"},
			want: []string{
				"a.gold:1:needle gold",
				"a.md:1:needle md",
				"b.py:1:needle py",
				"e.txt:1:needle text",
				"nested/d.cpp:1:needle cpp",
				"nested/d.hpp:1:needle header",
			},
		},
		{
			name: "type is a union with include",
			opts: searchcore.GrepOptions{Pattern: "needle", Type: "go", Include: []string{"*.py"}},
			want: []string{"a.go:1:needle go", "b.py:1:needle py", "nested/c.go:1:needle nested go"},
		},
		{
			name:         "unknown type with unsafe glob characters takes the walker",
			opts:         searchcore.GrepOptions{Pattern: "needle", Type: "we[i]rd"},
			want:         nil,
			expectWalker: true,
		},
		{
			name: "type_add defines a type on both engines",
			opts: searchcore.GrepOptions{Pattern: "needle", Type: "doc", TypeAdd: []string{"doc:*.md"}},
			want: []string{"a.md:1:needle md"},
		},
		{
			name: "type_add extends a built-in type on both engines",
			opts: searchcore.GrepOptions{Pattern: "needle", Type: "go", TypeAdd: []string{"go:*.gold"}},
			want: []string{"a.go:1:needle go", "a.gold:1:needle gold", "nested/c.go:1:needle nested go"},
		},
		{
			name: "type_clear removes the extra globs on both engines",
			opts: searchcore.GrepOptions{Pattern: "needle", Type: "cpp", TypeClear: []string{"cpp"}},
			want: []string{"nested/d.cpp:1:needle cpp"},
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
					t.Fatal("ripgrep accepted a derived glob the walker owns; results would silently diverge")
				}
				return
			}
			if !used {
				t.Fatal("ripgrep declined a simple derived glob it should handle")
			}
			assertStringList(t, relativeMatches(t, root, rgResult), tc.want)
		})
	}
}
