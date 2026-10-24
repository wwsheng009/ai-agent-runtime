package viewcore

import (
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/wwsheng009/ai-agent-runtime/internal/toolcore"
	toolcoreoutput "github.com/wwsheng009/ai-agent-runtime/internal/toolcore/output"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolresult"
)

const (
	// BatchDefaultLimit caps per-item reads in batch mode when the caller omits
	// limit. Batch calls are for scanning many files, not pulling whole files,
	// and a smaller default keeps the combined output inside the model context
	// window instead of being truncated by the echo layer. Explicit per-item
	// limits are always honored as-is.
	BatchDefaultLimit = 200
	// BatchCompactHeadLines is the per-file head window rendered in compact
	// batch mode (scan-then-read workflow). Metadata still carries lines_read /
	// is_truncated / suggested_next_offset / total_lines for follow-up reads.
	BatchCompactHeadLines = 10
	// BatchFailureDetailLimit bounds how many per-item failures a batch lists in
	// full. Without it, 400 unreadable files print into the errors block and the
	// failed_items contract, pushing the delivered payload past the aggregate
	// budget the tool declared (2026-09-27 review).
	BatchFailureDetailLimit = 20
	// BatchAggregateBudget is the declared window of the whole batch.
	BatchAggregateBudget = toolcoreoutput.ViewBatchAggregateBudgetBytes
	// BatchTailReserveBytes is held back from the aggregate budget for the
	// trailing blocks (the batch-window notice and the errors block), which are
	// appended after the last per-section cap check.
	BatchTailReserveBytes = 4 * 1024
)

// BatchSectionBudgetBytes is the aggregate budget available to file sections:
// the declared window minus the reserve for the trailing blocks. Sections
// decide admission against this, so the assembled body - tail included - stays
// inside the declared window (2026-09-27 review H9).
func BatchSectionBudgetBytes() int {
	budget := BatchAggregateBudget - BatchTailReserveBytes
	if budget < DefaultByteBudget {
		budget = DefaultByteBudget
	}
	return budget
}

// DeliveredRead carries the host bookkeeping for one read. A batch defers it
// until the section carrying the read is really appended, so an item dropped by
// the aggregate cap never lands in the host's read ledger or window cache.
type DeliveredRead struct {
	actions []func()
}

// Defer registers one host action to run when the read is really delivered.
func (d *DeliveredRead) Defer(action func()) {
	if d == nil || action == nil {
		return
	}
	d.actions = append(d.actions, action)
}

// Commit runs the deferred host actions.
func (d *DeliveredRead) Commit() {
	if d == nil {
		return
	}
	for _, action := range d.actions {
		action()
	}
}

// ReadItem reads one request of a batch and reports the host bookkeeping that
// must run once the section is delivered.
type ReadItem func(req Request) (*toolcore.Result, *DeliveredRead)

// ReadBatch reads a request list inside one aggregate window and renders it as
// one result.
//
// The per-item read is injected because a surface may own capabilities the
// shared handler does not have (a host document renderer, a host read ledger);
// everything that must not drift - the aggregate budget, the skip policy, the
// compact head window, the trailing blocks and the batch metadata - lives here.
func ReadBatch(requests []Request, compact bool, readOne ReadItem) *toolcore.Result {
	sections := make([]string, 0, len(requests))
	items := make([]map[string]any, 0, len(requests))
	failures := make([]string, 0)
	failedItems := make([]map[string]any, 0)
	failuresOmitted := 0
	failedItemsOmitted := 0
	succeeded := 0
	defaulted := make([]int, 0, len(requests))
	skipped := make([]map[string]any, 0)
	emittedBytes := 0

	for index, request := range requests {
		// Per-item byte budgets do not bound the combined payload, and the
		// tool-owned window removes the render-layer backstop: once the budget
		// is reached the remaining requests are reported as skipped instead of
		// silently flooding the context.
		if len(sections) > 0 && emittedBytes >= BatchSectionBudgetBytes() {
			skipped = append(skipped, batchSkippedRow(index, request.FilePath))
			continue
		}
		if request.Limit <= 0 {
			request.Limit = BatchDefaultLimit
			defaulted = append(defaulted, index)
		}
		if request.Limit > MaxLimit {
			request.Limit = MaxLimit
		}
		if compact && request.Limit > BatchCompactHeadLines {
			request.Limit = BatchCompactHeadLines
		}

		result, delivered := readOne(request)
		if result == nil || !result.Success {
			message := batchFailureMessage(result)
			appendBatchFailure(&failures, &failuresOmitted, fmt.Sprintf("%s: %s", request.FilePath, message))
			appendBatchFailedItem(&failedItems, &failedItemsOmitted,
				toolresult.FailedItemMap(toolresult.IntPtr(index), request.FilePath, request.FilePath, message))
			continue
		}

		body := result.Content
		if compact {
			body += compactSummary(result.Metadata)
		}
		section := fmt.Sprintf("===== %s =====\n%s", request.FilePath, body)
		// Admission decided after the section exists but before it is appended:
		// a section that would overflow the window is reported as skipped, and
		// its lazy read state is never committed.
		if len(sections) > 0 && emittedBytes+len(section) > BatchSectionBudgetBytes() {
			skipped = append(skipped, batchSkippedRow(index, request.FilePath))
			continue
		}
		sections = append(sections, section)
		emittedBytes += len(section)
		items = append(items, result.Metadata)
		// Count only sections that are actually delivered: a cap-dropped item
		// must not be reported as read (review m5).
		succeeded = len(items)
		delivered.Commit()
	}

	// The trailing blocks share one reserved window: appending them after the
	// last per-section cap check let the body exceed its own declared budget,
	// and the render contract then cut content whose ledger/dedup entries were
	// already committed (2026-09-27 review H9).
	var tail strings.Builder
	if len(skipped) > 0 {
		fmt.Fprintf(&tail,
			"===== batch window =====\n本次批量输出受聚合上限 %d 字节保护：已读取 %d/%d 个文件，跳过 %d 个。请对跳过项单独调用 view（可先用 compact=true 扫描）。",
			BatchAggregateBudget, succeeded, len(requests), len(skipped),
		)
	}
	if len(failures) > 0 || failuresOmitted > 0 {
		// The errors block is part of the same aggregate window as the file
		// sections: letting every failure print in full made 400 unreadable
		// files deliver ~95 KiB against a 64 KiB declaration (2026-09-27
		// review).
		if tail.Len() > 0 {
			tail.WriteString("\n")
		}
		tail.WriteString("===== errors =====\n")
		tail.WriteString(strings.Join(failures, "\n"))
		if failuresOmitted > 0 {
			if len(failures) > 0 {
				tail.WriteString("\n")
			}
			fmt.Fprintf(&tail, "... 另有 %d 个失败未逐条列出（失败合计 %d/%d）",
				failuresOmitted, len(failures)+failuresOmitted, len(requests))
		}
	}
	if tail.Len() > 0 {
		tailText := trimBatchTail(tail.String(), BatchTailReserveBytes)
		sections = append(sections, tailText)
		emittedBytes += len(tailText)
	}

	totalFailures := len(failures) + failuresOmitted
	if succeeded == 0 {
		metadata := map[string]any{
			"batch":         true,
			"request_count": len(requests),
			// Total failures, not just the bounded detail rows: consumers derive
			// success counts from requested-failed, so under-reporting
			// failed_count invented successes for an all-failed batch
			// (2026-09-27 review H8).
			"failed_count":                totalFailures,
			"batch_default_limit_applied": defaulted,
			"compact":                     compact,
		}
		if len(failedItems) > 0 {
			metadata[toolresult.MetadataFailedItemsKey] = failedItems
		}
		if failuresOmitted > 0 {
			metadata["failures_omitted"] = failuresOmitted
			metadata["failed_items_omitted"] = failedItemsOmitted
		}
		metadata["batch_tail_reserve_bytes"] = BatchTailReserveBytes
		attachBatchWindowMetadata(metadata, emittedBytes, skipped)

		summary := strings.Join(failures, "; ")
		if failuresOmitted > 0 {
			if summary != "" {
				summary += "; "
			}
			summary += fmt.Sprintf("另有 %d 个失败未逐条列出", failuresOmitted)
		}
		if strings.TrimSpace(summary) == "" {
			summary = "没有可读取的条目"
		}
		// No surface-independent error code on purpose: the local surface has
		// always published this failure as a message and let the diagnostic
		// layer derive the code, and one vocabulary for one tool matters more
		// here than a code that would only be a guess across N failures.
		return &toolcore.Result{
			Success:    false,
			OutputKind: toolcore.KindText,
			Content:    fmt.Sprintf("批量读取失败: %s", summary),
			Err:        fmt.Errorf("批量读取失败: %s", summary),
			Metadata:   metadata,
		}
	}

	metadata := map[string]any{
		"batch":                       true,
		"request_count":               len(requests),
		"succeeded_count":             succeeded,
		"failed_count":                totalFailures,
		"partial_failure":             len(failures) > 0,
		"items":                       items,
		"batch_default_limit_applied": defaulted,
		"compact":                     compact,
	}
	if len(failedItems) > 0 {
		metadata[toolresult.MetadataFailedItemsKey] = failedItems
	}
	if failuresOmitted > 0 {
		metadata["failures_omitted"] = failuresOmitted
		metadata["failed_items_omitted"] = failedItemsOmitted
	}
	metadata["batch_tail_reserve_bytes"] = BatchTailReserveBytes
	attachBatchWindowMetadata(metadata, emittedBytes, skipped)
	return &toolcore.Result{
		Success:    true,
		OutputKind: toolcore.KindText,
		Content:    strings.Join(sections, "\n\n"),
		Metadata:   metadata,
	}
}

// AttachIgnoredBatchWindow tells the model that a batch call carried a top-level
// offset/limit that only applies to single-file reads. Silently dropping them is
// the "silence still costs" shape: state it so the window can be moved onto the
// files[] entries instead.
func AttachIgnoredBatchWindow(result *toolcore.Result, offset, limit int) *toolcore.Result {
	if result == nil {
		return nil
	}
	if result.Metadata == nil {
		result.Metadata = map[string]any{}
	}
	note := fmt.Sprintf(
		"[note] 顶层 offset=%d limit=%d 未生效：files[] 批量读取需要在每个条目上设置 offset/limit。",
		offset, limit,
	)
	result.Metadata["ignored_top_level_window"] = map[string]any{"offset": offset, "limit": limit}
	result.Metadata["ignored_top_level_window_note"] = note
	appendNote(&result.Content, note)
	return result
}

// batchFailureMessage is the per-item reason recorded in the failures block: the
// underlying error when there is one, otherwise whatever the result carried.
func batchFailureMessage(result *toolcore.Result) string {
	if result == nil {
		return "unknown error"
	}
	if result.Err != nil {
		return result.Err.Error()
	}
	if message := strings.TrimSpace(result.Content); message != "" {
		return message
	}
	return "unknown error"
}

func batchSkippedRow(index int, filePath string) map[string]any {
	return map[string]any{
		"index":     index,
		"file_path": filePath,
		"reason":    "aggregate_budget",
	}
}

// appendBatchFailure records one failure line while the detail budget lasts and
// counts the rest as omitted.
func appendBatchFailure(failures *[]string, omitted *int, line string) {
	if failures == nil || omitted == nil {
		return
	}
	if len(*failures) < BatchFailureDetailLimit {
		*failures = append(*failures, line)
		return
	}
	*omitted++
}

// appendBatchFailedItem records one structured failed-item row under the same
// detail budget. A nil row is ignored and does not consume budget.
func appendBatchFailedItem(items *[]map[string]any, omitted *int, row map[string]any) {
	if items == nil || omitted == nil || row == nil {
		return
	}
	if len(*items) < BatchFailureDetailLimit {
		*items = append(*items, row)
		return
	}
	*omitted++
}

// attachBatchWindowMetadata publishes the aggregate-window accounting so the
// model (and tests) can tell how many bytes were emitted and which files were
// skipped to stay inside the cap.
func attachBatchWindowMetadata(metadata map[string]any, emittedBytes int, skipped []map[string]any) {
	if metadata == nil || len(skipped) == 0 {
		return
	}
	metadata["batch_aggregate_budget_bytes"] = BatchAggregateBudget
	metadata["batch_bytes_emitted"] = emittedBytes
	metadata["batch_skipped_count"] = len(skipped)
	metadata["batch_skipped_files"] = skipped
}

// trimBatchTail keeps the trailing batch blocks inside their reserved window,
// cutting on a UTF-8 boundary and stating that the details were trimmed so the
// model is never silently missing the skipped-file/error accounting.
func trimBatchTail(text string, limit int) string {
	if limit <= 0 || len(text) <= limit {
		return text
	}
	const marker = "\n... 尾部细节已按聚合预算截断（跳过项与失败数仍见 metadata）。"
	cut := limit - len(marker)
	if cut < 0 {
		cut = 0
	}
	for cut > 0 && !utf8.RuneStart(text[cut]) {
		cut--
	}
	return strings.TrimRight(text[:cut], "\n") + marker
}

// compactSummary renders a single-line metadata digest for compact batch mode,
// so models can decide the next offset without re-reading the tool schema or
// guessing from truncated content.
func compactSummary(metadata map[string]any) string {
	parts := make([]string, 0, 4)
	for _, key := range []string{"lines_read", "is_truncated", "suggested_next_offset", "total_lines"} {
		value, ok := metadata[key]
		if !ok {
			continue
		}
		switch typed := value.(type) {
		case int:
			parts = append(parts, fmt.Sprintf("%s=%d", key, typed))
		case bool:
			parts = append(parts, fmt.Sprintf("%s=%t", key, typed))
		}
	}
	if len(parts) == 0 {
		return "..."
	}
	return "... " + strings.Join(parts, ", ")
}
