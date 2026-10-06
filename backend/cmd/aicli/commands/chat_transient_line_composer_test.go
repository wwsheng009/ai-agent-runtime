package commands

import (
	"bufio"
	"bytes"
	"os"
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui"
)

// transient line（无 prompt 直读）在有固定底栏 surface 时必须折入底部 prompt
// 行：文本/光标经 OnChange/OnTerminalWrite 认领，读结束后归还用户草稿。
func TestChatTransientLineComposerReadsThroughBottomPromptRow(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	ui.SetTheme(ui.ThemeAuto)

	surface := ui.NewFixedBottomSurface(ui.NewTerminal())
	surface.EnableForTest(80, 24)
	surface.SetPhysicalWritesEnabled(false)

	restoreStdio := withTransientStdio(t, "transient answer\n")
	defer restoreStdio()

	session := &ChatSession{
		Surface:     surface,
		InputBox:    ui.NewInputBox(nil),
		InputReader: bufio.NewReader(strings.NewReader("stale\n")),
	}
	coord := newTestChatInteractionCoordinator(t, session)
	session.Interaction = coord
	coord.SetSurface(surface)
	coord.SetWriter(os.Stdout)
	if actor := coord.ensureUIActor(); actor != nil {
		actor.Post(ui.Resize{Width: 80, Height: 24, Generation: 1})
		coord.waitUIActorIdle()
	}
	coord.SetPromptInput("未提交草稿")

	composer := newChatTransientLineComposer(session)
	if !composer.mergedPromptSupported() {
		t.Fatal("expected fixed surface session to support merged transient input")
	}

	line, err := composer.ReadLine()
	if err != nil {
		t.Fatalf("merged transient read: %v", err)
	}
	if got := strings.TrimSpace(line); got != "transient answer" {
		t.Fatalf("line = %q, want %q", got, "transient answer")
	}
	if snapshot := coord.PromptInputSnapshot(); snapshot.Text != "未提交草稿" {
		t.Fatalf("expected parked draft to be restored, got %#v", snapshot)
	}
	// 直读不得消费主循环的共享 reader。
	if next, readErr := session.InputReader.ReadString('\n'); readErr != nil || next != "stale\n" {
		t.Fatalf("expected shared reader to remain untouched, got %q err=%v", next, readErr)
	}
}

// merged hooks 必须把编辑器文本帧认领到 prompt 行：原始 writer 零字节，
// 文本与焦点落在底部 prompt。
func TestChatTransientLineMergedHooksFoldIntoBottomPrompt(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	ui.SetTheme(ui.ThemeAuto)

	surface := ui.NewFixedBottomSurface(ui.NewTerminal())
	surface.EnableForTest(80, 24)
	surface.SetPhysicalWritesEnabled(false)

	session := &ChatSession{Surface: surface, InputBox: ui.NewInputBox(nil)}
	coord := newTestChatInteractionCoordinator(t, session)
	session.Interaction = coord
	coord.SetSurface(surface)
	coord.SetWriter(os.Stdout)
	if actor := coord.ensureUIActor(); actor != nil {
		actor.Post(ui.Resize{Width: 80, Height: 24, Generation: 1})
		coord.waitUIActorIdle()
	}

	composer := newChatTransientLineComposer(session)
	if !composer.mergedPromptSupported() {
		t.Fatal("expected fixed surface session to support merged transient input")
	}
	if !coord.ShowAnswerPrompt() {
		t.Fatal("expected the prompt row to be taken over")
	}
	defer coord.DiscardPrompt()

	hooks := composer.mergedHooks()
	var raw bytes.Buffer
	if claimed := hooks.OnTerminalWrite(ui.LineEditorSnapshot{}, ui.LineEditorRenderSnapshot{}, &raw, "typed"); !claimed {
		t.Fatal("expected editor write to be claimed by the prompt row")
	}
	if raw.Len() != 0 {
		t.Fatalf("claimed editor write leaked to the raw writer: %q", raw.String())
	}
	hooks.OnChange(ui.LineEditorSnapshot{Text: "typed", Cursor: 5})
	coord.waitUIActorIdle()
	if coord.uiActor == nil {
		t.Fatal("expected coordinator UI actor for surface projection")
	}
	state := coord.uiActor.AppState()
	if state.Bottom.PromptInput != "typed" {
		t.Fatalf("expected text on the bottom prompt, got %q", state.Bottom.PromptInput)
	}
	if state.Bottom.Focus != ui.BottomFocusPrompt {
		t.Fatalf("focus = %v, want BottomFocusPrompt", state.Bottom.Focus)
	}
}

// 无 surface 时保持原直读通道（不占用 prompt 行），行为与迁移前一致。
func TestChatTransientLineComposerFallsBackWithoutSurface(t *testing.T) {
	restoreStdio := withTransientStdio(t, "raw line\n")
	defer restoreStdio()

	session := &ChatSession{InputBox: ui.NewInputBox(nil)}
	coord := newTestChatInteractionCoordinator(t, session)
	session.Interaction = coord

	composer := newChatTransientLineComposer(session)
	if composer.mergedPromptSupported() {
		t.Fatal("expected merged transient input to require the fixed surface")
	}
	line, err := composer.ReadLine()
	if err != nil {
		t.Fatalf("raw transient read: %v", err)
	}
	if got := strings.TrimSpace(line); got != "raw line" {
		t.Fatalf("line = %q, want %q", got, "raw line")
	}
}
