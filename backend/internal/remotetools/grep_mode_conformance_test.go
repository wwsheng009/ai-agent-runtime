package remotetools

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/internal/toolcore/searchcore"
)

func grepModeTree(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	for name, content := range map[string]string{
		"a.txt": "hit one\nhit two\nhit three\n",
		"b.txt": "hit again\nmiss\n",
		"c.txt": "nothing here\n",
	} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func modeEntries(result searchcore.GrepResult) []string {
	out := make([]string, 0, len(result.Files))
	for _, entry := range result.Files {
		out = append(out, fmt.Sprintf("%s:%d", filepath.Base(entry.File), entry.Count))
	}
	return out
}

// TestRemoteGrepModesMatchWalker pins the files/count mode contract across
// engines: the same page, the same per-file counts and the same has_more bit.
func TestRemoteGrepModesMatchWalker(t *testing.T) {
	root := grepModeTree(t)
	runner := resolveRipgrepRunner()

	cases := []struct {
		name     string
		mode     searchcore.GrepMode
		limit    int
		offset   int
		want     []string
		wantMore bool
	}{
		{"files all", searchcore.GrepModeFiles, 50, 0, []string{"a.txt:3", "b.txt:1"}, false},
		{"files page one", searchcore.GrepModeFiles, 1, 0, []string{"a.txt:3"}, true},
		{"files page two", searchcore.GrepModeFiles, 1, 1, []string{"b.txt:1"}, false},
		{"count all", searchcore.GrepModeCount, 50, 0, []string{"a.txt:3", "b.txt:1"}, false},
		{"count page one keeps complete counts", searchcore.GrepModeCount, 1, 0, []string{"a.txt:3"}, true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			opts := searchcore.GrepOptions{
				Pattern: "hit", Path: root, Mode: tc.mode,
				Limit: tc.limit, Offset: tc.offset,
			}
			walker, err := searchcore.Grep(context.Background(), opts)
			if err != nil {
				t.Fatalf("walker: %v", err)
			}
			assertStringList(t, modeEntries(walker), tc.want)
			if walker.Truncated != tc.wantMore {
				t.Fatalf("walker truncated=%v want %v (%+v)", walker.Truncated, tc.wantMore, walker.Files)
			}

			if !runner.available() {
				return
			}
			rgResult, used, rgErr := runner.grep(context.Background(), opts)
			if rgErr != nil || !used {
				t.Fatalf("ripgrep used=%v err=%v", used, rgErr)
			}
			assertStringList(t, modeEntries(rgResult), tc.want)
			if rgResult.Truncated != tc.wantMore {
				t.Fatalf("ripgrep truncated=%v want %v (%+v)", rgResult.Truncated, tc.wantMore, rgResult.Files)
			}
		})
	}
}

func TestRemoteGrepRejectsUnsupportedMode(t *testing.T) {
	root := grepModeTree(t)
	for _, engine := range []struct {
		name string
		rg   ripgrepRunner
	}{
		{name: "walker"},
		{name: "ripgrep", rg: resolveRipgrepRunner()},
	} {
		if engine.name == "ripgrep" && !engine.rg.available() {
			continue
		}
		t.Run(engine.name, func(t *testing.T) {
			opts := searchcore.GrepOptions{
				Pattern: "hit", Path: root, Mode: searchcore.GrepMode("files_without"),
			}
			if engine.name == "walker" {
				if _, err := searchcore.Grep(context.Background(), opts); err == nil {
					t.Fatal("walker must reject an unsupported mode")
				}
				return
			}
			if _, used, err := engine.rg.grep(context.Background(), opts); err == nil || !used {
				t.Fatalf("ripgrep must reject an unsupported mode: used=%v err=%v", used, err)
			}
		})
	}
}
