//go:build !go1.21

package agent

import (
	"context"
	"sync"
	"time"
)

// 本文件为 Go 1.20 兼容构建（Windows 7 目标）提供 Go 1.21 才加入的
// context.WithoutCancel / context.WithTimeoutCause。context.Cause 已在 Go
// 1.20 提供，因此可用于传播标准库父上下文的取消原因。

// valueOnlyCtx detaches cancellation while preserving values. It is used as
// the parent of a Go 1.20 WithCancelCause context. That standard cancel context
// acts as a "cause anchor": its private Value key shadows the original
// parent's cancellation state, so context.Cause keeps working through
// WithValue and other standard context wrappers.
type valueOnlyCtx struct {
	context.Context
}

func (valueOnlyCtx) Deadline() (deadline time.Time, ok bool) { return }
func (valueOnlyCtx) Done() <-chan struct{}                   { return nil }
func (valueOnlyCtx) Err() error                              { return nil }

// agentWithoutCancel 等价于 context.WithoutCancel（Go 1.21+）：
// 返回的上下文永不过期、不可取消，但仍可读取父上下文中的值。
func agentWithoutCancel(parent context.Context) context.Context {
	if parent == nil {
		panic("cannot create context from nil parent")
	}
	causeAnchor, _ := context.WithCancelCause(valueOnlyCtx{parent})
	return withoutCancelCtx{causeAnchor}
}

type withoutCancelCtx struct {
	context.Context
}

func (withoutCancelCtx) Deadline() (deadline time.Time, ok bool) { return }
func (withoutCancelCtx) Done() <-chan struct{}                   { return nil }
func (withoutCancelCtx) Err() error                              { return nil }
func (c withoutCancelCtx) Value(key any) any                     { return c.Context.Value(key) }

// timeoutCauseCtx 等价于 context.WithTimeoutCause（Go 1.21+）。
type timeoutCauseCtx struct {
	parent      context.Context
	causeErr    error
	deadline    time.Time
	causeCtx    context.Context
	cancelCause context.CancelCauseFunc

	mu    sync.Mutex
	timer *time.Timer
	done  chan struct{}
	err   error
}

func (c *timeoutCauseCtx) Deadline() (deadline time.Time, ok bool) {
	if c == nil || c.deadline.IsZero() {
		return time.Time{}, false
	}
	return c.deadline, true
}

func (c *timeoutCauseCtx) Done() <-chan struct{} { return c.done }

func (c *timeoutCauseCtx) Err() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.err
}

func (c *timeoutCauseCtx) Value(key any) any { return c.causeCtx.Value(key) }

func (c *timeoutCauseCtx) complete(err, cause error) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.err != nil {
		return
	}
	if c.timer != nil {
		c.timer.Stop()
		c.timer = nil
	}
	c.err = err
	c.cancelCause(cause)
	close(c.done)
}

func (c *timeoutCauseCtx) expire() {
	if c.parent.Err() != nil {
		c.cancelFromParent()
		return
	}
	cause := c.causeErr
	if cause == nil {
		cause = context.DeadlineExceeded
	}
	c.complete(context.DeadlineExceeded, cause)
}

func (c *timeoutCauseCtx) cancelFromParent() {
	if c == nil || c.parent == nil {
		return
	}
	err := c.parent.Err()
	if err == nil {
		return
	}
	cause := agentContextCause(c.parent)
	if cause == nil {
		cause = err
	}
	c.complete(err, cause)
}

// agentWithTimeoutCause 等价于 context.WithTimeoutCause（Go 1.21+）：
// 超时后 Err() 返回 context.DeadlineExceeded，agentContextCause 返回调用
// 方提供的 cause；手动取消时二者分别返回 context.Canceled。
func agentWithTimeoutCause(parent context.Context, d time.Duration, cause error) (context.Context, context.CancelFunc) {
	if parent == nil {
		panic("cannot create context from nil parent")
	}
	deadline := time.Now().Add(d)
	parentDeadlineSooner := false
	if parentDeadline, ok := parent.Deadline(); ok && parentDeadline.Before(deadline) {
		deadline = parentDeadline
		parentDeadlineSooner = true
	}
	causeAnchor, cancelCause := context.WithCancelCause(valueOnlyCtx{parent})
	c := &timeoutCauseCtx{
		parent:      parent,
		causeErr:    cause,
		deadline:    deadline,
		causeCtx:    causeAnchor,
		cancelCause: cancelCause,
		done:        make(chan struct{}),
	}
	switch {
	case parent.Err() != nil:
		c.cancelFromParent()
	case parentDeadlineSooner:
		c.watchParent()
	default:
		remaining := time.Until(deadline)
		if remaining <= 0 {
			c.expire()
			break
		}
		c.mu.Lock()
		c.timer = time.AfterFunc(remaining, c.expire)
		c.mu.Unlock()
		c.watchParent()
	}
	cancel := func() {
		c.complete(context.Canceled, context.Canceled)
	}
	return c, cancel
}

func (c *timeoutCauseCtx) watchParent() {
	parentDone := c.parent.Done()
	if parentDone == nil {
		return
	}
	go func() {
		select {
		case <-parentDone:
			c.cancelFromParent()
		case <-c.done:
		}
	}()
}

// agentContextCause 等价于 context.Cause（Go 1.20+）：返回带 cause 的
// 上下文（agentWithTimeoutCause 创建的）所关联的 cause；否则返回 ctx.Err()。
func agentContextCause(ctx context.Context) error {
	return context.Cause(ctx)
}
