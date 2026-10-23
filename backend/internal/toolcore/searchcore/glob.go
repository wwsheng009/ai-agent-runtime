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
	// Offset and NextOffset let both surfaces page deterministically.
	Offset     int
	NextOffset int
	HasMore    bool
}

// Glob walks base and returns paths matching pattern. Unreadable subtrees are
// skipped; cancellation is returned to the caller.
func Glob(ctx context.Context, base, pattern string, limit int) (GlobResult, error) {
	return GlobWithOptions(ctx, GlobOptions{Base: base, Pattern: pattern, Limit: limit})
}

type GlobOptions struct {
	Base    string
	Pattern string
	Limit   int
	Offset  int
}

func GlobWithOptions(ctx context.Context, opts GlobOptions) (GlobResult, error) {
	base := opts.Base
	pattern := opts.Pattern
	limit := opts.Limit
	if limit < 1 {
		limit = 500
	}
	if opts.Offset < 0 {
		opts.Offset = 0
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
			if len(result.Matches) >= opts.Offset+limit+1 {
				return filepath.SkipAll
			}
		}
		return nil
	})
	if walkErr != nil && walkErr != filepath.SkipAll {
		return GlobResult{}, walkErr
	}
	if opts.Offset > 0 {
		if opts.Offset >= len(result.Matches) {
			result.Matches = nil
		} else {
			result.Matches = result.Matches[opts.Offset:]
		}
	}
	if len(result.Matches) > limit {
		result.Matches = result.Matches[:limit]
		result.Truncated = true
	}
	sort.Strings(result.Matches)
	result.Offset = opts.Offset
	result.NextOffset = opts.Offset + len(result.Matches)
	result.HasMore = result.Truncated
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
