package searchcore

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"testing"
)

func grepPathsFixture(t *testing.T) (first, second string) {
	t.Helper()
	first = t.TempDir()
	second = t.TempDir()
	files := []struct {
		root    string
		name    string
		content string
	}{
		{first, "a.txt", "needle a\n"},
		{first, "b.txt", "miss\n"},
		{second, "c.txt", "needle c\n"},
	}
	for _, file := range files {
		path := filepath.Join(file.root, file.name)
		if err := os.WriteFile(path, []byte(file.content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return first, second
}

func grepPathMatches(t *testing.T, opts GrepOptions) []string {
	t.Helper()
	result, err := Grep(context.Background(), opts)
	if err != nil {
		t.Fatalf("grep %+v: %v", opts, err)
	}
	out := make([]string, 0, len(result.Matches))
	for _, match := range result.Matches {
		out = append(out, filepath.Base(match.File))
	}
	sort.Strings(out)
	return out
}

// TestGrepSearchesEveryConfiguredRoot pins the multi-root contract: `path` is
// searched first, then every entry of `paths`, and the page window applies
// across the whole ordered result.
func TestGrepSearchesEveryConfiguredRoot(t *testing.T) {
	first, second := grepPathsFixture(t)

	both := grepPathMatches(t, GrepOptions{Pattern: "needle", Path: first, Paths: []string{second}})
	assertFileList(t, both, []string{"a.txt", "c.txt"})

	pathsOnly := grepPathMatches(t, GrepOptions{Pattern: "needle", Paths: []string{first, second}})
	assertFileList(t, pathsOnly, []string{"a.txt", "c.txt"})

	secondOnly := grepPathMatches(t, GrepOptions{Pattern: "needle", Paths: []string{second}})
	assertFileList(t, secondOnly, []string{"c.txt"})
}

func TestGrepMultiRootPageStopsEarly(t *testing.T) {
	first, second := grepPathsFixture(t)

	result, err := Grep(context.Background(), GrepOptions{
		Pattern: "needle", Path: first, Paths: []string{second}, Limit: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Matches) != 1 || filepath.Base(result.Matches[0].File) != "a.txt" {
		t.Fatalf("limit 1 should keep the first root's match, got %+v", result.Matches)
	}
	if !result.Truncated {
		t.Fatal("two matching files with limit 1 must report truncation")
	}
}

func TestGrepMultiRootIsNeverSingleFile(t *testing.T) {
	first, second := grepPathsFixture(t)

	result, err := Grep(context.Background(), GrepOptions{
		Pattern: "needle",
		Path:    filepath.Join(first, "a.txt"),
		Paths:   []string{filepath.Join(second, "c.txt")},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.SingleFile {
		t.Fatal("two file roots must not report single_file")
	}
	if len(result.Matches) != 2 {
		t.Fatalf("both files should match, got %+v", result.Matches)
	}
}

func TestGrepMultiRootFiltersAreRelativeToEachRoot(t *testing.T) {
	first := t.TempDir()
	second := t.TempDir()
	if err := os.MkdirAll(filepath.Join(first, "nested"), 0o755); err != nil {
		t.Fatal(err)
	}
	for path, content := range map[string]string{
		filepath.Join(first, "nested", "keep.go"): "needle keep\n",
		filepath.Join(first, "top.go"):            "needle top\n",
		filepath.Join(second, "other.go"):         "needle other\n",
	} {
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	// The same relative filter must apply inside each root, not against the
	// concatenation of roots.
	got := grepPathMatches(t, GrepOptions{
		Pattern: "needle", Path: first, Paths: []string{second},
		Include: []string{"*.go"}, Exclude: []string{"top.go"},
	})
	assertFileList(t, got, []string{"keep.go", "other.go"})
}
