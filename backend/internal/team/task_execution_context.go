package team

import (
	"context"
	"time"
)

// DetachedTaskExecutionContext keeps context values for logging/tracing while
// detaching teammate task execution from the caller request's cancel/deadline.
func DetachedTaskExecutionContext(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}
	return withoutCancel(ctx)
}

// withoutCancel mirrors context.WithoutCancel (Go 1.21+), which is not
// available on the Go 1.20 toolchain used for Windows 7 compatible builds:
// the returned context is never canceled, has no deadline, and carries no
// values. Keeping the same semantics on every toolchain avoids behavior
// divergence between the win7compat build and the main (go 1.24) build.
func withoutCancel(parent context.Context) context.Context {
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
