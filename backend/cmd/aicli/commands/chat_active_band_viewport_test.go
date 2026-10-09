package commands

import (
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui"
)

// TestChatInteractionCoordinatorActiveBandViewportInjection 固定 L5-2b 接线：
// SetSurface 注入视口门面、卸载/替换即清空、Shutdown 清空；无 surface 时
// currentActiveBandViewportPort 返回 nil（调用方经 activeBandViewport 安全降级）。
func TestChatInteractionCoordinatorActiveBandViewportInjection(t *testing.T) {
	session := &ChatSession{}
	coord := newTestChatInteractionCoordinator(t, session)

	if port := coord.currentActiveBandViewportPort(); port != nil {
		t.Fatalf("no surface: viewport port must be nil, got %T", port)
	}

	surface := ui.NewFixedBottomSurface(ui.NewTerminal())
	surface.EnableForTest(80, 24)
	surface.SetPhysicalWritesEnabled(false)
	coord.SetSurface(surface)
	port := coord.currentActiveBandViewportPort()
	if port == nil {
		t.Fatal("SetSurface must inject the ActiveBand viewport port")
	}

	// surface 卸载（替换为 nil）即清空门面。
	coord.SetSurface(nil)
	if port := coord.currentActiveBandViewportPort(); port != nil {
		t.Fatalf("surface unload must clear the viewport port: %T", port)
	}

	// Shutdown 清空门面（幂等收尾路径）。
	coord.SetSurface(surface)
	if coord.currentActiveBandViewportPort() == nil {
		t.Fatal("re-mounted surface must re-inject the viewport port")
	}
	coord.Shutdown()
	if port := coord.currentActiveBandViewportPort(); port != nil {
		t.Fatalf("Shutdown must clear the viewport port: %T", port)
	}
}

// TestChatInteractionCoordinatorActiveBandViewportLegacyFallback 固定无 actor 的
// 回落链：注入端口（无 actor）→ surface 终端缓存；未注入（直设 c.surface）→
// 按 legacy 语义构造；无 surface → no-op 口径 (0, ActiveBandMinRows)。
func TestChatInteractionCoordinatorActiveBandViewportLegacyFallback(t *testing.T) {
	surface := ui.NewFixedBottomSurface(ui.NewTerminal())
	surface.EnableForTest(80, 24)
	surface.SetPhysicalWritesEnabled(false)
	wantWidth, wantRows := 80, ui.ActiveBandRows(24)

	// 路径 1：SetSurface 注入（无 actor）→ 端口存在但 Unified=false，回落 surface。
	session := &ChatSession{}
	coord := newTestChatInteractionCoordinator(t, session)
	coord.SetSurface(surface)
	port := coord.activeBandViewport()
	if port == nil || port.Unified() {
		t.Fatalf("no-actor port: nil=%v unified=%v, want wired-but-legacy", port == nil, port != nil && port.Unified())
	}
	if width, rows := port.ViewportSize(); width != wantWidth || rows != wantRows {
		t.Fatalf("no-actor viewport = (%d, %d), want (%d, %d)", width, rows, wantWidth, wantRows)
	}

	// 路径 2：未注入（直设 c.surface，未走 SetSurface）→ legacy 构造。
	raw := &chatInteractionCoordinator{surface: surface}
	if width, rows := raw.activeBandViewport().ViewportSize(); width != wantWidth || rows != wantRows {
		t.Fatalf("unwired viewport = (%d, %d), want (%d, %d)", width, rows, wantWidth, wantRows)
	}

	// 路径 3：无 surface → no-op。
	if width, rows := (&chatInteractionCoordinator{}).activeBandViewport().ViewportSize(); width != 0 || rows != ui.ActiveBandMinRows {
		t.Fatalf("no-surface viewport = (%d, %d), want (0, %d)", width, rows, ui.ActiveBandMinRows)
	}
}
