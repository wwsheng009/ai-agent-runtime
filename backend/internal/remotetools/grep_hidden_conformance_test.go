package remotetools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/internal/toolcore/searchcore"
)

func grepHiddenTree(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	for path, content := range map[string]string{
		"visible.txt":    "needle visible\n",
		".hidden.txt":    "needle hidden\n",
		"nested/ok.txt":  "needle nested\n",
		".cache/deep.md": "needle cached\n",
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

// TestRemoteGrepHiddenMatchesWalker pins the hidden contract across engines:
// both skip dotfiles by default and both reach them with hidden enabled.
func TestRemoteGrepHiddenMatchesWalker(t *testing.T) {
	root := grepHiddenTree(t)
	runner := resolveRipgrepRunner()

	cases := []struct {
		name          string
		includeHidden bool
		want          []string
	}{
		{
			name: "default skips hidden",
			want: []string{"nested/ok.txt:1:needle nested", "visible.txt:1:needle visible"},
		},
		{
			name:          "hidden included on request",
			includeHidden: true,
			want:          []string{".cache/deep.md:1:needle cached", ".hidden.txt:1:needle hidden", "nested/ok.txt:1:needle nested", "visible.txt:1:needle visible"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			opts := searchcore.GrepOptions{
				Pattern: "needle", Path: root, Limit: 50, IncludeHidden: tc.includeHidden,
			}
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

// TestRemoteGrepNoHiddenWinsOverHidden pins the parameter pair's precedence.
func TestRemoteGrepNoHiddenWinsOverHidden(t *testing.T) {
	cfg, workspace := newSearchFixture(t)
	tool := newRemoteGrepTool(cfg, workspace)

	result := execGrep(t, tool, map[string]interface{}{
		"pattern": "needle", "hidden": true, "no_hidden": true,
	})
	if strings.Contains(result.Content, ".hidden.txt") {
		t.Fatalf("no_hidden must win over hidden:\n%s", result.Content)
	}
	if got := result.Metadata["hidden_files"]; got != false {
		t.Fatalf("hidden_files = %v, want false", got)
	}
}
