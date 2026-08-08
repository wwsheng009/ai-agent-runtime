package commands

import (
	"bytes"
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui"
	runtimetypes "github.com/wwsheng009/ai-agent-runtime/internal/types"
)

// TestPrintVisibleChatHistory_SeparatesFinalToolCells asserts that each final
// tool invocation is an independent retained block. Completed/Failed and their
// own output remain dense inside the cell, while adjacent tool cells get one
// separator. Running is never in scrollback (viewport-only until complete).
func TestPrintVisibleChatHistory_SeparatesFinalToolCells(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	ui.SetTheme(ui.ThemeAuto)

	assistant := runtimetypes.Message{
		Role:    "assistant",
		Content: "我先查看目录。",
		ToolCalls: []runtimetypes.ToolCall{
			{ID: "call-1", Name: "ls", Args: map[string]interface{}{"path": "docs"}},
			{ID: "call-2", Name: "read_file", Args: map[string]interface{}{"path": "docs/README.md"}},
		},
		Metadata: runtimetypes.NewMetadata(),
	}
	tool1 := runtimetypes.NewToolMessage("call-1", "README.md")
	tool2 := runtimetypes.NewToolMessage("call-2", "# Docs")

	session := &ChatSession{}
	session.Interaction = newChatInteractionCoordinator(session)
	var out bytes.Buffer
	session.Interaction.SetWriter(&out)
	replaceRuntimeMessages(session, []runtimetypes.Message{
		*runtimetypes.NewUserMessage("查看 docs"),
		assistant,
		*tool1,
		*tool2,
		*runtimetypes.NewAssistantMessage("目录里有 README。"),
	})
	if count := printVisibleChatHistory(session, "已加载历史会话"); count != 5 {
		t.Fatalf("expected 5 visible history messages, got %d", count)
	}

	plain := stripTerminalDecorations(out.String())
	lines := strings.Split(strings.TrimRight(plain, "\n"), "\n")

	// Build a compact skeleton of content vs blank for assertion.
	skeleton := make([]string, 0, len(lines))
	for _, line := range lines {
		if strings.TrimSpace(line) == "" {
			skeleton = append(skeleton, "BLANK")
			continue
		}
		skeleton = append(skeleton, "CONTENT:"+strings.TrimSpace(line))
	}

	want := []string{
		"CONTENT:已加载历史会话 (5 条消息):",
		"CONTENT:> 查看 docs",
		"BLANK",
		"CONTENT:• 我先查看目录。",
		"BLANK",
		"CONTENT:• Completed ls path=docs",
		"CONTENT:└  README.md",
		"BLANK",
		"CONTENT:• Completed read_file path=docs/README.md",
		"CONTENT:└  # Docs",
		"BLANK",
		"CONTENT:• 目录里有 README。",
	}
	if len(skeleton) != len(want) {
		t.Fatalf("skeleton length mismatch: got %d want %d\ngot:\n%s\nwant:\n%s\nraw:\n%s",
			len(skeleton), len(want), strings.Join(skeleton, "\n"), strings.Join(want, "\n"), plain)
	}
	for i := range want {
		if skeleton[i] != want[i] {
			t.Fatalf("skeleton[%d]=%q want %q\nfull:\n%s\nraw:\n%s",
				i, skeleton[i], want[i], strings.Join(skeleton, "\n"), plain)
		}
	}

	// No consecutive blanks, and max blank run stays 1.
	if maxBlankLineRun(plain) != 1 {
		t.Fatalf("expected maxBlank=1, got %d\n%s", maxBlankLineRun(plain), plain)
	}
	for i := 1; i < len(skeleton); i++ {
		if skeleton[i] == "BLANK" && skeleton[i-1] == "BLANK" {
			t.Fatalf("unexpected consecutive blanks at %d\n%s", i, strings.Join(skeleton, "\n"))
		}
	}
}
