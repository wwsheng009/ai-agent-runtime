package commands

import (
	"bytes"
	"os"
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/style"
)

func TestTryExecuteStructuredChatCommandStatus(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	session := &ChatSession{
		ProviderName: "openai",
		Model:        "gpt-test",
	}

	for _, command := range []string{"/status", "/status ", " /status"} {
		result, handled, err := tryExecuteStructuredChatCommand(session, command)
		if err != nil {
			t.Fatalf("%q returned error: %v", command, err)
		}
		if !handled {
			t.Fatalf("%q was not handled as a structured command", command)
		}
		if result.Action != CommandContinue {
			t.Fatalf("%q action=%v want CommandContinue", command, result.Action)
		}
		if len(result.Blocks) != 1 {
			t.Fatalf("%q blocks=%d want 1", command, len(result.Blocks))
		}
		plain := ui.RenderDocumentPlain(result.Document())
		for _, marker := range []string{
			"╭",
			"╰",
			"Model provider:",
			"Token usage",
		} {
			if !strings.Contains(plain, marker) {
				t.Fatalf("%q document missing %q:\n%s", command, marker, plain)
			}
		}
		if strings.HasPrefix(plain, "\n") || strings.HasSuffix(plain, "\n") {
			t.Fatalf("%q document owns a top-level boundary blank: %q", command, plain)
		}
	}

	// /status rejects arguments; the legacy handler owns the parameter error
	// so the message stays visible in every mode.
	for _, command := range []string{"/status on", "/status=x", "/status debug"} {
		if _, handled, err := tryExecuteStructuredChatCommand(session, command); err != nil || handled {
			t.Fatalf("%q structured match=(%t, %v), want legacy", command, handled, err)
		}
	}

	// A missing session still renders a structured error cell instead of
	// writing raw stdout.
	result, handled, err := tryExecuteStructuredChatCommand(nil, "/status")
	if err != nil {
		t.Fatalf("nil session /status returned error: %v", err)
	}
	if !handled {
		t.Fatal("nil session /status was not handled as a structured command")
	}
	plain := ui.RenderDocumentPlain(result.Document())
	if !strings.Contains(plain, "错误: 当前没有活动会话") {
		t.Fatalf("nil session /status document missing error text:\n%s", plain)
	}
}

func TestDispatchChatCommandStatusDoesNotWriteRawStdout(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	session := &ChatSession{
		ProviderName: "openai",
		Model:        "gpt-test",
	}
	coord := newChatInteractionCoordinator(session)
	t.Cleanup(coord.Shutdown)
	session.Interaction = coord

	var retained bytes.Buffer
	coord.SetWriter(&retained)
	raw := captureStdout(t, func() {
		if dispatchChatCommand(session, "/status", false) {
			t.Fatal("/status unexpectedly requested chat exit")
		}
	})
	if leaked := stripAsyncTerminalNoise(raw); leaked != "" {
		t.Fatalf("structured /status wrote raw stdout:\n%q", leaked)
	}
	if coord.commandCellSequence != 1 {
		t.Fatalf("structured /status committed %d cells, want 1", coord.commandCellSequence)
	}
	output := retained.String()
	if count := strings.Count(output, "Token usage"); count != 1 {
		t.Fatalf("status marker count=%d want 1:\n%s", count, output)
	}
	if !strings.Contains(output, "╰") {
		t.Fatalf("atomic status cell is missing its box tail:\n%s", output)
	}
}

// stripAsyncTerminalNoise removes save/restore cursor repaint frames
// (motion spinner repaints: "\x1b[s ... \x1b[u") that sibling tests may emit
// asynchronously to the process-wide stdout. They are not /status output and
// must not fail the raw-stdout gate; any other byte still fails it.
func stripAsyncTerminalNoise(s string) string {
	var b strings.Builder
	for {
		start := strings.Index(s, "\x1b[s")
		if start < 0 {
			b.WriteString(s)
			break
		}
		b.WriteString(s[:start])
		rest := s[start+2:]
		if end := strings.Index(rest, "\x1b[u"); end >= 0 {
			rest = rest[end+2:]
		}
		s = rest
	}
	return b.String()
}

func TestDispatchChatCommandStatusSurvivesOwnedViewportRepaints(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	ui.SetTheme(ui.ThemeAuto)

	const width, height = 100, 120
	surface := ui.NewFixedBottomSurface(ui.NewTerminal())
	surface.EnableForTest(width, height)
	session := &ChatSession{
		ProviderName: "openai",
		Model:        "gpt-test",
		Surface:      surface,
	}
	coord := newChatInteractionCoordinator(session)
	t.Cleanup(coord.Shutdown)
	session.Interaction = coord
	coord.SetSurface(surface)
	screen := newScreenVT(width, height)
	feed := func(paint func()) {
		t.Helper()
		screen.feed(captureSurfaceStdout(t, func() {
			coord.SetWriter(os.Stdout)
			paint()
		}))
	}

	const marker = "Token usage"
	assertSingle := func(stage string) {
		t.Helper()
		frame := commandResultFrameText(surface)
		if count := strings.Count(frame, marker); count != 1 {
			t.Fatalf("%s composed frame marker count=%d want 1:\n%s", stage, count, frame)
		}
		if rows := screen.RowsContaining(marker); len(rows) != 1 {
			t.Fatalf("%s physical screen marker rows=%v want one:\n%s", stage, rows, screen.dump())
		}
	}

	feed(func() {
		coord.PrintPrompt()
	})
	feed(func() {
		if dispatchChatCommand(session, "/status", false) {
			t.Fatal("/status unexpectedly requested chat exit")
		}
	})
	assertSingle("initial command frame")

	feed(func() {
		surface.SetStatusModels(style.StatusLineModel{State: style.RunReady}, nil)
		surface.ShowPrompt("> ")
	})
	assertSingle("status and prompt repaint")

	feed(func() {
		surface.SetActiveBand([]string{"• Running structured status check", "  retained active row"})
	})
	assertSingle("active band growth")

	feed(func() {
		surface.ClearActiveBand()
	})
	assertSingle("active band shrink")

	surface.EnableForTest(88, height)
	if frame := commandResultFrameText(surface); strings.Count(frame, marker) != 1 {
		t.Fatalf("resize recompose lost or duplicated status command marker:\n%s", frame)
	}
}
