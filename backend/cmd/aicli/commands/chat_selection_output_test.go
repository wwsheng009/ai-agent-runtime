package commands

import (
	"bytes"
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui"
)

// legacy 选择输出收口：无交互会话时保持原字节（启动期行为不变）；登记中的
// 交互会话存在时投递动态栏，绝不写裸 stdout/stderr（防止未来把 legacy
// reader 误接到会话期，P0 台账 §3 item 5 的兜底防线）。
func TestChatSelectionOutputClaimsToDiagnosticSinkWhenSessionActive(t *testing.T) {
	previous := chatDiagnosticSink.Swap(nil)
	t.Cleanup(func() { chatDiagnosticSink.Store(previous) })

	// 无 sink：选择警告原样写 stderr。
	stdout, stderr := captureStdoutStderr(t, func() {
		printChatSelectionWarning("legacy warning %d", 1)
	})
	if strings.TrimSpace(stdout) != "" {
		t.Fatalf("selection warning wrote stdout without a sink: %q", stdout)
	}
	if !strings.Contains(stderr, "legacy warning 1") {
		t.Fatalf("selection warning missing from stderr without a sink: %q", stderr)
	}

	// 有交互会话：投递动态栏，stdout/stderr 零字节。
	session := &ChatSession{}
	bridge := newChatRuntimeEventBridge(session)
	session.RuntimeEventBridge = bridge
	coordinator := newTestChatInteractionCoordinator(t, session)
	session.Interaction = coordinator
	var history bytes.Buffer
	coordinator.SetWriter(&history)
	surface := ui.NewFixedBottomSurface(ui.NewTerminal())
	surface.EnableForTest(160, 12)
	coordinator.SetSurface(surface)
	session.Surface = surface

	stdout, stderr = captureStdoutStderr(t, func() {
		printChatSelectionWarning("claimed warning %d", 2)
		coordinator.waitUIActorIdle()
	})
	if strings.TrimSpace(stdout) != "" || strings.TrimSpace(stderr) != "" {
		t.Fatalf("selection warning leaked with an active session: stdout=%q stderr=%q", stdout, stderr)
	}
	row := chatDynamicStatusRowText(coordinator, 160)
	if !strings.Contains(row, "claimed warning 2") {
		t.Fatalf("expected the warning on the dynamic status row, got %q", row)
	}
}
