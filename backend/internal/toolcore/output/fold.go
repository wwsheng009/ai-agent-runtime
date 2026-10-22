package output

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

// FoldStats describes what a head/tail fold omitted.
type FoldStats struct {
	TotalBytes   int
	HeadBytes    int
	TailBytes    int
	OmittedStart int
	OmittedEnd   int
}

// FoldHeadTail bounds text to limit bytes, preserving both ends. The returned
// text (including its notice) never exceeds limit. Both cuts are rune-safe.
func FoldHeadTail(text string, limit int) (string, FoldStats, bool) {
	stats := FoldStats{TotalBytes: len(text)}
	if limit <= 0 || len(text) <= limit {
		stats.HeadBytes = len(text)
		return text, stats, false
	}
	notice := fmt.Sprintf("\n\n…[%d bytes omitted: output truncated at the %d-byte limit; narrow the request or read the archived capture]…\n\n", 0, limit)
	available := limit - len(notice)
	if available < 16 {
		available = limit
		notice = ""
	}
	headBudget := available / 2
	tailBudget := available - headBudget
	head := runeSafePrefix(text, headBudget)
	tail := runeSafeSuffix(text, tailBudget)
	omitted := len(text) - len(head) - len(tail)
	if omitted < 0 {
		omitted = 0
	}
	stats.HeadBytes = len(head)
	stats.TailBytes = len(tail)
	stats.OmittedStart = len(head)
	stats.OmittedEnd = len(text) - len(tail)
	if notice != "" {
		notice = fmt.Sprintf("\n\n…[%d bytes omitted: output truncated at the %d-byte limit; narrow the request or read the archived capture]…\n\n", omitted, limit)
	}
	folded := head + notice + tail
	if len(folded) > limit {
		folded = runeSafePrefix(folded, limit)
		stats.TailBytes = 0
	}
	return folded, stats, true
}

// ShapeText applies the tool-owned output window and stamps the metadata that
// tells downstream render layers this body is already final.
func ShapeText(tool, content string, metadata map[string]any, outputBudget, remoteHardCap int) (string, map[string]any, FoldStats, bool) {
	budget := ResolveCap(tool, 0, outputBudget, remoteHardCap)
	if metadata == nil {
		metadata = map[string]any{}
	}
	folded, stats, didFold := FoldHeadTail(content, budget)
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
	} else if _, exists := metadata[KeyIsTruncated]; !exists {
		metadata[KeyIsTruncated] = false
	}
	return folded, metadata, stats, didFold
}

func runeSafePrefix(text string, limit int) string {
	if limit <= 0 {
		return ""
	}
	if limit >= len(text) {
		return text
	}
	cut := limit
	for cut > 0 && !utf8.RuneStart(text[cut]) {
		cut--
	}
	return text[:cut]
}

func runeSafeSuffix(text string, limit int) string {
	if limit <= 0 {
		return ""
	}
	if limit >= len(text) {
		return text
	}
	start := len(text) - limit
	for start < len(text) && !utf8.RuneStart(text[start]) {
		start++
	}
	return text[start:]
}

// TrimToBytes is a rune-safe prefix used for notices and test helpers.
func TrimToBytes(text string, limit int) string {
	return runeSafePrefix(text, limit)
}

// JoinNotice appends a notice without exceeding budget when possible.
func JoinNotice(body, notice string, budget int) string {
	if strings.TrimSpace(notice) == "" {
		return body
	}
	if budget <= 0 || len(body)+len(notice) <= budget {
		return body + notice
	}
	remaining := budget - len(notice)
	if remaining <= 0 {
		return TrimToBytes(notice, budget)
	}
	return TrimToBytes(body, remaining) + notice
}
