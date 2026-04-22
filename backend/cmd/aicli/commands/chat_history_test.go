package commands

import (
	"strings"
	"testing"

	"github.com/fatih/color"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/formatter"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui"
)

func TestPrintVisibleChatHistory_RendersRestoredMessagesWithToolSummary(t *testing.T) {
	oldNoColor := color.NoColor
	color.NoColor = true
	defer func() {
		color.NoColor = oldNoColor
	}()
	ui.SetTheme(ui.ThemeAuto)

	session := &ChatSession{
		Formatter: formatter.NewMarkdownFormatter(true),
		Messages: []map[string]interface{}{
			{
				"role":    "system",
				"content": "You are a helpful assistant.",
			},
			{
				"role":    "user",
				"content": "查看当前目录",
			},
			{
				"role":    "assistant",
				"content": "我来查看当前目录。",
				"tool_calls": []map[string]interface{}{
					{
						"id":   "call-1",
						"type": "function",
						"function": map[string]interface{}{
							"name":      "shell_command",
							"arguments": `{"command":"dir"}`,
						},
					},
				},
			},
			{
				"role":         "tool",
				"tool_call_id": "call-1",
				"content":      "目录: backend",
			},
		},
		SystemPromptText: "You are a helpful assistant.",
	}

	output := captureStdout(t, func() {
		count := printVisibleChatHistory(session, "已加载历史会话")
		if count != 3 {
			t.Fatalf("expected 3 visible history messages, got %d", count)
		}
	})

	for _, expected := range []string{
		"已加载历史会话 (3 条消息):",
		"查看当前目录",
		"我来查看当前目录。",
		"调用工具: shell_command",
		"[call-1] 目录: backend",
	} {
		if !strings.Contains(output, expected) {
			t.Fatalf("expected output to contain %q, got:\n%s", expected, output)
		}
	}
	if strings.Contains(output, "You are a helpful assistant.") {
		t.Fatalf("did not expect hidden system prompt in output, got:\n%s", output)
	}
}

func TestPrintVisibleChatHistory_ReturnsZeroWhenOnlyHiddenSystemPromptExists(t *testing.T) {
	oldNoColor := color.NoColor
	color.NoColor = true
	defer func() {
		color.NoColor = oldNoColor
	}()
	ui.SetTheme(ui.ThemeAuto)

	session := &ChatSession{
		Messages: []map[string]interface{}{
			{
				"role":    "system",
				"content": "Profile system prompt.",
			},
		},
		SystemPromptText: "Profile system prompt.",
	}

	output := captureStdout(t, func() {
		count := printVisibleChatHistory(session, "已加载历史会话")
		if count != 0 {
			t.Fatalf("expected no visible history messages, got %d", count)
		}
	})

	if strings.TrimSpace(output) != "" {
		t.Fatalf("expected no output when no visible history exists, got:\n%s", output)
	}
}
