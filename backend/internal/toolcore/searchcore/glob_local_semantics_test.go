package searchcore

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestGlobReturnsDirectories pins the local tool's path-listing contract: a
// glob is a path query, not a file query, so `**/docs` must find directories.
// The shared walker used to skip every directory, which answered a different
// question on the remote surface than on the local one.
func TestGlobReturnsDirectories(t *testing.T) {
	root := t.TempDir()
	for _, dir := range []string{"docs", "nested/docs"} {
		if err := os.MkdirAll(filepath.Join(root, filepath.FromSlash(dir)), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "docs", "README.md"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	result, err := GlobWithOptions(context.Background(), GlobOptions{
		Base: root, Pattern: "**/docs", Limit: 10,
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
	assertFileList(t, got, []string{"docs", "nested/docs"})
}

// TestGlobSingleFileRootMatchesItself pins the local behavior for `path`
// pointing at a file: the pattern decides whether that file is the answer.
func TestGlobSingleFileRootMatchesItself(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "main.go")
	if err := os.WriteFile(target, []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	hit, err := GlobWithOptions(context.Background(), GlobOptions{
		Base: target, Pattern: "**/*.go", Limit: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(hit.Matches) != 1 || hit.Matches[0] != target {
		t.Fatalf("file root match = %v, want [%s]", hit.Matches, target)
	}

	miss, err := GlobWithOptions(context.Background(), GlobOptions{
		Base: target, Pattern: "**/*.txt", Limit: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(miss.Matches) != 0 {
		t.Fatalf("a non-matching pattern over a file root must be empty, got %v", miss.Matches)
	}
}

// TestGlobBraceAlternativesAreUnioned pins brace handling in the shared core:
// `*.{go,ts}` is two patterns, not one literal name, and the union is
// de-duplicated and ordered once.
func TestGlobBraceAlternativesAreUnioned(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"a.go", "b.ts", "c.txt"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	result, err := GlobWithOptions(context.Background(), GlobOptions{
		Base: root, Pattern: "*.{go,ts}", Limit: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.BraceExpanded {
		t.Fatalf("expected BraceExpanded, got %+v", result)
	}
	got := make([]string, 0, len(result.Matches))
	for _, match := range result.Matches {
		got = append(got, filepath.Base(match))
	}
	assertFileList(t, got, []string{"a.go", "b.ts"})
}

// TestGlobUnsupportedBraceIsReported pins the fail-closed rule: an expansion
// that would exceed the cap is reported as unsupported instead of being applied
// partially (which would silently omit extensions).
func TestGlobUnsupportedBraceIsReported(t *testing.T) {
	// 65 alternatives is past the 64 cap.
	alternatives := make([]string, 0, 65)
	for i := 0; i < 65; i++ {
		alternatives = append(alternatives, "x"+strings.Repeat("x", i))
	}
	pattern := "*.{" + strings.Join(alternatives, ",") + "}"

	expanded := ExpandGlobBraces(pattern)
	if len(expanded) != 1 || expanded[0] != pattern {
		t.Fatalf("an over-cap expansion must be left unchanged, got %d patterns", len(expanded))
	}

	result, err := GlobWithOptions(context.Background(), GlobOptions{
		Base: t.TempDir(), Pattern: pattern, Limit: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.BraceExpanded {
		t.Fatalf("an unexpanded pattern must not claim BraceExpanded: %+v", result)
	}
	if !result.UnsupportedBracePattern {
		t.Fatalf("expected UnsupportedBracePattern for an over-cap expansion: %+v", result)
	}
}

// TestGlobPageWindowIsExact is the byte-window guard: 600 paths at ~40 bytes
// each cannot fit the 16 KiB window, so the page must be cut, must say so, and
// must leave a usable next_offset. A silently over-budget body would be folded
// by the render layer, which is exactly the double shape this window prevents.
func TestGlobPageWindowIsExact(t *testing.T) {
	result := GlobResult{
		Matches:   make([]string, 0, 600),
		Truncated: true,
	}
	for i := 0; i < 600; i++ {
		result.Matches = append(result.Matches, "/workspace/path-with-some-depth/file-"+
			strings.Repeat("0", 3)+string(rune('a'+i%26))+"-"+strings.Repeat("n", 10)+".go")
	}
	var builder strings.Builder
	shown, trimmed := renderGlobPage(&builder, relativePathResolver{}, result, 0, GlobOutputBudgetBytes)
	if !trimmed {
		t.Fatalf("expected the window to cut the page, got %d rows in %d bytes", len(shown), builder.Len())
	}
	if builder.Len() > GlobOutputBudgetBytes {
		t.Fatalf("page body is %d bytes, over the %d budget", builder.Len(), GlobOutputBudgetBytes)
	}
	if !strings.Contains(builder.String(), "next_offset=") {
		t.Fatalf("a cut page must carry a continuation offset:\n%s", builder.String())
	}
}

// relativePathResolver is the identity resolver: these tests build already
// relative rows.
type relativePathResolver struct{}

func (relativePathResolver) Root() string                       { return "/workspace" }
func (relativePathResolver) Resolve(raw string) (string, error) { return raw, nil }
func (relativePathResolver) Relative(abs string) string         { return abs }
