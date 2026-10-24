package searchcore

// GrepStats summarizes one call's returned page plus its scan work. Deliberately
// page-level: the engines stop one entry past the page, so a "whole search"
// statistic cannot be produced without a second full scan, and reporting one
// would silently disagree with the matches the caller received.
type GrepStats struct {
	// Matches counts the returned occurrences: matching lines in files/count
	// modes, or entries (fragments with only_matching) in content mode.
	Matches int
	// MatchedLines counts distinct file:line pairs in the page.
	MatchedLines int
	// FilesWithMatched counts distinct files in the page.
	FilesWithMatched int
	// FilesScanned is the whole-call scan counter, not a page statistic.
	FilesScanned int
}

// SummarizeGrepResult reduces a windowed result to its page statistics. For
// files/count modes the raw match list still carries the one file read past the
// page as has_more evidence, so those entries are excluded before counting.
func SummarizeGrepResult(result GrepResult) GrepStats {
	stats := GrepStats{FilesScanned: result.FilesScanned}
	if result.Mode != GrepModeContent {
		pageFiles := make(map[string]bool, len(result.Files))
		for _, entry := range result.Files {
			pageFiles[entry.File] = true
		}
		lines := make(map[GrepLineKey]bool, len(result.Matches))
		for _, match := range result.Matches {
			if !pageFiles[match.File] {
				continue
			}
			stats.Matches++
			lines[GrepLineKey{File: match.File, Line: match.Line}] = true
		}
		stats.MatchedLines = len(lines)
		stats.FilesWithMatched = len(result.Files)
		return stats
	}

	lines := make(map[GrepLineKey]bool, len(result.Matches))
	files := make(map[string]bool, len(result.Matches))
	for _, match := range result.Matches {
		lines[GrepLineKey{File: match.File, Line: match.Line}] = true
		files[match.File] = true
	}
	stats.Matches = len(result.Matches)
	stats.MatchedLines = len(lines)
	stats.FilesWithMatched = len(files)
	return stats
}

// GrepLineKey identifies one source line, so fragments of the same line count
// once in matched_lines.
type GrepLineKey struct {
	File string
	Line int
}
