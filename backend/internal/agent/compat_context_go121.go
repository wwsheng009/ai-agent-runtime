//go:build go1.21

package agent

import (
	"context"
	"time"
)

// On the main toolchain, delegate directly to the standard library. The
// Go 1.20 implementation lives in compat_context.go for Windows 7 builds.
func agentWithoutCancel(parent context.Context) context.Context {
	return context.WithoutCancel(parent)
}

func agentWithTimeoutCause(parent context.Context, d time.Duration, cause error) (context.Context, context.CancelFunc) {
	return context.WithTimeoutCause(parent, d, cause)
}

func agentContextCause(ctx context.Context) error {
	return context.Cause(ctx)
}
