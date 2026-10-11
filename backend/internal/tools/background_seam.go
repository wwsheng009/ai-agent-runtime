package tools

import (
	"context"
	"fmt"

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

// NewBackgroundSubmitter returns the broker-side seam implementation for a
// background manager (nil manager yields a submitter whose calls fail with a
// clear error rather than silently falling back).
func NewBackgroundSubmitter(manager *background.Manager) tools.BackgroundSubmitter {
	return &backgroundSubmitter{manager: manager}
}
