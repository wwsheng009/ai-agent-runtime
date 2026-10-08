package ui

import (
	"context"
	"errors"
	"testing"
	"time"
)

// P2-4 前置①：租约等待（AcquireAlternateScreenWait）。

func TestAcquireAlternateScreenWaitFreeSurface(t *testing.T) {
	surface, _ := newFencedLeaseTestSurface(t)

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
	surface, _ := newFencedLeaseTestSurface(t)
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
	surface, _ := newFencedLeaseTestSurface(t)
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
	surface, _ := newFencedLeaseTestSurface(t)

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

// P2-4b：SetAlternateScreenWaitBudget 让既有 AcquireAlternateScreen 调用点
// （各 S 档 screen handler）无需改动即可获得忙时等待能力。
func TestAcquireAlternateScreenHonorsSurfaceWaitBudget(t *testing.T) {
	surface, _ := newFencedLeaseTestSurface(t)
	ctx := context.Background()

	if got := surface.AlternateScreenWaitBudget(); got != 0 {
		t.Fatalf("default wait budget = %s, want 0 (fail fast)", got)
	}
	held, err := surface.AcquireAlternateScreen(ctx, FullscreenRequest{Title: "held"})
	if err != nil {
		t.Fatalf("initial acquire: %v", err)
	}

	// 默认 0：撞租约立即失败，不等。
	start := time.Now()
	if _, err := surface.AcquireAlternateScreen(ctx, FullscreenRequest{Title: "fast"}); !errors.Is(err, ErrScreenLeaseBusy) {
		t.Fatalf("default budget: want ErrScreenLeaseBusy, got %v", err)
	}
	if elapsed := time.Since(start); elapsed > 200*time.Millisecond {
		t.Fatalf("default budget must not wait: %s", elapsed)
	}

	// 配置预算后：同一调用点等待租约释放并成功获取。
	surface.SetAlternateScreenWaitBudget(time.Second)
	if got := surface.AlternateScreenWaitBudget(); got != time.Second {
		t.Fatalf("budget = %s, want 1s", got)
	}
	go func() {
		time.Sleep(25 * time.Millisecond)
		_ = held.Release(context.Background())
	}()
	start = time.Now()
	lease, err := surface.AcquireAlternateScreen(ctx, FullscreenRequest{Title: "waited"})
	if err != nil {
		t.Fatalf("budgeted acquire: %v", err)
	}
	if elapsed := time.Since(start); elapsed < 15*time.Millisecond {
		t.Fatalf("budgeted acquire returned before the lease was released: %s", elapsed)
	}
	if err := lease.Release(context.Background()); err != nil {
		t.Fatalf("Release: %v", err)
	}
}

func TestAcquireAlternateScreenWaitBudgetCanBeReset(t *testing.T) {
	surface, _ := newFencedLeaseTestSurface(t)
	ctx := context.Background()

	surface.SetAlternateScreenWaitBudget(5 * time.Second)
	surface.SetAlternateScreenWaitBudget(0)
	held, err := surface.AcquireAlternateScreen(ctx, FullscreenRequest{Title: "held"})
	if err != nil {
		t.Fatalf("initial acquire: %v", err)
	}
	defer func() { _ = held.Release(context.Background()) }()

	start := time.Now()
	if _, err := surface.AcquireAlternateScreen(ctx, FullscreenRequest{Title: "fast"}); !errors.Is(err, ErrScreenLeaseBusy) {
		t.Fatalf("reset budget: want ErrScreenLeaseBusy, got %v", err)
	}
	if elapsed := time.Since(start); elapsed > 200*time.Millisecond {
		t.Fatalf("reset budget must not wait: %s", elapsed)
	}
}
