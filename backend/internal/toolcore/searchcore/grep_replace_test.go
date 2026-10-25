package searchcore

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// writeReplaceFixture lays out the file every replacement test searches.
func writeReplaceFixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "rep.txt"), []byte("foo123\nbar\nfoo456\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return root
}

// TestWalkerGrepReplacesWholeLines is the pure-Go half of the contract: with no
// ripgrep on the host the tool must still substitute, because the capability
// belongs to the shared core, not to a binary that may be missing.
func TestWalkerGrepReplacesWholeLines(t *testing.T) {
	root := writeReplaceFixture(t)

	result, err := Grep(context.Background(), GrepOptions{
		Pattern: `foo(\d+)`,
		Path:    root,
		Replace: "num:$1",
		Limit:   10,
	})
	if err != nil {
		t.Fatalf("walker grep failed: %v", err)
	}
	if len(result.Matches) != 2 {
		t.Fatalf("got %d matches, want 2: %+v", len(result.Matches), result.Matches)
	}
	want := []struct {
		line int
		text string
	}{{1, "num:123"}, {3, "num:456"}}
	for i, w := range want {
		got := result.Matches[i]
		if got.Line != w.line || got.Text != w.text {
			t.Fatalf("match %d = line %d %q, want line %d %q", i, got.Line, got.Text, w.line, w.text)
		}
	}
}

// TestWalkerGrepReplacesOnlyMatchingFragments: `-o -r` prints one substituted
// fragment per match, so the walker expands each fragment rather than the line.
func TestWalkerGrepReplacesOnlyMatchingFragments(t *testing.T) {
	root := writeReplaceFixture(t)

	result, err := Grep(context.Background(), GrepOptions{
		Pattern:      `foo(\d+)`,
		Path:         root,
		Replace:      "[$1]",
		OnlyMatching: true,
		Limit:        10,
	})
	if err != nil {
		t.Fatalf("walker grep failed: %v", err)
	}
	if len(result.Matches) != 2 {
		t.Fatalf("got %d fragments, want 2: %+v", len(result.Matches), result.Matches)
	}
	for i, want := range []string{"[123]", "[456]"} {
		if got := result.Matches[i].Text; got != want {
			t.Fatalf("fragment %d = %q, want %q", i, got, want)
		}
		if got := result.Matches[i].Column; got != 1 {
			t.Fatalf("fragment %d column = %d, want 1", i, got)
		}
	}
}

// TestReplaceAgreesBetweenEngines runs the same call through both engines. The
// replacement is one contract, so the two answers must be identical - this is
// the assertion that keeps the walker's Go expansion from drifting away from
// ripgrep's own substitution.
func TestReplaceAgreesBetweenEngines(t *testing.T) {
	rgPath, err := exec.LookPath("rg")
	if err != nil {
		t.Skip("ripgrep is not installed; the engine half is covered by the argv and parse tests")
	}
	root := writeReplaceFixture(t)

	cases := []struct {
		name string
		opts GrepOptions
	}{
		{"whole lines", GrepOptions{Pattern: `foo(\d+)`, Replace: "num:$1"}},
		{"only matching", GrepOptions{Pattern: `foo(\d+)`, Replace: "[$1]", OnlyMatching: true}},
		{"columns", GrepOptions{Pattern: `foo(\d+)`, Replace: "num:$1", Column: true}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			opts := tc.opts
			opts.Path = root
			opts.Limit = 10

			walker, err := Grep(context.Background(), opts)
			if err != nil {
				t.Fatalf("walker: %v", err)
			}
			engine := Ripgrep{Path: rgPath}
			got, used, err := engine.Grep(context.Background(), opts)
			if err != nil {
				t.Fatalf("ripgrep: %v", err)
			}
			if !used {
				t.Fatal("the engine declined a replacement call")
			}
			if len(got.Matches) != len(walker.Matches) {
				t.Fatalf("engines disagree on match count: walker=%+v ripgrep=%+v",
					walker.Matches, got.Matches)
			}
			for i := range walker.Matches {
				w, g := walker.Matches[i], got.Matches[i]
				if w.Line != g.Line || w.Column != g.Column || w.Text != g.Text {
					t.Fatalf("engines disagree on match %d:\n walker  = %+v\n ripgrep = %+v", i, w, g)
				}
			}
		})
	}
}

// TestRipgrepReplacementModeAsksForTextOutput pins the argv: a replacement makes
// ripgrep print the substituted text, so the JSON event stream is replaced by
// NUL-delimited text - while the determinism base flags stay untouched.
func TestRipgrepReplacementModeAsksForTextOutput(t *testing.T) {
	root := writeReplaceFixture(t)

	runs := captureRipgrep(t, root, func(engine Ripgrep) (GrepResult, bool, error) {
		return engine.Grep(context.Background(), GrepOptions{
			Pattern: `foo(\d+)`,
			Path:    root,
			Replace: "num:$1",
			Limit:   10,
		})
	})
	if len(runs) == 0 {
		t.Fatal("the engine never ran")
	}
	joined := strings.Join(runs[0].args, " ")
	for _, want := range []string{"--null", "--line-number", "--replace num:$1"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("ripgrep was not asked for %q:\n%s", want, joined)
		}
	}
	if strings.Contains(joined, "--json") {
		t.Fatalf("a replacement cannot be read from the JSON stream, so --json must be gone:\n%s", joined)
	}
	for _, base := range []string{"--no-config", "--threads 1", "--sort path"} {
		if !strings.Contains(joined, base) {
			t.Fatalf("the determinism flag %q must survive the mode switch:\n%s", base, joined)
		}
	}
}

// TestRipgrepReplacementParsesNullDelimitedLines feeds the parser the exact byte
// shape `rg --null -n -r` produces, including an absolute Windows-style path
// whose own colon must not be mistaken for the line separator.
func TestRipgrepReplacementParsesNullDelimitedLines(t *testing.T) {
	cases := []struct {
		name       string
		line       string
		wantColumn bool
		wantPath   string
		wantLine   int
		wantCol    int
		wantText   string
	}{
		{
			name: "plain", line: "/tmp/rep.txt\x001:num:123",
			wantPath: "/tmp/rep.txt", wantLine: 1, wantText: "num:123",
		},
		{
			name: "column", line: "/tmp/rep.txt\x003:7:num:456", wantColumn: true,
			wantPath: "/tmp/rep.txt", wantLine: 3, wantCol: 7, wantText: "num:456",
		},
		{
			name:     "path with a colon stays intact",
			line:     "C:\\work\\rep.txt\x002:num:9",
			wantPath: "C:\\work\\rep.txt", wantLine: 2, wantText: "num:9",
		},
		{
			name:     "text that looks like a prefix is kept whole",
			line:     "/tmp/rep.txt\x001:2:3: kept",
			wantPath: "/tmp/rep.txt", wantLine: 1, wantText: "2:3: kept",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path, lineNo, column, text, ok := parseRipgrepReplaceLine([]byte(tc.line), tc.wantColumn)
			if !ok {
				t.Fatalf("the line %q must parse", tc.line)
			}
			if path != tc.wantPath || lineNo != tc.wantLine || column != tc.wantCol || text != tc.wantText {
				t.Fatalf("parsed path=%q line=%d column=%d text=%q, want %q/%d/%d/%q",
					path, lineNo, column, text, tc.wantPath, tc.wantLine, tc.wantCol, tc.wantText)
			}
		})
	}
}

// TestGrepReplaceRunsOnTheSharedHandler pins the routing: a replacement call is
// answered by the shared handler (metadata reports the template), and with
// context the substituted line wins over the file re-read while the surrounding
// lines stay raw - exactly what `rg -r -C` prints.
func TestGrepReplaceRunsOnTheSharedHandler(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "ctx.txt"), []byte("before\nMATCH2\nafter\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	result := runGrepHandler(t, root, map[string]any{
		"pattern": "MATCH", "replace": "hit", "context": 1,
	})
	if result.Metadata["replace"] != "hit" {
		t.Fatalf("metadata.replace = %#v, want the template", result.Metadata["replace"])
	}
	content := result.Content
	if !strings.Contains(content, "hit") {
		t.Fatalf("the substituted line must be rendered:\n%s", content)
	}
	if strings.Contains(content, "MATCH") {
		t.Fatalf("the pre-substitution line must not survive context expansion:\n%s", content)
	}
	for _, want := range []string{"before", "after"} {
		if !strings.Contains(content, want) {
			t.Fatalf("context line %q must still come from the file:\n%s", want, content)
		}
	}
}
