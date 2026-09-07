package agent

import (
	"context"
	"time"
)

// agentWithoutCancel / agentWithTimeoutCause / agentContextCause 直接委托标准库
// context.WithoutCancel / WithTimeoutCause / Cause（Go 1.21+）。Win7 兼容构建
// （Go 1.21.4）与主线（Go 1.25）共用同一实现，无行为分叉；Go 1.20 时代的
// 手工兼容实现已随工具链冻结在 1.21.4 而删除。
func agentWithoutCancel(parent context.Context) context.Context {
	return context.WithoutCancel(parent)
}

func agentWithTimeoutCause(parent context.Context, d time.Duration, cause error) (context.Context, context.CancelFunc) {
	return context.WithTimeoutCause(parent, d, cause)
}

func agentContextCause(ctx context.Context) error {
	return context.Cause(ctx)
}
