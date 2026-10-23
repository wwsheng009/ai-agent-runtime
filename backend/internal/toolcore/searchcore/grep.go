package searchcore

import (
	"context"
	"os"
	"path/filepath"
	"strings"
)

const (
	DefaultGrepLimit = 200
	MaxGrepLineBytes = 2048
)

type GrepOptions struct {
	Pattern string
	// Patterns is the batch spelling (rg -e ...). When it is non-empty the
	// single-value Pattern is ignored, matching the handler's precedence.
	Patterns []string
	Path     string
	Glob     string
	// Include/Exclude are file filters applied to the path relative to the
	// search root (a bare file name for a single-file root). Include is an
	// allow-list; exclude wins over include. Patterns without a slash match
	// the basename anywhere below the root.
	Include []string
	Exclude []string
	// Paths holds additional search roots, searched in order after Path. The
	// ordered pair mirrors the local tool: `path` first, then `paths`.
	Paths []string
	// Mode selects the output unit (content lines, files, per-file counts) and
	// with it the pagination unit. Empty means content.
	Mode GrepMode
	// CaseInsensitive is the legacy alias kept for callers that predate
	// IgnoreCase; it is folded into IgnoreCase by the pattern options.
	CaseInsensitive bool
	Literal         bool
	IgnoreCase      bool
	CaseSensitive   bool
	SmartCase       bool
	Word            bool
	LineRegexp      bool
	InvertMatch     bool
	Limit           int
	MaxFileBytes    int
	// Offset skips the first N matches before applying Limit. It is the
	// pagination cursor shared by local and remote grep.
	Offset int
}

type GrepMatch struct {
	File string
	Line int
	Text string
}

type GrepResult struct {
	Matches []GrepMatch
	// Files carries the page for files/count modes; Matches keeps the raw
	// matches those entries were summarized from (including the one file the
	// engines read past the page as has_more evidence).
	Files        []GrepFileSummary
	Mode         GrepMode
	FilesScanned int
	SingleFile   bool
	Truncated    bool
}

// Grep searches an authorized path with a bounded line reader. Binary-looking
// files are skipped without failing the whole search.
func Grep(ctx context.Context, opts GrepOptions) (GrepResult, error) {
	if opts.Limit < 1 {
		opts.Limit = DefaultGrepLimit
	}
	if opts.Offset < 0 {
		opts.Offset = 0
	}
	mode, err := NormalizeGrepMode(string(opts.Mode))
	if err != nil {
		return GrepResult{}, err
	}
	compiled, err := CompileGrepPattern(grepPatterns(opts), GrepPatternOptions{
		Literal:       opts.Literal,
		IgnoreCase:    opts.IgnoreCase || opts.CaseInsensitive,
		CaseSensitive: opts.CaseSensitive,
		SmartCase:     opts.SmartCase,
		Word:          opts.Word,
		LineRegexp:    opts.LineRegexp,
	})
	if err != nil {
		return GrepResult{}, err
	}
	var globMatcher *GlobMatcher
	if strings.TrimSpace(opts.Glob) != "" {
		globMatcher, err = NewGlobMatcher(opts.Glob)
		if err != nil {
			return GrepResult{}, err
		}
	}
	includeFilter, err := newGrepGlobFilter(opts.Include)
	if err != nil {
		return GrepResult{}, err
	}
	excludeFilter, err := newGrepGlobFilter(opts.Exclude)
	if err != nil {
		return GrepResult{}, err
	}

	var result GrepResult
	roots := grepRoots(opts)
	if len(roots) == 1 {
		if info, statErr := os.Stat(roots[0]); statErr == nil && info.Mode().IsRegular() {
			result.SingleFile = true
		}
	}
	reader := newLineReader()
	visit := func(root, path string) error {
		if ctx != nil && ctx.Err() != nil {
			return ctx.Err()
		}
		// The explicit root may be a symlink that the surface adapter already
		// resolved and verified. Links discovered during traversal are rejected
		// by the WalkDir callback below and never reach this visit.
		info, statErr := os.Stat(path)
		if statErr != nil || !info.Mode().IsRegular() {
			return nil
		}
		if opts.MaxFileBytes > 0 && info.Size() > int64(opts.MaxFileBytes) {
			return nil
		}
		if shouldFilterGrepCandidate(relativeGrepCandidate(root, path), includeFilter, excludeFilter) {
			return nil
		}
		result.FilesScanned++
		file, openErr := os.Open(path)
		if openErr != nil {
			return nil
		}
		defer file.Close()
		reader.reset(file)
		for {
			lineNo, text, truncated, ok := reader.next()
			if !ok {
				return nil
			}
			display := reader.displayText(text, truncated)
			if !validUTF8(display) {
				return nil
			}
			matched := compiled.Match(display)
			if matched == opts.InvertMatch {
				continue
			}
			result.Matches = append(result.Matches, GrepMatch{File: path, Line: lineNo, Text: string(display)})
			if GrepPageComplete(result.Matches, mode, opts) {
				return filepath.SkipAll
			}
		}
	}

	for _, root := range roots {
		if info, statErr := os.Stat(root); statErr == nil && info.Mode().IsRegular() {
			if globMatcher == nil || globMatcher.Match(filepath.Base(root)) {
				if err := visit(root, root); err != nil && err != filepath.SkipAll {
					return GrepResult{}, err
				}
			}
		} else {
			walkErr := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
				if err != nil {
					return nil
				}
				// File symlinks are not followed: a link inside the workspace must
				// not read a target outside the admitted roots.
				if entry.Type()&os.ModeSymlink != 0 {
					return nil
				}
				if entry.IsDir() {
					if ShouldSkipDir(entry.Name()) && path != root {
						return filepath.SkipDir
					}
					return nil
				}
				if globMatcher != nil && !globMatcher.Match(entry.Name()) {
					return nil
				}
				return visit(root, path)
			})
			if walkErr != nil && walkErr != filepath.SkipAll {
				return GrepResult{}, walkErr
			}
		}
		// Every root after the page is complete would only be scanned to be
		// discarded by ApplyGrepWindow.
		if GrepPageComplete(result.Matches, mode, opts) {
			break
		}
	}

	ApplyGrepPageWindow(&result, mode, opts)
	return result, nil
}

// grepRoots returns the ordered search roots: the single spelling first, then
// the batch spelling, exactly like the local tool builds searchPaths.
func grepRoots(opts GrepOptions) []string {
	roots := make([]string, 0, 1+len(opts.Paths))
	if strings.TrimSpace(opts.Path) != "" {
		roots = append(roots, opts.Path)
	}
	for _, path := range opts.Paths {
		if strings.TrimSpace(path) != "" {
			roots = append(roots, path)
		}
	}
	return roots
}

// relativeGrepCandidate is the path an include/exclude pattern is matched
// against: the slash path relative to the configured search root, or the bare
// file name when the root itself is the searched file.
func relativeGrepCandidate(root, path string) string {
	rel, err := filepath.Rel(root, path)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return filepath.Base(path)
	}
	return filepath.ToSlash(rel)
}

// grepPatterns returns the pattern list a GrepOptions carries, preferring the
// batch spelling exactly like the handler does.
func grepPatterns(opts GrepOptions) []string {
	if len(opts.Patterns) > 0 {
		return opts.Patterns
	}
	if opts.Pattern != "" {
		return []string{opts.Pattern}
	}
	return nil
}

// ApplyGrepWindow applies the shared offset/limit window to raw matches.
// Every grep engine (pure-Go walker, ripgrep runner) funnels its raw matches
// through this one function, so pagination cannot diverge by engine.
func ApplyGrepWindow(result *GrepResult, opts GrepOptions) {
	if result == nil {
		return
	}
	if opts.Limit < 1 {
		opts.Limit = DefaultGrepLimit
	}
	if opts.Offset < 0 {
		opts.Offset = 0
	}
	if opts.Offset > 0 {
		if opts.Offset >= len(result.Matches) {
			result.Matches = nil
			return
		}
		result.Matches = result.Matches[opts.Offset:]
	}
	if len(result.Matches) > opts.Limit {
		result.Matches = result.Matches[:opts.Limit]
		result.Truncated = true
	}
}
