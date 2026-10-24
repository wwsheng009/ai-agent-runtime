package mutatecore

import (
	"errors"
	"fmt"
	"strings"
)

// PatchSummaryMessage renders the one-line result of an applied patch: what
// kind of change happened and how many paths it touched.
func PatchSummaryMessage(result PatchResult) string {
	parts := make([]string, 0, 4)
	if result.Created > 0 {
		parts = append(parts, fmt.Sprintf("新增 %d", result.Created))
	}
	if result.Updated > 0 {
		parts = append(parts, fmt.Sprintf("修改 %d", result.Updated))
	}
	if result.Deleted > 0 {
		parts = append(parts, fmt.Sprintf("删除 %d", result.Deleted))
	}
	if result.Moved > 0 {
		parts = append(parts, fmt.Sprintf("移动 %d", result.Moved))
	}
	if len(parts) == 0 {
		parts = append(parts, "无变更")
	}
	return fmt.Sprintf("补丁已应用：%s；影响 %d 个路径", strings.Join(parts, "，"), result.FilesChanged)
}

// PatchStaleReason is the single reason string for a hunk whose old lines are
// no longer present in the file. Surfaces must not paraphrase it: the model
// copies the diagnostic back into the rebuilt patch, so the wording is part of
// the recovery contract.
const PatchStaleReason = "未找到期望旧内容"

// PatchStaleContextNextAction is the recovery hint every hunk miss carries.
// It names the structured fields (current_snippet / suggested_view_offset) so a
// caller can rebuild the hunk without another prose round trip.
func PatchStaleContextNextAction() string {
	return "STALE_CONTEXT: copy exact lines from current_snippet / “最接近的当前内容” (or re-view with suggested_view_offset), rebuild a smaller focused patch from that text (one file/region), and do not retry the same stale @@/old lines unchanged."
}

// PatchHunkDiagnostic renders the recovery message for a hunk that could not be
// located. The expected lines and the closest current window are both shown so
// the caller can rebuild the hunk by copying, not guessing.
//
// An empty header renders as "@@" (a hunk without a context anchor).
func PatchHunkDiagnostic(header string, expected, closest []string, startLine int, reason string) string {
	header = strings.TrimSpace(header)
	if header == "" {
		header = "@@"
	}
	current := ""
	if len(closest) > 0 {
		current = fmt.Sprintf(
			"\n最接近的当前内容（第 %d 行附近，可直接据此修正补丁）:\n%s",
			startLine,
			FormatPatchCurrentLines(closest, startLine),
		)
	}
	return fmt.Sprintf(
		"无法定位 hunk: %s；%s。next_action: 先用 view/grep 重读目标文件附近最新内容，按返回的“最接近的当前内容”重建补丁（一次只改一个文件/区域）；不要原样重试同一 stale @@/旧行。也可改用更短、更靠近目标的 @@ 上下文。\n期望内容:\n%s%s",
		header,
		reason,
		FormatPatchExpectedLines(expected),
		current,
	)
}

// FormatPatchExpectedLines renders the old-side lines a hunk asked for. The
// list is capped: a diagnostic that pastes a whole file competes with the
// context it is trying to repair.
func FormatPatchExpectedLines(lines []string) string {
	if len(lines) == 0 {
		return "(空旧内容)"
	}
	const maxLines = 12
	limit := len(lines)
	if limit > maxLines {
		limit = maxLines
	}
	preview := make([]string, 0, limit+1)
	for _, line := range lines[:limit] {
		preview = append(preview, TruncateDiagnosticText(line, 200))
	}
	if len(lines) > limit {
		preview = append(preview, fmt.Sprintf("... 省略 %d 行", len(lines)-limit))
	}
	return strings.Join(preview, "\n")
}

// FormatPatchCurrentLines renders the closest current window with stable line
// numbers. Full line text is kept (no mid-line truncation) so the caller can
// copy exact current bytes back into a hunk, matching the edit surface.
func FormatPatchCurrentLines(lines []string, startLine int) string {
	formatted := make([]string, 0, len(lines))
	for index, line := range lines {
		formatted = append(formatted, fmt.Sprintf("%6d|%s", startLine+index, line))
	}
	return strings.Join(formatted, "\n")
}

// PatchHunkErrorMetadata publishes the structured recovery fields a hunk miss
// carries, so a caller can act on them without parsing the message.
func PatchHunkErrorMetadata(hunkErr *PatchHunkError) map[string]any {
	if hunkErr == nil {
		return nil
	}
	metadata := map[string]any{"failure_class": "stale_context"}
	if path := strings.TrimSpace(hunkErr.Path); path != "" {
		metadata["file_path"] = path
	}
	if strings.TrimSpace(hunkErr.Header) != "" {
		metadata["hunk_header"] = strings.TrimSpace(hunkErr.Header)
	}
	if len(hunkErr.Closest) > 0 && hunkErr.StartLine > 0 {
		// view offsets are 0-based; the diagnostic reports 1-based lines.
		metadata["suggested_view_offset"] = hunkErr.StartLine - 1
		metadata["suggested_view_limit"] = 40
		// Structured copy-paste recovery: models often blind-retry apply_patch
		// after STALE, so expose the exact current lines instead of forcing an
		// extra view round trip when the window is already known.
		metadata["current_snippet"] = strings.Join(hunkErr.Closest, "\n")
		metadata["current_snippet_start_line"] = hunkErr.StartLine
	} else if startLine := ExtractPatchSuggestedViewLine(hunkErr.Message); startLine > 0 {
		// Fallback for wrapped / legacy messages that only embed line hints.
		metadata["suggested_view_offset"] = startLine - 1
		metadata["suggested_view_limit"] = 40
	}
	return metadata
}

// IsPatchStaleContextError reports a hunk whose @@ / old lines no longer match
// the file. Code-carrying errors are decided structurally; the message checks
// only catch wrapped spellings that lost their code.
func IsPatchStaleContextError(err error) bool {
	if err == nil {
		return false
	}
	var hunkErr *PatchHunkError
	if errors.As(err, &hunkErr) {
		return true
	}
	var coded *Error
	if errors.As(err, &coded) && coded.Code == CodeStaleContext {
		return true
	}
	message := err.Error()
	lower := strings.ToLower(message)
	return strings.Contains(message, "无法定位 hunk") ||
		strings.Contains(message, "未找到期望旧内容") ||
		strings.Contains(message, "未找到 @@ 上下文") ||
		(strings.Contains(lower, "hunk") && strings.Contains(lower, "stale")) ||
		strings.Contains(message, "stale @@")
}

// IsPatchInvalidSyntaxError reports parse/format failures that are the caller's
// own typo, not an execution fault. They must surface as TOOL_INVALID_ARGS:
// a malformed patch is never safe to blind-retry unchanged, and the generic
// execution verdict ("inspect the error details") hides the one thing the
// caller can fix.
func IsPatchInvalidSyntaxError(err error) bool {
	if err == nil || IsPatchStaleContextError(err) {
		return false
	}
	message := err.Error()
	lower := strings.ToLower(message)
	switch {
	case strings.Contains(message, "不是合法的 hunk"),
		strings.Contains(message, "不是合法的补丁"),
		strings.Contains(message, "不是合法的补丁操作头"),
		strings.Contains(message, "不是合法的新增文件内容"),
		strings.Contains(message, "hunk 没有内容"),
		strings.Contains(message, "更新文件没有内容变更"),
		strings.Contains(message, "新增文件没有内容"),
		strings.Contains(message, "缺少新增文件路径"),
		strings.Contains(message, "缺少更新文件路径"),
		strings.Contains(message, "缺少删除文件路径"),
		strings.Contains(message, "缺少移动目标路径"),
		strings.Contains(message, "patch 参数缺失"),
		strings.Contains(message, "补丁中没有可执行的文件操作"),
		strings.Contains(message, "补丁必须以"),
		strings.Contains(message, "补丁缺少"),
		strings.Contains(message, "应以 '+' 开头"):
		return true
	case strings.Contains(lower, "invalid patch"),
		strings.Contains(lower, "malformed patch"),
		// Envelope marker guidance errors always mention Begin/End Patch.
		strings.Contains(message, "Begin Patch") || strings.Contains(message, "End Patch"):
		return true
	default:
		return false
	}
}

// PatchFailureNextAction is the recovery hint for a failed patch call, chosen
// by failure class so the caller gets the one action that unblocks it.
func PatchFailureNextAction(err error) string {
	if err == nil {
		return ""
	}
	message := err.Error()
	lower := strings.ToLower(message)
	switch {
	case IsPatchStaleContextError(err):
		return PatchStaleContextNextAction()
	case strings.Contains(message, "Add File") || strings.Contains(message, "应以 '+' 开头"):
		return "For Add File hunks, every content line must start with '+'. Rewrite the add body with '+' prefixes, or use write/append_write for new files."
	case strings.Contains(message, "不是合法的 hunk") || strings.Contains(message, "hunk 没有内容") || strings.Contains(message, "更新文件没有内容变更"):
		return "TOOL_INVALID_ARGS: fix apply_patch syntax — each Update File needs @@ hunk headers and +/-/ context lines only (no nested Begin/End markers inside a hunk). Prefer one file/region; rebuild from a fresh view of the target."
	case strings.Contains(message, "不是合法的补丁") || strings.Contains(message, "Begin Patch") || strings.Contains(message, "End Patch"):
		return "Use Codex apply_patch markers: *** Begin Patch / *** Update|Add|Delete File / *** End Patch. Keep freeform patch text outside JSON when possible."
	case strings.Contains(message, "文件不存在") || strings.Contains(lower, "no such file") || strings.Contains(message, "访问文件失败") || strings.Contains(message, "解析补丁路径失败"):
		return "Confirm the target path with ls/glob/view first; fix path typos or create missing parents before retrying apply_patch."
	default:
		return "Inspect the apply_patch error, re-read the target file with view/grep, and retry with a smaller focused patch. Prefer one file/region per call."
	}
}

// ExtractPatchSuggestedViewLine reads the "第 %d 行附近" line hint out of a
// rendered patch diagnostic, for messages that lost their structured fields.
func ExtractPatchSuggestedViewLine(message string) int {
	const marker = "第 "
	idx := strings.Index(message, marker)
	if idx < 0 {
		return 0
	}
	rest := message[idx+len(marker):]
	end := strings.Index(rest, " 行")
	if end <= 0 {
		return 0
	}
	num := strings.TrimSpace(rest[:end])
	var line int
	for _, r := range num {
		if r < '0' || r > '9' {
			return 0
		}
		line = line*10 + int(r-'0')
	}
	return line
}

// PatchNotFoundMessage renders a missing-target failure with the same candidate
// hint every other local file tool publishes. An empty hint keeps the plain
// one-line form.
func PatchNotFoundMessage(prefix, rawPath, hint string) string {
	if strings.TrimSpace(hint) != "" {
		return fmt.Sprintf("%s: %s\n%s", prefix, rawPath, hint)
	}
	return fmt.Sprintf("%s: %s", prefix, rawPath)
}
