package remotetools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/internal/toolcore/searchcore"
)

func grepLimitsTree(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	for path, content := range map[string]string{
		"top.txt":          "needle top\n",
		"small.txt":        "needle small\n",
		"dir/mid.txt":      "needle mid\n",
		"dir/sub/deep.txt": "needle deep\n",
		"big.txt":          "needle big\n" + strings.Repeat("padding line\n", 200),
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

// TestRemoteGrepDepthAndSizeMatchWalker pins the search-scope trims across
// engines: max_depth counts directories from each root, and max_filesize drops
// oversized files on both paths.
func TestRemoteGrepDepthAndSizeMatchWalker(t *testing.T) {
	root := grepLimitsTree(t)
	runner := resolveRipgrepRunner()

	cases := []struct {
		name string
		opts searchcore.GrepOptions
		want []string
	}{
		{
			name: "max_depth zero",
			opts: searchcore.GrepOptions{Pattern: "needle", MaxDepth: 0, MaxDepthSet: true},
			want: []string{"big.txt:1:needle big", "small.txt:1:needle small", "top.txt:1:needle top"},
		},
		{
			name: "max_depth one",
			opts: searchcore.GrepOptions{Pattern: "needle", MaxDepth: 1, MaxDepthSet: true},
			want: []string{"big.txt:1:needle big", "dir/mid.txt:1:needle mid", "small.txt:1:needle small", "top.txt:1:needle top"},
		},
		{
			name: "max_filesize drops the big file",
			opts: searchcore.GrepOptions{Pattern: "needle", MaxFileSizeBytes: 1024},
			want: []string{"dir/mid.txt:1:needle mid", "dir/sub/deep.txt:1:needle deep", "small.txt:1:needle small", "top.txt:1:needle top"},
		},
		{
			name: "depth and size combined",
			opts: searchcore.GrepOptions{Pattern: "needle", MaxDepth: 0, MaxDepthSet: true, MaxFileSizeBytes: 1024},
			want: []string{"small.txt:1:needle small", "top.txt:1:needle top"},
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
