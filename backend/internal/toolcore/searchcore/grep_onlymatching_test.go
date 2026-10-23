package searchcore

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func grepFragmentFixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	for name, content := range map[string]string{
		"a.txt": "foo bar foo\nfoo again\n",
		"b.txt": "foo\n",
		"c.txt": "nothing\n",
	} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func fragmentLabels(result GrepResult) []string {
	out := make([]string, 0, len(result.Matches))
	for _, match := range result.Matches {
		out = append(out, fmt.Sprintf("%s:%d:%d:%s", filepath.Base(match.File), match.Line, match.Column, match.Text))
	}
	return out
}

// TestGrepOnlyMatchingEmitsFragments pins the fragment contract: one entry per
// match with its 1-based byte column, and the page unit becomes the fragment.
func TestGrepOnlyMatchingEmitsFragments(t *testing.T) {
	root := grepFragmentFixture(t)

	result, err := Grep(context.Background(), GrepOptions{
		Pattern: "foo", Path: root, OnlyMatching: true, Limit: 50,
	})
	if err != nil {
		t.Fatal(err)
	}
	assertFileList(t, fragmentLabels(result), []string{
		"a.txt:1:1:foo",
		"a.txt:1:9:foo",
		"a.txt:2:1:foo",
		"b.txt:1:1:foo",
	})
}

func TestGrepOnlyMatchingPagesByFragment(t *testing.T) {
	root := grepFragmentFixture(t)

	result, err := Grep(context.Background(), GrepOptions{
		Pattern: "foo", Path: root, OnlyMatching: true, Limit: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	assertFileList(t, fragmentLabels(result), []string{"a.txt:1:1:foo", "a.txt:1:9:foo"})
	if !result.Truncated {
		t.Fatal("more fragments exist, so has_more must be true")
	}
}

// TestGrepOnlyMatchingInvertKeepsWholeLines mirrors rg -o -v: with
// invert_match the fragment flag has no effect.
func TestGrepOnlyMatchingInvertKeepsWholeLines(t *testing.T) {
	root := grepFragmentFixture(t)

	result, err := Grep(context.Background(), GrepOptions{
		Pattern: "foo", Path: root, OnlyMatching: true, InvertMatch: true, Limit: 50,
	})
	if err != nil {
		t.Fatal(err)
	}
	assertFileList(t, matchLabels(result), []string{"c.txt:1:nothing"})
}

// TestGrepOnlyMatchingCountModeCountsMatches mirrors rg -o -c: the per-file
// count becomes a match count once fragments are the output unit.
func TestGrepOnlyMatchingCountModeCountsMatches(t *testing.T) {
	root := grepFragmentFixture(t)

	result, err := Grep(context.Background(), GrepOptions{
		Pattern: "foo", Path: root, OnlyMatching: true, Mode: GrepModeCount, Limit: 50,
	})
	if err != nil {
		t.Fatal(err)
	}
	assertFileList(t, summarizeNames(result), []string{"a.txt:3", "b.txt:1"})
}

// Files mode keeps the whole-line bookkeeping (its per-file counts are
// matching-line counts), so only_matching changes nothing there.
func TestGrepOnlyMatchingFilesModeUnaffected(t *testing.T) {
	root := grepFragmentFixture(t)

	result, err := Grep(context.Background(), GrepOptions{
		Pattern: "foo", Path: root, OnlyMatching: true, Mode: GrepModeFiles, Limit: 50,
	})
	if err != nil {
		t.Fatal(err)
	}
	assertFileList(t, summarizeNames(result), []string{"a.txt:2", "b.txt:1"})
}

// TestGrepMaxCountCountsLinesWithFragments mirrors rg -m 1 -o: the cap counts
// matching lines, and every fragment of the allowed lines is emitted.
func TestGrepMaxCountCountsLinesWithFragments(t *testing.T) {
	root := grepFragmentFixture(t)

	result, err := Grep(context.Background(), GrepOptions{
		Pattern: "foo", Path: root, OnlyMatching: true,
		MaxCount: 1, MaxCountSet: true, Limit: 50,
	})
	if err != nil {
		t.Fatal(err)
	}
	assertFileList(t, fragmentLabels(result), []string{
		"a.txt:1:1:foo",
		"a.txt:1:9:foo",
		"b.txt:1:1:foo",
	})
}
