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

// TestRemoteGrepFilesWithoutMatchesWalker pins files_without across engines: the
// same page, the same has_more bit and an empty match list. rg ignores --json in
// this mode and prints plain paths, so the ripgrep engine parses them instead;
// this test is what keeps that separate parse path honest.
func TestRemoteGrepFilesWithoutMatchesWalker(t *testing.T) {
	runner := resolveRipgrepRunner()
	// a/b/d have no match, c has one: enough files for a real has_more page.
	pagedRoot := t.TempDir()
	for name, content := range map[string]string{
		"a.txt": "nothing\n",
		"b.txt": "nothing\n",
		"c.txt": "hit\n",
		"d.txt": "nothing\n",
	} {
		if err := os.WriteFile(filepath.Join(pagedRoot, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	cases := []struct {
		name     string
		limit    int
		offset   int
		want     []string
		wantMore bool
	}{
		{"all", 50, 0, []string{"a.txt:0", "b.txt:0", "d.txt:0"}, false},
		{"first page", 2, 0, []string{"a.txt:0", "b.txt:0"}, true},
		{"last page", 2, 2, []string{"d.txt:0"}, false},
		{"offset past the last file", 2, 5, nil, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			opts := searchcore.GrepOptions{
				Pattern: "hit", Path: pagedRoot, Mode: searchcore.GrepModeFilesWithout,
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
			if len(walker.Matches) != 0 {
				t.Fatalf("walker must not collect match lines: %+v", walker.Matches)
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

// TestRemoteGrepRejectsUnknownMode keeps a genuinely unknown spelling failing on
// both engines: the enum must stay honest about what it can serve.
func TestRemoteGrepRejectsUnknownMode(t *testing.T) {
	root := grepModeTree(t)
	opts := searchcore.GrepOptions{
		Pattern: "hit", Path: root, Mode: searchcore.GrepMode("bogus"),
	}
	if _, err := searchcore.Grep(context.Background(), opts); err == nil {
		t.Fatal("walker must reject an unknown mode")
	}
	if runner := resolveRipgrepRunner(); runner.available() {
		if _, used, err := runner.grep(context.Background(), opts); err == nil || !used {
			t.Fatalf("ripgrep must reject an unknown mode: used=%v err=%v", used, err)
		}
	}
}
