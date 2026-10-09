package commands

import (
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui"
)

// newCommandsPopupPortSurface 构造 popup 门面测试用 surface（合成几何 + 关闭物理写）。
func newCommandsPopupPortSurface(t *testing.T) *ui.FixedBottomSurface {
	t.Helper()
	surface := ui.NewFixedBottomSurface(ui.NewTerminal())
	surface.EnableForTest(80, 24)
	surface.SetPhysicalWritesEnabled(false)
	return surface
}

// TestChatInteractionCoordinatorPopupPortInjection 固定 L5-2 Batch B 的接线：
// SetSurface 注入 popup 门面、卸载/替换即清空、Shutdown 清空；无 surface 时
// currentPopupPort 返回 nil（调用方经 chatSessionPopupPort 安全降级）。
func TestChatInteractionCoordinatorPopupPortInjection(t *testing.T) {
	session := &ChatSession{}
	coord := newTestChatInteractionCoordinator(t, session)

	if port := coord.currentPopupPort(); port != nil {
		t.Fatalf("no surface: popup port must be nil, got %T", port)
	}

	surface := newCommandsPopupPortSurface(t)
	coord.SetSurface(surface)
	port := coord.currentPopupPort()
	if port == nil {
		t.Fatal("SetSurface must inject the popup port")
	}

	// surface 卸载（替换为 nil）即清空门面。
	coord.SetSurface(nil)
	if port := coord.currentPopupPort(); port != nil {
		t.Fatalf("surface unload must clear the popup port: %T", port)
	}

	// Shutdown 清空门面（幂等收尾路径）。
	coord.SetSurface(surface)
	if coord.currentPopupPort() == nil {
		t.Fatal("re-mounted surface must re-inject the popup port")
	}
	coord.Shutdown()
	if port := coord.currentPopupPort(); port != nil {
		t.Fatalf("Shutdown must clear the popup port: %T", port)
	}
}

// TestChatSessionPopupPortFallbackWithoutCoordinator 固定无 coordinator 的
// legacy 回落：session.Surface 直设（测试/headless）时 chatSessionPopupPort
// 构造 surface 本地门面，Begin/Update/Clear/HasActive 行为与迁移前一致。
func TestChatSessionPopupPortFallbackWithoutCoordinator(t *testing.T) {
	surface := newCommandsPopupPortSurface(t)
	session := &ChatSession{Surface: surface}
	port := chatSessionPopupPort(session)
	if port == nil {
		t.Fatal("chatSessionPopupPort must never return nil")
	}

	handle := port.BeginPopupInputForOwner([]string{"fallback"}, "fb> ", "modal:selection")
	if !handle.Valid() {
		t.Fatal("legacy fallback begin must return a valid handle")
	}
	if !port.HasActivePopup() {
		t.Fatal("legacy fallback HasActivePopup must report the active popup")
	}
	if !port.UpdatePopupInputForHandle(handle, []string{"fallback-2"}, "fb> ", true) {
		t.Fatal("legacy fallback update must be accepted")
	}
	port.ClearPopupHandlePreserveCursor(handle)
	if port.HasActivePopup() {
		t.Fatal("legacy fallback clear must deactivate the popup")
	}
}

// TestChatSessionPopupPortNilSessionSafe 固定空会话/空 surface 的 no-op 降级。
func TestChatSessionPopupPortNilSessionSafe(t *testing.T) {
	for _, session := range []*ChatSession{nil, {}} {
		port := chatSessionPopupPort(session)
		port.ShowPopupInputForOwner([]string{"x"}, "> ", "owner")
		port.ClearPopup()
		if port.HasActivePopup() {
			t.Fatalf("no-op port for session %#v must report no active popup", session)
		}
		if handle := port.BeginPopupInputForOwner([]string{"x"}, "> ", "owner"); handle.Valid() {
			t.Fatalf("no-op port must return an invalid handle, got %v", handle)
		}
	}
}

// TestChatSessionPopupPortUnifiedActorSemantics 固定 unified 行为等价：
// 门面动作经 controller 归约（AppState.Bottom 为准），surface 本地 popup 字段
// 不再被写入（所有权上移）；begin→update→clear 以同一 token FIFO 生效；
// HasActivePopup 查询 BottomPaneState。
func TestChatSessionPopupPortUnifiedActorSemantics(t *testing.T) {
	session := &ChatSession{}
	coord := newTestChatInteractionCoordinator(t, session)
	surface := newCommandsPopupPortSurface(t)
	coord.SetSurface(surface)
	session.Interaction = coord

	port := chatSessionPopupPort(session)
	handle := port.BeginPopupInputForOwner([]string{"选择模型"}, "pick> ", "modal:selection")
	if !handle.Valid() {
		t.Fatal("unified begin must return a valid handle")
	}
	coord.waitUIActorIdle()
	actor := coord.currentUIActor()
	if actor == nil {
		t.Fatal("expected the lazily created UI actor")
	}

	state := actor.BottomPaneState()
	if state.PopupOwner != "modal:selection" || state.PopupInstance == 0 {
		t.Fatalf("unified begin state = owner %q instance %d, want a tokenized popup for handle %v", state.PopupOwner, state.PopupInstance, handle)
	}
	if strings.Join(state.PopupLines, "|") != "选择模型" || state.ComposerLine != "pick> " {
		t.Fatalf("unified begin lines = %q composer %q", state.PopupLines, state.ComposerLine)
	}
	if state.Focus != ui.BottomFocusPopup {
		t.Fatalf("merged composer popup must own focus, got %v", state.Focus)
	}
	if !port.HasActivePopup() {
		t.Fatal("unified HasActivePopup must report the active popup from BottomPaneState")
	}
	if surface.HasActivePopup() {
		t.Fatal("unified posting must not write surface-local popup state")
	}

	// FIFO update：同一 handle 在 begin 之后归约。
	if !port.UpdatePopupInputForHandle(handle, []string{"选择模型 B"}, "pick> B", true) {
		t.Fatal("unified update must report accepted")
	}
	coord.waitUIActorIdle()
	state = actor.BottomPaneState()
	if strings.Join(state.PopupLines, "|") != "选择模型 B" || state.ComposerLine != "pick> B" {
		t.Fatalf("unified update state = lines %q composer %q", state.PopupLines, state.ComposerLine)
	}

	// FIFO clear：清理后回归空态。
	port.ClearPopupHandlePreserveCursor(handle)
	coord.waitUIActorIdle()
	state = actor.BottomPaneState()
	if state.PopupOwner != "" || state.PopupInstance != 0 || len(state.PopupLines) != 0 || len(state.PopupStack) != 0 {
		t.Fatalf("unified clear left popup state: %+v", state)
	}
	if port.HasActivePopup() {
		t.Fatal("cleared popup must report inactive")
	}
}

// TestChatSessionPopupPortUnifiedBodyOnlyFocus 固定 body-only popup 的 unified
// 光标归属：不发布 composer 行、底部 prompt 保持可见时 Focus=BottomFocusPrompt。
func TestChatSessionPopupPortUnifiedBodyOnlyFocus(t *testing.T) {
	session := &ChatSession{}
	coord := newTestChatInteractionCoordinator(t, session)
	surface := newCommandsPopupPortSurface(t)
	coord.SetSurface(surface)
	session.Interaction = coord

	surface.ShowPrompt("> ")
	coord.waitUIActorIdle()

	port := chatSessionPopupPort(session)
	handle := port.BeginPopupInputForOwnerWithViewport(
		[]string{"[提问] 需要执行哪些文档改动？"},
		"",
		"modal:priority:approval",
		ui.PopupViewportSpec{HeaderLines: []string{"[提问] 需要执行哪些文档改动？"}},
	)
	if !handle.Valid() {
		t.Fatal("body-only begin must return a valid handle")
	}
	coord.waitUIActorIdle()

	state := coord.currentUIActor().BottomPaneState()
	if strings.TrimSpace(state.ComposerLine) != "" {
		t.Fatalf("body-only popup must not publish a composer row, got %q", state.ComposerLine)
	}
	if !state.PromptVisible || state.Focus != ui.BottomFocusPrompt {
		t.Fatalf("body-only popup must keep the bottom prompt cursor owner: promptVisible=%v focus=%v", state.PromptVisible, state.Focus)
	}
	if !port.HasActivePopup() {
		t.Fatal("body-only popup must count as active")
	}
	port.ClearPopupHandlePreserveCursor(handle)
	coord.waitUIActorIdle()
	if port.HasActivePopup() {
		t.Fatal("body-only clear must report inactive")
	}
}
