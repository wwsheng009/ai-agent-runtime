package lscore

// The differential oracle. ReadTreeVia claims to be the same listing that
// filepath.WalkDir produced before the seam landed, and a claim about equivalence
// over *shapes* cannot rest on three hand-picked cases. So the pre-move
// implementation is pinned here, verbatim, and both versions are run over the same
// real directory tree for every depth and limit combination.
//
// The expectation is computed by the old code over the same files, not written by
// me: if the two ever disagree on which rows survive a limit cut, this says so.

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/internal/toolcore"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolcore/localio"
)

// referenceReadTree is the implementation that lived in ls.go before Phase 2.5b.
// It is deliberately a copy rather than a call, so that a future edit to the seam
// version cannot quietly move the goal posts here too.
func referenceReadTree(path string, maxDepth, limit int) (TreeListing, error) {
	if maxDepth < 1 {
		maxDepth = 1
	}
	if maxDepth > MaxDepth {
		maxDepth = MaxDepth
	}
	if limit < 1 {
		limit = DefaultTreeLimit
	}

	listing := TreeListing{}
	walkErr := filepath.WalkDir(path, func(walkPath string, entry fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		relPath, relErr := filepath.Rel(path, walkPath)
		if relErr != nil {
			relPath = walkPath
		}
		if relPath == "." {
			return nil
		}
		normalized := filepath.ToSlash(relPath)
		depth := strings.Count(normalized, "/")
		if depth >= maxDepth {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		item := TreeEntry{RelPath: normalized, Depth: depth, Dir: entry.IsDir()}
		listing.Entries = append(listing.Entries, item)
		if item.Dir {
			listing.Dirs++
		} else {
			listing.Files++
		}
		if len(listing.Entries) >= limit {
			listing.Truncated = true
			listing.LimitHit = true
			return filepath.SkipAll
		}
		return nil
	})
	if walkErr != nil {
		return TreeListing{}, walkErr
	}
	sortTreeEntries(&listing)
	listing.Total = len(listing.Entries)
	return listing, nil
}

func sortTreeEntries(listing *TreeListing) {
	sort.Slice(listing.Entries, func(i, j int) bool {
		if listing.Entries[i].RelPath == listing.Entries[j].RelPath {
			if listing.Entries[i].Dir == listing.Entries[j].Dir {
				return listing.Entries[i].Depth < listing.Entries[j].Depth
			}
			return listing.Entries[i].Dir && !listing.Entries[j].Dir
		}
		return listing.Entries[i].RelPath < listing.Entries[j].RelPath
	})
}

// buildDifferentialTree is the sample set: mixed files and directories at every
// level, names chosen so lexical order differs from both insertion order and the
// final sorted shape (a directory whose name sorts between two files, a directory
// sorting after its own contents), plus a file root and a missing root.
func buildDifferentialTree(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	mkdir := func(rel string) string {
		path := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(path, 0o755); err != nil {
			t.Fatal(err)
		}
		return path
	}
	touch := func(rel string) {
		path := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for _, dir := range []string{"m", "m/deep", "m/deep/deeper", "b", "z", "a/a/a"} {
		mkdir(dir)
	}
	for _, file := range []string{
		"a.txt", "b.txt", "m/b.txt", "m/deep/c.txt", "m/deep/deeper/d.txt",
		"m/deep/z.txt", "b/inner.txt", "z/last.txt", "a/a/a/deep.txt", "A-Caps.txt",
	} {
		touch(file)
	}
	// A symlinked directory inside the tree: the walk must report the link and not
	// follow it. Skipped where the platform will not let an ordinary user link.
	if err := os.Symlink(filepath.Join(root, "m"), filepath.Join(root, "link-to-m")); err != nil {
		t.Logf("symlinks unavailable on this platform: %v", err)
	}
	// A dangling link, which is where Stat and Lstat disagree.
	if err := os.Symlink(filepath.Join(root, "nowhere"), filepath.Join(root, "dangling")); err != nil {
		t.Logf("symlinks unavailable on this platform: %v", err)
	}
	return root
}

func TestReadTreeViaMatchesTheWalkDirReference(t *testing.T) {
	root := buildDifferentialTree(t)

	type testCase struct {
		name     string
		path     string
		maxDepth int
		limit    int
		// diverges marks the cases where the seam answer is deliberately different
		// from the pre-move answer; every other case must agree exactly.
		diverges bool
	}
	cases := []testCase{{name: "empty dir", path: filepath.Join(root, "empty"), maxDepth: 3, limit: 100}}
	for _, depth := range []int{0, 1, 2, 3, 4, 10, 11, 99} {
		for _, limit := range []int{0, 1, 2, 3, 5, 7, 11, 100, DefaultTreeLimit} {
			cases = append(cases, testCase{name: fmt.Sprintf("depth %d limit %d", depth, limit), path: root, maxDepth: depth, limit: limit})
		}
	}
	// The root-shaped cases the old walk answered without an error.
	cases = append(cases,
		testCase{name: "file root", path: filepath.Join(root, "a.txt"), maxDepth: 2, limit: 10},
		testCase{name: "missing root", path: filepath.Join(root, "no-such-dir"), maxDepth: 2, limit: 10, diverges: true},
	)

	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			if test.name == "empty dir" {
				if err := os.MkdirAll(test.path, 0o755); err != nil {
					t.Fatal(err)
				}
			}
			want, wantErr := referenceReadTree(test.path, test.maxDepth, test.limit)
			got, gotErr := ReadTreeVia(context.Background(), localio.Access, test.path, test.maxDepth, test.limit)
			if test.diverges {
				// The one place this move is deliberately NOT a move, and the reason
				// belongs in the test rather than in a comment nobody reads:
				// filepath.WalkDir handed the root's error to the callback, the old
				// callback swallowed it, and the walk returned an empty listing with a
				// nil error. That made `if walkErr != nil` dead code. A seam cannot
				// inherit the swallow, because an accessor whose transport fails (a
				// network error, an RPC 500) would otherwise be reported as "this
				// directory is empty" - a false answer, not a refusal.
				//
				// ls itself still cannot observe the difference: its handler stats the
				// path first and turns that into NOT_FOUND, which the seam test
				// TestExistenceCheckComesFromAccessor pins.
				if wantErr != nil {
					t.Fatalf("the reference was expected to swallow the root error, got %v", wantErr)
				}
				if gotErr == nil {
					t.Fatal("the seam swallowed a missing root into an empty listing")
				}
				if !toolcore.IsNotExist(gotErr) {
					t.Fatalf("a missing root must classify as absence, got %v", gotErr)
				}
				if len(got.Entries) != 0 || got.Total != 0 {
					t.Fatalf("a failed walk must not hand back partial rows: %v", got.Entries)
				}
				return
			}
			if (wantErr == nil) != (gotErr == nil) {
				t.Fatalf("error presence differs: reference=%v seam=%v", wantErr, gotErr)
			}
			if wantErr != nil {
				if toolcore.IsNotExist(wantErr) != toolcore.IsNotExist(gotErr) {
					t.Fatalf("error kinds differ on absence: reference=%v seam=%v", wantErr, gotErr)
				}
				return
			}
			if renderListing(want) != renderListing(got) {
				t.Fatalf("listing differs\n  reference: %s\n  seam:      %s",
					renderListing(want), renderListing(got))
			}
			if want.Dirs != got.Dirs || want.Files != got.Files || want.Total != got.Total {
				t.Fatalf("counts differ: reference dirs=%d files=%d total=%d, seam dirs=%d files=%d total=%d",
					want.Dirs, want.Files, want.Total, got.Dirs, got.Files, got.Total)
			}
			if want.Truncated != got.Truncated || want.LimitHit != got.LimitHit {
				t.Fatalf("flags differ: reference truncated=%v limit=%v, seam truncated=%v limit=%v",
					want.Truncated, want.LimitHit, got.Truncated, got.LimitHit)
			}
		})
	}
}

func renderListing(listing TreeListing) string {
	parts := make([]string, 0, len(listing.Entries))
	for _, entry := range listing.Entries {
		mark := ""
		if entry.Dir {
			mark = "/"
		}
		parts = append(parts, fmt.Sprintf("%d:%s%s", entry.Depth, entry.RelPath, mark))
	}
	return strings.Join(parts, ",")
}

// toolcore is imported for IsNotExist, the predicate the handler uses to turn a
// missing root into NOT_FOUND; the differential compares through it, not around it.
