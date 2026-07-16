package commands

import (
	"bytes"
	"strings"
	"testing"

	"github.com/fatih/color"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/formatter"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui"
	runtimechatcore "github.com/wwsheng009/ai-agent-runtime/internal/chatcore"
	runtimetypes "github.com/wwsheng009/ai-agent-runtime/internal/types"
)

func TestPrintVisibleChatHistory_RendersRestoredMessagesWithUnifiedToolRenderer(t *testing.T) {
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
		"• Running dir",
		"• Completed dir",
		"目录: backend",
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

func TestPrintVisibleChatHistory_UsesUnifiedToolPreviewLimits(t *testing.T) {
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

	if !strings.Contains(output, "• Completed call-1") || !strings.Contains(output, "line 1") {
		t.Fatalf("expected unified tool output to keep its title and leading content, got:\n%s", output)
	}
	if !strings.Contains(output, "line 3") {
		t.Fatalf("expected unified three-line tool preview, got:\n%s", output)
	}
	if strings.Contains(output, "line 4") || strings.Contains(output, "line 7") {
		t.Fatalf("did not expect replay to bypass live tool preview limits, got:\n%s", output)
	}
}

func TestPrintVisibleChatHistory_MatchesLiveCompleteBlockRendering(t *testing.T) {
	oldNoColor := color.NoColor
	color.NoColor = true
	defer func() {
		color.NoColor = oldNoColor
	}()
	ui.SetTheme(ui.ThemeAuto)

	assistant := runtimetypes.Message{
		Role:    "assistant",
		Content: "我先查看目录。",
		ToolCalls: []runtimetypes.ToolCall{
			{ID: "call-1", Name: "ls", Args: map[string]interface{}{"path": "docs"}},
		},
		Metadata: runtimetypes.NewMetadata(),
	}
	runtimetypes.SetReasoningBlock(assistant.Metadata, &runtimetypes.ReasoningBlock{
		Summary:    "先确认目录内容。",
		Visibility: runtimetypes.ReasoningVisibilitySummary,
	})
	tool := runtimetypes.NewToolMessage("call-1", "目录: docs\nREADME.md")
	tool.Metadata["tool_source"] = "meta"
	tool.Metadata["workdir"] = `E:\repo`
	tool.Metadata["duration_ms"] = 1250

	historySession := &ChatSession{}
	historySession.Interaction = newChatInteractionCoordinator(historySession)
	var historyOutput bytes.Buffer
	historySession.Interaction.SetWriter(&historyOutput)
	replaceRuntimeMessages(historySession, []runtimetypes.Message{
		*runtimetypes.NewUserMessage("查看 docs"),
		assistant,
		*tool,
	})
	if count := printVisibleChatHistory(historySession, ""); count != 3 {
		t.Fatalf("expected 3 replayed messages, got %d", count)
	}

	liveSession := &ChatSession{}
	liveSession.Interaction = newChatInteractionCoordinator(liveSession)
	var liveOutput bytes.Buffer
	liveSession.Interaction.SetWriter(&liveOutput)
	liveRenderer := newAICLITranscriptRenderer(liveSession)
	liveRenderer.RenderUser("查看 docs")
	liveRenderer.RenderReasoning(runtimetypes.GetReasoningBlock(assistant.Metadata))
	liveRenderer.RenderAssistant("我先查看目录。")
	liveRenderer.RenderToolEvent(runtimechatcore.ChatEvent{
		Type:       runtimechatcore.EventTool,
		Stage:      "tool_requested",
		ToolName:   "ls",
		ToolCallID: "call-1",
		Arguments:  map[string]interface{}{"path": "docs"},
		Metadata: map[string]interface{}{
			"tool_source": "meta",
			"workdir":     `E:\repo`,
			"duration_ms": 1250,
		},
	})
	liveRenderer.RenderToolEvent(runtimechatcore.ChatEvent{
		Type:       runtimechatcore.EventTool,
		Stage:      "tool_result",
		ToolName:   "ls",
		ToolCallID: "call-1",
		Arguments:  map[string]interface{}{"path": "docs"},
		Output:     "目录: docs\nREADME.md",
		Success:    true,
		Metadata: map[string]interface{}{
			"tool_source": "meta",
			"workdir":     `E:\repo`,
			"duration_ms": 1250,
		},
	})

	if historyOutput.String() != liveOutput.String() {
		t.Fatalf("expected history replay and live complete blocks to share rendering\nhistory:\n%s\nlive:\n%s", historyOutput.String(), liveOutput.String())
	}
}

func TestPrintVisibleChatHistory_PreservesCompleteMessageContent(t *testing.T) {
	oldNoColor := color.NoColor
	color.NoColor = true
	defer func() {
		color.NoColor = oldNoColor
	}()
	ui.SetTheme(ui.ThemeAuto)

	content := "回答：\n\n    保留缩进的代码\n"
	historySession := &ChatSession{}
	historySession.Interaction = newChatInteractionCoordinator(historySession)
	var historyOutput bytes.Buffer
	historySession.Interaction.SetWriter(&historyOutput)
	replaceRuntimeMessages(historySession, []runtimetypes.Message{
		*runtimetypes.NewAssistantMessage(content),
	})
	if count := printVisibleChatHistory(historySession, ""); count != 1 {
		t.Fatalf("expected one replayed assistant message, got %d", count)
	}

	liveSession := &ChatSession{}
	liveSession.Interaction = newChatInteractionCoordinator(liveSession)
	var liveOutput bytes.Buffer
	liveSession.Interaction.SetWriter(&liveOutput)
	newAICLITranscriptRenderer(liveSession).RenderAssistant(content)

	if historyOutput.String() != liveOutput.String() {
		t.Fatalf("expected replay to preserve complete message whitespace\nhistory:\n%q\nlive:\n%q", historyOutput.String(), liveOutput.String())
	}
}

func TestPrintVisibleChatHistory_HandlesNestedMetadataAndUnknownRoles(t *testing.T) {
	oldNoColor := color.NoColor
	color.NoColor = true
	defer func() {
		color.NoColor = oldNoColor
	}()
	ui.SetTheme(ui.ThemeAuto)

	tool := runtimetypes.NewToolMessage("orphan-call", "result 1")
	tool.Metadata["tool_metadata"] = runtimetypes.Metadata{
		"tool_name":   "remote_search",
		"tool_source": "mcp",
		"workdir":     `E:\repo`,
		"duration_ms": 1250,
	}
	session := &ChatSession{}
	session.Interaction = newChatInteractionCoordinator(session)
	var output bytes.Buffer
	session.Interaction.SetWriter(&output)
	replaceRuntimeMessages(session, []runtimetypes.Message{
		*tool,
		{Role: "critic", Content: "需要补充证据。"},
	})

	if count := printVisibleChatHistory(session, ""); count != 2 {
		t.Fatalf("expected orphan tool result and unknown role to remain visible, got %d", count)
	}
	rendered := output.String()
	for _, expected := range []string{
		"• Completed [mcp] remote_search",
		"in 1.25s",
		`workdir: E:\repo`,
		"result 1",
		"[critic] 需要补充证据。",
	} {
		if !strings.Contains(rendered, expected) {
			t.Fatalf("expected replay output to contain %q, got:\n%s", expected, rendered)
		}
	}
}

func TestPrintVisibleChatHistory_RendersReasoningAndToolFailures(t *testing.T) {
	oldNoColor := color.NoColor
	color.NoColor = true
	defer func() {
		color.NoColor = oldNoColor
	}()
	ui.SetTheme(ui.ThemeAuto)

	assistant := runtimetypes.Message{
		Role:      "assistant",
		ToolCalls: []runtimetypes.ToolCall{{ID: "call-err", Name: "execute_shell_command", Args: map[string]interface{}{"command": "exit 1"}}},
		Metadata:  runtimetypes.NewMetadata(),
	}
	runtimetypes.SetReasoningBlock(assistant.Metadata, &runtimetypes.ReasoningBlock{
		Summary:    "需要验证失败原因。",
		Visibility: runtimetypes.ReasoningVisibilitySummary,
	})
	tool := runtimetypes.NewToolMessage("call-err", "Tool execution failed: exit status 1\nstderr details")
	tool.Metadata["tool_metadata"] = runtimetypes.Metadata{
		"tool_error": "exit status 1",
	}

	session := &ChatSession{}
	session.Interaction = newChatInteractionCoordinator(session)
	var output bytes.Buffer
	session.Interaction.SetWriter(&output)
	replaceRuntimeMessages(session, []runtimetypes.Message{assistant, *tool})

	if count := printVisibleChatHistory(session, ""); count != 2 {
		t.Fatalf("expected reasoning/tool messages to remain visible, got %d", count)
	}
	rendered := output.String()
	for _, expected := range []string{
		chatToolDivider("reasoning"),
		"需要验证失败原因。",
		"• Running exit 1",
		"• Failed exit 1",
		"stderr details",
	} {
		if !strings.Contains(rendered, expected) {
			t.Fatalf("expected replay output to contain %q, got:\n%s", expected, rendered)
		}
	}
	if strings.Count(rendered, "Tool execution failed:") != 0 {
		t.Fatalf("expected the model-history error prefix to be adapted rather than duplicated, got:\n%s", rendered)
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
