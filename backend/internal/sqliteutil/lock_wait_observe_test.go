package sqliteutil

import (
	"context"
	"errors"
	"testing"
	"time"
)

// TestRetryLockedCtxObserved_NoWaitOnFirstSuccess 钉住"无等待不上报"：
// 状态面用它区分"锁等待 p95=0（没发生过竞争）"与"没接线"。
func TestRetryLockedCtxObserved_NoWaitOnFirstSuccess(t *testing.T) {
	var samples []time.Duration
	err := RetryLockedCtxObserved(context.Background(), func(d time.Duration) {
		samples = append(samples, d)
	}, func() error { return nil })
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(samples) != 0 {
		t.Fatalf("expected no wait samples, got %v", samples)
	}
}

// TestRetryLockedCtxObserved_AccumulatesWaits 钉住累计语义：
// 两次锁冲突（50ms + 100ms 退避）后成功 → 上报一次 150ms。
func TestRetryLockedCtxObserved_AccumulatesWaits(t *testing.T) {
	var samples []time.Duration
	attempts := 0
	err := RetryLockedCtxObserved(context.Background(), func(d time.Duration) {
		samples = append(samples, d)
	}, func() error {
		attempts++
		if attempts < 3 {
			return errors.New("database is locked (5) (SQLITE_BUSY)")
		}
		return nil
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if attempts != 3 {
		t.Fatalf("expected 3 attempts, got %d", attempts)
	}
	if len(samples) != 1 {
		t.Fatalf("expected exactly one aggregated sample, got %v", samples)
	}
	if want := lockRetryWait(0) + lockRetryWait(1); samples[0] != want {
		t.Fatalf("waited = %v, want %v", samples[0], want)
	}
}

// TestRetryLockedCtxObserved_NonLockErrorNoWait 钉住"非锁错误不重试也不上报"。
func TestRetryLockedCtxObserved_NonLockErrorNoWait(t *testing.T) {
	boom := errors.New("no such table: files")
	var samples []time.Duration
	err := RetryLockedCtxObserved(context.Background(), func(d time.Duration) {
		samples = append(samples, d)
	}, func() error { return boom })
	if !errors.Is(err, boom) {
		t.Fatalf("expected boom error, got %v", err)
	}
	if len(samples) != 0 {
		t.Fatalf("expected no wait samples, got %v", samples)
	}
}

// TestRetryLockedCtxObserved_CancelStillReports 钉住"等待被取消截断仍上报"：
// 取消发生在退避等待中时，已累计的等待必须进入样本（否则最坏形态不可见）。
func TestRetryLockedCtxObserved_CancelStillReports(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	var samples []time.Duration
	attempts := 0
	go func() {
		// 第一次退避（50ms）开始后取消；给退避一个进入 select 的窗口。
		time.Sleep(10 * time.Millisecond)
		cancel()
	}()
	err := RetryLockedCtxObserved(ctx, func(d time.Duration) {
		samples = append(samples, d)
	}, func() error {
		attempts++
		return errors.New("database is locked (5) (SQLITE_BUSY)")
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
	if attempts != 1 {
		t.Fatalf("expected 1 attempt before cancel, got %d", attempts)
	}
	if len(samples) != 1 {
		t.Fatalf("expected one sample even on cancel, got %v", samples)
	}
}
