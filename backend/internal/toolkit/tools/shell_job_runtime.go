package tools

import (
	"context"
	"time"
)

// ShellJobRuntime is the core seam between a shell tool and the shared
// background job manager. The toolkit owns the interface so it does not import
// the broker/background package; the host supplies the single manager-backed
// implementation.
type ShellJobRuntime interface {
	SubmitShell(ctx context.Context, sessionID, command, cwd string) (jobID, status string, err error)
	WaitShell(ctx context.Context, jobID string, timeout time.Duration) (ShellJobSnapshot, error)
	ReadShell(ctx context.Context, jobID string, offset int64, limit int) (ShellJobOutput, error)
	CancelShell(ctx context.Context, jobID string) error
	ForgetShell(ctx context.Context, jobID, sessionID string) error
}

// ShellJobSnapshot is the terminal or timed-out view of one job.
type ShellJobSnapshot struct {
	ID       string
	Status   string
	ExitCode *int
	TimedOut bool
}

// ShellJobOutput is one cursor read from a job.
type ShellJobOutput struct {
	Output     string
	NextOffset int64
	Status     string
	ExitCode   *int
}
