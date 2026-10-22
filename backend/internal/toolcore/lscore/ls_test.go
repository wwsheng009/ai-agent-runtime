package lscore

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReadSortsDirectoriesFirstAndLimits(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "b.txt"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "a-dir"), 0o700); err != nil {
		t.Fatal(err)
	}
	listing, err := Read(dir, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(listing.Entries) != 1 || listing.Entries[0].Name != "a-dir" || !listing.Entries[0].Dir {
		t.Fatalf("unexpected entries: %+v", listing.Entries)
	}
	if !listing.Truncated || listing.Total != 2 {
		t.Fatalf("unexpected counts: %+v", listing)
	}
	if !strings.Contains(Render("dir", listing), "list truncated") {
		t.Fatal("missing truncation notice")
	}
}

func TestReadTreeClipsDepthAndSortsPaths(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "src", "nested"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "src", "main.go"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "src", "nested", "deep.go"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	listing, err := ReadTree(root, 2, 10)
	if err != nil {
		t.Fatal(err)
	}
	var paths []string
	for _, entry := range listing.Entries {
		paths = append(paths, entry.RelPath)
	}
	want := []string{"README.md", "src", "src/main.go", "src/nested"}
	if strings.Join(paths, ",") != strings.Join(want, ",") {
		t.Fatalf("unexpected tree order: got %v want %v", paths, want)
	}
	if listing.Files != 2 || listing.Dirs != 2 || listing.Truncated {
		t.Fatalf("unexpected tree counts: %+v", listing)
	}
}

func TestReadTreeLimitsAndMarksTruncated(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"a.txt", "b.txt", "c.txt"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	listing, err := ReadTree(root, 1, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(listing.Entries) != 2 || !listing.Truncated {
		t.Fatalf("unexpected limited tree: %+v", listing)
	}
}
