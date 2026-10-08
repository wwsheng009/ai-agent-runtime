package ui

import (
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/style"
	"strings"
	"testing"
)

func TestSanitizeToolOutputStripsClearAndOSC(t *testing.T) {
	in := "hello\x1b[2J\x1b[H\x1b]0;title\x07world\x1b]52;c;YQ==\x07"
	got := SanitizeToolOutput(in)
	if strings.Contains(got, "\x1b") || strings.Contains(got, "\x07") {
		t.Fatalf("control sequences remained: %q", got)
	}
	if !strings.Contains(got, "hello") || !strings.Contains(got, "world") {
		t.Fatalf("lost text: %q", got)
	}
	if strings.Contains(got, "title") || strings.Contains(got, "YQ==") {
		t.Fatalf("OSC payload leaked: %q", got)
	}
}

func TestPreviewToolOutputANSIKeepsTextDropsCursor(t *testing.T) {
	in := "\x1b[31mred\x1b[0m\x1b[10;10Hmoved"
	got := PreviewToolOutputANSI(in)
	if strings.Contains(got, "\x1b") {
		t.Fatalf("ESC remained: %q", got)
	}
	if got != "redmoved" {
		t.Fatalf("got %q", got)
	}
}

func TestGoldenStatusLineWidths(t *testing.T) {
	// Baseline golden widths for 40/80/120 columns (plain projection).
	model := style.StatusLineModel{
		State: style.RunReady,
		Segments: []style.StatusSegment{
			{Kind: style.StatusSegModel, Text: "model mimo", Priority: 0},
			{Kind: style.StatusSegUsage, Text: "Context 14% used", Priority: 1},
			{Kind: style.StatusSegPath, Text: "/very/long/path/to/workspace", Priority: 2},
		},
	}
	for _, width := range []int{40, 80, 120} {
		plain := style.StatusLineDocument(model, width).PlainText()
		if DisplayWidth(plain) > width {
			t.Fatalf("width %d overflow: %q (%d)", width, plain, DisplayWidth(plain))
		}
	}
}
