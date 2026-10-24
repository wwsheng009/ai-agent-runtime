package searchcore

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/toolcore"
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
	// CaseInsensitive matches the pattern without regard to letter case. The
	// local tool has always exposed this as case_insensitive/ignore_case, and
	// finding a file whose casing you do not remember is the main reason to
	// reach for glob.
	CaseInsensitive bool
	// SortByModTime orders matches newest-first (equal mtimes fall back to the
	// path) instead of the default name order. This is the local tool's order:
	// the files a caller is usually looking for are the ones just touched.
	SortByModTime bool
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
	if opts.CaseInsensitive {
		matcher.caseInsensitive = true
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
			// No hardcoded skipped trees: the same policy the local glob tool
			// has always had.
			return nil
		}
		rel, relErr := filepath.Rel(base, path)
		if relErr != nil {
			return nil
		}
		// The glob tool matches the root-relative path with standard glob
		// depth rules: `*.go` is depth 1, `**/*.go` recurses.
		if matcher.MatchRelative(filepath.ToSlash(rel)) {
			result.Matches = append(result.Matches, path)
			// Stop early only while the walk order is the page order. With name
			// ordering, the first N names visited are the first N of the page.
			// With mtime ordering the list is sorted after the walk, so stopping
			// at N would keep an arbitrary prefix and then sort it - "newest
			// page" would silently mean "newest among those visited first".
			if !opts.SortByModTime && len(result.Matches) >= opts.Offset+limit+1 {
				return filepath.SkipAll
			}
		}
		return nil
	})
	if walkErr != nil && walkErr != filepath.SkipAll {
		return GlobResult{}, walkErr
	}
	// Order before paging. Paging a list and sorting the page afterwards would
	// give every page its own order, so next_offset could not describe a stable
	// sequence.
	if opts.SortByModTime {
		sortGlobMatchesByModTime(result.Matches)
	} else {
		sort.Strings(result.Matches)
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
	result.Offset = opts.Offset
	result.NextOffset = opts.Offset + len(result.Matches)
	result.HasMore = result.Truncated
	return result, nil
}

// renderGlobPage writes the page in the local tool's shape: one relative path
// per line, no header. The header used to read `=== glob "x" in y ===`, which
// repeated what the call already said and made the body harder to scan (or to
// pipe into another tool).
func renderGlobPage(builder *strings.Builder, paths toolcore.PathResolver, result GlobResult, offset int) {
	if len(result.Matches) == 0 {
		builder.WriteString("未找到匹配项")
		return
	}
	lines := make([]string, 0, len(result.Matches))
	for _, match := range result.Matches {
		lines = append(lines, paths.Relative(match))
	}
	builder.WriteString(strings.Join(lines, "\n"))
	if !result.Truncated {
		return
	}
	// Two notice shapes, matching the local tool: an explicit offset call shows
	// the window range, while the default (first page) shows how many files came
	// back. Both carry next_offset so the model can continue instead of
	// rewording the search.
	next := offset + len(result.Matches)
	if offset > 0 {
		builder.WriteString(fmt.Sprintf("\n\n(显示第 %d-%d 个文件；next_offset=%d 可继续分页)",
			offset+1, next, next))
		return
	}
	builder.WriteString(fmt.Sprintf("\n\n(已分页显示前 %d 个文件；next_offset=%d 可继续分页)",
		len(result.Matches), next))
}

// sortGlobMatchesByModTime orders newest first, breaking ties by path so the
// sequence is stable across runs. A match that cannot be stat'ed sorts last
// rather than being dropped: it still is a real match.
func sortGlobMatchesByModTime(matches []string) {
	type entry struct {
		path    string
		modTime time.Time
	}
	entries := make([]entry, 0, len(matches))
	for _, match := range matches {
		var mod time.Time
		if info, err := os.Stat(match); err == nil {
			mod = info.ModTime()
		}
		entries = append(entries, entry{path: match, modTime: mod})
	}
	sort.SliceStable(entries, func(i, j int) bool {
		if entries[i].modTime.Equal(entries[j].modTime) {
			return entries[i].path < entries[j].path
		}
		return entries[i].modTime.After(entries[j].modTime)
	})
	for i, item := range entries {
		matches[i] = item.path
	}
}
