package skills

import (
	"context"
	"time"
)

// withoutCancel 等价于 context.WithoutCancel（Go 1.21+），
// 供 Go 1.20 兼容构建（Windows 7 目标）使用：
// 返回的上下文永不过期、不可取消、不携带任何值。
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