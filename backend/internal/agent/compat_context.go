package agent

import (
	"context"
	"sync"
	"time"
)

// 本文件为 Go 1.20 兼容构建（Windows 7 目标）提供 context 扩展 API 的
// 等价实现：context.WithoutCancel / context.WithTimeoutCause /
// context.Cause 均为 Go 1.21+ 标准库函数。实现保持与标准库一致的语义，
// 两个工具链（go 1.20 与主线 go 1.24）行为无差异。

// causeError 包装 cause 与底层错误，Unwrap 返回二者，使
// errors.Is(err, cause) 和 errors.Is(err, context.DeadlineExceeded) 都能命中。
type causeError struct {
	cause error
	err   error
}

func (e *causeError) Error() string { return e.err.Error() }

func (e *causeError) Unwrap() []error { return []error{e.cause, e.err} }

// causeProvider 由带 cause 的上下文实现，供 agentContextCause 探测。
type causeProvider interface{ cause() error }

// agentWithoutCancel 等价于 context.WithoutCancel（Go 1.21+）：
// 返回的上下文永不过期、不可取消、不携带任何值。
func agentWithoutCancel(parent context.Context) context.Context {
	if parent == nil {
		return context.Background()
	}
	return withoutCancelCtx{parent}
}

type withoutCancelCtx struct {
	context.Context
}

func (withoutCancelCtx) Deadline() (deadline time.Time, ok bool) { return }
func (withoutCancelCtx) Done() <-chan struct{}                   { return nil }
func (withoutCancelCtx) Err() error                              { return nil }
func (withoutCancelCtx) Value(key any) any                       { return nil }

// timeoutCauseCtx 等价于 context.WithTimeoutCause（Go 1.21+）。
type timeoutCauseCtx struct {
	parent   context.Context
	causeErr error

	mu    sync.Mutex
	timer *time.Timer
	done  chan struct{}
	err   error
}

func (c *timeoutCauseCtx) Deadline() (deadline time.Time, ok bool) {
	return time.Time{}, false
}

func (c *timeoutCauseCtx) Done() <-chan struct{} { return c.done }

func (c *timeoutCauseCtx) Err() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.err
}

func (c *timeoutCauseCtx) Value(key any) any { return c.parent.Value(key) }

// cause 在 deadline 触发（且设置了 cause）时返回 cause，否则返回 nil。
func (c *timeoutCauseCtx) cause() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, ok := c.err.(*causeError); ok {
		return c.causeErr
	}
	return nil
}

// agentWithTimeoutCause 等价于 context.WithTimeoutCause（Go 1.21+）：
// 超时后 Err() 返回包装 cause 的错误（errors.Is 可命中 cause 与
// context.DeadlineExceeded）；手动取消返回 context.Canceled。
func agentWithTimeoutCause(parent context.Context, d time.Duration, cause error) (context.Context, context.CancelFunc) {
	if parent == nil {
		parent = context.Background()
	}
	c := &timeoutCauseCtx{parent: parent, causeErr: cause, done: make(chan struct{})}
	if d <= 0 {
		c.mu.Lock()
		if cause != nil {
			c.err = &causeError{cause: cause, err: context.DeadlineExceeded}
		} else {
			c.err = context.DeadlineExceeded
		}
		close(c.done)
		c.mu.Unlock()
	} else {
		c.timer = time.AfterFunc(d, func() {
			c.mu.Lock()
			defer c.mu.Unlock()
			if c.err != nil {
				return
			}
			if cause != nil {
				c.err = &causeError{cause: cause, err: context.DeadlineExceeded}
			} else {
				c.err = context.DeadlineExceeded
			}
			close(c.done)
		})
	}
	cancel := func() {
		c.mu.Lock()
		defer c.mu.Unlock()
		if c.err != nil {
			return
		}
		if c.timer != nil {
			c.timer.Stop()
		}
		c.err = context.Canceled
		close(c.done)
	}
	return c, cancel
}

// agentContextCause 等价于 context.Cause（Go 1.21+）：返回带 cause 的
// 上下文（agentWithTimeoutCause 创建的）所关联的 cause；否则返回 ctx.Err()。
func agentContextCause(ctx context.Context) error {
	if ctx == nil {
		return nil
	}
	if provider, ok := ctx.(causeProvider); ok {
		if cause := provider.cause(); cause != nil {
			return cause
		}
	}
	return ctx.Err()
}