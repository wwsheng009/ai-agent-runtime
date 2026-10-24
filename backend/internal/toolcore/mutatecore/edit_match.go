package mutatecore

// The matching and recovery layer below is the local edit tool's own
// implementation, moved here verbatim. It decides whether an edit lands at all
// - CRLF drift, trailing whitespace, indentation, blank-run length, token
// fallback, closest-snippet ranking - so keeping a copy on each surface would
// mean two different tools wearing one name.

import (
	"fmt"
	"strings"
)

func TruncateDiagnosticText(text string, limit int) string {
	if limit <= 0 {
		return ""
	}
	runes := []rune(text)
	if len(runes) <= limit {
		return text
	}
	return string(runes[:limit]) + "..."
}

// BuildEditOldStringNotFoundError is the model-facing diagnostic for a missed
// old_string: what was tried, where the closest current lines are, and how to
// rebuild the value without another blind retry.
func BuildEditOldStringNotFoundError(content string, oldString string) string {
	parts := []string{
		"old_string 未在文件中找到；已尝试 CRLF/LF 与行尾空白/缩进对齐，仍无法唯一定位（不会做语义模糊改写）。",
	}

	normalizedContent := NormalizeLineEndings(content)
	normalizedOld := NormalizeLineEndings(oldString)
	if normalizedContent != content || normalizedOld != oldString {
		if strings.Contains(normalizedContent, normalizedOld) {
			parts = append(parts, "检测到 old_string 在统一为 LF 换行后可以匹配，失败很可能来自 CRLF/LF 换行差异。")
		}
	}

	if closest, startLine := FindClosestEditSnippetWithLine(normalizedContent, normalizedOld); closest != "" {
		if startLine > 0 {
			parts = append(parts, fmt.Sprintf("建议从第 %d 行附近用 view 重读（suggested_view_offset=%d）。", startLine, startLine-1))
		}
		// Multi-line block (like apply_patch) so the model can copy exact current text.
		parts = append(parts, fmt.Sprintf(
			"最接近的当前内容（第 %d 行附近，可直接据此重建 old_string）:\n%s",
			startLine,
			FormatEditClosestLines(closest, startLine),
		))
	}

	parts = append(parts,
		"next_action: 优先用上方“最接近的当前内容”作为 edit.old_string 的精确来源（去掉行号前缀后复制）；必要时再 view/grep 确认。代码编辑、多行替换或上下文可能变化时优先使用 apply_patch，并在 @@ 中提供靠近目标的函数/类上下文。不要原样重试同一 stale old_string。",
		fmt.Sprintf("old_string 预览: %q", TruncateDiagnosticText(oldString, 200)),
	)
	return strings.Join(parts, "\n")
}

// FormatEditSuccessContent renders a mutation result together with its unified
// diff, so a caller can confirm the edit it asked for is the edit that landed.
func FormatEditSuccessContent(message string, patch string) string {
	message = strings.TrimSpace(message)
	patch = strings.TrimRight(patch, "\n")
	if strings.TrimSpace(patch) == "" {
		return message + "\n\n文件差异:\n无内容变化"
	}
	return message + "\n\n文件差异:\n```diff\n" + patch + "\n```"
}

func FormatEditClosestLines(snippet string, startLine int) string {
	lines := strings.Split(NormalizeLineEndings(snippet), "\n")
	const maxLines = 16
	limit := len(lines)
	if limit > maxLines {
		limit = maxLines
	}
	out := make([]string, 0, limit+1)
	for i := 0; i < limit; i++ {
		lineNo := startLine + i
		if lineNo <= 0 {
			lineNo = i + 1
		}
		// Keep full line text (no mid-line truncation) so models can copy exact
		// old_string bytes including indent; only cap total line count.
		out = append(out, fmt.Sprintf("%6d|%s", lineNo, lines[i]))
	}
	if len(lines) > limit {
		out = append(out, fmt.Sprintf("... 省略 %d 行", len(lines)-limit))
	}
	return strings.Join(out, "\n")
}

func StaleEditContextNextAction() string {
	return "STALE_CONTEXT: copy exact lines from current_snippet / “最接近的当前内容” (or re-view with suggested_view_offset), rebuild a short confirmed old_string from that text, then retry; for multi-line or drifting context prefer apply_patch. Do not retry the same stale old_string unchanged."
}

// EditAmbiguousLineSample 是歧义报错里列出的命中行号上限：只做定位提示，
// 命中数量本身仍按实际计数完整回报。
// EditAmbiguousLineSample is how many hit lines an ambiguity diagnostic lists.
const EditAmbiguousLineSample = 20

// EditOccurrenceLineNumbers 返回 oldString 每处命中所在的 1-based 行号（最多 max 个）。
func EditOccurrenceLineNumbers(content, oldString string, max int) []int {
	if oldString == "" {
		return nil
	}
	lines := make([]int, 0, 4)
	offset := 0
	for {
		index := strings.Index(content[offset:], oldString)
		if index < 0 {
			break
		}
		absolute := offset + index
		lines = append(lines, 1+strings.Count(content[:absolute], "\n"))
		if max > 0 && len(lines) >= max {
			break
		}
		offset = absolute + len(oldString)
	}
	return lines
}

// FormatEditOccurrenceLines 渲染命中行号列表；样本被截断时补省略号。
func FormatEditOccurrenceLines(lines []int, total int) string {
	if len(lines) == 0 {
		return "未知"
	}
	parts := make([]string, 0, len(lines)+1)
	for _, line := range lines {
		parts = append(parts, fmt.Sprintf("%d", line))
	}
	if total > len(lines) {
		parts = append(parts, "…")
	}
	return strings.Join(parts, "、")
}

// ReplaceFirstEditOccurrences 只替换前 count 处命中（count <= 0 时原样返回）。
func ReplaceFirstEditOccurrences(content, oldString, newString string, count int) string {
	if count <= 0 || oldString == "" {
		return content
	}
	var builder strings.Builder
	remaining := count
	offset := 0
	for remaining > 0 {
		index := strings.Index(content[offset:], oldString)
		if index < 0 {
			break
		}
		absolute := offset + index
		builder.WriteString(content[offset:absolute])
		builder.WriteString(newString)
		offset = absolute + len(oldString)
		remaining--
	}
	builder.WriteString(content[offset:])
	return builder.String()
}

// MatchEditStrings tries exact then line-ending-normalized matches so models
// recover from CRLF/LF drift without a retry. Returns the exact substrings to
// use for Replace (preserving the file's newline style when possible).
func MatchEditStrings(content, oldString, newString string) (matchedOld, matchedNew string, ok bool) {
	if oldString == "" {
		return "", "", false
	}
	if strings.Contains(content, oldString) {
		return oldString, newString, true
	}

	normalizedOld := NormalizeLineEndings(oldString)
	normalizedNew := NormalizeLineEndings(newString)
	if normalizedOld == "" {
		return "", "", false
	}

	// LF old_string against CRLF file content.
	crlfOld := strings.ReplaceAll(normalizedOld, "\n", "\r\n")
	if crlfOld != oldString && strings.Contains(content, crlfOld) {
		return crlfOld, strings.ReplaceAll(normalizedNew, "\n", "\r\n"), true
	}

	// CRLF old_string against LF file content.
	if normalizedOld != oldString && strings.Contains(content, normalizedOld) {
		return normalizedOld, normalizedNew, true
	}

	// Whitespace/indent-tolerant match (same recovery class as apply_patch line
	// matchers). Prefer the file's exact bytes so replace cannot invent whitespace.
	if matchedOld, matchedNew, ok = MatchEditStringsWhitespaceTolerant(content, normalizedOld, normalizedNew); ok {
		return matchedOld, matchedNew, true
	}

	return "", "", false
}

// MatchEditStringsWhitespaceTolerant finds old_string in content when the only
// differences are trailing spaces, leading indent, internal column padding,
// or blank-line run length between the same significant lines.
// On success it returns the exact file substring and a new_string rewritten to
// use the file's indent style for the matched window.
func MatchEditStringsWhitespaceTolerant(content, oldLF, newLF string) (matchedOld, matchedNew string, ok bool) {
	contentLF := NormalizeLineEndings(content)
	oldLines := strings.Split(oldLF, "\n")
	// Drop a single trailing empty line from old so "foo\n" still matches "foo"
	// without requiring an extra blank line in the file.
	if len(oldLines) > 1 && oldLines[len(oldLines)-1] == "" {
		oldLines = oldLines[:len(oldLines)-1]
	}
	if len(oldLines) == 0 {
		return "", "", false
	}

	contentLines := strings.Split(contentLF, "\n")
	if len(oldLines) > len(contentLines) {
		return "", "", false
	}

	shared := LineMatchers()
	matchers := make([]func(actual, expected string) bool, 0, len(shared))
	for _, matcher := range shared {
		matchers = append(matchers, func(actual, expected string) bool {
			return matcher(actual, expected)
		})
	}

	start := -1
	for _, matcher := range matchers {
		for i := 0; i+len(oldLines) <= len(contentLines); i++ {
			window := contentLines[i : i+len(oldLines)]
			if SliceMatches(window, oldLines, LineMatcher(matcher)) {
				// Prefer unique windows to avoid wrong-site edits.
				if start >= 0 {
					return "", "", false
				}
				start = i
			}
		}
		if start >= 0 {
			break
		}
	}
	var matchedLines []string
	if start >= 0 {
		matchedLines = contentLines[start : start+len(oldLines)]
	} else {
		// Fixed-length window failed: allow blank-run length drift between the
		// same non-blank lines (models often invent 3 vs 4 blank lines).
		var okBlank bool
		matchedLines, okBlank = MatchEditBlankRunWindow(contentLines, oldLines)
		if !okBlank {
			return "", "", false
		}
	}
	matchedOldLF := strings.Join(matchedLines, "\n")

	// Rebuild replacement from the file window so unchanged lines keep exact
	// file bytes (column padding / blank runs), while changed bodies inherit
	// file indent.
	matchedNewLF := RebuildEditReplacement(matchedLines, oldLines, newLF)

	// Preserve original CRLF when the file uses it.
	if strings.Contains(content, "\r\n") {
		return strings.ReplaceAll(matchedOldLF, "\n", "\r\n"),
			strings.ReplaceAll(matchedNewLF, "\n", "\r\n"),
			true
	}
	return matchedOldLF, matchedNewLF, true
}

// MatchEditBlankRunWindow finds a unique content span whose non-blank lines
// match oldLines' non-blank lines under collapse-ws comparison. Blank runs
// between those lines may differ in length.
func MatchEditBlankRunWindow(contentLines, oldLines []string) ([]string, bool) {
	start, end, ok := locateBlankRunLineSpan(contentLines, oldLines)
	if !ok {
		return nil, false
	}
	return contentLines[start : end+1], true
}

// locateBlankRunLineSpan returns the unique [start,end] inclusive span in
// contentLines whose non-blank sequence matches oldLines under collapse-ws.
// Shared by edit and apply_patch so blank-run drift heals consistently.
func locateBlankRunLineSpan(contentLines, oldLines []string) (start, end int, ok bool) {
	oldNonBlank := NonBlankLines(oldLines)
	if len(oldNonBlank) < 2 {
		// Single significant line is already covered by fixed-window matchers.
		return 0, 0, false
	}

	type span struct{ start, end int } // inclusive end
	var found []span
	for i := 0; i < len(contentLines); i++ {
		if strings.TrimSpace(contentLines[i]) == "" {
			continue
		}
		if NormalizeComparableLine(contentLines[i]) != NormalizeComparableLine(oldNonBlank[0]) {
			continue
		}
		ci := i
		matchOK := true
		for k := 1; k < len(oldNonBlank); k++ {
			ci++
			for ci < len(contentLines) && strings.TrimSpace(contentLines[ci]) == "" {
				ci++
			}
			if ci >= len(contentLines) ||
				NormalizeComparableLine(contentLines[ci]) != NormalizeComparableLine(oldNonBlank[k]) {
				matchOK = false
				break
			}
		}
		if matchOK {
			found = append(found, span{start: i, end: ci})
			if len(found) > 1 {
				return 0, 0, false
			}
		}
	}
	if len(found) != 1 {
		return 0, 0, false
	}
	s := found[0]
	return s.start, s.end, true
}

// RebuildEditReplacement constructs the replacement text from the file window
// and the model's new_string. Unchanged lines (under collapse-ws) keep the
// file's exact bytes so column padding and blank runs are preserved; changed
// or inserted lines inherit file indent from the corresponding old/file line.
func RebuildEditReplacement(matchedLines, oldLines []string, newLF string) string {
	if newLF == "" {
		return newLF
	}
	// Equal-length windows: map line-by-line when possible.
	if len(matchedLines) == len(oldLines) {
		return alignEditReplacementIndentPreserveExact(matchedLines, oldLines, newLF)
	}
	// Blank-run flexible window: preserve file blank runs and rewrite only
	// non-blank bodies that the model actually changed.
	return alignEditReplacementNonBlank(matchedLines, oldLines, newLF)
}

// alignEditReplacementIndentPreserveExact is like alignEditReplacementIndent
// but keeps the file line verbatim when the model body is collapse-ws-equal
// to the matched old body (so column padding is not flattened).
func alignEditReplacementIndentPreserveExact(matchedLines, oldLines []string, newLF string) string {
	if newLF == "" {
		return newLF
	}
	newLines := strings.Split(newLF, "\n")
	trailingNL := strings.HasSuffix(newLF, "\n")
	if trailingNL && len(newLines) > 0 && newLines[len(newLines)-1] == "" {
		newLines = newLines[:len(newLines)-1]
	}

	out := make([]string, len(newLines))
	for i, line := range newLines {
		if strings.TrimSpace(line) == "" {
			// Prefer file blank line when window still aligns by index.
			if i < len(matchedLines) && strings.TrimSpace(matchedLines[i]) == "" {
				out[i] = matchedLines[i]
			} else {
				out[i] = line
			}
			continue
		}
		newWS := LeadingWhitespace(line)
		body := strings.TrimPrefix(line, newWS)

		if i < len(oldLines) && i < len(matchedLines) {
			// If model body equals matched file body under collapse-ws, keep
			// the file line bytes (column padding / fancy punctuation).
			if NormalizeComparableLine(line) == NormalizeComparableLine(matchedLines[i]) {
				out[i] = matchedLines[i]
				continue
			}
			// Also: if model new equals model old body (no semantic change)
			// keep file line even when new collapsed form differs only in pad.
			if NormalizeComparableLine(oldLines[i]) == NormalizeComparableLine(line) {
				out[i] = matchedLines[i]
				continue
			}

			oldWS := LeadingWhitespace(oldLines[i])
			fileWS := LeadingWhitespace(matchedLines[i])
			rel := newWS
			if oldWS != "" && strings.HasPrefix(newWS, oldWS) {
				rel = strings.TrimPrefix(newWS, oldWS)
			} else if newWS == oldWS {
				rel = ""
			} else if oldWS == "" {
				rel = newWS
			} else {
				out[i] = line
				continue
			}
			out[i] = fileWS + rel + body
			continue
		}

		if len(matchedLines) > 0 && len(oldLines) > 0 {
			fileWS0 := LeadingWhitespace(matchedLines[0])
			oldWS0 := LeadingWhitespace(oldLines[0])
			if oldWS0 == "" && fileWS0 != "" && !strings.HasPrefix(newWS, fileWS0) {
				out[i] = fileWS0 + line
				continue
			}
		}
		out[i] = line
	}

	joined := strings.Join(out, "\n")
	if trailingNL {
		joined += "\n"
	}
	return joined
}

// alignEditReplacementNonBlank rewrites newLF by mapping non-blank lines onto
// the file window's non-blank indents. Blank runs keep the *file* shape (not
// the model's invented blank count) when the model only renames a significant
// line; extra model blank lines between non-blank pairs are not inserted.
func alignEditReplacementNonBlank(matchedLines, oldLines []string, newLF string) string {
	if newLF == "" {
		return newLF
	}
	fileNB := NonBlankLines(matchedLines)
	oldNB := NonBlankLines(oldLines)
	newLines := strings.Split(newLF, "\n")
	trailingNL := strings.HasSuffix(newLF, "\n")
	if trailingNL && len(newLines) > 0 && newLines[len(newLines)-1] == "" {
		newLines = newLines[:len(newLines)-1]
	}
	newNB := NonBlankLines(newLines)

	// Build output by walking the file window structure: keep blank runs from
	// the file, rewrite non-blank lines from the model's non-blank sequence.
	if len(newNB) == 0 {
		return newLF
	}

	// Prefer file-shaped reconstruction when non-blank counts match old/file.
	if len(newNB) == len(fileNB) && len(oldNB) == len(fileNB) {
		out := make([]string, 0, len(matchedLines)+2)
		nbIdx := 0
		for _, fileLine := range matchedLines {
			if strings.TrimSpace(fileLine) == "" {
				out = append(out, fileLine)
				continue
			}
			if nbIdx >= len(newNB) {
				out = append(out, fileLine)
				continue
			}
			modelLine := newNB[nbIdx]
			// Unchanged under collapse-ws → keep file exact bytes.
			if NormalizeComparableLine(modelLine) == NormalizeComparableLine(fileLine) ||
				NormalizeComparableLine(modelLine) == NormalizeComparableLine(oldNB[nbIdx]) {
				out = append(out, fileLine)
			} else {
				newWS := LeadingWhitespace(modelLine)
				body := strings.TrimPrefix(modelLine, newWS)
				oldWS := LeadingWhitespace(oldNB[nbIdx])
				fileWS := LeadingWhitespace(fileLine)
				rel := newWS
				if oldWS != "" && strings.HasPrefix(newWS, oldWS) {
					rel = strings.TrimPrefix(newWS, oldWS)
				} else if newWS == oldWS {
					rel = ""
				} else if oldWS == "" {
					rel = newWS
				} else {
					out = append(out, modelLine)
					nbIdx++
					continue
				}
				out = append(out, fileWS+rel+body)
			}
			nbIdx++
		}
		// Append any extra model non-blank lines beyond the file window.
		for ; nbIdx < len(newNB); nbIdx++ {
			line := newNB[nbIdx]
			if len(fileNB) > 0 {
				fileWS0 := LeadingWhitespace(fileNB[0])
				oldWS0 := ""
				if len(oldNB) > 0 {
					oldWS0 = LeadingWhitespace(oldNB[0])
				}
				newWS := LeadingWhitespace(line)
				if oldWS0 == "" && fileWS0 != "" && !strings.HasPrefix(newWS, fileWS0) {
					out = append(out, fileWS0+line)
					continue
				}
			}
			out = append(out, line)
		}
		joined := strings.Join(out, "\n")
		if trailingNL {
			joined += "\n"
		}
		return joined
	}

	// Fallback: indent-align model lines without reshaping blank runs.
	return alignEditReplacementIndent(matchedLines, oldLines, newLF)
}

// alignEditReplacementIndent rewrites newLF so relative indent changes from the
// model are applied on top of the file's actual leading whitespace for the
// matched window. Absolute-looking first lines (no indent in both old and new)
// inherit the file first-line indent.
func alignEditReplacementIndent(matchedLines, oldLines []string, newLF string) string {
	if newLF == "" {
		return newLF
	}
	newLines := strings.Split(newLF, "\n")
	// Preserve whether replacement ends with a trailing newline.
	trailingNL := strings.HasSuffix(newLF, "\n")
	if trailingNL && len(newLines) > 0 && newLines[len(newLines)-1] == "" {
		newLines = newLines[:len(newLines)-1]
	}

	out := make([]string, len(newLines))
	for i, line := range newLines {
		if strings.TrimSpace(line) == "" {
			out[i] = line
			continue
		}
		newWS := LeadingWhitespace(line)
		body := strings.TrimPrefix(line, newWS)

		// Prefer per-line remap when old/file windows still align by index.
		if i < len(oldLines) && i < len(matchedLines) {
			oldWS := LeadingWhitespace(oldLines[i])
			fileWS := LeadingWhitespace(matchedLines[i])
			rel := newWS
			if oldWS != "" && strings.HasPrefix(newWS, oldWS) {
				rel = strings.TrimPrefix(newWS, oldWS)
			} else if newWS == oldWS {
				rel = ""
			} else if oldWS == "" {
				// Model dropped outer indent on this line; keep new's relative
				// indent on top of the file line's indent.
				rel = newWS
			} else {
				// Unrelated indent styles — keep model line unchanged.
				out[i] = line
				continue
			}
			out[i] = fileWS + rel + body
			continue
		}

		// Extra new lines beyond the matched window: inherit first-line file
		// indent delta when old first line lacked indent.
		if len(matchedLines) > 0 && len(oldLines) > 0 {
			fileWS0 := LeadingWhitespace(matchedLines[0])
			oldWS0 := LeadingWhitespace(oldLines[0])
			if oldWS0 == "" && fileWS0 != "" && !strings.HasPrefix(newWS, fileWS0) {
				out[i] = fileWS0 + line
				continue
			}
		}
		out[i] = line
	}

	joined := strings.Join(out, "\n")
	if trailingNL {
		joined += "\n"
	}
	return joined
}

func FindClosestEditSnippet(content, oldString string) string {
	snippet, _ := FindClosestEditSnippetWithLine(content, oldString)
	return snippet
}

// FindClosestEditSnippetLine returns a 1-based line number near the closest
// current content for a missed old_string, or 0 when no useful hint exists.
func FindClosestEditSnippetLine(content, oldString string) int {
	_, startLine := FindClosestEditSnippetWithLine(
		NormalizeLineEndings(content),
		NormalizeLineEndings(oldString),
	)
	return startLine
}

func FindClosestEditSnippetWithLine(content, oldString string) (string, int) {
	// Do not TrimSpace the whole file: leading blanks must keep stable 1-based
	// line numbers for suggested_view_offset / current_snippet_start_line.
	content = NormalizeLineEndings(content)
	oldString = strings.TrimSpace(NormalizeLineEndings(oldString))
	if strings.TrimSpace(content) == "" || oldString == "" {
		return "", 0
	}
	oldLines := strings.Split(oldString, "\n")
	// Drop a single trailing empty line so "foo\n" windows match "foo".
	if len(oldLines) > 1 && oldLines[len(oldLines)-1] == "" {
		oldLines = oldLines[:len(oldLines)-1]
	}
	if len(NonBlankLines(oldLines)) == 0 {
		return "", 0
	}
	contentLines := strings.Split(content, "\n")
	bestIdx := -1
	bestEnd := -1
	bestScore := 0.0

	// Prefer multi-line window alignment. Scoring only the first old line often
	// anchors on generic tokens (import (, }, return) far from the real drift.
	maxStart := len(contentLines)
	if len(oldLines) <= len(contentLines) {
		maxStart = len(contentLines) - len(oldLines) + 1
	}
	for i := 0; i < maxStart; i++ {
		end := i + len(oldLines)
		if end > len(contentLines) {
			end = len(contentLines)
		}
		score := scoreEditClosestWindow(contentLines[i:end], oldLines)
		if score > bestScore {
			bestScore = score
			bestIdx = i
			bestEnd = end
		}
	}

	// Also try anchoring each non-blank old line against content lines so a
	// drifted first line still recovers when a later distinctive line matches.
	// Align from the strong line forward (do not force weak old prefix lines
	// into the window — that mis-anchors recovery on ")" / blanks).
	for oldIdx, oldLine := range oldLines {
		if strings.TrimSpace(oldLine) == "" {
			continue
		}
		tailOld := oldLines[oldIdx:]
		for contentIdx := range contentLines {
			lineScore := editClosestLineScore(contentLines[contentIdx], oldLine)
			if lineScore < 0.55 {
				continue
			}
			end := contentIdx + len(tailOld)
			if end > len(contentLines) {
				end = len(contentLines)
			}
			if end <= contentIdx {
				continue
			}
			score := scoreEditClosestWindow(contentLines[contentIdx:end], tailOld)
			// Boost windows that contain a strong single-line anchor.
			score += 0.08 * lineScore
			if score > bestScore {
				bestScore = score
				bestIdx = contentIdx
				bestEnd = end
			}
		}
	}

	if bestIdx >= 0 && bestEnd > bestIdx && bestScore >= 0.45 {
		// Late-line anchors align a full old_string-sized window that may start on
		// weak prefix lines (")", blank). Tighten to the strong-matching core so
		// current_snippet / suggested_view_offset point at the real drift site.
		window := contentLines[bestIdx:bestEnd]
		from, to := tightenClosestWindowBounds(window, oldLines)
		if to > from {
			return strings.Join(window[from:to], "\n"), bestIdx + from + 1
		}
		return strings.Join(window, "\n"), bestIdx + 1
	}

	// True content_diff residual: multi-line window score can fall below the
	// 0.45 gate when the first old line is fully gone / invented, but distinctive
	// identifiers still live nearby (e.g. selector_id / custom-tool-call-namespace).
	// Fall back to token/single-line anchors so STALE still exports a
	// copy-pasteable current_snippet instead of empty recovery (old "no closest body").
	return FindClosestEditSnippetByTokenFallback(contentLines, oldLines)
}

// FindClosestEditSnippetByTokenFallback recovers a padded current window when
// line-aligned multi-window scoring fails. It never auto-heals writes — only
// ranks a copy-paste recovery site for STALE_CONTEXT metadata / error body.
func FindClosestEditSnippetByTokenFallback(contentLines, oldLines []string) (string, int) {
	if len(contentLines) == 0 || len(oldLines) == 0 {
		return "", 0
	}
	tokens := make([]string, 0, 16)
	seenTok := make(map[string]struct{}, 16)
	for _, line := range oldLines {
		for _, tok := range editDistinctiveTokens(line) {
			key := strings.ToLower(tok)
			if _, ok := seenTok[key]; ok {
				continue
			}
			seenTok[key] = struct{}{}
			tokens = append(tokens, tok)
		}
	}
	if len(tokens) == 0 {
		// Last resort: single best line-similarity hit (parity with apply_patch).
		bestIdx := -1
		bestScore := 0.0
		for i, actual := range contentLines {
			for _, expected := range oldLines {
				if strings.TrimSpace(expected) == "" {
					continue
				}
				if score := editClosestLineScore(actual, expected); score > bestScore {
					bestScore = score
					bestIdx = i
				}
			}
		}
		if bestIdx < 0 || bestScore < 0.35 {
			return "", 0
		}
		start, end := padEditClosestWindow(len(contentLines), bestIdx, bestIdx+1)
		return strings.Join(contentLines[start:end], "\n"), start + 1
	}

	bestIdx := -1
	bestScore := 0
	bestHits := 0
	// Prefer lines that hit multiple distinctive tokens; break ties toward fewer
	// total occurrences of the same token set (less ambiguous anchors).
	lineHits := make([]int, len(contentLines))
	lineScore := make([]int, len(contentLines))
	for i, actual := range contentLines {
		lower := strings.ToLower(actual)
		hits := 0
		score := 0
		for _, tok := range tokens {
			tl := strings.ToLower(tok)
			if strings.Contains(lower, tl) {
				hits++
				// Longer / more specific tokens dominate ranking.
				w := len(tok)
				if w > 24 {
					w = 24
				}
				score += w
			}
		}
		lineHits[i] = hits
		lineScore[i] = score
	}
	// Ambiguity penalty: tokens that appear on many lines are weaker anchors.
	occur := make(map[string]int, len(tokens))
	for _, tok := range tokens {
		tl := strings.ToLower(tok)
		for _, actual := range contentLines {
			if strings.Contains(strings.ToLower(actual), tl) {
				occur[tl]++
			}
		}
	}
	for i := range contentLines {
		if lineHits[i] == 0 {
			continue
		}
		score := lineScore[i]
		for _, tok := range tokens {
			tl := strings.ToLower(tok)
			if !strings.Contains(strings.ToLower(contentLines[i]), tl) {
				continue
			}
			if n := occur[tl]; n > 3 {
				// Soft penalty for very common tokens (still keep unique multi-hit lines).
				score -= (n - 3)
			}
		}
		if score > bestScore || (score == bestScore && lineHits[i] > bestHits) {
			bestScore = score
			bestHits = lineHits[i]
			bestIdx = i
		}
	}
	if bestIdx < 0 || bestHits == 0 || bestScore <= 0 {
		return "", 0
	}
	// Expand to cover neighboring lines that also hit old tokens (block context).
	coreStart, coreEnd := bestIdx, bestIdx+1
	for i := bestIdx - 1; i >= 0 && bestIdx-i <= 4; i-- {
		if lineHits[i] == 0 {
			break
		}
		coreStart = i
	}
	for i := bestIdx + 1; i < len(contentLines) && i-bestIdx <= 6; i++ {
		if lineHits[i] == 0 {
			break
		}
		coreEnd = i + 1
	}
	start, end := padEditClosestWindow(len(contentLines), coreStart, coreEnd)
	return strings.Join(contentLines[start:end], "\n"), start + 1
}

// padEditClosestWindow expands [coreStart, coreEnd) with modest context for
// token-fallback recovery (mirrors apply_patch pad policy: prefer right pad).
func padEditClosestWindow(fileLen, coreStart, coreEnd int) (start, end int) {
	if fileLen <= 0 {
		return 0, 0
	}
	if coreStart < 0 {
		coreStart = 0
	}
	if coreEnd > fileLen {
		coreEnd = fileLen
	}
	if coreEnd <= coreStart {
		coreStart = 0
		coreEnd = fileLen
		if coreEnd > 8 {
			coreEnd = 8
		}
	}
	const (
		preferPad = 2
		minWindow = 8
		maxWindow = 16
	)
	start = coreStart - preferPad
	if start < 0 {
		start = 0
	}
	end = coreEnd + preferPad
	if end > fileLen {
		end = fileLen
	}
	for end-start < minWindow && end < fileLen {
		end++
	}
	for end-start < minWindow && start > 0 {
		start--
	}
	if end-start > maxWindow {
		// Keep core near the start; trim far-side padding.
		if coreEnd-coreStart >= maxWindow {
			start = coreStart
			end = coreStart + maxWindow
			if end > fileLen {
				end = fileLen
				start = end - maxWindow
				if start < 0 {
					start = 0
				}
			}
		} else {
			start = coreStart
			if start > 0 && coreStart-start < preferPad {
				start = coreStart - preferPad
				if start < 0 {
					start = 0
				}
			}
			end = start + maxWindow
			if end > fileLen {
				end = fileLen
				start = end - maxWindow
				if start < 0 {
					start = 0
				}
			}
			if start > coreStart {
				start = coreStart
				end = start + maxWindow
				if end > fileLen {
					end = fileLen
				}
			}
		}
	}
	return start, end
}

// editDistinctiveTokens extracts identifier-like tokens useful for closest
// ranking when line-aligned windows fail. Short/generic tokens are dropped.
func editDistinctiveTokens(line string) []string {
	line = strings.TrimSpace(line)
	if line == "" || IsGenericStructuralLine(line) {
		return nil
	}
	var (
		out   []string
		cur   strings.Builder
		flush = func() {
			if cur.Len() == 0 {
				return
			}
			tok := cur.String()
			cur.Reset()
			if len(tok) < 6 {
				return
			}
			lower := strings.ToLower(tok)
			switch lower {
			case "return", "import", "package", "switch", "select", "default",
				"const", "type", "func", "class", "export", "string", "number",
				"object", "public", "private", "static", "async", "await",
				"interface", "struct", "error", "context", "testing":
				return
			}
			out = append(out, tok)
		}
	)
	for _, r := range line {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') ||
			(r >= '0' && r <= '9') || r == '_' || r == '-' {
			cur.WriteRune(r)
			continue
		}
		flush()
	}
	flush()
	return out
}

// tightenClosestWindowBounds returns [from,to) indexes inside window covering
// strong old/file line pairs, with one line of padding when available.
func tightenClosestWindowBounds(window, oldLines []string) (from, to int) {
	if len(window) == 0 {
		return 0, 0
	}
	firstStrong := -1
	lastStrong := -1
	limit := len(oldLines)
	if limit > len(window) {
		limit = len(window)
	}
	for i := 0; i < limit; i++ {
		if strings.TrimSpace(oldLines[i]) == "" {
			continue
		}
		if editClosestLineScore(window[i], oldLines[i]) >= 0.55 {
			if firstStrong < 0 {
				firstStrong = i
			}
			lastStrong = i
		}
	}
	if firstStrong < 0 {
		return 0, len(window)
	}
	from = firstStrong
	if from > 0 {
		from--
	}
	to = lastStrong + 1
	if to < len(window) {
		to++
	}
	if to > len(window) {
		to = len(window)
	}
	if to <= from {
		return 0, len(window)
	}
	return from, to
}

// scoreEditClosestWindow ranks how well a file window matches the model's
// missed old_string lines. Blank lines contribute little; collapse-ws equality
// nearly matches exact equality so column-padding drift still anchors recovery.
func scoreEditClosestWindow(window, oldLines []string) float64 {
	if len(oldLines) == 0 {
		return 0
	}
	total := 0.0
	strong := 0
	compared := 0
	for i, oldLine := range oldLines {
		if strings.TrimSpace(oldLine) == "" {
			// Model/file blank-run drift should not dominate the score.
			if i < len(window) && strings.TrimSpace(window[i]) == "" {
				total += 0.35
			}
			continue
		}
		compared++
		if i >= len(window) {
			continue
		}
		score := editClosestLineScore(window[i], oldLine)
		total += score
		if score >= 0.55 {
			strong++
		}
	}
	if compared == 0 {
		return total / float64(len(oldLines))
	}
	avg := total / float64(compared)
	// Multi-line agreement is the main signal for true content_diff recovery.
	if compared >= 2 && strong >= 2 {
		avg += 0.12 * (float64(strong) / float64(compared))
	} else if compared >= 2 && strong == 1 {
		// One strong + weak neighbors still beats a lone generic first-line hit.
		avg += 0.04
	}
	if avg > 1.2 {
		avg = 1.2
	}
	return avg
}

func editClosestLineScore(actual, expected string) float64 {
	actual = strings.TrimSpace(actual)
	expected = strings.TrimSpace(expected)
	if actual == "" || expected == "" {
		return 0
	}
	// Tiny structural lines (}, ), {) match everywhere and must not dominate
	// multi-line recovery anchors.
	if IsGenericStructuralLine(actual) || IsGenericStructuralLine(expected) {
		if actual == expected {
			return 0.25
		}
		return 0
	}
	if score := editLineSimilarity(actual, expected); score >= 0.8 {
		return score
	}
	// Column-alignment / punctuation-normalized equality (shared with matchers).
	if NormalizeComparableLine(actual) == NormalizeComparableLine(expected) {
		return 0.92
	}
	return editLineSimilarity(actual, expected)
}

func editLineSimilarity(a, b string) float64 {
	if a == "" || b == "" {
		return 0
	}
	if a == b {
		return 1
	}
	if strings.Contains(a, b) || strings.Contains(b, a) {
		return 0.8
	}
	// Near-identifier typos: "HelloWord" vs "HelloWorld" after normalizing
	// punctuation so func signatures still rank as strong anchors.
	normA := NormalizeComparableLine(a)
	normB := NormalizeComparableLine(b)
	if normA != "" && normB != "" {
		if normA == normB {
			return 0.95
		}
		if RuneEditSimilarity(normA, normB) >= 0.72 {
			return 0.78
		}
	}
	// lightweight token overlap (also soft-match near-equal tokens)
	aParts := strings.Fields(normA)
	bParts := strings.Fields(normB)
	if len(aParts) == 0 || len(bParts) == 0 {
		aParts = strings.Fields(a)
		bParts = strings.Fields(b)
	}
	if len(aParts) == 0 || len(bParts) == 0 {
		return 0
	}
	set := make(map[string]struct{}, len(aParts))
	for _, p := range aParts {
		set[p] = struct{}{}
	}
	overlap := 0.0
	for _, p := range bParts {
		if _, ok := set[p]; ok {
			overlap++
			continue
		}
		// Soft token match for near typos inside signatures.
		best := 0.0
		for _, ap := range aParts {
			if s := RuneEditSimilarity(ap, p); s > best {
				best = s
			}
		}
		if best >= 0.72 {
			overlap += best
		}
	}
	union := float64(len(aParts)+len(bParts)) - overlap
	if union <= 0 {
		return 1
	}
	return overlap / union
}

// RuneEditSimilarity is a cheap normalized inverse Levenshtein on runes.
// Used only for closest-snippet ranking (never for auto-heal writes).
