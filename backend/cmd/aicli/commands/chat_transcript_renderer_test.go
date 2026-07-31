package commands

import (
	"bytes"
	"context"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui"
	runtimechatcore "github.com/wwsheng009/ai-agent-runtime/internal/chatcore"
	runtimetypes "github.com/wwsheng009/ai-agent-runtime/internal/types"
	"strings"
	"testing"
)

func TestAICLITranscriptRenderer_RendersCompleteBlocksWithInteraction(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	ui.SetTheme(ui.ThemeAuto)

	session := &ChatSession{}
	session.Interaction = newChatInteractionCoordinator(session)
	var output bytes.Buffer
	session.Interaction.SetWriter(&output)
	renderer := newAICLITranscriptRenderer(session)

	if !renderer.RenderUser("检查目录") {
		t.Fatal("expected user block to render")
	}
	if !renderer.RenderReasoning(&runtimetypes.ReasoningBlock{
		Summary:    "先确认目录。",
		Visibility: runtimetypes.ReasoningVisibilitySummary,
	}) {
		t.Fatal("expected reasoning block to render")
	}
	if !renderer.RenderAssistant("目录如下。") {
		t.Fatal("expected assistant block to render")
	}
	if !renderer.RenderToolEvent(runtimechatcore.ChatEvent{
		Type:       runtimechatcore.EventTool,
		Stage:      "tool_requested",
		ToolName:   "ls",
		ToolCallID: "call-1",
		Arguments:  map[string]interface{}{"path": "docs"},
	}) {
		t.Fatal("expected tool request to render")
	}
	if !renderer.RenderToolEvent(runtimechatcore.ChatEvent{
		Type:       runtimechatcore.EventTool,
		Stage:      "tool_result",
		ToolName:   "ls",
		ToolCallID: "call-1",
		Arguments:  map[string]interface{}{"path": "docs"},
		Output:     "README.md",
		Success:    true,
	}) {
		t.Fatal("expected tool result to render")
	}
	if !renderer.RenderSystem("可见系统消息") {
		t.Fatal("expected system block to render")
	}

	rendered := output.String()
	expectedInOrder := []string{
		"> 检查目录",
		"先确认目录。",
		"目录如下。",
		"• Completed ls path=docs",
		"README.md",
		"可见系统消息",
	}
	lastIndex := -1
	for _, expected := range expectedInOrder {
		index := strings.Index(rendered, expected)
		if index < 0 {
			t.Fatalf("expected transcript output to contain %q, got:\n%s", expected, rendered)
		}
		if index <= lastIndex {
			t.Fatalf("expected %q after the previous block, got:\n%s", expected, rendered)
		}
		lastIndex = index
	}
	if strings.Contains(rendered, "• Running ls path=docs") {
		t.Fatalf("Running must remain viewport-only, got retained transcript:\n%s", rendered)
	}
}

func TestAICLITranscriptRenderer_RendersWithoutInteraction(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	ui.SetTheme(ui.ThemeAuto)

	renderer := newAICLITranscriptRenderer(&ChatSession{})
	output := captureStdout(t, func() {
		renderer.RenderUser("检查目录")
		renderer.RenderAssistant("目录如下。")
		renderer.RenderSystem("可见系统消息")
		renderer.RenderSupplement("补充信息")
		renderer.RenderToolEvent(runtimechatcore.ChatEvent{
			Type:      runtimechatcore.EventTool,
			Stage:     "tool_result",
			ToolName:  "ls",
			Arguments: map[string]interface{}{"path": "docs"},
			Output:    "README.md",
			Success:   true,
		})
	})

	for _, expected := range []string{
		"> 检查目录",
		"目录如下。",
		"可见系统消息",
		"补充信息",
		"• Completed ls path=docs",
		"README.md",
	} {
		if !strings.Contains(output, expected) {
			t.Fatalf("expected non-coordinated transcript output to contain %q, got:\n%s", expected, output)
		}
	}
}

func TestChatRuntimeEventBridge_CompleteResponseMatchesTranscriptRenderer(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	ui.SetTheme(ui.ThemeAuto)

	content := "异步团队已经完成。"
	bridgeOutput := captureStdout(t, func() {
		newChatRuntimeEventBridge(&ChatSession{}).renderResponse(content)
	})
	rendererOutput := captureStdout(t, func() {
		newAICLITranscriptRenderer(&ChatSession{}).RenderAssistant(content)
	})

	if bridgeOutput != rendererOutput {
		t.Fatalf("expected runtime event complete response to use transcript rendering\nbridge:\n%q\nrenderer:\n%q", bridgeOutput, rendererOutput)
	}
}

func TestShellSlashCommand_RendersCompleteResponseThroughTranscriptRenderer(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	ui.SetTheme(ui.ThemeAuto)

	const response = "slash command assistant response"
	session := &ChatSession{
		ChatExecutor: &fakeChatExecutor{output: response},
		cancelCtx:    context.Background(),
	}
	session.Interaction = newChatInteractionCoordinator(session)
	var transcript bytes.Buffer
	session.Interaction.SetWriter(&transcript)

	stdout := captureStdout(t, func() {
		if quit := handleCommand(session, "/shell echo transcript-shell-input", false); quit {
			t.Fatal("expected /shell command not to exit chat")
		}
	})

	if !strings.Contains(transcript.String(), response) {
		t.Fatalf("expected slash command response in transcript output, got %q", transcript.String())
	}
	if strings.Contains(stdout, response) {
		t.Fatalf("did not expect slash command response to bypass transcript rendering, got stdout %q", stdout)
	}
}

func TestAICLITranscriptRenderer_SuppressesNonTranscriptModes(t *testing.T) {
	tests := []struct {
		name    string
		session *ChatSession
	}{
		{name: "no interactive", session: &ChatSession{NoInteractive: true}},
		{name: "json output", session: &ChatSession{JSONOutput: true}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.session.Interaction = newChatInteractionCoordinator(tt.session)
			var output bytes.Buffer
			tt.session.Interaction.SetWriter(&output)
			renderer := newAICLITranscriptRenderer(tt.session)

			results := []bool{
				renderer.RenderUser("user"),
				renderer.RenderAssistant("assistant"),
				renderer.RenderSystem("system"),
				renderer.RenderSupplement("supplement"),
				renderer.RenderReasoning(&runtimetypes.ReasoningBlock{Summary: "reasoning"}),
				renderer.RenderToolEvent(runtimechatcore.ChatEvent{Type: runtimechatcore.EventTool, Stage: "tool_requested", ToolName: "ls"}),
			}
			for index, rendered := range results {
				if rendered {
					t.Fatalf("expected render operation %d to be suppressed", index)
				}
			}
			if output.Len() != 0 {
				t.Fatalf("expected no transcript output, got %q", output.String())
			}
		})
	}
}

func TestAICLITranscriptRenderer_HandlesNilAndEmptyInput(t *testing.T) {
	var nilRenderer *aicliTranscriptRenderer
	if nilRenderer.RenderAssistant("assistant") {
		t.Fatal("expected nil renderer to be a no-op")
	}

	renderer := newAICLITranscriptRenderer(nil)
	if renderer.RenderUser("user") || renderer.RenderAssistant("assistant") || renderer.RenderSupplement("supplement") {
		t.Fatal("expected nil session to suppress transcript output")
	}

	renderer = newAICLITranscriptRenderer(&ChatSession{})
	if renderer.RenderUser("  ") || renderer.RenderAssistant("\n") || renderer.RenderSystem("") || renderer.RenderSupplement("\t") {
		t.Fatal("expected empty complete blocks to be suppressed")
	}
}
