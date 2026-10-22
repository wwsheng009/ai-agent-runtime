package searchcore

import (
	"context"
	"os"
	"path/filepath"
	"sort"
)

type GlobResult struct {
	Matches   []string
	Truncated bool
}

// Glob walks base and returns paths matching pattern. Unreadable subtrees are
// skipped; cancellation is returned to the caller.
func Glob(ctx context.Context, base, pattern string, limit int) (GlobResult, error) {
	if limit < 1 {
		limit = 500
	}
	matcher, err := NewGlobMatcher(pattern)
	if err != nil {
		return GlobResult{}, err
	}
	var result GlobResult
	walkErr := filepath.WalkDir(base, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if ctx != nil && ctx.Err() != nil {
			return ctx.Err()
		}
		// Traversal never follows links. A discovered link can point outside
		// the admitted root, so listing it would leak paths the path jail has
		// not resolved.
		if entry.Type()&os.ModeSymlink != 0 {
			return nil
		}
		if entry.IsDir() {
			if ShouldSkipDir(entry.Name()) && path != base {
				return filepath.SkipDir
			}
			return nil
		}
		rel, relErr := filepath.Rel(base, path)
		if relErr != nil {
			return nil
		}
		if matcher.Match(filepath.ToSlash(rel)) {
			result.Matches = append(result.Matches, path)
			if len(result.Matches) >= limit+1 {
				return filepath.SkipAll
			}
		}
		return nil
	})
	if walkErr != nil && walkErr != filepath.SkipAll {
		return GlobResult{}, walkErr
	}
	if len(result.Matches) > limit {
		result.Matches = result.Matches[:limit]
		result.Truncated = true
	}
	sort.Strings(result.Matches)
	return result, nil
}

// SkippedDirNames lists dependency/VCS trees that would dominate a walk. The
// list is shared with surfaces that delegate traversal to ripgrep so both
// engines skip the same directories.
var SkippedDirNames = [...]string{
	".git",
	"node_modules",
	"vendor",
	".venv",
	"__pycache__",
	".idea",
	".gradle",
}

// ShouldSkipDir reports whether a directory name belongs to a skipped tree.
func ShouldSkipDir(name string) bool {
	for _, skipped := range SkippedDirNames {
		if name == skipped {
			return true
		}
	}
	return false
}
