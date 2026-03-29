package tools

import (
	"path/filepath"

	"github.com/pmezard/go-difflib/difflib"
)

func buildUnifiedPatch(path, before, after string) string {
	normalizedPath := filepath.ToSlash(path)
	fromFile := "a/" + normalizedPath
	toFile := "b/" + normalizedPath
	if before == "" {
		fromFile = "/dev/null"
	}

	diff, err := difflib.GetUnifiedDiffString(difflib.UnifiedDiff{
		A:        difflib.SplitLines(before),
		B:        difflib.SplitLines(after),
		FromFile: fromFile,
		ToFile:   toFile,
		Context:  3,
	})
	if err != nil {
		return ""
	}
	return diff
}
