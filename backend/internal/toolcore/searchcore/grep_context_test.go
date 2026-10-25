package searchcore

import (
	"fmt"
	"testing"
)

func contextLine(file string, line int, text string, isMatch, groupStart bool) GrepContextLine {
	return GrepContextLine{File: file, Line: line, Text: text, IsMatch: isMatch, GroupStart: groupStart}
}

func assertContextLines(t *testing.T, got, want []GrepContextLine) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("got %d lines, want %d:\n got=%s\nwant=%s",
			len(got), len(want), formatContextLines(got), formatContextLines(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("line %d mismatch:\n got=%+v\nwant=%+v\nall got=%s",
				i, got[i], want[i], formatContextLines(got))
		}
	}
}

func formatContextLines(lines []GrepContextLine) string {
	out := ""
	for _, line := range lines {
		marker := "-"
		if line.IsMatch {
			marker = ":"
		}
		group := ""
		if line.GroupStart {
			group = "^ "
		}
		out += fmt.Sprintf("%s%d%s%s\n", group, line.Line, marker, line.Text)
	}
	return out
}

func TestExpandGrepContextWindowsAndGroups(t *testing.T) {
	file := writePatternFixture(t, "ctx.txt", "l1\nl2\nMATCH3\nl4\nl5\nl6\nMATCH7\nl8\n")
	matches := []GrepMatch{
		{File: file, Line: 3, Text: "MATCH3"},
		{File: file, Line: 7, Text: "MATCH7"},
	}

	got := ExpandGrepContext(matches, 1, 1, false)
	want := []GrepContextLine{
		contextLine(file, 2, "l2", false, true),
		contextLine(file, 3, "MATCH3", true, false),
		contextLine(file, 4, "l4", false, false),
		contextLine(file, 6, "l6", false, true),
		contextLine(file, 7, "MATCH7", true, false),
		contextLine(file, 8, "l8", false, false),
	}
	assertContextLines(t, got, want)
}

func TestExpandGrepContextMergesOverlappingWindows(t *testing.T) {
	file := writePatternFixture(t, "merge.txt", "l1\nl2\nM3\nM4\nl5\nl6\n")
	matches := []GrepMatch{
		{File: file, Line: 3, Text: "M3"},
		{File: file, Line: 4, Text: "M4"},
	}

	got := ExpandGrepContext(matches, 1, 1, false)
	want := []GrepContextLine{
		contextLine(file, 2, "l2", false, true),
		contextLine(file, 3, "M3", true, false),
		contextLine(file, 4, "M4", true, false),
		contextLine(file, 5, "l5", false, false),
	}
	assertContextLines(t, got, want)
}

func TestExpandGrepContextClampsBeforeContext(t *testing.T) {
	content := ""
	for i := 1; i <= 20; i++ {
		content += fmt.Sprintf("l%d\n", i)
	}
	file := writePatternFixture(t, "long.txt", content)
	matches := []GrepMatch{{File: file, Line: 20, Text: "l20"}}

	got := ExpandGrepContext(matches, 99, 0, false)
	if len(got) != MaxGrepContextLines+1 {
		t.Fatalf("clamped window should cover %d lines, got %d: %s",
			MaxGrepContextLines+1, len(got), formatContextLines(got))
	}
	if got[0].Line != 20-MaxGrepContextLines || !got[0].GroupStart || got[0].IsMatch {
		t.Fatalf("unexpected first line: %+v", got[0])
	}
	if last := got[len(got)-1]; last.Line != 20 || !last.IsMatch {
		t.Fatalf("unexpected last line: %+v", last)
	}
}

func TestExpandGrepContextWithoutContextPassesThroughMatches(t *testing.T) {
	file := writePatternFixture(t, "plain.txt", "l1\nM2\nl3\nl4\nM5\n")
	matches := []GrepMatch{
		{File: file, Line: 2, Text: "M2"},
		{File: file, Line: 5, Text: "M5"},
	}

	got := ExpandGrepContext(matches, 0, 0, false)
	want := []GrepContextLine{
		contextLine(file, 2, "M2", true, true),
		contextLine(file, 5, "M5", true, true),
	}
	assertContextLines(t, got, want)
}

func TestExpandGrepContextStartsNewGroupPerFile(t *testing.T) {
	first := writePatternFixture(t, "first.txt", "a\nM1\nb\n")
	second := writePatternFixture(t, "second.txt", "c\nM2\nd\n")
	matches := []GrepMatch{
		{File: first, Line: 2, Text: "M1"},
		{File: second, Line: 2, Text: "M2"},
	}

	got := ExpandGrepContext(matches, 0, 1, false)
	want := []GrepContextLine{
		contextLine(first, 2, "M1", true, true),
		contextLine(first, 3, "b", false, false),
		contextLine(second, 2, "M2", true, true),
		contextLine(second, 3, "d", false, false),
	}
	assertContextLines(t, got, want)
}

func TestClampGrepContextLines(t *testing.T) {
	cases := map[int]int{-3: 0, 0: 0, 4: 4, 10: 10, 11: 10, 999: 10}
	for in, want := range cases {
		if got := ClampGrepContextLines(in); got != want {
			t.Fatalf("ClampGrepContextLines(%d) = %d, want %d", in, got, want)
		}
	}
}
