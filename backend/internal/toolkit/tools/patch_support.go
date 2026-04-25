package tools

import (
	"path/filepath"

	"github.com/pmezard/go-difflib/difflib"
)

func buildUnifiedPatch(path, before, after string) string {
	return buildUnifiedPatchFromStates(path, &before, &after)
}

func buildUnifiedPatchFromStates(path string, before, after *string) string {
	normalizedPath := filepath.ToSlash(path)
	fromFile := "a/" + normalizedPath
	toFile := "b/" + normalizedPath
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
