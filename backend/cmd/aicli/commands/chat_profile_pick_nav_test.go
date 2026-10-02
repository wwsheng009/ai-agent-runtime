package commands

import (
	"bufio"
	"io"
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui"
)

// newTUISelectionSession builds a session shaped like the real TUI one: the
// fixed bottom surface is enabled (so the picker paints a popup input row) and
// a buffered input queue exists (so the queue-vs-editor routing gate is live).
func newTUISelectionSession(t *testing.T) *ChatSession {
	t.Helper()
	oldInteractive := chatIsInteractiveTerminal
	chatIsInteractiveTerminal = func() bool { return true }
	t.Cleanup(func() { chatIsInteractiveTerminal = oldInteractive })

	surface := ui.NewFixedBottomSurface(ui.NewTerminal())
	surface.EnableForTest(80, 24)

	return &ChatSession{
		Surface:    surface,
		InputBox:   ui.NewInputBox(nil),
		InputQueue: newChatInputQueue(bufio.NewReader(strings.NewReader(""))),
	}
}

// ↑/↓ in every runtime selection popup (/model, /profile, /provider, effort …)
// only moves the highlight when the read goes through chatSelectionComposer,
// because that is the sole path that attaches the OnNavigate hook. When the
// read is routed through the queue instead, the modal composer is used, arrows
// fall through to history navigation, and the popup silently stops responding
// to ↑/↓ — exactly the "cannot switch with the arrow keys" symptom.
func TestSelectionReadKeepsArrowNavigationInTUI(t *testing.T) {
	session := newTUISelectionSession(t)

	if !useRuntimeSelectionPopup(session) {
		t.Fatal("precondition: TUI session must paint the selection popup")
	}
	if !shouldUseInteractiveLineEditor(session) {
		t.Fatal("precondition: TUI session must own stdin with the line editor")
	}
	if shouldRoutePriorityPromptThroughQueue(session) {
		t.Fatal("selection read routed through the queue: chatSelectionComposer (and its OnNavigate hook) is bypassed, so ↑/↓ cannot move the highlight")
	}

	handle := beginRuntimeSelectionPopup(session, []string{"coding", "docs"}, "pick: ")
	controller := newRuntimeSelectionController(session, handle, "pick: ", []string{"coding", "docs"}, 0,
		func(selected int, warning string) []string {
			marker := " "
			if selected == 1 {
				marker = ">"
			}
			return []string{marker + "coding", marker + "docs"}
		})

	composer := newChatSelectionComposer(session, "pick: ", controller)
	hooks := composer.hooks()
	if hooks.OnNavigate == nil {
		t.Fatal("selection composer must install OnNavigate so ↑/↓ move the highlight")
	}

	if !hooks.OnNavigate(ui.LineEditorSnapshot{}, 1) {
		t.Fatal("OnNavigate must consume ↑/↓ instead of letting history navigation hijack the modal")
	}
	if controller.Selected() != 1 {
		t.Fatalf("expected ↓ to select index 1, got %d", controller.Selected())
	}
	option, ok := controller.SelectedOption()
	if !ok || option != "docs" {
		t.Fatalf("expected highlighted option docs after ↓, got %q ok=%v", option, ok)
	}
}

// Blank submit must resolve to whatever the arrows highlighted, otherwise the
// highlight is decorative: Enter would always confirm the original row.
func TestRuntimeSelectionPopupBlankSubmitUsesHighlightedOption(t *testing.T) {
	state := newRuntimeModelPickerState([]string{"coding", "docs", "minimal", "review"}, "", 10)
	controller := newRuntimeSelectionController(nil, ui.PopupHandle{}, "pick: ",
		state.filteredOptions(), 0, func(int, string) []string { return nil })

	controller.Navigate(2)
	highlighted, ok := controller.SelectedOption()
	if !ok {
		t.Fatal("expected a highlighted option after navigating")
	}

	_, result := applyRuntimeModelPickerInput(state, "", highlighted)
	if !result.Done {
		t.Fatalf("blank submit must confirm the highlighted option, got message %q", result.Message)
	}
	if result.Selected != highlighted {
		t.Fatalf("blank submit selected %q, want highlighted %q", result.Selected, highlighted)
	}
}

// 复现用户粘贴里的现象：popup 输入行显示「选择 profile（关键词搜索，车确认，q 取消）」——
// 「回」字被吞掉，说明 popup 输入行不是 surface 画的，而是被行编辑器的直写覆盖了。
// chatSelectionComposer 曾只挂 OnChange/OnNavigate/OnCancel，缺 OnTerminalWrite，于是
// readInteractiveLineWithHooksContext 的 writeEditorText 直接往 os.Stdout 写，把 surface
// 拥有的 popup 输入行打穿。TUI 里 chatComposerController / chatBusyComposerCapture /
// chatMergedPromptComposer 都挂了这些 hook，唯独选择弹层没有。
func TestChatSelectionComposerClaimsTerminalWritesOnFixedSurface(t *testing.T) {
	session := newTUISelectionSession(t)
	controller := newRuntimeSelectionController(session, ui.PopupHandle{}, "pick: ", nil, -1,
		func(int, string) []string { return nil })

	hooks := newChatSelectionComposer(session, "pick: ", controller).hooks()

	if !chatComposerUsesFixedSurface(session) {
		t.Fatal("precondition: session must use the fixed bottom surface")
	}
	if hooks.OnTerminalWrite == nil {
		t.Fatal("selection composer must claim editor terminal writes; otherwise the editor writes straight to os.Stdout and corrupts the surface-owned popup input row")
	}
	if !hooks.OnTerminalWrite(ui.LineEditorSnapshot{}, ui.LineEditorRenderSnapshot{}, io.Discard, "pick: ") {
		t.Fatal("OnTerminalWrite must consume the write so no bytes reach os.Stdout over the surface-owned popup input row")
	}
	if !hooks.SuppressSubmitEcho {
		t.Fatal("selection composer must suppress the submit echo: the surface owns the popup input row, so the editor must not emit a bare CRLF over it")
	}
}

// fixed surface 下 popup 输入行归 surface 所有，因此选择弹层的 editor 不该再把
// 输入写进底部 prompt 行（那条路只在无 fixed surface 时成立）。
func TestChatSelectionComposerKeepsPromptRowOffSurfaceSessions(t *testing.T) {
	oldInteractive := chatIsInteractiveTerminal
	chatIsInteractiveTerminal = func() bool { return true }
	t.Cleanup(func() { chatIsInteractiveTerminal = oldInteractive })

	// 无 surface 的终端：走 trackPrompt 分支，hook 不得挂 terminal 接管。
	session := &ChatSession{InputBox: ui.NewInputBox(nil)}
	controller := newRuntimeSelectionController(session, ui.PopupHandle{}, "pick: ", nil, -1,
		func(int, string) []string { return nil })

	hooks := newChatSelectionComposer(session, "pick: ", controller).hooks()
	if chatComposerUsesFixedSurface(session) {
		t.Fatal("precondition: session must not use the fixed bottom surface")
	}
	if hooks.OnTerminalWrite != nil {
		t.Fatal("without a fixed surface the editor must keep owning terminal writes")
	}
	if hooks.SuppressSubmitEcho {
		t.Fatal("without a fixed surface the editor must keep its submit echo")
	}
}
