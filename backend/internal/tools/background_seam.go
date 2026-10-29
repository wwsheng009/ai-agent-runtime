package tools

import (
	"context"
	"fmt"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/background"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolkit/tools"
)

// backgroundSubmitter adapts the background manager to the shell tools'
// BackgroundSubmitter seam, so detach=true launches become supervised jobs
// (captured output, CAS lifecycle, process tree, supervision wake) instead of
// the deprecated fire-and-forget path (2026-10-11).
type backgroundSubmitter struct {
	manager *background.Manager
}

func (s *backgroundSubmitter) SubmitBackgroundShell(ctx context.Context, sessionID, command, cwd string) (string, string, error) {
	if s == nil || s.manager == nil {
		return "", "", fmt.Errorf("background manager is not configured")
	}
	job, err := s.manager.SubmitShell(ctx, sessionID, background.BackgroundTaskArgs{
		Command: command,
		Cwd:     cwd,
	})
	if err != nil || job == nil {
		return "", "", err
	}
	return job.ID, string(job.Status), nil
}

func (s *backgroundSubmitter) OpenInteractiveSession(ctx context.Context, sessionID, command, cwd string, tty bool, cols, rows int) (string, string, error) {
	if s == nil || s.manager == nil {
		return "", "", fmt.Errorf("background manager is not configured")
	}
	job, err := s.manager.OpenTerminal(ctx, sessionID, background.TerminalOpenArgs{
		Command: command,
		Cwd:     cwd,
		TTY:     tty,
		Cols:    cols,
		Rows:    rows,
	})
	if err != nil || job == nil {
		return "", "", err
	}
	return job.ID, string(job.Status), nil
}

// SetBackgroundSubmitter wires the seam onto every registered shell tool
// implementing SetBackgroundSubmitter (bash/shell/execute_shell_command share
// one BashTool implementation). Pass nil to revert to the legacy
// fire-and-forget detach launcher (2026-10-11).
func (m *Manager) SetBackgroundSubmitter(submitter tools.BackgroundSubmitter) {
	if m == nil || m.toolkit == nil {
		return
	}
	for _, tool := range m.toolkit.List() {
		if configurable, ok := tool.(interface {
			SetBackgroundSubmitter(tools.BackgroundSubmitter)
		}); ok {
			configurable.SetBackgroundSubmitter(submitter)
		}
	}
}

// SetShellJobRuntime wires the same manager into the shell tools' job-first
// path. It is separate from the legacy detach seam so the manager remains the
// only job backend while detach is retired incrementally.
func (m *Manager) SetShellJobRuntime(runtime tools.ShellJobRuntime) {
	if m == nil || m.toolkit == nil {
		return
	}
	for _, tool := range m.toolkit.List() {
		if configurable, ok := tool.(interface {
			SetShellJobRuntime(tools.ShellJobRuntime)
		}); ok {
			configurable.SetShellJobRuntime(runtime)
		}
	}
}

// NewBackgroundSubmitter returns the broker-side seam implementation for a
// background manager (nil manager yields a submitter whose calls fail with a
// clear error rather than silently falling back).
func NewBackgroundSubmitter(manager *background.Manager) tools.BackgroundSubmitter {
	return &backgroundSubmitter{manager: manager}
}

// shellJobRuntime adapts the one background manager to the toolkit shell job
// seam. It deliberately owns no state and exposes no second store/lifecycle.
type shellJobRuntime struct {
	manager *background.Manager
}

// NewShellJobRuntime returns the manager-backed runtime injected into shell
// tools. A nil manager produces fail-closed errors, never a local direct path.
func NewShellJobRuntime(manager *background.Manager) tools.ShellJobRuntime {
	return &shellJobRuntime{manager: manager}
}

func (r *shellJobRuntime) SubmitShell(ctx context.Context, sessionID, command, cwd string) (string, string, error) {
	if r == nil || r.manager == nil {
		return "", "", fmt.Errorf("background manager is not configured")
	}
	job, err := r.manager.SubmitShell(ctx, sessionID, background.BackgroundTaskArgs{
		Command: command,
		Cwd:     cwd,
	})
	if err != nil {
		return "", "", err
	}
	return job.ID, string(job.Status), nil
}

func (r *shellJobRuntime) WaitShell(ctx context.Context, jobID string, timeout time.Duration) (tools.ShellJobSnapshot, error) {
	if r == nil || r.manager == nil {
		return tools.ShellJobSnapshot{}, fmt.Errorf("background manager is not configured")
	}
	job, timedOut, err := r.manager.WaitJob(ctx, jobID, timeout)
	if err != nil {
		return tools.ShellJobSnapshot{}, err
	}
	return tools.ShellJobSnapshot{
		ID:       job.ID,
		Status:   string(job.Status),
		ExitCode: job.ExitCode,
		TimedOut: timedOut,
	}, nil
}

func (r *shellJobRuntime) ReadShell(ctx context.Context, jobID string, offset int64, limit int) (tools.ShellJobOutput, error) {
	if r == nil || r.manager == nil {
		return tools.ShellJobOutput{}, fmt.Errorf("background manager is not configured")
	}
	result, err := r.manager.ReadOutput(ctx, background.TaskOutputArgs{
		JobID:  jobID,
		Offset: offset,
		Limit:  limit,
	})
	if err != nil {
		return tools.ShellJobOutput{}, err
	}
	return tools.ShellJobOutput{
		Output:     result.Output,
		NextOffset: result.NextOffset,
		Status:     result.Status,
		ExitCode:   result.ExitCode,
	}, nil
}

func (r *shellJobRuntime) CancelShell(ctx context.Context, jobID string) error {
	if r == nil || r.manager == nil {
		return fmt.Errorf("background manager is not configured")
	}
	_, err := r.manager.CancelJob(ctx, jobID)
	return err
}

func (r *shellJobRuntime) ForgetShell(ctx context.Context, jobID, sessionID string) error {
	if r == nil || r.manager == nil {
		return fmt.Errorf("background manager is not configured")
	}
	return r.manager.ForgetJob(ctx, jobID, sessionID)
}
