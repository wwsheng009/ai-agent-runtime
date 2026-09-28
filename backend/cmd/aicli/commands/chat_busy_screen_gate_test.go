package commands

import (
	"context"
	"testing"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui"
)

// P2-4b ④：主循环读输入前等待忙时副屏关闭（以 surface 租约为事实源）。

func newBusyScreenGateSurface(t *testing.T) *ui.FixedBottomSurface {
	t.Helper()
	surface := ui.NewFixedBottomSurface(nil)
	surface.EnableForTest(72, 18)
	return surface
}

func TestWaitForBusyScreenIdleBlocksUntilLeaseReleased(t *testing.T) {
	surface := newBusyScreenGateSurface(t)
	session := &ChatSession{Surface: surface}
	lease, err := surface.AcquireAlternateScreen(context.Background(), ui.FullscreenRequest{Title: "busy-screen"})
	if err != nil {
		t.Fatalf("AcquireAlternateScreen: %v", err)
	}

	done := make(chan error, 1)
	go func() { done <- waitForBusyScreenIdle(session) }()

	select {
	case err := <-done:
		t.Fatalf("gate must block while lease is active, returned %v", err)
	case <-time.After(150 * time.Millisecond):
	}
	if err := lease.Release(context.Background()); err != nil {
		t.Fatalf("Release: %v", err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("gate returned %v after release", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("gate did not observe lease release")
	}
}

func TestWaitForBusyScreenIdleWithoutLeaseIsImmediate(t *testing.T) {
	surface := newBusyScreenGateSurface(t)
	start := time.Now()
	if err := waitForBusyScreenIdle(&ChatSession{Surface: surface}); err != nil {
		t.Fatalf("no-lease gate: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 100*time.Millisecond {
		t.Fatalf("no-lease gate must not wait: %s", elapsed)
	}
	if err := waitForBusyScreenIdle(nil); err != nil {
		t.Fatalf("nil session: %v", err)
	}
	if err := waitForBusyScreenIdle(&ChatSession{}); err != nil {
		t.Fatalf("nil surface: %v", err)
	}
	if chatBusyScreenActiveForSession(&ChatSession{}) {
		t.Fatal("nil surface must not report an active busy screen")
	}
}

func TestWaitForBusyScreenIdleHonoursInterrupt(t *testing.T) {
	surface := newBusyScreenGateSurface(t)
	session := &ChatSession{Surface: surface}
	lease, err := surface.AcquireAlternateScreen(context.Background(), ui.FullscreenRequest{Title: "busy-screen"})
	if err != nil {
		t.Fatalf("AcquireAlternateScreen: %v", err)
	}
	defer func() { _ = lease.Release(context.Background()) }()

	session.Interrupt()
	start := time.Now()
	err = waitForBusyScreenIdle(session)
	if err == nil {
		t.Fatal("interrupted wait must return an error")
	}
	if elapsed := time.Since(start); elapsed > 500*time.Millisecond {
		t.Fatalf("interrupt was not honoured promptly: %s", elapsed)
	}
}

// 集成：prepareInteractiveRead 必须经过该门（跨回合保护的实际落点）。
func TestPrepareInteractiveReadWaitsForBusyScreen(t *testing.T) {
	surface := newBusyScreenGateSurface(t)
	session := &ChatSession{Surface: surface}
	lease, err := surface.AcquireAlternateScreen(context.Background(), ui.FullscreenRequest{Title: "busy-screen"})
	if err != nil {
		t.Fatalf("AcquireAlternateScreen: %v", err)
	}

	done := make(chan error, 1)
	go func() {
		_, _, readErr := prepareInteractiveRead(session)
		done <- readErr
	}()
	select {
	case err := <-done:
		t.Fatalf("prepareInteractiveRead must wait for the busy screen, returned %v", err)
	case <-time.After(150 * time.Millisecond):
	}
	if err := lease.Release(context.Background()); err != nil {
		t.Fatalf("Release: %v", err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("prepareInteractiveRead returned %v after release", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("prepareInteractiveRead did not resume after lease release")
	}
}
