package commands

import (
	"context"
	"testing"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui"
)

// 阶段 C：会话级 ESC 消费者按引用计数共享，重叠回合（sendMessage + actor 执行器）
// 不再互相 disarm；actor-only 回合（不经过本地 sendMessage）同样可被 TUI ESC 中断。

func newEscapeConsumerTestSession(t *testing.T) (*ChatSession, *ui.KeyHandler) {
	t.Helper()
	kh := ui.NewKeyHandler()
	kh.Start()
	t.Cleanup(kh.Stop)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	return &ChatSession{
		KeyHandler: kh,
		cancelCtx:  ctx,
		cancelFunc: cancel,
	}, kh
}

func waitForEscapeInterrupt(t *testing.T, session *ChatSession) {
	t.Helper()
	deadline := time.After(2 * time.Second)
	for {
		if session.IsInterrupted() {
			return
		}
		select {
		case <-deadline:
			t.Fatal("expected ESC consumer to interrupt the session")
		default:
			time.Sleep(10 * time.Millisecond)
		}
	}
}

func TestChatEscapeConsumerSharesArmAcrossOverlappingTurns(t *testing.T) {
	session, kh := newEscapeConsumerTestSession(t)

	releaseLocalTurn := startChatEscapeInterruptWatcher(session)
	releaseActorTurn := startChatEscapeInterruptWatcher(session)
	if !kh.Armed() {
		t.Fatal("expected overlapping consumers to keep the key handler armed")
	}

	releaseLocalTurn()
	if !kh.Armed() {
		t.Fatal("expected the surviving actor consumer to keep the key handler armed")
	}

	kh.Notify()
	waitForEscapeInterrupt(t, session)

	releaseActorTurn()
	if kh.Armed() {
		t.Fatal("expected the key handler to disarm after the last consumer released")
	}
}

func TestChatEscapeConsumerStopsConsumingAfterRelease(t *testing.T) {
	session, kh := newEscapeConsumerTestSession(t)

	release := startChatEscapeInterruptWatcher(session)
	release()
	release() // 重复释放必须安全（幂等）

	if kh.Armed() {
		t.Fatal("expected released consumer to disarm the key handler")
	}

	kh.Notify()
	time.Sleep(80 * time.Millisecond)
	if session.IsInterrupted() {
		t.Fatal("expected released consumer to stop consuming ESC events")
	}
}

func TestChatEscapeConsumerInterruptsActorOnlyTurn(t *testing.T) {
	session, kh := newEscapeConsumerTestSession(t)

	// 模拟 actor/executor 回合：没有 sendMessage 作用域的消费者。
	release := startChatEscapeInterruptWatcher(session)
	defer release()

	kh.Notify()
	waitForEscapeInterrupt(t, session)
}

// P2-10：中断后的重复 Esc 不再完全静默，但每个中断周期只提示一次。
func TestChatEscapeStoppingNoticeIsRateLimitedPerInterrupt(t *testing.T) {
	session, kh := newEscapeConsumerTestSession(t)
	release := startChatEscapeInterruptWatcher(session)
	defer release()

	kh.Notify()
	waitForEscapeInterrupt(t, session)

	kh.Notify()
	deadline := time.After(2 * time.Second)
	for !session.escapeStoppingNoticeShown.Load() {
		select {
		case <-deadline:
			t.Fatal("expected repeated Esc to show the stopping notice once")
		default:
			time.Sleep(10 * time.Millisecond)
		}
	}
	if session.chatEscapeStoppingNoticeDue() {
		t.Fatal("stopping notice must be rate limited within one interrupt cycle")
	}

	session.ResetInterrupt()
	if !session.chatEscapeStoppingNoticeDue() {
		t.Fatal("a new interrupt cycle must allow the stopping notice again")
	}

	var nilSession *ChatSession
	if nilSession.chatEscapeStoppingNoticeDue() {
		t.Fatal("nil session must not report a stopping notice")
	}
}
