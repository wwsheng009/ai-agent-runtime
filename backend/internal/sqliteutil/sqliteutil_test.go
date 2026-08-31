package sqliteutil

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

// TestRetryLockedCtxAbortsOnCancellation 验证：fn 持续返回锁冲突错误时，
// ctx 取消后 RetryLockedCtx 必须立即返回 ctx.Err()，而不是继续睡眠重试。
func TestRetryLockedCtxAbortsOnCancellation(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Millisecond)
	defer cancel()

	var attempts int32
	start := time.Now()
	err := RetryLockedCtx(ctx, func() error {
		atomic.AddInt32(&attempts, 1)
		return errors.New("database is locked")
	})
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected ctx cancellation error, got nil")
	}
	if !errors.Is(err, context.DeadlineExceeded) && err != context.Canceled {
		t.Fatalf("expected context error, got: %v", err)
	}
	// 120ms 内必须退出：不能跑满默认 10 次重试（每次至少 50ms 退避 + fn）。
	if elapsed > 2*time.Second {
		t.Fatalf("ctx cancellation did not abort retry loop promptly, took %v", elapsed)
	}
	// ctx 超时前能执行到 1..N 次 fn；绝不能超过重试上限（验证确实提前退出）。
	if n := atomic.LoadInt32(&attempts); n > LockRetries {
		t.Fatalf("retry loop ran %d attempts, expected to stop at/before %d", n, LockRetries)
	}
}

// TestRetryLockedCtxSuccessAfterRetries 验证：fn 先失败几次锁冲突后成功，
// RetryLockedCtx 正常重试并返回 nil。
func TestRetryLockedCtxSuccessAfterRetries(t *testing.T) {
	var attempts int32
	err := RetryLockedCtx(context.Background(), func() error {
		if atomic.AddInt32(&attempts, 1) < 3 {
			return errors.New("database is locked")
		}
		return nil
	})
	if err != nil {
		t.Fatalf("expected success after retries, got: %v", err)
	}
	if n := atomic.LoadInt32(&attempts); n != 3 {
		t.Fatalf("expected 3 attempts, got %d", n)
	}
}

// TestRetryLockedCtxReturnsNonLockedErrorImmediately 验证：非锁冲突错误
// 立即返回，不做退避重试。
func TestRetryLockedCtxReturnsNonLockedErrorImmediately(t *testing.T) {
	var attempts int32
	boom := errors.New("boom")
	err := RetryLockedCtx(context.Background(), func() error {
		atomic.AddInt32(&attempts, 1)
		return boom
	})
	if !errors.Is(err, boom) {
		t.Fatalf("expected boom, got: %v", err)
	}
	if n := atomic.LoadInt32(&attempts); n != 1 {
		t.Fatalf("expected single attempt for non-lock error, got %d", n)
	}
}

// TestRetryLockedCtxExhaustsRetries 验证：始终锁冲突时，重试耗尽后返回
// 最后一个错误。语义：1 次初始尝试 + LockRetries 次重试 = LockRetries+1
// 次 fn 调用（最后一次由 attempt >= LockRetries 终止并返回）。
func TestRetryLockedCtxExhaustsRetries(t *testing.T) {
	var attempts int32
	err := RetryLockedCtx(context.Background(), func() error {
		atomic.AddInt32(&attempts, 1)
		return errors.New("database is locked")
	})
	if err == nil {
		t.Fatal("expected error after retries exhausted")
	}
	if n := atomic.LoadInt32(&attempts); n != LockRetries+1 {
		t.Fatalf("expected %d attempts (1 initial + %d retries), got %d", LockRetries+1, LockRetries, n)
	}
}

// TestRetryLockedCtxNilContext 验证：nil ctx 按 Background 处理，不 panic。
func TestRetryLockedCtxNilContext(t *testing.T) {
	var attempts int32
	err := RetryLockedCtx(nil, func() error {
		atomic.AddInt32(&attempts, 1)
		return nil
	})
	if err != nil {
		t.Fatalf("expected nil, got: %v", err)
	}
	if n := atomic.LoadInt32(&attempts); n != 1 {
		t.Fatalf("expected 1 attempt, got %d", n)
	}
}

// TestRetryLockedWrapsCtxVariant 验证：旧 API 仍可用（Background 包装）。
func TestRetryLockedWrapsCtxVariant(t *testing.T) {
	var attempts int32
	err := RetryLocked(func() error {
		atomic.AddInt32(&attempts, 1)
		if attempts < 2 {
			return errors.New("database table is locked")
		}
		return nil
	})
	if err != nil {
		t.Fatalf("expected success, got: %v", err)
	}
	if n := atomic.LoadInt32(&attempts); n != 2 {
		t.Fatalf("expected 2 attempts, got %d", n)
	}
}
