package agent

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/internal/output"
	runtimepolicy "github.com/wwsheng009/ai-agent-runtime/internal/policy"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolresult"
	"github.com/wwsheng009/ai-agent-runtime/internal/types"
)

var preferredToolArgPreviewKeys = []string{
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

func toolRequestedEventPayload(call types.ToolCall, step int, traceID string, extra map[string]interface{}) map[string]interface{} {
	payload := map[string]interface{}{
		"tool_call_id": call.ID,
		"step":         step,
		"trace_id":     traceID,
	}
	if preview := summarizeToolCallArgs(call.Args); preview != "" {
		payload["arg_preview"] = preview
	}
	if commandText := summarizeShellToolCommand(call.Name, call.Args); commandText != "" {
		payload["command_text"] = commandText
	}
	copyToolExecutionDirectory(payload, call.Args)
	mergeToolEventPayload(payload, extra)
	return payload
}

func toolCompletedEventPayload(result toolExecutionResult, step int, traceID string, extra map[string]interface{}) map[string]interface{} {
	payload := map[string]interface{}{
		"tool_call_id": result.Call.ID,
		"step":         step,
		"error":        result.Error,
		"trace_id":     traceID,
	}
	if preview := summarizeToolCallArgs(result.Call.Args); preview != "" {
		payload["arg_preview"] = preview
	}
	if commandText := summarizeShellToolCommand(result.Call.Name, result.Call.Args); commandText != "" {
		payload["command_text"] = commandText
	}
	copyToolExecutionDirectory(payload, result.Call.Args)
	if summaryLines := summarizeToolExecutionLines(result); len(summaryLines) > 0 {
		payload["summary"] = strings.Join(summaryLines, "\n")
		payload["summary_lines"] = append([]string(nil), summaryLines...)
	}
	if result.Envelope != nil {
		if source := toolresult.SourceFromMetadata(result.Envelope.Metadata); source != "" {
			payload[toolresult.SourceKey] = source
		}
	}
	mergeToolEventPayload(payload, extra)
	return payload
}

func copyToolExecutionDirectory(payload map[string]interface{}, args map[string]interface{}) {
	if payload == nil || len(args) == 0 {
		return
	}
	if workdir := normalizeToolEventText(renderToolArgValue(args["workdir"])); workdir != "" && workdir != "<nil>" {
		payload["workdir"] = truncateToolEventText(workdir, 200)
		return
	}
	if cwd := normalizeToolEventText(renderToolArgValue(args["cwd"])); cwd != "" && cwd != "<nil>" {
		payload["cwd"] = truncateToolEventText(cwd, 200)
	}
}

func mergeToolEventPayload(payload map[string]interface{}, extra map[string]interface{}) {
	for key, value := range extra {
		if strings.TrimSpace(key) == "" {
			continue
		}
		payload[key] = value
	}
}

func summarizeToolCallArgs(args map[string]interface{}) string {
	if len(args) == 0 {
		return ""
	}

	seen := make(map[string]struct{}, len(preferredToolArgPreviewKeys))
	for _, key := range preferredToolArgPreviewKeys {
		seen[key] = struct{}{}
		if preview := formatSingleToolArgPreview(key, args[key]); preview != "" {
			return preview
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
		if preview := formatSingleToolArgPreview(key, args[key]); preview != "" {
			return preview
		}
	}
	return ""
}

func summarizeShellToolCommand(toolName string, args map[string]interface{}) string {
	if !runtimepolicy.IsShellLikeToolName(strings.TrimSpace(toolName)) || len(args) == 0 {
		return ""
	}
	command := normalizeToolEventText(renderToolArgValue(args["command"]))
	if command == "" {
		return ""
	}
	return truncateToolEventText(command, 200)
}

func formatSingleToolArgPreview(key string, value interface{}) string {
	key = strings.TrimSpace(key)
	if key == "" || value == nil {
		return ""
	}

	text := normalizeToolEventText(renderToolArgValue(value))
	if text == "" || text == "{}" || text == "[]" {
		return ""
	}
	return truncateToolEventText(fmt.Sprintf("%s=%s", key, text), 72)
}

func renderToolArgValue(value interface{}) string {
	switch typed := value.(type) {
	case nil:
		return ""
	case string:
		return typed
	case bool:
		if !typed {
			return ""
		}
		return "true"
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
			return renderToolArgValue(typed[0])
		default:
			return fmt.Sprintf("[%d]", len(typed))
		}
	case map[string]interface{}:
		if len(typed) == 0 {
			return ""
		}
		for _, nestedKey := range preferredToolArgPreviewKeys {
			if preview := formatSingleToolArgPreview(keyWithPrefix("", nestedKey), typed[nestedKey]); preview != "" {
				return strings.TrimPrefix(preview, nestedKey+"=")
			}
		}
		raw, err := json.Marshal(typed)
		if err != nil {
			return fmt.Sprintf("%v", value)
		}
		return string(raw)
	default:
		return fmt.Sprintf("%v", value)
	}
}

func keyWithPrefix(prefix, key string) string {
	if strings.TrimSpace(prefix) == "" {
		return strings.TrimSpace(key)
	}
	if strings.TrimSpace(key) == "" {
		return strings.TrimSpace(prefix)
	}
	return strings.TrimSpace(prefix) + "." + strings.TrimSpace(key)
}

func summarizeToolExecutionLines(result toolExecutionResult) []string {
	if lines := summarizeToolTextLines(extractToolTextOutput(result.Output)); len(lines) > 0 {
		return lines
	}
	errText := normalizeToolEventText(result.Error)
	if result.Envelope != nil {
		if lines := summarizeToolTextLines(result.Envelope.Summary); len(lines) > 0 {
			if errText == "" || !isGenericToolFailureSummary(result.Envelope.Summary, firstNonEmptyToolRuntimeValue(result.Call.Name, result.Envelope.ToolName)) {
				return lines
			}
		}
	}
	if errText != "" {
		return []string{truncateToolEventText("failed: "+errText, 120)}
	}
	if summary := summarizeToolMetadata(toolMetadataFromEnvelope(result.Envelope)); summary != "" {
		return []string{summary}
	}
	return nil
}

func isGenericToolFailureSummary(summary, toolName string) bool {
	normalized := strings.ToLower(normalizeToolEventText(summary))
	if normalized == "" {
		return false
	}
	if normalized == "tool returned no output." {
		return true
	}
	if strings.HasPrefix(normalized, "tool ") && strings.HasSuffix(normalized, " failed before producing output.") {
		return true
	}
	if strings.TrimSpace(toolName) == "" {
		return false
	}
	expected := strings.ToLower(normalizeToolEventText(fmt.Sprintf("Tool %s failed before producing output.", strings.TrimSpace(toolName))))
	return normalized == expected
}

func firstNonEmptyToolRuntimeValue(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func toolMetadataFromEnvelope(envelope *output.Envelope) map[string]interface{} {
	if envelope == nil || len(envelope.Metadata) == 0 {
		return nil
	}
	raw, ok := envelope.Metadata["tool_metadata"].(map[string]interface{})
	if !ok || len(raw) == 0 {
		return nil
	}
	return raw
}

func summarizeToolMetadata(metadata map[string]interface{}) string {
	if len(metadata) == 0 {
		return ""
	}

	if fileCount, fileOK := intToolMetadataValue(metadata["file_count"]); fileOK {
		dirCount, dirOK := intToolMetadataValue(metadata["dir_count"])
		parts := []string{formatCountSummary(fileCount, "file", "files")}
		if dirOK {
			parts = append(parts, formatCountSummary(dirCount, "dir", "dirs"))
		}
		if truncatedToolMetadata(metadata["truncated"]) {
			parts = append(parts, "truncated")
		}
		return strings.Join(parts, ", ")
	}

	if matchCount, ok := intToolMetadataValue(metadata["match_count"]); ok {
		parts := []string{formatCountSummary(matchCount, "match", "matches")}
		if truncatedToolMetadata(metadata["truncated"]) {
			parts = append(parts, "truncated")
		}
		return strings.Join(parts, ", ")
	}

	if linesRead, ok := intToolMetadataValue(metadata["lines_read"]); ok {
		parts := []string{formatCountSummary(linesRead, "line", "lines")}
		if truncatedToolMetadata(metadata["is_truncated"]) {
			parts = append(parts, "truncated")
		}
		return strings.Join(parts, ", ")
	}

	if total, ok := intToolMetadataValue(metadata["total"]); ok {
		parts := []string{formatCountSummary(total, "item", "items")}
		if truncatedToolMetadata(metadata["truncated"]) {
			parts = append(parts, "truncated")
		}
		return strings.Join(parts, ", ")
	}

	return ""
}

func truncatedToolMetadata(value interface{}) bool {
	boolean, ok := value.(bool)
	return ok && boolean
}

func intToolMetadataValue(value interface{}) (int, bool) {
	switch typed := value.(type) {
	case int:
		return typed, true
	case int32:
		return int(typed), true
	case int64:
		return int(typed), true
	case float64:
		return int(typed), true
	default:
		return 0, false
	}
}

func formatCountSummary(count int, singular, plural string) string {
	if count == 1 {
		return fmt.Sprintf("1 %s", singular)
	}
	return fmt.Sprintf("%d %s", count, plural)
}

func extractToolTextOutput(value interface{}) string {
	switch typed := value.(type) {
	case string:
		return typed
	case []byte:
		return string(typed)
	case fmt.Stringer:
		return typed.String()
	default:
		return ""
	}
}

func summarizeToolTextLines(text string) []string {
	text = strings.TrimSpace(strings.ReplaceAll(text, "\r\n", "\n"))
	if text == "" {
		return nil
	}

	lines := make([]string, 0, 6)
	for _, rawLine := range strings.Split(text, "\n") {
		trimmed := strings.TrimSpace(rawLine)
		if trimmed == "" {
			continue
		}
		if strings.EqualFold(trimmed, "Metadata:") {
			break
		}
		if strings.HasPrefix(trimmed, "artifact_refs:") {
			continue
		}
		normalized := truncateToolEventText(normalizeToolEventText(trimmed), 120)
		if normalized == "" {
			continue
		}
		if len(lines) == 0 || lines[len(lines)-1] != normalized {
			lines = append(lines, normalized)
		}
	}
	if len(lines) == 0 {
		return nil
	}
	if len(lines) == 1 {
		if split := splitToolSummaryLine(lines[0]); len(split) > 1 {
			lines = split
		}
	}
	return selectToolSummaryLines(lines)
}

func splitToolSummaryLine(line string) []string {
	replacements := []struct {
		old string
		new string
	}{
		{old: ". Fields:", new: ".\nFields:"},
		{old: ". Keys:", new: ".\nKeys:"},
		{old: ". Summary:", new: ".\nSummary:"},
	}
	for _, replacement := range replacements {
		line = strings.ReplaceAll(line, replacement.old, replacement.new)
	}

	segments := strings.Split(line, "\n")
	out := make([]string, 0, len(segments))
	for _, segment := range segments {
		for _, clause := range strings.Split(segment, ". ") {
			clause = truncateToolEventText(normalizeToolEventText(clause), 120)
			if clause == "" {
				continue
			}
			out = append(out, clause)
			if len(out) == 3 {
				return out
			}
		}
	}
	return out
}

func selectToolSummaryLines(lines []string) []string {
	if len(lines) == 0 {
		return nil
	}
	if looksLikeDirectorySummary(lines) {
		return buildDirectorySummaryLines(lines)
	}
	if len(lines) <= 3 {
		return lines
	}
	if looksLikeSummaryTrailer(lines[len(lines)-1]) {
		return []string{lines[0], lines[1], lines[len(lines)-1]}
	}
	return lines[:3]
}

func looksLikeDirectorySummary(lines []string) bool {
	return len(lines) >= 2 &&
		strings.HasPrefix(lines[0], "目录:") &&
		strings.HasPrefix(lines[len(lines)-1], "统计:")
}

func buildDirectorySummaryLines(lines []string) []string {
	if len(lines) <= 2 {
		return lines
	}

	middle := lines[1 : len(lines)-1]
	if len(middle) > 3 {
		middle = middle[:3]
	}

	summary := []string{lines[0]}
	if joined := truncateToolEventText(strings.Join(middle, " · "), 120); joined != "" {
		summary = append(summary, joined)
	}
	summary = append(summary, lines[len(lines)-1])
	return summary
}

func looksLikeSummaryTrailer(line string) bool {
	line = strings.TrimSpace(line)
	switch {
	case strings.HasPrefix(line, "统计:"):
		return true
	case strings.HasPrefix(line, "("):
		return true
	case strings.Contains(line, "截断"):
		return true
	case strings.Contains(strings.ToLower(line), "truncated"):
		return true
	default:
		return false
	}
}

func normalizeToolEventText(text string) string {
	return strings.Join(strings.Fields(strings.TrimSpace(text)), " ")
}

func truncateToolEventText(text string, limit int) string {
	text = normalizeToolEventText(text)
	if limit <= 0 {
		return text
	}
	runes := []rune(text)
	if len(runes) <= limit {
		return text
	}
	if limit <= 3 {
		return string(runes[:limit])
	}
	return strings.TrimSpace(string(runes[:limit-3])) + "..."
}
