package searchcore

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// capturedRun is one ripgrep invocation as the engine asked for it.
type capturedRun struct {
	args []string
	dir  string
}

// captureRipgrep runs fn against an engine whose stream records every
// invocation instead of starting a process.
func captureRipgrep(t *testing.T, path string, fn func(engine Ripgrep) (GrepResult, bool, error)) []capturedRun {
	t.Helper()
	runs := make([]capturedRun, 0, 2)
	engine := Ripgrep{
		Path: "rg",
		Stream: func(_ context.Context, args []string, dir string, _ func(line []byte) bool) error {
			runs = append(runs, capturedRun{args: append([]string(nil), args...), dir: dir})
			return nil
		},
	}
	if _, _, err := fn(engine); err != nil {
		t.Fatalf("engine run failed: %v", err)
	}
	_ = path
	return runs
}

// TestRipgrepBaseArgsAreTheDeterminismContract pins the three flags every
// invocation carries, because the remaining ports keep running into them:
// --no-config makes a search independent of the user's ripgrep config,
// --threads 1 keeps ordering stable, and --sort path makes the engine's page
// compose like the walker's root-by-root walk. A user-chosen sort key replaces
// the --sort pair but keeps the other two flags and the root-by-root process
// model; a raw passthrough that needs one process for several roots would give
// one of them up, which is why the base stays asserted here.
func TestRipgrepBaseArgsAreTheDeterminismContract(t *testing.T) {
	want := []string{"--no-config", "--threads", "1", "--sort", "path"}
	if got := ripgrepBaseArgs(); !reflect.DeepEqual(got, want) {
		t.Fatalf("ripgrepBaseArgs() = %v, want %v", got, want)
	}
}

// TestRipgrepRunsOneProcessPerRoot pins the second half of the same contract:
// ripgrep's --sort is global, so one process over several roots would order
// results differently from the walker's root-by-root walk.
func TestRipgrepRunsOneProcessPerRoot(t *testing.T) {
	root := t.TempDir()
	first := filepath.Join(root, "first")
	second := filepath.Join(root, "second")
	for _, dir := range []string{first, second} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}

	runs := captureRipgrep(t, first, func(engine Ripgrep) (GrepResult, bool, error) {
		return engine.Grep(context.Background(), GrepOptions{
			Pattern: "needle", Path: first, Paths: []string{second}, Limit: 10,
		})
	})
	if len(runs) != 2 {
		t.Fatalf("engine started %d processes for two roots, want one each", len(runs))
	}
	for i, run := range runs {
		if len(run.args) < len(ripgrepBaseArgs()) {
			t.Fatalf("run %d has too few args: %v", i, run.args)
		}
		if !reflect.DeepEqual(run.args[:len(ripgrepBaseArgs())], ripgrepBaseArgs()) {
			t.Fatalf("run %d does not start with the base args: %v", i, run.args)
		}
	}
}

// TestRipgrepEngineFlagsReachArgv closes a gap: until now the engine-selection
// flags were only tested as far as "the searcher received the options", never as
// far as "ripgrep was asked for them".
func TestRipgrepEngineFlagsReachArgv(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("needle\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	runs := captureRipgrep(t, root, func(engine Ripgrep) (GrepResult, bool, error) {
		return engine.Grep(context.Background(), GrepOptions{
			Pattern:         "needle",
			Path:            root,
			Limit:           10,
			Pcre2:           true,
			Engine:          "pcre2",
			AutoHybridRegex: true,
			Multiline:       true,
			MultilineDotall: true,
			CRLF:            true,
		})
	})
	if len(runs) == 0 {
		t.Fatal("the engine never ran")
	}
	joined := strings.Join(runs[0].args, " ")
	for _, want := range []string{
		"--pcre2", "--engine pcre2", "--auto-hybrid-regex",
		"--multiline", "--multiline-dotall", "--crlf",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("ripgrep was not asked for %q:\n%s", want, joined)
		}
	}
	// The engine still owns the parse: its structured contract depends on the
	// JSON event stream, which is why a raw-passthrough mode cannot simply be a
	// flag on this path.
	if !strings.Contains(joined, "--json") {
		t.Fatalf("the engine must keep asking for the JSON event stream:\n%s", joined)
	}
}
