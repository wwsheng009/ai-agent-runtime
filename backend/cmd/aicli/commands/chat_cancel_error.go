package commands

import (
	"context"
	"errors"

	runtimeexecution "github.com/wwsheng009/ai-agent-runtime/internal/execution"
)

// userInterruptError is the canonical typed error for a user-initiated
// interrupt. The message stays user-facing ("用户中断"); classification relies
// on the typed cancellation cause (see runtimeexecution.IsCancellation), never
// on this text.
func userInterruptError() error {
	return runtimeexecution.CancellationErrorWithMessage("user_interrupt", "用户中断")
}

// isChatTurnStopError reports whether a turn ended because the runtime stopped
// or superseded its run (host actor stop, run released, session switch) rather
// than because the work failed.
//
// Those endings already publish a user-visible stop: session_end carries
// status=stopped plus the cancel cause, and the runtime renders its own stop
// summary. Rendering the bare cancellation a second time as
// "操作错误: context canceled" turns a normal stop into an apparent failure
// (2026-09-27 report).
//
// Deadlines stay failures: a real turn/request timeout must keep surfacing.
// User interrupts are handled earlier by the caller (session.IsInterrupted),
// which keeps its interrupt-specific recovery hint.
func isChatTurnStopError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	return runtimeexecution.IsCancellation(err)
}
