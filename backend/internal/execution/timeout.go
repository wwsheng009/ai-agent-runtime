package execution

import (
	"context"
	"fmt"
	"strings"
	"time"

	runtimeerrors "github.com/wwsheng009/ai-agent-runtime/internal/errors"
)

type TimeoutSource string

const (
	TimeoutSourceToolArgument          TimeoutSource = "tool_argument"
	TimeoutSourceToolDefault           TimeoutSource = "tool_default"
	TimeoutSourceChatTurnDeadline      TimeoutSource = "chat_turn_deadline"
	TimeoutSourceAgentRunDeadline      TimeoutSource = "agent_run_deadline"
	TimeoutSourceParentContextDeadline TimeoutSource = "parent_context_deadline"
	TimeoutSourceSandboxPolicy         TimeoutSource = "sandbox_policy"
)

type contextKey string

const (
	deadlineSourceKey contextKey = "execution_deadline_source"
	requestSourceKey  contextKey = "execution_timeout_request_source"
	cancelSourceKey   contextKey = "execution_cancel_source"
)

// TimeoutBudget records the requested timeout and the effective deadline after
// applying parent and policy limits.
type TimeoutBudget struct {
	Requested time.Duration
	Effective time.Duration
	Source    TimeoutSource
}

func WithDeadlineSource(ctx context.Context, source TimeoutSource) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, deadlineSourceKey, source)
}

func WithTimeoutRequestSource(ctx context.Context, source TimeoutSource) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, requestSourceKey, source)
}

// WithTimeoutSource adds a deadline while preserving a shorter parent
// deadline's source when the new timeout does not become authoritative.
func WithTimeoutSource(ctx context.Context, timeout time.Duration, source TimeoutSource) (context.Context, context.CancelFunc) {
	if ctx == nil {
		ctx = context.Background()
	}
	if parentDeadline, ok := ctx.Deadline(); !ok || time.Until(parentDeadline) > timeout {
		ctx = WithDeadlineSource(ctx, source)
	}
	return context.WithTimeout(ctx, timeout)
}

func WithCancelSource(ctx context.Context, source string) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, cancelSourceKey, strings.TrimSpace(source))
}

func CancelSource(ctx context.Context) string {
	if ctx != nil {
		if value, ok := ctx.Value(cancelSourceKey).(string); ok && strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return "parent_context"
}

func ResolveTimeout(ctx context.Context, requested time.Duration) TimeoutBudget {
	source := TimeoutSourceToolDefault
	if ctx != nil {
		if value, ok := ctx.Value(requestSourceKey).(TimeoutSource); ok && strings.TrimSpace(string(value)) != "" {
			source = value
		}
	}
	budget := TimeoutBudget{Requested: requested, Effective: requested, Source: source}
	if ctx == nil {
		return budget
	}
	if deadline, ok := ctx.Deadline(); ok {
		remaining := time.Until(deadline)
		if remaining < 0 {
			remaining = 0
		}
		if budget.Effective <= 0 || remaining < budget.Effective {
			budget.Effective = remaining
			budget.Source = DeadlineSource(ctx)
		}
	}
	return budget
}

func LimitTimeout(budget TimeoutBudget, limit time.Duration, source TimeoutSource) TimeoutBudget {
	if limit > 0 && (budget.Effective <= 0 || limit < budget.Effective) {
		budget.Effective = limit
		budget.Source = source
	}
	return budget
}

func DeadlineSource(ctx context.Context) TimeoutSource {
	if ctx != nil {
		if value, ok := ctx.Value(deadlineSourceKey).(TimeoutSource); ok && strings.TrimSpace(string(value)) != "" {
			return value
		}
	}
	return TimeoutSourceParentContextDeadline
}

func (b TimeoutBudget) Metadata() map[string]interface{} {
	return map[string]interface{}{
		"timeout_requested_ms": b.Requested.Milliseconds(),
		"timeout_effective_ms": b.Effective.Milliseconds(),
		"timeout_source":       string(b.Source),
		"timeout_ms":           b.Effective.Milliseconds(),
	}
}

func TimeoutError(budget TimeoutBudget) error {
	code := runtimeerrors.ErrToolTimeout
	if budget.Source == TimeoutSourceChatTurnDeadline {
		code = runtimeerrors.ErrTurnDeadlineExceeded
	}
	message := fmt.Sprintf("execution timed out after %s", budget.Effective)
	return runtimeerrors.WrapWithContext(code, message, context.DeadlineExceeded, budget.Metadata())
}

func CancellationError(source string) error {
	source = strings.TrimSpace(source)
	if source == "" {
		source = "parent_context"
	}
	metadata := map[string]interface{}{"cancel_source": source}
	return runtimeerrors.WrapWithContext(runtimeerrors.ErrAgentRunCanceled, "agent execution was canceled", context.Canceled, metadata)
}

func ContextCancellationError(ctx context.Context) error {
	return CancellationError(CancelSource(ctx))
}
