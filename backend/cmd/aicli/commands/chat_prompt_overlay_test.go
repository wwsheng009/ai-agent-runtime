package commands

import (
	"strings"
	"testing"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui"
)

func TestChatPromptOverlayClearSelectionPopupResetsPromptWithoutSurface(t *testing.T) {
	session := &ChatSession{}
	coord := newChatInteractionCoordinator(session)
	session.Interaction = coord
	coord.SetPromptInput("stale popup input")

	clearRuntimeSelectionPopup(session)

	if snapshot := coord.PromptInputSnapshot(); snapshot.Text != "" {
		t.Fatalf("expected selection popup cleanup to reset prompt without surface, got %#v", snapshot)
	}
}

func TestChatPromptOverlayShowSelectionPopupDoesNotClearPromptWithoutSurface(t *testing.T) {
	session := &ChatSession{}
	coord := newChatInteractionCoordinator(session)
	session.Interaction = coord
	coord.SetPromptInput("main draft")

	showRuntimeSelectionPopup(session, []string{"option"}, "select> ")

	if snapshot := coord.PromptInputSnapshot(); snapshot.Text != "main draft" {
		t.Fatalf("expected non-surface popup show to leave draft intact, got %#v", snapshot)
	}
}

func TestChatPromptOverlayClearComposerPreviewResetsPromptWithoutSurface(t *testing.T) {
	session := &ChatSession{}
	coord := newChatInteractionCoordinator(session)
	session.Interaction = coord
	coord.SetPromptInput("secret draft")

	clearRuntimeComposerPrompt(session)

	if snapshot := coord.PromptInputSnapshot(); snapshot.Text != "" {
		t.Fatalf("expected composer preview cleanup to reset prompt without surface, got %#v", snapshot)
	}
}

func TestChatPromptOverlayPendingPasteDraftFallsBackWithoutSurface(t *testing.T) {
	overlay := newChatPromptOverlay(&ChatSession{})

	if overlay.showPendingPasteDraft(2, "first\nsecond") {
		t.Fatal("expected pending paste draft preview to fall back without surface")
	}
	if overlay.clearPendingPasteDraft() {
		t.Fatal("expected pending paste draft cleanup to fall back without surface")
	}
}

func TestChatPromptOverlayPriorityPromptFallbackOutputContract(t *testing.T) {
	t.Setenv("NO_COLOR", "1")

	session := &ChatSession{}
	coord := newChatInteractionCoordinator(session)
	session.Interaction = coord
	coord.SetPromptInput("approval draft")

	var readPrompt string
	var transient bool
	output := captureStdout(t, func() {
		var cleanup func()
		readPrompt, cleanup, transient = showChatRuntimePriorityPrompt(session, []string{
			"[approval] command=git status",
		}, "[approval] allow bash? [y/N]: \n")
		cleanup()
	})

	if transient {
		t.Fatal("expected fallback priority prompt to be persistent")
	}
	if readPrompt != "[approval] allow bash? [y/N]: " {
		t.Fatalf("expected sanitized readable prompt, got %q", readPrompt)
	}
	if !strings.Contains(output, "[approval] command=git status") || !strings.Contains(output, "[approval] allow bash? [y/N]: ") {
		t.Fatalf("expected fallback output to include approval details and prompt, got %q", output)
	}
	if snapshot := coord.PromptInputSnapshot(); snapshot.Text != "" {
		t.Fatalf("expected priority prompt fallback to clear active draft before output, got %#v", snapshot)
	}
}

func TestChatPromptOverlayPriorityPromptsSerializeBeforeDisplayAndRead(t *testing.T) {
	session := &ChatSession{}
	_, firstCleanup, _ := showChatRuntimePriorityPrompt(session, []string{"first request"}, "first> ")

	secondShown := make(chan func(), 1)
	secondReadStarted := make(chan struct{}, 1)
	go func() {
		_, cleanup, _ := showChatRuntimePriorityPrompt(session, []string{"second request"}, "second> ")
		secondShown <- cleanup
		secondReadStarted <- struct{}{}
	}()

	select {
	case <-secondShown:
		t.Fatal("expected second priority prompt to remain blocked before display")
	case <-secondReadStarted:
		t.Fatal("expected second priority prompt not to enter its read phase")
	case <-time.After(50 * time.Millisecond):
	}

	firstCleanup()
	var secondCleanup func()
	select {
	case secondCleanup = <-secondShown:
	case <-time.After(time.Second):
		t.Fatal("expected second priority prompt to display after first cleanup")
	}
	select {
	case <-secondReadStarted:
	case <-time.After(time.Second):
		t.Fatal("expected second prompt read phase after display serialization")
	}
	secondCleanup()
}

func TestChatPromptOverlayPriorityPromptCleanupIsIdempotent(t *testing.T) {
	session := &ChatSession{}
	_, firstCleanup, _ := showChatRuntimePriorityPrompt(session, []string{"first"}, "first> ")
	firstCleanup()
	firstCleanup()

	_, secondCleanup, _ := showChatRuntimePriorityPrompt(session, []string{"second"}, "second> ")
	thirdShown := make(chan func(), 1)
	go func() {
		_, cleanup, _ := showChatRuntimePriorityPrompt(session, []string{"third"}, "third> ")
		thirdShown <- cleanup
	}()
	select {
	case <-thirdShown:
		t.Fatal("expected second prompt to retain the serialization lock")
	case <-time.After(50 * time.Millisecond):
	}
	secondCleanup()
	secondCleanup()
	select {
	case cleanup := <-thirdShown:
		cleanup()
	case <-time.After(time.Second):
		t.Fatal("expected exactly one unlock to release the third prompt")
	}
}

func TestPriorityPromptViewportUsesStructuralHeaderBodyAndFooter(t *testing.T) {
	viewport := priorityPromptViewport([]string{
		"queued input suspended",
		"approval request",
		"tool=shell",
		"reason=policy",
		"risk=high",
		"operation choices",
	})
	if got := strings.Join(viewport.HeaderLines, "\n"); got != "queued input suspended | approval request" {
		t.Fatalf("unexpected semantic header: %q", got)
	}
	if got := strings.Join(viewport.BodyLines, "\n"); got != "tool=shell | reason=policy | risk=high" {
		t.Fatalf("unexpected semantic body: %q", got)
	}
	if got := strings.Join(viewport.FooterLines, "\n"); got != "operation choices" {
		t.Fatalf("unexpected semantic footer: %q", got)
	}
}

func TestChatModalComposerPrompt_SurfacePopupInputFollowsTypedText(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	ui.SetTheme(ui.ThemeAuto)

	const width, height = 80, 24
	surface := ui.NewFixedBottomSurface(ui.NewTerminal())
	surface.EnableForTest(width, height)
	surface.SetPhysicalWritesEnabled(false)

	session := &ChatSession{Surface: surface}
	lines := []string{
		"[提问] 问题：需要执行哪些文档改动？",
		"[提问] 1. 全部按推荐执行",
	}
	readPrompt, cleanup, transient := showChatRuntimePriorityPrompt(session, lines, "请输入回答，可输入建议编号（必答）：\n")
	if !transient {
		t.Fatal("expected surface priority prompt to run in transient popup mode")
	}
	if !session.priorityPopupHandle.Valid() {
		t.Fatal("expected surface priority prompt to retain its popup handle")
	}

	composer := newChatModalComposerPrompt(session, readPrompt)
	composer.onChange(ui.LineEditorSnapshot{Text: "2"})

	// popup 输入行必须进化成“提示 + 输入”，这样 compose/legacy 光标列
	// （行显示宽度 + 1）才会跟随输入末尾，而不是钉在静态提示后的第一列。
	frameText := commandResultFrameText(surface)
	if !strings.Contains(frameText, readPrompt+"2") {
		t.Fatalf("expected popup input line %q to include typed text, frame:\n%s", readPrompt+"2", frameText)
	}

	// 继续输入：输入行继续增长并保留提示前缀。
	composer.onChange(ui.LineEditorSnapshot{Text: "2, 3"})
	frameText = commandResultFrameText(surface)
	if !strings.Contains(frameText, readPrompt+"2, 3") {
		t.Fatalf("expected popup input line %q after second change, frame:\n%s", readPrompt+"2, 3", frameText)
	}

	// cleanup 使 handle 失效；此后的输入更新必须是无副作用的安全路径。
	cleanup()
	if session.priorityPopupHandle.Valid() {
		t.Fatal("expected priority popup handle to be cleared after cleanup")
	}
	if session.priorityPopupLines != nil {
		t.Fatal("expected priority popup lines to be cleared after cleanup")
	}
	composer.onChange(ui.LineEditorSnapshot{Text: "3"}) // must not panic
}

func TestChatModalComposerPrompt_SurfacePopupInputWithoutActivePopupIsNoop(t *testing.T) {
	surface := ui.NewFixedBottomSurface(ui.NewTerminal())
	surface.EnableForTest(80, 24)
	surface.SetPhysicalWritesEnabled(false)

	session := &ChatSession{Surface: surface}
	composer := newChatModalComposerPrompt(session, "prompt> ")
	composer.onChange(ui.LineEditorSnapshot{Text: "x"}) // 无活跃 popup，no-op
}
