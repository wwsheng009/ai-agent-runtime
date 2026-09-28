package ui

import (
	"context"
	"errors"
	"testing"
	"time"
)

// P2-4 前置①：租约等待（AcquireAlternateScreenWait）。

func TestAcquireAlternateScreenWaitFreeSurface(t *testing.T) {
	surface := NewFixedBottomSurface(nil)
	surface.EnableForTest(80, 24)

	lease, err := surface.AcquireAlternateScreenWait(context.Background(), FullscreenRequest{Title: "free"}, time.Second)
	if err != nil {
		t.Fatalf("free surface should acquire immediately: %v", err)
	}
	if lease == nil || lease.ID() == 0 || !lease.Active() {
		t.Fatalf("lease not active: %+v", lease)
	}
	if err := lease.Release(context.Background()); err != nil {
		t.Fatalf("Release: %v", err)
	}
}

func TestAcquireAlternateScreenWaitWaitsForRelease(t *testing.T) {
	surface := NewFixedBottomSurface(nil)
	surface.EnableForTest(80, 24)
	ctx := context.Background()

	held, err := surface.AcquireAlternateScreen(ctx, FullscreenRequest{Title: "held"})
	if err != nil {
		t.Fatalf("initial acquire: %v", err)
	}
	go func() {
		time.Sleep(25 * time.Millisecond)
		_ = held.Release(context.Background())
	}()

	start := time.Now()
	lease, err := surface.AcquireAlternateScreenWait(ctx, FullscreenRequest{Title: "waiter"}, 2*time.Second)
	if err != nil {
		t.Fatalf("wait should succeed after release: %v", err)
	}
	if elapsed := time.Since(start); elapsed < 15*time.Millisecond {
		t.Fatalf("wait returned too early (%s); it did not observe the in-flight lease", elapsed)
	}
	if lease.ID() == held.ID() {
		t.Fatalf("waiter must get a fresh lease id, got %d", lease.ID())
	}
	if err := lease.Release(context.Background()); err != nil {
		t.Fatalf("Release waiter: %v", err)
	}
}

func TestAcquireAlternateScreenWaitBudgetExhausted(t *testing.T) {
	surface := NewFixedBottomSurface(nil)
	surface.EnableForTest(80, 24)
	ctx := context.Background()

	held, err := surface.AcquireAlternateScreen(ctx, FullscreenRequest{Title: "held"})
	if err != nil {
		t.Fatalf("initial acquire: %v", err)
	}
	defer func() { _ = held.Release(context.Background()) }()

	start := time.Now()
	lease, err := surface.AcquireAlternateScreenWait(ctx, FullscreenRequest{Title: "waiter"}, 40*time.Millisecond)
	if !errors.Is(err, ErrScreenLeaseBusy) {
		t.Fatalf("want ErrScreenLeaseBusy, got %v (lease=%v)", err, lease)
	}
	if elapsed := time.Since(start); elapsed < 30*time.Millisecond {
		t.Fatalf("budget was not honoured: elapsed=%s", elapsed)
	}
	if lease != nil {
		t.Fatalf("no lease may be returned on timeout")
	}
}

func TestAcquireAlternateScreenWaitContextCancel(t *testing.T) {
	surface := NewFixedBottomSurface(nil)
	surface.EnableForTest(80, 24)

	held, err := surface.AcquireAlternateScreen(context.Background(), FullscreenRequest{Title: "held"})
	if err != nil {
		t.Fatalf("initial acquire: %v", err)
	}
	defer func() { _ = held.Release(context.Background()) }()

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()
	start := time.Now()
	_, err = surface.AcquireAlternateScreenWait(ctx, FullscreenRequest{Title: "waiter"}, 5*time.Second)
	if !errors.Is(err, ErrScreenLeaseBusy) || !errors.Is(err, context.Canceled) {
		t.Fatalf("want busy wrapping context.Canceled, got %v", err)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("cancel was not honoured promptly: %s", elapsed)
	}
}

// 非租约类错误（未启用/无 transport）必须立即返回，不进入等待。
func TestAcquireAlternateScreenWaitPropagatesFatalErrors(t *testing.T) {
	disabled := NewFixedBottomSurface(nil)
	start := time.Now()
	_, err := disabled.AcquireAlternateScreenWait(context.Background(), FullscreenRequest{Title: "x"}, 2*time.Second)
	if !errors.Is(err, ErrFullScreenUnavailable) {
		t.Fatalf("want ErrFullScreenUnavailable, got %v", err)
	}
	if elapsed := time.Since(start); elapsed > 200*time.Millisecond {
		t.Fatalf("fatal error must not wait: %s", elapsed)
	}

	var nilSurface *FixedBottomSurface
	if _, err := nilSurface.AcquireAlternateScreenWait(context.Background(), FullscreenRequest{Title: "x"}, time.Second); err == nil {
		t.Fatalf("nil surface must fail")
	}
}
