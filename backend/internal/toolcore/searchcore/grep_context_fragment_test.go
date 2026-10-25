package searchcore

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestExpandGrepContextKeepsFragmentsOnTheMatchedLine pins the `rg -o -C` shape
// the shared handler now renders: the matched line shows its fragments, the
// surrounding lines show whole lines, and fragments sharing a line stay one
// block (a separator between them would invent a gap that is not there).
func TestExpandGrepContextKeepsFragmentsOnTheMatchedLine(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "a.txt")
	if err := os.WriteFile(path, []byte("before\nfoo and foo\nmiddle\nfoo\nlast\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	matches := []GrepMatch{
		{File: path, Line: 2, Column: 1, Text: "foo"},
		{File: path, Line: 2, Column: 9, Text: "foo"},
		{File: path, Line: 4, Column: 1, Text: "foo"},
	}
	lines := ExpandGrepContext(matches, 1, 1, true)

	type entry struct {
		line    int
		isMatch bool
		text    string
		group   bool
	}
	got := make([]entry, 0, len(lines))
	for _, line := range lines {
		got = append(got, entry{line: line.Line, isMatch: line.IsMatch, text: line.Text, group: line.GroupStart})
	}

	want := []entry{
		{line: 1, isMatch: false, text: "before"},
		{line: 2, isMatch: true, text: "foo"},
		{line: 2, isMatch: true, text: "foo"},
		{line: 3, isMatch: false, text: "middle"},
		{line: 4, isMatch: true, text: "foo"},
		{line: 5, isMatch: false, text: "last"},
	}
	if len(got) != len(want) {
		t.Fatalf("context window has %d entries, want %d: %+v", len(got), len(want), got)
	}
	for i := range want {
		if got[i].line != want[i].line || got[i].isMatch != want[i].isMatch {
			t.Fatalf("entry %d = %+v, want %+v", i, got[i], want[i])
		}
	}
	// Match lines carry the fragment; context lines carry the whole line.
	for i, item := range got {
		if want[i].isMatch {
			if item.text != "foo" {
				t.Fatalf("match entry %d shows %q, want the fragment", i, item.text)
			}
			continue
		}
		if !strings.Contains(item.text, strings.TrimSpace(want[i].text)) {
			t.Fatalf("context entry %d shows %q, want the whole line", i, item.text)
		}
	}
	for i, item := range got {
		if item.group != (i == 0) {
			t.Fatalf("entry %d GroupStart = %v; every line here is contiguous, so only the first starts the block", i, item.group)
		}
	}
}

// TestGrepOnlyMatchingWithContextRendersBothUnits is the handler-level view: the
// combination used to be refused ("not combined by the shared core yet"), and it
// now answers with fragments on the matched line and context around it.
func TestGrepOnlyMatchingWithContextRendersBothUnits(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("before\nfoo\nmiddle\nafter\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	result := runGrepHandler(t, root, map[string]any{
		"pattern":       "foo",
		"only_matching": true,
		"context":       1,
	})
	if !result.Success {
		t.Fatalf("only_matching with context must answer, got: %s", result.Content)
	}
	for _, want := range []string{"a.txt>2: foo", "a.txt 1: before", "a.txt 3: middle"} {
		if !strings.Contains(result.Content, want) {
			t.Fatalf("page missing %q:\n%s", want, result.Content)
		}
	}
}
