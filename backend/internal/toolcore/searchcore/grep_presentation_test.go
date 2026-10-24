package searchcore

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// grepPresentationFixture has the three line shapes these parameters care
// about: an indented match, a short non-match, and an over-long match whose
// needle sits deep in the line.
func grepPresentationFixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	content := "   indented needle here\n" +
		"second line\n" +
		strings.Repeat("y", 300) + "needle tail\n"
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return root
}

// pageFor runs a real search and renders it through the shared page renderer,
// so these tests exercise the same path the handler uses.
func pageFor(t *testing.T, root string, opts GrepOptions) string {
	t.Helper()
	opts.Path = root
	if opts.Pattern == "" {
		opts.Pattern = "needle"
	}
	result, err := Grep(context.Background(), opts)
	if err != nil {
		t.Fatalf("grep: %v", err)
	}
	page := renderGrepPage(grepRenderInputs{
		paths:   fakeRenderPaths{root: root},
		result:  &result,
		options: opts,
		roots:   []string{root},
		mode:    GrepModeContent,
	})
	return page.content
}

// TestGrepColumnReportsMatchPosition pins the information column adds: the
// 1-based byte offset of the match inside the line, which the default shape
// does not carry.
func TestGrepColumnReportsMatchPosition(t *testing.T) {
	root := grepPresentationFixture(t)
	page := pageFor(t, root, GrepOptions{Column: true, Limit: 50})
	if !strings.Contains(page, "a.txt:1:13:    indented needle here") {
		t.Fatalf("expected the match column on line 1:\n%s", page)
	}
	if !strings.Contains(page, "a.txt:3:301:") {
		t.Fatalf("expected column 301 on the long line:\n%s", page)
	}
}

// TestGrepTrimDoesNotMoveColumn is the pairing that is easy to get wrong:
// trimming changes what is printed, never where the match was found.
func TestGrepTrimDoesNotMoveColumn(t *testing.T) {
	root := grepPresentationFixture(t)
	page := pageFor(t, root, GrepOptions{Trim: true, Column: true, Limit: 50})
	if !strings.Contains(page, "a.txt:1:13: indented needle here") {
		t.Fatalf("trim must strip indentation but keep column 13:\n%s", page)
	}
}

// TestGrepMaxColumnsOmitsOrPreviews pins both shapes of the length cap,
// including the exact marker text the local tool has always printed.
func TestGrepMaxColumnsOmitsOrPreviews(t *testing.T) {
	root := grepPresentationFixture(t)

	omitted := pageFor(t, root, GrepOptions{MaxColumns: 20, MaxColumnsSet: true, Limit: 50})
	if !strings.Contains(omitted, "a.txt:1: [Omitted long matching line]") {
		t.Fatalf("expected the omit marker:\n%s", omitted)
	}
	if strings.Contains(omitted, "indented") {
		t.Fatalf("an omitted line must not leak its content:\n%s", omitted)
	}

	previewed := pageFor(t, root, GrepOptions{
		MaxColumns: 20, MaxColumnsSet: true, MaxColumnsPreview: true, Limit: 50,
	})
	if !strings.Contains(previewed, " [... omitted end of long line]") {
		t.Fatalf("expected the preview suffix:\n%s", previewed)
	}
	if !strings.Contains(previewed, "indented needle h") {
		t.Fatalf("the preview must keep a prefix:\n%s", previewed)
	}
}

// TestGrepInvertHasNoColumn: an inverted match is a line that does NOT contain
// the pattern, so there is no position to report.
func TestGrepInvertHasNoColumn(t *testing.T) {
	root := grepPresentationFixture(t)
	page := pageFor(t, root, GrepOptions{InvertMatch: true, Column: true, Limit: 50})
	if strings.HasPrefix(page, "a.txt:2:") && strings.Contains(page, "a.txt:2:1:") {
		t.Fatalf("inverted lines must not carry a column:\n%s", page)
	}
	if !strings.Contains(page, "a.txt:2: second line") {
		t.Fatalf("expected the inverted line without a column:\n%s", page)
	}
}
