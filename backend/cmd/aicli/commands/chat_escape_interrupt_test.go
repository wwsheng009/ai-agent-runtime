package commands

import (
	"context"
	"testing"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui"
)

func TestStartChatEscapeInterruptWatcherInterruptsActiveSession(t *testing.T) {
	kh := ui.NewKeyHandler()
	kh.Start()
	defer kh.Stop()

	ctx, cancel := context.WithCancel(context.Background())
	session := &ChatSession{
		KeyHandler: kh,
		cancelCtx:  ctx,
		cancelFunc: cancel,
	}

	stop := startChatEscapeInterruptWatcher(session)
	defer stop()

	kh.Notify()

	deadline := time.After(2 * time.Second)
	for {
		if session.IsInterrupted() {
			return
		}
		select {
		case <-deadline:
			t.Fatal("expected ESC watcher to interrupt active session")
		default:
			time.Sleep(10 * time.Millisecond)
		}
	}
}

func TestStartChatEscapeInterruptWatcherPreservesQueuedInput(t *testing.T) {
	kh := ui.NewKeyHandler()
	kh.Start()
	defer kh.Stop()

	ctx, cancel := context.WithCancel(context.Background())
	queue := newChatInputQueue(nil)
	queue.routeLine(chatQueuedInput{Text: "follow up", Source: "stdin"})
	session := &ChatSession{
		KeyHandler: kh,
		InputQueue: queue,
		cancelCtx:  ctx,
		cancelFunc: cancel,
	}
	session.Interaction = newTestChatInteractionCoordinator(t, session)

	stop := startChatEscapeInterruptWatcher(session)
	defer stop()
	kh.Notify()

	deadline := time.After(2 * time.Second)
	for !session.IsInterrupted() || queue.pendingCount() != 0 {
		select {
		case <-deadline:
			t.Fatalf("expected ESC watcher to interrupt and restore queued input, got interrupted=%v pending=%d", session.IsInterrupted(), queue.pendingCount())
		default:
			time.Sleep(10 * time.Millisecond)
		}
	}
	if snapshot := session.Interaction.PromptInputSnapshot(); snapshot.Text != "follow up" {
		t.Fatalf("expected queued input restored to composer, got %q", snapshot.Text)
	}
}

func TestStartChatEscapeInterruptWatcherStoppedDoesNotInterruptSession(t *testing.T) {
	kh := ui.NewKeyHandler()
	kh.Start()
	defer kh.Stop()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	session := &ChatSession{
		KeyHandler: kh,
		cancelCtx:  ctx,
		cancelFunc: cancel,
	}

	stop := startChatEscapeInterruptWatcher(session)
	stop()
	kh.Notify()
	time.Sleep(100 * time.Millisecond)

	if session.IsInterrupted() {
		t.Fatal("stopped ESC watcher should not interrupt session")
	}
}

func TestInterruptChatTurnFromBusyInputCancelCancelsActiveTurn(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	session := &ChatSession{
		NoInteractive: true,
		cancelCtx:     ctx,
		cancelFunc:    cancel,
	}

	interruptChatTurnFromBusyInputCancel(session)

	if !session.IsInterrupted() {
		t.Fatal("expected busy-input ESC cancel to mark session interrupted")
	}
	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("expected busy-input ESC cancel to cancel active context")
	}
}

func TestStartChatEscapeInterruptWatcherIgnoresRepeatedEsc(t *testing.T) {
	kh := ui.NewKeyHandler()
	kh.Start()
	defer kh.Stop()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	session := &ChatSession{
		KeyHandler: kh,
		cancelCtx:  ctx,
		cancelFunc: cancel,
	}

	stop := startChatEscapeInterruptWatcher(session)
	defer stop()
	kh.Notify()

	deadline := time.After(2 * time.Second)
	for !session.IsInterrupted() {
		select {
		case <-deadline:
			t.Fatal("expected first ESC to interrupt active session")
		default:
			time.Sleep(10 * time.Millisecond)
		}
	}

	kh.Notify()
	time.Sleep(100 * time.Millisecond)
	if !session.IsInterrupted() {
		t.Fatal("repeated ESC must not clear the interrupted state")
	}
}

// T6a（方案 §7.2 / D-C）：普通忙时输入行敲了半截草稿后按 Esc，必须同时满足
// 「回合中断」与「草稿仍在」；下次 capture 的 InitialText 与中断前一致。
func TestChatBusyInputEscInterruptPreservesHalfTypedDraft(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	session := &ChatSession{cancelCtx: ctx, cancelFunc: cancel}
	coord := newTestChatInteractionCoordinator(t, session)
	session.Interaction = coord
	coord.SetPromptInput("half typed follow-up")

	capture := newChatBusyComposerCapture(session, "> ", false, false)
	hooks := capture.hooks()
	if hooks.InitialText != "half typed follow-up" {
		t.Fatalf("capture InitialText = %q，期望已有草稿", hooks.InitialText)
	}

	// 编辑器 Esc 语义：先 OnCancel(snapshot)，随后 onChange("") 清空语义草稿
	// （ui/inputbox_editor.go:1304-1308）。
	draft := coord.PromptInputSnapshot()
	if !hooks.OnCancel(draft) || !capture.Cancelled() {
		t.Fatal("Esc 应取消忙时 capture")
	}
	hooks.OnChange(ui.LineEditorSnapshot{})
	if got := coord.PromptInputSnapshot().Text; got != "" {
		t.Fatalf("编辑器语义变化：Esc 后草稿 = %q，期望被 onChange 清空", got)
	}

	// 忙时循环在取消后的动作顺序：中断回合 → PreserveDraft（chat_busy_input.go）。
	interruptChatTurnFromBusyInputCancel(session)
	if !session.IsInterrupted() {
		t.Fatal("忙时 Esc 必须中断当前回合")
	}
	select {
	case <-ctx.Done():
	default:
		t.Fatal("忙时 Esc 必须取消当前回合的 context")
	}
	capture.PreserveDraft()

	restored := coord.PromptInputSnapshot()
	if restored.Text != draft.Text || restored.Cursor != draft.Cursor {
		t.Fatalf("Esc 中断后草稿丢失：%#v，期望 %#v", restored, draft)
	}
	next := newChatBusyComposerCapture(session, "> ", false, false).hooks()
	if next.InitialText != draft.Text || next.InitialCursor != draft.Cursor {
		t.Fatalf("下次 capture InitialText = %q cursor = %d，期望 %q/%d", next.InitialText, next.InitialCursor, draft.Text, draft.Cursor)
	}
}
