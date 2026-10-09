package commands

import (
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui"
)

// newCommandsPromptPortSurface 构造 prompt-editor 门面测试用 surface
// （合成几何 + 关闭物理写）。
func newCommandsPromptPortSurface(t *testing.T) *ui.FixedBottomSurface {
	t.Helper()
	surface := ui.NewFixedBottomSurface(ui.NewTerminal())
	surface.EnableForTest(80, 24)
	surface.SetPhysicalWritesEnabled(false)
	return surface
}

// TestChatInteractionCoordinatorPromptPortInjection 固定 L5-2c 的接线：
// SetSurface 注入 prompt-editor 门面、卸载/替换即清空、Shutdown 清空；
// 无 surface 时 currentPromptEditorPort 返回 nil（调用方经 chatSessionPromptPort
// 安全降级）。
func TestChatInteractionCoordinatorPromptPortInjection(t *testing.T) {
	session := &ChatSession{}
	coord := newTestChatInteractionCoordinator(t, session)

	if port := coord.currentPromptEditorPort(); port != nil {
		t.Fatalf("no surface: prompt port must be nil, got %T", port)
	}

	surface := newCommandsPromptPortSurface(t)
	coord.SetSurface(surface)
	port := coord.currentPromptEditorPort()
	if port == nil {
		t.Fatal("SetSurface must inject the prompt-editor port")
	}

	// surface 卸载（替换为 nil）即清空门面。
	coord.SetSurface(nil)
	if port := coord.currentPromptEditorPort(); port != nil {
		t.Fatalf("surface unload must clear the prompt-editor port: %T", port)
	}

	// Shutdown 清空门面（幂等收尾路径）。
	coord.SetSurface(surface)
	if coord.currentPromptEditorPort() == nil {
		t.Fatal("re-mounted surface must re-inject the prompt-editor port")
	}
	coord.Shutdown()
	if port := coord.currentPromptEditorPort(); port != nil {
		t.Fatalf("Shutdown must clear the prompt-editor port: %T", port)
	}
}

// TestChatSessionPromptPortFallbackWithoutCoordinator 固定无 coordinator 的
// legacy 回落：session.Surface 直设（测试/headless）时 chatSessionPromptPort
// 构造 surface 本地门面，状态行与预算行为与迁移前一致。
func TestChatSessionPromptPortFallbackWithoutCoordinator(t *testing.T) {
	if port := chatSessionPromptPort(nil); port == nil {
		t.Fatal("chatSessionPromptPort must never return nil")
	}

	surface := newCommandsPromptPortSurface(t)
	session := &ChatSession{Surface: surface}
	port := chatSessionPromptPort(session)
	if port == nil {
		t.Fatal("chatSessionPromptPort must never return nil")
	}

	if !port.SetStatusLine("fallback status") {
		t.Fatal("legacy fallback SetStatusLine must report applied")
	}
	if got, want := port.MaxVisibleRows(), surface.PromptInputMaxVisibleRows(); got != want {
		t.Fatalf("legacy fallback rows = %d, want surface impl %d", got, want)
	}
}
