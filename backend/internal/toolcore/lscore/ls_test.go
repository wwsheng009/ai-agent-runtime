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
