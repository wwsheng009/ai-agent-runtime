package tools

import (
	"github.com/wwsheng009/ai-agent-runtime/internal/background"
	"github.com/wwsheng009/ai-agent-runtime/internal/shelljob"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolkit/tools"
)

// SetShellJobRuntime wires the shared manager into the shell tools' job-first
// path. There is no separate detach seam.
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

// NewShellJobRuntime returns the manager-backed runtime injected into shell
// tools. A nil manager produces fail-closed errors, never a local direct path.
func NewShellJobRuntime(manager *background.Manager) tools.ShellJobRuntime {
	return shelljob.NewManagerRuntime(manager)
}
