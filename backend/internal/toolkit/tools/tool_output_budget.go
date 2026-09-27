package tools

import (
	"context"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/wwsheng009/ai-agent-runtime/internal/artifact"
	"github.com/wwsheng009/ai-agent-runtime/internal/observability"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolctx"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolkit"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolresult"
)

// Per-tool model-visible output budgets (bytes).
//
// Every tool listed here can produce a large text payload, so it owns its own
// window: it stops at its tool-specific budget, publishes its own truncation /
// continuation metadata, and stamps toolresult.MetadataSkipRenderTruncationKey
// so the render layer (L4) never folds the payload a second time with its
// one-size-fits-all budget.
//
// The numbers follow how the payload is consumed: reading code (view/grep) and
// paging raw archived output (artifact_read) benefits from a wide window, while
// path listings and fetched previews are scanning inputs and stay smaller. They
// are intentionally independent of output.ModelToolTextByteBudget, which stays
// the backstop for tools that do not own a budget.
const (
	// viewOutputBudgetBytes: one code window, large enough for a whole function
	// or file section without a second round trip.
	viewOutputBudgetBytes = 32 * 1024
	// viewBatchAggregateBudgetBytes bounds the combined model-visible payload of
	// one files[] batch. Per-item windows are viewOutputBudgetBytes each, and
	// view stamps skip_render_truncation, so without an in-tool aggregate cap a
	// wide batch rides the exemption past the render-layer backstop. The cap is
	// enforced in view.executeBatch and the result carries the skipped count so
	// the model can re-read omitted files individually (analysis §3.1).
	//
	// It must equal output.modelToolTextBudgetCeilingBytes (64 KiB), not exceed
	// it: a wider aggregate can never be model-visible in one piece, and the
	// gateway's archive tier follows the *declared* budget, so declaring 32 KiB
	// while really delivering up to 96 KiB archived bodies the model could read
	// end to end (review m6). Batch results declare this constant as their
	// model-visible window.
	viewBatchAggregateBudgetBytes = 64 * 1024
	// grepOutputBudgetBytes: one match list; wide enough for a useful first
	// pass before the model narrows the pattern.
	grepOutputBudgetBytes = 32 * 1024
	// globOutputBudgetBytes: one path result set.
	globOutputBudgetBytes = 16 * 1024
	// lsOutputBudgetBytes: one directory tree listing.
	lsOutputBudgetBytes = 16 * 1024
	// fetchOutputBudgetBytes: one fetched document body.
	fetchOutputBudgetBytes = 32 * 1024
	// artifactOutputBudgetBytes: raw archived bytes served per artifact_read page.
	artifactOutputBudgetBytes = 32 * 1024
	// shellOutputBudgetBytes: one shell command's model-visible window.
	//
	// Shell output is produced by a process rather than by a paged data source,
	// so the tool folds it itself: bash (single command and batch) and
	// aicli_exec keep a head+tail window of the captured text (errors and test
	// verdicts usually land at the tail), state the fold arithmetic in the body,
	// and archive the complete capture *before* folding so artifact_read can
	// still page the omitted middle by byte offset. The tool - not the render
	// layer - owns that window, which is why ownShellOutputWindow stamps
	// skip_render_truncation on every return path.
	shellOutputBudgetBytes = 32 * 1024
)

// stampToolOwnsOutput declares that a tool already shaped its own payload and
// owns its final window, so the render layer (L4) must not fold it again.
// Stamping is unconditional - independent of any "truncated"/"is_truncated"
// flag - because the tool's own budget decides the payload shape on every path,
// including complete results that happen to be large.
func stampToolOwnsOutput(result *toolkit.ToolResult) *toolkit.ToolResult {
	if result == nil {
		return nil
	}
	if result.Metadata == nil {
		result.Metadata = map[string]interface{}{}
	}
	result.Metadata[toolresult.MetadataSkipRenderTruncationKey] = true
	return result
}

// stampToolOwnsOutputWithBudget stamps a tool that owns its window AND declares
// that window as its model-visible budget.
//
// The declared window is what keeps the rest of the pipeline aligned with the
// tool's real contract: the gateway's archive tiering floor (a result the model
// can read end to end needs no archived record) and the render-layer fold
// budget both follow it, so neither falls back to a fraction of, or the whole,
// one-size-fits-all backstop.
func stampToolOwnsOutputWithBudget(result *toolkit.ToolResult, bytes int) *toolkit.ToolResult {
	return declareModelVisibleBudget(stampToolOwnsOutput(result), bytes)
}

// declareModelVisibleBudget attaches a declared model-visible budget to a
// result without folding its content.
//
// It is the second half of stampToolOwnsOutputWithBudget and is only meaningful
// together with the opt-out: the tool has already shaped its payload, and the
// declared number tells the rest of the pipeline how wide that window is (the
// gateway's archive tiering floor and the render-layer fold budget both follow
// it). Declaring a budget *without* stamping the opt-out still leaves the
// payload to the render layer, which is exactly the double-fold this contract
// removes.
func declareModelVisibleBudget(result *toolkit.ToolResult, bytes int) *toolkit.ToolResult {
	if result == nil || bytes <= 0 {
		return result
	}
	if result.Metadata == nil {
		result.Metadata = map[string]interface{}{}
	}
	result.Metadata[toolresult.MetadataModelVisibleBudgetKey] = bytes
	return result
}

// ownShellOutputWindow is the single place where a shell-style tool (bash
// single command, bash batch, aicli_exec) takes ownership of its output.
//
// Order matters. The complete capture is archived *before* the body is folded,
// so the record artifact_read pages holds the whole stream instead of the head
// the model already saw. The folded result is then marked as a window of that
// record (artifact_source_id), which is also what stops the gateway from
// archiving the folded body a second time, and finally stamped as tool-owned
// (skip_render_truncation + declared budget) so the render layer never folds a
// shell payload again.
//
// Results that already fit the window only receive the ownership stamp: there
// is nothing to fold, and the gateway keeps its usual tiering for them.
func ownShellOutputWindow(ctx context.Context, toolName string, result *toolkit.ToolResult) *toolkit.ToolResult {
	if result == nil {
		return nil
	}
	text := result.Content
	if text == "" {
		return stampToolOwnsOutputWithBudget(result, shellOutputBudgetBytes)
	}
	folded, foldStats, didFold := foldShellOutputToWindow(text, shellOutputBudgetBytes)
	if didFold {
		result.Content = folded
		metadata := result.Metadata
		if metadata == nil {
			metadata = map[string]interface{}{}
			result.Metadata = metadata
		}
		metadata["truncated"] = true
		metadata["output_window_bytes"] = shellOutputBudgetBytes
		metadata["output_window_total_bytes"] = len(text)
		// 中间值的读取入口与成本：head/tail 之间的字节区间就是 artifact_read
		// 需要翻页的部分，这里把区间与页数一并交给模型，避免盲翻整份 capture。
		metadata["output_window_head_bytes"] = foldStats.HeadBytes
		metadata["output_window_tail_bytes"] = foldStats.TailBytes
		metadata["output_window_omitted_start"] = foldStats.OmittedStart
		metadata["output_window_omitted_end"] = foldStats.OmittedEnd
		metadata["output_window_omitted_bytes"] = foldStats.OmittedEnd - foldStats.OmittedStart
		if pageBytes := artifactReadMaxLimitBytes(); pageBytes > 0 {
			omitted := foldStats.OmittedEnd - foldStats.OmittedStart
			metadata["output_window_middle_pages"] = (omitted + pageBytes - 1) / pageBytes
		}
		archiveShellOutputWindow(ctx, toolName, result, text)
	}
	return stampToolOwnsOutputWithBudget(result, shellOutputBudgetBytes)
}

// shellWindowFoldStats describes the head+tail window a shell fold produced.
// OmittedStart/OmittedEnd delimit the middle of the capture that the model did
// not see: it is exactly the byte range artifact_read must page.
type shellWindowFoldStats struct {
	HeadBytes    int
	TailBytes    int
	OmittedStart int
	OmittedEnd   int
	TotalBytes   int
	TotalLines   int
}

// foldShellOutputToWindow folds a shell-style payload to budget bytes as a
// head+tail window and reports the fold arithmetic.
//
// Both ends are cut on rune boundaries (the tail additionally prefers a line
// start), and the notice is charged against the same window: the model receives
// head+notice+tail, so the tool's own budget must bound all three together. The
// tail is what makes the fold usable without any extra round trip - shell
// failures, test verdicts and "N passed" summaries live at the end - while the
// omitted middle stays addressable through the archived capture.
func foldShellOutputToWindow(text string, budget int) (string, shellWindowFoldStats, bool) {
	stats := shellWindowFoldStats{TotalBytes: len(text)}
	if budget <= 0 || len(text) <= budget {
		stats.HeadBytes = len(text)
		stats.OmittedStart = len(text)
		stats.OmittedEnd = len(text)
		stats.TotalLines = strings.Count(strings.ReplaceAll(text, "\r\n", "\n"), "\n") + 1
		return text, stats, false
	}
	totalBytes := len(text)
	totalLines := strings.Count(strings.ReplaceAll(text, "\r\n", "\n"), "\n") + 1
	stats.TotalLines = totalLines

	contentBudget := budget
	for attempt := 0; attempt < 8; attempt++ {
		if contentBudget <= 0 {
			break
		}
		headBudget := contentBudget / 2
		tailBudget := contentBudget - headBudget
		head := safeShellHeadByBytes(text, headBudget)
		tail := safeShellTailByBytes(text, tailBudget)
		omittedStart := len(head)
		omittedEnd := totalBytes - len(tail)
		if omittedEnd < omittedStart {
			omittedEnd = omittedStart
		}
		notice := shellWindowFoldNotice(omittedStart, omittedEnd, totalBytes, totalLines)
		if len(head)+len(tail)+len(notice) <= budget {
			stats.HeadBytes = len(head)
			stats.TailBytes = len(tail)
			stats.OmittedStart = omittedStart
			stats.OmittedEnd = omittedEnd
			return head + notice + tail, stats, true
		}
		// The notice grows with the digit count of its numbers; shrink the
		// content window by the overflow and try again (converges in a few
		// rounds because a shorter window never lengthens the notice).
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
	notice := shellWindowFoldNotice(0, totalBytes, totalBytes, totalLines)
	stats.HeadBytes = 0
	stats.TailBytes = 0
	stats.OmittedStart = 0
	stats.OmittedEnd = totalBytes
	return safeShellHeadByBytes(notice, budget), stats, true
}

// shellWindowFoldNotice renders the fold notice for a shell payload whose
// producing tool owns the model-visible window.
//
// It states the window arithmetic, the exact byte range of the omitted middle,
// and the cheapest way back to it: the first artifact_read page offset/limit
// (with the total page count, so the model can price the full read before
// starting it), a bisect offset for "the interesting part is somewhere in the
// middle", and the cheaper alternative of re-running a narrower command.
func shellWindowFoldNotice(omittedStart, omittedEnd, totalBytes, totalLines int) string {
	omitted := omittedEnd - omittedStart
	if omitted < 0 {
		omitted = 0
	}
	pageBytes := artifactReadMaxLimitBytes()
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

// safeShellHeadByBytes returns the longest prefix of text within limit bytes
// that does not split a UTF-8 sequence.
func safeShellHeadByBytes(text string, limit int) string {
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

// safeShellTailByBytes returns the longest suffix of text within limit bytes
// that does not split a UTF-8 sequence. It also prefers to start right after a
// newline so the model does not receive a half line first, as long as trimming
// to that line start keeps at least half of the tail window.
func safeShellTailByBytes(text string, limit int) string {
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

// archiveShellOutputWindow stores the complete shell capture in the session's
// artifact store and marks the folded result as a window of that record.
//
// The store is optional (headless runs and unit tests execute tools without
// one). Without it the fold still happens - the model window must stay bounded
// either way - and the gateway archives the folded body as usual.
func archiveShellOutputWindow(ctx context.Context, toolName string, result *toolkit.ToolResult, full string) {
	store := toolctx.ArtifactStore(ctx)
	if store == nil || strings.TrimSpace(full) == "" {
		return
	}
	id, err := store.Put(ctx, artifact.Record{
		SessionID: toolctx.SessionID(ctx),
		ToolName:  toolName,
		Summary:   shellWindowSummary(full),
		Content:   full,
		Metadata: map[string]interface{}{
			"source":                  "tool_output_window",
			"tool_owned_window_bytes": shellOutputBudgetBytes,
			"captured_bytes":          len(full),
		},
		CreatedAt: time.Now().UTC(),
	})
	if err != nil {
		result.Metadata["artifact_error"] = err.Error()
		return
	}
	if strings.TrimSpace(id) == "" {
		return
	}
	result.Metadata["artifact_id"] = id
	result.Metadata["artifact_source_id"] = id
	observability.RecordToolOutputArchive(observability.ArchiveLayerToolWindow, observability.ArchiveDispositionArchived)
}

// shellWindowSummary keeps the archived record searchable: the first line of the
// capture, bounded to the same width the gateway uses for its own preview.
func shellWindowSummary(full string) string {
	head := strings.TrimSpace(full)
	if idx := strings.IndexByte(head, '\n'); idx >= 0 {
		head = head[:idx]
	}
	return safeShellHeadByBytes(head, 400)
}
