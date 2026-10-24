package mutatecore

import (
	"path/filepath"

	"github.com/pmezard/go-difflib/difflib"
)

// UnifiedDiff renders what a mutation changed. Both surfaces publish it under
// `patch`, and it is the cheapest evidence a caller has that the edit it asked
// for is the edit that landed.
//
// An empty string means "nothing to show" (identical content), not an error:
// the field is advisory, and a diff failure must not turn a successful write
// into a failure.
func UnifiedDiff(path, before, after string) string {
	return UnifiedDiffStates(path, &before, &after)
}

// UnifiedDiffStates is the same renderer for a mutation that has no side on one
// end: a created file has no old text, a deleted one has no new text. Those
// sides render as /dev/null instead of an empty file, which is the difference
// between "this file was created" and "this file was emptied".
func UnifiedDiffStates(path string, before, after *string) string {
	if before != nil && after != nil && *before == *after {
		return ""
	}
	if before == nil && after == nil {
		return ""
	}
	fromFile := "a/" + filepath.ToSlash(path)
	toFile := "b/" + filepath.ToSlash(path)
	var beforeLines []string
	var afterLines []string
	if before == nil {
		fromFile = "/dev/null"
	} else {
		beforeLines = difflib.SplitLines(*before)
	}
	if after == nil {
		toFile = "/dev/null"
	} else {
		afterLines = difflib.SplitLines(*after)
	}
	diff, err := difflib.GetUnifiedDiffString(difflib.UnifiedDiff{
		A:        beforeLines,
		B:        afterLines,
		FromFile: fromFile,
		ToFile:   toFile,
		Context:  3,
	})
	if err != nil {
		return ""
	}
	return diff
}
