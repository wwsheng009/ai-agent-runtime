package team

import (
	"context"
)

// DetachedTaskExecutionContext keeps context values for logging/tracing while
// detaching teammate task execution from the caller request's cancel/deadline.
// context.WithoutCancel (Go 1.21+) is available on every supported toolchain,
// including the Go 1.21.4 Windows 7 compatible build.
func DetachedTaskExecutionContext(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}
	return context.WithoutCancel(ctx)
}
