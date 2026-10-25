package searchcore

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/wwsheng009/ai-agent-runtime/internal/toolcore"
	toolcoreoutput "github.com/wwsheng009/ai-agent-runtime/internal/toolcore/output"
)

// grepRenderInputs carries everything the page renderer needs. Rendering is
// part of the shared contract: every surface prints the same shapes, the ones
// the local tool has always printed.
type grepRenderInputs struct {
	paths         toolcore.PathResolver
	result        *GrepResult
	options       GrepOptions
	patterns      []string
	roots         []string
	mode          GrepMode
	contextBefore int
	contextAfter  int
}

type grepPage struct {
	content       string
	contextLines  int
	matchCount    int
	renderedFiles int
	byteTruncated bool
	renderedLines int
}

// displayPathFor renders the file column of one match. A single-file search
// shows only the file name; a directory search shows the workspace-relative
// path; a multi-root search prefixes the entry with the root label the caller
// spelled - all three inherited from the local tool's scope display rules.
func (in grepRenderInputs) displayPathFor(file string) string {
	if in.result != nil && in.result.SingleFile && len(in.roots) == 1 {
		return filepath.Base(file)
	}
	if len(in.roots) > 1 {
		root := closestRoot(in.roots, file)
		if root != "" {
			label := in.paths.Relative(root)
			if label == "" || label == "." {
				return in.paths.Relative(file)
			}
			rel := in.paths.Relative(file)
			if strings.HasPrefix(rel, label+"/") {
				return rel
			}
			return label + "/" + rel
		}
	}
	rel := in.paths.Relative(file)
	// A surface whose workspace root is unrelated to the search root can only
	// answer absolutely. The local tool has always shown such files relative to
	// the search root, so fall back to that rather than leaking a host path
	// into model-visible output.
	if len(in.roots) == 1 && filepath.IsAbs(rel) {
		if rootRel, err := filepath.Rel(in.roots[0], file); err == nil &&
			!strings.HasPrefix(rootRel, "..") {
			return filepath.ToSlash(rootRel)
		}
	}
	return rel
}

func closestRoot(roots []string, file string) string {
	best := ""
	for _, root := range roots {
		if root == "" {
			continue
		}
		if strings.HasPrefix(file, root+string(filepath.Separator)) && len(root) > len(best) {
			best = root
		}
	}
	return best
}

// grepOmittedLongLine and grepLongLinePreviewSuffix are the markers the local
// tool has always printed. They are part of the visible contract, so the core
// reproduces them verbatim.
const (
	grepOmittedLongLine       = "[Omitted long matching line]"
	grepLongLinePreviewSuffix = " [... omitted end of long line]"
)

// renderText applies the two text-shaping parameters to one output line. Trim
// runs first because it only removes content the caller asked to hide, then
// the length cap decides whether anything is left to show. Neither touches the
// stored GrepMatch, so column numbers and counts stay derived from the original
// bytes.
func (in grepRenderInputs) renderText(text string) string {
	if in.options.Trim {
		text = strings.TrimLeft(text, " \t")
	}
	if !in.options.MaxColumnsSet || in.options.MaxColumns <= 0 {
		return text
	}
	if len(text) <= in.options.MaxColumns {
		return text
	}
	if in.options.MaxColumnsPreview {
		return truncateGrepTextBytes(text, in.options.MaxColumns) + grepLongLinePreviewSuffix
	}
	return grepOmittedLongLine
}

// truncateGrepTextBytes cuts at a UTF-8 boundary so the preview never ends in
// half a rune.
func truncateGrepTextBytes(text string, limit int) string {
	if limit <= 0 || len(text) <= limit {
		return text
	}
	cut := limit
	for cut > 0 && !utf8.RuneStart(text[cut]) {
		cut--
	}
	return text[:cut]
}

// renderGrepPage renders one result page in the local tool's output shapes:
//   - content:        `path:line: text` (fragment: `path:line:col: text`)
//   - context lines:  `path>N: text` for matches, `path N: text` for context,
//     blocks separated by `--`
//   - files:          one path per line
//   - count:          `path:count`
//   - empty page:     未找到匹配的内容
//   - page window:    中文分页提示 with next_offset (never the word 截断)
func renderGrepPage(in grepRenderInputs) grepPage {
	page := grepPage{matchCount: len(in.result.Matches)}
	var lines []string

	switch {
	case in.mode == GrepModeFiles || in.mode == GrepModeFilesWithout:
		for _, entry := range in.result.Files {
			lines = append(lines, in.displayPathFor(entry.File))
		}
		page.renderedFiles = len(in.result.Files)
	case in.mode == GrepModeCount:
		for _, entry := range in.result.Files {
			lines = append(lines, fmt.Sprintf("%s:%d", in.displayPathFor(entry.File), entry.Count))
		}
		page.renderedFiles = len(in.result.Files)
	case in.options.Passthru:
		// Passthru is the whole file in the context presentation: the match
		// marker distinguishes the selected lines, and neighbours keep the
		// established context shape. No `--` separators: the lines are
		// contiguous by construction, so a group boundary would be invented.
		for _, item := range in.result.Matches {
			display := in.displayPathFor(item.File)
			if item.IsContext {
				lines = append(lines, fmt.Sprintf("%s %d: %s", display, item.Line, in.renderText(item.Text)))
				continue
			}
			lines = append(lines, fmt.Sprintf("%s>%d: %s", display, item.Line, in.renderText(item.Text)))
		}
	case in.contextBefore > 0 || in.contextAfter > 0:
		// A replacement rewrites the matched line, so the engine's text has to
		// win over the re-read line or the substitution would be undone here.
		expanded := ExpandGrepContext(in.result.Matches, in.contextBefore, in.contextAfter,
			in.options.OnlyMatching, in.options.Replace != "")
		page.contextLines = len(expanded)
		separator := grepContextSeparator(in.options)
		printedAny := false
		for _, line := range expanded {
			if line.GroupStart && printedAny && separator != "" {
				lines = append(lines, separator)
			}
			display := in.displayPathFor(line.File)
			if line.IsMatch {
				lines = append(lines, fmt.Sprintf("%s>%d: %s", display, line.Line, in.renderText(line.Text)))
			} else {
				lines = append(lines, fmt.Sprintf("%s %d: %s", display, line.Line, in.renderText(line.Text)))
			}
			printedAny = true
		}
	default:
		for _, item := range in.result.Matches {
			text := in.renderText(item.Text)
			if item.Column > 0 {
				lines = append(lines, fmt.Sprintf("%s:%d:%d: %s", in.displayPathFor(item.File), item.Line, item.Column, text))
				continue
			}
			lines = append(lines, fmt.Sprintf("%s:%d: %s", in.displayPathFor(item.File), item.Line, text))
		}
	}

	// Byte window: grep owns its model-visible budget (32 KiB, shared with the
	// local tool through toolcore/output), stops at complete leading lines and
	// declares the window so the render layer never folds the page again.
	if len(lines) > 0 {
		budget := toolcoreoutput.GrepOutputBudgetBytes
		kept, used := 0, 0
		for _, line := range lines {
			lineBytes := len(line) + 1
			if kept > 0 && used+lineBytes > budget {
				break
			}
			used += lineBytes
			kept++
		}
		if kept < len(lines) {
			lines = lines[:kept]
			page.byteTruncated = true
			if !in.result.Truncated {
				in.result.Truncated = true
			}
		}
	}
	page.renderedLines = len(lines)

	// Page cap: the walker stops one entry past the page, but a runner-fed
	// engine can hand back the whole list at once. The offset window was
	// already applied by the shared window helpers; the renderer only re-applies
	// the limit cap so the paging notice matches the caller's page size.
	if len(lines) > in.options.Limit && !in.result.SingleFile {
		lines = lines[:in.options.Limit]
		if !in.result.Truncated {
			in.result.Truncated = true
		}
	}
	// A page that fills the requested window is a page boundary, not data
	// loss: the paging notice below must fire so the model continues with
	// next_offset instead of rewording the search.
	if !in.result.Truncated && in.options.Limit > 0 && len(lines) >= in.options.Limit {
		in.result.Truncated = true
	}

	content := strings.Join(lines, "\n")
	if content == "" {
		content = "未找到匹配的内容"
	}
	if in.result.Truncated {
		nextOffset := in.options.Offset + len(lines)
		if in.options.Offset > 0 || page.renderedFiles == 0 {
			content += fmt.Sprintf("\n\n(已分页显示第 %d-%d 个匹配；next_offset=%d 可继续分页)", in.options.Offset+1, nextOffset, nextOffset)
		} else {
			content += fmt.Sprintf("\n\n(已分页返回前 %d 个匹配；next_offset=%d 可继续分页读取后续匹配)", len(lines), nextOffset)
		}
		if page.byteTruncated {
			content += fmt.Sprintf("\n(输出超过 grep 字节预算 %d 字节，提前停止；next_step: 收窄 pattern、增加 paths/glob 限定，或用 max_count 限制每文件匹配数)", toolcoreoutput.GrepOutputBudgetBytes)
		}
	}

	page.content = content
	return page
}

// grepContextSeparator is the string printed between two context groups: rg's
// default "--", a caller-supplied string, or nothing under no_context_separator.
// An empty value counts as "not set" - the local tool passed nothing to rg in
// that case, which left rg's own default in place.
func grepContextSeparator(opts GrepOptions) string {
	if opts.NoContextSeparator {
		return ""
	}
	if opts.ContextSeparatorSet && opts.ContextSeparator != "" {
		return opts.ContextSeparator
	}
	return "--"
}

// renderGrepJSONPage turns the collected event lines into the page for json
// output: a slice of ripgrep's own stream, with each event's path rewritten to
// the surface's display path so a client reads the same path it would read in
// the rendered page. The payload stays parseable JSON Lines - nothing is
// appended to it - and the window is counted in lines, which is the unit the
// caller receives.
func renderGrepJSONPage(in grepRenderInputs) grepPage {
	page := grepPage{}
	if in.result == nil {
		return page
	}
	lines := in.result.JSONEvents
	paths := in.result.JSONEventPaths
	offset := in.options.Offset
	if offset < 0 {
		offset = 0
	}
	limit := in.options.Limit
	if limit < 1 {
		limit = DefaultGrepLimit
	}
	start := offset
	if start > len(lines) {
		start = len(lines)
	}
	end := start + limit
	if end > len(lines) {
		end = len(lines)
	}
	selected := make([]string, 0, end-start)
	for i := start; i < end; i++ {
		line := lines[i]
		if i < len(paths) && paths[i] != "" {
			line = rewriteGrepJSONPath(line, in.displayPathFor(paths[i]))
		}
		selected = append(selected, line)
	}
	page.content = strings.Join(selected, "\n")
	page.renderedLines = len(selected)
	page.matchCount = in.result.JSONStats.Matches
	if len(lines) > end {
		// Lines arrived past the page: has_more is true, and the next offset is
		// where this page stopped.
		in.result.Truncated = true
	}
	return page
}

// rewriteGrepJSONPath replaces the `"path":{"text":...}` value of one event line
// with the surface's display path. Everything else in the line is kept exactly
// as ripgrep wrote it, so a caller can still compare the stream with rg's own
// output - only the path is translated into the convention every other output
// shape of this tool uses.
func rewriteGrepJSONPath(line, display string) string {
	if display == "" {
		return line
	}
	var event rgJSONEvent
	if err := json.Unmarshal([]byte(line), &event); err != nil {
		return line
	}
	old := event.Data.Path.Text
	if old == "" || old == display {
		return line
	}
	anchor := strings.Index(line, `"path"`)
	if anchor < 0 {
		return line
	}
	textAt := strings.Index(line[anchor:], `"text":`)
	if textAt < 0 {
		return line
	}
	valueAt := anchor + textAt + len(`"text":`)
	encodedOld, err := json.Marshal(old)
	if err != nil || !strings.HasPrefix(line[valueAt:], string(encodedOld)) {
		return line
	}
	encodedNew, err := json.Marshal(display)
	if err != nil {
		return line
	}
	return line[:valueAt] + string(encodedNew) + line[valueAt+len(encodedOld):]
}

// GrepRenderBudget exposes the shared in-tool byte window so surfaces can
// declare the same model-visible budget the renderer enforced.
func GrepRenderBudget() int { return toolcoreoutput.GrepOutputBudgetBytes }

// renderGrepMetadataStamps adds the dispositions the local tool has always
// published: the render-layer opt-out with its declared window, empty-success
// marking for a true no-match page, and the byte-budget next_action.
func renderGrepMetadataStamps(metadata map[string]any, page grepPage, result *GrepResult, offset int) map[string]any {
	if metadata == nil {
		return nil
	}
	metadata["skip_render_truncation"] = true
	metadata["model_visible_budget_bytes"] = toolcoreoutput.GrepOutputBudgetBytes
	if page.byteTruncated {
		metadata["results_truncated"] = true
		metadata["truncation_reason"] = "byte_budget"
		metadata["next_action"] = "输出超过 grep 字节预算已提前停止；请收窄 pattern、增加 paths/glob 限定，或用 max_count 限制每文件匹配数。不要原样重试同一查询。"
	}
	if offset > 0 && page.matchCount == 0 && page.renderedFiles == 0 && !result.Truncated && offset >= 500 {
		// Deep offset beyond the collected window: keep the legacy guidance.
		metadata["next_action"] = "offset 超出本次抓取窗口（抓取上限 5000 条）：请减小 offset，或先收窄 pattern/paths 再分页。不要原样重试同一查询。"
	}
	if page.matchCount == 0 && page.renderedFiles == 0 && !result.Truncated && utf8.ValidString(page.content) {
		metadata["empty_result"] = true
		metadata["outcome"] = "empty"
	}
	return metadata
}

// renderGrepStatsSummary prints the stats block in the local tool's rg-style
// wording, appended to the page when stats was requested.
func renderGrepStatsSummary(summary GrepStats) string {
	if summary.Mode == GrepModeFilesWithout {
		// The mode never collects matches, so the match counters would be a
		// fabricated zero. Report only what the scan actually observed.
		return strings.Join([]string{
			"-- stats --",
			fmt.Sprintf("files without matches: %d", summary.FilesWithoutMatched),
			fmt.Sprintf("files searched: %d", summary.FilesScanned),
			fmt.Sprintf("bytes printed: %d", summary.BytesPrinted),
		}, "\n")
	}
	lines := []string{
		"-- stats --",
		fmt.Sprintf("matches: %d", summary.Matches),
		fmt.Sprintf("matched lines: %d", summary.MatchedLines),
		fmt.Sprintf("files contained matches: %d", summary.FilesWithMatched),
		fmt.Sprintf("files searched: %d", summary.FilesScanned),
		fmt.Sprintf("bytes printed: %d", summary.BytesPrinted),
	}
	return strings.Join(lines, "\n")
}
