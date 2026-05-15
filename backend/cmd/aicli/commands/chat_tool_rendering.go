package commands

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"

	runtimechatcore "github.com/wwsheng009/ai-agent-runtime/internal/chatcore"
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
		return appendCompactToolDirectory(renderCompactToolCompletedWithPayload(event.ToolName, payloadStringValue(event.Arguments["command"]), payloadStringValue(payload["command_text"]), payloadStringValue(payload["arg_preview"]), toolSource, chatToolSummaryLines(payload), payload), payload)
	case "batch_end":
		return ""
	default:
		return ""
	}
}

func sharedChatToolPayload(event runtimechatcore.ChatEvent) map[string]interface{} {
	payload := map[string]interface{}{}
	if preview := summarizeSharedChatToolCallArgs(event.Arguments); preview != "" {
		payload["arg_preview"] = preview
	}
	if commandText := summarizeSharedShellToolCommand(event.ToolName, event.Arguments); commandText != "" {
		payload["command_text"] = commandText
	}
	if workdir := strings.TrimSpace(payloadStringValue(event.Arguments["workdir"])); workdir != "" {
		payload["workdir"] = workdir
	} else if cwd := strings.TrimSpace(payloadStringValue(event.Arguments["cwd"])); cwd != "" {
		payload["cwd"] = cwd
	}
	if lines := summarizeSharedChatToolResultLines(event); len(lines) > 0 {
		payload["summary_lines"] = lines
		payload["summary"] = strings.Join(lines, "\n")
	}
	if output := editingSharedToolRenderOutput(event.ToolName, event.Output); output != "" {
		payload["render_output"] = output
		payload["render_output_format"] = "markdown"
		payload["render_output_untruncated"] = true
	}
	if errText := strings.TrimSpace(event.Error); errText != "" {
		payload["error"] = errText
	}
	if source := payloadStringValue(event.Metadata[toolresult.SourceKey]); source != "" {
		payload[toolresult.SourceKey] = source
	}
	for _, key := range []string{"shell_type", "shell_path", "shell_display"} {
		if value := payloadStringValue(event.Metadata[key]); value != "" {
			payload[key] = value
		}
	}
	if event.Stage == "batch_end" {
		payload["awaiting_model"] = true
	}
	return payload
}

func summarizeSharedChatToolCallArgs(args map[string]interface{}) string {
	if len(args) == 0 {
		return ""
	}

	seen := make(map[string]struct{}, len(sharedChatToolPreviewKeys))
	for _, key := range sharedChatToolPreviewKeys {
		seen[key] = struct{}{}
		if preview := formatSharedChatToolArgPreview(key, args[key]); preview != "" {
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
		if preview := formatSharedChatToolArgPreview(key, args[key]); preview != "" {
			return preview
		}
	}
	return ""
}

func formatSharedChatToolArgPreview(key string, value interface{}) string {
	key = strings.TrimSpace(key)
	if key == "" || value == nil {
		return ""
	}
	text := normalizeSharedChatToolText(renderSharedChatToolArgValue(value))
	if text == "" || text == "{}" || text == "[]" {
		return ""
	}
	return truncateChatRuntimeText(fmt.Sprintf("%s=%s", key, text), 72)
}

func renderSharedChatToolArgValue(value interface{}) string {
	switch typed := value.(type) {
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
		raw, err := json.Marshal(typed)
		if err != nil {
			return fmt.Sprintf("%v", value)
		}
		return string(raw)
	default:
		return fmt.Sprintf("%v", value)
	}
}

func summarizeSharedChatToolResultLines(event runtimechatcore.ChatEvent) []string {
	if summary := strings.TrimSpace(truncateOutputPreview(event.Output, 3, 360)); summary != "" {
		lines := make([]string, 0, 3)
		for _, line := range strings.Split(strings.ReplaceAll(summary, "\r\n", "\n"), "\n") {
			normalized := normalizeSharedChatToolText(line)
			if normalized == "" {
				continue
			}
			lines = append(lines, normalized)
			if len(lines) == 3 {
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
	lines := []string{"• Ran " + display}
	if rendered := renderMarkdownToolOutput(payload); rendered != "" {
		return rendered
	}
	outputLines := compactToolOutputLines(summaryLines)
	if len(outputLines) == 0 {
		outputLines = []string{"(no output)"}
	}
	for _, line := range outputLines {
		lines = append(lines, "  "+line)
	}
	return strings.Join(lines, "\n")
}

func renderMarkdownToolOutput(payload map[string]interface{}) string {
	if payload == nil || !payloadBoolValue(payload, "render_output_untruncated") {
		return ""
	}
	if format := strings.TrimSpace(payloadStringValue(payload["render_output_format"])); format != "" && format != "markdown" {
		return ""
	}
	output := strings.TrimRight(strings.ReplaceAll(payloadStringValue(payload["render_output"]), "\r\n", "\n"), "\n")
	if strings.TrimSpace(output) == "" {
		return ""
	}
	if rendered := renderEditedDiffOutput(output); rendered != "" {
		return rendered
	}
	lines := strings.Split(output, "\n")
	for i, line := range lines {
		lines[i] = "  " + line
	}
	return strings.Join(lines, "\n")
}

var unifiedDiffHunkHeaderPattern = regexp.MustCompile(`^@@ -(\d+)(?:,\d+)? \+(\d+)(?:,\d+)? @@`)

func renderEditedDiffOutput(output string) string {
	diff := extractFencedDiff(output)
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
		lines = append(lines, fmt.Sprintf("• Edited %s (+%d -%d)", file.path, file.additions, file.deletions))
		for _, line := range file.lines {
			lines = append(lines, "    "+line)
		}
	}
	return strings.Join(lines, "\n")
}

func extractFencedDiff(output string) string {
	normalized := strings.ReplaceAll(output, "\r\n", "\n")
	start := strings.Index(normalized, "```diff")
	if start < 0 {
		return ""
	}
	rest := normalized[start+len("```diff"):]
	rest = strings.TrimPrefix(rest, "\n")
	end := strings.Index(rest, "```")
	if end >= 0 {
		rest = rest[:end]
	}
	return strings.Trim(rest, "\n")
}

type renderedDiffFile struct {
	path      string
	additions int
	deletions int
	lines     []string
}

func parseUnifiedDiffFiles(diff string) []renderedDiffFile {
	rawLines := strings.Split(strings.ReplaceAll(diff, "\r\n", "\n"), "\n")
	files := make([]renderedDiffFile, 0, 1)
	var current *renderedDiffFile
	oldLine := 0
	newLine := 0
	for _, raw := range rawLines {
		line := strings.TrimRight(raw, "\r")
		switch {
		case strings.HasPrefix(line, "--- "):
			if current != nil && (current.path != "" || len(current.lines) > 0) {
				files = append(files, *current)
			}
			current = &renderedDiffFile{path: normalizeRenderedDiffPath(strings.TrimSpace(strings.TrimPrefix(line, "--- ")))}
			oldLine = 0
			newLine = 0
		case strings.HasPrefix(line, "+++ "):
			if current == nil {
				current = &renderedDiffFile{}
			}
			rawPath := strings.TrimSpace(strings.TrimPrefix(line, "+++ "))
			if !isRenderedDiffDevNull(rawPath) {
				path := normalizeRenderedDiffPath(rawPath)
				if path == "" {
					continue
				}
				current.path = path
			}
		case strings.HasPrefix(line, "@@"):
			if current == nil {
				current = &renderedDiffFile{}
			}
			oldLine, newLine = parseUnifiedDiffHunkStart(line)
		case current != nil && line != "":
			switch line[0] {
			case ' ':
				current.lines = append(current.lines, formatRenderedDiffLine(oldLine, ' ', newLine, strings.TrimPrefix(line, " ")))
				oldLine++
				newLine++
			case '-':
				current.deletions++
				current.lines = append(current.lines, formatRenderedDiffLine(oldLine, '-', 0, strings.TrimPrefix(line, "-")))
				oldLine++
			case '+':
				current.additions++
				current.lines = append(current.lines, formatRenderedDiffLine(0, '+', newLine, strings.TrimPrefix(line, "+")))
				newLine++
			}
		}
	}
	if current != nil && (current.path != "" || len(current.lines) > 0) {
		files = append(files, *current)
	}
	return files
}

func parseUnifiedDiffHunkStart(line string) (int, int) {
	match := unifiedDiffHunkHeaderPattern.FindStringSubmatch(line)
	if len(match) != 3 {
		return 0, 0
	}
	oldStart, _ := strconv.Atoi(match[1])
	newStart, _ := strconv.Atoi(match[2])
	return oldStart, newStart
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
	switch strings.TrimSpace(toolName) {
	case "edit", "apply_patch":
	default:
		return ""
	}
	return strings.TrimSpace(output)
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
