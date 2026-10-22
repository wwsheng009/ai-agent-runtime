package output

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

// ShellWindowStats describes the head+tail window a shell fold produced.
// OmittedStart/OmittedEnd delimit the middle the model did not see: exactly the
// byte range artifact_read must page.
type ShellWindowStats struct {
	HeadBytes    int
	TailBytes    int
	OmittedStart int
	OmittedEnd   int
	TotalBytes   int
	TotalLines   int
}

// FoldShellWindow bounds a shell capture to budget bytes as a head+tail window
// and reports the fold arithmetic.
//
// Both ends are cut on rune boundaries (the tail additionally prefers a line
// start), and the notice is charged against the same window: the model receives
// head+notice+tail, so the budget bounds all three together. The tail is what
// makes the fold usable without an extra round trip - shell failures, test
// verdicts and "N passed" summaries live at the end - while the omitted middle
// stays addressable through the archived capture.
//
// pageBytes is the artifact_read page size, used to price the omitted middle.
func FoldShellWindow(text string, budget, pageBytes int) (string, ShellWindowStats, bool) {
	stats := ShellWindowStats{TotalBytes: len(text)}
	if budget <= 0 || len(text) <= budget {
		stats.HeadBytes = len(text)
		stats.OmittedStart = len(text)
		stats.OmittedEnd = len(text)
		stats.TotalLines = lineCount(text)
		return text, stats, false
	}
	totalBytes := len(text)
	stats.TotalLines = lineCount(text)

	contentBudget := budget
	for attempt := 0; attempt < 8; attempt++ {
		if contentBudget <= 0 {
			break
		}
		headBudget := contentBudget / 2
		tailBudget := contentBudget - headBudget
		head := safeHeadByBytes(text, headBudget)
		tail := safeTailByBytes(text, tailBudget)
		omittedStart := len(head)
		omittedEnd := totalBytes - len(tail)
		if omittedEnd < omittedStart {
			omittedEnd = omittedStart
		}
		notice := shellWindowNotice(omittedStart, omittedEnd, totalBytes, stats.TotalLines, pageBytes)
		if len(head)+len(tail)+len(notice) <= budget {
			stats.HeadBytes = len(head)
			stats.TailBytes = len(tail)
			stats.OmittedStart = omittedStart
			stats.OmittedEnd = omittedEnd
			return head + notice + tail, stats, true
		}
		// The notice grows with the digit count of its numbers; shrink the
		// content window by the overflow and try again (converges because a
		// shorter window never lengthens the notice).
		overflow := len(head) + len(tail) + len(notice) - budget
		next := contentBudget - overflow
		if next >= contentBudget {
			next = contentBudget - 1
		}
		if next < 0 {
			next = 0
		}
		contentBudget = next
	}
	// Degenerate window (notice alone exceeds the budget): keep the notice,
	// which carries the recovery route, and drop head/tail.
	notice := shellWindowNotice(0, totalBytes, totalBytes, stats.TotalLines, pageBytes)
	stats.OmittedStart = 0
	stats.OmittedEnd = totalBytes
	return safeHeadByBytes(notice, budget), stats, true
}

// ShapeShellWindow folds a shell payload to the tool window and stamps the
// ownership/window metadata the render layer and artifact gateway consume.
func ShapeShellWindow(tool, content string, metadata map[string]any, outputBudget, remoteHardCap, pageBytes int) (string, map[string]any, ShellWindowStats, bool) {
	budget := ResolveCap(tool, 0, outputBudget, remoteHardCap)
	if metadata == nil {
		metadata = map[string]any{}
	}
	folded, stats, didFold := FoldShellWindow(content, budget, pageBytes)
	metadata[KeySkipRenderTruncation] = true
	metadata[KeyModelVisibleBudget] = budget
	if didFold {
		metadata[KeyIsTruncated] = true
		metadata[KeyTruncated] = true
		metadata["output_window_bytes"] = budget
		metadata["output_window_total_bytes"] = stats.TotalBytes
		metadata["output_window_head_bytes"] = stats.HeadBytes
		metadata["output_window_tail_bytes"] = stats.TailBytes
		metadata["output_window_omitted_start"] = stats.OmittedStart
		metadata["output_window_omitted_end"] = stats.OmittedEnd
		metadata["output_window_omitted_bytes"] = stats.OmittedEnd - stats.OmittedStart
		if pageBytes > 0 {
			omitted := stats.OmittedEnd - stats.OmittedStart
			metadata["output_window_middle_pages"] = (omitted + pageBytes - 1) / pageBytes
		}
	} else if _, exists := metadata[KeyIsTruncated]; !exists {
		metadata[KeyIsTruncated] = false
	}
	return folded, metadata, stats, didFold
}

func shellWindowNotice(omittedStart, omittedEnd, totalBytes, totalLines, pageBytes int) string {
	omitted := omittedEnd - omittedStart
	if omitted < 0 {
		omitted = 0
	}
	pages := 0
	if omitted > 0 && pageBytes > 0 {
		pages = (omitted + pageBytes - 1) / pageBytes
	}
	middleOffset := omittedStart + omitted/2
	return fmt.Sprintf(
		"\n\n[output window: showing head %d B + tail %d B of %d bytes of the captured output (%d lines total); "+
			"omitted middle bytes [%d,%d) = %d bytes. "+
			"The complete capture is archived: read the middle with artifact_read on the raw-output pointer below - "+
			"offset=%d, limit=%d, then follow next_offset (the whole middle costs %d pages), or bisect from offset=%d. "+
			"Cheapest route when you know what to look for: re-run a narrower command (tighter pattern, Select-String / Select-Object -First/-Last) "+
			"instead of paging the whole capture.]",
		omittedStart, totalBytes-omittedEnd, totalBytes, totalLines,
		omittedStart, omittedEnd, omitted,
		omittedStart, pageBytes, pages, middleOffset,
	)
}

func lineCount(text string) int {
	if text == "" {
		return 0
	}
	return strings.Count(strings.ReplaceAll(text, "\r\n", "\n"), "\n") + 1
}

// safeHeadByBytes returns the longest prefix of text within limit bytes that
// does not split a UTF-8 sequence.
func safeHeadByBytes(text string, limit int) string {
	if limit <= 0 {
		return ""
	}
	if len(text) <= limit {
		return text
	}
	cut := limit
	for cut > 0 && !utf8.RuneStart(text[cut]) {
		cut--
	}
	return text[:cut]
}

// safeTailByBytes returns the longest suffix of text within limit bytes that
// does not split a UTF-8 sequence. It prefers to start right after a newline
// so the model does not receive a half line first, as long as trimming to that
// line start keeps at least half of the tail window.
func safeTailByBytes(text string, limit int) string {
	if limit <= 0 {
		return ""
	}
	if len(text) <= limit {
		return text
	}
	start := len(text) - limit
	for start < len(text) && !utf8.RuneStart(text[start]) {
		start++
	}
	if idx := strings.IndexByte(text[start:], '\n'); idx >= 0 && idx < 512 {
		candidate := start + idx + 1
		if len(text)-candidate >= limit/2 {
			start = candidate
		}
	}
	return text[start:]
}
