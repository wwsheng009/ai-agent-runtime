package commands

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/fatih/color"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui"
	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
	runtimetypes "github.com/wwsheng009/ai-agent-runtime/internal/types"
)

func TestHistoryLoadAndResumeCommandsReplayTheSameTranscript(t *testing.T) {
	oldNoColor := color.NoColor
	color.NoColor = true
	defer func() {
		color.NoColor = oldNoColor
	}()
	ui.SetTheme(ui.ThemeAuto)

	history := historyCommandTranscriptFixture()
	storage := runtimechat.NewInMemoryStorage()
	manager := runtimechat.NewSessionManager(storage, nil)
	t.Cleanup(manager.Stop)

	persisted := runtimechat.NewSession("tester")
	persisted.ID = "unified-transcript-session"
	persisted.Metadata.Title = "Unified transcript"
	persisted.ReplaceHistory(history)
	if err := storage.Save(context.Background(), persisted); err != nil {
		t.Fatalf("save transcript fixture: %v", err)
	}

	historySession := newHistoryCommandTestSession(manager)
	if err := replaceRuntimeMessages(historySession, history); err != nil {
		t.Fatalf("prepare /history messages: %v", err)
	}

	outputs := map[string]string{
		"/history": renderHistoryCommandTranscript(t, historySession, "/history"),
		"/load":    renderHistoryCommandTranscript(t, newHistoryCommandTestSession(manager), "/load "+persisted.ID),
		"/resume":  renderHistoryCommandTranscript(t, newHistoryCommandTestSession(manager), "/resume "+persisted.ID),
	}

	for command, output := range outputs {
		for _, expected := range []string{
			"查看 docs",
			"先确认目录内容。",
			"• Running Get-ChildItem docs",
			"• Completed Get-ChildItem docs",
			"README.md",
			"• Failed exit 1",
			"stderr details",
			"[critic] 需要补充证据。",
		} {
			if !strings.Contains(output, expected) {
				t.Fatalf("expected %s transcript to contain %q, got:\n%s", command, expected, output)
			}
		}
	}

	want := normalizeHistoryCommandTranscriptHeader(outputs["/history"])
	for _, command := range []string{"/load", "/resume"} {
		if got := normalizeHistoryCommandTranscriptHeader(outputs[command]); got != want {
			t.Fatalf("expected %s to replay the same transcript as /history\n/history:\n%s\n%s:\n%s", command, outputs["/history"], command, outputs[command])
		}
	}
}

func historyCommandTranscriptFixture() []runtimetypes.Message {
	assistant := runtimetypes.Message{
		Role:    "assistant",
		Content: "我先检查目录。",
		ToolCalls: []runtimetypes.ToolCall{
			{ID: "call-ok", Name: "shell_command", Args: map[string]interface{}{"command": "Get-ChildItem docs"}},
			{ID: "call-fail", Name: "shell_command", Args: map[string]interface{}{"command": "exit 1"}},
		},
		Metadata: runtimetypes.NewMetadata(),
	}
	runtimetypes.SetReasoningBlock(assistant.Metadata, &runtimetypes.ReasoningBlock{
		Summary:    "先确认目录内容。",
		Visibility: runtimetypes.ReasoningVisibilitySummary,
	})
	succeeded := runtimetypes.NewToolMessage("call-ok", "README.md")
	failed := runtimetypes.NewToolMessage("call-fail", "Tool execution failed: exit status 1\nstderr details")
	failed.Metadata["tool_error"] = "exit status 1"

	return []runtimetypes.Message{
		*runtimetypes.NewUserMessage("查看 docs"),
		assistant,
		*succeeded,
		*failed,
		{Role: "critic", Content: "需要补充证据。", Metadata: runtimetypes.NewMetadata()},
	}
}

func newHistoryCommandTestSession(manager *runtimechat.SessionManager) *ChatSession {
	session := &ChatSession{
		SessionManager: manager,
		SessionUserID:  "tester",
	}
	session.Interaction = newChatInteractionCoordinator(session)
	return session
}

func renderHistoryCommandTranscript(t *testing.T, session *ChatSession, command string) string {
	t.Helper()

	var output bytes.Buffer
	session.Interaction.SetWriter(&output)
	captureStdout(t, func() {
		if quit := dispatchChatCommand(session, command, false); quit {
			t.Fatalf("expected %s not to exit chat", command)
		}
	})
	if strings.TrimSpace(output.String()) == "" {
		t.Fatalf("expected %s to render transcript output", command)
	}
	return output.String()
}

func normalizeHistoryCommandTranscriptHeader(output string) string {
	output = strings.Replace(output, "对话历史", "<history-header>", 1)
	return strings.Replace(output, "已加载历史会话", "<history-header>", 1)
}
