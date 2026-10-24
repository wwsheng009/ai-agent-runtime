package searchcore

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// globDepthTree builds a tree with files at two depths plus distinct mtimes.
func globDepthTree(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	relPaths := []string{"a.go", "pkg/c.go", "pkg/nested/d.go", "b.txt"}
	base := time.Now().Add(-time.Hour)
	for i, rel := range relPaths {
		full := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		// Later files are newer: the ordering is observable.
		ts := base.Add(time.Duration(i) * time.Minute)
		if err := os.Chtimes(full, ts, ts); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

// TestGlobCaseInsensitive pins the case_insensitive option: the local tool has
// always had it, and it is the reason a caller can find BotPage when they typed
// botpage.
func TestGlobCaseInsensitive(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"BotPage.tsx", "other.tsx"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	sensitive, err := GlobWithOptions(context.Background(), GlobOptions{
		Base: root, Pattern: "*.tsx", Limit: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(sensitive.Matches) != 2 {
		t.Fatalf("case-sensitive match count = %d, want 2", len(sensitive.Matches))
	}

	// A lowercase spelling must miss the mixed-case file by default...
	miss, err := GlobWithOptions(context.Background(), GlobOptions{
		Base: root, Pattern: "botpage.tsx", Limit: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(miss.Matches) != 0 {
		t.Fatalf("default matching must be case-sensitive, got %v", miss.Matches)
	}

	// ...and find it when the caller asks.
	hit, err := GlobWithOptions(context.Background(), GlobOptions{
		Base: root, Pattern: "botpage.tsx", Limit: 10, CaseInsensitive: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(hit.Matches) != 1 || filepath.Base(hit.Matches[0]) != "BotPage.tsx" {
		t.Fatalf("case-insensitive match = %v, want BotPage.tsx", hit.Matches)
	}
}

// TestGlobSortsByModTimeNewestFirst pins the match order: the local tool returns
// the most recently touched files first, because that is what a caller looking
// for "what changed" needs.
func TestGlobSortsByModTimeNewestFirst(t *testing.T) {
	root := globDepthTree(t)

	result, err := GlobWithOptions(context.Background(), GlobOptions{
		Base: root, Pattern: "**/*.go", Limit: 10, SortByModTime: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	got := make([]string, 0, len(result.Matches))
	for _, match := range result.Matches {
		rel, relErr := filepath.Rel(root, match)
		if relErr != nil {
			t.Fatal(relErr)
		}
		got = append(got, filepath.ToSlash(rel))
	}
	want := []string{"pkg/nested/d.go", "pkg/c.go", "a.go"}
	assertFileList(t, got, want)
}

// TestGlobPagingIsStableUnderModTimeOrder is the regression guard for a real
// trap: sorting after slicing would give each page its own order, so walking
// next_offset could repeat or skip files.
func TestGlobPagingIsStableUnderModTimeOrder(t *testing.T) {
	root := globDepthTree(t)

	seen := map[string]int{}
	offset := 0
	for page := 0; page < 10; page++ {
		result, err := GlobWithOptions(context.Background(), GlobOptions{
			Base: root, Pattern: "**/*.go", Limit: 1, Offset: offset, SortByModTime: true,
		})
		if err != nil {
			t.Fatalf("page %d: %v", page, err)
		}
		for _, match := range result.Matches {
			seen[filepath.Base(match)]++
		}
		if !result.HasMore {
			break
		}
		offset = result.NextOffset
	}
	if len(seen) != 3 {
		t.Fatalf("paging visited %d files, want 3: %v", len(seen), seen)
	}
	for name, count := range seen {
		if count != 1 {
			t.Fatalf("%s appeared %d times across pages", name, count)
		}
	}
}
