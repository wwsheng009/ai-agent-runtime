package searchcore

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// followFixture builds the layout the flag exists for: the only file that
// matches lives OUTSIDE the searched root, reachable through a directory link
// and through a file link.
func followFixture(t *testing.T) (root string, outsideNote string) {
	t.Helper()
	base := t.TempDir()
	outside := filepath.Join(base, "outside")
	root = filepath.Join(base, "root")
	for _, dir := range []string{outside, root} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	outsideNote = filepath.Join(outside, "note.txt")
	if err := os.WriteFile(outsideNote, []byte("needle\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "plain.txt"), []byte("hay\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "linkdir")); err != nil {
		t.Skipf("this host cannot create symlinks (privileges?): %v", err)
	}
	if err := os.Symlink(outsideNote, filepath.Join(root, "linkfile.txt")); err != nil {
		t.Skipf("this host cannot create symlinks (privileges?): %v", err)
	}
	return root, outsideNote
}

// TestWalkerFollowsLinksOnlyWhenAsked: the pure-Go engine must follow links
// itself, because otherwise `follow` would be the one thing only a host with rg
// could do. Without the flag nothing outside the root is read - the sandbox rule
// the walk has always applied.
func TestWalkerFollowsLinksOnlyWhenAsked(t *testing.T) {
	root, _ := followFixture(t)

	plain, err := Grep(context.Background(), GrepOptions{Pattern: "needle", Path: root, Limit: 10})
	if err != nil {
		t.Fatalf("walker grep failed: %v", err)
	}
	if len(plain.Matches) != 0 {
		t.Fatalf("links must not be followed by default, got %+v", plain.Matches)
	}

	followed, err := Grep(context.Background(), GrepOptions{Pattern: "needle", Path: root, Limit: 10, Follow: true})
	if err != nil {
		t.Fatalf("walker grep failed: %v", err)
	}
	files := make([]string, 0, len(followed.Matches))
	for _, match := range followed.Matches {
		files = append(files, match.File)
	}
	// Both entries are reached under the link's own path, which is what rg prints.
	for _, want := range []string{
		filepath.Join(root, "linkdir", "note.txt"),
		filepath.Join(root, "linkfile.txt"),
	} {
		if !containsGrepFile(files, want) {
			t.Fatalf("follow must reach %s, got %v", want, files)
		}
	}
}

func containsGrepFile(files []string, want string) bool {
	for _, file := range files {
		if file == want {
			return true
		}
	}
	return false
}

// TestWalkerFollowTerminatesOnLinkCycles: a link back to the root is a cycle,
// and the walk has to end instead of growing the path forever. The file is
// visited once, not once per loop iteration.
func TestWalkerFollowTerminatesOnLinkCycles(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("needle\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(root, filepath.Join(root, "loop")); err != nil {
		t.Skipf("this host cannot create symlinks (privileges?): %v", err)
	}

	result, err := Grep(context.Background(), GrepOptions{Pattern: "needle", Path: root, Limit: 10, Follow: true})
	if err != nil {
		t.Fatalf("a link cycle must terminate, got %v", err)
	}
	if len(result.Matches) != 1 {
		t.Fatalf("the file must be visited once, got %d matches: %+v", len(result.Matches), result.Matches)
	}
}

// TestWalkerFollowVisitsADirectoryOnce: two links to one directory are searched
// once. This is deliberately stronger than `rg --follow` (which prints both
// paths) and is what makes mutual links terminate - the rule is stated in the
// tool's schema so a caller can rely on it.
func TestWalkerFollowVisitsADirectoryOnce(t *testing.T) {
	base := t.TempDir()
	target := filepath.Join(base, "target")
	root := filepath.Join(base, "root")
	for _, dir := range []string{target, root} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(target, "note.txt"), []byte("needle\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, link := range []string{"first", "second"} {
		if err := os.Symlink(target, filepath.Join(root, link)); err != nil {
			t.Skipf("this host cannot create symlinks (privileges?): %v", err)
		}
	}

	result, err := Grep(context.Background(), GrepOptions{Pattern: "needle", Path: root, Limit: 10, Follow: true})
	if err != nil {
		t.Fatalf("walker grep failed: %v", err)
	}
	if len(result.Matches) != 1 {
		t.Fatalf("one target must be searched once, got %d matches: %+v", len(result.Matches), result.Matches)
	}
}

// TestRipgrepFollowAsksForTheFlag pins the engine half of the same capability.
func TestRipgrepFollowAsksForTheFlag(t *testing.T) {
	root, _ := followFixture(t)

	runs := captureRipgrep(t, root, func(engine Ripgrep) (GrepResult, bool, error) {
		return engine.Grep(context.Background(), GrepOptions{
			Pattern: "needle", Path: root, Limit: 10, Follow: true,
		})
	})
	if len(runs) == 0 {
		t.Fatal("the engine never ran")
	}
	if joined := strings.Join(runs[0].args, " "); !strings.Contains(joined, "--follow") {
		t.Fatalf("ripgrep was not asked to follow links:\n%s", joined)
	}
}

// TestGrepFollowReachesTheEngineAndTheMetadata walks the whole seam: the request,
// the options the engine receives, and the metadata the call reports.
func TestGrepFollowReachesTheEngineAndTheMetadata(t *testing.T) {
	root, _ := followFixture(t)
	searcher := &fakeSearcher{available: true}

	result := runGrepWithSearcher(t, root, searcher, map[string]any{"pattern": "needle", "follow": true})
	if result.Metadata["follow"] != true {
		t.Fatalf("metadata.follow = %#v, want true", result.Metadata["follow"])
	}
	if len(searcher.calls) == 0 {
		t.Fatal("the engine was never asked")
	}
	if !searcher.calls[0].Follow {
		t.Fatalf("the engine received Follow=false: %+v", searcher.calls[0])
	}
}
