package ui

import (
	"fmt"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/render"
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

func TestFormatToolCallResultStripsInjectedControls(t *testing.T) {
	out := FormatToolCallResult("shell", map[string]interface{}{"command": "ls"}, true,
		"ok\x1b[2J\x1b]0;pwned\x07done")
	if strings.Contains(out, "\x1b[2J") || strings.Contains(out, "pwned") {
		t.Fatalf("tool result leaked controls: %q", out)
	}
	if !strings.Contains(out, "ok") || !strings.Contains(out, "done") {
		t.Fatalf("lost result text: %q", out)
	}
}

func TestFormatToolCallUsesNegotiatedColorProfile(t *testing.T) {
	t.Run("no-color", func(t *testing.T) {
		t.Setenv("NO_COLOR", "1")
		t.Setenv("FORCE_COLOR", "")
		out := FormatToolCallStart("shell", map[string]interface{}{"command": "go test ./..."})
		if strings.ContainsRune(out, '\x1b') {
			t.Fatalf("NO_COLOR tool call contains ESC: %q", out)
		}
	})

	t.Run("ansi-16", func(t *testing.T) {
		t.Setenv("NO_COLOR", "")
		t.Setenv("FORCE_COLOR", "1")
		t.Setenv("AICLI_COLOR_DEPTH", "ansi16")
		out := FormatToolCallStart("shell", map[string]interface{}{"command": "go test ./..."})
		if !strings.ContainsRune(out, '\x1b') {
			t.Fatalf("forced ANSI-16 tool call has no SGR: %q", out)
		}
		if strings.Contains(out, "\x1b[38;2;") || strings.Contains(out, "\x1b[38;5;") {
			t.Fatalf("ANSI-16 tool call contains higher-depth color: %q", out)
		}
	})
}

func TestShellFeedbackStripsControls(t *testing.T) {
	fb := NewShellFeedback("echo\x1b[2J").
		SetOutput("line\x1b]0;x\x07").
		SetExitCode(0)
	got := fb.Format()
	if strings.Contains(got, "\x1b[2J") || strings.Contains(got, "\x1b]0") {
		t.Fatalf("shell feedback leaked controls: %q", got)
	}
	// Document plain projection must keep command text and status without notice rewrite.
	plain := render.PlainBackend{}.Render(fb.Document())
	if !strings.Contains(plain, "执行:") || !strings.Contains(plain, "echo") {
		t.Fatalf("document missing command header: %q", plain)
	}
	if strings.Contains(plain, "[notice]") {
		t.Fatalf("shell feedback rewritten as notice: %q", plain)
	}
	if strings.Contains(plain, "\x1b") || strings.Contains(plain, "\x07") {
		t.Fatalf("document plain leaked controls: %q", plain)
	}
}

func TestShellFeedbackUsesNegotiatedANSI16Profile(t *testing.T) {
	t.Setenv("NO_COLOR", "")
	t.Setenv("FORCE_COLOR", "1")
	t.Setenv("AICLI_COLOR_DEPTH", "ansi16")

	got := NewShellFeedback("go test ./...").
		SetOutput("ok").
		SetExitCode(0).
		Format()
	if !strings.ContainsRune(got, '\x1b') {
		t.Fatalf("forced ANSI-16 shell feedback has no SGR: %q", got)
	}
	if strings.Contains(got, "\x1b[38;2;") || strings.Contains(got, "\x1b[38;5;") {
		t.Fatalf("ANSI-16 shell feedback contains higher-depth color: %q", got)
	}
	plain := render.ANSIToPlain(got)
	if !strings.Contains(plain, "go test ./...") || !strings.Contains(plain, "成功") {
		t.Fatalf("styled shell feedback changed plain content: %q", plain)
	}
}

func TestFormatShellCommandStripsControls(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	got := FormatShellCommand("ls\x1b[2J\x1b]0;pwn\x07")
	if strings.Contains(got, "\x1b") || strings.Contains(got, "\x07") || strings.Contains(got, "pwn") {
		t.Fatalf("command header leaked controls: %q", got)
	}
	if !strings.Contains(got, "ls") || !strings.Contains(got, "执行:") {
		t.Fatalf("lost command text: %q", got)
	}
}

func TestFormatShellOutputStripsControlsAndTruncates(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	var b strings.Builder
	b.WriteString("safe\x1b[2J\x1b]0;title\x07\n")
	for i := 0; i < 20; i++ {
		b.WriteString(fmt.Sprintf("line-%d\n", i))
	}
	got := FormatShellOutput(b.String(), 4)
	if strings.Contains(got, "\x1b") || strings.Contains(got, "\x07") || strings.Contains(got, "title") {
		t.Fatalf("shell output leaked controls: %q", got)
	}
	if !strings.Contains(got, "│") {
		t.Fatalf("expected preview prefix: %q", got)
	}
	if !strings.Contains(got, "已省略") {
		t.Fatalf("expected truncation footer: %q", got)
	}
}

func TestFormatShellSummaryStripsControls(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	got := FormatShellSummary("echo\x1b[2J hi\x1b]0;x\x07", 1, 42)
	if strings.Contains(got, "\x1b") || strings.Contains(got, "\x07") {
		t.Fatalf("summary leaked controls: %q", got)
	}
	if !strings.Contains(got, "echo") || !strings.Contains(got, "exit=1") {
		t.Fatalf("lost summary fields: %q", got)
	}
	if !strings.Contains(got, "42ms") {
		t.Fatalf("lost duration: %q", got)
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
