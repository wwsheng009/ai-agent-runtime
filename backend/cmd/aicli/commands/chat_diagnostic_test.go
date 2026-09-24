package commands

import (
	"bytes"
	"strings"
	"testing"
)

// 诊断出口（mesh warning 等）的语义：交互式会话期间必须走语义补充 cell，
// 由 TerminalSession 成为唯一物理写者；没有交互式会话时才允许调用方回退
// stderr。直接写 stderr 会落在 FixedBottomSurface 的底部保留区上，把状态栏
// 覆盖成半截文本。

func TestNotifyChatDiagnosticWithoutSinkFallsBack(t *testing.T) {
	previous := chatDiagnosticSink.Swap(nil)
	t.Cleanup(func() { chatDiagnosticSink.Store(previous) })

	if NotifyChatDiagnostic("Warning: mesh: peer node-x stream lost") {
		t.Fatal("diagnostic accepted without a registered interactive session")
	}
	if NotifyChatDiagnostic("   ") {
		t.Fatal("blank diagnostic accepted")
	}
}

func TestRegisterChatDiagnosticSinkSkipsNonInteractiveSession(t *testing.T) {
	previous := chatDiagnosticSink.Swap(nil)
	t.Cleanup(func() { chatDiagnosticSink.Store(previous) })

	for name, session := range map[string]*ChatSession{
		"no-session":      nil,
		"non-interactive": {NoInteractive: true},
		"json-output":     {JSONOutput: true},
	} {
		registerChatDiagnosticSink(&chatInteractionCoordinator{session: session})
		if NotifyChatDiagnostic("Warning: mesh: peer node-x stream lost") {
			t.Fatalf("%s session accepted a TUI diagnostic", name)
		}
	}
}

func TestNotifyChatDiagnosticRoutesToInteractiveTranscript(t *testing.T) {
	previous := chatDiagnosticSink.Swap(nil)
	t.Cleanup(func() { chatDiagnosticSink.Store(previous) })

	session := &ChatSession{}
	bridge := newChatRuntimeEventBridge(session)
	session.RuntimeEventBridge = bridge
	coordinator := newTestChatInteractionCoordinator(t, session)
	session.Interaction = coordinator
	var output bytes.Buffer
	coordinator.SetWriter(&output)

	const line = "Warning: mesh: peer node-12504 stream lost: unexpected EOF"
	var accepted bool
	_, stderr := captureStdoutStderr(t, func() {
		accepted = NotifyChatDiagnostic(line)
	})
	if !accepted {
		t.Fatal("interactive session did not accept the diagnostic")
	}
	coordinator.waitUIActorIdle()

	if strings.TrimSpace(stderr) != "" {
		t.Fatalf("diagnostic leaked to stderr: %q", stderr)
	}
	snapshot := bridge.sceneSnapshot()
	if snapshot == nil || len(snapshot.Cells) != 1 {
		t.Fatalf("scene cells = %+v, want one diagnostic supplement", snapshot)
	}
	if cell := snapshot.Cells[0]; cell.Source != line {
		t.Fatalf("scene cell source = %q, want %q", cell.Source, line)
	}
}
