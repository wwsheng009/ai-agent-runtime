package team

import "context"

// DetachedTaskExecutionContext keeps context values for logging/tracing while
// detaching teammate task execution from the caller request's cancel/deadline.
func DetachedTaskExecutionContext(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}
	return context.WithoutCancel(ctx)
}
