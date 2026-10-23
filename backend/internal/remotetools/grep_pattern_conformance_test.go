package remotetools

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/internal/toolcore/searchcore"
)

// TestRemoteGrepPatternOptionsMatchWalker pins the shared pattern semantics:
// literal/word/line_regexp/case modes/invert and multi-pattern OR must mean the
// same thing on the ripgrep engine and on the pure-Go walker. The local legacy
// tool compiles its expression through the same searchcore definition.
func TestRemoteGrepPatternOptionsMatchWalker(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "sample.txt")
	if err := os.WriteFile(target, []byte("alpha\nAlpha\nA.B\nerror\nerr\nfoo bar\nfoo\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runner := resolveRipgrepRunner()
	if !runner.available() {
		t.Skip("ripgrep is not available")
	}

	cases := []struct {
		name string
		opts searchcore.GrepOptions
	}{
		{"literal", searchcore.GrepOptions{Pattern: "foo.", Literal: true}},
		{"regexp-dot", searchcore.GrepOptions{Pattern: "foo."}},
		{"ignore-case", searchcore.GrepOptions{Pattern: "alpha", IgnoreCase: true}},
		{"case-insensitive-alias", searchcore.GrepOptions{Pattern: "alpha", CaseInsensitive: true}},
		{"case-sensitive-overrides", searchcore.GrepOptions{Pattern: "alpha", IgnoreCase: true, CaseSensitive: true}},
		{"smart-case-lower", searchcore.GrepOptions{Pattern: "alpha", SmartCase: true}},
		{"smart-case-upper", searchcore.GrepOptions{Pattern: "Alpha", SmartCase: true}},
		{"word", searchcore.GrepOptions{Pattern: "err", Word: true}},
		{"line-regexp", searchcore.GrepOptions{Pattern: "foo", LineRegexp: true}},
		{"patterns-or", searchcore.GrepOptions{Patterns: []string{"alpha", "err"}}},
		{"patterns-with-ignore-case", searchcore.GrepOptions{Patterns: []string{"ALPHA", "ERR"}, IgnoreCase: true}},
		{"invert-match", searchcore.GrepOptions{Pattern: "alpha", InvertMatch: true}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			opts := tc.opts
			opts.Path = target
			opts.Limit = 50
			walker, err := searchcore.Grep(context.Background(), opts)
			if err != nil {
				t.Fatalf("walker: %v", err)
			}
			rgResult, used, err := runner.grep(context.Background(), opts)
			if err != nil || !used {
				t.Fatalf("ripgrep used=%v err=%v", used, err)
			}
			if len(rgResult.Matches) != len(walker.Matches) {
				t.Fatalf("match count: rg=%+v walker=%+v", rgResult.Matches, walker.Matches)
			}
			for i := range walker.Matches {
				got, want := rgResult.Matches[i], walker.Matches[i]
				if filepath.Base(got.File) != filepath.Base(want.File) || got.Line != want.Line || got.Text != want.Text {
					t.Fatalf("mismatch at %d: rg=%+v walker=%+v", i, got, want)
				}
			}
		})
	}
}
