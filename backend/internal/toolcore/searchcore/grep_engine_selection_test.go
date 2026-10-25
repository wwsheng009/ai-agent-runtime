package searchcore

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/internal/toolcore"
)

// fakeSearcher stands in for a host that has ripgrep: it records what it was
// asked for and answers with one distinguishable match, so a test can tell which
// engine produced the page.
type fakeSearcher struct {
	calls []GrepOptions
	// available=false models a host without ripgrep: the searcher declines, and
	// the handler has to fall back the way the builtin does.
	available bool
}

// Glob completes the Searcher contract. These tests never ask for it, so it
// reports "not me" and leaves the call to the shared walker.
func (s *fakeSearcher) Glob(context.Context, GlobOptions) (GlobResult, bool, error) {
	return GlobResult{}, false, nil
}

type searcherRuntime struct {
	presentationRuntime
	searcher Searcher
}

func (r searcherRuntime) Grep(ctx context.Context, opts GrepOptions) (GrepResult, bool, error) {
	return r.searcher.Grep(ctx, opts)
}

func (r searcherRuntime) Glob(ctx context.Context, opts GlobOptions) (GlobResult, bool, error) {
	return r.searcher.Glob(ctx, opts)
}

func (s *fakeSearcher) Grep(_ context.Context, opts GrepOptions) (GrepResult, bool, error) {
	s.calls = append(s.calls, opts)
	if !s.available {
		return GrepResult{}, false, nil
	}
	return GrepResult{
		Matches: []GrepMatch{{File: "engine.txt", Line: 1, Text: "answered by ripgrep", Column: 1}},
		Mode:    GrepModeContent,
	}, true, nil
}

// GrepOptions also implements the Searcher contract for this fake.
func runGrepWithSearcher(t *testing.T, root string, searcher Searcher, args map[string]any) *toolcore.Result {
	t.Helper()
	handler := NewGrepHandler(toolcore.Definition{Name: "grep", Version: "1.0.0"})
	result, err := handler.Execute(context.Background(), &toolcore.Call{
		Args:    args,
		Runtime: searcherRuntime{presentationRuntime: presentationRuntime{paths: presentationPaths{root: root}}, searcher: searcher},
	})
	if err != nil || result == nil {
		t.Fatalf("grep failed: err=%v result=%+v", err, result)
	}
	return result
}

// TestGrepEngineSelectionReachesRipgrep is the dispatch contract for
// ripgrep-only capabilities: the engine is asked first and receives the flags,
// rather than the pattern being handed to Go's regexp - which rejects exactly
// the patterns PCRE2 exists for.
func TestGrepEngineSelectionReachesRipgrep(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("needle\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name string
		args map[string]any
		want func(opts GrepOptions) bool
	}{
		{
			name: "pcre2",
			args: map[string]any{"pattern": "foo(?!bar)", "pcre2": true},
			want: func(o GrepOptions) bool { return o.Pcre2 && o.RequiresRipgrep() },
		},
		{
			name: "engine value",
			args: map[string]any{"pattern": "needle", "engine": "pcre2"},
			want: func(o GrepOptions) bool { return o.Engine == "pcre2" && o.RequiresRipgrep() },
		},
		{
			name: "default engine is not a ripgrep request",
			args: map[string]any{"pattern": "needle", "engine": "default"},
			want: func(o GrepOptions) bool { return !o.RequiresRipgrep() },
		},
		{
			name: "multiline",
			args: map[string]any{"pattern": "a\\nb", "multiline": true},
			want: func(o GrepOptions) bool { return o.Multiline && o.RequiresRipgrep() },
		},
		{
			name: "multiline dotall",
			args: map[string]any{"pattern": "a.b", "multiline_dotall": true},
			want: func(o GrepOptions) bool { return o.MultilineDotall && o.RequiresRipgrep() },
		},
		{
			name: "auto hybrid regex",
			args: map[string]any{"pattern": "needle", "auto_hybrid_regex": true},
			want: func(o GrepOptions) bool { return o.AutoHybridRegex && o.RequiresRipgrep() },
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			searcher := &fakeSearcher{available: true}
			result := runGrepWithSearcher(t, root, searcher, tc.args)
			if len(searcher.calls) == 0 {
				t.Fatalf("the engine was never asked for %v", tc.args)
			}
			if !tc.want(searcher.calls[0]) {
				t.Fatalf("the engine was asked with %+v", searcher.calls[0])
			}
			if result.Metadata["engine"] != "ripgrep" {
				t.Fatalf("engine metadata = %v, want ripgrep", result.Metadata["engine"])
			}
		})
	}
}

// TestGrepPcrePatternReachesTheEngineFirst: with ripgrep available, a pattern
// Go's regexp cannot compile must still be answered - that is the whole point of
// the flag. Without this the walker would fail first and the engine never ran.
func TestGrepPcrePatternReachesTheEngineFirst(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("needle\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	searcher := &fakeSearcher{available: true}
	result := runGrepWithSearcher(t, root, searcher, map[string]any{
		"pattern": "foo(?!bar)", // lookaround: valid PCRE2, invalid Go regexp
		"pcre2":   true,
	})
	if !result.Success {
		t.Fatalf("a PCRE pattern must be served by the engine, got %s", result.Content)
	}
	if len(searcher.calls) != 1 {
		t.Fatalf("the engine must be asked exactly once, got %d calls", len(searcher.calls))
	}
}

// TestGrepEngineSelectionFallsBackToTheWalker: a host without ripgrep keeps the
// builtin behavior - the walker answers, exactly as it does locally when no rg
// is on PATH. The flag is a request for a capability, not a hard failure.
func TestGrepEngineSelectionFallsBackToTheWalker(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("needle\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	searcher := &fakeSearcher{available: false}
	result := runGrepWithSearcher(t, root, searcher, map[string]any{
		"pattern": "needle",
		"pcre2":   true,
	})
	if !result.Success {
		t.Fatalf("the walker must answer when the engine declines, got %s", result.Content)
	}
	if result.Metadata["engine"] != "walker" {
		t.Fatalf("engine metadata = %v, want walker", result.Metadata["engine"])
	}
}

// TestGrepRequiresRipgrepIsDerivedFromTheFlags keeps one source of truth: the
// dispatch decision is computed from the flags, not tracked alongside them.
func TestGrepRequiresRipgrepIsDerivedFromTheFlags(t *testing.T) {
	cases := []struct {
		name string
		opts GrepOptions
		want bool
	}{
		{"plain", GrepOptions{}, false},
		{"engine default", GrepOptions{Engine: "default"}, false},
		{"engine unset", GrepOptions{Engine: "  "}, false},
		{"engine pcre2", GrepOptions{Engine: "pcre2"}, true},
		{"pcre2", GrepOptions{Pcre2: true}, true},
		{"multiline", GrepOptions{Multiline: true}, true},
		{"multiline dotall", GrepOptions{MultilineDotall: true}, true},
		{"auto hybrid", GrepOptions{AutoHybridRegex: true}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.opts.RequiresRipgrep(); got != tc.want {
				t.Fatalf("RequiresRipgrep() = %v, want %v", got, tc.want)
			}
		})
	}
}
