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
	// aicli_exec keep the first shellOutputBudgetBytes of the captured text,
	// state the fold arithmetic in the body, and archive the complete capture
	// *before* folding so artifact_read can still page the omitted tail. The
	// tool - not the render layer - owns that window, which is why
	// ownShellOutputWindow stamps skip_render_truncation on every return path.
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
	folded, didFold := foldShellOutputToWindow(text, shellOutputBudgetBytes)
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
		archiveShellOutputWindow(ctx, toolName, result, text)
	}
	return stampToolOwnsOutputWithBudget(result, shellOutputBudgetBytes)
}

// foldShellOutputToWindow folds a shell-style payload head-only to budget bytes
// and reports whether a fold happened.
//
// The head is cut on a rune boundary, and the notice is charged against the same
// window: the model receives head+notice, so the tool's own budget must bound
// the two together. The notice is rendered from the final head length, which
// needs at most one trim (a shorter head never lengthens the notice).
func foldShellOutputToWindow(text string, budget int) (string, bool) {
	if budget <= 0 || len(text) <= budget {
		return text, false
	}
	totalBytes := len(text)
	totalLines := strings.Count(strings.ReplaceAll(text, "\r\n", "\n"), "\n") + 1
	headBudget := budget
	for {
		head := safeShellHeadByBytes(text, headBudget)
		notice := shellWindowFoldNotice(len(head), totalBytes, totalLines)
		if len(head)+len(notice) <= budget {
			return head + notice, true
		}
		if headBudget == 0 {
			// Degenerate window (notice alone exceeds the budget): keep the
			// notice, which carries the recovery route, and drop the head.
			return safeShellHeadByBytes(notice, budget), true
		}
		headBudget = budget - len(notice)
		if headBudget < 0 {
			headBudget = 0
		}
	}
}

// shellWindowFoldNotice renders the fold notice for a shell payload whose
// producing tool owns the model-visible window.
//
// It plays the same role as the render-layer fold notice - state the shown head,
// the omitted bytes and the next step - but stays inside the tool's contract: the
// arithmetic describes the *captured* stream and the recovery route is the
// archived raw output the tool stored before folding.
func shellWindowFoldNotice(keptBytes, totalBytes, totalLines int) string {
	omitted := totalBytes - keptBytes
	if omitted < 0 {
		omitted = 0
	}
	return fmt.Sprintf(
		"\n\n[output window: showing the first %d of %d bytes of the captured output; omitted %d bytes (%d lines). "+
			"The complete capture is archived, so the omitted tail stays readable with artifact_read on the raw-output pointer below; "+
			"when the head is not the interesting part, re-run a narrower command instead (a tighter pattern, Select-Object -First/-Last).]",
		keptBytes, totalBytes, omitted, totalLines,
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
