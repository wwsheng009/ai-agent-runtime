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
