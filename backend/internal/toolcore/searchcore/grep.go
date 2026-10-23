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
	Pattern         string
	Path            string
	Glob            string
	CaseInsensitive bool
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
	Matches      []GrepMatch
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
	compiled, err := CompileRegexp(opts.Pattern, opts.CaseInsensitive)
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

	var result GrepResult
	reader := newLineReader()
	visit := func(path string) error {
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
			if !compiled.Match(display) {
				continue
			}
			result.Matches = append(result.Matches, GrepMatch{File: path, Line: lineNo, Text: string(display)})
			if len(result.Matches) > opts.Offset+opts.Limit {
				return filepath.SkipAll
			}
		}
	}

	if info, statErr := os.Stat(opts.Path); statErr == nil && info.Mode().IsRegular() {
		result.SingleFile = true
		if globMatcher == nil || globMatcher.Match(filepath.Base(opts.Path)) {
			if err := visit(opts.Path); err != nil && err != filepath.SkipAll {
				return GrepResult{}, err
			}
		}
	} else {
		walkErr := filepath.WalkDir(opts.Path, func(path string, entry os.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			// File symlinks are not followed: a link inside the workspace must
			// not read a target outside the admitted roots.
			if entry.Type()&os.ModeSymlink != 0 {
				return nil
			}
			if entry.IsDir() {
				if ShouldSkipDir(entry.Name()) && path != opts.Path {
					return filepath.SkipDir
				}
				return nil
			}
			if globMatcher != nil && !globMatcher.Match(entry.Name()) {
				return nil
			}
			return visit(path)
		})
		if walkErr != nil && walkErr != filepath.SkipAll {
			return GrepResult{}, walkErr
		}
	}

	ApplyGrepWindow(&result, opts)
	return result, nil
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
