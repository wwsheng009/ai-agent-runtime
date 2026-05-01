package ui

import (
	"io"
	"os"
	"strings"
	"testing"

	"github.com/fatih/color"
)

func captureStatusBarStdout(t *testing.T, fn func()) string {
	t.Helper()
	oldStdout := os.Stdout
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	os.Stdout = writer
	defer func() {
		os.Stdout = oldStdout
	}()

	fn()
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
	oldNoColor := color.NoColor
	color.NoColor = true
	defer func() { color.NoColor = oldNoColor }()

	bar := NewStatusBar(10)
	bar.SetHeight(1)
	bar.Update("State", "Ready", nil)

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
