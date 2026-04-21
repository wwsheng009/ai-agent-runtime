package commands

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	runtimechatcore "github.com/wwsheng009/ai-agent-runtime/internal/chatcore"
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
		line := "[tool] 执行工具调用"
		if count := chatEventInt(event, "call_count"); count > 1 {
			line = fmt.Sprintf("[tool] 执行 %d 个工具调用", count)
		}
		return strings.Join([]string{chatToolDivider("command start"), line}, "\n")
	case "tool_result":
		line := fmt.Sprintf("[tool done] %s", strings.TrimSpace(event.ToolName))
		if argPreview := chatToolArgPreview(payload); argPreview != "" {
			line += " " + argPreview
		}
		rendered := []string{line}
		for _, summaryLine := range chatToolSummaryLines(payload) {
			rendered = append(rendered, "  "+summaryLine)
		}
		return strings.Join(rendered, "\n")
	case "batch_end":
		successCount := chatEventInt(event, "success_count")
		errorCount := chatEventInt(event, "error_count")
		line := fmt.Sprintf("[tool] 完成 %d 个工具调用", successCount)
		if errorCount > 0 {
			line = fmt.Sprintf("[tool] 完成: %d 成功, %d 失败", successCount, errorCount)
		}
		rendered := []string{line, chatToolDivider("command end")}
		if waitingLine := chatToolPostCommandHint(payload); waitingLine != "" {
			rendered = append(rendered, waitingLine)
		}
		return strings.Join(rendered, "\n")
	default:
		return ""
	}
}

func sharedChatToolPayload(event runtimechatcore.ChatEvent) map[string]interface{} {
	payload := map[string]interface{}{}
	if preview := summarizeSharedChatToolCallArgs(event.Arguments); preview != "" {
		payload["arg_preview"] = preview
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

func normalizeSharedChatToolText(text string) string {
	return strings.Join(strings.Fields(strings.TrimSpace(text)), " ")
}
