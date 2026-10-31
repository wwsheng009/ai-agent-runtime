package lscore

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/internal/toolcore/localio"
)

func mustSymlinkOrSkip(t *testing.T, target, link string) {
	t.Helper()
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
}

func treePaths(listing TreeListing) []string {
	paths := make([]string, 0, len(listing.Entries))
	for _, entry := range listing.Entries {
		paths = append(paths, entry.RelPath)
	}
	return paths
}

func TestReadTreeWithOptionsFollowsSymlinkedRoot(t *testing.T) {
	base := t.TempDir()
	target := filepath.Join(base, "target")
	if err := os.MkdirAll(filepath.Join(target, "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target, "src", "main.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(base, "linked-root")
	mustSymlinkOrSkip(t, target, link)

	legacy, err := ReadTreeVia(context.Background(), localio.Access, link, 2, 100)
	if err != nil {
		t.Fatalf("legacy ls walk: %v", err)
	}
	if len(legacy.Entries) != 0 {
		t.Fatalf("follow=false must keep a linked root opaque, got %v", treePaths(legacy))
	}

	listing, err := ReadTreeViaWithOptions(context.Background(), localio.Access, link, 2, 100, ReadTreeOptions{FollowLinks: true})
	if err != nil {
		t.Fatalf("followed ls walk: %v", err)
	}
	got := strings.Join(treePaths(listing), ",")
	want := "src,src/main.go"
	if got != want {
		t.Fatalf("linked root listing = %q, want %q", got, want)
	}
	if listing.Dirs != 1 || listing.Files != 1 {
		t.Fatalf("linked root counts = dirs %d files %d, want 1/1", listing.Dirs, listing.Files)
	}
}

// The explicit root is resolved independently of nested links: the caller
// named that path, but did not thereby agree to follow every link below it.
func TestReadTreeWithOptionsFollowsRootWithoutFollowingNestedLinks(t *testing.T) {
	base := t.TempDir()
	target := filepath.Join(base, "target")
	outside := filepath.Join(base, "outside")
	for _, dir := range []string{target, outside} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(target, "plain.txt"), []byte("plain\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outside, "note.txt"), []byte("linked\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustSymlinkOrSkip(t, outside, filepath.Join(target, "nested"))
	link := filepath.Join(base, "linked-root")
	mustSymlinkOrSkip(t, target, link)

	listing, err := ReadTreeViaWithOptions(context.Background(), localio.Access, link, 3, 100, ReadTreeOptions{FollowRoot: true})
	if err != nil {
		t.Fatalf("follow-root ls walk: %v", err)
	}
	got := strings.Join(treePaths(listing), ",")
	want := "nested,plain.txt"
	if got != want {
		t.Fatalf("follow-root listing = %q, want %q", got, want)
	}
	if listing.Dirs != 0 || listing.Files != 2 {
		t.Fatalf("follow-root counts = dirs %d files %d, want 0/2", listing.Dirs, listing.Files)
	}
}

func TestReadTreeWithOptionsFollowsNestedDirectoryLink(t *testing.T) {
	base := t.TempDir()
	outside := filepath.Join(base, "outside")
	root := filepath.Join(base, "root")
	if err := os.MkdirAll(outside, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outside, "note.txt"), []byte("linked\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustSymlinkOrSkip(t, outside, filepath.Join(root, "linked"))

	plain, err := ReadTreeVia(context.Background(), localio.Access, root, 3, 100)
	if err != nil {
		t.Fatalf("plain ls walk: %v", err)
	}
	if got := strings.Join(treePaths(plain), ","); got != "linked" {
		t.Fatalf("follow=false listing = %q, want only the link entry", got)
	}
	if plain.Files != 1 || plain.Dirs != 0 {
		t.Fatalf("follow=false must report the link as a file, got %+v", plain)
	}

	followed, err := ReadTreeViaWithOptions(context.Background(), localio.Access, root, 3, 100, ReadTreeOptions{FollowLinks: true})
	if err != nil {
		t.Fatalf("followed ls walk: %v", err)
	}
	got := strings.Join(treePaths(followed), ",")
	want := "linked,linked/note.txt"
	if got != want {
		t.Fatalf("followed listing = %q, want %q", got, want)
	}
	if followed.Dirs != 1 || followed.Files != 1 {
		t.Fatalf("followed counts = dirs %d files %d, want 1/1", followed.Dirs, followed.Files)
	}
}

func TestReadTreeWithOptionsTerminatesLinkCycle(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "top.txt"), []byte("top\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustSymlinkOrSkip(t, root, filepath.Join(root, "loop"))

	listing, err := ReadTreeViaWithOptions(context.Background(), localio.Access, root, 8, 100, ReadTreeOptions{FollowLinks: true})
	if err != nil {
		t.Fatalf("cycle ls walk: %v", err)
	}
	got := strings.Join(treePaths(listing), ",")
	want := "loop,top.txt"
	if got != want {
		t.Fatalf("cycle listing = %q, want %q", got, want)
	}
}
