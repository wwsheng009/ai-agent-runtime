package remotetools

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/internal/toolcore/searchcore"
)

func multiRootFixture(t *testing.T) (first, second string) {
	t.Helper()
	first = t.TempDir()
	second = t.TempDir()
	for root, files := range map[string]map[string]string{
		first:  {"a.txt": "needle a\n", "b.txt": "miss\n", "d.txt": "needle d\n"},
		second: {"c.txt": "needle c\n"},
	} {
		for name, content := range files {
			if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
	return first, second
}

func matchNames(result searchcore.GrepResult) []string {
	names := make([]string, 0, len(result.Matches))
	for _, match := range result.Matches {
		names = append(names, filepath.Base(match.File)+":"+match.Text)
	}
	return names
}

// TestRemoteGrepMultiRootMatchesWalker pins the multi-root contract across
// engines: roots are searched in the configured order and the page window
// applies to the concatenated result, so a page limit produces the same
// matches (and the same has_more evidence) on both engines.
func TestRemoteGrepMultiRootMatchesWalker(t *testing.T) {
	first, second := multiRootFixture(t)
	runner := resolveRipgrepRunner()

	cases := []struct {
		name      string
		limit     int
		offset    int
		wantNames []string
		wantMore  bool
	}{
		{
			name:      "all matches in root order",
			limit:     50,
			wantNames: []string{"a.txt:needle a", "d.txt:needle d", "c.txt:needle c"},
		},
		{
			name:      "page limit stays inside the first root",
			limit:     1,
			wantNames: []string{"a.txt:needle a"},
			wantMore:  true,
		},
		{
			name:      "offset crosses into the next root",
			limit:     1,
			offset:    2,
			wantNames: []string{"c.txt:needle c"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			opts := searchcore.GrepOptions{
				Pattern: "needle", Path: first, Paths: []string{second},
				Limit: tc.limit, Offset: tc.offset,
			}
			walker, err := searchcore.Grep(context.Background(), opts)
			if err != nil {
				t.Fatalf("walker: %v", err)
			}
			assertStringList(t, matchNames(walker), tc.wantNames)
			if walker.Truncated != tc.wantMore {
				t.Fatalf("walker truncated=%v want %v (%+v)", walker.Truncated, tc.wantMore, walker.Matches)
			}

			if !runner.available() {
				return
			}
			rgResult, used, rgErr := runner.grep(context.Background(), opts)
			if rgErr != nil || !used {
				t.Fatalf("ripgrep used=%v err=%v", used, rgErr)
			}
			assertStringList(t, matchNames(rgResult), tc.wantNames)
			if rgResult.Truncated != tc.wantMore {
				t.Fatalf("ripgrep truncated=%v want %v (%+v)", rgResult.Truncated, tc.wantMore, rgResult.Matches)
			}
			if rgResult.SingleFile || walker.SingleFile {
				t.Fatalf("two roots must not report single_file: rg=%v walker=%v", rgResult.SingleFile, walker.SingleFile)
			}
		})
	}
}
