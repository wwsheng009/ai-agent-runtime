package commands

import (
	"strings"
	"testing"

	runtimetypes "github.com/wwsheng009/ai-agent-runtime/internal/types"
)

// TestSessionTranscriptFallbackRendersToolSummary 锁定无 surface/uiActor/Scene
// 时的兜底转录路径：工具消息必须以 compact 投影呈现，而不是模型面向原文。
func TestSessionTranscriptFallbackRendersToolSummary(t *testing.T) {
	toolMessage := realPersistedToolMessage()
	assistant := runtimetypes.NewAssistantMessage("前面的说明")
	assistant.ToolCalls = []runtimetypes.ToolCall{{
		ID:   toolMessage.ToolCallID,
		Name: "shell",
		Args: map[string]interface{}{"command": "echo E2E_SNAPSHOT_OK"},
	}}
	session := &ChatSession{Messages: []runtimetypes.Message{*assistant, toolMessage}}

	msgs := sessionTranscriptFallbackMessages(session)
	if len(msgs) != 2 {
		t.Fatalf("fallback messages=%d want 2: %#v", len(msgs), msgs)
	}
	var toolText string
	for _, msg := range msgs {
		if msg.Role == "tool" {
			toolText = msg.Content
		}
	}
	if toolText == "" {
		t.Fatalf("no tool message in fallback output: %#v", msgs)
	}

	want := chatHistoryToolDisplay(toolMessage, "shell", assistant.ToolCalls[0].Args)
	if toolText != want {
		t.Fatalf("fallback tool cell mismatch\n got: %q\nwant: %q", toolText, want)
	}
	if strings.Contains(toolText, "Full raw output artifact_id") {
		t.Fatalf("fallback leaked raw artifact pointer:\n%q", toolText)
	}
	if !strings.Contains(toolText, "• Completed echo E2E_SNAPSHOT_OK") {
		t.Fatalf("fallback missing compact title:\n%q", toolText)
	}
}

// TestSessionTranscriptFallbackLegacyToolMessageStillSummarized 覆盖无元数据的
// 历史工具消息：仍应得到 compact 摘要而不是原文。
func TestSessionTranscriptFallbackLegacyToolMessageStillSummarized(t *testing.T) {
	legacy := *runtimetypes.NewToolMessage("call-legacy-1", "README contents")
	session := &ChatSession{Messages: []runtimetypes.Message{legacy}}

	msgs := sessionTranscriptFallbackMessages(session)
	if len(msgs) != 1 || msgs[0].Role != "tool" {
		t.Fatalf("fallback messages=%#v", msgs)
	}
	if !strings.Contains(msgs[0].Content, "• Completed") || !strings.Contains(msgs[0].Content, "README contents") {
		t.Fatalf("legacy tool message not summarized: %q", msgs[0].Content)
	}
}
