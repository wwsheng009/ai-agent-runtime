package execution

import (
	"context"
	stderrors "errors"
	"testing"
	"time"

	runtimeerrors "github.com/wwsheng009/ai-agent-runtime/internal/errors"
)

func TestResolveTimeoutKeepsExplicitLongTimeoutWithoutParentDeadline(t *testing.T) {
	ctx := WithTimeoutRequestSource(context.Background(), TimeoutSourceToolArgument)
	budget := ResolveTimeout(ctx, 10*time.Minute)
	if budget.Requested != 10*time.Minute || budget.Effective != 10*time.Minute || budget.Source != TimeoutSourceToolArgument {
		t.Fatalf("unexpected budget: %+v", budget)
	}
}

func TestResolveTimeoutReportsShorterParentDeadlineSource(t *testing.T) {
	base := WithDeadlineSource(context.Background(), TimeoutSourceChatTurnDeadline)
	ctx, cancel := context.WithTimeout(base, 100*time.Millisecond)
	defer cancel()
	ctx = WithTimeoutRequestSource(ctx, TimeoutSourceToolArgument)
	budget := ResolveTimeout(ctx, 3*time.Minute)
	if budget.Source != TimeoutSourceChatTurnDeadline || budget.Effective <= 0 || budget.Effective >= time.Second {
		t.Fatalf("unexpected budget: %+v", budget)
	}
	err := TimeoutError(budget)
	if !runtimeerrors.Is(err, runtimeerrors.ErrTurnDeadlineExceeded) || !stderrors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("unexpected timeout error: %v", err)
	}
}

func TestWithTimeoutSourcePreservesShorterParentDeadlineSource(t *testing.T) {
	parent, parentCancel := WithTimeoutSource(context.Background(), 100*time.Millisecond, TimeoutSourceAgentRunDeadline)
	defer parentCancel()
	turn, turnCancel := WithTimeoutSource(parent, 10*time.Second, TimeoutSourceChatTurnDeadline)
	defer turnCancel()

	budget := ResolveTimeout(WithTimeoutRequestSource(turn, TimeoutSourceToolArgument), 10*time.Minute)
	if budget.Source != TimeoutSourceAgentRunDeadline {
		t.Fatalf("expected parent agent deadline source, got %+v", budget)
	}
}

func TestContextCancellationErrorIncludesConfiguredSource(t *testing.T) {
	err := ContextCancellationError(WithCancelSource(context.Background(), "user_interrupt"))
	var runtimeErr *runtimeerrors.RuntimeError
	if !stderrors.As(err, &runtimeErr) {
		t.Fatalf("expected RuntimeError, got %T", err)
	}
	if got, ok := runtimeErr.GetContextValue("cancel_source"); !ok || got != "user_interrupt" {
		t.Fatalf("unexpected cancel source: %#v", got)
	}
	if !stderrors.Is(err, context.Canceled) {
		t.Fatalf("expected context cancellation unwrap, got %v", err)
	}
}
