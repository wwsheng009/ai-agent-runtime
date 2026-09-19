package execution

import (
	"context"
	stderrors "errors"
	"fmt"
	"strings"
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

func TestCancellationErrorWithMessageKeepsUserFacingTextAndTypedCause(t *testing.T) {
	err := CancellationErrorWithMessage("user_interrupt", "用户中断")
	if !strings.Contains(err.Error(), "用户中断") {
		t.Fatalf("user-facing message lost: %v", err)
	}
	if !IsCancellation(err) || !stderrors.Is(err, context.Canceled) {
		t.Fatalf("typed cancellation lost: %v", err)
	}
	var runtimeErr *runtimeerrors.RuntimeError
	if !stderrors.As(err, &runtimeErr) || runtimeErr.Code != runtimeerrors.ErrAgentRunCanceled {
		t.Fatalf("expected AGENT_RUN_CANCELED code, got %#v", runtimeErr)
	}
}

func TestIsCancellationClassifiesTypedCancellationOnly(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		err  error
		want bool
	}{
		{name: "nil", err: nil, want: false},
		{name: "context canceled", err: context.Canceled, want: true},
		{name: "wrapped context canceled", err: fmt.Errorf("turn aborted: %w", context.Canceled), want: true},
		{name: "runtime cancellation error", err: CancellationError("user_interrupt"), want: true},
		{name: "bare deadline sentinel", err: context.DeadlineExceeded, want: true},
		{name: "wrapped deadline timeout", err: TimeoutError(TimeoutBudget{Effective: time.Second, Source: TimeoutSourceToolDefault}), want: false},
		{name: "diagnostic mentioning interrupt", err: fmt.Errorf("actor wait timed out; press Ctrl+C to interrupt and resume"), want: false},
		{name: "diagnostic mentioning Chinese interrupt", err: fmt.Errorf("actor 等待就绪超时，可 Ctrl+C 中断后重新 resume"), want: false},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := IsCancellation(tc.err); got != tc.want {
				t.Fatalf("IsCancellation(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}
