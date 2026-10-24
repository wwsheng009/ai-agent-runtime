package searchcore

import "testing"

// TestSummarizeGrepResultContentPage pins the page-level statistics contract
// for content mode: fragments count as matches, matched_lines collapses the
// fragments of one line.
func TestSummarizeGrepResultContentPage(t *testing.T) {
	result := GrepResult{
		Mode: GrepModeContent,
		Matches: []GrepMatch{
			{File: "a.txt", Line: 1, Column: 1, Text: "foo"},
			{File: "a.txt", Line: 1, Column: 9, Text: "foo"},
			{File: "b.txt", Line: 3, Text: "whole line"},
		},
		FilesScanned: 7,
	}
	stats := SummarizeGrepResult(result)
	if stats.Matches != 3 {
		t.Fatalf("matches = %d, want 3", stats.Matches)
	}
	if stats.MatchedLines != 2 {
		t.Fatalf("matched_lines = %d, want 2", stats.MatchedLines)
	}
	if stats.FilesWithMatched != 2 {
		t.Fatalf("files_with_matched = %d, want 2", stats.FilesWithMatched)
	}
	if stats.FilesScanned != 7 {
		t.Fatalf("files_scanned = %d, want 7", stats.FilesScanned)
	}
}

// TestSummarizeGrepResultExcludesEvidenceFile is the reason stats are computed
// after windowing: in files/count modes the raw match list still carries the
// file read past the page, and counting it would inflate every number.
func TestSummarizeGrepResultExcludesEvidenceFile(t *testing.T) {
	result := GrepResult{
		Mode: GrepModeFiles,
		Files: []GrepFileSummary{
			{File: "a.txt", Count: 2},
		},
		Matches: []GrepMatch{
			{File: "a.txt", Line: 1},
			{File: "a.txt", Line: 2},
			{File: "b.txt", Line: 1},
		},
		FilesScanned: 4,
	}
	stats := SummarizeGrepResult(result)
	if stats.Matches != 2 || stats.MatchedLines != 2 || stats.FilesWithMatched != 1 {
		t.Fatalf("evidence file leaked into stats: %+v", stats)
	}
	if stats.FilesScanned != 4 {
		t.Fatalf("files_scanned = %d, want 4", stats.FilesScanned)
	}
}

func TestSummarizeGrepResultCountModeWithFragments(t *testing.T) {
	result := GrepResult{
		Mode: GrepModeCount,
		Files: []GrepFileSummary{
			{File: "a.txt", Count: 3},
		},
		Matches: []GrepMatch{
			{File: "a.txt", Line: 1, Column: 1},
			{File: "a.txt", Line: 1, Column: 9},
			{File: "a.txt", Line: 2, Column: 1},
		},
	}
	stats := SummarizeGrepResult(result)
	if stats.Matches != 3 {
		t.Fatalf("matches = %d, want 3 fragments", stats.Matches)
	}
	if stats.MatchedLines != 2 {
		t.Fatalf("matched_lines = %d, want 2 distinct lines", stats.MatchedLines)
	}
}

func TestSummarizeGrepResultEmptyPage(t *testing.T) {
	stats := SummarizeGrepResult(GrepResult{Mode: GrepModeContent, FilesScanned: 3})
	if stats.Matches != 0 || stats.MatchedLines != 0 || stats.FilesWithMatched != 0 {
		t.Fatalf("empty page must report zeros, got %+v", stats)
	}
	if stats.FilesScanned != 3 {
		t.Fatalf("files_scanned = %d, want 3", stats.FilesScanned)
	}
}
