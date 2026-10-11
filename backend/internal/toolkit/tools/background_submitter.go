package tools

import "context"

// BackgroundSubmitter is the toolkit↔broker seam for managed background jobs.
// Shell tools (bash/shell/execute_shell_command) consult it when a caller asks
// for detach=true so the launch is routed through the supervised background
// manager (captured output, job lifecycle, process tree, supervision wake)
// instead of the deprecated fire-and-forget path (2026-10-11).
//
// The interface is defined here (toolkit layer) and injected via
// SetBackgroundSubmitter by internal/tools/manager.go, so the toolkit never
// imports the broker while the broker-side implementation satisfies it. The
// owning session id is read from the ambient context via toolctx.SessionID
// (the executor path already carries it).
type BackgroundSubmitter interface {
	// SubmitBackgroundShell submits a shell command as a managed one-shot job
	// and returns the job id plus its initial status.
	SubmitBackgroundShell(ctx context.Context, sessionID, command, cwd string) (jobID, status string, err error)
	// OpenInteractiveSession opens a long-lived interactive session
	// (interactive=true) and returns the job id plus its initial status.
	OpenInteractiveSession(ctx context.Context, sessionID, command, cwd string, tty bool, cols, rows int) (jobID, status string, err error)
}
