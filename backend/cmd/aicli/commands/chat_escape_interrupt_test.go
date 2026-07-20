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

	stop := startChatEscapeInterruptWatcher(session)
	defer stop()
	kh.Notify()

	deadline := time.After(2 * time.Second)
	for !session.IsInterrupted() {
		select {
		case <-deadline:
			t.Fatal("expected ESC watcher to interrupt active session")
		default:
			time.Sleep(10 * time.Millisecond)
		}
	}
	if got := queue.pendingCount(); got != 1 {
		t.Fatalf("expected ESC to preserve queued input, got %d", got)
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
