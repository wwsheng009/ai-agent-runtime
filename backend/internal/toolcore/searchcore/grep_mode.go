package searchcore

import (
	"fmt"
	"strings"
)

// GrepMode selects the output unit of a search. It is part of the shared
// contract: the engines stop collecting at a mode-aware boundary (matches for
// content, distinct files for files/count), so the page and its has_more bit
// mean the same thing on every surface.
type GrepMode string

const (
	// GrepModeContent lists matching lines (the default).
	GrepModeContent GrepMode = "content"
	// GrepModeFiles lists the files containing at least one match.
	GrepModeFiles GrepMode = "files"
	// GrepModeCount lists one `<file>:<matching line count>` entry per file.
	GrepModeCount GrepMode = "count"
	// GrepModeFilesWithout lists the files containing no matching line. Unlike
	// files/count it has no match list to summarize: the page unit is a file
	// that the scan actively proved to be match-free, which is why the walker
	// has to read every candidate to its first match instead of stopping at a
	// page boundary.
	GrepModeFilesWithout GrepMode = "files_without"
)

// NormalizeGrepMode parses the mode spelling, including the rg-compatible
// `files_without` aliases (`--files-without-match`).
func NormalizeGrepMode(raw string) (GrepMode, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", string(GrepModeContent):
		return GrepModeContent, nil
	case string(GrepModeFiles):
		return GrepModeFiles, nil
	case string(GrepModeCount):
		return GrepModeCount, nil
	case "files_without", "files-without", "files_without_match", "files_without_matches":
		return GrepModeFilesWithout, nil
	default:
		return "", fmt.Errorf("unknown mode %q (supported: content, files, files_without, count)", raw)
	}
}

// GrepFileSummary is one file of a files/count page. Count is the number of
// matching lines; it is complete because the engines stop one file past the
// page.
type GrepFileSummary struct {
	File  string
	Count int
}

// GrepPageComplete reports whether the raw matches already cover the page plus
// the one extra entry that proves has_more. In content mode the entry is a
// match; in files/count mode it is a whole file, which is also what makes the
// page's last count trustworthy.
func GrepPageComplete(matches []GrepMatch, mode GrepMode, opts GrepOptions) bool {
	limit := opts.Limit
	if limit < 1 {
		limit = DefaultGrepLimit
	}
	offset := opts.Offset
	if offset < 0 {
		offset = 0
	}
	if mode == GrepModeContent {
		return len(matches) > offset+limit
	}
	return CountDistinctGrepFiles(matches) > offset+limit
}

// CountDistinctGrepFiles counts the files represented in the match list.
// Matches must be grouped by file, which every engine guarantees (the walker
// visits file by file, ripgrep streams in path-sorted order, and the multi-root
// runner concatenates whole per-root results).
func CountDistinctGrepFiles(matches []GrepMatch) int {
	count := 0
	for i, match := range matches {
		if i == 0 || matches[i-1].File != match.File {
			count++
		}
	}
	return count
}

// ApplyGrepPageWindow applies the mode-appropriate window: match pagination for
// content, file pagination for files/count.
func ApplyGrepPageWindow(result *GrepResult, mode GrepMode, opts GrepOptions) {
	if result == nil {
		return
	}
	result.Mode = mode
	switch mode {
	case GrepModeContent:
		ApplyGrepWindow(result, opts)
	case GrepModeFilesWithout:
		// engines fill Files directly: there is no match list to group.
		applyGrepFileEntriesWindow(result, mode, opts, result.Files)
	default:
		ApplyGrepFileWindow(result, mode, opts)
	}
}

// GrepFilesWithoutComplete reports whether the collected no-match files already
// fill the page plus the one extra entry that proves has_more.
func GrepFilesWithoutComplete(files []GrepFileSummary, opts GrepOptions) bool {
	limit := opts.Limit
	if limit < 1 {
		limit = DefaultGrepLimit
	}
	offset := opts.Offset
	if offset < 0 {
		offset = 0
	}
	return len(files) > offset+limit
}

// GrepResultPageComplete reports whether the collected result already covers
// the page plus the evidence entry for its mode. It is the one stop condition
// the engines and the walker share, so a page means the same thing everywhere.
func GrepResultPageComplete(result *GrepResult, mode GrepMode, opts GrepOptions) bool {
	if result == nil {
		return false
	}
	if mode == GrepModeFilesWithout {
		return GrepFilesWithoutComplete(result.Files, opts)
	}
	return GrepPageComplete(result.Matches, mode, opts)
}

// ApplyGrepFileWindow reduces raw matches to the page of files for files/count
// modes. Entries keep first-match order and only files with complete counts are
// emitted.
func ApplyGrepFileWindow(result *GrepResult, mode GrepMode, opts GrepOptions) {
	applyGrepFileEntriesWindow(result, mode, opts, SummarizeGrepFiles(result.Matches))
}

// applyGrepFileEntriesWindow slices a file-entry list to the requested page. It
// is the single window for every file-valued mode: files/count feed it grouped
// matches, files_without feeds the proven no-match files it collected.
func applyGrepFileEntriesWindow(result *GrepResult, mode GrepMode, opts GrepOptions, entries []GrepFileSummary) {
	limit := opts.Limit
	if limit < 1 {
		limit = DefaultGrepLimit
	}
	offset := opts.Offset
	if offset < 0 {
		offset = 0
	}

	if offset > 0 {
		if offset >= len(entries) {
			entries = nil
		} else {
			entries = entries[offset:]
		}
	}
	if len(entries) > limit {
		entries = entries[:limit]
		result.Truncated = true
	}
	result.Mode = mode
	result.Files = entries
}

// SummarizeGrepFiles groups matches into per-file entries, preserving the
// first-match order of the input.
func SummarizeGrepFiles(matches []GrepMatch) []GrepFileSummary {
	entries := make([]GrepFileSummary, 0, 4)
	for _, match := range matches {
		if len(entries) > 0 && entries[len(entries)-1].File == match.File {
			entries[len(entries)-1].Count++
			continue
		}
		entries = append(entries, GrepFileSummary{File: match.File, Count: 1})
	}
	return entries
}
