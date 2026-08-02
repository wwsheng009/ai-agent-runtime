package commands

import (
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/formatter"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui"
	runtimetypes "github.com/wwsheng009/ai-agent-runtime/internal/types"
)

// TestReplayAfterTruncationClearsRetainedTail guards the "回退后界面残留旧消息"
// fix's second half: before replaying the truncated history, the retained
// visible region (rewriteable soft tail) is cleared, so rows of removed turns
// that still sit inside the visible window do not linger as ghost rows under
// the replay. Rows already handed off into native scrollback stay physically
// present; the archive marker in the header is the only distinction for those.
func TestReplayAfterTruncationClearsRetainedTail(t *testing.T) {
	const width, height = 80, 24
	session := &ChatSession{Formatter: formatter.NewMarkdownFormatter(false), SystemPromptText: "Profile system prompt."}
	coord := newChatInteractionCoordinator(session)
	session.Interaction = coord
	t.Cleanup(coord.Shutdown)
	surface := ui.NewFixedBottomSurface(ui.NewTerminal())
	surface.EnableForTest(width, height)
	coord.SetSurface(surface)
	session.Surface = surface

	seed := []runtimetypes.Message{
		*runtimetypes.NewSystemMessage("Profile system prompt."),
		*runtimetypes.NewUserMessage("第一轮问题"),
		*runtimetypes.NewAssistantMessage("第一轮回答"),
		*runtimetypes.NewUserMessage("将被回退的问题"),
		*runtimetypes.NewAssistantMessage("将被回退的回答"),
	}
	if err := replaceRuntimeMessages(session, seed); err != nil {
		t.Fatalf("seed messages: %v", err)
	}

	screen := newScreenVT(width, height)
	stream := captureSurfaceStdout(t, func() {
		// Paint the full history onto the transcript, simulating the pre-backtrack
		// screen where all four messages (including the soon-to-be-removed turn)
		// are visible.
		beginDirectInteractiveOutput(session)
		if got := printVisibleChatHistory(session, "回退前完整历史"); got != 4 {
			t.Fatalf("expected 4 seeded visible messages, got %d", got)
		}
		// Simulate backtrack: truncate the canonical history back to turn 1.
		truncated := []runtimetypes.Message{
			*runtimetypes.NewSystemMessage("Profile system prompt."),
			*runtimetypes.NewUserMessage("第一轮问题"),
			*runtimetypes.NewAssistantMessage("第一轮回答"),
		}
		if err := replaceRuntimeMessages(session, truncated); err != nil {
			t.Fatalf("truncate messages: %v", err)
		}
		// The post-backtrack replay: must clear the retained tail first, then
		// replay only the surviving history behind the archive marker.
		if got := replayVisibleChatHistoryAfterTruncation(session, "已回退到 user turn 1"); got != 2 {
			t.Fatalf("expected 2 replayed messages, got %d", got)
		}
	})
	screen.feed(stream)
	dump := screen.dump()

	if !strings.Contains(dump, "第一轮回答") {
		t.Fatalf("expected surviving history visible on screen, dump:\n%s", dump)
	}
	if strings.Contains(dump, "将被回退") {
		t.Fatalf("removed turn still visible on screen after replay; dump:\n%s", dump)
	}
	if !strings.Contains(stream, "上方旧消息已失效") {
		t.Fatalf("expected archive marker in replay output, got:\n%s", stream)
	}
	for _, line := range surface.SoftOutputTailLines() {
		if strings.Contains(line, "将被回退") {
			t.Fatalf("removed turn leaked into rewriteable soft tail: %q", line)
		}
	}
}

// TestReplayAfterTruncationWithoutSurfaceIsUnchanged verifies the clearing step
// is a strict no-op for sessions without an enabled surface (the common
// non-interactive / JSON path), so plain stdout replay behavior is unchanged.
func TestReplayAfterTruncationWithoutSurfaceIsUnchanged(t *testing.T) {
	session := &ChatSession{SystemPromptText: "Profile system prompt."}
	if err := replaceRuntimeMessages(session, []runtimetypes.Message{
		*runtimetypes.NewSystemMessage("Profile system prompt."),
		*runtimetypes.NewUserMessage("锚点问题"),
		*runtimetypes.NewAssistantMessage("锚点回答"),
	}); err != nil {
		t.Fatalf("seed messages: %v", err)
	}
	output := captureStdout(t, func() {
		if count := replayVisibleChatHistoryAfterTruncation(session, "已回退到 user turn 1"); count != 2 {
			t.Fatalf("expected 2 replayed visible messages, got %d", count)
		}
	})
	for _, expected := range []string{"已回退到 user turn 1", "锚点问题", "锚点回答", "上方旧消息已失效"} {
		if !strings.Contains(output, expected) {
			t.Fatalf("expected replay output to contain %q, got:\n%s", expected, output)
		}
	}
}
