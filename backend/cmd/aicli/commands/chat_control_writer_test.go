package commands

import (
	"bytes"
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui"
)

// 非 unified 会话：控制序列写回 legacy raw writer（字节不变）。
func TestChatControlSequenceWriterFallsBackToRawWriter(t *testing.T) {
	session := &ChatSession{}
	coordinator := newTestChatInteractionCoordinator(t, session)
	t.Cleanup(coordinator.Shutdown)
	session.Interaction = coordinator

	var raw bytes.Buffer
	writer := chatControlSequenceWriter{
		session: session,
		raw:     &raw,
		submit: func(coordinator *chatInteractionCoordinator, sequence string) bool {
			return coordinator.WriteTerminalBell(sequence)
		},
	}
	if _, err := writer.Write([]byte("\a")); err != nil {
		t.Fatalf("write bell: %v", err)
	}
	if raw.String() != "\a" {
		t.Fatalf("raw fallback = %q, want bell", raw.String())
	}
}

// unified 会话：标题序列经 TerminalSession 提交（不再触碰 raw writer）。
func TestChatControlSequenceWriterRoutesThroughUnifiedSession(t *testing.T) {
	session := &ChatSession{}
	coordinator := newTestChatInteractionCoordinator(t, session)
	t.Cleanup(coordinator.Shutdown)
	session.Interaction = coordinator

	surface := ui.NewFixedBottomSurface(ui.NewTerminal())
	surface.EnableForTest(72, 18)
	surface.SetPhysicalWritesEnabled(false)
	coordinator.SetSurface(surface)
	var terminal bytes.Buffer
	if !coordinator.enableUnifiedRendererWithWriter(&terminal) {
		t.Fatal("unified renderer did not attach")
	}
	coordinator.waitUIActorIdle()
	awaitUnifiedPresenterIdle(t, coordinator)
	terminal.Reset()

	var raw bytes.Buffer
	writer := chatControlSequenceWriter{
		session: session,
		raw:     &raw,
		submit: func(coordinator *chatInteractionCoordinator, sequence string) bool {
			return coordinator.WriteTerminalTitle(sequence)
		},
	}
	const title = "\x1b]0;unified-title\x07"
	if _, err := writer.Write([]byte(title)); err != nil {
		t.Fatalf("write title: %v", err)
	}
	if raw.Len() != 0 {
		t.Fatalf("raw fallback must stay empty when the unified session claims, got %q", raw.String())
	}
	if !strings.Contains(terminal.String(), title) {
		t.Fatalf("title bytes did not reach the unified writer: %q", terminal.String())
	}
}
