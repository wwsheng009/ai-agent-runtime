package searchcore

import "testing"

// TestGrepOffsetWindowAppliedBySharedHelpers pins the division of labor: the
// shared window helpers (ApplyGrepPageWindow) own offset/limit slicing; the
// renderer only renders whatever matches the window left in the result.
func TestGrepOffsetWindowAppliedBySharedHelpers(t *testing.T) {
	matches := []GrepMatch{
		{File: "a.txt", Line: 1, Text: "needle 1"},
		{File: "a.txt", Line: 2, Text: "needle 2"},
		{File: "a.txt", Line: 3, Text: "needle 3"},
		{File: "a.txt", Line: 4, Text: "needle 4"},
		{File: "a.txt", Line: 5, Text: "needle 5"},
		{File: "a.txt", Line: 6, Text: "needle 6"},
	}
	result := GrepResult{Matches: matches, Mode: GrepModeContent}
	ApplyGrepPageWindow(&result, GrepModeContent, GrepOptions{Offset: 2, Limit: 2})
	if len(result.Matches) != 2 || result.Matches[0].Line != 3 || result.Matches[1].Line != 4 {
		t.Fatalf("window should keep lines 3-4, got %+v", result.Matches)
	}
}
