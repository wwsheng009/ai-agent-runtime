package commands

import (
	"bufio"
	"os"
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/renderengine"
)

// 提问回答必须并入用户底部主 prompt：问题正文仍以 popup 呈现，但 popup 不再
// 拥有独立的回答输入行（ComposerLine），回答输入与光标复用普通 prompt 状态。
func TestChatQuestionAnswerMergedIntoBottomPromptReadsThroughPromptRow(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	ui.SetTheme(ui.ThemeAuto)

	surface := ui.NewFixedBottomSurface(ui.NewTerminal())
	surface.EnableForTest(80, 24)
	surface.SetPhysicalWritesEnabled(false)

	restoreStdio := withTransientStdio(t, "2\n")
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
	// 生产路径由 presenter 上报终端几何；这里显式投递一次 Resize，便于从
	// actor AppState 派生可比较的文本帧与光标。
	if actor := coord.ensureUIActor(); actor != nil {
		actor.Post(ui.Resize{Width: 80, Height: 24, Generation: 1})
		coord.waitUIActorIdle()
	}
	coord.SetPromptInput("未提交草稿")

	if !chatMergedAnswerPromptSupported(session) {
		t.Fatal("expected fixed surface session to support merged answer input")
	}

	body := append(
		[]string{"[提问] 问题：需要执行哪些文档改动？"},
		answerPromptBodyLines("请输入回答，可输入建议编号（必答）：\n")...,
	)
	cleanup, ok := showChatRuntimePriorityPromptBody(session, body)
	if !ok {
		t.Fatal("expected question popup body without dedicated input row")
	}
	// 合并路径不得占用 popup 输入通道：answer 输入属于底部 prompt，popup 只保留正文。
	if session.priorityPopupHandle.Valid() {
		t.Fatal("merged answer prompt must not claim the popup input (ComposerLine) channel")
	}

	// 回答输入并入底部 prompt（与 chatMergedPromptComposer 的顺序一致）。
	coord.SetPromptInput("")
	if !coord.ShowAnswerPrompt() {
		t.Fatal("expected answer prompt to reuse the fixed bottom prompt row")
	}
	coord.SetPromptInput("2")
	coord.waitUIActorIdle()
	if coord.uiActor == nil {
		t.Fatal("expected coordinator UI actor for surface projection")
	}
	state := coord.uiActor.AppState()
	if state.Bottom.ComposerLine != "" {
		t.Fatalf("question body must not publish a popup input row, got %q", state.Bottom.ComposerLine)
	}
	popupText := strings.Join(state.Bottom.PopupLines, "\n")
	if !strings.Contains(popupText, "[提问] 问题：需要执行哪些文档改动？") {
		t.Fatalf("expected question body in popup rows: %#v", state.Bottom.PopupLines)
	}
	if !strings.Contains(popupText, "请输入回答，可输入建议编号（必答）：") {
		t.Fatalf("expected answer hint to stay visible in popup rows: %#v", state.Bottom.PopupLines)
	}
	if state.Bottom.PromptInput != "2" {
		t.Fatalf("expected the answer to be rendered on the bottom prompt, got %q", state.Bottom.PromptInput)
	}
	if state.Bottom.Focus != ui.BottomFocusPrompt {
		t.Fatalf("focus = %v, want BottomFocusPrompt while typing the answer", state.Bottom.Focus)
	}

	// 光标必须停在底部 prompt 输入行并跟随已输入答案，而不是停在 popup 正文行。
	frame := ui.ComposeAppTextLayout(state)
	if frame.Cursor == nil || frame.Cursor.Focus != ui.BottomFocusPrompt {
		t.Fatalf("cursor = %+v, want BottomFocusPrompt (geometry=%+v prompt=%+v plan=%+v)",
			frame.Cursor, state.Geometry, state.Bottom, ui.LayoutAppState(state).Bottom.RowPlan)
	}
	plan := ui.LayoutAppState(state).Bottom.RowPlan
	if frame.Cursor.Row < plan.PromptInputStartRow || frame.Cursor.Row > plan.PromptInputStartRow+plan.PromptInputRows-1 {
		t.Fatalf("cursor row %d outside prompt input rows %d..%d",
			frame.Cursor.Row, plan.PromptInputStartRow, plan.PromptInputStartRow+plan.PromptInputRows-1)
	}
	answerCol := frame.Cursor.Col
	coord.SetPromptInput("23")
	coord.waitUIActorIdle()
	typedFrame := ui.ComposeAppTextLayout(coord.uiActor.AppState())
	if typedFrame.Cursor == nil || typedFrame.Cursor.Col != answerCol+1 || typedFrame.Cursor.Row != frame.Cursor.Row {
		t.Fatalf("cursor %+v did not follow the typed answer from %+v", typedFrame.Cursor, frame.Cursor)
	}
	for _, row := range typedFrame.Rows {
		if row.Row == typedFrame.Cursor.Row && row.Owner == renderengine.RowOwnerPopup {
			t.Fatalf("cursor row %d still owned by popup body: %q", row.Row, row.Text)
		}
	}

	// 恢复未提交草稿后再验证合并读取路径。
	coord.SetPromptInput("未提交草稿")
	draftBefore := coord.PromptInputSnapshot().Text

	line, merged, err := newChatMergedPromptComposer(session).ReadLine()
	afterRead := coord.PromptInputSnapshot().Text
	cleanup()
	afterCleanup := coord.PromptInputSnapshot().Text

	if err != nil {
		t.Fatalf("merged answer read: %v", err)
	}
	if !merged {
		t.Fatal("expected the answer to be read through the bottom prompt")
	}
	if got := strings.TrimSpace(line); got != "2" {
		t.Fatalf("answer = %q, want %q", got, "2")
	}
	// 回答结束后把用户未提交草稿归还，回答不得覆盖草稿。
	if snapshot := coord.PromptInputSnapshot(); snapshot.Text != "未提交草稿" {
		t.Fatalf("expected parked draft to be restored, got %#v (draftBefore=%q afterRead=%q afterCleanup=%q line=%q)", snapshot, draftBefore, afterRead, afterCleanup, line)
	}
	// 回答读取不得消费主循环的共享 reader。
	if next, readErr := session.InputReader.ReadString('\n'); readErr != nil || next != "stale\n" {
		t.Fatalf("expected shared reader to remain untouched, got %q err=%v", next, readErr)
	}
}

func TestChatMergedAnswerPromptFallsBackWithoutSurface(t *testing.T) {
	session := &ChatSession{InputBox: ui.NewInputBox(nil)}
	coord := newTestChatInteractionCoordinator(t, session)
	session.Interaction = coord

	if chatMergedAnswerPromptSupported(session) {
		t.Fatal("expected merged answer input to require the fixed surface")
	}

	line, merged, err := newChatMergedPromptComposer(session).ReadLine()
	if err != nil || merged || line != "" {
		t.Fatalf("expected merged read to decline without surface, got line=%q merged=%v err=%v", line, merged, err)
	}
}

// 合并通道可用但底部 prompt 无法占用（例如非交互会话）时，读取必须放弃合并
// 路径并把用户已输入的草稿原样归还，交给调用方回退到独立输入行。
func TestChatMergedAnswerPromptDeclineRestoresDraft(t *testing.T) {
	surface := ui.NewFixedBottomSurface(ui.NewTerminal())
	surface.EnableForTest(80, 24)
	surface.SetPhysicalWritesEnabled(false)

	session := &ChatSession{
		Surface:       surface,
		InputBox:      ui.NewInputBox(nil),
		NoInteractive: true,
	}
	coord := newTestChatInteractionCoordinator(t, session)
	session.Interaction = coord
	coord.SetSurface(surface)
	coord.SetWriter(os.Stdout)

	if !chatMergedAnswerPromptSupported(session) {
		t.Fatal("expected the merged channel to be offered before the prompt read")
	}
	coord.SetPromptInput("保留的草稿")

	line, merged, err := newChatMergedPromptComposer(session).ReadLine()
	if err != nil || merged || line != "" {
		t.Fatalf("expected merged read to decline, got line=%q merged=%v err=%v", line, merged, err)
	}
	if got := coord.PromptInputSnapshot().Text; got != "保留的草稿" {
		t.Fatalf("declined merged read must hand the parked draft back, got %q", got)
	}
}

func TestAnswerPromptBodyLinesKeepsHintWithoutDedicatedInputRow(t *testing.T) {
	got := answerPromptBodyLines("请输入回答，可输入建议编号（必答）：\n")
	if len(got) != 1 || got[0] != "请输入回答，可输入建议编号（必答）：" {
		t.Fatalf("answer hint body lines = %#v", got)
	}
	if lines := answerPromptBodyLines("\n"); lines != nil {
		t.Fatalf("expected empty hint to produce no body lines, got %#v", lines)
	}
}
