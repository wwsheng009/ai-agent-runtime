package commands

import runtimeexecution "github.com/wwsheng009/ai-agent-runtime/internal/execution"

// userInterruptError is the canonical typed error for a user-initiated
// interrupt. The message stays user-facing ("用户中断"); classification relies
// on the typed cancellation cause (see runtimeexecution.IsCancellation), never
// on this text.
func userInterruptError() error {
	return runtimeexecution.CancellationErrorWithMessage("user_interrupt", "用户中断")
}
