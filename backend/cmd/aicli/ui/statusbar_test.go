package ui

import (
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/render"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/style"
	"io"
	"os"
	"strings"
	"testing"
)

func captureStatusBarStdout(t *testing.T, fn func()) string {
	t.Helper()
	oldStdout := os.Stdout
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	os.Stdout = writer
	restoreTerminalOutput := SetTerminalOutputForTesting(writer)
	restored := false
	restore := func() {
		if restored {
			return
		}
		restoreTerminalOutput()
		os.Stdout = oldStdout
		restored = true
	}
	defer restore()

	fn()
	restore()
	if err := writer.Close(); err != nil {
		t.Fatalf("close writer: %v", err)
	}
	out, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("read stdout: %v", err)
	}
	_ = reader.Close()
	return string(out)
}

func TestStatusBarRenderDoesNotClearBelowStatusArea(t *testing.T) {
	t.Setenv("NO_COLOR", "1")

	bar := NewStatusBar(10)
	bar.SetHeight(1)
	bar.Update("State", "Ready")

	out := captureStatusBarStdout(t, func() {
		bar.Render()
	})
	if strings.Contains(out, "\x1b[0J") || strings.Contains(out, "\x1b[J") {
		t.Fatalf("status bar render must not clear from status row to screen end, got %q", out)
	}
	if !strings.Contains(out, "State: Ready") {
		t.Fatalf("expected rendered status item, got %q", out)
	}
}

func TestStatusBarThinkingDoesNotWriteDirectlyToStderr(t *testing.T) {
	bar := NewStatusBar(1).WithDefaultStatus()
	bar.WithAIThinking(false)
	bar.SetThinking(true)
	if got := bar.items[len(bar.items)-1].Key; got != "Status" {
		t.Fatalf("expected status item to be updated, got %q", got)
	}
}

func TestStatusBarDocumentUsesSemanticRolesAndCellWidth(t *testing.T) {
	bar := NewStatusBar(1)
	bar.UpdateWithWidthRole("模型", "你好", style.RoleCommand, 12)
	bar.UpdateRole("状态", "就绪", style.RoleSuccess)
	doc := bar.Document(20)
	if width := render.Width(doc.PlainText()); width > 20 {
		t.Fatalf("status document width %d exceeds limit: %q", width, doc.PlainText())
	}
	foundLabel, foundCommand := false, false
	for _, span := range doc.Blocks[0].Lines[0].Spans {
		switch span.Style.Role {
		case string(style.RoleMetaLabel):
			foundLabel = true
		case string(style.RoleCommand):
			foundCommand = true
		}
	}
	if !foundLabel || !foundCommand {
		t.Fatalf("missing semantic roles: %#v", doc.Blocks[0].Lines[0].Spans)
	}
}

func TestStatusBarUpdateUsesDefaultSemanticRole(t *testing.T) {
	bar := NewStatusBar(1).Update("State", "Ready")
	doc := bar.Document(80)
	if got := doc.PlainText(); got != "State:Ready" {
		t.Fatalf("unexpected status projection: %q", got)
	}
	spans := doc.Blocks[0].Lines[0].Spans
	if got := spans[len(spans)-1].Style.Role; got != string(style.RoleTextPrimary) {
		t.Fatalf("default value role=%q", got)
	}
}
