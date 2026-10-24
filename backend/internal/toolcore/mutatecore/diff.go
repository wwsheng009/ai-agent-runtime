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
	if before == after {
		return ""
	}
	diff, err := difflib.GetUnifiedDiffString(difflib.UnifiedDiff{
		A:        difflib.SplitLines(before),
		B:        difflib.SplitLines(after),
		FromFile: "a/" + filepath.ToSlash(path),
		ToFile:   "b/" + filepath.ToSlash(path),
		Context:  3,
	})
	if err != nil {
		return ""
	}
	return diff
}
