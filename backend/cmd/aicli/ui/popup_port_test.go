package ui

import (
	"strings"
	"testing"
)

// newPopupPortTestSurface 构造门面测试用 surface：合成几何 + 关闭物理写
// （legacy 本地实现只推进状态，不向进程 stdout 发射字节）。
func newPopupPortTestSurface(t *testing.T) *FixedBottomSurface {
	t.Helper()
	surface := newTestFixedBottomSurface()
	surface.SetPhysicalWritesEnabled(false)
	return surface
}

// TestPopupPortLegacyHandleLifecycle 固定 L5-2 Batch B 门面的 legacy 回落语义：
// 无 poster（无 actor）时 Begin/Update/Clear/HasActive 全部走 surface 本地实现，
// 行为与迁移前直读 surface 一致（handle 实例、owner、composer 行、状态清理）。
func TestPopupPortLegacyHandleLifecycle(t *testing.T) {
	surface := newPopupPortTestSurface(t)
	port := NewPopupPort(surface, nil)

	handle := port.BeginPopupInputForOwner([]string{"选择模型 A"}, "pick> ", "modal:selection")
	if !handle.Valid() {
		t.Fatal("legacy begin must return a valid handle")
	}
	surface.mu.Lock()
	owner, instance, composer := surface.popupOwner, surface.popupInstance, surface.composerLine
	surface.mu.Unlock()
	if owner != "modal:selection" || instance != handle.instance || composer != "pick> " {
		t.Fatalf("legacy begin state = owner %q instance %d composer %q, want handle %v", owner, instance, composer, handle)
	}
	if !port.HasActivePopup() {
		t.Fatal("legacy HasActivePopup must report the active popup")
	}

	if !port.UpdatePopupInputForHandle(handle, []string{"选择模型 B"}, "pick> B", true) {
		t.Fatal("legacy update must be accepted")
	}
	surface.mu.Lock()
	lines := strings.Join(surface.popupLines, "|")
	composer = surface.composerLine
	surface.mu.Unlock()
	if lines != "选择模型 B" || composer != "pick> B" {
		t.Fatalf("legacy update state = lines %q composer %q", lines, composer)
	}

	port.ClearPopupHandlePreserveCursor(handle)
	if port.HasActivePopup() {
		t.Fatal("legacy clear must deactivate the popup")
	}
	surface.mu.Lock()
	owner, instance = surface.popupOwner, surface.popupInstance
	surface.mu.Unlock()
	if owner != "" || instance != 0 {
		t.Fatalf("legacy clear left owner %q instance %d", owner, instance)
	}
}

// TestPopupPortLegacyPriorityStackRestore 固定门面路径的优先级/栈恢复语义：
// 低优先级 begin 被压栈、clear 高优先级后原样恢复（同一 handle 实例）。
func TestPopupPortLegacyPriorityStackRestore(t *testing.T) {
	surface := newPopupPortTestSurface(t)
	port := NewPopupPort(surface, nil)

	first := port.BeginPopupInputForOwner([]string{"选择模型"}, "pick> ", "modal:selection")
	second := port.BeginPopupInputForOwner([]string{"审批"}, "approve> ", "modal:priority:approval")
	if !first.Valid() || !second.Valid() || first.instance == second.instance {
		t.Fatalf("expected distinct valid handles, first=%v second=%v", first, second)
	}
	surface.mu.Lock()
	owner := surface.popupOwner
	surface.mu.Unlock()
	if owner != "modal:priority:approval" {
		t.Fatalf("higher priority popup must be active, owner=%q", owner)
	}

	port.ClearPopupHandlePreserveCursor(second)
	surface.mu.Lock()
	owner, instance, composer := surface.popupOwner, surface.popupInstance, surface.composerLine
	lines := strings.Join(surface.popupLines, "|")
	surface.mu.Unlock()
	if owner != "modal:selection" || instance != first.instance || composer != "pick> " || lines != "选择模型" {
		t.Fatalf("stack restore = owner %q instance %d composer %q lines %q, want first %v",
			owner, instance, composer, lines, first)
	}

	port.ClearPopupHandlePreserveCursor(first)
	if port.HasActivePopup() {
		t.Fatal("clearing the restored popup must deactivate the popup")
	}
}

// TestPopupPortLegacyFocusAttribution 固定两形态的光标归属（BottomFocus 派生）：
// merged composer（带输入行）由 popup 接管；body-only popup 保持底部 prompt 归属。
func TestPopupPortLegacyFocusAttribution(t *testing.T) {
	surface := newPopupPortTestSurface(t)
	port := NewPopupPort(surface, nil)

	surface.ShowPrompt("> ")
	port.ShowPopupInputForOwner(nil, "answer> ", "modal:agent_panel")
	surface.mu.Lock()
	state := surface.bottomPaneStateLocked()
	surface.mu.Unlock()
	if state.ComposerLine != "answer> " || state.Focus != BottomFocusPopup {
		t.Fatalf("merged composer popup must own focus: composer=%q focus=%v", state.ComposerLine, state.Focus)
	}

	port.ClearPopup()
	surface.ShowPrompt("> ")
	port.BeginPopupInputForOwnerWithViewport([]string{"[提问] 需要执行哪些改动？"}, "", "priority_prompt", PopupViewportSpec{})
	surface.mu.Lock()
	state = surface.bottomPaneStateLocked()
	surface.mu.Unlock()
	if strings.TrimSpace(state.ComposerLine) != "" {
		t.Fatalf("body-only popup must not publish a composer row, got %q", state.ComposerLine)
	}
	if !state.PromptVisible || state.Focus != BottomFocusPrompt {
		t.Fatalf("body-only popup must keep the bottom prompt cursor owner: promptVisible=%v focus=%v", state.PromptVisible, state.Focus)
	}
}

// TestPopupPortUnifiedPostsOrderedActionsWithAllocatedHandle 固定 unified 路径：
// 门面分配 token 后投递 durable action（surface 不变更），begin→update→clear
// 以同一 token 保持 FIFO；token 单调递增。
func TestPopupPortUnifiedPostsOrderedActionsWithAllocatedHandle(t *testing.T) {
	surface := newPopupPortTestSurface(t)
	var posted []UIAction
	surface.SetUIActorPoster(func(action UIAction) bool {
		posted = append(posted, action)
		return true
	})
	port := NewPopupPort(surface, func() (BottomPaneState, bool) { return BottomPaneState{}, true })

	handle := port.BeginPopupInputForOwner([]string{"first"}, "pick> ", "modal:selection")
	if !handle.Valid() {
		t.Fatal("unified begin must allocate a valid handle before posting")
	}
	if !port.UpdatePopupInputForHandle(handle, []string{"second"}, "pick> ", true) {
		t.Fatal("unified update must report accepted")
	}
	port.ClearPopupHandlePreserveCursor(handle)

	if len(posted) != 3 {
		t.Fatalf("posted = %d actions, want begin/update/clear", len(posted))
	}
	begin, ok := posted[0].(ShowPopupAction)
	if !ok || begin.Handle == nil || *begin.Handle != handle || !begin.Input {
		t.Fatalf("begin action = %#v, want tokenized input popup with handle %v", posted[0], handle)
	}
	if update, ok := posted[1].(UpdatePopupAction); !ok || update.Handle != handle {
		t.Fatalf("update action = %#v, want handle %v", posted[1], handle)
	}
	if clear, ok := posted[2].(ClearPopupAction); !ok || clear.Handle == nil || *clear.Handle != handle {
		t.Fatalf("clear action = %#v, want handle %v", posted[2], handle)
	}

	next := port.BeginPopupInputForOwner([]string{"third"}, "pick> ", "modal:selection")
	if !next.Valid() || next.instance <= handle.instance {
		t.Fatalf("token allocation must be monotonic: first=%d next=%d", handle.instance, next.instance)
	}

	surface.mu.Lock()
	owner, lines := surface.popupOwner, len(surface.popupLines)
	surface.mu.Unlock()
	if owner != "" || lines != 0 {
		t.Fatalf("unified posting must not mutate the surface: owner=%q lines=%d", owner, lines)
	}
}

// TestPopupPortUnifiedHasActivePopupUsesBottomPaneState 固定 HasActive 的
// unified 权威源：BottomPaneState 为准（surface 字段可能陈旧），stateSource
// 未接线（ok=false）时回落 surface 本地状态。
func TestPopupPortUnifiedHasActivePopupUsesBottomPaneState(t *testing.T) {
	surface := newPopupPortTestSurface(t)
	state := BottomPaneState{}
	sourceOK := true
	port := NewPopupPort(surface, func() (BottomPaneState, bool) { return state, sourceOK })

	// surface 残留旧状态不得影响 unified 判据。
	surface.mu.Lock()
	surface.popupOwner = "modal:selection"
	surface.popupLines = []string{"stale"}
	surface.mu.Unlock()
	if port.HasActivePopup() {
		t.Fatal("unified query must ignore stale surface popup fields")
	}

	state = BottomPaneState{PopupOwner: "modal:selection", PopupLines: []string{"live"}}
	if !port.HasActivePopup() {
		t.Fatal("unified query must report an active popup from BottomPaneState")
	}

	state = BottomPaneState{PopupStack: []PopupLayer{{Owner: "modal:selection"}}}
	if !port.HasActivePopup() {
		t.Fatal("stacked popup layer must count as active")
	}

	state = BottomPaneState{}
	if port.HasActivePopup() {
		t.Fatal("empty BottomPaneState must report no active popup")
	}

	// stateSource 未接线：回落 surface（此时 surface 仍有旧状态）。
	sourceOK = false
	if !port.HasActivePopup() {
		t.Fatal("unwired state source must fall back to surface state")
	}
}

// TestPopupPortPosterRejectFallsBackToLocalImpl 固定投递被拒（如 actor 关闭）
// 时的回落：门面分配 token 后同步应用到 surface，句柄仍然有效且状态一致。
func TestPopupPortPosterRejectFallsBackToLocalImpl(t *testing.T) {
	surface := newPopupPortTestSurface(t)
	surface.SetUIActorPoster(func(UIAction) bool { return false })
	port := NewPopupPort(surface, nil)

	handle := port.BeginPopupInputForOwner([]string{"fallback"}, "fb> ", "modal:selection")
	if !handle.Valid() {
		t.Fatal("fallback begin must return a valid handle")
	}
	surface.mu.Lock()
	owner, instance := surface.popupOwner, surface.popupInstance
	surface.mu.Unlock()
	if owner != "modal:selection" || instance != handle.instance {
		t.Fatalf("fallback state = owner %q instance %d, want handle %v", owner, instance, handle)
	}
}

// TestPopupPortPendingPasteAndBelowPromptVariants 固定 pending paste 与
// below-prompt 变体的门面语义（legacy 路径）。
func TestPopupPortPendingPasteAndBelowPromptVariants(t *testing.T) {
	surface := newPopupPortTestSurface(t)
	port := NewPopupPort(surface, nil)

	port.ShowPendingPastePreview(2, "line1\nline2")
	surface.mu.Lock()
	owner, lineCount := surface.popupOwner, len(surface.popupLines)
	surface.mu.Unlock()
	if owner != "pending_paste" || lineCount == 0 {
		t.Fatalf("pending paste preview = owner %q lines %d", owner, lineCount)
	}
	port.ClearPendingPastePreview()
	if port.HasActivePopup() {
		t.Fatal("pending paste clear must deactivate the preview")
	}

	port.ShowPopupPreserveCursorForOwnerBelowPrompt([]string{"below"}, "agent_panel")
	surface.mu.Lock()
	below := surface.popupBelowPrompt
	surface.mu.Unlock()
	if !below {
		t.Fatal("below-prompt variant must set popupBelowPrompt")
	}
}

// TestPopupPortNoopWithoutSurface 固定无 surface 的 no-op 端口：全部方法安全，
// 查询返回 false，begin 返回无效句柄。
func TestPopupPortNoopWithoutSurface(t *testing.T) {
	port := NewPopupPort(nil, nil)
	port.ShowPopupInputForOwner([]string{"x"}, "> ", "owner")
	port.ShowPopupPreserveCursorForOwner([]string{"x"}, "owner")
	port.UpdatePopupInputForHandle(PopupHandle{owner: "owner", instance: 1}, []string{"x"}, "> ", true)
	port.ClearPopup()
	port.ClearPopupPreserveCursor()
	port.ClearPopupForOwnerPreserveCursor("owner")
	port.ClearPopupHandlePreserveCursor(PopupHandle{owner: "owner", instance: 1})
	port.ShowPendingPastePreview(1, "x")
	port.ClearPendingPastePreview()

	if port.HasActivePopup() {
		t.Fatal("no-op port must report no active popup")
	}
	if handle := port.BeginPopupInputForOwner([]string{"x"}, "> ", "owner"); handle.Valid() {
		t.Fatalf("no-op port begin must return an invalid handle, got %v", handle)
	}
}
