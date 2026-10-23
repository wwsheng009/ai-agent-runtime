package searchcore

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func writePatternFixture(t *testing.T, name, content string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	return path
}

func grepFixtureLines(t *testing.T, path string, opts GrepOptions) []string {
	t.Helper()
	opts.Path = path
	result, err := Grep(context.Background(), opts)
	if err != nil {
		t.Fatalf("grep %+v: %v", opts, err)
	}
	lines := make([]string, 0, len(result.Matches))
	for _, match := range result.Matches {
		lines = append(lines, fmt.Sprintf("%d:%s", match.Line, match.Text))
	}
	return lines
}

func assertLines(t *testing.T, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("got %q, want %q", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("line %d: got %q, want %q (full got %q)", i, got[i], want[i], got)
		}
	}
}

// TestGrepPatternOptions is the canonical matrix for the switches that used to
// exist only inside the local legacy tool. Every engine must produce these
// exact lines; the ripgrep conformance test in remotetools runs the same sets
// through rg.
func TestGrepPatternOptions(t *testing.T) {
	file := writePatternFixture(t, "sample.txt",
		"alpha\nAlpha\nA.B\nerror\nerr\nfoo bar\nfoo\n")

	cases := []struct {
		name string
		opts GrepOptions
		want []string
	}{
		{
			name: "literal treats metacharacters as text",
			opts: GrepOptions{Pattern: "foo.", Literal: true},
			want: nil,
		},
		{
			name: "regexp metacharacter without literal",
			opts: GrepOptions{Pattern: "foo."},
			want: []string{"6:foo bar"},
		},
		{
			name: "ignore_case expands matches",
			opts: GrepOptions{Pattern: "alpha", IgnoreCase: true},
			want: []string{"1:alpha", "2:Alpha"},
		},
		{
			name: "case_sensitive overrides ignore_case",
			opts: GrepOptions{Pattern: "alpha", IgnoreCase: true, CaseSensitive: true},
			want: []string{"1:alpha"},
		},
		{
			name: "smart_case ignores case for lowercase pattern",
			opts: GrepOptions{Pattern: "alpha", SmartCase: true},
			want: []string{"1:alpha", "2:Alpha"},
		},
		{
			name: "smart_case stays sensitive for uppercase pattern",
			opts: GrepOptions{Pattern: "Alpha", SmartCase: true},
			want: []string{"2:Alpha"},
		},
		{
			name: "word skips partial matches",
			opts: GrepOptions{Pattern: "err", Word: true},
			want: []string{"5:err"},
		},
		{
			name: "line_regexp requires the whole line",
			opts: GrepOptions{Pattern: "foo", LineRegexp: true},
			want: []string{"7:foo"},
		},
		{
			name: "patterns join with OR",
			opts: GrepOptions{Patterns: []string{"alpha", "err"}},
			want: []string{"1:alpha", "4:error", "5:err"},
		},
		{
			name: "patterns honor ignore_case together",
			opts: GrepOptions{Patterns: []string{"ALPHA", "ERR"}, IgnoreCase: true},
			want: []string{"1:alpha", "2:Alpha", "4:error", "5:err"},
		},
		{
			name: "invert_match keeps non-matching lines",
			opts: GrepOptions{Pattern: "alpha", InvertMatch: true},
			want: []string{"2:Alpha", "3:A.B", "4:error", "5:err", "6:foo bar", "7:foo"},
		},
		{
			name: "case_insensitive alias behaves like ignore_case",
			opts: GrepOptions{Pattern: "alpha", CaseInsensitive: true},
			want: []string{"1:alpha", "2:Alpha"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assertLines(t, grepFixtureLines(t, file, tc.opts), tc.want)
		})
	}
}

func TestGrepPatternCompileErrors(t *testing.T) {
	if _, err := CompileGrepPattern(nil, GrepPatternOptions{}); err == nil {
		t.Fatal("expected an error for an empty pattern list")
	}
	if _, err := CompileGrepPattern([]string{"(?<=foo)"}, GrepPatternOptions{}); err == nil {
		t.Fatal("expected an error for a pattern Go RE2 cannot compile")
	}
	if _, err := CompileGrepPattern([]string{"a.b"}, GrepPatternOptions{Literal: true}); err != nil {
		t.Fatalf("literal pattern must compile: %v", err)
	}
}

func TestEffectiveIgnoreCasePrecedence(t *testing.T) {
	cases := []struct {
		name     string
		patterns []string
		opts     GrepPatternOptions
		want     bool
	}{
		{"default sensitive", []string{"alpha"}, GrepPatternOptions{}, false},
		{"ignore_case wins by default", []string{"alpha"}, GrepPatternOptions{IgnoreCase: true}, true},
		{"case_sensitive beats ignore_case", []string{"alpha"}, GrepPatternOptions{IgnoreCase: true, CaseSensitive: true}, false},
		{"smart_case lower", []string{"alpha"}, GrepPatternOptions{SmartCase: true}, true},
		{"smart_case upper", []string{"Alpha"}, GrepPatternOptions{SmartCase: true}, false},
		{"smart_case mixed list stays sensitive", []string{"alpha", "Beta"}, GrepPatternOptions{SmartCase: true}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := EffectiveIgnoreCase(tc.patterns, tc.opts); got != tc.want {
				t.Fatalf("EffectiveIgnoreCase(%q, %+v) = %v, want %v", tc.patterns, tc.opts, got, tc.want)
			}
		})
	}
}

func TestGrepPatternsFromArgsPrecedence(t *testing.T) {
	cases := []struct {
		name string
		args map[string]any
		want []string
	}{
		{"pattern only", map[string]any{"pattern": "alpha"}, []string{"alpha"}},
		{"regexp alias", map[string]any{"regexp": "beta"}, []string{"beta"}},
		{"pattern before patterns", map[string]any{"pattern": "alpha", "patterns": []any{"beta"}},
			[]string{"alpha", "beta"}},
		{"array patterns", map[string]any{"patterns": []any{"a", "b"}}, []string{"a", "b"}},
		{"string patterns split", map[string]any{"patterns": "a,b"}, []string{"a", "b"}},
		{"blank entries dropped", map[string]any{"pattern": "  ", "patterns": " "}, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := GrepPatternsFromArgs(tc.args)
			if len(got) != len(tc.want) {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
			for i := range tc.want {
				if got[i] != tc.want[i] {
					t.Fatalf("got %q, want %q", got, tc.want)
				}
			}
		})
	}
}
