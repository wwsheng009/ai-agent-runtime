package commands

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	runtimechatcore "github.com/wwsheng009/ai-agent-runtime/internal/chatcore"
	runtimepolicy "github.com/wwsheng009/ai-agent-runtime/internal/policy"
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
	switch event.Stage {
	case "batch_start":
		return ""
	case "tool_requested":
		return renderCompactToolRequested(event.ToolName, payloadStringValue(event.Arguments["command"]), payloadStringValue(payload["command_text"]), payloadStringValue(payload["arg_preview"]))
	case "tool_result":
		return renderCompactToolCompleted(event.ToolName, payloadStringValue(event.Arguments["command"]), payloadStringValue(payload["command_text"]), payloadStringValue(payload["arg_preview"]), chatToolSummaryLines(payload))
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
	if lines := summarizeSharedChatToolResultLines(event); len(lines) > 0 {
		payload["summary_lines"] = lines
		payload["summary"] = strings.Join(lines, "\n")
	}
	if errText := strings.TrimSpace(event.Error); errText != "" {
		payload["error"] = errText
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
	display := compactToolDisplayText(toolName, commandArg, commandText, argPreview)
	if display == "" {
		return ""
	}
	return "• Running " + display
}

func renderCompactToolCompleted(toolName, commandArg, commandText, argPreview string, summaryLines []string) string {
	display := compactToolDisplayText(toolName, commandArg, commandText, argPreview)
	if display == "" {
		return ""
	}
	lines := []string{"• Ran " + display}
	outputLines := compactToolOutputLines(summaryLines)
	if len(outputLines) == 0 {
		outputLines = []string{"(no output)"}
	}
	for _, line := range outputLines {
		lines = append(lines, "  "+line)
	}
	return strings.Join(lines, "\n")
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
