package commands

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui"
)

// 回归（2026-10-02 session_20261002165735_Oke7nD4X Esc 失效复盘）：
// wake / actor 直驱回合不经过本地主循环的 ResetInterrupt，中断标志可能
// 残留。旧实现只要 session.IsInterrupted() 为真就永久吞掉后续 Esc，一次
// 未生效的中断即可让 Esc 完全无响应。修复后按“清理是否仍在途”判定：
// 清理已结束 / 从未启动 / 已超期时必须重新发起真实中断。

func newEscapeRetryTestSession(t *testing.T) (*ChatSession, *ui.KeyHandler, *int32) {
	t.Helper()
	kh := ui.NewKeyHandler()
	kh.Start()
	t.Cleanup(kh.Stop)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	var interrupts int32
	session := &ChatSession{
		KeyHandler: kh,
		cancelCtx:  ctx,
		cancelFunc: cancel,
	}
	session.cancelCause = func(error) { atomic.AddInt32(&interrupts, 1) }
	return session, kh, &interrupts
}

func waitForEscapeInterruptCount(counter *int32, want int32, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if atomic.LoadInt32(counter) >= want {
			return true
		}
		time.Sleep(10 * time.Millisecond)
	}
	return atomic.LoadInt32(counter) >= want
}

// 第一次 Esc 的中断清理已经结束、但会话级 interrupted 标志仍残留时，
// 第二次 Esc 必须重新发起中断，而不是被静默吞掉。
func TestStartChatEscapeInterruptWatcherRetriesAfterCleanupCompleted(t *testing.T) {
	session, kh, interrupts := newEscapeRetryTestSession(t)

	stop := startChatEscapeInterruptWatcher(session)
	defer stop()

	kh.Notify()
	if !waitForEscapeInterruptCount(interrupts, 1, 2*time.Second) {
		t.Fatal("expected first ESC to start an interrupt")
	}
	// 等第一次中断清理完成（测试无 host，清理立即结束）并解除信号：
	// 现场即“interrupted 已置位 + 清理已结束”的 wake 回合残留态。
	session.waitForInterruptCleanupWithin(time.Second)
	if !session.IsInterrupted() {
		t.Fatal("precondition: interrupted flag must remain set until the local loop resets it")
	}

	kh.Notify()
	if !waitForEscapeInterruptCount(interrupts, 2, 2*time.Second) {
		t.Fatal("repeated ESC after a completed cleanup must re-issue the interrupt instead of being swallowed")
	}
}

// 中断清理信号超期未关闭（异常路径）时不能永久吞键：Esc 必须解除旧信号
// 并重新发起中断。
func TestStartChatEscapeInterruptWatcherRetriesStalledCleanup(t *testing.T) {
	session, kh, interrupts := newEscapeRetryTestSession(t)
	session.interrupted.Store(true)

	stalled := make(chan struct{})
	session.setInterruptCleanup(stalled)
	session.interruptCleanupMu.Lock()
	session.interruptCleanupStartedAt = time.Now().Add(-2 * chatInterruptCleanupWaitTimeout)
	session.interruptCleanupMu.Unlock()

	stop := startChatEscapeInterruptWatcher(session)
	defer stop()
	kh.Notify()

	if !waitForEscapeInterruptCount(interrupts, 1, 2*time.Second) {
		t.Fatal("ESC must retry the interrupt when the in-flight cleanup signal is stalled")
	}
	session.interruptCleanupMu.Lock()
	current := session.interruptCleanupDone
	session.interruptCleanupMu.Unlock()
	if current == stalled {
		t.Fatal("stalled cleanup signal must be detached before retrying")
	}
}

// 保留 P2-10 的“停止窗口”语义：清理仍在途且未超期时，重复 Esc 不叠加
// 新的中断请求。
func TestStartChatEscapeInterruptWatcherKeepsStoppingWindowWithoutRetry(t *testing.T) {
	session, kh, interrupts := newEscapeRetryTestSession(t)
	session.interrupted.Store(true)

	inFlight := make(chan struct{})
	session.setInterruptCleanup(inFlight)

	stop := startChatEscapeInterruptWatcher(session)
	defer stop()
	kh.Notify()
	time.Sleep(150 * time.Millisecond)

	if got := atomic.LoadInt32(interrupts); got != 0 {
		t.Fatalf("ESC must not stack a new interrupt while a fresh cleanup is in flight, got %d", got)
	}
}

func TestInterruptCleanupStalledDetach(t *testing.T) {
	session := &ChatSession{}

	inFlight := make(chan struct{})
	session.setInterruptCleanup(inFlight)
	if session.interruptCleanupStalled() {
		t.Fatal("fresh in-flight cleanup must not be reported as stalled")
	}
	if session.detachStalledInterruptCleanup() {
		t.Fatal("fresh cleanup must not be detached")
	}

	session.interruptCleanupMu.Lock()
	session.interruptCleanupStartedAt = time.Now().Add(-time.Hour)
	session.interruptCleanupMu.Unlock()
	if !session.interruptCleanupStalled() {
		t.Fatal("cleanup beyond its budget must be reported as stalled")
	}
	if !session.detachStalledInterruptCleanup() {
		t.Fatal("stalled cleanup must be detached")
	}
	if session.isInterruptCleanupInFlight() {
		t.Fatal("detached cleanup must no longer block reserveInterruptCleanup")
	}
	if _, ok := session.reserveInterruptCleanup(); !ok {
		t.Fatal("reserve must succeed after a stalled cleanup is detached")
	}

	completed := make(chan struct{})
	close(completed)
	session.interruptCleanupMu.Lock()
	session.interruptCleanupDone = completed
	session.interruptCleanupStartedAt = time.Now().Add(-time.Hour)
	session.interruptCleanupMu.Unlock()
	if session.interruptCleanupStalled() {
		t.Fatal("closed cleanup signal must never be reported as stalled")
	}
}
