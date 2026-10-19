package commands

import (
	"io"
	"os"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/renderengine"
)

// /login（及同族文本询问）的提示行必须独占 popup 输入通道（tokenized
// handle）：modal composer 只有在 session.priorityPopupHandle 有效时才把输入
// 折叠进 popup 输入行（foldChatPriorityPromptPopupInput）并认领编辑器的 raw
// 回显（onTerminalWrite）。此前 owner-only popup 没有 handle，编辑器保持 raw
// fallback，其“相对列、不带绝对行号”的回显落在最后绘制行（状态栏）上，输入
// 字符因此覆盖底部状态栏。
func TestChatComposerPreviewRegistersPopupInputHandleAndFoldsInput(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	ui.SetTheme(ui.ThemeAuto)

	surface := ui.NewFixedBottomSurface(ui.NewTerminal())
	surface.EnableForTest(80, 24)
	surface.SetPhysicalWritesEnabled(false)

	restoreStdio := withTransientStdio(t, "unused\n")
	defer restoreStdio()

	session := &ChatSession{
		Surface:  surface,
		InputBox: ui.NewInputBox(nil),
	}
	coord := newTestChatInteractionCoordinator(t, session)
	session.Interaction = coord
	coord.SetSurface(surface)
	coord.SetWriter(os.Stdout)
	if actor := coord.ensureUIActor(); actor != nil {
		actor.Post(ui.Resize{Width: 80, Height: 24, Generation: 1})
		coord.waitUIActorIdle()
	}

	prompt := "Base URL: "
	if !showRuntimeComposerPrompt(session, prompt) {
		t.Fatal("expected composer preview to render on the fixed surface")
	}
	if !session.priorityPopupHandle.Valid() {
		t.Fatal("composer preview must register a popup input handle so the modal composer can fold input")
	}

	// 编辑器回显必须被认领，不再直写终端（否则相对列定位回显会落在当前行）。
	composer := newChatModalComposerPrompt(session, prompt)
	if !composer.onTerminalWrite(ui.LineEditorSnapshot{}, ui.LineEditorRenderSnapshot{}, io.Discard, "https://example.test/v1") {
		t.Fatal("modal composer must claim the editor echo while the preview owns the popup input line")
	}

	// 输入折叠进 popup 输入行：ComposerLine = 提示 + 输入。
	answer := "https://example.test/v1"
	composer.onChange(ui.LineEditorSnapshot{Text: answer})
	coord.waitUIActorIdle()

	if coord.uiActor == nil {
		t.Fatal("expected coordinator UI actor for surface projection")
	}
	state := coord.uiActor.AppState()
	if got := state.Bottom.ComposerLine; got != prompt+answer {
		t.Fatalf("popup composer line = %q, want %q", got, prompt+answer)
	}

	// 光标意图落在 popup 输入行末尾，而不是提示之后的第一列。
	frame := ui.ComposeAppTextLayout(state)
	if frame.Cursor == nil || frame.Cursor.Focus != ui.BottomFocusPopup {
		t.Fatalf("cursor = %+v, want BottomFocusPopup", frame.Cursor)
	}
	lastPopupRow, lastPopupText := 0, ""
	for _, row := range frame.Rows {
		if row.Owner == renderengine.RowOwnerPopup && row.Text != "" {
			lastPopupRow, lastPopupText = row.Row, row.Text
		}
	}
	if lastPopupRow != frame.Cursor.Row || lastPopupText != prompt+answer {
		t.Fatalf("cursor row %d / popup row %d (%q) mismatch, want the folded composer line", frame.Cursor.Row, lastPopupRow, lastPopupText)
	}
	if want := ui.DisplayWidth(prompt+answer) + 1; frame.Cursor.Col != want {
		t.Fatalf("cursor col = %d, want %d (end of the folded input)", frame.Cursor.Col, want)
	}

	// 清理必须释放 popup 输入注册；否则后续 modal composer 会把输入认领进
	// 一个已经不渲染的 popup（文本不可见）。
	clearRuntimeComposerPrompt(session)
	coord.waitUIActorIdle()
	if session.priorityPopupHandle.Valid() {
		t.Fatal("composer preview cleanup must release the popup input handle")
	}
	if chatSessionPopupPort(session).HasActivePopup() {
		t.Fatal("composer preview cleanup must clear the popup")
	}
	if composer.onTerminalWrite(ui.LineEditorSnapshot{}, ui.LineEditorRenderSnapshot{}, io.Discard, "x") {
		t.Fatal("editor echo must not be claimed after the preview handle was released")
	}
}
