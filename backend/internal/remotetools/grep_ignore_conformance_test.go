package remotetools

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/internal/toolcore/searchcore"
)

func grepIgnoreTree(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	for path, content := range map[string]string{
		".gitignore":      "ignored.txt\nbuild\nnested/*.log\n!nested/keep.log\n",
		"ignored.txt":     "needle ignored\n",
		"keep.txt":        "needle keep\n",
		"build/x.txt":     "needle build\n",
		"nested/a.log":    "needle log\n",
		"nested/keep.log": "needle keeplog\n",
		"nested/c.txt":    "needle nested\n",
		"sub/.gitignore":  "local.txt\n",
		"sub/local.txt":   "needle sub ignored\n",
		"sub/other.txt":   "needle sub kept\n",
		"custom.ignore":   "keep.txt\n",
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

// TestRemoteGrepIgnoreMatchesWalker pins the ignore-file contract across
// engines: the same .gitignore sources and the same no_ignore switches must
// select the same files, which is what the local tool has always done.
func TestRemoteGrepIgnoreMatchesWalker(t *testing.T) {
	root := grepIgnoreTree(t)
	runner := resolveRipgrepRunner()
	customFile := filepath.Join(root, "custom.ignore")

	cases := []struct {
		name string
		opts searchcore.GrepOptions
		want []string
	}{
		{
			name: "default respects .gitignore",
			opts: searchcore.GrepOptions{Pattern: "needle"},
			want: []string{"keep.txt:1:needle keep", "nested/c.txt:1:needle nested", "nested/keep.log:1:needle keeplog", "sub/other.txt:1:needle sub kept"},
		},
		{
			name: "no_ignore searches everything",
			opts: searchcore.GrepOptions{Pattern: "needle", NoIgnore: true},
			want: []string{
				"build/x.txt:1:needle build", "ignored.txt:1:needle ignored", "keep.txt:1:needle keep",
				"nested/a.log:1:needle log", "nested/c.txt:1:needle nested", "nested/keep.log:1:needle keeplog",
				"sub/local.txt:1:needle sub ignored", "sub/other.txt:1:needle sub kept",
			},
		},
		{
			name: "explicit ignore file stacks on .gitignore",
			opts: searchcore.GrepOptions{Pattern: "needle", IgnoreFiles: []string{customFile}},
			want: []string{"nested/c.txt:1:needle nested", "nested/keep.log:1:needle keeplog", "sub/other.txt:1:needle sub kept"},
		},
		{
			name: "no_ignore_vcs drops the vcs rules",
			opts: searchcore.GrepOptions{Pattern: "needle", NoIgnoreVCS: true},
			want: []string{
				"build/x.txt:1:needle build", "ignored.txt:1:needle ignored", "keep.txt:1:needle keep",
				"nested/a.log:1:needle log", "nested/c.txt:1:needle nested", "nested/keep.log:1:needle keeplog",
				"sub/local.txt:1:needle sub ignored", "sub/other.txt:1:needle sub kept",
			},
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
			if rgErr != nil || !used {
				t.Fatalf("ripgrep used=%v err=%v", used, rgErr)
			}
			assertStringList(t, relativeMatches(t, root, rgResult), tc.want)
		})
	}
}
