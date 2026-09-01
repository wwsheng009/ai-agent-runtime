package ui

// ExecutorDiagProvider is a callback that returns the current executor recovery
// diagnostics snapshot. It is set by the chat command (or other caller) after
// it creates the TerminalSessionExecutor, so the pprof HTTP handler can expose
// the recovery-loop ring buffer without a direct dependency on the executor's
// lifecycle.
type ExecutorDiagProvider func() ExecutorRecoveryDiag

var executorDiagProvider ExecutorDiagProvider

// SetExecutorDiagProvider registers the callback for the pprof diagnostics
// endpoint. Only one provider is active at a time; the chat command sets it
// when it attaches the terminal executor.
func SetExecutorDiagProvider(p ExecutorDiagProvider) {
	executorDiagProvider = p
}

// ExecutorDiagSnapshot returns the current executor recovery diagnostics, or
// an empty snapshot if no provider is registered.
func ExecutorDiagSnapshot() ExecutorRecoveryDiag {
	if executorDiagProvider == nil {
		return ExecutorRecoveryDiag{}
	}
	return executorDiagProvider()
}