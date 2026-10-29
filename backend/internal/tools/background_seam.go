package tools

import (
	"context"
	"fmt"

	"github.com/wwsheng009/ai-agent-runtime/internal/background"
	"github.com/wwsheng009/ai-agent-runtime/internal/shelljob"
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

// NewShellJobRuntime returns the manager-backed runtime injected into shell
// tools. A nil manager produces fail-closed errors, never a local direct path.
func NewShellJobRuntime(manager *background.Manager) tools.ShellJobRuntime {
	return shelljob.NewManagerRuntime(manager)
}
