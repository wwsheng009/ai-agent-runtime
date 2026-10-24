package remotetools

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
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

// TestRemoteGrepBinaryHandlingMatchesWalker pins the binary contract across
// engines. The two implementations decide "is this file binary" in completely
// different ways (a fixed head probe versus ripgrep's own detector), so this is
// exactly the kind of divergence a shared contract must catch.
func TestRemoteGrepBinaryHandlingMatchesWalker(t *testing.T) {
	root := t.TempDir()
	files := map[string]string{
		"plain.txt":   "needle plain\n",
		"before.dat":  "needle before\x00DATA\n",
		"after.dat":   "DATA\x00needle after\n",
		"nomatch.dat": "DATA\x00DATA\n",
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	runner := resolveRipgrepRunner()

	for _, level := range []int{0, 1, 2, 3} {
		t.Run(fmt.Sprintf("level %d", level), func(t *testing.T) {
			opts := searchcore.GrepOptions{
				Pattern: "needle", Path: root, Limit: 50,
				Unrestricted: level, NoIgnore: level > 0,
			}
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
			if got, want := grepMatchFiles(rgResult), grepMatchFiles(walker); !equalStringSlices(got, want) {
				t.Fatalf("engines diverged at level %d:\nripgrep: %v\nwalker:  %v", level, got, want)
			}
			// Line numbers must agree too. This is the regression guard for the
			// first implementation, which used rg --binary: that flag makes the
			// NUL a line terminator, so "a\0b" became lines 1 and 2 while the
			// walker kept it as one line.
			if got, want := grepMatchLines(rgResult), grepMatchLines(walker); !equalStringSlices(got, want) {
				t.Fatalf("engines disagreed on match lines at level %d:\nripgrep: %v\nwalker:  %v", level, got, want)
			}
			for _, match := range rgResult.Matches {
				if strings.ContainsRune(match.Text, 0) {
					t.Fatalf("ripgrep emitted a raw NUL: %q", match.Text)
				}
			}
		})
	}
}

func grepMatchLines(result searchcore.GrepResult) []string {
	out := make([]string, 0, len(result.Matches))
	for _, match := range result.Matches {
		out = append(out, fmt.Sprintf("%s:%d:%s", filepath.Base(match.File), match.Line, match.Text))
	}
	return out
}

func grepMatchFiles(result searchcore.GrepResult) []string {
	seen := make(map[string]bool, len(result.Matches))
	out := make([]string, 0, len(result.Matches))
	for _, match := range result.Matches {
		name := filepath.Base(match.File)
		if seen[name] {
			continue
		}
		seen[name] = true
		out = append(out, name)
	}
	return out
}

func equalStringSlices(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// TestRemoteGrepOneFileSystemMatchesWalker pins one_file_system across engines.
//
// A fabricated boundary would only be visible to the walker: ripgrep decides
// device identity in its own process, so an injected seam cannot reach it. That
// is precisely why this test does NOT compare the two engine outputs on a
// simulated boundary - it would assert an agreement that cannot exist in a
// test process without mount privileges. What it does pin is the contract both
// sides share: a tree on one real filesystem is searched identically, and the
// walker honours a boundary.
func TestRemoteGrepOneFileSystemMatchesWalker(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"local.txt", "other/near.txt"} {
		path := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("needle "+name+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	opts := searchcore.GrepOptions{
		Pattern: "needle", Path: root, Limit: 50, OneFileSystem: true,
	}
	walker, err := searchcore.Grep(context.Background(), opts)
	if err != nil {
		t.Fatalf("walker: %v", err)
	}
	assertStringList(t, grepMatchFiles(walker), []string{"local.txt", "near.txt"})

	if runner := resolveRipgrepRunner(); runner.available() {
		rgResult, used, rgErr := runner.grep(context.Background(), opts)
		if rgErr != nil || !used {
			t.Fatalf("ripgrep used=%v err=%v", used, rgErr)
		}
		if got, want := grepMatchFiles(rgResult), grepMatchFiles(walker); !equalStringSlices(got, want) {
			t.Fatalf("engines diverged on one_file_system:\nripgrep: %v\nwalker:  %v", got, want)
		}
	}
}

// TestRemoteGrepOneFileSystemFlagReachesRipgrep proves the flag is actually
// forwarded, since the boundary itself cannot be fabricated for a separate
// process. The fake runner records the argv and returns one path.
func TestRemoteGrepOneFileSystemFlagReachesRipgrep(t *testing.T) {
	var gotArgs []string
	engine := searchcore.Ripgrep{
		Path: "rg",
		Stream: func(ctx context.Context, args []string, dir string, consume func(line []byte) bool) error {
			gotArgs = append([]string(nil), args...)
			return nil
		},
	}
	root := t.TempDir()
	if _, used, err := engine.Grep(context.Background(), searchcore.GrepOptions{
		Pattern: "needle", Path: root, OneFileSystem: true,
	}); err != nil || !used {
		t.Fatalf("used=%v err=%v", used, err)
	}
	if !strings.Contains(strings.Join(gotArgs, " "), "--one-file-system") {
		t.Fatalf("--one-file-system was not forwarded: %v", gotArgs)
	}

	gotArgs = nil
	if _, used, err := engine.Grep(context.Background(), searchcore.GrepOptions{
		Pattern: "needle", Path: root,
	}); err != nil || !used {
		t.Fatalf("used=%v err=%v", used, err)
	}
	if strings.Contains(strings.Join(gotArgs, " "), "--one-file-system") {
		t.Fatalf("--one-file-system must not be sent when unset: %v", gotArgs)
	}
}

// TestRemoteGrepNoMessagesFlagReachesRipgrep proves no_messages is forwarded,
// since its visible effect in rg is on stderr only.
func TestRemoteGrepNoMessagesFlagReachesRipgrep(t *testing.T) {
	var gotArgs []string
	engine := searchcore.Ripgrep{
		Path: "rg",
		Stream: func(ctx context.Context, args []string, dir string, consume func(line []byte) bool) error {
			gotArgs = append([]string(nil), args...)
			return nil
		},
	}
	root := t.TempDir()
	if _, used, err := engine.Grep(context.Background(), searchcore.GrepOptions{
		Pattern: "needle", Path: root, NoMessages: true,
	}); err != nil || !used {
		t.Fatalf("used=%v err=%v", used, err)
	}
	if !strings.Contains(strings.Join(gotArgs, " "), "--no-messages") {
		t.Fatalf("--no-messages was not forwarded: %v", gotArgs)
	}

	gotArgs = nil
	if _, used, err := engine.Grep(context.Background(), searchcore.GrepOptions{
		Pattern: "needle", Path: root,
	}); err != nil || !used {
		t.Fatalf("used=%v err=%v", used, err)
	}
	if strings.Contains(strings.Join(gotArgs, " "), "--no-messages") {
		t.Fatalf("--no-messages must not be sent when unset: %v", gotArgs)
	}
}

// TestRemoteGrepColumnMatchesWalker pins column across engines. The two derive
// it in completely different ways (regexp indices versus ripgrep's JSON
// offsets), so this is the case most likely to drift by one byte.
func TestRemoteGrepColumnMatchesWalker(t *testing.T) {
	root := t.TempDir()
	content := "   indented needle here\n" +
		"second line\n" +
		strings.Repeat("y", 300) + "needle tail\n"
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	runner := resolveRipgrepRunner()

	for _, onlyMatching := range []bool{false, true} {
		opts := searchcore.GrepOptions{
			Pattern: "needle", Path: root, Limit: 50,
			Column: true, OnlyMatching: onlyMatching,
		}
		walker, err := searchcore.Grep(context.Background(), opts)
		if err != nil {
			t.Fatalf("onlyMatching=%v: %v", onlyMatching, err)
		}
		if !runner.available() {
			continue
		}
		rgResult, used, rgErr := runner.grep(context.Background(), opts)
		if rgErr != nil || !used {
			t.Fatalf("ripgrep used=%v err=%v", used, rgErr)
		}
		got, want := grepMatchLines(rgResult), grepMatchLines(walker)
		if !equalStringSlices(got, want) {
			t.Fatalf("engines disagreed on column (onlyMatching=%v):\nripgrep: %v\nwalker:  %v",
				onlyMatching, got, want)
		}
	}
}

// TestRemoteGrepSemanticsMatchWalker pins the three parameters added in the
// convergence audit. Each one changes the result set rather than its
// presentation, so a divergence here is a correctness bug, not a style issue.
func TestRemoteGrepSemanticsMatchWalker(t *testing.T) {
	root := t.TempDir()
	for name, content := range map[string]string{
		"a.txt":      "foo foo\nfoo bar\n",
		"UP.GO":      "needle up\n",
		"low.go":     "needle low\n",
		"UPSKIP.TXT": "needle upskip\n",
		"keep.txt":   "needle keep\n",
		".gitignore": "upskip.txt\n",
	} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	runner := resolveRipgrepRunner()

	cases := []struct {
		name string
		opts searchcore.GrepOptions
	}{
		{"count_matches", searchcore.GrepOptions{
			Pattern: "foo", Path: root, Mode: searchcore.GrepModeCount,
			CountMatches: true, OnlyMatching: true, Limit: 50,
		}},
		{"glob_case_insensitive", searchcore.GrepOptions{
			Pattern: "needle", Path: root, Glob: "*.go",
			GlobCaseInsensitive: true, Limit: 50,
		}},
		{"ignore_file_case_insensitive", searchcore.GrepOptions{
			Pattern: "needle", Path: root, Glob: "*.txt",
			IgnoreFileCaseInsensitive: true, Limit: 50,
		}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			walker, err := searchcore.Grep(context.Background(), tc.opts)
			if err != nil {
				t.Fatalf("walker: %v", err)
			}
			if !runner.available() {
				return
			}
			rgResult, used, rgErr := runner.grep(context.Background(), tc.opts)
			if rgErr != nil || !used {
				t.Fatalf("ripgrep used=%v err=%v", used, rgErr)
			}
			got, want := grepMatchLines(rgResult), grepMatchLines(walker)
			if tc.opts.Mode != "" {
				got, want = grepMatchFiles(rgResult), grepMatchFiles(walker)
			}
			if !equalStringSlices(got, want) {
				t.Fatalf("engines diverged:\nripgrep: %v\nwalker:  %v", got, want)
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
