package commands

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	uidiff "github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/diff"
	runtimechatcore "github.com/wwsheng009/ai-agent-runtime/internal/chatcore"
	runtimeexecutor "github.com/wwsheng009/ai-agent-runtime/internal/executor"
	"github.com/wwsheng009/ai-agent-runtime/internal/pathdisplay"
	runtimepolicy "github.com/wwsheng009/ai-agent-runtime/internal/policy"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolresult"
)

var sharedChatToolPreviewKeys = []string{
	"command",
	"path",
	"file_path",
	"pattern",
	"query",
	"q",
	"url",
	"prompt",
	"name",
	"title",
	"task_id",
	"team_id",
	"id",
	"message",
	"input",
	"content",
}

func renderSharedChatToolEvent(event runtimechatcore.ChatEvent) string {
	payload := sharedChatToolPayload(event)
	toolSource := payloadStringValue(payload[toolresult.SourceKey])
	switch event.Stage {
	case "batch_start":
		return ""
	case "tool_requested":
		return appendCompactToolDirectory(renderCompactToolRequestedWithSource(event.ToolName, payloadStringValue(event.Arguments["command"]), payloadStringValue(payload["command_text"]), payloadStringValue(payload["arg_preview"]), toolSource), payload)
	case "tool_result":
		return renderCompactToolCompletedWithPayload(event.ToolName, payloadStringValue(event.Arguments["command"]), payloadStringValue(payload["command_text"]), payloadStringValue(payload["arg_preview"]), toolSource, chatToolSummaryLines(payload), payload)
	case "batch_end":
		return ""
	default:
		return ""
	}
}

func sharedChatToolPayload(event runtimechatcore.ChatEvent) map[string]interface{} {
	payload := map[string]interface{}{}
	if toolName := strings.TrimSpace(event.ToolName); toolName != "" {
		payload["tool_name"] = toolName
	}
	if preview := summarizeSharedChatToolCallArgs(event.ToolName, event.Arguments); preview != "" {
		payload["arg_preview"] = preview
	}
	if commandText := summarizeSharedShellToolCommand(event.ToolName, event.Arguments); commandText != "" {
		payload["command_text"] = commandText
	}
	if workdir := strings.TrimSpace(payloadStringValue(event.Arguments["workdir"])); workdir != "" {
		payload["workdir"] = workdir
	} else if workdir := strings.TrimSpace(payloadStringValue(event.Arguments["working_directory"])); workdir != "" {
		payload["workdir"] = workdir
	} else if cwd := strings.TrimSpace(payloadStringValue(event.Arguments["cwd"])); cwd != "" {
		payload["cwd"] = cwd
	} else if workdir := strings.TrimSpace(payloadStringValue(event.Metadata["workdir"])); workdir != "" {
		payload["workdir"] = workdir
	} else if workdir := strings.TrimSpace(payloadStringValue(event.Metadata["working_directory"])); workdir != "" {
		payload["workdir"] = workdir
	} else if cwd := strings.TrimSpace(payloadStringValue(event.Metadata["cwd"])); cwd != "" {
		payload["cwd"] = cwd
	}
	copySharedChatDisplayFilePath(payload, event.Arguments)
	if lines := summarizeSharedChatToolResultLines(event); len(lines) > 0 {
		payload["summary_lines"] = lines
		payload["summary"] = strings.Join(lines, "\n")
	}
	if output := editingSharedToolRenderOutput(event.ToolName, event.Output); output != "" {
		payload["render_output"] = output
		payload["render_output_format"] = "markdown"
		payload["render_output_untruncated"] = true
	} else if output := shellDiffSharedToolRenderOutput(event); output != "" {
		payload["render_output"] = output
		payload["render_output_format"] = "diff"
		payload["render_output_untruncated"] = true
	}
	if errText := strings.TrimSpace(event.Error); errText != "" {
		payload["error"] = errText
	}
	if source := payloadStringValue(event.Metadata[toolresult.SourceKey]); source != "" {
		payload[toolresult.SourceKey] = source
	}
	for _, key := range []string{"shell_type", "shell_path", "shell_display", "error_code", "next_action", "engine", "execution_backend", "backend_command", "backend_path", "backend_source"} {
		if value := payloadStringValue(event.Metadata[key]); value != "" {
			payload[key] = value
		}
	}
	copySharedSearchBackendMetadata(payload, event.Metadata)
	for _, key := range []string{"ok", "retryable"} {
		if value, ok := event.Metadata[key]; ok {
			payload[key] = value
		}
	}
	if durationMs := intPayloadValue(event.Metadata, "duration_ms"); durationMs > 0 {
		payload["duration_ms"] = durationMs
	}
	if event.Stage == "batch_end" {
		payload["awaiting_model"] = true
	}
	return payload
}

func copySharedChatDisplayFilePath(payload map[string]interface{}, args map[string]interface{}) {
	if payload == nil {
		return
	}
	_, path := pathdisplay.File(args)
	if pathdisplay.NeedsOwnLine(path) {
		payload["display_file_path"] = path
	}
}

func copySharedSearchBackendMetadata(payload, metadata map[string]interface{}) {
	if payload == nil || len(metadata) == 0 {
		return
	}
	if nested, ok := metadata["tool_metadata"].(map[string]interface{}); ok {
		copySharedSearchBackendMetadata(payload, nested)
	}
	for _, key := range []string{"engine", "execution_backend", "backend_command", "backend_path", "backend_source"} {
		if value := strings.TrimSpace(payloadStringValue(metadata[key])); value != "" {
			payload[key] = value
		}
	}
	toolName := strings.ToLower(strings.TrimSpace(payloadStringValue(payload["tool_name"])))
	if payloadStringValue(payload["execution_backend"]) == "" && (toolName == "grep" || toolName == "glob") {
		if engine := strings.TrimSpace(payloadStringValue(payload["engine"])); engine != "" {
			payload["execution_backend"] = engine
		}
	}
}

func summarizeSharedChatToolCallArgs(toolName string, args map[string]interface{}) string {
	if len(args) == 0 {
		return ""
	}
	if preview := summarizeSharedSearchToolCallArgs(toolName, args); preview != "" {
		return preview
	}

	fileArgKey, filePath := pathdisplay.File(args)
	parts := make([]string, 0, len(args))
	seen := make(map[string]struct{}, len(sharedChatToolPreviewKeys))
	for _, key := range sharedChatToolPreviewKeys {
		seen[key] = struct{}{}
		if preview := formatSharedChatToolArgPreviewForArgs(key, args[key], fileArgKey, filePath); preview != "" {
			parts = append(parts, preview)
		}
	}

	keys := make([]string, 0, len(args))
	for key := range args {
		if _, ok := seen[key]; ok {
			continue
		}
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		if sharedChatToolArgRenderedSeparately(key) {
			continue
		}
		if preview := formatSharedChatToolArgPreviewForArgs(key, args[key], fileArgKey, filePath); preview != "" {
			parts = append(parts, preview)
		}
	}
	return truncateChatRuntimeText(strings.Join(parts, " "), 200)
}

func sharedChatToolArgRenderedSeparately(key string) bool {
	switch strings.ToLower(strings.TrimSpace(key)) {
	case "cwd", "workdir", "working_directory":
		return true
	default:
		return false
	}
}

func summarizeSharedSearchToolCallArgs(toolName string, args map[string]interface{}) string {
	var keys []string
	switch strings.ToLower(strings.TrimSpace(toolName)) {
	case "glob":
		keys = []string{"pattern", "path", "case_insensitive", "ignore_case", "limit"}
	case "grep":
		keys = []string{"patterns", "pattern", "regexp", "paths", "path", "glob", "include", "type", "literal", "ignore_case"}
	default:
		return ""
	}

	parts := make([]string, 0, len(args))
	seen := make(map[string]struct{}, len(keys))
	for _, key := range keys {
		seen[key] = struct{}{}
		if preview := formatSharedSearchToolArgPreview(key, args[key]); preview != "" {
			parts = append(parts, preview)
		}
	}

	remainingKeys := make([]string, 0, len(args)-len(parts))
	for key := range args {
		if _, ok := seen[key]; ok || sharedChatToolArgRenderedSeparately(key) {
			continue
		}
		remainingKeys = append(remainingKeys, key)
	}
	sort.Strings(remainingKeys)
	for _, key := range remainingKeys {
		if preview := formatSharedChatToolArgPreview(key, args[key]); preview != "" {
			parts = append(parts, preview)
		}
	}
	return truncateChatRuntimeText(strings.Join(parts, " "), 200)
}

func formatSharedSearchToolArgPreview(key string, value interface{}) string {
	key = strings.TrimSpace(key)
	if key == "" || value == nil {
		return ""
	}
	text := normalizeSharedChatToolText(renderSharedSearchToolArgValue(value))
	if text == "" || text == "{}" || text == "[]" {
		return ""
	}
	return key + "=" + text
}

func renderSharedSearchToolArgValue(value interface{}) string {
	switch value.(type) {
	case []string, []interface{}:
		raw, err := json.Marshal(value)
		if err == nil {
			return string(raw)
		}
	}
	return renderSharedChatToolArgValue(value)
}

func formatSharedChatToolArgPreview(key string, value interface{}) string {
	key = strings.TrimSpace(key)
	if key == "" || value == nil {
		return ""
	}
	if sensitiveSharedChatToolArgKey(key) {
		return key + "=<redacted>"
	}
	text := normalizeSharedChatToolText(renderSharedChatToolArgValue(value))
	if text == "" || text == "{}" || text == "[]" {
		return ""
	}
	return truncateChatRuntimeText(fmt.Sprintf("%s=%s", key, text), 72)
}

func formatSharedChatToolArgPreviewForArgs(key string, value interface{}, fileArgKey, filePath string) string {
	if key == fileArgKey && filePath != "" {
		if pathdisplay.NeedsOwnLine(filePath) {
			return ""
		}
		value = filePath
	}
	return formatSharedChatToolArgPreview(key, value)
}

func sensitiveSharedChatToolArgKey(key string) bool {
	normalized := strings.ToLower(strings.TrimSpace(key))
	for _, marker := range []string{
		"api_key", "apikey", "access_key", "private_key", "authorization",
		"credential", "password", "passwd", "secret", "cookie",
	} {
		if strings.Contains(normalized, marker) {
			return true
		}
	}
	return strings.HasSuffix(normalized, "token")
}

func renderSharedChatToolArgValue(value interface{}) string {
	switch typed := value.(type) {
	case string:
		return typed
	case bool:
		return fmt.Sprintf("%t", typed)
	case []string:
		switch len(typed) {
		case 0:
			return ""
		case 1:
			return typed[0]
		default:
			return fmt.Sprintf("[%d]", len(typed))
		}
	case []interface{}:
		switch len(typed) {
		case 0:
			return ""
		case 1:
			return renderSharedChatToolArgValue(typed[0])
		default:
			return fmt.Sprintf("[%d]", len(typed))
		}
	case map[string]interface{}:
		if len(typed) == 0 {
			return ""
		}
		for _, nestedKey := range sharedChatToolPreviewKeys {
			if preview := formatSharedChatToolArgPreview(nestedKey, typed[nestedKey]); preview != "" {
				return strings.TrimPrefix(preview, nestedKey+"=")
			}
		}
		raw, err := json.Marshal(toolresult.CompactAttemptedArgs(typed))
		if err != nil {
			return fmt.Sprintf("%v", value)
		}
		return string(raw)
	default:
		return fmt.Sprintf("%v", value)
	}
}

func summarizeSharedChatToolResultLines(event runtimechatcore.ChatEvent) []string {
	maxLines, maxBytes := sharedChatToolResultPreviewLimits(event.ToolName)
	if summary := strings.TrimSpace(truncateOutputPreview(event.Output, maxLines, maxBytes)); summary != "" {
		lines := make([]string, 0, maxLines)
		for _, line := range strings.Split(strings.ReplaceAll(summary, "\r\n", "\n"), "\n") {
			normalized := normalizeSharedChatToolText(line)
			if normalized == "" {
				continue
			}
			lines = append(lines, normalized)
			if len(lines) == maxLines {
				return lines
			}
		}
		if len(lines) > 0 {
			return lines
		}
	}
	if errText := strings.TrimSpace(event.Error); errText != "" {
		return []string{"failed: " + normalizeSharedChatToolText(errText)}
	}
	return nil
}

func sharedChatToolResultPreviewLimits(toolName string) (int, int) {
	if strings.EqualFold(strings.TrimSpace(toolName), "todos") {
		return 32, 4096
	}
	return 3, 360
}

func summarizeSharedShellToolCommand(toolName string, args map[string]interface{}) string {
	if !runtimepolicy.IsShellLikeToolName(strings.TrimSpace(toolName)) || len(args) == 0 {
		return ""
	}
	command := normalizeSharedChatToolText(renderSharedChatToolArgValue(args["command"]))
	if command == "" {
		return ""
	}
	return truncateChatRuntimeText(command, 200)
}

func normalizeSharedChatToolText(text string) string {
	return strings.Join(strings.Fields(strings.TrimSpace(text)), " ")
}

func renderCompactToolRequested(toolName, commandArg, commandText, argPreview string) string {
	return renderCompactToolRequestedWithSource(toolName, commandArg, commandText, argPreview, "")
}

func renderCompactToolRequestedWithSource(toolName, commandArg, commandText, argPreview, toolSource string) string {
	display := compactToolDisplayTextWithSource(toolName, commandArg, commandText, argPreview, toolSource)
	if display == "" {
		return ""
	}
	return "• Running " + display
}

func renderCompactToolCompleted(toolName, commandArg, commandText, argPreview string, summaryLines []string) string {
	return renderCompactToolCompletedWithSource(toolName, commandArg, commandText, argPreview, "", summaryLines)
}

func renderCompactToolCompletedWithSource(toolName, commandArg, commandText, argPreview, toolSource string, summaryLines []string) string {
	return renderCompactToolCompletedWithPayload(toolName, commandArg, commandText, argPreview, toolSource, summaryLines, nil)
}

func renderCompactToolCompletedWithPayload(toolName, commandArg, commandText, argPreview, toolSource string, summaryLines []string, payload map[string]interface{}) string {
	display := compactToolDisplayTextWithSource(toolName, commandArg, commandText, argPreview, toolSource)
	if display == "" {
		return ""
	}
	if rendered := renderStructuredDiffToolOutput(payload); rendered != "" {
		return rendered
	}
	lines := []string{compactToolCompletionTitle(payload, display)}
	lines = append(lines, compactToolContextLines(payload)...)
	if renderedLines := renderMarkdownToolOutputLines(payload); len(renderedLines) > 0 {
		lines = append(lines, renderedLines...)
		return strings.Join(indentToolResultTree(lines), "\n")
	}
	outputLines := compactToolOutputLines(summaryLines)
	if len(outputLines) == 0 {
		outputLines = []string{"(no output)"}
	}
	for _, line := range outputLines {
		lines = append(lines, line)
	}
	return strings.Join(indentToolResultTree(lines), "\n")
}

// indentToolResultTree 把工具结果块应用内部层级缩进（树形结果块）：
// 首行是工具执行摘要（marker 顶格），内容行从第二行起整体缩进 2 空格，
// 中间行用竖线 "│" 标记、最后一行用收尾符号 "└" 标记。内容行自带的一层
// "  " 前缀先剥掉，再统一挂树形标记，保证 "  └  27 lines" 形态对齐。
func indentToolResultTree(lines []string) []string {
	if len(lines) <= 1 {
		return lines
	}
	out := make([]string, 0, len(lines))
	out = append(out, lines[0])
	for i := 1; i < len(lines); i++ {
		marker := "│"
		if i == len(lines)-1 {
			marker = "└"
		}
		out = append(out, "  "+marker+"  "+strings.TrimPrefix(lines[i], "  "))
	}
	return out
}

func renderMarkdownToolOutput(payload map[string]interface{}) string {
	if rendered := renderStructuredDiffToolOutput(payload); rendered != "" {
		return rendered
	}
	lines := renderMarkdownToolOutputLines(payload)
	if len(lines) == 0 {
		return ""
	}
	return strings.Join(lines, "\n")
}

func renderStructuredDiffToolOutput(payload map[string]interface{}) string {
	if output, ok := diffToolOutput(payload); ok {
		return renderDiffOutput(output, "Diff")
	}
	if output, ok := markdownToolOutput(payload); ok {
		return renderDiffOutput(output, "Edited")
	}
	return ""
}

func renderMarkdownToolOutputLines(payload map[string]interface{}) []string {
	output, ok := markdownToolOutput(payload)
	if !ok {
		return nil
	}
	if rendered := renderEditedDiffOutput(output); rendered != "" {
		return strings.Split(rendered, "\n")
	}
	lines := strings.Split(output, "\n")
	for i, line := range lines {
		lines[i] = "  " + line
	}
	return lines
}

func markdownToolOutput(payload map[string]interface{}) (string, bool) {
	if payload == nil {
		return "", false
	}
	if format := strings.TrimSpace(payloadStringValue(payload["render_output_format"])); format != "" && format != "markdown" {
		return "", false
	}
	return untruncatedToolRenderOutput(payload)
}

func diffToolOutput(payload map[string]interface{}) (string, bool) {
	if payload == nil {
		return "", false
	}
	format := strings.TrimSpace(payloadStringValue(payload["render_output_format"]))
	if format != "diff" && format != "unified_diff" {
		return "", false
	}
	return untruncatedToolRenderOutput(payload)
}

func untruncatedToolRenderOutput(payload map[string]interface{}) (string, bool) {
	if !payloadBoolValue(payload, "render_output_untruncated") {
		return "", false
	}
	output := strings.TrimRight(strings.ReplaceAll(payloadStringValue(payload["render_output"]), "\r\n", "\n"), "\n")
	if strings.TrimSpace(output) == "" {
		return "", false
	}
	return output, true
}

func renderEditedDiffOutput(output string) string {
	return renderDiffOutput(output, "Edited")
}

func renderDiffOutput(output, label string) string {
	diff := extractUnifiedDiffOutput(output)
	if strings.TrimSpace(diff) == "" {
		return ""
	}
	files := parseUnifiedDiffFiles(diff)
	if len(files) == 0 {
		return ""
	}
	lines := make([]string, 0, 32)
	for fileIndex, file := range files {
		if fileIndex > 0 {
			lines = append(lines, "  ")
		}
		lines = append(lines, fmt.Sprintf("• %s %s (+%d -%d)", label, file.path, file.additions, file.deletions))
		for _, line := range file.lines {
			lines = append(lines, "    "+line)
		}
	}
	return strings.Join(lines, "\n")
}

// extractFencedDiff pulls the body of the first ```diff fenced block out of a
// tool output like apply_patch's "文件差异:\n```diff\n...\n```".
//
// The closing fence must be a BARE ``` line (no leading space, + or - prefix).
// Diff body rows always carry a unified-diff prefix (" ", "+", "-", "@@") or
// are empty context lines, so a Markdown code fence inside the diff body
// (" ```go" context row, "-```"/"+```" add/delete row) must not be mistaken
// for the closing fence — that used to truncate the diff at the first inner
// fence and silently drop every later hunk ("only the start renders").
func extractFencedDiff(output string) string {
	normalized := strings.ReplaceAll(output, "\r\n", "\n")
	lines := strings.Split(normalized, "\n")
	start := -1
	for i, line := range lines {
		if strings.HasPrefix(strings.TrimSpace(line), "```diff") {
			start = i
			break
		}
	}
	if start < 0 {
		return ""
	}
	var out []string
	for _, line := range lines[start+1:] {
		if strings.HasPrefix(line, "```") {
			break
		}
		out = append(out, line)
	}
	return strings.Trim(strings.Join(out, "\n"), "\n")
}

func extractUnifiedDiffOutput(output string) string {
	if diff := extractFencedDiff(output); strings.TrimSpace(diff) != "" {
		return diff
	}
	normalized := strings.ReplaceAll(output, "\r\n", "\n")
	normalized = strings.ReplaceAll(normalized, "\r", "\n")
	lines := strings.Split(normalized, "\n")
	start := -1
	for i := 0; i+2 < len(lines); i++ {
		if isUnifiedDiffFileStart(lines, i) {
			start = i
			break
		}
	}
	if start < 0 {
		return ""
	}
	collected := collectUnifiedDiffLines(lines[start:])
	return strings.Trim(strings.Join(collected, "\n"), "\n")
}

func isUnifiedDiffFileStart(lines []string, i int) bool {
	return i+2 < len(lines) &&
		strings.HasPrefix(strings.TrimSpace(lines[i]), "--- ") &&
		strings.HasPrefix(strings.TrimSpace(lines[i+1]), "+++ ") &&
		strings.HasPrefix(strings.TrimSpace(lines[i+2]), "@@")
}

func collectUnifiedDiffLines(lines []string) []string {
	out := make([]string, 0, len(lines))
	inHunk := false
	sawDiff := false
	for i := 0; i < len(lines); i++ {
		line := strings.TrimRight(lines[i], "\r")
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "diff --git ") {
			// A new file starts after the previous hunk. Reset the hunk state
			// before skipping Git's per-file metadata (index/mode/rename rows).
			inHunk = false
			continue
		}
		if isUnifiedDiffFileStart(lines, i) {
			out = append(out, line)
			inHunk = false
			sawDiff = true
			continue
		}
		if len(out) > 0 && strings.HasPrefix(trimmed, "+++ ") {
			out = append(out, line)
			continue
		}
		if len(out) > 0 && strings.HasPrefix(trimmed, "@@") {
			out = append(out, line)
			inHunk = true
			sawDiff = true
			continue
		}
		if inHunk {
			if line == `\ No newline at end of file` {
				out = append(out, line)
				continue
			}
			if i+2 < len(lines) && isUnifiedDiffFileStart(lines, i) {
				inHunk = false
				i--
				continue
			}
			if line == "" {
				break
			}
			if isUnifiedDiffContinuation(line) {
				out = append(out, line)
				continue
			}
			break
		}
		if sawDiff && trimmed != "" {
			if isUnifiedDiffFileMetadata(trimmed) {
				continue
			}
			break
		}
	}
	return out
}

func isUnifiedDiffFileMetadata(line string) bool {
	for _, prefix := range []string{
		"index ",
		"old mode ",
		"new mode ",
		"deleted file mode ",
		"new file mode ",
		"similarity index ",
		"dissimilarity index ",
		"rename from ",
		"rename to ",
		"copy from ",
		"copy to ",
	} {
		if strings.HasPrefix(line, prefix) {
			return true
		}
	}
	return false
}

func isUnifiedDiffContinuation(line string) bool {
	if line == "" {
		return false
	}
	switch line[0] {
	case ' ', '+', '-':
		return true
	default:
		return false
	}
}

type renderedDiffFile struct {
	path      string
	additions int
	deletions int
	lines     []string
}

// renderedDiffElisionRow marks a gap in the numbered preview: between
// non-adjacent hunks, or at the end when the parse budget stopped early. The
// structured renderer keeps it as a meta row, so both transcript forms agree.
const renderedDiffElisionRow = "      ..."

// parseUnifiedDiffFiles projects a unified diff onto the numbered "• Edited"
// supplement rows.
//
// Parsing is delegated to the ui/diff package so the transcript text and the
// structured renderer that later re-reads it agree on file boundaries, line
// numbering and hunk grouping instead of maintaining a second parser here.
func parseUnifiedDiffFiles(diff string) []renderedDiffFile {
	parsed, truncated := uidiff.ParseUnifiedWithLimit(diff, uidiff.DefaultParseOptions())
	files := make([]renderedDiffFile, 0, len(parsed))
	for _, file := range parsed {
		rendered := renderedDiffFile{path: renderedDiffFilePath(file)}
		for hunkIndex, hunk := range file.Hunks {
			if hunkIndex > 0 {
				rendered.lines = append(rendered.lines, renderedDiffElisionRow)
			}
			for _, row := range hunk.Lines {
				switch row.Kind {
				case uidiff.LineAdd:
					rendered.additions++
					rendered.lines = append(rendered.lines,
						formatRenderedDiffLine(0, '+', row.NewLineNo, row.Text))
				case uidiff.LineDelete:
					rendered.deletions++
					rendered.lines = append(rendered.lines,
						formatRenderedDiffLine(row.OldLineNo, '-', 0, row.Text))
				case uidiff.LineContext:
					rendered.lines = append(rendered.lines,
						formatRenderedDiffLine(row.OldLineNo, ' ', row.NewLineNo, row.Text))
				default:
					// Meta rows such as "\ No newline at end of file" carry no
					// line number and are not part of the numbered preview.
				}
			}
		}
		if rendered.path == "" && len(rendered.lines) == 0 {
			continue
		}
		files = append(files, rendered)
	}
	// The parse budget can stop mid-diff. Reuse the elision marker so the
	// preview ends with a visible "there is more" row in both the plain
	// transcript and the structured re-render, which keeps the marker as a meta
	// row.
	if truncated && len(files) > 0 {
		last := &files[len(files)-1]
		last.lines = append(last.lines, renderedDiffElisionRow)
	}
	return files
}

// renderedDiffFilePath prefers the post-edit path and falls back to the
// pre-edit one for deletions, where the new side is /dev/null.
func renderedDiffFilePath(file uidiff.FileDiff) string {
	if !isRenderedDiffDevNull(file.NewPath) {
		if path := normalizeRenderedDiffPath(file.NewPath); path != "" {
			return path
		}
	}
	return normalizeRenderedDiffPath(file.OldPath)
}

func normalizeRenderedDiffPath(path string) string {
	path = strings.TrimSpace(path)
	path = strings.TrimPrefix(path, "a/")
	path = strings.TrimPrefix(path, "b/")
	path = strings.ReplaceAll(path, "/", `\`)
	return path
}

func isRenderedDiffDevNull(path string) bool {
	return strings.TrimSpace(path) == "/dev/null"
}

func formatRenderedDiffLine(oldLine int, marker rune, newLine int, text string) string {
	lineNumber := oldLine
	if marker == '+' || (marker == ' ' && newLine > 0) {
		lineNumber = newLine
	}
	if marker == ' ' {
		return fmt.Sprintf("%5d   %s", lineNumber, text)
	}
	return fmt.Sprintf("%5d %c %s", lineNumber, marker, text)
}

func editingSharedToolRenderOutput(toolName string, output string) string {
	normalizedToolName := strings.TrimSpace(toolName)
	switch normalizedToolName {
	case "edit", "apply", "apply_patch", "patch":
	default:
		lowerToolName := strings.ToLower(normalizedToolName)
		if !strings.Contains(lowerToolName, "edit") && !strings.Contains(lowerToolName, "apply") && !strings.Contains(lowerToolName, "patch") {
			return ""
		}
	}
	return strings.TrimSpace(output)
}

func shellDiffSharedToolRenderOutput(event runtimechatcore.ChatEvent) string {
	if !runtimepolicy.IsShellLikeToolName(strings.TrimSpace(event.ToolName)) ||
		!runtimeexecutor.IsGitDiffCommand(payloadStringValue(event.Arguments["command"])) ||
		strings.TrimSpace(event.Error) != "" {
		return ""
	}
	if complete, ok := event.Metadata["output_capture_complete"].(bool); ok && !complete {
		return ""
	}
	if limited, _ := event.Metadata["capture_limit_reached"].(bool); limited {
		return ""
	}
	output := strings.TrimSpace(event.Output)
	if !runtimeexecutor.LooksLikeUnifiedDiffOutput(output) {
		return ""
	}
	return output
}

func compactToolDisplayTextWithSource(toolName, commandArg, commandText, argPreview, toolSource string) string {
	display := compactToolDisplayText(toolName, commandArg, commandText, argPreview)
	prefix := compactToolSourcePrefix(toolSource)
	if prefix == "" {
		return display
	}
	if display == "" {
		return strings.TrimSpace(prefix)
	}
	return prefix + display
}

func compactToolDisplayText(toolName, commandArg, commandText, argPreview string) string {
	toolName = strings.TrimSpace(toolName)
	if runtimepolicy.IsShellLikeToolName(toolName) {
		command := firstNonEmptyChatValue(
			compactToolDisplaySegment(commandArg),
			compactToolDisplaySegment(commandText),
			compactToolDisplaySegment(extractCommandPreview(argPreview)),
		)
		if command != "" {
			return truncateChatRuntimeText(command, 200)
		}
	}
	if preview := compactToolDisplaySegment(argPreview); preview != "" {
		if toolName != "" {
			return truncateChatRuntimeText(toolName+" "+preview, 200)
		}
		return truncateChatRuntimeText(preview, 200)
	}
	if toolName != "" {
		return truncateChatRuntimeText(toolName, 200)
	}
	return ""
}

func compactToolSourcePrefix(toolSource string) string {
	switch toolresult.NormalizeSource(toolSource) {
	case toolresult.SourceMeta:
		return "[meta] "
	case toolresult.SourceMCP:
		return "[mcp] "
	case toolresult.SourceBroker:
		return "[broker] "
	default:
		return ""
	}
}

func compactToolOutputLines(summaryLines []string) []string {
	if len(summaryLines) == 0 {
		return nil
	}
	out := make([]string, 0, len(summaryLines))
	for _, line := range summaryLines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		out = append(out, trimmed)
	}
	if len(out) == 1 && strings.EqualFold(normalizeSharedChatToolText(out[0]), "Tool returned no output.") {
		return nil
	}
	return out
}

func compactToolDurationSuffix(payload map[string]interface{}) string {
	if payload == nil {
		return ""
	}
	durationMs := intPayloadValue(payload, "duration_ms")
	if durationMs <= 0 {
		return ""
	}
	return " in " + durationString(time.Duration(durationMs)*time.Millisecond)
}

func compactToolCompletionTitle(payload map[string]interface{}, display string) string {
	status := "Completed"
	if strings.TrimSpace(payloadStringValue(payload["error"])) != "" {
		status = "Failed"
	}
	return "• " + status + " " + display + compactToolBackendSuffix(payload) + compactToolDurationSuffix(payload)
}

func compactToolBackendSuffix(payload map[string]interface{}) string {
	if payload == nil {
		return ""
	}
	backend := firstNonEmptyChatValue(
		payloadStringValue(payload["execution_backend"]),
		payloadStringValue(payload["engine"]),
	)
	backend = compactToolDisplaySegment(backend)
	if backend == "" {
		return ""
	}
	return " via " + truncateChatRuntimeText(backend, 40)
}

func compactToolDisplaySegment(text string) string {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")
	return strings.Join(strings.Fields(strings.TrimSpace(text)), " ")
}

func extractCommandPreview(argPreview string) string {
	argPreview = strings.TrimSpace(argPreview)
	if !strings.HasPrefix(argPreview, "command=") {
		return ""
	}
	return strings.TrimSpace(strings.TrimPrefix(argPreview, "command="))
}
