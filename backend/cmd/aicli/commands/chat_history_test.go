package commands

import (
	"strings"
	"testing"

	"github.com/fatih/color"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/formatter"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui"
	runtimetypes "github.com/wwsheng009/ai-agent-runtime/internal/types"
)

func TestPrintVisibleChatHistory_RendersRestoredMessagesWithToolSummary(t *testing.T) {
	oldNoColor := color.NoColor
	color.NoColor = true
	defer func() {
		color.NoColor = oldNoColor
	}()
	ui.SetTheme(ui.ThemeAuto)

	session := &ChatSession{
		Formatter:        formatter.NewMarkdownFormatter(true),
		SystemPromptText: "You are a helpful assistant.",
	}
	replaceRuntimeMessages(session, []runtimetypes.Message{
		*runtimetypes.NewSystemMessage("You are a helpful assistant."),
		*runtimetypes.NewUserMessage("查看当前目录"),
		{
			Role:    "assistant",
			Content: "我来查看当前目录。",
			ToolCalls: []runtimetypes.ToolCall{
				{ID: "call-1", Name: "shell_command", Args: map[string]interface{}{"command": "dir"}},
			},
			Metadata: runtimetypes.NewMetadata(),
		},
		*runtimetypes.NewToolMessage("call-1", "目录: backend"),
	})

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
		SystemPromptText: "Profile system prompt.",
	}
	replaceRuntimeMessages(session, []runtimetypes.Message{
		*runtimetypes.NewSystemMessage("Profile system prompt."),
	})

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

func TestPrintVisibleChatHistory_TruncatesToolOutputForCLI(t *testing.T) {
	oldNoColor := color.NoColor
	color.NoColor = true
	defer func() {
		color.NoColor = oldNoColor
	}()
	ui.SetTheme(ui.ThemeAuto)

	longOutput := strings.Join([]string{
		"line 1",
		"line 2",
		"line 3",
		"line 4",
		"line 5",
		"line 6",
		"line 7",
	}, "\n")

	session := &ChatSession{}
	replaceRuntimeMessages(session, []runtimetypes.Message{
		*runtimetypes.NewToolMessage("call-1", longOutput),
	})

	output := captureStdout(t, func() {
		count := printVisibleChatHistory(session, "已加载历史会话")
		if count != 1 {
			t.Fatalf("expected 1 visible history message, got %d", count)
		}
	})

	if !strings.Contains(output, "[call-1] line 1") {
		t.Fatalf("expected truncated tool output to keep leading content, got:\n%s", output)
	}
	if !strings.Contains(output, "已省略剩余 1 行") {
		t.Fatalf("expected truncated tool output marker, got:\n%s", output)
	}
	if strings.Contains(output, "line 7") {
		t.Fatalf("did not expect full tool output in CLI history, got:\n%s", output)
	}
}

func TestAICLIMessageHelpers_AppendReplaceAndTruncate(t *testing.T) {
	session := &ChatSession{}

	appendRuntimeMessage(session, *runtimetypes.NewUserMessage("one"))
	if len(session.Messages) != 1 {
		t.Fatalf("expected 1 appended message, got %d", len(session.Messages))
	}

	original := []runtimetypes.Message{
		*runtimetypes.NewUserMessage("two"),
		*runtimetypes.NewAssistantMessage("three"),
	}
	replaceRuntimeMessages(session, original)
	if len(session.Messages) != 2 {
		t.Fatalf("expected 2 replaced messages, got %d", len(session.Messages))
	}
	original[0] = *runtimetypes.NewUserMessage("mutated")
	if got := session.Messages[0].Content; got != "two" {
		t.Fatalf("expected replacement to copy slice contents, got %#v", got)
	}

	truncateAICLIMessages(session, 1)
	if len(session.Messages) != 1 {
		t.Fatalf("expected 1 truncated message, got %d", len(session.Messages))
	}
	if got := session.Messages[0].Content; got != "two" {
		t.Fatalf("unexpected truncated message content: %#v", got)
	}

	truncateAICLIMessages(session, 0)
	if len(session.Messages) != 0 {
		t.Fatalf("expected messages to clear when truncating to zero, got %d", len(session.Messages))
	}
}

func TestAICLIMessageHelpers_MaintainRuntimeMirror(t *testing.T) {
	session := &ChatSession{}

	replaceRuntimeMessages(session, []runtimetypes.Message{
		*runtimetypes.NewUserMessage("hello"),
		{
			Role:    "assistant",
			Content: "done",
			ToolCalls: []runtimetypes.ToolCall{
				{ID: "call-1", Name: "echo", Args: map[string]interface{}{"text": "ok"}},
			},
			Metadata: runtimetypes.NewMetadata(),
		},
	})
	if len(session.Messages) != 2 {
		t.Fatalf("expected history to be populated, got %d", len(session.Messages))
	}
	if session.Messages[1].Role != "assistant" || len(session.Messages[1].ToolCalls) != 1 {
		t.Fatalf("unexpected history content: %#v", session.Messages[1])
	}

	truncateAICLIMessages(session, 1)
	if len(session.Messages) != 1 {
		t.Fatalf("expected history to truncate with messages, got %d", len(session.Messages))
	}

	appendRuntimeMessage(session, *session.Messages[0].Clone())
	if len(session.Messages) != 2 {
		t.Fatalf("expected appendRuntimeMessage to update history, got len=%d", len(session.Messages))
	}
}
