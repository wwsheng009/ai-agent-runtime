package commands

import (
	"context"
	"fmt"
	"testing"

	runtimeexecution "github.com/wwsheng009/ai-agent-runtime/internal/execution"
)

// TestIsChatTurnStopErrorClassification pins the boundary between "the runtime
// stopped this turn" (benign; must not render as 操作错误) and real failures
// (timeouts, provider errors) that must keep surfacing.
func TestIsChatTurnStopErrorClassification(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{name: "nil", err: nil, want: false},
		{name: "bare cancel", err: context.Canceled, want: true},
		{name: "wrapped cancel", err: fmt.Errorf("turn stopped: %w", context.Canceled), want: true},
		{name: "typed runtime cancel", err: runtimeexecution.CancellationError("user_interrupt"), want: true},
		{name: "bare deadline", err: context.DeadlineExceeded, want: false},
		{name: "wrapped deadline", err: fmt.Errorf("turn timed out: %w", context.DeadlineExceeded), want: false},
		{name: "provider failure", err: fmt.Errorf("HTTP 400: invalid_encrypted_content"), want: false},
		{name: "timeout budget", err: runtimeexecution.TimeoutError(runtimeexecution.TimeoutBudget{Effective: 1, Source: runtimeexecution.TimeoutSourceChatTurnDeadline}), want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isChatTurnStopError(tc.err); got != tc.want {
				t.Fatalf("isChatTurnStopError(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}
