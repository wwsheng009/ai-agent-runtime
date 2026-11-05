package searchcore

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/internal/toolcore"
)

// countingSearcher wraps fakeSearcher so a test can tell, from the page alone,
// which engine answered: ripgrep's canned match says so in its text.
func TestGrepRipgrepAnswersFirstWithoutRunningTheWalker(t *testing.T) {
	root := t.TempDir()
	// The file the walker would find. If the walker runs, its match for this
	// text is what the caller sees; if ripgrep answers, the canned text wins.
	if err := os.WriteFile(filepath.Join(root, "walker.txt"), []byte("needle\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	searcher := &fakeSearcher{available: true}
	result := runGrepWithSearcher(t, root, searcher, map[string]any{"pattern": "needle"})
	if !result.Success {
		t.Fatalf("grep must succeed, got %s", result.Content)
	}
	if len(searcher.calls) != 1 {
		t.Fatalf("ripgrep must be asked exactly once, got %d calls", len(searcher.calls))
	}
	// The walker's match must not appear: it must not have run at all.
	if got := result.Content; !contains(got, "answered by ripgrep") {
		t.Fatalf("expected the ripgrep answer, got %q", got)
	}
	if contains(result.Content, "walker.txt") {
		t.Fatalf("the walker ran even though ripgrep answered: %q", result.Content)
	}
	if result.Metadata["engine"] != "ripgrep" {
		t.Fatalf("engine = %#v, want ripgrep", result.Metadata["engine"])
	}
}

// TestGrepRipgrepErrorKeepsTheWalkerPage pins the fallback contract: when the
// engine was asked and failed, the walker's page stays and the call still
// succeeds. Reversing the order must not turn an rg failure into a hard error.
func TestGrepRipgrepErrorKeepsTheWalkerPage(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("needle\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	searcher := &erroringSearcher{}
	result := runGrepWithSearcher(t, root, searcher, map[string]any{"pattern": "needle"})
	if !result.Success {
		t.Fatalf("the walker page must survive an engine failure, got %s", result.Content)
	}
	if !contains(result.Content, "a.txt") {
		t.Fatalf("expected the walker's match, got %q", result.Content)
	}
	if result.Metadata["engine"] != "walker" {
		t.Fatalf("engine = %#v, want walker", result.Metadata["engine"])
	}
}

// TestGrepRipgrepDeclineRunsTheWalker is the no-ripgrep host: the engine
// declines, and the walker answers as it always did.
func TestGrepRipgrepDeclineRunsTheWalker(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("needle\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	searcher := &fakeSearcher{available: false}
	result := runGrepWithSearcher(t, root, searcher, map[string]any{"pattern": "needle"})
	if !result.Success {
		t.Fatalf("the walker must answer when the engine declines, got %s", result.Content)
	}
	if !contains(result.Content, "a.txt") {
		t.Fatalf("expected the walker's match, got %q", result.Content)
	}
	if result.Metadata["engine"] != "walker" {
		t.Fatalf("engine = %#v, want walker", result.Metadata["engine"])
	}
}

// TestGrepWalkerBudgetBoundsTheSearch is the budget regression: a search whose
// budget expires must return what it collected, marked as a partial page,
// instead of running the whole tree with no deadline.
func TestGrepWalkerBudgetBoundsTheSearch(t *testing.T) {
	root := t.TempDir()
	// Enough files that the walker cannot finish before the budget expires.
	// Directory names stay alphanumeric: Windows rejects the punctuation the
	// arithmetic here can produce.
	const dirs, filesPerDir = 20, 20
	for d := 0; d < dirs; d++ {
		dir := filepath.Join(root, fmt.Sprintf("d%02d", d))
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		for f := 0; f < filesPerDir; f++ {
			path := filepath.Join(dir, fmt.Sprintf("f%02d.txt", f))
			if err := os.WriteFile(path, []byte("needle\n"), 0o600); err != nil {
				t.Fatal(err)
			}
		}
	}

	// Use the visit-count budget rather than a 1ms wall-clock budget: a fast
	// filesystem can finish this tree before the timer fires, while a slow CI
	// runner can expire before the first file. Both produce environment-
	// dependent flakes. The visit-count stand-in exercises the same expiry
	// branch deterministically and is restored for other tests.
	originalVisits := GrepBudgetAfterVisits
	GrepBudgetAfterVisits = 10
	defer func() { GrepBudgetAfterVisits = originalVisits }()

	result, err := Grep(context.Background(), GrepOptions{Pattern: "needle", Path: root, Limit: 10})
	if err != nil {
		t.Fatalf("an expired budget must not fail the call: %v", err)
	}
	if !result.TimedOut {
		t.Fatal("a search that ran past its budget must be marked TimedOut")
	}
	// Whatever it did collect is kept: the page is the accumulated result, not
	// an error, and it never exceeds the page size the caller asked for. It may
	// legitimately be empty - a budget that expires on the first file collected
	// nothing - so only the bound is asserted here.
	if len(result.Matches) > 10 {
		t.Fatalf("a partial page must respect the requested limit, got %d", len(result.Matches))
	}
}

// TestGrepTimedOutKeepsCollectedMatches proves a partial page is real evidence:
// once the walk has collected something, expiry keeps it rather than throwing
// the work away with the rest of the tree.
func TestGrepTimedOutKeepsCollectedMatches(t *testing.T) {
	root := t.TempDir()
	// A wide, shallow tree: the walk collects many matches quickly, then keeps
	// going. The budget stops it partway, so a non-empty partial page is the
	// only outcome consistent with keeping what was already found.
	const dirs = 200
	for d := 0; d < dirs; d++ {
		dir := filepath.Join(root, fmt.Sprintf("d%03d", d))
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "f.txt"), []byte("needle\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	// Stop after 50 of the 200 files. The visit-count stand-in makes this
	// deterministic; the clock budget is racy on a machine of unknown speed.
	originalVisits := GrepBudgetAfterVisits
	GrepBudgetAfterVisits = 50
	defer func() { GrepBudgetAfterVisits = originalVisits }()

	result, err := Grep(context.Background(), GrepOptions{
		Pattern: "needle",
		Path:    root,
		// No page cap: the point is what the walker itself retains, so the
		// window must not cut the result first.
		Limit: GrepUnlimitedPageSize,
	})
	if err != nil {
		t.Fatalf("an expired budget must not fail the call: %v", err)
	}
	if !result.TimedOut {
		t.Fatal("a walk stopped by its budget must be marked TimedOut")
	}
	if len(result.Matches) == 0 {
		t.Fatal("a timed-out walk must keep the matches it had already collected")
	}
	// 50 files were read before the budget ended the walk; the 51st visit is
	// where it stopped, so the page is exactly the first 50 - never all 200.
	if len(result.Matches) > 50 {
		t.Fatalf("expected the walk to stop after 50 files, got %d", len(result.Matches))
	}
}

// TestGrepParentCancellationStillErrors pins the other half of the budget: a
// caller that cancels the search must be told the walk stopped, not handed a
// partial page that reads like a complete answer.
func TestGrepParentCancellationStillErrors(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("needle\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Grep(ctx, GrepOptions{Pattern: "needle", Path: root, Limit: 10}); err == nil {
		t.Fatal("a cancelled parent must still be reported as an error")
	}
}

// TestGrepTimedOutIsReportedInMetadata checks the budget is visible to the
// caller, so a partial page reads as one rather than as a complete answer.
func TestGrepTimedOutIsReportedInMetadata(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("needle\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	result := runGrepWithSearcher(t, root, &fakeSearcher{available: false}, map[string]any{"pattern": "needle"})
	if _, ok := result.Metadata["timed_out"]; !ok {
		t.Fatalf("metadata must report timed_out, got keys %v", metadataKeys(result.Metadata))
	}
}

// outsideWorkspaceResolver models the real localCoreRuntime: a path outside the
// workspace root is answered absolutely, because a relative spelling of it
// would not resolve from the workspace.
type outsideWorkspaceResolver struct{ root string }

func (p outsideWorkspaceResolver) Root() string { return p.root }
func (p outsideWorkspaceResolver) Resolve(raw string) (string, error) {
	if strings.TrimSpace(raw) == "" {
		return p.root, nil
	}
	if filepath.IsAbs(raw) {
		return raw, nil
	}
	return filepath.Join(p.root, raw), nil
}
func (p outsideWorkspaceResolver) Relative(abs string) string {
	rel, err := filepath.Rel(p.root, abs)
	if err != nil || strings.HasPrefix(rel, "..") {
		return abs
	}
	return filepath.ToSlash(rel)
}

// TestGrepMultiRootOutsideWorkspaceDoesNotDoublePrefix is the regression for
// the corrupted file column: two search roots that both live outside the
// workspace are answered absolutely, so joining the root label with the file's
// own absolute path produced `C:/root/C:/root/a.txt` - a path that resolves
// nowhere, and one no caller can open or read back.
func TestGrepMultiRootOutsideWorkspaceDoesNotDoublePrefix(t *testing.T) {
	workspace := t.TempDir()
	rootA := t.TempDir()
	rootB := t.TempDir()
	if err := os.WriteFile(filepath.Join(rootA, "a.txt"), []byte("needle\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(rootB, "b.txt"), []byte("needle\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	// The walker, not the engine: this is the render path every engine's
	// result funnels through.
	result, err := Grep(context.Background(), GrepOptions{
		Patterns: []string{"needle"},
		Path:     rootA,
		Paths:    []string{rootA, rootB},
		Mode:     GrepModeFiles,
	})
	if err != nil {
		t.Fatal(err)
	}

	handler := NewGrepHandler(toolcore.Definition{Name: "grep", Version: "1.0.0"})
	rendered, err := handler.Execute(context.Background(), &toolcore.Call{
		Args: map[string]any{"pattern": "needle", "paths": []any{rootA, rootB}, "mode": "files"},
		Runtime: searcherRuntime{
			presentationRuntime: presentationRuntime{paths: outsideWorkspaceResolver{root: workspace}},
			searcher:            &fakeSearcher{available: false},
		},
	})
	if err != nil || rendered == nil {
		t.Fatalf("grep failed: err=%v result=%+v", err, rendered)
	}
	if !rendered.Success {
		t.Fatalf("expected success, got %s", rendered.Content)
	}

	for _, want := range []string{
		filepath.ToSlash(filepath.Join(rootA, "a.txt")),
		filepath.ToSlash(filepath.Join(rootB, "b.txt")),
	} {
		if !strings.Contains(rendered.Content, want) {
			t.Fatalf("expected %q in output, got %q", want, rendered.Content)
		}
	}
	// The corruption signature: a root label glued to an absolute file path.
	for _, bad := range []string{
		filepath.ToSlash(rootA) + "/" + filepath.ToSlash(filepath.Join(rootA, "a.txt")),
		filepath.ToSlash(rootB) + "/" + filepath.ToSlash(filepath.Join(rootB, "b.txt")),
	} {
		if strings.Contains(rendered.Content, bad) {
			t.Fatalf("output must not contain the double-prefixed path %q, got %q", bad, rendered.Content)
		}
	}
	_ = result
}

func metadataKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}

// erroringSearcher models an engine that was asked and failed. The handler must
// keep the walker's page instead of failing the call.
type erroringSearcher struct{}

func (erroringSearcher) Glob(context.Context, GlobOptions) (GlobResult, bool, error) {
	return GlobResult{}, false, nil
}

func (erroringSearcher) Grep(context.Context, GrepOptions) (GrepResult, bool, error) {
	return GrepResult{}, true, context.DeadlineExceeded
}

var _ toolcore.Runtime = searcherRuntime{}
