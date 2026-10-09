package ui

import (
	"testing"
)

// newPromptEditorPortTestSurface 构造门面测试用 surface：合成几何 + 关闭物理写
// （legacy 本地实现只推进状态，不向进程 stdout 发射字节）。
func newPromptEditorPortTestSurface(t *testing.T) *FixedBottomSurface {
	t.Helper()
	surface := newTestFixedBottomSurface()
	surface.SetPhysicalWritesEnabled(false)
	return surface
}

// TestPromptEditorPortLegacyStatusLineFallback 固定 L5-2c 门面的 legacy 回落：
// 无 poster（无 actor）时 SetStatusLine 走 surface 本地实现（写入/清除状态行），
// 行为与迁移前直读 surface 一致。
func TestPromptEditorPortLegacyStatusLineFallback(t *testing.T) {
	surface := newPromptEditorPortTestSurface(t)
	port := NewPromptEditorPort(surface, nil)

	if !port.SetStatusLine("草稿 2/3 行") {
		t.Fatal("legacy SetStatusLine must report applied")
	}
	surface.mu.Lock()
	line := surface.promptEditorStatusLine
	surface.mu.Unlock()
	if line != "草稿 2/3 行" {
		t.Fatalf("legacy status line = %q, want 草稿 2/3 行", line)
	}
	if !port.SetStatusLine("") {
		t.Fatal("legacy clear must report applied")
	}
	surface.mu.Lock()
	line = surface.promptEditorStatusLine
	surface.mu.Unlock()
	if line != "" {
		t.Fatalf("legacy clear left %q", line)
	}
}

// TestPromptEditorPortUnifiedPostsStatusLineAction 固定 unified 路径：门面直接
// 投递 SetPromptEditorStatusAction（reducer 权威），surface 不再持有语义状态。
func TestPromptEditorPortUnifiedPostsStatusLineAction(t *testing.T) {
	surface := newPromptEditorPortTestSurface(t)
	var posted []UIAction
	surface.SetUIActorPoster(func(action UIAction) bool {
		posted = append(posted, action)
		return true
	})
	port := NewPromptEditorPort(surface, nil)

	if !port.SetStatusLine("draft 2/3") {
		t.Fatal("unified SetStatusLine must report posted")
	}
	if len(posted) != 1 {
		t.Fatalf("posted = %d actions, want 1", len(posted))
	}
	action, ok := posted[0].(SetPromptEditorStatusAction)
	if !ok || action.Line != "draft 2/3" {
		t.Fatalf("posted action = %#v, want SetPromptEditorStatusAction{Line: draft 2/3}", posted[0])
	}
	surface.mu.Lock()
	line := surface.promptEditorStatusLine
	surface.mu.Unlock()
	if line != "" {
		t.Fatalf("unified posting must not mutate the surface, got %q", line)
	}
}

// TestPromptEditorPortPosterRejectFallsBackToLocalImpl 固定投递被拒（如 actor
// 关闭）时的回落：同步应用到 surface 本地实现，状态一致。
func TestPromptEditorPortPosterRejectFallsBackToLocalImpl(t *testing.T) {
	surface := newPromptEditorPortTestSurface(t)
	surface.SetUIActorPoster(func(UIAction) bool { return false })
	port := NewPromptEditorPort(surface, nil)

	if !port.SetStatusLine("fallback") {
		t.Fatal("rejected post must fall back to local impl")
	}
	surface.mu.Lock()
	line := surface.promptEditorStatusLine
	surface.mu.Unlock()
	if line != "fallback" {
		t.Fatalf("fallback status line = %q, want fallback", line)
	}
}

// TestPromptEditorPortUnifiedMaxVisibleRowsUsesPolicyProjection 固定预算的
// unified 权威源：渲染器同源投影（BottomPanePolicyForGeometry →
// PromptMaxVisibleRows），随 ActiveBand 占行收缩、随几何高度封顶/收缩。
func TestPromptEditorPortUnifiedMaxVisibleRowsUsesPolicyProjection(t *testing.T) {
	surface := newPromptEditorPortTestSurface(t)
	state := BottomPaneState{}
	geometry := GeometryState{Width: 80, Height: 24}
	port := NewPromptEditorPort(surface, func() (BottomPaneState, GeometryState, bool) {
		return state, geometry, true
	})

	baseline := BottomPanePolicyForGeometry(state, geometry).PromptMaxVisibleRows
	if got := port.MaxVisibleRows(); got != baseline {
		t.Fatalf("unified rows = %d, want policy projection %d", got, baseline)
	}

	// 语义探针 1：ActiveBand 占行 → 预算收缩（选取能观察到收缩的几何：
	// 高终端会被 composer 上限封顶、极矮终端 ActiveBandRows=0，均观察不到）。
	bandObserved := false
	for h := 6; h <= 24 && !bandObserved; h++ {
		probe := GeometryState{Width: 80, Height: h}
		state = BottomPaneState{}
		before := BottomPanePolicyForGeometry(state, probe).PromptMaxVisibleRows
		state.ActiveBandLines = []string{"band-1", "band-2"}
		after := BottomPanePolicyForGeometry(state, probe).PromptMaxVisibleRows
		if after >= before {
			continue
		}
		geometry = probe
		if got := port.MaxVisibleRows(); got != after {
			t.Fatalf("rows with active band = %d, want policy projection %d (height %d)", got, after, h)
		}
		bandObserved = true
	}
	if !bandObserved {
		t.Fatal("no geometry found where the active band shrinks the editor budget")
	}

	// 语义探针 2：矮终端收缩且保底 ≥1。
	state = BottomPaneState{}
	geometry = GeometryState{Width: 80, Height: 8}
	short := BottomPanePolicyForGeometry(state, geometry).PromptMaxVisibleRows
	if got := port.MaxVisibleRows(); got != short || short < 1 {
		t.Fatalf("short terminal rows = %d (want %d, >=1)", got, short)
	}

	// 语义探针 3：高终端封顶常规 composer 上限。
	geometry = GeometryState{Width: 80, Height: 200}
	tall := BottomPanePolicyForGeometry(state, geometry).PromptMaxVisibleRows
	if got := port.MaxVisibleRows(); got != tall || tall != ChatComposerMaxVisibleRows {
		t.Fatalf("tall terminal rows = %d (want %d = cap %d)", got, tall, ChatComposerMaxVisibleRows)
	}
}

// TestPromptEditorPortLegacyMaxVisibleRowsMatchesSurfaceImpl 固定 legacy 回落：
// 未接线状态源时预算与 surface 公开方法（impl 主体）一致。
func TestPromptEditorPortLegacyMaxVisibleRowsMatchesSurfaceImpl(t *testing.T) {
	surface := newPromptEditorPortTestSurface(t)
	port := NewPromptEditorPort(surface, nil)

	if got, want := port.MaxVisibleRows(), surface.PromptInputMaxVisibleRows(); got != want {
		t.Fatalf("legacy rows = %d, want surface impl %d", got, want)
	}
}

// TestPromptEditorPortNoSurfaceIsSafe 固定 no-op 端口：无 surface 时调用安全，
// 预算返回常规 composer 上限（与现 disabled/nil 口径一致）。
func TestPromptEditorPortNoSurfaceIsSafe(t *testing.T) {
	port := NewPromptEditorPort(nil, nil)
	if port.SetStatusLine("x") {
		t.Fatal("no-surface SetStatusLine must report false")
	}
	if got := port.MaxVisibleRows(); got != ChatComposerMaxVisibleRows {
		t.Fatalf("no-surface rows = %d, want %d", got, ChatComposerMaxVisibleRows)
	}
}
