package ui

import (
	"testing"
)

// TestActiveBandViewportPortLegacyUsesSurfaceCache 固定 L5-2b 门面的 legacy
// 回落：无渲染链状态源时视口 = surface 终端缓存（宽 + ActiveBandRows(高)），
// 与公开方法一致。
func TestActiveBandViewportPortLegacyUsesSurfaceCache(t *testing.T) {
	surface := newTestFixedBottomSurface() // 合成 80x24
	port := NewActiveBandViewportPort(surface, nil)

	if port.Unified() {
		t.Fatal("legacy port must not report unified geometry")
	}
	width, rows := port.ViewportSize()
	wantWidth, wantRows := surface.ActiveBandViewportSize()
	if width != wantWidth || rows != wantRows {
		t.Fatalf("legacy viewport = (%d, %d), want surface (%d, %d)", width, rows, wantWidth, wantRows)
	}
	if width != 80 || rows != ActiveBandRows(24) {
		t.Fatalf("legacy viewport = (%d, %d), want (80, %d)", width, rows, ActiveBandRows(24))
	}
}

// TestActiveBandViewportPortUnifiedUsesRenderChainGeometry 固定 unified 投影：
// 渲染链几何（Width + ActiveBandRows(Height)），Unified() 报告源可用，且不读取
// surface 终端缓存。
func TestActiveBandViewportPortUnifiedUsesRenderChainGeometry(t *testing.T) {
	surface := newTestFixedBottomSurface() // surface 缓存 80x24，用于区分投影来源
	geometry := GeometryState{Width: 120, Height: 40}
	port := NewActiveBandViewportPort(surface, func() (GeometryState, bool) { return geometry, true })

	if !port.Unified() {
		t.Fatal("wired geometry source must report unified")
	}
	width, rows := port.ViewportSize()
	if width != 120 || rows != ActiveBandRows(40) {
		t.Fatalf("unified viewport = (%d, %d), want (120, %d)", width, rows, ActiveBandRows(40))
	}
	if width == 80 {
		t.Fatal("unified projection must not use the surface cache width")
	}
}

// TestActiveBandViewportPortUnmeasuredGeometryFallsBack 固定几何未测量/未接线
// （0 宽或 0 高、ok=false）时回落 surface 终端缓存。
func TestActiveBandViewportPortUnmeasuredGeometryFallsBack(t *testing.T) {
	surface := newTestFixedBottomSurface()
	state := GeometryState{}
	ok := true
	port := NewActiveBandViewportPort(surface, func() (GeometryState, bool) { return state, ok })

	if port.Unified() {
		t.Fatal("zero-size geometry must not report unified")
	}
	if width, rows := port.ViewportSize(); width != 80 || rows != ActiveBandRows(24) {
		t.Fatalf("unmeasured fallback = (%d, %d), want surface cache (80, %d)", width, rows, ActiveBandRows(24))
	}

	state = GeometryState{Width: 120, Height: 40}
	ok = false
	if port.Unified() {
		t.Fatal("ok=false must not report unified")
	}
	if width, _ := port.ViewportSize(); width != 80 {
		t.Fatalf("ok=false fallback width = %d, want surface 80", width)
	}
}

// TestActiveBandViewportPortNoSourceIsSafe 固定 no-op：无 surface 且无渲染链源
// 时返回 (0, ActiveBandMinRows)（与现 nil surface 口径一致），调用安全。
func TestActiveBandViewportPortNoSourceIsSafe(t *testing.T) {
	port := NewActiveBandViewportPort(nil, nil)
	if port.Unified() {
		t.Fatal("no-source port must not report unified")
	}
	width, rows := port.ViewportSize()
	if width != 0 || rows != ActiveBandMinRows {
		t.Fatalf("no-source viewport = (%d, %d), want (0, %d)", width, rows, ActiveBandMinRows)
	}
}
